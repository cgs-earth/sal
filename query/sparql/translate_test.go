package sparql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToSQLFiltersPredicateAndLiteralObject(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:name "bob" .
}`, SimpleObjects)

	require.NoError(t, err)
	require.Equal(t, `SELECT t0.subject AS "s"
FROM triples AS t0
WHERE t0.predicate = 'https://schema.org/name'
  AND t0.object = 'bob'`, query)
}

func TestToSQLFiltersSubjectAndIRIObject(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?name
WHERE {
  <https://example.org/alice> schema:knows <https://example.org/bob> .
  <https://example.org/bob> schema:name ?name .
}`, SimpleObjects)

	require.NoError(t, err)
	require.Equal(t, `SELECT t1.object AS "name"
FROM triples AS t0
CROSS JOIN triples AS t1
WHERE t0.subject = 'https://example.org/alice'
  AND t0.predicate = 'https://schema.org/knows'
  AND t0.object = 'https://example.org/bob'
  AND t1.subject = 'https://example.org/bob'
  AND t1.predicate = 'https://schema.org/name'`, query)
}

func TestToSQLJoinsRepeatedVariables(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s ?age
WHERE {
  ?s schema:name "bob" .
  ?s schema:age ?age .
}`, SimpleObjects)

	require.NoError(t, err)
	require.Contains(t, query, "t0.subject = t1.subject")
	require.Contains(t, query, `t1.object AS "age"`)
}

func TestToSQLUsesTypedObjectColumnsForLiteralFilters(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>

SELECT ?s
WHERE {
  ?s schema:startDate "2026-06-02"^^xsd:date .
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "t0.predicate = 'https://schema.org/startDate'")
	require.Contains(t, query, "t0.object_string = '2026-06-02'")
	require.NotContains(t, query, "t0.object =")
}

func TestToSQLUsesTypedObjectColumnsForNumericFilters(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:elevation 12.5 .
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "t0.object_float = 12.5")
}

func TestToSQLUsesTypedObjectColumnsForIRIObjectFilters(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:url <https://example.org/place> .
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "t0.object_iri = 'https://example.org/place'")
}

func TestToSQLSupportsSimpleLiteralFilter(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:age ?age .
  FILTER(?age > 21)
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "t0.object_float > 21")
}

func TestToSQLSupportsAndFilter(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:age ?age .
  FILTER(?age >= 21 && ?age < 65)
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "(t0.object_float >= 21 AND t0.object_float < 65)")
}

func TestToSQLRejectsAskQueries(t *testing.T) {
	_, err := ToSQL(`
PREFIX schema: <https://schema.org/>

ASK {
  ?s schema:name "bob" .
}`, SimpleObjects)

	require.ErrorContains(t, err, "only read-only SPARQL SELECT queries are supported")
}

func TestToSQLRejectsOptionalPatterns(t *testing.T) {
	_, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:name "bob" .
  OPTIONAL { ?s schema:age ?age . }
}`, SimpleObjects)

	require.ErrorContains(t, err, "only basic SPARQL triple patterns and FILTER expressions are supported")
}

func TestToSQLRejectsUnboundProjection(t *testing.T) {
	_, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?missing
WHERE {
  ?s schema:name "bob" .
}`, SimpleObjects)

	require.ErrorContains(t, err, "projected variable ?missing is not bound")
}

// The Properties starter query in the web UI. It is the one sample that
// filters a variable against several IRIs, so it is the one that pins down
// that the translator keeps the alternatives together.
func TestToSQLTranslatesTheUIPropertiesSample(t *testing.T) {
	query, err := ToSQL(`PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
PREFIX owl: <http://www.w3.org/2002/07/owl#>

SELECT ?property ?type
WHERE {
  ?property rdf:type ?type .

  FILTER (
    ?type = rdf:Property ||
    ?type = owl:ObjectProperty ||
    ?type = owl:DatatypeProperty ||
    ?type = owl:AnnotationProperty
  )
}`, SimpleObjects)

	require.NoError(t, err)
	require.Contains(t, query, `SELECT t0.subject AS "property", t0.object AS "type"`)
	require.Contains(t, query, `t0.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'`)
	require.Contains(t, query, `t0.object = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#Property'`)
	require.Contains(t, query, `t0.object = 'http://www.w3.org/2002/07/owl#AnnotationProperty'`)
}

func TestToSQLProjectsTypedObjectGeometriesAsWKT(t *testing.T) {
	query, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>

SELECT ?s ?wkt
WHERE {
  ?s geo:asWKT ?wkt .
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, `COALESCE(t0.object_iri, CAST(t0.object_float AS VARCHAR), t0.object_string, ST_AsText(t0.object_geometry)) AS "wkt"`)
}

