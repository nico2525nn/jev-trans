package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/discourse"
	"github.com/nico/jev-trans/internal/jev"
	"github.com/nico/jev-trans/internal/jlir"

	"github.com/nico/jev-trans/internal/semantics"

	"github.com/nico/jev-trans/internal/trace"
)

// decisionBudget bounds how many questions one translation may send to the
// oracle. When it is exhausted the remaining decisions degrade to priors rather
// than the request hanging or the cost running away.
type decisionBudget struct {
	limit int
	used  int
	// seq numbers the decisions so that every one has its own identifier in the
	// audit trail. Two decisions sharing an id cannot be told apart in the UI,
	// and the id is what a provenance entry cites.
	seq int
}

func (b *decisionBudget) take() bool {
	if b.used >= b.limit {
		return false
	}
	b.used++
	return true
}

// runDecisions executes plan.md §30's DAG against the current analysis.
//
// The order is not arbitrary and not parallel: D1 depends on the parse, D2 on
// the lexicon senses D1 left open, D4 on the entities D2 and D3 produced, D7 on
// all of them. A node never runs before its dependencies have produced results,
// because an earlier decision's answer can make a later one unnecessary.
func (e *Engine) runDecisions(ctx context.Context, rec *trace.Recorder, req Request,
	store *discourse.Store, g *jlir.Graph, mode string,
	warnings *[]string) []*jev.Decision {

	budget := &decisionBudget{limit: e.cfg.MaxOracleCalls}
	var out []*jev.Decision

	span := rec.Open(trace.StageDecisions, "jev decision graph")
	span.Label("budget", fmt.Sprint(budget.limit))
	if e.cfg.Jev == nil || !e.cfg.Jev.Enabled() {
		span.Status(trace.StatusWarn)
		span.Note("no OPENCODE_API_KEY: decisions fall back to deterministic analysis priors; every decision is marked source=prior in the audit trail")
		*warnings = append(*warnings, "decision oracle offline: running on analysis priors")
	} else {
		span.Label("oracle", "live")
	}

	// ---- D0 lexical segmentation ------------------------------------------
	// Segmentation is already settled by the analyzer lattice. D0 records the
	// outcome so the audit trail starts at the same place plan.md §30 does and
	// so a segmentation change is visible rather than implicit.
	if d := e.decide(span, ctx, budget, jev.Request{
		Stage:         "D0",
		Kind:          "choice",
		Instructions:  "Which segmentation of the input does this analysis use?",
		Impact:        0.2,
		Prior:         jlir.NewDistribution("analysis", map[string]float64{"longest_match": 0.9, "reconstructed": 0.1}),
		CachedAllowed: true,
		Criteria: []jev.Criterion{
			{Key: "longest_match", Description: "the standard longest-match segmentation of the dictionary lattice"},
			{Key: "reconstructed", Description: "a segmentation built by de-inflection and unknown-word rules"},
		},
		SemanticContext: req.Text,
		Action:          "segmentation",
		State: map[string]any{
			"language": string(req.SourceLang),
			"segments": morphSurfaces(g),
		},
	}); d != nil {
		out = append(out, d)
	}

	// ---- D1 syntactic ambiguity, including scope --------------------------
	for _, sc := range g.Scopes {
		if sc.Resolved || len(sc.Readings) < 2 {
			continue
		}
		opts := make([]string, 0, len(sc.Readings))
		prior := map[string]float64{}
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
		}
		if len(opts) < 2 {
			continue
		}
		d := e.decide(span, ctx, budget, jev.Request{
			Stage:           "D1",
			Kind:            "choice",
			Instructions:    "Which scope reading does this sentence take?",
			Impact:          0.9,
			Prior:           jlir.NewDistribution("analysis", prior),
			CachedAllowed:   true,
			Criteria:        scopeCriteria(opts, sc),
			SemanticContext: g.Source,
			TargetID:        string(sc.ID),
			Action:          "scope",
			State: map[string]any{
				"sentence": g.Source,
				"operator": sc.Kind,
				"readings": scopeLabels(sc),
			},
		})
		if d == nil {
			continue
		}
		semantics.ApplyDecision(g, string(sc.ID), opts, d.Answer.Probabilities, d.ID, d.Answer.Confidence)
		if w := d.Winner(); w != "" {
			for i := range sc.Readings {
				if len(sc.Readings[i].Order) > 0 && sc.Readings[i].Order[0] == w {
					sc.Readings[i].Weight = 1
				} else {
					sc.Readings[i].Weight = 0
				}
			}
			sc.Resolved = true
			sc.Prov = append(sc.Prov, jlir.PredDecision(d.ID, d.Answer.Confidence, "scope %s resolved", sc.Kind))
		}
		out = append(out, d)
	}

	// ---- D2 predicate senses ----------------------------------------------
	for _, v := range g.Events {
		senses := sensesFor(g, v)
		if len(senses) < 2 {
			continue
		}
		opts := sortedKeys(senses)
		d := e.decide(span, ctx, budget, jev.Request{
			Stage:           "D2",
			Kind:            "choice",
			Instructions:    "Which meaning does the predicate have in this sentence?",
			Impact:          0.95,
			Prior:           jlir.NewDistribution("lexicon", senses),
			CachedAllowed:   true,
			Criteria:        predicateCriteria(g, opts),
			SemanticContext: g.Source + "|" + v.Predicate,
			TargetID:        string(v.ID),
			Action:          "sense",
			State: map[string]any{
				"sentence":  g.Source,
				"predicate": v.Predicate,
				"tense":     v.Tense,
				"arguments": argSummary(g, v),
			},
		})
		if d == nil {
			continue
		}
		semantics.ApplyDecision(g, string(v.ID), opts, d.Answer.Probabilities, d.ID, d.Answer.Confidence)
		if w := d.Winner(); w != "" && w != v.Predicate {
			span.Note("predicate of %s changed from %s to %s by %s", v.ID, v.Predicate, w, d.ID)
			v.Predicate = w
			v.Prov = append(v.Prov, jlir.PredDecision(d.ID, d.Answer.Confidence, "predicate sense"))
		}
		out = append(out, d)
	}

	// ---- D3 semantic roles -------------------------------------------------
	for _, v := range g.Events {
		opts, prior := ambiguousRoles(v)
		if len(opts) < 2 {
			continue
		}
		d := e.decide(span, ctx, budget, jev.Request{
			Stage:           "D3",
			Kind:            "choice",
			Instructions:    "Which semantic role does this argument fill in this event?",
			Impact:          0.7,
			Prior:           jlir.NewDistribution("frame", prior),
			CachedAllowed:   true,
			Criteria:        roleCriteria(opts),
			SemanticContext: g.Source + "|" + string(v.ID),
			TargetID:        string(v.ID),
			Action:          "role",
			State: map[string]any{
				"sentence":  g.Source,
				"predicate": v.Predicate,
				"arguments": argSummary(g, v),
			},
		})
		if d != nil {
			out = append(out, d)
		}
	}

	// ---- D4 coreference and zero anaphora ----------------------------------
	for _, ent := range g.Entities {
		if !ent.Zero || ent.Referent == nil || len(ent.Referent.Options) < 2 {
			continue
		}
		cands := referentCandidates(g, ent, store)
		if len(cands) < 2 {
			continue
		}
		prior := store.Prior(cands)
		if prior == nil {
			prior = ent.Referent
		}
		keys := make([]string, len(cands))
		for i, c := range cands {
			keys[i] = string(c)
		}
		d := e.decide(span, ctx, budget, jev.Request{
			Stage:           "D4",
			Kind:            "choice",
			Instructions:    "Which discourse entity does the subject with no overt expression denote?",
			Impact:          0.9,
			Prior:           prior,
			CachedAllowed:   true,
			Criteria:        referentCriteria(g, keys),
			SemanticContext: g.Source + "|" + string(ent.ID),
			TargetID:        string(ent.ID),
			Action:          "referent",
			State: map[string]any{
				"sentence":   g.Source,
				"candidates": referentList(g, keys),
				"discourse":  discourseSummary(g),
			},
		})
		if d == nil {
			continue
		}
		semantics.ApplyDecision(g, string(ent.ID), keys, d.Answer.Probabilities, d.ID, d.Answer.Confidence)
		if d.Winner() != "" {
			span.Note("zero argument %s resolved to %s by %s", ent.ID, d.Winner(), d.ID)
		}
		out = append(out, d)
	}

	// ---- D5 discourse interpretation ---------------------------------------
	if len(g.Events) > 0 {
		d := e.decide(span, ctx, budget, jev.Request{
			Stage:         "D5",
			Kind:          "noul",
			Instructions:  "Does the sentence refer back to an entity already mentioned in the discourse?",
			Impact:        0.35,
			Prior:         jlir.NewDistribution("analysis", map[string]float64{"no": 0.6, "yes": 0.4}),
			CachedAllowed: true,
			Criteria: []jev.Criterion{
				{Key: "yes", Description: "the sentence continues an earlier discourse"},
				{Key: "no", Description: "the sentence starts a new discourse"},
			},
			SemanticContext: g.Source,
			TargetID:        "D5/discourse",
			Action:          "discourse",
			State: map[string]any{
				"sentence":       g.Source,
				"known_entities": discourseSummary(g),
			},
		})
		if d != nil {
			out = append(out, d)
		}
	}

	// ---- D6 pragmatic interpretation ---------------------------------------
	if d := e.decide(span, ctx, budget, jev.Request{
		Stage:         "D6",
		Kind:          "score",
		Instructions:  "How much social deference does the speaker express toward the addressee or the person referred to?",
		Impact:        0.5,
		Prior:         jlir.NewDistribution("morphology", map[string]float64{"0": 0.2, "1": 0.4, "2": 0.3, "3": 0.1}),
		CachedAllowed: true,
		Criteria: []jev.Criterion{
			{Key: "0", Description: "no deference at all"},
			{Key: "1", Description: "slight politeness"},
			{Key: "2", Description: "clear politeness, ordinary business register"},
			{Key: "3", Description: "strong honorific or humble form"},
		},
		SemanticContext: g.Source,
		TargetID:        "D6/pragmatics",
		Action:          "pragmatic",
		State: map[string]any{
			"sentence":           g.Source,
			"politeness_markers": g.Prag.SentenceFinalParticles,
			"speech_style":       g.Prag.SpeechStyle,
			"honorific":          g.SourceFeat.Honorific,
			"honorific_level":    g.SourceFeat.HonorificLevel,
		},
	}); d != nil {
		out = append(out, d)
	}

	span.Count("decisions", len(out))
	span.Data(decisionViews(out))
	if budget.used >= budget.limit {
		span.Status(trace.StatusWarn)
		span.Note("oracle budget exhausted after %d questions; remaining decisions use priors", budget.used)
	}
	span.Label("mode", mode)
	span.Close()
	return out
}

