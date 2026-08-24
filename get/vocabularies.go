package get

import (
	"fmt"

	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
)

// vocabulariesCmd lists every vocabulary the project knows about from
// .sal/config.jsonld: the vocabularies `sal build` and `sal validate` have
// pinned, unioned with what `sal import` has recorded with owl:imports on the
// project ontology node, each marked with whether it is imported. An http or
// salmodule:// import is itself a vocabulary document, resolved the same way a
// pinned vocabulary is, so it is listed as one; so is an oci:// import, since
// the artifact it names is a data product built from a vocabulary of its own
// even though nothing dereferences it as a document to pin. An import not yet
// pinned by a build or validation still gets a row, just with no version or
// format.
//
// These are vocabularies, not ontologies: a vocabulary is only known to be an
// owl:Ontology when its document says so, and this listing never reads the
// document.
type vocabulariesCmd struct{}

func (cmd *vocabulariesCmd) Run() error {
	base, err := pkg.DefaultSalBase()
	if err != nil {
		return err
	}
	path, err := pkg.SalConfigPath()
	if err != nil {
		return err
	}

	rows, err := importation.ProjectVocabularyRows(path, base)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no vocabularies found; run `sal import` to import one, or `sal build`/`sal validate` to pin the vocabularies a project resolves against")
		return nil
	}
	header, tableRows := dropEmptyColumns(importation.VocabularyTableHeader, importation.VocabularyTableRows(rows))
	fmt.Print(salsparql.FormatTable(header, tableRows))
	return nil
}
