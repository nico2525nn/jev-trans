package pipeline

import (
	"context"
	"fmt"
	"sort"

	"github.com/nico2525nn/jev-trans/internal/discourse"
	"github.com/nico2525nn/jev-trans/internal/jev"
	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/semantics"
	"github.com/nico2525nn/jev-trans/internal/trace"
)

// This file is the single decision layer of the pipeline: the ten nodes of
// plan.md §30, run in dependency order, each asking the oracle one bounded
// question and then committing its answer to the JLIR.
//
// Three properties are load-bearing and are asserted by the tests:
//
//  1. Every answer is applied. A decision recorded in the audit trail but never
//     written back into the graph is a decoration: the projection, the realizer
//     and the verifier all read the graph, so an unapplied answer changes
//     nothing while looking like a resolved decision.
//
//  2. A decision is only asked when it can change the translation (plan.md §34).
//
//  3. The state sent to the oracle is built here, not dumped. The oracle
//     suffers context rot, so each question gets only the material it needs.

// decisionBudget bounds how many questions one translation may send. When it is
// exhausted the remaining decisions degrade to priors rather than hanging the
// request or running the cost away.
type decisionBudget struct {
	limit int
	used  int
	seq   int
}

func (b *decisionBudget) take() bool {
	if b.used >= b.limit {
		return false
	}
	b.used++
	return true
}

// decisionContext is the one place a run's identity is assembled, so the cache
// key (plan.md §56) can see all four of its components: semantic context,
// candidate set, document style and decision type.
type decisionContext struct {
	graph  *jlir.Graph
	store  *discourse.Store
	style  string
	source lang.Lang
	target lang.Lang
}

// runDecisions executes plan.md §30's DAG.
func (e *Engine) runDecisions(ctx context.Context, rec *trace.Recorder, req Request,
	store *discourse.Store, g *jlir.Graph, mode string,
	warnings *[]string) []*jev.Decision {

	budget := &decisionBudget{limit: e.cfg.MaxOracleCalls}
	var out []*jev.Decision

	span := rec.Open(trace.StageDecisions, "jev decision graph")
	span.Label("budget", fmt.Sprint(budget.limit))
	cli := e.oracle()
	if cli == nil || !cli.Enabled() {
		span.Status(trace.StatusWarn)
		span.Note("no OPENCODE_API_KEY: decisions fall back to deterministic analysis " +
			"priors; every decision is marked source=prior in the audit trail")
		*warnings = append(*warnings, "decision oracle offline: running on analysis priors")
		span.Label("oracle", "offline")
	} else {
		span.Label("oracle", "live")
	}

	d := &decider{
		engine: e,
		span:   span,
		budget: budget,
		ctx:    ctx,
		out:    &out,
		dc: decisionContext{
			graph: g, store: store, style: styleKey(req),
			source: req.SourceLang, target: req.TargetLang,
		},
	}

	d.lexical()
	d.syntactic()
	d.predicates()
	d.roles()
	d.coreference()
	d.discourse()
	d.pragmatics()
	d.projection()
	d.construction()

	span.Count("decisions", len(out))
	span.Data(decisionViews(out))
	if budget.used >= budget.limit {
		span.Status(trace.StatusWarn)
		span.Note("oracle budget exhausted after %d questions; remaining decisions use priors",
			budget.used)
	}
	span.Label("mode", mode)
	span.Close()
	return out
}

// oracle returns the configured client, or nil. It never constructs one.
//
// The previous version allocated a fresh client per decision whenever none was
// configured, which did two wrong things at once: it threw away the decision
// cache on every call, and it read OPENCODE_API_KEY from the environment, so a
// run whose trace said "the oracle is offline" was in fact issuing live network
// requests.
func (e *Engine) oracle() *jev.Client {
	if e.cfg.Jev != nil {
		return e.cfg.Jev
	}
	return e.cfg.OfflineClient
}

func styleKey(req Request) string {
	return fmt.Sprintf("%s/%.2f/%.2f", req.Style.Register,
		req.Style.Politeness, req.Style.PronounExplicitness)
}

