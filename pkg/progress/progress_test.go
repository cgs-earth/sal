package progress

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cgs-earth/sal/pkg/telemetry"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// installTracker makes the tracker see every span started through
// telemetry.Start for the rest of the test.
func installTracker(t *testing.T) *Tracker {
	t.Helper()
	tracker := NewTracker()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracker)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return tracker
}

// plain strips the styling so a test can read what the pane says.
func plain(lines []string) string {
	stripped := make([]string, 0, len(lines))
	for _, line := range lines {
		stripped = append(stripped, ansi.Strip(line))
	}
	return strings.Join(stripped, "\n")
}

func TestTrackerBindsPhasesInPlanOrderAndSkipsTheOnesNeverStarted(t *testing.T) {
	tracker := installTracker(t)
	ctx, root := telemetry.Start(context.Background(), "sal.build")
	_, validate := telemetry.Start(ctx, "build.validate_sources")
	telemetry.End(validate, nil)
	// imports and module tasks never start, so a build that skips both
	// shows them skipped the moment the CONSTRUCT queries begin
	_, construct := telemetry.Start(ctx, "build.construct")

	phases := tracker.Phases()
	require.Equal(t, "build.validate_sources", phases[0].Span)
	require.NotNil(t, phases[0].Step)
	require.False(t, phases[0].Step.Running())
	require.True(t, phases[1].Skipped)
	require.True(t, phases[2].Skipped)
	require.NotNil(t, phases[3].Step)
	require.True(t, phases[3].Step.Running())
	require.Nil(t, phases[4].Step)
	require.False(t, phases[4].Skipped)

	telemetry.End(construct, nil)
	telemetry.End(root, nil)
	require.True(t, tracker.Phases()[4].Skipped)
	require.False(t, tracker.Root().Running())
}

func TestStepsStartedOutsideTheRootContextStillJoinTheTree(t *testing.T) {
	tracker := installTracker(t)
	_, root := telemetry.Start(context.Background(), "sal.build")
	_, stray := telemetry.Start(context.Background(), "duckdb.open")
	telemetry.End(stray, nil)
	telemetry.End(root, nil)

	require.Len(t, tracker.Root().Children, 1)
	require.Equal(t, "duckdb.open", tracker.Root().Children[0].Name)
	require.Contains(t, plain(tracker.Render(100, time.Now(), "*")), "Open DuckDB")
}

func TestRenderShowsTheRunningStepWithTheSpinnerAndTheDoneOnesWithTheirTime(t *testing.T) {
	tracker := installTracker(t)
	ctx, _ := telemetry.Start(context.Background(), "sal.build", attribute.Int("sal.files", 3))
	validateCtx, _ := telemetry.Start(ctx, "build.validate_sources", attribute.Int("sal.documents", 3))
	_, doc := telemetry.Start(validateCtx, "validate.document", attribute.String("sal.file", "data/people.ttl"), attribute.Int("sal.terms", 12))
	telemetry.End(doc, nil)
	_, vocabulary := telemetry.Start(validateCtx, "validate.load_vocabulary", attribute.String("sal.vocabulary", "https://schema.org/"))
	telemetry.End(vocabulary, nil)
	telemetry.Start(validateCtx, "validate.document", attribute.String("sal.file", "data/places.ttl"))

	text := plain(tracker.Render(120, time.Now().Add(time.Second), "⠋"))

	require.Contains(t, text, "⠋ Validate sources")
	require.Contains(t, text, "1/3 documents · 1 vocabulary")
	require.Contains(t, text, "✓ Validate data/people.ttl")
	require.Contains(t, text, "12 terms")
	require.Contains(t, text, "✓ Load https://schema.org/")
	require.Contains(t, text, "⠋ Validate data/places.ttl")
	require.Regexp(t, `⠋ Validate data/places.ttl\s+1\.0s`, text)
	require.Contains(t, text, "· Import ontologies")
	require.Contains(t, text, "upcoming")
	require.Contains(t, text, "· Write the Iceberg table")
}

