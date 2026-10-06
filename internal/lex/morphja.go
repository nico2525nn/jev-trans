// Package lex holds the analyzers that turn raw source text into the
// morphological lattice the rest of the pipeline reasons over.
//
// The Japanese analyzer is deliberately boring: a longest-match dictionary
// pass, a rule-based de-inflection pass that decomposes inflected verb and
// adjective forms into dictionary bases plus features, an auxiliary pass that
// keeps ている / てしまう / てください as the separate constituents plan.md §12
// demands, and an explicit unknown fallback so that no character is ever lost.
// Nothing here decides meaning: every segmentation the analyzer can build is
// kept in the lattice, and choosing between them is a later stage's job.
package lex

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nico2525nn/jev-trans/internal/forest"
	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/trace"
)

// Rule names recorded in MorphAlt.Rule and in Morph.Feats["rule"]. They say
// which layer of plan.md §25 fired, so the UI can show why a morpheme exists.
const (
	jaRuleDict      = "dict"
	jaRuleDeinflect = "deinflect"
	jaRuleAux       = "aux"
	jaRuleSplit     = "split"
	jaRuleNumeral   = "numeral"
	jaRuleName      = "name-guess"
	jaRuleUnknown   = "unknown"
	jaRuleSpace     = "whitespace"
)

// Weights, per morpheme, plus one global cost per extra morpheme.
//
// Two properties matter and they pull in opposite directions:
//
//   - an explanation, however small, must beat an unknown span: that is why an
//     unknown morpheme is strongly negative and why every extra morpheme costs
//     jaWMorphCost. Without the global cost 山 + 田 scored better than the
//     dictionary entry 山田.
//   - among explanations the longest lexicon hit must win: 静かな stays one
//     morpheme rather than 静か + sentence-final な, and 日本人 is preferred over
//     日本 + 人 while the split is still kept in the lattice.
const (
	jaWDict     = 2.00
	jaWFunction = -1.00 // a particle or the て-form connective: a grammatically
	// necessary but explanatorily weak morpheme, so it must never win over a
	// lexical explanation of the same span (hence ので rather than の + で)
	jaWDeinflect = 1.80
	jaWAux       = 2.50 // an auxiliary constituent
	jaWConj      = 2.00 // the te-form connective of an auxiliary compound
	jaWNumeral   = 2.20
	jaWName      = -0.50 // a guessed proper noun: better than unknown, worse than the lexicon
	jaWUnknown   = -4.00
	jaWSpace     = -1.00

	jaWRune        = 0.30 // per-rune length bonus
	jaWRankPenalty = 0.50 // per rank step of a homograph (absolute, not relative)
	jaWMorphCost   = 1.40 // charged once per morpheme of a path
)

// Bounds. They keep a pathological input (a megabyte of katakana) linear rather
// than combinatorial; plan.md §24 explicitly prefers a bounded robust parse to
// an unbounded precise one.
const (
	jaMaxCandRunes     = 14
	jaMaxSplitRunes    = 8
	jaMaxInflectRunes  = 14
	jaMaxAuxRunes      = 8
	jaMaxUnknownRunes  = 16
	jaMaxPathsPerPos   = 6
	jaMaxPathsFinal    = 8
	jaMaxMorphsPerPath = 400
	jaLargeInputRunes  = 400
	jaMaxCandsPerPos   = 28
)

// --- lexicon index --------------------------------------------------------

var (
	jaLex      map[string][]jaLexEntry
	jaMaxRunes int

	// jaConj maps an inflected surface to every dictionary base that inflects
	// that way. Building it once by conjugating the lexicon forward is what
	// makes de-inflection both cheap and okurigana-correct: 渡った needs the
	// entry 渡+った → 渡す, which no amount of string surgery on the surface can
	// produce.
	jaConj map[string][]jaReading

	// jaConjByBase is the reverse index, built alongside jaConj so that
	// JAConjugations can answer for one base without walking every surface.
	// internal/lexicon consumes it instead of re-conjugating the lexicon a
	// second time with its own, divergent tables.
	jaConjByBase map[string]map[string]bool

	// jaAdjIStems maps an い-adjective stem (高) to its dictionary form (高い).
	jaAdjIStems map[string]string
)

// jaReading is one precomputed conjugation of one dictionary base.
type jaReading struct {
	base  string
	feats map[string]string
	note  string
}

func init() {
	jaLex = make(map[string][]jaLexEntry, 4096)
	for _, cat := range jaAll {
		for _, e := range cat {
			le := jaLexEntry{
				pos:   jaPOSCodes[e.pos],
				base:  e.base,
				lem:   e.lem,
				feats: jaParseFeats(e.feats),
				rank:  e.rank,
			}
			if le.base == "" {
				le.base = e.surface
			}
			if le.lem == "" {
				le.lem = le.base
			}
			if le.pos == "" {
				le.pos = forest.POSNoun
			}
			if n := utf8.RuneCountInString(e.surface); n > jaMaxRunes {
				jaMaxRunes = n
			}
			key := jaEntryKey(le)
			dup := false
			for _, ex := range jaLex[e.surface] {
				if jaEntryKey(ex) == key {
					dup = true
					break
				}
			}
			if !dup {
				jaLex[e.surface] = append(jaLex[e.surface], le)
			}
		}
	}
	if jaMaxRunes > jaMaxCandRunes {
		jaMaxRunes = jaMaxCandRunes
	}
	jaBuildConjIndex()
}

// RegisterJapaneseVerb adds a verb the analyzer did not already know, and
// conjugates it.
//
// The predicate lexicon and the morphological analyzer used to be two separate
// tables: internal/lexicon held 241 verb bases and internal/lex knew the
// conjugations of 836 surfaces drawn from its own list. A verb in the first and
// not the second could not be segmented at all, so 住っていた came out as
// 住っ/て/いた with two unresolved morphemes and no predicate, and the sentence
// died. On real Aozora prose that was the single largest cause of failure.
//
// internal/lexicon already imports this package, so it can feed its verbs in
// through this hook and there is no cycle. The result is one morphological
// dictionary with two sources rather than two dictionaries.
//
// The class is inferred from the lemma, which is the standard orthographic test
// and is right for the overwhelming majority of Japanese verbs: a する ending is
// ichidan, a う ending is godan and takes its row from the final kana, and
// する/来る/往来する and friends are irregular by an explicit list.
func RegisterJapaneseVerb(lemma string) {
	lemma = strings.TrimSpace(lemma)
	if lemma == "" {
		return
	}
	if _, known := jaLex[lemma]; known {
		return
	}
	class := jaInferVerbClass(lemma)
	jaLex[lemma] = []jaLexEntry{{
		pos: forest.POSVerb, base: lemma, lem: lemma,
		feats: map[string]string{"cj": class, "class": "open_class", "from": "predicate lexicon"},
	}}
	// The forward conjugation tables are replayed lazily rather than here: this
	// is called once per verb from the predicate lexicon, and rebuilding a
	// 8192-entry index 240 times to reach the same result is wasted work.
	jaConjDirty = true
}

// jaConjDirty records that verbs were registered after this package's init ran.
// The predicate lexicon is built lazily, so it always does: package
// initialization order gives no guarantee that its init ran before ours.
var jaConjDirty bool

// ensureConjIndex replays the conjugation tables if anything registered a verb
// since the last time it ran.
func ensureConjIndex() {
	if !jaConjDirty {
		return
	}
	jaBuildConjIndex()
	jaConjDirty = false
}

// jaInferVerbClass classifies a verb lemma from its orthography.
func jaInferVerbClass(lemma string) string {
	switch lemma {
	case "来る", "くる", "いう", "言う", "する", "為る":
		return "k"
	}
	rs := []rune(lemma)
	if len(rs) == 0 {
		return ""
	}
	switch last := rs[len(rs)-1]; last {
	case 'る':
		return "i"
	case 'う':
		return "g"
	case 'く':
		return "g"
	}
	// A す ending can be godan (話す) or ichidan (離す). Both exist; the
	// ichidan reading is rarer, and the godan す row covers the common case.
	if strings.HasSuffix(lemma, "す") {
		return "g"
	}
	return "g"
}

// jaBuildConjIndex conjugates every verb and い-adjective in the lexicon
// once. This is the second layer of plan.md §25: rather than guessing an
// inflection backwards, the analyzer replays the forward tables.
func jaBuildConjIndex() {
	jaConj = make(map[string][]jaReading, 8192)
	jaConjByBase = make(map[string]map[string]bool, 512)
	jaAdjIStems = make(map[string]string, 256)

	verbs := make([]string, 0, 512)
	adjs := make([]string, 0, 128)
	for surface, entries := range jaLex {
		verb, adj := false, false
		for _, e := range entries {
			// Every declared class reaches the index, not just the two that
			// happened to be implemented: a cj=s/cj=r/cj=k row silently
			// contributed no conjugations at all, which is how 来られた became
			// unsegmentable.
			if e.feats["cj"] != "" {
				verb = true
			}
			if e.pos == forest.POSAdj && e.feats["adj"] == "i" {
				adj = true
			}
		}
		if verb {
			verbs = append(verbs, surface)
		}
		if adj {
			adjs = append(adjs, surface)
		}
	}
	sort.Strings(verbs)
	sort.Strings(adjs)

	for _, base := range verbs {
		stem, ok := jaSplitLastRune(base)
		if !ok || stem == "" {
			continue
		}
		class := ""
		for _, e := range jaLex[base] {
			if c := e.feats["cj"]; c != "" {
				class = c
				break
			}
		}
		switch class {
		case "i":
			jaAddConjugations(stem, base, jaIchiForms, "ichidan: ")
		case "g":
			row, ok := jaGodanTableFor(base)
			if !ok {
				continue
			}
			// A verb that opts out of its row's te/past (行く) gets only the
			// irregular pair; every other verb in the row keeps いて / いた,
			// which is correct for 聞く and 書か.
			if ir, ok := jaGodanIrregular[base]; ok {
				jaAddConjugations(stem, base, jaGodanRowForms(row, true), "godan "+row.kana+"-row: ")
				for _, f := range ir {
					jaAddConjugation(stem+f.sfx, base, f.feats, f.note)
				}
				continue
			}
			jaAddConjugations(stem, base, jaGodanRowForms(row, false), "godan "+row.kana+"-row: ")
		case "s", "r", "k":
			// The する / さる / 来る classes have no okurigana row that predicts
			// them, and the members of a class do not even agree with one
			// another: する is irregular where いたす takes the さ-row, なさる
			// takes the さ-row where いらっしゃる takes the ら-row, and くる
			// needs きた where 来る needs 来た. Each verb therefore states its
			// own stem and its own forms; a row with no table contributes
			// nothing rather than a stem that manufactures non-words.
			row, ok := jaIrregularVerbForms[base]
			if !ok {
				continue
			}
			note := "irregular " + class + "-class: "
			jaAddConjugations(row.stem, base, row.forms, note)
			for _, fm := range row.fulls {
				jaAddConjugation(fm.sfx, base, fm.feats, note+fm.note)
			}
		}
	}
	for _, base := range adjs {
		stem, ok := jaSplitLastRune(base)
		if !ok {
			continue
		}
		if _, dup := jaAdjIStems[stem]; !dup {
			jaAdjIStems[stem] = base
		}
	}
}

