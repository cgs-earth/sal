package sparql

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// constructRows runs the SQL a CONSTRUCT translates to against an in-memory
// stand-in for the triples table holding the given rows, and returns the
// triples it builds as subject, predicate, and the object column that is set.
func constructRows(t *testing.T, query string, inserts ...string) [][]string {
	t.Helper()
	statement, err := ToSQL(query)
	require.NoError(t, err)
	// the geometry column needs the spatial extension, which a unit test cannot
	// download, so it is read as the NULL every row here holds
	statement = strings.ReplaceAll(statement, "ST_AsText(t0.object_geometry)", "CAST(NULL AS VARCHAR)")
	statement = strings.ReplaceAll(statement, "ST_AsText(t1.object_geometry)", "CAST(NULL AS VARCHAR)")

	db := localDB(t)
	_, err = db.Exec(`CREATE TABLE triples (subject VARCHAR, predicate VARCHAR, object_string VARCHAR, object_iri VARCHAR,
		object_geometry VARCHAR, object_byte INTEGER, object_integer BIGINT, object_float DOUBLE, object_time TIMESTAMP,
		object_type VARCHAR, object_language VARCHAR, triple_hash VARCHAR)`)
	require.NoError(t, err)
	for _, insert := range inserts {
		_, err = db.Exec("INSERT INTO triples (subject, predicate, object_string, object_iri, object_integer, object_type, object_language) VALUES " + insert)
		require.NoError(t, err)
	}

	var rows [][]string
	err = streamRows(context.Background(), db, statement+"\nORDER BY ALL", func(row []sql.NullString) error {
		built := []string{row[0].String, row[1].String}
		for i, value := range row[2:] {
			if value.Valid {
				built = append(built, ConstructColumns[i+2]+"="+value.String)
			}
		}
		rows = append(rows, built)
		return nil
	})
	require.NoError(t, err)
	return rows
}

func TestToSQLTranslatesConstructToTripleColumns(t *testing.T) {
	statement, err := ToSQL(`
PREFIX schema: <https://schema.org/>
CONSTRUCT { ?s schema:alternateName ?name }
WHERE { ?s schema:name ?name }`)

	require.NoError(t, err)
	require.True(t, strings.HasPrefix(statement, "WITH solutions AS (\n  SELECT DISTINCT t0.subject AS \"s\", t0.object_iri AS \"name.object_iri\""))
	require.Contains(t, statement, "FROM triples AS t0\n  WHERE t0.predicate = 'https://schema.org/name')")
	require.Contains(t, statement, "SELECT DISTINCT solutions.\"s\" AS subject, 'https://schema.org/alternateName' AS predicate, solutions.\"name.object_iri\" AS object_iri")
	for _, column := range ConstructColumns {
		require.Contains(t, statement, " AS "+column)
	}
}

func TestConstructCopiesAnObjectWithItsDatatypeAndLanguage(t *testing.T) {
	rows := constructRows(t, `
PREFIX schema: <https://schema.org/>
CONSTRUCT { ?s schema:alternateName ?name }
WHERE { ?s schema:name ?name }`,
		`('http://example.org/a', 'https://schema.org/name', 'Ada', NULL, NULL, 'http://www.w3.org/1999/02/22-rdf-syntax-ns#langString', 'en')`,
		`('http://example.org/b', 'https://schema.org/name', NULL, NULL, 42, 'http://www.w3.org/2001/XMLSchema#integer', NULL)`,
		`('http://example.org/b', 'https://schema.org/age', NULL, NULL, 7, 'http://www.w3.org/2001/XMLSchema#integer', NULL)`)

	require.Equal(t, [][]string{
		{"http://example.org/a", "https://schema.org/alternateName", "object_string=Ada", "object_type=http://www.w3.org/1999/02/22-rdf-syntax-ns#langString", "object_language=en"},
		{"http://example.org/b", "https://schema.org/alternateName", "object_integer=42", "object_type=http://www.w3.org/2001/XMLSchema#integer"},
	}, rows)
}

func TestConstructBuildsEachTemplateTripleOncePerGraph(t *testing.T) {
	rows := constructRows(t, `
PREFIX ex: <http://example.org/>
CONSTRUCT { ?child ex:childOf ?parent . ?parent a ex:Parent ; ex:label "parent"@EN ; ex:rank 1 }
WHERE { ?parent ex:parentOf ?child }`,
		`('http://example.org/p', 'http://example.org/parentOf', NULL, 'http://example.org/c1', NULL, NULL, NULL)`,
		`('http://example.org/p', 'http://example.org/parentOf', NULL, 'http://example.org/c2', NULL, NULL, NULL)`)

	require.Equal(t, [][]string{
		{"http://example.org/c1", "http://example.org/childOf", "object_iri=http://example.org/p"},
		{"http://example.org/c2", "http://example.org/childOf", "object_iri=http://example.org/p"},
		{"http://example.org/p", "http://example.org/label", "object_string=parent", "object_type=http://www.w3.org/1999/02/22-rdf-syntax-ns#langString", "object_language=en"},
		{"http://example.org/p", "http://example.org/rank", "object_string=1", "object_type=http://www.w3.org/2001/XMLSchema#integer"},
		{"http://example.org/p", "http://www.w3.org/1999/02/22-rdf-syntax-ns#type", "object_iri=http://example.org/Parent"},
	}, rows)
}

