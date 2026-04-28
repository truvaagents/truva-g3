// Package main — vendor-neutral worker logic for the scheduled-executor.
//
// This file has ZERO vendor SDK imports. It depends on core interfaces
// (TaskConsumer, TaskHandle) and the locally-defined AgentResolver
// interface. The only file that imports go-redis is main.go.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultWorkerCount     = 5
	defaultMaxRetries      = 3
	defaultRetryBaseDelay  = 5 * time.Second
	defaultRetryMaxDelay   = 60 * time.Second
	defaultDispatchTimeout = 15 * time.Minute

	// Discovery retry uses a shorter backoff than HTTP dispatch because
	// Redis gaps are brief (~seconds) and agents re-register quickly.
	discoveryMaxRetries   = 3
	discoveryBaseDelay    = 1 * time.Second
	discoveryMaxDelay     = 4 * time.Second
	defaultQueueName       = "scheduled-executor"
	scheduledEndpointPath  = "/api/v1/scheduled"
)

// Environment variable names for executor numeric tuning.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md Configuration Split: numeric tuning
// uses environment variables, behavioural plugs use WithXXX option functions.
const (
	envExecutorWorkerCount     = "TRUVAG3_EXECUTOR_WORKER_COUNT"
	envExecutorMaxRetries      = "TRUVAG3_EXECUTOR_MAX_RETRIES"
	envExecutorRetryBaseDelay  = "TRUVAG3_EXECUTOR_RETRY_BASE_DELAY"
	envExecutorRetryMaxDelay   = "TRUVAG3_EXECUTOR_RETRY_MAX_DELAY"
	envExecutorDispatchTimeout = "TRUVAG3_EXECUTOR_DISPATCH_TIMEOUT"
)

// AgentResolver is the worker's view of the agent catalog. It captures
// exactly the two operations the dispatch path needs: look up an agent by
// name, and force a refresh when a cache miss occurs.
//
// *orchestration.AgentCatalog satisfies this interface via Go's structural
// typing (FindByName returns *core.ServiceInfo, Refresh takes ctx).
// Tests inject a trivial fake.
type AgentResolver interface {
	FindByName(name string) *core.ServiceInfo
	Refresh(ctx context.Context) error
}

// ExecutorDeps holds the dependencies for the Worker.
type ExecutorDeps struct {
	// Required — how to drain tasks from the transport.
	Consumer core.TaskConsumer

	// Required — must be built via telemetry.NewTracedHTTPClient for
	// trace propagation.
	HTTPClient *http.Client

	// Required — resolves target agents by name. In production, pass
	// *orchestration.AgentCatalog. In tests, pass a fake.
	Catalog AgentResolver

	// Optional — defaults to &core.NoOpLogger{}.
	Logger core.Logger

	// Optional tuning knobs.
	QueueName       string
	WorkerCount     int
	MaxRetries      int
	RetryBaseDelay  time.Duration
	RetryMaxDelay   time.Duration
	DispatchTimeout time.Duration
}

// Worker implements core.Runnable. It consumes tasks from the queue and
// dispatches them as HTTP POSTs to target agents.
type Worker struct {
	deps ExecutorDeps
}

var _ core.Runnable = (*Worker)(nil)

// NewWorker validates required deps and applies defaults.
func NewWorker(deps ExecutorDeps) (*Worker, error) {
	if deps.Consumer == nil {
		return nil, errors.New("executor: Consumer is required")
	}
	if deps.HTTPClient == nil {
		return nil, errors.New("executor: HTTPClient is required (use telemetry.NewTracedHTTPClient)")
	}
	if deps.Catalog == nil {
		return nil, errors.New("executor: Catalog is required (pass *orchestration.AgentCatalog or a test fake)")
	}
	if deps.Logger == nil {
		deps.Logger = &core.NoOpLogger{}
	}
	if deps.QueueName == "" {
		deps.QueueName = defaultQueueName
	}
	if deps.WorkerCount <= 0 {
		deps.WorkerCount = resolveIntEnv(envExecutorWorkerCount, defaultWorkerCount)
	}
	if deps.MaxRetries <= 0 {
		deps.MaxRetries = resolveIntEnv(envExecutorMaxRetries, defaultMaxRetries)
	}
	if deps.RetryBaseDelay <= 0 {
		deps.RetryBaseDelay = resolveDurationEnv(envExecutorRetryBaseDelay, defaultRetryBaseDelay)
	}
	if deps.RetryMaxDelay <= 0 {
		deps.RetryMaxDelay = resolveDurationEnv(envExecutorRetryMaxDelay, defaultRetryMaxDelay)
	}
	if deps.DispatchTimeout <= 0 {
		deps.DispatchTimeout = resolveDurationEnv(envExecutorDispatchTimeout, defaultDispatchTimeout)
	}
	return &Worker{deps: deps}, nil
}

