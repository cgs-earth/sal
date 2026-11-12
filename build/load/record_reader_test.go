package load

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func TestGetSchemasUseNativeIcebergGeometry(t *testing.T) {
	arrowSchema, icebergSchema, err := GetSchemas()
	require.NoError(t, err)

	convertedType, err := table.ArrowTypeToIceberg(arrowSchema.Field(4).Type, false)
	require.NoError(t, err)
	require.Equal(t, icebergSchema.Field(4).Type.String(), convertedType.String())
	require.Equal(t, "geometry", icebergSchema.Field(4).Type.String())
}

func TestGetSchemasSplitsObjectsByDatatype(t *testing.T) {
	arrowSchema, icebergSchema, err := GetSchemas()
	require.NoError(t, err)

	names := []string{"subject", "predicate", "object_string", "object_iri", "object_geometry", "object_byte", "object_integer", "object_float", "object_time", "object_type", "object_language", "triple_hash"}
	require.Equal(t, len(names), arrowSchema.NumFields())
	require.Equal(t, len(names), len(icebergSchema.Fields()))
	for i, name := range names {
		require.Equal(t, name, arrowSchema.Field(i).Name)
		require.Equal(t, name, icebergSchema.Field(i).Name)
	}
	require.Equal(t, []int{12}, icebergSchema.IdentifierFieldIDs)
}

// A table from the release that marked every pinned vocabulary's rows in a
// column of their own holds rows this sal never writes, so it is refused
// rather than diffed against a graph that can never account for them.
func TestNewIcebergTableRefusesATableWithTheVocabularyColumn(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
	cat, err := hadoop.NewCatalog(context.Background(), "local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	require.NoError(t, cat.CreateNamespace(ctx, catalog.ToIdentifier("default"), nil))
	older := iceberg.NewSchema(1,
		iceberg.NestedField{ID: 1, Name: "subject", Type: iceberg.PrimitiveTypes.String, Required: true},
		iceberg.NestedField{ID: 2, Name: "object_language", Type: iceberg.PrimitiveTypes.String, Required: false},
		iceberg.NestedField{ID: 3, Name: "vocabulary", Type: iceberg.PrimitiveTypes.String, Required: false},
	)
	_, err = cat.CreateTable(ctx, catalog.ToIdentifier("default", "triples"), older)
	require.NoError(t, err)

	_, icebergSchema, err := GetSchemas()
	require.NoError(t, err)
	_, err = NewIcebergTableFromCfg(ctx, icebergSchema, cat, cfg)

	require.ErrorContains(t, err, "wrote a vocabulary column")
	require.ErrorContains(t, err, "sal clean --wipe")
}

// triple_hash is the row's SHA-256 as 32 raw bytes, half the size of the hex
// it was written as before.
func TestGetSchemasStoresTheTripleHashAsFixedBytes(t *testing.T) {
	arrowSchema, icebergSchema, err := GetSchemas()
	require.NoError(t, err)

	require.Equal(t, "fixed[32]", icebergSchema.Field(11).Type.String())
	convertedType, err := table.ArrowTypeToIceberg(arrowSchema.Field(11).Type, false)
	require.NoError(t, err)
	require.Equal(t, "fixed[32]", convertedType.String())
}

// A table written while triple_hash was a hex string cannot hold the rows or
// the equality deletes this sal writes, so it is refused with the way out.
func TestNewIcebergTableRefusesATableWithAHexTripleHash(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
	cat, err := hadoop.NewCatalog(context.Background(), "local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	require.NoError(t, cat.CreateNamespace(ctx, catalog.ToIdentifier("default"), nil))
	older := iceberg.NewSchema(1,
		iceberg.NestedField{ID: 1, Name: "subject", Type: iceberg.PrimitiveTypes.String, Required: true},
		iceberg.NestedField{ID: 2, Name: "object_language", Type: iceberg.PrimitiveTypes.String, Required: false},
		iceberg.NestedField{ID: 3, Name: "triple_hash", Type: iceberg.PrimitiveTypes.String, Required: true},
	)
	_, err = cat.CreateTable(ctx, catalog.ToIdentifier("default", "triples"), older)
	require.NoError(t, err)

	_, icebergSchema, err := GetSchemas()
	require.NoError(t, err)
	_, err = NewIcebergTableFromCfg(ctx, icebergSchema, cat, cfg)

	require.ErrorContains(t, err, "triple_hash as a hex string")
	require.ErrorContains(t, err, "sal clean --wipe")
}

// requireTripleHashWithoutDictionary checks that the triple_hash column of a
// Parquet file is 32-byte fixed length values with no dictionary page.
func requireTripleHashWithoutDictionary(t *testing.T, path string) {
	t.Helper()
	reader, err := file.OpenParquetFile(strings.TrimPrefix(path, "file://"), false)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Close()) }()
	column := reader.MetaData().Schema.ColumnIndexByName("triple_hash")
	require.GreaterOrEqual(t, column, 0, "%s has no triple_hash column", path)
	require.Equal(t, parquet.Types.FixedLenByteArray, reader.MetaData().Schema.Column(column).PhysicalType())
	require.Equal(t, 32, reader.MetaData().Schema.Column(column).TypeLength())
	for i := range reader.NumRowGroups() {
		chunk, err := reader.MetaData().RowGroup(i).ColumnChunk(column)
		require.NoError(t, err)
		require.False(t, chunk.HasDictionaryPage(), "%s keeps a dictionary of triple_hash", path)
	}
}

