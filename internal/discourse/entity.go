// Package discourse implements the Document Context Store of plan.md §28:
//
//	翻訳単位をsentenceに限定しない。Document Context Store を持つ。
//
// The translation unit is not the sentence. 山田教授 / 山田さん / 教授 / 彼 /
// 山田 are one referent (plan.md §29), 東京 / Tokyo are two surface forms of one
// entity rather than two predicates (plan.md §51), and a technical term keeps
// one target form for the whole document (plan.md §52).
//
// The package is split into four files:
//
//	entity.go   identity registry, merging, salience and the zero-subject prior
//	anaphora.go zero arguments as unresolved distributions (plan.md §15/§16)
//	terms.go    terminology memory (plan.md §52)
//	store.go    the document state, the decision cache and invalidation (§57)
//
// Two rules shape every decision here. Nothing is invented: a merge needs a
// name, an alias, or an identity feature that carries provenance, and a
// gender never appears without one. Nothing collapses early: a zero subject
// stays a Distribution with a real posterior, because the plan's own example
// keeps Taro at 0.56 and Hanako at 0.43.
package discourse

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
)

// UnknownOption is the label the referential layer reserves for "not one of the
// candidates" (plan.md §15 lists it as a first class candidate of a zero
// argument). The prior never assigns it zero mass: dropping it would be the
// very collapse the plan forbids.
const UnknownOption = "UNKNOWN"

// Merge justification kinds. A merge is only ever performed for one of these
// and only when the evidence is actually present.
const (
	MergeByIdentity        = "identity"        // shared canonical identity key
	MergeByAlias           = "alias"           // shared surface form
	MergeByTransliteration = "transliteration" // 東京 / Tōkyō / Tokyo
	MergeByFeature         = "feature"         // explicit identity feature + provenance
	MergeByUser            = "user"            // user asserted it (plan.md §62)
	MergeByPronoun         = "pronoun"         // pronoun resolved against a clear antecedent
	MergeByDisclosure      = "disclosure"      // a later sentence revealed the identity
)

const (
	// maxPriorTop caps the probability a single zero-subject candidate may
	// reach. Plan.md §15 keeps 0.56/0.43 alive; a prior that saturates at 1.0
	// would destroy the ambiguity before any oracle ever sees it.
	maxPriorTop = 0.90
	// priorFloor keeps every candidate visible in the distribution, however
	// implausible the discourse made it.
	priorFloor = 0.02
	// unknownMass is the baseline weight of the UNKNOWN bucket.
	unknownMass = 0.03
	// pronounMargin is how far a pronoun-resolved antecedent must lead the
	// runner-up before the pronoun is folded into that entity's aliases. Below
	// it the pronoun stays unresolved (house rule 2) and is reported.
	pronounMargin = 0.25
	// salienceDecay is the mention-distance constant of the recency term, in
	// sentences: after two intervening sentences an entity keeps ~37% of its
	// recency weight, after four ~14%.
	salienceDecay = 2.0
)

// ErrUnjustifiedMerge is returned when a caller asks for a merge the discourse
// cannot justify. Guessing two referents to be identical is precisely the
// failure mode plan.md §4 forbids, so it is an error rather than a silent union.
var ErrUnjustifiedMerge = errors.New("discourse: merge is not justified by a name, alias, or identity feature")

// Alias is one surface form an entity has been referred to by, with the
// provenance that put it there.
type Alias struct {
	Surface string          `json:"surface"`
	Lang    lang.Lang       `json:"lang"`
	Kind    string          `json:"kind"` // name | title | alias | pronoun | translation
	Mention int             `json:"mention"`
	Prov    jlir.Provenance `json:"prov,omitempty"`
	// Anaphoric marks an alias that was attached by pronoun resolution rather
	// than by the sentence itself. It may be revisited when the discourse
	// changes; a name may not.
	Anaphoric bool `json:"anaphoric,omitempty"`
}

// Entity is the store's identity record for one referent. It mirrors the
// referential layer of plan.md §14 and the named-entity record of §51, and it
// carries the discourse-only bookkeeping the sentence graphs do not have:
// mention distance, aliases accumulated across sentences, and merge history.
type Entity struct {
	ID              jlir.ID              `json:"id"`
	Type            string               `json:"type"`
	Proper          bool                 `json:"proper"`
	Canonical       string               `json:"canonical"`
	Forms           map[lang.Lang]string `json:"forms,omitempty"`
	Reading         string               `json:"reading,omitempty"`
	Transliteration string               `json:"transliteration,omitempty"`

	Number  string `json:"number"`
	Gender  string `json:"gender"`
	Animacy string `json:"animacy"`
	Person  int    `json:"person,omitempty"`
	Plural  bool   `json:"plural,omitempty"`

	SpeakerRelation string `json:"speakerRelation,omitempty"`

	Aliases      []Alias `json:"aliases,omitempty"`
	MentionCount int     `json:"mentionCount"`
	FirstMention int     `json:"firstMention"`
	LastMention  int     `json:"lastMention"`
	Salience     float64 `json:"salience"`

	Features []jlir.Feature    `json:"features,omitempty"`
	Prov     []jlir.Provenance `json:"provenance,omitempty"`

	// External marks an entity the store introduced rather than one a sentence
	// mentioned overtly (the speaker, a previously seen referent).
	External bool `json:"external,omitempty"`
	// Alive is false once the entity has been merged into another one.
	Alive bool `json:"alive"`
	// MergedInto is the survivor of a merge.
	MergedInto jlir.ID `json:"mergedInto,omitempty"`
	// Keys are the normalized identity keys this entity answers to. They are
	// what lets 東京 in sentence 3 and Tokyo in sentence 7 meet.
	Keys []string `json:"keys,omitempty"`
}

// Animate reports a *known* animate referent. It never guesses: an entity whose
// animacy was never asserted is not animate here.
func (e *Entity) Animate() bool {
	if e == nil {
		return false
	}
	if e.Animacy == jlir.AnimacyAnimate {
		return true
	}
	if e.Animacy == jlir.AnimacyInanimate {
		return false
	}
	switch e.Type {
	case jlir.TypeHuman, jlir.TypePerson, jlir.TypeAnimal:
		return true
	}
	return false
}

// MayBeAnimate reports whether the entity could be animate. An unasserted
// animacy stays eligible for pronoun resolution — with a lower weight — instead
// of being silently excluded.
func (e *Entity) MayBeAnimate() bool {
	if e == nil {
		return false
	}
	return e.Animate() || e.Animacy == jlir.AnimacyUnknown
}

// GenderValue returns the asserted gender, or UNKNOWN.
func (e *Entity) GenderValue() string {
	if e == nil {
		return jlir.GenderUnknown
	}
	if f, ok := e.Feature("gender"); ok {
		return jlir.ValueString(f.Value)
	}
	if e.Gender != "" {
		return e.Gender
	}
	return jlir.GenderUnknown
}

// Feature returns the first feature with the given key.
func (e *Entity) Feature(key string) (jlir.Feature, bool) {
	if e == nil {
		return jlir.Feature{}, false
	}
	for _, f := range e.Features {
		if f.Key == key {
			return f, true
		}
	}
	return jlir.Feature{}, false
}

