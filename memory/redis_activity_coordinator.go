package memory

import (
	"context"
	"fmt"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface check.
var _ core.ActivityCoordinator = (*RedisActivityCoordinator)(nil)

// RedisActivityCoordinator uses Redis SET with TTL for transient activity signals.
// Key pattern: truvag3:activity:{domain}:{requestID} → signal JSON
//
// AnnounceActivity: SET with TTL
// UpdateStatus: GET + modify + SET (refreshes TTL)
// GetDomainActivities: SCAN truvag3:activity:{domain}:*
// CompleteActivity: DEL
// TTL handles crash cleanup — no orphaned signals.
type RedisActivityCoordinator struct {
	client redis.Cmdable
	domain string
	logger core.Logger
}

// RedisActivityCoordinatorOption configures RedisActivityCoordinator.
type RedisActivityCoordinatorOption func(*RedisActivityCoordinator) error

// WithActivityCoordinatorLogger sets the logger.
func WithActivityCoordinatorLogger(logger core.Logger) RedisActivityCoordinatorOption {
	return func(c *RedisActivityCoordinator) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			c.logger = cal.WithComponent("framework/memory")
		} else {
			c.logger = logger
		}
		return nil
	}
}

// NewRedisActivityCoordinator creates a Redis-backed activity coordinator.
func NewRedisActivityCoordinator(client redis.Cmdable, domain string, opts ...RedisActivityCoordinatorOption) (*RedisActivityCoordinator, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is required for RedisActivityCoordinator")
	}
	c := &RedisActivityCoordinator{
		client: client,
		domain: domain,
		logger: &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, fmt.Errorf("invalid activity coordinator option: %w", err)
		}
	}
	return c, nil
}

func (c *RedisActivityCoordinator) signalKey(requestID string) string {
	return fmt.Sprintf("truvag3:activity:%s:%s", c.domain, requestID)
}

func (c *RedisActivityCoordinator) AnnounceActivity(ctx context.Context, signal core.ActivitySignal) error {
	data, err := MarshalSignal(signal)
	if err != nil {
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Failed to marshal activity signal", map[string]interface{}{
				"operation":  "activity_announce",
				"request_id": signal.RequestID,
				"error":      err.Error(),
				"error_type": "marshal_failure",
			})
		}
		return fmt.Errorf("failed to marshal activity signal: %w", err)
	}
	if setErr := c.client.Set(ctx, c.signalKey(signal.RequestID), data, signal.TTL).Err(); setErr != nil {
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Failed to write activity signal to Redis", map[string]interface{}{
				"operation":  "activity_announce",
				"request_id": signal.RequestID,
				"error":      setErr.Error(),
				"error_type": "redis_write",
			})
		}
		return setErr
	}
	return nil
}

func (c *RedisActivityCoordinator) UpdateStatus(ctx context.Context, requestID, status string) error {
	key := c.signalKey(requestID)

	// GET existing signal
	data, err := c.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil // Signal expired or was completed — no-op
	}
	if err != nil {
		return fmt.Errorf("failed to get activity signal for status update: %w", err)
	}

	// Modify status
	signal, err := UnmarshalSignal(data)
	if err != nil {
		return fmt.Errorf("failed to unmarshal activity signal: %w", err)
	}
	signal.Status = status

	// Re-SET with remaining TTL
	ttl, err := c.client.TTL(ctx, key).Result()
	if err != nil || ttl <= 0 {
		return nil // Signal about to expire — don't refresh
	}

	updated, err := MarshalSignal(signal)
	if err != nil {
		return fmt.Errorf("failed to marshal updated signal: %w", err)
	}
	return c.client.Set(ctx, key, updated, ttl).Err()
}

func (c *RedisActivityCoordinator) GetDomainActivities(ctx context.Context, domain string) ([]core.ActivitySignal, error) {
	pattern := fmt.Sprintf("truvag3:activity:%s:*", domain)

	var signals []core.ActivitySignal
	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to scan activity signals: %w", err)
		}

		for _, key := range keys {
			data, err := c.client.Get(ctx, key).Bytes()
			if err == redis.Nil {
				continue // Expired between SCAN and GET
			}
			if err != nil {
				continue // Skip errors, fail-open
			}
			signal, err := UnmarshalSignal(data)
			if err != nil {
				continue // Malformed — skip
			}
			signals = append(signals, signal)
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return signals, nil
}

func (c *RedisActivityCoordinator) CompleteActivity(ctx context.Context, requestID string) error {
	return c.client.Del(ctx, c.signalKey(requestID)).Err()
}