// Both the data files and the equality delete files store triple_hash as
// fixed length bytes without a dictionary, since every value is unique.
func TestTripleHashIsWrittenWithoutADictionary(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	first := rdflibgo.NewGraph()
	for i := range 5000 {
		pkg.AddTriple(first, rdflibgo.NewURIRefUnsafe(fmt.Sprintf("http://example.com/s%d", i)), predicate, rdflibgo.NewLiteral("value"))
	}
	require.NoError(t, WriteGraphToIceberg(ctx, first, cfg, nil, ""))
	second := rdflibgo.NewGraph()
	pkg.AddTriple(second, rdflibgo.NewURIRefUnsafe("http://example.com/s0"), predicate, rdflibgo.NewLiteral("value"))
	require.NoError(t, WriteGraphToIceberg(ctx, second, cfg, nil, ""))

	tbl := stageTestTable(t, cfg)
	require.Equal(t, "false", tbl.Properties()["write.parquet.dict-encoding-enabled.column.triple_hash"])
	tasks, err := tbl.Scan().PlanFiles(ctx)
	require.NoError(t, err)
	var data, deletes int
	for _, task := range tasks {
		requireTripleHashWithoutDictionary(t, task.File.FilePath())
		data++
		for _, deleteFile := range task.EqualityDeleteFiles {
			requireTripleHashWithoutDictionary(t, deleteFile.FilePath())
			deletes++
		}
	}
	require.Positive(t, data)
	require.Positive(t, deletes)
}

// Data files carry a bloom filter of bloomFilterMaxBytes on subject and
// object_iri, the columns a SPARQL pattern binds to a constant IRI, and no
// dictionary on object_geometry, even when every geometry is the same one.
func TestDataFilesCarryIRIBloomFiltersAndNoGeometryDictionary(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
	link := rdflibgo.NewURIRefUnsafe("http://example.com/link")
	asWKT := rdflibgo.NewURIRefUnsafe("http://example.com/asWKT")
	graph := rdflibgo.NewGraph()
	for i := range 500 {
		subject := rdflibgo.NewURIRefUnsafe(fmt.Sprintf("http://example.com/s%d", i))
		pkg.AddTriple(graph, subject, link, rdflibgo.NewURIRefUnsafe(fmt.Sprintf("http://example.com/o%d", i)))
		pkg.AddTriple(graph, subject, asWKT, rdflibgo.NewLiteral("POINT (1 2)", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(geoSPARQLWKTLiteral))))
	}
	require.NoError(t, WriteGraphToIceberg(ctx, graph, cfg, nil, ""))

	tbl := stageTestTable(t, cfg)
	tasks, err := tbl.Scan().PlanFiles(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	for _, task := range tasks {
		reader, err := file.OpenParquetFile(strings.TrimPrefix(task.File.FilePath(), "file://"), false)
		require.NoError(t, err)
		schema := reader.MetaData().Schema
		for i := range reader.NumRowGroups() {
			rowGroup := reader.MetaData().RowGroup(i)
			for _, name := range []string{"subject", "object_iri"} {
				chunk, err := rowGroup.ColumnChunk(schema.ColumnIndexByName(name))
				require.NoError(t, err)
				require.Positive(t, chunk.BloomFilterOffset(), "%s has no bloom filter in %s", name, task.File.FilePath())
				require.LessOrEqual(t, int(chunk.BloomFilterLength()), bloomFilterMaxBytes+64, "%s bloom filter is over its cap", name)
			}
			geometry, err := rowGroup.ColumnChunk(schema.ColumnIndexByName("object_geometry"))
			require.NoError(t, err)
			require.False(t, geometry.HasDictionaryPage(), "object_geometry keeps a dictionary in %s", task.File.FilePath())
		}
		require.NoError(t, reader.Close())
	}
}

