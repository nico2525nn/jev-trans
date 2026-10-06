package lex

// English source analysis: tokenizer, closed-class tagger, suffix-rule
// fallback and lemmatizer, producing a MorphForest whose paths are complete
// analyses of the whole input (plan.md §23, §25, §46, §47).
//
// Two rules shape the whole file. plan.md §4 forbids inventing information, so
// anything the tables and the rules do not determine is emitted as a low
// weight reading with Morph.Unknown set instead of a confident guess. plan.md
// §17 forbids collapsing ambiguity, so every token that has more than one
// reading contributes one segmentation per reading and the forest keeps all of
// them: "it's" stays "it + is" *and* "it + 's", "he's" stays open, and "-ed"
// stays open between finite past and participle.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nico2525nn/jev-trans/internal/forest"
	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/trace"
)

// --- analysis units ------------------------------------------------------

// enTokKind distinguishes the three things a token can be.
type enTokKind int

const (
	enKindWord enTokKind = iota
	enKindNum
	enKindPunct
	// enKindSym is a symbol rather than punctuation: emoji, dingbats and
	// anything else that is not a letter, a digit or punctuation. It must not
	// be treated as punctuation, because a comma ends a constituent and a
	// picture does not.
	enKindSym
)

// enTok is one raw token with its byte span. Offsets are kept so every
// morpheme can point back at the exact source substring, which is what lets
// Morph.Text and the re-parser recover the original string.
type enTok struct {
	Text  string
	Start int
	End   int
	Kind  enTokKind
}

// enReading is one way of analysing one surface form: a lemma, a coarse POS
// and the morphological features the projection stage consumes.
type enReading struct {
	POS    forest.POS
	Lem    string
	Feats  map[string]string
	Weight float64
	Rule   string
	Dict   bool
}

// enPiece is one morpheme of one reading of a token.
type enPiece struct {
	Start int
	End   int
	R     enReading
}

// enSeq is one complete reading of one token. A plain word has exactly one
// piece; a contraction has as many pieces as the clitic analysis requires.
type enSeq struct {
	Pieces []enPiece
	Weight float64
	Rule   string
	Note   string
}

// AnalyzeEN runs the English analyzer over src and returns the morphological
// lattice. It never panics and never drops characters: every non-whitespace
// rune ends up inside exactly one morpheme of every path.
func AnalyzeEN(src string) *forest.MorphForest {
	span := enOpenSpan("English tokenizer / tagger / lemmatizer")
	if span != nil {
		defer span.Close()
	}

	f := &forest.MorphForest{Lang: lang.EN, Source: src, DictionarySize: enDictSize}
	toks := enTokenize(src)
	if len(toks) == 0 {
		f.Notes = append(f.Notes, "no tokens: input is empty or whitespace only")
		if span != nil {
			span.Status(trace.StatusSkip)
			span.Detail("nothing to analyze")
		}
		return f
	}

	allCaps := enAllCaps(src)
	seqs := make([][]enSeq, len(toks))
	for i, t := range toks {
		seqs[i] = enBaseSeqs(src, t, enAtSentenceStart(toks, i), allCaps)
	}
	// The second pass reweights readings using the neighbours' primary tags,
	// which is what promotes the infinitival "to" of "want to go" over the
	// adpositional "to" of "gave it to Hanako" without either being hard-coded.
	for i := range toks {
		seqs[i] = enContextualSeqs(seqs[i], toks, i)
	}

	f.Paths = enPaths(src, toks, seqs)
	f.UnknownRate, f.Notes = enStats(f, allCaps)
	if span != nil {
		span.Data(f)
		span.Count("morphemes", len(f.Best().Morphs))
		span.Count("lattice-paths", len(f.Paths))
		if f.UnknownRate > 0 {
			span.Status(trace.StatusWarn)
		}
	}
	return f
}

func enOpenSpan(title string) *trace.Span {
	r := trace.From(context.Background())
	if r == nil {
		return nil
	}
	return r.Open(trace.StageMorph, title)
}

// --- tokenization --------------------------------------------------------

func enIsWordRune(r rune) bool {
	return r == '_' || r == '\'' || r == '’' || unicode.IsLetter(r) || unicode.IsMark(r)
}

func enIsDigitByte(b byte) bool { return b >= '0' && b <= '9' }

// enPunctChars is the punctuation English actually uses, plus the full width
// forms, so that a CJK punctuation mark does not become a word.
const enPunctChars = ".,;:!?''\"()[]{}<>-–—/\\|@#$%^&*+=~`·。「」『』！？…．）＝"

func enIsPunctRune(r rune) bool { return strings.ContainsRune(enPunctChars, r) }

// enTokenize splits on whitespace and punctuation, keeping punctuation as its
// own morpheme. An apostrophe stays inside its word: "don't" is one token
// here and is split into clitics further down, where both readings can be
// kept open.
func enTokenize(src string) []enTok {
	var out []enTok
	i := 0
	for i < len(src) {
		r, sz := utf8.DecodeRuneInString(src[i:])
		switch {
		case unicode.IsSpace(r):
			i += sz
		case r < 128 && enIsDigitByte(byte(r)):
			j := i
		digits:
			for j < len(src) {
				rr, ss := utf8.DecodeRuneInString(src[j:])
				switch {
				case rr >= '0' && rr <= '9':
					j += ss
				case (rr == ',' || rr == '.') && j+1 < len(src) && enIsDigitByte(src[j+1]):
					j += ss
				default:
					break digits
				}
			}
			out = append(out, enTok{Text: src[i:j], Start: i, End: j, Kind: enKindNum})
			i = j
		case enIsWordRune(r):
			j := i
			for j < len(src) {
				rr, ss := utf8.DecodeRuneInString(src[j:])
				if !enIsWordRune(rr) {
					break
				}
				j += ss
			}
			out = append(out, enTok{Text: src[i:j], Start: i, End: j, Kind: enKindWord})
			i = j
		default:
			kind := enKindSym
			if enIsPunctRune(r) {
				kind = enKindPunct
			}
			out = append(out, enTok{Text: src[i : i+sz], Start: i, End: i + sz, Kind: kind})
			i += sz
		}
	}
	return out
}

