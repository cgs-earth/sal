package salmodule

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// ContainerRunner builds and runs the container images that back SAL modules.
type ContainerRunner interface {
	BuildImage(ctx context.Context, contextDir string, tag string) error
	// ImageExists reports whether the daemon already holds an image under tag,
	// which is how a module built by a previous invocation is found and reused.
	ImageExists(ctx context.Context, tag string) (bool, error)
	// RunContainer runs cmd in a container built from image. What the container
	// writes to stdout is handed to consume as it is written, together with a
	// handle that copies files out of the container; the container is kept
	// until consume returns, so a file may still be copied after the container
	// has exited. Whatever the container wrote to stderr is returned whole.
	RunContainer(ctx context.Context, image string, env []string, cmd []string, consume ContainerOutputConsumer) (stderr []byte, err error)
}

// ContainerOutputConsumer reads a running container's stdout until it ends,
// which happens when the container exits, and may copy files out of the
// container through files until it returns.
type ContainerOutputConsumer func(ctx context.Context, stdout io.Reader, files ContainerFiles) error

// ContainerFiles copies files out of a container RunContainer created.
type ContainerFiles interface {
	// CopyFile writes the contents of the regular file at path inside the
	// container to w and reports when the file was last modified there.
	CopyFile(ctx context.Context, path string, w io.Writer) (time.Time, error)
	// CopyDirectory walks the directory at path inside the container and calls
	// visit once per entry beneath it, with the entry's path relative to the
	// directory. A directory entry is visited with a nil reader; a regular file
	// is visited with a reader over its contents that is only valid during the
	// call. Walking stops at the first error visit returns.
	CopyDirectory(ctx context.Context, path string, visit ContainerEntryVisitor) error
}

// ContainerEntryVisitor receives one entry of a directory copied out of a
// container. content is nil for a directory entry.
type ContainerEntryVisitor func(relativePath string, isDir bool, modified time.Time, content io.Reader) error

type dockerRunner struct {
	client *client.Client
}

// newDockerRunner connects to the docker daemon described by the environment.
func newDockerRunner() (*dockerRunner, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("connect to the docker daemon: %w", err)
	}
	return &dockerRunner{client: cli}, nil
}

// BuildImage builds the Dockerfile in the root of contextDir and tags the result.
func (d *dockerRunner) BuildImage(ctx context.Context, contextDir string, tag string) error {
	buildContext, err := tarDirectory(contextDir)
	if err != nil {
		return err
	}

	result, err := d.client.ImageBuild(ctx, buildContext, client.ImageBuildOptions{
		Tags:       []string{tag},
		Dockerfile: "Dockerfile",
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("build image %s: %w", tag, err)
	}
	defer func() {
		if err := result.Body.Close(); err != nil {
			slog.Warn("failed to close docker build response: " + err.Error())
		}
	}()

	return reportBuildProgress(result.Body, tag)
}

// ImageExists asks the daemon whether an image is stored under tag.
func (d *dockerRunner) ImageExists(ctx context.Context, tag string) (bool, error) {
	_, err := d.client.ImageInspect(ctx, tag)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect image %s: %w", tag, err)
	}
	return true, nil
}

// reportBuildProgress drains the daemon's JSON build stream, logging build
// output and surfacing any error the daemon reports mid-stream.
func reportBuildProgress(body io.Reader, tag string) error {
	decoder := json.NewDecoder(body)
	for {
		var message struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read docker build output for %s: %w", tag, err)
		}
		if message.Error != "" {
			return fmt.Errorf("build image %s: %s", tag, strings.TrimSpace(message.Error))
		}
		if line := strings.TrimSpace(message.Stream); line != "" {
			slog.Debug("docker build " + tag + ": " + line)
		}
	}
}

// RunContainer runs cmd in a container built from image, streaming its stdout
// to consume while it runs, and returns whatever the container wrote to stderr.
func (d *dockerRunner) RunContainer(ctx context.Context, image string, env []string, cmd []string, consume ContainerOutputConsumer) ([]byte, error) {
	created, err := d.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: image, Cmd: cmd, Env: env},
	})
	if err != nil {
		return nil, fmt.Errorf("create container for %s: %w", image, err)
	}
	defer func() {
		if _, err := d.client.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			slog.Warn("failed to remove container for " + image + ": " + err.Error())
		}
	}()

	// the wait must be requested before the container starts so that a container
	// exiting immediately is not missed
	wait := d.client.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	if _, err := d.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("start container for %s: %w", image, err)
	}

	// the log stream is followed rather than read once the container has exited;
	// a single read can otherwise race the daemon's logging driver and come back
	// empty. Following replays what the container has already written.
	logs, err := d.client.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
	if err != nil {
		return nil, fmt.Errorf("follow output of container for %s: %w", image, err)
	}
	defer func() {
		if err := logs.Close(); err != nil {
			slog.Warn("failed to close log stream for " + image + ": " + err.Error())
		}
	}()

	// the multiplexed log stream is demuxed into a pipe the consumer reads
	// stdout from as the container writes it; the followed stream, and so the
	// pipe, ends when the container exits
	var stderr bytes.Buffer
	stdoutReader, stdoutWriter := io.Pipe()
	demuxed := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(stdoutWriter, &stderr, logs)
		_ = stdoutWriter.CloseWithError(err)
		demuxed <- err
	}()

	consumeErr := consume(ctx, stdoutReader, containerFiles{client: d.client, id: created.ID})
	// a consumer that stopped reading early must not leave the demuxer blocked
	// on the pipe
	_ = stdoutReader.Close()
	if consumeErr != nil {
		// the consumer rejected the output part way through, so the task may
		// well still be running; the followed log stream, and with it the
		// demuxer, only ends once the container has stopped, so it is stopped
		// now rather than left to run on. A container that has already exited
		// answers with a conflict, which is not a failure.
		if _, err := d.client.ContainerKill(ctx, created.ID, client.ContainerKillOptions{}); err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
			slog.Warn("failed to stop container for " + image + ": " + err.Error())
		}
	}
	if err := <-demuxed; err != nil && consumeErr == nil {
		return stderr.Bytes(), fmt.Errorf("read output of container for %s: %w", image, err)
	}
	if consumeErr != nil {
		return stderr.Bytes(), consumeErr
	}

	var exitCode int64
	select {
	case err := <-wait.Error:
		return stderr.Bytes(), fmt.Errorf("wait for container %s: %w", image, err)
	case result := <-wait.Result:
		exitCode = result.StatusCode
	case <-ctx.Done():
		return stderr.Bytes(), ctx.Err()
	}
	if exitCode != 0 {
		return stderr.Bytes(), fmt.Errorf("container %s exited with status %d: %s", image, exitCode, strings.TrimSpace(stderr.String()))
	}
	return stderr.Bytes(), nil
}

