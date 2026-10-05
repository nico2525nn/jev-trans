package plan

// realize.go: the packed realization forest and the constraint machinery
// (plan.md §36–§39, §45).
//
// The candidate space is never materialized as a million strings. Shared
// material is interned, so "Hanako", "a book" and every verb form are single
// nodes reused by every branch that mentions them, and a genuinely variable
// slot (determiner, pronoun, construction, particle, tense, politeness,
// sentence-final particle) is an alternative list on one node.
//
// Hard constraints reject a branch and are recorded in the ledger with the
// rule that killed it. Soft constraints only rank. The two are never mixed:
// a fluent sentence with the wrong predicate must not outrank an awkward one
// with the right one, and a frame that merely lost a scoring race is not a
// violation and is never filed in the rejection ledger.

import (
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/trace"
)

// Hard constraint rule names. They are stable strings: the UI shows them, the
// tests assert them, and the rejection ledger is the only place a branch that
// "could have existed" is allowed to be mentioned.
//
// Every rule here is a HARD rule, i.e. a violation of
// Semantics(T) ⊇ RequiredMeaning. Ranking facts (SoftDominance) are not in this
// ledger; they are carried as notes and as a separate "dominated" list on the
// construction-selection trace span, so "this frame was illegal" and "this
// frame lost a scoring race" can never be read as the same thing.
const (
	HardInventedGender  = "HARD.invented_gender"
	HardUnsupportedInfo = "HARD.unsupported_info"
	HardWrongEntity     = "HARD.wrong_entity"
	HardWrongPredicate  = "HARD.wrong_predicate"
	HardPolarity        = "HARD.polarity"
	HardNumber          = "HARD.number"
	HardTense           = "HARD.tense"
	HardScope           = "HARD.scope"
	HardModality        = "HARD.modality"
	HardAgreement       = "HARD.agreement"
	HardMissingArg      = "HARD.missing_argument"
	HardDeterminer      = "HARD.determiner"
	HardReferentEmpty   = "HARD.referent_unrealizable"
	HardUnknownSense    = "HARD.unknown_predicate"
	HardConstRequires   = "HARD.construction.requires"
	HardConstForbidden  = "HARD.construction.forbidden"
	// HardRoleUnrealizable rejects a frame that has no slot for a role the
	// event fills. Offering such a frame is what let 「太郎が花子に本を渡した」
	// be realized as 「太郎は本を受け取った」: the recipient had nowhere to go,
	// so it was dropped and the proposition reversed (plan.md §36).
	HardRoleUnrealizable = "HARD.construction.role_unrealizable"

	// HardRulePrefix is the namespace every violation rule shares. The
	// rejection ledger is sorted and displayed by prefix, so a rule outside this
	// namespace does not belong in it.
	HardRulePrefix = "HARD."
)

// LossVector is the planner's half of the translation loss vector of plan.md
// §43; the verifier owns the rest and has its own, authoritative type
// (verify.LossVector). This one aggregates the projection's LossHints so the
// pipeline can report how much the target projection gave up before the
// verifier ever runs.
type LossVector struct {
	Propositional float64 `json:"propositional"`
	Referential   float64 `json:"referential"`
	Temporal      float64 `json:"temporal"`
	Pragmatic     float64 `json:"pragmatic"`
	Stylistic     float64 `json:"stylistic"`
	Implicature   float64 `json:"implicature"`
}

// piece is one materialized element of a clause: a slot with its surviving
// alternatives and the label the packed forest shows for it.
//
// The packed forest concatenates an alternative's own lexeme before its single
// child, and treats several children as alternatives. A pattern is therefore
// materialized from the right: the last piece is the innermost child and every
// earlier piece wraps it, each of its alternatives carrying the same child
// node. Alternatives at a slot multiply only that slot's lexeme, so the rest of
// the sentence stays shared and the packing survives.
type piece struct {
	label string
	alts  []forest.Alt
	// clause is set when the piece realizes an embedded event instead of a
	// lexeme. The chain builder hands the clause the material that follows
	// it, because a subtree cannot be concatenated with material after it:
	// the clause is built with its own continuation inside.
	clause *NPPlan
	// noSubject suppresses the subject of an embedded clause, which is how a
	// control complement (want to go) drops it.
	noSubject bool
	// depth is the embedding depth of the clause.
	depth int
	// sealed marks a piece that already carries its own complete subtree, so
	// the chain builder must not attach the following material to it. A quoted
	// Japanese clause is one: the reporting verb belongs inside it, because the
	// packed forest concatenates a lexeme BEFORE its child and 「〜と思う」 puts
	// the verb after the clause.
	sealed bool
}

