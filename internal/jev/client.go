// Package jev is the decision oracle client and the decision graph that
// drives it.
//
// The defining constraint of JEV-Trans (plan.md §1) is that the model never
// writes a translation. Jev answers typed questions — choice, score, yes/no —
// about candidates that this package, the ontology and the parser forest have
// already generated (plan.md §32). Everything a decision needs travels in a
// compact structured `state`; nothing travels as free text (plan.md §31).
//
// Three properties matter more than speed here:
//
//   - The pipeline runs without an oracle. With no API key the client answers
//     from the deterministic analysis priors and says so, in the decision and
//     in the trace. A translation never fails because the oracle is down
//     (plan.md §34 makes "do not ask" a first-class outcome).
//   - Every decision is recorded: asked, skipped, cached, prior or
//     user-answered, each with the reason it ended up that way.
//   - A retried call never double-counts tokens, and a hung call cannot wedge a
//     translation: the timeout lives in the context, not in a goroutine nobody
//     joins.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nico2525nn/jev-trans/internal/jlir"
	"github.com/nico2525nn/jev-trans/internal/trace"
)

// System One defaults.
const (
	DefaultEndpoint = "https://opencode.ai/zen/v1/systemone"
	DefaultModel    = "jev-1.13-free"
	DefaultTimeout  = 45 * time.Second
	DefaultRetries  = 3
)

// Question kinds understood by the System One API.
const (
	// KindChoice picks one of at most MaxChoiceOptions options.
	KindChoice = "choice"
	// KindScore places the situation on an ordered scale of 2..10 levels.
	KindScore = "score"
	// KindNoul asks a yes/no question, with optional true/false rubrics.
	KindNoul = "noul"
)

// Protocol limits. They are properties of the oracle, not of this code, so they
// live in one place and the decision graph consults them before building a
// question rather than after the API rejects it.
const (
	// MaxChoiceOptions is the hard option ceiling of a Choice question.
	MaxChoiceOptions = 255
	// MinScoreLevels and MaxScoreLevels bound an ordered scale.
	MinScoreLevels = 2
	MaxScoreLevels = 10
	// maxResponseBytes bounds what a hostile or broken endpoint can make the
	// pipeline allocate.
	maxResponseBytes = 4 << 20
)

// Decision sources.
const (
	// SourceJev means the oracle answered.
	SourceJev = "jev"
	// SourceCache means an identical decision was already answered.
	SourceCache = "cache"
	// SourcePrior means the deterministic analysis answered instead of the
	// oracle: no key, offline mode, or an oracle that failed.
	SourcePrior = "prior"
	// SourceSkipped means the decision was not worth asking (plan.md §34).
	SourceSkipped = "skipped"
	// SourceUser means a human answered it (plan.md §62).
	SourceUser = "user"
)

// Options configures a Client. The zero value is valid: it reads the key from
// the environment, uses the public endpoint and the default model, and
// disables the oracle when no key is present.
type Options struct {
	// APIKey authenticates the call. Empty falls back to $OPENCODE_API_KEY.
	APIKey string
	// Endpoint overrides the System One URL (a gateway, a proxy, a test
	// server). Empty means DefaultEndpoint.
	Endpoint string
	// Model overrides the decision model. Empty means DefaultModel.
	Model string
	// Timeout bounds one HTTP attempt. Zero means DefaultTimeout.
	Timeout time.Duration
	// MaxRetries is the number of *additional* attempts after a retryable
	// failure. Zero means DefaultRetries; negative means no retries at all.
	MaxRetries int
	// HTTPClient is the transport. When nil a plain client is used, because
	// the per-attempt deadline is enforced through the context.
	HTTPClient *http.Client
	// Offline disables every oracle call. Decisions fall back to priors.
	Offline bool

	// Cache is the decision cache (plan.md §56). Nil installs a default
	// bounded LRU; NoCache removes it entirely.
	Cache   *Cache
	NoCache bool

	// StateBudget caps the serialised `state` payload in bytes. Zero means
	// DefaultStateBudget.
	StateBudget int

	// UserAgent identifies this system to the oracle. Empty uses a default.
	UserAgent string
}

// Client talks to System One. It is safe for concurrent use: the decision
// graph may fan a node's questions out together and the trace is what proves
// they ran.
type Client struct {
	apiKey      string
	endpoint    string
	model       string
	userAgent   string
	timeout     time.Duration
	retries     int
	stateBudget int
	hc          *http.Client
	cache       *Cache
	noCache     bool
	offline     bool
	// customTransport records that the operator supplied the transport
	// independently of OpenCode's key. Such a client counts as enabled even
	// without a key: a local gateway or a test server needs none. No
	// Authorization header is sent when the key is empty.
	customTransport bool

	mu    sync.Mutex
	stats Stats
}

