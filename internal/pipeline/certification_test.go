package pipeline

import (
	"testing"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/verify"
)

// "certified: 0" cannot be acted on. It looks identical whether the verifier is
// strict or whether it has no way to check what the system produced, and those
// call for opposite responses — leave it alone, or build the capability. The
// blockers exist to tell those apart.
func TestCertificationBlockersNameTheReason(t *testing.T) {
	cases := []struct {
		name string
		c    Candidate
		want []string
	}{
		{
			name: "clean candidate certifies",
			c:    Candidate{Status: StatusGood},
			want: nil,
		},
		{
			name: "exact candidate certifies",
			c:    Candidate{Status: StatusExact},
			want: nil,
		},
		{
			name: "an undetermined verdict blocks",
			c:    Candidate{Status: StatusUnderdetermined},
			want: []string{BlockerUnderdetermined},
		},
		{
			name: "a preserved ambiguity blocks",
			c:    Candidate{Status: StatusAmbiguous},
			want: []string{BlockerAmbiguous},
		},
		{
			name: "unsupported information blocks",
			c: Candidate{
				Status:      StatusGood,
				Unsupported: []jlir.UnsupportedFeature{{Key: "gender", Value: "female"}},
			},
			want: []string{BlockerUnsupportedInformation},
		},
		{
			name: "an unverifiable referent is named as such",
			c: Candidate{
				Status: StatusGood,
				Diffs: []verify.Diff{{
					Dimension: verify.DiffEntity, Severity: verify.SeveritySoft,
					Item: "v1/agent", Detail: "referent alignment could not be checked lexically",
				}},
			},
			want: []string{BlockerReferentUnverified},
		},
		{
			name: "a tense that could not be checked is named",
			c: Candidate{
				Status: StatusGood,
				Diffs: []verify.Diff{{
					Dimension: verify.DiffTense, Severity: verify.SeveritySoft,
				}},
			},
			want: []string{"tense_open"},
		},
		{
			name: "a hard diff blocks even under a GOOD status",
			c: Candidate{
				// deriveStatus would not produce this pairing, but a caller can
				// hand one over and the rule must not depend on the pairing
				// being impossible.
				Status: StatusGood,
				Diffs: []verify.Diff{{
					Dimension: verify.DiffPolarity, Severity: verify.SeverityHard,
				}},
			},
			want: []string{"tense_open"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := certificationBlockers(tc.c)
			if len(got) != len(tc.want) {
				t.Fatalf("blockers = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("blockers = %v, want %v", got, tc.want)
				}
			}
			if certified := len(got) == 0; certified != candidateCertified(tc.c) {
				t.Errorf("candidateCertified says %v but the blocker list is %v",
					certified, got)
			}
		})
	}
}

// The slugs are aggregated across a corpus, so they have to be stable and free
// of anything sentence-specific. A slug carrying the item or the detail would
// make every occurrence its own bucket.
func TestBlockerSlugsAreStable(t *testing.T) {
	for _, c := range []Candidate{
		{Status: StatusGood, Diffs: []verify.Diff{{
			Dimension: verify.DiffEntity, Severity: verify.SeveritySoft,
			Item: "v1/agent", Detail: "role agent referent alignment could not be checked lexically",
		}}},
		{Status: StatusGood, Diffs: []verify.Diff{{
			Dimension: verify.DiffEntity, Severity: verify.SeveritySoft,
			Item: "v2/theme", Detail: "role theme referent alignment could not be checked lexically",
		}}},
	} {
		got := certificationBlockers(c)
		if len(got) != 1 || got[0] != BlockerReferentUnverified {
			t.Fatalf("two different sentences must produce the same slug; got %v", got)
		}
	}
}

// A candidate with several problems reports all of them. Collapsing them to the
// worst would hide the second problem, which may be the cheap one to fix.
func TestBlockersAreCumulative(t *testing.T) {
	c := Candidate{
		Status:      StatusGood,
		Unsupported: []jlir.UnsupportedFeature{{Key: "gender"}},
		Diffs: []verify.Diff{{
			Dimension: verify.DiffTense, Severity: verify.SeveritySoft,
		}},
		Notes: []string{"lexical_gap: no English realization of e2"},
	}
	got := certificationBlockers(c)
	if len(got) != 3 {
		t.Fatalf("want three blockers, got %v", got)
	}
	found := false
	for _, b := range got {
		if b == BlockerUnsupportedInformation {
			found = true
		}
	}
	if !found {
		t.Errorf("unsupported information must be among the blockers: %v", got)
	}
}
