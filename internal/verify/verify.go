package verify

// Semantic equivalence verification, plan.md §41.
//
// plan.md §40 calls semantic re-parsing the core safety device of JEV-Trans
// and §44 makes this verifier a hard gate: Jev may rank a candidate's
// naturalness but must never rescue a semantically wrong sentence. So this file
// compares features, never embedding similarity, and it is deliberately
// conservative: when a comparison cannot be made honestly the answer is a
// recorded loss, an UNDETERMINED status, or nothing at all — never a guess.
//
// This is also where the "we do not know" answer becomes available. plan.md §61
// lists UNDERDETERMINED as a first-class outcome: a target language that forces
// a distinction the source cannot supply must be able to say so rather than
// invent it.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/trace"
)

// Diff dimensions: the feature families of plan.md §41. They double as the
// confidence keys of §59, so the same string names "what was compared" and
// "how confident we are in it".
const (
	DiffPredicate   = "predicate"
	DiffRole        = "role"
	DiffPolarity    = "polarity"
	DiffTense       = "tense"
	DiffAspect      = "aspect"
	DiffModality    = "modality"
	DiffScope       = "scope"
	DiffQuantifier  = "quantifier"
	DiffNumber      = "number"
	DiffGender      = "gender"
	DiffCoreference = "coreference"
	DiffPragmatics  = "pragmatics"
	DiffImplicature = "implicature"
	DiffEntity      = "entity"
	DiffRegister    = "register"
	DiffSubject     = "subject"
	DiffMorphology  = "morphology"
)

// Diff severities. A hard diff changes or drops source content; a soft diff
// records a loss the target language cannot avoid.
const (
	SeverityHard = "hard"
	SeveritySoft = "soft"
)

// Status values of plan.md §61.
const (
	StatusExact           = "EXACT"
	StatusGood            = "GOOD"
	StatusLossy           = "LOSSY"
	StatusAmbiguous       = "AMBIGUOUS"
	StatusUnderdetermined = "UNDERDETERMINED"
	StatusUnsupported     = "UNSUPPORTED"
	StatusUnparsable      = "UNPARSABLE"
)

// Loss amounts, in units of the dimension they charge. They are the documented
// weights loss.go's Total aggregates; a diff names which dimension it spends.
const (
	// A dropped or invented argument changes what the sentence is about.
	lossRoleAdded   = 0.45
	lossRoleMissing = 0.55
	// lossRoleRenamed is the price of the two languages labelling the same
	// argument differently. It is stylistic, not propositional: the referent is
	// the same entity and the proposition is unchanged.
	lossRoleRenamed      = 0.05
	lossReferentSwap     = 0.60
	lossPredicateSwap    = 0.70
	lossPredicateSibling = 0.50
	// plan.md §5: an honorific English cannot express is a pragmatic loss and
	// nothing else, which is why this is 0.18 and not 1.0.
	lossHonorificDropped = 0.18
	lossRegisterShift    = 0.10
	// Realization preference (which pronoun, which word order) is stylistic:
	// plan.md §42 exists so natural translations are not discarded.
	lossZeroRealization = 0.06
	lossPronounChoice   = 0.05
	lossReferentUncheck = 0.08
	// plan.md §22's canonical hallucination, reported at full referential cost
	// because it asserts something the source never said.
	lossGenderInvented = 0.80
	lossGenderChanged  = 0.50
	lossGenderLost     = 0.15
	lossNumberChanged  = 0.35
	lossTenseChanged   = 0.45
	lossPolarityChange = 0.60
	lossModalityChange = 0.45
	lossScopeChanged   = 0.55
	lossScopeCollapsed = 0.20
	lossQuantifier     = 0.50
	lossCompletionLost = 0.25
	lossAspectChanged  = 0.15
	lossCausationLost  = 0.20
)

// goodThreshold is the weighted total below which a candidate with no hard diff
// is still GOOD rather than LOSSY. It sits below the cheapest deliberate loss
// (a dropped honorific: 0.18 pragmatic = 0.072 weighted) so that plan.md §5's
// "loss = 0 is not required unconditionally" never silently reports a genuine
// loss as GOOD.
const goodThreshold = 0.05

// Diff is one recorded divergence between the source and the re-parsed target.
type Diff struct {
	// Dimension is one of the Diff* constants.
	Dimension string `json:"dimension"`
	// Item names the node and role the diff is about, e.g. "v1/agent".
	Item string `json:"item"`
	// Source and Target render the compared values. "—" means the side did not
	// determine a value, which is not the same as an empty value.
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
	// Severity is SeverityHard or SeveritySoft.
	Severity string `json:"severity"`
	// Detail explains the diff in one line for the UI and the trace.
	Detail string `json:"detail"`
}

// Result is the verifier's verdict for one candidate.
type Result struct {
	Status      string                    `json:"status"`
	Loss        LossVector                `json:"loss"`
	Confidence  map[string]float64        `json:"confidence"`
	Unsupported []jlir.UnsupportedFeature `json:"unsupported,omitempty"`
	Diffs       []Diff                    `json:"diffs,omitempty"`
	Notes       []string                  `json:"notes,omitempty"`
}

// Verify compares a re-parsed target graph against the source graph, feature by
// feature, and returns the verdict. src and tgt may be the same graph, which is
// how the round-trip self-consistency check of §63 is expressed.
//
// A nil argument is not an error: it means that side could not be built at all,
// which the plan classifies as UNPARSABLE rather than as a semantic difference.
func Verify(src, tgt *jlir.Graph, srcLang, tgtLang lang.Lang) *Result {
	if srcLang == "" && src != nil {
		srcLang = src.Lang
	}
	if tgtLang == "" && tgt != nil {
		tgtLang = tgt.Lang
	}
	if !srcLang.Valid() {
		srcLang = lang.DetectLang(graphSource(src))
	}
	if !tgtLang.Valid() {
		tgtLang = lang.DetectLang(graphSource(tgt))
	}

	v := &verifier{
		src: src, tgt: tgt,
		srcLang: srcLang, tgtLang: tgtLang,
		conf: defaultConfidence(),
		res:  &Result{Confidence: map[string]float64{}},
	}

	if src == nil || tgt == nil {
		v.res.Status = StatusUnparsable
		v.note("no JLIR graph for the %s side; the candidate cannot be verified",
			missingSide(src == nil, srcLang, tgtLang))
		v.finish()
		return v.res
	}

	v.collectUnsupported()
	v.compareEvents()
	v.comparePragmatics()
	v.computeAmbiguity()
	v.deriveStatus()
	v.finish()
	return v.res
}

