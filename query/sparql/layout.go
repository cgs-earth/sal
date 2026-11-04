package sparql

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// TableLayout describes how the triples table is laid out at its current
// snapshot, read from Iceberg metadata alone: how it is partitioned and
// sorted, what its data and delete files hold, which files stand out from
// the rest, and how well a filter on each column can skip files by the
// bounds Iceberg records for it. It is what the Stats tab draws in place of
// the per-file column statistics, which say the same thing in nine hundred
// rows.
type TableLayout struct {
	// Partitioning is the current partition spec, one entry per field.
	Partitioning []PartitionField `json:"partitioning"`
	// SortOrder is the current sort order, each column as "name asc" or
	// "name desc"; empty for an unsorted table.
	SortOrder []string `json:"sortOrder"`
	// Partitions is how many distinct partition values the live data files
	// fall in.
	Partitions int `json:"partitions"`
	// DataFiles and DeleteFiles summarize the live files of each kind. A
	// delete file holds triples removed since an earlier snapshot, which
	// every scan subtracts from the data files.
	DataFiles   FileSummary `json:"dataFiles"`
	DeleteFiles FileSummary `json:"deleteFiles"`
	// Outliers are the data files far smaller than the median or far larger
	// than the average.
	Outliers []FileOutlier `json:"outliers"`
	// Columns says, per column, how well a filter on it can skip files.
	Columns []ColumnPruning `json:"columns"`
	// Files is every live file, for the detailed view.
	Files []DataFile `json:"files"`
}

// PartitionField is one field of a partition spec.
type PartitionField struct {
	Name      string `json:"name"`
	Column    string `json:"column"`
	Transform string `json:"transform"`
}

// FileSummary sums up the files of one kind.
type FileSummary struct {
	Count       int     `json:"count"`
	Rows        int64   `json:"rows"`
	Bytes       int64   `json:"bytes"`
	AverageRows float64 `json:"averageRows"`
	MedianRows  int64   `json:"medianRows"`
	MinRows     int64   `json:"minRows"`
	MaxRows     int64   `json:"maxRows"`
}

// DataFile is one live file of the table.
type DataFile struct {
	// Path is the file's path under the table root.
	Path      string `json:"path"`
	Partition string `json:"partition"`
	// Kind is "data", "equality-deletes", or "position-deletes".
	Kind           string `json:"kind"`
	Rows           int64  `json:"rows"`
	Bytes          int64  `json:"bytes"`
	SequenceNumber int64  `json:"sequenceNumber"`
}

// FileOutlier is a data file whose row count stands out: Kind is "small" for
// one far below the median, with Ratio its rows over the median, and "large"
// for one far above the average, with Ratio its rows over the average.
type FileOutlier struct {
	File  DataFile `json:"file"`
	Kind  string   `json:"kind"`
	Ratio float64  `json:"ratio"`
}

// ColumnPruning says how a filter on one column fares against the bounds
// Iceberg records per data file. FilesWithBounds is how many data files
// record a lower and upper bound for the column, and MaxFilesPerValue how
// many of those files a single value can fall in by its bounds, the most
// files an equality filter has to read; a column every file holds a wide
// range of touches every file, and a column the files partition or sort by
// touches few. A geometry's bounds are boxes, which no value falls in the
// way a point falls in a range, so its MaxFilesPerValue is 0. Nulls is the
// null count summed over the data files.
type ColumnPruning struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	FilesWithBounds  int    `json:"filesWithBounds"`
	MaxFilesPerValue int    `json:"maxFilesPerValue"`
	Nulls            int64  `json:"nulls"`
	Lower            string `json:"lower"`
	Upper            string `json:"upper"`
}

// layoutFilesSQL lists the table's live files: the entries of the current
// snapshot's manifests that are not marked deleted, each with the partition
// and the byte size the column statistics record for it, since
// iceberg_metadata reports neither.
func layoutFilesSQL(tablePath string) string {
	escaped := escapeSQLLiteral(tablePath)
	return fmt.Sprintf(`
SELECT
	files.file_path,
	files.content,
	files.record_count,
	files.file_sequence_number,
	sizes.partition,
	sizes.bytes
FROM iceberg_metadata('%s') AS files
LEFT JOIN (
	SELECT file_path, any_value(partition) AS partition, sum(column_size) AS bytes
	FROM iceberg_column_stats('%s')
	GROUP BY file_path
) AS sizes USING (file_path)
WHERE files.status <> 'DELETED'
ORDER BY files.file_path`, escaped, escaped)
}

