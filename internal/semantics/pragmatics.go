package semantics

// Pragmatic layer (plan.md §18).
//
// Speech style, politeness, respect and humility are not decoration: they are
// part of the message, and English cannot express most of them
// morphologically. plan.md §5 requires the system to *know* it cannot render
// something rather than to drop it silently, so an honorific construction
// produces an explicit RespectAddressee together with a record that the target
// has no morphology for it.
//
// Sources used here:
//   - です/ます vs だ/である on the matrix or an auxiliary;
//   - honorific and humble verb morphology;
//   - sentence-final particles (ね/よ/ぞ/ぜ/わ/かな/かも/でしょう);
//   - evaluative な-adjectives, which carry a speaker stance rather than a
//     description.

import (
	"sort"
	"strings"

	"github.com/nico2525nn/jev-trans/internal/forest"
	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/lang"
	"github.com/nico2525nn/jev-trans/internal/syntax"
)

// SpeechStyle values. They are named so the UI can render them directly and so
// the target planner can look for a matching construction.
const (
	stylePlain     = "plain"
	stylePolite    = "polite"
	styleHonorific = "honorific"
	styleHumble    = "humble"
	styleUnknown   = "unknown"
)

// politeForms are the copula and auxiliary inflections that make a Japanese
// sentence polite. A form not in this set is not asserted to be plain: an
// absent marker means the style is undetermined, not casual.
var politeForms = map[string]bool{
	"です":     true,
	"ます":     true,
	"ました":    true,
	"ません":    true,
	"ませんでした": true,
	"ましょう":   true,
	"ましょうか":  true,
}

// plainCopulas are the plain copulas. である is written-style rather than
// casual, so it is handled separately below.
var plainCopulas = map[string]bool{"だ": true}

var formalCopulas = map[string]bool{"である": true}

// honorificVerbs are honorific inflections: the speaker is addressing a referent
// respectfully.
var honorificVerbs = map[string]bool{
	"なさる":   true,
	"なさいます": true,
	"なされる":  true,
	"される":   true,
	"してる":   true,
	"なさって":  true,
}

// humbleVerbs lower the speaker rather than raising the addressee.
var humbleVerbs = map[string]bool{
	"申す":    true,
	"申し上げる": true,
	"おる":    true,
	"居る":    true,
	"いたす":   true,
	"伺う":    true,
	"うかがう":  true,
	"まいる":   true,
}

// stanceAdjectives are evaluative な-adjectives: they evaluate rather than
// describe, so they belong to the pragmatic layer and not to the core.
var stanceAdjectives = map[string]bool{
	"嬉しい":   true,
	"楽しい":   true,
	"悲しい":   true,
	"悔しい":   true,
	"懐かしい":  true,
	"恥ずかしい": true,
	"面白い":   true,
}

// particleAttitude maps a sentence-final particle to the attitude it signals.
// The value is what the target sentence has to convey some other way — an
// English tag, an intonation contour, a hedge.
var particleAttitude = map[string]string{
	"ね":    "seeking_agreement",
	"よ":    "asserting_to_listener",
	"ぞ":    "asserting",
	"ぜ":    "emphatic_assertion",
	"わ":    "affective",
	"かな":   "questioning",
	"かも":   "possibility",
	"でしょう": "conjecture",
	"だろう":  "conjecture",
	"だろ":   "conjecture",
	"な":    "exclamation",
	"よね":   "seeking_agreement",
	"わね":   "seeking_agreement",
	"かなあ":  "questioning",
	"だそう":  "conjecture",
}

// hedges are the sentence-final particles that lower certainty.
var hedges = map[string]bool{
	"かな": true, "かも": true, "でしょう": true, "だろう": true, "だろ": true,
}

// assertives are the sentence-final particles that raise assertiveness.
var assertives = map[string]bool{"よ": true, "ぞ": true, "ぜ": true}

