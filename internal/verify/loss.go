package verify

// Translation loss accounting, plan.md §43.
//
// The system does not claim "loss = 0" unconditionally: Japanese and English
// do not grammaticalize the same information, and pretending otherwise would
// make every honorific look like an error. What the system does instead is say
// *which dimension* of meaning a candidate gave up, so a user can decide
// whether a pragmatic gain is worth a temporal loss.
//
// The six dimensions and the reasoning behind them:
//
//	propositional  predicate, roles, polarity, modality, quantifier, scope.
//	               Losing this changes what the sentence says.
//	referential    which entity a role points at, number, gender, givenness.
//	               Losing this changes who or what the sentence is about.
//	temporal       tense, aspect, completion, temporal relations.
//	pragmatic      register, politeness, respect, humility, speaker attitude.
//	               plan.md §5: an honorific English cannot express is a
//	               pragmatic loss and nothing else.
//	stylistic      realization choice: which pronoun, which word order, which
//	               register marker. plan.md §42 requires natural translations
//	               to survive, so realization preference is never a semantic
//	               error.
//	implicature    what the source conveyed without saying: causation,
//	               evidentiality, conversational implicature.
//
// Weights exist because the dimensions are not commensurable units of loss.
// Total is a weighted sum used for *ordering*; it is never presented to a user
// as "probability of being a good translation", because plan.md §41 forbids
// collapsing a feature-wise comparison into one similarity number.

import "math"

// Loss dimension keys. They double as the JSON field names of LossVector in
// the HTTP contract, so they must stay stable.
const (
	DimPropositional = "propositional"
	DimReferential   = "referential"
	DimTemporal      = "temporal"
	DimPragmatic     = "pragmatic"
	DimStylistic     = "stylistic"
	DimImplicature   = "implicature"
)

// LossWeights is the documented contribution of each dimension to Total.
// Propositional loss dominates because it changes the meaning of the sentence;
// stylistic loss is nearly free because plan.md §42 explicitly requires that
// natural realizations not be discarded on style alone.
var LossWeights = map[string]float64{
	DimPropositional: 1.00,
	DimReferential:   0.85,
	DimTemporal:      0.70,
	DimPragmatic:     0.40,
	DimStylistic:     0.20,
	DimImplicature:   0.55,
}

// LossDimOrder fixes the iteration order of the dimensions. Maps have no order
// and deterministic output is a house rule, so every traversal uses this slice.
var LossDimOrder = []string{
	DimPropositional, DimReferential, DimTemporal,
	DimImplicature, DimPragmatic, DimStylistic,
}

// LossVector is the per-candidate loss profile of plan.md §43. Every component
// is in [0,1] and represents the fraction of that dimension the candidate gave
// up relative to what the source actually encoded.
//
// A zero component means "this dimension was preserved", NOT "this dimension
// was checked". The Diffs slice is the evidence; the vector is the summary.
type LossVector struct {
	Propositional float64 `json:"propositional"`
	Referential   float64 `json:"referential"`
	Temporal      float64 `json:"temporal"`
	Pragmatic     float64 `json:"pragmatic"`
	Stylistic     float64 `json:"stylistic"`
	Implicature   float64 `json:"implicature"`
}

// Get returns the component for a dimension key, or 0 for an unknown key.
// Unknown keys return 0 rather than a default so that a typo in a dimension
// name shows up as a missing loss rather than an inflated one.
func (l LossVector) Get(dim string) float64 {
	switch dim {
	case DimPropositional:
		return l.Propositional
	case DimReferential:
		return l.Referential
	case DimTemporal:
		return l.Temporal
	case DimPragmatic:
		return l.Pragmatic
	case DimStylistic:
		return l.Stylistic
	case DimImplicature:
		return l.Implicature
	}
	return 0
}

// With returns a copy of l with the named dimension set to amount. The
// component is clamped to [0,1] so a long chain of soft diffs cannot produce a
// "loss" of 4.0 and swamp the ranking.
func (l LossVector) With(dim string, amount float64) LossVector {
	if amount < 0 || math.IsNaN(amount) {
		amount = 0
	}
	if amount > 1 {
		amount = 1
	}
	switch dim {
	case DimPropositional:
		l.Propositional = amount
	case DimReferential:
		l.Referential = amount
	case DimTemporal:
		l.Temporal = amount
	case DimPragmatic:
		l.Pragmatic = amount
	case DimStylistic:
		l.Stylistic = amount
	case DimImplicature:
		l.Implicature = amount
	}
	return l
}

// Add charges amount to one dimension, keeping the component in [0,1]. It is
// how the verifier accumulates loss: several soft diffs in the same dimension
// add up, but the sum saturates at 1 because "lost the whole dimension" is the
// worst a dimension can be lost.
func (l *LossVector) Add(dim string, amount float64) {
	if l == nil || amount <= 0 || math.IsNaN(amount) {
		return
	}
	cur := l.Get(dim) + amount
	if cur > 1 {
		cur = 1
	}
	*l = l.With(dim, cur)
}

// Total is the weighted sum used to order candidates. It is a ranking key, not
// a quality probability: the UI shows the six components separately so a user
// can see that a candidate bought naturalness with a temporal loss.
func (l LossVector) Total() float64 {
	sum := 0.0
	for _, dim := range LossDimOrder {
		w, ok := LossWeights[dim]
		if !ok {
			w = 0
		}
		sum += l.Get(dim) * w
	}
	return round3(sum)
}

// Max returns the largest component, ignoring zero dimensions.
func (l LossVector) Max() float64 {
	m := 0.0
	for _, dim := range LossDimOrder {
		if v := l.Get(dim); v > m {
			m = v
		}
	}
	return round3(m)
}

// Dominant names the dimension that gave up the most, or "" when nothing was
// lost. Ties resolve in the fixed order of LossDimOrder so the answer is stable
// across runs; two runs of the same input must not disagree about what the
// main problem with a candidate was.
func (l LossVector) Dominant() string {
	best, bestV := "", -1.0
	for _, dim := range LossDimOrder {
		v := l.Get(dim)
		if v > bestV+1e-9 {
			best, bestV = dim, v
		}
	}
	if bestV <= 0 {
		return ""
	}
	return best
}

// Less reports whether l is a strictly better loss profile than o, using the
// weighted Total. Strictness comes from the epsilon so that two candidates
// which differ by floating point noise compare equal rather than flapping.
func (l LossVector) Less(o LossVector) bool { return l.Total() < o.Total()-1e-9 }

// LessOrEqual reports l <= o componentwise, which is the relation plan.md §45
// uses for Pareto dominance. Note that dominance is componentwise, not on
// Total: a candidate that trades propositional loss for pragmatic gain is not
// dominated by one that has the better total but a worse propositional score.
func (l LossVector) LessOrEqual(o LossVector) bool {
	for _, dim := range LossDimOrder {
		if l.Get(dim) > o.Get(dim)+1e-9 {
			return false
		}
	}
	return true
}

// IsZero reports whether nothing at all was lost.
func (l LossVector) IsZero() bool { return l.Max() <= 0 }

// round3 keeps JSON output stable across platforms. Loss values are reported to
// the user, and 0.17999999999999999 in a diff view is noise, not information.
func round3(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return math.Round(f*1000) / 1000
}
