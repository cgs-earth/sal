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
}

// VocabularyGraph is the statements of one pinned vocabulary, attributed to
// the namespace the project pins it under.
type VocabularyGraph struct {
	Namespace string
	Graph     *rdflibgo.Graph
}

// tableRow is one row the triples table mirrors: the triple, its identity,
// and the vocabulary column, "" meaning NULL.
type tableRow struct {
	triple     rdflibgo.Triple
	hash       string
	vocabulary string
}

// tableRows flattens the asserted graph and the pinned vocabularies into the
// rows the table mirrors. Every asserted triple is a row with no vocabulary;
// then each vocabulary, in namespace order, contributes only the triples whose
// hash no earlier row has, so an asserted statement always wins over one a
// vocabulary states, and a statement two vocabularies share is attributed to
// the first namespace in sorted order.
func tableRows(graph *rdflibgo.Graph, vocabularies []VocabularyGraph) []tableRow {
	var rows []tableRow
	seen := map[string]struct{}{}
	add := func(g *rdflibgo.Graph, vocabulary string) {
		g.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
			hash := tripleHashForTriple(triple)
			if _, ok := seen[hash]; ok {
				return true
			}
			seen[hash] = struct{}{}
			rows = append(rows, tableRow{triple: triple, hash: hash, vocabulary: vocabulary})
			return true
		})
	}
	add(graph, "")
	sorted := append([]VocabularyGraph(nil), vocabularies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Namespace < sorted[j].Namespace })
	for _, vocabulary := range sorted {
		add(vocabulary.Graph, vocabulary.Namespace)
	}
	return rows
}

// WriteGraphToIceberg writes an RDF graph into the configured Iceberg triples
// table, along with the statements of the pinned vocabularies, which are
// marked with their namespace in the vocabulary column.
func WriteGraphToIceberg(ctx context.Context, graph *rdflibgo.Graph, vocabularies []VocabularyGraph, cfg *LoadConfig, customMetadata map[string]string) error {
	if graph == nil {
		return fmt.Errorf("load graph: missing graph")
	}
	if cfg == nil {
		return fmt.Errorf("load graph: missing arguments")
	}

	graph = stabilizeBlankNodes(graph)
	stabilized := make([]VocabularyGraph, 0, len(vocabularies))
	for _, vocabulary := range vocabularies {
		stabilized = append(stabilized, VocabularyGraph{Namespace: vocabulary.Namespace, Graph: stabilizeBlankNodes(vocabulary.Graph)})
	}

	arrowSchema, tableSchema, err := GetSchemas()
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

	err = processGraph(ctx, tableRows(graph, stabilized), cat, tbl.Identifier(), arrowSchema, cfg.BatchSize)
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

// processGraph writes the rows the table should mirror to Iceberg data files, then commits them in one snapshot.
func processGraph(
	ctx context.Context,
	rows []tableRow,
	cat catalog.Catalog,
	tableIdent table.Identifier,
	arrowSchema *arrow.Schema,
	batchSize int,
) error {
	tbl, err := cat.LoadTable(ctx, tableIdent)
	if err != nil {
		return fmt.Errorf("load table: %w", err)
	}

	diff, err := diffGraphAgainstTable(ctx, tbl, rows)
	if err != nil {
		return err
	}
	if len(diff.toAdd) == 0 && len(diff.toDrop) == 0 {
		slog.Warn("No changes from last Iceberg snapshot. No new snapshot will be created")
		return nil
	}
	slog.Info("Applying Iceberg triple diff", "added", len(diff.toAdd), "removed", len(diff.toDrop), "unchanged", diff.unchanged)

	dataFiles, written, err := writeGraph(ctx, tbl, rows, arrowSchema, batchSize, diff.toAdd)
	if err != nil {
		return err
	}
	return commitGraphDelta(ctx, tbl, dataFiles, written, diff.toDrop)
}

// writeGraph writes the rows whose hash is in hashes to Iceberg data files without parallelism.
func writeGraph(
	ctx context.Context,
	tbl *table.Table,
	rows []tableRow,
	arrowSchema *arrow.Schema,
	batchSize int,
	hashes map[string]struct{},
) ([]iceberg.DataFile, int64, error) {
	rdr := newFilteredGraphRecordReader(rows, arrowSchema, batchSize, hashes)
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
	dataFiles, rows, err := writeGraph(ctx, tbl, tableRows(graph, nil), arrowSchema, batchSize, hashes)
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
	hash       string
	predicate  string
	vocabulary string
}

// diffGraphAgainstTable compares the rows the table should mirror against the
// rows already in Iceberg. A row whose hash is in the table but whose
// vocabulary differs, because the project started or stopped asserting a
// statement a pinned vocabulary makes, is dropped and added again so the
// column mirrors the graph; the equality delete and the re-added row share a
// snapshot, and the delete applies only to data files sequenced before it.
func diffGraphAgainstTable(ctx context.Context, tbl *table.Table, rows []tableRow) (*graphTableDiff, error) {
	existing, err := readExistingTriples(ctx, tbl)
	if err != nil {
		return nil, err
	}

	diff := &graphTableDiff{toAdd: map[string]struct{}{}}
	newHashes := map[string]struct{}{}
	for _, row := range rows {
		newHashes[row.hash] = struct{}{}
		if current, ok := existing[row.hash]; ok {
			if current.vocabulary == row.vocabulary {
				diff.unchanged++
				continue
			}
			diff.toDrop = append(diff.toDrop, current)
		}
		diff.toAdd[row.hash] = struct{}{}
	}

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
		table.WithSelectedFields("triple_hash", "predicate", "vocabulary"),
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
		columns, err := existingTripleColumns(rec.Schema())
		if err != nil {
			rec.Release()
			return nil, err
		}
		hashColumn := rec.Column(columns["triple_hash"]).(*array.String)
		predicateColumn := rec.Column(columns["predicate"]).(*array.String)
		vocabularyColumn := rec.Column(columns["vocabulary"]).(*array.String)
		for i := 0; i < int(rec.NumRows()); i++ {
			if hashColumn.IsNull(i) || predicateColumn.IsNull(i) {
				continue
			}
			hash := hashColumn.Value(i)
			triple := existingTriple{hash: hash, predicate: predicateColumn.Value(i)}
			if !vocabularyColumn.IsNull(i) {
				triple.vocabulary = vocabularyColumn.Value(i)
			}
			triples[hash] = triple
		}
		rec.Release()
	}
	return triples, nil
}

// existingTripleColumns is the index of each column the diff reads, by name.
func existingTripleColumns(schema *arrow.Schema) (map[string]int, error) {
	columns := map[string]int{}
	for i, field := range schema.Fields() {
		columns[field.Name] = i
	}
	for _, name := range []string{"triple_hash", "predicate", "vocabulary"} {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("scan existing triple hashes: expected predicate, triple_hash and vocabulary columns")
		}
	}
	return columns, nil
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
