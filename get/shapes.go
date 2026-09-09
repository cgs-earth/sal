package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type shapesCmd struct {
	All bool `arg:"--all" help:"list shapes from every namespace, not only the ones named under the project base"`
}

func (cmd *shapesCmd) Run() error {
	prefix, err := subjectPrefix(cmd.All)
	if err != nil {
		return err
	}
	result, err := salsparql.RunLookup(salsparql.ShapesSQL(prefix))
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		noneFound("SHACL shapes", "the data product declares no sh:NodeShape or sh:PropertyShape resources", prefix)
		return nil
	}
	header, rows := dropEmptyColumns(result.Header, result.Rows)
	fmt.Print(salsparql.FormatTable(header, rows))
	return nil
}
