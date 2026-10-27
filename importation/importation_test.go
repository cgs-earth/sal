package importation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/pkg"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const testBase = "https://github.com/cgs-earth/sal/"

// writeTestOntology writes content verbatim as .sal/config.jsonld, for tests
// that exercise how a raw file on disk is handled.
func writeTestOntology(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.jsonld")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

// writeTestOntologyNode wraps content, a standalone ontology document the way
// SerializeOntology renders one, as the sole node of a fresh
// .sal/config.jsonld's @graph -- the shape ReadOntology expects on disk.
func writeTestOntologyNode(t *testing.T, content string) string {
	t.Helper()
	context, node, err := pkg.SplitJSONLDDocument([]byte(content))
	require.NoError(t, err)
	doc := pkg.ConfigDocument{Context: context, Graph: []json.RawMessage{node}}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.jsonld")
	require.NoError(t, os.WriteFile(path, raw, 0644))
	return path
}

func TestReadOntologyReturnsNilWhenTheFileIsMissing(t *testing.T) {
	ontology, err := ReadOntology(filepath.Join(t.TempDir(), "config.jsonld"), testBase)
	require.NoError(t, err)
	require.Nil(t, ontology)
}

func TestReadOntologyResolvesTheRelativeOntologyIRIAgainstTheProjectBase(t *testing.T) {
	path := writeTestOntologyNode(t, `{
  "@context": {
    "dcterms": "http://purl.org/dc/terms/",
    "owl": "http://www.w3.org/2002/07/owl#"
  },
  "@id": ".",
  "@type": "owl:Ontology",
  "dcterms:title": "My Ontology",
  "owl:imports": [
    { "@id": "https://example.com/onto2" },
    { "@id": "https://example.com/onto1" }
  ]
}`)

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, testBase, ontology.IRI)
	require.Equal(t, "My Ontology", ontology.Title)
	require.Equal(t, []string{"https://example.com/onto1", "https://example.com/onto2"}, ontology.Imports)
}

func TestReadOntologyReportsInvalidJSONLD(t *testing.T) {
	path := writeTestOntology(t, "not valid JSON")

	_, err := ReadOntology(path, testBase)
	require.ErrorContains(t, err, "invalid JSON-LD")
}

func TestWriteOntologyRendersTheProjectAsARelativeIRI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonld")

	require.NoError(t, WriteOntology(path, &Ontology{
		IRI:        testBase,
		Base:       testBase,
		IsOntology: true,
		Title:      "My Ontology",
		Imports:    []string{"https://example.com/onto1", "https://example.com/onto2"},
	}))

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{
  "@context": {
    "owl": "http://www.w3.org/2002/07/owl#",
    "dcterms": "http://purl.org/dc/terms/",
    "xsd": "http://www.w3.org/2001/XMLSchema#",
    "rdfs": "http://www.w3.org/2000/01/rdf-schema#",
    "schema": "https://schema.org/"
  },
  "@graph": [
    {
      "@id": ".",
      "@type": "owl:Ontology",
      "dcterms:title": "My Ontology",
      "owl:imports": [
        { "@id": "https://example.com/onto1" },
        { "@id": "https://example.com/onto2" }
      ]
    }
  ]
}`, string(content))
}

func TestWriteOntologyOmitsAnEmptyImportsClause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonld")

	require.NoError(t, WriteOntology(path, &Ontology{IRI: testBase, Base: testBase, IsOntology: true, Title: "sal"}))

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{
  "@context": {
    "owl": "http://www.w3.org/2002/07/owl#",
    "dcterms": "http://purl.org/dc/terms/",
    "xsd": "http://www.w3.org/2001/XMLSchema#",
    "rdfs": "http://www.w3.org/2000/01/rdf-schema#",
    "schema": "https://schema.org/"
  },
  "@graph": [
    { "@id": ".", "@type": "owl:Ontology", "dcterms:title": "sal" }
  ]
}`, string(content))
}

// configContext is the @context every write of .sal/config.jsonld leaves in
// the file, as JSON, so a test can state the node it expects alone.
const configContext = `{
    "owl": "http://www.w3.org/2002/07/owl#",
    "dcterms": "http://purl.org/dc/terms/",
    "xsd": "http://www.w3.org/2001/XMLSchema#",
    "rdfs": "http://www.w3.org/2000/01/rdf-schema#",
    "schema": "https://schema.org/"
  }`

