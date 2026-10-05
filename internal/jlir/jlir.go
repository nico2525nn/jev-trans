// Package jlir defines JLIR, the Jev Lossless Interlingual Representation.
//
// JLIR is deliberately not a language independent interlingua in the strong
// sense. plan.md §7 requires nine layers, and every one of them carries
// weight in some decision the pipeline makes:
//
//	1 Core semantics        Entity / Event / Role
//	2 Referential state     number, gender, salience, mention history
//	3 Temporal/event state  tense, aspect, polarity, modality, mood
//	4 Discourse state       event history, timeline, topic/focus stacks
//	5 Information structure topic / focus / contrast / given / new
//	6 Pragmatics            register, politeness, respect, attitude
//	7 Linguistic features   source-specific morphology and constructions
//	8 Uncertainty           distributions over unresolved choices
//	9 Provenance            every material feature traces to its origin
//
// Two invariants govern the whole package:
//
//   - Nothing is invented. A feature exists only with provenance pointing at a
//     token, a discourse fact, an oracle decision, or an ontology default.
//     Absence of provenance is itself detectable (UnsupportedFeature) and is
//     how the hallucination check in plan.md §22 is implemented.
//
//   - Nothing collapses early. Unresolved choices are carried as explicit
//     Distributions, never as a silently guessed single value.
package jlir

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/lang"
)

// ID names a node inside a Graph: "e1" (entity), "v1" (event), "s1" (scope),
// "p1" (predicate instantiation), "z1" (zero anaphora).
type ID string

// Origin classifies where a piece of information came from. The
// hallucination verifier treats OriginTarget/OriginRealization as requiring an
// upstream source path.
type Origin string

const (
	OriginLexical       Origin = "lexical"       // the source token itself
	OriginMorphological Origin = "morphological" // inflectional analysis
	OriginSyntactic     Origin = "syntactic"     // parse / attachment
	OriginScope         Origin = "scope"         // scope construction
	OriginDiscourse     Origin = "discourse_inference"
	OriginDecision      Origin = "jev_decision"
	OriginOntology      Origin = "ontology_default"
	OriginTarget        Origin = "target_projection"
	OriginRealization   Origin = "target_realization"
	OriginUser          Origin = "user_supplied"
	OriginPrior         Origin = "heuristic_prior"
)

// Provenance answers "where did this information come from", which plan.md §21
// and §60 require for every material feature.
type Provenance struct {
	Token      string  `json:"token,omitempty"`  // source surface form or span text
	Span       *Span   `json:"span,omitempty"`   // byte offsets in the source text
	EntityID   ID      `json:"entity,omitempty"` // discourse entity this came from
	Origin     Origin  `json:"origin"`
	Confidence float64 `json:"confidence"`
	DecisionID string  `json:"decision,omitempty"` // e.g. "JEV-1521"
	Note       string  `json:"note,omitempty"`
}

// Span is a half-open byte range in the source text.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Text returns the substring covered by the span, or "" if out of range.
func (s Span) Text(src string) string {
	if s.Start < 0 || s.End > len(src) || s.Start >= s.End {
		return ""
	}
	return src[s.Start:s.End]
}

// Feature is an attributed attribute. The same key may appear several times
// with different provenance; Supported reports whether at least one of them is
// traceable, and SupportScore how strongly.
type Feature struct {
	Key        string       `json:"key"`
	Value      any          `json:"value"`
	Confidence float64      `json:"confidence"`
	Prov       []Provenance `json:"provenance,omitempty"`
}

// Span returns the key/value pair as a short human readable string.
func (f Feature) Span() string { return f.Key + "=" + ValueString(f.Value) }

// ValueString renders a feature value for logs and the UI.
func ValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", t), "0"), ".")
	case int:
		return fmt.Sprintf("%d", t)
	}
	return fmt.Sprintf("%v", v)
}

