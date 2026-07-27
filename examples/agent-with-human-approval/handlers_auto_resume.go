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

// handleAutoResumeSSE handles SSE streaming for auto-approved checkpoints.
// This endpoint is called by the UI when it detects a checkpoint has been
// auto-approved (status: expired_approved) due to timeout with apply_default behavior.
//
// Endpoint: GET /hitl/auto-resume/{checkpoint_id}/stream
//
// This follows the same pattern as handleResumeSSE but specifically handles
// the expired_approved status (Scenario 3: Streaming + apply_default).
func (t *HITLChatAgent) handleAutoResumeSSE(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// =========================================================================
	// 1. CORS & REQUEST SETUP (matches handleResumeSSE)
	// =========================================================================
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With, X-Truvag3-Original-Request-ID")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "86400")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Extract checkpoint ID from path: /hitl/auto-resume/{id}/stream
	checkpointID := extractPathParam(r.URL.Path, "/hitl/auto-resume/")
	checkpointID = strings.TrimSuffix(checkpointID, "/stream")

	// Extract original request_id from header for trace correlation
	originalRequestIDFromHeader := r.Header.Get("X-Truvag3-Original-Request-ID")

	if checkpointID == "" {
		http.Error(w, "Checkpoint ID required", http.StatusBadRequest)
		return
	}

	// Entry log
	t.Logger.InfoWithContext(ctx, "Auto-resume SSE requested", map[string]interface{}{
		"operation":                       "hitl_auto_resume_sse",
		"checkpoint_id":                   checkpointID,
		"original_request_id_from_header": originalRequestIDFromHeader,
	})

	// =========================================================================
	// 2. SSE SETUP (matches handleResumeSSE)
	// =========================================================================
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

	// =========================================================================
	// 3. LOAD & VALIDATE CHECKPOINT (matches handleResumeSSE)
	// =========================================================================
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

	// CRITICAL: Validate status is expired_approved (auto-resume specific)
	// NOTE: We don't use IsResumableStatus() here because that includes approved/edited,
	// which are handled by the existing /hitl/commands/{id}/approve endpoint.
	// Auto-resume ONLY handles expired_approved (timeout auto-approval).
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

	// Apply header override for original_request_id before BuildResumeContext (RC7-B8).
	if originalRequestIDFromHeader != "" {
		checkpoint.OriginalRequestID = originalRequestIDFromHeader
	}

	// Validate checkpoint has required data (before span allocation).
	if checkpoint.Plan == nil {
		t.Logger.ErrorWithContext(ctx, "Invalid checkpoint - no plan stored", map[string]interface{}{
			"operation":     "hitl_auto_resume_sse",
			"checkpoint_id": checkpointID,
		})
		callback.SendError("invalid_checkpoint", "Checkpoint has no plan - cannot resume", false)
		return
	}

	// BuildResumeContext creates the linked trace span and sets all required context values (RC7-B8).
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

	// Span events fire on the linked span (after BuildResumeContext).
	loadedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("status", string(checkpoint.Status)),
		attribute.String("trigger", "expired_approved"),
	}
	if checkpoint.Decision != nil {
		loadedAttrs = append(loadedAttrs,
			attribute.String("interrupt_reason", string(checkpoint.Decision.Reason)),
		)
	}
	if checkpoint.OriginalTraceID != "" {
		loadedAttrs = append(loadedAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	if checkpoint.OriginalSpanID != "" {
		loadedAttrs = append(loadedAttrs, attribute.String("original_span_id", checkpoint.OriginalSpanID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.loaded", loadedAttrs...)

	resumeAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("status", string(checkpoint.Status)),
		attribute.String("interrupt_point", string(checkpoint.InterruptPoint)),
		attribute.String("trigger", "expired_approved"),
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
	telemetry.AddSpanEvent(ctx, "hitl.auto_resume_started", resumeAttrs...)

	callback.SendStatus("resuming", "Auto-approved - resuming execution...")

	// =========================================================================
	// 8. EXECUTE WITH STREAMING (matches handleResumeSSE)
	// =========================================================================

	// This example maps its chat-session UUID to the canonical conversation ID.
	sessionID := sessionIDFromCheckpoint(checkpoint.UserContext)
	if sessionID == "" {
		// Generate new session if not found
		session := t.sessionStore.Create("", nil)
		sessionID = session.ID
		callback.sendEvent("session", map[string]interface{}{
			"id": sessionID,
		})
	}

	if err := t.ProcessWithStreaming(ctx, sessionID, checkpoint.OriginalRequest, callback); err != nil {
		// Check if this is another HITL interrupt (step-level)
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

	// =========================================================================
	// 9. COMPLETION (matches handleResumeSSE)
	// =========================================================================

	checkpoint.Status = orchestration.CheckpointStatusCompleted
	if err := hitl.CheckpointStore.SaveCheckpoint(ctx, checkpoint); err != nil {
		t.Logger.WarnWithContext(ctx, "Failed to mark checkpoint completed", map[string]interface{}{
			"operation":     "hitl_auto_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
	}

	// Completion span event
	completedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.Float64("duration_ms", float64(time.Since(startTime).Milliseconds())),
		attribute.String("trigger", "expired_approved"),
	}
	if checkpoint.OriginalTraceID != "" {
		completedAttrs = append(completedAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.auto_resume_completed", completedAttrs...)

	// Completion log
	t.Logger.InfoWithContext(ctx, "Auto-resume SSE completed", map[string]interface{}{
		"operation":           "hitl_auto_resume_sse",
		"checkpoint_id":       checkpointID,
		"session_id":          sessionID,
		"original_request_id": checkpoint.OriginalRequestID,
		"duration_ms":         time.Since(startTime).Milliseconds(),
	})
}
