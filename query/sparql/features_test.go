package sparql

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// sampleTriples is a DuckDB with a triples table in the shape build writes,
// holding a few people, so that the SQL the translator writes for the
// constructs beyond basic patterns can be run rather than only compared as
// text. The spatial extension is loaded for the geometry column and the
// ST_AsText every projected object goes through.
func sampleTriples(t *testing.T) *sql.DB {
	t.Helper()
	db := localDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, "LOAD spatial")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TABLE triples AS SELECT * FROM (VALUES
	 ('http://a', 'https://schema.org/name', NULL::VARCHAR, NULL::DOUBLE, NULL::BIGINT, NULL::INTEGER, NULL::TIMESTAMP, NULL::GEOMETRY, 'alice', 'http://www.w3.org/2001/XMLSchema#string', NULL::VARCHAR, 'h1'),
	 ('http://a', 'https://schema.org/age', NULL, NULL, 30, NULL, NULL, NULL, NULL, 'http://www.w3.org/2001/XMLSchema#integer', NULL, 'h2'),
	 ('http://b', 'https://schema.org/name', NULL, NULL, NULL, NULL, NULL, NULL, 'bob', 'http://www.w3.org/2001/XMLSchema#string', NULL, 'h3'),
	 ('http://b', 'https://schema.org/age', NULL, NULL, 9, NULL, NULL, NULL, NULL, 'http://www.w3.org/2001/XMLSchema#integer', NULL, 'h4'),
	 ('http://a', 'https://schema.org/knows', 'http://b', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'h5'),
	 ('http://a', 'https://schema.org/knows', 'http://c', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'h6'),
	 ('http://c', 'http://www.w3.org/2000/01/rdf-schema#label', NULL, NULL, NULL, NULL, NULL, NULL, 'carol', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#langString', 'en', 'h7'),
	 ('http://c', 'https://schema.org/birthDate', NULL, NULL, NULL, NULL, TIMESTAMP '1990-06-15 00:00:00', NULL, NULL, 'http://www.w3.org/2001/XMLSchema#dateTime', NULL, 'h8'),
	 ('http://c', 'https://schema.org/geo', NULL, NULL, NULL, NULL, NULL, ST_GeomFromText('POINT(1 2)'), NULL, 'http://www.opengis.net/ont/geosparql#wktLiteral', NULL, 'h9')
	) t(subject, predicate, object_iri, object_float, object_integer, object_byte, object_time, object_geometry, object_string, object_type, object_language, triple_hash)`)
	require.NoError(t, err)
	return db
}

// answer translates a query and runs it against the sample triples.
func answer(t *testing.T, db *sql.DB, query string) (header []string, rows [][]string) {
	t.Helper()
	statement, err := ToSQL(query)
	require.NoError(t, err)
	header, rows, err = queryRows(context.Background(), db, statement)
	require.NoError(t, err, statement)
	return header, rows
}

const samplePrefixes = `
PREFIX schema: <https://schema.org/>
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>
`

func TestOptionalLeavesTheVariableUnboundWhenNothingMatches(t *testing.T) {
	header, rows := answer(t, sampleTriples(t), samplePrefixes+`
SELECT ?s ?label WHERE {
  <http://a> schema:knows ?s .
  OPTIONAL { ?s schema:name ?label }
}
ORDER BY ?s`)

	require.Equal(t, []string{"s", "label"}, header)
	require.Equal(t, [][]string{{"http://b", "bob"}, {"http://c", ""}}, rows)
}

func TestOptionalWithSeveralPatternsMatchesWholeOrNotAtAll(t *testing.T) {
	// bob has a name and an age, carol has neither, and alice is not known
	// by anyone; the group must not bind a name without an age
	_, rows := answer(t, sampleTriples(t), samplePrefixes+`
SELECT ?s ?name ?age WHERE {
  <http://a> schema:knows ?s .
  OPTIONAL { ?s schema:name ?name . ?s schema:age ?age }
}
ORDER BY ?s`)

	require.Equal(t, [][]string{{"http://b", "bob", "9"}, {"http://c", "", ""}}, rows)

	sql, err := ToSQL(samplePrefixes + `SELECT ?s WHERE { ?s schema:knows ?o OPTIONAL { ?o schema:name ?name . ?o schema:age ?age } }`)
	require.NoError(t, err)
	require.Contains(t, sql, "LEFT JOIN (triples AS o0_0\n  CROSS JOIN triples AS o0_1) ON ")
}

func TestOptionalFilterSeesTheVariablesOfThePatternBeforeIt(t *testing.T) {
	_, rows := answer(t, sampleTriples(t), samplePrefixes+`
SELECT ?s ?age WHERE {
  ?s schema:name ?name .
  OPTIONAL { ?s schema:age ?age FILTER(?age > 10 && ?name = "alice") }
}
ORDER BY ?s`)

	require.Equal(t, [][]string{{"http://a", "30"}, {"http://b", ""}}, rows)
}

func TestBoundAndNotExistsFindUnmatchedOptionals(t *testing.T) {
	db := sampleTriples(t)
	_, rows := answer(t, db, samplePrefixes+`
SELECT ?o WHERE {
  ?s schema:knows ?o .
  OPTIONAL { ?o schema:name ?name }
  FILTER(!BOUND(?name))
}`)
	require.Equal(t, [][]string{{"http://c"}}, rows)

	_, rows = answer(t, db, samplePrefixes+`
SELECT ?o WHERE {
  ?s schema:knows ?o .
  FILTER NOT EXISTS { ?o schema:name ?name }
}`)
	require.Equal(t, [][]string{{"http://c"}}, rows)
}

func TestOrderByOrdersNumbersAsNumbersAndOffsetSkipsRows(t *testing.T) {
	db := sampleTriples(t)
	// 9 sorts before 30 only when the numeric column is what is ordered
	_, rows := answer(t, db, samplePrefixes+`
SELECT ?s ?age WHERE { ?s schema:age ?age } ORDER BY DESC(?age)`)
	require.Equal(t, [][]string{{"http://a", "30"}, {"http://b", "9"}}, rows)

	_, rows = answer(t, db, samplePrefixes+`
SELECT ?s ?age WHERE { ?s schema:age ?age } ORDER BY ?age LIMIT 1 OFFSET 1`)
	require.Equal(t, [][]string{{"http://a", "30"}}, rows)

	sql, err := ToSQL(samplePrefixes + `SELECT ?s WHERE { ?s schema:age ?age } ORDER BY ?s LIMIT 1 OFFSET 1`)
	require.NoError(t, err)
	require.Contains(t, sql, "\nORDER BY t0.subject\nLIMIT 1\nOFFSET 1")
}

func TestAggregatesGroupAndFilterSolutions(t *testing.T) {
	db := sampleTriples(t)
	header, rows := answer(t, db, samplePrefixes+`
SELECT ?p (COUNT(*) AS ?n) WHERE { ?s ?p ?o } GROUP BY ?p HAVING (COUNT(*) > 1) ORDER BY DESC(?n) ?p`)
	require.Equal(t, []string{"p", "n"}, header)
	require.Equal(t, [][]string{{"https://schema.org/age", "2"}, {"https://schema.org/knows", "2"}, {"https://schema.org/name", "2"}}, rows)

	_, rows = answer(t, db, samplePrefixes+`
SELECT (MAX(?age) AS ?oldest) (MIN(?age) AS ?youngest) (SUM(?age) AS ?total) (COUNT(DISTINCT ?s) AS ?people)
WHERE { ?s schema:age ?age }`)
	require.Equal(t, [][]string{{"30.0", "9.0", "39.0", "2"}}, rows)

	_, rows = answer(t, db, samplePrefixes+`
SELECT ?s (GROUP_CONCAT(?o; SEPARATOR=", ") AS ?known) WHERE { ?s schema:knows ?o } GROUP BY ?s`)
	require.Equal(t, [][]string{{"http://a", "http://b, http://c"}}, rows)
}

func TestCountOfAnOptionalVariableCountsOnlyTheBoundOnes(t *testing.T) {
	_, rows := answer(t, sampleTriples(t), samplePrefixes+`
SELECT (COUNT(?s) AS ?people) (COUNT(?age) AS ?ages) WHERE {
  ?s schema:name ?name .
  OPTIONAL { ?s schema:age ?age FILTER(?age > 10) }
}`)

	require.Equal(t, [][]string{{"2", "1"}}, rows)
}

func TestValuesRestrictsAndBindsVariables(t *testing.T) {
	db := sampleTriples(t)
	_, rows := answer(t, db, samplePrefixes+`
SELECT ?s ?name WHERE {
  VALUES ?s { <http://a> <http://c> }
  ?s schema:name ?name .
}`)
	require.Equal(t, [][]string{{"http://a", "alice"}}, rows)

	// an UNDEF row is compatible with every solution
	_, rows = answer(t, db, samplePrefixes+`
SELECT ?s ?name ?tag WHERE {
  ?s schema:name ?name .
  VALUES (?name ?tag) { ("alice" "first") (UNDEF "any") }
}
ORDER BY ?s ?tag`)
	require.Equal(t, [][]string{{"http://a", "alice", "any"}, {"http://a", "alice", "first"}, {"http://b", "bob", "any"}}, rows)

	sql, err := ToSQL(samplePrefixes + `SELECT ?s WHERE { VALUES ?s { <http://a> } ?s schema:name ?name }`)
	require.NoError(t, err)
	require.Contains(t, sql, `CROSS JOIN (VALUES ('http://a')) AS v0("s")`)
	require.Contains(t, sql, `t0.subject = v0."s"`)
}

func TestUnionCombinesArmsAndKeepsAnObjectTyped(t *testing.T) {
	db := sampleTriples(t)
	header, rows := answer(t, db, samplePrefixes+`
SELECT ?s ?label (LANG(?label) AS ?lang) WHERE {
  <http://a> schema:knows ?s .
  { ?s schema:name ?label } UNION { ?s rdfs:label ?label }
}
ORDER BY ?s`)
	require.Equal(t, []string{"s", "label", "lang"}, header)
	require.Equal(t, [][]string{{"http://b", "bob", ""}, {"http://c", "carol", "en"}}, rows)

	// a variable one arm leaves unbound is NULL in that arm's rows, which
	// DuckDB orders last
	_, rows = answer(t, db, samplePrefixes+`
SELECT ?s ?name ?age WHERE {
  { ?s schema:name ?name } UNION { ?s schema:age ?age }
}
ORDER BY ?s ?name`)
	require.Equal(t, [][]string{{"http://a", "alice", ""}, {"http://a", "", "30"}, {"http://b", "bob", ""}, {"http://b", "", "9"}}, rows)
}

func TestUnionTranslatesToUnionAllOfItsArms(t *testing.T) {
	sql, err := ToSQL(samplePrefixes + `SELECT ?s WHERE { { ?s schema:name ?x } UNION { ?s rdfs:label ?x } UNION { ?s schema:age ?x } }`)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM (\nSELECT u0_0_0.subject AS \"s\", u0_0_0.object_iri AS \"x.object_iri\"")
	require.Contains(t, sql, "\nUNION ALL\nSELECT u0_1_0.subject AS \"s\"")
	require.Contains(t, sql, "\nUNION ALL\nSELECT u0_2_0.subject AS \"s\"")
	require.Contains(t, sql, ") AS u0")
}

func TestBindComputesAVariableTheRestOfTheGroupCanUse(t *testing.T) {
	db := sampleTriples(t)
	_, rows := answer(t, db, samplePrefixes+`
SELECT ?s ?shout ?decade WHERE {
  ?s schema:name ?name .
  ?s schema:age ?age .
  BIND(UCASE(?name) AS ?shout)
  BIND(FLOOR(?age / 10) * 10 AS ?decade)
  FILTER(?decade >= 10)
}`)
	require.Equal(t, [][]string{{"http://a", "ALICE", "30.0"}}, rows)

	// a value bound before a pattern constrains the pattern
	_, rows = answer(t, db, samplePrefixes+`
SELECT ?name WHERE {
  BIND(<http://b> AS ?s)
  ?s schema:name ?name .
}`)
	require.Equal(t, [][]string{{"bob"}}, rows)
}

func TestStringFunctionsTranslateToDuckDBs(t *testing.T) {
	db := sampleTriples(t)
	_, rows := answer(t, db, samplePrefixes+`
SELECT ?name (STRLEN(?name) AS ?len) (SUBSTR(?name, 1, 2) AS ?start) (CONCAT(?name, "!") AS ?bang) (REPLACE(?name, "(l+)", "[$1]") AS ?brackets) WHERE {
  ?s schema:name ?name .
  FILTER(REGEX(?name, "^A", "i") && STRSTARTS(?name, "al") && STRENDS(?name, "ce") && CONTAINS(?name, "lic"))
}`)
	require.Equal(t, [][]string{{"alice", "5", "al", "alice!", "a[l]ice"}}, rows)

	_, rows = answer(t, db, samplePrefixes+`
SELECT (STRBEFORE(?name, "o") AS ?before) (STRAFTER(?name, "o") AS ?after) (LCASE(STR(?s)) AS ?iri) WHERE {
  ?s schema:name ?name .
  FILTER(?name = "bob")
}`)
	require.Equal(t, [][]string{{"b", "b", "http://b"}}, rows)
}

func TestTermTestsAndCastsReadTheTypedColumns(t *testing.T) {
	db := sampleTriples(t)
	_, rows := answer(t, db, samplePrefixes+`
SELECT ?p (COUNT(*) AS ?n) WHERE {
  <http://a> ?p ?o .
  FILTER(isIRI(?o) || (isLiteral(?o) && isNumeric(?o) && xsd:integer(?o) + 1 = 31))
}
GROUP BY ?p ORDER BY ?p`)
	require.Equal(t, [][]string{{"https://schema.org/age", "1"}, {"https://schema.org/knows", "2"}}, rows)

	_, rows = answer(t, db, samplePrefixes+`
SELECT ?s (YEAR(?born) AS ?year) WHERE {
  ?s schema:birthDate ?born .
  FILTER(?born < "2000-01-01T00:00:00Z"^^xsd:dateTime && DATATYPE(?born) = xsd:dateTime)
}`)
	require.Equal(t, [][]string{{"http://c", "1990"}}, rows)
}

func TestCoalesceAndIfProjectAFallback(t *testing.T) {
	_, rows := answer(t, sampleTriples(t), samplePrefixes+`
SELECT ?s (COALESCE(?name, STR(?s)) AS ?display) (IF(BOUND(?name), "named", "anonymous") AS ?kind) WHERE {
  <http://a> schema:knows ?s .
  OPTIONAL { ?s schema:name ?name }
}
ORDER BY ?s`)

	require.Equal(t, [][]string{{"http://b", "bob", "named"}, {"http://c", "http://c", "anonymous"}}, rows)
}

func TestStrRendersAGeometryAsWKT(t *testing.T) {
	_, rows := answer(t, sampleTriples(t), samplePrefixes+`
SELECT (STR(?g) AS ?wkt) WHERE { ?s schema:geo ?g }`)

	require.Equal(t, [][]string{{"POINT (1 2)"}}, rows)
}

func TestAskAnswersTrueOrFalse(t *testing.T) {
	db := sampleTriples(t)
	header, rows := answer(t, db, samplePrefixes+`ASK { ?s schema:name "bob" }`)
	require.Equal(t, []string{"ask"}, header)
	require.Equal(t, [][]string{{"true"}}, rows)

	_, rows = answer(t, db, samplePrefixes+`ASK { ?s schema:name "nobody" }`)
	require.Equal(t, [][]string{{"false"}}, rows)
}

func TestSubqueryJoinsItsProjectionToTheEnclosingGroup(t *testing.T) {
	_, rows := answer(t, sampleTriples(t), samplePrefixes+`
SELECT ?name ?known WHERE {
  ?s schema:name ?name .
  {
    SELECT ?s (COUNT(?o) AS ?known) WHERE { ?s schema:knows ?o } GROUP BY ?s
  }
}`)

	require.Equal(t, [][]string{{"alice", "2"}}, rows)
}

func TestMinusAndOptionalInsideAnExistsGroup(t *testing.T) {
	// everyone with a name but no age over 10
	_, rows := answer(t, sampleTriples(t), samplePrefixes+`
SELECT ?s WHERE {
  ?s schema:name ?name .
  MINUS { ?s schema:age ?age FILTER(?age > 10) }
}`)

	require.Equal(t, [][]string{{"http://b"}}, rows)
}

func TestProjectedGeometryFunctionRendersAsWKT(t *testing.T) {
	sql, err := ToSQL(`
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>
PREFIX schema: <https://schema.org/>
SELECT (geof:envelope(?g) AS ?box) WHERE { ?s schema:geo ?g }`)

	require.NoError(t, err)
	require.Contains(t, sql, `SELECT ST_AsText(ST_Envelope(t0.object_geometry)) AS "box"`)
}

func TestDistinctWithOrderByOffsetAndLimit(t *testing.T) {
	sql, err := ToSQL(samplePrefixes + `SELECT DISTINCT ?s WHERE { ?s ?p ?o } ORDER BY DESC(?s) LIMIT 2 OFFSET 1`)

	require.NoError(t, err)
	require.Equal(t, `SELECT DISTINCT t0.subject AS "s"
FROM triples AS t0
ORDER BY t0.subject DESC
LIMIT 2
OFFSET 1`, sql)
}

func TestExprRejectsAnUnboundVariableAndAnUnknownFunction(t *testing.T) {
	_, err := ToSQL(samplePrefixes + `SELECT ?s WHERE { ?s ?p ?o BIND(STRLEN(?missing) AS ?n) }`)
	require.ErrorContains(t, err, "FILTER variable ?missing is not bound")

	_, err = ToSQL(samplePrefixes + `SELECT ?s WHERE { ?s ?p ?o FILTER(REGEX(?o, "a", "x")) }`)
	require.ErrorContains(t, err, `the REGEX flag "x" is not supported`)
}
