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

func TestLocateDataDirTableFindsTheOneTableInACopyOfTheDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "example", "triples", "metadata"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "blobs"), 0755))

	_, err := LocateDataDirTable(dataDir)
	require.ErrorContains(t, err, "no Iceberg table metadata found", "the table sits one level down, under the namespace")

	table, err := LocateDataDirTable(filepath.Join(dataDir, "example"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dataDir, "example", "triples"), table.Path)
	require.True(t, table.Remote(), "a table named by its path alone is verified through DuckDB, like one on object storage")

	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "example", "other", "metadata"), 0755))
	_, err = LocateDataDirTable(filepath.Join(dataDir, "example"))
	require.ErrorContains(t, err, "holds 2 Iceberg tables")
}