// Stats is the cumulative oracle accounting for the trace header.
type Stats struct {
	Calls        int        `json:"calls"`
	Questions    int        `json:"questions"`
	Cached       int        `json:"cached"`
	Prior        int        `json:"prior"`
	Skipped      int        `json:"skipped"`
	User         int        `json:"user"`
	Failed       int        `json:"failed"`
	LatencyMS    float64    `json:"latencyMs"`
	InputTokens  int        `json:"inputTokens"`
	OutputTokens int        `json:"outputTokens"`
	Cache        CacheStats `json:"cache"`
}

// New builds a client. It never returns nil and never panics, whatever the
// options contain.
func New(opts Options) *Client {
	c := &Client{}
	c.apiKey = strings.TrimSpace(opts.APIKey)
	if c.apiKey == "" {
		c.apiKey = strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	}
	c.endpoint = strings.TrimSpace(opts.Endpoint)
	customEndpoint := c.endpoint != "" && c.endpoint != DefaultEndpoint
	if c.endpoint == "" {
		c.endpoint = DefaultEndpoint
	}
	c.model = strings.TrimSpace(opts.Model)
	if c.model == "" {
		c.model = DefaultModel
	}
	c.timeout = opts.Timeout
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	c.retries = opts.MaxRetries
	if c.retries == 0 {
		c.retries = DefaultRetries
	} else if c.retries < 0 {
		c.retries = 0
	}
	c.hc = opts.HTTPClient
	if c.hc == nil {
		c.hc = &http.Client{}
	}
	c.stateBudget = opts.StateBudget
	if c.stateBudget <= 0 {
		c.stateBudget = DefaultStateBudget
	}
	c.userAgent = strings.TrimSpace(opts.UserAgent)
	if c.userAgent == "" {
		c.userAgent = "jev-trans"
	}
	c.offline = opts.Offline
	c.noCache = opts.NoCache
	if !c.noCache {
		c.cache = opts.Cache
		if c.cache == nil {
			c.cache = NewCache(DefaultCacheSize)
		}
	}
	c.customTransport = opts.HTTPClient != nil || customEndpoint
	return c
}

// Enabled reports whether the oracle can be consulted at all.
func (c *Client) Enabled() bool {
	if c == nil || c.offline {
		return false
	}
	return c.apiKey != "" || c.customTransport
}

// Offline reports whether the client was explicitly taken offline.
func (c *Client) Offline() bool { return c != nil && c.offline }

// Model returns the decision model in use.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// Endpoint returns the System One URL in use.
func (c *Client) Endpoint() string {
	if c == nil {
		return ""
	}
	return c.endpoint
}

// Cache returns the decision cache, which is nil when caching is off.
func (c *Client) Cache() *Cache {
	if c == nil {
		return nil
	}
	return c.cache
}

// Stats returns the cumulative oracle accounting.
func (c *Client) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	if c.cache != nil {
		s.Cache = c.cache.Stats()
	}
	return s
}

// Request is one decision to put to Jev. The caller owns the candidate set:
// Jev never invents an option (plan.md §32).
type Request struct {
	// ID identifies the decision in the trace and in the interactive UI
	// ("D4/e2"). With a user answer it is also how the answer finds its way
	// back to this exact question.
	ID string
	// Stage is the decision-graph node: D0..D9.
	Stage string
	// Kind is choice, score or noul.
	Kind string
	// Instructions is the question as the model sees it.
	Instructions string
	// Criteria are the options (choice), the ordered levels (score) or the
	// true/false rubrics (noul).
	Criteria []Criterion
	// State is the compact structured payload the question is about; see
	// state.go.
	State any
	// Impact is the expected translation impact of getting this decision
	// right, in [0,1]. It is the first factor of the scheduling priority
	// (plan.md §34).
	Impact float64
	// Prior is the deterministic distribution the analysis has already
	// produced. It is the offline answer and the fallback on failure.
	Prior *jlir.Distribution
	// CachedAllowed forces this decision through the cache even when the
	// client was built with NoCache. Left false the client's own cache policy
	// applies.
	CachedAllowed bool

	// SemanticContext is the content the decision is about (the spans under
	// discussion, the competing readings). Two questions with the same
	// context, candidates, style, kind and model are the same decision and
	// share a cache entry.
	SemanticContext string
	// Style is the document style profile, part of the cache identity.
	Style string
	// Candidates is the full candidate set when it is wider than one question
	// may carry; the decision graph then walks the ontology ladder.
	Candidates []string
	// Realizations maps an option key to the target string it produces. When
	// every option produces the same string the decision cannot change the
	// translation and must not be asked (plan.md §34).
	Realizations map[string]string
	// Action names what the decision commits to in the JLIR ("scope",
	// "referent", "sense", "role", "pragmatic", "projection", "construction",
	// "ranking"), and TargetID is the node it commits to. The decision graph
	// uses the pair to apply an answer.
	Action   string
	TargetID string
	// UserAnswer, when set, short-circuits the oracle: a human has already
	// resolved this exact question (plan.md §62).
	UserAnswer string
}

