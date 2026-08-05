package query

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type QueryCmd struct {
	Info         string `help:"Retrieve quick info about the data product. Options: head, snapshots, column-stats properties" default:"head"`
	SnapshotDiff string `arg:"--snapshot-diff" help:"Show rows added and removed by the specified Iceberg snapshot ID. Specify 'latest' for the latest snapshot."`
	SPARQL       bool   `arg:"--sparql" help:"Open an interactive read-only SPARQL shell against the triples table"`
}

func (cmd *QueryCmd) Run() error {
	if cmd == nil {
		return fmt.Errorf("query: missing arguments")
	}
	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return err
	}
	tablePath := table.Path
	escapedTablePath := strings.ReplaceAll(tablePath, "'", "''")

	if cmd.SPARQL {
		layout, err := salsparql.ObjectLayoutForTable(context.Background(), table.Warehouse, table.Namespace)
		if err != nil {
			return err
		}
		return salsparql.RunShell(context.Background(), tablePath, layout)
	}

	infoQuery := ""
	if cmd.SnapshotDiff != "" {
		infoQuery, err = queryForSnapshotDiff(context.Background(), table.Warehouse, table.Namespace, tablePath, cmd.SnapshotDiff)
		if err != nil {
			return err
		}
	} else {
		infoQuery, err = salsparql.InfoSQL(cmd.Info, tablePath)
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

func queryForSnapshotDiff(ctx context.Context, warehouse string, namespace string, tablePath string, snapshotDiff string) (string, error) {
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

	return salsparql.SnapshotDiffSQL(tablePath, snapshot.SnapshotID, snapshot.ParentSnapshotID), nil
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
