package pkg

import (
	"fmt"
	"log/slog"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const SalIcebergSnapshotsAnnotation = "sal.iceberg-snapshots"

func GetSnapshotsFromManifest(manifest ocispec.Manifest) ([]string, error) {
	for key, value := range manifest.Annotations {
		if key == SalIcebergSnapshotsAnnotation {
			return strings.Split(value, ","), nil
		}
	}
	return nil, fmt.Errorf("no snapshot metadata '%s' found in data product", SalIcebergSnapshotsAnnotation)
}

var ErrLocalHasDiverged = fmt.Errorf("local data product has diverged from remote")
var ErrNothingToPull = fmt.Errorf("no snapshots to pull")

type SnapshotDiffReport struct {
	Diverged                  bool
	SnapshotsInRemoteNotLocal []string
	SnapshotsInLocalNotRemote []string
}

func SnapshotDiff(localSnapshots []string, remoteSnapshots []string) (SnapshotDiffReport, error) {

	slog.Debug("snapshots", "local", localSnapshots, "remote", remoteSnapshots)

	n := min(len(localSnapshots), len(remoteSnapshots))

	for i := range n {
		if localSnapshots[i] != remoteSnapshots[i] {
			return SnapshotDiffReport{Diverged: true, SnapshotsInRemoteNotLocal: remoteSnapshots[i:], SnapshotsInLocalNotRemote: localSnapshots[i:]}, fmt.Errorf("%w: snapshot mismatch at index %d. Found local snapshot %s but remote snapshot %s", ErrLocalHasDiverged, i, localSnapshots[i], remoteSnapshots[i])
		}
	}

	if len(localSnapshots) > len(remoteSnapshots) {
		return SnapshotDiffReport{Diverged: true, SnapshotsInLocalNotRemote: localSnapshots[len(remoteSnapshots):]}, fmt.Errorf("%w: local is ahead of remote by %d snapshots", ErrNothingToPull, len(localSnapshots)-len(remoteSnapshots))
	}
	if len(localSnapshots) == len(remoteSnapshots) {
		return SnapshotDiffReport{}, fmt.Errorf("%w: found the same %d snapshots both locally and remote", ErrNothingToPull, len(remoteSnapshots))
	}

	return SnapshotDiffReport{SnapshotsInRemoteNotLocal: remoteSnapshots[len(localSnapshots):]}, nil
}

func GetLocalSalSnapshots() ([]string, error) {

	tbl, err := GetSalIcebergTable()
	if err != nil {
		return nil, err
	}
	var snapshots []string
	for _, s := range tbl.Metadata().Snapshots() {
		snapshots = append(snapshots, fmt.Sprintf("%d", s.SnapshotID))
	}
	return snapshots, nil
}
