package get

import (
	"encoding/json"
	"fmt"
	"sort"

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

// pinnedVocabularyNode is the subset of the JSON-LD shape
// build/validate.PinnedVocabularies writes for a pinned vocabulary node in
// .sal/config.jsonld that this command reports, read directly off the config
// file rather than through that package's unexported pin store.
type pinnedVocabularyNode struct {
	ID         string `json:"@id"`
	VersionIRI struct {
		ID string `json:"@id"`
	} `json:"owl:versionIRI"`
	Format string `json:"dcterms:format,omitempty"`
}

func (cmd *ontologiesCmd) Run() error {
	base, err := pkg.DefaultSalBase()
	if err != nil {
		return err
	}
	path, err := pkg.SalConfigPath()
	if err != nil {
		return err
	}

	doc, err := pkg.ReadConfigDocument(path)
	if err != nil {
		return err
	}
	_, pinnedNodes, err := pkg.PartitionConfigGraph(doc.Graph)
	if err != nil {
		return err
	}

	ontology, err := importation.ReadOntology(path, base)
	if err != nil {
		return err
	}
	var imports []string
	if ontology != nil {
		imports = ontology.Imports
	}

	rows, err := ontologyRows(pinnedNodes, imports)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no ontologies found; run `sal import` to import one, or `sal build`/`sal validate` to pin the vocabularies a project resolves against")
		return nil
	}
	header, rows := dropEmptyColumns([]string{"ontology", "version", "format", "imported"}, rows)
	fmt.Print(salsparql.FormatTable(header, rows))
	return nil
}

// ontologyRows renders the union of the pinned vocabulary nodes of
// .sal/config.jsonld and the project ontology's owl:imports as table rows,
// sorted by @id. A pinned vocabulary that is also imported reports the
// version and format it was pinned at; an import with no pin yet, such as an
// oci:// artifact, reports empty version and format columns instead.
func ontologyRows(pinnedNodes []json.RawMessage, imports []string) ([][]string, error) {
	type pin struct {
		version string
		format  string
	}
	pinned := make(map[string]pin, len(pinnedNodes))
	ids := make([]string, 0, len(pinnedNodes)+len(imports))
	seen := make(map[string]bool, len(pinnedNodes)+len(imports))
	for _, raw := range pinnedNodes {
		var node pinnedVocabularyNode
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, err
		}
		pinned[node.ID] = pin{version: node.VersionIRI.ID, format: node.Format}
		if !seen[node.ID] {
			seen[node.ID] = true
			ids = append(ids, node.ID)
		}
	}
	imported := make(map[string]bool, len(imports))
	for _, iri := range imports {
		imported[iri] = true
		if !seen[iri] {
			seen[iri] = true
			ids = append(ids, iri)
		}
	}
	sort.Strings(ids)

	rows := make([][]string, len(ids))
	for i, id := range ids {
		importedValue := "no"
		if imported[id] {
			importedValue = "yes"
		}
		rows[i] = []string{id, pinned[id].version, pinned[id].format, importedValue}
	}
	return rows, nil
}
