package deploy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/cgs-earth/sal/pkg"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const defaultRegistry = "ghcr.io"

type DeployCmd struct {
	Repository string `arg:"positional" help:"Full URL of the OCI registry and repository to push the built SAL data product to. Example: ghcr.io/my-username/my-repository"`
	Username   string `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry. This should match the username used to create the password token"`
	Password   string `arg:"--password,env:OCI_PASSWORD" help:"Password or access token for the OCI registry."`
	UpdateHash bool   `arg:"--update-hash" help:"Update the hash of the SAL data product based on the remote OCI registry"`
}

// updateHash retrieves the hash of the latest pushed SAL data product from the remote OCI registry and writes it to the DATAHEAD file in the local SAL data directory.
func updateHash(ctx context.Context, repo *remote.Repository) error {
	desc, err := repo.Resolve(ctx, "latest")
	if err != nil {
		return fmt.Errorf("resolve latest: %w", err)
	}

	slog.Info(desc.Digest.String())

	salDataHeadFile, err := pkg.SalDataHeadFile()
	if err != nil {
		return fmt.Errorf("failed to open DATAHEAD file: %w", err)
	}
	defer func() {
		err = salDataHeadFile.Close()
		if err != nil {
			slog.Error("failed to close DATAHEAD file", "error", err)
		}
	}()

	_, err = io.WriteString(salDataHeadFile, desc.Digest.String())
	if err != nil {
		return fmt.Errorf("failed to write descriptor to DATAHEAD file: %w", err)
	}

	return nil
}

func deploy(ctx context.Context, dataDir string, repo *remote.Repository, destination string) error {
	var layers []ocispec.Descriptor
	slog.Info("Deploying SAL data product in " + dataDir + " to " + destination)

	err := filepath.WalkDir(dataDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}

		desc, err := oras.PushBytes(ctx, repo, ocispec.MediaTypeImageLayer, b)
		if err != nil {
			return fmt.Errorf("push layer %s: %w", rel, err)
		}

		desc.Annotations = map[string]string{
			"org.opencontainers.image.title": filepath.ToSlash(rel),
		}

		layers = append(layers, desc)
		return nil
	})
	if err != nil {
		return err
	}

	if len(layers) == 0 {
		return fmt.Errorf("no files found in SAL data directory: %s", dataDir)
	}

	gitRemote, err := pkg.DefaultGitRemote()
	if err != nil {
		slog.Warn("failed to get git remote URL for artifact annotation: " + err.Error())
	}

	desc, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, "application/octet-stream", oras.PackManifestOptions{
		Layers: layers,
		ManifestAnnotations: map[string]string{
			"org.opencontainers.image.source": gitRemote,
		},
	})
	if err != nil {
		return fmt.Errorf("pack manifest: %w", err)
	}

	if err := repo.Tag(ctx, desc, "latest"); err != nil {
		return fmt.Errorf("tag artifact: %w", err)
	}
	slog.Info("Deployed data product to " + destination + ":latest")
	return nil
}

// Run executes the push command, which pushes all files in the SAL data directory
// to the specified OCI registry.
func Run(p *DeployCmd) error {
	if p.Password == "" {
		return fmt.Errorf("password is required for deploying to an OCI registry. See https://oras.land/docs/how_to_guides/remote_registries/#authentication for more information")
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
	if !p.UpdateHash {
		err := deploy(ctx, dataDir, repo, destination)
		if err != nil {
			return err
		}
	}
	return updateHash(ctx, repo)
}
