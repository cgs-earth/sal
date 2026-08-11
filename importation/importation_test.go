package importation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const testBase = "https://github.com/cgs-earth/sal/"

func writeTestOntology(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ontology.jsonld")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestReadOntologyReturnsNilWhenTheFileIsMissing(t *testing.T) {
	ontology, err := ReadOntology(filepath.Join(t.TempDir(), "ontology.jsonld"), testBase)
	require.NoError(t, err)
	require.Nil(t, ontology)
}

func TestReadOntologyResolvesTheRelativeOntologyIRIAgainstTheProjectBase(t *testing.T) {
	path := writeTestOntology(t, `{
  "@context": {
    "dc": "http://purl.org/dc/elements/1.1/",
    "owl": "http://www.w3.org/2002/07/owl#"
  },
  "@id": ".",
  "@type": "owl:Ontology",
  "dc:title": "My Ontology",
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
	path := filepath.Join(t.TempDir(), "ontology.jsonld")

	require.NoError(t, writeOntology(path, &Ontology{
		IRI:     testBase,
		Base:    testBase,
		Title:   "My Ontology",
		Imports: []string{"https://example.com/onto1", "https://example.com/onto2"},
	}))

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{
  "@context": {
    "dc": "http://purl.org/dc/elements/1.1/",
    "owl": "http://www.w3.org/2002/07/owl#"
  },
  "@id": ".",
  "@type": "owl:Ontology",
  "dc:title": "My Ontology",
  "owl:imports": [
    { "@id": "https://example.com/onto1" },
    { "@id": "https://example.com/onto2" }
  ]
}`, string(content))
}

func TestWriteOntologyOmitsAnEmptyImportsClause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ontology.jsonld")

	require.NoError(t, writeOntology(path, &Ontology{IRI: testBase, Base: testBase, Title: "sal"}))

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{
  "@context": {
    "dc": "http://purl.org/dc/elements/1.1/",
    "owl": "http://www.w3.org/2002/07/owl#"
  },
  "@id": ".",
  "@type": "owl:Ontology",
  "dc:title": "sal"
}`, string(content))
}

func TestWrittenOntologyReadsBackWithTheSameImports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ontology.jsonld")
	imports := []string{"https://example.com/onto1", "https://example.com/onto2"}
	require.NoError(t, writeOntology(path, &Ontology{IRI: testBase, Base: testBase, Title: `a "quoted" title`, Imports: imports}))

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, testBase, ontology.IRI)
	require.Equal(t, `a "quoted" title`, ontology.Title)
	require.Equal(t, imports, ontology.Imports)
}

func TestWrittenOntologyReadsBackASalModuleImport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ontology.jsonld")
	module := "salmodule://github.com/adplincinst/sample-salmodule-1"
	require.NoError(t, writeOntology(path, &Ontology{IRI: testBase, Base: testBase, Title: "sal", Imports: []string{module}}))

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, []string{module}, ontology.Imports)
}

func TestWriteOntologyKeepsStatementsTheTemplateDoesNotCover(t *testing.T) {
	path := writeTestOntology(t, `{
  "@context": {
    "dc": "http://purl.org/dc/elements/1.1/",
    "owl": "http://www.w3.org/2002/07/owl#"
  },
  "@id": ".",
  "@type": "owl:Ontology",
  "dc:title": "My Ontology",
  "dc:creator": "A Person"
}`)

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	ontology.Imports = append(ontology.Imports, "https://example.com/onto1")
	require.NoError(t, writeOntology(path, ontology))

	rewritten, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, []string{"https://example.com/onto1"}, rewritten.Imports)
	require.Equal(t, "My Ontology", rewritten.Title)
	creator := rdflibgo.NewURIRefUnsafe(dcNamespace + "creator")
	require.True(t, rewritten.Graph.Contains(rdflibgo.NewURIRefUnsafe(testBase), creator, rdflibgo.NewLiteral("A Person")))
}

func TestWriteOntologyReplacesAnExistingTitleInAFileWithExtraStatements(t *testing.T) {
	path := writeTestOntology(t, `{
  "@context": {
    "dc": "http://purl.org/dc/elements/1.1/",
    "owl": "http://www.w3.org/2002/07/owl#"
  },
  "@id": ".",
  "@type": "owl:Ontology",
  "dc:title": "Old Title",
  "dc:creator": "A Person"
}`)

	ontology, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	ontology.Title = "New Title"
	require.NoError(t, writeOntology(path, ontology))

	rewritten, err := ReadOntology(path, testBase)
	require.NoError(t, err)
	require.Equal(t, "New Title", rewritten.Title)
	var titles int
	title := dcTitle
	rewritten.Graph.Triples(nil, &title, nil)(func(rdflibgo.Triple) bool {
		titles++
		return true
	})
	require.Equal(t, 1, titles)
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