// SetFeature upserts a feature, merging provenance like the referential layer
// does. Provenance is what makes a value legitimate (plan.md §21); a feature
// arriving without it is still stored, but the caller can detect it.
func (e *Entity) SetFeature(f jlir.Feature) {
	if e == nil {
		return
	}
	for i := range e.Features {
		if e.Features[i].Key == f.Key {
			e.Features[i].Value = f.Value
			if f.Confidence > 0 {
				e.Features[i].Confidence = f.Confidence
			}
			e.Features[i].Prov = append(e.Features[i].Prov, f.Prov...)
			return
		}
	}
	e.Features = append(e.Features, f)
	if v := featureValue(f.Value, "gender"); v != "" {
		e.Gender = v
	}
	if v := featureValue(f.Value, "number"); v != "" {
		e.Number = v
	}
	if v := featureValue(f.Value, "animacy"); v != "" {
		e.Animacy = v
	}
	if v := featureValue(f.Value, "speaker_relation"); v != "" {
		e.SpeakerRelation = v
	}
}

func featureValue(v any, key string) string {
	m, ok := v.(map[string]string)
	if !ok {
		return ""
	}
	return m[key]
}

// Name returns the entity's proper name in l, falling back to the shortest
// name-like alias and finally to the canonical form.
func (e *Entity) Name(l lang.Lang) string {
	if e == nil {
		return ""
	}
	if s, ok := e.Forms[l]; ok && s != "" {
		return s
	}
	best := ""
	for _, a := range e.Aliases {
		if a.Kind != "name" && a.Kind != "translation" {
			continue
		}
		if a.Lang != l && a.Lang != "" {
			continue
		}
		if best == "" || len(a.Surface) < len(best) {
			best = a.Surface
		}
	}
	if best == "" {
		best = e.Canonical
	}
	return best
}

// Surface returns the most recent surface form of the entity in l.
func (e *Entity) Surface(l lang.Lang) string {
	if e == nil {
		return ""
	}
	for i := len(e.Aliases) - 1; i >= 0; i-- {
		if e.Aliases[i].Lang == l {
			return e.Aliases[i].Surface
		}
	}
	return e.Name(l)
}

// Recency is the mention-distance factor: 1 in the sentence of the mention,
// decaying exponentially with the number of sentences since (plan.md §28 wants
// salience to depend on how far back the mention is, not just on how often it
// happened).
func (e *Entity) Recency(now int) float64 {
	if e == nil {
		return 0
	}
	d := now - e.LastMention
	if d < 0 {
		d = 0
	}
	return math.Exp(-float64(d) / salienceDecay)
}

// Mention records that the entity was referred to in sentence sent.
func (e *Entity) Mention(sent int) {
	if e == nil {
		return
	}
	e.MentionCount++
	if e.FirstMention == 0 || sent < e.FirstMention {
		e.FirstMention = sent
	}
	if sent > e.LastMention {
		e.LastMention = sent
	}
}

// AddAlias records a surface form once; a repeated form bumps its mention count
// rather than duplicating the entry.
func (e *Entity) AddAlias(a Alias) {
	if e == nil || a.Surface == "" {
		return
	}
	for i := range e.Aliases {
		if e.Aliases[i].Surface == a.Surface && e.Aliases[i].Lang == a.Lang && e.Aliases[i].Kind == a.Kind {
			e.Aliases[i].Mention++
			e.Aliases[i].Anaphoric = e.Aliases[i].Anaphoric && a.Anaphoric
			return
		}
	}
	if a.Kind == "" {
		a.Kind = "alias"
	}
	e.Aliases = append(e.Aliases, a)
}

// HasAlias reports whether the entity has ever been called surface.
func (e *Entity) HasAlias(surface string) bool {
	if e == nil {
		return false
	}
	for _, a := range e.Aliases {
		if a.Surface == surface {
			return true
		}
	}
	return false
}

// MergeEvidence is the justification for identifying two entities. It is kept
// as data so the trace can show *why* two referents became one.
type MergeEvidence struct {
	Kind   string          `json:"kind"`
	Detail string          `json:"detail,omitempty"`
	Prov   jlir.Provenance `json:"prov,omitempty"`
}

func (m MergeEvidence) String() string {
	if m.Detail == "" {
		return m.Kind
	}
	return m.Kind + ":" + m.Detail
}

// MergeResult reports a completed identification.
type MergeResult struct {
	Kept     jlir.ID       `json:"kept"`
	Absorbed []jlir.ID     `json:"absorbed"`
	Evidence MergeEvidence `json:"evidence"`
}

// Change is a material attribute of an entity that changed. plan.md §57 turns
// exactly this record — 山田さんは女性です — into a cache invalidation.
type Change struct {
	Entity   jlir.ID         `json:"entity"`
	Key      string          `json:"key"`
	Previous string          `json:"previous,omitempty"`
	Value    string          `json:"value"`
	Prov     jlir.Provenance `json:"prov,omitempty"`
	Material bool            `json:"material"`
}

// PronounBinding is a pronoun mention that was attached to an antecedent.
type PronounBinding struct {
	Surface    string          `json:"surface"`
	Pronoun    jlir.ID         `json:"pronoun"`
	Antecedent jlir.ID         `json:"antecedent"`
	Confidence float64         `json:"confidence"`
	Prov       jlir.Provenance `json:"prov,omitempty"`
}

// Update is everything one Intern pass did. The store turns it into trace
// notes, invalidations and the sentence's contribution to the document state.
type Update struct {
	Sentence int              `json:"sentence"`
	Interned []jlir.ID        `json:"interned,omitempty"`
	Created  []jlir.ID        `json:"created,omitempty"`
	Merges   []MergeResult    `json:"merges,omitempty"`
	Resolved []PronounBinding `json:"resolved,omitempty"`
	Pending  []PendingPronoun `json:"pending,omitempty"`
	Zeros    []ZeroSlot       `json:"zeros,omitempty"`
	Changes  []Change         `json:"changes,omitempty"`
	Speaker  jlir.ID          `json:"speaker,omitempty"`
	Listener jlir.ID          `json:"listener,omitempty"`
	Subject  jlir.ID          `json:"subject,omitempty"`
	Topic    []jlir.ID        `json:"topic,omitempty"`
	Notes    []string         `json:"notes,omitempty"`
}

// Registry is the identity store: canonical entities, the identity keys they
// answer to, and the discourse features (salience, subject, topic) that make a
// zero argument resolvable.
//
// The Registry is not internally synchronized. Store is the synchronized entry
// point and the only place that may be reached from an HTTP handler.
type Registry struct {
	entities map[jlir.ID]*Entity
	byKey    map[string]jlir.ID
	byBase   map[string][]jlir.ID
	// local maps a sentence-scoped graph id ("e1") to the canonical id, per
	// sentence, so "e1" in sentence 1 and "e1" in sentence 2 never collide.
	local   map[string]jlir.ID
	current map[jlir.ID]jlir.ID

	now         int
	seq         int
	lastSubject jlir.ID
	topic       []jlir.ID
	speaker     jlir.ID
	listener    jlir.ID
	version     int
}

