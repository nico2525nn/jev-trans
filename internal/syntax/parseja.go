package syntax

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/trace"
)

// jaAspectCompletion is the aspect value for a てしまう reading. Completion is
// kept apart from tense on purpose: plan.md §12 refuses to reduce 食べてしまった
// to "EAT + past".
const jaAspectCompletion = "completion"

// ParseJA analyses the winning morphological path into Japanese clauses.
//
// Three decisions in this file are structural rather than cosmetic:
//
//   - は is a *topic*. It never fills Clause.Subject; a が-phrase does. When a
//     clause has a topic and no overt subject the parser creates a zero subject
//     with a probability instead of promoting the topic silently, because
//     plan.md §15 requires zero anaphora to be a first-class object and §19
//     requires topic and subject to stay apart.
//   - てしまう / ている / てください arrive from the analyzer as separate
//     constituents, so completion, aspect and politeness are read off distinct
//     morphemes instead of being re-guessed from the surface string (§12).
//   - Genuine ambiguity goes into Clause.Alternatives. が belonging to the
//     matrix rather than to a relative clause, and NOT > ALL versus ALL > NOT,
//     are the two that change the translation, so neither is resolved here
//     (plan.md §13, §17).

// ParseJA builds a clause bundle from a Japanese morphological lattice.
func ParseJA(src string, mf *forest.MorphForest) *Bundle {
	return ParseJAIn(context.Background(), src, mf)
}

// ParseJAIn is ParseJA with the ambient trace recorder, so the pipeline can show
// the parse stage instead of running it invisibly (house rule 3).
func ParseJAIn(ctx context.Context, src string, mf *forest.MorphForest) *Bundle {
	var span *trace.Span
	if rec := trace.From(ctx); rec != nil {
		span = rec.Open(trace.StageParse, "Japanese clause parsing")
		defer span.Close()
	}
	b := parseJA(src, mf)
	if span != nil {
		span.Data(b)
		span.Count("clauses", len(b.Clauses))
		span.Count("morphemes", len(b.Morphs))
		span.Label("tier", strconv.Itoa(b.Tier))
		span.Detail("%d clauses, coverage %.0f%%, tier %d", len(b.Clauses), b.Coverage*100, b.Tier)
		if b.Tier > 1 {
			span.Status(trace.StatusWarn)
		}
	}
	return b
}

func parseJA(src string, mf *forest.MorphForest) *Bundle {
	b := &Bundle{
		Lang:   lang.JA,
		Source: src,
		Morphs: map[string]*forest.Morph{},
	}
	if mf == nil || len(mf.Paths) == 0 {
		b.Notes = append(b.Notes, "no morphological analysis available; nothing to parse")
		b.Tier = 2
		return b
	}
	best := mf.Best()
	if len(best.Morphs) == 0 {
		b.Notes = append(b.Notes, "empty morphological analysis; nothing to parse")
		b.Tier = 2
		return b
	}
	p := &jaParse{src: src, ms: best.Morphs, b: b, index: map[string]int{}}
	sort.SliceStable(p.ms, func(i, j int) bool { return p.ms[i].Start < p.ms[j].Start })
	dictChars := 0
	for i, m := range p.ms {
		m.ID = "m" + strconv.Itoa(i+1)
		b.Morphs[m.ID] = m
		p.index[m.ID] = i
		if m.Dict {
			dictChars += m.End - m.Start
		} else {
			b.Unknowns = append(b.Unknowns, m.Surface)
		}
	}
	if len(src) > 0 {
		b.Coverage = float64(dictChars) / float64(len(src))
	}
	if len(b.Unknowns) > 0 {
		b.Notes = append(b.Notes, fmt.Sprintf(
			"%d morpheme(s) could not be resolved against the lexicon and are carried as UNKNOWN (plan.md §25)",
			len(b.Unknowns)))
	}
	p.run()
	b.Tier = p.tier()
	return b
}

// --- parser state ---------------------------------------------------------

type jaParse struct {
	src     string
	ms      []*forest.Morph
	b       *Bundle
	index   map[string]int
	phraseN int
	// undetermined records that the clause structure could not be pinned down,
	// which is what forces plan.md §24's Tier 2 fallback.
	undetermined bool
}

func (p *jaParse) id(i int) string {
	if i < 0 || i >= len(p.ms) {
		return ""
	}
	return p.ms[i].ID
}

func (p *jaParse) idx(id string) int {
	if i, ok := p.index[id]; ok {
		return i
	}
	return -1
}

// terminal resolves a morpheme id to its forest terminal. An id that is not in
// this parse's index yields "" rather than an out-of-range access: the same
// guard id() applies to morpheme positions.
func (p *jaParse) terminal(term []string, id string) string {
	i := p.idx(id)
	if i < 0 || i >= len(term) {
		return ""
	}
	return term[i]
}

