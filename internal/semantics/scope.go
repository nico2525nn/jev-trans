package semantics

// Scope graph construction (plan.md §13).
//
// Scope is a graph, never a string, because 「皆が帰らなかった」 is two
// propositions and neither is the parse. This layer emits both as
// ScopeReadings with weights and Resolved == false, and only commits when the
// morphology itself settles the question — a negative quantifier absorbing the
// negation (誰も), or an overt dominance/restrictive marker (必ず / しか).
// Everything else goes to the decision layer, which is what plan.md §58 calls
// delayed commitment.
//
// Operators handled: NOT, ALL, SOME, NONE, PROG, HAVE, COND, BECAUSE.

import (
	"strings"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/syntax"
)

// quantOperator classifies a quantifier string into a scope operator kind.
// It returns "" when the string is not a generalized quantifier, which is the
// common case and must not produce a spurious operator.
func quantOperator(q string) string {
	switch normalizeMarker(q) {
	case "皆", "みんな", "全員", "全部", "すべて", "全て", "各位":
		return jlir.ScopeAll
	case "誰か", "だれか", "何人か", "どれか":
		return jlir.ScopeSome
	case "誰も", "だれも", "何も", "何人も", "HELP":
		return jlir.ScopeNone
	case "all", "every", "everyone", "everybody", "each", "both":
		return jlir.ScopeAll
	case "some", "someone", "somebody", "any":
		return jlir.ScopeSome
	case "no one", "nobody", "no-one", "none", "nothing", "neither":
		return jlir.ScopeNone
	case "there is", "there are", "exists":
		return jlir.ScopeSome
	}
	return ""
}

// quantifierWords is the surface set recognized when the parser did not
// populate Clause.Quantifier. Matching the head noun's surface is morphology,
// not inference, so a fallback here is legitimate and keeps the operator from
// being lost.
var quantifierWords = map[string]string{
	"皆": jlir.ScopeAll, "みんな": jlir.ScopeAll, "全員": jlir.ScopeAll,
	"全部": jlir.ScopeAll, "すべて": jlir.ScopeAll, "全て": jlir.ScopeAll,
	"誰か": jlir.ScopeSome, "だれか": jlir.ScopeSome, "何人か": jlir.ScopeSome,
	"誰も": jlir.ScopeNone, "だれも": jlir.ScopeNone, "何も": jlir.ScopeNone, "何人も": jlir.ScopeNone,
	"everyone": jlir.ScopeAll, "everybody": jlir.ScopeAll, "all": jlir.ScopeAll, "each": jlir.ScopeAll,
	"someone": jlir.ScopeSome, "somebody": jlir.ScopeSome, "some": jlir.ScopeSome,
	"nobody": jlir.ScopeNone, "no one": jlir.ScopeNone, "none": jlir.ScopeNone, "nothing": jlir.ScopeNone,
}

// normalizeMarker lowercases and trims an English marker while leaving a
// Japanese string untouched.
func normalizeMarker(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)
	for _, r := range low {
		if r > 0x7f {
			return s
		}
	}
	return low
}

// dominanceMarker reports a quantifier that is overtly dominant over negation.
// 「必ず帰らなかった」 has ALL > NOT because 必ず sits outside ない; 「帰らなかっ
// ただけ」 has the opposite order for the same reason. An overt marker is
// morphology rather than a prior, so committing on it is legitimate.
func dominanceMarker(q string) bool {
	switch normalizeMarker(q) {
	case "必ず", "かならず", "すべて", "全部", "全て", "always", "definitely", "certainly":
		return true
	}
	return false
}

// restrictiveMarker is the mirror image: a marker that keeps negation wide.
func restrictiveMarker(q string) bool {
	switch normalizeMarker(q) {
	case "しか", "だけ", "のみ":
		return true
	}
	return false
}

