package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface check.
var _ core.ActivityCoordinator = (*RedisActivityCoordinator)(nil)

// RedisActivityCoordinator uses Redis SET with TTL for transient activity signals.
// Signals and the domain index share one cluster hash slot.
//
// AnnounceActivity: transactional SET with TTL + index membership
// UpdateStatus: GET + modify + SET (refreshes TTL)
// GetDomainActivities: SSCAN the domain index and hydrate candidates
// CompleteActivity: transactional DEL + index removal
// TTL handles crash cleanup — no orphaned signals.
type RedisActivityCoordinator struct {
	client   redis.Cmdable
	domain   string
	keyspace core.RedisKeyspace
	logger   core.Logger
}

// WithActivityCoordinatorKeyspace sets the deployment-scoped Redis keyspace.
func WithActivityCoordinatorKeyspace(keyspace core.RedisKeyspace) RedisActivityCoordinatorOption {
	return func(c *RedisActivityCoordinator) error {
		c.keyspace = keyspace
		return nil
	}
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
		client:   client,
		domain:   domain,
		keyspace: defaultRedisKeyspace(),
		logger:   &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, fmt.Errorf("invalid activity coordinator option: %w", err)
		}
	}
	return c, nil
}

func (c *RedisActivityCoordinator) signalKey(requestID string) string {
	return c.signalKeyForDomain(c.domain, requestID)
}

func (c *RedisActivityCoordinator) signalKeyForDomain(domain, requestID string) string {
	return c.keyspace.Tagged("activity", domain, "signal", requestID)
}

func (c *RedisActivityCoordinator) signalIndexKey(domain string) string {
	return c.keyspace.Tagged("activity", domain, "signals")
}

func (c *RedisActivityCoordinator) AnnounceActivity(ctx context.Context, signal core.ActivitySignal) error {
	data, err := MarshalSignal(signal)
	if err != nil {
		c.observeRuntimeFailure(ctx, "activity_announce_marshal", signal.RequestID, err)
		return nil
	}
	_, setErr := c.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, c.signalKey(signal.RequestID), data, signal.TTL)
		pipe.SAdd(ctx, c.signalIndexKey(c.domain), signal.RequestID)
		return nil
	})
	if setErr != nil {
		c.observeRuntimeFailure(ctx, "activity_announce", signal.RequestID, setErr)
		return nil
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
		c.observeRuntimeFailure(ctx, "activity_status_read", requestID, err)
		return nil
	}

	// Modify status
	signal, err := UnmarshalSignal(data)
	if err != nil {
		c.observeRuntimeFailure(ctx, "activity_status_unmarshal", requestID, err)
		return nil
	}
	signal.Status = status

	// Re-SET with remaining TTL
	ttl, err := c.client.TTL(ctx, key).Result()
	if err != nil {
		c.observeRuntimeFailure(ctx, "activity_status_ttl", requestID, err)
		return nil
	}
	if ttl <= 0 {
		return nil // Signal about to expire — don't refresh
	}

	updated, err := MarshalSignal(signal)
	if err != nil {
		c.observeRuntimeFailure(ctx, "activity_status_marshal", requestID, err)
		return nil
	}
	if err := c.client.Set(ctx, key, updated, ttl).Err(); err != nil {
		c.observeRuntimeFailure(ctx, "activity_status_write", requestID, err)
	}
	return nil
}

func (c *RedisActivityCoordinator) GetDomainActivities(ctx context.Context, domain string) ([]core.ActivitySignal, error) {
	ids, err := c.scanSetMembers(ctx, c.signalIndexKey(domain), 256)
	if err != nil {
		c.observeRuntimeFailure(ctx, "activity_index_scan", "", err)
		return nil, nil
	}
	pipe := c.client.Pipeline()
	commands := make([]*redis.StringCmd, len(ids))
	for i, id := range ids {
		commands[i] = pipe.Get(ctx, c.signalKeyForDomain(domain, id))
	}
	_, execErr := pipe.Exec(ctx)
	if execErr != nil && execErr != redis.Nil {
		c.observeRuntimeFailure(ctx, "activity_signal_load", "", execErr)
		return nil, nil
	}
	signals := make([]core.ActivitySignal, 0, len(ids))
	stale := make([]interface{}, 0)
	for i, command := range commands {
		data, err := command.Bytes()
		if err == redis.Nil {
			stale = append(stale, ids[i])
			continue
		}
		if err != nil {
			c.observeRuntimeFailure(ctx, "activity_signal_load", ids[i], err)
			return nil, nil
		}
		signal, err := UnmarshalSignal(data)
		if err != nil {
			c.observeRuntimeFailure(ctx, "activity_signal_unmarshal", ids[i], err)
			continue
		}
		signals = append(signals, signal)
	}
	if len(stale) > 0 {
		if err := c.client.SRem(ctx, c.signalIndexKey(domain), stale...).Err(); err != nil {
			c.observeRuntimeFailure(ctx, "activity_index_cleanup", "", err)
		}
	}
	return signals, nil
}

func (c *RedisActivityCoordinator) scanSetMembers(ctx context.Context, key string, countHint int64) ([]string, error) {
	seen := make(map[string]struct{})
	var cursor uint64
	for {
		ids, nextCursor, err := c.client.SScan(ctx, key, cursor, "", countHint).Result()
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			seen[id] = struct{}{}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}

func (c *RedisActivityCoordinator) CompleteActivity(ctx context.Context, requestID string) error {
	_, err := c.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, c.signalKey(requestID))
		pipe.SRem(ctx, c.signalIndexKey(c.domain), requestID)
		return nil
	})
	if err != nil {
		c.observeRuntimeFailure(ctx, "activity_complete", requestID, err)
	}
	return nil
}

func (c *RedisActivityCoordinator) observeRuntimeFailure(ctx context.Context, operation, requestID string, _ error) {
	if c.logger == nil {
		return
	}
	errorType := "backend"
	if strings.HasSuffix(operation, "_marshal") || strings.HasSuffix(operation, "_unmarshal") {
		errorType = "serialization"
	}
	c.logger.WarnWithContext(ctx, "Redis activity coordination failed open", map[string]interface{}{
		"operation":  operation,
		"request_id": requestID,
		"error":      "redis activity coordination unavailable",
		"error_type": errorType,
	})
}
