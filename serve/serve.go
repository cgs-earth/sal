package serve

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type ServeCmd struct {
	WithUI bool `arg:"--with-ui" help:"Serve the SAL web UI at / with stats, SQL, SPARQL, module, and map tabs"`
}

func (cmd *ServeCmd) Run() error {
	if cmd == nil {
		return fmt.Errorf("serve: missing arguments")
	}
	// an interrupt stops the server rather than the process, so that the
	// spans and metrics of the last requests are delivered before exit
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return err
	}
	runner, err := table.Runner(ctx, 0)
	if err != nil {
		return err
	}
	blobDir, err := pkg.SalBlobsDir()
	if err != nil {
		return err
	}
	stacDir, err := pkg.SalStacDir()
	if err != nil {
		return err
	}
	return Serve(ctx, ":8080", runner, blobDir, stacDir, cmd.WithUI)
}
