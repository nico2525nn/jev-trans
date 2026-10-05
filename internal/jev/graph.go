package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/ontology"
	"github.com/nico/jev-trans/internal/trace"
)

// Decision-graph nodes (plan.md §30). The set is fixed: a decision that does
// not belong to one of these ten stages does not belong to the graph.
const (
	StageLexical      = "D0"
	StageSyntactic    = "D1"
	StagePredicate    = "D2"
	StageRoles        = "D3"
	StageCoref        = "D4"
	StageDiscourse    = "D5"
	StagePragmatic    = "D6"
	StageProjection   = "D7"
	StageConstruction = "D8"
	StageRanking      = "D9"
)

// Stages is the dependency order of the graph.
var Stages = []string{
	StageLexical, StageSyntactic, StagePredicate, StageRoles, StageCoref,
	StageDiscourse, StagePragmatic, StageProjection, StageConstruction,
	StageRanking,
}

// Decision actions: what a decision commits to in the JLIR.
const (
	ActionSegmentation = "segmentation"
	ActionScope        = "scope"
	ActionReading      = "reading"
	ActionSense        = "sense"
	ActionRole         = "role"
	ActionReferent     = "referent"
	ActionPragmatic    = "pragmatic"
	ActionProjection   = "projection"
	ActionConstruction = "construction"
	ActionRanking      = "ranking"
)

// ladderMaxOptions is the width at which a candidate set stops being one
// question and becomes a ladder walk (plan.md §33). It sits well below the 255
// option ceiling: a 200-way choice is not a question anyone answers well.
const ladderMaxOptions = 32

// ErrDependencies is returned when a node is asked to run before the nodes it
// depends on have produced results. plan.md §30 requires a DAG, and a DAG that
// lets a node run early is not a DAG.
var ErrDependencies = errors.New("jev: decision node dependencies are not satisfied")

// Scheduler implements information-gain scheduling (plan.md §34). Its whole job
// is to prevent questions whose answer cannot change the translation.
type Scheduler struct {
	// Margin is the probability gap above which the prior is considered
	// decisive and the decision is not worth a call.
	Margin float64
	// Floor is the minimum priority below which a decision is not worth a
	// call even though it is formally undecided.
	Floor float64
	// Target names the projection language in the skip reasons.
	Target lang.Lang
}

// NewScheduler returns the default scheduling policy for a translation into
// target.
func NewScheduler(target lang.Lang) *Scheduler {
	return &Scheduler{Margin: 0.35, Floor: 0.05, Target: target}
}

// Priority is the scheduling weight of a decision: its expected translation
// impact times its uncertainty. A nil decision has no priority, and neither
// has a decision with a single option — nothing is uncertain about a choice
// that was never a choice.
func (s *Scheduler) Priority(d *Decision) float64 {
	if d == nil {
		return 0
	}
	impact := d.Impact
	if impact <= 0 {
		impact = 0.25
	}
	return impact * uncertainty(d.Answer.Probabilities)
}

// Worth reports whether a request deserves an oracle call, and why not when it
// does not. The order of the tests is the order of the plan's examples: first
// there must be an alternative at all, then the alternative must matter, then
// it must be uncertain enough to be worth a call.
func (s *Scheduler) Worth(rq Request) (bool, string) {
	if s == nil {
		return true, ""
	}
	keys := criterionKeys(rq)
	if rq.Kind != KindNoul && len(keys) < 2 {
		return false, "no alternatives to choose between"
	}
	if reason := s.sameRealization(rq); reason != "" {
		return false, reason
	}
	if rq.Kind == KindChoice && s.Margin > 0 {
		if m := priorMargin(rq); m >= s.Margin {
			return false, fmt.Sprintf("prior already decisive (margin %.2f >= %.2f)", m, s.Margin)
		}
	}
	if s.Floor > 0 {
		p := s.Priority(priorDecision(rq, ""))
		if p < s.Floor {
			return false, fmt.Sprintf("expected translation impact x uncertainty %.3f below floor %.3f", p, s.Floor)
		}
	}
	return true, ""
}

// sameRealization is the literal example from plan.md §34: P(A)=0.51,
// P(B)=0.49 is not worth a call if A and B produce the same English string.
func (s *Scheduler) sameRealization(rq Request) string {
	if len(rq.Realizations) < 2 {
		return ""
	}
	keys := make([]string, 0, len(rq.Realizations))
	for k := range rq.Realizations {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	first := rq.Realizations[keys[0]]
	for _, k := range keys[1:] {
		if rq.Realizations[k] != first {
			return ""
		}
	}
	target := "the target"
	if s.Target.Valid() {
		target = string(s.Target)
	}
	return fmt.Sprintf("every option realizes identically in %s (%q)", target, first)
}

// Candidate is one target realization offered to D9.
type Candidate struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	Text  string `json:"text,omitempty"`
	// Score is the projection stage's own soft-constraint estimate. It is the
	// only prior D9 has, and it makes the offline ranking a ranking rather
	// than a coin flip.
	Score float64 `json:"score,omitempty"`
}

// Input is the analysis a graph run reasons over.
type Input struct {
	// Source is the current reading. Nil means "no analysis", and every node
	// then simply produces no decisions.
	Source *jlir.Graph
	// Readings is the source semantic forest. More than one reading is itself
	// a D1 ambiguity.
	Readings []*jlir.Graph
	// Candidates are the target realizations to rank at D9.
	Candidates []Candidate
	// Target is the projection language, SourceLang the analysed one.
	Target     lang.Lang
	SourceLang lang.Lang
	Style      string
	DocID      string
}

// InteractiveQuestion is a question for the user (plan.md §62). It is emitted
// only when the decision could not be resolved, still matters, and has few
// enough options for a human to answer in one click.
type InteractiveQuestion struct {
	ID       string   `json:"id"`
	Stage    string   `json:"stage"`
	Question string   `json:"question"`
	Why      string   `json:"why,omitempty"`
	Options  []Option `json:"options"`
	Answered string   `json:"answered,omitempty"`
}

// GraphOptions configures a decision graph.
type GraphOptions struct {
	// Client is the oracle client. Nil installs an offline client so that a
	// graph always runs.
	Client *Client
	// Registry is the ontology used for sense candidates and the ladder. Nil
	// installs an empty registry, which makes D2/D8 inert rather than broken.
	Registry *ontology.Registry
	// Cache overrides the client's decision cache. The client owns the cache;
	// this is here so a caller can build one and share it explicitly.
	Cache *Cache

	// Target is the projection language, Source the analysed one. They decide
	// which questions are worth asking at all.
	Target lang.Lang
	Source lang.Lang
	Style  string

	// Margin, Floor and InteractiveFloor tune the scheduler. Zero takes the
	// default from NewScheduler.
	Margin           float64
	Floor            float64
	InteractiveFloor float64
	// MaxQuestions caps how many interactive questions one run may emit, so a
	// user is never flooded (plan.md §62: only when genuinely needed).
	MaxQuestions int

	// StateBudget caps the state payload sent with each node's batch.
	StateBudget int
}

// Graph is the decision DAG. It is not safe for concurrent Run calls: a
// document is translated one run at a time.
type Graph struct {
	client  *Client
	reg     *ontology.Registry
	sched   *Scheduler
	opts    GraphOptions
	nodes   []node
	byStage map[string]*node

	mu        sync.Mutex
	done      map[string]bool
	decisions []*Decision
	questions []*InteractiveQuestion
	answers   map[string]string
	src       *jlir.Graph
}

// node is one stage of the DAG.
type node struct {
	stage   string
	title   string
	deps    []string
	impact  float64
	collect func(*nodeCtx) []*Request
}

// nodeCtx is everything a collector may look at.
type nodeCtx struct {
	g       *Graph
	in      *Input
	src     *jlir.Graph
	stage   string
	state   map[string]any
	builder *StateBuilder
}

var emptyRegistry = ontology.New(nil)

func (g *Graph) registry() *ontology.Registry {
	if g != nil && g.reg != nil {
		return g.reg
	}
	return emptyRegistry
}

// registry is the ontology this run reasons over; nil-safe by construction, so
// a graph built without one simply asks fewer questions.
func (nc *nodeCtx) registry() *ontology.Registry {
	if nc != nil && nc.g != nil {
		return nc.g.registry()
	}
	return emptyRegistry
}

