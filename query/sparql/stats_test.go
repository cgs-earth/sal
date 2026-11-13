package sparql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// tableProperties mimics the two column result the properties query returns.
func tableProperties(rows ...[]string) Result {
	return Result{Header: []string{"key", "value"}, Rows: rows}
}

// statsResults mimics what the three statements Stats issues return, in order.
func statsResults(current Result, properties Result) []Result {
	snapshots := Result{
		Header: []string{"sequence_number", "snapshot_id"},
		Rows:   [][]string{{"2", "222"}, {"1", "111"}},
	}
	return []Result{current, snapshots, properties}
}

func TestStatsFromResultsPopulatesTheTriplesAndSections(t *testing.T) {
	current := Result{Header: []string{"snapshot_id", "triples"}, Rows: [][]string{{"222", "554"}}}
	properties := tableProperties([]string{"sal.salmodules", `["salmodule://github.com/test/one"]`})

	stats, err := statsFromResults("/tmp/table", statsResults(current, properties))

	require.NoError(t, err)
	require.Equal(t, "/tmp/table", stats.TablePath)
	require.Equal(t, int64(554), stats.Triples)
	require.Equal(t, []string{"salmodule://github.com/test/one"}, stats.Modules)
	require.Equal(t, properties, stats.Properties)
	require.Len(t, stats.Snapshots.Rows, 2)
	require.NotEmpty(t, stats.SampleQueries)
}

func TestStatsFromResultsCountsNoTriplesInATableWithNoSnapshot(t *testing.T) {
	current := Result{Header: []string{"snapshot_id", "triples"}}

	stats, err := statsFromResults("/tmp/table", statsResults(current, tableProperties()))

	require.NoError(t, err)
	require.Zero(t, stats.Triples)
}

func TestStatsFromResultsRejectsTheWrongNumberOfResults(t *testing.T) {
	_, err := statsFromResults("/tmp/table", []Result{{}, {}})

	require.ErrorContains(t, err, "expected 3 stats results, got 2")
}

func TestModulesFromPropertiesReadsRecordedModules(t *testing.T) {
	properties := tableProperties(
		[]string{"sal.hash", "abc123"},
		[]string{"sal.salmodules", `["salmodule://github.com/test/one","salmodule://github.com/test/two"]`},
	)

	modules := modulesFromProperties(properties)

	require.Equal(t, []string{"salmodule://github.com/test/one", "salmodule://github.com/test/two"}, modules)
}

func TestModulesFromPropertiesIsEmptyWithoutTheProperty(t *testing.T) {
	require.Empty(t, modulesFromProperties(tableProperties([]string{"sal.hash", "abc123"})))
}

func TestModulesFromPropertiesIgnoresMalformedJSON(t *testing.T) {
	require.Empty(t, modulesFromProperties(tableProperties([]string{"sal.salmodules", "not json"})))
}

func TestInfoSQLRejectsUnknownInfo(t *testing.T) {
	_, err := infoSQL("bogus", "/tmp/table", "")

	require.ErrorContains(t, err, "unknown info option")
	require.NotContains(t, err.Error(), "tags")
}

func TestInfoSQLBuildsSnapshotsQueryWithGitCommits(t *testing.T) {
	query, err := infoSQL("snapshots", "/tmp/table", "/tmp/table/metadata/v3.metadata.json")

	require.NoError(t, err)
	require.Contains(t, query, "FROM iceberg_snapshots('/tmp/table')")
	require.Contains(t, query, "read_text('/tmp/table/metadata/v3.metadata.json')")
	require.Contains(t, query, "json_each(json_extract(metadata_json, '$.refs'))")
	require.Contains(t, query, "WHERE json_extract_string(ref.value, '$.type') = 'tag'")
	require.Contains(t, query, "string_agg(ref.key, ', ' ORDER BY ref.key) AS git_commits")
	require.Contains(t, query, "LEFT JOIN git_commits ON git_commits.snapshot_id = snapshots.snapshot_id")
	// the manifest list and the raw summary are left out: the columns are
	// the snapshot, what it changed, its message, its commits, and its file
	require.NotContains(t, query, "snapshots.*")
	require.NotContains(t, query, "manifest_list")
}

