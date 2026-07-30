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

	graph, err := testTaskOntology(t).GraphFromTaskOutput([]byte(output))

	require.NoError(t, err)
	require.True(t, graphContains(graph, "https://example.test/person/bob", "https://schema.org/name", "Bob"))
	require.True(t, graphContains(graph, "https://example.test/person/bob", rdflibgo.RDF.Type.Value(), "https://schema.org/Person"))
}

func TestGraphFromTaskOutputReadsEveryOutputLine(t *testing.T) {
	output := "{\"@id\":\"https://example.test/a\",\"schema:name\":\"A\"}\n\n{\"@id\":\"https://example.test/b\",\"schema:name\":\"B\"}\n"

	graph, err := testTaskOntology(t).GraphFromTaskOutput([]byte(output))

	require.NoError(t, err)
	require.True(t, graphContains(graph, "https://example.test/a", "https://schema.org/name", "A"))
	require.True(t, graphContains(graph, "https://example.test/b", "https://schema.org/name", "B"))
}

func TestGraphFromTaskOutputReportsModuleErrors(t *testing.T) {
	output := `{"@type":"salmodule:Error","rdfs:comment":"Failed to fetch data: status code = 500"}`

	_, err := testTaskOntology(t).GraphFromTaskOutput([]byte(output))

	require.Error(t, err)
	require.Contains(t, err.Error(), "Failed to fetch data: status code = 500")
	require.Contains(t, err.Error(), testModuleNamespace)
}

func TestGraphFromTaskOutputRejectsNonJSONOutput(t *testing.T) {
	_, err := testTaskOntology(t).GraphFromTaskOutput([]byte("Traceback (most recent call last):"))

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid JSON on output line 1")
}

func TestGraphFromTaskOutputAcceptsEmptyOutput(t *testing.T) {
	graph, err := testTaskOntology(t).GraphFromTaskOutput([]byte("\n  \n"))

	require.NoError(t, err)
	require.Equal(t, 0, graph.Len())
}
