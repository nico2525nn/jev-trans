package discourse

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/trace"
)

const (
	// maxInvalidations is how much invalidation history the store keeps. The
	// list is diagnostic — the UI shows why a re-run happened — while the
	// decisions themselves are gone the moment they are invalidated.
	maxInvalidations = 64
	// maxTopics bounds the topic and focus stacks. A document has a discourse
	// memory, not an infinite one.
	maxTopics = 16
	// maxRelations bounds the social relation history for the same reason.
	maxRelations = 16
	// defaultDocument is the store used when a request carries no document id.
	defaultDocument = "default"
	// maxDocuments bounds how many document states a long running server keeps.
	// Beyond it the least recently touched document is dropped.
	maxDocuments = 256
)

// DecisionEntry is one cached decision together with the entities it depended
// on. It is what makes plan.md §57 possible: when the document graph changes,
// only the decisions whose entity list intersects the change are recomputed.
//
// The dependency is on entities, not on sentences: 「彼」 was decided between two
// candidates, so the decision depends on both, and a later 「山田さんは女性です」
// invalidates it.
type DecisionEntry struct {
	ID       string    `json:"id"`
	Stage    string    `json:"stage,omitempty"`
	Kind     string    `json:"kind,omitempty"` // zero_anaphora | referent | terminology
	Question string    `json:"question,omitempty"`
	Options  []string  `json:"options,omitempty"`
	Option   string    `json:"option,omitempty"`
	Entities []jlir.ID `json:"entities,omitempty"`
	Sentence int       `json:"sentence"`
	// Version is the store version the decision was taken under; an entry with
	// an older version is stale by construction (plan.md §56).
	Version int               `json:"version"`
	Prov    []jlir.Provenance `json:"provenance,omitempty"`
}

// Invalidation records why a decision was dropped.
type Invalidation struct {
	DecisionID string    `json:"decisionId"`
	Entities   []jlir.ID `json:"entities,omitempty"`
	// TriggeredBy lists the entities whose change caused the invalidation.
	TriggeredBy []jlir.ID `json:"triggeredBy,omitempty"`
	Version     int       `json:"version"`
	Note        string    `json:"note,omitempty"`
}

// Store is the Document Context Store of plan.md §28: entities, aliases,
// speaker and listener, social relations, event history, a timeline, the topic
// and focus stacks, terminology, the style profile, and the decisions taken so
// far.
//
// A Store must be built with NewStore; its zero value has no registry. It is
// safe for concurrent use: the HTTP layer keeps one per document id
// and several requests may touch the same document at once. The Registry and
// TermMemory it owns are not synchronized on their own; the Store's lock
// covers them.
type Store struct {
	mu    sync.RWMutex
	reg   *Registry
	terms *TermMemory

	sentences int
	version   int

	timeline   []EventView
	topicStack []string
	focusStack []string
	topicIDs   []jlir.ID
	focusIDs   []jlir.ID

	style     StyleView
	styleN    int
	speaker   jlir.ID
	listener  jlir.ID
	relations []RelationView

	decisions       map[string]*DecisionEntry
	decSeq          int
	invalidations   []Invalidation
	lastInvalidated []string

	pending     []*Anaphora
	anaphoraSeq int
}

// RelationView is one social relation the document established (plan.md §28).
type RelationView struct {
	From jlir.ID           `json:"from"`
	To   jlir.ID           `json:"to,omitempty"`
	Type string            `json:"type"`
	Text string            `json:"text,omitempty"`
	Prov []jlir.Provenance `json:"provenance,omitempty"`
}

// StyleView is the accumulated register of the document (plan.md §48). It is a
// running mean of what the sentences actually showed, never a style the
// pipeline assumed on the source's behalf.
type StyleView struct {
	SpeechStyle    string  `json:"speechStyle,omitempty"`
	Register       string  `json:"register,omitempty"`
	Formality      float64 `json:"formality"`
	Politeness     float64 `json:"politeness"`
	Respect        float64 `json:"respect"`
	Humility       float64 `json:"humility"`
	SocialDistance float64 `json:"socialDistance"`
	Empathy        float64 `json:"empathy"`
	Assertiveness  float64 `json:"assertiveness"`
	Certainty      float64 `json:"certainty"`
	// Sentences counts how many sentences contributed to the mean.
	Sentences int `json:"sentences"`
	// Source is "observed", "seeded" or "default", so the UI never presents an
	// inferred register as if the document had stated it.
	Source string `json:"source"`
}

