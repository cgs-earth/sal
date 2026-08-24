package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type classesCmd struct{}

func (cmd *classesCmd) Run() error {
	result, err := salsparql.RunLookup(salsparql.ClassesSQL())
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		fmt.Println("no RDF classes found; the data product declares no rdfs:Class or owl:Class resources")
		return nil
	}
	header, rows := dropEmptyColumns(result.Header, result.Rows)
	fmt.Print(salsparql.FormatTable(header, rows))
	return nil
}