// layoutBoundsSQL lists the bounds and counts every live data file records
// per column.
func layoutBoundsSQL(tablePath string) string {
	return fmt.Sprintf(`
SELECT file_path, column_name, column_type, lower_bound, upper_bound, null_value_count
FROM iceberg_column_stats('%s')
WHERE status <> 'DELETED' AND content = 'DATA'
ORDER BY file_path, column_name`, escapeSQLLiteral(tablePath))
}

// Layout reads the table's layout at its current snapshot.
func (r DuckDBRunner) Layout(ctx context.Context) (*TableLayout, error) {
	metadataFile, err := r.latestMetadataFile(ctx)
	if err != nil {
		return nil, err
	}
	metadata, err := r.RunSQL(ctx, fmt.Sprintf(`
SELECT content FROM read_text('%s')
ORDER BY regexp_extract(filename, 'v([0-9]+)\.metadata\.json', 1)::BIGINT DESC
LIMIT 1`, escapeSQLLiteral(metadataFile)))
	if err != nil {
		return nil, fmt.Errorf("read the table metadata: %w", err)
	}
	if len(metadata.Rows) == 0 {
		return nil, fmt.Errorf("the table has no metadata file at %s", metadataFile)
	}
	files, err := r.RunSQL(ctx, layoutFilesSQL(r.TablePath))
	if err != nil {
		return nil, fmt.Errorf("list the table's files: %w", err)
	}
	bounds, err := r.RunSQL(ctx, layoutBoundsSQL(r.TablePath))
	if err != nil {
		return nil, fmt.Errorf("read the table's column statistics: %w", err)
	}
	return layoutFromResults(r.TablePath, metadata.Rows[0][0], files, bounds)
}

// layoutFromResults assembles the layout from the metadata document and the
// two listings Layout reads.
func layoutFromResults(tablePath string, metadata string, files Result, bounds Result) (*TableLayout, error) {
	layout := &TableLayout{}
	if err := layout.readSpec(metadata); err != nil {
		return nil, err
	}
	layout.readFiles(tablePath, files)
	layout.readBounds(bounds)
	return layout, nil
}

// readSpec takes the current partition spec and sort order out of the
// metadata document, naming each field by the column of the current schema
// it is sourced from.
func (l *TableLayout) readSpec(content string) error {
	var metadata struct {
		CurrentSchemaID int `json:"current-schema-id"`
		Schemas         []struct {
			ID     int `json:"schema-id"`
			Fields []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"schemas"`
		DefaultSpecID  int `json:"default-spec-id"`
		PartitionSpecs []struct {
			ID     int `json:"spec-id"`
			Fields []struct {
				Name      string `json:"name"`
				SourceID  int    `json:"source-id"`
				Transform string `json:"transform"`
			} `json:"fields"`
		} `json:"partition-specs"`
		DefaultSortOrderID int `json:"default-sort-order-id"`
		SortOrders         []struct {
			ID     int `json:"order-id"`
			Fields []struct {
				SourceID  int    `json:"source-id"`
				Direction string `json:"direction"`
				Transform string `json:"transform"`
			} `json:"fields"`
		} `json:"sort-orders"`
	}
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		return fmt.Errorf("parse the table metadata: %w", err)
	}
	columns := map[int]string{}
	for _, schema := range metadata.Schemas {
		if schema.ID == metadata.CurrentSchemaID {
			for _, field := range schema.Fields {
				columns[field.ID] = field.Name
			}
		}
	}
	for _, spec := range metadata.PartitionSpecs {
		if spec.ID != metadata.DefaultSpecID {
			continue
		}
		for _, field := range spec.Fields {
			l.Partitioning = append(l.Partitioning, PartitionField{Name: field.Name, Column: columns[field.SourceID], Transform: field.Transform})
		}
	}
	for _, order := range metadata.SortOrders {
		if order.ID != metadata.DefaultSortOrderID {
			continue
		}
		for _, field := range order.Fields {
			name := columns[field.SourceID]
			if field.Transform != "" && field.Transform != "identity" {
				name = field.Transform + "(" + name + ")"
			}
			l.SortOrder = append(l.SortOrder, name+" "+field.Direction)
		}
	}
	return nil
}

// partitionValue is a value of the partition struct iceberg_column_stats
// renders, {'r0': 'https://schema.org/i'}.
var partitionValue = regexp.MustCompile(`: '((?:[^']|'')*)'`)