func (w *Worker) log() core.Logger {
	if w.deps.Logger == nil {
		return &core.NoOpLogger{}
	}
	return w.deps.Logger
}

// Start implements core.Runnable.
func (w *Worker) Start(ctx context.Context) error {
	w.log().Info("Executor worker started", map[string]interface{}{
		"operation":    "executor_start",
		"worker_count": w.deps.WorkerCount,
		"queue_name":   w.deps.QueueName,
		"max_retries":  w.deps.MaxRetries,
	})

	var wg sync.WaitGroup
	for i := 0; i < w.deps.WorkerCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			w.consumeLoop(ctx, id)
		}(i)
	}

	<-ctx.Done()
	w.log().Info("Executor worker stopping", map[string]interface{}{
		"operation": "executor_stop",
	})
	wg.Wait()
	return nil
}

func (w *Worker) consumeLoop(ctx context.Context, workerID int) {
	for {
		if ctx.Err() != nil {
			return
		}
		handle, err := w.deps.Consumer.Consume(ctx, w.deps.QueueName)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if err != nil {
			telemetry.Counter("truvag3.scheduled_executor.consume_errors",
				"error_type", "consume_error",
				"module", "scheduled-executor",
			)
			w.log().Warn("Executor consume error", map[string]interface{}{
				"operation":  "executor_consume",
				"worker_id":  workerID,
				"error":      err.Error(),
				"error_type": "consume_error",
			})
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}
		if handle == nil {
			continue
		}
		w.dispatch(ctx, handle)
	}
}

