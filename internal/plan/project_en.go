package plan

// project_en.go: what English forces on the translator.
//
// English is the demanding target (plan.md §46). It cannot drop a subject, it
// cannot do without a determiner, it cannot leave number unstated, and it marks
// gender on its pronouns. Every one of those obligations is decided *here*, at
// projection time, so the realizer only has to apply morphology:
//
//	- subject realization  always overt, except imperatives and passives
//	- determiner           a / an / the / this / that / zero, from discourse state
//	- number               forced even when Japanese was ambiguous
//	- pronoun              he/she/they/it, never invented
//	- word order           SVO + preposition + object + adjuncts
//	- tense / auxiliary    be, have, will, must, should, may, do-support negation
//	- agreement            third person -s, irregular be

import (
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/trace"
)

// maxEmbedDepth bounds clausal embedding (the controlled predicate of INTEND,
// the complement of KNOW). Deeper structures stay unrealized and are reported,
// because an unbounded recursion on a cyclic graph is not a translation.
const maxEmbedDepth = 2

// projectEN fills the projection with everything English demands.
func projectEN(r Request, p *Projection) {
	gp := newGenderPolicy(r.JLIR)
	span := r.recorder().Open(trace.StageProjection, "English projection")
	defer span.Close()

	events := eventOrder(r.JLIR)
	span.Count("events", len(events))

	for _, ev := range events {
		if ev == nil {
			continue
		}
		ep := projectENEvent(r, p, gp, ev, 0)
		if ep != nil {
			p.Events = append(p.Events, *ep)
		}
	}

	for _, e := range p.Events {
		if e.NumberUndetermined {
			span.Note("%s: English had to commit to a number the source does not license (UNDERDETERMINED)", e.EventID)
		}
		if len(e.Notes) > 0 {
			span.Note("%s: %s", e.EventID, strings.Join(e.Notes, "; "))
		}
	}
	span.Data(p.Events)
}

// projectENEvent plans one event for English.
func projectENEvent(r Request, p *Projection, gp genderPolicy, ev *jlir.Event, depth int) *EventPlan {
	ep := baseEventPlan(r.JLIR, ev, r, p)
	g := r.JLIR

	// --- tense ------------------------------------------------------------
	ep.Tense = enTense(ev, ep, p)

	// --- auxiliary selection ---------------------------------------------
	ep.Aux = enAux(ev, ep, p)

	// --- arguments --------------------------------------------------------
	roles := ev.OrderArgs()
	for _, role := range roles {
		arg := ev.Args[role]
		np := enNPPlan(r, p, gp, ev, role, arg.Value, depth)
		if np == nil {
			continue
		}
		// English does not drop arguments: every bound role surfaces, and a
		// Japanese zero becomes an overt phrase here (plan.md §46). What the
		// discourse licenses is *which* form, not whether.
		np.Omit = false
		np.OmitWhy = ""
		ep.Args[role] = np
		// Constraint propagation (plan.md §39): when a third person referent
		// has no sourced gender, the gendered pronouns are killed here, before
		// they are ever generated, and the kill is recorded so the UI can say
		// why "she" never existed.
		if np.Gender == "" && np.Person == 3 && isPersonType(np.Type) {
			ep.Rejects(RejectInfo{
				Slot: string(ev.ID) + ":" + role, Lex: "he/she", Rule: HardInventedGender,
				Reason: "gender of " + entityKey(rt_entity(g, np)) + " has no upstream provenance; he/she are not licensed",
				Stage:  "projection",
			})
		}
		if isSubjectRole(role) {
			ep.Subject = np
		}
		if isObjectRole(role) {
			ep.Object = np
		}
	}
	for _, role := range roles {
		if np := ep.Args[role]; np != nil && !np.NumberDetermined {
			ep.NumberUndetermined = true
		}
	}
	if ep.Subject == nil {
		// English wants a subject; a sentence without one is only grammatical
		// in the imperative or in a passive, both of which are rare readings.
		if ev.Mood == jlir.MoodImperative {
			ep.SubjectRequired = false
		} else {
			ep.SubjectRequired = true
			ep.Note("English requires an overt subject; none is licensed by the source")
			if !enImpliedSubject(g, ev) {
				loss(p, DimReferential, "subject", 0.5, ev.ID, "", "no agent/experiencer is bound to %s; the English subject would be invented", ev.ID)
			}
		}
	} else {
		ep.SubjectRequired = true
	}
	if ep.Agent() != nil && ep.Agent().Omitted() {
		ep.Note("English subject is licensed to be absent only under the imperative reading")
	}

	// --- construction -----------------------------------------------------
	selectConstructions(r, p, ep, ev, lang.EN, depth)

	if depth == 0 {
		span := r.recorder().Open(trace.StageConstruct, "construction selection "+string(ev.Predicate))
		span.Data(map[string]any{"event": ev.ID, "sense": ev.Predicate, "options": ep.Constructions, "chosen": ep.Construction})
		span.Close()
	}
	return ep
}