// Semantic type constants. Kept coarse on purpose (plan.md §11): predicates
// carry fine distinctions as features, entity types stay broad so that the
// ontology does not degenerate into a giant sense dictionary.
const (
	TypeHuman        = "HUMAN"
	TypePerson       = "PERSON"
	TypeAnimal       = "ANIMAL"
	TypeOrganization = "ORGANIZATION"
	TypePlace        = "PLACE"
	TypeArtifact     = "ARTIFACT"
	TypeFood         = "FOOD"
	TypeAbstract     = "ABSTRACT"
	TypeEventLike    = "EVENT_LIKE"
	TypeTime         = "TIME"
	TypeUnknown      = "UNKNOWN"
)

// Feature values for the referential layer.
const (
	NumberSingular = "SINGULAR"
	NumberPlural   = "PLURAL"
	NumberMass     = "MASS"
	NumberUnknown  = "UNKNOWN"

	GenderMale    = "MALE"
	GenderFemale  = "FEMALE"
	GenderNeuter  = "NEUTER"
	GenderUnknown = "UNKNOWN"

	AnimacyAnimate   = "ANIMATE"
	AnimacyInanimate = "INANIMATE"
	AnimacyUnknown   = "UNKNOWN"
)

// Feature values for the temporal / event layer.
const (
	TensePast    = "PAST"
	TensePresent = "PRESENT"
	TenseFuture  = "FUTURE"
	TenseUnknown = "UNKNOWN"

	PolarityPositive = "POSITIVE"
	PolarityNegative = "NEGATIVE"
	PolarityUnknown  = "UNKNOWN"

	AspectProgressive = "PROGRESSIVE"
	AspectPerfect     = "PERFECT"
	AspectInceptive   = "INCEPTIVE"
	AspectMomentary   = "MOMENTARY"

	CompletionCompleted  = "COMPLETED"
	CompletionIncomplete = "INCOMPLETE"

	ModalityNone   = "NONE"
	ModalityOblig  = "OBLIGATION"
	ModalityShould = "SHOULD"
	ModalityMay    = "PERMISSION"
	ModalityMust   = "REQUIREMENT"

	MoodIndicative = "INDICATIVE"
	MoodImperative = "IMPERATIVE"

	CausationNone    = "NONE"
	CausationCause   = "CAUSATION"
	CausationPurpose = "PURPOSE"
)

// Semantic roles. Surface argument structures (ERG ARG1, Japanese case
// particles) are never the final layer; plan.md §9 requires normalization
// into these.
const (
	RoleAgent       = "agent"
	RolePatient     = "patient"
	RoleTheme       = "theme"
	RoleExperiencer = "experiencer"
	RoleRecipient   = "recipient"
	RoleGoal        = "goal"
	RoleSource      = "source"
	RoleLocation    = "location"
	RoleInstrument  = "instrument"
	RolePossessor   = "possessor"
	RoleComitative  = "comitative"
	RoleTime        = "time"
	RoleManner      = "manner"
	RoleCause       = "cause"
	RoleBeneficiary = "beneficiary"
	RoleStimulus    = "stimulus"
	RoleProduct     = "product"
)

// AllRoles is the closed role inventory the verifier and realizers share.
var AllRoles = []string{
	RoleAgent, RolePatient, RoleTheme, RoleExperiencer, RoleRecipient,
	RoleGoal, RoleSource, RoleLocation, RoleInstrument, RolePossessor,
	RoleComitative, RoleTime, RoleManner, RoleCause, RoleBeneficiary,
	RoleStimulus, RoleProduct,
}

// IsRole reports whether r is a known semantic role.
func IsRole(r string) bool {
	for _, x := range AllRoles {
		if x == r {
			return true
		}
	}
	return false
}

// Alias is one surface form an entity has been referred to by.
type Alias struct {
	Surface string    `json:"surface"`
	Lang    lang.Lang `json:"lang"`
	Span    *Span     `json:"span,omitempty"`
	Kind    string    `json:"kind"` // name | title | alias | pronoun | zero | translation
}

