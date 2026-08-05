package sparql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshotSQLReadsTheTableAtTheGivenSnapshot(t *testing.T) {
	sql := SnapshotSQL("/tmp/warehouse/sal/triples", 122)

	require.Contains(t, sql, "iceberg_scan('/tmp/warehouse/sal/triples'")
	require.Contains(t, sql, "snapshot_from_id = 122")
	require.Contains(t, sql, "ORDER BY triple_hash")
}

func TestSnapshotDiffSQLForRootSnapshotShowsAllRowsAdded(t *testing.T) {
	sql := SnapshotDiffSQL("/tmp/warehouse/sal/triples", 123, nil)

	require.Contains(t, sql, "'added' AS change_type")
	require.Contains(t, sql, "snapshot_from_id = 123")
	require.NotContains(t, sql, "parent_rows")
	require.Contains(t, sql, "ORDER BY triple_hash")
}

func TestSnapshotDiffSQLComparesSnapshotToParent(t *testing.T) {
	parentID := int64(122)

	sql := SnapshotDiffSQL("/tmp/warehouse/sal/triples", 123, &parentID)

	require.Contains(t, sql, "snapshot_from_id = 123")
	require.Contains(t, sql, "snapshot_from_id = 122")
	require.Contains(t, sql, "'added' AS change_type")
	require.Contains(t, sql, "'removed' AS change_type")
	require.Contains(t, sql, "parent_rows.triple_hash = snapshot_rows.triple_hash")
	require.Contains(t, sql, "UNION ALL")
}

func TestSnapshotSQLEscapesQuotesInTablePath(t *testing.T) {
	sql := SnapshotSQL("/tmp/o'brien/sal/triples", 1)

	require.Contains(t, sql, "iceberg_scan('/tmp/o''brien/sal/triples'")
}

func TestSnapshotQueriesTimeTravelsToThePreviousSnapshot(t *testing.T) {
	queries := snapshotQueries("/tmp/warehouse/sal/triples", Result{
		Header: []string{"sequence_number", "snapshot_id", "operation"},
		Rows: [][]string{
			{"2", "123", "overwrite"},
			{"1", "122", "append"},
		},
	})

	require.Len(t, queries, 2)
	require.Equal(t, "Previous snapshot", queries[0].Name)
	require.Contains(t, queries[0].SQL, "snapshot_from_id = 122")
	require.NotContains(t, queries[0].SQL, "snapshot_from_id = 123")
	require.Equal(t, "Latest snapshot diff", queries[1].Name)
	require.Contains(t, queries[1].SQL, "snapshot_from_id = 123")
	require.Contains(t, queries[1].SQL, "snapshot_from_id = 122")
}

func TestSnapshotQueriesForASingleSnapshotOnlyOffersTheDiff(t *testing.T) {
	queries := snapshotQueries("/tmp/warehouse/sal/triples", Result{
		Header: []string{"sequence_number", "snapshot_id"},
		Rows:   [][]string{{"1", "123"}},
	})

	require.Len(t, queries, 1)
	require.Equal(t, "Latest snapshot diff", queries[0].Name)
	require.NotContains(t, queries[0].SQL, "parent_rows")
}

func TestSnapshotQueriesForATableWithoutSnapshots(t *testing.T) {
	require.Empty(t, snapshotQueries("/tmp/warehouse/sal/triples", Result{
		Header: []string{"sequence_number", "snapshot_id"},
	}))
}

func TestSnapshotQueriesIgnoresAnUnexpectedSnapshotListing(t *testing.T) {
	require.Empty(t, snapshotQueries("/tmp/warehouse/sal/triples", Result{
		Header: []string{"sequence_number", "operation"},
		Rows:   [][]string{{"1", "append"}},
	}))
}