func (nc *nodeCtx) target() lang.Lang {
	if nc.in != nil && nc.in.Target.Valid() {
		return nc.in.Target
	}
	if nc.g != nil && nc.g.opts.Target.Valid() {
		return nc.g.opts.Target
	}
	return lang.EN
}

func (nc *nodeCtx) sourceLang() lang.Lang {
	if nc.in != nil && nc.in.SourceLang.Valid() {
		return nc.in.SourceLang
	}
	if nc.src != nil && nc.src.Lang.Valid() {
		return nc.src.Lang
	}
	if nc.g != nil && nc.g.opts.Source.Valid() {
		return nc.g.opts.Source
	}
	return lang.JA
}

func (nc *nodeCtx) style() string {
	if nc.in != nil && nc.in.Style != "" {
		return nc.in.Style
	}
	if nc.g != nil {
		return nc.g.opts.Style
	}
	return ""
}

// stateBytes is the serialised size of the payload this node will send. The
// graph records it so a bloated state is visible rather than mysterious.
func (nc *nodeCtx) stateBytes() int {
	if nc == nil || nc.state == nil {
		return 0
	}
	b, err := json.Marshal(nc.state)
	if err != nil {
		return 0
	}
	return len(b)
}

// answered returns the human answer for a decision, if one was supplied
// through /api/disambiguate.
func (nc *nodeCtx) answered(id string) string {
	if nc == nil || nc.g == nil {
		return ""
	}
	nc.g.mu.Lock()
	defer nc.g.mu.Unlock()
	return nc.g.answers[id]
}

// req stamps the node context onto a request so that no collector has to
// remember the stage, the state or the style.
func (nc *nodeCtx) req(rq Request) *Request {
	rq.Stage = nc.stage
	rq.State = nc.state
	rq.Style = nc.style()
	rq.CachedAllowed = true
	if rq.UserAnswer == "" {
		rq.UserAnswer = nc.answered(rq.ID)
	}
	return &rq
}

// buildState assembles the shared state of a node. The backdrop goes in first
// so that a budget cut removes it first.
func (nc *nodeCtx) buildState() {
	budget := DefaultStateBudget
	if nc.g != nil && nc.g.opts.StateBudget > 0 {
		budget = nc.g.opts.StateBudget
	}
	b := NewState(budget)
	l := nc.sourceLang()
	b.Source(nc.src, l, PriorityBackdrop)
	if nc.in != nil {
		for i, r := range nc.in.Readings {
			if r == nil || r == nc.src || i > 3 {
				continue
			}
			b.Set("reading"+strconv.Itoa(i), SourceSummary(r, l), PriorityContext)
		}
	}
	if nc.src != nil {
		b.Info(nc.src.Info, l, PriorityContext)
		b.Pragmatics(nc.src.Prag, PriorityContext)
	}
	nc.builder = b
	nc.state = b.Build()
}

// dropped reports what the budget cut removed from this node's state.
func (nc *nodeCtx) dropped() []string {
	if nc == nil || nc.builder == nil {
		return nil
	}
	return nc.builder.Dropped()
}

// NewGraph builds the decision DAG.
func NewGraph(opts GraphOptions) *Graph {
	client := opts.Client
	if client == nil {
		client = New(Options{Offline: true})
	}
	reg := opts.Registry
	if reg == nil {
		reg = emptyRegistry
	}
	sched := NewScheduler(opts.Target)
	if opts.Margin > 0 {
		sched.Margin = opts.Margin
	}
	if opts.Floor > 0 {
		sched.Floor = opts.Floor
	}
	if opts.Target.Valid() {
		sched.Target = opts.Target
	}
	if opts.InteractiveFloor <= 0 {
		opts.InteractiveFloor = 0.6
	}
	if opts.MaxQuestions <= 0 {
		opts.MaxQuestions = 8
	}
	g := &Graph{
		client:  client,
		reg:     reg,
		sched:   sched,
		opts:    opts,
		byStage: map[string]*node{},
		done:    map[string]bool{},
		answers: map[string]string{},
	}
	g.nodes = g.buildNodes()
	for i := range g.nodes {
		g.byStage[g.nodes[i].stage] = &g.nodes[i]
	}
	return g
}

// buildNodes fixes the DAG: D0 feeds D1 feeds D2 … feeds D9. A decision that
// depends on an earlier decision's result happens later, which is the entire
// point of declaring the graph at all.
func (g *Graph) buildNodes() []node {
	return []node{
		{stage: StageLexical, title: "lexical segmentation", impact: 0.35, collect: collectLexical},
		{stage: StageSyntactic, title: "syntactic ambiguities", deps: []string{StageLexical}, impact: 0.7, collect: collectSyntactic},
		{stage: StagePredicate, title: "predicate senses", deps: []string{StageSyntactic}, impact: 0.75, collect: collectPredicate},
		{stage: StageRoles, title: "semantic roles", deps: []string{StagePredicate}, impact: 0.7, collect: collectRoles},
		{stage: StageCoref, title: "coreference / zero anaphora", deps: []string{StageRoles}, impact: 0.95, collect: collectCoref},
		{stage: StageDiscourse, title: "discourse interpretation", deps: []string{StageCoref}, impact: 0.9, collect: collectDiscourse},
		{stage: StagePragmatic, title: "pragmatic interpretation", deps: []string{StageDiscourse}, impact: 0.5, collect: collectPragmatic},
		{stage: StageProjection, title: "target projection choices", deps: []string{StagePragmatic}, impact: 0.8, collect: collectProjection},
		{stage: StageConstruction, title: "construction selection", deps: []string{StageProjection}, impact: 0.75, collect: collectConstruction},
		{stage: StageRanking, title: "final ranking", deps: []string{StageConstruction}, impact: 0.6, collect: collectRanking},
	}
}

// Node is the public view of one DAG node.
type Node struct {
	Stage  string   `json:"stage"`
	Title  string   `json:"title"`
	Deps   []string `json:"deps"`
	Impact float64  `json:"impact"`
	Done   bool     `json:"done"`
}

// Nodes returns the graph structure in dependency order.
func (g *Graph) Nodes() []Node {
	if g == nil {
		return nil
	}
	out := make([]Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, Node{
			Stage: n.stage, Title: n.title, Deps: n.deps,
			Impact: n.impact, Done: g.done[n.stage],
		})
	}
	return out
}

// Ready reports whether every dependency of stage has produced results.
func (g *Graph) Ready(stage string) bool {
	if g == nil {
		return false
	}
	n := g.byStage[stage]
	if n == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, d := range n.deps {
		if !g.done[d] {
			return false
		}
	}
	return true
}

// Report is the outcome of a graph run.
type Report struct {
	ByStage      map[string][]*Decision `json:"byStage"`
	Decisions    []*Decision            `json:"decisions"`
	Questions    []*InteractiveQuestion `json:"questions"`
	Asked        int                    `json:"asked"`
	Cached       int                    `json:"cached"`
	Skipped      int                    `json:"skipped"`
	Prior        int                    `json:"prior"`
	User         int                    `json:"user"`
	Failed       int                    `json:"failed"`
	OracleCalls  int                    `json:"oracleCalls"`
	LatencyMS    float64                `json:"latencyMs"`
	InputTokens  int                    `json:"inputTokens"`
	OutputTokens int                    `json:"outputTokens"`
}

// Views renders every decision for the trace.
func (r *Report) Views() []trace.DecisionView {
	if r == nil {
		return nil
	}
	out := make([]trace.DecisionView, 0, len(r.Decisions))
	for _, d := range r.Decisions {
		out = append(out, d.View())
	}
	return out
}

// Decision finds one decision by ID.
func (r *Report) Decision(id string) *Decision {
	if r == nil {
		return nil
	}
	for _, d := range r.Decisions {
		if d != nil && d.ID == id {
			return d
		}
	}
	return nil
}

// Run executes the whole graph in dependency order.
func (g *Graph) Run(ctx context.Context, rec *trace.Recorder, in Input) (*Report, error) {
	if g == nil {
		return nil, errors.New("jev: nil decision graph")
	}
	if rec == nil {
		rec = trace.New("jev decision graph")
	}
	rep := &Report{ByStage: map[string][]*Decision{}}
	for _, n := range g.nodes {
		if !g.Ready(n.stage) {
			span := rec.Open(trace.StageDecisions, n.stage+" "+n.title)
			span.Status(trace.StatusError)
			span.Note("refusing to run %s: dependencies %v have not produced results", n.stage, n.deps)
			span.Close()
			return rep, fmt.Errorf("%w: %s needs %v", ErrDependencies, n.stage, n.deps)
		}
		if err := g.runNode(ctx, rec, in, n, rep); err != nil {
			return rep, err
		}
	}
	g.settleZeros(rec)
	g.noteTotals(rec, rep)
	return rep, nil
}