// The node sal init writes is the project alone: a schema:Project with its
// description, and none of the ontology statements sal import adds.
func TestWriteOntologyRendersTheProjectNodeInitWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonld")

	require.NoError(t, WriteOntology(path, &Ontology{
		IRI:         testBase,
		Base:        testBase,
		IsProject:   true,
		Description: "A test project showing off sal on a small dataset",
	}))

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{
  "@context": `+configContext+`,
  "@graph": [
    {
      "@id": ".",
      "@type": "schema:Project",
      "dcterms:description": "A test project showing off sal on a small dataset"
    }
  ]
}`, string(content))
}

// Importing into the node sal init wrote keeps the project's type and
// description and adds the ontology's: the node is one project that is both.
func TestWriteOntologyKeepsTheDescriptionWhenTheProjectBecomesAnOntology(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonld")
	require.NoError(t, WriteOntology(path, &Ontology{IRI: testBase, Base: testBase, IsProject: true, Description: "A test project"}))

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.True(t, ontology.IsProject)
	require.False(t, ontology.IsOntology)
	require.Equal(t, "A test project", ontology.Description)

	ontology.IsOntology = true
	ontology.Title = "sal"
	ontology.Imports = []string{"https://example.com/onto1"}
	require.NoError(t, WriteOntology(path, ontology))

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{
  "@context": `+configContext+`,
  "@graph": [
    {
      "@id": ".",
      "@type": ["schema:Project", "owl:Ontology"],
      "dcterms:description": "A test project",
      "dcterms:title": "sal",
      "owl:imports": [{ "@id": "https://example.com/onto1" }]
    }
  ]
}`, string(content))

	rewritten, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.True(t, rewritten.IsProject)
	require.True(t, rewritten.IsOntology)
	require.Equal(t, "A test project", rewritten.Description)
	require.Equal(t, "sal", rewritten.Title)
}

// A node written before sal init recorded descriptions is an ontology and
// nothing more, and a rewrite leaves it that way rather than typing it a
// schema:Project it never said it was; its dc:title is read and written back
// as the dcterms:title the node carries now.
func TestWriteOntologyDoesNotTypeAnOntologyOnlyNodeAsAProject(t *testing.T) {
	path := writeTestOntologyNode(t, `{
  "@context": { "dc": "http://purl.org/dc/elements/1.1/", "owl": "http://www.w3.org/2002/07/owl#" },
  "@id": ".",
  "@type": "owl:Ontology",
  "dc:title": "My Ontology"
}`)

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.False(t, ontology.IsProject)
	require.True(t, ontology.IsOntology)
	require.NoError(t, WriteOntology(path, ontology))

	doc, err := pkg.ReadConfigDocument(path)
	require.NoError(t, err)
	require.JSONEq(t, `{ "@id": ".", "@type": "owl:Ontology", "dcterms:title": "My Ontology" }`, string(doc.Graph[0]))
}

func TestWriteOntologyReplacesTheDescriptionInAFileWithExtraStatements(t *testing.T) {
	path := writeTestOntologyNode(t, `{
  "@context": {
    "dcterms": "http://purl.org/dc/terms/",
    "dcterms": "http://purl.org/dc/terms/",
    "schema": "https://schema.org/"
  },
  "@id": ".",
  "@type": "schema:Project",
  "dcterms:description": "Old description",
  "dcterms:creator": "A Person"
}`)

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, "Old description", ontology.Description)
	ontology.Description = "New description"
	require.NoError(t, WriteOntology(path, ontology))

	rewritten, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, "New description", rewritten.Description)
	require.True(t, rewritten.IsProject)
	require.False(t, rewritten.IsOntology)
	subject := rdflibgo.NewURIRefUnsafe(testBase)
	require.True(t, pkg.Contains(rewritten.Graph, subject, rdflibgo.NewURIRefUnsafe(dctermsNamespace+"creator"), rdflibgo.NewLiteral("A Person")))
	var descriptions int
	description := dctermsDescription
	pkg.Triples(rewritten.Graph, nil, &description, nil)(func(rdflibgo.Triple) bool {
		descriptions++
		return true
	})
	require.Equal(t, 1, descriptions)
}

func TestWrittenOntologyReadsBackWithTheSameImports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonld")
	imports := []string{"https://example.com/onto1", "https://example.com/onto2"}
	require.NoError(t, WriteOntology(path, &Ontology{IRI: testBase, Base: testBase, Title: `a "quoted" title`, Imports: imports}))

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, testBase, ontology.IRI)
	require.Equal(t, `a "quoted" title`, ontology.Title)
	require.Equal(t, imports, ontology.Imports)
}

func TestWrittenOntologyReadsBackASalModuleImport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonld")
	module := "salmodule://github.com/adplincinst/sample-salmodule-1"
	require.NoError(t, WriteOntology(path, &Ontology{IRI: testBase, Base: testBase, Title: "sal", Imports: []string{module}}))

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, []string{module}, ontology.Imports)
}

