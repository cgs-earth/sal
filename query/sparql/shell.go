package sparql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

type Result struct {
	SQL     string
	Header  []string
	Rows    [][]string
	Message string
}

type Runner interface {
	Run(ctx context.Context, query string) (Result, error)
}

type DuckDBRunner struct {
	TablePath string
	Layout    ObjectLayout
	Limit     int
}

// RunShell opens an interactive SPARQL prompt against the Iceberg triples table.
func RunShell(ctx context.Context, tablePath string, layout ObjectLayout) error {
	runner := DuckDBRunner{
		TablePath: tablePath,
		Layout:    layout,
		Limit:     100,
	}
	_, err := tea.NewProgram(newShellModel(ctx, runner)).Run()
	return err
}

// Run translates SPARQL to SQL and executes it through DuckDB.
func (r DuckDBRunner) Run(ctx context.Context, query string) (Result, error) {
	sql, err := ToSQL(query, r.Layout)
	if err != nil {
		return Result{}, err
	}
	sql = strings.TrimRight(strings.TrimSpace(sql), ";")
	if r.Limit > 0 && !hasLimit(sql) {
		sql += fmt.Sprintf("\nLIMIT %d", r.Limit)
	}

	tablePath := strings.ReplaceAll(r.TablePath, "'", "''")
	statement := fmt.Sprintf(`
INSTALL iceberg;
LOAD iceberg;
INSTALL spatial;
LOAD spatial;
CREATE OR REPLACE VIEW triples AS
SELECT *
FROM iceberg_scan('%s', allow_moved_paths = true);
COPY (%s) TO STDOUT (HEADER, DELIMITER ',');
`, tablePath, sql)

	cmd := exec.CommandContext(ctx, "duckdb", "-csv", "-c", statement)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return Result{}, fmt.Errorf("duckdb query failed: %s", strings.TrimSpace(stderr.String()))
		}
		return Result{}, fmt.Errorf("duckdb query failed: %w", err)
	}

	header, rows, err := parseCSVResult(stdout.String())
	if err != nil {
		return Result{}, err
	}
	return Result{
		SQL:     sql,
		Header:  header,
		Rows:    rows,
		Message: fmt.Sprintf("%d rows", len(rows)),
	}, nil
}

func parseCSVResult(output string) ([]string, [][]string, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, nil, nil
	}
	records, err := csv.NewReader(strings.NewReader(output)).ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("parse duckdb CSV output: %w", err)
	}
	if len(records) == 0 {
		return nil, nil, nil
	}
	return records[0], records[1:], nil
}

func hasLimit(sql string) bool {
	return strings.Contains(strings.ToUpper(sql), "\nLIMIT ") || strings.Contains(strings.ToUpper(sql), " LIMIT ")
}

type queryResultMsg struct {
	result Result
	err    error
}

type historySavedMsg struct {
	entries []historyEntry
	err     error
}

type clearCopyFlashMsg struct {
	id int
}

type shellFocus int

const (
	focusHistory shellFocus = iota
	focusEditor
	focusResults
)

type historyEntry struct {
	Query string
	Path  string
}

type shellModel struct {
	ctx                context.Context
	runner             Runner
	query              string
	cursor             int
	selectionStart     int
	selectionEnd       int
	result             Result
	err                string
	copyFlash          string
	copyFlashID        int
	copyFlashFocus     shellFocus
	running            bool
	width              int
	height             int
	submitted          string
	focus              shellFocus
	historyDir         string
	history            []historyEntry
	historyIndex       int
	selectedRow        int
	resultOffset       int
	resultsAllSelected bool
	mouseSelecting     bool
	mouseSelectAnchor  int
	showHelp           bool
}

func newShellModel(ctx context.Context, runner Runner) shellModel {
	defaultQuery := `PREFIX schema: <https://schema.org/>

SELECT ?s ?p ?o
WHERE {
  ?s ?p ?o .
}`
	historyDir := shellHistoryDir()
	history, _ := loadQueryHistory(historyDir)
	return shellModel{
		ctx:          ctx,
		runner:       runner,
		query:        defaultQuery,
		cursor:       len([]rune(defaultQuery)),
		width:        100,
		height:       30,
		focus:        focusEditor,
		historyDir:   historyDir,
		history:      history,
		historyIndex: -1,
	}
}

var shellHistoryDir = defaultHistoryDir

func (m shellModel) Init() tea.Cmd {
	return nil
}

