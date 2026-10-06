package syntax

// English clause parsing.
//
// The parser is deliberately shallow: it recovers who did what to whom, when,
// with what modality, and it stops there. That is all the semantic layer needs,
// and it is all plan.md §23 and §24 ask for — a packed analysis forest rather
// than a single tree, with a Tier 1 precision pass and a Tier 2 robust
// fallback for whatever Tier 1 cannot reach.
//
// Every decision that the surface does not force is preserved as a Clause
// alternative rather than resolved (plan.md §17): a be + participle sequence
// yields PASSIVE *and* ACTIVE, an NP with an embedded prepositional phrase
// yields two subjects, and a negated clause with an infinitival complement
// yields both scopes. Nothing here invents gender, number, tense or discourse
// role that the surface does not carry (plan.md §4, §47).

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nico2525nn/jev-trans/internal/forest"
	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/trace"
)

// --- lexical knowledge the clause layer needs ----------------------------

// enConjoinOf maps a coordinating or subordinating word to the clause relation
// the semantic layer records.
var enConjoinOf = map[string]Conjoin{
	"and": ConjoinAnd, "or": ConjoinOr, "but": ConjoinBut,
	"because": ConjoinBecause, "since": ConjoinBecause,
	"if": ConjoinIf, "unless": ConjoinIf,
	"when": ConjoinWhen, "while": ConjoinWhen, "once": ConjoinWhen,
	"before": ConjoinBefore, "until": ConjoinAfter, "till": ConjoinAfter,
	"after":    ConjoinAfter,
	"so":       ConjoinSo,
	"although": ConjoinAlthough, "though": ConjoinAlthough, "whereas": ConjoinAlthough,
}

// enModalityLabel names the modality a modal contributes. Modality is the one
// place where English forces a Japanese translation to *add* information, so
// the parser records the source modal verbatim and leaves the choice of the
// Japanese form to the projection stage.
var enModalityLabel = map[string]string{
	"must": "NECESSITY", "ought": "OBLIGATION", "should": "OBLIGATION",
	"need": "OBLIGATION", "may": "PERMISSION", "might": "POSSIBILITY",
	"could": "POSSIBILITY", "can": "ABILITY", "will": "FUTURE",
	"shall": "FUTURE", "would": "COUNTERFACTUAL", "dare": "CAPACITY",
}

// enIntentionVerbs are the verbs whose to-infinitive is a genuine complement
// rather than a purpose adjunct.
var enIntentionVerbs = map[string]bool{
	"want": true, "plan": true, "try": true, "intend": true, "hope": true,
	"decide": true, "need": true, "love": true, "like": true, "hate": true,
	"offer": true, "refuse": true, "promise": true, "threaten": true,
	"afford": true, "manage": true, "fail": true, "agree": true,
	"expect": true, "learn": true, "mean": true, "ask": true, "tell": true,
}

// enComplementizerVerbs introduce a that-clause complement. A "that" after
// one of these is a complementizer; anywhere else it is a relative pronoun, and
// the two produce different JLIR structures.
var enComplementizerVerbs = map[string]bool{
	"say": true, "think": true, "know": true, "believe": true, "hope": true,
	"see": true, "find": true, "show": true, "explain": true, "feel": true,
	"hear": true, "guess": true, "suggest": true, "report": true, "admit": true,
	"remember": true, "forget": true, "decide": true, "agree": true, "claim": true,
	"argue": true, "reply": true, "answer": true, "warn": true, "promise": true,
	"notice": true, "mention": true, "prove": true, "realize": true, "understand": true,
}

// enRelativeOpeners are the words that can start a relative clause.
var enRelativeOpeners = map[string]bool{
	"that": true, "which": true, "who": true, "whom": true, "whose": true, "where": true,
}

// enMassNouns are uncountable in the sense English actually enforces: no
// article, no plural -s, a different verb complementation pattern. The
// projection layer needs to know this before it emits a Japanese phrase
// (plan.md §46).
var enMassNouns = map[string]bool{
	"advice": true, "bread": true, "butter": true, "cheese": true, "coffee": true,
	"evidence": true, "feedback": true, "furniture": true, "hair": true,
	"help": true, "homework": true, "information": true, "knowledge": true,
	"luggage": true, "milk": true, "money": true, "music": true, "news": true,
	"progress": true, "rice": true, "sand": true, "snow": true, "software": true,
	"tea": true, "traffic": true, "water": true, "weather": true, "work": true,
	"air": true, "beauty": true, "courage": true, "damage": true, "education": true,
	"electricity": true, "energy": true, "equipment": true, "fruit": true,
	"meat": true, "paper": true, "research": true, "sugar": true, "travel": true,
}

// enIrregularPlurals let the parser know that "children" and "men" are plural
// even though they do not end in -s. English forces number agreement on the
// determiner and the verb, so this has to be known here, not guessed later.
var enIrregularPlurals = map[string]string{
	"children": "child", "people": "person", "men": "man", "women": "woman",
	"teeth": "tooth", "feet": "foot", "mice": "mouse", "geese": "goose",
	"lives": "life", "wives": "wife", "knives": "knife", "leaves": "leaf",
	"wolves": "wolf", "halves": "half", "shelves": "shelf", "thieves": "thief",
	"loaves": "loaf", "selves": "self", "calves": "calf",
}

// --- token predicates ----------------------------------------------------

func enIsPunct(m *forest.Morph) bool { return m.POS == forest.POSPunct }
func enIsComma(m *forest.Morph) bool { return m.POS == forest.POSPunct && m.Surface == "," }
func enIsDet(m *forest.Morph) bool   { return m.POS == forest.POSDet }
func enIsAdj(m *forest.Morph) bool   { return m.POS == forest.POSAdj }
func enIsNum(m *forest.Morph) bool   { return m.POS == forest.POSNum }
func enIsPrep(m *forest.Morph) bool  { return m.POS == forest.POSPrep }
func enIsParticipleTo(m *forest.Morph) bool {
	return m.POS == forest.POSParticle && m.Feat("infinitive") == "true"
}
func enIsAdv(m *forest.Morph) bool   { return m.POS == forest.POSAdv }
func enIsConj(m *forest.Morph) bool  { return m.POS == forest.POSConj }
func enIsNeg(m *forest.Morph) bool   { return m.Feat("negative") == "true" }
func enIsModal(m *forest.Morph) bool { return m.Feat("modal") == "true" }

func enIsNounish(m *forest.Morph) bool {
	switch m.POS {
	case forest.POSNoun, forest.POSProper, forest.POSPronoun, forest.POSNum:
		return true
	}
	return false
}

func enIsVerbLike(m *forest.Morph) bool {
	return m.POS == forest.POSVerb || m.POS == forest.POSAux
}

// enIsPossessiveClitic is the 's of "Taro's". It is tagged as an auxiliary
// because that is the only clitic class the POS set has, so every verb scan
// has to filter it out explicitly or "Taro's book" parses as a verb phrase.
func enIsPossessiveClitic(m *forest.Morph) bool {
	return m.Feat("possessive") == "true" && m.POS == forest.POSAux
}

func enIsPossessiveDet(m *forest.Morph) bool {
	return m.POS == forest.POSDet && m.Feat("possessive") == "true"
}

func enIsCopula(m *forest.Morph) bool { return m.Feat("copula") == "true" }

func enIsHave(m *forest.Morph) bool {
	return m.Lem == "have" || m.Lem == "has" || m.Lem == "had"
}

