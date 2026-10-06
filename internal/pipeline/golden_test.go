package pipeline_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/pipeline"
	"github.com/nico/jev-trans/internal/plan"
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
			// WORK.04 is READ. The table used to put PERCEIVE.01 first, so
			// 「本を読んだ」 came out as "saw a book" even though the READ
			// construction existed and was unreachable.
			src:   "太郎が本を読んだ。",
			sense: "WORK.04",
			roles: map[string]string{"agent": "太郎", "theme": "本"},
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

// TestNoUnsupportedSentenceFinalParticle pins a pragmatic invention.
//
// 「Taro gave a book to Hanako.」 came out as 「太郎は本を花子に渡しましたね。」
// The ね has no source: politeness and sentence-final stance are different
// things, and treating politeness >= 0.5 as licence to add ね inserts an
// attitude the speaker never expressed. Nothing in the response said so either.
func TestNoUnsupportedSentenceFinalParticle(t *testing.T) {
	// The politeness the CLI defaults to, because that is where the particle
	// appeared. A zero-valued profile does not reproduce it.
	for _, politeness := range []float64{0.3, 0.5, 0.7} {
		resp, err := engine(t).Translate(t.Context(), pipeline.Request{
			Text: "Taro gave a book to Hanako.", SourceLang: lang.EN, TargetLang: lang.JA,
			DocumentID: t.Name(), Mode: "auto",
			Style: plan.StyleProfile{Register: plan.RegisterNeutral, Politeness: politeness},
		})
		if err != nil {
			t.Fatalf("Translate: %v", err)
		}
		if resp.Result.Selected == nil {
			t.Fatalf("politeness %.1f: no candidate selected", politeness)
		}
		got := resp.Result.Selected.Text
		src := sourceSentenceFinals(resp)
		for _, p := range []string{"ね", "よ", "か", "な", "ぞ", "ぜ", "わ", "さ"} {
			if strings.Contains(got, p) && !src[p] {
				t.Errorf("politeness %.1f: target %q adds the sentence-final particle %q, "+
					"which the source does not have and no explicit style asked for",
					politeness, got, p)
			}
		}
	}
}

// sourceSentenceFinals collects the particles the source sentence actually
// ends its clauses with, from the analysis the pipeline recorded.
func sourceSentenceFinals(resp *pipeline.Response) map[string]bool {
	out := map[string]bool{}
	g := resp.JLIR.Source
	if g == nil {
		return out
	}
	for _, p := range g.SourceFeat.SentenceFinalParticles {
		out[p] = true
	}
	for _, p := range g.Prag.SentenceFinalParticles {
		out[p] = true
	}
	return out
}

// TestEnglishTenseAgreementNotFlag pins the resolution of read/set/put, whose
// present and past are spelled identically.
//
// A per-token "this is ambiguous" flag cannot express the difference: "Taro read a
// book" is settled, because a third-person singular subject takes -s in the
// present and would have to read "reads". "I read a book" is not, because I read
// is both. The flag treated them alike and made the first one unverifiable.
func TestEnglishTenseAgreementNotFlag(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string // "" means the reading must stay open
	}{
		// Singular third person: the present would take -s, so it is past.
		{"Taro read a book.", jlir.TensePast},
		{"She read a book.", jlir.TensePast},
		// Plural and first/second person take no -s, so the present and the
		// past are the same string and nothing settles it.
		{"They read a book.", ""},
		{"I read a book.", ""},
		{"We read a book.", ""},
	} {
		t.Run(tc.src, func(t *testing.T) {
			g := analyzeEnglish(t, tc.src)
			if len(g.Events) == 0 {
				t.Fatalf("no event for %q", tc.src)
			}
			ev := g.Events[0]
			if tc.want == "" {
				// The reading stays open, and says so with the readings rather
				// than with a guess.
				var has bool
				for _, f := range ev.Features {
					if f.Key == "tense_readings" {
						has = true
					}
				}
				if !has {
					t.Errorf("%q: tense left empty without recording the open readings; "+
						"an unknown tense must be visible, not silently dropped (features %v)",
						tc.src, ev.Features)
				}
				return
			}
			if ev.Tense != tc.want {
				t.Errorf("%q: tense = %q, want %q", tc.src, ev.Tense, tc.want)
			}
		})
	}
}

func analyzeEnglish(t *testing.T, src string) *jlir.Graph {
	t.Helper()
	b := syntax.ParseEN(src, lex.AnalyzeEN(src))
	return semantics.Build(b, lang.EN)
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