// enAllCaps reports a shouted input. Tagging "TARO GAVE" as two proper nouns
// would invent a fact about the text, so capitalization is ignored outside the
// first token of a sentence in that case.
func enAllCaps(src string) bool {
	up, low := false, false
	for _, r := range src {
		if unicode.IsUpper(r) {
			up = true
		}
		if unicode.IsLower(r) {
			low = true
		}
	}
	return up && !low
}

func enIsCapitalized(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

func enIsSentenceFinalPunct(s string) bool {
	switch s {
	case ".", "!", "?", "。", "！", "？", "…":
		return true
	}
	return false
}

func enAtSentenceStart(toks []enTok, i int) bool {
	if i <= 0 {
		return true
	}
	return enIsSentenceFinalPunct(toks[i-1].Text)
}

// --- readings ------------------------------------------------------------

// enBaseSeqs builds every reading of one token, before neighbour information
// is available.
func enBaseSeqs(src string, t enTok, atStart, allCaps bool) []enSeq {
	switch t.Kind {
	case enKindPunct:
		r := enReading{POS: forest.POSPunct, Lem: t.Text, Feats: enFs("class", "punctuation"),
			Weight: 1, Rule: "punctuation", Dict: true}
		return []enSeq{{Pieces: []enPiece{{Start: t.Start, End: t.End, R: r}}, Weight: 1, Rule: "punctuation"}}
	case enKindNum:
		lem := strings.ReplaceAll(t.Text, ",", "")
		r := enReading{POS: forest.POSNum, Lem: lem, Feats: enFs("class", "numeral", "number", lem),
			Weight: 0.95, Rule: "numeral", Dict: true}
		return []enSeq{{Pieces: []enPiece{{Start: t.Start, End: t.End, R: r}}, Weight: 0.95, Rule: "numeral"}}
	case enKindSym:
		// A symbol carries no word identity. It is kept as a morpheme so the
		// input is fully covered, and it is never allowed to act as a
		// constituent boundary.
		r := enReading{POS: forest.POSUnknown, Lem: enLower(t.Text),
			Feats: enFs("class", "symbol"), Weight: 0.5, Rule: "symbol", Dict: true}
		return []enSeq{{Pieces: []enPiece{{Start: t.Start, End: t.End, R: r}}, Weight: 0.5, Rule: "symbol"}}
	}

	low := enLower(t.Text)
	if forms := enContractionForms(low, t.Text, t.Start); len(forms) > 0 {
		return enFormsToSeqs(forms, src, t.Start, t.End)
	}
	if strings.ContainsAny(t.Text, "'’") {
		if seqs := enApostropheFallback(low, t); len(seqs) > 0 {
			return seqs
		}
	}

	readings := enWordReadings(low, t.Text, atStart, allCaps)
	out := make([]enSeq, 0, len(readings))
	for _, r := range readings {
		out = append(out, enSeq{
			Pieces: []enPiece{{Start: t.Start, End: t.End, R: r}},
			Weight: r.Weight, Rule: r.Rule,
		})
	}
	if len(out) == 0 {
		r := enReading{POS: forest.POSUnknown, Lem: low, Weight: 0.1, Rule: "unresolved"}
		out = append(out, enSeq{Pieces: []enPiece{{Start: t.Start, End: t.End, R: r}}, Weight: 0.1, Rule: "unresolved"})
	}
	return enSortSeqs(out)
}

// enWordReadings lists the readings of a bare word, most reliable first:
// closed class, irregular paradigm, productive suffix rule, capitalization,
// then an explicit fallback. Every stage records which rule fired so the UI
// can show why a tag was chosen.
func enWordReadings(low, surface string, atStart, allCaps bool) []enReading {
	var rs []enReading
	dict := false

	if e, ok := enLex[low]; ok {
		rs = append(rs, enReading{POS: e.POS, Lem: e.Lem, Feats: enClone(e.Feats),
			Weight: 0.97, Rule: "closed-class", Dict: true})
		dict = true
	}
	if alts, ok := enLexAlt[low]; ok {
		dict = true
		for _, a := range alts {
			rs = append(rs, enReading{POS: a.POS, Lem: a.Lem, Feats: enClone(a.Feats),
				Weight: 0.38, Rule: "closed-class:alt", Dict: true})
		}
	}
	if forms, ok := enVerbForm[low]; ok {
		dict = true
		for _, vf := range forms {
			rs = append(rs, enReadingFromVerb(vf))
		}
	}
	if enAlsoNoun[low] && !enIsVerbReading(rs) {
		rs = append(rs, enReading{POS: forest.POSNoun, Lem: low, Feats: enFs("class", "noun"),
			Weight: 0.4, Rule: "noun:alt"})
	}
	if !dict {
		rs = append(rs, enSuffixReadings(low)...)
	}

	capitalized := enIsCapitalized(surface)
	switch {
	case capitalized && !allCaps:
		w := 0.8
		if atStart {
			w = 0.62
		}
		rs = append([]enReading{{POS: forest.POSProper, Lem: low,
			Feats: enFs("class", "proper", "capitalized", "true"), Weight: w, Rule: "capitalization"}}, rs...)
	case capitalized && allCaps && atStart:
		rs = append([]enReading{{POS: forest.POSProper, Lem: low,
			Feats: enFs("class", "proper", "capitalized", "true"), Weight: 0.5, Rule: "capitalization"}}, rs...)
	}

	if len(rs) == 0 {
		rs = enFallbackReadings(low)
	}
	return enSortReadings(rs)
}

func enIsVerbReading(rs []enReading) bool {
	for _, r := range rs {
		if r.POS == forest.POSVerb || r.POS == forest.POSAux {
			return true
		}
	}
	return false
}

// enTenseAmbiguousIrregulars are verbs whose base, past and participle are all
// spelled the same, so the written form carries no tense at all.
//
// It is derived from the paradigm table rather than written out beside it. The
// two disagreed: spend was listed here while the paradigm table spells its past
// "spent", so a reader of either table in isolation would conclude something
// false — and the direction the error ran was the dangerous one, because an
// ambiguous token is one the analyser refuses to settle.
//
// Deriving the property that actually defines the set removes the possibility
// of drift: a verb is here exactly when its past is spelled like its base.
var enTenseAmbiguousIrregulars = func() map[string]bool {
	m := make(map[string]bool, 16)
	for base, v := range enIrregularVerbs {
		// "was/were" names several forms and matches no single base; that verb
		// is not spelled identically in the first place.
		past, alt, multiple := strings.Cut(v.Past, "/")
		if multiple || alt != "" || past == "" {
			continue
		}
		if past == base {
			m[base] = true
		}
	}
	return m
}()

func enTenseAmbiguousIrregular(l string) bool { return enTenseAmbiguousIrregulars[l] }

func enReadingFromVerb(vf enVerbReading) enReading {
	var f map[string]string
	switch vf.Kind {
	case "base":
		f = enFs("class", "verb", "tense", "present", "part", "base", "lemma_kind", "irregular")
	case "past":
		f = enFs("class", "verb", "tense", "past", "part", "past", "lemma_kind", "irregular")
	case "participle":
		f = enFs("class", "verb", "tense", "past", "part", "participle", "lemma_kind", "irregular")
	default:
		f = enFs("class", "verb", "tense", "present", "part", "finite", "person", "3",
			"number", "sing", "lemma_kind", "irregular")
	}
	// English has verbs whose present and past are spelled identically: read,
	// set, put, cut, let, shut, spread. The spelling alone settles nothing, so
	// the token carries BOTH readings and lets the parse narrow them.
	//
	// The list is derived from enIrregularVerbs rather than restated, which is
	// what keeps spend out of it.
	//
	// "Taro read a book" is past, because a third-person singular subject takes
	// -s in the present and would have to be "reads". "I read a book" is not
	// settled by agreement: I read is present and I read is past. A flag
	// saying "ambiguous" cannot express that difference, so the readings
	// themselves are recorded and the parser decides from the subject.
	if enTenseAmbiguousIrregular(vf.Lemma) && f["tense"] != "past" {
		delete(f, "tense")
		f["tense_readings"] = "present,past"
	}
	return enReading{POS: forest.POSVerb, Lem: vf.Lemma, Feats: f, Weight: 0.93, Rule: "irregular", Dict: true}
}

// enSuffixReadings applies the productive suffix rules, ordered from most to
// least reliable. The order is the tagger's confidence model: -ing and -ly are
// nearly exceptionless, -ed is famously ambiguous between finite past and
// participle, so both readings are emitted rather than one being picked.
func enSuffixReadings(w string) []enReading {
	var out []enReading
	if len(w) >= 4 {
		switch {
		case strings.HasSuffix(w, "ing"):
			for _, l := range enLemmaIng(w) {
				out = append(out,
					enReading{POS: forest.POSVerb, Lem: l,
						Feats:  enFs("class", "verb", "tense", "present", "part", "gerund", "rule", "suffix:-ing"),
						Weight: 0.88, Rule: "suffix:-ing"})
			}
			// English has a large -ing adjective class (interesting, boring).
			// Leaving it out would make every copula sentence unparseable.
			for _, l := range enLemmaIng(w) {
				out = append(out, enReading{POS: forest.POSAdj, Lem: l,
					Feats:  enFs("class", "adjective", "part", "gerund", "rule", "suffix:-ing"),
					Weight: 0.55, Rule: "suffix:-ing"})
			}
		case strings.HasSuffix(w, "ly"):
			for _, l := range enLemmaAdv(w) {
				out = append(out,
					enReading{POS: forest.POSAdv, Lem: l,
						Feats:  enFs("class", "adverb", "rule", "suffix:-ly"),
						Weight: 0.9, Rule: "suffix:-ly"})
			}
		case enHasAnySuffix(w, "tion", "sion", "ment", "ness", "ity", "ance", "ence", "ship", "hood", "dom", "ism"):
			out = append(out, enReading{POS: forest.POSNoun, Lem: w,
				Feats:  enFs("class", "noun", "rule", "suffix:-nom"),
				Weight: 0.88, Rule: "suffix:-nom"})
		case enHasAnySuffix(w, "ous", "ful", "ive", "able", "ible", "al", "ic", "ish", "less", "ary", "ent", "ant"):
			out = append(out, enReading{POS: forest.POSAdj, Lem: w,
				Feats:  enFs("class", "adjective", "rule", "suffix:-adj"),
				Weight: 0.8, Rule: "suffix:-adj"})
		case strings.HasSuffix(w, "ed"):
			for _, l := range enLemmaED(w) {
				out = append(out,
					enReading{POS: forest.POSVerb, Lem: l,
						Feats:  enFs("class", "verb", "tense", "past", "part", "past", "person", "3", "rule", "suffix:-ed"),
						Weight: 0.84, Rule: "suffix:-ed"},
					enReading{POS: forest.POSVerb, Lem: l,
						Feats:  enFs("class", "verb", "tense", "past", "part", "participle", "rule", "suffix:-ed"),
						Weight: 0.8, Rule: "suffix:-ed"},
					enReading{POS: forest.POSAdj, Lem: l,
						Feats:  enFs("class", "adjective", "part", "participle", "rule", "suffix:-ed"),
						Weight: 0.35, Rule: "suffix:-ed"})
			}
		case strings.HasSuffix(w, "est"), strings.HasSuffix(w, "er"):
			degree := "comparative"
			if strings.HasSuffix(w, "est") {
				degree = "superlative"
			}
			for _, l := range enLemmaComparative(w) {
				out = append(out,
					enReading{POS: forest.POSAdj, Lem: l,
						Feats:  enFs("class", "adjective", "degree", degree, "rule", "suffix:-er/-est"),
						Weight: 0.72, Rule: "suffix:-er/-est"},
					enReading{POS: forest.POSAdv, Lem: l,
						Feats:  enFs("class", "adverb", "degree", degree, "rule", "suffix:-er/-est"),
						Weight: 0.3, Rule: "suffix:-er/-est"})
			}
		}
	}
	// -s is the least reliable suffix in English: plural noun and third person
	// singular are both spelled it, so both are offered.
	if len(w) >= 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") &&
		!strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is") {
		for _, l := range enLemmaPlural(w) {
			out = append(out, enReading{POS: forest.POSNoun, Lem: l,
				Feats:  enFs("class", "noun", "number", "plur", "rule", "suffix:-s"),
				Weight: 0.7, Rule: "suffix:-s"})
		}
		out = append(out, enReading{POS: forest.POSVerb, Lem: w,
			Feats:  enFs("class", "verb", "tense", "present", "part", "finite", "person", "3", "number", "sing", "rule", "suffix:-s"),
			Weight: 0.45, Rule: "suffix:-s"})
	}
	if len(out) == 0 {
		out = enFallbackReadings(w)
	}
	return out
}