func (m shellModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			if offset, ok := m.editorOffsetAtMouse(msg.X, msg.Y); ok {
				m.focus = focusEditor
				m.cursor = offset
				m.clearSelection()
				m.mouseSelecting = true
				m.mouseSelectAnchor = offset
			}
		}
		return m, nil
	case tea.MouseMotionMsg:
		if m.mouseSelecting {
			if offset, ok := m.editorOffsetAtMouse(msg.X, msg.Y); ok {
				m.cursor = offset
				m.selectionStart = m.mouseSelectAnchor
				m.selectionEnd = offset
			}
		}
		return m, nil
	case tea.MouseReleaseMsg:
		if m.mouseSelecting {
			if offset, ok := m.editorOffsetAtMouse(msg.X, msg.Y); ok {
				m.cursor = offset
				m.selectionStart = m.mouseSelectAnchor
				m.selectionEnd = offset
			}
			m.mouseSelecting = false
		}
		return m, nil
	case tea.PasteMsg:
		if m.focus == focusEditor {
			m.insertEditorText(msg.Content)
		}
		return m, nil
	case tea.ClipboardMsg:
		if m.focus == focusEditor {
			m.insertEditorText(msg.Content)
		}
		return m, nil
	case tea.KeyPressMsg:
		if msg.Keystroke() == "ctrl+h" {
			m.showHelp = !m.showHelp
			return m, nil
		}
		if isFocusPreviousKey(msg) {
			m.shiftFocus(-1)
			return m, nil
		}
		if isFocusNextKey(msg) {
			m.shiftFocus(1)
			return m, nil
		}
		if msg.Keystroke() == "ctrl+l" {
			return m, func() tea.Msg {
				return tea.ClearScreen()
			}
		}
		switch msg.Keystroke() {
		case "ctrl+d":
			return m, tea.Quit
		case "ctrl+r":
			if m.running {
				return m, nil
			}
			m.submitted = strings.TrimSpace(m.query)
			if m.submitted == "" {
				m.err = "enter a SPARQL SELECT query"
				return m, nil
			}
			m.err = ""
			m.running = true
			return m, tea.Batch(
				runQueryCmd(m.ctx, m.runner, m.submitted),
				saveHistoryCmd(m.historyDir, m.submitted),
			)
		}
		if m.focus == focusHistory {
			switch msg.Keystroke() {
			case "left":
				m.loadHistoryOffset(1)
				return m, nil
			case "right":
				m.loadHistoryOffset(-1)
				return m, nil
			}
			return m, nil
		}
		if m.focus == focusResults {
			switch msg.Keystroke() {
			case "up":
				m.selectedRow = max(0, m.selectedRow-1)
				m.resultsAllSelected = false
				m.ensureSelectedRowVisible()
				return m, nil
			case "down":
				m.selectedRow = min(max(0, len(m.result.Rows)-1), m.selectedRow+1)
				m.resultsAllSelected = false
				m.ensureSelectedRowVisible()
				return m, nil
			case "pgup":
				m.pageResults(-1)
				return m, nil
			case "pgdown":
				m.pageResults(1)
				return m, nil
			}
			switch msg.Key().Code {
			case tea.KeyPgUp:
				m.pageResults(-1)
				return m, nil
			case tea.KeyPgDown:
				m.pageResults(1)
				return m, nil
			}
			if isSelectAllKey(msg) {
				if len(m.result.Rows) > 0 {
					m.resultsAllSelected = true
				}
				return m, nil
			}
			if isCopyKey(msg) {
				if text, ok := m.selectedResultsCSV(); ok {
					return m, m.copyToClipboard(text, focusResults)
				}
				return m, nil
			}
			return m, nil
		}
		if msg.Keystroke() == "tab" {
			m.insertEditorText("\t")
			return m, nil
		}
		if isSelectAllKey(msg) {
			if len(m.result.Rows) > 0 && m.focus == focusResults {
				m.resultsAllSelected = true
				return m, nil
			}
			m.selectAll()
			return m, nil
		}
		if isCopyKey(msg) {
			if text, ok := m.selectedEditorText(); ok {
				return m, m.copyToClipboard(text, focusEditor)
			}
			return m, nil
		}
		if isPasteKey(msg) {
			return m, func() tea.Msg {
				return tea.ReadClipboard()
			}
		}
		if isLineStartKey(msg) {
			m.cursor = moveCursorLineStart(m.query, m.cursor)
			m.clearSelection()
			return m, nil
		}
		if isLineEndKey(msg) {
			m.cursor = moveCursorLineEnd(m.query, m.cursor)
			m.clearSelection()
			return m, nil
		}
		if isLineDeleteKey(msg) {
			m.query, m.cursor = deleteCurrentLine(m.query, m.cursor)
			m.clearSelection()
			return m, nil
		}
		if isWordLeftKey(msg) {
			m.cursor = moveCursorWordLeft(m.query, m.cursor)
			m.clearSelection()
			return m, nil
		}
		if isWordRightKey(msg) {
			m.cursor = moveCursorWordRight(m.query, m.cursor)
			m.clearSelection()
			return m, nil
		}
		if isWordDeleteKey(msg) {
			if !m.deleteSelection() {
				m.query, m.cursor = deleteWordBeforeCursor(m.query, m.cursor)
			}
			return m, nil
		}
		switch msg.Keystroke() {
		case "esc":
			return m, nil
		case "backspace":
			if !m.deleteSelection() {
				m.query, m.cursor = deleteBeforeCursor(m.query, m.cursor)
			}
			return m, nil
		case "enter":
			m.insertEditorText("\n")
			return m, nil
		case "left":
			m.cursor = max(0, m.cursor-1)
			m.clearSelection()
			return m, nil
		case "right":
			m.cursor = min(len([]rune(m.query)), m.cursor+1)
			m.clearSelection()
			return m, nil
		case "up":
			m.cursor = moveCursorVertically(m.query, m.cursor, -1)
			m.clearSelection()
			return m, nil
		case "down":
			m.cursor = moveCursorVertically(m.query, m.cursor, 1)
			m.clearSelection()
			return m, nil
		default:
			if msg.Key().Text != "" {
				m.insertEditorText(msg.Key().Text)
			}
			return m, nil
		}
	case queryResultMsg:
		m.running = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.result = msg.result
		m.selectedRow = 0
		m.resultOffset = 0
		m.resultsAllSelected = false
		return m, nil
	case historySavedMsg:
		if msg.err == nil {
			m.history = msg.entries
			m.historyIndex = -1
		}
		return m, nil
	case clearCopyFlashMsg:
		if msg.id == m.copyFlashID {
			m.copyFlash = ""
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m shellModel) View() tea.View {
	width := max(60, m.width)
	contentWidth := max(40, width-4)

	header := shellTitleStyle.Width(contentWidth).Render("SAL SPARQL  Ctrl+H help")
	editorWidth, sqlWidth := splitTopPanelWidths(contentWidth)
	editorColumn := m.renderEditorColumn(editorWidth)
	top := editorColumn
	if sqlWidth > 0 {
		top = lipgloss.JoinHorizontal(lipgloss.Top, editorColumn, " ", m.renderSQL(sqlWidth))
	}
	resultsHeight := m.resultsHeight(header, top)
	results := m.renderResults(contentWidth, resultsHeight)

	body := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		top,
		results,
	)
	rendered := shellAppStyle.Width(width).Render(body)
	if m.showHelp {
		rendered = renderHelpLayer(rendered, width)
	}
	view := tea.NewView(rendered)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m shellModel) renderEditorColumn(width int) string {
	return lipgloss.JoinVertical(lipgloss.Left, m.renderHistory(width), m.renderEditor(width))
}

