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
// The rule that matters here: if a generated target sentence cannot be parsed,
// Reparse returns nil. It never repairs, never guesses and never falls back to
// the source analysis, because a guessed JLIR_target would make the verifier
// report "no difference found" for a sentence the system does not actually
// understand.

import (
	"strings"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/semantics"
	"github.com/nico/jev-trans/internal/syntax"
	"github.com/nico/jev-trans/internal/trace"
)

// Reparse runs the full source pipeline — analysis, morphology, parse, semantic
// composition — over an already-realized target string and returns the JLIR
// graph of that string. It returns nil when the string carries no propositional
// content the pipeline can account for, so the caller reports UNPARSABLE.
func Reparse(text string, l lang.Lang) *jlir.Graph {
	return ReparseTraced(nil, text, l)
}

// ReparseTraced is Reparse with the trace spans that make the loop visible in
// the WebUI. The re-parse is the second half of the safety device, so the UI
// must show it running and must show why a candidate was declared unparsable.
func ReparseTraced(rec *trace.Recorder, text string, l lang.Lang) *jlir.Graph {
	var sp *trace.Span
	if rec != nil {
		sp = rec.Open(trace.StageReparse, "target re-parse")
		defer sp.Close()
	}

	graph := build(text, l)

	if rec != nil {
		switch {
		case graph == nil:
			sp.Note("no propositional content recovered; the candidate is UNPARSABLE")
		default:
			sp.Data(graph)
			sp.Count("events", len(graph.Events))
			sp.Count("entities", len(graph.Entities))
			if n := len(graph.Unresolved()); n > 0 {
				sp.Note("%d unresolved node(s) survived analysis", n)
				sp.Count("unresolved", n)
			}
		}
	}
	return graph
}

// build performs the four pipeline stages over text. It is separate from
// ReparseTraced so that the trace policy stays in one place.
func build(text string, l lang.Lang) *jlir.Graph {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if !l.Valid() {
		// The caller is expected to pass an explicit language; guessing here is
		// a convenience for the CLI only, never a silent pipeline decision.
		l = lang.DetectLang(text)
	}

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
