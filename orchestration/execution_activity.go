package orchestration

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// One owner per request segment serializes status changes with TTL renewal.
// Calls are bounded; stop is joined before removal so renewal cannot resurrect
// a completed signal. Nil from a legacy fail-open provider means submitted,
// not confirmed durable storage or backend health.
type executionActivity struct {
	mu          sync.Mutex
	coordinator core.ActivityCoordinator
	signal      core.ActivitySignal
	cancel      context.CancelFunc
	done        chan struct{}
	timeout     time.Duration
	reporter    core.PipelineHookEffectReporter
	started     time.Time
	closed      bool
	renewals    int
	errors      int
	logger      core.Logger
}

func (resources *executionResources) announceActivity(ctx context.Context, coordinator core.ActivityCoordinator, signal core.ActivitySignal) error {
	resources.mu.Lock()
	defer resources.mu.Unlock()
	if resources.closed {
		return errors.New("execution resource scope is closed")
	}
	if resources.activity != nil {
		return nil // A repeated planning hook does not start another owner.
	}
	interval := max(signal.TTL/3, time.Nanosecond)
	timeout := min(resources.operationTimeout(), interval)
	parent := resources.context
	if parent == nil {
		parent = ctx
	}
	loopCtx, cancel := context.WithCancel(parent)
	reporter, _ := core.PipelineHookEffectReporterFromContext(ctx)
	owner := &executionActivity{coordinator: coordinator, signal: signal, cancel: cancel, done: make(chan struct{}), timeout: timeout, reporter: reporter, started: time.Now(), logger: resources.logger}
	resources.activity = owner
	for _, effectID := range []string{"activity_signal_renewal", "activity_signal_cleanup"} {
		reportStructuredPipelineHookEffect(ctx, effectID, "Execution-owned activity lifecycle", core.PipelineHookEffectPending,
			"Request owns renewal and joined cleanup", nil, nil, owner.started)
	}
	err := owner.publish(ctx, "")
	go owner.run(loopCtx, interval)
	return err
}

func (resources *executionResources) operationTimeout() time.Duration {
	if resources.timeout > 0 {
		return resources.timeout
	}
	return 5 * time.Second
}

func (owner *executionActivity) publish(ctx context.Context, status string) (err error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	if status != "" {
		owner.signal.Status = status
	}
	callCtx, cancel := context.WithTimeout(ctx, owner.timeout)
	defer cancel()
	// A provider panic must not escape the request-owned renewal goroutine.
	defer func() {
		if recover() != nil {
			err = errors.New("activity provider panicked during submission")
		}
		if err != nil {
			owner.errors++
		}
	}()
	return owner.coordinator.AnnounceActivity(callCtx, owner.signal)
}

func (owner *executionActivity) run(ctx context.Context, interval time.Duration) {
	defer close(owner.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := owner.publish(ctx, "")
			owner.mu.Lock()
			owner.renewals++
			renewals, failures := owner.renewals, owner.errors
			owner.mu.Unlock()
			if err != nil && owner.logger != nil {
				owner.logger.WarnWithContext(ctx, "Activity renewal submission failed", map[string]interface{}{"operation": "activity_renewal", "error_type": "activity_announce", "request_id": owner.signal.RequestID})
			}
			telemetry.AddSpanEvent(ctx, "activity.renewal.submitted", attribute.String("request_id", GetRequestID(ctx)), attribute.Bool("provider_error_observed", err != nil))
			reportStructuredPipelineHookEffect(core.WithPipelineHookEffectReporter(ctx, owner.reporter), "activity_signal_renewal", "Activity renewal", core.PipelineHookEffectPending,
				"Submitted TTL renewal; provider nil is not a backend-health assertion", map[string]any{"submissions": renewals, "provider_errors": failures}, err, owner.started)
		}
	}
}

func completeOwnedActivity(ctx context.Context, coordinator core.ActivityCoordinator, requestID string) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("activity provider panicked during removal")
		}
	}()
	return coordinator.CompleteActivity(ctx, requestID)
}

func (owner *executionActivity) close(cleanupCtx context.Context, logger core.Logger) {
	owner.cancel()
	<-owner.done // Calls are context-bounded; providers must honor cancellation.
	owner.mu.Lock()
	owner.closed = true
	renewals, failures := owner.renewals, owner.errors
	owner.mu.Unlock()
	renewalStatus := core.PipelineHookEffectSucceeded
	if failures > 0 {
		renewalStatus = core.PipelineHookEffectFailed
	}
	reportStructuredPipelineHookEffect(core.WithPipelineHookEffectReporter(cleanupCtx, owner.reporter), "activity_signal_renewal", "Activity renewal", renewalStatus,
		"Renewal loop joined; counters describe submissions, not backend health", map[string]any{"submissions": renewals, "provider_errors": failures}, nil, owner.started)
	err := completeOwnedActivity(cleanupCtx, owner.coordinator, owner.signal.RequestID)
	status := core.PipelineHookEffectSucceeded
	if err != nil {
		status = core.PipelineHookEffectFailed
		if logger != nil {
			logger.WarnWithContext(cleanupCtx, "Activity cleanup submission failed; TTL remains the fallback", map[string]interface{}{"operation": "activity_cleanup", "error_type": "activity_complete", "request_id": owner.signal.RequestID})
		}
	}
	telemetry.AddSpanEvent(cleanupCtx, "activity.cleanup.submitted", attribute.String("request_id", GetRequestID(cleanupCtx)), attribute.Bool("provider_error_observed", err != nil))
	reportStructuredPipelineHookEffect(core.WithPipelineHookEffectReporter(cleanupCtx, owner.reporter), "activity_signal_cleanup", "Activity signal cleanup", status,
		"Submitted removal after joining renewal; nil does not confirm backend health", map[string]any{"provider_error_observed": err != nil}, err, owner.started)
}

func managedActivity(ctx context.Context) *executionActivity {
	resources := resourcesFromContext(ctx)
	if resources == nil {
		return nil
	}
	resources.mu.Lock()
	defer resources.mu.Unlock()
	return resources.activity
}

func updateExecutionActivity(ctx context.Context, coordinator core.ActivityCoordinator, requestID, status string) error {
	if owner := managedActivity(ctx); owner != nil {
		return owner.publish(ctx, status)
	}
	return coordinator.UpdateStatus(ctx, requestID, status)
}
