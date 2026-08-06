package sparql

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
)

// TableStats summarizes an Iceberg triples table for the `sal serve --with-ui` stats view.
type TableStats struct {
	TablePath   string `json:"tablePath"`
	Triples     int64  `json:"triples"`
	Subjects    int64  `json:"subjects"`
	Predicates  int64  `json:"predicates"`
	Objects     int64  `json:"objects"`
	Snapshots   Result `json:"snapshots"`
	Properties  Result `json:"properties"`
	ColumnStats Result `json:"columnStats"`
	// Modules are the SAL module URIs the build that wrote this table downloaded.
	Modules []string `json:"modules"`
	// Imports are the owl:imports IRIs the project's .sal/ontology.ttl records.
	Imports []string `json:"imports"`
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
var statsSections = []string{"snapshots", "properties", "column-stats"}

// Stats collects the counts, snapshots, table properties, and column statistics of the triples table.
//
// The four queries run one after another on the shared DuckDB handle. They used
// to be batched into a single duckdb process because starting one cost far more
// than running them; with DuckDB linked in there is no per query startup left to
// amortize.
func (r DuckDBRunner) Stats(ctx context.Context) (TableStats, error) {
	statements := make([]string, 0, len(statsSections)+1)
	statements = append(statements, countsSQL(r.Layout))
	for _, section := range statsSections {
		sql, err := InfoSQL(section, r.TablePath)
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
	stats.Imports = projectImports()
	stats.ImportedTables = r.Imports
	if len(r.Imports) > 0 {
		stats.SampleQueries = append(stats.SampleQueries, NamedQuery{
			Name: "Imported data products",
			SQL:  ImportedTablesSQL,
		})
	}
	return stats, nil
}

// projectImports reads the owl:imports IRIs out of the project's
// .sal/ontology.ttl. They come from disk rather than from the table because the
// table carries the imported statements, not the documents they were fetched
// from. A directory that is not a SAL project, or a project that has imported
// nothing, simply reports none rather than failing the whole stats view.
func projectImports() []string {
	path, err := pkg.SalOntologyPath()
	if err != nil {
		slog.Debug("not reporting project imports", "error", err)
		return nil
	}
	base, err := pkg.DefaultSalBase()
	if err != nil {
		slog.Debug("not reporting project imports", "error", err)
		return nil
	}
	ontology, err := importation.ReadOntology(path, base)
	if err != nil {
		slog.Warn("ignoring unreadable project ontology", "path", path, "error", err)
		return nil
	}
	if ontology == nil {
		return nil
	}
	return ontology.Imports
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
		TablePath:   tablePath,
		Triples:     parseCount(counts.Rows[0][0]),
		Subjects:    parseCount(counts.Rows[0][1]),
		Predicates:  parseCount(counts.Rows[0][2]),
		Objects:     parseCount(counts.Rows[0][3]),
		Snapshots:   results[1],
		Properties:  results[2],
		ColumnStats: results[3],
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

func countsSQL(layout ObjectLayout) string {
	return fmt.Sprintf(`
SELECT
	COUNT(*) AS triples,
	COUNT(DISTINCT triples.subject) AS subjects,
	COUNT(DISTINCT triples.predicate) AS predicates,
	COUNT(DISTINCT %s) AS objects
FROM triples`, bindingExpr("triples", "object", layout))
}

func parseCount(value string) int64 {
	count, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return count
}

// InfoSQL returns the DuckDB query behind a `sal query --info` option against the `triples` view.
func InfoSQL(info string, tablePath string) (string, error) {
	escapedTablePath := strings.ReplaceAll(tablePath, "'", "''")
	switch info {
	case "", "head":
		return "SELECT * FROM triples LIMIT 20", nil
	case "properties":
		return fmt.Sprintf(`
WITH latest_metadata AS (
	SELECT
		filename,
		content::JSON AS metadata_json
	FROM read_text('%s/metadata/*.metadata.json')
	ORDER BY regexp_extract(filename, 'v([0-9]+)\.metadata\.json', 1)::BIGINT DESC
	LIMIT 1
)
SELECT
	prop.key,
	json_extract_string(prop.value, '$') AS value
FROM latest_metadata,
json_each(json_extract(metadata_json, '$.properties')) AS prop
ORDER BY prop.key`, escapedTablePath), nil
	case "snapshots":
		return fmt.Sprintf(`
WITH snapshots AS (
	SELECT *
	FROM iceberg_snapshots('%s')
),
latest_metadata AS (
	SELECT
		filename,
		content::JSON AS metadata_json
	FROM read_text('%s/metadata/*.metadata.json')
	ORDER BY regexp_extract(filename, 'v([0-9]+)\.metadata\.json', 1)::BIGINT DESC
	LIMIT 1
),
tags AS (
	SELECT
		json_extract(ref.value, '$."snapshot-id"')::BIGINT AS snapshot_id,
		string_agg(ref.key, ', ' ORDER BY ref.key) AS tags
	FROM latest_metadata,
	json_each(json_extract(metadata_json, '$.refs')) AS ref
	WHERE json_extract_string(ref.value, '$.type') = 'tag'
	GROUP BY snapshot_id
)
SELECT
	snapshots.*,
	tags.tags
FROM snapshots
LEFT JOIN tags ON tags.snapshot_id = snapshots.snapshot_id
ORDER BY snapshots.sequence_number DESC`, escapedTablePath, escapedTablePath), nil
	case "column-stats":
		return fmt.Sprintf("SELECT * FROM iceberg_column_stats('%s')", escapedTablePath), nil
	default:
		return "", fmt.Errorf("unknown info option %q; expected one of: head, properties, snapshots, column-stats", info)
	}
}
