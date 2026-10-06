package semantics

// Normalization of surface argument structure into semantic roles (plan.md §9).
//
// Japanese case particles and English prepositions are *markers*, not roles:
// に after a transfer verb is a recipient, に after a motion verb is a goal, and
// English "to" covers both plus the benefactive. Nothing here may guess. The
// resolution order is therefore fixed and every step is recorded:
//
//  1. keep only the roles the predicate's role frame admits (ontology.Sense);
//  2. if exactly one survives, the marker is unambiguous;
//  3. otherwise ask the predicate family for evidence (TRANSFER ⇒ recipient,
//     MOVE ⇒ goal, HAVE ⇒ possessor, ...);
//  4. otherwise fall back to the ontology's declared default for the slot
//     (ontology.Arg.Default) — a declared fallback, not a guess;
//  5. otherwise leave the argument unbound and say so in the clause notes.
//
// Step 5 is deliberate: an unbound argument is visible and recoverable, a
// silent guess is neither.

import (
	"strings"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/ontology"
)

// jaMarkerRoles maps a Japanese case particle to the semantic roles it can
// express, most likely first. の is present because a の-phrase is folded into
// its head noun as a possessive relation; it never becomes a clause argument of
// its own unless the parser kept it in Indirect.
var jaMarkerRoles = map[string][]string{
	"が":    {jlir.RoleAgent, jlir.RoleExperiencer, jlir.RolePatient, jlir.RoleTheme, jlir.RoleStimulus},
	"ga":   {jlir.RoleAgent, jlir.RoleExperiencer, jlir.RolePatient, jlir.RoleTheme, jlir.RoleStimulus},
	"wo":   {jlir.RoleTheme, jlir.RolePatient, jlir.RoleProduct, jlir.RoleStimulus},
	"を":    {jlir.RoleTheme, jlir.RolePatient, jlir.RoleProduct, jlir.RoleStimulus},
	"ni":   {jlir.RoleRecipient, jlir.RoleGoal, jlir.RolePossessor, jlir.RoleLocation, jlir.RoleTime, jlir.RoleBeneficiary},
	"に":    {jlir.RoleRecipient, jlir.RoleGoal, jlir.RolePossessor, jlir.RoleLocation, jlir.RoleTime, jlir.RoleBeneficiary},
	"he":   {jlir.RoleGoal, jlir.RoleLocation, jlir.RoleRecipient},
	"e":    {jlir.RoleGoal, jlir.RoleLocation, jlir.RoleRecipient},
	"へ":    {jlir.RoleGoal, jlir.RoleLocation, jlir.RoleRecipient},
	"de":   {jlir.RoleLocation, jlir.RoleInstrument, jlir.RoleComitative, jlir.RoleTime, jlir.RoleManner},
	"で":    {jlir.RoleLocation, jlir.RoleInstrument, jlir.RoleComitative, jlir.RoleTime, jlir.RoleManner},
	"to":   {jlir.RoleComitative, jlir.RoleTheme, jlir.RoleRecipient},
	"と":    {jlir.RoleComitative, jlir.RoleTheme, jlir.RoleRecipient},
	"kara": {jlir.RoleSource, jlir.RoleTime, jlir.RoleCause},
	"から":   {jlir.RoleSource, jlir.RoleTime, jlir.RoleCause},
	"yori": {jlir.RoleSource, jlir.RoleTime, jlir.RoleCause},
	"より":   {jlir.RoleSource, jlir.RoleTime, jlir.RoleCause},
	"made": {jlir.RoleGoal, jlir.RoleTime, jlir.RoleLocation},
	"まで":   {jlir.RoleGoal, jlir.RoleTime, jlir.RoleLocation},
	"no":   {jlir.RolePossessor},
	"の":    {jlir.RolePossessor},
	"yue":  {jlir.RoleCause},
	"sen":  {jlir.RoleCause, jlir.RoleSource},
}

