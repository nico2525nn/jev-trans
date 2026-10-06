package pipeline_test

import (
	"strings"
	"testing"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/pipeline"
	"github.com/nico/jev-trans/internal/plan"
	"github.com/nico/jev-trans/internal/semantics"
	"github.com/nico/jev-trans/internal/syntax"
	"github.com/nico/jev-trans/internal/verify"
)

// engine returns an engine with no oracle, which is the honest offline
// configuration: every decision falls back to a deterministic prior and says so.
// engine builds the engine the CLI builds.
//
// It used to pass only a default mode, which left the morphological registry
// empty and silently ran every test in this package on the builtin analyser
// while the binary under test was using Sudachi. The two agree on the two
// sentences that started the suite and disagree on tense, register and
// segmentation for much else, so a golden written through this helper was a
// golden for a configuration nobody ships.
//
// Registering Sudachi when it is usable and keeping the builtin otherwise
// mirrors cmd/jevtrans, so a test failure now means the product is wrong
// rather than that the test built a different system than the command line
// did.
func engine(t *testing.T) *pipeline.Engine {
	t.Helper()
	return engineWithMorph(t, true)
}

// builtinEngine is the same engine with the external analyser forced off, for
// the assertions that must hold in an environment where Sudachi is not
// installed at all.
func builtinEngine(t *testing.T) *pipeline.Engine {
	t.Helper()
	return engineWithMorph(t, false)
}

func engineWithMorph(t *testing.T, wantSudachi bool) *pipeline.Engine {
	t.Helper()
	reg := lex.NewRegistry()
	external := false
	if wantSudachi && sudachiAvailable() {
		reg.Register(lex.NewProcessAnalyzer(lex.SudachiConfig("core")))
		external = true
	}
	// MorphProfile is the CLI's default. Leaving it empty is not the same as
	// auto — the profile decides which dictionary the backend is asked for, and
	// an empty profile made the builtin analyser lose the past tense on 「来た」
	// while the command line kept it, which is how a golden written here came to
	// disagree with the product.
	return pipeline.NewEngine(pipeline.EngineConfig{
		DefaultMode:   "auto",
		Morph:         reg,
		ExternalMorph: external,
		MorphProfile:  lex.ProfileAuto,
	})
}

func translate(t *testing.T, text string, src, tgt lang.Lang, mode string) *pipeline.Response {
	t.Helper()
	resp, err := engine(t).Translate(t.Context(), pipeline.Request{
		Text:       text,
		SourceLang: src,
		TargetLang: tgt,
		DocumentID: t.Name(),
		Mode:       mode,
	})
	if err != nil {
		t.Fatalf("Translate(%q) returned %v", text, err)
	}
	return resp
}

func first(resp *pipeline.Response) string {
	if resp.Result.Selected == nil {
		return ""
	}
	return resp.Result.Selected.Text
}

// TestNoPlaceholderEverReachesTheOutput is the regression test for the defect
// this suite was written for: an unresolved predicate used to be realized as the
// English word "unknown", conjugated into "unknowns.", re-parsed as the same
// unknown predicate with an empty argument set, and scored EXACT with full
// confidence. A placeholder word in the output is always a bug.
func TestNoPlaceholderEverReachesTheOutput(t *testing.T) {
	for _, tc := range []struct {
		text string
		src  lang.Lang
		tgt  lang.Lang
	}{
		{"私はqualified。", lang.JA, lang.EN},
		{"彼は猫と犬は閉じこめられた。", lang.JA, lang.EN},
		{"行李はそこ였습니다。", lang.JA, lang.EN},
		{"非nings", lang.JA, lang.EN},
		{"😀", lang.JA, lang.EN},
	} {
		t.Run(tc.src.String()+"->"+tc.tgt.String()+":"+tc.text, func(t *testing.T) {
			resp := translate(t, tc.text, tc.src, tc.tgt, "auto")
			got := strings.ToLower(first(resp))
			for _, bad := range []string{"unknown", "unparsable", "unnamed", "todo"} {
				if strings.Contains(got, bad) {
					t.Fatalf("output contains the placeholder %q: %q (status %s)",
						bad, first(resp), resp.Result.Status)
				}
			}
		})
	}
}

// TestUnresolvedPredicateIsReportedNotGuessed: when a predicate cannot be
// resolved the system must say so rather than emit something.
func TestUnresolvedPredicateIsReportedNotGuessed(t *testing.T) {
	resp := translate(t, "私はqualified。", lang.JA, lang.EN, "auto")
	if first(resp) != "" {
		t.Fatalf("an unresolved predicate produced output %q; want none", first(resp))
	}
	if resp.Result.Status != pipeline.StatusUnparsable {
		t.Fatalf("status = %s, want %s", resp.Result.Status, pipeline.StatusUnparsable)
	}
	if len(resp.Result.Candidates) != 0 {
		t.Fatalf("unresolved predicate yielded %d candidates, want 0",
			len(resp.Result.Candidates))
	}
}