// VerifyTraced is Verify plus the trace span the house rules require. The core
// is identical; only the observability differs.
func VerifyTraced(rec *trace.Recorder, src, tgt *jlir.Graph, srcLang, tgtLang lang.Lang) *Result {
	res := Verify(src, tgt, srcLang, tgtLang)
	if rec == nil {
		return res
	}
	sp := rec.Open(trace.StageVerify, "semantic equivalence verification")
	sp.Data(res)
	sp.Count("diffs", len(res.Diffs))
	for _, d := range res.Diffs {
		if d.Severity == SeverityHard {
			sp.Note("hard diff [%s] %s: %s", d.Dimension, d.Item, d.Detail)
		}
	}
	for _, u := range res.Unsupported {
		sp.Note("unsupported %s=%s on %s (%s)", u.Key, u.Value, u.Owner, u.Reason)
	}
	for _, n := range res.Notes {
		sp.Note("%s", n)
	}
	sp.Label("status", res.Status)
	if res.Loss.Max() > 0 {
		sp.Label("dominant-loss", res.Loss.Dominant())
	}
	switch res.Status {
	case StatusUnparsable, StatusUnsupported:
		sp.Status(trace.StatusError)
	case StatusLossy, StatusUnderdetermined, StatusAmbiguous:
		sp.Status(trace.StatusWarn)
	}
	sp.Close()
	return res
}

// verifier accumulates the diffs, the loss and the per-feature confidence of
// one comparison.
type verifier struct {
	src     *jlir.Graph
	tgt     *jlir.Graph
	srcLang lang.Lang
	tgtLang lang.Lang
	res     *Result
	conf    map[string]float64

	// ambiguous records that an open source reading did not survive
	// verification; underdetermined records that the target language needs a
	// distinction the source cannot supply.
	ambiguous       bool
	underdetermined bool
}

func (v *verifier) note(format string, args ...any) {
	v.res.Notes = append(v.res.Notes, fmt.Sprintf(format, args...))
}

// add records a diff, charges its loss dimension and lowers the confidence of
// every feature the diff touches. The loss dimension and the diff dimension are
// deliberately separate: a wrong referent is reported under "entity" and charged
// to referential loss.
func (v *verifier) add(d Diff, lossDim string, amount float64, confKeys ...string) {
	if d.Severity == "" {
		d.Severity = SeveritySoft
	}
	v.res.Diffs = append(v.res.Diffs, d)
	if lossDim != "" && amount > 0 {
		v.res.Loss.Add(lossDim, amount)
	}
	for _, k := range confKeys {
		v.penalize(k, amount, d.Severity == SeverityHard)
	}
}

// penalize lowers a per-feature confidence in proportion to the loss the diff
// caused. A hard diff counts for more than its nominal amount because a feature
// that provably differs is a weak feature; this is what makes §59's breakdown
// diagnostic rather than decorative.
func (v *verifier) penalize(key string, amount float64, hardDiff bool) {
	if key == "" || amount <= 0 {
		return
	}
	factor := 1 - amount
	if hardDiff {
		factor = 1 - min1(amount*1.5)
	}
	v.setConf(key, v.getConf(key)*factor)
}

func (v *verifier) getConf(key string) float64 {
	c, ok := v.conf[key]
	if !ok {
		return 1
	}
	return c
}

func (v *verifier) setConf(key string, c float64) { v.conf[key] = clamp01(c) }

// confKeyRole names the per-role referent confidence of §59.
func confKeyRole(role string) string { return "role:" + role }

func defaultConfidence() map[string]float64 {
	c := map[string]float64{
		DiffPredicate:   1,
		DiffTense:       1,
		DiffAspect:      1,
		DiffPolarity:    1,
		DiffModality:    1,
		DiffScope:       1,
		DiffQuantifier:  1,
		DiffRegister:    1,
		DiffSubject:     1,
		DiffNumber:      1,
		DiffGender:      1,
		DiffCoreference: 1,
		DiffEntity:      1,
	}
	for _, role := range jlir.AllRoles {
		c[confKeyRole(role)] = 1
	}
	return c
}

func hard(diff Dimension, item, srcVal, tgtVal, detail string) Diff {
	return Diff{Dimension: string(diff), Item: item, Source: srcVal, Target: tgtVal,
		Severity: SeverityHard, Detail: detail}
}

func soft(diff Dimension, item, srcVal, tgtVal, detail string) Diff {
	return Diff{Dimension: string(diff), Item: item, Source: srcVal, Target: tgtVal,
		Severity: SeveritySoft, Detail: detail}
}

// Dimension is the feature family a Diff belongs to. It is a distinct type so
// that a diff dimension can never be confused with a loss dimension.
type Dimension string

// --- hallucination check (plan.md §22) ------------------------------------

// collectUnsupported runs the machine check behind §22: a feature present on
// the target side with no path back to the source is invented information.
//
// Two sources of truth are combined. UnsupportedFeatures covers features that
// were asserted with target-side provenance; the synthesized pass covers the
// common case where a referential property was written into the entity's plain
// field without any provenance record at all, which is exactly how
// "The female teacher came." can be produced.
func (v *verifier) collectUnsupported() {
	seen := map[string]bool{}
	for _, u := range v.tgt.UnsupportedFeatures() {
		if v.sourceDetermines(u.Key, u.Value) {
			continue
		}
		key := string(u.Owner) + "\x00" + u.Key + "\x00" + u.Value
		if seen[key] {
			continue
		}
		seen[key] = true
		v.res.Unsupported = append(v.res.Unsupported, u)
		v.note("target asserts %s=%s on %s with no upstream provenance (plan.md §22): %s",
			u.Key, u.Value, u.Owner, u.Reason)
	}

	for _, u := range v.synthesizedUnsupported() {
		key := string(u.Owner) + "\x00" + u.Key + "\x00" + u.Value
		if seen[key] {
			continue
		}
		seen[key] = true
		v.res.Unsupported = append(v.res.Unsupported, u)
		v.note("target asserts %s=%s on %s and the source never determines that value (plan.md §22)",
			u.Key, u.Value, u.Owner)
	}
}

