// Package core provides topology-aware Redis/Valkey client abstractions for
// framework components that need a small namespaced command surface.
package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisClient provides a simplified, namespaced Redis command surface.
type RedisClient struct {
	client     redis.UniversalClient
	ownsClient bool
	dbID       int
	namespace  string
	logger     Logger // Optional logger
	closeMu    sync.Mutex
}

// RedisClientConnectionOptions configures an owning topology-aware client.
// Built-in callers pass a prefix returned by RedisKeyspace.Plain or Tagged.
type RedisClientConnectionOptions struct {
	Connection RedisConnectionConfig
	Namespace  string
	Logger     Logger
}

// NewRedisClientWithConnection constructs and owns a topology-aware DB-0 client.
func NewRedisClientWithConnection(opts RedisClientConnectionOptions) (*RedisClient, error) {
	namespace := strings.TrimSuffix(strings.TrimSpace(opts.Namespace), ":")
	if namespace == "" {
		return nil, fmt.Errorf("redis key namespace is required: %w", ErrInvalidConfiguration)
	}
	profile, err := normalizeRedisConnectionConfig(opts.Connection)
	if err != nil {
		return nil, err
	}
	client, err := NewRedisUniversalClient(profile)
	if err != nil {
		return nil, fmt.Errorf("initialize namespaced Redis client: %w", err)
	}
	rc := &RedisClient{
		client: client, ownsClient: true, dbID: profile.DB,
		namespace: namespace, logger: coreComponentLogger(opts.Logger),
	}
	if rc.logger != nil {
		rc.logger.Info("Redis client connected", map[string]interface{}{
			"operation":  "redis_client_connect",
			"redis_mode": profile.Mode,
			"db":         profile.DB,
			"namespace":  namespace,
		})
	}
	return rc, nil
}

// NewRedisClientWithClient wraps an application-owned universal client.
func NewRedisClientWithClient(client redis.UniversalClient, namespace string, logger Logger) (*RedisClient, error) {
	if nilRedisUniversalClient(client) {
		return nil, fmt.Errorf("redis client is required: %w", ErrInvalidConfiguration)
	}
	namespace = strings.TrimSuffix(strings.TrimSpace(namespace), ":")
	if namespace == "" {
		return nil, fmt.Errorf("redis key namespace is required: %w", ErrInvalidConfiguration)
	}
	return &RedisClient{client: client, namespace: namespace, logger: coreComponentLogger(logger)}, nil
}

func coreComponentLogger(logger Logger) Logger {
	if componentAware, ok := logger.(ComponentAwareLogger); ok {
		return componentAware.WithComponent("framework/core")
	}
	return logger
}

// Close closes the Redis connection
func (r *RedisClient) Close() error {
	if r == nil {
		return nil
	}
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	if !r.ownsClient {
		return nil
	}
	r.ownsClient = false
	if r.logger != nil {
		r.logger.Info("Closing Redis client connection", map[string]interface{}{
			"operation": "redis_client_close",
			"db":        r.dbID,
			"namespace": r.namespace,
		})
	}

	err := r.client.Close()
	if err != nil && r.logger != nil {
		r.logger.Error("Failed to close Redis client", map[string]interface{}{
			"operation":  "redis_client_close",
			"error":      "redis client close failed",
			"error_type": "backend",
			"db":         r.dbID,
			"namespace":  r.namespace,
		})
	}

	return err
}

// GetDB returns the DB number being used
func (r *RedisClient) GetDB() int {
	return r.dbID
}

// GetNamespace returns the namespace being used
func (r *RedisClient) GetNamespace() string {
	return r.namespace
}

// formatKey formats a key with the namespace
func (r *RedisClient) formatKey(key string) string {
	if r.namespace != "" {
		return fmt.Sprintf("%s:%s", r.namespace, key)
	}
	return key
}

// --- Rate Limiting Operations ---

// Incr increments a counter
func (r *RedisClient) Incr(ctx context.Context, key string) (int64, error) {
	return r.client.Incr(ctx, r.formatKey(key)).Result()
}

// IncrBy increments a counter by a specific amount
func (r *RedisClient) IncrBy(ctx context.Context, key string, value int64) (int64, error) {
	return r.client.IncrBy(ctx, r.formatKey(key), value).Result()
}