// Criterion is one option of a choice question, one level of a score scale, or
// one side of a noul question.
type Criterion struct {
	// Key is the machine-readable option identifier an answer refers to.
	Key string
	// Description is the rubric the model reads.
	Description string
	// Prior is the analysis prior for this option; it seeds the offline path
	// and breaks ties inside the question itself.
	Prior float64
}

// Option is a candidate as it appears in a decision record and in the UI.
type Option struct {
	Key         string  `json:"key"`
	Description string  `json:"label,omitempty"`
	Probability float64 `json:"probability"`
}

// Answer is the oracle's verdict. Probabilities is always normalized over the
// question's own criteria, whatever the model returned.
type Answer struct {
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Legend        map[string]string  `json:"legend,omitempty"`
}

// Decision is one recorded resolution: asked, skipped, cached, prior-derived
// or user-supplied. There is no fifth state — a decision the system could not
// account for is a bug, not a status.
type Decision struct {
	ID       string   `json:"id"`
	Stage    string   `json:"stage"`
	Kind     string   `json:"kind"`
	Question string   `json:"question"`
	Options  []Option `json:"options"`
	Answer   Answer   `json:"answer"`
	// Source is one of SourceJev, SourceCache, SourcePrior, SourceSkipped or
	// SourceUser.
	Source    string  `json:"source"`
	CacheHit  bool    `json:"cacheHit"`
	LatencyMS float64 `json:"latencyMs"`
	// InputTokens and OutputTokens are this decision's share of the billed
	// call. A retried call bills once, so retries never inflate them.
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	// SkipReason is populated whenever the oracle was not asked and no human
	// answered: the UI audit trail must say why.
	SkipReason string `json:"skipReason,omitempty"`
	// Impact is the expected translation impact carried over from the request;
	// Priority is Impact × uncertainty as computed by the scheduler.
	Impact   float64 `json:"impact"`
	Priority float64 `json:"priority"`
	// LadderLevels lists the hierarchical levels walked to reach a decision
	// whose candidate set exceeded one question's option ceiling (plan.md §33).
	LadderLevels []string `json:"ladderLevels,omitempty"`
	// Action and TargetID name what this decision commits to in the JLIR.
	Action   string `json:"action,omitempty"`
	TargetID string `json:"targetId,omitempty"`
}

// Winner returns the chosen option, falling back to the argmax of the
// probability map.
func (d *Decision) Winner() string {
	if d == nil {
		return ""
	}
	if d.Answer.Choice != "" {
		return d.Answer.Choice
	}
	return argmax(d.Answer.Probabilities)
}

// P returns the probability the decision assigns to one option.
func (d *Decision) P(option string) float64 {
	if d == nil {
		return 0
	}
	return d.Answer.Probabilities[option]
}

// Clone returns a deep copy. Decisions are handed to the trace, the cache and
// the UI, and none of them may write through to the others.
func (d *Decision) Clone() *Decision {
	if d == nil {
		return nil
	}
	c := *d
	c.Options = append([]Option(nil), d.Options...)
	c.LadderLevels = append([]string(nil), d.LadderLevels...)
	a := d.Answer
	a.Probabilities = make(map[string]float64, len(d.Answer.Probabilities))
	for k, v := range d.Answer.Probabilities {
		a.Probabilities[k] = v
	}
	if len(d.Answer.Legend) > 0 {
		a.Legend = make(map[string]string, len(d.Answer.Legend))
		for k, v := range d.Answer.Legend {
			a.Legend[k] = v
		}
	}
	c.Answer = a
	return &c
}

// AsCacheHit returns a copy marked as served from the cache: same verdict, no
// latency, no tokens. Source is preserved, because a decision answered from
// priors offline must keep saying so even when it is replayed.
func (d *Decision) AsCacheHit() *Decision {
	c := d.Clone()
	if c == nil {
		return nil
	}
	c.CacheHit = true
	c.LatencyMS = 0
	c.InputTokens, c.OutputTokens = 0, 0
	return c
}

// View renders the decision for the trace and the UI.
func (d *Decision) View() trace.DecisionView {
	if d == nil {
		return trace.DecisionView{}
	}
	v := trace.DecisionView{
		ID:         d.ID,
		Stage:      d.Stage,
		Kind:       d.Kind,
		Question:   d.Question,
		Source:     d.Source,
		CacheHit:   d.CacheHit,
		LatencyMS:  d.LatencyMS,
		SkipReason: d.SkipReason,
	}
	v.Options = make([]string, 0, len(d.Options))
	for _, o := range d.Options {
		v.Options = append(v.Options, o.Key)
	}
	v.Probabilities = make(map[string]float64, len(d.Answer.Probabilities))
	for k, p := range d.Answer.Probabilities {
		v.Probabilities[k] = p
	}
	v.Winner = d.Winner()
	v.Confidence = d.Answer.Confidence
	if v.Confidence <= 0 {
		for _, p := range v.Probabilities {
			if p > v.Confidence {
				v.Confidence = p
			}
		}
	}
	// Anything the oracle did not answer is a decision that was not asked.
	v.Skipped = d.Source != SourceJev && d.Source != SourceCache
	if d.Kind == KindScore && d.Answer.Score > 0 {
		s := d.Answer.Score
		v.Score = &s
	}
	if d.Kind == KindNoul {
		n := d.Answer.Noul
		v.Noul = &n
	}
	return v
}

