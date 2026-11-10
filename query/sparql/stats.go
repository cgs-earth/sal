package sparql

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	"golang.org/x/sync/singleflight"
)

// TableStats summarizes an Iceberg triples table for the `sal serve --with-ui` stats view.
type TableStats struct {
	TablePath string `json:"tablePath"`
	// Title and Description name the data product for the UI's landing page,
	// from the dcterms:title and dcterms:description `sal init` records on
	// the project node of .sal/config.jsonld. Both are empty for a
	// remote table, which has no project on disk, and for a project that
	// recorded neither.
	Title       string `json:"title"`
	Description string `json:"description"`
	Triples     int64  `json:"triples"`
	Subjects    int64  `json:"subjects"`
	Predicates  int64  `json:"predicates"`
	Objects     int64  `json:"objects"`
	Snapshots   Result `json:"snapshots"`
	Properties  Result `json:"properties"`
	// Layout is how the table is laid out at its current snapshot, read from
	// the Iceberg metadata; LayoutError says why it could not be when it is
	// nil, so the rest of the view still answers.
	Layout      *TableLayout `json:"layout"`
	LayoutError string       `json:"layoutError,omitempty"`
	// Modules are the SAL module URIs the build that wrote this table downloaded.
	Modules []string `json:"modules"`
	// Vocabularies is the same listing `sal get vocabularies` prints: every
	// vocabulary build/validate has pinned, unioned with what sal import has
	// recorded, each marked with whether it is imported.
	Vocabularies Result `json:"vocabularies"`
	// ImportedTables are the imported data products queryable as views of their
	// own, one per OCI artifact the project imported.
	ImportedTables []ImportedTable `json:"importedTables"`
	// SampleQueries are the sample statements the SQL editor offers that only
	// the server can write: time travel needs the literal snapshot IDs this
	// table has, and listing the imports needs the artifact each view came
	// from, which DuckDB has no catalog of.
	SampleQueries []NamedQuery `json:"sampleQueries"`
}

// statsSections are the `sal query --info` options the stats view reports, in the
// order statsFromResults expects them after the counts.
var statsSections = []string{"snapshots", "properties"}

// Stats collects the counts, snapshots, table properties, and layout of the triples table.
//
// What is read from the table is cached per table under the metadata file
// that is current (statsCache), so a refresh costs one read of the version
// hint until a build commits a new snapshot; counting the distinct terms of a
// large table and listing its files is what made every visit to the Stats tab
// slow. The project's own title, description, and vocabularies come from
// small files on disk and are read fresh each time.
func (r DuckDBRunner) Stats(ctx context.Context) (TableStats, error) {
	metadataFile, err := r.latestMetadataFile(ctx)
	if err != nil {
		return TableStats{}, err
	}
	stats, err := tableStatsCache.get(ctx, r.TablePath, metadataFile, func(ctx context.Context) (TableStats, error) {
		return r.tableStats(ctx, metadataFile)
	})
	if err != nil {
		return TableStats{}, err
	}
	// the vocabulary listing comes from the project's own .sal/config.jsonld,
	// which says nothing about a table on object storage
	if !isRemoteTablePath(r.TablePath) {
		stats.Vocabularies = r.vocabulariesResult()
		project := r.Project()
		stats.Title, stats.Description = project.Title, project.Description
	} else {
		stats.Vocabularies = Result{Header: importation.VocabularyTableHeader}
	}
	stats.ImportedTables = r.Imports
	if len(r.Imports) > 0 {
		// cloned so that appending never writes into the cached entry's array
		stats.SampleQueries = append(slices.Clone(stats.SampleQueries), NamedQuery{
			Name: "Imported data products",
			SQL:  ImportedTablesSQL,
		})
	}
	return stats, nil
}

// tableStats reads everything the stats view reports from the table itself,
// at the given metadata file: the counts, snapshots, properties, and layout.
// The statements run one after another on the shared DuckDB handle.
func (r DuckDBRunner) tableStats(ctx context.Context, metadataFile string) (TableStats, error) {
	statements := make([]string, 0, len(statsSections)+1)
	statements = append(statements, countsSQL())
	for _, section := range statsSections {
		sql, err := infoSQL(section, r.TablePath, metadataFile)
		if err != nil {
			return TableStats{}, err
		}
		statements = append(statements, sql)
	}

	results := make([]Result, 0, len(statements))
	for i, statement := range statements {
		result, err := r.RunSQL(ctx, statement)
		if err != nil {
			return TableStats{}, fmt.Errorf("stats statement %d: %w", i, err)
		}
		results = append(results, result)
	}
	stats, err := statsFromResults(r.TablePath, results)
	if err != nil {
		return TableStats{}, err
	}
	if stats.Layout, err = r.layoutAt(ctx, metadataFile); err != nil {
		slog.Warn("reading the table layout for the stats view", "error", err)
		stats.LayoutError = err.Error()
	}
	return stats, nil
}

