package jlir

import (
	"math"
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/lang"
)

// Distribution is an explicit probability distribution over a small closed
// option set. plan.md §16 makes this a first class citizen: internal state is
// a constraint graph plus a distribution, never a single guess.
//
// An empty Distribution means "not decided", which is distinct from a
// distribution concentrated on one option.
type Distribution struct {
	Options []string           `json:"options"`
	Prob    map[string]float64 `json:"prob"`
	Entropy float64            `json:"entropy"`
	// Resolved marks a distribution the pipeline has committed to. An
	// unresolved distribution must survive into realization as ambiguity.
	Resolved bool   `json:"resolved"`
	Winner   string `json:"winner,omitempty"`
	// Provenance explains where the probabilities came from: "jev", "prior",
	// "lexicon" or "user".
	Provenance string `json:"provenance,omitempty"`
}

// Uniform builds a uniform distribution over opts.
func Uniform(opts ...string) *Distribution {
	d := &Distribution{Options: append([]string(nil), opts...), Prob: map[string]float64{}}
	if len(opts) == 0 {
		return d
	}
	p := 1 / float64(len(opts))
	for _, o := range opts {
		d.Prob[o] = p
	}
	d.recompute()
	return d
}

// NewDistribution builds a distribution from raw weights, normalizing them.
// Zero total weight yields a uniform distribution so the graph never carries
// NaN probabilities into the verifier.
func NewDistribution(source string, weights map[string]float64) *Distribution {
	d := &Distribution{Prob: map[string]float64{}, Provenance: source}
	total := 0.0
	// Every key is an option, even a zero-weighted one: a caller that offers a
	// candidate has offered it, and dropping it silently changes the question
	// the oracle is asked. Filtering first meant an all-zero map produced an
	// OPTIONLESS distribution rather than the uniform one this function
	// documents, and lost the provenance string on the way.
	for k, v := range weights {
		if v < 0 {
			v = 0
		}
		d.Options = append(d.Options, k)
		total += v
	}
	sort.Strings(d.Options)
	if total == 0 {
		for i, o := range d.Options {
			d.Prob[o] = 1 / float64(len(d.Options)*max(1, i+1))
		}
		d.Normalize()
		d.Provenance = source
		return d
	}
	sort.Strings(d.Options)
	for _, k := range d.Options {
		d.Prob[k] = weights[k] / total
	}
	d.recompute()
	return d
}

// Merge folds another distribution into this one as a product of experts.
//
// It was removed rather than fixed: it added a logarithm into a field that
// holds a probability, which is only correct for a distribution already storing
// log weights (something the type never stated and no caller produced), and it
// had zero callers. Combination of a prior with an oracle posterior belongs in
// one place with one documented arithmetic; see discourse.Combine.
// Normalize rescales probabilities to sum to one.
func (d *Distribution) Normalize() {
	total := 0.0
	for _, o := range d.Options {
		total += d.Prob[o]
	}
	if total <= 0 {
		return
	}
	for _, o := range d.Options {
		d.Prob[o] /= total
	}
	d.recompute()
}

func (d *Distribution) recompute() {
	d.Entropy = 0
	best, bestP := "", -1.0
	for _, o := range d.Options {
		p := d.Prob[o]
		if p > 0 {
			d.Entropy -= p * math.Log(p)
		}
		if p > bestP {
			bestP, best = p, o
		}
	}
	d.Winner = best
	if best != "" && d.Entropy < 1e-9 {
		d.Prob[best] = 1
	}
}

// CommitTo installs a point mass on option, keeping the invariants that
// recompute() maintains. External writers previously assigned Options, Prob,
// Winner and Resolved by hand and left Entropy stale, so a user-answered
// distribution shipped with the pre-answer entropy in its JSON.
func (d *Distribution) CommitTo(option string) {
	if d == nil {
		return
	}
	if _, ok := d.Prob[option]; !ok {
		option = d.Winner
	}
	d.Prob = map[string]float64{option: 1}
	d.Options = []string{option}
	d.recompute()
	d.Resolved = true
}

// EntropyBits returns the entropy in bits, which is the unit the UI shows.
func (d *Distribution) EntropyBits() float64 {
	if d == nil {
		return 0
	}
	return d.Entropy / math.Ln2
}

// Commit marks the distribution resolved onto its argmax and drops the rest.
func (d *Distribution) Commit() {
	if d == nil || d.Winner == "" {
		return
	}
	d.Prob = map[string]float64{d.Winner: 1}
	d.Options = []string{d.Winner}
	d.Entropy = 0
	d.Resolved = true
}

// P returns the probability of an option.
func (d *Distribution) P(option string) float64 {
	if d == nil {
		return 0
	}
	return d.Prob[option]
}

// Ranked is one option with its probability, as returned by Top.
type Ranked struct {
	Option string
	P      float64
}

// Top returns the k highest probability options.
func (d *Distribution) Top(k int) []Ranked {
	if d == nil {
		return nil
	}
	all := make([]Ranked, 0, len(d.Options))
	for _, o := range d.Options {
		all = append(all, Ranked{o, d.Prob[o]})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].P != all[j].P {
			return all[i].P > all[j].P
		}
		return all[i].Option < all[j].Option
	})
	if k > 0 && len(all) > k {
		all = all[:k]
	}
	return all
}