// containerFiles copies files out of one container through the daemon's copy
// endpoint, which is what `docker cp` uses.
type containerFiles struct {
	client *client.Client
	id     string
}

// CopyFile fetches the file at path from the container as the single-entry tar
// archive the daemon serves it as and writes its contents to w.
func (c containerFiles) CopyFile(ctx context.Context, path string, w io.Writer) (time.Time, error) {
	result, err := c.client.CopyFromContainer(ctx, c.id, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		return time.Time{}, fmt.Errorf("copy %s from container: %w", path, err)
	}
	defer func() {
		if err := result.Content.Close(); err != nil {
			slog.Warn("failed to close copy stream for " + path + ": " + err.Error())
		}
	}()
	return extractFileFromArchive(result.Content, path, w)
}

// extractFileFromArchive writes the contents of the one regular file in a
// `docker cp` archive to w and returns the modification time its entry
// carries. The daemon archives whatever path names, so a directory arrives as
// many entries and is refused rather than flattened.
func extractFileFromArchive(archive io.Reader, path string, w io.Writer) (time.Time, error) {
	reader := tar.NewReader(archive)
	header, err := reader.Next()
	if err != nil {
		return time.Time{}, fmt.Errorf("read copy of %s from container: %w", path, err)
	}
	if header.Typeflag != tar.TypeReg {
		return time.Time{}, fmt.Errorf("copy %s from container: only a regular file can be copied, not a directory or link", path)
	}
	if _, err := io.Copy(w, reader); err != nil {
		return time.Time{}, fmt.Errorf("read copy of %s from container: %w", path, err)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return time.Time{}, fmt.Errorf("copy %s from container: expected a single file but the container returned more than one entry", path)
	}
	return header.ModTime, nil
}

// CopyDirectory fetches the directory at path from the container as the tar
// archive the daemon serves it as and visits each entry in it. The daemon
// archives the directory under its own base name, so that leading component
// is stripped to leave paths relative to the directory itself.
func (c containerFiles) CopyDirectory(ctx context.Context, path string, visit ContainerEntryVisitor) error {
	result, err := c.client.CopyFromContainer(ctx, c.id, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		return fmt.Errorf("copy %s from container: %w", path, err)
	}
	defer func() {
		if err := result.Content.Close(); err != nil {
			slog.Warn("failed to close copy stream for " + path + ": " + err.Error())
		}
	}()
	return walkDirectoryArchive(result.Content, path, visit)
}

// walkDirectoryArchive visits every entry of a `docker cp` archive of a
// directory, with paths made relative to the directory by dropping the base
// name the daemon prefixes them with. The first entry must be the directory
// itself, which is how a path naming a file rather than a directory is told
// apart and refused. Anything other than a regular file or directory, such as
// a symbolic link, is an error rather than silently left out of the copy.
func walkDirectoryArchive(archive io.Reader, path string, visit ContainerEntryVisitor) error {
	reader := tar.NewReader(archive)
	header, err := reader.Next()
	if err != nil {
		return fmt.Errorf("read copy of %s from container: %w", path, err)
	}
	if header.Typeflag != tar.TypeDir {
		return fmt.Errorf("copy %s from container: it is not a directory; a directory to copy is written as file:///absolute/path/ with a trailing slash", path)
	}
	prefix := strings.TrimSuffix(header.Name, "/") + "/"
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read copy of %s from container: %w", path, err)
		}
		relative := strings.TrimSuffix(strings.TrimPrefix(header.Name, prefix), "/")
		switch header.Typeflag {
		case tar.TypeDir:
			if err := visit(relative, true, header.ModTime, nil); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := visit(relative, false, header.ModTime, reader); err != nil {
				return err
			}
		default:
			return fmt.Errorf("copy %s from container: %s is neither a regular file nor a directory and cannot be copied", path, header.Name)
		}
	}
}

// tarDirectory packs dir into the tar stream that the docker build endpoint
// expects, skipping the git metadata of the cloned module repository.
func tarDirectory(dir string) (io.Reader, error) {
	var buf bytes.Buffer
	archive := tar.NewWriter(&buf)

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		// symlinks and other irregular files are not part of a build context
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		_, err = io.Copy(archive, file)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create docker build context from %s: %w", dir, err)
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("create docker build context from %s: %w", dir, err)
	}
	return &buf, nil
}
