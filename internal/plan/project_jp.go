package plan

// project_jp.go: what Japanese allows, and therefore what must be decided
// before realization (plan.md §35, §47).
//
// Japanese differs from English in five ways that this file has to resolve
// explicitly, because leaving them to the realizer would mean guessing:
//
//	- argument omission    drop what the discourse already establishes
//	- topic marking        は for the topic, が for the subject, and the two are
//	                       not the same thing: an English subject does not
//	                       automatically become は
//	- case particles       role dependent, with a few genuine alternatives
//	- politeness           です/ます against だ/である
//	- honorifics           先生/さん and the honorific verb forms
//
// A zero argument is a *realization choice*, not a loss of meaning: the
// referent stays in the graph (plan.md §3).

import (
	"strings"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/trace"
)

// Japanese case particles.
const (
	Ga     = "が"
	Wa     = "は"
	Wo     = "を"
	Ni     = "に"
	De     = "で"
	To     = "と"
	Kara   = "から"
	Madara = "まで"
	No     = "の"
	Yor    = "より"
	Mo     = "も"
)

// projectJP fills the projection with everything Japanese decides.
func projectJP(r Request, p *Projection) {
	gp := newGenderPolicy(r.JLIR)
	span := r.recorder().Open(trace.StageProjection, "Japanese projection")
	defer span.Close()

	events := eventOrder(r.JLIR)
	span.Count("events", len(events))
	span.Label("politeness", politenessLabel(r.Style.Normalized().Politeness))
	if p.Honorific {
		span.Label("honorific", "on")
	}

	for _, ev := range events {
		if ev == nil {
			continue
		}
		ep := projectJPEvent(r, p, gp, ev, 0)
		if ep != nil {
			p.Events = append(p.Events, *ep)
		}
	}
	for _, e := range p.Events {
		if len(e.Notes) > 0 {
			span.Note("%s: %s", e.EventID, strings.Join(e.Notes, "; "))
		}
	}
	span.Data(p.Events)
}

func politenessLabel(v float64) string {
	switch {
	case v >= 0.75:
		return "very polite"
	case v >= 0.5:
		return "polite"
	case v >= 0.25:
		return "neutral"
	}
	return "plain"
}

// projectJPEvent plans one event for Japanese.
func projectJPEvent(r Request, p *Projection, gp genderPolicy, ev *jlir.Event, depth int) *EventPlan {
	ep := baseEventPlan(r.JLIR, ev, r, p)
	ep.SubjectRequired = false    // Japanese does not require an overt subject
	ep.Tense = enTense(ev, ep, p) // the tense decision is target independent

	// An embedded clause forbids a は-subject in the matrix clause: は cannot
	// scope over a subordinate clause, so the agent is marked with が instead.
	hasEmbed := eventHasEmbedding(r.JLIR, ev)

	overt := 0
	roles := ev.OrderArgs()
	for _, role := range roles {
		arg := ev.Args[role]
		np := jpNPPlan(r, p, gp, ev, role, arg.Value, depth)
		if np == nil {
			continue
		}
		np.Topic = false
		if isSubjectRole(role) {
			marker := jpSubjectMarker(r, p, ev, np, hasEmbed)
			np.Particle = marker
			np.Topic = marker == Wa
		} else {
			np.Particle = jpCaseParticle(r, p, ev, role, np)
		}

		// Argument omission: what the discourse already establishes need not be
		// written again (plan.md §35, §15). A phrase whose noun does not exist
		// in the target is always dropped, even as the only argument: an empty
		// predicate is more honest than a borrowed surface form.
		omit, why := jpOmit(r, p, ev, role, np, depth)
		if omit && (overt > 0 || np.Omit) {
			np.Omit = true
			np.IsZero = true
			np.OmitWhy = why
			np.Determiner = ""
			np.Pronoun = ""
			loss(p, DimReferential, "argument_omission", 0.1, ev.ID, np.EntityID, "%s", why)
			ep.Args[role] = np
			if isSubjectRole(role) {
				ep.Subject = np
			}
			if isObjectRole(role) {
				ep.Object = np
			}
			continue
		}
		overt++
		ep.Args[role] = np
		if isSubjectRole(role) {
			ep.Subject = np
			if np.Omitted() {
				ep.OmitSubject = true
			}
		}
		if isObjectRole(role) {
			ep.Object = np
		}
	}

	// Honorific loss in the other direction: an honorific address with no
	// Japanese counterpart is recorded rather than dropped (plan.md §5).
	if p.Honorific {
		loss(p, DimPragmatic, "honorific", 0.18, ev.ID, "", "honorific address is carried into Japanese by the verb form and an honorific suffix")
	}

	selectConstructions(r, p, ep, ev, lang.JA, depth)
	return ep
}

