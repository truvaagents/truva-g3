package orchestration

import (
	"context"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// executionParent identifies the immediate invocation, independently of the
// request-family root. It contains transport correlation, not authorization.
type executionParent struct {
	RequestID string
	StepID    string
}

type requestCorrelationKey struct{}
type checkpointExecutionCorrelationKey struct{}

// Presence is authoritative even for a checkpoint with no recorded parent.
type checkpointExecutionCorrelation struct {
	OriginalRequestID string
	Parent            executionParent
}

func withRequestCorrelation(ctx context.Context, correlation requestCorrelation) context.Context {
	return context.WithValue(ctx, requestCorrelationKey{}, correlation)
}

func requestCorrelationFromContext(ctx context.Context) (requestCorrelation, bool) {
	correlation, ok := ctx.Value(requestCorrelationKey{}).(requestCorrelation)
	return correlation, ok
}

func withCheckpointExecutionCorrelation(ctx context.Context, correlation checkpointExecutionCorrelation) context.Context {
	return context.WithValue(ctx, checkpointExecutionCorrelationKey{}, correlation)
}

func validExecutionCorrelationID(value string) bool {
	_, err := core.WithIncomingCall(context.Background(), core.IncomingCall{RequestID: value})
	return err == nil
}

// beginExecutionCorrelation consumes caller facts before establishing local
// identity. Only the request runner owns this immutable value.
func (o *AIOrchestrator) beginExecutionCorrelation(ctx context.Context, requestID string) (context.Context, requestCorrelation) {
	correlation := requestCorrelation{RequestID: requestID, OriginalRequestID: requestID}
	bag := telemetry.GetBaggage(ctx)
	root := bag["original_request_id"]
	if root != "" && !validExecutionCorrelationID(root) {
		emitExecutionCorrelationOmitted(ctx, o.logger, requestID, "original_request_id", "invalid_identifier")
		root = ""
	}
	call, hasCall := core.IncomingCallFromContext(ctx)
	if _, resumed := IsResumeMode(ctx); resumed {
		if restored, ok := ctx.Value(checkpointExecutionCorrelationKey{}).(checkpointExecutionCorrelation); ok {
			root = restored.OriginalRequestID
			correlation.Parent = restored.Parent
		}
	} else if hasCall {
		correlation.Parent = executionParent{RequestID: call.RequestID, StepID: call.StepID}
		if root == "" {
			root = call.OriginalRequestID
		} else if call.OriginalRequestID != "" && call.OriginalRequestID != root {
			correlation.Parent = executionParent{}
			emitExecutionCorrelationOmitted(ctx, o.logger, requestID, "parent_request_id", "root_conflict")
		}
	}
	if root != "" {
		correlation.OriginalRequestID = root
	}
	if correlation.Parent.RequestID == requestID {
		correlation.Parent = executionParent{}
		emitExecutionCorrelationOmitted(ctx, o.logger, requestID, "parent_request_id", "self_parent")
	}

	ctx = core.WithoutIncomingCall(ctx)
	ctx = core.WithRequestID(ctx, requestID)
	ctx = core.WithOriginalRequestID(ctx, correlation.OriginalRequestID)
	ctx = WithRequestID(ctx, requestID)
	ctx = core.WithStepID(ctx, "")
	ctx = core.WithPhaseNumber(ctx, 0)
	ctx = core.WithPlanID(ctx, "")
	for _, key := range []string{"step_id", "phase_number", "plan_id"} {
		ctx = telemetry.WithoutBaggageMember(ctx, key)
	}
	ctx = setExecutionIdentityBaggage(ctx, "request_id", requestID, requestID, o.logger)
	ctx = setExecutionIdentityBaggage(ctx, "original_request_id", correlation.OriginalRequestID, requestID, o.logger)
	return withRequestCorrelation(ctx, correlation), correlation
}

// Exact-set rejection removes stale identity while retaining the authoritative
// typed/core values. It never evicts unrelated baggage or changes execution.
func setExecutionIdentityBaggage(ctx context.Context, field, value, requestID string, logger core.Logger) context.Context {
	if value == "" {
		return telemetry.WithoutBaggageMember(ctx, field)
	}
	next, err := telemetry.WithBaggageExact(ctx, field, value)
	if err == nil {
		return next
	}
	ctx = telemetry.WithoutBaggageMember(ctx, field)
	reason, _ := telemetry.BaggageExactErrorReasonOf(err)
	emitExecutionCorrelationOmitted(ctx, logger, requestID, field, string(reason))
	return ctx
}

func emitExecutionCorrelationOmitted(ctx context.Context, logger core.Logger, requestID, field, reason string) {
	telemetry.AddSpanEvent(ctx, "orchestrator.correlation.omitted",
		attribute.String("request_id", requestID),
		attribute.String("field", field),
		attribute.String("reason", reason),
	)
	if logger != nil {
		logger.WarnWithContext(ctx, "Execution correlation omitted", map[string]interface{}{
			"operation": "execution_correlation", "request_id": requestID,
			"status": "omitted", "field": field, "reason": reason,
			"error_type": "invalid_correlation", "error": "execution correlation could not be propagated",
		})
	}
}

// restoreCheckpointExecutionCorrelation validates optional saved facts before
// any linked span is emitted. Approval transport metadata supplies neither root
// nor parent; legacy checkpoints use their own request ID as the root fallback.
func restoreCheckpointExecutionCorrelation(ctx context.Context, checkpoint *ExecutionCheckpoint) (context.Context, string) {
	root := checkpoint.OriginalRequestID
	if root == "" || !validExecutionCorrelationID(root) {
		if root != "" {
			emitExecutionCorrelationOmitted(ctx, nil, checkpoint.RequestID, "original_request_id", "invalid_identifier")
		}
		root = checkpoint.RequestID
		if !validExecutionCorrelationID(root) {
			root = ""
		}
	}
	parent := executionParent{RequestID: checkpoint.ParentRequestID, StepID: checkpoint.ParentStepID}
	if parent.RequestID != "" || parent.StepID != "" {
		_, err := core.WithIncomingCall(context.Background(), core.IncomingCall{
			RequestID: parent.RequestID, StepID: parent.StepID,
		})
		if err != nil || parent.RequestID == checkpoint.RequestID {
			parent = executionParent{}
			emitExecutionCorrelationOmitted(ctx, nil, checkpoint.RequestID, "parent_request_id", "invalid_checkpoint_parent")
		}
	}
	ctx = core.WithoutIncomingCall(ctx)
	ctx = withCheckpointExecutionCorrelation(ctx, checkpointExecutionCorrelation{OriginalRequestID: root, Parent: parent})
	return setExecutionIdentityBaggage(ctx, "original_request_id", root, checkpoint.RequestID, nil), root
}
