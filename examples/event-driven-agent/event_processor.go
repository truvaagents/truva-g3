package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/trace"
)

// HandleAlertInvestigation processes a critical alert using AI orchestration.
// This is the TaskHandler registered with the worker pool.
//
// Pipeline:
//  1. Deserialize alert from task input
//  2. Build enriched natural language query for the LLM planner
//  3. Call orchestrator.ProcessRequest() for AI-driven DAG execution
//  4. Handle ProviderError (AI client failures)
//  5. Cleanup dedup key on completion
func (a *EventDrivenAgent) HandleAlertInvestigation(
	ctx context.Context,
	task *core.Task,
	reporter core.ProgressReporter,
) error {
	startTime := time.Now()

	// 1. Deserialize alert from task input
	alertJSON, ok := task.Input["alert_json"].(string)
	if !ok || alertJSON == "" {
		return fmt.Errorf("alert_json field is required in task input")
	}

	var alert Alert
	if err := json.Unmarshal([]byte(alertJSON), &alert); err != nil {
		return fmt.Errorf("failed to deserialize alert: %w", err)
	}

	// Restore the originating HTTP trace context across the async queue boundary.
	// Placed after alert deserialization so span attributes are populated with real values.
	// SpanKindConsumer marks this as a queue-consumer span in Jaeger (distinct from the HTTP producer).
	// Degrades gracefully when TraceID/ParentSpanID are empty (legacy tasks or untraced alerts).
	ctx, endConsumerSpan := telemetry.StartLinkedSpanWithOptions(
		ctx,
		"alert.investigation",
		task.TraceID,
		task.ParentSpanID,
		map[string]string{
			"task.id":           task.ID,
			"alert.name":        alert.Labels["alertname"],
			"alert.severity":    alert.Labels["severity"],
			"alert.fingerprint": alert.Fingerprint,
			"link.type":         "alert_queue_consumer",
		},
		trace.SpanKindConsumer,
	)
	defer endConsumerSpan()

	a.Logger.InfoWithContext(ctx, "Starting alert investigation", map[string]interface{}{
		"task_id":     task.ID,
		"alertname":   alert.Labels["alertname"],
		"severity":    alert.Labels["severity"],
		"fingerprint": alert.Fingerprint,
		"instance":    alert.Labels["instance"],
		"operation":   "alert_investigation",
	})

	// Report planning phase
	_ = reporter.Report(&core.TaskProgress{
		CurrentStep: 1,
		TotalSteps:  3,
		StepName:    "Planning Investigation",
		Percentage:  5,
		Message:     "AI is analyzing the alert and planning investigation steps...",
	})

	// 2. Build enriched natural language query
	enrichedQuery := buildEnrichedQuery(alert)

	// 3. Check orchestrator availability
	a.mu.RLock()
	orch := a.orchestrator
	a.mu.RUnlock()

	if orch == nil {
		a.Logger.WarnWithContext(ctx, "Orchestrator not available, returning fallback", map[string]interface{}{
			"operation": "alert_investigation",
		})
		task.Result = map[string]interface{}{
			"status":    "degraded",
			"alertname": alert.Labels["alertname"],
			"message":   "AI orchestration unavailable. Alert logged for manual investigation.",
		}
		return nil
	}

	// Track step results
	var stepResults []StepResultSummary
	var stepResultsMu sync.Mutex

	// Set up step completion callback
	ctx = orchestration.WithStepCallback(ctx, func(stepIndex, totalSteps int, step orchestration.RoutingStep, result orchestration.StepResult) {
		status := "completed"
		if !result.Success {
			status = "failed"
		}

		stepResultsMu.Lock()
		stepResults = append(stepResults, StepResultSummary{
			ToolName: step.AgentName,
			Success:  result.Success,
			Duration: result.Duration.String(),
		})
		stepResultsMu.Unlock()

		// Report progress
		percentage := 10 + int(float64(stepIndex+1)/float64(totalSteps)*80)
		_ = reporter.Report(&core.TaskProgress{
			CurrentStep: stepIndex + 2,
			TotalSteps:  totalSteps + 2,
			StepName:    fmt.Sprintf("%s: %s", status, step.AgentName),
			Percentage:  float64(percentage),
			Message:     fmt.Sprintf("Tool %d/%d %s", stepIndex+1, totalSteps, status),
		})
	})

	// Store metadata for HITL checkpoint
	ctx = orchestration.WithMetadata(ctx, map[string]interface{}{
		"alertname":   alert.Labels["alertname"],
		"fingerprint": alert.Fingerprint,
		"severity":    alert.Labels["severity"],
		"task_id":     task.ID,
	})

	// 4. Call orchestrator
	response, err := orch.ProcessRequest(ctx, enrichedQuery, map[string]interface{}{
		"task_id":     task.ID,
		"alertname":   alert.Labels["alertname"],
		"fingerprint": alert.Fingerprint,
		"mode":        "event_driven",
	})

	if err != nil {
		// Handle HITL interrupt (write ops pending approval)
		if orchestration.IsInterrupted(err) {
			checkpoint := orchestration.GetCheckpoint(err)
			a.Logger.InfoWithContext(ctx, "Investigation paused for HITL approval", map[string]interface{}{
				"task_id":       task.ID,
				"checkpoint_id": checkpoint.CheckpointID,
				"alertname":     alert.Labels["alertname"],
				"operation":     "alert_investigation",
			})
			task.Result = map[string]interface{}{
				"status":        "pending_approval",
				"checkpoint_id": checkpoint.CheckpointID,
				"alertname":     alert.Labels["alertname"],
				"message":       "Write operations require human approval",
			}
			return nil // Not an error -- HITL is expected
		}

		// Handle ProviderError (AI client failure)
		var pe core.ProviderError
		if errors.As(err, &pe) {
			a.Logger.ErrorWithContext(ctx, "AI provider error during investigation", map[string]interface{}{
				"task_id":     task.ID,
				"provider":    pe.Provider(),
				"status_code": pe.StatusCode(),
				"alertname":   alert.Labels["alertname"],
				"error":       pe.Error(),
				"operation":   "alert_investigation",
			})
			telemetry.Counter("event_agent.alerts_processed", "status", "provider_error", "module", "agent")
			return fmt.Errorf("AI provider %s failed: %w", pe.Provider(), err)
		}

		telemetry.Counter("event_agent.alerts_processed", "status", "failed", "module", "agent")
		return fmt.Errorf("orchestration failed for alert %s: %w", alert.Labels["alertname"], err)
	}

	// 5. Cleanup dedup key (allow re-investigation if alert fires again)
	dedupKey := a.alertDedupKey(alert.Fingerprint)
	if err := a.redisClient.Del(ctx, dedupKey).Err(); err != nil {
		a.Logger.WarnWithContext(ctx, "Failed to cleanup dedup key", map[string]interface{}{
			"dedup_key": dedupKey,
			"error":     err.Error(),
			"operation": "alert_investigation",
		})
	}

	// Report completion
	duration := time.Since(startTime)
	_ = reporter.Report(&core.TaskProgress{
		CurrentStep: len(response.AgentsInvolved) + 2,
		TotalSteps:  len(response.AgentsInvolved) + 2,
		StepName:    "Complete",
		Percentage:  100,
		Message:     fmt.Sprintf("Investigation complete. %d tools used.", len(response.AgentsInvolved)),
	})

	// Build result
	task.Result = map[string]interface{}{
		"status":         "completed",
		"alertname":      alert.Labels["alertname"],
		"fingerprint":    alert.Fingerprint,
		"response":       response.Response,
		"tools_used":     response.AgentsInvolved,
		"step_results":   stepResults,
		"confidence":     response.Confidence,
		"request_id":     response.RequestID,
		"execution_time": duration.String(),
		"duration_ms":    duration.Milliseconds(),
	}

	// Emit metrics
	telemetry.Counter("event_agent.alerts_processed", "status", "completed", "module", "agent")
	telemetry.Histogram("event_agent.processing_duration_ms", float64(duration.Milliseconds()))

	a.Logger.InfoWithContext(ctx, "Alert investigation completed", map[string]interface{}{
		"task_id":     task.ID,
		"alertname":   alert.Labels["alertname"],
		"tools_used":  len(response.AgentsInvolved),
		"duration_ms": duration.Milliseconds(),
		"confidence":  response.Confidence,
		"operation":   "alert_investigation",
		"status":      "success",
	})

	return nil
}

