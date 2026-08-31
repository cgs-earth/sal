package load

import (
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/cgs-earth/sal/pkg"
	geoarrow "github.com/geoarrow/geoarrow-go"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func TestGraphRecordReaderStreamsGraphTriples(t *testing.T) {
	graph := rdflibgo.NewGraph()
	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s1"),
		predicate,
		rdflibgo.NewLiteral("one"),
	)
	graph.Add(
		rdflibgo.NewBNode("subject"),
		predicate,
		rdflibgo.NewURIRefUnsafe("http://example.com/o2"),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s3"),
		predicate,
		rdflibgo.NewLiteral("three", rdflibgo.WithLang("en")),
	)

	arrowSchema, _, err := GetSchemas()
	require.NoError(t, err)
	rdr := newGraphRecordReader(graph, arrowSchema, 2)
	defer rdr.Release()

	var batches int
	var rows [][4]string
	for rdr.Next() {
		batches++
		rec := rdr.RecordBatch()
		subjects := rec.Column(0).(*array.String)
		predicates := rec.Column(1).(*array.String)
		objectStrings := rec.Column(2).(*array.String)
		objectIRIs := rec.Column(3).(*array.String)
		hashes := rec.Column(10).(*array.String)
		for i := 0; i < int(rec.NumRows()); i++ {
			// an object lands in exactly one typed column; these triples only
			// hold IRIs and strings
			object := objectStrings.Value(i)
			if objectIRIs.IsValid(i) {
				object = objectIRIs.Value(i)
			}
			rows = append(rows, [4]string{subjects.Value(i), predicates.Value(i), object, hashes.Value(i)})
		}
	}

	xsdString := rdflibgo.XSDString.Value()
	require.NoError(t, rdr.Err())
	require.Equal(t, 2, batches)
	require.Equal(t, int64(3), rdr.RowsRead())
	require.ElementsMatch(t, [][4]string{
		{"http://example.com/s1", "http://example.com/p", "one", tripleHash("http://example.com/s1", "http://example.com/p", "one", xsdString)},
		{"_:subject", "http://example.com/p", "http://example.com/o2", tripleHash("_:subject", "http://example.com/p", "http://example.com/o2", "")},
		{"http://example.com/s3", "http://example.com/p", "three", tripleHash("http://example.com/s3", "http://example.com/p", "three", rdflibgo.RDFLangString.Value())},
	}, rows)
}