func enIsParticiple(m *forest.Morph) bool { return m.Feat("part") == "participle" }

func enIsWord(m *forest.Morph, w string) bool {
	return strings.EqualFold(m.Surface, w)
}

func enIsLeftModifier(m *forest.Morph) bool {
	return enIsDet(m) || enIsAdj(m) || enIsNum(m)
}

func enOnlyPunctuation(w []*forest.Morph) bool {
	for _, m := range w {
		if !enIsPunct(m) {
			return false
		}
	}
	return len(w) > 0
}

// --- parser --------------------------------------------------------------

// ENParser implements Parser for English.
type ENParser struct{}

// Parse builds the clause bundle.
func (ENParser) Parse(src string, mf *forest.MorphForest) *Bundle { return ParseEN(src, mf) }

// ParseWithTier reports which tier produced the parse.
func (ENParser) ParseWithTier(src string, mf *forest.MorphForest) (*Bundle, int) {
	return parseEN(src, mf)
}

// ParseEN analyses the winning morphological path of mf into clauses.
func ParseEN(src string, mf *forest.MorphForest) *Bundle {
	b, _ := parseEN(src, mf)
	return b
}

type enParser struct {
	src     string
	bundle  *Bundle
	toks    []*forest.Morph
	clauseN int
	phraseN int
}

// enSeg is one clause-sized span of the token stream, found before any role is
// assigned.
type enSeg struct {
	from, to int
	conjoin  Conjoin
	relative bool
	relHead  string
}

// enNP is a noun phrase located in the token stream, with the internal
// structure the projection stage needs: pre-head modifiers, a possessive
// possessor, and prepositional phrases that are *inside* the NP rather than
// adjuncts to the clause.
type enNP struct {
	Start, End   int
	Head         int
	Modifiers    []int
	Possessors   [][2]int
	InternalPPs  [][3]int
	ok           bool
	alternatives []enNPAlt
}

// enNPAlt is a competing analysis of the same span, used when a noun phrase
// boundary is genuinely uncertain.
type enNPAlt struct {
	NP  enNP
	Why string
}

func parseEN(src string, mf *forest.MorphForest) (*Bundle, int) {
	span := enParseSpan()
	if span != nil {
		defer span.Close()
	}

	b := &Bundle{Lang: lang.EN, Source: src, Morphs: map[string]*forest.Morph{}}
	p := &enParser{src: src, bundle: b}

	if mf != nil {
		for _, m := range mf.Best().Morphs {
			if m == nil || strings.TrimSpace(m.Surface) == "" {
				continue
			}
			c := *m
			c.ID = fmt.Sprintf("m%d", len(b.Morphs)+1)
			c.Alternatives = append([]string(nil), m.Alternatives...)
			c.Notes = append([]string(nil), m.Notes...)
			cp := &c
			b.Morphs[c.ID] = cp
			p.toks = append(p.toks, cp)
		}
	}

	if len(p.toks) == 0 {
		b.Tier = 2
		b.Notes = append(b.Notes, "no morphemes available: the clause layer received an empty morphological analysis")
		b.Forest = nil
		if span != nil {
			span.Status(trace.StatusSkip)
			span.Detail("nothing to parse")
		}
		return b, 2
	}

	clauses := p.analyze()
	if len(clauses) == 0 {
		clauses = append(clauses, &Clause{
			ID:          p.nextClauseID(),
			Polarity:    "POS",
			Span:        jlir.Span{Start: p.toks[0].Start, End: p.toks[len(p.toks)-1].End},
			Notes:       []string{"no verb was found: the input is a fragment, not a clause"},
			Probability: 1,
		})
	}
	enAssignIndexes(clauses)
	b.Clauses = clauses

	// Coverage and unknowns are computed over the winning segmentation: the
	// fraction of the input's non-space characters that a dictionary entry
	// accounts for.
	total, dict := 0, 0
	for _, m := range b.Morphs {
		n := m.End - m.Start
		if n < 0 {
			n = 0
		}
		total += n
		if m.Dict {
			dict += n
		} else {
			b.Unknowns = append(b.Unknowns, m.Surface)
		}
	}
	if total > 0 {
		b.Coverage = float64(dict) / float64(total)
	}

	b.Tier = enTier(b)
	b.Forest = p.buildForest(clauses)
	b.Notes = append(b.Notes, enBundleNotes(b)...)

	if span != nil {
		span.Data(b)
		span.Count("clauses", len(clauses))
		span.Count("phrases", len(b.AllPhrases()))
		if b.Tier == 2 {
			span.Status(trace.StatusWarn)
			span.Detail("robust fallback: the input is not fully determined")
		}
	}
	return b, b.Tier
}

func enParseSpan() *trace.Span {
	r := trace.From(context.Background())
	if r == nil {
		return nil
	}
	return r.Open(trace.StageParse, "English clause parser")
}

func (p *enParser) nextClauseID() string {
	p.clauseN++
	return fmt.Sprintf("c%d", p.clauseN)
}

func (p *enParser) nextPhraseID() string {
	p.phraseN++
	return fmt.Sprintf("p%d", p.phraseN)
}

// --- segmentation --------------------------------------------------------

// enSentenceEnd returns the index of the sentence-final punctuation of the
// sentence starting at from, or -1.
func enSentenceEnd(toks []*forest.Morph, from int) int {
	for i := from; i < len(toks); i++ {
		if toks[i].POS != forest.POSPunct {
			continue
		}
		switch toks[i].Surface {
		case ".", "!", "?", "。", "！", "？":
			return i
		}
	}
	return -1
}

// enIsSplitter reports whether a token joins two clauses. Function words that
// are also prepositions ("before", "until") only join clauses when the tagger
// found them as conjunctions, which is why the decision is made on the tag and
// not on the string.
func enIsSplitter(m *forest.Morph) bool {
	if m.POS != forest.POSConj {
		return false
	}
	return m.Feat("splitter") == "true" || m.Feat("subordinator") == "true"
}

func enHasFiniteVerb(toks []*forest.Morph, a, b int) bool {
	for i := a; i < b; i++ {
		if enIsPossessiveClitic(toks[i]) {
			continue
		}
		if enIsVerbLike(toks[i]) {
			return true
		}
	}
	return false
}

func enLastNounHead(toks []*forest.Morph, a, b int) string {
	for i := b - 1; i >= a; i-- {
		if enIsPunct(toks[i]) {
			continue
		}
		if enIsNounish(toks[i]) {
			return toks[i].ID
		}
	}
	return ""
}

// enPrevIsComplementizerVerb reports whether the last non-punctuation token
// before position b takes a that-clause complement.
func enPrevIsComplementizerVerb(toks []*forest.Morph, a, b int) bool {
	for i := b - 1; i >= a; i-- {
		if enIsPunct(toks[i]) {
			continue
		}
		return enComplementizerVerbs[toks[i].Lem]
	}
	return false
}

