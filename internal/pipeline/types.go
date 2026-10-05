// Package pipeline wires the stages of JEV-Trans together in the order drawn
// in plan.md §6 and records everything that happened.
//
// The pipeline is the only place that knows the whole circuit. Every stage it
// calls is independently testable and independently inspectable, because the
// orchestrator hands each one a trace span and carries its artifact forward
// without flattening it.
package pipeline

import (
	"sync"

	"github.com/nico/jev-trans/internal/discourse"
	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jev"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lexgen"
	"github.com/nico/jev-trans/internal/plan"
	"github.com/nico/jev-trans/internal/semantics"
	"github.com/nico/jev-trans/internal/syntax"
	"github.com/nico/jev-trans/internal/trace"
	"github.com/nico/jev-trans/internal/verify"
)

// Status is the pipeline's honest verdict on a translation, from plan.md §61.
type Status string

const (
	StatusExact           Status = "EXACT"
	StatusGood            Status = "GOOD"
	StatusLossy           Status = "LOSSY"
	StatusAmbiguous       Status = "AMBIGUOUS"
	StatusUnderdetermined Status = "UNDERDETERMINED"
	StatusUnsupported     Status = "UNSUPPORTED"
	StatusUnparsable      Status = "UNPARSABLE"
)

// Severity orders statuses for reporting.
func (s Status) Severity() int {
	switch s {
	case StatusExact:
		return 0
	case StatusGood:
		return 1
	case StatusLossy:
		return 2
	case StatusAmbiguous:
		return 3
	case StatusUnderdetermined:
		return 4
	case StatusUnsupported:
		return 5
	case StatusUnparsable:
		return 6
	}
	return 6
}

// Confidence is the per-feature confidence breakdown of plan.md §59. A single
// global number would hide which part of the translation is weak.
type Confidence struct {
	Overall   float64            `json:"overall"`
	ByFeature map[string]float64 `json:"byFeature"`
}

// Option is one selectable answer to an interactive disambiguation question.
type Option struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Description string  `json:"description,omitempty"`
	Probability float64 `json:"probability"`
}

// Question is an interactive disambiguation request (plan.md §62). It is only
// emitted when the ambiguity actually changes the translation; everyday
// ambiguity is preserved in the output instead of being pushed onto the user.
type Question struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"` // referent | scope | sense | register
	Prompt  string   `json:"question"`
	Why     string   `json:"why,omitempty"`
	Options []Option `json:"options"`
	// Blocking marks a question the pipeline cannot proceed past.
	Blocking bool `json:"blocking"`
	// Default is the option the pipeline would take if the user never answers.
	Default string `json:"default,omitempty"`
}

// Candidate is one verified target realization.
type Candidate struct {
	Text          string                    `json:"text"`
	Status        Status                    `json:"status"`
	Loss          verify.LossVector         `json:"loss"`
	Confidence    Confidence                `json:"confidence"`
	Constructions []string                  `json:"constructions,omitempty"`
	Trace         map[string]string         `json:"trace,omitempty"`
	Unsupported   []jlir.UnsupportedFeature `json:"unsupported,omitempty"`
	Diffs         []verify.Diff             `json:"diffs,omitempty"`
	Notes         []string                  `json:"notes,omitempty"`
	Naturalness   float64                   `json:"naturalness"`
	RejectedBy    []string                  `json:"rejectedBy,omitempty"`
}

// Response is the full result of one translation. Its shape is the HTTP API
// contract and is rendered directly by the WebUI.
type Response struct {
	Source SideInfo `json:"source"`
	Target SideInfo `json:"target"`

	Result Result `json:"result"`

	Trace         *trace.Event    `json:"trace"`
	Summary       trace.Summary   `json:"summary"`
	JLIR          JLIRPair        `json:"jlir"`
	Artifacts     Artifacts       `json:"artifacts"`
	DocumentState discourse.State `json:"documentState"`
	Warnings      []string        `json:"warnings"`
}

// SideInfo describes one side of the translation.
type SideInfo struct {
	Text  string    `json:"text"`
	Lang  lang.Lang `json:"lang"`
	Title string    `json:"title,omitempty"`
}

// Result is the user-facing verdict.
type Result struct {
	Candidates []Candidate `json:"candidates"`
	Selected   *Candidate  `json:"selected"`
	Status     Status      `json:"status"`
	Questions  []Question  `json:"questions,omitempty"`
	// Interpretations lists the alternative source readings the pipeline kept
	// open, so the user can see the ambiguity was real and not hidden.
	Interpretations []Interpretation `json:"interpretations,omitempty"`
}

// Interpretation is one surviving source reading.
type Interpretation struct {
	Weight float64 `json:"weight"`
	Origin string  `json:"origin"`
	Label  string  `json:"label"`
}

// JLIRPair carries the two graphs the verifier compares.
type JLIRPair struct {
	Source *jlir.Graph `json:"source,omitempty"`
	Target *jlir.Graph `json:"target,omitempty"`
	// Readings is the source semantic forest.
	Readings []semantics.Reading `json:"readings,omitempty"`
}

// Artifacts is everything the UI renders as evidence.
type Artifacts struct {
	Morph          *forest.MorphForest  `json:"morph,omitempty"`
	Syntax         *syntax.Bundle       `json:"syntax,omitempty"`
	SemanticForest *semantics.Forest    `json:"semanticForest,omitempty"`
	Realization    *forest.Realization  `json:"realization,omitempty"`
	Projection     *plan.Projection     `json:"projection,omitempty"`
	Decisions      []trace.DecisionView `json:"decisions,omitempty"`
}

// Request is one translation request.
type Request struct {
	Text       string            `json:"text"`
	SourceLang lang.Lang         `json:"sourceLang"`
	TargetLang lang.Lang         `json:"targetLang"`
	Style      plan.StyleProfile `json:"style"`
	DocumentID string            `json:"documentId"`
	// Mode is "auto", "interactive" or "strict". Strict refuses to answer
	// while any ambiguity that changes the translation remains.
	Mode string `json:"mode"`
	// Answer carries a user response to a previously asked Question.
	Answer *Answer `json:"answer,omitempty"`
}

// Answer is a user disambiguation reply.
type Answer struct {
	QuestionID string `json:"questionId"`
	Option     string `json:"option"`
}

// EngineConfig configures an Engine.
type EngineConfig struct {
	Jev           *jev.Client
	MaxCandidates int
	// MaxOracleCalls bounds the decision budget for one translation. Exceeding
	// it degrades to priors rather than hanging the request.
	MaxOracleCalls int
	DefaultMode    string
	Debug          bool
}

// Engine runs translations. It owns the document stores so that a document id
// always maps to exactly one document state, and the lexicalizer that turns a
// source-language referent into a target-language surface.
type Engine struct {
	cfg    EngineConfig
	mu     sync.Mutex
	stores map[string]*discourse.Store
	lex    *lexgen.Lexicalizer
}

// NewEngine builds an Engine with sane defaults.
func NewEngine(cfg EngineConfig) *Engine {
	if cfg.MaxCandidates <= 0 {
		cfg.MaxCandidates = 12
	}
	if cfg.MaxOracleCalls <= 0 {
		cfg.MaxOracleCalls = 64
	}
	if cfg.DefaultMode == "" {
		cfg.DefaultMode = "auto"
	}
	return &Engine{
		cfg:    cfg,
		stores: map[string]*discourse.Store{},
		lex:    lexgen.Default(),
	}
}

// Config exposes the engine configuration to the server for /api/health.
func (e *Engine) Config() EngineConfig { return e.cfg }