// synthesizedUnsupported finds target entities that assert a protected
// referential property with no upstream justification and that no source entity
// bears at all. Only gender qualifies: it is the plan's canonical example and it
// is the one property English forces a choice about while Japanese leaves open.
// A property the target language simply cannot mark (number in Japanese) is a
// recorded loss, not an accusation of hallucination.
func (v *verifier) synthesizedUnsupported() []jlir.UnsupportedFeature {
	var out []jlir.UnsupportedFeature
	for _, e := range v.tgt.Entities {
		if e == nil {
			continue
		}
		gender := normalizeGender(e.GenderValue())
		if gender == GenderUndetermined {
			continue
		}
		if v.sourceDetermines("gender", gender) {
			continue
		}
		if v.genderSupportedByProvenance(e) {
			// The value came from the discourse store or an oracle decision, so
			// it is licensed rather than invented. It is still information the
			// source sentence did not carry, which the gender diff reports.
			continue
		}
		out = append(out, jlir.UnsupportedFeature{
			Owner: string(e.ID), Key: "gender", Value: gender,
			Reason: "asserted for an entity the source does not gender, with no discourse or decision provenance",
		})
	}
	return out
}

// genderSupportedByProvenance reports whether the entity's gender feature traces
// to an upstream origin.
func (v *verifier) genderSupportedByProvenance(e *jlir.Entity) bool {
	f, ok := e.Feature("gender")
	if !ok || len(f.Prov) == 0 {
		return false
	}
	for _, p := range f.Prov {
		if p.Upstream() {
			return true
		}
	}
	return false
}

// sourceDetermines reports whether the source graph asserts this value
// anywhere. If it does, the target copied source material rather than inventing
// it, and the per-feature diffs below are the right place to complain.
func (v *verifier) sourceDetermines(key, value string) bool {
	for _, f := range graphFeatures(v.src) {
		if f.Key == key && jlir.ValueString(f.Value) == value {
			return true
		}
	}
	if key == "gender" {
		for _, e := range v.src.Entities {
			if normalizeGender(e.GenderValue()) == value {
				return true
			}
		}
	}
	return false
}

// graphFeatures collects every attributed feature of a graph.
func graphFeatures(g *jlir.Graph) []jlir.Feature {
	if g == nil {
		return nil
	}
	var out []jlir.Feature
	for _, e := range g.Entities {
		if e != nil {
			out = append(out, e.Features...)
		}
	}
	for _, ev := range g.Events {
		if ev != nil {
			out = append(out, ev.Features...)
		}
	}
	return out
}

// --- event matching -------------------------------------------------------

// eventPair is one matched source/target event.
type eventPair struct {
	src *jlir.Event
	tgt *jlir.Event
}

// matchEvents pairs source events with target events by predicate sense first
// and argument signature second. The assignment is greedy but deterministic:
// scores are compared in a fixed order and the earliest candidate wins, so two
// runs of the same input pair the same events.
func matchEvents(src, tgt *jlir.Graph) (pairs []eventPair, missingSrc []*jlir.Event, matchedTgt map[jlir.ID]bool) {
	matchedTgt = map[jlir.ID]bool{}
	for _, sv := range src.Events {
		if sv == nil {
			continue
		}
		best, bestScore := (*jlir.Event)(nil), 0
		for _, tv := range tgt.Events {
			if tv == nil || matchedTgt[tv.ID] {
				continue
			}
			if s := pairScore(sv, tv); s > bestScore {
				best, bestScore = tv, s
			}
		}
		if best == nil {
			missingSrc = append(missingSrc, sv)
			continue
		}
		matchedTgt[best.ID] = true
		pairs = append(pairs, eventPair{src: sv, tgt: best})
	}
	return pairs, missingSrc, matchedTgt
}

// pairScore scores a candidate pairing. An exact sense outranks the same
// predicate family, which outranks a shared argument signature. Any positive
// score is enough to pair: leaving both events unmatched would report the same
// divergence twice, once as a dropped clause and once as an invented one.
func pairScore(sv, tv *jlir.Event) int {
	score := 0
	if sv.Predicate != "" && sv.Predicate == tv.Predicate {
		score += 10
	} else if fam := predicateFamily(sv.Predicate); fam != "" && fam == predicateFamily(tv.Predicate) {
		score += 5
	}
	for r := range sv.Args {
		if _, ok := tv.Args[r]; ok {
			score++
		}
	}
	return score
}

func predicateFamily(sense string) string {
	if i := strings.Index(sense, "."); i > 0 {
		return sense[:i]
	}
	return sense
}

// compareEvents matches and compares every event of the two graphs.
func (v *verifier) compareEvents() {
	pairs, missing, matched := matchEvents(v.src, v.tgt)

	for _, m := range missing {
		v.add(hard(DiffPredicate, string(m.ID), m.Predicate, "",
			"source event "+string(m.ID)+" ("+m.Predicate+") has no counterpart in the target re-parse"),
			DimPropositional, lossPredicateSwap, DiffPredicate)
	}
	for _, t := range v.tgt.Events {
		if t == nil || matched[t.ID] {
			continue
		}
		v.add(hard(DiffPredicate, string(t.ID), "", t.Predicate,
			"target event "+string(t.ID)+" ("+t.Predicate+") asserts a proposition the source does not contain"),
			DimPropositional, lossPredicateSwap, DiffPredicate)
	}

	for _, p := range pairs {
		item := string(p.src.ID)
		v.comparePredicate(p)
		v.compareEventFeatures(p, item)
		v.compareRoles(p, item)
		v.setConf(DiffPredicate, v.getConf(DiffPredicate)*argConfidenceOf(p.src)*argConfidenceOf(p.tgt))
	}

	v.compareScopes()
	v.summarizeSubject()
}

// comparePredicate checks that the matched events actually say the same thing.
func (v *verifier) comparePredicate(p eventPair) {
	if p.src.Predicate == p.tgt.Predicate {
		return
	}
	if predicateFamily(p.src.Predicate) == predicateFamily(p.tgt.Predicate) {
		// A sibling sense is a precision loss, not a different proposition.
		// 「帰る」 rendered as "go" keeps the core event and drops the
		// back-and-forth; that belongs in the loss vector (plan.md §43).
		// Treating it as hard would reject the only English a short translation
		// can offer. A change of family is still hard.
		v.add(soft(DiffPredicate, string(p.src.ID), p.src.Predicate, p.tgt.Predicate,
			"source sense "+p.src.Predicate+" was realized as the sibling sense "+p.tgt.Predicate),
			DimPropositional, lossPredicateSibling, DiffPredicate)
		return
	}
	v.add(hard(DiffPredicate, string(p.src.ID), p.src.Predicate, p.tgt.Predicate,
		"predicate changed from "+orNone(p.src.Predicate)+" to "+orNone(p.tgt.Predicate)),
		DimPropositional, lossPredicateSwap, DiffPredicate)
}

