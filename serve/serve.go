package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type ServeCmd struct {
	WithUI bool   `arg:"--with-ui" help:"Serve the SAL web UI at / with stats, SQL, SPARQL, module, and map tabs"`
	Data   string `arg:"--data" placeholder:"DIR" help:"Serve a copy of a project's .sal/data directory, such as a bucket sal upload wrote mounted into a container, instead of the project the current directory belongs to; it holds one Iceberg table plus the blobs and stac directories beside it"`
	// SlowQueryAfter is QueryLanes.DemoteAfter; see salsparql.UseQueryLanes.
	SlowQueryAfter time.Duration `arg:"--slow-query-after" placeholder:"DURATION" default:"5s" help:"Move a query still running after this long to a second DuckDB database with a smaller share of the threads and memory, so that heavy queries do not hold up quick ones; 0 runs every query on one database"`
}

func (cmd *ServeCmd) Run() error {
	if cmd == nil {
		return fmt.Errorf("serve: missing arguments")
	}
	// an interrupt stops the server rather than the process, so that the
	// spans and metrics of the last requests are delivered before exit
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cmd.SlowQueryAfter > 0 {
		salsparql.UseQueryLanes(salsparql.DefaultQueryLanes(cmd.SlowQueryAfter))
	}
	table, blobDir, stacDir, err := cmd.locate()
	if err != nil {
		return err
	}
	runner, err := table.Runner(ctx, 0)
	var missing salsparql.NoTriplesTableError
	if errors.As(err, &missing) {
		return fmt.Errorf("no data found at %s: add data to the project before serving it", missing.Path)
	}
	if err != nil {
		return err
	}
	return Serve(ctx, ":8080", runner, blobDir, stacDir, cmd.WithUI)
}

// locate finds the table to serve and the blob and STAC directories beside
// it: in the copy of .sal/data that --data names, which needs no SAL project
// around it, or else in the project the current directory belongs to.
func (cmd *ServeCmd) locate() (salsparql.TriplesTable, string, string, error) {
	if cmd.Data != "" {
		table, err := salsparql.LocateDataDirTable(cmd.Data)
		if err != nil {
			return salsparql.TriplesTable{}, "", "", err
		}
		return table, filepath.Join(table.DataRoot, "blobs"), filepath.Join(table.DataRoot, "stac"), nil
	}
	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return salsparql.TriplesTable{}, "", "", err
	}
	blobDir, err := pkg.SalBlobsDir()
	if err != nil {
		return salsparql.TriplesTable{}, "", "", err
	}
	stacDir, err := pkg.SalStacDir()
	if err != nil {
		return salsparql.TriplesTable{}, "", "", err
	}
	return table, blobDir, stacDir, nil
}
