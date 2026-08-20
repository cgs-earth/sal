package sparql

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"text/tabwriter"

	"github.com/cgs-earth/sal/pkg"
)

// TriplesTable is the built triples table of the SAL project that the current
// working directory belongs to.
type TriplesTable struct {
	Warehouse string
	Namespace string
	Path      string
	// Imports are the triples tables of the OCI artifacts the project imports,
	// which queries reach through a view named after each artifact.
	Imports []ImportedTable
}

// LocateTriplesTable resolves the triples table of the SAL project in the
// current working directory. An unbuilt project is reported as an actionable
// error rather than left for DuckDB to fail on a missing path.
func LocateTriplesTable() (TriplesTable, error) {
	warehouse, err := pkg.SalDataDir()
	if err != nil {
		return TriplesTable{}, err
	}

	entries, err := os.ReadDir(warehouse)
	if err != nil {
		return TriplesTable{}, fmt.Errorf("failed to read SAL data directory: %w", err)
	}
	if len(entries) == 0 {
		return TriplesTable{}, fmt.Errorf("no data has been built yet; run `sal build` to build a data product first")
	}

	namespace, err := pkg.GitProjectName()
	if err != nil {
		return TriplesTable{}, err
	}

	return TriplesTable{
		Warehouse: warehouse,
		Namespace: namespace,
		Path:      joinRemote(warehouse, namespace, "triples"),
		Imports:   LocateImportedTables(),
	}, nil
}

// Runner opens a DuckDB runner over the table, after checking that the table
// has the object columns every query in this package reads.
func (t TriplesTable) Runner(ctx context.Context, limit int) (DuckDBRunner, error) {
	if err := VerifyObjectColumns(ctx, t.Warehouse, t.Namespace); err != nil {
		return DuckDBRunner{}, err
	}
	return DuckDBRunner{TablePath: t.Path, Limit: limit, Imports: t.Imports}, nil
}

// RunLookup opens the triples table of the SAL project in the current working
// directory and runs a lookup's SQL against it.
func RunLookup(sql string) (Result, error) {
	ctx := context.Background()
	table, err := LocateTriplesTable()
	if err != nil {
		return Result{}, err
	}
	runner, err := table.Runner(ctx, 0)
	if err != nil {
		return Result{}, err
	}
	return runner.RunSQL(ctx, sql)
}

// FormatTable renders a DuckDB result as aligned columns so that a lookup
// prints readably whatever columns it selected.
func FormatTable(header []string, rows [][]string) string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	if len(header) > 0 {
		_, _ = fmt.Fprintln(writer, strings.Join(header, "\t"))
	}
	for _, row := range rows {
		_, _ = fmt.Fprintln(writer, strings.Join(row, "\t"))
	}
	_ = writer.Flush()
	return out.String()
}

func joinRemote(base string, parts ...string) string {
	joined := path.Join(parts...)
	if joined == "." {
		return strings.TrimSuffix(base, "/")
	}
	return strings.TrimSuffix(base, "/") + "/" + joined
}
