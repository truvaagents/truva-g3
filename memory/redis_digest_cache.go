package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface check.
var _ core.DigestCache = (*RedisDigestCache)(nil)

// RedisDigestCache stores digests in Redis, shared across all agent instances.
// Key: truvag3:memory:{domain}:digest
type RedisDigestCache struct {
	client redis.Cmdable
	logger core.Logger
}

// NewRedisDigestCache creates a Redis-backed digest cache.
func NewRedisDigestCache(client redis.Cmdable, logger core.Logger) (*RedisDigestCache, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is required for RedisDigestCache")
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/memory")
	}
	return &RedisDigestCache{
		client: client,
		logger: logger,
	}, nil
}

func digestKey(domain string) string {
	return fmt.Sprintf("truvag3:memory:%s:digest", domain)
}

func (c *RedisDigestCache) GetDigest(ctx context.Context, domain string) ([]byte, error) {
	data, err := c.client.Get(ctx, digestKey(domain)).Bytes()
	if err == redis.Nil {
		return nil, nil // Cache miss
	}
	if err != nil {
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Failed to read digest cache", map[string]interface{}{
				"operation":  "digest_cache",
				"domain":     domain,
				"error":      err.Error(),
				"error_type": "cache_read",
			})
		}
		return nil, err
	}
	return data, nil
}

func (c *RedisDigestCache) SetDigest(ctx context.Context, domain string, data []byte, ttl time.Duration) error {
	err := c.client.Set(ctx, digestKey(domain), data, ttl).Err()
	if err != nil {
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Failed to write digest cache", map[string]interface{}{
				"operation":  "digest_cache",
				"domain":     domain,
				"error":      err.Error(),
				"error_type": "cache_write",
			})
		}
	}
	return err
}
