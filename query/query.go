package query

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
)

type QueryCmd struct {
	Info         string `help:"Retrieve quick info about the data product. Options: head, snapshots, column-stats properties" default:"head"`
	SnapshotDiff string `arg:"--snapshot-diff" help:"Show rows added and removed by the specified Iceberg snapshot ID. Specify 'latest' for the latest snapshot."`
}

func Run(cmd *QueryCmd) error {
	if cmd == nil {
		return fmt.Errorf("query: missing arguments")
	}
	warehouse, err := pkg.SalDataDir()
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(warehouse)
	if err != nil {
		return fmt.Errorf("failed to read SAL data directory: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no data has been built yet; run `sal build` to build a data product first")
	}

	namespace, err := pkg.GitProjectName()
	if err != nil {
		return err
	}

	tablePath := joinRemote(warehouse, namespace, "triples")
	escapedTablePath := strings.ReplaceAll(tablePath, "'", "''")

	infoQuery := ""
	if cmd.SnapshotDiff != "" {
		infoQuery, err = queryForSnapshotDiff(context.Background(), warehouse, namespace, escapedTablePath, cmd.SnapshotDiff)
		if err != nil {
			return err
		}
	} else {
		infoQuery, err = queryForInfo(cmd.Info, escapedTablePath)
		if err != nil {
			return err
		}
	}

	tmp, err := os.CreateTemp("", "sal-duckdb-*.sql")
	if err != nil {
		return fmt.Errorf("failed to create duckdb init file: %w", err)
	}
	defer func() {
		if err := os.Remove(tmp.Name()); err != nil {
			slog.Error(err.Error())
		}
	}()

	_, err = fmt.Fprintf(tmp, `
INSTALL iceberg;
LOAD iceberg;

INSTALL spatial;
LOAD spatial;

CREATE OR REPLACE VIEW triples AS
SELECT *
FROM iceberg_scan('%s', allow_moved_paths = true);

.mode box

%s;

.print ''
.print 'Connected to Iceberg table as view: triples'
.print 'You can now query it, e.g.:'
.print '  SELECT * FROM triples LIMIT 10;'
.print ''
`, escapedTablePath, infoQuery)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write duckdb init file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close duckdb init file: %w", err)
	}

	duck := exec.Command("duckdb", "-init", tmp.Name())
	duck.Stdin = os.Stdin
	duck.Stdout = os.Stdout
	duck.Stderr = os.Stderr

	if err := duck.Run(); err != nil {
		return fmt.Errorf("failed to open duckdb shell: %w", err)
	}

	return nil
}

func queryForInfo(info string, escapedTablePath string) (string, error) {
	switch info {
	case "", "head":
		return "SELECT * FROM triples LIMIT 20", nil
	case "properties":
		return fmt.Sprintf(`
WITH latest_metadata AS (
	SELECT
		filename,
		content::JSON AS metadata_json
	FROM read_text('%s/metadata/*.metadata.json')
	ORDER BY regexp_extract(filename, 'v([0-9]+)\.metadata\.json', 1)::BIGINT DESC
	LIMIT 1
)
SELECT
	prop.key,
	json_extract_string(prop.value, '$') AS value
FROM latest_metadata,
json_each(json_extract(metadata_json, '$.properties')) AS prop
ORDER BY prop.key`, escapedTablePath), nil
	case "snapshots":
		return fmt.Sprintf("SELECT * FROM iceberg_snapshots('%s')", escapedTablePath), nil
	case "column-stats":
		return fmt.Sprintf("SELECT * FROM iceberg_column_stats('%s')", escapedTablePath), nil
	default:
		return "", fmt.Errorf("unknown info option %q; expected one of: head, properties, snapshots, column-stats", info)
	}
}

func queryForSnapshotDiff(ctx context.Context, warehouse string, namespace string, escapedTablePath string, snapshotDiff string) (string, error) {
	cat, err := hadoop.NewCatalog("local-catalog", warehouse, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create catalog: %w", err)
	}
	tbl, err := cat.LoadTable(ctx, table.Identifier{namespace, "triples"})
	if err != nil {
		return "", fmt.Errorf("load table: %w", err)
	}
	snapshot, err := snapshotForDiff(snapshotDiff, tbl.CurrentSnapshot(), tbl.SnapshotByID)
	if err != nil {
		return "", err
	}

	return snapshotDiffQuery(escapedTablePath, snapshot.SnapshotID, snapshot.ParentSnapshotID), nil
}

func snapshotForDiff(snapshotDiff string, currentSnapshot *table.Snapshot, snapshotByID func(int64) *table.Snapshot) (*table.Snapshot, error) {
	snapshotDiff = strings.TrimSpace(snapshotDiff)
	if strings.EqualFold(snapshotDiff, "latest") {
		if currentSnapshot == nil {
			return nil, fmt.Errorf("no snapshots found")
		}
		return currentSnapshot, nil
	}
	snapshotID, err := strconv.ParseInt(snapshotDiff, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("snapshot diff must be a positive snapshot ID or 'latest'")
	}
	if snapshotID <= 0 {
		return nil, fmt.Errorf("snapshot diff must be a positive snapshot ID or 'latest'")
	}
	snapshot := snapshotByID(snapshotID)
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot %d not found", snapshotID)
	}
	return snapshot, nil
}

// snapshotDiffQuery compares a snapshot to its parent by triple_hash and labels
// rows as added or removed from the requested snapshot's point of view.
func snapshotDiffQuery(escapedTablePath string, snapshotID int64, parentSnapshotID *int64) string {
	if parentSnapshotID == nil {
		return fmt.Sprintf(`
SELECT
	'added' AS change_type,
	snapshot_rows.*
FROM iceberg_scan('%s', allow_moved_paths = true, snapshot_from_id = %d) AS snapshot_rows
ORDER BY triple_hash`, escapedTablePath, snapshotID)
	}

	return fmt.Sprintf(`
WITH snapshot_rows AS (
	SELECT *
	FROM iceberg_scan('%s', allow_moved_paths = true, snapshot_from_id = %d)
),
parent_rows AS (
	SELECT *
	FROM iceberg_scan('%s', allow_moved_paths = true, snapshot_from_id = %d)
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
ORDER BY change_type, triple_hash`, escapedTablePath, snapshotID, escapedTablePath, *parentSnapshotID)
}

func joinRemote(base string, parts ...string) string {
	joined := path.Join(parts...)
	if joined == "." {
		return strings.TrimSuffix(base, "/")
	}
	return strings.TrimSuffix(base, "/") + "/" + joined
}
