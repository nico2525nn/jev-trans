// Package semantics turns a parse into a JLIR graph.
//
// It is the seam between the two halves of the system: everything upstream
// produces a *syntax.Bundle, everything downstream consumes a *jlir.Graph. The
// package exists because that translation is not mechanical — a case particle
// is not a role, a zero subject is not a missing argument, a topic is not a
// subject, and てしまう is not "eat + past" (plan.md §§9, 12, 15, 19).
//
// The rules this package obeys, in order of importance:
//
//   - Nothing is invented. Gender, number, person and animacy are asserted only
//     with provenance pointing at an overt token or an explicit lexical feature
//     (plan.md §§2, 14, 21, 22). The canonical failure mode — turning 太郎 into
//     "the female teacher" — is structurally impossible here.
//   - Nothing collapses early. Unresolved scope, zero referents and competing
//     predicate senses leave the graph as explicit Distributions (plan.md §§16, 17).
//   - Every stage writes to the trace. A stage that records nothing is
//     considered unfinished.
//   - Output is deterministic: anything iterated is sorted before it can reach
//     JSON, so the UI diff between two readings is meaningful.
package semantics

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lexicon"
	"github.com/nico/jev-trans/internal/ontology"
	"github.com/nico/jev-trans/internal/syntax"
	"github.com/nico/jev-trans/internal/trace"
)

// ambient lets the pipeline route the sub-spans this package opens into its own
// trace tree. BuildForest is normally already wrapped in a StageSemantic span by
// the caller; when it is not, UseRecorder keeps the stage visible anyway.
var ambient struct {
	mu  sync.RWMutex
	rec *trace.Recorder
}

// UseRecorder installs the recorder that Build and BuildForest write their spans
// to, and returns the previous one. Passing nil detaches.
func UseRecorder(r *trace.Recorder) *trace.Recorder {
	ambient.mu.Lock()
	defer ambient.mu.Unlock()
	prev := ambient.rec
	ambient.rec = r
	return prev
}

func ambientRecorder() *trace.Recorder {
	ambient.mu.RLock()
	defer ambient.mu.RUnlock()
	return ambient.rec
}

// unknownPredicate is the predicate id used when the lexicon has no entry. It is
// deliberately conspicuous: the target planner sees an unimplementable predicate
// instead of a confident wrong one, and an UnknownUnit records why.
const unknownPredicate = "UNKNOWN.VERB"

// unknownReferent is the escape option every zero distribution carries. The
// pipeline may hand it to the oracle as a real alternative (plan.md §15 lists
// "unknown" alongside the discourse entities).
const unknownReferent = "UNKNOWN"

// senseAltNote renders competing predicate senses the way the decision layer
// reads them back: "SENSE=weight;SENSE=weight", no spaces, so splitting on ";"
// and "=" yields the candidate set.
func senseAltNote(alt map[string]float64) string {
	if len(alt) < 2 {
		return ""
	}
	keys := make([]string, 0, len(alt))
	for k := range alt {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.3f", k, alt[k]))
	}
	return strings.Join(parts, ";")
}

// Build turns one parse into one JLIR graph.
//
// It must never panic. A bundle it cannot interpret yields a graph with
// UNPARSABLE semantics and a recorded note, not a crash: plan.md §24 requires
// parser failure to be survivable, and the layer above it inherits the duty.
func Build(b *syntax.Bundle, src lang.Lang) *jlir.Graph {
	g, _ := buildWith(b, src, nil, nil)
	return g
}

// buildWith is Build with the two overrides BuildForest needs: an alternative
// clause to substitute, and a predicate sense to force for a clause.
func buildWith(b *syntax.Bundle, src lang.Lang, override map[string]*syntax.Clause, forced map[string]string) (*jlir.Graph, string) {
	rec := ambientRecorder()
	var sp *trace.Span
	if rec != nil {
		sp = rec.Open(trace.StageJLIR, "jlir core")
		defer sp.Close()
	}

	g, origin := safeBuild(b, src, override, forced)
	if sp != nil {
		sp.Data(g)
		if g != nil {
			sp.Detail("%s", g.Describe())
			sp.Count("entities", len(g.Entities))
			sp.Count("events", len(g.Events))
			sp.Count("scopes", len(g.Scopes))
			for _, n := range g.Notes {
				sp.Note("%s", n)
			}
			if open := g.Unresolved(); len(open) > 0 {
				sp.Status(trace.StatusWarn)
				sp.Label("open", strings.Join(open, ","))
			}
		}
	}
	return g, origin
}

// safeBuild contains every panic the analysis could raise and reports the
// failure as an UNPARSABLE graph instead of propagating it.
func safeBuild(b *syntax.Bundle, src lang.Lang, override map[string]*syntax.Clause, forced map[string]string) (g *jlir.Graph, origin string) {
	defer func() {
		if r := recover(); r != nil {
			g = unparsableGraph(b, src, fmt.Sprintf("semantic analysis failed: %v", r))
			origin = "recovery"
		}
	}()
	return analyze(b, src, override, forced)
}

// unparsableGraph is the empty-but-valid graph returned when analysis cannot
// proceed. It is never nil, because every downstream stage must be able to read
// a graph without a nil check.
func unparsableGraph(b *syntax.Bundle, src lang.Lang, why string) *jlir.Graph {
	text := ""
	if b != nil {
		text = b.Source
	}
	if src == "" {
		src = lang.DetectLang(text)
	}
	jb := jlir.NewBuilder(text, src, jlir.Span{Start: 0, End: len(text)})
	g := jb.Build()
	g.Weight = 0
	g.Notes = append(g.Notes, "UNPARSABLE: "+why)
	g.SourceFeat.Lang = src
	g.SourceFeat.Constructions = append(g.SourceFeat.Constructions, "UNPARSABLE")
	if b != nil {
		for _, u := range b.Unknowns {
			g.SourceFeat.Unknowns = append(g.SourceFeat.Unknowns, jlir.UnknownUnit{
				Surface: u, Reason: "morpheme could not be resolved",
			})
		}
	}
	return g
}

// analyzer carries the state of one analysis pass.
type analyzer struct {
	b    *syntax.Bundle
	src  lang.Lang
	ja   bool
	jb   *jlir.Builder
	onto *ontology.Registry
	// lex is the curated inventory. prov is the chain that actually answers, and
	// they are both kept: the curated table is the authority for what it knows,
	// while an external provider fills the gaps without overruling it.
	lex  *lexicon.Lexicon
	prov *lexicon.Set
	span *trace.Span

	// ents maps a coreference key to the entity it denotes, so that a repeated
	// mention of 太郎 is one referent with two aliases rather than two referents.
	ents map[string]*jlir.Entity
	// order is discourse order of first mention; zero anaphora draws its
	// candidate antecedents from it.
	order []jlir.ID
	// contrast records that a contrastive marker was seen, which turns a plain
	// が into a focus contrast rather than a neutral subject (plan.md §19).
	contrast bool
	// phraseSpans records which source spans have already been turned into
	// entities, so several passes over one phrase do not inflate the mention
	// count and turn a first mention into a "given".
	phraseSpans map[string]bool
}

// phraseKey identifies a phrase by its source span, falling back to the text
// when the parser supplied no span.
func phraseKey(sp jlir.Span) string {
	return fmt.Sprintf("%d-%d", sp.Start, sp.End)
}

func phraseSeen(set map[string]bool, sp jlir.Span) bool {
	return set[phraseKey(sp)]
}

