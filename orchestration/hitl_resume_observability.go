package orchestration

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// The observer belongs to one resume call. It reports the execution ID even
// when the processing call returns an error or propagates a panic.
type resumeRequestIDObserver struct {
	mu sync.Mutex
	id string
}
type resumeRequestIDObserverKey struct{}

func reportResumeRequestID(ctx context.Context, id string) {
	if observer, ok := ctx.Value(resumeRequestIDObserverKey{}).(*resumeRequestIDObserver); ok {
		observer.mu.Lock()
		observer.id = id
		observer.mu.Unlock()
	}
}

type resumeObservation struct {
	coordinator  *ResumeCoordinator
	ctx          context.Context
	observer     *resumeRequestIDObserver
	checkpointID string
	attemptID    string
	successorID  string
	startedAt    time.Time
}

func (o *resumeObservation) context() context.Context {
	o.observer.mu.Lock()
	id := o.observer.id
	o.observer.mu.Unlock()
	if id != "" {
		o.ctx = WithRequestID(telemetry.WithBaggage(o.ctx, "request_id", id), id)
	}
	return o.ctx
}

func (o *resumeObservation) emit(event, operation, status, outcome, stage, errorType string, level string) {
	ctx := o.context()
	bag := telemetry.GetBaggage(ctx)
	requestID := bag["request_id"]
	if requestID == "" {
		requestID = GetRequestID(ctx)
	}
	attrs := []attribute.KeyValue{attribute.String("request_id", requestID)}
	fields := map[string]interface{}{
		"request_id": requestID, "checkpoint_id": o.checkpointID,
		"attempt_id": o.attemptID, "owner": o.coordinator.owner,
	}
	for _, key := range []string{"original_request_id", MetadataConversationID} {
		if value := bag[key]; value != "" {
			fields[key] = value
		}
	}
	if o.successorID != "" {
		fields["successor_checkpoint_id"] = o.successorID
	}
	fields["outcome"] = outcome
	if stage != "" {
		fields["stage"] = stage
	}
	if errorType != "" {
		fields["error_type"] = errorType
	}
	if stage == "" && (outcome == "invalid_input" || outcome == "in_progress" || outcome == "not_resumable" || outcome == "not_found") {
		fields["reason"] = outcome
	}
	for key, value := range fields {
		if key != "request_id" {
			attrs = append(attrs, attribute.String(key, value.(string)))
		}
	}
	telemetry.AddSpanEvent(ctx, event, attrs...)
	fields["operation"], fields["status"] = operation, status
	if stage != "" {
		fields["duration_ms"] = time.Since(o.startedAt).Milliseconds()
	}
	if errorType != "" {
		// Diagnostic descriptions are fixed vocabulary, not copies of backend
		// payloads or application errors. Returned causes remain unchanged.
		fields["error"] = "HITL resume operation failed: " + errorType
		telemetry.RecordSpanError(ctx, errors.New("HITL resume operation failed: "+errorType))
	}
	switch level {
	case "error":
		o.coordinator.logger.ErrorWithContext(ctx, "HITL resume lifecycle", fields)
	case "warn":
		o.coordinator.logger.WarnWithContext(ctx, "HITL resume lifecycle", fields)
	default:
		o.coordinator.logger.InfoWithContext(ctx, "HITL resume lifecycle", fields)
	}
}

func (o *resumeObservation) terminal(outcome, stage, errorType string) {
	o.coordinator.counter(MetricResumeOutcome, "module", telemetry.ModuleOrchestration, "outcome", outcome, "stage", stage)
	event, operation, status, level := "hitl.resume.failed", "hitl_resume_fail", "error", "error"
	switch outcome {
	case "completed":
		event, operation, status, level = "hitl.resume.completed", "hitl_resume_complete", "success", "info"
	case "continued":
		event, operation, status, level = "hitl.resume.continued", "hitl_resume_continue", "interrupted", "info"
	case "claim_lost":
		event, operation = "hitl.resume.claim_lost", "hitl_resume_claim_lost"
	case "cancelled", "deadline":
		level = "warn"
	}
	o.emit(event, operation, status, outcome, stage, errorType, level)
}

func resumeFailureClassification(err error, boundary string) (outcome, errorType string) {
	var lost *ErrCheckpointResumeClaimLost
	switch {
	case errors.As(err, &lost):
		return "claim_lost", "claim_lost"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline", "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled", "cancelled"
	default:
		return "failed", boundary
	}
}
