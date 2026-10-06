package plan

// realize_test.go: regression tests for the defects this package's refactor
// fixed. Each test names the behaviour plan.md requires, not the code.

import (
	"strings"
	"testing"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
)

func jaOut(t *testing.T, events ...EventPlan) []string {
	t.Helper()
	r := Request{Source: lang.JA, Target: lang.JA, Style: StyleProfile{Register: RegisterNeutral}}
	p := &Projection{Source: lang.JA, Target: lang.JA, Events: events}
	real := Realize(r, p)
	var out []string
	for _, c := range real.Candidates(12) {
		out = append(out, c.Text)
	}
	return out
}

func enOut(t *testing.T, events ...EventPlan) []string {
	t.Helper()
	r := Request{Source: lang.EN, Target: lang.EN, Style: StyleProfile{Register: RegisterNeutral}}
	p := &Projection{Source: lang.EN, Target: lang.EN, Events: events}
	real := Realize(r, p)
	var out []string
	for _, c := range real.Candidates(12) {
		out = append(out, c.Text)
	}
	return out
}

func transferEvent() EventPlan {
	return EventPlan{
		EventID: "e2", SenseID: "TRANSFER.01", SenseFamily: "TRANSFER",
		Tense: jlir.TensePast, Polarity: jlir.PolarityPositive,
		Construction: "C.GIVE.JA.01", Constructions: []string{"C.GIVE.JA.01"},
		Args: map[string]*NPPlan{
			jlir.RoleAgent:     {Noun: "Taro", Proper: true},
			jlir.RoleTheme:     {Noun: "book"},
			jlir.RoleRecipient: {Noun: "Hanako", Proper: true},
		},
	}
}

// TestTransferDoesNotReverseIntoReceive is plan.md §36's
// Semantics(T) ⊇ RequiredMeaning at the level of a single frame: the RECEIVE
// frame has no recipient slot, so offering it for a hand-over silently drops
// the recipient and reverses the proposition.
func TestTransferDoesNotReverseIntoReceive(t *testing.T) {
	ev := transferEvent()
	ev.Constructions = []string{"C.GIVE.JA.01", "C.RECV.JA.01"}
	ev.Construction = "C.GIVE.JA.01"
	p := &Projection{Source: lang.JA, Target: lang.JA, Events: []EventPlan{ev}}
	r := Request{Source: lang.JA, Target: lang.JA, Style: StyleProfile{Register: RegisterNeutral}}
	real := Realize(r, p)
	rt := &realizer{r: r, p: p, b: nil, out: real, l: lang.JA}
	recv, _ := Default().Get("C.RECV.JA.01")
	if recv == nil {
		t.Fatal("C.RECV.JA.01 is missing from the library")
	}
	if ok, rule, why := rt.checkConstruction(&ev, recv); ok {
		t.Fatal("the RECEIVE frame was admitted for a hand-over: " + why)
	} else if rule != HardWrongPredicate && rule != HardRoleUnrealizable {
		t.Errorf("rejected by %s, want a hard violation (%s)", rule, why)
	}
	give, _ := Default().Get("C.GIVE.JA.01")
	if ok, _, why := rt.checkConstruction(&ev, give); !ok {
		t.Errorf("the GIVE frame was rejected for a hand-over: %s", why)
	}
}

// TestSelectionIsKeyedBySense checks that the library indexes senses, not
// families: TRANSFER.01 and TRANSFER.09 are opposite propositions inside one
// ontology family.
func TestSelectionIsKeyedBySense(t *testing.T) {
	lib := Default()
	give := lib.forSense("TRANSFER.01", lang.EN)
	for _, c := range give {
		if c.Family == "RECEIVE" {
			t.Errorf("%s (%s) is offered for TRANSFER.01", c.ID, c.Family)
		}
	}
	recv := lib.forSense("TRANSFER.09", lang.EN)
	if len(recv) == 0 {
		t.Fatal("TRANSFER.09 (receive) has no frame")
	}
	for _, c := range recv {
		if c.Family == "GIVE" {
			t.Errorf("%s is offered for TRANSFER.09", c.ID)
		}
	}
}

// TestEveryConstructionDeclaresASense is the invariant that makes the sense
// index sound: a construction naming no sense would be unreachable, and
// "the library has 180 constructions" would silently mean "however many of them
// some sense happens to reach".
func TestEveryConstructionDeclaresASense(t *testing.T) {
	for _, c := range builtinConstructions() {
		if len(c.Senses) == 0 {
			t.Errorf("%s declares no sense and can never be selected", c.ID)
		}
	}
}

