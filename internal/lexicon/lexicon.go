// Package lexicon maps surface forms onto predicate senses.
//
// Two rules shape everything here.
//
// First, ambiguity survives analysis. 行く and run both have several readings
// and they are stored as several weighted SenseHits; nothing is collapsed
// before the decision layer (plan.md §10, §33). Weights are priors, not
// decisions: they rank the oracle's options and order the semantic forest, but
// they never remove a candidate.
//
// Second, the tables are plain data. The ontology says what a predicate is,
// the lexicon says which words reach for it. That split is what lets the
// morphological analyzers (internal/lex) and the semantic layer (internal/
// semantics) agree without either of them knowing about the other's tables.
//
// Japanese surfaces are inflected heavily and the analyzer hands us either the
// surface or the de-inflected base, so both are indexed here: base forms get
// their conjugations generated (see jaForms) and an okurigana-tolerant prefix
// search catches everything else. English gets case/whitespace normalisation
// plus a lemma fallback, and a conservative rule-based de-inflector for forms
// the tables do not list.
package lexicon

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/nico2525nn/jev-trans/internal/lex"
	"github.com/nico2525nn/jev-trans/internal/ontology"
)

// SenseHit is one weighted sense candidate for a surface form.
//
// Weight is a prior in [0,1]: how likely this reading is before any context is
// taken into account. It orders candidates and seeds jlir.Distribution; it
// never decides (plan.md §23).
type SenseHit struct {
	SenseID string  `json:"senseId"`
	Weight  float64 `json:"weight"`
	Note    string  `json:"note,omitempty"`
}

// Idiom is a fixed expression that must be detected before its parts are
// committed to literal predicates (plan.md §49). Literal is the sense the
// expression composes to if the idiomatic reading loses, so the semantic layer
// can keep both hypotheses alive instead of forcing the choice.
type Idiom struct {
	ID string `json:"id"`
	// SenseID is the predicate the idiomatic reading selects. It always
	// exists in ontology.Default(); see Validate.
	SenseID string `json:"senseId"`
	// Surface is the idiom as written, used for display.
	Surface string `json:"surface"`
	// Literal is the compositional fallback predicate ("" when the phrase has
	// no literal reading, e.g. "bite the bullet").
	Literal string `json:"literal,omitempty"`
	// Gloss explains the idiom for the decision prompt (plan.md §31: the
	// oracle gets the minimum context that lets it choose).
	Gloss string `json:"gloss"`
	// Formal is the register weight in [0,1]: 0 is slang/colloquial, 1 is
	// formal. The realizer uses it when a style profile is supplied.
	Formal float64 `json:"formal"`
	// Metaphor marks a figurative but compositional expression (plan.md §50),
	// which behaves like an idiom for detection but keeps a real surface
	// reading that the target language may be able to preserve.
	Metaphor bool `json:"metaphor,omitempty"`
	// Lang is "ja" or "en"; idioms are detected per language.
	Lang string `json:"lang"`
}

// Term is one terminology-memory record (plan.md §52): a source term, the
// concept it denotes, the preferred target rendering and the forms the
// realizer must not produce.
type Term struct {
	Source string `json:"source"`
	// Concept is the stable identity shared across languages, so a term is
	// matched on meaning rather than on the surface string alone.
	Concept string `json:"concept"`
	// Preferred is the only accepted target form.
	Preferred string `json:"preferred"`
	// Forbidden lists target forms rejected even if they are otherwise good
	// translations (a house style or a competing standard's wording).
	Forbidden []string `json:"forbidden,omitempty"`
	Note      string   `json:"note,omitempty"`
	// Lang is the language Source is written in.
	Lang string `json:"lang,omitempty"`
}

