package verify

// Candidate ranking, plan.md §44 and §45.
//
// Two rules govern this file and neither of them is negotiable:
//
//  1. plan.md §44 — only candidates that passed the verifier reach the reranker.
//     Jev may score naturalness, idiomaticity, register match and contextual
//     fit, but it may never rescue a semantically wrong sentence. Hard
//     failures are therefore removed from the ranking before naturalness is
//     looked at, not merely pushed down.
//
//  2. plan.md §45 — a candidate A dominates B when A loses no more than B on
//     every loss dimension and is at least as natural. Dominated candidates are
//     pruned, because presenting "slightly worse at everything" to a user is
//     noise, not choice.
//
// The Jev rerank (decision D9) is injected as a function value so this package
// never imports internal/jev: the pipeline owns the client, the cache and the
// decision budget, and passes the scores in.

import (
	"context"
	"fmt"
	"sort"

	"github.com/nico/jev-trans/internal/trace"
)

// Candidate is one verified target string together with everything the ranker
// needs to order it.
type Candidate struct {
	Text string `json:"text"`
	// Verify is the semantic verdict. A candidate whose Verify is nil has not
	// been checked, and an unchecked candidate is not allowed to compete: the
	// ranker reports it as failing the hard gate rather than assuming it is
	// fine, because "we did not check" is not "we checked and found nothing".
	Verify *Result `json:"verify,omitempty"`
	// Naturalness is the oracle's 0..1 score, or the pipeline's own estimate
	// when no oracle ran. Zero is a valid score, not "unset".
	Naturalness float64 `json:"naturalness"`
	// Constructions lists the construction ids the realizer used, so the UI can
	// explain why this surface form was available at all.
	Constructions []string `json:"constructions,omitempty"`
	// Trace carries per-candidate provenance lines.
	Trace map[string]string `json:"trace,omitempty"`
}

// NaturalnessFn is the D9 rerank hook. It receives the candidates that already
// passed the hard gate and returns one 0..1 naturalness score per candidate, in
// the same order. Returning an error leaves the incoming scores untouched: a
// failed oracle call must degrade to priors, never to a crash or a reordering
// based on stale numbers.
type NaturalnessFn func(ctx context.Context, cands []Candidate) ([]float64, error)

// RankPruned returns the candidates that failed the hard gate together with the
// reason, so the UI can show "why she never existed" rather than silently
// omitting it (plan.md §37, §39).
type RankPruned struct {
	Index    int
	Text     string
	Rule     string
	Status   string
	Rejected []string
}

// RankOutcome is the full report of one ranking run: what was selected, what
// was pruned, and why.
type RankOutcome struct {
	Ranked  []Candidate
	Pruned  []RankPruned
	Drops   []string // Pareto dominance edges, for the trace and the UI
	Notes   []string
	Applied bool // whether a rerank hook actually changed a score
}

// hardRejected reports whether a candidate fails the semantic hard gate, and
// returns the rules that rejected it.
func hardRejected(c Candidate) (bool, []string) {
	if c.Verify == nil {
		return true, []string{"not verified: the semantic gate of plan.md §44 requires a verification result"}
	}
	var rules []string
	switch c.Verify.Status {
	case StatusUnparsable:
		rules = append(rules, "UNPARSABLE: the target sentence could not be re-parsed")
	case StatusUnsupported:
		rules = append(rules, "UNSUPPORTED: the target asserts information with no upstream provenance")
	}
	for _, d := range c.Verify.Diffs {
		if d.Severity == SeverityHard {
			rules = append(rules, fmt.Sprintf("hard diff [%s] %s: %s", d.Dimension, d.Item, d.Detail))
		}
	}
	for _, u := range c.Verify.Unsupported {
		rules = append(rules, fmt.Sprintf("unsupported %s=%s on %s", u.Key, u.Value, u.Owner))
	}
	return len(rules) > 0, rules
}

