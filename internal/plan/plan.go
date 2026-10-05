// Package plan is the target half of JEV-Trans: it turns a JLIR graph into a
// target-language message plan and then into a packed realization forest.
//
// The package never writes prose. It decides what the target language *must*
// express (plan.md §35), picks a construction for every event (§26), and then
// searches for target strings that satisfy the semantic constraints (§36):
//
//	find T such that Grammar(T) ∧ Semantics(T) ⊇ RequiredMeaning
//	                ∧ Contradictions(T, Source) = ∅ ∧ UnsupportedInfo(T) = ∅
//
// Naturalness is only afterwards, as an objective function (§37, §45).
//
// Three files carry the three responsibilities:
//
//	plan.go         request, style and projection types, plus the language
//	                neutral decision helpers every stage shares
//	construction.go the construction library (predicate + discourse -> frame)
//	project_en.go   what English forces: overt subject, article, number,
//	                agreement, prepositions, word order
//	project_jp.go   what Japanese allows: argument omission, topic marking,
//	                case particles, politeness morphology, SOV order
//	realize*.go     the packed forest and the two morphological realizers
package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/trace"
)

// Request is one translation job on the target side. It carries the source
// interpretation, the language pair, the target style and the decision hook.
//
// Resolve is the seam to the decision oracle (plan.md §30). It is called only
// for decisions that a prior cannot settle; its signature is
//
//	func(stage, slot string, options []string, prior jlir.Distribution) (string, float64, string)
//
// returning the chosen option, its probability and the decision id. When it is
// nil the planner runs on deterministic priors and says so in Projection.Notes
// rather than pretending an oracle answered.
type Request struct {
	JLIR    *jlir.Graph
	Source  lang.Lang
	Target  lang.Lang
	Style   StyleProfile
	Resolve func(stage, slot string, options []string, prior jlir.Distribution) (string, float64, string)

	// Ctx carries the ambient trace recorder (trace.With). It is optional: a
	// request built by the CLI without one still runs, and its spans are
	// discarded instead of being dropped.
	Ctx context.Context

	// Lexicalize returns the target-language surface of a referent the graph
	// only knows in the source language. Without it the planner has no way to
	// say "book" for 本 or "Taro" for 太郎, and must refuse to emit a target
	// surface rather than borrowing the source script (plan.md §10, §51).
	Lexicalize Lexicalizer
}

// Lexicalizer maps a source surface onto a target one. ok=false is the normal
// answer for an unknown word and must never be treated as licence to guess.
type Lexicalizer func(surface string, src, tgt lang.Lang, typ string, proper bool) (string, bool)

// Stage names used when asking the oracle. They mirror the trace stages so a
// decision can be attributed to the pipeline stage that needed it.
const (
	DecideConstruction = "construction"
	DecidePronoun      = "pronoun"
	DecideCase         = "case"
	DecideMarker       = "topic"
	DecideSense        = "sense"
	DecideNumber       = "number"
	DecidePoliteness   = "politeness"
)

// Registers understood by the style profile. They bias construction selection
// (plan.md §26) but never hard-constrain it: a technical register still allows
// a neutral construction, just at a lower naturalness score.
const (
	RegisterNeutral   = "neutral"
	RegisterFormal    = "formal"
	RegisterCasual    = "casual"
	RegisterTechnical = "technical"
	RegisterLiterary  = "literary"
)

// StyleProfile is the target politeness/style configuration (plan.md §48). All
// fields are 0..1 scores except Register, which is one of the Register*
// constants; the zero value means "neutral, plain, low pronoun explicitness".
type StyleProfile struct {
	Politeness          float64 `json:"politeness"`          // 0 plain .. 1 polite
	Register            string  `json:"register"`            // neutral|formal|casual|technical|literary
	PronounExplicitness float64 `json:"pronounExplicitness"` // 0 suppress pronouns .. 1 always overt
	Literaryness        float64 `json:"literaryness"`
}

// Normalized clamps the profile into its legal range and fills in the default
// register, so downstream code never has to defend against out-of-range input.
func (s StyleProfile) Normalized() StyleProfile {
	out := s
	out.Politeness = clamp01(out.Politeness)
	out.PronounExplicitness = clamp01(out.PronounExplicitness)
	out.Literaryness = clamp01(out.Literaryness)
	if out.Register == "" {
		out.Register = RegisterNeutral
	}
	return out
}

