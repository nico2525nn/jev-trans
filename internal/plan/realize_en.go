package plan

// realize_en.go: the English morphological realizer.
//
// It turns one planned event into English surface strings: full verb morphology
// (regular and irregular, -s/-es, -ed with e-drop, y->ied and CVC doubling,
// -ing with e-drop and doubling), auxiliary selection for the progressive and
// the perfect, do-support negation, subject-verb agreement, determiner choice,
// a/an by initial sound, the clausal complement joiners, and word order.
//
// Every decision that could have gone another way is an alternative on a
// shared node, and every decision that must not go another way is a hard
// constraint recorded in the ledger.

import (
	"strings"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
)

// enFamilyLemma maps a construction family onto its English lemma for the
// families whose lemma is not simply the lower cased family name.
var enFamilyLemma = map[string]string{
	"NOMINAL": "",
	"EXIST":   "there is",
	"NAMED":   "be called",
	"ABLE":    "be able",
	"BE":      "be",
	"COPULA":  "be",
}

// enLemmaFor returns the English lemma of a family.
func enLemmaFor(family string) string {
	if l, ok := enFamilyLemma[family]; ok {
		return l
	}
	return strings.ToLower(family)
}

// enAuxPhrase is a verb phrase made of an inflected auxiliary plus a literal
// tail ("is able to", "was called").
type enAuxPhrase struct {
	aux  string
	tail string
}

// enSplit decomposes a multiword lemma into its inflected auxiliary and its
// literal tail.
func enSplit(lemma string) enAuxPhrase {
	switch lemma {
	case "be called":
		return enAuxPhrase{aux: "be", tail: " called"}
	case "be named":
		return enAuxPhrase{aux: "be", tail: " named"}
	case "be able":
		return enAuxPhrase{aux: "be", tail: " able to"}
	case "have to":
		return enAuxPhrase{aux: "have", tail: " to"}
	case "would like":
		return enAuxPhrase{aux: "would", tail: " like"}
	case "there is":
		return enAuxPhrase{aux: "", tail: "there is"}
	case "there are":
		return enAuxPhrase{aux: "", tail: "there are"}
	case "is allowed to":
		return enAuxPhrase{aux: "is", tail: " allowed to"}
	}
	return enAuxPhrase{aux: "", tail: ""}
}

// enExistential renders "there is", "there was", "there will be" and their
// negations. An existential has no lexical verb, so the whole predicate is the
// tail of the verb phrase.
func enExistential(tense, polarity string) string {
	negative := strings.EqualFold(polarity, jlir.PolarityNegative)
	switch tense {
	case jlir.TensePast:
		if negative {
			return "there was not"
		}
		return "there was"
	case jlir.TenseFuture:
		if negative {
			return "there will not be"
		}
		return "there will be"
	}
	if negative {
		return "there is not"
	}
	return "there is"
}

// enBe renders the be verb with agreement.
func enBe(tense string, person int, plural bool) string {
	switch tense {
	case jlir.TensePast:
		if person == 1 {
			return "was"
		}
		if plural {
			return "were"
		}
		return "was"
	default:
		if person == 1 {
			return "am"
		}
		if plural {
			return "are"
		}
		return "is"
	}
}

// enHave renders have with agreement.
func enHave(tense string, person int, plural bool) string {
	if tense == jlir.TensePast {
		return "had"
	}
	if person == 3 && !plural {
		return "has"
	}
	return "have"
}

// enAgreement returns the person and number agreement features of a subject.
func enAgreement(np *NPPlan) (person int, plural bool) {
	if np == nil {
		return 3, false
	}
	person = np.Person
	if np.Number == jlir.NumberPlural && person != 1 {
		plural = true
	}
	if np.Pronoun == "they" && person != 1 {
		plural = true
	}
	if np.Pronoun == "I" {
		person, plural = 1, false
	}
	if person == 0 {
		person = 3
	}
	return person, plural
}

// enAuxForm conjugates an auxiliary with the subject's agreement features.
// Only be, have and do inflect; a modal auxiliary such as "would" is already
// finite and stays as it is.
func enAuxForm(aux, tense string, person int, plural bool) string {
	switch strings.ToLower(aux) {
	case "be":
		return enBe(tense, person, plural)
	case "have":
		return enHave(tense, person, plural)
	case "do":
		return enDoSupport(tense, person, plural)
	}
	return aux
}

// enDoSupport returns the do-form used for negation.
func enDoSupport(tense string, person int, plural bool) string {
	if tense == jlir.TensePast {
		return "did"
	}
	if tense == jlir.TenseFuture {
		return "will"
	}
	if person != 3 || plural {
		return "do"
	}
	return "does"
}