func TestGraphRecordReaderSerializesObjectColumns(t *testing.T) {
	graph := rdflibgo.NewGraph()
	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s1"),
		predicate,
		rdflibgo.NewURIRefUnsafe("http://example.com/o"),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s2"),
		predicate,
		rdflibgo.NewLiteral("42.5", rdflibgo.WithDatatype(rdflibgo.XSDDouble)),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s3"),
		predicate,
		rdflibgo.NewLiteral("label"),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s4"),
		predicate,
		rdflibgo.NewLiteral("POINT (1 2)", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(geoSPARQLWKTLiteral))),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s5"),
		predicate,
		rdflibgo.NewLiteral("42", rdflibgo.WithDatatype(rdflibgo.XSDInteger)),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s6"),
		predicate,
		rdflibgo.NewLiteral("-8", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(pkg.XSDByte))),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s7"),
		predicate,
		rdflibgo.NewLiteral("2002-05-30T09:30:10-06:00", rdflibgo.WithDatatype(rdflibgo.XSDDateTime)),
	)

	arrowSchema, _, err := GetSchemas()
	require.NoError(t, err)
	rdr := newGraphRecordReader(graph, arrowSchema, 10)
	defer rdr.Release()

	require.True(t, rdr.Next())
	rec := rdr.RecordBatch()
	require.Equal(t, int64(7), rec.NumRows())

	subjects := rec.Column(0).(*array.String)
	objectString := rec.Column(2).(*array.String)
	objectIRI := rec.Column(3).(*array.String)
	objectGeometry := rec.Column(4).(*geoarrow.WKBArray)
	objectByte := rec.Column(5).(*array.Int32)
	objectInteger := rec.Column(6).(*array.Int64)
	objectFloat := rec.Column(7).(*array.Float64)
	objectTime := rec.Column(8).(*array.Timestamp)
	objectType := rec.Column(9).(*array.String)
	hashes := rec.Column(10).(*array.String)

	expectedWKB, err := wktObjectToWKB("POINT (1 2)")
	require.NoError(t, err)

	rowsBySubject := map[string]int{}
	for i := 0; i < int(rec.NumRows()); i++ {
		rowsBySubject[subjects.Value(i)] = i
	}
	for _, subject := range []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7"} {
		require.Contains(t, rowsBySubject, "http://example.com/"+subject)
	}

	// requireOnly checks that a row's value landed in exactly one object column.
	requireOnly := func(row int, populated any) {
		t.Helper()
		for _, column := range []interface {
			IsNull(int) bool
		}{objectString, objectIRI, objectGeometry, objectByte, objectInteger, objectFloat, objectTime} {
			if column == populated {
				require.False(t, column.IsNull(row))
			} else {
				require.True(t, column.IsNull(row))
			}
		}
	}

	iriRow := rowsBySubject["http://example.com/s1"]
	requireOnly(iriRow, objectIRI)
	require.Equal(t, "http://example.com/o", objectIRI.Value(iriRow))
	require.True(t, objectType.IsNull(iriRow))
	require.Equal(t, tripleHash("http://example.com/s1", "http://example.com/p", "http://example.com/o", ""), hashes.Value(iriRow))

	floatRow := rowsBySubject["http://example.com/s2"]
	requireOnly(floatRow, objectFloat)
	require.Equal(t, 42.5, objectFloat.Value(floatRow))
	require.Equal(t, rdflibgo.XSDDouble.Value(), objectType.Value(floatRow))
	require.Equal(t, tripleHash("http://example.com/s2", "http://example.com/p", "42.5", rdflibgo.XSDDouble.Value()), hashes.Value(floatRow))

	stringRow := rowsBySubject["http://example.com/s3"]
	requireOnly(stringRow, objectString)
	require.Equal(t, "label", objectString.Value(stringRow))
	require.Equal(t, rdflibgo.XSDString.Value(), objectType.Value(stringRow))
	require.Equal(t, tripleHash("http://example.com/s3", "http://example.com/p", "label", rdflibgo.XSDString.Value()), hashes.Value(stringRow))

	geometryRow := rowsBySubject["http://example.com/s4"]
	requireOnly(geometryRow, objectGeometry)
	require.Equal(t, geoarrow.WKBBytes(expectedWKB), objectGeometry.Value(geometryRow))
	require.Equal(t, geoSPARQLWKTLiteral, objectType.Value(geometryRow))
	require.Equal(t, tripleHash("http://example.com/s4", "http://example.com/p", "POINT (1 2)", geoSPARQLWKTLiteral), hashes.Value(geometryRow))

	integerRow := rowsBySubject["http://example.com/s5"]
	requireOnly(integerRow, objectInteger)
	require.Equal(t, int64(42), objectInteger.Value(integerRow))
	require.Equal(t, rdflibgo.XSDInteger.Value(), objectType.Value(integerRow))

	byteRow := rowsBySubject["http://example.com/s6"]
	requireOnly(byteRow, objectByte)
	require.Equal(t, int32(-8), objectByte.Value(byteRow))
	require.Equal(t, pkg.XSDByte, objectType.Value(byteRow))

	// the dateTime is stored normalized to UTC
	timeRow := rowsBySubject["http://example.com/s7"]
	requireOnly(timeRow, objectTime)
	expectedTime, err := time.Parse(time.RFC3339, "2002-05-30T15:30:10Z")
	require.NoError(t, err)
	require.Equal(t, arrow.Timestamp(expectedTime.UnixMicro()), objectTime.Value(timeRow))
	require.Equal(t, rdflibgo.XSDDateTime.Value(), objectType.Value(timeRow))

	require.False(t, rdr.Next())
	require.NoError(t, rdr.Err())
}

// TestAppendObjectFieldsKeepsUnparseableTypedLiteralsAsStrings checks that a
// literal whose datatype names a typed column but whose lexical form does not
// parse as one -- an out-of-range byte, a zoneless dateTime -- is stored as a
// string with its datatype in object_type, rather than being mangled or
// dropped.
func TestAppendObjectFieldsKeepsUnparseableTypedLiteralsAsStrings(t *testing.T) {
	graph := rdflibgo.NewGraph()
	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s1"),
		predicate,
		rdflibgo.NewLiteral("4200", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(pkg.XSDByte))),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s2"),
		predicate,
		rdflibgo.NewLiteral("2002-05-30T09:30:10", rdflibgo.WithDatatype(rdflibgo.XSDDateTime)),
	)

	arrowSchema, _, err := GetSchemas()
	require.NoError(t, err)
	rdr := newGraphRecordReader(graph, arrowSchema, 10)
	defer rdr.Release()

	require.True(t, rdr.Next())
	rec := rdr.RecordBatch()
	require.Equal(t, int64(2), rec.NumRows())

	subjects := rec.Column(0).(*array.String)
	objectString := rec.Column(2).(*array.String)
	objectByte := rec.Column(5).(*array.Int32)
	objectTime := rec.Column(8).(*array.Timestamp)
	objectType := rec.Column(9).(*array.String)
	for i := 0; i < int(rec.NumRows()); i++ {
		require.True(t, objectByte.IsNull(i))
		require.True(t, objectTime.IsNull(i))
		switch subjects.Value(i) {
		case "http://example.com/s1":
			require.Equal(t, "4200", objectString.Value(i))
			require.Equal(t, pkg.XSDByte, objectType.Value(i))
		case "http://example.com/s2":
			require.Equal(t, "2002-05-30T09:30:10", objectString.Value(i))
			require.Equal(t, rdflibgo.XSDDateTime.Value(), objectType.Value(i))
		}
	}
}