// segment splits one sentence into clause-sized spans. Three things split:
// an explicit connective, a relative pronoun, and a second finite verb. The
// third case is what separates "I read it yesterday was interesting" into the
// two clauses it actually is; nothing else in English forces it, and
// inventing it without a verb would be guessing (plan.md §4).
func (p *enParser) segment(toks []*forest.Morph, from, to int) []*enSeg {
	var segs []*enSeg
	start := from
	pending := ConjoinNone
	pendingRel := ""

	emit := func(end int) {
		if end > start {
			segs = append(segs, &enSeg{
				from:     start,
				to:       end,
				conjoin:  pending,
				relative: pendingRel != "",
				relHead:  pendingRel,
			})
		}
		start = end
		pending = ConjoinNone
		pendingRel = ""
	}

	for i := from; i < to; i++ {
		t := toks[i]
		if i == start || enIsPunct(t) || enIsPossessiveClitic(t) {
			continue
		}
		// A relative pronoun starts a new clause; the noun phrase in front of
		// it stays with the clause it was already part of.
		if enRelativeOpeners[strings.ToLower(t.Surface)] &&
			enHasFiniteVerb(toks, i+1, to) &&
			!enPrevIsComplementizerVerb(toks, start, i) {
			head := enLastNounHead(toks, start, i)
			emit(i)
			start = i + 1
			pendingRel = head
			continue
		}
		// "so that" introduces a purpose clause whose head is two tokens later;
		// splitting between them would strand "that".
		if enIsWord(t, "so") && i+1 < to && enIsWord(toks[i+1], "that") {
			emit(i)
			start = i + 2
			pending = ConjoinPurpose
			continue
		}
		if enIsSplitter(t) {
			emit(i)
			pending = enConjoinOf[strings.ToLower(t.Surface)]
			if pending == ConjoinNone && strings.EqualFold(t.Surface, "so") {
				pending = ConjoinSo
			}
			start = i + 1
			continue
		}
		if cut := enStartsNewClauseAtVerb(toks, i, start); cut >= 0 {
			emit(cut)
			start = cut
		}
	}
	if start < to {
		segs = append(segs, &enSeg{from: start, to: to, conjoin: pending, relative: pendingRel != "", relHead: pendingRel})
	}
	return segs
}

// enOpenComplementizer reports whether a token still has its complement open.
func enOpenComplementizer(m *forest.Morph) bool {
	switch strings.ToLower(m.Surface) {
	case "that", "whether", "if", "because", "unless", "although", "though",
		"while", "when", "where", "before", "after", "until", "since":
		return true
	}
	return false
}

// enStartsNewClauseAtVerb returns the token index where a new clause must
// begin, or -1 when the verb at i continues the current clause.
//
// Two adjacent finite verbs cannot be one clause in English unless something
// intervenes, so the boundary is real evidence, not a guess: it is what
// separates "I read it yesterday was interesting" into two clauses. Auxiliaries,
// negation attached to the preceding auxiliary, and infinitival markers all
// continue the same verb group and therefore suppress it. The boundary is
// extended leftwards over the new clause's own subject, because the subject
// arrives before its verb and would otherwise be stranded in the previous
// clause.
func enStartsNewClauseAtVerb(toks []*forest.Morph, i, start int) int {
	if i <= start {
		return -1
	}
	m := toks[i]
	if enIsPossessiveClitic(m) || !enIsVerbLike(m) {
		return -1
	}
	switch m.Feat("part") {
	case "finite", "base", "past", "":
	default:
		return -1
	}
	if !enHasFiniteVerb(toks, start, i) {
		return -1
	}
	prev := toks[i-1]
	switch {
	case enIsPunct(prev):
		if prev.Surface != "," && prev.Surface != ";" {
			return -1
		}
	case enIsNeg(prev):
		// "did not go" is one clause because the do-form in front of the
		// negation is the auxiliary of the very verb that follows it. In
		// "said do not do" that do-form is itself preceded by a finite verb, so
		// the negation opens a new auxiliary group and a new clause.
		if i-2 >= start {
			a := toks[i-2]
			sameGroup := (a.Lem == "do" || enIsHave(a) || enIsModal(a)) &&
				!enHasFiniteVerb(toks, start, i-2)
			if sameGroup {
				return -1
			}
			// The auxiliary that owns the negation belongs to the new clause.
			return i - 2
		}
	case enIsVerbLike(prev), enIsPrep(prev), enIsAdv(prev), enIsConj(prev):
		return -1
	}
	// An open complementizer still binds the two verbs into one clause:
	// "said that he was" is one clause with two verbs, not two clauses. A
	// comma closes it, which is what lets "if she comes, he leaves" split.
	for k := i - 1; k >= start; k-- {
		switch {
		case enIsComma(toks[k]), enIsSplitter(toks[k]):
			k = start
		case enOpenComplementizer(toks[k]):
			return -1
		}
	}
	if !(enIsNounish(prev) || enIsAdj(prev) || enIsNum(prev) || enIsNeg(prev) || enIsAdv(prev)) {
		return -1
	}
	cut := i
	for cut-1 >= start {
		p := toks[cut-1]
		if !(enIsDet(p) || enIsAdj(p) || enIsNounish(p)) {
			break
		}
		cut--
	}
	// The noun phrase in front of the verb is its subject only if it is not
	// itself the tail of the previous clause's verb group.
	if cut-1 >= start {
		q := toks[cut-1]
		if enIsVerbLike(q) || enIsPrep(q) || enIsAdv(q) || enIsConj(q) || enIsNeg(q) {
			return i
		}
	}
	if cut == start {
		return i
	}
	return cut
}

// analyze is the driver: sentences, then clause spans, then roles, then the
// attachments that cross span boundaries.
func (p *enParser) analyze() []*Clause {
	var out []*Clause
	from := 0
	for from < len(p.toks) {
		end := len(p.toks)
		if k := enSentenceEnd(p.toks, from); k >= 0 {
			end = k + 1
		}
		// host is the verb-less noun phrase that splitting off a relative clause
		// strands in front of it: "the book [that I read] was interesting" leaves
		// "the book" with no verb of its own. English still makes it the subject
		// of the clause that follows, so it is handed over rather than reported
		// as a separate fragment.
		var host *Clause
		var hostPhrase *Phrase
		for _, s := range p.segment(p.toks, from, end) {
			c := p.parseTokens(p.toks[s.from:s.to], s.conjoin, hostPhrase, s.from == from, 0)
			if c == nil {
				continue
			}
			if s.relative {
				c.Conjoin = ConjoinRelative
				if enAttachRelative(enWithHost(out, host), c, s.relHead) {
					// The clause now lives inside the noun phrase, so it must not
					// also be listed as a top-level clause.
					continue
				}
			}
			if c.Subject == hostPhrase && hostPhrase != nil {
				host, hostPhrase = nil, nil
			}
			if host != nil {
				out = append(out, host)
				host, hostPhrase = nil, nil
			}
			if c.Matrix == "" && host == nil {
				host = c
				if c.Topic != nil {
					hostPhrase = c.Topic
				}
				continue
			}
			out = append(out, c)
		}
		if host != nil {
			out = append(out, host)
		}
		if end <= from {
			break
		}
		from = end
	}
	return out
}

// enWithHost includes the clause still waiting for its subject in the search
// space, so a relative clause can attach to a noun phrase that has not been
// committed to the output yet.
func enWithHost(out []*Clause, host *Clause) []*Clause {
	if host == nil {
		return out
	}
	return append(append([]*Clause(nil), out...), host)
}

// enClausesPhrases collects every phrase reachable from a clause set, so an
// attachment can be found even when the host noun phrase is itself nested.
func enClausesPhrases(clauses []*Clause) []*Phrase {
	var out []*Phrase
	var walk func(c *Clause, depth int)
	walk = func(c *Clause, depth int) {
		if c == nil || depth > 6 {
			return
		}
		direct := []*Phrase{c.Topic, c.Subject, c.Object}
		keys := make([]string, 0, len(c.Indirect))
		for k := range c.Indirect {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ph := c.Indirect[k]; ph != nil {
				direct = append(direct, ph)
			}
		}
		for _, ph := range direct {
			if ph != nil {
				out = append(out, ph)
			}
		}
		for _, ph := range direct {
			if ph != nil {
				walk(ph.Relative, depth+1)
			}
		}
		walk(c.Complement, depth+1)
	}
	for _, c := range clauses {
		walk(c, 0)
	}
	return out
}

