package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const provTestDocument = `{
  "@context": {
    "dcterms": "http://purl.org/dc/terms/",
    "owl": "http://www.w3.org/2002/07/owl#",
    "rdfs": "http://www.w3.org/2000/01/rdf-schema#",
    "xsd": "http://www.w3.org/2001/XMLSchema#"
  },
  "@graph": [{
    "@id": "out/catalog/",
    "rdfs:label": "catalog/",
    "owl:versionIRI": {"@id": "urn:sha256:1111"},
    "dcterms:modified": {"@value": "2026-09-10T14:26:05Z", "@type": "xsd:dateTime"},
    "rdfs:comment": "Represents the directory out/catalog/ a SAL module task copied out of its container."
  }]
}`

// A directory an earlier run copied is described in the graph of a later
// build from its prov.jsonld record, so the table describes it whether or
// not the module ran again.
func TestAppendBlobProvenanceDescribesTheDirectoriesTheBlobStoreRecords(t *testing.T) {
	project := newPinsTestProject(t)
	blobDir := filepath.Join(project, ".sal", "data", "blobs")
	require.NoError(t, os.MkdirAll(blobDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, salmodule.ProvFile), []byte(provTestDocument), 0644))
	graph := rdflibgo.NewGraph()

	require.NoError(t, appendBlobProvenance(graph))

	require.True(t, graphHasTriple(graph, "urn:sha256:1111", "http://www.w3.org/2000/01/rdf-schema#label", "catalog/"))
	require.True(t, graphHasTriple(graph, "urn:sha256:1111", "http://purl.org/dc/terms/identifier", "out/catalog/"))
	require.True(t, graphHasTriple(graph, "urn:sha256:1111", "http://www.w3.org/2000/01/rdf-schema#comment", "Represents the directory out/catalog/ a SAL module task copied out of its container."))
	require.True(t, graph.Contains(rdflibgo.NewURIRefUnsafe("urn:sha256:1111"), rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#versionIRI"), rdflibgo.NewURIRefUnsafe("urn:sha256:1111")))
}

func TestAppendBlobProvenanceAddsNothingWithoutAProvFile(t *testing.T) {
	newPinsTestProject(t)
	graph := rdflibgo.NewGraph()

	require.NoError(t, appendBlobProvenance(graph))

	var count int
	graph.Triples(nil, nil, nil)(func(rdflibgo.Triple) bool {
		count++
		return true
	})
	require.Zero(t, count)
}

func TestAppendBlobProvenanceFailsOnAProvFileThatDoesNotParse(t *testing.T) {
	project := newPinsTestProject(t)
	blobDir := filepath.Join(project, ".sal", "data", "blobs")
	require.NoError(t, os.MkdirAll(blobDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, salmodule.ProvFile), []byte("{"), 0644))

	err := appendBlobProvenance(rdflibgo.NewGraph())

	require.ErrorContains(t, err, "copied directory provenance")
}
