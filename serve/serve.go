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
	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return err
	}

	layout, err := salsparql.ObjectLayoutForTable(context.Background(), table.Warehouse, table.Namespace)
	if err != nil {
		return err
	}
	return Serve(context.Background(), ":8080", table.Path, layout, cmd.WithUI)
}
