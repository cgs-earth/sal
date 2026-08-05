package sparql

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNeedsSpatialDetectsSTFunctions(t *testing.T) {
	require.True(t, needsSpatial(geometrySQL(SimpleObjects, 10, 0), SimpleObjects))
	require.True(t, needsSpatial("SELECT ST_AsText(geom) FROM shapes", SimpleObjects))
}

func TestNeedsSpatialSkipsTheStatsQueriesOnTheSimpleLayout(t *testing.T) {
	require.False(t, needsSpatial(countsSQL(SimpleObjects), SimpleObjects))
	require.False(t, needsSpatial("SELECT * FROM triples LIMIT 20", SimpleObjects))
}

func TestNeedsSpatialSkipsTheCountsQueryOnTheTypedLayout(t *testing.T) {
	// The counts query names the object columns individually, so COUNT(*) is the
	// only star in it and nothing projects the geometry column.
	require.False(t, needsSpatial(countsSQL(TypedObjects), TypedObjects))
}

func TestNeedsSpatialCoversStarProjectionsOnTheTypedLayout(t *testing.T) {
	// DuckDB reads object_geometry as GEOMETRY and cannot cast it without spatial,
	// so a bare star has to load the extension even with no ST_ call in sight.
	require.True(t, needsSpatial("SELECT * FROM triples LIMIT 20", TypedObjects))
	require.True(t, needsSpatial("SELECT object_geometry FROM triples", TypedObjects))
}

func TestPreambleOmitsSpatialUnlessAsked(t *testing.T) {
	runner := DuckDBRunner{TablePath: "/tmp/table"}

	without := runner.preamble(false)

	require.Contains(t, without, "LOAD iceberg;")
	require.NotContains(t, without, "LOAD spatial;")
	require.Contains(t, without, "iceberg_scan('/tmp/table', allow_moved_paths = true)")
}

func TestPreambleLoadsSpatialWhenAsked(t *testing.T) {
	require.Contains(t, DuckDBRunner{TablePath: "/tmp/table"}.preamble(true), "LOAD spatial;")
}

func TestPreambleEscapesSingleQuotesInTheTablePath(t *testing.T) {
	preamble := DuckDBRunner{TablePath: "/tmp/o'brien/triples"}.preamble(false)

	require.Contains(t, preamble, "iceberg_scan('/tmp/o''brien/triples'")
}

func TestBatchScriptWritesOneCopyPerStatement(t *testing.T) {
	script := batchScript("LOAD iceberg;\n", []string{"SELECT 1", "SELECT 2"}, "/tmp/batch")

	require.Contains(t, script, "LOAD iceberg;")
	require.Contains(t, script, "COPY (SELECT 1) TO '/tmp/batch/0.csv' (HEADER, DELIMITER ',');")
	require.Contains(t, script, "COPY (SELECT 2) TO '/tmp/batch/1.csv' (HEADER, DELIMITER ',');")
	require.Less(t, indexOf(t, script, "0.csv"), indexOf(t, script, "1.csv"))
}

func TestBatchScriptEscapesSingleQuotesInTheOutputDirectory(t *testing.T) {
	script := batchScript("", []string{"SELECT 1"}, "/tmp/o'brien")

	require.Contains(t, script, "TO '/tmp/o''brien/0.csv'")
}

func TestCollectBatchResultsReadsEachStatementsCSV(t *testing.T) {
	dir := t.TempDir()
	writeBatchOutput(t, dir, 0, "count\n7\n")
	writeBatchOutput(t, dir, 1, "key,value\nsal.hash,abc\n")

	results, err := collectBatchResults([]string{"SELECT 1", "SELECT 2"}, dir)

	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "SELECT 1", results[0].SQL)
	require.Equal(t, []string{"count"}, results[0].Header)
	require.Equal(t, [][]string{{"7"}}, results[0].Rows)
	require.Equal(t, "1 rows", results[0].Message)
	require.Equal(t, []string{"key", "value"}, results[1].Header)
	require.Equal(t, [][]string{{"sal.hash", "abc"}}, results[1].Rows)
}

func TestCollectBatchResultsReportsAMissingOutputFile(t *testing.T) {
	dir := t.TempDir()
	writeBatchOutput(t, dir, 0, "count\n7\n")

	_, err := collectBatchResults([]string{"SELECT 1", "SELECT 2"}, dir)

	require.ErrorContains(t, err, "statement 1")
}

func TestBatchErrorNamesTheFirstStatementWithoutOutput(t *testing.T) {
	dir := t.TempDir()
	writeBatchOutput(t, dir, 0, "count\n7\n")
	runErr := fmt.Errorf("duckdb query failed: Parser Error")

	err := batchError(runErr, []string{"SELECT 1", "SELECT bad", "SELECT 3"}, dir)

	require.ErrorContains(t, err, "batched statement 1")
	require.ErrorIs(t, err, runErr)
}

func TestBatchErrorFallsBackWhenEveryStatementWroteOutput(t *testing.T) {
	dir := t.TempDir()
	writeBatchOutput(t, dir, 0, "count\n7\n")
	runErr := fmt.Errorf("duckdb query failed: something else")

	require.Equal(t, runErr, batchError(runErr, []string{"SELECT 1"}, dir))
}

func TestMissingSpatialExtensionRecognizesBothDuckDBReports(t *testing.T) {
	require.True(t, missingSpatialExtension(fmt.Errorf(`Catalog Error: Scalar Function with name "st_point" is not in the catalog, but it exists in the spatial extension`)))
	require.True(t, missingSpatialExtension(fmt.Errorf("Conversion Error: Unimplemented type for cast (BLOB -> GEOMETRY)")))
	require.False(t, missingSpatialExtension(fmt.Errorf("Parser Error: syntax error at or near SELCT")))
}

func writeBatchOutput(t *testing.T, dir string, index int, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.csv", index)), []byte(content), 0o600))
}

func indexOf(t *testing.T, haystack string, needle string) int {
	t.Helper()
	index := strings.Index(haystack, needle)
	require.GreaterOrEqual(t, index, 0, "expected %q in the script", needle)
	return index
}
