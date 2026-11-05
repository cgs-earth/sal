package validate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const seedTestDocument = `@prefix owl: <http://www.w3.org/2002/07/owl#> .
<https://vocab.test/things#Widget> a owl:Class .
`

// seedDocumentFor is the document served at a vocabulary's document URL: one
// class in the namespace the URL is the document of, as the real ones define.
func seedDocumentFor(u string) string {
	for _, namespace := range CommonVocabularies {
		if vocabularyDocumentURL(namespace) == u {
			return "@prefix owl: <http://www.w3.org/2002/07/owl#> .\n<" + namespace + "Widget> a owl:Class .\n"
		}
	}
	return seedTestDocument
}

// replaceSeedFetch makes seeding fetch from fetch for the rest of the test.
func replaceSeedFetch(t *testing.T, fetch func(context.Context, string) ([]byte, string, PinnedVersion, error)) {
	t.Helper()
	previous := SeedFetch
	SeedFetch = fetch
	t.Cleanup(func() { SeedFetch = previous })
}

func servesSeedDocument(_ context.Context, u string) ([]byte, string, PinnedVersion, error) {
	return []byte(seedDocumentFor(u)), "text/turtle", PinnedVersion{}, nil
}

func TestSeedVocabulariesStoresDocumentsWithoutPinningThem(t *testing.T) {
	replaceSeedFetch(t, servesSeedDocument)
	projectDir := t.TempDir()
	blobDir := filepath.Join(projectDir, "data", "blobs")

	seeded, err := SeedVocabularies(context.Background(), blobDir)
	require.NoError(t, err)
	require.ElementsMatch(t, CommonVocabularies, seeded)

	require.FileExists(t, filepath.Join(blobDir, SeedFile))
	require.FileExists(t, filepath.Join(blobDir, documentDigest([]byte(seedDocumentFor("https://schema.org/version/latest/schemaorg-current-https.jsonld")))))
	require.NoFileExists(t, filepath.Join(projectDir, "config.jsonld"), "a seed is not a pin")

	pins, err := LoadPinnedVocabularies(filepath.Join(projectDir, "config.jsonld"), blobDir)
	require.NoError(t, err)
	require.Empty(t, pins.IDs(), "no vocabulary is listed until a build uses it")
}

func TestSeedVocabulariesSkipsOneThatCannotBeFetchedOrRead(t *testing.T) {
	replaceSeedFetch(t, func(ctx context.Context, u string) ([]byte, string, PinnedVersion, error) {
		switch u {
		case "http://purl.org/dc/terms/":
			return nil, "", PinnedVersion{}, errors.New("i/o timeout")
		case "http://xmlns.com/foaf/0.1/":
			return []byte("<html>not rdf</html>"), "text/html", PinnedVersion{}, nil
		}
		return servesSeedDocument(ctx, u)
	})

	seeded, err := SeedVocabularies(context.Background(), t.TempDir())
	require.NoError(t, err)
	require.NotContains(t, seeded, "http://purl.org/dc/terms/")
	require.NotContains(t, seeded, "http://xmlns.com/foaf/0.1/")
	require.Contains(t, seeded, "https://schema.org/")
}

func TestSeedVocabulariesDoesNotFetchWhatIsAlreadySeeded(t *testing.T) {
	var fetches atomic.Int32
	replaceSeedFetch(t, func(ctx context.Context, u string) ([]byte, string, PinnedVersion, error) {
		fetches.Add(1)
		return servesSeedDocument(ctx, u)
	})
	blobDir := t.TempDir()
	_, err := SeedVocabularies(context.Background(), blobDir)
	require.NoError(t, err)
	first := fetches.Load()

	_, err = SeedVocabularies(context.Background(), blobDir)
	require.NoError(t, err)
	require.Equal(t, first, fetches.Load())
}

