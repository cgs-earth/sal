package upload

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/build/load"
	"github.com/cgs-earth/sal/pkg"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"
)

// builtDataDir builds a fresh project's .sal/data holding one triple about
// subject, with a blob named after it so each layout has a file of its own
// outside the table.
func builtDataDir(t *testing.T, subject string) string {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), ".sal", "data")
	graph := rdflibgo.NewGraph()
	pkg.AddTriple(graph, rdflibgo.NewURIRefUnsafe("https://example.com/"+subject), rdflibgo.NewURIRefUnsafe("https://example.com/vocab/name"), rdflibgo.NewLiteral(subject))
	cfg := &load.LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: dataDir, Namespace: "proj"}
	require.NoError(t, load.WriteGraphToIceberg(context.Background(), graph, cfg, map[string]string{"sal.hash": subject}, ""))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "blobs"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "blobs", subject), []byte(subject), 0644))
	return dataDir
}

// localBucket is a bucket held in a directory, and an opener for it that
// ignores the URL deploy opens, so a gs:// destination is uploaded to disk.
func localBucket(t *testing.T) (string, bucketOpener) {
	t.Helper()
	dir := t.TempDir()
	return dir, func(context.Context, string) (*blob.Bucket, error) {
		return fileblob.OpenBucket(dir, &fileblob.Options{Metadata: fileblob.MetadataDontWrite})
	}
}

// bucketKeys lists every object in a local bucket by its key.
func bucketKeys(t *testing.T, dir string) []string {
	t.Helper()
	var keys []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		keys = append(keys, filepath.ToSlash(rel))
		return err
	}))
	return keys
}

// uploadedKeys is what an upload of dataDir alone leaves in an empty bucket.
func uploadedKeys(t *testing.T, dataDir string) []string {
	t.Helper()
	dir, opener := localBucket(t)
	require.NoError(t, deploy(context.Background(), dataDir, "gs://my-bucket/", opener, deployOptions{}))
	return bucketKeys(t, dir)
}

func TestDeployWithDeleteLeavesExactlyTheNewLayout(t *testing.T) {
	ctx := context.Background()
	dir, opener := localBucket(t)
	require.NoError(t, deploy(ctx, builtDataDir(t, "A"), "gs://my-bucket/", opener, deployOptions{}))

	layoutB := builtDataDir(t, "B")
	require.NoError(t, deploy(ctx, layoutB, "gs://my-bucket/", opener, deployOptions{Delete: true}))

	keys := bucketKeys(t, dir)
	require.Contains(t, keys, "blobs/B")
	require.NotContains(t, keys, "blobs/A")
	// the metadata object names are fresh on every upload, so compare the
	// shape rather than the names: the same number of objects in each place
	require.Len(t, keys, len(uploadedKeys(t, layoutB)))
}

// Without --delete an upload only adds and overwrites, so what an earlier
// upload wrote stays in the bucket.
func TestDeployWithoutDeleteLeavesEarlierObjectsInPlace(t *testing.T) {
	ctx := context.Background()
	dir, opener := localBucket(t)
	layoutA := builtDataDir(t, "A")
	require.NoError(t, deploy(ctx, layoutA, "gs://my-bucket/", opener, deployOptions{}))
	keysA := bucketKeys(t, dir)

	require.NoError(t, deploy(ctx, builtDataDir(t, "B"), "gs://my-bucket/", opener, deployOptions{}))

	keys := bucketKeys(t, dir)
	require.Contains(t, keys, "blobs/A")
	require.Contains(t, keys, "blobs/B")
	require.Subset(t, keys, keysA)
}

func TestDeployDryRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	dir, opener := localBucket(t)
	require.NoError(t, deploy(ctx, builtDataDir(t, "A"), "gs://my-bucket/", opener, deployOptions{}))
	before := bucketKeys(t, dir)

	var out bytes.Buffer
	require.NoError(t, deploy(ctx, builtDataDir(t, "B"), "gs://my-bucket/", opener, deployOptions{Delete: true, DryRun: true, Out: &out}))

	require.Equal(t, before, bucketKeys(t, dir))
	require.Contains(t, out.String(), "upload gs://my-bucket/blobs/B\n")
	require.Contains(t, out.String(), "delete gs://my-bucket/blobs/A\n")
	require.Contains(t, out.String(), "upload gs://my-bucket/proj/triples/metadata/version-hint.text\n")
}

// An upload that fails partway deletes nothing, so the objects the previous
// table is read from are still there.
func TestDeployDeletesNothingWhenTheUploadFails(t *testing.T) {
	ctx := context.Background()
	dir, opener := localBucket(t)
	require.NoError(t, deploy(ctx, builtDataDir(t, "A"), "gs://my-bucket/", opener, deployOptions{}))
	// a file where the new layout needs a directory makes every upload under
	// it fail
	require.NoError(t, os.WriteFile(filepath.Join(dir, "blocker"), []byte("x"), 0644))
	before := bucketKeys(t, dir)
	layoutB := builtDataDir(t, "B")
	require.NoError(t, os.MkdirAll(filepath.Join(layoutB, "blocker"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(layoutB, "blocker", "file"), []byte("y"), 0644))

	err := deploy(ctx, layoutB, "gs://my-bucket/", opener, deployOptions{Delete: true})
	require.ErrorContains(t, err, "blocker/file")

	require.Subset(t, bucketKeys(t, dir), before)
}

func TestDeployRefusesToDeleteWithoutABucketName(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "file.txt"), []byte("contents"), 0644))
	_, opener := localBucket(t)

	err := deploy(context.Background(), dataDir, "gs://", opener, deployOptions{Delete: true})
	require.ErrorContains(t, err, "--delete needs a bucket URL")
	err = deploy(context.Background(), dataDir, "my-bucket", opener, deployOptions{Delete: true})
	require.ErrorContains(t, err, "--delete needs a bucket URL")
}
