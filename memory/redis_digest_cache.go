package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface check.
var _ core.DigestCache = (*RedisDigestCache)(nil)

// RedisDigestCache stores digests in Redis, shared across all agent instances.
// Key: truvag3:memory:{domain}:digest
type RedisDigestCache struct {
	client   redis.Cmdable
	keyspace core.RedisKeyspace
	logger   core.Logger
}

// RedisDigestCacheOption configures RedisDigestCache.
type RedisDigestCacheOption func(*RedisDigestCache) error

// WithDigestCacheKeyspace sets the deployment-scoped Redis keyspace.
func WithDigestCacheKeyspace(keyspace core.RedisKeyspace) RedisDigestCacheOption {
	return func(cache *RedisDigestCache) error {
		cache.keyspace = keyspace
		return nil
	}
}

// NewRedisDigestCache creates a Redis-backed digest cache.
func NewRedisDigestCache(client redis.Cmdable, logger core.Logger, opts ...RedisDigestCacheOption) (*RedisDigestCache, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is required for RedisDigestCache")
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/memory")
	}
	cache := &RedisDigestCache{
		client:   client,
		keyspace: defaultRedisKeyspace(),
		logger:   logger,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("redis digest cache option cannot be nil")
		}
		if err := opt(cache); err != nil {
			return nil, fmt.Errorf("invalid Redis digest cache option: %w", err)
		}
	}
	return cache, nil
}

func (c *RedisDigestCache) digestKey(domain string) string {
	return c.keyspace.Plain("memory", domain, "digest")
}

func (c *RedisDigestCache) GetDigest(ctx context.Context, domain string) ([]byte, error) {
	data, err := c.client.Get(ctx, c.digestKey(domain)).Bytes()
	if err == redis.Nil {
		return nil, nil // Cache miss
	}
	if err != nil {
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Failed to read digest cache", map[string]interface{}{
				"operation":  "digest_cache",
				"request_id": core.GetRequestID(ctx),
				"domain":     domain,
				"error":      "redis digest cache read failed",
				"error_type": "cache_read",
			})
		}
		return nil, err
	}
	return data, nil
}

func (c *RedisDigestCache) SetDigest(ctx context.Context, domain string, data []byte, ttl time.Duration) error {
	err := c.client.Set(ctx, c.digestKey(domain), data, ttl).Err()
	if err != nil {
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Failed to write digest cache", map[string]interface{}{
				"operation":  "digest_cache",
				"request_id": core.GetRequestID(ctx),
				"domain":     domain,
				"error":      "redis digest cache write failed",
				"error_type": "cache_write",
			})
		}
	}
	return err
}
