package jev

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
)

// plan.md §31 forbids asking Jev for free text, and a decision model with a
// long context is a decision model that starts hallucinating. The `state` field
// of the System One request is therefore a compact *structured object* built
// here, never a dump of the whole analysis. It holds the entities, events and
// readings the specific question is about, and nothing else.
//
// The builder keeps every item with a priority so that a state which exceeds
// the byte budget sheds its least relevant material instead of being sent
// truncated: an ellipsis would silently corrupt the question, whereas a
// missing background entity is merely absent context.
const DefaultStateBudget = 4000

// Item priorities. Higher survives a budget cut.
const (
	// PriorityRequired is used for the item the question is actually about.
	PriorityRequired = 90
	// PriorityFocus is the local neighbourhood of the question (the competing
	// entities/events, the competing readings).
	PriorityFocus = 75
	// PriorityLocal is sentence-local material another option mentions.
	PriorityLocal = 60
	// PriorityContext is the rest of the sentence.
	PriorityContext = 45
	// PriorityBackdrop is document level material.
	PriorityBackdrop = 25
)

type stateItem struct {
	key      string
	value    any
	priority int
	required bool
	order    int
}

// StateBuilder assembles one `state` payload from JLIR slices under a byte
// budget. It is deliberately not a general JSON builder: every method here
// exists because some decision stage needs that exact material, and the code
// that needs it must never reach for the entire graph.
type StateBuilder struct {
	budget  int
	items   []stateItem
	dropped []string
}

// NewState starts a builder with the given byte budget. A budget of zero or
// less uses DefaultStateBudget.
func NewState(budget int) *StateBuilder {
	if budget <= 0 {
		budget = DefaultStateBudget
	}
	return &StateBuilder{budget: budget}
}

// Set adds an item with an explicit priority. Later additions are dropped
// before earlier ones at the same priority only if they are the same size;
// ties are broken in favour of the first item added.
func (s *StateBuilder) Set(key string, value any, priority int) *StateBuilder {
	if s == nil || key == "" {
		return s
	}
	s.items = append(s.items, stateItem{key: key, value: value, priority: priority, order: len(s.items)})
	return s
}

// Require adds an item that survives any budget cut, because the question
// cannot be answered without it.
func (s *StateBuilder) Require(key string, value any) *StateBuilder {
	if s == nil || key == "" {
		return s
	}
	s.items = append(s.items, stateItem{key: key, value: value, priority: PriorityRequired, required: true, order: len(s.items)})
	return s
}

// Entities adds the compact record of ids, in the order given. Ids that do not
// resolve are skipped rather than rendered as nulls.
func (s *StateBuilder) Entities(g *jlir.Graph, ids []jlir.ID, l lang.Lang, priority int) *StateBuilder {
	if s == nil || g == nil || len(ids) == 0 {
		return s
	}
	seen := map[jlir.ID]bool{}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if st := EntityState(g, id, l); st != nil {
			out = append(out, st)
		}
	}
	if len(out) == 0 {
		return s
	}
	return s.Set("entities", out, priority)
}

// Events adds the compact record of ids.
func (s *StateBuilder) Events(g *jlir.Graph, ids []jlir.ID, l lang.Lang, priority int) *StateBuilder {
	if s == nil || g == nil || len(ids) == 0 {
		return s
	}
	seen := map[jlir.ID]bool{}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if st := EventState(g, id, l); st != nil {
			out = append(out, st)
		}
	}
	if len(out) == 0 {
		return s
	}
	return s.Set("events", out, priority)
}

// Scope adds one scope node with its competing readings. This is the entire
// payload of a D1 question: the operators and the orderings they permit.
func (s *StateBuilder) Scope(n *jlir.ScopeNode, priority int) *StateBuilder {
	if s == nil || n == nil {
		return s
	}
	return s.Set("scope", ScopeState(n), priority)
}

// Pragmatics adds the pragmatic readings.
func (s *StateBuilder) Pragmatics(p jlir.Pragmatics, priority int) *StateBuilder {
	if s == nil {
		return s
	}
	st := PragmaticState(p)
	if len(st) == 0 {
		return s
	}
	return s.Set("pragmatics", st, priority)
}

// Info adds the information structure layer (topic/focus, never conflated with
// subject — plan.md §19).
func (s *StateBuilder) Info(inf jlir.InfoStruct, l lang.Lang, priority int) *StateBuilder {
	if s == nil {
		return s
	}
	st := InfoState(inf, l)
	if len(st) == 0 {
		return s
	}
	return s.Set("info", st, priority)
}

