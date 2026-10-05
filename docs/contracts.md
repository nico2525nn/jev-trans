# JEV-Trans integration contracts

Read `plan.md` for the specification. This file pins the **interfaces** that
several packages share, so the slices can be built in parallel without drift.
Nobody edits the contract files without changing every consumer.

Contract files (read them, do not modify):

| File | Owner | Purpose |
|---|---|---|
| `internal/lang/lang.go` | foundation | `Lang`, `Script`, detection helpers |
| `internal/trace/trace.go` | foundation | trace spine; `trace.Recorder`, `Span`, `DecisionView`, `Summary` |
| `internal/jlir/jlir.go` | foundation | 9-layer IR types, `Graph`, `Entity`, `Event`, `ScopeNode` |
| `internal/jlir/dist.go` | foundation | `Distribution` (uncertainty layer), `Graph.DeepCopy`, `ResolveZero` |
| `internal/jlir/support.go` | foundation | provenance, `UnsupportedFeatures`, `InventedGender`, `Backtrace` |
| `internal/forest/forest.go` | foundation | packed forests: `Morph`, `MorphForest`, `Node`, `Alt`, `Forest`, `Tree`, `Realization` |
| `internal/syntax/clause.go` | foundation | `Clause`, `Phrase`, `Bundle` — the parser/semantics seam |
| `internal/ontology/ontology.go` | foundation | `Sense`, `Arg`, `Registry`, `Ladder` |

## Design rules that apply to every slice

1. **Never invent information.** A feature about gender, number, name, age or
   profession must not be added without provenance. If the source does not
   determine it, keep the value `UNKNOWN` or a `Distribution`.
2. **Never collapse early.** Where an analysis is ambiguous, emit alternatives
   and let the decision layer (or the user) resolve it.
3. **Every stage writes to the trace.** Open a span, attach its artifact, note
   anything suspicious. The WebUI renders the trace; a stage that does not
   write a span is invisible and considered unfinished.
4. **Deterministic output.** Sort anything iterated before it reaches JSON, or
   the UI diff and the tests become flaky.
5. **No new third-party dependencies.** Standard library only.

## Slices and the exact surface each must provide

### A. `internal/lex` + `internal/syntax` — source analysis

```go
// internal/lex/morph.go
package lex

// AnalyzeJA runs the Japanese morphological analyzer and returns the lattice.
func AnalyzeJA(src string) *forest.MorphForest

// AnalyzeEN runs the English tokenizer, tagger and lemmatizer.
func AnalyzeEN(src string) *forest.MorphForest
```

```go
// internal/syntax/parse.go
package syntax

func ParseJA(src string, mf *forest.MorphForest) *Bundle
func ParseEN(src string, mf *forest.MorphForest) *Bundle
```

Requirements:
- Longest-match dictionary lookup, then rule-based decomposition, then an
  explicit `UNKNOWN` morpheme. Never drop characters: `Bundle.Morphs` must
  cover the whole input span, and `MorphForest.UnknownRate` is reported.
- Inflection must yield real morphological features (`tense`, `aspect`,
  `polarity`, `politeness`, `honorific`, `completion`), because the projection
  stage consumes them rather than re-guessing from surface strings.
- Japanese must recognise zero subject arguments, case particles, を/が/に/で/
  と/から/まで/へ, は as a *topic* (not subject), honorific and humble forms,
  てしまう completion, sentence-final particles.
- English must handle contractions, irregular morphology, negation scope,
  modals, passives (flagged, with uncertainty), infinitival and that-complements.
- Ambiguity goes into `Bundle.Clauses[].Alternatives` or repeated slot entries,
  never into a silently chosen parse.

### B. `internal/ontology` data + `internal/lexicon` — knowledge

```go
// internal/ontology/senses.go
func Default() *Registry

// internal/lexicon/lexicon.go
func Default() *Lexicon
func (l *Lexicon) SensesJP(surface string) []SenseHit
func (l *Lexicon) SensesEN(surface string) []SenseHit
func (l *Lexicon) Idiom(surface string) (*Idiom, bool)
func (l *Lexicon) Term(source string) (*Term, bool)

type SenseHit struct {
    SenseID string         `json:"senseId"`
    Weight  float64        `json:"weight"`
    Note    string         `json:"note,omitempty"`
}
```

The ontology needs at least these families so that the pipeline is genuinely
usable on real sentences: communication (say/tell/ask/answer), motion, transfer,
mental states (think/know/want/feel/see/hear), perception, existence/identity,
possession, comparison, quantification, obligation/modal, change of state,
eating/drinking, weather, time reference, social interaction (meet/visit),
and the copula. Senses carry `Features` such as `physicality`, `intentionality`,
`animate_subject`, `motion`, `past_only`, `control`.

### C. `internal/jev` — the decision oracle

```go
package jev

func New(opts Options) *Client
func (c *Client) Enabled() bool

type Decision struct {
    ID      string          // "JEV-1"
    Stage   string          // one of D0..D9
    Kind    string          // choice | score | noul | ladder
    Question string
    Options []Option
    Answer  Answer
    Source  string          // "jev" | "cache" | "prior" | "skipped"
    CacheHit bool
    LatencyMS float64
}

func (c *Client) Ask(ctx context.Context, rq Request) (*Decision, error)
func (c *Client) AskMany(ctx context.Context, rqs []Request) ([]*Decision, error)
```

