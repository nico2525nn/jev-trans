// Package forest defines the packed (shared-subtree) representations the
// pipeline uses instead of committing to a single parse.
//
// Three artifacts share one node machinery because they share one discipline
// (plan.md §24, §38, §58):
//
//	Morph — the morphological lattice: every segmentation of the input, with
//	        unknown-word alternatives kept rather than discarded.
//	Synt  — the packed syntactic forest: every derivation compatible with the
//	        rules, deduplicated by shared subtree.
//	Real  — the packed realization forest: candidate target strings that share
//	        common material, so many strings cost few nodes.
//
// A node holds alternatives; an alternative either supplies a terminal Lex or
// a list of child node ids. Because sub-forests are interned by content,
// alternative derivations converge on shared nodes instead of duplicating
// them. The forest is never collapsed: expansion happens only where a caller
// actually needs strings.
package forest

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/nico2525nn/jev-trans/internal/lang"
)

// POS is a coarse part of speech normalized across both languages so the
// semantic layer is written once.
type POS string

const (
	POSNoun     POS = "NOUN"
	POSProper   POS = "PROPER"
	POSPronoun  POS = "PRONOUN"
	POSVerb     POS = "VERB"
	POSAux      POS = "AUX"
	POSAdj      POS = "ADJ"
	POSAdv      POS = "ADV"
	POSParticle POS = "PARTICLE"
	POSDet      POS = "DET"
	POSPrep     POS = "PREP"
	POSConj     POS = "CONJ"
	POSPunct    POS = "PUNCT"
	POSNum      POS = "NUM"
	POSInterj   POS = "INTERJ"
	POSAffix    POS = "AFFIX"
	POSUnknown  POS = "UNKNOWN"
)

// Script of a morpheme, used to route unknown words.
type Script string

const (
	ScriptKanji    Script = "kanji"
	ScriptKatakana Script = "katakana"
	ScriptHiragana Script = "hiragana"
	ScriptLatin    Script = "latin"
	ScriptDigit    Script = "digit"
	ScriptOther    Script = "other"
)

// posTags maps the Universal POS tag set — the one Sudachi, MeCab and UniDic all
// speak — onto the coarse parts of speech the pipeline reasons about. The
// mapping is deliberately lossy: the JLIR needs to know a token is a verb or a
// noun, not which cell of the JPOS grid it came from.
var posTags = map[string]POS{
	"名詞": POSNoun, "普通名詞": POSNoun, "固有名詞": POSProper, "人名": POSProper,
	"地名": POSProper, "組織名": POSProper, "動詞": POSVerb, "形容詞": POSAdj,
	"形状詞": POSAdj, "副詞": POSAdv, "連体詞": POSAdj, "助詞": POSParticle,
	"助動詞": POSAux, "接続詞": POSConj, "感動詞": POSInterj, "連体化": POSNoun,
	"N": POSProper, "名詞-普通名詞": POSNoun, "名詞-固有名詞": POSProper,
	"名詞-人名": POSProper, "名詞-地名": POSProper, "動詞-一般": POSVerb,
	"動詞-非自立可能": POSAux, "形容詞-一般": POSAdj, "形状詞-一般": POSAdj,
	"副詞-一般": POSAdv, "助詞-格助詞": POSParticle, "助詞-係助詞": POSParticle,
	"助詞-副助詞": POSParticle, "助詞-接続助詞": POSParticle, "助詞-終助詞": POSParticle,
	"助動詞-助動詞-一般": POSAux, "助動詞-助動詞-過去": POSAux,
	"接頭辞": POSAffix, "接尾辞": POSAffix, "記号": POSPunct, "空白": POSPunct,

	// Pronouns. 誰 and 私 are 代名詞 in SudachiDict, and without this every
	// pronoun-headed argument fell out of noun-phrase attachment: 「誰が云ふ。」 had
	// no NP for が to bind to, so the event got zero arguments and the sentence
	// died as no_arguments_bound. The tag was missing, not the analysis.
	"代名詞": POSPronoun, "代名詞-一般": POSPronoun, "代名詞-普通名詞": POSPronoun,
	"代名詞-人名": POSPronoun, "代名詞-地名": POSPronoun, "代名詞-指示詞": POSPronoun,

	// Numeric and non-nominal first-level tags that appear in the JPOS grid.
	"数詞": POSNum, "接頭詞": POSAffix, "その他": POSUnknown, "空白文字": POSPunct,
}

