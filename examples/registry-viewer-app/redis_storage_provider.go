package main

import (
	"context"
	"time"

	"github.com/go-redis/redis/v8"
)

// RedisStorageProvider implements the StorageProvider interface using Redis.
// This is an application-level implementation that the orchestration module
// accepts through dependency injection.
type RedisStorageProvider struct {
	client *redis.Client
}

// NewRedisStorageProvider creates a new Redis-backed StorageProvider.
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
	return r.client.ZAdd(ctx, key, &redis.Z{
		Score:  score,
		Member: member,
	}).Err()
}

// ListByScoreDesc returns members from sorted index (highest score first) with pagination.
// Redis implementation uses ZREVRANGEBYSCORE.
func (r *RedisStorageProvider) ListByScoreDesc(ctx context.Context, key string, min, max string, offset, count int64) ([]string, error) {
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
