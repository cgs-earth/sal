package clone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apache/iceberg-go/catalog"
	"github.com/cgs-earth/sal/pkg"
	"oras.land/oras-go/v2/registry/remote"
)

func (cmd *OciArtifactRetrievalCmd) RunPull() error {
	ctx := context.Background()

	ref, err := cmd.GetArtifactReference()
	if err != nil {
		return err
	}
	pkg.Infof("Starting pull of %s", ref.Repository)

	repo, err := remote.NewRepository(ref.Repository)
	if err != nil {
		return fmt.Errorf("failed creating OCI registry client: %w", err)
	}

	repo.Client = pkg.NewOciClientWithOptionalAuth(cmd, ref)

	desc, manifest, err := pkg.FetchManifest(ctx, repo, ref.Reference)
	if err != nil {
		return err
	}

	remoteSnapshots, err := pkg.GetSnapshotsFromManifest(manifest)
	if err != nil {
		return fmt.Errorf("error getting snapshot data from %s %w", cmd.Artifact, err)
	}

	localSnapshots, err := pkg.GetLocalSalSnapshots()
	// if the error is that the table just doesn't exist yet, that is
	// ok since it will be created upon pull
	if errors.Is(err, catalog.ErrNoSuchTable) {
		slog.Info("No local SAL data product found; pulling remote data product")
		localSnapshots = nil
	} else if err != nil {
		return err
	}

	_, err = pkg.SnapshotDiff(localSnapshots, remoteSnapshots)
	if errors.Is(err, pkg.ErrNothingToPull) {
		pkg.Infof("Skipping pull: %s", err.Error())
		return nil
	} else if err != nil {
		return fmt.Errorf("skipping pull: %w", err)
	}

	dataDir, err := pkg.SalDataDir()
	if err != nil {
		return err
	}

	slog.Info("Pulling data product", "destination", dataDir)
	return pullManifestLayers(ctx, repo, manifest, desc, ref.Reference, dataDir)
}
