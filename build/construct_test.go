package build

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/build/load"
	"github.com/cgs-earth/sal/construct"
	"github.com/cgs-earth/sal/pkg"
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
	stageGraph = func(_ context.Context, graph *rdflibgo.Graph, _ GraphExportFormat) (construct.Runner, *load.StagedGraph, func(), error) {
		runner.staged = graph
		return runner, nil, func() {}, nil
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
// test, on a branch of the project's table, and swaps only the DuckDB runner
// over that table for the fake, since DuckDB's extensions cannot be
// downloaded here. It returns the fake and what the last build staged.
func installRealStaging(t *testing.T, runner construct.Runner) *load.StagedGraph {
	t.Helper()
	last := &load.StagedGraph{}
	original := stageGraph
	stageGraph = func(ctx context.Context, graph *rdflibgo.Graph, format GraphExportFormat) (construct.Runner, *load.StagedGraph, func(), error) {
		_, staged, cleanup, err := original(ctx, graph, format)
		if err != nil {
			return nil, nil, nil, err
		}
		*last = *staged
		if fake, ok := runner.(*stagedConstructRunner); ok {
			fake.staged = graph
		}
		return runner, staged, cleanup, nil
	}
	t.Cleanup(func() { stageGraph = original })
	return last
}

// requireNothingStagedIsLeft checks that a build left the project's table
// with no trace of its staging: no branch, and no file in the table's data
// directory that the table does not refer to.
func requireNothingStagedIsLeft(t *testing.T, tbl *table.Table) {
	t.Helper()
	for name := range tbl.Metadata().Refs() {
		require.NotEqual(t, load.StagingBranch, name)
	}
	referenced := map[string]struct{}{}
	fs, err := tbl.FS(context.Background())
	require.NoError(t, err)
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
	require.NoError(t, filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			require.Contains(t, referenced, path)
		}
		return err
	}))
}

// TestBuildStagesOnABranchAndCommitsOneSnapshot guards the one thing the
// staging must never do: move the main branch of the project's table. The
// build is staged in the project's own table, on a branch, and the table
// still gains exactly one snapshot holding everything, made of the data files
// the staging wrote plus the constructed triples.
func TestBuildStagesOnABranchAndCommitsOneSnapshot(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	runner := &stagedConstructRunner{}
	staged := installRealStaging(t, runner)
	commitConstructQuery(t, git, "aliases.rq", constructTestQuery)

	graph, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()

	require.NoError(t, err)
	require.Equal(t, 1, runner.runs)

	// the build was staged in the project's table, which is the only one
	dataDir, err := pkg.SalDataDir()
	require.NoError(t, err)
	tables, err := pkg.IcebergTablePaths(dataDir)
	require.NoError(t, err)
	require.Equal(t, []string{staged.TablePath}, tables)

	tbl, err := pkg.GetSalIcebergTable()
	require.NoError(t, err)
	require.Len(t, tbl.Metadata().Snapshots(), 1)
	require.NotEqual(t, staged.SnapshotID, tbl.CurrentSnapshot().SnapshotID)
	require.Nil(t, tbl.CurrentSnapshot().ParentSnapshotID)
	require.Equal(t, fmt.Sprint(graph.Len()), tbl.CurrentSnapshot().Summary.Properties["added-records"])
	require.Equal(t, fmt.Sprint(graph.Len()), tbl.CurrentSnapshot().Summary.Properties["total-records"])
	requireNothingStagedIsLeft(t, tbl)
	require.True(t, graphHasTriple(graph, "https://github.com/cgs-earth/sal-run-test-project/widgets/1", "https://vocab.test/things#alias", "A widget"))
}

// TestEveryBuildWithConstructQueriesAddsOneSnapshot checks the same across
// builds: a build that changes what the queries construct adds one snapshot
// to the table, whose parent is the snapshot of the build before it, and a
// build that changes nothing adds none.
func TestEveryBuildWithConstructQueriesAddsOneSnapshot(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	installRealStaging(t, &stagedConstructRunner{})
	commitConstructQuery(t, git, "aliases.rq", constructTestQuery)
	built := func() *table.Table {
		tbl, err := pkg.GetSalIcebergTable()
		require.NoError(t, err)
		requireNothingStagedIsLeft(t, tbl)
		return tbl
	}

	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	require.Len(t, built().Metadata().Snapshots(), 1)
	first := built().CurrentSnapshot().SnapshotID

	// the first build pinned the vocabulary into .sal/config.jsonld
	git("add", "-A")
	git("commit", "-m", "pin vocabularies")
	_, err = (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	require.Len(t, built().Metadata().Snapshots(), 1)
	require.Equal(t, first, built().CurrentSnapshot().SnapshotID)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "data.ttl"), []byte(pinsTestSource+"\n<widgets/2> a things:Widget ;\n    things:label \"Another widget\" .\n"), 0644))
	git("add", "-A")
	git("commit", "-m", "another widget")
	graph, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	require.Len(t, built().Metadata().Snapshots(), 2)
	require.Equal(t, first, *built().CurrentSnapshot().ParentSnapshotID)
	require.Equal(t, "3", built().CurrentSnapshot().Summary.Properties["added-records"])
	require.True(t, graphHasTriple(graph, "https://github.com/cgs-earth/sal-run-test-project/widgets/2", "https://vocab.test/things#alias", "Another widget"))
}

// TestBuildCommitsNothingWhenAConstructQueryFails checks the other half of
// the build being atomic: the queries run before the main branch of the
// project's table is written, so a query that fails leaves no snapshot
// behind, not one holding the sources without what should have been
// constructed from them, and the table the staging created is gone again.
func TestBuildCommitsNothingWhenAConstructQueryFails(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	installRealStaging(t, failingConstructRunner{})
	commitConstructQuery(t, git, "aliases.rq", constructTestQuery)

	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()

	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.ErrorContains(t, err, "aliases.rq")
	dataDir, err := pkg.SalDataDir()
	require.NoError(t, err)
	tables, err := pkg.IcebergTablePaths(dataDir)
	require.NoError(t, err)
	require.Empty(t, tables)
}

// TestAFailedConstructQueryLeavesTheLastBuildAsItWas checks the same for a
// project that was built before: the table keeps the snapshot of the last
// build as its only one, and nothing the failed build staged is left in it.
func TestAFailedConstructQueryLeavesTheLastBuildAsItWas(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})
	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	tbl, err := pkg.GetSalIcebergTable()
	require.NoError(t, err)
	lastBuild := tbl.CurrentSnapshot().SnapshotID

	git("add", "-A")
	git("commit", "-m", "pin vocabularies")
	installRealStaging(t, failingConstructRunner{})
	commitConstructQuery(t, git, "aliases.rq", constructTestQuery)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "data.ttl"), []byte(pinsTestSource+"\n<widgets/2> a things:Widget ;\n    things:label \"Another widget\" .\n"), 0644))
	git("add", "-A")
	git("commit", "-m", "another widget")

	_, err = (&BuildCmd{Format: GraphExportFormatIceberg}).Run()

	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	tbl, err = pkg.GetSalIcebergTable()
	require.NoError(t, err)
	require.Len(t, tbl.Metadata().Snapshots(), 1)
	require.Equal(t, lastBuild, tbl.CurrentSnapshot().SnapshotID)
	requireNothingStagedIsLeft(t, tbl)
}

type failingConstructRunner struct{}

func (failingConstructRunner) Construct(context.Context, string, func([]sql.NullString) error) error {
	return os.ErrDeadlineExceeded
}