// empty reports whether the piece carries no material at all.
func (p piece) empty() bool { return len(p.alts) == 0 && p.clause == nil }

// realizer carries everything the two morphological realizers share: the
// request, the projection, the graph, the builder and the rejection ledger.
type realizer struct {
	r   Request
	p   *Projection
	g   *jlir.Graph
	gp  genderPolicy
	b   *forest.Builder
	out *forest.Realization
	l   lang.Lang
	// notes accumulates realization level diagnostics for the trace.
	notes []string
	// noSubject suppresses the subject of the clause currently being built,
	// which is how a control complement (want to go) drops its subject.
	noSubject bool
	// clauseFinal marks the clause being built as the last one of the
	// sentence, which is where the sentence-final particle goes.
	clauseFinal bool
	// bareVerb suppresses subject-verb agreement inside a control complement:
	// "wants to go", not "wants to goes".
	bareVerb bool
	// embedded marks the clause being built as a clausal argument of another
	// event, so it carries no sentence punctuation of its own.
	embedded bool
}

// Realize builds the packed realization forest for a projection.
//
// The returned forest holds every surviving target string as a derivation;
// Candidates(limit) expands it into ranked strings. Both languages go through
// this same structure, which is what makes the packed-forest and constraint
// machinery genuinely shared rather than parallel implementations.
func Realize(r Request, p *Projection) *forest.Realization {
	rec := r.recorder()
	span := rec.Open(trace.StageRealize, "packed realization forest")
	defer span.Close()

	out := &forest.Realization{Slots: map[string]int{}}
	if p == nil {
		out.Notes = append(out.Notes, "no projection supplied: nothing to realize")
		span.Note("no projection supplied")
		span.Status(trace.StatusWarn)
		return out
	}
	target := p.Target
	if !target.Valid() {
		target = r.Target
	}
	rt := &realizer{
		r: r, p: p, g: r.JLIR, gp: newGenderPolicy(r.JLIR),
		b: forest.NewBuilder("t"), out: out, l: target,
	}

	// Replay the rejections the projection already made: an unknown predicate or
	// a construction whose requirements the event fails must stay visible even
	// though the surviving branch looks perfectly normal.
	for i := range p.Events {
		for _, rj := range p.Events[i].Rejected() {
			rt.reject(rj.Slot, rj.Lex, rj.Rule, rj.Reason, orDefault(rj.Stage, "construction"))
		}
	}

	// The sentence is assembled from the right: the punctuation is innermost,
	// then each clause is wrapped around the material that follows it, so a
	// clause is a single interned subtree whose alternatives are its
	// constructions and whose inside is shared with every other candidate.
	punct := rt.punctuation(p)
	punctNode := rt.terminal("PUNCT", punct)
	node := punctNode
	events := p.Events
	embedded := embeddedEvents(r.JLIR)
	// Only matrix clauses get the sentence's own punctuation: an embedded event
	// is realized inside its controller, once, with nothing after it.
	var matrix []int
	for i := range events {
		if embedded[events[i].EventID] {
			rt.note("event %s is an embedded clause, realized inside its matrix event", events[i].EventID)
			continue
		}
		matrix = append(matrix, i)
	}
	for pos := len(matrix) - 1; pos >= 0; pos-- {
		i := matrix[pos]
		ep := &events[i]
		label := "C" + string(ep.EventID)
		rt.clauseFinal = pos == len(matrix)-1
		frames := rt.frames(ep, i == 0, 0)
		// The sentence's punctuation is decided here and nowhere else, and it is
		// decided from what actually realized rather than from the clause's
		// position. The assembly runs right to left, so `node` still being the
		// bare punctuation node means nothing to the right materialized: this
		// clause is therefore the sentence's last real one and must not close
		// itself. Deciding this by position, as the per-language frame builders
		// used to, made a final clause that yielded no frame leave the previous
		// clause's 。 in place of the sentence's own: 「あります。。」.
		if node != punctNode && !rt.embedded {
			for k := range frames {
				frames[k].parts = append(frames[k].parts, piece{label: "PUNCT",
					alts: []forest.Alt{{Lex: punct, Probability: 0.9, Hard: true}}})
			}
		}
		var alts []forest.Alt
		for _, f := range frames {
			sub := rt.chain(f.parts, node)
			if sub == "" {
				continue
			}
			prob := f.c.Naturalness
			if f.c.ID != ep.Construction {
				prob *= 0.8 // the oracle chose another one: kept, ranked lower
			}
			alts = append(alts, forest.Alt{Children: []string{sub}, Probability: prob,
				Rule: "<" + f.c.ID + ">", Hard: true})
		}
		if len(alts) == 0 {
			rt.note("event %s produced no realizable clause", ep.EventID)
			continue
		}
		rt.countSlot("construction:"+string(ep.EventID), len(alts))
		node = rt.altNode(label, alts)
	}
	if node == "" || node == punctNode {
		out.Notes = append(out.Notes, "the projection contains no realizable clause")
		span.Note("no realizable clause: the projection is empty or every branch was rejected")
		span.Status(trace.StatusWarn)
		return out
	}
	root := rt.b.Add("S", []forest.Alt{{Children: []string{node}, Probability: 1, Hard: true}})
	f := rt.b.Root(root)
	out.Forest = f
	out.Notes = append(out.Notes, rt.notes...)

	span.Count("nodes", len(f.Nodes))
	span.Count("shared", f.Shared)
	span.Count("rejected", len(out.Rejected))
	for _, slot := range sortedKeys(out.Slots) {
		span.Label(slot, sprinti(out.Slots[slot]))
	}
	span.Data(out)
	if len(out.Rejected) > 0 {
		span.Note("%d branches were rejected by hard constraints", len(out.Rejected))
	}
	return out
}

