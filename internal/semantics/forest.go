package semantics

// The source semantic forest (plan.md §§23, 58).
//
// A sentence with residual ambiguity yields several graphs, not one merged
// soup. Two independent sources of ambiguity feed this forest:
//
//	parse ambiguity   syntax.Clause.Alternatives — competing attachments or
//	                  scope analyses of one clause span;
//	predicate sense  one surface verb reaching several ontology senses.
//
// Each combination becomes a Reading with a weight, the weights are normalized
// and sorted, and the top few survive. A sentence with no ambiguity yields
// exactly one reading, because manufacturing a forest where none exists would
// be noise pretending to be caution.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/lexicon"
	"github.com/nico2525nn/jev-trans/internal/ontology"
	"github.com/nico2525nn/jev-trans/internal/syntax"
)

// maxReadings bounds the forest. plan.md §32 caps an oracle question at 255
// options; a forest far larger than that cannot be resolved anyway, and the
// whole point is that the ambiguity is preserved *and* actionable.
const maxReadings = 8

// Reading is one JLIR interpretation of the sentence together with the weight
// that makes it more or less likely than its siblings.
type Reading struct {
	Graph *jlir.Graph `json:"graph"`
	// Weight is the normalized posterior over readings. The readings sum to 1.
	Weight float64 `json:"weight"`
	// Origin says what the reading varies over: "parse", "sense", or both. It is
	// shown in the UI so the user can see what is still open.
	Origin string `json:"origin"`
}

// Forest is the weighted set of readings of one sentence.
type Forest struct {
	Lang     lang.Lang `json:"lang"`
	Source   string    `json:"source"`
	Readings []Reading `json:"readings"`
}

// BuildForest combines parse ambiguity with predicate-sense ambiguity into
// weighted readings.
//
// The readings are normalized to sum to one and sorted by descending weight.
// At most maxReadings survive; a sentence with no ambiguity yields exactly one.
// BuildForest turns a parse into every reading the analysis admits.
//
// prov is the chain that supplies predicate knowledge. Passing nil installs the
// curated table, so the common case stays a one-argument call while an operator
// that has an external inventory can name it at configuration time rather than
// have it discovered from the environment on every sentence.
func BuildForest(b *syntax.Bundle, src lang.Lang, prov *lexicon.Set) *Forest {
	if prov == nil {
		prov = lexicon.NewSet(lexicon.NewTableProvider(lexicon.Default()))
	}
	f := &Forest{Lang: src}
	if b != nil {
		f.Source = b.Source
	}
	if src == "" && b != nil {
		f.Lang = b.Lang
	}

	// The base reading always exists: an empty bundle or a bundle with no clause
	// must still produce a usable graph rather than an error downstream.
	base, _ := buildWith(b, src, nil, nil, prov)
	if base == nil {
		base = unparsableGraph(b, src, "no reading could be constructed")
	}
	base.Weight = 1
	readings := []Reading{{Graph: base, Weight: 1, Origin: "base"}}

	variants := enumerateVariants(b, src, prov)
	for _, v := range variants {
		g, _ := buildWith(b, src, v.overrides, v.senses, prov)
		if g == nil {
			continue
		}
		g.Weight = v.weight
		readings = append(readings, Reading{Graph: g, Weight: v.weight, Origin: v.origin})
	}

	readings = dedupeReadings(readings)
	normalizeWeights(readings)
	sort.SliceStable(readings, func(i, j int) bool {
		if readings[i].Weight != readings[j].Weight {
			return readings[i].Weight > readings[j].Weight
		}
		return readings[i].Origin < readings[j].Origin
	})
	if len(readings) > maxReadings {
		readings = readings[:maxReadings]
	}
	for i := range readings {
		readings[i].Graph.Weight = readings[i].Weight
		// A forest member is an interpretation under consideration, not a
		// committed reading, and the UI should say so.
		readings[i].Graph.Structural = true
	}
	if len(readings) > 0 {
		readings[0].Graph.Weight = 1
	}
	f.Readings = readings
	if len(readings) > 1 {
		for _, r := range readings[1:] {
			if r.Graph != nil {
				r.Graph.Notes = append(r.Graph.Notes, fmt.Sprintf(
					"reading of weight %.3f; a sibling reading with weight %.3f is still open",
					r.Weight, readings[0].Weight))
			}
		}
	}
	return f
}

// variant is one way of combining a parse alternative with a predicate sense.
type variant struct {
	overrides map[string]*syntax.Clause
	senses    map[string]string
	weight    float64
	origin    string
}

