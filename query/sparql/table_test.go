package sparql

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A warehouse with no table under the namespace, as after sal init with no
// build, is reported as no data rather than as a catalog failure.
func TestRunnerReportsAnUnbuiltTableAsNoData(t *testing.T) {
	warehouse := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(warehouse, "blobs"), 0755))
	tbl := TriplesTable{Warehouse: warehouse, Namespace: "sal", Path: filepath.Join(warehouse, "sal", "triples")}

	_, err := tbl.Runner(context.Background(), 0)

	var missing NoTriplesTableError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, tbl.Path, missing.Path)
	require.EqualError(t, err, "no data found at "+tbl.Path+": add data to the project and run `sal build` first")
}

func TestFormatTableAlignsColumnsUnderTheirHeader(t *testing.T) {
	output := FormatTable(
		[]string{"class", "instances"},
		[][]string{
			{"https://schema.org/Person", "12"},
			{"https://schema.org/Place", "3"},
		},
	)
	require.Equal(t, ""+
		"class                      instances\n"+
		"https://schema.org/Person  12\n"+
		"https://schema.org/Place   3\n", output)
}

func TestFormatTableOmitsHeaderRowWhenThereIsNoHeader(t *testing.T) {
	require.Equal(t, "https://schema.org/Person\n", FormatTable(nil, [][]string{{"https://schema.org/Person"}}))
}

// makeTable makes the directories of an Iceberg table at path: a metadata
// directory holding one metadata file.
func makeTable(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(path, "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "metadata", "v1.metadata.json"), []byte("{}"), 0644))
}

// A copy of .sal/data, and the bucket-root upload that is the same layout,
// hold the table two levels down, under the project's namespace, with blobs/
// and stac/ beside that directory.
func TestLocateDataDirTableFindsTheTableOfABucketRootUpload(t *testing.T) {
	dataDir := t.TempDir()
	makeTable(t, filepath.Join(dataDir, "example", "triples"))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "blobs"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "stac"), 0755))

	table, err := LocateDataDirTable(dataDir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dataDir, "example", "triples"), table.Path)
	require.Equal(t, dataDir, table.DataRoot, "blobs and stac are beside the namespace directory")
	require.True(t, table.Remote(), "a table named by its path alone is verified through DuckDB, like one on object storage")
}

// An upload to an explicit table root has the table at the root and nothing
// else.
func TestLocateDataDirTableFindsATableRootUpload(t *testing.T) {
	dataDir := t.TempDir()
	makeTable(t, dataDir)

	table, err := LocateDataDirTable(dataDir)
	require.NoError(t, err)
	require.Equal(t, dataDir, table.Path)
	require.Equal(t, dataDir, table.DataRoot)
}

// A table one level down, in a directory that is itself given as the data
// directory, is found with its blobs one level above it only when they exist.
func TestLocateDataDirTableFindsATableOneLevelDown(t *testing.T) {
	dataDir := t.TempDir()
	makeTable(t, filepath.Join(dataDir, "triples"))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "blobs"), 0755))

	table, err := LocateDataDirTable(dataDir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dataDir, "triples"), table.Path)
	require.Equal(t, dataDir, table.DataRoot)
}

func TestLocateDataDirTableRefusesADirectoryWithoutATable(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "blobs"), 0755))

	_, err := LocateDataDirTable(dataDir)
	require.ErrorContains(t, err, "no Iceberg table metadata found")
}

func TestLocateDataDirTableRefusesMoreThanOneTable(t *testing.T) {
	dataDir := t.TempDir()
	makeTable(t, filepath.Join(dataDir, "example", "triples"))
	makeTable(t, filepath.Join(dataDir, "example", "other"))

	_, err := LocateDataDirTable(dataDir)
	require.ErrorContains(t, err, "holds 2 Iceberg tables")
}

// Tables a module handed over (under blobs/) or the project imported (under
// imports/) are not the data product being served.
func TestLocateDataDirTableIgnoresBlobsAndImports(t *testing.T) {
	dataDir := t.TempDir()
	makeTable(t, filepath.Join(dataDir, "example", "triples"))
	makeTable(t, filepath.Join(dataDir, "blobs", "abc123", "table"))
	makeTable(t, filepath.Join(dataDir, "imports", "other", "triples"))

	table, err := LocateDataDirTable(dataDir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dataDir, "example", "triples"), table.Path)
}
