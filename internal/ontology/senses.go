package ontology

// This file is the predicate inventory itself. It deliberately stays small:
// plan.md §11 forbids turning the ontology into a WordNet sized sense
// dictionary, and §10 forbids mapping words to words. What we store is a set
// of event primitives plus the constraints (role frame + boolean features)
// that let near synonyms collapse onto one primitive with different feature
// values.
//
// Structure: every predicate family is itself a sense (`TRANSFER`) carrying
// the coarse role frame, and the numbered children (`TRANSFER.01`) are the
// concrete primitives. The parent link is what `Registry.Ladder` walks when a
// candidate set is too large for a single oracle Choice question
// (plan.md §33).
//
// Surface forms for both languages live in internal/lexicon; this file is
// language independent by construction.

import "github.com/nico/jev-trans/internal/jlir"

// Feature keys. plan.md §11 asks for manner/physicality/ownership_change/
// intentionality style constraints next to the role frame; these are the keys
// we settled on. They are booleans encoded as "1" because a missing key and a
// "0" value must not be confused (a missing key means "not claimed").
const (
	fMotion      = "motion"
	fPhysical    = "physicality"
	fIntent      = "intentionality"
	fAnimate     = "animate_subject"
	fPastOnly    = "past_only"
	fControl     = "control"
	fCreation    = "creation"
	fPossession  = "possession"
	fEvaluation  = "evaluation"
	fSpeech      = "speech"
	fCognition   = "cognition"
	fAffect      = "affect"
	fPerception  = "perception"
	fScalar      = "scalar"
	fQuant       = "quantifier"
	fChangeState = "change_of_state"
	fContact     = "contact"
	fTelic       = "telic"
	fDurative    = "durative"
	fSocial      = "social"
	fDeictic     = "deictic"
	fWeather     = "weather"
	fHabitual    = "habitual"
	fPast        = "past"
	fManner      = "manner"
	fPlural      = "plurality"
	// fDirection is not a boolean: it carries "away"/"toward"/"back" for the
	// deictic motion predicates, which the construction library needs in order
	// to choose between go and come.
	fDirection = "direction"
	// fIngestible is "solid", "liquid" or "either". It is the constraint that
	// stops an eat predicate from realizing as "drink": the two share a family
	// in the construction library, so without it the choice is arbitrary and
	// the verifier rightly rejects the result.
	fIngestible = "ingestible"
)

// sn builds a realizable sense. Feature keys are set to "1".
func sn(id, name, gloss string, args []Arg, feats ...string) *Sense {
	s := &Sense{ID: id, Name: name, Gloss: gloss, Args: args, Realizable: true}
	if len(feats) > 0 {
		s.Features = make(map[string]string, len(feats))
		for _, f := range feats {
			s.Features[f] = "1"
		}
	}
	return s
}

// req is a mandatory slot.
func req(role string) Arg { return Arg{Role: role, Required: true} }

// opt is an optional slot.
func opt(role string) Arg { return Arg{Role: role} }

// dfl is an optional slot that carries the role to fall back on when the
// source marks it with an ambiguous case marker (Japanese に, で or English a
// preposition with several readings). Without this the case-to-role mapping
// would have to guess, which plan.md §2 forbids.
func dfl(role, fallback string) Arg { return Arg{Role: role, Default: fallback} }

// Default returns the predicate inventory JEV-Trans reasons over.
//
// It is built fresh on every call (Sense maps are mutable and the semantic
// layer annotates copies), but callers that only read should keep the result.
func Default() *Registry {
	reg := New(All())
	// Directional motion predicates carry an explicit direction constraint.
	// 行く is away from the deictic centre and 来る is towards it, which is
	// lexical knowledge the surface does not spell out with a particle. The
	// construction library selects "go"/"come" on it; without the constraint
	// every directional construction is rejected as unsatisfiable and the
	// sentence falls through to a directionless fallback.
	constrain(reg, "MOVE.01", fDirection, "away")
	constrain(reg, "MOVE.02", fDirection, "toward")
	constrain(reg, "MOVE.03", fDirection, "back")
	// Solid versus liquid. 食べる and 飲む share the CONSUME family, so the
	// construction library would otherwise choose between "eat" and "drink" on
	// naturalness alone and could render ご飯を食べる as "drinks a meal".
	constrain(reg, "CONSUME.01", fIngestible, "solid")
	constrain(reg, "CONSUME.02", fIngestible, "liquid")
	constrain(reg, "CONSUME.03", fIngestible, "either")
	return reg
}

// constrain attaches a non-boolean constraint to a declared sense.
func constrain(reg *Registry, id, key, value string) {
	s, ok := reg.Sense(id)
	if !ok {
		return
	}
	if s.Features == nil {
		s.Features = map[string]string{}
	}
	s.Features[key] = value
}

// All returns every declared sense in declaration order. `New` sorts them.
func All() []*Sense {
	return allSenses
}

var allSenses = buildSenses()

// -----------------------------------------------------------------------------
// COMMUNICATE — speech acts.
// -----------------------------------------------------------------------------