// Polite reports whether the target should use polite morphology (です/ます for
// Japanese, and a non-impersonal English register).
func (s StyleProfile) Polite() bool { return s.Normalized().Politeness >= 0.5 }

// Plain is the complement of Polite, spelled out because the choice is a hard
// morphological fork in Japanese.
func (s StyleProfile) Plain() bool { return !s.Polite() }

// PreferPronouns reports whether an explicit pronoun should be used even when
// a name is available (plan.md §47: English "he" must not become 彼は).
func (s StyleProfile) PreferPronouns() bool { return s.Normalized().PronounExplicitness >= 0.5 }

// Loss dimensions, matching the translation loss vector of plan.md §43.
const (
	DimPropositional = "propositional"
	DimReferential   = "referential"
	DimTemporal      = "temporal"
	DimPragmatic     = "pragmatic"
	DimStylistic     = "stylistic"
	DimImplicature   = "implicature"
)

// LossHint records one feature the target language cannot carry across, or can
// only carry across approximately. plan.md §46 and §47 make this explicit
// rather than implicit: honorific address, topic marking, explicit zeros and
// unsourced number are all *recorded*, not silently dropped.
type LossHint struct {
	Feature   string  `json:"feature"`
	Dimension string  `json:"dimension"`
	Weight    float64 `json:"weight"`
	Event     jlir.ID `json:"event,omitempty"`
	Entity    jlir.ID `json:"entity,omitempty"`
	Reason    string  `json:"reason"`
}

// DecisionRecord is one oracle (or prior) call made by the planner. The UI
// renders it next to the projection so a user can see which choices were asked
// about and which were merely assumed.
type DecisionRecord struct {
	Stage       string   `json:"stage"`
	Slot        string   `json:"slot"`
	Question    string   `json:"question,omitempty"`
	Options     []string `json:"options,omitempty"`
	Chosen      string   `json:"chosen"`
	Probability float64  `json:"probability"`
	DecisionID  string   `json:"decisionId,omitempty"`
	Source      string   `json:"source"` // "jev" | "prior" | "skipped"
}

// Projection is what the target language demands before a single word is
// written (plan.md §35).
type Projection struct {
	Source lang.Lang    `json:"source"`
	Target lang.Lang    `json:"target"`
	Style  StyleProfile `json:"style"`

	Events    []EventPlan `json:"events"`
	LossHints []LossHint  `json:"lossHints,omitempty"`
	Notes     []string    `json:"notes,omitempty"`

	// Decisions is the planner's own decision log.
	Decisions []DecisionRecord `json:"decisions,omitempty"`
	// SourceRegister/SourceFormality record what the source was like, so the
	// target style could be inferred from it when the caller supplied none.
	SourceRegister  string  `json:"sourceRegister,omitempty"`
	SourceFormality float64 `json:"sourceFormality,omitempty"`
	// StyleInferred marks a style the planner guessed from the source instead
	// of one the caller pinned.
	StyleInferred bool `json:"styleInferred,omitempty"`
	// Honorific and SentenceFinal carry document level decisions that apply to
	// every clause (Japanese only).
	Honorific     bool   `json:"honorific,omitempty"`
	SentenceFinal string `json:"sentenceFinal,omitempty"`

	// lex is the caller's target-surface resolver, carried so that projection
	// and realization both consult one source of truth. It is not serialized:
	// it is a capability, not a result.
	lex Lexicalizer
}

// Lexicalizer returns the resolver the caller supplied, or nil.
func (p *Projection) Lexicalizer() Lexicalizer {
	if p == nil {
		return nil
	}
	return p.lex
}

// TargetLang returns the target language. It exists because Projection carries
// the pair in two fields and every consumer wants the second one.
func (p *Projection) TargetLang() lang.Lang {
	if p == nil {
		return ""
	}
	return p.Target
}