func TestConstructLeavesOutATripleWithALiteralSubject(t *testing.T) {
	rows := constructRows(t, `
PREFIX ex: <http://example.org/>
CONSTRUCT { ?o ex:valueOf ?s }
WHERE { ?s ex:value ?o }`,
		`('http://example.org/a', 'http://example.org/value', 'a literal', NULL, NULL, 'http://www.w3.org/2001/XMLSchema#string', NULL)`,
		`('http://example.org/a', 'http://example.org/value', NULL, 'http://example.org/b', NULL, NULL, NULL)`,
		`('http://example.org/a', 'http://example.org/value', '_:sal_1', NULL, NULL, NULL, NULL)`)

	require.Equal(t, [][]string{
		{"_:sal_1", "http://example.org/valueOf", "object_iri=http://example.org/a"},
		{"http://example.org/b", "http://example.org/valueOf", "object_iri=http://example.org/a"},
	}, rows)
}

func TestConstructBuildsATemplateBlankNodePerSolution(t *testing.T) {
	rows := constructRows(t, `
PREFIX ex: <http://example.org/>
CONSTRUCT { ?s ex:measurement _:m . _:m ex:value ?v }
WHERE { ?s ex:value ?v }`,
		`('http://example.org/a', 'http://example.org/value', NULL, NULL, 1, 'http://www.w3.org/2001/XMLSchema#integer', NULL)`,
		`('http://example.org/b', 'http://example.org/value', NULL, NULL, 2, 'http://www.w3.org/2001/XMLSchema#integer', NULL)`)

	require.Len(t, rows, 4)
	nodes := map[string]string{}
	for _, row := range rows {
		if row[1] == "http://example.org/measurement" {
			nodes[row[0]] = strings.TrimPrefix(row[2], "object_string=")
		}
	}
	require.Len(t, nodes, 2)
	require.True(t, strings.HasPrefix(nodes["http://example.org/a"], "_:construct_m_"))
	require.NotEqual(t, nodes["http://example.org/a"], nodes["http://example.org/b"])
	for _, row := range rows {
		if row[1] == "http://example.org/value" {
			require.Contains(t, []string{nodes["http://example.org/a"], nodes["http://example.org/b"]}, row[0])
		}
	}
}

func TestConstructWhereUsesItsPatternAsTheTemplate(t *testing.T) {
	rows := constructRows(t, `
PREFIX ex: <http://example.org/>
CONSTRUCT WHERE { ?s ex:knows ?o }`,
		`('http://example.org/a', 'http://example.org/knows', NULL, 'http://example.org/b', NULL, NULL, NULL)`,
		`('http://example.org/a', 'http://example.org/likes', NULL, 'http://example.org/c', NULL, NULL, NULL)`)

	require.Equal(t, [][]string{{"http://example.org/a", "http://example.org/knows", "object_iri=http://example.org/b"}}, rows)
}

func TestConstructLimitBoundsTheSolutions(t *testing.T) {
	statement, err := ToSQL(`CONSTRUCT { ?s ?p ?o } WHERE { ?s ?p ?o } LIMIT 5`)

	require.NoError(t, err)
	require.Contains(t, statement, "\n  LIMIT 5)\nSELECT DISTINCT")
}

func TestConstructRejectsATemplateVariableTheWhereClauseDoesNotBind(t *testing.T) {
	_, err := ToSQL(`CONSTRUCT { ?s ?p ?missing } WHERE { ?s ?p ?o }`)

	require.ErrorContains(t, err, "CONSTRUCT template variable ?missing is not bound")
}

func TestConstructRejectsALiteralSubject(t *testing.T) {
	_, err := ToSQL(`CONSTRUCT { "bob" ?p ?o } WHERE { ?s ?p ?o }`)

	require.ErrorContains(t, err, "the subject of a CONSTRUCT template triple must be an IRI")
}

func TestConstructPrefixesTellsAConstructFromASelect(t *testing.T) {
	prefixes, isConstruct, err := ConstructPrefixes(`PREFIX ex: <http://example.org/> CONSTRUCT { ?s ex:p ?o } WHERE { ?s ?p ?o }`)
	require.NoError(t, err)
	require.True(t, isConstruct)
	require.Equal(t, "http://example.org/", prefixes["ex"])

	_, isConstruct, err = ConstructPrefixes(`SELECT * WHERE { ?s ?p ?o }`)
	require.NoError(t, err)
	require.False(t, isConstruct)

	_, _, err = ConstructPrefixes(`CONSTRUCT { ?s ?p`)
	require.ErrorContains(t, err, "parse SPARQL query")
}
