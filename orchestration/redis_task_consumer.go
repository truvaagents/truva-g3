// Package orchestration — Redis BRPOP-based core.TaskConsumer (default, at-most-once).
//
// This is the default consumer-side reference implementation. It uses BRPOP
// against the same truvag3:tasks:queue:{name} list pattern that RedisTaskQueue
// and RedisTaskDispatcher already use, so a BRPOP-based consumer is wire-
// compatible with the existing producer. Delivery semantics are at-most-once:
// the task is removed from the list when BRPOP returns, and there is no
// explicit acknowledgment step — if the executor crashes mid-dispatch, the
// task is lost. See §9 of the design doc for the loss window and mitigations.
//
// TaskHandle implementation: the returned handle (redisAtMostOnceHandle) has
// no-op Ack. Nack persists a dead-letter entry via LPUSH to
// truvag3:tasks:dead:{queueName}.
//
// For at-least-once semantics, use RedisStreamsTaskConsumer via
// NewRedisStreamsSchedulerBackends instead.

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

const (
	// dlqKeyPrefix is the Redis list key for dead-lettered tasks.
	// Shared with RedisStreamsTaskConsumer so operators see the same DLQ
	// key pattern regardless of which backend they use.
	dlqKeyPrefix = "truvag3:tasks:dead:"

	// defaultBrpopTimeout is the BRPOP block duration. Short enough that
	// ctx cancellation is observed within ~1s of the cancel; long enough
	// to avoid tight-looping when the queue is empty.
	defaultBrpopTimeout = 1 * time.Second
)

// Compile-time checks.
var _ core.TaskConsumer = (*RedisTaskConsumer)(nil)
var _ core.TaskHandle = (*redisAtMostOnceHandle)(nil)

// RedisTaskConsumer is a core.TaskConsumer backed by Redis BRPOP.
// Delivery semantics: at-most-once.
type RedisTaskConsumer struct {
	client      redis.Cmdable
	queueName   string // captured for DLQ key derivation in handles
	queuePrefix string
	dlqPrefix   string
}

// NewRedisTaskConsumer returns a consumer that BRPOPs from the standard
// truvag3:tasks:queue:{name} list pattern. The queueName is captured here
// so handles returned from Consume know where to write dead-letters.
func NewRedisTaskConsumer(client redis.Cmdable, queueName string) (*RedisTaskConsumer, error) {
	return NewRedisTaskConsumerWithPrefix(client, queueName, "truvag3:tasks")
}

func NewRedisTaskConsumerWithPrefix(client redis.Cmdable, queueName, prefix string) (*RedisTaskConsumer, error) {
	if client == nil {
		return nil, errNilRedisClient
	}
	if queueName == "" {
		return nil, fmt.Errorf("orchestration: NewRedisTaskConsumer queueName is required")
	}
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), ":")
	if prefix == "" {
		return nil, fmt.Errorf("orchestration: task consumer prefix is required")
	}
	return &RedisTaskConsumer{
		client: client, queueName: queueName,
		queuePrefix: prefix + ":queue:", dlqPrefix: prefix + ":dead:",
	}, nil
}

// Consume implements core.TaskConsumer.
func (c *RedisTaskConsumer) Consume(ctx context.Context, queueName string) (core.TaskHandle, error) {
	if queueName == "" {
		return nil, fmt.Errorf("orchestration: Consume queueName is required")
	}
	key := c.queuePrefix + queueName

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	result, err := c.client.BRPop(ctx, defaultBrpopTimeout, key).Result()
	if errors.Is(err, redis.Nil) {
		// BRPOP timeout — no element. Caller re-loops via consumeLoop.
		return nil, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("orchestration: BRPOP %s: %w", key, err)
	}
	if len(result) != 2 {
		return nil, fmt.Errorf("orchestration: unexpected BRPOP result length %d", len(result))
	}

	var task core.Task
	if err := json.Unmarshal([]byte(result[1]), &task); err != nil {
		return nil, fmt.Errorf("orchestration: unmarshal task: %w", err)
	}
	return &redisAtMostOnceHandle{
		client: c.client,
		task:   &task,
		dlqKey: c.dlqPrefix + queueName,
	}, nil
}

// redisAtMostOnceHandle is the TaskHandle returned by RedisTaskConsumer.
// The task has already left the queue when the handle is created, so Ack
// is a no-op. Nack persists a dead-letter entry to the DLQ list.
type redisAtMostOnceHandle struct {
	client  redis.Cmdable
	task    *core.Task
	dlqKey  string
	mu      sync.Mutex
	settled bool
}

func (h *redisAtMostOnceHandle) Task() *core.Task { return h.task }

// Ack is a no-op for at-most-once backends — the task is already off the queue.
func (h *redisAtMostOnceHandle) Ack(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled {
		return fmt.Errorf("orchestration: handle already settled")
	}
	h.settled = true
	return nil
}

// Nack persists the task to the DLQ list with the given reason.
func (h *redisAtMostOnceHandle) Nack(ctx context.Context, reason string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled {
		return fmt.Errorf("orchestration: handle already settled")
	}
	h.settled = true

	if h.task == nil {
		return errNilTask
	}
	entry := deadLetterEntry{
		Task:     h.task,
		Reason:   reason,
		FailedAt: time.Now(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("orchestration: marshal dead-letter: %w", err)
	}
	if err := h.client.LPush(ctx, h.dlqKey, payload).Err(); err != nil {
		return fmt.Errorf("orchestration: LPUSH %s: %w", h.dlqKey, err)
	}
	return nil
}

// deadLetterEntry is the wire format written to the DLQ list. Shared with
// RedisStreamsTaskConsumer's DLQ path so operators see the same JSON shape
// regardless of which backend they use.
type deadLetterEntry struct {
	Task     *core.Task `json:"task"`
	Reason   string     `json:"reason"`
	FailedAt time.Time  `json:"failed_at"`
}
