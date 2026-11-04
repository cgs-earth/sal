package sparql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const layoutMetadata = `{
  "format-version": 2,
  "current-schema-id": 1,
  "schemas": [
    {"schema-id": 0, "fields": [{"id": 1, "name": "subject"}, {"id": 2, "name": "predicate"}]},
    {"schema-id": 1, "fields": [{"id": 1, "name": "subject"}, {"id": 2, "name": "predicate"}, {"id": 3, "name": "object_iri"}]}
  ],
  "default-spec-id": 1,
  "partition-specs": [
    {"spec-id": 0, "fields": []},
    {"spec-id": 1, "fields": [{"name": "predicate_partition", "source-id": 2, "transform": "truncate[20]", "field-id": 1000}]}
  ],
  "default-sort-order-id": 1,
  "sort-orders": [
    {"order-id": 0, "fields": []},
    {"order-id": 1, "fields": [{"source-id": 2, "transform": "identity", "direction": "asc", "null-order": "nulls-last"}, {"source-id": 1, "transform": "bucket[4]", "direction": "desc", "null-order": "nulls-last"}]}
  ]
}`

const layoutTable = "/tmp/warehouse/sal/triples"

func layoutFile(name string, content string, rows string, sequence string, partition string, bytes string) []string {
	return []string{layoutTable + "/data/predicate_partition=" + partition + "/" + name, content, rows, sequence, "{'r0': '" + partition + "'}", bytes}
}

func layoutFiles(rows ...[]string) Result {
	return Result{Header: []string{"file_path", "content", "record_count", "file_sequence_number", "partition", "bytes"}, Rows: rows}
}

func layoutBounds(rows ...[]string) Result {
	return Result{Header: []string{"file_path", "column_name", "column_type", "lower_bound", "upper_bound", "null_value_count"}, Rows: rows}
}

func TestLayoutReadsTheCurrentPartitionSpecAndSortOrder(t *testing.T) {
	layout, err := layoutFromResults(layoutTable, layoutMetadata, layoutFiles(), layoutBounds())

	require.NoError(t, err)
	require.Equal(t, []PartitionField{{Name: "predicate_partition", Column: "predicate", Transform: "truncate[20]"}}, layout.Partitioning)
	require.Equal(t, []string{"predicate asc", "bucket[4](subject) desc"}, layout.SortOrder)
	require.Zero(t, layout.DataFiles.Count)
	require.Nil(t, layout.Outliers)
}

func TestLayoutRejectsMalformedMetadata(t *testing.T) {
	_, err := layoutFromResults(layoutTable, "{", layoutFiles(), layoutBounds())

	require.ErrorContains(t, err, "parse the table metadata")
}

func TestLayoutSummarizesDataAndDeleteFilesApart(t *testing.T) {
	layout, err := layoutFromResults(layoutTable, layoutMetadata, layoutFiles(
		layoutFile("a.parquet", "DATA", "100", "1", "https%3A%2F%2Fschema.org%2Fn", "2000"),
		layoutFile("b.parquet", "DATA", "300", "1", "https%3A%2F%2Fschema.org%2Fn", "6000"),
		layoutFile("c.parquet", "DATA", "200", "2", "http%3A%2F%2Fwww.w3.org%2F19", "4000"),
		layoutFile("d.parquet", "EQUALITY_DELETES", "5", "2", "https%3A%2F%2Fschema.org%2Fn", "500"),
		// a file listed by two manifests is one file
		layoutFile("c.parquet", "DATA", "200", "2", "http%3A%2F%2Fwww.w3.org%2F19", "4000"),
	), layoutBounds())

	require.NoError(t, err)
	require.Equal(t, 2, layout.Partitions)
	require.Equal(t, FileSummary{Count: 3, Rows: 600, Bytes: 12000, AverageRows: 200, MedianRows: 200, MinRows: 100, MaxRows: 300}, layout.DataFiles)
	require.Equal(t, FileSummary{Count: 1, Rows: 5, Bytes: 500, AverageRows: 5, MedianRows: 5, MinRows: 5, MaxRows: 5}, layout.DeleteFiles)
	require.Len(t, layout.Files, 4)
	require.Equal(t, DataFile{
		Path:           "data/predicate_partition=https%3A%2F%2Fschema.org%2Fn/d.parquet",
		Partition:      "https%3A%2F%2Fschema.org%2Fn",
		Kind:           "equality-deletes",
		Rows:           5,
		Bytes:          500,
		SequenceNumber: 2,
	}, layout.Files[3])
	// fewer than four data files are too few to call any an outlier
	require.Nil(t, layout.Outliers)
}