// sortedJAKeys returns a surface set in sorted order, so every caller that
// exposes a derived list gets the same deterministic answer (house rule 4).
func sortedJAKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func jaAddConjugations(stem, base string, forms []jaForm, prefix string) {
	for _, fm := range forms {
		jaAddConjugation(stem+fm.sfx, base, fm.feats, prefix+fm.note)
	}
}

func jaAddConjugation(key, base string, feats map[string]string, note string) {
	cp := make(map[string]string, len(feats))
	for k, v := range feats {
		cp[k] = v
	}
	jaConj[key] = append(jaConj[key], jaReading{base: base, feats: cp, note: note})
	if jaConjByBase[base] == nil {
		jaConjByBase[base] = make(map[string]bool, 32)
	}
	jaConjByBase[base][key] = true
}

// jaSplitLastRune returns s without its final rune, which is how the
// conjugation stem of a Japanese verb or adjective is recovered.
func jaSplitLastRune(s string) (string, bool) {
	i := len(s) - 1
	for i > 0 && s[i]&0xC0 == 0x80 {
		i--
	}
	if i <= 0 {
		return "", false
	}
	return s[:i], true
}

func jaLastRune(s string) (string, bool) {
	head, ok := jaSplitLastRune(s)
	if !ok {
		return s, true
	}
	return s[len(head):], true
}

// jaEntryKey makes identical table rows collapse instead of showing up as phantom
// alternatives in the lattice.
func jaEntryKey(e jaLexEntry) string {
	keys := make([]string, 0, len(e.feats))
	for k := range e.feats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(e.base)
	sb.WriteByte('/')
	sb.WriteString(e.lem)
	sb.WriteByte('/')
	sb.WriteString(string(e.pos))
	for _, k := range keys {
		sb.WriteByte('/')
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(e.feats[k])
	}
	return sb.String()
}

// jaParseFeats turns "case=ga tense=past" into a map.
func jaParseFeats(s string) map[string]string {
	if s == "" {
		return map[string]string{}
	}
	out := make(map[string]string, 6)
	for _, kv := range strings.Fields(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			out[kv] = "1"
			continue
		}
		out[k] = v
	}
	return out
}

func jaCopyFeats(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// --- inflection -----------------------------------------------------------

// jaForm is an inflectional suffix together with the features it contributes.
type jaForm struct {
	sfx   string
	kind  string
	feats map[string]string
	note  string
}

const (
	jaKindTe    = "te"
	jaKindTa    = "ta"
	jaKindPlain = "plain"
)

func jaFormOf(sfx, kind, note string, kv ...string) jaForm {
	feats := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		feats[kv[i]] = kv[i+1]
	}
	return jaForm{sfx: sfx, kind: kind, feats: feats, note: note}
}

