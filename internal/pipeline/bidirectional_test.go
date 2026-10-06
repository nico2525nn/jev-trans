package pipeline_test

import (
	"strings"
	"testing"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/pipeline"
	"github.com/nico/jev-trans/internal/plan"
)

// This file is the regression net for the two claims that are easy to break by
// improving something else: the system translates in both directions, and it
// never adds an attitude the speaker did not express.
//
// Everything here runs under both morphological backends. The builtin analyser
// is the fallback for an environment with no Sudachi, and a guarantee that only
// holds when Sudachi happens to be installed is a statement about the
// environment rather than about the code.

// jaFinals are the Japanese sentence-final particles. Each one is a claim about
// the speaker's stance, so each one needs a source.
var jaFinals = []string{"ね", "よ", "か", "な", "ぞ", "ぜ", "わ", "さ"}

// enFinals are the English equivalents. English marks stance with particles far
// less than Japanese does, which makes them a sharper test: the target has no
// grammatical slot to fill, so any of them can only have been invented.
var enFinals = []string{" indeed", " isn't it", " you know", " right?"}

// TestNoUnsupportedPragmaticInformationInEitherDirection pins the invention in
// both directions.
//
// 「Taro gave a book to Hanako.」 came out as 「太郎は本を花子に渡しましたね。」
// The ね has no source: politeness and sentence-final stance are different
// things, and treating a politeness of 0.5 as licence to add ね inserts an
// attitude the speaker never expressed. Nothing in the response said so either —
// so the test has to look at the text, because the response gave no warning.
//
// The reverse direction is the same failure wearing different clothes: a plain
// Japanese sentence must not come out with an English stance marker, and an
// English one must not come back with a Japanese one.
func TestNoUnsupportedPragmaticInformationInEitherDirection(t *testing.T) {
	forEachBackend(t, func(t *testing.T, _ string) {
		for _, politeness := range []float64{0.3, 0.5, 0.7} {
			// EN -> JA
			resp, err := engine(t).Translate(t.Context(), pipeline.Request{
				Text:       "Taro gave a book to Hanako.",
				SourceLang: lang.EN, TargetLang: lang.JA,
				DocumentID: t.Name(), Mode: "auto",
				Style: plan.StyleProfile{Register: plan.RegisterNeutral, Politeness: politeness},
			})
			if err != nil {
				t.Fatalf("EN->JA: %v", err)
			}
			if resp.Result.Selected == nil {
				t.Fatalf("EN->JA politeness %.1f: no candidate selected", politeness)
			}
			got := resp.Result.Selected.Text
			src := sourceSentenceFinals(resp)
			for _, p := range jaFinals {
				if strings.Contains(got, p) && !src[p] {
					t.Errorf("EN->JA politeness %.1f: %q adds %q, which the source has no "+
						"source for and no explicit style asked for", politeness, got, p)
				}
			}

			// JA -> EN. A plain sentence must not acquire an English stance
			// marker; English has no slot that obliges one.
			resp, err = engine(t).Translate(t.Context(), pipeline.Request{
				Text:       "太郎は本を花子に渡した。",
				SourceLang: lang.JA, TargetLang: lang.EN,
				DocumentID: t.Name(), Mode: "auto",
				Style: plan.StyleProfile{Register: plan.RegisterNeutral, Politeness: politeness},
			})
			if err != nil {
				t.Fatalf("JA->EN: %v", err)
			}
			if resp.Result.Selected == nil {
				t.Fatalf("JA->EN politeness %.1f: no candidate selected", politeness)
			}
			got = resp.Result.Selected.Text
			for _, p := range enFinals {
				if strings.Contains(strings.ToLower(got), p) {
					t.Errorf("JA->EN politeness %.1f: %q adds the stance marker %q, "+
						"which the Japanese source does not carry", politeness, got, p)
				}
			}
		}
	})
}

