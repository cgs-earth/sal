package build

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/pkg"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const ontologyTestBase = "https://github.com/cgs-earth/sal/"

func writeProjectOntology(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ontology.ttl")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func graphTripleCount(graph *rdflibgo.Graph) int {
	var count int
	graph.Triples(nil, nil, nil)(func(rdflibgo.Triple) bool {
		count++
		return true
	})
	return count
}

func TestImportOntologiesDoesNothingWithoutAProjectOntology(t *testing.T) {
	graph := rdflibgo.NewGraph(rdflibgo.WithBase(ontologyTestBase))
	path := filepath.Join(t.TempDir(), "ontology.ttl")

	fetched := false
	err := importOntologies(graph, path, ontologyTestBase, func(string) (*rdflibgo.Graph, error) {
		fetched = true
		return nil, nil
	})

	require.NoError(t, err)
	require.False(t, fetched)
	require.Equal(t, 0, graphTripleCount(graph))
}

func TestImportOntologiesMergesEveryImport(t *testing.T) {
	path := writeProjectOntology(t, `@prefix dc: <http://purl.org/dc/elements/1.1/> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .

<.> a owl:Ontology ;
    dc:title "My Ontology" ;
    owl:imports <https://example.com/onto1>, <https://example.com/onto2> .
`)

	var requested []string
	fetch := func(iri string) (*rdflibgo.Graph, error) {
		requested = append(requested, iri)
		imported := rdflibgo.NewGraph()
		imported.Add(
			rdflibgo.NewURIRefUnsafe(iri+"#Thing"),
			rdflibgo.RDF.Type,
			rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"),
		)
		return imported, nil
	}

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(ontologyTestBase))
	require.NoError(t, importOntologies(graph, path, ontologyTestBase, fetch))

	require.Equal(t, []string{"https://example.com/onto1", "https://example.com/onto2"}, requested)
	// one statement per import; the project ontology's own statements arrive
	// with it as a source file rather than from here
	require.Equal(t, 2, graphTripleCount(graph))
	require.True(t, graph.Contains(
		rdflibgo.NewURIRefUnsafe("https://example.com/onto1#Thing"),
		rdflibgo.RDF.Type,
		rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"),
	))
}

func TestAppendProjectOntologyAddsTheOntologyToTheFilesBeingBuilt(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sal"), 0755))
	ontology := filepath.Join(project, ".sal", "ontology.ttl")
	require.NoError(t, os.WriteFile(ontology, []byte("# managed by sal import\n"), 0644))
	t.Chdir(project)

	files, err := appendProjectOntology([]string{filepath.Join(project, "data.ttl")})

	require.NoError(t, err)
	// t.Chdir resolves through symlinks on macOS, so compare against the path
	// the project reports for itself rather than the temp directory's name
	resolved, err := pkg.SalOntologyPath()
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(project, "data.ttl"), resolved}, files)

	// a project ontology already in the list is not added twice
	unchanged, err := appendProjectOntology(files)
	require.NoError(t, err)
	require.Equal(t, files, unchanged)
}

func TestAppendProjectOntologyLeavesTheFilesAloneWithoutOne(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sal"), 0755))
	t.Chdir(project)

	files, err := appendProjectOntology([]string{"data.ttl"})

	require.NoError(t, err)
	require.Equal(t, []string{"data.ttl"}, files)
}

func TestImportOntologiesReportsWhichImportCouldNotBeFetched(t *testing.T) {
	path := writeProjectOntology(t, `@prefix owl: <http://www.w3.org/2002/07/owl#> .

<.> a owl:Ontology ;
    owl:imports <https://example.com/missing> .
`)

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(ontologyTestBase))
	err := importOntologies(graph, path, ontologyTestBase, func(string) (*rdflibgo.Graph, error) {
		return nil, fmt.Errorf("bad response status code: 404")
	})

	require.ErrorContains(t, err, "import https://example.com/missing")
	require.ErrorContains(t, err, "404")
}
