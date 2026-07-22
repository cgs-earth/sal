package sparql

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	shellHistoryDir = func() string { return "" }
	os.Exit(m.Run())
}

type fakeRunner struct {
	result Result
	err    error
	query  string
}

func (r *fakeRunner) Run(_ context.Context, query string) (Result, error) {
	r.query = query
	return r.result, r.err
}

func TestParseCSVResultReadsHeaderAndRows(t *testing.T) {
	header, rows, err := parseCSVResult("s,name\nhttps://example.org/a,bob\n")

	require.NoError(t, err)
	require.Equal(t, []string{"s", "name"}, header)
	require.Equal(t, [][]string{{"https://example.org/a", "bob"}}, rows)
}

func TestRenderTableShowsHeadersAndRows(t *testing.T) {
	table := renderTable(
		[]string{"subject", "name"},
		[][]string{{"https://example.org/alice", "bob"}},
		80,
		false,
		0,
		false,
	)

	require.Contains(t, table, "subject")
	require.Contains(t, table, "name")
	require.Contains(t, table, "https://example.org/alice")
	require.Contains(t, table, "bob")
}

func TestRenderTableHighlightsFocusedRowOnly(t *testing.T) {
	table := renderTable(
		[]string{"subject"},
		[][]string{{"row one"}, {"row two"}},
		80,
		true,
		1,
		false,
	)

	require.Contains(t, table, tableFocusedRowStyle.Render("row two"))
	require.NotContains(t, table, tableFocusedRowStyle.Render("row one"))
}

func TestRenderTableHighlightsAllSelectedRows(t *testing.T) {
	table := renderTable(
		[]string{"subject"},
		[][]string{{"row one"}, {"row two"}},
		80,
		true,
		0,
		true,
	)

	require.Contains(t, table, tableFocusedRowStyle.Render("row one"))
	require.Contains(t, table, tableFocusedRowStyle.Render("row two"))
}

func TestSyntaxHighlightStylesSPARQLParts(t *testing.T) {
	highlighted := syntaxHighlight(`PREFIX schema: <https://schema.org/>
SELECT ?s WHERE { ?s schema:name "bob" . }`)

	require.Contains(t, highlighted, "\x1b[")
	require.Contains(t, highlighted, "PREFIX")
	require.Contains(t, highlighted, "?s")
	require.Contains(t, highlighted, "schema:name")
	require.Contains(t, highlighted, `"bob"`)
}

func TestHighlightSQLStylesKeywordsAndStrings(t *testing.T) {
	highlighted := highlightSQL(`SELECT t0.subject AS s FROM triples AS t0 JOIN other AS o ON o.s = 'bob'`)

	require.Contains(t, highlighted, "\x1b[")
	require.Contains(t, highlighted, sqlKeywordStyle.Render("SELECT"))
	require.Contains(t, highlighted, sqlKeywordStyle.Render("JOIN"))
	require.Contains(t, highlighted, sqlStringStyle.Render("'bob'"))
	require.Contains(t, highlighted, "t0.subject")
}

func TestShellViewDelineatesEditorAndResults(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	view := model.View().Content

	require.Contains(t, view, "Editor")
	require.Contains(t, view, "History")
	require.Contains(t, view, "Results")
	require.Contains(t, view, "F2: SQL View")
	require.Contains(t, view, "Run a query to show results here.")
	require.NotContains(t, view, "Run a query to show generated SQL.")
	require.Contains(t, view, "\x1b[")
}

func TestShellViewEnablesMouseSelection(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	view := model.View()

	require.Equal(t, tea.MouseModeCellMotion, view.MouseMode)
}

func TestShellHelpStylesKeyboardDescriptions(t *testing.T) {
	help := renderHelp(80)

	require.Contains(t, help, shellHelpKeyStyle.Render("Ctrl+H"))
	require.Contains(t, help, shellHelpDescriptionStyle.Render("toggle help"))
	require.Contains(t, help, shellHelpKeyStyle.Render("F2"))
	require.Contains(t, help, shellHelpDescriptionStyle.Render("switch main and SQL pages"))
	require.Contains(t, help, shellHelpKeyStyle.Render("Shift+←/→"))
	require.Contains(t, help, shellHelpDescriptionStyle.Render("change focus"))
	require.Contains(t, help, shellHelpKeyStyle.Render("Ctrl+U"))
	require.Contains(t, help, shellHelpDescriptionStyle.Render("clear current editor line"))
	require.NotContains(t, help, "row/table csv")
	require.Contains(t, help, shellHelpDescriptionStyle.Render("copy selection or SQL page"))
	require.Contains(t, help, shellHelpDescriptionStyle.Render("quit"))
}

