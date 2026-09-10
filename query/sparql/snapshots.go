package sparql

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// NamedQuery is a ready to run DuckDB statement offered as a sample query.
type NamedQuery struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

// snapshotScanSQL is the table at a snapshot, read inline since iceberg_scan
// only takes a literal snapshot ID.
func snapshotScanSQL(tablePath string, snapshotID int64) string {
	return fmt.Sprintf("iceberg_scan('%s', allow_moved_paths = true, snapshot_from_id = %d)", escapeSQLLiteral(tablePath), snapshotID)
}

// SnapshotSQL reads the triples table as it stood at a snapshot, so an earlier
// state can be inspected without rolling the table back to it.
func SnapshotSQL(tablePath string, snapshotID int64) string {
	return fmt.Sprintf(`
SELECT *
FROM %s
ORDER BY triple_hash`, snapshotScanSQL(tablePath, snapshotID))
}

// SnapshotDiffSQL compares a snapshot to its parent by triple_hash and labels
// rows as added or removed from the requested snapshot's point of view.
func SnapshotDiffSQL(tablePath string, snapshotID int64, parentSnapshotID *int64) string {
	if parentSnapshotID == nil {
		return fmt.Sprintf(`
SELECT
	'added' AS change_type,
	snapshot_rows.*
FROM %s AS snapshot_rows
ORDER BY triple_hash`, snapshotScanSQL(tablePath, snapshotID))
	}

	return fmt.Sprintf(`
WITH snapshot_rows AS (
	SELECT *
	FROM %s
),
parent_rows AS (
	SELECT *
	FROM %s
)
SELECT
	'added' AS change_type,
	snapshot_rows.*
FROM snapshot_rows
WHERE NOT EXISTS (
	SELECT 1
	FROM parent_rows
	WHERE parent_rows.triple_hash = snapshot_rows.triple_hash
)
UNION ALL
SELECT
	'removed' AS change_type,
	parent_rows.*
FROM parent_rows
WHERE NOT EXISTS (
	SELECT 1
	FROM snapshot_rows
	WHERE snapshot_rows.triple_hash = parent_rows.triple_hash
)
ORDER BY change_type, triple_hash`, snapshotScanSQL(tablePath, snapshotID), snapshotScanSQL(tablePath, *parentSnapshotID))
}

// snapshotQueries turns the snapshot listing into the statements the UI offers
// for time travel: the table at the snapshot before the latest one, and the rows
// the latest snapshot added and removed. `iceberg_scan` only accepts a literal
// snapshot ID, so these have to be built once the snapshot IDs are known rather
// than written as a fixed sample.
func snapshotQueries(tablePath string, snapshots Result) []NamedQuery {
	idIndex := slices.Index(snapshots.Header, "snapshot_id")
	if idIndex < 0 {
		return nil
	}
	// InfoSQL orders snapshots newest first, so ids[1] is the snapshot the
	// latest one was committed on top of.
	var ids []int64
	for _, row := range snapshots.Rows {
		if len(row) <= idIndex {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSpace(row[idIndex]), 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	switch len(ids) {
	case 0:
		return nil
	case 1:
		return []NamedQuery{{Name: "Latest snapshot diff", SQL: SnapshotDiffSQL(tablePath, ids[0], nil)}}
	default:
		return []NamedQuery{
			{Name: "Previous snapshot", SQL: SnapshotSQL(tablePath, ids[1])},
			{Name: "Latest snapshot diff", SQL: SnapshotDiffSQL(tablePath, ids[0], &ids[1])},
		}
	}
}

// AtSnapshot returns the runner reading the table at the given snapshot instead
// of the current one.
func (r DuckDBRunner) AtSnapshot(snapshotID int64) Runner {
	r.SnapshotID = snapshotID
	return r
}

// HasSnapshot reports whether the triples table has a snapshot with the given
// ID. It asks the table each time rather than remembering the answer, since a
// build can commit a new snapshot while a server is running.
func (r DuckDBRunner) HasSnapshot(ctx context.Context, snapshotID int64) (bool, error) {
	statement := fmt.Sprintf("SELECT snapshot_id FROM iceberg_snapshots('%s') WHERE snapshot_id = %d", escapeSQLLiteral(r.TablePath), snapshotID)
	_, rows, err := r.runSQL(ctx, statement, false)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}