// EventView is one clause in the document timeline.
type EventView struct {
	Sentence  int     `json:"sentence"`
	Clause    int     `json:"clause"`
	Event     jlir.ID `json:"event"`
	Predicate string  `json:"predicate,omitempty"`
	Subject   jlir.ID `json:"subject,omitempty"`
	// SubjectText is the document's current name for that referent, which is
	// what makes the timeline readable across a 山田教授 → 彼 → 山田 chain.
	SubjectText string    `json:"subjectText,omitempty"`
	Object      jlir.ID   `json:"object,omitempty"`
	Topic       []jlir.ID `json:"topic,omitempty"`
	Tense       string    `json:"tense,omitempty"`
	Polarity    string    `json:"polarity,omitempty"`
	Weight      float64   `json:"weight,omitempty"`
	Note        string    `json:"note,omitempty"`
}

// EntityView is the JSON-friendly projection of an entity for the API response.
type EntityView struct {
	ID              jlir.ID           `json:"id"`
	Type            string            `json:"type"`
	Proper          bool              `json:"proper,omitempty"`
	Canonical       string            `json:"canonical,omitempty"`
	Forms           map[string]string `json:"forms,omitempty"`
	Reading         string            `json:"reading,omitempty"`
	Transliteration string            `json:"transliteration,omitempty"`
	AliasList       []AliasView       `json:"aliases,omitempty"`
	MentionCount    int               `json:"mentionCount"`
	FirstMention    int               `json:"firstMention,omitempty"`
	LastMention     int               `json:"lastMention,omitempty"`
	Salience        float64           `json:"salience"`
	Number          string            `json:"number,omitempty"`
	Gender          string            `json:"gender,omitempty"`
	Animacy         string            `json:"animacy,omitempty"`
	Plural          bool              `json:"plural,omitempty"`
	SpeakerRelation string            `json:"speakerRelation,omitempty"`
	Features        []jlir.Feature    `json:"features,omitempty"`
	// Unsupported lists protected attributes asserted without upstream
	// provenance (plan.md §22). The UI shows them as warnings.
	Unsupported []string `json:"unsupported,omitempty"`
	MergedInto  jlir.ID  `json:"mergedInto,omitempty"`
	External    bool     `json:"external,omitempty"`
}

// View renders an entity for the API response. Sensitive attributes asserted
// without upstream provenance are reported under Unsupported instead of being
// presented as fact (plan.md §22, §60).
func (e *Entity) View() EntityView {
	if e == nil {
		return EntityView{}
	}
	v := EntityView{
		ID: e.ID, Type: e.Type, Proper: e.Proper, Canonical: e.Canonical,
		Reading: e.Reading, Transliteration: e.Transliteration,
		MentionCount: e.MentionCount, FirstMention: e.FirstMention,
		LastMention: e.LastMention, Salience: e.Salience,
		Number: e.Number, Gender: e.GenderValue(), Animacy: e.Animacy,
		Plural: e.Plural, SpeakerRelation: e.SpeakerRelation,
		MergedInto: e.MergedInto, External: e.External,
	}
	if len(e.Forms) > 0 {
		v.Forms = make(map[string]string, len(e.Forms))
		for l, f := range e.Forms {
			v.Forms[string(l)] = f
		}
	}
	for _, a := range e.Aliases {
		v.AliasList = append(v.AliasList, AliasView{
			Surface: a.Surface, Lang: string(a.Lang), Kind: a.Kind,
			Mention: a.Mention, Anaphoric: a.Anaphoric, Origin: a.Prov.Origin,
		})
	}
	sort.Slice(v.AliasList, func(i, j int) bool {
		if v.AliasList[i].Surface != v.AliasList[j].Surface {
			return v.AliasList[i].Surface < v.AliasList[j].Surface
		}
		return v.AliasList[i].Kind < v.AliasList[j].Kind
	})
	v.Features = append([]jlir.Feature(nil), e.Features...)
	sort.Slice(v.Features, func(i, j int) bool { return v.Features[i].Key < v.Features[j].Key })
	for _, f := range e.Features {
		if !jlir.IsSensitive(f.Key) {
			continue
		}
		upstream := false
		for _, p := range f.Prov {
			if p.Upstream() {
				upstream = true
				break
			}
		}
		if !upstream {
			v.Unsupported = append(v.Unsupported, f.Key+"="+jlir.ValueString(f.Value))
		}
	}
	sort.Strings(v.Unsupported)
	return v
}

// AliasView is one surface form of an entity in the API response.
type AliasView struct {
	Surface   string      `json:"surface"`
	Lang      string      `json:"lang,omitempty"`
	Kind      string      `json:"kind,omitempty"`
	Mention   int         `json:"mention,omitempty"`
	Anaphoric bool        `json:"anaphoric,omitempty"`
	Origin    jlir.Origin `json:"origin,omitempty"`
}

