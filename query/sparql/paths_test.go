package sparql

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

const pathPrefixes = `
PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
PREFIX schema: <https://schema.org/>
PREFIX ex: <https://example.test/>
`

// pathTable is a triples table shaped like the view, without the geometry
// column so that nothing here needs the spatial extension. It holds the class
// hierarchy schema.org states, as a project that imports schema.org with
// owl:imports would, beside the project's own rows: a subclass of its own, a
// cycle, a list, and one instance of each class.
const pathTable = `CREATE TABLE triples AS
SELECT * FROM (VALUES
	('https://schema.org/Organization', 'http://www.w3.org/2000/01/rdf-schema#subClassOf', 'https://schema.org/Thing', NULL, 'h1'),
	('https://schema.org/NGO', 'http://www.w3.org/2000/01/rdf-schema#subClassOf', 'https://schema.org/Organization', NULL, 'h2'),
	('https://schema.org/Corporation', 'http://www.w3.org/2000/01/rdf-schema#subClassOf', 'https://schema.org/Organization', NULL, 'h3'),
	('https://example.test/Charity', 'http://www.w3.org/2000/01/rdf-schema#subClassOf', 'https://schema.org/NGO', NULL, 'h4'),
	('https://example.test/X', 'http://www.w3.org/2000/01/rdf-schema#subClassOf', 'https://example.test/Y', NULL, 'h5'),
	('https://example.test/Y', 'http://www.w3.org/2000/01/rdf-schema#subClassOf', 'https://example.test/X', NULL, 'h6'),
	('https://example.test/org', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type', 'https://schema.org/Organization', NULL, 'h7'),
	('https://example.test/ngo', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type', 'https://schema.org/NGO', NULL, 'h8'),
	('https://example.test/corp', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type', 'https://schema.org/Corporation', NULL, 'h9'),
	('https://example.test/charity', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type', 'https://example.test/Charity', NULL, 'h10'),
	('https://example.test/thing', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type', 'https://schema.org/Thing', NULL, 'h11'),
	('https://example.test/x', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type', 'https://example.test/X', NULL, 'h12'),
	('https://example.test/org', 'https://schema.org/name', NULL, 'Org Inc', 'h13'),
	('https://example.test/org', 'http://www.w3.org/2000/01/rdf-schema#label', NULL, 'The Org', 'h14'),
	('https://example.test/list', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#first', NULL, 'a', 'h15'),
	('https://example.test/list', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#rest', NULL, '_:b1', 'h16'),
	('_:b1', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#first', NULL, 'b', 'h17'),
	('_:b1', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#rest', NULL, '_:b2', 'h18'),
	('_:b2', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#first', NULL, 'c', 'h19'),
	('_:b2', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#rest', 'http://www.w3.org/1999/02/22-rdf-syntax-ns#nil', NULL, 'h20')
) AS rows(subject, predicate, object_iri, object_string, triple_hash),
(SELECT NULL::DOUBLE AS object_float, NULL::BIGINT AS object_integer, NULL::INTEGER AS object_byte,
	NULL::TIMESTAMP AS object_time, NULL::VARCHAR AS object_language, NULL::VARCHAR AS object_type)`

func pathDB(t *testing.T) *sql.DB {
	t.Helper()
	db := localDB(t)
	_, err := db.ExecContext(context.Background(), pathTable)
	require.NoError(t, err)
	return db
}

// firstColumn runs a SPARQL query through the same wrapper queries run under
// and returns its first column sorted.
func firstColumn(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	sql, err := toSQL(pathPrefixes+query, tableSources{})
	require.NoError(t, err)
	_, rows, err := queryRows(context.Background(), db, sql)
	require.NoError(t, err, sql)
	var values []string
	for _, row := range rows {
		values = append(values, row[0])
	}
	sort.Strings(values)
	return values
}

func TestPathZeroOrMoreFindsDirectAndInheritedInstances(t *testing.T) {
	db := pathDB(t)
	const query = `SELECT ?x WHERE { ?x rdf:type/rdfs:subClassOf* schema:Organization }`

	require.Equal(t, []string{
		"https://example.test/charity",
		"https://example.test/corp",
		"https://example.test/ngo",
		"https://example.test/org",
	}, firstColumn(t, db, query))
}