// RunStage executes one node, refusing it when its dependencies have not run.
func (g *Graph) RunStage(ctx context.Context, rec *trace.Recorder, in Input, stage string) ([]*Decision, error) {
	if g == nil {
		return nil, errors.New("jev: nil decision graph")
	}
	if rec == nil {
		rec = trace.New("jev decision graph")
	}
	n := g.byStage[stage]
	if n == nil {
		return nil, fmt.Errorf("jev: unknown decision node %q", stage)
	}
	if !g.Ready(stage) {
		span := rec.Open(trace.StageDecisions, stage+" "+n.title)
		span.Status(trace.StatusError)
		span.Note("refusing to run %s: dependencies %v have not produced results", stage, n.deps)
		span.Close()
		return nil, fmt.Errorf("%w: %s needs %v", ErrDependencies, stage, n.deps)
	}
	rep := &Report{ByStage: map[string][]*Decision{}}
	err := g.runNode(ctx, rec, in, *n, rep)
	return rep.ByStage[stage], err
}

// runNode is the body of one stage: collect, schedule, ask, apply, record.
func (g *Graph) runNode(ctx context.Context, rec *trace.Recorder, in Input, n node, rep *Report) error {
	span := rec.Open(trace.StageDecisions, n.stage+" "+n.title)
	defer span.Close()

	nc := &nodeCtx{g: g, in: &in, src: g.currentSource(in), stage: n.stage}
	g.clearStage(n.stage)
	nc.buildState()
	span.Count("stateBytes", nc.stateBytes())
	span.Label("stateBudget", strconv.Itoa(stateBudgetOf(g)))
	if dropped := nc.dropped(); len(dropped) > 0 {
		span.Note("state trimmed to %d bytes; dropped %s", nc.stateBytes(), strings.Join(dropped, ", "))
	}

	var rqs []*Request
	if n.collect != nil {
		rqs = n.collect(nc)
	}
	if len(rqs) == 0 {
		span.Detail("no ambiguity at %s; nothing to ask", n.stage)
		g.markDone(n.stage, nil)
		return nil
	}

	var todo, ladders []Request
	for i, r := range rqs {
		if r == nil {
			continue
		}
		rq := *r
		if rq.ID == "" {
			rq.ID = n.stage + "/d" + strconv.Itoa(i+1)
		}
		if rq.Impact <= 0 {
			rq.Impact = n.impact
		}
		rq.CachedAllowed = true
		if rq.UserAnswer == "" {
			rq.UserAnswer = nc.answered(rq.ID)
		}
		if ok, reason := g.sched.Worth(rq); !ok {
			g.record(nc, rep, span, g.client.Skip(rq, reason))
			continue
		}
		if len(rq.Candidates) > ladderMaxOptions {
			ladders = append(ladders, rq)
			continue
		}
		todo = append(todo, rq)
	}
	SortRequestsByPriority(todo)

	asked := 0
	if len(todo) > 0 {
		ds, err := g.client.AskBatch(ctx, nc.state, todo)
		if err != nil {
			span.Status(trace.StatusError)
			span.Note("decision batch at %s aborted: %v", n.stage, err)
			g.markDone(n.stage, nil)
			return err
		}
		for _, d := range ds {
			if d == nil {
				continue
			}
			g.apply(nc, d)
			g.record(nc, rep, span, d)
			asked++
		}
	}
	for _, rq := range ladders {
		ds, err := g.walkLadder(ctx, nc, rep, span, rq)
		if err != nil {
			span.Status(trace.StatusError)
			span.Note("ladder at %s aborted: %v", n.stage, err)
			g.markDone(n.stage, nil)
			return err
		}
		asked += len(ds)
	}
	span.Data(map[string]any{
		"stage":      n.stage,
		"title":      n.title,
		"candidates": len(rqs),
		"decided":    asked,
		"priority":   nodePriority(g, rep, n.stage),
	})
	g.markDone(n.stage, nil)
	return nil
}

// walkLadder resolves a candidate set too wide for one question by asking the
// coarse levels first and narrowing (plan.md §33, and the 255 option ceiling).
// Each level is one cheap question; the point is to never ask the 200-way
// question at all.
func (g *Graph) walkLadder(ctx context.Context, nc *nodeCtx, rep *Report, span *trace.Span, rq Request) ([]*Decision, error) {
	var out []*Decision
	cur := dedupe(rq.Candidates)
	prior := rq.Prior
	levels := 0
	for len(cur) > ladderMaxOptions {
		parents := g.parents(cur)
		if len(parents) == 0 || len(parents) >= len(cur) {
			break
		}
		step := rq
		step.ID = rq.ID + "/family" + strconv.Itoa(levels+1)
		step.Instructions = rq.Instructions + " Answer with the predicate family."
		step.Criteria = nc.senseCriteria(parents, prior)
		step.Prior = projectPrior(prior, parents)
		step.Candidates = append([]string(nil), parents...)
		levels++
		ds, err := g.client.AskBatch(ctx, nc.state, []Request{step})
		if err != nil {
			return out, err
		}
		d := firstDecision(ds)
		if d == nil {
			break
		}
		d.LadderLevels = append(append([]string(nil), d.LadderLevels...), parents...)
		g.apply(nc, d)
		g.record(nc, rep, span, d)
		out = append(out, d)
		if d.Source != SourceJev && d.Source != SourceCache && d.Source != SourceUser {
			// The level is unresolved; asking the next level on top of an
			// unresolved one would compound a guess.
			return out, nil
		}
		kids := g.reg.Families(d.Winner())
		narrowed := intersect(kids, cur)
		if len(narrowed) == 0 {
			narrowed = []string{d.Winner()}
		}
		cur = narrowed
		prior = projectPrior(prior, cur)
	}
	if len(cur) < 2 {
		return out, nil
	}
	final := rq
	final.Criteria = nc.senseCriteria(cur, prior)
	final.Prior = prior
	final.Candidates = append([]string(nil), cur...)
	ds, err := g.client.AskBatch(ctx, nc.state, []Request{final})
	if err != nil {
		return out, err
	}
	d := firstDecision(ds)
	if d == nil {
		return out, nil
	}
	for i, l := range cur {
		if i == 0 {
			d.LadderLevels = append(d.LadderLevels, l)
		}
	}
	g.apply(nc, d)
	g.record(nc, rep, span, d)
	return append(out, d), nil
}

// parents collapses a candidate set onto the ontology families that contain it.
func (g *Graph) parents(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		p := id
		if s, ok := g.reg.Sense(id); ok && s != nil {
			if pa := s.Parent(); pa != "" {
				if _, known := g.reg.Sense(pa); known {
					p = pa
				}
			}
		}
		out = append(out, p)
	}
	return dedupe(out)
}

func intersect(a, b []string) []string {
	keep := make(map[string]bool, len(b))
	for _, s := range b {
		keep[s] = true
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if keep[s] {
			out = append(out, s)
		}
	}
	return out
}

func projectPrior(d *jlir.Distribution, keys []string) *jlir.Distribution {
	if d == nil {
		return nil
	}
	w := map[string]float64{}
	for _, k := range keys {
		if p := d.P(k); p > 0 {
			w[k] = p
		}
	}
	return jlir.NewDistribution("prior", w)
}

func firstDecision(ds []*Decision) *Decision {
	for _, d := range ds {
		if d != nil {
			return d
		}
	}
	return nil
}

func stateBudgetOf(g *Graph) int {
	if g != nil && g.opts.StateBudget > 0 {
		return g.opts.StateBudget
	}
	return DefaultStateBudget
}

func nodePriority(g *Graph, rep *Report, stage string) float64 {
	if rep == nil {
		return 0
	}
	best := 0.0
	for _, d := range rep.ByStage[stage] {
		if d != nil && d.Priority > best {
			best = d.Priority
		}
	}
	return round2(best)
}

func (g *Graph) currentSource(in Input) *jlir.Graph {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.src != nil {
		return g.src
	}
	return in.Source
}

