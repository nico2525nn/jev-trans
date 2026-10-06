package lex

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/lang"
)

// The morphological backend contract.
//
// plan.md's pipeline asks for a morphological lattice. plan2.md adds the
// requirement that the lattice come from an exchangeable backend rather than
// from the tables compiled into this package: the in-house dictionary is a few
// hundred surfaces and cannot analyse prose, while Sudachi and the UniDic
// family are real morphological analysers with lexicons of a different order.
//
// The boundary is deliberately a process boundary rather than a library one.
// jev-trans core stays standard-library-only and holds no large dictionary; a
// backend is an executable that happens to be on the PATH, or a configured
// command, and the core speaks a small JSON protocol to it. Nothing in the
// pipeline imports a third-party module, and an environment without the
// external analysers still runs: the builtin backend is the fallback, and the
// trace says which one answered.

// Profile selects an analyser configuration. A modern novel and a pre-1946
// literary text want different lexicons — SudachiDict for the former, the
// 国語研 old-kana UniDic builds for the latter — and an Aozora Bunko text has
// to be analysed with the one that knows its spelling.
type Profile string

const (
	// ProfileAuto picks a backend by inspecting the text.
	ProfileAuto Profile = "auto"
	// ProfileModern is ordinary contemporary Japanese.
	ProfileModern Profile = "modern"
	// ProfileOldKanaColloquial is pre-1946 kana orthography, including the
	// iteration marks and ゐ/ゑ the modern lexicon does not carry.
	ProfileOldKanaColloquial Profile = "old-kana-colloquial"
	// ProfileModernLiterary is contemporary spelling in literary prose, which
	// leans on the である form and written copulas.
	ProfileModernLiterary Profile = "modern-literary"
)

func (p Profile) String() string {
	if p == "" {
		return string(ProfileModern)
	}
	return string(p)
}

// valid reports whether p is one of the declared profiles. An unrecognised
// profile falls back rather than erroring, because a typo in a config file
// should not stop a translation.
func (p Profile) Valid() bool {
	switch p {
	case ProfileAuto, ProfileModern, ProfileOldKanaColloquial, ProfileModernLiterary:
		return true
	}
	return false
}

// Token is one morphological unit. The field set is what plan.md §12 and §20
// need downstream, plus the normalized form and reading that an external
// analyser supplies and the in-house one cannot.
type Token struct {
	Surface    string            `json:"surface"`
	Lemma      string            `json:"lemma"`
	BaseForm   string            `json:"baseForm"`
	Normalized string            `json:"normalized"`
	Reading    string            `json:"reading"`
	POS        []string          `json:"pos,omitempty"`
	Features   map[string]string `json:"features,omitempty"`
	Start      int               `json:"start"`
	End        int               `json:"end"`
	Unknown    bool              `json:"unknown,omitempty"`
}

// Key is the canonical lowercase surface, used for lexicon lookups.
func (t Token) Key() string { return t.Lemma }

// Analysis is one backend's answer.
type Analysis struct {
	Tokens []Token `json:"tokens"`
	// Backend, BackendVersion, Dictionary and Profile are recorded so the
	// trace can say exactly which analyser produced a lattice. A translation
	// whose analysis depends on an unavailable dictionary has to be able to
	// show that.
	Backend        string  `json:"backend"`
	BackendVersion string  `json:"backendVersion"`
	Dictionary     string  `json:"dictionary,omitempty"`
	Profile        Profile `json:"profile"`
	ElapsedMS      float64 `json:"elapsedMs"`
	// Notes carries backend diagnostics, such as a fallback.
	Notes []string `json:"notes,omitempty"`
}

// MorphAnalyzer is the exchangeable backend.
//
// A backend must be total: it returns an Analysis or an error, never a partial
// answer with a nil error. The caller decides whether an error means fall back
// or fail, and the trace records which happened.
type MorphAnalyzer interface {
	Name() string
	Version() string
	// Supports reports whether this backend can serve the profile.
	Supports(p Profile) bool
	Analyze(ctx context.Context, text string, p Profile) (*Analysis, error)
}