func analyze(b *syntax.Bundle, src lang.Lang, override map[string]*syntax.Clause, forced map[string]string) (*jlir.Graph, string) {
	if b == nil {
		return unparsableGraph(nil, src, "no parse bundle"), "empty"
	}
	if src == "" {
		src = b.Lang
	}
	if src == "" {
		src = lang.DetectLang(b.Source)
	}
	jb := jlir.NewBuilder(b.Source, src, jlir.Span{Start: 0, End: len(b.Source)})
	a := &analyzer{
		b:           b,
		src:         src,
		ja:          src == lang.JA,
		jb:          jb,
		onto:        ontology.Default(),
		lex:         lexicon.Default(),
		prov:        lexicon.ProviderFromEnv(),
		ents:        map[string]*jlir.Entity{},
		phraseSpans: map[string]bool{},
	}
	if rec := ambientRecorder(); rec != nil {
		a.span = rec.Open(trace.StageSemantic, "core semantics: "+describeClauses(b))
		defer a.span.Close()
	}

	a.scanSourceFeatures()

	// Clause order is discourse order and the first clause is the matrix, so the
	// graph's ClauseOrder can be trusted for reordering downstream.
	for _, c := range b.Clauses {
		if c == nil {
			continue
		}
		cl := c
		if alt, ok := override[c.ID]; ok && alt != nil {
			cl = alt
		}
		a.clause(cl, forced)
	}

	g := jb.Build()
	a.finalize(g)

	if a.span != nil {
		a.span.Data(g)
		a.span.Detail("%s", g.Describe())
		a.span.Count("entities", len(g.Entities))
		a.span.Count("events", len(g.Events))
		a.span.Count("open positions", len(g.Unresolved()))
		for _, n := range g.Notes {
			a.span.Note("%s", n)
		}
	}
	origin := ""
	if len(g.Events) > 0 {
		origin = g.Events[0].Predicate
	}
	return g, origin
}

func describeClauses(b *syntax.Bundle) string {
	if b == nil {
		return "no bundle"
	}
	if len(b.Clauses) == 1 {
		return "1 clause"
	}
	return fmt.Sprintf("%d clauses", len(b.Clauses))
}

// clause maps one syntax.Clause onto a jlir.Event and everything hanging off
// it, and returns that event so a caller can link a complement or a relative
// clause to it.
func (a *analyzer) clause(c *syntax.Clause, forced map[string]string) *jlir.Event {
	if c == nil {
		return nil
	}
	morph := a.b.Morph(c.Matrix)
	token := ""
	if morph != nil {
		token = morph.Text(a.b.Source)
	}

	predID, sense := a.predicate(c, forced)
	ev := jlir.NewEvent(a.jb.NewEvent(), predID)
	ev.Prov = append(ev.Prov, jlir.PredSpan(jlir.OriginLexical, c.Span, a.b.Source, 0.9,
		"predicate %s from %q", predID, token))
	a.applySenseFeatures(ev, sense)

	if alt := a.senseAlternatives(c, predID); len(alt) > 1 {
		// OriginLexical is what the decision layer looks for when it asks which
		// sense is meant; the note *is* the candidate set.
		note := senseAltNote(alt)
		ev.Prov = append(ev.Prov, jlir.PredToken(jlir.OriginLexical, token, 0.5, "%s", note))
		ev.SetFeature(jlir.Feature{
			Key: "competing_senses", Value: alt, Confidence: 0.5,
			Prov: []jlir.Provenance{jlir.PredToken(jlir.OriginLexical, token, 0.5, "%s", note)},
		})
	}

	a.morphology(c, ev)
	a.arguments(c, sense, ev)
	a.scopeFor(c, ev)
	a.pragmatics(c, ev)

	a.jb.AddEvent(ev)
	a.jb.G.ClauseOrder = append(a.jb.G.ClauseOrder, ev.ID)

	// A complement is a proposition in its own right: it becomes its own event
	// and is linked rather than flattened, so it keeps its own tense.
	if c.Complement != nil {
		if sub := a.clause(c.Complement, forced); sub != nil {
			a.linkComplement(ev, sub, c)
		}
	}
	// A relative clause inside a noun phrase is analysed too: dropping it would
	// lose content that the English target has to express.
	if c.Topic != nil && c.Topic.Relative != nil {
		if sub := a.clause(c.Topic.Relative, forced); sub != nil {
			ev.Features = append(ev.Features, jlir.Feature{
				Key: "topic_relative", Value: string(sub.ID), Confidence: 0.8,
				Prov: []jlir.Provenance{jlir.PredSpan(jlir.OriginSyntactic, c.Span, a.b.Source, 0.8,
					"relative clause %s modifying the topic", sub.ID)},
			})
		}
	}
	return ev
}

func (a *analyzer) linkComplement(ev, sub *jlir.Event, c *syntax.Clause) {
	prov := []jlir.Provenance{jlir.PredSpan(jlir.OriginSyntactic, c.Span, a.b.Source, 0.85,
		"clausal complement %s of %s", sub.ID, ev.ID)}
	ev.SetFeature(jlir.Feature{Key: "complement", Value: string(sub.ID), Confidence: 0.85, Prov: prov})
	sub.SetFeature(jlir.Feature{Key: "complement_of", Value: string(ev.ID), Confidence: 0.85, Prov: prov})
	switch c.Conjoin {
	case syntax.ConjoinBecause:
		sub.Causation = jlir.CausationCause
		ev.SetFeature(jlir.Feature{Key: "cause", Value: string(sub.ID), Confidence: 0.8, Prov: prov})
	case syntax.ConjoinPurpose:
		sub.Causation = jlir.CausationPurpose
		ev.SetFeature(jlir.Feature{Key: "purpose", Value: string(sub.ID), Confidence: 0.8, Prov: prov})
	case syntax.ConjoinAlthough, syntax.ConjoinBut:
		ev.SetFeature(jlir.Feature{Key: "concession", Value: string(sub.ID), Confidence: 0.8, Prov: prov})
	case syntax.ConjoinBefore:
		sub.TemporalRelation = "BEFORE:" + string(ev.ID)
	case syntax.ConjoinAfter:
		sub.TemporalRelation = "AFTER:" + string(ev.ID)
	case syntax.ConjoinWhen:
		sub.TemporalRelation = "WHEN:" + string(ev.ID)
	default:
		sub.TemporalRelation = "UNDER:" + string(ev.ID)
	}
}

// predicate resolves the clause's matrix verb to an ontology sense.
//
// The lookup is lemma-first: the lexicon stores 渡す, not 渡した. When nothing
// resolves, the event still exists but carries a conspicuous UNKNOWN.VERB
// predicate and an UnknownUnit, because an invented predicate is worse than an
// admitted gap (plan.md §25).
func (a *analyzer) predicate(c *syntax.Clause, forced map[string]string) (string, *ontology.Sense) {
	if forced != nil {
		if id, ok := forced[c.ID]; ok && id != "" {
			if s, ok := a.onto.Sense(id); ok {
				a.declarePredicate(s)
				return id, s
			}
		}
	}
	if id, sense := a.lookup(c); id != "" {
		return id, sense
	}
	// An い-adjective can be the clause predicate on its own: 「この道は古い。」
	// has no verb at all, and Japanese uses that construction constantly. The
	// JLIR has a COPULA family precisely for it, and the adjective supplies the
	// theme and the property being attributed.
	//
	// Without this the clause produced no event, the unknown-unit record got an
	// EMPTY surface (because matrixSurface returned ""), and the sentence died
	// with a diagnostic that pointed nowhere. On real prose this was the
	// single largest silent failure.
	// A copula auxiliary is the clause predicate too. Sudachi splits ですね into
	// です + ね, so the matrix morpheme arrives as an auxiliary rather than as
	// the single dictionary entry the builtin analyser produced; without this
	// the clause lost its predicate entirely.
	if cop := a.matrixCopula(c); cop != "" {
		for _, hit := range a.sensesJP(cop) {
			if !strings.HasPrefix(hit.SenseID, "COPULA.") {
				continue
			}
			sense, ok := a.onto.Sense(hit.SenseID)
			if !ok {
				sense = a.onto.MustSense(hit.SenseID)
			}
			a.declarePredicate(sense)
			return hit.SenseID, sense
		}
		id := a.copulaSenseFor(c)
		sense, _ := a.onto.Sense(id)
		a.declarePredicate(sense)
		return id, sense
	}
	if adj := a.matrixAdjective(c); adj != "" {
		id := a.copulaSenseFor(c)
		sense, _ := a.onto.Sense(id)
		a.declarePredicate(sense)
		c.Notes = append(c.Notes,
			"predicate is the い-adjective "+adj+", realized through the copula sense "+id)
		a.jb.G.Notes = append(a.jb.G.Notes, "adjective predicate "+adj+" -> "+id)
		return id, sense
	}
	tok := a.matrixSurface(c)
	a.jb.G.SourceFeat.Unknowns = append(a.jb.G.SourceFeat.Unknowns, jlir.UnknownUnit{
		Surface: tok,
		Reason:  "predicate is not in the lexicon",
	})
	a.note(c, "predicate %q unresolved; carried as %s rather than guessed", tok, unknownPredicate)
	return unknownPredicate, a.onto.MustSense(unknownPredicate)
}