// The numeric and time columns are written without a dictionary even when
// every row holds the same value, which is when iceberg-go's default would
// keep one.
func TestNumericAndTimeColumnsAreWrittenWithoutADictionary(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
	xsd := func(name string) rdflibgo.LiteralOption {
		return rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe("http://www.w3.org/2001/XMLSchema#" + name))
	}
	graph := rdflibgo.NewGraph()
	for i := range 500 {
		subject := rdflibgo.NewURIRefUnsafe(fmt.Sprintf("http://example.com/s%d", i))
		pkg.AddTriple(graph, subject, rdflibgo.NewURIRefUnsafe("http://example.com/value/byte"), rdflibgo.NewLiteral("3", xsd("byte")))
		pkg.AddTriple(graph, subject, rdflibgo.NewURIRefUnsafe("http://example.com/value/integer"), rdflibgo.NewLiteral("7", xsd("integer")))
		pkg.AddTriple(graph, subject, rdflibgo.NewURIRefUnsafe("http://example.com/value/float"), rdflibgo.NewLiteral("1.5", xsd("double")))
		pkg.AddTriple(graph, subject, rdflibgo.NewURIRefUnsafe("http://example.com/value/time"), rdflibgo.NewLiteral("2020-01-01T00:00:00Z", xsd("dateTime")))
	}
	require.NoError(t, WriteGraphToIceberg(ctx, graph, cfg, nil, ""))

	tbl := stageTestTable(t, cfg)
	tasks, err := tbl.Scan().PlanFiles(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tasks)
	columns := []string{"object_byte", "object_integer", "object_float", "object_time"}
	values := map[string]int64{}
	for _, task := range tasks {
		reader, err := file.OpenParquetFile(strings.TrimPrefix(task.File.FilePath(), "file://"), false)
		require.NoError(t, err)
		for i := range reader.NumRowGroups() {
			rowGroup := reader.MetaData().RowGroup(i)
			for _, name := range columns {
				chunk, err := rowGroup.ColumnChunk(reader.MetaData().Schema.ColumnIndexByName(name))
				require.NoError(t, err)
				require.False(t, chunk.HasDictionaryPage(), "%s keeps a dictionary in %s", name, task.File.FilePath())
				stats, err := chunk.Statistics()
				require.NoError(t, err)
				values[name] += stats.NumValues()
			}
		}
		require.NoError(t, reader.Close())
	}
	// every column held its 500 repeated values, so a dictionary would have paid
	for _, name := range columns {
		require.Equal(t, int64(500), values[name], "%s values", name)
	}
}

func TestWriteGraphToIcebergIngestsSimpleWKTGeometry(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:          10,
		ParquetCompression: "snappy",
		Warehouse:          t.TempDir(),
		Namespace:          "default",
	}

	graph := rdflibgo.NewGraph()
	pkg.AddTriple(graph,
		rdflibgo.NewURIRefUnsafe("http://example.com/s"),
		rdflibgo.NewURIRefUnsafe("http://example.com/hasGeometry"),
		rdflibgo.NewLiteral("POINT (1 2)", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(geoSPARQLWKTLiteral))),
	)

	require.NoError(t, WriteGraphToIceberg(ctx, graph, cfg, nil, ""))
	loaded := stageTestTable(t, cfg)
	require.NotNil(t, loaded.CurrentSnapshot())
	require.NotNil(t, loaded.CurrentSnapshot().Summary)
	require.Equal(t, 3, loaded.Metadata().FormatVersion())
	require.Equal(t, "geometry", loaded.Schema().Field(4).Type.String())
	require.Equal(t, "1", loaded.CurrentSnapshot().Summary.Properties["added-records"])

	// the geometry round trips through the spool and the diff as WKB
	_, records, err := loaded.Scan(table.WithSelectedFields("object_geometry")).ToArrowRecords(ctx)
	require.NoError(t, err)
	var geometries int
	for rec, err := range records {
		require.NoError(t, err)
		for i := 0; i < int(rec.NumRows()); i++ {
			require.True(t, rec.Column(0).IsValid(i))
			geometries++
		}
		rec.Release()
	}
	require.Equal(t, 1, geometries)
}