// enFallbackReadings is the honest end of the ladder (plan.md §25, steps 5
// and 6): plausible shapes, low confidence, flagged unknown by the caller.
func enFallbackReadings(w string) []enReading {
	return []enReading{
		{POS: forest.POSNoun, Lem: w, Feats: enFs("class", "noun"), Weight: 0.36, Rule: "fallback"},
		{POS: forest.POSVerb, Lem: w, Feats: enFs("class", "verb", "tense", "present"), Weight: 0.24, Rule: "fallback"},
		{POS: forest.POSAdj, Lem: w, Feats: enFs("class", "adjective"), Weight: 0.16, Rule: "fallback"},
	}
}

// --- lemmatizer ----------------------------------------------------------

// enLemmaIng de-inflects -ng forms, undoing both e-deletion (making → make)
// and consonant doubling (running → run). Both restores are offered because
// English spelling does not record which one applied.
func enLemmaIng(w string) []string {
	base := w[:len(w)-3]
	if enDoubledFinal(base) {
		base = base[:len(base)-1]
	}
	out := []string{base}
	if len(base) >= 2 && !strings.ContainsRune("aeiou", rune(base[len(base)-1])) {
		out = append(out, base+"e")
	}
	return out
}

// enLemmaED de-inflects -d forms. -ied is unambiguous (tried → try); a silent
// final e cannot be recovered from the spelling, so "liked" yields "lik" and
// "like", and "walked" yields "walk" and "walke".
func enLemmaED(w string) []string {
	if strings.HasSuffix(w, "ied") && len(w) > 4 {
		return []string{w[:len(w)-3] + "y", w[:len(w)-3]}
	}
	base := w[:len(w)-2]
	if enDoubledFinal(base) {
		base = base[:len(base)-1]
	}
	out := []string{base}
	if len(w) >= 4 && w[len(w)-2] == 'e' {
		out = append(out, w[:len(w)-1])
	}
	return out
}

