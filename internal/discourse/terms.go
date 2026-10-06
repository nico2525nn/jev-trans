package discourse

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
)

// Term is one entry of the terminology memory (plan.md §52): a source term, the
// concept it denotes, the target form the document has agreed on, and the
// target forms that must never be used. Keeping this for the whole document is
// what stops 用語揺れ — technical vocabulary drifting between sentences.
type Term struct {
	ID         string    `json:"id,omitempty"`
	Source     string    `json:"source"`
	SourceLang lang.Lang `json:"sourceLang,omitempty"`
	// Concept is the ontology sense or concept key the term denotes. It is what
	// lets two different source terms of the same concept share one target.
	Concept    string    `json:"concept,omitempty"`
	TargetLang lang.Lang `json:"targetLang,omitempty"`
	// Preferred is the target form to use.
	Preferred string `json:"preferred,omitempty"`
	// PreferredForms overrides Preferred per target language.
	PreferredForms map[lang.Lang]string `json:"preferredForms,omitempty"`
	// Forbidden target forms. A candidate matching one is a conflict, never a
	// silent rewrite.
	Forbidden []string `json:"forbidden,omitempty"`
	// ForbiddenForms overrides Forbidden per target language.
	ForbiddenForms map[lang.Lang][]string `json:"forbiddenForms,omitempty"`
	// Forms maps a language to a further source surface of the same concept, so
	// 東京 and Tokyo both resolve to the same entry.
	Forms map[lang.Lang]string `json:"forms,omitempty"`
	Note  string               `json:"note,omitempty"`
	// Scope is "project" or "document"; the plan allows either granularity.
	Scope string            `json:"scope,omitempty"`
	Prov  []jlir.Provenance `json:"provenance,omitempty"`
}

// Lookup is the answer to "what do we call this in the target". It always
// carries both lists so the realizer can enforce the negative constraint as
// well as follow the positive one.
type Lookup struct {
	TermID     string    `json:"termId,omitempty"`
	Source     string    `json:"source"`
	SourceLang lang.Lang `json:"sourceLang,omitempty"`
	TargetLang lang.Lang `json:"targetLang,omitempty"`
	Preferred  []string  `json:"preferred,omitempty"`
	Forbidden  []string  `json:"forbidden,omitempty"`
	// Term is the full entry, for the UI.
	Term *Term `json:"term,omitempty"`
}

// Conflict is a case where the document has already chosen something the memory
// does not want. It is reported to the caller and surfaced in the snapshot; the
// memory never rewrites a translation behind the pipeline's back.
type Conflict struct {
	TermID     string    `json:"termId,omitempty"`
	Source     string    `json:"source"`
	SourceLang lang.Lang `json:"sourceLang,omitempty"`
	TargetLang lang.Lang `json:"targetLang,omitempty"`
	// Chosen is what a previous sentence settled on.
	Chosen string `json:"chosen"`
	// Preferred is what the memory wants instead.
	Preferred []string `json:"preferred,omitempty"`
	// Forbidden lists the forms that must not be used at all.
	Forbidden []string `json:"forbidden,omitempty"`
	// Reason is one of: forbidden or not_preferred.
	Reason   string            `json:"reason"`
	Note     string            `json:"note,omitempty"`
	Prov     []jlir.Provenance `json:"provenance,omitempty"`
	Sentence int               `json:"sentence,omitempty"`
}

// Conflict reasons.
const (
	ConflictForbidden    = "forbidden"     // the chosen form is explicitly banned
	ConflictNotPreferred = "not_preferred" // the chosen form is not the agreed one
)

// TermView is the JSON-friendly projection the API response carries.
type TermView struct {
	ID         string            `json:"id,omitempty"`
	Source     string            `json:"source"`
	SourceLang string            `json:"sourceLang,omitempty"`
	Concept    string            `json:"concept,omitempty"`
	Preferred  string            `json:"preferred,omitempty"`
	Forbidden  []string          `json:"forbidden,omitempty"`
	Forms      map[string]string `json:"forms,omitempty"`
	Scope      string            `json:"scope,omitempty"`
	Conflicts  int               `json:"conflicts,omitempty"`
}

