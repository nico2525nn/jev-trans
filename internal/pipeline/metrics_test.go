package pipeline_test

import (
	"testing"

	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/pipeline"
)

// The stage table must describe the response it travelled in.
//
// Selected used to be the length of the shortlist while the output stage takes
// exactly one candidate. It agreed only while no sentence produced two
// survivors, and the first one that did would have made these numbers disagree
// with the reply to the same request — which is the failure mode that reads as
// "the UI is lying" and costs an afternoon.
func TestSelectedMatchesTheResponse(t *testing.T) {
	for _, src := range []string{
		"太郎が花子に本を渡した。",
		"本を読んだ。",
		"太郎が来た。",
		"誰かが来た。",
	} {
		for _, tgt := range []lang.Lang{lang.EN, lang.JA} {
			sl := lang.JA
			if tgt == lang.JA {
				sl = lang.EN
			}
			resp := translate(t, src, sl, tgt, "auto")
			m := resp.Metrics

			want := 0
			if resp.Result.Selected != nil {
				want = 1
			}
			if m.Selected != want {
				t.Errorf("%q -> %s: stage metrics report selected=%d but the response "+
					"carries %d", src, tgt, m.Selected, want)
			}
			if m.ShortlistedCandidates != m.EligibleCandidates {
				t.Errorf("%q -> %s: shortlisted %d and eligible %d are the same set; "+
					"they are named separately only to answer different questions",
					src, tgt, m.ShortlistedCandidates, m.EligibleCandidates)
			}
			if m.Selected > m.ShortlistedCandidates {
				t.Errorf("%q -> %s: selected %d exceeds the shortlist of %d",
					src, tgt, m.Selected, m.ShortlistedCandidates)
			}
		}
	}
}

// The four counts are separate claims and none of them may exceed the one
// before it. A funnel that goes up is a bug in the accounting, whatever the
// numbers say.
func TestCandidateFunnelOnlyDecreases(t *testing.T) {
	for _, src := range []string{
		"太郎が花子に本を渡した。",
		"本を読んだ。",
		"誰かが来た。",
		"空が青くなった。",
	} {
		resp := translate(t, src, lang.JA, lang.EN, "auto")
		m := resp.Metrics
		checks := []struct {
			name      string
			outer, in int
		}{
			{"eligible", m.RawCandidates, m.EligibleCandidates},
			{"certified", m.EligibleCandidates, m.CertifiedCandidates},
			{"selected", m.ShortlistedCandidates, m.Selected},
		}
		for _, c := range checks {
			if c.in > c.outer {
				t.Errorf("%q: %s (%d) exceeds raw/shortlisted (%d)",
					src, c.name, c.in, c.outer)
			}
		}
	}
}

// certified and verified are the same number under two names, and only one of
// them may be dropped. They used to mean different things — verified counted
// whatever survived the gate — and the stage tool read the wrong one for a
// while.
func TestVerifiedIsAnAliasOfCertified(t *testing.T) {
	resp := translate(t, "太郎が花子に本を渡した。", lang.JA, lang.EN, "auto")
	if resp.Metrics.Verified != resp.Metrics.CertifiedCandidates {
		t.Errorf("verified=%d but certifiedCandidates=%d; they name the same count and "+
			"must not drift", resp.Metrics.Verified, resp.Metrics.CertifiedCandidates)
	}
	_ = pipeline.StatusExact
}
