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
}`)

	require.NoError(t, err)
	require.Equal(t, `SELECT t0.subject AS "s"
FROM triples AS t0
WHERE t0.predicate = 'https://schema.org/name'
  AND t0.object_string = 'bob'`, query)
}

func TestToSQLFiltersSubjectAndIRIObject(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?name
WHERE {
  <https://example.org/alice> schema:knows <https://example.org/bob> .
  <https://example.org/bob> schema:name ?name .
}`)

	require.NoError(t, err)
	require.Equal(t, `SELECT `+objectProjection("t1")+` AS "name"
FROM triples AS t0
CROSS JOIN triples AS t1
WHERE t0.subject = 'https://example.org/alice'
  AND t0.predicate = 'https://schema.org/knows'
  AND t0.object_iri = 'https://example.org/bob'
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
}`)

	require.NoError(t, err)
	require.Contains(t, query, "t0.subject = t1.subject")
	require.Contains(t, query, objectProjection("t1")+` AS "age"`)
}

func TestToSQLComparesLiteralsInTheStringColumn(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>

SELECT ?s
WHERE {
  ?s schema:startDate "2026-06-02"^^xsd:date .
}`)

	require.NoError(t, err)
	require.Contains(t, query, "t0.predicate = 'https://schema.org/startDate'")
	require.Contains(t, query, "t0.object_string = '2026-06-02'")
	require.NotContains(t, query, "t0.object =")
}

func TestToSQLComparesNumbersInTheNumericColumns(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:elevation 12.5 .
}`)

	require.NoError(t, err)
	require.Contains(t, query, objectNumeric("t0")+" = 12.5")
}

// A quoted number is an xsd:string, which build stores in object_string, so it
// is compared there rather than in the numeric columns.
func TestToSQLComparesQuotedNumbersInTheStringColumn(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:name "42" .
}`)

	require.NoError(t, err)
	require.Contains(t, query, "t0.object_string = '42'")
}

func TestToSQLComparesTypedIntegersInTheNumericColumns(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>

SELECT ?s
WHERE {
  ?s schema:age "42"^^xsd:integer .
}`)

	require.NoError(t, err)
	require.Contains(t, query, objectNumeric("t0")+" = 42")
}

// A dateTime with an explicit timezone is stored in object_time normalized to
// UTC, so it is compared there as the TIMESTAMP it was stored as.
func TestToSQLComparesDateTimesInTheTimeColumn(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>

SELECT ?s
WHERE {
  ?s schema:startDate "2002-05-30T09:30:10-06:00"^^xsd:dateTime .
}`)

	require.NoError(t, err)
	require.Contains(t, query, "t0.object_time = TIMESTAMP '2002-05-30 15:30:10'")
}

// A zoneless dateTime is stored as a string, so it is compared there.
func TestToSQLComparesZonelessDateTimesInTheStringColumn(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>

SELECT ?s
WHERE {
  ?s schema:startDate "2002-05-30T09:30:10"^^xsd:dateTime .
}`)

	require.NoError(t, err)
	require.Contains(t, query, "t0.object_string = '2002-05-30T09:30:10'")
}

func TestToSQLUsesTypedObjectColumnsForIRIObjectFilters(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:url <https://example.org/place> .
}`)

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
}`)

	require.NoError(t, err)
	require.Contains(t, query, objectNumeric("t0")+" > 21")
}

func TestToSQLSupportsAndFilter(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?s schema:age ?age .
  FILTER(?age >= 21 && ?age < 65)
}`)

	require.NoError(t, err)
	require.Contains(t, query, "("+objectNumeric("t0")+" >= 21 AND "+objectNumeric("t0")+" < 65)")
}

func TestToSQLTranslatesAskToAnExistsProjection(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

ASK {
  ?s schema:name "bob" .
}`)

	require.NoError(t, err)
	require.Equal(t, `SELECT EXISTS (SELECT 1
  FROM triples AS t0
  WHERE t0.predicate = 'https://schema.org/name'
    AND t0.object_string = 'bob') AS "ask"`, query)
}

