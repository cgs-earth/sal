package salmodule

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/turtle"
)

const testProjectBase = "https://example.test/project/"

// testInstanceOntology declares the terms the project fixtures below configure
// their task instances with.
const testInstanceOntology = `{
	"@context": {
		"schema": "https://schema.org/",
		"owl": "http://www.w3.org/2002/07/owl#",
		"xsd": "http://www.w3.org/2001/XMLSchema#",
		"salmodule": "https://w3id.org/sal/cgs-earth/sal/ontology/salmodule#"
	},
	"@graph": [
		{"@id": ".", "@type": "owl:Ontology"},
		{"@id": "EducationalHistoryFinder", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"}},
		{"@id": "maxRetries", "@type": "owl:DatatypeProperty"},
		{"@id": "label", "@type": "owl:DatatypeProperty"},
		{"@id": "school", "@type": "owl:ObjectProperty"},
		{"@id": "credentials", "@type": "owl:ObjectProperty"},
		{"@id": "user", "@type": "owl:DatatypeProperty"}
	]
}`

// taskInstanceFor parses a project fixture and renders the task instance SAL
// would pass to the module for the <Finder> instance it declares.
func taskInstanceFor(t *testing.T, project string) string {
	t.Helper()

	ontology, err := parseModuleOntology(testModuleNamespace, []byte(testInstanceOntology))
	require.NoError(t, err)

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(testProjectBase))
	require.NoError(t, turtle.Parse(graph, bytes.NewReader([]byte(project)), turtle.WithBase(testProjectBase)))

	instance, err := ontology.TaskInstance(graph, rdflibgo.NewURIRefUnsafe(testProjectBase+"Finder"), testModuleNamespace+"EducationalHistoryFinder")
	require.NoError(t, err)
	return instance
}

const testInstancePrefixes = `
	@base <https://example.test/project/> .
	@prefix history: <salmodule://www.github.com/test/history-getter/> .
	@prefix salmodule: <https://w3id.org/sal/cgs-earth/sal/ontology/salmodule#> .
	@prefix schema: <https://schema.org/> .
	@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .
`

func TestTaskInstanceCarriesTheModulesOwnProperties(t *testing.T) {
	instance := taskInstanceFor(t, testInstancePrefixes+`
		<Finder> a history:EducationalHistoryFinder ;
			history:maxRetries "5"^^xsd:integer ;
			history:label "finder" .
	`)

	require.JSONEq(t, `{
		"@id": "https://example.test/project/Finder",
		"@type": "EducationalHistoryFinder",
		"maxRetries": {"@value": "5", "@type": "xsd:integer"},
		"label": "finder"
	}`, instance)
}

// A project describes its instance with terms from several vocabularies, but
// only the module knows what configures it, so nothing else is passed along.
func TestTaskInstanceOmitsPropertiesFromOtherVocabularies(t *testing.T) {
	instance := taskInstanceFor(t, testInstancePrefixes+`
		<Finder> a history:EducationalHistoryFinder ;
			a salmodule:NodeProcessor ;
			schema:name "described for the project, not for the module" ;
			salmodule:taskInstanceEnvVar "IGNORED" ;
			history:maxRetries "5"^^xsd:integer .
	`)

	require.JSONEq(t, `{
		"@id": "https://example.test/project/Finder",
		"@type": "EducationalHistoryFinder",
		"maxRetries": {"@value": "5", "@type": "xsd:integer"}
	}`, instance)
}

func TestTaskInstanceWritesRepeatedPropertiesAsAnArray(t *testing.T) {
	instance := taskInstanceFor(t, testInstancePrefixes+`
		<Finder> a history:EducationalHistoryFinder ;
			history:label "first", "second" .
	`)

	require.JSONEq(t, `{
		"@id": "https://example.test/project/Finder",
		"@type": "EducationalHistoryFinder",
		"label": ["first", "second"]
	}`, instance)
}

// A referenced node stays a reference, while an anonymous node has no identifier
// the module could dereference and so is inlined.
func TestTaskInstanceInlinesAnonymousNodesAndReferencesNamedOnes(t *testing.T) {
	instance := taskInstanceFor(t, testInstancePrefixes+`
		<Finder> a history:EducationalHistoryFinder ;
			history:school <https://example.test/school/mit> ;
			history:credentials [ history:user "bob" ] .
	`)

	require.JSONEq(t, `{
		"@id": "https://example.test/project/Finder",
		"@type": "EducationalHistoryFinder",
		"school": {"@id": "https://example.test/school/mit"},
		"credentials": {"user": "bob"}
	}`, instance)
}

func TestTaskInstanceKeepsLanguageTags(t *testing.T) {
	instance := taskInstanceFor(t, testInstancePrefixes+`
		<Finder> a history:EducationalHistoryFinder ;
			history:label "chercheur"@fr .
	`)

	require.JSONEq(t, `{
		"@id": "https://example.test/project/Finder",
		"@type": "EducationalHistoryFinder",
		"label": {"@value": "chercheur", "@language": "fr"}
	}`, instance)
}

// An IRI the ontology's @context cannot shorten has to survive in full, or the
// module would resolve it against its own vocabulary.
func TestTaskInstanceLeavesIRIsTheContextCannotShortenInFull(t *testing.T) {
	instance := taskInstanceFor(t, testInstancePrefixes+`
		@prefix ex: <https://vocab.example.test/> .

		<Finder> a history:EducationalHistoryFinder ;
			history:maxRetries "5"^^ex:retryCount .
	`)

	require.JSONEq(t, `{
		"@id": "https://example.test/project/Finder",
		"@type": "EducationalHistoryFinder",
		"maxRetries": {"@value": "5", "@type": "https://vocab.example.test/retryCount"}
	}`, instance)
}

func TestTaskInstanceForAnInstanceWithoutConfiguration(t *testing.T) {
	instance := taskInstanceFor(t, testInstancePrefixes+`
		<Finder> a history:EducationalHistoryFinder .
	`)

	require.JSONEq(t, `{
		"@id": "https://example.test/project/Finder",
		"@type": "EducationalHistoryFinder"
	}`, instance)
}

// An RDF list, written ( ... ) in Turtle, is ordered configuration: a list of
// sources to run in order, or the arguments of a command. It becomes a JSON
// array in that order, nested objects and all, which is what a module's
// @context declares a list-valued term as with @container: @list.
func TestTaskInstanceWritesAnRDFListAsAnOrderedArray(t *testing.T) {
	instance := taskInstanceFor(t, testInstancePrefixes+`
		<Finder> a history:EducationalHistoryFinder ;
			history:credentials (
				[ history:user "zoe" ; history:label ( "--verbose" "--retries" "3" ) ]
				[ history:user "adam" ; history:school <https://example.org/school> ]
			) .
	`)

	require.JSONEq(t, `{
		"@id": "https://example.test/project/Finder",
		"@type": "EducationalHistoryFinder",
		"credentials": [
			{"user": "zoe", "label": ["--verbose", "--retries", "3"]},
			{"user": "adam", "school": {"@id": "https://example.org/school"}}
		]
	}`, instance)
}

func TestTaskInstanceWritesAnEmptyRDFListAsAnEmptyArray(t *testing.T) {
	instance := taskInstanceFor(t, testInstancePrefixes+`
		<Finder> a history:EducationalHistoryFinder ;
			history:credentials ( ) .
	`)

	require.JSONEq(t, `{
		"@id": "https://example.test/project/Finder",
		"@type": "EducationalHistoryFinder",
		"credentials": []
	}`, instance)
}
