package query

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
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
	ctx := context.Background()
	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return err
	}
	runner, err := table.Runner(ctx, 100)
	if err != nil {
		return err
	}

	if cmd.SPARQL {
		return salsparql.RunShell(ctx, runner)
	}

	// The shell opens on the requested info query, so `sal query --info snapshots`
	// shows the snapshots and then leaves the triples view there to explore.
	infoQuery := ""
	if cmd.SnapshotDiff != "" {
		infoQuery, err = queryForSnapshotDiff(ctx, table.Warehouse, table.Namespace, table.Path, cmd.SnapshotDiff)
	} else {
		infoQuery, err = salsparql.InfoSQL(cmd.Info, table.Path)
	}
	if err != nil {
		return err
	}
	return salsparql.RunSQLShell(ctx, runner, infoQuery)
}

// InstallExtensionsCmd downloads the DuckDB extensions sal queries load.
//
// DuckDB is linked into the binary, but its extensions are not, so they are
// fetched from extensions.duckdb.org on first use. Running this ahead of time
// primes that cache, which is what the container image does so that a query
// never has to reach the network.
type InstallExtensionsCmd struct{}

func (cmd *InstallExtensionsCmd) Run() error {
	if err := salsparql.InstallExtensions(context.Background()); err != nil {
		return err
	}
	pkg.Infof("Installed the DuckDB extensions: %s\n", strings.Join(salsparql.Extensions, ", "))
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