func TestToSQLTranslatesOptionalToALeftJoin(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?s ?age
WHERE {
  ?s schema:name "bob" .
  OPTIONAL { ?s schema:age ?age . }
}`)

	require.NoError(t, err)
	require.Equal(t, `SELECT t0.subject AS "s", `+objectProjection("o0_0")+` AS "age"
FROM triples AS t0
LEFT JOIN triples AS o0_0 ON o0_0.predicate = 'https://schema.org/age'
  AND t0.subject = o0_0.subject
WHERE t0.predicate = 'https://schema.org/name'
  AND t0.object_string = 'bob'`, query)
}

func TestToSQLRejectsUnboundProjection(t *testing.T) {
	_, err := ToSQL(`
PREFIX schema: <https://schema.org/>

SELECT ?missing
WHERE {
  ?s schema:name "bob" .
}`)

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
}`)

	require.NoError(t, err)
	require.Contains(t, query, `SELECT t0.subject AS "property", `+objectProjection("t0")+` AS "type"`)
	require.Contains(t, query, `t0.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'`)
	require.Contains(t, query, `t0.object_iri = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#Property'`)
	require.Contains(t, query, `t0.object_iri = 'http://www.w3.org/2002/07/owl#AnnotationProperty'`)
}

func TestToSQLProjectsGeometriesAsWKT(t *testing.T) {
	query, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>

SELECT ?s ?wkt
WHERE {
  ?s geo:asWKT ?wkt .
}`)

	require.NoError(t, err)
	require.Contains(t, query, objectProjection("t0")+` AS "wkt"`)
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
}`)

	require.NoError(t, err)
	require.NotContains(t, query, "ST_AsText")
	require.Contains(t, query, objectText("t0")+" = t1.subject")
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
}`)

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
}`)

	require.NoError(t, err)
	require.Contains(t, query, "(ST_Within(t0.object_geometry, t1.object_geometry) AND NOT (ST_Equals(t0.object_geometry, t1.object_geometry)))")
}

func TestToSQLDropsTheCRSFromAWKTLiteral(t *testing.T) {
	query, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfDisjoint(?wkt, "<http://www.opengis.net/def/crs/OGC/1.3/CRS84> POINT(1 2)"^^geo:wktLiteral))
}`)

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
}`)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Distance_Sphere("+
		"ST_Transform(ST_ClosestPoint(ST_Transform(t0.object_geometry, 'EPSG:4326', 'EPSG:3857'), ST_Transform(ST_GeomFromText('POINT(-89.5 40.5)'), 'EPSG:4326', 'EPSG:3857')), 'EPSG:3857', 'EPSG:4326'), "+
		"ST_Transform(ST_ClosestPoint(ST_Transform(ST_GeomFromText('POINT(-89.5 40.5)'), 'EPSG:4326', 'EPSG:3857'), ST_Transform(t0.object_geometry, 'EPSG:4326', 'EPSG:3857')), 'EPSG:3857', 'EPSG:4326')"+
		") < 5000")

	query, err = ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:distance(?wkt, "POINT(-89.5 40.5)"^^geo:wktLiteral) <= 0.5)
}`)

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
}`)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Intersects(ST_Envelope(t0.object_geometry), ST_GeomFromText('POINT(-89.5 40.5)'))")
}

const geoPrefixes = `
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>
PREFIX uom: <http://www.opengis.net/def/uom/OGC/1.0/>
`

func TestToSQLTranslatesGeoSPARQLMeasuresByUnit(t *testing.T) {
	query, err := ToSQL(geoPrefixes + `SELECT ?g (geof:area(?wkt, uom:degree) AS ?a) (geof:metricArea(?wkt) AS ?m) WHERE { ?g geo:asWKT ?wkt . FILTER(geof:length(?wkt, uom:metre) > 100 && geof:perimeter(?wkt, uom:degree) < 1) }`)

	require.NoError(t, err)
	require.Contains(t, query, `ST_Area(t0.object_geometry) AS "a", ST_Area_Spheroid(t0.object_geometry) AS "m"`)
	require.Contains(t, query, "(ST_Length_Spheroid(t0.object_geometry) > 100 AND ST_Perimeter(t0.object_geometry) < 1)")

	query, err = ToSQL(geoPrefixes + `SELECT ?g WHERE { ?g geo:asWKT ?wkt . FILTER(geof:metricLength(?wkt) > 100 && geof:metricPerimeter(?wkt) > 100) }`)

	require.NoError(t, err)
	require.Contains(t, query, "(ST_Length_Spheroid(t0.object_geometry) > 100 AND ST_Perimeter_Spheroid(t0.object_geometry) > 100)")
}