func enLemmaPlural(w string) []string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return []string{w[:len(w)-3] + "y", w[:len(w)-3] + "ie"}
	case strings.HasSuffix(w, "ves") && len(w) > 4:
		return []string{w[:len(w)-3] + "f", w[:len(w)-3] + "fe"}
	case enHasAnySuffix(w, "ses", "xes", "zes", "ches", "shes"):
		return []string{w[:len(w)-2], w[:len(w)-1]}
	}
	return []string{w[:len(w)-1]}
}

func enLemmaComparative(w string) []string {
	switch {
	case strings.HasSuffix(w, "iest") && len(w) > 4:
		return []string{w[:len(w)-4] + "y", w[:len(w)-4]}
	case strings.HasSuffix(w, "ier") && len(w) > 3:
		return []string{w[:len(w)-3] + "y", w[:len(w)-3]}
	case strings.HasSuffix(w, "est"), strings.HasSuffix(w, "er"):
		b := w[:len(w)-2]
		var out []string
		if enDoubledFinal(b) {
			out = append(out, b[:len(b)-1])
		}
		out = append(out, b)
		out = append(out, b+"e")
		return out
	}
	return []string{w}
}

func enLemmaAdv(w string) []string {
	if strings.HasSuffix(w, "ily") && len(w) > 4 {
		return []string{w[:len(w)-3] + "y", w[:len(w)-3]}
	}
	b := w[:len(w)-2]
	if enDoubledFinal(b) {
		b = b[:len(b)-1]
	}
	out := []string{b}
	if len(b) > 0 && !strings.ContainsRune("aeiou", rune(b[len(b)-1])) {
		out = append(out, b+"e")
	}
	return out
}

// --- contextual reweighting ----------------------------------------------

