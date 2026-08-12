package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type shapesCmd struct{}

func (cmd *shapesCmd) Run() error {
	result, err := salsparql.RunLookup(salsparql.ShapesSQL)
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		fmt.Println("no SHACL shapes found; the data product declares no sh:NodeShape or sh:PropertyShape resources")
		return nil
	}
	header, rows := dropEmptyColumns(result.Header, result.Rows)
	fmt.Print(salsparql.FormatTable(header, rows))
	return nil
}