func (p *jaParse) spanOf(from, to int) jlir.Span {
	if from < 0 || to >= len(p.ms) || to < from {
		return jlir.Span{}
	}
	return jlir.Span{Start: p.ms[from].Start, End: p.ms[to].End}
}

func (p *jaParse) note(c *Clause, format string, args ...any) {
	c.Notes = append(c.Notes, fmt.Sprintf(format, args...))
}

// run segments the sentence and analyses every clause, then packs the result.
func (p *jaParse) run() {
	for idx, sp := range p.segment() {
		p.b.Clauses = append(p.b.Clauses, p.analyze(sp, idx))
	}
	if len(p.b.Clauses) == 0 {
		p.b.Notes = append(p.b.Notes, "no clause could be recovered from the analysis")
		p.undetermined = true
	}
	p.b.Forest = p.buildForest()
}

// tier applies plan.md §24: Tier 1 needs a fully dictionary-backed analysis and
// a fully determined clause structure. A zero subject is ordinary Japanese and
// does not degrade the tier; an unresolved attachment does.
func (p *jaParse) tier() int {
	for _, m := range p.ms {
		if !m.Dict {
			return 2
		}
	}
	if p.undetermined || len(p.b.Unknowns) > 0 {
		return 2
	}
	for _, c := range p.b.Clauses {
		if c.Matrix == "" || len(c.Alternatives) > 0 {
			return 2
		}
	}
	return 1
}

// --- clause segmentation --------------------------------------------------

// jaSpan is one clause: a morpheme range plus its head predicate.
type jaSpan struct {
	from, to int
	matrix   int
	aux      []int
	conjoin  Conjoin
}

// segment splits the morpheme sequence at finite predicates and subordinators.
func (p *jaParse) segment() []jaSpan {
	var spans []jaSpan
	cur := jaSpan{from: 0, matrix: -1}
	started := false
	awaiting := false
	linker := -1
	linkConjoin := ConjoinNone

	for i := range p.ms {
		m := p.ms[i]
		if m.POS == forest.POSPunct {
			if jaIsSentenceEnd(m) && started {
				cur.to = i
				spans = append(spans, cur)
				cur = jaSpan{from: i + 1, matrix: -1}
				started = false
				awaiting = false
				linker = -1
				linkConjoin = ConjoinNone
			}
			continue
		}
		if !started {
			cur.from = i
			started = true
		}
		if jaIsFinitePredicate(m) {
			switch {
			case cur.matrix < 0:
				if linker >= cur.from {
					cur.from = linker
				}
				cur.conjoin = linkConjoin
				linker = -1
				linkConjoin = ConjoinNone
				cur.matrix = i
				awaiting = m.Feat("conj") == "te"
			case awaiting:
				// The matrix is a te-form verb, so a following finite morpheme is
				// its auxiliary (食べて + いる), not the head of a new clause.
				cur.aux = append(cur.aux, i)
				awaiting = false
			default:
				end := i
				if linker >= cur.from {
					end = linker
				} else if i > cur.from && jaIsClauseLinker(p.ms[i-1]) {
					end = i - 1
					if linkConjoin == ConjoinNone {
						linkConjoin = jaConjoinOf(p.ms[i-1])
					}
				}
				cur.to = end
				spans = append(spans, cur)
				cur = jaSpan{from: end, matrix: i, conjoin: linkConjoin}
				linker = -1
				linkConjoin = ConjoinNone
				awaiting = m.Feat("conj") == "te"
			}
			continue
		}
		if jaIsSubordinator(m) && cur.matrix >= 0 && linker < 0 {
			linker = i
			linkConjoin = jaConjoinOf(m)
		}
	}
	if started {
		cur.to = len(p.ms) - 1
		spans = append(spans, cur)
	}
	// Fill in the head predicate of a span that only gained one during the scan.
	for i := range spans {
		if spans[i].matrix >= 0 {
			continue
		}
		for j := spans[i].from; j <= spans[i].to; j++ {
			if jaIsFinitePredicate(p.ms[j]) {
				spans[i].matrix = j
				break
			}
		}
	}
	return spans
}

func jaIsSentenceEnd(m *forest.Morph) bool {
	if m.POS != forest.POSPunct {
		return false
	}
	switch m.Surface {
	case "。", "．", ".", "!", "？", "?":
		return true
	}
	return false
}

// jaIsFinitePredicate reports whether a morpheme can head a finite clause. The
// auxiliary roles of plan.md §12 (progressive, completion) are excluded: their
// tense belongs to the verb they attach to.
func jaIsFinitePredicate(m *forest.Morph) bool {
	switch m.POS {
	case forest.POSVerb:
		return true
	case forest.POSAux:
		// An auxiliary role (progressive, completion, request) carries no
		// tense of its own, so it is never a clause head.
		if m.Feat("role") != "" && m.Feat("tense") == "" {
			return false
		}
		if m.Feat("tense") != "" || m.Feat("copula") != "" {
			return true
		}
		return m.Feat("mood") != ""
	case forest.POSAdj:
		// A な-adjective carrying the copula is a predicate (静かではない).
		return m.Feat("copula") != ""
	}
	return false
}

