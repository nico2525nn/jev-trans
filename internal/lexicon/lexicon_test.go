package lexicon

import (
	"strings"
	"testing"
)

func TestNoConjugationEngineDuplicatedHere(t *testing.T) {
	// This package used to carry its own jaForms/surForms/godanForms tables.
	// The concrete regression they caused: 行く was indexed as 行いて/行いた
	// (a hardcoded く-row te-form) beside the correct 行って/行った.
	for _, nonWord := range []string{"行いて", "行いた"} {
		if hits := Default().SensesJP(nonWord); len(hits) > 0 {
			t.Errorf("SensesJP(%q) = %v; the く-row shape was applied to 行く, which opts out of it",
				nonWord, hits)
		}
	}
	for _, form := range []string{"行って", "行った", "聞いて", "聞いた"} {
		if hits := Default().SensesJP(form); len(hits) == 0 {
			t.Errorf("SensesJP(%q) = nothing, want the 行く reading", form)
		}
	}
}

func TestGeneratedNonWordsAreAbsent(t *testing.T) {
	l := Default()
	// 帰る is a godan る verb (帰らない / 帰ります / 帰ったら). It used to be
	// typed as ichidan, which indexed all of these.
	for _, nonWord := range []string{"帰た", "帰ます", "帰ました", "帰たい", "帰たら"} {
		if hits := l.SensesJP(nonWord); len(hits) > 0 {
			t.Errorf("SensesJP(%q) = %v, want nothing: 帰る does not form this word", nonWord, hits)
		}
	}
	for _, form := range []string{"帰る", "帰った", "帰らない", "帰らなかった", "帰ります", "帰りました"} {
		if hits := l.SensesJP(form); len(hits) == 0 {
			t.Errorf("SensesJP(%q) = nothing, want the 帰る reading", form)
		}
	}
}

// TestNounIsNotReinterpretedAsAVerb is the regression test for the greedy
// longest-prefix fallback: 行く先 ("destination") resolved to MOVE.01 and
// 買った本 ("the book I bought") to TRANSFER.05, because both start with a
// registered verb. A lookup that answers about one word with a reading of
// another is worse than no answer (plan.md §25).
func TestNounIsNotReinterpretedAsAVerb(t *testing.T) {
	l := Default()
	for _, tc := range []struct{ surface, wrongSense string }{
		{"行く先", "MOVE.01"},
		{"買った本", "TRANSFER.05"},
		{"行った人", "MOVE.01"},
	} {
		for _, h := range l.SensesJP(tc.surface) {
			if h.SenseID == tc.wrongSense {
				t.Errorf("SensesJP(%q) returned %s: the prefix %q is a different word",
					tc.surface, h.SenseID, strings.TrimSuffix(tc.surface, "先"))
			}
		}
	}
}

// TestEverySenseIDExists is the package's own consistency check; the tables are
// data, so a dangling SenseID is a build-time fact.
func TestEverySenseIDExists(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestIdiomLanguagesAreSet guards plan.md §49: an idiom has to be detected in
// the language it is written in, and 雪に負ける is Japanese even though its row
// once carried no Lang and was filed into the English namespace.
func TestIdiomLanguagesAreSet(t *testing.T) {
	l := Default()
	jaOnly := []string{"雪に負ける", "雪が降り積もる", "手を出す", "気を使う", "目が利く"}
	for _, s := range jaOnly {
		id, ok := l.Idiom(s)
		if !ok {
			t.Errorf("idiom %q is not registered", s)
			continue
		}
		if id.Lang != "ja" {
			t.Errorf("idiom %q is registered with Lang=%q, want ja", s, id.Lang)
		}
		if hits := l.SensesEN(s); len(hits) > 0 {
			t.Errorf("Japanese idiom %q leaked into the English sense map as %v", s, hits)
		}
	}
}

// TestEnIrregularHasNoDuplicateLemma: the table declared buy, bring and write
// twice, and the later copy of write had dropped `written`, so the drifted row
// silently won.
func TestEnIrregularHasNoDuplicateLemma(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range enIrregular {
		if seen[row.Base] {
			t.Errorf("enIrregular declares %q twice; the later row silently wins", row.Base)
		}
		seen[row.Base] = true
	}
	for _, want := range []struct{ lemma, form string }{
		{"buy", "bought"},
		{"bring", "brought"},
		{"write", "written"},
	} {
		if !seen[want.lemma] {
			t.Errorf("enIrregular is missing %q", want.lemma)
		}
		if enIrregularReverse[want.form] != want.lemma {
			t.Errorf("enIrregularReverse[%q] = %q, want %q", want.form, enIrregularReverse[want.form], want.lemma)
		}
	}
}
