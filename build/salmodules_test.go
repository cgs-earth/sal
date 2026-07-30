package build

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/turtle"
)

const testModuleNamespace = "salmodule://www.github.com/test/history-getter/"

const testModuleOntology = `{
	"@context": {
		"schema": "https://schema.org/",
		"owl": "http://www.w3.org/2002/07/owl#",
		"salmodule": "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
	},
	"@graph": [
		{"@id": ".", "@type": "owl:Ontology"},
		{"@id": "EducationalHistoryFinder", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"}},
		{"@id": "NotATask", "@type": "owl:Class"}
	]
}`

// testProject is the shape of a SAL project that references a SAL module, as
// described in build/testdata/reference/ontology_with_sal.ttl.
const testProject = `
	@base <https://example.test/project/> .
	@prefix history: <salmodule://www.github.com/test/history-getter/> .
	@prefix salmodule: <https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#> .

	<EducationFinder> a history:EducationalHistoryFinder ;
		a salmodule:NodeProcessor ;
		salmodule:taskInstanceEnvVar '{"@id":"https://example.test/project/EducationFinder","@type":"EducationalHistoryFinder"}' .
`

type testContainerRunner struct {
	ontology  string
	runOutput string
	runErr    error
	runs      int
}

func (r *testContainerRunner) BuildImage(context.Context, string, string) error { return nil }

func (r *testContainerRunner) RunContainer(_ context.Context, _ string, _ []string, cmd []string) ([]byte, []byte, error) {
	switch cmd[len(cmd)-1] {
	case salmodule.OntologyCommand:
		return []byte(r.ontology), nil, nil
	case salmodule.RunCommand:
		r.runs++
		return []byte(r.runOutput), nil, r.runErr
	}
	return nil, nil, fmt.Errorf("unexpected command %v", cmd)
}

func testResolver(runner salmodule.ContainerRunner) *salmodule.Resolver {
	return &salmodule.Resolver{
		Runner: runner,
		Command: func(_ context.Context, _ string, _ string, args ...string) error {
			return os.WriteFile(filepath.Join(args[len(args)-1], "Dockerfile"), []byte("FROM scratch\n"), 0644)
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
	require.Contains(t, tasks[0].taskInstance, `"@type":"EducationalHistoryFinder"`)
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