func (m shellModel) resultsHeight(header string, top string) int {
	bodyHeight := max(1, m.height-2)
	used := lipgloss.Height(header) + lipgloss.Height(top)
	return max(6, bodyHeight-used)
}

func (m shellModel) renderEditor(width int) string {
	status := ""
	if m.running {
		status = shellRunningStyle.Render("running")
	}
	body := renderEditorBody(m.query, m.cursor, m.selectionStart, m.selectionEnd)
	titleStyle := sectionTitleStyle
	panelStyle := editorPanelStyle
	if m.focus == focusEditor {
		titleStyle = focusedSectionTitleStyle
		panelStyle = focusedEditorPanelStyle
	}
	labelParts := []string{titleStyle.Render("Editor")}
	if status != "" {
		labelParts = append(labelParts, " ", status)
	}
	if m.copyFlash != "" && m.copyFlashFocus == focusEditor {
		labelParts = append(labelParts, " ", editorFlashStyle.Render(m.copyFlash))
	}
	label := lipgloss.JoinHorizontal(lipgloss.Top, labelParts...)
	panel := panelStyle.Width(width).Render(body)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		label,
		panel,
	)
}

func (m shellModel) renderHistory(width int) string {
	titleStyle := historyTitleStyle
	if m.focus == focusHistory {
		titleStyle = focusedHistoryTitleStyle
	}
	status := shellMutedStyle.Render("empty")
	if len(m.history) > 0 {
		if m.historyIndex < 0 {
			status = shellMutedStyle.Render(fmt.Sprintf("0/%d", len(m.history)))
		} else {
			index := min(max(0, m.historyIndex), len(m.history)-1)
			status = shellMutedStyle.Render(fmt.Sprintf("%d/%d", index+1, len(m.history)))
		}
	}
	content := lipgloss.JoinHorizontal(lipgloss.Top, shellMutedStyle.Render("←"), " ", titleStyle.Render("History"), " ", status, " ", shellMutedStyle.Render("→"))
	return lipgloss.NewStyle().Width(width).Align(lipgloss.Right).Render(content)
}

func (m shellModel) renderSQL(width int) string {
	body := shellMutedStyle.Render("Run a query to show generated SQL.")
	if strings.TrimSpace(m.result.SQL) != "" {
		body = sqlPreviewStyle.Render(m.result.SQL)
	}
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		sectionTitleStyle.Render("SQL"),
		body,
	)
	return sqlPanelStyle.Width(width).Render(content)
}

func (m shellModel) renderResults(width int, availableHeight int) string {
	var body strings.Builder
	if m.running {
		body.WriteString(shellMutedStyle.Render("Waiting on DuckDB..."))
	} else if m.err != "" {
		body.WriteString(shellErrorStyle.Render("Error: " + m.err))
	} else if len(m.result.Header) > 0 {
		body.WriteString(shellSuccessStyle.Render(m.result.Message))
		if m.focus == focusResults {
			body.WriteString(shellMutedStyle.Render(fmt.Sprintf("  row %d/%d", m.selectedRow+1, len(m.result.Rows))))
		}
		body.WriteString("\n")
		visibleRows := maxVisibleRenderedResultRows(availableHeight)
		offset, rows := resultWindow(m.result.Rows, m.selectedRow, m.resultOffset, visibleRows)
		body.WriteString(renderTable(m.result.Header, rows, width-6, m.focus == focusResults, m.selectedRow-offset, m.resultsAllSelected))
	} else {
		body.WriteString(shellMutedStyle.Render("Run a query to show results here."))
	}
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderResultsTitle(),
		body.String(),
	)
	return m.resultsPanel().Width(width).Render(content)
}

func (m *shellModel) shiftFocus(delta int) {
	order := []shellFocus{focusHistory, focusEditor}
	if len(m.result.Rows) > 0 {
		order = append(order, focusResults)
	}

	current := 1
	for i, focus := range order {
		if focus == m.focus {
			current = i
			break
		}
	}
	next := (current + delta) % len(order)
	if next < 0 {
		next += len(order)
	}
	m.focus = order[next]
	if m.focus == focusResults {
		m.selectedRow = min(max(0, m.selectedRow), len(m.result.Rows)-1)
		m.ensureSelectedRowVisible()
	}
}

func (m *shellModel) loadHistoryOffset(delta int) {
	if len(m.history) == 0 {
		return
	}
	index := 0
	if m.historyIndex >= 0 {
		index = m.historyIndex + delta
	}
	m.historyIndex = min(max(0, index), len(m.history)-1)
	m.query = m.history[m.historyIndex].Query
	m.cursor = len([]rune(m.query))
	m.clearSelection()
	m.err = ""
}

