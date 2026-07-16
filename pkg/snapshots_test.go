package pkg

import (
	"context"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

func TestSnapshotDiffReportsRemoteSnapshotsMissingLocally(t *testing.T) {
	report, err := SnapshotDiff(
		[]string{"4848026723551914760", "4941839245415841596"},
		[]string{"4848026723551914760", "4941839245415841596", "1654723299241951698"},
	)

	require.NoError(t, err)
	require.False(t, report.Diverged)
	require.Equal(t, []string{"1654723299241951698"}, report.SnapshotsInRemoteNotLocal)
	require.Empty(t, report.SnapshotsInLocalNotRemote)
}

func TestSnapshotDiffReportsRemoteSnapshotsWhenHistoriesDivergeImmediately(t *testing.T) {
	report, err := SnapshotDiff(
		[]string{"4848026723551914760", "4941839245415841596"},
		[]string{"1654723299241951698", "9104382435375494597", "1699607284277310357"},
	)

	require.ErrorIs(t, err, ErrLocalHasDiverged)
	require.True(t, report.Diverged)
	require.Equal(t, []string{"1654723299241951698", "9104382435375494597", "1699607284277310357"}, report.SnapshotsInRemoteNotLocal)
	require.Equal(t, []string{"4848026723551914760", "4941839245415841596"}, report.SnapshotsInLocalNotRemote)
}

func TestFetchAndDiffSnapshotsUsesLocalSnapshotsWhenReadSucceeds(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, "application/octet-stream", oras.PackManifestOptions{
		ManifestAnnotations: map[string]string{
			SalIcebergSnapshotsAnnotation: "4848026723551914760,4941839245415841596,1654723299241951698",
		},
		ConfigDescriptor: &ocispec.Descriptor{
			MediaType: "application/vnd.cgs-earth.sal.config.v1+json",
		},
	})
	require.NoError(t, err)
	require.NoError(t, store.Tag(ctx, manifestDesc, "latest"))

	report, err := fetchAndDiffSnapshots(store, "latest", func() ([]string, error) {
		return []string{"4848026723551914760", "4941839245415841596"}, nil
	})

	require.NoError(t, err)
	require.Equal(t, []string{"1654723299241951698"}, report.SnapshotsInRemoteNotLocal)
}
