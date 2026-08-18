package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/build/validate"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
)

// runTestSource is a project source file declaring one task instance of the
// fake module salmodules_test.go describes with testModuleOntology.
const runTestSource = `@prefix history: <salmodule://www.github.com/test/history-getter/> .
@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

<finder/1> a history:EducationalHistoryFinder ;
    history:maxRetries "5"^^xsd:integer .
`

type fakeTableHead struct {
	current *table.Snapshot
	tags    map[string]*table.Snapshot
}

func (f *fakeTableHead) CurrentSnapshot() *table.Snapshot           { return f.current }
func (f *fakeTableHead) SnapshotByName(name string) *table.Snapshot { return f.tags[name] }
func (f *fakeTableHead) CurrentSchema() *iceberg.Schema             { return nil }

func TestVerifyTableMatchesCommitAcceptsTheTaggedHead(t *testing.T) {
	snapshot := &table.Snapshot{SnapshotID: 7}
	head := &fakeTableHead{current: snapshot, tags: map[string]*table.Snapshot{"abc123": snapshot}}

	require.NoError(t, verifyTableMatchesCommit(head, "abc123"))
}

func TestVerifyTableMatchesCommitRejectsATableWithNoSnapshot(t *testing.T) {
	err := verifyTableMatchesCommit(&fakeTableHead{}, "abc123")

	require.Error(t, err)
	require.Contains(t, err.Error(), "sal build")
}

func TestVerifyTableMatchesCommitRejectsACommitTheTableWasNotBuiltFrom(t *testing.T) {
	snapshot := &table.Snapshot{SnapshotID: 7}
	head := &fakeTableHead{current: snapshot, tags: map[string]*table.Snapshot{"abc123": snapshot}}

	require.ErrorIs(t, verifyTableMatchesCommit(head, "def456"), ErrStaleDataProduct)
}

func TestVerifyTableMatchesCommitRejectsATagOnAnOlderSnapshot(t *testing.T) {
	head := &fakeTableHead{
		current: &table.Snapshot{SnapshotID: 8},
		tags:    map[string]*table.Snapshot{"abc123": {SnapshotID: 7}},
	}

	require.ErrorIs(t, verifyTableMatchesCommit(head, "abc123"), ErrStaleDataProduct)
}

func TestRunRefusesADirtyWorktree(t *testing.T) {
	original := uncommittedChangesInGit
	uncommittedChangesInGit = func() (bool, error) { return true, nil }
	t.Cleanup(func() { uncommittedChangesInGit = original })

	_, err := (&RunCmd{}).Run()

	require.ErrorIs(t, err, ErrRunUncommittedChanges)
}

func TestRunTellsTheUserToBuildWhenThereIsNoTable(t *testing.T) {
	originalChanges := uncommittedChangesInGit
	uncommittedChangesInGit = func() (bool, error) { return false, nil }
	originalHead := salTableHead
	salTableHead = func() (tableHead, error) {
		return nil, fmt.Errorf("load table: %w", catalog.ErrNoSuchTable)
	}
	t.Cleanup(func() {
		uncommittedChangesInGit = originalChanges
		salTableHead = originalHead
	})

	_, err := (&RunCmd{}).Run()

	require.Error(t, err)
	require.Contains(t, err.Error(), "sal build")
}

func TestRunRefusesWhenTheTableWasBuiltFromAnotherCommit(t *testing.T) {
	originalChanges := uncommittedChangesInGit
	uncommittedChangesInGit = func() (bool, error) { return false, nil }
	snapshot := &table.Snapshot{SnapshotID: 7}
	originalHead := salTableHead
	salTableHead = func() (tableHead, error) {
		return &fakeTableHead{current: snapshot, tags: map[string]*table.Snapshot{"oldcommit": snapshot}}, nil
	}
	originalCommit := gitCommitHash
	gitCommitHash = func() (string, error) { return "newcommit", nil }
	t.Cleanup(func() {
		uncommittedChangesInGit = originalChanges
		salTableHead = originalHead
		gitCommitHash = originalCommit
	})

	_, err := (&RunCmd{}).Run()

	require.ErrorIs(t, err, ErrStaleDataProduct)
}

// newRunTestProject creates a committed SAL project holding source and makes it
// the working directory, returning a helper that runs further git commands in
// it.
func newRunTestProject(t *testing.T, source string) func(args ...string) {
	t.Helper()

	project := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-c", "user.email=sal@example.test", "-c", "user.name=SAL"}, args...)...)
		command.Dir = project
		out, err := command.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}

	git("init")
	git("remote", "add", "origin", "https://github.com/cgs-earth/sal-run-test-project.git")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sal", "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"), []byte(".sal/data\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(project, "data.ttl"), []byte(source), 0644))
	git("add", "-A")
	git("commit", "-m", "project source")

	t.Chdir(project)
	return git
}

