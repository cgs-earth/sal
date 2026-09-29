package load

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

var stageTestPredicate = rdflibgo.NewURIRefUnsafe("http://example.com/p")

// stageTestGraph holds one triple per name, each about a subject of its own.
func stageTestGraph(names ...string) *rdflibgo.Graph {
	graph := rdflibgo.NewGraph()
	for _, name := range names {
		graph.Add(rdflibgo.NewURIRefUnsafe("http://example.com/"+name), stageTestPredicate, rdflibgo.NewLiteral(name))
	}
	return graph
}

func stageTestConfig(t *testing.T) *LoadConfig {
	t.Helper()
	return &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
}

func stageTestTable(t *testing.T, cfg *LoadConfig) *table.Table {
	t.Helper()
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := cat.LoadTable(context.Background(), catalog.ToIdentifier(cfg.Namespace, "triples"))
	require.NoError(t, err)
	return tbl
}

// graphHashes is the triple hash of every triple in a graph as the table
// would hold it.
func graphHashes(graph *rdflibgo.Graph) map[string]struct{} {
	hashes := map[string]struct{}{}
	StabilizeBlankNodes(graph).Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		hashes[tripleHashForTriple(triple)] = struct{}{}
		return true
	})
	return hashes
}

// requireNoStagedFiles checks that the table has no staging branch and that
// every file in its data directory is one a snapshot refers to.
func requireNoStagedFiles(t *testing.T, tbl *table.Table) {
	t.Helper()
	require.Nil(t, tbl.SnapshotByName(StagingBranch))
	fs, err := tbl.FS(context.Background())
	require.NoError(t, err)
	referenced := map[string]struct{}{}
	for _, snapshot := range tbl.Metadata().Snapshots() {
		manifests, err := snapshot.Manifests(fs)
		require.NoError(t, err)
		for _, manifest := range manifests {
			for entry, err := range manifest.Entries(fs, false) {
				require.NoError(t, err)
				referenced[strings.TrimPrefix(entry.DataFile().FilePath(), "file://")] = struct{}{}
			}
		}
	}
	dataDir := filepath.Join(strings.TrimPrefix(tbl.Location(), "file://"), "data")
	err = filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			require.Contains(t, referenced, path)
		}
		return err
	})
	if !os.IsNotExist(err) {
		require.NoError(t, err)
	}
}

func TestStageGraphLeavesTheMainBranchWhereItWas(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "drop"), cfg, nil))
	lastBuild := stageTestTable(t, cfg).CurrentSnapshot().SnapshotID

	staged, err := StageGraph(ctx, stageTestGraph("keep", "add"), cfg)

	require.NoError(t, err)
	tbl := stageTestTable(t, cfg)
	require.Equal(t, lastBuild, tbl.CurrentSnapshot().SnapshotID)
	require.NotEqual(t, lastBuild, staged.SnapshotID)
	require.Equal(t, staged.SnapshotID, tbl.SnapshotByName(StagingBranch).SnapshotID)
	require.Equal(t, lastBuild, *tbl.SnapshotByID(staged.SnapshotID).ParentSnapshotID)
	// only the triple the table lacked was written
	require.Equal(t, "1", tbl.SnapshotByID(staged.SnapshotID).Summary.Properties["added-records"])
	hashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)
	require.Equal(t, graphHashes(stageTestGraph("keep", "drop")), hashes)
}

func TestCommitReusesTheDataFilesTheGraphWasStagedIn(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "drop"), cfg, nil))
	lastBuild := stageTestTable(t, cfg).CurrentSnapshot().SnapshotID
	staged, err := StageGraph(ctx, stageTestGraph("keep", "add"), cfg)
	require.NoError(t, err)
	require.Len(t, staged.dataFiles, 1)
	final := stageTestGraph("keep", "add", "constructed")

	require.NoError(t, staged.Commit(ctx, final, map[string]string{"sal.hash": "abc"}))
	require.NoError(t, staged.Discard(ctx))

	tbl := stageTestTable(t, cfg)
	require.Len(t, tbl.Metadata().Snapshots(), 2)
	require.Nil(t, tbl.SnapshotByID(staged.SnapshotID))
	current := tbl.CurrentSnapshot()
	require.Equal(t, lastBuild, *current.ParentSnapshotID)
	require.Equal(t, "2", current.Summary.Properties["added-records"])
	require.Equal(t, "2", current.Summary.Properties["added-data-files"])
	require.Equal(t, "abc", tbl.Properties()["sal.hash"])
	require.FileExists(t, strings.TrimPrefix(staged.dataFiles[0].FilePath(), "file://"))
	hashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)
	require.Equal(t, graphHashes(final), hashes)
	requireNoStagedFiles(t, tbl)
}