// enMarkerRoles maps an English preposition to the roles it can express.
var enMarkerRoles = map[string][]string{
	"to":      {jlir.RoleRecipient, jlir.RoleGoal, jlir.RoleBeneficiary, jlir.RoleLocation},
	"at":      {jlir.RoleLocation, jlir.RoleTime},
	"in":      {jlir.RoleLocation, jlir.RoleTime},
	"on":      {jlir.RoleLocation, jlir.RoleTime},
	"into":    {jlir.RoleGoal, jlir.RoleLocation},
	"onto":    {jlir.RoleGoal, jlir.RoleLocation},
	"for":     {jlir.RoleBeneficiary, jlir.RoleRecipient, jlir.RoleGoal, jlir.RoleTime},
	"with":    {jlir.RoleInstrument, jlir.RoleComitative},
	"from":    {jlir.RoleSource, jlir.RoleLocation, jlir.RoleTime},
	"by":      {jlir.RoleAgent, jlir.RoleInstrument},
	"of":      {jlir.RolePossessor},
	"about":   {jlir.RoleTheme, jlir.RoleCause},
	"during":  {jlir.RoleTime},
	"before":  {jlir.RoleTime},
	"after":   {jlir.RoleTime},
	"without": {jlir.RoleInstrument, jlir.RoleComitative},
	"near":    {jlir.RoleLocation, jlir.RoleGoal},
	"under":   {jlir.RoleLocation, jlir.RoleGoal},
	"over":    {jlir.RoleLocation, jlir.RoleGoal},
	"than":    {jlir.RoleTheme},
}

// familyEvidence gives the role a predicate family prefers for a marker. It is
// the only content disambiguation in the table: the marker says *that* an
// oblique argument is present, the family says *which* role it fills.
var familyEvidence = map[string]map[string][]string{
	"TRANSFER": {
		"ni": {jlir.RoleRecipient}, "に": {jlir.RoleRecipient},
		"to": {jlir.RoleRecipient}, "he": {jlir.RoleGoal}, "e": {jlir.RoleGoal}, "へ": {jlir.RoleGoal},
		"kara": {jlir.RoleSource}, "から": {jlir.RoleSource}, "from": {jlir.RoleSource},
		"no": {jlir.RolePossessor}, "の": {jlir.RolePossessor}, "of": {jlir.RolePossessor},
	},
	"MOVE": {
		"ni": {jlir.RoleGoal}, "に": {jlir.RoleGoal},
		"he": {jlir.RoleGoal}, "e": {jlir.RoleGoal}, "へ": {jlir.RoleGoal}, "to": {jlir.RoleGoal},
		"kara": {jlir.RoleSource}, "から": {jlir.RoleSource}, "from": {jlir.RoleSource},
		"made": {jlir.RoleGoal}, "まで": {jlir.RoleGoal},
	},
	"RUN": {
		"ni": {jlir.RoleGoal}, "に": {jlir.RoleGoal},
		"he": {jlir.RoleGoal}, "e": {jlir.RoleGoal}, "へ": {jlir.RoleGoal}, "to": {jlir.RoleGoal},
	},
	"HAVE": {
		"ni": {jlir.RolePossessor}, "に": {jlir.RolePossessor}, "of": {jlir.RolePossessor},
		"no": {jlir.RolePossessor}, "の": {jlir.RolePossessor},
		"de": {jlir.RoleLocation}, "で": {jlir.RoleLocation}, "in": {jlir.RoleLocation},
	},
	"COMMUNICATE": {
		"ni": {jlir.RoleRecipient}, "に": {jlir.RoleRecipient}, "to": {jlir.RoleRecipient},
		"for":  {jlir.RoleBeneficiary},
		"with": {jlir.RoleInstrument, jlir.RoleComitative},
		"no":   {jlir.RoleTheme}, "の": {jlir.RoleTheme},
	},
	"MENTAL": {
		"ni": {jlir.RoleCause}, "に": {jlir.RoleCause}, "for": {jlir.RoleCause},
		"about": {jlir.RoleTheme},
	},
	"FEEL": {
		"ni": {jlir.RoleCause}, "に": {jlir.RoleCause}, "for": {jlir.RoleCause},
		"about": {jlir.RoleTheme},
	},
	"PERCEIVE": {
		"ni": {jlir.RoleRecipient}, "に": {jlir.RoleRecipient}, "to": {jlir.RoleRecipient},
		"from": {jlir.RoleSource}, "から": {jlir.RoleSource},
		"de": {jlir.RoleLocation}, "で": {jlir.RoleLocation},
	},
	"CONSUME": {
		"de": {jlir.RoleLocation, jlir.RoleInstrument}, "で": {jlir.RoleLocation, jlir.RoleInstrument},
		"ni": {jlir.RoleGoal}, "に": {jlir.RoleGoal},
	},
	"TIME": {
		"ni": {jlir.RoleTime}, "に": {jlir.RoleTime}, "at": {jlir.RoleTime}, "on": {jlir.RoleTime},
		"kara": {jlir.RoleSource}, "から": {jlir.RoleSource}, "from": {jlir.RoleSource},
		"made": {jlir.RoleTime}, "まで": {jlir.RoleTime},
	},
	"MEET": {
		"ni": {jlir.RoleComitative}, "に": {jlir.RoleComitative}, "to": {jlir.RoleComitative},
		"with": {jlir.RoleComitative},
	},
	"WORK": {
		"de": {jlir.RoleLocation}, "で": {jlir.RoleLocation}, "at": {jlir.RoleLocation},
		"ni": {jlir.RoleGoal}, "に": {jlir.RoleGoal}, "for": {jlir.RoleBeneficiary},
	},
	"MODAL": {
		"ni": {jlir.RoleGoal}, "に": {jlir.RoleGoal}, "to": {jlir.RoleGoal},
		"for": {jlir.RoleBeneficiary},
	},
	"CHANGE": {
		"ni": {jlir.RoleGoal}, "に": {jlir.RoleGoal}, "to": {jlir.RoleGoal},
		"from": {jlir.RoleSource}, "kara": {jlir.RoleSource}, "から": {jlir.RoleSource},
	},
	"COMPARE": {
		"ni": {jlir.RoleGoal}, "に": {jlir.RoleGoal}, "to": {jlir.RoleGoal},
		"de": {jlir.RoleManner}, "で": {jlir.RoleManner}, "with": {jlir.RoleManner},
		"than": {jlir.RoleTheme},
	},
}