// tableStatsCache is the process-wide cache of what Stats reads from a table.
var tableStatsCache = &statsCache{entries: map[string]cachedStats{}}

// statsCache holds, per table path, the stats read at the metadata file that
// was current when they were read. A build commits a new metadata file, so a
// different current file is what makes an entry stale; only the latest entry
// per table is kept. A glob, which a local table without a version hint is
// read through, names no version and is never cached. Concurrent requests for
// the same table and version share one read.
type statsCache struct {
	mu      sync.Mutex
	entries map[string]cachedStats
	flight  singleflight.Group
}

type cachedStats struct {
	metadataFile string
	stats        TableStats
}

// get returns the stats cached for the table at metadataFile, reading them
// with read when there are none. A failed read is not cached.
func (c *statsCache) get(ctx context.Context, tablePath string, metadataFile string, read func(context.Context) (TableStats, error)) (TableStats, error) {
	if strings.Contains(metadataFile, "*") {
		return read(ctx)
	}
	c.mu.Lock()
	entry, ok := c.entries[tablePath]
	c.mu.Unlock()
	if ok && entry.metadataFile == metadataFile {
		return entry.stats, nil
	}
	value, err, _ := c.flight.Do(tablePath+"\x00"+metadataFile, func() (any, error) {
		// one request giving up must not fail the others waiting on the read
		stats, err := read(context.WithoutCancel(ctx))
		if err != nil {
			return TableStats{}, err
		}
		c.mu.Lock()
		c.entries[tablePath] = cachedStats{metadataFile: metadataFile, stats: stats}
		c.mu.Unlock()
		return stats, nil
	})
	if err != nil {
		return TableStats{}, err
	}
	return value.(TableStats), nil
}

// standaloneBase stands in for the project base when a data directory is
// served with no git repository around it. The config's own node is named
// "." and so resolves against it, but nothing served reads that IRI, only the
// title, description, imports, and pins.
const standaloneBase = "https://sal.invalid/"

// projectBase is the base the project's config resolves against: the git
// remote's, or standaloneBase for a served data directory without one.
func projectBase(standalone bool) (string, error) {
	base, err := pkg.DefaultSalBase()
	if err != nil && standalone {
		return standaloneBase, nil
	}
	return base, err
}

// projectOntology reads the project's own node out of .sal/config.jsonld:
// its title and description name the data product, and its owl:imports IRIs
// are the imported artifacts. They come from disk rather than from the table
// because the table carries the imported statements, not the documents they
// were fetched from. A directory that is not a SAL project, or a project that
// has never imported anything, simply has none rather than failing the whole
// stats view.
func projectOntology(path string, standalone bool) *importation.Ontology {
	base, err := projectBase(standalone)
	if err != nil {
		slog.Debug("not reporting the project ontology", "error", err)
		return nil
	}
	ontology, err := importation.ReadOntology(path, base)
	if err != nil {
		slog.Warn("ignoring unreadable project ontology", "path", path, "error", err)
		return nil
	}
	return ontology
}

// vocabulariesResult builds the same vocabulary listing `sal get vocabularies`
// reports, as a Result the UI can render with the same header and rows a
// `sal query --info` result gets. A directory that is not a SAL project, or
// one with nothing pinned or imported yet, simply reports no rows rather
// than failing the whole stats view.
func (r DuckDBRunner) vocabulariesResult() Result {
	header := importation.VocabularyTableHeader
	path, _, err := r.projectFiles()
	if err != nil {
		slog.Debug("not reporting project vocabularies", "error", err)
		return Result{Header: header}
	}
	base, err := projectBase(r.DataRoot != "")
	if err != nil {
		slog.Debug("not reporting project vocabularies", "error", err)
		return Result{Header: header}
	}
	rows, err := importation.ProjectVocabularyRows(path, base)
	if err != nil {
		slog.Warn("ignoring unreadable project vocabularies", "path", path, "error", err)
		return Result{Header: header}
	}
	return Result{Header: header, Rows: importation.VocabularyTableRows(rows)}
}