// EventPlan is the target-side plan for one event: which arguments exist, which
// morphology the target needs, and which constructions may realize it.
type EventPlan struct {
	EventID jlir.ID `json:"eventId"`
	SenseID string  `json:"senseId"`
	// SenseFamily is the parent of the ontology sense id ("TRANSFER" for
	// "TRANSFER.01"). The construction library and the verb lexicon are keyed
	// by family, so a new sense number never breaks realization.
	SenseFamily string `json:"senseFamily"`

	Tense      string `json:"tense,omitempty"`
	Aspect     string `json:"aspect,omitempty"`
	Completion string `json:"completion,omitempty"`
	Polarity   string `json:"polarity,omitempty"`
	Modality   string `json:"modality,omitempty"`
	Mood       string `json:"mood,omitempty"`

	Subject *NPPlan            `json:"subject,omitempty"`
	Object  *NPPlan            `json:"object,omitempty"`
	Args    map[string]*NPPlan `json:"args,omitempty"`

	// Construction is the chosen construction id; Constructions keeps the
	// remaining live alternatives so the realizer can branch on them.
	Construction  string   `json:"construction,omitempty"`
	Constructions []string `json:"constructions,omitempty"`

	// Aux is the auxiliary the target needs ("be", "have", "will", "must"),
	// "" for a bare lexical verb.
	Aux string `json:"aux,omitempty"`
	// SubjectRequired marks an event whose subject the target must realize
	// overtly (English) and may omit (Japanese after discourse priming).
	SubjectRequired bool `json:"subjectRequired,omitempty"`
	// OmitSubject is the Japanese decision not to write the subject at all.
	OmitSubject bool `json:"omitSubject,omitempty"`
	// NumberUndetermined records that English had to pick a number the source
	// does not license (plan.md §46). It is an UNDERDETERMINED condition, not a
	// coin flip: the alternative stays in the forest and a loss hint is filed.
	NumberUndetermined bool `json:"numberUndetermined,omitempty"`
	// Politeness and Register drive Japanese morphology and construction choice.
	Politeness float64 `json:"politeness,omitempty"`
	Register   string  `json:"register,omitempty"`
	Honorific  bool    `json:"honorific,omitempty"`

	Notes []string `json:"notes,omitempty"`

	// rejects holds the hard-constraint rejections discovered while projecting
	// this event. They stay off the wire because they are large; the realizer
	// replays them into forest.Realization.Rejected, which is the artifact the
	// UI renders.
	rejects []RejectInfo
}

// Role returns the plan for a role, or nil.
func (e *EventPlan) Role(role string) *NPPlan {
	if e == nil || e.Args == nil {
		return nil
	}
	return e.Args[role]
}

// Note appends a diagnostic line to the event plan.
func (e *EventPlan) Note(msg string) {
	if e == nil {
		return
	}
	e.Notes = append(e.Notes, msg)
}

// NPPlan is one projected noun phrase. Only the fields the target actually
// needs are filled; the rest stay at their neutral value.
type NPPlan struct {
	EntityID jlir.ID `json:"entityId,omitempty"`
	// Determiner is "a", "an", "the", "", "this" or "that".
	Determiner string   `json:"determiner,omitempty"`
	Noun       string   `json:"noun,omitempty"`
	Adjectives []string `json:"adjectives,omitempty"`
	// Pronoun is he/she/they/it — only set when the source licenses it.
	Pronoun     string   `json:"pronoun,omitempty"`
	Number      string   `json:"number,omitempty"`
	Possessives []string `json:"possessives,omitempty"`
	Honorific   string   `json:"honorific,omitempty"`
	// IsZero is the Japanese realization "write nothing here" (plan.md §3):
	// the referent is preserved internally while the surface stays empty.
	IsZero bool     `json:"isZero,omitempty"`
	Notes  []string `json:"notes,omitempty"`

	// --- additions required to actually run the projection ---------------

	// Role is the semantic role this phrase fills.
	Role string `json:"role,omitempty"`
	// Alias is the surface form the graph already knows in the target
	// language. When it is empty the planner could not fill the gap and says
	// so rather than transliterating.
	Alias string `json:"alias,omitempty"`
	// NounPhrase is the head noun only (no determiner), which is what the
	// article chooser needs.
	Type   string `json:"type,omitempty"`
	Proper bool   `json:"proper,omitempty"`
	Gender string `json:"gender,omitempty"`
	// GenderSupported is true only when gender has upstream provenance.
	GenderSupported bool    `json:"genderSupported,omitempty"`
	Animacy         string  `json:"animacy,omitempty"`
	Person          int     `json:"person,omitempty"`
	Salience        float64 `json:"salience,omitempty"`
	MentionCount    int     `json:"mentionCount,omitempty"`
	Given           bool    `json:"given,omitempty"`
	// DeterminerOpts/NumberOpts/PronounOpts keep the live alternatives so the
	// realizer can branch instead of committing here.
	DeterminerOpts []string `json:"determinerOpts,omitempty"`
	NumberOpts     []string `json:"numberOpts,omitempty"`
	PronounOpts    []string `json:"pronounOpts,omitempty"`
	// Particle is the Japanese case particle (が/は/を/に/...).
	Particle string `json:"particle,omitempty"`
	// Topic marks the phrase with は (Japanese topic, *not* subject).
	Topic bool `json:"topic,omitempty"`
	// Omit records the Japanese decision to write nothing for this argument.
	Omit             bool   `json:"omit,omitempty"`
	OmitWhy          string `json:"omitWhy,omitempty"`
	NumberDetermined bool   `json:"numberDetermined,omitempty"`
	// IsClause marks a phrase whose referent is an embedded event rather than
	// an entity (the controlled predicate of INTEND, the complement of KNOW).
	IsClause bool `json:"isClause,omitempty"`
	// Clause is the embedded event plan when IsClause.
	Clause *EventPlan `json:"clause,omitempty"`
}

