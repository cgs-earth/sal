package salmodule

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cgs-earth/sal/pkg"
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
	pkg.Triples(graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
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

// parseTaskOutput feeds output to a TaskOutputParser line by line the way
// RunTask does and returns the graph it filled.
func parseTaskOutput(t *testing.T, ontology *ModuleOntology, output string) (*rdflibgo.Graph, error) {
	t.Helper()
	graph := rdflibgo.NewGraph(rdflibgo.WithBase(testProjectBase))
	parser := ontology.NewTaskOutputParser(graph, testProjectBase)
	for i, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			if err := parser.Line([]byte(trimmed), i+1); err != nil {
				return graph, err
			}
		}
	}
	return graph, parser.Finish()
}

func TestGraphFromTaskOutputResolvesKeysWithOntologyContext(t *testing.T) {
	output := `{"@id":"https://example.test/person/bob","@type":"schema:Person","schema:name":"Bob"}`

	graph, err := parseTaskOutput(t, testTaskOntology(t), output)

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

	graph, err := parseTaskOutput(t, testTaskOntology(t), output)

	require.NoError(t, err)
	require.True(t, graphContains(graph, testProjectBase+"person/bob", rdflibgo.RDF.Type.Value(), "https://schema.org/Person"))
	require.True(t, graphContains(graph, testProjectBase+"person/bob", "https://schema.org/knows", testProjectBase+"person/alice"))
	require.False(t, graphContains(graph, testModuleNamespace+"person/bob", rdflibgo.RDF.Type.Value(), "https://schema.org/Person"))
}

// TestGraphFromTaskOutputKeepsAbsoluteIRIs checks that a module remains free to
// name its output under whatever namespace it chooses by writing absolute IRIs.
func TestGraphFromTaskOutputKeepsAbsoluteIRIs(t *testing.T) {
	output := `{"@id":"https://www.usgs.gov/site/1","@type":"schema:Place","schema:name":"Site 1"}`

	graph, err := parseTaskOutput(t, testTaskOntology(t), output)

	require.NoError(t, err)
	require.True(t, graphContains(graph, "https://www.usgs.gov/site/1", "https://schema.org/name", "Site 1"))
}

func TestGraphFromTaskOutputReadsEveryOutputLine(t *testing.T) {
	output := "{\"@id\":\"https://example.test/a\",\"schema:name\":\"A\"}\n\n{\"@id\":\"https://example.test/b\",\"schema:name\":\"B\"}\n"

	graph, err := parseTaskOutput(t, testTaskOntology(t), output)

	require.NoError(t, err)
	require.True(t, graphContains(graph, "https://example.test/a", "https://schema.org/name", "A"))
	require.True(t, graphContains(graph, "https://example.test/b", "https://schema.org/name", "B"))
}

func TestGraphFromTaskOutputReportsModuleErrors(t *testing.T) {
	output := `{"@type":"salmodule:Error","rdfs:comment":"Failed to fetch data: status code = 500"}`

	_, err := parseTaskOutput(t, testTaskOntology(t), output)

	require.Error(t, err)
	require.Contains(t, err.Error(), "Failed to fetch data: status code = 500")
	require.Contains(t, err.Error(), testModuleNamespace)
}

func TestGraphFromTaskOutputRejectsNonJSONOutput(t *testing.T) {
	_, err := parseTaskOutput(t, testTaskOntology(t), "Traceback (most recent call last):")

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid JSON on output line 1")
}

// A batch is parsed as one JSON-LD document, which numbers its blank nodes
// from zero, so the blank nodes of one batch must not be mistaken for those of
// another.
func TestTaskOutputParserKeepsBlankNodesOfDifferentBatchesApart(t *testing.T) {
	var lines []string
	for i := 0; i < taskOutputBatchLines+1; i++ {
		lines = append(lines, `{"@id":"https://example.test/place/`+fmt.Sprint(i)+`","schema:geo":{"schema:latitude":`+fmt.Sprint(i)+`}}`)
	}

	graph, err := parseTaskOutput(t, testTaskOntology(t), strings.Join(lines, "\n"))

	require.NoError(t, err)
	blanks := map[string]bool{}
	pkg.Triples(graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if blank, ok := triple.Object.(rdflibgo.BNode); ok {
			blanks[blank.Value()] = true
		}
		return true
	})
	require.Len(t, blanks, taskOutputBatchLines+1)
	require.Equal(t, 2*(taskOutputBatchLines+1), pkg.GraphLen(graph))
}

// An error node and its message can arrive in different batches and still be
// reported together, since the message is read back out of the graph.
func TestTaskOutputParserReportsAnErrorSplitAcrossBatches(t *testing.T) {
	lines := []string{`{"@id":"https://example.test/failure","@type":"salmodule:Error"}`}
	for i := 0; i < taskOutputBatchLines; i++ {
		lines = append(lines, `{"@id":"https://example.test/a","schema:name":"A"}`)
	}
	lines = append(lines, `{"@id":"https://example.test/failure","rdfs:comment":"upstream is down"}`)

	_, err := parseTaskOutput(t, testTaskOntology(t), strings.Join(lines, "\n"))

	var taskErr TaskError
	require.ErrorAs(t, err, &taskErr)
	require.Equal(t, []string{"upstream is down"}, taskErr.Messages)
}

func TestTaskOutputParserCollectsTheTriplesNamingFiles(t *testing.T) {
	output := `{"@id":"https://example.test/dataset","schema:hasPart":{"@id":"file:///tmp/test.txt"},"schema:name":"a dataset"}`
	graph := rdflibgo.NewGraph(rdflibgo.WithBase(testProjectBase))
	parser := testTaskOntology(t).NewTaskOutputParser(graph, testProjectBase)

	require.NoError(t, parser.Line([]byte(output), 1))
	require.NoError(t, parser.Finish())

	require.Len(t, parser.FileTriples(), 1)
	require.Equal(t, "file:///tmp/test.txt", parser.FileTriples()[0].Object.(rdflibgo.URIRef).Value())
	require.Equal(t, 2, pkg.GraphLen(graph))
}

func TestTaskOutputParserRejectsALineThatIsNotAnObject(t *testing.T) {
	_, err := parseTaskOutput(t, testTaskOntology(t), `["not","a","node"]`)

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid JSON on output line 1")
}

func TestGraphFromTaskOutputAcceptsEmptyOutput(t *testing.T) {
	graph, err := parseTaskOutput(t, testTaskOntology(t), "\n  \n")

	require.NoError(t, err)
	require.Equal(t, 0, pkg.GraphLen(graph))
}
