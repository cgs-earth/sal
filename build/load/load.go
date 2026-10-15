package load

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	rdflibgo "github.com/tggo/goRDFlib"
	"go.opentelemetry.io/otel/attribute"
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

// WriteGraphToIceberg writes an RDF graph into the configured Iceberg triples table.
func WriteGraphToIceberg(ctx context.Context, graph *rdflibgo.Graph, cfg *LoadConfig, customMetadata map[string]string) error {
	return writeGraphToIceberg(ctx, graph, cfg, customMetadata, nil)
}

// writeGraphToIceberg is WriteGraphToIceberg for a graph that may have been
// staged first, whose data files the commit then reuses.
func writeGraphToIceberg(ctx context.Context, graph *rdflibgo.Graph, cfg *LoadConfig, customMetadata map[string]string, staged *StagedGraph) (err error) {
	if graph == nil {
		return fmt.Errorf("load graph: missing graph")
	}
	if cfg == nil {
		return fmt.Errorf("load graph: missing arguments")
	}
	ctx, span := telemetry.Start(ctx, "iceberg.write", attribute.String("sal.iceberg.warehouse", cfg.Warehouse), attribute.String("sal.iceberg.namespace", cfg.Namespace))
	defer func() { telemetry.End(span, err) }()

	graph = StabilizeBlankNodes(graph)

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

	err = processGraph(ctx, graph, cat, tbl.Identifier(), arrowSchema, cfg.BatchSize, staged)
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

	_, err = tx.Commit(ctx)
	if err != nil {
		return err
	}

	return pkg.SetTagOfLatestSnapshot(tbl, cat)
}

// processGraph writes an RDF graph to Iceberg data files, then commits them in
// one snapshot. The data files a staged graph already wrote are committed as
// they are, and only the triples they do not hold are written.
func processGraph(
	ctx context.Context,
	graph *rdflibgo.Graph,
	cat catalog.Catalog,
	tableIdent table.Identifier,
	arrowSchema *arrow.Schema,
	batchSize int,
	staged *StagedGraph,
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

	toWrite := diff.toAdd
	dataFiles := staged.reusableFiles(diff.toAdd)
	if len(dataFiles) > 0 {
		toWrite = map[tripleDigest]struct{}{}
		for digest := range diff.toAdd {
			if _, ok := staged.hashes[digest]; !ok {
				toWrite[digest] = struct{}{}
			}
		}
		slog.Info("Reusing the data files the build was staged in", "data_files", len(dataFiles), "triples", len(staged.hashes))
	}
	if len(toWrite) > 0 {
		written, _, err := writeGraph(ctx, tbl, graph, arrowSchema, batchSize, toWrite)
		if err != nil {
			return err
		}
		dataFiles = append(slices.Clone(dataFiles), written...)
	}
	_, err = commitGraphDelta(ctx, tbl, table.MainBranch, dataFiles, int64(len(diff.toAdd)), diff.toDrop)
	return err
}

// writeGraph writes all triples in graph to Iceberg data files without parallelism.
func writeGraph(
	ctx context.Context,
	tbl *table.Table,
	graph *rdflibgo.Graph,
	arrowSchema *arrow.Schema,
	batchSize int,
	hashes map[tripleDigest]struct{},
) (_ []iceberg.DataFile, _ int64, err error) {
	ctx, span := telemetry.Start(ctx, "iceberg.write_data_files")
	defer func() { telemetry.End(span, err) }()
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

	span.SetAttributes(attribute.Int("sal.iceberg.data_files", len(dataFiles)), attribute.Int64("sal.triples.written", rdr.RowsRead()))
	slog.Info("Successfully wrote to iceberg table with " + fmt.Sprint(len(dataFiles)) + " data files and " + fmt.Sprint(rdr.RowsRead()) + " triples")
	return dataFiles, rdr.RowsRead(), nil
}

func appendGraph(
	ctx context.Context,
	tbl *table.Table,
	graph *rdflibgo.Graph,
	arrowSchema *arrow.Schema,
	batchSize int,
	hashes map[tripleDigest]struct{},
) error {
	dataFiles, rows, err := writeGraph(ctx, tbl, graph, arrowSchema, batchSize, hashes)
	if err != nil {
		return err
	}
	_, err = commitGraphDelta(ctx, tbl, table.MainBranch, dataFiles, rows, nil)
	return err
}

type graphTableDiff struct {
	toAdd     map[tripleDigest]struct{}
	toDrop    []existingTriple
	unchanged int
}

// existingTriple is a row of the table as the diff needs it: its hash and
// predicate, which an equality delete names it by, and whether the graph being
// written still holds it.
type existingTriple struct {
	hash      string
	predicate string
	seen      bool
}