// TestEveryCircuitStageRuns pins plan.md §6: the trace is the evidence the UI
// renders, so a stage that stops running is invisible rather than absent.
func TestEveryCircuitStageRuns(t *testing.T) {
	resp := translate(t, "太郎が花子に本を渡した。", lang.JA, lang.EN, "auto")
	seen := map[string]bool{}
	for _, s := range resp.Summary.Stages {
		seen[string(s.Stage)] = true
	}
	for _, want := range []string{
		"INPUT_NORMALIZATION", "MORPHOLOGICAL_LATTICE", "PACKED_SYNTACTIC_FOREST",
		"SOURCE_SEMANTIC_FOREST", "JLIR_CORE", "JEV_DECISION_GRAPH",
		"CONSTRAINED_JLIR_STATE", "TARGET_PROJECTION_ENGINE",
		"TARGET_MESSAGE_PLANNER", "CONSTRUCTION_SELECTION",
		"PACKED_REALIZATION_FOREST", "GRAMMATICAL_REALIZER", "TARGET_REPARSER",
		"TARGET_JLIR", "SEMANTIC_EQUIVALENCE_VERIFIER", "JEV_RERANKER",
		"FINAL_OUTPUT",
	} {
		if !seen[want] {
			t.Errorf("stage %s did not run; trace has %v", want, keys(seen))
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestNoGenderIsInvented is the plan.md §4 and §22 guarantee stated as a test:
// nothing may acquire a gender the source did not supply, with or without an
// explicit overt pronoun.
func TestNoGenderIsInvented(t *testing.T) {
	for _, text := range []string{
		"太郎が花子に本を渡した。",
		"私は行きます。",
		"猫が犬を追った。",
		"本を読みました。",
		"BERT", "Taro gave a book to Hanako.",
	} {
		src := lang.DetectLang(text)
		tgt := src.Other()
		g := semantics.Analyze(text, src)
		for _, e := range g.Entities {
			f, ok := e.Feature("gender")
			if !ok {
				continue
			}
			v := jlir.ValueString(f.Value)
			if v == jlir.GenderUnknown || v == "" {
				continue
			}
			// A gender is only legitimate when the surface carries it.
			surface := e.Alias(g.Lang)
			carries := strings.Contains(surface, "彼") || strings.Contains(surface, "彼女") ||
				strings.Contains(strings.ToLower(surface), "he ") ||
				strings.Contains(strings.ToLower(surface), "she ")
			if !carries {
				t.Errorf("%q: entity %s has gender=%s but its surface %q does not",
					text, e.ID, v, surface)
			}
			_ = tgt
		}
	}
}

// TestMorphemeCoverageIsTotal: the analyzer must never drop characters. A gap
// silently changes the span arithmetic that provenance depends on.
func TestMorphemeCoverageIsTotal(t *testing.T) {
	for _, text := range []string{
		"田中さんはビールを飲みました。",
		"コーヒーとテーブルとカメラ",
		"私は行きます。嬉しそうでした。",
		"テストテストテスト",
		"ABC",
	} {
		mf := lex.AnalyzeJA(text)
		best := mf.Best()
		if len(best.Morphs) == 0 {
			t.Errorf("%q: no morphemes", text)
			continue
		}
		cursor := 0
		for _, m := range best.Morphs {
			if m.Start != cursor {
				t.Errorf("%q: gap or overlap at %q (expected offset %d, got %d)",
					text, m.Surface, cursor, m.Start)
			}
			cursor = m.End
		}
		if cursor != len(text) {
			t.Errorf("%q: morphemes cover %d bytes of %d", text, cursor, len(text))
		}
	}
}

// TestKatakanaLoanwordIsOneToken: the prolonged sound mark ー is part of the
// word, not punctuation. Treating it as a dictionary entry split ビール into
// ビ / ー / ル and left two of the three unresolved.
func TestKatakanaLoanwordIsOneToken(t *testing.T) {
	for _, word := range []string{"ビール", "コーヒー", "テーブル", "カメラ"} {
		mf := lex.AnalyzeJA(word)
		best := mf.Best()
		if len(best.Morphs) != 1 {
			names := make([]string, len(best.Morphs))
			for i, m := range best.Morphs {
				names[i] = m.Surface
			}
			t.Errorf("%q segmented as %v, want one morpheme", word, names)
		}
	}
}

// TestScopeAmbiguitySurvivesAnalysis is the plan.md §13 guarantee: NOT over ALL
// and ALL over NOT are both retained.
func TestScopeAmbiguitySurvivesAnalysis(t *testing.T) {
	g := semantics.Analyze("皆が帰らなかった。", lang.JA)
	if len(g.Scopes) == 0 {
		t.Fatal("no scope node for a negated quantified subject")
	}
	found := false
	for _, s := range g.Scopes {
		if len(s.Readings) >= 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("scope readings were collapsed: %+v", g.Scopes)
	}
}

// TestUnsupportedFeaturesAreDetectable wires the plan.md §22 check directly
// against the JLIR it is supposed to protect.
func TestUnsupportedFeaturesAreDetectable(t *testing.T) {
	g := semantics.Analyze("太郎が花子に本を渡した。", lang.JA)
	// The source must be clean.
	if u := g.UnsupportedFeatures(); len(u) != 0 {
		t.Errorf("clean source reported unsupported features: %+v", u)
	}
	// Now forge one and it must be caught.
	patched := false
	for _, e := range g.Entities {
		if e.Type == jlir.TypeHuman || e.Type == jlir.TypePerson || patched {
			continue
		}
		e.Features = append(e.Features, jlir.Feature{
			Key: "gender", Value: jlir.GenderFemale, Confidence: 1,
			Prov: []jlir.Provenance{{
				Origin: jlir.OriginRealization, Confidence: 1, Note: "fabricated",
			}},
		})
		patched = true
		break
	}
	if !patched {
		t.Skip("no human entity in this analysis to fabricate onto")
	}
	u := g.UnsupportedFeatures()
	if len(u) == 0 {
		t.Fatal("a target-side gender with no upstream provenance was not detected")
	}
	if u[0].Key != "gender" {
		t.Errorf("detected %q, want gender", u[0].Key)
	}
	if inv := g.InventedGender(); len(inv) == 0 {
		t.Error("InventedGender did not report the fabricated gender")
	}
}

// TestVerifyIsIdempotentOnIdenticalGraphs: identical graphs must produce zero
// loss and no diffs, which is the baseline every other verdict is measured
// against.
func TestVerifyIsIdempotentOnIdenticalGraphs(t *testing.T) {
	g := semantics.Analyze("太郎が花子に本を渡した。", lang.JA)
	res := verify.Verify(g, g.DeepCopy(), lang.JA, lang.JA)
	if res.Status != verify.StatusExact {
		t.Errorf("status = %s, want %s", res.Status, verify.StatusExact)
	}
	if res.Loss.Total() != 0 {
		t.Errorf("loss = %v, want zero", res.Loss)
	}
	if len(res.Diffs) != 0 {
		t.Errorf("diffs = %+v, want none", res.Diffs)
	}
}

// TestRealizeIsIndependentlyExercisable checks the plan.md §36 contract can be
// run directly on a projection, which is what makes the constraint solver
// testable at all.
func TestRealizeIsIndependentlyExercisable(t *testing.T) {
	g := semantics.Analyze("太郎が花子に本を渡した。", lang.JA)
	r := plan.Request{JLIR: g, Source: lang.JA, Target: lang.EN, Style: plan.StyleProfile{Register: plan.RegisterNeutral, Politeness: 0.5}}
	p := plan.Project(r)
	if p == nil || len(p.Events) == 0 {
		t.Fatal("projection produced no events")
	}
	real := plan.Realize(r, p)
	if real == nil {
		t.Fatal("Realize returned nil")
	}
	cands := real.Candidates(8)
	if len(cands) == 0 {
		t.Fatal("no candidates from a projectable graph")
	}
	for _, c := range cands {
		if strings.Contains(strings.ToLower(c.Text), "unknown") {
			t.Errorf("candidate contains a placeholder: %q", c.Text)
		}
	}
}

// TestRoundTripIsStable checks plan.md §63's premise at the level the system
// currently supports: translating back must produce a graph the verifier can
// compare against the original, and must not crash or fabricate.
func TestRoundTripIsStable(t *testing.T) {
	src := semantics.Analyze("私は行きます。", lang.JA)
	en := verify.Reparse("I go.", lang.EN, nil)
	if en == nil {
		t.Fatal("could not re-parse the round-trip target")
	}
	res := verify.Verify(src, en, lang.JA, lang.EN)
	if res.Status == "" {
		t.Error("verifier returned no status")
	}
	if len(res.Notes) == 0 && res.Status == verify.StatusExact {
		t.Error("an exact verdict with no notes suggests the comparison is vacuous")
	}
}

// TestNoPanicOnHostileInput is a robustness floor, not a quality claim.
func TestNoPanicOnHostileInput(t *testing.T) {
	inputs := []struct {
		text string
		src  lang.Lang
	}{
		{"", lang.JA}, {" ", lang.JA}, {"。", lang.JA}, {"あ", lang.JA},
		{"テストテストテストテスト", lang.JA}, {"ABC", lang.JA}, {"123", lang.JA},
		{"😀😀😀", lang.JA}, {"これは。テスト。です。", lang.JA},
		{strings.Repeat("あ", 500), lang.JA},
		{"", lang.EN}, {".", lang.EN}, {"!!! ???", lang.EN},
		{"The quick brown fox jumps over the lazy dog.", lang.EN},
		{strings.Repeat("the ", 400), lang.EN},
	}
	for _, in := range inputs {
		src, tgt := in.src, in.src.Other()
		t.Run(src.String()+":"+in.text, func(t *testing.T) {
			// Empty and whitespace-only input is rejected with an error rather
			// than translated; what must not happen is a panic.
			if strings.TrimSpace(in.text) == "" {
				if _, err := engine(t).Translate(t.Context(), pipeline.Request{
					Text: in.text, SourceLang: src, TargetLang: tgt,
					DocumentID: t.Name(), Mode: "auto",
				}); err == nil {
					t.Error("empty input was accepted")
				}
				return
			}
			translate(t, in.text, src, tgt, "auto")
		})
	}
}

// TestNoTraceStageWithoutArtifact: a stage that opens a span but attaches no
// artifact is exactly what the reviewer found in StagePlan and StageConstruct.
func TestNoTraceStageWithoutArtifact(t *testing.T) {
	resp := translate(t, "太郎が花子に本を渡した。", lang.JA, lang.EN, "auto")
	var walk func(interface{ GetNotes() []string })
	_ = walk
	byStage := map[string]int{}
	for _, s := range resp.Summary.Stages {
		byStage[string(s.Stage)] = s.Events
	}
	for _, want := range []string{"PACKED_REALIZATION_FOREST", "SEMANTIC_EQUIVALENCE_VERIFIER"} {
		if byStage[want] == 0 {
			t.Errorf("stage %s contributed no span", want)
		}
	}
}

// TestScopeRepresentationsAgree pins the fix for a defect the code review
// found: plan.md §13's NOT > ALL ambiguity was represented twice — as a clause
// alternative by the parser and as a scope node by the semantic layer — and the
// two asserted OPPOSITE weights for the same reading. Whichever representation
// a consumer happened to read decided the answer.
func TestScopeRepresentationsAgree(t *testing.T) {
	resp := translate(t, "皆が帰らなかった。", lang.JA, lang.EN, "auto")
	mf := lex.AnalyzeJA("皆が帰らなかった。")
	b := syntax.ParseJA("皆が帰らなかった。", mf)
	if b == nil || len(b.Clauses) == 0 {
		t.Fatal("no clause")
	}
	var clauseWeights []float64
	for _, c := range b.Clauses {
		clauseWeights = append(clauseWeights, c.Probability)
		for _, alt := range c.Alternatives {
			clauseWeights = append(clauseWeights, alt.Probability)
		}
	}
	g := semantics.Analyze("皆が帰らなかった。", lang.JA)
	for _, sc := range g.Scopes {
		if len(sc.Readings) < 2 {
			continue
		}
		for _, r := range sc.Readings {
			for _, cw := range clauseWeights {
				if cw == 0.5 {
					continue
				}
				if (cw > 0.5) != (r.Weight > 0.5) && cw != r.Weight {
					t.Errorf("parser leans %v while the scope node says %q weight %.2f",
						clauseWeights, r.Label, r.Weight)
				}
			}
		}
	}
	_ = resp
}

// Japanese drops the subject, and the parser plans an explicit zero phrase for
// it rather than leaving the field absent. An analyzer that tests for absence
// only therefore sees no omitted subject at all, and every such sentence ends
// with an event that has no arguments — which reads as an argument-binding bug
// three stages away from the disagreement that caused it.
//
// The assertion is about the zero anaphor existing as a first-class argument
// with an undecided referent, not about a specific antecedent: plan.md §17 is
// explicit that ambiguity should survive when nothing forces it away.
func TestZeroSubjectBecomesAnArgument(t *testing.T) {
	resp := translate(t, "本を読んだ。", lang.JA, lang.EN, "full")
	if len(resp.Metrics.PredicateGaps) != 0 {
		t.Fatalf("the predicate must resolve: %+v", resp.Metrics.PredicateGaps)
	}
	for _, l := range resp.Metrics.FrameLosses {
		if l == "no_arguments_bound" {
			t.Fatal("an omitted Japanese subject must not leave the event argument-less")
		}
	}
}
