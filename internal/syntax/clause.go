// Package syntax defines the analysis contract shared by the Japanese and
// English parsers and consumed by the semantic layer.
//
// The pipeline models a parse as a set of Clauses rather than a bare tree.
// That is deliberate: Japanese case markers and English word order carry the
// same information, and the only thing the JLIR layer needs is which entity
// fills which surface slot plus the morphology that was recovered. Keeping a
// concrete clause record avoids inventing a fully general constituency grammar
// the rest of the system would then have to undo.
//
// Ambiguity is not lost here either: an unresolved attachment is expressed as
// two Clause alternatives or as a slot holding several candidate phrases, and
// the whole clause set is bundled in a Bundle which the semantic layer turns
// into a weighted source semantic forest (plan.md §23, §58).
package syntax

import (
	"sort"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
)

// Conjoin labels the relation of a clause to the one before it.
type Conjoin string

const (
	ConjoinNone     Conjoin = ""
	ConjoinAnd      Conjoin = "AND"
	ConjoinBut      Conjoin = "BUT"
	ConjoinOr       Conjoin = "OR"
	ConjoinBecause  Conjoin = "BECAUSE"
	ConjoinIf       Conjoin = "CONDITION"
	ConjoinWhen     Conjoin = "TEMPORAL_WHEN"
	ConjoinBefore   Conjoin = "TEMPORAL_BEFORE"
	ConjoinAfter    Conjoin = "TEMPORAL_AFTER"
	ConjoinSo       Conjoin = "RESULT"
	ConjoinPurpose  Conjoin = "PURPOSE"
	ConjoinAlthough Conjoin = "CONCESSION"
	ConjoinQuote    Conjoin = "QUOTE"
	ConjoinRelative Conjoin = "RELATIVE"
)

// Phrase is a noun-phrase-like constituent: a head plus pre-head modifiers.
type Phrase struct {
	ID string `json:"id"`
	// Head is the morpheme ID of the head noun. Empty for zero arguments.
	Head string `json:"head,omitempty"`
	// Modifiers are morpheme IDs of pre-head elements (adjectives, relative
	// clause heads, honorifics).
	Modifiers []string `json:"modifiers,omitempty"`
	// Case is the case particle that marked the phrase in Japanese (ga, wo,
	// ni, de, to, kara, made, ...) or "" in English.
	Case string `json:"case,omitempty"`
	// Marked indicates a particle was actually present, as opposed to the
	// phrase being an unmarked argument.
	Marked bool `json:"marked,omitempty"`
	// Topic marks a は-phrase: it is a topic, which is NOT the same as the
	// grammatical subject (plan.md §19).
	Topic bool `json:"topic,omitempty"`
	// Numeral carries a detected quantity.
	Numeral string `json:"numeral,omitempty"`
	// Pronoun is true for pronoun heads.
	Pronoun bool `json:"pronoun,omitempty"`
	// Demonstrative records あの人/その人 style references.
	Demonstrative string `json:"demonstrative,omitempty"`
	// Honorific records a respectful form attached to a referent.
	Honorific string `json:"honorific,omitempty"`
	// Zero marks an argument with no overt realization, which is the normal
	// case for Japanese subjects.
	Zero bool `json:"zero,omitempty"`
	// Relative is a clause serving as a noun modifier.
	Relative *Clause `json:"relative,omitempty"`
	// Probability carries the attachment weight.
	Probability float64   `json:"probability"`
	Span        jlir.Span `json:"span"`
	// Possessors are に-marked possessors folded into the noun phrase.
	Possessors []string `json:"possessors,omitempty"`
	// Conjoined lists coordinated heads (A and B).
	Conjoined []string `json:"conjoined,omitempty"`
	// Notes records parser diagnostics.
	Notes []string `json:"notes,omitempty"`
}

