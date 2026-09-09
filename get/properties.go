package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type propertiesCmd struct {
	All bool `arg:"--all" help:"list properties from every namespace, not only the ones named under the project base"`
}

func (cmd *propertiesCmd) Run() error {
	prefix, err := subjectPrefix(cmd.All)
	if err != nil {
		return err
	}
	result, err := salsparql.RunLookup(salsparql.PropertiesSQL(prefix))
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		noneFound("RDF properties", "the data product declares no rdf:Property, owl:ObjectProperty, owl:DatatypeProperty, or owl:AnnotationProperty resources", prefix)
		return nil
	}
	fmt.Print(salsparql.FormatTable(result.Header, result.Rows))
	return nil
}