// jpNPPlan projects one argument for Japanese.
func jpNPPlan(r Request, p *Projection, gp genderPolicy, ev *jlir.Event, role string, id jlir.ID, depth int) *NPPlan {
	g := r.JLIR

	if sub := embeddedEvent(g, id); sub != nil {
		np := &NPPlan{Role: role, IsClause: true, Particle: No}
		if depth >= maxEmbedDepth {
			np.Note("embedded clause dropped: the embedding depth limit was reached")
			loss(p, DimPropositional, "embedding_depth", 0.4, ev.ID, "", "clause embedding deeper than %d is not realized", maxEmbedDepth)
			return np
		}
		np.Clause = projectJPEvent(r, p, gp, sub, depth+1)
		return np
	}

	e := g.Entity(id)
	if e == nil {
		np := &NPPlan{Role: role, Particle: Ga}
		np.Note("argument refers to unknown entity " + string(id))
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
		Number:       orUnknown(e.Number, jlir.NumberUnknown),
	}
	np.Gender = gp.supported(e)
	np.GenderSupported = np.Gender != ""
	if e.Zero {
		np.IsZero = true
		np.Note("zero anaphora: the referent is carried, the surface stays empty")
	}

	// Japanese keeps the source surface: no article, no forced number, no
	// gendered pronoun unless the discourse asks for one.
	np.Noun = nounFor(g, e, lang.JA, p, ev.ID)
	if np.Noun == "" {
		if r.Source == lang.JA {
			np.Noun = aliasFor(g, e, lang.JA)
		}
	}
	if np.Noun == "" {
		// No Japanese surface: Japanese may simply leave the argument out,
		// which is a realization choice rather than a translation of garbage.
		np.Omit = true
		np.IsZero = true
		np.OmitWhy = "no Japanese surface form for " + entityKey(e) + ": realized as zero"
		loss(p, DimReferential, "lexical_gap", 0.45, ev.ID, e.ID, "%s", np.OmitWhy)
	}

	// Pronoun explicitness: 彼/彼女 for an established, third person referent,
	// suppressed otherwise (plan.md §47).
	if !np.Omitted() && !np.Proper && e.Person == 3 && np.Given {
		np.PronounOpts = jpPronouns(e, np)
		style := r.Style.Normalized().PronounExplicitness
		if len(np.PronounOpts) > 0 && style >= 0.25 {
			if len(np.PronounOpts) == 1 {
				np.Pronoun = np.PronounOpts[0]
			} else {
				weights := map[string]float64{}
				for i, o := range np.PronounOpts {
					weights[o] = 1 - float64(i)*0.2
				}
				if pick, ok := r.decide(DecidePronoun, string(ev.ID)+":"+role,
					"which Japanese pronoun realizes "+entityKey(e)+" as "+role+"?", weights, p); ok {
					np.Pronoun = pick
				}
			}
			if np.Pronoun == "" {
				np.Pronoun = np.PronounOpts[0]
			}
		}
	}
	if np.Pronoun != "" {
		if ok, _, why := gp.check(e, jpGenderOf(np.Pronoun)); !ok {
			np.Note("dropped pronoun " + np.Pronoun + ": " + why)
			np.Pronoun = ""
			np.PronounOpts = nil
		}
	}

	// Honorific: only for an addressable third person, and recorded as a
	// pragmatic choice rather than smuggled in.
	if p.Honorific && !np.Omitted() && e.Person == 3 && isPersonType(e.Type) && !np.Proper {
		np.Honorific = honorificSuffix(r, p)
	}
	return np
}