// enInflect returns the finite form of an English verb.
func enInflect(lemma, tense string, person int, plural bool) string {
	v := lookupEN(lemma)
	switch tense {
	case jlir.TensePast:
		return v.past
	case jlir.TenseFuture:
		return "will " + v.base
	}
	if person == 3 && !plural {
		return v.third
	}
	return v.base
}

// enVerbPiece builds the predicate of one clause: the finite verb, its
// auxiliaries, do-support negation and the agreement of the subject.
func (rt *realizer) enVerbPiece(ep *EventPlan, c *Construction, depth int) []forest.Alt {
	subj := resolveSubject(ep, c)
	if rt.noSubject {
		subj = nil
	}
	person, plural := enAgreement(subj)
	negative := strings.EqualFold(ep.Polarity, jlir.PolarityNegative)
	if rt.bareVerb {
		// A control complement is a bare infinitive: "wants to go", never
		// "wants to goes". The embedded clause has no subject, so it cannot
		// agree with one either.
		person, plural = 3, true
	}

	lemma := c.Lex
	if lemma == "" {
		lemma = enLemmaFor(c.Family)
	}
	lead, verb, tail := rt.enVerbParts(ep, c, lemma, tenseOf(ep), person, plural, negative)
	if rt.bareVerb && verb != "" {
		verb = baseOf(verb)
	}
	words := append([]string(nil), lead...)
	if verb != "" {
		words = append(words, verb)
	}
	if tail != "" {
		words = append(words, tail)
	}
	lex := strings.TrimSpace(strings.Join(words, " "))
	if lex == "" {
		return nil
	}
	// Negation that no part of the phrase expressed is a hard failure: better an
	// empty branch than a silently affirmative sentence.
	if negative && !strings.Contains(lex, "not") {
		rt.reject(string(ep.EventID), lex, HardPolarity,
			"negative polarity of "+string(ep.EventID)+" has no realization", "realization")
		return nil
	}
	return []forest.Alt{{Lex: lex, Probability: c.Naturalness, Rule: "<" + c.ID + ">", Hard: true}}
}

// tenseOf returns the planned tense.
func tenseOf(ep *EventPlan) string {
	if ep == nil || ep.Tense == "" {
		return jlir.TensePresent
	}
	return ep.Tense
}

// complementArg returns the clausal argument of a construction with a
// complement strategy.
func complementArg(ep *EventPlan) *NPPlan {
	for _, role := range []string{jlir.RoleTheme, jlir.RolePatient, jlir.RoleStimulus} {
		if np := ep.Args[role]; np != nil && np.IsClause {
			return np
		}
	}
	for _, role := range sortedArgs(ep) {
		if np := ep.Args[role]; np != nil && np.IsClause {
			return np
		}
	}
	return nil
}

// sortedArgs returns the argument roles in a deterministic order.
func sortedArgs(ep *EventPlan) []string {
	if ep == nil {
		return nil
	}
	out := make([]string, 0, len(ep.Args))
	for k := range ep.Args {
		out = append(out, k)
	}
	return sortStrings(out)
}

// enVerbParts splits a predicate into leading auxiliaries, the finite verb and
// the literal tail. It is the whole English auxiliary machinery in one place:
//
//	future      will + base
//	progressive be + not + -ing
//	perfect     have + not + past participle
//	negation    do/does/did + not + base (do-support)
//	modal       modal + not + base
//	otherwise   tense and agreement inflection of the lemma
func (rt *realizer) enVerbParts(ep *EventPlan, c *Construction, lemma, tense string, person int, plural, negative bool) (lead []string, verb, tail string) {
	// An existential has no lexical verb at all.
	if strings.HasPrefix(lemma, "there ") {
		return nil, "", enExistential(tense, ep.Polarity)
	}
	split := enSplit(lemma)
	phrasal := split.aux != ""
	modal := ""
	switch strings.ToLower(ep.Aux) {
	case "must", "should", "may":
		modal = strings.ToLower(ep.Aux)
	case "will":
		if tense != jlir.TensePast {
			modal = "will"
		}
	}
	if modal == "" && isModalLemma(lemma) {
		modal = lemma
		split = enAuxPhrase{}
		phrasal = false
	}

	// A phrasal predicate ("is called", "has to", "is able to") carries its own
	// auxiliary and its own tail, so tense and agreement apply to the auxiliary
	// and nothing else is inflected.
	if phrasal {
		verb = enAuxForm(split.aux, tense, person, plural)
		if negative && !strings.Contains(verb, "not") {
			verb += " not"
		}
		return nil, verb, strings.TrimSpace(split.tail)
	}

	switch strings.ToUpper(ep.Aspect) {
	case jlir.AspectProgressive:
		verb = enBe(tense, person, plural)
		if negative {
			verb += " not"
		}
		return nil, verb, " " + ingForm(lemma)
	case jlir.AspectPerfect:
		verb = enHave(tense, person, plural)
		if negative {
			verb += " not"
		}
		return nil, verb, pastParticiple(lemma)
	}

	if modal != "" {
		verb = modal
		if negative {
			verb += " not"
		}
		return nil, verb, baseOf(lemma)
	}

	if negative {
		if tense == jlir.TenseFuture {
			return []string{"will", "not"}, baseOf(lemma), ""
		}
		return []string{enDoSupport(tense, person, plural), "not"}, baseOf(lemma), ""
	}
	return nil, enInflect(lemma, tense, person, plural), ""
}

