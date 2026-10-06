package plan

// construction.go: the construction library of plan.md §26.
//
// Generation is centred on constructions, not on words:
//
//	INTEND(agent,event) -> "X intends to Y" / "X means to Y" / "X plans to Y"
//	                    -> 「XはYするつもりだ」「XはYしようと思っている」
//
// Each construction carries the semantic and syntactic requirements it has,
// its register, a naturalness prior, and the discourse conditions under which
// it is preferred. Selection is a scored filter, not an argmax over a bag of
// strings: a construction whose requirements the event does not satisfy is
// *rejected* (a hard constraint of plan.md §37) and the rejection is recorded
// so the UI can explain why a fluent-looking option never appeared.
//
// Pattern syntax (identical shape for both languages, opposite binding
// direction):
//
//	English   uppercase = a slot, lowercase = a preposition binding the next slot
//	Japanese  uppercase = a slot, lowercase = a case particle marking the
//	          preceding slot ("SUBJ OBJ を RECIPIENT に V")
//
// Slots are SUBJ (the construction's subject role), V (the predicate), OBJ (the
// direct object) or a semantic role name from jlir.AllRoles.
//
// Selection is keyed by SENSE, not by family. plan.md §36 requires
// Semantics(T) ⊇ RequiredMeaning, and two senses inside one ontology family can
// need opposite frames: TRANSFER.01 hands a theme to a recipient while
// TRANSFER.09 takes one from a source, so offering the RECEIVE frame for
// TRANSFER.01 offers a sentence in which the recipient has vanished and the
// proposition has reversed. Every construction therefore declares the senses it
// realizes, and both selection and the event-level check consult that
// declaration.

import (
	"sort"
	"strings"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/trace"
)

// Construction is one realizable frame for a predicate sense in one language.
type Construction struct {
	ID string `json:"id"`
	// Target is the language this construction produces.
	Target lang.Lang `json:"target"`
	// Senses lists the predicate sense ids this construction realizes. It is the
	// selection key: a sense is offered only the constructions that name it, so
	// the frame for a sense can never be one a sibling sense needs instead.
	Senses []string `json:"senses"`
	// Pattern is the ordered frame, e.g. "SUBJ V OBJ to RECIPIENT" or
	// "SUBJ OBJ を RECIPIENT に V".
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
	// means "look the family up in the verb lexicon", or, for a chain frame,
	// "the predicate comes from the theme's own verb".
	Lex string `json:"lex,omitempty"`
	// Join is the word between the verb and a clausal argument: "to" for
	// control, "that" for a full clause complement. It is what makes
	// "intends to" possible without a dictionary entry for the whole phrase.
	Join string `json:"join,omitempty"`
	// Tail is a string appended after the predicate: Japanese つもりだ,
	// わけではない, English "or something".
	Tail string `json:"tail,omitempty"`
	// Chain selects how a thematic-verb frame continues the theme's verb:
	// "nai" (negative stem), "nai_n" (negative stem + な), "te"/"ta" (て-form),
	// "dic" (dictionary form), "tai" (連用形 + たい), "potential". It is what
	// turns "must eat" into 食べなければならない without a dictionary entry for
	// the whole phrase.
	Chain string `json:"chain,omitempty"`
	// Complement describes how a clausal argument is attached:
	// "to"   control, the sub-clause has no subject (want to go)
	// "that" full clause complement (know that he said)
	// "quot" Japanese quoted clause before the verb (〜と思う)
	Complement string `json:"complement,omitempty"`
	// SubjRole overrides which role realizes as the grammatical subject, which
	// Japanese needs for 在る ("there is") and English for a passive.
	SubjRole string `json:"subjRole,omitempty"`
	// Polarity records the Japanese ending the construction prefers:
	// "masu", "da" or "nominal".
	Polarity string `json:"polarity,omitempty"`
	// Honorific marks the honorific variant of a predicate. Honorificity is
	// expressed by construction choice alone: the construction names its own
	// honorific lexeme (お渡しする, 申す, 参见 honorific forms), because a
	// dictionary form cannot be conjugated into one by appending ます.
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
	// Oral is the medium of the source as a tri-state: "1" spoken, "0"
	// written, "" unknown. plan.md §4 forbids reading an absent value as a
	// positive claim, so an event that carries no medium annotation is not
	// evidence that it was spoken and earns no spoken-register preference.
	Oral string
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

// oralFromMedium classifies a source medium feature. An absent feature yields
// the empty string, meaning "unknown", not "spoken".
func oralFromMedium(medium string) string {
	switch {
	case medium == "":
		return ""
	case strings.Contains(medium, "writ"):
		return "0"
	default:
		return "1"
	}
}

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
	// bySense indexes constructions by every sense they declare, so selection
	// never has to ask which family might cover a sense.
	bySense map[string][]*Construction // sense|target -> constructions
}

// Default returns the built-in construction library.
func Default() *Library {
	l := &Library{byID: map[string]*Construction{}, bySense: map[string][]*Construction{}}
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
	// A construction that names no sense is unreachable: selection is keyed by
	// sense, so an empty declaration is a data error rather than a wildcard.
	// Asserting it here keeps "180 constructions" from silently meaning
	// "however many of them some sense happens to reach".
	if len(c.Senses) == 0 {
		return
	}
	l.items = append(l.items, c)
	l.byID[c.ID] = c
	for _, s := range c.Senses {
		key := s + "|" + string(c.Target)
		l.bySense[key] = append(l.bySense[key], c)
	}
	for key := range l.bySense {
		l.sortBySense(key)
	}
}

