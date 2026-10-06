package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/nico/jev-trans/internal/discourse"
	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jev"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/lexicon"
	"github.com/nico/jev-trans/internal/plan"
	"github.com/nico/jev-trans/internal/semantics"
	"github.com/nico/jev-trans/internal/syntax"
	"github.com/nico/jev-trans/internal/trace"
	"github.com/nico/jev-trans/internal/verify"
)

// ErrEmptyInput is returned when there is nothing to translate.
var ErrEmptyInput = errors.New("no input text")

// ErrSameLanguage is returned when source and target are identical; the
// pipeline has nothing to project.
var ErrSameLanguage = errors.New("source and target language are identical")

// Translate runs one sentence through the whole circuit and returns the full
// evidence, not just a string.
//
// The stage order below is plan.md §6 verbatim. Each stage gets its own span
// so the WebUI can show it light up in sequence, and each stage's artifact is
// carried on the Response rather than being reduced to a debug log.
func (e *Engine) Translate(ctx context.Context, req Request) (*Response, error) {
	rec := trace.New("translate")
	ctx = trace.With(ctx, rec)

	if !req.SourceLang.Valid() || !req.TargetLang.Valid() {
		return nil, fmt.Errorf("unsupported language pair %q -> %q", req.SourceLang, req.TargetLang)
	}
	if req.SourceLang == req.TargetLang {
		return nil, ErrSameLanguage
	}
	mode := req.Mode
	if mode == "" {
		mode = e.cfg.DefaultMode
	}

	resp := &Response{
		Source: SideInfo{Lang: req.SourceLang},
		Target: SideInfo{Lang: req.TargetLang, Title: req.TargetLang.Name()},
	}
	var warnings []string
	store := e.store(req.DocumentID)

	rec.Do(trace.StageDiscourse, "document state", func(s *trace.Span) error {
		snap := store.Snapshot()
		resp.DocumentState = snap
		s.Data(snap)
		s.Count("known entities", len(snap.Entities))
		s.Label("version", fmt.Sprint(snap.Version))
		return nil
	})

	// ---- 1. INPUT NORMALIZATION -------------------------------------------
	var norm string
	rec.Do(trace.StageNormalize, "input normalization", func(s *trace.Span) error {
		norm = normalizeInput(s, req.Text, e.cfg.ExternalMorph)
		s.Data(map[string]any{"input": req.Text, "normalized": norm})
		return nil
	})
	resp.Source.Text = norm
	if norm == "" {
		return nil, ErrEmptyInput
	}

	var mf *forest.MorphForest

	// ---- 2. MORPHOLOGICAL LATTICE -----------------------------------------
	// The morphological dictionary is fed by the predicate lexicon (see
	// lex.RegisterJapaneseVerb), and the lexicon is built lazily. Initialising it
	// here, before the analyzer runs, is what makes 住っていた segmentable; with
	// the reverse order the analyzer would consult a dictionary that had not yet
	// been told about 住む.
	lexicon.Default()

	var backends []string
	var prof lex.Profile
	rec.Do(trace.StageMorph, "morphological lattice", func(s *trace.Span) error {
		// The plain entry points are used deliberately: this stage already owns
		// a span and attaches the lattice to it, so an inner span would
		// duplicate the node in the circuit. The *In variants exist for callers
		// that want the nested spans.
		switch req.SourceLang {
		case lang.JA:
			mf, prof, backends = e.analyzeJapanese(ctx, s, norm)
		default:
			mf = lex.AnalyzeEN(norm)
		}
		for _, b := range backends {
			s.Label("analyzer", b)
		}
		_ = prof
		if mf == nil {
			return errors.New("morphological analyzer returned nothing")
		}
		resp.Artifacts.Morph = mf
		s.Data(mf)
		s.Count("morphemes", len(mf.Best().Morphs))
		s.Count("lattice paths", len(mf.Paths))
		s.Label("unknown rate", fmt.Sprintf("%.2f", mf.UnknownRate))
		s.Count("dictionary entries", mf.DictionarySize)
		for _, m := range mf.Best().Morphs {
			if !m.Dict {
				s.Note("ungrounded morpheme %q at byte %d; carried as UNKNOWN, not guessed",
					m.Surface, m.Start)
			}
		}
		if mf.UnknownRate > 0.2 {
			s.Status(trace.StatusWarn)
			warnings = append(warnings, fmt.Sprintf("low morphological coverage (%.0f%%)", mf.UnknownRate*100))
		}
		return nil
	})

	// ---- 3. PACKED SYNTACTIC FOREST ---------------------------------------
	var bundle *syntax.Bundle
	rec.Do(trace.StageParse, "packed syntactic forest", func(s *trace.Span) error {
		switch req.SourceLang {
		case lang.JA:
			bundle = syntax.ParseJA(norm, mf)
		default:
			bundle = syntax.ParseEN(norm, mf)
		}
		if bundle == nil {
			return errors.New("parser returned nothing")
		}
		resp.Artifacts.Syntax = bundle
		s.Data(bundle)
		s.Count("clauses", len(bundle.Clauses))
		s.Label("tier", fmt.Sprint(bundle.Tier))
		s.Label("coverage", fmt.Sprintf("%.2f", bundle.Coverage))
		if bundle.Tier > 1 {
			s.Status(trace.StatusWarn)
			s.Note("tier %d (robust fallback) parse; some structure is underdetermined", bundle.Tier)
		}
		for _, u := range bundle.Unknowns {
			s.Note("unresolved: %s", u)
		}
		return nil
	})
	if bundle == nil {
		return nil, errors.New("source analysis produced no parse")
	}

	// ---- 4. SOURCE SEMANTIC FOREST ----------------------------------------
	var semForest *semantics.Forest
	rec.Do(trace.StageSemantic, "source semantic forest", func(s *trace.Span) error {
		semForest = semantics.BuildForest(bundle, req.SourceLang)
		if semForest == nil || len(semForest.Readings) == 0 {
			return errors.New("no semantic reading could be constructed")
		}
		resp.Artifacts.SemanticForest = semForest
		s.Data(semForest)
		s.Count("readings", len(semForest.Readings))
		if len(semForest.Readings) > 1 {
			s.Status(trace.StatusWarn)
			s.Note("%d source readings kept open; ambiguity is preserved rather than resolved by fiat",
				len(semForest.Readings))
		}
		return nil
	})
	if semForest == nil || len(semForest.Readings) == 0 {
		return nil, errors.New("no semantic reading could be constructed")
	}
	srcGraph := semForest.Readings[0].Graph
	resp.JLIR.Source = srcGraph
	resp.JLIR.Readings = semForest.Readings
	for _, r := range semForest.Readings {
		resp.Result.Interpretations = append(resp.Result.Interpretations, Interpretation{
			Weight: r.Weight, Origin: r.Origin, Label: describeReading(r.Graph),
		})
	}

	// ---- 5. JLIR CORE -----------------------------------------------------
	rec.Do(trace.StageJLIR, "jlir core", func(s *trace.Span) error {
		s.Data(srcGraph)
		s.Detail("%s", srcGraph.Describe())
		s.Count("entities", len(srcGraph.Entities))
		s.Count("events", len(srcGraph.Events))
		s.Count("scopes", len(srcGraph.Scopes))
		s.Count("open positions", len(srcGraph.Unresolved()))
		s.Label("politeness", fmt.Sprintf("%.2f", srcGraph.Prag.Politeness))
		return nil
	})

	// ---- 6. JEV DECISION GRAPH --------------------------------------------
	decisions := e.runDecisions(ctx, rec, req, store, srcGraph, mode, &warnings)

	// ---- 7. CONSTRAINED JLIR STATE ---------------------------------------
	rec.Do(trace.StageConstrained, "constrained jlir state", func(s *trace.Span) error {
		if req.Answer != nil {
			applyUserAnswer(s, srcGraph, req.Answer)
		}
		open := srcGraph.Unresolved()
		s.Data(srcGraph)
		s.Count("open positions", len(open))
		s.Detail("ambiguity retained: %v", open)
		if len(open) > 0 {
			s.Status(trace.StatusWarn)
			s.Note("carrying %d unresolved position(s) into projection", len(open))
		}
		for _, f := range srcGraph.UnsupportedFeatures() {
			s.Status(trace.StatusError)
			s.Note("source asserts %s=%s on %s with no upstream provenance", f.Key, f.Value, f.Owner)
		}
		return nil
	})

	pr := e.planRequest(ctx, req, srcGraph, decisions)

	// ---- 8. TARGET PROJECTION ENGINE --------------------------------------
	var projection *plan.Projection
	rec.Do(trace.StageProjection, "target projection engine", func(s *trace.Span) error {
		projection = plan.Project(pr)
		if projection == nil {
			return errors.New("projection produced nothing")
		}
		resp.Artifacts.Projection = projection
		s.Data(projection)
		s.Label("target", string(projection.Target))
		s.Label("register", projection.Style.Register)
		s.Label("politeness", fmt.Sprintf("%.2f", projection.Style.Politeness))
		for _, h := range projection.LossHints {
			s.Status(trace.StatusWarn)
			s.Note("cannot realize in %s: %s (%s)", projection.Target, h.Feature, h.Reason)
		}
		for _, n := range projection.Notes {
			s.Note("%s", n)
		}
		return nil
	})

	// ---- 9. TARGET MESSAGE PLANNER ----------------------------------------
	rec.Do(trace.StagePlan, "target message planner", func(s *trace.Span) error {
		s.Data(projectionViewOf(projection))
		s.Count("clauses", len(projection.Events))
		if len(projection.Events) == 0 {
			s.Status(trace.StatusWarn)
			s.Note("no event to plan: the source carried no propositional content")
		}
		return nil
	})

	// ---- 10. CONSTRUCTION SELECTION ---------------------------------------
	rec.Do(trace.StageConstruct, "construction selection", func(s *trace.Span) error {
		s.Data(constructionView(projection))
		for _, ev := range projection.Events {
			s.Count("constructions", 1)
			if ev.Construction != "" {
				s.Label(string(ev.EventID), ev.Construction)
			}
		}
		if len(projection.Decisions) > 0 {
			s.Data(projection.Decisions)
		}
		return nil
	})

	// ---- 11. PACKED REALIZATION FOREST ------------------------------------
	var realization *forest.Realization
	rec.Do(trace.StageRealize, "packed realization forest", func(s *trace.Span) error {
		realization = plan.Realize(pr, projection)
		if realization == nil {
			return errors.New("realizer produced nothing")
		}
		resp.Artifacts.Realization = realization
		s.Data(realization)
		if realization.Forest != nil {
			st := realization.Forest.Stats()
			s.Count("nodes", st.Nodes)
			s.Count("shared nodes", st.Shared)
			s.Count("terminals", st.Terminals)
			s.Count("max depth", st.MaxDepth)
		}
		for _, rb := range realization.Rejected {
			s.Status(trace.StatusWarn)
			s.Note("rejected %q by %s: %s", rb.Lex, rb.Rule, rb.Reason)
		}
		return nil
	})

	// ---- 12. GRAMMATICAL REALIZER -----------------------------------------
	var raw []forest.Candidate
	rec.Do(trace.StageSurface, "grammatical realizer", func(s *trace.Span) error {
		raw = realization.Candidates(e.cfg.MaxCandidates)
		s.Data(raw)
		s.Count("candidates", len(raw))
		for _, c := range raw {
			s.Detail("%s", c.Text)
		}
		return nil
	})

	// ---- 13-15. TARGET RE-PARSER / TARGET JLIR / VERIFIER ------------------
	candidates, vCands, targetGraph := e.verifyCandidates(ctx, rec, req, srcGraph, raw, &warnings)
	resp.JLIR.Target = targetGraph

	// ---- 16. JEV RERANKER: hard gate, Pareto dominance, D9 ----------------
	//
	// RankWith runs unconditionally. It is the only place the plan.md §44 hard
	// gate is enforced, and guarding the call on len(candidates) > 1 meant that
	// when the hard constraints pruned the forest to a single derivation --
	// precisely the case where the gate matters most -- the candidate reached
	// FINAL OUTPUT with whatever status the verifier gave it, including
	// UNSUPPORTED. A single candidate that fails the gate must still fail.
	//
	// RankWith is given a nil recorder so that it does not open a second span
	// for the same stage; this block owns the stage.
	rankCtx := trace.With(ctx, rec)
	outcome, rankErr := verify.RankWith(rankCtx, nil, vCands, e.naturalness)
	rec.Do(trace.StageRerank, "hard gate, dominance and jev rerank", func(s *trace.Span) error {
		s.Count("input candidates", len(vCands))
		if outcome == nil {
			s.Status(trace.StatusWarn)
			s.Note("ranking produced no outcome; every candidate is reported unverified")
			return nil
		}
		s.Count("surviving", len(outcome.Ranked))
		s.Count("hard gate rejected", len(outcome.Pruned))
		s.Count("dominance edges", len(outcome.Drops))
		s.Data(outcome)
		for _, d := range outcome.Drops {
			s.Note("dominated: %s", d)
		}
		for _, p := range outcome.Pruned {
			s.Note("hard gate rejected %q: %s", p.Text, strings.Join(p.Rejected, "; "))
		}
		if len(outcome.Pruned) > 0 {
			s.Status(trace.StatusError)
		}
		if outcome.Applied {
			s.Label("rerank", "jev")
		} else {
			s.Note("no oracle rerank applied; ordering is by verifier loss and construction prior")
		}
		if rankErr != nil {
			s.Note("rerank hook failed: %v", rankErr)
		}
		return nil
	})
	if rankErr != nil {
		warnings = append(warnings, "rerank hook failed: "+rankErr.Error())
	}
	candidates = fromVerifyCandidates(outcomeRanked(outcome))
	for _, c := range candidates {
		if c.RejectedBy == nil {
			c.RejectedBy = nil
		}
	}

	// ---- stage metrics ---------------------------------------------------
	//
	// Computed here, by the code that did the work, and cumulative: a stage
	// only counts when every earlier stage counted for the same sentence.
	resp.Metrics = stageMetrics(mf, srcGraph, projection, raw, candidates)

	// ---- 17. FINAL OUTPUT -------------------------------------------------
	rec.Do(trace.StageOutput, "final output", func(s *trace.Span) error {
		resp.Result.Candidates = candidates
		resp.Result.Status = aggregateStatus(candidates)
		if len(candidates) > 0 {
			best := candidates[0]
			resp.Result.Selected = &best
		}
		for _, c := range candidates {
			if strings.TrimSpace(c.Text) != "" {
				resp.Target.Text = c.Text
				break
			}
		}
		s.Data(resp.Result)
		s.Count("candidates", len(candidates))
		s.Detail("status=%s", resp.Result.Status)
		if resp.Result.Status != StatusExact && resp.Result.Status != StatusGood {
			s.Status(trace.StatusWarn)
		}
		return nil
	})

	// ---- document state update, last so it sees the final analysis ---------
	rec.Do(trace.StageDiscourse, "document state update", func(s *trace.Span) error {
		store.Observe(srcGraph)
		snap := store.Snapshot()
		resp.DocumentState = snap
		s.Data(snap)
		s.Count("entities", len(store.Entities()))
		s.Label("version", fmt.Sprint(snap.Version))
		return nil
	})

	resp.Result.Questions = e.questions(rec, srcGraph, mode)
	resp.Artifacts.Decisions = decisionViews(decisions)
	resp.Warnings = dedupe(warnings)
	resp.Summary = rec.Summary(resp.Artifacts.Decisions)
	resp.Trace = rec.Root()

	if len(candidates) == 0 {
		resp.Result.Status = StatusUnparsable
	}
	return resp, nil
}

