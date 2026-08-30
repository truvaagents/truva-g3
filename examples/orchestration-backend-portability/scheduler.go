package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

const (
	SchedulerServiceName   = "portable-scheduler-tool"
	ScheduledExecutorQueue = orchestration.ScheduledExecutorQueue
	maxScheduledResponse   = 2 << 20
)

// RunSchedulerTool exposes the framework's standard scheduling capabilities
// over the proof-owned PostgreSQL ScheduleStore while running the neutral
// Scheduler with PostgreSQL task idempotency, NATS dispatch, and a Redis lock.
func RunSchedulerTool(
	ctx context.Context,
	backends SchedulerBackends,
	redisURL string,
	port int,
	serviceNamespace string,
) error {
	if backends.Schedules == nil || backends.Tasks == nil || backends.Dispatcher == nil || backends.Lock == nil {
		return fmt.Errorf("live portability: complete scheduler backends are required")
	}
	serviceNamespace = strings.TrimSpace(serviceNamespace)
	if serviceNamespace == "" {
		return fmt.Errorf("live portability: service namespace is required")
	}

	tool := core.NewTool(SchedulerServiceName)
	framework, err := core.NewFramework(
		tool,
		core.WithName(SchedulerServiceName),
		core.WithPort(port),
		core.WithNamespace(serviceNamespace),
		core.WithRedisURL(redisURL),
		core.WithDiscovery(true, "redis"),
		core.WithCORSDefaults(),
	)
	if err != nil {
		return fmt.Errorf("live portability: construct scheduler framework: %w", err)
	}

	orchestration.RegisterScheduleCapabilities(tool, backends.Schedules)
	registerSchedulerProofCapabilities(tool, backends)
	scheduler, err := orchestration.NewScheduler(orchestration.SchedulerDeps{
		ScheduleStore:  backends.Schedules,
		TaskDispatcher: backends.Dispatcher,
		TaskStore:      backends.Tasks,
		Lock:           backends.Lock,
		Logger:         tool.Logger,
	})
	if err != nil {
		return fmt.Errorf("live portability: construct scheduler: %w", err)
	}
	framework.RegisterRunnable(scheduler)
	return framework.Run(ctx)
}

func registerSchedulerProofCapabilities(tool *core.BaseTool, backends SchedulerBackends) {
	tool.RegisterCapability(core.Capability{
		Name:        "portability_backend_status",
		Description: "Inspect the provider selections used by this scheduler proof.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Internal:    true,
		Handler: func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodPost {
				writeToolError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST is required")
				return
			}
			writeToolSuccess(writer, http.StatusOK, map[string]interface{}{
				"validated":         backends.Descriptor.Validated,
				"selected_backends": backends.Descriptor.SelectedBackends,
			})
		},
	})

	tool.RegisterCapability(core.Capability{
		Name:        "portability_task_status",
		Description: "Read a materialized scheduled task from PostgreSQL.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Internal:    true,
		InputSummary: &core.SchemaSummary{RequiredFields: []core.FieldHint{
			{Name: "task_id", Type: "string", Description: "Deterministic scheduled task identifier."},
		}},
		Handler: func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodPost {
				writeToolError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST is required")
				return
			}
			defer func() { _ = request.Body.Close() }()
			request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			var input struct {
				TaskID string `json:"task_id"`
			}
			if err := decoder.Decode(&input); err != nil {
				writeToolError(writer, http.StatusBadRequest, "INVALID_REQUEST", "task_id is required")
				return
			}
			input.TaskID = strings.TrimSpace(input.TaskID)
			if input.TaskID == "" {
				writeToolError(writer, http.StatusBadRequest, "INVALID_TASK_ID", "task_id is required")
				return
			}
			task, err := backends.Tasks.Get(request.Context(), input.TaskID)
			if err != nil {
				if errors.Is(err, core.ErrTaskNotFound) {
					writeToolError(writer, http.StatusNotFound, "TASK_NOT_FOUND", "task was not found")
					return
				}
				writeToolError(writer, http.StatusServiceUnavailable, "TASK_READ_FAILED", err.Error())
				return
			}
			writeToolSuccess(writer, http.StatusOK, map[string]interface{}{"task": task})
		},
	})
}