func jaIsSubordinator(m *forest.Morph) bool {
	if m.Feat("conj") == "te" {
		return false // the te-form connective belongs to the verb compound
	}
	return m.POS == forest.POSConj || m.Feat("conj") != ""
}

func jaIsClauseLinker(m *forest.Morph) bool {
	if m.Feat("conj") != "" {
		return true
	}
	// から is grammatically a source particle and only contextually a linker;
	// when it sits directly in front of a predicate it joins clauses.
	return m.Surface == "から" && m.Case() == "kara"
}

func jaConjoinOf(m *forest.Morph) Conjoin {
	if c := m.Feat("conj"); c != "" {
		return Conjoin(c)
	}
	switch m.Surface {
	case "から", "ので":
		return ConjoinBecause
	case "ば", "たら", "なら":
		return ConjoinIf
	case "とき":
		return ConjoinWhen
	case "けど":
		return ConjoinBut
	case "そして", "また":
		return ConjoinAnd
	case "ように", "ため":
		return ConjoinPurpose
	}
	return ConjoinNone
}

func jaIsTeConnective(m *forest.Morph) bool { return m.Feat("conj") == "te" }

func jaIsContent(m *forest.Morph) bool {
	switch m.POS {
	case forest.POSNoun, forest.POSProper, forest.POSPronoun, forest.POSNum:
		return true
	case forest.POSParticle:
		// honorific and counter nouns live in the particle section of the lexicon.
		return m.Feat("honorific_noun") != "" || m.Feat("quantifier") == "counter"
	}
	return false
}

func jaIsQuantity(m *forest.Morph) bool {
	return m.POS == forest.POSNum || m.Feat("numeral") != "" || m.Feat("quantifier") == "counter"
}

func jaIsMarkingParticle(m *forest.Morph) bool {
	switch m.Case() {
	case "ga", "wo", "ni", "de", "to", "kara", "made", "wa", "he":
		return true
	}
	return false
}

// --- clause analysis ------------------------------------------------------

// analyze builds one clause from its morpheme range.
func (p *jaParse) analyze(sp jaSpan, index int) *Clause {
	c := &Clause{
		ID:          "c" + strconv.Itoa(index+1),
		Index:       index,
		Span:        p.spanOf(sp.from, sp.to),
		Probability: 1.0,
	}
	if sp.conjoin != ConjoinNone {
		c.Conjoin = sp.conjoin
	}
	if sp.matrix < 0 {
		p.note(c, "no finite predicate in this span: kept as a fragment instead of forcing one (plan.md §25)")
		p.collectAdverbs(c, sp.from, sp.to)
		p.undetermined = true
		p.b.Notes = append(p.b.Notes, fmt.Sprintf(
			"span %q has no predicate and is carried as an unresolved node (plan.md §25)",
			c.Span.Text(p.src)))
		return c
	}
	c.Matrix = p.id(sp.matrix)
	p.morphology(c, sp)
	p.arguments(c, sp)
	p.quantifier(c)
	p.politeness(c, sp)
	p.sentiment(c)
	p.sentenceFinal(c, sp)
	p.zeroSubject(c, sp)
	p.scopeAlternatives(c)
	p.promoteAlternative(c)
	return c
}

// jaNP is a noun phrase under construction.
type jaNP struct {
	from, to  int
	head      int
	mods      []int
	relHeads  []string
	numeral   string
	counter   string
	quant     string
	conjoined []int
}

