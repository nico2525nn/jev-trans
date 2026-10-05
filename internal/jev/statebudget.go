package jev

import (
	"encoding/json"
	"sort"
	"strings"
)

// The state budget implements plan.md §31's discipline: the oracle is asked
// bounded questions about bounded state, and a question is never built by
// dumping everything the system knows into `state` and hoping the model finds
// the relevant part. Jev's own guidance is that unrelated material in the state
// costs accuracy — it is context rot, and a larger state makes it harder to tell
// which part of the input produced a wrong answer.
//
// This file used to be 695 lines with a StateBuilder and fifteen renderers for
// entities, events, scopes and pragmatics, none of which had a caller. What
// survives is the part the client actually needs: a byte budget and a rule for
// dropping the least relevant key when a payload exceeds it. The rendering is
// the orchestrator's job, because only the orchestrator knows which question is
// about to be asked.
//
// The one place a hard cut is the best available option is a non-object state,
// which is truncated mid-string. Everything else loses a whole key: a truncated
// question is silently corrupt, whereas a missing background entity is merely
// absent context.

// DefaultStateBudget is the serialised-state ceiling in bytes.
const DefaultStateBudget = 4000

// stateKeyPriority orders state keys by how much they matter to a decision.
// Higher survives a budget cut. The buckets are deliberately coarse: the point
// is that the competing readings of the sentence and the candidate entities
// survive, and the discourse background does not.
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
// payload fits budget.
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
	// Most relevant first, so the drop loop removes the least relevant key.
	sort.Slice(keys, func(i, j int) bool {
		pi, pj := stateKeyPriority(keys[i]), stateKeyPriority(keys[j])
		if pi != pj {
			return pi > pj
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

// MergeStates combines the states of a batch into one payload. Map states are
// merged first-wins so that a question-specific field is never overwritten by a
// later question's generic one; plain strings are concatenated under "text" so
// a source sentence present in one question is not lost when another supplies a
// structured object.
func MergeStates(states []any, budget int) any {
	if budget <= 0 {
		budget = DefaultStateBudget
	}
	var parts []any
	var obj map[string]any
	ensureObj := func() {
		if obj == nil {
			obj = map[string]any{}
		}
	}
	for _, st := range states {
		if st == nil {
			continue
		}
		switch v := st.(type) {
		case map[string]any:
			ensureObj()
			for k, val := range v {
				if _, exists := obj[k]; !exists {
					obj[k] = val
				}
			}
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
			ensureObj()
			if s, ok := obj["text"].(string); ok {
				obj["text"] = s + " " + v
			} else {
				obj["text"] = v
			}
		default:
			parts = append(parts, v)
		}
	}
	if obj == nil && len(parts) == 0 {
		return map[string]any{}
	}
	if obj == nil {
		if len(parts) == 1 {
			return TrimState(parts[0], budget)
		}
		return TrimState(parts, budget)
	}
	if len(parts) > 0 {
		obj["parts"] = parts
	}
	return TrimState(obj, budget)
}
