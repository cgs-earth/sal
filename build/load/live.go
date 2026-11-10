package load

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/table"
)

// liveFiles are the files the current snapshot of a triples table reads: its
// data files and its equality delete files, each with the data sequence
// number that decides which rows a delete removes. complex is set when the
// snapshot has a kind of delete sal never writes, a position delete or a
// deletion vector, which liveRowsSQL cannot apply.
type liveFiles struct {
	data    []sequencedFile
	deletes []sequencedFile
	complex bool
}

type sequencedFile struct {
	path     string
	sequence int64
}

// currentFiles lists the files of the table's current snapshot from its
// manifests, which is metadata only: no data file is opened.
func currentFiles(ctx context.Context, tbl *table.Table) (liveFiles, error) {
	var files liveFiles
	snapshot := tbl.CurrentSnapshot()
	if snapshot == nil {
		return files, nil
	}
	fs, err := tbl.FS(ctx)
	if err != nil {
		return files, err
	}
	manifests, err := snapshot.Manifests(fs)
	if err != nil {
		return files, fmt.Errorf("read the manifests of the triples table: %w", err)
	}
	hashField, ok := tbl.Schema().FindFieldByName("triple_hash")
	if !ok {
		return files, fmt.Errorf("triple_hash field not found in table schema")
	}
	for _, manifest := range manifests {
		for entry, err := range manifest.Entries(fs, true) {
			if err != nil {
				return files, fmt.Errorf("read the manifests of the triples table: %w", err)
			}
			sequence := entry.DataSequenceNumber()
			if sequence < 0 {
				sequence = manifest.SequenceNumber()
			}
			file := sequencedFile{path: strings.TrimPrefix(entry.DataFile().FilePath(), "file://"), sequence: sequence}
			switch entry.DataFile().ContentType() {
			case iceberg.EntryContentData:
				files.data = append(files.data, file)
			case iceberg.EntryContentEqDeletes:
				if ids := entry.DataFile().EqualityFieldIDs(); len(ids) != 1 || ids[0] != hashField.ID {
					files.complex = true
				}
				files.deletes = append(files.deletes, file)
			default:
				files.complex = true
			}
		}
	}
	return files, nil
}

// liveRowsSQL is a DuckDB query over the rows the snapshot holds, as columns:
// every row of a data file that no equality delete with a higher sequence
// number names by triple_hash, which is the rule Iceberg reads equality
// deletes by, so a triple a later build added again after an earlier one
// deleted it is kept. It reads the Parquet files directly, so DuckDB applies
// the deletes as a join it can spill, where a scan through iceberg-go holds
// every delete of the table in memory. object_geometry is read as the WKB
// blob a reader of the column expects.
func (f liveFiles) liveRowsSQL(columns []string) string {
	selected := make([]string, len(columns))
	for i, column := range columns {
		selected[i] = "data." + column
		if column == "object_geometry" {
			selected[i] = "data.object_geometry::BLOB AS object_geometry"
		}
	}
	dataPaths := make([]string, len(f.data))
	for i, file := range f.data {
		dataPaths[i] = file.path
	}
	if len(f.deletes) == 0 {
		return fmt.Sprintf("SELECT %s FROM read_parquet(%s) data", strings.Join(selected, ", "), sqlList(dataPaths))
	}
	sequences := make([]string, 0, len(f.data)+len(f.deletes))
	deletePaths := make([]string, len(f.deletes))
	for _, file := range f.data {
		sequences = append(sequences, fmt.Sprintf("(%s, %d)", sqlString(file.path), file.sequence))
	}
	for i, file := range f.deletes {
		deletePaths[i] = file.path
		sequences = append(sequences, fmt.Sprintf("(%s, %d)", sqlString(file.path), file.sequence))
	}
	return fmt.Sprintf(`WITH sequences(filename, sequence) AS (VALUES %s),
deleted AS (
	SELECT deletes.triple_hash, max(sequences.sequence) AS sequence
	FROM read_parquet(%s, filename = true) deletes JOIN sequences USING (filename)
	GROUP BY deletes.triple_hash
)
SELECT %s
FROM read_parquet(%s, filename = true) data
JOIN sequences USING (filename)
LEFT JOIN deleted ON deleted.triple_hash = data.triple_hash
WHERE deleted.sequence IS NULL OR deleted.sequence <= sequences.sequence`,
		strings.Join(sequences, ", "), sqlList(deletePaths), strings.Join(selected, ", "), sqlList(dataPaths))
}

// ScanLiveRows hands fn the given columns of every row the table's current
// snapshot holds, in batches, and returns how many rows there were. The
// equality deletes are applied by DuckDB (see liveFiles.liveRowsSQL), which
// writes the rows to a file under scratch first, so that reading a table
// whose builds have deleted millions of rows costs no more memory than a
// table that never deleted one. A snapshot with deletes sal never writes is
// scanned through iceberg-go instead.
func ScanLiveRows(ctx context.Context, tbl *table.Table, scratch string, columns []string, fn func(arrow.RecordBatch) error) (rows int64, err error) {
	files, err := currentFiles(ctx, tbl)
	if err != nil {
		return 0, err
	}
	if files.complex {
		return scanThroughIceberg(ctx, tbl, columns, fn)
	}
	if len(files.data) == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(scratch, 0755); err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp(scratch, "scan-")
	if err != nil {
		return 0, err
	}
	db := &scratchDB{dir: dir}
	defer func() { err = joinClose(err, db.Close()) }()
	path := filepath.Join(dir, "rows.parquet")
	if _, err := db.copy(ctx, "COPY ("+files.liveRowsSQL(columns)+") TO "+sqlString(path)+" (FORMAT parquet, COMPRESSION snappy)"); err != nil {
		return 0, fmt.Errorf("read the triples table: %w", err)
	}
	if err := db.closeDB(); err != nil {
		return 0, err
	}
	records, closeRecords, err := readParquetFile(ctx, path)
	if err != nil {
		return 0, err
	}
	defer func() { err = joinClose(err, closeRecords()) }()
	for record, err := range records {
		if err != nil {
			return rows, err
		}
		rows += record.NumRows()
		err = fn(record)
		record.Release()
		if err != nil {
			return rows, err
		}
	}
	return rows, nil
}

// scanThroughIceberg is ScanLiveRows for a snapshot liveRowsSQL cannot read.
func scanThroughIceberg(ctx context.Context, tbl *table.Table, columns []string, fn func(arrow.RecordBatch) error) (int64, error) {
	_, records, err := tbl.Scan(table.WithSelectedFields(columns...), table.WithCaseSensitive(true)).ToArrowRecords(ctx)
	if err != nil {
		return 0, fmt.Errorf("scan the triples table: %w", err)
	}
	var rows int64
	for record, err := range records {
		if err != nil {
			return rows, fmt.Errorf("read the triples table: %w", err)
		}
		if record == nil {
			continue
		}
		rows += record.NumRows()
		err = fn(record)
		record.Release()
		if err != nil {
			return rows, err
		}
	}
	return rows, nil
}