// --- the wire ---------------------------------------------------------------

type wireRequest struct {
	Model     string                  `json:"model"`
	State     any                     `json:"state"`
	Questions map[string]wireQuestion `json:"questions"`
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type wireResponse struct {
	Model   string                `json:"model"`
	Answers map[string]wireAnswer `json:"answers"`
	Usage   wireUsage             `json:"usage"`
}

type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type wireAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Score         *float64           `json:"score"`
	Noul          *float64           `json:"noul"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Legend        map[string]string  `json:"legend"`
}

type pending struct {
	index int
	rq    Request
	key   string
}

// Ask resolves one decision. It never fails because the oracle is unavailable:
// the returned decision then carries Source = SourcePrior and says why. The
// only error case is a context that was already cancelled before the call,
// where the caller has already asked for the work to stop.
func (c *Client) Ask(ctx context.Context, rq Request) (*Decision, error) {
	ds, err := c.AskBatch(ctx, nil, []Request{rq})
	if err != nil {
		return nil, err
	}
	if len(ds) == 0 {
		return nil, fmt.Errorf("jev: no decision produced for %q", rq.ID)
	}
	return ds[0], nil
}

// AskMany resolves a batch of decisions in one HTTP call.
//
// The batch is a single round trip on purpose. Adding questions to a System One
// request barely changes its latency — only its token count — so a node of the
// decision graph that produced five questions spends one call, not five.
// Questions answered from a user answer, from the cache, or that cannot be
// asked at all never reach the wire and do not inflate the batch.
func (c *Client) AskMany(ctx context.Context, rqs []Request) ([]*Decision, error) {
	return c.AskBatch(ctx, nil, rqs)
}

// AskBatch is AskMany with an explicit shared `state`. When state is nil the
// per-request states are merged, which is exactly what AskMany does.
func (c *Client) AskBatch(ctx context.Context, state any, rqs []Request) ([]*Decision, error) {
	out := make([]*Decision, len(rqs))
	if len(rqs) == 0 {
		return out, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	reqs := make([]Request, len(rqs))
	copy(reqs, rqs)
	for i := range reqs {
		reqs[i] = normalizeRequest(reqs[i], i)
	}

	var batch []pending
	for i := range reqs {
		rq := reqs[i]
		if rq.UserAnswer != "" {
			out[i] = userDecision(rq, rq.UserAnswer)
			continue
		}
		if reason := unaskable(rq); reason != "" {
			out[i] = skipDecision(rq, reason)
			continue
		}
		if key, ok := c.cacheKey(rq); ok {
			if hit, found := c.cache.Get(key); found {
				c.mu.Lock()
				c.stats.Cached++
				c.mu.Unlock()
				out[i] = hit.AsCacheHit()
				continue
			}
		}
		batch = append(batch, pending{
			index: i,
			rq:    rq,
			key:   "q" + strconv.Itoa(len(batch)+1),
		})
	}
	if len(batch) == 0 {
		return out, nil
	}

	if !c.Enabled() {
		reason := "oracle not consulted: no API key configured (analysis priors used)"
		if c.Offline() {
			reason = "oracle not consulted: client is offline (analysis priors used)"
		}
		for _, p := range batch {
			d := priorDecision(p.rq, reason)
			c.storeDecision(p.rq, d)
			out[p.index] = d
		}
		return out, nil
	}

	answers, usage, latency, err := c.call(ctx, c.stateFor(state, batch), batch)
	if err != nil {
		c.mu.Lock()
		c.stats.Failed += len(batch)
		c.mu.Unlock()
		reason := "oracle unavailable (" + err.Error() + "); analysis priors used"
		for _, p := range batch {
			out[p.index] = priorDecision(p.rq, reason)
		}
		return out, nil
	}
	// Only the accepted response is billed. A retry that failed carries no
	// usage and a retry that succeeded ends the loop, so the batch is counted
	// exactly once and split across its decisions.
	c.mu.Lock()
	c.stats.Calls++
	c.stats.Questions += len(batch)
	c.stats.LatencyMS += latency
	c.stats.InputTokens += usage.InputTokens
	c.stats.OutputTokens += usage.OutputTokens
	c.mu.Unlock()

	n := float64(len(batch))
	for _, p := range batch {
		a, ok := answers[p.key]
		if !ok {
			out[p.index] = priorDecision(p.rq, "oracle returned no answer for this question; analysis priors used")
			continue
		}
		d := c.decisionFromWire(p.rq, a, usage, latency/n)
		c.storeDecision(p.rq, d)
		out[p.index] = d
	}
	return out, nil
}

func (c *Client) cacheKey(rq Request) (string, bool) {
	if c == nil || c.cache == nil {
		return "", false
	}
	if len(rq.Criteria) == 0 {
		return "", false
	}
	return CacheKey(c.model, rq.Kind, rq.Style, rq.SemanticContext, rq.Instructions, rq.Criteria), true
}

// cacheAllowed reports whether a decision may be stored. Request.CachedAllowed
// forces caching on for that request; otherwise the client's policy decides.
func (c *Client) cacheAllowed(rq Request) bool {
	if c == nil || c.cache == nil {
		return false
	}
	if rq.CachedAllowed {
		return true
	}
	return !c.noCache
}

func (c *Client) storeDecision(rq Request, d *Decision) {
	if d == nil || !c.cacheAllowed(rq) {
		return
	}
	if key, ok := c.cacheKey(rq); ok {
		c.cache.Put(key, d)
	}
	c.mu.Lock()
	switch d.Source {
	case SourcePrior:
		c.stats.Prior++
	case SourceSkipped:
		c.stats.Skipped++
	case SourceUser:
		c.stats.User++
	}
	c.mu.Unlock()
}

func (c *Client) stateFor(state any, batch []pending) any {
	if c == nil {
		return TrimState(state, DefaultStateBudget)
	}
	if state != nil {
		return TrimState(state, c.stateBudget)
	}
	states := make([]any, 0, len(batch))
	for _, p := range batch {
		states = append(states, p.rq.State)
	}
	return MergeStates(states, c.stateBudget)
}

// call performs the single HTTP round trip for a batch, with bounded retries on
// 429 and 5xx. Any other 4xx is final: repeating it would waste the budget and
// hide a real bug.
func (c *Client) call(ctx context.Context, state any, batch []pending) (map[string]wireAnswer, wireUsage, float64, error) {
	body := wireRequest{
		Model:     c.model,
		State:     state,
		Questions: make(map[string]wireQuestion, len(batch)),
	}
	for _, p := range batch {
		body.Questions[p.key] = wireQuestionOf(p.rq)
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, wireUsage{}, 0, fmt.Errorf("marshal oracle request: %w", err)
	}

	attempts := c.retries + 1
	start := time.Now()
	var lastErr error
	for attempt := range attempts {
		if err := ctx.Err(); err != nil {
			return nil, wireUsage{}, elapsed(start), err
		}
		raw, status, retryAfter, err := c.do(ctx, buf)
		switch {
		case err != nil:
			lastErr = err
			if attempt < attempts-1 && !sleep(ctx, backoff(attempt, retryAfter)) {
				return nil, wireUsage{}, elapsed(start), ctx.Err()
			}
		case status == http.StatusOK:
			var wr wireResponse
			if err := json.Unmarshal(raw, &wr); err != nil {
				// A malformed body will not become well formed by asking again.
				return nil, wireUsage{}, elapsed(start), fmt.Errorf("decode oracle response: %w", err)
			}
			if wr.Answers == nil {
				wr.Answers = map[string]wireAnswer{}
			}
			return wr.Answers, wr.Usage, elapsed(start), nil
		case status == http.StatusTooManyRequests || status >= 500:
			lastErr = fmt.Errorf("oracle returned HTTP %d", status)
			if attempt < attempts-1 && !sleep(ctx, backoff(attempt, retryAfter)) {
				return nil, wireUsage{}, elapsed(start), ctx.Err()
			}
		default:
			return nil, wireUsage{}, elapsed(start), fmt.Errorf("oracle returned HTTP %d", status)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("oracle exhausted its retries")
	}
	return nil, wireUsage{}, elapsed(start), lastErr
}

func (c *Client) do(ctx context.Context, body []byte) ([]byte, int, time.Duration, error) {
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, 0, err
	}
	return raw, resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

func elapsed(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

// backoff is deterministic: an exponential ramp from 250ms capped at 4s, or
// whatever Retry-After asked for. No jitter — the run must be reproducible.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, 8*time.Second)
	}
	d := 250 * time.Millisecond * time.Duration(1<<min(attempt, 4))
	return min(d, 4*time.Second)
}

// sleep waits for d, reporting false if the context ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(h); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// decisionFromWire turns one raw answer into a Decision. Every field is
// optional on the wire: a missing confidence, a missing probability map or a
// choice that is not among the criteria all degrade to the analysis prior
// rather than to a panic or a fabricated winner.
func (c *Client) decisionFromWire(rq Request, a wireAnswer, usage wireUsage, latencyMS float64) *Decision {
	keys := criterionKeys(rq)
	prob := normalizeProbabilities(a.Probabilities, keys, rq.Prior)
	ans := Answer{
		Probabilities: prob,
		Confidence:    a.Confidence,
		Legend:        a.Legend,
	}
	if len(ans.Legend) == 0 {
		ans.Legend = legendOf(rq)
	}
	switch rq.Kind {
	case KindScore:
		if a.Score != nil {
			ans.Score = clamp01(*a.Score)
			ans.Probabilities = scoreDistribution(rq, ans.Score)
			ans.Choice = nearestLevel(rq, ans.Score)
		}
	case KindNoul:
		n := 0.5
		if a.Noul != nil {
			n = clamp01(*a.Noul)
		} else if p, ok := prob["true"]; ok {
			n = p
		}
		ans.Noul = n
		ans.Probabilities = map[string]float64{"true": n, "false": 1 - n}
		ans.Choice = "true"
		if n < 0.5 {
			ans.Choice = "false"
		}
	default:
		ans.Choice = a.Choice
	}
	if ans.Choice == "" {
		ans.Choice = argmax(prob)
	}
	if ans.Confidence <= 0 {
		ans.Confidence = maxOf(prob)
	}
	ans.Confidence = clamp01(ans.Confidence)
	impact := rq.Impact
	if impact <= 0 {
		impact = 0.25
	}
	d := &Decision{
		ID:           rq.ID,
		Stage:        rq.Stage,
		Kind:         rq.Kind,
		Question:     rq.Instructions,
		Options:      optionsOf(rq, prob),
		Answer:       ans,
		Source:       SourceJev,
		LatencyMS:    latencyMS,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		Impact:       impact,
		Action:       rq.Action,
		TargetID:     rq.TargetID,
	}
	d.Priority = priorityOf(impact, prob)
	return d
}

// --- decision constructors --------------------------------------------------

func skipDecision(rq Request, reason string) *Decision {
	d := baseDecision(rq, SourceSkipped)
	d.SkipReason = reason
	return d
}

func priorDecision(rq Request, reason string) *Decision {
	d := baseDecision(rq, SourcePrior)
	d.SkipReason = reason
	return d
}

func userDecision(rq Request, option string) *Decision {
	d := baseDecision(rq, SourceUser)
	if _, ok := d.Answer.Probabilities[option]; !ok {
		// A user answer outside the candidate set is still honoured — the UI
		// allows it — but it must not pretend to be one of them.
		for k := range d.Answer.Probabilities {
			d.Answer.Probabilities[k] = 0
		}
	}
	d.Answer.Probabilities[option] = 1
	d.Answer.Choice = option
	d.Answer.Confidence = 1
	d.Priority = 0 // answered: nothing left to schedule
	return d
}

func baseDecision(rq Request, source string) *Decision {
	keys := criterionKeys(rq)
	raw := map[string]float64{}
	if rq.Prior != nil {
		for _, k := range keys {
			if p := rq.Prior.P(k); p > 0 {
				raw[k] = p
			}
		}
	}
	prob := normalizeProbabilities(raw, keys, nil)
	ans := Answer{Probabilities: prob, Legend: legendOf(rq)}
	switch rq.Kind {
	case KindScore:
		ans.Choice = argmax(prob)
		ans.Score = levelScore(ans.Choice)
	case KindNoul:
		n := prob["true"]
		if len(prob) > 1 {
			n = 0.5
		}
		ans.Noul = n
		ans.Probabilities = map[string]float64{"true": n, "false": 1 - n}
		ans.Choice = "false"
		if n >= 0.5 {
			ans.Choice = "true"
		}
	default:
		ans.Choice = argmax(prob)
	}
	ans.Confidence = clamp01(maxOf(prob))
	impact := rq.Impact
	if impact <= 0 {
		impact = 0.25
	}
	return &Decision{
		ID:       rq.ID,
		Stage:    rq.Stage,
		Kind:     rq.Kind,
		Question: rq.Instructions,
		Options:  optionsOf(rq, prob),
		Answer:   ans,
		Source:   source,
		Impact:   impact,
		Priority: priorityOf(impact, prob),
		Action:   rq.Action,
		TargetID: rq.TargetID,
	}
}

// Skip records a decision that will not be asked, with the reason. The decision
// graph uses it so that a skip is produced by the same code path as an answer.
func (c *Client) Skip(rq Request, reason string) *Decision { return skipDecision(rq, reason) }

// --- request normalization --------------------------------------------------

func normalizeRequest(rq Request, idx int) Request {
	if strings.TrimSpace(rq.ID) == "" {
		rq.ID = "JEV-" + strconv.Itoa(idx+1)
	}
	if strings.TrimSpace(rq.Stage) == "" {
		rq.Stage = StageLexical
	}
	switch strings.ToLower(strings.TrimSpace(rq.Kind)) {
	case KindScore:
		rq.Kind = KindScore
	case KindNoul:
		rq.Kind = KindNoul
	default:
		rq.Kind = KindChoice
	}
	if rq.Impact <= 0 {
		rq.Impact = 0.25
	}
	if rq.Impact > 1 {
		rq.Impact = 1
	}
	if rq.SemanticContext == "" {
		rq.SemanticContext = rq.Stage + "|" + rq.Action + "|" + rq.TargetID + "|" + rq.ID
	}
	if strings.TrimSpace(rq.Instructions) == "" {
		rq.Instructions = defaultInstructions(rq)
	}
	rq.Criteria = normalizeCriteria(rq)
	return rq
}

func defaultInstructions(rq Request) string {
	switch rq.Kind {
	case KindScore:
		return "Rate the situation on the ordered scale given."
	case KindNoul:
		return "Answer whether the stated condition holds."
	default:
		return "Choose the option the source text best supports."
	}
}

// normalizeCriteria enforces the protocol limits: dedupe by key, cap a choice
// question at the option ceiling, keep a score scale inside 2..10 levels, and
// give a noul question its two sides.
func normalizeCriteria(rq Request) []Criterion {
	seen := map[string]bool{}
	out := make([]Criterion, 0, len(rq.Criteria))
	for _, cr := range rq.Criteria {
		k := strings.TrimSpace(cr.Key)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Criterion{Key: k, Description: truncate(cr.Description, 255), Prior: cr.Prior})
	}
	switch rq.Kind {
	case KindScore:
		if len(out) > MaxScoreLevels {
			out = out[:MaxScoreLevels]
		}
	case KindNoul:
		// A noul question without rubrics is legal on the wire; naming both
		// sides is easier for the model and comparable across documents.
		if len(out) == 0 {
			out = []Criterion{
				{Key: "true", Description: "the condition holds"},
				{Key: "false", Description: "the condition does not hold"},
			}
		}
	default:
		if len(out) > MaxChoiceOptions {
			// Keep the highest prior options, ties broken by key, so the cut
			// is deterministic.
			sort.SliceStable(out, func(i, j int) bool {
				if out[i].Prior != out[j].Prior {
					return out[i].Prior > out[j].Prior
				}
				return out[i].Key < out[j].Key
			})
			out = out[:MaxChoiceOptions]
		}
	}
	return out
}

// unaskable reports why a request must not reach the oracle at all. It is the
// last line of the "never ask what the code cannot use" rule (plan.md §31).
func unaskable(rq Request) string {
	switch rq.Kind {
	case KindScore:
		if len(rq.Criteria) < MinScoreLevels {
			return "score question needs at least " + itoa(MinScoreLevels) + " levels"
		}
	case KindNoul:
		if len(rq.Criteria) < 2 {
			return "noul question needs both a true and a false side"
		}
	default:
		if len(rq.Criteria) == 0 {
			return "choice question has no candidate set"
		}
		if len(rq.Criteria) > MaxChoiceOptions {
			return "candidate set of " + itoa(len(rq.Criteria)) + " exceeds the " +
				itoa(MaxChoiceOptions) + " option ceiling; it needs the hierarchical ladder"
		}
	}
	return ""
}

func criterionKeys(rq Request) []string {
	out := make([]string, 0, len(rq.Criteria))
	for _, cr := range rq.Criteria {
		out = append(out, cr.Key)
	}
	return out
}

func legendOf(rq Request) map[string]string {
	out := make(map[string]string, len(rq.Criteria))
	for _, cr := range rq.Criteria {
		if cr.Description != "" {
			out[cr.Key] = cr.Description
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func optionsOf(rq Request, prob map[string]float64) []Option {
	if len(rq.Criteria) == 0 {
		// A bare probability map still deserves options: the UI renders them
		// as the candidate set.
		keys := make([]string, 0, len(prob))
		for k := range prob {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]Option, 0, len(keys))
		for _, k := range keys {
			out = append(out, Option{Key: k, Probability: prob[k]})
		}
		return out
	}
	out := make([]Option, 0, len(rq.Criteria))
	for _, cr := range rq.Criteria {
		out = append(out, Option{Key: cr.Key, Description: cr.Description, Probability: prob[cr.Key]})
	}
	return out
}

func wireQuestionOf(rq Request) wireQuestion {
	wq := wireQuestion{Type: rq.Kind, Instructions: truncate(rq.Instructions, 2000)}
	switch rq.Kind {
	case KindScore:
		levels := make([]string, 0, len(rq.Criteria))
		for _, cr := range rq.Criteria {
			levels = append(levels, truncate(cr.Description, 255))
		}
		wq.Criteria = levels
	case KindNoul:
		crit := map[string]string{}
		for _, cr := range rq.Criteria {
			if cr.Description != "" {
				crit[cr.Key] = truncate(cr.Description, 255)
			}
		}
		if len(crit) > 0 {
			wq.Criteria = crit
		}
	default:
		crit := make(map[string]string, len(rq.Criteria))
		for _, cr := range rq.Criteria {
			crit[cr.Key] = truncate(cr.Description, 255)
		}
		wq.Criteria = crit
	}
	return wq
}

// --- small numeric helpers --------------------------------------------------

func normalizeProbabilities(raw map[string]float64, keys []string, prior *jlir.Distribution) map[string]float64 {
	out := make(map[string]float64, len(keys))
	put := func(k string, v float64) {
		if v > 0 {
			out[k] += v
		}
	}
	if len(keys) == 0 {
		for k, v := range raw {
			put(k, v)
		}
		return rescale(out)
	}
	for _, k := range keys {
		put(k, raw[k])
	}
	if totalOf(out) <= 0 && prior != nil {
		for _, k := range keys {
			put(k, prior.P(k))
		}
	}
	if totalOf(out) <= 0 {
		for _, k := range keys {
			out[k] = 1
		}
	}
	return rescale(out)
}

func rescale(m map[string]float64) map[string]float64 {
	total := totalOf(m)
	if total <= 0 {
		for k := range m {
			delete(m, k)
		}
		return m
	}
	for k := range m {
		m[k] /= total
	}
	return m
}

func totalOf(m map[string]float64) float64 {
	t := 0.0
	for _, v := range m {
		if v > 0 {
			t += v
		}
	}
	return t
}

func maxOf(m map[string]float64) float64 {
	best := 0.0
	for _, v := range m {
		if v > best {
			best = v
		}
	}
	return best
}

func argmax(m map[string]float64) string {
	best, bp := "", -1.0
	for k, v := range m {
		if v > bp || (v == bp && k < best) {
			best, bp = k, v
		}
	}
	return best
}

func itoa(i int) string { return strconv.Itoa(i) }

// levelScore maps an option key back onto the ordinal scale of a score question:
// "lvl_3" is the third level. Keys that do not follow the pattern return 0.
func levelScore(key string) float64 {
	if !strings.HasPrefix(key, "lvl_") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(key, "lvl_"))
	if err != nil {
		return 0
	}
	return float64(n)
}

// scoreLevels builds the option keys of an ordered scale, 1-based, with the
// given level descriptions. The number of levels must be 2..10 (plan.md's
// System One scale constraint).
func scoreLevels(descriptions ...string) []Criterion {
	if len(descriptions) < MinScoreLevels {
		return nil
	}
	if len(descriptions) > MaxScoreLevels {
		descriptions = descriptions[:MaxScoreLevels]
	}
	out := make([]Criterion, 0, len(descriptions))
	for i, d := range descriptions {
		out = append(out, Criterion{
			Key:         "lvl_" + strconv.Itoa(i+1),
			Description: d,
			Prior:       1 / float64(len(descriptions)),
		})
	}
	return out
}

// scoreDistribution turns a position on an ordered scale into a distribution
// over its levels: the nearest levels carry the mass, further ones fade. It is
// what lets a score answer be compared with a choice answer downstream.
func scoreDistribution(rq Request, score float64) map[string]float64 {
	out := map[string]float64{}
	if len(rq.Criteria) == 0 {
		return out
	}
	n := float64(len(rq.Criteria))
	const sigma = 0.75
	for i, cr := range rq.Criteria {
		pos := levelScore(cr.Key)
		if pos <= 0 {
			pos = float64(i + 1)
		}
		d := pos - clamp(score, 1, n)
		out[cr.Key] = math.Exp(-(d * d) / (2 * sigma * sigma))
	}
	return rescale(out)
}

func nearestLevel(rq Request, score float64) string {
	best := ""
	bestD := math.MaxFloat64
	for i, cr := range rq.Criteria {
		pos := levelScore(cr.Key)
		if pos <= 0 {
			pos = float64(i + 1)
		}
		if d := math.Abs(pos - score); d < bestD {
			best, bestD = cr.Key, d
		}
	}
	return best
}

// priorityOf is plan.md §34: expected translation impact × uncertainty.
func priorityOf(impact float64, prob map[string]float64) float64 {
	return impact * uncertainty(prob)
}

// uncertainty is one minus the probability gap between the two best options,
// divided by the largest gap the option count allows, so that a 200-way choice
// is not automatically "maximally uncertain".
func uncertainty(prob map[string]float64) float64 {
	if len(prob) < 2 {
		return 0
	}
	first, second := 0.0, 0.0
	for _, p := range prob {
		switch {
		case p > first:
			second = first
			first = p
		case p > second:
			second = p
		}
	}
	u := 1 - (first - second)
	if u <= 0 {
		return 0
	}
	if n := float64(len(prob)); n > 2 {
		u /= 1 - 1/n
		if u > 1 {
			u = 1
		}
	}
	return u
}

func clamp01(f float64) float64 { return clamp(f, 0, 1) }

func clamp(f, lo, hi float64) float64 {
	if f < lo {
		return lo
	}
	if f > hi {
		return hi
	}
	return f
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// truncate cuts a string to n bytes on a rune boundary: the payload is UTF-8
// and half a rune would make the whole request invalid.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