// buildEnrichedQuery constructs a natural language query from the alert for the LLM planner.
func buildEnrichedQuery(alert Alert) string {
	incidentChannel := os.Getenv("TRUVAG3_SLACK_CHANNEL_INCIDENTS")
	if incidentChannel == "" {
		incidentChannel = "#incidents"
	}
	return fmt.Sprintf(
		"ALERT: %s (severity: %s). %s. "+
			"Affected instance or target: %s. Investigate and respond using the agent's "+
			"configured incident-response procedure. When that procedure calls for a "+
			"team notification, use the configured incident channel %s.",
		alert.Labels["alertname"],
		alert.Labels["severity"],
		alert.Annotations["summary"],
		alert.Labels["instance"],
		incidentChannel,
	)
}

// StepResultSummary provides a summary of each tool execution step.
type StepResultSummary struct {
	ToolName string `json:"tool_name"`
	Success  bool   `json:"success"`
	Duration string `json:"duration"`
}

// executeApprovedCheckpointResponse uses the same result-bearing orchestrator
// entry point for HTTP and queue resumes. The coordinator supplies the restored
// context and owns every checkpoint status transition.
func (a *EventDrivenAgent) executeApprovedCheckpointResponse(ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*orchestration.OrchestratorResponse, *orchestration.ExecutionResult, error) {
	orch := a.GetOrchestrator()
	if orch == nil {
		return nil, nil, errors.New("orchestrator not available")
	}
	return orch.ProcessRequestWithExecution(ctx, checkpoint.OriginalRequest, checkpoint.UserContext)
}

