package sparql

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// localDB opens a DuckDB database with nothing loaded beyond the extensions the
// library is linked against, so these tests never reach extensions.duckdb.org.
func localDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})
	return db
}

func TestNeedsSpatialDetectsSTFunctions(t *testing.T) {
	require.True(t, needsSpatial(geometrySQL(GeometryQuery{Limit: 10})))
	require.True(t, needsSpatial("SELECT ST_AsText(geom) FROM shapes"))
}

func TestNeedsSpatialSkipsTheCountsQuery(t *testing.T) {
	// The counts query names the object columns individually, so COUNT(*) is the
	// only star in it and nothing projects the geometry column.
	require.False(t, needsSpatial(countsSQL()))
	require.False(t, needsSpatial("SELECT subject FROM triples LIMIT 20"))
}

func TestNeedsSpatialCoversStarProjections(t *testing.T) {
	// DuckDB reads object_geometry as GEOMETRY and cannot cast it without spatial,
	// so a bare star has to load the extension even with no ST_ call in sight.
	require.True(t, needsSpatial("SELECT * FROM triples LIMIT 20"))
	require.True(t, needsSpatial("SELECT object_geometry FROM triples"))
}

func TestMissingSpatialExtensionRecognizesBothDuckDBReports(t *testing.T) {
	require.True(t, missingSpatialExtension(fmt.Errorf(`Catalog Error: Scalar Function with name "st_point" is not in the catalog, but it exists in the spatial extension`)))
	require.True(t, missingSpatialExtension(fmt.Errorf("Conversion Error: Unimplemented type for cast (BLOB -> GEOMETRY)")))
	require.False(t, missingSpatialExtension(fmt.Errorf("Parser Error: syntax error at or near SELCT")))
}

func TestViewSQLScansTheTablePath(t *testing.T) {
	require.Contains(t, viewSQL("/tmp/table"), "iceberg_scan('/tmp/table', allow_moved_paths = true)")
}

func TestViewSQLEscapesSingleQuotesInTheTablePath(t *testing.T) {
	require.Contains(t, viewSQL("/tmp/o'brien/triples"), "iceberg_scan('/tmp/o''brien/triples'")
}

func TestTextQueryStripsTheTrailingSemicolon(t *testing.T) {
	// A trailing semicolon would close the statement inside the wrapper.
	require.Equal(t, "SELECT COLUMNS(*)::VARCHAR FROM (SELECT 1)", textQuery("  SELECT 1;  "))
}

func TestQueryRowsKeepsTheColumnNamesOfTheStatement(t *testing.T) {
	header, rows, err := queryRows(context.Background(), localDB(t), "SELECT 1 AS subject, 'a' AS predicate")

	require.NoError(t, err)
	require.Equal(t, []string{"subject", "predicate"}, header)
	require.Equal(t, [][]string{{"1", "a"}}, rows)
}

func TestQueryRowsReadsNullAsAnEmptyString(t *testing.T) {
	_, rows, err := queryRows(context.Background(), localDB(t), "SELECT NULL AS object")

	require.NoError(t, err)
	require.Equal(t, [][]string{{""}}, rows)
}

func TestQueryRowsRendersNonScalarColumnsAsDuckDBText(t *testing.T) {
	// These are the types a triples table and the Iceberg metadata functions
	// produce that have no Go value formatting the same way DuckDB does.
	_, rows, err := queryRows(context.Background(), localDB(t),
		"SELECT [1, 2] AS list, {'x': 1} AS struct, 1.5::DECIMAL(4,2) AS decimal, TIMESTAMP '2024-01-02 03:04:05' AS ts")

	require.NoError(t, err)
	require.Equal(t, [][]string{{"[1, 2]", "{'x': 1}", "1.50", "2024-01-02 03:04:05"}}, rows)
}

func TestQueryRowsKeepsValuesWithCommasAndQuotesIntact(t *testing.T) {
	// The duckdb CLI used to hand these back as CSV, which had to be parsed to
	// recover them. Reading rows through the driver removes that round trip.
	_, rows, err := queryRows(context.Background(), localDB(t), `SELECT 'a,b' AS one, 'say "hi"' AS two`)

	require.NoError(t, err)
	require.Equal(t, [][]string{{"a,b", `say "hi"`}}, rows)
}

func TestQueryRowsPreservesTheOrderingOfTheStatement(t *testing.T) {
	// InfoSQL("snapshots") orders newest first and SnapshotDiffSQL depends on it,
	// so the wrapper textQuery adds must not disturb the inner ORDER BY. The row
	// count is large enough to make DuckDB run the scan in parallel.
	_, rows, err := queryRows(context.Background(), localDB(t), "SELECT i FROM range(200000) t(i) ORDER BY i DESC")

	require.NoError(t, err)
	require.Len(t, rows, 200000)
	require.Equal(t, [][]string{{"199999"}, {"199998"}, {"199997"}}, rows[:3])
}

func TestQueryRowsReportsAStatementThatDoesNotParse(t *testing.T) {
	_, _, err := queryRows(context.Background(), localDB(t), "SELCT 1")

	require.ErrorContains(t, err, "duckdb query failed")
}

// TestObjectExpressionsRenderTimestampsAsXSDDateTime runs the object COALESCE
// expressions against DuckDB itself, checking they are valid core SQL (no
// extensions) and that a stored UTC timestamp renders back as the xsd:dateTime
// lexical form build parsed it from, fractional seconds included.
func TestObjectExpressionsRenderTimestampsAsXSDDateTime(t *testing.T) {
	db := localDB(t)
	_, err := db.ExecContext(context.Background(), `CREATE TABLE triples AS SELECT
		NULL::VARCHAR AS object_iri,
		NULL::DOUBLE AS object_float,
		NULL::BIGINT AS object_integer,
		NULL::INTEGER AS object_byte,
		TIMESTAMP '2002-05-30 15:30:10' AS object_time,
		NULL::VARCHAR AS object_string`)
	require.NoError(t, err)

	var rendered string
	require.NoError(t, db.QueryRow("SELECT "+bindingExpr("triples", "object")+" FROM triples").Scan(&rendered))
	require.Equal(t, "2002-05-30T15:30:10Z", rendered)

	_, err = db.ExecContext(context.Background(), "UPDATE triples SET object_time = TIMESTAMP '2002-05-30 15:30:10.123456'")
	require.NoError(t, err)
	require.NoError(t, db.QueryRow("SELECT "+bindingExpr("triples", "object")+" FROM triples").Scan(&rendered))
	require.Equal(t, "2002-05-30T15:30:10.123456Z", rendered)
}

// TestObjectNumericExprReadsEveryNumericColumn checks the numeric COALESCE a
// FILTER compares through reads a value whichever numeric column holds it.
func TestObjectNumericExprReadsEveryNumericColumn(t *testing.T) {
	db := localDB(t)
	_, err := db.ExecContext(context.Background(), `CREATE TABLE triples AS
		SELECT 42.5::DOUBLE AS object_float, NULL::BIGINT AS object_integer, NULL::INTEGER AS object_byte
		UNION ALL SELECT NULL, 7, NULL
		UNION ALL SELECT NULL, NULL, -8`)
	require.NoError(t, err)

	rows, err := db.QueryContext(context.Background(), "SELECT "+objectNumericExpr("triples")+" AS n FROM triples ORDER BY n")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var values []float64
	for rows.Next() {
		var value float64
		require.NoError(t, rows.Scan(&value))
		values = append(values, value)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []float64{-8, 7, 42.5}, values)
}