func communicateSenses() []*Sense {
	return []*Sense{
		sn("COMMUNICATE", "communicate", "transfer a message between participants",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RolePatient), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fSpeech, fIntent, fAnimate, fSocial),

		sn("COMMUNICATE.01", "say", "produce an utterance",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RolePatient), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fSpeech, fIntent, fAnimate),
		sn("COMMUNICATE.02", "tell", "inform someone of something",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fSpeech, fIntent, fAnimate, fSocial),
		sn("COMMUNICATE.03", "ask", "request information or an action",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fSpeech, fIntent, fAnimate, fSocial),
		sn("COMMUNICATE.04", "answer", "respond to a question",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleSource),
				opt(jlir.RoleTime)},
			fSpeech, fIntent, fAnimate, fSocial),
		sn("COMMUNICATE.05", "explain", "make something understandable",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleManner), opt(jlir.RoleCause)},
			fSpeech, fIntent, fAnimate, fCognition),
		sn("COMMUNICATE.06", "talk", "converse",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary), opt(jlir.RoleTheme),
				opt(jlir.RoleComitative), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fSpeech, fIntent, fAnimate, fSocial, fDurative),
		sn("COMMUNICATE.07", "write", "produce a written message",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RoleProduct), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleTime), opt(jlir.RoleInstrument)},
			fSpeech, fIntent, fAnimate, fCreation, fTelic),
		sn("COMMUNICATE.08", "call", "telephone or address someone",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleTime)},
			fSpeech, fIntent, fAnimate),
		sn("COMMUNICATE.09", "name", "assign a name",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme)},
			fSpeech, fIntent, fAnimate, fCreation),
		sn("COMMUNICATE.10", "shout", "speak loudly",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RolePatient), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleManner)},
			fSpeech, fIntent, fAnimate, fEvaluation),
		sn("COMMUNICATE.11", "whisper", "speak quietly, often confidentially",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RolePatient), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleManner)},
			fSpeech, fIntent, fAnimate, fManner),
		sn("COMMUNICATE.12", "promise", "commit to a future course of action",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fSpeech, fIntent, fAnimate, fControl),
		sn("COMMUNICATE.13", "advise", "recommend a course of action",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleRecipient), dfl(jlir.RoleTheme, jlir.RoleGoal), opt(jlir.RoleCause)},
			fSpeech, fIntent, fAnimate, fCognition),
		sn("COMMUNICATE.14", "apologize", "express regret for a wrong",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary), opt(jlir.RoleTheme), opt(jlir.RoleCause)},
			fSpeech, fIntent, fAnimate, fAffect),
	}
}

// -----------------------------------------------------------------------------
// MOVE — locomotion and body movement of a theme.
// -----------------------------------------------------------------------------

func moveSenses() []*Sense {
	return []*Sense{
		sn("MOVE", "move", "change the location of a theme",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), dfl(jlir.RoleSource, jlir.RoleLocation),
				opt(jlir.RoleManner), opt(jlir.RoleTime), opt(jlir.RoleInstrument)},
			fMotion, fPhysical, fIntent),

		sn("MOVE.01", "go", "move towards a goal away from the speaker",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), dfl(jlir.RoleSource, jlir.RoleLocation),
				opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fMotion, fPhysical, fIntent, fDeictic),
		sn("MOVE.02", "come", "move towards the speaker or the addressee",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), dfl(jlir.RoleSource, jlir.RoleLocation),
				opt(jlir.RoleTime), opt(jlir.RoleComitative)},
			fMotion, fPhysical, fIntent, fDeictic),
		sn("MOVE.03", "return", "go back to a previous location",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleSource), dfl(jlir.RoleSource, jlir.RoleLocation),
				opt(jlir.RoleTime)},
			fMotion, fPhysical, fIntent, fDeictic),
		sn("MOVE.04", "arrive", "reach a destination",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), dfl(jlir.RoleSource, jlir.RoleLocation),
				opt(jlir.RoleTime)},
			fMotion, fPhysical, fTelic),
		sn("MOVE.05", "depart", "leave a location",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleLocation), opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fMotion, fPhysical, fTelic),
		sn("MOVE.06", "walk", "move on foot at walking pace",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), opt(jlir.RoleComitative), opt(jlir.RoleManner)},
			fMotion, fPhysical, fAnimate, fManner),
		sn("MOVE.07", "carry", "hold something while moving",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleGoal), opt(jlir.RoleLocation), opt(jlir.RoleManner)},
			fMotion, fPhysical, fControl, fPossession),
		sn("MOVE.08", "bring", "carry something towards the goal",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleRecipient), opt(jlir.RoleLocation)},
			fMotion, fPhysical, fIntent, fControl),
		sn("MOVE.09", "take", "seize or pick up, away from the goal",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleLocation), dfl(jlir.RoleGoal, jlir.RoleLocation)},
			fMotion, fPhysical, fControl, fPossession),
		sn("MOVE.10", "fetch", "go for something and return with it",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleRecipient), opt(jlir.RoleSource)},
			fMotion, fPhysical, fIntent, fControl),
		sn("MOVE.11", "follow", "move after someone else",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleSource), opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fMotion, fPhysical, fIntent),
		sn("MOVE.12", "enter", "go into a bounded space",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), dfl(jlir.RoleSource, jlir.RoleLocation)},
			fMotion, fPhysical, fTelic),
		sn("MOVE.13", "leave", "go away from a bounded space",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleLocation), opt(jlir.RoleGoal)},
			fMotion, fPhysical, fTelic),
		sn("MOVE.14", "travel", "move over a distance",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), dfl(jlir.RoleSource, jlir.RoleLocation),
				opt(jlir.RoleInstrument), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fMotion, fPhysical, fIntent, fDurative),
		sn("MOVE.15", "fly", "move through the air",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), opt(jlir.RoleTime), opt(jlir.RoleInstrument)},
			fMotion, fPhysical, fIntent),
		sn("MOVE.16", "drive", "operate a vehicle so that it moves",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RoleInstrument), dfl(jlir.RoleGoal, jlir.RoleLocation), opt(jlir.RoleComitative)},
			fMotion, fPhysical, fIntent, fControl),
		sn("MOVE.17", "escape", "get away from a threatening place",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleLocation), dfl(jlir.RoleGoal, jlir.RoleLocation)},
			fMotion, fPhysical, fIntent, fControl),
		sn("MOVE.18", "approach", "move close to someone or something",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), dfl(jlir.RoleSource, jlir.RoleLocation)},
			fMotion, fPhysical, fIntent, fDeictic),
	}
}

// -----------------------------------------------------------------------------
// RUN — the worked example of plan.md §10. One English verb, one Japanese
// verb per sense, and a single Japanese verb (走る) that has to reach
// RUN.01 only after the decision layer rules out the others.
// -----------------------------------------------------------------------------