// isModalLemma reports whether a lemma is a modal auxiliary.
func isModalLemma(lemma string) bool {
	switch strings.ToLower(lemma) {
	case "can", "must", "should", "may", "might", "will", "shall", "could", "would":
		return true
	}
	return false
}

// baseOf returns the dictionary form of an English verb.
func baseOf(lemma string) string { return lookupEN(lemma).base }

// --- English noun phrases -------------------------------------------------

// enNP realizes one planned argument as an English noun phrase node, including
// its preposition. An omitted argument produces no node at all: the absence is
// the realization (plan.md §3).
func (rt *realizer) enNP(np *NPPlan, slot string, ep *EventPlan, initial bool, prep string, depth int) string {
	if np == nil || np.Omitted() {
		return ""
	}
	if np.IsClause {
		// An argument bound to an event surfaces as a clause. Whether it keeps
		// its subject is the construction's business: a control complement has
		// none, a bare complement does.
		if np.Clause == nil {
			rt.reject(slot, "", HardMissingArg, "clausal argument has no plan", "realization")
			return ""
		}
		return rt.clauseNode(np.Clause, false, depth+1)
	}
	alts := rt.enNPAlts(np, slot, prep, initial)
	if len(alts) == 0 {
		return ""
	}
	alts = pruneSlot(slot, alts, npLoss)
	rt.countSlot(slot, len(alts))
	return rt.altNode("NP", alts)
}

// npLoss is the per-alternative loss used for Pareto pruning inside a slot: an
// alternative that loses on referential or pragmatic loss without being more
// likely is dropped.
func npLoss(a forest.Alt) LossVector {
	var v LossVector
	if meta := a.Note; meta != "" {
		switch {
		case strings.Contains(meta, "undetermined"):
			v.Referential += 0.2
		case strings.Contains(meta, "zero"):
			v.Referential += 0.1
		}
	}
	if a.Probability < 0.2 {
		v.Stylistic += 0.1
	}
	return v
}

