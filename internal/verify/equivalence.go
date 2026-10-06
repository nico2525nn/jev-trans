package verify

// Equivalence classes, plan.md §42.
//
// Requiring literal JLIR equality would throw away the translations a human
// would consider correct: Japanese drops subjects, English is forced to add
// articles, てしまう and "already" encode completion by different means, and
// an honorific has no English counterpart. The verifier therefore compares
// semantic content using an explicit, enumerable set of classes rather than
// string identity, and charges only the loss that genuinely remains.
//
// Every class below is a deliberate decision, not a heuristic: each one states
// which pair of surface forms it licenses and which loss dimension survives the
// match. A pair that matches no class is compared literally.

import (
	"strings"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
)

// ClassID names one equivalence class. The values are stable because the WebUI
// renders them in the loss report.
type ClassID string

const (
	// ClassExplicitToZero licenses an overt source reference to be realized as
	// a zero subject or zero object. Japanese does this routinely; English
	// does it in ellipsis. Referent identity must survive the switch.
	ClassExplicitToZero ClassID = "EC1_EXPLICIT_PRONOUN_ZERO_REALIZATION"

	// ClassZeroToExplicit licenses a zero source realization to become an
	// explicit target NP, but only when the discourse state makes the referent
	// unique. Without a unique referent this is not an equivalence: it is the
	// system guessing, and plan.md §61 says the honest answer is
	// UNDERDETERMINED.
	ClassZeroToExplicit ClassID = "EC2_ZERO_REALIZATION_EXPLICIT_PRONOUN"

	// ClassCompletionAspect licenses English "already" and a Japanese
	// aspectual completion marker such as てしまう to cover the same
	// completion content.
	ClassCompletionAspect ClassID = "EC3_ALREADY_ASPECTUAL_COMPLETION"

	// ClassHonorificPlain licenses an honorific the target language cannot
	// express to map onto its plain counterpart. Propositional content is
	// identical; only the pragmatic layer is lost (plan.md §5).
	ClassHonorificPlain ClassID = "EC4_HONORIFIC_PLAIN_COUNTERPART"

	// ClassGenderNeutral licenses English "they" where the source did not
	// determine gender. This is the mirror image of plan.md §22's "The female
	// teacher came.": declining to guess is correct, not lossy.
	ClassGenderNeutral ClassID = "EC5_THEY_AVOIDS_GENDER_INVENTION"
)

// Pair is one feature value from the source next to the value the target
// actually realized, plus the context the classes need. Building a Pair is the
// verifier's job; the classes only judge it.
type Pair struct {
	// Dimension is the diff dimension: "referential", "coreference",
	// "temporal", "pragmatic" or "gender".
	Dimension string
	// Key narrows the pair inside the dimension, e.g. "honorific",
	// "completion", "pronoun".
	Key string

	Source string
	Target string

	SourceLang lang.Lang
	TargetLang lang.Lang

	// SourceOvert / TargetOvert say the side has a surface realization.
	SourceOvert bool
	TargetOvert bool
	// SourceZero / TargetZero say the side is an unrealized placeholder.
	SourceZero bool
	TargetZero bool

	// SourceGender is the normalized source gender; UNKNOWN when the source
	// does not determine one.
	SourceGender string
	// ReferentUnique reports whether the discourse state pins the target side
	// to exactly one referent.
	ReferentUnique bool
	// ReferentAligned reports whether the verifier has established that the
	// target binding denotes *the same referent* the source bound, by canonical
	// identity keys or by the committed referent decision of a zero anaphor.
	//
	// The realization classes EC1 and EC2 cannot be decided without it. A zero
	// form and an explicit pronoun are the same *reference* only if the referent
	// survived; otherwise the target quietly talks about someone else, and
	// plan.md §41 calls that a referential diff, not a style choice. Without
	// this field the classes licensed a realization change on surface shape
	// alone — and since EC1 fires whenever the target is Japanese, that waived
	// referent checking for every overt source referent.
	ReferentAligned bool
	// Role is the semantic role being realized.
	Role string
}

// Class is one entry of the registry. Applies reports whether a pair is an
// instance of the class; Cost names the loss dimension that still survives the
// match, or "" when the match is free.
type Class struct {
	ID      ClassID
	Plan    string
	Title   string
	Cost    string
	Applies func(p Pair) bool
}

