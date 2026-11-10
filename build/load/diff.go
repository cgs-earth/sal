package load

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/compute"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	// the diff runs in a DuckDB database of its own, which reads and writes
	// nothing but Parquet and so needs no extension downloaded
	_ "github.com/duckdb/duckdb-go/v2"
	"go.opentelemetry.io/otel/attribute"
)

// readBatchRows is how many rows a Parquet file the diff wrote is read back
// in at a time.
const readBatchRows = 32768

// The diff's DuckDB gets at least diffMemoryFloor, and a thread per
// diffMemoryPerThread of its memory limit, up to one per core: a sort spills
// with 256 MiB a thread and runs out of memory with much less.
const (
	diffMemoryFloor     = 512 << 20
	diffMemoryPerThread = 256 << 20
)

// spoolDiff is the diff of a spool against the triples table, computed by
// DuckDB over Parquet files so that neither side is ever held in memory: the
// table's (triple_hash, predicate) pairs are written to a file of their own,
// and the rows to add and the hashes to delete are both anti joins, which
// DuckDB spills to disk under its memory_limit. The diff owns a scratch
// directory under the spool's, which Close removes.
type spoolDiff struct {
	*scratchDB
	// existing is the file of the table's (triple_hash, predicate) pairs, ""
	// for a table with no snapshot
	existing     string
	existingRows int64
	// addedHashes are files of the hashes earlier rounds of this diff added,
	// which a later round excludes
	addedHashes []string
	sequence    int
}

// openSpoolDiff makes the scratch directory a diff runs in and writes the
// table's current hashes out for it to read.
func openSpoolDiff(ctx context.Context, spool *Spool, tbl *table.Table) (_ *spoolDiff, err error) {
	dir, err := os.MkdirTemp(spool.Dir(), "diff-")
	if err != nil {
		return nil, err
	}
	diff := &spoolDiff{scratchDB: &scratchDB{dir: dir}}
	if err := diff.dumpExisting(ctx, tbl); err != nil {
		return nil, joinClose(err, diff.Close())
	}
	return diff, nil
}

// scratchDB is a DuckDB database for heavy work over Parquet files, with its
// memory limited to sal's share of the machine and anything beyond spilled to
// a directory of its own. It holds nothing but its settings, since every
// statement reads and writes files, so it is opened for a round of
// statements and closed before the Go side works on what they wrote, which
// hands DuckDB's memory back first. Close removes the directory.
type scratchDB struct {
	db  *sql.DB
	dir string
}

// open opens the database unless it is open.
func (d *scratchDB) open(ctx context.Context) error {
	if d.db != nil {
		return nil
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return fmt.Errorf("open duckdb: %w", err)
	}
	// the settings below are the database's, so every statement must run on
	// the one connection they were made on
	db.SetMaxOpenConns(1)
	settings := []string{
		"SET temp_directory = " + sqlString(filepath.Join(d.dir, "duckdb")),
		// rows are written sorted where order matters, so DuckDB is free to
		// skip keeping the order it read them in, which costs memory
		"SET preserve_insertion_order = false",
	}
	if limit := max(pkg.DuckDBMemoryLimit(), diffMemoryFloor); pkg.DuckDBMemoryLimit() > 0 {
		// every thread of a sort or a join holds blocks of its own, so a
		// small limit with many threads fails where fewer threads spill
		threads := min(runtime.NumCPU(), max(1, int(limit/diffMemoryPerThread)))
		settings = append(settings, fmt.Sprintf("SET memory_limit = '%dMB'", limit>>20), fmt.Sprintf("SET threads = %d", threads))
	}
	for _, setting := range settings {
		if _, err := db.ExecContext(ctx, setting); err != nil {
			return joinClose(fmt.Errorf("configure duckdb (%s): %w", setting, err), db.Close())
		}
	}
	d.db = db
	return nil
}

// copy runs a COPY statement and returns how many rows it wrote.
func (d *scratchDB) copy(ctx context.Context, statement string) (int64, error) {
	if err := d.open(ctx); err != nil {
		return 0, err
	}
	result, err := d.db.ExecContext(ctx, statement)
	if err != nil {
		return 0, fmt.Errorf("diff the build against the table: %w", err)
	}
	return result.RowsAffected()
}

// closeDB closes the database, which frees what DuckDB held, keeping the
// scratch files.
func (d *scratchDB) closeDB() error {
	if d.db == nil {
		return nil
	}
	err := d.db.Close()
	d.db = nil
	return err
}

