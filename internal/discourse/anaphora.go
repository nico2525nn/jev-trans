package discourse

import (
	"fmt"
	"math"

	"github.com/nico2525nn/jev-trans/internal/jlir"
)

// Anaphora is the state of one zero argument (plan.md §15):
//
//	太郎は花子に会った。嬉しそうだった。
//
// The second clause's subject is not fixed to Taro when it is parsed. It starts
// as a candidate set plus a discourse prior, it may be updated by an oracle
// posterior, and it is committed only when the winner is decisive. Plan.md §16
// makes that distribution a first class citizen of the graph, not an internal
// detail, so the whole thing is data the pipeline can hand to the realizer, the
// verifier or the UI unchanged.
type Anaphora struct {
	// ZeroID is the placeholder node in the source graph ("z1").
	ZeroID jlir.ID `json:"zeroId,omitempty"`
	// Role is the semantic role the zero fills.
	Role string `json:"role,omitempty"`
	// Surface is the overt form the zero would have had, e.g. the clause it
	// continues. It is only used to phrase a user question.
	Surface string `json:"surface,omitempty"`
	// Sentence is the document sentence the zero occurs in.
	Sentence int `json:"sentence"`

	Candidates []jlir.ID          `json:"candidates,omitempty"`
	Prior      *jlir.Distribution `json:"prior,omitempty"`
	// Posterior is the oracle judgement, if one was merged in.
	Posterior *jlir.Distribution `json:"posterior,omitempty"`
	// Distribution is the current referent distribution. It stays unresolved
	// unless Commit or a user answer resolved it.
	Distribution *jlir.Distribution `json:"distribution,omitempty"`

	// Margin is the probability gap between the best and the runner-up. A small
	// margin is the correct reason to stay open, not a failure.
	Margin float64 `json:"margin"`
	// Committed records whether the referent was fixed, and CommittedTo which.
	Committed   bool    `json:"committed"`
	CommittedTo jlir.ID `json:"committedTo,omitempty"`

	QuestionID string `json:"questionId,omitempty"`
	// Answered marks a question the user settled (plan.md §62).
	Answered bool              `json:"answered,omitempty"`
	Source   string            `json:"source,omitempty"`
	Prov     []jlir.Provenance `json:"provenance,omitempty"`
	Notes    []string          `json:"notes,omitempty"`
}

// ZeroSlot is a zero placeholder observed in a sentence, before the decision
// layer has had a chance to say anything about it.
type ZeroSlot struct {
	ZeroID     jlir.ID           `json:"zeroId"`
	Type       string            `json:"type,omitempty"`
	Role       string            `json:"role,omitempty"`
	Candidates []string          `json:"candidates,omitempty"`
	Prov       []jlir.Provenance `json:"provenance,omitempty"`
}

// PendingPronoun is a pronoun mention the discourse could not attach to a clear
// antecedent. It is reported, never guessed away.
type PendingPronoun struct {
	Surface    string             `json:"surface"`
	Entity     jlir.ID            `json:"entity,omitempty"`
	Candidates []jlir.ID          `json:"candidates,omitempty"`
	Prior      *jlir.Distribution `json:"prior,omitempty"`
	Note       string             `json:"note,omitempty"`
}

// MinMargin is the decision margin below which a zero argument stays open.
// Plan.md §15's own example (Taro 0.56, Hanako 0.43) has a margin of 0.13 and
// is explicitly *not* to be resolved, so the threshold sits above it.
const MinMargin = 0.15

// priorWeight is how much the discourse prior counts against an oracle
// posterior in Combine. It is below 1 on purpose: a judgement made with the
// whole context in view outranks a salience heuristic, but the prior must still
// be able to hold a candidate that the oracle did not consider.
const priorWeight = 0.35

// naturalAmbiguityMargin is the referent margin below which even a target
// language that can express ambiguity naturally is better served by asking.
const naturalAmbiguityMargin = 0.35

// dampUnmentioned is the probability factor a candidate keeps when an oracle
// judgement simply did not mention it. Silence is not a verdict, so the
// candidate is damped rather than deleted.
const dampUnmentioned = 0.5

// posteriorFloor keeps a ruled-out option at a tiny mass instead of zero, so
// the combined distribution stays finite and every option stays visible.
const posteriorFloor = 1e-4

// Candidates returns the referents a zero argument may denote, given the pool
// the discourse knows about. The pool is the store's live entities plus the
// entities of the current sentence, minus the placeholder itself.
//
// Role filters the pool to referents that can plausibly fill it — an experiencer
// must be animate — but never removes a candidate merely because its animacy
// was never asserted; plan.md §4 is about not inventing, not about forgetting.
func Candidates(g *jlir.Graph, pool []jlir.ID, role string) []jlir.ID {
	seen := map[jlir.ID]bool{}
	var out []jlir.ID
	add := func(id jlir.ID) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range pool {
		add(id)
	}
	if g != nil {
		for _, e := range g.Entities {
			if e == nil || e.Zero {
				continue
			}
			if roleNeedsAnimate(role) && e.Animacy == jlir.AnimacyInanimate {
				continue
			}
			add(e.ID)
		}
	}
	sortIDs(out)
	return out
}

