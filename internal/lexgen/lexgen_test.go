package lexgen

import (
	"testing"

	"github.com/nico2525nn/jev-trans/internal/lang"
)

// TestSharedEnglishLexemeResolvesBothWays is the regression test for the
// first-wins inversion: six English nouns were shared by two Japanese words and
// the losing one silently became unreachable from EN → JA.
func TestSharedEnglishLexemeResolvesBothWays(t *testing.T) {
	l := Default()
	for _, tc := range []struct{ en, ja string }{
		{"friend", "友達"},
		{"sister", "姉"},
		{"child", "子供"},
		{"brother", "兄"},
		{"student", "生徒"},
		{"bag", "鞄"},
	} {
		got, ok := l.Form(tc.en, lang.EN, lang.JA, "n", false)
		if !ok {
			t.Errorf("Form(%q, en→ja) is unresolved, want %q", tc.en, tc.ja)
			continue
		}
		if got != tc.ja {
			t.Errorf("Form(%q, en→ja) = %q, want %q", tc.en, got, tc.ja)
		}
	}
	// The alternative Japanese word must still work in its own direction.
	for _, ja := range []string{"友", "妹", "子ども", "弟", "学生", "袋"} {
		if _, ok := l.Form(ja, lang.JA, lang.EN, "n", false); !ok {
			t.Errorf("Form(%q, ja→en) is unresolved, want an English lexeme", ja)
		}
	}
}

// TestCollisionsAreReported: the inversion has to choose, and the choice is
// reported rather than hidden behind a "symmetric by construction" comment.
func TestCollisionsAreReported(t *testing.T) {
	want := map[string]bool{
		"friend": true, "sister": true, "child": true,
		"brother": true, "student": true, "bag": true,
	}
	got := Default().Collisions()
	if len(got) == 0 {
		t.Fatal("no collisions reported, but jaNouns shares six English lexemes")
	}
	for _, en := range got {
		if !want[en] {
			t.Errorf("unexpected collision reported for %q", en)
		}
		delete(want, en)
	}
	for en := range want {
		t.Errorf("collision %q is not reported", en)
	}
}

// TestEveryJapaneseKeyIsReachable is the regression test for the three keys
// that were not Japanese: "ceipt" (a truncated Latin string), the Hangul 이웃
// and the Simplified Chinese 伙伴. None can be produced by the analyzer, and
// Form("receipt", en, ja) returned the string "ceipt" with ok=true.
func TestEveryJapaneseKeyIsReachable(t *testing.T) {
	if err := Validate(Default()); err != nil {
		t.Fatal(err)
	}
	l := Default()
	for _, bad := range []string{"ceipt", "이웃", "伙伴"} {
		if _, ok := l.nouns[bad]; ok {
			t.Errorf("jaNouns still carries the non-Japanese key %q", bad)
		}
	}
}

// TestTargetSurfaceIsAlwaysJapanese: every value the package returns for a
// Japanese source, and every target it produces from English, has to be in the
// target script. Returning the English word unchanged is how a lexical gap gets
// mistaken for a grounded translation.
func TestTargetSurfaceIsAlwaysJapanese(t *testing.T) {
	l := Default()
	if got, ok := l.Form("then", lang.EN, lang.JA, "adv", false); !ok {
		t.Error("then is unresolved; a lexical gap would be honest, a Latin echo would not")
	} else if got == "then" {
		t.Error("Form(then, en→ja) returned the English word unchanged")
	}
	for ja := range l.nouns {
		if got, ok := l.Form(ja, lang.JA, lang.EN, "n", false); ok && got == ja {
			t.Errorf("Form(%q, ja→en) returned the Japanese word unchanged", ja)
		}
	}
}

// TestStatsCoversDerivedTables: Stats omitted enNoun/enName, which is why the
// six dropped noun mappings stayed invisible.
func TestStatsCoversDerivedTables(t *testing.T) {
	s := Stats(Default())
	for _, k := range []string{"japaneseNouns", "japaneseNames", "identityTerms",
		"katakanaFragments", "englishNouns", "englishNames", "nounCollisions"} {
		if _, ok := s[k]; !ok {
			t.Errorf("Stats omits %q", k)
		}
	}
	if s["englishNouns"] == 0 {
		t.Error("Stats reports no English nouns; the EN → JA table is derived and was hidden")
	}
}