// Close closes the database and removes the scratch files.
func (d *scratchDB) Close() error {
	if d == nil {
		return nil
	}
	return joinClose(d.closeDB(), os.RemoveAll(d.dir))
}

// dumpExisting writes the (triple_hash, predicate) pair of every row the
// table's current snapshot holds to a Parquet file, by a DuckDB query that
// applies the table's equality deletes (liveFiles.liveRowsSQL) rather than a
// scan, since a table of a billion rows is tens of gigabytes of hashes and
// iceberg-go holds every delete of the table in memory to apply them. An
// equality delete names a row by both.
func (d *spoolDiff) dumpExisting(ctx context.Context, tbl *table.Table) (err error) {
	if tbl.CurrentSnapshot() == nil {
		return nil
	}
	ctx, span := telemetry.Start(ctx, "iceberg.read_existing")
	defer func() { telemetry.End(span, err) }()
	slog.Info("Reading the triple hashes the Iceberg table already holds")
	defer func() { span.SetAttributes(attribute.Int64("sal.triples.existing", d.existingRows)) }()

	files, err := currentFiles(ctx, tbl)
	if err != nil {
		return err
	}
	if !files.complex {
		path := filepath.Join(d.dir, "existing.parquet")
		if d.existingRows, err = d.copy(ctx, "COPY ("+files.liveRowsSQL([]string{"triple_hash", "predicate"})+") TO "+sqlString(path)+" (FORMAT parquet, COMPRESSION snappy)"); err != nil {
			return err
		}
		if d.existingRows > 0 {
			d.existing = path
		}
		return nil
	}

	// a snapshot with deletes sal never writes is read through iceberg-go,
	// which applies them
	_, records, err := tbl.Scan(
		table.WithSelectedFields("triple_hash", "predicate"),
		table.WithCaseSensitive(true),
	).ToArrowRecords(ctx)
	if err != nil {
		return fmt.Errorf("scan existing triple hashes: %w", err)
	}
	path := filepath.Join(d.dir, "existing.parquet")
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	// the writer takes its schema from the first record, since the scan
	// decides how it represents a string column; closing it closes the file
	var writer *pqarrow.FileWriter
	defer func() {
		if writer != nil {
			err = joinClose(err, writer.Close())
		} else if out != nil {
			err = joinClose(err, out.Close())
		}
	}()
	for record, err := range records {
		if err != nil {
			return fmt.Errorf("read existing triple hashes: %w", err)
		}
		hashes := record.Column(record.Schema().FieldIndices("triple_hash")[0])
		predicates := record.Column(record.Schema().FieldIndices("predicate")[0])
		schema := arrow.NewSchema([]arrow.Field{
			{Name: "triple_hash", Type: hashes.DataType()},
			{Name: "predicate", Type: predicates.DataType()},
		}, nil)
		if writer == nil {
			if writer, err = pqarrow.NewFileWriter(schema, out, spoolWriterProperties(), pqarrow.DefaultWriterProps()); err != nil {
				record.Release()
				return err
			}
		}
		pair := array.NewRecordBatch(schema, []arrow.Array{hashes, predicates}, record.NumRows())
		err = writer.Write(pair)
		pair.Release()
		d.existingRows += record.NumRows()
		record.Release()
		if err != nil {
			return fmt.Errorf("write existing triple hashes: %w", err)
		}
	}
	if writer == nil {
		// a snapshot whose rows were all deleted: nothing to diff against
		err = out.Close()
		out = nil
		return err
	}
	err = writer.Close()
	writer, out = nil, nil
	if err != nil {
		return err
	}
	d.existing = path
	return nil
}

// added writes to a new Parquet file every row of one partition of the
// spool whose triple neither the table nor any of the exclude files holds,
// sorted by predicate and hash, which keeps the partitions of the table the
// rows fall in apart for the Iceberg writer, and puts a triple the spool holds
// more than once on adjacent rows, since the hash covers the predicate;
// writeRowFiles keeps the first. Leaving that to the writer is what keeps the
// query to anti joins and a sort, all of which DuckDB spills to disk, where a
// DISTINCT over whole rows fails once they no longer fit in memory. Only the
// table's rows with the partition's predicates are joined against. It
// returns the file and how many rows it holds, duplicates included.
func (d *spoolDiff) added(ctx context.Context, partition SpoolPartition, exclude []string) (string, int64, error) {
	if len(partition.Files) == 0 {
		return "", 0, nil
	}
	var anti []string
	if d.existing != "" {
		predicates := make([]string, len(partition.Predicates))
		for i, predicate := range partition.Predicates {
			predicates[i] = sqlString(predicate)
		}
		anti = append(anti, "ANTI JOIN (SELECT triple_hash FROM read_parquet("+sqlString(d.existing)+") WHERE predicate IN ("+strings.Join(predicates, ", ")+")) existing USING (triple_hash)")
	}
	if len(exclude) > 0 {
		anti = append(anti, "ANTI JOIN read_parquet("+sqlList(exclude)+") excluded USING (triple_hash)")
	}
	path := d.next("added")
	statement := fmt.Sprintf(`COPY (
	SELECT candidate.*
	FROM read_parquet(%s) candidate
	%s
	ORDER BY candidate.predicate, candidate.triple_hash
) TO %s (FORMAT parquet, COMPRESSION snappy)`, sqlList(partition.Files), strings.Join(anti, "\n\t"), sqlString(path))
	rows, err := d.copy(ctx, statement)
	return path, rows, err
}

