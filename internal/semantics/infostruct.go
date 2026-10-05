package semantics

// Information structure (plan.md §19).
//
// Japanese は and が must not be flattened into an English subject. は marks a
// topic — a participant the sentence is about and the addresser assumes to be
// shared knowledge — while が marks the subject, and a contrastive が narrows
// further to the focus of contrast. The realizer needs all three, because
// "Taro came" and "As for Taro, he came" are different messages rather than
// paraphrases.
//
// Given / new / background are read off the bundle's mention history: an entity
// the sentence mentions for the first time is new, one it has already
// introduced is given, and a topic-marked entity introduced earlier is
// background.

import (
	"strings"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/syntax"
)

// contrastiveConnectives are the connectives that turn a plain が into a
// contrastive one: 「Aは高かったが、Bは安かった」 makes B the focus of contrast.
var contrastiveConnectives = map[string]bool{
	"だが": true, "けれども": true, "けど": true, "でも": true, "しかし": true,
	"それから": false,
	// A lone が counts as the だが connective only when it is *not* the subject
	// marker of the phrase being examined, which morphEndingAt has already ruled
	// out by the time the window is inspected.
	"が":   true,
	"but": true, "however": true, "whereas": true, "while": true,
	"although": true, "though": true, "yet": true, "still": true, "instead": true,
}

// contrastiveAdverbs are the discourse adverbs that license a contrastive が
// without a full connective.
var contrastiveAdverbs = map[string]bool{
	"but": true, "however": true, "instead": true, "rather": true,
	"actually": true, "yet": true, "on the other hand": true,
	"でも": true, "しかし": true, "だが": true, "そもそも": true, "実は": true,
}

// infoTopic records a は-phrase as the topic. It is emphatically not recorded as
// a subject: the two are different relations, and conflating them is the
// specific failure plan.md §19 names.
func (a *analyzer) infoTopic(p *syntax.Phrase) {
	ent := a.entityFor(p, "topic")
	if ent == nil {
		return
	}
	g := a.jb.G
	g.Info.Topic = appendUniqueID(g.Info.Topic, ent.ID)
	g.Info.Marker = "は"
	g.Info.Prov = append(g.Info.Prov, jlir.PredSpan(jlir.OriginSyntactic, p.Span, a.b.Source, 0.95,
		"%q is marked by は and is therefore the topic, not the subject", a.phraseText(p)))
	a.classifyMention(ent, true)
}

// infoSubject records a が-phrase as the focus, and as a contrast when the
// clause sits in a contrastive context.
func (a *analyzer) infoSubject(c *syntax.Clause, p *syntax.Phrase) {
	ent := a.entityFor(p, "subject")
	if ent == nil {
		return
	}
	g := a.jb.G
	g.Info.Focus = appendUniqueID(g.Info.Focus, ent.ID)
	g.Info.Prov = append(g.Info.Prov, jlir.PredSpan(jlir.OriginSyntactic, p.Span, a.b.Source, 0.95,
		"%q is marked by が and is therefore the subject/focus", a.phraseText(p)))
	if a.isContrastive(c, p) {
		a.contrast = true
		g.Info.Contrast = appendUniqueID(g.Info.Contrast, ent.ID)
		g.Info.Marker = "が"
		g.Info.Prov = append(g.Info.Prov, jlir.PredSpan(jlir.OriginSyntactic, p.Span, a.b.Source, 0.9,
			"contrastive が: %q is the focus of contrast", a.phraseText(p)))
		a.jb.G.SourceFeat.Constructions = append(a.jb.G.SourceFeat.Constructions, "JP.CONTRASTIVE_GA")
	}
	a.classifyMention(ent, false)
}

// isContrastive reports whether a が-phrase is contrastive.
//
// Two surface things license it: a contrastive connective immediately before
// the clause (「...だが、花子は」), or a contrastive adverb earlier in the
// sentence. Both are detection rather than inference, so a sentence with
// neither yields no contrast.
//
// A が that is the *subject marker* of this very phrase never licenses
// contrast: 「太郎が来た」 ends in が and is not contrastive.
func (a *analyzer) isContrastive(c *syntax.Clause, p *syntax.Phrase) bool {
	// A clause the parser already labelled adversative is contrastive by the
	// parse itself, which is stronger evidence than any surface scan.
	if c != nil {
		switch c.Conjoin {
		case syntax.ConjoinBut, syntax.ConjoinAlthough, syntax.ConjoinOr:
			return true
		}
	}
	if p.Span.Start <= 0 || p.Span.Start >= len(a.b.Source) {
		return false
	}
	// If the morpheme ending where the phrase starts is the が that marks this
	// phrase, it is the subject marker rather than a connective.
	if m := a.morphEndingAt(p.Span.Start); m != nil && isSubjectMarker(m) {
		return false
	}
	before := strings.TrimRight(a.window(a.b.Source, p.Span.Start, 8), "、。,.! ?")
	if before == "" {
		return false
	}
	for marker, contrastive := range contrastiveConnectives {
		if contrastive && strings.HasSuffix(before, marker) {
			return true
		}
	}
	for _, id := range a.b.SortedMorphIDs() {
		m := a.b.Morph(id)
		if m == nil || m.Start >= p.Span.Start {
			continue
		}
		surface := strings.ToLower(m.Text(a.b.Source))
		if contrastiveAdverbs[surface] || contrastiveAdverbs[strings.ToLower(m.Base)] {
			return true
		}
	}
	return false
}

// isSubjectMarker reports whether a morpheme is the が that marks the following
// noun phrase as the clause subject.
func isSubjectMarker(m *forest.Morph) bool {
	if m == nil {
		return false
	}
	if m.Text("") == "が" {
		return true
	}
	return m.IsTopic() == false && (m.Case() == "ga" || m.Surface == "が")
}

// morphEndingAt returns the morpheme that ends exactly at offset, or nil.
func (a *analyzer) morphEndingAt(offset int) *forest.Morph {
	for _, id := range a.b.SortedMorphIDs() {
		if m := a.b.Morph(id); m != nil && m.End == offset {
			return m
		}
	}
	return nil
}

// window returns up to n bytes of source text immediately before offset.
func (a *analyzer) window(src string, offset, n int) string {
	if offset <= 0 || offset > len(src) {
		return ""
	}
	start := offset - n
	if start < 0 {
		start = 0
	}
	return src[start:offset]
}

// classifyMention fills given / new / background from the mention history.
func (a *analyzer) classifyMention(e *jlir.Entity, topic bool) {
	g := a.jb.G
	known := e.MentionCount > 1
	switch {
	case topic && known:
		g.Info.Background = appendUniqueID(g.Info.Background, e.ID)
	case known:
		g.Info.Given = appendUniqueID(g.Info.Given, e.ID)
	default:
		g.Info.New = appendUniqueID(g.Info.New, e.ID)
	}
	g.Info.Prov = append(g.Info.Prov, jlir.PredSpan(jlir.OriginDiscourse, g.Span, g.Source, 0.6,
		"%s is %s in this sentence", e.ID, givennessWord(topic, known)))
}

func givennessWord(topic, known bool) string {
	switch {
	case topic && known:
		return "background"
	case known:
		return "given"
	}
	return "new"
}

func appendUniqueID(list []jlir.ID, id jlir.ID) []jlir.ID {
	for _, x := range list {
		if x == id {
			return list
		}
	}
	return append(list, id)
}
