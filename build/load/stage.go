package load

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg/telemetry"
	rdflibgo "github.com/tggo/goRDFlib"
	"go.opentelemetry.io/otel/attribute"
)

// StagingBranch is the Iceberg branch a graph is staged on. It exists only
// while a build's CONSTRUCT queries run; main never points at a snapshot
// committed to it.
const StagingBranch = "sal-staging"

// StagedGraph is a graph written to a triples table without being committed
// to its main branch, so that it can be queried before the build that holds
// it is complete. Only what differs from the table is written, and Commit
// reuses those data files rather than writing them again.
type StagedGraph struct {
	// TablePath is the root of the table the graph is staged in.
	TablePath string
	// SnapshotID is the snapshot that holds the staged graph. It is the
	// table's current snapshot when the graph is what the table already holds.
	SnapshotID int64

	cat   catalog.Catalog
	ident table.Identifier
	cfg   *LoadConfig
	// dataFiles hold the triples in hashes, the ones the table lacked
	dataFiles []iceberg.DataFile
	hashes    map[string]struct{}
	// onBranch is whether a snapshot was committed to the staging branch
	onBranch bool
	// createdTable is whether staging is what created the table
	createdTable bool
	discarded    bool
}

// StageGraph writes what of a graph the triples table does not hold yet, and
// the deletes for what the table holds that the graph does not, as a snapshot
// on the staging branch. The main branch is left where it was. A graph the
// table already holds is staged as the table's current snapshot, with nothing
// written.
func StageGraph(ctx context.Context, graph *rdflibgo.Graph, cfg *LoadConfig) (_ *StagedGraph, err error) {
	if graph == nil || cfg == nil {
		return nil, fmt.Errorf("stage graph: missing arguments")
	}
	ctx, span := telemetry.Start(ctx, "iceberg.stage", attribute.String("sal.iceberg.warehouse", cfg.Warehouse), attribute.String("sal.iceberg.namespace", cfg.Namespace))
	defer func() { telemetry.End(span, err) }()

	graph = StabilizeBlankNodes(graph)
	arrowSchema, tableSchema, err := GetSchemas()
	if err != nil {
		return nil, err
	}
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create catalog: %w", err)
	}
	ident := catalog.ToIdentifier(cfg.Namespace, "triples")
	existed, err := cat.CheckTableExists(ctx, ident)
	if err != nil {
		return nil, err
	}
	staged := &StagedGraph{
		TablePath:    filepath.Join(cfg.Warehouse, cfg.Namespace, "triples"),
		cat:          cat,
		ident:        ident,
		cfg:          cfg,
		createdTable: !existed,
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, staged.Discard(ctx))
		}
	}()

	tbl, err := NewIcebergTableFromCfg(ctx, tableSchema, cat, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Iceberg table: %w", err)
	}
	if err := applyWriteProperties(ctx, tbl, cfg); err != nil {
		return nil, err
	}
	// a build that was killed while it was staging left its branch behind
	if err := removeStagingBranch(ctx, cat, ident); err != nil {
		return nil, err
	}
	if tbl, err = cat.LoadTable(ctx, ident); err != nil {
		return nil, fmt.Errorf("load table: %w", err)
	}

	diff, err := diffGraphAgainstTable(ctx, tbl, graph)
	if err != nil {
		return nil, err
	}
	current := tbl.CurrentSnapshot()
	if len(diff.toAdd) == 0 && len(diff.toDrop) == 0 {
		if current == nil {
			return nil, fmt.Errorf("no triples found")
		}
		staged.SnapshotID = current.SnapshotID
		return staged, nil
	}

	if len(diff.toAdd) > 0 {
		staged.hashes = diff.toAdd
		if staged.dataFiles, _, err = writeGraph(ctx, tbl, graph, arrowSchema, cfg.BatchSize, diff.toAdd); err != nil {
			return nil, err
		}
	}
	// a branch that does not exist yet starts from nothing, so it is made to
	// start from main for the staged snapshot to hold the table plus the diff
	if current != nil {
		branch := table.NewSetSnapshotRefUpdate(StagingBranch, current.SnapshotID, table.BranchRef, 0, 0, 0)
		if _, _, err := cat.CommitTable(ctx, ident, nil, []table.Update{branch}); err != nil {
			return nil, fmt.Errorf("create the staging branch: %w", err)
		}
		staged.onBranch = true
		if tbl, err = cat.LoadTable(ctx, ident); err != nil {
			return nil, fmt.Errorf("load table: %w", err)
		}
	}
	committed, err := commitGraphDelta(ctx, tbl, StagingBranch, staged.dataFiles, int64(len(diff.toAdd)), diff.toDrop)
	if err != nil {
		return nil, err
	}
	staged.onBranch = true
	snapshot := committed.SnapshotByName(StagingBranch)
	if snapshot == nil {
		return nil, fmt.Errorf("stage graph: the staging branch has no snapshot")
	}
	staged.SnapshotID = snapshot.SnapshotID
	slog.Info("Staged the build on a branch of the Iceberg table", "branch", StagingBranch, "added", len(diff.toAdd), "removed", len(diff.toDrop), "unchanged", diff.unchanged)
	return staged, nil
}

