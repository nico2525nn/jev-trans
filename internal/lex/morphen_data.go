package lex

// English lexical data for AnalyzeEN: the closed classes, the irregular verb
// paradigms and the clitic forms a tokenizer cannot split by rule alone.
//
// This file is data on purpose. plan.md §25 makes the escalation ladder
// explicit (morphological decomposition → known construction → … →
// unresolved node), and plan.md §17 forbids collapsing an ambiguity. A
// rule-only tagger would silently produce one confident tag for every word its
// suffix rules happen to cover, which is exactly the invented-information
// failure mode of plan.md §4. Keeping the closed classes in a table lets the
// analyzer answer "I do not know this word" (Morph.Unknown) instead of
// guessing, and lets a word with two dictionary roles (that, set, left)
// surface as two lattice paths instead of one silent pick.

import (
	"strings"

	"github.com/nico/jev-trans/internal/forest"
)

// --- feature helpers -----------------------------------------------------

// enFs builds a feature map from alternating key/value arguments.
func enFs(kv ...string) map[string]string {
	if len(kv) < 2 {
		return nil
	}
	m := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// enClone copies a feature map so registered entries can never be mutated by
// a caller that decorates its own reading.
func enClone(f map[string]string) map[string]string {
	if len(f) == 0 {
		return nil
	}
	out := make(map[string]string, len(f))
	for k, v := range f {
		out[k] = v
	}
	return out
}

// --- lexicon -------------------------------------------------------------

// enLexEntry is one dictionary sense of a closed-class form.
type enLexEntry struct {
	POS   forest.POS
	Lem   string
	Feats map[string]string
	Dict  bool
}

// enLex maps a lowercased surface to its primary closed-class reading.
var enLex = map[string]enLexEntry{}

// enLexAlt maps a lowercased surface to further closed-class readings of the
// same string (that = determiner and complementizer, will = auxiliary and
// noun, …). Keeping them here rather than discarding them is what produces
// the "that he was tired" versus "that book" lattice paths.
var enLexAlt = map[string][]enLexEntry{}

// enDictSize is the reported dictionary size, computed once the tables are
// built so the number shown in the UI matches the tables actually consulted.
var enDictSize int

func enReg(pos forest.POS, feats map[string]string, words ...string) {
	for _, w := range words {
		enLex[w] = enLexEntry{POS: pos, Lem: w, Feats: enClone(feats), Dict: true}
	}
}

func enRegOne(w string, pos forest.POS, feats map[string]string) {
	enLex[w] = enLexEntry{POS: pos, Lem: w, Feats: enClone(feats), Dict: true}
}

func enRegAlt(w string, pos forest.POS, feats map[string]string) {
	enLexAlt[w] = append(enLexAlt[w], enLexEntry{POS: pos, Lem: w, Feats: enClone(feats), Dict: true})
}

func buildLex() {
	// Determiners ------------------------------------------------------
	enRegOne("a", forest.POSDet, enFs("class", "determiner", "article", "indefinite"))
	enRegOne("an", forest.POSDet, enFs("class", "determiner", "article", "indefinite"))
	enRegOne("the", forest.POSDet, enFs("class", "determiner", "article", "definite"))
	enReg(forest.POSDet, enFs("class", "determiner", "demonstrative", "true"),
		"this", "that", "these", "those")
	enReg(forest.POSDet, enFs("class", "determiner", "quantifier", "true"),
		"some", "any", "each", "every", "either", "neither", "another", "both",
		"all", "much", "many", "few", "little", "several", "enough", "other", "others")
	// "no" is the only overt negation English puts on the noun instead of on
	// the verb group, so it carries the same negative feature.
	enRegOne("no", forest.POSDet, enFs("class", "determiner", "quantifier", "true", "negative", "true"))
	for _, w := range []string{"my", "your", "his", "her", "its", "our", "their", "whose"} {
		enRegOne(w, forest.POSDet, enFs("class", "possessive_determiner", "possessive", "true"))
	}
	enRegAlt("that", forest.POSConj, enFs("class", "complementizer"))
	for _, w := range []string{"before", "after", "until", "till", "when", "while", "during", "throughout", "despite", "within", "once", "lest", "unless"} {
		enRegAlt(w, forest.POSConj, enFs("class", "conjunction", "subordinator", "true"))
	}
	enRegAlt("this", forest.POSPronoun, enFs("class", "pronoun", "demonstrative", "true"))
	enRegAlt("these", forest.POSPronoun, enFs("class", "pronoun", "demonstrative", "true"))
	enRegAlt("those", forest.POSPronoun, enFs("class", "pronoun", "demonstrative", "true"))
	enRegAlt("one", forest.POSPronoun, enFs("class", "pronoun"))
	enRegAlt("another", forest.POSPronoun, enFs("class", "pronoun"))
	enRegAlt("other", forest.POSPronoun, enFs("class", "pronoun"))
	enRegAlt("all", forest.POSPronoun, enFs("class", "pronoun", "quantifier", "true"))
	enRegAlt("both", forest.POSPronoun, enFs("class", "pronoun", "quantifier", "true"))
	enRegAlt("none", forest.POSPronoun, enFs("class", "pronoun", "quantifier", "true", "negative", "true"))
	enRegAlt("each", forest.POSPronoun, enFs("class", "pronoun", "quantifier", "true"))

	// Pronouns ----------------------------------------------------------
	enRegOne("i", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "number", "sing", "case", "nom"))
	enRegOne("me", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "number", "sing", "case", "obj"))
	enRegOne("my", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "case", "poss"))
	enRegOne("mine", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "possessive", "true"))
	enRegOne("you", forest.POSPronoun, enFs("class", "pronoun", "person", "2", "case", "nom"))
	enRegOne("your", forest.POSPronoun, enFs("class", "pronoun", "person", "2", "possessive", "true"))
	enRegOne("yours", forest.POSPronoun, enFs("class", "pronoun", "person", "2", "possessive", "true"))
	enRegOne("yourself", forest.POSPronoun, enFs("class", "pronoun", "person", "2", "reflexive", "true"))
	enRegOne("yourselves", forest.POSPronoun, enFs("class", "pronoun", "person", "2", "reflexive", "true"))
	enRegOne("he", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "number", "sing", "case", "nom", "gender", "male"))
	enRegOne("him", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "number", "sing", "case", "obj", "gender", "male"))
	enRegOne("his", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "possessive", "true", "gender", "male"))
	enRegOne("himself", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "reflexive", "true", "gender", "male"))
	enRegOne("she", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "number", "sing", "case", "nom", "gender", "female"))
	enRegOne("her", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "number", "sing", "case", "obj", "gender", "female"))
	enRegOne("hers", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "possessive", "true", "gender", "female"))
	enRegOne("herself", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "reflexive", "true", "gender", "female"))
	enRegOne("it", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "number", "sing", "case", "nom"))
	enRegOne("its", forest.POSPronoun, enFs("class", "pronoun", "possessive", "true"))
	enRegOne("itself", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "reflexive", "true"))
	enRegOne("we", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "number", "plur", "case", "nom"))
	enRegOne("us", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "number", "plur", "case", "obj"))
	enRegOne("our", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "possessive", "true"))
	enRegOne("ours", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "possessive", "true"))
	enRegOne("ourselves", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "reflexive", "true"))
	enRegOne("they", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "number", "plur", "case", "nom"))
	enRegOne("them", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "number", "plur", "case", "obj"))
	enRegOne("their", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "possessive", "true"))
	enRegOne("theirs", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "possessive", "true"))
	enRegOne("themselves", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "reflexive", "true"))
	enRegOne("who", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "case", "nom", "relative", "true"))
	enRegOne("whom", forest.POSPronoun, enFs("class", "pronoun", "person", "3", "case", "obj", "relative", "true"))
	enRegOne("whose", forest.POSPronoun, enFs("class", "pronoun", "possessive", "true", "relative", "true"))
	enRegOne("myself", forest.POSPronoun, enFs("class", "pronoun", "person", "1", "reflexive", "true"))
	enReg(forest.POSPronoun, enFs("class", "pronoun", "quantifier", "some"),
		"someone", "somebody", "something", "anyone", "anybody", "anything")
	enReg(forest.POSPronoun, enFs("class", "pronoun", "quantifier", "all"),
		"everyone", "everybody", "everything")
	enReg(forest.POSPronoun, enFs("class", "pronoun", "quantifier", "none", "negative", "true"),
		"nobody", "nothing", "none")
	enRegAlt("nobody", forest.POSPronoun, enFs("class", "pronoun", "quantifier", "none"))
	enRegAlt("nothing", forest.POSPronoun, enFs("class", "pronoun", "quantifier", "none"))
	enRegAlt("everyone", forest.POSPronoun, enFs("class", "pronoun", "quantifier", "all"))
	enRegAlt("everything", forest.POSPronoun, enFs("class", "pronoun", "quantifier", "all"))
	enRegAlt("what", forest.POSPronoun, enFs("class", "pronoun", "relative", "true"))
	enRegAlt("what", forest.POSConj, enFs("class", "complementizer"))
	enRegAlt("which", forest.POSPronoun, enFs("class", "pronoun", "relative", "true"))
	enRegAlt("whatever", forest.POSPronoun, enFs("class", "pronoun", "relative", "true"))
	enRegAlt("whichever", forest.POSPronoun, enFs("class", "pronoun", "relative", "true"))
	enRegAlt("wherever", forest.POSConj, enFs("class", "conjunction", "concession", "true"))

	// Prepositions ------------------------------------------------------
	enReg(forest.POSPrep, enFs("class", "preposition"),
		"in", "on", "at", "by", "for", "with", "about", "against", "between",
		"among", "amid", "into", "through", "throughout", "during", "before",
		"after", "above", "below", "from", "of", "off", "out", "over", "under", "to",
		"upon", "within", "without", "across", "behind", "beyond", "despite",
		"toward", "towards", "onto", "near", "per", "until", "till")

	// Adverbs and particles --------------------------------------------
	enReg(forest.POSAdv, enFs("class", "adverb"), "then", "now", "here", "there",
		"today", "tomorrow", "yesterday", "tonight",
		"always", "already", "still", "just", "also", "too", "very", "quite",
		"rather", "soon", "again", "once", "twice", "only", "even", "well",
		"fast", "hard", "late", "early", "together", "alone", "perhaps", "maybe",
		"probably", "certainly", "definitely", "however", "therefore", "thus",
		"moreover", "furthermore", "indeed", "almost", "nearly", "hardly",
		"barely", "suddenly", "finally", "recently", "lately", "anymore",
		"anyway", "besides", "otherwise", "instead", "abroad", "anywhere",
		"everywhere", "somewhere", "nowhere", "elsewhere", "else")
	// Negation. English negation is overt either on the verb group or on the
	// determiner; both have to reach the clause (plan.md §17).
	enReg(forest.POSAdv, enFs("class", "adverb", "negative", "true"),
		"not", "never", "nor", "neither")
	enRegAlt("nor", forest.POSConj, enFs("class", "conjunction", "splitter", "true"))
	enRegAlt("neither", forest.POSConj, enFs("class", "conjunction", "splitter", "true"))

	// Conjunctions ------------------------------------------------------
	enReg(forest.POSConj, enFs("class", "conjunction", "splitter", "true"),
		"and", "or", "but", "yet")
	enReg(forest.POSConj, enFs("class", "conjunction", "subordinator", "true"),
		"because", "if", "unless", "when", "while", "although", "though",
		"whereas", "until", "once", "lest")
	enRegAlt("so", forest.POSAdv, enFs("class", "adverb"))
	enRegOne("so", forest.POSConj, enFs("class", "conjunction", "subordinator", "true"))
	enRegOne("because", forest.POSPrep, enFs("class", "preposition"))
	enRegAlt("because", forest.POSConj, enFs("class", "conjunction", "subordinator", "true"))
	enRegOne("since", forest.POSPrep, enFs("class", "preposition"))
	enRegAlt("since", forest.POSConj, enFs("class", "conjunction", "subordinator", "true"))
	enRegOne("as", forest.POSPrep, enFs("class", "preposition"))
	enRegAlt("as", forest.POSConj, enFs("class", "conjunction", "subordinator", "true"))
	enRegAlt("as", forest.POSAdv, enFs("class", "adverb"))
	enRegOne("for", forest.POSPrep, enFs("class", "preposition"))
	enRegAlt("for", forest.POSConj, enFs("class", "conjunction", "subordinator", "true"))
	enRegOne("than", forest.POSPrep, enFs("class", "preposition"))
	enRegAlt("than", forest.POSConj, enFs("class", "conjunction", "comparative", "true"))
	enRegOne("like", forest.POSPrep, enFs("class", "preposition"))
	enRegAlt("like", forest.POSVerb, enFs("class", "verb", "tense", "present"))
	enRegAlt("what", forest.POSDet, enFs("class", "determiner"))
	enRegAlt("which", forest.POSDet, enFs("class", "determiner"))
	enRegOne("which", forest.POSPronoun, enFs("class", "pronoun", "relative", "true"))

	// Auxiliaries -------------------------------------------------------
	enAuxLem("be", "be", "tense", "present", "part", "base", "copula", "true")
	enAuxLem("am", "be", "tense", "present", "part", "finite", "copula", "true", "person", "1", "number", "sing")
	enAuxLem("is", "be", "tense", "present", "part", "finite", "copula", "true", "person", "3", "number", "sing")
	enAuxLem("are", "be", "tense", "present", "part", "finite", "copula", "true", "person", "2", "number", "plur")
	enAuxLem("was", "be", "tense", "past", "part", "finite", "copula", "true", "person", "3", "number", "sing")
	enAuxLem("were", "be", "tense", "past", "part", "finite", "copula", "true", "person", "2", "number", "plur")
	enAuxLem("been", "be", "tense", "past", "part", "participle", "copula", "true")
	enAuxLem("being", "be", "tense", "present", "part", "gerund", "copula", "true")
	enAuxLem("have", "have", "tense", "present", "part", "base")
	enAuxLem("has", "have", "tense", "present", "part", "finite", "person", "3", "number", "sing")
	enAuxLem("had", "have", "tense", "past", "part", "finite")
	enAuxLem("having", "have", "tense", "present", "part", "gerund")
	enAuxLem("do", "do", "tense", "present", "part", "base")
	enAuxLem("does", "do", "tense", "present", "part", "finite", "person", "3", "number", "sing")
	enAuxLem("did", "do", "tense", "past", "part", "finite")
	enAuxLem("doing", "do", "tense", "present", "part", "gerund")
	enAux("ought", "tense", "present", "part", "finite")

	// Modals ------------------------------------------------------------
	enModal("will", "tense", "future", "part", "finite")
	enModal("would", "tense", "past", "part", "finite")
	enModal("shall", "tense", "future", "part", "finite")
	enModal("should", "tense", "present", "part", "finite")
	enModal("can", "tense", "present", "part", "finite")
	enModal("could", "tense", "past", "part", "finite")
	enModal("may", "tense", "present", "part", "finite")
	enModal("might", "tense", "past", "part", "finite")
	enModal("must", "tense", "present", "part", "finite")
	enRegAlt("will", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("can", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("might", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("must", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("need", forest.POSVerb, enFs("class", "verb", "tense", "present"))
	enAux("need", "tense", "present", "part", "base")
	enRegAlt("dare", forest.POSVerb, enFs("class", "verb", "tense", "present"))
	enAux("dare", "tense", "present", "part", "base")

	// Words that are both noun and verb. English morphology is not a function
	// of the string alone, so both readings must survive into the lattice.
	for _, w := range []string{"help", "work", "walk", "talk", "call", "answer",
		"name", "place", "part", "power", "show", "turn", "form", "light",
		"watch", "run", "plan", "question", "study", "report", "act", "play",
		"water", "cook", "drink", "back", "change", "dream", "experience",
		"interest", "land", "love", "mind", "note", "park", "pound", "rest",
		"shop", "smile", "sound", "space", "trade"} {
		enRegAlt(w, forest.POSNoun, enFs("class", "noun"))
	}
	enRegAlt("left", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("left", forest.POSAdj, enFs("class", "adjective"))
	enRegAlt("saw", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("set", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("cut", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("well", forest.POSAdj, enFs("class", "adjective"))
	enRegAlt("well", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("round", forest.POSPrep, enFs("class", "preposition"))
	enRegAlt("round", forest.POSAdj, enFs("class", "adjective"))
	enRegAlt("round", forest.POSNoun, enFs("class", "noun"))
	enRegAlt("past", forest.POSPrep, enFs("class", "preposition"))
	enRegAlt("past", forest.POSAdj, enFs("class", "adjective"))
	enRegAlt("past", forest.POSNoun, enFs("class", "noun"))
}

func enAux(w string, kv ...string) {
	feats := append([]string{"class", "auxiliary", "auxiliary", "true"}, kv...)
	enRegOne(w, forest.POSAux, enFs(feats...))
}

// enAuxLem registers an auxiliary whose dictionary form is not its lemma
// ("was" is a form of "be"). The semantic layer resolves predicates by
// lemma, so a wrong lemma here would silently mis-classify the predicate.
func enAuxLem(w, lem string, kv ...string) {
	feats := append([]string{"class", "auxiliary", "auxiliary", "true"}, kv...)
	enLex[w] = enLexEntry{POS: forest.POSAux, Lem: lem, Feats: enFs(feats...), Dict: true}
}

func enModal(w string, kv ...string) {
	feats := append([]string{"class", "modal", "modal", "true", "auxiliary", "true"}, kv...)
	enRegOne(w, forest.POSAux, enFs(feats...))
}

// --- irregular verbs -----------------------------------------------------

// enIrregular is one verb paradigm. Past may name several surface forms
// ("was/were"), which is itself information: person and number must not be
// invented for a bare "was".
type enIrregular struct {
	Past string
	PP   string
}

var enIrregularVerbs = map[string]enIrregular{
	"be":         {"was/were", "been"},
	"have":       {"had", "had"},
	"do":         {"did", "done"},
	"go":         {"went", "gone"},
	"eat":        {"ate", "eaten"},
	"give":       {"gave", "given"},
	"take":       {"took", "taken"},
	"see":        {"saw", "seen"},
	"meet":       {"met", "met"},
	"come":       {"came", "come"},
	"know":       {"knew", "known"},
	"think":      {"thought", "thought"},
	"want":       {"wanted", "wanted"},
	"say":        {"said", "said"},
	"tell":       {"told", "told"},
	"speak":      {"spoke", "spoken"},
	"read":       {"read", "read"},
	"write":      {"wrote", "written"},
	"buy":        {"bought", "bought"},
	"bring":      {"brought", "brought"},
	"hold":       {"held", "held"},
	"keep":       {"kept", "kept"},
	"leave":      {"left", "left"},
	"find":       {"found", "found"},
	"feel":       {"felt", "felt"},
	"become":     {"became", "become"},
	"begin":      {"began", "begun"},
	"break":      {"broke", "broken"},
	"choose":     {"chose", "chosen"},
	"drive":      {"drove", "driven"},
	"fall":       {"fell", "fallen"},
	"get":        {"got", "got"},
	"grow":       {"grew", "grown"},
	"hear":       {"heard", "heard"},
	"let":        {"let", "let"},
	"lose":       {"lost", "lost"},
	"make":       {"made", "made"},
	"mean":       {"meant", "meant"},
	"put":        {"put", "put"},
	"run":        {"ran", "run"},
	"send":       {"sent", "sent"},
	"set":        {"set", "set"},
	"sit":        {"sat", "sat"},
	"stand":      {"stood", "stood"},
	"stay":       {"stayed", "stayed"},
	"teach":      {"taught", "taught"},
	"understand": {"understood", "understood"},
	"wear":       {"wore", "worn"},
	"win":        {"won", "won"},
	"drink":      {"drank", "drunk"},
	"sing":       {"sang", "sung"},
	"swim":       {"swam", "swum"},
	"ride":       {"rode", "ridden"},
	"rise":       {"rose", "risen"},
	"sleep":      {"slept", "slept"},
	"build":      {"built", "built"},
	"burn":       {"burnt", "burnt"},
	"deal":       {"dealt", "dealt"},
	"dream":      {"dreamt", "dreamt"},
	"freeze":     {"froze", "frozen"},
	"hide":       {"hid", "hidden"},
	"hit":        {"hit", "hit"},
	"hurt":       {"hurt", "hurt"},
	"lay":        {"laid", "laid"},
	"lead":       {"led", "led"},
	"lend":       {"lent", "lent"},
	"light":      {"lit", "lit"},
	"seek":       {"sought", "sought"},
	"shake":      {"shook", "shaken"},
	"shoot":      {"shot", "shot"},
	"shut":       {"shut", "shut"},
	"spend":      {"spent", "spent"},
	"spread":     {"spread", "spread"},
	"steal":      {"stole", "stolen"},
	"strike":     {"struck", "struck"},
	"swear":      {"swore", "sworn"},
	"sweep":      {"swept", "swept"},
	"swing":      {"swung", "swung"},
	"throw":      {"threw", "thrown"},
	"wake":       {"woke", "woken"},
	"bear":       {"bore", "borne"},
	"beat":       {"beat", "beat"},
	"bite":       {"bit", "bitten"},
	"blow":       {"blew", "blown"},
	"catch":      {"caught", "caught"},
	"cut":        {"cut", "cut"},
	"feed":       {"fed", "fed"},
	"fight":      {"fought", "fought"},
	"forgive":    {"forgave", "forgiven"},
	"forbid":     {"forbade", "forbidden"},
	"hang":       {"hung", "hung"},
	"overcome":   {"overcame", "overcome"},
	"pay":        {"paid", "paid"},
	"prove":      {"proved", "proved"},
	"split":      {"split", "split"},
	"strive":     {"strove", "striven"},
	"undertake":  {"undertook", "undertaken"},
	"withdraw":   {"withdrew", "withdrawn"},
	"cling":      {"clung", "clung"},
	"flee":       {"fled", "fled"},
	"grind":      {"ground", "ground"},
	"leap":       {"leapt", "leapt"},
	"shine":      {"shone", "shone"},
	"slide":      {"slid", "slid"},
	"spin":       {"spun", "spun"},
	"tear":       {"tore", "torn"},
	"wind":       {"wound", "wound"},
}

// enVerbReading is one irregular surface form of one lemma.
type enVerbReading struct {
	Lemma string
	Kind  string // "past", "participle", "present3"
}

// enVerbForm indexes inflected verb surfaces back to their lemma. One surface
// can carry several forms ("found" is both past and participle of find) and
// several lemmas can share a surface; the slice is the ambiguity, not noise.
var enVerbForm = map[string][]enVerbReading{}

// enSelfInflected lists the verbs whose past and participle are spelled exactly
// like the base form. They are the reason "read", "set" and "put" cannot be
// lemmatized by suffix alone.
var enSelfInflected = map[string]bool{
	"read": true, "set": true, "put": true, "cut": true, "let": true,
	"spread": true, "shoot": true, "cost": true, "hurt": true, "shut": true,
}

// enAlsoNoun lists verb forms that are also ordinary nouns, so the lattice
// keeps the noun reading of "left", "saw", "cut", "set".
var enAlsoNoun = map[string]bool{
	"left": true, "saw": true, "set": true, "cut": true, "run": true,
	"lie": true, "light": true, "watch": true, "wind": true, "bear": true,
	"beat": true, "hit": true, "lay": true, "lead": true, "read": true,
	"sting": true, "strike": true, "swim": true, "weep": true, "pound": true,
	"rest": true, "land": true, "park": true, "sound": true, "water": true,
}

func buildVerbs() {
	add := func(form, lemma, kind string) {
		if form == "" {
			return
		}
		if form == lemma && !enSelfInflected[lemma] {
			// A form identical to its lemma still has to be reachable as
			// past or participle for "was read" to be a passive.
			return
		}
		enVerbForm[form] = append(enVerbForm[form], enVerbReading{Lemma: lemma, Kind: kind})
	}
	for lemma, v := range enIrregularVerbs {
		for _, past := range strings.Split(v.Past, "|") {
			add(past, lemma, "past")
		}
		add(v.PP, lemma, "participle")
		third := lemma + "s"
		switch lemma {
		case "be":
			third = "is"
		case "have":
			third = "has"
		case "do":
			third = "does"
		case "go":
			third = "goes"
		}
		add(third, lemma, "present3")
		// The base form itself is a dictionary entry too: without it "say"
		// or "go" would only be reachable through an inflected form.
		enVerbForm[lemma] = append(enVerbForm[lemma], enVerbReading{Lemma: lemma, Kind: "base"})
	}
	for form := range enVerbForm {
		sortEnVerbForms(form)
	}
}

// sortEnVerbForms makes the reading order of a surface deterministic and drops
// duplicates, since the reverse index is built from a Go map.
func sortEnVerbForms(form string) {
	rs := enVerbForm[form]
	if len(rs) < 2 {
		return
	}
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0; j-- {
			a, b := rs[j-1], rs[j]
			if a.Lemma < b.Lemma || (a.Lemma == b.Lemma && a.Kind <= b.Kind) {
				break
			}
			rs[j-1], rs[j] = b, a
		}
	}
	seen := map[string]bool{}
	out := rs[:0]
	for _, r := range rs {
		key := r.Lemma + "|" + r.Kind
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	enVerbForm[form] = out
}

// --- contractions --------------------------------------------------------

// enConPart is one clitic piece of a contracted form.
type enConPart struct {
	Surface string
	POS     forest.POS
	Feats   map[string]string
	Lem     string
}

// enConForm is one way of segmenting a contracted token.
type enConForm struct {
	Parts  []enConPart
	Weight float64
	Note   string
}

// enClitic is the fixed second piece of a contraction, keyed by the
// apostrophe form that remains once the base has been peeled off.
var enClitic map[string][]enConForm

// enSpecialContractions holds forms the generic apostrophe rules cannot
// produce, either because the clitic has no apostrophe ("cannot") or because
// the apostrophe is not a possessive ("let's").
var enSpecialContractions = map[string][]enConForm{
	"cannot": {{
		Parts: []enConPart{{
			Surface: "cannot", POS: forest.POSAux, Lem: "can",
			Feats: enFs("class", "modal", "modal", "true", "auxiliary", "true",
				"tense", "present", "negative", "true"),
		}},
		Weight: 0.9,
		Note:   "cannot carries negation without an apostrophe",
	}},
	"let's": {{
		Parts: []enConPart{
			{Surface: "let", POS: forest.POSVerb, Lem: "let",
				Feats: enFs("tense", "present", "part", "base")},
			{Surface: "us", POS: forest.POSPronoun, Lem: "us",
				Feats: enFs("class", "pronoun", "person", "1", "number", "plur", "case", "obj")},
		},
		Weight: 0.92,
		Note:   "let's is let us, not a possessive",
	}},
	"o'clock": {{
		Parts: []enConPart{
			{Surface: "of", POS: forest.POSPrep, Lem: "of", Feats: enFs("class", "preposition")},
			{Surface: "the", POS: forest.POSDet, Lem: "the",
				Feats: enFs("class", "determiner", "article", "definite")},
			{Surface: "clock", POS: forest.POSNoun, Lem: "clock", Feats: enFs("class", "noun")},
		},
		Weight: 0.7,
		Note:   "o'clock stands for of the clock",
	}},
	"y'all": {{
		Parts: []enConPart{
			{Surface: "you", POS: forest.POSPronoun, Lem: "you", Feats: enFs("class", "pronoun", "person", "2")},
			{Surface: "all", POS: forest.POSPronoun, Lem: "all", Feats: enFs("class", "pronoun", "quantifier", "true")},
		},
		Weight: 0.85,
	}},
}

// enCopularBases are the bases whose "'s" is really a copula. "he's" is
// therefore two readings, and choosing between them here would be choosing
// between "he is" and "his" without any evidence (plan.md §4).
var enCopularBases = map[string]string{
	"he": "is", "she": "is", "it": "is", "that": "is", "this": "is",
	"there": "is", "here": "is", "where": "is", "how": "is", "what": "is",
	"who": "is", "when": "is", "one": "is",
}

func init() {
	buildLex()
	// The open-class nouns and adjectives are registered after the closed
	// class so that a form registered in both keeps the closed-class reading,
	// which is the more reliable of the two.
	buildOpenClass()
	buildVerbs()
	buildContractions()
	enDictSize = len(enLex) + len(enIrregularVerbs)*4
}

// buildContractions precomputes the clitic pieces whose second element is a
// fixed function word rather than a tagged base.
func buildContractions() {
	enClitic = map[string][]enConForm{
		"'m": {{
			Parts: []enConPart{{
				Surface: "am", POS: forest.POSAux, Lem: "am",
				Feats: enFs("class", "auxiliary", "auxiliary", "true", "copula", "true",
					"tense", "present", "part", "finite", "person", "1", "number", "sing"),
			}},
			Weight: 0.9,
			Note:   "'m contracts am",
		}},
		"'re": {{
			Parts: []enConPart{{
				Surface: "are", POS: forest.POSAux, Lem: "are",
				Feats: enFs("class", "auxiliary", "auxiliary", "true", "copula", "true",
					"tense", "present", "part", "finite", "person", "2", "number", "plur"),
			}},
			Weight: 0.9,
			Note:   "'re contracts are",
		}},
		"'ve": {{
			Parts: []enConPart{{
				Surface: "have", POS: forest.POSAux, Lem: "have",
				Feats: enFs("class", "auxiliary", "auxiliary", "true", "tense", "present", "part", "base"),
			}},
			Weight: 0.9,
			Note:   "'ve contracts have",
		}},
		"'ll": {{
			Parts: []enConPart{{
				Surface: "will", POS: forest.POSAux, Lem: "will",
				Feats: enFs("class", "modal", "modal", "true", "auxiliary", "true",
					"tense", "future", "part", "finite"),
			}},
			Weight: 0.9,
			Note:   "'ll contracts will",
		}},
		"'d": {{
			Parts: []enConPart{{
				Surface: "had", POS: forest.POSAux, Lem: "had",
				Feats: enFs("class", "auxiliary", "auxiliary", "true", "tense", "past", "part", "finite"),
			}},
			Weight: 0.6,
			Note:   "'d is had or would; both readings are kept",
		}, {
			Parts: []enConPart{{
				Surface: "would", POS: forest.POSAux, Lem: "would",
				Feats: enFs("class", "modal", "modal", "true", "auxiliary", "true",
					"tense", "past", "part", "finite"),
			}},
			Weight: 0.55,
			Note:   "'d is had or would; both readings are kept",
		}},
		"'s": {{
			Parts: []enConPart{{
				Surface: "'s", POS: forest.POSAux, Lem: "'s",
				Feats: enFs("class", "clitic", "possessive", "true", "marker", "possessive"),
			}},
			Weight: 0.8,
			Note:   "possessive clitic",
		}},
		"'t": {{
			Parts: []enConPart{{
				Surface: "'t", POS: forest.POSAdv, Lem: "not",
				Feats: enFs("class", "adverb", "negative", "true", "contracted", "true"),
			}},
			Weight: 0.95,
			Note:   "'t negates the auxiliary in front of it",
		}},
	}
}

// --- small helpers -------------------------------------------------------

// enHasAnySuffix reports whether w ends with one of the given suffixes.
func enHasAnySuffix(w string, suffixes ...string) bool {
	for _, s := range suffixes {
		if strings.HasSuffix(w, s) {
			return true
		}
	}
	return false
}

// enDoubledFinal reports whether base ends in a doubled final consonant, as
// in run + "-ning" → running, or stop + "-ped" → stopped. It is the standard
// rule with the usual exceptions: s, x and z are never doubled, and l is only
// doubled after a single short vowel.
func enDoubledFinal(base string) bool {
	n := len(base)
	if n < 3 || n%2 == 0 {
		return false
	}
	a, b, c := base[n-3], base[n-2], base[n-1]
	if a != b {
		return false
	}
	switch c {
	case 's', 'x', 'z':
		return false
	case 'l':
		return !strings.ContainsRune("aeiou", rune(a))
	default:
		return !strings.ContainsRune("aeiouwxy", rune(a))
	}
}
