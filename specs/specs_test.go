package specs

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

// compileSchema compiles the embedded SAL module ontology schema.
func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(SalModuleOntologySchema))
	require.NoError(t, err)
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource(SalModuleOntologySchemaFile, document))
	schema, err := compiler.Compile(SalModuleOntologySchemaFile)
	require.NoError(t, err)
	return schema
}

// validate checks a JSON-LD module ontology against the schema.
func validate(t *testing.T, ontology string) error {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(ontology))
	require.NoError(t, err)
	return compileSchema(t).Validate(instance)
}

// validOntology is the smallest module ontology the schema accepts; the tests
// below edit it to produce the documents they refuse.
const validOntology = `{
  "@context": {
    "schema": "https://schema.org/",
    "owl": "http://www.w3.org/2002/07/owl#",
    "rdfs": "http://www.w3.org/2000/01/rdf-schema#",
    "sh": "http://www.w3.org/ns/shacl#",
    "xsd": "http://www.w3.org/2001/XMLSchema#",
    "salmodule": "https://w3id.org/sal/cgs-earth/sal/ontology/salmodule#"
  },
  "@graph": [
    {"@id": ".", "@type": "owl:Ontology", "rdfs:label": "Weather stations", "salmodule:taskInstanceEnvVar": "STATION_TASK"},
    {
      "@id": "StationFetcher", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"},
      "rdfs:comment": "Fetches every station in a region",
      "salmodule:taskShape": {
        "@type": "sh:NodeShape",
        "sh:targetClass": {"@id": "StationFetcher"},
        "sh:property": [{"sh:path": {"@id": "region"}, "sh:minCount": 1, "sh:datatype": {"@id": "xsd:string"}, "sh:languageIn": {"@list": ["en"]}}]
      },
      "salmodule:stdoutShape": {
        "@type": "sh:NodeShape",
        "sh:targetClass": {"@id": "schema:Place"},
        "sh:property": [{"sh:path": {"@id": "schema:name"}, "sh:minCount": 1, "sh:maxCount": 1, "sh:nodeKind": {"@id": "sh:Literal"}}]
      }
    },
    {"@id": "region", "@type": "owl:DatatypeProperty", "rdfs:domain": {"@id": "StationFetcher"}, "rdfs:range": {"@id": "xsd:string"}}
  ]
}`

func TestSchemaAcceptsAMinimalModuleOntology(t *testing.T) {
	require.NoError(t, validate(t, validOntology))
}

func TestSchemaAcceptsTheExampleModuleOntology(t *testing.T) {
	ontology, err := os.ReadFile("../examples/salmodule/python-geoconnex/ontology.jsonld")
	require.NoError(t, err)

	require.NoError(t, validate(t, string(ontology)))
}

func TestSchemaAcceptsTheOntologyOfTheSalCLI(t *testing.T) {
	ontology, err := os.ReadFile("../salmodule/sal_ontology.jsonld")
	require.NoError(t, err)

	require.NoError(t, validate(t, string(ontology)))
}

func TestSchemaRequiresTheSalModulePrefix(t *testing.T) {
	ontology := strings.Replace(validOntology, `"salmodule": "https://w3id.org/sal/cgs-earth/sal/ontology/salmodule#"`, `"sal": "https://w3id.org/sal/cgs-earth/sal/ontology/salmodule#"`, 1)

	err := validate(t, ontology)

	require.ErrorContains(t, err, "salmodule")
}

func TestSchemaRequiresExactlyOneOntologyNode(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `{"@id": ".", "@type": "owl:Ontology"`, `{"@id": "x", "@type": "owl:Class"`, 1))

	require.ErrorContains(t, err, "contains")
}

func TestSchemaRequiresTheOntologyNodeToBeNamedDot(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `{"@id": ".", "@type": "owl:Ontology"`, `{"@id": "stations", "@type": "owl:Ontology"`, 1))

	require.ErrorContains(t, err, `'.'`)
}

func TestSchemaRequiresATaskClass(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `"rdfs:subClassOf": {"@id": "salmodule:Task"}`, `"rdfs:subClassOf": {"@id": "schema:Thing"}`, 1))

	require.ErrorContains(t, err, "contains")
}

func TestSchemaRejectsASubClassOfWrittenAsAString(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `"rdfs:subClassOf": {"@id": "salmodule:Task"}`, `"rdfs:subClassOf": "salmodule:Task"`, 1))

	require.ErrorContains(t, err, "rdfs:subClassOf")
}

func TestSchemaRejectsAnInvalidEnvironmentVariableName(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `"STATION_TASK"`, `"station task"`, 1))

	require.ErrorContains(t, err, "taskInstanceEnvVar")
}

func TestSchemaRejectsATermWithSpaces(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `"@id": "StationFetcher", "@type": "owl:Class"`, `"@id": "Station Fetcher", "@type": "owl:Class"`, 1))

	require.ErrorContains(t, err, "@id")
}

func TestSchemaRejectsAShapeWithoutItsType(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `"@type": "sh:NodeShape",
        "sh:targetClass": {"@id": "StationFetcher"}`, `"sh:targetClass": {"@id": "StationFetcher"}`, 1))

	require.ErrorContains(t, err, "salmodule:taskShape")
}

func TestSchemaAcceptsAShapeReferencedByID(t *testing.T) {
	ontology := strings.Replace(validOntology, `"salmodule:stdoutShape": {`, `"salmodule:stdoutShape": {"@id": "PlaceShape"}, "salmodule:stdinShape": {`, 1)

	require.NoError(t, validate(t, ontology))
}

func TestSchemaRejectsAnUnprefixedKeyInAShape(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `"sh:minCount": 1, "sh:maxCount": 1`, `"minCount": 1, "sh:maxCount": 1`, 1))

	require.ErrorContains(t, err, "minCount")
}

func TestSchemaRejectsAnUnsupportedConstraintOnStdout(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `"sh:nodeKind": {"@id": "sh:Literal"}`, `"sh:uniqueLang": true`, 1))

	require.ErrorContains(t, err, "sh:uniqueLang")
}

func TestSchemaRejectsAnInvalidNodeKind(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `{"@id": "sh:Literal"}`, `"sh:Literal"`, 1))

	require.ErrorContains(t, err, "sh:nodeKind")
}

func TestSchemaRejectsAListWrittenAsAnArray(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `"sh:languageIn": {"@list": ["en"]}`, `"sh:languageIn": ["en"]`, 1))

	require.ErrorContains(t, err, "sh:languageIn")
}

func TestSchemaRejectsABaseInTheContext(t *testing.T) {
	err := validate(t, strings.Replace(validOntology, `"schema": "https://schema.org/",`, `"@base": "https://example.org/", "schema": "https://schema.org/",`, 1))

	require.ErrorContains(t, err, "@base")
}