// enAttachRelative hangs a relative clause on the noun phrase it modifies and
// reports whether it found a host. A relative clause with no host stays a
// top-level clause instead of being attached to whatever happened to be
// nearest.
func enAttachRelative(clauses []*Clause, c *Clause, head string) bool {
	if head == "" {
		c.Notes = append(c.Notes, "relative clause has no overt head noun")
		return false
	}
	for _, ph := range enClausesPhrases(clauses) {
		if ph.Relative != nil || ph.Head != head {
			continue
		}
		ph.Relative = c
		return true
	}
	c.Notes = append(c.Notes, "relative clause head "+head+" was not found in a noun phrase")
	return false
}

// --- role assignment -----------------------------------------------------

// parseTokens analyses one clause-sized token span. It is recursive only
// through clausal complements, and only to a bounded depth.
func (p *enParser) parseTokens(w []*forest.Morph, conjoin Conjoin, host *Phrase, first bool, depth int) *Clause {
	if len(w) == 0 || enOnlyPunctuation(w) {
		return nil
	}
	c := &Clause{
		ID:          p.nextClauseID(),
		Conjoin:     conjoin,
		Polarity:    "POS",
		Probability: 1,
		Span:        jlir.Span{Start: w[0].Start, End: w[len(w)-1].End},
		Indirect:    map[string]*Phrase{},
	}

	n := len(w)
	consumed := make([]bool, n)

	i := 0
	for i < n && enIsPunct(w[i]) {
		i++
	}
	// A leading adverb attaches to the clause, not to the subject NP.
	for i < n && enIsAdv(w[i]) && i+1 < n && !enIsNounish(w[i+1]) &&
		!enIsDet(w[i+1]) && !enIsVerbLike(w[i+1]) {
		c.Adverbs = append(c.Adverbs, w[i].ID)
		consumed[i] = true
		i++
	}
	preVerb := i

	vs := -1
	for k := preVerb; k < n; k++ {
		if enIsVerbLikeAt(w, k) {
			vs = k
			break
		}
	}
	if vs < 0 {
		// No verb: a fragment. Whatever nominal material exists is reported
		// as the topic of the fragment rather than as a subject.
		if ph := p.phraseOf(w, enFindNP(w, preVerb, n), consumed); ph != nil {
			c.Topic = ph
			c.Notes = append(c.Notes, "no finite verb: nominal fragment kept as a topic")
		}
		p.finish(w, c)
		return c
	}

	ve := enVerbGroupEnd(w, vs, n)
	var matrixIdx int
	matrixIdx, c.Auxiliaries, c.Modality = enHeadVerb(w, vs, ve, c)
	c.Matrix = w[matrixIdx].ID

	// Negation, adverbs and voice are read off the verb group.
	for k := vs; k < ve; k++ {
		if enIsNeg(w[k]) {
			if c.Negation == "" {
				c.Negation = w[k].ID
				c.Polarity = "NEG"
			}
			consumed[k] = true
			continue
		}
		if enIsAdv(w[k]) {
			c.Adverbs = append(c.Adverbs, w[k].ID)
			consumed[k] = true
		}
	}
	if c.Negation == "" {
		// "no book" and "nobody" negate without a verb-group adverb.
		for k := 0; k < n; k++ {
			if enIsNeg(w[k]) {
				c.Negation = w[k].ID
				c.Polarity = "NEG"
				break
			}
		}
	}
	enFillTense(c, w, vs, matrixIdx, ve)
	enFillVoice(c, w, vs, matrixIdx)
	if vs == preVerb && matrixIdx == vs && host != nil {
		c.Subject = host
	} else if vs == preVerb && matrixIdx == vs && first {
		// Only a sentence-initial bare verb group is an imperative. A bare verb
		// group later in the sentence is a quotation or a shared predicate, and
		// claiming an imperative there would invent a mood.
		c.Mood = "IMPERATIVE"
		c.Notes = append(c.Notes, "bare verb group: read as an imperative")
	}

	// Subject, object and the prepositional phrases that follow.
	subjNP, fronted := enFindSubjectNP(w, preVerb, vs, consumed)
	if subjNP.ok {
		c.Subject = p.phraseOf(w, subjNP, consumed)
	}
	for _, pp := range fronted {
		if ph := p.phraseOf(w, enNP{Start: pp[1], End: pp[2], Head: enHeadIndex(w, pp[1], pp[2]), ok: true}, consumed); ph != nil {
			enAddIndirect(c, w[pp[0]], ph, "fronted prepositional phrase")
			for k := pp[0]; k < pp[2]; k++ {
				consumed[k] = true
			}
		}
	}

	objEnd := enObjectLimit(w, ve, n)
	if objEnd > ve {
		if objNP := enFindObjectNP(w, ve, objEnd); objNP.ok {
			c.Object = p.phraseOf(w, objNP, consumed)
			if len(objNP.alternatives) > 0 {
				c.Notes = append(c.Notes, "the direct object boundary is ambiguous: "+objNP.alternatives[0].Why)
			}
		}
	}

	// Remaining prepositional phrases are indirect arguments, keyed by their
	// surface preposition because Japanese will have to pick a case marker for
	// each of them.
	for k := ve; k < n; k++ {
		if consumed[k] || !enIsPrep(w[k]) || enIsPunct(w[k]) {
			continue
		}
		if k > 0 && enIsVerbLike(w[k-1]) && k+1 < n && enIsVerbLike(w[k+1]) {
			continue // infinitival "to", handled as a complement
		}
		j := enNPEndForward(w, k+1, n)
		if j <= k+1 {
			continue
		}
		ph := p.phraseOf(w, enNP{Start: k + 1, End: j, Head: enHeadIndex(w, k+1, j), ok: true}, consumed)
		if ph == nil {
			continue
		}
		note := "oblique argument"
		if strings.EqualFold(w[k].Surface, "by") && c.Voice == "PASSIVE" {
			note = "agent of the passive"
		}
		enAddIndirect(c, w[k], ph, note)
		for x := k; x < j; x++ {
			consumed[x] = true
		}
	}

	// Complements, then post-verbal adverbs.
	if depth < 3 {
		p.fillComplement(c, w, ve, n, consumed, depth)
	}
	for k := ve; k < n; k++ {
		if consumed[k] || !enIsAdv(w[k]) || enIsNeg(w[k]) {
			continue
		}
		c.Adverbs = append(c.Adverbs, w[k].ID)
		consumed[k] = true
	}
	for k := 0; k < n; k++ {
		if !consumed[k] && enIsAdv(w[k]) && !enIsNeg(w[k]) {
			c.Adverbs = append(c.Adverbs, w[k].ID)
		}
	}

	// Quantifier on the subject: English makes it overt, Japanese does not.
	if c.Subject != nil {
		if h := p.bundle.Morph(c.Subject.Head); h != nil {
			if q := h.Feat("quantifier"); q != "" {
				c.Quantifier = q
				c.QuantifiedNP = c.Subject.ID
			}
			if g := h.Feat("gender"); g != "" && g != "unknown" {
				c.Notes = append(c.Notes, "source asserts gender="+g+" on the subject")
			}
		}
	}

	p.recordAlternatives(c, w, subjNP, matrixIdx)
	p.finish(w, c)
	return c
}