// Note appends a diagnostic line to the noun phrase plan.
func (n *NPPlan) Note(msg string) {
	if n == nil {
		return
	}
	n.Notes = append(n.Notes, msg)
}

// Head returns the noun phrase without determiner or pronoun, which is what
// article choice and Japanese particle attachment need.
func (n *NPPlan) Head() string {
	if n == nil {
		return ""
	}
	if n.Noun != "" {
		return n.Noun
	}
	return n.Pronoun
}

// Omitted reports whether the phrase is realized as pure absence.
func (n *NPPlan) Omitted() bool { return n == nil || n.IsZero || n.Omit }

// --- entry points --------------------------------------------------------

// Project computes what the target language demands for one JLIR graph. It
// never collapses ambiguity it cannot justify: unresolved choices survive as
// alternatives inside the plan and as LossHints, and every stage writes a span.
func Project(r Request) *Projection {
	rec := r.recorder()
	span := rec.Open(trace.StageProjection, "target projection "+string(r.Source)+" -> "+string(r.Target))
	defer span.Close()

	p := &Projection{Source: r.Source, Target: r.Target, Style: r.Style.Normalized(), lex: r.Lexicalize}
	g := r.JLIR
	if g != nil {
		p.SourceRegister = g.Prag.SpeechStyle
		p.SourceFormality = g.Prag.Formality
	}
	if r.Style == (StyleProfile{}) {
		// plan.md §48: with no profile supplied, infer politeness from the
		// source register instead of defaulting silently.
		p.Style.Politeness = clamp01(g_Prag_Politeness(g) + 0.2)
		p.Style.Register = RegisterNeutral
		p.StyleInferred = true
		p.Notes = append(p.Notes, "target style inferred from the source register; no StyleProfile was supplied")
	}

	if g == nil {
		p.Notes = append(p.Notes, "no JLIR graph supplied: projection is empty")
		span.Note("empty projection: no JLIR graph supplied")
		span.Status(trace.StatusWarn)
		span.Data(p)
		return p
	}
	if !r.Target.Valid() {
		p.Notes = append(p.Notes, "unsupported target language "+string(r.Target))
		span.Note("unsupported target language %q", r.Target)
		span.Status(trace.StatusError)
		span.Data(p)
		return p
	}
	if r.Resolve == nil {
		p.Notes = append(p.Notes, "no decision oracle configured: construction, pronoun and case choices fall back to deterministic priors")
		span.Note("no oracle: deterministic priors only")
	}

	// Document level decisions that apply to every clause.
	p.Honorific = r.Style.Normalized().Politeness >= 0.65 || g_Prag_Respect(g) > 0.5 || g.SourceFeat.Honorific
	p.SentenceFinal = jpSentenceFinal(r, g)
	if r.Target == lang.JA {
		p.Style.Politeness = p.Style.Normalized().Politeness
	}

	switch r.Target {
	case lang.EN:
		projectEN(r, p)
	case lang.JA:
		projectJP(r, p)
	}

	span.Count("events", len(p.Events))
	span.Count("lossHints", len(p.LossHints))
	span.Count("decisions", len(p.Decisions))
	span.Data(p)
	return p
}

// Candidates is the convenience entry point used by the HTTP layer and the
// reranker: project and realize in one call.
func Candidates(r Request, p *Projection, limit int) []forest.Candidate {
	if p == nil {
		p = Project(r)
	}
	return Realize(r, p).Candidates(limit)
}

