package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/apache/iceberg-go/catalog"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const DefaultAssumedRegistry = "ghcr.io"

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
