package push

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/cgs-earth/sal/pkg"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"
)

const maxConcurrentUploads = 4

const SalGitHashAnnotation = "sal.git-commit-hash"

type PushCmd struct {
	Repository string `arg:"positional" help:"Full URL of the OCI registry and repository to push the built SAL data product to. Example: ghcr.io/my-username/my-repository"`
	Username   string `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry. This should match the username used to create the password token"`
	Password   string `arg:"--password,env:OCI_PASSWORD" help:"Password or access token for the OCI registry."`
}

func (p *PushCmd) GetUsername() string {
	return p.Username
}

func (p *PushCmd) GetPassword() string {
	return p.Password
}

var _ pkg.CmdWithAuth = (*PushCmd)(nil)

// push uploads all files in dataDir as OCI layers, then packs and tags a
// manifest that references those uploaded layers.
func push(dataDir string, repo *remote.Repository, destination string) error {
	ctx := context.Background()
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

	SnapShots, err := pkg.GetLocalSalSnapshots()
	if err != nil {
		return fmt.Errorf("error getting snapshot data %w", err)
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
				msg := fmt.Sprintf("Uploaded %d/%d files (%s)", completed, len(files), pkg.BytesToHumanReadable(uploadedBytes.Load()))
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

	description := fmt.Sprintf("SAL-produced iceberg data product for %s at commit %s", gitRemote, gitHash)

	desc, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, "application/octet-stream", oras.PackManifestOptions{
		Layers: layers,
		ManifestAnnotations: map[string]string{
			"org.opencontainers.image.source":      gitRemote,
			SalGitHashAnnotation:                   gitHash,
			"org.opencontainers.image.description": description,
			pkg.SalIcebergSnapshotsAnnotation:      strings.Join(SnapShots, ","),
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

	slog.Info("Pushed data product to " + destination + ":latest totaling " + pkg.BytesToHumanReadable(uploadedBytes.Load()))
	return nil
}

// Run executes the push command, which pushes all files in the SAL data directory
// to the specified OCI registry.
func (p *PushCmd) Run() error {
	if p.Password == "" {
		return fmt.Errorf("password is required for pushing to an OCI registry. See https://oras.land/docs/how_to_guides/remote_registries/#authentication for more information")
	}

	artifactRef, err := pkg.ParseArtifact(p.Repository)
	if err != nil {
		return err
	}

	dataDir, err := pkg.SalDataDir()
	if err != nil {
		return err
	}

	repo, err := remote.NewRepository(artifactRef.Repository)
	if err != nil {
		return fmt.Errorf("failed creating OCI registry client: %w", err)
	}

	repo.Client = pkg.NewOciClientWithOptionalAuth(p, artifactRef)

	diff, _ := pkg.FetchAndDiffSnapshots(repo, artifactRef.Reference)
	if len(diff.SnapshotsInRemoteNotLocal) > 0 {
		proceed := pkg.Confirm(fmt.Sprintf("Remote %s contains %d snapshots not present locally. Continue pushing anyways?", p.Repository, len(diff.SnapshotsInRemoteNotLocal)))
		if !proceed {
			return fmt.Errorf("cancelled pushing to %s", p.Repository)
		}
	}

	return push(dataDir, repo, artifactRef.Repository)
}