func TestShellHelpLayerIsToggledWithCtrlH(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})

	require.NotContains(t, model.View().Content, "toggle help")

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	model = updated.(shellModel)

	require.Contains(t, model.View().Content, "Help")
	require.Contains(t, model.View().Content, "run query")
}

func TestRenderHistoryShowsCompactCountByDefault(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.history = []historyEntry{{Query: "SELECT ?s WHERE {}"}}
	model.historyIndex = 0

	history := model.renderHistory(50)

	require.Contains(t, history, historyTitleStyle.Render("History"))
	require.Contains(t, history, shellMutedStyle.Render("1/1"))
	require.NotContains(t, history, "SELECT ?s WHERE {}")
}

func TestRenderHistoryUsesFocusedStyle(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.focus = focusHistory
	model.history = []historyEntry{{Query: "SELECT ?s WHERE {}"}}
	model.historyIndex = 0

	history := model.renderHistory(50)

	require.Contains(t, history, focusedHistoryTitleStyle.Render("History"))
	require.Contains(t, history, shellMutedStyle.Render("←"))
	require.Contains(t, history, shellMutedStyle.Render("→"))
	require.Contains(t, history, shellMutedStyle.Render("1/1"))
	require.NotContains(t, history, "SELECT ?s WHERE {}")
}

func TestSQLRendersOnSeparatePageNotMainViewOrResults(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.result = Result{
		SQL:     "SELECT t0.subject AS s FROM triples AS t0",
		Header:  []string{"s"},
		Rows:    [][]string{{"https://example.org/alice"}},
		Message: "1 rows",
	}

	mainView := model.View().Content
	resultsPanel := model.renderResults(80, 20)
	model.page = pageSQL
	sqlPage := model.View().Content

	require.NotContains(t, mainView, "SELECT t0.subject")
	require.NotContains(t, resultsPanel, "SELECT t0.subject")
	require.Contains(t, sqlPage, "SELECT")
	require.Contains(t, sqlPage, "F2 main")
	require.NotContains(t, sqlPage, "Ctrl+C copy")
	require.Contains(t, sqlPage, sqlKeywordStyle.Render("SELECT"))
}

func TestRenderSQLKeepsTitleAbovePanel(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.page = pageSQL
	model.result = Result{SQL: "SELECT t0.subject AS s FROM triples AS t0"}

	sqlView := model.View().Content

	titleIndex := strings.Index(sqlView, "Generated SQL")
	borderIndex := strings.Index(sqlView, "┏")
	require.NotEqual(t, -1, titleIndex)
	require.NotEqual(t, -1, borderIndex)
	require.Less(t, titleIndex, borderIndex)
	require.Contains(t, sqlView[:titleIndex], "\n")
	require.NotContains(t, sqlView[strings.Index(sqlView, "┏"):], "Generated SQL")
}

func TestRenderEditorKeepsStatusOutsideEditorBox(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	editor := model.renderEditor(80)

	editorIndex := strings.Index(editor, "Editor")
	borderIndex := strings.Index(editor, "┏")
	require.NotEqual(t, -1, editorIndex)
	require.NotEqual(t, -1, borderIndex)
	require.Less(t, editorIndex, borderIndex)
}

func TestRenderEditorUsesNeutralBorderWhenEditorIsBlurred(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT"
	model.cursor = len(model.query)
	model.focus = focusHistory

	editor := model.renderEditor(80)

	require.Contains(t, editor, sectionTitleStyle.Render("Editor"))
	require.NotContains(t, editor, focusedSectionTitleStyle.Render("Editor"))
	require.Contains(t, editor, "\x1b[38;2;88;91;112m")
}

func TestRenderEditorUsesThickBorderWhenFocused(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT"
	model.cursor = len(model.query)

	editor := model.renderEditor(80)

	require.Contains(t, editor, "┏")
	require.Contains(t, editor, "┗")
}

func TestRenderEditorBodyShowsCursor(t *testing.T) {
	body := renderEditorBody(`SELECT ?s WHERE { ?s ?p ?o . }`, 7)

	require.Contains(t, body, "\x1b[")
	require.Contains(t, body, "SELECT")
	require.Contains(t, body, editorCursorStyle.Render("?"))
}

func TestRenderEmptyEditorBodyShowsPlaceholderAndCursor(t *testing.T) {
	body := renderEditorBody("", 0)

	require.Contains(t, body, "Enter a SPARQL SELECT query")
	require.Contains(t, body, editorCursorStyle.Render(" "))
}

func TestRenderEditorBodyShowsSelection(t *testing.T) {
	body := renderEditorBody("SELECT ?s WHERE {}", len("SELECT ?s"), len("SELECT "), len("SELECT ?s"))

	require.Contains(t, body, editorSelectionStyle.Render("?s"))
}