// punctuation returns the sentence final punctuation of the target.
func (rt *realizer) punctuation(p *Projection) string {
	q := false
	if rt.g != nil {
		for _, ev := range rt.g.Events {
			if ev == nil {
				continue
			}
			if strings.EqualFold(jlir.ValueString(eventFeatureValue(ev, "question")), "1") ||
				strings.EqualFold(rt.g.Prag.SentenceFinalAttitude, "question") {
				q = true
			}
		}
	}
	if rt.l == lang.JA {
		if q {
			return "？"
		}
		return "。"
	}
	if q {
		return "?"
	}
	return "."
}

// eventFeatureValue is a defensive accessor: the graph may be hand built.
func eventFeatureValue(ev *jlir.Event, key string) any {
	if ev == nil {
		return nil
	}
	if f, ok := ev.Feature(key); ok {
		return f.Value
	}
	return nil
}

// frames realizes one event plan into one materializable pattern per surviving
// construction. Both languages go through it, which is what makes the packing
// and constraint machinery shared rather than parallel.
func (rt *realizer) frames(ep *EventPlan, first bool, depth int) []frame {
	if ep == nil || depth > maxEmbedDepth {
		return nil
	}
	switch rt.l {
	case lang.EN:
		return rt.framesEN(ep, first, depth)
	case lang.JA:
		return rt.framesJP(ep, first, depth)
	}
	return nil
}

// clauseTail builds an embedded clause, with tail as the material that follows
// it inside the parent frame. An embedded clause is an alternative-bearing node
// of its own, so it has to be built with its continuation already inside.
func (rt *realizer) clauseTail(ep *EventPlan, tail string, noSubject bool, depth int) string {
	if ep == nil {
		return ""
	}
	saved, savedBare, savedFinal, savedEmbedded := rt.noSubject, rt.bareVerb, rt.clauseFinal, rt.embedded
	rt.noSubject, rt.bareVerb = noSubject, noSubject
	// An embedded clause is not a sentence: it never owns the sentence-final
	// particle and never closes itself with a full stop. Without this the
	// sub-clause emitted its own 。 and the outer sentence emitted another.
	rt.clauseFinal, rt.embedded = true, true
	frames := rt.frames(ep, false, depth)
	var alts []forest.Alt
	for _, f := range frames {
		parts := append([]piece(nil), f.parts...)
		sub := rt.chain(parts, tail)
		if sub == "" {
			continue
		}
		alts = append(alts, forest.Alt{Children: []string{sub}, Probability: f.c.Naturalness,
			Rule: "<" + f.c.ID + ">", Hard: true})
	}
	rt.noSubject, rt.bareVerb = saved, savedBare
	rt.clauseFinal, rt.embedded = savedFinal, savedEmbedded
	if len(alts) == 0 {
		return ""
	}
	return rt.altNode("CL", alts)
}

// clauseNode is an embedded clause with nothing following it.
func (rt *realizer) clauseNode(ep *EventPlan, first bool, depth int) string {
	return rt.clauseTail(ep, "", rt.noSubject, depth)
}