func (g *Graph) setSource(in *Input, reading string) {
	if in == nil {
		return
	}
	i, err := strconv.Atoi(strings.TrimPrefix(reading, "g"))
	if err != nil || i < 0 || i >= len(in.Readings) || in.Readings[i] == nil {
		return
	}
	g.mu.Lock()
	g.src = in.Readings[i]
	g.mu.Unlock()
}

// clearStage drops the previous decisions of a node so that a re-run replaces
// them instead of appending a second, contradictory record.
func (g *Graph) clearStage(stage string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	kept := make([]*Decision, 0, len(g.decisions))
	for _, d := range g.decisions {
		if d == nil || d.Stage != stage {
			kept = append(kept, d)
		}
	}
	g.decisions = kept
}

func (g *Graph) markDone(stage string, ds []*Decision) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.done[stage] = true
}

// record stores one decision and says in the trace why it looks the way it
// does. Every decision is recorded: asked, skipped, cached, prior or
// user-answered. A stage that decided something and left no trace is a stage
// the UI cannot audit.
func (g *Graph) record(nc *nodeCtx, rep *Report, span *trace.Span, d *Decision) {
	if d == nil {
		return
	}
	switch d.Source {
	case SourceJev:
		span.Count("asked", 1)
		span.Label("lastOracleAnswer", d.Winner())
	case SourceCache:
		span.Count("cached", 1)
	case SourceSkipped:
		span.Count("skipped", 1)
		span.Note("%s: %s", d.ID, d.SkipReason)
	case SourceUser:
		span.Count("user", 1)
		span.Note("%s: answered by the user (%s)", d.ID, d.Winner())
	default:
		span.Count("prior", 1)
		span.Note("%s: %s", d.ID, d.SkipReason)
	}
	span.Label("candidates", strconv.Itoa(len(d.Options)))

	g.mu.Lock()
	g.decisions = append(g.decisions, d)
	g.mu.Unlock()
	if rep == nil {
		return
	}
	switch d.Source {
	case SourceJev:
		rep.Asked++
		rep.LatencyMS += d.LatencyMS
	case SourceCache:
		rep.Cached++
	case SourceSkipped:
		rep.Skipped++
	case SourceUser:
		rep.User++
	default:
		rep.Prior++
	}
	rep.Decisions = append(rep.Decisions, d)
	if rep.ByStage == nil {
		rep.ByStage = map[string][]*Decision{}
	}
	rep.ByStage[d.Stage] = append(rep.ByStage[d.Stage], d)
	if q := g.interactive(d); q != nil {
		rep.Questions = append(rep.Questions, q)
		g.mu.Lock()
		g.questions = append(g.questions, q)
		g.mu.Unlock()
		span.Note("%s: escalating to the user (%d options)", d.ID, len(q.Options))
	}
}

// interactive decides whether an unresolved decision should become a question
// for the user (plan.md §62). Only a decision that is still unresolved, still
// matters and is small enough to answer in one click qualifies.
func (g *Graph) interactive(d *Decision) *InteractiveQuestion {
	switch d.Source {
	case SourceJev, SourceCache, SourceUser:
		return nil
	}
	if len(d.Options) < 2 || len(d.Options) > 5 {
		return nil
	}
	if d.Priority < g.opts.InteractiveFloor {
		return nil
	}
	if d.SkipReason == "" {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.questions) >= g.opts.MaxQuestions {
		return nil
	}
	return &InteractiveQuestion{
		ID:       d.ID,
		Stage:    d.Stage,
		Question: d.Question,
		Why:      d.SkipReason,
		Options:  append([]Option(nil), d.Options...),
		Answered: g.answers[d.ID],
	}
}

// settleZeros reports whether every zero anaphora could be bound. When they
// could not, the analysis stays ambiguous on purpose and the caller says so
// (plan.md §61, UNDERDETERMINED).
func (g *Graph) settleZeros(rec *trace.Recorder) {
	src := g.currentSource(Input{})
	if src == nil {
		return
	}
	if src.ResolveZero() {
		return
	}
	span := rec.Open(trace.StageDecisions, "zero anaphora settlement")
	span.Status(trace.StatusWarn)
	span.Note("some zero anaphorae remain unresolved; the analysis stays ambiguous rather than guessing")
	span.Close()
}

// noteTotals attaches the oracle accounting to the trace.
func (g *Graph) noteTotals(rec *trace.Recorder, rep *Report) {
	st := g.client.Stats()
	if rep != nil {
		rep.OracleCalls = st.Calls
		rep.Failed = st.Failed
		rep.InputTokens = st.InputTokens
		rep.OutputTokens = st.OutputTokens
	}
	if rec == nil || rep == nil {
		return
	}
	span := rec.Open(trace.StageDecisions, "decision budget")
	span.Label("oracleCalls", strconv.Itoa(st.Calls))
	span.Label("questions", strconv.Itoa(st.Questions))
	span.Label("cacheHits", strconv.Itoa(st.Cache.Hits))
	span.Label("cacheMisses", strconv.Itoa(st.Cache.Misses))
	span.Label("inputTokens", strconv.Itoa(st.InputTokens))
	if !g.client.Enabled() {
		span.Status(trace.StatusWarn)
		span.Note("the decision oracle was not consulted: no API key or offline mode; every decision came from the deterministic analysis priors")
	}
	if len(rep.Questions) == 0 {
		span.Note("no interaction required")
	} else {
		span.Count("interactiveQuestions", len(rep.Questions))
	}
	span.Data(map[string]any{"decisions": len(rep.Decisions), "questions": rep.Questions})
	span.Close()
}

// apply commits an answered decision to the analysis. Only a decision the oracle
// or a human actually answered is applied — a prior never collapses the forest
// (plan.md §58).
func (g *Graph) apply(nc *nodeCtx, d *Decision) {
	if d == nil || nc == nil || nc.src == nil {
		return
	}
	switch d.Source {
	case SourceJev, SourceCache, SourceUser:
	default:
		return
	}
	winner := d.Winner()
	switch d.Action {
	case ActionScope:
		sc := nc.src.Scope(jlir.ID(d.TargetID))
		if sc == nil || len(sc.Readings) == 0 {
			return
		}
		idx := readingIndex(winner)
		if idx < 0 || idx >= len(sc.Readings) {
			return
		}
		for i := range sc.Readings {
			if i == idx {
				sc.Readings[i].Weight = 1
				continue
			}
			sc.Readings[i].Weight = 0
		}
		sc.Resolved = true
	case ActionReferent:
		e := nc.src.Entity(jlir.ID(d.TargetID))
		if e == nil || e.Referent == nil {
			return
		}
		p := d.P(winner)
		if p <= 0 {
			p = 1
		}
		for _, o := range e.Referent.Options {
			e.Referent.Prob[o] = 0
		}
		e.Referent.Prob[winner] = p
		e.Referent.Winner = winner
		e.Referent.Resolved = true
		e.Referent.Normalize()
	case ActionSense:
		if v := nc.src.Event(jlir.ID(d.TargetID)); v != nil && winner != "" {
			v.Predicate = winner
		}
	case ActionRole:
		event, entity, ok := splitID(d.TargetID)
		if !ok {
			return
		}
		v := nc.src.Event(jlir.ID(event))
		if v == nil {
			return
		}
		for r, a := range v.Args {
			if r != winner && jlir.IsRole(r) && jlir.IsRole(winner) && a.Value == jlir.ID(entity) {
				delete(v.Args, r)
			}
		}
		v.Args[winner] = jlir.Arg{Value: jlir.ID(entity), Confidence: clamp01(d.P(winner))}
	case ActionPragmatic:
		switch winner {
		case "ironic":
			nc.src.Prag.IronySuspected = true
		case "literal":
			nc.src.Prag.IronySuspected = false
		}
		if lvl := levelScore(winner); lvl > 0 {
			nc.src.Prag.Formality = clamp01(lvl / MaxScoreLevels)
		}
	case ActionReading:
		g.setSource(nc.in, winner)
	}
}

// Decisions returns every decision recorded so far.
func (g *Graph) Decisions() []*Decision {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*Decision(nil), g.decisions...)
}

// Views renders every decision for the trace.
func (g *Graph) Views() []trace.DecisionView {
	ds := g.Decisions()
	out := make([]trace.DecisionView, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.View())
	}
	return out
}

