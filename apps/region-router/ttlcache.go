package main

import (
	"sync"
	"time"
)

// ttlCache is a generic thread-safe in-memory cache with TTL eviction.
type ttlCache[K comparable, V any] struct {
	mu    sync.RWMutex
	items map[K]cacheEntry[V]
	ttl   time.Duration
}

type cacheEntry[V any] struct {
	value     V
	expiresAt time.Time
}

func newTTLCache[K comparable, V any](ttl time.Duration) *ttlCache[K, V] {
	return &ttlCache[K, V]{
		items: make(map[K]cacheEntry[V]),
		ttl:   ttl,
	}
}

func (c *ttlCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		var zero V
		return zero, false
	}
	return entry.value, true
}

func (c *ttlCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	c.items[key] = cacheEntry[V]{value: value, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *ttlCache[K, V]) Delete(key K) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}