// embeddedEvents returns the events that are bound as clausal arguments of
// another event. They are realized inside their controller, so realizing them
// again as matrix clauses would say the same thing twice.
func embeddedEvents(g *jlir.Graph) map[jlir.ID]bool {
	out := map[jlir.ID]bool{}
	if g == nil {
		return out
	}
	for _, ev := range g.Events {
		if ev == nil {
			continue
		}
		for _, arg := range ev.Args {
			if g.Event(arg.Value) != nil {
				out[arg.Value] = true
			}
		}
	}
	return out
}

// constructionsOf returns the live constructions of an event, always
// non-empty so the realizer never has to invent a frame.
func (rt *realizer) constructionsOf(ep *EventPlan) []*Construction {
	lib := Default()
	ids := ep.Constructions
	if len(ids) == 0 && ep.Construction != "" {
		ids = []string{ep.Construction}
	}
	var out []*Construction
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		c, ok := lib.Get(id)
		if !ok {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		out = append(out, fallbackConstruction(rt.l, Context{Sense: ep.SenseID, Family: ep.SenseFamily, Target: rt.l}))
	}
	return out
}

// checkConstruction applies the event level hard constraints to a construction.
func (rt *realizer) checkConstruction(ep *EventPlan, c *Construction) (ok bool, rule, reason string) {
	// The construction must declare the sense. plan.md §36 requires
	// Semantics(T) ⊇ RequiredMeaning, and TRANSFER.01 (a hand-over) and
	// TRANSFER.09 (a taking) are opposite propositions that happen to share an
	// ontology family: admitting either frame for the other sense produces a
	// fluent sentence with the wrong predicate.
	if c.Family != "UNKNOWN" && ep.SenseID != "" && !c.realizesSense(ep.SenseID) {
		return false, HardWrongPredicate, "construction " + c.ID + " does not realize sense " + ep.SenseID
	}
	// The frame must have a slot for every role the event fills. A role with
	// nowhere to go is silently dropped, and dropping the recipient of a
	// hand-over reverses the proposition.
	if role, missing := unfillableRole(ep, c); missing {
		return false, HardRoleUnrealizable, "construction " + c.ID +
			" has no slot for the " + role + " of " + string(ep.EventID)
	}
	// A copular predication cannot carry negation or modality: rejecting here
	// is cheaper than producing "Taro is not is called".
	if strings.EqualFold(ep.Polarity, jlir.PolarityNegative) && isCopular(c) {
		return false, HardPolarity, "construction " + c.ID + " is copular and cannot realize the negation of " + string(ep.EventID)
	}
	if ep.Modality != "" && ep.Modality != jlir.ModalityNone && isCopular(c) {
		return false, HardModality, "construction " + c.ID + " is copular and cannot carry the modality " + ep.Modality
	}
	if ep.Tense == jlir.TensePast && c.Requires["tense"] == jlir.TensePresent {
		return false, HardTense, "construction " + c.ID + " requires a present tense but the event is past"
	}
	return true, "", ""
}

// unfillableRole returns the first role the event fills that the construction's
// pattern has no slot for. A role with nowhere to go is silently dropped, and
// dropping the recipient of a hand-over reverses the proposition: that is how
// 「太郎が花子に本を渡した」 was offered as 「太郎は本を受け取った」 (plan.md §36).
//
// Only roles the construction is expected to carry count. A frame may leave an
// adjunct unexpressed, and it may fold a clausal argument into its predicate,
// but it may not drop a participant it names nowhere.
func unfillableRole(ep *EventPlan, c *Construction) (role string, missing bool) {
	if ep == nil || c == nil {
		return "", false
	}
	for _, r := range sortedRoles(ep.Args) {
		np := ep.Args[r]
		if np == nil || np.Omitted() {
			continue
		}
		// A clausal argument is realized inside the predicate by the quoting or
		// chaining machinery, not as a slot of its own.
		if np.IsClause {
			continue
		}
		if !frameBinds(c, ep, r) {
			return r, true
		}
	}
	return "", false
}

