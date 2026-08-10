package sparql

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
)

// ImportedTable is the Iceberg table of another SAL data product that this
// project imported, registered as its own DuckDB view so it can be queried
// beside the project's own `triples` view.
type ImportedTable struct {
	// View is the name the table is registered under, taken from the artifact
	// the table was pulled from: oci://ghcr.io/cgs-earth/water:v1 becomes water.
	View string `json:"view"`
	// Artifact is the oci:// reference recorded with owl:imports.
	Artifact string `json:"artifact"`
	// Path is the Iceberg table root on disk under .sal/data/imports.
	Path string `json:"path"`
}

// LocateImportedTables finds the Iceberg tables of the OCI artifacts the SAL
// project in the current working directory imports.
//
// An import that has not been pulled, or that holds no Iceberg table, reports
// no view rather than failing: the imported data products are a convenience on
// top of the project's own table, so a query against that table should still
// run when one of them is missing.
func LocateImportedTables() []ImportedTable {
	importsDir, err := pkg.SalImportsDir()
	if err != nil {
		slog.Debug("not registering imported data products", "error", err)
		return nil
	}
	return importedTables(importsDir, projectImports())
}

// importedTables resolves each oci:// import to the Iceberg tables pulled for it.
func importedTables(importsDir string, imports []string) []ImportedTable {
	// The project's own table is always the `triples` view and the imports
	// stack together as `imports`, so an artifact named after either has to
	// give way rather than shadow it.
	taken := map[string]bool{"triples": true, ImportsView: true}

	var tables []ImportedTable
	for _, iri := range imports {
		if !importation.IsOciImport(iri) {
			continue
		}
		directory, err := importation.ArtifactDir(importsDir, iri)
		if err != nil {
			slog.Warn("not registering an imported artifact", "import", iri, "error", err)
			continue
		}
		roots, err := pkg.IcebergTablePaths(directory)
		if err != nil {
			slog.Warn("not registering an imported artifact", "import", iri, "error", err)
			continue
		}
		if len(roots) == 0 {
			slog.Debug("imported artifact holds no Iceberg table", "import", iri, "path", directory)
			continue
		}
		for _, root := range roots {
			tables = append(tables, ImportedTable{
				View:     uniqueViewName(viewName(directory, root, len(roots) > 1), taken),
				Artifact: iri,
				Path:     root,
			})
		}
	}
	return tables
}

// viewName names the view an imported table is registered as. A data product
// holds a single table in the ordinary case, so it is named after the artifact
// alone; a bundle carrying several is qualified by each table's path within it
// so that the names stay distinct and say where they came from.
func viewName(directory string, root string, qualify bool) string {
	name := filepath.Base(directory)
	if !qualify {
		return name
	}
	rel, err := filepath.Rel(directory, root)
	if err != nil {
		return name
	}
	return name + "_" + strings.ReplaceAll(filepath.ToSlash(rel), "/", "_")
}

// uniqueViewName resolves a name that is already registered by numbering it,
// so that two artifacts with the same name both stay queryable.
func uniqueViewName(name string, taken map[string]bool) string {
	unique := name
	for suffix := 2; taken[unique]; suffix++ {
		unique = fmt.Sprintf("%s_%d", name, suffix)
	}
	taken[unique] = true
	return unique
}

// ImportsView is the view every imported data product is readable through at
// once, labelled with the view each row came from.
const ImportsView = "imports"

// ImportedTablesSQL counts the triples each imported data product holds. It is
// a plain group by because the `imports` view already labels every row with the
// import it came from.
const ImportedTablesSQL = `SELECT
	view,
	COUNT(*) AS triples
FROM imports
GROUP BY view
ORDER BY view`

// importsViewSQL stacks the imported data products into the `imports` view.
//
// Only the columns both object layouts share are selected, so that data
// products built with different layouts still stack: a UNION ALL needs matching
// schemas, and the object column is exactly what the layouts disagree about.
func importsViewSQL(tables []ImportedTable) string {
	selects := make([]string, 0, len(tables))
	for _, table := range tables {
		selects = append(selects, fmt.Sprintf(
			"SELECT '%s' AS view, * FROM %s",
			escapeSQLLiteral(table.View), quoteIdentifier(table.View)))
	}
	return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS\n%s",
		quoteIdentifier(ImportsView), strings.Join(selects, "\nUNION ALL\n"))
}