// forSense returns the constructions that realize a sense: the ones that name
// it exactly, plus the ones that name its whole family. The family key is what
// makes a generic frame ("SUBJ V to LOCATION" for any motion sense) reachable
// without every construction having to enumerate every sense of its family.
func (l *Library) forSense(sense string, target lang.Lang) []*Construction {
	out := l.bySense[sense+"|"+string(target)]
	if fam := familyOf(sense); fam != sense {
		out = append(out, l.bySense[fam+"|"+string(target)]...)
	}
	return out
}

// sortBySense keeps a sense's candidates in a deterministic order.
func (l *Library) sortBySense(key string) {
	sort.SliceStable(l.bySense[key], func(i, j int) bool {
		return l.bySense[key][i].ID < l.bySense[key][j].ID
	})
}

// Get looks a construction up by id.
func (l *Library) Get(id string) (*Construction, bool) {
	if l == nil {
		return nil, false
	}
	c, ok := l.byID[id]
	return c, ok
}

// realizesSense reports whether the construction claims the sense. A sense may
// be declared exactly, or through the family it belongs to when the
// construction covers every sense of that family.
func (c *Construction) realizesSense(sense string) bool {
	fam := familyOf(sense)
	for _, s := range c.Senses {
		if s == sense {
			return true
		}
		if !strings.Contains(s, ".") && s == fam {
			return true
		}
	}
	return false
}

// familiesForSense lists the construction families that declare the sense, in
// deterministic order. It replaces a hand-maintained family alias table: the
// inventory itself is the authority on which frame realizes which sense, so the
// two vocabularies cannot drift apart.
func (l *Library) familiesForSense(sense string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range l.forSense(sense, lang.EN) {
		if seen[c.Family] {
			continue
		}
		seen[c.Family] = true
		out = append(out, c.Family)
	}
	sort.Strings(out)
	return out
}

// Select returns every construction that can realize sense in target, scored
// against ctx. Surviving constructions come first, ordered by score; rejected
// ones follow with Rejected set, so the caller can fill the rejection ledger
// without knowing which rule killed what.
func (l *Library) Select(sense string, target lang.Lang, ctx Context) []Scored {
	ctx.Known = len(l.forSense(sense, target)) > 0
	ctx.Target = target
	if ctx.Family == "" {
		ctx.Family = familyOf(sense)
	}
	var out []Scored
	seen := map[string]bool{}
	for _, c := range l.forSense(sense, target) {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		out = append(out, l.score(c, ctx))
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
	// An unknown medium buys nothing: the preference for a spoken or a written
	// register is evidence-driven, and there is no evidence when the event says
	// nothing about its medium.
	if ctx.Oral == "1" && c.Register == RegisterCasual {
		s.Score += 0.05
		why = append(why, "spoken register")
	}
	if ctx.Oral == "0" && c.Register == RegisterFormal {
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
		return ctx.Oral, ctx.Oral != ""
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
	// A construction that lost the scoring race never became a candidate. It is
	// a ranking fact, not a violation, so it goes to the dominance ledger and
	// not into the rejection ledger: plan.md §37 forbids mixing the two, and
	// the UI sorts the ledger by the HARD./SOFT. prefix.
	var dominated []string
	if len(live) > maxConstructions {
		for _, s := range live[maxConstructions:] {
			dominated = append(dominated, s.C.ID)
			ep.Note("dominated construction " + s.C.ID + ": lower ranked than the " +
				sprintf("%d", maxConstructions) + " better constructions")
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
	traceSelection(r.recorder(), ev.ID, chosen, live, dominated)
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
		Oral:       oralFromMedium(evFeature(ev, "medium")),
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
//
// It deliberately supplies NO verb lexeme. Filling the slot with the sense
// name lower-cased -- which is what this used to do -- makes the morphology
// layer conjugate "unknown" into "unknowns." / "unknowned."; the re-parser then
// reads that non-word back as the same UNKNOWN.VERB with an empty argument set,
// the verifier compares two empty role maps and reports EXACT with full
// confidence. A fabricated sentence that passes the safety net is the one
// failure plan.md §22 exists to prevent.
//
// A predicate with no registered lexeme therefore yields no candidate at all.
// Realize rejects it and the pipeline reports UNPARSABLE, which is true.
func fallbackConstruction(target lang.Lang, ctx Context) *Construction {
	return &Construction{
		ID:          "C.FALLBACK." + strings.ToUpper(string(target)) + ".01",
		Target:      target,
		Senses:      []string{ctx.Sense},
		Pattern:     "SUBJ V OBJ",
		Family:      "UNKNOWN",
		Register:    RegisterNeutral,
		Naturalness: 0,
		Notes: "generic frame for an unregistered sense; it carries no verb lexeme, so the " +
			"predicate cannot be realized and the sentence is reported UNPARSABLE rather " +
			"than filled with a placeholder word",
	}
}

// targetLabel names a language for question text.
func targetLabel(l lang.Lang) string {
	if l == lang.JA {
		return "Japanese"
	}
	return "English"
}

// traceSelection writes a construction selection span. Kept here so both
// projections record the same artifact shape. dominated is reported in its own
// field so a frame that lost a scoring race is never read as a frame that was
// illegal (plan.md §37).
func traceSelection(rec *trace.Recorder, evID jlir.ID, chosen string, live []Scored, dominated []string) {
	if rec == nil {
		return
	}
	span := rec.Open(trace.StageConstruct, "construction selection "+string(evID))
	defer span.Close()
	rows := make([]map[string]any, 0, len(live))
	for _, s := range live {
		rows = append(rows, map[string]any{"id": s.C.ID, "score": s.Score, "why": s.Why, "pattern": s.C.Pattern})
	}
	data := map[string]any{"event": string(evID), "chosen": chosen, "scored": rows}
	if len(dominated) > 0 {
		data["dominated"] = dominated
	}
	span.Data(data)
}
