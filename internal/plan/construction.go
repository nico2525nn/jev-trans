package plan

// construction.go: the construction library of plan.md §26.
//
// Generation is centred on constructions, not on words:
//
//	INTEND(agent, event) -> "X intends to Y" / "X means to Y" / "X plans to Y"
//	                    -> 「XはYするつもりだ」「XはYしようと思っている」
//
// Each construction carries the semantic and syntactic requirements it has,
// its register, a naturalness prior, and the discourse conditions under which
// it is preferred. Selection is a scored filter, not an argmax over a bag of
// strings: a construction whose requirements the event does not satisfy is
// *rejected* (a hard constraint of plan.md §37) and the rejection is recorded
// so the UI can explain why a fluent-looking option never appeared.
//
// Pattern syntax (identical shape for both languages):
//
//	English   uppercase = a slot, lowercase = a preposition binding the next slot
//	Japanese  uppercase = a slot, lowercase = a connective particle binding the
//	          next slot
//
// Slots are SUBJ (the construction's subject role), V (the predicate), OBJ (the
// direct object) or a semantic role name from jlir.AllRoles.

import (
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/trace"
)

// Construction is one realizable frame for a predicate sense in one language.
type Construction struct {
	ID string `json:"id"`
	// Target is the language this construction produces.
	Target lang.Lang `json:"target"`
	// Senses lists the predicate sense ids (or families) it realizes.
	Senses []string `json:"senses"`
	// Pattern is the ordered frame, e.g. "SUBJ V OBJ to RECIPIENT".
	Pattern string `json:"pattern"`
	// Register is the register the construction belongs to.
	Register string `json:"register,omitempty"`
	// Naturalness is the prior for this construction as an English or Japanese
	// way of putting the sense, 0..1.
	Naturalness float64 `json:"naturalness"`
	// Requires lists features of the discourse/event the construction needs.
	Requires map[string]string `json:"requires,omitempty"`
	// Forbidden lists features that make the construction unusable.
	Forbidden map[string]string `json:"forbidden,omitempty"`
	// Notes is the human readable explanation shown in the UI.
	Notes string `json:"notes,omitempty"`

	// --- realization directives ------------------------------------------

	// Family is the construction family this belongs to; the verb lexicon of
	// the realizer is keyed by family, so several constructions can share one
	// predicate while differing in frame, register or discourse condition.
	Family string `json:"family,omitempty"`
	// Lex is the predicate of this construction in the target language. Empty
	// means "look the family up in the verb lexicon".
	Lex string `json:"lex,omitempty"`
	// Join is the word between the verb and a clausal argument: "to" for
	// control, "that" for a full clause complement. It is what makes
	// "intends to" possible without a dictionary entry for the whole phrase.
	Join string `json:"join,omitempty"`
	// Tail is a string appended after the predicate: Japanese つもりだ,
	// わけではない, English "or something".
	Tail string `json:"tail,omitempty"`
	// Lex2 is the predicate of the second V slot, used by Japanese frames that
	// put the object's case particle between two pieces of the verb, such as
	// 〜を〜なければならない.
	Lex2 string `json:"lex2,omitempty"`
	// Chain selects the thematic-verb stem a Japanese predicate continues from:
	// "nai" (negative stem), "nai_n" (negative stem + な), "masu" (連用形),
	// "te" (て-form), "tai" or "potential". It is what turns "must eat" into
	// 食べなければならない without a dictionary entry for the whole phrase.
	Chain string `json:"chain,omitempty"`
	// Complement describes how a clausal argument is attached:
	// "to"   control, the sub-clause has no subject (want to go)
	// "that" full clause complement (know that he said)
	// "quot" Japanese quoted clause before the verb (〜と思う)
	Complement string `json:"complement,omitempty"`
	// SubjRole overrides which role realizes as the grammatical subject, which
	// Japanese needs for 在る ("there is") and English for a passive.
	SubjRole string `json:"subjRole,omitempty"`
	// Prep overrides the preposition of a role in English.
	Prep map[string]string `json:"prep,omitempty"`
	// Case overrides the Japanese case particle of a role.
	Case map[string]string `json:"case,omitempty"`
	// TopicMark pins the Japanese subject marker ("wa"/"ga"); empty leaves the
	// decision to the projection.
	TopicMark string `json:"topicMark,omitempty"`
	// Polarity records the Japanese ending the construction prefers:
	// "masu", "da" or "nominal".
	Polarity string `json:"polarity,omitempty"`
	// Honorific marks the honorific variant of a predicate.
	Honorific bool `json:"honorific,omitempty"`
}

