package edit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cgs-earth/sal/specs"
	"github.com/stretchr/testify/require"
)

const testModuleNamespace = "salmodule://github.com/cgs-earth/example/"

// validModuleOntology is the document the form assembles for a module with
// one task and every string filled in.
const validModuleOntology = `{
  "@context": {
    "dcterms": "http://purl.org/dc/terms/",
    "owl": "http://www.w3.org/2002/07/owl#",
    "rdfs": "http://www.w3.org/2000/01/rdf-schema#",
    "salmodule": "https://w3id.org/sal/salmodule#",
    "sh": "http://www.w3.org/ns/shacl#",
    "xsd": "http://www.w3.org/2001/XMLSchema#"
  },
  "@graph": [
    {
      "@id": ".",
      "@type": "owl:Ontology",
      "dcterms:creator": "Ada Lovelace",
      "dcterms:title": "example",
      "owl:versionInfo": "1.0"
    },
    {
      "@id": "FetchStations",
      "@type": "owl:Class",
      "rdfs:subClassOf": {
        "@id": "salmodule:Task"
      },
      "rdfs:label": "Fetch stations",
      "rdfs:comment": "Fetches every station in a region.",
      "salmodule:taskShape": {
        "@type": "sh:NodeShape",
        "sh:targetClass": {
          "@id": "FetchStations"
        },
        "sh:property": []
      }
    }
  ]
}
`

// newForm serves the module form for a fresh module root in the given mode
// and returns the server and the root, counting how often the form wrote.
func newForm(t *testing.T, mode string) (*httptest.Server, string, *int) {
	t.Helper()
	dir := t.TempDir()
	writes := 0
	form := &Form{Dir: dir, Namespace: testModuleNamespace, Title: "example", Creator: "Ada Lovelace", Mode: mode, Written: func() { writes++ }}
	handler, err := form.Handler()
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, dir, &writes
}

// post sends a document, with the base it was edited from, to an endpoint
// and decodes the JSON answer.
func post(t *testing.T, server *httptest.Server, path string, document string, base string) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(documentRequest{Document: document, Base: base})
	require.NoError(t, err)
	response, err := http.Post(server.URL+path, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	var answer map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&answer))
	return response.StatusCode, answer
}

func getStatus(t *testing.T, server *httptest.Server) Status {
	t.Helper()
	response, err := http.Get(server.URL + "/api/module/status")
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var status Status
	require.NoError(t, json.NewDecoder(response.Body).Decode(&status))
	return status
}

func TestFormReportsTheModuleAsNotInitializedUntilTheOntologyExists(t *testing.T) {
	server, dir, _ := newForm(t, "init")

	status := getStatus(t, server)
	require.Equal(t, "init", status.Mode)
	require.False(t, status.Initialized)
	require.Empty(t, status.Document)
	require.False(t, status.Dockerfile)
	require.Equal(t, filepath.Join(dir, "ontology.jsonld"), status.OntologyPath)
	require.Equal(t, testModuleNamespace, status.Namespace)
	require.Equal(t, "example", status.Title)
	require.Equal(t, "Ada Lovelace", status.Creator)

	// the file appearing, by any means, is seen with no restart, and its
	// text is what the form edits from
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ontology.jsonld"), []byte(validModuleOntology), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0644))
	status = getStatus(t, server)
	require.True(t, status.Initialized)
	require.Equal(t, validModuleOntology, status.Document)
	require.True(t, status.Dockerfile)

	require.NoError(t, os.Remove(filepath.Join(dir, "ontology.jsonld")))
	require.False(t, getStatus(t, server).Initialized)
}

// lineOf is the 1-based line the first occurrence of text starts on.
func lineOf(document string, text string) float64 {
	return float64(strings.Count(document[:strings.Index(document, text)], "\n") + 1)
}

// problemLines are the lines the validate endpoint reports problems on.
func problemLines(t *testing.T, server *httptest.Server, document string) []float64 {
	t.Helper()
	code, answer := post(t, server, "/api/module/validate", document, "")
	require.Equal(t, http.StatusOK, code)
	var lines []float64
	for _, problem := range answer["problems"].([]any) {
		lines = append(lines, problem.(map[string]any)["line"].(float64))
	}
	return lines
}

func TestFormChecksTheDocumentOnItsOwnLines(t *testing.T) {
	server, _, _ := newForm(t, "init")

	// with no class subclassing salmodule:Task, the graph as a whole is wrong
	document := strings.Replace(validModuleOntology, "\"@id\": \"salmodule:Task\"", "\"@id\": \"owl:Thing\"", 1)
	require.Equal(t, []float64{lineOf(document, "\"@graph\"")}, problemLines(t, server, document))

	// a property shape with a node kind SHACL does not have is wrong on the
	// line of that value, and its path is a term the module never declares
	document = strings.Replace(validModuleOntology, "\"sh:property\": []", "\"sh:property\": [\n          {\n            \"sh:path\": {\n              \"@id\": \"region\"\n            },\n            \"sh:nodeKind\": {\n              \"@id\": \"sh:Nonsense\"\n            }\n          }\n        ]", 1)
	lines := problemLines(t, server, document)
	require.Contains(t, lines, lineOf(document, "\"sh:Nonsense\""))
	require.Contains(t, lines, lineOf(document, "\"sh:path\""), "the undeclared term is reported on the line of the node naming it")

	require.Empty(t, problemLines(t, server, validModuleOntology))
}

