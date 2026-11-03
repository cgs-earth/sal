package salmodule

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// testShapedOntology declares a task class whose output must be schema:Place
// nodes with exactly one name, a subclass of it, a task class with no output
// shape at all, and a taskShape that must never be applied to output.
const testShapedOntology = `{
	"@context": {
		"schema": "https://schema.org/",
		"owl": "http://www.w3.org/2002/07/owl#",
		"rdfs": "http://www.w3.org/2000/01/rdf-schema#",
		"sh": "http://www.w3.org/ns/shacl#",
		"salmodule": "https://w3id.org/sal/salmodule#"
	},
	"@graph": [
		{"@id": ".", "@type": "owl:Ontology"},
		{
			"@id": "PlaceFinder",
			"@type": "owl:Class",
			"rdfs:subClassOf": {"@id": "salmodule:Task"},
			"salmodule:stdoutShape": {
				"@type": "sh:NodeShape",
				"sh:targetClass": {"@id": "schema:Place"},
				"sh:property": [
					{"sh:path": {"@id": "schema:name"}, "sh:minCount": 1, "sh:maxCount": 1, "sh:message": "A place needs exactly one name"},
					{"sh:path": {"@id": "schema:geo"}, "sh:nodeKind": {"@id": "sh:IRI"}}
				]
			},
			"salmodule:taskShape": {
				"@type": "sh:NodeShape",
				"sh:targetClass": {"@id": "schema:Person"},
				"sh:property": [{"sh:path": {"@id": "schema:email"}, "sh:minCount": 1}]
			}
		},
		{"@id": "CityFinder", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "PlaceFinder"}},
		{"@id": "UnshapedFinder", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"}}
	]
}`

func testShapedValidator(t *testing.T, class string) *StreamValidator {
	t.Helper()
	ontology, err := parseModuleOntology(testModuleNamespace, []byte(testShapedOntology))
	require.NoError(t, err)
	validator, err := ontology.StdoutValidator(testModuleNamespace+class, testProjectBase)
	require.NoError(t, err)
	require.NotNil(t, validator)
	return validator
}

func TestStdoutValidatorIsNilForAClassWithoutAStdoutShape(t *testing.T) {
	ontology, err := parseModuleOntology(testModuleNamespace, []byte(testShapedOntology))
	require.NoError(t, err)

	validator, err := ontology.StdoutValidator(testModuleNamespace+"UnshapedFinder", testProjectBase)

	require.NoError(t, err)
	require.Nil(t, validator)
}

func TestStdoutValidatorAcceptsAConformingLine(t *testing.T) {
	validator := testShapedValidator(t, "PlaceFinder")

	err := validator.ValidateLine([]byte(`{"@id":"place/1","@type":"schema:Place","schema:name":"Lake Tahoe","schema:geo":{"@id":"place/1/geo"}}`), 1)

	require.NoError(t, err)
}

func TestStdoutValidatorReportsTheViolatingNodeAndLine(t *testing.T) {
	validator := testShapedValidator(t, "PlaceFinder")

	err := validator.ValidateLine([]byte(`{"@id":"https://example.test/place/1","@type":"schema:Place"}`), 3)

	var shapeErr StreamShapeError
	require.ErrorAs(t, err, &shapeErr)
	require.Equal(t, 3, shapeErr.Line)
	require.Equal(t, testModuleNamespace+"PlaceFinder", shapeErr.Class)
	require.Equal(t, testModuleNamespace, shapeErr.Namespace)
	require.Len(t, shapeErr.Violations, 1)
	require.Contains(t, shapeErr.Violations[0], "<https://example.test/place/1>")
	require.Contains(t, shapeErr.Violations[0], "A place needs exactly one name")
	require.Contains(t, err.Error(), "stdout line 3")
}

