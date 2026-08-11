package redisprovider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

const defaultLockKeyPrefix = "truvag3:lock"

var releaseOwnedLock = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// redisDistributedLock implements the framework's efficiency-lock contract.
// Each instance keeps an opaque token per acquired key so a delayed Release
// cannot delete a lease that expired and was acquired by another instance.
type redisDistributedLock struct {
	client    redis.Cmdable
	keyPrefix string
	logger    core.Logger

	mu     sync.Mutex
	tokens map[string]string
}

func newRedisDistributedLock(client redis.Cmdable, keyPrefix string, logger core.Logger) (*redisDistributedLock, error) {
	if client == nil {
		return nil, fmt.Errorf("redisprovider: distributed lock client is required")
	}
	if keyPrefix == "" {
		keyPrefix = defaultLockKeyPrefix
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	return &redisDistributedLock{
		client: client, keyPrefix: keyPrefix, logger: logger, tokens: make(map[string]string),
	}, nil
}

func (lock *redisDistributedLock) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, fmt.Errorf("redisprovider: distributed lock TTL must be positive")
	}
	token, err := newLockToken()
	if err != nil {
		return false, fmt.Errorf("redisprovider: create distributed lock token: %w", err)
	}
	acquired, err := lock.client.SetNX(ctx, lock.redisKey(key), token, ttl).Result()
	if err != nil {
		lock.logFailure(ctx, "lock_acquire", err)
		return false, err
	}
	if acquired {
		lock.mu.Lock()
		lock.tokens[key] = token
		lock.mu.Unlock()
	}
	return acquired, nil
}

func (lock *redisDistributedLock) Release(ctx context.Context, key string) error {
	lock.mu.Lock()
	token, owned := lock.tokens[key]
	lock.mu.Unlock()
	if !owned {
		return nil
	}

	if _, err := releaseOwnedLock.Run(ctx, lock.client, []string{lock.redisKey(key)}, token).Result(); err != nil {
		lock.logFailure(ctx, "lock_release", err)
		return err
	}
	lock.mu.Lock()
	if lock.tokens[key] == token {
		delete(lock.tokens, key)
	}
	lock.mu.Unlock()
	return nil
}

func (lock *redisDistributedLock) redisKey(key string) string {
	return lock.keyPrefix + ":" + key
}

func (lock *redisDistributedLock) logFailure(ctx context.Context, errorType string, err error) {
	if lock.logger == nil {
		return
	}
	lock.logger.WarnWithContext(ctx, "Distributed lock operation failed", map[string]interface{}{
		"operation":  "distributed_lock",
		"error_type": errorType,
		"error":      core.RedactSensitiveText(err.Error()),
	})
}

func newLockToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

var _ core.DistributedLock = (*redisDistributedLock)(nil)
