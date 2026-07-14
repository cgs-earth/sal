package load

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	rdflibgo "github.com/tggo/goRDFlib"
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
	DataTypeCols        bool   `arg:"--data-type-cols" help:"Split distinct data types into separate columns" default:"false"`
}

// WriteGraphToIceberg writes an RDF graph into the configured Iceberg triples table.
func WriteGraphToIceberg(ctx context.Context, graph *rdflibgo.Graph, cfg *LoadCmd, customMetadata map[string]string) error {
	if graph == nil {
		return fmt.Errorf("load graph: missing graph")
	}
	if cfg == nil {
		return fmt.Errorf("load graph: missing arguments")
	}

	arrowSchema, tableSchema, err := GetSchemas(cfg.DataTypeCols)
	if err != nil {
		return err
	}

	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	if err != nil {
		return fmt.Errorf("failed to create catalog: %w", err)
	}

	tbl, err := NewIcebergTableFromCfg(ctx, tableSchema, cat, cfg)
	if err != nil {
		return fmt.Errorf("failed to create Iceberg table: %w", err)
	}

	if err := applyWriteProperties(ctx, tbl, cfg); err != nil {
		return err
	}

	err = processGraph(ctx, graph, cat, tbl.Identifier(), arrowSchema, cfg.BatchSize, cfg.DataTypeCols)
	if err != nil {
		return err
	}

	tx := tbl.NewTransaction()
	if err = tx.SetProperties(customMetadata); err != nil {
		return err
	}

	_, err = tx.Commit(context.Background())
	return err
}

// processGraph writes an RDF graph to Iceberg data files, then commits them in one snapshot.
func processGraph(
	ctx context.Context,
	graph *rdflibgo.Graph,
	cat catalog.Catalog,
	tableIdent table.Identifier,
	arrowSchema *arrow.Schema,
	batchSize int,
	dataTypeCols bool,
) error {
	tbl, err := cat.LoadTable(ctx, tableIdent)
	if err != nil {
		return fmt.Errorf("load table: %w", err)
	}

	if dataTypeCols {
		return appendGraph(ctx, tbl, graph, arrowSchema, batchSize)
	}

	dataFiles, rows, err := writeGraph(ctx, tbl, graph, arrowSchema, batchSize)
	if err != nil {
		return err
	}
	return commitDataFiles(ctx, tbl, dataFiles, rows)
}

// writeGraph writes all triples in graph to Iceberg data files without parallelism.
func writeGraph(
	ctx context.Context,
	tbl *table.Table,
	graph *rdflibgo.Graph,
	arrowSchema *arrow.Schema,
	batchSize int,
) ([]iceberg.DataFile, int64, error) {
	rdr := newGraphRecordReader(graph, arrowSchema, batchSize)
	defer rdr.Release()

	records := retainedRecordIterator(rdr)
	var dataFiles []iceberg.DataFile
	for df, err := range table.WriteRecords(ctx, tbl, arrowSchema, records) {
		if err != nil {
			return nil, 0, fmt.Errorf("write graph: %w", err)
		}
		dataFiles = append(dataFiles, df)
	}
	if err := rdr.Err(); err != nil {
		return nil, 0, fmt.Errorf("read graph: %w", err)
	}

	slog.Info("Successfully wrote to iceberg table with " + fmt.Sprint(len(dataFiles)) + " data files and " + fmt.Sprint(rdr.RowsRead()) + " triples")
	return dataFiles, rdr.RowsRead(), nil
}

func appendGraph(
	ctx context.Context,
	tbl *table.Table,
	graph *rdflibgo.Graph,
	arrowSchema *arrow.Schema,
	batchSize int,
) error {
	rdr := newGraphRecordReader(graph, arrowSchema, batchSize)
	defer rdr.Release()

	txn := tbl.NewTransaction()
	if err := txn.Append(ctx, rdr, nil); err != nil {
		return fmt.Errorf("append graph: %w", err)
	}
	if err := rdr.Err(); err != nil {
		return fmt.Errorf("read graph: %w", err)
	}
	if rdr.RowsRead() == 0 {
		return fmt.Errorf("no triples found")
	}
	if _, err := txn.Commit(ctx); err != nil {
		return fmt.Errorf("commit data files: %w", err)
	}

	slog.Info("Successfully appended to iceberg table with " + fmt.Sprint(rdr.RowsRead()) + " triples")
	return nil
}

// commitDataFiles commits produced Iceberg data files in one table snapshot.
func commitDataFiles(ctx context.Context, tbl *table.Table, dataFiles []iceberg.DataFile, rows int64) error {
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
	return nil
}

type recordBatchReader interface {
	Next() bool
	RecordBatch() arrow.RecordBatch
	Err() error
}

// retainedRecordIterator adapts SAL record readers to Iceberg's retained batch iterator.
func retainedRecordIterator(rdr recordBatchReader) func(func(arrow.RecordBatch, error) bool) {
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
