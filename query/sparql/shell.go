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
	historyVisible     bool
	selectedRow        int
	resultOffset       int
	resultsAllSelected bool
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
	case tea.PasteMsg:
		m.insertEditorText(msg.Content)
		return m, nil
	case tea.ClipboardMsg:
		m.insertEditorText(msg.Content)
		return m, nil
	case tea.KeyPressMsg:
		if msg.Keystroke() == "tab" {
			m.toggleFocus()
			return m, nil
		}
		if msg.Keystroke() == "ctrl+l" {
			return m, func() tea.Msg {
				return tea.ClearScreen()
			}
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
		}
		if isSelectAllKey(msg) {
			if m.focus == focusResults && len(m.result.Rows) > 0 {
				m.resultsAllSelected = true
				return m, nil
			}
			m.selectAll()
			return m, nil
		}
		if isCopyKey(msg) {
			if m.focus == focusResults {
				if text, ok := m.selectedResultsCSV(); ok {
					return m, m.copyToClipboard(text, focusResults)
				}
				return m, nil
			}
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
		case "esc":
			m.query = ""
			m.cursor = 0
			m.clearSelection()
			m.err = ""
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
			nextCursor := moveCursorVertically(m.query, m.cursor, -1)
			if nextCursor == m.cursor && len(m.history) > 0 {
				m.historyVisible = true
				m.focus = focusHistory
				m.historyIndex = -1
				return m, nil
			}
			m.cursor = nextCursor
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

	header := shellTitleStyle.Width(contentWidth).Render("SAL SPARQL")
	help := renderHelp()
	editorWidth, sqlWidth := splitTopPanelWidths(contentWidth)
	editorColumn := m.renderEditorColumn(editorWidth)
	top := editorColumn
	if sqlWidth > 0 {
		top = lipgloss.JoinHorizontal(lipgloss.Top, editorColumn, " ", m.renderSQL(sqlWidth))
	}
	resultsHeight := m.resultsHeight(header, help, top)
	results := m.renderResults(contentWidth, resultsHeight)

	body := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		help,
		top,
		results,
	)
	view := tea.NewView(shellAppStyle.Width(width).Render(body))
	view.AltScreen = true
	return view
}

func (m shellModel) renderEditorColumn(width int) string {
	if !m.historyVisible && m.focus != focusHistory {
		return m.renderEditor(width)
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.renderHistory(width), m.renderEditor(width))
}