// Entity is any referent: a person, an object, an event-as-entity, an
// abstraction. Zero anaphora is modelled as an Entity with an unresolved
// Referent distribution rather than as a missing field, per plan.md §15.
type Entity struct {
	ID       ID     `json:"id"`
	Type     string `json:"type"`
	Identity string `json:"identity"` // stable canonical key, e.g. "yamada"
	Proper   bool   `json:"proper"`

	Number  string `json:"number"`
	Gender  string `json:"gender"`
	Animacy string `json:"animacy"`
	Person  int    `json:"person"` // 1 first, 2 second, 3 third, 0 unknown
	Plural  bool   `json:"plural"`

	Aliases         []Alias `json:"aliases,omitempty"`
	MentionCount    int     `json:"mentionCount"`
	Salience        float64 `json:"salience"`
	SpeakerRelation string  `json:"speakerRelation,omitempty"`

	// Referent is set when this entity is a zero/anaphoric placeholder. The
	// options are candidate entity IDs and "UNKNOWN".
	Referent *Distribution `json:"referent,omitempty"`

	Features []Feature    `json:"features,omitempty"`
	Prov     []Provenance `json:"provenance,omitempty"`
	// External marks entities introduced by the discourse store rather than by
	// the current sentence (the speaker, previously mentioned people).
	External bool `json:"external,omitempty"`
	// Zero marks a placeholder with no overt surface realization.
	Zero bool `json:"zero,omitempty"`
}

// Arg is a filled semantic role.
type Arg struct {
	Value      ID           `json:"value"` // entity ID or event ID
	Confidence float64      `json:"confidence"`
	Prov       []Provenance `json:"provenance,omitempty"`
}

// Event is an event node with its full temporal/modal state. plan.md §12
// explicitly refuses to collapse てしまう into "EAT + PAST": completion and
// speaker attitude are separate features.
type Event struct {
	ID        ID             `json:"id"`
	Predicate string         `json:"predicate"` // ontology sense id, e.g. "TRANSFER.01"
	Args      map[string]Arg `json:"args"`

	Tense         string `json:"tense"`
	Aspect        string `json:"aspect,omitempty"`
	Completion    string `json:"completion,omitempty"`
	Polarity      string `json:"polarity"`
	Modality      string `json:"modality,omitempty"`
	Mood          string `json:"mood,omitempty"`
	Causation     string `json:"causation,omitempty"`
	Evidentiality string `json:"evidentiality,omitempty"`
	// TemporalRelation links this event to another ("BEFORE:E18").
	TemporalRelation string `json:"temporalRelation,omitempty"`

	Features []Feature    `json:"features,omitempty"`
	Prov     []Provenance `json:"provenance,omitempty"`
	// Fixed marks events whose content the pipeline has already committed to;
	// incremental re-analysis must not collapse them.
	Fixed bool `json:"fixed,omitempty"`
}

// Arg returns the entity/event bound to role r.
func (e *Event) Arg(r string) (Arg, bool) {
	a, ok := e.Args[r]
	return a, ok
}

// HasRole reports whether role r is filled.
func (e *Event) HasRole(r string) bool { _, ok := e.Args[r]; return ok }

// ScopeReading is one way of ordering a set of operators.
type ScopeReading struct {
	Order  []string     `json:"order"` // outermost first, node IDs
	Label  string       `json:"label"` // "NOT > ALL"
	Weight float64      `json:"weight"`
	Prov   []Provenance `json:"provenance,omitempty"`
}

// ScopeNode is one operator in the scope graph. Scope is a graph, never a
// string, because plan.md §13 requires the NOT/ALL ambiguity to survive
// analysis as two readings.
type ScopeNode struct {
	ID       ID       `json:"id"`
	Kind     string   `json:"kind"` // NOT, ALL, EXISTS, NONE, PROG, HAVE, COND, BECAUSE, PRESUPPOSITION
	Operands []string `json:"operands"`
	// Readings is non-empty when the scope is genuinely ambiguous; the first
	// entry is the active one only when Resolved is true.
	Readings []ScopeReading `json:"readings,omitempty"`
	Resolved bool           `json:"resolved"`
	Prov     []Provenance   `json:"provenance,omitempty"`
}