// statsFromResults assembles the stats payload from the batched results, in the
// order Stats issued them: counts, then one per statsSections entry.
func statsFromResults(tablePath string, results []Result) (TableStats, error) {
	if len(results) != len(statsSections)+1 {
		return TableStats{}, fmt.Errorf("expected %d stats results, got %d", len(statsSections)+1, len(results))
	}
	counts := results[0]
	if len(counts.Rows) == 0 || len(counts.Rows[0]) < 4 {
		return TableStats{}, fmt.Errorf("triples table returned no counts")
	}

	stats := TableStats{
		TablePath:  tablePath,
		Triples:    parseCount(counts.Rows[0][0]),
		Subjects:   parseCount(counts.Rows[0][1]),
		Predicates: parseCount(counts.Rows[0][2]),
		Objects:    parseCount(counts.Rows[0][3]),
		Snapshots:  results[1],
		Properties: results[2],
	}
	stats.Modules = modulesFromProperties(stats.Properties)
	stats.SampleQueries = snapshotQueries(tablePath, stats.Snapshots)
	return stats, nil
}

// modulesFromProperties reads the SAL module URIs a build recorded in the
// Iceberg table properties. A table built without modules, or by a version of
// SAL that did not record them, simply has none.
func modulesFromProperties(properties Result) []string {
	keyIndex := slices.Index(properties.Header, "key")
	valueIndex := slices.Index(properties.Header, "value")
	if keyIndex < 0 || valueIndex < 0 {
		return nil
	}
	for _, row := range properties.Rows {
		if len(row) <= max(keyIndex, valueIndex) || row[keyIndex] != salmodule.IcebergTableProperty {
			continue
		}
		var modules []string
		if err := json.Unmarshal([]byte(row[valueIndex]), &modules); err != nil {
			slog.Warn("ignoring malformed "+salmodule.IcebergTableProperty+" table property", "error", err)
			return nil
		}
		return modules
	}
	return nil
}

// countsSQL counts the triples and their distinct subjects, predicates, and
// objects for the stats view. The subjects and objects are estimated with
// approx_count_distinct, a HyperLogLog sketch of fixed size, since an exact
// COUNT(DISTINCT) holds every distinct term in a hash table: over a table of
// tens of millions of triples that is gigabytes, which got a Cloud Run
// instance killed on every visit to the Stats tab. The predicates are few, so
// they are still counted exactly.
func countsSQL() string {
	return fmt.Sprintf(`
SELECT
	COUNT(*) AS triples,
	approx_count_distinct(triples.subject) AS subjects,
	COUNT(DISTINCT triples.predicate) AS predicates,
	approx_count_distinct(%s) AS objects
FROM triples`, bindingExpr("triples", "object"))
}

func parseCount(value string) int64 {
	count, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return count
}

// InfoSQL returns the DuckDB query behind a `sal query --info` option against
// the `triples` view. The properties and snapshots options read the table's
// latest metadata file, which is found through the version hint first, so
// that they work on a table on object storage too; see latestMetadataFile.
func (r DuckDBRunner) InfoSQL(ctx context.Context, info string) (string, error) {
	switch info {
	case "properties", "snapshots":
		metadataFile, err := r.latestMetadataFile(ctx)
		if err != nil {
			return "", err
		}
		return infoSQL(info, r.TablePath, metadataFile)
	default:
		return infoSQL(info, r.TablePath, "")
	}
}

// latestMetadataFile is the path of the table's current metadata file, as
// read_text can read it. The Hadoop catalog iceberg-go writes a built table
// with, and `sal upload`, both leave the current version number in
// metadata/version-hint.text, so the file is named from that rather than by
// globbing metadata/*.metadata.json: a glob lists a directory, which the
// generic HTTP reader behind an https:// table cannot do. A local table whose
// hint cannot be read falls back to the glob, which the DuckDB queries then
// order by version themselves.
func (r DuckDBRunner) latestMetadataFile(ctx context.Context) (string, error) {
	// the hint is read without the triples view, which there is no need to
	// create or replace just to read one file
	db, err := instance.prepare(ctx, "", nil, false)
	if err != nil {
		return "", err
	}
	_, rows, err := queryRows(ctx, db, fmt.Sprintf("SELECT content FROM read_text('%s/metadata/version-hint.text')", escapeSQLLiteral(r.TablePath)))
	if err == nil && len(rows) == 1 && strings.TrimSpace(rows[0][0]) != "" {
		return r.TablePath + "/metadata/v" + strings.TrimSpace(rows[0][0]) + ".metadata.json", nil
	}
	if isRemoteTablePath(r.TablePath) {
		if err == nil {
			err = fmt.Errorf("metadata/version-hint.text is empty")
		}
		return "", fmt.Errorf("read the current metadata version of the triples table at %s: %w", r.TablePath, err)
	}
	return r.TablePath + "/metadata/*.metadata.json", nil
}

// metadataFiles is what the snapshots query reads to find the metadata file
// each snapshot was committed in: every metadata file of the table, which
// a local table and one on object storage can be asked for with a glob,
// and for an https table, whose directory the generic HTTP reader cannot
// list, the latest file alone.
func metadataFiles(tablePath string, latest string) string {
	if scheme, _, ok := strings.Cut(tablePath, "://"); ok && (strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")) {
		return latest
	}
	return tablePath + "/metadata/*.metadata.json"
}