func TestQueryOffsetAtLineColumnClampsToLineEnd(t *testing.T) {
	query := "abc\ndef"

	require.Equal(t, 3, queryOffsetAtLineColumn(query, 0, 20))
	require.Equal(t, len("abc\nde"), queryOffsetAtLineColumn(query, 1, 2))
	require.Equal(t, len(query), queryOffsetAtLineColumn(query, 10, 0))
}

func TestMoveCursorVerticallyMovesBetweenLines(t *testing.T) {
	query := "abc\ndefgh\nij"

	require.Equal(t, 6, moveCursorVertically(query, 2, 1))
	require.Equal(t, 2, moveCursorVertically(query, 6, -1))
	require.Equal(t, 12, moveCursorVertically(query, 8, 1))
}

func TestShellModelEditsAtCursor(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT  WHERE {}"
	model.cursor = len("SELECT ")

	updated, _ := model.Update(tea.KeyPressMsg{Text: "?s", Code: 's'})
	model = updated.(shellModel)

	require.Equal(t, "SELECT ?s WHERE {}", model.query)
	require.Equal(t, len("SELECT ?s"), model.cursor)
}

func TestShellModelSelectsAllWithCtrlA(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.cursor = 0

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	model = updated.(shellModel)

	require.Equal(t, 0, model.selectionStart)
	require.Equal(t, len([]rune(model.query)), model.selectionEnd)
	require.Equal(t, len([]rune(model.query)), model.cursor)
}

func TestShellModelTabInsertsTabWhileEditorIsActive(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT  WHERE {}"
	model.cursor = len("SELECT ")
	model.result = Result{Header: []string{"s"}, Rows: [][]string{{"row"}}}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(shellModel)

	require.Equal(t, focusEditor, model.focus)
	require.Equal(t, "SELECT \t WHERE {}", model.query)
	require.Equal(t, len("SELECT \t"), model.cursor)
}

func TestShellModelEscapeDoesNotChangeEditorState(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.cursor = len("SELECT ?s")

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	model = updated.(shellModel)

	require.Equal(t, focusEditor, model.focus)
	require.Equal(t, "SELECT ?s WHERE {}", model.query)
	require.Equal(t, len("SELECT ?s"), model.cursor)
}

func TestShellModelShiftRightChangesFocusToResults(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.result = Result{Header: []string{"s"}, Rows: [][]string{{"row"}}}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyRightShift})
	model = updated.(shellModel)

	require.Equal(t, focusResults, model.focus)
}

func TestShellModelShiftLeftBackToEditorReactivatesEditing(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT "
	model.cursor = len(model.query)
	model.result = Result{Header: []string{"s"}, Rows: [][]string{{"row"}}}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyRightShift})
	model = updated.(shellModel)
	require.Equal(t, focusResults, model.focus)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyLeftShift})
	model = updated.(shellModel)
	require.Equal(t, focusEditor, model.focus)

	updated, _ = model.Update(tea.KeyPressMsg{Text: "?s", Code: 's'})
	model = updated.(shellModel)
	require.Equal(t, "SELECT ?s", model.query)
}

func TestShellModelF2NavigatesToSQLPage(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.result = Result{SQL: "SELECT t0.subject AS s FROM triples AS t0"}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	model = updated.(shellModel)

	require.Equal(t, pageSQL, model.page)
	require.Contains(t, model.View().Content, sqlKeywordStyle.Render("SELECT"))
	require.Contains(t, model.View().Content, "t0.subject")
}

func TestShellModelF2ReturnsToMainPage(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.page = pageSQL
	model.result = Result{SQL: "SELECT t0.subject AS s FROM triples AS t0"}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	model = updated.(shellModel)

	require.Equal(t, pageMain, model.page)
	require.NotContains(t, model.View().Content, "SELECT t0.subject")
	require.Contains(t, model.View().Content, "Results")
}

func TestShellModelEscapeReturnsFromSQLPage(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.page = pageSQL

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	model = updated.(shellModel)

	require.Equal(t, pageMain, model.page)
}

func TestShellModelHistoryIgnoresTextInput(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.focus = focusHistory
	model.query = "SELECT "
	model.cursor = len(model.query)

	updated, _ := model.Update(tea.KeyPressMsg{Text: "?s", Code: 's'})
	model = updated.(shellModel)

	require.Equal(t, focusHistory, model.focus)
	require.Equal(t, "SELECT ", model.query)
}

func TestShellModelMouseClickMovesEditorCursor(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.cursor = 0
	x, y, _, _ := model.editorBodyBounds()

	updated, _ := model.Update(tea.MouseClickMsg{X: x + len("SELECT "), Y: y, Button: tea.MouseLeft})
	model = updated.(shellModel)

	require.Equal(t, focusEditor, model.focus)
	require.Equal(t, len("SELECT "), model.cursor)
	require.Equal(t, model.cursor, model.selectionStart)
	require.Equal(t, model.cursor, model.selectionEnd)
	require.True(t, model.mouseSelecting)
}

