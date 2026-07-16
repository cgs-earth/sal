package clone

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	_ "crypto/sha256"

	"github.com/cgs-earth/sal/pkg"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	sourceAnnotation    = "org.opencontainers.image.source"
	gitCommitAnnotation = "sal.git-commit-hash"
	maxConcurrentPulls  = 8
)

type OciArtifactRetrievalCmd struct {
	Artifact    string `arg:"positional" help:"Full URL of the OCI artifact to pull. Example: ghcr.io/my-username/my-repository:latest"`
	Username    string `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry"`
	Password    string `arg:"--password,env:OCI_PASSWORD" help:"Password for the OCI registry"`
	Destination string `arg:"--destination" help:"Optional destination path for the cloned source repository. If not specified, git clone will create a directory in the current working directory"`
}

func (cmd *OciArtifactRetrievalCmd) GetUsername() string {
	return cmd.Username
}

func (cmd *OciArtifactRetrievalCmd) GetPassword() string {
	return cmd.Password
}

func (cmd *OciArtifactRetrievalCmd) GetArtifactReference() (pkg.ArtifactReference, error) {
	return pkg.ParseArtifact(cmd.Artifact)
}

var _ pkg.OciArtifactCmdWithAuth = (*OciArtifactRetrievalCmd)(nil)

type artifactMetadata struct {
	source    string
	gitCommit string
}

type commandRunner func(ctx context.Context, dir string, name string, args ...string) error

func runCommand(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %s: %w", name, strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

func metadataFromManifest(manifest ocispec.Manifest) (artifactMetadata, error) {
	source := manifest.Annotations[sourceAnnotation]
	if source == "" {
		return artifactMetadata{}, fmt.Errorf("artifact manifest is missing %s annotation", sourceAnnotation)
	}

	gitCommit := manifest.Annotations[gitCommitAnnotation]
	if gitCommit == "" {
		gitCommit = manifest.Config.Annotations[gitCommitAnnotation]
	}
	if gitCommit == "" {
		return artifactMetadata{}, fmt.Errorf("artifact manifest is missing %s annotation", gitCommitAnnotation)
	}

	return artifactMetadata{source: source, gitCommit: gitCommit}, nil
}

func cloneRepository(ctx context.Context, source string, destination string, run commandRunner) (string, error) {
	if destination != "" {
		if err := run(ctx, "", "git", "clone", source, destination); err != nil {
			return "", err
		}
		return destination, nil
	}

	if err := run(ctx, "", "git", "clone", source); err != nil {
		return "", err
	}
	return pkg.RepoDirFromSource(source), nil
}

// checkoutRepository pins the cloned source tree to the commit recorded in the
// artifact manifest.
func checkoutRepository(ctx context.Context, repoDir string, gitCommit string, run commandRunner) error {
	return run(ctx, repoDir, "git", "checkout", gitCommit)
}

func initializeSALProject(ctx context.Context, repoDir string, run commandRunner) error {
	salPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate sal executable: %w", err)
	}
	return run(ctx, repoDir, salPath, "init")
}

// pullManifestLayers copies a SAL OCI artifact into destination, restoring
// layers according to their exact org.opencontainers.image.title annotations.
func pullManifestLayers(ctx context.Context, src oras.ReadOnlyTarget, manifest ocispec.Manifest, desc ocispec.Descriptor, reference string, destination string) error {
	if err := os.MkdirAll(destination, 0755); err != nil {
		return fmt.Errorf("create pull destination %s: %w", destination, err)
	}

	fs, err := file.New(destination)
	if err != nil {
		return fmt.Errorf("create destination file store: %w", err)
	}
	defer func() {
		if err := fs.Close(); err != nil {
			slog.Warn("failed to clean up pull file store: " + err.Error())
		}
	}()

	var pulledFiles atomic.Int64
	group, pullCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxConcurrentPulls)
	for _, layer := range manifest.Layers {
		title := layer.Annotations[ocispec.AnnotationTitle]
		if title == "" {
			continue
		}

		group.Go(func() error {
			rc, err := src.Fetch(pullCtx, layer)
			if err != nil {
				return fmt.Errorf("fetch layer %s: %w", title, err)
			}
			layer.Annotations[ocispec.AnnotationTitle] = title
			err = fs.Push(pullCtx, layer, rc)
			closeErr := rc.Close()
			if err != nil {
				return fmt.Errorf("write layer %s: %w", title, err)
			}
			if closeErr != nil {
				return fmt.Errorf("close layer %s: %w", title, closeErr)
			}
			pulledFiles.Add(1)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	if pulledFiles.Load() == 0 {
		return fmt.Errorf("artifact %s has no SAL data layers with %s annotations", reference, ocispec.AnnotationTitle)
	}

	slog.Info("Pulled data product to "+destination, "digest", desc.Digest.String(), "files", pulledFiles.Load())
	return nil
}

func (cmd *OciArtifactRetrievalCmd) RunClone() error {
	ctx := context.Background()
	ref, err := pkg.ParseArtifact(cmd.Artifact)
	if err != nil {
		return err
	}

	repo, err := remote.NewRepository(ref.Repository)
	if err != nil {
		return fmt.Errorf("failed creating OCI registry client: %w", err)
	}

	repo.Client = pkg.NewOciClientWithOptionalAuth(cmd, ref)

	desc, manifest, err := pkg.FetchManifest(ctx, repo, ref.Reference)
	if err != nil {
		return err
	}

	metadata, err := metadataFromManifest(manifest)
	if err != nil {
		return err
	}

	repoDir, err := cloneRepository(ctx, metadata.source, cmd.Destination, runCommand)
	if err != nil {
		return fmt.Errorf("clone source repository: %w", err)
	}
	slog.Info("Cloned " + metadata.source + " to " + repoDir + " with commit " + metadata.gitCommit)
	if err := checkoutRepository(ctx, repoDir, metadata.gitCommit, runCommand); err != nil {
		return fmt.Errorf("checkout source repository: %w", err)
	}
	if err := initializeSALProject(ctx, repoDir, runCommand); err != nil {
		return fmt.Errorf("initialize cloned SAL project: %w", err)
	}

	dataDir := filepath.Join(repoDir, ".sal", "data")
	return pullManifestLayers(ctx, repo, manifest, desc, ref.Reference, dataDir)
}