// Commit writes the graph to the main branch of the table it was staged in,
// the way WriteGraphToIceberg does, reusing the data files staging wrote.
func (s *StagedGraph) Commit(ctx context.Context, graph *rdflibgo.Graph, customMetadata map[string]string) error {
	return writeGraphToIceberg(ctx, graph, s.cfg, customMetadata, s)
}

// reusableFiles returns the data files staging wrote when every triple in
// them is one the commit adds. A staged triple the final graph no longer
// holds, which is a blank node renamed because it gained a triple, cannot be
// deleted in the snapshot that adds its file, so such files are not reused.
func (s *StagedGraph) reusableFiles(toAdd map[string]struct{}) []iceberg.DataFile {
	if s == nil {
		return nil
	}
	for hash := range s.hashes {
		if _, ok := toAdd[hash]; !ok {
			slog.Info("Writing the staged triples again since some of them changed after the build was staged")
			return nil
		}
	}
	return s.dataFiles
}

// Discard removes the staging branch and its snapshot, and every file only
// that snapshot refers to. After Commit that leaves the data files the
// commit reused; without one it leaves the table as it was before staging,
// and a table staging created is dropped again.
func (s *StagedGraph) Discard(ctx context.Context) error {
	if s == nil || s.discarded {
		return nil
	}
	s.discarded = true

	tbl, err := s.cat.LoadTable(ctx, s.ident)
	if errors.Is(err, catalog.ErrNoSuchTable) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("discard the staged build: %w", err)
	}
	if s.createdTable && tbl.CurrentSnapshot() == nil {
		return s.cat.DropTable(ctx, s.ident)
	}
	if s.onBranch {
		return removeStagingBranch(ctx, s.cat, s.ident)
	}
	// staging failed after the data files were written and before they made
	// it into a snapshot
	fs, err := tbl.FS(ctx)
	if err != nil {
		return err
	}
	for _, file := range s.dataFiles {
		err = errors.Join(err, fs.Remove(file.FilePath()))
	}
	return err
}

// removeStagingBranch removes the staging branch from a table and, unless
// another branch or tag still reaches it, the snapshot the branch points at
// together with the files only that snapshot refers to.
func removeStagingBranch(ctx context.Context, cat catalog.Catalog, ident table.Identifier) error {
	before, err := cat.LoadTable(ctx, ident)
	if err != nil {
		return fmt.Errorf("load table: %w", err)
	}
	var staged *table.SnapshotRef
	reachable := map[int64]struct{}{}
	for name, ref := range before.Metadata().Refs() {
		if name == StagingBranch {
			staged = &ref
			continue
		}
		for snapshot := before.SnapshotByID(ref.SnapshotID); snapshot != nil; {
			reachable[snapshot.SnapshotID] = struct{}{}
			if snapshot.ParentSnapshotID == nil {
				break
			}
			snapshot = before.SnapshotByID(*snapshot.ParentSnapshotID)
		}
	}
	if staged == nil {
		return nil
	}

	updates := []table.Update{table.NewRemoveSnapshotRefUpdate(StagingBranch)}
	removeSnapshot := table.NewRemoveSnapshotsUpdate([]int64{staged.SnapshotID}, true)
	_, isReachable := reachable[staged.SnapshotID]
	if !isReachable {
		updates = append(updates, removeSnapshot)
	}
	if _, _, err := cat.CommitTable(ctx, ident, nil, updates); err != nil {
		return fmt.Errorf("remove the staging branch: %w", err)
	}
	if isReachable {
		return nil
	}
	after, err := cat.LoadTable(ctx, ident)
	if err != nil {
		return fmt.Errorf("load table: %w", err)
	}
	// committing through the catalog does not run the cleanup a transaction
	// would, which is what deletes the files only the staged snapshot used
	if err := removeSnapshot.PostCommit(ctx, before, after); err != nil {
		return fmt.Errorf("remove the staged files: %w", err)
	}
	return nil
}
