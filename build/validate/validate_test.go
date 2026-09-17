package validate

import (
	"context"
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
	_, err := validator.ValidateFile(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, validator.PinDeclaredPrefixes(context.Background()))
	require.NoError(t, pins.Save())

	content, err := os.ReadFile(filepath.Join(projectDir, "config.jsonld"))
	require.NoError(t, err)
	require.Contains(t, string(content), testVocabularyNamespace)
}

func TestPinDeclaredPrefixesFailsWhenADeclaredVocabularyCannotBeResolved(t *testing.T) {
	pins := EphemeralVocabularies()
	pins.Fetch = func(context.Context, string) ([]byte, string, PinnedVersion, error) {
		return nil, "", PinnedVersion{}, fmt.Errorf("bad response status code: 404")
	}
	path := writeTurtleTestFileNamed(t, "missing.ttl", `
		@prefix gone: <https://vocab.test/gone#> .

		<widgets/1> a <Widget> .
	`)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(context.Background(), path)
	require.NoError(t, err)

	err = validator.PinDeclaredPrefixes(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "https://vocab.test/gone#")
	require.Contains(t, err.Error(), "404")
}

// The XSD built-in datatypes and the project's own terms are checked without a
// vocabulary document, so there is no version of either to pin.
func TestPinDeclaredPrefixesSkipsTheProjectBaseAndXsd(t *testing.T) {
	pins := EphemeralVocabularies()
	pins.Fetch = func(_ context.Context, u string) ([]byte, string, PinnedVersion, error) {
		return nil, "", PinnedVersion{}, fmt.Errorf("%s should not have been dereferenced", u)
	}
	path := writeTurtleTestFileNamed(t, "builtins.ttl", `
		@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .
		@prefix self: <`+testBase+`> .

		<widgets/1> self:count "1"^^xsd:integer .
	`)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(context.Background(), path)
	require.NoError(t, err)

	require.NoError(t, validator.PinDeclaredPrefixes(context.Background()))
}

// The schema.org namespace answers with an HTML page whatever is asked for, so
// the vocabulary is fetched from the release document schema.org publishes and
// pinned against the namespace the project declared.
func TestSchemaOrgIsResolvedFromItsReleaseDocument(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, "", nil)
	var requested []string
	pins.Fetch = func(_ context.Context, u string) ([]byte, string, PinnedVersion, error) {
		requested = append(requested, u)
		return []byte(testSchemaOrgVocabulary), "text/turtle", PinnedVersion{}, nil
	}
	path := writeTurtleTestFileNamed(t, "schema.ttl", `
		@prefix schema: <https://schema.org/> .

		<person/bob> a schema:Person .
	`)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	require.Equal(t, []string{"https://schema.org/version/latest/schemaorg-current-https.jsonld"}, requested)
	content, err := os.ReadFile(filepath.Join(projectDir, "config.jsonld"))
	require.NoError(t, err)
	require.Contains(t, string(content), `"@id": "https://schema.org/"`)
	require.Len(t, pins.Documents(), 1)
}

// There is no bundled copy to fall back on: a fetched document sal cannot read
// fails the term, and is not pinned as the version the project validated against.
func TestAFetchedVocabularyThatCannotBeParsedIsAnErrorAndIsNotPinned(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, "", nil)
	pins.Fetch = func(context.Context, string) ([]byte, string, PinnedVersion, error) {
		return []byte("this is not turtle"), "text/turtle", PinnedVersion{}, nil
	}
	path := writeTurtleTestFileNamed(t, "schema.ttl", `
		@prefix schema: <https://schema.org/> .

		<person/bob> a schema:Person .
	`)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(context.Background(), path)

	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported vocabulary serialization")
	require.Empty(t, pins.IDs())
}

// An ontology the config file's ontology node imports is merged at the version the project
// pinned rather than at whatever its source is serving now.
func TestPinnedGraphMergesTheVersionTheProjectPinned(t *testing.T) {
	projectDir := t.TempDir()
	const imported = `@prefix owl: <http://www.w3.org/2002/07/owl#> .
<https://vocab.test/onto#Thing> a owl:Class .
`
	pins := newTestPins(t, projectDir, imported, nil)
	_, err := PinnedGraph(context.Background(), pins, "https://vocab.test/onto")
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	fetches := 0
	reopened := newTestPins(t, projectDir, `@prefix owl: <http://www.w3.org/2002/07/owl#> .
<https://vocab.test/onto#Replaced> a owl:Class .
`, &fetches)
	graph, err := PinnedGraph(context.Background(), reopened, "https://vocab.test/onto")

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
	pins.Fetch = func(context.Context, string) ([]byte, string, PinnedVersion, error) {
		fetches++
		return []byte(testVocabularyDocument), "text/turtle", PinnedVersion{}, nil
	}
	const uses = `
		@prefix things: <https://vocab.test/things#> .

		<widgets/1> a things:Widget .
	`
	first := writeTurtleTestFileNamed(t, "first.ttl", uses)
	second := writeTurtleTestFileNamed(t, "second.ttl", uses)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(context.Background(), first)
	require.NoError(t, err)
	_, err = validator.ValidateFile(context.Background(), second)
	require.NoError(t, err)
	require.NoError(t, validator.PinDeclaredPrefixes(context.Background()))

	require.Equal(t, 1, fetches)
}

// A namespace ending in a fragment is served by the document without it, so
// reading the pinned graph of a namespace dereferences the same URL validation
// did rather than the namespace itself.
func TestPinnedGraphDereferencesANamespaceThroughItsDocumentURL(t *testing.T) {
	var requested []string
	pins := EphemeralVocabularies()
	pins.Fetch = func(_ context.Context, source string) ([]byte, string, PinnedVersion, error) {
		requested = append(requested, source)
		return []byte(testVocabularyDocument), "text/turtle", PinnedVersion{}, nil
	}

	graph, err := PinnedGraph(context.Background(), pins, testVocabularyNamespace)

	require.NoError(t, err)
	require.Equal(t, []string{"https://vocab.test/things"}, requested)
	require.True(t, graph.Contains(
		rdflibgo.NewURIRefUnsafe(testVocabularyNamespace+"Widget"),
		rdflibgo.RDF.Type,
		rdflibgo.NewURIRefUnsafe(owlNamespaceIRI+"Class"),
	))
}

// A vocabulary that cannot be checked at all fails every term used from it in
// the same way, so it is reported once, at its first use, with the other lines
// it affects listed, rather than once per use.
func TestAVocabularyThatCannotBeCheckedIsReportedOnce(t *testing.T) {
	pins := EphemeralVocabularies()
	pins.Fetch = func(context.Context, string) ([]byte, string, PinnedVersion, error) {
		return nil, "", PinnedVersion{}, fmt.Errorf("bad response status code: 404")
	}
	path := writeTurtleTestFileNamed(t, "gone.ttl", `
		@prefix gone: <https://vocab.test/gone#> .

		<widgets/1> a gone:Widget ;
			gone:size "large" ;
			gone:color "red" .
		<widgets/2> a gone:Widget ;
			gone:size "small" .
	`)

	validator := NewValidator(pins, testBase, nil)
	_, err := validator.ValidateFile(context.Background(), path)

	require.Error(t, err)
	var errs MultiError
	require.ErrorAs(t, err, &errs)
	require.Len(t, errs, 1)
	require.Equal(t, path+":4: failed to check vocabulary for gone:Widget: bad response status code: 404 (also affects lines 5, 6, 7, 8)", errs[0].Error())
}