// lookup tries the matrix morph's lemma, then its base form, then its surface.
func (a *analyzer) lookup(c *syntax.Clause) (string, *ontology.Sense) {
	m := a.b.Morph(c.Matrix)
	keys := make([]string, 0, 4)
	if m != nil {
		keys = append(keys, m.Lem, m.Base, m.Surface)
	}
	keys = append(keys, a.b.Text(c.Matrix))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		for _, hit := range a.senseHits(k) {
			if hit.SenseID == "" {
				continue
			}
			s, ok := a.onto.Sense(hit.SenseID)
			if !ok {
				continue
			}
			a.declarePredicate(s)
			return hit.SenseID, s
		}
	}
	return "", nil
}

// senseHits queries the lexicon, preferring the source language table and
// falling back to the other one, because lemmas are often shared.
func (a *analyzer) senseHits(surface string) []lexicon.SenseHit {
	if a.ja {
		if hits := a.sensesJP(surface); len(hits) > 0 {
			return hits
		}
		return a.sensesEN(surface)
	}
	if hits := a.sensesEN(surface); len(hits) > 0 {
		return hits
	}
	return a.sensesJP(surface)
}

// sensesJP and sensesEN ask the provider chain, which answers from the curated
// table first and from an external inventory only where the table is silent.
// Going through the chain rather than the table is what lets coverage grow
// without the core knowing where the knowledge came from.
func (a *analyzer) sensesJP(surface string) []lexicon.SenseHit {
	if a.prov != nil {
		return a.prov.SensesJP(surface)
	}
	if a.lex == nil {
		return nil
	}
	return a.lex.SensesJP(surface)
}

func (a *analyzer) sensesEN(surface string) []lexicon.SenseHit {
	if a.prov != nil {
		return a.prov.SensesEN(surface)
	}
	if a.lex == nil {
		return nil
	}
	return a.lex.SensesEN(surface)
}

// senseAlternatives returns the full weighted candidate set for the predicate,
// which is what plan.md §58 keeps open rather than collapsing.
func (a *analyzer) senseAlternatives(c *syntax.Clause, chosen string) map[string]float64 {
	out := map[string]float64{}
	m := a.b.Morph(c.Matrix)
	keys := make([]string, 0, 3)
	if m != nil {
		keys = append(keys, m.Lem, m.Base, m.Surface)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		for _, hit := range a.senseHits(k) {
			if hit.SenseID == "" || seen["id:"+hit.SenseID] {
				continue
			}
			seen["id:"+hit.SenseID] = true
			w := hit.Weight
			if w <= 0 {
				w = 0.05
			}
			out[hit.SenseID] += w
		}
	}
	if chosen != "" {
		if _, ok := out[chosen]; !ok {
			out[chosen] = 1
		}
	}
	return out
}

// declarePredicate records the ontology sense in the graph so the realizer and
// the UI do not have to re-resolve it.
func (a *analyzer) declarePredicate(s *ontology.Sense) {
	if s == nil || a.jb.G.Predicate(s.ID) != nil {
		return
	}
	specs := make([]jlir.RoleSpec, 0, len(s.Args))
	for _, arg := range s.Args {
		specs = append(specs, jlir.RoleSpec{Role: arg.Role, Required: arg.Required})
	}
	a.jb.AddPredicate(&jlir.Predicate{
		ID:            s.ID,
		Name:          s.Name,
		Gloss:         s.Gloss,
		Args:          specs,
		Features:      s.Features,
		SubPredicates: a.onto.Families(s.ID),
	})
}

// applySenseFeatures copies the ontology sense's constraints onto the event as
// attributed features. This is what plan.md §11 asks for: a predicate is a
// semantic primitive plus constraints, and the constraints are what the target
// projection needs in order to choose between constructions. Without it, 行く
// reaches the planner with no direction and every directional construction is
// rejected as unsatisfiable.
//
// The provenance origin is the ontology, not the source text, so a constraint
// that comes from here can never be mistaken for something the sentence said.
func (a *analyzer) applySenseFeatures(ev *jlir.Event, s *ontology.Sense) {
	if ev == nil || s == nil || len(s.Features) == 0 {
		return
	}
	for k, v := range s.Features {
		if jlir.HasFeature(ev.Features, k) {
			continue
		}
		ev.Features = append(ev.Features, jlir.Feature{
			Key:   k,
			Value: v,
			Prov: []jlir.Provenance{{
				Origin:     jlir.OriginOntology,
				Confidence: 0.9,
				Note:       "predicate constraint from ontology sense " + s.ID,
			}},
		})
	}
}

func (a *analyzer) matrixSurface(c *syntax.Clause) string {
	if m := a.b.Morph(c.Matrix); m != nil {
		return m.Text(a.b.Source)
	}
	return a.b.Text(c.Matrix)
}

// note appends a diagnostic to the clause and to the graph.
func (a *analyzer) note(c *syntax.Clause, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if c != nil {
		c.Notes = append(c.Notes, msg)
	}
	a.jb.G.Notes = append(a.jb.G.Notes, msg)
	if a.span != nil {
		a.span.Note("%s", msg)
	}
}

// finalize normalizes the graph: deterministic ordering and the note that
// records what stayed open.
func (a *analyzer) finalize(g *jlir.Graph) {
	g.SourceFeat.Lang = a.src
	sort.SliceStable(g.SourceFeat.Unknowns, func(i, j int) bool {
		if g.SourceFeat.Unknowns[i].Surface != g.SourceFeat.Unknowns[j].Surface {
			return g.SourceFeat.Unknowns[i].Surface < g.SourceFeat.Unknowns[j].Surface
		}
		return g.SourceFeat.Unknowns[i].Reason < g.SourceFeat.Unknowns[j].Reason
	})
	sort.SliceStable(g.SourceFeat.Idioms, func(i, j int) bool {
		if g.SourceFeat.Idioms[i].Surface != g.SourceFeat.Idioms[j].Surface {
			return g.SourceFeat.Idioms[i].Surface < g.SourceFeat.Idioms[j].Surface
		}
		return g.SourceFeat.Idioms[i].Reading < g.SourceFeat.Idioms[j].Reading
	})
	sort.SliceStable(g.SourceFeat.Metaphors, func(i, j int) bool {
		if g.SourceFeat.Metaphors[i].Surface != g.SourceFeat.Metaphors[j].Surface {
			return g.SourceFeat.Metaphors[i].Surface < g.SourceFeat.Metaphors[j].Surface
		}
		return g.SourceFeat.Metaphors[i].Target < g.SourceFeat.Metaphors[j].Target
	})
	sort.Strings(g.SourceFeat.SentenceFinalParticles)
	sort.Strings(g.Prag.SentenceFinalParticles)
	sort.Strings(g.SourceFeat.Constructions)
	g.Notes = dedupeStrings(g.Notes)
	if g.Weight == 0 {
		g.Weight = 1
	}
	if len(g.Events) == 0 && len(g.Entities) == 0 {
		g.Notes = append(g.Notes,
			"no clause or noun phrase could be interpreted; the graph is empty by design rather than by failure")
	}
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	cp := append([]string(nil), in...)
	sort.Strings(cp)
	out := make([]string, 0, len(cp))
	prev := ""
	for i, s := range cp {
		if i > 0 && s == prev {
			continue
		}
		out = append(out, s)
		prev = s
	}
	return out
}

// --- temporal / event layer (plan.md §12) ---------------------------------