// RejectInfo is one hard-constraint rejection discovered during projection. It
// is carried on the event plan so the realizer can replay it into the
// forest.Realization ledger.
type RejectInfo struct {
	Slot   string `json:"slot"`
	Lex    string `json:"lex"`
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
	Stage  string `json:"stage,omitempty"`
}

// Rejects accumulates construction rejections on an event plan.
func (e *EventPlan) Rejects(info ...RejectInfo) {
	if e == nil {
		return
	}
	e.rejects = append(e.rejects, info...)
}

// Rejected returns the construction rejections of the plan.
func (e *EventPlan) Rejected() []RejectInfo {
	if e == nil {
		return nil
	}
	return e.rejects
}

// Context is the discourse state a construction is selected against. It is
// deliberately small: everything in it is either given by the JLIR or decided
// by the projection, never re-derived here.
type Context struct {
	Sense    string
	Family   string
	Target   lang.Lang
	Event    jlir.ID
	Definite map[string]bool
	Number   map[string]string
	Tense    string
	Polarity string
	Modality string
	Mood     string
	Aspect   string
	Register string
	// Politeness is 0..1; Honorific is the derived binary.
	Politeness float64
	Honorific  bool
	// Direction is "toward"/"away" for motion predicates, "" otherwise.
	Direction string
	// Act is the speech act of a communication predicate ("question",
	// "request", "statement"), used by the Requires keys.
	Act string
	// Oral marks a spoken (non written) source.
	Oral bool
	// Features carries every event feature by key, so that an ontology
	// constraint such as ingestible=solid is visible to the Requires filter
	// without this package needing a case per constraint.
	Features map[string]string
	// Known reports whether the sense is in the construction library at all.
	Known bool
	// Args lists the filled roles, in realization order.
	Args []string
}

// Has reports whether role is filled in the event.
func (c Context) Has(role string) bool {
	for _, r := range c.Args {
		if r == role {
			return true
		}
	}
	return false
}

// IsDefinite reports whether the role's referent is discourse-established.
func (c Context) IsDefinite(role string) bool { return c.Definite[role] }

// Scored is one construction with the verdict of the hard/soft split.
type Scored struct {
	C *Construction
	// Score is the soft score: naturalness, register match, discourse fit.
	Score float64
	// Why explains the score for the UI.
	Why string
	// Rejected marks a construction killed by a hard constraint; Rule says
	// which one. Rejected entries are returned by Select with a zero score so
	// the ledger can be filled without a second lookup.
	Rejected bool
	Rule     string
	Reason   string
}

// Library is the construction inventory.
type Library struct {
	items []*Construction
	byID  map[string]*Construction
	byFam map[string][]*Construction // family|target -> constructions
}

// Default returns the built-in construction library.
func Default() *Library {
	l := &Library{byID: map[string]*Construction{}, byFam: map[string][]*Construction{}}
	for _, c := range builtinConstructions() {
		if c == nil || c.ID == "" {
			continue
		}
		l.add(c)
	}
	return l
}

func (l *Library) add(c *Construction) {
	if _, dup := l.byID[c.ID]; dup {
		return
	}
	// The id encodes the target (C.GIVE.EN.01). An id whose language segment
	// disagrees with the construction's target is a data error, and using it
	// would put one language's morphemes inside the other language's sentence.
	if !strings.Contains(strings.ToUpper(c.ID), "."+strings.ToUpper(string(c.Target))+".") {
		return
	}
	l.items = append(l.items, c)
	l.byID[c.ID] = c
	key := c.familyKey()
	l.byFam[key] = append(l.byFam[key], c)
	sort.SliceStable(l.byFam[key], func(i, j int) bool { return l.byFam[key][i].ID < l.byFam[key][j].ID })
}

func (c *Construction) familyKey() string {
	f := c.Family
	if f == "" {
		f = familyOf(c.Senses[0])
	}
	return f + "|" + string(c.Target)
}