// decider carries the per-run state of the decision layer.
type decider struct {
	engine *Engine
	span   *trace.Span
	budget *decisionBudget
	ctx    context.Context
	out    *[]*jev.Decision
	dc     decisionContext
}

// ask performs one decision with budget enforcement and records the outcome.
// A skipped decision is still recorded: an audit trail with holes in it is worse
// than one that says "I did not ask, and here is why".
func (d *decider) ask(rq jev.Request) *jev.Decision {
	if len(rq.Criteria) == 0 {
		return nil
	}
	if rq.Prior == nil {
		rq.Prior = jlir.Uniform(optionKeys(rq.Criteria)...)
	}
	d.budget.seq++
	if rq.ID == "" {
		rq.ID = fmt.Sprintf("JEV-%d", d.budget.seq)
	}
	if rq.Style == "" {
		rq.Style = d.dc.style
	}
	if rq.SemanticContext == "" {
		rq.SemanticContext = d.dc.graph.Source
	}

	if !d.budget.take() {
		skipped := &jev.Decision{
			ID: rq.ID, Stage: rq.Stage, Kind: rq.Kind, Question: rq.Instructions,
			Source: jev.SourceSkipped, SkipReason: "oracle budget exhausted",
		}
		d.span.Note("skipped %s: oracle budget exhausted", rq.Stage)
		d.record(skipped)
		return skipped
	}
	cli := d.engine.oracle()
	if cli == nil {
		prior := &jev.Decision{
			ID: rq.ID, Stage: rq.Stage, Kind: rq.Kind, Question: rq.Instructions,
			Source:     jev.SourcePrior,
			SkipReason: "no oracle configured; deterministic analysis prior used",
		}
		d.span.Note("%s: no oracle configured; using the analysis prior", rq.Stage)
		d.record(prior)
		return prior
	}
	dec, err := cli.Ask(d.ctx, rq)
	if err != nil {
		d.span.Note("decision %s failed (%v); falling back to prior", rq.Stage, err)
		dec = &jev.Decision{
			ID: rq.ID, Stage: rq.Stage, Kind: rq.Kind, Question: rq.Instructions,
			Source:     jev.SourcePrior,
			SkipReason: fmt.Sprintf("oracle error: %v", err),
		}
	}
	d.record(dec)
	return dec
}

func (d *decider) record(dec *jev.Decision) {
	if dec == nil {
		return
	}
	*d.out = append(*d.out, dec)
}

// --- D0 lexical segmentation ---------------------------------------------

// D0 records the segmentation the analyzer chose. The analyzer already decided
// it, so the oracle is not asked to invent a segmentation; the node exists so
// the audit trail starts where plan.md §30 says it does, and so a change in the
// lattice is visible as a decision rather than an invisible fact.
func (d *decider) lexical() {
	g := d.dc.graph
	d.ask(jev.Request{
		Stage:        jev.StageLexical,
		Kind:         jev.KindChoice,
		Action:       jev.ActionSegmentation,
		TargetID:     "D0/segmentation",
		Instructions: "Which segmentation of the input does this analysis use?",
		Criteria: []jev.Criterion{
			{Key: "longest_match", Description: "the standard longest-match segmentation of the dictionary lattice", Prior: 0.9},
			{Key: "rule_deinflected", Description: "a segmentation whose inflected forms were recovered by the conjugation rules", Prior: 0.08},
			{Key: "reconstructed", Description: "a segmentation built by unknown-word rules", Prior: 0.02},
		},
		Prior: jlir.NewDistribution("analyzer", map[string]float64{
			"longest_match": 0.9, "rule_deinflected": 0.08, "reconstructed": 0.02,
		}),
		Impact: 0.2,
		State: map[string]any{
			"language": string(d.dc.source),
			"segments": entitySurfaces(g),
			"unknowns": unknownSurfaces(g),
		},
	})
}

// --- D1 syntactic ambiguity, including scope ------------------------------