// jaIchiForms covers 一段 verb endings. Longest first, so ませんでした is never read
// as ません plus a stray た. The irregular past (食べた = stem + た) and the
// te-form (食べて = stem + て) are present because 一段 verbs do not keep the
// okurigana a regular table would need.
var jaIchiForms = []jaForm{
	jaFormOf("させられません", jaKindPlain, "causative-passive polite",
		"causative", "1", "passive", "1", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
	jaFormOf("させられる", jaKindPlain, "causative-passive (humble)",
		"causative", "1", "passive", "1", "honorific", "humble"),
	jaFormOf("られました", jaKindPlain, "polite passive",
		"passive", "1", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
	jaFormOf("られない", jaKindPlain, "passive negative", "passive", "1", jaFtNegative, "1"),
	jaFormOf("ませんでした", jaKindPlain, "polite negative past",
		"polite", jaPoliteMasu, jaFtTense, jaTensePast, jaFtNegative, "1"),
	// ました is the polite past of 一段 verbs and must be in the table: without
	// it 食べました falls through to した, and the sentence analyses as a
	// different predicate entirely. The godan table has it as masu+"ました";
	// 一段 verbs need it verbatim because the stem does not change.
	jaFormOf("ました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
	jaFormOf("ましょう", jaKindPlain, "polite volitional", "mood", "volitional", "polite", jaPoliteMasu),
	jaFormOf("ますか", jaKindPlain, "polite question", "mood", "question", "polite", jaPoliteMasu),
	jaFormOf("まして", jaKindPlain, "polite conjunctive", "polite", jaPoliteMasu, jaFtConj, "conjunctive"),
	jaFormOf("ません", jaKindPlain, "polite negative",
		"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
	jaFormOf("ます", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
	jaFormOf("させる", jaKindPlain, "causative", "causative", "1"),
	jaFormOf("られる", jaKindPlain, "passive/potential", "passive", "1", "potential", "1"),
	jaFormOf("たければ", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
	jaFormOf("なかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
	jaFormOf("ない", jaKindPlain, "negative", jaFtNegative, "1"),
	jaFormOf("ず", jaKindPlain, "negative formal", jaFtNegative, "1", "form", "formal"),
	jaFormOf("ぬ", jaKindPlain, "negative formal", jaFtNegative, "1", "form", "formal"),
	jaFormOf("なさいませ", jaKindPlain, "humble request", "politeness", "request", "honorific", "humble"),
	jaFormOf("なさい", jaKindPlain, "polite imperative", "politeness", "command", "honorific", "respectful"),
	jaFormOf("たい", jaKindPlain, "desiderative", "desire", "1"),
	jaFormOf("たら", jaKindPlain, "conditional", jaFtConj, "CONDITION", jaFtTense, jaTensePast),
	jaFormOf("たり", jaKindPlain, "conjunctive", jaFtConj, "AND"),
	jaFormOf("れば", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
	jaFormOf("そう", jaKindPlain, "evidential", "evidential", "1"),
	jaFormOf("ながら", jaKindPlain, "conjunctive", jaFtConj, "AND"),
	jaFormOf("ので", jaKindPlain, "subordinator", jaFtConj, "BECAUSE"),
	jaFormOf("から", jaKindPlain, "subordinator", jaFtConj, "BECAUSE"),
	jaFormOf("ば", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
	jaFormOf("て", jaKindTe, "te-form connective", jaFtConj, "te"),
	jaFormOf("た", jaKindTa, "past", jaFtTense, jaTensePast),
	jaFormOf("は", jaKindPlain, "attributive topic", jaFtCase, "wa"),
	jaFormOf("が", jaKindPlain, "attributive subject", jaFtCase, "ga"),
	jaFormOf("も", jaKindPlain, "attributive additive", jaFtCase, "mo"),
	jaFormOf("に", jaKindPlain, "attributive dative", jaFtCase, "ni"),
}

// jaSuruForms covers the する family. The stem keeps its し, so 学習します
// resolves to 学習する while します resolves to する itself.
var jaSuruForms = []jaForm{
	jaFormOf("しませんでした", jaKindPlain, "polite negative past",
		"polite", jaPoliteMasu, jaFtTense, jaTensePast, jaFtNegative, "1"),
	jaFormOf("しません", jaKindPlain, "polite negative",
		"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
	jaFormOf("できません", jaKindPlain, "polite negative (potential)",
		"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1", "potential", "1"),
	jaFormOf("しないです", jaKindPlain, "negative polite", jaFtNegative, "1", "polite", jaPoliteTei),
	jaFormOf("しました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
	jaFormOf("します", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
	jaFormOf("しましょ", jaKindPlain, "polite volitional", "mood", "volitional", "polite", jaPoliteMasu),
	jaFormOf("しよう", jaKindPlain, "volitional", "mood", "volitional"),
	jaFormOf("したら", jaKindPlain, "conditional", jaFtConj, "CONDITION", jaFtTense, jaTensePast),
	jaFormOf("したり", jaKindPlain, "conjunctive", jaFtConj, "AND"),
	jaFormOf("すれば", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
	jaFormOf("して", jaKindTe, "te-form connective", jaFtConj, "te"),
	jaFormOf("した", jaKindTa, "past", jaFtTense, jaTensePast),
	jaFormOf("して", jaKindPlain, "te-form connective", jaFtConj, "te"),
	jaFormOf("してる", jaKindPlain, "progressive colloquial", "aspect", jaAspectProgressive),
	jaFormOf("しない", jaKindPlain, "negative", jaFtNegative, "1"),
	jaFormOf("する", jaKindPlain, "plain present"),
	jaFormOf("した", jaKindPlain, "plain past", jaFtTense, jaTensePast),
}

// godanTable is one 五段 conjugation row, pre-conjugated.
type godanTable struct {
	kana  string
	forms []jaForm
}

var jaGodanTables = jaBuildGodanTables()

func jaBuildGodanTables() []godanTable {
	type row struct {
		kanas string
		te    string
		ta    string
		neg   string
		masu  string
		e     string
		vol   string
		pass  string
	}
	rows := []row{
		{"う会", "って", "った", "わ", "い", "え", "お", "わ"},
		{"つち", "って", "った", "た", "つ", "て", "と", "た"},
		{"る", "って", "った", "ら", "り", "れ", "ろ", "ら"},
		{"く", "いて", "いた", "か", "き", "け", "こ", "か"},
		{"ぐ", "いで", "いだ", "が", "ぎ", "げ", "ご", "が"},
		{"す", "して", "した", "さ", "し", "せ", "そ", "さ"},
		{"む", "んで", "んだ", "ま", "み", "め", "も", "ま"},
		{"ぶ", "んで", "んだ", "ば", "び", "べ", "ぼ", "ば"},
		{"ぬ", "んで", "んだ", "な", "に", "ね", "の", "な"},
	}
	out := make([]godanTable, 0, len(rows))
	for _, r := range rows {
		forms := []jaForm{
			jaFormOf(r.te, jaKindTe, "te-form connective", jaFtConj, "te"),
			jaFormOf(r.ta, jaKindTa, "past", jaFtTense, jaTensePast),
			jaFormOf(r.neg+"ない", jaKindPlain, "negative", jaFtNegative, "1"),
			jaFormOf(r.neg+"なかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
			jaFormOf(r.neg+"なくて", jaKindTe, "negative te-form", jaFtNegative, "1", jaFtConj, "te"),
			jaFormOf(r.neg+"ず", jaKindPlain, "negative formal", jaFtNegative, "1", "form", "formal"),
			jaFormOf(r.neg+"ないで", jaKindPlain, "negative imperative", jaFtNegative, "1", "mood", "imperative"),
			jaFormOf(r.masu+"ます", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
			jaFormOf(r.masu+"ました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
			jaFormOf(r.masu+"ません", jaKindPlain, "polite negative",
				"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
			jaFormOf(r.neg+"ません", jaKindPlain, "polite negative",
				"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
			jaFormOf(r.masu+"ませんでした", jaKindPlain, "polite negative past",
				"polite", jaPoliteMasu, jaFtTense, jaTensePast, jaFtNegative, "1"),
			jaFormOf(r.masu+"まして", jaKindPlain, "polite conjunctive", "polite", jaPoliteMasu, jaFtConj, "conjunctive"),
			jaFormOf(r.masu+"ましょう", jaKindPlain, "polite volitional", "mood", "volitional", "polite", jaPoliteMasu),
			jaFormOf(r.masu+"ますか", jaKindPlain, "polite question", "mood", "question", "polite", jaPoliteMasu),
			jaFormOf(r.e, jaKindPlain, "imperative", "mood", "imperative"),
			jaFormOf(r.e+"ば", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
			jaFormOf(r.vol, jaKindPlain, "volitional", "mood", "volitional"),
			jaFormOf(r.pass+"れる", jaKindPlain, "passive", "passive", "1"),
			jaFormOf(r.pass+"ない", jaKindPlain, "passive negative", "passive", "1", jaFtNegative, "1"),
			jaFormOf(r.pass+"せる", jaKindPlain, "causative", "causative", "1"),
			jaFormOf(r.pass+"せられる", jaKindPlain, "causative-passive", "causative", "1", "passive", "1"),
			jaFormOf(r.pass+"されます", jaKindPlain, "polite passive", "passive", "1", "polite", jaPoliteMasu),
			jaFormOf(r.e+"る", jaKindPlain, "potential", "potential", "1"),
			jaFormOf(r.e+"ます", jaKindPlain, "polite potential",
				"potential", "1", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
			jaFormOf(r.e+"ない", jaKindPlain, "potential negative", "potential", "1", jaFtNegative, "1"),
			jaFormOf(r.e+"られる", jaKindPlain, "polite potential/passive",
				"potential", "1", "passive", "1", "polite", jaPoliteMasu),
		}
		for _, kana := range r.kanas {
			out = append(out, godanTable{kana: string(kana), forms: forms})
		}
	}
	return out
}

// jaGodanTableFor returns the conjugation table of a godan verb, keyed by its
// okurigana.
func jaGodanTableFor(base string) (godanTable, bool) {
	kana, ok := jaLastRune(base)
	if !ok {
		return godanTable{}, false
	}
	for _, t := range jaGodanTables {
		if t.kana == kana {
			return t, true
		}
	}
	return godanTable{}, false
}

// jaGodanIrreg is one irregular te/ta form of a godan verb. 行く goes
// 行って/行った, which no okurigana row predicts; it is listed rather than
// special-cased in the matcher.
//
// The key is the verb, not the row: the く-row te-form いて is correct for 聞く
// and wrong only for 行く, so a per-kana table would have to suppress it for
// every く-row verb at once.
type jaGodanIrreg struct {
	sfx   string
	feats map[string]string
	note  string
}

var jaGodanIrregular = map[string][]jaGodanIrreg{
	"行く": {
		{sfx: "って", feats: map[string]string{jaFtConj: "te"}, note: "irregular te-form (行く→行って)"},
		{sfx: "った", feats: map[string]string{jaFtTense: jaTensePast}, note: "irregular past (行く→行った)"},
	},
}

// jaGodanRowForms returns the row's forms, with the te-form and past dropped
// when the verb opts out of them.
//
// Emitting both meant 行いて and 行いた were indexed beside the real 行って /
// 行った: the row's shape applied to a verb that does not take it. Dropping the
// regular pair makes the irregular entry the whole answer for such a verb
// rather than an addition to a wrong guess.
func jaGodanRowForms(row godanTable, dropTeTa bool) []jaForm {
	if !dropTeTa {
		return row.forms
	}
	out := make([]jaForm, 0, len(row.forms))
	for _, fm := range row.forms {
		if fm.kind == jaKindTe || fm.kind == jaKindTa {
			continue
		}
		out = append(out, fm)
	}
	return out
}

// jaIrregularVerb is the conjugation data of one verb whose class no okurigana
// row predicts. The class letter in the lexicon only selects which table a verb
// consults; what actually distinguishes する from いたす, なさる from
// いらっしゃる and くる from 来る is lexical, so it is written down per verb
// rather than inferred from the final kana. Deriving it is what produced the
// non-words こない / こます / こて / こた for 来る.
type jaIrregularVerb struct {
	// stem is the form the suffixes below attach to. It is not always the
	// dictionary form minus its last rune: する's is し, so します is し+ます.
	stem  string
	forms []jaForm
	// fulls are whole surfaces the stem rule cannot build: くる takes きて
	// and きた, where the okurigana changes the く instead of following it.
	fulls []jaForm
}

// jaIrregularVerbForms is the irregular-class conjugation table, keyed by
// dictionary form. plan.md §25 step 1 is only honest if the analyzer really
// knows the classes the data declares; a cj=s/cj=r/cj=k row with no entry here
// gets no forward conjugations at all rather than wrong ones.
var jaIrregularVerbForms = map[string]jaIrregularVerb{
	// --- する class ------------------------------------------------------
	// する is genuinely irregular: the stem し takes ます but the negative is
	// しない, and しないでした is not a predictable shape either.
	"する": {stem: "し", forms: jaFormOfSlice(
		jaFormOf("ます", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
		jaFormOf("ました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
		jaFormOf("ません", jaKindPlain, "polite negative",
			"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
		jaFormOf("ませんでした", jaKindPlain, "polite negative past",
			"polite", jaPoliteMasu, jaFtTense, jaTensePast, jaFtNegative, "1"),
		jaFormOf("ましょう", jaKindPlain, "polite volitional", "mood", "volitional", "polite", jaPoliteMasu),
		jaFormOf("ませんか", jaKindPlain, "polite question", "mood", "question", "polite", jaPoliteMasu),
		jaFormOf("まして", jaKindPlain, "polite conjunctive", "polite", jaPoliteMasu, jaFtConj, "conjunctive"),
		jaFormOf("て", jaKindTe, "te-form connective", jaFtConj, "te"),
		jaFormOf("た", jaKindTa, "past", jaFtTense, jaTensePast),
		jaFormOf("ない", jaKindPlain, "negative", jaFtNegative, "1"),
		jaFormOf("なかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
		jaFormOf("ないです", jaKindPlain, "negative polite", jaFtNegative, "1", "polite", jaPoliteTei),
		jaFormOf("ないでした", jaKindPlain, "negative polite past", jaFtNegative, "1", "polite", jaPoliteTei, jaFtTense, jaTensePast),
		jaFormOf("なくて", jaKindTe, "negative te-form", jaFtNegative, "1", jaFtConj, "te"),
		jaFormOf("たい", jaKindPlain, "desiderative", "desire", "1"),
		jaFormOf("なさい", jaKindPlain, "polite imperative", "politeness", "command", "honorific", "respectful"),
		jaFormOf("よう", jaKindPlain, "volitional", "mood", "volitional"),
		jaFormOf("れば", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
		jaFormOf("たら", jaKindPlain, "conditional", jaFtConj, "CONDITION", jaFtTense, jaTensePast),
		jaFormOf("たり", jaKindPlain, "conjunctive", jaFtConj, "AND"),
		jaFormOf("させる", jaKindPlain, "causative", "causative", "1"),
		jaFormOf("させられる", jaKindPlain, "causative-passive", "causative", "1", "passive", "1"),
	)},
	// 致す / いたす / 申す are the humble する verbs. They keep the し of する
	// but not its irregularity: the stem 致 takes 致します, and the negative
	// comes off the さ-row (致さない), not the ない-row.
	"致す": {stem: "致", forms: jaFormOfSlice(
		jaFormOf("します", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
		jaFormOf("しました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
		jaFormOf("しません", jaKindPlain, "polite negative",
			"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
		jaFormOf("しませんでした", jaKindPlain, "polite negative past",
			"polite", jaPoliteMasu, jaFtTense, jaTensePast, jaFtNegative, "1"),
		jaFormOf("して", jaKindTe, "te-form connective", jaFtConj, "te"),
		jaFormOf("した", jaKindTa, "past", jaFtTense, jaTensePast),
		jaFormOf("さない", jaKindPlain, "negative", jaFtNegative, "1"),
		jaFormOf("さなかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
	)},
	"いたす": {stem: "いた", forms: jaFormOfSlice(
		jaFormOf("します", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
		jaFormOf("しました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
		jaFormOf("しません", jaKindPlain, "polite negative",
			"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
		jaFormOf("しませんでした", jaKindPlain, "polite negative past",
			"polite", jaPoliteMasu, jaFtTense, jaTensePast, jaFtNegative, "1"),
		jaFormOf("して", jaKindTe, "te-form connective", jaFtConj, "te"),
		jaFormOf("した", jaKindTa, "past", jaFtTense, jaTensePast),
		jaFormOf("さない", jaKindPlain, "negative", jaFtNegative, "1"),
		jaFormOf("さなかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
	)},
	"申す": {stem: "申", forms: jaFormOfSlice(
		jaFormOf("します", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
		jaFormOf("しました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
		jaFormOf("しません", jaKindPlain, "polite negative",
			"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
		jaFormOf("しませんでした", jaKindPlain, "polite negative past",
			"polite", jaPoliteMasu, jaFtTense, jaTensePast, jaFtNegative, "1"),
		jaFormOf("して", jaKindTe, "te-form connective", jaFtConj, "te"),
		jaFormOf("した", jaKindTa, "past", jaFtTense, jaTensePast),
		jaFormOf("さない", jaKindPlain, "negative", jaFtNegative, "1"),
		jaFormOf("さなかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
	)},
	// --- さる class ------------------------------------------------------
	// なさる is a さる verb: the stem なさ takes the さ-row, so the plain
	// negative is なさない and the te-form is なさって.
	"なさる": {stem: "なさ", forms: jaFormOfSlice(
		jaFormOf("る", jaKindPlain, "plain present"),
		jaFormOf("います", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
		jaFormOf("いました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
		jaFormOf("いません", jaKindPlain, "polite negative",
			"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
		jaFormOf("さない", jaKindPlain, "negative", jaFtNegative, "1"),
		jaFormOf("さなかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
		jaFormOf("さなくて", jaKindTe, "negative te-form", jaFtNegative, "1", jaFtConj, "te"),
		jaFormOf("って", jaKindTe, "te-form connective", jaFtConj, "te"),
		jaFormOf("った", jaKindTa, "past", jaFtTense, jaTensePast),
		jaFormOf("たら", jaKindPlain, "conditional", jaFtConj, "CONDITION", jaFtTense, jaTensePast),
		jaFormOf("たり", jaKindPlain, "conjunctive", jaFtConj, "AND"),
		jaFormOf("させる", jaKindPlain, "causative", "causative", "1"),
		jaFormOf("させられる", jaKindPlain, "causative-passive", "causative", "1", "passive", "1"),
		jaFormOf("される", jaKindPlain, "passive/honorific", "passive", "1", "honorific", "respectful"),
		jaFormOf("たい", jaKindPlain, "desiderative", "desire", "1"),
		jaFormOf("なさい", jaKindPlain, "polite imperative", "politeness", "command", "honorific", "respectful"),
		jaFormOf("よう", jaKindPlain, "volitional", "mood", "volitional"),
		jaFormOf("れば", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
	)},
	// いらっしゃる is also a さる verb but takes the ら-row, so it shares
	// almost nothing with なさる. That disagreement is exactly why the class
	// cannot be generated from a row and each verb gets its own entry.
	"いらっしゃる": {stem: "いらっしゃ", forms: jaFormOfSlice(
		jaFormOf("る", jaKindPlain, "plain present"),
		jaFormOf("います", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
		jaFormOf("いました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
		jaFormOf("いません", jaKindPlain, "polite negative",
			"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
		jaFormOf("らない", jaKindPlain, "negative", jaFtNegative, "1"),
		jaFormOf("らなかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
		jaFormOf("って", jaKindTe, "te-form connective", jaFtConj, "te"),
		jaFormOf("った", jaKindTa, "past", jaFtTense, jaTensePast),
		jaFormOf("たら", jaKindPlain, "conditional", jaFtConj, "CONDITION", jaFtTense, jaTensePast),
		jaFormOf("たり", jaKindPlain, "conjunctive", jaFtConj, "AND"),
		jaFormOf("ないです", jaKindPlain, "negative polite", jaFtNegative, "1", "polite", jaPoliteTei),
		jaFormOf("たい", jaKindPlain, "desiderative", "desire", "1"),
		jaFormOf("よう", jaKindPlain, "volitional", "mood", "volitional"),
	)},
	// --- 来る class ------------------------------------------------------
	// 来る is irregular in the strongest sense: it has no okurigana, its
	// negative is 来ない, and its honorific (尊敬) is 来られる. The honorific
	// past 来られた is the most frequent Japanese past form in polite prose
	// and must be recoverable, so られる/られた are in the table.
	"来る": {stem: "来", forms: jaFormOfSlice(
		jaFormOf("る", jaKindPlain, "plain present"),
		jaFormOf("ます", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
		jaFormOf("ました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
		jaFormOf("ません", jaKindPlain, "polite negative",
			"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
		jaFormOf("ませんでした", jaKindPlain, "polite negative past",
			"polite", jaPoliteMasu, jaFtTense, jaTensePast, jaFtNegative, "1"),
		jaFormOf("ましょう", jaKindPlain, "polite volitional", "mood", "volitional", "polite", jaPoliteMasu),
		jaFormOf("て", jaKindTe, "te-form connective", jaFtConj, "te"),
		jaFormOf("た", jaKindTa, "past", jaFtTense, jaTensePast),
		jaFormOf("ない", jaKindPlain, "negative", jaFtNegative, "1"),
		jaFormOf("なかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
		jaFormOf("ないです", jaKindPlain, "negative polite", jaFtNegative, "1", "polite", jaPoliteTei),
		jaFormOf("なくて", jaKindTe, "negative te-form", jaFtNegative, "1", jaFtConj, "te"),
		jaFormOf("ず", jaKindPlain, "negative formal", jaFtNegative, "1", "form", "formal"),
		jaFormOf("ないで", jaKindPlain, "negative imperative", jaFtNegative, "1", "mood", "imperative"),
		jaFormOf("られる", jaKindPlain, "honorific/potential/passive",
			"honorific", "respectful", "potential", "1", "passive", "1"),
		jaFormOf("られない", jaKindPlain, "honorific negative",
			"honorific", "respectful", "potential", "1", "passive", "1", jaFtNegative, "1"),
		jaFormOf("られました", jaKindPlain, "honorific past (polite)",
			"honorific", "respectful", "potential", "1", "passive", "1",
			jaFtTense, jaTensePast, "polite", jaPoliteMasu),
		// られた is the honorific past in the plain register, and it is the
		// single most frequent way a past-tense verb appears in modern
		// Japanese: 先生が来られた. Nothing else produces this surface, so
		// without the row the whole verb is unsegmentable.
		jaFormOf("られた", jaKindTa, "honorific past",
			"honorific", "respectful", "potential", "1", "passive", "1", jaFtTense, jaTensePast),
		jaFormOf("させる", jaKindPlain, "causative", "causative", "1"),
		jaFormOf("させられる", jaKindPlain, "causative-passive", "causative", "1", "passive", "1"),
		jaFormOf("ないです", jaKindPlain, "negative polite", jaFtNegative, "1", "polite", jaPoliteTei),
		jaFormOf("ないでした", jaKindPlain, "negative polite past",
			jaFtNegative, "1", "polite", jaPoliteTei, jaFtTense, jaTensePast),
		jaFormOf("たい", jaKindPlain, "desiderative", "desire", "1"),
		jaFormOf("よう", jaKindPlain, "volitional", "mood", "volitional"),
		jaFormOf("い", jaKindPlain, "imperative", "mood", "imperative"),
		jaFormOf("たら", jaKindPlain, "conditional", jaFtConj, "CONDITION", jaFtTense, jaTensePast),
		jaFormOf("たり", jaKindPlain, "conjunctive", jaFtConj, "AND"),
		jaFormOf("れば", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
		jaFormOf("ながら", jaKindPlain, "conjunctive", jaFtConj, "AND"),
		jaFormOf("そう", jaKindPlain, "evidential", "evidential", "1"),
	)},
	// くる is 来る written in kana. Its stem is く, so く+ない = こない is
	// right, but the te-form changes the く rather than following it (きて,
	// not くて), which is why those two are listed as whole surfaces.
	"くる": {
		stem: "く",
		forms: jaFormOfSlice(
			jaFormOf("る", jaKindPlain, "plain present"),
			jaFormOf("ます", jaKindPlain, "polite present", "polite", jaPoliteMasu, jaFtTense, jaTensePresent),
			jaFormOf("ました", jaKindTa, "polite past", "polite", jaPoliteMasu, jaFtTense, jaTensePast),
			jaFormOf("ません", jaKindPlain, "polite negative",
				"polite", jaPoliteMasu, jaFtTense, jaTensePresent, jaFtNegative, "1"),
			jaFormOf("ませんでした", jaKindPlain, "polite negative past",
				"polite", jaPoliteMasu, jaFtTense, jaTensePast, jaFtNegative, "1"),
			jaFormOf("ない", jaKindPlain, "negative", jaFtNegative, "1"),
			jaFormOf("なかった", jaKindPlain, "negative past", jaFtNegative, "1", jaFtTense, jaTensePast),
			jaFormOf("ないです", jaKindPlain, "negative polite", jaFtNegative, "1", "polite", jaPoliteTei),
			jaFormOf("なくて", jaKindTe, "negative te-form", jaFtNegative, "1", jaFtConj, "te"),
			jaFormOf("たら", jaKindPlain, "conditional", jaFtConj, "CONDITION", jaFtTense, jaTensePast),
			jaFormOf("たり", jaKindPlain, "conjunctive", jaFtConj, "AND"),
			jaFormOf("られる", jaKindPlain, "honorific/potential/passive",
				"honorific", "respectful", "potential", "1", "passive", "1"),
			jaFormOf("られない", jaKindPlain, "honorific negative",
				"honorific", "respectful", "potential", "1", "passive", "1", jaFtNegative, "1"),
			jaFormOf("い", jaKindPlain, "imperative", "mood", "imperative"),
			jaFormOf("よう", jaKindPlain, "volitional", "mood", "volitional"),
		),
		fulls: jaFormOfSlice(
			jaFormOf("きて", jaKindTe, "te-form (く→き)", jaFtConj, "te"),
			jaFormOf("きた", jaKindTa, "past (く→き)", jaFtTense, jaTensePast),
		),
	},
}

// jaFormOfSlice is the literal form of a table row, so each entry above reads
// as the conjugations it contributes.
func jaFormOfSlice(fs ...jaForm) []jaForm { return fs }

// jaAdjIForms are the inflected forms of an い-adjective stem.
var jaAdjIForms = []jaForm{
	jaFormOf("かった", jaKindTa, "adjective past", jaFtTense, jaTensePast),
	jaFormOf("くない", jaKindPlain, "negative", jaFtNegative, "1"),
	jaFormOf("くて", jaKindTe, "te-form connective", jaFtConj, "te"),
	jaFormOf("ければ", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
	jaFormOf("く", jaKindPlain, "adverbial"),
	jaFormOf("さ", jaKindPlain, "adjectival noun"),
	jaFormOf("み", jaKindPlain, "adjectival noun"),
	jaFormOf("そう", jaKindPlain, "evidential", "evidential", "1"),
}

// jaAdjIrregular covers いい, whose inflected stem is よ rather than 良.
var jaAdjIrregular = []struct{ stem, base string }{{stem: "よ", base: "良い"}}

// jaNaAdjForms are the inflected forms of a な-adjective / nominal stem.
var jaNaAdjForms = []jaForm{
	jaFormOf("ではない", jaKindPlain, "negative", jaFtNegative, "1"),
	jaFormOf("じゃない", jaKindPlain, "negative colloquial", jaFtNegative, "1"),
	jaFormOf("でした", jaKindTa, "polite past", "polite", jaPoliteTei, jaFtTense, jaTensePast),
	jaFormOf("ですね", jaKindPlain, "agreement", "polite", jaPoliteTei, "sfp", "ne"),
	jaFormOf("ですか", jaKindPlain, "polite question", "polite", jaPoliteTei, "mood", "question"),
	jaFormOf("だ", jaKindPlain, "plain copula", jaFtCopula, "da"),
	jaFormOf("です", jaKindPlain, "polite copula", jaFtCopula, "da", "polite", jaPoliteTei),
	jaFormOf("だった", jaKindTa, "plain copula past", jaFtCopula, "da", jaFtTense, jaTensePast),
	jaFormOf("なら", jaKindPlain, "conditional", jaFtConj, "CONDITION"),
	jaFormOf("な", jaKindPlain, "attributive", jaFtCase, "no"),
	jaFormOf("に", jaKindPlain, "adverbial", jaFtCase, "ni"),
	jaFormOf("の", jaKindPlain, "genitive", jaFtCase, "no"),
	jaFormOf("では", jaKindPlain, "topic/location", jaFtCase, "wa"),
}

// jaAuxRoleKeys are the features that make an entry an auxiliary rather than a
// plain verb: ている is progressive, てしまう is completion, てください is a
// request. Collapsing them into "verb + past" is exactly what plan.md §12
// forbids.
var jaAuxRoleKeys = []string{"aspect", "completion", "politeness", "benefactive", "direction"}

// --- analyzer -------------------------------------------------------------

type jaAnalyzer struct {
	src      string
	runes    []rune
	off      []int // byte offset of each rune; off[n] == len(src)
	n        int
	maxPaths int
	maxCands int
}

func jaNewJAAnalyzer(src string) *jaAnalyzer {
	a := &jaAnalyzer{
		src:      src,
		runes:    []rune(src),
		off:      make([]int, len(src)+1),
		maxPaths: jaMaxPathsPerPos,
		maxCands: jaMaxCandsPerPos,
	}
	pos := 0
	for i, r := range a.runes {
		a.off[i] = pos
		pos += utf8.RuneLen(r)
	}
	a.n = len(a.runes)
	a.off[a.n] = pos
	if a.n > jaLargeInputRunes {
		a.maxPaths = 2
		a.maxCands = 8
	}
	return a
}

// start returns the byte offset of rune index i.
func (a *jaAnalyzer) start(i int) int { return a.off[i] }

// end returns the byte offset just past rune index j.
func (a *jaAnalyzer) end(j int) int { return a.off[j] }

// sub returns the source text of the rune range [i,j).
func (a *jaAnalyzer) sub(i, j int) string { return a.src[a.start(i):a.end(j)] }

// --- public API -----------------------------------------------------------

// JAConjugations returns every surface the analyzer's forward conjugation
// index generates for a dictionary base, sorted. It is the single source of
// truth for "which inflected forms of this word exist": internal/lexicon used
// to keep a second, divergent copy of the whole Japanese conjugation engine,
// and it is that copy which indexed 行いて beside 行って.
//
// A base the analyzer does not know yields no forms. That is the honest answer
// — the lexicon cannot assert a conjugation the morphology engine cannot
// produce — so a caller needing broader coverage must add the verb to the
// analyzer's tables rather than re-deriving the forms here.
// JAClass reports the conjugation class the analyzer holds for a dictionary
// base — "i" (ichidan), "g" (godan), "s" (する), "r" (さる), "k" (来る) — or ""
// when the base is not a verb in the tables.
//
// It exists so that a caller holding its own class annotation can check the two
// against each other instead of drifting apart: the lexicon typed 帰る as
// ichidan, which is what generated 帰た and 帰ます for a godan る verb.
func JAClass(base string) string {
	for _, e := range jaLex[base] {
		if c := e.feats["cj"]; c != "" {
			return c
		}
	}
	return ""
}

func JAConjugations(base string) []string {
	if base == "" {
		return nil
	}
	return sortedJAKeys(jaConjByBase[base])
}

// AnalyzeJA runs the Japanese morphological analyzer and returns the lattice.
//
// Every segmentation of src that the analyzer can build is preserved: the best
// one first, then the genuinely different alternatives. Characters are never
// dropped — an unexplainable span becomes a POSUnknown morpheme and raises
// UnknownRate instead (CONTRACTS, source-analysis requirements).
func AnalyzeJA(src string) *forest.MorphForest {
	return AnalyzeJAIn(context.Background(), src)
}

// AnalyzeJAIn is AnalyzeJA with the ambient trace recorder, so the pipeline can
// show the stage instead of running it invisibly (house rule 3).
func AnalyzeJAIn(ctx context.Context, src string) *forest.MorphForest {
	ensureConjIndex()
	var span *trace.Span
	if rec := trace.From(ctx); rec != nil {
		span = rec.Open(trace.StageMorph, "Japanese morphology")
		defer span.Close()
	}
	mf := jaAnalyze(src)
	if span != nil {
		span.Data(mf)
		span.Count("paths", len(mf.Paths))
		span.Count("morphemes", len(mf.Best().Morphs))
		span.Detail("%d morphemes, %d paths, %.0f%% unknown",
			len(mf.Best().Morphs), len(mf.Paths), mf.UnknownRate*100)
		if mf.UnknownRate > 0 {
			span.Status(trace.StatusWarn)
		}
	}
	return mf
}

func jaAnalyze(src string) *forest.MorphForest {
	mf := &forest.MorphForest{
		Lang:           lang.JA,
		Source:         src,
		Paths:          []forest.MorphAlt{},
		DictionarySize: len(jaLex),
	}
	if src == "" {
		mf.Paths = append(mf.Paths, forest.MorphAlt{Weight: 0, Rule: jaRuleDict})
		mf.Notes = append(mf.Notes, "empty input")
		return mf
	}
	a := jaNewJAAnalyzer(src)
	for _, st := range a.lattice() {
		alt := a.jaMaterialize(st)
		if alt == nil {
			continue
		}
		mf.Paths = append(mf.Paths, *alt)
		if len(mf.Paths) >= jaMaxPathsFinal {
			break
		}
	}
	if len(mf.Paths) == 0 {
		// Defensive: the unknown fallback is total, so this cannot happen, but a
		// lattice with no path would silently drop the whole input.
		mf.Paths = append(mf.Paths, forest.MorphAlt{
			Morphs: []*forest.Morph{{
				Surface: src, Start: 0, End: len(src), POS: forest.POSUnknown,
				Script: forest.Script(lang.RuneScript(jaFirstRune(src))),
				Base:   src, Lem: src, Unknown: true,
				Feats: map[string]string{jaFtConjRule: jaRuleUnknown},
				Notes: []string{"no segmentation could be built for the whole input"},
			}},
			Weight: 0,
			Rule:   jaRuleUnknown,
		})
	}
	unknown := 0
	for _, m := range mf.Paths[0].Morphs {
		if !m.Dict {
			unknown += m.End - m.Start
		}
	}
	mf.UnknownRate = float64(unknown) / float64(len(src))
	if mf.UnknownRate > 0 {
		mf.Notes = append(mf.Notes, fmt.Sprintf(
			"%.0f%% of the input is not in the lexicon; those spans are kept as UNKNOWN morphemes instead of being guessed (plan.md §25)",
			mf.UnknownRate*100))
	}
	if len(mf.Paths) > 1 {
		mf.Notes = append(mf.Notes,
			fmt.Sprintf("%d competing segmentations kept in the lattice", len(mf.Paths)))
	}
	return mf
}

// --- lattice construction -------------------------------------------------

// jaStep is one node of the search: the path that reached rune index pos, the
// candidate that extended it and the resulting weight.
type jaStep struct {
	pos  int
	prev *jaStep
	c    *jaCand
	w    float64
	rule string
}

// jaCand is one reading of a span: a non-empty morpheme sequence plus its
// weight. end is the exclusive rune index the span ends at.
type jaCand struct {
	morphs []*forest.Morph
	end    int
	w      float64
	rule   string
}

func (a *jaAnalyzer) lattice() []*jaStep {
	steps := make([][]*jaStep, a.n+1)
	steps[0] = []*jaStep{{pos: 0, rule: ""}}
	for pos := range a.n {
		if len(steps[pos]) == 0 {
			continue
		}
		for _, c := range a.jaCandsAt(pos) {
			if len(c.morphs) == 0 || c.end <= pos || c.end > a.n {
				continue
			}
			if c.morphs[0].Start != a.start(pos) || c.morphs[len(c.morphs)-1].End != a.end(c.end) {
				continue
			}
			// Every morpheme is charged, not only the extra ones: the constant
			// offset cancels out, so this is "one cost per morpheme" in disguise.
			delta := c.w - jaWMorphCost*float64(len(c.morphs))
			for _, st := range steps[pos] {
				if a.morphCount(st)+len(c.morphs) > jaMaxMorphsPerPath {
					continue
				}
				ns := &jaStep{pos: c.end, prev: st, c: c, w: st.w + delta, rule: jaJoinRule(st.rule, c.rule)}
				steps[c.end] = jaInsertStep(steps[c.end], ns, a.maxPaths)
			}
		}
	}
	return steps[a.n]
}

func (a *jaAnalyzer) morphCount(st *jaStep) int {
	n := 0
	for s := st; s != nil && s.c != nil; s = s.prev {
		n += len(s.c.morphs)
	}
	return n
}

// jaInsertStep keeps the heaviest paths reaching a position.
func jaInsertStep(list []*jaStep, s *jaStep, max int) []*jaStep {
	i := 0
	for i < len(list) && list[i].w >= s.w {
		i++
	}
	if i >= max {
		return list
	}
	list = append(list, nil)
	copy(list[i+1:], list[i:])
	list[i] = s
	if len(list) > max {
		list = list[:max]
	}
	return list
}

func jaJoinRule(prev, add string) string {
	if prev == "" {
		return add
	}
	if prev == add || strings.Contains(prev, "+"+add) {
		return prev
	}
	return prev + "+" + add
}

func (a *jaAnalyzer) jaMaterialize(st *jaStep) *forest.MorphAlt {
	if st == nil {
		return nil
	}
	var chain []*jaCand
	for s := st; s != nil && s.c != nil; s = s.prev {
		chain = append(chain, s.c)
	}
	morphs := make([]*forest.Morph, 0, 16)
	for i := len(chain) - 1; i >= 0; i-- {
		morphs = append(morphs, chain[i].morphs...)
	}
	if len(morphs) == 0 {
		return nil
	}
	sort.SliceStable(morphs, func(i, j int) bool { return morphs[i].Start < morphs[j].Start })
	for i, m := range morphs {
		m.ID = "m" + strconv.Itoa(i+1)
	}
	return &forest.MorphAlt{Morphs: morphs, Weight: st.w, Rule: st.rule}
}

// jaCandsAt returns every reading of the span starting at rune index pos, most
// trustworthy first so that the per-position truncation keeps the good ones.
func (a *jaAnalyzer) jaCandsAt(pos int) []*jaCand {
	out := make([]*jaCand, 0, 24)
	out = a.jaDictCands(pos, out)
	out = a.jaNumeralCands(pos, out)
	out = a.jaDeinflectCands(pos, out)
	out = a.jaAuxCands(pos, out)
	out = a.jaFallbackCands(pos, out)
	if len(out) > a.maxCands {
		out = out[:a.maxCands]
	}
	return out
}

// --- 1. longest-match dictionary lookup ------------------------------------

func (a *jaAnalyzer) jaDictCands(pos int, out []*jaCand) []*jaCand {
	limit := a.n
	if lim := pos + jaMaxRunes; lim < limit {
		limit = lim
	}
	for l := limit - pos; l >= 1; l-- {
		surface := a.sub(pos, pos+l)
		entries := jaLex[surface]
		if len(entries) == 0 {
			continue
		}
		for _, e := range entries {
			base := jaWDict
			if e.pos == forest.POSParticle || e.pos == forest.POSConj || e.pos == forest.POSAffix {
				base = jaWFunction
			}
			out = append(out, &jaCand{
				morphs: []*forest.Morph{a.jaDictMorph(pos, pos+l, surface, entries, e)},
				end:    pos + l,
				w:      base + jaWRune*float64(l) - jaWRankPenalty*float64(e.rank),
				rule:   jaRuleDict,
			})
		}
	}
	return out
}

// jaDictMorph materialises one dictionary hit. Alternatives records the other
// readings of the same surface so a homograph stays visible instead of being
// silently resolved.
func (a *jaAnalyzer) jaDictMorph(i, j int, surface string, entries []jaLexEntry, e jaLexEntry) *forest.Morph {
	m := &forest.Morph{
		Surface: surface,
		Base:    e.base,
		Lem:     e.lem,
		POS:     e.pos,
		Script:  forest.Script(lang.RuneScript(jaFirstRune(surface))),
		Start:   a.start(i),
		End:     a.end(j),
		Feats:   jaCopyFeats(e.feats),
		Dict:    true,
	}
	m.Feats[jaFtConjRule] = jaRuleDict
	if len(entries) > 1 {
		for _, other := range entries {
			if other.base != e.base {
				m.Alternatives = append(m.Alternatives, other.base)
			}
		}
		m.Alternatives = jaDedupeStrings(m.Alternatives)
		if len(m.Alternatives) > 0 {
			m.Notes = append(m.Notes, "homograph: also "+strings.Join(m.Alternatives, ", "))
		}
	}
	if e.rank > 0 {
		m.Notes = append(m.Notes, "non-preferred dictionary reading (rank "+strconv.Itoa(e.rank)+")")
	}
	return m
}

// --- 2. rule-based de-inflection ------------------------------------------

// jaInfl is one de-inflection reading of a surface.
type jaInfl struct {
	base  string
	feats map[string]string
	note  string
	// grounded records that the reconstructed base is a lexicon entry. Every
	// reading the forward conjugation index produces is grounded by
	// construction; the する-compound rule is the one that can propose a base
	// the tables never listed (勉強します → 勉強する), and that reading keeps
	// the unresolved mark of plan.md §25 step 6.
	grounded bool
}

func (a *jaAnalyzer) jaDeinflectCands(pos int, out []*jaCand) []*jaCand {
	limit := a.n
	if lim := pos + jaMaxInflectRunes; lim < limit {
		limit = lim
	}
	for l := limit - pos; l >= 2; l-- {
		surface := a.sub(pos, pos+l)
		for _, infl := range a.jaInflections(surface) {
			// A te-form leaves the connective out: the verb stem is one morpheme
			// and the て belongs to the auxiliary compound that follows it. Keeping
			// them apart is what lets 食べてしまった come out as EAT + connective +
			// completion instead of "EAT + past" (plan.md §12).
			if infl.feats[jaFtConj] == "te" && l > 1 &&
				(strings.HasSuffix(surface, "て") || strings.HasSuffix(surface, "で")) {
				conn := "て"
				if strings.HasSuffix(surface, "で") {
					conn = "で"
				}
				stem := surface[:len(surface)-len(conn)]
				if stem != "" {
					out = append(out, &jaCand{
						morphs: []*forest.Morph{a.jaInflMorph(pos, pos+l-1, stem, infl)},
						end:    pos + l - 1,
						w:      jaWDeinflect + jaWRune*float64(l-1),
						rule:   jaRuleDeinflect,
					})
				}
			}
			out = append(out, &jaCand{
				morphs: []*forest.Morph{a.jaInflMorph(pos, pos+l, surface, infl)},
				end:    pos + l,
				w:      jaWDeinflect + jaWRune*float64(l),
				rule:   jaRuleDeinflect,
			})
		}
	}
	return out
}

// jaInflections returns every dictionary-grounded reading of an inflected surface.
// A reading is only produced when the reconstructed base is actually in the
// lexicon, which is what keeps this layer from inventing words.
func (a *jaAnalyzer) jaInflections(s string) []jaInfl {
	var out []jaInfl
	if len(s) < 2 {
		return out
	}
	// Verb conjugation: the index is keyed by the inflected surface itself, so
	// 渡った finds 渡+った → 渡す with no guesswork about okurigana.
	if readings, ok := jaConj[s]; ok {
		for _, r := range readings {
			// Every key of the conjugation index was generated from a lexicon
			// base, so these readings are grounded by construction.
			out = append(out, jaInfl{base: r.base, feats: r.feats, note: r.note, grounded: true})
		}
	}
	// する-class: the stem keeps its し, so 学習します resolves to 学習する.
	for _, fm := range jaSuruForms {
		if len(s) < len(fm.sfx) || !strings.HasSuffix(s, fm.sfx) {
			continue
		}
		prefix := s[:len(s)-len(fm.sfx)]
		if !a.jaSuruOK(prefix) {
			continue
		}
		out = append(out, jaInfl{
			base:  prefix + "する",
			feats: fm.feats,
			note:  "suru-class: " + fm.note,
			// 勉強します proposes 勉強する, which is not a table entry: the
			// noun 勉強 is what the analyzer actually grounded on.
			grounded: len(jaLex[prefix+"する"]) > 0,
		})
	}
	// い-adjectives.
	for _, fm := range jaAdjIForms {
		if len(s) <= len(fm.sfx) || !strings.HasSuffix(s, fm.sfx) {
			continue
		}
		stem := s[:len(s)-len(fm.sfx)]
		if base, ok := jaAdjIStems[stem]; ok {
			out = append(out, jaInfl{
				base: base, feats: fm.feats,
				note:     "i-adjective: " + fm.note,
				grounded: true,
			})
		}
	}
	for _, ir := range jaAdjIrregular {
		for _, fm := range jaAdjIForms {
			if len(s) <= len(fm.sfx) || !strings.HasSuffix(s, fm.sfx) {
				continue
			}
			if s[:len(s)-len(fm.sfx)] != ir.stem {
				continue
			}
			out = append(out, jaInfl{
				base: ir.base, feats: fm.feats,
				note:     "i-adjective (irregular): " + fm.note,
				grounded: true,
			})
		}
	}
	// な-adjectives and nominal + copula.
	for _, fm := range jaNaAdjForms {
		if len(s) <= len(fm.sfx) || !strings.HasSuffix(s, fm.sfx) {
			continue
		}
		stem := s[:len(s)-len(fm.sfx)]
		if a.jaIsAdjNa(stem) {
			out = append(out, jaInfl{
				base: stem, feats: fm.feats,
				note:     "na-adjective: " + fm.note,
				grounded: true,
			})
		}
	}
	return out
}

func (a *jaAnalyzer) jaInflMorph(i, j int, surface string, infl jaInfl) *forest.Morph {
	e := a.jaPreferredEntry(infl.base)
	m := &forest.Morph{
		Surface: surface,
		Base:    e.base,
		Lem:     e.lem,
		POS:     e.pos,
		Script:  forest.Script(lang.RuneScript(jaFirstRune(surface))),
		Start:   a.start(i),
		End:     a.end(j),
		Feats:   jaCopyFeats(e.feats),
		// Dict says the reconstructed base was actually found in the lexicon,
		// which is the case for every reading that comes out of the forward
		// conjugation index: 飲みました → 飲む, so it is a resolved word.
		// Unknown is reserved by plan.md §25 for an expression that survived
		// *every* decomposition attempt, so it must stay false here unless the
		// base itself is absent from the lexicon — which is exactly what the
		// する-compound rule can produce (勉強します → 勉強する is not a table
		// entry; only 勉強 is). Flagging those keeps the record meaningful
		// instead of drowning it in correctly de-inflected verbs.
		Dict:    infl.grounded,
		Unknown: !infl.grounded,
	}
	for k, v := range infl.feats {
		m.Feats[k] = v
	}
	m.Feats[jaFtConjRule] = jaRuleDeinflect
	m.Notes = append(m.Notes, fmt.Sprintf("%s → %s (%s)", surface, infl.base, infl.note))
	return m
}

// jaClassOf returns the conjugation class recorded in the lexicon, or "".
func (a *jaAnalyzer) jaClassOf(base string) string {
	for _, e := range jaLex[base] {
		if c := e.feats["cj"]; c != "" {
			return c
		}
	}
	return ""
}

// jaSuruOK accepts a する compound whose stem is a known する verb or a noun that
// takes する (勉強します → 勉強する).
func (a *jaAnalyzer) jaSuruOK(prefix string) bool {
	if prefix == "" {
		return true
	}
	if a.jaClassOf(prefix+"する") == "s" {
		return true
	}
	for _, e := range jaLex[prefix] {
		if e.feats["suru"] == "1" {
			return true
		}
	}
	return false
}

func (a *jaAnalyzer) jaIsAdjNa(stem string) bool {
	for _, e := range jaLex[stem] {
		if e.pos == forest.POSAdj && e.feats["adj"] == "na" {
			return true
		}
	}
	return false
}

// jaPreferredEntry picks the best lexicon record for a dictionary form.
func (a *jaAnalyzer) jaPreferredEntry(base string) jaLexEntry {
	entries := jaLex[base]
	if len(entries) == 0 {
		return jaLexEntry{pos: forest.POSUnknown, base: base, lem: base, feats: map[string]string{}}
	}
	best := entries[0]
	for _, e := range entries {
		if e.rank < best.rank {
			best = e
		}
	}
	return best
}

// --- 3. auxiliaries -------------------------------------------------------

// jaAuxCands splits a て/で compound into its constituents. plan.md §12: 食べて
// しまった must surface as EAT + past + completion with the completion kept
// separate from the tense, and てもらえますか must keep its request reading.
func (a *jaAnalyzer) jaAuxCands(pos int, out []*jaCand) []*jaCand {
	if pos >= a.n {
		return out
	}
	limit := a.n
	if lim := pos + 1 + jaMaxAuxRunes; lim < limit {
		limit = lim
	}
	for _, conn := range jaTeConnectives {
		if string(a.runes[pos]) != conn {
			continue
		}
		for l := limit - pos - 1; l >= 1; l-- {
			rest := a.sub(pos+1, pos+1+l)
			readings := a.jaAuxReadings(rest, 0)
			if len(readings) == 0 {
				continue
			}
			for _, ms := range readings {
				morphs := make([]*forest.Morph, 0, len(ms)+1)
				morphs = append(morphs, a.jaConjMorph(pos, pos+1, conn))
				// The constituent surfaces concatenate to rest exactly, so byte
				// offsets follow from the surface lengths.
				bytePos := a.end(pos + 1)
				for _, m := range ms {
					m.Start = bytePos
					m.End = bytePos + len(m.Surface)
					bytePos = m.End
					morphs = append(morphs, m)
				}
				w := jaWConj + jaWRune // the connective itself
				for _, m := range ms {
					w += jaWAux + jaWRune*float64(utf8.RuneCountInString(m.Surface))
				}
				out = append(out, &jaCand{
					morphs: morphs,
					end:    pos + 1 + l,
					w:      w,
					rule:   jaRuleAux,
				})
			}
			// The longest auxiliary reading wins; shorter ones are noise.
			break
		}
	}
	return out
}

// jaAuxReadings returns the constituent readings of an auxiliary surface,
// splitting a trailing particle so てもらえますか keeps its question reading.
func (a *jaAnalyzer) jaAuxReadings(s string, depth int) [][]*forest.Morph {
	if s == "" || depth > 2 {
		return nil
	}
	var out [][]*forest.Morph
	for _, m := range a.jaAuxMorphs(s) {
		out = append(out, []*forest.Morph{m})
	}
	if depth >= 2 {
		return out
	}
	tails := append(append([]string{}, jaAuxTails...), "か", "ね", "よ", "な", "でしょう", "だろう")
	for _, tail := range tails {
		if len(s) <= len(tail) || !strings.HasSuffix(s, tail) {
			continue
		}
		inner := a.jaAuxReadings(s[:len(s)-len(tail)], depth+1)
		if len(inner) == 0 {
			continue
		}
		tm := jaTailMorph(s, tail)
		for _, ms := range inner {
			out = append(out, append(append([]*forest.Morph{}, ms...), tm))
		}
	}
	return out
}

// jaTailMorph materialises a sentence-final particle that follows an auxiliary.
func jaTailMorph(surface, tail string) *forest.Morph {
	m := &forest.Morph{
		Surface: tail,
		Base:    tail,
		Lem:     tail,
		POS:     jaTailPOS(tail),
		Script:  forest.Script(lang.RuneScript(jaFirstRune(tail))),
		Start:   len(surface) - len(tail),
		End:     len(surface),
		Feats:   jaTailFeats(tail),
		Dict:    true,
	}
	m.Feats[jaFtConjRule] = jaRuleDict
	return m
}

func jaTailPOS(tail string) forest.POS {
	switch tail {
	case "か", "ね", "よ", "な":
		return forest.POSParticle
	default:
		return forest.POSAux
	}
}

func jaTailFeats(tail string) map[string]string {
	out := map[string]string{jaFtConjRule: jaRuleDict}
	switch tail {
	case "か":
		out[jaFtSFP] = "ka"
		out["mood"] = "question"
	case "ね":
		out[jaFtSFP] = "ne"
		out["agreement"] = "1"
	case "よ":
		out[jaFtSFP] = "yo"
		out["informative"] = "1"
	case "な":
		out[jaFtSFP] = "na"
		out[jaFtNegative] = "1"
	case "でしょう", "だろう":
		out[jaFtSFP] = "kamoshideshou"
		out["mood"] = "presumptive"
		out[jaFtCopula] = "da"
		out["polite"] = jaPoliteTei
	default:
		out["polite"] = jaPoliteMasu
		out["mood"] = "question"
	}
	return out
}

// jaAuxMorphs returns the auxiliary readings of a surface: the auxiliary role
// first (progressive / completion / request) and then the plain verb reading,
// which is a real alternative for ています ("eat and stay" vs "be eating").
func (a *jaAnalyzer) jaAuxMorphs(s string) []*forest.Morph {
	var out []*forest.Morph
	seen := map[string]bool{}
	for _, infl := range a.jaInflections(s) {
		role, ok := a.jaAuxRoleOf(infl.base)
		if !ok {
			continue
		}
		m := a.jaAuxMorphFor(s, infl, role)
		key := m.Surface + "|" + m.Base + "|" + m.Feats[jaFtTense] + "|" + m.Feats["polite"]
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	if !seen[s+"|direct"] {
		for _, e := range jaLex[s] {
			if e.pos != forest.POSAux || !jaIsAuxRoleEntry(e.feats) {
				continue
			}
			m := &forest.Morph{
				Surface: s, Base: e.base, Lem: e.lem, POS: forest.POSAux,
				Script: forest.Script(lang.RuneScript(jaFirstRune(s))),
				Feats:  jaCopyFeats(e.feats), Dict: true,
			}
			m.Feats["role"] = jaAuxRoleName(e.feats)
			m.Feats[jaFtConjRule] = jaRuleAux
			m.Notes = append(m.Notes, "auxiliary "+m.Feats["role"])
			out = append(out, m)
			break
		}
	}
	return out
}

// jaAuxMorphFor builds an inflected auxiliary morpheme with its role preserved.
func (a *jaAnalyzer) jaAuxMorphFor(surface string, infl jaInfl, role jaLexEntry) *forest.Morph {
	feats := jaCopyFeats(role.feats)
	for k, v := range infl.feats {
		feats[k] = v
	}
	feats["role"] = jaAuxRoleName(role.feats)
	feats[jaFtConjRule] = jaRuleAux
	return &forest.Morph{
		Surface: surface,
		Base:    role.base,
		Lem:     role.lem,
		POS:     forest.POSAux,
		Script:  forest.Script(lang.RuneScript(jaFirstRune(surface))),
		Feats:   feats,
		Dict:    true,
		Notes: []string{fmt.Sprintf("%s → %s (%s, role %s)",
			surface, infl.base, infl.note, feats["role"])},
	}
}

// jaAuxRoleOf finds the auxiliary role record of a dictionary form.
func (a *jaAnalyzer) jaAuxRoleOf(base string) (jaLexEntry, bool) {
	for _, e := range jaLex[base] {
		if e.pos == forest.POSAux && jaIsAuxRoleEntry(e.feats) {
			return e, true
		}
	}
	return jaLexEntry{}, false
}

func jaIsAuxRoleEntry(feats map[string]string) bool {
	for _, k := range jaAuxRoleKeys {
		if _, ok := feats[k]; ok {
			return true
		}
	}
	return false
}

func jaAuxRoleName(feats map[string]string) string {
	for _, k := range []string{"aspect", "completion", "politeness", "benefactive"} {
		if v := feats[k]; v != "" {
			return v
		}
	}
	if _, ok := feats["direction"]; ok {
		return "from_speaker"
	}
	return "auxiliary"
}

func (a *jaAnalyzer) jaConjMorph(i, j int, conn string) *forest.Morph {
	m := &forest.Morph{
		Surface: conn,
		Base:    conn,
		Lem:     conn,
		POS:     forest.POSAffix,
		Script:  forest.Script(lang.RuneScript(jaFirstRune(conn))),
		Start:   a.start(i),
		End:     a.end(j),
		Feats:   map[string]string{jaFtConj: "te", jaFtConjRule: jaRuleAux},
		Dict:    true,
	}
	m.Notes = append(m.Notes, "te-form connective; the auxiliary that follows carries its own role")
	return m
}

// --- numerals ----------------------------------------------------------

var jaNumeralDigits = map[rune]int{
	'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
}

var jaNumeralUnits = map[rune]int{
	'十': 10, '百': 100, '千': 1000, '万': 10000, '億': 100000000, '兆': 1000000000000,
}

func jaIsNumeralRune(r rune) bool {
	if _, ok := jaNumeralDigits[r]; ok {
		return true
	}
	if _, ok := jaNumeralUnits[r]; ok {
		return true
	}
	return r == '零'
}

// jaNumeralCands composes a run of numeral kanji (二十三 → 23) so that the counter
// which follows attaches to a quantity rather than to a digit string.
func (a *jaAnalyzer) jaNumeralCands(pos int, out []*jaCand) []*jaCand {
	l := 0
	for l < jaMaxCandRunes && pos+l < a.n && jaIsNumeralRune(a.runes[pos+l]) {
		l++
	}
	if l < 2 {
		return out
	}
	surface := a.sub(pos, pos+l)
	v, ok := jaNumeralValue(surface)
	if !ok {
		return out
	}
	m := &forest.Morph{
		Surface: surface,
		Base:    surface,
		Lem:     surface,
		POS:     forest.POSNum,
		Script:  forest.ScriptKanji,
		Start:   a.start(pos),
		End:     a.end(pos + l),
		Feats: map[string]string{
			jaFtNumeral:  strconv.Itoa(v),
			jaFtConjRule: jaRuleNumeral,
		},
		Dict:  true,
		Notes: []string{"numeral composed from kanji digits: " + surface},
	}
	return append(out, &jaCand{
		morphs: []*forest.Morph{m},
		end:    pos + l,
		w:      jaWNumeral + jaWRune*float64(l),
		rule:   jaRuleNumeral,
	})
}

func jaNumeralValue(s string) (int, bool) {
	runes := []rune(s)
	total, section, digit := 0, 0, 0
	seen := false
	for _, r := range runes {
		if r == '零' {
			digit = 0
			seen = true
			continue
		}
		if d, ok := jaNumeralDigits[r]; ok {
			digit = d
			seen = true
			continue
		}
		u, ok := jaNumeralUnits[r]
		if !ok {
			return 0, false
		}
		seen = true
		switch {
		case u >= 10000:
			section += digit
			if section == 0 {
				section = 1
			}
			total += section * u
			section, digit = 0, 0
		default:
			if digit == 0 {
				digit = 1
			}
			section += digit * u
			digit = 0
		}
	}
	if !seen {
		return 0, false
	}
	return total + section + digit, true
}

// --- explicit unknown fallback -----------------------------------------

// jaFallbackCands guarantees the lattice is total: whitespace first, then the
// maximal run of characters no rule can explain. Nothing is ever dropped, and
// an unresolvable kanji compound ending in a personal-name character is
// offered as a *guessed* proper noun with Unknown set — a candidate, not a fact.
func (a *jaAnalyzer) jaFallbackCands(pos int, out []*jaCand) []*jaCand {
	if jaIsASCIISpace(a.runes[pos]) {
		l := 0
		for l < jaMaxUnknownRunes && pos+l < a.n && jaIsASCIISpace(a.runes[pos+l]) {
			l++
		}
		surface := a.sub(pos, pos+l)
		m := &forest.Morph{
			Surface: surface,
			Base:    surface,
			Lem:     surface,
			POS:     forest.POSUnknown,
			Script:  forest.ScriptOther,
			Start:   a.start(pos),
			End:     a.end(pos + l),
			Feats:   map[string]string{jaFtConjRule: jaRuleSpace},
			Unknown: true,
			Notes:   []string{"whitespace kept as its own morpheme so the span stays covered"},
		}
		return append(out, &jaCand{
			morphs: []*forest.Morph{m},
			end:    pos + l,
			w:      jaWSpace - 0.01*float64(l),
			rule:   jaRuleSpace,
		})
	}
	l := 0
	for l < jaMaxUnknownRunes && pos+l < a.n &&
		!jaIsASCIISpace(a.runes[pos+l]) && !a.jaKnownStart(pos+l) {
		l++
	}
	// A katakana loanword is one word whatever the dictionary happens to
	// contain inside it: ビール must not become ビ + ール because some entry
	// starts at ル. The run therefore covers the whole katakana span. It is
	// still an UNKNOWN morpheme, just an honest single one (plan.md §25 step 1),
	// and a dictionary hit on the same span still wins because it scores far
	// higher.
	if l > 0 && jaIsKatakanaRune(a.runes[pos]) {
		for l < jaMaxUnknownRunes && pos+l < a.n && jaIsKatakanaRune(a.runes[pos+l]) {
			l++
		}
	}
	if l == 0 {
		l = 1
	}
	surface := a.sub(pos, pos+l)
	if l >= 2 {
		if feats, ok := jaNameSuffixGuess(surface); ok {
			m := &forest.Morph{
				Surface: surface,
				Base:    surface,
				Lem:     surface,
				POS:     forest.POSProper,
				Script:  forest.Script(lang.RuneScript(jaFirstRune(surface))),
				Start:   a.start(pos),
				End:     a.end(pos + l),
				Feats:   jaCopyFeats(feats),
				Unknown: true,
				Notes: []string{
					"guessed proper noun from a personal-name suffix; no dictionary entry, so it stays a candidate rather than a fact (plan.md §22)",
				},
			}
			m.Feats[jaFtConjRule] = jaRuleName
			out = append(out, &jaCand{
				morphs: []*forest.Morph{m},
				end:    pos + l,
				w:      jaWName + jaWRune*float64(l),
				rule:   jaRuleName,
			})
		}
	}
	m := &forest.Morph{
		Surface: surface,
		Base:    surface,
		Lem:     surface,
		POS:     forest.POSUnknown,
		Script:  forest.Script(lang.RuneScript(jaFirstRune(surface))),
		Start:   a.start(pos),
		End:     a.end(pos + l),
		Feats:   map[string]string{jaFtConjRule: jaRuleUnknown},
		Unknown: true,
		Notes: []string{
			"no dictionary entry and no applicable inflection rule; kept as UNKNOWN instead of being guessed (plan.md §25)",
		},
	}
	return append(out, &jaCand{
		morphs: []*forest.Morph{m},
		end:    pos + l,
		w:      jaWUnknown - 0.50*float64(l),
		rule:   jaRuleUnknown,
	})
}

// jaKnownStart reports whether some rule fires at rune index i, which is what
// bounds the unknown run.
func (a *jaAnalyzer) jaKnownStart(i int) bool {
	limit := a.n
	if lim := i + jaMaxRunes; lim < limit {
		limit = lim
	}
	for l := 1; l <= limit-i; l++ {
		if len(jaLex[a.sub(i, i+l)]) > 0 {
			return true
		}
	}
	// The inflection check has to look at the whole inflected span, not just two
	// runes: します is three runes long, and stopping the unknown run short of it
	// is what made 学习します come back as one UNKNOWN morpheme.
	for l := 2; l <= jaMaxInflectRunes && i+l <= a.n; l++ {
		if len(a.jaInflections(a.sub(i, i+l))) > 0 {
			return true
		}
	}
	if i+2 <= a.n && jaIsNumeralRune(a.runes[i]) && jaIsNumeralRune(a.runes[i+1]) {
		return true
	}
	return false
}

func jaNameSuffixGuess(surface string) (map[string]string, bool) {
	runes := []rune(surface)
	feats, ok := jaNameSuffixes[string(runes[len(runes)-1])]
	if !ok {
		return nil, false
	}
	for _, r := range runes {
		if !jaIsKanjiRune(r) {
			return nil, false
		}
	}
	out := map[string]string{"proper": "1"}
	for k, v := range jaParseFeats(feats) {
		out[k] = v
	}
	return out, true
}

// --- helpers --------------------------------------------------------------

func jaFirstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func jaIsASCIISpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

func jaIsKanjiRune(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

// jaIsKatakanaRune covers the katakana block plus ー, the katakana-hiragana
// prolonged-sound mark. ー belongs to the katakana word it prolongs and is
// never a morpheme on its own: it is the vowel of ビール, not punctuation.
func jaIsKatakanaRune(r rune) bool {
	return (r >= 0x30A1 && r <= 0x30FA) || (r >= 0x30FC && r <= 0x30FF)
}

func jaDedupeStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