// scopeFor builds the scope operators one clause introduces.
func (a *analyzer) scopeFor(c *syntax.Clause, ev *jlir.Event) {
	quant, quantOp := a.quantifierOf(c)
	negated := a.negated(c, ev)

	span := c.Span
	negID := ""
	if negated {
		negID = a.newScope(jlir.ScopeNot, []string{string(ev.ID)}, jlir.PredSpan(jlir.OriginScope,
			a.negationSpan(c), a.b.Source, 0.95, "negation over %s", ev.ID))
		// Whether the negation is inside or outside a quantifier is decided below.
		// With nothing to decide against, the node is already linear.
		a.commitOne(negID, jlir.PredSpan(jlir.OriginScope, a.negationSpan(c), a.b.Source, 0.9,
			"negation operator over %s", ev.ID))
	}

	// Aspect operators. ている is progressive; a perfect is a HAVE scope. Both
	// are operators, not tenses, which is why they live here rather than in
	// morphology.
	if c.Aspect != "" {
		switch normAspect(c.Aspect) {
		case jlir.AspectProgressive:
			a.newScope(jlir.ScopeProg, []string{string(ev.ID)}, jlir.PredSpan(jlir.OriginScope,
				span, a.b.Source, 0.9, "progressive aspect over %s", ev.ID))
		case jlir.AspectPerfect:
			a.newScope(jlir.ScopeHave, []string{string(ev.ID)}, jlir.PredSpan(jlir.OriginScope,
				span, a.b.Source, 0.9, "perfect aspect over %s", ev.ID))
		}
	}

	// Conditionals and causal connectives are scopes too: 「〜たら」 introduces a
	// COND node, 「〜から」/「because」 a BECAUSE node.
	if c.Conjoin == syntax.ConjoinIf || hasMarker(c, "conditional") {
		a.newScope(jlir.ScopeCond, []string{string(ev.ID)}, jlir.PredSpan(jlir.OriginScope,
			span, a.b.Source, 0.85, "conditional scope over %s", ev.ID))
	}
	if c.Conjoin == syntax.ConjoinBecause || hasMarker(c, "causal") {
		a.newScope(jlir.ScopeBecause, []string{string(ev.ID)}, jlir.PredSpan(jlir.OriginScope,
			span, a.b.Source, 0.85, "causal scope over %s", ev.ID))
	}

	if quantOp == "" {
		// No generalized quantifier: the negation and aspect operators stand on
		// their own and nothing is ambiguous about their ordering.
		return
	}

	// A negative quantifier absorbs the negation lexically: 誰も is already
	// NOT > ALL, so there is nothing left to decide.
	if quantOp == jlir.ScopeNone {
		quantID := a.newScope(jlir.ScopeNone, []string{string(ev.ID)}, jlir.PredSpan(jlir.OriginScope,
			span, a.b.Source, 0.95, "negative quantifier %q is lexically NOT > ALL", quant))
		if negID != "" {
			a.commitSingle(quantID, negID, jlir.ScopeNot+" > "+jlir.ScopeNone,
				jlir.PredSpan(jlir.OriginScope, span, a.b.Source, 0.95,
					"a negative quantifier already scopes over the negation"))
		} else {
			a.commitOne(quantID, jlir.PredSpan(jlir.OriginScope, span, a.b.Source, 0.95,
				"only one operator is present"))
		}
		a.recordQuantifier(c, quant, jlir.ScopeNone, ev)
		return
	}

	quantProv := jlir.PredSpan(jlir.OriginScope, span, a.b.Source, 0.9, "quantifier %q", quant)
	quantID := a.newScope(quantOp, []string{string(ev.ID)}, quantProv)
	a.recordQuantifier(c, quant, quantOp, ev)

	if negID == "" {
		a.commitOne(quantID, jlir.PredSpan(jlir.OriginScope, span, a.b.Source, 0.95,
			"only one operator is present, so the scope is not ambiguous"))
		return
	}

	// Both operators present: the ordering is open unless the morphology says
	// otherwise. This is the case plan.md §13 is written about.
	switch {
	case dominanceMarker(quant):
		a.commitSingle(quantID, negID, quantOp+" > "+jlir.ScopeNot,
			jlir.PredSpan(jlir.OriginScope, span, a.b.Source, 0.95,
				"overt dominance marker %q settles the scope without an oracle call", quant))
	case restrictiveMarker(quant):
		a.commitSingle(negID, quantID, jlir.ScopeNot+" > "+quantOp,
			jlir.PredSpan(jlir.OriginScope, span, a.b.Source, 0.95,
				"restrictive marker %q keeps the negation wide", quant))
	default:
		a.ambiguousOrder(quantID, negID, quantOp, c, quant)
	}
}

// quantifierOf reads the clause's quantifier. It prefers the parser's own
// analysis and falls back to matching the head noun's surface, which keeps the
// operator from being lost when the parser only recorded it morphologically.
func (a *analyzer) quantifierOf(c *syntax.Clause) (string, string) {
	if q := c.Quantifier; q != "" {
		if op := quantOperator(q); op != "" {
			return q, op
		}
	}
	if q := c.QuantifiedNP; q != "" {
		if op := quantOperator(q); op != "" {
			return q, op
		}
	}
	for _, p := range []*syntax.Phrase{c.Subject, c.Topic} {
		if p == nil || p.Head == "" {
			continue
		}
		surface := a.b.Text(p.Head)
		if op, ok := quantifierWords[normalizeMarker(surface)]; ok {
			return surface, op
		}
	}
	return "", ""
}

// negated reports whether the clause carries a negative operator.
func (a *analyzer) negated(c *syntax.Clause, ev *jlir.Event) bool {
	if ev.Polarity == jlir.PolarityNegative || c.Negation != "" {
		return true
	}
	if m := a.b.Morph(c.Matrix); m != nil {
		if strings.EqualFold(m.Feat("polarity"), "negative") {
			return true
		}
	}
	if m := a.b.Morph(c.Negation); m != nil && m.Text(a.b.Source) != "" {
		return true
	}
	return false
}

