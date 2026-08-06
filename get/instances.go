package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type instancesCmd struct{}

func (cmd *instancesCmd) Run() error {
	result, err := salsparql.RunLookup(salsparql.InstancesSQL)
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		fmt.Println("no instances found; the data product has no rdf:type statements outside its vocabulary")
		return nil
	}
	fmt.Print(salsparql.FormatTable(result.Header, result.Rows))
	return nil
}