// enContextualSeqs reweights the readings of token i using its neighbours'
// primary tags. It only reorders readings that already exist; it never invents
// a tag the dictionaries and the rules did not produce.
func enContextualSeqs(seqs []enSeq, toks []enTok, i int) []enSeq {
	if len(seqs) == 0 {
		return seqs
	}
	prev := enPrimaryReading(toks, i-1)
	next := enPrimaryReading(toks, i+1)
	word := enLower(toks[i].Text)

	out := make([]enSeq, len(seqs))
	copy(out, seqs)
	for k := range out {
		if len(out[k].Pieces) != 1 {
			// One contextual correction still applies inside a contraction: an
			// "'d" followed by have is the perfect modal "would have", not the
			// ungrammatical "had have".
			p := out[k].Pieces[len(out[k].Pieces)-1]
			if p.R.Lem == "would" && next != nil && next.Lem == "have" {
				p.R.Weight = 0.99
				out[k].Weight = p.R.Weight
			}
			continue
		}
		r := &out[k].Pieces[0].R
		switch {
		case word == "to" && enIsVerbish(prev) && enIsVerbish(next):
			if r.POS == forest.POSPrep || r.Lem == "to" {
				r.POS = forest.POSParticle
				r.Feats = enMergeFeats(r.Feats, enFs("class", "particle", "infinitive", "true"))
				r.Weight = 0.95
				r.Rule = "infinitive-to"
			}
		case enIsCopula(prev) && strings.HasSuffix(word, "ed"):
			// "he was tired" is overwhelmingly an adjective predicate while
			// "was written" is overwhelmingly a passive, so the copula decides,
			// not the -ed. The competing reading stays in the lattice.
			if r.POS == forest.POSAdj {
				r.Weight = 0.88
			}
			if r.Feats["part"] == "past" {
				r.Weight = 0.25
			}
		case enIsCopula(prev) && strings.HasSuffix(word, "ing"):
			// "was interesting" is a predicate adjective, "is running in the
			// park" is a progressive; the following preposition tells them
			// apart far more reliably than the -ing itself.
			if next != nil && (next.POS == forest.POSPrep || next.POS == forest.POSAdv) {
				if r.POS == forest.POSVerb && r.Feats["part"] == "gerund" {
					r.Weight = 0.9
				}
			} else {
				if r.POS == forest.POSAdj {
					r.Weight = 0.88
				}
				if r.POS == forest.POSVerb && r.Feats["part"] == "gerund" {
					r.Weight = 0.6
				}
			}
		case (enIsDet(prev) || enIsPossessiveDet(prev)) && strings.HasSuffix(word, "s") && r.POS == forest.POSNoun:
			if r.Weight < 0.88 {
				r.Weight = 0.88
			}
		case enIsVerbish(prev) && strings.HasSuffix(word, "ing") && r.POS == forest.POSVerb:
			r.Weight = 0.9
		case r.POS == forest.POSVerb && strings.HasSuffix(word, "s") && enAfterDeterminerNP(toks, i):
			// "the cat sat" style agreement: a determiner-headed noun phrase
			// in front of an -s word means the word is the verb.
			r.Weight = 0.9
		case r.POS == forest.POSConj && r.Feats["subordinator"] == "true" && enVerbishAt(toks, i+2):
			// "because he was" is a clause, "because of the rain" is not:
			// the finite verb after it settles it.
			r.Weight = 0.99
			r.Rule = "subordinator"
		case enIsPronoun(prev) && strings.HasSuffix(word, "s") && r.POS == forest.POSVerb:
			// Subject-verb agreement: "he walks" is a verb even though the
			// -s suffix alone would also allow a plural noun.
			r.Weight = 0.9
		case enIsPluralSubject(prev) && r.POS == forest.POSVerb && !strings.HasSuffix(word, "s"):
			r.Weight = 0.85
		case enIsComplementizer(word) && r.POS == forest.POSConj &&
			enIsVerbish(prev) && enVerbishAt(toks, i+2):
			// "say that he was" is a complement, "the book that I read" is a
			// relative clause; the verb after the noun phrase tells them apart.
			r.Weight = 0.99
			r.Rule = "complementizer"
		case enIsComplementizer(word) && r.POS == forest.POSPronoun &&
			enIsVerbish(prev) && enVerbishAt(toks, i+2):
			r.Weight = 0.99
			r.Rule = "relative-pronoun"
		}
		out[k].Weight = r.Weight
	}
	return enSortSeqs(out)
}

func enPrimaryReading(toks []enTok, i int) *enReading {
	if i < 0 || i >= len(toks) || toks[i].Kind == enKindPunct {
		return nil
	}
	rs := enWordReadings(enLower(toks[i].Text), toks[i].Text, enAtSentenceStart(toks, i), false)
	if len(rs) == 0 {
		return nil
	}
	return &rs[0]
}

func enIsVerbish(r *enReading) bool {
	return r != nil && (r.POS == forest.POSVerb || r.POS == forest.POSAux)
}

func enIsCopula(r *enReading) bool {
	return r != nil && r.Feats["copula"] == "true"
}

func enIsDet(r *enReading) bool { return r != nil && r.POS == forest.POSDet }

func enIsPossessiveDet(r *enReading) bool {
	return r != nil && r.POS == forest.POSDet && r.Feats["possessive"] == "true"
}

func enIsPronoun(r *enReading) bool { return r != nil && r.POS == forest.POSPronoun }

// enIsPluralSubject is the agreement evidence for "they walk" versus
// "the walk": a plural or first/second person subject forces the verb.
func enIsPluralSubject(r *enReading) bool {
	if !enIsPronoun(r) {
		return false
	}
	switch r.Feats["number"] {
	case "plur":
		return true
	}
	switch r.Feats["person"] {
	case "1", "2":
		return true
	}
	return false
}

// enVerbishAt is the verb test one token further right.
func enVerbishAt(toks []enTok, k int) bool {
	if k < 0 || k >= len(toks) || toks[k].Kind == enKindPunct {
		return false
	}
	if toks[k].Kind != enKindWord {
		return false
	}
	return enIsVerbish(enPrimaryReading(toks, k))
}

