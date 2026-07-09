package upload

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob"
)

func TestFilesToDeployPreservesRelativeKeys(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "project", "triples", "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "project", "triples", "metadata", "v1.metadata.json"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "project", "triples", "data.parquet"), []byte("parquet"), 0644))

	files, err := filesToDeploy(dataDir)
	require.NoError(t, err)
	require.Len(t, files, 2)

	keys := []string{files[0].key, files[1].key}
	require.Contains(t, keys, "project/triples/metadata/v1.metadata.json")
	require.Contains(t, keys, "project/triples/data.parquet")
}

func TestDeployUploadPhasesPublishMetadataAfterDataAndVersionHintLast(t *testing.T) {
	files := []deployFile{
		{key: "sal/triples/metadata/version-hint.text"},
		{key: "sal/triples/metadata/v1.metadata.json"},
		{key: "sal/triples/data/file.parquet"},
		{key: "README.md"},
		{key: "sal/triples/metadata/manifest.avro"},
	}

	phases := deployUploadPhases(files)
	require.Len(t, phases, 3)

	require.ElementsMatch(t, []deployFile{
		{key: "sal/triples/data/file.parquet"},
		{key: "README.md"},
	}, phases[0])
	require.ElementsMatch(t, []deployFile{
		{key: "sal/triples/metadata/v1.metadata.json"},
		{key: "sal/triples/metadata/manifest.avro"},
	}, phases[1])
	require.Equal(t, []deployFile{{key: "sal/triples/metadata/version-hint.text"}}, phases[2])
}

func TestDeployUploadsAllFilesToBucket(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	destination := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "project", "triples", "metadata"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "project", "triples", "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "project", "triples", "metadata", "v1.metadata.json"), []byte(`{"location":"`+filepath.ToSlash(filepath.Join(dataDir, "project", "triples"))+`"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "project", "triples", "data", "file.parquet"), []byte("parquet bytes"), 0644))

	err := deploy(ctx, dataDir, "file://"+destination, blob.OpenBucket)
	require.NoError(t, err)

	metadata, err := os.ReadFile(filepath.Join(destination, "metadata", "v1.metadata.json"))
	require.NoError(t, err)
	require.Contains(t, string(metadata), `"location": "file://`+filepath.ToSlash(destination)+`"`)

	parquet, err := os.ReadFile(filepath.Join(destination, "data", "file.parquet"))
	require.NoError(t, err)
	require.Equal(t, []byte("parquet bytes"), parquet)
}

func TestDeployReturnsErrorWhenDataDirIsEmpty(t *testing.T) {
	err := deploy(context.Background(), t.TempDir(), "mem://", blob.OpenBucket)
	require.ErrorContains(t, err, "no files found")
}

func TestDeployNormalizesGCSBucketURL(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "file.txt"), []byte("contents"), 0644))

	var openedURL string
	err := deploy(context.Background(), dataDir, "gs://my-bucket?prefix=data/", func(_ context.Context, url string) (*blob.Bucket, error) {
		openedURL = url
		return nil, fmt.Errorf("stop after URL capture")
	})
	require.ErrorContains(t, err, "stop after URL capture")
	require.Equal(t, "gs://my-bucket?prefix=data/", openedURL)
}

func TestDeployConvertsGCSPathToUploadPrefix(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "file.txt"), []byte("contents"), 0644))

	var openedURL string
	err := deploy(context.Background(), dataDir, "gs://my-bucket/sal/", func(_ context.Context, url string) (*blob.Bucket, error) {
		openedURL = url
		return nil, fmt.Errorf("stop after URL capture")
	})
	require.ErrorContains(t, err, "stop after URL capture")
	require.Equal(t, "gs://my-bucket?prefix=sal%2F", openedURL)
}

func TestDeployConvertsStorageGoogleapisURLToGCSUploadURL(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "file.txt"), []byte("contents"), 0644))

	var openedURL string
	err := deploy(context.Background(), dataDir, "https://storage.googleapis.com/my-bucket/sal/", func(_ context.Context, url string) (*blob.Bucket, error) {
		openedURL = url
		return nil, fmt.Errorf("stop after URL capture")
	})
	require.ErrorContains(t, err, "stop after URL capture")
	require.Equal(t, "gs://my-bucket?prefix=sal%2F", openedURL)
}

func TestObjectBaseURLIncludesPathAndPrefix(t *testing.T) {
	require.Equal(t, "gs://my-bucket/sal/project/triples", joinRemote(objectBaseURL("gs://my-bucket/sal/"), "project/triples"))
	require.Equal(t, "gs://my-bucket/sal/project/triples", joinRemote(objectBaseURL("gs://my-bucket?prefix=sal/"), "project/triples"))
	require.Equal(t, "https://storage.googleapis.com/my-bucket/sal/project/triples", joinRemote(objectBaseURL("https://storage.googleapis.com/my-bucket/sal/"), "project/triples"))
}