func (m shellModel) resultsHeight(header string, help string, top string) int {
	bodyHeight := max(1, m.height-2)
	used := lipgloss.Height(header) + lipgloss.Height(help) + lipgloss.Height(top)
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
		titleStyle = focusedSectionTitleStyle
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
		body.WriteString("\n\n")
		visibleRows := maxVisibleResultRows(availableHeight)
		offset, rows := resultWindow(m.result.Rows, m.selectedRow, m.resultOffset, visibleRows)
		body.WriteString(renderTable(m.result.Header, rows, width-4, m.focus == focusResults, m.selectedRow-offset, m.resultsAllSelected))
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

func (m *shellModel) toggleFocus() {
	switch m.focus {
	case focusEditor:
		if m.historyVisible && len(m.history) > 0 {
			m.focus = focusHistory
			return
		}
		if len(m.result.Rows) > 0 {
			m.focus = focusResults
			m.selectedRow = min(max(0, m.selectedRow), len(m.result.Rows)-1)
			m.ensureSelectedRowVisible()
			return
		}
	case focusHistory:
		if len(m.result.Rows) > 0 {
			m.focus = focusResults
			m.selectedRow = min(max(0, m.selectedRow), len(m.result.Rows)-1)
			m.ensureSelectedRowVisible()
			return
		}
		m.focus = focusEditor
		m.historyVisible = false
		return
	case focusResults:
		m.focus = focusEditor
		m.historyVisible = false
		return
	}
	m.focus = focusEditor
	m.historyVisible = false
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
	cols := len(header)
	widths := make([]int, cols)
	for i, cell := range header {
		widths[i] = len(cell)
	}
	for _, row := range rows {
		for i := 0; i < cols && i < len(row); i++ {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
	}
	maxCol := max(8, (width-(3*(cols-1)))/cols)
	for i := range widths {
		widths[i] = min(widths[i], maxCol)
	}

	var b strings.Builder
	writeStyledTableRow(&b, header, widths, tableHeaderStyle)
	for i, w := range widths {
		if i > 0 {
			b.WriteString(tableDividerStyle.Render("-+-"))
		} else {
			b.WriteString(tableDividerStyle.Render(""))
		}
		b.WriteString(tableDividerStyle.Render(strings.Repeat("-", w)))
	}
	b.WriteByte('\n')
	for i, row := range rows {
		style := tableCellStyle
		if focused && (allRowsSelected || i == focusedRow) {
			style = tableFocusedRowStyle
		}
		writeStyledTableRow(&b, row, widths, style)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeStyledTableRow(b *strings.Builder, row []string, widths []int, style lipgloss.Style) {
	for i, width := range widths {
		if i > 0 {
			b.WriteString(tableDividerStyle.Render(" | "))
		}
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		b.WriteString(style.Render(padCell(cell, width)))
	}
	b.WriteByte('\n')
}

func padCell(value string, width int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > width {
		if width <= 3 {
			return value[:width]
		}
		value = value[:width-3] + "..."
	}
	return value + strings.Repeat(" ", width-len(value))
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

func renderHelp() string {
	items := []string{
		helpItem("Ctrl+R", "run"),
		helpItem("Tab", "change focus"),
		helpItem("Home/End", "row bounds"),
		helpItem("Ctrl+U", "clear row"),
		helpItem("Ctrl+A", "select all"),
		helpItem("Ctrl+C", "copy"),
		helpItem("Ctrl+V", "paste"),
		helpItem("Ctrl+D", "quit"),
	}
	return shellHelpStyle.Render(strings.Join(items, "  "))
}

func helpItem(key string, description string) string {
	return shellHelpKeyStyle.Render(key) + " " + shellHelpDescriptionStyle.Render(description)
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

var (
	shellAppStyle = lipgloss.NewStyle().
			Padding(1, 2)
	shellTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E8EEF2")).
			Background(lipgloss.Color("#2D5A4E")).
			Padding(0, 1)
	shellHelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6E7C7C")).
			MarginTop(1).
			MarginBottom(1)
	shellHelpKeyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#20302C")).
				Bold(true)
	shellHelpDescriptionStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#6E7C7C")).
					Italic(true)
	historyTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#8C9696"))
	sectionTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#20302C"))
	focusedSectionTitleStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("#1F6F52"))
	editorPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#5A8F7B")).
				Padding(1, 2).
				MarginBottom(1)
	focusedEditorPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#1F6F52")).
				Padding(1, 2).
				MarginBottom(1)
	resultPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#6C7A99")).
				Padding(1, 2)
	focusedResultPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#1F6F52")).
				Padding(1, 2)
	sqlPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#6C7A99")).
			Padding(1, 2).
			MarginBottom(1)
	shellMutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7A8282"))
	editorFlashStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#1F6F52")).
				Italic(true)
	shellErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#B42318")).
			Bold(true)
	shellSuccessStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#1F7A4D")).
				Bold(true)
	shellRunningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B5A00")).
				Bold(true)
	sqlPreviewStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#48545C")).
			Padding(0, 1)
	editorCursorStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#5A8F7B")).
				Foreground(lipgloss.Color("#E8EEF2"))
	editorSelectionStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#D7E8E2")).
				Foreground(lipgloss.Color("#10231D"))

	sparqlKeywordStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#005F87")).
				Bold(true)
	sparqlVariableStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8A4A00"))
	sparqlIRIStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#1F6F52"))
	sparqlStringStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8F2F4A"))
	sparqlCommentStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7A8282")).
				Italic(true)
	sparqlPrefixedNameStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#5D4B8C"))

	tableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#20302C"))
	tableCellStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#263238"))
	tableFocusedRowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#10231D")).
				Background(lipgloss.Color("#CDE8DD")).
				Bold(true)
	tableDividerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9BA7A7"))
)
