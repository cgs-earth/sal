package get

import (
	"fmt"

	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
)

// importsCmd lists the owl:imports a project records on the ontology node in
// .sal/config.jsonld, which `sal import` writes and `sal build` reads to
// merge imported ontologies into the data product.
type importsCmd struct{}

func (cmd *importsCmd) Run() error {
	base, err := pkg.DefaultSalBase()
	if err != nil {
		return err
	}
	path, err := pkg.SalConfigPath()
	if err != nil {
		return err
	}

	ontology, err := importation.ReadOntology(path, base)
	if err != nil {
		return err
	}
	if ontology == nil || len(ontology.Imports) == 0 {
		fmt.Println("no imports found; run `sal import` to add one")
		return nil
	}

	rows := make([][]string, len(ontology.Imports))
	for i, iri := range ontology.Imports {
		rows[i] = []string{iri}
	}
	fmt.Print(salsparql.FormatTable([]string{"import"}, rows))
	return nil
}
