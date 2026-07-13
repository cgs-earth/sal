package clone

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "crypto/sha256"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const (
	sourceAnnotation    = "org.opencontainers.image.source"
	gitCommitAnnotation = "sal.git-commit-hash"
)

type CloneCmd struct {
	Artifact    string `arg:"positional" help:"Full URL of the OCI artifact to pull. Example: ghcr.io/my-username/my-repository:latest"`
	Username    string `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry"`
	Password    string `arg:"--password,env:OCI_PASSWORD" help:"Password for the OCI registry"`
	Destination string `arg:"--destination" help:"Optional destination path for the cloned source repository. If not specified, git clone will create a directory in the current working directory"`
}

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

func fetchManifest(ctx context.Context, src oras.ReadOnlyTarget, reference string) (ocispec.Descriptor, ocispec.Manifest, error) {
	desc, manifestBytes, err := oras.FetchBytes(ctx, src, reference, oras.DefaultFetchBytesOptions)
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Manifest{}, fmt.Errorf("fetch artifact manifest %s: %w", reference, err)
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return ocispec.Descriptor{}, ocispec.Manifest{}, fmt.Errorf("decode artifact manifest %s: %w", reference, err)
	}

	return desc, manifest, nil
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

func repoDirFromSource(source string) string {
	source = strings.TrimSuffix(source, "/")
	source = strings.TrimSuffix(source, ".git")
	if i := strings.LastIndex(source, "/"); i != -1 {
		return source[i+1:]
	}
	if i := strings.LastIndex(source, ":"); i != -1 {
		return source[i+1:]
	}
	return source
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
	return repoDirFromSource(source), nil
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

	var pulledFiles int
	for _, layer := range manifest.Layers {
		title := layer.Annotations[ocispec.AnnotationTitle]
		if title == "" {
			continue
		}

		rc, err := src.Fetch(ctx, layer)
		if err != nil {
			return fmt.Errorf("fetch layer %s: %w", title, err)
		}
		layer.Annotations[ocispec.AnnotationTitle] = title
		err = fs.Push(ctx, layer, rc)
		closeErr := rc.Close()
		if err != nil {
			return fmt.Errorf("write layer %s: %w", title, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close layer %s: %w", title, closeErr)
		}
		pulledFiles++
	}

	if pulledFiles == 0 {
		return fmt.Errorf("artifact %s has no SAL data layers with %s annotations", reference, ocispec.AnnotationTitle)
	}

	slog.Info("Pulled data product to "+destination, "digest", desc.Digest.String(), "files", pulledFiles)
	return nil
}

type artifactReference struct {
	repository   string
	reference    string
	registryName string
	owner        string
	artifactName string
}

func parseArtifact(artifact string) (artifactReference, error) {
	artifact = strings.TrimPrefix(artifact, "https://")
	artifact = strings.TrimPrefix(artifact, "http://")

	ref, err := registry.ParseReference(artifact)
	if err != nil {
		return artifactReference{}, fmt.Errorf("invalid OCI artifact reference: %w", err)
	}

	owner, _, _ := strings.Cut(ref.Repository, "/")
	artifactName := repoDirFromSource(ref.Repository)
	return artifactReference{
		repository:   ref.Registry + "/" + ref.Repository,
		reference:    ref.ReferenceOrDefault(),
		registryName: ref.Registry,
		owner:        owner,
		artifactName: artifactName,
	}, nil
}

func credentialFromConfig(cfg *CloneCmd, ref artifactReference) auth.Credential {
	username := cfg.Username
	if username == "" && cfg.Password != "" {
		username = ref.owner
	}

	return auth.Credential{
		Username: username,
		Password: cfg.Password,
	}
}

func Run(cfg *CloneCmd) error {
	ctx := context.Background()
	ref, err := parseArtifact(cfg.Artifact)
	if err != nil {
		return err
	}

	repo, err := remote.NewRepository(ref.repository)
	if err != nil {
		return fmt.Errorf("failed creating OCI registry client: %w", err)
	}

	if cfg.Username != "" || cfg.Password != "" {
		credential := credentialFromConfig(cfg, ref)
		repo.Client = &auth.Client{
			Client:     retry.DefaultClient,
			Cache:      auth.NewCache(),
			Credential: auth.StaticCredential(ref.registryName, credential),
		}
	}

	desc, manifest, err := fetchManifest(ctx, repo, ref.reference)
	if err != nil {
		return err
	}

	metadata, err := metadataFromManifest(manifest)
	if err != nil {
		return err
	}

	repoDir, err := cloneRepository(ctx, metadata.source, cfg.Destination, runCommand)
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
	return pullManifestLayers(ctx, repo, manifest, desc, ref.reference, dataDir)
}
