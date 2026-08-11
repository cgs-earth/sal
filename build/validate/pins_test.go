package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const testVocabularyNamespace = "https://vocab.test/things#"

const testVocabularyDocument = `@prefix owl: <http://www.w3.org/2002/07/owl#> .
<https://vocab.test/things#Widget> a owl:Class .
`

// newTestPins returns a pin store backed by a temporary project that serves a
// fixed document instead of dereferencing anything.
func newTestPins(t *testing.T, projectDir string, body string, fetches *int) *PinnedVocabularies {
	t.Helper()

	pins, err := LoadPinnedVocabularies(filepath.Join(projectDir, "ns-prefix-versions.jsonld"), filepath.Join(projectDir, "data"))
	require.NoError(t, err)
	pins.Fetch = func(string) ([]byte, string, PinnedVersion, error) {
		if fetches != nil {
			*fetches++
		}
		return []byte(body), "text/turtle", PinnedVersion{}, nil
	}
	return pins
}

func TestSaveWritesTheDocumentUnderItsHashAndPinsThatVersion(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, testVocabularyDocument, nil)

	_, mediaType, pinned, err := pins.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.False(t, pinned)
	require.Equal(t, "text/turtle", mediaType)
	require.NoError(t, pins.Save())

	sum := sha256.Sum256([]byte(testVocabularyDocument))
	digest := hex.EncodeToString(sum[:])
	document, err := os.ReadFile(filepath.Join(projectDir, "data", digest))
	require.NoError(t, err)
	require.Equal(t, testVocabularyDocument, string(document))

	content, err := os.ReadFile(filepath.Join(projectDir, "ns-prefix-versions.jsonld"))
	require.NoError(t, err)
	var doc pinnedVocabulariesDocument
	require.NoError(t, json.Unmarshal(content, &doc))
	require.Equal(t, owlNamespaceIRI, doc.Context["owl"])
	require.Len(t, doc.Graph, 1)
	require.Equal(t, testVocabularyNamespace, doc.Graph[0].ID)
	require.Equal(t, "owl:Ontology", doc.Graph[0].Type)
	require.Equal(t, "urn:sha256:"+digest, doc.Graph[0].VersionIRI.ID)
	require.Equal(t, "text/turtle", doc.Graph[0].Format)
	require.NotNil(t, doc.Graph[0].Modified)
}

// A salmodule:// vocabulary's Fetch reports the git commit hash of the module
// repository instead of leaving the version to be derived from the document,
// since code the module runs can change without its ontology document changing.
func TestADocumentPinnedByFetchIsRecordedAtTheCommitHashRatherThanItsDigest(t *testing.T) {
	projectDir := t.TempDir()
	pins, err := LoadPinnedVocabularies(filepath.Join(projectDir, "ns-prefix-versions.jsonld"), filepath.Join(projectDir, "data"))
	require.NoError(t, err)
	const commitHash = "abc123def456abc123def456abc123def456abc"
	pins.Fetch = func(string) ([]byte, string, PinnedVersion, error) {
		return []byte(testVocabularyDocument), "application/ld+json", PinnedVersion{Scheme: gitCommitVersionScheme, Value: commitHash}, nil
	}

	_, _, pinned, err := pins.Document(testVocabularyNamespace, "salmodule://example.test/module")
	require.NoError(t, err)
	require.False(t, pinned)
	require.NoError(t, pins.Save())

	document, err := os.ReadFile(filepath.Join(projectDir, "data", commitHash))
	require.NoError(t, err)
	require.Equal(t, testVocabularyDocument, string(document))

	content, err := os.ReadFile(filepath.Join(projectDir, "ns-prefix-versions.jsonld"))
	require.NoError(t, err)
	var doc pinnedVocabulariesDocument
	require.NoError(t, json.Unmarshal(content, &doc))
	require.Len(t, doc.Graph, 1)
	require.Equal(t, "urn:git-commit-hash:"+commitHash, doc.Graph[0].VersionIRI.ID)

	// reopening resolves the pin from disk rather than fetching it again
	reopened, err := LoadPinnedVocabularies(filepath.Join(projectDir, "ns-prefix-versions.jsonld"), filepath.Join(projectDir, "data"))
	require.NoError(t, err)
	reopened.Fetch = func(string) ([]byte, string, PinnedVersion, error) {
		t.Fatal("should not have been fetched again")
		return nil, "", PinnedVersion{}, nil
	}
	body, _, pinned, err := reopened.Document(testVocabularyNamespace, "salmodule://example.test/module")
	require.NoError(t, err)
	require.True(t, pinned)
	require.Equal(t, testVocabularyDocument, string(body))
}

