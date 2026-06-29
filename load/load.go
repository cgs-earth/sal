package load

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
)

type LoadCmd struct {
	BatchSize           int    `arg:"--batch-size" help:"Arrow records per batch" default:"131072"`
	Workers             int    `arg:"--workers" help:"number of input files to convert to Parquet in parallel" default:"8"`
	ParquetCompression  string `arg:"--compression" help:"Parquet compression codec: snappy, zstd, gzip, brotli, lz4, uncompressed" default:"snappy"`
	MetricsMode         string `arg:"--metrics-mode" help:"Iceberg metrics mode: none, counts, truncate(N), full" default:"truncate(16)"`
	TargetFileSizeBytes int64  `arg:"--target-file-size-bytes" help:"target Iceberg data file size" default:"0"`
	InputDir            string `arg:"positional,required" placeholder:"PATH" help:"path to a directory containing .nq.gz files"`
	MaxFiles            int    `arg:"--max-files" help:"maximum number of input files to process" default:"0"`
	Warehouse           string `arg:"--warehouse" help:"Iceberg warehouse directory" default:"/tmp/iceberg-warehouse"`
	Namespace           string `arg:"--namespace" help:"Iceberg namespace" default:"default"`
}

func Run(cfg *LoadCmd) error {

	ctx := context.Background()

	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	if err != nil {
		return fmt.Errorf("failed to create catalog: %w", err)
	}

	tbl, err := NewIcebergTableFromCfg(ctx, cat, cfg)
	if err != nil {
		return fmt.Errorf("failed to create Iceberg table: %w", err)
	}

	pattern := filepath.Join(cfg.InputDir, "*.nq.gz")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no .nq.gz files found in %s", cfg.InputDir)
	}
	if cfg.MaxFiles > 0 && len(files) > cfg.MaxFiles {
		files = files[:cfg.MaxFiles]
	}
	slog.Info("Found" + fmt.Sprint(len(files)) + ".nq.gz file(s)")

	if err := applyWriteProperties(ctx, tbl, cfg); err != nil {
		return err
	}
	slog.Info("Write settings",
		"workers", cfg.Workers,
		"batch_size", cfg.BatchSize,
		"compression", cfg.ParquetCompression,
		"metrics", cfg.MetricsMode,
		"target_file_size", cfg.TargetFileSizeBytes,
	)

	if err := processFiles(ctx, files, cat, tbl.Identifier(), arrowSchema, cfg.BatchSize, cfg.Workers); err != nil {
		return err
	}

	slog.Info("All files loaded successfully. Table present at " + tbl.Location())
	return nil
}

// processFiles writes each .nq.gz input to Iceberg data files in parallel, then
// commits all of the produced data files in one table snapshot.
func processFiles(
	ctx context.Context,
	files []string,
	cat catalog.Catalog,
	tableIdent table.Identifier,
	arrowSchema *arrow.Schema,
	batchSize int,
	workers int,
) error {
	tbl, err := cat.LoadTable(ctx, tableIdent)
	if err != nil {
		return fmt.Errorf("load table: %w", err)
	}

	dataFiles, rows, err := writeFilesInParallel(ctx, tbl, files, arrowSchema, batchSize, workers)
	if err != nil {
		return err
	}
	if len(dataFiles) == 0 {
		return fmt.Errorf("no triples found")
	}

	txn := tbl.NewTransaction()
	if err := txn.AddDataFiles(ctx, dataFiles, iceberg.Properties(nil), table.WithoutDuplicateCheck()); err != nil {
		return fmt.Errorf("stage data files: %w", err)
	}
	if _, err := txn.Commit(ctx); err != nil {
		return fmt.Errorf("commit data files: %w", err)
	}

	log.Printf("  committed %d parquet data file(s) with %d triples in one snapshot", len(dataFiles), rows)
	return nil
}

func writeFilesInParallel(
	ctx context.Context,
	tbl *table.Table,
	files []string,
	arrowSchema *arrow.Schema,
	batchSize int,
	workers int,
) ([]iceberg.DataFile, int64, error) {
	if workers > len(files) {
		workers = len(files)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		dataFiles []iceberg.DataFile
		rows      int64
		err       error
	}

	jobs := make(chan string)
	results := make(chan result, len(files))
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Go(func() {
			for path := range jobs {
				dataFiles, rows, err := writeOneInputFile(ctx, tbl, path, arrowSchema, batchSize)
				if err != nil {
					cancel()
				}
				results <- result{dataFiles: dataFiles, rows: rows, err: err}
			}
		})
	}

	go func() {
		defer close(jobs)
		for _, path := range files {
			select {
			case jobs <- path:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	var allDataFiles []iceberg.DataFile
	var totalRows int64
	for res := range results {
		if res.err != nil {
			return nil, 0, res.err
		}
		allDataFiles = append(allDataFiles, res.dataFiles...)
		totalRows += res.rows
	}

	return allDataFiles, totalRows, nil
}

func writeOneInputFile(
	ctx context.Context,
	tbl *table.Table,
	path string,
	arrowSchema *arrow.Schema,
	batchSize int,
) ([]iceberg.DataFile, int64, error) {
	rdr := newNQuadRecordReader([]string{path}, arrowSchema, batchSize)
	defer rdr.Release()

	records := retainedRecordIterator(rdr)
	var dataFiles []iceberg.DataFile
	for df, err := range table.WriteRecords(ctx, tbl, arrowSchema, records) {
		if err != nil {
			return nil, 0, fmt.Errorf("write %s: %w", path, err)
		}
		dataFiles = append(dataFiles, df)
	}
	if err := rdr.Err(); err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", path, err)
	}

	log.Printf("  wrote %s as %d parquet data file(s), %d triples", path, len(dataFiles), rdr.RowsRead())
	return dataFiles, rdr.RowsRead(), nil
}

func retainedRecordIterator(rdr *nquadRecordReader) func(func(arrow.RecordBatch, error) bool) {
	return func(yield func(arrow.RecordBatch, error) bool) {
		for rdr.Next() {
			rec := rdr.RecordBatch()
			rec.Retain()
			if !yield(rec, nil) {
				return
			}
		}
		if err := rdr.Err(); err != nil {
			yield(nil, err)
		}
	}
}