// Agent returns the plan for the subject-like role, or nil.
func (e *EventPlan) Agent() *NPPlan {
	if e == nil {
		return nil
	}
	if e.Subject != nil {
		return e.Subject
	}
	return e.Args[jlir.RoleAgent]
}

// enTense resolves the tense English must use. An undetermined source tense
// falls back on the tense morpheme the analysis recorded, and failing that on
// the present, with a loss hint either way.
func enTense(ev *jlir.Event, ep *EventPlan, p *Projection) string {
	switch ev.Tense {
	case jlir.TensePast, jlir.TensePresent, jlir.TenseFuture:
		return ev.Tense
	}
	if m := evFeature(ev, "tense"); m != "" {
		switch strings.ToUpper(m) {
		case jlir.TensePast, jlir.TensePresent, jlir.TenseFuture:
			return strings.ToUpper(m)
		}
	}
	ep.Note("tense defaulted to present: the source does not determine it")
	return jlir.TensePresent
}

// enAux selects the auxiliary English needs. Progressive and perfect aspects
// and the modal readings of the ontology all require an auxiliary, and losing
// one is a propositional loss, so the choice is recorded on the event plan.
func enAux(ev *jlir.Event, ep *EventPlan, p *Projection) string {
	modality := strings.ToUpper(ev.Modality)
	switch modality {
	case jlir.ModalityMust, jlir.ModalityOblig:
		return "must"
	case jlir.ModalityShould:
		return "should"
	case jlir.ModalityMay:
		return "may"
	}
	switch strings.ToUpper(ev.Aspect) {
	case jlir.AspectProgressive:
		return "be"
	case jlir.AspectPerfect:
		return "have"
	}
	if ep.Tense == jlir.TenseFuture {
		return "will"
	}
	// てしまう plus a past tense is a perfect, not a bare past (plan.md §12).
	if strings.EqualFold(ev.Completion, jlir.CompletionCompleted) && ep.Tense == jlir.TensePast {
		return "have"
	}
	return ""
}