func TestRewriteStagedIcebergRootsUsesExactBucketURLForSingleTable(t *testing.T) {
	dataDir := t.TempDir()
	tablePath := filepath.Join(dataDir, "sal", "triples")
	require.NoError(t, os.MkdirAll(filepath.Join(tablePath, "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"), []byte(`{
		"location": "`+filepath.ToSlash(tablePath)+`",
		"snapshots": []
	}`), 0644))

	require.NoError(t, rewriteStagedIcebergRoots(dataDir, "gs://sal-test-bucket/sal/triples"))

	metadata, err := os.ReadFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"))
	require.NoError(t, err)
	require.Contains(t, string(metadata), `"location": "gs://sal-test-bucket/sal/triples"`)
}

func TestRewriteStagedIcebergRootsPreservesTablePathForBareBucket(t *testing.T) {
	dataDir := t.TempDir()
	tablePath := filepath.Join(dataDir, "sal", "triples")
	require.NoError(t, os.MkdirAll(filepath.Join(tablePath, "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"), []byte(`{
		"location": "`+filepath.ToSlash(tablePath)+`",
		"snapshots": []
	}`), 0644))

	require.NoError(t, rewriteStagedIcebergRoots(dataDir, "gs://sal-test-bucket/"))

	metadata, err := os.ReadFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"))
	require.NoError(t, err)
	require.Contains(t, string(metadata), `"location": "gs://sal-test-bucket/sal/triples"`)
}

func TestRewriteStagedIcebergRootsUsesStorageGoogleapisRemotePaths(t *testing.T) {
	dataDir := t.TempDir()
	tablePath := filepath.Join(dataDir, "sal", "triples")
	require.NoError(t, os.MkdirAll(filepath.Join(tablePath, "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"), []byte(`{
		"location": "`+filepath.ToSlash(tablePath)+`",
		"snapshots": []
	}`), 0644))

	require.NoError(t, rewriteStagedIcebergRoots(dataDir, "https://storage.googleapis.com/sal-test-bucket/"))

	metadata, err := os.ReadFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"))
	require.NoError(t, err)
	require.Contains(t, string(metadata), `"location": "https://storage.googleapis.com/sal-test-bucket/sal/triples"`)
}

func TestDeployUploadRootUsesSingleIcebergTableDirectoryForExplicitTableRoot(t *testing.T) {
	dataDir := t.TempDir()
	tablePath := filepath.Join(dataDir, "sal", "triples")
	require.NoError(t, os.MkdirAll(filepath.Join(tablePath, "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"), []byte("{}"), 0644))

	uploadRoot, err := deployUploadRoot(dataDir, "gs://my-bucket/sal/triples")
	require.NoError(t, err)
	require.Equal(t, tablePath, uploadRoot)
}

func TestDeployUploadRootUsesSingleIcebergTableDirectoryForExplicitStorageGoogleapisRoot(t *testing.T) {
	dataDir := t.TempDir()
	tablePath := filepath.Join(dataDir, "sal", "triples")
	require.NoError(t, os.MkdirAll(filepath.Join(tablePath, "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"), []byte("{}"), 0644))

	uploadRoot, err := deployUploadRoot(dataDir, "https://storage.googleapis.com/my-bucket/sal/triples")
	require.NoError(t, err)
	require.Equal(t, tablePath, uploadRoot)
}

func TestDeployUploadRootPreservesLayoutForBareBucket(t *testing.T) {
	dataDir := t.TempDir()
	tablePath := filepath.Join(dataDir, "sal", "triples")
	require.NoError(t, os.MkdirAll(filepath.Join(tablePath, "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"), []byte("{}"), 0644))

	uploadRoot, err := deployUploadRoot(dataDir, "gs://my-bucket/")
	require.NoError(t, err)
	require.Equal(t, dataDir, uploadRoot)
}

func TestDeployUploadRootPreservesLayoutForBareStorageGoogleapisBucket(t *testing.T) {
	dataDir := t.TempDir()
	tablePath := filepath.Join(dataDir, "sal", "triples")
	require.NoError(t, os.MkdirAll(filepath.Join(tablePath, "metadata"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tablePath, "metadata", "v1.metadata.json"), []byte("{}"), 0644))

	uploadRoot, err := deployUploadRoot(dataDir, "https://storage.googleapis.com/my-bucket/")
	require.NoError(t, err)
	require.Equal(t, dataDir, uploadRoot)
}
