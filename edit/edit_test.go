package edit

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/twmb/avro"
	"github.com/twmb/avro/ocf"
)

func TestRewriteStringReplacesRootAndChildren(t *testing.T) {
	rewrite := rootRewriter{
		oldRoot: "/tmp/warehouse/project/triples",
		newRoot: "s3://my_test_bucket",
	}

	rewritten, changed := rewrite.rewriteString("/tmp/warehouse/project/triples/data/file.parquet")
	require.True(t, changed)
	require.Equal(t, "s3://my_test_bucket/data/file.parquet", rewritten)

	rewritten, changed = rewrite.rewriteString("/tmp/warehouse/project/triples")
	require.True(t, changed)
	require.Equal(t, "s3://my_test_bucket", rewritten)
}

func TestRewriteStringEscapesPercentSignsForHTTPRoots(t *testing.T) {
	rewrite := rootRewriter{
		oldRoot:       "/tmp/warehouse/project/triples",
		newRoot:       "https://storage.googleapis.com/my_test_bucket/sal/triples",
		escapeURIPath: true,
	}

	rewritten, changed := rewrite.rewriteString("/tmp/warehouse/project/triples/data/predicate_partition=http%3A%2F%2Fschema.org%2Fte/file.parquet")

	require.True(t, changed)
	require.Equal(t, "https://storage.googleapis.com/my_test_bucket/sal/triples/data/predicate_partition=http%253A%252F%252Fschema.org%252Fte/file.parquet", rewritten)
}

func TestRewriteStringDoesNotEscapePercentSignsForGCSRoots(t *testing.T) {
	rewrite := rootRewriter{
		oldRoot: "/tmp/warehouse/project/triples",
		newRoot: "gs://my_test_bucket/sal/triples",
	}

	rewritten, changed := rewrite.rewriteString("/tmp/warehouse/project/triples/data/predicate_partition=http%3A%2F%2Fschema.org%2Fte/file.parquet")

	require.True(t, changed)
	require.Equal(t, "gs://my_test_bucket/sal/triples/data/predicate_partition=http%3A%2F%2Fschema.org%2Fte/file.parquet", rewritten)
}

func TestRewriteStringDoesNotRewritePartialPrefix(t *testing.T) {
	rewrite := rootRewriter{
		oldRoot: "/tmp/warehouse/project/triples",
		newRoot: "s3://my_test_bucket",
	}

	rewritten, changed := rewrite.rewriteString("/tmp/warehouse/project/triples_backup/data/file.parquet")
	require.False(t, changed)
	require.Equal(t, "/tmp/warehouse/project/triples_backup/data/file.parquet", rewritten)
}

func TestRewriteJSONFileUpdatesNestedPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.metadata.json")
	original := map[string]any{
		"location":            "/tmp/warehouse/project/triples",
		"current-snapshot-id": json.Number("5012377253338815300"),
		"snapshots": []any{
			map[string]any{
				"snapshot-id":   json.Number("5012377253338815300"),
				"manifest-list": "/tmp/warehouse/project/triples/metadata/snap.avro",
			},
		},
	}
	b, err := json.Marshal(original)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0644))

	changed, err := rewriteJSONFile(path, rootRewriter{
		oldRoot: "/tmp/warehouse/project/triples",
		newRoot: "s3://my_test_bucket",
	})
	require.NoError(t, err)
	require.True(t, changed)

	var rewritten map[string]any
	b, err = os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, decodeJSON(b, &rewritten))
	require.Equal(t, "s3://my_test_bucket", rewritten["location"])
	require.Equal(t, json.Number("5012377253338815300"), rewritten["current-snapshot-id"])

	snapshots := rewritten["snapshots"].([]any)
	require.Equal(t, "s3://my_test_bucket/metadata/snap.avro", snapshots[0].(map[string]any)["manifest-list"])
	require.Equal(t, json.Number("5012377253338815300"), snapshots[0].(map[string]any)["snapshot-id"])
}

func TestRewriteAvroFileUpdatesNestedPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.avro")
	schema, err := avro.Parse(`{
		"type": "record",
		"name": "manifest_entry",
		"fields": [
			{"name": "manifest_path", "type": "string"},
			{"name": "data_file", "type": {
				"type": "record",
				"name": "data_file",
				"fields": [
					{"name": "file_path", "type": "string"},
					{"name": "record_count", "type": "long"}
				]
			}}
		]
	}`)
	require.NoError(t, err)

	var buf bytes.Buffer
	writer, err := ocf.NewWriter(&buf, schema, ocf.WithCodec(ocf.DeflateCodec(-1)))
	require.NoError(t, err)
	require.NoError(t, writer.Encode(map[string]any{
		"manifest_path": "/tmp/warehouse/project/triples/metadata/m0.avro",
		"data_file": map[string]any{
			"file_path":    "/tmp/warehouse/project/triples/data/file.parquet",
			"record_count": int64(10),
		},
	}))
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0644))

	changed, err := rewriteAvroFile(path, rootRewriter{
		oldRoot: "/tmp/warehouse/project/triples",
		newRoot: "s3://my_test_bucket",
	})
	require.NoError(t, err)
	require.True(t, changed)

	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()

	reader, err := ocf.NewReader(file)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Close()) }()

	var record map[string]any
	require.NoError(t, reader.Decode(&record))
	require.Equal(t, "s3://my_test_bucket/metadata/m0.avro", record["manifest_path"])

	dataFile := record["data_file"].(map[string]any)
	require.Equal(t, "s3://my_test_bucket/data/file.parquet", dataFile["file_path"])
	require.ErrorIs(t, reader.Decode(&record), io.EOF)
}

func TestRewriteIcebergTableRootLeavesParquetFilesUntouched(t *testing.T) {
	tablePath := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(tablePath, "metadata"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(tablePath, "data"), 0755))

	require.NoError(t, os.WriteFile(filepath.Join(tablePath, "data", "file.parquet"), []byte("parquet bytes"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"), []byte(`{
		"location": "`+filepath.ToSlash(tablePath)+`",
		"snapshots": [{"manifest-list": "`+filepath.ToSlash(filepath.Join(tablePath, "metadata", "snap.avro"))+`"}]
	}`), 0644))

	changedFiles, err := RewriteIcebergTableRoot(tablePath, "s3://my_test_bucket")
	require.NoError(t, err)
	require.Equal(t, 1, changedFiles)

	parquetBytes, err := os.ReadFile(filepath.Join(tablePath, "data", "file.parquet"))
	require.NoError(t, err)
	require.Equal(t, []byte("parquet bytes"), parquetBytes)
}