// negationSpan points at the negative morpheme when the parser identified one,
// and at the clause otherwise so the provenance still locates something real.
func (a *analyzer) negationSpan(c *syntax.Clause) jlir.Span {
	if m := a.b.Morph(c.Negation); m != nil {
		return jlir.Span{Start: m.Start, End: m.End}
	}
	return c.Span
}

// ambiguousOrder emits the two readings plan.md §13 requires, leaning very
// slightly on the unmarked order. The lean is small on purpose: collapsing
// here is exactly what the plan forbids, and the oracle is what settles it.
func (a *analyzer) ambiguousOrder(quantID, negID, quantOp string, c *syntax.Clause, quant string) {
	node := a.jb.Build().Scope(jlir.ID(quantID))
	if node == nil {
		return
	}
	prov := jlir.PredSpan(jlir.OriginScope, c.Span, a.b.Source, 0.5,
		"the scope of %q over %s is not settled by the morphology", quant, negID)
	node.Readings = []jlir.ScopeReading{
		{
			Order: []string{negID, quantID}, Label: jlir.ScopeNot + " > " + quantOp,
			Weight: 0.45, Prov: []jlir.Provenance{prov},
		},
		{
			Order: []string{quantID, negID}, Label: quantOp + " > " + jlir.ScopeNot,
			Weight: 0.55, Prov: []jlir.Provenance{prov},
		},
	}
	node.Resolved = false
	node.Prov = append(node.Prov, prov)
	if a.span != nil {
		a.span.Note("scope %s left open: %s and %s are both available for %q",
			node.ID, jlir.ScopeNot+" > "+quantOp, quantOp+" > "+jlir.ScopeNot, quant)
	}
	a.note(c, "scope ambiguous: %s and %s are both available; not collapsed",
		quantOp+" > "+jlir.ScopeNot, jlir.ScopeNot+" > "+quantOp)
}

// commitSingle commits a scope node to one ordered pair, which is the shape
// ApplyDecision produces too.
func (a *analyzer) commitSingle(outer, inner, label string, prov jlir.Provenance) {
	node := a.jb.Build().Scope(jlir.ID(outer))
	if node == nil {
		return
	}
	node.Readings = []jlir.ScopeReading{{
		Order: []string{outer, inner}, Label: label, Weight: 1, Prov: []jlir.Provenance{prov},
	}}
	node.Resolved = true
	node.Prov = append(node.Prov, prov)
}

// commitOne commits a scope node that has no ordering to choose between.
func (a *analyzer) commitOne(id string, prov jlir.Provenance) {
	node := a.jb.Build().Scope(jlir.ID(id))
	if node == nil {
		return
	}
	node.Readings = []jlir.ScopeReading{{
		Order: []string{id}, Label: node.Kind, Weight: 1, Prov: []jlir.Provenance{prov},
	}}
	node.Resolved = true
	node.Prov = append(node.Prov, prov)
}

// newScope appends a scope node and returns its id.
func (a *analyzer) newScope(kind string, operands []string, prov jlir.Provenance) string {
	node := &jlir.ScopeNode{
		ID:       a.jb.NewScope(),
		Kind:     kind,
		Operands: operands,
		Prov:     []jlir.Provenance{prov},
	}
	a.jb.AddScope(node)
	if a.span != nil {
		a.span.Count("scopes", 1)
	}
	return string(node.ID)
}

// recordQuantifier notes the quantifier on the event, because the target side
// needs to know a generalized quantifier was present even when the scope
// itself resolved.
func (a *analyzer) recordQuantifier(c *syntax.Clause, quant, op string, ev *jlir.Event) {
	if quant == "" {
		return
	}
	prov := []jlir.Provenance{jlir.PredSpan(jlir.OriginScope, c.Span, a.b.Source, 0.9,
		"quantifier %q introduces %s", quant, op)}
	ev.SetFeature(jlir.Feature{Key: "quantifier_scope", Value: op, Confidence: 0.9, Prov: prov})
	if op == jlir.ScopeNone {
		ev.SetFeature(jlir.Feature{Key: "negative_quantifier", Value: quant, Confidence: 0.95, Prov: prov})
	}
}

// hasMarker reports whether any of the clause's modifiers is one of the markers
// in the given class.
func hasMarker(c *syntax.Clause, class string) bool {
	table := conditionalMarkers
	if class == "causal" {
		table = causalMarkers
	}
	for _, m := range c.Modifiers {
		if table[normalizeMarker(m)] {
			return true
		}
	}
	return false
}

// conditionalMarkers and causalMarkers are the connectives that introduce a
// COND or BECAUSE scope node. The parser may record them as clause modifiers or
// as the Conjoin label; both are checked.
var conditionalMarkers = map[string]bool{
	"たら": true, "ば": true, "なら": true, "idanara": true,
	"if": true, "unless": true, "provided": true, "conditional": true,
}

var causalMarkers = map[string]bool{
	"から": true, "ので": true, "ため": true,
	"because": true, "since": true, "as": true, "causal": true,
}
