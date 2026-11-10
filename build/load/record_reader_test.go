package load

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
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
	schema := arrow.NewSchema([]arrow.Field{{Name: "triple_hash", Type: arrow.BinaryTypes.String}}, nil)
	batch := func(hashes ...string) arrow.RecordBatch {
		builder := array.NewStringBuilder(memory.DefaultAllocator)
		builder.AppendValues(hashes, nil)
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
		column := b.Column(0).(*array.String)
		for i := 0; i < column.Len(); i++ {
			kept = append(kept, column.Value(i))
		}
		b.Release()
	}

	require.Equal(t, []string{"a", "b", "c", "d"}, kept)
	require.Equal(t, int64(4), rows)
}
