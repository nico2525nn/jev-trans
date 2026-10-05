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

	"github.com/nico/jev-trans/internal/lang"
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
	// The tables are symmetric by construction: an English lexeme and its
	// Japanese counterpart are one entry seen from the other side, not a second
	// translation. Inverting them here is what lets EN -> JP project "book" as
	// 本 without a hand-maintained second table that could drift. The first
	// mapping wins on a collision, so the direction is deterministic.
	l.enNoun = make(map[string]string, len(jaNouns))
	for _, ja := range sortedKeys(jaNouns) {
		if _, taken := l.enNoun[jaNouns[ja]]; !taken {
			l.enNoun[jaNouns[ja]] = ja
		}
	}
	l.enName = make(map[string]string, len(jaNames))
	for _, ja := range sortedKeys(jaNames) {
		if _, taken := l.enName[jaNames[ja]]; !taken {
			l.enName[jaNames[ja]] = ja
		}
	}
	return l
}

// sortedKeys gives the deterministic iteration order the inversion needs.
func sortedKeys(m map[string]string) []string {
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
	// An identical-script surface needs no work.
	if tgt == lang.JA || (src == lang.JA && tgt == lang.EN) {
		// handled below
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
	}
}