func (e *Engine) store(id string) *discourse.Store {
	if id == "" {
		id = "default"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stores == nil {
		e.stores = map[string]*discourse.Store{}
	}
	s, ok := e.stores[id]
	if !ok {
		s = discourse.NewStore()
		e.stores[id] = s
	}
	return s
}

// ResetStore drops a document's accumulated state.
func (e *Engine) ResetStore(id string) {
	if id == "" {
		id = "default"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.stores, id)
}

// planRequest builds the planner request. The oracle hook only *reads* answers
// the decision graph already obtained for a slot; asking the oracle is the
// decision graph's job, which is what keeps every question recorded, cached
// and scheduled.
func (e *Engine) planRequest(ctx context.Context, req Request, g *jlir.Graph, decisions []*jev.Decision) plan.Request {
	return plan.Request{
		JLIR:       g,
		Source:     req.SourceLang,
		Target:     req.TargetLang,
		Style:      req.Style,
		Ctx:        ctx,
		Lexicalize: e.lexicalize,
		IsName:     e.lex.IsProperName,
		Resolve: func(stage, slot string, options []string, prior jlir.Distribution) (string, float64, string) {
			for _, d := range decisions {
				if d == nil || d.Source == jev.SourceSkipped {
					continue
				}
				if slotMatches(d, slot) && d.Winner() != "" {
					return d.Winner(), d.Answer.Confidence, d.ID
				}
			}
			if prior.Winner != "" {
				return prior.Winner, prior.P(prior.Winner), "prior"
			}
			// No evidence: return nothing. Returning options[0] commits the
			// first enumerated candidate for a slot we know nothing about, which
			// is the invented answer plan.md §4 rules out. An empty resolution
			// makes the planner record a gap instead.
			return "", 0, "unresolved"
		},
	}
}

// analyzeJapanese runs the morphological stage through the backend registry.
//
// Which backend answered is recorded on the span with its version, dictionary
// and the profile it was asked for. That is not decoration: an analysis from
// the builtin 800-surface dictionary and one from SudachiDict are different
// evidence, and a run that silently fell back to the weaker analyser has to be
// distinguishable from one that did not.
func (e *Engine) analyzeJapanese(ctx context.Context, s *trace.Span, text string) (*forest.MorphForest, lex.Profile, []string) {
	profile := e.cfg.MorphProfile
	if profile == "" || !profile.Valid() {
		profile = lex.ProfileAuto
	}
	an, err := e.morph.Analyze(ctx, text, profile, lang.JA)
	if err != nil || an == nil {
		// The registry already tried every registered backend, so a further
		// silent fallback would hide a configuration problem.
		s.Note("every morphological backend failed (%v); using the builtin analyser", err)
		s.Status(trace.StatusWarn)
		mf := lex.AnalyzeJAIn(ctx, text)
		return mf, lex.ProfileModern, []string{"builtin (fallback)"}
	}
	s.Label("profile", string(an.Profile))
	s.Detail("%s %s, dictionary %s, %d tokens",
		an.Backend, an.BackendVersion, an.Dictionary, len(an.Tokens))
	s.Count("tokens", len(an.Tokens))
	s.Count("unresolved", len(an.Unresolved()))
	return an.Lattice(text), an.Profile, []string{
		fmt.Sprintf("%s/%s/%s", an.Backend, an.BackendVersion, an.Dictionary),
	}
}

// lexicalize resolves a target-language surface for a referent the graph only
// knows in the source language. It is a lookup, not a decision: 本 is "book"
// because the lexeme table says so, and a word the table does not cover
// returns ok=false so the planner files a lexical gap rather than inventing a
// word (plan.md §10, §51).
//
// The surface is tried in the table its kind suggests and then in the other one.
// 太郎 lives in the name table, but whether the analysis marked the entity
// proper is a separate decision that may not have been made, and guessing wrong
// leaves the referent unrealized and the sentence unsayable.
func (e *Engine) lexicalize(surface string, src, tgt lang.Lang, typ string, proper bool) (string, bool) {
	if surface == "" {
		return "", false
	}
	for _, asProper := range []bool{proper, !proper} {
		if v, ok := e.lex.Form(surface, src, tgt, typ, asProper); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// slotMatches reports whether a recorded decision speaks for this slot.
func slotMatches(d *jev.Decision, slot string) bool {
	if d.TargetID != "" && d.TargetID == slot {
		return true
	}
	for _, o := range d.Options {
		if o.Key == slot {
			return true
		}
	}
	return false
}

// naturalness is the D9 rerank hook. It returns nil when the oracle is
// offline, which leaves the verifier-derived ordering untouched rather than
// reordering on invented numbers.
func (e *Engine) naturalness(ctx context.Context, cands []verify.Candidate) ([]float64, error) {
	if e.cfg.Jev == nil || !e.cfg.Jev.Enabled() || len(cands) == 0 {
		return nil, nil
	}
	reqs := make([]jev.Request, 0, len(cands))
	for i, c := range cands {
		reqs = append(reqs, jev.Request{
			Stage:         "D9",
			Kind:          "score",
			Instructions:  "How natural does this sentence sound as a translation, judged only on fluency and idiomaticity?",
			Impact:        0.2,
			CachedAllowed: true,
			Criteria: []jev.Criterion{
				{Key: "0", Description: "unnatural or wrong for the language"},
				{Key: "1", Description: "acceptable but stiff"},
				{Key: "2", Description: "natural"},
				{Key: "3", Description: "idiomatic and fluent, as a native speaker would write it"},
			},
			SemanticContext: c.Text,
			TargetID:        fmt.Sprintf("D9/c%d", i),
			Action:          "ranking",
			State: map[string]any{
				"sentence": c.Text,
				"loss":     c.Verify.Loss.Total(),
			},
		})
	}
	ds, err := e.cfg.Jev.AskMany(ctx, reqs)
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(cands))
	for i := range cands {
		if i < len(ds) && ds[i] != nil {
			out[i] = ds[i].Answer.Score / 3
		}
	}
	return out, nil
}

// verifyCandidates re-parses and verifies every realized candidate. This is the
// loop plan.md §40 calls the core safety device: nothing reaches the user
// without having been parsed again and compared feature by feature.
func (e *Engine) verifyCandidates(ctx context.Context, rec *trace.Recorder, req Request,
	src *jlir.Graph, raw []forest.Candidate, warnings *[]string) ([]Candidate, []verify.Candidate, *jlir.Graph) {

	out := make([]Candidate, 0, len(raw))
	vCands := make([]verify.Candidate, 0, len(raw))
	if len(raw) == 0 {
		s := rec.Open(trace.StageReparse, "target re-parser")
		s.Status(trace.StatusSkip)
		s.Note("no candidates reached the verifier")
		s.Close()
		return out, vCands, nil
	}

	// kept pairs each re-parse with the candidate it came from. Building a
	// parallel texts/regraphs slice and indexing raw by position attached the
	// right sentence to the wrong candidate's constructions, provenance trace
	// and naturalness score whenever a blank candidate was skipped.
	type keptCand struct {
		cand  forest.Candidate
		graph *jlir.Graph
	}
	var kept []keptCand
	texts := make([]string, 0, len(raw))

	rep := rec.Open(trace.StageReparse, "target re-parser")
	blank := 0
	for _, c := range raw {
		if strings.TrimSpace(c.Text) == "" {
			blank++
			continue
		}
		tgt := verify.Reparse(c.Text, req.TargetLang)
		kept = append(kept, keptCand{cand: c, graph: tgt})
		texts = append(texts, c.Text)
		if tgt == nil {
			rep.Note("could not re-parse %q; the target sentence is not well formed", c.Text)
			rep.Status(trace.StatusError)
			*warnings = append(*warnings, fmt.Sprintf("candidate %q could not be re-parsed", c.Text))
		}
	}
	rep.Count("candidates", len(kept))
	if blank > 0 {
		rep.Note("skipped %d blank candidate(s)", blank)
	}
	rep.Data(texts)
	rep.Close()

	tj := rec.Open(trace.StageTargetJLIR, "target jlir")
	tj.Count("graphs", len(kept))
	graphs := make([]*jlir.Graph, 0, len(kept))
	for _, k := range kept {
		graphs = append(graphs, k.graph)
	}
	tj.Data(graphs)
	tj.Close()

	ver := rec.Open(trace.StageVerify, "semantic equivalence verifier")
	ver.Detail("comparing source and target feature by feature")
	ver.Label("method", "feature diff, no embedding similarity")
	var targetGraph *jlir.Graph
	for _, k := range kept {
		text := k.cand.Text
		cand := Candidate{Text: text, Trace: k.cand.Trace, Constructions: k.cand.Constructions}
		cand.Naturalness = k.cand.Probability
		if k.graph != nil && targetGraph == nil {
			// The API contract exposes jlir.target; it was declared and never
			// assigned, so the UI's target-graph tab always reported it absent.
			targetGraph = k.graph
		}
		if k.graph == nil {
			res := &verify.Result{Status: string(StatusUnparsable), Notes: []string{
				"target sentence could not be re-parsed"}}
			cand.Status = StatusUnparsable
			cand.Notes = append(cand.Notes, res.Notes...)
			cand.Loss = res.Loss
			cand.Confidence = Confidence{ByFeature: map[string]float64{}}
			out = append(out, cand)
			vCands = append(vCands, verify.Candidate{
				Text: text, Verify: res, Naturalness: cand.Naturalness,
				Constructions: cand.Constructions, Trace: cand.Trace,
			})
			continue
		}
		res := verify.Verify(src, k.graph, req.SourceLang, req.TargetLang)
		cand.Loss = res.Loss
		cand.Unsupported = res.Unsupported
		cand.Diffs = res.Diffs
		cand.Notes = append(cand.Notes, res.Notes...)
		cand.Confidence = Confidence{Overall: res.Confidence["overall"], ByFeature: res.Confidence}
		cand.Status = Status(res.Status)
		for _, d := range res.Diffs {
			if d.Severity == "hard" {
				ver.Note("%q rejected: %s %s", text, d.Item, d.Detail)
			}
		}
		for _, u := range res.Unsupported {
			ver.Note("%q introduces %s=%s with no source provenance", text, u.Key, u.Value)
			*warnings = append(*warnings, fmt.Sprintf("candidate %q invents %s", text, u.Key))
		}
		if cand.Status == StatusUnsupported || cand.Status == StatusUnparsable {
			ver.Status(trace.StatusError)
		}
		out = append(out, cand)
		vCands = append(vCands, verify.Candidate{
			Text: text, Verify: res, Naturalness: cand.Naturalness,
			Constructions: cand.Constructions, Trace: cand.Trace,
		})
	}
	ver.Count("candidates", len(out))
	ver.Data(out)
	ver.Close()
	_ = ctx
	return out, vCands, targetGraph
}

// outcomeRanked extracts the surviving candidates from a ranking outcome,
// tolerating a nil outcome so a failed rerank degrades rather than panics.
func outcomeRanked(outcome *verify.RankOutcome) []verify.Candidate {
	if outcome == nil {
		return nil
	}
	return outcome.Ranked
}

func fromVerifyCandidates(in []verify.Candidate) []Candidate {
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		cand := Candidate{
			Text: c.Text, Constructions: c.Constructions,
			Trace: c.Trace, Naturalness: c.Naturalness,
		}
		if c.Verify != nil {
			cand.Loss = c.Verify.Loss
			cand.Unsupported = c.Verify.Unsupported
			cand.Diffs = c.Verify.Diffs
			cand.Notes = append(cand.Notes, c.Verify.Notes...)
			cand.Confidence = Confidence{Overall: c.Verify.Confidence["overall"], ByFeature: c.Verify.Confidence}
			cand.Status = Status(c.Verify.Status)
		}
		out = append(out, cand)
	}
	return out
}

// aggregateStatus reports the best achievable status across candidates. The
// pipeline is not required to always return a single confident translation;
// plan.md §61 requires it to be able to say that it does not know.
func aggregateStatus(cands []Candidate) Status {
	if len(cands) == 0 {
		return StatusUnparsable
	}
	best := cands[0].Status
	for _, c := range cands[1:] {
		if c.Status.Severity() < best.Severity() {
			best = c.Status
		}
	}
	return best
}

// questions emits an interactive question only when the ambiguity genuinely
// changes the translation output, which is the bar plan.md §62 sets. In auto
// mode it emits none: ordinary ambiguity is preserved in the output instead of
// being pushed onto the user.
func (e *Engine) questions(rec *trace.Recorder, g *jlir.Graph, mode string) []Question {
	if mode == "auto" {
		return nil
	}
	var out []Question
	n := 0
	for _, ent := range g.Entities {
		if !ent.Zero || ent.Referent == nil || len(ent.Referent.Options) < 2 {
			continue
		}
		top := ent.Referent.Top(3)
		if len(top) == 0 || top[0].P-secondOrZero(top) > 0.25 {
			continue
		}
		n++
		q := Question{
			ID:       fmt.Sprintf("q%d", n),
			Kind:     "referent",
			Prompt:   "One clause has no overt subject in the source. Which referent does it denote?",
			Why:      "the choice changes who the clause is about in the target text",
			Blocking: true,
			Default:  top[0].Option,
		}
		for _, t := range top {
			q.Options = append(q.Options, Option{
				Key: t.Option, Label: referentLabel(t.Option, g), Probability: t.P,
			})
		}
		out = append(out, q)
	}
	for _, sc := range g.Scopes {
		if sc.Resolved || len(sc.Readings) < 2 {
			continue
		}
		n++
		q := Question{
			ID:       fmt.Sprintf("q%d", n),
			Kind:     "scope",
			Prompt:   fmt.Sprintf("How do the operators of %s combine?", sc.Kind),
			Why:      "the readings differ in meaning, not only in emphasis",
			Blocking: true,
		}
		for _, r := range sc.Readings {
			key := r.Order[0]
			if key == "" {
				key = r.Label
			}
			q.Options = append(q.Options, Option{Key: key, Label: r.Label, Probability: r.Weight})
			if q.Default == "" {
				q.Default = key
			}
		}
		if len(q.Options) > 0 {
			out = append(out, q)
		}
	}
	if len(out) > 0 {
		rec.Root().Note("%d interactive question(s) raised in %s mode", len(out), mode)
	}
	return out
}

func secondOrZero(top []jlir.Ranked) float64 {
	if len(top) < 2 {
		return 0
	}
	return top[1].P
}

// referentLabel resolves a distribution option key to something a human can
// read. Options are discourse entity IDs, except for the UNKNOWN sentinel.
func referentLabel(key string, g *jlir.Graph) string {
	if key == "" || key == "UNKNOWN" {
		return "unresolved"
	}
	if ent := g.Entity(jlir.ID(key)); ent != nil {
		if name := ent.Name(g.Lang); name != "" {
			return name
		}
		if alias := ent.Alias(g.Lang); alias != "" {
			return alias
		}
		return string(ent.ID)
	}
	return key
}

func applyUserAnswer(s *trace.Span, g *jlir.Graph, a *Answer) {
	if a == nil || a.Option == "" {
		return
	}
	applied := false
	for _, ent := range g.Entities {
		if !ent.Zero || ent.Referent == nil {
			continue
		}
		for _, o := range ent.Referent.Options {
			if o != a.Option {
				continue
			}
			ent.Referent.Options = []string{a.Option}
			ent.Referent.Prob = map[string]float64{a.Option: 1}
			ent.Referent.Winner = a.Option
			ent.Referent.Resolved = true
			ent.Referent.Provenance = jev.SourceUser
			ent.SetFeature(jlir.Feature{
				Key: "referent", Value: a.Option, Confidence: 1,
				Prov: []jlir.Provenance{jlir.PredDecision(a.QuestionID, 1, "user disambiguation")},
			})
			applied = true
			s.Note("applied user answer %s=%s", ent.ID, a.Option)
		}
	}
	if !applied {
		s.Note("user answer %q did not match any open position", a.Option)
	}
	g.ResolveZero()
}

// historicalKana maps pre-1946 orthography onto the modern spelling.
//
// Aozora Bunko is full of it: 宮沢賢治 alone writes ゐる where any modern
// edition has いる. The morphological analyzer's lexicon is modern, so every
// historical form is an unknown morpheme, and an unknown morpheme is fatal by
// design. Normalizing here — at the INPUT NORMALIZATION stage plan.md §6
// provides — turns a whole class of "the analyzer cannot segment this" into an
// ordinary word. The substitution is recorded in the trace, and the original
// text is preserved on the response, so nothing is silently discarded.
var historicalKana = map[rune]rune{
	'ゐ': 'い', // ゐる -> いる
	'ゑ': 'え', // ゑる -> える
	'ふ': 'う', // ふむ -> うむ, ふつ -> うつ
	'ぢ': 'じ', // already-voiced where modern spelling has it
	'づ': 'ず',
}

// normalizeHistoricalKana rewrites pre-1946 kana and expands the iteration
// marks ゝ (repeats the preceding kana) and ゞ (repeats it with voicing).
func normalizeHistoricalKana(s string) (string, int) {
	rs := []rune(s)
	out := make([]rune, 0, len(rs)+8)
	changed := 0
	for _, r := range rs {
		switch r {
		case 'ゝ', 'ヽ':
			if n := len(out); n > 0 {
				out = append(out, out[n-1])
				changed++
				continue
			}
		case 'ゞ', 'ヾ':
			if n := len(out); n > 0 {
				out = append(out, voiced(out[n-1]))
				changed++
				continue
			}
		}
		if to, ok := historicalKana[r]; ok {
			out = append(out, to)
			changed++
			continue
		}
		out = append(out, r)
	}
	return string(out), changed
}

// voiced returns the dakuten form of a hiragana, or the kana itself when it
// cannot take one.
func voiced(r rune) rune {
	const (
		ka = "かきくけこ"
		sa = "さしすせそ"
		ta = "たちつてと"
		na = "なにぬねの"
		ha = "はひふへほ"
		ma = "まみむめも"
		ya = "やゆよ"
		ra = "らりるれろ"
		wa = "わゐうゑを"
	)
	table := []string{ka, sa, ta, na, ha, ma, ya, ra, wa}
	base := r - 'ぁ'
	for _, g := range table {
		for i, c := range []rune(g) {
			if c == r && i+1 < len([]rune(g)) {
				_ = base
				return r + 1
			}
		}
	}
	return r
}

// normalizeInput collapses the input harmlessly: full-width spaces, CR, tabs,
// the ideographic space, and pre-1946 kana orthography. Punctuation is left
// alone, because plan.md §7 requires the JLIR to keep source-specific
// material.
func normalizeInput(s *trace.Span, in string, externalMorph bool) string {
	var b strings.Builder
	b.Grow(len(in))
	changed := 0
	for _, r := range in {
		switch {
		case r == '　' || r == '\r' || r == '\t':
			b.WriteRune(' ')
			changed++
		case unicode.IsSpace(r) && r != '\n':
			b.WriteRune(' ')
			changed++
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	// Historical kana are rewritten only when no external analyser is going to
	// handle them. SudachiDict and the 国語研 old-kana UniDic builds carry ゐ and
	// the iteration marks as dictionary entries, so rewriting first would destroy
	// information the better analyser could have used. That is why the rewrite
	// is a builtin-backend fallback, not a preprocessing step.
	if out != "" && !externalMorph {
		if fixed, kana := normalizeHistoricalKana(out); kana > 0 {
			s.Note("rewrote %d pre-1946 kana character(s) to the modern spelling "+
				"(ゐ→い, ゑ→え, ふ→う, iteration marks expanded)", kana)
			changed += kana
			out = fixed
		}
	}
	if s != nil && changed > 0 {
		s.Note("normalized %d character(s) in total", changed)
	}
	return out
}

func describeReading(g *jlir.Graph) string {
	if g == nil {
		return ""
	}
	preds := make([]string, 0, len(g.Events))
	for _, v := range g.Events {
		preds = append(preds, v.Predicate)
	}
	sort.Strings(preds)
	if len(preds) == 0 {
		return "no propositional content"
	}
	return strings.Join(preds, ", ")
}

type projectionView struct {
	Target  string            `json:"target"`
	Style   plan.StyleProfile `json:"style"`
	Clauses int               `json:"clauses"`
	Notes   []string          `json:"notes,omitempty"`
}

func projectionViewOf(p *plan.Projection) projectionView {
	if p == nil {
		return projectionView{}
	}
	return projectionView{
		Target: string(p.Target), Style: p.Style,
		Clauses: len(p.Events), Notes: p.Notes,
	}
}

func constructionView(p *plan.Projection) map[string]string {
	out := map[string]string{}
	if p == nil {
		return out
	}
	for _, ev := range p.Events {
		out[string(ev.EventID)] = ev.Construction
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// stageMetrics fills in the pipeline's own account of the run.
//
// Every predicate, frame and construction is counted, not the best one: a
// sentence with three clauses where two resolve has not resolved its predicates.
// The fallback construction is excluded because it carries no verb, so counting
// it would report a construction for every sentence that got this far.
func stageMetrics(
	mf *forest.MorphForest,
	g *jlir.Graph,
	projection *plan.Projection,
	raw []forest.Candidate,
	accepted []Candidate,
) StageMetrics {
	var m StageMetrics

	if mf != nil {
		best := mf.Best()
		m.Tokens = len(best.Morphs)
		for _, mo := range best.Morphs {
			if !mo.Dict {
				m.OpaqueTokens++
			}
		}
		m.MorphologyComplete = m.OpaqueTokens == 0
	}

	m.PredicatesTotal = len(g.Events)
	for _, e := range g.Events {
		if e.Predicate != "" && !strings.HasPrefix(e.Predicate, "UNKNOWN") {
			m.PredicatesResolved++
		}
	}
	m.PredicatesComplete = m.PredicatesTotal > 0 && m.PredicatesResolved == m.PredicatesTotal

	m.FramesTotal = len(g.Events)
	for _, e := range g.Events {
		if frameFilled(g, e) {
			m.FramesResolved++
			continue
		}
		for _, why := range frameLosses(g, e) {
			m.FrameLosses = append(m.FrameLosses, why)
		}
	}
	m.FramesComplete = m.FramesTotal > 0 && m.FramesResolved == m.FramesTotal

	if projection != nil {
		for _, e := range projection.Events {
			m.ConstructionsTotal++
			if c := e.Construction; c != "" && !strings.Contains(c, "FALLBACK") {
				m.ConstructionsSelected++
			}
		}
	}
	m.ConstructionsComplete = m.ConstructionsTotal > 0 &&
		m.ConstructionsSelected == m.ConstructionsTotal

	m.RawCandidates = len(raw)
	m.AcceptedCandidates = len(accepted)
	m.Verified = len(accepted)
	m.OpenPositions = len(g.Unresolved())

	// Cumulative: the funnel only decreases.
	if !m.MorphologyComplete {
		m.PredicatesComplete, m.FramesComplete, m.ConstructionsComplete = false, false, false
	}
	if !m.PredicatesComplete {
		m.FramesComplete, m.ConstructionsComplete = false, false
	}
	if !m.FramesComplete {
		m.ConstructionsComplete = false
	}
	return m
}

// frameLosses names why an event is missing a role its frame requires.
//
// A frame drop is otherwise just a number, and the next fix would be a guess.
// Each cause is a stable slug so the corpus tool can group them and the most
// frequent one can be attacked first.
func frameLosses(g *jlir.Graph, e *jlir.Event) []string {
	if e == nil {
		return []string{"no event"}
	}
	pred := g.Predicate(e.Predicate)
	if pred == nil {
		return []string{"unknown_predicate:" + e.Predicate}
	}
	var out []string
	for _, spec := range pred.Args {
		if !spec.Required || e.HasRole(spec.Role) {
			continue
		}
		switch {
		case len(e.Args) == 0:
			// Nothing bound at all: the argument binder never saw a phrase.
			out = append(out, "no_arguments_bound")
		default:
			out = append(out, "missing_role:"+spec.Role)
		}
	}
	if len(out) == 0 && len(e.Args) == 0 {
		return []string{"no_arguments_bound"}
	}
	if len(out) == 0 {
		out = append(out, "unknown_predicate:"+e.Predicate)
	}
	sort.Strings(out)
	return dedupeStrings(out)
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// frameFilled reports whether an event carries every role its ontology frame
// declares mandatory. "the argument map is non-empty" is not a filled frame: a
// TRANSFER event with only a theme has lost both its agent and its recipient,
// and that is the case the verifier has to catch rather than pass.
func frameFilled(g *jlir.Graph, e *jlir.Event) bool {
	if e == nil || len(e.Args) == 0 {
		return false
	}
	pred := g.Predicate(e.Predicate)
	if pred == nil {
		return false
	}
	seen := map[string]bool{}
	for r := range e.Args {
		seen[r] = true
	}
	for _, spec := range pred.Args {
		if !spec.Required {
			continue
		}
		if !seen[spec.Role] {
			return false
		}
	}
	return true
}