// morphology projects the clause's recovered inflection onto the event. Every
// value is either taken from the analysis or left UNKNOWN; nothing is defaulted
// into existence.
func (a *analyzer) morphology(c *syntax.Clause, ev *jlir.Event) {
	m := a.b.Morph(c.Matrix)
	prov := func(origin jlir.Origin, format string, args ...any) []jlir.Provenance {
		return []jlir.Provenance{jlir.PredSpan(origin, c.Span, a.b.Source, 0.9, format, args...)}
	}

	if ev.Tense, _ = a.tense(c, m); ev.Tense != jlir.TenseUnknown {
		ev.Prov = append(ev.Prov, prov(jlir.OriginMorphological, "tense %s recovered from inflection", ev.Tense)...)
	}
	ev.Polarity = a.polarity(c, m)
	if ev.Polarity != jlir.PolarityUnknown {
		ev.Prov = append(ev.Prov, prov(jlir.OriginMorphological, "polarity %s", ev.Polarity)...)
	}
	if c.Aspect != "" {
		ev.Aspect = normAspect(c.Aspect)
		ev.Prov = append(ev.Prov, prov(jlir.OriginMorphological, "aspect %s", ev.Aspect)...)
	}
	// てしまう carries completion AND speaker evaluation. plan.md §12 refuses to
	// collapse it into a bare past, so the attitude is its own feature.
	if c.Completion != "" {
		ev.Completion = normCompletion(c.Completion)
		ev.Features = append(ev.Features, jlir.Feature{
			Key: "completion_attitude", Value: completionAttitude(ev.Completion), Confidence: 0.75,
			Prov: prov(jlir.OriginMorphological, "てしまう: completed action carrying speaker evaluation"),
		})
		a.jb.G.SourceFeat.Constructions = append(a.jb.G.SourceFeat.Constructions, "JP.TE_SHIMAU")
	}
	if c.Modality != "" {
		ev.Modality = normModality(c.Modality)
		ev.Prov = append(ev.Prov, prov(jlir.OriginMorphological, "modality %s", ev.Modality)...)
	}
	if c.Mood != "" {
		ev.Mood = c.Mood
		ev.Prov = append(ev.Prov, prov(jlir.OriginMorphological, "mood %s", c.Mood)...)
	}
	// An unresolved tense is carried on the event as an open reading rather than as
	// a flag saying "ambiguous". 「I read a book.」 is present-or-past and the
	// evidence does not decide; saying so with a distribution leaves the choice
	// where plan.md §13 wants it, in the scope graph the decision layer reads.
	if m != nil && m.Feat("tense_readings") != "" && ev.Tense == jlir.TenseUnknown {
		opts := strings.Split(m.Feat("tense_readings"), ",")
		if len(opts) > 1 {
			ev.Features = append(ev.Features, jlir.Feature{
				Key: "tense_readings", Value: opts, Confidence: 0.4,
				Prov: prov(jlir.OriginMorphological,
					"%s is spelled identically in the present and the past; the subject did not settle it",
					m.Text(a.b.Source)),
			})
		}
	}
	if c.Causation != "" {
		ev.Causation = c.Causation
	}
	if c.Voice != "" && c.Voice != "active" {
		ev.Features = append(ev.Features, jlir.Feature{
			Key: "voice", Value: c.Voice, Confidence: 0.8,
			Prov: prov(jlir.OriginMorphological, "voice %s", c.Voice),
		})
		a.jb.G.SourceFeat.Constructions = append(a.jb.G.SourceFeat.Constructions, "JP.PASSIVE")
	}
	if c.Evaluative != "" {
		ev.Features = append(ev.Features, jlir.Feature{
			Key: "evaluative", Value: c.Evaluative, Confidence: 0.8,
			Prov: prov(jlir.OriginMorphological, "evaluative predicate %s", c.Evaluative),
		})
		a.jb.G.SourceFeat.Constructions = append(a.jb.G.SourceFeat.Constructions, "JP.EVALUATIVE_NA")
	}
	if c.Quantifier != "" {
		ev.Features = append(ev.Features, jlir.Feature{
			Key: "quantifier", Value: c.Quantifier, Confidence: 0.9,
			Prov: prov(jlir.OriginMorphological, "quantifier %s", c.Quantifier),
		})
	}
}

// tense recovers the clause tense from the predicate and the auxiliaries that
// hang off it.
//
// The auxiliaries matter because a real analyser splits them off: 読んだ is
// 読ん + た, and the bare stem carries no tense at all. Reading tense from the
// matrix morpheme alone is why 「渡した」 came out as "gave" and 「読んだ」 as
// "reads". Japanese tense lives in the auxiliary, not in the verb's own form.
func (a *analyzer) tense(c *syntax.Clause, m *forest.Morph) (string, string) {
	if t := normTense(c.Tense); t != jlir.TenseUnknown {
		return t, "clause analysis"
	}
	// The clause's own auxiliaries first: た and ました are unambiguous, while
	// a stem's features may only describe the form it happens to take.
	for _, id := range c.Auxiliaries {
		am := a.b.Morph(id)
		if am == nil {
			continue
		}
		if t := normTense(am.Feat("tense")); t != jlir.TenseUnknown {
			return t, "auxiliary " + am.Text(a.b.Source)
		}
	}
	if m != nil {
		if t := normTense(m.Feat("tense")); t != jlir.TenseUnknown {
			return t, "morpheme features"
		}
	}
	return jlir.TenseUnknown, ""
}

func (a *analyzer) polarity(c *syntax.Clause, m *forest.Morph) string {
	switch strings.ToLower(strings.TrimSpace(c.Polarity)) {
	case "negative", "neg":
		return jlir.PolarityNegative
	case "positive", "pos":
		return jlir.PolarityPositive
	}
	if c.Negation != "" {
		return jlir.PolarityNegative
	}
	if m != nil && strings.EqualFold(m.Feat("polarity"), "negative") {
		return jlir.PolarityNegative
	}
	return jlir.PolarityUnknown
}

func normTense(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "past", "preterite", "非過去", "完了":
		return jlir.TensePast
	case "present", "now", "現在":
		return jlir.TensePresent
	case "future", "未来":
		return jlir.TenseFuture
	}
	return jlir.TenseUnknown
}

func normAspect(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "progressive", "durative", "continuous", "進行":
		return jlir.AspectProgressive
	case "perfect", "完了":
		return jlir.AspectPerfect
	case "inceptive", "開始":
		return jlir.AspectInceptive
	case "momentary", "瞬間":
		return jlir.AspectMomentary
	}
	return ""
}

func normCompletion(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "complete", "completed", "完了":
		return jlir.CompletionCompleted
	case "incomplete", "未完了":
		return jlir.CompletionIncomplete
	}
	return ""
}

// completionAttitude names what てしまう adds beyond completion. It is kept
// separate from the tense because the speaker's evaluation, not the event's
// temporal profile, is what makes 「食べてしまった」 different from 「食べた」
// (plan.md §12, and §18 lists it as a pragmatic signal).
func completionAttitude(v string) string {
	if v == jlir.CompletionCompleted {
		return "completion_with_speaker_evaluation"
	}
	return "incompleteness"
}

func normModality(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "oblig", "obligation", "obligation/must":
		return jlir.ModalityOblig
	case "should", "advised":
		return jlir.ModalityShould
	case "may", "permission", "permitted":
		return jlir.ModalityMay
	case "must", "requirement", "required":
		return jlir.ModalityMust
	}
	return jlir.ModalityNone
}

func normNumber(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "singular", "sg":
		return jlir.NumberSingular
	case "plural", "pl":
		return jlir.NumberPlural
	case "mass":
		return jlir.NumberMass
	}
	return jlir.NumberUnknown
}

// --- argument structure (plan.md §9) ---------------------------------------

// arguments normalizes the clause's surface slots into semantic roles and binds
// the entities. roles.go holds the resolution rules.
func (a *analyzer) arguments(c *syntax.Clause, sense *ontology.Sense, ev *jlir.Event) {
	ja := a.ja

	if p := c.Topic; p != nil {
		a.infoTopic(p)
		// A は-phrase is a topic, and only becomes an argument when nothing else
		// can take the subject role (plan.md §19).
		// A zero が-subject counts as "nothing else": in 「私は行きます」 the
		// topic is the subject, and the parser records the omitted が as a
		// zero phrase rather than as nil.
		if c.Subject == nil || c.Subject.Zero {
			a.bindSlot(c, ev, sense, p, subjectRoles, "topic", ja)
		}
	}
	if p := c.Subject; p != nil && !p.Zero {
		a.bindSlot(c, ev, sense, p, subjectRoles, "subject", ja)
		a.infoSubject(c, p)
	}
	if p := c.Object; p != nil {
		a.bindSlot(c, ev, sense, p, objectRoles, "object", ja)
	}

	keys := make([]string, 0, len(c.Indirect))
	for k := range c.Indirect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := c.Indirect[k]
		if p == nil {
			continue
		}
		marker := k
		if p.Case != "" {
			marker = p.Case
		}
		a.bindSlot(c, ev, sense, p, markerCandidates(marker, ja), "indirect", ja)
	}

	// Japanese drops the subject. When the predicate needs one, the missing
	// argument is a zero anaphor with a real referent distribution (plan.md §15),
	// never a silently absent role.
	//
	// The test has to be `nil or Zero`, not `nil`. The parser does not leave an
	// omitted subject as an absent field: it plans an explicit zero phrase so
	// that the omission is visible in the trace, and this analyzer used to test
	// for absence only. The two layers therefore disagreed about what an
	// omitted subject looks like, and every sentence whose subject Japanese
	// dropped finished with an event that had no arguments at all.
	if ja && (c.Subject == nil || c.Subject.Zero) {
		a.zeroSubject(c, sense, ev)
	}
}

