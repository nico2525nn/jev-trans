package verify

import (
	"strings"
	"testing"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
)

// These tests cover the repairs this slice made to the verifier's verdict
// layer: the §44 hard gate, the referent-identity precondition on the
// realization equivalence classes, the status vocabulary, and the §59 headline
// confidence.

func namedEntity(id jlir.ID, name string, l lang.Lang) *jlir.Entity {
	e := jlir.NewEntityValue(id, jlir.TypePerson)
	e.Proper = true
	e.Aliases = []jlir.Alias{{Surface: name, Lang: l, Kind: "name"}}
	return e
}

// oneEventGraph builds the smallest graph the verifier will compare: one event
// with one filled agent role bound to the given entity. Nothing else is set, so
// any diff the tests see comes from the binding under test.
func oneEventGraph(l lang.Lang, text string, e *jlir.Entity) *jlir.Graph {
	v := jlir.NewEvent("v1", "ARRIVE.01")
	v.Args = map[string]jlir.Arg{jlir.RoleAgent: {Value: e.ID, Confidence: 1}}
	return &jlir.Graph{Lang: l, Source: text, Entities: []*jlir.Entity{e}, Events: []*jlir.Event{v}}
}

func hasHardDiffFor(res *Result, dim string) bool {
	for _, d := range res.Diffs {
		if d.Severity == SeverityHard && d.Dimension == dim {
			return true
		}
	}
	return false
}

func diffMentions(res *Result, substr string) bool {
	for _, d := range res.Diffs {
		if strings.Contains(d.Detail, substr) {
			return true
		}
	}
	return false
}

// --- defect 1: the §44 hard gate -----------------------------------------

// TestSingleFailingCandidateIsRejected pins the regression that motivated the
// slice: the pipeline used to guard the ranking call on len(candidates) > 1, so
// a forest the hard constraints had pruned down to one derivation skipped the
// gate entirely and reached FINAL OUTPUT with whatever status it had. The gate
// must not depend on how many candidates there are.
func TestSingleFailingCandidateIsRejected(t *testing.T) {
	for _, status := range []string{StatusUnsupported, StatusDivergent, StatusUnparsable} {
		t.Run(status, func(t *testing.T) {
			out := RankOutcomeOf([]Candidate{{
				Text:   "only candidate",
				Verify: &Result{Status: status},
			}})

			if len(out.Ranked) != 0 {
				t.Fatalf("one %s candidate survived the hard gate: %q", status, out.Ranked[0].Text)
			}
			if len(out.Pruned) != 1 {
				t.Fatalf("pruned %d candidates, want 1", len(out.Pruned))
			}
			p := out.Pruned[0]
			if len(p.Rejected) == 0 {
				t.Fatal("rejection carries no named rule")
			}
			if p.Status != status {
				t.Errorf("pruned status = %s, want %s", p.Status, status)
			}
			if p.Status == StatusExact {
				t.Error("a failing candidate reported EXACT")
			}
			if len(out.Notes) == 0 {
				t.Error("rejecting the only candidate produced no explanation")
			}
		})
	}
}

// TestHardGateRejectsUnverifiedAndHandBuiltResults covers the two ways a
// candidate could otherwise slip past: no verification result at all, and a
// Result built outside Verify whose status was never set but which carries a
// hard diff.
func TestHardGateRejectsUnverifiedAndHandBuiltResults(t *testing.T) {
	if rejected, _ := hardRejected(Candidate{Text: "unchecked"}); !rejected {
		t.Error("an unverified candidate passed the hard gate")
	}
	if rejected, rules := hardRejected(Candidate{
		Text:   "hand-built",
		Verify: &Result{Diffs: []Diff{hard(DiffPolarity, "v1", "POSITIVE", "NEGATIVE", "polarity flipped")}},
	}); !rejected || len(rules) == 0 {
		t.Error("a hand-built Result with a hard diff passed the hard gate")
	}
	if rejected, _ := hardRejected(Candidate{
		Text:   "clean",
		Verify: &Result{Status: StatusGood},
	}); rejected {
		t.Error("a GOOD candidate with no hard diff was rejected")
	}
}

