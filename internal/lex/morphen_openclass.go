package lex

import "github.com/nico/jev-trans/internal/forest"

// This file is the English open-class noun lexicon.
//
// The English analyzer's tables were deliberately limited to closed-class
// material, on the theory that open-class words belong to internal/lexicon.
// But internal/lexicon maps a surface to a *predicate sense* and carries no
// part of speech and no morphology, so ordinary nouns had nowhere to live.
//
// That was not a smaller index; it was a hole in the safety net. plan.md §40
// makes re-parsing every generated sentence the core defence, and a re-parser
// that tags "teacher" as UNKNOWN cannot tell a correct translation from a
// wrong one — it returns UNKNOWN.VERB for both, so the verifier either rejects
// good candidates or, worse, matches garbage against garbage and reports EXACT.
//
// Every entry here is a noun with its lemma. Nothing in this file assigns a
// predicate sense: that remains internal/lexicon's job, and a noun the lexicon
// does not know still parses as a noun, which is the honest outcome — an
// unrepresented referent, not an invented one.

// enRegNoun registers a countable noun. The count flag matters downstream:
// English requires a determiner and a plural where Japanese requires neither,
// and the article chooser needs to know which.
func enRegNoun(countable bool, words ...string) {
	for _, w := range words {
		feats := map[string]string{"class": "noun"}
		if countable {
			feats["countable"] = "true"
			feats["number"] = "sg"
		} else {
			feats["countable"] = "false"
			feats["number"] = "mass"
		}
		enLex[w] = enLexEntry{POS: forest.POSNoun, Lem: w, Feats: enClone(feats), Dict: true}
	}
}

// enRegNounIrregular registers a countable noun whose plural is not formed by
// adding -s. The analyzer has no morphology rule for these, so an entry is the
// only way "children" parses.
func enRegNounIrregular(plural, lemma string) {
	enLex[plural] = enLexEntry{POS: forest.POSNoun, Lem: lemma,
		Feats: enClone(map[string]string{"class": "noun", "countable": "true", "number": "pl"}),
		Dict:  true}
	enLex[lemma] = enLexEntry{POS: forest.POSNoun, Lem: lemma,
		Feats: enClone(map[string]string{"class": "noun", "countable": "true", "number": "sg"}),
		Dict:  true}
}

