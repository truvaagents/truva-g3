package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

// AlertQueueConsumer reads alerts from the raw alert queue (truvag3:event:alert_queue)
// and submits them as tasks to the RedisTaskQueue for worker pool processing.
//
// This bridges the deterministic pipeline (webhook → dedup → LPUSH) with the
// async task system (BRPOP → HandleAlertInvestigation).
type AlertQueueConsumer struct {
	redisClient *redis.Client
	taskQueue   *orchestration.RedisTaskQueue
	logger      core.Logger
	queueKey    string
}

// NewAlertQueueConsumer creates a new consumer that bridges alert queue → task queue.
func NewAlertQueueConsumer(
	redisClient *redis.Client,
	taskQueue *orchestration.RedisTaskQueue,
	logger core.Logger,
) *AlertQueueConsumer {
	return &AlertQueueConsumer{
		redisClient: redisClient,
		taskQueue:   taskQueue,
		logger:      logger,
		queueKey:    "truvag3:event:alert_queue",
	}
}

// Start runs the consumer loop. It blocks until ctx is cancelled.
// Uses BRPOP with a timeout to allow graceful shutdown checks.
func (c *AlertQueueConsumer) Start(ctx context.Context) error {
	c.logger.Info("Alert queue consumer started", map[string]interface{}{
		"queue_key": c.queueKey,
		"operation": "queue_consumer",
	})

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Alert queue consumer stopping", map[string]interface{}{
				"operation": "queue_consumer",
			})
			return ctx.Err()
		default:
		}

		// BRPOP with 5s timeout (allows periodic ctx.Done() checks)
		result, err := c.redisClient.BRPop(ctx, 5*time.Second, c.queueKey).Result()
		if err != nil {
			if err == redis.Nil {
				continue // Timeout, no items — loop back
			}
			if ctx.Err() != nil {
				return ctx.Err() // Context cancelled during BRPOP
			}
			c.logger.Error("BRPOP error", map[string]interface{}{
				"queue_key": c.queueKey,
				"error":     err.Error(),
				"operation": "queue_consumer",
			})
			time.Sleep(1 * time.Second) // Back off on Redis errors
			continue
		}

		// result[0] = key, result[1] = value
		if len(result) < 2 {
			continue
		}

		envelopeJSON := result[1]

		// Deserialize envelope carrying alert + trace context (RC5).
		// BACKWARD COMPAT: A legacy raw Alert JSON unmarshal into alertEnvelope succeeds
		// (Go ignores unknown fields) but leaves AlertJSON == "". Check both conditions.
		var envelope alertEnvelope
		if err := json.Unmarshal([]byte(envelopeJSON), &envelope); err != nil || envelope.AlertJSON == "" {
			// Legacy format — treat the whole payload as the raw alert JSON.
			envelope = alertEnvelope{AlertJSON: envelopeJSON}
		}

		// Parse the alert to extract metadata for the task
		var alert Alert
		if err := json.Unmarshal([]byte(envelope.AlertJSON), &alert); err != nil {
			c.logger.Error("Failed to parse alert from queue", map[string]interface{}{
				"error":     err.Error(),
				"operation": "queue_consumer",
			})
			continue
		}

		// Create a task for the worker pool
		task := &core.Task{
			ID:     fmt.Sprintf("alert-%s-%d", alert.Fingerprint, time.Now().UnixMilli()),
			Type:   "alert_investigation",
			Status: core.TaskStatusQueued,
			Input: map[string]interface{}{
				"alert_json": envelope.AlertJSON,
			},
			CreatedAt: time.Now(),
		}
		// Restore originating HTTP trace context for StartLinkedSpan in the worker handler.
		// SetTraceContext is a no-op when both strings are empty (legacy or untraced).
		task.SetTraceContext(envelope.TraceID, envelope.SpanID)

		// Submit to the task queue
		if err := c.taskQueue.Enqueue(ctx, task); err != nil {
			c.logger.Error("Failed to enqueue task", map[string]interface{}{
				"task_id":     task.ID,
				"alertname":   alert.Labels["alertname"],
				"fingerprint": alert.Fingerprint,
				"error":       err.Error(),
				"operation":   "queue_consumer",
			})
			continue
		}

		c.logger.Info("Alert submitted as task", map[string]interface{}{
			"task_id":     task.ID,
			"alertname":   alert.Labels["alertname"],
			"severity":    alert.Labels["severity"],
			"fingerprint": alert.Fingerprint,
			"operation":   "queue_consumer",
		})
	}
}