func TestAPinnedVocabularyIsResolvedFromDiskRatherThanFetched(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, testVocabularyDocument, nil)
	_, _, _, err := pins.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	fetches := 0
	reopened := newTestPins(t, projectDir, "this should never be fetched", &fetches)
	body, mediaType, pinned, err := reopened.Document(testVocabularyNamespace, "https://vocab.test/things")

	require.NoError(t, err)
	require.True(t, pinned)
	require.Zero(t, fetches)
	require.Equal(t, testVocabularyDocument, string(body))
	require.Equal(t, "text/turtle", mediaType)
}

func TestRefreshFetchesAndRepinsAVocabularyThatIsAlreadyPinned(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, testVocabularyDocument, nil)
	_, _, _, err := pins.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	const updated = testVocabularyDocument + "<https://vocab.test/things#Gadget> a owl:Class .\n"
	fetches := 0
	refreshed := newTestPins(t, projectDir, updated, &fetches)
	refreshed.Refresh = true
	body, _, pinned, err := refreshed.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.False(t, pinned)
	require.Equal(t, 1, fetches)
	require.Equal(t, updated, string(body))
	require.NoError(t, refreshed.Save())

	sum := sha256.Sum256([]byte(updated))
	content, err := os.ReadFile(filepath.Join(projectDir, "ns-prefix-versions.jsonld"))
	require.NoError(t, err)
	require.Contains(t, string(content), "urn:sha256:"+hex.EncodeToString(sum[:]))
}

// A build that resolved everything from its pins must not touch the lockfile,
// since a rewritten one would leave the project's git worktree dirty and the
// next build would refuse to run.
func TestSaveLeavesTheLockfileAloneWhenNothingChanged(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, testVocabularyDocument, nil)
	_, _, _, err := pins.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	path := filepath.Join(projectDir, "ns-prefix-versions.jsonld")
	before, err := os.Stat(path)
	require.NoError(t, err)

	reopened := newTestPins(t, projectDir, testVocabularyDocument, nil)
	_, _, _, err = reopened.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.NoError(t, reopened.Save())

	after, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, before.ModTime(), after.ModTime())
}

// .sal/data is not in git, so a fresh clone has the pins before it has the
// documents they name.
func TestAPinnedVocabularyMissingFromDiskIsFetchedAgain(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, testVocabularyDocument, nil)
	_, _, _, err := pins.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.NoError(t, pins.Save())
	require.NoError(t, os.RemoveAll(filepath.Join(projectDir, "data")))

	fetches := 0
	reopened := newTestPins(t, projectDir, testVocabularyDocument, &fetches)
	body, _, pinned, err := reopened.Document(testVocabularyNamespace, "https://vocab.test/things")

	require.NoError(t, err)
	require.False(t, pinned)
	require.Equal(t, 1, fetches)
	require.Equal(t, testVocabularyDocument, string(body))
}