// Scope operator kinds.
const (
	ScopeNot            = "NOT"
	ScopeAll            = "ALL"
	ScopeSome           = "SOME"
	ScopeNone           = "NONE"
	ScopeProg           = "PROG"
	ScopeHave           = "HAVE"
	ScopeCond           = "COND"
	ScopeBecause        = "BECAUSE"
	ScopePresupposition = "PRESUPPOSITION"
	ScopeFact           = "FACT"
)

// InfoStruct is the information structure layer. Topic is emphatically not
// subject (plan.md §19).
type InfoStruct struct {
	Topic      []ID         `json:"topic,omitempty"`
	Focus      []ID         `json:"focus,omitempty"`
	Contrast   []ID         `json:"contrast,omitempty"`
	Given      []ID         `json:"given,omitempty"`
	New        []ID         `json:"new,omitempty"`
	Background []ID         `json:"background,omitempty"`
	Marker     string       `json:"marker,omitempty"` // は / ga / focus particle
	Prov       []Provenance `json:"provenance,omitempty"`
}

// Pragmatic layer (plan.md §18).
type Pragmatics struct {
	SpeechStyle           string  `json:"speechStyle"`
	Formality             float64 `json:"formality"`
	Politeness            float64 `json:"politeness"`
	SocialDistance        float64 `json:"socialDistance"`
	Respect               float64 `json:"respect"`
	Humility              float64 `json:"humility"`
	Empathy               float64 `json:"empathy"`
	Assertiveness         float64 `json:"assertiveness"`
	Certainty             float64 `json:"certainty"`
	EmotionalTone         string  `json:"emotionalTone,omitempty"`
	SentenceFinalAttitude string  `json:"sentenceFinalAttitude,omitempty"`
	// RespectAddressee marks honorific construction directed at a referent,
	// which English cannot encode on the surface (plan.md §5).
	RespectAddressee       string       `json:"respectAddressee,omitempty"`
	Humidification         bool         `json:"humidification,omitempty"`
	IronySuspected         bool         `json:"ironySuspected,omitempty"`
	SentenceFinalParticles []string     `json:"sentenceFinalParticles,omitempty"`
	Prov                   []Provenance `json:"provenance,omitempty"`
}

// IdiomHypothesis is one reading of a multiword expression, kept unresolved
// until the target decides (plan.md §49).
type IdiomHypothesis struct {
	ID             string       `json:"id"`
	Surface        string       `json:"surface"`
	Reading        string       `json:"reading"` // ontology predicate for the idiomatic sense
	LiteralReading string       `json:"literalReading,omitempty"`
	Span           *Span        `json:"span,omitempty"`
	Confidence     float64      `json:"confidence"`
	Prov           []Provenance `json:"provenance,omitempty"`
}

// MetaphorHypothesis keeps the surface and abstract readings side by side
// (plan.md §50).
type MetaphorHypothesis struct {
	Surface    string  `json:"surface"`
	Target     string  `json:"target"`
	Abstract   string  `json:"abstract"`
	Confidence float64 `json:"confidence"`
}

// UnknownUnit records an expression that survived every decomposition attempt
// (plan.md §25). Being honest about this is preferred to inventing meaning.
type UnknownUnit struct {
	Surface string `json:"surface"`
	Span    *Span  `json:"span,omitempty"`
	Reason  string `json:"reason"`
}

// SourceFeatures is layer 7: language specific material a fully neutral IR
// would drop, but which the projection and the loss accounting need.
type SourceFeatures struct {
	Lang                   lang.Lang            `json:"lang"`
	TopicMarker            bool                 `json:"topicMarker,omitempty"`
	SentenceFinalParticles []string             `json:"sentenceFinalParticles,omitempty"`
	ExplicitZeroSubject    bool                 `json:"explicitZeroSubject,omitempty"`
	Honorific              bool                 `json:"honorific,omitempty"`
	HonorificLevel         string               `json:"honorificLevel,omitempty"`
	Humidification         bool                 `json:"humidification,omitempty"`
	ConditionalMarker      string               `json:"conditionalMarker,omitempty"`
	TenseMorpheme          string               `json:"tenseMorpheme,omitempty"`
	AspectMorpheme         string               `json:"aspectMorpheme,omitempty"`
	PolitenessMorpheme     string               `json:"politenessMorpheme,omitempty"`
	CaseMarkers            map[string]string    `json:"caseMarkers,omitempty"`
	Idioms                 []IdiomHypothesis    `json:"idioms,omitempty"`
	Metaphors              []MetaphorHypothesis `json:"metaphors,omitempty"`
	Unknowns               []UnknownUnit        `json:"unknowns,omitempty"`
	Constructions          []string             `json:"constructions,omitempty"`
}