// Questions returns the questions that still need a human.
func (g *Graph) Questions() []*InteractiveQuestion {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*InteractiveQuestion(nil), g.questions...)
}

func (g *Graph) question(id string) *InteractiveQuestion {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, q := range g.questions {
		if q != nil && q.ID == id {
			return q
		}
	}
	return nil
}

// ApplyAnswer records a human answer to an interactive question. Resolve is
// what actually refreshes the decision; this is only the intent.
func (g *Graph) ApplyAnswer(questionID, option string) bool {
	if g == nil || questionID == "" {
		return false
	}
	q := g.question(questionID)
	if q == nil {
		return false
	}
	g.mu.Lock()
	g.answers[questionID] = option
	for _, qq := range g.questions {
		if qq != nil && qq.ID == questionID {
			qq.Answered = option
		}
	}
	g.mu.Unlock()
	return true
}

// Resolve applies a human answer and re-runs the stage that asked, returning
// the refreshed decision. This is what unblocks a run after /api/disambiguate.
func (g *Graph) Resolve(ctx context.Context, rec *trace.Recorder, in Input, questionID, option string) (*Decision, error) {
	q := g.question(questionID)
	if q == nil {
		return nil, fmt.Errorf("jev: unknown question %q", questionID)
	}
	if !offersOption(q, option) {
		return nil, fmt.Errorf("jev: %q is not an option of question %q", option, questionID)
	}
	g.ApplyAnswer(questionID, option)
	ds, err := g.RunStage(ctx, rec, in, q.Stage)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		if d != nil && d.ID == questionID {
			return d, nil
		}
	}
	return nil, fmt.Errorf("jev: question %q produced no decision on re-run", questionID)
}

func offersOption(q *InteractiveQuestion, option string) bool {
	if q == nil || option == "" {
		return false
	}
	for _, o := range q.Options {
		if o.Key == option {
			return true
		}
	}
	return false
}

// SortRequestsByPriority orders pending requests so the questions with the most
// at stake are inside the first batch.
func SortRequestsByPriority(rs []Request) {
	prio := make(map[string]float64, len(rs))
	for _, r := range rs {
		prio[r.ID+"\x00"+r.Instructions] = r.Impact * priorUncertainty(r)
	}
	sort.SliceStable(rs, func(i, j int) bool {
		pi := prio[rs[i].ID+"\x00"+rs[i].Instructions]
		pj := prio[rs[j].ID+"\x00"+rs[j].Instructions]
		if pi != pj {
			return pi > pj
		}
		return rs[i].ID < rs[j].ID
	})
}

func priorUncertainty(rq Request) float64 {
	keys := criterionKeys(rq)
	if len(keys) < 2 {
		return 0
	}
	prob := map[string]float64{}
	for _, cr := range rq.Criteria {
		p := cr.Prior
		if rq.Prior != nil && rq.Prior.P(cr.Key) > 0 {
			p = rq.Prior.P(cr.Key)
		}
		if p > 0 {
			prob[cr.Key] = p
		}
	}
	if len(prob) == 0 {
		return 1
	}
	return uncertainty(normalizeProbabilities(prob, keys, nil))
}

func priorMargin(rq Request) float64 {
	keys := criterionKeys(rq)
	if len(keys) < 2 {
		return 1
	}
	weights := map[string]float64{}
	for _, k := range keys {
		p := 0.0
		if rq.Prior != nil {
			p = rq.Prior.P(k)
		}
		if p <= 0 {
			for _, cr := range rq.Criteria {
				if cr.Key == k {
					p = cr.Prior
				}
			}
		}
		if p > 0 {
			weights[k] = p
		}
	}
	return jlir.NewDistribution("prior", weights).Margin()
}

func readingIndex(key string) int {
	if !strings.HasPrefix(key, "r") {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimPrefix(key, "r"))
	if err != nil {
		return -1
	}
	return n
}

