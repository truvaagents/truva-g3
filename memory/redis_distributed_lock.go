package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// Compile-time interface check.
var _ core.DistributedLock = (*RedisDistributedLock)(nil)

// RedisDistributedLock provides mutual exclusion via Redis SETNX with TTL.
// Key pattern: truvag3:lock:{key}
type RedisDistributedLock struct {
	client redis.Cmdable
	logger core.Logger
}

// NewRedisDistributedLock creates a Redis-backed distributed lock.
func NewRedisDistributedLock(client redis.Cmdable, logger core.Logger) (*RedisDistributedLock, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is required for RedisDistributedLock")
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/memory")
	}
	return &RedisDistributedLock{client: client, logger: logger}, nil
}

func lockKey(key string) string {
	return fmt.Sprintf("truvag3:lock:%s", key)
}

func (l *RedisDistributedLock) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ok, err := l.client.SetNX(ctx, lockKey(key), "locked", ttl).Result()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if l.logger != nil {
			l.logger.WarnWithContext(ctx, "Failed to acquire distributed lock", map[string]interface{}{
				"operation":  "distributed_lock",
				"key":        key,
				"error":      err.Error(),
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
	err := l.client.Del(ctx, lockKey(key)).Err()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if l.logger != nil {
			l.logger.WarnWithContext(ctx, "Failed to release distributed lock", map[string]interface{}{
				"operation":  "distributed_lock",
				"key":        key,
				"error":      err.Error(),
				"error_type": "lock_release",
			})
		}
	}
	return err
}
