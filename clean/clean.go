package clean

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
	"oras.land/oras-go/v2/registry/remote"
)

// DeleteSnapshots removes the given snapshots from Iceberg table metadata and
// rolls the main branch back when the current snapshot is being removed.
func DeleteSnapshots(tbl *table.Table, cat catalog.Catalog, snapshotIDs []string) error {
	if len(snapshotIDs) == 0 {
		return nil
	}

	ids, err := parseSnapshotIDs(snapshotIDs)
	if err != nil {
		return err
	}

	metadata := tbl.Metadata()
	snapshots := metadata.Snapshots()
	if len(ids) >= len(snapshots) {
		return fmt.Errorf("cannot delete every snapshot in the table; use --wipe to remove the data product")
	}

	removeSet := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if tbl.SnapshotByID(id) == nil {
			return fmt.Errorf("snapshot %d not found in local table", id)
		}
		removeSet[id] = struct{}{}
	}

	currentSnapshot := tbl.CurrentSnapshot()
	if currentSnapshot == nil {
		return fmt.Errorf("cannot delete snapshots from a table with no current snapshot")
	}

	var updates []table.Update
	var requirements []table.Requirement
	currentSnapshotID := currentSnapshot.SnapshotID
	requirements = append(requirements, table.AssertRefSnapshotID(table.MainBranch, &currentSnapshotID))

	if _, removingCurrent := removeSet[currentSnapshot.SnapshotID]; removingCurrent {
		var rollbackSnapshotID int64
		for i := len(snapshots) - 1; i >= 0; i-- {
			if _, removing := removeSet[snapshots[i].SnapshotID]; !removing {
				rollbackSnapshotID = snapshots[i].SnapshotID
				break
			}
		}
		if rollbackSnapshotID == 0 {
			return fmt.Errorf("cannot find a surviving snapshot to make current")
		}
		updates = append(updates, table.NewSetSnapshotRefUpdate(table.MainBranch, rollbackSnapshotID, table.BranchRef, 0, 0, 0))
	}

	updates = append(updates, table.NewRemoveSnapshotsUpdate(ids, true))

	_, _, err = cat.CommitTable(context.Background(), tbl.Identifier(), requirements, updates)
	if err != nil {
		return fmt.Errorf("delete snapshots: %w", err)
	}

	slog.Info("Deleted Iceberg snapshots", "snapshots", snapshotIDs)
	return nil
}

// SquashSnapshots replaces local-only snapshots with one new metadata snapshot
// that points at the latest local table state and uses the remote head as parent.
func SquashSnapshots(tbl *table.Table, cat catalog.Catalog, localOnlySnapshotIDs []string, remoteSnapshotIDs []string) error {
	if len(localOnlySnapshotIDs) < 2 {
		return nil
	}

	ids, err := parseSnapshotIDs(localOnlySnapshotIDs)
	if err != nil {
		return err
	}

	var parentSnapshotID *int64
	if len(remoteSnapshotIDs) > 0 {
		remoteIDs, err := parseSnapshotIDs(remoteSnapshotIDs[len(remoteSnapshotIDs)-1:])
		if err != nil {
			return err
		}
		if tbl.SnapshotByID(remoteIDs[0]) == nil {
			return fmt.Errorf("remote head snapshot %d not found in local table", remoteIDs[0])
		}
		parentSnapshotID = &remoteIDs[0]
	}

	latestSnapshot := tbl.SnapshotByID(ids[len(ids)-1])
	if latestSnapshot == nil {
		return fmt.Errorf("snapshot %d not found in local table", ids[len(ids)-1])
	}
	for _, id := range ids[:len(ids)-1] {
		if tbl.SnapshotByID(id) == nil {
			return fmt.Errorf("snapshot %d not found in local table", id)
		}
	}

	currentSnapshot := tbl.CurrentSnapshot()
	if currentSnapshot == nil {
		return fmt.Errorf("cannot squash snapshots from a table with no current snapshot")
	}

	newSnapshot := *latestSnapshot
	newSnapshot.SnapshotID = time.Now().UnixNano()
	for newSnapshot.SnapshotID <= 0 || tbl.SnapshotByID(newSnapshot.SnapshotID) != nil {
		newSnapshot.SnapshotID++
	}
	newSnapshot.ParentSnapshotID = parentSnapshotID
	newSnapshot.SequenceNumber = tbl.Metadata().LastSequenceNumber() + 1
	newSnapshot.TimestampMs = time.Now().UnixMilli()

	currentSnapshotID := currentSnapshot.SnapshotID
	requirements := []table.Requirement{table.AssertRefSnapshotID(table.MainBranch, &currentSnapshotID)}
	updates := []table.Update{
		table.NewAddSnapshotUpdate(&newSnapshot),
		table.NewSetSnapshotRefUpdate(table.MainBranch, newSnapshot.SnapshotID, table.BranchRef, 0, 0, 0),
		table.NewRemoveSnapshotsUpdate(ids, false),
	}

	_, _, err = cat.CommitTable(context.Background(), tbl.Identifier(), requirements, updates)
	if err != nil {
		return fmt.Errorf("squash snapshots: %w", err)
	}

	slog.Info("Squashed Iceberg snapshots", "removed_snapshots", localOnlySnapshotIDs, "squashed_snapshot", newSnapshot.SnapshotID)
	return nil
}