// State is the document state as the API response and the WebUI consume it.
// Every slice is sorted deterministically, so two identical documents render
// identically.
type State struct {
	Entities   []EntityView `json:"entities"`
	Speaker    *EntityView  `json:"speaker,omitempty"`
	Listener   *EntityView  `json:"listener,omitempty"`
	Timeline   []EventView  `json:"timeline"`
	TopicStack []string     `json:"topicStack"`
	FocusStack []string     `json:"focusStack"`
	Style      StyleView    `json:"style"`
	Terms      []TermView   `json:"terms"`
	Sentences  int          `json:"sentences"`
	Version    int          `json:"version"`

	// Relations is the social relation history (plan.md §28).
	Relations []RelationView `json:"relations,omitempty"`
	// OpenQuestions are the ambiguities worth asking the user about (plan.md §62).
	OpenQuestions []Question `json:"openQuestions,omitempty"`
	// Pending lists the zero arguments that are still unresolved.
	Pending []*Anaphora `json:"pending,omitempty"`
	// Conflicts are terminology conflicts that were reported, not applied.
	Conflicts []Conflict `json:"conflicts,omitempty"`
	// Salience is the current discourse salience of every live entity.
	Salience map[string]float64 `json:"salience,omitempty"`
	// Invalidated lists the decision ids the most recent change dropped.
	Invalidated []string `json:"invalidated,omitempty"`
}

// NewStore returns an empty document store.
func NewStore() *Store {
	return &Store{
		reg:       NewRegistry(),
		terms:     NewTermMemory(),
		decisions: map[string]*DecisionEntry{},
		style:     defaultStyle(),
	}
}

func defaultStyle() StyleView {
	return StyleView{
		Register: "neutral", Formality: 0.5, Politeness: 0.5, Respect: 0.5,
		Humility: 0.5, SocialDistance: 0.5, Empathy: 0.5, Assertiveness: 0.5,
		Certainty: 0.5, Source: "default",
	}
}

// Registry exposes the identity store for callers that want to inspect it.
func (s *Store) Registry() *Registry { return s.reg }

// TermMemory exposes the terminology memory.
func (s *Store) TermMemory() *TermMemory { return s.terms }

// Version returns the monotone document version; a cached decision taken under
// an older version is stale (plan.md §56).
func (s *Store) Version() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// Sentences returns how many sentences the store has observed.
func (s *Store) Sentences() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sentences
}

// Observe folds one sentence graph into the document state. It is the entry
// point the pipeline calls for every translated sentence.
func (s *Store) Observe(g *jlir.Graph) {
	s.ObserveUpdate(g)
}