// --- defect 2: the realization classes must not waive identity -------------

// TestOvertSourceBoundToDifferentEntityIsHardReferentialDiff reproduces the
// waived check. The source binds its agent role to 花子; the target re-parse
// binds the same role to 太郎. Before the repair ClassExplicitToZero fired on
// the shape of the pair alone — EC1 applies to every role the moment the target
// language is Japanese — and the candidate was charged 0.06 stylistic loss.
func TestOvertSourceBoundToDifferentEntityIsHardReferentialDiff(t *testing.T) {
	src := oneEventGraph(lang.JA, "花子が来た。", namedEntity("s1", "花子", lang.JA))
	tgt := oneEventGraph(lang.JA, "太郎が来た。", namedEntity("t1", "太郎", lang.JA))

	res := Verify(src, tgt, lang.JA, lang.JA)

	if res.Status != StatusDivergent {
		t.Errorf("status = %s, want %s (diffs %+v)", res.Status, StatusDivergent, res.Diffs)
	}
	if !hasHardDiffFor(res, DiffEntity) {
		t.Errorf("no hard entity diff recorded; diffs = %+v", res.Diffs)
	}
	if res.Loss.Referential < lossReferentSwap {
		t.Errorf("referential loss = %.2f, want at least %.2f: a referent swap, not a style tweak",
			res.Loss.Referential, lossReferentSwap)
	}
	if res.Loss.Stylistic > 0 {
		t.Errorf("stylistic loss = %.2f; a swapped referent must not be charged as realization preference",
			res.Loss.Stylistic)
	}
	if diffMentions(res, string(ClassExplicitToZero)) {
		t.Error("EC1 licensed a realization change across a referent swap")
	}
}

// TestRealizationChangeWithPreservedReferentStillPasses is the other half of
// the same contract: a genuine zero realization whose referent survived must
// still be licensed by EC1, or the repair would turn every correct Japanese
// zero pronoun into a failure.
func TestRealizationChangeWithPreservedReferentStillPasses(t *testing.T) {
	src := oneEventGraph(lang.JA, "花子が∅を見た。", namedEntity("s1", "花子", lang.JA))

	// The target realizes the same referent as a zero whose referent decision
	// is committed to the target-side 花子 entity.
	zero := jlir.NewEntityValue("z1", jlir.TypePerson)
	zero.Zero = true
	zero.Aliases = []jlir.Alias{{Surface: "∅", Lang: lang.JA, Kind: "zero"}}
	zero.Referent = jlir.NewDistribution("jev:D1", map[string]float64{"t1": 1})
	zero.Referent.Commit()
	tgt := oneEventGraph(lang.JA, "花子が∅を見た。", zero)
	tgt.Entities = append(tgt.Entities, namedEntity("t1", "花子", lang.JA))

	res := Verify(src, tgt, lang.JA, lang.JA)

	if res.Status == StatusDivergent {
		t.Errorf("a preserved referent realized as a zero was reported divergent: %+v", res.Diffs)
	}
	if !diffMentions(res, string(ClassExplicitToZero)) {
		t.Errorf("EC1 did not fire for a licensed zero realization; diffs = %+v", res.Diffs)
	}
	if res.Loss.Stylistic <= 0 {
		t.Error("a realization change cost no stylistic loss")
	}
	if res.Loss.Referential != 0 {
		t.Errorf("referential loss = %.2f on a preserved referent", res.Loss.Referential)
	}
}