func (m *shellModel) copyToClipboard(text string, focus shellFocus) tea.Cmd {
	m.copyFlashID++
	m.copyFlash = "Copied to clipboard"
	m.copyFlashFocus = focus
	return tea.Batch(tea.SetClipboard(text), clearCopyFlashCmd(m.copyFlashID))
}

func (m *shellModel) insertEditorText(text string) {
	if !m.replaceSelection(text) {
		m.query, m.cursor = insertAtCursor(m.query, m.cursor, text)
	}
	m.clearSelection()
}

func (m *shellModel) selectAll() {
	m.selectionStart = 0
	m.selectionEnd = len([]rune(m.query))
	m.cursor = m.selectionEnd
}

func (m *shellModel) clearSelection() {
	m.selectionStart = m.cursor
	m.selectionEnd = m.cursor
}

// editorOffsetAtMouse maps terminal mouse coordinates into a query rune offset.
func (m shellModel) editorOffsetAtMouse(x int, y int) (int, bool) {
	originX, originY, bodyWidth, bodyHeight := m.editorBodyBounds()
	line, column := y-originY, x-originX
	if line < 0 || line >= bodyHeight || column < 0 || column >= bodyWidth {
		return 0, false
	}
	return queryOffsetAtLineColumn(m.query, line, column), true
}

// editorBodyBounds returns the terminal cell bounds for the visible editor text.
func (m shellModel) editorBodyBounds() (int, int, int, int) {
	width := max(60, m.width)
	contentWidth := max(40, width-4)
	editorWidth, _ := splitTopPanelWidths(contentWidth)

	x := 2 + 1 + 2
	y := 1 + 1 + 1 + 1
	y += lipgloss.Height(m.renderHistory(editorWidth))
	bodyWidth := max(1, editorWidth-2-4)
	bodyHeight := max(1, lipgloss.Height(renderEditorBody(m.query, m.cursor, m.selectionStart, m.selectionEnd)))
	return x, y, bodyWidth, bodyHeight
}

// queryOffsetAtLineColumn returns the nearest query rune offset for a text cell.
func queryOffsetAtLineColumn(query string, line int, column int) int {
	if query == "" {
		return 0
	}
	runes := []rune(query)
	currentLine := 0
	currentColumn := 0
	for i, r := range runes {
		if currentLine == line && currentColumn >= column {
			return i
		}
		if r == '\n' {
			if currentLine == line {
				return i
			}
			currentLine++
			currentColumn = 0
			continue
		}
		currentColumn++
	}
	return len(runes)
}

func (m shellModel) selectedEditorText() (string, bool) {
	start, end, ok := selectionBounds(m.query, m.selectionStart, m.selectionEnd)
	if !ok {
		return "", false
	}
	return string([]rune(m.query)[start:end]), true
}

func (m shellModel) selectedResultsCSV() (string, bool) {
	if len(m.result.Rows) == 0 {
		return "", false
	}
	records := m.result.Rows
	if !m.resultsAllSelected {
		row := min(max(0, m.selectedRow), len(m.result.Rows)-1)
		records = [][]string{m.result.Rows[row]}
		return recordsToCSV(records), true
	}
	allRecords := make([][]string, 0, len(m.result.Rows)+1)
	if len(m.result.Header) > 0 {
		allRecords = append(allRecords, m.result.Header)
	}
	allRecords = append(allRecords, records...)
	return recordsToCSV(allRecords), true
}

func (m *shellModel) deleteSelection() bool {
	return m.replaceSelection("")
}

func (m *shellModel) replaceSelection(replacement string) bool {
	start, end, ok := selectionBounds(m.query, m.selectionStart, m.selectionEnd)
	if !ok {
		return false
	}
	runes := []rune(m.query)
	replacementRunes := []rune(replacement)
	next := make([]rune, 0, len(runes)-(end-start)+len(replacementRunes))
	next = append(next, runes[:start]...)
	next = append(next, replacementRunes...)
	next = append(next, runes[end:]...)
	m.query = string(next)
	m.cursor = start + len(replacementRunes)
	m.clearSelection()
	return true
}

func (m *shellModel) ensureSelectedRowVisible() {
	visibleRows := maxVisibleResultRows(m.height)
	if m.selectedRow < m.resultOffset {
		m.resultOffset = m.selectedRow
	}
	if m.selectedRow >= m.resultOffset+visibleRows {
		m.resultOffset = m.selectedRow - visibleRows + 1
	}
	m.resultOffset = min(max(0, m.resultOffset), max(0, len(m.result.Rows)-visibleRows))
}

func (m *shellModel) pageResults(direction int) {
	if len(m.result.Rows) == 0 {
		return
	}
	step := max(1, maxVisibleResultRows(m.height)-1)
	m.selectedRow = min(max(0, m.selectedRow+(direction*step)), len(m.result.Rows)-1)
	m.resultsAllSelected = false
	m.ensureSelectedRowVisible()
}

func (m shellModel) visibleResultRows() [][]string {
	_, rows := resultWindow(m.result.Rows, m.selectedRow, m.resultOffset, maxVisibleResultRows(m.height))
	return rows
}

func maxVisibleResultRows(availableHeight int) int {
	return max(1, availableHeight-9)
}

func maxVisibleRenderedResultRows(availableHeight int) int {
	return max(1, availableHeight-10)
}

