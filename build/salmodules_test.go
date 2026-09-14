package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/turtle"
)

const testModuleNamespace = "salmodule://www.github.com/test/history-getter/"

const testModuleCommitHash = "abc123def456abc123def456abc123def456abc"

// testFileModified is when the fake container reports its files were written.
var testFileModified = time.Date(2026, 9, 10, 14, 26, 5, 0, time.UTC)

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
	// containerFiles are the files, by absolute path, a task can name for copying.
	containerFiles map[string]string
}

// CopyFile serves the fake container's files.
func (r *testContainerRunner) CopyFile(_ context.Context, path string, w io.Writer) (time.Time, error) {
	content, ok := r.containerFiles[path]
	if !ok {
		return time.Time{}, fmt.Errorf("no such file %s", path)
	}
	_, err := io.WriteString(w, content)
	return testFileModified, err
}

func (r *testContainerRunner) BuildImage(context.Context, string, string) error { return nil }

func (r *testContainerRunner) ImageExists(context.Context, string) (bool, error) { return false, nil }

func (r *testContainerRunner) RunContainer(ctx context.Context, _ string, env []string, cmd []string, consume salmodule.ContainerOutputConsumer) ([]byte, error) {
	switch cmd[len(cmd)-1] {
	case salmodule.OntologyCommand:
		return nil, consume(ctx, strings.NewReader(r.ontology), r)
	case salmodule.RunCommand:
		r.runs++
		r.runEnv = env
		if err := consume(ctx, strings.NewReader(r.runOutput), r); err != nil {
			return nil, err
		}
		return nil, r.runErr
	}
	return nil, fmt.Errorf("unexpected command %v", cmd)
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

	tasksRun, err := MaterializeSalModules(context.Background(), graph, testResolver(runner), t.TempDir())

	require.NoError(t, err)
	require.Equal(t, 1, tasksRun)
	require.Equal(t, 1, runner.runs)
	require.True(t, graphHasTriple(graph, "https://example.test/person/bob", "https://schema.org/name", "Bob"))
}

// TestMaterializeSalModulesNamesRelativeOutputUnderTheProject checks that a
// relative IRI a task emits resolves against the project namespace rather than
// the module's, since what a task produces is the project's own instance data.
func TestMaterializeSalModulesNamesRelativeOutputUnderTheProject(t *testing.T) {
	graph := parseTestProject(t, testProject)
	runner := &testContainerRunner{
		ontology:  testModuleOntology,
		runOutput: `{"@id":"person/bob","@type":"schema:Person","schema:name":"Bob"}`,
	}

	_, err := MaterializeSalModules(context.Background(), graph, testResolver(runner), t.TempDir())

	require.NoError(t, err)
	require.True(t, graphHasTriple(graph, "https://example.test/project/person/bob", "https://schema.org/name", "Bob"))
	require.False(t, graphHasTriple(graph, testModuleNamespace+"person/bob", "https://schema.org/name", "Bob"))
}

// TestMaterializeSalModulesPassesTheInstanceConfiguredInRDF checks that the task
// instance the module receives is built from the instance's RDF properties, and
// that only the properties the module's own vocabulary defines are passed on.
func TestMaterializeSalModulesPassesTheInstanceConfiguredInRDF(t *testing.T) {
	runner := &testContainerRunner{ontology: testModuleOntology}

	_, err := MaterializeSalModules(context.Background(), parseTestProject(t, testProject), testResolver(runner), t.TempDir())

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

	tasksRun, err := MaterializeSalModules(context.Background(), graph, testResolver(runner), t.TempDir())

	require.NoError(t, err)
	require.Equal(t, 0, tasksRun)
	require.Equal(t, 0, runner.runs)
}

func TestMaterializeSalModulesFailsWhenTheModuleReportsAnError(t *testing.T) {
	graph := parseTestProject(t, testProject)
	runner := &testContainerRunner{
		ontology:  testModuleOntology,
		runOutput: `{"@type":"salmodule:Error","rdfs:comment":"reference feature server is unreachable"}`,
	}

	_, err := MaterializeSalModules(context.Background(), graph, testResolver(runner), t.TempDir())

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

	_, err := MaterializeSalModules(context.Background(), graph, testResolver(runner), t.TempDir())

	require.Error(t, err)
	require.Contains(t, err.Error(), "reference feature server is unreachable")
}

func TestMaterializeSalModulesReportsContainerFailuresWithoutModuleErrors(t *testing.T) {
	graph := parseTestProject(t, testProject)
	runner := &testContainerRunner{
		ontology: testModuleOntology,
		runErr:   fmt.Errorf("container exited with status 137"),
	}

	_, err := MaterializeSalModules(context.Background(), graph, testResolver(runner), t.TempDir())

	require.Error(t, err)
	require.Contains(t, err.Error(), "container exited with status 137")
}

// TestMaterializeSalModulesCopiesTheFilesATaskNames checks that a file a task
// names with a file:/// IRI object is copied into the blob store under its
// digest, that the graph refers to the copy rather than the container path,
// and that the copy's name is recorded.
func TestMaterializeSalModulesCopiesTheFilesATaskNames(t *testing.T) {
	graph := parseTestProject(t, testProject)
	blobDir := filepath.Join(t.TempDir(), "blobs")
	runner := &testContainerRunner{
		ontology:       testModuleOntology,
		runOutput:      `{"@id":"https://example.test/dataset","schema:hasPart":{"@id":"file:///tmp/test.txt"}}`,
		containerFiles: map[string]string{"/tmp/test.txt": "hello\n"},
	}

	tasksRun, err := MaterializeSalModules(context.Background(), graph, testResolver(runner), blobDir)

	require.NoError(t, err)
	require.Equal(t, 1, tasksRun)
	digest := sha256.Sum256([]byte("hello\n"))
	copyIRI := "urn:sha256:" + hex.EncodeToString(digest[:])
	require.FileExists(t, filepath.Join(blobDir, hex.EncodeToString(digest[:])))
	require.True(t, graphHasTriple(graph, copyIRI, "http://www.w3.org/2000/01/rdf-schema#label", "test.txt"))
	require.True(t, graphHasTriple(graph, copyIRI, "http://purl.org/dc/terms/modified", "2026-09-10T14:26:05Z"))
	var partIRIs []string
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if triple.Predicate.Value() == "https://schema.org/hasPart" {
			partIRIs = append(partIRIs, triple.Object.String())
		}
		return true
	})
	require.Equal(t, []string{copyIRI}, partIRIs)
}

func TestMaterializeSalModulesFailsWhenANamedFileIsMissingFromTheContainer(t *testing.T) {
	graph := parseTestProject(t, testProject)
	runner := &testContainerRunner{
		ontology:  testModuleOntology,
		runOutput: `{"@id":"https://example.test/dataset","schema:hasPart":{"@id":"file:///tmp/missing.txt"}}`,
	}

	_, err := MaterializeSalModules(context.Background(), graph, testResolver(runner), t.TempDir())

	require.Error(t, err)
	require.Contains(t, err.Error(), "no such file /tmp/missing.txt")
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