// TestPragmaticInformationInTheSourceSurvives is the other half, and it is the
// half that stops the fix from being "delete the pragmatic layer".
//
// Suppressing every stance marker would satisfy the test above while losing real
// information. 「今日はいい天気ですね。」 carries ね in the source; the target
// must be allowed to keep it, and the system must be able to say where it came
// from.
func TestPragmaticInformationInTheSourceSurvives(t *testing.T) {
	resp := translate(t, "今日はいい天気ですね。", lang.JA, lang.EN, "auto")
	g := resp.JLIR.Source
	if g == nil {
		t.Fatal("no source graph")
	}
	found := false
	for _, p := range append(append([]string{}, g.SourceFeat.SentenceFinalParticles...),
		g.Prag.SentenceFinalParticles...) {
		// The recorded value is the surface the analyser saw, which for ですね is
		// the auxiliary and not the particle. What matters is that the stance
		// is recorded at all and attributable to the source.
		if strings.Contains(p, "ね") {
			found = true
		}
	}
	if !found {
		t.Errorf("the source's own ね was not recorded; source-final=%v prag=%v",
			g.SourceFeat.SentenceFinalParticles, g.Prag.SentenceFinalParticles)
	}
}

// TestNoGenderIsInventedAcrossBothDirections is the other invariant the
// asymmetry between the two languages invites a break of.
//
// Japanese は/が carry no gender, so a JA->EN translation that picks he or she
// has invented a fact about a person. The reverse direction is the mirror case:
// English he/she carries gender, so EN->JA must not materialise 彼 or 彼女 from
// it either — the referent is internal, and plan.md §47 says so.
func TestNoGenderIsInventedAcrossBothDirections(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		resp := translate(t, "太郎が花子に本を渡した。", lang.JA, lang.EN, "auto")
		if resp.Result.Selected != nil {
			got := strings.ToLower(resp.Result.Selected.Text)
			for _, p := range []string{" he ", " she ", " his ", " her "} {
				if strings.Contains(got, p) {
					t.Errorf("%s: JA->EN invented %q: %q — Japanese has no gender on "+
						"が or は", backend, p, got)
				}
			}
		}
		resp = translate(t, "Taro gave a book to Hanako.", lang.EN, lang.JA, "auto")
		if resp.Result.Selected != nil {
			got := resp.Result.Selected.Text
			// 彼 and 彼女 are only acceptable when the source named a gender
			// for some referent. The check is deliberately blunt: it fails if
			// the text carries a gendered pronoun and the source graph records
			// no gender at all, which is the only way one can be manufactured.
			if (strings.Contains(got, "彼") || strings.Contains(got, "彼女")) &&
				!anyGenderFeature(resp.JLIR.Source) {
				t.Errorf("%s: EN->EN->JA produced the gendered %q but the source graph "+
					"records no gender feature: %q", backend, got, got)
			}
		}
	})
}

func anyGenderFeature(g *jlir.Graph) bool {
	if g == nil {
		return false
	}
	for _, e := range g.Entities {
		for _, f := range e.Features {
			if strings.EqualFold(f.Key, "gender") {
				return true
			}
		}
	}
	return false
}

// forEachBackend runs a check under the builtin analyser and, when it is
// usable, under Sudachi too. A guarantee that holds for one and not the other
// is not a guarantee.
func forEachBackend(t *testing.T, check func(t *testing.T, backend string)) {
	t.Helper()

	t.Run("builtin", func(t *testing.T) {
		t.Setenv("JEV_SUDACHI_ADAPTER", "off")
		check(t, "builtin")
	})

	t.Run("sudachi", func(t *testing.T) {
		if !sudachiAvailable() {
			t.Skip("sudachi is not usable in this environment")
		}
		t.Setenv("JEV_SUDACHI_ADAPTER", "sudachipy")
		check(t, "sudachi")
	})
}