// ObserveUpdate is Observe with the update record returned, for callers that
// want the merge list, the pronoun bindings and the feature changes without
// reading the trace.
func (s *Store) ObserveUpdate(g *jlir.Graph) *Update {
	if g == nil {
		return &Update{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sentences++
	u := s.reg.Intern(g, s.sentences)
	s.version++
	s.speaker = s.reg.Speaker()
	s.listener = s.reg.Listener()
	s.foldTimeline(g)
	s.foldTopics(g)
	s.foldStyle(g)
	s.foldRelations(u)
	for _, p := range u.Pending {
		s.recordPending(New(p.Entity, "", s.sentences, p.Candidates, p.Prior))
	}
	return u
}

// ObserveTraced is Observe with the trace span every stage owes the UI (house
// rule 3). A nil recorder in the context is tolerated.
func (s *Store) ObserveTraced(ctx context.Context, g *jlir.Graph) *Update {
	u := s.ObserveUpdate(g)
	rec := trace.From(ctx)
	if rec == nil {
		return u
	}
	span := rec.Open(trace.StageDiscourse, "document state update")
	span.Data(u)
	span.Count("sentences", s.Sentences())
	span.Count("entities", len(u.Interned))
	span.Count("merges", len(u.Merges))
	span.Count("zero_arguments", len(u.Zeros))
	span.Count("pending", len(u.Pending))
	span.Label("version", fmt.Sprint(s.Version()))
	for _, m := range u.Merges {
		span.Note("entity %s absorbs %s on %s", m.Kept, m.Absorbed, m.Evidence)
	}
	for _, c := range u.Changes {
		if c.Material {
			span.Note("%s is now %s=%s", c.Entity, c.Key, c.Value)
		}
	}
	for _, p := range u.Pending {
		span.Note("%s left unresolved over %v", p.Surface, p.Candidates)
	}
	for _, n := range u.Notes {
		span.Note("%s", n)
	}
	span.Close()
	return u
}

// foldTimeline appends this sentence's clauses to the document timeline.
func (s *Store) foldTimeline(g *jlir.Graph) {
	for i, v := range orderedEvents(g) {
		ev := EventView{
			Sentence: s.sentences, Clause: i, Event: v.ID, Predicate: v.Predicate,
			Tense: v.Tense, Polarity: v.Polarity, Weight: g.Weight,
		}
		for _, role := range []string{jlir.RoleAgent, jlir.RoleExperiencer, jlir.RoleTheme, jlir.RolePatient} {
			a, ok := v.Arg(role)
			if !ok {
				continue
			}
			id := s.reg.Canonical(a.Value)
			switch role {
			case jlir.RoleAgent, jlir.RoleExperiencer:
				ev.Subject = id
			default:
				if ev.Object == "" {
					ev.Object = id
				}
			}
			break
		}
		if e := s.reg.Get(ev.Subject); e != nil {
			ev.SubjectText = e.Name(g.Lang)
		}
		for _, t := range g.Info.Topic {
			ev.Topic = append(ev.Topic, s.reg.Canonical(t))
		}
		if v.Fixed {
			ev.Note = "committed"
		}
		s.timeline = append(s.timeline, ev)
	}
}

// foldTopics maintains the topic and focus stacks (plan.md §19, §28).
func (s *Store) foldTopics(g *jlir.Graph) {
	for _, id := range g.Info.Topic {
		if c := s.reg.Canonical(id); c != "" {
			s.pushStack(&s.topicIDs, &s.topicStack, c, g.Lang)
		}
	}
	for _, id := range g.Info.Focus {
		if c := s.reg.Canonical(id); c != "" {
			s.pushStack(&s.focusIDs, &s.focusStack, c, g.Lang)
		}
	}
}

// pushStack moves an entity to the front of a discourse stack, dropping
// duplicates and entries that no longer resolve. Most recent first is the order
// the UI expects and the order a zero argument is resolved against.
func (s *Store) pushStack(ids *[]jlir.ID, surfaces *[]string, id jlir.ID, l lang.Lang) {
	next := appendUniqueIDs([]jlir.ID{id}, *ids...)
	if len(next) > maxTopics {
		next = next[:maxTopics]
	}
	*ids = next
	labels := make([]string, 0, len(next))
	for _, x := range next {
		e := s.reg.Get(x)
		if e == nil {
			continue
		}
		label := e.Surface(l)
		if label == "" {
			label = e.Canonical
		}
		if label != "" {
			labels = append(labels, label)
		}
	}
	*surfaces = labels
}

// foldStyle folds this sentence's pragmatic profile into the running mean.
func (s *Store) foldStyle(g *jlir.Graph) {
	p := g.Prag
	if p.SpeechStyle == "" && p.Formality == 0 && p.Politeness == 0 && p.Respect == 0 &&
		p.Humility == 0 && p.SocialDistance == 0 && p.Empathy == 0 &&
		p.Assertiveness == 0 && p.Certainty == 0 {
		return
	}
	n := float64(s.styleN)
	mix := func(old, v float64) float64 {
		if v == 0 {
			return old
		}
		return (old*n + v) / (n + 1)
	}
	s.style.Formality = mix(s.style.Formality, p.Formality)
	s.style.Politeness = mix(s.style.Politeness, p.Politeness)
	s.style.Respect = mix(s.style.Respect, p.Respect)
	s.style.Humility = mix(s.style.Humility, p.Humility)
	s.style.SocialDistance = mix(s.style.SocialDistance, p.SocialDistance)
	s.style.Empathy = mix(s.style.Empathy, p.Empathy)
	s.style.Assertiveness = mix(s.style.Assertiveness, p.Assertiveness)
	s.style.Certainty = mix(s.style.Certainty, p.Certainty)
	if p.SpeechStyle != "" {
		s.style.SpeechStyle = p.SpeechStyle
	}
	s.styleN++
	s.style.Sentences = s.styleN
	s.style.Source = "observed"
	s.style.Register = registerOf(s.style)
}

func registerOf(v StyleView) string {
	switch {
	case v.Politeness >= 0.7 && v.Respect >= 0.6:
		return "polite"
	case v.Politeness <= 0.3:
		return "casual"
	case v.Formality >= 0.7:
		return "formal"
	}
	return "neutral"
}

// foldRelations records the speaker/addressee relation the sentence showed.
func (s *Store) foldRelations(u *Update) {
	if u.Speaker == "" || u.Listener == "" {
		return
	}
	s.relations = append(s.relations, RelationView{
		From: u.Speaker, To: u.Listener, Type: "addressee",
		Prov: []jlir.Provenance{{Origin: jlir.OriginDiscourse, EntityID: u.Speaker,
			Note: "speaker and addressee established for this sentence"}},
	})
	if len(s.relations) > maxRelations {
		s.relations = s.relations[len(s.relations)-maxRelations:]
	}
}

// Entities returns every live entity, sorted by id.
func (s *Store) Entities() []*Entity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reg.All()
}

// EntityIDs returns the canonical ids of every live entity.
func (s *Store) EntityIDs() []jlir.ID {
	ents := s.Entities()
	out := make([]jlir.ID, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.ID)
	}
	return out
}

// Entities2IDs is an alias kept for callers that read the store as a pool.
func (s *Store) Entities2IDs() []jlir.ID { return s.EntityIDs() }

