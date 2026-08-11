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
	path := filepath.Join(t.TempDir(), "ontology.jsonld")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

// failOnPull is the pull for a project whose imports are all ontology documents,
// where reaching a registry at all would be the bug.
func failOnPull(t *testing.T) func(string) error {
	t.Helper()
	return func(iri string) error {
		t.Fatalf("unexpected artifact pull of %s", iri)
		return nil
	}
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
	path := filepath.Join(t.TempDir(), "ontology.jsonld")

	fetched := false
	err := importOntologies(graph, path, ontologyTestBase, func(string) (*rdflibgo.Graph, error) {
		fetched = true
		return nil, nil
	}, failOnPull(t))

	require.NoError(t, err)
	require.False(t, fetched)
	require.Equal(t, 0, graphTripleCount(graph))
}

func TestImportOntologiesMergesEveryImport(t *testing.T) {
	path := writeProjectOntology(t, `{
  "@context": {
    "dc": "http://purl.org/dc/elements/1.1/",
    "owl": "http://www.w3.org/2002/07/owl#"
  },
  "@id": ".",
  "@type": "owl:Ontology",
  "dc:title": "My Ontology",
  "owl:imports": [
    { "@id": "https://example.com/onto1" },
    { "@id": "https://example.com/onto2" }
  ]
}`)

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
	require.NoError(t, importOntologies(graph, path, ontologyTestBase, fetch, failOnPull(t)))

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
	ontology := filepath.Join(project, ".sal", "ontology.jsonld")
	require.NoError(t, os.WriteFile(ontology, []byte(`{"@id": "."}`), 0644))
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
	path := writeProjectOntology(t, `{
  "@context": { "owl": "http://www.w3.org/2002/07/owl#" },
  "@id": ".",
  "@type": "owl:Ontology",
  "owl:imports": [{ "@id": "https://example.com/missing" }]
}`)

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(ontologyTestBase))
	err := importOntologies(graph, path, ontologyTestBase, func(string) (*rdflibgo.Graph, error) {
		return nil, fmt.Errorf("bad response status code: 404")
	}, failOnPull(t))

	require.ErrorContains(t, err, "import https://example.com/missing")
	require.ErrorContains(t, err, "404")
}

func TestImportOntologiesPullsAnOciArtifactAndKeepsItOutOfTheGraph(t *testing.T) {
	path := writeProjectOntology(t, `{
  "@context": { "owl": "http://www.w3.org/2002/07/owl#" },
  "@id": ".",
  "@type": "owl:Ontology",
  "owl:imports": [
    { "@id": "https://example.com/onto1" },
    { "@id": "oci://ghcr.io/cgs-earth/sal:e57e9af" }
  ]
}`)

	// the ontology file is a build source, so its statements are already in the
	// graph by the time the imports are resolved
	graph := rdflibgo.NewGraph(rdflibgo.WithBase(ontologyTestBase))
	project := rdflibgo.NewURIRefUnsafe(ontologyTestBase)
	imports := rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#imports")
	artifact := rdflibgo.NewURIRefUnsafe("oci://ghcr.io/cgs-earth/sal:e57e9af")
	graph.Add(project, imports, rdflibgo.NewURIRefUnsafe("https://example.com/onto1"))
	graph.Add(project, imports, artifact)

	var pulled []string
	fetch := func(iri string) (*rdflibgo.Graph, error) {
		imported := rdflibgo.NewGraph()
		imported.Add(rdflibgo.NewURIRefUnsafe(iri+"#Thing"), rdflibgo.RDF.Type, rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"))
		return imported, nil
	}
	pull := func(iri string) error {
		pulled = append(pulled, iri)
		return nil
	}

	require.NoError(t, importOntologies(graph, path, ontologyTestBase, fetch, pull))

	require.Equal(t, []string{"oci://ghcr.io/cgs-earth/sal:e57e9af"}, pulled)
	// the artifact reference is gone from the graph, while the ontology import
	// sitting next to it in the same file is untouched and was merged
	require.False(t, graph.Contains(project, imports, artifact))
	require.True(t, graph.Contains(project, imports, rdflibgo.NewURIRefUnsafe("https://example.com/onto1")))
	require.True(t, graph.Contains(
		rdflibgo.NewURIRefUnsafe("https://example.com/onto1#Thing"),
		rdflibgo.RDF.Type,
		rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"),
	))
}

// A module's vocabulary is an ontology document like any other; only where it is
// fetched from differs, so it is merged rather than pulled to disk.
func TestImportOntologiesMergesASalModuleOntology(t *testing.T) {
	path := writeProjectOntology(t, `{
  "@context": { "owl": "http://www.w3.org/2002/07/owl#" },
  "@id": ".",
  "@type": "owl:Ontology",
  "owl:imports": [{ "@id": "salmodule://github.com/adplincinst/sample-salmodule-1" }]
}`)

	var requested []string
	fetch := func(iri string) (*rdflibgo.Graph, error) {
		requested = append(requested, iri)
		imported := rdflibgo.NewGraph()
		imported.Add(
			rdflibgo.NewURIRefUnsafe(iri+"/EducationalHistoryFinder"),
			rdflibgo.RDF.Type,
			rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"),
		)
		return imported, nil
	}

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(ontologyTestBase))
	require.NoError(t, importOntologies(graph, path, ontologyTestBase, fetch, failOnPull(t)))

	require.Equal(t, []string{"salmodule://github.com/adplincinst/sample-salmodule-1"}, requested)
	require.True(t, graph.Contains(
		rdflibgo.NewURIRefUnsafe("salmodule://github.com/adplincinst/sample-salmodule-1/EducationalHistoryFinder"),
		rdflibgo.RDF.Type,
		rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"),
	))
}

func TestImportOntologiesReportsWhichArtifactCouldNotBePulled(t *testing.T) {
	path := writeProjectOntology(t, `{
  "@context": { "owl": "http://www.w3.org/2002/07/owl#" },
  "@id": ".",
  "@type": "owl:Ontology",
  "owl:imports": [{ "@id": "oci://ghcr.io/cgs-earth/sal:e57e9af" }]
}`)

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(ontologyTestBase))
	err := importOntologies(graph, path, ontologyTestBase, nil, func(string) error {
		return fmt.Errorf("unauthorized")
	})

	require.ErrorContains(t, err, "import oci://ghcr.io/cgs-earth/sal:e57e9af")
	require.ErrorContains(t, err, "unauthorized")
}
