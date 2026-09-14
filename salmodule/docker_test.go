package salmodule

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// dockerCopyArchive builds the tar stream the daemon's copy endpoint answers
// with: the named entry alone, headed by its base name.
func dockerCopyArchive(t *testing.T, name string, typeflag byte, content string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	archive := tar.NewWriter(&buf)
	require.NoError(t, archive.WriteHeader(&tar.Header{Name: name, Typeflag: typeflag, Mode: 0644, Size: int64(len(content)), ModTime: testFileModified}))
	_, err := io.WriteString(archive, content)
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	return &buf
}

func TestExtractFileFromArchiveWritesTheFilesContents(t *testing.T) {
	var out bytes.Buffer

	modified, err := extractFileFromArchive(dockerCopyArchive(t, "test.txt", tar.TypeReg, "hello"), "/tmp/test.txt", &out)

	require.NoError(t, err)
	require.Equal(t, "hello", out.String())
	require.True(t, testFileModified.Equal(modified))
}

func TestExtractFileFromArchiveRefusesADirectory(t *testing.T) {
	_, err := extractFileFromArchive(dockerCopyArchive(t, "out/", tar.TypeDir, ""), "/tmp/out", io.Discard)

	require.Error(t, err)
	require.Contains(t, err.Error(), "only a regular file can be copied")
}

// dockerDirectoryArchive builds the tar stream the daemon's copy endpoint
// answers with for a directory: the directory itself first, headed by its base
// name, then everything beneath it.
func dockerDirectoryArchive(t *testing.T, entries ...*tar.Header) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	archive := tar.NewWriter(&buf)
	for _, header := range entries {
		content := header.PAXRecords["content"]
		header.PAXRecords = nil
		header.Size = int64(len(content))
		header.ModTime = testFileModified
		require.NoError(t, archive.WriteHeader(header))
		_, err := io.WriteString(archive, content)
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	return &buf
}

func tarFile(name string, content string) *tar.Header {
	return &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, PAXRecords: map[string]string{"content": content}}
}

func tarDir(name string) *tar.Header {
	return &tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0755}
}

func TestWalkDirectoryArchiveVisitsEntriesRelativeToTheDirectory(t *testing.T) {
	archive := dockerDirectoryArchive(t, tarDir("zarr_test/"), tarFile("zarr_test/a.txt", "a"), tarDir("zarr_test/sub/"), tarFile("zarr_test/sub/b.txt", "b"))
	var visited []string

	err := walkDirectoryArchive(archive, "/out/zarr_test/", func(relative string, isDir bool, modified time.Time, content io.Reader) error {
		require.True(t, testFileModified.Equal(modified))
		if isDir {
			require.Nil(t, content)
			visited = append(visited, relative+"/")
			return nil
		}
		body, err := io.ReadAll(content)
		require.NoError(t, err)
		visited = append(visited, relative+"="+string(body))
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, []string{"a.txt=a", "sub/", "sub/b.txt=b"}, visited)
}

func TestWalkDirectoryArchiveRefusesAFile(t *testing.T) {
	err := walkDirectoryArchive(dockerCopyArchive(t, "test.txt", tar.TypeReg, "hello"), "/tmp/test.txt/", func(string, bool, time.Time, io.Reader) error { return nil })

	require.Error(t, err)
	require.Contains(t, err.Error(), "not a directory")
}

func TestWalkDirectoryArchiveRefusesALink(t *testing.T) {
	link := &tar.Header{Name: "out/link", Typeflag: tar.TypeSymlink, Linkname: "a.txt"}
	archive := dockerDirectoryArchive(t, tarDir("out/"), link)

	err := walkDirectoryArchive(archive, "/out/", func(string, bool, time.Time, io.Reader) error { return nil })

	require.Error(t, err)
	require.Contains(t, err.Error(), "neither a regular file nor a directory")
}