// Predicate is an ontology predicate instantiation carried into the graph so
// the realization stage does not have to re-resolve it and the UI can show the
// full sense definition.
type Predicate struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Gloss         string            `json:"gloss"`
	Args          []RoleSpec        `json:"args"`
	Features      map[string]string `json:"features,omitempty"`
	SubPredicates []string          `json:"subPredicates,omitempty"`
}

// RoleSpec declares a role of a predicate, including whether it is mandatory.
type RoleSpec struct {
	Role     string `json:"role"`
	Required bool   `json:"required"`
}

// SpanText maps a span id to its source substring.
type SpanText struct {
	Span
	Text string    `json:"text"`
	Lang lang.Lang `json:"lang"`
}

// Graph is one complete JLIR interpretation of one sentence in one language.
// A sentence with residual ambiguity yields several Graphs (the source
// semantic forest), never one merged soup.
type Graph struct {
	Lang   lang.Lang `json:"lang"`
	Source string    `json:"source"`
	Span   Span      `json:"span"`

	Entities []*Entity    `json:"entities"`
	Events   []*Event     `json:"events"`
	Scopes   []*ScopeNode `json:"scopes,omitempty"`
	Preds    []*Predicate `json:"predicates,omitempty"`

	Info       InfoStruct     `json:"info"`
	Prag       Pragmatics     `json:"pragmatics"`
	SourceFeat SourceFeatures `json:"sourceFeatures"`

	// Aliases maps a language specific surface form to an entity ID. This is
	// the identity mapping required by plan.md §51 (東京/Tokyo are two surface
	// forms of one entity, not two predicates).
	Aliases map[string]ID `json:"aliases,omitempty"`
	// NamedEntities is the canonical form table for those entities.
	NamedEntities map[string]NamedEntity `json:"namedEntities,omitempty"`
	// ClauseOrder lists event IDs in discourse order; the first is the matrix
	// clause of the sentence.
	ClauseOrder []ID `json:"clauseOrder,omitempty"`
	// Structural marks the graph as an intermediate artifact (source forest
	// member or constrained state) rather than a final interpretation.
	Structural bool     `json:"structural,omitempty"`
	Weight     float64  `json:"weight"`
	Notes      []string `json:"notes,omitempty"`
}

// NamedEntity carries the identity record for proper names.
type NamedEntity struct {
	Canonical       string            `json:"canonical"`
	Forms           map[string]string `json:"forms"` // lang -> form
	Reading         string            `json:"reading,omitempty"`
	Transliteration string            `json:"transliteration,omitempty"`
	Type            string            `json:"type"`
}

// ID counters -------------------------------------------------------------

type idgen struct {
	prefix string
	n      int
}

func (g *idgen) next() ID {
	g.n++
	return ID(fmt.Sprintf("%s%d", g.prefix, g.n))
}

// Builder incrementally assembles a Graph while handing out fresh IDs.
type Builder struct {
	G     *Graph
	ents  idgen
	evs   idgen
	scs   idgen
	prds  idgen
	used  map[ID]bool
	dirty bool
}

// NewBuilder starts a Graph for the given source text.
func NewBuilder(src string, l lang.Lang, span Span) *Builder {
	b := &Builder{G: &Graph{
		Lang:          l,
		Source:        src,
		Span:          span,
		Aliases:       map[string]ID{},
		NamedEntities: map[string]NamedEntity{},
	}, used: map[ID]bool{}}
	b.ents.prefix, b.evs.prefix, b.scs.prefix, b.prds.prefix = "e", "v", "s", "p"
	return b
}