// readFiles reads the file listing into Files and the summaries, with the
// outliers among the data files.
func (l *TableLayout) readFiles(tablePath string, files Result) {
	column := func(name string) int { return slices.Index(files.Header, name) }
	pathIndex, contentIndex, rowsIndex, sequenceIndex, partitionIndex, bytesIndex :=
		column("file_path"), column("content"), column("record_count"), column("file_sequence_number"), column("partition"), column("bytes")
	seen := map[string]bool{}
	partitions := map[string]bool{}
	cell := func(row []string, index int) string {
		if index < 0 || index >= len(row) {
			return ""
		}
		return row[index]
	}
	for _, row := range files.Rows {
		filePath := cell(row, pathIndex)
		if filePath == "" || seen[filePath] {
			continue
		}
		seen[filePath] = true
		file := DataFile{
			Path:           strings.TrimPrefix(strings.TrimPrefix(filePath, tablePath), "/"),
			Kind:           fileKind(cell(row, contentIndex)),
			Rows:           parseCount(cell(row, rowsIndex)),
			Bytes:          parseCount(cell(row, bytesIndex)),
			SequenceNumber: parseCount(cell(row, sequenceIndex)),
		}
		var values []string
		for _, match := range partitionValue.FindAllStringSubmatch(cell(row, partitionIndex), -1) {
			values = append(values, strings.ReplaceAll(match[1], "''", "'"))
		}
		file.Partition = strings.Join(values, ", ")
		if file.Partition == "" {
			// the directory the file sits in names its partition too
			if directory := path.Base(path.Dir(filePath)); strings.Contains(directory, "=") {
				file.Partition = directory
			}
		}
		l.Files = append(l.Files, file)
		if file.Kind == "data" {
			partitions[file.Partition] = true
		}
	}
	l.Partitions = len(partitions)
	var data, deletes []DataFile
	for _, file := range l.Files {
		if file.Kind == "data" {
			data = append(data, file)
		} else {
			deletes = append(deletes, file)
		}
	}
	l.DataFiles = summarizeFiles(data)
	l.DeleteFiles = summarizeFiles(deletes)
	l.Outliers = fileOutliers(data, l.DataFiles)
}

func fileKind(content string) string {
	switch strings.ToUpper(content) {
	case "EQUALITY_DELETES":
		return "equality-deletes"
	case "POSITION_DELETES":
		return "position-deletes"
	default:
		return "data"
	}
}

func summarizeFiles(files []DataFile) FileSummary {
	summary := FileSummary{Count: len(files)}
	if len(files) == 0 {
		return summary
	}
	rows := make([]int64, 0, len(files))
	for _, file := range files {
		summary.Rows += file.Rows
		summary.Bytes += file.Bytes
		rows = append(rows, file.Rows)
	}
	slices.Sort(rows)
	summary.AverageRows = float64(summary.Rows) / float64(len(files))
	summary.MedianRows = rows[len(rows)/2]
	if len(rows)%2 == 0 {
		summary.MedianRows = (rows[len(rows)/2-1] + rows[len(rows)/2]) / 2
	}
	summary.MinRows, summary.MaxRows = rows[0], rows[len(rows)-1]
	return summary
}

// Outlier thresholds: a data file holding under a tenth of the median row
// count is small, one holding over four times the average is large. Either
// costs a scan: a small file is a file's worth of overhead for little data,
// and a large one is read whole by any filter its bounds admit. The median
// is the reference for a small file so that a few huge files do not make
// every ordinary one look small, and the average for a large one so that
// many tiny files, the diffs of builds that changed little, do not make
// every ordinary one look large.
const (
	smallFileFraction = 0.1
	largeFileMultiple = 4
	maxOutliers       = 25
)

// fileOutliers picks the data files that stand out, the furthest first, at
// most maxOutliers of them. A table of fewer than four data files has too
// little to compare.
func fileOutliers(files []DataFile, summary FileSummary) []FileOutlier {
	if len(files) < 4 || summary.MedianRows == 0 {
		return nil
	}
	var outliers []FileOutlier
	for _, file := range files {
		small := float64(file.Rows) / float64(summary.MedianRows)
		large := float64(file.Rows) / summary.AverageRows
		switch {
		case small < smallFileFraction:
			outliers = append(outliers, FileOutlier{File: file, Kind: "small", Ratio: small})
		case large > largeFileMultiple:
			outliers = append(outliers, FileOutlier{File: file, Kind: "large", Ratio: large})
		}
	}
	sort.SliceStable(outliers, func(i, j int) bool {
		return math.Abs(math.Log(outliers[i].Ratio+1e-9)) > math.Abs(math.Log(outliers[j].Ratio+1e-9))
	})
	if len(outliers) > maxOutliers {
		outliers = outliers[:maxOutliers]
	}
	return outliers
}

