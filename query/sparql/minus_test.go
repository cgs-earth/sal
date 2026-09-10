package sparql

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

const addedSinceSnapshotQuery = `
SELECT ?subject ?predicate ?object
WHERE {
  ?subject ?predicate ?object .
  MINUS {
    SERVICE <http://localhost:8080/v122/sparql> {
      ?subject ?predicate ?object .
    }
  }
}`

const removedSinceSnapshotQuery = `
SELECT ?subject ?predicate ?object
WHERE {
  SERVICE <http://localhost:8080/v122/sparql> {
    ?subject ?predicate ?object .
  }
  MINUS {
    ?subject ?predicate ?object .
  }
}`

func TestToSQLTranslatesMINUSAsANotExistsSubquery(t *testing.T) {
	sql, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . MINUS { ?s <http://q> ?x } }`)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM triples AS t0\nWHERE t0.predicate = 'http://p'\n  AND NOT EXISTS (SELECT 1\n  FROM triples AS x0_0\n  WHERE x0_0.predicate = 'http://q'\n    AND x0_0.subject = t0.subject)")
	require.NotContains(t, sql, "CROSS JOIN", "the subtracted group must not be joined into the query")
}

func TestToSQLCorrelatesAnObjectInMINUSByExactTerm(t *testing.T) {
	sql, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . MINUS { ?s <http://q> ?o } }`)

	require.NoError(t, err)
	// The rendered text alone would conflate "x"@en with "x"@fr and "5" with
	// 5, and would compare two geometries as NULL; the tag and datatype
	// columns are compared beside it, and the text includes the WKT.
	require.Contains(t, sql, objectTextExpr("x0_0")+" IS NOT DISTINCT FROM "+objectTextExpr("t0"))
	require.Contains(t, sql, "x0_0.object_language IS NOT DISTINCT FROM t0.object_language")
	require.Contains(t, sql, "x0_0.object_type IS NOT DISTINCT FROM t0.object_type")
}

func TestToSQLCorrelatesAWholeTripleByItsHash(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(addedSinceSnapshotQuery, false)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM triples AS t0\nWHERE NOT EXISTS (SELECT 1\n  FROM iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 122) AS x0_0\n  WHERE x0_0.vocabulary IS NULL\n    AND x0_0.triple_hash = t0.triple_hash)")
	require.NotContains(t, sql, "x0_0.subject", "a whole triple is compared by hash, not position by position")
}

func TestToSQLDoesNotUseTheHashWhenPositionsAreSwapped(t *testing.T) {
	// ?o ?p ?s is a different triple from ?s ?p ?o, so their hashes must not
	// be compared even though every position correlates to the same scan.
	sql, err := ToSQL(`SELECT ?s WHERE { ?s ?p ?o . MINUS { ?o ?p ?s } }`)

	require.NoError(t, err)
	require.NotContains(t, sql, "triple_hash")
	require.Contains(t, sql, "x0_0.subject = "+bindingExpr("t0", "object"))
}

func TestToSQLTranslatesTheRemovedSinceSnapshotQuery(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(removedSinceSnapshotQuery, false)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 122) AS t0\nWHERE t0.vocabulary IS NULL\n  AND NOT EXISTS (SELECT 1\n  FROM triples AS x0_0\n  WHERE x0_0.triple_hash = t0.triple_hash)")
}

func TestToSQLReadsAMINUSGroupFromTheSERVICEItIsWrittenIn(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(`
SELECT ?s WHERE { SERVICE <http://localhost:8080/v7/sparql> { ?s <http://p> ?o . MINUS { ?s <http://q> ?z } } }`, false)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 7) AS t0")
	require.Contains(t, sql, "FROM iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 7) AS x0_0")
	require.NotContains(t, sql, "triples AS")
}

func TestToSQLReadsAnEXISTSGroupFromTheSERVICEItIsWrittenIn(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(`
SELECT ?s WHERE { SERVICE <http://localhost:8080/v7/sparql> { ?s <http://p> ?o . FILTER(NOT EXISTS { ?s <http://q> ?z } || ?o = "y") } }`, false)

	require.NoError(t, err)
	require.Contains(t, sql, "snapshot_from_id = 7) AS x0_0")
	require.NotContains(t, sql, "triples AS")
}

