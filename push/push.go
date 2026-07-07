package push

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"

	"github.com/cgs-earth/sal/pkg"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const defaultRegistry = "ghcr.io"
const maxConcurrentUploads = 4

type PushCmd struct {
	Repository string `arg:"positional" help:"Full URL of the OCI registry and repository to push the built SAL data product to. Example: ghcr.io/my-username/my-repository"`
	Username   string `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry. This should match the username used to create the password token"`
	Password   string `arg:"--password,env:OCI_PASSWORD" help:"Password or access token for the OCI registry."`
}

// formatUploadedSize returns a human-readable transfer size using MB for
// larger uploads and KB for smaller uploads.
func formatUploadedSize(bytes int64) string {
	if bytes >= 1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
}

// push uploads all files in dataDir as OCI layers, then packs and tags a
// manifest that references those uploaded layers.
func push(ctx context.Context, dataDir string, repo *remote.Repository, destination string) error {
	slog.Info("Pushing SAL data product in " + dataDir + " to " + destination)

	type uploadFile struct {
		path  string
		title string
	}

	var files []uploadFile
	err := filepath.WalkDir(dataDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}

		files = append(files, uploadFile{path: path, title: filepath.ToSlash(rel)})
		return nil
	})
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no files found in SAL data directory: %s", dataDir)
	}

	layers := make([]ocispec.Descriptor, len(files))
	var uploadedFiles atomic.Int64
	var uploadedBytes atomic.Int64
	group, uploadCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxConcurrentUploads)
	for i, file := range files {
		group.Go(func() error {
			b, err := os.ReadFile(file.path)
			if err != nil {
				return fmt.Errorf("read %s: %w", file.path, err)
			}

			desc, err := oras.PushBytes(uploadCtx, repo, ocispec.MediaTypeImageLayer, b)
			if err != nil {
				return fmt.Errorf("push layer %s: %w", file.title, err)
			}

			desc.Annotations = map[string]string{
				"org.opencontainers.image.title": file.title,
			}

			layers[i] = desc
			completed := uploadedFiles.Add(1)
			uploadedBytes.Add(int64(len(b)))
			if completed%5 == 0 {
				msg := fmt.Sprintf("Uploaded %d/%d files (%s)", completed, len(files), formatUploadedSize(uploadedBytes.Load()))
				slog.Info(msg)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	gitRemote, err := pkg.DefaultGitRemote()
	if err != nil {
		slog.Warn("failed to get git remote URL for artifact annotation: " + err.Error())
	}

	gitHash, err := pkg.GitCommitHash()
	if err != nil {
		slog.Warn("failed to get git commit hash for artifact annotation: " + err.Error())
	}
	desc, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, "application/octet-stream", oras.PackManifestOptions{
		Layers: layers,
		ManifestAnnotations: map[string]string{
			"org.opencontainers.image.source": gitRemote,
			"sal.git-commit-hash":             gitHash,
		},
		// TODO add more metadata and digest info about the sal config itself
		ConfigDescriptor: &ocispec.Descriptor{
			MediaType: "application/vnd.cgs-earth.sal.config.v1+json",
			Annotations: map[string]string{
				"sal.git-commit-hash": gitHash,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("pack manifest: %w", err)
	}

	// tag and push
	if err := repo.Tag(ctx, desc, "latest"); err != nil {
		return fmt.Errorf("tag artifact: %w", err)
	}
	if err := repo.Tag(ctx, desc, gitHash); err != nil {
		return fmt.Errorf("push artifact: %w", err)
	}

	slog.Info("Pushed data product to " + destination + ":latest totaling " + formatUploadedSize(uploadedBytes.Load()))
	return nil
}

// Run executes the push command, which pushes all files in the SAL data directory
// to the specified OCI registry.
func Run(p *PushCmd) error {
	if p.Password == "" {
		return fmt.Errorf("password is required for pushing to an OCI registry. See https://oras.land/docs/how_to_guides/remote_registries/#authentication for more information")
	}

	username := p.Username
	if username == "" {
		owner, err := pkg.GitProjectOwner()
		if err != nil {
			return fmt.Errorf("failed to get Git project owner: %w", err)
		}
		if owner == "" {
			return fmt.Errorf("username is required for deploying to an OCI registry and could not be inferred from the git project URL. Please provide a username using the --username flag")
		}
		username = owner
	}
	destination := p.Repository
	if destination == "" {
		projectName, err := pkg.GitProjectName()
		if err != nil {
			return fmt.Errorf("failed to get Git project name: %w", err)
		}
		destination = defaultRegistry + "/" + username + "/" + projectName
		slog.Info("No registry/repository specified, using " + destination + " as the default registry.")
	} else {
		destination = strings.TrimPrefix(destination, "https://")
		destination = strings.TrimPrefix(destination, "http://")
	}

	dataDir, err := pkg.SalDataDir()
	if err != nil {
		return err
	}

	repo, err := remote.NewRepository(destination)
	if err != nil {
		return fmt.Errorf("failed creating OCI registry client: %w", err)
	}

	credential := auth.Credential{
		Username: username,
		Password: p.Password,
	}

	repo.Client = &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.NewCache(),
		Credential: auth.StaticCredential(repo.Reference.Registry, credential),
	}
	ctx := context.Background()
	return push(ctx, dataDir, repo, destination)
}