// Stats is what the UI shows about the knowledge base (plan.md §11: a system
// that does not know what it does not know cannot calibrate).
type Stats struct {
	Senses int `json:"senses"` // senses in the ontology
	// ReachableSenses is how many of them the tables actually reach. The gap
	// between it and Senses is the honest measure of what the lexicon does not
	// cover yet, which is what plan.md §11 asks the UI to show.
	ReachableSenses int `json:"reachableSenses"`
	SenseHits       int `json:"senseHits"`  // surface → sense links
	JPSurfaces      int `json:"jpSurfaces"` // Japanese surface forms indexed
	ENSurfaces      int `json:"enSurfaces"` // English surface forms indexed
	Idioms          int `json:"idioms"`
	Terms           int `json:"terms"`
	Families        int `json:"families"`
	// UnknownSenseIDs counts links whose SenseID is absent from the ontology.
	// It must be 0; Validate reports the offending IDs.
	UnknownSenseIDs int `json:"unknownSenseIds"`
}

// Lexicon is the immutable surface→sense knowledge base. Lookups return copies
// of the stored slices so callers cannot corrupt the shared table.
type Lexicon struct {
	reg    *ontology.Registry
	jp     map[string][]SenseHit
	en     map[string][]SenseHit
	idioms map[string]*Idiom
	terms  map[string]*Term
	// unknown holds SenseIDs referenced by the tables but missing from the
	// ontology. Populated once by Default and reported by Stats.
	unknown []string
	// senses counts the distinct senses the tables actually reach, which is a
	// far more honest number than the ontology size.
	senses int
	// senseHits counts surface→sense links.
	senseHits int
}

var (
	shared     *Lexicon
	sharedOnce bool
)

// Default returns the process-wide lexicon. The tables are large and immutable,
// so they are built once and shared; callers must treat the result as read-only
// (the accessors already return copies).
func Default() *Lexicon {
	if sharedOnce {
		return shared
	}
	l := build()
	shared, sharedOnce = l, true
	return l
}

