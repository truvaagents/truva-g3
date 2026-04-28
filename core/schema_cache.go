package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
)

// SchemaCache provides caching for JSON Schemas used in Phase 3 validation.
// This interface allows agents to cache schemas efficiently while supporting
// different caching strategies.
type SchemaCache interface {
	// Get retrieves a cached schema by tool and capability name.
	// Returns the schema and true if found, nil and false otherwise.
	Get(ctx context.Context, toolName, capabilityName string) (map[string]interface{}, bool)

	// Set stores a schema in the cache.
	// Returns an error if the cache operation fails.
	Set(ctx context.Context, toolName, capabilityName string, schema map[string]interface{}) error

	// Stats returns cache statistics for monitoring.
	Stats() map[string]interface{}
}

// RedisSchemaCache provides Redis-backed schema caching.
// Schemas are stored in Redis with configurable TTL and prefix.
// This provides shared caching across agent replicas with ~1-2ms latency.
type RedisSchemaCache struct {
	client *redis.Client
	ttl    time.Duration
	prefix string

	// Stats (atomic for thread-safety)
	hits   int64
	misses int64
}

// SchemaCacheOption allows customization of the schema cache behavior.
type SchemaCacheOption func(*RedisSchemaCache)

// WithTTL sets the TTL for schemas in Redis.
// Default is DefaultSchemaCacheTTL (24 hours). Schemas rarely change, so longer TTL is usually fine.
func WithTTL(ttl time.Duration) SchemaCacheOption {
	return func(c *RedisSchemaCache) {
		c.ttl = ttl
	}
}

// WithPrefix sets the Redis key prefix for schema cache entries.
// Default is DefaultRedisPrefix ("truvag3:schema:"). Useful for multi-tenant deployments.
func WithPrefix(prefix string) SchemaCacheOption {
	return func(c *RedisSchemaCache) {
		c.prefix = prefix
	}
}

// NewSchemaCache creates a new Redis-backed schema cache.
//
// Example usage:
//
//	// Default configuration
//	cache := NewSchemaCache(redisClient)
//
//	// Custom configuration
//	cache := NewSchemaCache(redisClient,
//	    WithTTL(1 * time.Hour),      // Shorter TTL
//	    WithPrefix("myapp:schemas:"), // Custom prefix
//	)
func NewSchemaCache(redisClient *redis.Client, opts ...SchemaCacheOption) SchemaCache {
	cache := &RedisSchemaCache{
		client: redisClient,
		ttl:    DefaultSchemaCacheTTL, // Schemas rarely change
		prefix: DefaultRedisPrefix,    // Namespace Redis keys
	}

	// Apply options
	for _, opt := range opts {
		opt(cache)
	}

	return cache
}

// Get retrieves a schema from Redis.
func (c *RedisSchemaCache) Get(ctx context.Context, toolName, capabilityName string) (map[string]interface{}, bool) {
	// Generate Redis key
	redisKey := fmt.Sprintf("%s%s:%s", c.prefix, toolName, capabilityName)

	// Query Redis
	val, err := c.client.Get(ctx, redisKey).Result()
	if err == redis.Nil {
		// Cache miss
		atomic.AddInt64(&c.misses, 1)
		// Emit framework metrics for schema cache miss
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("memory.cache.misses", "memory_type", "schema_cache")
		}
		return nil, false
	}
	if err != nil {
		// Redis error - degrade gracefully
		atomic.AddInt64(&c.misses, 1)
		// Emit framework metrics for schema cache error
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("memory.cache.misses", "memory_type", "schema_cache")
			registry.Counter("memory.operations", "operation", "get", "memory_type", "schema_cache", "result", "error")
		}
		return nil, false
	}

	// Parse schema from Redis
	var schema map[string]interface{}
	if err := json.Unmarshal([]byte(val), &schema); err != nil {
		// Corrupt data in Redis - treat as miss
		atomic.AddInt64(&c.misses, 1)
		// Emit framework metrics for corrupt cache entry
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("memory.cache.misses", "memory_type", "schema_cache")
			registry.Counter("memory.operations", "operation", "get", "memory_type", "schema_cache", "result", "corrupt")
		}
		return nil, false
	}

	atomic.AddInt64(&c.hits, 1)
	// Emit framework metrics for schema cache hit
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("memory.cache.hits", "memory_type", "schema_cache")
		registry.Counter("memory.operations", "operation", "get", "memory_type", "schema_cache", "result", "hit")
	}
	return schema, true
}

// Set stores a schema in Redis.
func (c *RedisSchemaCache) Set(ctx context.Context, toolName, capabilityName string, schema map[string]interface{}) error {
	// Generate Redis key
	redisKey := fmt.Sprintf("%s%s:%s", c.prefix, toolName, capabilityName)

	// Marshal schema
	data, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("failed to marshal schema: %w", err)
	}

	// Write to Redis
	if err := c.client.Set(ctx, redisKey, data, c.ttl).Err(); err != nil {
		// Emit framework metrics for failed cache set
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("memory.operations", "operation", "set", "memory_type", "schema_cache", "result", "error")
		}
		return fmt.Errorf("failed to set schema in Redis: %w", err)
	}

	// Emit framework metrics for successful cache set
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("memory.operations", "operation", "set", "memory_type", "schema_cache", "result", "success")
		registry.Gauge("memory.size_bytes", float64(len(data)), "memory_type", "schema_cache")
	}

	return nil
}

// Stats returns cache performance statistics for monitoring.
func (c *RedisSchemaCache) Stats() map[string]interface{} {
	hits := atomic.LoadInt64(&c.hits)
	misses := atomic.LoadInt64(&c.misses)
	total := hits + misses

	stats := map[string]interface{}{
		"hits":          hits,
		"misses":        misses,
		"total_lookups": total,
	}

	if total > 0 {
		stats["hit_rate"] = float64(hits) / float64(total)
	}

	return stats
}