func resultWindow(rows [][]string, selectedRow int, offset int, visibleRows int) (int, [][]string) {
	if len(rows) == 0 {
		return 0, nil
	}
	visibleRows = max(1, visibleRows)
	selectedRow = min(max(0, selectedRow), len(rows)-1)
	offset = min(max(0, offset), max(0, len(rows)-visibleRows))
	if selectedRow < offset {
		offset = selectedRow
	}
	if selectedRow >= offset+visibleRows {
		offset = selectedRow - visibleRows + 1
	}
	offset = min(max(0, offset), max(0, len(rows)-visibleRows))
	end := min(len(rows), offset+visibleRows)
	return offset, rows[offset:end]
}

func (m shellModel) resultsTitle() lipgloss.Style {
	if m.focus == focusResults {
		return focusedSectionTitleStyle
	}
	return sectionTitleStyle
}

func (m shellModel) renderResultsTitle() string {
	parts := []string{m.resultsTitle().Render("Results")}
	if m.copyFlash != "" && m.copyFlashFocus == focusResults {
		parts = append(parts, " ", editorFlashStyle.Render(m.copyFlash))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func (m shellModel) resultsPanel() lipgloss.Style {
	if m.focus == focusResults {
		return focusedResultPanelStyle
	}
	return resultPanelStyle
}

func runQueryCmd(ctx context.Context, runner Runner, query string) tea.Cmd {
	return func() tea.Msg {
		result, err := runner.Run(ctx, query)
		return queryResultMsg{result: result, err: err}
	}
}

func saveHistoryCmd(dir string, query string) tea.Cmd {
	return func() tea.Msg {
		entries, err := saveQueryHistory(dir, query)
		return historySavedMsg{entries: entries, err: err}
	}
}

func clearCopyFlashCmd(id int) tea.Cmd {
	return tea.Tick(900*time.Millisecond, func(time.Time) tea.Msg {
		return clearCopyFlashMsg{id: id}
	})
}

func defaultHistoryDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".sal", "sparql_history")
}

// saveQueryHistory writes a query into the history directory and keeps only the most recent entries.
func saveQueryHistory(dir string, query string) ([]historyEntry, error) {
	query = strings.TrimSpace(query)
	if dir == "" || query == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(query))
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".sparql")
	if err := os.WriteFile(path, []byte(query), 0o644); err != nil {
		return nil, err
	}
	if err := pruneQueryHistory(dir, 100); err != nil {
		return nil, err
	}
	return loadQueryHistory(dir)
}

// loadQueryHistory returns saved queries sorted by most recent file write first.
func loadQueryHistory(dir string) ([]historyEntry, error) {
	if dir == "" {
		return nil, nil
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type diskEntry struct {
		historyEntry
		modTime int64
	}
	entries := make([]diskEntry, 0, len(files))
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		info, err := file.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, file.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		query := strings.TrimSpace(string(content))
		if query == "" {
			continue
		}
		entries = append(entries, diskEntry{
			historyEntry: historyEntry{Query: query, Path: path},
			modTime:      info.ModTime().UnixNano(),
		})
	}
	sort.Slice(entries, func(i int, j int) bool {
		if entries[i].modTime == entries[j].modTime {
			return entries[i].Path > entries[j].Path
		}
		return entries[i].modTime > entries[j].modTime
	})
	history := make([]historyEntry, len(entries))
	for i, entry := range entries {
		history[i] = entry.historyEntry
	}
	return history, nil
}

func pruneQueryHistory(dir string, limit int) error {
	entries, err := loadQueryHistory(dir)
	if err != nil {
		return err
	}
	if len(entries) <= limit {
		return nil
	}
	for _, entry := range entries[limit:] {
		if err := os.Remove(entry.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func splitTopPanelWidths(width int) (int, int) {
	if width < 80 {
		return width, 0
	}
	sqlWidth := max(28, width/3)
	editorWidth := max(40, width-sqlWidth-1)
	return editorWidth, sqlWidth
}

func recordsToCSV(records [][]string) string {
	var b strings.Builder
	writer := csv.NewWriter(&b)
	_ = writer.WriteAll(records)
	return b.String()
}

func renderTable(header []string, rows [][]string, width int, focused bool, focusedRow int, allRowsSelected bool) string {
	if len(header) == 0 {
		return ""
	}
	if width <= 0 {
		width = 100
	}
	trimmedHeader := trimTableRow(header, width)
	trimmedRows := make([][]string, len(rows))
	for i, row := range rows {
		trimmedRows[i] = trimTableRow(row, width)
	}
	return table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(tableBorderStyle).
		Width(width).
		Height(len(trimmedRows) + 4).
		Wrap(false).
		StyleFunc(func(row, _ int) lipgloss.Style {
			switch {
			case row == table.HeaderRow:
				return tableHeaderStyle
			case focused && (allRowsSelected || row == focusedRow):
				return tableFocusedRowStyle
			case row%2 == 0:
				return tableEvenRowStyle
			default:
				return tableOddRowStyle
			}
		}).
		Headers(trimmedHeader...).
		Rows(trimmedRows...).
		String()
}

func trimTableRow(row []string, width int) []string {
	trimmed := make([]string, len(row))
	maxCellWidth := max(8, width/max(1, len(row))-4)
	for i, cell := range row {
		trimmed[i] = truncateTableCell(cell, maxCellWidth)
	}
	return trimmed
}

func truncateTableCell(value string, width int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) <= width {
		return value
	}
	if width <= 3 {
		return value[:width]
	}
	return value[:width-3] + "..."
}

func renderHelpLayer(base string, width int) string {
	help := renderHelp(width)
	x := max(0, (lipgloss.Width(base)-lipgloss.Width(help))/2)
	y := max(1, min(5, lipgloss.Height(base)/3))
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(help).X(x).Y(y).Z(1),
	).Render()
}

func renderHelp(width int) string {
	helpWidth := min(max(36, width-12), 72)
	items := []string{
		helpItem("Ctrl+H", "toggle help"),
		helpItem("Ctrl+R", "run query"),
		helpItem("Shift+←/→", "change focus"),
		helpItem("Left/Right", "browse history when history is focused"),
		helpItem("Up/Down", "move cursor or selected result row"),
		helpItem("PgUp/PgDown", "page through results"),
		helpItem("Ctrl+U", "clear current editor line"),
		helpItem("Ctrl+A", "select editor text or result rows"),
		helpItem("Ctrl+C", "copy selection"),
		helpItem("Ctrl+V", "paste into editor"),
		helpItem("Ctrl+L", "clear screen"),
		helpItem("Ctrl+D", "quit"),
	}
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		shellHelpTitleStyle.Render("Help"),
		strings.Join(items, "\n"),
	)
	return shellHelpStyle.Width(helpWidth).Render(content)
}