// enNPAlts builds the alternatives of one English noun phrase.
func (rt *realizer) enNPAlts(np *NPPlan, slot, prep string, initial bool) []forest.Alt {
	var alts []forest.Alt
	pre := ""
	if prep != "" {
		pre = prep + " "
	}

	// --- pronoun reading -------------------------------------------------
	pronouns := np.PronounOpts
	if np.Pronoun != "" && !containsString(pronouns, np.Pronoun) {
		pronouns = append([]string{np.Pronoun}, pronouns...)
	}
	if len(pronouns) > 0 {
		for i, pron := range pronouns {
			if !rt.pronounAllowed(np, pron, slot) {
				continue
			}
			lex := pre + pron
			alts = append(alts, forest.Alt{
				Lex: lex, Probability: pronProb(np, pron, i), Hard: true,
				Rule: "<pronoun>", Note: "pronoun realization",
			})
		}
	}

	// --- noun phrase reading ---------------------------------------------
	head := np.Noun
	if head == "" {
		head = aliasFor(rt.g, rt.entity(np), rt.l)
	}
	if head == "" {
		// The graph knows the referent only in the source language; the
		// caller's resolver supplies the target surface. Declining here means a
		// genuine lexical gap, which the hard constraint below reports.
		head = lexicalize(rt.p, rt.g, rt.entity(np), rt.l)
	}
	if head == "" {
		if len(alts) == 0 {
			rt.reject(slot, "", HardReferentEmpty,
				"no English surface form and no licensed pronoun for the "+string(np.EntityID), "realization")
		}
		return alts
	}
	dets := np.DeterminerOpts
	if len(dets) == 0 && np.Determiner != "" {
		dets = []string{np.Determiner}
	}
	if len(dets) == 0 {
		dets = []string{""}
	}
	numbers := np.NumberOpts
	if len(numbers) == 0 && np.Number != "" {
		numbers = []string{np.Number}
	}
	if len(numbers) == 0 {
		numbers = []string{jlir.NumberSingular}
	}
	proper := np.Proper || hasNameAlias(rt.entity(np), lang.EN)
	for _, num := range numbers {
		if np.NumberDetermined && np.Number != "" && num != np.Number {
			rt.reject(slot, pluralize(head, num), HardNumber,
				"number "+num+" contradicts the sourced number "+np.Number+" of "+string(np.EntityID), "realization")
			continue
		}
		noun := pluralize(head, num)
		for i, det := range dets {
			if proper && (det == "a" || det == "an") {
				rt.reject(slot, strings.TrimSpace(det+" "+noun), HardDeterminer,
					"an indefinite article cannot introduce a proper name", "realization")
				continue
			}
			if proper && det != "" {
				continue
			}
			body := strings.TrimSpace(strings.Join(append([]string{det}, append(append([]string{}, np.Adjectives...), noun)...), " "))
			if body == "" {
				continue
			}
			lex := pre + body
			prob := detProb(det, i, len(dets)) * numberProb(num, i)
			note := ""
			if !np.NumberDetermined {
				note = "undetermined number"
			}
			alts = append(alts, forest.Alt{
				Lex: lex, Probability: prob, Hard: true, Rule: "<np>", Note: note,
			})
		}
	}
	// Capitalize the first word of the sentence when this phrase opens it. The
	// lower case reading is not a rival candidate: an English sentence does not
	// begin with a lower case word, so it is recorded as dominated rather than
	// ranked.
	if initial {
		kept := make([]forest.Alt, 0, len(alts))
		for _, a := range alts {
			upper := capitalize(a.Lex)
			if upper != a.Lex {
				a.Lex = upper
				rt.reject(slot, strings.ToLower(a.Lex), SoftDominance,
					"an English sentence does not start with a lower case word", "realization")
			}
			kept = append(kept, a)
		}
		alts = kept
	}
	return alts
}

// pronProb ranks pronoun alternatives: the chosen one first, then the rest.
func pronProb(np *NPPlan, pron string, idx int) float64 {
	if np.Pronoun == pron {
		return 0.95
	}
	return 0.6 / float64(1+idx)
}

// detProb ranks determiner alternatives.
func detProb(det string, idx, total int) float64 {
	if idx == 0 {
		return 0.95
	}
	return 0.4 / float64(1+idx*total)
}

// numberProb ranks number alternatives.
func numberProb(num string, idx int) float64 {
	if idx == 0 {
		return 1
	}
	return 0.5
}

// containsString reports membership.
func containsString(in []string, s string) bool {
	for _, x := range in {
		if x == s {
			return true
		}
	}
	return false
}

// capitalize upper cases the first letter of a lexeme, leaving Japanese and
// already-capital material alone.
func capitalize(s string) string {
	for i, r := range s {
		if r >= 'a' && r <= 'z' {
			return s[:i] + strings.ToUpper(string(r)) + s[i+len(string(r)):]
		}
		if r < 0x80 {
			return s
		}
		return s
	}
	return s
}

// --- the clause -----------------------------------------------------------

// framesEN materializes one event plan into one frame per surviving English
// construction.
func (rt *realizer) framesEN(ep *EventPlan, first bool, depth int) []frame {
	var out []frame
	for _, c := range rt.constructionsOf(ep) {
		if ok, rule, why := rt.checkConstruction(ep, c); !ok {
			rt.reject(string(ep.EventID), c.ID, rule, why, "realization")
			continue
		}
		parts := rt.patternEN(ep, c, first, depth)
		if rt.unextendable(parts) {
			// Material after an embedded clause cannot be concatenated: the
			// packed forest prefixes to a subtree but never appends to one.
			rt.reject(string(ep.EventID), c.ID, HardMissingArg,
				"construction "+c.ID+" puts material after an embedded clause", "realization")
			continue
		}
		// English separates words; Japanese does not. The first piece of the
		// sentence carries no leading space, and the punctuation none.
		for i := range parts {
			parts[i].alts = enSpaceAlts(parts[i].alts, first && i == 0)
		}
		if !rt.clauseFinal {
			// A sentence with several clauses closes each one of them.
			parts = append(parts, piece{label: "PUNCT",
				alts: []forest.Alt{{Lex: rt.punctuation(rt.p), Probability: 0.9, Hard: true}}})
		}
		parts = append(parts, rt.idPiece(c))
		if len(parts) <= 1 {
			continue // nothing but the id: the frame has no material
		}
		out = append(out, frame{c: c, parts: parts})
	}
	return out
}

