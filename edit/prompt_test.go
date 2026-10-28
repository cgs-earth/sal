package edit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// exampleModule is the ontology of examples/salmodule/python-geoconnex,
// which declares a task with a configuration property and a stdout shape.
func exampleModule(t *testing.T) []byte {
	t.Helper()
	document, err := os.ReadFile(filepath.Join("..", "examples", "salmodule", "python-geoconnex", "ontology.jsonld"))
	require.NoError(t, err)
	return document
}

func TestAgentPromptDescribesTheTasksAndTheirShapes(t *testing.T) {
	prompt, err := AgentPrompt("/work/geoconnex", "salmodule://github.com/cgs-earth/python-geoconnex/", exampleModule(t))
	require.NoError(t, err)

	require.Contains(t, prompt, "Implement the SAL module whose ontology is ontology.jsonld in /work/geoconnex.")
	require.Contains(t, prompt, "- Namespace: salmodule://github.com/cgs-earth/python-geoconnex/")
	require.Contains(t, prompt, "- Title: Sample SAL Module")
	require.Contains(t, prompt, "- Creator: Andrew Padilla")
	require.Contains(t, prompt, "    schema: https://schema.org/")
	require.Contains(t, prompt, "- Task instance environment variable: SALMODULE_TASK_INSTANCE")

	require.Contains(t, prompt, "### GeoconnexReferenceFeatureStates\nGeoconnex U.S. States Reference Features.\nFetches the states from Geoconnex reference feature in Geoconnex.")
	require.Contains(t, prompt, "IRI: salmodule://github.com/cgs-earth/python-geoconnex/GeoconnexReferenceFeatureStates")
	require.Contains(t, prompt, "- maxRetries: a xsd:integer literal, required, exactly one. The maximum number of retry attempts to connect to the reference feature server before giving up.")
	require.Contains(t, prompt, "[sh:minInclusive: 0]", "a constraint the summary does not name is still listed")
	require.Contains(t, prompt, `{"@id": "https://example.org/project/GeoconnexReferenceFeatureStates1", "@type": "GeoconnexReferenceFeatureStates", "maxRetries": {"@type":"xsd:integer","@value":"3"}}`)

	require.Contains(t, prompt, `- every node printed with "@type": "schema:Place" must satisfy:`)
	require.Contains(t, prompt, "    - schema:name: any value, required, exactly one")
	require.Contains(t, prompt, "    - schema:subjectOf: IRI, required, at least 1")
	require.Contains(t, prompt, "    - gsp:hasGeometry: IRI, required, at least 1")
	require.NotContains(t, prompt, "Input (from salmodule:stdinShape")

	require.Contains(t, prompt, "docker build -t sal-module-geoconnex .")
	require.Contains(t, prompt, "docker run --rm -e SALMODULE_TASK_INSTANCE='{")
	require.Contains(t, prompt, "sal validate --salmodule")
}

func TestAgentPromptPointsAtTheInstalledSkill(t *testing.T) {
	dir := t.TempDir()
	for _, skillDir := range []string{".claude", ".agents"} {
		skill := filepath.Join(dir, skillDir, "skills", "salmodule")
		require.NoError(t, os.MkdirAll(skill, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# skill\n"), 0644))
	}

	prompt, err := AgentPrompt(dir, "salmodule://github.com/cgs-earth/example/", exampleModule(t))
	require.NoError(t, err)

	require.Contains(t, prompt, "read the SAL module skill installed in this repository")
	require.Contains(t, prompt, "\n    "+filepath.Join(".claude", "skills", "salmodule", "SKILL.md")+"\n")
	require.Contains(t, prompt, "\n    "+filepath.Join(".agents", "skills", "salmodule", "SKILL.md")+"\n")
	require.NotContains(t, prompt, "npx skills add")
}

func TestAgentPromptSaysWhenATaskDeclaresNoOutputShape(t *testing.T) {
	document := `{
  "@context": {"owl": "http://www.w3.org/2002/07/owl#", "rdfs": "http://www.w3.org/2000/01/rdf-schema#", "salmodule": "https://w3id.org/sal/salmodule#"},
  "@graph": [
    {"@id": ".", "@type": "owl:Ontology", "salmodule:taskInstanceEnvVar": "MY_TASK"},
    {"@id": "Fetch", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"}},
    {"@id": "region", "@type": "owl:DatatypeProperty", "rdfs:comment": "The region to fetch.", "rdfs:range": {"@id": "xsd:string"}},
    {"@id": "Helper", "@type": "owl:Class"}
  ]
}`
	prompt, err := AgentPrompt("/work/m", "salmodule://github.com/cgs-earth/m/", []byte(document))
	require.NoError(t, err)

	require.Contains(t, prompt, "- Task instance environment variable: MY_TASK")
	require.Contains(t, prompt, "Configuration: the task shape constrains no properties")
	require.Contains(t, prompt, "Output: the class declares no salmodule:stdoutShape")
	require.Contains(t, prompt, "- region (xsd:string): The region to fetch.")
	require.Contains(t, prompt, "Other nodes in the graph, which this summary does not describe: Helper.")
	require.Contains(t, prompt, `docker run --rm -e MY_TASK='{"@id": "https://example.org/project/Fetch1", "@type": "Fetch"}' sal-module-m salmodule run`)
}

func TestAgentPromptRefusesADocumentWithoutATask(t *testing.T) {
	_, err := AgentPrompt("/work/m", "salmodule://github.com/cgs-earth/m/", []byte(`{"@context": {}, "@graph": [{"@id": ".", "@type": "owl:Ontology"}]}`))
	require.ErrorContains(t, err, "declares no task class")

	_, err = AgentPrompt("/work/m", "salmodule://github.com/cgs-earth/m/", []byte(`not json`))
	require.ErrorContains(t, err, "not a JSON-LD document")
}

func TestCardinalityWordsTheCounts(t *testing.T) {
	require.Equal(t, "optional, repeatable", promptProperty{}.Cardinality())
	require.Equal(t, "optional, at most one", promptProperty{MaxCount: "1"}.Cardinality())
	require.Equal(t, "required, exactly one", promptProperty{MinCount: "1", MaxCount: "1"}.Cardinality())
	require.Equal(t, "required, at least 2", promptProperty{MinCount: "2"}.Cardinality())
	require.Equal(t, "required, between 1 and 3", promptProperty{MinCount: "1", MaxCount: "3"}.Cardinality())
	require.Equal(t, "optional, at most 3", promptProperty{MinCount: "0", MaxCount: "3"}.Cardinality())
}