func TestWriteGraphToIcebergDiffAddsAndRemovesByTripleHash(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:          10,
		ParquetCompression: "snappy",
		Warehouse:          t.TempDir(),
		Namespace:          "default",
	}

	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	first := rdflibgo.NewGraph()
	pkg.AddTriple(first, rdflibgo.NewURIRefUnsafe("http://example.com/keep"), predicate, rdflibgo.NewLiteral("same"))
	pkg.AddTriple(first, rdflibgo.NewURIRefUnsafe("http://example.com/drop"), predicate, rdflibgo.NewLiteral("old"))
	require.NoError(t, WriteGraphToIceberg(ctx, first, cfg, nil, ""))
	firstSnapshotID := stageTestTable(t, cfg).CurrentSnapshot().SnapshotID

	second := rdflibgo.NewGraph()
	pkg.AddTriple(second, rdflibgo.NewURIRefUnsafe("http://example.com/keep"), predicate, rdflibgo.NewLiteral("same"))
	pkg.AddTriple(second, rdflibgo.NewURIRefUnsafe("http://example.com/add"), predicate, rdflibgo.NewLiteral("new"))
	require.NoError(t, WriteGraphToIceberg(ctx, second, cfg, nil, ""))

	loaded := stageTestTable(t, cfg)
	require.NotNil(t, loaded.CurrentSnapshot().ParentSnapshotID)
	require.Equal(t, firstSnapshotID, *loaded.CurrentSnapshot().ParentSnapshotID)
	require.Equal(t, table.OpOverwrite, loaded.CurrentSnapshot().Summary.Operation)
	require.Equal(t, "1", loaded.CurrentSnapshot().Summary.Properties[pkg.SnapshotTriplesAddedProperty])
	require.Equal(t, "1", loaded.CurrentSnapshot().Summary.Properties[pkg.SnapshotTriplesRemovedProperty])
	hashes, err := readExistingTripleHashes(ctx, loaded)
	require.NoError(t, err)

	xsdString := rdflibgo.XSDString.Value()
	require.Contains(t, hashes, tripleHash("http://example.com/keep", "http://example.com/p", "same", xsdString, ""))
	require.Contains(t, hashes, tripleHash("http://example.com/add", "http://example.com/p", "new", xsdString, ""))
	require.NotContains(t, hashes, tripleHash("http://example.com/drop", "http://example.com/p", "old", xsdString, ""))
	require.Len(t, hashes, 2)
}

// TestWriteSpoolToIcebergKeepsOneRowPerTriple checks that a triple the spool
// holds more than once, which a module writing the same node twice does, is
// one row of the table.
func TestWriteSpoolToIcebergKeepsOneRowPerTriple(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	spool, err := NewSpool(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, spool.Remove()) })
	triple := rdflibgo.Triple{Subject: rdflibgo.NewURIRefUnsafe("http://example.com/s"), Predicate: stageTestPredicate, Object: rdflibgo.NewLiteral("o")}
	require.NoError(t, spool.Add(triple))
	_, err = spool.Mark()
	require.NoError(t, err)
	require.NoError(t, spool.Add(triple))

	require.NoError(t, WriteSpoolToIceberg(ctx, spool, cfg, nil, ""))

	hashes, err := readExistingTripleHashes(ctx, stageTestTable(t, cfg))
	require.NoError(t, err)
	require.Len(t, hashes, 1)
	require.Equal(t, "1", stageTestTable(t, cfg).CurrentSnapshot().Summary.Properties["added-records"])
}