// Writing the ontology node preserves a pinned vocabulary node already in
// .sal/config.jsonld byte for byte, since sal import never inspects it.
func TestWriteOntologyPreservesAnExistingPinnedVocabularyNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonld")
	pinned := json.RawMessage(`{"@id":"https://schema.org/","@type":"owl:Ontology","owl:versionIRI":{"@id":"urn:sha256:abc"}}`)
	require.NoError(t, pkg.WriteConfigDocument(path, &pkg.ConfigDocument{
		Context: map[string]string{"owl": "http://www.w3.org/2002/07/owl#"},
		Graph:   []json.RawMessage{pinned},
	}))

	require.NoError(t, WriteOntology(path, &Ontology{IRI: testBase, Base: testBase, Title: "sal"}))

	doc, err := pkg.ReadConfigDocument(path)
	require.NoError(t, err)
	_, pins, err := pkg.PartitionConfigGraph(doc.Graph)
	require.NoError(t, err)
	require.Len(t, pins, 1)
	require.JSONEq(t, string(pinned), string(pins[0]))
}

func TestWriteOntologyKeepsStatementsTheTemplateDoesNotCover(t *testing.T) {
	path := writeTestOntologyNode(t, `{
  "@context": {
    "dcterms": "http://purl.org/dc/terms/",
    "owl": "http://www.w3.org/2002/07/owl#"
  },
  "@id": ".",
  "@type": "owl:Ontology",
  "dcterms:title": "My Ontology",
  "dcterms:creator": "A Person"
}`)

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	ontology.Imports = append(ontology.Imports, "https://example.com/onto1")
	require.NoError(t, WriteOntology(path, ontology))

	rewritten, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, []string{"https://example.com/onto1"}, rewritten.Imports)
	require.Equal(t, "My Ontology", rewritten.Title)
	creator := rdflibgo.NewURIRefUnsafe(dctermsNamespace + "creator")
	require.True(t, pkg.Contains(rewritten.Graph, rdflibgo.NewURIRefUnsafe(testBase), creator, rdflibgo.NewLiteral("A Person")))
}

func TestWriteOntologyReplacesAnExistingTitleInAFileWithExtraStatements(t *testing.T) {
	path := writeTestOntologyNode(t, `{
  "@context": {
    "dc": "http://purl.org/dc/elements/1.1/",
    "dcterms": "http://purl.org/dc/terms/",
    "owl": "http://www.w3.org/2002/07/owl#"
  },
  "@id": ".",
  "@type": "owl:Ontology",
  "dc:title": "Old Title",
  "dcterms:creator": "A Person"
}`)

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	ontology.Title = "New Title"
	require.NoError(t, WriteOntology(path, ontology))

	rewritten, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, "New Title", rewritten.Title)
	// the one title is the dcterms one; the dc:title it replaced is gone
	var titles int
	for _, title := range []rdflibgo.URIRef{dctermsTitle, dcTitle} {
		pkg.Triples(rewritten.Graph, nil, &title, nil)(func(rdflibgo.Triple) bool {
			titles++
			return true
		})
	}
	require.Equal(t, 1, titles)
	require.True(t, pkg.Contains(rewritten.Graph, rdflibgo.NewURIRefUnsafe(testBase), dctermsTitle, rdflibgo.NewLiteral("New Title")))
}

func TestImportIRIAcceptsAnHTTPSURL(t *testing.T) {
	value, err := importIRI("  <https://schema.org/version/latest/schemaorg-current-https.ttl>  ")
	require.NoError(t, err)
	require.Equal(t, "https://schema.org/version/latest/schemaorg-current-https.ttl", value)
}

func TestImportIRIRejectsSomethingThatIsNeitherAURLNorAnArtifact(t *testing.T) {
	_, err := importIRI("./local/ontology.jsonld")
	require.ErrorContains(t, err, "is not an http or https URL, a salmodule:// reference, or an oci:// reference")
}

func TestImportIRIRecordsASalModuleUnderTheIRINamingIt(t *testing.T) {
	value, err := importIRI("salmodule://github.com/adplincinst/sample-salmodule-1")
	require.NoError(t, err)
	require.Equal(t, "salmodule://github.com/adplincinst/sample-salmodule-1", value)
}

// A module reference may leave the host out, in which case github.com is
// assumed. Recording the resolved form means the same module imported both ways
// is only recorded once.
func TestImportIRIFillsInTheDefaultHostOfASalModule(t *testing.T) {
	value, err := importIRI("  <salmodule://adplincinst/sample-salmodule-1>  ")
	require.NoError(t, err)
	require.Equal(t, "salmodule://github.com/adplincinst/sample-salmodule-1", value)
}

func TestImportIRIRejectsASalModuleThatDoesNotNameARepository(t *testing.T) {
	_, err := importIRI("salmodule://adplincinst")
	require.ErrorContains(t, err, "must be of the form salmodule://[HOST/]OWNER/REPO")
}
