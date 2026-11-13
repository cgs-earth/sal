package sparql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	// DuckDB is linked into the sal binary rather than shelled out to, so no
	// duckdb CLI has to be installed for sal to query a built table.
	_ "github.com/duckdb/duckdb-go/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// maxQueryTextAttribute bounds the query text a span records, since a query
// can be pasted in far longer than a trace viewer can show.
const maxQueryTextAttribute = 4096

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

// slowLane is the second DuckDB database `sal serve` moves a query to once it
// has run on instance for longer than QueryLanes.DemoteAfter (see UseQueryLanes).
var slowLane = &duckdbInstance{}

// lanes is how queries are split between instance and slowLane; the zero
// value runs everything on instance, which is what every command but `sal
// serve` does.
var lanes struct {
	sync.Mutex
	QueryLanes
}

// QueryLanes splits the queries a server answers between two DuckDB
// databases, so that a heavy query cannot make every other one wait.
//
// Every query on one database shares its threads and its memory, and DuckDB
// has no priorities: a scan of a whole table keeps every thread busy, and a
// lookup arriving behind it waits for threads with it. A query starts on the
// fast database; one still running after DemoteAfter is cancelled there and
// run again from the start on the slow one, which has its own, smaller,
// share of threads and memory. A query that finishes within DemoteAfter, which
// is what a lookup does, therefore never queues behind more than DemoteAfter of
// a heavy one, and the heavy ones contend with each other instead. A demoted
// query pays DemoteAfter again, which is small next to what a query that slow
// takes anyway.
type QueryLanes struct {
	// DemoteAfter is how long a query may run on the fast database; zero
	// runs every query there.
	DemoteAfter time.Duration
	// FastThreads, SlowThreads, FastMemory, and SlowMemory are each
	// database's threads and memory_limit in bytes; zero leaves DuckDB's
	// default, which for memory is 80% of the machine for each database.
	FastThreads, SlowThreads int
	FastMemory, SlowMemory   uint64
}

// DefaultQueryLanes splits a machine's cores and its memory budget
// (pkg.Budget) between the lanes: every core and half the budget for the fast
// database, a quarter of the cores and a quarter of the budget for the slow
// one. The threads of both may run at once, the operating system sharing the
// cores between them, but the slow lane's few cannot crowd out the fast
// lane's many; the memory shares are what keep the two from claiming more
// than the machine has between them.
func DefaultQueryLanes(demoteAfter time.Duration) QueryLanes {
	cores := runtime.NumCPU()
	budget := pkg.Budget().Bytes
	return QueryLanes{
		DemoteAfter: demoteAfter,
		FastThreads: cores,
		SlowThreads: max(1, cores/4),
		FastMemory:  budget / 2,
		SlowMemory:  budget / 4,
	}
}

// UseQueryLanes splits every query this process runs from now on between the
// fast and the slow database as lanes says; see QueryLanes.
func UseQueryLanes(config QueryLanes) {
	lanes.Lock()
	lanes.QueryLanes = config
	lanes.Unlock()
	instance.configure(databaseSettings(config.FastThreads, config.FastMemory))
	slowLane.configure(databaseSettings(config.SlowThreads, config.SlowMemory))
}

// currentLanes is the QueryLanes in force.
func currentLanes() QueryLanes {
	lanes.Lock()
	defer lanes.Unlock()
	return lanes.QueryLanes
}

// databaseSettings are the statements that give a database its threads and
// memory, leaving whichever is zero at DuckDB's default.
func databaseSettings(threads int, memory uint64) []string {
	var settings []string
	if threads > 0 {
		settings = append(settings, fmt.Sprintf("SET threads = %d", threads))
	}
	if memory > 0 {
		settings = append(settings, fmt.Sprintf("SET memory_limit = '%dB'", memory))
	}
	return settings
}

type duckdbInstance struct {
	mu sync.Mutex
	db *sql.DB
	// settings are the database settings UseQueryLanes gave it, applied when
	// it opens, or on the next query when it is already open (applied).
	settings []string
	applied  bool
	// spatial records that the extension has been loaded, since it is loaded on
	// demand rather than at open.
	spatial bool
	// viewPath is the table the `triples` view currently reads.
	viewPath string
	// imports are the imported data products already registered as views.
	imports []ImportedTable
}

