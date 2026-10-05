// Package trace is the observability spine of JEV-Trans.
//
// Every pipeline stage opens a Span. Spans form a tree that is serialized
// straight to the WebUI, which is how "you can see the internal circuit"
// becomes a property of the system rather than a feature bolted on later.
// Each span carries an arbitrary JSON payload describing the artifact that
// stage produced, so the UI can render morpheme lattices, packed forests,
// JLIR graphs, decision distributions and loss vectors from one code path.
package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Stage names follow the block diagram in plan.md §6 exactly, so the UI circuit
// and the specification stay in lockstep.
type Stage string

const (
	StageNormalize Stage = "INPUT_NORMALIZATION"
	StageMorph     Stage = "MORPHOLOGICAL_LATTICE"
	// The wire value is part of the API contract and the WebUI matches on it.
	// It used to read PACKED_SYNTAX_TIC_FOREST, so panels.js carried a second,
	// correct spelling as its display label.
	StageParse       Stage = "PACKED_SYNTACTIC_FOREST"
	StageSemantic    Stage = "SOURCE_SEMANTIC_FOREST"
	StageJLIR        Stage = "JLIR_CORE"
	StageDecisions   Stage = "JEV_DECISION_GRAPH"
	StageConstrained Stage = "CONSTRAINED_JLIR_STATE"
	StageProjection  Stage = "TARGET_PROJECTION_ENGINE"
	StagePlan        Stage = "TARGET_MESSAGE_PLANNER"
	StageConstruct   Stage = "CONSTRUCTION_SELECTION"
	StageRealize     Stage = "PACKED_REALIZATION_FOREST"
	StageSurface     Stage = "GRAMMATICAL_REALIZER"
	StageReparse     Stage = "TARGET_REPARSER"
	StageTargetJLIR  Stage = "TARGET_JLIR"
	StageVerify      Stage = "SEMANTIC_EQUIVALENCE_VERIFIER"
	StageRerank      Stage = "JEV_RERANKER"
	StageDiscourse   Stage = "DOCUMENT_STATE_UPDATE"
	StageOutput      Stage = "FINAL_OUTPUT"
)

// AllStages is the canonical stage order used by the WebUI circuit diagram.
var AllStages = []Stage{
	StageNormalize, StageMorph, StageParse, StageSemantic, StageJLIR,
	StageDecisions, StageConstrained, StageProjection, StagePlan,
	StageConstruct, StageRealize, StageSurface, StageReparse,
	StageTargetJLIR, StageVerify, StageRerank, StageDiscourse, StageOutput,
}

// Status is the outcome of a span.
type Status string

const (
	StatusOK    Status = "ok"
	StatusWarn  Status = "warn"
	StatusError Status = "error"
	StatusSkip  Status = "skip"
)