// --- the builtin backend --------------------------------------------------

// BuiltinAnalyzer is the in-house analyser: it is the only one that needs no
// external resource, and it stays as the fallback so the system is never
// without a morphology stage.
type BuiltinAnalyzer struct{}

// Name implements MorphAnalyzer.
func (BuiltinAnalyzer) Name() string { return "builtin" }

// Version implements MorphAnalyzer.
func (BuiltinAnalyzer) Version() string { return "1" }

// Supports implements MorphAnalyzer. The builtin analyser has no lexicons, so
// it serves every profile equally and equally badly; that is exactly why it is
// the fallback and not the primary.
func (BuiltinAnalyzer) Supports(Profile) bool { return true }

// Analyze implements MorphAnalyzer.
func (BuiltinAnalyzer) Analyze(ctx context.Context, text string, _ Profile) (*Analysis, error) {
	start := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mf := AnalyzeJAIn(ctx, text)
	if mf == nil {
		return nil, fmt.Errorf("builtin analyser returned no lattice")
	}
	out := &Analysis{
		Backend:        "builtin",
		BackendVersion: "1",
		Profile:        ProfileModern,
	}
	for _, m := range mf.Best().Morphs {
		t := Token{
			Surface:  m.Surface,
			Lemma:    m.Lem,
			BaseForm: m.Base,
			POS:      []string{string(m.POS)},
			Features: m.Feats,
			Start:    m.Start,
			End:      m.End,
			Unknown:  !m.Dict,
		}
		if t.Lemma == "" {
			t.Lemma = m.Surface
		}
		out.Tokens = append(out.Tokens, t)
	}
	out.ElapsedMS = float64(time.Since(start).Microseconds()) / 1000
	return out, nil
}

// --- registry -------------------------------------------------------------

// Registry chooses a backend for a profile and records what it chose.
type Registry struct {
	mu       sync.RWMutex
	backends []MorphAnalyzer
	// last records the most recent choice so the pipeline can put it in the
	// trace without plumbing a handle around.
	last *Analysis
}

// NewRegistry returns a registry containing only the builtin backend, which is
// the state of an environment with no external analysers configured.
func NewRegistry() *Registry {
	return &Registry{backends: []MorphAnalyzer{BuiltinAnalyzer{}}}
}

// Register adds a backend, ahead of the ones already present.
func (r *Registry) Register(a MorphAnalyzer) {
	if a == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends = append([]MorphAnalyzer{a}, r.backends...)
}

// Backends lists the registered backends in preference order.
func (r *Registry) Backends() []MorphAnalyzer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]MorphAnalyzer, len(r.backends))
	copy(out, r.backends)
	return out
}

// Analyze returns the best available analysis for the text.
//
// Preference order is: a backend that declares support for the requested
// profile, then any backend that can run at all. A backend that supports the
// profile and then fails is retried on the next candidate, and the reason is
// recorded rather than swallowed — a silent fallback to a 700-surface
// dictionary would produce output that looks fine and is not.
func (r *Registry) Analyze(ctx context.Context, text string, p Profile, l lang.Lang) (*Analysis, error) {
	if !p.Valid() {
		p = ProfileAuto
	}
	if p == ProfileAuto {
		p = DetectProfile(text)
	}
	var rejected []string
	for _, a := range r.backends {
		if a == nil || !a.Supports(p) {
			if a != nil {
				rejected = append(rejected, fmt.Sprintf("%s does not serve profile %q", a.Name(), p))
			}
			continue
		}
		an, err := a.Analyze(ctx, text, p)
		if err != nil {
			// The reason a backend was not used has to travel with the answer.
			// Recording it in the trace but not in the returned Analysis meant
			// that "Sudachi was configured and failed" looked identical to
			// "Sudachi was never installed".
			rejected = append(rejected, fmt.Sprintf("%s: %v", a.Name(), err))
			continue
		}
		if an != nil {
			if len(rejected) > 0 {
				an.Notes = append(an.Notes, rejected...)
				rejected = nil
			}
			an.Profile = p
			if an.Backend == "" {
				an.Backend = a.Name()
			}
			if an.BackendVersion == "" {
				an.BackendVersion = a.Version()
			}
			r.mu.Lock()
			r.last = an
			r.mu.Unlock()
			return an, nil
		}
	}
	// Nothing worked. The builtin never fails on non-empty input, so reaching
	// here means the input itself is unusable.
	if len(rejected) == 0 {
		return nil, fmt.Errorf("no morphological backend is registered")
	}
	return nil, fmt.Errorf("no morphological backend answered (%s)", strings.Join(rejected, "; "))
}