func TestShellModelMouseDragSelectsEditorText(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	x, y, _, _ := model.editorBodyBounds()

	updated, _ := model.Update(tea.MouseClickMsg{X: x + len("SELECT "), Y: y, Button: tea.MouseLeft})
	model = updated.(shellModel)
	updated, _ = model.Update(tea.MouseMotionMsg{X: x + len("SELECT ?s"), Y: y, Button: tea.MouseLeft})
	model = updated.(shellModel)

	require.Equal(t, len("SELECT "), model.selectionStart)
	require.Equal(t, len("SELECT ?s"), model.selectionEnd)
	require.Equal(t, len("SELECT ?s"), model.cursor)
	text, ok := model.selectedEditorText()
	require.True(t, ok)
	require.Equal(t, "?s", text)
}

func TestShellModelMouseReleaseFinishesEditorSelection(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s\nWHERE {}"
	x, y, _, _ := model.editorBodyBounds()

	updated, _ := model.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	model = updated.(shellModel)
	updated, _ = model.Update(tea.MouseReleaseMsg{X: x + len("WHERE"), Y: y + 1, Button: tea.MouseLeft})
	model = updated.(shellModel)

	require.False(t, model.mouseSelecting)
	require.Equal(t, 0, model.selectionStart)
	require.Equal(t, len("SELECT ?s\nWHERE"), model.selectionEnd)
	require.Equal(t, len("SELECT ?s\nWHERE"), model.cursor)
}

func TestShellModelMouseClickOutsideEditorDoesNotMoveCursor(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.cursor = len(model.query)

	updated, _ := model.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	model = updated.(shellModel)

	require.Equal(t, len("SELECT ?s WHERE {}"), model.cursor)
	require.False(t, model.mouseSelecting)
}

func TestShellModelMouseClickBelowEditorDoesNotMoveCursor(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.cursor = len(model.query)
	x, y, _, height := model.editorBodyBounds()

	updated, _ := model.Update(tea.MouseClickMsg{X: x, Y: y + height + 1, Button: tea.MouseLeft})
	model = updated.(shellModel)

	require.Equal(t, len("SELECT ?s WHERE {}"), model.cursor)
	require.False(t, model.mouseSelecting)
}

func TestShellModelIgnoresMetaA(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.cursor = 0

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModMeta})
	model = updated.(shellModel)

	require.Equal(t, 0, model.selectionStart)
	require.Equal(t, 0, model.selectionEnd)
	require.Equal(t, 0, model.cursor)
}

func TestShellModelCopiesSelectedTextWithCtrlC(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.selectionStart = len("SELECT ")
	model.selectionEnd = len("SELECT ?s")
	model.cursor = model.selectionEnd

	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(shellModel)

	require.Equal(t, len("SELECT ?s"), model.cursor)
	require.Equal(t, "Copied to clipboard", model.copyFlash)
	require.Equal(t, focusEditor, model.copyFlashFocus)
	require.Contains(t, model.renderEditor(80), editorFlashStyle.Render("Copied to clipboard"))
	require.NotNil(t, cmd)
	batch := cmd().(tea.BatchMsg)
	require.Len(t, batch, 2)
	require.Equal(t, "?s", fmt.Sprint(batch[0]()))
}

func TestShellModelClearsCopyFlash(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.copyFlash = "Copied to clipboard"
	model.copyFlashID = 3

	updated, _ := model.Update(clearCopyFlashMsg{id: 3})
	model = updated.(shellModel)

	require.Empty(t, model.copyFlash)
}

func TestShellModelIgnoresStaleCopyFlashClear(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.copyFlash = "Copied to clipboard"
	model.copyFlashID = 3

	updated, _ := model.Update(clearCopyFlashMsg{id: 2})
	model = updated.(shellModel)

	require.Equal(t, "Copied to clipboard", model.copyFlash)
}

func TestShellModelIgnoresMetaC(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.selectionStart = len("SELECT ")
	model.selectionEnd = len("SELECT ?s")
	model.cursor = model.selectionEnd

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModMeta})

	require.Nil(t, cmd)
}

func TestShellModelCopyShortcutDoesNotQuitWithoutSelection(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	require.Nil(t, cmd)
}

func TestShellModelRequestsClipboardWithCtrlV(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})

	require.NotNil(t, cmd)
	require.NotNil(t, cmd())
}

