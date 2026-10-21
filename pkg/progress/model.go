package progress

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// pane is which of the two panes is on screen.
type pane int

const (
	paneProgress pane = iota
	paneLogs
)

// tickInterval is how often the panes redraw: the elapsed times tick, the
// spinner turns, and whatever the tracker and the log buffer gathered since
// the last tick is shown.
const tickInterval = 100 * time.Millisecond

type tickMsg time.Time

// doneMsg is sent when the work the panes were opened for has returned.
type doneMsg struct {
	err error
}

// promptMsg asks the user a yes/no question through the panes; the answer is
// sent on reply.
type promptMsg struct {
	question string
	reply    chan bool
}

// model is the Bubble Tea model of the two panes.
type model struct {
	ui     *UI
	pane   pane
	width  int
	height int
	now    time.Time
	frame  int
	// logs are the log lines seen so far, and wrapped the same lines wrapped
	// to wrapWidth, which is what the log pane scrolls through
	logs      []string
	wrapped   []string
	wrapWidth int
	// logOffset is the first wrapped line on screen; logFollow keeps the pane
	// at the bottom as lines arrive, until the user scrolls up
	logOffset      int
	logFollow      bool
	progressOffset int
	prompt         *promptMsg
	finished       bool
}

func newModel(ui *UI) model {
	return model{ui: ui, now: time.Now(), logFollow: true, width: 80, height: 24}
}

func (m model) Init() tea.Cmd {
	return tick()
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.now = time.Time(msg)
		m.frame = (m.frame + 1) % len(spinnerFrames)
		m.pullLogs()
		return m, tick()
	case doneMsg:
		m.finished = true
		m.now = time.Now()
		m.pullLogs()
		return m, tea.Quit
	case promptMsg:
		pending := msg
		m.prompt = &pending
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.rewrap()
		return m, nil
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scroll(-3)
		case tea.MouseWheelDown:
			m.scroll(3)
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.keyPress(msg)
	}
	return m, nil
}

// keyPress handles a key: ctrl+c interrupts the build, y or n answer a
// pending prompt, tab and the pane keys switch panes, and the rest scroll.
func (m model) keyPress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Interrupt
	}
	if m.prompt != nil {
		switch key {
		case "y", "Y":
			m.prompt.reply <- true
			m.prompt = nil
			return m, nil
		case "n", "N", "enter", "esc":
			m.prompt.reply <- false
			m.prompt = nil
			return m, nil
		}
	}
	switch key {
	case "tab", "shift+tab", "left", "right", "h":
		m.pane = 1 - m.pane
	case "1", "p":
		m.pane = paneProgress
	case "2", "l":
		m.pane = paneLogs
	case "up", "k":
		m.scroll(-1)
	case "down", "j":
		m.scroll(1)
	case "pgup", "b":
		m.scroll(-m.bodyHeight())
	case "pgdown", "space", "f":
		m.scroll(m.bodyHeight())
	case "home", "g":
		m.scrollTo(0)
	case "end", "G":
		m.scrollTo(1 << 30)
		if m.pane == paneLogs {
			m.logFollow = true
		}
	}
	return m, nil
}

// scroll moves the pane on screen by delta lines. Scrolling the log pane up
// stops it following new lines; scrolling to its bottom resumes.
func (m *model) scroll(delta int) {
	if m.pane == paneLogs {
		m.scrollTo(m.logOffset + delta)
		m.logFollow = m.logOffset >= m.maxLogOffset()
		return
	}
	m.scrollTo(m.progressOffset + delta)
}

func (m *model) scrollTo(offset int) {
	if m.pane == paneLogs {
		m.logOffset = max(0, min(offset, m.maxLogOffset()))
		m.logFollow = m.logOffset >= m.maxLogOffset()
		return
	}
	lines := len(m.ui.tracker.Render(m.width, m.now, spinnerFrames[m.frame]))
	m.progressOffset = max(0, min(offset, lines-m.bodyHeight()))
}

func (m *model) maxLogOffset() int {
	return max(0, len(m.wrapped)-m.bodyHeight())
}

// bodyHeight is how many lines a pane has between the title bar and the
// help line, less the prompt when one is showing.
func (m model) bodyHeight() int {
	height := m.height - 2
	if m.prompt != nil {
		height--
	}
	return max(1, height)
}

// pullLogs takes the lines written since the last tick into the pane.
func (m *model) pullLogs() {
	fresh := m.ui.logs.LinesFrom(len(m.logs))
	if len(fresh) == 0 {
		return
	}
	m.logs = append(m.logs, fresh...)
	for _, line := range fresh {
		m.wrapped = append(m.wrapped, wrapLine(line, m.wrapWidth)...)
	}
	if m.logFollow {
		m.logOffset = m.maxLogOffset()
	}
}

// rewrap wraps every log line again for the current width.
func (m *model) rewrap() {
	m.wrapWidth = max(10, m.width)
	m.wrapped = m.wrapped[:0]
	for _, line := range m.logs {
		m.wrapped = append(m.wrapped, wrapLine(line, m.wrapWidth)...)
	}
	if m.logFollow {
		m.logOffset = m.maxLogOffset()
	}
}

// wrapLine wraps one styled log line to width, breaking a long IRI or path
// rather than letting the terminal wrap it out of step with the pane.
func wrapLine(line string, width int) []string {
	if width <= 0 {
		return []string{line}
	}
	return strings.Split(ansi.Wrap(line, width, ""), "\n")
}

func (m model) View() tea.View {
	var body []string
	if m.pane == paneLogs {
		body = m.wrapped
		if len(body) > m.logOffset {
			body = body[m.logOffset:]
		} else {
			body = nil
		}
	} else {
		body = m.ui.tracker.Render(m.width, m.now, spinnerFrames[m.frame])
		if len(body) > m.progressOffset {
			body = body[m.progressOffset:]
		} else {
			body = nil
		}
	}
	height := m.bodyHeight()
	if len(body) > height {
		body = body[:height]
	}
	for len(body) < height {
		body = append(body, "")
	}

	parts := []string{m.titleBar(), strings.Join(body, "\n")}
	if m.prompt != nil {
		parts = append(parts, stylePrompt.Width(m.width).Render(m.prompt.question+"  [y/N]"))
	}
	parts = append(parts, m.helpLine())
	view := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, parts...))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

// titleBar names the command, shows the two panes with the one on screen
// highlighted, and counts the time since the invocation started.
func (m model) titleBar() string {
	progressTab, logsTab := styleTab, styleTab
	if m.pane == paneProgress {
		progressTab = styleTabOn
	} else {
		logsTab = styleTabOn
	}
	left := styleTitle.Render("sal "+m.ui.command) + " " + progressTab.Render("1 Progress") + " " + logsTab.Render("2 Logs")
	elapsed := formatDuration(m.now.Sub(m.ui.started))
	if m.pane == paneLogs {
		elapsed = plural(len(m.logs), "line") + " · " + elapsed
	}
	right := styleElapsed.Render(elapsed) + " "
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	return styleBar.Width(m.width).Render(left + strings.Repeat(" ", gap) + right)
}

func (m model) helpLine() string {
	help := "tab switch pane · ↑/↓ pgup/pgdn scroll · end follow · ctrl+c cancel"
	if m.prompt != nil {
		help = "y yes · n/enter no · " + help
	}
	return cut(styleHelp.Render(" "+help), m.width)
}
