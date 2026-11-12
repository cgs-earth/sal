package load

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

// A spool batches this many rows of a partition into one Arrow record, which
// is one Parquet row group, and starts a new file for the partition after
// this many rows, so that what it buffers in memory is a batch per partition
// whatever it is given.
const (
	spoolBatchRows = 8192
	spoolFileRows  = 4 << 20
)

// partitionKeyRunes is the width of the truncate transform the triples
// table is partitioned by, on the predicate.
const partitionKeyRunes = 20

// Spool is the set of rows a build commits, written to Parquet files in a
// scratch directory as the triples arrive rather than held in memory: what a
// SAL module task produces is handed to Add as it is parsed, and the graph of
// everything else the build gathers is added whole with AddGraph. Each row
// is a row of the triples table, triple_hash included, so the diff against
// the table is a set operation DuckDB runs over the files (see
// WriteSpoolToIceberg), which spills to disk rather than needing the build in
// memory. A spool may hold the same triple more than once; the diff keeps one.
//
// The rows are written to files per partition of the table, the first
// partitionKeyRunes characters of the predicate, so that the diff can work a
// partition at a time: only one partition is ever sorted, and its files are
// removed as soon as its data files are written, which keeps the disk a build
// needs near the size of the spool rather than several times it.
//
// The rows are also grouped into segments: every row added since the last
// Mark belongs to the current one. A staged build marks the spool when it is
// staged, so that what its CONSTRUCT queries add afterwards can be diffed on
// its own. A spool is not safe for concurrent use.
type Spool struct {
	dir        string
	schema     *arrow.Schema
	partitions map[string]*spoolPartition

	// files are the closed files, each with its segment and partition
	files    []spoolFile
	segment  int
	rows     int64
	sequence int
}

// spoolPartition is what a spool is writing for one partition of the table.
type spoolPartition struct {
	builder    *array.RecordBuilder
	writer     *pqarrow.FileWriter
	path       string
	fileRows   int64
	predicates map[string]bool
}

type spoolFile struct {
	path    string
	segment int
	key     string
}

// SpoolPartition is the files of one partition of the table a spool holds,
// with every predicate written to it.
type SpoolPartition struct {
	Key        string
	Predicates []string
	Files      []string
}

// NewSpool makes a spool writing its files to a new directory under dir,
// which Remove deletes again.
func NewSpool(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp(dir, "spool-")
	if err != nil {
		return nil, fmt.Errorf("make the spool directory: %w", err)
	}
	schema, _, err := GetSchemas()
	if err != nil {
		return nil, err
	}
	return &Spool{dir: scratch, schema: schema, partitions: map[string]*spoolPartition{}}, nil
}

// Dir is the directory the spool writes its files to.
func (s *Spool) Dir() string { return s.dir }

// Rows is how many rows have been added to the spool.
func (s *Spool) Rows() int64 { return s.rows }

// partitionKey is the partition of the table a predicate's rows are in.
func partitionKey(predicate string) string {
	runes := 0
	for i := range predicate {
		if runes == partitionKeyRunes {
			return predicate[:i]
		}
		runes++
	}
	return predicate
}

// Add writes one triple to the spool. A blank node is written with the label
// it has, so it must already be stable.
func (s *Spool) Add(triple rdflibgo.Triple) error {
	subject := storedSubject(triple.Subject)
	predicate := triple.Predicate.String()
	object := graphTripleObject(triple.Object)
	digest := digestTriple(subject, predicate, object.o, object.oDatatype, object.oLanguage)

	key := partitionKey(predicate)
	partition := s.partitions[key]
	if partition == nil {
		partition = &spoolPartition{builder: array.NewRecordBuilder(memory.DefaultAllocator, s.schema), predicates: map[string]bool{}}
		s.partitions[key] = partition
	}
	if !partition.predicates[predicate] {
		partition.predicates[strings.Clone(predicate)] = true
	}
	builder := partition.builder
	builder.Field(0).(*array.StringBuilder).Append(subject)
	builder.Field(1).(*array.StringBuilder).Append(predicate)
	if err := appendObjectFields(builder, object); err != nil {
		return fmt.Errorf("serialize object for %s %s: %w", triple.Subject.String(), predicate, err)
	}
	builder.Field(s.schema.NumFields() - 1).(*array.FixedSizeBinaryBuilder).Append(digest[:])
	s.rows++
	if builder.Field(0).Len() >= spoolBatchRows {
		return s.writeBatch(key, partition)
	}
	return nil
}

// AddGraph writes every triple of graph to the spool. Its blank nodes must
// already be stable, see StabilizeBlankNodes.
func (s *Spool) AddGraph(graph *rdflibgo.Graph) error {
	var err error
	pkg.Triples(graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		err = s.Add(triple)
		return err == nil
	})
	return err
}

// Mark closes the current segment: rows added from now on belong to the
// next one. It returns the segment that was closed.
func (s *Spool) Mark() (int, error) {
	if err := s.Flush(); err != nil {
		return 0, err
	}
	s.segment++
	return s.segment - 1, nil
}