func TestInfoSQLReadsTheBuildMessageAndTripleCountsOfEverySnapshot(t *testing.T) {
	query, err := infoSQL("snapshots", "/tmp/table", "/tmp/table/metadata/v3.metadata.json")

	require.NoError(t, err)
	require.Contains(t, query, "json_each(json_extract(metadata_json, '$.snapshots')) AS snapshot")
	require.Contains(t, query, `json_extract_string(snapshot.value, '$.summary."sal.message"') AS message`)
	// a snapshot written before sal recorded its own counts falls back to
	// Iceberg's, which say the same for sal's writes
	require.Contains(t, query, `COALESCE(json_extract_string(snapshot.value, '$.summary."sal.triples.added"'), json_extract_string(snapshot.value, '$.summary."added-records"'), '0')::BIGINT AS triples_added`)
	require.Contains(t, query, `COALESCE(json_extract_string(snapshot.value, '$.summary."sal.triples.removed"'), json_extract_string(snapshot.value, '$.summary."added-equality-deletes"'), '0')::BIGINT AS triples_removed`)
	require.Contains(t, query, "LEFT JOIN summaries ON summaries.snapshot_id = snapshots.snapshot_id")
	// the message sits between the counts and the commits
	require.Less(t, strings.Index(query, "summaries.triples_removed,"), strings.Index(query, "summaries.message,"))
	require.Less(t, strings.Index(query, "summaries.message,"), strings.Index(query, "git_commits.git_commits"))
}

// The metadata file a snapshot was committed in is the lowest version naming
// it as current, found over every metadata file of a table that can be
// listed; an https table cannot be, so only its latest file is read.
func TestInfoSQLFindsTheMetadataFileOfEverySnapshot(t *testing.T) {
	query, err := infoSQL("snapshots", "/tmp/table", "/tmp/table/metadata/v3.metadata.json")

	require.NoError(t, err)
	require.Contains(t, query, "read_text('/tmp/table/metadata/*.metadata.json')")
	require.Contains(t, query, `json_extract(content::JSON, '$."current-snapshot-id"')::BIGINT AS snapshot_id`)
	require.Contains(t, query, "arg_min(filename, regexp_extract(filename, 'v([0-9]+)\\.metadata\\.json', 1)::BIGINT) AS metadata_file")
	require.Contains(t, query, "LEFT JOIN metadata_files ON metadata_files.snapshot_id = snapshots.snapshot_id")

	query, err = infoSQL("snapshots", "gs://bucket/sal/triples", "gs://bucket/sal/triples/metadata/v3.metadata.json")
	require.NoError(t, err)
	require.Contains(t, query, "read_text('gs://bucket/sal/triples/metadata/*.metadata.json')")

	query, err = infoSQL("snapshots", "https://storage.googleapis.com/bucket/sal/triples", "https://storage.googleapis.com/bucket/sal/triples/metadata/v3.metadata.json")
	require.NoError(t, err)
	require.NotContains(t, query, "*.metadata.json")
	require.Equal(t, 2, strings.Count(query, "read_text('https://storage.googleapis.com/bucket/sal/triples/metadata/v3.metadata.json')"))
}

func TestInfoSQLRejectsTagsInfo(t *testing.T) {
	_, err := infoSQL("tags", "/tmp/table", "")

	require.ErrorContains(t, err, "unknown info option")
}

func TestInfoSQLEscapesSingleQuotesInTablePath(t *testing.T) {
	query, err := infoSQL("column-stats", "/tmp/o'brien/triples", "")

	require.NoError(t, err)
	require.Contains(t, query, "iceberg_column_stats('/tmp/o''brien/triples', allow_moved_paths = true)")
}

func TestLatestMetadataFileReadsTheVersionHint(t *testing.T) {
	table := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(table, "metadata"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(table, "metadata", "version-hint.text"), []byte("7\n"), 0o644))

	file, err := DuckDBRunner{TablePath: table}.latestMetadataFile(context.Background())

	require.NoError(t, err)
	require.Equal(t, table+"/metadata/v7.metadata.json", file)
}

func TestLatestMetadataFileFallsBackToTheGlobForALocalTableWithoutAHint(t *testing.T) {
	table := t.TempDir()

	file, err := DuckDBRunner{TablePath: table}.latestMetadataFile(context.Background())

	require.NoError(t, err)
	require.Equal(t, table+"/metadata/*.metadata.json", file)
}

