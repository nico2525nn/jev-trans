// Package lexgen produces the target-language surface form of a referent that
// the JLIR only knows in the source language.
//
// This is the piece plan.md §51 describes when it says a named entity is an
// identity mapping rather than a translation: 東京 and Tokyo are two surface
// forms of one entity, and producing the second one is not translation, it is
// lookup. plan.md §10 makes the same point for common nouns: translation goes
// word → meaning → word, so a Japanese noun whose sense is known still needs
// an English lexeme, and that lexeme has to come from somewhere real.
//
// The rule this package obeys is the same as everywhere else in JEV-Trans: it
// never guesses. A surface it cannot ground is not returned, and the caller
// then reports a lexical gap as a referential loss instead of emitting the
// source script inside a target sentence.
package lexgen

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/lex"
)

// Lexicalizer resolves target-language surfaces.
type Lexicalizer struct {
	nouns  map[string]string               // ja noun -> en noun
	names  map[string]string               // ja proper name -> en transliteration
	terms  map[lang.Lang]map[string]string // source lang -> surface -> surface
	roman  map[rune]string                 // katakana -> romaji fragment
	enNoun map[string]string               // en noun -> ja noun (derived)
	enName map[string]string               // en name -> ja name (derived)

	jPronoun map[string]string // ja pronoun -> en pronoun
	ePronoun map[string]string // en pronoun (lower case) -> ja pronoun

	// collisions lists the English lexemes that more than one Japanese word
	// maps to (friend → 友 / 友達). The EN → JP direction has to pick one of
	// them, and the fact that this is a choice belongs in the coverage numbers
	// rather than in a comment claiming the tables are "symmetric by
	// construction". They were not.
	collisions []string
}

// Default returns the built-in lexicalizer.
func Default() *Lexicalizer {
	l := &Lexicalizer{
		nouns:    jaNouns,
		names:    jaNames,
		terms:    identityTerms,
		roman:    katakanaRomaji,
		jPronoun: jaPronouns,
		ePronoun: enPronouns,
	}
	// EN -> JA is the reverse of the same table, inverted here rather than
	// hand-maintained a second time. The inversion is NOT lossless: six English
	// lexemes are shared by two Japanese words (friend is both 友 and 友達, bag
	// both 鞄 and 袋, and so on), and a plain first-wins inversion made one of
	// each pair unreachable from EN -> JA without saying so.
	//
	// So the inversion is explicit about its lossy step: every collision is
	// recorded, and enNounOverrides names which Japanese word each shared
	// English lexeme projects to. The rule is "the more idiomatic word wins",
	// and the alternative stays reachable from JA -> EN, so nothing is lost in
	// the direction it is actually written in.
	l.enNoun = make(map[string]string, len(jaNouns))
	byEnglish := make(map[string][]string, len(jaNouns))
	for _, ja := range sortedKeys(jaNouns) {
		byEnglish[jaNouns[ja]] = append(byEnglish[jaNouns[ja]], ja)
	}
	for _, en := range sortedKeys(byEnglish) {
		ja := byEnglish[en][0]
		if len(byEnglish[en]) > 1 {
			l.collisions = append(l.collisions, en)
			if override, ok := enNounOverrides[en]; ok {
				ja = override
			}
		}
		l.enNoun[en] = ja
	}
	l.enName = make(map[string]string, len(jaNames))
	for _, ja := range sortedKeys(jaNames) {
		if _, taken := l.enName[jaNames[ja]]; !taken {
			l.enName[jaNames[ja]] = ja
		}
	}
	return l
}

// Collisions returns the English lexemes that more than one Japanese word
// translates to, sorted. Each is a place where the EN -> JA direction had to
// choose, which the coverage report makes visible rather than silent.
func (l *Lexicalizer) Collisions() []string {
	if l == nil {
		return nil
	}
	return append([]string(nil), l.collisions...)
}

// sortedKeys gives the deterministic iteration order the inversion needs.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Form returns the target-language surface for a source surface.
//
// typ and proper come from the entity: a proper name goes through the
// transliteration table, a common noun through the lexeme table. Returns false
// when nothing is grounded, and the caller must then record a lexical gap.
func (l *Lexicalizer) Form(surface string, src, tgt lang.Lang, typ string, proper bool) (string, bool) {
	surface = strings.TrimSpace(surface)
	if surface == "" {
		return "", false
	}
	if src == tgt {
		return surface, true
	}
	if f, ok := l.terms[src]; ok {
		if v, ok := f[surface]; ok {
			return v, true
		}
	}
	if src == lang.JA && tgt == lang.EN {
		if proper {
			if v, ok := l.names[surface]; ok {
				return v, true
			}
			if v, ok := l.romanize(surface); ok {
				return v, true
			}
			return "", false
		}
		// A pronoun is checked before the noun table because 彼 is both a
		// pronoun and a determiner, and its pronoun reading is the one the
		// referential layer recorded.
		if v, ok := l.jPronoun[surface]; ok {
			return v, true
		}
		if v, ok := l.nouns[surface]; ok {
			return v, true
		}
		return "", false
	}
	if src == lang.EN && tgt == lang.JA {
		// The reverse of the same table. An English lexeme with no Japanese
		// counterpart here is a genuine gap: the surface is left unrealized and
		// the caller files a lexical-gap loss rather than transliterating.
		key := strings.ToLower(surface)
		if proper {
			if v, ok := l.enName[key]; ok {
				return v, true
			}
			if v, ok := l.enName[surface]; ok {
				return v, true
			}
			return "", false
		}
		// Pronouns before nouns: "they" and "everyone" are pronouns and
		// quantifiers, and they must not be routed through the lexeme table.
		if v, ok := l.ePronoun[key]; ok {
			return v, true
		}
		if v, ok := l.enNoun[key]; ok {
			return v, true
		}
		return "", false
	}
	return "", false
}