// finish normalizes what every clause needs regardless of which path built it.
func (p *enParser) finish(w []*forest.Morph, c *Clause) {
	c.Span = jlir.Span{Start: w[0].Start, End: w[len(w)-1].End}
	sort.Strings(c.Adverbs)
	c.Adverbs = enUnique(c.Adverbs)
}

// enIsVerbLikeAt is the verb test the parser uses. A verb-shaped word right
// after a determiner is a noun ("the walk was long"), which a POS tag alone
// cannot decide.
func enIsVerbLikeAt(w []*forest.Morph, k int) bool {
	m := w[k]
	if enIsPossessiveClitic(m) {
		return false
	}
	if m.POS == forest.POSAux {
		return true
	}
	if m.POS != forest.POSVerb {
		return false
	}
	if k > 0 && (enIsDet(w[k-1]) || enIsPossessiveDet(w[k-1]) || enIsPunct(w[k-1])) {
		return false
	}
	return true
}

// enVerbGroupEnd finds the end of the verb group: auxiliaries, the main verb,
// adverbs and negation, stopping at the first argument.
func enVerbGroupEnd(w []*forest.Morph, vs, n int) int {
	end := vs + 1
	for k := vs + 1; k < n; k++ {
		switch {
		case enIsVerbLike(w[k]) && !enIsPossessiveClitic(w[k]):
			end = k + 1
		case enIsAdv(w[k]):
			end = k + 1
		case (enIsPrep(w[k]) || enIsParticipleTo(w[k])) && k+1 < n && enIsVerbLike(w[k+1]):
			// An infinitival marker introduces a complement, not more of
			// this verb group.
			return end
		default:
			return end
		}
	}
	return end
}

// enHeadVerb picks the main verb of the group and splits off the auxiliaries.
// It also recognizes the two periphrastic modals English spells with a verb
// plus a particle: "have to go" and "be going to go".
func enHeadVerb(w []*forest.Morph, vs, ve int, c *Clause) (int, []string, string) {
	main := -1
	for k := ve - 1; k >= vs; k-- {
		if enIsVerbLike(w[k]) && !enIsPossessiveClitic(w[k]) {
			main = k
			break
		}
	}
	if main < 0 {
		main = vs
	}
	var aux []string
	modality := ""
	for k := vs; k < main; k++ {
		if enIsVerbLike(w[k]) || enIsModal(w[k]) {
			aux = append(aux, w[k].ID)
		}
		if enIsModal(w[k]) {
			if l := enModalityLabel[w[k].Lem]; l != "" {
				modality = l
			}
		}
	}
	if enIsModal(w[main]) {
		if l := enModalityLabel[w[main].Lem]; l != "" {
			modality = l
		}
		aux = append(aux, w[main].ID)
	}

	// "has to go": the auxiliary is a modal use of have.
	if main+2 < len(w) && enIsHave(w[main]) && enIsWord(w[main+1], "to") &&
		enIsVerbLike(w[main+2]) {
		aux = append(aux, w[main].ID)
		aux = append(aux, w[main+1].ID)
		modality = "OBLIGATION"
		c.Notes = append(c.Notes, "have + to + verb read as a modal construction")
		return main + 2, aux, modality
	}
	// "is going to go": a periphrastic future or intention.
	if main+2 < len(w) && w[main].Feat("part") == "gerund" && enIsWord(w[main+1], "to") &&
		enIsVerbLike(w[main+2]) {
		aux = append(aux, w[main].ID)
		aux = append(aux, w[main+1].ID)
		if main > 0 && (enIsCopula(w[main-1]) || w[main-1].Lem == "be") {
			modality = "INTENTION"
		}
		c.Notes = append(c.Notes, "be + going + to + verb read as a periphrastic future")
		return main + 2, aux, modality
	}
	return main, aux, modality
}

// enFillTense derives the clause tense and aspect from the verb group, keeping
// perfect and progressive constructions explicit instead of flattening them.
func enFillTense(c *Clause, w []*forest.Morph, vs, main, ve int) {
	perfect, progressive, future := false, false, false
	for k := vs; k < main; k++ {
		if enIsHave(w[k]) {
			perfect = true
		}
		if w[k].Lem == "be" {
			progressive = true
		}
		if w[k].Lem == "will" || w[k].Lem == "shall" {
			future = true
		}
	}
	if enIsModal(w[main]) && (w[main].Lem == "will" || w[main].Lem == "shall") {
		future = true
	}
	switch {
	case future:
		c.Tense = "future"
	case perfect:
		c.Tense = "perfect"
	default:
		if t := w[main].Feat("tense"); t != "" {
			c.Tense = t
		} else if readings := w[main].Feat("tense_readings"); readings != "" {
			// The spelling carries both readings, so agreement decides.
			c.Tense = enResolveTenseByAgreement(readings, c, w, vs)
		} else if w[main].Feat("part") == "participle" {
			c.Tense = "past"
		}
	}
	if progressive {
		c.Aspect = "progressive"
	}
	if enIsCopula(w[main]) {
		c.Tense = w[main].Feat("tense")
	}
}

// enResolveTenseByAgreement narrows a verb whose present and past are spelled
// identically using the subject.
//
// "Taro read a book" is past: a third-person singular subject takes -s in the
// present, so the present would have to be "reads". "I read a book" is not
// settled — I read is the present and I read is the past — and is left for the
// semantic forest to hold open, which is where an unresolved reading belongs.
func enResolveTenseByAgreement(readings string, c *Clause, w []*forest.Morph, vs int) string {
	opts := strings.Split(readings, ",")
	if len(opts) < 2 {
		return opts[0]
	}
	// The subject is the first noun-ish token before the verb.
	for k := 0; k < vs; k++ {
		if !enIsNounish(w[k]) {
			continue
		}
		switch strings.ToLower(w[k].Surface) {
		case "i", "you", "we", "they":
			// No agreement evidence either way: first and second person, and
			// plural, keep the same -e ending. Leave it open.
			return ""
		default:
			// Third person singular would need -s, which the reading lacks.
			return "past"
		}
	}
	return ""
}

// enFillVoice flags the passive. The active reading is kept as an alternative
// by recordAlternatives: a be + participle sequence is evidence for the passive,
// not proof of it.
// A form whose primary reading is not a participle can still be one:
// "read" is the past of read in American English and the participle in
// British English. The lattice recorded both, so the passive is raised
// here with a note rather than being lost.
func enFillVoice(c *Clause, w []*forest.Morph, vs, main int) {
	if main <= vs {
		return
	}
	m := w[main]
	participle := enIsParticiple(m)
	if !participle {
		want := m.Lem + ":VERB:participle:"
		for _, alt := range m.Alternatives {
			if strings.Contains(alt, want) {
				participle = true
				c.Notes = append(c.Notes, "the participle reading of "+m.Surface+" is an alternative, not the primary tag")
				break
			}
		}
	}
	if !participle {
		return
	}
	for k := vs; k < main; k++ {
		if w[k].Lem == "be" || w[k].Lem == "get" {
			c.Voice = "PASSIVE"
			return
		}
	}
}

// enObjectLimit is where the direct object ends: the first preposition,
// adverb, comma, conjunction or sentence boundary.
func enObjectLimit(w []*forest.Morph, from, n int) int {
	for k := from; k < n; k++ {
		m := w[k]
		switch {
		case enIsComma(m), enIsPrep(m), enIsAdv(m), enIsConj(m):
			return k
		case m.POS == forest.POSPunct && (m.Surface == "." || m.Surface == ";"):
			return k
		}
	}
	return n
}