// D1 resolves scope orderings. This is the node plan.md §13 and §31 both hinge
// on: "NOT > ALL" and "ALL > NOT" are different propositions, and a translation
// system that silently picks one changes the meaning.
func (d *decider) syntactic() {
	for _, sc := range d.dc.graph.Scopes {
		if sc.Resolved || len(sc.Readings) < 2 {
			continue
		}
		opts := make([]string, 0, len(sc.Readings))
		prior := map[string]float64{}
		labels := map[string]string{}
		for _, r := range sc.Readings {
			key := ""
			if len(r.Order) > 0 {
				key = r.Order[0]
			}
			if key == "" {
				continue
			}
			opts = append(opts, key)
			prior[key] += r.Weight
			labels[key] = r.Label
		}
		if len(opts) < 2 {
			continue
		}
		dec := d.ask(jev.Request{
			Stage:           jev.StageSyntactic,
			Kind:            jev.KindChoice,
			Action:          jev.ActionScope,
			TargetID:        string(sc.ID),
			Instructions:    "Which scope reading does this sentence take?",
			Criteria:        scopeCriteria(opts, labels),
			Prior:           jlir.NewDistribution("morphology", prior),
			Impact:          0.9,
			SemanticContext: d.dc.graph.Source + "|" + sc.Kind,
			State: map[string]any{
				"sentence": d.dc.graph.Source,
				"operator": sc.Kind,
				"readings": scopeLabels(sc),
			},
		})
		if dec == nil {
			continue
		}
		semantics.ApplyDecision(d.dc.graph, string(sc.ID), opts,
			dec.Answer.Probabilities, dec.ID, dec.Answer.Confidence)
		if w := dec.Winner(); w != "" {
			for i := range sc.Readings {
				if len(sc.Readings[i].Order) > 0 && sc.Readings[i].Order[0] == w {
					sc.Readings[i].Weight = 1
				} else {
					sc.Readings[i].Weight = 0
				}
			}
			sc.Resolved = true
			sc.Prov = append(sc.Prov,
				jlir.PredDecision(dec.ID, dec.Answer.Confidence, "scope %s resolved", sc.Kind))
			d.span.Note("scope %s resolved to %s by %s", sc.Kind, w, dec.ID)
		}
	}
}

// --- D2 predicate senses --------------------------------------------------

// D2 selects among competing predicate senses.
func (d *decider) predicates() {
	for _, v := range d.dc.graph.Events {
		senses := competingSenses(v)
		if len(senses) < 2 {
			continue
		}
		opts := sortedKeys(senses)
		dec := d.ask(jev.Request{
			Stage:           jev.StagePredicate,
			Kind:            jev.KindChoice,
			Action:          jev.ActionSense,
			TargetID:        string(v.ID),
			Instructions:    "Which meaning does the predicate have in this sentence?",
			Criteria:        predicateCriteria(d.dc.graph, opts),
			Prior:           jlir.NewDistribution("lexicon", senses),
			Impact:          0.95,
			SemanticContext: d.dc.graph.Source + "|" + v.Predicate,
			State: map[string]any{
				"sentence":  d.dc.graph.Source,
				"predicate": v.Predicate,
				"tense":     v.Tense,
				"arguments": argSummary(d.dc.graph, v),
			},
		})
		if dec == nil {
			continue
		}
		semantics.ApplyDecision(d.dc.graph, string(v.ID), opts,
			dec.Answer.Probabilities, dec.ID, dec.Answer.Confidence)
		if w := dec.Winner(); w != "" && w != v.Predicate {
			d.span.Note("predicate of %s changed from %s to %s by %s", v.ID, v.Predicate, w, dec.ID)
			v.Predicate = w
			v.Prov = append(v.Prov,
				jlir.PredDecision(dec.ID, dec.Answer.Confidence, "predicate sense"))
		}
	}
}

