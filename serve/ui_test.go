package serve

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// largestUIAsset returns the name and bytes of the biggest embedded asset, so the
// tests do not have to hardcode a Vite fingerprint that changes on every UI build.
func largestUIAsset(t *testing.T) (string, []byte) {
	t.Helper()
	dist, err := fs.Sub(uiAssets, "sal-ui/dist")
	require.NoError(t, err)

	var name string
	var content []byte
	require.NoError(t, fs.WalkDir(dist, ".", func(candidate string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || path.Ext(candidate) != ".js" {
			return err
		}
		body, err := fs.ReadFile(dist, candidate)
		if err != nil {
			return err
		}
		if len(body) > len(content) {
			name, content = candidate, body
		}
		return nil
	}))
	require.NotEmpty(t, name, "the embedded UI has no JavaScript to serve")
	return name, content
}

func newUIOnlyServer(t *testing.T) *httptest.Server {
	t.Helper()
	handler, err := uiHandler()
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// getAsset fetches an asset with an explicit Accept-Encoding, bypassing the
// transport's automatic gzip handling so the response headers stay untouched.
func getAsset(t *testing.T, server *httptest.Server, name string, acceptEncoding string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/"+name, http.NoBody)
	require.NoError(t, err)
	request.Header.Set("Accept-Encoding", acceptEncoding)

	response, err := http.DefaultTransport.RoundTrip(request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	return response
}

func TestUIServesGzippedAssetsWhenTheClientAcceptsGzip(t *testing.T) {
	name, raw := largestUIAsset(t)
	response := getAsset(t, newUIOnlyServer(t), name, "gzip, deflate, br")

	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "gzip", response.Header.Get("Content-Encoding"))
	require.Equal(t, "Accept-Encoding", response.Header.Get("Vary"))
	require.Contains(t, response.Header.Get("Content-Type"), "javascript")

	reader, err := gzip.NewReader(response.Body)
	require.NoError(t, err)
	decompressed, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, raw, decompressed, "the gzipped asset must decompress to the embedded bytes")
	require.Less(t, int(response.ContentLength), len(raw), "gzip should have made the asset smaller")
}

func TestUIServesIdentityWhenGzipIsNotAccepted(t *testing.T) {
	name, raw := largestUIAsset(t)
	response := getAsset(t, newUIOnlyServer(t), name, "identity")

	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Empty(t, response.Header.Get("Content-Encoding"))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, raw, body)
}

func TestUIKeepsTheImmutableCacheHeaderOnCompressedAssets(t *testing.T) {
	name, _ := largestUIAsset(t)
	require.True(t, strings.HasPrefix(name, "assets/"), "expected the largest chunk under assets/")

	response := getAsset(t, newUIOnlyServer(t), name, "gzip")

	require.Equal(t, "public, max-age=31536000, immutable", response.Header.Get("Cache-Control"))
}

func TestAcceptsGzipReadsTheHeader(t *testing.T) {
	require.True(t, acceptsGzip("gzip"))
	require.True(t, acceptsGzip("br, gzip;q=0.8"))
	require.True(t, acceptsGzip("deflate, GZIP"))
	require.False(t, acceptsGzip(""))
	require.False(t, acceptsGzip("br, deflate"))
	require.False(t, acceptsGzip("gzip;q=0"), "a client can refuse gzip outright")
}

func TestCompressUIAssetsSkipsIncompressibleAndTinyFiles(t *testing.T) {
	dist, err := fs.Sub(uiAssets, "sal-ui/dist")
	require.NoError(t, err)

	compressed, err := compressUIAssets(dist)

	require.NoError(t, err)
	require.NotEmpty(t, compressed)
	for name, body := range compressed {
		require.True(t, isCompressibleAsset(name), "%s should not have been compressed", name)
		raw, err := fs.ReadFile(dist, name)
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(raw), minCompressibleSize)
		require.Less(t, len(body), len(raw))
	}
}