func runSenses() []*Sense {
	return []*Sense{
		sn("RUN", "run", "a predicate family whose members only share the English surface 'run'",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleAgent), opt(jlir.RoleLocation), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fMotion, fIntent),

		sn("RUN.01", "locomotion_on_foot", "move fast on foot",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fMotion, fPhysical, fAnimate, fManner),
		sn("RUN.02", "operate_machine", "run a machine or program",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleLocation), opt(jlir.RoleTime)},
			fControl, fIntent, fAnimate, fPhysical),
		sn("RUN.03", "manage_organization", "administer an institution or programme",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleLocation), opt(jlir.RoleTime)},
			fControl, fIntent, fAnimate, fSocial),
		sn("RUN.04", "continue_functioning", "keep operating over time",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleTime), opt(jlir.RoleManner)}, fDurative, fPhysical, fHabitual),
		sn("RUN.05", "flow_liquid", "a liquid streams",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleLocation), dfl(jlir.RoleGoal, jlir.RoleLocation)},
			fMotion, fPhysical, fDurative),
	}
}

// -----------------------------------------------------------------------------
// TRANSFER — change of possession or location of a theme.
// -----------------------------------------------------------------------------

func transferSenses() []*Sense {
	return []*Sense{
		sn("TRANSFER", "transfer", "the theme changes possessor or location by agency",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RolePossessor),
				dfl(jlir.RoleRecipient, jlir.RoleLocation), opt(jlir.RoleInstrument), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fPhysical, fIntent, fPossession),

		sn("TRANSFER.01", "give", "voluntarily hand ownership to someone",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleRecipient), dfl(jlir.RoleSource, jlir.RolePossessor),
				opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fPhysical, fIntent, fPossession, fContact, fTelic),
		sn("TRANSFER.02", "send", "cause the theme to move towards a recipient",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleRecipient), dfl(jlir.RoleSource, jlir.RolePossessor),
				dfl(jlir.RoleGoal, jlir.RoleLocation), opt(jlir.RoleInstrument), opt(jlir.RoleTime)},
			fPhysical, fIntent, fTelic),
		sn("TRANSFER.03", "lend", "give temporarily and expect return",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleRecipient), dfl(jlir.RoleSource, jlir.RolePossessor),
				opt(jlir.RoleTime)},
			fPossession, fIntent, fDurative),
		sn("TRANSFER.04", "borrow", "take temporarily with the intent to return",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RolePossessor),
				dfl(jlir.RoleRecipient, jlir.RoleBeneficiary), opt(jlir.RoleTime)},
			fPossession, fIntent, fDurative),
		sn("TRANSFER.05", "buy", "acquire a theme in exchange for money",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleSource), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleInstrument)},
			fPossession, fIntent, fTelic),
		sn("TRANSFER.06", "sell", "dispose of a theme in exchange for money",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RolePossessor),
				req(jlir.RoleRecipient), opt(jlir.RoleInstrument)},
			fPossession, fIntent, fTelic),
		sn("TRANSFER.07", "offer", "present a theme and leave the decision to the recipient",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleRecipient), opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fIntent, fSocial, fPossession),
		sn("TRANSFER.08", "show", "make the theme perceptible to someone",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleRecipient), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fPerception, fIntent, fSocial),
		sn("TRANSFER.09", "receive", "take possession from a giver",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleSource), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleTime)},
			fPossession, fIntent, fContact),
		sn("TRANSFER.10", "deliver", "convey to the intended recipient",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleRecipient), dfl(jlir.RoleSource, jlir.RoleLocation),
				dfl(jlir.RoleGoal, jlir.RoleLocation)},
			fPhysical, fIntent, fTelic),
		sn("TRANSFER.11", "distribute", "allocate a theme to several recipients",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary),
				opt(jlir.RoleGoal), opt(jlir.RoleInstrument)},
			fIntent, fPossession, fSocial),
		sn("TRANSFER.12", "exchange", "swap themes in both directions",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleRecipient), dfl(jlir.RoleComitative, jlir.RoleSource)},
			fPossession, fIntent, fSocial),
		sn("TRANSFER.13", "steal", "take possession without the owner's consent",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), req(jlir.RoleSource), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary)},
			fPossession, fIntent, fEvaluation),
		sn("TRANSFER.14", "pass", "hand over or go past",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleLocation), opt(jlir.RoleGoal)},
			fPhysical, fIntent, fPossession),
		sn("TRANSFER.15", "lend_a_hand", "help, take part, or interfere",
			[]Arg{req(jlir.RoleAgent), opt(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary), opt(jlir.RoleManner)},
			fIntent, fSocial, fControl, fPossession),
	}
}

// -----------------------------------------------------------------------------
// MENTAL — cognition and volition.
// -----------------------------------------------------------------------------

func mentalSenses() []*Sense {
	return []*Sense{
		sn("MENTAL", "mental", "an internal state of a cognizer",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleTheme, jlir.RolePatient), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fCognition, fAnimate),

		sn("MENTAL.01", "think", "hold a thought",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleTheme, jlir.RolePatient), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fCognition, fAnimate),
		sn("MENTAL.02", "know", "hold a belief that is treated as settled",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RolePatient), opt(jlir.RoleTime)},
			fCognition, fAnimate),
		sn("MENTAL.03", "believe", "treat something as true",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleCause), opt(jlir.RoleTime)},
			fCognition, fAnimate),
		sn("MENTAL.04", "guess", "form a supposition without certainty",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fCognition, fAnimate),
		sn("MENTAL.05", "forget", "fail to recall something one knew",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fCognition, fAnimate, fPastOnly),
		sn("MENTAL.06", "remember", "recall something",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fCognition, fAnimate, fPastOnly),
		sn("MENTAL.07", "want", "have a desire for something",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleTheme, jlir.RoleGoal), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fCognition, fIntent, fAnimate),
		sn("MENTAL.08", "decide", "commit to a course of action",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleTheme, jlir.RolePatient), opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fCognition, fIntent, fAnimate, fControl),
		sn("MENTAL.09", "plan", "form an intention in advance",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleTheme, jlir.RolePatient), opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fCognition, fIntent, fAnimate),
		sn("MENTAL.10", "mean", "convey an intended sense",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleRecipient), opt(jlir.RoleManner)},
			fCognition, fIntent, fSpeech),
		sn("MENTAL.11", "understand", "grasp the meaning of something",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fCognition, fAnimate),
		sn("MENTAL.12", "doubt", "hold that something may be false",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleCause)},
			fCognition, fAnimate),
		sn("MENTAL.13", "consider", "think about something deliberately",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleTheme, jlir.RolePatient), opt(jlir.RoleGoal), opt(jlir.RoleManner)},
			fCognition, fIntent, fAnimate),
	}
}

