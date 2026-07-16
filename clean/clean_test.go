package clean

import (
	"context"
	"fmt"
	"testing"

	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/build/load"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func TestDeleteSnapshotsRemovesLatestSnapshotAndRollsBackMain(t *testing.T) {
	ctx := context.Background()
	cat, tbl, snapshots := createTableWithSnapshots(t, 3)

	require.NoError(t, DeleteSnapshots(tbl, cat, []string{fmt.Sprintf("%d", snapshots[2])}))

	loaded, err := cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	require.Equal(t, snapshots[1], loaded.CurrentSnapshot().SnapshotID)
	require.Nil(t, loaded.SnapshotByID(snapshots[2]))
	require.NotNil(t, loaded.SnapshotByID(snapshots[0]))
	require.NotNil(t, loaded.SnapshotByID(snapshots[1]))
	require.Len(t, loaded.Metadata().Snapshots(), 2)
}

func TestDeleteSnapshotsRemovesNonCurrentSnapshot(t *testing.T) {
	ctx := context.Background()
	cat, tbl, snapshots := createTableWithSnapshots(t, 3)

	require.NoError(t, DeleteSnapshots(tbl, cat, []string{fmt.Sprintf("%d", snapshots[0])}))

	loaded, err := cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	require.Equal(t, snapshots[2], loaded.CurrentSnapshot().SnapshotID)
	require.Nil(t, loaded.SnapshotByID(snapshots[0]))
	require.NotNil(t, loaded.SnapshotByID(snapshots[1]))
	require.NotNil(t, loaded.SnapshotByID(snapshots[2]))
	require.Len(t, loaded.Metadata().Snapshots(), 2)
}

func TestDeleteSnapshotsRejectsDeletingEverySnapshot(t *testing.T) {
	cat, tbl, snapshots := createTableWithSnapshots(t, 2)

	err := DeleteSnapshots(tbl, cat, []string{fmt.Sprintf("%d", snapshots[0]), fmt.Sprintf("%d", snapshots[1])})

	require.ErrorContains(t, err, "cannot delete every snapshot")
}

func TestSquashSnapshotsCondensesLocalSnapshotsIntoNewSnapshot(t *testing.T) {
	ctx := context.Background()
	cat, tbl, snapshots := createTableWithSnapshots(t, 4)
	latestManifest := tbl.SnapshotByID(snapshots[3]).ManifestList

	require.NoError(t, SquashSnapshots(
		tbl,
		cat,
		[]string{fmt.Sprintf("%d", snapshots[1]), fmt.Sprintf("%d", snapshots[2]), fmt.Sprintf("%d", snapshots[3])},
		[]string{fmt.Sprintf("%d", snapshots[0])},
	))

	loaded, err := cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	require.Len(t, loaded.Metadata().Snapshots(), 2)
	require.NotNil(t, loaded.SnapshotByID(snapshots[0]))
	require.Nil(t, loaded.SnapshotByID(snapshots[1]))
	require.Nil(t, loaded.SnapshotByID(snapshots[2]))
	require.Nil(t, loaded.SnapshotByID(snapshots[3]))
	require.NotEqual(t, snapshots[3], loaded.CurrentSnapshot().SnapshotID)
	require.Equal(t, latestManifest, loaded.CurrentSnapshot().ManifestList)
	require.NotNil(t, loaded.CurrentSnapshot().ParentSnapshotID)
	require.Equal(t, snapshots[0], *loaded.CurrentSnapshot().ParentSnapshotID)
}

func createTableWithSnapshots(t *testing.T, count int) (*hadoop.Catalog, *table.Table, []int64) {
	t.Helper()

	ctx := context.Background()
	cfg := &load.LoadConfig{
		BatchSize:           10,
		ParquetCompression:  "snappy",
		MetricsMode:         "truncate(16)",
		TargetFileSizeBytes: 0,
		Warehouse:           t.TempDir(),
		Namespace:           "default",
	}

	for i := 0; i < count; i++ {
		require.NoError(t, load.WriteGraphToIceberg(ctx, graphForSnapshot(i), cfg, map[string]string{"sal.test-snapshot": fmt.Sprintf("%d", i)}))
	}

	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := cat.LoadTable(ctx, table.Identifier{"default", "triples"})
	require.NoError(t, err)

	snapshots := make([]int64, 0, len(tbl.Metadata().Snapshots()))
	for _, snapshot := range tbl.Metadata().Snapshots() {
		snapshots = append(snapshots, snapshot.SnapshotID)
	}
	require.Len(t, snapshots, count)

	return cat, tbl, snapshots
}

func graphForSnapshot(i int) *rdflibgo.Graph {
	graph := rdflibgo.NewGraph()
	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s"),
		predicate,
		rdflibgo.NewLiteral(fmt.Sprintf("value-%d", i)),
	)
	return graph
}