// frameBinds reports whether the construction's pattern has a place for the
// role. The two generic slots are interpreted the way the realizer interprets
// them: SUBJ takes the construction's subject role and, when that is unfilled,
// the first of the fallback roles resolveSubject walks; OBJ takes whichever
// direct-object role the event fills.
func frameBinds(c *Construction, ep *EventPlan, role string) bool {
	hasSubject, hasObject := false, false
	for _, t := range tokens(c.Pattern) {
		switch {
		case t == "SUBJ":
			hasSubject = true
		case t == "OBJ":
			hasObject = true
		case isSlotToken(t):
			if roleOfToken(t) == role {
				return true
			}
		}
	}
	// OBJ is checked before SUBJ: the object slot is the one that binds the
	// direct-object roles, and a frame that names both (SUBJ V OBJ to
	// RECIPIENT) uses SUBJ for a different participant.
	if hasObject {
		switch role {
		case jlir.RolePatient, jlir.RoleTheme, jlir.RoleStimulus, jlir.RoleProduct:
			return true
		}
	}
	if hasSubject {
		if role == subjectRoleOf(c) {
			return true
		}
		if np := ep.Args[subjectRoleOf(c)]; np != nil && !np.Omitted() {
			return false
		}
		for _, alt := range alternativeSubjects() {
			if np := ep.Args[alt]; np != nil && !np.Omitted() {
				return role == alt
			}
		}
	}
	return false
}