// TestRealizationClassesRequireReferentIdentity exercises the classes directly,
// so the precondition is pinned even if the verifier stops building Pairs the
// way it does today.
func TestRealizationClassesRequireReferentIdentity(t *testing.T) {
	base := Pair{
		Dimension:   DiffCoreference,
		SourceOvert: true,
		TargetZero:  true,
		TargetLang:  lang.JA,
		Role:        jlir.RolePatient,
	}

	unaligned := base
	unaligned.ReferentUnique = true
	if ok, _ := Equivalent(unaligned); ok {
		t.Error("EC1 fired with no evidence the referent survived")
	}

	aligned := base
	aligned.ReferentUnique = true
	aligned.ReferentAligned = true
	if ok, id := Equivalent(aligned); !ok || id != ClassExplicitToZero {
		t.Errorf("EC1 did not fire for an aligned, uniquely bound zero: ok=%v id=%q", ok, id)
	}

	// EC2 already demanded a unique referent; it must also demand alignment.
	ec2 := Pair{Dimension: DiffCoreference, SourceZero: true, TargetOvert: true,
		ReferentUnique: true, TargetLang: lang.JA, Role: jlir.RoleAgent}
	if ok, _ := Equivalent(ec2); ok {
		t.Error("EC2 fired without evidence the referent survived")
	}
	ec2.ReferentAligned = true
	if ok, id := Equivalent(ec2); !ok || id != ClassZeroToExplicit {
		t.Errorf("EC2 did not fire for an aligned explicit realization: ok=%v id=%q", ok, id)
	}
}

// --- defect 3: isZeroEntity must mean "unrealized" -------------------------

func TestZeroEntityTestIgnoresReferentDistribution(t *testing.T) {
	e := namedEntity("e1", "花子", lang.JA)
	if isZeroEntity(e) {
		t.Fatal("an entity with an overt alias reported as a zero entity")
	}
	// The Referent distribution used to be enough on its own, which would have
	// reported this entity as SourceZero and waived referent checking for it.
	e.Referent = jlir.NewDistribution("prior:recency", map[string]float64{"e2": 1})
	if isZeroEntity(e) {
		t.Error("a referent distribution alone made an overt entity report as zero")
	}
	if !entityOvert(e, lang.JA) {
		t.Error("an entity with an overt alias is not overt")
	}

	z := jlir.NewEntityValue("z1", jlir.TypePerson)
	z.Zero = true
	if !isZeroEntity(z) {
		t.Error("the Zero flag is not honoured")
	}

	aliasZero := jlir.NewEntityValue("z2", jlir.TypePerson)
	aliasZero.Aliases = []jlir.Alias{{Surface: "∅", Lang: lang.JA, Kind: "zero"}}
	if !isZeroEntity(aliasZero) {
		t.Error("a zero-kind alias is not honoured")
	}
}

// --- defect 4: LOSSY vs the hard gate -------------------------------------

// TestLossyAndDivergentAreDistinguishable pins the split. Respect the source
// did not carry is a graded quality (LOSSY); a changed polarity is a failed gate
// (DIVERGENT). A caller must be able to tell them apart, and hardRejected must
// consume the latter.
func TestLossyAndDivergentAreDistinguishable(t *testing.T) {
	src := oneEventGraph(lang.JA, "花子が来た。", namedEntity("s1", "花子", lang.JA))
	src.Events[0].Polarity = jlir.PolarityPositive

	// Soft only: the target adds respect the source does not carry.
	tgtSoft := oneEventGraph(lang.JA, "花子が来ました。", namedEntity("t1", "花子", lang.JA))
	tgtSoft.Events[0].Polarity = jlir.PolarityPositive
	tgtSoft.Prag.Respect = 0.9
	res := Verify(src, tgtSoft, lang.JA, lang.JA)
	if res.Status != StatusLossy {
		t.Errorf("soft-only divergence reported %s, want %s (diffs %+v)", res.Status, StatusLossy, res.Diffs)
	}
	if rejected, _ := hardRejected(Candidate{Verify: res}); rejected {
		t.Error("a LOSSY candidate was rejected by the hard gate")
	}

	// Hard: the polarity changed.
	tgtHard := oneEventGraph(lang.JA, "花子が来なかった。", namedEntity("t1", "花子", lang.JA))
	tgtHard.Events[0].Polarity = jlir.PolarityNegative
	res = Verify(src, tgtHard, lang.JA, lang.JA)
	if res.Status != StatusDivergent {
		t.Errorf("a changed polarity reported %s, want %s (diffs %+v)", res.Status, StatusDivergent, res.Diffs)
	}
	rejected, rules := hardRejected(Candidate{Verify: res})
	if !rejected {
		t.Fatal("hardRejected accepted a DIVERGENT result")
	}
	if len(rules) == 0 {
		t.Fatal("hardRejected rejected without naming a rule")
	}
	if !strings.Contains(strings.Join(rules, "; "), "polarity") {
		t.Errorf("rejection rules do not name the polarity rule: %v", rules)
	}
	if len(res.Rejections) == 0 {
		t.Error("the verifier recorded no rejection reasons for a divergent candidate")
	}
}