func roleNeedsAnimate(role string) bool {
	switch role {
	case jlir.RoleExperiencer, jlir.RoleAgent:
		return true
	}
	return false
}

// Combine folds an oracle judgement into a discourse prior and returns the
// resulting distribution. It is a package level function taking a plain map so
// this package never has to import the oracle client: the pipeline passes the
// answer in, and a candidate the posterior ignores is not silently deleted
// (it is damped, which is how "the oracle did not consider him" differs from
// "the oracle ruled him out").
func Combine(prior *jlir.Distribution, posterior map[string]float64) *jlir.Distribution {
	opts := map[string]bool{}
	for _, o := range priorOptions(prior) {
		opts[o] = true
	}
	for o := range posterior {
		if o != "" {
			opts[o] = true
		}
	}
	if len(opts) == 0 {
		return nil
	}
	weights := map[string]float64{}
	for o := range opts {
		w := 0.0
		if p := prior.P(o); p > 0 {
			w += priorWeight * math.Log(p)
		}
		if p, ok := posterior[o]; ok {
			if p <= 0 {
				p = posteriorFloor
			}
			w += math.Log(p)
		} else if p := prior.P(o); p > 0 {
			// Present in the prior, absent from the judgement: damped, not
			// dropped, because "not considered" is not "ruled out".
			w += math.Log(dampUnmentioned)
		}
		weights[o] = w
	}
	return jlir.NewDistribution("prior+posterior", weights)
}

func priorOptions(d *jlir.Distribution) []string {
	if d == nil {
		return nil
	}
	return d.Options
}

// ApplyPosterior merges an oracle judgement into the anaphora's distribution
// and refreshes the margin. The distribution stays unresolved; the caller
// decides whether the margin justifies a commitment.
func ApplyPosterior(a *Anaphora, posterior map[string]float64) *Anaphora {
	if a == nil {
		return nil
	}
	if len(posterior) == 0 {
		return a
	}
	prior := a.Distribution
	if prior == nil {
		prior = a.Prior
	}
	d := Combine(prior, posterior)
	if d == nil {
		return a
	}
	d.Resolved = false
	d.Winner = ""
	a.Posterior = jlir.NewDistribution("jev", posterior)
	a.Distribution = d
	a.Source = "prior+posterior"
	a.Margin = d.Margin()
	a.Committed = false
	a.CommittedTo = ""
	return a
}

// New builds the initial anaphora state from a candidate set and a prior.
func New(zeroID jlir.ID, role string, sentence int, candidates []jlir.ID, prior *jlir.Distribution) *Anaphora {
	a := &Anaphora{
		ZeroID:       zeroID,
		Role:         role,
		Sentence:     sentence,
		Candidates:   sortedIDs(uniqueIDs(candidates)),
		Prior:        prior,
		Distribution: prior,
		Source:       "discourse_prior",
		Prov: []jlir.Provenance{{
			EntityID: zeroID, Origin: jlir.OriginDiscourse,
			Note: "zero argument left unresolved; candidate set from the document state",
		}},
	}
	if prior != nil {
		a.Margin = prior.Margin()
	}
	return a
}

// Top returns the highest probability options.
func (a *Anaphora) Top(k int) []jlir.Ranked {
	if a == nil || a.Distribution == nil {
		return nil
	}
	return a.Distribution.Top(k)
}

// Winner returns the currently most probable referent, which is a ranking, not
// a decision: it is only the referent to use when Committed is true.
func (a *Anaphora) Winner() jlir.ID {
	if a == nil || a.Distribution == nil || a.Distribution.Winner == "" {
		return ""
	}
	return jlir.ID(a.Distribution.Winner)
}

// Ambiguous reports whether the referent is still genuinely open.
func (a *Anaphora) Ambiguous(threshold float64) bool {
	if a == nil || a.Distribution == nil {
		return false
	}
	return a.Distribution.Ambiguous(threshold)
}

// Commit fixes the referent when the decision margin is decisive, and leaves
// the distribution open when it is not. Leaving it open is the correct
// behaviour: plan.md §17 prefers preserved ambiguity over a wrong commitment.
func (a *Anaphora) Commit() bool {
	return a.CommitWithMargin(MinMargin)
}

// CommitWithMargin is Commit with an explicit margin threshold.
func (a *Anaphora) CommitWithMargin(minMargin float64) bool {
	if a == nil || a.Distribution == nil {
		return false
	}
	if a.Distribution.Winner == "" || a.Distribution.Winner == UnknownOption {
		return false
	}
	if a.Margin < minMargin {
		a.Notes = append(a.Notes, fmt.Sprintf(
			"left open: margin %.2f is below the %.2f threshold", a.Margin, minMargin))
		return false
	}
	a.Distribution.Commit()
	a.Committed = true
	a.CommittedTo = jlir.ID(a.Distribution.Winner)
	a.Source = "committed"
	return true
}