func TestShellModelSelectsAllResultsWithCtrlA(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.focus = focusResults
	model.result = Result{
		Header: []string{"s"},
		Rows:   [][]string{{"row one"}, {"row two"}},
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	model = updated.(shellModel)

	require.True(t, model.resultsAllSelected)
	require.Equal(t, 0, model.selectionStart)
	require.Equal(t, 0, model.selectionEnd)
}

func TestShellModelCopiesSelectedResultRowWithCtrlC(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.focus = focusResults
	model.selectedRow = 1
	model.result = Result{
		Header: []string{"s", "name"},
		Rows:   [][]string{{"https://example.org/alice", "alice"}, {"https://example.org/bob", "bob"}},
	}

	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(shellModel)

	require.Equal(t, "Copied to clipboard", model.copyFlash)
	require.Equal(t, focusResults, model.copyFlashFocus)
	require.Contains(t, model.renderResults(80, 20), editorFlashStyle.Render("Copied to clipboard"))
	require.NotContains(t, model.renderEditor(80), editorFlashStyle.Render("Copied to clipboard"))
	require.NotNil(t, cmd)
	batch := cmd().(tea.BatchMsg)
	require.Equal(t, "https://example.org/bob,bob\n", fmt.Sprint(batch[0]()))
}

func TestShellModelCopiesAllSelectedResultsAsCSVWithCtrlC(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.focus = focusResults
	model.resultsAllSelected = true
	model.result = Result{
		Header: []string{"s", "name"},
		Rows:   [][]string{{"https://example.org/alice", "alice"}, {"https://example.org/bob", "bob"}},
	}

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	require.NotNil(t, cmd)
	batch := cmd().(tea.BatchMsg)
	require.Equal(t, "s,name\nhttps://example.org/alice,alice\nhttps://example.org/bob,bob\n", fmt.Sprint(batch[0]()))
}

func TestShellModelCopiesSQLPageWithCtrlC(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.page = pageSQL
	model.result = Result{SQL: "SELECT t0.subject AS s FROM triples AS t0"}

	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(shellModel)

	require.Equal(t, "Copied to clipboard", model.copyFlash)
	require.Equal(t, focusSQL, model.copyFlashFocus)
	require.Contains(t, model.renderSQL(80), editorFlashStyle.Render("Copied to clipboard"))
	require.NotNil(t, cmd)
	batch := cmd().(tea.BatchMsg)
	require.Equal(t, "SELECT t0.subject AS s FROM triples AS t0", fmt.Sprint(batch[0]()))
}

func TestShellModelSQLPageCtrlCIgnoresEmptySQL(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.page = pageSQL

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	require.Nil(t, cmd)
}

func TestShellModelMovingResultsClearsAllResultsSelection(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.focus = focusResults
	model.resultsAllSelected = true
	model.result = Result{
		Header: []string{"s"},
		Rows:   [][]string{{"row one"}, {"row two"}},
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(shellModel)

	require.False(t, model.resultsAllSelected)
	require.Equal(t, 1, model.selectedRow)
}

func TestShellModelPagesThroughResults(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.focus = focusResults
	model.height = 12
	model.result = Result{
		Header: []string{"s"},
		Rows: [][]string{
			{"row 0"}, {"row 1"}, {"row 2"}, {"row 3"}, {"row 4"},
		},
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	model = updated.(shellModel)
	require.Equal(t, 2, model.selectedRow)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	model = updated.(shellModel)
	require.Equal(t, 0, model.selectedRow)
}

func TestShellModelPageResultsClearsAllResultsSelection(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.focus = focusResults
	model.height = 12
	model.resultsAllSelected = true
	model.result = Result{
		Header: []string{"s"},
		Rows:   [][]string{{"row 0"}, {"row 1"}, {"row 2"}},
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	model = updated.(shellModel)

	require.False(t, model.resultsAllSelected)
}

func TestShellModelCtrlLClearsScreen(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})

	require.NotNil(t, cmd)
	require.NotNil(t, cmd())
}

func TestShellModelQuitsWithCtrlD(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})

	require.NotNil(t, cmd)
}

func TestShellModelDoesNotQuitWithCtrlQ(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})

	require.Nil(t, cmd)
}

func TestSaveQueryHistoryWritesAndLoadsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	older := "SELECT ?older WHERE {}"
	newer := "SELECT ?newer WHERE {}"

	_, err := saveQueryHistory(dir, older)
	require.NoError(t, err)
	oldTime := time.Now().Add(-time.Hour)
	for _, path := range historyFiles(t, dir) {
		require.NoError(t, os.Chtimes(path, oldTime, oldTime))
	}
	_, err = saveQueryHistory(dir, newer)
	require.NoError(t, err)

	history, err := loadQueryHistory(dir)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, newer, history[0].Query)
	require.Equal(t, older, history[1].Query)
}