// TestStdoutValidatorNamesTheConstraintWhenTheShapeHasNoMessage checks that a
// property shape written without sh:message still produces a readable
// violation.
func TestStdoutValidatorNamesTheConstraintWhenTheShapeHasNoMessage(t *testing.T) {
	validator := testShapedValidator(t, "PlaceFinder")

	err := validator.ValidateLine([]byte(`{"@id":"place/1","@type":"schema:Place","schema:name":"Lake Tahoe","schema:geo":"not an IRI"}`), 1)

	var shapeErr StreamShapeError
	require.ErrorAs(t, err, &shapeErr)
	require.Len(t, shapeErr.Violations, 1)
	require.Contains(t, shapeErr.Violations[0], "<https://schema.org/geo>")
	require.Contains(t, shapeErr.Violations[0], "sh:NodeKind")
}

// TestStdoutValidatorResolvesRelativeIRIsAgainstTheProject checks that a node
// is validated under the name it will be committed with, which is what a
// shape targeting a specific node has to be written against.
func TestStdoutValidatorResolvesRelativeIRIsAgainstTheProject(t *testing.T) {
	validator := testShapedValidator(t, "PlaceFinder")

	err := validator.ValidateLine([]byte(`{"@id":"place/1","@type":"schema:Place"}`), 1)

	var shapeErr StreamShapeError
	require.ErrorAs(t, err, &shapeErr)
	require.Contains(t, shapeErr.Violations[0], "<"+testProjectBase+"place/1>")
}

// TestStdoutValidatorLeavesNodesTheShapeDoesNotTargetAlone checks two things at
// once: a node of a class the stdoutShape does not target passes, and the
// ontology's taskShape, which does target schema:Person, is never applied to
// output.
func TestStdoutValidatorLeavesNodesTheShapeDoesNotTargetAlone(t *testing.T) {
	validator := testShapedValidator(t, "PlaceFinder")

	err := validator.ValidateLine([]byte(`{"@id":"person/bob","@type":"schema:Person","schema:name":"Bob"}`), 1)

	require.NoError(t, err)
}

func TestStdoutValidatorAppliesTheShapeOfASuperclass(t *testing.T) {
	validator := testShapedValidator(t, "CityFinder")

	err := validator.ValidateLine([]byte(`{"@id":"place/1","@type":"schema:Place"}`), 1)

	var shapeErr StreamShapeError
	require.ErrorAs(t, err, &shapeErr)
	require.Equal(t, testModuleNamespace+"CityFinder", shapeErr.Class)
}

func TestStdoutValidatorRejectsInvalidJSONWithItsLineNumber(t *testing.T) {
	validator := testShapedValidator(t, "PlaceFinder")

	err := validator.ValidateLine([]byte(`{"@id":`), 7)

	require.ErrorContains(t, err, "invalid JSON on stdout line 7")
}

func TestStdoutValidatorRefusesUnsupportedConstraints(t *testing.T) {
	ontology, err := parseModuleOntology(testModuleNamespace, []byte(`{
		"@context": {
			"schema": "https://schema.org/",
			"owl": "http://www.w3.org/2002/07/owl#",
			"rdfs": "http://www.w3.org/2000/01/rdf-schema#",
			"sh": "http://www.w3.org/ns/shacl#",
			"salmodule": "https://w3id.org/sal/salmodule#"
		},
		"@graph": [
			{
				"@id": "PlaceFinder",
				"@type": "owl:Class",
				"rdfs:subClassOf": {"@id": "salmodule:Task"},
				"salmodule:stdoutShape": {
					"@type": "sh:NodeShape",
					"sh:targetClass": {"@id": "schema:Place"},
					"sh:property": [{"sh:path": {"@id": "schema:name"}, "sh:languageIn": {"@list": ["en"]}}]
				}
			}
		]
	}`))
	require.NoError(t, err)

	validator, err := ontology.StdoutValidator(testModuleNamespace+"PlaceFinder", testProjectBase)

	require.Nil(t, validator)
	require.ErrorContains(t, err, "sh:languageIn")
	require.ErrorContains(t, err, testModuleNamespace+"PlaceFinder")
}

