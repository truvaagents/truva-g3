package orchestration

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// ExecutionLifecycleObservation describes request completion, independently of
// individual step success or an application's positive/negative disposition.
// Absent on legacy/direct-plan records means unknown. Revisions are scoped to
// RequestID; resumed requests get a new ID linked to their predecessor.
type ExecutionLifecycleObservation struct {
	State      string    `json:"state"`
	Revision   uint64    `json:"revision"`
	ObservedAt time.Time `json:"observed_at"`
}

const (
	RequestLifecycleRunning   = "running"
	RequestLifecycleCompleted = "completed"
	RequestLifecycleFailed    = "failed"
	RequestLifecycleCancelled = "cancelled"
	RequestLifecycleSuspended = "suspended"
)

type executionLifecycleKey struct{}
type executionLifecycle struct {
	mu          sync.Mutex
	observation ExecutionLifecycleObservation
}

func executionLifecycleSnapshot(ctx context.Context) *ExecutionLifecycleObservation {
	holder, _ := ctx.Value(executionLifecycleKey{}).(*executionLifecycle)
	if holder == nil {
		return nil
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	copy := holder.observation
	return &copy
}

func (o *AIOrchestrator) settleRequestLifecycle(state *executionRunState, result *requestRunResult, err error) {
	holder, _ := state.Context.Value(executionLifecycleKey{}).(*executionLifecycle)
	if holder == nil {
		return
	}
	status := RequestLifecycleCompleted
	switch {
	case IsInterrupted(err):
		status = RequestLifecycleSuspended
	case errors.Is(err, context.Canceled):
		status = RequestLifecycleCancelled
	case err != nil || result == nil || result.Delivery.PartialContent:
		status = RequestLifecycleFailed
	}
	holder.mu.Lock()
	if holder.observation.State != RequestLifecycleRunning {
		holder.mu.Unlock()
		return
	}
	holder.observation = ExecutionLifecycleObservation{State: status, Revision: 2, ObservedAt: time.Now().UTC()}
	holder.mu.Unlock()
	telemetry.AddSpanEvent(state.Context, "orchestrator.execution.lifecycle", attribute.String("request_id", GetRequestID(state.Context)), attribute.String("state", status))
}

func (o *AIOrchestrator) publishSettledLifecycle(state *executionRunState) {
	if o.executionStore == nil {
		return
	}
	observation := executionLifecycleSnapshot(state.Context)
	retention := executionRetentionTTL(&StoredExecution{Lifecycle: observation}, o.config.ExecutionStore.TTL, o.config.ExecutionStore.ErrorTTL)
	if !state.Recorder.RecordLifecycle(observation, retention) {
		// Early failures may have produced neither a plan nor hook snapshot.
		// Store actual lifecycle without inventing a tool result or response.
		o.storeTerminalExecutionAsync(state.Context, state.Input.Request, state.Correlation.RequestID, nil, nil, nil)
	}
}
