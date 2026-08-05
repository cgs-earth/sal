package sparql

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/cgs-earth/sal/pkg"
)

// TriplesTable is the built triples table of the SAL project that the current
// working directory belongs to.
type TriplesTable struct {
	Warehouse string
	Namespace string
	Path      string
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
	}, nil
}

// Runner opens a DuckDB runner over the table with the object column layout the
// table was built with, so queries bind the object column the same way the
// SPARQL shell and the serve endpoints do.
func (t TriplesTable) Runner(ctx context.Context, limit int) (DuckDBRunner, error) {
	layout, err := ObjectLayoutForTable(ctx, t.Warehouse, t.Namespace)
	if err != nil {
		return DuckDBRunner{}, err
	}
	return DuckDBRunner{TablePath: t.Path, Layout: layout, Limit: limit}, nil
}

func joinRemote(base string, parts ...string) string {
	joined := path.Join(parts...)
	if joined == "." {
		return strings.TrimSuffix(base, "/")
	}
	return strings.TrimSuffix(base, "/") + "/" + joined
}