// Classes is the ordered registry of plan.md §42. The order is fixed so that a
// pair reported as equivalent always names the same class.
var Classes = []Class{
	{
		ID: ClassExplicitToZero, Plan: "42", Title: "explicit reference vs zero realization",
		Cost: DimStylistic,
		Applies: func(p Pair) bool {
			if !isCorefDimension(p.Dimension) {
				return false
			}
			// The referent must have survived, and the target side must actually
			// be pinned to one. A zero form whose antecedent is still an open
			// distribution is not an equivalent realization, it is an unfinished
			// decision, and §61 says the honest status for that is
			// UNDERDETERMINED rather than GOOD.
			return p.SourceOvert && p.TargetZero && p.ReferentAligned && p.ReferentUnique &&
				allowsZeroRealization(p.TargetLang, p.Role)
		},
	},
	{
		ID: ClassZeroToExplicit, Plan: "42", Title: "zero realization vs explicit reference",
		Cost: DimStylistic,
		Applies: func(p Pair) bool {
			if !isCorefDimension(p.Dimension) {
				return false
			}
			// §42 already demands a unique referent here; ReferentAligned adds
			// that the referent is the *source's*, which uniqueness alone does
			// not say.
			return p.SourceZero && p.TargetOvert && p.ReferentUnique && p.ReferentAligned
		},
	},
	{
		ID: ClassCompletionAspect, Plan: "42", Title: "already vs aspectual completion marker",
		Cost: DimTemporal,
		Applies: func(p Pair) bool {
			if p.Dimension != DiffAspect {
				return false
			}
			sc := completionClass(p.Source)
			tc := completionClass(p.Target)
			return sc != "" && sc == tc
		},
	},
	{
		ID: ClassHonorificPlain, Plan: "5/42", Title: "honorific vs plain counterpart",
		Cost: DimPragmatic,
		Applies: func(p Pair) bool {
			if p.Dimension != DiffPragmatics {
				return false
			}
			return honorificClass(p.Source) != "" && honorificClass(p.Target) == ""
		},
	},
	{
		ID: ClassGenderNeutral, Plan: "22/42", Title: "they avoids gender invention",
		Cost: "",
		Applies: func(p Pair) bool {
			if p.Dimension != DiffGender {
				return false
			}
			// Only when the source genuinely failed to determine gender. A
			// gendered source pronoun must never be equated with "they".
			if normalizeGender(p.SourceGender) != GenderUndetermined {
				return false
			}
			return isGenderNeutralPronoun(p.TargetLang, p.Target)
		},
	},
}

// Classify returns the first class that covers p.
func Classify(p Pair) (Class, bool) {
	for _, c := range Classes {
		if c.Applies(p) {
			return c, true
		}
	}
	return Class{}, false
}

// Equivalent reports whether p is covered by an equivalence class and which
// one. A covered pair is never a semantic error; it may still cost the
// dimension named by the class.
func Equivalent(p Pair) (bool, ClassID) {
	c, ok := Classify(p)
	if !ok {
		return false, ""
	}
	return true, c.ID
}

func isCorefDimension(d string) bool {
	return d == DiffCoreference || d == DiffEntity || d == DiffRole
}

// --- surface vocabularies -------------------------------------------------

// japaneseHonorifics are the lexical honorifics whose English counterpart is a
// plain noun. They are listed explicitly rather than detected by script or
// length so that an ordinary kanji noun is never silently downgraded.
var japaneseHonorifics = []string{
	"先生", "せんさ", "さん", "君", "くん", "ちゃん", "様", "さま",
	"先輩", "後輩", "大使", "殿", "和尚", "お坊さん", "お嬢さん",
	"社長", "会長", "院長", "所長", "部長", "課長", "監督", "支配人",
}

// englishHonorifics is the small set English actually encodes lexically.
var englishHonorifics = []string{
	"mr", "mrs", "ms", "miss", "sir", "madam", "dr", "prof", "professor",
}

// japaneseCompletionMorphemes are the aspectual markers that encode completion
// in the slot English fills with "already".
var japaneseCompletionMorphemes = []string{
	"てしまう", "てしまった", "てしまい", "てしまわ",
}

// englishCompletionMarkers are the English completion adverbs.
var englishCompletionMarkers = []string{"already"}