// bindSlot normalizes one surface phrase and attaches the resulting entity.
func (a *analyzer) bindSlot(c *syntax.Clause, ev *jlir.Event, sense *ontology.Sense,
	p *syntax.Phrase, candidates []string, slot string, ja bool) {
	ent := a.entityFor(p, slot)
	if ent == nil {
		return
	}
	if len(candidates) == 0 {
		a.note(c, "%s %q: surface marker has no role candidates; kept as an entity but not bound",
			slot, a.phraseText(p))
		return
	}
	marker := p.Case
	if marker == "" {
		marker = slot
	}
	res := resolveSlot(sense, marker, candidates, slot)
	if !res.Bound {
		a.note(c, "%s %q: %s", slot, a.phraseText(p), res.Note)
		return
	}
	prov := []jlir.Provenance{jlir.PredSpan(jlir.OriginSyntactic, p.Span, a.b.Source, res.Conf,
		"%s %q → %s: %s", slot, a.phraseText(p), res.Role, res.Note)}
	// Two phrases can normalize onto the same role. Overwriting would silently
	// drop one, so the second is left unbound and the conflict is reported.
	if prev, taken := ev.Args[res.Role]; taken && prev.Value != ent.ID {
		a.note(c, "%s %q would also be %s, already filled by %s; left unbound",
			slot, a.phraseText(p), res.Role, prev.Value)
		return
	}
	ev.Args[res.Role] = jlir.Arg{Value: ent.ID, Confidence: res.Conf, Prov: prov}
	ev.Features = append(ev.Features, jlir.Feature{
		Key: "surface_role:" + res.Role, Value: marker, Confidence: res.Conf, Prov: prov,
	})
	if ja && marker != "" && slot == "indirect" {
		if a.jb.G.SourceFeat.CaseMarkers == nil {
			a.jb.G.SourceFeat.CaseMarkers = map[string]string{}
		}
		a.jb.G.SourceFeat.CaseMarkers[marker] = res.Role
	}
	a.note(c, "%s %q → %s (%.2f)", slot, a.phraseText(p), res.Role, res.Conf)
}

// zeroSubject synthesizes the argument Japanese left unexpressed.
func (a *analyzer) zeroSubject(c *syntax.Clause, sense *ontology.Sense, ev *jlir.Event) {
	if sense == nil || sense.ID == unknownPredicate {
		return
	}
	role := ""
	for _, want := range subjectRoles {
		if sense.Accepts(want) {
			role = want
			break
		}
	}
	if role == "" {
		return
	}
	// An overt argument may already have filled the slot a naive
	// subject-preference search picks — 「ご飯を食べてしまった」 has no overt
	// subject, but 食べる's theme is bound to を. Creating a zero anaphor here
	// would overwrite it and lose the object.
	if prev, taken := ev.Args[role]; taken {
		a.note(c, "no overt subject, but %s is already filled by %s; no zero anaphor created", role, prev.Value)
		return
	}
	z := a.zeroEntity(c.Span)
	if z == nil {
		return
	}
	prov := []jlir.Provenance{jlir.PredSpan(jlir.OriginSyntactic, c.Span, a.b.Source, 0.8,
		"subject not expressed in Japanese; zero anaphor for %s", role)}
	ev.Args[role] = jlir.Arg{Value: z.ID, Confidence: 0.5, Prov: prov}
	a.jb.G.SourceFeat.ExplicitZeroSubject = true
	a.jb.G.SourceFeat.Constructions = append(a.jb.G.SourceFeat.Constructions, "JP.ZERO_SUBJECT")
	a.note(c, "subject unexpressed; created zero anaphor %s for %s over %d candidate referents",
		z.ID, role, len(z.Referent.Options))
}

// isSurfacePlusMarker reports whether longer is just surface followed by a case
// particle or a suffix honorific, which contributes no new name.
func isSurfacePlusMarker(longer, surface string) bool {
	if !strings.HasPrefix(longer, surface) {
		return false
	}
	rest := longer[len(surface):]
	if rest == "" {
		return true
	}
	for _, marker := range caseMarkersAndSuffixes {
		if rest == marker {
			return true
		}
	}
	return false
}

// caseMarkersAndSuffixes are the strings that follow a noun in a phrase span
// without being part of the referent's name.
var caseMarkersAndSuffixes = []string{
	"が", "を", "に", "へ", "で", "と", "から", "まで", "の", "や", "など",
	"は", "も", "ね", "よ",
}

func (a *analyzer) phraseText(p *syntax.Phrase) string {
	if p == nil {
		return ""
	}
	if t := p.Span.Text(a.b.Source); t != "" {
		return t
	}
	return a.b.Text(p.Head)
}

// --- referential layer (plan.md §14) --------------------------------------

// pronounGender is the only source of gender in the whole system: an overt
// pronoun whose referent the language itself genders. Nothing is inferred from a
// name, a common noun, a profession or a sentence type.
var pronounGender = map[string]string{
	"彼":  jlir.GenderMale,
	"彼氏": jlir.GenderMale,
	"彼ら": jlir.GenderMale,
	"姉":  jlir.GenderFemale,
	"妹":  jlir.GenderFemale,
	"彼女": jlir.GenderFemale,

	"he":      jlir.GenderMale,
	"him":     jlir.GenderMale,
	"his":     jlir.GenderMale,
	"himself": jlir.GenderMale,
	"she":     jlir.GenderFemale,
	"her":     jlir.GenderFemale,
	"hers":    jlir.GenderFemale,
	"herself": jlir.GenderFemale,
	"it":      jlir.GenderNeuter,
	"its":     jlir.GenderNeuter,
	"itself":  jlir.GenderNeuter,
}

// pronounPerson records grammatical person for the overt pronouns that carry it.
var pronounPerson = map[string]int{
	"私": 1, "わたし": 1, "僕": 1, "俺": 1, "あたし": 1,
	"i": 1, "me": 1, "my": 1, "myself": 1,
	"あなた": 2, "君": 2, "きみ": 2,
	"you": 2, "your": 2, "yourself": 2,
	"彼": 3, "彼女": 3, "彼氏": 3, "あの人": 3, "その人": 3, "人": 3,
	"we": 3, "us": 3, "our": 3,
	"they": 3, "them": 3, "their": 3, "those": 3,
}

// validGender normalizes a lexical gender feature, returning "" when it carries
// no actual gender claim.
func validGender(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case jlir.GenderMale:
		return jlir.GenderMale
	case jlir.GenderFemale:
		return jlir.GenderFemale
	case jlir.GenderNeuter:
		return jlir.GenderNeuter
	}
	return ""
}

// homophonousPronouns are pronouns that also function as demonstratives, so the
// gender they appear to assert is less certain and the confidence says so.
var homophonousPronouns = map[string]bool{"彼": true, "彼女": true, "彼氏": true}