func TestLatestMetadataFileFailsForARemoteTableWithoutAHint(t *testing.T) {
	_, err := DuckDBRunner{TablePath: "gs://sal-no-such-bucket-for-tests/sal/triples"}.latestMetadataFile(context.Background())

	require.Error(t, err)
	require.ErrorContains(t, err, "read the current metadata version of the triples table at gs://sal-no-such-bucket-for-tests/sal/triples")
}

func TestStatsCacheReusesStatsWhileTheMetadataFileIsCurrent(t *testing.T) {
	cache := &statsCache{entries: map[string]cachedStats{}}
	reads := 0
	read := func(context.Context) (TableStats, error) {
		reads++
		return TableStats{Triples: int64(reads)}, nil
	}

	first, err := cache.get(context.Background(), "/table", "/table/metadata/v3.metadata.json", read)
	require.NoError(t, err)
	second, err := cache.get(context.Background(), "/table", "/table/metadata/v3.metadata.json", read)
	require.NoError(t, err)

	require.Equal(t, 1, reads)
	require.Equal(t, first, second)
}

func TestStatsCacheReadsAgainOnceANewMetadataFileIsCurrent(t *testing.T) {
	cache := &statsCache{entries: map[string]cachedStats{}}
	reads := 0
	read := func(context.Context) (TableStats, error) {
		reads++
		return TableStats{Triples: int64(reads)}, nil
	}

	_, err := cache.get(context.Background(), "/table", "/table/metadata/v3.metadata.json", read)
	require.NoError(t, err)
	stats, err := cache.get(context.Background(), "/table", "/table/metadata/v4.metadata.json", read)
	require.NoError(t, err)

	require.Equal(t, 2, reads)
	require.Equal(t, int64(2), stats.Triples)
	require.Len(t, cache.entries, 1)
}

func TestStatsCacheDoesNotCacheAFailedRead(t *testing.T) {
	cache := &statsCache{entries: map[string]cachedStats{}}
	reads := 0
	read := func(context.Context) (TableStats, error) {
		reads++
		if reads == 1 {
			return TableStats{}, errors.New("table unreachable")
		}
		return TableStats{Triples: 5}, nil
	}

	_, err := cache.get(context.Background(), "/table", "/table/metadata/v3.metadata.json", read)
	require.ErrorContains(t, err, "table unreachable")
	stats, err := cache.get(context.Background(), "/table", "/table/metadata/v3.metadata.json", read)
	require.NoError(t, err)

	require.Equal(t, int64(5), stats.Triples)
}

func TestStatsCacheNeverCachesAGlob(t *testing.T) {
	cache := &statsCache{entries: map[string]cachedStats{}}
	reads := 0
	read := func(context.Context) (TableStats, error) {
		reads++
		return TableStats{}, nil
	}

	for range 2 {
		_, err := cache.get(context.Background(), "/table", "/table/metadata/*.metadata.json", read)
		require.NoError(t, err)
	}

	require.Equal(t, 2, reads)
	require.Empty(t, cache.entries)
}

// The triples are the current snapshot's records less its equality deletes,
// read from the newest metadata file, not from an older snapshot or file.
func TestCurrentSnapshotSQLReadsTheLiveRecordsOfTheCurrentSnapshot(t *testing.T) {
	metadata := filepath.Join(t.TempDir(), "metadata")
	require.NoError(t, os.MkdirAll(metadata, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(metadata, "v1.metadata.json"), []byte(`{"current-snapshot-id": 1, "snapshots": [
		{"snapshot-id": 1, "summary": {"total-records": "5"}}]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(metadata, "v2.metadata.json"), []byte(`{"current-snapshot-id": 8888888888888888888, "snapshots": [
		{"snapshot-id": 1, "summary": {"total-records": "5"}},
		{"snapshot-id": 8888888888888888888, "summary": {"total-records": "700", "total-equality-deletes": "40"}}]}`), 0o644))

	header, rows, err := queryRows(context.Background(), sampleTriples(t), currentSnapshotSQL(filepath.Join(metadata, "*.metadata.json")))

	require.NoError(t, err)
	require.Equal(t, []string{"snapshot_id", "triples"}, header)
	require.Equal(t, [][]string{{"8888888888888888888", "660"}}, rows)
}
