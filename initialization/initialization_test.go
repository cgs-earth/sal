package initialization

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/cgs-earth/sal/specs"
	"github.com/santhosh-tekuri/jsonschema/v6"
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
	// the prompts read nothing rather than the terminal unless a test answers
	// them, and a module is initialized at the prompt rather than in a browser
	answerPrompts(t)
	previous := headlessReason
	headlessReason = func() string { return "this is a test" }
	t.Cleanup(func() { headlessReason = previous })
	return dir
}

func TestInitSalModuleOpensTheFormByDefault(t *testing.T) {
	previous := headlessReason
	headlessReason = func() string { return "" }
	t.Cleanup(func() { headlessReason = previous })

	require.True(t, (&InitCmd{SalModule: true}).opensForm())
	require.False(t, (&InitCmd{SalModule: true, NoUI: true}).opensForm())
	require.False(t, (&InitCmd{SalModule: true, Bare: true}).opensForm())
}

func TestInitSalModuleAsksAtThePromptWhereNoBrowserWouldOpen(t *testing.T) {
	previous := headlessReason
	headlessReason = func() string { return "CI is set" }
	t.Cleanup(func() { headlessReason = previous })

	require.False(t, (&InitCmd{SalModule: true}).opensForm())
	require.True(t, (&InitCmd{SalModule: true, UI: true}).opensForm())
}

func TestHeadlessReasonNamesCI(t *testing.T) {
	t.Setenv("CI", "true")
	require.Equal(t, "CI is set", headlessReason())
}

func TestHeadlessReasonNamesAnSSHSession(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("SSH_CONNECTION", "10.0.0.2 52000 10.0.0.1 22")
	require.Equal(t, "this is an SSH session", headlessReason())
}

// answerPrompts feeds the prompts the given lines instead of the terminal for
// the rest of the test; with none, every prompt reads one blank answer.
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

// initProject runs init as a project with the description every project needs.
func initProject() error {
	return (&InitCmd{Description: "A test project"}).Run()
}

// readProjectConfig parses the .sal/config.jsonld init wrote into dir.
func readProjectConfig(t *testing.T, dir string) pkg.ConfigDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".sal", "config.jsonld"))
	require.NoError(t, err)
	var doc pkg.ConfigDocument
	require.NoError(t, json.Unmarshal(raw, &doc))
	return doc
}

func TestInitRecordsTheDescriptionGivenAsAFlag(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, (&InitCmd{Description: "A test project showing off sal features on a small dataset"}).Run())

	doc := readProjectConfig(t, dir)
	require.Equal(t, pkg.SalConfigContext, doc.Context)
	require.Len(t, doc.Graph, 1)
	require.JSONEq(t, `{
  "@id": ".",
  "@type": "schema:Project",
  "dcterms:title": "example",
  "dcterms:description": "A test project showing off sal features on a small dataset"
}`, string(doc.Graph[0]))
}

func TestInitAsksForTheDescriptionWhenNotGiven(t *testing.T) {
	dir := newGitRepo(t)
	answerPrompts(t, "  Answered at the prompt  ")
	require.NoError(t, (&InitCmd{}).Run())

	doc := readProjectConfig(t, dir)
	require.JSONEq(t, `{"@id": ".", "@type": "schema:Project", "dcterms:title": "example", "dcterms:description": "Answered at the prompt"}`, string(doc.Graph[0]))
}

// An empty answer is asked again rather than taken, since the description is
// required.
func TestInitAsksAgainUntilADescriptionIsGiven(t *testing.T) {
	dir := newGitRepo(t)
	answerPrompts(t, "", "   ", "Third time")
	require.NoError(t, (&InitCmd{}).Run())

	doc := readProjectConfig(t, dir)
	require.JSONEq(t, `{"@id": ".", "@type": "schema:Project", "dcterms:title": "example", "dcterms:description": "Third time"}`, string(doc.Graph[0]))
}

func TestInitWithoutADescriptionFailsAndWritesNothing(t *testing.T) {
	dir := newGitRepo(t)
	previous := stdin
	stdin = strings.NewReader("")
	t.Cleanup(func() { stdin = previous })

	require.ErrorIs(t, (&InitCmd{}).Run(), ErrDescriptionRequired)
	require.NoDirExists(t, filepath.Join(dir, ".sal"))
	require.NoFileExists(t, filepath.Join(dir, ".gitignore"))
}

