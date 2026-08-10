package pkg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeTableMetadata(t *testing.T, root string) string {
	t.Helper()
	metadata := filepath.Join(root, "metadata")
	require.NoError(t, os.MkdirAll(metadata, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(metadata, "v1.metadata.json"), []byte("{}"), 0644))
	return root
}

func TestIcebergTablePathsFindsEveryTableUnderAWarehouse(t *testing.T) {
	warehouse := t.TempDir()
	first := writeTableMetadata(t, filepath.Join(warehouse, "sal", "triples"))
	second := writeTableMetadata(t, filepath.Join(warehouse, "other", "triples"))

	tables, err := IcebergTablePaths(warehouse)

	require.NoError(t, err)
	require.ElementsMatch(t, []string{first, second}, tables)
}

// A metadata directory holding no metadata file is not a table; an Avro
// manifest can sit beside one that has not been written yet.
func TestIcebergTablePathsIgnoresAMetadataDirectoryWithoutMetadata(t *testing.T) {
	warehouse := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(warehouse, "sal", "triples", "metadata"), 0755))

	tables, err := IcebergTablePaths(warehouse)

	require.NoError(t, err)
	require.Empty(t, tables)
}

func TestIcebergTablePathsReportsNoTablesForAMissingDirectory(t *testing.T) {
	tables, err := IcebergTablePaths(filepath.Join(t.TempDir(), "never-written"))

	require.NoError(t, err)
	require.Empty(t, tables)
}
