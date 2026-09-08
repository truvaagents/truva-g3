package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrInvalidPipelineHookEffect identifies an effect envelope rejected by the
// active reporter before it can enter execution-debug persistence.
var ErrInvalidPipelineHookEffect = errors.New("invalid pipeline hook effect")

// PipelineHookEffectStatus describes the lifecycle of one concrete effect
// attempted by a pipeline hook. It is deliberately separate from the hook
// invocation outcome: a hook may return successfully while a fail-open effect
// fails or asynchronous work is still pending.
type PipelineHookEffectStatus string

const (
	PipelineHookEffectPending   PipelineHookEffectStatus = "pending"
	PipelineHookEffectSucceeded PipelineHookEffectStatus = "succeeded"
	PipelineHookEffectPartial   PipelineHookEffectStatus = "partial"
	PipelineHookEffectFailed    PipelineHookEffectStatus = "failed"
	PipelineHookEffectSkipped   PipelineHookEffectStatus = "skipped"
)

// PipelineHookEffect is provider-neutral, exact-fidelity troubleshooting
// evidence reported by a pipeline hook. Status is the producer's observation,
// not an independent durability attestation. EffectID is stable within one hook
// invocation; reporting the same ID again replaces the prior state, which lets
// asynchronous effects move from pending to a terminal status. A hook must
// announce a pending asynchronous effect before its invocation returns.
//
// Data is raw JSON so a hook chooses and versions its own structured payload
// without widening the core contract. The framework neither redacts nor
// truncates Data or Error. Applications must enable and govern execution-debug
// persistence accordingly.
type PipelineHookEffect struct {
	EffectID      string                   `json:"effect_id"`
	SchemaVersion int                      `json:"schema_version"`
	Name          string                   `json:"name"`
	Status        PipelineHookEffectStatus `json:"status"`
	Summary       string                   `json:"summary,omitempty"`
	Data          json.RawMessage          `json:"data,omitempty"`
	Error         string                   `json:"error,omitempty"`
	StartedAt     time.Time                `json:"started_at,omitempty"`
	Duration      time.Duration            `json:"duration,omitempty"`
}

// PipelineHookEffectReporter is the typed request-scoped extension seam used
// by hooks to attach effect evidence to their current invocation. Reporting is
// diagnostic and must not alter the hook's business result.
type PipelineHookEffectReporter interface {
	ReportPipelineHookEffect(effect PipelineHookEffect) error
}

type pipelineHookEffectReporterContextKey struct{}

// WithPipelineHookEffectReporter attaches an invocation-bound reporter to a
// hook context. Orchestration owns the reporter lifecycle; custom pipeline
// runners may provide their own implementation.
func WithPipelineHookEffectReporter(
	ctx context.Context,
	reporter PipelineHookEffectReporter,
) context.Context {
	if ctx == nil || reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, pipelineHookEffectReporterContextKey{}, reporter)
}

// PipelineHookEffectReporterFromContext returns the invocation-bound reporter,
// when effect capture is enabled for this request.
func PipelineHookEffectReporterFromContext(
	ctx context.Context,
) (PipelineHookEffectReporter, bool) {
	if ctx == nil {
		return nil, false
	}
	reporter, ok := ctx.Value(pipelineHookEffectReporterContextKey{}).(PipelineHookEffectReporter)
	return reporter, ok && reporter != nil
}

// ReportPipelineHookEffect reports effect evidence when the current hook
// runner supplied a reporter. It is a no-op when execution debugging is not
// enabled, so hooks do not need storage-specific conditionals.
func ReportPipelineHookEffect(ctx context.Context, effect PipelineHookEffect) error {
	reporter, ok := PipelineHookEffectReporterFromContext(ctx)
	if !ok {
		return nil
	}
	return reporter.ReportPipelineHookEffect(effect)
}
