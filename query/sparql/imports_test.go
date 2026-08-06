package sparql

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeIcebergTable lays down the metadata file that marks a directory as an
// Iceberg table root, which is all the table discovery walk looks for.
func writeIcebergTable(t *testing.T, root string) string {
	t.Helper()
	metadata := filepath.Join(root, "metadata")
	require.NoError(t, os.MkdirAll(metadata, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(metadata, "v1.metadata.json"), []byte("{}"), 0644))
	return root
}

func TestImportedTablesNamesTheViewAfterTheArtifact(t *testing.T) {
	importsDir := t.TempDir()
	path := writeIcebergTable(t, filepath.Join(importsDir, "water", "water", "triples"))

	tables := importedTables(importsDir, []string{"oci://ghcr.io/cgs-earth/water:v1"})

	require.Equal(t, []ImportedTable{{
		View:     "water",
		Artifact: "oci://ghcr.io/cgs-earth/water:v1",
		Path:     path,
	}}, tables)
}

func TestImportedTablesIgnoresOntologyImports(t *testing.T) {
	require.Empty(t, importedTables(t.TempDir(), []string{"https://schema.org/version/latest/schemaorg-current-https.ttl"}))
}

func TestImportedTablesSkipsAnArtifactThatWasNeverPulled(t *testing.T) {
	importsDir := t.TempDir()
	writeIcebergTable(t, filepath.Join(importsDir, "water", "water", "triples"))

	tables := importedTables(importsDir, []string{
		"oci://ghcr.io/cgs-earth/water:v1",
		"oci://ghcr.io/cgs-earth/rivers:v1",
	})

	require.Len(t, tables, 1)
	require.Equal(t, "water", tables[0].View)
}

func TestImportedTablesSkipsAnArtifactWithoutAnIcebergTable(t *testing.T) {
	importsDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(importsDir, "water", "docs"), 0755))

	require.Empty(t, importedTables(importsDir, []string{"oci://ghcr.io/cgs-earth/water:v1"}))
}

func TestImportedTablesQualifiesSeveralTablesInOneArtifact(t *testing.T) {
	importsDir := t.TempDir()
	writeIcebergTable(t, filepath.Join(importsDir, "water", "rivers", "triples"))
	writeIcebergTable(t, filepath.Join(importsDir, "water", "lakes", "triples"))

	tables := importedTables(importsDir, []string{"oci://ghcr.io/cgs-earth/water:v1"})

	views := []string{tables[0].View, tables[1].View}
	require.ElementsMatch(t, []string{"water_lakes_triples", "water_rivers_triples"}, views)
}

// An artifact called triples would otherwise replace the view the project's own
// table is queried through.
func TestImportedTablesDoesNotShadowTheProjectsOwnView(t *testing.T) {
	importsDir := t.TempDir()
	writeIcebergTable(t, filepath.Join(importsDir, "triples", "sal", "triples"))

	tables := importedTables(importsDir, []string{"oci://ghcr.io/cgs-earth/triples:v1"})

	require.Len(t, tables, 1)
	require.Equal(t, "triples_2", tables[0].View)
}

func TestImportsViewLabelsEveryRowWithTheViewItCameFrom(t *testing.T) {
	sql := importsViewSQL([]ImportedTable{
		{View: "water", Path: "/imports/water/water/triples"},
		{View: "rivers", Path: "/imports/rivers/rivers/triples"},
	})

	require.Contains(t, sql, `CREATE OR REPLACE VIEW "imports" AS`)
	require.Contains(t, sql, `SELECT 'water' AS view, * FROM "water"`)
	require.Contains(t, sql, `SELECT 'rivers' AS view, * FROM "rivers"`)
	require.Contains(t, sql, "UNION ALL")
	// The object column is what the two object layouts disagree about, so it is
	// left out to keep data products built either way stackable.
	require.NotContains(t, sql, "object")
}

func TestImportViewSQLQuotesTheViewName(t *testing.T) {
	sql := importViewSQL(ImportedTable{View: "sal-water", Path: "/imports/sal-water/sal/triples"})

	require.Contains(t, sql, `CREATE OR REPLACE VIEW "sal-water" AS`)
	require.Contains(t, sql, `iceberg_scan('/imports/sal-water/sal/triples', allow_moved_paths = true)`)
}