// jpPronouns returns the Japanese pronoun alternatives licensed for an entity.
// Gender suppression is the default: an unsourced gender yields こちら/あちら
// or no pronoun at all rather than 彼 or 彼女.
func jpPronouns(e *jlir.Entity, np *NPPlan) []string {
	switch np.Gender {
	case jlir.GenderMale:
		return []string{"彼"}
	case jlir.GenderFemale:
		return []string{"彼女"}
	}
	switch e.Type {
	case jlir.TypeHuman, jlir.TypePerson:
		return []string{"あの人"}
	case jlir.TypeArtifact, jlir.TypePlace, jlir.TypeFood, jlir.TypeAbstract, jlir.TypeTime:
		return []string{"それ"}
	case jlir.TypeOrganization:
		return []string{"そちら", "あちら"}
	}
	return nil
}

// jpGenderOf maps a Japanese pronoun back to the English gender it asserts, so
// the single genderPolicy check covers both languages.
func jpGenderOf(pron string) string {
	switch pron {
	case "彼":
		return "he"
	case "彼女":
		return "she"
	}
	return ""
}

// honorificSuffix picks the Japanese honorific from the respect profile.
func honorificSuffix(r Request, p *Projection) string {
	if p.Style.Normalized().Politeness >= 0.85 && p.Style.Normalized().Register == RegisterFormal {
		return "様"
	}
	return "さん"
}

// isPersonType reports whether a type denotes a person or animal.
func isPersonType(t string) bool {
	switch t {
	case jlir.TypeHuman, jlir.TypePerson, jlir.TypeAnimal:
		return true
	}
	return false
}

// jpOmit decides whether an argument is realized as pure absence. The rules
// are conservative on purpose: omitting an argument that the discourse has not
// established is a meaning loss, not a style choice.
func jpOmit(r Request, p *Projection, ev *jlir.Event, role string, np *NPPlan, depth int) (bool, string) {
	g := r.JLIR
	e := g.Entity(np.EntityID)
	if e == nil {
		return true, "argument has no entity to refer to"
	}
	if np.IsClause {
		return false, ""
	}
	if np.Omit {
		return true, np.OmitWhy
	}
	if focus(g, e) {
		return false, "" // contrastive material is never dropped
	}
	if e.Zero {
		return true, "source zero anaphora is preserved as zero"
	}
	if isSubjectRole(role) && !given(g, e) && depth == 0 {
		return false, "" // first mention of the agent needs its phrase
	}
	if given(g, e) {
		if e.Salience >= 0.35 || e.MentionCount > 1 {
			return true, "discourse already establishes " + entityKey(e) + ": realized as zero"
		}
		if role == jlir.RolePatient || role == jlir.RoleRecipient || role == jlir.RoleLocation {
			return true, "definite already-mentioned " + role + " is realized as zero"
		}
	}
	return false, ""
}

// jpSubjectMarker decides は (topic) against が (subject). The rule is not
// "English subject -> は": the topic is the discourse-given, comment-relevant
// constituent, and が marks an exhaustive or contrastive subject.
func jpSubjectMarker(r Request, p *Projection, ev *jlir.Event, np *NPPlan, hasEmbed bool) string {
	g := r.JLIR
	e := g.Entity(np.EntityID)
	if hasEmbed {
		return Ga // は cannot scope over an embedded clause
	}
	if focus(g, e) {
		return Ga
	}
	if inList(g.Info.Topic, np.EntityID) {
		return Wa
	}
	weights := map[string]float64{}
	switch {
	case np.Given:
		weights[Wa] = 0.62
		weights[Ga] = 0.38
	case e != nil && e.Salience >= 0.6:
		weights[Wa] = 0.55
		weights[Ga] = 0.45
	default:
		weights[Ga] = 0.62
		weights[Wa] = 0.38
	}
	if r.Style.Normalized().Register == RegisterLiterary {
		weights[Wa] += 0.2
	}
	mark, _ := r.decide(DecideCase, string(ev.ID)+":"+np.Role+":topic",
		"is the agent of "+string(ev.ID)+" the topic (は) or an exhaustive subject (が)?", weights, p)
	if mark != Wa {
		return Ga
	}
	return Wa
}

