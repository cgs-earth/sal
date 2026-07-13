package clone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

func pull(ctx context.Context, src oras.ReadOnlyTarget, reference string, destination string) error {
	desc, manifest, err := fetchManifest(ctx, src, reference)
	if err != nil {
		return err
	}
	return pullManifestLayers(ctx, src, manifest, desc, reference, destination)
}

func TestPullRestoresArtifactFilesToDestination(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	rootDesc, err := oras.PushBytes(ctx, store, ocispec.MediaTypeImageLayer, []byte("root"))
	require.NoError(t, err)
	rootDesc.Annotations = map[string]string{
		ocispec.AnnotationTitle: "root.txt",
	}

	nestedDesc, err := oras.PushBytes(ctx, store, ocispec.MediaTypeImageLayer, []byte("nested"))
	require.NoError(t, err)
	nestedDesc.Annotations = map[string]string{
		ocispec.AnnotationTitle: "nested/data.txt",
	}

	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, "application/octet-stream", oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{rootDesc, nestedDesc},
		ConfigDescriptor: &ocispec.Descriptor{
			MediaType: "application/vnd.cgs-earth.sal.config.v1+json",
		},
	})
	require.NoError(t, err)
	require.NoError(t, store.Tag(ctx, manifestDesc, "latest"))

	destination := t.TempDir()
	err = pull(ctx, store, "latest", destination)

	require.NoError(t, err)
	rootBytes, err := os.ReadFile(filepath.Join(destination, "root.txt"))
	require.NoError(t, err)
	require.Equal(t, "root", string(rootBytes))
	nestedBytes, err := os.ReadFile(filepath.Join(destination, "nested", "data.txt"))
	require.NoError(t, err)
	require.Equal(t, "nested", string(nestedBytes))
}

func TestPullManifestLayersPreservesArtifactNamePrefix(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	layer, err := oras.PushBytes(ctx, store, ocispec.MediaTypeImageLayer, []byte("triples"))
	require.NoError(t, err)
	layer.Annotations = map[string]string{
		ocispec.AnnotationTitle: "sal/triples",
	}

	manifest := ocispec.Manifest{
		Layers: []ocispec.Descriptor{layer},
	}
	destination := t.TempDir()
	err = pullManifestLayers(ctx, store, manifest, ocispec.Descriptor{}, "latest", destination)

	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(destination, "sal", "triples"))
	require.NoError(t, err)
	require.Equal(t, "triples", string(got))
	require.NoFileExists(t, filepath.Join(destination, "triples"))
}

func TestParseArtifactDefaultsReferenceToLatest(t *testing.T) {
	ref, err := parseArtifact("ghcr.io/my-username/my-repository")

	require.NoError(t, err)
	require.Equal(t, "ghcr.io/my-username/my-repository", ref.repository)
	require.Equal(t, "latest", ref.reference)
	require.Equal(t, "ghcr.io", ref.registryName)
	require.Equal(t, "my-username", ref.owner)
	require.Equal(t, "my-repository", ref.artifactName)
}

func TestParseArtifactStripsHTTPScheme(t *testing.T) {
	ref, err := parseArtifact("https://ghcr.io/my-username/my-repository:v1")

	require.NoError(t, err)
	require.Equal(t, "ghcr.io/my-username/my-repository", ref.repository)
	require.Equal(t, "v1", ref.reference)
	require.Equal(t, "ghcr.io", ref.registryName)
	require.Equal(t, "my-username", ref.owner)
}

func TestCredentialFromConfigInfersUsernameFromArtifactOwner(t *testing.T) {
	ref, err := parseArtifact("ghcr.io/cgs-earth/sal:latest")
	require.NoError(t, err)

	credential := credentialFromConfig(&CloneCmd{Password: "token"}, ref)

	require.Equal(t, "cgs-earth", credential.Username)
	require.Equal(t, "token", credential.Password)
}

func TestCredentialFromConfigUsesExplicitUsername(t *testing.T) {
	ref, err := parseArtifact("ghcr.io/cgs-earth/sal:latest")
	require.NoError(t, err)

	credential := credentialFromConfig(&CloneCmd{Username: "octocat", Password: "token"}, ref)

	require.Equal(t, "octocat", credential.Username)
	require.Equal(t, "token", credential.Password)
}

func TestMetadataFromManifestReadsSourceAndCommitAnnotations(t *testing.T) {
	metadata, err := metadataFromManifest(ocispec.Manifest{
		Annotations: map[string]string{
			sourceAnnotation:    "https://github.com/cgs-earth/sal.git",
			gitCommitAnnotation: "abc123",
		},
	})

	require.NoError(t, err)
	require.Equal(t, "https://github.com/cgs-earth/sal.git", metadata.source)
	require.Equal(t, "abc123", metadata.gitCommit)
}

func TestMetadataFromManifestReadsCommitFromConfigAnnotations(t *testing.T) {
	metadata, err := metadataFromManifest(ocispec.Manifest{
		Annotations: map[string]string{
			sourceAnnotation: "https://github.com/cgs-earth/sal.git",
		},
		Config: ocispec.Descriptor{
			Annotations: map[string]string{
				gitCommitAnnotation: "abc123",
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "https://github.com/cgs-earth/sal.git", metadata.source)
	require.Equal(t, "abc123", metadata.gitCommit)
}

func TestMetadataFromManifestRequiresSource(t *testing.T) {
	_, err := metadataFromManifest(ocispec.Manifest{
		Annotations: map[string]string{
			gitCommitAnnotation: "abc123",
		},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), sourceAnnotation)
}

func TestMetadataFromManifestRequiresCommit(t *testing.T) {
	_, err := metadataFromManifest(ocispec.Manifest{
		Annotations: map[string]string{
			sourceAnnotation: "https://github.com/cgs-earth/sal.git",
		},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), gitCommitAnnotation)
}

func TestRepoDirFromSourceHandlesHTTPSURLs(t *testing.T) {
	got := repoDirFromSource("https://github.com/cgs-earth/sal.git")

	require.Equal(t, "sal", got)
}

func TestRepoDirFromSourceHandlesSSHURLs(t *testing.T) {
	got := repoDirFromSource("git@github.com:cgs-earth/sal.git")

	require.Equal(t, "sal", got)
}

func TestCloneRepositoryUsesExplicitDestination(t *testing.T) {
	var calls []string
	run := func(ctx context.Context, dir string, name string, args ...string) error {
		calls = append(calls, fmt.Sprintf("%s:%s %s", dir, name, strings.Join(args, " ")))
		return nil
	}

	repoDir, err := cloneRepository(context.Background(), "https://github.com/cgs-earth/sal.git", "/tmp/sal", run)

	require.NoError(t, err)
	require.Equal(t, "/tmp/sal", repoDir)
	require.Equal(t, []string{":git clone https://github.com/cgs-earth/sal.git /tmp/sal"}, calls)
}

func TestCheckoutRepositoryRunsInClonedRepository(t *testing.T) {
	var calls []string
	run := func(ctx context.Context, dir string, name string, args ...string) error {
		calls = append(calls, fmt.Sprintf("%s:%s %s", dir, name, strings.Join(args, " ")))
		return nil
	}

	err := checkoutRepository(context.Background(), "/tmp/sal", "abc123", run)

	require.NoError(t, err)
	require.Equal(t, []string{"/tmp/sal:git checkout abc123"}, calls)
}
