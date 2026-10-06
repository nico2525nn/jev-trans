package pipeline_test

import (
	"strings"
	"testing"

	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/syntax"
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
	// The sentence must still produce a candidate; that is what regressed.
	resp := translate(t, "道が悪いので野原を歩く。", lang.JA, lang.EN, "full")
	if resp.Metrics.EligibleCandidates == 0 {
		t.Fatal("the subordinate-clause sentence stopped producing a candidate")
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