// unextendable reports whether a frame places material after an embedded clause,
// which the packed forest cannot express: an alternative's lexeme is prefixed to
// its child, never appended to it. Such a frame is declined rather than
// silently truncated.
func (rt *realizer) unextendable(parts []piece) bool {
	for i, p := range parts {
		if p.clause == nil {
			continue
		}
		for j := i + 1; j < len(parts); j++ {
			if parts[j].clause == nil && len(parts[j].alts) > 0 {
				return true
			}
		}
	}
	return false
}

// patternEN walks a construction pattern and materializes its slots in order.
// A bare token before a slot is the preposition binding it; JOIN contributes the
// complement joiner; a slot bound to an event contributes the embedded clause.
func (rt *realizer) patternEN(ep *EventPlan, c *Construction, first bool, depth int) []piece {
	var parts []piece
	prep := ""
	openIdx := len(parts)
	add := func(p piece) {
		if p.empty() {
			return
		}
		parts = append(parts, p)
	}
	for _, tok := range tokens(c.Pattern) {
		switch tok {
		case "SUBJ":
			if rt.noSubject {
				// A control complement is a bare predicate: "wants to go".
				continue
			}
			np := resolveSubject(ep, c)
			p := rt.enNPPiece(np, "SUBJ", prep, first && len(parts) == openIdx, depth, c)
			prep = ""
			add(p)
		case "V":
			add(piece{label: "V", alts: rt.enVerbPiece(ep, c, depth)})
		case "JOIN":
			if c.Join != "" {
				add(piece{label: "JOIN", alts: []forest.Alt{{Lex: c.Join, Probability: 0.95, Hard: true}}})
			}
		case "TAIL":
			if c.Tail != "" {
				add(piece{label: "TAIL", alts: []forest.Alt{{Lex: c.Tail, Probability: 0.9, Hard: true}}})
			}
		case "quote":
			// An English clause never precedes its verb.
		case "OBJ":
			role := objectRole(ep)
			if rt.implicitJoin(ep, c, ep.Args[role]) {
				add(piece{label: "JOIN", alts: []forest.Alt{{Lex: "to", Probability: 0.8, Hard: true}}})
			}
			p := rt.enNPPiece(ep.Args[role], "OBJ", prep, false, depth, c)
			prep = ""
			add(p)
		default:
			if isSlotToken(tok) {
				role := roleOfToken(tok)
				pre := prep
				if pre == "" {
					pre = rt.prepositionFor(c, role)
				}
				if rt.implicitJoin(ep, c, ep.Args[role]) {
					add(piece{label: "JOIN", alts: []forest.Alt{{Lex: "to", Probability: 0.8, Hard: true}}})
				}
				p := rt.enNPPiece(ep.Args[role], tok, pre, false, depth, c)
				prep = ""
				add(p)
				continue
			}
			prep = tok // a preposition binding the next slot
		}
	}
	return parts
}

// enSpace returns the lexeme with the leading space English needs between
// words. The packed forest concatenates lexemes without consulting the spacing
// rule of the surface renderer, so the space belongs here; the first piece of a
// clause carries none.
func enSpace(lex string, first bool) string {
	if lex == "" || first {
		return lex
	}
	return " " + lex
}

// enSpaceAlts applies enSpace to every alternative of a piece.
func enSpaceAlts(alts []forest.Alt, first bool) []forest.Alt {
	if first {
		return alts
	}
	out := make([]forest.Alt, len(alts))
	for i, a := range alts {
		if len(a.Children) == 0 {
			a.Lex = enSpace(a.Lex, first)
		} else {
			// A subtree child is concatenated verbatim, so the separating space
			// has to be written after this piece's own material.
			a.Lex = strings.TrimRight(a.Lex, " ")
			if a.Lex != "" {
				a.Lex += " "
			}
		}
		out[i] = a
	}
	return out
}

