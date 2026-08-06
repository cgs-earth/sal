package sparql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// tableProperties mimics the two column result the properties query returns.
func tableProperties(rows ...[]string) Result {
	return Result{Header: []string{"key", "value"}, Rows: rows}
}

// statsResults mimics what the four statements Stats issues return, in order.
func statsResults(counts Result, properties Result) []Result {
	snapshots := Result{
		Header: []string{"sequence_number", "snapshot_id"},
		Rows:   [][]string{{"2", "222"}, {"1", "111"}},
	}
	columnStats := Result{Header: []string{"column_name"}, Rows: [][]string{{"subject"}}}
	return []Result{counts, snapshots, properties, columnStats}
}

func TestStatsFromResultsPopulatesCountsAndSections(t *testing.T) {
	counts := Result{
		Header: []string{"triples", "subjects", "predicates", "objects"},
		Rows:   [][]string{{"554", "120", "17", "480"}},
	}
	properties := tableProperties([]string{"sal.salmodules", `["salmodule://github.com/test/one"]`})

	stats, err := statsFromResults("/tmp/table", statsResults(counts, properties))

	require.NoError(t, err)
	require.Equal(t, "/tmp/table", stats.TablePath)
	require.Equal(t, int64(554), stats.Triples)
	require.Equal(t, int64(120), stats.Subjects)
	require.Equal(t, int64(17), stats.Predicates)
	require.Equal(t, int64(480), stats.Objects)
	require.Equal(t, []string{"salmodule://github.com/test/one"}, stats.Modules)
	require.Equal(t, properties, stats.Properties)
	require.Len(t, stats.Snapshots.Rows, 2)
	require.Equal(t, [][]string{{"subject"}}, stats.ColumnStats.Rows)
	require.NotEmpty(t, stats.SampleQueries)
}

func TestStatsFromResultsRejectsAnEmptyCountsResult(t *testing.T) {
	counts := Result{Header: []string{"triples", "subjects", "predicates", "objects"}}

	_, err := statsFromResults("/tmp/table", statsResults(counts, tableProperties()))

	require.ErrorContains(t, err, "no counts")
}

func TestStatsFromResultsRejectsTheWrongNumberOfResults(t *testing.T) {
	_, err := statsFromResults("/tmp/table", []Result{{}, {}})

	require.ErrorContains(t, err, "expected 4 stats results, got 2")
}

func TestModulesFromPropertiesReadsRecordedModules(t *testing.T) {
	properties := tableProperties(
		[]string{"sal.hash", "abc123"},
		[]string{"sal.salmodules", `["salmodule://github.com/test/one","salmodule://github.com/test/two"]`},
	)

	modules := modulesFromProperties(properties)

	require.Equal(t, []string{"salmodule://github.com/test/one", "salmodule://github.com/test/two"}, modules)
}

func TestModulesFromPropertiesIsEmptyWithoutTheProperty(t *testing.T) {
	require.Empty(t, modulesFromProperties(tableProperties([]string{"sal.hash", "abc123"})))
}

func TestModulesFromPropertiesIgnoresMalformedJSON(t *testing.T) {
	require.Empty(t, modulesFromProperties(tableProperties([]string{"sal.salmodules", "not json"})))
}

func TestInfoSQLRejectsUnknownInfo(t *testing.T) {
	_, err := InfoSQL("bogus", "/tmp/table")

	require.ErrorContains(t, err, "unknown info option")
	require.NotContains(t, err.Error(), "tags")
}

func TestInfoSQLBuildsSnapshotsQueryWithTags(t *testing.T) {
	query, err := InfoSQL("snapshots", "/tmp/table")

	require.NoError(t, err)
	require.Contains(t, query, "FROM iceberg_snapshots('/tmp/table')")
	require.Contains(t, query, "read_text('/tmp/table/metadata/*.metadata.json')")
	require.Contains(t, query, "json_each(json_extract(metadata_json, '$.refs'))")
	require.Contains(t, query, "WHERE json_extract_string(ref.value, '$.type') = 'tag'")
	require.Contains(t, query, "string_agg(ref.key, ', ' ORDER BY ref.key) AS tags")
	require.Contains(t, query, "LEFT JOIN tags ON tags.snapshot_id = snapshots.snapshot_id")
}

func TestInfoSQLRejectsTagsInfo(t *testing.T) {
	_, err := InfoSQL("tags", "/tmp/table")

	require.ErrorContains(t, err, "unknown info option")
}

func TestInfoSQLEscapesSingleQuotesInTablePath(t *testing.T) {
	query, err := InfoSQL("column-stats", "/tmp/o'brien/triples")

	require.NoError(t, err)
	require.Contains(t, query, "iceberg_column_stats('/tmp/o''brien/triples')")
}

func TestCountsSQLUsesTypedObjectColumnsForTypedLayout(t *testing.T) {
	require.Contains(t, countsSQL(TypedObjects), "COUNT(DISTINCT COALESCE(triples.object_iri, CAST(triples.object_float AS VARCHAR), triples.object_string))")
	require.Contains(t, countsSQL(SimpleObjects), "COUNT(DISTINCT triples.object)")
}