// Entity looks up a live entity by any id the pipeline may hold.
func (s *Store) Entity(id jlir.ID) *Entity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reg.Get(s.reg.Canonical(id))
}

// Salience returns the current discourse salience of every entity.
func (s *Store) Salience() map[jlir.ID]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reg.Salience()
}

// Prior returns the discourse distribution over the candidates of a zero
// argument (plan.md §15). It resolves sentence-scoped ids, so a caller may pass
// whatever the graph handed it.
func (s *Store) Prior(candidates []jlir.ID) *jlir.Distribution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reg.Prior(candidates)
}

// All2IDs lists the live entity ids of a registry.
func (r *Registry) All2IDs() []jlir.ID {
	out := []jlir.ID{}
	for _, e := range r.All() {
		out = append(out, e.ID)
	}
	return out
}

// ResolveZero merges the discourse prior with a Jev judgement into the referent
// distribution of a zero placeholder and installs the result in the graph. This
// is plan.md §15's 「Jevが文脈を評価し、posterior distributionを更新する」 without
// this package ever importing the oracle client.
func (s *Store) ResolveZero(g *jlir.Graph, zero jlir.ID, posterior map[string]float64) *Anaphora {
	return s.resolveZero(g, zero, posterior, MinMargin)
}

func (s *Store) resolveZero(g *jlir.Graph, zero jlir.ID, posterior map[string]float64, minMargin float64) *Anaphora {
	s.mu.Lock()
	defer s.mu.Unlock()

	var ent *jlir.Entity
	if g != nil {
		ent = g.Entity(zero)
	}
	cands := Candidates(g, s.reg.All2IDs(), "")
	if ent != nil && ent.Referent != nil {
		for _, o := range ent.Referent.Options {
			if o == UnknownOption {
				cands = append(cands, UnknownOption)
				continue
			}
			cands = append(cands, s.reg.Canonical(jlir.ID(o)))
		}
	}
	cands = sortedIDs(uniqueIDs(cands))
	if len(cands) == 0 {
		return nil
	}
	a := New(zero, roleOf(g, zero), s.sentences, cands, s.reg.Prior(cands))
	if len(posterior) > 0 {
		ApplyPosterior(a, posterior)
	}
	committed := a.CommitWithMargin(minMargin)
	if ent != nil && a.Distribution != nil {
		ent.Referent = a.Distribution
		ent.Prov = append(ent.Prov, a.Prov...)
	}
	s.recordPending(a)
	if committed {
		s.pending = dropPending(s.pending, a.ZeroID)
		s.anaphoraSeq++
		s.RecordDecisionLocked(DecisionEntry{
			ID: fmt.Sprintf("Z%d", s.anaphoraSeq), Stage: "D6", Kind: "zero_anaphora",
			Question: fmt.Sprintf("referent of %s", a.ZeroID),
			Option:   string(a.CommittedTo), Sentence: a.Sentence,
			Entities: a.Candidates, Prov: a.Prov,
		})
	} else {
		// An open zero gets a stable handle so plan.md §62 can address it.
		s.anaphoraSeq++
		a.QuestionID = fmt.Sprintf("q%d", s.anaphoraSeq)
	}
	s.version++
	return a
}

func roleOf(g *jlir.Graph, zero jlir.ID) string {
	if g == nil {
		return ""
	}
	for _, v := range g.Events {
		for role, a := range v.Args {
			if a.Value == zero {
				return role
			}
		}
	}
	return ""
}

func (s *Store) recordPending(a *Anaphora) {
	if a == nil {
		return
	}
	for _, x := range s.pending {
		if x.ZeroID == a.ZeroID {
			*x = *a
			return
		}
	}
	s.pending = append(s.pending, a)
}

func dropPending(list []*Anaphora, zero jlir.ID) []*Anaphora {
	out := list[:0]
	for _, x := range list {
		if x.ZeroID != zero {
			out = append(out, x)
		}
	}
	return out
}

// PendingAnaphora returns the zero arguments that are still open.
func (s *Store) PendingAnaphora() []*Anaphora {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Anaphora, 0, len(s.pending))
	for _, a := range s.pending {
		if !a.Committed && !a.Answered {
			cp := *a
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sentence < out[j].Sentence })
	return out
}

// OpenAnaphora returns the open ambiguities worth asking about (plan.md §62).
func (s *Store) OpenAnaphora() []*Anaphora { return s.PendingAnaphora() }