// compareEventFeatures compares the temporal, modal and mood layer event by
// event. Undetermined values are never compared: a gap in the analysis is not a
// mistranslation, and treating it as one would manufacture diffs out of honesty.
func (v *verifier) compareEventFeatures(p eventPair, item string) {
	s, t := p.src, p.tgt

	if sp, tp := normalizeFeature(s.Polarity), normalizeFeature(t.Polarity); sp != "" && tp != "" && sp != tp {
		v.add(hard(DiffPolarity, item, s.Polarity, t.Polarity,
			"polarity changed from "+s.Polarity+" to "+t.Polarity),
			DimPropositional, lossPolarityChange, DiffPolarity)
	}

	if st, tt := normalizeFeature(s.Tense), normalizeFeature(t.Tense); st != "" && tt != "" && st != tt {
		if equivalentTense(st, tt) {
			v.note("%s: tense %s/%s is licensed by the target language's inflection", item, st, tt)
		} else {
			v.add(hard(DiffTense, item, s.Tense, t.Tense,
				"tense changed from "+s.Tense+" to "+t.Tense),
				DimTemporal, lossTenseChanged, DiffTense)
		}
	}

	if sa, ta := normalizeFeature(s.Aspect), normalizeFeature(t.Aspect); sa != "" && ta != "" && sa != ta {
		v.add(soft(DiffAspect, item, s.Aspect, t.Aspect,
			"aspect changed from "+s.Aspect+" to "+t.Aspect),
			DimTemporal, lossAspectChanged, DiffAspect)
	}

	sc, tc := normalizeFeature(s.Completion), normalizeFeature(t.Completion)
	if sc != "" || tc != "" {
		if ok, id := Equivalent(Pair{
			Dimension: DiffAspect, Key: "completion",
			Source: sc, Target: tc,
			SourceLang: v.srcLang, TargetLang: v.tgtLang,
		}); ok {
			v.note("%s: completion expressed by equivalence class %s", item, id)
		} else if sc != tc {
			v.add(soft(DiffAspect, item, orNone(s.Completion), orNone(t.Completion),
				"completion content differs (source "+orNone(s.Completion)+", target "+orNone(t.Completion)+")"),
				DimTemporal, lossCompletionLost, DiffAspect)
		}
	}

	sm, tm := normalizeFeature(s.Modality), normalizeFeature(t.Modality)
	if sm != "" && tm != "" && sm != tm && !(sm == jlir.ModalityNone && tm == "") {
		v.add(hard(DiffModality, item, orNone(s.Modality), orNone(t.Modality),
			"modality changed from "+s.Modality+" to "+t.Modality),
			DimPropositional, lossModalityChange, DiffModality)
	}

	if normalizeFeature(s.Mood) != normalizeFeature(t.Mood) {
		v.add(soft(DiffPragmatics, item, orNone(s.Mood), orNone(t.Mood),
			"mood changed from "+orNone(s.Mood)+" to "+orNone(t.Mood)),
			DimPragmatic, lossRegisterShift, DiffPragmatics, DiffRegister)
	}

	if normalizeFeature(s.Causation) != normalizeFeature(t.Causation) {
		v.add(soft(DiffImplicature, item, orNone(s.Causation), orNone(t.Causation),
			"causal framing changed from "+orNone(s.Causation)+" to "+orNone(t.Causation)),
			DimImplicature, lossCausationLost, DiffImplicature)
	}
}

// equivalentTense reports whether a tense difference is licensed by the target
// language rather than a mistranslation: Japanese verbal inflection does not
// distinguish all English tenses, so a Japanese past may legitimately back a
// present-in-past reading.
func equivalentTense(src, tgt string) bool {
	if src == tgt {
		return true
	}
	return src == jlir.TenseUnknown && tgt == jlir.TensePast
}

// --- roles and referents --------------------------------------------------

// compareRoles checks that every filled source role survives, binds the same
// referent, and that the target invents no new argument.
func (v *verifier) compareRoles(p eventPair, item string) {
	for _, r := range p.src.OrderArgs() {
		sa, _ := p.src.Arg(r)
		se := v.src.Entity(sa.Value)
		v.setConf(confKeyRole(r), v.getConf(confKeyRole(r))*argConfidence(sa))

		ta, ok := p.tgt.Arg(r)
		if !ok {
			if alt, altRole, found := v.aliasedRole(p, r, se); found {
				v.add(soft(DiffRole, item+"/"+r, entityLabel(v.srcLang, se),
					entityLabel(v.tgtLang, v.tgt.Entity(alt.Value)),
					"role "+r+" realized as "+altRole+" on the same referent"),
					DimStylistic, lossRoleRenamed, DiffRole, confKeyRole(r))
				v.compareBoundEntity(r, item+"/"+r, se, alt, v.tgt.Entity(alt.Value))
				continue
			}
			v.add(hard(DiffRole, item+"/"+r, entityLabel(v.srcLang, se), "",
				"role "+r+" was dropped: the target does not express its argument"),
				DimPropositional, lossRoleMissing, DiffRole, confKeyRole(r))
			continue
		}
		v.setConf(confKeyRole(r), v.getConf(confKeyRole(r))*argConfidence(ta))
		v.compareBoundEntity(r, item+"/"+r, se, ta, v.tgt.Entity(ta.Value))
	}

	for _, r := range p.tgt.OrderArgs() {
		if p.src.HasRole(r) {
			continue
		}
		te := v.tgt.Entity(p.tgt.Args[r].Value)
		if r == jlir.RoleTime || r == jlir.RoleManner || r == jlir.RoleCause {
			// An adjunct the source left unstated is framing, not content.
			v.add(soft(DiffRole, item+"/"+r, "", entityLabel(v.tgtLang, te),
				"target adds an unstated "+r+" adjunct"),
				DimPragmatic, lossRegisterShift, DiffPragmatics)
			continue
		}
		v.add(hard(DiffRole, item+"/"+r, "", entityLabel(v.tgtLang, te),
			"target introduces the argument "+r+" that the source does not have"),
			DimPropositional, lossRoleAdded, DiffRole, confKeyRole(r))
	}
}