// internalPOS is the set of coarse parts of speech, so a backend that already
// speaks this vocabulary (the builtin one does) round-trips unchanged instead
// of being re-interpreted as a Universal tag it is not.
var internalPOS = func() map[string]POS {
	m := make(map[string]POS, 16)
	for _, p := range []POS{
		POSNoun, POSProper, POSPronoun, POSVerb, POSAux, POSAdj, POSAdv,
		POSParticle, POSPunct, POSNum, POSInterj, POSAffix, POSUnknown,
	} {
		m[string(p)] = p
	}
	return m
}()

// POSByTag maps a Universal POS tag to a coarse part of speech. A tag that is
// already one of ours passes through. An unmapped tag is a genuinely unknown POS
// rather than an error, so it falls back to POSUnknown and the surface stays
// analyzable.
func POSByTag(tag string) (POS, bool) {
	if p, ok := internalPOS[tag]; ok {
		return p, true
	}
	p, ok := posTags[tag]
	return p, ok
}

// POSFromTags resolves a backend's full POS tuple to a coarse part of speech.
//
// Only the first tag used to be consulted, and that is a trap: SudachiDict
// analyses 誰 as 代名詞 but analyses many words as 名詞,代名詞 or
// 動詞,非自立可能, so a tag the table did not know about silently produced a
// token with no part of speech at all. A word with no POS is a word the noun
// phrase builder cannot use, and the symptom arrives three stages later as a
// missing argument.
//
// The tuple is scanned left to right and the first tag the table recognises
// wins, which is the reading the order of the tuple already implies: the
// leftmost tag is the most general and the rightmost the most specific.
func POSFromTags(tags []string) POS {
	for _, t := range tags {
		if p, ok := POSByTag(t); ok {
			return p
		}
	}
	return ""
}