// implicitJoin reports whether a clausal argument needs an English joiner the
// construction does not declare. English forms a bare infinitive complement out
// of a control verb ("wants to go"), so a construction without a complement
// strategy still has to supply the "to"; supplying nothing would give "wants
// go".
func (rt *realizer) implicitJoin(ep *EventPlan, c *Construction, np *NPPlan) bool {
	return np != nil && np.IsClause && c != nil && c.Complement == "" && c.Join == ""
}

// enNPPiece materializes one argument slot. An argument bound to an event
// becomes an embedded clause piece, which the chain builder hands the material
// that follows it.
func (rt *realizer) enNPPiece(np *NPPlan, slot, prep string, initial bool, depth int, c *Construction) piece {
	if np == nil || np.Omitted() {
		return piece{label: slot}
	}
	if np.IsClause {
		if np.Clause == nil {
			rt.reject(slot, "", HardMissingArg, "clausal argument has no plan", "realization")
			return piece{label: slot}
		}
		// A control complement has no subject of its own; a full clause
		// complement keeps it.
		return piece{label: slot, clause: np, depth: depth + 1,
			noSubject: c == nil || c.Complement != "that"}
	}
	return piece{label: slot, alts: rt.enNPAlts(np, slot, prep, initial)}
}

// objectRole picks the direct object role of an event.
func objectRole(ep *EventPlan) string {
	for _, role := range []string{jlir.RolePatient, jlir.RoleTheme, jlir.RoleStimulus} {
		if np := ep.Args[role]; np != nil {
			return role
		}
	}
	return jlir.RolePatient
}

// --- English morphology ---------------------------------------------------

// enVerb is a resolved English verb: the four forms every inflection needs.
type enVerb struct {
	base    string // dictionary form
	third   string // third person singular present
	past    string // simple past
	pp      string // past participle
	prog    string // -ing form
	irregul bool
}

// enIrregular holds the high frequency verbs whose morphology no rule can
// produce. Forms are base/third/past/participle; "be" additionally needs the
// irregular person forms, which enBe handles.
var enIrregular = map[string][4]string{
	"be":         {"be", "is", "was", "been"},
	"have":       {"have", "has", "had", "had"},
	"do":         {"do", "does", "did", "done"},
	"go":         {"go", "goes", "went", "gone"},
	"come":       {"come", "comes", "came", "come"},
	"get":        {"get", "gets", "got", "gotten"},
	"give":       {"give", "gives", "gave", "given"},
	"take":       {"take", "takes", "took", "taken"},
	"make":       {"make", "makes", "made", "made"},
	"say":        {"say", "says", "said", "said"},
	"tell":       {"tell", "tells", "told", "told"},
	"speak":      {"speak", "speaks", "spoke", "spoken"},
	"think":      {"think", "thinks", "thought", "thought"},
	"know":       {"know", "knows", "knew", "known"},
	"see":        {"see", "sees", "saw", "seen"},
	"hear":       {"hear", "hears", "heard", "heard"},
	"meet":       {"meet", "meets", "met", "met"},
	"read":       {"read", "reads", "read", "read"},
	"write":      {"write", "writes", "wrote", "written"},
	"run":        {"run", "runs", "ran", "run"},
	"sit":        {"sit", "sits", "sat", "sat"},
	"stand":      {"stand", "stands", "stood", "stood"},
	"buy":        {"buy", "buys", "bought", "bought"},
	"bring":      {"bring", "brings", "brought", "brought"},
	"catch":      {"catch", "catches", "caught", "caught"},
	"teach":      {"teach", "teaches", "taught", "taught"},
	"sell":       {"sell", "sells", "sold", "sold"},
	"spend":      {"spend", "spends", "spent", "spent"},
	"send":       {"send", "sends", "sent", "sent"},
	"leave":      {"leave", "leaves", "left", "left"},
	"lose":       {"lose", "loses", "lost", "lost"},
	"pay":        {"pay", "pays", "paid", "paid"},
	"lay":        {"lay", "lays", "laid", "laid"},
	"lead":       {"lead", "leads", "led", "led"},
	"find":       {"find", "finds", "found", "found"},
	"hold":       {"hold", "holds", "held", "held"},
	"keep":       {"keep", "keeps", "kept", "kept"},
	"sleep":      {"sleep", "sleeps", "slept", "slept"},
	"feel":       {"feel", "feels", "felt", "felt"},
	"fall":       {"fall", "falls", "fell", "fallen"},
	"grow":       {"grow", "grows", "grew", "grown"},
	"fly":        {"fly", "flies", "flew", "flown"},
	"draw":       {"draw", "draws", "drew", "drawn"},
	"break":      {"break", "breaks", "broke", "broken"},
	"choose":     {"choose", "chooses", "chose", "chosen"},
	"forget":     {"forget", "forgets", "forgot", "forgotten"},
	"understand": {"understand", "understands", "understood", "understood"},
	"eat":        {"eat", "eats", "ate", "eaten"},
	"drink":      {"drink", "drinks", "drank", "drunk"},
	"sing":       {"sing", "sings", "sang", "sung"},
	"swim":       {"swim", "swims", "swam", "swum"},
	"begin":      {"begin", "begins", "began", "begun"},
	"wear":       {"wear", "wears", "wore", "worn"},
	"win":        {"win", "wins", "won", "won"},
	"throw":      {"throw", "throws", "threw", "thrown"},
	"hurt":       {"hurt", "hurts", "hurt", "hurt"},
	"put":        {"put", "puts", "put", "put"},
	"cut":        {"cut", "cuts", "cut", "cut"},
	"set":        {"set", "sets", "set", "set"},
	"open":       {"open", "opens", "opened", "opened"},
	"close":      {"close", "closes", "closed", "closed"},
	"start":      {"start", "starts", "started", "started"},
	"stop":       {"stop", "stops", "stopped", "stopped"},
	"work":       {"work", "works", "worked", "worked"},
	"live":       {"live", "lives", "lived", "lived"},
	"show":       {"show", "shows", "showed", "shown"},
	"arrive":     {"arrive", "arrives", "arrived", "arrived"},
	"want":       {"want", "wants", "wanted", "wanted"},
	"like":       {"like", "likes", "liked", "liked"},
	"move":       {"move", "moves", "moved", "moved"},
	"walk":       {"walk", "walks", "walked", "walked"},
	"play":       {"play", "plays", "played", "played"},
	"study":      {"study", "studies", "studied", "studied"},
	"cry":        {"cry", "cries", "cried", "cried"},
	"carry":      {"carry", "carries", "carried", "carried"},
	"reply":      {"reply", "replies", "replied", "replied"},
	"plan":       {"plan", "plans", "planned", "planned"},
	"visit":      {"visit", "visits", "visited", "visited"},
	"answer":     {"answer", "answers", "answered", "answered"},
	"ask":        {"ask", "asks", "asked", "asked"},
	"call":       {"call", "calls", "called", "called"},
	"need":       {"need", "needs", "needed", "needed"},
	"mean":       {"mean", "means", "meant", "meant"},
}

