package get

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

// GetCmd groups the lookups that read the RDF resources inside a built data
// product. Anything about the Iceberg table that holds them belongs in
// `sal query` instead.
type GetCmd struct {
	Classes   *classesCmd   `arg:"subcommand:classes" help:"List the RDF classes that resources in the data product are typed with"`
	Datatypes *datatypesCmd `arg:"subcommand:datatypes" help:"List the RDF datatypes the data product declares"`
}

func (cmd *GetCmd) Run() error {
	switch {
	case cmd.Classes != nil:
		return cmd.Classes.Run()
	case cmd.Datatypes != nil:
		return cmd.Datatypes.Run()
	default:
		return fmt.Errorf("get must be ran with a subcommand")
	}
}

// runLookup opens the triples table of the SAL project in the current working
// directory and runs the SQL the lookup builds for the object column layout the
// table was built with.
func runLookup(sqlFor func(salsparql.ObjectLayout) string) (salsparql.Result, error) {
	ctx := context.Background()
	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return salsparql.Result{}, err
	}
	runner, err := table.Runner(ctx, 0)
	if err != nil {
		return salsparql.Result{}, err
	}
	return runner.RunSQL(ctx, sqlFor(runner.Layout))
}

// formatTable renders a DuckDB result as aligned columns so that a resource
// lookup prints readably whatever columns it selected.
func formatTable(header []string, rows [][]string) string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	if len(header) > 0 {
		_, _ = fmt.Fprintln(writer, strings.Join(header, "\t"))
	}
	for _, row := range rows {
		_, _ = fmt.Fprintln(writer, strings.Join(row, "\t"))
	}
	_ = writer.Flush()
	return out.String()
}