func helpItem(key string, description string) string {
	return shellHelpRowStyle.Render(shellHelpKeyStyle.Render(key) + "  " + shellHelpDescriptionStyle.Render(description))
}

func renderEditorBody(query string, cursor int, selection ...int) string {
	renderedCursor := editorCursorStyle.Render(" ")
	if strings.TrimSpace(query) == "" {
		return shellMutedStyle.Render("Enter a SPARQL SELECT query...") + renderedCursor
	}
	if len(selection) >= 2 {
		if start, end, ok := selectionBounds(query, selection[0], selection[1]); ok {
			runes := []rune(query)
			return syntaxHighlight(string(runes[:start])) +
				editorSelectionStyle.Render(string(runes[start:end])) +
				syntaxHighlight(string(runes[end:]))
		}
	}
	before, at, after := splitAtCursor(query, cursor)
	if at != "" {
		if at == "\n" {
			return syntaxHighlight(before) + renderedCursor + "\n" + syntaxHighlight(after)
		}
		renderedCursor = editorCursorStyle.Render(at)
	}
	return syntaxHighlight(before) + renderedCursor + syntaxHighlight(after)
}

func selectionBounds(value string, start int, end int) (int, int, bool) {
	length := len([]rune(value))
	start = clampCursor(start, length)
	end = clampCursor(end, length)
	if start == end {
		return 0, 0, false
	}
	if start > end {
		start, end = end, start
	}
	return start, end, true
}

func insertAtCursor(value string, cursor int, inserted string) (string, int) {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	insertedRunes := []rune(inserted)
	next := make([]rune, 0, len(runes)+len(insertedRunes))
	next = append(next, runes[:cursor]...)
	next = append(next, insertedRunes...)
	next = append(next, runes[cursor:]...)
	return string(next), cursor + len(insertedRunes)
}

func deleteBeforeCursor(value string, cursor int) (string, int) {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	if cursor == 0 {
		return value, cursor
	}
	next := make([]rune, 0, len(runes)-1)
	next = append(next, runes[:cursor-1]...)
	next = append(next, runes[cursor:]...)
	return string(next), cursor - 1
}

func deleteWordBeforeCursor(value string, cursor int) (string, int) {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	if cursor == 0 {
		return value, cursor
	}
	start := cursor
	for start > 0 && unicode.IsSpace(runes[start-1]) {
		start--
	}
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	next := make([]rune, 0, len(runes)-(cursor-start))
	next = append(next, runes[:start]...)
	next = append(next, runes[cursor:]...)
	return string(next), start
}

func moveCursorLineStart(value string, cursor int) int {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	start, _ := currentLineBounds(runes, cursor)
	return start
}

func moveCursorLineEnd(value string, cursor int) int {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	_, end := currentLineBounds(runes, cursor)
	return end
}

func deleteCurrentLine(value string, cursor int) (string, int) {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	start, end := currentLineBounds(runes, cursor)
	if end < len(runes) {
		end++
	} else if start > 0 {
		start--
	}
	next := make([]rune, 0, len(runes)-(end-start))
	next = append(next, runes[:start]...)
	next = append(next, runes[end:]...)
	return string(next), start
}

func moveCursorWordLeft(value string, cursor int) int {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	for cursor > 0 && unicode.IsSpace(runes[cursor-1]) {
		cursor--
	}
	for cursor > 0 && !unicode.IsSpace(runes[cursor-1]) {
		cursor--
	}
	return cursor
}

func moveCursorWordRight(value string, cursor int) int {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	for cursor < len(runes) && unicode.IsSpace(runes[cursor]) {
		cursor++
	}
	for cursor < len(runes) && !unicode.IsSpace(runes[cursor]) {
		cursor++
	}
	return cursor
}

func isWordDeleteKey(msg tea.KeyPressMsg) bool {
	switch msg.Keystroke() {
	case "ctrl+backspace", "alt+backspace", "ctrl+w":
		return true
	}
	key := msg.Key()
	if key.Code != tea.KeyBackspace {
		return false
	}
	return key.Mod&tea.ModCtrl != 0 || key.Mod&tea.ModAlt != 0
}

func isLineStartKey(msg tea.KeyPressMsg) bool {
	key := msg.Key()
	return msg.Keystroke() == "home" || key.Code == tea.KeyHome
}

func isLineEndKey(msg tea.KeyPressMsg) bool {
	key := msg.Key()
	return msg.Keystroke() == "end" || key.Code == tea.KeyEnd
}

func isLineDeleteKey(msg tea.KeyPressMsg) bool {
	key := msg.Key()
	return msg.Keystroke() == "ctrl+u" || (unicode.ToLower(key.Code) == 'u' && key.Mod&tea.ModCtrl != 0)
}