// Event is a single recorded unit of work. Data is any value that marshals to
// JSON; the server sends it through untouched.
type Event struct {
	ID       string            `json:"id"`
	Seq      int               `json:"seq"`
	Stage    Stage             `json:"stage"`
	Title    string            `json:"title"`
	Detail   string            `json:"detail,omitempty"`
	Status   Status            `json:"status"`
	Started  time.Time         `json:"started"`
	Duration float64           `json:"durationMs"`
	Counts   map[string]int    `json:"counts,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Data     any               `json:"data,omitempty"`
	Notes    []string          `json:"notes,omitempty"`
	Children []*Event          `json:"children,omitempty"`
}

// Recorder accumulates events for one translation run. It is safe for
// concurrent use because the decision graph may fan Jev questions out in
// parallel and the trace is what proves they ran.
type Recorder struct {
	mu     sync.Mutex
	seq    int
	root   *Event
	stack  []*Event
	ids    int
	all    []*Event
	closed bool
}

// New returns a Recorder whose root span covers the whole run.
func New(title string) *Recorder {
	now := time.Now()
	r := &Recorder{
		root: &Event{Stage: StageOutput, Title: title, Status: StatusOK, Started: now, Counts: map[string]int{}},
	}
	r.all = append(r.all, r.root)
	r.stack = []*Event{r.root}
	return r
}

// Span is an in-flight recording handle.
type Span struct {
	rec    *Recorder
	ev     *Event
	closed bool
}

// Open starts a new top-level event appended to the recorder.
func (r *Recorder) Open(stage Stage, title string) *Span {
	return r.open(nil, stage, title)
}

// Child starts a span nested under s.
func (s *Span) Child(stage Stage, title string) *Span {
	if s == nil || s.closed {
		return nil
	}
	return s.rec.open(s.ev, stage, title)
}

func (r *Recorder) open(parent *Event, stage Stage, title string) *Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.ids++
	ev := &Event{
		ID:      fmt.Sprintf("n%d", r.ids),
		Seq:     r.seq,
		Stage:   stage,
		Title:   title,
		Status:  StatusOK,
		Started: time.Now(),
		Counts:  map[string]int{},
	}
	if parent == nil {
		parent = r.currentLocked()
	}
	if parent != nil {
		ev.Seq = r.seq
		parent.Children = append(parent.Children, ev)
		r.stack = append(r.stack, ev)
	} else {
		r.root.Children = append(r.root.Children, ev)
		r.stack = append(r.stack, ev)
	}
	r.all = append(r.all, ev)
	return &Span{rec: r, ev: ev}
}

func (r *Recorder) currentLocked() *Event {
	if len(r.stack) == 0 {
		return r.root
	}
	return r.stack[len(r.stack)-1]
}

// SetTitle replaces the span title (used when a stage learns its real subject,
// e.g. the detected predicate family).
func (s *Span) SetTitle(t string) {
	if s == nil {
		return
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.ev.Title = t
}

// Detail sets a one-line human readable summary shown in the UI tooltip.
func (s *Span) Detail(format string, args ...any) {
	if s == nil {
		return
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.ev.Detail = fmt.Sprintf(format, args...)
}

// Count increments a named counter rendered as a badge in the UI.
// Count records a stage counter. Zero is recorded rather than discarded: a
// stage that produced nothing and a stage whose counter was never set are
// different facts, and the circuit view exists to tell them apart.
func (s *Span) Count(key string, n int) {
	if s == nil {
		return
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	if s.ev.Counts == nil {
		s.ev.Counts = map[string]int{}
	}
	s.ev.Counts[key] += n
}

// Label attaches a key/value chip to the span.
func (s *Span) Label(key, value string) {
	if s == nil {
		return
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	if s.ev.Labels == nil {
		s.ev.Labels = map[string]string{}
	}
	s.ev.Labels[key] = value
}

// Note appends a diagnostic line. Warnings and data-quality caveats that the
// user must see (unknown words, invented-information rejections, loss
// records) are surfaced here rather than swallowed.
func (s *Span) Note(format string, args ...any) {
	if s == nil {
		return
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.ev.Notes = append(s.ev.Notes, fmt.Sprintf(format, args...))
}

// Data attaches the stage artifact.
func (s *Span) Data(v any) {
	if s == nil {
		return
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.ev.Data = v
}

// Status sets the outcome colour.
func (s *Span) Status(st Status) {
	if s == nil {
		return
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.ev.Status = st
}

// Close finishes the span. Safe to call more than once.
func (s *Span) Close() {
	if s == nil || s.closed {
		return
	}
	s.closed = true
	r := s.rec
	r.mu.Lock()
	defer r.mu.Unlock()
	s.ev.Duration = float64(time.Since(s.ev.Started).Microseconds()) / 1000
	// Pop the matching stack frame.
	for i := len(r.stack) - 1; i >= 0; i-- {
		if r.stack[i] == s.ev {
			r.stack = r.stack[:i]
			break
		}
	}
}

// Do runs fn inside a span and closes it.
//
// It converts a panic into an error status so one broken stage cannot take the
// whole circuit down. The previous version documented that behaviour and
// contained no recover at all, so a panic in any stage unwound the request. A
// stage that cannot complete should show as a failed stage, not as a lost
// request.
func (r *Recorder) Do(stage Stage, title string, fn func(*Span) error) *Span {
	s := r.Open(stage, title)
	defer s.Close()
	if fn == nil {
		return s
	}
	defer func() {
		if p := recover(); p != nil {
			s.Status(StatusError)
			s.Note("stage panicked: %v", p)
		}
	}()
	if err := fn(s); err != nil {
		s.Status(StatusError)
		s.Note("error: %v", err)
	}
	return s
}

// Summary is a compact per-stage roll-up used by the CLI and the UI header.
type Summary struct {
	Stages  []StageSummary `json:"stages"`
	TotalMS float64        `json:"totalMs"`
	Events  int            `json:"events"`
	JevCost JevCost        `json:"jev"`
}

// StageSummary aggregates every span of one stage.
type StageSummary struct {
	Stage     Stage          `json:"stage"`
	Duration  float64        `json:"durationMs"`
	Events    int            `json:"events"`
	Status    Status         `json:"status"`
	Decisions []DecisionView `json:"decisions,omitempty"`
}

// Note appends a diagnostic line to an already-recorded event. It exists for
// callers that learn something after the span has closed — an interactive
// question raised at the end of a run, for instance.
func (e *Event) Note(format string, args ...any) {
	if e == nil {
		return
	}
	e.Notes = append(e.Notes, fmt.Sprintf(format, args...))
}

// JevCost records oracle usage so the UI can prove the decision budget.
type JevCost struct {
	Calls  int `json:"calls"`
	Cached int `json:"cached"`
	// Skipped counts decisions that were never asked, and Priors counts those
	// answered from the analysis fallback. They used to be folded into Cached,
	// which made the UI's "how much was free" figure over-report.
	Skipped      int     `json:"skipped"`
	Priors       int     `json:"priors"`
	Questions    int     `json:"questions"`
	LatencyMS    float64 `json:"latencyMs"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
}