// NewRegistry returns an empty identity store.
func NewRegistry() *Registry {
	return &Registry{
		entities: map[jlir.ID]*Entity{},
		byKey:    map[string]jlir.ID{},
		byBase:   map[string][]jlir.ID{},
		local:    map[string]jlir.ID{},
		current:  map[jlir.ID]jlir.ID{},
	}
}

// Version counts identity-affecting mutations so a stale cache entry is
// detectable (plan.md §56).
func (r *Registry) Version() int { return r.version }

// Get returns the entity with that canonical id, or nil.
func (r *Registry) Get(id jlir.ID) *Entity {
	if id == "" {
		return nil
	}
	e, ok := r.entities[id]
	if !ok || !e.Alive {
		return nil
	}
	return e
}

// Canonical maps any id the pipeline may hold — a canonical "E3", a
// sentence-scoped "e1", or an absorbed id — onto the surviving canonical id.
// An unknown id is returned unchanged so a caller-supplied candidate is never
// silently dropped from a distribution.
func (r *Registry) Canonical(id jlir.ID) jlir.ID {
	if id == "" {
		return ""
	}
	if e := r.Get(id); e != nil {
		return e.ID
	}
	if c, ok := r.current[id]; ok {
		if e := r.Get(c); e != nil {
			return e.ID
		}
	}
	if c, ok := r.local[localKey(r.now, id)]; ok {
		if e := r.Get(c); e != nil {
			return e.ID
		}
	}
	return id
}

// CanonicalAt is Canonical for a specific sentence, for pipelines that hold a
// graph together with the sentence index it was produced for.
func (r *Registry) CanonicalAt(sent int, id jlir.ID) jlir.ID {
	if id == "" {
		return ""
	}
	if e := r.Get(id); e != nil {
		return e.ID
	}
	if c, ok := r.local[localKey(sent, id)]; ok {
		if e := r.Get(c); e != nil {
			return e.ID
		}
	}
	return id
}

// BySurface resolves a surface form to a live entity.
func (r *Registry) BySurface(surface string) *Entity {
	id, ok := r.byKey[normalizeKey(surface)]
	if !ok {
		return nil
	}
	return r.Get(id)
}

