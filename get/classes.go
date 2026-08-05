package get

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type classesCmd struct{}

func (cmd *classesCmd) Run() error {
	ctx := context.Background()
	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return err
	}
	runner, err := table.Runner(ctx, 0)
	if err != nil {
		return err
	}

	result, err := runner.RunSQL(ctx, salsparql.ClassesSQL(runner.Layout))
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		fmt.Println("no RDF classes found; the data product has no rdf:type statements")
		return nil
	}
	fmt.Print(formatTable(result.Header, result.Rows))
	return nil
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
