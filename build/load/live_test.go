package load

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/stretchr/testify/require"
)

// liveSubjects is the subject of every row ScanLiveRows reads from the table.
func liveSubjects(t *testing.T, cfg *LoadConfig) []string {
	t.Helper()
	var subjects []string
	rows, err := ScanLiveRows(context.Background(), stageTestTable(t, cfg), filepath.Join(cfg.Warehouse, ScratchDir), []string{"subject"}, func(record arrow.RecordBatch) error {
		column := record.Column(0).(*array.String)
		for i := 0; i < column.Len(); i++ {
			subjects = append(subjects, column.Value(i))
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, int64(len(subjects)), rows)
	return subjects
}

// A row an equality delete removed stays removed, and a triple a later build
// adds again is read again: a delete applies only to the files written before
// it, which is what the rows' sequence numbers decide.
func TestScanLiveRowsAppliesEqualityDeletesBySequence(t *testing.T) {
	ctx := context.Background()
	cfg := stageTestConfig(t)
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "readded"), cfg, nil, ""))
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep"), cfg, nil, ""))
	require.ElementsMatch(t, []string{"http://example.com/keep"}, liveSubjects(t, cfg))

	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "readded"), cfg, nil, ""))

	require.ElementsMatch(t, []string{"http://example.com/keep", "http://example.com/readded"}, liveSubjects(t, cfg))
	// the diff reads the table the same way, so building the same graph again
	// finds nothing to change
	snapshots := len(stageTestTable(t, cfg).Metadata().Snapshots())
	require.NoError(t, WriteGraphToIceberg(ctx, stageTestGraph("keep", "readded"), cfg, nil, ""))
	require.Len(t, stageTestTable(t, cfg).Metadata().Snapshots(), snapshots)
	hashes, err := readExistingTripleHashes(ctx, stageTestTable(t, cfg))
	require.NoError(t, err)
	require.Equal(t, graphHashes(stageTestGraph("keep", "readded")), hashes)
}