func (a *EventDrivenAgent) executeApprovedCheckpoint(ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*orchestration.ExecutionResult, error) {
	_, execution, err := a.executeApprovedCheckpointResponse(ctx, checkpoint)
	return execution, err
}

func (a *EventDrivenAgent) resumeCheckpoint(ctx context.Context, checkpointID string) (*orchestration.OrchestratorResponse, error) {
	a.mu.RLock()
	hitl := a.hitl
	a.mu.RUnlock()
	if hitl == nil || hitl.Resumer == nil {
		return nil, errors.New("HITL resume coordinator not available")
	}
	var response *orchestration.OrchestratorResponse
	_, err := hitl.Resumer.ResumeWithExecutor(ctx, checkpointID,
		orchestration.ResumeExecutorFunc(func(resumeCtx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*orchestration.ExecutionResult, error) {
			var execution *orchestration.ExecutionResult
			var executeErr error
			response, execution, executeErr = a.executeApprovedCheckpointResponse(resumeCtx, checkpoint)
			if executeErr == nil && response == nil {
				executeErr = errors.New("resume produced no application response")
			}
			return execution, executeErr
		}))
	return response, err
}

// resumeSuccessor recognizes only a durably finalized continuation, not an
// interrupt nested inside a finalization failure.
func resumeSuccessor(err error) *orchestration.ExecutionCheckpoint {
	var lifecycle *orchestration.ErrCheckpointResumeLifecycle
	if err == nil || errors.As(err, &lifecycle) {
		return nil
	}
	return orchestration.GetCheckpoint(err)
}