func TestFormRefusesADocumentThatIsNotJSON(t *testing.T) {
	server, _, _ := newForm(t, "init")
	code, answer := post(t, server, "/api/module/validate", "{ not json", "")
	require.Equal(t, http.StatusOK, code)
	problems := answer["problems"].([]any)
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].(map[string]any)["message"], "invalid JSON")

	response, err := http.Post(server.URL+"/api/module/validate", "application/json", strings.NewReader(`{"document": ""}`))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
}

func TestFormWritesTheModuleFiles(t *testing.T) {
	server, dir, writes := newForm(t, "init")

	// a document with problems is not written
	code, answer := post(t, server, "/api/module/write", `{"@context": {}, "@graph": []}`, "")
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.NotEmpty(t, answer["problems"])
	require.NoFileExists(t, filepath.Join(dir, "ontology.jsonld"))
	require.Equal(t, 0, *writes)

	code, answer = post(t, server, "/api/module/write", validModuleOntology, "")
	require.Equal(t, http.StatusOK, code, "%v", answer)
	require.Equal(t, filepath.Join(dir, "ontology.jsonld"), answer["ontologyPath"])
	require.Equal(t, 1, *writes)

	// the file is the document byte for byte, so the lines the form showed
	// are the lines an editor shows
	written, err := os.ReadFile(filepath.Join(dir, "ontology.jsonld"))
	require.NoError(t, err)
	require.Equal(t, validModuleOntology, string(written))
	require.FileExists(t, filepath.Join(dir, "Dockerfile"))
	require.FileExists(t, filepath.Join(dir, ".dockerignore"))
	require.FileExists(t, filepath.Join(dir, ".vscode", specs.SalModuleOntologySchemaFile))
	require.FileExists(t, filepath.Join(dir, ".vscode", "settings.json"))
	require.True(t, getStatus(t, server).Initialized)
}

// The form edits as well as creates: a document written from the text the
// form loaded replaces the file, and the Dockerfile beside it is left as it
// is.
func TestFormRewritesTheOntologyItLoaded(t *testing.T) {
	server, dir, writes := newForm(t, "edit")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ontology.jsonld"), []byte(validModuleOntology), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0644))
	edited := strings.Replace(validModuleOntology, "Fetch stations", "Fetch every station", 1)

	code, answer := post(t, server, "/api/module/write", edited, validModuleOntology)
	require.Equal(t, http.StatusOK, code, "%v", answer)
	require.Equal(t, 1, *writes)

	written, err := os.ReadFile(filepath.Join(dir, "ontology.jsonld"))
	require.NoError(t, err)
	require.Equal(t, edited, string(written))
	dockerfile, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	require.Equal(t, "FROM scratch\n", string(dockerfile))
	require.Equal(t, edited, getStatus(t, server).Document)
}

// A file edited by hand since the form loaded it is not written over.
func TestFormRefusesToWriteOverAFileChangedOnDisk(t *testing.T) {
	server, dir, writes := newForm(t, "edit")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ontology.jsonld"), []byte(validModuleOntology), 0644))
	byHand := strings.Replace(validModuleOntology, "1.0", "2.0", 1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ontology.jsonld"), []byte(byHand), 0644))

	code, answer := post(t, server, "/api/module/write", validModuleOntology, validModuleOntology)
	require.Equal(t, http.StatusConflict, code)
	require.Contains(t, answer["error"], "changed on disk")
	require.Equal(t, 0, *writes)
	written, err := os.ReadFile(filepath.Join(dir, "ontology.jsonld"))
	require.NoError(t, err)
	require.Equal(t, byHand, string(written))
}

func TestFormServesThePageForEveryOtherPath(t *testing.T) {
	server, _, _ := newForm(t, "init")
	for _, path := range []string{"/", "/anything/the/app/routes"} {
		response, err := http.Get(server.URL + path)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Contains(t, response.Header.Get("Content-Type"), "text/html")
		require.Contains(t, string(body), "<title>SAL module ontology</title>")
	}
}

func TestEditRequiresAnOntology(t *testing.T) {
	t.Chdir(t.TempDir())
	require.ErrorIs(t, (&EditCmd{}).Run(), ErrNotAModule)
}

func TestWriteAgentsFileWritesAndRewritesItsOwnFileOnly(t *testing.T) {
	dir := t.TempDir()
	state, err := agentsFileState(dir)
	require.NoError(t, err)
	require.Equal(t, AgentsFileNone, state)

	path, err := WriteAgentsFile(dir, testModuleNamespace, []byte(validModuleOntology))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "AGENTS.md"), path)
	agents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(agents), agentsMarker), "the file is marked as sal's")
	require.Contains(t, string(agents), "\n# "+filepath.Base(dir)+"\n")
	require.Contains(t, string(agents), "### FetchStations")
	state, err = agentsFileState(dir)
	require.NoError(t, err)
	require.Equal(t, AgentsFileSal, state)

	// a later write replaces it, since it is sal's
	edited := strings.Replace(validModuleOntology, "Fetch stations", "Fetch every station", 1)
	_, err = WriteAgentsFile(dir, testModuleNamespace, []byte(edited))
	require.NoError(t, err)
	agents, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(agents), "Fetch every station.")

	// one someone else wrote is never written over
	require.NoError(t, os.WriteFile(path, []byte("# Mine\n"), 0644))
	_, err = WriteAgentsFile(dir, testModuleNamespace, []byte(validModuleOntology))
	require.ErrorIs(t, err, ErrForeignAgentsFile)
	mine, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "# Mine\n", string(mine))
}