// competingSenses reads the structured candidate set the semantic layer wrote.
//
// It used to recover the candidates by splitting a provenance note on ';' and
// '=', which threw away the lexicon's actual weights and fed a flat fabricated
// prior to the oracle. The feature is the structured form of the same
// information and is the only thing this should read.
func competingSenses(v *jlir.Event) map[string]float64 {
	out := map[string]float64{}
	if v == nil {
		return out
	}
	f, ok := v.Feature("competing_senses")
	if !ok {
		return out
	}
	switch m := f.Value.(type) {
	case map[string]float64:
		for k, w := range m {
			if w > 0 {
				out[k] = w
			}
		}
	case map[string]any:
		for k, w := range m {
			switch n := w.(type) {
			case float64:
				if n > 0 {
					out[k] = n
				}
			case int:
				out[k] = float64(n)
			}
		}
	}
	return out
}

// --- D3 semantic roles ----------------------------------------------------

// D3 resolves an argument whose role the frame left ambiguous, and writes the
// answer back into the event. A role decision that is not applied leaves the
// projection choosing for itself, which is what plan.md §4 forbids.
func (d *decider) roles() {
	for _, v := range d.dc.graph.Events {
		opts, prior := ambiguousRoles(v)
		if len(opts) < 2 {
			continue
		}
		dec := d.ask(jev.Request{
			Stage:           jev.StageRoles,
			Kind:            jev.KindChoice,
			Action:          jev.ActionRole,
			TargetID:        string(v.ID),
			Instructions:    "Which semantic role does this argument fill in this event?",
			Criteria:        roleCriteria(opts),
			Prior:           jlir.NewDistribution("frame", prior),
			Impact:          0.7,
			SemanticContext: d.dc.graph.Source + "|" + string(v.ID),
			State: map[string]any{
				"sentence":  d.dc.graph.Source,
				"predicate": v.Predicate,
				"arguments": argSummary(d.dc.graph, v),
			},
		})
		if dec == nil {
			continue
		}
		if w := dec.Winner(); w != "" {
			applyRole(v, prior, w, dec)
			d.span.Note("role of %s resolved to %s by %s", v.ID, w, dec.ID)
		}
	}
}

// applyRole commits the D3 answer by moving the argument from the role the
// frame ranked first to the role the oracle chose.
func applyRole(v *jlir.Event, prior map[string]float64, role string, dec *jev.Decision) {
	current := ""
	for _, r := range v.OrderArgs() {
		if prior[r] <= 0 {
			continue
		}
		if current == "" || prior[r] > prior[current] {
			current = r
		}
	}
	if current == "" || current == role {
		v.SetFeature(jlir.Feature{
			Key: "role_decision", Value: role, Confidence: dec.Answer.Confidence,
			Prov: []jlir.Provenance{
				jlir.PredDecision(dec.ID, dec.Answer.Confidence, "semantic role"),
			},
		})
		return
	}
	arg, ok := v.Args[current]
	if !ok {
		return
	}
	delete(v.Args, current)
	arg.Prov = append(arg.Prov,
		jlir.PredDecision(dec.ID, dec.Answer.Confidence, "role %s reassigned to %s", current, role))
	v.Args[role] = arg
}

// --- D4 coreference and zero anaphora -------------------------------------

// D4 asks which discourse entity a zero argument denotes. The answer is folded
// into the referent distribution by the same arithmetic as any other decision,
// and the document store's salience prior is what makes the result a posterior
// rather than a fresh guess (plan.md §16).
func (d *decider) coreference() {
	for _, ent := range d.dc.graph.Entities {
		if !ent.Zero || ent.Referent == nil || len(ent.Referent.Options) < 2 {
			continue
		}
		cands := referentCandidates(d.dc.graph, ent, d.dc.store)
		if len(cands) < 2 {
			continue
		}
		keys := make([]string, len(cands))
		for i, c := range cands {
			keys[i] = string(c)
		}
		prior := ent.Referent
		if d.dc.store != nil {
			if p := d.dc.store.Prior(cands); p != nil {
				prior = p
			}
		}
		dec := d.ask(jev.Request{
			Stage:           jev.StageCoref,
			Kind:            jev.KindChoice,
			Action:          jev.ActionReferent,
			TargetID:        string(ent.ID),
			Instructions:    "Which discourse entity does the subject with no overt expression denote?",
			Criteria:        referentCriteria(d.dc.graph, keys),
			Prior:           prior,
			Impact:          0.9,
			SemanticContext: d.dc.graph.Source + "|" + string(ent.ID),
			State: map[string]any{
				"sentence":   d.dc.graph.Source,
				"candidates": referentList(d.dc.graph, keys),
				"discourse":  discourseSummary(d.dc.graph),
			},
		})
		if dec == nil {
			continue
		}
		semantics.ApplyDecision(d.dc.graph, string(ent.ID), keys,
			dec.Answer.Probabilities, dec.ID, dec.Answer.Confidence)
		if w := dec.Winner(); w != "" {
			d.span.Note("zero argument %s resolved to %s by %s", ent.ID, w, dec.ID)
		}
	}
}

