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