// Margin is the probability gap between the best and second best option; it is
// the number the information-gain scheduler uses to decide whether an oracle
// call can possibly change the outcome.
func (d *Distribution) Margin() float64 {
	t := d.Top(2)
	if len(t) < 2 {
		if len(t) == 1 {
			return t[0].P
		}
		return 0
	}
	return t[0].P - t[1].P
}

// Ambiguous reports whether a decision still matters: more than one option
// carries non-trivial mass.
func (d *Distribution) Ambiguous(threshold float64) bool {
	if d == nil || len(d.Options) < 2 {
		return false
	}
	live := 0
	for _, o := range d.Options {
		if d.Prob[o] >= threshold {
			live++
		}
	}
	return live > 1
}

func (d *Distribution) String() string {
	if d == nil {
		return "<nil>"
	}
	parts := make([]string, 0, len(d.Options))
	for _, o := range d.Options {
		parts = append(parts, o+"="+ValueString(d.Prob[o]))
	}
	return strings.Join(parts, " ")
}

// --- referential helpers --------------------------------------------------

// Alias returns the most recent surface form for this entity, preferring a
// form in the requested language. This is what realizes 「彼」 as "he" or
// "Yamada" without the realizer ever consulting a gender field it has not
// been given.
func (e *Entity) Alias(l lang.Lang) string {
	for i := len(e.Aliases) - 1; i >= 0; i-- {
		if e.Aliases[i].Lang == l {
			return e.Aliases[i].Surface
		}
	}
	if len(e.Aliases) > 0 {
		return e.Aliases[len(e.Aliases)-1].Surface
	}
	return ""
}

// Name returns the shortest proper-noun style alias in l, else the empty string.
func (e *Entity) Name(l lang.Lang) string {
	best := ""
	for _, a := range e.Aliases {
		if a.Kind != "name" && a.Kind != "translation" {
			continue
		}
		if a.Lang != l {
			continue
		}
		if best == "" || len(a.Surface) < len(best) {
			best = a.Surface
		}
	}
	return best
}

// Mention returns the highest mention count among this entity's aliases.
func (e *Entity) GenderValue() string {
	if v, ok := e.Feature("gender"); ok {
		return ValueString(v.Value)
	}
	return e.Gender
}

// Feature returns the first feature with the given key.
func (e *Entity) Feature(key string) (Feature, bool) {
	for _, f := range e.Features {
		if f.Key == key {
			return f, true
		}
	}
	return Feature{}, false
}

// SetFeature upserts a feature, merging provenance.
func (e *Entity) SetFeature(f Feature) {
	for i := range e.Features {
		if e.Features[i].Key == f.Key {
			e.Features[i] = f
			return
		}
	}
	e.Features = append(e.Features, f)
}

// Feature returns the first feature with the given key on the event.
func (e *Event) Feature(key string) (Feature, bool) {
	for _, f := range e.Features {
		if f.Key == key {
			return f, true
		}
	}
	return Feature{}, false
}

// SetFeature upserts an event feature.
func (e *Event) SetFeature(f Feature) {
	for i := range e.Features {
		if e.Features[i].Key == f.Key {
			e.Features[i] = f
			return
		}
	}
	e.Features = append(e.Features, f)
}