func TestADocumentSeededForANamespaceIsPinnedWhenABuildUsesItWithoutFetching(t *testing.T) {
	replaceSeedFetch(t, servesSeedDocument)
	projectDir := t.TempDir()
	blobDir := filepath.Join(projectDir, "data")
	_, err := SeedVocabularies(context.Background(), blobDir)
	require.NoError(t, err)

	pins, err := LoadPinnedVocabularies(filepath.Join(projectDir, "config.jsonld"), blobDir)
	require.NoError(t, err)
	pins.Fetch = func(context.Context, string) ([]byte, string, PinnedVersion, error) {
		return nil, "", PinnedVersion{}, errors.New("the network is down")
	}

	body, mediaType, _, err := pins.Document(context.Background(), "https://schema.org/", "https://schema.org/version/latest/schemaorg-current-https.jsonld")
	require.NoError(t, err)
	require.Equal(t, seedDocumentFor("https://schema.org/version/latest/schemaorg-current-https.jsonld"), string(body))
	require.Equal(t, "text/turtle", mediaType)
	require.Equal(t, []string{"https://schema.org/"}, pins.IDs(), "only the namespace that was used is pinned")

	require.NoError(t, pins.Save())
	reloaded, err := LoadPinnedVocabularies(filepath.Join(projectDir, "config.jsonld"), blobDir)
	require.NoError(t, err)
	require.Equal(t, []string{"https://schema.org/"}, reloaded.IDs())
}

func TestNoCacheIgnoresTheSeededDocuments(t *testing.T) {
	replaceSeedFetch(t, servesSeedDocument)
	projectDir := t.TempDir()
	blobDir := filepath.Join(projectDir, "data")
	_, err := SeedVocabularies(context.Background(), blobDir)
	require.NoError(t, err)

	pins, err := LoadPinnedVocabularies(filepath.Join(projectDir, "config.jsonld"), blobDir)
	require.NoError(t, err)
	pins.Refresh = true
	fetched := false
	pins.Fetch = func(context.Context, string) ([]byte, string, PinnedVersion, error) {
		fetched = true
		return []byte(seedTestDocument), "text/turtle", PinnedVersion{}, nil
	}
	_, _, _, err = pins.Document(context.Background(), "https://schema.org/", "https://schema.org/x")
	require.NoError(t, err)
	require.True(t, fetched)
}

func TestASeededDocumentThatNoLongerHashesIsFetchedInstead(t *testing.T) {
	replaceSeedFetch(t, servesSeedDocument)
	projectDir := t.TempDir()
	blobDir := filepath.Join(projectDir, "data")
	_, err := SeedVocabularies(context.Background(), blobDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, documentDigest([]byte(seedDocumentFor("https://schema.org/version/latest/schemaorg-current-https.jsonld")))), []byte("tampered"), 0644))

	pins, err := LoadPinnedVocabularies(filepath.Join(projectDir, "config.jsonld"), blobDir)
	require.NoError(t, err)
	pins.Fetch = func(context.Context, string) ([]byte, string, PinnedVersion, error) {
		return []byte("fresh"), "text/turtle", PinnedVersion{}, nil
	}
	body, _, _, err := pins.Document(context.Background(), "https://schema.org/", "https://schema.org/x")
	require.NoError(t, err)
	require.Equal(t, "fresh", string(body))
}

func zeroBackoff(t *testing.T) {
	t.Helper()
	previous := vocabularyFetchBackoff
	vocabularyFetchBackoff = 0
	t.Cleanup(func() { vocabularyFetchBackoff = previous })
}

func TestFetchRetriesAServerError(t *testing.T) {
	zeroBackoff(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/turtle")
		_, _ = w.Write([]byte(seedTestDocument))
	}))
	defer server.Close()

	body, mediaType, _, err := fetchVocabularyDocument(context.Background(), server.URL)
	require.NoError(t, err)
	require.Equal(t, seedTestDocument, string(body))
	require.Equal(t, "text/turtle", mediaType)
	require.EqualValues(t, 3, requests.Load())
}

func TestFetchDoesNotRetryANotFound(t *testing.T) {
	zeroBackoff(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, _, _, err := fetchVocabularyDocument(context.Background(), server.URL)
	require.ErrorContains(t, err, "bad response status code: 404")
	require.EqualValues(t, 1, requests.Load())
}

func TestFetchExplainsAnUnreachableHost(t *testing.T) {
	zeroBackoff(t)
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	_, _, _, err := fetchVocabularyDocument(context.Background(), url)
	require.ErrorContains(t, err, "could not be reached after 4 attempts")
	require.ErrorContains(t, err, "sal init")
}