// --- D5 discourse ---------------------------------------------------------

// D5 is a probe, not a commitment: it asks whether the sentence continues an
// earlier discourse so the question appears in the audit trail. It commits
// nothing, which is why its Action is ActionNone rather than an invented target.
func (d *decider) discourse() {
	if len(d.dc.graph.Events) == 0 {
		return
	}
	d.ask(jev.Request{
		Stage:        jev.StageDiscourse,
		Kind:         jev.KindNoul,
		Action:       jev.ActionNone,
		TargetID:     "D5/discourse",
		Instructions: "Does the sentence refer back to an entity already mentioned in the discourse?",
		Criteria: []jev.Criterion{
			{Key: "yes", Description: "the sentence continues an earlier discourse"},
			{Key: "no", Description: "the sentence starts a new discourse"},
		},
		Prior:           jlir.NewDistribution("discourse", map[string]float64{"no": 0.6, "yes": 0.4}),
		Impact:          0.35,
		SemanticContext: d.dc.graph.Source,
		State: map[string]any{
			"sentence":       d.dc.graph.Source,
			"known_entities": discourseSummary(d.dc.graph),
		},
	})
}

// --- D6 pragmatics --------------------------------------------------------

// D6 measures deference and writes it back, because the target projection's
// politeness and honorific decisions consume exactly this number.
func (d *decider) pragmatics() {
	g := d.dc.graph
	dec := d.ask(jev.Request{
		Stage:        jev.StagePragmatic,
		Kind:         jev.KindScore,
		Action:       jev.ActionPragmatic,
		TargetID:     "D6/pragmatics",
		Instructions: "How much social deference does the speaker express toward the addressee or the person referred to?",
		Criteria: []jev.Criterion{
			{Key: "0", Description: "no deference at all"},
			{Key: "1", Description: "slight politeness"},
			{Key: "2", Description: "clear politeness, ordinary business register"},
			{Key: "3", Description: "strong honorific or humble form"},
		},
		Prior: jlir.NewDistribution("morphology", map[string]float64{
			"0": 0.2, "1": 0.4, "2": 0.3, "3": 0.1,
		}),
		Impact:          0.5,
		SemanticContext: g.Source,
		State: map[string]any{
			"sentence":           g.Source,
			"politeness_markers": g.Prag.SentenceFinalParticles,
			"speech_style":       g.Prag.SpeechStyle,
			"honorific":          g.SourceFeat.Honorific,
			"honorific_level":    g.SourceFeat.HonorificLevel,
		},
	})
	if dec == nil || dec.Answer.Score <= 0 {
		return
	}
	g.Prag.Politeness = clamp01(dec.Answer.Score / 3)
	g.Prag.Prov = append(g.Prag.Prov,
		jlir.PredDecision(dec.ID, dec.Answer.Confidence, "deference measured by the oracle"))
	if dec.Answer.Score >= 2.5 {
		g.SourceFeat.Honorific = true
	}
}

// --- D7 target projection -------------------------------------------------