// enNPPlan projects one argument of an event for English.
func enNPPlan(r Request, p *Projection, gp genderPolicy, ev *jlir.Event, role string, id jlir.ID, depth int) *NPPlan {
	g := r.JLIR

	// A role bound to an event is a clausal complement, not a noun phrase.
	if sub := embeddedEvent(g, id); sub != nil {
		np := &NPPlan{Role: role, IsClause: true, Particle: caseForEN(role)}
		if depth >= maxEmbedDepth {
			np.Note("embedded clause dropped: the embedding depth limit was reached")
			loss(p, DimPropositional, "embedding_depth", 0.4, ev.ID, "", "clause embedding deeper than %d is not realized", maxEmbedDepth)
			return np
		}
		np.Clause = projectENEvent(r, p, gp, sub, depth+1)
		return np
	}

	e := g.Entity(id)
	if e == nil {
		np := &NPPlan{Role: role, Particle: caseForEN(role)}
		np.Note("argument refers to unknown entity " + string(id) + "; English cannot realize it")
		loss(p, DimReferential, "unresolved_referent", 0.6, ev.ID, id, "no entity %s in the graph for role %s", id, role)
		return np
	}

	np := &NPPlan{
		Role:         role,
		EntityID:     e.ID,
		Type:         e.Type,
		Proper:       e.Proper,
		Animacy:      e.Animacy,
		Person:       e.Person,
		Salience:     e.Salience,
		MentionCount: e.MentionCount,
		Given:        given(g, e),
		Particle:     caseForEN(role),
	}
	np.Gender = gp.supported(e)
	np.GenderSupported = np.Gender != ""
	if np.Gender == "" && e.Gender != "" && e.Gender != jlir.GenderUnknown {
		np.Note("gender is present but has no upstream provenance; no gendered pronoun is licensed")
		loss(p, DimReferential, "gender", 0.3, ev.ID, e.ID, "gender of %s has no upstream provenance and will not be realized", entityKey(e))
	}
	if e.Zero {
		np.IsZero = true
		np.Note("zero anaphora: the referent is preserved internally and must surface in English")
		loss(p, DimReferential, "zero_pronoun", 0.25, ev.ID, e.ID, "Japanese zero pronoun for %s becomes an overt English phrase", entityKey(e))
	}
	for _, f := range e.Features {
		if strings.HasPrefix(f.Key, "adj:") {
			np.Adjectives = append(np.Adjectives, jlir.ValueString(f.Value))
		}
	}
	sort.Strings(np.Adjectives)

	// --- noun -------------------------------------------------------------
	np.Noun = nounFor(g, e, lang.EN, p, ev.ID)
	// A name takes no article. Trusting the analyser's part of speech here
	// produces "A Taro", because both analysers report a personal name as an
	// ordinary noun.
	if r.IsName != nil && e != nil {
		for _, a := range e.Aliases {
			if a.Lang == r.Source && r.IsName(a.Surface, a.Lang) {
				e.Proper = true
				np.Proper = true
				break
			}
		}
	}
	if np.Noun == "" {
		if n := aliasFor(g, e, lang.EN); n != "" {
			np.Noun = n
		} else if e.Proper || hasNameAlias(e, lang.EN) {
			// already reported by nounFor
		} else {
			np.Note("no English surface form for " + entityKey(e) + "; the referent is left unrealized rather than transliterated")
			loss(p, DimReferential, "lexical_gap", 0.55, ev.ID, e.ID, "no English realization of %s in the source graph", entityKey(e))
		}
	}

	// --- number -----------------------------------------------------------
	num, determined := numberOf(e, lang.EN)
	if determined {
		np.Number = num
		np.NumberDetermined = true
		np.NumberOpts = []string{num}
	} else {
		// English forces a number the source does not license. This is an
		// UNDERDETERMINED condition: both options stay alive, the prior wins
		// the default slot, and the loss is recorded. plan.md §46 forbids
		// pretending the coin was fair.
		np.Number = jlir.NumberSingular
		np.NumberOpts = []string{jlir.NumberSingular, jlir.NumberPlural}
		np.Note("number undetermined by the source; English must commit to one")
		loss(p, DimReferential, "number", 0.45, ev.ID, e.ID, "UNDERDETERMINED: English requires a number for %s but the source does not determine it", entityKey(e))
	}
	if np.Noun == "" && np.Pronoun == "" {
		// Without a noun the only licensed realization is a pronoun; if even
		// that is unavailable the phrase stays empty and the loss is recorded.
		//
		// A zero anaphor whose referent the source left undecided is the case
		// that matters most here, because it is the common one: Japanese omits
		// the subject of an ordinary sentence and English cannot. "someone" says
		// there is an agent without saying which, which is exactly what the
		// source asserted — the referent distribution is a distribution, not a
		// missing value. Choosing "they" instead would put a person in a slot
		// the source left open and claim a number the source never licensed.
		if p := zeroAnaphorPronoun(e); p != "" {
			np.Pronoun = p
			np.Note("realized as the English indefinite pronoun: the source left the " +
				"referent undecided, and this names no particular referent")
		} else if opts := gp.pronouns(e); len(opts) > 0 {
			np.PronounOpts = opts[:1]
		} else {
			np.Note("referent has neither a target noun nor a licensed pronoun")
		}
	}

	// --- determiner -------------------------------------------------------
	np.DeterminerOpts, np.Determiner = enDeterminer(r, p, gp, ev, e, np)

	// --- pronoun ----------------------------------------------------------
	// A pronoun replaces the noun only once the referent is established;
	// otherwise English is introducing the referent and must use a noun phrase.
	if intro := enPronounLicensed(r, e, np); intro {
		opts := gp.pronouns(e)
		if r.Style.Normalized().PronounExplicitness < 0.5 {
			opts = filterPronouns(opts)
		}
		if len(opts) > 0 {
			np.PronounOpts = opts
			if len(opts) == 1 {
				np.Pronoun = opts[0]
			} else {
				weights := map[string]float64{}
				for i, o := range opts {
					weights[o] = 1 - float64(i)*0.2
				}
				if pick, ok := r.decide(DecidePronoun, string(ev.ID)+":"+role,
					"which pronoun realizes "+entityKey(e)+" as "+role+"?", weights, p); ok {
					np.Pronoun = pick
				}
			}
		}
	}
	if np.Pronoun != "" {
		if ok, rule, why := gp.check(e, np.Pronoun); !ok {
			// Belt and braces: a gendered pronoun can never survive a check
			// that failed, however it was produced.
			np.Note("dropped pronoun " + np.Pronoun + ": " + why)
			np.Pronoun = ""
			np.PronounOpts = nil
			np.Notes = append(np.Notes, rule)
		}
	}
	if np.Pronoun != "" && np.Determiner != "" {
		// "the he" is not English: a determiner only belongs to a noun phrase.
		np.Determiner = ""
		np.DeterminerOpts = nil
	}
	// A source that already wrote a pronoun has licensed a pronoun: the author
	// established the referent. The lexicalizer answers 彼 with "he", and that
	// must land in the pronoun slot. Treating it as a noun produces "a he",
	// which is not English and which the re-parser then reads back as a common
	// noun. A determiner never belongs in front of a pronoun either.
	if isEnglishPronoun(np.Noun) {
		if np.Pronoun == "" {
			np.Pronoun = np.Noun
		}
		np.Noun = ""
		np.Determiner = ""
		np.DeterminerOpts = nil
	}
	return np
}