func splitID(s string) (first, second string, ok bool) {
	i := strings.Index(s, "|")
	if i <= 0 || i >= len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

func scopeIsPolaritySensitive(sc *jlir.ScopeNode) bool {
	if sc == nil {
		return false
	}
	switch sc.Kind {
	case jlir.ScopeNot, jlir.ScopeAll, jlir.ScopeSome, jlir.ScopeNone:
		return true
	}
	for _, r := range sc.Readings {
		for _, o := range r.Order {
			for _, op := range sc.Operands {
				if o == op {
					switch op {
					case jlir.ScopeNot, jlir.ScopeAll, jlir.ScopeSome, jlir.ScopeNone:
						return true
					}
				}
			}
		}
	}
	return false
}

// --- node collectors --------------------------------------------------------

// D0: lexical segmentation. The analysis already made a deterministic choice;
// this asks only where that choice is genuinely doubtful, which is where an
// expression survived every dictionary lookup (plan.md §25).
func collectLexical(nc *nodeCtx) []*Request {
	if nc.src == nil {
		return nil
	}
	var out []*Request
	for _, u := range nc.src.SourceFeat.Unknowns {
		if u.Surface == "" {
			continue
		}
		runs := scriptRuns(u.Surface)
		if len(runs) < 2 {
			continue
		}
		crits := []Criterion{{
			Key:         "whole",
			Prior:       0.6,
			Description: "「" + u.Surface + "」 is one lexical unit",
		}}
		share := 0.4 / float64(len(runs))
		for i, r := range runs {
			crits = append(crits, Criterion{
				Key:         "split" + strconv.Itoa(i+1),
				Prior:       share,
				Description: "「" + u.Surface[:r.start] + "」 + 「" + u.Surface[r.start:] + "」",
			})
		}
		out = append(out, nc.req(Request{
			ID:              "D0/" + u.Surface,
			Kind:            KindChoice,
			Instructions:    "How should the unresolved expression 「" + u.Surface + "」 be segmented?",
			Criteria:        crits,
			Impact:          0.35,
			Action:          ActionSegmentation,
			TargetID:        u.Surface,
			SemanticContext: "segmentation:" + u.Surface,
		}))
	}
	return out
}

type textRun struct{ start, end int }

// scriptRuns returns the maximal same-script runs of s. A Japanese expression
// that mixes scripts is the classic segmentation risk, so the boundaries are
// what the question is about.
func scriptRuns(s string) []textRun {
	var out []textRun
	start, cur := 0, lang.ScriptOther
	seen := false
	for i, r := range s {
		sc := lang.RuneScript(r)
		if sc == lang.ScriptOther {
			continue
		}
		if !seen {
			cur, start, seen = sc, i, true
			continue
		}
		if sc != cur {
			out = append(out, textRun{start, i})
			cur, start = sc, i
		}
	}
	if seen {
		out = append(out, textRun{start, len(s)})
	}
	return out
}

// D1: syntactic ambiguities — the source semantic forest and the scope graph,
// including the NOT > ALL versus ALL > NOT reading that flips the meaning of a
// quantified clause.
func collectSyntactic(nc *nodeCtx) []*Request {
	var out []*Request
	if nc.in != nil && len(nc.in.Readings) > 1 {
		crits := make([]Criterion, 0, len(nc.in.Readings))
		weights := map[string]float64{}
		for i, r := range nc.in.Readings {
			if r == nil {
				continue
			}
			matrix := ""
			if m := r.Matrix(); m != nil {
				matrix = m.Predicate
			}
			crits = append(crits, Criterion{
				Key:         "g" + strconv.Itoa(i),
				Prior:       r.Weight,
				Description: fmt.Sprintf("reading %d: matrix predicate %s, %d entities", i, orDash(matrix), len(r.Entities)),
			})
			weights["g"+strconv.Itoa(i)] = r.Weight
		}
		if len(crits) > 1 {
			out = append(out, nc.req(Request{
				ID:              "D1/reading",
				Kind:            KindChoice,
				Instructions:    "Which structural reading of the sentence does the context support?",
				Criteria:        crits,
				Prior:           jlir.NewDistribution("source_forest", weights),
				Impact:          0.8,
				Action:          ActionReading,
				SemanticContext: "reading:" + readingContext(nc.in.Readings),
			}))
		}
	}
	if nc.src == nil {
		return out
	}
	for _, sc := range nc.src.Scopes {
		if sc == nil || sc.Resolved || len(sc.Readings) < 2 {
			continue
		}
		counts := map[string]int{}
		for _, r := range sc.Readings {
			counts[readingLabel(sc, 0, r)]++
		}
		crits := make([]Criterion, 0, len(sc.Readings))
		weights := map[string]float64{}
		for i, r := range sc.Readings {
			label := readingLabel(sc, i, r)
			if counts[label] > 1 {
				label = label + " [" + strings.Join(r.Order, " ") + "]"
			}
			key := "r" + strconv.Itoa(i)
			crits = append(crits, Criterion{Key: key, Description: label, Prior: r.Weight})
			weights[key] = r.Weight
		}
		impact := 0.6
		if scopeIsPolaritySensitive(sc) {
			impact = 0.95
		}
		out = append(out, nc.req(Request{
			ID:              "D1/" + string(sc.ID),
			Kind:            KindChoice,
			Instructions:    "Which ordering of " + sc.Kind + " over " + strings.Join(sc.Operands, " ") + " does the sentence support?",
			Criteria:        crits,
			Prior:           jlir.NewDistribution("scope_reading", weights),
			Impact:          impact,
			Action:          ActionScope,
			TargetID:        string(sc.ID),
			SemanticContext: "scope:" + string(sc.ID) + ":" + scopeContextKey(sc),
		}))
	}
	return out
}

func readingContext(readings []*jlir.Graph) string {
	parts := make([]string, 0, len(readings))
	for _, r := range readings {
		if r == nil {
			continue
		}
		pred := ""
		if m := r.Matrix(); m != nil {
			pred = m.Predicate
		}
		parts = append(parts, pred)
	}
	return strings.Join(parts, "|")
}

func scopeContextKey(sc *jlir.ScopeNode) string {
	parts := make([]string, 0, len(sc.Readings))
	for _, r := range sc.Readings {
		parts = append(parts, readingLabel(sc, 0, r))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// D2: predicate senses. The candidate set comes from the ontology, and a set
// wider than one question may carry is walked as a ladder (plan.md §33).
func collectPredicate(nc *nodeCtx) []*Request {
	if nc.src == nil {
		return nil
	}
	var out []*Request
	for _, v := range nc.src.Events {
		if v == nil {
			continue
		}
		cands, prior := nc.senseCandidates(v)
		if len(cands) < 2 {
			continue
		}
		out = append(out, nc.req(Request{
			ID:              "D2/" + string(v.ID),
			Kind:            KindChoice,
			Instructions:    "Which predicate sense does 「" + orDash(v.Predicate) + "」 express here?",
			Criteria:        nc.senseCriteria(cands, prior),
			Prior:           prior,
			Candidates:      cands,
			Impact:          0.75,
			Action:          ActionSense,
			TargetID:        string(v.ID),
			SemanticContext: "predicate:" + v.Predicate + ":" + strings.Join(cands, ","),
		}))
	}
	return out
}

// senseCandidates returns the senses the analysis cannot choose between. A
// predicate the ontology knows and that no event feature contradicts is not a
// question; a predicate the ontology does not know, or one that several senses
// of the same family fit equally, is.
func (nc *nodeCtx) senseCandidates(v *jlir.Event) ([]string, *jlir.Distribution) {
	reg := nc.registry()
	if v == nil || reg == nil {
		return nil, nil
	}
	sense, known := reg.Sense(v.Predicate)
	var pool []string
	if known && sense != nil {
		pool = append(pool, sense.Children...)
		if p := sense.Parent(); p != "" {
			pool = append(pool, reg.Families(p)...)
		}
	}
	if len(pool) < 2 && !known {
		pool = append(pool, reg.Roots()...)
	}
	pool = dedupe(pool)
	if len(pool) < 2 {
		return nil, nil
	}
	weights := map[string]float64{}
	best, second := 0.0, 0.0
	for _, id := range pool {
		score := senseScore(reg.MustSense(id), v)
		if score <= 0 {
			continue
		}
		weights[id] = score
		switch {
		case score > best:
			second = best
			best = score
		case score > second:
			second = score
		}
	}
	if known && best-second > 0.25 {
		return nil, nil
	}
	if best <= 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(weights))
	for id := range weights {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, jlir.NewDistribution("ontology_features", weights)
}

func (nc *nodeCtx) senseCriteria(ids []string, prior *jlir.Distribution) []Criterion {
	reg := nc.registry()
	out := make([]Criterion, 0, len(ids))
	for _, id := range ids {
		desc := id
		if s, ok := reg.Sense(id); ok && s != nil {
			if s.Gloss != "" {
				desc = s.Name + ": " + s.Gloss
			} else if s.Name != "" {
				desc = s.Name
			}
		}
		out = append(out, Criterion{Key: id, Description: desc, Prior: prior.P(id)})
	}
	return out
}

// senseScore counts how well a sense matches the features the analysis found.
// It is deterministic and derived from the analysis, which is what makes the
// offline path a weak opinion rather than a coin flip.
func senseScore(s *ontology.Sense, v *jlir.Event) float64 {
	if s == nil || v == nil {
		return 0
	}
	score := 1.0
	for _, f := range v.Features {
		got, ok := s.Features[f.Key]
		if !ok {
			continue
		}
		if got == jlir.ValueString(f.Value) {
			score++
			continue
		}
		score--
	}
	if score <= 0 {
		return 0
	}
	return score
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// roleRivals are the role pairs the surface genuinely confuses: the same
// nominative marking in Japanese, the same preposition in English.
var roleRivals = map[string]bool{
	jlir.RoleAgent + "|" + jlir.RolePatient:         true,
	jlir.RoleGoal + "|" + jlir.RoleLocation:         true,
	jlir.RoleSource + "|" + jlir.RoleTheme:          true,
	jlir.RoleRecipient + "|" + jlir.RoleBeneficiary: true,
	jlir.RoleExperiencer + "|" + jlir.RoleStimulus:  true,
	jlir.RoleTheme + "|" + jlir.RolePatient:         true,
}

func rivalRoles(a, b string) bool {
	if a > b {
		a, b = b, a
	}
	return roleRivals[a+"|"+b]
}

// D3: semantic roles. Two roles the surface cannot separate, or one role the
// analysis filled with low confidence where a rival was available.
func collectRoles(nc *nodeCtx) []*Request {
	if nc.src == nil {
		return nil
	}
	reg := nc.registry()
	var out []*Request
	for _, v := range nc.src.Events {
		if v == nil || len(v.Args) == 0 {
			continue
		}
		roles := make([]string, 0, len(v.Args))
		for r := range v.Args {
			roles = append(roles, r)
		}
		sort.Strings(roles)
		asked := map[string]bool{}
		for i := range roles {
			for j := i + 1; j < len(roles); j++ {
				r1, r2 := roles[i], roles[j]
				if !rivalRoles(r1, r2) {
					continue
				}
				a1, a2 := v.Args[r1], v.Args[r2]
				if a1.Value != a2.Value || minFloat(a1.Confidence, a2.Confidence) > 0.9 {
					continue
				}
				key := string(v.ID) + "|" + string(a1.Value)
				if asked[key] {
					continue
				}
				asked[key] = true
				out = append(out, roleRequest(nc, reg, v, string(a1.Value), []string{r1, r2}, a1, a2))
			}
		}
		for _, r := range roles {
			a := v.Args[r]
			if a.Confidence > 0.6 {
				continue
			}
			for _, other := range jlir.AllRoles {
				if other == r || !rivalRoles(r, other) {
					continue
				}
				if _, filled := v.Args[other]; filled {
					continue
				}
				key := string(v.ID) + "|" + string(a.Value)
				if asked[key] {
					continue
				}
				asked[key] = true
				out = append(out, roleRequest(nc, reg, v, string(a.Value), []string{r, other}, a, a))
			}
		}
	}
	return out
}

func roleRequest(nc *nodeCtx, reg *ontology.Registry, v *jlir.Event, entity string, options []string, args ...jlir.Arg) *Request {
	sort.Strings(options)
	weights := map[string]float64{}
	crits := make([]Criterion, 0, len(options))
	sense := reg.MustSense(v.Predicate)
	for _, r := range options {
		w := 0.5
		desc := r
		if sense != nil {
			for _, a := range sense.Args {
				if a.Role != r {
					continue
				}
				desc = describeRole(r, a.Default, sense)
				if a.Default == r {
					w = 0.7
				}
			}
		}
		crits = append(crits, Criterion{Key: r, Description: desc, Prior: w})
		weights[r] = w
	}
	conf := 0.0
	for _, a := range args {
		conf += a.Confidence
	}
	if n := float64(len(args)); n > 0 {
		conf /= n
	}
	return nc.req(Request{
		ID:              "D3/" + string(v.ID) + "/" + entity,
		Kind:            KindChoice,
		Instructions:    "Which semantic role does " + entity + " fill in " + orDash(v.Predicate) + "?",
		Criteria:        crits,
		Prior:           jlir.NewDistribution("role_frame", weights),
		Impact:          0.7 * (1 - clamp01(conf)),
		Action:          ActionRole,
		TargetID:        string(v.ID) + "|" + entity,
		SemanticContext: "roles:" + string(v.ID) + ":" + v.Predicate + ":" + strings.Join(options, ","),
	})
}

func describeRole(role, def string, sense *ontology.Sense) string {
	out := role
	if sense != nil && sense.Gloss != "" {
		out = role + " (" + sense.Gloss + ")"
	}
	if def == "" {
		return out
	}
	return out + "; the ontology's default marking is " + def
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// D4: coreference and zero anaphora. A binary referent choice.
func collectCoref(nc *nodeCtx) []*Request {
	if nc.src == nil {
		return nil
	}
	var out []*Request
	for _, e := range nc.src.Entities {
		if e == nil || e.Referent == nil || e.Referent.Resolved {
			continue
		}
		if len(e.Referent.Options) != 2 {
			continue
		}
		out = append(out, nc.req(Request{
			ID:              "D4/" + string(e.ID),
			Kind:            KindChoice,
			Instructions:    "Which referent does " + orDash(entityLabel(e, nc.sourceLang())) + " stand for?",
			Criteria:        nc.referentCriteria(e),
			Prior:           e.Referent,
			Impact:          0.95,
			Action:          ActionReferent,
			TargetID:        string(e.ID),
			SemanticContext: "referent:" + string(e.ID) + ":" + strings.Join(e.Referent.Options, ","),
		}))
	}
	return out
}

// D5: discourse interpretation. More than two live referents is no longer a
// lexical zero but a discourse search over the document state.
func collectDiscourse(nc *nodeCtx) []*Request {
	if nc.src == nil {
		return nil
	}
	var out []*Request
	for _, e := range nc.src.Entities {
		if e == nil || e.Referent == nil || e.Referent.Resolved || len(e.Referent.Options) < 3 {
			continue
		}
		// Salience is discourse evidence: fold it into the prior so the offline
		// answer follows the document, not the surface form.
		weights := make(map[string]float64, len(e.Referent.Options))
		for _, id := range e.Referent.Options {
			w := e.Referent.P(id)
			if cand := nc.src.Entity(jlir.ID(id)); cand != nil {
				w *= 1 + cand.Salience
			}
			weights[id] = w
		}
		prior := jlir.NewDistribution("discourse_salience", weights)
		out = append(out, nc.req(Request{
			ID:              "D5/" + string(e.ID),
			Kind:            KindChoice,
			Instructions:    "Which referent does the discourse make most likely for " + orDash(entityLabel(e, nc.sourceLang())) + "?",
			Criteria:        nc.referentCriteria(e),
			Prior:           prior,
			Impact:          0.9,
			Action:          ActionReferent,
			TargetID:        string(e.ID),
			SemanticContext: "discourse:" + string(e.ID) + ":" + strings.Join(e.Referent.Options, ","),
		}))
	}
	return out
}

func (nc *nodeCtx) referentCriteria(e *jlir.Entity) []Criterion {
	l := nc.sourceLang()
	out := make([]Criterion, 0, len(e.Referent.Options))
	for _, id := range e.Referent.Options {
		desc := id
		if cand := nc.src.Entity(jlir.ID(id)); cand != nil {
			desc = entityLabel(cand, l)
			if cand.MentionCount > 0 {
				desc += " (mentioned " + strconv.Itoa(cand.MentionCount) + "x)"
			}
			if cand.External {
				desc += " (from the document)"
			}
		}
		out = append(out, Criterion{Key: id, Description: desc, Prior: e.Referent.P(id)})
	}
	return out
}

func entityLabel(e *jlir.Entity, l lang.Lang) string {
	if e == nil {
		return ""
	}
	if s := e.Alias(l); s != "" {
		return s
	}
	if e.Identity != "" {
		return e.Identity
	}
	return string(e.ID)
}

// D6: pragmatic interpretation. Register and irony, the two places where the
// literal reading and the intended reading diverge most.
func collectPragmatic(nc *nodeCtx) []*Request {
	if nc.src == nil {
		return nil
	}
	feat := nc.src.SourceFeat
	var out []*Request
	if nc.src.Prag.IronySuspected {
		weights := map[string]float64{"literal": 0.5, "ironic": 0.3, "indeterminate": 0.2}
		out = append(out, nc.req(Request{
			ID:           "D6/irony",
			Kind:         KindChoice,
			Instructions: "Should the utterance be read literally or ironically?",
			Criteria: []Criterion{
				{Key: "literal", Description: "a straightforward literal reading", Prior: 0.5},
				{Key: "ironic", Description: "the surface contradicts the intended meaning", Prior: 0.3},
				{Key: "indeterminate", Description: "the text does not settle it", Prior: 0.2},
			},
			Prior:           jlir.NewDistribution("pragmatic", weights),
			Impact:          0.5,
			Action:          ActionPragmatic,
			TargetID:        "irony",
			SemanticContext: "pragmatics:irony:" + strings.Join(feat.SentenceFinalParticles, ","),
		}))
	}
	if feat.Honorific || feat.Humidification || len(feat.SentenceFinalParticles) > 0 || nc.src.Prag.Respect > 0 {
		levels := []string{"casual", "neutral", "polite", "formal", "ceremonial"}
		out = append(out, nc.req(Request{
			ID:           "D6/register",
			Kind:         KindScore,
			Instructions: "Which register does the addressee relationship call for in " + orDash(nc.target().Name()) + "?",
			Criteria:     scoreLevels(levels...),
			Prior:        jlir.NewDistribution("pragmatics", registerWeights(nc.src.Prag, len(levels))),
			Impact:       0.45,
			Action:       ActionPragmatic,
			TargetID:     "register",
			SemanticContext: "pragmatics:register:" +
				fmt.Sprintf("%.2f/%.2f/%.2f", nc.src.Prag.Respect, nc.src.Prag.Humility, nc.src.Prag.Formality),
		}))
	}
	return out
}

func registerWeights(p jlir.Pragmatics, levels int) map[string]float64 {
	w := map[string]float64{}
	for i := 1; i <= levels; i++ {
		w["lvl_"+strconv.Itoa(i)] = 0.2
	}
	// Respect and humility push the register up the scale; politeness alone
	// only moves it one step.
	score := clamp01(0.5*p.Respect + 0.3*p.Humility + 0.2*p.Politeness)
	level := int(score*float64(levels-1)) + 1
	w["lvl_"+strconv.Itoa(level)] = 0.6
	return w
}

// D7: target projection choices. The target language forces decisions the source
// never made: English needs an overt gendered pronoun, Japanese needs a
// politeness level. This is where "never invent information" is either honoured
// or violated, so the alternatives always include the one that invents nothing.
func collectProjection(nc *nodeCtx) []*Request {
	if nc.src == nil {
		return nil
	}
	l := nc.sourceLang()
	var out []*Request
	if nc.target() == lang.JA {
		if nc.src.SourceFeat.Honorific || nc.src.SourceFeat.Humidification || nc.src.Prag.Respect > 0 {
			weights := map[string]float64{"plain": 0.3, "polite": 0.4, "honorific": 0.2, "humble": 0.1}
			if nc.src.SourceFeat.Honorific {
				weights["honorific"] = 0.5
			}
			if nc.src.SourceFeat.Humidification {
				weights["humble"] = 0.5
			}
			out = append(out, nc.req(Request{
				ID:           "D7/politeness",
				Kind:         KindChoice,
				Instructions: "Which politeness level should the Japanese realization use?",
				Criteria: []Criterion{
					{Key: "plain", Description: "常体 (plain form)", Prior: weights["plain"]},
					{Key: "polite", Description: "敬体 (です・ます)", Prior: weights["polite"]},
					{Key: "honorific", Description: "尊敬語 (honorific, elevating the addressee)", Prior: weights["honorific"]},
					{Key: "humble", Description: "謙譲語 (humble, lowering the speaker)", Prior: weights["humble"]},
				},
				Prior:           jlir.NewDistribution("pragmatics", weights),
				Impact:          0.6,
				Action:          ActionProjection,
				TargetID:        "politeness",
				SemanticContext: "projection:ja:politeness:" + fmt.Sprintf("%.2f", nc.src.Prag.Respect),
			}))
		}
		for _, e := range nc.src.Entities {
			if e == nil || !e.Zero {
				continue
			}
			weights := map[string]float64{"ellipsis": 0.55, "topic": 0.3, "pronoun": 0.15}
			out = append(out, nc.req(Request{
				ID:           "D7/subject/" + string(e.ID),
				Kind:         KindChoice,
				Instructions: "How should the zero subject " + orDash(entityLabel(e, l)) + " be realized in Japanese?",
				Criteria: []Criterion{
					{Key: "ellipsis", Description: "leave the subject unexpressed", Prior: 0.55},
					{Key: "topic", Description: "mark it as the topic with は", Prior: 0.3},
					{Key: "pronoun", Description: "make it overt with 彼/彼女", Prior: 0.15},
				},
				Prior:           jlir.NewDistribution("target_projection", weights),
				Impact:          0.7,
				Action:          ActionProjection,
				TargetID:        string(e.ID),
				SemanticContext: "projection:ja:subject:" + string(e.ID),
				Realizations: map[string]string{
					"ellipsis": "",
					"topic":    orDash(entityLabel(e, l)) + "は",
					"pronoun":  "彼/彼女",
				},
			}))
		}
		return out
	}
	for _, e := range nc.src.Entities {
		if e == nil || !needsEnglishPronoun(e) {
			continue
		}
		name := entityLabel(e, l)
		weights := map[string]float64{"he": 0.34, "she": 0.34, "they": 0.24, "repeat": 0.08}
		out = append(out, nc.req(Request{
			ID:           "D7/pronoun/" + string(e.ID),
			Kind:         KindChoice,
			Instructions: "Which English form should " + orDash(name) + " take? Its gender is not determined by the source.",
			Criteria: []Criterion{
				{Key: "he", Description: "masculine pronoun", Prior: 0.34},
				{Key: "she", Description: "feminine pronoun", Prior: 0.34},
				{Key: "they", Description: "gender-neutral pronoun", Prior: 0.24},
				{Key: "repeat", Description: "repeat the referent's name instead of asserting a gender", Prior: 0.08},
			},
			Prior:           jlir.NewDistribution("target_projection", weights),
			Impact:          0.8,
			Action:          ActionProjection,
			TargetID:        string(e.ID),
			SemanticContext: "projection:en:pronoun:" + string(e.ID),
			Realizations: map[string]string{
				"he":     "he",
				"she":    "she",
				"they":   "they",
				"repeat": name,
			},
		}))
	}
	return out
}

// needsEnglishPronoun reports whether the target would have to assert something
// the source does not say about this entity.
func needsEnglishPronoun(e *jlir.Entity) bool {
	if e == nil {
		return false
	}
	if e.Gender != "" && e.Gender != jlir.GenderUnknown {
		return false
	}
	for _, a := range e.Aliases {
		if a.Kind == "pronoun" || a.Kind == "zero" {
			return true
		}
	}
	return e.Zero
}

// D8: construction selection. The construction inventory of the source parser
// is the candidate set; when it is wider than one question, the ladder decides
// coarsely first.
func collectConstruction(nc *nodeCtx) []*Request {
	if nc.src == nil {
		return nil
	}
	var out []*Request
	for _, v := range nc.src.Events {
		if v == nil {
			continue
		}
		cands, prior := nc.constructionCandidates(v)
		if len(cands) < 2 {
			continue
		}
		out = append(out, nc.req(Request{
			ID:              "D8/" + string(v.ID),
			Kind:            KindChoice,
			Instructions:    "Which construction expresses " + orDash(v.Predicate) + " most faithfully?",
			Criteria:        nc.senseCriteria(cands, prior),
			Prior:           prior,
			Candidates:      cands,
			Impact:          0.75,
			Action:          ActionConstruction,
			TargetID:        string(v.ID),
			SemanticContext: "construction:" + string(v.ID) + ":" + strings.Join(cands, ","),
		}))
	}
	return out
}

func (nc *nodeCtx) constructionCandidates(v *jlir.Event) ([]string, *jlir.Distribution) {
	reg := nc.registry()
	if v == nil || reg == nil {
		return nil, nil
	}
	// Constructions the source parser actually proposed come first.
	if cands := dedupe(nc.src.SourceFeat.Constructions); len(cands) > 1 {
		weights := map[string]float64{}
		for i, c := range cands {
			weights[c] = float64(len(cands)-i) / float64(len(cands))
		}
		return cands, jlir.NewDistribution("constructions", weights)
	}
	sense := reg.MustSense(v.Predicate)
	pool := append([]string(nil), sense.Children...)
	if p := sense.Parent(); p != "" {
		pool = append(pool, reg.Families(p)...)
	}
	pool = dedupe(pool)
	if len(pool) < 2 {
		return nil, nil
	}
	weights := map[string]float64{}
	for _, id := range pool {
		if s, ok := reg.Sense(id); ok && s != nil && !s.Realizable {
			continue
		}
		weights[id] = senseScore(reg.MustSense(id), v)
	}
	if len(weights) < 2 {
		return nil, nil
	}
	prior := jlir.NewDistribution("constructions", weights)
	if !prior.Ambiguous(0.2) {
		return nil, nil
	}
	return prior.Options, prior
}

// D9: final ranking. A score over a fixed five-level quality scale, plus one
// yes/no question per candidate when there are few enough to check individually.
func collectRanking(nc *nodeCtx) []*Request {
	if nc.in == nil || len(nc.in.Candidates) < 2 {
		return nil
	}
	cands := nc.in.Candidates
	levels := []string{"unacceptable", "poor", "acceptable", "good", "excellent"}
	out := []*Request{nc.req(Request{
		ID:              "D9/rank",
		Kind:            KindScore,
		Instructions:    "How well does the best available candidate preserve the source meaning?",
		Criteria:        scoreLevels(levels...),
		Prior:           rankingPrior(cands, len(levels)),
		Impact:          0.6,
		Action:          ActionRanking,
		TargetID:        "candidates",
		SemanticContext: "ranking:" + candidateKeySet(cands),
	})}
	if len(cands) <= 4 {
		for _, c := range cands {
			if strings.TrimSpace(c.Text) == "" {
				continue
			}
			weights := map[string]float64{"true": 0.75, "false": 0.25}
			out = append(out, nc.req(Request{
				ID:           "D9/implication/" + c.Key,
				Kind:         KindNoul,
				Instructions: "Does the candidate \"" + c.Text + "\" preserve every implication of the source?",
				Criteria: []Criterion{
					{Key: "true", Description: "yes: every implication survives", Prior: 0.75},
					{Key: "false", Description: "no: an implication is lost or added", Prior: 0.25},
				},
				Prior:           jlir.NewDistribution("verification", weights),
				Impact:          0.55,
				Action:          ActionRanking,
				TargetID:        c.Key,
				SemanticContext: "ranking:implication:" + c.Key + ":" + c.Text,
			}))
		}
	}
	return out
}

// rankingPrior derives the offline ranking scale from the projection stage's own
// estimates. It is the only honest prior D9 has: the constraint solver already
// scored these candidates.
func rankingPrior(cands []Candidate, levels int) *jlir.Distribution {
	w := map[string]float64{}
	for i := 1; i <= levels; i++ {
		w["lvl_"+strconv.Itoa(i)] = 0.2
	}
	lo, hi := cands[0].Score, cands[0].Score
	for _, c := range cands[1:] {
		lo = minFloat(lo, c.Score)
		hi = maxFloat(hi, c.Score)
	}
	norm := 0.5
	if hi > lo {
		norm = (cands[0].Score - lo) / (hi - lo)
	}
	level := int(clamp01(norm)*float64(levels-1)) + 1
	w["lvl_"+strconv.Itoa(level)] = 0.6
	return jlir.NewDistribution("projection_ranking", w)
}

func candidateKeySet(cands []Candidate) string {
	keys := make([]string, 0, len(cands))
	for _, c := range cands {
		keys = append(keys, c.Key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
