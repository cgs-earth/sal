package load

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
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

	arrowSchema, _, err := GetSchemas(false)
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
		objects := rec.Column(2).(*array.String)
		hashes := rec.Column(3).(*array.String)
		for i := 0; i < int(rec.NumRows()); i++ {
			rows = append(rows, [4]string{subjects.Value(i), predicates.Value(i), objects.Value(i), hashes.Value(i)})
		}
	}

	require.NoError(t, rdr.Err())
	require.Equal(t, 2, batches)
	require.Equal(t, int64(3), rdr.RowsRead())
	require.ElementsMatch(t, [][4]string{
		{"http://example.com/s1", "http://example.com/p", "one", tripleHash("http://example.com/s1", "http://example.com/p", "one")},
		{"subject", "http://example.com/p", "http://example.com/o2", tripleHash("subject", "http://example.com/p", "http://example.com/o2")},
		{"http://example.com/s3", "http://example.com/p", "three", tripleHash("http://example.com/s3", "http://example.com/p", "three")},
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
		rdflibgo.NewLiteral("42.5"),
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

	arrowSchema, _, err := GetSchemas(true)
	require.NoError(t, err)
	rdr := newGraphRecordReader(graph, arrowSchema, 10)
	defer rdr.Release()

	require.True(t, rdr.Next())
	rec := rdr.RecordBatch()
	require.Equal(t, int64(4), rec.NumRows())

	objectIRI := rec.Column(2).(*array.String)
	objectFloat := rec.Column(3).(*array.Float64)
	objectString := rec.Column(4).(*array.String)
	objectGeometry := rec.Column(5).(*geoarrow.WKBArray)
	hashes := rec.Column(6).(*array.String)
	subjects := rec.Column(0).(*array.String)

	expectedWKB, err := wktObjectToWKB("POINT (1 2)")
	require.NoError(t, err)

	rowsBySubject := map[string]int{}
	for i := 0; i < int(rec.NumRows()); i++ {
		rowsBySubject[subjects.Value(i)] = i
	}
	require.Contains(t, rowsBySubject, "http://example.com/s1")
	require.Contains(t, rowsBySubject, "http://example.com/s2")
	require.Contains(t, rowsBySubject, "http://example.com/s3")
	require.Contains(t, rowsBySubject, "http://example.com/s4")

	iriRow := rowsBySubject["http://example.com/s1"]
	require.Equal(t, "http://example.com/o", objectIRI.Value(iriRow))
	require.True(t, objectFloat.IsNull(iriRow))
	require.True(t, objectString.IsNull(iriRow))
	require.True(t, objectGeometry.IsNull(iriRow))
	require.Equal(t, tripleHash("http://example.com/s1", "http://example.com/p", "http://example.com/o"), hashes.Value(iriRow))

	floatRow := rowsBySubject["http://example.com/s2"]
	require.True(t, objectIRI.IsNull(floatRow))
	require.Equal(t, 42.5, objectFloat.Value(floatRow))
	require.True(t, objectString.IsNull(floatRow))
	require.True(t, objectGeometry.IsNull(floatRow))
	require.Equal(t, tripleHash("http://example.com/s2", "http://example.com/p", "42.5"), hashes.Value(floatRow))

	stringRow := rowsBySubject["http://example.com/s3"]
	require.True(t, objectIRI.IsNull(stringRow))
	require.True(t, objectFloat.IsNull(stringRow))
	require.Equal(t, "label", objectString.Value(stringRow))
	require.True(t, objectGeometry.IsNull(stringRow))
	require.Equal(t, tripleHash("http://example.com/s3", "http://example.com/p", "label"), hashes.Value(stringRow))

	geometryRow := rowsBySubject["http://example.com/s4"]
	require.True(t, objectIRI.IsNull(geometryRow))
	require.True(t, objectFloat.IsNull(geometryRow))
	require.True(t, objectString.IsNull(geometryRow))
	require.Equal(t, geoarrow.WKBBytes(expectedWKB), objectGeometry.Value(geometryRow))
	require.Equal(t, tripleHash("http://example.com/s4", "http://example.com/p", "POINT (1 2)"), hashes.Value(geometryRow))

	require.False(t, rdr.Next())
	require.NoError(t, rdr.Err())
}

func TestTripleHashUsesTermsWithoutTypeMarkers(t *testing.T) {
	typedLiteral := rdflibgo.NewLiteral("2026-06-02", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe("http://www.w3.org/2001/XMLSchema#date")))
	triple := rdflibgo.Triple{
		Subject:   rdflibgo.NewURIRefUnsafe("http://example.com/s"),
		Predicate: rdflibgo.NewURIRefUnsafe("http://purl.org/dc/terms/created"),
		Object:    typedLiteral,
	}

	hashFromTriple := tripleHashForTriple(triple)
	hashFromTerms := tripleHash("http://example.com/s", "http://purl.org/dc/terms/created", "2026-06-02")

	require.Equal(t, hashFromTerms, hashFromTriple)
	require.Len(t, hashFromTriple, 64)
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