func (w *Worker) dispatch(ctx context.Context, handle core.TaskHandle) {
	start := time.Now()
	task := handle.Task()
	dispatchCtx, endSpan := telemetry.StartLinkedSpan(ctx,
		"scheduled_executor.dispatch",
		task.TraceID,
		task.ParentSpanID,
		map[string]string{
			"task.id":      task.ID,
			"task.type":    task.Type,
			"schedule.id":  task.ScheduleID,
			"target_agent": task.TargetAgent,
			"queue.name":   w.deps.QueueName,
		},
	)
	defer endSpan()
	requestID := dispatchRequestID(task)
	dispatchCtx = withDispatchBaggage(dispatchCtx, requestID, task)
	telemetry.SetSpanAttributes(dispatchCtx,
		attribute.String("request_id", requestID),
		attribute.String("task_id", task.ID),
		attribute.String("schedule_id", task.ScheduleID),
		attribute.String("target_agent", task.TargetAgent),
	)
	telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.started",
		attribute.String("request_id", requestID),
		attribute.String("task_id", task.ID),
		attribute.String("schedule_id", task.ScheduleID),
		attribute.String("target_agent", task.TargetAgent),
	)

	telemetry.Counter("truvag3.scheduled_executor.tasks_received",
		"target_agent", task.TargetAgent,
		"module", "scheduled-executor",
	)

	// Settlement backstop: if dispatch returns without settling, Nack.
	settled := false
	settle := func(ack bool, reason string) {
		if settled {
			return
		}
		settled = true
		var err error
		if ack {
			err = handle.Ack(dispatchCtx)
		} else {
			err = handle.Nack(dispatchCtx, reason)
		}
		w.recordSettlement(dispatchCtx, task, requestID, ack, reason, err)
	}
	defer func() {
		if !settled {
			settle(false, "worker_internal_error")
		}
	}()

	// Validate task type.
	if task.Type != core.ScheduledTaskType {
		err := fmt.Errorf("invalid task type: %s", task.Type)
		telemetry.RecordSpanError(dispatchCtx, err)
		telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.invalid_task_type",
			attribute.String("request_id", requestID),
			attribute.String("task_id", task.ID),
			attribute.String("task_type", task.Type),
		)
		w.log().ErrorWithContext(dispatchCtx, "Executor: invalid task type", map[string]interface{}{
			"operation":   "executor_dispatch",
			"request_id":  requestID,
			"task_id":     task.ID,
			"schedule_id": task.ScheduleID,
			"task_type":   task.Type,
			"error_type":  "invalid_task_type",
			"error":       err.Error(),
		})
		settle(false, "invalid_task_type")
		return
	}
	if task.TargetAgent == "" {
		err := errors.New("task missing target_agent")
		telemetry.RecordSpanError(dispatchCtx, err)
		telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.missing_target_agent",
			attribute.String("request_id", requestID),
			attribute.String("task_id", task.ID),
			attribute.String("schedule_id", task.ScheduleID),
		)
		w.log().ErrorWithContext(dispatchCtx, "Executor: task missing target_agent", map[string]interface{}{
			"operation":   "executor_dispatch",
			"request_id":  requestID,
			"task_id":     task.ID,
			"schedule_id": task.ScheduleID,
			"error_type":  "missing_target_agent",
			"error":       err.Error(),
		})
		settle(false, "missing_target_agent")
		return
	}

	// Resolve target agent.
	svcInfo := w.deps.Catalog.FindByName(task.TargetAgent)
	if svcInfo == nil {
		// Cache miss — retry discovery with backoff. Transient Redis gaps
		// cause Refresh() to fail; agents re-register within seconds of
		// recovery, so a short retry window covers the common case.
		for attempt := 1; attempt <= discoveryMaxRetries && svcInfo == nil; attempt++ {
			if attempt > 1 {
				backoff := w.discoveryBackoff(attempt - 1)
				telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.discovery_retry_scheduled",
					attribute.String("request_id", requestID),
					attribute.String("task_id", task.ID),
					attribute.String("schedule_id", task.ScheduleID),
					attribute.String("target_agent", task.TargetAgent),
					attribute.Int("attempt", attempt),
					attribute.Int64("backoff_ms", backoff.Milliseconds()),
				)
				w.log().WarnWithContext(dispatchCtx, "Executor: retrying catalog refresh after backoff", map[string]interface{}{
					"operation":    "executor_dispatch",
					"request_id":   requestID,
					"task_id":      task.ID,
					"schedule_id":  task.ScheduleID,
					"target_agent": task.TargetAgent,
					"attempt":      attempt,
					"max_attempts": discoveryMaxRetries,
					"backoff_ms":   backoff.Milliseconds(),
				})
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
			}
			if refreshErr := w.deps.Catalog.Refresh(dispatchCtx); refreshErr != nil {
				telemetry.RecordSpanError(dispatchCtx, refreshErr)
				telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.catalog_refresh_failed",
					attribute.String("request_id", requestID),
					attribute.String("task_id", task.ID),
					attribute.String("schedule_id", task.ScheduleID),
					attribute.String("target_agent", task.TargetAgent),
					attribute.Int("attempt", attempt),
					attribute.String("error", refreshErr.Error()),
				)
				w.log().WarnWithContext(dispatchCtx, "Executor: catalog refresh failed", map[string]interface{}{
					"operation":    "executor_dispatch",
					"request_id":   requestID,
					"task_id":      task.ID,
					"schedule_id":  task.ScheduleID,
					"target_agent": task.TargetAgent,
					"attempt":      attempt,
					"max_attempts": discoveryMaxRetries,
					"error":        refreshErr.Error(),
					"error_type":   "catalog_refresh_error",
				})
				continue
			}
			svcInfo = w.deps.Catalog.FindByName(task.TargetAgent)
		}
	}
	if svcInfo == nil {
		err := fmt.Errorf("target agent %s not found in registry", task.TargetAgent)
		telemetry.RecordSpanError(dispatchCtx, err)
		telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.unknown_target_agent",
			attribute.String("request_id", requestID),
			attribute.String("task_id", task.ID),
			attribute.String("schedule_id", task.ScheduleID),
			attribute.String("target_agent", task.TargetAgent),
		)
		w.log().ErrorWithContext(dispatchCtx, "Executor: target agent not found in registry", map[string]interface{}{
			"operation":    "executor_dispatch",
			"request_id":   requestID,
			"task_id":      task.ID,
			"schedule_id":  task.ScheduleID,
			"target_agent": task.TargetAgent,
			"error_type":   "unknown_target_agent",
			"error":        err.Error(),
		})
		settle(false, "unknown_target_agent")
		return
	}
	if svcInfo.Type != core.ComponentTypeAgent {
		err := fmt.Errorf("target %s is not an agent", task.TargetAgent)
		telemetry.RecordSpanError(dispatchCtx, err)
		telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.target_not_agent",
			attribute.String("request_id", requestID),
			attribute.String("task_id", task.ID),
			attribute.String("schedule_id", task.ScheduleID),
			attribute.String("target_agent", task.TargetAgent),
		)
		w.log().ErrorWithContext(dispatchCtx, "Executor: target is not an agent", map[string]interface{}{
			"operation":    "executor_dispatch",
			"request_id":   requestID,
			"task_id":      task.ID,
			"schedule_id":  task.ScheduleID,
			"target_agent": task.TargetAgent,
			"target_type":  fmt.Sprintf("%v", svcInfo.Type),
			"error_type":   "target_not_agent",
			"error":        err.Error(),
		})
		settle(false, "target_not_agent")
		return
	}

	// Build URL from registry fields.
	url := fmt.Sprintf("http://%s:%d%s", svcInfo.Address, svcInfo.Port, scheduledEndpointPath)

	payload := buildScheduledRequest(task)
	body, err := json.Marshal(payload)
	if err != nil {
		telemetry.RecordSpanError(dispatchCtx, err)
		telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.marshal_failed",
			attribute.String("request_id", requestID),
			attribute.String("task_id", task.ID),
			attribute.String("schedule_id", task.ScheduleID),
			attribute.String("error", err.Error()),
		)
		w.log().ErrorWithContext(dispatchCtx, "Executor: failed to marshal scheduled request", map[string]interface{}{
			"operation":    "executor_dispatch",
			"request_id":   requestID,
			"task_id":      task.ID,
			"schedule_id":  task.ScheduleID,
			"target_agent": task.TargetAgent,
			"error":        err.Error(),
			"error_type":   "marshal_error",
		})
		settle(false, "marshal_error")
		return
	}

	// Retry loop with exponential backoff.
	var lastErr error
	for attempt := 1; attempt <= w.deps.MaxRetries; attempt++ {
		statusCode, respBody, postErr := w.postOnce(dispatchCtx, url, body)

		if postErr == nil && statusCode >= 200 && statusCode < 300 {
			success, agentErr := parseAgentResponse(respBody)
			if !success {
				// Semantic failure — agent decided to fail. Ack, not Nack.
				telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.agent_error",
					attribute.String("request_id", requestID),
					attribute.String("task_id", task.ID),
					attribute.String("schedule_id", task.ScheduleID),
					attribute.String("target_agent", task.TargetAgent),
					attribute.String("error", agentErr),
				)
				w.log().WarnWithContext(dispatchCtx, "Executor: agent returned success=false", map[string]interface{}{
					"operation":    "executor_dispatch",
					"request_id":   requestID,
					"task_id":      task.ID,
					"schedule_id":  task.ScheduleID,
					"target_agent": task.TargetAgent,
					"duration_ms":  time.Since(start).Milliseconds(),
					"error":        agentErr,
					"error_type":   "agent_error",
				})
				settle(true, "")
				return
			}
			telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.completed",
				attribute.String("request_id", requestID),
				attribute.String("task_id", task.ID),
				attribute.String("schedule_id", task.ScheduleID),
				attribute.String("target_agent", task.TargetAgent),
				attribute.Int("attempt", attempt),
				attribute.Int64("duration_ms", time.Since(start).Milliseconds()),
			)
			w.log().InfoWithContext(dispatchCtx, "Executor dispatch success", map[string]interface{}{
				"operation":    "executor_dispatch",
				"request_id":   requestID,
				"task_id":      task.ID,
				"schedule_id":  task.ScheduleID,
				"target_agent": task.TargetAgent,
				"status":       "success",
				"attempt":      attempt,
				"duration_ms":  time.Since(start).Milliseconds(),
			})
			settle(true, "")
			return
		}

		// Failure — decide retry vs terminal Nack.
		if !shouldRetry(statusCode, postErr) {
			if postErr != nil {
				telemetry.RecordSpanError(dispatchCtx, postErr)
			}
			telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.non_retryable_failure",
				attribute.String("request_id", requestID),
				attribute.String("task_id", task.ID),
				attribute.String("schedule_id", task.ScheduleID),
				attribute.String("target_agent", task.TargetAgent),
				attribute.Int("status_code", statusCode),
			)
			w.log().ErrorWithContext(dispatchCtx, "Executor dispatch failed without retry", map[string]interface{}{
				"operation":    "executor_dispatch",
				"request_id":   requestID,
				"task_id":      task.ID,
				"schedule_id":  task.ScheduleID,
				"target_agent": task.TargetAgent,
				"status_code":  statusCode,
				"error":        errorString(postErr),
				"error_type":   "non_retryable_failure",
			})
			settle(false, fmt.Sprintf("non_retryable_status_%d", statusCode))
			return
		}
		lastErr = postErr
		if lastErr == nil {
			lastErr = fmt.Errorf("status %d", statusCode)
		}
		telemetry.RecordSpanError(dispatchCtx, lastErr)

		if attempt < w.deps.MaxRetries {
			backoff := w.computeBackoff(attempt)
			telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.retry_scheduled",
				attribute.String("request_id", requestID),
				attribute.String("task_id", task.ID),
				attribute.String("schedule_id", task.ScheduleID),
				attribute.String("target_agent", task.TargetAgent),
				attribute.Int("attempt", attempt),
				attribute.Int("status_code", statusCode),
				attribute.Int64("backoff_ms", backoff.Milliseconds()),
				attribute.String("error", lastErr.Error()),
			)
			w.log().WarnWithContext(dispatchCtx, "Executor dispatch transient failure, retrying", map[string]interface{}{
				"operation":    "executor_dispatch",
				"request_id":   requestID,
				"task_id":      task.ID,
				"schedule_id":  task.ScheduleID,
				"target_agent": task.TargetAgent,
				"attempt":      attempt,
				"status_code":  statusCode,
				"backoff_ms":   backoff.Milliseconds(),
				"error":        lastErr.Error(),
				"error_type":   "transient",
			})
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}
	}

	// Max retries exhausted.
	telemetry.RecordSpanError(dispatchCtx, lastErr)
	telemetry.AddSpanEvent(dispatchCtx, "scheduled_executor.dispatch.max_retries_exhausted",
		attribute.String("request_id", requestID),
		attribute.String("task_id", task.ID),
		attribute.String("schedule_id", task.ScheduleID),
		attribute.String("target_agent", task.TargetAgent),
		attribute.Int("attempts", w.deps.MaxRetries),
		attribute.String("error", errorString(lastErr)),
	)
	w.log().ErrorWithContext(dispatchCtx, "Executor dispatch max retries exhausted", map[string]interface{}{
		"operation":    "executor_dispatch",
		"request_id":   requestID,
		"task_id":      task.ID,
		"schedule_id":  task.ScheduleID,
		"target_agent": task.TargetAgent,
		"attempts":     w.deps.MaxRetries,
		"duration_ms":  time.Since(start).Milliseconds(),
		"error":        lastErr.Error(),
		"error_type":   "max_retries_exhausted",
	})
	settle(false, "max_retries_exhausted")
}

