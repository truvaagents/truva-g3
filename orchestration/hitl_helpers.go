package orchestration

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"

	"github.com/truvaagents/truva-g3/telemetry"
)

// =============================================================================
// HITL Framework Helpers
// =============================================================================
//
// This file provides helper functions that encapsulate common HITL patterns.
// These helpers reduce boilerplate and ensure consistent behavior across
// applications using the HITL system.
//
// Context helpers for resume mode (WithResumeMode, WithPlanOverride, WithCompletedSteps)
// are defined in orchestrator.go.
//
// =============================================================================

// -----------------------------------------------------------------------------
// Request Mode Context Helpers
// -----------------------------------------------------------------------------

// requestModeKey is the context key for storing RequestMode.
type requestModeKey struct{}

// WithRequestMode adds the request mode to the context.
// Use this in your HTTP handlers to mark whether the request is streaming or non-streaming.
// This determines expiry behavior for HITL checkpoints.
//
// Example:
//
//	func handleStreamingRequest(w http.ResponseWriter, r *http.Request) {
//	    ctx := orchestration.WithRequestMode(r.Context(), orchestration.RequestModeStreaming)
//	    // Process request - checkpoints will have request_mode: "streaming"
//	}
//
//	func handleAsyncRequest(w http.ResponseWriter, r *http.Request) {
//	    ctx := orchestration.WithRequestMode(r.Context(), orchestration.RequestModeNonStreaming)
//	    // Process request - checkpoints will have request_mode: "non_streaming"
//	}
func WithRequestMode(ctx context.Context, mode RequestMode) context.Context {
	return context.WithValue(ctx, requestModeKey{}, mode)
}

// GetRequestMode retrieves the request mode from the context.
// Returns empty string if not set.
func GetRequestMode(ctx context.Context) RequestMode {
	if mode, ok := ctx.Value(requestModeKey{}).(RequestMode); ok {
		return mode
	}
	return ""
}

// -----------------------------------------------------------------------------
// Status Helpers
// -----------------------------------------------------------------------------

// IsResumableStatus checks if a checkpoint status allows resumption.
// Returns true for statuses that indicate the workflow can continue:
//   - approved: Human explicitly approved
//   - edited: Human modified and approved
//   - expired_approved: Auto-approved on timeout
//
// This helper prevents status check bugs and ensures consistent resume logic
// across the framework and applications.
func IsResumableStatus(status CheckpointStatus) bool {
	switch status {
	case CheckpointStatusApproved,
		CheckpointStatusEdited,
		CheckpointStatusExpiredApproved:
		return true
	default:
		return false
	}
}

// IsTerminalStatus checks if a checkpoint status is terminal (no further action possible).
// Returns true for:
//   - completed: Execution finished
//   - rejected: Human rejected
//   - aborted: Human or system aborted
//   - expired: Expired with implicit deny
//   - expired_rejected: Auto-rejected on timeout
//   - expired_aborted: Auto-aborted on timeout
func IsTerminalStatus(status CheckpointStatus) bool {
	switch status {
	case CheckpointStatusCompleted,
		CheckpointStatusRejected,
		CheckpointStatusAborted,
		CheckpointStatusExpired,
		CheckpointStatusExpiredRejected,
		CheckpointStatusExpiredAborted:
		return true
	default:
		return false
	}
}

// IsPendingStatus checks if a checkpoint is still awaiting a response.
func IsPendingStatus(status CheckpointStatus) bool {
	return status == CheckpointStatusPending
}

// -----------------------------------------------------------------------------
// Resume Context Builder
// -----------------------------------------------------------------------------