// removed writes to a new Parquet file the (triple_hash, predicate) pair of
// every row of from, a file of such pairs, that none of the candidate files
// holds, sorted by predicate and hash, and returns it with how many rows it
// holds. There is nothing to remove from "". The equality delete writer keeps
// a writer open per partition, each with a queue of batches, so pairs that
// arrive a partition at a time keep only one of them busy and buffering.
func (d *spoolDiff) removed(ctx context.Context, from string, candidates []string) (string, int64, error) {
	if from == "" {
		return "", 0, nil
	}
	path := d.next("removed")
	anti := ""
	if len(candidates) > 0 {
		anti = "ANTI JOIN read_parquet(" + sqlList(candidates) + ") candidate USING (triple_hash)"
	}
	statement := fmt.Sprintf(`COPY (
	SELECT pairs.triple_hash, pairs.predicate
	FROM read_parquet(%s) pairs
	%s
	ORDER BY pairs.predicate, pairs.triple_hash
) TO %s (FORMAT parquet, COMPRESSION snappy)`, sqlString(from), anti, sqlString(path))
	rows, err := d.copy(ctx, statement)
	return path, rows, err
}

func (d *spoolDiff) next(name string) string {
	d.sequence++
	return filepath.Join(d.dir, fmt.Sprintf("%s-%d.parquet", name, d.sequence))
}

// writeRowFiles writes the rows of a Parquet file added wrote, which are
// sorted by predicate and hash, to the table's data files, one partition at a
// time, keeping one row of each run of rows with the same hash. It returns
// the files and how many rows they hold.
func writeRowFiles(ctx context.Context, tbl *table.Table, path string) (_ []iceberg.DataFile, rows int64, err error) {
	if path == "" {
		return nil, 0, nil
	}
	ctx, span := telemetry.Start(ctx, "iceberg.write_data_files")
	defer func() { telemetry.End(span, err) }()
	schema, _, err := GetSchemas()
	if err != nil {
		return nil, 0, err
	}
	var read int64
	records, closeRecords, err := readParquetRecords(ctx, path, schema, &read)
	if err != nil {
		return nil, 0, err
	}
	defer func() { err = joinClose(err, closeRecords()) }()

	var dataFiles []iceberg.DataFile
	for dataFile, err := range table.WriteRecords(ctx, tbl, schema, distinctRows(ctx, records, schema.NumFields()-1, &rows), table.WithClusteredWrite()) {
		if err != nil {
			return nil, 0, fmt.Errorf("write graph: %w", err)
		}
		dataFiles = append(dataFiles, dataFile)
	}
	span.SetAttributes(attribute.Int("sal.iceberg.data_files", len(dataFiles)), attribute.Int64("sal.triples.written", rows))
	slog.Info(fmt.Sprintf("Wrote %d triples to %d Iceberg data files", rows, len(dataFiles)))
	return dataFiles, rows, nil
}