// --- noun phrase location ------------------------------------------------

func enHeadIndex(w []*forest.Morph, a, b int) int {
	for i := b - 1; i >= a; i-- {
		if enIsNounish(w[i]) {
			return i
		}
	}
	return -1
}

// enNPEndForward measures the noun phrase that starts at a.
func enNPEndForward(w []*forest.Morph, a, b int) int {
	if a >= b {
		return a
	}
	if enIsLeftModifier(w[a]) || enIsPossessiveDet(w[a]) {
		k := a
		for k < b && enIsLeftModifier(w[k]) {
			k++
		}
		if k < b && enIsNounish(w[k]) {
			return k + 1
		}
		return a
	}
	if enIsNounish(w[a]) {
		return a + 1
	}
	return a
}

// enFindNP locates a noun phrase by scanning leftwards from its head, which is
// how English works: the head is last inside the phrase. Two structural
// decisions are made on the way and neither is free: a prepositional phrase
// between two nouns belongs *inside* the phrase ("the man with the hat said"),
// while a prepositional phrase with no noun to its left is an adjunct of the
// clause ("in the garden Taro saw"). Only the second reading is offered as a
// competing alternative; the first is kept.
func enFindNP(w []*forest.Morph, a, b int) enNP {
	var np enNP
	head := -1
	for k := b - 1; k >= a; k-- {
		if enIsPunct(w[k]) || enIsDet(w[k]) {
			continue
		}
		if enIsNounish(w[k]) {
			head = k
			break
		}
	}
	if head < 0 {
		return np
	}
	np.ok = true
	np.Head = head
	s := head
loop:
	for s > a {
		prev := w[s-1]
		switch {
		case enIsLeftModifier(prev):
			np.Modifiers = append(np.Modifiers, s-1)
			s--
		case enIsPossessiveClitic(prev):
			np.Modifiers = append(np.Modifiers, s-1)
			s--
			p := s - 1
			for p >= a && enIsLeftModifier(w[p]) {
				p--
			}
			if p >= a && enIsNounish(w[p]) {
				np.Possessors = append(np.Possessors, [2]int{p, s})
				np.alternatives = append(np.alternatives, enNPAlt{
					Why: "the possessive clitic could attach to the possessor or to the head",
				})
				s = p
			}
		case enIsPrep(prev):
			p := s - 2
			for p >= a && enIsLeftModifier(w[p]) {
				p--
			}
			if p >= a && enIsNounish(w[p]) {
				np.InternalPPs = append(np.InternalPPs, [3]int{s - 1, s, head + 1})
				np.alternatives = append(np.alternatives, enNPAlt{
					Why: "the prepositional phrase may modify the phrase or stand outside it",
				})
				s = p
				continue loop
			}
			break loop
		default:
			break loop
		}
	}
	np.Start, np.End = s, head+1
	return np
}

// enFindSubjectNP locates the pre-verbal subject, skipping a fronted
// prepositional phrase: in "In the garden Taro saw a cat" the garden is not the
// subject, and in "The man with the hat said" the phrase with the hat is.
func enFindSubjectNP(w []*forest.Morph, a, b int, consumed []bool) (enNP, [][3]int) {
	var fronted [][3]int
	i := a
	for i < b && enIsPrep(w[i]) {
		j := enNPEndForward(w, i+1, b)
		if j <= i+1 {
			break
		}
		fronted = append(fronted, [3]int{i, i + 1, j})
		i = j
	}
	return enFindNP(w, i, b), fronted
}

// enFindObjectNP locates the post-verbal direct object, scanning forward
// because in English the head comes first.
func enFindObjectNP(w []*forest.Morph, a, b int) enNP {
	var np enNP
	i := a
	for i < b && enIsPunct(w[i]) {
		i++
	}
	for i < b && enIsAdv(w[i]) {
		i++
	}
	if i >= b {
		return np
	}
	var head int
	switch {
	case enIsNounish(w[i]):
		head = i
	case enIsLeftModifier(w[i]):
		k := i
		for k < b && enIsLeftModifier(w[k]) {
			k++
		}
		if k >= b || !enIsNounish(w[k]) {
			return np
		}
		head = k
		for x := i; x < k; x++ {
			np.Modifiers = append(np.Modifiers, x)
		}
	default:
		return np
	}
	np.ok = true
	np.Head = head
	s := head + 1
	for s < b && enIsLeftModifier(w[s]) {
		np.Modifiers = append(np.Modifiers, s)
		s++
	}
	for s < b && enIsPossessiveClitic(w[s]) {
		np.Modifiers = append(np.Modifiers, s)
		s++
	}
	np.Start, np.End = i, s
	if s < b && enIsNounish(w[s]) {
		np.alternatives = append(np.alternatives, enNPAlt{
			Why: "a following noun could be a second conjunct rather than part of the phrase",
		})
	}
	return np
}

// --- phrase construction -------------------------------------------------

// phraseOf turns a located noun phrase into a Phrase and marks the tokens it
// consumed so nothing is counted twice.
func (p *enParser) phraseOf(w []*forest.Morph, np enNP, consumed []bool) *Phrase {
	if !np.ok || np.Head < 0 || np.Head >= len(w) {
		return nil
	}
	ph := &Phrase{
		ID:          p.nextPhraseID(),
		Head:        w[np.Head].ID,
		Probability: 1,
		Span:        jlir.Span{Start: w[np.Start].Start, End: w[np.End-1].End},
	}
	for _, m := range np.Modifiers {
		ph.Modifiers = append(ph.Modifiers, w[m].ID)
	}
	for _, ps := range np.Possessors {
		ph.Possessors = append(ph.Possessors, w[ps[0]].ID)
		ph.Modifiers = append(ph.Modifiers, w[ps[0]].ID)
	}
	head := w[np.Head]
	switch {
	case head.POS == forest.POSNum:
		ph.Numeral = head.Lem
	case head.POS == forest.POSPronoun:
		ph.Pronoun = true
	}
	if d := head.Feat("demonstrative"); d == "true" {
		ph.Demonstrative = head.Lem
	}
	// Count agreement is English-specific pressure on the translation: an
	// article, a plural form and a verb form all depend on it.
	switch {
	case head.Feat("number") == "plur":
		ph.Notes = append(ph.Notes, "head is plural: the target must realize plural marking")
	case enIrregularPlurals[strings.ToLower(head.Lem)] != "":
		ph.Notes = append(ph.Notes, "irregular plural of "+enIrregularPlurals[strings.ToLower(head.Lem)])
	}
	if enMassNouns[strings.ToLower(head.Lem)] {
		ph.Notes = append(ph.Notes, "mass noun: English forbids a plural here, the target may need a counter")
	}
	if len(np.InternalPPs) > 0 {
		ph.Notes = append(ph.Notes, "the noun phrase contains an internal prepositional phrase")
	}
	for _, pp := range np.InternalPPs {
		ph.Modifiers = append(ph.Modifiers, w[pp[0]].ID)
	}
	for i := np.Start; i < np.End && i < len(consumed); i++ {
		consumed[i] = true
	}
	return ph
}

