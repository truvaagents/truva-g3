package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface check.
var _ core.DistributedLock = (*RedisDistributedLock)(nil)

// RedisDistributedLock provides mutual exclusion via Redis SETNX with TTL.
// Keys use the deployment-scoped versioned locks keyspace and a per-lock hash
// tag so acquire and release remain single-slot in cluster mode.
type RedisDistributedLock struct {
	client   redis.Cmdable
	keyspace core.RedisKeyspace
	logger   core.Logger
}

// RedisDistributedLockOption configures RedisDistributedLock.
type RedisDistributedLockOption func(*RedisDistributedLock) error

// WithDistributedLockKeyspace sets the deployment-scoped Redis keyspace.
func WithDistributedLockKeyspace(keyspace core.RedisKeyspace) RedisDistributedLockOption {
	return func(lock *RedisDistributedLock) error {
		lock.keyspace = keyspace
		return nil
	}
}

// NewRedisDistributedLock creates a Redis-backed distributed lock.
func NewRedisDistributedLock(client redis.Cmdable, logger core.Logger, opts ...RedisDistributedLockOption) (*RedisDistributedLock, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is required for RedisDistributedLock")
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/memory")
	}
	lock := &RedisDistributedLock{client: client, keyspace: defaultRedisKeyspace(), logger: logger}
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("redis distributed lock option cannot be nil")
		}
		if err := opt(lock); err != nil {
			return nil, fmt.Errorf("invalid Redis distributed lock option: %w", err)
		}
	}
	return lock, nil
}

func (l *RedisDistributedLock) lockKey(key string) string {
	return l.keyspace.Tagged("locks", key, "lease")
}

func (l *RedisDistributedLock) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ok, err := l.client.SetNX(ctx, l.lockKey(key), "locked", ttl).Result()
	if err != nil {
		if l.logger != nil {
			l.logger.WarnWithContext(ctx, "Failed to acquire distributed lock", map[string]interface{}{
				"operation":  "distributed_lock",
				"request_id": core.GetRequestID(ctx),
				"key":        key,
				"error":      "redis distributed lock acquisition failed",
				"error_type": "lock_acquire",
			})
		}
		return false, err
	}
	return ok, nil
}

// Release deletes the lock key unconditionally.
// Note: This does not check ownership — if the lock expired and was re-acquired
// by another holder, this Release deletes their lock. This is acceptable for
// efficiency locks (duplicate work, not corruption). For correctness locks,
// use a Lua script that checks the value before deleting.
func (l *RedisDistributedLock) Release(ctx context.Context, key string) error {
	err := l.client.Del(ctx, l.lockKey(key)).Err()
	if err != nil {
		if l.logger != nil {
			l.logger.WarnWithContext(ctx, "Failed to release distributed lock", map[string]interface{}{
				"operation":  "distributed_lock",
				"request_id": core.GetRequestID(ctx),
				"key":        key,
				"error":      "redis distributed lock release failed",
				"error_type": "lock_release",
			})
		}
	}
	return err
}