// --- shared decision helpers ---------------------------------------------

func (r Request) recorder() *trace.Recorder {
	if rec := trace.From(r.Ctx); rec != nil {
		return rec
	}
	// No ambient recorder: keep a private one so the stage still runs the same
	// code path (and cannot panic on a nil span).
	return trace.New("plan")
}

// decide asks the oracle when one is wired up and otherwise takes the prior's
// argmax. The chosen option, its probability and its provenance are recorded in
// the projection, which is what makes the decision auditable (plan.md §60).
// The second result reports whether there was anything to decide at all.
func (r Request) decide(stage, slot, question string, weights map[string]float64, p *Projection) (string, bool) {
	if len(weights) == 0 {
		return "", false
	}
	prior := jlir.NewDistribution("prior", weights)
	opts := append([]string(nil), prior.Options...)
	rank := prior.Top(1)
	chosen := ""
	conf := 0.0
	if len(rank) > 0 {
		chosen, conf = rank[0].Option, rank[0].P
	}
	rec := DecisionRecord{Stage: stage, Slot: slot, Question: question, Options: opts,
		Chosen: chosen, Probability: conf, Source: "prior"}
	if r.Resolve != nil {
		pick, prob, id := r.Resolve(stage, slot, opts, *prior)
		if pick != "" {
			if _, ok := prior.Prob[pick]; ok {
				rec.Chosen, rec.Probability = pick, prob
				if id == "" {
					rec.Source = "jev"
				} else {
					rec.DecisionID, rec.Source = id, "jev"
				}
				if prob <= 0 {
					rec.Probability = prior.P(pick)
				}
			} else {
				// The oracle named an option the candidate set does not
				// contain: refuse it rather than inventing material.
				rec.Source = "prior"
				if p != nil {
					p.Notes = append(p.Notes, "decision for "+slot+" answered with an option outside the candidate set; prior kept")
				}
			}
		}
	}
	if p != nil {
		p.Decisions = append(p.Decisions, rec)
	}
	return rec.Chosen, true
}

// loss files a loss hint on the projection.
func loss(p *Projection, dimension, feature string, weight float64, ev jlir.ID, ent jlir.ID, format string, args ...any) {
	if p == nil {
		return
	}
	p.LossHints = append(p.LossHints, LossHint{
		Feature: feature, Dimension: dimension, Weight: weight,
		Event: ev, Entity: ent, Reason: sprintf(format, args...),
	})
}

// Loss aggregates the hints into the loss vector of plan.md §43. The planner
// only knows the *projection* half of the loss; the verifier owns the rest.
func (p *Projection) Loss() LossVector {
	var v LossVector
	if p == nil {
		return v
	}
	for _, h := range p.LossHints {
		switch h.Dimension {
		case DimPropositional:
			v.Propositional += h.Weight
		case DimReferential:
			v.Referential += h.Weight
		case DimTemporal:
			v.Temporal += h.Weight
		case DimPragmatic:
			v.Pragmatic += h.Weight
		case DimStylistic:
			v.Stylistic += h.Weight
		case DimImplicature:
			v.Implicature += h.Weight
		}
	}
	return v
}

// genderPolicy is the constraint-propagation hook of plan.md §39. It is built
// once per projection and *queried while the forest is being built*, so "he"
// and "she" are never even generated when the source does not license them.
// The realizer calls check, which returns the rule that would kill the branch,
// so the rejection ledger can explain itself.
type genderPolicy struct {
	invented map[string]bool
}

// newGenderPolicy snapshots which entities the graph itself already flags as
// carrying a target-side-only gender.
func newGenderPolicy(g *jlir.Graph) genderPolicy {
	inv := map[string]bool{}
	if g != nil {
		for _, id := range g.InventedGender() {
			inv[id] = true
		}
	}
	return genderPolicy{invented: inv}
}

// supported returns the gender of e when it has upstream provenance, and ""
// otherwise. An entity the graph already flagged as invented is never trusted.
func (gp genderPolicy) supported(e *jlir.Entity) string {
	if e == nil {
		return ""
	}
	if gp.invented[string(e.ID)] {
		return ""
	}
	if f, ok := e.Feature("gender"); ok {
		for _, p := range f.Prov {
			if p.Upstream() {
				return normalizeGender(jlir.ValueString(f.Value))
			}
		}
		return ""
	}
	if g := normalizeGender(e.Gender); g != jlir.GenderUnknown && g != "" {
		for _, p := range e.Prov {
			if p.Upstream() {
				return g
			}
		}
	}
	return ""
}