// Questions builds the questions the UI may show for the open ambiguities. A
// question is only produced when it could actually change the translation.
func (s *Store) Questions(target lang.Lang) []Question {
	pending := s.PendingAnaphora()
	out := make([]Question, 0, len(pending))
	for _, a := range pending {
		if q := BuildQuestion(a.QuestionID, a, string(target)); q != nil {
			out = append(out, *q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Answer applies a user's disambiguation (plan.md §62). The answer is recorded
// as jlir.OriginUser provenance and cached against the candidate entities, so a
// later document change can invalidate it like any other decision.
func (s *Store) Answer(questionID, option string) (*Anaphora, bool) {
	if option == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.pending {
		if questionID != "" && a.QuestionID != questionID {
			continue
		}
		Answer(a, questionID, option)
		s.pending = dropPending(s.pending, a.ZeroID)
		s.anaphoraSeq++
		s.RecordDecisionLocked(DecisionEntry{
			ID: fmt.Sprintf("Z%d", s.anaphoraSeq), Stage: "D6", Kind: "zero_anaphora",
			Question: questionID, Option: option, Sentence: a.Sentence,
			Entities: a.Candidates, Prov: a.Prov,
		})
		s.version++
		cp := *a
		return &cp, true
	}
	return nil, false
}

// MergeEntities identifies two referents and returns the absorbed ids together
// with the decisions that depended on them — the whole point of plan.md §57.
func (s *Store) MergeEntities(a, b jlir.ID, ev MergeEvidence) ([]jlir.ID, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.reg.Merge(a, b, ev)
	if err != nil {
		return nil, nil, err
	}
	changed := append([]jlir.ID{res.Kept}, res.Absorbed...)
	stale := s.invalidateLocked(changed, "entity merge: "+ev.String())
	s.version++
	return res.Absorbed, stale, nil
}

// Reveal asserts an attributed fact about an entity — for example
// 「山田さんは女性です」 — and invalidates exactly the decisions that depended on
// it, so the ambiguous pronouns of the earlier sentences can be re-evaluated.
func (s *Store) Reveal(id jlir.ID, key string, value any, prov jlir.Provenance) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, changed := s.reg.SetFeature(id, key, value, prov)
	if ch.Entity == "" {
		return nil, fmt.Errorf("discourse: unknown entity %q", id)
	}
	if !changed {
		return nil, nil
	}
	note := fmt.Sprintf("%s is now %s=%s", ch.Entity, ch.Key, ch.Value)
	if ch.Prov.Origin == jlir.OriginUser {
		note += " (user supplied)"
	}
	stale := s.invalidateLocked([]jlir.ID{ch.Entity}, note)
	s.version++
	return stale, nil
}

// RevealTraced is Reveal with its trace span.
func (s *Store) RevealTraced(ctx context.Context, id jlir.ID, key string, value any, prov jlir.Provenance) ([]string, error) {
	stale, err := s.Reveal(id, key, value, prov)
	rec := trace.From(ctx)
	if rec == nil || err != nil {
		return stale, err
	}
	span := rec.Open(trace.StageDiscourse, "entity disclosure")
	span.Count("invalidated", len(stale))
	span.Label("entity", string(id))
	span.Label("feature", key)
	for _, d := range stale {
		span.Note("decision %s depended on %s and must be retaken", d, id)
	}
	span.Close()
	return stale, nil
}

// Invalidate returns the ids of the cached decisions that depended on the given
// entities and drops them. Decisions about anything else survive untouched,
// which is what makes incremental translation possible (plan.md §57).
func (s *Store) Invalidate(changed []jlir.ID) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	return s.invalidateLocked(changed, "document change")
}

// InvalidateTraced is Invalidate with its trace span.
func (s *Store) InvalidateTraced(ctx context.Context, changed []jlir.ID) []string {
	ids := s.Invalidate(changed)
	rec := trace.From(ctx)
	if rec == nil {
		return ids
	}
	span := rec.Open(trace.StageDiscourse, "decision invalidation")
	span.Data(map[string]any{"changed": changed, "invalidated": ids})
	span.Count("invalidated", len(ids))
	for _, id := range ids {
		span.Note("decision %s must be retaken", id)
	}
	span.Close()
	return ids
}

func (s *Store) invalidateLocked(changed []jlir.ID, note string) []string {
	want := map[jlir.ID]bool{}
	for _, c := range changed {
		if c == "" {
			continue
		}
		want[c] = true
		want[s.reg.Canonical(c)] = true
	}
	var ids []string
	for id, d := range s.decisions {
		hit := false
		for _, e := range d.Entities {
			if want[e] || want[s.reg.Canonical(e)] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		ids = append(ids, id)
		s.invalidations = append(s.invalidations, Invalidation{
			DecisionID: id, Entities: append([]jlir.ID(nil), d.Entities...),
			TriggeredBy: append([]jlir.ID(nil), changed...), Version: s.version, Note: note,
		})
		delete(s.decisions, id)
	}
	sort.Strings(ids)
	if len(s.invalidations) > maxInvalidations {
		s.invalidations = s.invalidations[len(s.invalidations)-maxInvalidations:]
	}
	s.lastInvalidated = ids
	return ids
}

// Invalidations returns the invalidation history.
func (s *Store) Invalidations() []Invalidation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Invalidation(nil), s.invalidations...)
}

// Invalidated returns the ids dropped by the most recent invalidation.
func (s *Store) Invalidated() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.lastInvalidated...)
}

