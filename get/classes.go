package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type classesCmd struct {
	All bool `arg:"--all" help:"list classes from every namespace, not only the ones named under the project base"`
}

func (cmd *classesCmd) Run() error {
	prefix, err := subjectPrefix(cmd.All)
	if err != nil {
		return err
	}
	result, err := salsparql.RunLookup(salsparql.ClassesSQL(prefix))
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		noneFound("RDF classes", "the data product declares no rdfs:Class or owl:Class resources", prefix)
		return nil
	}
	header, rows := dropEmptyColumns(result.Header, result.Rows)
	fmt.Print(salsparql.FormatTable(header, rows))
	return nil
}