// D7 chooses how arguments with no overt surface form are realized. It commits
// a projection policy the realizer and the loss accounting can both read.
func (d *decider) projection() {
	g := d.dc.graph
	dec := d.ask(jev.Request{
		Stage:        jev.StageProjection,
		Kind:         jev.KindChoice,
		Action:       jev.ActionProjection,
		TargetID:     "D7/realization",
		Instructions: "How should an argument with no overt surface form be realized in the target language?",
		Criteria: []jev.Criterion{
			{Key: "overt", Description: "state it with a full noun phrase or pronoun"},
			{Key: "zero", Description: "leave it unexpressed; the referent is preserved internally"},
			{Key: "demonstrative", Description: "use a demonstrative phrase such as 'that person'"},
		},
		Prior: jlir.NewDistribution("projection", map[string]float64{
			"overt": 0.3, "zero": 0.5, "demonstrative": 0.2,
		}),
		Impact:          0.55,
		SemanticContext: g.Source,
		State: map[string]any{
			"sentence": g.Source,
			"target":   string(d.dc.target),
			"entities": entitySurfaces(g),
		},
	})
	if dec == nil {
		return
	}
	if w := dec.Winner(); w != "" {
		g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "D7:"+w)
		g.Prag.Prov = append(g.Prag.Prov, jlir.PredDecision(
			dec.ID, dec.Answer.Confidence, "zero realization policy: %s", w))
		d.span.Note("projection policy for unrealized arguments: %s (by %s)", w, dec.ID)
	}
}

// --- D8 construction selection --------------------------------------------

// D8 asks which construction best expresses the predicate. It commits a
// preference the planner may honour; the planner still has to be able to
// realize the chosen one, so this is a preference and not an instruction.
func (d *decider) construction() {
	g := d.dc.graph
	if len(g.Events) == 0 {
		return
	}
	v := g.Events[0]
	dec := d.ask(jev.Request{
		Stage:        jev.StageConstruction,
		Kind:         jev.KindChoice,
		Action:       jev.ActionConstruction,
		TargetID:     string(v.ID),
		Instructions: "Which construction best expresses this predicate in the target language?",
		Criteria: []jev.Criterion{
			{Key: "neutral", Description: "the ordinary, unmarked construction"},
			{Key: "formal", Description: "a written or formal register construction"},
			{Key: "casual", Description: "a colloquial construction"},
			{Key: "honorific", Description: "a deferential construction for the measured register"},
		},
		Prior: jlir.NewDistribution("projection", map[string]float64{
			"neutral": 0.5, "formal": 0.2, "casual": 0.15, "honorific": 0.15,
		}),
		Impact:          0.45,
		SemanticContext: g.Source + "|" + v.Predicate,
		State: map[string]any{
			"sentence":  g.Source,
			"predicate": v.Predicate,
			"target":    string(d.dc.target),
			"register":  d.dc.style,
		},
	})
	if dec == nil {
		return
	}
	if w := dec.Winner(); w != "" {
		g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "D8:"+w)
		d.span.Note("construction preference for %s: %s (by %s)", v.ID, w, dec.ID)
	}
}

// --- helpers --------------------------------------------------------------

func optionKeys(cs []jev.Criterion) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Key
	}
	return out
}

func sortedKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func scopeCriteria(opts []string, labels map[string]string) []jev.Criterion {
	out := make([]jev.Criterion, 0, len(opts))
	for _, o := range opts {
		d := labels[o]
		if d == "" {
			d = o
		}
		out = append(out, jev.Criterion{Key: o, Description: d})
	}
	return out
}

func scopeLabels(sc *jlir.ScopeNode) []string {
	out := make([]string, 0, len(sc.Readings))
	for _, r := range sc.Readings {
		out = append(out, r.Label)
	}
	return out
}

func predicateCriteria(g *jlir.Graph, opts []string) []jev.Criterion {
	out := make([]jev.Criterion, 0, len(opts))
	for _, o := range opts {
		c := jev.Criterion{Key: o, Description: o}
		if p := g.Predicate(o); p != nil && p.Gloss != "" {
			c.Description = o + ": " + p.Gloss
		}
		out = append(out, c)
	}
	return out
}

func roleCriteria(opts []string) []jev.Criterion {
	out := make([]jev.Criterion, 0, len(opts))
	for _, o := range opts {
		out = append(out, jev.Criterion{Key: o, Description: o})
	}
	return out
}