// enumerateVariants produces every alternative reading the bundle licenses.
//
// The product of "clause alternatives" and "predicate senses" is kept bounded:
// a clause contributes at most one alternative and a predicate at most three
// senses, so the enumeration stays small enough to be useful rather than
// combinatorial.
func enumerateVariants(b *syntax.Bundle, src lang.Lang, prov *lexicon.Set) []variant {
	if b == nil {
		return nil
	}
	if src == "" {
		src = b.Lang
	}
	ja := src == lang.JA
	lex := prov
	onto := ontology.Default()

	var out []variant

	// Parse ambiguity: for each clause with an alternative reading, substitute
	// that alternative and see what it yields.
	for _, c := range b.Clauses {
		if c == nil || len(c.Alternatives) == 0 {
			continue
		}
		for _, alt := range c.Alternatives {
			if alt == nil {
				continue
			}
			w := alt.Probability
			if w <= 0 {
				w = 0.5
			}
			out = append(out, variant{
				overrides: map[string]*syntax.Clause{c.ID: alt},
				weight:    w * 0.9,
				origin:    "clause " + c.ID + " alternative " + alt.ID,
			})
		}
	}

	// Predicate-sense ambiguity: one surface verb, several ontology senses.
	for _, c := range b.Clauses {
		if c == nil {
			continue
		}
		m := b.Morph(c.Matrix)
		if m == nil {
			continue
		}
		surface := strings.TrimSpace(m.Lem)
		if surface == "" {
			surface = strings.TrimSpace(m.Base)
		}
		if surface == "" {
			continue
		}
		ids := senseIDs(lex, surface, ja)
		if len(ids) < 2 {
			continue
		}
		weights := senseWeights(lex, surface, ja)
		kept := 0
		for _, id := range ids {
			if kept >= 3 {
				break
			}
			if _, ok := onto.Sense(id); !ok {
				continue
			}
			w := weights[id]
			if w <= 0 {
				w = 0.4
			}
			out = append(out, variant{
				senses: map[string]string{c.ID: id},
				weight: w * 0.9,
				origin: fmt.Sprintf("clause %s read as %s", c.ID, id),
			})
			kept++
		}
	}
	return out
}

// senseWeights sums the lexicon's weights per sense id.
func senseWeights(l lexicon.Provider, surface string, ja bool) map[string]float64 {
	out := map[string]float64{}
	if l == nil {
		return out
	}
	var hits []lexicon.SenseHit
	if ja {
		hits = l.SensesJP(surface)
		if len(hits) == 0 {
			hits = l.SensesEN(surface)
		}
	} else {
		hits = l.SensesEN(surface)
		if len(hits) == 0 {
			hits = l.SensesJP(surface)
		}
	}
	for _, h := range hits {
		w := h.Weight
		if w <= 0 {
			w = 0.05
		}
		out[h.SenseID] += w
	}
	return out
}

// dedupeReadings drops readings that say exactly the same thing. Two readings
// that differ only in an id number are not ambiguity.
func dedupeReadings(in []Reading) []Reading {
	seen := map[string]bool{}
	out := make([]Reading, 0, len(in))
	for _, r := range in {
		if r.Graph == nil {
			continue
		}
		key := readingKey(r.Graph)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	if len(out) == 0 {
		for _, r := range in {
			if r.Graph != nil {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// readingKey is the content signature of a graph: which predicates, which roles,
// which scope labels. Entity ids are excluded on purpose, because renumbering is
// not a different reading.
func readingKey(g *jlir.Graph) string {
	var sb strings.Builder
	preds := make([]string, 0, len(g.Events))
	for _, v := range g.Events {
		preds = append(preds, fmt.Sprintf("%s:%s:%s", v.Predicate, v.Tense, v.Polarity))
	}
	sort.Strings(preds)
	sb.WriteString(strings.Join(preds, "|"))
	sb.WriteString("#")
	roles := make([]string, 0, len(g.Events))
	for _, v := range g.Events {
		for _, role := range v.OrderArgs() {
			roles = append(roles, role+"="+string(v.Args[role].Value))
		}
	}
	sort.Strings(roles)
	sb.WriteString(strings.Join(roles, "|"))
	sb.WriteString("#")
	labels := make([]string, 0, len(g.Scopes))
	for _, s := range g.Scopes {
		for _, rd := range s.Readings {
			labels = append(labels, rd.Label)
		}
	}
	sort.Strings(labels)
	sb.WriteString(strings.Join(labels, "|"))
	return sb.String()
}

// normalizeWeights makes the weights a posterior: they sum to one, and a single
// reading is certain rather than merely probable.
func normalizeWeights(rs []Reading) {
	if len(rs) == 0 {
		return
	}
	if len(rs) == 1 {
		rs[0].Weight = 1
		return
	}
	total := 0.0
	for _, r := range rs {
		if r.Weight < 0 {
			r.Weight = 0
		}
		total += r.Weight
	}
	if total <= 0 {
		uniform := 1 / float64(len(rs))
		for i := range rs {
			rs[i].Weight = uniform
		}
		return
	}
	for i := range rs {
		rs[i].Weight /= total
	}
}