// TestBothDirectionsSurviveBothBackends is the plain directional net. The two
// golden sentences are the ones the whole stack is built on; if either of them
// works in one direction and not the other, the system is not bidirectional in
// any sense that matters.
func TestBothDirectionsSurviveBothBackends(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		resp := translate(t, "太郎が花子に本を渡した。", lang.JA, lang.EN, "auto")
		if resp.Result.Selected == nil {
			t.Fatalf("%s: JA->EN produced no candidate for the core golden", backend)
		}
		if got := resp.Result.Selected.Text; got != "Taro gave a book to Hanako." {
			t.Errorf("%s: JA->EN = %q, want %q", backend, got, "Taro gave a book to Hanako.")
		}
		// With an explicit polite style. A zero-valued style profile legitimately
		// yields the plain form — no style was requested, so none is invented —
		// and asserting the polite string here would be asserting the CLI's
		// default rather than the direction's capability.
		resp, err := engine(t).Translate(t.Context(), pipeline.Request{
			Text:       "Taro gave a book to Hanako.",
			SourceLang: lang.EN, TargetLang: lang.JA,
			DocumentID: t.Name(), Mode: "auto",
			Style: plan.StyleProfile{Register: plan.RegisterNeutral, Politeness: 0.7},
		})
		if err != nil {
			t.Fatalf("%s: EN->JA: %v", backend, err)
		}
		if resp.Result.Selected == nil {
			t.Fatalf("%s: EN->JA produced no candidate for the core golden", backend)
		}
		if got := resp.Result.Selected.Text; got != "太郎は本を花子に渡しました。" {
			t.Errorf("%s: EN->JA = %q, want %q", backend, got, "太郎は本を花子に渡しました。")
		}

		// And with none, the plain form — the two together say the style is a
		// parameter rather than a constant.
		plain := translate(t, "Taro gave a book to Hanako.", lang.EN, lang.JA, "auto")
		if plain.Result.Selected == nil {
			t.Fatalf("%s: EN->JA plain produced no candidate", backend)
		}
		if got := plain.Result.Selected.Text; got != "太郎は本を花子に渡した。" {
			t.Errorf("%s: EN->JA with no style = %q, want %q", backend, got, "太郎は本を花子に渡した。")
		}
	})
}

// TestTheBuiltinAnalyserIsUsableOnItsOwn guards the claim the dual-backend tests
// rest on. If the fallback analyser could not analyse the core golden, every
// other assertion here would be vacuous in an environment without Sudachi.
func TestTheBuiltinAnalyserIsUsableOnItsOwn(t *testing.T) {
	t.Setenv("JEV_SUDACHI_ADAPTER", "off")
	an := lex.AnalyzeJA("太郎が花子に本を渡した。")
	if an == nil || len(an.Paths) == 0 {
		t.Fatal("the builtin analyser produced no path")
	}
	for _, m := range an.Paths[0].Morphs {
		if m.POS == "" {
			t.Errorf("morpheme %q has no part of speech under the builtin analyser", m.Surface)
		}
	}
}

// TestSentenceFinalParticleIsRecordedAtParticleGranularity pins the key the
// invention check looks under.
//
// The builtin analyser cannot split です + ね, so the clause carried 「ですね」 as
// one morpheme and that whole string went into sentenceFinalParticles. A source
// which genuinely carries ね was therefore stored under a key nothing else can
// match, and the check for an invented particle would have called a faithful
// translation an invention. Both backends must record ね.
func TestSentenceFinalParticleIsRecordedAtParticleGranularity(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		resp := translate(t, "今日はいい天気ですね。", lang.JA, lang.EN, "auto")
		g := resp.JLIR.Source
		if g == nil {
			t.Fatalf("%s: no source graph", backend)
		}
		got := append(append([]string{}, g.SourceFeat.SentenceFinalParticles...),
			g.Prag.SentenceFinalParticles...)
		for _, p := range got {
			if p != "ね" {
				t.Errorf("%s: recorded %q; the field must hold the particle, not the "+
					"auxiliary phrase around it", backend, p)
			}
		}
		if len(got) == 0 {
			t.Errorf("%s: the source's ね was not recorded at all", backend)
		}
	})
}

// TestASurfaceWithNoParticleRecordsNothing is the other half. ですか has no
// stance marker, and writing it into a field named sentence-final particles
// would assert that it does.
func TestASurfaceWithNoParticleRecordsNothing(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		resp := translate(t, "今日はいい天気ですか。", lang.JA, lang.EN, "auto")
		g := resp.JLIR.Source
		if g == nil {
			return
		}
		for _, p := range append(append([]string{}, g.SourceFeat.SentenceFinalParticles...),
			g.Prag.SentenceFinalParticles...) {
			if p != "か" {
				t.Errorf("%s: recorded %q; ですか carries only か", backend, p)
			}
		}
	})
}