// jevSourcePrior mirrors jev.SourcePrior. It is duplicated rather than
// imported so the trace package stays dependency-free: a span is recorded by
// code that may have no oracle client at all.
const jevSourcePrior = "prior"

// DecisionView is the UI-facing shape of one oracle decision. It lives in
// trace so that both the oracle and the renderer agree on the contract.
type DecisionView struct {
	ID            string             `json:"id"`
	Stage         string             `json:"stage"`
	Kind          string             `json:"kind"`
	Question      string             `json:"question"`
	Options       []string           `json:"options"`
	Winner        string             `json:"winner"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Source        string             `json:"source"`
	CacheHit      bool               `json:"cacheHit"`
	LatencyMS     float64            `json:"latencyMs"`
	Skipped       bool               `json:"skipped"`
	SkipReason    string             `json:"skipReason,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
}

// Summary builds the roll-up. decisions is the oracle's own decision log.
func (r *Recorder) Summary(decisions []DecisionView) Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	byStage := map[Stage]*StageSummary{}
	var order []Stage
	for _, ev := range r.all {
		st := byStage[ev.Stage]
		if st == nil {
			st = &StageSummary{Stage: ev.Stage, Status: StatusOK}
			byStage[ev.Stage] = st
			order = append(order, ev.Stage)
		}
		st.Duration += ev.Duration
		st.Events++
		if severity(ev.Status) > severity(st.Status) {
			st.Status = ev.Status
		}
	}
	s := Summary{Events: len(r.all)}
	for _, st := range order {
		s.Stages = append(s.Stages, *byStage[st])
		s.TotalMS += byStage[st].Duration
	}
	for _, d := range decisions {
		for i := range s.Stages {
			if string(s.Stages[i].Stage) == d.Stage {
				s.Stages[i].Decisions = append(s.Stages[i].Decisions, d)
			}
		}
		s.JevCost.Calls++
		s.JevCost.Questions++
		s.JevCost.LatencyMS += d.LatencyMS
		switch {
		case d.Skipped:
			s.JevCost.Skipped++
		case d.CacheHit:
			s.JevCost.Cached++
		}
		if d.Source == jevSourcePrior {
			s.JevCost.Priors++
		}
	}
	return s
}

func severity(s Status) int {
	switch s {
	case StatusError:
		return 3
	case StatusWarn:
		return 2
	case StatusOK:
		return 1
	}
	return 0
}

// Root returns the root event for direct embedding.
func (r *Recorder) Root() *Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.root
}

// All returns every event in creation order.
func (r *Recorder) All() []*Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Event, len(r.all))
	copy(out, r.all)
	return out
}

// JSON renders the whole trace as indented JSON. Used by the CLI dump and by
// the smoke tests to assert that stages really ran.
func (r *Recorder) JSON() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.root.Duration = float64(time.Since(r.root.Started).Microseconds()) / 1000
	return json.MarshalIndent(r.root, "", "  ")
}

// Flat renders a one-line-per-event console view.
func (r *Recorder) Flat() string {
	var b strings.Builder
	for _, ev := range r.All() {
		fmt.Fprintf(&b, "%8.1fms %-32s %-10s %s\n", ev.Duration, ev.Stage, ev.Status, ev.Title)
		for _, n := range ev.Notes {
			fmt.Fprintf(&b, "            · %s\n", n)
		}
	}
	return b.String()
}

// Context plumbing lets stages reach the ambient recorder without threading it
// through every constructor signature.
type ctxKey struct{}

func With(ctx context.Context, r *Recorder) context.Context {
	return context.WithValue(ctx, ctxKey{}, r)
}

// From returns the ambient recorder, or nil. A nil context is legal: the
// planner is exercised directly from tests and from the CLI without one, and
// context.Context is an interface whose nil value panics on every method call.
func From(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(ctxKey{}).(*Recorder)
	return r
}