func TestToSQLRejectsAMeasureInAnUnknownUnit(t *testing.T) {
	_, err := ToSQL(geoPrefixes + `SELECT ?g WHERE { ?g geo:asWKT ?wkt . FILTER(geof:area(?wkt, uom:foot) > 1) }`)

	require.ErrorContains(t, err, `geof:area unit "http://www.opengis.net/def/uom/OGC/1.0/foot" is not supported; use uom:degree or uom:metre`)

	_, err = ToSQL(geoPrefixes + `SELECT ?g WHERE { ?g geo:asWKT ?wkt . FILTER(geof:area(?wkt) > 1) }`)

	require.ErrorContains(t, err, "geof:area takes a geometry and a unit, got 1 arguments")

	_, err = ToSQL(geoPrefixes + `SELECT ?g WHERE { ?g geo:asWKT ?wkt . FILTER(geof:metricArea(?wkt, uom:metre) > 1) }`)

	require.ErrorContains(t, err, "geof:metricarea takes one geometry, got 2 arguments")
}

func TestToSQLTranslatesGeoSPARQLBufferByUnit(t *testing.T) {
	query, err := ToSQL(geoPrefixes + `SELECT ?g WHERE { ?g geo:asWKT ?wkt . FILTER(geof:sfWithin("POINT(-89.5 40.5)"^^geo:wktLiteral, geof:buffer(?wkt, 0.5, uom:degree))) }`)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Within(ST_GeomFromText('POINT(-89.5 40.5)'), ST_Buffer(t0.object_geometry, 0.5))")

	metres := "ST_Transform(ST_Buffer(ST_Transform(t0.object_geometry, 'EPSG:4326', 'EPSG:3857'), 1000 / cos(radians(ST_Y(ST_Centroid(t0.object_geometry))))), 'EPSG:3857', 'EPSG:4326')"
	query, err = ToSQL(geoPrefixes + `SELECT ?g (geof:buffer(?wkt, 1000, uom:metre) AS ?b) WHERE { ?g geo:asWKT ?wkt }`)

	require.NoError(t, err)
	require.Contains(t, query, "ST_AsText("+metres+`) AS "b"`)

	query, err = ToSQL(geoPrefixes + `SELECT ?g (geof:metricBuffer(?wkt, 1000) AS ?b) WHERE { ?g geo:asWKT ?wkt }`)

	require.NoError(t, err)
	require.Contains(t, query, "ST_AsText("+metres+`) AS "b"`)

	// the radius is an expression like any other
	query, err = ToSQL(geoPrefixes + `PREFIX schema: <https://schema.org/>
SELECT ?g WHERE { ?g geo:asWKT ?wkt ; schema:radius ?r . FILTER(geof:sfContains(geof:buffer(?wkt, ?r * 2, uom:degree), ?wkt)) }`)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Buffer(t0.object_geometry, (COALESCE(t1.object_float, CAST(t1.object_integer AS DOUBLE), CAST(t1.object_byte AS DOUBLE)) * 2))")

	_, err = ToSQL(geoPrefixes + `SELECT ?g WHERE { ?g geo:asWKT ?wkt . FILTER(geof:sfContains(geof:buffer(?wkt, 1), ?wkt)) }`)

	require.ErrorContains(t, err, "geof:buffer takes a geometry, a radius, and a unit, got 2 arguments")
}

func TestToSQLTranslatesGeoSPARQLMetricDistance(t *testing.T) {
	query, err := ToSQL(geoPrefixes + `SELECT ?g WHERE { ?g geo:asWKT ?wkt . FILTER(geof:metricDistance(?wkt, "POINT(-89.5 40.5)"^^geo:wktLiteral) < 5000) }`)

	require.NoError(t, err)
	require.Contains(t, query, "ST_Distance_Sphere(ST_Transform(ST_ClosestPoint(")
	require.Contains(t, query, ") < 5000")
}

func TestToSQLTranslatesGeoSPARQLGeometryProperties(t *testing.T) {
	query, err := ToSQL(geoPrefixes + `SELECT ?g (geof:minX(?wkt) AS ?x0) (geof:maxY(?wkt) AS ?y1) (geof:dimension(?wkt) AS ?d) (geof:numGeometries(?wkt) AS ?n) (geof:centroid(?wkt) AS ?c)
WHERE { ?g geo:asWKT ?wkt . FILTER(geof:isSimple(?wkt) && !geof:isEmpty(?wkt) && geof:minZ(?wkt) < 10) }`)

	require.NoError(t, err)
	require.Contains(t, query, `ST_XMin(t0.object_geometry) AS "x0", ST_YMax(t0.object_geometry) AS "y1", ST_Dimension(t0.object_geometry) AS "d", ST_NumGeometries(t0.object_geometry) AS "n", ST_AsText(ST_Centroid(t0.object_geometry)) AS "c"`)
	require.Contains(t, query, "(ST_IsSimple(t0.object_geometry) AND NOT (ST_IsEmpty(t0.object_geometry))) AND ST_ZMin(t0.object_geometry) < 10")

	query, err = ToSQL(geoPrefixes + `SELECT ?g (geof:coordinateDimension(?wkt) AS ?cd) (geof:spatialDimension(?wkt) AS ?sd) WHERE { ?g geo:asWKT ?wkt . FILTER(!geof:is3D(?wkt) && !geof:isMeasured(?wkt)) }`)

	require.NoError(t, err)
	require.Contains(t, query, `2 + CASE WHEN ST_HasZ(t0.object_geometry) THEN 1 ELSE 0 END + CASE WHEN ST_HasM(t0.object_geometry) THEN 1 ELSE 0 END AS "cd", 2 + CASE WHEN ST_HasZ(t0.object_geometry) THEN 1 ELSE 0 END AS "sd"`)
	require.Contains(t, query, "(NOT (ST_HasZ(t0.object_geometry)) AND NOT (ST_HasM(t0.object_geometry)))")
}

