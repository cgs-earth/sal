package sparql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