// arguments attaches every marked and unmarked argument of the clause.
func (p *jaParse) arguments(c *Clause, sp jaSpan) {
	end := sp.to
	if sp.matrix >= 0 {
		end = sp.matrix
	}
	if end > len(p.ms) {
		end = len(p.ms)
	}
	var np *jaNP
	var lastToPhrase *Phrase

	for i := sp.from; i < end; i++ {
		m := p.ms[i]
		switch {
		case jaIsTeConnective(m):
			// The te-form connective belongs to the verb+auxiliary compound.

		case m.Case() == "no":
			if np != nil && np.head >= 0 {
				if rel := p.relative(i, end, c.Index); rel != nil {
					ph := p.phraseOf(np)
					ph.Relative = rel
					heads := []string{jaRelativeSubjectHead(rel)}
					np = &jaNP{from: i + 1, head: -1, relHeads: heads}
					continue
				}
			}
			c.Adnominals = append(c.Adnominals, m.ID)

		case jaIsMarkingParticle(m):
			if np != nil && np.head >= 0 {
				ph := p.phraseOf(np)
				p.attach(c, ph, m.Case())
				if ph.Case == "to" {
					lastToPhrase = ph
				} else {
					lastToPhrase = nil
				}
			} else {
				p.note(c, "particle %s has no noun phrase to attach to", m.Surface)
				p.undetermined = true
			}
			np = nil

		case jaIsContent(m):
			switch {
			case np == nil:
				np = &jaNP{from: i, head: i, to: i + 1}
			case np.head < 0:
				np.head = i
				np.to = i + 1
			case jaIsQuantity(m):
				np.to = i + 1
				if v := m.Feat("numeral"); v != "" && np.numeral == "" {
					np.numeral = v
				}
				if m.Feat("quantifier") == "counter" {
					np.counter = m.Surface
				}
				if q := m.Feat("quantifier"); q != "" && q != "counter" {
					np.quant = q
				}
			case lastToPhrase != nil:
				// 「東京と京都」: the second conjunct shares the と-phrase.
				lastToPhrase.Conjoined = append(lastToPhrase.Conjoined, m.ID)
				p.note(c, "%s is coordinated with %s inside the と-phrase",
					m.Surface, p.ms[p.idx(lastToPhrase.Head)].Surface)
				np = &jaNP{from: i, head: i, to: i + 1}
				lastToPhrase = nil
			default:
				np = &jaNP{from: i, head: i, to: i + 1}
			}

		case m.POS == forest.POSAdj:
			switch {
			case np == nil:
				c.Modifiers = append(c.Modifiers, m.ID)
			default:
				np.mods = append(np.mods, i)
				np.to = i + 1
			}

		case m.POS == forest.POSAdv:
			c.Adverbs = append(c.Adverbs, m.ID)

		case m.POS == forest.POSPunct:
			// skipped

		default:
			if m.POS == forest.POSUnknown {
				p.note(c, "%q is UNKNOWN and is not assigned an argument role", m.Surface)
				p.undetermined = true
			}
		}
	}
	// A noun phrase directly in front of the predicate with no particle is an
	// unmarked argument: Japanese omits を freely.
	if np != nil && np.head >= 0 {
		ph := p.phraseOf(np)
		switch {
		case c.Object == nil:
			ph.Marked = false
			ph.Notes = append(ph.Notes, "unmarked argument: Japanese omits the を particle")
			c.Object = ph
		case c.Subject == nil:
			ph.Marked = false
			c.Subject = ph
			ph.Notes = append(ph.Notes,
				"unmarked subject: recoverable from the predicate frame (plan.md §15)")
		default:
			c.Adnominals = append(c.Adnominals, p.id(np.head))
			p.note(c, "noun phrase %s is not attached to any slot", ph.Span.Text(p.src))
			p.undetermined = true
		}
	}
}

// attach routes a marked noun phrase to its clause slot.
func (p *jaParse) attach(c *Clause, ph *Phrase, caseKey string) {
	ph.Marked = true
	ph.Case = caseKey
	switch caseKey {
	case "wa":
		ph.Topic = true
		if c.Topic == nil {
			c.Topic = ph
		} else {
			p.note(c, "second は-phrase %s; the earlier one stays the topic", ph.Span.Text(p.src))
			p.addIndirect(c, "wa2", ph)
		}
	case "ga":
		if c.Subject == nil {
			c.Subject = ph
		} else {
			p.note(c, "second が-phrase %s kept as ga2", ph.Span.Text(p.src))
			p.addIndirect(c, "ga2", ph)
		}
	case "wo":
		if c.Object == nil {
			c.Object = ph
		} else {
			p.addIndirect(c, "wo2", ph)
		}
	case "ni", "he", "de", "to", "kara", "made":
		key := caseKey
		if _, taken := c.Indirect[key]; taken {
			key = caseKey + "2"
			p.note(c, "second %s-phrase %s kept as %s", caseKey, ph.Span.Text(p.src), key)
		}
		p.addIndirect(c, key, ph)
	default:
		p.addIndirect(c, caseKey, ph)
	}
}

func (p *jaParse) addIndirect(c *Clause, key string, ph *Phrase) {
	if c.Indirect == nil {
		c.Indirect = map[string]*Phrase{}
	}
	c.Indirect[key] = ph
}