func TestPathOneOrMoreExcludesTheIdentity(t *testing.T) {
	db := pathDB(t)

	require.Equal(t, []string{
		"https://example.test/charity",
		"https://example.test/corp",
		"https://example.test/ngo",
	}, firstColumn(t, db, `SELECT ?x WHERE { ?x rdf:type/rdfs:subClassOf+ schema:Organization }`))
}

func TestPathZeroOrOneStopsAfterOneStep(t *testing.T) {
	db := pathDB(t)

	require.Equal(t, []string{
		"https://example.test/corp",
		"https://example.test/ngo",
		"https://example.test/org",
	}, firstColumn(t, db, `SELECT ?x WHERE { ?x rdf:type/rdfs:subClassOf? schema:Organization }`))
}

func TestPathClosureTerminatesOnACycle(t *testing.T) {
	db := pathDB(t)

	require.Equal(t, []string{"https://example.test/x"}, firstColumn(t, db, `SELECT ?x WHERE { ?x rdf:type/rdfs:subClassOf* ex:Y }`))
}

func TestPathInverseClosureWalksDownward(t *testing.T) {
	db := pathDB(t)

	require.Equal(t, []string{
		"https://example.test/Charity",
		"https://schema.org/Corporation",
		"https://schema.org/NGO",
	}, firstColumn(t, db, `SELECT ?sub WHERE { schema:Organization (^rdfs:subClassOf)+ ?sub }`))
}

func TestPathSequenceThroughBlankNodesFollowsRdfRest(t *testing.T) {
	db := pathDB(t)

	require.Equal(t, []string{"_:b1"}, firstColumn(t, db, `SELECT ?node WHERE { ex:list rdf:rest* ?node . ?node rdf:first "b" }`))
	require.Equal(t, []string{"_:b1", "_:b2", "https://example.test/list"}, firstColumn(t, db, `SELECT ?node WHERE { ex:list rdf:rest* ?node . ?node rdf:first ?item }`))
}

func TestPathAlternativeMatchesEitherPredicate(t *testing.T) {
	db := pathDB(t)

	require.Equal(t, []string{"https://example.test/org", "https://example.test/org"}, firstColumn(t, db, `SELECT ?s WHERE { ?s schema:name|rdfs:label ?o }`))
	// every subject with a predicate other than these two, that also has a name
	require.Equal(t, []string{"https://example.test/org"}, firstColumn(t, db, `SELECT ?s WHERE { ?s !(rdf:type|rdfs:label) ?o . ?s schema:name ?n }`))
}

// The identity of a repeated step bound by the enclosing query is read through
// a LATERAL derived table inside the EXISTS subquery.
func TestPathClosureRunsInsideAnExistsSubquery(t *testing.T) {
	db := pathDB(t)

	require.Equal(t, []string{"https://example.test/thing", "https://example.test/x"}, firstColumn(t, db, `SELECT ?x WHERE { ?x rdf:type ?t . FILTER NOT EXISTS { ?t rdfs:subClassOf* schema:Organization } }`))
}

// Every statement in the table is walkable, the imported hierarchy and the
// project's own alike: a class only the imported ontology names is reached
// through the path, and asking for the hierarchy itself returns all of it.
func TestPathReachesTheClassesAnImportedHierarchyNames(t *testing.T) {
	db := pathDB(t)

	require.Equal(t, []string{
		"https://example.test/X",
		"https://example.test/Y",
		"https://schema.org/NGO",
		"https://schema.org/Organization",
		"https://schema.org/Thing",
	}, firstColumn(t, db, `SELECT DISTINCT ?class WHERE { ?s rdf:type/rdfs:subClassOf+ ?class }`))
	require.Len(t, firstColumn(t, db, `SELECT ?sub WHERE { ?sub rdfs:subClassOf ?super }`), 6)
}

