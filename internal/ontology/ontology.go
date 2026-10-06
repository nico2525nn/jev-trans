// Package ontology defines the predicate inventory JEV-Trans reasons over.
//
// plan.md §10 forbids word-to-word translation and §11 forbids turning the
// ontology into a WordNet-sized sense dictionary. The compromise this package
// implements is "semantic primitive + constraints": a modest inventory of
// event primitives, each with a required role frame and a set of boolean
// features, plus an explicit parent/child hierarchy so that a word with many
// senses can be disambiguated coarsely before fine (plan.md §33).
//
// Nothing here is language specific. Japanese and English surface forms live
// in internal/lexicon and the target realization templates live in
// internal/plan; this package only says what a predicate *is*.
package ontology

import (
	"sort"
	"strings"
)

// Sense is one predicate sense.
type Sense struct {
	// ID is the stable identifier used in JLIR Event.Predicate, e.g.
	// "TRANSFER.01". The parent is the part before the dot.
	ID    string `json:"id"`
	Name  string `json:"name"`
	Gloss string `json:"gloss"`
	// Args is the role frame: which semantic roles this predicate admits and
	// which are mandatory.
	Args []Arg `json:"args"`
	// Features are non-role attributes carried with the sense, e.g.
	// physicality=1, intentionality=1, animate_subject=1. These are the
	// "semantic primitive + constraints" of plan.md §11.
	Features map[string]string `json:"features,omitempty"`
	// Children are sub-sense IDs forming the hierarchical disambiguation
	// ladder (semantic class -> family -> specific sense).
	Children []string `json:"children,omitempty"`
	// Realizable reports whether the target language is expected to express
	// this predicate without help; used to size the translation loss budget.
	Realizable bool `json:"realizable"`
}

// Arg is one slot of a predicate's role frame.
type Arg struct {
	Role     string `json:"role"`
	Required bool   `json:"required"`
	// Default is the role to use when the source marks the argument with an
	// ambiguous case marker and no other evidence exists.
	Default string `json:"default,omitempty"`
}

// Parent returns the parent sense ID, or "" for a root sense.
func (s *Sense) Parent() string {
	if i := strings.Index(s.ID, "."); i > 0 {
		return s.ID[:i]
	}
	return ""
}

// Has reports whether the feature key is present.
func (s *Sense) Has(f string) bool {
	_, ok := s.Features[f]
	return ok
}

// Accepts reports whether the predicate admits the role.
func (s *Sense) Accepts(role string) bool {
	for _, a := range s.Args {
		if a.Role == role {
			return true
		}
	}
	return false
}

// Requires reports whether a role is mandatory for this sense. It is distinct
// from Accepts: a zero argument must fill a slot the frame cannot do without,
// and choosing among the two by preference order alone puts it in the wrong one
// whenever an optional role happens to be listed first.
func (s *Sense) Requires(role string) bool {
	for _, a := range s.Args {
		if a.Role == role {
			return a.Required
		}
	}
	return false
}

// RequiredArgs returns the mandatory roles.
func (s *Sense) RequiredArgs() []string {
	var out []string
	for _, a := range s.Args {
		if a.Required {
			out = append(out, a.Role)
		}
	}
	return out
}

// Level is one level of the hierarchical disambiguation ladder (plan.md §33).
type Level struct {
	Name string   `json:"name"`
	IDs  []string `json:"ids"`
}

// Registry is an immutable predicate inventory.
type Registry struct {
	byID     map[string]*Sense
	order    []*Sense
	roots    []string
	families map[string][]string
}

