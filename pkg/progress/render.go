package progress

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The palette is the Catppuccin Mocha the SQL shell uses, so the two look
// like one tool.
const (
	colorRed      = "#F38BA8"
	colorYellow   = "#F9E2AF"
	colorGreen    = "#A6E3A1"
	colorBlue     = "#89B4FA"
	colorLavender = "#B4BEFE"
	colorText     = "#CDD6F4"
	colorSubtext1 = "#BAC2DE"
	colorSubtext0 = "#A6ADC8"
	colorOverlay1 = "#7F849C"
	colorSurface1 = "#45475A"
	colorCrust    = "#11111B"
)

var (
	styleDone     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen))
	styleFailed   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorRed))
	styleRunning  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorBlue))
	stylePending  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorOverlay1))
	stylePhase    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Bold(true)
	styleStep     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	styleElapsed  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtext0))
	styleDetail   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtext1))
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorRed))
	styleTitle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCrust)).Background(lipgloss.Color(colorLavender)).Bold(true).Padding(0, 1)
	styleTab      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtext0)).Padding(0, 1)
	styleTabOn    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCrust)).Background(lipgloss.Color(colorBlue)).Bold(true).Padding(0, 1)
	styleBar      = lipgloss.NewStyle().Background(lipgloss.Color(colorSurface1)).Foreground(lipgloss.Color(colorText))
	styleHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorOverlay1))
	stylePrompt   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCrust)).Background(lipgloss.Color(colorYellow)).Bold(true).Padding(0, 1)
	styleEllipsis = lipgloss.NewStyle().Foreground(lipgloss.Color(colorOverlay1)).Italic(true)
)

// spinnerFrames is what a running step shows in place of its mark, one frame
// per tick.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// recentDone is how many completed steps are kept under a step whose
// completed children would otherwise crowd the pane; the earlier ones fold
// into one line counting them. Running and failed steps are always shown.
const recentDone = 3

// maxDepth bounds how far under a phase steps are shown.
const maxDepth = 4

// row is one line of the progress pane before its columns are aligned.
type row struct {
	depth   int
	mark    string
	label   string
	elapsed string
	detail  string
	// failure is the error a failed step ended with, shown on the lines below
	// its row.
	failure string
	phase   bool
	// fold counts completed steps folded into this row instead of shown.
	fold int
}

// Render draws the progress pane as lines fitted to width: the phases of
// the plan in order, each with the steps under it, their elapsed times, and
// what their attributes say about them. Steps still running show the given
// spinner frame as their mark.
func (t *Tracker) Render(width int, now time.Time, spinner string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var rows []row
	bound := map[*Step]bool{}
	for _, phase := range t.phases {
		switch {
		case phase.Step != nil:
			bound[phase.Step] = true
			rows = t.stepRows(rows, phase.Step, 0, phase.Label, now, spinner)
		case phase.Skipped:
			rows = append(rows, row{mark: stylePending.Render("–"), label: stylePending.Render(phase.Label), detail: stylePending.Render("skipped"), phase: true})
		default:
			rows = append(rows, row{mark: stylePending.Render("·"), label: stylePending.Render(phase.Label), detail: stylePending.Render("upcoming"), phase: true})
		}
	}
	// a step under the root that marks no phase is still shown, after the plan
	if t.root != nil {
		for _, step := range t.root.Children {
			if !bound[step] && !hiddenSteps[step.Name] {
				rows = t.stepRows(rows, step, 0, "", now, spinner)
			}
		}
	}
	return layout(rows, width)
}

// stepRows appends the row for a step and the rows for the steps under it.
// A label overrides the one describe gives the step, which is how a phase
// is named by the plan rather than by its span.
func (t *Tracker) stepRows(rows []row, step *Step, depth int, label string, now time.Time, spinner string) []row {
	described, detail := describe(step)
	if label == "" {
		label = described
	}
	r := row{depth: depth, elapsed: formatDuration(step.Elapsed(now)), phase: depth == 0}
	switch {
	case step.Running():
		r.mark = styleRunning.Render(spinner)
	case step.Failed != "":
		r.mark = styleFailed.Render("✗")
		r.failure = step.Failed
	default:
		r.mark = styleDone.Render("✓")
	}
	if depth == 0 {
		r.label = stylePhase.Render(label)
	} else {
		r.label = styleStep.Render(label)
	}
	r.detail = styleDetail.Render(detail)
	rows = append(rows, r)
	if depth >= maxDepth {
		return rows
	}

	// every running or failed step is shown; of the completed ones only the
	// most recent are, with the rest folded into one line, so a phase over
	// hundreds of documents stays a few lines tall
	var shown []*Step
	completed := 0
	for _, child := range step.Children {
		if hiddenSteps[child.Name] {
			continue
		}
		if !child.Running() && child.Failed == "" {
			completed++
		}
		shown = append(shown, child)
	}
	folded := max(0, completed-recentDone)
	if folded > 0 {
		rows = append(rows, row{depth: depth + 1, fold: folded})
	}
	skipped := 0
	for _, child := range shown {
		if !child.Running() && child.Failed == "" && skipped < folded {
			skipped++
			continue
		}
		rows = t.stepRows(rows, child, depth+1, "", now, spinner)
	}
	return rows
}

// elapsedWidth is the right-hand column holding each row's elapsed time.
const elapsedWidth = 9

// layout writes the rows as lines of at most width cells: the mark, the
// label, and the detail run on from the left, cut where the line would
// otherwise overrun, and the elapsed time sits right-aligned at the end, so
// the times line up down the pane while a long IRI keeps as much of itself
// as the width allows.
func layout(rows []row, width int) []string {
	textWidth := max(10, width-elapsedWidth)
	var lines []string
	for _, r := range rows {
		indent := strings.Repeat("  ", r.depth)
		if r.fold > 0 {
			lines = append(lines, cut(indent+styleEllipsis.Render(fmt.Sprintf("… %s done", plural(r.fold, "step"))), width))
			continue
		}
		text := indent + r.mark + " " + r.label
		if r.detail != "" {
			text += "  " + r.detail
		}
		text = ansi.Truncate(text, textWidth, "…")
		if r.elapsed != "" {
			pad := textWidth - lipgloss.Width(text)
			text += strings.Repeat(" ", max(0, pad)) + styleElapsed.Render(fmt.Sprintf("%*s", elapsedWidth-1, r.elapsed))
		}
		lines = append(lines, cut(text, width))
		if r.failure != "" {
			// the first lines of the error, where a build says what went
			// wrong; the whole of it is printed once the pane closes
			for i, errLine := range strings.SplitN(strings.TrimSpace(r.failure), "\n", 4) {
				if i == 3 {
					lines = append(lines, cut(indent+"    "+styleEllipsis.Render("…"), width))
					break
				}
				lines = append(lines, cut(indent+"    "+styleError.Render(errLine), width))
			}
		}
	}
	return lines
}

// cut truncates a styled line to width, marking what was cut.
func cut(line string, width int) string {
	if width <= 0 {
		return line
	}
	return ansi.Truncate(line, width, "…")
}