// A serialization is a string, so it is projected as it is rather than
// through the ST_AsText a geometry goes through.
func TestToSQLTranslatesGeoSPARQLSerializations(t *testing.T) {
	query, err := ToSQL(geoPrefixes + `SELECT ?g (geof:asWKT(?wkt) AS ?w) (geof:asGeoJSON(?wkt) AS ?j) WHERE { ?g geo:asWKT ?wkt . FILTER(STRSTARTS(geof:asWKT(?wkt), "POINT")) }`)

	require.NoError(t, err)
	require.Contains(t, query, `ST_AsText(t0.object_geometry) AS "w", ST_AsGeoJSON(t0.object_geometry) AS "j"`)
	require.Contains(t, query, "starts_with(ST_AsText(t0.object_geometry), 'POINT')")
}

func TestToSQLTranslatesGeoSPARQLGeometryTypeAndSRID(t *testing.T) {
	query, err := ToSQL(geoPrefixes + `PREFIX sf: <http://www.opengis.net/ont/sf#>
SELECT ?g (geof:getSRID(?wkt) AS ?crs) WHERE { ?g geo:asWKT ?wkt . FILTER(geof:geometryType(?wkt) = sf:Polygon) }`)

	require.NoError(t, err)
	require.Contains(t, query, `CASE WHEN t0.object_geometry IS NOT NULL THEN 'http://www.opengis.net/def/crs/OGC/1.3/CRS84' END AS "crs"`)
	require.Contains(t, query, "CASE CAST(ST_GeometryType(t0.object_geometry) AS VARCHAR) WHEN 'POINT' THEN 'http://www.opengis.net/ont/sf#Point' WHEN 'LINESTRING' THEN 'http://www.opengis.net/ont/sf#LineString' WHEN 'POLYGON' THEN 'http://www.opengis.net/ont/sf#Polygon' WHEN 'MULTIPOINT' THEN 'http://www.opengis.net/ont/sf#MultiPoint' WHEN 'MULTILINESTRING' THEN 'http://www.opengis.net/ont/sf#MultiLineString' WHEN 'MULTIPOLYGON' THEN 'http://www.opengis.net/ont/sf#MultiPolygon' WHEN 'GEOMETRYCOLLECTION' THEN 'http://www.opengis.net/ont/sf#GeometryCollection' END = 'http://www.opengis.net/ont/sf#Polygon'")
}

func TestToSQLRejectsGeoSPARQLOnNonGeometryVariables(t *testing.T) {
	_, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(?g, ?wkt))
}`)

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
  FILTER(geof:relate(?wkt, ?wkt, "T*F**FFF*"))
}`)

	require.ErrorContains(t, err, "GeoSPARQL function geof:relate is not supported: DuckDB's spatial extension has no DE-9IM function to answer it")

	_, err = ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(geof:transform(?wkt, <http://www.opengis.net/def/crs/EPSG/0/3857>), ?wkt))
}`)

	require.ErrorContains(t, err, "GeoSPARQL function geof:transform is not supported: every geometry is stored in CRS84")

	_, err = ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(geof:boundingCircle(?wkt), ?wkt))
}`)

	require.ErrorContains(t, err, "GeoSPARQL function geof:boundingcircle is not supported yet")

	_, err = ToSQL(`
SELECT ?s
WHERE {
  ?s ?p ?o .
  FILTER(<http://example.org/myfunction>(?o))
}`)

	require.ErrorContains(t, err, `SPARQL function "http://example.org/myfunction" is not supported yet`)
}

