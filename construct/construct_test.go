package construct

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/turtle"
)

const constructQuery = `PREFIX ex: <http://example.org/>
CONSTRUCT { ?s ex:alias ?name } WHERE { ?s ex:name ?name }`

// fakeRunner answers every CONSTRUCT with the same rows and records the
// queries it was asked.
type fakeRunner struct {
	rows    [][]sql.NullString
	queries []string
}

func (f *fakeRunner) Construct(_ context.Context, query string, rowFn func([]sql.NullString) error) error {
	f.queries = append(f.queries, query)
	for _, row := range f.rows {
		if err := rowFn(row); err != nil {
			return err
		}
	}
	return nil
}

// row is one constructed triple whose object is an IRI or, with a datatype, a
// literal held in object_string.
func row(subject, predicate, iri, text, datatype string) []sql.NullString {
	value := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	return []sql.NullString{value(subject), value(predicate), value(iri), {}, {}, {}, {}, {}, value(text), value(datatype), {}}
}

// newProject creates a git repository with a sparql/ directory holding the
// given files and returns its root.
func newProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	init := exec.Command("git", "init", "-q")
	init.Dir = dir
	out, err := init.CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sal", "data"), 0755))
	for name, content := range files {
		path := filepath.Join(dir, QueryDir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	}
	return dir
}

func TestFindQueriesReturnsOnlyConstructQueries(t *testing.T) {
	dir := newProject(t, map[string]string{
		"aliases.rq":            constructQuery,
		"nested/aliases.sparql": constructQuery,
		"names.rq":              `SELECT * WHERE { ?s ?p ?o }`,
		"notes.md":              "CONSTRUCT is described here",
	})

	queries, err := FindQueries(dir)

	require.NoError(t, err)
	require.Len(t, queries, 2)
	require.Equal(t, "aliases", queries[0].Name)
	require.Equal(t, filepath.Join(dir, QueryDir, "aliases.rq"), queries[0].Path)
	require.Equal(t, constructQuery, queries[0].Text)
	require.Equal(t, filepath.Join("nested", "aliases"), queries[1].Name)
}

func TestFindQueriesFindsNothingWithoutASparqlDirectory(t *testing.T) {
	queries, err := FindQueries(t.TempDir())

	require.NoError(t, err)
	require.Empty(t, queries)
}

func TestFindQueriesNamesTheFileThatDoesNotParse(t *testing.T) {
	dir := newProject(t, map[string]string{"broken.rq": `CONSTRUCT { ?s ?p`})

	_, err := FindQueries(dir)

	require.ErrorContains(t, err, filepath.Join(QueryDir, "broken.rq"))
}

func TestFindQueriesRefusesAConstructSalCannotTranslate(t *testing.T) {
	dir := newProject(t, map[string]string{"optional.rq": `CONSTRUCT { ?s ?p ?o } WHERE { ?s ?p ?o OPTIONAL { ?o ?q ?r } }`})

	_, err := FindQueries(dir)

	require.ErrorContains(t, err, "optional.rq")
}

func TestFindQueriesRefusesTwoQueriesWithOneOutputName(t *testing.T) {
	dir := newProject(t, map[string]string{"aliases.rq": constructQuery, "aliases.sparql": constructQuery})

	_, err := FindQueries(dir)

	require.ErrorContains(t, err, "aliases.ttl")
}

