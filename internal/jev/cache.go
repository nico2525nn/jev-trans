package jev

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// plan.md §56: Jev calls are expensive and, within one document, highly
// repetitive. The cache key is the identity of the *decision*, not of the
// sentence: two sentences that pose the same question with the same candidates
// under the same style and the same model get the same answer.
const DefaultCacheSize = 512

// CacheStats is surfaced in the trace so the UI can prove how much of the run
// was free.
type CacheStats struct {
	Hits      int `json:"hits"`
	Misses    int `json:"misses"`
	Puts      int `json:"puts"`
	Evictions int `json:"evictions"`
	Size      int `json:"size"`
	Max       int `json:"max"`
}

// lruNode is an entry plus its position in the recency list. The list is
// intrusive: no separate index to keep in sync, no allocation per move.
type lruNode struct {
	key      string
	decision *Decision
	prev     *lruNode
	next     *lruNode
}

// Cache is a bounded LRU of decisions, safe for concurrent use. Values store
// the full answer, so a hit is indistinguishable from a call except for the
// CacheHit flag and the zero latency.
type Cache struct {
	mu        sync.Mutex
	max       int
	items     map[string]*lruNode
	head      *lruNode // most recently used
	tail      *lruNode // least recently used
	hits      int
	misses    int
	puts      int
	evictions int
}

// NewCache returns a cache holding at most max entries. A non-positive max
// falls back to DefaultCacheSize.
func NewCache(max int) *Cache {
	if max <= 0 {
		max = DefaultCacheSize
	}
	return &Cache{max: max, items: map[string]*lruNode{}}
}

func (c *Cache) unlink(n *lruNode) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		c.head = n.next
	}
	if n.next != nil {
		n.next.prev = n.prev
	} else {
		c.tail = n.prev
	}
	n.prev, n.next = nil, nil
}

func (c *Cache) pushFront(n *lruNode) {
	n.prev = nil
	n.next = c.head
	if c.head != nil {
		c.head.prev = n
	}
	c.head = n
	if c.tail == nil {
		c.tail = n
	}
}

// Get returns a copy of the cached decision. The copy matters: the caller
// annotates the decision it gets back (source, latency, skip reason) and must
// not write through into the cache.
func (c *Cache) Get(key string) (*Decision, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.items[key]
	if !ok {
		c.misses++
		return nil, false
	}
	c.hits++
	if c.head != n {
		c.unlink(n)
		c.pushFront(n)
	}
	return n.decision.Clone(), true
}

// Put stores a decision, evicting the least recently used entry when full.
func (c *Cache) Put(key string, d *Decision) {
	if c == nil || key == "" || d == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.items[key]; ok {
		n.decision = d.Clone()
		if c.head != n {
			c.unlink(n)
			c.pushFront(n)
		}
		return
	}
	n := &lruNode{key: key, decision: d.Clone()}
	c.items[key] = n
	c.pushFront(n)
	c.puts++
	for len(c.items) > c.max && c.tail != nil {
		victim := c.tail
		c.unlink(victim)
		delete(c.items, victim.key)
		c.evictions++
	}
}

// Invalidate drops every entry whose decision matches, which is how
// incremental translation (plan.md §57) forgets the pronoun choices that a new
// sentence has just settled.
func (c *Cache) Invalidate(match func(*Decision) bool) int {
	if c == nil || match == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dropped := 0
	for n := c.head; n != nil; {
		next := n.next
		if match(n.decision) {
			c.unlink(n)
			delete(c.items, n.key)
			dropped++
		}
		n = next
	}
	return dropped
}

// Reset empties the cache and zeroes the counters.
func (c *Cache) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = map[string]*lruNode{}
	c.head, c.tail = nil, nil
	c.hits, c.misses, c.puts, c.evictions = 0, 0, 0, 0
}

// Len reports the number of cached decisions.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Stats returns the counters for the trace.
func (c *Cache) Stats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{
		Hits: c.hits, Misses: c.misses, Puts: c.puts,
		Evictions: c.evictions, Size: len(c.items), Max: c.max,
	}
}

// CacheKey hashes the identity of a decision: the model that answered it, the
// decision type, the document style, the semantic context, the instructions
// and the candidate set (plan.md §56).
//
// The candidate set is a *set*: for choice and noul questions the order in
// which the pipeline happened to enumerate candidates is not part of the
// question. For score questions it is, because the levels are an ordered
// scale. Priors are deliberately excluded — a re-analysis that shifts the
// priors is asking a sharper question and deserves a fresh answer, not a
// cached one.
func CacheKey(model, kind, style, context, instructions string, crits []Criterion) string {
	h := sha256.New()
	field := func(s string) {
		h.Write([]byte(strconv.Itoa(len(s))))
		h.Write([]byte(":"))
		h.Write([]byte(s))
		h.Write([]byte("\x1f"))
	}
	field(model)
	field(kind)
	field(style)
	field(context)
	field(instructions)
	parts := make([]string, 0, len(crits))
	for _, cr := range crits {
		parts = append(parts, cr.Key+"\x1e"+cr.Description)
	}
	if kind != KindScore {
		sort.Strings(parts)
	}
	field(strings.Join(parts, "\x1d"))
	return hex.EncodeToString(h.Sum(nil))[:32]
}
