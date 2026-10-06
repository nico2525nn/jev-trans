package lex

import (
	"strings"
	"testing"
)

// The two "spelled the same" sets are derived from the paradigm table, so this
// test asserts the derivation itself rather than a hand-copied list. A test that
// restated the same list would pass while both drifted apart from the data —
// which is exactly what happened: spend was in the tense-ambiguous set while
// its own paradigm spells the past "spent", and shoot and cost were in the
// self-inflected set while theirs spell "shot" and "costed".
func TestTenseAmbiguousSetMatchesTheParadigmTable(t *testing.T) {
	for base, v := range enIrregularVerbs {
		past, alt, multiple := strings.Cut(v.Past, "/")
		if multiple || alt != "" || past == "" {
			if enTenseAmbiguousIrregular(base) {
				t.Errorf("%q is listed as tense-ambiguous but its past is %q, which is "+
					"spelled differently", base, v.Past)
			}
			continue
		}
		want := past == base
		if got := enTenseAmbiguousIrregular(base); got != want {
			if want {
				t.Errorf("%q: past is %q, identical to the base, but it is not treated "+
					"as tense-ambiguous", base, past)
			} else {
				t.Errorf("%q: past is %q, spelled differently from the base, yet it is "+
					"treated as tense-ambiguous", base, past)
			}
		}
	}
}

// The concrete case that started this: spend is spent, not spend.
func TestSpendIsNotTenseAmbiguous(t *testing.T) {
	if enTenseAmbiguousIrregular("spend") {
		t.Fatal(`spend must not be tense-ambiguous: its past is "spent"`)
	}
	// And the verb that actually needs settling still is.
	for _, v := range []string{"read", "set", "put", "cut", "let", "shut", "spread"} {
		if !enTenseAmbiguousIrregular(v) {
			t.Errorf("%q is spelled the same in base, past and participle and must "+
				"stay undecided", v)
		}
	}
}

func TestSelfInflectedSetMatchesTheParadigmTable(t *testing.T) {
	for base, v := range enIrregularVerbs {
		past, alt, multiple := strings.Cut(v.Past, "/")
		if multiple || alt != "" || past == "" {
			if enSelfInflected[base] {
				t.Errorf("%q is listed as self-inflected but its paradigm is %q/%q",
					base, v.Past, v.PP)
			}
			continue
		}
		want := past == base && v.PP == base
		if got := enSelfInflected[base]; got != want {
			t.Errorf("%q: past %q participle %q, so self-inflected=%v; the table says %v",
				base, past, v.PP, got, want)
		}
	}
}

// A verb whose past differs is decidable by its suffix, and treating it as
// ambiguous costs the analyser a decision it had no reason to withhold.
func TestDecidableIrregularsAreNotListed(t *testing.T) {
	for _, v := range []string{"spend", "shoot", "cost", "burst", "find", "buy"} {
		if enTenseAmbiguousIrregular(v) {
			t.Errorf("%q is listed as tense-ambiguous but its past is spelled differently", v)
		}
		if enSelfInflected[v] {
			t.Errorf("%q is listed as self-inflected but its past is spelled differently", v)
		}
	}
}