// All returns every live entity, sorted by id for deterministic output.
func (r *Registry) All() []*Entity {
	out := make([]*Entity, 0, len(r.entities))
	for _, e := range r.entities {
		if e.Alive {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Merged returns the entities that were folded into others, sorted by id.
func (r *Registry) Merged() []*Entity {
	var out []*Entity
	for _, e := range r.entities {
		if !e.Alive {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LastSubject returns the canonical id of the subject of the previous clause,
// the strongest candidate for a following zero subject.
func (r *Registry) LastSubject() jlir.ID { return r.lastSubject }

// Topic returns the current topic chain, most recent first.
func (r *Registry) Topic() []jlir.ID { return append([]jlir.ID(nil), r.topic...) }

// Speaker returns the canonical id of the speaker, if one is known.
func (r *Registry) Speaker() jlir.ID { return r.speaker }

// Listener returns the canonical id of the addressee, if one is known.
func (r *Registry) Listener() jlir.ID { return r.listener }

// Salience returns the current salience of every live entity (plan.md §14).
func (r *Registry) Salience() map[jlir.ID]float64 {
	out := make(map[jlir.ID]float64, len(r.entities))
	for _, e := range r.All() {
		out[e.ID] = e.Salience
	}
	return out
}

// Intern folds one sentence graph into the identity store.
//
// Zero placeholders are not entities: they are recorded as ZeroSlot so the
// decision layer can fill them, which is what keeps plan.md §15 honest about a
// zero argument never having been a real referent.
func (r *Registry) Intern(g *jlir.Graph, sent int) *Update {
	u := &Update{Sentence: sent}
	if g == nil {
		return u
	}
	if sent > r.now {
		r.now = sent
	}
	for _, e := range g.Entities {
		if e == nil {
			continue
		}
		if e.Zero || e.Referent != nil {
			u.Zeros = append(u.Zeros, ZeroSlot{
				ZeroID:     e.ID,
				Type:       e.Type,
				Candidates: referentOptions(e),
				Prov:       append([]jlir.Provenance(nil), e.Prov...),
			})
			continue
		}
		ent, created := r.internEntity(g, e, sent, u)
		if ent == nil {
			continue
		}
		r.current[e.ID] = ent.ID
		r.local[localKey(sent, e.ID)] = ent.ID
		u.Interned = append(u.Interned, ent.ID)
		if created {
			u.Created = append(u.Created, ent.ID)
		}
	}
	u.Merges = append(u.Merges, r.attachNamed(g)...)
	r.attachSpeakerAndListener(g, u)
	r.setTopic(g, u)
	r.setSubject(g)
	// Salience is rescored before pronoun resolution so a pronoun sees the
	// sentence it belongs to rather than the previous one.
	r.Rescore()
	u.Resolved = r.resolvePronouns(g, sent, u)
	r.Rescore()
	u.Subject = r.lastSubject
	u.Topic = r.Topic()
	u.Speaker = r.speaker
	u.Listener = r.listener
	r.version++
	sortIDs(u.Interned)
	sortIDs(u.Created)
	return u
}

// referentOptions reads the candidates a zero placeholder already carries,
// without asking the foundation package for a helper it does not have.
func referentOptions(e *jlir.Entity) []string {
	if e == nil || e.Referent == nil {
		return nil
	}
	return append([]string(nil), e.Referent.Options...)
}

func localKey(sent int, id jlir.ID) string { return fmt.Sprintf("%d\x00%s", sent, id) }

// internEntity finds or creates the store entity for one graph entity.
func (r *Registry) internEntity(g *jlir.Graph, e *jlir.Entity, sent int, u *Update) (*Entity, bool) {
	keys := identityKeys(g.Lang, e, g)
	strong := strongKeys(keys)

	// Strong evidence: a name, an alias, a transliteration or an asserted
	// identity. Any hit merges, because the surface itself is the evidence.
	for _, k := range strong {
		if id, ok := r.byKey[k]; ok {
			if ent := r.Get(id); ent != nil {
				r.absorb(ent, e, g, sent, u)
				return ent, false
			}
		}
	}

	// Weak evidence: 山田教授 and 山田さん differ only in a title. That merges
	// only while exactly one entity carries the bare name — two Yamadas in one
	// document must stay two people, so the ambiguity is reported instead.
	var weak []jlir.ID
	for _, k := range baseKeys(keys) {
		weak = appendUniqueIDs(weak, r.byBase[k]...)
	}
	if len(weak) == 1 {
		if ent := r.Get(weak[0]); ent != nil {
			r.absorb(ent, e, g, sent, u)
			u.Notes = append(u.Notes, fmt.Sprintf(
				"%s unified with %s on a shared bare name (title stripped)", e.ID, ent.ID))
			return ent, false
		}
	} else if len(weak) > 1 {
		u.Notes = append(u.Notes, fmt.Sprintf(
			"%s shares a bare name with %v; not merged, the document has several candidates",
			e.ID, sortedIDs(weak)))
	}

	ent := r.newEntity(e, g, sent, keys)
	u.Notes = append(u.Notes, fmt.Sprintf("%s introduced as %s (%s)", ent.ID, ent.Canonical, ent.Type))
	return ent, true
}

func (r *Registry) newEntity(e *jlir.Entity, g *jlir.Graph, sent int, keys []string) *Entity {
	r.seq++
	ent := &Entity{
		ID:              jlir.ID(fmt.Sprintf("E%d", r.seq)),
		Type:            e.Type,
		Proper:          e.Proper,
		Canonical:       canonicalName(g.Lang, e),
		Forms:           map[lang.Lang]string{},
		Number:          orUnknown(e.Number, jlir.NumberUnknown),
		Gender:          orUnknown(e.Gender, jlir.GenderUnknown),
		Animacy:         orUnknown(e.Animacy, jlir.AnimacyUnknown),
		Person:          e.Person,
		Plural:          e.Plural,
		SpeakerRelation: e.SpeakerRelation,
		Alive:           true,
		FirstMention:    sent,
	}
	for _, ne := range namedForms(g, e) {
		ent.Canonical = orKeep(ent.Canonical, ne.Canonical)
		ent.Reading = orKeep(ent.Reading, ne.Reading)
		ent.Transliteration = orKeep(ent.Transliteration, ne.Transliteration)
		for l, form := range ne.Forms {
			if form != "" {
				ent.Forms[lang.Lang(l)] = form
			}
		}
	}
	for _, f := range e.Features {
		ent.SetFeature(f)
	}
	ent.Prov = append(ent.Prov, e.Prov...)
	ent.Prov = append(ent.Prov, jlir.Provenance{
		EntityID: ent.ID, Origin: jlir.OriginDiscourse,
		Confidence: 0.9, Note: "interned from " + string(g.Lang) + " sentence " + fmt.Sprint(sent),
	})
	for _, a := range e.Aliases {
		ent.AddAlias(Alias{Surface: a.Surface, Lang: a.Lang, Kind: aliasKind(a.Kind), Mention: 1})
	}
	if ent.Canonical != "" {
		ent.AddAlias(Alias{Surface: ent.Canonical, Lang: g.Lang, Kind: "name", Mention: 1})
	}
	for l, form := range ent.Forms {
		ent.AddAlias(Alias{Surface: form, Lang: l, Kind: "translation", Mention: 1})
	}
	ent.Mention(sent)
	r.entities[ent.ID] = ent
	r.index(ent, keys)
	return ent
}

// absorb folds a freshly analysed graph entity into an existing store entity.
func (r *Registry) absorb(ent *Entity, e *jlir.Entity, g *jlir.Graph, sent int, u *Update) {
	before := map[string]string{}
	for _, f := range ent.Features {
		before[f.Key] = jlir.ValueString(f.Value)
	}
	for _, a := range e.Aliases {
		ent.AddAlias(Alias{Surface: a.Surface, Lang: a.Lang, Kind: aliasKind(a.Kind), Mention: 1})
	}
	for _, ne := range namedForms(g, e) {
		ent.Canonical = orKeep(ent.Canonical, ne.Canonical)
		ent.Reading = orKeep(ent.Reading, ne.Reading)
		ent.Transliteration = orKeep(ent.Transliteration, ne.Transliteration)
		for l, form := range ne.Forms {
			if form != "" {
				ent.Forms[lang.Lang(l)] = form
			}
			ent.AddAlias(Alias{Surface: form, Lang: lang.Lang(l), Kind: "translation", Mention: 1})
		}
	}
	for _, f := range e.Features {
		ent.SetFeature(f)
		if prev, ok := before[f.Key]; ok && prev != jlir.ValueString(f.Value) {
			u.Changes = append(u.Changes, Change{
				Entity: ent.ID, Key: f.Key, Previous: prev, Value: jlir.ValueString(f.Value),
				Material: jlir.IsSensitive(f.Key), Prov: firstProv(f.Prov),
			})
		} else if !ok {
			u.Changes = append(u.Changes, Change{
				Entity: ent.ID, Key: f.Key, Value: jlir.ValueString(f.Value),
				Material: jlir.IsSensitive(f.Key), Prov: firstProv(f.Prov),
			})
		}
	}
	ent.Prov = append(ent.Prov, e.Prov...)
	if ent.Type == jlir.TypeUnknown && e.Type != jlir.TypeUnknown {
		ent.Type = e.Type
	}
	ent.Mention(sent)
	r.index(ent, identityKeys(g.Lang, e, g))
}

func firstProv(p []jlir.Provenance) jlir.Provenance {
	if len(p) == 0 {
		return jlir.Provenance{}
	}
	return p[0]
}

// index registers every identity key of the entity.
func (r *Registry) index(ent *Entity, keys []string) {
	seen := map[string]bool{}
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		r.byKey[k] = ent.ID
		ent.Keys = append(ent.Keys, k)
	}
	for _, k := range baseKeys(keys) {
		if !containsID(r.byBase[k], ent.ID) {
			r.byBase[k] = append(r.byBase[k], ent.ID)
		}
	}
}

// attachNamed copies NamedEntity identity records (plan.md §51: 東京 and Tokyo
// are language realizations of one entity, not two predicates).
func (r *Registry) attachNamed(g *jlir.Graph) []MergeResult {
	var out []MergeResult
	if len(g.NamedEntities) == 0 {
		return out
	}
	keys := map[string]bool{}
	byKey := map[string][]jlir.ID{}
	for key, ne := range g.NamedEntities {
		ids := make([]jlir.ID, 0, len(ne.Forms)+1)
		if c := r.Canonical(jlir.ID(key)); c != "" {
			if ent := r.Get(c); ent != nil {
				ids = append(ids, ent.ID)
			}
		}
		for _, form := range ne.Forms {
			if id, ok := r.byKey[normalizeKey(form)]; ok {
				ids = appendUniqueIDs(ids, id)
			}
		}
		if ent := r.BySurface(ne.Canonical); ent != nil {
			ids = appendUniqueIDs(ids, ent.ID)
		}
		if len(ids) == 0 {
			continue
		}
		formKeys := map[string]bool{}
		for _, form := range ne.Forms {
			if k := normalizeKey(form); k != "" {
				formKeys[k] = true
				keys[k] = true
			}
		}
		for k := range formKeys {
			byKey[k] = append([]jlir.ID(nil), ids...)
		}
		for _, id := range ids {
			ent := r.Get(id)
			if ent == nil {
				continue
			}
			ent.Canonical = orKeep(ent.Canonical, ne.Canonical)
			ent.Reading = orKeep(ent.Reading, ne.Reading)
			ent.Transliteration = orKeep(ent.Transliteration, ne.Transliteration)
			if ent.Type == jlir.TypeUnknown && ne.Type != "" {
				ent.Type = ne.Type
			}
			for l, form := range ne.Forms {
				if form == "" {
					continue
				}
				ent.Forms[lang.Lang(l)] = form
				ent.AddAlias(Alias{Surface: form, Lang: lang.Lang(l), Kind: "translation", Mention: 1})
			}
		}
	}
	// Every surface form of one named entity becomes one key space, so 東京 and
	// Tokyo meet on the next sentence without a guess.
	all := make([]string, 0, len(keys))
	for k := range keys {
		all = append(all, k)
	}
	sort.Strings(all)
	for _, k := range all {
		ids := byKey[k]
		if len(ids) < 2 {
			continue
		}
		survivor := ids[0]
		for _, id := range ids[1:] {
			res, err := r.Merge(survivor, jlir.ID(id), MergeEvidence{
				Kind:   MergeByTransliteration,
				Detail: "named entity surface " + k,
			})
			if err != nil {
				continue
			}
			r.version++
			survivor = res.Kept
			out = append(out, *res)
		}
	}
	return out
}

// Merge identifies two live entities and returns the absorbed ids so the caller
// can invalidate every decision that depended on them (plan.md §57).
func (r *Registry) Merge(a, b jlir.ID, ev MergeEvidence) (*MergeResult, error) {
	keep, gone := r.Get(a), r.Get(b)
	if keep == nil || gone == nil {
		return nil, fmt.Errorf("discourse: cannot merge unknown entities %q and %q", a, b)
	}
	if keep.ID == gone.ID {
		return &MergeResult{Kept: keep.ID, Evidence: ev}, nil
	}
	if ev.Kind == "" {
		ev.Kind = MergeByIdentity
	}
	if !r.justified(keep, gone, ev) {
		return nil, ErrUnjustifiedMerge
	}
	res := &MergeResult{Kept: keep.ID, Evidence: ev}
	if keep.MentionCount < gone.MentionCount {
		// The better attested referent survives, so salience stays meaningful.
		keep, gone = gone, keep
		res.Kept = keep.ID
	}
	for _, k := range gone.Keys {
		r.byKey[k] = keep.ID
		keep.Keys = appendUnique(keep.Keys, k)
		r.byBase[k] = appendUniqueIDs(r.byBase[k], keep.ID)
	}
	for _, a := range gone.Aliases {
		keep.AddAlias(a)
	}
	for _, f := range gone.Features {
		if _, has := keep.Feature(f.Key); !has {
			keep.Features = append(keep.Features, f)
		}
	}
	keep.Prov = append(keep.Prov, gone.Prov...)
	keep.Prov = append(keep.Prov, jlir.Provenance{
		EntityID: keep.ID, Origin: jlir.OriginDiscourse,
		Confidence: 0.8, Note: "merged " + string(gone.ID) + " (" + ev.String() + ")",
	})
	keep.MentionCount += gone.MentionCount
	if gone.FirstMention > 0 && (keep.FirstMention == 0 || gone.FirstMention < keep.FirstMention) {
		keep.FirstMention = gone.FirstMention
	}
	if gone.LastMention > keep.LastMention {
		keep.LastMention = gone.LastMention
	}
	gone.Alive = false
	gone.MergedInto = keep.ID
	res.Absorbed = append(res.Absorbed, gone.ID)
	for k, v := range r.current {
		if v == gone.ID {
			r.current[k] = keep.ID
		}
	}
	for k, v := range r.local {
		if v == gone.ID {
			r.local[k] = keep.ID
		}
	}
	if r.lastSubject == gone.ID {
		r.lastSubject = keep.ID
	}
	r.topic = replaceID(r.topic, gone.ID, keep.ID)
	if r.speaker == gone.ID {
		r.speaker = keep.ID
	}
	if r.listener == gone.ID {
		r.listener = keep.ID
	}
	r.Rescore()
	return res, nil
}

// justified enforces "never merge on a guess": the evidence must be a shared
// name/alias/transliteration, or an explicit identity feature or user decision
// carrying upstream provenance.
func (r *Registry) justified(keep, gone *Entity, ev MergeEvidence) bool {
	// The name-based case: the two referents answer to the same identity key,
	// which can only be a name, an alias, a reading or a transliteration.
	for _, k := range keep.Keys {
		for _, o := range gone.Keys {
			if k == o {
				return true
			}
		}
	}
	// A pronoun is joined to its antecedent only when the discourse margin is
	// decisive (pronounMargin) and the reasoning carries provenance.
	if ev.Kind == MergeByPronoun && ev.Prov.Upstream() && ev.Prov.Confidence >= pronounMargin {
		return true
	}
	switch ev.Kind {
	case MergeByUser:
		return ev.Prov.Origin == jlir.OriginUser
	case MergeByFeature, MergeByDisclosure:
		return ev.Prov.Upstream()
	}
	return false
}

// SetFeature asserts an attributed attribute on an entity and reports whether
// a material value changed. The store turns a material change into an
// invalidation (plan.md §57).
func (r *Registry) SetFeature(id jlir.ID, key string, value any, prov jlir.Provenance) (Change, bool) {
	ent := r.Get(r.Canonical(id))
	if ent == nil {
		return Change{}, false
	}
	prev := ""
	if f, ok := ent.Feature(key); ok {
		prev = jlir.ValueString(f.Value)
	}
	ent.SetFeature(jlir.Feature{Key: key, Value: value, Confidence: prov.Confidence, Prov: []jlir.Provenance{prov}})
	r.version++
	ch := Change{Entity: ent.ID, Key: key, Previous: prev, Value: jlir.ValueString(value),
		Prov: prov, Material: jlir.IsSensitive(key)}
	return ch, prev != ch.Value
}

// resolvePronouns folds bare pronoun mentions into the referent they clearly
// denote, and records the ones that stay ambiguous. This is what makes
// 山田教授 … 彼 one entity (plan.md §29), and it is the only place where a
// discourse inference, rather than an overt name, joins an alias.
func (r *Registry) resolvePronouns(g *jlir.Graph, sent int, u *Update) []PronounBinding {
	var out []PronounBinding
	for _, e := range g.Entities {
		if e == nil || e.Zero || e.Referent != nil {
			continue
		}
		ent := r.Get(r.Canonical(e.ID))
		if ent == nil {
			continue
		}
		pr := pronounOf(ent, g.Lang)
		if pr.surface == "" {
			continue
		}
		cands := r.pronounCandidates(ent, pr)
		if len(cands) == 0 {
			continue
		}
		d := r.priorOver(cands)
		top := d.Top(2)
		if len(top) == 0 {
			continue
		}
		margin := top[0].P
		if len(top) > 1 {
			margin = top[0].P - top[1].P
		}
		if margin < pronounMargin {
			u.Pending = append(u.Pending, PendingPronoun{
				Surface:    pr.surface,
				Entity:     ent.ID,
				Candidates: cands,
				Prior:      d,
				Note:       "antecedent is not clear enough to attach the pronoun",
			})
			continue
		}
		target := r.Get(jlir.ID(top[0].Option))
		if target == nil {
			continue
		}
		prov := jlir.Provenance{
			Token: pr.surface, Origin: jlir.OriginDiscourse,
			Confidence: margin, Note: "pronoun resolved against " + string(target.ID) +
				" with salience margin " + fmt.Sprintf("%.2f", margin),
		}
		res, err := r.Merge(target.ID, ent.ID, MergeEvidence{
			Kind: MergeByPronoun, Detail: pr.surface, Prov: prov})
		if err != nil {
			// The evidence turned out not to be strong enough: keep the pronoun
			// as its own entity rather than pretending it was identified.
			u.Pending = append(u.Pending, PendingPronoun{
				Surface: pr.surface, Entity: ent.ID, Candidates: cands, Prior: d,
				Note: "pronoun not attached: " + err.Error(),
			})
			continue
		}
		target.AddAlias(Alias{Surface: pr.surface, Lang: g.Lang, Kind: "pronoun", Mention: 1,
			Prov: prov, Anaphoric: true})
		target.Mention(sent)
		if prev := pr.gender; prev != "" && target.GenderValue() == jlir.GenderUnknown {
			// A gendered pronoun is upstream evidence of gender; it is recorded
			// with provenance, never asserted silently (house rule 1).
			target.SetFeature(jlir.Feature{Key: "gender", Value: prev, Confidence: margin,
				Prov: []jlir.Provenance{prov}})
			u.Changes = append(u.Changes, Change{Entity: target.ID, Key: "gender",
				Value: prev, Prov: prov, Material: true})
		}
		u.Merges = append(u.Merges, *res)
		if len(res.Absorbed) > 0 {
			out = append(out, PronounBinding{Surface: pr.surface, Pronoun: res.Absorbed[0],
				Antecedent: res.Kept, Confidence: margin, Prov: prov})
		}
		r.version++
	}
	return out
}

// pronounCandidates lists the referents a pronoun could denote, filtered by
// gender only where gender was actually asserted.
func (r *Registry) pronounCandidates(self *Entity, pr pronoun) []jlir.ID {
	var out []jlir.ID
	for _, e := range r.All() {
		if e.ID == self.ID {
			continue
		}
		if pr.person != 0 && e.Person != 0 && pr.person != e.Person {
			continue
		}
		switch pr.person {
		case 1:
			if e.SpeakerRelation != "" && e.SpeakerRelation != "speaker" {
				continue
			}
		case 2:
			if e.SpeakerRelation != "" && e.SpeakerRelation != "listener" {
				continue
			}
		}
		if pr.gender != "" {
			g := e.GenderValue()
			if g != jlir.GenderUnknown && g != pr.gender {
				continue
			}
		}
		if pr.demonstrative {
			if e.Animate() {
				continue
			}
		} else if !e.MayBeAnimate() {
			continue
		}
		out = append(out, e.ID)
	}
	return out
}

// attachSpeakerAndListener recognises 私 / 僕 / I as the speaker and
// あなた / you as the addressee, with provenance, so pronouns can be realized
// consistently for the whole document (plan.md §48).
func (r *Registry) attachSpeakerAndListener(g *jlir.Graph, u *Update) {
	for _, e := range g.Entities {
		if e == nil || e.Zero || e.Referent != nil {
			continue
		}
		ent := r.Get(r.Canonical(e.ID))
		if ent == nil {
			continue
		}
		if f, ok := ent.Feature("speaker_relation"); ok {
			switch jlir.ValueString(f.Value) {
			case "speaker":
				if r.speaker == "" {
					r.speaker = ent.ID
					u.Speaker = ent.ID
				}
			case "listener", "addressee":
				if r.listener == "" {
					r.listener = ent.ID
					u.Listener = ent.ID
				}
			}
			continue
		}
		if p := personOf(ent, g.Lang); p == 1 && r.speaker == "" {
			r.speaker = ent.ID
			ent.SpeakerRelation = "speaker"
			u.Speaker = ent.ID
		} else if p == 2 && r.listener == "" {
			r.listener = ent.ID
			ent.SpeakerRelation = "listener"
			u.Listener = ent.ID
		}
	}
}

// setTopic records the は-chain. Topic is emphatically not subject (plan.md §19).
func (r *Registry) setTopic(g *jlir.Graph, u *Update) {
	var topic []jlir.ID
	for _, id := range g.Info.Topic {
		if c := r.Canonical(id); c != "" {
			if e := r.Get(c); e != nil {
				topic = append(topic, e.ID)
			}
		}
	}
	if len(topic) == 0 {
		return
	}
	r.topic = topic
	u.Topic = append([]jlir.ID(nil), topic...)
}

// setSubject records the subject of the clause that was just read: the matrix
// clause's agent/experiencer, else the first overt entity. It is the strongest
// candidate for a following zero subject (plan.md §15).
func (r *Registry) setSubject(g *jlir.Graph) {
	events := orderedEvents(g)
	for _, v := range events {
		for _, role := range []string{jlir.RoleAgent, jlir.RoleExperiencer, jlir.RoleTheme, jlir.RolePatient} {
			a, ok := v.Arg(role)
			if !ok {
				continue
			}
			c := r.Canonical(a.Value)
			e := r.Get(c)
			if e == nil {
				continue
			}
			if r.isPronounEntity(e) {
				continue
			}
			r.lastSubject = e.ID
			return
		}
	}
	// No filled argument: fall back to the first entity that is not a pronoun
	// placeholder, which is what an overt は-phrase or a bare NP gives us.
	for _, id := range append(append([]jlir.ID(nil), g.Info.Topic...), focusAndNew(g)...) {
		if e := r.Get(r.Canonical(id)); e != nil && !r.isPronounEntity(e) {
			r.lastSubject = e.ID
			return
		}
	}
	for _, e := range r.current {
		if ent := r.Get(e); ent != nil && !r.isPronounEntity(ent) {
			r.lastSubject = ent.ID
			return
		}
	}
}

func focusAndNew(g *jlir.Graph) []jlir.ID {
	out := append([]jlir.ID(nil), g.Info.Focus...)
	return append(out, g.Info.New...)
}

func orderedEvents(g *jlir.Graph) []*jlir.Event {
	var out []*jlir.Event
	seen := map[jlir.ID]bool{}
	for _, id := range g.ClauseOrder {
		if v := g.Event(id); v != nil && !seen[v.ID] {
			seen[v.ID] = true
			out = append(out, v)
		}
	}
	for _, v := range g.Events {
		if v != nil && !seen[v.ID] {
			seen[v.ID] = true
			out = append(out, v)
		}
	}
	return out
}

func (r *Registry) isPronounEntity(e *Entity) bool {
	if e == nil || len(e.Aliases) == 0 {
		return false
	}
	for _, a := range e.Aliases {
		if a.Kind != "pronoun" {
			return false
		}
	}
	return true
}

// Rescore recomputes discourse salience for every entity at the current
// sentence, decaying with mention distance (plan.md §14, §28).
func (r *Registry) Rescore() {
	inTopic := map[jlir.ID]bool{}
	for _, id := range r.topic {
		inTopic[id] = true
	}
	for _, e := range r.entities {
		if !e.Alive {
			continue
		}
		score := 2.0 * e.Recency(r.now)
		score += 0.35 * math.Log1p(float64(e.MentionCount))
		if e.MayBeAnimate() {
			score += 0.6
		}
		if e.ID == r.lastSubject {
			score += 1.8
		}
		if inTopic[e.ID] {
			score += 0.9
		}
		switch e.SpeakerRelation {
		case "speaker":
			score += 0.5
		case "listener":
			score += 0.3
		}
		// Damped into (0,1): salience orders referents, it does not decide.
		e.Salience = score / (score + 2.0)
	}
}

// Prior returns the discourse distribution over the candidates of a zero
// argument. The order of the Japanese pattern the translation depends on is
// encoded here: the previous clause's subject first, then the most recently
// mentioned animate referent, then the topic — and the result never saturates
// on 1.0 (plan.md §15, §16).
func (r *Registry) Prior(candidates []jlir.ID) *jlir.Distribution {
	if len(candidates) == 0 {
		return nil
	}
	return r.priorOver(candidates)
}

func (r *Registry) priorOver(candidates []jlir.ID) *jlir.Distribution {
	inTopic := map[jlir.ID]bool{}
	for _, id := range r.topic {
		inTopic[id] = true
	}
	weights := map[string]float64{}
	var real []jlir.ID
	for _, c := range candidates {
		if c == UnknownOption {
			weights[UnknownOption] += unknownMass
			continue
		}
		id := r.Canonical(c)
		ent := r.Get(id)
		if ent == nil {
			// An unresolvable candidate keeps a floor mass: dropping it would be
			// a silent collapse of the option set.
			weights[string(id)] += priorFloor
			real = append(real, id)
			continue
		}
		w := 0.05 + ent.Salience
		if ent.ID == r.lastSubject {
			w *= 2.5
		}
		if inTopic[ent.ID] {
			w *= 1.5
		}
		if ent.Animate() {
			w *= 1.25
		}
		if ent.MayBeAnimate() && !ent.Animate() {
			// Animacy was never asserted; keep it possible but weaker.
			w *= 0.6
		}
		if ent.Person == 1 && ent.ID == r.speaker {
			w *= 1.4
		}
		weights[string(ent.ID)] += w
		real = append(real, ent.ID)
	}
	if len(weights) == 0 {
		return nil
	}
	d := jlir.NewDistribution("discourse_prior", weights)
	// A single surviving referent carries nothing to be uncertain between; the
	// floor, the UNKNOWN residual and the top cap only matter where two or more
	// candidates compete, which is the situation plan.md §15 is about.
	if len(sortedIDs(uniqueIDs(real))) < 2 {
		return d
	}
	applyPriorShape(d)
	return d
}

// applyPriorShape enforces the floor, the UNKNOWN residual and the top cap that
// keep plan.md §15's 0.56/0.43/0.01 shape alive.
func applyPriorShape(d *jlir.Distribution) {
	if d == nil || len(d.Options) == 0 {
		return
	}
	hasUnknown := d.Prob[UnknownOption] > 0
	for _, o := range d.Options {
		if o == UnknownOption {
			continue
		}
		if d.Prob[o] < priorFloor {
			d.Prob[o] = priorFloor
		}
	}
	if !hasUnknown {
		d.Prob[UnknownOption] = unknownMass
		d.Options = append(d.Options, UnknownOption)
		d.Options = sorted(d.Options)
	}
	d.Normalize()
	top := d.Top(1)
	if len(top) == 0 || top[0].P <= maxPriorTop || top[0].Option == UnknownOption {
		return
	}
	excess := top[0].P - maxPriorTop
	rest := []string{}
	var restP float64
	for _, o := range d.Options {
		if o == top[0].Option {
			continue
		}
		restP += d.Prob[o]
		rest = append(rest, o)
	}
	if restP <= 0 {
		d.Prob[top[0].Option] = maxPriorTop
		d.Normalize()
		return
	}
	for _, o := range rest {
		d.Prob[o] += excess * d.Prob[o] / restP
	}
	d.Prob[top[0].Option] = maxPriorTop
	d.Normalize()
	d.Provenance = "discourse_prior"
}

// Reset empties the registry.
func (r *Registry) Reset() {
	r.entities = map[jlir.ID]*Entity{}
	r.byKey = map[string]jlir.ID{}
	r.byBase = map[string][]jlir.ID{}
	r.local = map[string]jlir.ID{}
	r.current = map[jlir.ID]jlir.ID{}
	r.now, r.seq, r.version = 0, 0, 0
	r.lastSubject, r.speaker, r.listener = "", "", ""
	r.topic = nil
}

// --- identity keys --------------------------------------------------------

// normalizeKey folds a surface form into a comparable identity key: case,
// diacritics and separators are dropped, so "Tōkyō", "Tokyo" and "tokyo" meet.
func normalizeKey(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if f, ok := latinFold[r]; ok {
			b.WriteString(f)
			continue
		}
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\u30fc': // katakana prolonged sound mark: part of the word
			b.WriteRune(r)
		}
	}
	return b.String()
}

var latinFold = map[rune]string{
	'ā': "a", 'á': "a", 'à': "a", 'â': "a", 'ä': "a", 'ã': "a", 'å': "a", 'ă': "a", 'ą': "a",
	'é': "e", 'è': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ĕ': "e", 'ė': "e", 'ę': "e",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i", 'ī': "i", 'į': "i", 'ı': "i",
	'ó': "o", 'ò': "o", 'ô': "o", 'ö': "o", 'õ': "o", 'ō': "o", 'ø': "o", 'ő': "o", 'ơ': "o",
	'ú': "u", 'ù': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ů': "u", 'ű': "u", 'ư': "u",
	'ñ': "n", 'ń': "n", 'ň': "n",
	'ç': "c", 'ć': "c", 'č': "c",
	'ś': "s", 'š': "s", 'ş': "s",
	'ž': "z", 'ź': "z", 'ż': "z",
	'ý': "y", 'ÿ': "y",
	'ß': "ss", 'æ': "ae", 'œ': "oe", 'ð': "d", 'þ': "th",
}

// titles are stripped to build the weak "bare name" key: 山田教授 and 山田 are
// the same person only while the document has exactly one 山田.
var titles = []string{
	"准教授", "教授", "講師", "助教", "主任", "係長", "部長", "課長", "室長", "社長",
	"副社長", "院長", "所長", "館長", "団長", "市長", "村長", "会長", "代表",
	"先生", "博士", "修士", "氏", "君", "様", "さん", "くん", "ちゃん", "殿", "どの",
	"professor", "prof", "doctor", "dr", "mister", "mr", "madam", "mrs", "miss", "ms", "sir", "dame",
}

// baseKeys strips titles and middle particles to get the weak key set.
func baseKeys(keys []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range keys {
		b := stripTitles(k)
		if b == "" || b == k || seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	return out
}

func stripTitles(k string) string {
	changed := true
	for changed {
		changed = false
		for _, t := range titles {
			if len(k) > len(t) && strings.HasSuffix(k, t) {
				k = strings.TrimSuffix(k, t)
				changed = true
				break
			}
		}
	}
	return k
}

// identityKeys collects every key an entity answers to: its asserted identity,
// its names in every language, the canonical form, the reading and the
// transliteration. This is what makes 東京 / Tōkyō / Tokyo one entity (§51).
func identityKeys(l lang.Lang, e *jlir.Entity, g *jlir.Graph) []string {
	var out []string
	add := func(s string) {
		if k := normalizeKey(s); k != "" {
			out = appendUnique(out, k)
		}
	}
	add(e.Identity)
	for _, ne := range namedForms(g, e) {
		add(ne.Canonical)
		add(ne.Reading)
		add(ne.Transliteration)
		for _, form := range ne.Forms {
			add(form)
		}
	}
	for _, a := range e.Aliases {
		switch a.Kind {
		case "pronoun", "zero":
			continue
		}
		add(a.Surface)
	}
	if e.Proper {
		add(e.Alias(l))
		add(canonicalName(l, e))
	}
	return out
}

// strongKeys are the keys on which a merge is allowed without further evidence.
func strongKeys(keys []string) []string { return keys }

// namedForms returns the NamedEntity records attached to a graph entity.
func namedForms(g *jlir.Graph, e *jlir.Entity) []jlir.NamedEntity {
	if g == nil || len(g.NamedEntities) == 0 {
		return nil
	}
	var out []jlir.NamedEntity
	if ne, ok := g.NamedEntities[string(e.ID)]; ok {
		out = append(out, ne)
	}
	for key, ne := range g.NamedEntities {
		if key == string(e.ID) {
			continue
		}
		// A record keyed by an alias, a surface form or an identity string still
		// belongs to this entity.
		if key == e.Identity || hasSurface(e, key) || key == e.Alias(g.Lang) {
			out = append(out, ne)
		}
	}
	return out
}

// hasSurface reports whether a graph entity carries a surface form.
func hasSurface(e *jlir.Entity, s string) bool {
	if e == nil || s == "" {
		return false
	}
	for _, a := range e.Aliases {
		if a.Surface == s {
			return true
		}
	}
	return false
}

// canonicalName picks the name the document should call the entity by: the
// Japanese proper name if there is one, else the English one, else the graph
// language's first name-like alias.
func canonicalName(l lang.Lang, e *jlir.Entity) string {
	if e.Proper {
		if n := e.Name(lang.JA); n != "" {
			return n
		}
		if n := e.Name(lang.EN); n != "" {
			return n
		}
	}
	for _, a := range e.Aliases {
		if a.Kind == "name" || a.Kind == "translation" {
			return a.Surface
		}
	}
	return e.Alias(l)
}

func aliasKind(k string) string {
	if k == "" {
		return "alias"
	}
	return k
}

func orUnknown(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func orKeep(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// --- pronouns -------------------------------------------------------------

type pronoun struct {
	surface       string
	gender        string
	person        int
	demonstrative bool
	plural        bool
}

// pronounForms maps the surface forms that can carry an entity's identity in one
// clause. 「彼」 in sentence 5 is the same referent as 山田教授 in sentence 1.
var pronounForms = map[string]pronoun{
	"彼":   {surface: "彼", gender: jlir.GenderMale},
	"彼氏":  {surface: "彼氏", gender: jlir.GenderMale},
	"彼女":  {surface: "彼女", gender: jlir.GenderFemale},
	"あの人": {surface: "あの人", demonstrative: true},
	"あの方": {surface: "あの方", demonstrative: true},
	"その人": {surface: "その人", demonstrative: true},
	"その方": {surface: "その方", demonstrative: true},
	"この人": {surface: "この人", demonstrative: true},
	"此人":  {surface: "此人", gender: jlir.GenderMale},
	"两人":  {surface: "两人", plural: true},

	"私":    {surface: "私", person: 1},
	"わたし":  {surface: "わたし", person: 1},
	"僕":    {surface: "僕", person: 1},
	"俺":    {surface: "俺", person: 1},
	"僕ら":   {surface: "僕ら", person: 1, plural: true},
	"私たち":  {surface: "私たち", person: 1, plural: true},
	"我々":   {surface: "我々", person: 1, plural: true},
	"あなた":  {surface: "あなた", person: 2},
	"君":    {surface: "君", person: 2},
	"あなた方": {surface: "あなた方", person: 2},
	"君たち":  {surface: "君たち", person: 2},
	"我々達":  {surface: "我々達", person: 1},

	"he":    {surface: "he", gender: jlir.GenderMale},
	"him":   {surface: "him", gender: jlir.GenderMale},
	"his":   {surface: "his", gender: jlir.GenderMale},
	"she":   {surface: "she", gender: jlir.GenderFemale},
	"her":   {surface: "her", gender: jlir.GenderFemale},
	"hers":  {surface: "hers", gender: jlir.GenderFemale},
	"they":  {surface: "they", plural: true},
	"them":  {surface: "them", plural: true},
	"their": {surface: "their", plural: true},
	"i":     {surface: "I", person: 1},
	"me":    {surface: "me", person: 1},
	"my":    {surface: "my", person: 1},
	"we":    {surface: "we", person: 1, plural: true},
	"us":    {surface: "us", person: 1, plural: true},
	"you":   {surface: "you", person: 2},
	"your":  {surface: "your", person: 2},
}

// pronounOf returns the pronoun reading of an entity when every alias it has is
// a pronoun. Anything else — a name, a title — is not a pronoun mention.
func pronounOf(e *Entity, l lang.Lang) pronoun {
	if e == nil || len(e.Aliases) == 0 {
		return pronoun{}
	}
	for _, a := range e.Aliases {
		if _, ok := pronounForms[a.Surface]; !ok {
			return pronoun{}
		}
	}
	// Prefer the most recent pronoun surface.
	a := e.Aliases[len(e.Aliases)-1]
	p := pronounForms[a.Surface]
	p.surface = a.Surface
	if p.plural {
		p.person = 0
	}
	_ = l
	return p
}

// personOf reports the grammatical person of a pronoun mention, or 0.
func personOf(e *Entity, l lang.Lang) int {
	p := pronounOf(e, l)
	return p.person
}

// --- small helpers --------------------------------------------------------

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func appendUniqueIDs(dst []jlir.ID, vs ...jlir.ID) []jlir.ID {
	for _, v := range vs {
		found := false
		for _, x := range dst {
			if x == v {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, v)
		}
	}
	return dst
}

func containsID(s []jlir.ID, v jlir.ID) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func uniqueIDs(s []jlir.ID) []jlir.ID {
	var out []jlir.ID
	for _, v := range s {
		out = appendUniqueIDs(out, v)
	}
	return out
}

func sortIDs(s []jlir.ID) {
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
}

func sortedIDs(s []jlir.ID) []jlir.ID {
	out := uniqueIDs(s)
	sortIDs(out)
	return out
}

func replaceID(s []jlir.ID, from, to jlir.ID) []jlir.ID {
	for i, v := range s {
		if v == from {
			s[i] = to
		}
	}
	return s
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