// enReverse maps an inflected form back to its dictionary form, so a lexicon
// or a construction that hands over "went" still realizes correctly.
var enReverse = func() map[string]string {
	m := map[string]string{}
	for _, v := range enIrregular {
		for _, f := range v[1:] {
			m[f] = v[0]
		}
	}
	return m
}()

// lookupEN resolves a lemma into its forms, reversing an inflected input when
// necessary.
func lookupEN(lemma string) enVerb {
	if lemma == "" {
		return enVerb{base: "", third: "", past: "", pp: "", prog: ""}
	}
	if base, ok := enReverse[lemma]; ok {
		lemma = base
	}
	if f, ok := enIrregular[lemma]; ok {
		return enVerb{base: f[0], third: f[1], past: f[2], pp: f[3],
			prog: ingOfBase(f[0]), irregul: true}
	}
	return enVerb{
		base:    lemma,
		third:   thirdPerson(lemma),
		past:    pastTense(lemma),
		pp:      pastTense(lemma),
		prog:    ingOfBase(lemma),
		irregul: false,
	}
}

// baseOfIrregular keeps the dictionary form of an irregular verb.
func baseOfIrregular(lemma string) string { return lookupEN(lemma).base }

// syllables is a rough count used only by the doubling rule, which applies to
// monosyllables (and to the handful of disyllabic exceptions listed below).
func syllables(w string) int {
	n := 0
	prevVowel := false
	for _, r := range strings.ToLower(w) {
		vowel := r == 'a' || r == 'e' || r == 'i' || r == 'o' || r == 'u'
		if vowel && !prevVowel {
			n++
		}
		prevVowel = vowel
	}
	if n == 0 {
		return 1
	}
	return n
}

// enNoDouble lists the words the doubling rule must not touch.
var enNoDouble = map[string]bool{
	"visit": true, "edit": true, "limit": true, "profit": true, "orbit": true,
	"happen": true, "open": true, "offer": true, "suffer": true, "proper": true,
	"travel": true, "cancel": true, "label": true, "model": true, "signal": true,
	"marvel": true, "level": true, "equal": true, "murmur": true,
}

