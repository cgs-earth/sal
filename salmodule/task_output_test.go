package salmodule

import (
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func testTaskOntology(t *testing.T) *ModuleOntology {
	t.Helper()

	ontology, err := parseModuleOntology(testModuleNamespace, []byte(testOntology))
	require.NoError(t, err)
	return ontology
}

func graphContains(graph *rdflibgo.Graph, subject, predicate, object string) bool {
	found := false
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if triple.Subject.String() != subject || triple.Predicate.Value() != predicate {
			return true
		}
		switch term := triple.Object.(type) {
		case rdflibgo.URIRef:
			found = found || term.Value() == object
		case rdflibgo.Literal:
			found = found || term.Lexical() == object
		}
		return true
	})
	return found
}

func TestGraphFromTaskOutputResolvesKeysWithOntologyContext(t *testing.T) {
	output := `{"@id":"https://example.test/person/bob","@type":"schema:Person","schema:name":"Bob"}`

	graph, err := testTaskOntology(t).GraphFromTaskOutput([]byte(output), testProjectBase)

	require.NoError(t, err)
	require.True(t, graphContains(graph, "https://example.test/person/bob", "https://schema.org/name", "Bob"))
	require.True(t, graphContains(graph, "https://example.test/person/bob", rdflibgo.RDF.Type.Value(), "https://schema.org/Person"))
}

// TestGraphFromTaskOutputResolvesRelativeIRIsAgainstTheProject checks that a
// relative IRI a task emits is named under the project the task was run for,
// not under the module's own namespace, which names its vocabulary rather than
// the instance data it produces.
func TestGraphFromTaskOutputResolvesRelativeIRIsAgainstTheProject(t *testing.T) {
	output := `{"@id":"person/bob","@type":"schema:Person","schema:knows":{"@id":"person/alice"}}`

	graph, err := testTaskOntology(t).GraphFromTaskOutput([]byte(output), testProjectBase)

	require.NoError(t, err)
	require.True(t, graphContains(graph, testProjectBase+"person/bob", rdflibgo.RDF.Type.Value(), "https://schema.org/Person"))
	require.True(t, graphContains(graph, testProjectBase+"person/bob", "https://schema.org/knows", testProjectBase+"person/alice"))
	require.False(t, graphContains(graph, testModuleNamespace+"person/bob", rdflibgo.RDF.Type.Value(), "https://schema.org/Person"))
}

// TestGraphFromTaskOutputKeepsAbsoluteIRIs checks that a module remains free to
// name its output under whatever namespace it chooses by writing absolute IRIs.
func TestGraphFromTaskOutputKeepsAbsoluteIRIs(t *testing.T) {
	output := `{"@id":"https://www.usgs.gov/site/1","@type":"schema:Place","schema:name":"Site 1"}`

	graph, err := testTaskOntology(t).GraphFromTaskOutput([]byte(output), testProjectBase)

	require.NoError(t, err)
	require.True(t, graphContains(graph, "https://www.usgs.gov/site/1", "https://schema.org/name", "Site 1"))
}

func TestGraphFromTaskOutputReadsEveryOutputLine(t *testing.T) {
	output := "{\"@id\":\"https://example.test/a\",\"schema:name\":\"A\"}\n\n{\"@id\":\"https://example.test/b\",\"schema:name\":\"B\"}\n"

	graph, err := testTaskOntology(t).GraphFromTaskOutput([]byte(output), testProjectBase)

	require.NoError(t, err)
	require.True(t, graphContains(graph, "https://example.test/a", "https://schema.org/name", "A"))
	require.True(t, graphContains(graph, "https://example.test/b", "https://schema.org/name", "B"))
}

func TestGraphFromTaskOutputReportsModuleErrors(t *testing.T) {
	output := `{"@type":"salmodule:Error","rdfs:comment":"Failed to fetch data: status code = 500"}`

	_, err := testTaskOntology(t).GraphFromTaskOutput([]byte(output), testProjectBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "Failed to fetch data: status code = 500")
	require.Contains(t, err.Error(), testModuleNamespace)
}

func TestGraphFromTaskOutputRejectsNonJSONOutput(t *testing.T) {
	_, err := testTaskOntology(t).GraphFromTaskOutput([]byte("Traceback (most recent call last):"), testProjectBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid JSON on output line 1")
}

func TestGraphFromTaskOutputAcceptsEmptyOutput(t *testing.T) {
	graph, err := testTaskOntology(t).GraphFromTaskOutput([]byte("\n  \n"), testProjectBase)

	require.NoError(t, err)
	require.Equal(t, 0, graph.Len())
}
