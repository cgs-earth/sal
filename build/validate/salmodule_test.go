package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
)

const testModuleOntology = `{
	"@context": {
		"owl": "http://www.w3.org/2002/07/owl#",
		"salmodule": "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
	},
	"@graph": [
		{"@id": "EducationalHistoryFinder", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"}}
	]
}`

type testModuleRunner struct{}

func (testModuleRunner) BuildImage(context.Context, string, string) error { return nil }

func (testModuleRunner) RunContainer(_ context.Context, _ string, _ []string, cmd []string) ([]byte, []byte, error) {
	if cmd[len(cmd)-1] == salmodule.OntologyCommand {
		return []byte(testModuleOntology), nil, nil
	}
	return nil, nil, fmt.Errorf("unexpected command %v", cmd)
}

// useFakeSalModule points the shared module resolver and the vocabulary cache at
// test doubles so that dereferencing a salmodule:// vocabulary neither clones a
// repository nor runs a container.
func useFakeSalModule(t *testing.T) {
	t.Helper()

	cacheDir := filepath.Join(t.TempDir(), "sal", "cache")
	originalCacheRootDir := cacheRootDir
	cacheRootDir = func() string { return cacheDir }

	resolver := salmodule.Default()
	originalRunner, originalCommand := resolver.Runner, resolver.Command
	resolver.Reset()
	resolver.Runner = testModuleRunner{}
	resolver.Command = func(_ context.Context, _ string, _ string, args ...string) error {
		return os.WriteFile(filepath.Join(args[len(args)-1], "Dockerfile"), []byte("FROM scratch\n"), 0644)
	}

	t.Cleanup(func() {
		cacheRootDir = originalCacheRootDir
		resolver.Runner, resolver.Command = originalRunner, originalCommand
		resolver.Reset()
	})
}

func TestValidateAcceptsTermsDefinedBySalModuleOntology(t *testing.T) {
	useFakeSalModule(t)
	path := writeTurtleTestFile(t, `
		@prefix history: <salmodule://www.github.com/test/history-getter/> .

		<EducationFinder> a history:EducationalHistoryFinder .
	`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.NoError(t, err)
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