func TestToSQLJoinsObjectVariablesWithoutTheGeometryColumn(t *testing.T) {
	// A join or a comparison never renders the geometry, so a query that only
	// projects subjects does not need the spatial extension.
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:knows ?o .
  ?o schema:name "bob" .
}`, TypedObjects)

	require.NoError(t, err)
	require.NotContains(t, query, "ST_AsText")
	require.Contains(t, query, "COALESCE(t0.object_iri, CAST(t0.object_float AS VARCHAR), t0.object_string) = t1.subject")
}

func TestToSQLTranslatesGeoSPARQLIntersectsWithWKTLiteral(t *testing.T) {
	query, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?feature
WHERE {
  ?feature geo:hasGeometry ?geometry .
  ?geometry geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(?wkt, "POLYGON((-91 39, -88 39, -88 42, -91 42, -91 39))"^^geo:wktLiteral))
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Intersects(t1.object_geometry, ST_GeomFromText('POLYGON((-91 39, -88 39, -88 42, -91 42, -91 39))'))")
}

func TestToSQLTranslatesGeoSPARQLRelationsBetweenTwoVariables(t *testing.T) {
	query, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?a ?b
WHERE {
  ?a geo:asWKT ?ga .
  ?b geo:asWKT ?gb .
  FILTER(geof:sfWithin(?ga, ?gb) && !geof:sfEquals(?ga, ?gb))
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "(ST_Within(t0.object_geometry, t1.object_geometry) AND NOT (ST_Equals(t0.object_geometry, t1.object_geometry)))")
}

func TestToSQLParsesWKTOnTheSimpleLayout(t *testing.T) {
	query, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfContains("POINT(-89.5 40.5)"^^geo:wktLiteral, ?wkt))
}`, SimpleObjects)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Contains(ST_GeomFromText('POINT(-89.5 40.5)'), ST_GeomFromText(t0.object))")
}

func TestToSQLDropsTheCRSFromAWKTLiteral(t *testing.T) {
	query, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfDisjoint(?wkt, "<http://www.opengis.net/def/crs/OGC/1.3/CRS84> POINT(1 2)"^^geo:wktLiteral))
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Disjoint(t0.object_geometry, ST_GeomFromText('POINT(1 2)'))")
}

func TestToSQLComparesGeoSPARQLDistance(t *testing.T) {
	query, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>
PREFIX uom: <http://www.opengis.net/def/uom/OGC/1.0/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:distance(?wkt, "POINT(-89.5 40.5)"^^geo:wktLiteral, uom:metre) < 5000)
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Distance_Sphere(t0.object_geometry, ST_GeomFromText('POINT(-89.5 40.5)')) < 5000")

	query, err = ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:distance(?wkt, "POINT(-89.5 40.5)"^^geo:wktLiteral) <= 0.5)
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Distance(t0.object_geometry, ST_GeomFromText('POINT(-89.5 40.5)')) <= 0.5")
}

func TestToSQLNestsGeometryConstructors(t *testing.T) {
	query, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(geof:envelope(?wkt), "POINT(-89.5 40.5)"^^geo:wktLiteral))
}`, TypedObjects)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Intersects(ST_Envelope(t0.object_geometry), ST_GeomFromText('POINT(-89.5 40.5)'))")
}

func TestToSQLRejectsGeoSPARQLOnNonGeometryVariables(t *testing.T) {
	_, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(?g, ?wkt))
}`, TypedObjects)

	require.ErrorContains(t, err, "?g is bound as a subject, not as a geometry literal")
}

func TestToSQLRejectsUnsupportedGeoSPARQLFunctions(t *testing.T) {
	_, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>
PREFIX uom: <http://www.opengis.net/def/uom/OGC/1.0/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(geof:buffer(?wkt, 1, uom:metre), ?wkt))
}`, TypedObjects)

	require.ErrorContains(t, err, "GeoSPARQL function geof:buffer is not supported yet")

	_, err = ToSQL(`
SELECT ?s
WHERE {
  ?s ?p ?o .
  FILTER(REGEX(?o, "bob"))
}`, TypedObjects)

	require.ErrorContains(t, err, `SPARQL function "regex" is not supported yet`)
}

func TestToSQLRejectsNonWKTGeometryLiterals(t *testing.T) {
	_, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(?wkt, "<gml:Point/>"^^geo:gmlLiteral))
}`, TypedObjects)

	require.ErrorContains(t, err, "must be a geo:wktLiteral")
}
