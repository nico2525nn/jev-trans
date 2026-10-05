package jlir

import (
	"fmt"
	"sort"
	"strings"
)

// ProvenedTrace is one hop in a target-span backtrace, satisfying plan.md §60:
// any target span must lead back to construction, event, feature, source token
// and the oracle decision that made it.
type ProvenedTrace struct {
	Target       string   `json:"target"`
	EventID      ID       `json:"event,omitempty"`
	Role         string   `json:"role,omitempty"`
	EntityID     ID       `json:"entity,omitempty"`
	Construction string   `json:"construction,omitempty"`
	Feature      string   `json:"feature,omitempty"`
	SourceToken  string   `json:"sourceToken,omitempty"`
	Decision     string   `json:"decision,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

// Upstream returns true when a provenance entry points back at the source
// text or at a decision the system is willing to stand behind. Target
// derived origins do NOT count: they are exactly what the hallucination check
// is meant to catch.
func (p Provenance) Upstream() bool {
	switch p.Origin {
	case OriginLexical, OriginMorphological, OriginSyntactic, OriginScope,
		OriginDiscourse, OriginDecision, OriginOntology, OriginUser:
		return true
	}
	return false
}

// Backtrace produces the human-readable chain used by the UI's provenance
// panel. constructor receives the construction id chosen for an event so the
// trace can be reported without this package depending on the planner.
func (g *Graph) Backtrace(eventID ID, role string, constructor func(ID) string) ProvenedTrace {
	t := ProvenedTrace{EventID: eventID, Role: role}
	v := g.Event(eventID)
	if v == nil {
		t.Notes = append(t.Notes, "no such event")
		return t
	}
	t.Target = string(eventID) + "." + role
	if constructor != nil {
		t.Construction = constructor(eventID)
	}
	if role != "" {
		if a, ok := v.Args[role]; ok {
			ent := g.Entity(a.Value)
			if ent != nil {
				t.EntityID = ent.ID
				t.SourceToken = ent.Alias(g.Lang)
			}
			for _, p := range a.Prov {
				if p.DecisionID != "" {
					t.Decision = p.DecisionID
				}
			}
		}
	}
	for _, f := range v.Features {
		for _, p := range f.Prov {
			if p.DecisionID != "" {
				t.Feature = f.Key
				t.Decision = p.DecisionID
			}
		}
	}
	return t
}

// UnsupportedFeature is a feature asserted in g that has no path back to any
// upstream source. This is the machine check behind plan.md §22 and the
// UNPARSABLE / UNSUPPORTED failure modes of §61.
type UnsupportedFeature struct {
	Owner   string   `json:"owner"`
	Key     string   `json:"key"`
	Value   string   `json:"value"`
	Origins []Origin `json:"origins"`
	Reason  string   `json:"reason"`
}

// SensitiveKeys are the features whose invention is most damaging, and which
// the pipeline is most careful about. Gender is first because it is the plan's
// canonical example ("The female teacher came.").
var SensitiveKeys = []string{"gender", "number", "age", "nationality", "profession_specific", "name"}

// IsSensitive reports whether inventing the key would be a hallucination of a
// protected property.
func IsSensitive(key string) bool {
	for _, k := range SensitiveKeys {
		if k == key {
			return true
		}
	}
	return false
}

// UnsupportedFeatures scans every entity and event feature and returns those
// whose provenance never reaches an upstream origin. Features with no
// provenance at all are reported too, because "we do not know" and "we made it
// up" must not be indistinguishable.
func (g *Graph) UnsupportedFeatures() []UnsupportedFeature {
	var out []UnsupportedFeature
	check := func(owner string, f Feature) {
		if len(f.Prov) == 0 {
			out = append(out, UnsupportedFeature{
				Owner: owner, Key: f.Key, Value: ValueString(f.Value),
				Reason: "asserted without any provenance",
			})
			return
		}
		origins := make([]Origin, 0, len(f.Prov))
		upstream := false
		for _, p := range f.Prov {
			origins = append(origins, p.Origin)
			if p.Upstream() {
				upstream = true
			}
		}
		if !upstream {
			out = append(out, UnsupportedFeature{
				Owner: owner, Key: f.Key, Value: ValueString(f.Value), Origins: origins,
				Reason: "provenance ends at the target side; no path to the source",
			})
		}
	}
	for _, e := range g.Entities {
		for _, f := range e.Features {
			check(string(e.ID), f)
		}
	}
	for _, v := range g.Events {
		for _, f := range v.Features {
			check(string(v.ID), f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// InventedGender reports entities whose gender feature was added on the target
// side only. The pipeline uses this to prune "he"/"she" branches during
// constraint propagation (plan.md §39) instead of generating and rejecting
// them afterwards.
func (g *Graph) InventedGender() []string {
	var out []string
	for _, e := range g.Entities {
		f, ok := e.Feature("gender")
		if !ok {
			continue
		}
		upstream := false
		for _, p := range f.Prov {
			if p.Upstream() {
				upstream = true
			}
		}
		if !upstream {
			out = append(out, string(e.ID))
		}
	}
	return out
}

// ReferentDistribution returns the current referent distribution for the
// entity bound to role r of event v, walking through zero placeholders.
func (g *Graph) ReferentDistribution(v *Event, role string) *Distribution {
	a, ok := v.Args[role]
	if !ok {
		return nil
	}
	e := g.Entity(a.Value)
	if e == nil {
		return nil
	}
	if e.Zero && e.Referent != nil {
		return e.Referent
	}
	d := Uniform(string(e.ID))
	return d
}

// FeatureConfidence returns the confidence of a feature or 1 when absent and
// defaulted by the ontology.
func FeatureConfidence(features []Feature, key string) float64 {
	for _, f := range features {
		if f.Key == key {
			return f.Confidence
		}
	}
	return 1
}

// HasFeature reports whether the key is present.
func HasFeature(features []Feature, key string) bool {
	for _, f := range features {
		if f.Key == key {
			return true
		}
	}
	return false
}

// Pred is shorthand for building a provenance entry.
func Pred(origin Origin, conf float64, format string, args ...any) Provenance {
	return Provenance{Origin: origin, Confidence: conf, Note: fmt.Sprintf(format, args...)}
}

// PredToken is Pred plus the surface token it came from.
func PredToken(origin Origin, token string, conf float64, format string, args ...any) Provenance {
	p := Pred(origin, conf, format, args...)
	p.Token = token
	return p
}

// PredSpan is Pred plus a source span.
func PredSpan(origin Origin, sp Span, src string, conf float64, format string, args ...any) Provenance {
	p := Pred(origin, conf, format, args...)
	s := sp
	p.Span = &s
	p.Token = sp.Text(src)
	return p
}

// PredEntity links provenance to a discourse entity.
func PredEntity(origin Origin, id ID, conf float64, format string, args ...any) Provenance {
	p := Pred(origin, conf, format, args...)
	p.EntityID = id
	return p
}

// PredDecision links provenance to an oracle decision id.
func PredDecision(id string, conf float64, format string, args ...any) Provenance {
	p := Pred(OriginDecision, conf, format, args...)
	p.DecisionID = id
	return p
}

// Describe renders a compact, stable summary used by the CLI and the UI header.
func (g *Graph) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s graph: %d entities, %d events, %d scopes, %d predicates",
		g.Lang, len(g.Entities), len(g.Events), len(g.Scopes), len(g.Preds))
	if len(g.ClauseOrder) > 0 {
		fmt.Fprintf(&b, "; clauses=%v", g.ClauseOrder)
	}
	if n := len(g.Unresolved()); n > 0 {
		fmt.Fprintf(&b, "; %d open positions: %s", n, strings.Join(g.Unresolved(), ","))
	}
	return b.String()
}

// ArgNames returns the filled roles of v in a deterministic order.
func (e *Event) ArgNames() []string {
	out := make([]string, 0, len(e.Args))
	for r := range e.Args {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// RoleRank orders roles for target realization. English word order and
// Japanese case ordering both derive from this: subject before object before
// indirect objects before adjuncts.
var RoleRank = map[string]int{
	RoleAgent: 0, RoleExperiencer: 0, RolePatient: 1, RoleTheme: 1,
	RoleStimulus: 1, RoleRecipient: 2, RoleGoal: 2, RoleSource: 2,
	RolePossessor: 3, RoleProduct: 3, RoleInstrument: 4, RoleComitative: 5,
	RoleLocation: 6, RoleTime: 6, RoleManner: 7, RoleCause: 8, RoleBeneficiary: 9,
}

// OrderArgs returns role names sorted by RoleRank then alphabetically, which
// keeps realization deterministic across runs.
func (e *Event) OrderArgs() []string {
	names := e.ArgNames()
	sort.Slice(names, func(i, j int) bool {
		ri, rj := RoleRank[names[i]], RoleRank[names[j]]
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	return names
}