func TestToSQLTranslatesASequencePathThroughAHiddenVariable(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT * WHERE { ?x rdf:type/rdfs:label ?l }`)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM triples AS t0\nCROSS JOIN triples AS t1")
	require.Contains(t, sql, "t0.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'")
	require.Contains(t, sql, bindingExpr("t0", "object")+" = t1.subject")
	require.Contains(t, sql, "t1.predicate = 'http://www.w3.org/2000/01/rdf-schema#label'")
	// the variable the steps share is never projected
	require.Contains(t, sql, `SELECT t0.subject AS "x", `+objectTextExpr("t1")+` AS "l"`)
	require.NotContains(t, sql, "__sal_path")
}

func TestToSQLTranslatesAnInversePathBySwappingColumns(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?o WHERE { ?o ^schema:knows ex:alice }`)

	require.NoError(t, err)
	require.Contains(t, sql, "t0.predicate = 'https://schema.org/knows'")
	require.Contains(t, sql, "t0.subject = 'https://example.test/alice'")
	require.Contains(t, sql, objectTextExpr("t0")+` AS "o"`)
}

func TestToSQLTranslatesAnAlternativePathAsPredicateIn(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?s WHERE { ?s schema:name|rdfs:label ?o }`)

	require.NoError(t, err)
	require.Contains(t, sql, "t0.predicate IN ('https://schema.org/name', 'http://www.w3.org/2000/01/rdf-schema#label')")
}

func TestToSQLTranslatesAnInverseAlternativePath(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?s WHERE { ?s ^(schema:knows|schema:parent) ex:alice }`)

	require.NoError(t, err)
	require.Contains(t, sql, "t0.predicate IN ('https://schema.org/knows', 'https://schema.org/parent')")
	require.Contains(t, sql, "t0.subject = 'https://example.test/alice'")
}

func TestToSQLTranslatesANegatedPathAsPredicateNotIn(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?s WHERE { ?s !(rdf:type|rdfs:label) ?o }`)

	require.NoError(t, err)
	require.Contains(t, sql, "t0.predicate NOT IN ('http://www.w3.org/1999/02/22-rdf-syntax-ns#type', 'http://www.w3.org/2000/01/rdf-schema#label')")
}

func TestToSQLTranslatesZeroOrMoreAsARecursiveClosureWithIdentity(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?x WHERE { ?x rdf:type/rdfs:subClassOf* schema:Organization }`)

	require.NoError(t, err)
	require.Equal(t, `SELECT t0.subject AS "x"
FROM triples AS t0
CROSS JOIN LATERAL (WITH RECURSIVE edges AS (
    SELECT edge.subject AS start, `+bindingExpr("edge", "object")+` AS finish
    FROM triples AS edge
    WHERE edge.predicate = 'http://www.w3.org/2000/01/rdf-schema#subClassOf'),
  closure(start, finish) AS (
    SELECT start, finish FROM edges
    UNION
    SELECT closure.start, edges.finish FROM closure JOIN edges ON edges.start = closure.finish)
  SELECT start, finish FROM closure
  UNION ALL SELECT `+bindingExpr("t0", "object")+`, `+bindingExpr("t0", "object")+`) AS t1
WHERE t0.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'
  AND `+bindingExpr("t0", "object")+` = t1.start
  AND t1.finish = 'https://schema.org/Organization'`, sql)
}

func TestToSQLTranslatesOneOrMoreWithoutIdentity(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?x WHERE { ?x rdf:type/rdfs:subClassOf+ schema:Organization }`)

	require.NoError(t, err)
	require.Contains(t, sql, "WITH RECURSIVE edges AS")
	require.NotContains(t, sql, "UNION ALL")
	// the identity is what would have needed the enclosing scan
	require.NotContains(t, sql, "LATERAL")
}

func TestToSQLTranslatesZeroOrOneWithoutRecursion(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?x WHERE { ?x rdf:type/rdfs:subClassOf? schema:Organization }`)

	require.NoError(t, err)
	require.NotContains(t, sql, "RECURSIVE")
	require.Contains(t, sql, "LATERAL (WITH edges AS (")
	require.Contains(t, sql, "SELECT start, finish FROM edges\n  UNION ALL SELECT "+bindingExpr("t0", "object")+", "+bindingExpr("t0", "object")+")")
}

