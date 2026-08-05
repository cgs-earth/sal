package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type classesCmd struct{}

func (cmd *classesCmd) Run() error {
	result, err := runLookup(salsparql.ClassesSQL)
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