func TestToSQLDropsAMINUSThatSharesNoVariable(t *testing.T) {
	// SPARQL: a MINUS group compatible with nothing subtracts nothing.
	sql, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . MINUS { ?x <http://q> ?y } }`)

	require.NoError(t, err)
	require.NotContains(t, sql, "EXISTS")
}

func TestToSQLScopesMINUSToTheGroupBeforeIt(t *testing.T) {
	// The parser reads this as Join(Minus(A, B), C): ?x is shared between the
	// MINUS group and C, which comes after it, so it is not a correlation.
	sql, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . MINUS { ?x ?y ?z } ?x <http://q> ?r }`)

	require.NoError(t, err)
	require.NotContains(t, sql, "EXISTS")
	require.Contains(t, sql, "CROSS JOIN triples AS t1")
}

func TestToSQLTranslatesFILTERNOTEXISTS(t *testing.T) {
	sql, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . FILTER NOT EXISTS { ?s <http://q> ?z } }`)

	require.NoError(t, err)
	require.Contains(t, sql, "AND NOT EXISTS (SELECT 1\n  FROM triples AS x0_0\n  WHERE x0_0.predicate = 'http://q'\n    AND x0_0.subject = t0.subject)")
}

func TestToSQLTranslatesEXISTSInEitherSpelling(t *testing.T) {
	positive, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . FILTER EXISTS { ?s <http://q> ?z } }`)
	require.NoError(t, err)
	require.Contains(t, positive, "\n  AND EXISTS (SELECT 1")

	bang, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . FILTER(!EXISTS { ?s <http://q> ?z }) }`)
	require.NoError(t, err)
	require.Contains(t, bang, "\n  AND NOT EXISTS (SELECT 1")

	parenthesized, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . FILTER(!(EXISTS { ?s <http://q> ?z })) }`)
	require.NoError(t, err)
	require.Contains(t, parenthesized, "\n  AND NOT (EXISTS (SELECT 1")
}

func TestToSQLCombinesEXISTSWithOtherFilters(t *testing.T) {
	sql, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . FILTER(NOT EXISTS { ?s <http://q> ?z } || ?o = "y") }`)

	require.NoError(t, err)
	require.Contains(t, sql, "AND (NOT EXISTS (SELECT 1")
	require.Contains(t, sql, ") OR t0.object_string = 'y')")
}

func TestToSQLLetsAnEXISTSFilterReachTheEnclosingQuery(t *testing.T) {
	// EXISTS is evaluated with the enclosing solution substituted in, so its
	// filters may name variables only the enclosing query binds.
	sql, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . FILTER EXISTS { ?s <http://q> ?z . FILTER(?o = "x") } }`)

	require.NoError(t, err)
	require.Contains(t, sql, "AND t0.object_string = 'x')")
}

func TestToSQLKeepsAMINUSFilterToTheGroupItself(t *testing.T) {
	// A MINUS group is evaluated on its own, so a filter inside it cannot name
	// a variable only the enclosing query binds.
	_, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . MINUS { ?s <http://q> ?z . FILTER(?o = "x") } }`)

	require.ErrorContains(t, err, "FILTER variable ?o is not bound")
}

func TestToSQLRejectsAnOuterFilterOnAVariableBoundOnlyInsideAGroup(t *testing.T) {
	_, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . MINUS { ?s <http://q> ?z } FILTER(?z = "x") }`)

	require.ErrorContains(t, err, "FILTER variable ?z is not bound")
}

func TestToSQLDoesNotProjectGroupVariablesWithSelectStar(t *testing.T) {
	sql, err := ToSQL(`SELECT * WHERE { ?s <http://p> ?o . MINUS { ?s <http://q> ?z } }`)

	require.NoError(t, err)
	require.Contains(t, sql, `AS "o"`)
	require.NotContains(t, sql, `AS "z"`)
}

func TestToSQLNestsEXISTSWithAliasesOfTheirOwn(t *testing.T) {
	sql, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . FILTER NOT EXISTS { ?s <http://q> ?z . FILTER NOT EXISTS { ?z <http://r> ?s } } }`)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM triples AS x0_0")
	require.Contains(t, sql, "FROM triples AS x1_0")
	require.Contains(t, sql, "x1_0.subject = "+bindingExpr("x0_0", "object"))
}

func TestToSQLKeepsDISTINCTOnTheOuterSelect(t *testing.T) {
	sql, err := ToSQL(`SELECT DISTINCT ?s WHERE { ?s <http://p> ?o . MINUS { ?s <http://q> ?z } }`)

	require.NoError(t, err)
	require.Contains(t, sql, "SELECT DISTINCT t0.subject")
	require.Contains(t, sql, "SELECT 1\n")
	require.Equal(t, 1, countOf(sql, "DISTINCT t0"))
}

func TestToSQLRejectsAnEmptyMINUSGroup(t *testing.T) {
	_, err := ToSQL(`SELECT ?s WHERE { ?s <http://p> ?o . MINUS { } }`)

	require.ErrorContains(t, err, "MINUS or EXISTS group must include at least one triple pattern")
}

func countOf(s string, sub string) int {
	count := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			count++
		}
	}
	return count
}

// minusTable is a triples table shaped like the view, without the geometry
// column so that nothing here needs the spatial extension.
const minusTable = `CREATE TABLE triples AS
SELECT * FROM (VALUES
	('a', 'http://p', NULL, 'keep', 'h1'),
	('b', 'http://p', NULL, 'gone', 'h2'),
	('b', 'http://q', NULL, 'z', 'h3')
) AS rows(subject, predicate, object_iri, object_string, triple_hash),
(SELECT NULL::DOUBLE AS object_float, NULL::BIGINT AS object_integer, NULL::INTEGER AS object_byte,
	NULL::TIMESTAMP AS object_time, NULL::VARCHAR AS object_language, NULL::VARCHAR AS object_type)`

func subjectsOf(t *testing.T, query string) []string {
	t.Helper()
	db := localDB(t)
	_, err := db.ExecContext(context.Background(), minusTable)
	require.NoError(t, err)
	sql, err := ToSQL(query)
	require.NoError(t, err)
	_, rows, err := queryRows(context.Background(), db, sql)
	require.NoError(t, err)
	var subjects []string
	for _, row := range rows {
		subjects = append(subjects, row[0])
	}
	sort.Strings(subjects)
	return subjects
}

// TestMINUSRunsAsACorrelatedSubqueryInDuckDB executes the translation, since
// the string tests cannot prove DuckDB accepts a correlated NOT EXISTS over the
// same table.
func TestMINUSRunsAsACorrelatedSubqueryInDuckDB(t *testing.T) {
	require.Equal(t, []string{"a"}, subjectsOf(t, `SELECT ?s WHERE { ?s <http://p> ?o . MINUS { ?s <http://q> ?x } }`))
}

func TestMINUSByTripleHashRunsInDuckDB(t *testing.T) {
	// Every triple whose object is "gone" is subtracted by its hash, leaving
	// a's triple and b's other one.
	require.Equal(t, []string{"a", "b"}, subjectsOf(t, `SELECT ?s WHERE { ?s ?p ?o . MINUS { ?s ?p ?o . FILTER(?o = "gone") } }`))
}