func writeToolSuccess(writer http.ResponseWriter, status int, data interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(core.ToolResponse{Success: true, Data: data})
}

func writeToolError(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Category:  core.CategoryServiceError,
			Retryable: status >= http.StatusInternalServerError,
		},
	})
}

type serviceResolver interface {
	FindService(ctx context.Context, serviceName string) ([]*core.ServiceInfo, error)
}

// ScheduledExecutor consumes the NATS scheduled-executor queue, resolves the
// target agent through Redis, invokes its standard scheduled endpoint, and
// persists terminal task state in PostgreSQL before settling the NATS claim.
type ScheduledExecutor struct {
	consumer core.TaskConsumer
	tasks    core.TaskStore
	resolver serviceResolver
	http     *http.Client
	logger   core.Logger
	queue    string
}

var _ core.Runnable = (*ScheduledExecutor)(nil)

func NewScheduledExecutor(backends ExecutorBackends, client *http.Client, logger core.Logger) (*ScheduledExecutor, error) {
	if backends.Consumer == nil || backends.Tasks == nil || backends.Discovery == nil {
		return nil, fmt.Errorf("live portability: executor consumer, task, and discovery backends are required")
	}
	if client == nil {
		return nil, fmt.Errorf("live portability: HTTP client is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("live portability: logger is required")
	}
	return &ScheduledExecutor{
		consumer: backends.Consumer,
		tasks:    backends.Tasks,
		resolver: backends.Discovery,
		http:     client,
		logger:   logger,
		queue:    ScheduledExecutorQueue,
	}, nil
}

func (executor *ScheduledExecutor) Start(ctx context.Context) error {
	executor.logger.Info("portable scheduled executor started", map[string]interface{}{"queue": executor.queue})
	for ctx.Err() == nil {
		handle, err := executor.consumer.Consume(ctx, executor.queue)
		if err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return nil
			}
			executor.logger.Error("scheduled consume failed", map[string]interface{}{"error": err.Error()})
			if !waitForRetry(ctx) {
				break
			}
			continue
		}
		if handle == nil {
			continue
		}
		if err := executor.process(ctx, handle); err != nil && !errors.Is(err, context.Canceled) {
			taskID := ""
			if task := handle.Task(); task != nil {
				taskID = task.ID
			}
			executor.logger.Error("scheduled dispatch failed", map[string]interface{}{
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
	}
	return nil
}

func (executor *ScheduledExecutor) process(ctx context.Context, handle core.TaskHandle) error {
	transportTask := handle.Task()
	if transportTask == nil || strings.TrimSpace(transportTask.ID) == "" {
		return handle.Nack(ctx, "invalid_task")
	}
	task, err := executor.tasks.Get(ctx, transportTask.ID)
	if err != nil {
		// Leave the NATS claim unsettled when PostgreSQL is temporarily
		// unavailable; JetStream will make it visible after AckWait.
		return fmt.Errorf("load PostgreSQL task: %w", err)
	}
	if task.Status == core.TaskStatusCompleted {
		return handle.Ack(ctx)
	}
	if task.Type != core.ScheduledTaskType || strings.TrimSpace(task.TargetAgent) == "" {
		return executor.fail(ctx, handle, task, "invalid_scheduled_task", fmt.Errorf("invalid scheduled task metadata"))
	}
	instruction, ok := task.Input["instruction"].(string)
	instruction = strings.TrimSpace(instruction)
	if !ok || instruction == "" {
		return executor.fail(ctx, handle, task, "missing_instruction", fmt.Errorf("scheduled instruction is required"))
	}

	started := time.Now().UTC()
	task.Status = core.TaskStatusRunning
	if task.StartedAt == nil {
		task.StartedAt = &started
	}
	if err := executor.tasks.Update(ctx, task); err != nil {
		return fmt.Errorf("mark PostgreSQL task running: %w", err)
	}

	service, err := executor.resolveTarget(ctx, task.TargetAgent)
	if err != nil {
		return executor.fail(ctx, handle, task, "target_discovery_failed", err)
	}
	payload, err := json.Marshal(orchestration.ScheduledRequest{
		ScheduleID:  task.ScheduleID,
		TaskID:      task.ID,
		Instruction: instruction,
		Input:       task.Input,
	})
	if err != nil {
		return executor.fail(ctx, handle, task, "request_encode_failed", err)
	}
	endpoint := serviceURL(service, "/api/v1/scheduled")
	responseBody, err := executor.postWithRetry(ctx, endpoint, payload)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return executor.fail(ctx, handle, task, "target_request_failed", err)
	}
	var response interface{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return executor.fail(ctx, handle, task, "target_response_invalid", err)
	}
	finished := time.Now().UTC()
	task.Status = core.TaskStatusCompleted
	task.CompletedAt = &finished
	task.Result = map[string]interface{}{
		"target_agent": task.TargetAgent,
		"response":     response,
	}
	if err := executor.tasks.Update(ctx, task); err != nil {
		return fmt.Errorf("complete PostgreSQL task: %w", err)
	}
	if err := handle.Ack(ctx); err != nil {
		return fmt.Errorf("acknowledge scheduled NATS task: %w", err)
	}
	executor.logger.Info("portable scheduled task completed", map[string]interface{}{
		"task_id":      task.ID,
		"target_agent": task.TargetAgent,
	})
	return nil
}

func (executor *ScheduledExecutor) resolveTarget(ctx context.Context, name string) (*core.ServiceInfo, error) {
	var lastError error
	for attempt := 0; attempt < 3; attempt++ {
		services, err := executor.resolver.FindService(ctx, name)
		if err == nil {
			for _, service := range services {
				if service != nil && service.Type == core.ComponentTypeAgent && service.Health == core.HealthHealthy {
					return service, nil
				}
			}
			for _, service := range services {
				if service != nil && service.Type == core.ComponentTypeAgent {
					return service, nil
				}
			}
			lastError = fmt.Errorf("agent %q was not registered", name)
		} else {
			lastError = err
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, fmt.Errorf("discover target agent: %w", lastError)
}

func (executor *ScheduledExecutor) postWithRetry(ctx context.Context, endpoint string, payload []byte) ([]byte, error) {
	var lastError error
	for attempt := 0; attempt < 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("create scheduled request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := executor.http.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, maxScheduledResponse+1))
			closeErr := response.Body.Close()
			if readErr != nil {
				return nil, fmt.Errorf("read scheduled response: %w", readErr)
			}
			if closeErr != nil {
				return nil, fmt.Errorf("close scheduled response: %w", closeErr)
			}
			if len(body) > maxScheduledResponse {
				return nil, fmt.Errorf("scheduled response exceeds 2 MiB")
			}
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				var envelope struct {
					Success bool `json:"success"`
				}
				if err := json.Unmarshal(body, &envelope); err != nil {
					return nil, fmt.Errorf("decode scheduled response envelope: %w", err)
				}
				if !envelope.Success {
					return nil, fmt.Errorf("target agent returned success=false")
				}
				return body, nil
			}
			lastError = fmt.Errorf("target agent returned HTTP %d", response.StatusCode)
			if response.StatusCode < http.StatusInternalServerError && response.StatusCode != http.StatusTooManyRequests {
				return nil, lastError
			}
		} else {
			lastError = err
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, fmt.Errorf("dispatch scheduled request: %w", lastError)
}

func (executor *ScheduledExecutor) fail(
	ctx context.Context,
	handle core.TaskHandle,
	task *core.Task,
	reason string,
	cause error,
) error {
	finished := time.Now().UTC()
	task.Status = core.TaskStatusFailed
	task.CompletedAt = &finished
	task.Error = &core.TaskError{Code: core.TaskErrorCodeHandlerError, Message: cause.Error(), Details: reason}
	if err := executor.tasks.Update(ctx, task); err != nil {
		return fmt.Errorf("persist PostgreSQL task failure: %w", err)
	}
	if err := handle.Nack(ctx, reason); err != nil {
		return fmt.Errorf("dead-letter scheduled NATS task after %v: %w", cause, err)
	}
	return cause
}