// BuildResumeContext prepares a context for HITL resume execution.
//
// This helper encapsulates the context setup pattern using existing helpers
// from orchestrator.go and executor.go:
//   - WithResumeMode(ctx, checkpoint.CheckpointID)
//   - WithPlanOverride(ctx, checkpoint.Plan)
//   - WithCompletedSteps(ctx, checkpoint.StepResults)
//   - WithPreResolvedParams(ctx, checkpoint.ResolvedParameters, stepID)
//
// The framework prepares the context; the application uses it with its own processing method.
// This keeps the framework decoupled from application-specific execution patterns.
//
// Usage in expiry callback:
//
//	checkpointStore.SetExpiryCallback(func(ctx context.Context, cp *ExecutionCheckpoint, action CommandType) {
//	    // Application decides: should we resume?
//	    if !orchestration.IsResumableStatus(cp.Status) || action != CommandApprove {
//	        return
//	    }
//
//	    // Framework prepares the context and creates a linked trace span (RC7).
//	    resumeCtx, endSpan, err := orchestration.BuildResumeContext(ctx, cp)
//	    if err != nil {
//	        log.Error("Failed to build resume context", "error", err)
//	        return
//	    }
//	    defer endSpan()
//
//	    // Application executes the resume using its own method
//	    sessionID := cp.UserContext["session_id"].(string)
//	    agent.ProcessWithStreaming(resumeCtx, sessionID, cp.OriginalRequest, callback)
//	})
func BuildResumeContext(ctx context.Context, checkpoint *ExecutionCheckpoint) (context.Context, func(), error) {
	noop := func() {}

	if checkpoint == nil {
		return nil, noop, fmt.Errorf("checkpoint cannot be nil")
	}

	// Validate checkpoint is resumable
	if !IsResumableStatus(checkpoint.Status) {
		return nil, noop, fmt.Errorf("checkpoint %s has non-resumable status %q "+
			"(only approved, edited, or expired_approved checkpoints can be resumed)",
			checkpoint.CheckpointID, checkpoint.Status)
	}

	// Restore trace context across the async boundary (RC7-B3).
	// Read typed fields first (set by createCheckpoint after RC7-B2 is deployed),
	// fall back to UserContext for checkpoints created before RC7-B2 was rolled out.
	traceID := checkpoint.OriginalTraceID
	spanID := checkpoint.OriginalSpanID
	if traceID == "" && checkpoint.UserContext != nil {
		if tid, ok := checkpoint.UserContext["original_trace_id"].(string); ok {
			traceID = tid
		}
		if sid, ok := checkpoint.UserContext["original_span_id"].(string); ok {
			spanID = sid
		}
	}

	// Resolve original_request_id for baggage propagation.
	originalRequestID := checkpoint.OriginalRequestID
	if originalRequestID == "" {
		originalRequestID = checkpoint.RequestID
	}

	// StartLinkedSpan creates a new span linked (not child) to the original trace.
	// Degrades gracefully when traceID/spanID are empty — creates an unlinked root span.
	// SpanKindInternal is correct here; queue workers use SpanKindConsumer upstream (RC6).
	resumeCtx, endSpan := telemetry.StartLinkedSpan(
		ctx, "hitl.resume", traceID, spanID,
		map[string]string{
			"checkpoint_id":       checkpoint.CheckpointID,
			"interrupt_point":     string(checkpoint.InterruptPoint),
			"request_id":          checkpoint.RequestID,
			"original_request_id": originalRequestID,
			"link.type":           "hitl_resume",
		},
	)
	// Fire a span event so the trace link is visible in the Jaeger event timeline.
	telemetry.AddSpanEvent(resumeCtx, "hitl.trace_link_created",
		attribute.String("original_trace_id", traceID),
		attribute.String("checkpoint_id", checkpoint.CheckpointID),
	)
	// Propagate original_request_id through W3C Baggage so all downstream child spans inherit it.
	resumeCtx = telemetry.WithBaggage(resumeCtx, "original_request_id", originalRequestID)

	// Build resume context using existing helpers from orchestrator.go
	resumeCtx = WithResumeMode(resumeCtx, checkpoint.CheckpointID)

	if checkpoint.Plan != nil {
		// Inject the approved plan so orchestrator skips LLM planning
		resumeCtx = WithPlanOverride(resumeCtx, checkpoint.Plan)
	}

	if len(checkpoint.StepResults) > 0 {
		// Inject completed step results so executor skips already-done work
		resumeCtx = WithCompletedSteps(resumeCtx, checkpoint.StepResults)
	}

	// Inject pre-resolved parameters for step-level HITL resume
	// This ensures the executor uses the approved parameter values
	if len(checkpoint.ResolvedParameters) > 0 && checkpoint.CurrentStep != nil {
		resumeCtx = WithPreResolvedParams(
			resumeCtx,
			checkpoint.ResolvedParameters,
			checkpoint.CurrentStep.StepID,
		)
	}

	// Preserve request mode if set
	if checkpoint.RequestMode != "" {
		resumeCtx = WithRequestMode(resumeCtx, checkpoint.RequestMode)
	}

	// Preserve metadata if set
	if len(checkpoint.UserContext) > 0 {
		resumeCtx = WithMetadata(resumeCtx, checkpoint.UserContext)
	}

	return resumeCtx, endSpan, nil
}
