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
	RunContainer(ctx context.Context, image string, env []string, cmd []string) (stdout []byte, stderr []byte, err error)
}

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

// RunContainer runs cmd in a container built from image and returns whatever the
// container wrote to stdout and stderr.
func (d *dockerRunner) RunContainer(ctx context.Context, image string, env []string, cmd []string) ([]byte, []byte, error) {
	created, err := d.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: image, Cmd: cmd, Env: env},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create container for %s: %w", image, err)
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
		return nil, nil, fmt.Errorf("start container for %s: %w", image, err)
	}

	// the log stream is followed rather than read once the container has exited;
	// a single read can otherwise race the daemon's logging driver and come back
	// empty. Following replays what the container has already written.
	logs, err := d.client.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
	if err != nil {
		return nil, nil, fmt.Errorf("follow output of container for %s: %w", image, err)
	}
	defer func() {
		if err := logs.Close(); err != nil {
			slog.Warn("failed to close log stream for " + image + ": " + err.Error())
		}
	}()

	// the followed stream ends when the container exits
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logs); err != nil {
		return nil, nil, fmt.Errorf("read output of container for %s: %w", image, err)
	}

	var exitCode int64
	select {
	case err := <-wait.Error:
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("wait for container %s: %w", image, err)
	case result := <-wait.Result:
		exitCode = result.StatusCode
	case <-ctx.Done():
		return stdout.Bytes(), stderr.Bytes(), ctx.Err()
	}
	if exitCode != 0 {
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("container %s exited with status %d: %s", image, exitCode, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), stderr.Bytes(), nil
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