func (w *Worker) recordSettlement(ctx context.Context, task *core.Task, requestID string, ack bool, reason string, err error) {
	if ack {
		if err != nil {
			telemetry.RecordSpanError(ctx, err)
			telemetry.AddSpanEvent(ctx, "scheduled_executor.dispatch.ack_failed",
				attribute.String("request_id", requestID),
				attribute.String("task_id", task.ID),
				attribute.String("schedule_id", task.ScheduleID),
				attribute.String("error", err.Error()),
			)
			telemetry.Counter("truvag3.scheduled_executor.ack_errors_total",
				"module", "scheduled-executor",
			)
			w.log().ErrorWithContext(ctx, "Executor: ack failed after successful dispatch", map[string]interface{}{
				"operation":   "executor_dispatch",
				"request_id":  requestID,
				"task_id":     task.ID,
				"schedule_id": task.ScheduleID,
				"error":       err.Error(),
				"error_type":  "ack_failure",
			})
			return
		}
		telemetry.AddSpanEvent(ctx, "scheduled_executor.dispatch.acked",
			attribute.String("request_id", requestID),
			attribute.String("task_id", task.ID),
			attribute.String("schedule_id", task.ScheduleID),
			attribute.String("target_agent", task.TargetAgent),
		)
		telemetry.Counter("truvag3.scheduled_executor.tasks_dispatched",
			"target_agent", task.TargetAgent,
			"status", "success",
			"module", "scheduled-executor",
		)
		return
	}
	// Nack path.
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "scheduled_executor.dispatch.dlq_write_failed",
			attribute.String("request_id", requestID),
			attribute.String("task_id", task.ID),
			attribute.String("schedule_id", task.ScheduleID),
			attribute.String("reason", reason),
			attribute.String("error", err.Error()),
		)
		telemetry.Counter("truvag3.scheduled_executor.dlq_writes_total",
			"status", "failure",
			"module", "scheduled-executor",
		)
		w.log().ErrorWithContext(ctx, "Executor: Nack (DLQ persist) failed", map[string]interface{}{
			"operation":   "executor_dispatch",
			"request_id":  requestID,
			"task_id":     task.ID,
			"schedule_id": task.ScheduleID,
			"reason":      reason,
			"error":       err.Error(),
			"error_type":  "dlq_write_failure",
		})
		return
	}
	telemetry.AddSpanEvent(ctx, "scheduled_executor.dispatch.dead_lettered",
		attribute.String("request_id", requestID),
		attribute.String("task_id", task.ID),
		attribute.String("schedule_id", task.ScheduleID),
		attribute.String("target_agent", task.TargetAgent),
		attribute.String("reason", reason),
	)
	telemetry.Counter("truvag3.scheduled_executor.dlq_writes_total",
		"status", "success",
		"module", "scheduled-executor",
	)
	telemetry.Counter("truvag3.scheduled_executor.tasks_dispatched",
		"target_agent", task.TargetAgent,
		"status", "dead_letter",
		"module", "scheduled-executor",
	)
}