// enAddIndirect files a prepositional phrase under its surface preposition.
func enAddIndirect(c *Clause, prep *forest.Morph, ph *Phrase, note string) {
	key := strings.ToLower(prep.Surface)
	if ph == nil {
		return
	}
	ph.Notes = append(ph.Notes, note)
	if _, dup := c.Indirect[key]; dup {
		c.Notes = append(c.Notes, "more than one "+key+"-phrase: only the first is filed under the preposition")
		return
	}
	c.Indirect[key] = ph
}

// --- complements ---------------------------------------------------------

// fillComplement attaches a clausal complement: a to-infinitive, a
// that-clause, or the predicate of a copula.
func (p *enParser) fillComplement(c *Clause, w []*forest.Morph, ve, n int, consumed []bool, depth int) {
	if ve >= n {
		return
	}
	// Copula predicate: the matrix verb stays the copula and the predicate is
	// the complement, so the semantic layer can see that the subject is being
	// predicated rather than acted on.
	if enIsCopula(w[enMatrixIndex(c, w)]) && !enIsPunct(w[ve]) {
		if enIsAdj(w[ve]) || enIsNounish(w[ve]) || enIsAdv(w[ve]) {
			end := ve + 1
			for end < n && (enIsAdj(w[end]) || enIsNounish(w[end])) {
				end++
			}
			head := enHeadIndex(w, ve, end)
			if head >= 0 || enIsAdj(w[ve]) {
				pred := &Clause{
					ID:          p.nextClauseID(),
					Polarity:    "POS",
					Probability: 0.6,
					Span:        jlir.Span{Start: w[ve].Start, End: w[end-1].End},
				}
				if enIsAdj(w[ve]) {
					pred.Matrix = w[ve].ID
				} else {
					// A predicate nominative is not a second argument: the noun is
					// what the subject is called, so only the head is recorded.
					pred.Matrix = w[head].ID
					for k := ve; k < end; k++ {
						consumed[k] = true
					}
				}
				pred.Notes = append(pred.Notes, "predicate of the copula; shares the matrix subject")
				c.Complement = pred
				if c.Object != nil && c.Object.Span == pred.Span {
					// The same span cannot be both the direct object and the
					// predicate of the copula; the predicate reading wins because
					// it is the one English marks.
					c.Object = nil
				}
				for k := ve; k < end; k++ {
					consumed[k] = true
				}
				return
			}
		}
	}
	// to-infinitive.
	if (enIsParticipleTo(w[ve]) || enIsPrep(w[ve])) && ve+1 < n &&
		enIsWord(w[ve], "to") && enIsVerbLike(w[ve+1]) && enIntentionVerbs[enMatrixLemma(c, w)] {
		pred := &Clause{
			ID:          p.nextClauseID(),
			Polarity:    "POS",
			Probability: 0.7,
			Matrix:      w[ve+1].ID,
			Span:        jlir.Span{Start: w[ve].Start, End: w[n-1].End},
			Auxiliaries: []string{w[ve].ID},
		}
		pred.Tense = "future"
		pred.Notes = append(pred.Notes, "infinitival complement of "+enMatrixLemma(c, w)+
			"; negation in the matrix outscopes it, and the narrow-scope reading is kept as an alternative")
		c.Complement = pred
		for k := ve; k < n; k++ {
			consumed[k] = true
		}
		return
	}
	// that-clause / whether-clause.
	for k := ve; k < n; k++ {
		if consumed[k] || enIsPunct(w[k]) {
			continue
		}
		s := strings.ToLower(w[k].Surface)
		if s != "that" && s != "whether" && s != "if" {
			continue
		}
		if k > 0 && !enComplementizerVerbs[w[k-1].Lem] {
			continue
		}
		if !enHasFiniteVerb(w, k+1, n) {
			continue
		}
		sub := p.parseTokens(w[k+1:n], ConjoinNone, nil, false, depth+1)
		if sub == nil {
			continue
		}
		sub.Conjoin = ConjoinNone
		sub.Notes = append(sub.Notes, "that-complement of "+w[k-1].Lem)
		c.Complement = sub
		for x := k; x < n; x++ {
			consumed[x] = true
		}
		return
	}
}

// enMatrixIndex recovers the index of a clause’s matrix morpheme inside the span.
func enMatrixIndex(c *Clause, w []*forest.Morph) int {
	for i, m := range w {
		if m.ID == c.Matrix {
			return i
		}
	}
	return -1
}

// enMatrixLemma is the lemma of the matrix predicate, or "".
func enMatrixLemma(c *Clause, w []*forest.Morph) string {
	i := enMatrixIndex(c, w)
	if i < 0 {
		return ""
	}
	return w[i].Lem
}

// --- alternatives --------------------------------------------------------

// recordAlternatives writes the readings the parser refuses to collapse into
// Clause.Alternatives (plan.md §17).
func (p *enParser) recordAlternatives(c *Clause, w []*forest.Morph, subjNP enNP, main int) {
	// Passive versus active.
	if c.Voice == "PASSIVE" && main >= 0 {
		alt := enCloneClause(c)
		alt.Voice = "ACTIVE"
		alt.Probability = 0.4
		alt.Auxiliaries = nil
		alt.Notes = append([]string{"active reading of the be + participle sequence"}, alt.Notes...)
		c.Alternatives = append(c.Alternatives, alt)
	}
	// A noun phrase with an internal prepositional phrase has two boundaries.
	if len(subjNP.InternalPPs) > 0 && c.Subject != nil {
		pp := subjNP.InternalPPs[0]
		alt := enCloneClause(c)
		alt.Subject = p.phraseOf(w, enFindNP(w, pp[1], pp[2]), make([]bool, len(w)))
		alt.Indirect = map[string]*Phrase{}
		for k, v := range c.Indirect {
			alt.Indirect[k] = v
		}
		alt.Indirect[strings.ToLower(w[pp[0]].Surface)] = p.phraseOf(w,
			enNP{Start: pp[1], End: pp[2], Head: enHeadIndex(w, pp[1], pp[2]), ok: true}, make([]bool, len(w)))
		alt.Probability = 0.45
		alt.Notes = append([]string{"the prepositional phrase is an argument, not a modifier, of the subject"}, alt.Notes...)
		c.Alternatives = append(c.Alternatives, alt)
	}
	// Negation scope over a clausal complement.
	if c.Negation != "" && c.Complement != nil {
		alt := enCloneClause(c)
		alt.Polarity = "POS"
		alt.Negation = ""
		alt.Complement = enCloneClause(c.Complement)
		alt.Complement.Polarity = "NEG"
		alt.Complement.Negation = c.Negation
		alt.Probability = 0.35
		alt.Notes = append([]string{"narrow scope: the negation belongs to the complement"}, alt.Notes...)
		c.Alternatives = append(c.Alternatives, alt)
	}
}

// enCloneClause copies a clause for use as an alternative. The complement is
// deliberately shared rather than cloned recursively: the alternative differs
// from the winner in exactly the fields being copied.
func enCloneClause(c *Clause) *Clause {
	if c == nil {
		return nil
	}
	out := *c
	out.ID = c.ID + "?"
	out.Auxiliaries = append([]string(nil), c.Auxiliaries...)
	out.Adverbs = append([]string(nil), c.Adverbs...)
	out.Notes = append([]string(nil), c.Notes...)
	out.Alternatives = nil
	if c.Indirect != nil {
		out.Indirect = make(map[string]*Phrase, len(c.Indirect))
		for k, v := range c.Indirect {
			out.Indirect[k] = v
		}
	}
	return &out
}

// --- discourse order -----------------------------------------------------