// HandleHITLResumeTask uses the worker's task context, not the submitting HTTP
// request. A failed attempt is reported as a failed task, never auto-resubmitted:
// tools may already have acted. A saved successor requires another decision.
func (a *EventDrivenAgent) HandleHITLResumeTask(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
	started := time.Now()
	checkpointID, _ := task.Input["checkpoint_id"].(string)
	if checkpointID == "" {
		return &orchestration.ErrInvalidResumeRequest{Field: "checkpoint_id"}
	}
	// Preserve queue-consumer observability without creating a second hitl.resume span.
	ctx, endConsumer := telemetry.StartLinkedSpanWithOptions(ctx, "hitl.resume_task",
		task.TraceID, task.ParentSpanID, map[string]string{"task.id": task.ID, "link.type": "task_queue_consumer"}, trace.SpanKindConsumer)
	defer endConsumer()
	a.Logger.InfoWithContext(ctx, "HITL resume task started", map[string]interface{}{
		"operation": "hitl_resume_task", "checkpoint_id": checkpointID, "task_id": task.ID,
	})
	_ = reporter.Report(&core.TaskProgress{CurrentStep: 1, TotalSteps: 3, StepName: "Resuming from checkpoint", Percentage: 10})
	response, err := a.resumeCheckpoint(ctx, checkpointID)
	if child := resumeSuccessor(err); child != nil {
		task.Result = map[string]interface{}{
			"status": "pending_approval", "checkpoint_id": child.CheckpointID,
			"checkpoint":   orchestration.CheckpointResponseFrom(child),
			"resumed_from": checkpointID, "message": "Another step requires approval",
		}
		telemetry.Counter("event_agent.hitl_resume_completed", "status", "nested_interrupt", "module", "agent")
		return nil
	}
	if err != nil {
		_, code, _ := orchestration.HITLErrorResponse(err)
		task.Result = map[string]interface{}{"status": "failed", "code": code, "resumed_from": checkpointID, "retryable": false}
		telemetry.Counter("event_agent.hitl_resume_completed", "status", "failed", "module", "agent")
		return fmt.Errorf("resume task failed: %w", err)
	}
	if response == nil {
		return &orchestration.ErrCheckpointExecutionFailed{}
	}
	duration := time.Since(started)
	task.Result = map[string]interface{}{
		"status": "completed", "resumed_from": checkpointID, "response": response.Response,
		"tools_used": response.AgentsInvolved, "confidence": response.Confidence,
		"request_id": response.RequestID, "duration_ms": duration.Milliseconds(),
	}
	_ = reporter.Report(&core.TaskProgress{CurrentStep: 3, TotalSteps: 3, StepName: "Complete", Percentage: 100,
		Message: fmt.Sprintf("Resume completed. %d tools used.", len(response.AgentsInvolved))})
	telemetry.Counter("event_agent.hitl_resume_completed", "status", "completed", "module", "agent")
	telemetry.Histogram("event_agent.hitl_resume_duration_ms", float64(duration.Milliseconds()))
	a.Logger.InfoWithContext(ctx, "HITL resume task completed", map[string]interface{}{
		"operation": "hitl_resume_task", "checkpoint_id": checkpointID, "task_id": task.ID,
		"request_id": response.RequestID, "duration_ms": duration.Milliseconds(),
	})
	return nil
}

func writeHITLResumeError(w http.ResponseWriter, err error) {
	status, code, message := orchestration.HITLErrorResponse(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": message, "code": code, "retryable": false})
}

// HandleHITLResume is the synchronous embedded-mode transport. API-only mode
// enqueues a task instead; both reach the same coordinator in an execution host.
func (a *EventDrivenAgent) HandleHITLResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed, use POST", http.StatusMethodNotAllowed)
		return
	}
	checkpointID := strings.TrimPrefix(r.URL.Path, "/hitl/resume/")
	if checkpointID == "" || strings.Contains(checkpointID, "/") {
		writeHITLResumeError(w, &orchestration.ErrInvalidResumeRequest{Field: "checkpoint_id"})
		return
	}
	response, err := a.resumeCheckpoint(r.Context(), checkpointID)
	if child := resumeSuccessor(err); child != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "interrupted", "interrupted": true, "checkpoint_id": child.CheckpointID,
			"checkpoint": orchestration.CheckpointResponseFrom(child), "resumed_from": checkpointID,
		})
		return
	}
	if err != nil {
		writeHITLResumeError(w, err)
		return
	}
	if response == nil {
		writeHITLResumeError(w, &orchestration.ErrCheckpointExecutionFailed{})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "completed", "resumed_from": checkpointID,
		"response": response.Response, "tools_used": response.AgentsInvolved,
		"confidence": response.Confidence, "request_id": response.RequestID,
	})
}
