package get

import (
	"fmt"

	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
)

// ontologiesCmd lists every ontology the project knows about from
// .sal/config.jsonld: the vocabularies `sal build` and `sal validate` have
// pinned, unioned with what `sal import` has recorded with owl:imports on the
// project ontology node, each marked with whether it is imported. An http or
// salmodule:// import is itself an ontology document, resolved the same way a
// vocabulary is, so it is listed as one; so is an oci:// import, since the
// artifact it names is a data product built from an ontology of its own even
// though nothing dereferences it as a document to pin. An import not yet
// pinned by a build or validation still gets a row, just with no version or
// format.
type ontologiesCmd struct{}

func (cmd *ontologiesCmd) Run() error {
	base, err := pkg.DefaultSalBase()
	if err != nil {
		return err
	}
	path, err := pkg.SalConfigPath()
	if err != nil {
		return err
	}

	rows, err := importation.ProjectOntologyRows(path, base)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no ontologies found; run `sal import` to import one, or `sal build`/`sal validate` to pin the vocabularies a project resolves against")
		return nil
	}
	header, tableRows := dropEmptyColumns(importation.OntologyTableHeader, importation.OntologyTableRows(rows))
	fmt.Print(salsparql.FormatTable(header, tableRows))
	return nil
}