func TestRenderFoldsCompletedStepsBeyondTheMostRecent(t *testing.T) {
	tracker := installTracker(t)
	ctx, _ := telemetry.Start(context.Background(), "sal.build")
	validateCtx, _ := telemetry.Start(ctx, "build.validate_sources", attribute.Int("sal.documents", 10))
	for i := range 10 {
		_, doc := telemetry.Start(validateCtx, "validate.document", attribute.String("sal.file", "doc"+string(rune('0'+i))+".ttl"))
		telemetry.End(doc, nil)
	}
	_, running := telemetry.Start(validateCtx, "validate.document", attribute.String("sal.file", "running.ttl"))
	_, failed := telemetry.Start(validateCtx, "validate.document", attribute.String("sal.file", "broken.ttl"))
	telemetry.End(failed, errors.New("term schema:nameee is not defined"))

	text := plain(tracker.Render(120, time.Now(), "⠋"))

	require.Contains(t, text, "… 7 steps done")
	require.NotContains(t, text, "doc0.ttl")
	require.NotContains(t, text, "doc6.ttl")
	require.Contains(t, text, "doc7.ttl")
	require.Contains(t, text, "doc9.ttl")
	require.Contains(t, text, "⠋ Validate running.ttl")
	require.Contains(t, text, "✗ Validate broken.ttl")
	require.Contains(t, text, "term schema:nameee is not defined")
	telemetry.End(running, nil)
}

func TestRenderCountsTasksAndModulesAndSaysHowAModuleWasResolved(t *testing.T) {
	tracker := installTracker(t)
	ctx, _ := telemetry.Start(context.Background(), "sal.build")
	tasksCtx, _ := telemetry.Start(ctx, "build.materialize_modules", attribute.Int("sal.module.tasks", 2), attribute.Int("sal.modules", 1))
	taskCtx, first := telemetry.Start(tasksCtx, "salmodule.task", attribute.String("sal.module.task_class", "salmodule://github.com/cgs-earth/geoconnex/Crawl"))
	_, resolve := telemetry.Start(taskCtx, "salmodule.resolve", attribute.String("sal.module", "salmodule://github.com/cgs-earth/geoconnex/"))
	resolve.SetAttributes(attribute.String("sal.module.cache", "pinned image"))
	telemetry.End(resolve, nil)
	_, run := telemetry.Start(taskCtx, "salmodule.run_task", attribute.String("sal.module", "salmodule://github.com/cgs-earth/geoconnex/"))
	run.SetAttributes(attribute.Int64("sal.module.output_bytes", 3*1024*1024), attribute.Int("sal.module.files_copied", 2))
	telemetry.End(run, nil)
	first.SetAttributes(attribute.Int("sal.triples.materialized", 480))
	telemetry.End(first, nil)
	telemetry.Start(tasksCtx, "salmodule.task", attribute.String("sal.module.task_class", "salmodule://github.com/cgs-earth/geoconnex/Index"))

	text := plain(tracker.Render(140, time.Now(), "⠋"))

	require.Contains(t, text, "⠋ Run SAL module tasks")
	require.Contains(t, text, "1/2 tasks · 1 module")
	require.Contains(t, text, "✓ Task salmodule://github.com/cgs-earth/geoconnex/Crawl")
	require.Contains(t, text, "480 triples")
	require.Contains(t, text, "reused the pinned image")
	require.Contains(t, text, "3.0 MiB read · 2 files copied")
	require.Contains(t, text, "⠋ Task salmodule://github.com/cgs-earth/geoconnex/Index")
	// the phases before the tasks never started, so they were skipped
	require.Contains(t, text, "– Validate sources")
	require.Contains(t, text, "skipped")
}

func TestRenderSaysWhenTheTableHadNoChanges(t *testing.T) {
	tracker := installTracker(t)
	ctx, root := telemetry.Start(context.Background(), "sal.build")
	writeCtx, write := telemetry.Start(ctx, "iceberg.write")
	_, diff := telemetry.Start(writeCtx, "iceberg.diff")
	diff.SetAttributes(attribute.Int("sal.triples.existing", 9), attribute.Int("sal.triples.added", 0), attribute.Int("sal.triples.removed", 0), attribute.Int("sal.triples.unchanged", 9))
	telemetry.End(diff, nil)
	telemetry.End(write, nil)
	telemetry.End(root, nil)

	text := plain(tracker.Render(120, time.Now(), "⠋"))

	require.Contains(t, text, "✓ Write the Iceberg table")
	require.Contains(t, text, "no changes, no new snapshot")
	require.Contains(t, text, "9 existing · 0 added · 0 removed · 9 unchanged")
}