// Flush writes what is buffered and closes the open files, so that every row
// added so far is in Files.
func (s *Spool) Flush() error {
	for key, partition := range s.partitions {
		if partition.builder.Field(0).Len() > 0 {
			if err := s.writeBatch(key, partition); err != nil {
				return err
			}
		}
		if err := partition.closeFile(); err != nil {
			return err
		}
	}
	return nil
}

// Files are the closed files of the segments from, through to, inclusive.
func (s *Spool) Files(from, to int) []string {
	var paths []string
	for _, file := range s.files {
		if file.segment >= from && file.segment <= to {
			paths = append(paths, file.path)
		}
	}
	return paths
}

// Partitions are the closed files of the segments from, through to,
// inclusive, by the partition of the table they hold rows of, sorted by key.
func (s *Spool) Partitions(from, to int) []SpoolPartition {
	byKey := map[string]*SpoolPartition{}
	var keys []string
	for _, file := range s.files {
		if file.segment < from || file.segment > to {
			continue
		}
		partition, ok := byKey[file.key]
		if !ok {
			partition = &SpoolPartition{Key: file.key}
			for predicate := range s.partitions[file.key].predicates {
				partition.Predicates = append(partition.Predicates, predicate)
			}
			sort.Strings(partition.Predicates)
			byKey[file.key] = partition
			keys = append(keys, file.key)
		}
		partition.Files = append(partition.Files, file.path)
	}
	sort.Strings(keys)
	partitions := make([]SpoolPartition, len(keys))
	for i, key := range keys {
		partitions[i] = *byKey[key]
	}
	return partitions
}

// Drop deletes closed files of the spool once what they hold is written
// where it is going, so that the disk a build uses shrinks as it commits.
func (s *Spool) Drop(paths []string) error {
	dropped := map[string]bool{}
	var err error
	for _, path := range paths {
		dropped[path] = true
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			err = joinClose(err, removeErr)
		}
	}
	s.files = slices.DeleteFunc(s.files, func(file spoolFile) bool { return dropped[file.path] })
	return err
}

// Segment is the segment rows are being added to.
func (s *Spool) Segment() int { return s.segment }

// Remove deletes the spool's files and its directory.
func (s *Spool) Remove() error {
	if s == nil {
		return nil
	}
	for _, partition := range s.partitions {
		partition.builder.Release()
		_ = partition.closeFile()
	}
	s.partitions = map[string]*spoolPartition{}
	return os.RemoveAll(s.dir)
}

// writeBatch writes a partition's buffered rows as one row group of its open
// file, opening one when none is, and closes the file once it is full.
func (s *Spool) writeBatch(key string, partition *spoolPartition) error {
	record := partition.builder.NewRecordBatch()
	defer record.Release()
	if partition.writer == nil {
		path := filepath.Join(s.dir, fmt.Sprintf("%06d.parquet", s.sequence))
		s.sequence++
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		writer, err := pqarrow.NewFileWriter(s.schema, file, spoolWriterProperties(), pqarrow.NewArrowWriterProperties(pqarrow.WithStoreSchema()))
		if err != nil {
			_ = file.Close()
			return err
		}
		partition.writer, partition.path = writer, path
		s.files = append(s.files, spoolFile{path: path, segment: s.segment, key: key})
	}
	if err := partition.writer.Write(record); err != nil {
		return fmt.Errorf("write the spool: %w", err)
	}
	partition.fileRows += record.NumRows()
	if partition.fileRows >= spoolFileRows {
		return partition.closeFile()
	}
	return nil
}

// closeFile closes the partition's open file, which closing the writer does.
func (p *spoolPartition) closeFile() error {
	if p.writer == nil {
		return nil
	}
	err := p.writer.Close()
	p.writer, p.path, p.fileRows = nil, "", 0
	if err != nil {
		return fmt.Errorf("close the spool file: %w", err)
	}
	return nil
}

// spoolDictionarylessColumns are the columns of the spool whose values are
// nearly all distinct: every row has its own hash, and subjects and object
// values repeat a handful of times at most. A dictionary of such a column
// holds every value once more and is then abandoned for plain encoding once
// it outgrows its page, so building it is pure cost on the build's one
// parsing goroutine.
var spoolDictionarylessColumns = []string{"subject", "object_string", "object_iri", "object_geometry", "triple_hash"}

// spoolWriterProperties is how scratch Parquet is written: fast to write and
// to read back, with no statistics nobody reads, and no dictionary for a
// column whose values hardly repeat. The predicate, datatype, and language
// columns keep theirs, since a few distinct values cover millions of rows.
func spoolWriterProperties() *parquet.WriterProperties {
	properties := []parquet.WriterProperty{
		parquet.WithCompression(compress.Codecs.Snappy),
		parquet.WithStats(false),
		parquet.WithMaxRowGroupLength(spoolBatchRows),
	}
	for _, column := range spoolDictionarylessColumns {
		properties = append(properties, parquet.WithDictionaryFor(column, false))
	}
	return parquet.NewWriterProperties(properties...)
}