// phraseOf materialises a noun phrase.
func (p *jaParse) phraseOf(np *jaNP) *Phrase {
	p.phraseN++
	ph := &Phrase{
		ID:          "p" + strconv.Itoa(p.phraseN),
		Probability: 1.0,
		Span:        p.spanOf(np.from, np.to-1),
	}
	if np.head >= 0 {
		ph.Head = p.id(np.head)
		head := p.ms[np.head]
		ph.Pronoun = head.POS == forest.POSPronoun
		ph.Demonstrative = head.Feat("demonstrative")
		ph.Honorific = head.Feat("honorific_noun")
		if ph.Honorific != "" {
			p.b.Notes = append(p.b.Notes, fmt.Sprintf(
				"honorific noun %s attaches a respectful register to its referent (plan.md §5)", head.Surface))
		}
	}
	for _, i := range np.mods {
		ph.Modifiers = append(ph.Modifiers, p.id(i))
	}
	for _, id := range np.relHeads {
		if id != "" {
			ph.Modifiers = append(ph.Modifiers, id)
		}
	}
	ph.Numeral = np.numeral
	ph.Conjoined = append(ph.Conjoined, nil...)
	for _, i := range np.conjoined {
		ph.Conjoined = append(ph.Conjoined, p.id(i))
	}
	if np.counter != "" {
		ph.Notes = append(ph.Notes, "counter "+np.counter+" attached to the quantity")
		if ph.Numeral == "" {
			ph.Numeral = np.counter
		}
	}
	return ph
}

// relative builds the clause a の marks a predicate for, or nil when the の is a
// plain genitive link.
func (p *jaParse) relative(noIdx, end, index int) *Clause {
	for j := noIdx + 1; j < end; j++ {
		m := p.ms[j]
		if m.Case() == "wo" || m.Case() == "mo" {
			return nil // 「...の...を」 embeds a clause inside an argument
		}
		if jaIsFinitePredicate(m) {
			return p.embedded(noIdx+1, j+1, index)
		}
	}
	return nil
}

// embedded analyses a subordinate clause that never becomes a Bundle clause of
// its own; it is reached through Phrase.Relative.
func (p *jaParse) embedded(from, to, index int) *Clause {
	if from < 0 || from >= to || to > len(p.ms) {
		return nil
	}
	sp := jaSpan{from: from, to: to, matrix: -1}
	for j := from; j < to; j++ {
		if jaIsFinitePredicate(p.ms[j]) {
			sp.matrix = j
			break
		}
	}
	if sp.matrix < 0 {
		return nil
	}
	c := p.analyze(sp, index)
	c.ID = c.ID + "r" + strconv.Itoa(from)
	c.Conjoin = ConjoinRelative
	c.Probability = 0.9
	return c
}

// jaRelativeSubjectHead returns the morpheme id of the relative clause's most
// prominent argument, so the parent phrase can point at it.
func jaRelativeSubjectHead(c *Clause) string {
	if c == nil {
		return ""
	}
	if c.Subject != nil && c.Subject.Head != "" {
		return c.Subject.Head
	}
	if c.Topic != nil && c.Topic.Head != "" {
		return c.Topic.Head
	}
	return c.Matrix
}

// --- morphology -----------------------------------------------------------

// morphology copies the features recovered by the analyzer onto the clause,
// keeping tense, aspect and completion distinct (plan.md §12).
func (p *jaParse) morphology(c *Clause, sp jaSpan) {
	p.mergeFeats(c, sp.matrix)
	seen := map[string]bool{p.id(sp.matrix): true}
	for _, i := range sp.aux {
		id := p.id(i)
		c.Auxiliaries = append(c.Auxiliaries, id)
		seen[id] = true
		p.mergeFeats(c, i)
	}
	// Auxiliary roles that are not tensed (てしまう, てください) never head a
	// clause, so the segmentation never offered them; they are collected here
	// and their features merged into the predicate they modify (plan.md §12).
	for i := sp.from; i <= sp.to; i++ {
		m := p.ms[i]
		if m.POS != forest.POSAux || m.Feat("role") == "" || seen[m.ID] {
			continue
		}
		c.Auxiliaries = append(c.Auxiliaries, m.ID)
		seen[m.ID] = true
		p.mergeFeats(c, i)
	}
	if c.Tense == "" {
		c.Tense = "present"
	}
	if c.Polarity == "" {
		c.Polarity = "positive"
	}
	for i := sp.from; i <= sp.to && i < len(p.ms); i++ {
		m := p.ms[i]
		if m.Feat("evidential") != "" {
			c.Causation = jaAppendNote(c.Causation, "evidential")
		}
	}
}

