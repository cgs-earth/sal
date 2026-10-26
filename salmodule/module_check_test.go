package salmodule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const checkTestNamespace = "salmodule://github.com/test/stations/"

// checkTestOntology is a module ontology every check accepts. The tests below
// break one thing in it at a time and expect the problem on the line it is on:
// line 10 is the @graph, 11 the ontology node, 12 to 25 the task class with its
// taskShape (15 to 19) and stdoutShape (20 to 24), and 26 the property node.
const checkTestOntology = `{
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
        "sh:property": [{"sh:path": {"@id": "region"}, "sh:minCount": 1, "sh:datatype": {"@id": "xsd:string"}}]
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

// checkOntology runs the ontology checks over checkTestOntology with old
// replaced by new and returns each problem as reported.
func checkOntology(t *testing.T, old, new string) []string {
	t.Helper()
	document := strings.Replace(checkTestOntology, old, new, 1)
	require.NotEqual(t, checkTestOntology, document, "the test's edit changed nothing")
	var messages []string
	for _, err := range checkOntologyDocument("ontology.jsonld", []byte(document), checkTestNamespace) {
		require.IsType(t, ModuleError{}, err)
		messages = append(messages, err.Error())
	}
	return messages
}

func TestCheckModuleAcceptsTheExampleModule(t *testing.T) {
	require.Empty(t, CheckModule("../examples/salmodule/python-geoconnex", "salmodule://github.com/cgs-earth/sal/"))
}

func TestCheckOntologyAcceptsTheOntologyOfTheSalCLI(t *testing.T) {
	require.Empty(t, checkOntologyDocument("sal_ontology.jsonld", []byte(salOntology), "salmodule://github.com/cgs-earth/sal/"))
}

func TestCheckOntologyAcceptsAValidOntology(t *testing.T) {
	require.Empty(t, checkOntologyDocument("ontology.jsonld", []byte(checkTestOntology), checkTestNamespace))
}

func TestIsModuleRootNeedsBothFiles(t *testing.T) {
	dir := t.TempDir()
	require.False(t, IsModuleRoot(dir))

	require.NoError(t, os.WriteFile(filepath.Join(dir, OntologyFile), []byte(checkTestOntology), 0644))
	require.False(t, IsModuleRoot(dir))

	require.NoError(t, os.WriteFile(filepath.Join(dir, DockerfileName), []byte("FROM scratch\n"), 0644))
	require.True(t, IsModuleRoot(dir))
}

func TestCheckModuleReportsTheMissingFiles(t *testing.T) {
	dir := t.TempDir()

	problems := CheckModule(dir, checkTestNamespace)

	require.Len(t, problems, 2)
	require.Equal(t, filepath.Join(dir, DockerfileName), problems[0].(ModuleError).Path)
	require.ErrorContains(t, problems[0], "needs a Dockerfile")
	require.Equal(t, filepath.Join(dir, OntologyFile), problems[1].(ModuleError).Path)
	require.ErrorContains(t, problems[1], "needs an ontology.jsonld")
}

func TestCheckModuleChecksTheOntologyOnceBothFilesExist(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, DockerfileName), []byte("FROM scratch\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, OntologyFile), []byte(strings.Replace(checkTestOntology, `"@id": "."`, `"@id": "stations"`, 1)), 0644))

	problems := CheckModule(dir, checkTestNamespace)

	require.Len(t, problems, 1)
	require.Equal(t, ModuleError{Path: filepath.Join(dir, OntologyFile), Line: 11, Message: `The owl:Ontology node is named "@id": "."`}, problems[0])
}

func TestCheckOntologyReportsInvalidJSONOnItsLine(t *testing.T) {
	problems := checkOntology(t, `"@graph": [`, `"@graph": [,`)

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], "ontology.jsonld:10: invalid JSON:")
}

func TestCheckOntologyRequiresTheOntologyNodeToBeNamedDot(t *testing.T) {
	require.Equal(t, []string{`ontology.jsonld:11: The owl:Ontology node is named "@id": "."`}, checkOntology(t, `"@id": "."`, `"@id": "stations"`))
}

func TestCheckOntologyRequiresExactlyOneOntologyNode(t *testing.T) {
	problems := checkOntology(t, `{"@id": "region", "@type": "owl:DatatypeProperty"`, `{"@id": "other", "@type": "owl:Ontology"}, {"@id": "region", "@type": "owl:DatatypeProperty"`)

	require.Equal(t, []string{
		`ontology.jsonld:10: @graph must hold exactly one node typed owl:Ontology, named "@id": "."`,
		`ontology.jsonld:26: The owl:Ontology node is named "@id": "."`,
	}, problems)
}

func TestCheckOntologyRequiresATaskClass(t *testing.T) {
	problems := checkOntology(t, `{"@id": "salmodule:Task"}`, `{"@id": "schema:Thing"}`)

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], "ontology.jsonld:10: @graph must hold at least one owl:Class whose rdfs:subClassOf is salmodule:Task")
}

// The schema is the one place that refuses the inferred classes as parents,
// since sal itself still runs such a module with a warning.
func TestCheckOntologyRefusesAnInferredClassAsTheParent(t *testing.T) {
	problems := checkOntology(t, `{"@id": "salmodule:Task"}`, `{"@id": "salmodule:NodeProducer"}`)

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], "ontology.jsonld:10:")
	require.Contains(t, problems[0], "salmodule:NodeProducer, salmodule:NodeConsumer, and salmodule:NodeProcessor are inferred")
}

func TestCheckOntologyReportsASubClassOfWrittenAsAString(t *testing.T) {
	problems := checkOntology(t, `"rdfs:subClassOf": {"@id": "salmodule:Task"}`, `"rdfs:subClassOf": "salmodule:Task"`)

	require.Len(t, problems, 2)
	require.Contains(t, problems[0], "ontology.jsonld:10: @graph must hold at least one owl:Class")
	require.Equal(t, `ontology.jsonld:13: rdfs:subClassOf is an IRI reference, written {"@id": "salmodule:Task"}; a bare string is a literal that sal ignores`, problems[1])
}

func TestCheckOntologyReportsAnInvalidEnvironmentVariableName(t *testing.T) {
	require.Equal(t, []string{"ontology.jsonld:11: An environment variable name holds only letters, digits, and underscores and does not start with a digit"}, checkOntology(t, `"STATION_TASK"`, `"station task"`))
}

func TestCheckOntologyReportsABaseInTheContext(t *testing.T) {
	problems := checkOntology(t, `"schema": "https://schema.org/",`, `"@base": "https://example.org/", "schema": "https://schema.org/",`)

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], "ontology.jsonld:3: Do not set @base")
}

func TestCheckOntologyReportsAMissingContextOnce(t *testing.T) {
	require.Equal(t, []string{"ontology.jsonld:1: A SAL module ontology is an object holding only @context and @graph"}, checkOntology(t, `"@context"`, `"@contextt"`))
}

// A key that is neither prefixed nor defined in the @context expands to
// nothing, so JSON-LD drops it without a word; the schema allows it in a node,
// since a @context may define it, so the scan of the source is what finds it.
func TestCheckOntologyReportsAKeyJSONLDWouldDrop(t *testing.T) {
	problems := checkOntology(t, `"rdfs:comment": "Fetches every station in a region"`, `"comment": "Fetches every station in a region"`)

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], `ontology.jsonld:14: key "comment" is neither a prefixed term nor a term the @context defines, so JSON-LD drops it`)
}

func TestCheckOntologyAcceptsAKeyTheContextDefines(t *testing.T) {
	document := strings.Replace(checkTestOntology, `"schema": "https://schema.org/",`, `"schema": "https://schema.org/", "comment": "rdfs:comment",`, 1)
	document = strings.Replace(document, `"rdfs:comment": "Fetches every station in a region"`, `"comment": "Fetches every station in a region"`, 1)

	require.Empty(t, checkOntologyDocument("ontology.jsonld", []byte(document), checkTestNamespace))
}

// The schema refuses such a key inside a shape as well, on the line of the
// shape; the key is reported once, on its own line.
func TestCheckOntologyReportsADroppedKeyInAShapeOnce(t *testing.T) {
	problems := checkOntology(t, `"sh:minCount": 1, "sh:maxCount": 1`, `"minCount": 1, "sh:maxCount": 1`)

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], `ontology.jsonld:23: key "minCount" is neither a prefixed term`)
}

// A term of the module's own namespace that no node declares is reported on
// the line it is written on.
func TestCheckOntologyReportsAnUndeclaredTerm(t *testing.T) {
	require.Equal(t, []string{`ontology.jsonld:18: undefined term regionn: no node in @graph declares "@id": "regionn" with an @type`}, checkOntology(t, `{"@id": "region"}`, `{"@id": "regionn"}`))
}

func TestCheckOntologyReportsAnUndeclaredTermUsedAsAValue(t *testing.T) {
	require.Equal(t, []string{`ontology.jsonld:26: undefined term Region: no node in @graph declares "@id": "Region" with an @type`}, checkOntology(t, `"rdfs:range": {"@id": "xsd:string"}`, `"rdfs:range": {"@id": "Region"}`))
}

func TestCheckOntologyReportsAShapeReferencedButNeverDeclared(t *testing.T) {
	require.Equal(t, []string{`ontology.jsonld:20: undefined term PlaceShape: no node in @graph declares "@id": "PlaceShape" with an @type`}, checkOntology(t, `"salmodule:stdoutShape": {`, `"salmodule:stdinShape": {"@id": "PlaceShape"}, "salmodule:stdoutShape": {`))
}

// A node that says something about a term without typing it is reported on
// its own line rather than on the line of a bare reference to the term.
func TestCheckOntologyRequiresADeclaredTermToHaveAType(t *testing.T) {
	require.Equal(t, []string{`ontology.jsonld:26: undefined term region: no node in @graph declares "@id": "region" with an @type`}, checkOntology(t, `{"@id": "region", "@type": "owl:DatatypeProperty", `, `{"@id": "region", `))
}

// A shape written inline is a blank node the schema has checked on the line
// it is written on; the SHACL reading of the same shape adds nothing.
func TestCheckOntologyReportsAnInlineIllFormedPropertyShapeOnce(t *testing.T) {
	require.Equal(t, []string{`ontology.jsonld:18: sh:path is an IRI reference, written {"@id": "schema:name"}`}, checkOntology(t, `{"sh:path": {"@id": "region"}, `, `{"sh:path": [{"@id": "region"}, {"@id": "schema:name"}], `))
}

// The schema only knows a node is a property shape by its @type or its
// sh:path, so a node of another type referenced from sh:property is a shape
// only the SHACL implementation that will enforce it can refuse.
func TestCheckOntologyReportsAnIllFormedShapeTheSchemaCannotSee(t *testing.T) {
	document := strings.Replace(checkTestOntology, `"sh:property": [{"sh:path": {"@id": "schema:name"}, "sh:minCount": 1, "sh:maxCount": 1, "sh:nodeKind": {"@id": "sh:Literal"}}]`, `"sh:property": [{"@id": "NameShape"}]`, 1)
	document = strings.Replace(document, `{"@id": "region", "@type": "owl:DatatypeProperty"`, `{"@id": "NameShape", "@type": "owl:Thing", "sh:minCount": 1},
    {"@id": "region", "@type": "owl:DatatypeProperty"`, 1)

	problems := checkOntologyDocument("ontology.jsonld", []byte(document), checkTestNamespace)

	require.Len(t, problems, 1)
	require.Equal(t, 26, problems[0].(ModuleError).Line)
	require.ErrorContains(t, problems[0], "invalid SHACL shape: property shape <"+checkTestNamespace+"NameShape>")
	require.ErrorContains(t, problems[0], "has no sh:path")
}

func TestCheckOntologyReportsAnUnsupportedStdoutConstraintOnce(t *testing.T) {
	require.Equal(t, []string{"ontology.jsonld:23: sal does not support sh:uniqueLang in a salmodule:stdoutShape"}, checkOntology(t, `"sh:nodeKind": {"@id": "sh:Literal"}`, `"sh:uniqueLang": true`))
}

// The schema looks only at the shape written under salmodule:stdoutShape and
// its own property shapes; a constraint further away, in a shape the stdout
// shape refers to, is found the way a run would find it.
func TestCheckOntologyReportsAnUnsupportedConstraintInAReferencedStdoutShape(t *testing.T) {
	document := strings.Replace(checkTestOntology, `"salmodule:stdoutShape": {`, `"salmodule:stdoutShape": {"@id": "PlaceShape"},
      "salmodule:stdinShape": {`, 1)
	document = strings.Replace(document, `{"@id": "region", "@type": "owl:DatatypeProperty"`, `{"@id": "PlaceShape", "@type": "sh:NodeShape", "sh:property": [{"sh:path": {"@id": "schema:name"}, "sh:uniqueLang": true}]},
    {"@id": "region", "@type": "owl:DatatypeProperty"`, 1)

	problems := checkOntologyDocument("ontology.jsonld", []byte(document), checkTestNamespace)

	require.Len(t, problems, 1)
	require.Equal(t, ModuleError{Path: "ontology.jsonld", Line: 12, Message: "the salmodule:stdoutShape of " + checkTestNamespace + "StationFetcher uses sh:uniqueLang, which SAL does not support"}, problems[0])
}

func TestCheckOntologyReportsAnInvalidNodeKind(t *testing.T) {
	problems := checkOntology(t, `{"@id": "sh:Literal"}`, `"sh:Literal"`)

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], "ontology.jsonld:23: sh:nodeKind is one of sh:IRI, sh:BlankNode, sh:Literal")
}

func TestCheckOntologyReportsACountThatIsNotAnInteger(t *testing.T) {
	require.Equal(t, []string{"ontology.jsonld:18: A count is a non-negative integer"}, checkOntology(t, `"sh:minCount": 1, "sh:datatype"`, `"sh:minCount": "one", "sh:datatype"`))
}

func TestCheckOntologyReportsAListWrittenAsAnArray(t *testing.T) {
	problems := checkOntology(t, `"sh:nodeKind": {"@id": "sh:Literal"}`, `"sh:in": ["a", "b"]`)

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], `ontology.jsonld:23: An RDF list is written {"@list": [ ... ]}`)
}

func TestCheckOntologyReportsAShapeWithoutItsType(t *testing.T) {
	problems := checkOntology(t, "\"@type\": \"sh:NodeShape\",\n        \"sh:targetClass\": {\"@id\": \"StationFetcher\"}", "\"sh:targetClass\": {\"@id\": \"StationFetcher\"}")

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], `ontology.jsonld:15: A shape is either written inline with "@type": "sh:NodeShape"`)
}

// A value that matches none of the forms a property allows is reported as one
// problem naming every form, not as one problem per form.
func TestCheckOntologyFoldsTheFormsAValueMayTake(t *testing.T) {
	require.Equal(t, []string{"ontology.jsonld:11: got number, want string or object or array"}, checkOntology(t, `"rdfs:label": "Weather stations"`, `"rdfs:label": 5`))
}

// A violation of a form with no message of its own is reported with the
// reason the form the author was writing failed, not with the reasons every
// other form failed to match.
func TestCheckOntologyReportsTheFormBeingWrittenNotTheOthers(t *testing.T) {
	problems := checkOntology(t, `{"sh:path": {"@id": "region"}, `, `{`)

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], "ontology.jsonld:18: A property shape is either written inline with an sh:path")
}