// enPronouns is the closed set of English pronouns. A form in it is a pronoun
// by grammar, whatever the lexicon called it.
var enPronouns = map[string]bool{
	"i": true, "me": true, "my": true, "mine": true, "myself": true,
	"you": true, "your": true, "yours": true, "yourself": true,
	"he": true, "him": true, "his": true, "himself": true,
	"she": true, "her": true, "hers": true, "herself": true,
	"it": true, "its": true, "itself": true,
	"we": true, "us": true, "our": true, "ours": true,
	"they": true, "them": true, "their": true, "theirs": true,
	"this": true, "that": true, "these": true, "those": true,
	"who": true, "whom": true, "whose": true, "which": true, "what": true,
	"someone": true, "somebody": true, "something": true,
	"anyone": true, "anybody": true, "anything": true,
	"everyone": true, "everybody": true, "everything": true,
	"no one": true, "nobody": true, "nothing": true,
}

// isEnglishPronoun reports whether form is a pronoun.
func isEnglishPronoun(form string) bool {
	return form != "" && enPronouns[strings.ToLower(strings.TrimSpace(form))]
}

// rt_entity resolves an entity id in a graph; it keeps the rejection ledger code
// readable.
func rt_entity(g *jlir.Graph, np *NPPlan) *jlir.Entity {
	if g == nil || np == nil {
		return nil
	}
	return g.Entity(np.EntityID)
}

// enPronounLicensed reports whether a pronoun may replace the noun phrase.
// Introducing a referent requires a noun phrase in English; an established
// referent may be pronominalized, and a style that asks for explicit pronouns
// may pronominalize earlier.
func enPronounLicensed(r Request, e *jlir.Entity, np *NPPlan) bool {
	if e == nil || np.IsZero {
		return false
	}
	if e.Person == 1 || e.Person == 2 {
		return true // "I" and "you" carry no lexical information to lose
	}
	if r.Style.Normalized().PronounExplicitness >= 0.8 {
		return true
	}
	return np.Given && !np.Proper
}

// filterPronouns drops the singular they in favour of the disambiguating forms
// when style forbids pronouns: with PronounExplicitness low, English keeps a
// name rather than an uninformative "they".
func filterPronouns(opts []string) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		if o == "they" && len(opts) > 1 {
			continue
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return opts
	}
	return out
}

// enDeterminerOpts decides the article from discourse state (plan.md §46):
// a previously mentioned or uniquely identified referent takes "the", a new
// one takes "a"/"an", a mass noun takes none.
func enDeterminer(r Request, p *Projection, gp genderPolicy, ev *jlir.Event, e *jlir.Entity, np *NPPlan) ([]string, string) {
	weights := map[string]float64{}
	switch {
	case np.Proper || hasNameAlias(e, lang.EN):
		weights[""] = 0.92
	case np.Number == jlir.NumberPlural && np.Given:
		weights[""] = 0.5
		weights["the"] = 0.5
	case np.Given:
		weights["the"] = 0.78
		weights["this"] = 0.12
		weights["that"] = 0.1
	default:
		if np.Number == jlir.NumberMass {
			weights[""] = 0.7
			weights["the"] = 0.3
		} else {
			art := "a"
			if np.Noun != "" {
				art = articleFor(np.Noun)
			}
			weights[art] = 0.74
			weights["the"] = 0.26
		}
	}
	// A contrastive focus licenses the demonstrative even for a new referent.
	if focus(r.JLIR, e) {
		weights["this"] = weights["this"] + 0.3
	}
	opts := sortedKeys(weights)
	best, ok := r.decide(DecideMarker, string(ev.ID)+":"+np.Role+":determiner",
		"which determiner introduces "+np.Head()+" as "+np.Role+"?", weights, p)
	if !ok {
		// Nothing was decidable (no noun to attach an article to): stay bare.
		np.Determiner = ""
	}
	return opts, best
}

