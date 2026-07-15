package clone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apache/iceberg-go/catalog"
	"github.com/cgs-earth/sal/pkg"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

func (cfg *OciArtifactRetrievalCmd) RunPull() error {
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

	remoteSnapshots, err := getSnapshots(manifest)
	if err != nil {
		return fmt.Errorf("error getting snapshot data from %s %w", cfg.Artifact, err)
	}

	localSnapshots, err := pkg.GetSalSnapshots()
	// if the error is that the table just doesn't exist yet, that is
	// ok since it will be created upon pull
	if !errors.Is(err, catalog.ErrNoSuchTable) {
		return err
	}

	err = canPull(localSnapshots, remoteSnapshots)
	if errors.Is(err, ErrNothingToPull) {
		slog.Info(err.Error())
		return nil
	} else if err != nil {
		return fmt.Errorf("skipping pull: %w", err)
	}

	dataDir, err := pkg.SalDataDir()
	if err != nil {
		return err
	}

	return pullManifestLayers(ctx, repo, manifest, desc, ref.reference, dataDir)
}