// serveModuleVocabulary makes the fake module's ontology resolvable during
// validation without docker or the network.
func serveModuleVocabulary(t *testing.T) {
	t.Helper()

	original := projectVocabularies
	projectVocabularies = func(refresh bool) (*validate.PinnedVocabularies, error) {
		pins, err := original(refresh)
		if err != nil {
			return nil, err
		}
		pins.Fetch = func(source string) ([]byte, string, validate.PinnedVersion, error) {
			if source != testModuleNamespace {
				return nil, "", validate.PinnedVersion{}, fmt.Errorf("bad response status code: 404")
			}
			return []byte(testModuleOntology), "application/ld+json", validate.PinnedVersion{}, nil
		}
		return pins, nil
	}
	t.Cleanup(func() { projectVocabularies = original })
}

// installFakeModuleRunner makes the shared resolver `sal run` materializes with
// clone and run the fake module through runner instead of git and docker.
func installFakeModuleRunner(t *testing.T, runner *testContainerRunner) {
	t.Helper()

	resolver := salmodule.Default()
	resolver.Reset()
	resolver.Runner = runner
	resolver.Command = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if args[0] == "rev-parse" {
			return []byte(testModuleCommitHash), nil
		}
		return nil, os.WriteFile(filepath.Join(args[len(args)-1], "Dockerfile"), []byte("FROM scratch\n"), 0644)
	}
	t.Cleanup(func() {
		resolver.Runner = nil
		resolver.Command = nil
		resolver.Reset()
	})
}

// buildAndCommitPins runs the builds that put a project in the state `sal run`
// requires: the first build pins the vocabularies into .sal/config.jsonld,
// committing that moves HEAD, and the second build tags the table with the
// commit the worktree now sits at.
func buildAndCommitPins(t *testing.T, git func(args ...string)) {
	t.Helper()

	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
	git("add", "-A")
	git("commit", "-m", "pin vocabularies")
	_, err = (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)
}

// TestRunCommitsModuleOutputOnTopOfABuild is the round trip of the build and
// run split: the builds commit the task's configuration without running
// anything, and run invokes the module and commits what it produced as a new
// snapshot.
func TestRunCommitsModuleOutputOnTopOfABuild(t *testing.T) {
	git := newRunTestProject(t, runTestSource)
	serveModuleVocabulary(t)
	runner := &testContainerRunner{
		ontology:  testModuleOntology,
		runOutput: `{"@id":"https://example.test/person/bob","@type":"schema:Person","schema:name":"Bob"}`,
	}
	installFakeModuleRunner(t, runner)

	buildAndCommitPins(t, git)
	require.Equal(t, 0, runner.runs)

	graph, err := (&RunCmd{}).Run()

	require.NoError(t, err)
	require.Equal(t, 1, runner.runs)
	require.True(t, graphHasTriple(graph, "https://example.test/person/bob", "https://schema.org/name", "Bob"))
}

// TestRunForceRunsWithADirtyWorktree checks the testing escape hatch: the pins
// the first build writes leave the worktree dirty, which refuses a normal run,
// and --force runs the module anyway.
func TestRunForceRunsWithADirtyWorktree(t *testing.T) {
	newRunTestProject(t, runTestSource)
	serveModuleVocabulary(t)
	runner := &testContainerRunner{
		ontology:  testModuleOntology,
		runOutput: `{"@id":"https://example.test/person/bob","@type":"schema:Person","schema:name":"Bob"}`,
	}
	installFakeModuleRunner(t, runner)

	_, err := (&BuildCmd{Format: GraphExportFormatIceberg}).Run()
	require.NoError(t, err)

	_, err = (&RunCmd{}).Run()
	require.ErrorIs(t, err, ErrRunUncommittedChanges)

	graph, err := (&RunCmd{Force: true}).Run()

	require.NoError(t, err)
	require.Equal(t, 1, runner.runs)
	require.True(t, graphHasTriple(graph, "https://example.test/person/bob", "https://schema.org/name", "Bob"))
}

func TestRunFailsWhenTheProjectDeclaresNoTasks(t *testing.T) {
	git := newRunTestProject(t, pinsTestSource)
	servePinsTestVocabulary(t)
	installFakeModuleRunner(t, &testContainerRunner{})

	buildAndCommitPins(t, git)

	_, err := (&RunCmd{}).Run()

	require.ErrorIs(t, err, ErrNoModuleTasks)
}