// RecordDecision caches a decision together with the entities it depends on,
// stamping the current version so a stale entry is detectable.
func (s *Store) RecordDecision(d DecisionEntry) *DecisionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.RecordDecisionLocked(d)
}

// RecordDecisionLocked is RecordDecision with the lock already held.
func (s *Store) RecordDecisionLocked(d DecisionEntry) *DecisionEntry {
	if d.ID == "" {
		s.decSeq++
		d.ID = fmt.Sprintf("D%d", s.decSeq)
	}
	if d.Sentence == 0 {
		d.Sentence = s.sentences
	}
	ents := make([]jlir.ID, 0, len(d.Entities))
	for _, e := range d.Entities {
		if e == "" {
			continue
		}
		ents = appendUniqueIDs(ents, s.reg.Canonical(e))
	}
	sortIDs(ents)
	d.Entities = ents
	d.Options = sorted(d.Options)
	d.Version = s.version
	entry := &d
	s.decisions[d.ID] = entry
	return entry
}

// Decision returns a cached decision by id.
func (s *Store) Decision(id string) (DecisionEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.decisions[id]
	if !ok {
		return DecisionEntry{}, false
	}
	return *d, true
}

// Decisions returns every cached decision, sorted by id.
func (s *Store) Decisions() []DecisionEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DecisionEntry, 0, len(s.decisions))
	for _, d := range s.decisions {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ForgetDecision drops one decision, e.g. once the pipeline has recomputed it.
func (s *Store) ForgetDecision(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.decisions[id]; !ok {
		return false
	}
	delete(s.decisions, id)
	return true
}

// CheckTerm reports where a target form a previous sentence already chose
// disagrees with the terminology memory. Nothing is rewritten: the caller gets
// a conflict list and decides (plan.md §52).
func (s *Store) CheckTerm(source string, l lang.Lang, chosen, target lang.Lang) []Conflict {
	s.mu.Lock()
	defer s.mu.Unlock()
	conflicts := s.terms.Check(source, l, string(chosen), target)
	for i := range conflicts {
		conflicts[i].Sentence = s.sentences
	}
	s.terms.ReportConflict(conflicts)
	return conflicts
}

// CheckTermTraced is CheckTerm with its trace span, because a terminology
// conflict is something the user has to see.
func (s *Store) CheckTermTraced(ctx context.Context, source string, l lang.Lang, chosen, target lang.Lang) []Conflict {
	conflicts := s.CheckTerm(source, l, chosen, target)
	rec := trace.From(ctx)
	if rec == nil {
		return conflicts
	}
	span := rec.Open(trace.StageDiscourse, "terminology check")
	span.Data(map[string]any{"source": source, "chosen": chosen, "conflicts": conflicts})
	span.Count("conflicts", len(conflicts))
	for _, c := range conflicts {
		span.Note("terminology conflict on %q: %s chose %q, memory prefers %v",
			c.Source, c.Reason, c.Chosen, c.Preferred)
	}
	if len(conflicts) > 0 {
		span.Status(trace.StatusWarn)
	}
	span.Close()
	return conflicts
}

// LookupTerm returns the preferred and forbidden target forms of a source term.
func (s *Store) LookupTerm(source string, l lang.Lang) (*Lookup, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.terms.Lookup(source, l)
}

// DefineTerm adds or extends a terminology entry.
func (s *Store) DefineTerm(t Term) []Conflict {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, conflicts := s.terms.Define(t)
	for i := range conflicts {
		conflicts[i].Sentence = s.sentences
	}
	s.terms.ReportConflict(conflicts)
	return conflicts
}

// LoadTerms loads a terminology memory from a JSON file into the document.
func (s *Store) LoadTerms(path string) ([]Conflict, error) {
	m, err := Load(path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conflicts := m.Conflicts()
	for _, t := range m.Terms() {
		_, c := s.terms.Define(*t)
		conflicts = append(conflicts, c...)
	}
	s.terms.ReportConflict(conflicts)
	return conflicts, nil
}

// Reset clears the document state. The terminology memory survives: a project
// glossary outlives one document run unless the caller drops it explicitly.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reg.Reset()
	s.sentences = 0
	s.version++
	s.timeline = nil
	s.topicStack, s.focusStack = nil, nil
	s.topicIDs, s.focusIDs = nil, nil
	s.speaker, s.listener = "", ""
	s.relations = nil
	s.pending = nil
	s.anaphoraSeq = 0
	s.decisions = map[string]*DecisionEntry{}
	s.decSeq = 0
	s.invalidations = nil
	s.lastInvalidated = nil
	s.styleN = 0
	s.style = defaultStyle()
}

// ResetAll clears the document state and its terminology memory.
func (s *Store) ResetAll() {
	s.Reset()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terms.Reset()
}

// Snapshot renders the document state for the API response and the WebUI.
func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ents := s.reg.All()
	views := make([]EntityView, 0, len(ents))
	byID := make(map[jlir.ID]EntityView, len(ents))
	for _, e := range ents {
		byID[e.ID] = e.View()
	}
	st := State{
		Timeline:    append([]EventView(nil), s.timeline...),
		TopicStack:  append([]string(nil), s.topicStack...),
		FocusStack:  append([]string(nil), s.focusStack...),
		Style:       s.style,
		Terms:       s.terms.Views(),
		Sentences:   s.sentences,
		Version:     s.version,
		Relations:   append([]RelationView(nil), s.relations...),
		Conflicts:   s.terms.Conflicts(),
		Salience:    make(map[string]float64, len(ents)),
		Invalidated: append([]string(nil), s.lastInvalidated...),
	}
	// Entities are emitted in canonical id order, which is also creation order.
	for _, e := range ents {
		views = append(views, byID[e.ID])
		st.Salience[string(e.ID)] = e.Salience
	}
	st.Entities = views
	if v, ok := byID[s.speaker]; ok {
		v := v
		st.Speaker = &v
	}
	if v, ok := byID[s.listener]; ok {
		v := v
		st.Listener = &v
	}
	for _, a := range s.pending {
		cp := *a
		st.Pending = append(st.Pending, &cp)
		if q := BuildQuestion(a.QuestionID, a, ""); q != nil {
			st.OpenQuestions = append(st.OpenQuestions, *q)
		}
	}
	sort.Slice(st.Pending, func(i, j int) bool {
		if st.Pending[i].Sentence != st.Pending[j].Sentence {
			return st.Pending[i].Sentence < st.Pending[j].Sentence
		}
		return st.Pending[i].ZeroID < st.Pending[j].ZeroID
	})
	sort.Slice(st.OpenQuestions, func(i, j int) bool { return st.OpenQuestions[i].ID < st.OpenQuestions[j].ID })
	return st
}