// entityFor returns (creating if needed) the entity a phrase denotes.
func (a *analyzer) entityFor(p *syntax.Phrase, slot string) *jlir.Entity {
	if p == nil {
		return nil
	}
	if p.Zero || (p.Head == "" && p.Span.End <= p.Span.Start) {
		return a.zeroEntity(p.Span)
	}
	text := a.phraseText(p)
	key := corefKey(a.b, p)
	// Several passes legitimately ask for the same phrase (the topic is read
	// once for information structure and once for argument structure). Only a
	// genuinely new span counts as a re-mention, otherwise the first mention of
	// an entity would be recorded as "given".
	if e, ok := a.ents[key]; ok {
		if !phraseSeen(a.phraseSpans, p.Span) {
			a.phraseSpans[phraseKey(p.Span)] = true
			a.rememberSpan(e, p, text)
		}
		return e
	}
	a.phraseSpans[phraseKey(p.Span)] = true

	m := a.b.Morph(p.Head)
	typ := jlir.TypeUnknown
	if m != nil {
		if t := m.Feat("entity_type"); t != "" {
			typ = t
		} else if t := m.Feat("type"); t != "" {
			typ = t
		}
	}
	if typ == jlir.TypeUnknown && p.Pronoun {
		typ = jlir.TypeHuman
	}
	e := jlir.NewEntityValue(a.jb.NewEntity(), typ)
	e.Prov = append(e.Prov, jlir.PredSpan(jlir.OriginLexical, p.Span, a.b.Source, 1.0,
		"entity introduced by the %s phrase %q", slot, text))
	e.Identity = key
	e.MentionCount = 1
	e.Salience = 1
	e.Proper = !p.Pronoun && m != nil && m.POS == forest.POSProper
	if e.Proper {
		a.jb.G.NamedEntities[e.Identity] = jlir.NamedEntity{
			Canonical: text,
			Forms:     map[string]string{string(a.src): text},
			Type:      typ,
		}
	}

	surface := text
	if p.Head != "" {
		if h := a.b.Text(p.Head); h != "" {
			surface = h
		}
	}
	kind := "alias"
	switch {
	case p.Pronoun:
		kind = "pronoun"
	case e.Proper:
		kind = "name"
	}
	sp := p.Span
	e.Aliases = append(e.Aliases, jlir.Alias{Surface: surface, Lang: a.src, Span: &sp, Kind: kind})
	// A phrase span usually covers the case particle too, so the raw span text
	// is "本を" where the referent's surface is "本". Only a span text that adds
	// something other than the marker is worth keeping as an alias; otherwise
	// the realizer would pick up a particle that is not part of the name.
	if text != "" && surface != text && !isSurfacePlusMarker(text, surface) {
		e.Aliases = append(e.Aliases, jlir.Alias{Surface: text, Lang: a.src, Span: &sp, Kind: "alias"})
	}
	if p.Honorific != "" {
		sp := p.Span
		e.Aliases = append(e.Aliases, jlir.Alias{Surface: p.Honorific, Lang: a.src, Span: &sp, Kind: "title"})
	}
	if p.Demonstrative != "" {
		sp := p.Span
		e.Aliases = append(e.Aliases, jlir.Alias{Surface: p.Demonstrative, Lang: a.src, Span: &sp, Kind: "alias"})
	}

	a.referentialFeatures(e, p, m, surface)
	a.jb.AddEntity(e)
	a.ents[key] = e
	a.order = append(a.order, e.ID)
	return e
}

// referentialFeatures asserts number, gender, animacy and person, each with the
// provenance that earned it. The default is UNKNOWN, always.
func (a *analyzer) referentialFeatures(e *jlir.Entity, p *syntax.Phrase, m *forest.Morph, surface string) {
	prov := func(conf float64, format string, args ...any) []jlir.Provenance {
		return []jlir.Provenance{jlir.PredSpan(jlir.OriginLexical, p.Span, a.b.Source, conf, format, args...)}
	}

	// Number: overt morphology only.
	num, numProv := a.numberOf(p, m)
	e.Number = num
	if numProv != nil {
		e.Features = append(e.Features, jlir.Feature{Key: "number", Value: num, Confidence: 0.85, Prov: numProv})
	}
	e.Plural = num == jlir.NumberPlural

	head := a.b.Text(p.Head)
	lower := strings.ToLower(surface)
	g, gok := pronounGender[lower]
	if !gok && head != "" {
		g, gok = pronounGender[strings.ToLower(head)]
	}
	if !gok && m != nil {
		// A lexicon may declare a gender feature that is deliberately empty
		// ("unspecified", "unknown"). Only an actual value is asserted; anything
		// else leaves the field UNKNOWN, because recording the literal string
		// "unspecified" as a gender would be a claim the system cannot back.
		if v := validGender(m.Feat("gender")); v != "" {
			g, gok = v, true
		}
	}
	if gok && p.Pronoun {
		conf := 1.0
		if homophonousPronouns[surface] {
			conf = 0.7
		}
		e.Gender = g
		e.Features = append(e.Features, jlir.Feature{
			Key: "gender", Value: g, Confidence: conf,
			Prov: prov(conf, "gender from the overt pronoun %q", surface),
		})
	} else {
		e.Gender = jlir.GenderUnknown
	}

	// Person, same rule as gender.
	if pr, ok := pronounPerson[lower]; ok && p.Pronoun {
		e.Person = pr
		e.Features = append(e.Features, jlir.Feature{
			Key: "person", Value: pr, Confidence: 0.9,
			Prov: prov(0.9, "person from the overt pronoun %q", surface),
		})
	} else if pr, ok := pronounPerson[strings.ToLower(head)]; ok && p.Pronoun {
		e.Person = pr
		e.Features = append(e.Features, jlir.Feature{
			Key: "person", Value: pr, Confidence: 0.9,
			Prov: prov(0.9, "person from the overt pronoun %q", head),
		})
	}

	// Animacy from the lexer's own feature, or from the fact that the head is a
	// pronoun for a person. Never from the sentence's semantics.
	anim := jlir.AnimacyUnknown
	var animProv []jlir.Provenance
	if m != nil {
		if v := m.Feat("animacy"); v != "" {
			anim, animProv = v, prov(0.8, "animacy feature of %q", surface)
		}
	}
	if anim == jlir.AnimacyUnknown && p.Pronoun {
		switch lower {
		case "it", "its", "itself", "それ", "それら", "あれ", "これ":
			anim, animProv = jlir.AnimacyInanimate, prov(0.85, "overt inanimate pronoun %q", surface)
		default:
			anim, animProv = jlir.AnimacyAnimate, prov(0.85, "overt pronoun for a person")
		}
	}
	e.Animacy = anim
	if animProv != nil {
		e.Features = append(e.Features, jlir.Feature{Key: "animacy", Value: anim, Confidence: 0.85, Prov: animProv})
	}

	if len(p.Possessors) > 0 {
		e.Features = append(e.Features, jlir.Feature{
			Key: "possessed_by", Value: strings.Join(p.Possessors, ","), Confidence: 0.8,
			Prov: prov(0.8, "の-phrase folded into the noun phrase"),
		})
	}
	if len(p.Conjoined) > 0 {
		e.Features = append(e.Features, jlir.Feature{
			Key: "conjoined_heads", Value: strings.Join(p.Conjoined, ","), Confidence: 0.8,
			Prov: prov(0.8, "coordinated noun phrase"),
		})
	}
}

// numberOf derives number from overt morphology: plural suffixes, numerals,
// pronouns and the lexer's own feature. A bare noun stays UNKNOWN, because a
// Japanese noun is not marked for number and an English one without a
// determiner is not determinate either.
func (a *analyzer) numberOf(p *syntax.Phrase, m *forest.Morph) (string, []jlir.Provenance) {
	prov := func(conf float64, format string, args ...any) []jlir.Provenance {
		return []jlir.Provenance{jlir.PredSpan(jlir.OriginLexical, p.Span, a.b.Source, conf, format, args...)}
	}
	if m != nil {
		if v := m.Feat("number"); v != "" {
			return normNumber(v), prov(0.9, "number feature of the dictionary entry")
		}
	}
	if p.Numeral != "" {
		if n := numeralValue(p.Numeral); n > 0 {
			if n == 1 {
				return jlir.NumberSingular, prov(0.9, "numeral %q", p.Numeral)
			}
			return jlir.NumberPlural, prov(0.9, "numeral %q", p.Numeral)
		}
		return jlir.NumberPlural, prov(0.6, "numeral %q is not singular", p.Numeral)
	}
	if p.Pronoun {
		switch strings.ToLower(a.b.Text(p.Head)) {
		case "they", "we", "them", "us", "those":
			return jlir.NumberPlural, prov(0.95, "plural pronoun")
		case "i", "he", "she", "it", "you", "me", "him", "her":
			return jlir.NumberSingular, prov(0.95, "singular pronoun")
		}
	}
	if m != nil {
		base := m.Base
		if base == "" {
			base = m.Lem
		}
		if base != "" {
			if strings.HasSuffix(base, pluralSuffixJA) {
				return jlir.NumberPlural, prov(0.9, "plural suffix %q", pluralSuffixJA)
			}
			if isPluralEnglish(base) {
				return jlir.NumberPlural, prov(0.85, "regular English plural of %q", base)
			}
		}
	}
	return jlir.NumberUnknown, nil
}