func (p *jaParse) mergeFeats(c *Clause, i int) {
	m := p.ms[i]
	if v := m.Feat("tense"); v != "" {
		c.Tense = v
	}
	if m.Feat("negative") != "" {
		c.Polarity = "negative"
	}
	if v := m.Feat("completion"); v != "" {
		c.Completion = v
		c.Aspect = jaAspectCompletion
	} else if v := m.Feat("aspect"); v != "" {
		c.Aspect = v
	}
	if v := m.Feat("mood"); v != "" && c.Mood == "" {
		c.Mood = v
	}
	if v := m.Feat("polite"); v != "" {
		c.Modality = jaAppendNote(c.Modality, v)
	}
	if v := m.Feat("evaluative"); v != "" && c.Evaluative == "" {
		c.Evaluative = v
	}
	switch {
	case m.Feat("causative") != "" && m.Feat("passive") != "":
		c.Voice = "causative-passive"
	case m.Feat("causative") != "":
		c.Voice = "causative"
	case m.Feat("passive") != "":
		c.Voice = "passive"
	}
	switch m.Feat("honorific") {
	case "respectful":
		c.Honorific = true
		p.note(c, "respectful construction on %s: English has no natural morphological equivalent (plan.md §5)", m.Surface)
		p.b.Notes = append(p.b.Notes, fmt.Sprintf(
			"honorific content on %s: preserved internally, unrealizable on the surface (plan.md §5)", m.Surface))
	case "humble":
		c.Honorific = true
		p.note(c, "humble construction on %s: the speaker lowers themselves relative to the referent (plan.md §5)", m.Surface)
		p.b.Notes = append(p.b.Notes, fmt.Sprintf(
			"humble construction on %s: preserved internally, unrealizable on the surface (plan.md §5)", m.Surface))
	}
	if v := m.Feat("modality"); v != "" {
		c.Modality = jaAppendNote(c.Modality, v)
	}
	if v := m.Feat("role"); v != "" {
		p.note(c, "auxiliary role %s carried by %s, kept apart from the tense (plan.md §12)", v, m.Surface)
	}
}

func jaAppendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	if strings.Contains(existing, add) {
		return existing
	}
	return existing + "," + add
}

// politeness records the register: です/ます versus だ/である.
func (p *jaParse) politeness(c *Clause, sp jaSpan) {
	polite := false
	for i := sp.from; i <= sp.to && i < len(p.ms); i++ {
		switch p.ms[i].Feat("polite") {
		case "masu", "tei":
			polite = true
		case "plain":
			polite = false
		}
		if v := p.ms[i].Feat("politeness"); v == "request" || v == "command" {
			c.Mood = jaAppendNote(c.Mood, v)
		}
	}
	if polite {
		c.Politeness = "polite"
	} else {
		c.Politeness = "plain"
	}
}

// sentiment copies evaluative colouring off the predicate.
func (p *jaParse) sentiment(c *Clause) {
	for _, i := range c.Auxiliaries {
		if v := p.ms[p.idx(i)].Feat("evaluative"); v != "" && c.Evaluative == "" {
			c.Evaluative = v
		}
	}
}

// sentenceFinal records the sentence-final particles, which English has no
// morphological slot for (plan.md §20).
func (p *jaParse) sentenceFinal(c *Clause, sp jaSpan) {
	for i := sp.from; i <= sp.to && i < len(p.ms); i++ {
		if v := p.ms[i].Feat("sfp"); v != "" {
			c.SentenceFinal = append(c.SentenceFinal, p.id(i))
			p.note(c, "sentence-final particle %s (%s) is preserved as a source feature", p.ms[i].Surface, v)
		}
	}
}

func (p *jaParse) collectAdverbs(c *Clause, from, to int) {
	for i := from; i < to && i < len(p.ms); i++ {
		if p.ms[i].POS == forest.POSAdv {
			c.Adverbs = append(c.Adverbs, p.id(i))
		}
	}
}

// quantifier records a quantificative noun on the subject.
func (p *jaParse) quantifier(c *Clause) {
	for _, ph := range []*Phrase{c.Subject, c.Topic} {
		if ph == nil || ph.Head == "" {
			continue
		}
		switch q := p.ms[p.idx(ph.Head)].Feat("quantifier"); q {
		case "all", "none", "some", "both":
			c.Quantifier = q
			c.QuantifiedNP = ph.ID
			if q == "none" {
				// 誰も already restricts the domain, so no scope ambiguity is left.
				p.note(c, "%s restricts the quantifier to the empty set", ph.Span.Text(p.src))
			}
			return
		}
	}
}

// zeroSubject creates the first-class zero argument plan.md §15 requires.
// Nothing is invented: the phrase carries no referent at all, only the case
// role and the probability that one has to be supplied from discourse.
func (p *jaParse) zeroSubject(c *Clause, sp jaSpan) {
	if c.Subject != nil || sp.matrix < 0 {
		return
	}
	frame := p.ms[sp.matrix].Feat("frame")
	prob := 0.0
	switch {
	case frame == "transitive":
		prob = 0.6
		p.note(c, "no が-phrase for a transitive predicate: the subject is a zero argument (plan.md §15)")
	case frame == "intransitive" && c.Topic != nil:
		prob = 0.7
		p.note(c, "no が-phrase: the topic fills the only argument, still carried as an explicit zero (plan.md §15)")
	case frame == "intransitive":
		// Valueless clause: no agent is expected, so no zero argument is warranted.
		return
	default:
		prob = 0.35
		p.note(c, "no overt subject and no verb frame information: zero argument offered as a candidate")
	}
	if c.Topic != nil {
		prob -= 0.2
		p.note(c, "the topic may or may not fill the subject role; the readings are kept apart (plan.md §19)")
	}
	if prob <= 0 {
		return
	}
	p.phraseN++
	c.Subject = &Phrase{
		ID:          "p" + strconv.Itoa(p.phraseN),
		Case:        "ga",
		Zero:        true,
		Probability: jaRound2(prob),
		Span:        p.spanOf(sp.matrix, sp.matrix),
		Notes: []string{
			"zero anaphora: no surface realization; the referent comes from discourse, so no identity is asserted here (plan.md §15)",
		},
	}
}