// pragmatics fills the pragmatic layer from the clause's morphology.
func (a *analyzer) pragmatics(c *syntax.Clause, ev *jlir.Event) {
	g := a.jb.G
	p := &g.Prag
	matrix := a.b.Morph(c.Matrix)

	styleProv := a.styleProvenance(c)
	if len(styleProv) > 0 {
		p.Prov = append(p.Prov, styleProv...)
	}
	p.SpeechStyle, p.Formality, p.Politeness = a.speechStyle(c, styleProv)

	// --- honorific / humble ----------------------------------------------
	if c.Honorific || hasStem(matrix, honorificVerbs) || hasStemInChain(a, c, honorificVerbs) {
		p.Respect = maxFloat(p.Respect, 0.8)
		p.SpeechStyle = styleHonorific
		p.RespectAddressee = a.respectAddressee(c)
		g.SourceFeat.Honorific = true
		g.SourceFeat.HonorificLevel = "honorific"
		g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "JP.HONORIFIC")
		p.Prov = append(p.Prov, jlir.PredSpan(jlir.OriginMorphological, c.Span, a.b.Source, 0.9,
			"honorific morphology on %q addresses a referent respectfully", a.matrixSurface(c)))
		if p.RespectAddressee == "" {
			p.Prov = append(p.Prov, jlir.PredToken(jlir.OriginLexical, a.matrixSurface(c), 0.6,
				"honorific present but no overt addressee in the clause; recorded rather than guessed"))
		}
	}
	if hasStem(matrix, humbleVerbs) || hasStemInChain(a, c, humbleVerbs) {
		p.Humility = maxFloat(p.Humility, 0.8)
		p.Humidification = true
		p.SpeechStyle = styleHumble
		g.SourceFeat.Humidification = true
		g.SourceFeat.HonorificLevel = "humble"
		g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "JP.HUMBLE")
		p.Prov = append(p.Prov, jlir.PredSpan(jlir.OriginMorphological, c.Span, a.b.Source, 0.9,
			"humble morphology lowers the speaker rather than raising the addressee"))
	}

	// --- sentence-final particles ----------------------------------------
	for _, part := range a.sentenceFinal(c) {
		g.SourceFeat.SentenceFinalParticles = appendUniqueString(g.SourceFeat.SentenceFinalParticles, part)
		p.SentenceFinalParticles = appendUniqueString(p.SentenceFinalParticles, part)
		attitude, ok := particleAttitude[part]
		if !ok {
			continue
		}
		if p.SentenceFinalAttitude == "" {
			p.SentenceFinalAttitude = attitude
		}
		p.Prov = append(p.Prov, jlir.PredToken(jlir.OriginLexical, part, 0.85,
			"sentence-final %q signals %s", part, attitude))
		switch {
		case hedges[part]:
			p.Certainty = clamp01(p.Certainty - 0.2)
		case assertives[part]:
			p.Assertiveness = clamp01(p.Assertiveness + 0.2)
		}
	}
	if len(p.SentenceFinalParticles) > 0 {
		g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "JP.SENTENCE_FINAL")
	}

	// --- evaluative predicates -------------------------------------------
	if c.Evaluative != "" {
		ev.Features = append(ev.Features, jlir.Feature{
			Key: "evaluative_stance", Value: c.Evaluative, Confidence: 0.8,
			Prov: []jlir.Provenance{jlir.PredSpan(jlir.OriginMorphological, c.Span, a.b.Source, 0.8,
				"evaluative な-adjective %q carries a speaker stance", c.Evaluative)},
		})
		if p.EmotionalTone == "" {
			p.EmotionalTone = evaluativeTone(c.Evaluative)
		}
		p.Prov = append(p.Prov, jlir.PredSpan(jlir.OriginMorphological, c.Span, a.b.Source, 0.8,
			"evaluative predicate %q", c.Evaluative))
		g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "JP.EVALUATIVE_NA")
	}
	if hasStem(matrix, stanceAdjectives) || hasStemInChain(a, c, stanceAdjectives) {
		g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "JP.STANCE_ADJECTIVE")
	}

	// plan.md §35 asks the projection stage to budget for what the target cannot
	// express. Respect is the clearest case: English has no morphological
	// honorific, so the constraint solver has to reconstruct it or lose it.
	if p.Respect > 0 && a.src == lang.JA {
		g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "TARGET.CANNOT_EXPRESS_RESPECT")
	}
}