// infoSQL is InfoSQL over an explicit metadata file, a single path or a glob,
// which the properties and snapshots queries read.
func infoSQL(info string, tablePath string, metadataFile string) (string, error) {
	escapedTablePath := escapeSQLLiteral(tablePath)
	escapedMetadataFile := escapeSQLLiteral(metadataFile)
	switch info {
	case "", "head":
		return "SELECT * FROM triples LIMIT 20", nil
	case "properties":
		return fmt.Sprintf(`
WITH latest_metadata AS (
	SELECT
		filename,
		content::JSON AS metadata_json
	FROM read_text('%s')
	ORDER BY regexp_extract(filename, 'v([0-9]+)\.metadata\.json', 1)::BIGINT DESC
	LIMIT 1
)
SELECT
	prop.key,
	json_extract_string(prop.value, '$') AS value
FROM latest_metadata,
json_each(json_extract(metadata_json, '$.properties')) AS prop
ORDER BY prop.key`, escapedMetadataFile), nil
	case "snapshots":
		// the message and triple counts a build recorded in a snapshot's
		// summary and the git commits tagged on it are read from the latest
		// metadata file, since iceberg_snapshots() reports none of them. The
		// metadata file a snapshot was committed in is the lowest version
		// naming it as current, found by reading every metadata file the
		// table has; an https table cannot be listed, so there only the
		// latest file is read and only the current snapshot has one.
		return fmt.Sprintf(`
WITH snapshots AS (
	SELECT *
	FROM iceberg_snapshots('%s')
),
latest_metadata AS (
	SELECT
		filename,
		content::JSON AS metadata_json
	FROM read_text('%s')
	ORDER BY regexp_extract(filename, 'v([0-9]+)\.metadata\.json', 1)::BIGINT DESC
	LIMIT 1
),
git_commits AS (
	SELECT
		json_extract(ref.value, '$."snapshot-id"')::BIGINT AS snapshot_id,
		string_agg(ref.key, ', ' ORDER BY ref.key) AS git_commits
	FROM latest_metadata,
	json_each(json_extract(metadata_json, '$.refs')) AS ref
	WHERE json_extract_string(ref.value, '$.type') = 'tag'
	GROUP BY snapshot_id
),
summaries AS (
	SELECT
		json_extract(snapshot.value, '$."snapshot-id"')::BIGINT AS snapshot_id,
		COALESCE(json_extract_string(snapshot.value, '$.summary."%s"'), json_extract_string(snapshot.value, '$.summary."added-records"'), '0')::BIGINT AS triples_added,
		COALESCE(json_extract_string(snapshot.value, '$.summary."%s"'), json_extract_string(snapshot.value, '$.summary."added-equality-deletes"'), '0')::BIGINT AS triples_removed,
		json_extract_string(snapshot.value, '$.summary."%s"') AS message
	FROM latest_metadata,
	json_each(json_extract(metadata_json, '$.snapshots')) AS snapshot
),
metadata_files AS (
	SELECT
		json_extract(content::JSON, '$."current-snapshot-id"')::BIGINT AS snapshot_id,
		arg_min(filename, regexp_extract(filename, 'v([0-9]+)\.metadata\.json', 1)::BIGINT) AS metadata_file
	FROM read_text('%s')
	GROUP BY snapshot_id
)
SELECT
	snapshots.sequence_number,
	snapshots.snapshot_id,
	snapshots.parent_id,
	snapshots.timestamp_ms,
	snapshots.operation,
	summaries.triples_added,
	summaries.triples_removed,
	summaries.message,
	git_commits.git_commits,
	metadata_files.metadata_file
FROM snapshots
LEFT JOIN summaries ON summaries.snapshot_id = snapshots.snapshot_id
LEFT JOIN git_commits ON git_commits.snapshot_id = snapshots.snapshot_id
LEFT JOIN metadata_files ON metadata_files.snapshot_id = snapshots.snapshot_id
ORDER BY snapshots.sequence_number DESC`, escapedTablePath, escapedMetadataFile, pkg.SnapshotTriplesAddedProperty, pkg.SnapshotTriplesRemovedProperty, pkg.SnapshotMessageProperty, escapeSQLLiteral(metadataFiles(tablePath, metadataFile))), nil
	case "column-stats":
		return fmt.Sprintf("SELECT * FROM iceberg_column_stats('%s', allow_moved_paths = true)", escapedTablePath), nil
	default:
		return "", fmt.Errorf("unknown info option %q; expected one of: head, properties, snapshots, column-stats", info)
	}
}