// --- document registry ----------------------------------------------------

type docEntry struct {
	store    *Store
	lastUsed time.Time
}

// Documents maps a document id to one Store, creating it on first use. It is
// what the HTTP layer needs: two requests for the same document share one
// state, and two documents never share one.
type Documents struct {
	mu      sync.Mutex
	entries map[string]*docEntry
}

// NewDocuments returns an empty document registry.
func NewDocuments() *Documents {
	return &Documents{entries: map[string]*docEntry{}}
}

// Get returns the store for a document id, creating it if necessary.
func (d *Documents) Get(id string) *Store {
	if id == "" {
		id = defaultDocument
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries == nil {
		d.entries = map[string]*docEntry{}
	}
	e, ok := d.entries[id]
	if !ok {
		e = &docEntry{store: NewStore()}
		d.entries[id] = e
		d.evictLocked()
	}
	e.lastUsed = time.Now()
	return e.store
}

// Lookup returns the store for a document id if it already exists.
func (d *Documents) Lookup(id string) (*Store, bool) {
	if id == "" {
		id = defaultDocument
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.entries[id]
	if !ok {
		return nil, false
	}
	e.lastUsed = time.Now()
	return e.store, true
}

// Drop forgets a document entirely.
func (d *Documents) Drop(id string) {
	if id == "" {
		id = defaultDocument
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, id)
}

// Reset clears the state of a document but keeps its terminology memory, which
// is what "start over on this document" means.
func (d *Documents) Reset(id string) { d.Get(id).Reset() }

// IDs returns the known document ids, sorted.
func (d *Documents) IDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.entries))
	for k := range d.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// evictLocked drops the least recently used document beyond maxDocuments.
func (d *Documents) evictLocked() {
	if len(d.entries) <= maxDocuments {
		return
	}
	oldest := ""
	var oldestAt time.Time
	for k, e := range d.entries {
		if oldest == "" || e.lastUsed.Before(oldestAt) {
			oldest, oldestAt = k, e.lastUsed
		}
	}
	if oldest != "" {
		delete(d.entries, oldest)
	}
}

// defaultDocuments is the process wide registry the HTTP layer uses.
var defaultDocuments = NewDocuments()

// ForDocument returns the store for a document id, created on first use.
func ForDocument(id string) *Store { return defaultDocuments.Get(id) }

// LookupDocument returns the store for a document id if it exists.
func LookupDocument(id string) (*Store, bool) { return defaultDocuments.Lookup(id) }

// DropDocument forgets a document.
func DropDocument(id string) { defaultDocuments.Drop(id) }

// ResetDocument clears the state of a document.
func ResetDocument(id string) { defaultDocuments.Reset(id) }

// DocumentIDs returns the known document ids.
func DocumentIDs() []string { return defaultDocuments.IDs() }