// Source adds a compact summary of the whole reading. It is PriorityBackdrop
// by default because it is the first thing a budget cut removes.
func (s *StateBuilder) Source(g *jlir.Graph, l lang.Lang, priority int) *StateBuilder {
	if s == nil || g == nil {
		return s
	}
	return s.Set("source", SourceSummary(g, l), priority)
}

// Candidates adds the target-side realization options of a projection
// decision, which is what makes the impact of D7/D8 legible.
func (s *StateBuilder) Candidates(opts []map[string]any, priority int) *StateBuilder {
	if s == nil || len(opts) == 0 {
		return s
	}
	return s.Set("candidates", opts, priority)
}

// Dropped reports the keys removed by the last Build, so the decision graph can
// say in the trace that the oracle saw a reduced state.
func (s *StateBuilder) Dropped() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.dropped...)
}

// Build produces the payload, dropping the least relevant items until the
// serialised form fits the budget. Required items are never dropped; if they
// alone exceed the budget the payload is returned over budget rather than
// mutilated, because a question about material the model cannot see is worse
// than a long state.
func (s *StateBuilder) Build() map[string]any {
	if s == nil || len(s.items) == 0 {
		return map[string]any{}
	}
	s.dropped = nil
	budget := s.budget
	if budget <= 0 {
		budget = DefaultStateBudget
	}
	keep := make([]bool, len(s.items))
	sizes := make([]int, len(s.items))
	total := 0
	for i, it := range s.items {
		keep[i] = true
		b, err := json.Marshal(it.value)
		if err != nil {
			b = []byte("null")
		}
		sizes[i] = len(b) + len(it.key) + 8
		total += sizes[i]
	}
	for total > budget {
		idx := -1
		for i, it := range s.items {
			if !keep[i] || it.required {
				continue
			}
			if idx < 0 || it.priority < s.items[idx].priority {
				idx = i
			}
		}
		if idx < 0 {
			break
		}
		keep[idx] = false
		total -= sizes[idx]
		s.dropped = append(s.dropped, s.items[idx].key)
	}
	out := make(map[string]any, len(s.items))
	for i, it := range s.items {
		if keep[i] {
			out[it.key] = it.value
		}
	}
	return out
}

// --- compact record helpers -------------------------------------------------

func entitySurface(e *jlir.Entity, l lang.Lang) string {
	if e == nil {
		return ""
	}
	if s := e.Alias(l); s != "" {
		return s
	}
	if e.Identity != "" {
		return e.Identity
	}
	return string(e.ID)
}