// enIsComplementizer reports whether a word can introduce a clausal
// complement or a relative clause.
func enAfterDeterminerNP(toks []enTok, i int) bool {
	for k := i - 1; k >= 0 && k >= i-5; k-- {
		switch toks[k].Kind {
		case enKindPunct:
			return false
		case enKindNum:
			return true
		}
		if enIsDet(enPrimaryReading(toks, k)) {
			return true
		}
		r := enPrimaryReading(toks, k)
		if r == nil || (r.POS != forest.POSNoun && r.POS != forest.POSProper &&
			r.POS != forest.POSAdj && r.POS != forest.POSPronoun) {
			return false
		}
	}
	return false
}

func enIsComplementizer(w string) bool {
	switch w {
	case "that", "which", "who", "whom":
		return true
	}
	return false
}

// enKnownWord reports whether a form is in the dictionary, used to choose
// between two spellings of the same contracted base.
func enKnownWord(w string) bool {
	if _, ok := enLex[w]; ok {
		return true
	}
	if _, ok := enVerbForm[w]; ok {
		return true
	}
	return false
}

// --- contractions --------------------------------------------------------

// enContractionForms splits a contracted token into its parts. "it's" yields
// both a copula form and a possessive form, "he'd" yields both had and would,
// and "won't" is recognized as will not: those are the readings a downstream
// verifier must see, and picking one here would be inventing information.
func enContractionForms(low, text string, start int) []enConForm {
	if forms, ok := enSpecialContractions[low]; ok {
		return forms
	}
	apos := strings.IndexAny(text, "'’")
	if apos <= 0 {
		return nil
	}
	clitic := enNormalizeApos(text[apos:])
	if _, ok := enClitic[clitic]; !ok {
		return nil
	}
	baseText := text[:apos]
	lowBase := enLower(baseText)

	clitics := enClitic[clitic]
	if clitic == "'s" {
		if isw, ok := enCopularBases[lowBase]; ok {
			clitics = append(clitics, enConForm{
				Parts: []enConPart{{Surface: isw, POS: forest.POSAux, Lem: isw,
					Feats: enFs("class", "auxiliary", "auxiliary", "true", "tense", "present",
						"part", "finite", "copula", "true")}},
				Weight: 0.9,
				Note:   "'s read as the copula " + isw,
			})
		}
	}

	// "won't", "shan't" and "ain't" have no dictionary base at all: the
	// spelling of the auxiliary is simply not there.
	switch lowBase {
	case "won":
		return enFixedBaseForms(clitics, enConPart{Surface: baseText, POS: forest.POSAux, Lem: "will",
			Feats: enFs("class", "modal", "modal", "true", "auxiliary", "true", "tense", "future", "part", "finite")},
			"won't is will not")
	case "shan":
		return enFixedBaseForms(clitics, enConPart{Surface: baseText, POS: forest.POSAux, Lem: "shall",
			Feats: enFs("class", "modal", "modal", "true", "auxiliary", "true", "tense", "future", "part", "finite")},
			"shan't is shall not")
	case "ai":
		// ain't is is not / are not / have not; person and number are not
		// recoverable, so all three readings survive.
		forms := []enConPart{
			{Surface: baseText, POS: forest.POSAux, Lem: "be",
				Feats: enFs("class", "auxiliary", "auxiliary", "true", "tense", "present", "part", "finite", "copula", "true", "person", "3", "number", "sing")},
			{Surface: baseText, POS: forest.POSAux, Lem: "be",
				Feats: enFs("class", "auxiliary", "auxiliary", "true", "tense", "present", "part", "finite", "copula", "true", "person", "2", "number", "plur")},
			{Surface: baseText, POS: forest.POSAux, Lem: "have",
				Feats: enFs("class", "auxiliary", "auxiliary", "true", "tense", "present", "part", "finite")},
		}
		var out []enConForm
		for i, base := range forms {
			w := 0.8 - float64(i)*0.12
			for _, cf := range clitics {
				parts := append(append([]enConPart{}, base), cf.Parts...)
				out = append(out, enConForm{Parts: parts, Weight: w * cf.Weight,
					Note: "ain't has no single reading: " + cf.Note})
			}
		}
		return out
	}

	type baseCand struct {
		part enConPart
		w    float64
	}
	var cands []baseCand
	// "didn.t" is "did" + not while "can.t" is "can" + not: the spelling does
	// not record which n belongs to the base, so both readings are offered
	// and the dictionary decides which one leads.
	if clitic == "'t" && apos > 0 && text[apos-1] == 'n' {
		longBase := text[:apos]
		shortBase := text[:apos-1]
		longKnown := enKnownWord(enLower(longBase))
		shortKnown := enKnownWord(enLower(shortBase))
		if shortKnown && !longKnown {
			for _, b := range enBaseParts(enLower(shortBase), shortBase) {
				cands = append(cands, baseCand{b, 1})
			}
			for _, b := range enBaseParts(enLower(longBase), longBase) {
				cands = append(cands, baseCand{b, 0.2})
			}
		} else {
			for _, b := range enBaseParts(enLower(longBase), longBase) {
				w := 1.0
				if !longKnown {
					w = 0.2
				}
				cands = append(cands, baseCand{b, w})
			}
			if shortKnown && shortBase != longBase {
				for _, b := range enBaseParts(enLower(shortBase), shortBase) {
					cands = append(cands, baseCand{b, 0.2})
				}
			}
		}
	} else {
		for _, b := range enBaseParts(lowBase, baseText) {
			cands = append(cands, baseCand{b, 1})
		}
	}
	if len(cands) == 0 {
		return nil
	}
	var out []enConForm
	for _, cand := range cands {
		for _, cf := range clitics {
			parts := make([]enConPart, 0, len(cf.Parts)+1)
			parts = append(parts, cand.part)
			parts = append(parts, cf.Parts...)
			out = append(out, enConForm{Parts: parts, Weight: cf.Weight * cand.w, Note: cf.Note})
		}
	}
	return out
}

