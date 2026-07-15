package clone

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/cgs-earth/sal/push"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func getSnapshots(manifest ocispec.Manifest) ([]string, error) {
	for key, value := range manifest.Annotations {
		if key == push.SalIcebergSnapshotsAnnotation {
			return strings.Split(value, ","), nil
		}
	}
	return nil, fmt.Errorf("no snapshot metadata '%s' found in data product", push.SalIcebergSnapshotsAnnotation)
}

var ErrLocalHasDiverged = fmt.Errorf("local data product has diverged from remote")
var ErrNothingToPull = fmt.Errorf("no snapshots to pull")

func canPull(localSnapshots []string, remoteSnapshots []string) error {

	slog.Debug("snapshots", "local", localSnapshots, "remote", remoteSnapshots)
	// fmt.Println(strings.Join(remoteSnapshots, ","))

	n := min(len(localSnapshots), len(remoteSnapshots))

	for i := range n {
		if localSnapshots[i] != remoteSnapshots[i] {
			return fmt.Errorf("%w: snapshot mismatch at index %d. Found local snapshot %s but remote snapshot %s", ErrLocalHasDiverged, i, localSnapshots[i], remoteSnapshots[i])
		}
	}

	if len(localSnapshots) > len(remoteSnapshots) {
		return fmt.Errorf("%w: local is ahead of remote by %d snapshots", ErrNothingToPull, len(localSnapshots)-len(remoteSnapshots))
	}
	if len(localSnapshots) == len(remoteSnapshots) {
		return fmt.Errorf("%w: found the same %d snapshots both locally and remote", ErrNothingToPull, len(remoteSnapshots))
	}

	return nil
}
