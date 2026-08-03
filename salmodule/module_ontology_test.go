package salmodule

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const testModuleNamespace = "salmodule://www.github.com/test/history-getter/"

// testOntology mirrors the shape of a real module ontology, including the
// common case of an ontology that uses rdfs terms without binding the rdfs
// prefix in its @context.
const testOntology = `{
	"@context": {
		"schema": "https://schema.org/",
		"owl": "http://www.w3.org/2002/07/owl#",
		"salmodule": "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
	},
	"@graph": [
		{
			"@id": ".",
			"@type": "owl:Ontology",
			"owl:versionInfo": "1.0"
		},
		{
			"@id": "EducationalHistoryFinder",
			"@type": "owl:Class",
			"rdfs:subClassOf": {"@id": "salmodule:Task"}
		},
		{
			"@id": "NotATask",
			"@type": "owl:Class"
		}
	]
}`

func TestParseModuleOntologyFindsTaskSubclasses(t *testing.T) {
	ontology, err := parseModuleOntology(testModuleNamespace, []byte(testOntology))

	require.NoError(t, err)
	require.True(t, ontology.IsTaskClass(testModuleNamespace+"EducationalHistoryFinder"))
	require.False(t, ontology.IsTaskClass(testModuleNamespace+"NotATask"))
}

func TestParseModuleOntologyFindsIndirectTaskSubclasses(t *testing.T) {
	document := `{
		"@context": {
			"owl": "http://www.w3.org/2002/07/owl#",
			"rdfs": "http://www.w3.org/2000/01/rdf-schema#",
			"salmodule": "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
		},
		"@graph": [
			{"@id": "BaseFinder", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:NodeProcessor"}},
			{"@id": "EducationalHistoryFinder", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "BaseFinder"}}
		]
	}`

	ontology, err := parseModuleOntology(testModuleNamespace, []byte(document))

	require.NoError(t, err)
	require.True(t, ontology.IsTaskClass(testModuleNamespace+"EducationalHistoryFinder"))
}

func TestParseModuleOntologyDefaultsTaskInstanceEnvVar(t *testing.T) {
	ontology, err := parseModuleOntology(testModuleNamespace, []byte(testOntology))

	require.NoError(t, err)
	require.Equal(t, DefaultTaskInstanceEnvVar, ontology.TaskInstanceEnvVar)
}

func TestParseModuleOntologyUsesDeclaredTaskInstanceEnvVar(t *testing.T) {
	document := `{
		"@context": {
			"owl": "http://www.w3.org/2002/07/owl#",
			"salmodule": "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
		},
		"@graph": [
			{"@id": ".", "@type": "owl:Ontology", "salmodule:taskInstanceEnvVar": "MODULE_TASK"}
		]
	}`

	ontology, err := parseModuleOntology(testModuleNamespace, []byte(document))

	require.NoError(t, err)
	require.Equal(t, "MODULE_TASK", ontology.TaskInstanceEnvVar)
}

func TestParseModuleOntologyRejectsInvalidJSON(t *testing.T) {
	_, err := parseModuleOntology(testModuleNamespace, []byte("not json"))

	require.Error(t, err)
	require.Contains(t, err.Error(), testModuleNamespace)
}
