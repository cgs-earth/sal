package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"

	"github.com/apache/iceberg-go/catalog"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const DefaultAssumedRegistry = "ghcr.io"

const MaxConcurrentPulls = 8

type CmdWithAuth interface {
	GetUsername() string
	GetPassword() string
}

type OciArtifactCmdWithAuth interface {
	CmdWithAuth
	GetArtifactReference() (ArtifactReference, error)
}

func FetchManifest(ctx context.Context, src oras.ReadOnlyTarget, reference string) (ocispec.Descriptor, ocispec.Manifest, error) {
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

func RepoDirFromSource(source string) string {
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

type ArtifactReference struct {
	// ghcr.io/cgs-earth/sal
	Repository string
	// example latest
	Reference string
	// example ghcr.io
	RegistryName string
	// example cgs-earth
	Owner string
	// example sal
	ArtifactName string
}

func GuessDefaultArtifact() (string, error) {
	gitProjectName, err := GitProjectName()
	if err != nil {
		return "", err
	}
	gitProjectOwner, err := GitProjectOwner()
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s/%s/%s:latest", DefaultAssumedRegistry, gitProjectOwner, gitProjectName), nil
}

func ParseArtifact(artifact string) (ArtifactReference, error) {

	if artifact == "" {
		var err error
		artifact, err = GuessDefaultArtifact()
		if err != nil {
			return ArtifactReference{}, err
		}
	}

	artifact = strings.TrimPrefix(artifact, "https://")
	artifact = strings.TrimPrefix(artifact, "http://")

	ref, err := registry.ParseReference(artifact)
	if err != nil {
		return ArtifactReference{}, fmt.Errorf("invalid OCI artifact reference: %w", err)
	}

	owner, _, _ := strings.Cut(ref.Repository, "/")
	artifactName := RepoDirFromSource(ref.Repository)
	return ArtifactReference{
		Repository:   ref.Registry + "/" + ref.Repository,
		Reference:    ref.ReferenceOrDefault(),
		RegistryName: ref.Registry,
		Owner:        owner,
		ArtifactName: artifactName,
	}, nil
}

func NewOciClientWithOptionalAuth(cmd CmdWithAuth, ref ArtifactReference) *auth.Client {
	username := cmd.GetUsername()
	password := cmd.GetPassword()
	if password != "" || username != "" {
		if username == "" && password != "" {
			username = ref.Owner
		}

		credential := auth.Credential{
			Username: username,
			Password: password,
		}
		return &auth.Client{
			Client:     retry.DefaultClient,
			Cache:      auth.NewCache(),
			Credential: auth.StaticCredential(ref.RegistryName, credential),
		}
	}
	return auth.DefaultClient
}

func FetchAndDiffSnapshots(repo *remote.Repository, reference string) (SnapshotDiffReport, error) {
	return fetchAndDiffSnapshots(repo, reference, GetLocalSalSnapshots)
}

func fetchAndDiffSnapshots(src oras.ReadOnlyTarget, reference string, getLocalSnapshots func() ([]string, error)) (SnapshotDiffReport, error) {
	ctx := context.Background()
	_, manifest, err := FetchManifest(ctx, src, reference)
	if err != nil {
		return SnapshotDiffReport{}, err
	}

	remoteSnapshots, err := GetSnapshotsFromManifest(manifest)
	if err != nil {
		return SnapshotDiffReport{}, err
	}

	localSnapshots, err := getLocalSnapshots()
	// if the error is that the table just doesn't exist yet, that is
	// ok since it will be created upon pull
	if err != nil && !errors.Is(err, catalog.ErrNoSuchTable) {
		return SnapshotDiffReport{}, err
	}

	return SnapshotDiff(localSnapshots, remoteSnapshots)
}

// PullManifestLayers copies an OCI artifact into destination, restoring layers
// according to their exact org.opencontainers.image.title annotations. It backs
// `sal clone`, `sal pull`, and the OCI artifacts `sal import` records.
func PullManifestLayers(ctx context.Context, src oras.ReadOnlyTarget, manifest ocispec.Manifest, desc ocispec.Descriptor, reference string, destination string) error {
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
	group.SetLimit(MaxConcurrentPulls)
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
		return fmt.Errorf("artifact %s has no layers with %s annotations", reference, ocispec.AnnotationTitle)
	}

	slog.Info("Pulled "+reference+" to "+destination, "digest", desc.Digest.String(), "files", pulledFiles.Load())
	return nil
}
