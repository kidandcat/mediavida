package main

import (
	"sync"
	"time"
)

// TTLCache is a small concurrency-safe cache keyed by string, storing the
// already-marshaled JSON bytes of shared (identical-for-everyone) MV pages so
// N users hitting them within a TTL window collapse to a single upstream fetch.
// Entries expire lazily on Get.
type TTLCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	val    []byte
	expiry time.Time
}

// NewTTLCache returns a TTLCache whose entries live for ttl.
func NewTTLCache(ttl time.Duration) *TTLCache {
	return &TTLCache{
		ttl:     ttl,
		entries: make(map[string]cacheEntry),
	}
}

// Get returns the cached bytes for key and true when present and unexpired.
// Expired entries are treated as a miss (and lazily evicted).
func (c *TTLCache) Get(key string) ([]byte, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expiry) {
		c.mu.Lock()
		// Re-check under the write lock: another writer may have refreshed it.
		if cur, still := c.entries[key]; still && !time.Now().After(cur.expiry) {
			c.mu.Unlock()
			return cur.val, true
		}
		delete(c.entries, key)
		c.mu.Unlock()
		return nil, false
	}
	return e.val, true
}

// Set stores val under key with the cache's TTL.
func (c *TTLCache) Set(key string, val []byte) {
	c.mu.Lock()
	c.entries[key] = cacheEntry{val: val, expiry: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}
