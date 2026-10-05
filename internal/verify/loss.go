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
// The dimensions are not commensurable, so a scalar summary needs weights — but
// plan.md §43 closes with 「最終候補選択では、単一scoreではなくこのvectorを
// 考慮する」: final selection considers the vector, not one score. The candidate
// ordering in rank.go is therefore componentwise (see LossVector.Compare) and
// the weights below never decide anything. They exist only so that Total is a
// defined number where one is genuinely wanted: the Jev request context and the
// "loss.total" trace label.
//
// The numbers are an ordinal encoding of the §45 dominance tiers, not
// measurements. §45 states the ordering explicitly — semantic loss, then
// pragmatic loss, then style loss — so the weights carry tier membership and
// nothing finer: the four semantic components all weigh the same, and two
// candidates differing only inside a tier get the same Total and are separated
// by Compare.
//
// They are a constant, never a knob: lossWeight is a switch, so no caller can
// retune the ranking by writing to a package-level map.

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

// LossDimensionOrder is the fixed traversal order of the loss dimensions, from
// the dimension that changes the most to the one that changes the least.
// Compare (plan.md §43) reads the vector in this order and every other
// traversal uses it too, so two runs never disagree about a tie. A map has no
// order and deterministic output is a house rule, so the order lives here and
// nowhere else.
//
// The tiers are ordered apart: the four semantic dimensions come before the
// pragmatic one, which comes before the stylistic one, because §45 lists
// dominance in exactly that order and §42 says a realization preference is
// never a reason to discard a translation.
func LossDimensionOrder() []string {
	return []string{
		DimPropositional, DimReferential, DimTemporal,
		DimImplicature, DimPragmatic, DimStylistic,
	}
}

// lossWeight is the constant contribution of one dimension to Total.
func lossWeight(dim string) float64 {
	switch dim {
	case DimPropositional, DimReferential, DimTemporal, DimImplicature:
		return 1.00
	case DimPragmatic:
		return 0.40
	case DimStylistic:
		return 0.20
	}
	return 0
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

// Total is the weighted sum of the vector. It is a *summary*, not a ranking
// key: plan.md §43 requires final selection to consider the vector, so
// sortRanked uses Compare, and Total survives only for the trace label and the
// context handed to the oracle. It is never a quality probability — §41
// forbids collapsing a feature-wise comparison into one similarity number.
func (l LossVector) Total() float64 {
	sum, wsum := 0.0, 0.0
	for _, dim := range LossDimensionOrder() {
		w := lossWeight(dim)
		sum += l.Get(dim) * w
		wsum += w
	}
	if wsum == 0 {
		return 0
	}
	return round3(sum / wsum)
}

// Max returns the largest component, ignoring zero dimensions.
func (l LossVector) Max() float64 {
	m := 0.0
	for _, dim := range LossDimensionOrder() {
		if v := l.Get(dim); v > m {
			m = v
		}
	}
	return round3(m)
}

// MaxOutside returns the largest component that is not DimStylistic.
//
// §42 requires that a realization preference — which pronoun, which word order,
// whether a role was relabelled — never counts against a translation: it is a
// correct realization, not a defect. GOOD/LOSSY therefore keys off this value,
// not off a weighted total, because a scalar threshold would have to guess how
// much style is worth and §43 says the dimensions are not commensurable.
func (l LossVector) MaxOutside(dim string) float64 {
	m := 0.0
	for _, d := range LossDimensionOrder() {
		if d == dim {
			continue
		}
		if v := l.Get(d); v > m {
			m = v
		}
	}
	return m
}

// Dominant names the dimension that gave up the most, or "" when nothing was
// lost. Ties resolve in the fixed order of LossDimensionOrder so the answer is
// stable across runs; two runs of the same input must not disagree about what
// the main problem with a candidate was.
func (l LossVector) Dominant() string {
	best, bestV := "", -1.0
	for _, dim := range LossDimensionOrder() {
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

// Compare orders two loss profiles componentwise, in the dimension order of
// LossDimensionOrder, and returns -1 when l is the better profile, +1 when o
// is, and 0 when they are indistinguishable. Lower loss is better.
//
// This is plan.md §43's 「単一scoreではなくこのvectorを考慮する」 made
// executable: the most meaning-changing dimension decides, and only when it
// ties does the next one speak. An epsilon keeps floating point noise from
// making two equal candidates disagree, which sort.SliceStable would turn into
// an arbitrary reordering.
func (l LossVector) Compare(o LossVector) int {
	for _, dim := range LossDimensionOrder() {
		a, b := l.Get(dim), o.Get(dim)
		switch {
		case a < b-1e-9:
			return -1
		case a > b+1e-9:
			return 1
		}
	}
	return 0
}

// LessOrEqual reports l <= o componentwise, which is the relation plan.md §45
// uses for Pareto dominance. Note that dominance is componentwise, not on
// Total: a candidate that trades propositional loss for pragmatic gain is not
// dominated by one that has the better total but a worse propositional score.
func (l LossVector) LessOrEqual(o LossVector) bool {
	for _, dim := range LossDimensionOrder() {
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