// prepare returns the shared handle with the `triples` view over tablePath, a
// view per imported data product, and, when the statement needs it, the spatial
// extension loaded.
func (d *duckdbInstance) prepare(ctx context.Context, tablePath string, imports []ImportedTable, withSpatial bool) (*sql.DB, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.db == nil {
		// opening the database and loading the iceberg extension is the slow
		// part of a process's first query, so it is a span of its own
		openCtx, span := telemetry.Start(ctx, "duckdb.open")
		db, err := sql.Open("duckdb", "")
		if err != nil {
			telemetry.End(span, err)
			return nil, fmt.Errorf("open duckdb: %w", err)
		}
		// The object cache holds on to Iceberg metadata it has already read. A
		// long lived process would otherwise keep serving the snapshot that was
		// current when it first read the table, where a fresh duckdb process
		// never could.
		if _, err := db.ExecContext(openCtx, "SET enable_object_cache = false; INSTALL iceberg; LOAD iceberg;"); err != nil {
			if closeErr := db.Close(); closeErr != nil {
				slog.Warn("failed to close the duckdb handle", "error", closeErr)
			}
			telemetry.End(span, err)
			return nil, fmt.Errorf("load the duckdb iceberg extension: %w", err)
		}
		d.db = db
		telemetry.End(span, nil)
	}
	if !d.applied {
		for _, setting := range d.settings {
			if _, err := d.db.ExecContext(ctx, setting); err != nil {
				return nil, fmt.Errorf("configure duckdb: %w", err)
			}
		}
		d.applied = true
	}

	if withSpatial && !d.spatial {
		// the spatial extension is tens of megabytes, downloaded on first use
		spatialCtx, span := telemetry.Start(ctx, "duckdb.load_spatial")
		// Every geometry sal stores is longitude then latitude, so the
		// functions that care about axis order (the spherical distances and
		// ST_Transform) are told so; DuckDB 2.0 refuses to run them until
		// the setting is given either way.
		_, err := d.db.ExecContext(spatialCtx, "INSTALL spatial; LOAD spatial; SET geometry_always_xy = true;")
		telemetry.End(span, err)
		if err != nil {
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
	d.registerImports(ctx, imports)
	return d.db, nil
}

// configure records the settings the database is to run with, which prepare
// applies before its next query.
func (d *duckdbInstance) configure(settings []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.settings, d.applied = settings, false
}

// registerImports registers a view per imported data product plus the `imports`
// view they stack into. A table DuckDB cannot read is warned about and left
// unregistered rather than failing the query that was actually asked, which is
// against the project's own table.
func (d *duckdbInstance) registerImports(ctx context.Context, imports []ImportedTable) {
	if len(imports) == 0 || slices.Equal(imports, d.imports) {
		return
	}
	registered := make([]ImportedTable, 0, len(imports))
	for _, table := range imports {
		if _, err := d.db.ExecContext(ctx, importViewSQL(table)); err != nil {
			slog.Warn("skipping an unreadable imported data product", "view", table.View, "path", table.Path, "error", err)
			continue
		}
		registered = append(registered, table)
	}
	if len(registered) > 0 {
		if _, err := d.db.ExecContext(ctx, importsViewSQL(registered)); err != nil {
			slog.Warn("not registering the "+ImportsView+" view over the imported data products", "error", err)
		}
	}
	// The requested set is what is recorded, not the subset that registered, so
	// that a table DuckDB refused is not retried on every query.
	d.imports = imports
}

// InstallExtensions downloads every extension sal loads into the DuckDB
// extension cache, so a machine or a container image can be primed for queries
// ahead of the first one.
func InstallExtensions(ctx context.Context) error {
	db, err := instance.prepare(ctx, "", nil, false)
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

// TriplesView is the view every query reads the table through: what the
// project's source files assert, what it imports, what its modules produced,
// and the provenance of its pins. A vocabulary the project only validates
// against has no rows here, so a SPARQL property path reasons over exactly
// what the project asserted or imported.
const TriplesView = "triples"

// viewSQL is the `triples` view every query runs against.
func viewSQL(tablePath string) string {
	return fmt.Sprintf(`CREATE OR REPLACE VIEW %s AS
SELECT *
FROM iceberg_scan('%s', allow_moved_paths = true)`, TriplesView, escapeSQLLiteral(tablePath))
}

// importViewSQL is the view an imported data product is queried through. It is
// the same scan the `triples` view is, under the name the import was given.
func importViewSQL(table ImportedTable) string {
	return fmt.Sprintf(`CREATE OR REPLACE VIEW %s AS
SELECT *
FROM iceberg_scan('%s', allow_moved_paths = true)`, quoteIdentifier(table.View), escapeSQLLiteral(table.Path))
}

// RunSQL executes a DuckDB statement with the Iceberg triples table registered as the `triples` view.
func (r DuckDBRunner) RunSQL(ctx context.Context, statement string) (_ Result, err error) {
	ctx, span := telemetry.Start(ctx, "duckdb.query", attribute.String("db.system.name", "duckdb"), attribute.String("db.query.text", queryTextAttribute(statement)))
	defer func() { telemetry.End(span, err) }()
	withSpatial := needsSpatial(statement)
	header, rows, err := r.runSQL(ctx, statement, withSpatial)
	// A statement can need spatial in a way needsSpatial does not recognize.
	// Loading it and running again beats failing a query that would have worked.
	if err != nil && !withSpatial && missingSpatialExtension(err) {
		header, rows, err = r.runSQL(ctx, statement, true)
	}
	if err != nil {
		return Result{}, err
	}
	span.SetAttributes(attribute.Int("db.response.returned_rows", len(rows)))
	return Result{
		SQL:     statement,
		Header:  header,
		Rows:    rows,
		Message: fmt.Sprintf("%d rows", len(rows)),
	}, nil
}

// queryTextAttribute is a query as a span records it, cut to
// maxQueryTextAttribute bytes.
func queryTextAttribute(query string) string {
	if len(query) > maxQueryTextAttribute {
		return query[:maxQueryTextAttribute]
	}
	return query
}

// runSQL runs a statement on the fast database, and, when lanes are in use
// and it is still running after QueryLanes.DemoteAfter, cancels it there and
// runs it again on the slow one.
func (r DuckDBRunner) runSQL(ctx context.Context, statement string, withSpatial bool) ([]string, [][]string, error) {
	demoteAfter := currentLanes().DemoteAfter
	if demoteAfter <= 0 {
		return r.runOn(ctx, instance, statement, withSpatial)
	}
	fastCtx, cancel := context.WithTimeout(ctx, demoteAfter)
	header, rows, err := r.runOn(fastCtx, instance, statement, withSpatial)
	cancel()
	if err == nil || ctx.Err() != nil || !errors.Is(fastCtx.Err(), context.DeadlineExceeded) {
		return header, rows, err
	}
	slog.Debug("moving a slow query to the slow lane", "after", demoteAfter)
	trace.SpanFromContext(ctx).SetAttributes(attribute.Bool("sal.duckdb.demoted", true))
	return r.runOn(ctx, slowLane, statement, withSpatial)
}

// runOn runs a statement on one database, buffering its rows as text.
func (r DuckDBRunner) runOn(ctx context.Context, database *duckdbInstance, statement string, withSpatial bool) ([]string, [][]string, error) {
	db, err := database.prepare(ctx, r.TablePath, r.Imports, withSpatial)
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
// actually run. Two things need it: the ST_ functions, and the object_geometry
// column, which DuckDB exposes as GEOMETRY and cannot read at all without
// spatial.
func needsSpatial(sql string) bool {
	lowered := strings.ToLower(sql)
	// Every DuckDB spatial function is named ST_*.
	if strings.Contains(lowered, "st_") {
		return true
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

// quoteIdentifier quotes a view name, since an artifact name is whatever the
// registry called it rather than a bare SQL identifier.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