// TestStdoutValidatorDoesNotFailOnWarnings checks that a shape whose severity
// is below sh:Violation reports rather than rejects.
func TestStdoutValidatorDoesNotFailOnWarnings(t *testing.T) {
	ontology, err := parseModuleOntology(testModuleNamespace, []byte(`{
		"@context": {
			"schema": "https://schema.org/",
			"owl": "http://www.w3.org/2002/07/owl#",
			"rdfs": "http://www.w3.org/2000/01/rdf-schema#",
			"sh": "http://www.w3.org/ns/shacl#",
			"salmodule": "https://w3id.org/sal/salmodule#"
		},
		"@graph": [
			{
				"@id": "PlaceFinder",
				"@type": "owl:Class",
				"rdfs:subClassOf": {"@id": "salmodule:Task"},
				"salmodule:stdoutShape": {
					"@type": "sh:NodeShape",
					"sh:targetClass": {"@id": "schema:Place"},
					"sh:property": [{"sh:path": {"@id": "schema:name"}, "sh:minCount": 1, "sh:severity": {"@id": "sh:Warning"}}]
				}
			}
		]
	}`))
	require.NoError(t, err)
	validator, err := ontology.StdoutValidator(testModuleNamespace+"PlaceFinder", testProjectBase)
	require.NoError(t, err)

	err = validator.ValidateLine([]byte(`{"@id":"place/1","@type":"schema:Place"}`), 1)

	require.NoError(t, err)
}

// TestRunTaskStopsAtTheFirstLineThatViolatesTheStdoutShape checks that a
// rejected line ends the run there: the error names the line, and the file a
// later line names is never copied out of the container.
func TestRunTaskStopsAtTheFirstLineThatViolatesTheStdoutShape(t *testing.T) {
	runner := &fakeRunner{
		ontology: testShapedOntology,
		runOutput: `{"@id":"place/1","@type":"schema:Place","schema:name":"Lake Tahoe"}` + "\n" +
			`{"@id":"place/2","@type":"schema:Place"}` + "\n" +
			`{"@id":"place/3","@type":"schema:Place","schema:name":"Lake Erie","schema:hasMap":{"@id":"file:///tmp/erie.png"}}` + "\n",
		containerFiles: map[string]string{"/tmp/erie.png": "png"},
	}
	resolver := newTestResolver(runner)
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)
	ontology, err := resolver.Ontology(context.Background(), ref)
	require.NoError(t, err)
	validator, err := ontology.StdoutValidator(testModuleNamespace+"PlaceFinder", testProjectBase)
	require.NoError(t, err)

	_, err = resolver.RunTask(context.Background(), ref, "{}", t.TempDir(), TaskStream{Validator: validator}, TaskStream{})

	var shapeErr StreamShapeError
	require.True(t, errors.As(err, &shapeErr), err)
	require.Equal(t, 2, shapeErr.Line)
	require.Empty(t, runner.copies)
}

// TestRunTaskValidatesEveryLineWhenAllConform is the happy path through the
// same code: a validator that rejects nothing changes nothing about the result.
func TestRunTaskValidatesEveryLineWhenAllConform(t *testing.T) {
	runner := &fakeRunner{
		ontology: testShapedOntology,
		runOutput: `{"@id":"place/1","@type":"schema:Place","schema:name":"Lake Tahoe"}` + "\n" +
			`{"@id":"place/2","@type":"schema:Place","schema:name":"Lake Erie"}` + "\n",
	}
	resolver := newTestResolver(runner)
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)
	ontology, err := resolver.Ontology(context.Background(), ref)
	require.NoError(t, err)
	validator, err := ontology.StdoutValidator(testModuleNamespace+"PlaceFinder", testProjectBase)
	require.NoError(t, err)

	var lines []string
	result, err := resolver.RunTask(context.Background(), ref, "{}", t.TempDir(), TaskStream{Validator: validator, Sink: collectLines(&lines)}, TaskStream{})

	require.NoError(t, err)
	require.Len(t, lines, 2)
	require.Equal(t, int64(len(runner.runOutput)), result.OutputBytes)
}

