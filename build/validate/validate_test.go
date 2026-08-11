package validate

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func writeTurtleTestFileNamed(t *testing.T, name string, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

// A project pins what it declares rather than only what it happened to use, so
// that adding a term from an already declared prefix does not silently pull in
// whatever version of that vocabulary is being served that day.
func TestPinDeclaredPrefixesPinsAPrefixNoTermUses(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, testVocabularyDocument, nil)
	path := writeTurtleTestFileNamed(t, "unused.ttl", `
		@prefix things: <https://vocab.test/things#> .

		<widgets/1> a <Widget> .
	`)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)
	require.NoError(t, validator.PinDeclaredPrefixes())
	require.NoError(t, pins.Save())

	content, err := os.ReadFile(filepath.Join(projectDir, "ns-prefix-versions.jsonld"))
	require.NoError(t, err)
	require.Contains(t, string(content), testVocabularyNamespace)
}

func TestPinDeclaredPrefixesFailsWhenADeclaredVocabularyCannotBeResolved(t *testing.T) {
	pins := EphemeralVocabularies()
	pins.Fetch = func(string) ([]byte, string, error) {
		return nil, "", fmt.Errorf("bad response status code: 404")
	}
	path := writeTurtleTestFileNamed(t, "missing.ttl", `
		@prefix gone: <https://vocab.test/gone#> .

		<widgets/1> a <Widget> .
	`)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	err = validator.PinDeclaredPrefixes()

	require.Error(t, err)
	require.Contains(t, err.Error(), "https://vocab.test/gone#")
	require.Contains(t, err.Error(), "404")
}

// The XSD built-in datatypes and the project's own terms are checked without a
// vocabulary document, so there is no version of either to pin.
func TestPinDeclaredPrefixesSkipsTheProjectBaseAndXsd(t *testing.T) {
	pins := EphemeralVocabularies()
	pins.Fetch = func(u string) ([]byte, string, error) {
		return nil, "", fmt.Errorf("%s should not have been dereferenced", u)
	}
	path := writeTurtleTestFileNamed(t, "builtins.ttl", `
		@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .
		@prefix self: <`+testBase+`> .

		<widgets/1> self:count "1"^^xsd:integer .
	`)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	require.NoError(t, validator.PinDeclaredPrefixes())
}

// build/vocab holds copies of the vocabularies that cannot be dereferenced
// reliably. What SAL validated against is the copy, so the copy is what the
// project pins.
func TestAVocabularyThatCannotBeFetchedPinsSalsBundledCopy(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, "", nil)
	pins.Fetch = func(string) ([]byte, string, error) {
		return nil, "", fmt.Errorf("bad response status code: 403")
	}
	path := writeTurtleTestFileNamed(t, "schema.ttl", `
		@prefix schema: <https://schema.org/> .

		<person/bob> a schema:Person .
	`)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	content, err := os.ReadFile(filepath.Join(projectDir, "ns-prefix-versions.jsonld"))
	require.NoError(t, err)
	require.Contains(t, string(content), "https://schema.org/")
	require.Len(t, pins.Documents(), 1)
}

// An ontology .sal/ontology.ttl imports is merged at the version the project
// pinned rather than at whatever its source is serving now.
func TestPinnedGraphMergesTheVersionTheProjectPinned(t *testing.T) {
	projectDir := t.TempDir()
	const imported = `@prefix owl: <http://www.w3.org/2002/07/owl#> .
<https://vocab.test/onto#Thing> a owl:Class .
`
	pins := newTestPins(t, projectDir, imported, nil)
	_, err := PinnedGraph(pins, "https://vocab.test/onto")
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	fetches := 0
	reopened := newTestPins(t, projectDir, `@prefix owl: <http://www.w3.org/2002/07/owl#> .
<https://vocab.test/onto#Replaced> a owl:Class .
`, &fetches)
	graph, err := PinnedGraph(reopened, "https://vocab.test/onto")

	require.NoError(t, err)
	require.Zero(t, fetches)
	require.True(t, graph.Contains(
		rdflibgo.NewURIRefUnsafe("https://vocab.test/onto#Thing"),
		rdflibgo.RDF.Type,
		rdflibgo.NewURIRefUnsafe(owlNamespaceIRI+"Class"),
	))
}

func TestOneValidatorResolvesAVocabularyOnceAcrossFiles(t *testing.T) {
	fetches := 0
	pins := EphemeralVocabularies()
	pins.Fetch = func(string) ([]byte, string, error) {
		fetches++
		return []byte(testVocabularyDocument), "text/turtle", nil
	}
	const uses = `
		@prefix things: <https://vocab.test/things#> .

		<widgets/1> a things:Widget .
	`
	first := writeTurtleTestFileNamed(t, "first.ttl", uses)
	second := writeTurtleTestFileNamed(t, "second.ttl", uses)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(first)
	require.NoError(t, err)
	_, err = validator.ValidateFile(second)
	require.NoError(t, err)
	require.NoError(t, validator.PinDeclaredPrefixes())

	require.Equal(t, 1, fetches)
}