// All returns every construction in deterministic order.
func (l *Library) All() []*Construction {
	out := append([]*Construction(nil), l.items...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get looks a construction up by id.
func (l *Library) Get(id string) (*Construction, bool) {
	if l == nil {
		return nil, false
	}
	c, ok := l.byID[id]
	return c, ok
}

// Len reports the size of the library.
func (l *Library) Len() int { return len(l.items) }

// Select returns every construction that can realize sense in target, scored
// against ctx. Surviving constructions come first, ordered by score; rejected
// ones follow with Rejected set, so the caller can fill the rejection ledger
// without knowing which rule killed what.
func (l *Library) Select(sense string, target lang.Lang, ctx Context) []Scored {
	fams := candidateFamilies(sense)
	ctx.Known = len(fams) > 0
	ctx.Target = target
	if ctx.Family == "" {
		ctx.Family = familyOf(sense)
	}
	var out []Scored
	seen := map[string]bool{}
	for _, f := range fams {
		for _, c := range l.byFam[f+"|"+string(target)] {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			out = append(out, l.score(c, ctx))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rejected != out[j].Rejected {
			return !out[i].Rejected
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].C.ID < out[j].C.ID
	})
	return out
}

// score applies the hard constraints first (a failure rejects) and only then
// the soft ones. This ordering is the whole point of plan.md §37.
func (l *Library) score(c *Construction, ctx Context) Scored {
	s := Scored{C: c}
	for _, k := range sortedKeys(c.Requires) {
		got, ok := ctxFeature(ctx, k)
		if !ok || !strings.EqualFold(got, c.Requires[k]) {
			s.Rejected = true
			s.Rule = HardConstRequires
			s.Reason = "construction requires " + k + "=" + c.Requires[k] + " but the discourse does not provide it"
			return s
		}
	}
	for _, k := range sortedKeys(c.Forbidden) {
		if got, ok := ctxFeature(ctx, k); ok && strings.EqualFold(got, c.Forbidden[k]) {
			s.Rejected = true
			s.Rule = HardConstForbidden
			s.Reason = "construction forbids " + k + "=" + c.Forbidden[k] + ", which the discourse provides"
			return s
		}
	}
	// Soft criteria only.
	s.Score = c.Naturalness
	var why []string
	why = append(why, "naturalness "+sprintf("%.2f", c.Naturalness))
	s.Score += registerFit(c.Register, ctx.Register)
	if c.Register == ctx.Register {
		why = append(why, "register match")
	}
	if ctx.Target == lang.JA {
		if c.Honorific == ctx.Honorific && (c.Honorific || ctx.Honorific) {
			s.Score += 0.12
			why = append(why, "honorific fit")
		}
		if c.Polarity == "masu" && ctx.Politeness >= 0.5 {
			s.Score += 0.08
			why = append(why, "polite ending")
		}
		if c.Polarity == "da" && ctx.Politeness < 0.5 {
			s.Score += 0.08
			why = append(why, "plain ending")
		}
	}
	if c.Complement != "" && ctx.Has(jlir.RoleTheme) {
		s.Score += 0.06
		why = append(why, "clausal argument present")
	}
	if ctx.Direction != "" && strings.Contains(strings.ToLower(c.Notes), ctx.Direction) {
		s.Score += 0.05
		why = append(why, "matches motion direction")
	}
	if ctx.Oral && c.Register == RegisterCasual {
		s.Score += 0.05
		why = append(why, "spoken register")
	}
	if !ctx.Oral && c.Register == RegisterFormal {
		s.Score += 0.05
		why = append(why, "written register")
	}
	s.Why = strings.Join(why, ", ")
	return s
}

// registerFit scores register agreement as a soft preference.
func registerFit(a, b string) float64 {
	if a == "" {
		a = RegisterNeutral
	}
	if b == "" {
		b = RegisterNeutral
	}
	if a == b {
		return 0.14
	}
	if (a == RegisterNeutral) != (b == RegisterNeutral) {
		return 0
	}
	return -0.2
}

// ctxFeature resolves the Requires/Forbidden keys against the context.
func ctxFeature(ctx Context, key string) (string, bool) {
	switch key {
	case "honorific":
		if ctx.Honorific {
			return "1", true
		}
		return "0", true
	case "politeness":
		switch {
		case ctx.Politeness >= 0.75:
			return "high", true
		case ctx.Politeness >= 0.5:
			return "polite", true
		case ctx.Politeness >= 0.25:
			return "neutral", true
		}
		return "plain", true
	case "register":
		return ctx.Register, ctx.Register != ""
	case "tense":
		return ctx.Tense, ctx.Tense != ""
	case "modality":
		return ctx.Modality, ctx.Modality != ""
	case "polarity":
		return ctx.Polarity, ctx.Polarity != ""
	case "mood":
		return ctx.Mood, ctx.Mood != ""
	case "aspect":
		return ctx.Aspect, ctx.Aspect != ""
	case "direction":
		return ctx.Direction, ctx.Direction != ""
	case "oral":
		if ctx.Oral {
			return "1", true
		}
		return "0", true
	case "recipient_defined":
		if ctx.IsDefinite(jlir.RoleRecipient) {
			return "1", true
		}
		return "0", true
	case "source_defined":
		if ctx.IsDefinite(jlir.RoleSource) {
			return "1", true
		}
		return "0", true
	case "speech_act":
		return ctx.Act, ctx.Act != ""
	}
	// Anything the event carries is available as a constraint key.
	if v, ok := ctx.Features[key]; ok {
		return v, true
	}
	return "", false
}

// --- family resolution ----------------------------------------------------

// familyAliases maps an ontology family onto the construction families that
// can realize it. The ontology agent names senses; this table keeps the two
// vocabularies from drifting.
var familyAliases = map[string][]string{
	"GIVE":     {"GIVE"},
	"GIVING":   {"GIVE"},
	"HANDOFF":  {"GIVE"},
	"DELIVER":  {"GIVE"},
	"TRANSFER": {"GIVE", "RECEIVE"},
	"RECEIVE":  {"RECEIVE"},
	// PERCEIVE is the ontology family for seeing, watching and noticing; the
	// construction library files those under SEE. Without this alias a vision
	// predicate falls through to the generic frame and loses its verb entirely.
	"PERCEIVE":      {"SEE", "HEAR"},
	"VISION":        {"SEE"},
	"AUDITION":      {"HEAR"},
	"GET":           {"RECEIVE", "TAKE"},
	"TAKE":          {"TAKE"},
	"ACQUIRE":       {"TAKE"},
	"COMMUNICATION": {"SAY", "TELL", "ASK", "ANSWER", "SPEAK"},
	"COMMUNICATE":   {"SAY", "TELL", "ASK", "ANSWER", "SPEAK"},
	"SAY":           {"SAY"},
	"STATE":         {"SAY"},
	"TELL":          {"TELL"},
	"ASK":           {"ASK"},
	"ANSWER":        {"ANSWER"},
	"SPEAK":         {"SPEAK"},
	"TALK":          {"SPEAK", "SAY"},
	"MOTION":        {"MOVE", "ARRIVE", "LEAVE"},
	"MOVE":          {"MOVE"},
	"GO":            {"MOVE", "ARRIVE"},
	"TRAVEL":        {"MOVE"},
	"APPROACH":      {"MOVE"},
	"ARRIVE":        {"ARRIVE"},
	"LEAVE":         {"LEAVE"},
	"DEPART":        {"LEAVE"},
	"MEET":          {"MEET"},
	"VISIT":         {"MEET"},
	"CONTACT":       {"MEET"},
	"WANT":          {"WANT"},
	"DESIRE":        {"WANT"},
	"INTEND":        {"INTEND"},
	"PLAN":          {"INTEND"},
	"DECIDE":        {"INTEND"},
	"CHOOSE":        {"INTEND"},
	"HOPE":          {"INTEND"},
	"BE":            {"BE", "NAMED", "EXIST", "COPULA"},
	"BECOME":        {"BECOME"},
	"EXIST":         {"EXIST"},
	"NAMED":         {"NAMED"},
	"IDENTITY":      {"NAMED", "BE"},
	"CALLED":        {"NAMED"},
	"HAVE":          {"HAVE"},
	"POSSESS":       {"HAVE"},
	"OWN":           {"HAVE"},
	"CAN":           {"ABLE"},
	"ABLE":          {"ABLE"},
	"OBLIG":         {"MUST"},
	"OBLIGATION":    {"MUST"},
	"MUST":          {"MUST"},
	"REQUIREMENT":   {"MUST"},
	"SHOULD":        {"SHOULD"},
	"PERMISSION":    {"MAY"},
	"ALLOW":         {"MAY"},
	"PERMIT":        {"MAY"},
	"EAT":           {"EAT"},
	"DRINK":         {"DRINK"},
	"CONSUME":       {"EAT", "DRINK"},
	"MEAL":          {"EAT"},
	"PERCEPTION":    {"SEE", "HEAR"},
	"SEE":           {"SEE"},
	"LOOK":          {"SEE"},
	"WATCH":         {"SEE"},
	"OBSERVE":       {"SEE"},
	"HEAR":          {"HEAR"},
	"LISTEN":        {"HEAR"},
	"KNOW":          {"KNOW"},
	"THINK":         {"THINK"},
	"BELIEVE":       {"BELIEVE"},
	"GUESS":         {"THINK"},
	"UNDERSTAND":    {"KNOW"},
	"CONSIDER":      {"THINK"},
	"MENTAL":        {"KNOW", "THINK", "BELIEVE", "WANT"},
	"COMPARE":       {"COMPARE"},
	"COMPARISON":    {"COMPARE"},
	"COPULA":        {"COPULA"},
	"LIKE":          {"LIKE"},
	"LOVE":          {"LIKE"},
	"SHOW":          {"SHOW"},
	"READ":          {"READ"},
	"WRITE":         {"WRITE"},
	"SLEEP":         {"SLEEP"},
	"LIVE":          {"LIVE"},
	"WORK":          {"WORK"},
	"OPEN":          {"OPEN"},
	"CLOSE":         {"CLOSE"},
	"BEGIN":         {"START"},
	"START":         {"START"},
	"STOP":          {"STOP"},
	"SEND":          {"SEND"},
	"BUY":           {"BUY"},
	"SELL":          {"BUY"},
	"RUN":           {"MOVE"},
	"WALK":          {"MOVE"},
	"TIME":          {"BE"},
}

// familyStems is the substring fallback used when an ontology family is not in
// familyAliases, keyed by a stem that is at least three characters long so that
// "BE" cannot swallow "BELIEVE".
var familyStems = map[string][]string{
	"GIV": {"GIVE"}, "TRANS": {"GIVE", "RECEIVE"}, "RECEIV": {"RECEIVE"},
	"HAND": {"GIVE"}, "DELIV": {"GIVE"},
	"SAY": {"SAY"}, "STAT": {"SAY"}, "TELL": {"TELL"}, "ASK": {"ASK"},
	"ANSW": {"ANSWER"}, "SPEAK": {"SPEAK"}, "TALK": {"SPEAK", "SAY"},
	"COMMUNIC": {"SAY", "TELL", "ASK", "ANSWER", "SPEAK"},
	"MOVE":     {"MOVE"}, "GO": {"MOVE", "ARRIVE"}, "TRAV": {"MOVE"},
	"APPROACH": {"MOVE"}, "ARRIV": {"ARRIVE"}, "LEAV": {"LEAVE"}, "DEPART": {"LEAVE"},
	"MEET": {"MEET"}, "VISIT": {"MEET"}, "CONTACT": {"MEET"},
	"WANT": {"WANT"}, "DESIR": {"WANT"}, "INTEND": {"INTEND"}, "PLAN": {"INTEND"},
	"DECID": {"INTEND"}, "CHOOS": {"INTEND"}, "HOPE": {"INTEND"},
	"BECOM": {"BECOME"}, "EXIST": {"EXIST"}, "NAM": {"NAMED"}, "CALL": {"NAMED"},
	"HAVE": {"HAVE"}, "POSSESS": {"HAVE"}, "OWN": {"HAVE"},
	"CAN": {"ABLE"}, "ABLE": {"ABLE"}, "OBLIG": {"MUST"}, "MUST": {"MUST"},
	"REQUIRE": {"MUST"}, "SHOULD": {"SHOULD"}, "PERMIS": {"MAY"}, "ALLOW": {"MAY"},
	"EAT": {"EAT"}, "DRINK": {"DRINK"}, "CONSUM": {"EAT", "DRINK"},
	"SEE": {"SEE"}, "LOOK": {"SEE"}, "WATCH": {"SEE"}, "OBSERV": {"SEE"},
	"HEAR": {"HEAR"}, "LISTEN": {"HEAR"},
	"KNOW": {"KNOW"}, "THINK": {"THINK"}, "BELIE": {"BELIEVE"}, "GUESS": {"THINK"},
	"UNDERSTAND": {"KNOW"}, "CONSIDER": {"THINK"},
	"COMPAR": {"COMPARE"}, "COPULA": {"COPULA"}, "COPUL": {"COPULA"},
	"LIKE": {"LIKE"}, "LOVE": {"LIKE"}, "SHOW": {"SHOW"},
	"READ": {"READ"}, "WRITE": {"WRITE"}, "SLEEP": {"SLEEP"}, "LIVE": {"LIVE"},
	"WORK": {"WORK"}, "OPEN": {"OPEN"}, "CLOSE": {"CLOSE"},
	"BEGIN": {"START"}, "START": {"START"}, "STOP": {"STOP"}, "SEND": {"SEND"},
	"BUY": {"BUY"}, "SELL": {"BUY"},
}

// candidateFamilies resolves a sense id (or bare family) to construction
// families, longest stem first, deterministically.
func candidateFamilies(sense string) []string {
	fam := familyOf(sense)
	if fam == "" {
		return nil
	}
	if v, ok := familyAliases[fam]; ok {
		return append([]string(nil), v...)
	}
	best := ""
	for stem := range familyStems {
		if len(stem) < 3 {
			continue
		}
		if strings.HasPrefix(fam, stem) || strings.HasPrefix(stem, fam) {
			if len(stem) > len(best) {
				best = stem
			}
		}
	}
	if best == "" {
		return nil
	}
	return append([]string(nil), familyStems[best]...)
}

// --- selection ------------------------------------------------------------

// maxConstructions bounds the alternatives kept for the realizer. Three keeps
// a genuinely variable frame while stopping the forest from exploding.
const maxConstructions = 3

// selectConstructions is the projection's construction step. It fills
// EventPlan.Construction with the winner and EventPlan.Constructions with the
// live alternatives, and records every rejected construction on the plan.
func selectConstructions(r Request, p *Projection, ep *EventPlan, ev *jlir.Event, target lang.Lang, depth int) {
	lib := Default()
	ctx := constructionContext(r, p, ep, ev, target)
	scored := lib.Select(ev.Predicate, target, ctx)

	var live []Scored
	for _, s := range scored {
		if s.Rejected {
			ep.Rejects(RejectInfo{Slot: string(ev.ID), Lex: s.C.ID, Rule: s.Rule, Reason: s.Reason, Stage: "construction"})
			continue
		}
		live = append(live, s)
	}
	if len(live) == 0 {
		// plan.md §25: an unknown construction is kept, not invented. The
		// fallback frame carries the sense name so nothing is fabricated and
		// the loss is visible.
		ep.Rejects(RejectInfo{Slot: string(ev.ID), Lex: ev.Predicate, Rule: HardUnknownSense,
			Reason: "no construction realizes " + ev.Predicate + " in " + string(target) + "; the generic frame is used", Stage: "construction"})
		loss(p, DimPropositional, "unknown_construction", 0.6, ev.ID, "", "no construction registered for sense %s", ev.Predicate)
		fb := fallbackConstruction(target, ctx)
		ep.Construction = fb.ID
		ep.Constructions = []string{fb.ID}
		ep.Note("fallback frame: " + fb.Notes)
		return
	}
	if len(live) > maxConstructions {
		for _, s := range live[maxConstructions:] {
			ep.Rejects(RejectInfo{Slot: string(ev.ID), Lex: s.C.ID, Rule: SoftDominance,
				Reason: sprintf("lower ranked than the %d better constructions", maxConstructions), Stage: "construction"})
		}
		live = live[:maxConstructions]
	}

	weights := map[string]float64{}
	for _, s := range live {
		w := s.Score
		if w <= 0.001 {
			w = 0.001
		}
		weights[s.C.ID] = w
	}
	question := "which construction realizes " + ev.Predicate + " as " + targetLabel(target) + "?"
	if target == lang.JA && ctx.Honorific {
		question = "which construction realizes " + ev.Predicate + " in polite Japanese with honorific address?"
	}
	chosen, _ := r.decide(DecideConstruction, string(ev.ID), question, weights, p)
	if chosen == "" {
		chosen = live[0].C.ID
	}
	ep.Construction = chosen
	ep.Constructions = nil
	// Keep the chosen construction first so the forest's prior ordering
	// follows the decision rather than the alphabetical id order.
	ep.Constructions = append(ep.Constructions, chosen)
	for _, s := range live {
		if s.C.ID != chosen {
			ep.Constructions = append(ep.Constructions, s.C.ID)
		}
	}
	ep.Note("construction " + chosen)
	traceSelection(r.recorder(), ev.ID, chosen, live)
}

// constructionContext builds the selection context from the event and the
// decisions the projection has already taken.
func constructionContext(r Request, p *Projection, ep *EventPlan, ev *jlir.Event, target lang.Lang) Context {
	ctx := Context{
		Sense:      ev.Predicate,
		Family:     ep.SenseFamily,
		Target:     target,
		Event:      ev.ID,
		Definite:   map[string]bool{},
		Number:     map[string]string{},
		Tense:      ep.Tense,
		Polarity:   ep.Polarity,
		Modality:   ep.Modality,
		Mood:       ep.Mood,
		Aspect:     ep.Aspect,
		Register:   r.Style.Normalized().Register,
		Politeness: r.Style.Normalized().Politeness,
		Honorific:  p != nil && p.Honorific,
		Direction:  strings.ToLower(evFeature(ev, "direction")),
		Oral:       !strings.Contains(evFeature(ev, "medium"), "writ"),
		Args:       ev.OrderArgs(),
	}
	if f := evFeature(ev, "speech_act"); f != "" {
		ctx.Act = f
	}
	// Every event feature becomes a construction constraint key. Adding a
	// constraint to the ontology must not also mean teaching ctxFeature a new
	// case, or the constraint silently rejects every construction that states
	// it — which looks exactly like the predicate having no realization.
	ctx.Features = map[string]string{}
	for _, f := range ev.Features {
		if f.Key != "" {
			ctx.Features[f.Key] = jlir.ValueString(f.Value)
		}
	}
	for _, role := range ctx.Args {
		np := ep.Args[role]
		if np == nil {
			continue
		}
		ctx.Definite[role] = np.Given || np.Proper || np.IsZero
		ctx.Number[role] = np.Number
	}
	return ctx
}

// fallbackConstruction is the frame used for a sense the library does not know.
// It asserts nothing: the verb slot carries the sense name so the gap is
// visible in the candidate instead of being papered over with a guess.
func fallbackConstruction(target lang.Lang, ctx Context) *Construction {
	c := &Construction{
		ID:          "C.FALLBACK." + strings.ToUpper(string(target)) + ".01",
		Target:      target,
		Senses:      []string{ctx.Sense},
		Pattern:     "SUBJ V OBJ",
		Family:      "UNKNOWN",
		Register:    RegisterNeutral,
		Naturalness: 0.1,
		Notes:       "generic frame for an unregistered sense; the predicate slot carries the sense name",
	}
	if target == lang.EN {
		c.Lex = strings.ToLower(ctx.Family)
	}
	return c
}

// targetLabel names a language for question text.
func targetLabel(l lang.Lang) string {
	if l == lang.JA {
		return "Japanese"
	}
	return "English"
}

// traceSelection writes a construction selection span. Kept here so both
// projections record the same artifact shape.
func traceSelection(rec *trace.Recorder, evID jlir.ID, chosen string, live []Scored) {
	if rec == nil {
		return
	}
	span := rec.Open(trace.StageConstruct, "construction selection "+string(evID))
	defer span.Close()
	rows := make([]map[string]any, 0, len(live))
	for _, s := range live {
		rows = append(rows, map[string]any{"id": s.C.ID, "score": s.Score, "why": s.Why, "pattern": s.C.Pattern})
	}
	span.Data(map[string]any{"event": string(evID), "chosen": chosen, "scored": rows})
}
