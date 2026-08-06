package sparql

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

type fakeSQLRunner struct {
	result Result
	err    error
	sql    string
}

func (r *fakeSQLRunner) RunSQL(_ context.Context, sql string) (Result, error) {
	r.sql = sql
	return r.result, r.err
}

func TestSQLShellRunsTheStatementItOpenedOn(t *testing.T) {
	runner := &fakeSQLRunner{result: Result{Header: []string{"key"}}}
	model := newSQLShellModel(context.Background(), runner, "SELECT * FROM triples LIMIT 3")

	require.True(t, model.running, "the shell reports the opening query as running")
	msg := model.Init()()

	require.Equal(t, "SELECT * FROM triples LIMIT 3", runner.sql)
	require.Equal(t, Result{Header: []string{"key"}}, msg.(queryResultMsg).result)
}

func TestSQLShellPassesTheEditorStatementThroughUntranslated(t *testing.T) {
	// A statement that is not valid SPARQL proves nothing translated it.
	runner := &fakeSQLRunner{}
	model := newSQLShellModel(context.Background(), runner, "SELECT COUNT(*) FROM triples")

	model.Init()()

	require.Equal(t, "SELECT COUNT(*) FROM triples", runner.sql)
}

func TestSQLShellFallsBackToAStarterStatement(t *testing.T) {
	model := newSQLShellModel(context.Background(), &fakeSQLRunner{}, "  ")

	require.Equal(t, "SELECT * FROM triples LIMIT 20", model.query)
}

func TestSPARQLShellDoesNotRunAnythingOnOpen(t *testing.T) {
	require.Nil(t, newShellModel(context.Background(), &fakeRunner{}).Init())
}

func TestSQLShellHasNoSQLPageToSwitchTo(t *testing.T) {
	// The SQL page shows the SQL a SPARQL query was translated into, and in SQL
	// mode the editor already holds that statement.
	model := newSQLShellModel(context.Background(), &fakeSQLRunner{}, "SELECT 1")

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyF2})

	require.Equal(t, pageMain, updated.(shellModel).page)
	require.NotContains(t, updated.(shellModel).View().Content, "F2")
}

func TestSQLShellTitlesItselfAsSQL(t *testing.T) {
	content := newSQLShellModel(context.Background(), &fakeSQLRunner{}, "SELECT 1").View().Content

	require.Contains(t, content, "SAL SQL")
	require.NotContains(t, content, "SAL SPARQL")
}

func TestSQLShellHelpOmitsThePageSwitch(t *testing.T) {
	help := renderHelp(80, modeSQL)

	require.Contains(t, help, shellHelpKeyStyle.Render("Ctrl+R"))
	require.NotContains(t, help, shellHelpKeyStyle.Render("F2"))
}

func TestSQLShellEditorPlaceholderAsksForSQL(t *testing.T) {
	require.Contains(t, renderEditorBody(modeSQL, "", 0), "Enter a SQL SELECT query")
}

func TestSQLShellEditorHighlightsSQLKeywords(t *testing.T) {
	statement := "SELECT subject FROM triples"
	// The cursor sits past the end so that it does not overlay a keyword.
	body := renderEditorBody(modeSQL, statement, len(statement))

	require.Contains(t, body, sqlKeywordStyle.Render("SELECT"))
	require.Contains(t, body, sqlKeywordStyle.Render("FROM"))
}

func TestSQLAndSPARQLHistoriesAreKeptApart(t *testing.T) {
	// A SPARQL query and a SQL statement are not interchangeable in the editor,
	// so browsing history must never offer one in the other's shell.
	require.NotEqual(t, defaultHistoryDir(modeSQL), defaultHistoryDir(modeSPARQL))
}
