package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/orchestration"
)

var providerExtendMinimumTTLScript = redis.NewScript(`
local current = redis.call("PTTL", KEYS[1])
local requested = tonumber(ARGV[1])
if current == -2 or current == -1 or current >= requested then
	return current
end
redis.call("PEXPIRE", KEYS[1], requested)
return current
`)

var providerSetWithMinimumTTLScript = redis.NewScript(`
local previous = redis.call("PTTL", KEYS[1])
redis.call("SET", KEYS[1], ARGV[1])
if previous == -1 then
	return previous
end
local requested = tonumber(ARGV[2])
local selected = requested
if previous >= 0 and previous > selected then
	selected = previous
end
redis.call("PEXPIRE", KEYS[1], selected)
return previous
`)

// RedisStorageProvider implements the required ExecutionStorageProvider and
// optional IndexTTLManager interfaces using Redis.
// This is an application-level implementation that the orchestration module
// accepts through dependency injection.
type RedisStorageProvider struct {
	client *redis.Client
}

// NewRedisStorageProvider creates a Redis-backed execution storage provider.
func NewRedisStorageProvider(client *redis.Client) *RedisStorageProvider {
	return &RedisStorageProvider{
		client: client,
	}
}

// Get retrieves a value by key. Returns empty string if not found.
func (r *RedisStorageProvider) Get(ctx context.Context, key string) (string, error) {
	val, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil // Key not found is not an error
	}
	if err != nil {
		return "", err
	}
	return val, nil
}

// Set stores a value with TTL. Use 0 for no expiration.
func (r *RedisStorageProvider) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

// ExtendKeyTTL implements orchestration.KeyTTLManager without creating a
// missing key, shortening a longer lifetime, or expiring a persistent key.
func (r *RedisStorageProvider) ExtendKeyTTL(
	ctx context.Context,
	key string,
	minTTL time.Duration,
) error {
	milliseconds, err := providerTTLMilliseconds(minTTL)
	if err != nil {
		return err
	}
	return providerExtendMinimumTTLScript.Run(
		ctx,
		r.client,
		[]string{key},
		strconv.FormatInt(milliseconds, 10),
	).Err()
}

// SetKeyWithMinimumTTL atomically writes a value while preserving a longer or
// persistent lifetime already attached to the key.
func (r *RedisStorageProvider) SetKeyWithMinimumTTL(
	ctx context.Context,
	key string,
	value string,
	minTTL time.Duration,
) error {
	milliseconds, err := providerTTLMilliseconds(minTTL)
	if err != nil {
		return err
	}
	return providerSetWithMinimumTTLScript.Run(
		ctx,
		r.client,
		[]string{key},
		value,
		strconv.FormatInt(milliseconds, 10),
	).Err()
}

// ExtendIndexTTL implements orchestration.IndexTTLManager with the same
// atomic minimum-lifetime semantics as ordinary keys.
func (r *RedisStorageProvider) ExtendIndexTTL(
	ctx context.Context,
	key string,
	minTTL time.Duration,
) error {
	return r.ExtendKeyTTL(ctx, key, minTTL)
}

func providerTTLMilliseconds(ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	milliseconds := ttl.Milliseconds()
	if milliseconds <= 0 {
		milliseconds = 1
	}
	return milliseconds, nil
}

// Del deletes one or more keys.
func (r *RedisStorageProvider) Del(ctx context.Context, keys ...string) error {
	return r.client.Del(ctx, keys...).Err()
}

// Exists checks if a key exists.
func (r *RedisStorageProvider) Exists(ctx context.Context, key string) (bool, error) {
	count, err := r.client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// AddToIndex adds a member with score to a sorted index.
// Redis implementation uses ZADD.
func (r *RedisStorageProvider) AddToIndex(ctx context.Context, key string, score float64, member string) error {
	return r.client.ZAdd(ctx, key, redis.Z{
		Score:  score,
		Member: member,
	}).Err()
}

// ListByScoreDesc returns members from sorted index (highest score first) with pagination.
// Redis implementation uses ZREVRANGEBYSCORE.
func (r *RedisStorageProvider) ListByScoreDesc(ctx context.Context, key string, min, max string, offset, count int64) ([]string, error) {
	// Keep the legacy command for Redis-compatible providers without ZRANGE REV.
	//nolint:staticcheck // ZRevRangeByScore remains supported by go-redis/v9.
	return r.client.ZRevRangeByScore(ctx, key, &redis.ZRangeBy{
		Min:    min,
		Max:    max,
		Offset: offset,
		Count:  count,
	}).Result()
}

// RemoveFromIndex removes members from a sorted index.
// Redis implementation uses ZREM.
func (r *RedisStorageProvider) RemoveFromIndex(ctx context.Context, key string, members ...string) error {
	// Convert strings to interface{} for Redis client
	args := make([]interface{}, len(members))
	for i, m := range members {
		args[i] = m
	}
	return r.client.ZRem(ctx, key, args...).Err()
}

var _ orchestration.StorageProvider = (*RedisStorageProvider)(nil)
var _ orchestration.KeyTTLManager = (*RedisStorageProvider)(nil)
var _ orchestration.ExecutionStorageProvider = (*RedisStorageProvider)(nil)
var _ orchestration.IndexTTLManager = (*RedisStorageProvider)(nil)