// TestModalFamilyIsReachable covers the family that used to fall through to
// the fallback frame: MODAL resolved through candidateFamilies to nothing, so
// eight constructions were unreachable.
func TestModalFamilyIsReachable(t *testing.T) {
	lib := Default()
	for _, sense := range []string{"MODAL.01", "MODAL.02", "MODAL.03", "MODAL.04", "MODAL.05", "MODAL.06", "MODAL.07"} {
		if n := len(lib.forSense(sense, lang.EN)) + len(lib.forSense(sense, lang.JA)); n == 0 {
			t.Errorf("no construction realizes %s", sense)
		}
	}
	for _, id := range []string{"C.MAY.EN.01", "C.MAY.EN.02", "C.MAY.JA.01",
		"C.SELL.EN.01", "C.SELL.JA.01", "C.SELL.JA.02"} {
		c, ok := lib.Get(id)
		if !ok {
			t.Errorf("%s is not in the library", id)
			continue
		}
		found := false
		for _, s := range c.Senses {
			if len(lib.forSense(s, c.Target)) > 0 {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is unreachable", id)
		}
	}
}

// TestJapanesePatternsAreSOVOrder guards the rewrite that deleted the
// realization-time reordering: a Japanese pattern must not list anything after
// its verb, and every case particle must follow the slot it marks.
func TestJapanesePatternsAreSOVOrder(t *testing.T) {
	for _, c := range builtinConstructions() {
		if c.Target != lang.JA {
			continue
		}
		toks := tokens(c.Pattern)
		vAt := -1
		for i, tok := range toks {
			if tok == "V" {
				vAt = i
				break
			}
		}
		if vAt < 0 {
			continue
		}
		for i, tok := range toks {
			if i <= vAt || isSlotToken(tok) {
				continue
			}
			// A lowercase token after V can only be a TAIL particle, which no
			// construction uses; an argument slot after V is the defect.
			t.Errorf("%s: %q follows the verb in %q", c.ID, tok, c.Pattern)
		}
	}
}

// TestJapaneseParticlesFollowTheirSlot checks that every case particle sits
// immediately after the slot it marks, which is what stops 「〜を同じです」 and
// 「〜を〜を比べます」.
func TestJapaneseParticlesFollowTheirSlot(t *testing.T) {
	for _, c := range builtinConstructions() {
		if c.Target != lang.JA {
			continue
		}
		toks := tokens(c.Pattern)
		for i, tok := range toks {
			if isSlotToken(tok) {
				continue
			}
			// A particle marks the slot to its LEFT.
			if i == 0 || !isSlotToken(toks[i-1]) {
				t.Errorf("%s: the particle %q in %q marks no slot", c.ID, tok, c.Pattern)
			}
		}
	}
}

// TestParticleComesFromThePatternNotTheProjection: the pattern owns the case
// particle. When the projection's を won instead, a frame stating と produced
// 「〜を同じです」.
func TestParticleComesFromThePatternNotTheProjection(t *testing.T) {
	// C.CMP.JA.03 is 「〜と同じ」: the object takes と, not the を the projection
	// assigned to a theme.
	for _, got := range jaOut(t, EventPlan{
		EventID: "e1", SenseID: "COMPARE.05", SenseFamily: "COMPARE",
		Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
		Construction: "C.CMP.JA.03", Constructions: []string{"C.CMP.JA.03"},
		Args: map[string]*NPPlan{
			jlir.RoleAgent:    {Noun: "Taro", Proper: true, Particle: Ga},
			jlir.RoleTheme:    {Noun: "book", Particle: Wo},
			jlir.RoleStimulus: {Noun: "Hanako", Proper: true, Particle: Wo},
		},
	}) {
		if strings.Contains(got, "を") {
			t.Errorf("%q carries a を the pattern does not state", got)
		}
		if !strings.Contains(got, "と同じ") {
			t.Errorf("%q is not 〜と同じ", got)
		}
	}
}

// TestHonorificFormsAreNotConcatenated covers the defect where every jpVerb
// honorific form was a dictionary form and ます was appended to it:
// 「という名前だおっしゃるます」.
func TestHonorificFormsAreNotConcatenated(t *testing.T) {
	for _, c := range builtinConstructions() {
		if c.Target != lang.JA {
			continue
		}
		for _, bad := range []string{"るます", "るました"} {
			for _, out := range jaOut(t, EventPlan{
				EventID: "e1", SenseID: firstSense(c), SenseFamily: familyOf(firstSense(c)),
				Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive, Politeness: 0.7,
				Construction: c.ID, Constructions: []string{c.ID},
				Args: map[string]*NPPlan{
					jlir.RoleAgent:   {Noun: "Taro", Proper: true},
					jlir.RolePatient: {Noun: "book"},
					jlir.RoleTheme:   {Noun: "book"},
				},
			}) {
				if strings.Contains(out, bad) {
					t.Errorf("%s realized %q, which contains the non-word %q", c.ID, out, bad)
				}
			}
		}
	}
}

func firstSense(c *Construction) string {
	for _, s := range c.Senses {
		return s
	}
	return ""
}

// TestNaruCompoundsConjugate checks ご覧になる, which used to be classified as
// an i-adjective and produced 「本をご覧になるです」.
func TestNaruCompoundsConjugate(t *testing.T) {
	for _, tc := range []struct{ plain, polite, past string }{
		{"ご覧になる", "ご覧になります", "ご覧になった"},
		{"ご存じになる", "ご存じになります", "ご存じになった"},
	} {
		v := lookupJPVerb(tc.plain, "")
		if v.cls != jpNaru {
			t.Fatalf("%s is classified %s, want %s", tc.plain, v.cls, jpNaru)
		}
		present := &EventPlan{Politeness: 0, Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive}
		if got := jpConjugate(v, present, false); got != tc.plain {
			t.Errorf("%s plain = %q, want %q", tc.plain, got, tc.plain)
		}
		polite := &EventPlan{Politeness: 0.7, Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive}
		if got := jpConjugate(v, polite, false); got != tc.polite {
			t.Errorf("%s polite = %q, want %q", tc.plain, got, tc.polite)
		}
		past := &EventPlan{Politeness: 0, Tense: jlir.TensePast, Polarity: jlir.PolarityPositive}
		if got := jpConjugate(v, past, false); got != tc.past {
			t.Errorf("%s past = %q, want %q", tc.plain, got, tc.past)
		}
	}
}

// TestCvcDoubles is the CVC rule that never fired: the guard required a
// consonant and then returned false on one, so pastTense("drop") was "droped".
func TestCvcDoubles(t *testing.T) {
	for _, tc := range []struct{ base, past, ing string }{
		{"drop", "dropped", "dropping"},
		{"grab", "grabbed", "grabbing"},
		{"nod", "nodded", "nodding"},
		{"plan", "planned", "planning"},
		{"chat", "chatted", "chatting"},
		{"beg", "begged", "begging"},
	} {
		if got := pastTense(tc.base); got != tc.past {
			t.Errorf("pastTense(%q) = %q, want %q", tc.base, got, tc.past)
		}
		if got := ingOfBase(tc.base); got != tc.ing {
			t.Errorf("ingOfBase(%q) = %q, want %q", tc.base, got, tc.ing)
		}
	}
	// The rule must not fire where English does not double.
	for _, tc := range []struct{ base, past, ing string }{
		{"visit", "visited", "visiting"},
		{"offer", "offered", "offering"},
		{"open", "opened", "opening"},
		{"play", "played", "playing"},
		{"saw", "sawed", "sawing"},
		{"fix", "fixed", "fixing"},
	} {
		if got := pastTense(tc.base); got != tc.past {
			t.Errorf("pastTense(%q) = %q, want %q", tc.base, got, tc.past)
		}
		if got := ingOfBase(tc.base); got != tc.ing {
			t.Errorf("ingOfBase(%q) = %q, want %q", tc.base, got, tc.ing)
		}
	}
}

// TestFPluralIsNotAVowelRule: a blanket -f → -ves rule corrupted most f-final
// nouns (roof→rooves, chief→chieves, belief→believes, knife→knifes).
func TestFPluralIsNotAVowelRule(t *testing.T) {
	for _, tc := range []struct{ sing, plur string }{
		{"roof", "roofs"}, {"chief", "chiefs"}, {"belief", "beliefs"},
		{"knife", "knives"}, {"proof", "proofs"}, {"safe", "safes"},
		{"wolf", "wolves"}, {"half", "halves"}, {"self", "selves"},
		{"shelf", "shelves"}, {"leaf", "leaves"}, {"wife", "wives"},
		{"life", "lives"}, {"man", "men"}, {"woman", "women"},
		{"person", "people"}, {"child", "children"}, {"book", "books"},
	} {
		if got := Pluralize(tc.sing, jlir.NumberPlural); got != tc.plur {
			t.Errorf("Pluralize(%q) = %q, want %q", tc.sing, got, tc.plur)
		}
	}
	if got := Pluralize("roof", jlir.NumberSingular); got != "roof" {
		t.Errorf("the singular was pluralized to %q", got)
	}
}

// TestBeAllowedToAgreesWithTheData: enSplit carried the inflected "is allowed
// to" while C.MAY.EN.02 declares "be allowed to", so the multiword lemma fell
// through and was inflected as one word.
func TestBeAllowedToAgreesWithTheData(t *testing.T) {
	got := enSplit("be allowed to")
	if got.aux != "be" || got.tail != " allowed to" {
		t.Fatalf("enSplit(%q) = %+v, want aux be and tail \" allowed to\"", "be allowed to", got)
	}
	c, ok := Default().Get("C.MAY.EN.02")
	if !ok {
		t.Fatal("C.MAY.EN.02 is missing")
	}
	if c.Lex != "be allowed to" {
		t.Fatalf("C.MAY.EN.02 declares %q", c.Lex)
	}
}

// TestUnknownMediumIsNotSpoken: plan.md §4 forbids reading an absent value as
// a positive claim. The medium feature is absent for almost every event, and
// the old code asserted "not written", which is "spoken".
func TestUnknownMediumIsNotSpoken(t *testing.T) {
	for _, tc := range []struct{ medium, want string }{
		{"", ""},
		{"written", "0"},
		{"spoken", "1"},
		{"oral", "1"},
	} {
		if got := oralFromMedium(tc.medium); got != tc.want {
			t.Errorf("oralFromMedium(%q) = %q, want %q", tc.medium, got, tc.want)
		}
	}
	// An unknown medium must buy no register preference.
	lib := Default()
	c, _ := lib.Get("C.GIVE.JA.02") // the casual-register frame
	if c == nil {
		t.Fatal("C.GIVE.JA.02 is missing")
	}
	unknown := lib.score(c, Context{Target: lang.JA, Oral: ""})
	spoken := lib.score(c, Context{Target: lang.JA, Oral: "1"})
	if unknown.Score >= spoken.Score {
		t.Errorf("an unknown medium scored %v, no less than the %v of a known spoken source",
			unknown.Score, spoken.Score)
	}
	if !strings.Contains(unknown.Why, "spoken register") &&
		!strings.Contains(spoken.Why, "spoken register") {
		t.Errorf("neither scoring recorded why: %q / %q", unknown.Why, spoken.Why)
	}
}

// TestRejectionLedgerHoldsOnlyHardRules: plan.md §37 forbids filing a frame
// that merely lost a scoring race alongside a frame that was illegal, because
// the UI sorts that ledger by the HARD./SOFT. rule prefix.
func TestRejectionLedgerHoldsOnlyHardRules(t *testing.T) {
	p := &Projection{Source: lang.EN, Target: lang.EN, Events: []EventPlan{{
		EventID: "e1", SenseID: "TRANSFER.01", SenseFamily: "TRANSFER",
		Tense: jlir.TensePast, Polarity: jlir.PolarityPositive,
		Construction: "C.RECV.JA.01", Constructions: []string{"C.RECV.JA.01"},
		Args: map[string]*NPPlan{
			jlir.RoleAgent:     {Noun: "Taro"},
			jlir.RoleTheme:     {Noun: "book"},
			jlir.RoleRecipient: {Noun: "Hanako"},
		},
	}}}
	// SoftDominance no longer exists: a ranking fact has no rule name, so it
	// cannot reach the hard ledger through Rejects at all. Inject the string a
	// pre-fix caller would have used to prove the ledger still filters it.
	p.Events[0].Rejects(RejectInfo{Slot: "e1", Lex: "X", Rule: "SOFT.dominated", Reason: "lost a race"})
	r := Request{Source: lang.EN, Target: lang.EN, Style: StyleProfile{Register: RegisterNeutral}}
	real := Realize(r, p)
	for _, rb := range real.Rejected {
		if !strings.HasPrefix(rb.Rule, HardRulePrefix) {
			t.Errorf("the rejection ledger holds a non-violation: %s %s", rb.Rule, rb.Reason)
		}
	}
}

// TestSentencePunctuationIsEmittedOnce covers 「あります。。」: the earlier clause
// emitted its own 。 because it believed a later clause would follow, and the
// later clause then failed to realize.
func TestSentencePunctuationIsEmittedOnce(t *testing.T) {
	got := jaOut(t,
		EventPlan{
			EventID: "e1", SenseID: "EXIST.01", SenseFamily: "EXIST",
			Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
			Construction: "C.BE.JA.01", Constructions: []string{"C.BE.JA.01"},
			Args: map[string]*NPPlan{jlir.RolePatient: {Noun: "weather"}},
		},
		EventPlan{
			// A clause with nothing to say produces no frame at all.
			EventID: "e2", SenseID: "WEATHER.99", SenseFamily: "WEATHER",
			Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
			Construction: "C.FALLBACK.JA.01", Constructions: []string{"C.FALLBACK.JA.01"},
			Args: map[string]*NPPlan{jlir.RoleTheme: {Noun: "rain", Omit: true}},
		})
	if len(got) == 0 {
		t.Fatal("nothing was realized")
	}
	for _, s := range got {
		if strings.Contains(s, "。。") {
			t.Errorf("%q ends the sentence twice", s)
		}
	}
}

// TestQuotedClauseKeepsItsAlternatives: Japanese quoting used to realize the
// embedded clause into a throwaway forest and keep trees[0].Text(), which left
// exactly one candidate for the whole sentence.
func TestQuotedClauseKeepsItsAlternatives(t *testing.T) {
	// Japanese nouns, because the target is Japanese: a quoted clause is
	// realized as a subtree of the same forest, and its own constructions
	// multiply the whole sentence's candidate set.
	sub := EventPlan{
		EventID: "e2", SenseID: "TRANSFER.01", SenseFamily: "TRANSFER",
		Tense: jlir.TensePast, Polarity: jlir.PolarityPositive,
		Construction: "C.GIVE.JA.01", Constructions: []string{"C.GIVE.JA.01", "C.GIVE.JA.02"},
		Args: map[string]*NPPlan{
			jlir.RoleAgent:     {Noun: "太郎", Proper: true},
			jlir.RoleTheme:     {Noun: "本"},
			jlir.RoleRecipient: {Noun: "花子", Proper: true},
		},
	}
	got := jaOut(t, EventPlan{
		EventID: "e1", SenseID: "MENTAL.01", SenseFamily: "MENTAL",
		Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
		Construction: "C.THINK.JA.01", Constructions: []string{"C.THINK.JA.01"},
		Args: map[string]*NPPlan{
			jlir.RoleExperiencer: {Noun: "田中", Proper: true},
			jlir.RoleTheme:       {IsClause: true, Clause: &sub},
		},
	})
	if len(got) < 2 {
		t.Fatalf("a quoted clause collapsed the candidate set to %d", len(got))
	}
	sawHandover := false
	for _, s := range got {
		if strings.Contains(s, "渡した") && strings.Contains(s, "と思う") {
			sawHandover = true
		}
		if strings.Contains(s, "。") && !strings.HasSuffix(s, "。") {
			t.Errorf("the quoted clause swallowed the outer sentence's punctuation: %q", s)
		}
	}
	if !sawHandover {
		t.Errorf("no candidate realizes the quoted hand-over: %v", got)
	}
}

// TestNoJapaneseCandidateOrphansItsParticle is the acceptance criterion: no
// Japanese construction may realize with a particle or a TAIL in the wrong
// position.
func TestNoJapaneseCandidateOrphansItsParticle(t *testing.T) {
	probe := []struct {
		sense  string
		events map[string]*NPPlan
	}{
		{"MODAL.01", map[string]*NPPlan{jlir.RoleAgent: {Noun: "Taro", Proper: true},
			jlir.RoleTheme: {IsClause: true, Clause: &EventPlan{
				EventID: "e3", SenseID: "PERCEIVE.01", SenseFamily: "PERCEIVE",
				Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
				Construction: "C.READ.JA.01", Constructions: []string{"C.READ.JA.01"},
				Args: map[string]*NPPlan{jlir.RoleAgent: {Noun: "Taro", Proper: true},
					jlir.RolePatient: {Noun: "book"}}}}}},
		{"MODAL.02", map[string]*NPPlan{jlir.RoleAgent: {Noun: "Taro", Proper: true},
			jlir.RoleTheme: {IsClause: true, Clause: &EventPlan{
				EventID: "e3", SenseID: "CONSUME.01", SenseFamily: "CONSUME",
				Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
				Construction: "C.EAT.JA.01", Constructions: []string{"C.EAT.JA.01"},
				Args: map[string]*NPPlan{jlir.RoleAgent: {Noun: "Taro", Proper: true},
					jlir.RolePatient: {Noun: "rice"}}}}}},
		{"MENTAL.09", map[string]*NPPlan{jlir.RoleAgent: {Noun: "Taro", Proper: true},
			jlir.RoleTheme: {IsClause: true, Clause: &EventPlan{
				EventID: "e3", SenseID: "PERCEIVE.01", SenseFamily: "PERCEIVE",
				Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
				Construction: "C.READ.JA.01", Constructions: []string{"C.READ.JA.01"},
				Args: map[string]*NPPlan{jlir.RoleAgent: {Noun: "Taro", Proper: true},
					jlir.RolePatient: {Noun: "book"}}}}}},
	}
	want := map[string]string{
		"MODAL.01":  "読まなければならない",
		"MODAL.02":  "るべきだ",
		"MENTAL.09": "つもりだ",
	}
	for _, tc := range probe {
		var texts []string
		for _, c := range Default().forSense(tc.sense, lang.JA) {
			texts = append(texts, jaOut(t, EventPlan{
				EventID: "e1", SenseID: tc.sense, SenseFamily: familyOf(tc.sense),
				Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
				Construction: c.ID, Constructions: []string{c.ID},
				Args: tc.events,
			})...)
		}
		if len(texts) == 0 {
			t.Errorf("%s realized nothing", tc.sense)
			continue
		}
		ok := false
		for _, s := range texts {
			if strings.Contains(s, want[tc.sense]) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s never produced %q: %v", tc.sense, want[tc.sense], texts)
		}
	}
}

// TestChainFramesTakeTheThemeVerb checks the thematic-verb chain: the ending
// continues the theme's verb rather than the family's, which produced
// 「をつもりだ思います」.
func TestChainFramesTakeTheThemeVerb(t *testing.T) {
	read := EventPlan{
		EventID: "e3", SenseID: "PERCEIVE.01", SenseFamily: "PERCEIVE",
		Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
		Construction: "C.READ.JA.01", Constructions: []string{"C.READ.JA.01"},
		Args: map[string]*NPPlan{jlir.RoleAgent: {Noun: "太郎", Proper: true},
			jlir.RolePatient: {Noun: "本"}},
	}
	got := jaOut(t, EventPlan{
		EventID: "e1", SenseID: "MODAL.01", SenseFamily: "MODAL",
		Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
		Construction: "C.MUST.JA.01", Constructions: []string{"C.MUST.JA.01"},
		Args: map[string]*NPPlan{jlir.RoleAgent: {Noun: "太郎", Proper: true},
			jlir.RoleTheme: {IsClause: true, Clause: &read}},
	})
	want := "太郎が本を読まなければならない"
	found := false
	for _, s := range got {
		if strings.Contains(s, want) {
			found = true
		}
	}
	if !found {
		t.Errorf("wanted %q, got %v", want, got)
	}
}

// TestTaiAttachesToTheRenyoukei: 食べたい comes from 食, 飲みたい from 飲,
// 待ちたい from 待っ. Neither the te-form nor the masu stem is right.
func TestTaiAttachesToTheRenyoukei(t *testing.T) {
	for _, tc := range []struct{ dic, want string }{
		{"食べる", "食べ"}, {"飲む", "飲み"}, {"書く", "書い"},
		{"泳ぐ", "泳ぎ"}, {"話す", "話し"}, {"待つ", "待っ"},
		{"死ぬ", "死に"}, {"飛ぶ", "飛び"}, {"作る", "作り"}, {"買う", "買い"},
	} {
		if got := lookupJPVerb(tc.dic, "").taiStem(); got != tc.want {
			t.Errorf("taiStem(%q) = %q, want %q", tc.dic, got, tc.want)
		}
	}
}

// TestGodanPotentialAttachesToTheStem: 読める, not 読まえる.
func TestGodanPotentialAttachesToTheStem(t *testing.T) {
	for _, tc := range []struct{ dic, want string }{
		{"読む", "読める"}, {"書く", "書ける"}, {"泳ぐ", "泳げる"},
		{"話す", "話せる"}, {"待つ", "待てる"}, {"死ぬ", "死ねる"},
	} {
		if got := lookupJPVerb(tc.dic, "").potential(); got != tc.want {
			t.Errorf("potential(%q) = %q, want %q", tc.dic, got, tc.want)
		}
	}
}
