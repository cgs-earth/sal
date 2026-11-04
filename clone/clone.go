package clone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "crypto/sha256"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.opentelemetry.io/otel/attribute"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	sourceAnnotation    = "org.opencontainers.image.source"
	gitCommitAnnotation = "sal.git-commit-hash"
)

type OciArtifactRetrievalCmd struct {
	Artifact    string `arg:"positional" help:"Full URL of the OCI artifact to pull. Example: ghcr.io/my-username/my-repository:latest"`
	Username    string `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry"`
	Password    string `arg:"--password,env:OCI_PASSWORD" help:"Password for the OCI registry"`
	Destination string `arg:"--destination" help:"Optional destination path for the cloned source repository. If not specified, git clone will create a directory in the current working directory"`
	NoTUI       bool   `arg:"--no-tui" help:"write the log to stderr as it happens instead of opening the progress and log panes"`
	Parallel    int    `arg:"--parallel" default:"16" help:"how many files to pull from the registry at once"`
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

// initializeSALProject runs `sal init` in repoDir with the description the
// artifact carries, since nothing is at the prompt to answer for one.
func initializeSALProject(ctx context.Context, repoDir string, description string, run commandRunner) error {
	salPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate sal executable: %w", err)
	}
	return run(ctx, repoDir, salPath, "init", "--description", description)
}

// projectDescription is the description the project pulled from an
// artifact is initialized with: the one the artifact carries from the
// project node it was pushed from, or, for an artifact pushed before that
// was recorded, one naming the repository it was pulled from.
func projectDescription(manifest ocispec.Manifest, repository string) string {
	if description := strings.TrimSpace(manifest.Annotations[pkg.SalDescriptionAnnotation]); description != "" {
		return description
	}
	return "Data product pulled from " + repository
}

// ErrDirectoryNotEmpty is returned by a pull outside a SAL project when the
// current directory holds anything, since a pull only initializes a
// directory it can make a project of from nothing.
var ErrDirectoryNotEmpty = errors.New("the current directory is not a SAL project and is not empty; run sal pull inside an initialized project, or in an empty directory for the pull to initialize one from the artifact")

// initializeEmptyDirectory makes the current directory, which must be
// empty, a SAL project for the artifact: a git repository whose origin is
// the source repository the manifest records, so the data product sits
// under the same project name and base it was built with, initialized with
// the description the artifact carries. No source is checked out; `sal
// clone` is what restores the sources with the data product. It returns the
// project's data directory.
func initializeEmptyDirectory(ctx context.Context, dir string, manifest ocispec.Manifest, repository string, run commandRunner) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) > 0 {
		return "", ErrDirectoryNotEmpty
	}
	source := manifest.Annotations[sourceAnnotation]
	if source == "" {
		return "", fmt.Errorf("cannot initialize a project for the artifact: its manifest is missing the %s annotation naming the source repository", sourceAnnotation)
	}
	slog.Info("Initializing a SAL project for the artifact in " + dir + " with the source repository " + source)
	if err := run(ctx, dir, "git", "init"); err != nil {
		return "", err
	}
	if err := run(ctx, dir, "git", "remote", "add", "origin", source); err != nil {
		return "", err
	}
	if err := initializeSALProject(ctx, dir, projectDescription(manifest, repository), run); err != nil {
		return "", fmt.Errorf("initialize SAL project: %w", err)
	}
	return filepath.Join(dir, ".sal", "data"), nil
}

// RunClone clones the source repository the artifact names at the commit it
// was built from, initializes it as a SAL project, and pulls the data
// product into it. The invocation is one trace, `sal.clone`, which the
// progress pane follows step by step.
func (cmd *OciArtifactRetrievalCmd) RunClone() (err error) {
	ref, err := pkg.ParseArtifact(cmd.Artifact)
	if err != nil {
		return err
	}
	ctx, span := telemetry.Start(context.Background(), "sal.clone", attribute.String("sal.oci.repository", ref.Repository))
	defer func() { telemetry.End(span, err) }()

	repo, err := remote.NewRepository(ref.Repository)
	if err != nil {
		return fmt.Errorf("failed creating OCI registry client: %w", err)
	}

	repo.Client = pkg.NewOciClientWithOptionalAuth(cmd, ref)

	manifestCtx, manifestSpan := telemetry.Start(ctx, "oci.fetch_manifest", attribute.String("sal.oci.reference", ref.Reference))
	desc, manifest, err := pkg.FetchManifest(manifestCtx, repo, ref.Reference)
	telemetry.End(manifestSpan, err)
	if err != nil {
		return err
	}

	metadata, err := metadataFromManifest(manifest)
	if err != nil {
		return err
	}

	cloneCtx, cloneSpan := telemetry.Start(ctx, "clone.repository", attribute.String("sal.clone.source", metadata.source))
	repoDir, err := cloneRepository(cloneCtx, metadata.source, cmd.Destination, runCommand)
	telemetry.End(cloneSpan, err)
	if err != nil {
		return fmt.Errorf("clone source repository: %w", err)
	}
	slog.Info("Cloned " + metadata.source + " to " + repoDir + " with commit " + metadata.gitCommit)
	checkoutCtx, checkoutSpan := telemetry.Start(ctx, "clone.checkout", attribute.String("sal.clone.commit", metadata.gitCommit))
	err = checkoutRepository(checkoutCtx, repoDir, metadata.gitCommit, runCommand)
	telemetry.End(checkoutSpan, err)
	if err != nil {
		return fmt.Errorf("checkout source repository: %w", err)
	}
	initCtx, initSpan := telemetry.Start(ctx, "clone.init", attribute.String("sal.clone.directory", repoDir))
	err = initializeSALProject(initCtx, repoDir, projectDescription(manifest, ref.Repository), runCommand)
	telemetry.End(initSpan, err)
	if err != nil {
		return fmt.Errorf("initialize cloned SAL project: %w", err)
	}

	dataDir := filepath.Join(repoDir, ".sal", "data")
	return pkg.PullManifestLayers(ctx, repo, manifest, desc, ref.Reference, dataDir, cmd.Parallel)
}
