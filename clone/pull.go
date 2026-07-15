package clone

import (
	"context"
	"fmt"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/push"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

func getSnapshots(manifest ocispec.Manifest) ([]string, error) {
	for key, value := range manifest.Annotations {
		if key == push.SalIcebergSnapshotsAnnotation {
			return strings.Split(value, ","), nil
		}
	}
	return nil, fmt.Errorf("no snapshot metadata found in data product")
}

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

	snapshots, err := getSnapshots(manifest)
	if err != nil {
		return err
	}

	fmt.Printf("%v", snapshots)

	dataDir, err := pkg.SalDataDir()
	if err != nil {
		return err
	}

	return pullManifestLayers(ctx, repo, manifest, desc, ref.reference, dataDir)
}
