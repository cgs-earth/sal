package load

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	rdflibgo "github.com/tggo/goRDFlib"
)

const deleteHashChunkSize = 100

type LoadConfig struct {
	// BatchSize is the number of Arrow records to write per batch.
	BatchSize int

	// Workers is the number of input files to convert to Parquet in parallel.
	Workers int

	// ParquetCompression is the Parquet compression codec
	// (snappy, zstd, gzip, brotli, lz4, or uncompressed).
	ParquetCompression string

	// MetricsMode is the Iceberg metrics mode
	// (none, counts, truncate(N), or full).
	MetricsMode string

	// TargetFileSizeBytes is the target Iceberg data file size in bytes.
	TargetFileSizeBytes int64

	// InputDir is the path to a directory containing .nq.gz files.
	InputDir string

	// MaxFiles is the maximum number of input files to process.
	// A value of 0 processes all files.
	MaxFiles int

	// Warehouse is the Iceberg warehouse directory.
	Warehouse string

	// Namespace is the Iceberg namespace.
	Namespace string

	// DataTypeCols splits distinct RDF object data types into separate columns.
	DataTypeCols bool
}

// WriteGraphToIceberg writes an RDF graph into the configured Iceberg triples table.
func WriteGraphToIceberg(ctx context.Context, graph *rdflibgo.Graph, cfg *LoadConfig, customMetadata map[string]string) error {
	if graph == nil {
		return fmt.Errorf("load graph: missing graph")
	}
	if cfg == nil {
		return fmt.Errorf("load graph: missing arguments")
	}

	graph = stabilizeBlankNodes(graph)

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

	tbl, err = cat.LoadTable(ctx, tbl.Identifier())
	if err != nil {
		return fmt.Errorf("reload table before setting metadata: %w", err)
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

	diff, err := diffGraphAgainstTable(ctx, tbl, graph)
	if err != nil {
		return err
	}
	if len(diff.toAdd) == 0 && len(diff.toDrop) == 0 {
		slog.Warn("No changes from last Iceberg snapshot. No new snapshot will be created")
		return nil
	}
	slog.Info("Applying Iceberg triple diff", "added", len(diff.toAdd), "removed", len(diff.toDrop), "unchanged", diff.unchanged)

	tbl, err = deleteRemovedTriples(ctx, tbl, diff.toDrop)
	if err != nil {
		return err
	}
	if len(diff.toAdd) == 0 {
		return nil
	}

	if dataTypeCols {
		return appendGraph(ctx, tbl, graph, arrowSchema, batchSize, diff.toAdd)
	}

	dataFiles, rows, err := writeGraph(ctx, tbl, graph, arrowSchema, batchSize, diff.toAdd)
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
	hashes map[string]struct{},
) ([]iceberg.DataFile, int64, error) {
	rdr := newFilteredGraphRecordReader(graph, arrowSchema, batchSize, hashes)
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
	hashes map[string]struct{},
) error {
	rdr := newFilteredGraphRecordReader(graph, arrowSchema, batchSize, hashes)
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

type graphTableDiff struct {
	toAdd     map[string]struct{}
	toDrop    []string
	unchanged int
}

// diffGraphAgainstTable compares new graph triple hashes against hashes already in Iceberg.
func diffGraphAgainstTable(ctx context.Context, tbl *table.Table, graph *rdflibgo.Graph) (*graphTableDiff, error) {
	existing, err := readExistingTripleHashes(ctx, tbl)
	if err != nil {
		return nil, err
	}

	diff := &graphTableDiff{toAdd: map[string]struct{}{}}
	newHashes := map[string]struct{}{}
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		hash := tripleHashForTriple(triple)
		newHashes[hash] = struct{}{}
		if _, ok := existing[hash]; ok {
			diff.unchanged++
			return true
		}
		diff.toAdd[hash] = struct{}{}
		return true
	})

	for hash := range existing {
		if _, ok := newHashes[hash]; !ok {
			diff.toDrop = append(diff.toDrop, hash)
		}
	}

	return diff, nil
}

// readExistingTripleHashes scans only the triple_hash column from the current Iceberg table.
func readExistingTripleHashes(ctx context.Context, tbl *table.Table) (map[string]struct{}, error) {
	hashes := map[string]struct{}{}
	if tbl.CurrentSnapshot() == nil {
		return hashes, nil
	}

	_, records, err := tbl.Scan(
		table.WithSelectedFields("triple_hash"),
		table.WithCaseSensitive(true),
	).ToArrowRecords(ctx)
	if err != nil {
		return nil, fmt.Errorf("scan existing triple hashes: %w", err)
	}
	for rec, err := range records {
		if err != nil {
			return nil, fmt.Errorf("read existing triple hashes: %w", err)
		}
		if rec == nil {
			continue
		}
		hashColumn := rec.Column(0).(*array.String)
		for i := 0; i < int(rec.NumRows()); i++ {
			if hashColumn.IsNull(i) {
				continue
			}
			hashes[hashColumn.Value(i)] = struct{}{}
		}
		rec.Release()
	}
	return hashes, nil
}

// deleteRemovedTriples removes table rows whose triple_hash is absent from the new graph.
func deleteRemovedTriples(ctx context.Context, tbl *table.Table, hashes []string) (*table.Table, error) {
	for start := 0; start < len(hashes); start += deleteHashChunkSize {
		end := min(start+deleteHashChunkSize, len(hashes))
		predicate := tripleHashDeletePredicate(hashes[start:end])
		next, err := tbl.Delete(ctx, predicate, nil)
		if err != nil {
			return nil, fmt.Errorf("delete removed triples: %w", err)
		}
		tbl = next
	}
	return tbl, nil
}

func tripleHashDeletePredicate(hashes []string) iceberg.BooleanExpression {
	if len(hashes) == 0 {
		return iceberg.AlwaysFalse{}
	}
	var expr iceberg.BooleanExpression = iceberg.EqualTo(iceberg.Reference("triple_hash"), hashes[0])
	for _, hash := range hashes[1:] {
		expr = iceberg.NewOr(expr, iceberg.EqualTo(iceberg.Reference("triple_hash"), hash))
	}
	return expr
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