// sortedRoles returns the filled roles of a plan in a deterministic order, so
// the role the ledger names does not depend on map iteration.
func sortedRoles(args map[string]*NPPlan) []string {
	out := make([]string, 0, len(args))
	for r, np := range args {
		if np == nil || np.Omitted() {
			continue
		}
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// isCopular reports whether a construction is a bare copula.
func isCopular(c *Construction) bool {
	switch c.Family {
	case "COPULA", "NAMED", "EXIST":
		return true
	}
	return false
}

// terminal interns a leaf node. Every leaf the realizer interns is a surface
// form that must appear verbatim in some candidate, so it is always hard: a
// branch carrying it is a real branch, never a dominated one.
func (rt *realizer) terminal(label, lex string) string {
	return rt.b.Add(label, []forest.Alt{{Lex: lex, Probability: 1, Hard: true}})
}

// altNode interns a node with explicit alternatives.
func (rt *realizer) altNode(label string, alts []forest.Alt) string {
	if len(alts) == 0 {
		return ""
	}
	return rt.b.Add(label, alts)
}

// chain materializes a sequence of pieces into a single packed subtree, with
// tail as the material that follows the last piece (the sentence punctuation,
// or the remainder of the sentence). Alternatives at a piece multiply that
// piece's lexeme only: every alternative points at the same remainder, so
// packing survives.
func (rt *realizer) chain(parts []piece, tail string) string {
	node := tail
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		if p.clause != nil {
			if p.clause.Clause == nil {
				continue
			}
			sub := rt.clauseTail(p.clause.Clause, tail, p.noSubject, p.depth)
			if sub == "" {
				continue
			}
			node = rt.b.Add(p.label, []forest.Alt{{Children: []string{sub}, Probability: 0.95, Hard: true}})
			continue
		}
		if len(p.alts) == 0 {
			continue
		}
		if p.sealed {
			node = rt.b.Add(p.label, p.alts)
			continue
		}
		attached := make([]forest.Alt, 0, len(p.alts))
		for _, a := range p.alts {
			if node != "" {
				a.Children = []string{node}
			}
			attached = append(attached, a)
		}
		node = rt.b.Add(p.label, attached)
	}
	return node
}

// frame is one construction's materialized pattern, kept beside its
// construction so the alternatives of a clause can be ranked per construction.
type frame struct {
	c     *Construction
	parts []piece
}

// idPiece is the innermost piece of a clause: a lexeme-free node whose label is
// the construction id, so the packed path of every candidate ends with it and a
// string can be attributed to the construction that produced it.
func (rt *realizer) idPiece(c *Construction) piece {
	return piece{label: c.ID, alts: []forest.Alt{{
		Probability: 1, Hard: true, Rule: "<" + c.ID + ">",
	}}}
}

// reject records a branch killed by a hard constraint. Only HARD rules reach
// this ledger: a ranking fact is not a violation and must not be presented as
// one (plan.md §37).
func (rt *realizer) reject(slot, lex, rule, reason, stage string) {
	if rule == "" {
		return
	}
	// Only a violation may enter the ledger. A frame that merely lost a
	// scoring race is a ranking fact, and plan.md §37 forbids presenting the two
	// as one thing: the UI sorts this ledger by the rule prefix, so a SOFT entry
	// here reads as an illegal frame.
	if !strings.HasPrefix(rule, HardRulePrefix) {
		rt.note("not a violation, so not a rejection: %q %s (%s)", lex, reason, rule)
		return
	}
	rt.out.AddRejected(forest.RejectedBranch{
		Slot: slot, Lex: lex, Rule: rule, Reason: reason, Stage: stage,
	})
	rt.note("rejected %q in %s: %s (%s)", lex, slot, reason, rule)
}

// note appends a diagnostic line.
func (rt *realizer) note(format string, args ...any) {
	rt.notes = append(rt.notes, sprintf(format, args...))
}

// countSlot records the funnel of one slot.
func (rt *realizer) countSlot(slot string, n int) {
	if n <= 0 {
		return
	}
	if rt.out.Slots == nil {
		rt.out.Slots = map[string]int{}
	}
	rt.out.Slots[slot] += n
}

// pronounAllowed is the gender check the realizer calls while it builds, so a
// gendered pronoun that has no upstream provenance is never even generated
// (plan.md §39). The rejection is recorded so the ledger explains the absence.
func (rt *realizer) pronounAllowed(np *NPPlan, pron, slot string) bool {
	if np == nil || pron == "" {
		return false
	}
	if !isPronounGendered(pron) {
		return true
	}
	ok, rule, reason := rt.gp.check(rt.entity(np), pron)
	if !ok {
		rt.reject(slot, pron, rule, reason, "projection")
		return false
	}
	return true
}

// entity resolves the graph entity behind a noun phrase plan.
func (rt *realizer) entity(np *NPPlan) *jlir.Entity {
	if rt.g == nil || np == nil || np.EntityID == "" {
		return nil
	}
	return rt.g.Entity(np.EntityID)
}

// tokens splits a pattern into its slots.
func tokens(pattern string) []string { return strings.Fields(pattern) }

// slotRoles maps the upper case slot tokens of a pattern onto the canonical
// jlir role names.
var slotRoles = map[string]string{
	"AGENT":       jlir.RoleAgent,
	"EXPERIENCER": jlir.RoleExperiencer,
	"PATIENT":     jlir.RolePatient,
	"THEME":       jlir.RoleTheme,
	"STIMULUS":    jlir.RoleStimulus,
	"RECIPIENT":   jlir.RoleRecipient,
	"GOAL":        jlir.RoleGoal,
	"SOURCE":      jlir.RoleSource,
	"LOCATION":    jlir.RoleLocation,
	"INSTRUMENT":  jlir.RoleInstrument,
	"COMITATIVE":  jlir.RoleComitative,
	"TIME":        jlir.RoleTime,
	"MANNER":      jlir.RoleManner,
	"CAUSE":       jlir.RoleCause,
	"BENEFICIARY": jlir.RoleBeneficiary,
	"PRODUCT":     jlir.RoleProduct,
	"POSSESSOR":   jlir.RolePossessor,
}

// isSlotToken reports whether a token names a slot rather than a preposition.
func isSlotToken(tok string) bool {
	switch tok {
	case "SUBJ", "V", "JOIN", "TAIL", "OBJ", "quote":
		return true
	}
	_, ok := slotRoles[tok]
	return ok
}

// roleOfToken maps a slot token onto the semantic role it binds.
func roleOfToken(tok string) string {
	switch tok {
	case "OBJ":
		return jlir.RolePatient
	case "SUBJ":
		return jlir.RoleAgent
	}
	if r, ok := slotRoles[tok]; ok {
		return r
	}
	return tok
}

// subjectRoleOf returns the role a construction realizes as its subject.
func subjectRoleOf(c *Construction) string {
	if c != nil && c.SubjRole != "" {
		return c.SubjRole
	}
	return jlir.RoleAgent
}

// alternativeSubjects lists the roles that can serve as the subject, in
// preference order, when the primary one is unbound.
func alternativeSubjects() []string {
	return []string{jlir.RoleAgent, jlir.RoleExperiencer, jlir.RolePatient, jlir.RoleTheme}
}

// resolveSubject picks the noun phrase that realizes the subject slot.
func resolveSubject(ep *EventPlan, c *Construction) *NPPlan {
	role := subjectRoleOf(c)
	if np := ep.Args[role]; np != nil {
		return np
	}
	for _, alt := range alternativeSubjects() {
		if np := ep.Args[alt]; np != nil {
			return np
		}
	}
	return ep.Subject
}

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// sprinti formats an integer for a trace chip.
func sprinti(n int) string { return sprintf("%d", n) }

// sortStrings sorts in place and returns the slice, for deterministic output.
func sortStrings(in []string) []string {
	sort.Strings(in)
	return in
}
