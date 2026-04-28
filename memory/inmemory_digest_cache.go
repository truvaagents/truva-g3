package memory

import (
	"context"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface check.
var _ core.DigestCache = (*InMemoryDigestCache)(nil)

type cachedEntry struct {
	data      []byte
	expiresAt time.Time
}

// InMemoryDigestCache stores digests in memory per-instance. Not shared across pods.
// Suitable for single-instance deployments and testing.
type InMemoryDigestCache struct {
	mu    sync.Mutex
	store map[string]cachedEntry // domain → cached data + expiry
}

// NewInMemoryDigestCache creates an in-memory digest cache.
func NewInMemoryDigestCache() (*InMemoryDigestCache, error) {
	return &InMemoryDigestCache{
		store: make(map[string]cachedEntry),
	}, nil
}

func (c *InMemoryDigestCache) GetDigest(ctx context.Context, domain string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.store[domain]
	if !ok {
		return nil, nil // Cache miss
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.store, domain) // Lazy cleanup of expired entry
		return nil, nil
	}
	return entry.data, nil
}

func (c *InMemoryDigestCache) SetDigest(ctx context.Context, domain string, data []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[domain] = cachedEntry{
		data:      data,
		expiresAt: time.Now().Add(ttl),
	}
	return nil
}