// compareBoundEntity is the referential coreference check of §41. The
// equivalence classes run first: an explicit source pronoun realized as a
// Japanese zero is correct, and §42 exists so that natural realizations survive
// verification instead of being discarded by a literal string comparison.
// roleAliases groups semantic roles that occupy the same argument position in
// Japanese and English. Japanese marks a topic with は and a subject with が,
// and the normalizer routes a は-phrase through the predicate frame to
// whichever role it admits first; English puts that entity in subject position
// and labels it agent. The label differs while the proposition is identical,
// which is exactly the difference plan.md §42 says must not throw away a
// natural translation.
var roleAliases = map[string][]string{
	jlir.RoleAgent:       {jlir.RolePatient, jlir.RoleTheme, jlir.RoleExperiencer},
	jlir.RolePatient:     {jlir.RoleAgent, jlir.RoleTheme, jlir.RoleExperiencer},
	jlir.RoleTheme:       {jlir.RoleAgent, jlir.RolePatient, jlir.RoleExperiencer},
	jlir.RoleExperiencer: {jlir.RoleAgent, jlir.RolePatient, jlir.RoleTheme},
	jlir.RoleRecipient:   {jlir.RoleGoal},
	jlir.RoleGoal:        {jlir.RoleRecipient},
}

// aliasedRole finds the role the target actually used for the same referent.
func (v *verifier) aliasedRole(p eventPair, srcRole string, se *jlir.Entity) (jlir.Arg, string, bool) {
	if se == nil {
		return jlir.Arg{}, "", false
	}
	for _, cand := range roleAliases[srcRole] {
		ta, ok := p.tgt.Arg(cand)
		if !ok {
			continue
		}
		te := v.tgt.Entity(ta.Value)
		if te == nil || !sameReferent(v.src, v.tgt, se, te) {
			continue
		}
		return ta, cand, true
	}
	return jlir.Arg{}, "", false
}

func (v *verifier) compareBoundEntity(role, item string, se *jlir.Entity, ta jlir.Arg, te *jlir.Entity) {
	coref := Pair{
		Dimension:      DiffCoreference,
		Key:            role,
		Source:         entityLabel(v.srcLang, se),
		Target:         entityLabel(v.tgtLang, te),
		SourceLang:     v.srcLang,
		TargetLang:     v.tgtLang,
		SourceOvert:    entityOvert(se, v.srcLang),
		TargetOvert:    entityOvert(te, v.tgtLang),
		SourceZero:     isZeroEntity(se),
		TargetZero:     isZeroEntity(te),
		SourceGender:   genderOf(se),
		ReferentUnique: referentUnique(te),
		Role:           role,
	}

	if ok, id := Equivalent(coref); ok {
		// Referent identity is preserved, so this costs style only.
		v.add(soft(DiffCoreference, item, coref.Source, coref.Target,
			"equivalence class "+string(id)+": realization change preserves the referent"),
			DimStylistic, lossZeroRealization, DiffPragmatics)
		v.compareNumber(item, se, te)
		v.compareGender(item, se, te, coref)
		return
	}

	if coref.TargetZero && !coref.ReferentUnique {
		// The target bound a zero form to nothing the source pinned down.
		v.underdetermined = true
		v.add(hard(DiffCoreference, item, coref.Source, coref.Target,
			"target zero form has no unique referent and the source determines none"),
			DimReferential, lossReferentSwap, DiffCoreference, confKeyRole(role))
		v.compareGender(item, se, te, coref)
		return
	}
	if coref.SourceZero && coref.TargetOvert && !coref.ReferentUnique {
		v.underdetermined = true
		v.add(hard(DiffEntity, item, coref.Source, coref.Target,
			"target made explicit a referent the source left unrecoverable"),
			DimReferential, lossReferentSwap, DiffEntity, confKeyRole(role))
		v.compareGender(item, se, te, coref)
		return
	}

	v.compareReferentIdentity(item, role, se, te)
	v.compareNumber(item, se, te)
	v.compareGender(item, se, te, coref)
}

// compareReferentIdentity checks that the bound entity is the same referent.
//
// The verifier distinguishes three outcomes and refuses to collapse them:
//   - the identity keys agree, so the alignment is confirmed;
//   - both sides are proper names written in the same script and they differ,
//     so the alignment is refuted and this is a hard referential diff;
//   - otherwise the names cannot be compared lexically (太郎 vs Taro), so the
//     alignment rests on the pipeline's own identity mapping and is recorded as
//     a soft unchecked loss rather than a silent pass.
func (v *verifier) compareReferentIdentity(item, role string, se, te *jlir.Entity) {
	if sameReferent(v.src, v.tgt, se, te) {
		return
	}
	sName, tName := entityName(se, v.srcLang), entityName(te, v.tgtLang)
	switch {
	case se == nil || te == nil:
		v.add(hard(DiffEntity, item, orNone(sName), orNone(tName),
			"role "+role+" binds a different referent on the target side"),
			DimReferential, lossReferentSwap, DiffEntity)
	case se.Proper && te.Proper && sName != "" && tName != "" &&
		lang.DetectLang(sName) == lang.DetectLang(tName):
		v.add(hard(DiffEntity, item, sName, tName,
			"role "+role+" binds a different proper name than the source"),
			DimReferential, lossReferentSwap, DiffEntity)
	default:
		v.add(soft(DiffEntity, item, orNone(sName), orNone(tName),
			"role "+role+" referent alignment could not be checked lexically; it rests on the pipeline identity mapping"),
			DimReferential, lossReferentUncheck, DiffEntity)
	}
}

// compareNumber compares the referential number layer.
func (v *verifier) compareNumber(item string, se, te *jlir.Entity) {
	if se == nil || te == nil {
		return
	}
	sn, tn := normalizeNumber(numberOf(se)), normalizeNumber(numberOf(te))
	if sn == tn {
		return
	}
	if sn == "UNKNOWN" || tn == "UNKNOWN" {
		// One side marked number and the other did not. That is a reference the
		// target language declined to express, not a claim about the referent.
		v.note("%s: number determined on one side only (source %s, target %s)", item, sn, tn)
		return
	}
	if sn == jlir.NumberMass || tn == jlir.NumberMass {
		return
	}
	v.add(hard(DiffNumber, item, sn, tn,
		"number changed from "+sn+" to "+tn),
		DimReferential, lossNumberChanged, DiffNumber)
}

