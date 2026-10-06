package pipeline_test

import (
	"context"
	"sort"
	"testing"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/semantics"
	"github.com/nico/jev-trans/internal/syntax"
)

// TestJapaneseRoleBindingGolden pins the case-particle to semantic-role
// mapping on the sentences plan.md uses as its worked examples.
//
// This is the test the whole downstream stack rests on. When it failed,
// 「太郎が花子に本を渡した」 came out as "A books gives a book." with the agent
// and the recipient deleted, and the verifier passed it because the source
// graph had lost them too. An external analyser reports が only as 助詞-格助詞
// with no case feature, so a sentence analysed by Sudachi lost every argument
// the core could not see was a particle.
func TestJapaneseRoleBindingGolden(t *testing.T) {
	cases := []struct {
		src   string
		sense string
		roles map[string]string
	}{
		{
			src:   "太郎が花子に本を渡した。",
			sense: "TRANSFER.01",
			roles: map[string]string{"agent": "太郎", "recipient": "花子", "theme": "本"},
		},
		{
			src:   "太郎が本を読んだ。",
			sense: "PERCEIVE.01",
			roles: map[string]string{"experiencer": "太郎", "stimulus": "本"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			g := semantics.Analyze(tc.src, lang.JA)
			var found bool
			for _, e := range g.Events {
				if e.Predicate != tc.sense {
					continue
				}
				found = true
				for role, want := range tc.roles {
					arg, ok := e.Args[role]
					if !ok {
						t.Errorf("%s has no %s; args=%v", tc.sense, role, argNames(e))
						continue
					}
					ent := g.Entity(arg.Value)
					got := ""
					if ent != nil {
						got = ent.Alias(lang.JA)
					}
					if got != want {
						t.Errorf("%s/%s = %q, want %q", tc.sense, role, got, want)
					}
				}
			}
			if !found {
				t.Fatalf("no %s event in %v", tc.sense, predicates(g))
			}
		})
	}
}

// TestJapaneseRoleBindingSurvivesExternalAnalyser runs the same golden cases
// through Sudachi when it is available. The failure it guards against is
// backend-specific: the builtin analyser emits a `case` feature and Sudachi does
// not, so an argument-binding fix verified only against the builtin says nothing
// about the analyser that will actually run.
func TestJapaneseRoleBindingSurvivesExternalAnalyser(t *testing.T) {
	if !sudachiAvailable() {
		t.Skip("sudachi is not usable in this environment")
	}
	cases := []struct {
		src   string
		sense string
		roles map[string]string
	}{
		{
			src:   "太郎が花子に本を渡した。",
			sense: "TRANSFER.01",
			roles: map[string]string{"agent": "太郎", "recipient": "花子", "theme": "本"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			g := analyzeWithSudachi(t, tc.src)
			for _, e := range g.Events {
				if e.Predicate != tc.sense {
					continue
				}
				for role, want := range tc.roles {
					arg, ok := e.Args[role]
					if !ok {
						t.Errorf("%s lost %s under the external analyser; args=%v",
							tc.sense, role, argNames(e))
						continue
					}
					ent := g.Entity(arg.Value)
					if got := ent.Alias(lang.JA); got != want {
						t.Errorf("%s/%s = %q, want %q", tc.sense, role, got, want)
					}
				}
			}
		})
	}
}

func argNames(e *jlir.Event) []string {
	out := make([]string, 0, len(e.Args))
	for r := range e.Args {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func predicates(g *jlir.Graph) []string {
	out := make([]string, 0, len(g.Events))
	for _, e := range g.Events {
		out = append(out, e.Predicate)
	}
	return out
}

// sudachiAvailable reports whether the external analyser can answer here, so
// the golden tests skip rather than fail in an environment without it.
func sudachiAvailable() bool {
	cfg := lex.SudachiConfig("core")
	if !lex.Available(cfg.Command) {
		return false
	}
	p := lex.NewProcessAnalyzer(cfg)
	defer p.Close()
	_, err := p.Analyze(context.Background(), "今日はいい天気ですね。", lex.ProfileModern)
	return err == nil
}

// analyzeWithSudachi runs the full source analysis through the external
// backend, which is the path that regressed.
func analyzeWithSudachi(t *testing.T, src string) *jlir.Graph {
	t.Helper()
	reg := lex.NewRegistry()
	reg.Register(lex.NewProcessAnalyzer(lex.SudachiConfig("core")))
	an, err := reg.Analyze(context.Background(), src, lex.ProfileAuto, lang.JA)
	if err != nil {
		t.Fatalf("sudachi: %v", err)
	}
	if an.Backend != "sudachi" {
		t.Skipf("backend is %q, not sudachi", an.Backend)
	}
	bundle := syntax.ParseJA(src, an.Lattice(src))
	return semantics.Build(bundle, lang.JA)
}