// -----------------------------------------------------------------------------
// FEEL — affect.
// -----------------------------------------------------------------------------

func feelSenses() []*Sense {
	return []*Sense{
		sn("FEEL", "feel", "an affective state of an experiencer",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleTheme, jlir.RoleStimulus), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fAffect, fAnimate),

		sn("FEEL.01", "like", "experience positive affect towards a theme",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fAffect, fAnimate, fEvaluation),
		sn("FEEL.02", "love", "experience intense positive affect",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fAffect, fAnimate, fEvaluation),
		sn("FEEL.03", "hate", "experience intense negative affect",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fAffect, fAnimate, fEvaluation),
		sn("FEEL.04", "fear", "be afraid of a threat",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fAffect, fAnimate),
		sn("FEEL.05", "be_glad", "be pleased about something",
			[]Arg{req(jlir.RoleExperiencer), opt(jlir.RoleTheme), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fAffect, fAnimate, fEvaluation),
		sn("FEEL.06", "be_sorry", "feel regret or sympathy",
			[]Arg{req(jlir.RoleExperiencer), opt(jlir.RoleTheme), opt(jlir.RoleCause)},
			fAffect, fAnimate, fEvaluation),
		sn("FEEL.07", "be_happy", "be in a positive affective state",
			[]Arg{req(jlir.RoleExperiencer), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fAffect, fAnimate),
		sn("FEEL.08", "be_sad", "be in a negative affective state",
			[]Arg{req(jlir.RoleExperiencer), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fAffect, fAnimate),
		sn("FEEL.09", "be_angry", "be in an angry state",
			[]Arg{req(jlir.RoleExperiencer), opt(jlir.RoleTheme), opt(jlir.RoleCause)},
			fAffect, fAnimate, fEvaluation),
		sn("FEEL.10", "be_surprised", "react to something unexpected",
			[]Arg{req(jlir.RoleExperiencer), opt(jlir.RoleTheme), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fAffect, fAnimate),
		sn("FEEL.11", "be_careful_with", "pay considerate attention to someone or something",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fAffect, fIntent, fAnimate, fSocial),
	}
}

// -----------------------------------------------------------------------------
// PERCEIVE — acquisition of sensory information.
// -----------------------------------------------------------------------------

func perceiveSenses() []*Sense {
	return []*Sense{
		sn("PERCEIVE", "perceive", "an experienceer becomes aware of a stimulus",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleStimulus, jlir.RoleTheme), opt(jlir.RoleInstrument), opt(jlir.RoleTime), opt(jlir.RoleLocation)},
			fPerception, fAnimate),

		sn("PERCEIVE.01", "see", "receive visual information",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleStimulus, jlir.RoleTheme), opt(jlir.RoleInstrument), opt(jlir.RoleLocation), opt(jlir.RoleTime)},
			fPerception, fAnimate),
		sn("PERCEIVE.02", "look", "direct the eyes, without asserting discovery",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleStimulus, jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleLocation), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fPerception, fIntent, fAnimate, fMotion),
		sn("PERCEIVE.03", "hear", "receive auditory information",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleStimulus, jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleLocation), opt(jlir.RoleTime)},
			fPerception, fAnimate),
		sn("PERCEIVE.04", "listen", "attend to sound without asserting comprehension",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleStimulus, jlir.RoleTheme), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fPerception, fIntent, fAnimate, fDurative),
		sn("PERCEIVE.05", "watch", "attend to something over time",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleStimulus, jlir.RoleTheme), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fPerception, fIntent, fAnimate, fDurative),
		sn("PERCEIVE.06", "notice", "become aware of something",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleStimulus), opt(jlir.RoleTime)},
			fPerception, fAnimate, fCognition),
		sn("PERCEIVE.07", "find", "come upon a state or thing by search or chance",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleStimulus), dfl(jlir.RoleTheme, jlir.RolePatient), opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fPerception, fCognition, fAnimate),
		sn("PERCEIVE.08", "smell", "receive olfactory information",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleStimulus, jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleLocation)},
			fPerception, fAnimate),
		sn("PERCEIVE.09", "touch", "make physical contact with a theme",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fPerception, fPhysical, fContact, fAnimate),
		sn("PERCEIVE.10", "recognize", "identify something as already known",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleStimulus), opt(jlir.RoleTime)},
			fPerception, fCognition, fAnimate),
		sn("PERCEIVE.11", "be_discerning", "have a sharp eye for what is worth choosing",
			[]Arg{req(jlir.RoleExperiencer), dfl(jlir.RoleTheme, jlir.RolePatient)},
			fPerception, fEvaluation, fAnimate),
	}
}

// -----------------------------------------------------------------------------
// EXIST — being, identity and copular predication.
// -----------------------------------------------------------------------------

