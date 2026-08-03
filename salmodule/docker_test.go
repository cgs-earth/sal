package salmodule

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// tarEntries reads back the names of every entry in a build context stream.
func tarEntries(t *testing.T, stream io.Reader) []string {
	t.Helper()

	var names []string
	archive := tar.NewReader(stream)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names = append(names, header.Name)
	}
	return names
}

func TestTarDirectoryPacksTheBuildContext(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.py"), []byte("print('hi')\n"), 0644))

	stream, err := tarDirectory(dir)

	require.NoError(t, err)
	require.ElementsMatch(t, []string{"Dockerfile", "src", "src/main.py"}, tarEntries(t, stream))
}

func TestTarDirectorySkipsGitMetadata(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))

	stream, err := tarDirectory(dir)

	require.NoError(t, err)
	require.Equal(t, []string{"Dockerfile"}, tarEntries(t, stream))
}

func TestReportBuildProgressSurfacesDaemonErrors(t *testing.T) {
	stream := strings.NewReader(`{"stream":"Step 1/2 : FROM scratch"}` + "\n" + `{"error":"pull access denied for missing-image"}`)

	err := reportBuildProgress(stream, "sal-module-test:latest")

	require.Error(t, err)
	require.Contains(t, err.Error(), "pull access denied for missing-image")
}

func TestReportBuildProgressAcceptsSuccessfulBuilds(t *testing.T) {
	stream := strings.NewReader(`{"stream":"Step 1/1 : FROM scratch"}` + "\n" + `{"stream":"Successfully tagged sal-module-test:latest"}`)

	require.NoError(t, reportBuildProgress(stream, "sal-module-test:latest"))
}
