package deploy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/cgs-earth/sal/edit"
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

// Deploy deploys a built SAL data product to a bucket.
// Authentication is handled by tools like gsutil or aws s3
// which manage credentials.
// After upload, files then can be queried via a service like duckdb
// using a query like the following
// SELECT subject, predicate, object
// FROM iceberg_scan(
//
//	'gs://sal-test-bucket/sal/triples'
//
// )LIMIT 5;
type DeployCmd struct {
	Bucket string `arg:"--bucket" help:"The scheme and name of the bucket to deploy a built SAL data product to. Example: s3://my-bucket or gcs://my-bucket"`
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
	return deploy(ctx, salDataDir, c.Bucket, blob.OpenBucket)
}

type bucketOpener func(context.Context, string) (*blob.Bucket, error)

type deployFile struct {
	path string
	key  string
	size int64
}

// deploy uploads every file under dataDir to bucketURL, preserving relative paths as blob keys.
func deploy(ctx context.Context, dataDir string, bucketURL string, openBucket bucketOpener) error {
	stagedDataDir, cleanup, err := stagedDataDirForDeploy(dataDir, bucketURL)
	if err != nil {
		return err
	}
	defer cleanup()

	openedBucketURL := bucketOpenURL(bucketURL)
	files, err := filesToDeploy(stagedDataDir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no files found in SAL data directory: %s", dataDir)
	}

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

// bucketOpenURL converts user-facing bucket URLs into the format expected by Go Cloud blob drivers.
func bucketOpenURL(bucketURL string) string {
	u, err := url.Parse(bucketURL)
	if err != nil || u.Scheme == "" {
		return bucketURL
	}

	switch u.Scheme {
	case "gs", "s3", "azblob":
		pathPrefix := strings.Trim(u.EscapedPath(), "/")
		if pathPrefix == "" {
			return u.String()
		}
		query := u.Query()
		prefix := strings.Trim(query.Get("prefix"), "/")
		if prefix != "" {
			pathPrefix += "/" + prefix
		}
		query.Set("prefix", pathPrefix+"/")
		u.RawQuery = query.Encode()
		u.Path = ""
		u.RawPath = ""
	}

	return u.String()
}

// stagedDataDirForDeploy rewrites Iceberg metadata in a temporary copy so local data remains unchanged.
func stagedDataDirForDeploy(dataDir string, bucketURL string) (string, func(), error) {
	stagedDataDir, err := os.MkdirTemp("", "sal-deploy-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create deployment staging directory: %w", err)
	}
	cleanup := func() {
		if err := os.RemoveAll(stagedDataDir); err != nil {
			slog.Warn("failed to remove deployment staging directory: " + err.Error())
		}
	}
	if err := copyDir(dataDir, stagedDataDir); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := rewriteStagedIcebergRoots(stagedDataDir, bucketURL); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return stagedDataDir, cleanup, nil
}

// copyDir copies regular files from src to dst while preserving permissions and relative paths.
func copyDir(src string, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src string, dst string, mode os.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() {
		if closeErr := in.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close %s: %w", src, closeErr)
		}
	}()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close %s: %w", dst, closeErr)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

// rewriteStagedIcebergRoots points each staged Iceberg table at its final remote object URI.
func rewriteStagedIcebergRoots(stagedDataDir string, bucketURL string) error {
	tables, err := icebergTablePaths(stagedDataDir)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}

	baseRoot := objectBaseURL(bucketURL)
	for _, tablePath := range tables {
		rel, err := filepath.Rel(stagedDataDir, tablePath)
		if err != nil {
			return fmt.Errorf("relative table path for %s: %w", tablePath, err)
		}
		newRoot := joinRemote(baseRoot, filepath.ToSlash(rel))
		changes, err := edit.RewriteIcebergTableRoot(tablePath, newRoot)
		if err != nil {
			return err
		}
		slog.Info("Prepared Iceberg table metadata for deploy",
			"table", rel,
			"new_root", newRoot,
			"files_changed", changes,
		)
	}
	return nil
}

func icebergTablePaths(root string) ([]string, error) {
	var tables []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if path == root || entry.Name() != "metadata" {
			return nil
		}
		tablePath := filepath.Dir(path)
		matches, err := filepath.Glob(filepath.Join(path, "*.metadata.json"))
		if err != nil {
			return err
		}
		if len(matches) > 0 {
			tables = append(tables, tablePath)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("find Iceberg tables in %s: %w", root, err)
	}
	return tables, nil
}

func objectBaseURL(bucketURL string) string {
	u, err := url.Parse(bucketURL)
	if err != nil || u.Scheme == "" {
		return strings.TrimSuffix(bucketURL, "/")
	}
	if u.Scheme != "gcs" && u.Scheme != "gs" && u.Scheme != "s3" && u.Scheme != "azblob" {
		u.RawQuery = ""
		return strings.TrimSuffix(u.String(), "/")
	}

	prefix := strings.Trim(u.Path, "/")
	query := u.Query()
	if queryPrefix := strings.Trim(query.Get("prefix"), "/"); queryPrefix != "" {
		if prefix == "" {
			prefix = queryPrefix
		} else {
			prefix += "/" + queryPrefix
		}
	}
	u.RawQuery = ""
	u.Path = ""
	u.RawPath = ""

	base := strings.TrimSuffix(u.String(), "/")
	if prefix == "" {
		return base
	}
	return base + "/" + prefix
}

func joinRemote(base string, parts ...string) string {
	joined := strings.Trim(strings.Join(parts, "/"), "/")
	if joined == "" {
		return strings.TrimSuffix(base, "/")
	}
	return strings.TrimSuffix(base, "/") + "/" + joined
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
