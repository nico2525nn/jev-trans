package pipeline_test

import (
	"strings"
	"testing"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/lex"
	"github.com/nico2525nn/jev-trans/internal/syntax"
)

// 「空が青くなった」 is 「い-adjective + なる」: the predicate is なる and the
// adjective supplies the comitative the CHANGE frame requires. The parser files
// the adjective as a clause modifier and nothing bound it, so every 〜くなる and
// 〜になる sentence was reported as a frame loss — which reads as the binder
// failing on a role nobody had asked it for.
func TestAdjectivalComitativeIsBound(t *testing.T) {
	for _, src := range []string{
		"空が青くなった。",
		"お昼は暑くなった。",
		"空は暗くなった。",
		"そらが少し明るくなった。",
	} {
		cs := clausesOf(t, src)
		found := false
		for _, c := range cs {
			if len(c.Modifiers) > 0 {
				found = true
			}
		}
		resp := translate(t, src, lang.JA, lang.EN, "full")
		if !found {
			t.Errorf("%q: the adjective was not bound as a comitative", src)
		}
		for _, l := range resp.Metrics.FrameLosses {
			if l == "missing_role:comitative" {
				t.Errorf("%q: frame still reports %s", src, l)
			}
		}
	}
}

// The く-form cannot modify a following noun, so an adjective ending in く that
// precedes a verb is unambiguously the predicate's comitative. Folding it into
// whatever noun phrase happened to be open loses it entirely.
func TestAdverbialAdjectiveBecomesAClauseModifier(t *testing.T) {
	for _, c := range clausesOf(t, "空が青くなった。") {
		if len(c.Modifiers) == 0 {
			t.Fatalf("青く must be filed as a clause modifier, got none")
		}
	}
}

// The な-form is premodifier, never a comitative. Only the く-form takes part
// in the 〜くなる construction, so 美しい must never be promoted to one —
// 「美しい庭がある」 has no なる to promote it against, and promoting it anyway
// would attach an attributive to a predicate it does not belong to.
func TestNominalAdjectiveIsNotAComitative(t *testing.T) {
	resp := translate(t, "美しい庭がある。", lang.JA, lang.EN, "full")
	for _, c := range resp.Artifacts.Syntax.Clauses {
		for _, n := range c.Notes {
			if strings.Contains(n, "→ comitative") {
				t.Fatalf("a premodifier was promoted to comitative: %s", n)
			}
		}
	}
	// 庭 is still the argument; the sentence must not have lost its subject.
	for _, l := range resp.Metrics.FrameLosses {
		if l == "no_arguments_bound" {
			t.Fatal("美しい庭 lost its argument")
		}
	}
}

// An adjective in a subordinate clause is that clause's predicate, not the
// matrix predicate's comitative. 「道が悪いので野原を歩く」 promoted 悪い across
// the ので boundary and the sentence stopped parsing.
func TestSubordinateAdjectiveIsNotPromoted(t *testing.T) {
	for _, c := range clausesOf(t, "道が悪いので野原を歩く。") {
		for _, n := range c.Notes {
			if strings.Contains(n, "→ comitative") {
				t.Fatalf("a subordinate adjective was promoted to comitative: %s", n)
			}
		}
	}
	// The subordinate clause's argument must survive: 野原を is the place the
	// walk happens in, and losing it produced "A road goes." — not a degraded
	// translation of the sentence but a different one, and it passed the gate
	// because the source graph had lost the phrase too.
	resp := translate(t, "道が悪いので野原を歩く。", lang.JA, lang.EN, "full")
	evs := resp.JLIR.Source.Events
	if len(evs) == 0 {
		t.Fatal("no event")
	}
	goal := false
	for _, role := range []string{jlir.RoleGoal, jlir.RoleLocation} {
		if arg, ok := evs[0].Args[role]; ok {
			goal = true
			if resp.JLIR.Source.Entity(arg.Value) == nil {
				t.Fatalf("the goal points at an entity that is not in the graph")
			}
		}
	}
	if !goal {
		t.Fatalf("the を-marked place of motion was dropped; args=%v", evs[0].Args)
	}
}

// clausesOf parses with the builtin analyser on purpose. The assertions here
// are about the parser, not about whichever backend happens to be installed,
// and the builtin path is the one that has to hold in an environment with no
// Sudachi at all.
func clausesOf(t *testing.T, src string) []*syntax.Clause {
	t.Helper()
	b := syntax.ParseJA(src, lex.AnalyzeJA(src))
	if b == nil {
		return nil
	}
	return b.Clauses
}

// A zero argument exists to fill a slot the frame cannot do without. COPULA.01
// requires a theme and merely accepts an experiencer, and the preference order
// lists experiencer first, so 「さうだ。」 put its zero anaphor in the optional
// slot and was then reported as missing the theme it had just been offered.
func TestZeroArgumentPrefersARequiredRole(t *testing.T) {
	resp := translate(t, "さうだ。", lang.JA, lang.EN, "full")
	for _, l := range resp.Metrics.FrameLosses {
		if strings.HasPrefix(l, "missing_role:") {
			t.Fatalf("the zero argument went into an optional slot: %s", l)
		}
	}
	evs := resp.JLIR.Source.Events
	if len(evs) == 0 {
		t.Fatal("no event")
	}
	if _, ok := evs[0].Args[jlir.RoleTheme]; !ok {
		t.Fatalf("the required role must be the one filled, got %v", evs[0].Args)
	}
}