// NewEntity allocates an entity ID.
func (b *Builder) NewEntity() ID {
	for {
		id := b.ents.next()
		if !b.used[id] {
			b.used[id] = true
			return id
		}
	}
}

// NewEvent allocates an event ID.
func (b *Builder) NewEvent() ID {
	for {
		id := b.evs.next()
		if !b.used[id] {
			b.used[id] = true
			return id
		}
	}
}

// NewScope allocates a scope node ID.
func (b *Builder) NewScope() ID {
	for {
		id := b.scs.next()
		if !b.used[id] {
			b.used[id] = true
			return id
		}
	}
}

// NewPredicateID allocates a predicate instantiation ID.
func (b *Builder) NewPredicateID() ID {
	for {
		id := b.prds.next()
		if !b.used[id] {
			b.used[id] = true
			return id
		}
	}
}

// AddEntity appends e and registers its aliases.
func (b *Builder) AddEntity(e *Entity) *Entity {
	b.G.Entities = append(b.G.Entities, e)
	for _, a := range e.Aliases {
		b.G.Aliases[a.Surface] = e.ID
	}
	return e
}

// AddEvent appends v.
func (b *Builder) AddEvent(v *Event) *Event {
	b.G.Events = append(b.G.Events, v)
	return v
}

// AddScope appends s.
func (b *Builder) AddScope(s *ScopeNode) *ScopeNode {
	b.G.Scopes = append(b.G.Scopes, s)
	return s
}

// AddPredicate appends p and returns its instantiation id.
func (b *Builder) AddPredicate(p *Predicate) ID {
	b.G.Preds = append(b.G.Preds, p)
	id := b.NewPredicateID()
	return id
}

// Build finalizes and returns the graph.
func (b *Builder) Build() *Graph { return b.G }

// Entity looks up an entity by ID.
func (g *Graph) Entity(id ID) *Entity {
	for _, e := range g.Entities {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// Event looks up an event by ID.
func (g *Graph) Event(id ID) *Event {
	for _, v := range g.Events {
		if v.ID == id {
			return v
		}
	}
	return nil
}

// Scope looks up a scope node by ID.
func (g *Graph) Scope(id ID) *ScopeNode {
	for _, s := range g.Scopes {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Predicate looks up a predicate instantiation by ontology sense ID.
func (g *Graph) Predicate(sense string) *Predicate {
	for _, p := range g.Preds {
		if p.ID == sense {
			return p
		}
	}
	return nil
}

// EntityByAlias resolves a surface form to an entity.
func (g *Graph) EntityByAlias(surface string) *Entity {
	id, ok := g.Aliases[surface]
	if !ok {
		return nil
	}
	return g.Entity(id)
}

// Matrix returns the first clause's event, or nil.
func (g *Graph) Matrix() *Event {
	if len(g.ClauseOrder) == 0 {
		return nil
	}
	return g.Event(g.ClauseOrder[0])
}

// NewEvent returns an Event pre-filled with the neutral defaults the pipeline
// uses so that no stage has to invent polarity/tense defaults inline.
func NewEvent(id ID, predicate string) *Event {
	return &Event{
		ID:        id,
		Predicate: predicate,
		Args:      map[string]Arg{},
		Tense:     TenseUnknown,
		Polarity:  PolarityUnknown,
		Modality:  ModalityNone,
		Causation: CausationNone,
	}
}

// NewEntityValue returns an Entity with neutral referential defaults.
func NewEntityValue(id ID, typ string) *Entity {
	return &Entity{
		ID:      id,
		Type:    typ,
		Number:  NumberUnknown,
		Gender:  GenderUnknown,
		Animacy: AnimacyUnknown,
	}
}

// SortIDs returns a stable copy of ids sorted for deterministic output, which
// matters because the UI diffs graphs between candidate readings.
func SortIDs(ids []ID) []ID {
	out := make([]ID, len(ids))
	copy(out, ids)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
