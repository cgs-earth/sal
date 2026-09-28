package build

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/construct"
	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const constructTestQuery = `PREFIX things: <https://vocab.test/things#>
CONSTRUCT { ?widget things:alias ?label } WHERE { ?widget things:label ?label }`

// stagedConstructRunner stands in for DuckDB over the staged table: it
// answers a CONSTRUCT from the graph the build staged, copying every
// things:label as a things:alias.
type stagedConstructRunner struct {
	staged *rdflibgo.Graph
	runs   int
}

func (r *stagedConstructRunner) Construct(_ context.Context, _ string, rowFn func([]sql.NullString) error) error {
	r.runs++
	var err error
	r.staged.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if triple.Predicate.Value() != "https://vocab.test/things#label" {
			return true
		}
		value := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
		literal := triple.Object.(rdflibgo.Literal)
		err = rowFn([]sql.NullString{value(triple.Subject.String()), value("https://vocab.test/things#alias"),
			{}, {}, {}, {}, {}, {}, value(literal.Lexical()), value(literal.Datatype().Value()), {}})
		return err == nil
	})
	return err
}

// installStagedConstructRunner keeps a build from writing a staged table and
// opening DuckDB over it, which needs extensions a unit test cannot download.
func installStagedConstructRunner(t *testing.T) *stagedConstructRunner {
	t.Helper()
	runner := &stagedConstructRunner{}
	original := stageGraph
	stageGraph = func(_ context.Context, graph *rdflibgo.Graph) (construct.Runner, func(), error) {
		runner.staged = graph
		return runner, func() {}, nil
	}
	t.Cleanup(func() { stageGraph = original })
	return runner
}

// commitConstructQuery adds a query to the project's sparql/ directory and
// commits it, so that the build runs against a clean worktree.
func commitConstructQuery(t *testing.T, git func(args ...string), name string, query string) {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, "sparql"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "sparql", name), []byte(query), 0644))
	git("add", "-A")
	git("commit", "-m", "construct query")
}

// TestBuildCommitsConstructedTriplesInTheSameSnapshot checks that a build
// runs the project's CONSTRUCT queries over what it gathered and commits what
// they built with everything else, as the one snapshot the build makes.
func TestBuildCommitsConstructedTriplesInTheSameSnapshot(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	runner := installStagedConstructRunner(t)
	commitConstructQuery(t, git, "aliases.rq", constructTestQuery)

	graph, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()

	require.NoError(t, err)
	require.Equal(t, 1, runner.runs)
	widget := "https://github.com/cgs-earth/sal-run-test-project/widgets/1"
	require.True(t, graphHasTriple(graph, widget, "https://vocab.test/things#alias", "A widget"))
	require.True(t, graphHasTriple(graph, widget, "https://vocab.test/things#label", "A widget"))

	tbl, err := pkg.GetSalIcebergTable()
	require.NoError(t, err)
	require.Len(t, tbl.Metadata().Snapshots(), 1)
	require.Equal(t, "append", string(tbl.CurrentSnapshot().Summary.Operation))

	cwd, err := os.Getwd()
	require.NoError(t, err)
	constructed, err := os.ReadFile(filepath.Join(cwd, ".sal", "constructed", "aliases.ttl"))
	require.NoError(t, err)
	require.Contains(t, string(constructed), "A widget")
}

// TestBuildHashesTheConstructQueries checks that changing a query changes the
// hash a build records, since the query decides what the table holds.
func TestBuildHashesTheConstructQueries(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	installStagedConstructRunner(t)

	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	tbl, err := pkg.GetSalIcebergTable()
	require.NoError(t, err)
	withoutQuery := tbl.Properties()["sal.hash"]

	commitConstructQuery(t, git, "aliases.rq", constructTestQuery)
	_, err = (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	tbl, err = pkg.GetSalIcebergTable()
	require.NoError(t, err)

	require.NotEmpty(t, withoutQuery)
	require.NotEqual(t, withoutQuery, tbl.Properties()["sal.hash"])
}

// TestBuildRefusesAConstructQueryItCannotTranslate checks that a query sal
// cannot run fails the build before anything is committed.
func TestBuildRefusesAConstructQueryItCannotTranslate(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	runner := installStagedConstructRunner(t)
	commitConstructQuery(t, git, "broken.rq", `CONSTRUCT { ?s ?p ?missing } WHERE { ?s ?p ?o }`)

	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()

	require.ErrorContains(t, err, "broken.rq")
	require.Equal(t, 0, runner.runs)
	_, err = pkg.GetSalIcebergTable()
	require.Error(t, err)
}

// TestBuildWithoutConstructQueriesStagesNothing checks that a project with no
// CONSTRUCT query pays nothing for the step, and that what an earlier build
// constructed is still wiped.
func TestBuildWithoutConstructQueriesStagesNothing(t *testing.T) {
	newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	runner := installStagedConstructRunner(t)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	stale := filepath.Join(cwd, ".sal", "constructed", "aliases.ttl")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0755))
	require.NoError(t, os.WriteFile(stale, []byte("# stale\n"), 0644))

	_, err = (&BuildCmd{Format: GraphExportFormatIceberg, Force: true}).Run()

	require.NoError(t, err)
	require.Nil(t, runner.staged)
	require.NoFileExists(t, stale)
}