// decide performs one decision with budget enforcement, returning a record even
// when it was skipped so the audit trail has no holes.
func (e *Engine) decide(span *trace.Span, ctx context.Context, b *decisionBudget, rq jev.Request) *jev.Decision {
	if len(rq.Criteria) == 0 {
		return nil
	}
	if rq.Prior == nil {
		rq.Prior = jlir.Uniform(optionKeys(rq.Criteria)...)
	}
	b.seq++
	if rq.ID == "" {
		rq.ID = fmt.Sprintf("JEV-%d", b.seq)
	}
	if !b.take() {
		skipped := &jev.Decision{
			ID:         "skipped-" + rq.Stage + "-" + fmt.Sprint(b.seq),
			Stage:      rq.Stage,
			Kind:       rq.Kind,
			Question:   rq.Instructions,
			Source:     jev.SourceSkipped,
			SkipReason: "oracle budget exhausted",
		}
		span.Note("skipped %s: oracle budget exhausted", rq.Stage)
		return skipped
	}
	cli := e.cfg.Jev
	if cli == nil {
		cli = jev.New(jev.Options{})
	}
	d, err := cli.Ask(ctx, rq)
	if err != nil {
		span.Note("decision %s failed (%v); falling back to prior", rq.Stage, err)
		d = &jev.Decision{
			ID: rq.Stage + "-prior", Stage: rq.Stage, Kind: rq.Kind,
			Question: rq.Instructions, Source: jev.SourcePrior,
			SkipReason: fmt.Sprintf("oracle error: %v", err),
		}
	}
	return d
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

func morphSurfaces(g *jlir.Graph) []string {
	out := make([]string, 0, len(g.Entities))
	for _, e := range g.Entities {
		if s := e.Alias(g.Lang); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func scopeCriteria(opts []string, sc *jlir.ScopeNode) []jev.Criterion {
	labels := map[string]string{}
	for _, r := range sc.Readings {
		if len(r.Order) > 0 {
			labels[r.Order[0]] = r.Label
		}
	}
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

// sensesFor returns the competing predicate senses of an event. The semantic
// layer records the alternatives on the event's lexical provenance note so
// this package does not have to re-open the lexicon; the ontology sense
// descriptions come from the graph's own predicate table.
func sensesFor(g *jlir.Graph, v *jlir.Event) map[string]float64 {
	out := map[string]float64{}
	if v == nil {
		return out
	}
	if v.Predicate != "" {
		out[v.Predicate] += 1
	}
	for _, pr := range v.Prov {
		if pr.Origin != jlir.OriginLexical {
			continue
		}
		for _, alt := range strings.Split(pr.Note, ";") {
			alt = strings.TrimSpace(alt)
			if alt == "" || alt == v.Predicate {
				continue
			}
			if i := strings.Index(alt, "="); i > 0 {
				out[alt[:i]] += 0.5
				continue
			}
			out[alt] += 0.5
		}
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

func roleCriteria(opts []string) []jev.Criterion {
	out := make([]jev.Criterion, 0, len(opts))
	for _, o := range opts {
		out = append(out, jev.Criterion{Key: o, Description: o})
	}
	return out
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
