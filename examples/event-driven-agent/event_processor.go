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

	// RC6: Restore originating HTTP trace context across the async queue boundary.
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
	reporter.Report(&core.TaskProgress{
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
		reporter.Report(&core.TaskProgress{
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
	dedupKey := fmt.Sprintf("truvag3:event:dedup:%s", alert.Fingerprint)
	if err := a.redisClient.Del(ctx, dedupKey).Err(); err != nil {
		a.Logger.WarnWithContext(ctx, "Failed to cleanup dedup key", map[string]interface{}{
			"dedup_key": dedupKey,
			"error":     err.Error(),
			"operation": "alert_investigation",
		})
	}

	// Report completion
	duration := time.Since(startTime)
	reporter.Report(&core.TaskProgress{
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

// HandleHITLResumeTask is the worker-pool task handler for async HITL resume.
// The API pod enqueues a task with type "hitl_resume"; the worker picks it up
// and re-enters the orchestrator via BuildResumeContext + ProcessRequest.
//
// Pipeline:
//  1. Deserialize checkpoint_id from task input
//  2. Load checkpoint from Redis DB 6
//  3. Restore trace context via StartLinkedSpanWithOptions (consumer span)
//  4. Build resume context (WithResumeMode, WithPlanOverride, WithCompletedSteps)
//  5. Re-enter orchestrator with original request
//  6. Handle nested HITL interrupts or completion
func (a *EventDrivenAgent) HandleHITLResumeTask(
	ctx context.Context,
	task *core.Task,
	reporter core.ProgressReporter,
) error {
	startTime := time.Now()

	// 1. Extract checkpoint ID from task input
	checkpointID, ok := task.Input["checkpoint_id"].(string)
	if !ok || checkpointID == "" {
		return fmt.Errorf("checkpoint_id is required in task input")
	}

	a.mu.RLock()
	hitl := a.hitl
	orch := a.orchestrator
	a.mu.RUnlock()

	if hitl == nil || orch == nil {
		return fmt.Errorf("HITL or orchestrator not available")
	}

	// 2. Load checkpoint
	checkpoint, err := hitl.CheckpointStore.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		telemetry.Counter("event_agent.hitl_resume_completed", "status", "failed", "module", "agent")
		return fmt.Errorf("failed to load checkpoint %s: %w", checkpointID, err)
	}

	// §3: Restore trace context across the async queue boundary.
	// Same pattern as HandleAlertInvestigation (line 51-64).
	traceID, _ := task.Input["trace_id"].(string)
	parentSpanID, _ := task.Input["parent_span_id"].(string)
	ctx, endConsumerSpan := telemetry.StartLinkedSpanWithOptions(
		ctx,
		"hitl.resume",
		traceID,
		parentSpanID,
		map[string]string{
			"task.id":       task.ID,
			"checkpoint.id": checkpointID,
			"link.type":     "hitl_resume_consumer",
		},
		trace.SpanKindConsumer,
	)
	defer endConsumerSpan()

	// §1: Context-aware logging after checkpoint load
	a.Logger.InfoWithContext(ctx, "HITL resume task started", map[string]interface{}{
		"operation":     "hitl_resume",
		"checkpoint_id": checkpointID,
		"request_id":    checkpoint.RequestID,
		"approved_by":   task.Input["approved_by"],
		"task_id":       task.ID,
	})

	reporter.Report(&core.TaskProgress{
		CurrentStep: 1, TotalSteps: 3,
		StepName:   "Resuming from checkpoint",
		Percentage: 10,
		Message:    fmt.Sprintf("Resuming execution from checkpoint %s", checkpointID),
	})

	// 3. Build resume context — sets WithResumeMode, WithPlanOverride, WithCompletedSteps,
	// WithPreResolvedParams, WithRequestMode, and WithMetadata.
	ctx, endLinkedSpan, err := orchestration.BuildResumeContext(ctx, checkpoint)
	if err != nil {
		telemetry.Counter("event_agent.hitl_resume_completed", "status", "failed", "module", "agent")
		return fmt.Errorf("failed to build resume context: %w", err)
	}
	defer endLinkedSpan()

	a.Logger.DebugWithContext(ctx, "Resume context built, re-entering orchestrator", map[string]interface{}{
		"operation":       "hitl_resume",
		"checkpoint_id":   checkpointID,
		"interrupt_point": string(checkpoint.InterruptPoint),
		"plan_id":         checkpoint.Plan.PlanID,
		"step_results":    len(checkpoint.StepResults),
	})

	// Propagate approved_by metadata
	if approvedBy, ok := task.Input["approved_by"].(string); ok {
		ctx = orchestration.WithMetadata(ctx, map[string]interface{}{
			"approved_by": approvedBy,
		})
	}

	// 4. Re-enter orchestrator with original request
	response, err := orch.ProcessRequest(ctx, checkpoint.Plan.OriginalRequest, checkpoint.UserContext)
	if err != nil {
		// Nested HITL interrupt (subsequent sensitive step)
		if orchestration.IsInterrupted(err) {
			newCheckpoint := orchestration.GetCheckpoint(err)
			a.Logger.InfoWithContext(ctx, "HITL resume hit nested interrupt", map[string]interface{}{
				"operation":         "hitl_resume",
				"checkpoint_id":     checkpointID,
				"new_checkpoint_id": newCheckpoint.CheckpointID,
			})
			telemetry.Counter("event_agent.hitl_resume_completed", "status", "nested_interrupt", "module", "agent")
			task.Result = map[string]interface{}{
				"status":        "pending_approval",
				"checkpoint_id": newCheckpoint.CheckpointID,
				"resumed_from":  checkpointID,
				"message":       "Another step requires approval",
			}
			return nil
		}
		telemetry.Counter("event_agent.hitl_resume_completed", "status", "failed", "module", "agent")
		return fmt.Errorf("resume orchestration failed: %w", err)
	}

	// 5. Mark original checkpoint as completed
	checkpoint.Status = orchestration.CheckpointStatusCompleted
	hitl.CheckpointStore.SaveCheckpoint(ctx, checkpoint)

	// 6. Store result
	duration := time.Since(startTime)
	task.Result = map[string]interface{}{
		"status":       "completed",
		"resumed_from": checkpointID,
		"response":     response.Response,
		"tools_used":   response.AgentsInvolved,
		"confidence":   response.Confidence,
		"request_id":   response.RequestID,
		"duration_ms":  duration.Milliseconds(),
	}

	reporter.Report(&core.TaskProgress{
		CurrentStep: 3, TotalSteps: 3,
		StepName:   "Complete",
		Percentage: 100,
		Message:    fmt.Sprintf("Resume completed. %d tools used.", len(response.AgentsInvolved)),
	})

	// §2: Emit metrics
	telemetry.Counter("event_agent.hitl_resume_completed", "status", "completed", "module", "agent")
	telemetry.Histogram("event_agent.hitl_resume_duration_ms", float64(duration.Milliseconds()))

	a.Logger.InfoWithContext(ctx, "HITL resume completed", map[string]interface{}{
		"operation":     "hitl_resume",
		"checkpoint_id": checkpointID,
		"tools_used":    len(response.AgentsInvolved),
		"confidence":    response.Confidence,
		"request_id":    response.RequestID,
		"duration_ms":   duration.Milliseconds(),
	})

	return nil
}

// HandleHITLResume resumes an interrupted orchestration from a checkpoint.
// POST /hitl/resume/{checkpoint_id}
//
// Unlike the generic HITLHandler.HandleResume (which only marks the checkpoint
// completed), this re-enters the orchestrator with WithResumeMode so the
// executor skips already-completed steps and continues from the interrupted step.
func (a *EventDrivenAgent) HandleHITLResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	// Extract checkpoint ID from path: /hitl/resume/{checkpoint_id}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[2] == "" {
		http.Error(w, "checkpoint_id is required in path", http.StatusBadRequest)
		return
	}
	checkpointID := parts[2]

	// Load checkpoint
	a.mu.RLock()
	hitl := a.hitl
	orch := a.orchestrator
	a.mu.RUnlock()

	if hitl == nil || orch == nil {
		http.Error(w, "HITL or orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	checkpoint, err := hitl.CheckpointStore.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		a.Logger.ErrorWithContext(ctx, "Failed to load checkpoint", map[string]interface{}{
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		http.Error(w, "checkpoint not found: "+err.Error(), http.StatusNotFound)
		return
	}

	// Build resume context — sets WithResumeMode, WithPlanOverride, WithCompletedSteps,
	// WithPreResolvedParams, WithRequestMode, and WithMetadata in a single call.
	// Also creates a linked trace span (RC7-B3) so the resume is visible in Jaeger.
	ctx, endLinkedSpan, err := orchestration.BuildResumeContext(ctx, checkpoint)
	if err != nil {
		a.Logger.ErrorWithContext(ctx, "Failed to build resume context", map[string]interface{}{
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer endLinkedSpan()

	a.Logger.InfoWithContext(ctx, "Resuming orchestration from checkpoint", map[string]interface{}{
		"checkpoint_id":    checkpointID,
		"request_id":       checkpoint.RequestID,
		"interrupt_point":  checkpoint.InterruptPoint,
		"original_request": checkpoint.Plan.OriginalRequest[:min(80, len(checkpoint.Plan.OriginalRequest))],
	})

	// Re-enter the orchestrator with the original request
	response, err := orch.ProcessRequest(ctx, checkpoint.Plan.OriginalRequest, checkpoint.UserContext)
	if err != nil {
		// Another HITL interrupt (shouldn't happen for same step but possible for later steps)
		if orchestration.IsInterrupted(err) {
			newCheckpoint := orchestration.GetCheckpoint(err)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":        "interrupted",
				"checkpoint_id": newCheckpoint.CheckpointID,
				"message":       "Another step requires approval",
				"resumed_from":  checkpointID,
			})
			return
		}

		a.Logger.ErrorWithContext(ctx, "Resume orchestration failed", map[string]interface{}{
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		http.Error(w, "resume failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Mark checkpoint as completed
	checkpoint.Status = orchestration.CheckpointStatusCompleted
	hitl.CheckpointStore.SaveCheckpoint(ctx, checkpoint)

	a.Logger.InfoWithContext(ctx, "Resume orchestration completed", map[string]interface{}{
		"checkpoint_id": checkpointID,
		"tools_used":    len(response.AgentsInvolved),
		"confidence":    response.Confidence,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":       "completed",
		"resumed_from": checkpointID,
		"response":     response.Response,
		"tools_used":   response.AgentsInvolved,
		"confidence":   response.Confidence,
	})
}
