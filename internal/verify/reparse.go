package verify

// Semantic re-parsing, plan.md §40.
//
// This is the step that closes the loop:
//
//	JLIR_source -> generation -> target sentence -> parser -> JLIR_target
//
// and then "JLIR_source ↔ JLIR_target" is compared. Without it the pipeline
// would be checking its own intentions against itself, which is exactly the
// failure mode plan.md §40 names as the core safety device.
//
// Two rules matter here.
//
// First, if a generated target sentence cannot be parsed, Reparse returns nil.
// It never repairs, never guesses and never falls back to the source analysis,
// because a guessed JLIR_target would make the verifier report "no difference
// found" for a sentence the system does not actually understand.
//
// Second, this file owns the analyze-and-compose path outright. It used to sit
// next to a line-for-line restatement in internal/semantics, and two copies of
// the same four stages is how they drift apart: one of them grew a recover and
// the other did not. There is one now, and it is here.

import (
	"strings"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/semantics"
	"github.com/nico/jev-trans/internal/syntax"
)

// Reparse runs the full source pipeline — morphological analysis, parsing,
// semantic composition — over an already-realized target string and returns the
// JLIR graph of that string. It returns nil when the string carries no
// propositional content the pipeline can account for, and when any stage
// panics, so the caller reports UNPARSABLE in both cases.
//
// The caller owns the trace span for this stage. This function is deliberately
// trace-free so a re-parse never opens a second span behind the caller's back,
// the same discipline RankWith follows.
func Reparse(text string, l lang.Lang) (g *jlir.Graph) {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if !l.Valid() {
		// The caller is expected to pass an explicit language; guessing here is
		// a convenience for the CLI only, never a silent pipeline decision.
		l = lang.DetectLang(text)
	}

	// plan.md §24: a stage failure must be survivable and observable rather
	// than fatal. The analyzer is the least defended code in the system — it
	// walks a lattice built from arbitrary text — and a panic inside it must
	// cost this candidate its verification, not the whole translation. The
	// recovered value is nil, which is exactly what "we could not re-parse this
	// sentence" means to every caller, and the pipeline turns it into an
	// UNPARSABLE candidate with a warning attached.
	defer func() {
		if r := recover(); r != nil {
			g = nil
		}
	}()

	bundle := analyzeAndParse(text, l)
	if bundle == nil || len(bundle.Clauses) == 0 {
		return nil
	}

	graph := compose(bundle, l)
	if graph == nil {
		return nil
	}
	if len(graph.Events) == 0 && len(graph.Entities) == 0 {
		// A parse that produced nothing to compare is not a successful re-parse.
		return nil
	}
	if graph.Lang == "" {
		graph.Lang = l
	}
	if graph.Source == "" {
		graph.Source = text
	}
	return graph
}

// analyzeAndParse runs the morphological analyzer and the parser for the given
// language, treating both as total functions: a nil result or a bundle with no
// clause means the string is unparsable, and the caller turns that into nil.
func analyzeAndParse(text string, l lang.Lang) *syntax.Bundle {
	switch l {
	case lang.JA:
		return syntax.ParseJA(text, lex.AnalyzeJA(text))
	case lang.EN:
		return syntax.ParseEN(text, lex.AnalyzeEN(text))
	}
	return nil
}

// compose hands the parsed bundle to the semantic layer, which builds the JLIR
// graph. It is a named seam rather than an inline call so that the exact
// semantic entry point is asserted in one place: the pipeline contract pins it
// as semantics.Build(bundle *syntax.Bundle, src lang.Lang) *jlir.Graph.
func compose(b *syntax.Bundle, l lang.Lang) *jlir.Graph {
	if b == nil {
		return nil
	}
	return semantics.Build(b, l)
}