// diffGraphAgainstTable compares new graph triple hashes against hashes already in Iceberg.
// The graph is walked once, marking every row of the table it still holds, so
// that the rows to drop are the ones left unmarked; no second set of the
// graph's hashes is built, since a graph of millions of triples makes that set
// as large as the table's own.
func diffGraphAgainstTable(ctx context.Context, tbl *table.Table, graph *rdflibgo.Graph) (_ *graphTableDiff, err error) {
	ctx, span := telemetry.Start(ctx, "iceberg.diff")
	defer func() { telemetry.End(span, err) }()
	slog.Info("Reading the triple hashes the Iceberg table already holds")
	existing, err := readExistingTriples(ctx, tbl)
	if err != nil {
		return nil, err
	}

	slog.Info("Comparing the graph against the Iceberg table", "graph_triples", pkg.GraphLen(graph), "table_rows", len(existing))
	diff := &graphTableDiff{toAdd: map[tripleDigest]struct{}{}}
	pkg.Triples(graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		digest := tripleDigestForTriple(triple)
		if row, ok := existing[digest]; ok {
			if !row.seen {
				diff.unchanged++
				row.seen = true
				existing[digest] = row
			}
			return true
		}
		diff.toAdd[digest] = struct{}{}
		return true
	})

	for digest, row := range existing {
		if !row.seen {
			row.hash = hex.EncodeToString(digest[:])
			diff.toDrop = append(diff.toDrop, row)
		}
	}
	span.SetAttributes(
		attribute.Int("sal.triples.existing", len(existing)),
		attribute.Int("sal.triples.added", len(diff.toAdd)),
		attribute.Int("sal.triples.removed", len(diff.toDrop)),
		attribute.Int("sal.triples.unchanged", diff.unchanged),
	)

	return diff, nil
}

// readExistingTriples scans the minimal columns needed to diff and delete rows.
// A hash is kept as its 32 bytes rather than the 64 hex characters the column
// holds, and the predicates, of which a table has few, are shared between the
// rows that carry them, so a table of millions of rows is held as compactly
// as it can be while the graph is diffed against it.
func readExistingTriples(ctx context.Context, tbl *table.Table) (map[tripleDigest]existingTriple, error) {
	triples := map[tripleDigest]existingTriple{}
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
	predicates := map[string]string{}
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
			var digest tripleDigest
			if n, err := hex.Decode(digest[:], []byte(hashColumn.Value(i))); err != nil || n != len(digest) {
				rec.Release()
				return nil, fmt.Errorf("read existing triple hashes: %q is not a SHA-256 triple_hash", hashColumn.Value(i))
			}
			// the column's strings are views into the record's buffers, which
			// a kept string would hold in memory whole
			predicate, ok := predicates[predicateColumn.Value(i)]
			if !ok {
				predicate = strings.Clone(predicateColumn.Value(i))
				predicates[predicate] = predicate
			}
			triples[digest] = existingTriple{predicate: predicate}
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

// readExistingTripleHashes scans triple hashes from the current Iceberg table,
// as the hex strings the triple_hash column holds.
func readExistingTripleHashes(ctx context.Context, tbl *table.Table) (map[string]struct{}, error) {
	triples, err := readExistingTriples(ctx, tbl)
	if err != nil {
		return nil, err
	}
	hashes := map[string]struct{}{}
	for digest := range triples {
		hashes[hex.EncodeToString(digest[:])] = struct{}{}
	}
	return hashes, nil
}

// commitGraphDelta commits appended data files and equality deletes in one
// Iceberg snapshot on a branch of the table, and returns the table as committed.
func commitGraphDelta(ctx context.Context, tbl *table.Table, branch string, dataFiles []iceberg.DataFile, rows int64, toDrop []existingTriple) (_ *table.Table, err error) {
	if len(dataFiles) == 0 && len(toDrop) == 0 {
		return nil, fmt.Errorf("no triples found")
	}
	ctx, span := telemetry.Start(ctx, "iceberg.commit", attribute.Int("sal.iceberg.data_files", len(dataFiles)), attribute.Int64("sal.triples.added", rows), attribute.Int("sal.triples.removed", len(toDrop)))
	defer func() { telemetry.End(span, err) }()

	txn := tbl.NewTransactionOnBranch(branch)
	var deleteFiles []iceberg.DataFile
	if len(toDrop) > 0 {
		deleteFiles, err = writeTripleHashDeletes(ctx, txn, tbl.Schema(), toDrop)
		if err != nil {
			return nil, err
		}
	}

	rowDelta := txn.NewRowDelta(nil)
	rowDelta.AddRows(dataFiles...)
	rowDelta.AddDeletes(deleteFiles...)
	if err := rowDelta.Commit(ctx); err != nil {
		return nil, fmt.Errorf("stage row delta: %w", err)
	}

	committed, err := txn.Commit(ctx)
	if err != nil {
		return nil, fmt.Errorf("commit row delta: %w", err)
	}
	slog.Info("Successfully committed iceberg row delta", "branch", branch, "added", rows, "removed", len(toDrop), "data_files", len(dataFiles), "delete_files", len(deleteFiles))
	return committed, nil
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
