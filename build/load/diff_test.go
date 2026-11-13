package load

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/stretchr/testify/require"
)

// TestReadParquetFileAcceptsPagesLargerThanArrowsDefault reads a file whose
// one page is past arrow-go's 64 MiB default limit, the way DuckDB writes a
// partition of large geometries, which a Geoconnex build failed on.
func TestReadParquetFileAcceptsPagesLargerThanArrowsDefault(t *testing.T) {
	const values, valueBytes = 70, 1 << 20
	schema := arrow.NewSchema([]arrow.Field{{Name: "object_geometry", Type: arrow.BinaryTypes.Binary}}, nil)
	builder := array.NewBinaryBuilder(memory.DefaultAllocator, arrow.BinaryTypes.Binary)
	value := make([]byte, valueBytes)
	for range values {
		_, _ = rand.Read(value)
		builder.Append(value)
	}
	column := builder.NewArray()
	defer column.Release()
	record := array.NewRecordBatch(schema, []arrow.Array{column}, values)
	defer record.Release()

	path := filepath.Join(t.TempDir(), "added-1.parquet")
	out, err := os.Create(path)
	require.NoError(t, err)
	writer, err := pqarrow.NewFileWriter(schema, out, parquet.NewWriterProperties(
		parquet.WithDictionaryDefault(false),
		parquet.WithDataPageSize(values*valueBytes*2),
	), pqarrow.DefaultWriterProps())
	require.NoError(t, err)
	require.NoError(t, writer.Write(record))
	require.NoError(t, writer.Close())

	records, closeFile, err := readParquetFile(context.Background(), path)
	require.NoError(t, err)
	defer func() { require.NoError(t, closeFile()) }()
	rows := int64(0)
	for batch, err := range records {
		require.NoError(t, err)
		rows += batch.NumRows()
		batch.Release()
	}
	require.EqualValues(t, values, rows)
}