func featureSlice(fs []jlir.Feature, limit int) map[string]any {
	if len(fs) == 0 {
		return nil
	}
	sorted := append([]jlir.Feature(nil), fs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	out := make(map[string]any, min(len(sorted), limit))
	for i, f := range sorted {
		if i >= limit {
			break
		}
		out[f.Key] = jlir.ValueString(f.Value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// origins distills provenance to the origin labels only. The full provenance
// travels in the JLIR artifact and the trace; the oracle only needs to know
// that a fact is traceable, not where every byte of it lives.
func origins(ps []jlir.Provenance) []string {
	if len(ps) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, p := range ps {
		s := string(p.Origin)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// EntityState renders one entity for the oracle: what it is, how it is referred
// to, and — for a placeholder — which referents are still in play. Provenance
// labels travel with it so the model can respect an UNKNOWN instead of filling
// it in.
func EntityState(g *jlir.Graph, id jlir.ID, l lang.Lang) map[string]any {
	if g == nil {
		return nil
	}
	e := g.Entity(id)
	if e == nil {
		return nil
	}
	st := map[string]any{
		"id":      string(e.ID),
		"type":    e.Type,
		"surface": entitySurface(e, l),
	}
	if e.Number != "" && e.Number != jlir.NumberUnknown {
		st["number"] = e.Number
	}
	if e.Gender != "" && e.Gender != jlir.GenderUnknown {
		st["gender"] = e.Gender
	}
	if e.Animacy != "" && e.Animacy != jlir.AnimacyUnknown {
		st["animacy"] = e.Animacy
	}
	if e.Proper {
		st["proper"] = true
	}
	if e.Zero {
		st["zero"] = true
	}
	if e.External {
		st["fromDiscourse"] = true
	}
	if e.MentionCount > 0 {
		st["mentions"] = e.MentionCount
	}
	if e.Salience > 0 {
		st["salience"] = round2(e.Salience)
	}
	if e.SpeakerRelation != "" {
		st["relation"] = e.SpeakerRelation
	}
	if fs := featureSlice(e.Features, 4); fs != nil {
		st["features"] = fs
	}
	if org := origins(e.Prov); org != nil {
		st["from"] = org
	}
	if e.Referent != nil && len(e.Referent.Options) > 1 {
		cands := []map[string]any{}
		for _, r := range e.Referent.Top(6) {
			cands = append(cands, map[string]any{"id": r.Option, "p": round2(r.P)})
		}
		if len(cands) > 0 {
			st["referentCandidates"] = cands
		}
		if e.Referent.Resolved {
			st["referentResolved"] = true
		}
	}
	return st
}

// EventState renders one event with its role frame, temporal and modal state.
func EventState(g *jlir.Graph, id jlir.ID, l lang.Lang) map[string]any {
	if g == nil {
		return nil
	}
	v := g.Event(id)
	if v == nil {
		return nil
	}
	st := map[string]any{
		"id":        string(v.ID),
		"predicate": v.Predicate,
	}
	if sense := g.Predicate(v.Predicate); sense != nil && sense.Name != "" {
		st["predicateName"] = sense.Name
	}
	if v.Tense != "" && v.Tense != jlir.TenseUnknown {
		st["tense"] = v.Tense
	}
	if v.Aspect != "" {
		st["aspect"] = v.Aspect
	}
	if v.Completion != "" {
		st["completion"] = v.Completion
	}
	if v.Polarity != "" && v.Polarity != jlir.PolarityUnknown {
		st["polarity"] = v.Polarity
	}
	if v.Modality != "" && v.Modality != jlir.ModalityNone {
		st["modality"] = v.Modality
	}
	if v.Mood != "" {
		st["mood"] = v.Mood
	}
	if v.Causation != "" && v.Causation != jlir.CausationNone {
		st["causation"] = v.Causation
	}
	if len(v.Args) > 0 {
		roles := make([]string, 0, len(v.Args))
		for r := range v.Args {
			roles = append(roles, r)
		}
		sort.Strings(roles)
		args := make([]map[string]any, 0, len(roles))
		for _, r := range roles {
			a := v.Args[r]
			args = append(args, map[string]any{
				"role": r,
				"of":   string(a.Value),
				"p":    round2(a.Confidence),
			})
		}
		st["args"] = args
	}
	if fs := featureSlice(v.Features, 4); fs != nil {
		st["features"] = fs
	}
	if org := origins(v.Prov); org != nil {
		st["from"] = org
	}
	return st
}

func readingLabel(n *jlir.ScopeNode, i int, r jlir.ScopeReading) string {
	if r.Label != "" {
		return r.Label
	}
	if len(r.Order) > 0 {
		return strings.Join(r.Order, " > ")
	}
	if i < len(n.Operands) {
		return n.Operands[i]
	}
	return n.Kind
}

// ScopeState renders a scope node and its competing readings as an explicit
// set of orderings. plan.md §13 refuses to linearize scope into a string on
// the source side; the oracle is asked precisely because "NOT > ALL" and
// "ALL > NOT" produce different English.
func ScopeState(n *jlir.ScopeNode) map[string]any {
	if n == nil {
		return nil
	}
	st := map[string]any{"id": string(n.ID), "kind": n.Kind}
	if len(n.Operands) > 0 {
		st["operands"] = append([]string(nil), n.Operands...)
	}
	readings := []map[string]any{}
	counts := map[string]int{}
	for _, r := range n.Readings {
		counts[readingLabel(n, 0, r)]++
	}
	for i, r := range n.Readings {
		label := readingLabel(n, i, r)
		if counts[label] > 1 {
			label = label + " [" + strings.Join(r.Order, " ") + "]"
		}
		readings = append(readings, map[string]any{
			"key":   "r" + itoa(i),
			"label": label,
			"p":     round2(r.Weight),
		})
	}
	if len(readings) > 0 {
		st["readings"] = readings
	}
	if n.Resolved {
		st["resolved"] = true
	}
	return st
}

// PragmaticState renders the pragmatic layer. An UNKNOWN certainty stays
// UNKNOWN: plan.md §18 requires the system to be able to say "this is
// underspecified", not to guess a register.
func PragmaticState(p jlir.Pragmatics) map[string]any {
	st := map[string]any{}
	if p.SpeechStyle != "" {
		st["style"] = p.SpeechStyle
	}
	for k, v := range map[string]float64{
		"formality":     p.Formality,
		"politeness":    p.Politeness,
		"respect":       p.Respect,
		"humility":      p.Humility,
		"assertiveness": p.Assertiveness,
		"certainty":     p.Certainty,
	} {
		if v != 0 {
			st[k] = round2(v)
		}
	}
	if p.EmotionalTone != "" {
		st["tone"] = p.EmotionalTone
	}
	if p.SentenceFinalAttitude != "" {
		st["attitude"] = p.SentenceFinalAttitude
	}
	if len(p.SentenceFinalParticles) > 0 {
		st["particles"] = append([]string(nil), p.SentenceFinalParticles...)
	}
	if p.RespectAddressee != "" {
		st["respectTarget"] = p.RespectAddressee
	}
	if p.IronySuspected {
		st["ironySuspected"] = true
	}
	if org := origins(p.Prov); org != nil {
		st["from"] = org
	}
	return st
}

// InfoState renders topic/focus without conflating them with subject.
func InfoState(inf jlir.InfoStruct, l lang.Lang) map[string]any {
	st := map[string]any{}
	add := func(k string, ids []jlir.ID) {
		if len(ids) == 0 {
			return
		}
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, string(id))
		}
		st[k] = out
	}
	add("topic", inf.Topic)
	add("focus", inf.Focus)
	add("contrast", inf.Contrast)
	add("given", inf.Given)
	add("new", inf.New)
	if inf.Marker != "" {
		st["marker"] = inf.Marker
	}
	return st
}

// SourceSummary is the fallback backdrop: enough of the sentence for the model
// to read a question, and nothing more.
func SourceSummary(g *jlir.Graph, l lang.Lang) map[string]any {
	if g == nil {
		return nil
	}
	st := map[string]any{
		"lang":     string(g.Lang),
		"entities": len(g.Entities),
		"events":   len(g.Events),
	}
	if g.Matrix() != nil {
		st["matrix"] = string(g.Matrix().ID)
		st["matrixPredicate"] = g.Matrix().Predicate
	}
	if len(g.SourceFeat.Constructions) > 0 {
		st["constructions"] = append([]string(nil), g.SourceFeat.Constructions...)
	}
	if len(g.SourceFeat.Unknowns) > 0 {
		u := make([]string, 0, len(g.SourceFeat.Unknowns))
		for _, x := range g.SourceFeat.Unknowns {
			u = append(u, x.Surface)
		}
		st["unresolvedUnits"] = u
	}
	return st
}

// --- batching support -------------------------------------------------------

// MergeStates folds the per-request states of one batch into the single `state`
// object the System One API accepts. Object states are unioned (first writer
// wins, which keeps the most specific builder in charge); anything else is
// collected under "parts". The result is trimmed to budget.
func MergeStates(states []any, budget int) any {
	var parts []any
	var obj map[string]any
	for _, st := range states {
		if st == nil {
			continue
		}
		switch v := st.(type) {
		case map[string]any:
			if obj == nil {
				obj = map[string]any{}
			}
			for k, val := range v {
				if _, exists := obj[k]; !exists {
					obj[k] = val
				}
			}
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
			if s, ok := obj["text"].(string); ok {
				obj["text"] = s + " " + v
			} else {
				obj = ensureObj(obj)
				obj["text"] = v
			}
		default:
			parts = append(parts, v)
		}
	}
	if obj == nil && len(parts) == 0 {
		return map[string]any{}
	}
	if len(parts) > 0 {
		obj = ensureObj(obj)
		obj["parts"] = parts
	}
	return TrimState(obj, budget)
}

func ensureObj(obj map[string]any) map[string]any {
	if obj == nil {
		return map[string]any{}
	}
	return obj
}

// stateKeyPriority orders state keys by how much they change an answer. A
// budget cut removes the most decorative material first.
func stateKeyPriority(key string) int {
	switch key {
	case "focus", "options", "alternatives":
		return 85
	case "scope", "scopeNode", "readings":
		return 80
	case "entity", "entities", "event", "events", "referentCandidates":
		return 75
	case "predicate", "candidates", "args":
		return 65
	case "info", "pragmatics", "discourse":
		return 45
	case "parts", "context":
		return 30
	case "source", "text":
		return 20
	}
	return 40
}

// TrimState drops state keys, least relevant first, until the serialised
// payload fits budget. A non-object state is truncated, which is the one place
// where a hard cut is the best available option.
func TrimState(state any, budget int) any {
	if state == nil {
		return map[string]any{}
	}
	if budget <= 0 {
		budget = DefaultStateBudget
	}
	if raw, err := json.Marshal(state); err == nil && len(raw) <= budget {
		return state
	}
	obj, ok := state.(map[string]any)
	if !ok {
		if s, isStr := state.(string); isStr {
			if len(s) <= budget {
				return s
			}
			return s[:budget]
		}
		return state
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, pj := stateKeyPriority(keys[i]), stateKeyPriority(keys[j])
		if pi != pj {
			return pi < pj
		}
		return keys[i] < keys[j]
	})
	out := make(map[string]any, len(obj))
	for _, k := range keys {
		out[k] = obj[k]
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			continue
		}
		if raw, err := json.Marshal(out); err == nil && len(raw) <= budget {
			return out
		}
		delete(out, k)
	}
	return map[string]any{}
}
