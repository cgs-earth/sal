package sparql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// useTestLanes puts lanes in force for one test and takes them back out,
// closing the slow database and returning the fast one to its defaults.
func useTestLanes(t *testing.T, config QueryLanes) {
	t.Helper()
	UseQueryLanes(config)
	t.Cleanup(func() {
		UseQueryLanes(QueryLanes{})
		slowLane.mu.Lock()
		if slowLane.db != nil {
			require.NoError(t, slowLane.db.Close())
			slowLane.db, slowLane.spatial, slowLane.viewPath, slowLane.imports = nil, false, "", nil
		}
		slowLane.mu.Unlock()
		db, err := instance.prepare(context.Background(), "", nil, false)
		require.NoError(t, err)
		_, err = db.Exec("RESET threads; RESET memory_limit")
		require.NoError(t, err)
	})
}

// A query still running when DemoteAfter is up is run again on the slow
// database, which runs with the threads and memory it was given.
func TestRunSQLMovesAQueryThatOutrunsTheFastLaneToTheSlowOne(t *testing.T) {
	useTestLanes(t, QueryLanes{DemoteAfter: time.Nanosecond, SlowThreads: 1, SlowMemory: 256 << 20})

	header, rows, err := DuckDBRunner{}.runSQL(context.Background(), "SELECT current_setting('threads') AS threads, current_setting('memory_limit') AS memory", false)

	require.NoError(t, err)
	require.Equal(t, []string{"threads", "memory"}, header)
	require.Equal(t, [][]string{{"1", "256.0 MiB"}}, rows)
	require.NotNil(t, slowLane.db)
}

// A query that finishes within DemoteAfter never touches the slow database.
func TestRunSQLKeepsAQuickQueryOnTheFastLane(t *testing.T) {
	useTestLanes(t, QueryLanes{DemoteAfter: time.Minute, FastThreads: 2, SlowThreads: 1})

	_, rows, err := DuckDBRunner{}.runSQL(context.Background(), "SELECT current_setting('threads') AS threads", false)

	require.NoError(t, err)
	require.Equal(t, [][]string{{"2"}}, rows)
	require.Nil(t, slowLane.db)
}

// A query whose caller gave up is not run again on the slow database.
func TestRunSQLDoesNotDemoteACancelledQuery(t *testing.T) {
	useTestLanes(t, QueryLanes{DemoteAfter: time.Nanosecond})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := DuckDBRunner{}.runSQL(ctx, "SELECT 1", false)

	require.Error(t, err)
	require.Nil(t, slowLane.db)
}

func TestDefaultQueryLanesGivesTheSlowLaneTheSmallerShare(t *testing.T) {
	config := DefaultQueryLanes(5 * time.Second)

	require.Equal(t, 5*time.Second, config.DemoteAfter)
	require.GreaterOrEqual(t, config.SlowThreads, 1)
	require.LessOrEqual(t, config.SlowThreads, config.FastThreads)
	require.LessOrEqual(t, config.SlowMemory, config.FastMemory)
}