func enFixedBaseForms(clitics []enConForm, base enConPart, note string) []enConForm {
	var out []enConForm
	for _, cf := range clitics {
		parts := append([]enConPart{base}, cf.Parts...)
		out = append(out, enConForm{Parts: parts, Weight: cf.Weight, Note: note})
	}
	return out
}

func enBaseParts(lowBase, surface string) []enConPart {
	var out []enConPart
	if e, ok := enLex[lowBase]; ok {
		out = append(out, enConPart{Surface: surface, POS: e.POS, Lem: e.Lem, Feats: enClone(e.Feats)})
		return out
	}
	for i, r := range enWordReadings(lowBase, surface, false, false) {
		if i >= 2 {
			break
		}
		out = append(out, enConPart{Surface: surface, POS: r.POS, Lem: r.Lem, Feats: enClone(r.Feats)})
	}
	return out
}

// enApostropheFallback handles an internal apostrophe that is neither a
// clitic nor a possessive ("rock'n'roll"): each fragment is tagged on its
// own and nothing is invented about the join.
func enApostropheFallback(low string, t enTok) []enSeq {
	fragments := strings.FieldsFunc(low, func(r rune) bool { return r == '\'' || r == '’' })
	if len(fragments) < 2 {
		return nil
	}
	var pieces []enPiece
	pos := t.Start
	for _, f := range fragments {
		idx := strings.Index(t.Text[pos-t.Start:], f)
		if idx < 0 {
			return nil
		}
		s := pos + idx
		e := s + len(f)
		rs := enWordReadings(f, f, false, false)
		if len(rs) == 0 {
			return nil
		}
		pieces = append(pieces, enPiece{Start: s, End: e, R: rs[0]})
		pos = e
	}
	w := 0.6
	for i := range pieces {
		w *= pieces[i].R.Weight
	}
	return []enSeq{{Pieces: pieces, Weight: w, Rule: "apostrophe-split",
		Note: "internal apostrophe, segmented without deciding its function"}}
}

// enNormalizeApos folds the typographic apostrophe onto the ASCII one.
func enNormalizeApos(s string) string { return strings.ReplaceAll(s, "’", "'") }

func enLower(s string) string { return strings.ToLower(enNormalizeApos(s)) }

func enFormsToSeqs(forms []enConForm, text string, start, end int) []enSeq {
	var out []enSeq
	for _, f := range forms {
		pieces := enPlaceParts(f.Parts, text, start, end)
		if len(pieces) == 0 {
			continue
		}
		w := f.Weight
		for i := range pieces {
			w *= pieces[i].R.Weight
		}
		out = append(out, enSeq{Pieces: pieces, Weight: w, Rule: "contraction", Note: f.Note})
	}
	return enSortSeqs(out)
}

// enPlaceParts gives every clitic piece an exact source span. When a part's
// spelling is not literally present ("us" in "let's") the piece covers the
// remaining characters of the token, so no character of the input is ever
// dropped; the morpheme's lemma then differs from its surface, which is the
// honest representation of a clitic.
func enPlaceParts(parts []enConPart, text string, start, end int) []enPiece {
	pieces := make([]enPiece, 0, len(parts))
	pos := start
	for _, p := range parts {
		s, e := pos, pos+1
		if p.Surface != "" {
			if idx := strings.Index(text[pos:end], p.Surface); idx >= 0 {
				s, e = pos+idx, pos+idx+len(p.Surface)
			}
		}
		if e > end {
			e = end
		}
		if s < pos {
			s = pos
		}
		lem := p.Lem
		if lem == "" {
			lem = enLower(text[s:e])
		}
		r := enReading{POS: p.POS, Lem: lem,
			Feats:  enMergeFeats(enClone(p.Feats), enFs("contraction", text)),
			Weight: 0.95, Rule: "contraction", Dict: true}
		pieces = append(pieces, enPiece{Start: s, End: e, R: r})
		pos = e
	}
	if n := len(pieces); n > 0 && pieces[n-1].End < end {
		pieces[n-1].End = end
	}
	return pieces
}

// --- lattice assembly ----------------------------------------------------

