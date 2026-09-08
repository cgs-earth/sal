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

func TestTranslateScansTheTableAtTheRunnersSnapshot(t *testing.T) {
	runner := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples", SnapshotID: 122}

	sql, err := runner.Translate(`
PREFIX schema: <https://schema.org/>

SELECT ?s ?name
WHERE {
  ?s schema:name ?name .
  ?s schema:age 42 .
}`)

	require.NoError(t, err)
	require.Contains(t, sql, "FROM iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 122) AS t0")
	require.Contains(t, sql, "CROSS JOIN iceberg_scan('/tmp/warehouse/sal/triples', allow_moved_paths = true, snapshot_from_id = 122) AS t1")
	require.NotContains(t, sql, "triples AS")
	require.Contains(t, sql, "t0.subject = t1.subject")
}

func TestTranslateScansTheTriplesViewWithoutASnapshot(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples"}.Translate("SELECT ?s WHERE { ?s ?p ?o }")

	require.NoError(t, err)
	require.Contains(t, sql, "FROM triples AS t0")
	require.NotContains(t, sql, "iceberg_scan")
}

func TestTranslateEscapesQuotesInTheSnapshotTablePath(t *testing.T) {
	sql, err := DuckDBRunner{TablePath: "/tmp/o'brien/sal/triples", SnapshotID: 1}.Translate("SELECT ?s WHERE { ?s ?p ?o }")

	require.NoError(t, err)
	require.Contains(t, sql, "iceberg_scan('/tmp/o''brien/sal/triples', allow_moved_paths = true, snapshot_from_id = 1) AS t0")
}

func TestSnapshotSourceDoesNotForceTheSpatialExtension(t *testing.T) {
	// The scan of a snapshot must not read as an ST_ call or a star projection,
	// or every snapshot query would load the spatial extension for nothing.
	sql, err := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples", SnapshotID: 122}.Translate("SELECT ?s WHERE { ?s ?p ?o }")

	require.NoError(t, err)
	require.False(t, needsSpatial(sql))
}

func TestAtSnapshotKeepsTheRestOfTheRunner(t *testing.T) {
	runner := DuckDBRunner{TablePath: "/tmp/warehouse/sal/triples", Limit: 100, Imports: []ImportedTable{{View: "upstream", Path: "/tmp/upstream"}}}

	snapshot, ok := runner.AtSnapshot(122).(DuckDBRunner)

	require.True(t, ok)
	require.Equal(t, int64(122), snapshot.SnapshotID)
	require.Equal(t, runner.TablePath, snapshot.TablePath)
	require.Equal(t, runner.Limit, snapshot.Limit)
	require.Equal(t, runner.Imports, snapshot.Imports)
	require.Zero(t, runner.SnapshotID, "the runner AtSnapshot was called on must read the current table still")
}
