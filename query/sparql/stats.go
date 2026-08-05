package sparql

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

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
	// SnapshotQueries are the time travel sample queries the SQL editor offers,
	// built from the snapshot IDs this table actually has.
	SnapshotQueries []NamedQuery `json:"snapshotQueries"`
}

// Stats collects the counts, snapshots, table properties, and column statistics of the triples table.
func (r DuckDBRunner) Stats(ctx context.Context) (TableStats, error) {
	counts, err := r.RunSQL(ctx, countsSQL(r.Layout))
	if err != nil {
		return TableStats{}, err
	}
	if len(counts.Rows) == 0 || len(counts.Rows[0]) < 4 {
		return TableStats{}, fmt.Errorf("triples table returned no counts")
	}

	stats := TableStats{
		TablePath:  r.TablePath,
		Triples:    parseCount(counts.Rows[0][0]),
		Subjects:   parseCount(counts.Rows[0][1]),
		Predicates: parseCount(counts.Rows[0][2]),
		Objects:    parseCount(counts.Rows[0][3]),
	}

	sections := []struct {
		info   string
		target *Result
	}{
		{"snapshots", &stats.Snapshots},
		{"properties", &stats.Properties},
		{"column-stats", &stats.ColumnStats},
	}
	for _, section := range sections {
		sql, err := InfoSQL(section.info, r.TablePath)
		if err != nil {
			return TableStats{}, err
		}
		result, err := r.RunSQL(ctx, sql)
		if err != nil {
			return TableStats{}, err
		}
		*section.target = result
	}
	stats.Modules = modulesFromProperties(stats.Properties)
	stats.SnapshotQueries = snapshotQueries(r.TablePath, stats.Snapshots)
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
