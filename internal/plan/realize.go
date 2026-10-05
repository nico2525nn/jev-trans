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
// with the right one.

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
	// SoftDominance marks a candidate pruned by Pareto dominance, which is a
	// ranking fact rather than a violation.
	SoftDominance = "SOFT.dominated"
)

// LossVector is the translation loss vector of plan.md §43. The planner fills
// the projection half of it; the verifier owns the rest.
type LossVector struct {
	Propositional float64 `json:"propositional"`
	Referential   float64 `json:"referential"`
	Temporal      float64 `json:"temporal"`
	Pragmatic     float64 `json:"pragmatic"`
	Stylistic     float64 `json:"stylistic"`
	Implicature   float64 `json:"implicature"`
}

// Add returns the component-wise sum.
func (l LossVector) Add(o LossVector) LossVector {
	return LossVector{
		Propositional: l.Propositional + o.Propositional,
		Referential:   l.Referential + o.Referential,
		Temporal:      l.Temporal + o.Temporal,
		Pragmatic:     l.Pragmatic + o.Pragmatic,
		Stylistic:     l.Stylistic + o.Stylistic,
		Implicature:   l.Implicature + o.Implicature,
	}
}

// Dominates implements the dominance relation of plan.md §45: a is at least as
// good as b on every loss dimension and strictly better on at least one, or at
// least as natural.
func (l LossVector) Dominates(o LossVector) bool {
	better := l.Propositional <= o.Propositional && l.Referential <= o.Referential &&
		l.Temporal <= o.Temporal && l.Pragmatic <= o.Pragmatic &&
		l.Stylistic <= o.Stylistic && l.Implicature <= o.Implicature
	if !better {
		return false
	}
	equal := l.Propositional == o.Propositional && l.Referential == o.Referential &&
		l.Temporal == o.Temporal && l.Pragmatic == o.Pragmatic &&
		l.Stylistic == o.Stylistic && l.Implicature == o.Implicature
	return !equal
}

// Total is the unweighted sum, used only for ordering, never for acceptance.
func (l LossVector) Total() float64 {
	return l.Propositional + l.Referential + l.Temporal + l.Pragmatic + l.Stylistic + l.Implicature
}

// PruneDominated removes candidates that Pareto-dominance makes redundant.
// lossOf and naturalnessOf let the caller supply the objective; the forest's
// own probability is the default naturalness estimate. It is exported because
// the reranker (plan.md §44) runs the same reduction over a wider candidate
// set than the forest holds.
func PruneDominated(cands []forest.Candidate, lossOf func(forest.Candidate) LossVector, keep int) []forest.Candidate {
	n := len(cands)
	if n < 2 {
		return cands
	}
	losses := make([]LossVector, n)
	nat := make([]float64, n)
	for i, c := range cands {
		if lossOf != nil {
			losses[i] = lossOf(c)
		}
		nat[i] = c.Probability
	}
	drop := make([]bool, n)
	for i := range cands {
		for j := range cands {
			if i == j || drop[i] {
				continue
			}
			if losses[j].Dominates(losses[i]) && nat[j] >= nat[i] {
				drop[i] = true
				break
			}
			if !losses[j].Dominates(losses[i]) && nat[j] > nat[i] &&
				losses[i].Dominates(losses[j]) {
				// Equal loss, lower naturalness: dominated by probability alone.
				drop[i] = true
				break
			}
		}
	}
	out := make([]forest.Candidate, 0, n)
	for i, c := range cands {
		if drop[i] {
			continue
		}
		out = append(out, c)
	}
	if keep > 0 && len(out) > keep {
		out = out[:keep]
	}
	return out
}

// ConstructionIDs splits the packed path a candidate carries into its node
// labels. forest.Enumerate collapses a derivation into its deepest node, so the
// construction id is the last segment of Candidate.Constructions[0]; this
// helper hands the consumer the segments instead of the path.
func ConstructionIDs(c forest.Candidate) []string {
	var out []string
	for _, entry := range c.Constructions {
		for _, seg := range strings.Split(entry, "/") {
			if seg == "" || seg == "S" {
				continue
			}
			out = append(out, seg)
		}
	}
	return out
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
	// lexeme. The chain builder hands the clause the material that follows it,
	// because a subtree cannot be concatenated with material after it: the
	// clause is built with its own continuation inside.
	clause *NPPlan
	// noSubject suppresses the subject of an embedded clause, which is how a
	// control complement (want to go) drops it.
	noSubject bool
	// depth is the embedding depth of the clause.
	depth int
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
	// sentence, which is where the sentence punctuation goes.
	clauseFinal bool
	// bareVerb suppresses subject-verb agreement inside a control complement:
	// "wants to go", not "wants to goes".
	bareVerb bool
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
	punctNode := rt.terminal("PUNCT", punct, 1, "", true)
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
	saved, savedBare := rt.noSubject, rt.bareVerb
	rt.noSubject, rt.bareVerb = noSubject, noSubject
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
	fams := candidateFamilies(ep.SenseID)
	if c.Family != "UNKNOWN" && len(fams) > 0 {
		ok = false
		for _, f := range fams {
			if f == c.Family {
				ok = true
				break
			}
		}
		if !ok {
			return false, HardWrongPredicate, "construction " + c.ID + " realizes " + c.Family + ", not " + ep.SenseFamily
		}
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

// isCopular reports whether a construction is a bare copula.
func isCopular(c *Construction) bool {
	switch c.Family {
	case "COPULA", "NAMED", "EXIST":
		return true
	}
	return false
}

// terminal interns a leaf node.
func (rt *realizer) terminal(label, lex string, prob float64, rule string, hard bool) string {
	if prob <= 0 {
		prob = 0.0001
	}
	return rt.b.Add(label, []forest.Alt{{Lex: lex, Probability: prob, Rule: rule, Hard: hard}})
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

// reject records a branch killed by a hard constraint.
func (rt *realizer) reject(slot, lex, rule, reason, stage string) {
	if rule == "" {
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

// prepositionFor returns the preposition of a role for a construction.
func (rt *realizer) prepositionFor(c *Construction, role string) string {
	if c != nil {
		if p, ok := c.Prep[role]; ok {
			return p
		}
	}
	return enPrep(role)
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

// pruneSlot applies Pareto dominance inside one slot: an alternative that is
// dominated on (loss, probability) by a sibling is dropped before it ever
// multiplies the search space (plan.md §45).
func pruneSlot(label string, alts []forest.Alt, loss func(forest.Alt) LossVector) []forest.Alt {
	if len(alts) < 2 || loss == nil {
		return alts
	}
	losses := make([]LossVector, len(alts))
	for i, a := range alts {
		losses[i] = loss(a)
	}
	drop := make([]bool, len(alts))
	for i := range alts {
		for j := range alts {
			if i == j {
				continue
			}
			if losses[j].Dominates(losses[i]) && alts[j].Probability >= alts[i].Probability {
				drop[i] = true
				break
			}
		}
	}
	out := make([]forest.Alt, 0, len(alts))
	for i, a := range alts {
		if drop[i] {
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		out = alts[:1]
	}
	return out
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
