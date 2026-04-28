package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// handleResumeSSE resumes execution after HITL approval and streams results via SSE.
// Endpoint: POST /hitl/resume/{checkpoint_id}
func (t *DevOpsChatAgent) handleResumeSSE(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With, X-Truvag3-Original-Request-ID")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "86400")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Extract checkpoint ID from path
	checkpointID := extractPathParam(r.URL.Path, "/hitl/resume/")
	originalRequestIDFromHeader := r.Header.Get("X-Truvag3-Original-Request-ID")

	if checkpointID == "" {
		http.Error(w, "Checkpoint ID required", http.StatusBadRequest)
		return
	}

	t.Logger.InfoWithContext(ctx, "SSE resume requested", map[string]interface{}{
		"operation":                       "hitl_resume_sse",
		"checkpoint_id":                   checkpointID,
		"original_request_id_from_header": originalRequestIDFromHeader,
	})

	// Check if SSE is supported
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	callback := NewSSECallbackWithLogger(w, flusher, t.Logger, ctx)

	// Get HITL infrastructure
	hitl := t.GetHITL()
	if hitl == nil {
		callback.SendError("hitl_not_configured", "HITL infrastructure not available", false)
		return
	}

	// Load checkpoint
	checkpoint, err := hitl.CheckpointStore.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to load checkpoint", map[string]interface{}{
			"operation":     "hitl_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		callback.SendError("checkpoint_not_found", "Checkpoint not found or expired", false)
		return
	}

	// Apply header override for original_request_id
	if originalRequestIDFromHeader != "" {
		checkpoint.OriginalRequestID = originalRequestIDFromHeader
	}

	// Verify checkpoint is in a resumable state
	if !orchestration.IsResumableStatus(checkpoint.Status) {
		callback.SendError("not_approved", "Checkpoint has not been approved", false)
		return
	}

	// BuildResumeContext creates the linked trace span and sets all required context values
	ctx, endLinkedSpan, err := orchestration.BuildResumeContext(ctx, checkpoint)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to build resume context", map[string]interface{}{
			"operation":     "hitl_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		callback.SendError("invalid_checkpoint", err.Error(), false)
		return
	}
	defer endLinkedSpan()

	// Telemetry: checkpoint loaded
	loadedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("status", string(checkpoint.Status)),
	}
	if checkpoint.Decision != nil {
		loadedAttrs = append(loadedAttrs,
			attribute.String("interrupt_reason", string(checkpoint.Decision.Reason)),
		)
	}
	if checkpoint.OriginalTraceID != "" {
		loadedAttrs = append(loadedAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.loaded", loadedAttrs...)

	// Extract session ID from checkpoint metadata
	sessionID := ""
	if checkpoint.UserContext != nil {
		if sid, ok := checkpoint.UserContext["session_id"].(string); ok {
			sessionID = sid
		}
	}
	if sessionID == "" {
		session := t.sessionStore.Create("", nil)
		sessionID = session.ID
		callback.sendEvent("session", map[string]interface{}{
			"id": sessionID,
		})
	}

	// Telemetry: resume started
	resumeAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("session_id", sessionID),
		attribute.String("status", string(checkpoint.Status)),
		attribute.String("interrupt_point", string(checkpoint.InterruptPoint)),
	}
	if checkpoint.Decision != nil {
		resumeAttrs = append(resumeAttrs,
			attribute.String("interrupt_reason", string(checkpoint.Decision.Reason)),
			attribute.String("interrupt_message", checkpoint.Decision.Message),
		)
	}
	if checkpoint.Plan != nil {
		resumeAttrs = append(resumeAttrs,
			attribute.String("plan_id", checkpoint.Plan.PlanID),
			attribute.Int("step_count", len(checkpoint.Plan.Steps)),
		)
	}
	if checkpoint.OriginalTraceID != "" {
		resumeAttrs = append(resumeAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.resume_started", resumeAttrs...)

	callback.SendStatus("resuming", "Resuming execution after approval...")

	resumeLogFields := map[string]interface{}{
		"operation":       "hitl_resume_sse",
		"checkpoint_id":   checkpointID,
		"completed_count": len(checkpoint.StepResults),
		"interrupt_point": checkpoint.InterruptPoint,
	}
	if checkpoint.Plan != nil {
		resumeLogFields["plan_id"] = checkpoint.Plan.PlanID
		resumeLogFields["step_count"] = len(checkpoint.Plan.Steps)
	}
	t.Logger.InfoWithContext(ctx, "Resume context built from checkpoint", resumeLogFields)

	// Re-process the original request with HITL resume context
	if err := t.ProcessWithStreaming(ctx, sessionID, checkpoint.OriginalRequest, callback); err != nil {
		// Check if this is another HITL interrupt (chained step-level)
		if orchestration.IsInterrupted(err) {
			t.Logger.InfoWithContext(ctx, "Resume paused for step-level HITL", map[string]interface{}{
				"operation":         "hitl_resume_sse",
				"checkpoint_id":     checkpointID,
				"new_checkpoint_id": orchestration.GetCheckpointID(err),
			})
			return
		}

		t.Logger.ErrorWithContext(ctx, "Resume processing failed", map[string]interface{}{
			"operation":     "hitl_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
			"duration_ms":   time.Since(startTime).Milliseconds(),
		})
		callback.SendError("resume_failed", err.Error(), true)
		return
	}

	// Mark checkpoint as completed
	checkpoint.Status = orchestration.CheckpointStatusCompleted
	if err := hitl.CheckpointStore.SaveCheckpoint(ctx, checkpoint); err != nil {
		t.Logger.WarnWithContext(ctx, "Failed to mark checkpoint completed", map[string]interface{}{
			"operation":     "hitl_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
	}

	// Telemetry: resume completed
	resumeCompletedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.Float64("duration_ms", float64(time.Since(startTime).Milliseconds())),
	}
	if checkpoint.OriginalTraceID != "" {
		resumeCompletedAttrs = append(resumeCompletedAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.resume_completed", resumeCompletedAttrs...)

	t.Logger.InfoWithContext(ctx, "SSE resume completed", map[string]interface{}{
		"operation":     "hitl_resume_sse",
		"checkpoint_id": checkpointID,
		"session_id":    sessionID,
		"duration_ms":   time.Since(startTime).Milliseconds(),
	})
}

// handleAutoResumeSSE handles SSE streaming for auto-approved checkpoints.
// Endpoint: GET /hitl/auto-resume/{checkpoint_id}/stream
func (t *DevOpsChatAgent) handleAutoResumeSSE(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With, X-Truvag3-Original-Request-ID")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "86400")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Extract checkpoint ID: /hitl/auto-resume/{id}/stream
	checkpointID := extractPathParam(r.URL.Path, "/hitl/auto-resume/")
	checkpointID = strings.TrimSuffix(checkpointID, "/stream")
	originalRequestIDFromHeader := r.Header.Get("X-Truvag3-Original-Request-ID")

	if checkpointID == "" {
		http.Error(w, "Checkpoint ID required", http.StatusBadRequest)
		return
	}

	t.Logger.InfoWithContext(ctx, "Auto-resume SSE requested", map[string]interface{}{
		"operation":                       "hitl_auto_resume_sse",
		"checkpoint_id":                   checkpointID,
		"original_request_id_from_header": originalRequestIDFromHeader,
	})

	// SSE setup
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	callback := NewSSECallbackWithLogger(w, flusher, t.Logger, ctx)

	// Load & validate checkpoint
	hitl := t.GetHITL()
	if hitl == nil {
		callback.SendError("hitl_not_configured", "HITL infrastructure not available", false)
		return
	}

	checkpoint, err := hitl.CheckpointStore.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to load checkpoint", map[string]interface{}{
			"operation":     "hitl_auto_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		callback.SendError("checkpoint_not_found", "Checkpoint not found or expired", false)
		return
	}

	// Auto-resume ONLY handles expired_approved
	if checkpoint.Status != orchestration.CheckpointStatusExpiredApproved {
		t.Logger.WarnWithContext(ctx, "Auto-resume rejected - invalid status", map[string]interface{}{
			"operation":       "hitl_auto_resume_sse",
			"checkpoint_id":   checkpointID,
			"expected_status": "expired_approved",
			"actual_status":   string(checkpoint.Status),
		})
		callback.SendError("invalid_status",
			fmt.Sprintf("Auto-resume requires status 'expired_approved', got '%s'", checkpoint.Status),
			false)
		return
	}

	if originalRequestIDFromHeader != "" {
		checkpoint.OriginalRequestID = originalRequestIDFromHeader
	}

	if checkpoint.Plan == nil {
		callback.SendError("invalid_checkpoint", "Checkpoint has no plan - cannot resume", false)
		return
	}

	// Build resume context
	ctx, endLinkedSpan, err := orchestration.BuildResumeContext(ctx, checkpoint)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to build resume context", map[string]interface{}{
			"operation":     "hitl_auto_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		callback.SendError("invalid_checkpoint", err.Error(), false)
		return
	}
	defer endLinkedSpan()

	// Telemetry
	loadedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("status", string(checkpoint.Status)),
		attribute.String("trigger", "expired_approved"),
	}
	if checkpoint.Decision != nil {
		loadedAttrs = append(loadedAttrs, attribute.String("interrupt_reason", string(checkpoint.Decision.Reason)))
	}
	if checkpoint.OriginalTraceID != "" {
		loadedAttrs = append(loadedAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.loaded", loadedAttrs...)

	resumeAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("interrupt_point", string(checkpoint.InterruptPoint)),
		attribute.String("trigger", "expired_approved"),
	}
	if checkpoint.Plan != nil {
		resumeAttrs = append(resumeAttrs,
			attribute.String("plan_id", checkpoint.Plan.PlanID),
			attribute.Int("step_count", len(checkpoint.Plan.Steps)),
		)
	}
	if checkpoint.OriginalTraceID != "" {
		resumeAttrs = append(resumeAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.auto_resume_started", resumeAttrs...)

	callback.SendStatus("resuming", "Auto-approved - resuming execution...")

	// Extract session ID
	sessionID := ""
	if checkpoint.UserContext != nil {
		if sid, ok := checkpoint.UserContext["session_id"].(string); ok {
			sessionID = sid
		}
	}
	if sessionID == "" {
		session := t.sessionStore.Create("", nil)
		sessionID = session.ID
		callback.sendEvent("session", map[string]interface{}{
			"id": sessionID,
		})
	}

	// Execute
	if err := t.ProcessWithStreaming(ctx, sessionID, checkpoint.OriginalRequest, callback); err != nil {
		if orchestration.IsInterrupted(err) {
			t.Logger.InfoWithContext(ctx, "Auto-resume paused for step-level HITL", map[string]interface{}{
				"operation":         "hitl_auto_resume_sse",
				"checkpoint_id":     checkpointID,
				"new_checkpoint_id": orchestration.GetCheckpointID(err),
			})
			return
		}

		t.Logger.ErrorWithContext(ctx, "Auto-resume processing failed", map[string]interface{}{
			"operation":     "hitl_auto_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
			"duration_ms":   time.Since(startTime).Milliseconds(),
		})
		callback.SendError("resume_failed", err.Error(), true)
		return
	}

	// Mark completed
	checkpoint.Status = orchestration.CheckpointStatusCompleted
	if err := hitl.CheckpointStore.SaveCheckpoint(ctx, checkpoint); err != nil {
		t.Logger.WarnWithContext(ctx, "Failed to mark checkpoint completed", map[string]interface{}{
			"operation":     "hitl_auto_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
	}

	completedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.Float64("duration_ms", float64(time.Since(startTime).Milliseconds())),
		attribute.String("trigger", "expired_approved"),
	}
	if checkpoint.OriginalTraceID != "" {
		completedAttrs = append(completedAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.auto_resume_completed", completedAttrs...)

	t.Logger.InfoWithContext(ctx, "Auto-resume SSE completed", map[string]interface{}{
		"operation":     "hitl_auto_resume_sse",
		"checkpoint_id": checkpointID,
		"session_id":    sessionID,
		"duration_ms":   time.Since(startTime).Milliseconds(),
	})
}

// extractPathParam is defined in handlers.go
