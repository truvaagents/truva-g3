// Package redisadapter contains the Redis coordination adapter owned by the
// orchestration backend portability example.
package redisadapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

const lockKeyPrefix = "truvag3:lock"

var releaseOwnedLock = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// DistributedLock implements the framework efficiency-lock contract with
// owner-safe release. A delayed holder cannot delete a lease that expired and
// was subsequently acquired by another process.
type DistributedLock struct {
	client    redis.Cmdable
	namespace string

	mu     sync.Mutex
	tokens map[string]string
}

func NewDistributedLock(client redis.Cmdable, namespace string) (*DistributedLock, error) {
	if client == nil {
		return nil, fmt.Errorf("redis adapter: distributed lock client is required")
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil, fmt.Errorf("redis adapter: distributed lock namespace is required")
	}
	return &DistributedLock{
		client: client, namespace: namespace, tokens: make(map[string]string),
	}, nil
}

func (lock *DistributedLock) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	key, err := validateLockKey(key)
	if err != nil {
		return false, err
	}
	if ttl <= 0 {
		return false, fmt.Errorf("redis adapter: distributed lock TTL must be positive")
	}
	token, err := newLockToken()
	if err != nil {
		return false, fmt.Errorf("redis adapter: create distributed lock token: %w", err)
	}
	acquired, err := lock.client.SetNX(ctx, lock.redisKey(key), token, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("redis adapter: acquire distributed lock: %w", err)
	}
	if acquired {
		lock.mu.Lock()
		lock.tokens[key] = token
		lock.mu.Unlock()
	}
	return acquired, nil
}

func (lock *DistributedLock) Release(ctx context.Context, key string) error {
	key, err := validateLockKey(key)
	if err != nil {
		return err
	}

	lock.mu.Lock()
	token, owned := lock.tokens[key]
	lock.mu.Unlock()
	if !owned {
		return nil
	}

	if _, err := releaseOwnedLock.Run(ctx, lock.client, []string{lock.redisKey(key)}, token).Result(); err != nil {
		return fmt.Errorf("redis adapter: release distributed lock: %w", err)
	}
	lock.mu.Lock()
	if lock.tokens[key] == token {
		delete(lock.tokens, key)
	}
	lock.mu.Unlock()
	return nil
}

func (lock *DistributedLock) redisKey(key string) string {
	return lockKeyPrefix + ":" + lock.namespace + ":" + key
}

func validateLockKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("redis adapter: distributed lock key is required")
	}
	return key, nil
}

func newLockToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

var _ core.DistributedLock = (*DistributedLock)(nil)