func TestSaveQueryHistoryKeepsOneHundredQueries(t *testing.T) {
	dir := t.TempDir()

	for i := 0; i < 105; i++ {
		_, err := saveQueryHistory(dir, fmt.Sprintf("SELECT ?s WHERE { ?s ?p %d . }", i))
		require.NoError(t, err)
	}

	history, err := loadQueryHistory(dir)
	require.NoError(t, err)
	require.Len(t, history, 100)
}

func TestShellModelKeepsHistoryVisibleWhenEditorCursorMoves(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.cursor = 0
	model.history = []historyEntry{
		{Query: "SELECT ?new WHERE {}"},
		{Query: "SELECT ?old WHERE {}"},
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	model = updated.(shellModel)

	require.Equal(t, focusEditor, model.focus)
	require.Equal(t, -1, model.historyIndex)
	require.Equal(t, "SELECT ?s WHERE {}", model.query)
	require.Contains(t, model.View().Content, "History")
	require.Contains(t, model.renderHistory(50), shellMutedStyle.Render("0/2"))
	require.Contains(t, model.renderHistory(50), shellMutedStyle.Render("←"))
	require.Contains(t, model.renderHistory(50), shellMutedStyle.Render("→"))
}

func TestShellModelDoesNotShowHistoryWhenUpMovesEditorCursor(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "abc\ndef"
	model.cursor = len("abc\nd")
	model.history = []historyEntry{{Query: "SELECT ?old WHERE {}"}}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	model = updated.(shellModel)

	require.Equal(t, focusEditor, model.focus)
	require.Equal(t, 1, model.cursor)
}

func TestShellModelFocusesHistoryWithShiftLeftAndLoadsQueriesWithLeftRight(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.cursor = 0
	model.history = []historyEntry{
		{Query: "SELECT ?new WHERE {}"},
		{Query: "SELECT ?old WHERE {}"},
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyLeftShift})
	model = updated.(shellModel)
	require.Equal(t, focusHistory, model.focus)
	require.Equal(t, "SELECT ?s WHERE {}", model.query)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	model = updated.(shellModel)
	require.Equal(t, focusHistory, model.focus)
	require.Equal(t, 0, model.historyIndex)
	require.Equal(t, "SELECT ?new WHERE {}", model.query)
	require.Equal(t, len([]rune(model.query)), model.cursor)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	model = updated.(shellModel)
	require.Equal(t, focusHistory, model.focus)
	require.Equal(t, 1, model.historyIndex)
	require.Equal(t, "SELECT ?old WHERE {}", model.query)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model = updated.(shellModel)
	require.Equal(t, focusHistory, model.focus)
	require.Equal(t, 0, model.historyIndex)
	require.Equal(t, "SELECT ?new WHERE {}", model.query)
}

func TestShellModelShiftRightReturnsFromHistoryToEditor(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.focus = focusHistory
	model.history = []historyEntry{{Query: "SELECT ?old WHERE {}"}}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyRightShift})
	model = updated.(shellModel)

	require.Equal(t, focusEditor, model.focus)
}

func TestShellModelSavesSubmittedQueryToHistory(t *testing.T) {
	runner := &fakeRunner{result: Result{Header: []string{"s"}, Rows: [][]string{{"a"}}, Message: "1 rows"}}
	model := newShellModel(context.Background(), runner)
	model.historyDir = t.TempDir()
	model.query = "SELECT ?s WHERE {}"

	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	model = updated.(shellModel)
	require.NotNil(t, cmd)

	for _, batchCmd := range cmd().(tea.BatchMsg) {
		msg := batchCmd()
		updated, _ = model.Update(msg)
		model = updated.(shellModel)
	}

	require.Len(t, model.history, 1)
	require.Equal(t, "SELECT ?s WHERE {}", model.history[0].Query)
}

func historyFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths
}

func TestShellModelPastesClipboardAtCursor(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT  WHERE {}"
	model.cursor = len("SELECT ")

	updated, _ := model.Update(tea.ClipboardMsg{Content: "?s"})
	model = updated.(shellModel)

	require.Equal(t, "SELECT ?s WHERE {}", model.query)
	require.Equal(t, len("SELECT ?s"), model.cursor)
}

func TestShellModelPastesBracketedPasteAtCursor(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT  WHERE {}"
	model.cursor = len("SELECT ")

	updated, _ := model.Update(tea.PasteMsg{Content: "?s"})
	model = updated.(shellModel)

	require.Equal(t, "SELECT ?s WHERE {}", model.query)
	require.Equal(t, len("SELECT ?s"), model.cursor)
}

func TestShellModelTypingReplacesSelection(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.selectionStart = len("SELECT ")
	model.selectionEnd = len("SELECT ?s")
	model.cursor = model.selectionEnd

	updated, _ := model.Update(tea.KeyPressMsg{Text: "?o", Code: 'o'})
	model = updated.(shellModel)

	require.Equal(t, "SELECT ?o WHERE {}", model.query)
	require.Equal(t, len("SELECT ?o"), model.cursor)
	require.Equal(t, model.cursor, model.selectionStart)
	require.Equal(t, model.cursor, model.selectionEnd)
}