// romanize turns a katakana or kanji personal name into a romaji form. It is
// deliberately conservative: kanji are only transliterated when a reading is
// known, because a guessed reading of a Japanese name is a fabrication.
func (l *Lexicalizer) romanize(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	allKana := true
	for _, r := range s {
		if r < 0x30A0 || r > 0x30FF {
			allKana = false
			break
		}
	}
	if !allKana {
		return "", false
	}
	var b strings.Builder
	for _, r := range s {
		frag, ok := l.roman[r]
		if !ok {
			// An unpronounceable katakana is not a name we can ground.
			return "", false
		}
		b.WriteString(frag)
	}
	out := b.String()
	if out == "" {
		return "", false
	}
	return titleWord(out), true
}

func titleWord(s string) string {
	rs := []rune(s)
	for i, r := range rs {
		if i == 0 || rs[i-1] == ' ' {
			if r >= 'a' && r <= 'z' {
				rs[i] = r - 32
			}
		}
	}
	return string(rs)
}

// Has reports whether the lexicalizer knows this surface at all, which lets the
// UI distinguish "no translation known" from "not attempted".
func (l *Lexicalizer) Has(surface string) bool {
	if _, ok := l.nouns[surface]; ok {
		return true
	}
	if _, ok := l.names[surface]; ok {
		return true
	}
	return false
}

// Validate reports every Japanese key that the analyzer cannot produce.
//
// The package promises that Form returns a grounded surface or nothing at all.
// A key the morph layer cannot reach breaks that promise in the worst possible
// way: it is still returned, with ok=true, so the caller never records a
// lexical gap. The table carried "ceipt", the Hangul "이웃" and the Simplified
// Chinese "伙伴" this way. Keys are checked against the analyzer's own
// dictionary, so the check cannot pass on a word that only the table knows.
func Validate(l *Lexicalizer) error {
	if l == nil {
		return nil
	}
	var bad []string
	for _, ja := range sortedKeys(l.nouns) {
		if !analyzerCovers(ja) {
			bad = append(bad, ja)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return &UnreachableError{Keys: bad}
}

// analyzerCovers reports whether the analyzer segments key with no unresolved
// morpheme. A key it can only cover as UNKNOWN is exactly the case that breaks
// Form's promise: the surface exists but nothing grounds it.
func analyzerCovers(key string) bool {
	best := lex.AnalyzeJA(key).Best()
	if len(best.Morphs) == 0 {
		return false
	}
	// Every rune must be accounted for by a morpheme the analyzer resolved.
	covered := 0
	for _, m := range best.Morphs {
		if m.Unknown {
			return false
		}
		covered += utf8.RuneCountInString(m.Surface)
	}
	return covered == utf8.RuneCountInString(key)
}

// UnreachableError lists Japanese keys the analyzer cannot produce.
type UnreachableError struct{ Keys []string }

func (e *UnreachableError) Error() string {
	return "lexgen: these Japanese keys are unreachable from the analyzer: " +
		strings.Join(e.Keys, ", ")
}

// Stats reports coverage for the UI.
func Stats(l *Lexicalizer) map[string]int {
	if l == nil {
		return map[string]int{}
	}
	return map[string]int{
		"japaneseNouns":     len(l.nouns),
		"japaneseNames":     len(l.names),
		"identityTerms":     len(l.terms),
		"katakanaFragments": len(l.roman),
		// The derived EN -> JA tables are listed too: they are half the
		// resolution surface, and omitting them is what let six dropped
		// noun mappings go unnoticed.
		"englishNouns": len(l.enNoun),
		"englishNames": len(l.enName),
		// nounCollisions counts the English lexemes two Japanese words share.
		// It is not an error, but it is the honest measure of how much the
		// reverse direction had to choose.
		"nounCollisions": len(l.collisions),
	}
}

// IsProperName reports whether the surface is registered as a proper name in the
// given language.
//
// The English determiner chooser needs this and cannot get it from the entity's
// Proper flag: that flag comes from the analyser's part of speech, and both the
// builtin analyser and Sudachi report a personal name as an ordinary noun
// (太郎 is 名詞,普通名詞 to Sudachi), so "A Taro" is what a determiner attached
// on the wrong assumption of common-noun status produces.
func (l *Lexicalizer) IsProperName(surface string, l2 lang.Lang) bool {
	switch l2 {
	case lang.JA:
		if _, ok := l.names[surface]; ok {
			return true
		}
		if _, ok := l.enName[surface]; ok {
			return true
		}
	default:
		if _, ok := l.enName[surface]; ok {
			return true
		}
		if _, ok := l.names[surface]; ok {
			return true
		}
	}
	return false
}
