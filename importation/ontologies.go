package importation

import (
	"encoding/json"
	"sort"

	"github.com/cgs-earth/sal/pkg"
)

// OntologyRow is one row of the project's ontology listing: the union of the
// vocabularies build/validate has pinned and the ontologies/artifacts sal
// import has recorded, reported together the way `sal get ontologies` and the
// serve --with-ui stats view both display it.
type OntologyRow struct {
	ID       string
	Version  string
	Format   string
	Imported bool
}

// pinnedVocabularyNode is the subset of the JSON-LD shape
// build/validate.PinnedVocabularies writes for a pinned vocabulary node in
// .sal/config.jsonld that this reports, read directly off the config file
// rather than through that package's unexported pin store.
type pinnedVocabularyNode struct {
	ID         string `json:"@id"`
	VersionIRI struct {
		ID string `json:"@id"`
	} `json:"owl:versionIRI"`
	Format string `json:"dcterms:format,omitempty"`
}

// ProjectOntologyRows reads .sal/config.jsonld at path and reports the union
// of the vocabularies it has pinned and the ontologies its project ontology
// node has imported, sorted by @id. A project with no config file yet simply
// reports no rows.
func ProjectOntologyRows(path string, base string) ([]OntologyRow, error) {
	doc, err := pkg.ReadConfigDocument(path)
	if err != nil {
		return nil, err
	}
	_, pinnedNodes, err := pkg.PartitionConfigGraph(doc.Graph)
	if err != nil {
		return nil, err
	}

	ontology, err := ReadOntology(path, base)
	if err != nil {
		return nil, err
	}
	var imports []string
	if ontology != nil {
		imports = ontology.Imports
	}

	return OntologyRows(pinnedNodes, imports)
}

// OntologyRows renders the union of the pinned vocabulary nodes of
// .sal/config.jsonld and the project ontology's owl:imports as rows, sorted
// by @id. A pinned vocabulary that is also imported reports the version and
// format it was pinned at; an import with no pin yet, such as an oci://
// artifact, reports empty version and format instead.
func OntologyRows(pinnedNodes []json.RawMessage, imports []string) ([]OntologyRow, error) {
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

	rows := make([]OntologyRow, len(ids))
	for i, id := range ids {
		rows[i] = OntologyRow{ID: id, Version: pinned[id].version, Format: pinned[id].format, Imported: imported[id]}
	}
	return rows, nil
}

// OntologyTableHeader is the column header `sal get ontologies` and the serve
// --with-ui stats view both use for OntologyRows rendered as a table.
var OntologyTableHeader = []string{"ontology", "version", "format", "imported"}

// OntologyTableRows renders rows as a table under OntologyTableHeader,
// spelling out Imported as "yes"/"no".
func OntologyTableRows(rows []OntologyRow) [][]string {
	table := make([][]string, len(rows))
	for i, row := range rows {
		imported := "no"
		if row.Imported {
			imported = "yes"
		}
		table[i] = []string{row.ID, row.Version, row.Format, imported}
	}
	return table
}