// Clause is one analyzed clause.
type Clause struct {
	ID string `json:"id"`
	// Index is discourse order, 0 for the matrix clause.
	Index int `json:"index"`
	// Conjoin is the relation to the preceding clause.
	Conjoin Conjoin `json:"conjoin,omitempty"`

	// Matrix is the head predicate morpheme. Empty only for fragments.
	Matrix string `json:"matrix,omitempty"`
	// Auxiliaries lists supporting morphemes (English be/have/do, Japanese
	// ている, ました, でしょう).
	Auxiliaries []string `json:"auxiliaries,omitempty"`
	// Negation holds an explicit negative morpheme.
	Negation string `json:"negation,omitempty"`

	// Topic is the は-marked phrase.
	Topic *Phrase `json:"topic,omitempty"`
	// Subject is the が-marked phrase (Japanese) or the pre-verbal NP (English).
	Subject *Phrase `json:"subject,omitempty"`
	// Object is the を-marked phrase (Japanese) or the post-verbal direct object.
	Object *Phrase `json:"object,omitempty"`
	// Indirect holds に/へ-marked phrases (Japanese) or prepositional phrases
	// with an oblique role candidate (English), keyed by surface preposition.
	Indirect map[string]*Phrase `json:"indirect,omitempty"`
	// Adnominals are の-marked noun modifiers that were not folded into a
	// head.
	Adnominals []string `json:"adnominals,omitempty"`
	// Adverbs are sentential or manner adverbs.
	Adverbs []string `json:"adverbs,omitempty"`
	// Modifiers are clause-level (で/形容詞) modifiers.
	Modifiers []string `json:"modifiers,omitempty"`

	// Morphology recovered from inflection.
	Tense         string   `json:"tense"`
	Aspect        string   `json:"aspect,omitempty"`
	Completion    string   `json:"completion,omitempty"`
	Polarity      string   `json:"polarity"`
	Politeness    string   `json:"politeness,omitempty"`
	Mood          string   `json:"mood,omitempty"`
	Modality      string   `json:"modality,omitempty"`
	Honorific     bool     `json:"honorific,omitempty"`
	Evaluative    string   `json:"evaluative,omitempty"`
	SentenceFinal []string `json:"sentenceFinal,omitempty"`
	Voice         string   `json:"voice,omitempty"`
	Causation     string   `json:"causation,omitempty"`

	// Quantifiers holds quantificative nouns detected on the subject
	// (Japanese 皆/全員/誰も, English everyone/no one).
	Quantifier string `json:"quantifier,omitempty"`
	// QuantifiedNP is the phrase the quantifier selects.
	QuantifiedNP string `json:"quantifiedNP,omitempty"`

	// Complement is a clausal complement introduced by to/that/that-clause in
	// English, or a 那样-type complement in Japanese.
	Complement *Clause `json:"complement,omitempty"`

	Span        jlir.Span `json:"span"`
	Probability float64   `json:"probability"`
	// Alternatives holds competing readings of this same span (scope, or
	// attachment of a shared phrase).
	Alternatives []*Clause `json:"alternatives,omitempty"`
	// Notes records parser diagnostics for the UI.
	Notes []string `json:"notes,omitempty"`
}

// IsMatrix reports whether the clause is the sentence's main clause.
func (c *Clause) IsMatrix() bool { return c != nil && c.Index == 0 }

// Packaged structures a whole sentence parse.
type Bundle struct {
	Lang   lang.Lang `json:"lang"`
	Source string    `json:"source"`
	// Clauses are the clauses of the single best parse.
	Clauses []*Clause `json:"clauses"`
	// Forest is the packed syntactic forest for display; its node labels are
	// clause labels and its terminal lexemes are morpheme surfaces.
	Forest *forest.Forest `json:"forest,omitempty"`
	// Morphs is the winning morphological segmentation, indexed by morph ID.
	Morphs map[string]*forest.Morph `json:"morphs"`
	// Coverage is the fraction of characters covered by dictionary morphemes.
	Coverage float64 `json:"coverage"`
	// Unknowns lists reconstructed morphemes, which the pipeline reports
	// rather than hides.
	Unknowns []string `json:"unknowns,omitempty"`
	// Tier records whether a precision grammar (Tier 1) or the robust fallback
	// (Tier 2) produced this parse (plan.md §24).
	Tier  int      `json:"tier"`
	Notes []string `json:"notes,omitempty"`
}

// Morph resolves a morpheme ID.
func (b *Bundle) Morph(id string) *forest.Morph {
	if b == nil || b.Morphs == nil {
		return nil
	}
	return b.Morphs[id]
}

// Text returns the surface of a morpheme ID.
func (b *Bundle) Text(id string) string {
	m := b.Morph(id)
	if m == nil {
		return id
	}
	return m.Text(b.Source)
}

// SortedMorphIDs returns every morpheme ID in surface order, which makes UI
// rendering and deterministic tests possible.
func (b *Bundle) SortedMorphIDs() []string {
	if b == nil {
		return nil
	}
	out := make([]string, 0, len(b.Morphs))
	for id, m := range b.Morphs {
		_ = m
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// PhraseIDs walks a clause and returns every phrase ID it references.
func PhraseIDs(c *Clause) []string {
	if c == nil {
		return nil
	}
	var out []string
	add := func(p *Phrase) {
		if p == nil {
			return
		}
		if p.ID != "" {
			out = append(out, p.ID)
		}
		if p.Relative != nil {
			out = append(out, PhraseIDs(p.Relative)...)
		}
	}
	add(c.Topic)
	add(c.Subject)
	add(c.Object)
	for _, p := range c.Indirect {
		add(p)
	}
	sort.Strings(out)
	return out
}

// AllPhrases returns every non-nil phrase of the bundle in order.
func (b *Bundle) AllPhrases() []*Phrase {
	var out []*Phrase
	var walk func(c *Clause)
	walk = func(c *Clause) {
		if c == nil {
			return
		}
		for _, p := range []*Phrase{c.Topic, c.Subject, c.Object} {
			if p != nil {
				out = append(out, p)
			}
		}
		keys := make([]string, 0, len(c.Indirect))
		for k := range c.Indirect {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if p := c.Indirect[k]; p != nil {
				out = append(out, p)
			}
		}
		if c.Complement != nil {
			walk(c.Complement)
		}
	}
	for _, c := range b.Clauses {
		walk(c)
	}
	return out
}

// Shared contracts for parser implementations. Both languages satisfy this so
// the pipeline can treat a re-parse and a fresh parse identically.
type Parser interface {
	// Parse builds a clause bundle from an already-computed morphological
	// lattice.
	Parse(src string, mf *forest.MorphForest) *Bundle
	// ParseWithTier reports which tier produced the parse.
	ParseWithTier(src string, mf *forest.MorphForest) (*Bundle, int)
}