// subjectRoles is the preference order for the が-subject slot. が is not an
// ambiguous marker: it marks the subject, and the only question is which
// subject-like role of the frame it fills. The frame order breaks that tie and
// the choice is recorded so the decision layer can still revisit it.
var subjectRoles = []string{
	jlir.RoleAgent, jlir.RoleExperiencer, jlir.RolePatient, jlir.RoleTheme, jlir.RoleStimulus,
}

// objectRoles is the preference order for the を-object slot.
var objectRoles = []string{
	jlir.RoleTheme, jlir.RolePatient, jlir.RoleProduct, jlir.RoleStimulus,
}

// roleResolution is the outcome of normalizing one surface marker.
type roleResolution struct {
	Role string
	// Conf is deliberately below the oracle's skip threshold when the choice
	// was made by a tie-break rather than by the morphology, so plan.md §34's
	// information-gain scheduler still asks about it.
	Conf float64
	// Note explains the choice and goes into the clause notes.
	Note string
	// Bound is false when nothing in the frame and no declared default could
	// decide, in which case the argument stays out of the event's role map.
	Bound bool
}

// markerCandidates returns the candidate roles for a surface marker.
func markerCandidates(marker string, ja bool) []string {
	key := strings.ToLower(strings.TrimSpace(marker))
	if !ja {
		return enMarkerRoles[key]
	}
	if r, ok := jaMarkerRoles[marker]; ok {
		return r
	}
	return jaMarkerRoles[key]
}

// familyOf returns the predicate family of a sense id ("TRANSFER.01" ⇒
// "TRANSFER"). A sense with no dot is its own family.
func familyOf(id string) string {
	if i := strings.Index(id, "."); i > 0 {
		return id[:i]
	}
	return id
}

