// Package orchestration — Redis Streams core.TaskConsumer (alternative, at-least-once).
//
// Uses XREADGROUP + XACK for at-least-once delivery. The task stays in the
// stream's pending-entries list until explicitly settled via Ack (XACK) or
// Nack (LPUSH to DLQ + XACK in a pipeline).
//
// Requires a companion RedisStreamsReaper Runnable to reclaim stuck pending
// entries from crashed replicas. Without the reaper, a crashed executor
// holds its claimed tasks forever.

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
)

const (
	taskStreamKeyPrefix = "truvag3:tasks:stream:"
	defaultXreadBlock   = 1 * time.Second
)

var _ core.TaskConsumer = (*RedisStreamsTaskConsumer)(nil)
var _ core.TaskHandle = (*redisStreamsHandle)(nil)

// RedisStreamsTaskConsumer is a core.TaskConsumer backed by Redis Streams
// XREADGROUP. Delivery semantics: at-least-once.
type RedisStreamsTaskConsumer struct {
	client       redis.Cmdable
	queueName    string
	groupName    string
	consumerName string
}

// NewRedisStreamsTaskConsumer creates a Streams-based consumer. It calls
// XGROUP CREATE with MKSTREAM on construction so the stream and group
// exist before the first Consume call.
func NewRedisStreamsTaskConsumer(client redis.Cmdable, queueName, groupName string) (*RedisStreamsTaskConsumer, error) {
	if client == nil {
		return nil, errNilRedisClient
	}
	if queueName == "" {
		return nil, fmt.Errorf("orchestration: NewRedisStreamsTaskConsumer queueName is required")
	}
	if groupName == "" {
		return nil, fmt.Errorf("orchestration: NewRedisStreamsTaskConsumer groupName is required")
	}

	streamKey := taskStreamKeyPrefix + queueName
	// Create the consumer group. The "$" ID means "only new messages."
	// MKSTREAM creates the stream if it doesn't exist.
	err := client.XGroupCreateMkStream(context.Background(), streamKey, groupName, "$").Err()
	if err != nil && !isConsumerGroupExistsErr(err) {
		return nil, fmt.Errorf("orchestration: XGROUP CREATE %s %s: %w", streamKey, groupName, err)
	}

	return &RedisStreamsTaskConsumer{
		client:       client,
		queueName:    queueName,
		groupName:    groupName,
		consumerName: consumerName(),
	}, nil
}

// Consume implements core.TaskConsumer.
func (c *RedisStreamsTaskConsumer) Consume(ctx context.Context, queueName string) (core.TaskHandle, error) {
	streamKey := taskStreamKeyPrefix + queueName

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	res, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    c.groupName,
		Consumer: c.consumerName,
		Streams:  []string{streamKey, ">"},
		Count:    1,
		Block:    defaultXreadBlock,
	}).Result()

	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("orchestration: XREADGROUP %s: %w", streamKey, err)
	}
	if len(res) == 0 || len(res[0].Messages) == 0 {
		return nil, nil
	}

	msg := res[0].Messages[0]
	payload, ok := msg.Values["task"].(string)
	if !ok {
		return nil, fmt.Errorf("orchestration: stream message missing 'task' field")
	}
	var task core.Task
	if err := json.Unmarshal([]byte(payload), &task); err != nil {
		return nil, fmt.Errorf("orchestration: unmarshal task: %w", err)
	}

	return &redisStreamsHandle{
		client:    c.client,
		streamKey: streamKey,
		dlqKey:    dlqKeyPrefix + queueName,
		group:     c.groupName,
		messageID: msg.ID,
		task:      &task,
	}, nil
}

// redisStreamsHandle is the TaskHandle for at-least-once delivery.
// Ack calls XACK. Nack pipelines LPUSH (DLQ) + XACK.
type redisStreamsHandle struct {
	client    redis.Cmdable
	streamKey string
	dlqKey    string
	group     string
	messageID string
	task      *core.Task
	mu        sync.Mutex
	settled   bool
}

func (h *redisStreamsHandle) Task() *core.Task { return h.task }

func (h *redisStreamsHandle) Ack(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled {
		return fmt.Errorf("orchestration: handle already settled")
	}
	h.settled = true

	return h.client.XAck(ctx, h.streamKey, h.group, h.messageID).Err()
}

func (h *redisStreamsHandle) Nack(ctx context.Context, reason string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled {
		return fmt.Errorf("orchestration: handle already settled")
	}
	h.settled = true

	entry := deadLetterEntry{
		Task:     h.task,
		Reason:   reason,
		FailedAt: time.Now(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("orchestration: marshal dead-letter: %w", err)
	}

	// Pipeline: LPUSH to DLQ + XACK in one round-trip.
	pipe := h.client.Pipeline()
	pipe.LPush(ctx, h.dlqKey, payload)
	pipe.XAck(ctx, h.streamKey, h.group, h.messageID)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("orchestration: Nack pipeline: %w", err)
	}
	return nil
}

// RedisStreamsTaskDispatcher dispatches tasks via XADD to a Redis Stream.
// Wire-compatible with RedisStreamsTaskConsumer.
type RedisStreamsTaskDispatcher struct {
	client redis.Cmdable
}

func NewRedisStreamsTaskDispatcher(client redis.Cmdable, queueName string) (*RedisStreamsTaskDispatcher, error) {
	if client == nil {
		return nil, errNilRedisClient
	}
	return &RedisStreamsTaskDispatcher{client: client}, nil
}

var _ core.TaskDispatcher = (*RedisStreamsTaskDispatcher)(nil)

func (d *RedisStreamsTaskDispatcher) Dispatch(ctx context.Context, queueName string, task *core.Task) error {
	if task == nil {
		return errNilTask
	}
	if task.ID == "" {
		return errEmptyTaskID
	}

	// Idempotency check via a Redis SET with NX.
	idKey := "truvag3:tasks:id:" + task.ID
	ok, err := d.client.SetNX(ctx, idKey, "1", 24*time.Hour).Result()
	if err != nil {
		return fmt.Errorf("orchestration: idempotency check: %w", err)
	}
	if !ok {
		return core.ErrTaskAlreadyExists
	}

	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("orchestration: marshal task: %w", err)
	}

	streamKey := taskStreamKeyPrefix + queueName
	return d.client.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{"task": string(payload)},
	}).Err()
}

// consumerName returns a unique name for this consumer instance.
// In K8s, os.Hostname() returns the pod name (unique per replica).
func consumerName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "executor-" + uuid.New().String()[:12]
}

// isConsumerGroupExistsErr checks if the error is "BUSYGROUP Consumer Group name already exists".
func isConsumerGroupExistsErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}