// TestTripleHashIncludesTheDatatype checks that two literals with the same
// lexical form but different datatypes are distinct rows, matching what
// object_type records, while the storage representation still plays no part.
func TestTripleHashIncludesTheDatatype(t *testing.T) {
	typedLiteral := rdflibgo.NewLiteral("2026-06-02", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe("http://www.w3.org/2001/XMLSchema#date")))
	triple := rdflibgo.Triple{
		Subject:   rdflibgo.NewURIRefUnsafe("http://example.com/s"),
		Predicate: rdflibgo.NewURIRefUnsafe("http://purl.org/dc/terms/created"),
		Object:    typedLiteral,
	}

	hashFromTriple := tripleHashForTriple(triple)
	hashFromTerms := tripleHash("http://example.com/s", "http://purl.org/dc/terms/created", "2026-06-02", "http://www.w3.org/2001/XMLSchema#date")

	require.Equal(t, hashFromTerms, hashFromTriple)
	require.Len(t, hashFromTriple, 64)
	require.NotEqual(t, hashFromTriple, tripleHash("http://example.com/s", "http://purl.org/dc/terms/created", "2026-06-02", rdflibgo.XSDString.Value()))
}

func TestBlankNodeTermsAreStoredWithNTriplesPrefix(t *testing.T) {
	require.Equal(t, "_:b1", storedSubject(rdflibgo.NewBNode("b1")))

	object := graphTripleObject(rdflibgo.NewBNode("b1"))
	require.Equal(t, "_:b1", object.o)
	require.Equal(t, objectKindBNode, object.oKind)
}

func TestStabilizeBlankNodesStabilizesBlankNodeHashes(t *testing.T) {
	first := graphWithGeometryBlankNode("first")
	second := graphWithGeometryBlankNode("second")

	canonicalFirst := stabilizeBlankNodes(first)
	canonicalSecond := stabilizeBlankNodes(second)

	require.Equal(t, tripleHashes(canonicalFirst), tripleHashes(canonicalSecond))
}

func TestStabilizeBlankNodesPreservesRelativeIRIs(t *testing.T) {
	graph := rdflibgo.NewGraph(rdflibgo.WithBase("https://example.test/base/"))
	graph.Add(
		rdflibgo.NewURIRefUnsafe("Organization001"),
		rdflibgo.NewURIRefUnsafe("https://schema.org/worksFor"),
		rdflibgo.NewURIRefUnsafe("org/acme"),
	)

	stable := stabilizeBlankNodes(graph)

	require.Same(t, graph, stable)
	require.Equal(t, tripleHashes(graph), tripleHashes(stable))
}

func TestStabilizeBlankNodesStabilizesNestedBlankNodeHashes(t *testing.T) {
	first := graphWithNestedBlankNodes("location1", "address1")
	second := graphWithNestedBlankNodes("location2", "address2")

	stableFirst := stabilizeBlankNodes(first)
	stableSecond := stabilizeBlankNodes(second)

	require.Equal(t, tripleHashes(stableFirst), tripleHashes(stableSecond))
}

func graphWithGeometryBlankNode(id string) *rdflibgo.Graph {
	graph := rdflibgo.NewGraph()
	subject := rdflibgo.NewURIRefUnsafe("http://example.com/place")
	blank := rdflibgo.NewBNode(id)
	graph.Add(subject, rdflibgo.NewURIRefUnsafe("http://www.opengis.net/ont/geosparql#hasGeometry"), blank)
	graph.Add(blank, rdflibgo.RDF.Type, rdflibgo.NewURIRefUnsafe("http://www.opengis.net/ont/sf#MultiPolygon"))
	graph.Add(blank, rdflibgo.NewURIRefUnsafe("http://www.opengis.net/ont/geosparql#asWKT"), rdflibgo.NewLiteral("POINT (1 2)", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(geoSPARQLWKTLiteral))))
	return graph
}

func graphWithNestedBlankNodes(locationID string, addressID string) *rdflibgo.Graph {
	graph := rdflibgo.NewGraph()
	subject := rdflibgo.NewURIRefUnsafe("http://example.com/place")
	location := rdflibgo.NewBNode(locationID)
	address := rdflibgo.NewBNode(addressID)
	graph.Add(subject, rdflibgo.NewURIRefUnsafe("https://schema.org/location"), location)
	graph.Add(location, rdflibgo.NewURIRefUnsafe("https://schema.org/address"), address)
	graph.Add(address, rdflibgo.NewURIRefUnsafe("https://schema.org/streetAddress"), rdflibgo.NewLiteral("100 Example Street"))
	return graph
}

func tripleHashes(graph *rdflibgo.Graph) map[string]struct{} {
	hashes := map[string]struct{}{}
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		hashes[tripleHashForTriple(triple)] = struct{}{}
		return true
	})
	return hashes
}
