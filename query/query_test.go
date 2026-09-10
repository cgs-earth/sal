package query

import (
	"testing"

	"github.com/apache/iceberg-go/table"
	"github.com/stretchr/testify/require"
)

func TestSnapshotForDiffUsesCurrentSnapshotForLatest(t *testing.T) {
	current := &table.Snapshot{SnapshotID: 456}

	snapshot, err := snapshotForDiff("latest", current, func(int64) *table.Snapshot {
		t.Fatal("snapshot lookup should not be called for latest snapshot")
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, int64(456), snapshot.SnapshotID)
}

func TestSnapshotForDiffUsesCurrentSnapshotForLatestCaseInsensitively(t *testing.T) {
	current := &table.Snapshot{SnapshotID: 456}

	snapshot, err := snapshotForDiff(" Latest ", current, func(int64) *table.Snapshot {
		t.Fatal("snapshot lookup should not be called for latest snapshot")
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, int64(456), snapshot.SnapshotID)
}

func TestSnapshotForDiffRejectsLatestWhenNoSnapshotsExist(t *testing.T) {
	snapshot, err := snapshotForDiff("latest", nil, func(int64) *table.Snapshot {
		t.Fatal("snapshot lookup should not be called for latest snapshot")
		return nil
	})

	require.Nil(t, snapshot)
	require.ErrorContains(t, err, "no snapshots found")
}

func TestSnapshotForDiffUsesExplicitSnapshotID(t *testing.T) {
	snapshot, err := snapshotForDiff("123", &table.Snapshot{SnapshotID: 456}, func(id int64) *table.Snapshot {
		require.Equal(t, int64(123), id)
		return &table.Snapshot{SnapshotID: id}
	})

	require.NoError(t, err)
	require.Equal(t, int64(123), snapshot.SnapshotID)
}

func TestSnapshotForDiffRejectsNonPositiveIDs(t *testing.T) {
	snapshot, err := snapshotForDiff("-1", &table.Snapshot{SnapshotID: 456}, func(int64) *table.Snapshot {
		t.Fatal("snapshot lookup should not be called for invalid IDs")
		return nil
	})

	require.Nil(t, snapshot)
	require.ErrorContains(t, err, "positive snapshot ID or 'latest'")
}

func TestSnapshotForDiffRejectsInvalidIDs(t *testing.T) {
	snapshot, err := snapshotForDiff("not-a-snapshot", &table.Snapshot{SnapshotID: 456}, func(int64) *table.Snapshot {
		t.Fatal("snapshot lookup should not be called for invalid IDs")
		return nil
	})

	require.Nil(t, snapshot)
	require.ErrorContains(t, err, "positive snapshot ID or 'latest'")
}