// jpCaseParticle returns the case particle for a non-subject role. A few roles
// genuinely have two realizations and the choice is left to the oracle.
func jpCaseParticle(r Request, p *Projection, ev *jlir.Event, role string, np *NPPlan) string {
	base := jpBaseCase(role)
	alts := jpCaseAlternatives(role, base)
	if len(alts) < 2 {
		return base
	}
	weights := map[string]float64{}
	for i, a := range alts {
		weights[a] = 1 - float64(i)*0.25
	}
	pick, ok := r.decide(DecideCase, string(ev.ID)+":"+role+":case",
		"which case particle marks the "+role+" of "+string(ev.ID)+"?", weights, p)
	if ok && pick != "" {
		return pick
	}
	return base
}

// jpBaseCase is the default particle per role.
func jpBaseCase(role string) string {
	switch role {
	case jlir.RoleAgent, jlir.RoleExperiencer:
		return Ga
	case jlir.RolePatient, jlir.RoleTheme, jlir.RoleStimulus, jlir.RoleProduct:
		return Wo
	case jlir.RoleRecipient, jlir.RoleGoal, jlir.RoleTime, jlir.RoleBeneficiary:
		return Ni
	case jlir.RoleSource:
		return Kara
	case jlir.RoleLocation:
		return Ni
	case jlir.RoleInstrument, jlir.RoleManner:
		return De
	case jlir.RoleComitative:
		return To
	case jlir.RolePossessor:
		return No
	case jlir.RoleCause:
		return "ので"
	}
	return De
}

// jpCaseAlternatives lists the particles a role may take, best first.
func jpCaseAlternatives(role, base string) []string {
	switch role {
	case jlir.RoleGoal:
		return []string{Ni, "へ"}
	case jlir.RoleLocation:
		return []string{Ni, De}
	case jlir.RoleTime:
		return []string{Ni}
	case jlir.RoleComitative:
		return []string{To, "と一緒に"}
	case jlir.RoleSource:
		return []string{Kara}
	}
	return []string{base}
}

// eventHasEmbedding reports whether the event contains a role bound to another
// event, which changes the Japanese subject marking rule.
func eventHasEmbedding(g *jlir.Graph, ev *jlir.Event) bool {
	if ev == nil {
		return false
	}
	for _, role := range ev.OrderArgs() {
		if embeddedEvent(g, ev.Args[role].Value) != nil {
			return true
		}
	}
	return false
}

// inList reports membership in an ID list.
func inList(list []jlir.ID, id jlir.ID) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

// jpSentenceFinal picks the sentence final particle from the attitude recorded
// in the pragmatic layer, falling back on the style profile. An empty result
// means the planner wrote no particle at all, which is the neutral default for
// Japanese written prose.
func jpSentenceFinal(r Request, g *jlir.Graph) string {
	attitude := ""
	parts := []string{}
	if g != nil {
		attitude = strings.ToLower(g.Prag.SentenceFinalAttitude)
		parts = append(parts, g.SourceFeat.SentenceFinalParticles...)
		parts = append(parts, g.Prag.SentenceFinalParticles...)
	}
	for _, s := range parts {
		if s != "" {
			return s
		}
	}
	switch attitude {
	case "question", "確認", "interrogative":
		return "か"
	case "exclamation", "感嘆":
		return "よ"
	case "soft", "hedged", "polite", "epistemic":
		return "ね"
	case "assertive", "emphatic", "断定":
		return "よ"
	}
	if r.Style.Normalized().Politeness >= 0.5 {
		return "ね"
	}
	return ""
}
