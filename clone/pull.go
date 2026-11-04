package clone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/apache/iceberg-go/catalog"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"oras.land/oras-go/v2/registry/remote"
)

// RunPull pulls the snapshots the remote artifact has that the local data
// product lacks. Outside a SAL project it initializes one in the current
// directory, which must be empty, from what the artifact's manifest records,
// so a data product can be pulled somewhere new without `sal init` first.
// The invocation is one trace, `sal.pull`, which the progress pane follows:
// the manifest, then the layers as they arrive.
func (cmd *OciArtifactRetrievalCmd) RunPull() (err error) {
	ref, err := cmd.GetArtifactReference()
	if err != nil {
		return err
	}
	ctx, span := telemetry.Start(context.Background(), "sal.pull", attribute.String("sal.oci.repository", ref.Repository))
	defer func() { telemetry.End(span, err) }()
	pkg.Infof("Starting pull of %s", ref.Repository)

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

	remoteSnapshots, err := pkg.GetSnapshotsFromManifest(manifest)
	if err != nil {
		return fmt.Errorf("error getting snapshot data from %s %w", cmd.Artifact, err)
	}

	// a directory that is no project yet becomes one for the artifact, so
	// that the local snapshots and the data directory below are then found
	dataDir, err := pkg.SalDataDir()
	if errors.Is(err, pkg.ErrSalDirNotFound) {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		initCtx, initSpan := telemetry.Start(ctx, "clone.init", attribute.String("sal.clone.directory", cwd))
		dataDir, err = initializeEmptyDirectory(initCtx, cwd, manifest, ref.Repository, runCommand)
		telemetry.End(initSpan, err)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
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

	slog.Info("Pulling data product", "destination", dataDir)
	return pkg.PullManifestLayers(ctx, repo, manifest, desc, ref.Reference, dataDir, cmd.Parallel)
}
