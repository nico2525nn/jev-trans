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
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/lexgen"
	"github.com/nico/jev-trans/internal/lexicon"
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
	Metrics       StageMetrics    `json:"stageMetrics"`
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

// StageMetrics is the pipeline's own account of how far a sentence got.
//
// It lives here, in the code that actually did the work, rather than being
// re-derived by the measurement script. An external measurer inspecting the
// JSON has to guess what a field means, and it guessed wrong: it counted stages
// independently, so a sentence could "pass" construction after failing the
// frame stage, which made the funnel non-monotonic and the numbers beside it
// meaningless.
//
// Every Complete flag below is CUMULATIVE and requires every earlier stage to
// have completed as well. A sentence that fails morphology cannot be counted at
// any later stage.
type StageMetrics struct {
	// Morphology is complete when the input was segmented with no unresolved
	// surface. Coverage is total by construction — unknown surfaces are emitted
	// as morphemes rather than dropped — so this is about dictionary coverage.
	MorphologyComplete bool `json:"morphologyComplete"`
	Tokens             int  `json:"tokens"`
	OpaqueTokens       int  `json:"opaqueTokens"`

	// Predicates are complete when every clause head resolved to an ontology
	// predicate. One resolved clause among three is not a resolved sentence.
	PredicatesComplete bool `json:"predicatesComplete"`
	PredicatesTotal    int  `json:"predicatesTotal"`
	PredicatesResolved int  `json:"predicatesResolved"`

	// Frames are complete when every event carries the roles its ontology frame
	// declares mandatory. A non-empty argument map is not a filled frame.
	FramesComplete bool `json:"framesComplete"`
	FramesTotal    int  `json:"framesTotal"`
	FramesResolved int  `json:"framesResolved"`

	// Constructions are available when a construction realizing the sense was
	// selected for every event. The generic fallback frame does not count: it
	// carries no verb.
	ConstructionsComplete bool `json:"constructionsComplete"`
	ConstructionsTotal    int  `json:"constructionsTotal"`
	ConstructionsSelected int  `json:"constructionsSelected"`

	// RawCandidates is what the realizer produced, before verification.
	// AcceptedCandidates is what survived the semantic hard gate and ranking.
	// Reporting only the latter hides the exact stage that rejects work.
	RawCandidates      int `json:"rawCandidates"`
	AcceptedCandidates int `json:"acceptedCandidates"`
	Verified           int `json:"verified"`
	// EligibleCandidates passed the hard gate but are not certified: they are
	// the ones whose equivalence could not be settled, chiefly because the
	// target language needs a distinction the source cannot supply. Passing the
	// gate and proving equivalence are different claims and the metric has to
	// keep them apart, or "verification passed" starts counting sentences the
	// system merely declined to reject.
	EligibleCandidates int `json:"eligibleCandidates"`
	// CertifiedCandidates proved equivalence: no hard diff, no unsupported
	// information, no unresolved reading left open.
	CertifiedCandidates int `json:"certifiedCandidates"`
	Selected            int `json:"selected"`

	// OpenPositions counts ambiguity carried unresolved into the target, which
	// is plan.md section 13 and 15 doing their job rather than a defect.
	OpenPositions int `json:"openPositions"`
	// RejectedWithRule counts candidates the verifier refused, with the rule
	// names available on Result.Candidates.
	RejectedByGate int `json:"rejectedByGate"`

	// FrameLosses names, per sentence, why an argument did not reach the
	// event. Without it a frame drop is a count and the next fix is a guess;
	// with it the drop is a list of causes and the next fix is chosen from the
	// most frequent one. Values are stable slugs, not prose.
	FrameLosses []string `json:"frameLosses,omitempty"`

	// PredicateGaps records every clause head that did not become a predicate,
	// with enough context to tell a missing lexeme from a wrong clause head
	// from a misclassified auxiliary. A bare count of "73 unknown predicates"
	// does not say whether to write a dictionary, fix the parser, or both.
	PredicateGaps []PredicateGap `json:"predicateGaps,omitempty"`
}

// PredicateGap is one unresolved clause head.
type PredicateGap struct {
	Sentence string `json:"sentence"`
	Surface  string `json:"surface"`
	Lemma    string `json:"lemma,omitempty"`
	POS      string `json:"pos,omitempty"`
	// Cause is a stable slug: unknown_lexeme | unknown_pos | auxiliary |
	// no_clause_head | unknown_surface
	Cause string `json:"cause"`
	// SurfaceAll lists every token the clause head turned out to span, which is
	// how a wrong head (a copula picked for what was really ている) becomes
	// visible.
	SurfaceAll string `json:"surfaceAll,omitempty"`
	// MorphUnknown marks that the head's own morphemes were not all grounded,
	// which separates "the analyser could not see it" from "we have never
	// written it down".
	MorphUnknown bool `json:"morphUnknown,omitempty"`
	// Note carries the parser's own explanation when it gave one.
	Note string `json:"note,omitempty"`
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
	Jev *jev.Client
	// OfflineClient is used when Jev is nil. It exists so that the decision layer
	// never has to construct a client itself: doing so read OPENCODE_API_KEY
	// from the environment and opened live network calls inside a run whose trace
	// reported the oracle as offline, while also discarding the decision cache
	// on every call. A caller that wants no oracle configures an offline client
	// here explicitly.
	OfflineClient *jev.Client
	// Morph is the morphological backend registry. Nil installs the builtin
	// analyser alone, which is the state of an environment with no external
	// analyser configured. Exchanging the backend is a configuration change,
	// never a code change.
	Morph *lex.Registry
	// MorphProfile selects which lexicon the backend should use.
	MorphProfile lex.Profile
	// Predicates is the chain that supplies predicate knowledge. Nil installs
	// the curated table, which is the state of a process that configured
	// nothing — the same rule Morph follows, so the two halves of the analysis
	// stack fail the same way.
	//
	// It is configuration rather than an environment read inside the analyzer
	// for two reasons. An env read per analyze() call re-reads and re-parses the
	// inventory on every sentence, and it makes the answer depend on the
	// process environment at a moment the caller cannot see or record. Setting
	// it once at construction puts the choice where the trace can name it.
	Predicates *lexicon.Set
	// ExternalMorph records that a morphological backend outside this package
	// will answer. It disables the builtin historical-kana rewrite, which
	// exists only so the in-house dictionary can cope and would destroy
	// information an external analyser can use. SudachiDict and the 国語研
	// old-kana UniDic builds carry ゐ and the iteration marks as entries.
	ExternalMorph bool
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
	morph  *lex.Registry
	// predicates is resolved once, at construction, so the answer does not
	// depend on the process environment at a moment the caller cannot see.
	predicates *lexicon.Set
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
	morph := cfg.Morph
	if morph == nil {
		morph = lex.NewRegistry()
	}
	predicates := cfg.Predicates
	if predicates == nil {
		predicates = lexicon.ProviderFromEnv()
	}
	return &Engine{
		cfg:        cfg,
		stores:     map[string]*discourse.Store{},
		lex:        lexgen.Default(),
		morph:      morph,
		predicates: predicates,
	}
}

// Config exposes the engine configuration to the server for /api/health.
func (e *Engine) Config() EngineConfig { return e.cfg }

// PredicateProviders names the predicate knowledge this engine consults, in
// preference order. It is part of the health surface because the two states
// "the chain is the curated table alone" and "the chain could not load its
// inventory" produce identical translations and must not look identical from
// the outside.
func (e *Engine) PredicateProviders() []string {
	if e == nil {
		return nil
	}
	return e.predicates.Names()
}