func TestRenderFitsEveryLineToTheWidth(t *testing.T) {
	tracker := installTracker(t)
	ctx, _ := telemetry.Start(context.Background(), "sal.build")
	_, imports := telemetry.Start(ctx, "build.import_ontologies")
	telemetry.Start(ctx, "build.import_ontology", attribute.String("sal.import", "https://example.org/a/very/long/ontology/iri/that/does/not/fit/in/a/narrow/terminal#"))
	telemetry.End(imports, nil)

	for _, line := range tracker.Render(40, time.Now(), "⠋") {
		require.LessOrEqual(t, ansi.StringWidth(line), 40, line)
	}
}

func TestLogBufferSplitsLinesAcrossWrites(t *testing.T) {
	var buffer logBuffer
	_, err := buffer.Write([]byte("first line\nsecond "))
	require.NoError(t, err)
	require.Equal(t, []string{"first line"}, buffer.LinesFrom(0))

	_, err = buffer.Write([]byte("line\nthird\n"))
	require.NoError(t, err)
	require.Equal(t, []string{"second line", "third"}, buffer.LinesFrom(1))
	require.Nil(t, buffer.LinesFrom(3))
	require.Equal(t, 3, buffer.Len())
}

func TestLogPaneFollowsNewLinesUntilScrolledUp(t *testing.T) {
	ui := New("build", nil, "")
	m := newModel(ui)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 6})
	m = updated.(model)
	for i := range 10 {
		_, err := ui.logs.Write([]byte("log line " + string(rune('0'+i)) + "\n"))
		require.NoError(t, err)
	}
	updated, _ = m.Update(tickMsg(time.Now()))
	m = updated.(model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(model)

	// four body lines of ten, following the bottom
	require.Equal(t, paneLogs, m.pane)
	view := ansi.Strip(m.View().Content)
	require.Contains(t, view, "log line 9")
	require.NotContains(t, view, "log line 0")

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(model)
	require.False(t, m.logFollow)
	_, err := ui.logs.Write([]byte("log line after\n"))
	require.NoError(t, err)
	updated, _ = m.Update(tickMsg(time.Now()))
	m = updated.(model)
	require.NotContains(t, ansi.Strip(m.View().Content), "log line after")

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m = updated.(model)
	require.True(t, m.logFollow)
	require.Contains(t, ansi.Strip(m.View().Content), "log line after")
}

func TestLogPaneWrapsLongLinesToTheWidth(t *testing.T) {
	ui := New("build", nil, "")
	m := newModel(ui)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	m = updated.(model)
	_, err := ui.logs.Write([]byte(strings.Repeat("x", 70) + "\n"))
	require.NoError(t, err)
	updated, _ = m.Update(tickMsg(time.Now()))
	m = updated.(model)

	require.Len(t, m.wrapped, 3)
	for _, line := range m.wrapped {
		require.LessOrEqual(t, ansi.StringWidth(line), 30)
	}
}

func TestPromptIsAnsweredFromTheKeyboard(t *testing.T) {
	ui := New("build", nil, "")
	m := newModel(ui)
	reply := make(chan bool, 1)
	updated, _ := m.Update(promptMsg{question: "Include the prefix?", reply: reply})
	m = updated.(model)
	require.Contains(t, ansi.Strip(m.View().Content), "Include the prefix?  [y/N]")

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = updated.(model)
	require.True(t, <-reply)
	require.Nil(t, m.prompt)

	updated, _ = m.Update(promptMsg{question: "Again?", reply: reply})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	require.False(t, <-reply)
	require.Nil(t, m.prompt)
}

func TestPromptIsRefusedWhenThePanesHaveClosed(t *testing.T) {
	ui := New("build", nil, "")
	ui.program = tea.NewProgram(newModel(ui), tea.WithoutRenderer(), tea.WithoutSignals(), tea.WithInput(nil))
	close(ui.done)
	// the program never ran, so Send blocks until its context is done;
	// Kill ends it so the prompt falls through to the closed panes
	ui.program.Kill()

	require.False(t, ui.confirm("Include the prefix?"))
}

