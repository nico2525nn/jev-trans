package semantics

// The convenience path used by the re-parser and the server.

import (
	"fmt"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/syntax"
)

// Analyze runs the full source pipeline for one sentence and returns its JLIR
// graph: morphological analysis, parsing, then the semantic layer.
//
// It is the path plan.md §40's re-parser closes the loop with — a target string
// is re-analyzed through exactly the same code the source went through, so a
// hallucinated target is detectable rather than merely unlikely.
//
// Like Build it never panics. plan.md §24 requires that a stage failure be
// survivable and observable rather than fatal, and a panic inside the analyzer
// or the parser must not take the translation down with it; the failure is
// reported as an UNPARSABLE graph instead.
//
// It deliberately imports neither internal/plan nor internal/verify: those
// depend on the semantic layer, and that dependency must stay one-way.
func Analyze(text string, src lang.Lang) (g *jlir.Graph) {
	if src == "" {
		src = lang.DetectLang(text)
	}
	defer func() {
		if r := recover(); r != nil {
			g = unparsableGraph(nil, src, fmt.Sprintf("source analysis failed: %v", r))
			g.Source = text
		}
	}()

	var bundle *syntax.Bundle
	switch src {
	case lang.JA:
		bundle = syntax.ParseJA(text, lex.AnalyzeJA(text))
	default:
		bundle = syntax.ParseEN(text, lex.AnalyzeEN(text))
	}
	if bundle == nil {
		bundle = &syntax.Bundle{Lang: src, Source: text, Morphs: map[string]*forest.Morph{}}
	}
	g = Build(bundle, src)
	if g != nil && g.Source == "" {
		g.Source = text
	}
	return g
}