// styleProvenance returns the entries that justify the style claims, so that
// "polite" is always traceable to a specific auxiliary or to the parser's own
// inflection analysis.
func (a *analyzer) styleProvenance(c *syntax.Clause) []jlir.Provenance {
	if c.Politeness != "" || c.Honorific {
		return []jlir.Provenance{jlir.PredSpan(jlir.OriginMorphological, c.Span, a.b.Source, 0.9,
			"clause analysis reports politeness %q honorific=%v", c.Politeness, c.Honorific)}
	}
	for _, id := range a.verbChain(c) {
		m := a.b.Morph(id)
		if m == nil {
			continue
		}
		surface := m.Text(a.b.Source)
		sp := jlir.Span{Start: m.Start, End: m.End}
		if politeForms[surface] || m.Feat("politeness") == "polite" {
			return []jlir.Provenance{jlir.PredSpan(jlir.OriginMorphological, sp, a.b.Source, 0.95,
				"polite auxiliary %q", surface)}
		}
		if formalCopulas[surface] || plainCopulas[surface] || m.Feat("politeness") == "plain" {
			return []jlir.Provenance{jlir.PredSpan(jlir.OriginMorphological, sp, a.b.Source, 0.9,
				"non-polite copula %q", surface)}
		}
	}
	return nil
}

// speechStyle returns the style, the formality score and the politeness score.
// When nothing in the morphology decides, the style is "unknown" and both
// scores stay at zero: guessing plain because no polite marker was found would
// be an invented claim about register.
func (a *analyzer) speechStyle(c *syntax.Clause, prov []jlir.Provenance) (string, float64, float64) {
	switch strings.ToLower(strings.TrimSpace(c.Politeness)) {
	case "polite", "teineigo", "敬語":
		return stylePolite, 0.6, 0.85
	case "plain", "casual", "常体":
		return stylePlain, 0.2, 0.15
	case "formal", "文語":
		return stylePlain, 0.85, 0.5
	case "honorific":
		return styleHonorific, 0.7, 0.9
	case "humble":
		return styleHumble, 0.8, 0.9
	}
	if c.Honorific {
		return styleHonorific, 0.7, 0.9
	}
	if !a.ja {
		// English marks politeness lexically ("please", "could you") rather than
		// morphologically, so nothing is asserted here.
		return styleUnknown, 0, 0
	}
	for _, id := range a.verbChain(c) {
		m := a.b.Morph(id)
		if m == nil {
			continue
		}
		surface := m.Text(a.b.Source)
		switch {
		case politeForms[surface] || m.Feat("politeness") == "polite":
			return stylePolite, 0.6, 0.85
		case formalCopulas[surface]:
			return stylePlain, 0.85, 0.5
		case plainCopulas[surface] || m.Feat("politeness") == "plain":
			return stylePlain, 0.2, 0.15
		}
	}
	_ = prov
	return styleUnknown, 0, 0
}