func TestCtrlCInterruptsTheWork(t *testing.T) {
	m := newModel(New("build", nil, ""))
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	require.NotNil(t, cmd)
	require.IsType(t, tea.InterruptMsg{}, cmd())
}

func TestDoneQuitsThePanes(t *testing.T) {
	m := newModel(New("build", nil, ""))
	updated, cmd := m.Update(doneMsg{err: errors.New("validation failed")})
	require.True(t, updated.(model).finished)
	require.NotNil(t, cmd)
	require.IsType(t, tea.QuitMsg{}, cmd())
}

func TestSummaryReportsAFailedBuildAndWhereTheLogIs(t *testing.T) {
	ui := New("build", nil, "/tmp/sal/logs/build.log")
	ui.tracker = installTracker(t)
	ctx, root := telemetry.Start(context.Background(), "sal.build")
	_, validate := telemetry.Start(ctx, "build.validate_sources", attribute.Int("sal.documents", 1))
	telemetry.End(validate, errors.New("data/people.ttl:4 nameee is not a term of https://schema.org/"))
	telemetry.End(root, errors.New("validation failed"))

	summary := ansi.Strip(ui.Summary())

	require.Contains(t, summary, "✗ sal build failed after")
	require.Contains(t, summary, "✗ Validate sources")
	require.Contains(t, summary, "nameee is not a term")
	require.Contains(t, summary, "– Write the Iceberg table")
	require.Contains(t, summary, "log: /tmp/sal/logs/build.log")
}

func TestSummaryReportsAFinishedBuild(t *testing.T) {
	ui := New("run", nil, "")
	ui.tracker = installTracker(t)
	_, root := telemetry.Start(context.Background(), "sal.run")
	telemetry.End(root, nil)

	summary := ansi.Strip(ui.Summary())

	require.Contains(t, summary, "✓ sal run finished in")
	require.NotContains(t, summary, "log:")
}

func TestFormatDuration(t *testing.T) {
	require.Equal(t, "0.3s", formatDuration(300*time.Millisecond))
	require.Equal(t, "12.4s", formatDuration(12400*time.Millisecond))
	require.Equal(t, "3m05s", formatDuration(3*time.Minute+5*time.Second))
	require.Equal(t, "1h02m", formatDuration(time.Hour+2*time.Minute))
}

func TestPlural(t *testing.T) {
	require.Equal(t, "1 file", plural(1, "file"))
	require.Equal(t, "3 files", plural(3, "file"))
	require.Equal(t, "2 vocabularies", plural(2, "vocabulary"))
	require.Equal(t, "2 files copied", plural(2, "file copied"))
	require.Equal(t, "1 triple constructed", plural(1, "triple constructed"))
}

func TestFormatBytes(t *testing.T) {
	require.Equal(t, "512 B", formatBytes(512))
	require.Equal(t, "1.5 KiB", formatBytes(1536))
	require.Equal(t, "3.0 MiB", formatBytes(3*1024*1024))
}

// a span whose parent the tracker never saw is a child of the root, which
// keeps the tree whole when a context carries a span from elsewhere
func TestUnknownParentFallsBackToTheRoot(t *testing.T) {
	tracker := installTracker(t)
	_, root := telemetry.Start(context.Background(), "sal.build")
	foreign := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true,
	}))
	_, child := telemetry.Start(foreign, "iceberg.write")
	telemetry.End(child, nil)
	telemetry.End(root, nil)

	require.Len(t, tracker.Root().Children, 1)
	require.NotNil(t, tracker.Phases()[4].Step)
}

func TestPanesAreNotShownInCI(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}
	// a terminal that is not CI; whether stdin and stderr are terminals is
	// then up to the process, which under `go test` they are not
	require.False(t, terminalSupported(env(map[string]string{"TERM": "xterm-256color", "CI": "false"})))
	require.False(t, terminalSupported(env(map[string]string{"TERM": "xterm-256color", "CI": "true"})))
	require.False(t, terminalSupported(env(map[string]string{"TERM": "xterm-256color", "GITHUB_ACTIONS": "true"})))
	require.False(t, terminalSupported(env(map[string]string{"TERM": "xterm-256color", "GITLAB_CI": "true"})))
	require.False(t, terminalSupported(env(map[string]string{"TERM": "dumb"})))
	require.False(t, terminalSupported(env(map[string]string{})))
}