// LastAnalysis returns the most recent successful analysis, for the trace.
func (r *Registry) LastAnalysis() *Analysis {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.last
}

// --- profile detection ----------------------------------------------------

// oldKanaRunes are the pre-1946 forms whose presence identifies a text as
// needing the old-kana lexicon rather than SudachiDict.
var oldKanaRunes = []rune{'ゐ', 'ゑ', 'ゝ', 'ゞ', 'ヽ', 'ヾ'}

// DetectProfile picks a profile from the text itself.
//
// This is a heuristic and is documented as one. It answers "which lexicon
// should be consulted", which is a routing decision, not a linguistic claim; the
// decision layer is free to revisit it once candidates exist.
func DetectProfile(text string) Profile {
	if text == "" {
		return ProfileModern
	}
	old, hira, kata, kanji := 0, 0, 0, 0
	for _, r := range text {
		switch {
		case strings.ContainsRune(string(oldKanaRunes), r):
			old++
		case r >= 'ぁ' && r <= 'ゖ':
			hira++
		case r >= 'ァ' && r <= 'ヶ':
			kata++
		case r >= '一' && r <= '鿿', r >= '㐀' && r <= '䶿':
			kanji++
		}
	}
	total := old + hira + kata + kanji
	if total == 0 {
		return ProfileModern
	}
	if float64(old)/float64(total) > 0.001 {
		return ProfileOldKanaColloquial
	}
	// である and literary connectives are the modern-literary marker.
	if strings.Contains(text, "である") || strings.Contains(text, "optotic") {
		return ProfileModernLiterary
	}
	if kata > 0 && kata*8 > kanji {
		return ProfileModern
	}
	return ProfileModern
}

// --- bridging to the existing lattice --------------------------------------

// Lattice converts a backend analysis into the forest type the parsers consume,
// so every downstream stage is unchanged whichever backend answered.
func (a *Analysis) Lattice(source string) *forest.MorphForest {
	mf := &forest.MorphForest{
		Lang:   lang.JA,
		Source: source,
		Notes:  append([]string(nil), a.Notes...),
	}
	morphs := make([]*forest.Morph, 0, len(a.Tokens))
	for i, t := range a.Tokens {
		feats := map[string]string{}
		for k, v := range t.Features {
			feats[k] = v
		}
		if len(t.POS) > 0 {
			if feats["pos"] == "" {
				feats["pos"] = t.POS[0]
			}
			// A Universal POS tuple carries the conjugation in its later
			// elements: 助動詞-デス, 終止形-一般, 未然形-一般. The core's clause
			// segmentation decides what is a predicate from the copula, tense
			// and mood features, so those elements have to be translated rather
			// than dropped. Dropping them made です look like a bare auxiliary
			// with no predicate, and every copular sentence became a fragment.
			applyJapaneseFeatures(feats, t.POS, t.Surface)
		}
		m := &forest.Morph{
			ID:      fmt.Sprintf("m%d", i+1),
			Surface: t.Surface,
			Base:    t.BaseForm,
			Lem:     t.Lemma,
			Start:   t.Start,
			End:     t.End,
			Feats:   feats,
			Dict:    !t.Unknown,
			Unknown: t.Unknown,
			Script:  scriptOf(t.Surface),
		}
		if len(t.POS) > 0 {
			if pos, ok := forest.POSByTag(t.POS[0]); ok {
				m.POS = pos
			}
		}
		morphs = append(morphs, m)
	}
	// One path containing every token: a MorphAlt is a whole segmentation of
	// the input, not one morpheme.
	if len(morphs) > 0 {
		mf.Paths = []forest.MorphAlt{{Morphs: morphs, Weight: 1, Rule: a.Backend}}
	}
	return mf
}