// --- ambiguities ----------------------------------------------------------

// scopeAlternatives models NOT > ALL versus ALL > NOT (plan.md §13). Japanese
// puts the quantifier inside the subject, so the question is whether the
// negation reaches inside that quantified noun or scopes over the clause.
func (p *jaParse) scopeAlternatives(c *Clause) {
	if c.Quantifier == "" || c.Quantifier == "none" || c.Polarity != "negative" {
		return
	}
	// No weight preference here. The parser has no evidence about scope; the
	// semantic layer owns that judgement and records it on the ScopeNode, and
	// the D1 decision settles it. This used to assert 0.55 for NOT > ALL while
	// internal/semantics asserted 0.45 for the same reading, so the two
	// representations of one ambiguity disagreed about which reading was more
	// likely. Equal weights here make the forest weights agree in direction
	// with the semantic layer's instead of contradicting them.
	c.Probability = 0.5
	alt := *c
	alt.Alternatives = nil
	alt.Probability = 0.5
	alt.Notes = nil
	p.note(&alt, "scope: ALL > NOT — not everybody acted, so some did")
	p.note(c, "scope: NOT > ALL — nobody acted")
	for _, ph := range []*Phrase{c.Topic, c.Subject, c.Object} {
		if ph != nil && ph.ID != "" && ph.ID == c.QuantifiedNP {
			ph.Notes = append(ph.Notes, "the scope of the negation over the quantifier is unresolved")
		}
	}
	c.Alternatives = append(c.Alternatives, &alt)
	p.undetermined = true
}

// promoteAlternative models a が that may belong either to the matrix clause or
// to a の-marked relative clause: 「私が読んだ本をJensが読んだ」 has both readings.
func (p *jaParse) promoteAlternative(c *Clause) {
	if c.Subject != nil || c.Index != 0 {
		return
	}
	rel := jaFindRelativeWithSubject(c)
	if rel == nil {
		return
	}
	alt := *c
	alt.Alternatives = nil
	alt.Subject = rel
	alt.Probability = 0.35
	alt.Notes = nil
	p.note(&alt, "が is the subject of the matrix clause, not of the relative clause")
	p.note(c, "が sits inside a relative clause; the matrix-subject reading is kept (plan.md §17)")
	c.Alternatives = append(c.Alternatives, &alt)
	p.undetermined = true
}

// jaFindRelativeWithSubject returns the first overt subject that belongs to a
// relative clause hanging off this clause. The walk descends into relative
// clauses only, and each step covers a strictly smaller span, so it terminates.
func jaFindRelativeWithSubject(c *Clause) *Phrase {
	var walk func(ph *Phrase) *Phrase
	walk = func(ph *Phrase) *Phrase {
		if ph == nil || ph.Relative == nil {
			return nil
		}
		for _, cand := range []*Clause{ph.Relative} {
			if cand == nil {
				continue
			}
			others := append([]*Clause{}, cand.Alternatives...)
			for _, sub := range append([]*Clause{cand}, others...) {
				if sub != nil && sub.Subject != nil && !sub.Subject.Zero {
					return sub.Subject
				}
			}
		}
		for _, q := range jaOrderedPhrases(ph.Relative) {
			if r := walk(q); r != nil {
				return r
			}
		}
		return nil
	}
	for _, ph := range jaOrderedPhrases(c) {
		if r := walk(ph); r != nil {
			return r
		}
	}
	return nil
}

// jaOrderedPhrases lists a clause's phrases in a deterministic order.
func jaOrderedPhrases(c *Clause) []*Phrase {
	if c == nil {
		return nil
	}
	keys := make([]string, 0, len(c.Indirect))
	for k := range c.Indirect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*Phrase, 0, len(keys)+3)
	out = append(out, c.Topic, c.Subject, c.Object)
	for _, k := range keys {
		out = append(out, c.Indirect[k])
	}
	return out
}

func jaRound2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// --- packed syntactic forest ----------------------------------------------

