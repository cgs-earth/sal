package sparql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const compareSnapshotsQuery = `
PREFIX schema: <https://schema.org/>

SELECT ?s ?before ?after
WHERE {
  ?s schema:name ?after .
  SERVICE <http://localhost:8080/v122/sparql> {
    ?s schema:name ?before .
  }
  FILTER(?before != ?after)
}`

func TestTranslateJoinsTheCurrentTableToASnapshotNamedBySERVICE(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(compareSnapshotsQuery)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM triples AS t0")
	require.Contains(t, sql, "CROSS JOIN iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 122) AS t1")
	require.Contains(t, sql, "t0.subject = t1.subject")
	require.Contains(t, sql, "t1.predicate = 'https://schema.org/name'")
	// The two objects are compared as the text each renders to, whichever
	// column holds them, not only as object_string.
	require.Contains(t, sql, bindingExpr("t1", "object")+" != "+bindingExpr("t0", "object"))
}

func TestTranslateReadsASERVICEOnTheUnversionedEndpointAsTheCurrentTable(t *testing.T) {
	// A query sent to /v122/sparql compares the table at that snapshot against
	// the current one by naming the plain endpoint.
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples", SnapshotID: 122}.Translate(`
SELECT ?s ?o
WHERE {
  ?s <https://schema.org/name> ?o .
  SERVICE <http://localhost:8080/sparql> { ?s <https://schema.org/name> ?o }
}`)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 122) AS t0")
	require.Contains(t, sql, "CROSS JOIN triples AS t1")
}

func TestTranslateAcceptsAQueryMadeOnlyOfASERVICE(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(`
SELECT ?name
WHERE {
  SERVICE SILENT <https://data.example.org/v7/sparql> {
    ?s <https://schema.org/name> ?name .
    FILTER(?name != "bob")
  }
}`)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 7) AS t0")
	require.NotContains(t, sql, "triples AS")
	require.Contains(t, sql, "t0.object_string != 'bob'")
}

func TestTranslateRejectsASERVICEThatIsNotAnEndpointOfThisServer(t *testing.T) {
	_, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(`
SELECT ?s WHERE { SERVICE <https://query.wikidata.org/sparql/> { ?s ?p ?o } }`)

	require.ErrorContains(t, err, "not a SPARQL endpoint of this server")
	require.ErrorContains(t, err, "/v<snapshot id>/sparql")
}

func TestTranslateRejectsASERVICEWithoutASnapshotID(t *testing.T) {
	_, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(`
SELECT ?s WHERE { SERVICE <http://localhost:8080/v0/sparql> { ?s ?p ?o } }`)

	require.ErrorContains(t, err, "does not name a snapshot ID")
}

func TestTranslateRejectsASERVICENamedByAPrefixedNameOrVariable(t *testing.T) {
	_, err := ToSQL(`
PREFIX ex: <http://localhost:8080/>
SELECT ?s WHERE { SERVICE ex:sparql { ?s ?p ?o } }`)
	require.ErrorContains(t, err, "SERVICE must name its endpoint as a full IRI")

	_, err = ToSQL(`SELECT ?s WHERE { SERVICE ?endpoint { ?s ?p ?o } }`)
	require.ErrorContains(t, err, "SERVICE must name its endpoint as a full IRI")
}

func TestTranslateStillRejectsAGRAPHClause(t *testing.T) {
	_, err := ToSQL(`SELECT ?s WHERE { GRAPH <http://localhost:8080/v1/sparql> { ?s ?p ?o } }`)

	require.ErrorContains(t, err, "only basic SPARQL triple patterns and FILTER expressions are supported yet")
}

func TestRewriteServiceLeavesATermCalledServiceAlone(t *testing.T) {
	// A variable or prefixed name called service in subject position is
	// followed by a predicate IRI, exactly the shape the keyword has.
	for _, query := range []string{
		"SELECT ?service WHERE { ?service <https://schema.org/name> ?o }",
		"SELECT ?o WHERE { $Service <https://schema.org/name> ?o }",
		"SELECT ?o WHERE { ex:service <https://schema.org/name> ?o }",
	} {
		require.Equal(t, query, rewriteService(query))
	}
}

func TestRewriteServiceMarksTheEndpointIRI(t *testing.T) {
	require.Equal(t,
		"SELECT ?s WHERE {\n  GRAPH <urn:x-sal-service:http://localhost:8080/v12/sparql> { ?s ?p ?o }\n}",
		rewriteService("SELECT ?s WHERE {\n  service silent <http://localhost:8080/v12/sparql> { ?s ?p ?o }\n}"))
}

func TestToSQLComparesTwoObjectVariablesAsText(t *testing.T) {
	sql, err := ToSQL(`
SELECT ?s
WHERE {
  ?s <https://schema.org/a> ?x .
  ?s <https://schema.org/b> ?y .
  FILTER(?x = ?y)
}`)

	require.NoError(t, err)
	require.Contains(t, sql, bindingExpr("t0", "object")+" = "+bindingExpr("t1", "object"))
}
