package initialization

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
    "salmodule": "https://w3id.org/sal/cgs-earth/sal/ontology/salmodule#",
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

// newModuleInitForm serves the module init form for a fresh module root and
// returns the server and the root, counting how often the form wrote.
func newModuleInitForm(t *testing.T) (*httptest.Server, string, *int) {
	t.Helper()
	dir := t.TempDir()
	writes := 0
	form := &moduleInitServer{dir: dir, namespace: testModuleNamespace, title: "example", creator: "Ada Lovelace", written: func() { writes++ }}
	handler, err := form.handler()
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, dir, &writes
}

// postDocument sends a document to a validate or write endpoint and decodes
// the JSON answer.
func postDocument(t *testing.T, server *httptest.Server, path string, document string) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(ontologyRequest{Document: document})
	require.NoError(t, err)
	response, err := http.Post(server.URL+path, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	var answer map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&answer))
	return response.StatusCode, answer
}

func getStatus(t *testing.T, server *httptest.Server) moduleInitStatus {
	t.Helper()
	response, err := http.Get(server.URL + "/api/init/status")
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var status moduleInitStatus
	require.NoError(t, json.NewDecoder(response.Body).Decode(&status))
	return status
}

func TestInitUIRequiresSalModule(t *testing.T) {
	newGitRepo(t)
	require.ErrorIs(t, (&InitCmd{UI: true}).Run(), ErrUIWithoutSalModule)
	require.ErrorIs(t, (&InitCmd{NoUI: true}).Run(), ErrUIWithoutSalModule)
}

func TestModuleInitFormReportsTheModuleAsNotInitializedUntilTheOntologyExists(t *testing.T) {
	server, dir, _ := newModuleInitForm(t)

	status := getStatus(t, server)
	require.False(t, status.Initialized)
	require.Equal(t, filepath.Join(dir, "ontology.jsonld"), status.OntologyPath)
	require.Equal(t, testModuleNamespace, status.Namespace)
	require.Equal(t, "example", status.Title)
	require.Equal(t, "Ada Lovelace", status.Creator)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "ontology.jsonld"), []byte(validModuleOntology), 0644))
	require.True(t, getStatus(t, server).Initialized)

	// deleting the file unlocks the form again, with no restart
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
	code, answer := postDocument(t, server, "/api/init/validate", document)
	require.Equal(t, http.StatusOK, code)
	var lines []float64
	for _, problem := range answer["problems"].([]any) {
		lines = append(lines, problem.(map[string]any)["line"].(float64))
	}
	return lines
}

func TestModuleInitFormChecksTheDocumentOnItsOwnLines(t *testing.T) {
	server, _, _ := newModuleInitForm(t)

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

func TestModuleInitFormRefusesADocumentThatIsNotJSON(t *testing.T) {
	server, _, _ := newModuleInitForm(t)
	code, answer := postDocument(t, server, "/api/init/validate", "{ not json")
	require.Equal(t, http.StatusOK, code)
	problems := answer["problems"].([]any)
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].(map[string]any)["message"], "invalid JSON")

	response, err := http.Post(server.URL+"/api/init/validate", "application/json", strings.NewReader(`{"document": ""}`))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
}

func TestModuleInitFormWritesTheModuleFilesOnce(t *testing.T) {
	server, dir, writes := newModuleInitForm(t)

	// a document with problems is not written
	code, answer := postDocument(t, server, "/api/init/write", `{"@context": {}, "@graph": []}`)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.NotEmpty(t, answer["problems"])
	require.NoFileExists(t, filepath.Join(dir, "ontology.jsonld"))
	require.Equal(t, 0, *writes)

	code, answer = postDocument(t, server, "/api/init/write", validModuleOntology)
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

	// the form is for creating an ontology, not editing one
	code, answer = postDocument(t, server, "/api/init/write", validModuleOntology)
	require.Equal(t, http.StatusConflict, code)
	require.Contains(t, answer["error"], "already exists")
	require.Equal(t, 1, *writes)
}

func TestModuleInitFormServesThePageForEveryOtherPath(t *testing.T) {
	server, _, _ := newModuleInitForm(t)
	for _, path := range []string{"/", "/anything/the/app/routes"} {
		response, err := http.Get(server.URL + path)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Contains(t, response.Header.Get("Content-Type"), "text/html")
		require.Contains(t, string(body), "<title>SAL module init</title>")
	}
}