func build() *Lexicon {
	reg := ontology.Default()
	l := &Lexicon{
		reg:    reg,
		jp:     make(map[string][]SenseHit, 4096),
		en:     make(map[string][]SenseHit, 4096),
		idioms: make(map[string]*Idiom, 128),
		terms:  make(map[string]*Term, 64),
	}

	// --- one morphological dictionary --------------------------------------
	//
	// Every verb this table knows becomes a dictionary entry in internal/lex, so
	// the analyzer can segment its inflected forms. The two tables used to be
	// independent: the predicate lexicon held 241 verb bases and the analyzer
	// knew the conjugations of its own 836 surfaces, so a verb in one and not
	// the other could not be segmented at all. On real Aozora prose that was
	// the largest single cause of failure — 住っていた, 飛んだ and 起きた all
	// came out as unresolved fragments with no predicate.
	//
	// Registration happens here rather than at package init because this
	// package's tables are built lazily, and the analyzer's index is built in
	// its own init; RegisterJapaneseVerb rebuilds the index.
	for _, part := range jaTables {
		for _, e := range part {
			if looksLikeJapaneseVerb(e.Base, e.Spec) {
				lex.RegisterJapaneseVerb(e.Base)
			}
		}
	}

	// --- Japanese -----------------------------------------------------------
	// Base forms expand into their conjugations so that SensesJP("帰った") and
	// SensesJP("帰る") both resolve without the caller de-inflecting first.
	for _, part := range jaTables {
		for _, e := range part {
			hits := parseSpec(e.Spec, "ja:"+e.Base)
			if len(hits) == 0 {
				continue
			}
			// The conjugations come from the analyzer (lex.JAConjugations), not
			// from a second engine here. This package used to keep its own
			// jaForms / godanForms copy of the Japanese conjugation tables, and
			// the two copies disagreed: the copy hardcoded the く-row te-form as
			// いて, so 行く got 行いて indexed beside the correct 行って. One
			// engine, one answer.
			//
			// A row whose base the analyzer does not know contributes only its
			// explicit Forms list. That is a smaller index than before and an
			// accurate one; the way to widen it is to add the verb to the
			// analyzer's tables, not to re-derive the forms here.
			surfaces := make([]string, 0, 32)
			surfaces = append(surfaces, e.Base)
			surfaces = append(surfaces, e.Forms...)
			surfaces = append(surfaces, lex.JAConjugations(e.Base)...)
			for _, s := range surfaces {
				if s == "" {
					continue
				}
				l.jp[s] = mergeHits(l.jp[s], hits)
			}
		}
	}

	// --- English ------------------------------------------------------------
	for _, part := range enTables {
		for _, e := range part {
			hits := parseSpec(e.Spec, "en:"+e.Base)
			if len(hits) == 0 {
				continue
			}
			surfaces := append([]string{e.Base}, e.Forms...)
			for _, s := range surfaces {
				if s == "" {
					continue
				}
				k := normalizeEN(s)
				l.en[k] = mergeHits(l.en[k], hits)
			}
			// Regular inflections, generated. Explicit forms above win because
			// mergeHits keeps the higher weight and dedupes by SenseID.
			for _, f := range enForms(e.Base) {
				k := normalizeEN(f)
				if k == "" {
					continue
				}
				l.en[k] = mergeHits(l.en[k], hits)
			}
		}
	}

	// --- Idioms and terminology --------------------------------------------
	for _, id := range idioms {
		c := *id
		c.ID = defaultString(c.ID, "IDIOM."+strings.ToUpper(normalizeKey(c.Surface)))
		if c.Surface == "" && id.ID != "" {
			c.Surface = id.ID
		}
		l.idioms[idiomKey(c.Lang, c.Surface)] = &c
		// An idiom is also a lexical unit, so its predicate has to be reachable
		// through Senses* as well; otherwise the analyzer drops it as an
		// unknown token before the idiom pass ever runs (plan.md §49 wants the
		// idiom detected before composition, not instead of tokenization).
		hits := []SenseHit{{SenseID: c.SenseID, Weight: 0.9, Note: "idiom " + c.ID}}
		if c.Lang == "ja" {
			l.jp[c.Surface] = mergeHits(l.jp[c.Surface], hits)
		} else {
			k := normalizeEN(c.Surface)
			l.en[k] = mergeHits(l.en[k], hits)
		}
	}
	for _, t := range termTable {
		c := *t
		l.terms[termKey(c.Lang, c.Source)] = &c
	}

	// ---------------------------------------------------------------------
	// CONSISTENCY CHECK — this is the one place it belongs.
	//
	// Every SenseID in the JP table, the EN table and the idiom table (with
	// its literal fallback) must exist in the ontology, because
	// jlir.Event.Predicate is looked up by ID and a dangling one degrades to a
	// synthetic permissive sense that silently loses the role frame. The check
	// runs once here rather than in a test because the tables are data: a bad
	// link is a build-time fact, not a runtime condition. Validate reports the
	// offending IDs and Stats counts them.
	// ---------------------------------------------------------------------
	l.unknown = l.unknownSenseIDs()
	l.senses = l.reachableSenses()
	for _, hits := range l.jp {
		l.senseHits += len(hits)
	}
	for _, hits := range l.en {
		l.senseHits += len(hits)
	}
	return l
}

// Validate reports every SenseID the tables reference that the ontology does
// not define. It returns nil for a consistent lexicon, which is the only
// acceptable state.
func (l *Lexicon) Validate() error {
	if l == nil {
		return nil
	}
	if len(l.unknown) == 0 {
		return nil
	}
	return &ValidationError{IDs: append([]string(nil), l.unknown...)}
}

// ValidationError lists dangling SenseIDs.
type ValidationError struct{ IDs []string }

func (e *ValidationError) Error() string {
	return "lexicon references unknown senses: " + strings.Join(e.IDs, ", ")
}

func (l *Lexicon) unknownSenseIDs() []string {
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := l.reg.Sense(id); !ok {
			seen[id] = true
		}
	}
	for _, hits := range l.jp {
		for _, h := range hits {
			add(h.SenseID)
		}
	}
	for _, hits := range l.en {
		for _, h := range hits {
			add(h.SenseID)
		}
	}
	for _, id := range l.idioms {
		add(id.SenseID)
		add(id.Literal)
	}
	// Family roots are legitimate predicates too (RUN is declared as a sense),
	// so nothing else to validate here.
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (l *Lexicon) reachableSenses() int {
	seen := map[string]bool{}
	for _, hits := range l.jp {
		for _, h := range hits {
			seen[h.SenseID] = true
		}
	}
	for _, hits := range l.en {
		for _, h := range hits {
			seen[h.SenseID] = true
		}
	}
	for _, id := range l.idioms {
		seen[id.SenseID] = true
	}
	return len(seen)
}

