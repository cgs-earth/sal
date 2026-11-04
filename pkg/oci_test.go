package pkg

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

// flakySource is a read-only target whose first fetch of every blob fails
// partway through the body, the way a registry drops a stream on a long
// pull, and whose later fetches succeed.
type flakySource struct {
	oras.ReadOnlyTarget
	mu      sync.Mutex
	dropped map[string]bool
	fetches int
}

func (f *flakySource) Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	rc, err := f.ReadOnlyTarget.Fetch(ctx, target)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if f.dropped[target.Digest.String()] {
		return rc, nil
	}
	f.dropped[target.Digest.String()] = true
	return &droppedStream{ReadCloser: rc}, nil
}

// droppedStream yields the first byte and then fails.
type droppedStream struct {
	io.ReadCloser
	read bool
}

func (d *droppedStream) Read(p []byte) (int, error) {
	if d.read {
		return 0, errors.New("stream error: PROTOCOL_ERROR; received from peer")
	}
	d.read = true
	return d.ReadCloser.Read(p[:1])
}

func TestPullManifestLayersFetchesALayerAgainWhenTheStreamDrops(t *testing.T) {
	previous := pullRetryBackoff
	pullRetryBackoff = 0
	t.Cleanup(func() { pullRetryBackoff = previous })
	ctx := context.Background()
	store := memory.New()
	var layers []ocispec.Descriptor
	for _, name := range []string{"one.txt", "two.txt"} {
		desc, err := oras.PushBytes(ctx, store, ocispec.MediaTypeImageLayer, []byte("content of "+name))
		require.NoError(t, err)
		desc.Annotations = map[string]string{ocispec.AnnotationTitle: name}
		layers = append(layers, desc)
	}
	source := &flakySource{ReadOnlyTarget: store, dropped: map[string]bool{}}
	destination := t.TempDir()

	err := PullManifestLayers(ctx, source, ocispec.Manifest{Layers: layers}, ocispec.Descriptor{}, "latest", destination, 0)

	require.NoError(t, err)
	require.Equal(t, 4, source.fetches, "every layer is fetched twice: once dropped, once whole")
	for _, name := range []string{"one.txt", "two.txt"} {
		content, err := os.ReadFile(filepath.Join(destination, name))
		require.NoError(t, err)
		require.Equal(t, "content of "+name, string(content))
	}
}

func TestPullManifestLayersGivesUpOnALayerThatKeepsFailing(t *testing.T) {
	previous := pullRetryBackoff
	pullRetryBackoff = 0
	t.Cleanup(func() { pullRetryBackoff = previous })
	ctx := context.Background()
	store := memory.New()
	desc, err := oras.PushBytes(ctx, store, ocispec.MediaTypeImageLayer, []byte("content"))
	require.NoError(t, err)
	desc.Annotations = map[string]string{ocispec.AnnotationTitle: "one.txt"}
	source := &alwaysDropping{ReadOnlyTarget: store}

	err = PullManifestLayers(ctx, source, ocispec.Manifest{Layers: []ocispec.Descriptor{desc}}, ocispec.Descriptor{}, "latest", t.TempDir(), 0)

	require.ErrorContains(t, err, "write layer one.txt")
	require.ErrorContains(t, err, "after 4 attempts")
	require.Equal(t, pullAttempts, source.fetches)
}

// alwaysDropping drops every stream.
type alwaysDropping struct {
	oras.ReadOnlyTarget
	mu      sync.Mutex
	fetches int
}

func (a *alwaysDropping) Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	rc, err := a.ReadOnlyTarget.Fetch(ctx, target)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.fetches++
	a.mu.Unlock()
	return &droppedStream{ReadCloser: rc}, nil
}

func TestParseArtifactDefaultsReferenceToLatest(t *testing.T) {
	ref, err := ParseArtifact("ghcr.io/my-username/my-repository")

	require.NoError(t, err)
	require.Equal(t, "ghcr.io/my-username/my-repository", ref.Repository)
	require.Equal(t, "latest", ref.Reference)
	require.Equal(t, "ghcr.io", ref.RegistryName)
	require.Equal(t, "my-username", ref.Owner)
	require.Equal(t, "my-repository", ref.ArtifactName)
}

func TestParseArtifactStripsHTTPScheme(t *testing.T) {
	ref, err := ParseArtifact("https://ghcr.io/my-username/my-repository:v1")

	require.NoError(t, err)
	require.Equal(t, "ghcr.io/my-username/my-repository", ref.Repository)
	require.Equal(t, "v1", ref.Reference)
	require.Equal(t, "ghcr.io", ref.RegistryName)
	require.Equal(t, "my-username", ref.Owner)
}

func TestRepoDirFromSourceHandlesSSHURLs(t *testing.T) {
	got := RepoDirFromSource("git@github.com:cgs-earth/sal.git")

	require.Equal(t, "sal", got)
}

func TestRepoDirFromSourceHandlesHTTPSURLs(t *testing.T) {
	got := RepoDirFromSource("https://github.com/cgs-earth/sal.git")

	require.Equal(t, "sal", got)
}