func TestRunWritesEachQueryAsTurtleAndReturnsTheMergedGraph(t *testing.T) {
	dir := newProject(t, map[string]string{"aliases.rq": constructQuery, "nested/more.rq": constructQuery})
	queries, err := FindQueries(dir)
	require.NoError(t, err)
	runner := &fakeRunner{rows: [][]sql.NullString{
		row("http://example.org/a", "http://example.org/alias", "", "Ada", "http://www.w3.org/2001/XMLSchema#string"),
		row("http://example.org/a", "http://example.org/knows", "http://example.org/b", "", ""),
	}}

	merged, err := Run(context.Background(), runner, dir, queries)

	require.NoError(t, err)
	require.Equal(t, []string{constructQuery, constructQuery}, runner.queries)
	require.Equal(t, 2, merged.Len())
	for _, name := range []string{"aliases.ttl", filepath.Join("nested", "more.ttl")} {
		file, err := os.Open(filepath.Join(dir, ".sal", "constructed", name))
		require.NoError(t, err)
		written := rdflibgo.NewGraph()
		require.NoError(t, turtle.Parse(written, file))
		require.NoError(t, file.Close())
		require.Equal(t, 2, written.Len())
		content, err := os.ReadFile(filepath.Join(dir, ".sal", "constructed", name))
		require.NoError(t, err)
		require.Contains(t, string(content), "ex:knows ex:b")
		require.True(t, written.Contains(rdflibgo.NewURIRefUnsafe("http://example.org/a"), rdflibgo.NewURIRefUnsafe("http://example.org/knows"), rdflibgo.NewURIRefUnsafe("http://example.org/b")))
	}
}

func TestRunWipesWhatAnEarlierRunConstructed(t *testing.T) {
	dir := newProject(t, map[string]string{"aliases.rq": constructQuery})
	stale := filepath.Join(dir, ".sal", "constructed", "removed-query.ttl")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0755))
	require.NoError(t, os.WriteFile(stale, []byte("# stale\n"), 0644))
	queries, err := FindQueries(dir)
	require.NoError(t, err)

	_, err = Run(context.Background(), &fakeRunner{}, dir, queries)

	require.NoError(t, err)
	require.NoFileExists(t, stale)
	require.FileExists(t, filepath.Join(dir, ".sal", "constructed", "aliases.ttl"))
}

func TestRunWithoutQueriesOnlyWipes(t *testing.T) {
	dir := newProject(t, nil)
	stale := filepath.Join(dir, ".sal", "constructed", "removed-query.ttl")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0755))
	require.NoError(t, os.WriteFile(stale, []byte("# stale\n"), 0644))

	merged, err := Run(context.Background(), nil, dir, nil)

	require.NoError(t, err)
	require.Equal(t, 0, merged.Len())
	require.NoDirExists(t, filepath.Join(dir, ".sal", "constructed"))
}

func TestRunGitignoresTheDirectoryWhenTheProjectDoesNot(t *testing.T) {
	dir := newProject(t, map[string]string{"aliases.rq": constructQuery})
	queries, err := FindQueries(dir)
	require.NoError(t, err)

	_, err = Run(context.Background(), &fakeRunner{}, dir, queries)

	require.NoError(t, err)
	ignore, err := os.ReadFile(filepath.Join(dir, ".sal", "constructed", ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, "*\n", string(ignore))
	status := exec.Command("git", "status", "-s", ".sal/constructed")
	status.Dir = dir
	out, err := status.Output()
	require.NoError(t, err)
	require.Empty(t, string(out))
}

func TestRunLeavesTheDirectoryAloneWhenTheProjectAlreadyIgnoresIt(t *testing.T) {
	dir := newProject(t, map[string]string{"aliases.rq": constructQuery})
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".sal/constructed\n"), 0644))
	queries, err := FindQueries(dir)
	require.NoError(t, err)

	_, err = Run(context.Background(), &fakeRunner{}, dir, queries)

	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(dir, ".sal", "constructed", ".gitignore"))
}

func TestRunNamesTheQueryThatFailed(t *testing.T) {
	dir := newProject(t, map[string]string{"aliases.rq": constructQuery})
	queries, err := FindQueries(dir)
	require.NoError(t, err)
	runner := &fakeRunner{rows: [][]sql.NullString{row("http://example.org/a", "http://example.org/alias", "", "Ada", "")}}
	failing := failingRunner{runner}

	_, err = Run(context.Background(), failing, dir, queries)

	require.ErrorContains(t, err, "aliases.rq")
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
}

type failingRunner struct{ *fakeRunner }

func (failingRunner) Construct(context.Context, string, func([]sql.NullString) error) error {
	return os.ErrDeadlineExceeded
}