// TestGoodIsNotBoughtWithARealizationPreference records why the GOOD/LOSSY
// boundary is componentwise rather than a weighted threshold. Only stylistic
// loss may leave a candidate GOOD (§42); anything else is a real loss, so the
// candidate is LOSSY however small it is.
func TestGoodIsNotBoughtWithARealizationPreference(t *testing.T) {
	if got := (LossVector{}).MaxOutside(DimStylistic); got != 0 {
		t.Errorf("empty vector MaxOutside(stylistic) = %v, want 0", got)
	}
	onlyStyle := LossVector{}.With(DimStylistic, lossRoleRenamed)
	if got := onlyStyle.MaxOutside(DimStylistic); got != 0 {
		t.Errorf("stylistic-only loss MaxOutside = %v, want 0", got)
	}
	if got := onlyStyle.Total(); got == 0 {
		t.Error("stylistic-only loss summarised as zero total")
	}
	for _, dim := range []string{DimPropositional, DimReferential, DimTemporal, DimPragmatic, DimImplicature} {
		v := LossVector{}.With(dim, 0.01)
		if got := v.MaxOutside(DimStylistic); got <= 0 {
			t.Errorf("loss in %s did not register outside stylistic", dim)
		}
	}
}

// --- defect 5: the §59 headline confidence -------------------------------

