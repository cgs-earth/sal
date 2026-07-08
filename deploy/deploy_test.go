package deploy

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

func TestDeployUploadsAllFilesToBucket(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	destination := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "project", "triples", "metadata"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "project", "triples", "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "project", "triples", "metadata", "v1.metadata.json"), []byte(`{"location":"test"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "project", "triples", "data", "file.parquet"), []byte("parquet bytes"), 0644))

	err := deploy(ctx, dataDir, "file://"+destination, "", "", blob.OpenBucket)
	require.NoError(t, err)

	metadata, err := os.ReadFile(filepath.Join(destination, "project", "triples", "metadata", "v1.metadata.json"))
	require.NoError(t, err)
	require.Equal(t, []byte(`{"location":"test"}`), metadata)

	parquet, err := os.ReadFile(filepath.Join(destination, "project", "triples", "data", "file.parquet"))
	require.NoError(t, err)
	require.Equal(t, []byte("parquet bytes"), parquet)
}

func TestDeployReturnsErrorWhenDataDirIsEmpty(t *testing.T) {
	err := deploy(context.Background(), t.TempDir(), "mem://", "", "", blob.OpenBucket)
	require.ErrorContains(t, err, "no files found")
}

func TestDeployNormalizesGCSBucketURL(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "file.txt"), []byte("contents"), 0644))

	var openedURL string
	err := deploy(context.Background(), dataDir, "gcs://my-bucket?prefix=data/", "", "", func(_ context.Context, url string) (*blob.Bucket, error) {
		openedURL = url
		return nil, fmt.Errorf("stop after URL capture")
	})
	require.ErrorContains(t, err, "stop after URL capture")
	require.Equal(t, "gs://my-bucket?prefix=data/", openedURL)
}

func TestNormalizeBucketURLLeavesNativeGSSchemeAlone(t *testing.T) {
	require.Equal(t, "gs://my-bucket", normalizeBucketURL("gs://my-bucket"))
}

func TestApplyCredentialEnvironmentMapsS3Credentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "old-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "old-secret")

	restore, err := applyCredentialEnvironment("s3://my-bucket", "new-key", "new-secret")
	require.NoError(t, err)
	require.Equal(t, "new-key", os.Getenv("AWS_ACCESS_KEY_ID"))
	require.Equal(t, "new-secret", os.Getenv("AWS_SECRET_ACCESS_KEY"))

	restore()
	require.Equal(t, "old-key", os.Getenv("AWS_ACCESS_KEY_ID"))
	require.Equal(t, "old-secret", os.Getenv("AWS_SECRET_ACCESS_KEY"))
}

func TestApplyCredentialEnvironmentRequiresUsernameAndPasswordTogether(t *testing.T) {
	_, err := applyCredentialEnvironment("s3://my-bucket", "new-key", "")
	require.ErrorContains(t, err, "both --username and --password")
}