// compareGender compares the referential gender layer. This is the plan's
// canonical safety case: a target gender the source did not supply is a hard
// diff whether it was invented outright (§22, UNSUPPORTED) or licensed by a
// grammatical demand of the target language that the source cannot satisfy
// (§61, UNDERDETERMINED).
func (v *verifier) compareGender(item string, se, te *jlir.Entity, coref Pair) {
	if se == nil || te == nil {
		return
	}
	sg, tg := normalizeGender(genderOf(se)), normalizeGender(genderOf(te))

	switch {
	case sg == GenderUndetermined && tg == GenderUndetermined:
		return
	case sg == GenderUndetermined && tg != GenderUndetermined:
		// "they" for an undetermined source gender is the correct output, not a
		// loss; the §42 class says so explicitly.
		if ok, id := Equivalent(Pair{
			Dimension: DiffGender, Key: "pronoun",
			Source: coref.Source, Target: coref.Target,
			SourceLang: v.srcLang, TargetLang: v.tgtLang,
			SourceGender: sg, ReferentUnique: coref.ReferentUnique,
			Role: coref.Role,
		}); ok {
			v.add(soft(DiffGender, item, GenderUndetermined, coref.Target,
				"equivalence class "+string(id)+": the target declined to invent a gender the source left open"),
				DimStylistic, lossPronounChoice, DiffPragmatics)
			v.note("%s: gender deliberately left undetermined on the target side (%s)", item, coref.Target)
			return
		}
		v.add(hard(DiffGender, item, GenderUndetermined, tg,
			"target asserts gender "+tg+" where the source determines none"),
			DimReferential, lossGenderInvented, DiffGender)
		if v.genderSupportedByProvenance(te) {
			// The value is licensed by the discourse store or an oracle decision,
			// so it is not invented — but the source sentence still did not say
			// it, and plan.md §61 wants that said out loud.
			v.underdetermined = true
			v.note("%s: gender came from discourse or a decision, not from the source sentence", item)
		}
	case sg != GenderUndetermined && tg == GenderUndetermined:
		// Japanese marks no gender agreement, so the drop is legal.
		v.add(soft(DiffGender, item, sg, GenderUndetermined,
			"target does not mark the gender the source asserts"),
			DimReferential, lossGenderLost, DiffGender)
	default:
		if sg != tg {
			v.add(hard(DiffGender, item, sg, tg,
				"gender changed from "+sg+" to "+tg),
				DimReferential, lossGenderChanged, DiffGender)
		}
	}
}

// --- scope and quantifier -------------------------------------------------

// compareScopes compares the scope layer. An unresolved source reading the
// target silently collapsed is a real loss, and a changed ordering is a change
// of meaning: "not all" is not "all not".
func (v *verifier) compareScopes() {
	sOpen, sTotal := scopeOpenings(v.src)
	tOpen, tTotal := scopeOpenings(v.tgt)

	if sOpen > 0 && tOpen < sOpen {
		v.ambiguous = true
		v.add(soft(DiffScope, "scope",
			fmt.Sprint(sOpen)+" open readings", fmt.Sprint(tOpen)+" open readings",
			"target collapsed "+fmt.Sprint(sOpen-tOpen)+" source scope reading(s) the source left open"),
			DimPropositional, lossScopeCollapsed, DiffScope)
	}
	if sTotal != tTotal {
		v.add(hard(DiffScope, "scope", fmt.Sprint(sTotal), fmt.Sprint(tTotal),
			"the target carries a different number of scope operators"),
			DimPropositional, lossScopeChanged, DiffScope)
	}
	if sq, tq := quantifierCount(v.src), quantifierCount(v.tgt); sq != tq {
		v.add(hard(DiffQuantifier, "quantifier", fmt.Sprint(sq), fmt.Sprint(tq),
			"the quantifier count changed, so the proposition's extension differs"),
			DimPropositional, lossQuantifier, DiffQuantifier)
	}
	ss, sok := scopeSignature(v.src)
	ts, tok := scopeSignature(v.tgt)
	if sok && tok && ss != ts {
		v.add(hard(DiffScope, "order", ss, ts,
			"scope ordering changed from "+ss+" to "+ts),
			DimPropositional, lossScopeChanged, DiffScope)
	}
}