func TestShellModelBackspaceDeletesSelection(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?s WHERE {}"
	model.selectionStart = len("SELECT ")
	model.selectionEnd = len("SELECT ?s")
	model.cursor = model.selectionEnd

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	model = updated.(shellModel)

	require.Equal(t, "SELECT  WHERE {}", model.query)
	require.Equal(t, len("SELECT "), model.cursor)
}

func TestDeleteWordBeforeCursorDeletesPreviousWord(t *testing.T) {
	query, cursor := deleteWordBeforeCursor("SELECT ?subject WHERE", len("SELECT ?subject"))

	require.Equal(t, "SELECT  WHERE", query)
	require.Equal(t, len("SELECT "), cursor)
}

func TestShellModelDeletesWordWithCtrlBackspace(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?subject WHERE {}"
	model.cursor = len("SELECT ?subject")

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModCtrl})
	model = updated.(shellModel)

	require.Equal(t, "SELECT  WHERE {}", model.query)
	require.Equal(t, len("SELECT "), model.cursor)
}

func TestShellModelDeletesWordWithAltBackspace(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?subject WHERE {}"
	model.cursor = len("SELECT ?subject")

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt})
	model = updated.(shellModel)

	require.Equal(t, "SELECT  WHERE {}", model.query)
	require.Equal(t, len("SELECT "), model.cursor)
}

func TestShellModelMovesCursorWithArrows(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "abc\ndef"
	model.cursor = 5

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	model = updated.(shellModel)
	require.Equal(t, 1, model.cursor)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(shellModel)
	require.Equal(t, 5, model.cursor)
}

func TestMoveCursorLineStartAndEnd(t *testing.T) {
	query := "abc\ndefgh\nij"

	require.Equal(t, 4, moveCursorLineStart(query, 6))
	require.Equal(t, 9, moveCursorLineEnd(query, 6))
}

func TestDeleteCurrentLineDeletesFocusedLine(t *testing.T) {
	query, cursor := deleteCurrentLine("abc\ndefgh\nij", 6)

	require.Equal(t, "abc\nij", query)
	require.Equal(t, 4, cursor)
}

func TestDeleteCurrentLineDeletesLastLineAndPrecedingNewline(t *testing.T) {
	query, cursor := deleteCurrentLine("abc\ndef", len("abc\ndef"))

	require.Equal(t, "abc", query)
	require.Equal(t, 3, cursor)
}

func TestShellModelMovesCursorToLineBoundsWithHomeAndEnd(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "abc\ndefgh\nij"
	model.cursor = 6

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	model = updated.(shellModel)
	require.Equal(t, 4, model.cursor)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	model = updated.(shellModel)
	require.Equal(t, 9, model.cursor)
}

func TestShellModelDeletesCurrentLineWithCtrlU(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "abc\ndefgh\nij"
	model.cursor = 6

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	model = updated.(shellModel)

	require.Equal(t, "abc\nij", model.query)
	require.Equal(t, 4, model.cursor)
}

func TestMoveCursorWordLeftAndRight(t *testing.T) {
	query := "SELECT ?subject WHERE"

	require.Equal(t, len("SELECT "), moveCursorWordLeft(query, len("SELECT ?subject")))
	require.Equal(t, len("SELECT ?subject"), moveCursorWordRight(query, len("SELECT ")))
}

func TestShellModelMovesCursorByWordWithPlatformModifier(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?subject WHERE"
	model.cursor = len("SELECT ?subject")

	mod, _ := wordJumpModifier()
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: mod})
	model = updated.(shellModel)
	require.Equal(t, len("SELECT "), model.cursor)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: mod})
	model = updated.(shellModel)
	require.Equal(t, len("SELECT ?subject"), model.cursor)
}

func TestShellModelMovesCursorByWordWithDedicatedModifiedArrowCodes(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?subject WHERE"
	model.cursor = len("SELECT ?subject")

	leftCode, rightCode := wordJumpArrowCodes()
	updated, _ := model.Update(tea.KeyPressMsg{Code: leftCode})
	model = updated.(shellModel)
	require.Equal(t, len("SELECT "), model.cursor)

	updated, _ = model.Update(tea.KeyPressMsg{Code: rightCode})
	model = updated.(shellModel)
	require.Equal(t, len("SELECT ?subject"), model.cursor)
}

func TestShellModelMovesCursorByWordWithMacOptionBFEncoding(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Option+B/F word navigation is macOS-specific")
	}
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?subject WHERE"
	model.cursor = len("SELECT ?subject")

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt})
	model = updated.(shellModel)
	require.Equal(t, len("SELECT "), model.cursor)

	updated, _ = model.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt})
	model = updated.(shellModel)
	require.Equal(t, len("SELECT ?subject"), model.cursor)
}

