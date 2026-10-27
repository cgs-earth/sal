package sparql

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"text/tabwriter"

	"github.com/apache/iceberg-go/catalog"
	"github.com/cgs-earth/sal/pkg"
)

// NoTriplesTableError reports a project whose triples table has not been
// built: there is nothing at Path for a query to read. Commands that have
// their own advice, such as serve, replace the message and keep the path.
type NoTriplesTableError struct {
	Path string
}

func (e NoTriplesTableError) Error() string {
	return "no data found at " + e.Path + ": add data to the project and run `sal build` first"
}

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
// current working directory. Whether the table has been built is only known
// once Runner opens it, which reports an unbuilt one as a NoTriplesTableError
// rather than leaving the catalog to fail on a missing metadata directory.
func LocateTriplesTable() (TriplesTable, error) {
	warehouse, err := pkg.SalDataDir()
	if err != nil {
		return TriplesTable{}, err
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
// has the object columns every query in this package reads. A remote table is
// checked through DuckDB, since no local catalog can load it.
func (t TriplesTable) Runner(ctx context.Context, limit int) (DuckDBRunner, error) {
	if t.Remote() {
		if err := VerifyRemoteObjectColumns(ctx, t.Path); err != nil {
			return DuckDBRunner{}, err
		}
		return DuckDBRunner{TablePath: t.Path, Limit: limit}, nil
	}
	if err := VerifyObjectColumns(ctx, t.Warehouse, t.Namespace); errors.Is(err, catalog.ErrNoSuchTable) {
		return DuckDBRunner{}, NoTriplesTableError{Path: t.Path}
	} else if err != nil {
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
