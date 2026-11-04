package clone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cgs-earth/sal/pkg"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

func pull(ctx context.Context, src oras.ReadOnlyTarget, reference string, destination string) error {
	desc, manifest, err := pkg.FetchManifest(ctx, src, reference)
	if err != nil {
		return err
	}
	return pkg.PullManifestLayers(ctx, src, manifest, desc, reference, destination, 0)
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
	err = pkg.PullManifestLayers(ctx, store, manifest, ocispec.Descriptor{}, "latest", destination, 0)

	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(destination, "sal", "triples"))
	require.NoError(t, err)
	require.Equal(t, "triples", string(got))
	require.NoFileExists(t, filepath.Join(destination, "triples"))
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

// recordingRunner records every command it is asked to run, in the
// directory it was asked to run it in, and runs none.
func recordingRunner(calls *[]string) commandRunner {
	return func(ctx context.Context, dir string, name string, args ...string) error {
		*calls = append(*calls, fmt.Sprintf("%s:%s %s", dir, name, strings.Join(args, " ")))
		return nil
	}
}

func TestInitializeSALProjectPassesTheDescription(t *testing.T) {
	var calls []string

	require.NoError(t, initializeSALProject(context.Background(), "/tmp/sal", "A project", recordingRunner(&calls)))

	sal, err := os.Executable()
	require.NoError(t, err)
	require.Equal(t, []string{"/tmp/sal:" + sal + " init --description A project"}, calls)
}

func TestProjectDescriptionComesFromTheArtifactOrNamesTheRepository(t *testing.T) {
	carried := ocispec.Manifest{Annotations: map[string]string{pkg.SalDescriptionAnnotation: " Stream gauges "}}
	require.Equal(t, "Stream gauges", projectDescription(carried, "ghcr.io/org/gauges"))

	older := ocispec.Manifest{Annotations: map[string]string{sourceAnnotation: "https://github.com/org/gauges.git"}}
	require.Equal(t, "Data product pulled from ghcr.io/org/gauges", projectDescription(older, "ghcr.io/org/gauges"))
}

// A pull outside a project makes the empty current directory a project for
// the artifact: a git repository with the source as its origin, initialized
// with the artifact's description, whose data directory the pull then fills.
func TestInitializeEmptyDirectoryMakesTheDirectoryAProjectForTheArtifact(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	manifest := ocispec.Manifest{Annotations: map[string]string{
		sourceAnnotation:             "https://github.com/org/gauges.git",
		pkg.SalDescriptionAnnotation: "Stream gauges",
	}}

	dataDir, err := initializeEmptyDirectory(context.Background(), dir, manifest, "ghcr.io/org/gauges", recordingRunner(&calls))

	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, ".sal", "data"), dataDir)
	sal, err := os.Executable()
	require.NoError(t, err)
	require.Equal(t, []string{
		dir + ":git init",
		dir + ":git remote add origin https://github.com/org/gauges.git",
		dir + ":" + sal + " init --description Stream gauges",
	}, calls)
}

func TestInitializeEmptyDirectoryRefusesADirectoryHoldingAnything(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0644))
	var calls []string
	manifest := ocispec.Manifest{Annotations: map[string]string{sourceAnnotation: "https://github.com/org/gauges.git"}}

	_, err := initializeEmptyDirectory(context.Background(), dir, manifest, "ghcr.io/org/gauges", recordingRunner(&calls))

	require.ErrorIs(t, err, ErrDirectoryNotEmpty)
	require.Empty(t, calls)
}

func TestInitializeEmptyDirectoryNeedsTheSourceRepository(t *testing.T) {
	var calls []string

	_, err := initializeEmptyDirectory(context.Background(), t.TempDir(), ocispec.Manifest{}, "ghcr.io/org/gauges", recordingRunner(&calls))

	require.ErrorContains(t, err, sourceAnnotation)
	require.Empty(t, calls)
}