// normalizeGender maps the several spellings of a gender value onto the three
// jlir constants, treating anything else as unknown.
func normalizeGender(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case jlir.GenderMale, "M", "MASC", "MASCULINE":
		return jlir.GenderMale
	case jlir.GenderFemale, "F", "FEM", "FEMININE":
		return jlir.GenderFemale
	case jlir.GenderNeuter, "N", "NEUT", "IT":
		return jlir.GenderNeuter
	}
	return jlir.GenderUnknown
}

// isPronounGendered reports whether a pronoun asserts gender.
func isPronounGendered(pron string) bool {
	switch strings.ToLower(pron) {
	case "he", "him", "his", "she", "her", "hers":
		return true
	}
	return false
}

// check is the hard constraint itself. It is exported through the package's
// realize step so that the rule appears in the rejection ledger rather than
// being buried in a branch that silently never fires.
func (gp genderPolicy) check(e *jlir.Entity, pron string) (ok bool, rule, reason string) {
	if pron == "" || !isPronounGendered(pron) {
		return true, "", ""
	}
	if g := gp.supported(e); g != "" {
		want := "she"
		if g == jlir.GenderMale {
			want = "he"
		}
		if strings.EqualFold(pron, want) {
			return true, "", ""
		}
		return false, HardInventedGender, "pronoun " + pron + " contradicts the sourced gender " + g + " of " + entityKey(e)
	}
	return false, HardInventedGender, "pronoun " + pron + " asserts a gender that has no upstream provenance for " + entityKey(e)
}

// pronouns returns the pronoun alternatives licensed for e in English order.
// An unsupported gender yields "they" rather than a coin flip between he and
// she (plan.md §4), which is what keeps a bare 帰った from becoming a specific
// gender on the way out.
func (gp genderPolicy) pronouns(e *jlir.Entity) []string {
	if e == nil {
		return nil
	}
	switch gp.supported(e) {
	case jlir.GenderMale:
		return []string{"he"}
	case jlir.GenderFemale:
		return []string{"she"}
	case jlir.GenderNeuter:
		return []string{"it"}
	}
	if e.Person == 1 {
		return []string{"I"}
	}
	if e.Person == 2 {
		return []string{"you"}
	}
	switch e.Type {
	case jlir.TypeHuman, jlir.TypePerson, jlir.TypeAnimal:
		return []string{"they"}
	case jlir.TypeOrganization:
		return []string{"they", "it"}
	case jlir.TypeArtifact, jlir.TypePlace, jlir.TypeFood, jlir.TypeAbstract, jlir.TypeTime, jlir.TypeEventLike:
		return []string{"it"}
	}
	return nil
}

// numberOf resolves the number English is forced to commit to. The second
// result is false when the source does not license a number at all, which the
// caller records as an UNDERDETERMINED condition (plan.md §46) instead of
// silently choosing.
func numberOf(e *jlir.Entity, l lang.Lang) (string, bool) {
	if e == nil {
		return jlir.NumberUnknown, false
	}
	switch e.Number {
	case jlir.NumberSingular, jlir.NumberPlural, jlir.NumberMass:
		return e.Number, true
	}
	if e.Plural {
		return jlir.NumberPlural, true
	}
	if e.Proper || hasNameAlias(e, l) {
		// A proper name is a licensed singular: the surface itself identifies a
		// single individual. That is not an invented number.
		return jlir.NumberSingular, true
	}
	if f, ok := e.Feature("number"); ok {
		for _, p := range f.Prov {
			if p.Upstream() {
				switch strings.ToUpper(jlir.ValueString(f.Value)) {
				case jlir.NumberSingular, jlir.NumberPlural, jlir.NumberMass:
					return strings.ToUpper(jlir.ValueString(f.Value)), true
				}
			}
		}
	}
	return jlir.NumberUnknown, false
}

// hasNameAlias reports whether the entity has a proper-name style surface in
// the target language. The language matters: an entity can be a proper noun in
// Japanese (太郎) and a common noun in translation ("the person who gave the
// book"), and only the target-side name licenses a bare English noun phrase.
func hasNameAlias(e *jlir.Entity, l lang.Lang) bool {
	if e == nil {
		return false
	}
	for _, a := range e.Aliases {
		if a.Kind != "name" {
			continue
		}
		if l == "" || a.Lang == l || a.Lang == "" {
			return true
		}
	}
	return false
}