const pluralSuffixJA = "たち"

// isPluralEnglish recognizes the regular English plural. Irregulars stay
// UNKNOWN rather than being guessed from spelling.
func isPluralEnglish(base string) bool {
	if len(base) < 4 || !strings.HasSuffix(base, "s") {
		return false
	}
	stem := base[:len(base)-1]
	switch {
	case strings.HasSuffix(stem, "ss"), strings.HasSuffix(stem, "us"), strings.HasSuffix(stem, "is"):
		return false
	case strings.HasSuffix(stem, "x"), strings.HasSuffix(stem, "ch"), strings.HasSuffix(stem, "sh"):
		return strings.HasSuffix(base, "es")
	}
	return true
}

// numeralValue reads a small set of numerals so that 「三人」 is plural and
// 「一人」 is singular. Anything else returns 0 and the caller says "plural".
func numeralValue(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	small := map[string]int{"一": 1, "1": 1, "one": 1, "二": 2, "2": 2, "two": 2,
		"三": 3, "3": 3, "three": 3, "四": 4, "4": 4, "four": 4, "五": 5, "5": 5, "five": 5,
		"六": 6, "6": 6, "six": 6, "七": 7, "7": 7, "seven": 7, "八": 8, "8": 8, "eight": 8,
		"九": 9, "9": 9, "nine": 9, "十": 10, "10": 10, "ten": 10}
	if n, ok := small[s]; ok {
		return n
	}
	tens := map[string]int{"二十": 20, "30": 30, "三十": 30, "40": 40, "四十": 40, "50": 50, "五十": 50}
	if n, ok := tens[s]; ok {
		return n
	}
	// 「三人」「2人」 and friends: the numeral heads a counter.
	if i := strings.Index(s, "人"); i > 0 {
		if n := digitValue(s[:i]); n > 0 {
			return n
		}
		if n, ok := small[s[:i]]; ok {
			return n
		}
	}
	return 0
}

