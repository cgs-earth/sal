package build

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/turtle"
)

const testModuleNamespace = "salmodule://www.github.com/test/history-getter/"

const testModuleCommitHash = "abc123def456abc123def456abc123def456abc"

const testModuleOntology = `{
	"@context": {
		"schema": "https://schema.org/",
		"owl": "http://www.w3.org/2002/07/owl#",
		"xsd": "http://www.w3.org/2001/XMLSchema#",
		"salmodule": "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
	},
	"@graph": [
		{"@id": ".", "@type": "owl:Ontology"},
		{"@id": "EducationalHistoryFinder", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"}},
		{"@id": "maxRetries", "@type": "owl:DatatypeProperty"},
		{"@id": "school", "@type": "owl:ObjectProperty"},
		{"@id": "NotATask", "@type": "owl:Class"}
	]
}`

// testProject is the shape of a SAL project that references a SAL module, as
// described in build/testdata/reference/ontology_with_sal.ttl. The instance is
// configured with the module's own properties rather than an embedded JSON-LD
// literal.
const testProject = `
	@base <https://example.test/project/> .
	@prefix history: <salmodule://www.github.com/test/history-getter/> .
	@prefix salmodule: <https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#> .
	@prefix schema: <https://schema.org/> .
	@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

	<EducationFinder> a history:EducationalHistoryFinder ;
		a salmodule:NodeProcessor ;
		schema:name "not a module property" ;
		history:maxRetries "5"^^xsd:integer .
`

type testContainerRunner struct {
	ontology  string
	runOutput string
	runErr    error
	runs      int
	// runEnv is the environment the run command was last invoked with, which is
	// how the task instance reaches the module.
	runEnv []string
}

func (r *testContainerRunner) BuildImage(context.Context, string, string) error { return nil }

func (r *testContainerRunner) RunContainer(_ context.Context, _ string, env []string, cmd []string) ([]byte, []byte, error) {
	switch cmd[len(cmd)-1] {
	case salmodule.OntologyCommand:
		return []byte(r.ontology), nil, nil
	case salmodule.RunCommand:
		r.runs++
		r.runEnv = env
		return []byte(r.runOutput), nil, r.runErr
	}
	return nil, nil, fmt.Errorf("unexpected command %v", cmd)
}

func testResolver(runner salmodule.ContainerRunner) *salmodule.Resolver {
	return &salmodule.Resolver{
		Runner: runner,
		Command: func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
			if args[0] == "rev-parse" {
				return []byte(testModuleCommitHash), nil
			}
			return nil, os.WriteFile(filepath.Join(args[len(args)-1], "Dockerfile"), []byte("FROM scratch\n"), 0644)
		},
	}
}

func parseTestProject(t *testing.T, content string) *rdflibgo.Graph {
	t.Helper()

	graph := rdflibgo.NewGraph(rdflibgo.WithBase("https://example.test/project/"))
	require.NoError(t, turtle.Parse(graph, bytes.NewReader([]byte(content)), turtle.WithBase("https://example.test/project/")))
	return graph
}

func TestFindSalModuleTasksReadsTaskInstances(t *testing.T) {
	tasks, err := findSalModuleTasks(parseTestProject(t, testProject))

	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, testModuleNamespace+"EducationalHistoryFinder", tasks[0].classIRI)
	require.Equal(t, "https://www.github.com/test/history-getter.git", tasks[0].ref.CloneURL)
	require.True(t, tasks[0].declaredTask)
}

func TestFindSalModuleTasksIgnoresGraphsWithoutModules(t *testing.T) {
	tasks, err := findSalModuleTasks(parseTestProject(t, `
		@prefix schema: <https://schema.org/> .

		<https://example.test/person/bob> a schema:Person ;
			schema:name "Bob" .
	`))

	require.NoError(t, err)
	require.Empty(t, tasks)
}