// aliasFor returns the surface form the graph knows in l, preferring a name
// style alias. An empty result is meaningful: the source side has no target
// alias and the planner must report the lexical gap instead of borrowing the
// source surface (which would surface Japanese script inside an English
// sentence).
func aliasFor(g *jlir.Graph, e *jlir.Entity, l lang.Lang) string {
	if e == nil || l == "" {
		return ""
	}
	if n := e.Name(l); n != "" && surfaceFits(n, l) {
		return n
	}
	for _, a := range e.Aliases {
		if a.Lang != l && a.Lang != "" {
			continue
		}
		if a.Surface == "" || a.Kind == "zero" {
			continue
		}
		if surfaceFits(a.Surface, l) {
			return a.Surface
		}
	}
	return ""
}

// surfaceFits rejects a surface form whose script cannot belong in l. An
// English sentence may never contain kanji, and a Japanese sentence should not
// silently inherit an English one: in both cases the honest answer is a
// recorded lexical gap, which the planner files, rather than a borrowed string.
func surfaceFits(s string, l lang.Lang) bool {
	if s == "" {
		return false
	}
	hasJP, hasLatin := false, false
	for _, r := range s {
		switch {
		case r >= 0x3040 && r <= 0x30FF, r >= 0x4E00 && r <= 0x9FFF, r == 0x3005:
			hasJP = true
		case r < 0x80:
			hasLatin = true
		}
	}
	if l == lang.EN {
		return !hasJP
	}
	// Japanese: a Latin-only surface is legal for loanwords, so it is accepted,
	// but a mixed script with kana is preferred. Nothing is rejected here.
	return hasJP || hasLatin
}

// nounFor returns the head noun for the target, filing a loss hint when the
// graph carries no target-language surface for the referent.
func nounFor(g *jlir.Graph, e *jlir.Entity, l lang.Lang, p *Projection, ev jlir.ID) string {
	if s := aliasFor(g, e, l); s != "" {
		return s
	}
	// The graph only knows the source surface. Translating it is a lexical
	// lookup, not a planner decision, so the caller's resolver supplies it.
	if s := lexicalize(p, g, e, l); s != "" {
		return s
	}
	if e != nil && l == lang.EN && e.Proper {
		loss(p, DimReferential, "lexical_gap", 0.5, ev, idOf(e),
			"no %s surface form for %s", l, entityKey(e))
	}
	return ""
}

// lexicalize asks the caller's resolver for the target surface of a referent,
// trying the source-language forms the graph actually knows. A resolver that
// declines yields "", which the callers turn into a visible lexical gap.
func lexicalize(p *Projection, g *jlir.Graph, e *jlir.Entity, l lang.Lang) string {
	if p == nil || p.lex == nil || e == nil || g == nil || !l.Valid() {
		return ""
	}
	src := g.Lang
	if !src.Valid() || src == l {
		return ""
	}
	for _, surf := range sourceForms(e, src) {
		if surf == "" {
			continue
		}
		if v, ok := p.lex(surf, src, l, e.Type, e.Proper); ok && v != "" {
			return v
		}
	}
	return ""
}