func digitValue(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// corefKey is the identity key for a noun phrase: the head lemma when there is
// one, so that two mentions of 太郎 are one entity, and the surface otherwise.
func corefKey(b *syntax.Bundle, p *syntax.Phrase) string {
	if p == nil {
		return ""
	}
	if p.Pronoun {
		return "pron:" + strings.ToLower(b.Text(p.Head))
	}
	if m := b.Morph(p.Head); m != nil {
		lemma := m.Lem
		if lemma == "" {
			lemma = m.Base
		}
		if lemma == "" {
			lemma = m.Surface
		}
		if lemma != "" {
			return "n:" + strings.ToLower(lemma)
		}
	}
	if p.Span.End > p.Span.Start {
		return "n:" + strings.ToLower(p.Span.Text(b.Source))
	}
	return "n:" + strings.ToLower(b.Text(p.Head))
}

// rememberSpan records a genuine re-mention of an entity and adds the alias the
// new mention uses.
func (a *analyzer) rememberSpan(e *jlir.Entity, p *syntax.Phrase, text string) {
	e.MentionCount++
	e.Salience += 0.5
	sp := p.Span
	surface := a.b.Text(p.Head)
	if surface == "" {
		surface = text
	}
	for _, al := range e.Aliases {
		if al.Surface == surface && al.Lang == a.src {
			return
		}
	}
	e.Aliases = append(e.Aliases, jlir.Alias{Surface: surface, Lang: a.src, Span: &sp, Kind: "alias"})
	if text != "" {
		e.Prov = append(e.Prov, jlir.PredSpan(jlir.OriginSyntactic, p.Span, a.b.Source, 0.8,
			"re-mention of %q", text))
	}
}

// --- zero anaphora (plan.md §§15, 16) --------------------------------------

// zeroEntity creates a placeholder with an *unresolved* distribution over the
// plausible antecedents plus a generic UNKNOWN. It is never bound here: binding
// is the decision layer's job (plan.md §15).
func (a *analyzer) zeroEntity(span jlir.Span) *jlir.Entity {
	e := jlir.NewEntityValue(a.jb.NewEntity(), jlir.TypeUnknown)
	e.Zero = true
	e.Prov = append(e.Prov, jlir.PredSpan(jlir.OriginSyntactic, span, a.b.Source, 0.8,
		"argument with no overt realization; antecedent undecided"))
	e.Aliases = append(e.Aliases, jlir.Alias{Surface: "∅", Lang: a.src, Span: &span, Kind: "zero"})

	// Recency decay: the MOST RECENT overt referent is the likeliest
	// antecedent. This used to iterate a.order in mention order and assign
	// 1/(i+1), which gave the first-mentioned entity 1.0 and the most recent
	// 1/n — the decay ran backwards, and a sentence whose only overt entity
	// came first produced a degenerate {UNKNOWN: 1.0}.
	//
	// The prior is intentionally flat across the overt mentions. The discourse
	// store's salience model is what knows that a Japanese clause prefers the
	// previous clause's subject; this layer only knows what this sentence
	// mentions, and inventing a discourse prior here would double-count it.
	weights := map[string]float64{}
	n := 0
	for _, id := range a.order {
		cand := a.jb.G.Entity(id)
		if cand == nil || cand.Zero {
			continue
		}
		n++
		weights[string(id)] = 1
	}
	if n == 0 {
		// Nothing in this sentence can be the antecedent. Saying so is correct;
		// inventing a mass out of nothing is not.
		weights[unknownReferent] = 1
	} else {
		// An unresolved option must stay reachable: plan.md §15 keeps earlier
		// referents in the set rather than taking a winner.
		weights[unknownReferent] = 0.35
	}
	d := jlir.NewDistribution("prior:recency", weights)
	d.Provenance = "prior: flat over overt mentions in this sentence; " +
		"discourse salience is applied by the document store"
	e.Referent = d
	e.Features = append(e.Features, jlir.Feature{
		Key: "referent_candidates", Value: append([]string(nil), d.Options...), Confidence: 0.4,
		Prov: []jlir.Provenance{jlir.PredSpan(jlir.OriginSyntactic, span, a.b.Source, 0.4,
			"candidate antecedents %v", d.Options)},
	})
	a.jb.AddEntity(e)
	a.order = append(a.order, e.ID)
	return e
}

// ResolveContext tells Candidates how to weigh antecedents. It is deliberately
// small: everything the caller knows about the discourse and nothing about the
// analysis, which is already in the graph.
type ResolveContext struct {
	// Graph is optional. When set and different from the graph passed to
	// Candidates, its entities extend the candidate set — the discourse store
	// keeps its own graph of entities the sentence never mentioned.
	Graph *jlir.Graph
	// Zero names the zero entity to enumerate; empty means the first one that is
	// still unresolved, in discourse order.
	Zero jlir.ID
	// Salience carries discourse salience per entity id. It is blended with the
	// in-sentence recency prior, never used alone, so an entity the sentence does
	// mention still has a chance.
	Salience map[jlir.ID]float64
	// SpeakerID is the entity standing for the speaker. It is added to the
	// candidate set because a Japanese zero subject very often denotes the
	// speaker (plan.md §15 lists "speaker" explicitly).
	SpeakerID jlir.ID
	// Max caps the number of returned options; 0 means no cap.
	Max int
}

// Candidates returns the antecedent keys of an unresolved zero anaphor so the
// pipeline can hand them to the oracle as a closed option set (plan.md §32: the
// system generates the candidates, the oracle only chooses).
func Candidates(g *jlir.Graph, rctx ResolveContext) []string {
	if g == nil {
		return nil
	}
	target := zeroTarget(g, rctx)
	if target == nil || target.Referent == nil || len(target.Referent.Options) < 2 {
		return nil
	}
	d := target.Referent

	weights := map[string]float64{}
	ids := overtIDs(g)
	if rctx.Graph != nil && rctx.Graph != g {
		ids = append(ids, extraIDs(g, rctx.Graph)...)
	}
	for _, id := range ids {
		if id == target.ID {
			continue
		}
		key := string(id)
		if key == unknownReferent {
			continue
		}
		base := d.P(key)
		if base <= 0 {
			// The discourse store knows this entity even though the sentence did
			// not mention it: floor weight, never more than the in-sentence prior.
			base = 0.05
		}
		if s := rctx.Salience[id]; s > 0 {
			base += s
		}
		weights[key] = base
	}
	if e := g.Entity(rctx.SpeakerID); e != nil && !e.Zero {
		weights[string(rctx.SpeakerID)] += 1
	} else if _, ok := weights[unknownReferent]; !ok {
		w := d.P(unknownReferent)
		if w <= 0 {
			w = 0.2
		}
		weights[unknownReferent] = w
	}
	if len(weights) == 0 {
		return nil
	}

	merged := jlir.NewDistribution("prior:candidates", weights)
	out := make([]string, 0, len(merged.Options))
	for _, r := range merged.Top(0) {
		if r.P <= 0 {
			continue
		}
		out = append(out, r.Option)
		if rctx.Max > 0 && len(out) >= rctx.Max {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// zeroTarget picks the zero entity Candidates should enumerate.
func zeroTarget(g *jlir.Graph, rctx ResolveContext) *jlir.Entity {
	if rctx.Zero != "" {
		e := g.Entity(rctx.Zero)
		if e != nil && e.Zero && e.Referent != nil {
			return e
		}
		return nil
	}
	for _, e := range g.Entities {
		if e.Zero && e.Referent != nil && len(e.Referent.Options) > 1 {
			return e
		}
	}
	return nil
}

// overtIDs lists the non-placeholder entities in discourse order.
func overtIDs(g *jlir.Graph) []jlir.ID {
	out := make([]jlir.ID, 0, len(g.Entities))
	for _, e := range g.Entities {
		if !e.Zero {
			out = append(out, e.ID)
		}
	}
	return out
}

// extraIDs lists entities the discourse store contributes but the sentence did
// not mention.
func extraIDs(g, other *jlir.Graph) []jlir.ID {
	out := make([]jlir.ID, 0, len(other.Entities))
	for _, e := range other.Entities {
		if e.Zero || g.Entity(e.ID) != nil {
			continue
		}
		out = append(out, e.ID)
	}
	return out
}

// --- decision folding ------------------------------------------------------

// ApplyDecision folds an oracle answer back into the graph as a committed
// distribution with decision provenance. The pipeline calls this; this package
// deliberately does not import internal/jev, so the dependency stays one-way.
//
// target is the id of the node the decision was about: a scope node, an event
// (whose predicate sense was decided) or a zero entity (whose referent was).
func ApplyDecision(g *jlir.Graph, target string, options []string,
	weights map[string]float64, decisionID string, confidence float64) {
	if g == nil || target == "" {
		return
	}
	if len(weights) == 0 {
		weights = make(map[string]float64, len(options))
		for _, o := range options {
			weights[o] = 1
		}
	}
	dist := jlir.NewDistribution("jev:"+decisionID, weights)
	prov := jlir.PredDecision(decisionID, confidence, "decision on %s", target)

	switch id := jlir.ID(target); {
	case g.Scope(id) != nil:
		applyScopeDecision(g.Scope(id), dist, prov)
	case g.Event(id) != nil:
		applyEventDecision(g, g.Event(id), dist, prov)
	case g.Entity(id) != nil:
		applyEntityDecision(g, g.Entity(id), dist, prov)
	default:
		g.Notes = append(g.Notes, fmt.Sprintf("decision %s targeted unknown node %q", decisionID, target))
	}
}

// applyScopeDecision commits one reading and zeroes the rest, which is what
// makes the graph's scope linearization single-valued afterwards.
func applyScopeDecision(sc *jlir.ScopeNode, dist *jlir.Distribution, prov jlir.Provenance) {
	win := dist.Winner
	if win == "" {
		return
	}
	committed := false
	for i := range sc.Readings {
		if len(sc.Readings[i].Order) > 0 && sc.Readings[i].Order[0] == win {
			sc.Readings[i].Weight = 1
			sc.Readings[i].Prov = append(sc.Readings[i].Prov, prov)
			committed = true
			continue
		}
		sc.Readings[i].Weight = 0
	}
	if !committed {
		sc.Readings = append(sc.Readings, jlir.ScopeReading{
			Order: []string{win}, Label: win, Weight: 1, Prov: []jlir.Provenance{prov},
		})
	}
	sc.Resolved = true
	sc.Prov = append(sc.Prov, prov)
}

// applyEventDecision records the chosen predicate sense as a committed
// distribution rather than as a note string, so the provenance chain from the
// decision back to the sense survives (plan.md §60).
func applyEventDecision(g *jlir.Graph, ev *jlir.Event, dist *jlir.Distribution, prov jlir.Provenance) {
	win := dist.Winner
	if win == "" {
		return
	}
	dist.Provenance = "jev:" + prov.DecisionID
	ev.SetFeature(jlir.Feature{
		Key: "predicate_sense_distribution", Value: append([]string(nil), dist.Options...), Confidence: 1,
		Prov: []jlir.Provenance{prov},
	})
	ev.SetFeature(jlir.Feature{
		Key: "predicate_sense_committed", Value: win, Confidence: 1,
		Prov: []jlir.Provenance{prov},
	})
	ev.Prov = append(ev.Prov, prov)
	_ = g
}

// applyEntityDecision commits a zero referent and rebinds the arguments that
// pointed at the placeholder, so downstream stages see the resolved entity.
func applyEntityDecision(g *jlir.Graph, e *jlir.Entity, dist *jlir.Distribution, prov jlir.Provenance) {
	if e.Referent == nil {
		return
	}
	dist.Provenance = "jev:" + prov.DecisionID
	dist.Commit()
	e.Referent = dist
	e.Prov = append(e.Prov, prov)
	e.Features = append(e.Features, jlir.Feature{
		Key: "referent_decision", Value: dist.Winner, Confidence: 1,
		Prov: []jlir.Provenance{prov},
	})
	for _, ev := range g.Events {
		for role, arg := range ev.Args {
			if arg.Value != e.ID {
				continue
			}
			winner := jlir.ID(dist.Winner)
			if g.Entity(winner) == nil {
				continue
			}
			arg.Value = winner
			arg.Confidence = dist.P(dist.Winner)
			arg.Prov = append(arg.Prov, prov)
			ev.Args[role] = arg
		}
	}
}

// DefaultLexicon exposes the built lexicon for probing and tests.
func DefaultLexicon() *lexicon.Lexicon { return lexicon.Default() }

// matrixAdjective returns the clause's い-adjective when the clause predicate is
// an adjective rather than a verb.
func (a *analyzer) matrixAdjective(c *syntax.Clause) string {
	if c == nil || c.Matrix == "" {
		return ""
	}
	m := a.b.Morph(c.Matrix)
	if m == nil {
		return ""
	}
	if m.POS != forest.POSAdj {
		return ""
	}
	if m.Feat("adj") != "i" && m.Feat("adj") != "" {
		return ""
	}
	return m.Text(a.b.Source)
}

// copulaSenseFor picks the copula sense that matches the clause's register, so
// that 「静かだ」 and 「静かです」 do not resolve to the same predicate.
func (a *analyzer) copulaSenseFor(c *syntax.Clause) string {
	if c != nil && c.Politeness != "" && c.Politeness != "plain" {
		return "COPULA.03"
	}
	return "COPULA.01"
}

// matrixCopula returns the copula that heads the clause, or "".
//
// The copula is the predicate of 「今日はいい天気です」 even though no verb
// follows it. An external analyser reports it as an auxiliary, so it is
// recognised by the surface rather than by a part of speech that differs
// between analysers.
func (a *analyzer) matrixCopula(c *syntax.Clause) string {
	if c == nil || c.Matrix == "" {
		return ""
	}
	m := a.b.Morph(c.Matrix)
	if m == nil {
		return ""
	}
	surface := m.Text(a.b.Source)
	for _, hit := range a.sensesJP(surface) {
		if strings.HasPrefix(hit.SenseID, "COPULA.") {
			return surface
		}
	}
	// だった and だった-nit forms carry the copula inside a larger surface.
	for _, suffix := range []string{"だった", "であった", "である", "です", "だ"} {
		if strings.HasSuffix(surface, suffix) {
			return surface
		}
	}
	return ""
}