// buildForest packs the parse into a shared-subtree forest: clauses are nodes,
// morphemes are terminals, and every ambiguity appears as a second alternative
// of the node it belongs to (plan.md §23).
func (p *jaParse) buildForest() *forest.Forest {
	fb := forest.NewBuilder("s")
	term := make([]string, len(p.ms))
	for i := range p.ms {
		m := p.ms[i]
		term[i] = fb.AddTerminal("["+m.ID+"] "+m.Surface, m.Surface, 1.0, m.Feat("rule"))
	}
	clauseNodes := make([]string, 0, len(p.b.Clauses))
	for _, c := range p.b.Clauses {
		alts := []forest.Alt{{
			Children:    p.clauseChildren(fb, term, c),
			Probability: c.Probability,
			Rule:        "matrix",
			Hard:        true,
		}}
		for _, alt := range c.Alternatives {
			if alt == nil {
				continue
			}
			alts = append(alts, forest.Alt{
				Children:    p.clauseChildren(fb, term, alt),
				Probability: alt.Probability,
				Rule:        "alternative",
				Note:        jaFirstNote(alt.Notes),
			})
		}
		clauseNodes = append(clauseNodes, fb.Add(jaClauseLabel(c), alts))
	}
	if len(clauseNodes) == 0 {
		return fb.Root(fb.Add("S", []forest.Alt{{
			Probability: 0.1,
			Rule:        "unparsed",
			Note:        "no clause recovered from the analysis",
		}}))
	}
	return fb.Root(fb.Add("S", []forest.Alt{{
		Children:    clauseNodes,
		Probability: 1.0,
		Rule:        "clause-chain",
		Hard:        true,
	}}))
}

func (p *jaParse) clauseChildren(fb *forest.Builder, term []string, c *Clause) []string {
	var kids []string
	add := func(ph *Phrase) {
		if ph == nil {
			return
		}
		alts := []forest.Alt{{
			Children:    p.phraseChildren(fb, term, ph),
			Probability: ph.Probability,
			Rule:        ph.Case,
			Hard:        true,
		}}
		if ph.Relative != nil {
			plain := *ph
			plain.Relative = nil
			plain.Notes = append(append([]string{}, ph.Notes...),
				"read without the embedded clause: the の-marked predicate is the head")
			alts = append(alts, forest.Alt{
				Children:    p.phraseChildren(fb, term, &plain),
				Probability: 0.4,
				Rule:        "relative-reading",
				Note:        "の read as a genitive link rather than as a clause marker",
			})
		}
		kids = append(kids, fb.Add(jaPhraseLabel(ph), alts))
	}
	add(c.Topic)
	add(c.Subject)
	add(c.Object)
	keys := make([]string, 0, len(c.Indirect))
	for k := range c.Indirect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(c.Indirect[k])
	}
	for _, id := range c.Adverbs {
		if t := p.terminal(term, id); t != "" {
			kids = append(kids, t)
		}
	}
	for _, id := range c.Auxiliaries {
		if t := p.terminal(term, id); t != "" {
			kids = append(kids, t)
		}
	}
	if t := p.terminal(term, c.Matrix); t != "" {
		kids = append(kids, t)
	}
	return kids
}

func (p *jaParse) phraseChildren(fb *forest.Builder, term []string, ph *Phrase) []string {
	var kids []string
	for _, id := range ph.Modifiers {
		if t := p.terminal(term, id); t != "" {
			kids = append(kids, t)
		}
	}
	for _, id := range ph.Conjoined {
		if t := p.terminal(term, id); t != "" {
			kids = append(kids, t)
		}
	}
	if t := p.terminal(term, ph.Head); t != "" {
		kids = append(kids, t)
	}
	if ph.Zero {
		kids = append(kids, fb.Add("Ø zero subject (case="+ph.Case+")", []forest.Alt{{
			Probability: ph.Probability,
			Rule:        "zero-anaphora",
			Note:        "no overt referent: the discourse layer decides (plan.md §15)",
		}}))
	}
	return kids
}

func jaClauseLabel(c *Clause) string {
	var sb strings.Builder
	sb.WriteString("CLAUSE ")
	sb.WriteString(strconv.Itoa(c.Index))
	if c.Conjoin != ConjoinNone {
		sb.WriteByte(' ')
		sb.WriteString(string(c.Conjoin))
	}
	if c.Tense != "" {
		sb.WriteString(" [")
		sb.WriteString(c.Tense)
		if c.Politeness != "" && c.Politeness != "plain" {
			sb.WriteByte('/')
			sb.WriteString(c.Politeness)
		}
		sb.WriteByte(']')
	}
	if c.Matrix != "" {
		sb.WriteString(" · ")
		sb.WriteString(c.Matrix)
	}
	return sb.String()
}

func jaPhraseLabel(ph *Phrase) string {
	label := "NP"
	switch {
	case ph.Zero:
		label = "NP zero[" + ph.Case + "]"
	case ph.Topic:
		label = "NP topic[" + ph.Case + "]"
	case ph.Marked:
		label = "NP[" + ph.Case + "]"
	}
	if ph.Relative != nil {
		label += " +relative"
	}
	return label
}

func jaFirstNote(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return notes[0]
}