// SensesJP returns the weighted sense candidates for a Japanese surface form,
// or nil when the form is not in the lexicon.
//
// The lookup is an exact surface match, and nothing else. Inflected forms are
// in the index because they were generated from their base form by the
// analyzer's own conjugation tables (see build), so a surface that is not
// registered really is a form this lexicon has no predicate for.
//
// It used to fall back to a longest-prefix search over registered bases, with
// no check on the remainder it dropped. That made it reinterpret nouns as
// verbs: 行く先 ("destination") resolved to MOVE.01 and 買った本 ("the book I
// bought") to TRANSFER.05, because both begin with a registered verb. A
// lookup that answers a question about a word with a reading of a different
// word is worse than no answer (plan.md §25: unresolved beats invented), so the
// fallback is gone rather than restricted.
func (l *Lexicon) SensesJP(surface string) []SenseHit {
	if l == nil || surface == "" {
		return nil
	}
	if hits, ok := l.jp[surface]; ok {
		return copyHits(hits)
	}
	return nil
}

// SensesEN returns the weighted sense candidates for an English surface form,
// or nil when the form is not in the lexicon.
//
// Lookup order: normalised surface, then the lemma fallback (irregular forms
// are listed explicitly in the table), then a conservative de-inflector.
// "running" therefore reaches run's readings whether the tokenizer lemmatized
// it or not.
func (l *Lexicon) SensesEN(surface string) []SenseHit {
	if l == nil || surface == "" {
		return nil
	}
	k := normalizeEN(surface)
	if hits, ok := l.en[k]; ok {
		return copyHits(hits)
	}
	for _, c := range enCandidates(k) {
		if hits, ok := l.en[c]; ok {
			return copyHits(hits)
		}
	}
	return nil
}

// Idiom returns the idiom entry for a surface form. The lookup is
// language-agnostic on the key: Japanese keys are matched after stripping
// surrounding punctuation, English keys after normalisation. A dummy subject
// ("it is raining cats and dogs") is tolerated because idioms routinely appear
// with one and the surface stored here is the idiomatic core.
func (l *Lexicon) Idiom(surface string) (*Idiom, bool) {
	if l == nil || surface == "" {
		return nil, false
	}
	if id, ok := l.idioms["ja"+"\x00"+trimJP(surface)]; ok {
		return id, true
	}
	if id, ok := l.idioms["en"+"\x00"+normalizeEN(surface)]; ok {
		return id, true
	}
	// Substring form for multiword expressions: "it is raining cats and dogs".
	for _, c := range stripDummySubject(normalizeEN(surface)) {
		if id, ok := l.idioms["en"+"\x00"+c]; ok {
			return id, true
		}
	}
	// Japanese: an idiom embedded in a longer span, longest match first.
	trimmed := trimJP(surface)
	for n := len(trimmed) - 1; n >= 2; n-- {
		for start := 0; start+n <= len(trimmed); start++ {
			if id, ok := l.idioms["ja"+"\x00"+trimmed[start:start+n]]; ok {
				return id, true
			}
		}
	}
	return nil, false
}

