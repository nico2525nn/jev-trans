package semantics

// Source linguistic features (plan.md §20) and unknowns (plan.md §25).
//
// Layer 7 of the JLIR holds material a fully neutral interlingua would drop but
// the projection and loss accounting need: which construction was used, what
// tense/aspect morphemes carried it, which sentence-final particle closed it,
// and — crucially — which morphemes could not be resolved at all.
//
// An unknown stays unknown. Nothing here fills a gap with a plausible meaning.

import (
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lexicon"
)

// scanSourceFeatures walks the bundle once and records the layer-7 material that
// is visible at sentence level rather than clause level.
func (a *analyzer) scanSourceFeatures() {
	g := a.jb.G
	g.SourceFeat.Lang = a.src

	if a.b == nil {
		return
	}
	for _, id := range a.b.SortedMorphIDs() {
		m := a.b.Morph(id)
		if m == nil {
			continue
		}
		if m.Unknown {
			g.SourceFeat.Unknowns = append(g.SourceFeat.Unknowns, jlir.UnknownUnit{
				Surface: m.Text(a.b.Source),
				Reason:  unknownReason(m),
			})
		}
		if g.SourceFeat.TenseMorpheme == "" && m.Feat("tense") != "" {
			g.SourceFeat.TenseMorpheme = m.Surface
		}
		if g.SourceFeat.AspectMorpheme == "" && m.Feat("aspect") != "" {
			g.SourceFeat.AspectMorpheme = m.Surface
		}
		if g.SourceFeat.PolitenessMorpheme == "" && m.Feat("politeness") != "" {
			g.SourceFeat.PolitenessMorpheme = m.Surface
		}
		if m.IsTopic() {
			g.SourceFeat.TopicMarker = true
			g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "JP.TOPIC_HA")
		}
		if c := m.Case(); c != "" && c != "wa" {
			if g.SourceFeat.CaseMarkers == nil {
				g.SourceFeat.CaseMarkers = map[string]string{}
			}
			if _, ok := g.SourceFeat.CaseMarkers[c]; !ok {
				g.SourceFeat.CaseMarkers[c] = ""
			}
		}
	}

	// Every string the analyzer already admitted it could not resolve is
	// carried over verbatim rather than being re-guessed here.
	for _, u := range a.b.Unknowns {
		g.SourceFeat.Unknowns = append(g.SourceFeat.Unknowns, jlir.UnknownUnit{
			Surface: u, Reason: "reported by the morphological analyzer",
		})
	}

	a.scanIdioms()
}

// unknownReason states *why* a morpheme is unknown, because "unknown" alone
// tells the user nothing about whether the word can be recovered later.
func unknownReason(m *forest.Morph) string {
	if m.Dict {
		return "dictionary entry did not decompose further"
	}
	if len(m.Alternatives) > 0 {
		return "several dictionary entries share this surface; the segmentation is unresolved"
	}
	if len(m.Notes) > 0 {
		return m.Notes[0]
	}
	return "no dictionary entry and no applicable decomposition rule"
}

// scanIdioms detects multiword expressions before any literal predicate is
// committed (plan.md §49). An idiom is a hypothesis, not a verdict: both the
// idiomatic and the literal reading stay in the graph so the decision layer can
// choose with the whole sentence in view.
//
// Metaphors (plan.md §50) get the same treatment: a surface reading and an
// abstract reading side by side rather than one silently substituted for the
// other.
func (a *analyzer) scanIdioms() {
	g := a.jb.G
	if len(a.b.Morphs) == 0 {
		return
	}
	ids := a.b.SortedMorphIDs()
	for _, window := range idiomWindows(ids, 4) {
		surface := a.windowSurface(window)
		if len(surface) < 2 {
			continue
		}
		id, ok := a.lex.Idiom(surface)
		if !ok || id == nil {
			continue
		}
		span := a.spanOf(window)
		prov := jlir.PredSpan(jlir.OriginLexical, span, a.b.Source, 0.7,
			"multiword expression %q has a conventional reading", surface)
		hyp := jlir.IdiomHypothesis{
			ID:             id.ID,
			Surface:        surface,
			Reading:        id.SenseID,
			LiteralReading: id.Literal,
			Span:           &span,
			Confidence:     clamp01(id.Formal),
			Prov:           []jlir.Provenance{prov},
		}
		if hyp.ID == "" {
			hyp.ID = "IDIOM:" + surface
		}
		if hyp.LiteralReading == "" {
			// The literal reading stays a sibling hypothesis; an empty one is
			// better than a fabricated composition.
			hyp.LiteralReading = "literal composition of " + surface
		}
		g.SourceFeat.Idioms = append(g.SourceFeat.Idioms, hyp)
		if id.Metaphor {
			g.SourceFeat.Metaphors = append(g.SourceFeat.Metaphors, jlir.MetaphorHypothesis{
				Surface:    surface,
				Target:     id.Literal,
				Abstract:   id.SenseID,
				Confidence: clamp01(id.Formal),
			})
		}
	}
}

// idiomWindows enumerates the contiguous morpheme windows worth looking up,
// longest first: a four-morpheme idiom should be found before its three-morpheme
// prefix is mistaken for a different expression.
func idiomWindows(ids []string, max int) [][]string {
	out := make([][]string, 0, len(ids)*max)
	for size := max; size >= 2; size-- {
		for i := 0; i+size <= len(ids); i++ {
			out = append(out, ids[i:i+size])
		}
	}
	return out
}

// windowSurface concatenates a morpheme window the way the source writes it:
// Japanese has no spaces, English has them.
func (a *analyzer) windowSurface(window []string) string {
	var sb strings.Builder
	for i, id := range window {
		m := a.b.Morph(id)
		if m == nil {
			continue
		}
		if i > 0 && a.src == lang.EN {
			sb.WriteByte(' ')
		}
		sb.WriteString(m.Text(a.b.Source))
	}
	return sb.String()
}

// spanOf returns the source span covering a morpheme window.
func (a *analyzer) spanOf(window []string) jlir.Span {
	span := jlir.Span{}
	for _, id := range window {
		m := a.b.Morph(id)
		if m == nil {
			continue
		}
		if span.End == 0 && span.Start == 0 {
			span = jlir.Span{Start: m.Start, End: m.End}
			continue
		}
		if m.Start < span.Start {
			span.Start = m.Start
		}
		if m.End > span.End {
			span.End = m.End
		}
	}
	return span
}

// senseIDs returns the sorted ontology sense ids a surface can denote.
func senseIDs(l *lexicon.Lexicon, surface string, ja bool) []string {
	if l == nil {
		return nil
	}
	var hits []lexicon.SenseHit
	if ja {
		hits = l.SensesJP(surface)
		if len(hits) == 0 {
			hits = l.SensesEN(surface)
		}
	} else {
		hits = l.SensesEN(surface)
		if len(hits) == 0 {
			hits = l.SensesJP(surface)
		}
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.SenseID != "" {
			out = appendUniqueString(out, h.SenseID)
		}
	}
	sort.Strings(out)
	return out
}
