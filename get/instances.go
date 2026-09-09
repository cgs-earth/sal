package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type instancesCmd struct {
	All bool `arg:"--all" help:"list instances from every namespace, not only the ones named under the project base"`
}

func (cmd *instancesCmd) Run() error {
	prefix, err := subjectPrefix(cmd.All)
	if err != nil {
		return err
	}
	result, err := salsparql.RunLookup(salsparql.InstancesSQL(prefix))
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		noneFound("instances", "the data product has no rdf:type statements outside its vocabulary", prefix)
		return nil
	}
	fmt.Print(salsparql.FormatTable(result.Header, result.Rows))
	return nil
}