func (w *Worker) postOnce(ctx context.Context, url string, body []byte) (int, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, w.deps.DispatchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.deps.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, nil
}

func shouldRetry(statusCode int, err error) bool {
	if err != nil {
		return true // transport error
	}
	if statusCode >= 500 {
		return true
	}
	if statusCode == http.StatusRequestTimeout {
		return true // 408
	}
	if statusCode == http.StatusTooManyRequests {
		return true // 429
	}
	return false
}

func (w *Worker) computeBackoff(attempt int) time.Duration {
	delay := time.Duration(float64(w.deps.RetryBaseDelay) * math.Pow(2, float64(attempt-1)))
	if delay > w.deps.RetryMaxDelay {
		return w.deps.RetryMaxDelay
	}
	return delay
}

// discoveryBackoff computes exponential backoff for catalog refresh retries.
// Uses shorter constants than computeBackoff because Redis recovery is fast.
func (w *Worker) discoveryBackoff(attempt int) time.Duration {
	delay := time.Duration(float64(discoveryBaseDelay) * math.Pow(2, float64(attempt-1)))
	if delay > discoveryMaxDelay {
		return discoveryMaxDelay
	}
	return delay
}

func buildScheduledRequest(task *core.Task) map[string]interface{} {
	req := map[string]interface{}{
		"schedule_id": task.ScheduleID,
		"task_id":     task.ID,
		"input":       task.Input,
	}
	if instruction, ok := task.Input["instruction"].(string); ok {
		req["instruction"] = instruction
	}
	return req
}