// genderNeutralEN lists the English forms that deliberately decline to commit
// to a gender. Using them where the source is silent is the correct output.
var genderNeutralEN = map[string]bool{
	"they": true, "them": true, "their": true, "theirs": true,
	"themselves": true, "themself": true,
}

// genderNeutralJA lists the Japanese forms that are similarly gender blind.
// 彼 and 彼女 are deliberately absent: Japanese pronominal reference is itself
// gender marking.
var genderNeutralJA = map[string]bool{
	"あの人": true, "その人": true, "の人": true,
	"あれ": true, "これ": true, "それ": true, "もの": true, "方": true,
}

// isGenderNeutralPronoun reports whether s is a form that declines to assert
// gender in language l.
func isGenderNeutralPronoun(l lang.Lang, s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return false
	}
	switch l {
	case lang.EN:
		return genderNeutralEN[t]
	case lang.JA:
		return genderNeutralJA[t]
	}
	return false
}

// honorificClass classifies a realization as honorific-bearing or plain. It
// returns a non-empty class for material that carries respect or humility and
// "" for plain or unspecified material.
func honorificClass(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	lt := strings.ToLower(t)
	for _, h := range japaneseHonorifics {
		if h == "" {
			continue
		}
		if strings.Contains(t, h) {
			return "ja:" + h
		}
	}
	for _, h := range englishHonorifics {
		if lt == h || strings.Contains(lt, " "+h+".") || strings.Contains(lt, " "+h+",") || strings.Contains(lt, " "+h+" ") {
			return "en:" + h
		}
	}
	// Register level markers that are not tied to a referent.
	if strings.Contains(t, "でしょう") || strings.Contains(t, "だろう") {
		return "ja:hedge"
	}
	return ""
}

// completionClass maps a completion expression onto the shared aspectual class
// of plan.md §42. "already", てしまう and COMPLETED all land on "completed";
// anything else returns "" so the caller compares literally.
func completionClass(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return ""
	}
	switch t {
	case "completed", "perfect", "perfective", "already", "てしまう", "てしまった":
		return "completed"
	case "incomplete", "imperfect", "progressive", "in_progress":
		return "incomplete"
	case "inceptive":
		return "inceptive"
	case "momentary":
		return "momentary"
	}
	for _, m := range japaneseCompletionMorphemes {
		if strings.Contains(t, m) {
			return "completed"
		}
	}
	for _, m := range englishCompletionMarkers {
		if strings.Contains(t, m) {
			return "completed"
		}
	}
	return ""
}

// allowsZeroRealization reports whether the language may leave a role
// unrealized. Japanese omits subjects and objects freely; English only omits
// the subject of an imperative or an elided coordinate.
func allowsZeroRealization(l lang.Lang, role string) bool {
	if l == lang.JA {
		return true
	}
	return l == lang.EN && role != jlir.RoleAgent && role != jlir.RoleExperiencer
}

// GenderUndetermined is the normalized "the source does not determine this"
// gender value.
const GenderUndetermined = "UNKNOWN"

// normalizeGender folds the spellings of "we do not know" together. Everything
// else is compared verbatim: MALE and FEMALE never collapse into each other.
func normalizeGender(v string) string {
	t := strings.TrimSpace(strings.ToUpper(v))
	switch t {
	case "", GenderUndetermined, "NEUTRAL", "NEUTER_PROXY", "UNKNOWN_GENDER":
		return GenderUndetermined
	}
	return t
}

// normalizeNumber folds number values onto a comparable form.
func normalizeNumber(v string) string {
	t := strings.TrimSpace(strings.ToUpper(v))
	switch t {
	case "", "UNKNOWN":
		return "UNKNOWN"
	case "SING", "SG", "SINGULAR":
		return "SINGULAR"
	case "PLUR", "PLURAL":
		return "PLURAL"
	case "MASS":
		return "MASS"
	}
	return t
}

// normalizeFeature folds UNKNOWN and the empty string together so that a layer
// that never decided does not read as a layer that decided "no value". This is
// the difference between "we do not know" and "we decided there is none", and
// plan.md §22 insists they stay distinguishable.
func normalizeFeature(v string) string {
	t := strings.TrimSpace(strings.ToUpper(v))
	if t == "" || t == "UNKNOWN" {
		return ""
	}
	return t
}