func existSenses() []*Sense {
	return []*Sense{
		sn("EXIST", "exist", "hold of an entity with respect to a property or class",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fCognition),

		sn("EXIST.01", "be", "the copula: the theme instantiates the complement",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), opt(jlir.RoleTime), opt(jlir.RoleLocation)},
			fCognition),
		sn("EXIST.02", "exist", "instantiate without an assertion of identity",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fPhysical, fDeictic),
		sn("EXIST.03", "seem", "appear to be, from the speaker's judgement",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), opt(jlir.RoleExperiencer), opt(jlir.RoleManner)},
			fCognition, fEvaluation),
		sn("EXIST.04", "appear", "come into view or come to exist",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fPhysical, fPerception, fDeictic),
		sn("EXIST.05", "remain", "stay in the same state",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime), opt(jlir.RoleComitative)},
			fDurative, fDeictic),
		sn("EXIST.06", "be_born", "come into existence as an organism",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleTime, jlir.RoleLocation), opt(jlir.RoleSource)},
			fPhysical, fChangeState, fPastOnly),
		sn("EXIST.07", "die", "cease to be alive",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleTime, jlir.RoleLocation), opt(jlir.RoleCause)},
			fPhysical, fChangeState, fPastOnly),
		sn("EXIST.08", "reside", "have a habitual place of living",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleSource), opt(jlir.RoleTime)},
			fPhysical, fDurative),
		sn("EXIST.09", "be_absent", "not be present at a location",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fPhysical, fDeictic),
		sn("EXIST.10", "consist_of", "be made up of parts",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleSource), opt(jlir.RoleComitative)},
			fPhysical),
	}
}

// -----------------------------------------------------------------------------
// HAVE — possession, holding, wearing.
// -----------------------------------------------------------------------------

func haveSenses() []*Sense {
	return []*Sense{
		sn("HAVE", "have", "the possessor is related to the theme by ownership or presence",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fPossession),

		sn("HAVE.01", "have", "be in a state of possessing or experiencing",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), opt(jlir.RoleTime), opt(jlir.RoleLocation)},
			fPossession),
		sn("HAVE.02", "own", "hold rights of possession over something",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fPossession, fControl),
		sn("HAVE.03", "possess", "have as a property or an attribute",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), opt(jlir.RoleManner)},
			fPossession),
		sn("HAVE.04", "contain", "have something inside",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), opt(jlir.RoleSource)},
			fPossession, fPhysical),
		sn("HAVE.05", "lack", "not have what is needed",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), opt(jlir.RoleGoal)},
			fPossession, fEvaluation),
		sn("HAVE.06", "hold", "keep in the hands or under control",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fControl, fPossession, fContact),
		sn("HAVE.07", "wear", "have something on the body as clothing",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fPossession, fPhysical),
		sn("HAVE.08", "be_equipped", "have the instruments needed to act",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), opt(jlir.RoleGoal)},
			fPossession, fControl),
		sn("HAVE.09", "owe", "be under an obligation to pay or repay",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), dfl(jlir.RoleRecipient, jlir.RoleBeneficiary), opt(jlir.RoleTime)},
			fPossession, fControl),
		sn("HAVE.10", "share", "have or use something jointly with others",
			[]Arg{req(jlir.RolePossessor), req(jlir.RoleTheme), dfl(jlir.RoleComitative, jlir.RoleRecipient)},
			fPossession, fSocial),
	}
}

// -----------------------------------------------------------------------------
// COMPARE — scalar comparison.
// -----------------------------------------------------------------------------

func compareSenses() []*Sense {
	return []*Sense{
		sn("COMPARE", "compare", "relate two participants on a scale",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleComitative), opt(jlir.RoleComitative), opt(jlir.RoleTheme)},
			fScalar),

		sn("COMPARE.01", "more", "greater in amount or degree",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleComitative), opt(jlir.RoleComitative)},
			fScalar, fEvaluation),
		sn("COMPARE.02", "less", "smaller in amount or degree",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleComitative), opt(jlir.RoleComitative)},
			fScalar, fEvaluation),
		sn("COMPARE.03", "better", "preferred on an evaluative scale",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleComitative), opt(jlir.RoleComitative)},
			fScalar, fEvaluation),
		sn("COMPARE.04", "worse", "dispreferred on an evaluative scale",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleComitative), opt(jlir.RoleComitative)},
			fScalar, fEvaluation),
		sn("COMPARE.05", "equal", "the same on the scale",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleComitative), opt(jlir.RoleComitative)},
			fScalar),
		sn("COMPARE.06", "different", "not the same",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleComitative), opt(jlir.RoleComitative)},
			fScalar, fEvaluation),
		sn("COMPARE.07", "similar", "alike in relevant respects",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleComitative), opt(jlir.RoleComitative)},
			fScalar),
		sn("COMPARE.08", "the_same", "identical in relevant respects",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleComitative)},
			fScalar),
	}
}

// -----------------------------------------------------------------------------
// QUANTIFY — quantity.
// -----------------------------------------------------------------------------

func quantifySenses() []*Sense {
	return []*Sense{
		sn("QUANTIFY", "quantify", "specify the quantity of a set",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleTime), opt(jlir.RoleComitative)},
			fQuant, fScalar),

		sn("QUANTIFY.01", "many", "a large number of individuals",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fQuant, fPlural),
		sn("QUANTIFY.02", "much", "a large amount of mass",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fQuant, fPlural),
		sn("QUANTIFY.03", "few", "a small number of individuals",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fQuant, fPlural),
		sn("QUANTIFY.04", "all", "the whole of a set",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fQuant, fPlural),
		sn("QUANTIFY.05", "some", "an unspecified part of a set",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fQuant, fPlural),
		sn("QUANTIFY.06", "most", "the greater part of a set",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fQuant, fPlural),
		sn("QUANTIFY.07", "every", "each member of a set without exception",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fQuant, fPlural),
		sn("QUANTIFY.08", "several", "more than two, not many",
			[]Arg{req(jlir.RoleTheme)},
			fQuant, fPlural),
		sn("QUANTIFY.09", "no", "not any member of a set",
			[]Arg{req(jlir.RoleTheme)},
			fQuant, fPlural),
		sn("QUANTIFY.10", "both", "each of two members",
			[]Arg{req(jlir.RoleTheme)},
			fQuant, fPlural),
	}
}

