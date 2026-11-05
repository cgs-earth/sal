package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/build/load"
	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"
)

const copyVocabulary = `@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix ex: <https://example.com/vocab/> .
ex: a owl:Ontology .
ex:Thing a rdfs:Class .
ex:name a owl:DatatypeProperty .
`

// uploadedCopy builds a project's .sal directory (a real triples table, a
// pinned vocabulary under blobs/, a STAC catalog, and config.jsonld), uploads
// it to a bucket root the way `sal upload --bucket gs://my-bucket/` does with
// the bucket held in a local directory, and returns that directory: the copy
// of the bucket a server would be handed.
func uploadedCopy(t *testing.T, withConfig bool) string {
	t.Helper()
	ctx := context.Background()
	salDir := filepath.Join(t.TempDir(), ".sal")
	dataDir := filepath.Join(salDir, "data")

	graph := rdflibgo.NewGraph()
	pkg.AddTriple(graph, rdflibgo.NewURIRefUnsafe("https://example.com/a"), rdflibgo.NewURIRefUnsafe("https://example.com/vocab/name"), rdflibgo.NewLiteral("A"))
	cfg := &load.LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: dataDir, Namespace: "proj"}
	require.NoError(t, load.WriteGraphToIceberg(ctx, graph, cfg, map[string]string{"sal.hash": "abc"}, ""))

	sum := sha256.Sum256([]byte(copyVocabulary))
	digest := hex.EncodeToString(sum[:])
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "blobs"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "blobs", digest), []byte(copyVocabulary), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "stac"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "stac", "catalog.json"), []byte(`{"type":"Catalog"}`), 0644))
	if withConfig {
		require.NoError(t, os.WriteFile(filepath.Join(salDir, "config.jsonld"), []byte(`{
  "@context": {"owl": "http://www.w3.org/2002/07/owl#", "dcterms": "http://purl.org/dc/terms/", "xsd": "http://www.w3.org/2001/XMLSchema#", "rdfs": "http://www.w3.org/2000/01/rdf-schema#", "schema": "https://schema.org/"},
  "@graph": [
    {"@id": ".", "@type": "schema:Project", "dcterms:title": "proj", "dcterms:description": "A project"},
    {"@id": "https://example.com/vocab/", "@type": "owl:Ontology", "rdfs:label": "https://example.com/vocab/", "owl:versionIRI": {"@id": "urn:sha256:`+digest+`"}, "dcterms:format": "text/turtle"}
  ]
}`), 0644))
	}

	bucket := t.TempDir()
	opener := func(ctx context.Context, _ string) (*blob.Bucket, error) {
		return fileblob.OpenBucket(bucket, &fileblob.Options{Metadata: fileblob.MetadataDontWrite})
	}
	require.NoError(t, deploy(ctx, dataDir, "gs://my-bucket/", opener))
	return bucket
}

// The layout a bucket-root upload writes is what `sal serve --data` accepts,
// and the blobs and the project config are found beside the namespace
// directory the table sits in.
func TestServeDataLocatesATableInABucketRootUpload(t *testing.T) {
	bucket := uploadedCopy(t, true)

	table, err := salsparql.LocateDataDirTable(bucket)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(bucket, "proj", "triples"), table.Path)
	require.Equal(t, bucket, table.DataRoot)
	require.FileExists(t, filepath.Join(bucket, "config.jsonld"))
	require.DirExists(t, filepath.Join(bucket, "blobs"))
	require.FileExists(t, filepath.Join(bucket, "stac", "catalog.json"))
}

// The storage layout reads the uploaded copy as files on disk, not at the
// gs:// paths its rewritten metadata records.
func TestStorageLayoutReadsAnUploadedCopyWithoutNetwork(t *testing.T) {
	bucket := uploadedCopy(t, true)
	table, err := salsparql.LocateDataDirTable(bucket)
	require.NoError(t, err)
	runner, err := table.Runner(context.Background(), 0)
	require.NoError(t, err)

	layout, err := runner.Layout(context.Background())
	require.NoError(t, err)
	require.NotZero(t, layout.DataFiles.Count)
}

// A data directory lists the vocabularies the project pinned, from the
// config.jsonld the upload carried, with no .sal project above it.
func TestServeDataListsThePinnedVocabulariesOfAnUploadedCopy(t *testing.T) {
	bucket := uploadedCopy(t, true)
	t.Chdir(t.TempDir())
	table, err := salsparql.LocateDataDirTable(bucket)
	require.NoError(t, err)
	runner, err := table.Runner(context.Background(), 0)
	require.NoError(t, err)

	listing, err := runner.Vocabularies(context.Background())
	require.NoError(t, err)
	var found bool
	for _, vocabulary := range listing.Vocabularies {
		if vocabulary.Namespace == "https://example.com/vocab/" {
			found = true
			require.Contains(t, vocabulary.Classes, "Thing")
			require.Contains(t, vocabulary.Properties, "name")
		}
	}
	require.True(t, found, "the pinned vocabulary is listed: %+v", listing.Vocabularies)
}

// A copy with no config.jsonld, such as one uploaded before the file was
// carried, still lists the built-in vocabularies rather than failing.
func TestServeDataWithoutAProjectConfigListsOnlyTheBuiltIns(t *testing.T) {
	bucket := uploadedCopy(t, false)
	t.Chdir(t.TempDir())
	table, err := salsparql.LocateDataDirTable(bucket)
	require.NoError(t, err)
	runner, err := table.Runner(context.Background(), 0)
	require.NoError(t, err)

	listing, err := runner.Vocabularies(context.Background())
	require.NoError(t, err)
	for _, vocabulary := range listing.Vocabularies {
		require.True(t, vocabulary.Builtin, "%s is not a built-in", vocabulary.Namespace)
	}
}

// The Stats tab's title, description, and vocabulary listing come from the
// same config.jsonld, with no git remote around the served directory to
// resolve the project's IRI against.
func TestServeDataNamesTheProjectFromTheUploadedConfig(t *testing.T) {
	bucket := uploadedCopy(t, true)
	t.Chdir(t.TempDir())
	table, err := salsparql.LocateDataDirTable(bucket)
	require.NoError(t, err)
	runner, err := table.Runner(context.Background(), 0)
	require.NoError(t, err)

	project := runner.Project()
	require.Equal(t, "proj", project.Title)
	require.Equal(t, "A project", project.Description)
}