func TestOverallConfidenceIsDerivedFromTheBreakdown(t *testing.T) {
	src := oneEventGraph(lang.JA, "花子が来た。", namedEntity("s1", "花子", lang.JA))
	src.Prag.Respect = 0.5
	tgt := src.DeepCopy()
	tgt.Prag.Respect = 0.1

	res := Verify(src, tgt, lang.JA, lang.JA)

	got, ok := res.Confidence["overall"]
	if !ok {
		t.Fatalf("no %q key in the confidence breakdown: %+v", "overall", res.Confidence)
	}
	if want := OverallConfidence(res.Confidence); got != want {
		t.Errorf("overall = %.3f, want %.3f recomputed from the map", got, want)
	}
	if again := OverallConfidence(res.Confidence); again != got {
		t.Errorf("overall is not stable: %.3f then %.3f", got, again)
	}
	if got < 0 || got > 1 {
		t.Errorf("overall = %v, outside [0,1]", got)
	}

	// The headline is the mean of the feature keys it summarises, so it lands
	// between the weakest and the strongest of them — never outside.
	lo, hi := 1.0, 0.0
	for k, v := range res.Confidence {
		if k == "overall" || strings.HasPrefix(k, "role:") {
			continue
		}
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if got < lo-1e-9 || got > hi+1e-9 {
		t.Errorf("overall %.3f outside the feature range [%.3f, %.3f]", got, lo, hi)
	}
	// A collapsed feature has to move the headline: the weakness §59 exists to
	// expose cannot be averaged away into a confident-looking number.
	if got > 0.999 {
		t.Errorf("overall %.3f stayed confident despite a register of %.3f", got, res.Confidence[DiffRegister])
	}

	// Per-role keys are folded into "subject" rather than counted again, so a
	// map of nothing but role keys has no headline to average.
	if OverallConfidence(map[string]float64{"role:agent": 0.1}) != 0 {
		t.Error("a role key alone leaked into the headline")
	}
}

// --- defect 6: componentwise ordering ------------------------------------

// TestCompareIsComponentwise pins §43's vector requirement. Under the §45 tier
// weights these two vectors summarise to the same number; the componentwise
// comparison must still prefer the one that kept the propositional content.
func TestCompareIsComponentwise(t *testing.T) {
	propHeavy := LossVector{Propositional: 0.4}
	pragHeavy := LossVector{Pragmatic: 1.0}
	if propHeavy.Total() != pragHeavy.Total() {
		t.Skipf("tier weights changed: %.3f vs %.3f", propHeavy.Total(), pragHeavy.Total())
	}
	if c := pragHeavy.Compare(propHeavy); c != -1 {
		t.Errorf("Compare(pragmatic-only, propositional) = %d, want -1", c)
	}
	if c := propHeavy.Compare(pragHeavy); c != 1 {
		t.Errorf("Compare(propositional, pragmatic-only) = %d, want 1", c)
	}
	if c := propHeavy.Compare(propHeavy); c != 0 {
		t.Errorf("Compare(self) = %d, want 0", c)
	}

	// Ordering actually uses Compare, not Total: equal totals, equal
	// naturalness, and the propositional-clean candidate comes first.
	a := Candidate{Text: "a", Verify: &Result{Loss: pragHeavy}, Naturalness: 0.5}
	b := Candidate{Text: "b", Verify: &Result{Loss: propHeavy}, Naturalness: 0.5}
	out := RankOutcomeOf([]Candidate{a, b})
	if len(out.Ranked) != 2 {
		t.Fatalf("both candidates should survive dominance, got %d", len(out.Ranked))
	}
	if out.Ranked[0].Text != "a" {
		t.Errorf("ranked %q first; §43 orders the vector, not the weighted sum", out.Ranked[0].Text)
	}
}

func TestParetoPruningIsKept(t *testing.T) {
	// One candidate dominates the other on every dimension and on naturalness.
	weak := Candidate{Text: "weak", Verify: &Result{Loss: LossVector{Pragmatic: 0.4}},
		Naturalness: 0.2}
	strong := Candidate{Text: "strong", Verify: &Result{Loss: LossVector{}}, Naturalness: 0.9}
	out := RankOutcomeOf([]Candidate{weak, strong})
	if len(out.Ranked) != 1 || out.Ranked[0].Text != "strong" {
		t.Fatalf("dominance pruning removed the wrong candidate: %+v", out.Ranked)
	}
	if len(out.Drops) != 1 {
		t.Errorf("dropped %d candidates, want 1 dominance edge", len(out.Drops))
	}
}

// --- defect 8: the survivors are actually reachable ----------------------

func TestReparseSurvivesUnparsableInput(t *testing.T) {
	// Reparse owns the analyze-and-compose path and must never take the
	// translation down: every one of these is either a graph or nil, never a
	// panic and never a guessed graph.
	for _, in := range []struct {
		text string
		l    lang.Lang
	}{
		{"", lang.JA},
		{"   ", lang.EN},
		{"\U0001F600", lang.JA},
		{"", ""},
	} {
		if g := Reparse(in.text, in.l, nil); g != nil && len(g.Events) == 0 && len(g.Entities) == 0 {
			t.Errorf("Reparse(%q, %q) returned an empty graph rather than nil", in.text, in.l)
		}
	}
}

func TestReparseBuildsAComparableGraph(t *testing.T) {
	g := Reparse("Taro gave a book to Hanako.", lang.EN, nil)
	if g == nil {
		t.Fatal("could not re-parse a well-formed English target")
	}
	if len(g.Events) == 0 {
		t.Fatal("re-parse produced no event; there is nothing to verify against")
	}
	if g.Lang != lang.EN || g.Source == "" {
		t.Errorf("re-parse lost its provenance: lang=%q source=%q", g.Lang, g.Source)
	}
}