// -----------------------------------------------------------------------------
// MODAL — obligation, permission and ability.
// -----------------------------------------------------------------------------

func modalSenses() []*Sense {
	return []*Sense{
		sn("MODAL", "modal", "the speaker's stance towards the truth of the proposition",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleExperiencer), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fCognition),

		sn("MODAL.01", "must", "obligation or strong necessity",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleExperiencer, jlir.RoleAgent), opt(jlir.RoleTime)},
			fCognition, fControl),
		sn("MODAL.02", "should", "recommendation or expectation",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleExperiencer, jlir.RoleAgent), opt(jlir.RoleTime)},
			fCognition, fEvaluation),
		sn("MODAL.03", "may", "possibility or permission",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleExperiencer, jlir.RoleAgent), opt(jlir.RoleTime)},
			fCognition),
		sn("MODAL.04", "can", "ability or capacity",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fCognition, fControl),
		sn("MODAL.05", "be_allowed", "be permitted to do something",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fCognition, fControl, fSocial),
		sn("MODAL.06", "be_able", "have the capacity to do something",
			[]Arg{req(jlir.RoleExperiencer), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fCognition, fControl),
		sn("MODAL.07", "must_not", "prohibition",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleExperiencer, jlir.RoleAgent), opt(jlir.RoleTime)},
			fCognition, fControl, fEvaluation),
	}
}

// -----------------------------------------------------------------------------
// CHANGE — change of state.
// -----------------------------------------------------------------------------

func changeSenses() []*Sense {
	return []*Sense{
		sn("CHANGE", "change", "the theme reaches a different state",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RolePatient), opt(jlir.RoleGoal), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fChangeState),

		sn("CHANGE.01", "change", "switch from one state to another",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RolePatient), opt(jlir.RoleGoal), opt(jlir.RoleCause)},
			fChangeState),
		sn("CHANGE.02", "turn_into", "become something else by transformation",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), dfl(jlir.RoleSource, jlir.RolePatient), opt(jlir.RoleCause)},
			fChangeState, fTelic),
		sn("CHANGE.03", "grow", "increase in size or amount",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fChangeState, fDurative),
		sn("CHANGE.04", "increase", "become greater",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RolePatient), opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fChangeState, fScalar),
		sn("CHANGE.05", "decrease", "become smaller",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RolePatient), opt(jlir.RoleGoal), opt(jlir.RoleTime)},
			fChangeState, fScalar),
		sn("CHANGE.06", "become", "enter a new state",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fChangeState, fTelic),
		sn("CHANGE.07", "develop", "progress to a more advanced stage",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleCause), opt(jlir.RoleTime), opt(jlir.RoleGoal)},
			fChangeState, fDurative),
		sn("CHANGE.08", "occur", "happen, come to occur",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleTime, jlir.RoleLocation), opt(jlir.RoleCause)},
			fChangeState, fDeictic),
		sn("CHANGE.09", "become_visible", "come into view or noticeability",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleExperiencer, jlir.RoleAgent), opt(jlir.RoleTime)},
			fChangeState, fPerception),
	}
}

// -----------------------------------------------------------------------------
// CONSUME — ingestion.
// -----------------------------------------------------------------------------

func consumeSenses() []*Sense {
	return []*Sense{
		sn("CONSUME", "consume", "take food or drink into the body",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleInstrument), opt(jlir.RoleTime), opt(jlir.RoleLocation)},
			fPhysical, fAnimate, fTelic),

		sn("CONSUME.01", "eat", "ingest solid food",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleInstrument), opt(jlir.RoleTime), opt(jlir.RoleLocation)},
			fPhysical, fAnimate, fTelic),
		sn("CONSUME.02", "drink", "ingest liquid",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleInstrument), opt(jlir.RoleTime), opt(jlir.RoleLocation)},
			fPhysical, fAnimate, fTelic),
		sn("CONSUME.03", "taste", "perceive by eating or drinking a little",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleStimulus, jlir.RoleTheme), opt(jlir.RoleTime)},
			fPerception, fAffect, fAnimate),
		sn("CONSUME.04", "swallow", "move food or liquid down the throat",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fPhysical, fAnimate, fTelic),
		sn("CONSUME.05", "chew", "grind food with the teeth",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleTime)},
			fPhysical, fAnimate),
		sn("CONSUME.06", "prepare_food", "make a dish ready to eat",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleProduct), dfl(jlir.RoleTheme, jlir.RoleSource), opt(jlir.RoleInstrument), opt(jlir.RoleTime)},
			fCreation, fIntent, fAnimate),
	}
}

// -----------------------------------------------------------------------------
// WEATHER — meteorological events. Japanese and English both mark the
// experiencer slot empty and let the phenomenon be the theme; the lexicon
// carries that as a weight difference between senses.
// -----------------------------------------------------------------------------

func weatherSenses() []*Sense {
	return []*Sense{
		sn("WEATHER", "weather", "a meteorological phenomenon obtains at a place and time",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fWeather, fPhysical, fDeictic),

		sn("WEATHER.01", "rain", "liquid precipitation falls",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fWeather, fPhysical),
		sn("WEATHER.02", "snow", "frozen precipitation falls",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fWeather, fPhysical),
		sn("WEATHER.03", "be_sunny", "the sun is unobscured",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fWeather, fPhysical),
		sn("WEATHER.04", "be_cloudy", "the sky is overcast",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fWeather, fPhysical),
		sn("WEATHER.05", "be_cold", "the temperature is low",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fWeather, fPhysical, fEvaluation),
		sn("WEATHER.06", "be_hot", "the temperature is high",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fWeather, fPhysical, fEvaluation),
		sn("WEATHER.07", "be_windy", "strong air movement",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fWeather, fPhysical),
		sn("WEATHER.08", "be_humid", "the air holds much moisture",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime)},
			fWeather, fPhysical),
		sn("WEATHER.09", "storm", "violent weather",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fWeather, fPhysical, fEvaluation),
		sn("WEATHER.10", "clear_up", "the weather improves",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fWeather, fChangeState),
	}
}

