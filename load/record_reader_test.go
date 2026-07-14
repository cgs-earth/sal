package load

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
)

func TestNQuadRecordReaderStreamsFiles(t *testing.T) {
	dir := t.TempDir()
	first := writeGzipNQuads(t, dir, "first.nq.gz", []string{
		`<http://example.com/s1> <http://example.com/p> "one" .`,
		`not valid`,
	})
	second := writeGzipNQuads(t, dir, "second.nq.gz", []string{
		`<http://example.com/s2> <http://example.com/p> "two" .`,
		`<http://example.com/s3> <http://example.com/p> "three" .`,
	})

	schema := arrow.NewSchema([]arrow.Field{
		{Name: "subject", Type: arrow.BinaryTypes.String},
		{Name: "predicate", Type: arrow.BinaryTypes.String},
		{Name: "object", Type: arrow.BinaryTypes.String},
	}, nil)

	rdr := newNQuadRecordReader([]string{first, second}, schema, 2)
	defer rdr.Release()

	var batches int
	var rows int64
	for rdr.Next() {
		batches++
		rows += rdr.RecordBatch().NumRows()
	}
	if err := rdr.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
	if batches != 2 {
		t.Fatalf("read %d batches, want 2", batches)
	}
	if rows != 3 {
		t.Fatalf("read %d rows, want 3", rows)
	}
	if rdr.RowsRead() != 3 {
		t.Fatalf("RowsRead() = %d, want 3", rdr.RowsRead())
	}
}

func writeGzipNQuads(t *testing.T, dir, name string, lines []string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create gzip fixture: %v", err)
	}
	gz := gzip.NewWriter(f)
	for _, line := range lines {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write gzip fixture: %v", err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close gzip fixture: %v", err)
	}
	return path
}