// scopeSignature renders the operator orderings of a graph, or "" when it has
// no scope to compare. Every reading is listed so that an ambiguity difference
// shows up instead of being smoothed over.
func scopeSignature(g *jlir.Graph) (string, bool) {
	if g == nil || len(g.Scopes) == 0 {
		return "", false
	}
	nodes := append([]*jlir.ScopeNode(nil), g.Scopes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	var parts []string
	for _, s := range nodes {
		if s == nil || s.Kind == "" {
			continue
		}
		if len(s.Readings) == 0 {
			parts = append(parts, s.Kind)
			continue
		}
		readings := append([]jlir.ScopeReading(nil), s.Readings...)
		sort.Slice(readings, func(i, j int) bool {
			if readings[i].Label != readings[j].Label {
				return readings[i].Label < readings[j].Label
			}
			return readings[i].Weight > readings[j].Weight
		})
		for _, r := range readings {
			label := r.Label
			if label == "" {
				label = strings.Join(r.Order, ">")
			}
			parts = append(parts, label)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, " / "), true
}

// scopeOpenings counts operators carrying more than one reading and, separately,
// the operators the target committed to. A resolved node is not open even when
// it had alternatives: the decision has been made and documented.
func scopeOpenings(g *jlir.Graph) (open, total int) {
	if g == nil {
		return 0, 0
	}
	for _, s := range g.Scopes {
		if s == nil {
			continue
		}
		total++
		if len(s.Readings) > 1 && !s.Resolved {
			open++
		}
	}
	return open, total
}

// quantifierCount counts the quantified operators.
func quantifierCount(g *jlir.Graph) int {
	if g == nil {
		return 0
	}
	n := 0
	for _, s := range g.Scopes {
		if s == nil {
			continue
		}
		switch s.Kind {
		case jlir.ScopeAll, jlir.ScopeNone, jlir.ScopeSome:
			n++
		}
	}
	return n
}

// --- pragmatics -----------------------------------------------------------

// comparePragmatics compares the pragmatic layer of plan.md §18. plan.md §5 is
// explicit that an honorific English cannot express is a pragmatic loss and not
// a failure, so this path never produces a hard diff for a missing respect
// marker: it produces a recorded weighted loss plus the §42 class licensing it.
func (v *verifier) comparePragmatics() {
	sHon := honorificEvidence(v.src)
	tHon := honorificEvidence(v.tgt)

	switch {
	case sHon != "" && tHon == "":
		id := ""
		if ok, cid := Equivalent(Pair{
			Dimension: DiffPragmatics, Key: "honorific",
			Source: sHon, Target: "",
			SourceLang: v.srcLang, TargetLang: v.tgtLang,
		}); ok {
			id = string(cid)
		}
		detail := "source respect marker " + sHon + " has no realization in the target"
		if id != "" {
			detail += " (equivalence class " + id + ")"
		}
		v.add(soft(DiffPragmatics, "honorific", sHon, "", detail),
			DimPragmatic, lossHonorificDropped, DiffPragmatics, DiffRegister)
		v.note("unrealizable source feature (plan.md §5): honorific=%s preserved internally, surface realization unavailable", sHon)
	case tHon != "" && sHon == "":
		v.add(soft(DiffPragmatics, "honorific", "", tHon,
			"target adds a respect marker the source does not carry"),
			DimPragmatic, lossRegisterShift, DiffPragmatics, DiffRegister)
	}

	if d := absDelta(v.src.Prag.Respect, v.tgt.Prag.Respect); d > 0.25 {
		v.add(soft(DiffPragmatics, "respect", fmt.Sprint(round3(v.src.Prag.Respect)), fmt.Sprint(round3(v.tgt.Prag.Respect)),
			"respect level differs by "+fmt.Sprint(round3(d))),
			DimPragmatic, min1(d*0.2), DiffPragmatics, DiffRegister)
	}
	if d := absDelta(v.src.Prag.Politeness, v.tgt.Prag.Politeness); d > 0.25 {
		v.add(soft(DiffPragmatics, "politeness", fmt.Sprint(round3(v.src.Prag.Politeness)), fmt.Sprint(round3(v.tgt.Prag.Politeness)),
			"politeness differs by "+fmt.Sprint(round3(d))),
			DimPragmatic, min1(d*0.2), DiffPragmatics, DiffRegister)
	}
	if v.src.Prag.SpeechStyle != "" && v.tgt.Prag.SpeechStyle != "" &&
		v.src.Prag.SpeechStyle != v.tgt.Prag.SpeechStyle {
		v.add(soft(DiffPragmatics, "style", v.src.Prag.SpeechStyle, v.tgt.Prag.SpeechStyle,
			"speech style changed from "+v.src.Prag.SpeechStyle+" to "+v.tgt.Prag.SpeechStyle),
			DimPragmatic, lossRegisterShift, DiffPragmatics, DiffRegister)
	}
	if len(v.src.Prag.SentenceFinalParticles) > 0 &&
		len(v.tgt.Prag.SentenceFinalParticles) == 0 && v.tgtLang == lang.JA {
		v.add(soft(DiffPragmatics, "sentence-final",
			strings.Join(v.src.Prag.SentenceFinalParticles, ","), "",
			"target drops the sentence-final particles that carry the speaker's stance"),
			DimPragmatic, lossRegisterShift, DiffPragmatics, DiffRegister)
	}

	v.setConf(DiffRegister, v.getConf(DiffRegister)*registerConfidence(v.src.Prag, v.tgt.Prag))
}

// honorificEvidence reports the strongest honorific or respectful marker in a
// graph, reading both the pragmatic layer and the lexical material. Japanese
// encodes respect morphologically as well as lexically, so both are consulted.
func honorificEvidence(g *jlir.Graph) string {
	if g == nil {
		return ""
	}
	if g.SourceFeat.HonorificLevel != "" {
		return "honorific:" + g.SourceFeat.HonorificLevel
	}
	if g.SourceFeat.Honorific {
		return "honorific:lexical"
	}
	if g.Prag.RespectAddressee != "" {
		return "respect:" + g.Prag.RespectAddressee
	}
	if g.Prag.Humility > 0 {
		return "humble"
	}
	if g.Prag.Respect > 0.5 {
		return "respect"
	}
	for _, e := range g.Entities {
		if e == nil {
			continue
		}
		for _, a := range e.Aliases {
			if cls := honorificClass(a.Surface); cls != "" {
				return cls
			}
		}
	}
	return honorificClass(g.Source)
}

// registerConfidence folds the pragmatic scalars into one register confidence.
// A graph carrying no register evidence is not penalised: the target may simply
// have nothing to mark.
func registerConfidence(s, t jlir.Pragmatics) float64 {
	if s.SpeechStyle == "" && t.SpeechStyle == "" && s.Respect == 0 && t.Respect == 0 &&
		s.Politeness == 0 && t.Politeness == 0 {
		return 1
	}
	c := 1.0
	c -= min1(absDelta(s.Respect, t.Respect) * 0.4)
	c -= min1(absDelta(s.Politeness, t.Politeness) * 0.4)
	c -= min1(absDelta(s.SocialDistance, t.SocialDistance) * 0.2)
	if s.SpeechStyle != "" && t.SpeechStyle != "" && s.SpeechStyle != t.SpeechStyle {
		c -= 0.15
	}
	return clamp01(c)
}

// --- ambiguity and status -------------------------------------------------

// summarizeSubject folds the subject and experiencer referent confidences into
// the dedicated §59 "subject" key, which is the number a reader checks first
// when a zero pronoun went wrong.
func (v *verifier) summarizeSubject() {
	c := 1.0
	n := 0
	for _, role := range []string{jlir.RoleAgent, jlir.RoleExperiencer} {
		if got, ok := v.conf[confKeyRole(role)]; ok {
			c *= got
			n++
		}
	}
	if n == 0 {
		return
	}
	if got, ok := v.conf[DiffSubject]; ok && got < c {
		c = got
	}
	v.setConf(DiffSubject, c)
}

// computeAmbiguity records that an open source reading did not survive
// verification. It is deliberately conservative: two equivalent graphs are by
// definition equivalent whatever they left open, so an identical pair reports
// EXACT and never AMBIGUOUS.
func (v *verifier) computeAmbiguity() {
	sOpen, _ := scopeOpenings(v.src)
	tOpen, _ := scopeOpenings(v.tgt)
	if sOpen > 0 && tOpen < sOpen {
		v.ambiguous = true
	}
	if unresolvedRef(v.src) > unresolvedRef(v.tgt) {
		v.ambiguous = true
	}
}

// unresolvedRef counts entities whose referent is still an open distribution.
func unresolvedRef(g *jlir.Graph) int {
	if g == nil {
		return 0
	}
	n := 0
	for _, e := range g.Entities {
		if e == nil || e.Referent == nil || e.Referent.Resolved {
			continue
		}
		if e.Referent.Ambiguous(0.1) {
			n++
		}
	}
	return n
}

// deriveStatus computes the verdict of plan.md §61. The precedence matters: an
// unsupported assertion is reported as UNSUPPORTED even though it is also a hard
// diff, because the actionable question is "where did this information come
// from", not "how large is the loss".
func (v *verifier) deriveStatus() {
	hardCount := 0
	for _, d := range v.res.Diffs {
		if d.Severity == SeverityHard {
			hardCount++
		}
	}

	switch {
	case len(v.res.Unsupported) > 0:
		v.res.Status = StatusUnsupported
	case v.underdetermined:
		v.res.Status = StatusUnderdetermined
	case hardCount > 0:
		v.res.Status = StatusLossy
	case len(v.res.Diffs) == 0 && v.res.Loss.IsZero():
		v.res.Status = StatusExact
	case v.ambiguous:
		v.res.Status = StatusAmbiguous
	case v.res.Loss.Total() <= goodThreshold:
		v.res.Status = StatusGood
	default:
		v.res.Status = StatusLossy
	}
}

// finish copies the accumulated per-feature confidence into the result.
func (v *verifier) finish() {
	for k, c := range v.conf {
		v.res.Confidence[k] = round3(clamp01(c))
	}
	sort.Strings(v.res.Notes)
}

// --- referent helpers -----------------------------------------------------

// entityOvert reports whether e has a surface realization in l.
func entityOvert(e *jlir.Entity, l lang.Lang) bool {
	if e == nil || isZeroEntity(e) {
		return false
	}
	return strings.TrimSpace(e.Alias(l)) != ""
}

// isZeroEntity reports whether e is an unrealized placeholder. A zero anaphor is
// an Entity with a Referent distribution (plan.md §15), not a missing node, so
// the test must accept the flag, the distribution, and an explicit zero alias.
func isZeroEntity(e *jlir.Entity) bool {
	if e == nil {
		return true
	}
	if e.Zero {
		return true
	}
	if e.Referent != nil {
		return true
	}
	for _, a := range e.Aliases {
		if a.Kind == "zero" {
			return true
		}
	}
	return false
}

// referentUnique reports whether the discourse state pins e to one referent.
func referentUnique(e *jlir.Entity) bool {
	if e == nil || e.Referent == nil {
		return false
	}
	live := 0
	for _, p := range e.Referent.Prob {
		if p > 0.01 {
			live++
		}
	}
	return live == 1
}

// sameReferent reports whether se and te denote the same referent. Canonical
// identity keys come first; a positional fallback covers the case where the
// pipeline aligned two translations of one sentence without carrying a
// cross-lingual key into the target identity field.
func sameReferent(sg, tg *jlir.Graph, se, te *jlir.Entity) bool {
	if se == nil || te == nil {
		return se == nil && te == nil
	}
	if se.ID == te.ID {
		return true
	}
	keys := entityKeys(sg, se)
	if len(keys) == 0 {
		keys = entityKeys(tg, te)
	}
	if len(keys) > 0 {
		return intersects(keys, entityKeys(tg, te))
	}
	if se.Proper != te.Proper || se.Type != te.Type {
		return false
	}
	return positionIndex(sg, se) == positionIndex(tg, te)
}

// entityKeys returns the canonical identity keys of e: its own identity, its
// names, and the canonical form of any named-entity entry that produced one of
// those names. This is the cross-lingual bridge that lets 太郎 and Taro compare
// as one referent (plan.md §51).
func entityKeys(g *jlir.Graph, e *jlir.Entity) map[string]bool {
	keys := map[string]bool{}
	if g == nil || e == nil {
		return keys
	}
	if e.Identity != "" {
		keys["id:"+strings.ToLower(e.Identity)] = true
	}
	for _, a := range e.Aliases {
		s := strings.TrimSpace(a.Surface)
		if s == "" {
			continue
		}
		if a.Kind == "name" || a.Kind == "translation" {
			keys["nm:"+strings.ToLower(s)] = true
		}
	}
	for canon, ne := range g.NamedEntities {
		for _, form := range ne.Forms {
			f := strings.TrimSpace(form)
			if f == "" {
				continue
			}
			for _, a := range e.Aliases {
				if strings.EqualFold(strings.TrimSpace(a.Surface), f) {
					keys["id:"+strings.ToLower(canon)] = true
				}
			}
		}
	}
	return keys
}

func intersects(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

func positionIndex(g *jlir.Graph, e *jlir.Entity) int {
	if g == nil {
		return -1
	}
	for i, x := range g.Entities {
		if x == e {
			return i
		}
	}
	return -1
}

// entityName returns the proper-noun style label of e, or "" when it has none.
func entityName(e *jlir.Entity, l lang.Lang) string {
	if e == nil {
		return ""
	}
	if n := e.Name(l); n != "" {
		return n
	}
	if e.Proper || e.Identity != "" {
		return strings.TrimSpace(e.Alias(l))
	}
	return ""
}

// entityLabel renders the best human label for a diff.
func entityLabel(l lang.Lang, e *jlir.Entity) string {
	if e == nil {
		return "∅"
	}
	if s := entityName(e, l); s != "" {
		return s
	}
	if s := strings.TrimSpace(e.Alias(l)); s != "" {
		return s
	}
	return "∅"
}

func genderOf(e *jlir.Entity) string {
	if e == nil {
		return GenderUndetermined
	}
	return e.GenderValue()
}

func numberOf(e *jlir.Entity) string {
	if e == nil {
		return jlir.NumberUnknown
	}
	if f, ok := e.Feature("number"); ok {
		return jlir.ValueString(f.Value)
	}
	if e.Plural {
		return jlir.NumberPlural
	}
	return e.Number
}

// argConfidence reads a binding confidence. An unset zero means full confidence:
// the pipeline omits the field rather than meaning "no confidence".
func argConfidence(a jlir.Arg) float64 {
	if a.Confidence <= 0 || a.Confidence > 1 {
		return 1
	}
	return a.Confidence
}

// argConfidenceOf folds every argument binding confidence of an event.
func argConfidenceOf(e *jlir.Event) float64 {
	if e == nil {
		return 1
	}
	c := 1.0
	for _, a := range e.Args {
		c *= argConfidence(a)
	}
	return c
}

// --- small helpers --------------------------------------------------------

func graphSource(g *jlir.Graph) string {
	if g == nil {
		return ""
	}
	return g.Source
}

func missingSide(srcMissing bool, srcLang, tgtLang lang.Lang) string {
	if srcMissing {
		return srcLang.Name()
	}
	return tgtLang.Name()
}

func clamp01(f float64) float64 {
	if f < 0 || f != f {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func min1(f float64) float64 {
	if f < 0 || f != f {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func absDelta(a, b float64) float64 {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d
}

// orNone renders an undetermined value as "—" so the UI never shows an empty
// string where the real meaning is "we do not know".
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