// -----------------------------------------------------------------------------
// TIME — temporal relations and spans.
// -----------------------------------------------------------------------------

func timeSenses() []*Sense {
	return []*Sense{
		sn("TIME", "time", "a temporal relation between events or points",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleTime), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fDeictic, fPhysical),

		sn("TIME.01", "be_before", "the theme precedes the source",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleTime), opt(jlir.RoleTime)},
			fDeictic),
		sn("TIME.02", "be_after", "the theme follows the source",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleTime), opt(jlir.RoleTime)},
			fDeictic),
		sn("TIME.03", "last", "occupy a duration",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleTime), opt(jlir.RoleManner)},
			fDeictic, fDurative, fPastOnly),
		sn("TIME.04", "begin", "start at a point in time",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleTime, jlir.RoleSource), opt(jlir.RoleCause)},
			fChangeState, fTelic, fDeictic),
		sn("TIME.05", "end", "stop at a point in time",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleTime, jlir.RoleSource), opt(jlir.RoleCause)},
			fChangeState, fTelic, fDeictic),
		sn("TIME.06", "duration", "the length of a span",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleTime)},
			fDeictic, fScalar),
		sn("TIME.07", "be_during", "obtain throughout a span",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleTime)},
			fDeictic, fDurative),
		sn("TIME.08", "be_early", "happen before the expected time",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleTime), opt(jlir.RoleTime)},
			fDeictic, fEvaluation),
		sn("TIME.09", "be_late", "happen after the expected time",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleSource, jlir.RoleTime), opt(jlir.RoleTime)},
			fDeictic, fEvaluation),
	}
}

// -----------------------------------------------------------------------------
// MEET — social interaction.
// -----------------------------------------------------------------------------

func meetSenses() []*Sense {
	return []*Sense{
		sn("MEET", "meet", "participants come together face to face",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleComitative, jlir.RoleRecipient), dfl(jlir.RoleLocation, jlir.RoleGoal), opt(jlir.RoleTime)},
			fSocial, fMotion, fPhysical, fAnimate),

		sn("MEET.01", "meet", "come together with someone",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleComitative, jlir.RoleRecipient), dfl(jlir.RoleLocation, jlir.RoleGoal), opt(jlir.RoleTime)},
			fSocial, fMotion, fAnimate),
		sn("MEET.02", "visit", "go to see someone for a period",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleRecipient), dfl(jlir.RoleLocation, jlir.RoleGoal), opt(jlir.RoleTime)},
			fSocial, fMotion, fAnimate, fIntent),
		sn("MEET.03", "greet", "address someone on arrival",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleRecipient), opt(jlir.RoleManner), opt(jlir.RoleTime)},
			fSocial, fIntent, fAnimate),
		sn("MEET.04", "see_off", "accompany someone on their departure",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleRecipient), dfl(jlir.RoleLocation, jlir.RoleGoal), opt(jlir.RoleTime)},
			fSocial, fMotion, fAnimate),
		sn("MEET.05", "encounter", "come across someone or something by chance",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleStimulus), opt(jlir.RoleLocation), opt(jlir.RoleTime)},
			fSocial, fMotion, fAnimate),
		sn("MEET.06", "welcome", "receive someone gladly",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleRecipient), dfl(jlir.RoleLocation, jlir.RoleGoal), opt(jlir.RoleTime)},
			fSocial, fAffect, fAnimate),
		sn("MEET.07", "introduce", "present one person to another",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleRecipient), dfl(jlir.RoleTheme, jlir.RoleComitative), opt(jlir.RoleTime)},
			fSocial, fIntent, fAnimate),
	}
}

// -----------------------------------------------------------------------------
// WORK — labour, learning and production.
// -----------------------------------------------------------------------------

func workSenses() []*Sense {
	return []*Sense{
		sn("WORK", "work", "activity undertaken towards a purpose",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RolePatient), dfl(jlir.RoleGoal, jlir.RoleBeneficiary),
				dfl(jlir.RoleLocation, jlir.RoleInstrument), opt(jlir.RoleTime), opt(jlir.RoleComitative)},
			fIntent, fAnimate, fControl),

		sn("WORK.01", "work", "perform an occupation or task",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RolePatient), dfl(jlir.RoleLocation, jlir.RoleInstrument),
				dfl(jlir.RoleGoal, jlir.RoleBeneficiary), opt(jlir.RoleTime)},
			fIntent, fAnimate, fControl, fDurative),
		sn("WORK.02", "study", "learn systematically",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RoleSource), opt(jlir.RoleTime), opt(jlir.RoleGoal)},
			fIntent, fAnimate, fCognition, fDurative),
		sn("WORK.03", "teach", "instruct a learner",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleRecipient), dfl(jlir.RoleTheme, jlir.RoleSource), opt(jlir.RoleTime)},
			fIntent, fAnimate, fSocial, fSpeech),
		sn("WORK.04", "learn", "acquire knowledge or a skill",
			[]Arg{req(jlir.RoleAgent), dfl(jlir.RoleTheme, jlir.RoleSource), dfl(jlir.RoleRecipient, jlir.RoleAgent),
				opt(jlir.RoleTime)},
			fAnimate, fCognition, fIntent),
		sn("WORK.05", "operate", "run a machine or a process",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleLocation), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fControl, fIntent, fAnimate, fPhysical),
		sn("WORK.06", "manage", "direct an organisation or a process",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), opt(jlir.RoleGoal), opt(jlir.RoleTime), opt(jlir.RoleComitative)},
			fControl, fIntent, fAnimate, fSocial),
		sn("WORK.07", "produce", "bring a product into existence",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleProduct), dfl(jlir.RoleTheme, jlir.RoleSource), opt(jlir.RoleInstrument), opt(jlir.RoleTime)},
			fCreation, fIntent, fAnimate, fTelic),
		sn("WORK.08", "repair", "restore something to working order",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RoleBeneficiary), opt(jlir.RoleInstrument), opt(jlir.RoleTime)},
			fCreation, fControl, fIntent, fAnimate),
		sn("WORK.09", "use", "put a resource to instrumental purpose",
			[]Arg{req(jlir.RoleAgent), req(jlir.RoleTheme), dfl(jlir.RoleGoal, jlir.RolePatient), opt(jlir.RoleInstrument), opt(jlir.RoleTime)},
			fIntent, fControl, fAnimate),
	}
}