// distinctRows passes on the batches of records with every row whose string
// column at hashColumn repeats the row before it, in the same batch or at the
// end of the last, filtered out, and counts the rows it passes on.
func distinctRows(ctx context.Context, records func(func(arrow.RecordBatch, error) bool), hashColumn int, rows *int64) func(func(arrow.RecordBatch, error) bool) {
	return func(yield func(arrow.RecordBatch, error) bool) {
		previous := ""
		for batch, err := range records {
			if err != nil {
				yield(nil, err)
				return
			}
			hashes := batch.Column(hashColumn).(*array.String)
			var keep *array.BooleanBuilder
			for i := 0; i < hashes.Len(); i++ {
				repeated := hashes.Value(i) == previous
				if repeated && keep == nil {
					// a batch with a repeat is the rare one, so the mask is
					// only built once one is found
					keep = array.NewBooleanBuilder(memory.DefaultAllocator)
					for range i {
						keep.Append(true)
					}
				}
				if keep != nil {
					keep.Append(!repeated)
				}
				if !repeated {
					previous = strings.Clone(hashes.Value(i))
				}
			}
			if keep != nil {
				mask := keep.NewBooleanArray()
				keep.Release()
				filtered, err := compute.FilterRecordBatch(ctx, batch, mask, compute.DefaultFilterOptions())
				mask.Release()
				batch.Release()
				if err != nil {
					yield(nil, fmt.Errorf("drop repeated triples: %w", err))
					return
				}
				batch = filtered
			}
			*rows += batch.NumRows()
			if !yield(batch, nil) {
				return
			}
		}
	}
}

// readParquetRecords streams a Parquet file as record batches of schema,
// matching its columns to the schema's by position: DuckDB writes the WKB of
// object_geometry as a plain blob, which is given back its geoarrow type, and
// every column it writes as nullable, which the schema says is not. rows
// counts the rows read.
func readParquetRecords(ctx context.Context, path string, schema *arrow.Schema, rows *int64) (func(func(arrow.RecordBatch, error) bool), func() error, error) {
	batches, closeFile, err := readParquetFile(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	records := func(yield func(arrow.RecordBatch, error) bool) {
		for record, err := range batches {
			if err != nil {
				yield(nil, err)
				return
			}
			columns := make([]arrow.Array, record.NumCols())
			for i := range columns {
				columns[i] = asSchemaColumn(record.Column(i), schema.Field(i).Type)
			}
			batch := array.NewRecordBatch(schema, columns, record.NumRows())
			for _, column := range columns {
				column.Release()
			}
			record.Release()
			*rows += batch.NumRows()
			if !yield(batch, nil) {
				return
			}
		}
	}
	return records, closeFile, nil
}

// readParquetFile streams a Parquet file as the record batches it holds, each
// one the caller's to release.
func readParquetFile(ctx context.Context, path string) (func(func(arrow.RecordBatch, error) bool), func() error, error) {
	reader, err := file.OpenParquetFile(path, false)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	arrowReader, err := pqarrow.NewFileReader(reader, pqarrow.ArrowReadProperties{BatchSize: readBatchRows}, memory.DefaultAllocator)
	if err != nil {
		_ = reader.Close()
		return nil, nil, err
	}
	recordReader, err := arrowReader.GetRecordReader(ctx, nil, nil)
	if err != nil {
		_ = reader.Close()
		return nil, nil, err
	}
	closeAll := func() error {
		recordReader.Release()
		return reader.Close()
	}
	records := func(yield func(arrow.RecordBatch, error) bool) {
		for recordReader.Next() {
			record := recordReader.RecordBatch()
			record.Retain()
			if !yield(record, nil) {
				return
			}
		}
		if err := recordReader.Err(); err != nil {
			yield(nil, fmt.Errorf("read %s: %w", path, err))
		}
	}
	return records, closeAll, nil
}

// asSchemaColumn returns column, retained, as the type the schema gives it:
// an extension column, such as the geoarrow WKB of object_geometry, is the
// extension over the column's storage, whether the file stored it as the
// extension or, as DuckDB writes it, as a plain blob.
func asSchemaColumn(column arrow.Array, want arrow.DataType) arrow.Array {
	extension, ok := want.(arrow.ExtensionType)
	if !ok || arrow.TypeEqual(column.DataType(), extension) {
		column.Retain()
		return column
	}
	if stored, ok := column.(array.ExtensionArray); ok {
		column = stored.Storage()
	}
	return array.NewExtensionArrayWithStorage(extension, column)
}

// deleteSchema is the schema of the pairs an equality delete names a row by.
func deleteSchema() *arrow.Schema {
	return arrow.NewSchema([]arrow.Field{
		{Name: "triple_hash", Type: arrow.BinaryTypes.String, Nullable: false},
		{Name: "predicate", Type: arrow.BinaryTypes.String, Nullable: false},
	}, nil)
}

// sqlString quotes a value as a SQL string literal.
func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// sqlList is a DuckDB list of string literals, which read_parquet takes for
// several files.
func sqlList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = sqlString(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// joinClose keeps the first error of an operation and of the cleanup after it.
func joinClose(err error, closeErr error) error {
	if err != nil {
		return err
	}
	return closeErr
}