// Unresolved returns the surfaces the backend could not identify, sorted by
// frequency so the loss report names the worst offenders first.
func (a *Analysis) Unresolved() []string {
	counts := map[string]int{}
	for _, t := range a.Tokens {
		if t.Unknown {
			counts[t.Surface]++
		}
	}
	out := make([]string, 0, len(counts))
	for s := range counts {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

func scriptOf(s string) forest.Script {
	for _, r := range s {
		switch {
		case r >= 0x30A0 && r <= 0x30FF:
			return forest.ScriptKatakana
		case r >= 0x3040 && r <= 0x309F:
			return forest.ScriptHiragana
		case r >= 0x4E00 && r <= 0x9FFF:
			return forest.ScriptKanji
		case r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			return forest.ScriptLatin
		case r >= '0' && r <= '9':
			return forest.ScriptDigit
		}
	}
	return forest.ScriptOther
}

// copulaMarkers are the elements of a Universal POS tuple that identify the
// copula. Sudachi puts them after the coarse part of speech.
var copulaMarkers = []string{"デス", "ダ", "デアル", "ナイ"}

var tenseMarkers = map[string]string{
	"終止形-一般": "present", "連用形-一般": "present", "終止形-过去": "past",
	"連用形-过去": "past", "未然形-一般": "", "命令形-一般": "",
}

var tenseOfConj = map[string]string{
	"終止形": "present", "連用形": "present", "連体形": "present",
	"未然形": "present", "命令形": "imperative", "終止形-过去": "past",
}

// applyJapaneseFeatures translates the Universal POS tuple into the feature
// vocabulary the core reads. A backend that reports a richer analysis should
// not have to also re-implement the core's conventions.
func applyJapaneseFeatures(feats map[string]string, pos []string, surface string) {
	for _, el := range pos {
		for _, marker := range copulaMarkers {
			if el == marker || strings.HasSuffix(el, "-"+marker) {
				feats["copula"] = "1"
			}
		}
		if t, ok := tenseMarkers[el]; ok && t != "" && feats["tense"] == "" {
			feats["tense"] = t
		}
		// A case particle. The core reads Morph.Case(), which is how the clause
		// parser knows that 太郎が carries a subject marker at all, and Sudachi
		// reports it only as 助詞-格助詞 with no case feature. Without this,
		// every argument of an externally analysed sentence silently fell out of
		// the clause: 「太郎が花子に本を渡した」 came out with one theme and no
		// agent and no recipient.
		if isCaseParticleSurface(surface) && feats["case"] == "" {
			feats["case"] = canonicalCaseMarker(surface)
		}
		for pre, val := range tenseOfConj {
			if strings.HasPrefix(el, pre) && feats["tense"] == "" {
				feats["tense"] = val
			}
		}
	}
	if feats["tense"] == "" && strings.Contains(surface, "た") {
		feats["tense"] = "past"
	}
}

// japaneseCaseParticles maps the surface of a Japanese case particle onto the
// marker the core uses. The core keys on the romanised marker because the
// builtin analyser emits that; an external analyser emits the surface, and
// translating it here is what keeps both backends interchangeable.
var japaneseCaseParticles = map[string]string{
	"が": "ga", "で": "de", "に": "ni", "へ": "he", "と": "to",
	"も": "mo", "の": "no", "や": "ya", "か": "ka", "ね": "ne",
	"から": "kara", "まで": "made", "より": "yori", "ほど": "hodo",
}

// isCaseParticleSurface reports whether the token is one of them. Two-character
// markers are checked first because から and まで contain one.
func isCaseParticleSurface(surface string) bool {
	if _, ok := japaneseCaseParticles[surface]; ok {
		return true
	}
	return false
}

// canonicalCaseMarker returns the core's marker for a particle surface, or "".
func canonicalCaseMarker(surface string) string { return japaneseCaseParticles[surface] }
