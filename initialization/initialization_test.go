package initialization

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
)

// newGitRepo creates a git repository with an https remote in a temp directory,
// changes into it, and points HOME at a second temp directory so that init's
// user-level files never touch the real home.
func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q")
	run("remote", "add", "origin", "https://github.com/cgs-earth/example.git")
	run("config", "user.name", "Ada Lovelace")
	t.Setenv("HOME", t.TempDir())
	t.Chdir(dir)
	return dir
}

// answerPrompts feeds the sample task prompts the given lines instead of
// the terminal for the rest of the test.
func answerPrompts(t *testing.T, lines ...string) {
	t.Helper()
	previous := stdin
	stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	t.Cleanup(func() { stdin = previous })
}

// readOntology parses the ontology.jsonld init wrote into dir.
func readOntology(t *testing.T, dir string) (map[string]any, []map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "ontology.jsonld"))
	require.NoError(t, err)
	var doc struct {
		Context map[string]any   `json:"@context"`
		Graph   []map[string]any `json:"@graph"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	return doc.Context, doc.Graph
}

func TestInitWithoutSalModuleWritesNoModuleFiles(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, (&InitCmd{}).Run())

	require.DirExists(t, filepath.Join(dir, ".sal", "data"))
	require.NoFileExists(t, filepath.Join(dir, "ontology.jsonld"))
	require.NoFileExists(t, filepath.Join(dir, "Dockerfile"))
	require.NoFileExists(t, filepath.Join(dir, ".dockerignore"))
}

func TestInitSalModuleBareScaffoldsModuleFiles(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	require.DirExists(t, filepath.Join(dir, ".sal", "data"))

	dockerfile, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	require.Equal(t, "# When invoked this Dockerfile should stream JSON-LD to standard out in accordance with the salmodule specification.\n", string(dockerfile))

	dockerignore, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	require.NoError(t, err)
	require.Equal(t, "# Ignore any files that should not be packaged into the salmodule container ran by the SAL cli.\n", string(dockerignore))

	context, graph := readOntology(t, dir)
	require.Equal(t, map[string]any{
		"dc":        "http://purl.org/dc/elements/1.1/",
		"owl":       "http://www.w3.org/2002/07/owl#",
		"rdfs":      "http://www.w3.org/2000/01/rdf-schema#",
		"salmodule": salmodule.Namespace,
	}, context)
	require.Len(t, graph, 2)

	ontology := graph[0]
	require.Equal(t, "owl:Ontology", ontology["@type"])
	require.Equal(t, ".", ontology["@id"])
	require.Equal(t, "Ada Lovelace", ontology["dc:creator"])
	require.Equal(t, "example", ontology["dc:title"])
	require.Equal(t, "", ontology["owl:versionInfo"])
	require.Equal(t, "", ontology["rdfs:label"])

	task := graph[1]
	require.Equal(t, "owl:Class", task["@type"])
	require.Equal(t, "", task["@id"])
	require.Equal(t, "", task["rdfs:label"])
	require.Equal(t, "", task["rdfs:comment"])
	require.Equal(t, map[string]any{"@id": "salmodule:Task"}, task["rdfs:subClassOf"])
	require.Equal(t, map[string]any{}, task["salmodule:taskShape"])
}

func TestInitSalModulePromptsFillInSampleTask(t *testing.T) {
	dir := newGitRepo(t)
	answerPrompts(t, "FetchStations", "Fetch weather stations", "Fetches every station in a region")
	require.NoError(t, (&InitCmd{SalModule: true}).Run())

	context, graph := readOntology(t, dir)
	require.Equal(t, "http://www.w3.org/ns/shacl#", context["sh"])

	task := graph[1]
	require.Equal(t, "FetchStations", task["@id"])
	require.Equal(t, "owl:Class", task["@type"])
	require.Equal(t, "Fetch weather stations", task["rdfs:label"])
	require.Equal(t, "Fetches every station in a region", task["rdfs:comment"])
	require.Equal(t, map[string]any{"@id": "salmodule:Task"}, task["rdfs:subClassOf"])
	require.Equal(t, map[string]any{
		"@type":          "sh:NodeShape",
		"sh:targetClass": map[string]any{"@id": "FetchStations"},
		"sh:property":    []any{},
	}, task["salmodule:taskShape"])
}

func TestInitSalModuleRejectsTaskIDWithSpacesOrSpecialCharacters(t *testing.T) {
	dir := newGitRepo(t)
	answerPrompts(t, "fetch stations", "fetch-stations", "1stTask", "FetchStations", "Fetch weather stations", "Fetches stations")
	require.NoError(t, (&InitCmd{SalModule: true}).Run())

	_, graph := readOntology(t, dir)
	require.Equal(t, "FetchStations", graph[1]["@id"])
	require.Equal(t, "Fetch weather stations", graph[1]["rdfs:label"])
	require.Equal(t, "Fetches stations", graph[1]["rdfs:comment"])
}

func TestInitSalModuleWithoutInputLeavesTaskBlank(t *testing.T) {
	dir := newGitRepo(t)
	previous := stdin
	stdin = strings.NewReader("")
	t.Cleanup(func() { stdin = previous })
	require.NoError(t, (&InitCmd{SalModule: true}).Run())

	context, graph := readOntology(t, dir)
	require.NotContains(t, context, "sh")
	require.Equal(t, "", graph[1]["@id"])
	require.Equal(t, map[string]any{}, graph[1]["salmodule:taskShape"])
}

func TestInitSalModuleWithoutGitUserLeavesCreatorBlank(t *testing.T) {
	dir := newGitRepo(t)
	unset := exec.Command("git", "config", "--unset", "user.name")
	unset.Dir = dir
	require.NoError(t, unset.Run())

	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	_, graph := readOntology(t, dir)
	require.Equal(t, "", graph[0]["dc:creator"])
	require.Equal(t, "example", graph[0]["dc:title"])
}

func TestInitSalModuleLeavesExistingFilesAlone(t *testing.T) {
	dir := newGitRepo(t)
	existing := "FROM python:3.11-slim\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(existing), 0644))

	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	dockerfile, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	require.Equal(t, existing, string(dockerfile))
	require.FileExists(t, filepath.Join(dir, "ontology.jsonld"))
	require.FileExists(t, filepath.Join(dir, ".dockerignore"))
}