func isWordLeftKey(msg tea.KeyPressMsg) bool {
	mod, name := wordJumpModifier()
	if msg.Keystroke() == name+"+left" || (runtime.GOOS == "darwin" && msg.Keystroke() == "alt+b") {
		return true
	}
	key := msg.Key()
	if runtime.GOOS == "darwin" && (key.Code == tea.KeyLeftAlt || key.Code == tea.KeyLeftMeta || (unicode.ToLower(key.Code) == 'b' && key.Mod&tea.ModAlt != 0)) {
		return true
	}
	if runtime.GOOS != "darwin" && key.Code == tea.KeyLeftCtrl {
		return true
	}
	return key.Code == tea.KeyLeft && key.Mod&mod != 0
}

func isWordRightKey(msg tea.KeyPressMsg) bool {
	mod, name := wordJumpModifier()
	if msg.Keystroke() == name+"+right" || (runtime.GOOS == "darwin" && msg.Keystroke() == "alt+f") {
		return true
	}
	key := msg.Key()
	if runtime.GOOS == "darwin" && (key.Code == tea.KeyRightAlt || key.Code == tea.KeyRightMeta || (unicode.ToLower(key.Code) == 'f' && key.Mod&tea.ModAlt != 0)) {
		return true
	}
	if runtime.GOOS != "darwin" && key.Code == tea.KeyRightCtrl {
		return true
	}
	return key.Code == tea.KeyRight && key.Mod&mod != 0
}

func wordJumpModifier() (tea.KeyMod, string) {
	if runtime.GOOS == "darwin" {
		return tea.ModAlt, "alt"
	}
	return tea.ModCtrl, "ctrl"
}

func isSelectAllKey(msg tea.KeyPressMsg) bool {
	return hasClipboardShortcut(msg, 'a')
}

func isCopyKey(msg tea.KeyPressMsg) bool {
	return hasClipboardShortcut(msg, 'c')
}

func isPasteKey(msg tea.KeyPressMsg) bool {
	return hasClipboardShortcut(msg, 'v')
}

func hasClipboardShortcut(msg tea.KeyPressMsg, key rune) bool {
	stroke := strings.ToLower(msg.Keystroke())
	if stroke == fmt.Sprintf("ctrl+%c", key) {
		return true
	}
	k := msg.Key()
	return unicode.ToLower(k.Code) == key && k.Mod&tea.ModCtrl != 0
}

func isFocusPreviousKey(msg tea.KeyPressMsg) bool {
	stroke := strings.ToLower(msg.Keystroke())
	if stroke == "tab+left" || stroke == "shift+left" {
		return true
	}
	key := msg.Key()
	return key.Code == tea.KeyLeftShift || (key.Code == tea.KeyLeft && key.Mod&tea.ModShift != 0)
}

func isFocusNextKey(msg tea.KeyPressMsg) bool {
	stroke := strings.ToLower(msg.Keystroke())
	if stroke == "tab+right" || stroke == "shift+right" {
		return true
	}
	key := msg.Key()
	return key.Code == tea.KeyRightShift || (key.Code == tea.KeyRight && key.Mod&tea.ModShift != 0)
}

func splitAtCursor(value string, cursor int) (string, string, string) {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	before := string(runes[:cursor])
	if cursor == len(runes) {
		return before, "", ""
	}
	return before, string(runes[cursor : cursor+1]), string(runes[cursor+1:])
}

func moveCursorVertically(value string, cursor int, delta int) int {
	runes := []rune(value)
	cursor = clampCursor(cursor, len(runes))
	lineStart, lineEnd := currentLineBounds(runes, cursor)
	column := cursor - lineStart
	if delta < 0 {
		if lineStart == 0 {
			return cursor
		}
		prevEnd := lineStart - 1
		prevStart := prevEnd
		for prevStart > 0 && runes[prevStart-1] != '\n' {
			prevStart--
		}
		return prevStart + min(column, prevEnd-prevStart)
	}
	if lineEnd == len(runes) {
		return cursor
	}
	nextStart := lineEnd + 1
	nextEnd := nextStart
	for nextEnd < len(runes) && runes[nextEnd] != '\n' {
		nextEnd++
	}
	return nextStart + min(column, nextEnd-nextStart)
}

func currentLineBounds(runes []rune, cursor int) (int, int) {
	start := cursor
	for start > 0 && runes[start-1] != '\n' {
		start--
	}
	end := cursor
	for end < len(runes) && runes[end] != '\n' {
		end++
	}
	return start, end
}

func clampCursor(cursor int, length int) int {
	return min(max(0, cursor), length)
}

func syntaxHighlight(query string) string {
	var b strings.Builder
	for i := 0; i < len(query); {
		ch := rune(query[i])
		if ch == '#' {
			end := i
			for end < len(query) && query[end] != '\n' {
				end++
			}
			b.WriteString(sparqlCommentStyle.Render(query[i:end]))
			i = end
			continue
		}
		if ch == '"' || ch == '\'' {
			end := scanQuoted(query, i)
			b.WriteString(sparqlStringStyle.Render(query[i:end]))
			i = end
			continue
		}
		if ch == '<' {
			end := i + 1
			for end < len(query) && query[end] != '>' {
				end++
			}
			if end < len(query) {
				end++
			}
			b.WriteString(sparqlIRIStyle.Render(query[i:end]))
			i = end
			continue
		}
		if ch == '?' || ch == '$' {
			end := i + 1
			for end < len(query) && isSPARQLNameRune(rune(query[end])) {
				end++
			}
			b.WriteString(sparqlVariableStyle.Render(query[i:end]))
			i = end
			continue
		}
		if isSPARQLNameStart(ch) {
			end := i + 1
			for end < len(query) && isSPARQLNameRune(rune(query[end])) {
				end++
			}
			token := query[i:end]
			if isSPARQLKeyword(token) {
				b.WriteString(sparqlKeywordStyle.Render(token))
			} else if strings.Contains(token, ":") {
				b.WriteString(sparqlPrefixedNameStyle.Render(token))
			} else {
				b.WriteString(token)
			}
			i = end
			continue
		}
		b.WriteByte(query[i])
		i++
	}
	return b.String()
}