func TestStagingAGraphTheTableHoldsWritesNothing(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep"), cfg, nil))
	lastBuild := stageTestTable(t, cfg).CurrentSnapshot().SnapshotID

	staged, err := StageGraph(ctx, stageTestGraph("keep"), cfg)

	require.NoError(t, err)
	require.Equal(t, lastBuild, staged.SnapshotID)
	require.Empty(t, staged.dataFiles)
	require.NoError(t, staged.Discard(ctx))
	tbl := stageTestTable(t, cfg)
	require.Len(t, tbl.Metadata().Snapshots(), 1)
	requireNoStagedFiles(t, tbl)
}

func TestDiscardWithoutCommitRemovesWhatWasStaged(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "drop"), cfg, nil))
	lastBuild := stageTestTable(t, cfg).CurrentSnapshot().SnapshotID
	staged, err := StageGraph(ctx, stageTestGraph("keep", "add"), cfg)
	require.NoError(t, err)

	require.NoError(t, staged.Discard(ctx))

	tbl := stageTestTable(t, cfg)
	require.Len(t, tbl.Metadata().Snapshots(), 1)
	require.Equal(t, lastBuild, tbl.CurrentSnapshot().SnapshotID)
	require.NoFileExists(t, strings.TrimPrefix(staged.dataFiles[0].FilePath(), "file://"))
	requireNoStagedFiles(t, tbl)
	hashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)
	require.Equal(t, graphHashes(stageTestGraph("keep", "drop")), hashes)
}

func TestDiscardDropsATableThatStagingCreated(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	staged, err := StageGraph(ctx, stageTestGraph("add"), cfg)
	require.NoError(t, err)
	require.Nil(t, stageTestTable(t, cfg).CurrentSnapshot())

	require.NoError(t, staged.Discard(ctx))

	require.NoDirExists(t, staged.TablePath)
}

// A build that was killed between staging and discarding leaves its branch
// in the table, which the next build to stage removes before it makes its own.
func TestStageGraphRemovesTheBranchAnEarlierBuildLeft(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep"), cfg, nil))
	killed, err := StageGraph(ctx, stageTestGraph("keep", "killed"), cfg)
	require.NoError(t, err)

	staged, err := StageGraph(ctx, stageTestGraph("keep", "add"), cfg)

	require.NoError(t, err)
	tbl := stageTestTable(t, cfg)
	require.Nil(t, tbl.SnapshotByID(killed.SnapshotID))
	require.NoFileExists(t, strings.TrimPrefix(killed.dataFiles[0].FilePath(), "file://"))
	require.Equal(t, staged.SnapshotID, tbl.SnapshotByName(StagingBranch).SnapshotID)
	require.Len(t, tbl.Metadata().Snapshots(), 2)
}

// A blank node is named from the triples about it, so one that gains a
// constructed triple is renamed and the rows staged under its old name are
// not what the build commits. They cannot be deleted in the snapshot that
// would add them, so their data files are left out of it.
func TestCommitWritesAgainWhatChangedSinceItWasStaged(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	graph := func() *rdflibgo.Graph {
		graph := stageTestGraph("keep")
		graph.Add(rdflibgo.NewBNode("b"), stageTestPredicate, rdflibgo.NewLiteral("blank"))
		return graph
	}
	staged, err := StageGraph(ctx, graph(), cfg)
	require.NoError(t, err)
	final := graph()
	final.Add(rdflibgo.NewBNode("b"), stageTestPredicate, rdflibgo.NewLiteral("constructed"))

	require.NoError(t, staged.Commit(ctx, final, nil))
	require.NoError(t, staged.Discard(ctx))

	tbl := stageTestTable(t, cfg)
	require.Len(t, tbl.Metadata().Snapshots(), 1)
	require.Equal(t, "3", tbl.CurrentSnapshot().Summary.Properties["total-records"])
	hashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)
	require.Equal(t, graphHashes(final), hashes)
	requireNoStagedFiles(t, tbl)
}