func parseSnapshotIDs(snapshotIDs []string) ([]int64, error) {
	ids := make([]int64, 0, len(snapshotIDs))
	seen := map[int64]struct{}{}
	for _, snapshotID := range snapshotIDs {
		id, err := strconv.ParseInt(strings.TrimSpace(snapshotID), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid snapshot ID %q", snapshotID)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func confirm(prompt string) (bool, error) {

	pkg.Warnf("%s [y/N]", prompt)

	var input string
	if _, err := fmt.Scanln(&input); err != nil {
		// If user just presses enter, Scanln errors → treat as "no"
		return false, nil
	}

	input = strings.ToLower(strings.TrimSpace(input))
	return input == "y" || input == "yes", nil
}

type CleanCmd struct {
	Wipe     bool   `arg:"--wipe,-w" help:"Wipe the entire data product. Useful for debugging and testing purposes"`
	Squash   bool   `arg:"--squash" help:"Condense local snapshots ahead of the remote artifact into one new snapshot"`
	Username string `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry"`
	Password string `arg:"--password,env:OCI_PASSWORD" help:"Password for the OCI registry"`
	Artifact string `arg:"positional" help:"Full URL of the OCI artifact to diff against. Example: ghcr.io/my-username/my-repository:latest"`
}

func (cmd *CleanCmd) GetUsername() string {
	return cmd.Username
}

func (cmd *CleanCmd) GetPassword() string {
	return cmd.Password
}

func (cmd *CleanCmd) GetArtifactReference() (pkg.ArtifactReference, error) {
	return pkg.ParseArtifact(cmd.Artifact)
}

func wipe() error {
	ok, err := confirm("This will permanently delete the entire data product. Continue?")
	if err != nil {
		return err
	}

	if !ok {
		slog.Info("Wipe cancelled")
		return nil
	}

	dataProductPath, err := pkg.SalBuiltDataProductPath()
	if err != nil {
		return err
	}

	var totalBytes int64

	err = filepath.WalkDir(dataProductPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		totalBytes += info.Size()
		return nil
	})

	if errors.Is(err, os.ErrNotExist) {
		slog.Warn("No data product to clean at " + dataProductPath)
		return nil
	}
	if err != nil {
		return err
	}

	if err := os.RemoveAll(dataProductPath); err != nil {
		return err
	}

	msg := fmt.Sprintf("Removed %s of data artifacts from %s", pkg.BytesToHumanReadable(totalBytes), dataProductPath)

	slog.Info(msg)

	return nil
}

func cleanDiff(cmd *CleanCmd) error {
	ctx := context.Background()

	ref, err := cmd.GetArtifactReference()
	if err != nil {
		return err
	}

	repo, err := remote.NewRepository(ref.Repository)
	if err != nil {
		return fmt.Errorf("failed creating OCI registry client: %w", err)
	}

	repo.Client = pkg.NewOciClientWithOptionalAuth(cmd, ref)

	_, manifest, err := pkg.FetchManifest(ctx, repo, ref.Reference)
	if err != nil {
		return err
	}

	remoteSnapshots, err := pkg.GetSnapshotsFromManifest(manifest)
	if err != nil {
		return fmt.Errorf("error getting snapshot data from %s %w", cmd.Artifact, err)
	}

	cat, err := pkg.SalIcebergCatalog()
	if err != nil {
		return err
	}

	tbl, err := pkg.GetSalIcebergTable()
	// if the error is that the table just doesn't exist yet, that is
	// ok since it will be created upon pull
	if errors.Is(err, catalog.ErrNoSuchTable) {
		slog.Warn("No SAL data product to clean")
		return nil
	} else if err != nil {
		return err
	}

	localSnapshots := make([]string, 0, len(tbl.Metadata().Snapshots()))
	for _, snapshot := range tbl.Metadata().Snapshots() {
		localSnapshots = append(localSnapshots, fmt.Sprintf("%d", snapshot.SnapshotID))
	}

	diff, _ := pkg.SnapshotDiff(localSnapshots, remoteSnapshots)

	if cmd.Squash {
		if len(diff.SnapshotsInRemoteNotLocal) > 0 {
			return fmt.Errorf("cannot squash local snapshots because the remote has snapshot(s) not present locally: %s", strings.Join(diff.SnapshotsInRemoteNotLocal, ", "))
		}
		if len(diff.SnapshotsInLocalNotRemote) < 2 {
			slog.Info("Nothing to squash; local table has fewer than two snapshots ahead of remote")
			return nil
		}

		latestLocalSnapshot := diff.SnapshotsInLocalNotRemote[len(diff.SnapshotsInLocalNotRemote)-1]
		msg := fmt.Sprintf("Found %d snapshot(s) in local but not remote: %s. Squash them into one new snapshot based on %s?", len(diff.SnapshotsInLocalNotRemote), strings.Join(diff.SnapshotsInLocalNotRemote, ", "), latestLocalSnapshot)
		ok, err := confirm(msg)
		if err != nil {
			return err
		}

		if !ok {
			slog.Info("Squash cancelled")
			return nil
		}

		return SquashSnapshots(tbl, cat, diff.SnapshotsInLocalNotRemote, remoteSnapshots)
	}

	if len(diff.SnapshotsInLocalNotRemote) > 0 {
		msg := fmt.Sprintf("Found %d snapshot(s) in local but not remote: %s. Delete them permanently?", len(diff.SnapshotsInLocalNotRemote), strings.Join(diff.SnapshotsInLocalNotRemote, ", "))
		ok, err := confirm(msg)
		if err != nil {
			return err
		}

		if !ok {
			slog.Info("Wipe cancelled")
			return nil
		}

		return DeleteSnapshots(tbl, cat, diff.SnapshotsInLocalNotRemote)
	} else {
		slog.Info("Nothing to clean; local table is the same as remote; Use --wipe to remove the entire data product")
	}

	return nil

}

func (cmd *CleanCmd) Run() error {

	if cmd.Wipe {
		return wipe()
	} else {
		return cleanDiff(cmd)
	}
}
