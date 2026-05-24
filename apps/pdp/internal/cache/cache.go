// Package cache implements the two-tier decision cache for the PDP.
//
// L1: in-process sync.Map + LRU eviction (cap: 10 000 entries, < 100 ns hit).
// L2: Redis (cluster-aware in prod, single node in dev, < 1 ms hit).
//
// Cache key: pdp:v1:{tenantId}:{userId}:{action}:{resourceSHA}:{policyVersion}:{attrsSHA}
// Stale entries become unreachable when policyVersion is bumped — no explicit delete needed.
// Singleflight prevents stampede on concurrent cache misses.
package cache

import (
	"context"
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	pdpmetrics "github.com/governance-platform/pdp/internal/metrics"
	"github.com/governance-platform/policy-engine/engine"
)

const (
	l1Cap        = 10_000
	defaultTTL   = 5 * time.Minute
	negCacheTTL  = 30 * time.Second
	cacheVersion = "v1"
)

// DecisionKey carries the cache lookup key components.
type DecisionKey struct {
	TenantID      string
	UserID        string
	Action        string
	ResourceURI   string
	PolicyVersion string
	ContextAttrs  map[string]string
	ResourceAttrs map[string]interface{}
}

func (k DecisionKey) String() string {
	resSHA := sha16(k.ResourceURI)
	attrsSHA := sha16(attrsDigest(k.ContextAttrs, k.ResourceAttrs))
	return fmt.Sprintf("pdp:%s:%s:%s:%s:%s:%s:%s",
		cacheVersion, k.TenantID, k.UserID, k.Action, resSHA, k.PolicyVersion, attrsSHA)
}

// Cache is the two-tier decision cache.
type Cache struct {
	l1     *lruMap
	l2     *redis.Client
	flight singleflight.Group
}

// New creates a Cache. If rdb is nil, L2 is disabled (dev/test mode).
func New(rdb *redis.Client) *Cache {
	return &Cache{
		l1: newLRU(l1Cap),
		l2: rdb,
	}
}

// Get looks up a decision. Returns (decision, layer, found).
// layer ∈ {"L1", "L2", "miss"}.
func (c *Cache) Get(ctx context.Context, key DecisionKey) (*engine.Decision, string, bool) {
	k := key.String()

	// L1
	if v, ok := c.l1.Get(k); ok {
		pdpmetrics.DecisionTotal.WithLabelValues(string(v.Effect), "L1", key.TenantID).Inc()
		return v, "L1", true
	}

	// L2
	if c.l2 != nil {
		b, err := c.l2.Get(ctx, k).Bytes()
		if err == nil {
			var d engine.Decision
			if json.Unmarshal(b, &d) == nil {
				c.l1.Set(k, &d) // backfill L1
				pdpmetrics.DecisionTotal.WithLabelValues(string(d.Effect), "L2", key.TenantID).Inc()
				return &d, "L2", true
			}
		}
		if err != redis.Nil {
			pdpmetrics.RedisDown.Set(1)
		} else {
			pdpmetrics.RedisDown.Set(0)
		}
	}

	return nil, "miss", false
}

// Set stores a decision in both L1 and L2.
func (c *Cache) Set(ctx context.Context, key DecisionKey, d *engine.Decision) {
	k := key.String()
	c.l1.Set(k, d)
	pdpmetrics.CacheL1Size.Set(float64(c.l1.Len()))

	if c.l2 != nil {
		b, err := json.Marshal(d)
		if err != nil {
			return
		}
		ttl := defaultTTL
		if d.Effect == engine.EffectDeny {
			ttl = negCacheTTL
		}
		if err := c.l2.Set(ctx, k, b, ttl).Err(); err != nil {
			pdpmetrics.RedisDown.Set(1)
		} else {
			pdpmetrics.RedisDown.Set(0)
		}
	}
}

// EvictTenant drops all L1 entries for the given tenant.
// L2 entries become stale-and-unreachable via the versioned key design.
func (c *Cache) EvictTenant(tenantID string) {
	prefix := fmt.Sprintf("pdp:%s:%s:", cacheVersion, tenantID)
	c.l1.EvictPrefix(prefix)
	pdpmetrics.CacheL1Size.Set(float64(c.l1.Len()))
}

// Do executes fn with singleflight coalescing on key.
// Use this to prevent stampede on cache miss.
func (c *Cache) Do(key DecisionKey, fn func() (*engine.Decision, error)) (*engine.Decision, error) {
	k := key.String()
	v, err, _ := c.flight.Do(k, func() (interface{}, error) {
		return fn()
	})
	if err != nil {
		return nil, err
	}
	return v.(*engine.Decision), nil
}

// ── LRU implementation ────────────────────────────────────────────────────────

type lruEntry struct {
	key      string
	decision *engine.Decision
}

type lruMap struct {
	mu      sync.Mutex
	cap     int
	items   map[string]*list.Element
	order   *list.List
}

func newLRU(cap int) *lruMap {
	return &lruMap{
		cap:   cap,
		items: make(map[string]*list.Element, cap),
		order: list.New(),
	}
}

func (m *lruMap) Get(key string) (*engine.Decision, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.items[key]
	if !ok {
		return nil, false
	}
	m.order.MoveToFront(el)
	return el.Value.(*lruEntry).decision, true
}

func (m *lruMap) Set(key string, d *engine.Decision) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[key]; ok {
		el.Value.(*lruEntry).decision = d
		m.order.MoveToFront(el)
		return
	}
	if m.order.Len() >= m.cap {
		// Evict LRU.
		back := m.order.Back()
		if back != nil {
			m.order.Remove(back)
			delete(m.items, back.Value.(*lruEntry).key)
		}
	}
	el := m.order.PushFront(&lruEntry{key: key, decision: d})
	m.items[key] = el
}

func (m *lruMap) EvictPrefix(prefix string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, el := range m.items {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			m.order.Remove(el)
			delete(m.items, k)
		}
	}
}

func (m *lruMap) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func sha16(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8])
}

func attrsDigest(ctx map[string]string, res map[string]interface{}) string {
	b1, _ := json.Marshal(ctx)
	b2, _ := json.Marshal(res)
	h := sha256.Sum256(append(b1, b2...))
	return fmt.Sprintf("%x", h[:8])
}
