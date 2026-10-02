package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type executionResourcesKey struct{}

// Private to one ProcessRequest segment, never serialized into metadata,
// checkpoints or prompts. Direct hook/ExecutePlan users own their lifecycle.
type executionResources struct {
	mu       sync.Mutex
	closed   bool
	leases   []ownedInvestigation
	activity *executionActivity
	timeout  time.Duration
	context  context.Context
	logger   core.Logger
}

type ownedInvestigation struct {
	coordinator core.InvestigationLeaseCoordinator
	lease       core.InvestigationLease
	reporter    core.PipelineHookEffectReporter
	effectID    string
	started     time.Time
}

func resourcesFromContext(ctx context.Context) *executionResources {
	resources, _ := ctx.Value(executionResourcesKey{}).(*executionResources)
	return resources
}

func (resources *executionResources) acquire(ctx context.Context, coordinator core.InvestigationLeaseCoordinator, agent, entity string) (bool, string, error) {
	resources.mu.Lock()
	defer resources.mu.Unlock()
	if resources.closed {
		return false, "", errors.New("execution resource scope is closed")
	}
	request := core.InvestigationLeaseRequest{
		AgentName: agent, EntityID: entity, AcquisitionID: uuid.NewString(),
	}
	lease, holder, err := coordinator.AcquireInvestigation(ctx, request)
	if err != nil || lease == nil {
		return false, holder, err
	}
	if lease.AgentName != agent || lease.EntityID != entity || lease.AcquisitionID != request.AcquisitionID || lease.ExpiresAt.IsZero() {
		return false, "", errors.New("investigation provider returned an invalid ownership handle")
	}
	reporter, _ := core.PipelineHookEffectReporterFromContext(ctx)
	entry := ownedInvestigation{coordinator: coordinator, lease: *lease, reporter: reporter,
		effectID: fmt.Sprintf("investigation_lease_cleanup:%d", len(resources.leases)), started: time.Now()}
	resources.leases = append(resources.leases, entry)
	reportStructuredPipelineHookEffect(ctx, entry.effectID, "Investigation lease cleanup", core.PipelineHookEffectPending,
		"Acquired an execution-owned claim; cleanup is pending", map[string]any{"entity_id": entity, "ownership": "acquired"}, nil, entry.started)
	return true, "", nil
}

func releaseOwnedInvestigation(ctx context.Context, entry ownedInvestigation) (outcome core.InvestigationReleaseOutcome, err error) {
	defer func() {
		if recover() != nil {
			outcome, err = core.InvestigationUnknown, errors.New("investigation provider panicked during cleanup")
		}
	}()
	return entry.coordinator.ReleaseInvestigationLease(ctx, entry.lease)
}

func (resources *executionResources) close(ctx context.Context, timeout time.Duration, logger core.Logger) {
	resources.mu.Lock()
	if resources.closed {
		resources.mu.Unlock()
		return
	}
	resources.closed = true
	leases := resources.leases
	activity := resources.activity
	resources.leases = nil
	resources.mu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if activity != nil {
		activity.close(cleanupCtx, logger)
	}
	if len(leases) == 0 {
		return
	}
	for _, entry := range leases {
		outcome, err := core.InvestigationUnknown, cleanupCtx.Err()
		if err == nil {
			outcome, err = releaseOwnedInvestigation(cleanupCtx, entry)
		}
		if err != nil {
			outcome = core.InvestigationUnknown
		}
		switch outcome {
		case core.InvestigationReleased, core.InvestigationAbsent, core.InvestigationNotOwned:
		case core.InvestigationUnknown:
			if err == nil {
				err = errors.New("investigation provider did not confirm cleanup")
			}
		default:
			outcome, err = core.InvestigationUnknown, errors.New("investigation provider returned an invalid release outcome")
		}
		status := core.PipelineHookEffectSucceeded
		if err != nil || outcome == core.InvestigationUnknown {
			status = core.PipelineHookEffectFailed
			if logger != nil {
				logger.WarnWithContext(cleanupCtx, "Investigation cleanup could not confirm release", map[string]interface{}{
					"operation": "investigation_cleanup", "error_type": "ownership_unknown", "request_id": GetRequestID(ctx),
				})
			}
		}
		telemetry.AddSpanEvent(cleanupCtx, "memory.investigation.cleanup", attribute.String("request_id", GetRequestID(ctx)), attribute.String("outcome", string(outcome)))
		effectCtx := core.WithPipelineHookEffectReporter(cleanupCtx, entry.reporter)
		reportStructuredPipelineHookEffect(effectCtx, entry.effectID, "Investigation lease cleanup", status,
			"Observed execution-owned investigation cleanup", map[string]any{"entity_id": entry.lease.EntityID, "outcome": outcome},
			err, entry.started)
	}
}