func TestMaterializeSalModulesMergesTaskOutput(t *testing.T) {
	graph := parseTestProject(t, testProject)
	runner := &testContainerRunner{
		ontology:  testModuleOntology,
		runOutput: `{"@id":"https://example.test/person/bob","@type":"schema:Person","schema:name":"Bob"}`,
	}

	err := MaterializeSalModules(context.Background(), graph, testResolver(runner))

	require.NoError(t, err)
	require.Equal(t, 1, runner.runs)
	require.True(t, graphHasTriple(graph, "https://example.test/person/bob", "https://schema.org/name", "Bob"))
}

// TestMaterializeSalModulesPassesTheInstanceConfiguredInRDF checks that the task
// instance the module receives is built from the instance's RDF properties, and
// that only the properties the module's own vocabulary defines are passed on.
func TestMaterializeSalModulesPassesTheInstanceConfiguredInRDF(t *testing.T) {
	runner := &testContainerRunner{ontology: testModuleOntology}

	err := MaterializeSalModules(context.Background(), parseTestProject(t, testProject), testResolver(runner))

	require.NoError(t, err)
	require.Len(t, runner.runEnv, 1)
	name, instance, found := strings.Cut(runner.runEnv[0], "=")
	require.True(t, found)
	require.Equal(t, salmodule.DefaultTaskInstanceEnvVar, name)
	require.JSONEq(t, `{
		"@id": "https://example.test/project/EducationFinder",
		"@type": "EducationalHistoryFinder",
		"maxRetries": {"@value": "5", "@type": "xsd:integer"}
	}`, instance)
}

func TestMaterializeSalModulesSkipsClassesThatAreNotTasks(t *testing.T) {
	graph := parseTestProject(t, `
		@base <https://example.test/project/> .
		@prefix history: <salmodule://www.github.com/test/history-getter/> .

		<SomeReference> a history:NotATask .
	`)
	runner := &testContainerRunner{ontology: testModuleOntology}

	err := MaterializeSalModules(context.Background(), graph, testResolver(runner))

	require.NoError(t, err)
	require.Equal(t, 0, runner.runs)
}

func TestMaterializeSalModulesFailsWhenTheModuleReportsAnError(t *testing.T) {
	graph := parseTestProject(t, testProject)
	runner := &testContainerRunner{
		ontology:  testModuleOntology,
		runOutput: `{"@type":"salmodule:Error","rdfs:comment":"reference feature server is unreachable"}`,
	}

	err := MaterializeSalModules(context.Background(), graph, testResolver(runner))

	require.Error(t, err)
	require.Contains(t, err.Error(), "reference feature server is unreachable")
}

func TestMaterializeSalModulesPrefersTheModuleErrorOverTheContainerExitStatus(t *testing.T) {
	graph := parseTestProject(t, testProject)
	// a task reports why it failed on stdout and then exits non-zero
	runner := &testContainerRunner{
		ontology:  testModuleOntology,
		runOutput: `{"@type":"salmodule:Error","rdfs:comment":"reference feature server is unreachable"}`,
		runErr:    fmt.Errorf("container exited with status 1"),
	}

	err := MaterializeSalModules(context.Background(), graph, testResolver(runner))

	require.Error(t, err)
	require.Contains(t, err.Error(), "reference feature server is unreachable")
}

func TestMaterializeSalModulesReportsContainerFailuresWithoutModuleErrors(t *testing.T) {
	graph := parseTestProject(t, testProject)
	runner := &testContainerRunner{
		ontology: testModuleOntology,
		runErr:   fmt.Errorf("container exited with status 137"),
	}

	err := MaterializeSalModules(context.Background(), graph, testResolver(runner))

	require.Error(t, err)
	require.Contains(t, err.Error(), "container exited with status 137")
}

func graphHasTriple(graph *rdflibgo.Graph, subject, predicate, object string) bool {
	found := false
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if triple.Subject.String() != subject || triple.Predicate.Value() != predicate {
			return true
		}
		if literal, ok := triple.Object.(rdflibgo.Literal); ok && literal.Lexical() == object {
			found = true
		}
		return true
	})
	return found
}