// Morph is one node of the morphological lattice. Surface, dictionary form,
// semantic lemma and morphological features are all separate, because the
// projection stage needs tense and aspect recovered from inflection rather
// than re-guessed from the surface string.
type Morph struct {
	ID      string            `json:"id"`
	Surface string            `json:"surface"`
	Base    string            `json:"base"` // dictionary form after de-inflection
	Lem     string            `json:"lemma"`
	POS     POS               `json:"pos"`
	Script  Script            `json:"script"`
	Start   int               `json:"start"`
	End     int               `json:"end"`
	Feats   map[string]string `json:"features,omitempty"`

	// Unknown marks a morpheme reconstructed by rule rather than found in the
	// dictionary. The pipeline surfaces these instead of trusting them.
	Unknown bool `json:"unknown,omitempty"`
	Dict    bool `json:"dict,omitempty"`
	// Alternatives lists other dictionary entries sharing this surface.
	Alternatives []string `json:"alternatives,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

// Feat returns a morphological feature.
func (m *Morph) Feat(k string) string { return m.Feats[k] }

// Case returns the Japanese case-particle role (ga/wo/ni/...) or "".
func (m *Morph) Case() string { return m.Feats["case"] }

// IsTopic reports a は acting as topic marker.
func (m *Morph) IsTopic() bool { return m.Feat("case") == "wa" }

// Text returns the covered source substring.
func (m *Morph) Text(src string) string {
	if src == "" {
		return m.Surface
	}
	if m.Start >= 0 && m.End <= len(src) && m.Start < m.End {
		return src[m.Start:m.End]
	}
	return m.Surface
}

// MorphAlt is one segmentation of the whole input.
type MorphAlt struct {
	Morphs []*Morph `json:"morphs"`
	Weight float64  `json:"weight"`
	Rule   string   `json:"rule"`
}

// MorphForest is the morphological lattice for one input.
type MorphForest struct {
	Lang           lang.Lang  `json:"lang"`
	Source         string     `json:"source"`
	Paths          []MorphAlt `json:"paths"`
	DictionarySize int        `json:"dictionarySize"`
	UnknownRate    float64    `json:"unknownRate"`
	Notes          []string   `json:"notes,omitempty"`
}

// Best returns the highest-weight path.
func (f *MorphForest) Best() MorphAlt {
	if f == nil || len(f.Paths) == 0 {
		return MorphAlt{}
	}
	b := f.Paths[0]
	for _, p := range f.Paths[1:] {
		if p.Weight > b.Weight {
			b = p
		}
	}
	return b
}

// Segments renders the best path.
func (f *MorphForest) Segments() []string {
	b := f.Best()
	out := make([]string, len(b.Morphs))
	for i, m := range b.Morphs {
		out[i] = m.Surface
	}
	return out
}

// --- packed forest -------------------------------------------------------

// Alt is one expansion of a node: either a terminal Lex or child nodes.
type Alt struct {
	Lex         string   `json:"lex,omitempty"`
	Children    []string `json:"children,omitempty"`
	Probability float64  `json:"probability"`
	Rule        string   `json:"rule,omitempty"`
	Note        string   `json:"note,omitempty"`
	// Hard records whether this branch survived every hard constraint. Only
	// Hard branches may reach the verifier (plan.md §37).
	Hard bool `json:"hard,omitempty"`
	// Trace maps a slot name to the decision id responsible for the choice.
	Trace map[string]string `json:"trace,omitempty"`
}

// Node is a node of a packed forest.
type Node struct {
	ID    string            `json:"id"`
	Label string            `json:"label"`
	Alts  []Alt             `json:"alts"`
	Meta  map[string]string `json:"meta,omitempty"`
	// Shared marks nodes referenced by more than one parent; the UI highlights
	// them so the packing itself is visible.
	Shared bool `json:"shared,omitempty"`
}

// Builder interns packed forests. Two identical sub-forests collapse to one
// node, which is what keeps the node count sub-linear in the derivation count.
type Builder struct {
	mu     sync.Mutex
	nodes  map[string]*Node
	byID   map[string]*Node
	order  []string
	prefix string
	calls  int
}

// NewBuilder returns a builder prefixing node ids, e.g. "s" for syntactic.
func NewBuilder(prefix string) *Builder {
	return &Builder{nodes: map[string]*Node{}, byID: map[string]*Node{}, prefix: prefix}
}

// Add interns a node and returns its id.
func (b *Builder) Add(label string, alts []Alt) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := internKey(label, alts)
	if n, ok := b.nodes[key]; ok {
		n.Shared = true
		return n.ID
	}
	b.calls++
	id := fmt.Sprintf("%s%d", b.prefix, b.calls)
	n := &Node{ID: id, Label: label, Alts: alts}
	b.nodes[key] = n
	b.byID[id] = n
	b.order = append(b.order, id)
	return id
}

// AddTerminal interns a lexical node.
func (b *Builder) AddTerminal(label, lex string, prob float64, rule string) string {
	return b.Add(label, []Alt{{Lex: lex, Probability: prob, Rule: rule, Hard: true}})
}

func internKey(label string, alts []Alt) string {
	var sb strings.Builder
	sb.WriteString(label)
	for _, a := range alts {
		sb.WriteByte('|')
		sb.WriteString(strings.Join(a.Children, ","))
		sb.WriteByte('/')
		sb.WriteString(a.Lex)
	}
	return sb.String()
}

// Root finalizes the forest.
func (b *Builder) Root(rootID string) *Forest {
	f := &Forest{Root: rootID, Index: map[string]*Node{}}
	for _, id := range b.order {
		n := b.byID[id]
		if n != nil {
			f.Nodes = append(f.Nodes, n)
			f.Index[id] = n
		}
	}
	return f
}

// Forest is an immutable packed forest.
type Forest struct {
	Root   string           `json:"root"`
	Nodes  []*Node          `json:"nodes"`
	Index  map[string]*Node `json:"-"`
	Shared int              `json:"shared"`
}

// Node looks up a node by id.
func (f *Forest) Node(id string) *Node {
	if f == nil {
		return nil
	}
	if f.Index != nil {
		return f.Index[id]
	}
	for _, n := range f.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// Tree is one fully expanded derivation.
type Tree struct {
	Label       string  `json:"label"`
	Lex         string  `json:"lex,omitempty"`
	Rule        string  `json:"rule,omitempty"`
	Children    []*Tree `json:"children,omitempty"`
	Probability float64 `json:"probability"`
	Note        string  `json:"note,omitempty"`
}

// Text concatenates terminal lexemes. sep is inserted between lexemes whose
// Meta["spaceAfter"] is not "0"; Japanese lexemes therefore stay glued while
// English words separate.
func (t *Tree) Text() string {
	var sb strings.Builder
	t.write(&sb)
	return sb.String()
}

func (t *Tree) write(sb *strings.Builder) {
	if t.Lex != "" {
		sb.WriteString(t.Lex)
	}
	for _, c := range t.Children {
		if c.Lex != "" && sb.Len() > 0 && needsSpace(sb.String(), c.Lex) {
			sb.WriteString(" ")
		}
		c.write(sb)
	}
}

// needsSpace decides whether a space belongs between what has been written and
// the next lexeme. Japanese text never takes a space; English does, except
// before punctuation.
func needsSpace(written, next string) bool {
	if written == "" {
		return false
	}
	r, _ := lastRune(written)
	n, _ := firstRune(next)
	if isJP(r) || isJP(n) {
		return false
	}
	if isPunct(n) {
		return false
	}
	return true
}

func lastRune(s string) (rune, bool) {
	rs := []rune(s)
	if len(rs) == 0 {
		return 0, false
	}
	return rs[len(rs)-1], true
}

func firstRune(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

func isJP(r rune) bool {
	return (r >= 0x3040 && r <= 0x30FF) || (r >= 0x4E00 && r <= 0x9FFF) || r == 0x3005
}

func isPunct(r rune) bool {
	return strings.ContainsRune(".,!?;:'’)]}", r)
}

// Enumerate expands the forest into at most limit complete trees, most
// probable first. The expansion budget is bounded so a pathological grammar
// cannot hang a request.
func (f *Forest) Enumerate(limit int) []Tree {
	if f == nil || f.Root == "" || limit <= 0 {
		return nil
	}
	budget := limit * 32
	if budget < 256 {
		budget = 256
	}
	var out []Tree
	var walk func(id string, p float64, depth int)
	walk = func(id string, p float64, depth int) {
		if len(out) >= limit || depth > 48 || budget <= 0 {
			return
		}
		budget--
		n := f.Node(id)
		if n == nil {
			return
		}
		alts := append([]Alt(nil), n.Alts...)
		sort.SliceStable(alts, func(i, j int) bool { return alts[i].Probability > alts[j].Probability })
		for _, a := range alts {
			if len(out) >= limit {
				return
			}
			if a.Probability <= 0 && len(alts) > 1 {
				continue
			}
			t := Tree{Label: n.Label, Lex: a.Lex, Rule: a.Rule, Note: a.Note, Probability: p * a.Probability}
			before := len(out)
			for _, cid := range a.Children {
				walk(cid, p*a.Probability, depth+1)
			}
			if len(out) > before {
				// Children already appended their own trees; attach this node's
				// lexical content by prefixing onto each of them.
				for i := before; i < len(out); i++ {
					child := out[i]
					if a.Lex != "" {
						child.Lex = a.Lex + child.Lex
					}
					if t.Rule != "" {
						child.Rule = t.Rule + " > " + child.Rule
					}
					child.Label = n.Label + "/" + child.Label
					out[i] = child
				}
				continue
			}
			// Terminal (or unsatisfiable children): emit this node alone.
			out = append(out, t)
		}
	}
	walk(f.Root, 1, 0)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Probability > out[j].Probability })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Derivation pairs a tree with its probability.
type Derivation struct {
	Tree        *Tree   `json:"tree"`
	Probability float64 `json:"probability"`
}

// Derive returns the top-n derivations with their probabilities.
func (f *Forest) Derive(n int) []Derivation {
	trees := f.Enumerate(n)
	out := make([]Derivation, len(trees))
	for i, t := range trees {
		t := t
		out[i] = Derivation{Tree: &t, Probability: t.Probability}
	}
	return out
}

// Stats summarises forest size for the UI badge.
type Stats struct {
	Nodes     int `json:"nodes"`
	Shared    int `json:"shared"`
	Terminals int `json:"terminals"`
	MaxDepth  int `json:"maxDepth"`
}

// Stats computes the summary.
func (f *Forest) Stats() Stats {
	var s Stats
	if f == nil {
		return s
	}
	s.Nodes = len(f.Nodes)
	var depth func(id string, d int)
	depth = func(id string, d int) {
		n := f.Node(id)
		if n == nil {
			return
		}
		if d > s.MaxDepth {
			s.MaxDepth = d
		}
		for _, a := range n.Alts {
			if len(a.Children) == 0 {
				s.Terminals++
			}
			for _, c := range a.Children {
				depth(c, d+1)
			}
		}
	}
	depth(f.Root, 1)
	for _, n := range f.Nodes {
		if n.Shared {
			s.Shared++
		}
	}
	return s
}

// --- realization forest --------------------------------------------------

// RejectedBranch records a branch killed by a hard constraint together with the
// rule that killed it, so the UI can show *why* "she" never existed rather
// than silently omitting it (plan.md §37, §39).
type RejectedBranch struct {
	Slot   string `json:"slot"`
	Lex    string `json:"lex"`
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
	Stage  string `json:"stage"`
}

// Realization is a packed target forest plus its constraint ledger. It uses
// the same Node/Alt machinery as the syntactic forest; only the bookkeeping
// differs.
type Realization struct {
	Forest   *Forest          `json:"forest"`
	Rejected []RejectedBranch `json:"rejected,omitempty"`
	Notes    []string         `json:"notes,omitempty"`
	// Slots records, per slot, how many branches survived, which the UI shows
	// as a funnel.
	Slots map[string]int `json:"slots,omitempty"`
}

// Candidate is one complete target string.
type Candidate struct {
	Text          string            `json:"text"`
	Probability   float64           `json:"probability"`
	Constructions []string          `json:"constructions,omitempty"`
	Trace         map[string]string `json:"trace,omitempty"`
	Tree          *Tree             `json:"tree,omitempty"`
}

// Candidates expands the realization forest into ranked strings.
func (r *Realization) Candidates(limit int) []Candidate {
	if r == nil || r.Forest == nil {
		return nil
	}
	trees := r.Forest.Enumerate(limit)
	out := make([]Candidate, len(trees))
	for i, t := range trees {
		con, trace := collect(&t)
		out[i] = Candidate{
			Text: t.Text(), Probability: t.Probability,
			Constructions: con, Trace: trace, Tree: &t,
		}
	}
	return out
}

func collect(t *Tree) ([]string, map[string]string) {
	var con []string
	trace := map[string]string{}
	var walk func(n *Tree)
	walk = func(n *Tree) {
		con = append(con, n.Label)
		if n.Rule != "" {
			for _, id := range strings.Fields(n.Rule) {
				trace[n.Label] = strings.Trim(id, "<>")
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(t)
	return con, trace
}

// AddRejected appends to the constraint ledger.
func (r *Realization) AddRejected(rb RejectedBranch) {
	r.Rejected = append(r.Rejected, rb)
}