// articleFor chooses "a" or "an" by initial sound, not by letter: English
// spells half the exceptions with a vowel letter and a consonant sound.
func articleFor(noun string) string {
	w := strings.ToLower(strings.TrimSpace(noun))
	if w == "" {
		return "a"
	}
	// Multiword heads take their article from the first word.
	if i := strings.IndexAny(w, " -"); i > 0 {
		w = w[:i]
	}
	for _, u := range []string{"uni", "use", "usu", "uti", "ubi", "eu", "eul", "one"} {
		if strings.HasPrefix(w, u) {
			return "a"
		}
	}
	switch w[0] {
	case 'a', 'e', 'i', 'o':
		return "an"
	case 'h':
		if strings.HasPrefix(w, "hour") || strings.HasPrefix(w, "honest") || strings.HasPrefix(w, "heir") {
			return "an"
		}
		return "a"
	}
	return "a"
}

// enOmittable reports whether English may leave an argument unexpressed.
func enOmittable(g *jlir.Graph, ev *jlir.Event, role string, id jlir.ID) bool {
	e := g.Entity(id)
	if e == nil || isSubjectRole(role) {
		return false
	}
	if !given(g, e) || focus(g, e) {
		return false
	}
	return role == jlir.RolePatient || role == jlir.RoleRecipient || role == jlir.RoleLocation
}

// enImpliedSubject reports whether the discourse makes a subject recoverable
// even though the event does not bind one, which is the difference between a
// licensed drop and an invented referent.
// zeroAnaphorPronoun returns the English indefinite pronoun that realizes a
// zero anaphor the source left undecided, or "" when the anaphor is not that.
//
// The condition is deliberately narrow: the entity must be a zero anaphor AND
// its referent distribution must still be open. An anaphor the discourse layer
// already resolved to Taro is Taro, not someone, and rendering it as "someone"
// would discard information the analysis did the work to recover.
func zeroAnaphorPronoun(e *jlir.Entity) string {
	if e == nil || !e.Zero || e.Referent == nil {
		return ""
	}
	if len(e.Referent.Options) < 2 {
		return ""
	}
	if e.Referent.Resolved {
		return ""
	}
	open := false
	for _, o := range e.Referent.Options {
		if o == "UNKNOWN" {
			open = true
		}
	}
	if !open {
		return ""
	}
	switch e.Type {
	case jlir.TypeHuman, jlir.TypePerson, jlir.TypeAnimal:
		return "someone"
	case jlir.TypeArtifact, jlir.TypeFood, jlir.TypeAbstract, jlir.TypeEventLike:
		return "something"
	}
	// An undecided zero anaphor of unknown animacy is still a person in most
	// Japanese sentences, and "something" for a subject reads as a thing. A
	// generic "someone" is the least committal English subject that exists.
	return "someone"
}

func enImpliedSubject(g *jlir.Graph, ev *jlir.Event) bool {
	if g == nil {
		return false
	}
	for _, id := range g.Info.Given {
		if g.Entity(id) != nil {
			return true
		}
	}
	return false
}

// caseForEN returns the preposition English conventionally uses for a role.
func caseForEN(role string) string { return enPrep(role) }

// evFeature reads an event feature value.
func evFeature(ev *jlir.Event, key string) string {
	if ev == nil {
		return ""
	}
	if f, ok := ev.Feature(key); ok {
		return jlir.ValueString(f.Value)
	}
	return ""
}

// isSubjectRole reports whether a role functions as the subject of its event.
func isSubjectRole(role string) bool {
	return role == jlir.RoleAgent || role == jlir.RoleExperiencer
}

// isObjectRole reports whether a role functions as the direct object.
func isObjectRole(role string) bool {
	switch role {
	case jlir.RolePatient, jlir.RoleTheme, jlir.RoleStimulus:
		return true
	}
	return false
}