// Idioms returns every idiom sorted by ID.
func (l *Lexicon) Idioms() []*Idiom {
	if l == nil {
		return nil
	}
	out := make([]*Idiom, 0, len(l.idioms))
	for _, id := range l.idioms {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Term returns the terminology record for a source term (plan.md §52).
func (l *Lexicon) Term(source string) (*Term, bool) {
	if l == nil || source == "" {
		return nil, false
	}
	if t, ok := l.terms["ja"+"\x00"+trimJP(source)]; ok {
		return t, true
	}
	if t, ok := l.terms["en"+"\x00"+normalizeEN(source)]; ok {
		return t, true
	}
	return nil, false
}

// Terms returns every terminology record sorted by source.
func (l *Lexicon) Terms() []*Term {
	if l == nil {
		return nil
	}
	out := make([]*Term, 0, len(l.terms))
	for _, t := range l.terms {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// Registry exposes the ontology the lexicon points into, so callers do not have
// to hold two references.
func (l *Lexicon) Registry() *ontology.Registry {
	if l == nil {
		return nil
	}
	return l.reg
}

// Stats reports the coverage of the knowledge base.
func (l *Lexicon) Stats() Stats {
	if l == nil {
		return Stats{}
	}
	rs := l.reg.Stats()
	jp := make(map[string]bool, len(l.jp))
	for k := range l.jp {
		jp[k] = true
	}
	en := make(map[string]bool, len(l.en))
	for k := range l.en {
		en[k] = true
	}
	return Stats{
		Senses:          rs.Senses,
		SenseHits:       l.senseHits,
		JPSurfaces:      len(jp),
		ENSurfaces:      len(en),
		Idioms:          len(l.idioms),
		Terms:           len(l.terms),
		Families:        rs.Families,
		UnknownSenseIDs: len(l.unknown),
	}
}

// -----------------------------------------------------------------------------
// Table representation
// -----------------------------------------------------------------------------

// entry is one row of a surface table. Spec holds compact "ID:weight" items
// separated by whitespace or commas, e.g. "MOVE.01:0.9 MOVE.04:0.15".
//
// There is deliberately no conjugation-class field. A Japanese row used to
// carry one (v1, v5u, adj, …) and this package generated the forms from it with
// its own copy of the conjugation tables. That copy drifted from the analyzer's
// — the く-row te-form was hardcoded as いて, so 行く got 行いて indexed — and
// the class annotations drifted too: 帰る was labelled ichidan, which indexed
// 帰た, 帰ます and 帰たい for a godan る verb.
//
// The analyzer now owns the class (lex.JAConjugations) and this table owns only
// the senses, which is the split plan.md §10 asks for. Forms lists the
// surfaces no forward table produces.
type entry struct {
	Base  string // Japanese dictionary form, or English lemma
	Forms []string
	Spec  string
}

// parseSpec turns a compact spec string into weighted hits. It panics on a
// malformed entry: a broken data row is a build-time bug, and failing loudly at
// init beats a sense that silently vanishes at runtime.
func parseSpec(spec, where string) []SenseHit {
	var out []SenseHit
	for _, item := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ','
	}) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		id, w := item, 1.0
		if i := strings.LastIndex(item, ":"); i > 0 {
			id = item[:i]
			v, err := strconv.ParseFloat(item[i+1:], 64)
			if err != nil || v < 0 || v > 1 {
				panic("lexicon: bad weight in " + where + ": " + item)
			}
			w = v
		}
		if id == "" {
			panic("lexicon: empty sense id in " + where)
		}
		out = append(out, SenseHit{SenseID: id, Weight: w})
	}
	return out
}

// mergeHits unions two hit lists, keeping the higher weight per SenseID and
// preserving descending weight order so the answer is deterministic.
func mergeHits(a, b []SenseHit) []SenseHit {
	if len(a) == 0 {
		return append([]SenseHit(nil), b...)
	}
	if len(b) == 0 {
		return append([]SenseHit(nil), a...)
	}
	best := make(map[string]SenseHit, len(a)+len(b))
	order := make([]string, 0, len(a)+len(b))
	for _, list := range [][]SenseHit{a, b} {
		for _, h := range list {
			if prev, ok := best[h.SenseID]; ok {
				if h.Weight > prev.Weight {
					best[h.SenseID] = h
				}
				continue
			}
			best[h.SenseID] = h
			order = append(order, h.SenseID)
		}
	}
	out := make([]SenseHit, 0, len(order))
	for _, id := range order {
		out = append(out, best[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].SenseID < out[j].SenseID
	})
	return out
}

func copyHits(h []SenseHit) []SenseHit {
	if h == nil {
		return nil
	}
	return append([]SenseHit(nil), h...)
}

// -----------------------------------------------------------------------------
// Key normalisation
// -----------------------------------------------------------------------------

// normalizeEN lowercases, collapses whitespace and strips the punctuation that
// a tokenizer leaves behind. It deliberately does not stem: stemming would
// merge "flies" and "fly" away from the predicate, and the tables are small
// enough to afford exact keys plus a lemma fallback.
func normalizeEN(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '-' || r == '_':
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		default:
			// Punctuation is a separator, not part of the key.
			space = b.Len() > 0
		}
	}
	return b.String()
}

func normalizeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToUpper(r))
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// trimJP strips Japanese punctuation and whitespace from both ends. The
// interior of a Japanese expression is never normalized: 雪が降る and 雪が降る
// differ only in okurigana handling, not in width.
func trimJP(s string) string {
	return strings.Trim(s, " \t\u3000、。「」『』（）〈〉《》…,.!?;:'\"·・")
}

func idiomKey(lang, surface string) string {
	l := lang
	if l != "ja" {
		l = "en"
	}
	if l == "ja" {
		return "ja\x00" + trimJP(surface)
	}
	return "en\x00" + normalizeEN(surface)
}

func termKey(lang, source string) string {
	l := lang
	if l != "ja" {
		l = "en"
	}
	if l == "ja" {
		return "ja\x00" + trimJP(source)
	}
	return "en\x00" + normalizeEN(source)
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// stripDummySubject removes a leading "it is"/"it's"/"they are" so that the
// idiomatic core is found. It returns the original string first.
func stripDummySubject(s string) []string {
	out := []string{s}
	for _, p := range []string{"it is ", "it's ", "it was ", "they are ", "they're ", "he is ", "she is "} {
		if strings.HasPrefix(s, p) && len(s) > len(p) {
			out = append(out, strings.TrimSpace(s[len(p):]))
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// English inflection
// -----------------------------------------------------------------------------

// enForms generates the regular inflections of a lemma: third person singular,
// progressive and past. Irregular forms are listed explicitly in the table, so
// this only has to cover the productive pattern.
func enForms(lemma string) []string {
	l := normalizeEN(lemma)
	if l == "" || strings.Contains(l, " ") {
		return nil
	}
	var out []string
	switch l[len(l)-1] {
	case 's', 'x', 'z', 'o':
		out = append(out, l+"es")
	case 'y':
		if len(l) > 1 && !isVowel(rune(l[len(l)-2])) {
			out = append(out, l[:len(l)-1]+"ies")
		} else {
			out = append(out, l+"s")
		}
	default:
		out = append(out, l+"s")
	}
	out = append(out, l+"ing", l+"ed")
	// Consonant doubling: run → running. Only where the final syllable is
	// stressed-ish short; the heuristic is the standard one.
	if len(l) >= 3 {
		final := rune(l[len(l)-1])
		prev := rune(l[len(l)-2])
		if isConsonant(final) && isConsonant(prev) && !isVowel(rune(l[len(l)-3])) && !endsWithVowelGroup(final, prev) {
			dbl := string(final)
			out = append(out, l+dbl+"ing", l+dbl+"ed")
		}
	}
	// e-drop: make → making.
	if len(l) >= 2 && l[len(l)-1] == 'e' {
		out = append(out, l+"ing", l[:len(l)-1]+"ing", l+"ed", l[:len(l)-1]+"d")
	}
	// y → i: try → trying.
	if len(l) >= 3 && l[len(l)-1] == 'y' && !isVowel(rune(l[len(l)-2])) {
		st := l[:len(l)-1]
		out = append(out, st+"ing", st+"ied")
	}
	return out
}

// endsWithVowelGroup reports whether final is preceded by a vowel+diphthong
// pattern that must not be doubled (e.g. "ay", "oy", "ey", "y").
func endsWithVowelGroup(final, prev rune) bool {
	return false || isVowel(final) || isVowel(prev)
}

func isVowel(r rune) bool {
	switch r {
	case 'a', 'e', 'i', 'o', 'u', 'y':
		return true
	}
	return false
}

func isConsonant(r rune) bool { return r > 0 && !isVowel(r) }

// enCandidates proposes lemmas for an unknown English surface form, most
// likely first. It is deliberately conservative: a wrong guess is worse than no
// candidate because it collapses the ambiguity the pipeline exists to keep.
func enCandidates(k string) []string {
	if k == "" || strings.Contains(k, " ") {
		return nil
	}
	var out []string
	add := func(s string) {
		if s != "" && s != k {
			for _, e := range out {
				if e == s {
					return
				}
			}
			out = append(out, s)
		}
	}
	// Reversed irregulars.
	if l, ok := enIrregularReverse[k]; ok {
		add(l)
	}
	switch {
	case strings.HasSuffix(k, "ing"):
		st := k[:len(k)-3]
		add(st)
		add(st + "e")
		if len(st) >= 2 && !isVowel(rune(st[len(st)-1])) && isConsonant(rune(st[len(st)-2])) {
			add(st[:len(st)-1])
		}
	case strings.HasSuffix(k, "ies"):
		add(k[:len(k)-3] + "y")
	case strings.HasSuffix(k, "ied"):
		add(k[:len(k)-3] + "y")
	case strings.HasSuffix(k, "es"):
		add(k[:len(k)-2])
		add(k[:len(k)-1])
	case strings.HasSuffix(k, "ed"):
		st := k[:len(k)-2]
		add(st)
		add(st + "e")
		if len(st) >= 2 && !isVowel(rune(st[len(st)-1])) && isConsonant(rune(st[len(st)-2])) {
			add(st[:len(st)-1])
		}
	case strings.HasSuffix(k, "s"):
		add(k[:len(k)-1])
		add(k[:len(k)-2])
	}
	return out
}

// enIrregularReverse maps an inflected form to its lemma. Built once from
// enIrregular, which is the table of irregular verb forms.
var enIrregularReverse = func() map[string]string {
	m := make(map[string]string, len(enIrregular)*4)
	for _, row := range enIrregular {
		lemma, forms := row.Base, row.Forms
		if lemma == "" || len(forms) == 0 {
			continue
		}
		m[normalizeEN(lemma)] = lemma
		for _, f := range forms {
			m[normalizeEN(f)] = lemma
		}
	}
	return m
}()

// verbFamilies are the ontology families that denote events. A dictionary
// base whose senses are all eventive is a verb; anything else is a noun.
//
// This replaced a hand-written list of leading kanji, which was both
// incomplete (住, 起 and 眠 were missed) and arbitrary. Deriving the
// predicate class from the predicate inventory means a base added to the
// lexicon is classified the moment its senses are, with no second list to
// keep in step.
var verbFamilies = map[string]bool{
	"MOVE": true, "RUN": true, "CHANGE": true, "COMMUNICATE": true,
	"MENTAL": true, "PERCEIVE": true, "FEEL": true, "SLEEP": true,
	"WEATHER": true, "CONSUME": true, "TRANSFER": true, "HAVE": true,
	"EXIST": true, "WORK": true, "MEET": true, "MODAL": true,
}

// looksLikeJapaneseVerb reports whether a dictionary base is a verb, decided
// by the ontology families its senses belong to rather than by its spelling.
func looksLikeJapaneseVerb(base string, spec string) bool {
	if base == "" {
		return false
	}
	for _, hit := range parseSpec(spec, "probe") {
		if verbFamilies[familyOfSense(hit.SenseID)] {
			return true
		}
	}
	return false
}

// familyOfSense returns the ontology family of a sense id ("TRANSFER.01" ->
// "TRANSFER"). It uses the registry rather than string surgery so a family
// name is never misspelled.
func familyOfSense(id string) string {
	s, ok := ontology.Default().Sense(id)
	if !ok {
		if i := strings.Index(id, "."); i > 0 {
			return id[:i]
		}
		return id
	}
	if p := s.Parent(); p != "" {
		return p
	}
	return s.ID
}