// TestWriteSpoolToIcebergWritesFilesSortedByPredicate checks that rows reach
// the table grouped by predicate, which is its sort order and what lets the
// writer keep one partition open at a time, whatever order they were spooled
// in.
func TestWriteSpoolToIcebergWritesFilesSortedByPredicate(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	spool, err := NewSpool(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, spool.Remove()) })
	for i := range 50 {
		for _, predicate := range []string{"https://example.com/zebra", "https://example.com/apple", "https://other.example.com/mango"} {
			require.NoError(t, spool.Add(rdflibgo.Triple{
				Subject:   rdflibgo.NewURIRefUnsafe(fmt.Sprintf("http://example.com/s%d", i)),
				Predicate: rdflibgo.NewURIRefUnsafe(predicate),
				Object:    rdflibgo.NewLiteral(fmt.Sprint(i)),
			}))
		}
	}

	require.NoError(t, WriteSpoolToIceberg(ctx, spool, cfg, nil, ""))

	tbl := stageTestTable(t, cfg)
	require.Equal(t, "150", tbl.CurrentSnapshot().Summary.Properties["added-records"])
	// one data file per partition, since the clustered writer refuses a
	// partition it has already closed
	require.Equal(t, "2", tbl.CurrentSnapshot().Summary.Properties["added-data-files"])
	// the spool was written a partition at a time, and each partition's files
	// were dropped once its data files were written
	require.Empty(t, spool.Files(0, spool.Segment()))
}

// Rows reach the data files sorted by predicate and then by subject, which is
// the table's declared sort order and what gives each row group of a
// predicate its own range of subjects, whatever order they were spooled in.
// There are more rows than one batch of readBatchRows, since iceberg-go
// sorts each batch it writes by the declared order on its own, which leaves
// a row group of several batches holding several runs that each span every
// subject unless the diff sorted the rows first.
func TestWriteSpoolToIcebergSortsRowsBySubjectWithinAPredicate(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	spool, err := NewSpool(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, spool.Remove()) })
	// subjects spooled in an order neither alphabetical nor by hash
	const subjectCount = readBatchRows + 7000
	for n := range subjectCount {
		i := n * 7919 % subjectCount
		for _, predicate := range []string{"https://example.com/b", "https://example.com/a"} {
			require.NoError(t, spool.Add(rdflibgo.Triple{
				Subject:   rdflibgo.NewURIRefUnsafe(fmt.Sprintf("http://example.com/s%d", i)),
				Predicate: rdflibgo.NewURIRefUnsafe(predicate),
				Object:    rdflibgo.NewLiteral(fmt.Sprint(i)),
			}))
		}
	}

	require.NoError(t, WriteSpoolToIceberg(ctx, spool, cfg, nil, ""))

	tbl := stageTestTable(t, cfg)
	var keys [][]int
	for _, field := range tbl.SortOrder().Fields() {
		keys = append(keys, field.SourceIDs)
	}
	require.Equal(t, [][]int{{2}, {1}}, keys, "the table is sorted by predicate (field 2) then subject (field 1)")

	tasks, err := tbl.Scan().PlanFiles(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	reader, err := file.OpenParquetFile(strings.TrimPrefix(tasks[0].File.FilePath(), "file://"), false)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Close()) }()
	arrowReader, err := pqarrow.NewFileReader(reader, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	require.NoError(t, err)
	written, err := arrowReader.ReadTable(ctx)
	require.NoError(t, err)
	defer written.Release()
	var rows [][2]string
	subjects, predicates := written.Column(0).Data().Chunks(), written.Column(1).Data().Chunks()
	for c := range subjects {
		for i := range subjects[c].Len() {
			rows = append(rows, [2]string{predicates[c].(*array.String).Value(i), subjects[c].(*array.String).Value(i)})
		}
	}
	require.Len(t, rows, 2*subjectCount)
	require.True(t, slices.IsSortedFunc(rows, func(a, b [2]string) int {
		return cmp.Or(strings.Compare(a[0], b[0]), strings.Compare(a[1], b[1]))
	}), "rows are not sorted by predicate then subject")
}

// A spool partitions its rows the way the table does, by the first twenty
// characters of the predicate, counted as characters rather than bytes.
func TestPartitionKeyIsTheFirstTwentyCharactersOfThePredicate(t *testing.T) {
	require.Equal(t, "https://schema.org/n", partitionKey("https://schema.org/name"))
	require.Equal(t, "https://example.com/", partitionKey("https://example.com/zebra"))
	require.Equal(t, "short", partitionKey("short"))
	require.Equal(t, "https://例え.jp/述語/あいう", partitionKey("https://例え.jp/述語/あいうえお"))
}