func TestADocumentThatNoLongerHashesToItsPinnedVersionIsFetchedAgain(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, testVocabularyDocument, nil)
	_, _, _, err := pins.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	sum := sha256.Sum256([]byte(testVocabularyDocument))
	tampered := filepath.Join(projectDir, "data", hex.EncodeToString(sum[:]))
	require.NoError(t, os.WriteFile(tampered, []byte("# not what was pinned\n"), 0644))

	fetches := 0
	reopened := newTestPins(t, projectDir, testVocabularyDocument, &fetches)
	body, _, _, err := reopened.Document(testVocabularyNamespace, "https://vocab.test/things")

	require.NoError(t, err)
	require.Equal(t, 1, fetches)
	require.Equal(t, testVocabularyDocument, string(body))
}

func TestTwoNamespacesServedByOneDocumentAreFetchedOnce(t *testing.T) {
	projectDir := t.TempDir()
	fetches := 0
	pins := newTestPins(t, projectDir, testVocabularyDocument, &fetches)

	_, _, _, err := pins.Document("https://vocab.test/things#", "https://vocab.test/things")
	require.NoError(t, err)
	_, _, _, err = pins.Document("https://vocab.test/things/", "https://vocab.test/things")
	require.NoError(t, err)

	require.Equal(t, 1, fetches)
	require.Len(t, pins.Documents(), 1)
}

func TestAnEphemeralStoreWritesNothing(t *testing.T) {
	projectDir := t.TempDir()
	pins := EphemeralVocabularies()
	pins.Fetch = func(string) ([]byte, string, PinnedVersion, error) {
		return []byte(testVocabularyDocument), "text/turtle", PinnedVersion{}, nil
	}

	_, _, pinned, err := pins.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.False(t, pinned)
	require.NoError(t, pins.Save())

	entries, err := os.ReadDir(projectDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestAppendProvenanceAddsAnOntologyNodeForEveryPin(t *testing.T) {
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, testVocabularyDocument, nil)
	_, mediaType, _, err := pins.Document(testVocabularyNamespace, "https://vocab.test/things")
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	sum := sha256.Sum256([]byte(testVocabularyDocument))
	digest := hex.EncodeToString(sum[:])

	graph := rdflibgo.NewGraph()
	pins.AppendProvenance(graph)

	subject := rdflibgo.NewURIRefUnsafe(testVocabularyNamespace)
	require.True(t, graph.Contains(subject, rdflibgo.RDF.Type, rdflibgo.NewURIRefUnsafe(owlNamespaceIRI+"Ontology")))
	require.True(t, graph.Contains(subject, rdflibgo.NewURIRefUnsafe(owlNamespaceIRI+"versionIRI"), rdflibgo.NewURIRefUnsafe("urn:sha256:"+digest)))
	require.True(t, graph.Contains(subject, rdflibgo.NewURIRefUnsafe(dctermsNamespaceIRI+"format"), rdflibgo.NewLiteral(mediaType)))

	var modifiedCount int
	predicate := rdflibgo.NewURIRefUnsafe(dctermsNamespaceIRI + "modified")
	graph.Triples(subject, &predicate, nil)(func(rdflibgo.Triple) bool {
		modifiedCount++
		return true
	})
	require.Equal(t, 1, modifiedCount)
}

func TestAppendProvenanceAddsNothingForAnEmptyStore(t *testing.T) {
	graph := rdflibgo.NewGraph()
	EphemeralVocabularies().AppendProvenance(graph)

	var count int
	graph.Triples(nil, nil, nil)(func(rdflibgo.Triple) bool {
		count++
		return true
	})
	require.Equal(t, 0, count)
}

func TestLoadingAVersionThatIsNotAHashIsAnError(t *testing.T) {
	projectDir := t.TempDir()
	path := filepath.Join(projectDir, "ns-prefix-versions.jsonld")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"@context": {"owl": "http://www.w3.org/2002/07/owl#"},
		"@graph": [{"@id": "https://vocab.test/things#", "@type": "owl:Ontology", "owl:versionIRI": {"@id": "https://vocab.test/things/1.0"}}]
	}`), 0644))

	_, err := LoadPinnedVocabularies(path, filepath.Join(projectDir, "data"))

	require.Error(t, err)
	require.Contains(t, err.Error(), "urn:sha256:")
}
