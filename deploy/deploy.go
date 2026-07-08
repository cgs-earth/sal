package deploy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/cgs-earth/sal/pkg"
	"gocloud.dev/blob"
	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/memblob"
	_ "gocloud.dev/blob/s3blob"
	"golang.org/x/sync/errgroup"
)

const maxConcurrentDeployUploads = 4

type DeployCmd struct {
	Bucket   string `arg:"--bucket" help:"The scheme and name of the bucket to deploy a built SAL data product to. Example: s3://my-bucket or gcs://my-bucket"`
	Username string `arg:"--username" help:"Username for the bucket"`
	Password string `arg:"--password" help:"Password for the bucket"`
}

func (c *DeployCmd) Run() error {
	if strings.TrimSpace(c.Bucket) == "" {
		return fmt.Errorf("--bucket is required")
	}

	salDataDir, err := pkg.SalDataDir()
	if err != nil {
		return fmt.Errorf("failed to get SAL data directory: %w", err)
	}
	if _, err := os.Stat(salDataDir); os.IsNotExist(err) {
		return fmt.Errorf("no SAL data directory found in %s", salDataDir)
	}

	ctx := context.Background()
	return deploy(ctx, salDataDir, c.Bucket, c.Username, c.Password, blob.OpenBucket)
}

type bucketOpener func(context.Context, string) (*blob.Bucket, error)

type deployFile struct {
	path string
	key  string
	size int64
}

// deploy uploads every file under dataDir to bucketURL, preserving relative paths as blob keys.
func deploy(ctx context.Context, dataDir string, bucketURL string, username string, password string, openBucket bucketOpener) error {
	openedBucketURL := normalizeBucketURL(bucketURL)
	files, err := filesToDeploy(dataDir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no files found in SAL data directory: %s", dataDir)
	}

	restoreEnv, err := applyCredentialEnvironment(openedBucketURL, username, password)
	if err != nil {
		return err
	}
	defer restoreEnv()

	bucket, err := openBucket(ctx, openedBucketURL)
	if err != nil {
		return fmt.Errorf("open bucket %s: %w", bucketURL, err)
	}
	defer func() {
		if err := bucket.Close(); err != nil {
			slog.Warn("failed to close deployment bucket: " + err.Error())
		}
	}()

	var deployedFiles atomic.Int64
	var deployedBytes atomic.Int64
	group, uploadCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxConcurrentDeployUploads)
	for _, file := range files {
		group.Go(func() error {
			if err := uploadFile(uploadCtx, bucket, file); err != nil {
				return err
			}

			completed := deployedFiles.Add(1)
			deployedBytes.Add(file.size)
			if completed%5 == 0 {
				slog.Info("Deployed files",
					"completed", completed,
					"total", len(files),
					"bytes", deployedBytes.Load(),
				)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	slog.Info("Deployed SAL data directory",
		"source", dataDir,
		"bucket", bucketURL,
		"files", deployedFiles.Load(),
		"bytes", deployedBytes.Load(),
	)
	return nil
}

func normalizeBucketURL(bucketURL string) string {
	if strings.HasPrefix(bucketURL, "gcs://") {
		return "gs://" + strings.TrimPrefix(bucketURL, "gcs://")
	}
	return bucketURL
}

func filesToDeploy(dataDir string) ([]deployFile, error) {
	var files []deployFile
	err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}
		files = append(files, deployFile{
			path: path,
			key:  filepath.ToSlash(rel),
			size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read SAL data directory %s: %w", dataDir, err)
	}
	return files, nil
}

func uploadFile(ctx context.Context, bucket *blob.Bucket, file deployFile) (err error) {
	src, err := os.Open(file.path)
	if err != nil {
		return fmt.Errorf("open %s: %w", file.path, err)
	}
	defer func() {
		if closeErr := src.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close %s: %w", file.path, closeErr)
		}
	}()

	writer, err := bucket.NewWriter(ctx, file.key, nil)
	if err != nil {
		return fmt.Errorf("create blob %s: %w", file.key, err)
	}
	defer func() {
		if err != nil {
			_ = writer.Close()
		}
	}()

	if _, err := io.Copy(writer, src); err != nil {
		return fmt.Errorf("upload %s: %w", file.key, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close blob %s: %w", file.key, err)
	}
	return nil
}

func applyCredentialEnvironment(bucketURL string, username string, password string) (func(), error) {
	if username == "" && password == "" {
		return func() {}, nil
	}
	if username == "" || password == "" {
		return nil, fmt.Errorf("both --username and --password are required when providing bucket credentials")
	}

	switch {
	case strings.HasPrefix(bucketURL, "s3://"):
		return setTemporaryEnv(map[string]string{
			"AWS_ACCESS_KEY_ID":     username,
			"AWS_SECRET_ACCESS_KEY": password,
		}), nil
	case strings.HasPrefix(bucketURL, "azblob://"):
		return setTemporaryEnv(map[string]string{
			"AZURE_STORAGE_ACCOUNT": username,
			"AZURE_STORAGE_KEY":     password,
		}), nil
	default:
		slog.Warn("bucket credentials were provided, but this bucket scheme does not have a generic username/password mapping; relying on Go Cloud driver authentication")
		return func() {}, nil
	}
}

func setTemporaryEnv(values map[string]string) func() {
	type oldValue struct {
		value string
		set   bool
	}
	old := make(map[string]oldValue, len(values))
	for key, value := range values {
		prev, ok := os.LookupEnv(key)
		old[key] = oldValue{value: prev, set: ok}
		_ = os.Setenv(key, value)
	}

	return func() {
		for key, prev := range old {
			if prev.set {
				_ = os.Setenv(key, prev.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}
}