// columnBound is one data file's range for one column.
type columnBound struct {
	lower, upper string
}

// readBounds folds the per-file column statistics into one ColumnPruning
// per column, in the order the columns first appear.
func (l *TableLayout) readBounds(bounds Result) {
	column := func(name string) int { return slices.Index(bounds.Header, name) }
	nameIndex, typeIndex, lowerIndex, upperIndex, nullsIndex :=
		column("column_name"), column("column_type"), column("lower_bound"), column("upper_bound"), column("null_value_count")
	if nameIndex < 0 {
		return
	}
	cell := func(row []string, index int) string {
		if index < 0 || index >= len(row) {
			return ""
		}
		return row[index]
	}
	ranges := map[string][]columnBound{}
	pruning := map[string]*ColumnPruning{}
	for _, row := range bounds.Rows {
		name := cell(row, nameIndex)
		if name == "" {
			continue
		}
		entry, ok := pruning[name]
		if !ok {
			entry = &ColumnPruning{Name: name, Type: cell(row, typeIndex)}
			pruning[name] = entry
			l.Columns = append(l.Columns, ColumnPruning{})
		}
		entry.Nulls += parseCount(cell(row, nullsIndex))
		lower, upper := cell(row, lowerIndex), cell(row, upperIndex)
		if lower == "" || upper == "" {
			continue
		}
		entry.FilesWithBounds++
		ranges[name] = append(ranges[name], columnBound{lower: lower, upper: upper})
		if entry.Lower == "" || boundLess(entry.Type, lower, entry.Lower) {
			entry.Lower = lower
		}
		if entry.Upper == "" || boundLess(entry.Type, entry.Upper, upper) {
			entry.Upper = upper
		}
	}
	l.Columns = l.Columns[:0]
	names := make([]string, 0, len(pruning))
	for name := range pruning {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int { return columnOrder(a) - columnOrder(b) })
	for _, name := range names {
		entry := pruning[name]
		if !strings.HasPrefix(strings.ToUpper(entry.Type), "GEOMETRY") {
			entry.MaxFilesPerValue = maxOverlap(entry.Type, ranges[name])
		}
		l.Columns = append(l.Columns, *entry)
	}
}

// columnOrder puts the table's columns in their schema order, subject,
// predicate, then the object columns, and anything unknown after them.
func columnOrder(name string) int {
	order := []string{"subject", "predicate", "object_iri", "object_string", "object_language", "object_type", "object_integer", "object_byte", "object_float", "object_time", "object_geometry", "triple_hash"}
	if index := slices.Index(order, name); index >= 0 {
		return index
	}
	return len(order)
}

// boundLess compares two bounds as the column's type orders them: numbers
// as numbers and everything else as text, which is how DuckDB renders a
// timestamp bound, in an order that sorts.
func boundLess(columnType string, a, b string) bool {
	switch strings.ToUpper(columnType) {
	case "DOUBLE", "FLOAT", "BIGINT", "INTEGER", "SMALLINT", "TINYINT", "HUGEINT", "DECIMAL":
		x, errX := strconv.ParseFloat(a, 64)
		y, errY := strconv.ParseFloat(b, 64)
		if errX == nil && errY == nil {
			return x < y
		}
	}
	return a < b
}

// maxOverlap is the most files one value can fall in by their bounds: the
// largest number of ranges that share a point, found by sweeping their ends
// in order with a range's start counted before another's end at the same
// value, since the bounds are inclusive.
func maxOverlap(columnType string, ranges []columnBound) int {
	type end struct {
		value string
		start bool
	}
	ends := make([]end, 0, 2*len(ranges))
	for _, r := range ranges {
		ends = append(ends, end{value: r.lower, start: true}, end{value: r.upper, start: false})
	}
	sort.SliceStable(ends, func(i, j int) bool {
		if boundLess(columnType, ends[i].value, ends[j].value) {
			return true
		}
		if boundLess(columnType, ends[j].value, ends[i].value) {
			return false
		}
		return ends[i].start && !ends[j].start
	})
	open, most := 0, 0
	for _, e := range ends {
		if e.start {
			open++
			most = max(most, open)
		} else {
			open--
		}
	}
	return most
}
