package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const testModuleCommitHash = "abc123def456abc123def456abc123def456abc"

const testModuleOntology = `{
	"@context": {
		"owl": "http://www.w3.org/2002/07/owl#",
		"salmodule": "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
	},
	"@graph": [
		{"@id": "EducationalHistoryFinder", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"}},
		{"@id": "maxRetries", "@type": "owl:DatatypeProperty"}
	]
}`

type testModuleRunner struct{}

func (testModuleRunner) BuildImage(context.Context, string, string) error { return nil }

func (testModuleRunner) ImageExists(context.Context, string) (bool, error) { return false, nil }

func (testModuleRunner) RunContainer(_ context.Context, _ string, _ []string, cmd []string) ([]byte, []byte, error) {
	if cmd[len(cmd)-1] == salmodule.OntologyCommand {
		return []byte(testModuleOntology), nil, nil
	}
	return nil, nil, fmt.Errorf("unexpected command %v", cmd)
}

// useFakeSalModule points the shared module resolver at test doubles so that
// dereferencing a salmodule:// vocabulary neither clones a repository nor runs a
// container.
func useFakeSalModule(t *testing.T) {
	t.Helper()

	resolver := salmodule.Default()
	originalRunner, originalCommand := resolver.Runner, resolver.Command
	resolver.Reset()
	resolver.Runner = testModuleRunner{}
	resolver.Command = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if args[0] == "rev-parse" {
			return []byte(testModuleCommitHash), nil
		}
		return nil, os.WriteFile(filepath.Join(args[len(args)-1], "Dockerfile"), []byte("FROM scratch\n"), 0644)
	}

	t.Cleanup(func() {
		resolver.Runner, resolver.Command = originalRunner, originalCommand
		resolver.Reset()
	})
}

func TestValidateAcceptsTermsDefinedBySalModuleOntology(t *testing.T) {
	useFakeSalModule(t)
	path := writeTurtleTestFile(t, `
		@prefix history: <salmodule://www.github.com/test/history-getter/> .
		@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

		<EducationFinder> a history:EducationalHistoryFinder ;
			history:maxRetries "5"^^xsd:integer .
	`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.NoError(t, err)
}

// The properties a task instance is configured with come from the module's own
// vocabulary, so a typo in one is reported like a typo in any other term.
func TestValidateChecksTaskConfigurationPropertiesAgainstTheModuleOntology(t *testing.T) {
	useFakeSalModule(t)
	path := writeTurtleTestFile(t, `
		@prefix history: <salmodule://www.github.com/test/history-getter/> .
		@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

		<EducationFinder> a history:EducationalHistoryFinder ;
			history:maxRetriess "5"^^xsd:integer .
	`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "history:maxRetriess")
}

// `sal import` records a module under the IRI naming it, which is its
// vocabulary base without the trailing slash. The ontology it publishes still
// has to parse against the base, or none of its terms would match the ones a
// project references.
func TestPinnedGraphResolvesAModuleOntologyAgainstTheModuleNamespace(t *testing.T) {
	useFakeSalModule(t)

	graph, err := PinnedGraph(EphemeralVocabularies(), "salmodule://www.github.com/test/history-getter")

	require.NoError(t, err)
	require.True(t, graph.Contains(
		rdflibgo.NewURIRefUnsafe("salmodule://www.github.com/test/history-getter/EducationalHistoryFinder"),
		rdflibgo.RDF.Type,
		rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"),
	))
}

// A salmodule:// vocabulary is pinned by the git commit hash of the module
// repository it was built from, not by the digest of its ontology document,
// since code the module runs can change without the ontology itself changing.
func TestPinnedGraphPinsASalModuleVocabularyByItsCommitHash(t *testing.T) {
	useFakeSalModule(t)
	pins := EphemeralVocabularies()

	_, err := PinnedGraph(pins, "salmodule://www.github.com/test/history-getter")
	require.NoError(t, err)

	entry, ok := pins.entries["salmodule://www.github.com/test/history-getter"]
	require.True(t, ok)
	require.Equal(t, gitCommitVersionScheme, entry.Scheme)
	require.Equal(t, testModuleCommitHash, entry.Version)
}

func TestValidateRejectsTermsMissingFromSalModuleOntology(t *testing.T) {
	useFakeSalModule(t)
	path := writeTurtleTestFile(t, `
		@prefix history: <salmodule://www.github.com/test/history-getter/> .

		<EducationFinder> a history:EducationalHistoryFinderr .
	`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "history:EducationalHistoryFinderr")
}