// Expire sets a TTL on a key
func (r *RedisClient) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return r.client.Expire(ctx, r.formatKey(key), ttl).Err()
}

// Get retrieves a value
func (r *RedisClient) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, r.formatKey(key)).Result()
}

// Set stores a value with optional TTL
func (r *RedisClient) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return r.client.Set(ctx, r.formatKey(key), value, ttl).Err()
}

// Del deletes keys
func (r *RedisClient) Del(ctx context.Context, keys ...string) error {
	for _, key := range keys {
		if err := r.client.Del(ctx, r.formatKey(key)).Err(); err != nil {
			return err
		}
	}
	return nil
}

// TTL gets the TTL of a key
func (r *RedisClient) TTL(ctx context.Context, key string) (time.Duration, error) {
	return r.client.TTL(ctx, r.formatKey(key)).Result()
}

// --- Sorted Set Operations (for sliding window) ---

// ZAdd adds members to a sorted set
func (r *RedisClient) ZAdd(ctx context.Context, key string, members ...redis.Z) error {
	return r.client.ZAdd(ctx, r.formatKey(key), members...).Err()
}

// ZRemRangeByScore removes members by score range
func (r *RedisClient) ZRemRangeByScore(ctx context.Context, key string, min, max string) error {
	return r.client.ZRemRangeByScore(ctx, r.formatKey(key), min, max).Err()
}

// ZCard gets the cardinality of a sorted set
func (r *RedisClient) ZCard(ctx context.Context, key string) (int64, error) {
	return r.client.ZCard(ctx, r.formatKey(key)).Result()
}

// ZCount counts members in a score range
func (r *RedisClient) ZCount(ctx context.Context, key string, min, max string) (int64, error) {
	return r.client.ZCount(ctx, r.formatKey(key), min, max).Result()
}

// ZRevRange returns members by rank range in descending score order
func (r *RedisClient) ZRevRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	// Keep the legacy command so external Redis-compatible servers do not need
	// the Redis 6.2 ZRANGE REV syntax solely because the Go client was upgraded.
	//nolint:staticcheck // ZRevRange remains supported by go-redis/v9.
	return r.client.ZRevRange(ctx, r.formatKey(key), start, stop).Result()
}

// ZRem removes members from a sorted set
func (r *RedisClient) ZRem(ctx context.Context, key string, members ...interface{}) error {
	return r.client.ZRem(ctx, r.formatKey(key), members...).Err()
}

// --- Pipeline Operations (for efficiency) ---

// Pipeline creates a pipeline for batched operations.
// Note: Pipeline uses raw go-redis commands. Use FormatKey() to namespace keys.
func (r *RedisClient) Pipeline() redis.Pipeliner {
	return r.client.Pipeline()
}

// FormatKey returns a fully namespaced key for use with Pipeline operations.
func (r *RedisClient) FormatKey(key string) string {
	return r.formatKey(key)
}

// --- Health Check ---

// HealthCheck verifies Redis connectivity
func (r *RedisClient) HealthCheck(ctx context.Context) error {
	startedAt := time.Now()
	if r.logger != nil {
		r.logger.DebugWithContext(ctx, "Performing Redis health check", map[string]interface{}{
			"operation":  "redis_health_check",
			"request_id": GetRequestID(ctx),
			"db":         r.dbID,
			"namespace":  r.namespace,
		})
	}

	err := r.client.Ping(ctx).Err()
	if err != nil {
		if r.logger != nil {
			r.logger.ErrorWithContext(ctx, "Redis health check failed", map[string]interface{}{
				"operation":   "redis_health_check",
				"request_id":  GetRequestID(ctx),
				"error":       "redis health check failed",
				"error_type":  "backend",
				"duration_ms": time.Since(startedAt).Milliseconds(),
				"db":          r.dbID,
				"namespace":   r.namespace,
			})
		}
	} else {
		if r.logger != nil {
			r.logger.DebugWithContext(ctx, "Redis health check passed", map[string]interface{}{
				"operation":   "redis_health_check",
				"request_id":  GetRequestID(ctx),
				"duration_ms": time.Since(startedAt).Milliseconds(),
				"db":          r.dbID,
				"namespace":   r.namespace,
			})
		}
	}

	return err
}