// View renders a term for the API response, with deterministic key ordering.
func (t *Term) View() TermView {
	if t == nil {
		return TermView{}
	}
	v := TermView{
		ID: t.ID, Source: t.Source, Concept: t.Concept, Scope: t.Scope,
		Preferred: t.Preferred, Forbidden: sorted(t.Forbidden),
	}
	if t.SourceLang != "" {
		v.SourceLang = string(t.SourceLang)
	}
	if len(t.Forms) > 0 {
		v.Forms = map[string]string{}
		for k, f := range t.Forms {
			v.Forms[string(k)] = f
		}
	}
	return v
}

// TermMemory is the document or project level terminology store (plan.md §52).
// It is not internally synchronized; Store owns the lock.
type TermMemory struct {
	terms     []*Term
	index     map[string]*Term
	byConcept map[string]*Term
	conflicts []Conflict
	seq       int
}

// NewTermMemory returns an empty terminology memory.
func NewTermMemory() *TermMemory {
	return &TermMemory{index: map[string]*Term{}, byConcept: map[string]*Term{}}
}

func termKey(source string, l lang.Lang) string {
	return normalizeKey(source) + "|" + string(l)
}

// Define adds or extends an entry. An existing entry for the same source form
// keeps its identity: its forbidden list grows, and a conflicting preferred
// form is reported rather than silently overwriting the document's earlier
// decision.
func (m *TermMemory) Define(t Term) (*Term, []Conflict) {
	if strings.TrimSpace(t.Source) == "" {
		return nil, nil
	}
	if t.SourceLang == "" {
		t.SourceLang = lang.JA
	}
	var conflicts []Conflict
	langs := []lang.Lang{t.SourceLang}
	if t.Forms != nil {
		for l := range t.Forms {
			langs = appendUniqueLang(langs, l)
		}
	}
	langs = sortedLangs(langs)

	var existing *Term
	for _, l := range langs {
		if e, ok := m.index[termKey(t.Source, l)]; ok && e != nil {
			existing = e
			break
		}
	}
	if existing != nil {
		if existing.Preferred != "" && t.Preferred != "" && existing.Preferred != t.Preferred {
			conflicts = append(conflicts, Conflict{
				TermID: existing.ID, Source: existing.Source, SourceLang: existing.SourceLang,
				TargetLang: t.TargetLang, Chosen: existing.Preferred,
				Preferred: sortedUniquePrefs(t),
				Reason:    ConflictNotPreferred,
				Note:      "a new definition prefers a different target than the document already uses",
			})
		}
		existing.Forbidden = unique(append(existing.Forbidden, t.Forbidden...))
		for k, v := range t.PreferredForms {
			if existing.PreferredForms == nil {
				existing.PreferredForms = map[lang.Lang]string{}
			}
			existing.PreferredForms[k] = v
		}
		for k, v := range t.ForbiddenForms {
			existing.ForbiddenForms[k] = unique(append(existing.ForbiddenForms[k], v...))
		}
		for l, f := range t.Forms {
			if existing.Forms == nil {
				existing.Forms = map[lang.Lang]string{}
			}
			if _, ok := existing.Forms[l]; !ok {
				existing.Forms[l] = f
			}
			m.index[termKey(f, l)] = existing
		}
		if existing.Concept == "" {
			existing.Concept = t.Concept
		}
		existing.Prov = append(existing.Prov, t.Prov...)
		m.reindex(existing)
		return existing, conflicts
	}

	m.seq++
	if t.ID == "" {
		t.ID = fmt.Sprintf("T%d", m.seq)
	}
	if t.Scope == "" {
		t.Scope = "document"
	}
	entry := &t
	m.terms = append(m.terms, entry)
	m.reindex(entry)
	if entry.Concept != "" {
		if other, ok := m.byConcept[entry.Concept]; ok && other != entry {
			// Two source terms of one concept must agree on one target form.
			if other.Preferred != "" && entry.Preferred != "" && other.Preferred != entry.Preferred {
				conflicts = append(conflicts, Conflict{
					TermID: entry.ID, Source: entry.Source, SourceLang: entry.SourceLang,
					TargetLang: entry.TargetLang, Chosen: entry.Preferred,
					Preferred: []string{other.Preferred}, Reason: ConflictNotPreferred,
					Note: "another source term of concept " + entry.Concept + " already chose a target form",
				})
			}
		} else {
			m.byConcept[entry.Concept] = entry
		}
	}
	return entry, conflicts
}