// respectAddressee returns the entity the honorific is directed at. An honorific
// addresses the interlocutor, which in Japanese is normally the は-phrase or the
// addressee of a speech act. When neither is overt the field stays empty and the
// graph says so, because naming an addressee that the sentence does not contain
// would be an invented referent.
func (a *analyzer) respectAddressee(c *syntax.Clause) string {
	for _, p := range []*syntax.Phrase{c.Topic, c.Subject} {
		if p == nil || p.Zero {
			continue
		}
		// The entity's own surface, not the raw phrase span: the span usually
		// covers the case particle and sometimes the verb, and a respect target
		// has to be a referent, not a string slice.
		if ent := a.entityFor(p, "topic"); ent != nil {
			if name := ent.Name(a.src); name != "" {
				return name
			}
			if name := ent.Alias(a.src); name != "" {
				return name
			}
		}
	}
	keys := make([]string, 0, len(c.Indirect))
	for k := range c.Indirect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != "に" && !strings.EqualFold(k, "to") {
			continue
		}
		if p := c.Indirect[k]; p != nil {
			if ent := a.entityFor(p, "indirect"); ent != nil {
				if name := ent.Alias(a.src); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

// sentenceFinal collects the clause's sentence-final particles, from the parser's
// own list first and from the clause's trailing morphemes as a fallback.
func (a *analyzer) sentenceFinal(c *syntax.Clause) []string {
	out := make([]string, 0, 4)
	for _, id := range c.SentenceFinal {
		if text := a.sentenceFinalParticle(a.b.Text(id)); text != "" {
			out = appendUniqueString(out, text)
		}
	}
	if len(out) > 0 {
		return out
	}
	ids := a.b.SortedMorphIDs()
	for i := len(ids) - 1; i >= 0 && i >= len(ids)-4; i-- {
		m := a.b.Morph(ids[i])
		if m == nil {
			continue
		}
		surface := a.sentenceFinalParticle(m.Text(a.b.Source))
		if surface != "" {
			out = appendUniqueString(out, surface)
		}
	}
	return out
}

// sentenceFinalParticle reduces a surface to the stance particle it actually is,
// or "" when it is not one.
//
// The granularity matters because the field is compared against the target's
// particles. A backend that cannot split です + ね hands over 「ですね」 as one
// morpheme, and recording that string means a source which genuinely carries
// ね is stored under a key nothing else will ever match: the invention check
// looks for ね, does not find it, and would call a faithful translation an
// invented particle. Recording ね instead makes the check mean what it says.
//
// A surface with no known particle inside it records nothing rather than itself.
// Writing ですね into a field named sentence-final particles asserts that the
// clause ends in a stance marker, and ですか has none.
func (a *analyzer) sentenceFinalParticle(surface string) string {
	surface = strings.TrimSpace(surface)
	if surface == "" {
		return ""
	}
	if _, ok := particleAttitude[surface]; ok {
		return surface
	}
	// Longest particle first: か and ね are single characters and けれど is
	// longer, so scanning by descending length keeps けれど from being read as
	// けれど's own trailing よ.
	best := ""
	for p := range particleAttitude {
		if !strings.HasSuffix(surface, p) {
			continue
		}
		// Longest match wins. か is one character and よ is one character, so
		// 「cmakeよ」 has to resolve to よ by exclusion and 「_kwargsよ」 to the
		// longer particle; comparing lengths handles both without an
		// enumeration of particles that happen to end in another particle.
		if len(p) > len(best) {
			best = p
		}
	}
	return best
}

// verbChain lists the morphemes whose inflection determines the style: the
// matrix verb, its auxiliaries and the negation when it carries one.
func (a *analyzer) verbChain(c *syntax.Clause) []string {
	out := make([]string, 0, 5)
	if c.Matrix != "" {
		out = append(out, c.Matrix)
	}
	out = append(out, c.Auxiliaries...)
	if c.Negation != "" {
		out = append(out, c.Negation)
	}
	return out
}

// hasStem reports whether a morpheme's surface, base or lemma is in the table.
func hasStem(m *forest.Morph, table map[string]bool) bool {
	if m == nil {
		return false
	}
	return table[m.Surface] || table[m.Base] || table[m.Lem]
}

// hasStemInChain reports whether any auxiliary or negation is in the table.
func hasStemInChain(a *analyzer, c *syntax.Clause, table map[string]bool) bool {
	for _, id := range c.Auxiliaries {
		if hasStem(a.b.Morph(id), table) {
			return true
		}
	}
	if hasStem(a.b.Morph(c.Negation), table) {
		return true
	}
	return false
}

func evaluativeTone(word string) string {
	switch word {
	case "嬉しい", "楽しい", "面白い":
		return "positive"
	case "悲しい", "悔しい", "恥ずかしい", "懐かしい":
		return "negative_or_mixed"
	}
	return "evaluative"
}

func appendUniqueString(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func maxFloat(x, y float64) float64 {
	if x > y {
		return x
	}
	return y
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
