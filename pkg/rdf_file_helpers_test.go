package pkg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindRdfDataInPathsSkipsTheSalDirectory(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sal", "data", "triples", "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sal", "config.jsonld"), []byte("# managed by sal import\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sal", "data", "triples", "metadata", "v1.metadata.json"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(project, "data.ttl"), []byte("# project data\n"), 0644))

	files, err := FindRdfDataInPaths([]string{project})

	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(project, "data.ttl")}, files)
}

func TestFindRdfDataInPathsWalksTheSalDirectoryWhenItIsTheRequestedPath(t *testing.T) {
	project := t.TempDir()
	salDir := filepath.Join(project, ".sal")
	require.NoError(t, os.MkdirAll(salDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(salDir, "config.jsonld"), []byte("# managed by sal import\n"), 0644))

	files, err := FindRdfDataInPaths([]string{salDir})

	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(salDir, "config.jsonld")}, files)
}