func scanQuoted(value string, start int) int {
	quote := value[start]
	for i := start + 1; i < len(value); i++ {
		if value[i] == '\\' {
			i++
			continue
		}
		if value[i] == quote {
			return i + 1
		}
	}
	return len(value)
}

func isSPARQLNameStart(ch rune) bool {
	return unicode.IsLetter(ch) || ch == '_'
}

func isSPARQLNameRune(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '-' || ch == ':'
}

func isSPARQLKeyword(token string) bool {
	switch strings.ToUpper(token) {
	case "PREFIX", "BASE", "SELECT", "DISTINCT", "REDUCED", "WHERE", "FILTER", "OPTIONAL", "UNION", "LIMIT", "OFFSET", "ORDER", "BY", "ASC", "DESC", "GROUP", "HAVING", "BIND", "VALUES", "ASK", "CONSTRUCT", "FROM", "NAMED", "GRAPH", "TRUE", "FALSE", "A":
		return true
	default:
		return false
	}
}

const (
	catRosewater = "#F5E0DC"
	catPink      = "#F5C2E7"
	catMauve     = "#CBA6F7"
	catRed       = "#F38BA8"
	catPeach     = "#FAB387"
	catYellow    = "#F9E2AF"
	catGreen     = "#A6E3A1"
	catTeal      = "#94E2D5"
	catSky       = "#89DCEB"
	catBlue      = "#89B4FA"
	catLavender  = "#B4BEFE"
	catText      = "#CDD6F4"
	catSubtext1  = "#BAC2DE"
	catSubtext0  = "#A6ADC8"
	catOverlay2  = "#9399B2"
	catOverlay1  = "#7F849C"
	catSurface2  = "#585B70"
	catSurface1  = "#45475A"
	catSurface0  = "#313244"
	catBase      = "#1E1E2E"
	catMantle    = "#181825"
	catCrust     = "#11111B"
)

var (
	shellAppStyle = lipgloss.NewStyle().
			Padding(1, 2)
	shellTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(catCrust)).
			Background(lipgloss.Color(catLavender)).
			Padding(0, 1)
	shellHelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(catText)).
			Border(lipgloss.ThickBorder()).
			BorderForeground(lipgloss.Color(catBlue)).
			Padding(1, 2)
	shellHelpTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catBlue)).
				Background(lipgloss.Color(catBase)).
				Bold(true)
	shellHelpRowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catText)).
				Background(lipgloss.Color(catBase))
	shellHelpKeyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catRosewater)).
				Background(lipgloss.Color(catBase)).
				Bold(true)
	shellHelpDescriptionStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color(catText)).
					Background(lipgloss.Color(catBase)).
					Italic(true)
	historyTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(catSubtext0))
	sectionTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(catText))
	focusedSectionTitleStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color(catLavender))
	focusedHistoryTitleStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color(catCrust)).
					Background(lipgloss.Color(catLavender)).
					Padding(0, 1)
	editorPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(catSurface2)).
				Padding(1, 2).
				MarginBottom(1)
	focusedEditorPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(lipgloss.Color(catLavender)).
				Padding(1, 2).
				MarginBottom(1)
	resultPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(catSurface2)).
				Padding(1, 2)
	focusedResultPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(lipgloss.Color(catLavender)).
				Padding(1, 2)
	sqlPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(catSurface2)).
			Padding(1, 2).
			MarginBottom(1)
	shellMutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(catOverlay2))
	editorFlashStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catGreen)).
				Italic(true)
	shellErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(catRed)).
			Bold(true)
	shellSuccessStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catGreen)).
				Bold(true)
	shellRunningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catYellow)).
				Bold(true)
	sqlPreviewStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(catSubtext1)).
			Padding(0, 1)
	editorCursorStyle = lipgloss.NewStyle().
				Background(lipgloss.Color(catLavender)).
				Foreground(lipgloss.Color(catCrust))
	editorSelectionStyle = lipgloss.NewStyle().
				Background(lipgloss.Color(catSurface2)).
				Foreground(lipgloss.Color(catText))

	sparqlKeywordStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catBlue)).
				Bold(true)
	sparqlVariableStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catPeach))
	sparqlIRIStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(catTeal))
	sparqlStringStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catGreen))
	sparqlCommentStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catOverlay1)).
				Italic(true)
	sparqlPrefixedNameStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catMauve))

	tableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(catText)).
				Align(lipgloss.Center).
				Padding(0, 1)
	tableCellStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(catSubtext1)).
			Padding(0, 1)
	tableOddRowStyle = tableCellStyle.
				Foreground(lipgloss.Color(catSubtext1))
	tableEvenRowStyle = tableCellStyle.
				Foreground(lipgloss.Color(catOverlay2))
	tableFocusedRowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catCrust)).
				Background(lipgloss.Color(catLavender)).
				Bold(true).
				Padding(0, 1)
	tableBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(catSurface2))
)
