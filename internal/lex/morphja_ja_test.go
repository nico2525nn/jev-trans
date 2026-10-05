package lex

import (
	"strings"
	"testing"
)

// segOf returns the morpheme surfaces of the analyzer's best path.
func segOf(t *testing.T, src string) []string {
	t.Helper()
	best := AnalyzeJA(src).Best()
	out := make([]string, len(best.Morphs))
	for i, m := range best.Morphs {
		out[i] = m.Surface
	}
	return out
}

// TestKatakanaLoanwordStaysOneMorpheme is the regression test for ー being
// classified as punctuation: because the unknown-run scan stopped at any
// dictionary entry, ビール segmented as ビ / ー / ル and two of those three
// morphemes were unresolved.
func TestKatakanaLoanwordStaysOneMorpheme(t *testing.T) {
	for _, word := range []string{"ビール", "コーヒー", "テーブル", "カメラ", "インターネット"} {
		got := segOf(t, word)
		if len(got) != 1 {
			t.Errorf("%q segmented as %v, want one morpheme", word, got)
		}
	}
}

// TestChouonpuIsNotAMorpheme: ー belongs to the katakana word it prolongs. A
// standalone ー dictionary entry makes every loanword shatter.
func TestChouonpuIsNotAMorpheme(t *testing.T) {
	if _, ok := jaLex["ー"]; ok {
		t.Error("ー is registered as a standalone morpheme; it is the vowel of the word before it")
	}
}

// TestDeInflectedVerbIsNotUnresolved is the regression test for plan.md §25:
// 飲みました was flagged Unknown even though its base 飲む is in the lexicon,
// so the semantics layer recorded every correctly de-inflected verb as an
// unresolved semantic unit.
func TestDeInflectedVerbIsNotUnresolved(t *testing.T) {
	for _, tc := range []struct{ surface, base string }{
		{"飲みました", "飲む"},
		{"食べました", "食べる"},
		{"見ました", "見る"},
		{"書きました", "書く"},
		{"帰りました", "帰る"},
		{"来られました", "来る"},
		{"不应该", ""},
	} {
		if tc.base == "" {
			continue
		}
		mf := AnalyzeJA(tc.surface)
		best := mf.Best()
		if len(best.Morphs) == 0 {
			t.Errorf("%q produced no morphemes", tc.surface)
			continue
		}
		m := best.Morphs[0]
		if m.Surface != tc.surface {
			t.Errorf("%q segmented as %q, want the whole surface as one morpheme", tc.surface, m.Surface)
			continue
		}
		if !m.Dict || m.Unknown {
			t.Errorf("%q → %q: Dict=%v Unknown=%v, want a resolved de-inflection",
				tc.surface, m.Base, m.Dict, m.Unknown)
		}
		if m.Base != tc.base {
			t.Errorf("%q → base %q, want %q", tc.surface, m.Base, tc.base)
		}
	}
}

// TestIrregularConjugationClassesAreImplemented is the regression test for the
// three declared classes the index ignored: cj=s (する), cj=r (さる) and cj=k
// (来る). With them unimplemented 先生が来られた could not be segmented at all.
func TestIrregularConjugationClassesAreImplemented(t *testing.T) {
	for _, tc := range []struct{ surface, base string }{
		{"します", "する"},
		{"しませんでした", "する"},
		{"しないでした", "する"},
		{"いたします", "いたす"},
		{"致しません", "致す"},
		{"なさいます", "なさる"},
		{"いらっしゃいました", "いらっしゃる"},
		{"来ます", "来る"},
		{"来ました", "来る"},
		{"来られた", "来る"},
		{"来ません", "来る"},
		{"きた", "くる"},
	} {
		readings, ok := jaConj[tc.surface]
		if !ok {
			t.Errorf("%q is not in the conjugation index; the class that produces it is unimplemented", tc.surface)
			continue
		}
		found := false
		for _, r := range readings {
			if r.base == tc.base {
				found = true
			}
		}
		if !found {
			t.Errorf("%q resolves to %v, want a reading of %q", tc.surface, readings, tc.base)
		}
	}
}

// TestEveryDeclaredIrregularClassIsTabulated keeps the tables and the data in
// step: a cj=s/cj=r/cj=k row with no table entry gets no conjugations at all,
// which is exactly how the classes went missing in the first place.
func TestEveryDeclaredIrregularClassIsTabulated(t *testing.T) {
	for surface, entries := range jaLex {
		for _, e := range entries {
			c := e.feats["cj"]
			if c != "s" && c != "r" && c != "k" {
				continue
			}
			if _, ok := jaIrregularVerbForms[surface]; !ok {
				t.Errorf("%s is declared cj=%s but has no entry in jaIrregularVerbForms", surface, c)
			}
		}
	}
}

// TestKuruIsNotIchidan is the regression test for 来る being declared cj=i,
// which made the index generate こない / こます / こて / こた — none of which is
// a Japanese word.
func TestKuruIsNotIchidan(t *testing.T) {
	if c := JAClass("来る"); c == "i" {
		t.Error("来る is declared ichidan; it is an irregular verb of its own class")
	}
	for _, nonWord := range []string{"こない", "こます", "こて", "こた", "来まする"} {
		if _, ok := jaConj[nonWord]; ok {
			t.Errorf("the conjugation index manufactures the non-word %q", nonWord)
		}
	}
}

// TestGodanIrregularTeReplacesTheRowShape: 行く takes 行って/行った, not the
// く-row いて/いた, while 聞く and 書く keep the row's regular shapes. Applying
// the row unconditionally indexed 行いて beside 行って.
func TestGodanIrregularTeReplacesTheRowShape(t *testing.T) {
	row, ok := jaGodanTableFor("行く")
	if !ok || row.kana != "く" {
		t.Fatalf("行く no longer resolves to the く-row: %+v", row)
	}
	for _, form := range []string{"行って", "行った"} {
		if _, ok := jaConj[form]; !ok {
			t.Errorf("%q is missing from the index", form)
		}
	}
	for _, nonWord := range []string{"行いて", "行いた"} {
		if _, ok := jaConj[nonWord]; ok {
			t.Errorf("the index manufactures the non-word %q", nonWord)
		}
	}
	// The rest of the く-row must be untouched.
	for _, form := range []string{"聞いて", "聞いた", "書いた"} {
		if _, ok := jaConj[form]; !ok {
			t.Errorf("%q went missing when 行く's irregular pair was handled", form)
		}
	}
}

// TestJAConjugationsIsDeterministic covers the exported reverse index the
// lexicon consumes in place of its own conjugation engine.
func TestJAConjugationsIsDeterministic(t *testing.T) {
	first := JAConjugations("飲む")
	if len(first) == 0 {
		t.Fatal("JAConjugations(飲む) returned nothing")
	}
	for i := 1; i < len(first); i++ {
		if first[i-1] > first[i] {
			t.Fatalf("JAConjugations is not sorted: %v", first)
		}
	}
	if second := JAConjugations("飲む"); strings.Join(second, ",") != strings.Join(first, ",") {
		t.Error("JAConjugations is not deterministic across calls")
	}
	if !strings.Contains(strings.Join(first, ","), "飲みました") {
		t.Errorf("JAConjugations(飲む) = %v, want it to contain 飲みました", first)
	}
	if got := JAConjugations("__no_such_verb__"); got != nil {
		t.Errorf("JAConjugations on an unknown base returned %v, want nil", got)
	}
}