// enPaths builds whole-input analyses. The number of paths is bounded so a
// pathological sentence cannot explode the lattice; the bound never drops the
// primary reading.
func enPaths(src string, toks []enTok, seqs [][]enSeq) []forest.MorphAlt {
	lens := make([]int, len(seqs))
	for i, s := range seqs {
		if len(s) == 0 {
			// Guarantee a reading so the path builder can never index an
			// empty lattice.
			t := toks[i]
			r := enReading{POS: forest.POSUnknown, Lem: enLower(t.Text), Weight: 0.05, Rule: "unresolved"}
			seqs[i] = []enSeq{{Pieces: []enPiece{{Start: t.Start, End: t.End, R: r}}, Weight: 0.05, Rule: "unresolved"}}
			s = seqs[i]
		}
		lens[i] = len(s)
	}
	combos := enCombine(lens, 24)
	alts := enAlternativeLists(seqs)

	var out []forest.MorphAlt
	seen := map[string]bool{}
	for _, picks := range combos {
		weight := 1.0
		morphs := make([]*forest.Morph, 0, len(toks)*2)
		var sig strings.Builder
		alternative := false
		for i := range toks {
			seq := seqs[i][picks[i]]
			if picks[i] != 0 || seq.Rule == "contraction" || seq.Rule == "apostrophe-split" {
				alternative = true
			}
			for j, pc := range seq.Pieces {
				surface := toks[i].Text
				if pc.Start >= 0 && pc.End <= len(src) && pc.End > pc.Start {
					surface = src[pc.Start:pc.End]
				}
				m := &forest.Morph{
					Surface: surface,
					Base:    surface,
					Lem:     pc.R.Lem,
					POS:     pc.R.POS,
					Script:  forest.Script(lang.RuneScript(enFirstRune(surface))),
					Start:   pc.Start,
					End:     pc.End,
					Feats:   enClone(pc.R.Feats),
					Dict:    pc.R.Dict,
					Unknown: !pc.R.Dict,
				}
				if l := alts[i]; len(l) > 0 && j == 0 {
					m.Alternatives = append([]string(nil), l...)
				}
				if !pc.R.Dict {
					m.Notes = append(m.Notes, "reconstructed by "+pc.R.Rule)
				}
				if seq.Note != "" && j == 0 {
					m.Notes = append(m.Notes, seq.Note)
				}
				m.ID = fmt.Sprintf("m%d", len(morphs)+1)
				sig.WriteString(m.Lem + ":" + string(m.POS) + ":" + m.Feats["part"] + ";")
				weight *= pc.R.Weight
				morphs = append(morphs, m)
			}
		}
		key := sig.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		if weight < 1e-9 {
			weight = 1e-9
		}
		rule := "en/primary"
		if alternative {
			rule = "en/alternative"
		}
		out = append(out, forest.MorphAlt{Morphs: morphs, Weight: weight, Rule: rule})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	return out
}

// enAlternativeLists renders the non-primary readings of each token as
// "lemma:POS:rule" strings attached to Morph.Alternatives, so a reader of the
// lattice sees the whole analysis space, not only the winner.
func enAlternativeLists(seqs [][]enSeq) [][]string {
	out := make([][]string, len(seqs))
	for i, ss := range seqs {
		var list []string
		for j, s := range ss {
			if j == 0 {
				continue
			}
			for _, pc := range s.Pieces {
				// lemma:POS:part:rule. The part is included so a downstream stage can
				// ask whether a form can be a participle without re-running the
				// analyzer.
				list = append(list, pc.R.Lem+":"+string(pc.R.POS)+":"+pc.R.Feats["part"]+":"+pc.R.Rule)
			}
		}
		sort.Strings(list)
		out[i] = enDedupeStrings(list, 6)
	}
	return out
}

// enCombine enumerates reading combinations, capped at max. When the full
// product is small it is enumerated exhaustively; otherwise a bounded DFS
// keeps the combinations that differ from the primary reading in as few
// positions as possible.
func enCombine(lens []int, max int) [][]int {
	total := 1
	for _, l := range lens {
		if l <= 0 {
			continue
		}
		total *= l
		if total > 1<<20 {
			total = 1 << 20
			break
		}
	}
	if total <= max {
		out := make([][]int, 0, total)
		picks := make([]int, len(lens))
		for {
			c := make([]int, len(picks))
			copy(c, picks)
			out = append(out, c)
			k := len(picks) - 1
			for ; k >= 0; k-- {
				picks[k]++
				if picks[k] < lens[k] {
					break
				}
				picks[k] = 0
			}
			if k < 0 {
				break
			}
		}
		return out
	}
	var out [][]int
	var rec func(k int, acc []int)
	rec = func(k int, acc []int) {
		if len(out) >= max {
			return
		}
		if k == len(lens) {
			c := make([]int, len(acc))
			copy(c, acc)
			out = append(out, c)
			return
		}
		limit := lens[k]
		if limit > 3 {
			limit = 3
		}
		for j := range limit {
			if len(out) >= max {
				return
			}
			rec(k+1, append(acc, j))
		}
	}
	rec(0, nil)
	return out
}

// enStats computes UnknownRate over the winning path and returns the notes the
// UI shows: reconstructed words, segmented contractions, ambiguity, casing.
func enStats(f *forest.MorphForest, allCaps bool) (float64, []string) {
	best := f.Best()
	total, unknown := 0, 0
	var notes []string
	for _, m := range best.Morphs {
		n := m.End - m.Start
		if n < 0 {
			n = 0
		}
		total += n
		if m.Unknown {
			unknown += n
			notes = append(notes, "unknown word: "+m.Surface+", tagged "+string(m.POS)+" by rule, not by dictionary")
		} else if c := m.Feat("contraction"); c != "" {
			notes = append(notes, "contraction segmented: "+c+" gives "+m.Surface+" ("+m.Lem+")")
		}
	}
	if allCaps {
		notes = append(notes, "input is entirely upper case; capitalization is not used as a proper-noun cue")
	}
	if len(f.Paths) > 1 {
		notes = append(notes, fmt.Sprintf("%d lattice paths: the input is ambiguous and no choice was made here", len(f.Paths)))
	}
	notes = enDedupeStrings(notes, 24)
	if total == 0 {
		return 0, notes
	}
	return float64(unknown) / float64(total), notes
}

// --- small helpers -------------------------------------------------------

// enMergeFeats overlays extra features onto a copy of base.
func enMergeFeats(base, extra map[string]string) map[string]string {
	if len(extra) == 0 && len(base) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func enFirstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// enSortReadings orders readings by confidence and removes duplicates, so the
// lattice never contains the same reading twice.
func enSortReadings(rs []enReading) []enReading {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Weight > rs[j].Weight })
	out := make([]enReading, 0, len(rs))
	seen := map[string]bool{}
	for _, r := range rs {
		key := string(r.POS) + "|" + r.Lem + "|" + enFeatKey(r.Feats)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
		if len(out) >= 5 {
			break
		}
	}
	return out
}

func enSortSeqs(ss []enSeq) []enSeq {
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].Weight > ss[j].Weight })
	if len(ss) > 5 {
		ss = ss[:5]
	}
	return ss
}

// enFeatKey renders a feature map deterministically for deduplication.
func enFeatKey(f map[string]string) string {
	if len(f) == 0 {
		return ""
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(f[k])
		b.WriteByte(';')
	}
	return b.String()
}

func enDedupeStrings(in []string, max int) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}