// Rank orders the candidates that pass the hard gate, using the naturalness
// scores already present on each candidate.
//
// Hard failures are removed first: plan.md §44 makes the verifier a hard gate
// and states plainly that a semantically wrong sentence must not be rescued by
// naturalness. A candidate that never went through Verify is treated as failing
// rather than as passing, so an unverified string cannot win by default.
func Rank(cands []Candidate) []Candidate {
	return RankOutcomeOf(cands).Ranked
}

// RankWith is Rank plus the D9 rerank hook and the trace span. score may be nil,
// in which case the naturalness already carried by the candidates is used; that
// is the normal path when no API key is configured and the client answers from
// priors (plan.md §56).
func RankWith(ctx context.Context, rec *trace.Recorder, cands []Candidate, score NaturalnessFn) (*RankOutcome, error) {
	var sp *trace.Span
	if rec != nil {
		sp = rec.Open(trace.StageRerank, "candidate dominance and Jev rerank")
		defer sp.Close()
	}

	out := RankOutcomeOf(cands)

	if score != nil {
		if err := applyNaturalness(ctx, score, out.Ranked); err != nil {
			out.Notes = append(out.Notes,
				"naturalness rerank unavailable: "+err.Error()+"; falling back to the incoming scores")
			if sp != nil {
				sp.Note("naturalness rerank unavailable: %v", err)
			}
		} else {
			out.Applied = true
		}
	}
	out = rerank(out)

	if sp != nil {
		sp.Data(out)
		sp.Count("ranked", len(out.Ranked))
		sp.Count("pruned", len(out.Pruned))
		sp.Count("dominated", len(out.Drops))
		for _, p := range out.Pruned {
			sp.Note("rejected %q by %s", p.Text, p.Rule)
		}
		for _, d := range out.Drops {
			sp.Note("dominance: %s", d)
		}
		for _, n := range out.Notes {
			sp.Note("%s", n)
		}
	}
	return out, nil
}

// RankOutcomeOf performs the hard-gate removal and the Pareto pruning and
// returns the full report. It is the deterministic core of Rank: no oracle, no
// trace, no randomness.
func RankOutcomeOf(cands []Candidate) *RankOutcome {
	out := &RankOutcome{}
	var passing []Candidate
	for i, c := range cands {
		if rejected, rules := hardRejected(c); rejected {
			status := ""
			if c.Verify != nil {
				status = c.Verify.Status
			}
			out.Pruned = append(out.Pruned, RankPruned{
				Index: i, Text: c.Text, Rule: rules[0], Status: status, Rejected: rules,
			})
			continue
		}
		passing = append(passing, c)
	}

	if len(passing) == 0 {
		if len(cands) > 0 && len(out.Pruned) > 0 {
			out.Notes = append(out.Notes,
				"no candidate passed the semantic hard gate; the system reports failure instead of choosing a bad sentence")
		}
		return out
	}

	kept, drops := paretoPrune(passing)
	out.Drops = drops
	out.Ranked = sortRanked(kept)
	if n := len(passing) - len(kept); n > 0 {
		out.Notes = append(out.Notes,
			fmt.Sprintf("%d dominated candidate(s) removed (plan.md §45)", n))
	}
	return out
}

// rerank re-applies the ordering after a naturalness hook has updated the
// scores. Dominance pruning already ran, so only the ordering is recomputed.
func rerank(out *RankOutcome) *RankOutcome {
	if out == nil || len(out.Ranked) == 0 {
		return out
	}
	// The survivors are already Pareto-optimal; re-sorting them on the fresh
	// scores cannot resurrect a pruned candidate, which is the point: naturalness
	// may reorder the shortlist but never re-admit a hard failure.
	out.Ranked = sortRanked(out.Ranked)
	return out
}

// paretoPrune removes candidates that another candidate beats on every loss
// dimension and matches or beats on naturalness (plan.md §45). The relation is
// componentwise rather than on the weighted total, because a candidate that
// trades propositional loss for pragmatic gain is genuinely a different offer,
// not a worse one.
func paretoPrune(cands []Candidate) ([]Candidate, []string) {
	if len(cands) <= 1 {
		return cands, nil
	}
	var kept []Candidate
	var drops []string
	for i, a := range cands {
		dominated := false
		for j, b := range cands {
			if i == j {
				continue
			}
			if dominates(b, a) {
				dominated = true
				drops = append(drops, fmt.Sprintf("%q dominated by %q", shortText(a.Text), shortText(b.Text)))
				break
			}
		}
		if !dominated {
			kept = append(kept, a)
		}
	}
	sort.Strings(drops)
	return kept, drops
}