// A project that has a .sal/config.jsonld is initialized, and init refuses to
// write over what it records, with or without a description given.
func TestInitFailsWhenTheProjectIsAlreadyInitialized(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, (&InitCmd{Description: "Original description"}).Run())

	err := (&InitCmd{Description: "New description"}).Run()

	require.ErrorContains(t, err, filepath.Join(dir, ".sal", "config.jsonld")+" already exists; this directory is already a SAL project")
	doc := readProjectConfig(t, dir)
	require.JSONEq(t, `{"@id": ".", "@type": "schema:Project", "dcterms:title": "example", "dcterms:description": "Original description"}`, string(doc.Graph[0]))
}

// A module is not a project, so it is neither asked for a description nor
// given a .sal/config.jsonld to record one in.
func TestInitSalModuleWritesNoProjectConfig(t *testing.T) {
	dir := newGitRepo(t)
	answerPrompts(t, "FetchStations", "Fetch weather stations", "Fetches every station in a region")
	require.NoError(t, (&InitCmd{SalModule: true}).Run())

	require.NoFileExists(t, filepath.Join(dir, ".sal", "config.jsonld"))
	require.Equal(t, "FetchStations", readOntologyTaskID(t, dir))
}

// readOntologyTaskID is the @id of the task class in the ontology.jsonld init
// wrote into dir, which says which prompt answer the task consumed.
func readOntologyTaskID(t *testing.T, dir string) string {
	t.Helper()
	_, graph := readOntology(t, dir)
	id, _ := graph[1]["@id"].(string)
	return id
}

func TestInitWithoutSalModuleWritesNoModuleFiles(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, initProject())

	require.DirExists(t, filepath.Join(dir, ".sal", "data"))
	require.NoFileExists(t, filepath.Join(dir, "ontology.jsonld"))
	require.NoFileExists(t, filepath.Join(dir, "Dockerfile"))
	require.NoFileExists(t, filepath.Join(dir, ".dockerignore"))
	require.NoDirExists(t, filepath.Join(dir, ".vscode"))
}

// readSettings parses the .vscode/settings.json init wrote into dir.
func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".vscode", "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(raw, &settings))
	return settings
}

// schemaEntry is the json.schemas entry init registers for ontology.jsonld.
var schemaEntry = map[string]any{
	"fileMatch": []any{"/ontology.jsonld"},
	"url":       "./.vscode/salmodule-ontology.schema.json",
}

func TestInitSalModuleWritesTheOntologySchemaForTheEditor(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	schema, err := os.ReadFile(filepath.Join(dir, ".vscode", "salmodule-ontology.schema.json"))
	require.NoError(t, err)
	require.Equal(t, specs.SalModuleOntologySchema, schema)
	require.Equal(t, map[string]any{"json.schemas": []any{schemaEntry}}, readSettings(t, dir))
}

