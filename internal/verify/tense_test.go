package verify

import (
	"testing"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
)

// An undetermined source tense is not evidence of anything.
//
// equivalentTense used to return true for UNKNOWN against PAST, which reads
// as a licence to call an unproved tense a proved one — the opposite of what
// plan.md §61 says an undetermined feature should do. The branch turned out to
// be unreachable (normalizeFeature maps UNKNOWN to "" and the guard skips the
// comparison), so it never actually mis-declared anything; it just sat there
// looking like a decision. What it did leave behind was the real gap: an
// undetermined source tense against a committed target tense was compared
// nowhere and recorded nowhere.
//
// It is now UNDERDETERMINED, with the reason on the diff.
// The comparison itself, exercised where it lives.
func TestUndeterminedSourceTenseIsRecorded(t *testing.T) {
	src := &jlir.Graph{Source: "本を読んだ。", Events: []*jlir.Event{{
		ID: "v1", Predicate: "WORK.04", Tense: jlir.TenseUnknown,
	}}}
	tgt := &jlir.Graph{Source: "read a book.", Lang: "en", Events: []*jlir.Event{{
		ID: "v1", Predicate: "WORK.04", Tense: jlir.TensePast,
	}}}
	res := Verify(src, tgt, lang.JA, lang.EN)

	if res.Status != StatusUnderdetermined {
		t.Errorf("status is %q; an undetermined source tense against a committed target "+
			"tense is the UNDERDETERMINED condition, not a match", res.Status)
	}
	found := false
	for _, d := range res.Diffs {
		if d.Dimension == DiffTense {
			found = true
			if d.Severity == SeverityHard {
				t.Errorf("an undetermined source tense is not a mistranslation: %s", d.Detail)
			}
		}
	}
	if !found {
		t.Errorf("the undetermined tense was not recorded at all; diffs=%v", res.Diffs)
	}
}

// A determined tense that really changed is still a hard diff. Narrowing the
// undetermined case must not have softened this one.
func TestDeterminedTenseChangeIsStillHard(t *testing.T) {
	src := &jlir.Graph{Source: "本を読む。", Events: []*jlir.Event{{
		ID: "v1", Predicate: "WORK.04", Tense: jlir.TensePresent,
	}}}
	tgt := &jlir.Graph{Source: "read a book.", Lang: "en", Events: []*jlir.Event{{
		ID: "v1", Predicate: "WORK.04", Tense: jlir.TensePast,
	}}}
	res := Verify(src, tgt, lang.JA, lang.EN)

	hard := false
	for _, d := range res.Diffs {
		if d.Dimension == DiffTense && d.Severity == SeverityHard {
			hard = true
		}
	}
	if !hard {
		t.Errorf("present against past is a real change and must stay a hard diff; diffs=%v",
			res.Diffs)
	}
}

// A tense both sides determined identically is not a diff.
func TestMatchingTenseIsNotADiff(t *testing.T) {
	src := &jlir.Graph{Source: "本を読んだ。", Events: []*jlir.Event{{
		ID: "v1", Predicate: "WORK.04", Tense: jlir.TensePast,
	}}}
	tgt := &jlir.Graph{Source: "read a book.", Lang: "en", Events: []*jlir.Event{{
		ID: "v1", Predicate: "WORK.04", Tense: jlir.TensePast,
	}}}
	res := Verify(src, tgt, lang.JA, lang.EN)
	for _, d := range res.Diffs {
		if d.Dimension == DiffTense {
			t.Errorf("identical tenses produced a diff: %s", d.Detail)
		}
	}
	// The status is not asserted here: these graphs carry no arguments, so other
	// undetermined conditions fire for reasons that have nothing to do with
	// tense. The claim under test is the one about the tense diff.
}