// isVowel reports whether c is an English vowel letter.
func isVowel(c byte) bool {
	switch c {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

// isConsonant reports whether c is an English consonant letter.
func isConsonant(c byte) bool {
	return c >= 'a' && c <= 'z' && !isVowel(c)
}

// needsDoubling applies the CVC rule: a one syllable consonant-vowel-consonant
// ending doubles before -ed and -ing, except after w, x and y.
func needsDoubling(w string) bool {
	l := strings.ToLower(w)
	if len(l) < 3 || enNoDouble[l] {
		return false
	}
	if syllables(l) != 1 {
		return false
	}
	a, b, c := l[len(l)-3], l[len(l)-2], l[len(l)-1]
	if !isVowel(b) || !isConsonant(c) || !isConsonant(a) {
		return false
	}
	switch c {
	case 'w', 'x', 'y':
		return false
	}
	// Two consonants in a row at the end ("stomp") are already closed.
	if isConsonant(a) {
		return false
	}
	return true
}

// doubleLast returns w with its final consonant doubled.
func doubleLast(w string) string {
	if w == "" {
		return w
	}
	return w[:len(w)-1] + string(w[len(w)-1]) + string(w[len(w)-1])
}

// thirdPerson returns the -s form: -es after a sibilant, y -> ies after a
// consonant, plain -s otherwise.
func thirdPerson(lemma string) string {
	l := strings.ToLower(lemma)
	if l == "" {
		return ""
	}
	switch {
	case strings.HasSuffix(l, "s"), strings.HasSuffix(l, "x"), strings.HasSuffix(l, "z"),
		strings.HasSuffix(l, "ch"), strings.HasSuffix(l, "sh"), strings.HasSuffix(l, "o"):
		return lemma + "es"
	case strings.HasSuffix(l, "y") && len(l) > 1 && !isVowel(l[len(l)-2]):
		return lemma[:len(lemma)-1] + "ies"
	}
	return lemma + "s"
}

// pastTense returns the -ed form with e-drop, y -> ied and CVC doubling.
func pastTense(lemma string) string {
	l := strings.ToLower(lemma)
	if l == "" {
		return ""
	}
	switch {
	case strings.HasSuffix(l, "e"):
		return lemma + "d"
	case strings.HasSuffix(l, "y") && len(l) > 1 && !isVowel(l[len(l)-2]):
		return lemma[:len(lemma)-1] + "ied"
	case needsDoubling(l):
		return doubleLast(lemma) + "ed"
	}
	return lemma + "ed"
}

// pastParticiple returns the past participle.
func pastParticiple(lemma string) string { return lookupEN(lemma).pp }

// ingOfBase returns the -ing form of a known dictionary form.
func ingOfBase(base string) string {
	l := strings.ToLower(base)
	if l == "" {
		return ""
	}
	if strings.HasSuffix(l, "ie") {
		return base[:len(base)-2] + "ying"
	}
	if strings.HasSuffix(l, "e") && !strings.HasSuffix(l, "ee") && !strings.HasSuffix(l, "oe") {
		return base[:len(base)-1] + "ing"
	}
	if needsDoubling(l) {
		return doubleLast(base) + "ing"
	}
	return base + "ing"
}

// ingForm returns the -ing form.
func ingForm(lemma string) string { return lookupEN(lemma).prog }

// pluralize returns the plural of an English noun, honouring the same sibilant
// and y rules as the verb inflection plus the irregular nouns the system cannot
// do without.
func pluralize(noun, number string) string {
	if noun == "" {
		return ""
	}
	if number != jlir.NumberPlural {
		return noun
	}
	switch strings.ToLower(noun) {
	case "man":
		return "men"
	case "woman":
		return "women"
	case "child":
		return "children"
	case "person":
		return "people"
	case "foot":
		return "feet"
	case "tooth":
		return "teeth"
	case "mouse":
		return "mice"
	case "this":
		return "these"
	case "it":
		return "they"
	}
	l := strings.ToLower(noun)
	switch {
	case strings.HasSuffix(l, "s"), strings.HasSuffix(l, "x"), strings.HasSuffix(l, "z"),
		strings.HasSuffix(l, "ch"), strings.HasSuffix(l, "sh"):
		return noun + "es"
	case strings.HasSuffix(l, "y") && len(l) > 1 && !isVowel(l[len(l)-2]):
		return noun[:len(noun)-1] + "ies"
	case strings.HasSuffix(l, "f"):
		return noun[:len(noun)-1] + "ves"
	}
	return noun + "s"
}
