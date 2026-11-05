package sparql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/apache/iceberg-go/catalog"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/rewrite"
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
	// DataRoot is set for a table found in a copy of .sal/data: the directory
	// holding its blobs/, stac/, and config.jsonld.
	DataRoot string
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

// LocateDataDirTable resolves the triples table inside a copy of a project's
// .sal/data directory that is not the project's own, such as a bucket `sal
// upload` wrote mounted into a container: the one directory, at any depth
// (`<project>/triples` in a bucket-root upload), or the directory itself when
// it is a table root, that holds an Iceberg metadata directory. Tables under
// `blobs/` (files a module handed over) and `imports/` (imported data
// products) are not the one being served. The table
// is named by its path alone, like a table on object storage, so Runner
// verifies it through DuckDB rather than through a catalog over the
// warehouse, and iceberg_scan's allow_moved_paths reads it wherever the
// metadata says it was written.
func LocateDataDirTable(dataDir string) (TriplesTable, error) {
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return TriplesTable{}, err
	}
	found, err := rewrite.IcebergTablePaths(dataDir)
	if err != nil {
		return TriplesTable{}, err
	}
	var tables []string
	for _, table := range found {
		rel, err := filepath.Rel(dataDir, table)
		if err != nil {
			return TriplesTable{}, err
		}
		if top, _, _ := strings.Cut(filepath.ToSlash(rel), "/"); top != "blobs" && top != "imports" {
			tables = append(tables, table)
		}
	}
	if len(tables) == 0 {
		return TriplesTable{}, fmt.Errorf("no Iceberg table metadata found in %s", dataDir)
	}
	if len(tables) > 1 {
		return TriplesTable{}, fmt.Errorf("%s holds %d Iceberg tables, %s; a data directory serves one", dataDir, len(tables), strings.Join(tables, ", "))
	}
	return TriplesTable{Path: tables[0], DataRoot: dataRoot(dataDir, tables[0])}, nil
}

// Runner opens a DuckDB runner over the table, after checking that the table
// has the object columns every query in this package reads. A remote table is
// checked through DuckDB, since no local catalog can load it.
func (t TriplesTable) Runner(ctx context.Context, limit int) (DuckDBRunner, error) {
	if t.Remote() {
		if err := VerifyRemoteObjectColumns(ctx, t.Path); err != nil {
			return DuckDBRunner{}, err
		}
		return DuckDBRunner{TablePath: t.Path, Limit: limit, DataRoot: t.DataRoot}, nil
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

// dataRoot finds the directory that holds the blobs/ and stac/ of the data
// product whose table is at tablePath inside dataDir. A copy of .sal/data or
// a bucket-root upload has them beside the namespace directory, above the
// table (`<root>/<project>/triples`), while an explicit-table-root upload has
// the table at dataDir itself. It is the nearest directory from the table up
// to dataDir that holds either one, and dataDir when none does.
func dataRoot(dataDir string, tablePath string) string {
	dir := tablePath
	if dir != dataDir {
		dir = filepath.Dir(dir)
	}
	for {
		for _, name := range []string{"blobs", "stac"} {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.IsDir() {
				return dir
			}
		}
		if dir == dataDir || dir == filepath.Dir(dir) {
			return dataDir
		}
		dir = filepath.Dir(dir)
	}
}
