package lexicon

import (
	"os"
	"sort"
	"strings"
	"sync"
)

// A LexicalProvider supplies predicate candidates for a surface form.
//
// It exists because a curated table cannot cover prose. The corpus in this
// repository is Miyazawa Kenji's 1920s Japanese, and the diagnosis said the
// bottleneck is 35 distinct unknown lexemes — 落ち, きらめき, 置きすて, 浮ん —
// none of which any hand-written table would contain. Typing them in would fix
// this one book and nothing else, which is the failure mode plan.md §25 warns
// about from the other side: unresolved beats invented, and an invented
// inventory is just unresolved with extra steps.
//
// So the knowledge lives behind an interface, exactly as the morphological
// analyser does (plan2 §B2). The curated table is one provider. A JMdict- or
// WordNet-backed provider is another. Nothing in the core imports them, and
// neither the dictionaries nor their sizes are compiled in.
type Provider interface {
	// Name identifies the provider in the trace, so an answer can be traced to
	// where it came from.
	Name() string
	// SensesJP returns weighted predicate candidates for a Japanese surface.
	SensesJP(surface string) []SenseHit
	// SensesEN returns weighted predicate candidates for an English surface.
	SensesEN(surface string) []SenseHit
	// Ready reports whether the provider can answer. A provider whose data file
	// is absent is simply not consulted, which is the difference between "this
	// environment has no JMdict" and "the system does not know the word".
	Ready() bool
	// Stats reports coverage for the UI.
	Stats() map[string]int
}

// TableProvider is the curated inventory compiled into this package.
type TableProvider struct {
	lex *Lexicon
}

// NewTableProvider wraps a lexicon as a provider.
func NewTableProvider(l *Lexicon) *TableProvider { return &TableProvider{lex: l} }

// Name implements Provider.
func (p *TableProvider) Name() string { return "curated-table" }

// SensesJP implements Provider.
func (p *TableProvider) SensesJP(surface string) []SenseHit {
	if p == nil || p.lex == nil {
		return nil
	}
	return p.lex.SensesJP(surface)
}

// SensesEN implements Provider.
func (p *TableProvider) SensesEN(surface string) []SenseHit {
	if p == nil || p.lex == nil {
		return nil
	}
	return p.lex.SensesEN(surface)
}

// Ready implements Provider. The curated table is always available.
func (p *TableProvider) Ready() bool { return p != nil && p.lex != nil }

// Stats implements Provider.
func (p *TableProvider) Stats() map[string]int {
	if p == nil || p.lex == nil {
		return nil
	}
	st := p.lex.Stats()
	return map[string]int{
		"jpSurfaces":    st.JPSurfaces,
		"enSurfaces":    st.ENSurfaces,
		"senseHits":     st.SenseHits,
		"families":      st.Families,
		"unknownSenses": st.UnknownSenseIDs,
	}
}

// Set is an ordered provider chain. Earlier providers win, so the curated table
// stays the authority for what it knows and an external dictionary fills the
// gaps rather than overruling it. That ordering matters: a general dictionary's
// first sense for a polysemous verb is often not the one a translation wants,
// and letting it outrank a curated reading would silently degrade the parts of
// the system that work.
type Set struct {
	providers []Provider
}

// NewSet builds a provider chain. Nil providers are skipped.
func NewSet(providers ...Provider) *Set {
	s := &Set{}
	for _, p := range providers {
		if p != nil {
			s.providers = append(s.providers, p)
		}
	}
	return s
}

// Add appends a provider at the end of the chain.
func (s *Set) Add(p Provider) {
	if p != nil {
		s.providers = append(s.providers, p)
	}
}

// Name implements Provider, so a chain can stand in wherever a provider can.
// The name names the whole chain, because a question answered by two providers
// should not claim to have been answered by whichever happened to be first.
func (s *Set) Name() string {
	if s == nil {
		return "none"
	}
	names := s.Names()
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, "+")
}

// Ready implements Provider.
func (s *Set) Ready() bool { return s != nil && len(s.Names()) > 0 }

// Providers lists the chain in preference order.
func (s *Set) Providers() []Provider {
	if s == nil {
		return nil
	}
	return s.providers
}

// SensesJP returns the first non-empty answer from the chain.
func (s *Set) SensesJP(surface string) []SenseHit {
	if s == nil || surface == "" {
		return nil
	}
	for _, p := range s.providers {
		if !p.Ready() {
			continue
		}
		if hits := p.SensesJP(surface); len(hits) > 0 {
			return hits
		}
	}
	return nil
}

// SensesEN returns the first non-empty answer from the chain.
func (s *Set) SensesEN(surface string) []SenseHit {
	if s == nil || surface == "" {
		return nil
	}
	for _, p := range s.providers {
		if !p.Ready() {
			continue
		}
		if hits := p.SensesEN(surface); len(hits) > 0 {
			return hits
		}
	}
	return nil
}