func TestShellModelIgnoresInactiveWordJumpModifier(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.query = "SELECT ?subject WHERE"
	model.cursor = len("SELECT ?subject")

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: inactiveWordJumpModifier()})
	model = updated.(shellModel)

	require.Equal(t, len("SELECT ?subject"), model.cursor)
}

func inactiveWordJumpModifier() tea.KeyMod {
	mod, _ := wordJumpModifier()
	if mod == tea.ModAlt {
		return tea.ModCtrl
	}
	return tea.ModAlt
}

func wordJumpArrowCodes() (rune, rune) {
	if runtime.GOOS == "darwin" {
		return tea.KeyLeftAlt, tea.KeyRightAlt
	}
	return tea.KeyLeftCtrl, tea.KeyRightCtrl
}

func TestShellModelTabsIntoResultsAndMovesFocusedRow(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.result = Result{
		Header: []string{"s"},
		Rows:   [][]string{{"a"}, {"b"}, {"c"}},
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(shellModel)
	require.Equal(t, focusEditor, model.focus)
	require.Contains(t, model.query, "\t")

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyRightShift})
	model = updated.(shellModel)
	require.Equal(t, focusResults, model.focus)
	require.Equal(t, 0, model.selectedRow)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(shellModel)
	require.Equal(t, 1, model.selectedRow)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	model = updated.(shellModel)
	require.Equal(t, 0, model.selectedRow)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyLeftShift})
	model = updated.(shellModel)
	require.Equal(t, focusEditor, model.focus)
}

func TestShellModelScrollsResultsWithFocusedRow(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.height = 10
	model.focus = focusResults
	model.result = Result{
		Header: []string{"s"},
		Rows: [][]string{
			{"row 0"},
			{"row 1"},
			{"row 2"},
			{"row 3"},
		},
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(shellModel)
	require.Equal(t, 1, model.selectedRow)
	require.Equal(t, 1, model.resultOffset)

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(shellModel)
	require.Equal(t, 2, model.selectedRow)
	require.Equal(t, 2, model.resultOffset)
	require.Equal(t, [][]string{{"row 2"}}, model.visibleResultRows())
}

func TestResultWindowKeepsSelectedRowVisible(t *testing.T) {
	rows := [][]string{
		{"row 0"},
		{"row 1"},
		{"row 2"},
		{"row 3"},
		{"row 4"},
	}

	offset, visible := resultWindow(rows, 4, 0, 2)

	require.Equal(t, 3, offset)
	require.Equal(t, [][]string{{"row 3"}, {"row 4"}}, visible)
}

func TestRenderResultsFitsAvailableHeight(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.result = Result{
		Header:  []string{"s"},
		Rows:    [][]string{{"row 0"}, {"row 1"}, {"row 2"}, {"row 3"}},
		Message: "4 rows",
	}
	model.focus = focusResults
	model.selectedRow = 3

	rendered := model.renderResults(80, 11)

	require.LessOrEqual(t, lipgloss.Height(rendered), 11)
	require.Contains(t, rendered, "row 3")
	require.NotContains(t, rendered, "row 0")
}

func TestShellModelFocusShortcutReturnsToEditor(t *testing.T) {
	model := newShellModel(context.Background(), &fakeRunner{})
	model.result = Result{
		Header: []string{"s"},
		Rows:   [][]string{{"a"}},
	}
	model.focus = focusResults

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyLeftShift})
	model = updated.(shellModel)

	require.Equal(t, focusEditor, model.focus)
}

func TestShellModelRunsQueryWithCtrlR(t *testing.T) {
	runner := &fakeRunner{
		result: Result{
			SQL:     "SELECT t0.subject AS s FROM triples AS t0",
			Header:  []string{"s"},
			Rows:    [][]string{{"https://example.org/alice"}},
			Message: "1 rows",
		},
	}
	model := newShellModel(context.Background(), runner)
	model.historyDir = t.TempDir()
	model.query = `PREFIX schema: <https://schema.org/>
SELECT ?s WHERE { ?s schema:name "bob" . }`

	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	model = updated.(shellModel)
	require.True(t, model.running)
	require.NotNil(t, cmd)

	for _, batchCmd := range cmd().(tea.BatchMsg) {
		if msg, ok := batchCmd().(queryResultMsg); ok {
			updated, _ = model.Update(msg)
			model = updated.(shellModel)
		}
	}

	require.False(t, model.running)
	require.Empty(t, model.err)
	require.Equal(t, model.query, runner.query)
	require.Contains(t, model.View().Content, "https://example.org/alice")
}