func buildOpenClass() {
	// --- people --------------------------------------------------------
	enRegNoun(true, "person", "man", "woman", "boy", "girl", "child", "friend",
		"neighbour", "neighbor", "teacher", "student", "doctor", "engineer",
		"lawyer", "writer", "artist", "singer", "player", "coach", "driver",
		"worker", "farmer", "soldier", "officer", "manager", "director",
		"professor", "scientist", "author", "reader", "speaker", "listener",
		"customer", "client", "visitor", "guest", "host", "owner", "partner",
		"colleague", "boss", "stranger", "neighbour", "mother", "father",
		"sister", "brother", "son", "daughter", "cousin", "uncle", "aunt",
		"wife", "husband", "parent", "ancestor", "twin", "name", "title")
	enRegNoun(true, "children", "people", "men", "women", "boys", "girls",
		"friends", "teachers", "students", "parents", "wives", "husbands")
	enRegNoun(true, "child")
	enRegNounIrregular("children", "child")
	enRegNounIrregular("people", "person")
	enRegNounIrregular("men", "man")
	enRegNounIrregular("women", "woman")

	// --- animals --------------------------------------------------------
	enRegNoun(true, "animal", "pet", "dog", "cat", "bird", "fish", "horse",
		"cow", "pig", "sheep", "mouse", "rat", "snake", "frog", "bee",
		"insect", "wolf", "bear", "lion", "tiger", "monkey", "rabbit",
		"deer", "duck", "goose", "whale", "shark", "fox")
	enRegNoun(false, "meat", "fur")

	// --- body and health -------------------------------------------------
	enRegNoun(true, "head", "eye", "ear", "face", "hand", "foot", "leg",
		"arm", "finger", "tooth", "hair", "heart", "bone", "skin", "voice",
		"name", "body", "leg", "arm")
	enRegNoun(false, "blood", "health", "medicine", "food")
	enRegNoun(true, "disease", "illness", "symptom", "wound")

	// --- food -----------------------------------------------------------
	enRegNoun(false, "rice", "water", "milk", "tea", "coffee", "juice", "wine",
		"beer", "bread", "meat", "fish", "soup", "cheese", "butter", "sugar",
		"salt", "oil", "food", "breakfast", "lunch", "dinner", "meal")
	enRegNoun(true, "egg", "apple", "orange", "potato", "tomato", "cake",
		"sandwich", "noodle", "fruit", "vegetable", "dish", "plate", "bowl",
		"cup", "glass", "bottle", "knife", "fork", "spoon")
	enRegNounIrregular("feet", "foot")
	enRegNounIrregular("teeth", "tooth")
	enRegNounIrregular("men", "man")

	// --- objects ---------------------------------------------------------
	enRegNoun(true, "book", "letter", "note", "paper", "page", "picture",
		"photograph", "map", "card", "ticket", "key", "door", "window", "wall",
		"floor", "roof", "house", "room", "table", "chair", "desk", "bed",
		"bag", "box", "bottle", "pen", "pencil", "clock", "watch", "phone",
		"computer", "machine", "tool", "camera", "radio", "lamp", "mirror",
		"basket", "broom", "camera", "screen", "keyboard", "file", "folder",
		"envelope", "stamp", "coin", "ring", "toy", "ball", "kite", "doll")
	enRegNoun(false, "furniture", "equipment", "luggage", "money", "glass",
		"plastic", "metal", "wood", "cloth")
	enRegNoun(true, "clothes", "shoe", "hat", "coat", "shirt", "trousers",
		"dress", "sock", "glove", "scarf")
	enRegNoun(true, "car", "bus", "train", "plane", "ship", "boat", "bike",
		"bicycle", "truck", "taxi", "road", "street", "bridge", "station",
		"airport", "ticket")

	// --- places ----------------------------------------------------------
	enRegNoun(true, "city", "town", "village", "country", "state", "region",
		"island", "mountain", "river", "sea", "lake", "forest", "desert",
		"beach", "park", "garden", "farm", "field", "shop", "store", "market",
		"office", "factory", "hospital", "school", "university", "library",
		"museum", "church", "temple", "hotel", "restaurant", "bar", "theatre",
		"theater", "bank", "post office", "airport", "port", "gate", "wall")
	enRegNoun(false, "space", "air", "water", "land", "nature", "history",
		"government", "society", "culture", "language", "music", "art")
	enRegNoun(true, "word", "sentence", "paragraph", "question", "answer",
		"problem", "idea", "story", "book", "letter", "number", "list")

	// --- time and events --------------------------------------------------
	enRegNoun(true, "day", "week", "month", "year", "hour", "minute", "second",
		"morning", "afternoon", "evening", "night", "weekend", "birthday",
		"holiday", "meeting", "party", "lesson", "class", "course", "exam",
		"concert", "game", "match", "race", "trip", "journey", "flight")
	enRegNoun(false, "time", "weather", "sunlight", "rain", "snow", "wind")
	enRegNoun(true, "date", "calendar", "clock", "season", "spring", "summer",
		"autumn", "winter")

	// --- abstract ---------------------------------------------------------
	enRegNoun(false, "work", "information", "knowledge", "experience",
		"education", "research", "news", "advice", "help", "money", "price",
		"cost", "value", "reason", "result", "effect", "problem", "solution",
		"plan", "purpose", "goal", "risk", "chance", "fact", "truth", "power",
		"freedom", "peace", "health", "happiness", "sadness", "fear", "hope",
		"love", "kindness", "respect", "trust", "safety", "justice", "law",
		"policy", "rule", "right", "duty", "skill", "quality", "quality")
	enRegNoun(true, "decision", "question", "doubt", "idea", "opinion",
		"belief", "habit", "custom", "rule", "law", "skill", "talent", "tool")

	// Adjectives. Registering them is what stops a re-parse from mistaking
	// "red book" for two nouns, which the clause builder cannot order.
	enReg(forest.POSAdj, enFs("class", "adjective"), "good", "bad", "big",
		"small", "large", "little", "long", "short", "high", "low", "old",
		"new", "young", "hot", "cold", "warm", "cool", "fast", "slow", "easy",
		"hard", "difficult", "simple", "clear", "dark", "light", "heavy",
		"strong", "weak", "clean", "dirty", "happy", "sad", "angry", "tired",
		"beautiful", "ugly", "nice", "kind", "quiet", "loud", "red", "blue",
		"green", "white", "black", "brown", "grey", "gray", "yellow", "orange")

	// Adverbs that are not formed from an adjective.
	enReg(forest.POSAdv, enFs("class", "adverb"), "very", "quite", "rather",
		"almost", "always", "never", "often", "sometimes", "usually",
		"already", "still", "just", "soon", "later", "again", "together",
		"perhaps", "maybe", "probably", "certainly", "really", "truly",
		"finally", "recently", "currently", "originally", "suddenly",
		"carefully", "quickly", "slowly", "loudly", "quietly", "clearly")
}
