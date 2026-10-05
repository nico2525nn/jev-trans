// Package lang holds the language identifiers and small language-level
// helpers shared by every stage of the translation pipeline.
//
// A Lang is the smallest unit of type the system reasons about: it decides
// which morphological analyzer runs, which construction library is consulted,
// which realizer produces surface strings and which politeness profile applies.
package lang

import (
	"strings"
	"unicode"
)

// Lang identifies a supported language of the pipeline.
type Lang string

const (
	// JA is Japanese. Japanese is the source language in most runs because the
	// system's hardest problems (zero pronouns, no articles, topic marking,
	// honorifics, sentence-final particles) originate there.
	JA Lang = "ja"
	// EN is English. English forces overt subjects, articles, number agreement
	// and gender-marked pronouns, so it is the demanding projection target.
	EN Lang = "en"
)

// Valid reports whether l is a language the pipeline can process.
func (l Lang) Valid() bool { return l == JA || l == EN }

// Other returns the opposite direction of the language pair.
func (l Lang) Other() Lang {
	if l == JA {
		return EN
	}
	return JA
}

func (l Lang) String() string { return string(l) }

// Name returns an English display name for the language.
func (l Lang) Name() string {
	switch l {
	case JA:
		return "Japanese"
	case EN:
		return "English"
	}
	return string(l)
}

// Script classifies the writing system of a single rune. The JP morphological
// analyzer uses it to route katakana and kanji to the right unknown-word
// strategies, and the trace renderer uses it to pick fonts.
type Script string

const (
	ScriptHiragana Script = "hiragana"
	ScriptKatakana Script = "katakana"
	ScriptKanji    Script = "kanji"
	ScriptLatin    Script = "latin"
	ScriptDigit    Script = "digit"
	ScriptOther    Script = "other"
)

// RuneScript classifies r.
func RuneScript(r rune) Script {
	switch {
	case r >= 'あ' && r <= 'ん':
		return ScriptHiragana
	case r >= 'ァ' && r <= 'ン':
		return ScriptKatakana
	case r >= '一' && r <= '鿿', r >= '㐀' && r <= '䶿':
		return ScriptKanji
	case r < 128 && unicode.IsLetter(r):
		return ScriptLatin
	case r >= '0' && r <= '9':
		return ScriptDigit
	}
	return ScriptOther
}

// Detect returns the dominant script of s, or ScriptOther for empty input.
func Detect(s string) Script {
	counts := map[Script]int{}
	for _, r := range s {
		if sc := RuneScript(r); sc != ScriptOther {
			counts[sc]++
		}
	}
	best, bestN := ScriptOther, 0
	for sc, n := range counts {
		if n > bestN || (n == bestN && sc < best) {
			best, bestN = sc, n
		}
	}
	return best
}

// DetectLang guesses the language of s from character repertoire. It is a
// convenience for the CLI and the WebUI; the HTTP API takes an explicit
// source_lang because guessing is exactly the kind of decision the pipeline
// refuses to make silently.
func DetectLang(s string) Lang {
	kanji, kana, latin := 0, 0, 0
	for _, r := range s {
		switch RuneScript(r) {
		case ScriptKanji:
			kanji++
		case ScriptHiragana, ScriptKatakana:
			kana++
		case ScriptLatin:
			latin++
		}
	}
	if kana > 0 || kanji*3 > latin {
		return JA
	}
	return EN
}

// Parse converts a user supplied language tag, accepting the common aliases.
func Parse(s string) (Lang, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ja", "jp", "jpn", "japanese", "日本語":
		return JA, true
	case "en", "eng", "english", "英語":
		return EN, true
	}
	return "", false
}