// enAssignIndexes numbers the clauses in discourse order with the matrix
// clause at 0, which is what IsMatrix tests. When a subordinate clause comes
// first in the surface ("Because he was late, Taro left") the matrix clause is
// rotated to the front: the index is the clause's role in the argument
// structure, not its position on the page. Embedded clauses are numbered after
// the top-level ones so no index is ever shared.
func enAssignIndexes(clauses []*Clause) {
	if len(clauses) == 0 {
		return
	}
	matrix := 0
	for i, c := range clauses {
		switch c.Conjoin {
		case ConjoinNone, ConjoinAnd, ConjoinBut, ConjoinOr:
			matrix = i
			goto found
		}
	}
found:
	ordered := make([]*Clause, 0, len(clauses))
	ordered = append(ordered, clauses[matrix:]...)
	ordered = append(ordered, clauses[:matrix]...)
	for i, c := range ordered {
		c.Index = i
	}
	next := len(ordered)
	var walk func(c *Clause)
	walk = func(c *Clause) {
		if c == nil {
			return
		}
		if c.Complement != nil {
			c.Complement.Index = next
			next++
			walk(c.Complement)
		}
		seen := map[string]bool{}
		var phrases []*Phrase
		for _, ph := range []*Phrase{c.Topic, c.Subject, c.Object} {
			if ph != nil {
				phrases = append(phrases, ph)
			}
		}
		for _, ph := range c.Indirect {
			phrases = append(phrases, ph)
		}
		for _, ph := range phrases {
			if ph.Relative == nil || seen[ph.Relative.ID] {
				continue
			}
			seen[ph.Relative.ID] = true
			ph.Relative.Index = next
			next++
			walk(ph.Relative)
		}
	}
	for _, c := range ordered {
		walk(c)
	}
}

// --- tier, coverage, forest ---------------------------------------------

// enTier reports whether the parse is fully determined. A Tier 1 parse has no
// reconstructed morpheme, no unresolved alternative and a matrix predicate in
// every clause (plan.md §24).
func enTier(b *Bundle) int {
	for _, u := range b.Unknowns {
		if strings.TrimSpace(u) != "" {
			return 2
		}
	}
	var check func(c *Clause) bool
	check = func(c *Clause) bool {
		if c == nil {
			return true
		}
		if len(c.Alternatives) > 0 || c.Matrix == "" {
			return false
		}
		if !check(c.Complement) {
			return false
		}
		return true
	}
	for _, c := range b.Clauses {
		if !check(c) {
			return 2
		}
	}
	return 1
}

func enBundleNotes(b *Bundle) []string {
	var notes []string
	keys := make([]string, 0, len(b.Morphs))
	for id := range b.Morphs {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	// Only the most informative notes are kept: a long document would
	// otherwise bury everything else the user needs to see.
	for _, id := range keys {
		m := b.Morphs[id]
		if len(m.Alternatives) > 0 {
			notes = append(notes, m.ID+" "+m.Surface+" has "+fmt.Sprint(len(m.Alternatives))+
				" alternative reading(s): "+strings.Join(m.Alternatives, ", "))
		}
		if len(notes) >= 12 {
			break
		}
	}
	notes = append(notes, "morpheme ids are surface-ordered m1..m"+fmt.Sprint(len(b.Morphs))+
		"; whitespace is not a morpheme, so the ids cover every non-space character of the input")
	return notes
}

// buildForest packs the clauses into a forest whose terminals are morpheme
// surfaces. One node per clause reading, so a passive/active alternative is two
// nodes and the UI can show which one the decision layer prefers.
func (p *enParser) buildForest(clauses []*Clause) *forest.Forest {
	b := forest.NewBuilder("s")
	metas := map[string]map[string]string{}
	seen := map[*Clause]bool{}

	var nodeFor func(c *Clause) string
	nodeFor = func(c *Clause) string {
		if c == nil || seen[c] {
			return ""
		}
		seen[c] = true
		children := p.nodeChildren(c, b, nodeFor)
		readings := append([]*Clause{c}, c.Alternatives...)
		label := c.ID
		alts := make([]forest.Alt, 0, len(readings))
		for i, r := range readings {
			if i > 0 {
				label = c.ID + "@" + strings.ToLower(enReadingTag(r, c))
			}
			alts = append(alts, forest.Alt{
				Children:    children,
				Probability: r.Probability,
				Rule:        "reading:" + enReadingTag(r, c),
				Note:        strings.Join(r.Notes, "; "),
				Hard:        true,
			})
		}
		id := b.Add(label, alts)
		meta := map[string]string{
			"index":    fmt.Sprint(c.Index),
			"conjoin":  string(c.Conjoin),
			"voice":    c.Voice,
			"polarity": c.Polarity,
		}
		if c.Matrix != "" {
			meta["matrix"] = c.Matrix
		}
		if c.Subject != nil {
			meta["subject"] = c.Subject.ID
		}
		if c.Object != nil {
			meta["object"] = c.Object.ID
		}
		for k, v := range c.Indirect {
			meta["indirect."+k] = v.ID
		}
		metas[id] = meta
		return id
	}

	var roots []string
	for _, c := range clauses {
		if id := nodeFor(c); id != "" {
			roots = append(roots, id)
		}
	}
	rootID := b.Add("S", []forest.Alt{{Children: roots, Probability: 1, Rule: "root", Hard: true}})
	f := b.Root(rootID)
	for id, meta := range metas {
		if n := f.Node(id); n != nil {
			n.Meta = meta
		}
	}
	return f
}

func enReadingTag(r, base *Clause) string {
	if r.Voice != base.Voice && r.Voice != "" {
		return r.Voice
	}
	if r.Polarity != base.Polarity {
		return r.Polarity
	}
	if len(r.Notes) > 0 && len(base.Notes) > 0 && r.Notes[0] != base.Notes[0] {
		return "alt"
	}
	return "primary"
}

// nodeChildren interleaves a clause's own terminals with the nodes of the
// clauses embedded in it, in surface order, so the packed forest reads as the
// original sentence.
func (p *enParser) nodeChildren(c *Clause, builder *forest.Builder, nodeFor func(*Clause) string) []string {
	type item struct {
		off int
		id  string
	}
	var items []item
	embedded := []*Clause{}
	if c.Complement != nil {
		embedded = append(embedded, c.Complement)
	}
	for _, ph := range []*Phrase{c.Topic, c.Subject, c.Object} {
		if ph != nil && ph.Relative != nil {
			embedded = append(embedded, ph.Relative)
		}
	}
	for _, ph := range c.Indirect {
		if ph != nil && ph.Relative != nil {
			embedded = append(embedded, ph.Relative)
		}
	}
	for _, e := range embedded {
		if id := nodeFor(e); id != "" {
			items = append(items, item{off: e.Span.Start, id: id})
		}
	}
	for _, m := range p.toks {
		if m.Start < c.Span.Start || m.End > c.Span.End {
			continue
		}
		skip := false
		for _, e := range embedded {
			if m.Start >= e.Span.Start && m.End <= e.Span.End {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		prob := 1.0
		if m.Unknown {
			prob = 0.8
		}
		id := builder.AddTerminal(m.ID, m.Surface, prob, "morph:"+string(m.POS))
		items = append(items, item{off: m.Start, id: id})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].off < items[j].off })
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.id)
	}
	return out
}

// --- small helpers -------------------------------------------------------

func enUnique(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := in[:0]
	for i, s := range in {
		if i > 0 && s == in[i-1] {
			continue
		}
		out = append(out, s)
	}
	return out
}
