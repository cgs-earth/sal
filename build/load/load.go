package load

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
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
	if err != nil {
		return err
	}

	return pkg.SetTagOfLatestSnapshot(tbl, cat)
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

	dataFiles, rows, err := writeGraph(ctx, tbl, graph, arrowSchema, batchSize, diff.toAdd)
	if err != nil {
		return err
	}
	return commitGraphDelta(ctx, tbl, dataFiles, rows, diff.toDrop)
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
	dataFiles, rows, err := writeGraph(ctx, tbl, graph, arrowSchema, batchSize, hashes)
	if err != nil {
		return err
	}
	return commitGraphDelta(ctx, tbl, dataFiles, rows, nil)
}

type graphTableDiff struct {
	toAdd     map[string]struct{}
	toDrop    []existingTriple
	unchanged int
}

type existingTriple struct {
	hash      string
	predicate string
}

// diffGraphAgainstTable compares new graph triple hashes against hashes already in Iceberg.
func diffGraphAgainstTable(ctx context.Context, tbl *table.Table, graph *rdflibgo.Graph) (*graphTableDiff, error) {
	existing, err := readExistingTriples(ctx, tbl)
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

	for hash, triple := range existing {
		if _, ok := newHashes[hash]; !ok {
			diff.toDrop = append(diff.toDrop, triple)
		}
	}

	return diff, nil
}

// readExistingTriples scans the minimal columns needed to diff and delete rows.
func readExistingTriples(ctx context.Context, tbl *table.Table) (map[string]existingTriple, error) {
	triples := map[string]existingTriple{}
	if tbl.CurrentSnapshot() == nil {
		return triples, nil
	}

	_, records, err := tbl.Scan(
		table.WithSelectedFields("triple_hash", "predicate"),
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
		hashIndex, predicateIndex, err := existingTripleColumnIndexes(rec.Schema())
		if err != nil {
			rec.Release()
			return nil, err
		}
		hashColumn := rec.Column(hashIndex).(*array.String)
		predicateColumn := rec.Column(predicateIndex).(*array.String)
		for i := 0; i < int(rec.NumRows()); i++ {
			if hashColumn.IsNull(i) || predicateColumn.IsNull(i) {
				continue
			}
			hash := hashColumn.Value(i)
			triples[hash] = existingTriple{hash: hash, predicate: predicateColumn.Value(i)}
		}
		rec.Release()
	}
	return triples, nil
}

func existingTripleColumnIndexes(schema *arrow.Schema) (int, int, error) {
	hashIndex := -1
	predicateIndex := -1
	for i, field := range schema.Fields() {
		switch field.Name {
		case "triple_hash":
			hashIndex = i
		case "predicate":
			predicateIndex = i
		}
	}
	if hashIndex < 0 || predicateIndex < 0 {
		return 0, 0, fmt.Errorf("scan existing triple hashes: expected predicate and triple_hash columns")
	}
	return hashIndex, predicateIndex, nil
}

// readExistingTripleHashes scans triple hashes from the current Iceberg table.
func readExistingTripleHashes(ctx context.Context, tbl *table.Table) (map[string]struct{}, error) {
	triples, err := readExistingTriples(ctx, tbl)
	if err != nil {
		return nil, err
	}
	hashes := map[string]struct{}{}
	for hash := range triples {
		hashes[hash] = struct{}{}
	}
	return hashes, nil
}

// commitGraphDelta commits appended data files and equality deletes in one Iceberg snapshot.
func commitGraphDelta(ctx context.Context, tbl *table.Table, dataFiles []iceberg.DataFile, rows int64, toDrop []existingTriple) error {
	if len(dataFiles) == 0 && len(toDrop) == 0 {
		return fmt.Errorf("no triples found")
	}

	txn := tbl.NewTransaction()
	var deleteFiles []iceberg.DataFile
	if len(toDrop) > 0 {
		var err error
		deleteFiles, err = writeTripleHashDeletes(ctx, txn, tbl.Schema(), toDrop)
		if err != nil {
			return err
		}
	}

	rowDelta := txn.NewRowDelta(nil)
	rowDelta.AddRows(dataFiles...)
	rowDelta.AddDeletes(deleteFiles...)
	if err := rowDelta.Commit(ctx); err != nil {
		return fmt.Errorf("stage row delta: %w", err)
	}

	if _, err := txn.Commit(ctx); err != nil {
		return fmt.Errorf("commit row delta: %w", err)
	}
	slog.Info("Successfully committed iceberg row delta", "added", rows, "removed", len(toDrop), "data_files", len(dataFiles), "delete_files", len(deleteFiles))
	return nil
}

func writeTripleHashDeletes(ctx context.Context, txn *table.Transaction, schema *iceberg.Schema, triples []existingTriple) ([]iceberg.DataFile, error) {
	tripleHashField, ok := schema.FindFieldByName("triple_hash")
	if !ok {
		return nil, fmt.Errorf("triple_hash field not found in table schema")
	}

	sort.Slice(triples, func(i, j int) bool {
		return triples[i].hash < triples[j].hash
	})

	records, release := equalityDeleteRecords(triples)
	defer release()

	deleteFiles, err := txn.WriteEqualityDeletes(ctx, []int{tripleHashField.ID}, records)
	if err != nil {
		return nil, fmt.Errorf("write equality deletes: %w", err)
	}
	return deleteFiles, nil
}

func equalityDeleteRecords(triples []existingTriple) (func(func(arrow.RecordBatch, error) bool), func()) {
	deleteSchema := arrow.NewSchema([]arrow.Field{
		{Name: "triple_hash", Type: arrow.BinaryTypes.String, Nullable: false},
		{Name: "predicate", Type: arrow.BinaryTypes.String, Nullable: false},
	}, nil)
	var records []arrow.RecordBatch
	for start := 0; start < len(triples); start += deleteHashChunkSize {
		end := min(start+deleteHashChunkSize, len(triples))
		builder := array.NewRecordBuilder(memory.NewGoAllocator(), deleteSchema)
		hashBuilder := builder.Field(0).(*array.StringBuilder)
		predicateBuilder := builder.Field(1).(*array.StringBuilder)
		for _, triple := range triples[start:end] {
			hashBuilder.Append(triple.hash)
			predicateBuilder.Append(triple.predicate)
		}
		records = append(records, builder.NewRecordBatch())
		builder.Release()
	}

	return func(yield func(arrow.RecordBatch, error) bool) {
			for _, record := range records {
				if !yield(record, nil) {
					return
				}
			}
		}, func() {
			for _, record := range records {
				record.Release()
			}
		}
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