func argSummary(g *jlir.Graph, v *jlir.Event) map[string]string {
	out := map[string]string{}
	for _, r := range v.OrderArgs() {
		a := v.Args[r]
		name := string(a.Value)
		if ent := g.Entity(a.Value); ent != nil {
			if alias := ent.Alias(g.Lang); alias != "" {
				name = alias
			}
		}
		out[r] = name
	}
	return out
}

// ambiguousRoles returns the roles an argument could fill with comparable
// probability, which is the only situation where asking the oracle can help.
func ambiguousRoles(v *jlir.Event) ([]string, map[string]float64) {
	prior := map[string]float64{}
	var opts []string
	for _, r := range v.OrderArgs() {
		a := v.Args[r]
		if a.Confidence <= 0 || a.Confidence >= 0.75 {
			continue
		}
		prior[r] += a.Confidence
		opts = append(opts, r)
	}
	sort.Strings(opts)
	return opts, prior
}

func referentCandidates(g *jlir.Graph, zero *jlir.Entity, store *discourse.Store) []jlir.ID {
	seen := map[string]bool{}
	var out []jlir.ID
	add := func(id jlir.ID) {
		if id == "" || seen[string(id)] {
			return
		}
		seen[string(id)] = true
		out = append(out, id)
	}
	for _, e := range g.Entities {
		if e.ID == zero.ID || e.Zero {
			continue
		}
		add(e.ID)
	}
	if store != nil {
		for _, e := range store.Entities() {
			add(e.ID)
		}
	}
	if zero.Referent != nil {
		for _, o := range zero.Referent.Options {
			if o != "UNKNOWN" {
				add(jlir.ID(o))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) == 0 {
		out = []jlir.ID{jlir.ID("UNKNOWN")}
	}
	return out
}

func referentCriteria(g *jlir.Graph, cands []string) []jev.Criterion {
	out := make([]jev.Criterion, 0, len(cands)+1)
	for _, c := range cands {
		d := c
		if e := g.Entity(jlir.ID(c)); e != nil {
			if alias := e.Alias(g.Lang); alias != "" {
				d = alias + " (" + string(e.ID) + ")"
			}
		}
		out = append(out, jev.Criterion{Key: c, Description: d})
	}
	out = append(out, jev.Criterion{
		Key:         "UNKNOWN",
		Description: "none of the listed entities; the source does not determine the referent",
	})
	return out
}

func referentList(g *jlir.Graph, cands []string) []map[string]string {
	out := make([]map[string]string, 0, len(cands))
	for _, c := range cands {
		item := map[string]string{"id": c, "label": referentLabel(c, g)}
		if e := g.Entity(jlir.ID(c)); e != nil {
			item["type"] = e.Type
			item["mentions"] = fmt.Sprint(e.MentionCount)
		}
		out = append(out, item)
	}
	return out
}

func discourseSummary(g *jlir.Graph) []map[string]string {
	out := make([]map[string]string, 0, len(g.Entities))
	for _, e := range g.Entities {
		if e.Zero {
			continue
		}
		out = append(out, map[string]string{
			"id":       string(e.ID),
			"label":    e.Alias(g.Lang),
			"type":     e.Type,
			"mentions": fmt.Sprint(e.MentionCount),
			"gender":   e.Gender,
		})
	}
	return out
}

func entitySurfaces(g *jlir.Graph) []string {
	out := make([]string, 0, len(g.Entities))
	for _, e := range g.Entities {
		if s := e.Alias(g.Lang); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func unknownSurfaces(g *jlir.Graph) []string {
	var out []string
	for _, u := range g.SourceFeat.Unknowns {
		out = append(out, u.Surface)
	}
	sort.Strings(out)
	return out
}

func decisionViews(ds []*jev.Decision) []trace.DecisionView {
	out := make([]trace.DecisionView, 0, len(ds))
	for _, d := range ds {
		if d == nil {
			continue
		}
		out = append(out, d.View())
	}
	return out
}