func TestLayoutReadsThePartitionFromThePathWhenTheStatsHaveNone(t *testing.T) {
	layout, err := layoutFromResults(layoutTable, layoutMetadata, Result{
		Header: []string{"file_path", "content", "record_count", "file_sequence_number", "partition", "bytes"},
		Rows:   [][]string{{layoutTable + "/data/predicate_partition=https%3A%2F%2Fschema.org%2Fn/a.parquet", "DATA", "10", "1", "", ""}},
	}, layoutBounds())

	require.NoError(t, err)
	require.Equal(t, "predicate_partition=https%3A%2F%2Fschema.org%2Fn", layout.Files[0].Partition)
	require.Equal(t, 1, layout.Partitions)
}

func TestLayoutMedianOfAnEvenCountIsTheMiddlePair(t *testing.T) {
	layout, err := layoutFromResults(layoutTable, layoutMetadata, layoutFiles(
		layoutFile("a.parquet", "DATA", "10", "1", "p", "1"),
		layoutFile("b.parquet", "DATA", "20", "1", "p", "1"),
		layoutFile("c.parquet", "DATA", "40", "1", "p", "1"),
		layoutFile("d.parquet", "DATA", "80", "1", "p", "1"),
	), layoutBounds())

	require.NoError(t, err)
	require.Equal(t, int64(30), layout.DataFiles.MedianRows)
	require.Equal(t, 37.5, layout.DataFiles.AverageRows)
}

func TestLayoutCallsOutFilesThatStandOut(t *testing.T) {
	layout, err := layoutFromResults(layoutTable, layoutMetadata, layoutFiles(
		layoutFile("tiny.parquet", "DATA", "1", "3", "p", "1000"),
		layoutFile("a.parquet", "DATA", "100", "1", "p", "1000"),
		layoutFile("b.parquet", "DATA", "100", "1", "p", "1000"),
		layoutFile("c.parquet", "DATA", "100", "1", "p", "1000"),
		layoutFile("d.parquet", "DATA", "2000", "1", "p", "1000"),
		layoutFile("huge.parquet", "DATA", "4690", "1", "p", "1000"),
		layoutFile("small.parquet", "DATA", "9", "2", "p", "1000"),
	), layoutBounds())

	require.NoError(t, err)
	require.Equal(t, int64(100), layout.DataFiles.MedianRows)
	require.Equal(t, 1000.0, layout.DataFiles.AverageRows)
	// the furthest first, a tenth as far as four times; d is under four
	// times the average though far over the median, and a small file is
	// measured against the median
	require.Len(t, layout.Outliers, 3)
	require.Equal(t, "tiny.parquet", pathBase(layout.Outliers[0].File.Path))
	require.Equal(t, "small", layout.Outliers[0].Kind)
	require.Equal(t, 0.01, layout.Outliers[0].Ratio)
	require.Equal(t, "small.parquet", pathBase(layout.Outliers[1].File.Path))
	require.Equal(t, 0.09, layout.Outliers[1].Ratio)
	require.Equal(t, "huge.parquet", pathBase(layout.Outliers[2].File.Path))
	require.Equal(t, "large", layout.Outliers[2].Kind)
	require.Equal(t, 4.69, layout.Outliers[2].Ratio)
}

func pathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func TestLayoutMeasuresHowManyFilesOneValueCanTouch(t *testing.T) {
	a, b, c := layoutTable+"/data/p=x/a.parquet", layoutTable+"/data/p=y/b.parquet", layoutTable+"/data/p=z/c.parquet"
	layout, err := layoutFromResults(layoutTable, layoutMetadata, layoutFiles(), layoutBounds(
		// predicate ranges are disjoint: a value falls in one file
		[]string{a, "predicate", "VARCHAR", "http://a", "http://b", "0"},
		[]string{b, "predicate", "VARCHAR", "http://c", "http://d", "0"},
		[]string{c, "predicate", "VARCHAR", "http://e", "http://f", "0"},
		// subject ranges all overlap: a value can fall in every file
		[]string{a, "subject", "VARCHAR", "http://a", "http://z", "0"},
		[]string{b, "subject", "VARCHAR", "http://b", "http://y", "0"},
		[]string{c, "subject", "VARCHAR", "http://c", "http://x", "0"},
		// two of three numeric ranges share a point, compared as numbers
		[]string{a, "object_integer", "BIGINT", "1", "9", "5"},
		[]string{b, "object_integer", "BIGINT", "10", "100", "0"},
		[]string{c, "object_integer", "BIGINT", "50", "60", "7"},
		// one file records no bounds for the column
		[]string{a, "object_float", "DOUBLE", "", "", "9"},
		[]string{b, "object_float", "DOUBLE", "1.5", "2.5", "0"},
		[]string{c, "object_float", "DOUBLE", "", "", "9"},
		// a geometry's bounds are boxes, not a range a value falls in
		[]string{a, "object_geometry", "GEOMETRY('OGC:CRS84')", `{"bbox_x":-1}`, `{"bbox_x":1}`, "0"},
		[]string{b, "object_geometry", "GEOMETRY('OGC:CRS84')", `{"bbox_x":-1}`, `{"bbox_x":1}`, "0"},
	))

	require.NoError(t, err)
	names := make([]string, 0, len(layout.Columns))
	for _, column := range layout.Columns {
		names = append(names, column.Name)
	}
	require.Equal(t, []string{"subject", "predicate", "object_integer", "object_float", "object_geometry"}, names, "in schema order")
	require.Equal(t, ColumnPruning{Name: "subject", Type: "VARCHAR", FilesWithBounds: 3, MaxFilesPerValue: 3, Lower: "http://a", Upper: "http://z"}, layout.Columns[0])
	require.Equal(t, ColumnPruning{Name: "predicate", Type: "VARCHAR", FilesWithBounds: 3, MaxFilesPerValue: 1, Lower: "http://a", Upper: "http://f"}, layout.Columns[1])
	require.Equal(t, ColumnPruning{Name: "object_integer", Type: "BIGINT", FilesWithBounds: 3, MaxFilesPerValue: 2, Nulls: 12, Lower: "1", Upper: "100"}, layout.Columns[2])
	require.Equal(t, ColumnPruning{Name: "object_float", Type: "DOUBLE", FilesWithBounds: 1, MaxFilesPerValue: 1, Nulls: 18, Lower: "1.5", Upper: "2.5"}, layout.Columns[3])
	require.Equal(t, ColumnPruning{Name: "object_geometry", Type: "GEOMETRY('OGC:CRS84')", FilesWithBounds: 2, MaxFilesPerValue: 0, Lower: `{"bbox_x":-1}`, Upper: `{"bbox_x":1}`}, layout.Columns[4])
}

func TestMaxOverlapCountsAnInclusiveTouchAsShared(t *testing.T) {
	require.Equal(t, 2, maxOverlap("VARCHAR", []columnBound{{"a", "m"}, {"m", "z"}}))
	require.Equal(t, 1, maxOverlap("VARCHAR", []columnBound{{"a", "l"}, {"m", "z"}}))
	require.Equal(t, 0, maxOverlap("VARCHAR", nil))
	// 9 < 10 as numbers though not as text
	require.Equal(t, 1, maxOverlap("BIGINT", []columnBound{{"1", "9"}, {"10", "20"}}))
	require.Equal(t, 2, maxOverlap("VARCHAR", []columnBound{{"1", "9"}, {"10", "20"}}))
}

func TestLayoutSQLEscapesTheTablePath(t *testing.T) {
	require.Contains(t, layoutFilesSQL("/tmp/o'brien/triples"), "iceberg_metadata('/tmp/o''brien/triples')")
	require.Contains(t, layoutFilesSQL("/tmp/o'brien/triples"), "iceberg_column_stats('/tmp/o''brien/triples')")
	require.Contains(t, layoutBoundsSQL("/tmp/o'brien/triples"), "iceberg_column_stats('/tmp/o''brien/triples')")
	require.Contains(t, layoutBoundsSQL("/tmp/o'brien/triples"), "content = 'DATA'")
}