// resolveSlot normalizes one surface slot against a predicate's role frame.
// marker is the surface marker itself (に, "to", ...) and is what the family
// evidence table is keyed by; candidates is the role list the marker could
// express; slot is "subject", "object" or "indirect" and selects the
// preference list. Confidences are deliberately below 1 because every step here
// is a normalization decision the decision layer may revisit.
func resolveSlot(s *ontology.Sense, marker string, candidates []string, slot string) roleResolution {
	if s == nil || len(candidates) == 0 {
		return roleResolution{
			Note: "no predicate frame or no candidate roles; argument left unbound",
		}
	}
	accepted := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if s.Accepts(c) {
			accepted = append(accepted, c)
		}
	}
	switch len(accepted) {
	case 0:
		return roleResolution{
			Note: "predicate " + s.ID + " admits none of the roles this marker could fill; argument left unbound",
		}
	case 1:
		return roleResolution{
			Role:  accepted[0],
			Conf:  0.92,
			Bound: true,
			Note:  "role " + accepted[0] + " is the only role " + s.ID + " admits here",
		}
	}

	// Step 3: predicate family evidence. The family table is keyed by the
	// surface marker, so it is consulted with the marker rather than with the
	// candidate roles.
	if ev, ok := familyEvidence[familyOf(s.ID)]; ok {
		for _, want := range evidenceFor(ev, marker) {
			for _, cand := range accepted {
				if cand == want {
					return roleResolution{
						Role:  want,
						Conf:  0.9,
						Bound: true,
						Note:  "predicate family " + familyOf(s.ID) + " makes this marker " + want,
					}
				}
			}
		}
	}

	// The subject and object slots are not ambiguous markers: the morphology
	// says which slot it is, and only the role label varies. The preference
	// order decides, and the decision stays visible.
	// "topic" belongs here too: は marks a topic, but when no が-phrase exists
	// the topic is the subject (「太郎は本を渡した」), and the same frame
	// decides which subject-like role it fills. Leaving "topic" out sent the
	// argument down the ambiguous-marker path, where no evidence exists, and
	// the agent was silently left unbound.
	if slot == "subject" || slot == "object" || slot == "topic" {
		want := subjectRoles
		if slot == "object" {
			want = objectRoles
		}
		for _, w := range want {
			for _, cand := range accepted {
				if cand == w {
					return roleResolution{
						Role:  w,
						Conf:  0.8,
						Bound: true,
						Note:  "role " + w + " chosen from the " + slot + " preference order; frame also admits " + joinRoles(accepted),
					}
				}
			}
		}
	}

	// Step 4: the ontology's declared fallback for the slot.
	for _, arg := range s.Args {
		if arg.Default == "" {
			continue
		}
		for _, cand := range accepted {
			if cand == arg.Role {
				return roleResolution{
					Role:  arg.Default,
					Conf:  0.55,
					Bound: true,
					Note: "ambiguous marker on " + s.ID + " (" + joinRoles(accepted) +
						"); fell back to the declared ontology default " + arg.Default,
				}
			}
		}
	}

	// Step 5: nothing decides it. Leaving the argument unbound is honest; a
	// guess here would silently change the translation.
	return roleResolution{
		Note: "ambiguous marker on " + s.ID + " (" + joinRoles(accepted) +
			") with no family evidence and no declared default; argument left unbound for the decision layer",
	}
}

// evidenceFor returns the roles the family table prefers for one surface marker,
// in the table's own order. It returns nil when the family says nothing about
// this marker, which is what sends the resolution on to the ontology default.
func evidenceFor(ev map[string][]string, marker string) []string {
	key := strings.ToLower(strings.TrimSpace(marker))
	if r, ok := ev[key]; ok {
		return r
	}
	if r, ok := ev[marker]; ok {
		return r
	}
	// Japanese and romanized markers name the same particle.
	romaji := map[string]string{"に": "ni", "へ": "e", "で": "de", "と": "to",
		"から": "kara", "まで": "made", "の": "no", "が": "ga", "を": "wo"}[marker]
	if r, ok := ev[romaji]; ok {
		return r
	}
	return nil
}

func joinRoles(roles []string) string {
	return strings.Join(roles, "/")
}