func TestToSQLTranslatesAnInverseClosureWithSwappedEdges(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?sub WHERE { schema:Organization (^rdfs:subClassOf)+ ?sub }`)

	require.NoError(t, err)
	require.Contains(t, sql, "SELECT "+bindingExpr("edge", "object")+" AS start, edge.subject AS finish")
	require.Contains(t, sql, "t0.start = 'https://schema.org/Organization'")
	require.Contains(t, sql, `t0.finish AS "sub"`)
}

func TestToSQLUsesTheEdgeNodesAsIdentityWhenNothingIsBound(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?a ?b WHERE { ?a rdfs:subClassOf* ?b }`)

	require.NoError(t, err)
	require.NotContains(t, sql, "LATERAL")
	require.Contains(t, sql, "UNION ALL SELECT node, node FROM (SELECT start AS node FROM edges UNION SELECT finish FROM edges)")
	require.Contains(t, sql, `SELECT t0.start AS "a", t0.finish AS "b"`)
}

func TestToSQLUsesAConstantAsIdentityWithoutLateral(t *testing.T) {
	sql, err := ToSQL(pathPrefixes + `SELECT ?b WHERE { schema:NGO rdfs:subClassOf* ?b }`)

	require.NoError(t, err)
	require.NotContains(t, sql, "LATERAL")
	require.Contains(t, sql, "UNION ALL SELECT 'https://schema.org/NGO', 'https://schema.org/NGO'")
	require.Contains(t, sql, "t0.start = 'https://schema.org/NGO'")
}

func TestToSQLScansTheSnapshotInsideAClosure(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples", SnapshotID: 122}.Translate(pathPrefixes + `SELECT ?b WHERE { schema:NGO rdfs:subClassOf* ?b }`)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 122) AS edge\n    WHERE edge.predicate")
}

// Every pattern reads the same rows whatever its predicate: the schema
// predicates are not routed to a wider source than the rest of the query.
func TestTranslateReadsEveryPatternFromTheSameSource(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(pathPrefixes + `
SELECT ?s WHERE { ?s rdf:type ?t . ?t rdfs:subClassOf ?c . SERVICE <http://localhost:8080/v7/sparql> { ?s rdfs:subPropertyOf ?o } }`)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM triples AS t0")
	require.Contains(t, sql, "CROSS JOIN triples AS t1")
	require.Contains(t, sql, "snapshot_from_id = 7) AS t2")
	require.NotContains(t, sql, "triples_all")
}

func TestToSQLReadsAPathInsideAMINUSFromTheSameSource(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate(pathPrefixes + `
SELECT ?x WHERE { ?x rdf:type ?t . MINUS { SERVICE <http://localhost:8080/v7/sparql> { ?t rdfs:subClassOf+ schema:Thing } } }`)

	require.NoError(t, err)
	require.Contains(t, sql, "NOT EXISTS (SELECT 1\n  FROM (WITH RECURSIVE edges AS (")
	require.Contains(t, sql, "snapshot_from_id = 7) AS edge")
	require.Contains(t, sql, "x0_0.start = "+bindingExpr("t0", "object"))
}

func TestToSQLRejectsANegatedInversePath(t *testing.T) {
	_, err := ToSQL(pathPrefixes + `SELECT ?s WHERE { ?s !^rdf:type ?o }`)

	require.ErrorContains(t, err, "a negated inverse path, !^p, is not supported yet")
}

func TestToSQLRejectsARepeatedSequencePath(t *testing.T) {
	_, err := ToSQL(pathPrefixes + `SELECT ?s WHERE { ?s (rdf:type/rdfs:subClassOf)* ?o }`)

	require.ErrorContains(t, err, "only a single IRI, its inverse, or an alternative of IRIs can be repeated with *, + or ?")
}

func TestToSQLRejectsAMixedAlternativePath(t *testing.T) {
	_, err := ToSQL(pathPrefixes + `SELECT ?s WHERE { ?s schema:knows|^schema:knows ?o }`)

	require.ErrorContains(t, err, "only an alternative of plain IRIs")
}