func parseAgentResponse(body []byte) (bool, string) {
	var envelope struct {
		Success bool                   `json:"success"`
		Error   map[string]interface{} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false, fmt.Sprintf("unmarshal: %v", err)
	}
	if !envelope.Success {
		if msg, ok := envelope.Error["message"].(string); ok {
			return false, msg
		}
		return false, "agent returned success=false"
	}
	return true, ""
}

func dispatchRequestID(task *core.Task) string {
	return fmt.Sprintf("scheduled-%s-%d", task.ID, time.Now().UnixNano())
}

func withDispatchBaggage(ctx context.Context, requestID string, task *core.Task) context.Context {
	labels := []string{"request_id", requestID}
	if task != nil {
		if task.ScheduleID != "" {
			labels = append(labels, "schedule_id", task.ScheduleID)
		}
		if task.ID != "" {
			labels = append(labels, "task_id", task.ID)
		}
	}
	return telemetry.WithBaggage(ctx, labels...)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// resolveDurationEnv reads a Go duration from an environment variable,
// falling back to the given default. Same pattern as orchestration/scheduler.go.
func resolveDurationEnv(envVar string, fallback time.Duration) time.Duration {
	raw := os.Getenv(envVar)
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return fallback
}

// resolveIntEnv reads a positive integer from an environment variable,
// falling back to the given default.
func resolveIntEnv(envVar string, fallback int) int {
	raw := os.Getenv(envVar)
	if raw == "" {
		return fallback
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	return fallback
}