// New builds a registry from a flat sense list.
func New(senses []*Sense) *Registry {
	r := &Registry{byID: map[string]*Sense{}, families: map[string][]string{}}
	for _, s := range senses {
		if s == nil || s.ID == "" {
			continue
		}
		if _, dup := r.byID[s.ID]; dup {
			continue
		}
		r.byID[s.ID] = s
		r.order = append(r.order, s)
	}
	sort.Slice(r.order, func(i, j int) bool { return r.order[i].ID < r.order[j].ID })
	for _, s := range r.order {
		p := s.Parent()
		if p == "" {
			r.roots = append(r.roots, s.ID)
			continue
		}
		if _, ok := r.byID[p]; ok {
			r.families[p] = append(r.families[p], s.ID)
		} else {
			// A sense whose parent is not a declared sense is its own root.
			r.roots = append(r.roots, s.ID)
		}
	}
	for k := range r.families {
		sort.Strings(r.families[k])
	}
	return r
}

// Sense looks up a sense by ID.
func (r *Registry) Sense(id string) (*Sense, bool) {
	s, ok := r.byID[id]
	return s, ok
}

// MustSense looks up a sense, returning a synthetic permissive one when the ID
// is unknown. Analysis of unknown vocabulary must not crash the pipeline; the
// caller is expected to also record an UnknownUnit.
func (r *Registry) MustSense(id string) *Sense {
	if s, ok := r.byID[id]; ok {
		return s
	}
	return &Sense{ID: id, Name: id, Gloss: "unknown predicate", Args: []Arg{}, Realizable: true}
}

// Senses returns every sense sorted by ID.
func (r *Registry) Senses() []*Sense {
	if r == nil {
		return nil
	}
	return r.order
}

// Families returns the child sense IDs of a parent sense.
func (r *Registry) Families(parent string) []string {
	if r == nil {
		return nil
	}
	return r.families[parent]
}

// Roots returns the top level sense IDs.
func (r *Registry) Roots() []string {
	if r == nil {
		return nil
	}
	return r.roots
}

// Find returns every sense whose name or gloss mentions the query.
func (r *Registry) Find(query string) []*Sense {
	q := strings.ToLower(query)
	var out []*Sense
	for _, s := range r.Senses() {
		if strings.Contains(strings.ToLower(s.Name), q) || strings.Contains(strings.ToLower(s.Gloss), q) {
			out = append(out, s)
		}
	}
	return out
}

// WithFeature returns senses carrying the feature.
func (r *Registry) WithFeature(key, value string) []*Sense {
	var out []*Sense
	for _, s := range r.Senses() {
		if v, ok := s.Features[key]; ok && (value == "" || v == value) {
			out = append(out, s)
		}
	}
	return out
}

// Ladder builds the hierarchical disambiguation levels for a candidate set,
// collapsing it onto the parents first when it is too large for a single
// Choice question (plan.md §33, and the 255 option ceiling of the oracle).
func (r *Registry) Ladder(candidates []string) []Level {
	levels := []Level{}
	cur := append([]string(nil), candidates...)
	sort.Strings(cur)
	levels = append(levels, Level{Name: "sense", IDs: cur})
	for range 3 {
		if len(levels[len(levels)-1].IDs) <= 32 {
			break
		}
		seen := map[string]bool{}
		var parents []string
		for _, id := range levels[len(levels)-1].IDs {
			p := id
			if s, ok := r.byID[id]; ok {
				if pa := s.Parent(); pa != "" {
					if _, ok2 := r.byID[pa]; ok2 {
						p = pa
					}
				}
			}
			if !seen[p] {
				seen[p] = true
				parents = append(parents, p)
			}
		}
		if len(parents) == len(levels[len(levels)-1].IDs) {
			break
		}
		sort.Strings(parents)
		levels = append(levels, Level{Name: "predicate family", IDs: parents})
	}
	return levels
}

// Stats is reported in the UI so the user can see how much the system actually
// knows.
type Stats struct {
	Senses   int `json:"senses"`
	Families int `json:"families"`
	Roots    int `json:"roots"`
}

// Stats computes the summary.
func (r *Registry) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return Stats{Senses: len(r.order), Families: len(r.families), Roots: len(r.roots)}
}
