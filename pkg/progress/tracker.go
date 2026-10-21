// Package progress is the terminal UI `sal build` and `sal run` show while
// they work: a progress pane following every step of the invocation, and a
// log pane holding the log as it would otherwise have been printed, toggled
// with a key. The progress pane is a view over the OpenTelemetry spans sal
// already records around its slow steps (see pkg/telemetry), fed by a span
// processor rather than by progress calls of its own, so a step shows up here
// exactly when it shows up in a trace.
package progress

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Step is one span of the invocation as the progress pane follows it: what it
// is, when it started and ended, the attributes it carries, and the steps
// under it.
type Step struct {
	Name    string
	Started time.Time
	// Ended is zero while the step is still running.
	Ended time.Time
	// Failed is the error a failed step ended with, and empty otherwise.
	Failed   string
	Attrs    map[string]attribute.Value
	Parent   *Step
	Children []*Step
}

// Running reports whether the step has not ended yet.
func (s *Step) Running() bool {
	return s.Ended.IsZero()
}

// Elapsed is how long the step has been running as of now, or how long it ran.
func (s *Step) Elapsed(now time.Time) time.Duration {
	if s.Running() {
		return now.Sub(s.Started)
	}
	return s.Ended.Sub(s.Started)
}

// attrInt reads an integer attribute, reporting whether the step carries it.
func (s *Step) attrInt(key string) (int64, bool) {
	value, ok := s.Attrs[key]
	if !ok || value.Type() != attribute.INT64 {
		return 0, false
	}
	return value.AsInt64(), true
}

// attrString reads a string attribute, empty when the step does not carry it.
func (s *Step) attrString(key string) string {
	value, ok := s.Attrs[key]
	if !ok || value.Type() != attribute.STRING {
		return ""
	}
	return value.AsString()
}

// child returns the first child step with the given name, or nil.
func (s *Step) child(name string) *Step {
	for _, child := range s.Children {
		if child.Name == name {
			return child
		}
	}
	return nil
}

// countChildren counts the direct children with the given name, all of them or
// only the ended ones.
func (s *Step) countChildren(name string, endedOnly bool) int {
	count := 0
	for _, child := range s.Children {
		if child.Name == name && (!endedOnly || !child.Running()) {
			count++
		}
	}
	return count
}

// Phase is one step of the plan a command follows, in the order the command
// takes them, bound to the span that marks it once that span starts. A phase
// whose span never started by the time a later phase did, or by the time the
// invocation ended, was skipped: a build with no imports fetches none, and a
// build with --without-sal-modules runs no task.
type Phase struct {
	// Span is the name of the span that marks the phase, started directly
	// under the invocation's root span.
	Span  string
	Label string
	// Step is the span the phase is bound to, nil until it starts.
	Step    *Step
	Skipped bool
}

// buildPlan is the order a `sal build` or `sal run` goes through its phases.
var buildPlan = []Phase{
	{Span: "build.validate_sources", Label: "Validate sources"},
	{Span: "build.import_ontologies", Label: "Import ontologies"},
	{Span: "build.materialize_modules", Label: "Run SAL module tasks"},
	{Span: "build.construct", Label: "Run CONSTRUCT queries"},
	{Span: "iceberg.write", Label: "Write the Iceberg table"},
}

// Tracker follows the spans of one invocation as a tree of steps and binds
// the ones directly under the root to the phases of the plan. It is a span
// processor, so it is installed through telemetry.Setup, and it is safe to
// read from another goroutine than the one the spans are recorded on.
type Tracker struct {
	mu     sync.Mutex
	steps  map[trace.SpanID]*Step
	root   *Step
	phases []*Phase
}

// NewTracker returns a tracker following the build plan.
func NewTracker() *Tracker {
	tracker := &Tracker{steps: map[trace.SpanID]*Step{}}
	for _, phase := range buildPlan {
		phase := phase
		tracker.phases = append(tracker.phases, &phase)
	}
	return tracker
}

// OnStart records a span as a step under its parent. The first span with no
// parent is the invocation's root; a later one started from a context
// without a span is kept under the root rather than as a second tree.
func (t *Tracker) OnStart(_ context.Context, span sdktrace.ReadWriteSpan) {
	t.mu.Lock()
	defer t.mu.Unlock()
	step := &Step{Name: span.Name(), Started: span.StartTime(), Attrs: attributeMap(span.Attributes())}
	if parent, ok := t.steps[span.Parent().SpanID()]; ok {
		step.Parent = parent
	} else if t.root != nil {
		step.Parent = t.root
	}
	t.steps[span.SpanContext().SpanID()] = step
	if step.Parent == nil {
		t.root = step
		return
	}
	step.Parent.Children = append(step.Parent.Children, step)
	if step.Parent == t.root {
		t.bindPhase(step)
	}
}

// bindPhase binds a step started directly under the root to the first phase
// of the plan waiting for its name. Every phase before it that has not
// started was skipped, since the plan is followed in order.
func (t *Tracker) bindPhase(step *Step) {
	for _, phase := range t.phases {
		if phase.Step != nil || phase.Skipped {
			continue
		}
		if phase.Span == step.Name {
			phase.Step = step
			return
		}
		// only decided once a later phase actually starts, so a step that
		// belongs to no phase leaves the plan alone
		if t.plannedLater(step.Name, phase) {
			phase.Skipped = true
		}
	}
}

// plannedLater reports whether a span name marks a phase of the plan that
// comes after the given one.
func (t *Tracker) plannedLater(name string, before *Phase) bool {
	seen := false
	for _, phase := range t.phases {
		if phase == before {
			seen = true
			continue
		}
		if seen && phase.Span == name {
			return true
		}
	}
	return false
}

// OnEnd records when a step ended, the attributes it ended with, and the
// error it failed with if any. The root ending marks every phase that never
// started as skipped.
func (t *Tracker) OnEnd(span sdktrace.ReadOnlySpan) {
	t.mu.Lock()
	defer t.mu.Unlock()
	step, ok := t.steps[span.SpanContext().SpanID()]
	if !ok {
		return
	}
	step.Ended = span.EndTime()
	step.Attrs = attributeMap(span.Attributes())
	if span.Status().Code == codes.Error {
		step.Failed = span.Status().Description
		if step.Failed == "" {
			step.Failed = "failed"
		}
	}
	if step == t.root {
		for _, phase := range t.phases {
			if phase.Step == nil {
				phase.Skipped = true
			}
		}
	}
}

// Shutdown implements the span processor; the tracker holds nothing to release.
func (t *Tracker) Shutdown(context.Context) error { return nil }

// ForceFlush implements the span processor; the tracker exports nothing.
func (t *Tracker) ForceFlush(context.Context) error { return nil }

// Root returns the invocation's root step, nil before it starts.
func (t *Tracker) Root() *Step {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.root
}

// Phases returns the plan as it stands: which phases have started, ended, or
// been skipped.
func (t *Tracker) Phases() []Phase {
	t.mu.Lock()
	defer t.mu.Unlock()
	phases := make([]Phase, 0, len(t.phases))
	for _, phase := range t.phases {
		phases = append(phases, *phase)
	}
	return phases
}

func attributeMap(attrs []attribute.KeyValue) map[string]attribute.Value {
	m := make(map[string]attribute.Value, len(attrs))
	for _, attr := range attrs {
		m[string(attr.Key)] = attr.Value
	}
	return m
}