func TestToSQLRejectsNonWKTGeometryLiterals(t *testing.T) {
	_, err := ToSQL(`
PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?g
WHERE {
  ?g geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(?wkt, "<gml:Point/>"^^geo:gmlLiteral))
}`)

	require.ErrorContains(t, err, "must be a geo:wktLiteral")
}

func TestToSQLProjectsTheLanguageTagAndDatatype(t *testing.T) {
	query, err := ToSQL(`
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT ?s ?label (LANG(?label) AS ?lang) (DATATYPE(?label) AS ?type)
WHERE {
  ?s rdfs:label ?label .
}`)

	require.NoError(t, err)
	require.Equal(t, `SELECT t0.subject AS "s", `+objectProjection("t0")+` AS "label", CASE WHEN t0.object_type IS NULL THEN NULL ELSE COALESCE(t0.object_language, '') END AS "lang", t0.object_type AS "type"
FROM triples AS t0
WHERE t0.predicate = 'http://www.w3.org/2000/01/rdf-schema#label'`, query)
}

func TestToSQLFiltersOnTheLanguageTag(t *testing.T) {
	query, err := ToSQL(`
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT ?label
WHERE {
  ?s rdfs:label ?label .
  FILTER(lang(?label) = "en")
}`)

	require.NoError(t, err)
	require.Contains(t, query, "AND CASE WHEN t0.object_type IS NULL THEN NULL ELSE COALESCE(t0.object_language, '') END = 'en'")
}

func TestToSQLFiltersOnTheDatatype(t *testing.T) {
	query, err := ToSQL(`
PREFIX schema: <https://schema.org/>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>

SELECT ?date
WHERE {
  ?s schema:birthDate ?date .
  FILTER(DATATYPE(?date) = xsd:date)
}`)

	require.NoError(t, err)
	require.Contains(t, query, "AND t0.object_type = 'http://www.w3.org/2001/XMLSchema#date'")
}

// LANGMATCHES follows RFC 4647 basic filtering: a range matches a tag equal to
// it or extending it with a subtag, and "*" matches any tagged literal. The
// range is lowered since the table stores tags lower case.
func TestToSQLTranslatesLangMatches(t *testing.T) {
	query, err := ToSQL(`
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT ?label
WHERE {
  ?s rdfs:label ?label .
  FILTER(langMatches(LANG(?label), "EN"))
}`)
	require.NoError(t, err)
	require.Contains(t, query, "AND (t0.object_language = 'en' OR starts_with(t0.object_language, 'en-'))")

	query, err = ToSQL(`
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT ?label
WHERE {
  ?s rdfs:label ?label .
  FILTER(langMatches(LANG(?label), "*"))
}`)
	require.NoError(t, err)
	require.Contains(t, query, "AND t0.object_language IS NOT NULL")
}

func TestToSQLMatchesALanguageTaggedLiteralByValueAndTag(t *testing.T) {
	query, err := ToSQL(`
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT ?s
WHERE {
  ?s rdfs:label "My test datatype"@EN .
}`)

	require.NoError(t, err)
	require.Contains(t, query, "AND t0.object_string = 'My test datatype'")
	require.Contains(t, query, "AND t0.object_language = 'en'")
}

func TestToSQLRejectsLangOnASubjectVariable(t *testing.T) {
	_, err := ToSQL(`
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT ?s
WHERE {
  ?s rdfs:label ?label .
  FILTER(LANG(?s) = "en")
}`)

	require.ErrorContains(t, err, "LANG(?s) is not defined: ?s is bound as a subject")
}

func TestToSQLRejectsLangMatchesWithoutALangCall(t *testing.T) {
	_, err := ToSQL(`
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT ?s
WHERE {
  ?s rdfs:label ?label .
  FILTER(langMatches(?label, "en"))
}`)

	require.ErrorContains(t, err, "the first argument of LANGMATCHES must be LANG(?variable)")
}

func TestToSQLProjectsAggregatesAndVariableExpressions(t *testing.T) {
	query, err := ToSQL(`
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT (COUNT(?s) AS ?n)
WHERE {
  ?s rdfs:label ?label .
}`)

	require.NoError(t, err)
	require.Equal(t, `SELECT count(t0.subject) AS "n"
FROM triples AS t0
WHERE t0.predicate = 'http://www.w3.org/2000/01/rdf-schema#label'`, query)

	query, err = ToSQL(`
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT (?s AS ?resource)
WHERE {
  ?s rdfs:label ?label .
}`)

	require.NoError(t, err)
	require.Contains(t, query, `SELECT t0.subject AS "resource"`)
}
