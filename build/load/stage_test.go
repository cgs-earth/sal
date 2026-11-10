package load

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

var stageTestPredicate = rdflibgo.NewURIRefUnsafe("http://example.com/p")

// stageTestGraph holds one triple per name, each about a subject of its own.
func stageTestGraph(names ...string) *rdflibgo.Graph {
	graph := rdflibgo.NewGraph()
	for _, name := range names {
		pkg.AddTriple(graph, rdflibgo.NewURIRefUnsafe("http://example.com/"+name), stageTestPredicate, rdflibgo.NewLiteral(name))
	}
	return graph
}

func stageTestConfig(t *testing.T) *LoadConfig {
	t.Helper()
	return &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
}

func stageTestTable(t *testing.T, cfg *LoadConfig) *table.Table {
	t.Helper()
	cat, err := hadoop.NewCatalog(context.Background(), "local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := cat.LoadTable(context.Background(), catalog.ToIdentifier(cfg.Namespace, "triples"))
	require.NoError(t, err)
	return tbl
}

// readExistingTripleHashes is the triple_hash of every row the table's
// current snapshot holds.
func readExistingTripleHashes(ctx context.Context, tbl *table.Table) (map[string]struct{}, error) {
	hashes := map[string]struct{}{}
	if tbl.CurrentSnapshot() == nil {
		return hashes, nil
	}
	_, records, err := tbl.Scan(table.WithSelectedFields("triple_hash")).ToArrowRecords(ctx)
	if err != nil {
		return nil, err
	}
	for rec, err := range records {
		if err != nil {
			return nil, err
		}
		column := rec.Column(0).(*array.String)
		for i := 0; i < column.Len(); i++ {
			hashes[column.Value(i)] = struct{}{}
		}
		rec.Release()
	}
	return hashes, nil
}

// graphHashes is the triple hash of every triple in a graph as the table
// would hold it.
func graphHashes(graph *rdflibgo.Graph) map[string]struct{} {
	hashes := map[string]struct{}{}
	pkg.Triples(StabilizeBlankNodes(graph), nil, nil, nil)(func(triple rdflibgo.Triple) bool {
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
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "drop"), cfg, nil, ""))
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
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "drop"), cfg, nil, ""))
	lastBuild := stageTestTable(t, cfg).CurrentSnapshot().SnapshotID
	staged, err := StageGraph(ctx, stageTestGraph("keep", "add"), cfg)
	require.NoError(t, err)
	require.Len(t, staged.dataFiles, 1)
	final := stageTestGraph("keep", "add", "constructed")
	require.NoError(t, staged.Spool().AddGraph(stageTestGraph("constructed")))

	require.NoError(t, staged.Commit(ctx, map[string]string{"sal.hash": "abc"}, "Add constructed"))
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
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep"), cfg, nil, ""))
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
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "drop"), cfg, nil, ""))
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
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep"), cfg, nil, ""))
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

// What is spooled after staging, which is what a build's CONSTRUCT queries
// produce, refers to a blank node by the label it was staged under, and the
// commit keeps that label rather than renaming the node for the triple it
// gained, so the staged data files are committed as they are.
func TestCommitKeepsTheLabelsTheBuildWasStagedUnder(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	graph := stageTestGraph("keep")
	pkg.AddTriple(graph, rdflibgo.NewBNode("b"), stageTestPredicate, rdflibgo.NewLiteral("blank"))
	staged, err := StageGraph(ctx, graph, cfg)
	require.NoError(t, err)
	var label rdflibgo.Subject
	pkg.Triples(graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if isBlank(triple.Subject) {
			label = triple.Subject
		}
		return true
	})
	constructed := rdflibgo.Triple{Subject: label, Predicate: stageTestPredicate, Object: rdflibgo.NewLiteral("constructed")}
	require.NoError(t, staged.Spool().Add(constructed))

	require.NoError(t, staged.Commit(ctx, nil, ""))
	require.NoError(t, staged.Discard(ctx))

	tbl := stageTestTable(t, cfg)
	require.Len(t, tbl.Metadata().Snapshots(), 1)
	require.Equal(t, "3", tbl.CurrentSnapshot().Summary.Properties["total-records"])
	hashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)
	require.Contains(t, hashes, tripleHashForTriple(constructed))
	requireNoStagedFiles(t, tbl)
}

// A row the table holds that staging deletes, because only a CONSTRUCT query
// makes it, is spooled again once the query has run, and the commit then
// neither deletes it nor adds it a second time: a rebuild of unchanged
// sources commits nothing.
func TestCommitKeepsARowSpooledAgainAfterStaging(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "constructed"), cfg, nil, ""))
	lastBuild := stageTestTable(t, cfg).CurrentSnapshot().SnapshotID
	staged, err := StageGraph(ctx, stageTestGraph("keep"), cfg)
	require.NoError(t, err)
	require.Equal(t, int64(1), staged.removed)
	require.NoError(t, staged.Spool().AddGraph(stageTestGraph("constructed")))

	require.NoError(t, staged.Commit(ctx, nil, "Nothing changed"))
	require.NoError(t, staged.Discard(ctx))

	tbl := stageTestTable(t, cfg)
	require.Equal(t, lastBuild, tbl.CurrentSnapshot().SnapshotID)
	require.Len(t, tbl.Metadata().Snapshots(), 1)
	hashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)
	require.Equal(t, graphHashes(stageTestGraph("keep", "constructed")), hashes)
	requireNoStagedFiles(t, tbl)
}

// snapshotMessage is the build message the table's current snapshot carries.
func snapshotMessage(t *testing.T, cfg *LoadConfig) string {
	t.Helper()
	snapshot := stageTestTable(t, cfg).CurrentSnapshot()
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.Summary)
	return snapshot.Summary.Properties[pkg.SnapshotMessageProperty]
}

// The message a build is given is recorded in the summary of the snapshot it
// commits, whether the graph was written directly or staged first, and a
// build that changes nothing commits no snapshot and so records no message.
func TestWriteGraphToIcebergRecordsTheMessageInTheSnapshotSummary(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)

	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep"), cfg, nil, "Add keep"))
	require.Equal(t, "Add keep", snapshotMessage(t, cfg))
	first := stageTestTable(t, cfg).CurrentSnapshot().SnapshotID

	staged, err := StageGraph(ctx, stageTestGraph("keep", "add"), cfg)
	require.NoError(t, err)
	require.NoError(t, staged.Commit(ctx, nil, "Add add"))
	require.NoError(t, staged.Discard(ctx))
	require.Equal(t, "Add add", snapshotMessage(t, cfg))
	require.NotEqual(t, first, stageTestTable(t, cfg).CurrentSnapshot().SnapshotID)

	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "add"), cfg, nil, "Nothing changed"))
	require.Equal(t, "Add add", snapshotMessage(t, cfg))
}