func TestWriteGraphToIcebergDoesNotRewriteEquivalentBlankNodeGraph(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:          10,
		ParquetCompression: "snappy",
		Warehouse:          t.TempDir(),
		Namespace:          "default",
	}

	require.NoError(t, WriteGraphToIceberg(ctx, graphWithGeometryBlankNode("first"), cfg, map[string]string{"sal.hash": "first"}, ""))
	cat, err := hadoop.NewCatalog(context.Background(), "local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := cat.LoadTable(ctx, table.Identifier{"default", "triples"})
	require.NoError(t, err)
	firstHashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)

	require.NoError(t, WriteGraphToIceberg(ctx, graphWithGeometryBlankNode("second"), cfg, map[string]string{"sal.hash": "second"}, ""))
	tbl, err = cat.LoadTable(ctx, table.Identifier{"default", "triples"})
	require.NoError(t, err)
	secondHashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)

	require.Equal(t, firstHashes, secondHashes)
	require.Len(t, secondHashes, 3)
}

// TestWriteGraphToIcebergStoresBlankNodesWithNTriplesPrefix scans the written
// table and checks a blank node landed in the subject and object columns as
// "_:sal_..." rather than as a bare label, since the "_:" prefix is what lets
// readers tell a blank node apart from an IRI or a plain string.
func TestWriteGraphToIcebergStoresBlankNodesWithNTriplesPrefix(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:          10,
		ParquetCompression: "snappy",
		Warehouse:          t.TempDir(),
		Namespace:          "default",
	}

	require.NoError(t, WriteGraphToIceberg(ctx, graphWithGeometryBlankNode("b1"), cfg, map[string]string{"sal.hash": "h"}, ""))
	cat, err := hadoop.NewCatalog(context.Background(), "local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := cat.LoadTable(ctx, table.Identifier{"default", "triples"})
	require.NoError(t, err)

	var subjects, objects []string
	// a blank node object is stored in object_string with its _: prefix
	_, records, err := tbl.Scan(table.WithSelectedFields("subject", "object_string")).ToArrowRecords(ctx)
	require.NoError(t, err)
	for rec, err := range records {
		require.NoError(t, err)
		subjectColumn := rec.Column(0).(*array.String)
		objectColumn := rec.Column(1).(*array.String)
		for i := 0; i < int(rec.NumRows()); i++ {
			subjects = append(subjects, subjectColumn.Value(i))
			objects = append(objects, objectColumn.Value(i))
		}
		rec.Release()
	}

	require.Len(t, subjects, 3)
	blankID := ""
	for _, object := range objects {
		if strings.HasPrefix(object, "_:sal_") {
			blankID = object
		}
	}
	require.NotEmpty(t, blankID, "expected the hasGeometry object to be stored as a _:sal_ blank node, got objects %v", objects)
	require.Contains(t, subjects, blankID)
	require.NotContains(t, subjects, strings.TrimPrefix(blankID, "_:"))
}

// distinctRows keeps the first of every run of rows with one hash, including
// a run that carries over from the end of one batch to the start of the next.
func TestDistinctRowsDropsARepeatAcrossBatches(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{{Name: "triple_hash", Type: tripleHashArrowType}}, nil)
	// a hash is 32 bytes; each test hash is a letter padded with zeros
	digest := func(letter string) []byte { return append([]byte(letter), make([]byte, 31)...) }
	batch := func(hashes ...string) arrow.RecordBatch {
		builder := array.NewFixedSizeBinaryBuilder(memory.DefaultAllocator, tripleHashArrowType)
		for _, hash := range hashes {
			builder.Append(digest(hash))
		}
		column := builder.NewArray()
		return array.NewRecordBatch(schema, []arrow.Array{column}, int64(len(hashes)))
	}
	records := func(yield func(arrow.RecordBatch, error) bool) {
		for _, b := range []arrow.RecordBatch{batch("a", "b", "b"), batch("b", "c"), batch("d")} {
			if !yield(b, nil) {
				return
			}
		}
	}

	var rows int64
	var kept []string
	for b, err := range distinctRows(context.Background(), records, 0, &rows) {
		require.NoError(t, err)
		column := b.Column(0).(*array.FixedSizeBinary)
		for i := 0; i < column.Len(); i++ {
			kept = append(kept, string(column.Value(i)[:1]))
		}
		b.Release()
	}

	require.Equal(t, []string{"a", "b", "c", "d"}, kept)
	require.Equal(t, int64(4), rows)
}