// sortedUniquePrefs collects every preferred target form of a term definition,
// sorted and de-duplicated.
func sortedUniquePrefs(t Term) []string {
	out := []string{t.Preferred}
	for _, v := range t.PreferredForms {
		out = append(out, v)
	}
	return sorted(unique(out))
}

// DefineTerm is the convenience constructor for the common case.
func (m *TermMemory) DefineTerm(source string, sourceLang, targetLang lang.Lang, concept, preferred string, forbidden ...string) *Term {
	t, _ := m.Define(Term{
		Source: source, SourceLang: sourceLang, TargetLang: targetLang,
		Concept: concept, Preferred: preferred, Forbidden: forbidden,
	})
	return t
}

// reindex registers every surface of the term.
func (m *TermMemory) reindex(t *Term) {
	m.index[termKey(t.Source, t.SourceLang)] = t
	for l, f := range t.Forms {
		m.index[termKey(f, l)] = t
	}
}

// Lookup returns the preferred and forbidden target forms for a source term.
func (m *TermMemory) Lookup(source string, l lang.Lang) (*Lookup, bool) {
	if m == nil || source == "" {
		return nil, false
	}
	t, ok := m.index[termKey(source, l)]
	if !ok {
		if t, ok = m.index[termKey(source, lang.JA)]; !ok {
			if t, ok = m.index[termKey(source, lang.EN)]; !ok {
				return nil, false
			}
		}
	}
	target := t.TargetLang
	if target == "" {
		target = l.Other()
	}
	lk := &Lookup{
		TermID: t.ID, Source: t.Source, SourceLang: t.SourceLang, TargetLang: target,
		Forbidden: sorted(unique(t.ForbiddenForms[target])),
		Term:      t,
	}
	if len(lk.Forbidden) == 0 {
		lk.Forbidden = sorted(unique(t.Forbidden))
	}
	if p, ok := t.PreferredForms[target]; ok && p != "" {
		lk.Preferred = []string{p}
	} else if t.Preferred != "" {
		lk.Preferred = []string{t.Preferred}
	} else {
		forms := make([]string, 0, len(t.PreferredForms))
		for l := range t.PreferredForms {
			forms = append(forms, string(l))
		}
		lk.Preferred = sorted(unique(forms))
	}
	return lk, true
}

// LookupConcept resolves a source term through the concept it denotes, which is
// how two different surface forms of one idea end up with one target form.
func (m *TermMemory) LookupConcept(concept string, target lang.Lang) (*Lookup, bool) {
	if m == nil || concept == "" {
		return nil, false
	}
	t, ok := m.byConcept[concept]
	if !ok {
		return nil, false
	}
	source := t.Source
	return m.Lookup(source, t.SourceLang)
}

// Check reports where a target form the document already chose disagrees with
// the memory. It changes nothing: the caller decides whether to re-render,
// to warn the user, or to leave the earlier translation alone.
func (m *TermMemory) Check(source string, l lang.Lang, chosen string, target lang.Lang) []Conflict {
	if m == nil || source == "" || chosen == "" {
		return nil
	}
	lk, ok := m.Lookup(source, l)
	if !ok {
		return nil
	}
	for _, bad := range lk.Forbidden {
		if matchesTerm(bad, chosen) {
			return []Conflict{{
				TermID: lk.TermID, Source: source, SourceLang: lk.SourceLang,
				TargetLang: targetOr(target, lk.TargetLang), Chosen: chosen,
				Preferred: lk.Preferred, Forbidden: lk.Forbidden, Reason: ConflictForbidden,
				Prov: lk.Term.Prov,
			}}
		}
	}
	if len(lk.Preferred) > 0 && !containsTerm(lk.Preferred, chosen) {
		return []Conflict{{
			TermID: lk.TermID, Source: source, SourceLang: lk.SourceLang,
			TargetLang: targetOr(target, lk.TargetLang), Chosen: chosen,
			Preferred: lk.Preferred, Forbidden: lk.Forbidden, Reason: ConflictNotPreferred,
			Prov: lk.Term.Prov,
		}}
	}
	return nil
}

// ReportConflict records a conflict in the document's conflict list so the UI
// and the next translation run can see it. Conflicts accumulate; they are never
// cleared by a later matching choice, because the earlier sentence was still
// rendered with the wrong term.
func (m *TermMemory) ReportConflict(c []Conflict) {
	for _, x := range c {
		m.conflicts = append(m.conflicts, x)
	}
}