// testStderrShapedOntology declares a task class whose stderr nodes must carry
// a label, on top of the output shape every task of the module declares.
const testStderrShapedOntology = `{
	"@context": {
		"schema": "https://schema.org/",
		"owl": "http://www.w3.org/2002/07/owl#",
		"rdfs": "http://www.w3.org/2000/01/rdf-schema#",
		"sh": "http://www.w3.org/ns/shacl#",
		"salmodule": "https://w3id.org/sal/salmodule#"
	},
	"@graph": [
		{"@id": ".", "@type": "owl:Ontology"},
		{
			"@id": "StrictReporter",
			"@type": "owl:Class",
			"rdfs:subClassOf": {"@id": "salmodule:Task"},
			"salmodule:stderrShape": {
				"@type": "sh:NodeShape",
				"sh:targetClass": {"@id": "salmodule:Error"},
				"sh:property": [{"sh:path": {"@id": "rdfs:label"}, "sh:minCount": 1, "sh:message": "An error needs a label"}]
			}
		}
	]
}`

func TestStderrValidatorIsNilForAClassWithoutAStderrShape(t *testing.T) {
	ontology, err := parseModuleOntology(testModuleNamespace, []byte(testShapedOntology))
	require.NoError(t, err)

	validator, err := ontology.StderrValidator(testModuleNamespace+"PlaceFinder", testProjectBase)

	require.NoError(t, err)
	require.Nil(t, validator)
}

func TestStderrValidatorReportsTheViolatingNodeAndStream(t *testing.T) {
	ontology, err := parseModuleOntology(testModuleNamespace, []byte(testStderrShapedOntology))
	require.NoError(t, err)
	validator, err := ontology.StderrValidator(testModuleNamespace+"StrictReporter", testProjectBase)
	require.NoError(t, err)
	require.NotNil(t, validator)

	require.NoError(t, validator.ValidateLine([]byte(`{"@type":"salmodule:Error","rdfs:label":"Unreachable","rdfs:comment":"boom"}`), 1))
	err = validator.ValidateLine([]byte(`{"@type":"salmodule:Error","rdfs:comment":"boom"}`), 2)

	var shapeErr StreamShapeError
	require.True(t, errors.As(err, &shapeErr), err)
	require.Equal(t, "stderr", shapeErr.Stream)
	require.Equal(t, 2, shapeErr.Line)
	require.ErrorContains(t, err, "stderr line 2")
	require.ErrorContains(t, err, "salmodule:stderrShape")
	require.ErrorContains(t, err, "An error needs a label")
}

// A node on stderr that violates the class's stderrShape stops the task the
// way a stdout violation does, with the stream named.
func TestRunTaskStopsATaskWhoseStderrViolatesItsStderrShape(t *testing.T) {
	runner := &fakeRunner{
		ontology:  testStderrShapedOntology,
		runOutput: `{"@id":"place/1","@type":"schema:Place","schema:name":"Lake Tahoe"}` + "\n",
		runStderr: "fetching places\n" + `{"@type":"salmodule:Error","rdfs:comment":"boom"}` + "\n",
	}
	resolver := newTestResolver(runner)
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)
	ontology, err := resolver.Ontology(context.Background(), ref)
	require.NoError(t, err)
	validator, err := ontology.StderrValidator(testModuleNamespace+"StrictReporter", testProjectBase)
	require.NoError(t, err)

	_, err = resolver.RunTask(context.Background(), ref, "{}", t.TempDir(), TaskStream{}, TaskStream{Validator: validator})

	var shapeErr StreamShapeError
	require.True(t, errors.As(err, &shapeErr), err)
	require.Equal(t, "stderr", shapeErr.Stream)
	require.Equal(t, 2, shapeErr.Line, "the log line counts, since the line number names where in stderr the node is")
}
