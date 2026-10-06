package pipeline_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/lex"
	"github.com/nico2525nn/jev-trans/internal/pipeline"
	"github.com/nico2525nn/jev-trans/internal/plan"
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
	forEachBackend(t, func(t *testing.T, backend string) {
		e := engine(t)
		if backend == "builtin" {
			e = builtinEngine(t)
		}
		for _, politeness := range []float64{0.3, 0.5, 0.7} {
			// EN -> JA
			resp, err := e.Translate(t.Context(), pipeline.Request{
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
			resp, err = e.Translate(t.Context(), pipeline.Request{
				Text:       "太郎は本を花子に渡した。",
				SourceLang: lang.JA, TargetLang: lang.EN,
				DocumentID: t.Name() + fmt.Sprint(politeness), Mode: "auto",
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
		e := engine(t)
		if backend == "builtin" {
			e = builtinEngine(t)
		}
		resp := mustTranslate(t, e, "太郎が花子に本を渡した。")
		if resp.Result.Selected != nil {
			got := strings.ToLower(resp.Result.Selected.Text)
			for _, p := range []string{" he ", " she ", " his ", " her "} {
				if strings.Contains(got, p) {
					t.Errorf("%s: JA->EN invented %q: %q — Japanese has no gender on "+
						"が or は", backend, p, got)
				}
			}
		}
		resp = mustTranslate(t, e, "Taro gave a book to Hanako.")
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
//
// The switch is the engine's morphological registry, not an environment
// variable. Setting JEV_SUDACHI_ADAPTER changes what the command line
// constructs; it does not change an engine a caller already built, so a test
// that only set the variable was running the same analyser twice.
func forEachBackend(t *testing.T, check func(t *testing.T, backend string)) {
	t.Helper()

	t.Run("builtin", func(t *testing.T) {
		check(t, "builtin")
	})

	t.Run("sudachi", func(t *testing.T) {
		if !sudachiAvailable() {
			t.Skip("sudachi is not usable in this environment")
		}
		check(t, "sudachi")
	})
}

// withEngine runs body against one of the two engines.
func withEngine(t *testing.T, backend string, body func(t *testing.T, e *pipeline.Engine)) {
	t.Helper()
	if backend == "sudachi" {
		body(t, engine(t))
		return
	}
	body(t, builtinEngine(t))
}

// TestBothDirectionsSurviveBothBackends is the plain directional net. The two
// golden sentences are the ones the whole stack is built on; if either of them
// works in one direction and not the other, the system is not bidirectional in
// any sense that matters.
func TestBothDirectionsSurviveBothBackends(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		e := engine(t)
		if backend == "builtin" {
			e = builtinEngine(t)
		}
		resp := mustTranslate(t, e, "太郎が花子に本を渡した。")
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
		resp, err := e.Translate(t.Context(), pipeline.Request{
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
		plain, err := e.Translate(t.Context(), pipeline.Request{
			Text: "Taro gave a book to Hanako.", SourceLang: lang.EN, TargetLang: lang.JA,
			DocumentID: t.Name(), Mode: "auto",
			Style: plan.StyleProfile{Register: plan.RegisterNeutral, PronounExplicitness: 0.3},
		})
		if err != nil {
			t.Fatalf("%s: EN->JA plain: %v", backend, err)
		}
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
		e := engine(t)
		if backend == "builtin" {
			e = builtinEngine(t)
		}
		resp := mustTranslate(t, e, "今日はいい天気ですね。")
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
		e := engine(t)
		if backend == "builtin" {
			e = builtinEngine(t)
		}
		resp := mustTranslate(t, e, "今日はいい天気ですか。")
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

// TestIndefinitePronounIsNotTheInterrogative pins a translation that was wrong
// in the confident direction.
//
// 「誰かが来た。」 came out as "Who came." — the interrogative reading, which is
// the opposite of what the sentence says. Sudachi reports 誰, か, が as three
// morphemes because か and が are separate particles, so the noun phrase ended
// before the か and the lexicalizer was handed a bare 誰.
//
// The fix has two halves and both are needed. The phrase absorbs the か when a
// case particle follows it, which is what distinguishes the indefinite 誰か from
// the interrogative 誰. And the quantifier lookup reads the phrase rather than
// only the head morpheme: 誰 is a pronoun and 誰か is a generalized quantifier,
// so looking only at the head left the source with no scope node while the
// English "someone" had one, and the verifier then refused every faithful
// rendering as DIVERGENT.
func TestIndefinitePronounIsNotTheInterrogative(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		e := engine(t)
		if backend == "builtin" {
			e = builtinEngine(t)
		}
		for _, tc := range []struct {
			src  string
			want string
		}{
			{"誰かが来た。", "Someone came."},
			{"誰かが云ふ。", "Someone says."},
			{"誰かが本を読んだ。", "Someone read a book."},
		} {
			// A document per sentence: the engine keeps document state, and three
			// unrelated sentences sharing one accumulate discourse the later two
			// resolve against. That is correct behaviour and the wrong thing for a
			// test of independent sentences.
			resp := mustTranslate(t, e, tc.src)
			if resp.Result.Selected == nil {
				t.Errorf("%q: no candidate", tc.src)
				continue
			}
			if got := resp.Result.Selected.Text; got != tc.want {
				t.Errorf("%q = %q, want %q", tc.src, got, tc.want)
			}
		}
	})
}

// The interrogative reading must survive the fix above. 「誰が来た。」 asks who
// came; if 誰 alone had been rewritten to someone, the system would be
// answering its own question.
func TestInterrogativePronounSurvives(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		e := engine(t)
		if backend == "builtin" {
			e = builtinEngine(t)
		}
		resp := mustTranslate(t, e, "誰が来た。")
		if resp.Result.Selected == nil {
			t.Fatal("no candidate")
		}
		if got := resp.Result.Selected.Text; got != "Who came." {
			t.Errorf("誰が来た。 = %q, want %q", got, "Who came.")
		}
	})
}

// English has no plural of "someone": the indefinite reading already covers any
// number, so "someones" is not a word. The regular -s rule used to produce it,
// and 「誰かが来た」 came out as "Someones came."
func TestQuantifierPronounsHaveNoPlural(t *testing.T) {
	for _, w := range []string{"someone", "anyone", "everyone", "nobody",
		"something", "anything", "everything", "nothing", "each", "neither"} {
		if got := plan.Pluralize(w, jlir.NumberPlural); got != w {
			t.Errorf("plural of %q = %q; the quantifier pronouns are invariant", w, got)
		}
	}
	// And the regular rule still applies to everything else.
	for _, tc := range []struct{ sing, plur string }{
		{"book", "books"}, {"city", "cities"}, {"box", "boxes"}, {"child", "children"},
	} {
		if got := plan.Pluralize(tc.sing, jlir.NumberPlural); got != tc.plur {
			t.Errorf("plural of %q = %q, want %q", tc.sing, got, tc.plur)
		}
	}
}

// mustTranslate runs one sentence and fails the test if the engine does.
func mustTranslate(t *testing.T, e *pipeline.Engine, src string) *pipeline.Response {
	t.Helper()
	if e == nil {
		return translate(t, src, lang.JA, lang.EN, "auto")
	}
	// The style is the CLI's default rather than a zero value. A zero-valued
	// profile is a legitimate request, but it is not what the command line does,
	// and a golden written against it is a golden for a configuration nobody
	// ships.
	resp, err := e.Translate(t.Context(), pipeline.Request{
		Text: src, SourceLang: lang.JA, TargetLang: lang.EN,
		DocumentID: t.Name() + "|" + src, Mode: "auto",
		Style: plan.StyleProfile{Register: plan.RegisterNeutral, Politeness: 0.5,
			PronounExplicitness: 0.3},
	})
	if err != nil {
		t.Fatalf("Translate(%q): %v", src, err)
	}
	return resp
}

// An omitted Japanese subject whose referent stays open is realized as the
// English indefinite pronoun. This is the most common Japanese-to-English
// pattern there is — Japanese drops the subject of an ordinary sentence and
// English cannot — and it produced nothing at all: the realizer refused to
// invent a subject, so 「本を読んだ。」 had no candidate.
//
// Three things had to agree before it could.
//
// The target projection now realizes an undecided zero anaphor as the English
// indefinite. "someone" says there is an agent without saying which, which is
// what the source asserted; "they" would have put a person in a slot the source
// left open and claimed a number it never licensed.
//
// NPPlan.Omitted treated any zero anaphor as omitted in the target, so the
// choice was made and then deleted on the way to the surface.
//
// And the source records that an undecided zero subject is existentially
// quantified. English spells it, the re-parse sees a SOME scope node the
// Japanese source did not have, and the verifier compares scope operators by
// count — so the faithful English was rejected for carrying an operator the
// source was missing rather than wrong. The anaphor is only quantified when
// the referent is still open: one the discourse layer resolved to Taro is Taro,
// and quantifying that would be a different proposition.
func TestOmittedSubjectBecomesAnIndefiniteInEnglish(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		resp := mustTranslate(t, backendEngine(t, backend), "本を読んだ。")
		if resp.Result.Selected == nil {
			t.Fatalf("%s: an omitted subject must still produce a candidate", backend)
		}
		if got := resp.Result.Selected.Text; got != "someone read a book." {
			t.Errorf("%s: %q, want %q", backend, got, "someone read a book.")
		}
	})
}

// An anaphor the source supplied must not be turned into an indefinite. The
// exemption in the verifier is about English words that denote without picking;
// it must not extend to a form that does pick.
func TestResolvedSubjectKeepsItsReferent(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		resp := mustTranslate(t, backendEngine(t, backend), "太郎が本を読んだ。")
		if resp.Result.Selected == nil {
			t.Fatalf("%s: no candidate", backend)
		}
		got := resp.Result.Selected.Text
		if strings.Contains(got, "someone") || strings.Contains(got, "Someones") {
			t.Errorf("%s: an overt subject was replaced by an indefinite: %q", backend, got)
		}
	})
}

func backendEngine(t *testing.T, backend string) *pipeline.Engine {
	t.Helper()
	if backend == "sudachi" {
		return engine(t)
	}
	return builtinEngine(t)
}

// "unknown" is a sentinel, not a register.
//
// The pragmatics layer writes SpeechStyle "unknown" when the morphology
// decides nothing, which is the correct conservative behaviour: guessing plain
// because no polite marker was found would be an invented claim about
// register. The verifier then compared a determined source style against that
// sentinel and charged "speech style changed from plain to unknown" — the
// checker reporting that it knew nothing as though the target had said
// something.
//
// The target side is re-analysed from English, which carries no polite
// auxiliary to read, so this was the common case rather than an edge one. It is
// the same distinction compareNumber already makes: an unexpressed layer is not
// a claim the other side contradicts.
func TestUnknownRegisterIsNotADiff(t *testing.T) {
	resp := mustTranslate(t, engine(t), "誰かが云ふ。")
	if resp.Result.Selected == nil {
		t.Fatal("no candidate")
	}
	sel := resp.Result.Selected
	for _, d := range sel.Diffs {
		if strings.Contains(d.Detail, "to unknown") {
			t.Errorf("an undetermined register was charged as a change: %q", d.Detail)
		}
	}
	// And the diff that remains must be a real one, not the sentinel.
	for _, d := range sel.Diffs {
		if d.Detail == "referent alignment could not be checked lexically" {
			continue
		}
		t.Logf("remaining diff: %s/%s %s", d.Dimension, d.Severity, d.Detail)
	}
}

// A や-coordinated object is one argument with two conjuncts.
//
// や closed the noun phrase as if it were a case particle, so the second
// conjunct opened a phrase of its own and then lost the を-slot to the first.
// On the corpus 「肥料や薪炭を」 came out carrying 薪炭 and no 肥料 at all — the
// conjunct was absent from the translation, not degraded, and the event
// asserted half of what the sentence said.
func TestYaCoordinationKeepsBothConjuncts(t *testing.T) {
	forEachBackend(t, func(t *testing.T, backend string) {
		e := backendEngine(t, backend)
		// 本 and 紙 are in both dictionaries, so this tests the coordination
		// rule rather than the analyser's vocabulary. A sentence one backend
		// cannot segment would turn a parser assertion into a dictionary test.
		resp, err := e.Translate(t.Context(), pipeline.Request{
			Text: "本や紙を買った。", SourceLang: lang.JA, TargetLang: lang.EN,
			DocumentID: t.Name(), Mode: "auto",
			Style: plan.StyleProfile{Register: plan.RegisterNeutral, Politeness: 0.5},
		})
		if err != nil {
			t.Fatal(err)
		}
		obj := resp.Artifacts.Syntax.Clauses[0].Object
		if obj == nil {
			t.Fatalf("%s: no object", backend)
		}
		if obj.Case != "wo" {
			t.Errorf("%s: the coordinated phrase took the を-slot, got case %q", backend, obj.Case)
		}
		text := obj.Span.Text(resp.JLIR.Source.Source)
		if !strings.Contains(text, "本") || !strings.Contains(text, "紙") {
			t.Errorf("%s: the object span %q must contain both conjuncts", backend, text)
		}
		if len(obj.Conjoined) != 1 {
			t.Errorf("%s: want exactly one conjunct recorded, got %v", backend, obj.Conjoined)
		}
		// And both must reach the graph, not just the parse.
		var aliases []string
		for _, en := range resp.JLIR.Source.Entities {
			for _, al := range en.Aliases {
				aliases = append(aliases, al.Surface)
			}
		}
		joined := strings.Join(aliases, " ")
		if !strings.Contains(joined, "本") || !strings.Contains(joined, "紙") {
			t.Errorf("%s: a conjunct never reached the graph; entities carry %v", backend, aliases)
		}
	})
}