// Answer applies a user's decision to an ambiguity question (plan.md §62). The
// answer is recorded as jlir.OriginUser provenance, which is what makes it
// citable in the trace and honest in the verifier.
func Answer(a *Anaphora, questionID, option string) *Anaphora {
	if a == nil || option == "" {
		return a
	}
	if a.Distribution == nil {
		keys := make([]string, len(a.Candidates))
		for i, c := range a.Candidates {
			keys[i] = string(c)
		}
		a.Distribution = jlir.Uniform(keys...)
	}
	if _, ok := a.Distribution.Prob[option]; !ok {
		// An answer about an option that was never a candidate is recorded as a
		// note rather than silently widening the option set.
		a.Notes = append(a.Notes, "user answered "+option+", which was not a candidate")
		return a
	}
	prov := jlir.Provenance{
		Token: option, Origin: jlir.OriginUser, Confidence: 1.0,
		DecisionID: questionID, Note: "user disambiguated the referent",
	}
	a.Prov = append(a.Prov, prov)
	a.Distribution = jlir.NewDistribution("user", map[string]float64{option: 1})
	a.Distribution.Resolved = true
	a.Distribution.Winner = option
	a.Margin = 1
	a.Answered = true
	a.QuestionID = questionID
	a.Committed = true
	a.CommittedTo = jlir.ID(option)
	a.Source = "user"
	return a
}

// QuestionOption is one option of an interactive disambiguation question.
type QuestionOption struct {
	Key         string  `json:"key"`
	Label       string  `json:"label,omitempty"`
	Probability float64 `json:"probability"`
}

// Question is a question the pipeline may put to the user. Plan.md §62 is
// explicit that this happens only when it matters, so BuildQuestion is the only
// way one comes into existence and it refuses to build a pointless question.
type Question struct {
	ID       string           `json:"id"`
	Question string           `json:"question"`
	Kind     string           `json:"kind,omitempty"`
	Options  []QuestionOption `json:"options"`
	// Why records the margin that triggered the question, so the UI can explain
	// what buying the answer is worth.
	Why string `json:"why,omitempty"`
}

// BuildQuestion renders an ambiguity the user may have to settle. It returns
// nil when the question would not be worth asking: one option, a decisive
// margin, or a target language that expresses the ambiguity naturally anyway.
func BuildQuestion(id string, a *Anaphora, target string) *Question {
	if a == nil || a.Distribution == nil || a.Committed || a.Answered {
		return nil
	}
	if !a.Distribution.Ambiguous(0.10) {
		return nil
	}
	if a.Margin >= MinMargin {
		return nil
	}
	// A target that can carry the ambiguity on the surface only excuses the
	// question while the referent is not close to being decided; below that the
	// ambiguity would silently change the meaning (plan.md §62).
	if target != "" && targetPreservesAmbiguity(target) && a.Margin >= naturalAmbiguityMargin {
		return nil
	}
	surface := a.Surface
	if surface == "" {
		surface = string(a.ZeroID)
	}
	q := &Question{
		ID: id, Kind: "zero_anaphora",
		Question: fmt.Sprintf("「%s」は誰を指しますか。", surface),
		Why:      fmt.Sprintf("referent margin is only %.2f", a.Margin),
	}
	for _, r := range a.Distribution.Top(0) {
		label := r.Option
		if label == UnknownOption {
			label = "Unknown / not one of the above"
		}
		q.Options = append(q.Options, QuestionOption{Key: r.Option, Label: label, Probability: r.P})
	}
	if len(q.Options) < 2 {
		return nil
	}
	if a.QuestionID != "" {
		q.ID = a.QuestionID
	}
	return q
}

// targetPreservesAmbiguity reports whether the target language can keep the
// ambiguity on the surface. A plain-English document can: "he looked happy"
// after a two-participant clause is ambiguous, so asking would be noise.
func targetPreservesAmbiguity(target string) bool {
	switch target {
	case "en":
		return true
	}
	return false
}

// Resolver builds zero-argument state against a document store and keeps the
// open questions so the HTTP layer can settle them later.
type Resolver struct {
	store *Store
	// MinMargin is the commitment threshold for this resolver.
	MinMargin float64
}

// NewResolver returns a resolver reading from s.
func NewResolver(s *Store) *Resolver {
	return &Resolver{store: s, MinMargin: MinMargin}
}

// Resolve builds (or refreshes) the referent state of a zero argument. The
// candidate set comes from the document store, the prior from discourse
// salience, and posterior is the oracle's judgement if the pipeline has one.
func (r *Resolver) Resolve(g *jlir.Graph, zero jlir.ID, posterior map[string]float64) *Anaphora {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.resolveZero(g, zero, posterior, r.MinMargin)
}

// Open returns the questions that are currently worth asking.
func (r *Resolver) Open() []*Anaphora {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.OpenAnaphora()
}

// Pending lists every zero argument seen so far that is still unresolved.
func (r *Resolver) Pending() []*Anaphora {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.PendingAnaphora()
}

// CandidatePool returns the live entities a zero argument could refer to.
func (r *Resolver) CandidatePool() []jlir.ID {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Entities2IDs()
}