// Unresolved reports the number of positions still carrying real ambiguity:
// zero referents, unresolved scope readings, non-extreme event confidences.
func (g *Graph) Unresolved() []string {
	var out []string
	for _, e := range g.Entities {
		if e.Referent != nil && !e.Referent.Resolved && len(e.Referent.Options) > 1 {
			out = append(out, string(e.ID)+".referent")
		}
	}
	for _, s := range g.Scopes {
		if !s.Resolved && len(s.Readings) > 1 {
			out = append(out, string(s.ID)+".scope")
		}
	}
	for _, v := range g.Events {
		for role, a := range v.Args {
			if a.Confidence > 0 && a.Confidence < 0.95 {
				out = append(out, string(v.ID)+"."+role)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Entropy returns the mean normalized entropy over all open distributions; it
// is the graph-level ambiguity measure shown in the UI.
func (g *Graph) Entropy() float64 {
	var sum float64
	var n int
	for _, e := range g.Entities {
		if e.Referent != nil && len(e.Referent.Options) > 1 {
			sum += e.Referent.Entropy
			n++
		}
	}
	for _, s := range g.Scopes {
		if len(s.Readings) > 1 {
			sum += s.Entropy()
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// Entropy returns the entropy of the scope readings, in bits.
func (s *ScopeNode) Entropy() float64 {
	if len(s.Readings) <= 1 {
		return 0
	}
	h := 0.0
	for _, r := range s.Readings {
		if r.Weight > 0 {
			h -= r.Weight * math.Log2(r.Weight)
		}
	}
	return h
}

// ActiveReading returns the reading currently in force, or nil.
func (s *ScopeNode) ActiveReading() *ScopeReading {
	if len(s.Readings) == 0 {
		return nil
	}
	best := 0
	for i, r := range s.Readings {
		if r.Weight > s.Readings[best].Weight {
			best = i
		}
	}
	return &s.Readings[best]
}

// ScopeOrder returns the effective linearization of a scope graph starting at
// root, honouring the active readings. Unresolved scopes linearize via their
// highest weight reading but keep Resolved false so the ambiguity survives.
func (g *Graph) ScopeOrder(root ID) []string {
	var out []string
	seen := map[ID]bool{}
	var walk func(id ID, depth int)
	walk = func(id ID, depth int) {
		if seen[id] || depth > 32 {
			return
		}
		seen[id] = true
		node := g.Scope(id)
		if node == nil {
			out = append(out, string(id))
			return
		}
		out = append(out, string(id))
		order := node.Operands
		if r := node.ActiveReading(); r != nil && len(r.Order) > 0 {
			order = r.Order
		}
		for _, o := range order {
			walk(ID(o), depth+1)
		}
	}
	walk(root, 0)
	return out
}

// Roots returns scope nodes nothing else dominates.
func (g *Graph) ScopeRoots() []ID {
	dominated := map[ID]bool{}
	for _, s := range g.Scopes {
		if r := s.ActiveReading(); r != nil {
			for _, o := range r.Order {
				dominated[ID(o)] = true
			}
		}
		for _, o := range s.Operands {
			dominated[ID(o)] = true
		}
	}
	var out []ID
	for _, s := range g.Scopes {
		if !dominated[s.ID] {
			out = append(out, s.ID)
		}
	}
	return out
}

// DeepCopy clones the graph structurally. Candidate scoring must never mutate
// the source interpretation.
func (g *Graph) DeepCopy() *Graph {
	if g == nil {
		return nil
	}
	c := *g
	c.Entities = make([]*Entity, len(g.Entities))
	for i, e := range g.Entities {
		ec := *e
		ec.Aliases = append([]Alias(nil), e.Aliases...)
		ec.Features = append([]Feature(nil), e.Features...)
		ec.Prov = append([]Provenance(nil), e.Prov...)
		if e.Referent != nil {
			rd := *e.Referent
			rd.Options = append([]string(nil), e.Referent.Options...)
			rd.Prob = map[string]float64{}
			for k, v := range e.Referent.Prob {
				rd.Prob[k] = v
			}
			ec.Referent = &rd
		}
		c.Entities[i] = &ec
	}
	c.Events = make([]*Event, len(g.Events))
	for i, v := range g.Events {
		vc := *v
		vc.Args = map[string]Arg{}
		for k, a := range v.Args {
			ac := a
			ac.Prov = append([]Provenance(nil), a.Prov...)
			vc.Args[k] = ac
		}
		vc.Features = append([]Feature(nil), v.Features...)
		vc.Prov = append([]Provenance(nil), v.Prov...)
		c.Events[i] = &vc
	}
	c.Scopes = make([]*ScopeNode, len(g.Scopes))
	for i, s := range g.Scopes {
		sc := *s
		sc.Operands = append([]string(nil), s.Operands...)
		sc.Readings = append([]ScopeReading(nil), s.Readings...)
		sc.Prov = append([]Provenance(nil), s.Prov...)
		c.Scopes[i] = &sc
	}
	c.Preds = append([]*Predicate(nil), g.Preds...)
	c.SourceFeat.Idioms = append([]IdiomHypothesis(nil), g.SourceFeat.Idioms...)
	c.SourceFeat.Unknowns = append([]UnknownUnit(nil), g.SourceFeat.Unknowns...)
	c.SourceFeat.Constructions = append([]string(nil), g.SourceFeat.Constructions...)
	c.Info.Topic = append([]ID(nil), g.Info.Topic...)
	c.Info.Focus = append([]ID(nil), g.Info.Focus...)
	c.ClauseOrder = append([]ID(nil), g.ClauseOrder...)
	c.Aliases = map[string]ID{}
	for k, v := range g.Aliases {
		c.Aliases[k] = v
	}
	c.NamedEntities = map[string]NamedEntity{}
	for k, v := range g.NamedEntities {
		c.NamedEntities[k] = v
	}
	return &c
}

// ResolveZero binds every zero entity whose referent distribution has been
// committed to the single remaining option. Returns false when some zeros are
// still ambiguous, which the pipeline reports as AMBIGUOUS/UNDERDETERMINED
// rather than silently guessing.
func (g *Graph) ResolveZero() bool {
	complete := true
	for _, e := range g.Entities {
		if !e.Zero || e.Referent == nil {
			continue
		}
		if !e.Referent.Resolved {
			complete = false
			continue
		}
		for _, v := range g.Events {
			for role, a := range v.Args {
				if a.Value == e.ID {
					v.Args[role] = Arg{Value: ID(e.Referent.Winner), Confidence: e.Referent.P(e.Referent.Winner), Prov: append([]Provenance(nil), a.Prov...)}
				}
			}
		}
	}
	return complete
}