// Conflicts returns every reported conflict in the order it was found.
func (m *TermMemory) Conflicts() []Conflict {
	if m == nil {
		return nil
	}
	return append([]Conflict(nil), m.conflicts...)
}

// ConflictsFor returns the conflicts recorded for one source term.
func (m *TermMemory) ConflictsFor(source string) []Conflict {
	if m == nil {
		return nil
	}
	var out []Conflict
	for _, c := range m.conflicts {
		if c.Source == source {
			out = append(out, c)
		}
	}
	return out
}

// Terms returns every entry, sorted by source form for deterministic output.
func (m *TermMemory) Terms() []*Term {
	if m == nil {
		return nil
	}
	out := append([]*Term(nil), m.terms...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].SourceLang < out[j].SourceLang
	})
	return out
}

// Views renders the memory for the API response, with the conflict counts.
func (m *TermMemory) Views() []TermView {
	terms := m.Terms()
	out := make([]TermView, 0, len(terms))
	for _, t := range terms {
		v := t.View()
		v.Conflicts = len(m.ConflictsFor(t.Source))
		out = append(out, v)
	}
	return out
}

// Reset empties the memory.
func (m *TermMemory) Reset() {
	m.terms = nil
	m.index = map[string]*Term{}
	m.byConcept = map[string]*Term{}
	m.conflicts = nil
	m.seq = 0
}

// File is the on-disk shape of a terminology memory: a project or document
// glossary. It is plain JSON so it can be versioned next to the document.
type File struct {
	Scope string `json:"scope,omitempty"`
	Terms []Term `json:"terms"`
}

// Load reads a terminology memory from a JSON file.
func Load(path string) (*TermMemory, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := NewTermMemory()
	if _, err := m.LoadFrom(f); err != nil {
		return nil, fmt.Errorf("discourse: %s: %w", path, err)
	}
	return m, nil
}

// LoadFrom reads a terminology memory from any reader, accumulating the
// conflicts the file itself creates.
func (m *TermMemory) LoadFrom(r io.Reader) ([]Conflict, error) {
	if m == nil {
		return nil, fmt.Errorf("discourse: nil term memory")
	}
	if m.index == nil {
		m.index = map[string]*Term{}
	}
	if m.byConcept == nil {
		m.byConcept = map[string]*Term{}
	}
	var f File
	dec := json.NewDecoder(r)
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	if f.Scope != "" {
		for _, t := range m.terms {
			if t.Scope == "" {
				t.Scope = f.Scope
			}
		}
	}
	var conflicts []Conflict
	for _, t := range f.Terms {
		if f.Scope != "" && t.Scope == "" {
			t.Scope = f.Scope
		}
		if len(t.Prov) == 0 {
			t.Prov = []jlir.Provenance{{
				Token: t.Source, Origin: jlir.OriginUser,
				Note: "loaded from the terminology memory file",
			}}
		}
		_, c := m.Define(t)
		conflicts = append(conflicts, c...)
	}
	m.ReportConflict(conflicts)
	return conflicts, nil
}

// MarshalJSON renders the memory as the File shape, so it round trips through
// LoadFrom unchanged.
func (m *TermMemory) MarshalJSON() ([]byte, error) {
	f := File{}
	for _, t := range m.Terms() {
		f.Terms = append(f.Terms, *t)
	}
	if len(f.Terms) > 0 {
		f.Scope = f.Terms[0].Scope
	}
	return json.MarshalIndent(f, "", "  ")
}

// matchesTerm reports whether a chosen target form is the banned one. A banned
// multiword phrase matches the chosen form as a substring, which is what catches
// "standard oil" inside a longer rendering.
func matchesTerm(forbidden, chosen string) bool {
	if forbidden == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(forbidden), strings.TrimSpace(chosen)) {
		return true
	}
	return strings.Contains(strings.ToLower(chosen), strings.ToLower(forbidden))
}

func containsTerm(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func unique(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func appendUniqueLang(s []lang.Lang, v lang.Lang) []lang.Lang {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func sortedLangs(s []lang.Lang) []lang.Lang {
	out := append([]lang.Lang(nil), s...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func targetOr(target, fallback lang.Lang) lang.Lang {
	if target != "" {
		return target
	}
	return fallback
}
