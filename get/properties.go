package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type propertiesCmd struct{}

func (cmd *propertiesCmd) Run() error {
	result, err := salsparql.RunLookup(salsparql.PropertiesSQL)
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		fmt.Println("no RDF properties found; the data product declares no rdf:Property, owl:ObjectProperty, owl:DatatypeProperty, or owl:AnnotationProperty resources")
		return nil
	}
	fmt.Print(salsparql.FormatTable(result.Header, result.Rows))
	return nil
}