// installRealStaging lets a build stage its graph exactly as it does outside a
// test, writing the temporary Iceberg table, and swaps only the DuckDB runner
// over that table for the fake, since DuckDB's extensions cannot be
// downloaded here. It returns the fake and the staged table's path.
func installRealStaging(t *testing.T) (*stagedConstructRunner, *string) {
	t.Helper()
	runner := &stagedConstructRunner{}
	stagedPath := new(string)
	original := stageGraph
	stageGraph = func(ctx context.Context, graph *rdflibgo.Graph) (construct.Runner, func(), error) {
		real, cleanup, err := original(ctx, graph)
		if err != nil {
			return nil, nil, err
		}
		*stagedPath = real.(salsparql.DuckDBRunner).TablePath
		runner.staged = graph
		return runner, cleanup, nil
	}
	t.Cleanup(func() { stageGraph = original })
	return runner, stagedPath
}

// TestBuildWritesTheGraphTwiceButCommitsOneSnapshot guards the one thing the
// staging must never do: reach the project's table. The graph is written to
// Iceberg twice, once staged for the CONSTRUCT queries and once for real, and
// the project's table still gains exactly one snapshot holding everything.
func TestBuildWritesTheGraphTwiceButCommitsOneSnapshot(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	runner, stagedPath := installRealStaging(t)
	commitConstructQuery(t, git, "aliases.rq", constructTestQuery)

	graph, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()

	require.NoError(t, err)
	require.Equal(t, 1, runner.runs)

	// the staged table was written outside the project and is gone again
	dataDir, err := pkg.SalDataDir()
	require.NoError(t, err)
	require.NotEmpty(t, *stagedPath)
	require.NotContains(t, *stagedPath, dataDir)
	require.NoDirExists(t, *stagedPath)
	tables, err := pkg.IcebergTablePaths(dataDir)
	require.NoError(t, err)
	require.Len(t, tables, 1)

	tbl, err := pkg.GetSalIcebergTable()
	require.NoError(t, err)
	require.Len(t, tbl.Metadata().Snapshots(), 1)
	require.Equal(t, fmt.Sprint(graph.Len()), tbl.CurrentSnapshot().Summary.Properties["added-records"])
	require.Equal(t, fmt.Sprint(graph.Len()), tbl.CurrentSnapshot().Summary.Properties["total-records"])
	require.True(t, graphHasTriple(graph, "https://github.com/cgs-earth/sal-run-test-project/widgets/1", "https://vocab.test/things#alias", "A widget"))
}

// TestEveryBuildWithConstructQueriesAddsOneSnapshot checks the same across
// builds: a build that changes what the queries construct adds one snapshot
// to the table, and a build that changes nothing adds none.
func TestEveryBuildWithConstructQueriesAddsOneSnapshot(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	installRealStaging(t)
	commitConstructQuery(t, git, "aliases.rq", constructTestQuery)
	snapshots := func() int {
		tbl, err := pkg.GetSalIcebergTable()
		require.NoError(t, err)
		return len(tbl.Metadata().Snapshots())
	}

	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	require.Equal(t, 1, snapshots())

	// the first build pinned the vocabulary into .sal/config.jsonld
	git("add", "-A")
	git("commit", "-m", "pin vocabularies")
	_, err = (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	require.Equal(t, 1, snapshots())

	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "data.ttl"), []byte(pinsTestSource+"\n<widgets/2> a things:Widget ;\n    things:label \"Another widget\" .\n"), 0644))
	git("add", "-A")
	git("commit", "-m", "another widget")
	graph, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	require.Equal(t, 2, snapshots())
	require.True(t, graphHasTriple(graph, "https://github.com/cgs-earth/sal-run-test-project/widgets/2", "https://vocab.test/things#alias", "Another widget"))
}

// TestBuildCommitsNothingWhenAConstructQueryFails checks the other half of
// the build being atomic: the queries run before the project's table is
// written, so a query that fails leaves no snapshot behind, not one holding
// the sources without what should have been constructed from them.
func TestBuildCommitsNothingWhenAConstructQueryFails(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	original := stageGraph
	var stagedPath string
	stageGraph = func(ctx context.Context, graph *rdflibgo.Graph) (construct.Runner, func(), error) {
		real, cleanup, err := original(ctx, graph)
		if err != nil {
			return nil, nil, err
		}
		stagedPath = real.(salsparql.DuckDBRunner).TablePath
		return failingConstructRunner{}, cleanup, nil
	}
	t.Cleanup(func() { stageGraph = original })
	commitConstructQuery(t, git, "aliases.rq", constructTestQuery)

	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()

	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.ErrorContains(t, err, "aliases.rq")
	require.NoDirExists(t, stagedPath)
	dataDir, err := pkg.SalDataDir()
	require.NoError(t, err)
	tables, err := pkg.IcebergTablePaths(dataDir)
	require.NoError(t, err)
	require.Empty(t, tables)
}

type failingConstructRunner struct{}

func (failingConstructRunner) Construct(context.Context, string, func([]sql.NullString) error) error {
	return os.ErrDeadlineExceeded
}
