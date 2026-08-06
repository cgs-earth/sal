package sparql

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	// DuckDB is linked into the sal binary rather than shelled out to, so no
	// duckdb CLI has to be installed for sal to query a built table.
	_ "github.com/duckdb/duckdb-go/v2"
)

// Extensions are the DuckDB extensions sal queries need on top of the ones the
// linked in library already carries. iceberg reads the triples table, httpfs
// lets it read one on object storage, avro reads Iceberg manifests, and spatial
// handles geometry objects.
var Extensions = []string{"iceberg", "httpfs", "avro", "spatial"}

// instance is the DuckDB database every sal query runs through.
//
// Opening DuckDB and loading its extensions costs far more than any query sal
// runs against a triples table, so a process pays for it once rather than once
// per query. `sal serve` in particular answers every request on this one handle.
var instance = &duckdbInstance{}

type duckdbInstance struct {
	mu sync.Mutex
	db *sql.DB
	// spatial records that the extension has been loaded, since it is loaded on
	// demand rather than at open.
	spatial bool
	// viewPath is the table the `triples` view currently reads.
	viewPath string
}

// prepare returns the shared handle with the `triples` view over tablePath and,
// when the statement needs it, the spatial extension loaded.
func (d *duckdbInstance) prepare(ctx context.Context, tablePath string, withSpatial bool) (*sql.DB, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.db == nil {
		db, err := sql.Open("duckdb", "")
		if err != nil {
			return nil, fmt.Errorf("open duckdb: %w", err)
		}
		// The object cache holds on to Iceberg metadata it has already read. A
		// long lived process would otherwise keep serving the snapshot that was
		// current when it first read the table, where a fresh duckdb process
		// never could.
		if _, err := db.ExecContext(ctx, "SET enable_object_cache = false; INSTALL iceberg; LOAD iceberg;"); err != nil {
			if closeErr := db.Close(); closeErr != nil {
				slog.Warn("failed to close the duckdb handle", "error", closeErr)
			}
			return nil, fmt.Errorf("load the duckdb iceberg extension: %w", err)
		}
		d.db = db
	}

	if withSpatial && !d.spatial {
		if _, err := d.db.ExecContext(ctx, "INSTALL spatial; LOAD spatial;"); err != nil {
			return nil, fmt.Errorf("load the duckdb spatial extension: %w", err)
		}
		d.spatial = true
	}

	if tablePath != "" && tablePath != d.viewPath {
		if _, err := d.db.ExecContext(ctx, viewSQL(tablePath)); err != nil {
			return nil, fmt.Errorf("create the triples view over %s: %w", tablePath, err)
		}
		d.viewPath = tablePath
	}
	return d.db, nil
}

// InstallExtensions downloads every extension sal loads into the DuckDB
// extension cache, so a machine or a container image can be primed for queries
// ahead of the first one.
func InstallExtensions(ctx context.Context) error {
	db, err := instance.prepare(ctx, "", false)
	if err != nil {
		return err
	}
	for _, name := range Extensions {
		if _, err := db.ExecContext(ctx, "INSTALL "+name); err != nil {
			return fmt.Errorf("install the duckdb %s extension: %w", name, err)
		}
	}
	return nil
}

// viewSQL is the `triples` view every query runs against.
func viewSQL(tablePath string) string {
	return fmt.Sprintf(`CREATE OR REPLACE VIEW triples AS
SELECT *
FROM iceberg_scan('%s', allow_moved_paths = true)`, escapeSQLLiteral(tablePath))
}

// RunSQL executes a DuckDB statement with the Iceberg triples table registered as the `triples` view.
func (r DuckDBRunner) RunSQL(ctx context.Context, statement string) (Result, error) {
	withSpatial := needsSpatial(statement, r.Layout)
	header, rows, err := r.runSQL(ctx, statement, withSpatial)
	// A statement can need spatial in a way needsSpatial does not recognize.
	// Loading it and running again beats failing a query that would have worked.
	if err != nil && !withSpatial && missingSpatialExtension(err) {
		header, rows, err = r.runSQL(ctx, statement, true)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{
		SQL:     statement,
		Header:  header,
		Rows:    rows,
		Message: fmt.Sprintf("%d rows", len(rows)),
	}, nil
}

func (r DuckDBRunner) runSQL(ctx context.Context, statement string, withSpatial bool) ([]string, [][]string, error) {
	db, err := instance.prepare(ctx, r.TablePath, withSpatial)
	if err != nil {
		return nil, nil, err
	}
	return queryRows(ctx, db, statement)
}

// queryRows runs a statement and returns its header and its rows as text.
func queryRows(ctx context.Context, db *sql.DB, statement string) ([]string, [][]string, error) {
	rows, err := db.QueryContext(ctx, textQuery(statement))
	if err != nil {
		return nil, nil, fmt.Errorf("duckdb query failed: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Warn("failed to close the duckdb result", "error", err)
		}
	}()

	header, err := rows.Columns()
	if err != nil {
		return nil, nil, fmt.Errorf("duckdb query failed: %w", err)
	}

	var results [][]string
	for rows.Next() {
		values := make([]sql.NullString, len(header))
		targets := make([]any, len(header))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, nil, fmt.Errorf("read the duckdb result: %w", err)
		}
		row := make([]string, len(header))
		for i, value := range values {
			// An invalid NullString is a SQL NULL, which renders as the empty
			// string here just as it did in the CSV the duckdb CLI wrote.
			row[i] = value.String
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("duckdb query failed: %w", err)
	}
	return header, results, nil
}

// textQuery wraps a statement so that DuckDB renders every column to text itself.
//
// The driver hands back Go values, and the types a triples table produces --
// GEOMETRY, DECIMAL, LIST, STRUCT, JSON -- have no Go equivalent that formats
// the way DuckDB does. Casting in DuckDB keeps the rendering identical to the
// CSV the duckdb CLI used to write, and leaves NULL as the only non-text value
// a row can hold. COLUMNS(*) preserves the column names of the inner statement.
func textQuery(statement string) string {
	return "SELECT COLUMNS(*)::VARCHAR FROM (" + strings.TrimRight(strings.TrimSpace(statement), ";") + ")"
}

// countStar matches COUNT(*), which projects no column and so cannot pull in the
// geometry column that `*` otherwise would.
var countStar = regexp.MustCompile(`count\s*\(\s*\*\s*\)`)

// needsSpatial reports whether a statement has to run with the spatial extension
// loaded.
//
// The extension is around 60 MB and DuckDB cannot autoload it, so loading it
// unconditionally costs more than any query against a triples table costs to
// actually run. Two things need it: the ST_ functions, and the typed layout's
// object_geometry column, which DuckDB exposes as GEOMETRY and cannot read at
// all without spatial.
func needsSpatial(sql string, layout ObjectLayout) bool {
	lowered := strings.ToLower(sql)
	// Every DuckDB spatial function is named ST_*.
	if strings.Contains(lowered, "st_") {
		return true
	}
	// The simple layout has no geometry column for a projection to reach.
	if layout != TypedObjects {
		return false
	}
	projected := countStar.ReplaceAllString(lowered, "")
	return strings.Contains(projected, "object_geometry") || strings.Contains(projected, "*")
}

// missingSpatialExtension recognizes the two ways DuckDB reports that a statement
// needed spatial: an ST_ function missing from the catalog, and a geometry column
// it cannot cast without the extension loaded.
func missingSpatialExtension(err error) bool {
	message := err.Error()
	return strings.Contains(message, "spatial extension") || strings.Contains(message, "GEOMETRY")
}

func escapeSQLLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