// sourceForms lists the surfaces an entity is known by in the source language,
// longest first, so a full name is tried before a bare given name.
func sourceForms(e *jlir.Entity, src lang.Lang) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, a := range e.Aliases {
		if a.Lang == src || a.Lang == "" {
			add(a.Surface)
		}
	}
	if e.Proper {
		for _, a := range e.Aliases {
			if a.Lang == src || a.Lang == "" {
				add(a.Surface)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// entityKey renders an entity for diagnostics.
func entityKey(e *jlir.Entity) string {
	if e == nil {
		return "<nil>"
	}
	if k := e.Identity; k != "" {
		return string(e.ID) + "(" + k + ")"
	}
	return string(e.ID)
}

func idOf(e *jlir.Entity) jlir.ID {
	if e == nil {
		return ""
	}
	return e.ID
}

// eventOrder returns the events of g in discourse order, defensively: any
// event missing from ClauseOrder is appended in declaration order so a
// hand-built graph still realizes completely.
func eventOrder(g *jlir.Graph) []*jlir.Event {
	if g == nil {
		return nil
	}
	var out []*jlir.Event
	seen := map[jlir.ID]bool{}
	for _, id := range g.ClauseOrder {
		if seen[id] {
			continue
		}
		if ev := g.Event(id); ev != nil {
			seen[id] = true
			out = append(out, ev)
		}
	}
	for _, ev := range g.Events {
		if ev == nil || seen[ev.ID] {
			continue
		}
		seen[ev.ID] = true
		out = append(out, ev)
	}
	return out
}

// baseEventPlan copies the language neutral part of an event into a plan and
// normalizes the values every target depends on. Tense, polarity and modality
// are never guessed: an empty value is reported as a loss.
func baseEventPlan(g *jlir.Graph, ev *jlir.Event, r Request, p *Projection) *EventPlan {
	ep := &EventPlan{
		EventID:     ev.ID,
		SenseID:     ev.Predicate,
		SenseFamily: familyOf(ev.Predicate),
		Args:        map[string]*NPPlan{},
		Politeness:  r.Style.Normalized().Politeness,
		Register:    r.Style.Normalized().Register,
		Honorific:   p != nil && p.Honorific,
	}
	ep.Tense = orUnknown(ev.Tense, jlir.TenseUnknown)
	ep.Aspect = ev.Aspect
	ep.Completion = ev.Completion
	ep.Polarity = orUnknown(ev.Polarity, jlir.PolarityPositive)
	ep.Modality = ev.Modality
	ep.Mood = ev.Mood

	if ep.Tense == jlir.TenseUnknown {
		loss(p, DimTemporal, "tense", 0.35, ev.ID, "", "source does not determine the tense of %s; target picks its prior", ev.ID)
		ep.Note("tense undetermined by the source")
	}
	if ev.Predicate == "" {
		loss(p, DimPropositional, "predicate", 0.6, ev.ID, "", "event %s carries no predicate sense", ev.ID)
		ep.Note("event has no predicate sense")
	}
	if ev.Mood == jlir.MoodImperative && r.Target == lang.EN {
		// An imperative has no overt subject in English either.
		ep.Note("imperative mood: the subject is licensed to be absent")
	}
	return ep
}

// familyOf returns the semantic family of an ontology sense id. Construction
// lookup and the verb lexicons are keyed by family, so "TRANSFER.01" and
// "TRANSFER.07" share a frame family without sharing a sense.
func familyOf(sense string) string {
	if i := strings.IndexByte(sense, '.'); i > 0 {
		return sense[:i]
	}
	if sense != "" {
		return sense
	}
	return "UNKNOWN"
}

// embeddedEvent reports whether an argument binds an event rather than an
// entity, i.e. a controlled predicate or a clausal complement.
func embeddedEvent(g *jlir.Graph, id jlir.ID) *jlir.Event {
	if g == nil || id == "" {
		return nil
	}
	return g.Event(id)
}

// given reports whether the entity is already established in the discourse
// (info.Given or mention history). Determiners and Japanese omission both hang
// off this.
func given(g *jlir.Graph, e *jlir.Entity) bool {
	if g == nil || e == nil {
		return false
	}
	for _, id := range g.Info.Given {
		if id == e.ID {
			return true
		}
	}
	for _, id := range g.Info.Topic {
		if id == e.ID {
			return true
		}
	}
	// MentionCount counts mentions including the one in this very sentence, so
	// a single mention is a first mention and therefore new, not given.
	// Japanese omits what the discourse has already established; treating a
	// first mention as established silently deletes every argument of the first
	// clause.
	return e.MentionCount > 1
}

// focus reports whether the entity is contrastive, which forbids omission and
// pushes Japanese towards が.
func focus(g *jlir.Graph, e *jlir.Entity) bool {
	if g == nil || e == nil {
		return false
	}
	for _, id := range g.Info.Contrast {
		if id == e.ID {
			return true
		}
	}
	return false
}

func orUnknown(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

func g_Prag_Politeness(g *jlir.Graph) float64 {
	if g == nil {
		return 0
	}
	return g.Prag.Politeness
}

func g_Prag_Respect(g *jlir.Graph) float64 {
	if g == nil {
		return 0
	}
	return g.Prag.Respect
}

// sortedKeys returns map keys in a deterministic order (house rule 4).
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sprintf keeps formatting in one place so every diagnostic line in the
// package reads the same way in the trace.
func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