// Names lists the ready providers, for the trace.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, p := range s.providers {
		if p.Ready() {
			out = append(out, p.Name())
		}
	}
	return out
}

// Stats aggregates coverage across the ready providers.
func (s *Set) Stats() map[string]int {
	out := map[string]int{}
	if s == nil {
		return out
	}
	names := []string{}
	for _, p := range s.providers {
		if !p.Ready() {
			continue
		}
		names = append(names, p.Name())
		for k, v := range p.Stats() {
			out[k+":"+p.Name()] = v
		}
	}
	sort.Strings(names)
	out["providers"] = len(names)
	return out
}

// FileProvider reads a simple TSV sense table from disk:
//
//	# surface<TAB>SENSE:weight[,SENSE:weight...]
//	読む	WORK.04:0.94	MENTAL.11:0.04
//
// It exists so an external inventory can be dropped in without a code change
// and without compiling a large asset into the binary, which is the boundary
// plan2 asks for. It is deliberately not a JMdict parser: JMdict is an XML
// file of a hundred megabytes with its own inflection and priority semantics,
// and pretending a two-line reader handles it would produce wrong senses with
// more confidence than the curated table has.
//
// The ontology stays closed. A row here may only name a sense the ontology
// already defines; one that names an unknown sense is discarded, the same way
// the verifier discards a target feature with no provenance path (plan.md §22).
// So a provider extends coverage of the inventory, it does not extend the
// inventory — adding senses is a change to the ontology and has to be reviewed
// as one, because a sense is a claim about how the world works rather than a
// fact about a word.
//
// Where the data comes from is the operator's choice and must be recorded: the
// file records its own provenance in the first comment line.
type FileProvider struct {
	path string
	name string
	once sync.Once
	jp   map[string][]SenseHit
	en   map[string][]SenseHit
	ok   bool
	n    int
}

// NewFileProvider returns a provider backed by a TSV file. It reports itself
// unready when the file is absent, so the chain falls through silently and the
// trace can say why.
func NewFileProvider(path, name string) *FileProvider {
	return &FileProvider{path: path, name: name}
}

func (p *FileProvider) load() {
	p.once.Do(func() {
		if p == nil || p.path == "" {
			return
		}
		data, err := os.ReadFile(p.path)
		if err != nil {
			return
		}
		p.jp = map[string][]SenseHit{}
		p.en = map[string][]SenseHit{}
		for i, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Split(line, "\t")
			if len(fields) < 2 {
				continue
			}
			surface := strings.TrimSpace(fields[0])
			hits := parseSpec(strings.Join(fields[1:], "\t"), p.path+":"+itoa(i))
			if surface == "" || len(hits) == 0 {
				continue
			}
			if isJapaneseSurface(surface) {
				p.jp[surface] = hits
			} else {
				p.en[normalizeEN(surface)] = hits
			}
			p.n++
		}
		p.ok = p.n > 0
	})
}

// Name implements Provider.
func (p *FileProvider) Name() string {
	if p == nil || p.name == "" {
		return "file"
	}
	return p.name
}

// Ready implements Provider.
func (p *FileProvider) Ready() bool {
	if p == nil {
		return false
	}
	p.load()
	return p.ok
}

// SensesJP implements Provider.
func (p *FileProvider) SensesJP(surface string) []SenseHit {
	if !p.Ready() {
		return nil
	}
	if hits, ok := p.jp[surface]; ok {
		return copyHits(hits)
	}
	return nil
}

// SensesEN implements Provider.
func (p *FileProvider) SensesEN(surface string) []SenseHit {
	if !p.Ready() {
		return nil
	}
	if hits, ok := p.en[normalizeEN(surface)]; ok {
		return copyHits(hits)
	}
	return nil
}

// Stats implements Provider.
func (p *FileProvider) Stats() map[string]int {
	if !p.Ready() {
		return nil
	}
	return map[string]int{"entries": p.n, "jp": len(p.jp), "en": len(p.en)}
}

func isJapaneseSurface(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x3040 && r <= 0x30FF, r >= 0x4E00 && r <= 0x9FFF:
			return true
		case r < 128:
			continue
		default:
			return false
		}
	}
	return false
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ProviderFromEnv builds the chain the process should use, consulting the
// environment for an external sense table.
//
// JEV_LEXICON_TSV points at a TSV inventory; when it is absent the chain is the
// curated table alone, which is the state of an environment with no external
// knowledge installed.
func ProviderFromEnv() *Set {
	s := NewSet(NewTableProvider(Default()))
	if path := os.Getenv("JEV_LEXICON_TSV"); path != "" {
		s.Add(NewFileProvider(path, "tsv:"+path))
	}
	return s
}