// dominates reports whether a is at least as good as b everywhere and strictly
// better somewhere. Epsilon comparisons keep floating point noise from making
// two equal candidates dominate each other in a cycle.
func dominates(a, b Candidate) bool {
	if !a.Verify.Loss.LessOrEqual(b.Verify.Loss) {
		return false
	}
	if a.Naturalness+1e-9 < b.Naturalness {
		return false
	}
	// Strictness: without it, identical candidates would each delete the other.
	strict := a.Naturalness > b.Naturalness+1e-9
	if !strict {
		for _, dim := range LossDimOrder {
			if a.Verify.Loss.Get(dim) < b.Verify.Loss.Get(dim)-1e-9 {
				strict = true
				break
			}
		}
	}
	return strict
}

// sortRanked orders the shortlist. The keys are, in order: verifier status
// severity, total loss, naturalness (descending), then the original candidate
// order — so ties are deterministic and stable rather than dependent on map
// iteration or on a comparator that is inconsistent for equal elements.
func sortRanked(cands []Candidate) []Candidate {
	idx := map[string]int{}
	for i, c := range cands {
		if _, ok := idx[c.Text]; !ok {
			idx[c.Text] = i
		}
	}
	out := make([]Candidate, len(cands))
	copy(out, cands)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if sa, sb := statusSeverity(a), statusSeverity(b); sa != sb {
			return sa < sb
		}
		if la, lb := a.Verify.Loss.Total(), b.Verify.Loss.Total(); la != lb {
			return la < lb
		}
		if a.Naturalness != b.Naturalness {
			return a.Naturalness > b.Naturalness
		}
		return idx[a.Text] < idx[b.Text]
	})
	return decorate(out)
}

// decorate copies the ranking evidence into each candidate's trace so the UI can
// render "why this one" without recomputing anything. The map is copied, never
// mutated in place: the caller's map belongs to the pipeline, not to us.
func decorate(cands []Candidate) []Candidate {
	for i := range cands {
		cp := map[string]string{}
		for k, val := range cands[i].Trace {
			cp[k] = val
		}
		cp["status"] = cands[i].Verify.Status
		cp["loss.total"] = fmt.Sprintf("%.3f", cands[i].Verify.Loss.Total())
		if dom := cands[i].Verify.Loss.Dominant(); dom != "" {
			cp["loss.dominant"] = dom
		}
		cp["naturalness"] = fmt.Sprintf("%.3f", cands[i].Naturalness)
		cands[i].Trace = cp
	}
	return cands
}

// statusSeverity orders the §61 statuses. Lower is better; the ordering is the
// one plan.md §61 itself uses to talk about "returning something worse than
// nothing", with EXACT best and UNPARSABLE worst.
func statusSeverity(c Candidate) int {
	switch c.Verify.Status {
	case StatusExact:
		return 0
	case StatusGood:
		return 1
	case StatusLossy:
		return 2
	case StatusAmbiguous:
		return 3
	case StatusUnderdetermined:
		return 4
	}
	return 5
}

// applyNaturalness asks the hook for scores and writes them onto the
// shortlist. A score vector of the wrong length, or one carrying NaN, is
// rejected outright rather than partially applied.
func applyNaturalness(ctx context.Context, score NaturalnessFn, cands []Candidate) error {
	if len(cands) == 0 {
		return nil
	}
	got, err := score(ctx, cands)
	if err != nil {
		return err
	}
	if len(got) != len(cands) {
		return fmt.Errorf("naturalness hook returned %d scores for %d candidates", len(got), len(cands))
	}
	for i, s := range got {
		if s != s || s < 0 || s > 1 {
			return fmt.Errorf("naturalness hook returned an out-of-range score %v", s)
		}
		cands[i].Naturalness = s
	}
	return nil
}

func shortText(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:40] + "…"
}
