package progress

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	"github.com/charmbracelet/colorprofile"
)

// ErrInterrupted is returned by Run when the user pressed ctrl+c, or the
// process was sent SIGINT, before the work finished.
var ErrInterrupted = tea.ErrInterrupted

// UI is the pair of panes a command runs under: it owns the span processor
// the progress pane reads, the writer the log pane reads, and the terminal
// while Run is running.
type UI struct {
	command string
	tracker *Tracker
	logs    *logBuffer
	logFile io.Writer
	logPath string
	started time.Time
	program *tea.Program
	// done is closed when the panes have closed, which releases a prompt
	// still waiting for an answer
	done chan struct{}
	// width is the terminal width the panes last drew at, which the summary
	// printed after them is fitted to
	width       int
	interrupted bool
}

// New prepares the panes for a command. Every log line is also written to
// logFile when it is not nil, and logPath is where the user is told to find
// it once the panes close.
func New(command string, logFile io.Writer, logPath string) *UI {
	return &UI{
		command: command,
		tracker: NewTracker(),
		logs:    &logBuffer{},
		logFile: logFile,
		logPath: logPath,
		started: time.Now(),
		done:    make(chan struct{}),
		width:   100,
	}
}

// ciVariables are set by CI providers in every job they run. `CI` is the
// convention most of them follow; the rest cover the providers that set only
// their own name.
var ciVariables = []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "TRAVIS", "JENKINS_URL", "TF_BUILD", "TEAMCITY_VERSION", "CODEBUILD_BUILD_ID", "CLOUD_BUILD"}

// TerminalSupported reports whether the panes can be shown: stdin and stderr
// are both terminals, since the panes draw on stderr and read keys from
// stdin, the terminal is a real one, and the process is not running in CI,
// where nobody is watching and a log that scrolls is what the job keeps.
// A CI runner that attaches a pseudo-terminal would otherwise open them.
func TerminalSupported() bool {
	return terminalSupported(os.Getenv)
}

// terminalSupported is TerminalSupported with the environment injectable.
func terminalSupported(getenv func(string) string) bool {
	for _, name := range ciVariables {
		if value := getenv(name); value != "" && value != "false" && value != "0" {
			return false
		}
	}
	if term := getenv("TERM"); term == "" || term == "dumb" {
		return false
	}
	for _, file := range []*os.File{os.Stdin, os.Stderr} {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

// LogWriter is where the slog handler writes while the panes are up: each
// line goes to the log pane and to the log file.
func (ui *UI) LogWriter() io.Writer {
	if ui.logFile == nil {
		return ui.logs
	}
	return io.MultiWriter(ui.logs, ui.logFile)
}

// SpanProcessor is what feeds the progress pane; pass it to telemetry.Setup.
func (ui *UI) SpanProcessor() telemetry.SpanProcessor {
	return ui.tracker
}

// Run shows the panes while work runs and returns what work returned, or
// ErrInterrupted when the user cancelled before it finished. The panes close
// as soon as work returns; Summary then says how it went. A yes/no prompt
// the work asks through pkg.Confirm is answered in the panes.
func (ui *UI) Run(work func() error) (err error) {
	ui.program = tea.NewProgram(newModel(ui), tea.WithOutput(os.Stderr), tea.WithInput(os.Stdin))
	previousPrompt := pkg.ConfirmPrompt
	pkg.ConfirmPrompt = ui.confirm
	defer func() { pkg.ConfirmPrompt = previousPrompt }()

	result := make(chan error, 1)
	panicked := make(chan any, 1)
	go func() {
		defer func() {
			// a panic in the work must not unwind while the terminal is in
			// the panes' raw mode; it is raised again once they have closed
			if r := recover(); r != nil {
				panicked <- r
				ui.program.Send(doneMsg{})
			}
		}()
		err := work()
		result <- err
		ui.program.Send(doneMsg{err: err})
	}()

	final, runErr := ui.program.Run()
	close(ui.done)
	if m, ok := final.(model); ok && m.width > 0 {
		ui.width = m.width
	}
	select {
	case r := <-panicked:
		panic(r)
	default:
	}
	if errors.Is(runErr, tea.ErrInterrupted) {
		ui.interrupted = true
		return ErrInterrupted
	}
	// the panes failing to draw does not stop the work, which keeps logging
	// to the file; its result is still what the command reports
	workErr := <-result
	if runErr != nil {
		return errors.Join(workErr, fmt.Errorf("progress panes: %w", runErr))
	}
	return workErr
}

// confirm answers pkg.Confirm through the panes, as no when they closed
// before the user answered.
func (ui *UI) confirm(question string) bool {
	reply := make(chan bool, 1)
	ui.program.Send(promptMsg{question: question, reply: reply})
	select {
	case answer := <-reply:
		return answer
	case <-ui.done:
		return false
	}
}

// Summary is what is printed once the panes have closed, so that the
// terminal keeps a record of the invocation: how it ended and after how long,
// the plan with every step's time, and where the full log is.
func (ui *UI) Summary() string {
	now := time.Now()
	root := ui.tracker.Root()
	elapsed := now.Sub(ui.started)
	if root != nil && !root.Running() {
		elapsed = root.Elapsed(now)
	}
	var headline string
	switch {
	case ui.interrupted:
		headline = styleFailed.Render("✗") + " sal " + ui.command + " interrupted after " + formatDuration(elapsed)
	case root != nil && root.Failed != "":
		headline = styleFailed.Render("✗") + " sal " + ui.command + " failed after " + formatDuration(elapsed)
	default:
		headline = styleDone.Render("✓") + " sal " + ui.command + " finished in " + formatDuration(elapsed)
	}
	lines := []string{headline}
	lines = append(lines, ui.tracker.Render(ui.width, now, "→")...)
	if ui.logPath != "" {
		lines = append(lines, styleHelp.Render("log: "+ui.logPath))
	}
	return strings.Join(lines, "\n")
}

// PrintSummary writes Summary to w with its colours fitted to what w can show.
func (ui *UI) PrintSummary(w io.Writer) {
	_, _ = fmt.Fprintln(colorprofile.NewWriter(w, os.Environ()), ui.Summary())
}