func TestInitSalModuleAddsTheSchemaToExistingEditorSettings(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".vscode"), 0755))
	existing := `{"editor.tabSize": 2, "json.schemas": [{"fileMatch": ["/package.json"], "url": "https://json.schemastore.org/package.json"}]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".vscode", "settings.json"), []byte(existing), 0644))

	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	require.Equal(t, map[string]any{
		"editor.tabSize": float64(2),
		"json.schemas": []any{
			map[string]any{"fileMatch": []any{"/package.json"}, "url": "https://json.schemastore.org/package.json"},
			schemaEntry,
		},
	}, readSettings(t, dir))
}

func TestInitSalModuleRegistersTheSchemaOnlyOnce(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())
	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	require.Equal(t, map[string]any{"json.schemas": []any{schemaEntry}}, readSettings(t, dir))
}

func TestInitSalModuleRewritesAStaleSchemaCopy(t *testing.T) {
	dir := newGitRepo(t)
	schemaPath := filepath.Join(dir, ".vscode", "salmodule-ontology.schema.json")
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".vscode"), 0755))
	require.NoError(t, os.WriteFile(schemaPath, []byte("{}"), 0644))

	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	schema, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	require.Equal(t, specs.SalModuleOntologySchema, schema)
}

func TestInitSalModuleLeavesUnparseableEditorSettingsAlone(t *testing.T) {
	dir := newGitRepo(t)
	settingsPath := filepath.Join(dir, ".vscode", "settings.json")
	existing := "{\n  // comments are allowed in settings.json\n  \"editor.tabSize\": 2\n}\n"
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".vscode"), 0755))
	require.NoError(t, os.WriteFile(settingsPath, []byte(existing), 0644))

	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	content, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	require.Equal(t, existing, string(content))
	require.FileExists(t, filepath.Join(dir, ".vscode", "salmodule-ontology.schema.json"))
}

func TestInitGitignoresGeneratedDirectories(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, initProject())

	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, ".sal/data\n.sal/constructed\n", string(gitignore))
}

func TestInitAddsOnlyTheMissingGitignoreEntries(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules\n.sal/data"), 0644))

	require.NoError(t, initProject())

	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, "node_modules\n.sal/data\n.sal/constructed\n", string(gitignore))
}

func TestInitCreatesSparqlDirectory(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, initProject())

	require.DirExists(t, filepath.Join(dir, "sparql"))
}

func TestInitLeavesExistingSparqlDirectoryAlone(t *testing.T) {
	dir := newGitRepo(t)
	query := filepath.Join(dir, "sparql", "stations.rq")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sparql"), 0755))
	require.NoError(t, os.WriteFile(query, []byte("SELECT * WHERE { ?s ?p ?o }\n"), 0644))

	require.NoError(t, initProject())

	content, err := os.ReadFile(query)
	require.NoError(t, err)
	require.Equal(t, "SELECT * WHERE { ?s ?p ?o }\n", string(content))
}

func TestInitSalModuleMakesNoProjectLayout(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	require.NoDirExists(t, filepath.Join(dir, ".sal"))
	require.NoDirExists(t, filepath.Join(dir, "sparql"))
	require.NoFileExists(t, filepath.Join(dir, ".gitignore"))
}

func TestInitSalModuleBareScaffoldsModuleFiles(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	dockerfile, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	require.Equal(t, "# When invoked this Dockerfile should stream JSON-LD to standard out in accordance with the salmodule specification.\n", string(dockerfile))

	dockerignore, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	require.NoError(t, err)
	require.Equal(t, "# Ignore any files that should not be packaged into the salmodule container ran by the SAL cli.\n", string(dockerignore))

	context, graph := readOntology(t, dir)
	require.Equal(t, map[string]any{
		"dcterms":   "http://purl.org/dc/terms/",
		"owl":       "http://www.w3.org/2002/07/owl#",
		"rdfs":      "http://www.w3.org/2000/01/rdf-schema#",
		"salmodule": salmodule.Namespace,
	}, context)
	require.Len(t, graph, 2)

	ontology := graph[0]
	require.Equal(t, "owl:Ontology", ontology["@type"])
	require.Equal(t, ".", ontology["@id"])
	require.Equal(t, "Ada Lovelace", ontology["dcterms:creator"])
	require.Equal(t, "example", ontology["dcterms:title"])
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
	require.Equal(t, "", graph[0]["dcterms:creator"])
	require.Equal(t, "example", graph[0]["dcterms:title"])
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

// validateAgainstSchema checks the ontology.jsonld init wrote into dir against
// the JSON Schema it also wrote, which is how an editor will check it.
func validateAgainstSchema(t *testing.T, dir string) error {
	t.Helper()
	schemaDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(specs.SalModuleOntologySchema))
	require.NoError(t, err)
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource(specs.SalModuleOntologySchemaFile, schemaDocument))
	schema, err := compiler.Compile(specs.SalModuleOntologySchemaFile)
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(dir, "ontology.jsonld"))
	require.NoError(t, err)
	ontology, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	return schema.Validate(ontology)
}

func TestInitSalModuleSampleTaskConformsToTheSchema(t *testing.T) {
	dir := newGitRepo(t)
	answerPrompts(t, "FetchStations", "Fetch weather stations", "Fetches every station in a region")
	require.NoError(t, (&InitCmd{SalModule: true}).Run())

	require.NoError(t, validateAgainstSchema(t, dir))
}

func TestInitSalModuleBareSkeletonIsFlaggedBySchemaUntilFilledIn(t *testing.T) {
	dir := newGitRepo(t)
	require.NoError(t, (&InitCmd{SalModule: true, Bare: true}).Run())

	err := validateAgainstSchema(t, dir)

	require.ErrorContains(t, err, "/@graph/1/@id")
	require.ErrorContains(t, err, "/@graph/1/salmodule:taskShape")
}