Endpoint: `POST https://opencode.ai/zen/v1/systemone`, model `jev-1.13-free`,
auth `Bearer $OPENCODE_API_KEY`. Question types `choice` (≤255 options),
`score` (2..10 levels), `noul`. The `state` must be a compact **structured**
payload containing only what the question needs — jev suffers context rot.
Never send free-form generation requests (plan.md §31).

When no API key is configured the client must still work: return priors with
`Source = "prior"`, and say so in the trace. The system must never crash or
silently pretend an oracle answered.

Also required: decision cache keyed by
`hash(semantic_context, candidate_set, document_style, decision_type)` (plan.md
§56), and information-gain scheduling that skips decisions whose outcome
cannot change the translation (plan.md §34).

### D. `internal/plan` — target side

```go
package plan

type StyleProfile struct {
    Politeness          float64 // 0 plain .. 1 polite
    Register            string  // neutral | formal | casual | technical | literary
    PronounExplicitness float64 // 0 suppress pronouns .. 1 always overt
    Literaryness        float64
}

type LossHint struct {
    Feature string
    Reason  string
}

type Request struct {
    JLIR    *jlir.Graph
    Source  lang.Lang
    Target  lang.Lang
    Style   StyleProfile
    Resolve func(stage, slot string, options []string, prior jlir.Distribution) (string, float64, string)
}

func Project(r Request) *Projection
func Realize(r Request, p *Projection) *forest.Realization
```

`Projection.LossHints []LossHint` records features the target language cannot
realize, so loss is known before generation rather than discovered afterwards.

`Projection` records what the target language demands: subject realization,
determiners, number, pronoun, word order, tense/aspect/agreement for English;
argument omission, topic marking, case particles, politeness morphology,
honorifics, sentence-final particles for Japanese.

Hard constraints reject branches (wrong entity, invented gender, missing
negation, wrong number/tense); soft constraints only rank (naturalness,
register, brevity). A rejected hard branch is recorded in
`forest.Realization.Rejected` with the rule that killed it.

### E. `internal/verify` — verification and ranking

```go
package verify

type Result struct {
    Status      string        // EXACT|GOOD|LOSSY|AMBIGUOUS|UNDERDETERMINED|UNSUPPORTED|UNPARSABLE
    Loss        LossVector
    Confidence  map[string]float64
    Unsupported []jlir.UnsupportedFeature
    Diffs       []Diff
    Notes       []string
}

func Verify(src, tgt *jlir.Graph, srcLang, tgtLang lang.Lang) *Result
func Reparse(text string, l lang.Lang) *jlir.Graph
func Rank(cands []Candidate) []Candidate
```

`Reparse` must reuse `syntax.ParseJA`/`ParseEN` through the semantic layer, so
the safety net of plan.md §40 is genuinely closed-loop.

### F. `internal/discourse` — document state

```go
package discourse

type Store struct{ ... }
func NewStore() *Store
func (s *Store) Observe(g *jlir.Graph)          // fold a new sentence in
func (s *Store) Salience() map[jlir.ID]float64
func (s *Store) Prior(referentCandidates []jlir.ID) *jlir.Distribution
func (s *Store) Snapshot() State
func (s *Store) Invalidate(changed []jlir.ID)   // incremental translation
```

### G. HTTP API and WebUI

```go
POST /api/translate      {text, sourceLang, targetLang, style?, documentId?, mode?}
POST /api/disambiguate   {documentId, questionId, option}
POST /api/document/reset {documentId}
GET  /api/ontology
GET  /api/lexicon?q=
GET  /api/health
GET  /                      (embedded UI)
```

The translate response is the contract the UI renders. It must contain, at
minimum:

```jsonc
{
  "source":   {"text": "...", "lang": "ja"},
  "target":   {"lang": "en"},
  "result": {
    "candidates": [{
      "text": "Taro gave a book to Hanako.",
      "status": "EXACT",
      "loss": {"propositional":0,"referential":0,"temporal":0,
               "pragmatic":0.12,"stylistic":0.04,"implicature":0},
      "confidence": {"overall":0.93, "byFeature": {"predicate":0.99}},
      "constructions": ["C.GIVE.01"],
      "unsupported": [],
      "notes": []
    }],
    "selected": { /* same shape, or null */ },
    "status": "EXACT",
    "questions": [ {"id":"q1","question":"...","options":[
        {"key":"e1","label":"Yamada","probability":0.56}]} ]
  },
  "trace":    { /* trace.Event tree */ },
  "summary":  { /* trace.Summary */ },
  "jlir":     {"source": { /* jlir.Graph */ }, "target": { /* jlir.Graph */ }},
  "artifacts":{
    "morph": { /* forest.MorphForest */ },
    "syntax": { /* syntax.Bundle */ },
    "semanticForest": {"readings":[{"weight":0.62,"graph":{/* jlir.Graph */}}]},
    "realization": { /* forest.Realization */ },
    "decisions": [ /* trace.DecisionView */ ]
  },
  "documentState": { },
  "warnings": []
}
```

## Build and verify

```sh
go build ./... && go vet ./...
```
`GOCACHE`/`GOTMPDIR` are already pointed at `.go/` because `/tmp` is
quota-limited on this machine.