// -----------------------------------------------------------------------------
// SLEEP — sleep and rest.
// -----------------------------------------------------------------------------

func sleepSenses() []*Sense {
	return []*Sense{
		sn("SLEEP", "sleep", "suspend activity in a state of recuperation",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fPhysical, fAnimate, fDurative),

		sn("SLEEP.01", "sleep", "be asleep",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fPhysical, fAnimate, fDurative),
		sn("SLEEP.02", "wake", "cease to be asleep",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleTime, jlir.RoleLocation)},
			fChangeState, fPhysical, fAnimate),
		sn("SLEEP.03", "rest", "relax from exertion",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fPhysical, fAnimate, fDurative),
		sn("SLEEP.04", "be_tired", "lack energy, often from exertion",
			[]Arg{req(jlir.RoleExperiencer), opt(jlir.RoleCause), opt(jlir.RoleTime)},
			fPhysical, fAffect, fAnimate, fEvaluation),
		sn("SLEEP.05", "lie_down", "be in a horizontal resting posture",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime)},
			fPhysical, fAnimate),
		sn("SLEEP.06", "get_up", "rise from rest",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleLocation, jlir.RoleTime), opt(jlir.RoleTime)},
			fMotion, fPhysical, fAnimate),
	}
}

// -----------------------------------------------------------------------------
// COPULA — the Japanese copula chain. Japanese has three copulas (だ/です/
// である) with different morphology and politeness; keeping them apart lets
// the projection stage pick the right target form without re-analysing.
// -----------------------------------------------------------------------------

func copulaSenses() []*Sense {
	return []*Sense{
		sn("COPULA", "copula", "predicate an identity or property of the topic",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), opt(jlir.RoleExperiencer), opt(jlir.RoleTime)},
			fCognition, fDeictic),

		sn("COPULA.01", "copula_plain", "だ — plain identity, written or informal speech",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), opt(jlir.RoleExperiencer), opt(jlir.RoleTime)},
			fCognition, fDeictic),
		sn("COPULA.02", "copula_past", "だった — past copula",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), opt(jlir.RoleExperiencer), opt(jlir.RoleTime), opt(jlir.RoleManner)},
			fCognition, fDeictic, fPast),
		sn("COPULA.03", "copula_polite", "です — polite identity",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), opt(jlir.RoleExperiencer), opt(jlir.RoleTime)},
			fCognition, fDeictic, fSocial),
		sn("COPULA.04", "copula_formal", "である — written formal identity",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), opt(jlir.RoleExperiencer), opt(jlir.RoleTime)},
			fCognition, fDeictic, fSocial),
		sn("COPULA.05", "copula_conjectural", "でしょう — conjectured identity",
			[]Arg{req(jlir.RoleTheme), req(jlir.RoleComitative), opt(jlir.RoleExperiencer), opt(jlir.RoleTime)},
			fCognition, fDeictic),
	}
}

// -----------------------------------------------------------------------------
// ARTIFACT — nominal primitives for tools, devices and vehicles. They exist so
// that non-compositional compounds (電動牙刷 "electric toothbrush") and
// terminology memory entries have a real predicate to attach to instead of a
// dangling string.
// -----------------------------------------------------------------------------

func artifactSenses() []*Sense {
	return []*Sense{
		sn("ARTIFACT", "artifact", "a man-made object class",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RolePossessor), opt(jlir.RoleInstrument), opt(jlir.RoleComitative)},
			fPhysical, fPossession),

		sn("ARTIFACT.01", "electric_device", "a device powered by electricity",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleInstrument), opt(jlir.RolePossessor)},
			fPhysical, fControl),
		sn("ARTIFACT.02", "manual_tool", "a tool worked by hand power",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleInstrument), opt(jlir.RolePossessor)},
			fPhysical, fContact),
		sn("ARTIFACT.03", "vehicle", "a device that carries participants",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleInstrument), opt(jlir.RolePossessor)},
			fPhysical, fMotion),
		sn("ARTIFACT.04", "household_appliance", "a device used in the home",
			[]Arg{req(jlir.RoleTheme), opt(jlir.RoleInstrument), opt(jlir.RolePossessor)},
			fPhysical),
		sn("ARTIFACT.05", "measuring_instrument", "an instrument used to determine a value",
			[]Arg{req(jlir.RoleTheme), dfl(jlir.RoleTheme, jlir.RoleProduct), opt(jlir.RoleInstrument)},
			fPhysical, fPerception),
	}
}

func buildSenses() []*Sense {
	out := make([]*Sense, 0, 240)
	for _, f := range []func() []*Sense{
		communicateSenses, moveSenses, runSenses, transferSenses, mentalSenses,
		feelSenses, perceiveSenses, existSenses, haveSenses, compareSenses,
		quantifySenses, modalSenses, changeSenses, consumeSenses, weatherSenses,
		timeSenses, meetSenses, workSenses, sleepSenses, copulaSenses,
		artifactSenses,
	} {
		out = append(out, f()...)
	}
	return out
}
