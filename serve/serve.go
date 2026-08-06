package serve

import (
	"context"
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type ServeCmd struct {
	WithUI bool `arg:"--with-ui" help:"Serve the SAL web UI at / with stats, SQL, SPARQL, module, and map tabs"`
}

func (cmd *ServeCmd) Run() error {
	if cmd == nil {
		return fmt.Errorf("serve: missing arguments")
	}
	ctx := context.Background()
	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return err
	}
	runner, err := table.Runner(ctx, 0)
	if err != nil {
		return err
	}
	return Serve(ctx, ":8080", runner, cmd.WithUI)
}
