package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type pipelineHookExecutionHolderContextKey struct{}

type pipelineHookExecutionPublisher func(uint64, []PipelineHookExecution)

// newOrderedPipelineHookExecutionPublisher prevents a slower, older effect
// report from being queued after a newer report when detached effects finish
// concurrently. The execution recorder serializes writes in submission order;
// this revision gate makes that submission order monotonic as well.
func newOrderedPipelineHookExecutionPublisher(
	initialRevision uint64,
	publish func([]PipelineHookExecution),
) pipelineHookExecutionPublisher {
	var mu sync.Mutex
	latestRevision := initialRevision
	return func(revision uint64, executions []PipelineHookExecution) {
		if publish == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if revision <= latestRevision {
			return
		}
		publish(executions)
		latestRevision = revision
	}
}

// pipelineHookExecutionHolder is request-local. Hook invocations are currently
// sequential, while asynchronous effects and execution-record snapshots may
// run concurrently, so every state transition is protected by the holder.
type pipelineHookExecutionHolder struct {
	mu                sync.RWMutex
	executions        []PipelineHookExecution
	revision          uint64
	publisher         pipelineHookExecutionPublisher
	pending           map[string]bool
	awaitingPublisher int
	wg                *sync.WaitGroup
}

// pipelineHookInvocation is bound to exactly one stored hook invocation. It is
// also the core-facing effect reporter injected into that hook's context.
type pipelineHookInvocation struct {
	holder    *pipelineHookExecutionHolder
	index     int
	startedAt time.Time
}

func newPipelineHookExecutionHolder(waitGroups ...*sync.WaitGroup) *pipelineHookExecutionHolder {
	holder := &pipelineHookExecutionHolder{pending: make(map[string]bool)}
	if len(waitGroups) > 0 {
		holder.wg = waitGroups[0]
	}
	return holder
}

func withPipelineHookExecutionHolder(
	ctx context.Context,
	holder *pipelineHookExecutionHolder,
) context.Context {
	return context.WithValue(ctx, pipelineHookExecutionHolderContextKey{}, holder)
}

func pipelineHookExecutionHolderFromContext(
	ctx context.Context,
) (*pipelineHookExecutionHolder, bool) {
	if ctx == nil {
		return nil, false
	}
	holder, ok := ctx.Value(pipelineHookExecutionHolderContextKey{}).(*pipelineHookExecutionHolder)
	return holder, ok && holder != nil
}

func beginPipelineHookInvocation(
	ctx context.Context,
	hookName string,
	phase string,
	sequence int,
	planPhase int,
	startedAt time.Time,
) *pipelineHookInvocation {
	holder, ok := pipelineHookExecutionHolderFromContext(ctx)
	if !ok {
		return nil
	}
	holder.mu.Lock()
	holder.executions = append(holder.executions, PipelineHookExecution{
		HookName:  hookName,
		Phase:     phase,
		Sequence:  sequence,
		PlanPhase: planPhase,
		StartedAt: startedAt.UTC(),
	})
	index := len(holder.executions) - 1
	holder.revision++
	holder.mu.Unlock()
	return &pipelineHookInvocation{holder: holder, index: index, startedAt: startedAt}
}

func (invocation *pipelineHookInvocation) Context(ctx context.Context) context.Context {
	if invocation == nil {
		return ctx
	}
	return core.WithPipelineHookEffectReporter(ctx, invocation)
}

func (invocation *pipelineHookInvocation) Complete(
	status PipelineHookExecutionStatus,
	err error,
) {
	if invocation == nil || invocation.holder == nil {
		return
	}
	holder := invocation.holder
	holder.mu.Lock()
	if invocation.index < 0 || invocation.index >= len(holder.executions) {
		holder.mu.Unlock()
		return
	}
	execution := &holder.executions[invocation.index]
	execution.Status = status
	execution.Duration = time.Since(invocation.startedAt)
	if execution.Duration < 0 {
		execution.Duration = 0
	}
	if err != nil {
		execution.Error = err.Error()
	} else {
		execution.Error = ""
	}
	holder.revision++
	revision := holder.revision
	publisher, snapshot := holder.publisherSnapshotLocked()
	holder.mu.Unlock()
	if publisher != nil {
		publisher(revision, snapshot)
	}
}

func (invocation *pipelineHookInvocation) ReportPipelineHookEffect(
	effect core.PipelineHookEffect,
) error {
	if invocation == nil || invocation.holder == nil {
		return nil
	}
	if effect.SchemaVersion == 0 {
		effect.SchemaVersion = 1
	}
	if err := validatePipelineHookEffect(effect); err != nil {
		return err
	}
	effect.Data = bytes.Clone(effect.Data)
	startedAtProvided := !effect.StartedAt.IsZero()
	if startedAtProvided {
		effect.StartedAt = effect.StartedAt.UTC()
	} else {
		effect.StartedAt = time.Now().UTC()
	}
	if effect.Duration < 0 {
		effect.Duration = 0
	}

	holder := invocation.holder
	holder.mu.Lock()
	if invocation.index < 0 || invocation.index >= len(holder.executions) {
		holder.mu.Unlock()
		return fmt.Errorf("%w: invocation is no longer available", core.ErrInvalidPipelineHookEffect)
	}
	execution := &holder.executions[invocation.index]
	effects := execution.Effects
	pendingKey := fmt.Sprintf("%d:%s", invocation.index, effect.EffectID)
	wasPending := holder.pending[pendingKey]
	existingIndex := -1
	for index := range effects {
		if effects[index].EffectID == effect.EffectID {
			existingIndex = index
			break
		}
	}
	if existingIndex < 0 && execution.Status != "" {
		holder.mu.Unlock()
		return fmt.Errorf(
			"%w: new effect %q reported after invocation completed",
			core.ErrInvalidPipelineHookEffect,
			effect.EffectID,
		)
	}
	if existingIndex >= 0 && effects[existingIndex].Status != core.PipelineHookEffectPending &&
		effect.Status == core.PipelineHookEffectPending {
		holder.mu.Unlock()
		return fmt.Errorf(
			"%w: terminal effect %q cannot return to pending",
			core.ErrInvalidPipelineHookEffect,
			effect.EffectID,
		)
	}
	if existingIndex >= 0 {
		if !startedAtProvided {
			effect.StartedAt = effects[existingIndex].StartedAt
		}
		effects[existingIndex] = effect
	} else {
		execution.Effects = append(effects, effect)
	}
	isPending := effect.Status == core.PipelineHookEffectPending
	releasePending := false
	if isPending && !wasPending {
		holder.pending[pendingKey] = true
		if holder.wg != nil {
			holder.wg.Add(1)
		}
	} else if !isPending && wasPending {
		delete(holder.pending, pendingKey)
		if holder.publisher == nil {
			holder.awaitingPublisher++
		} else {
			releasePending = true
		}
	}
	holder.revision++
	revision := holder.revision
	publisher, snapshot := holder.publisherSnapshotLocked()
	holder.mu.Unlock()
	if publisher != nil {
		publisher(revision, snapshot)
	}
	if releasePending && holder.wg != nil {
		holder.wg.Done()
	}
	return nil
}

func validatePipelineHookEffect(effect core.PipelineHookEffect) error {
	if effect.EffectID == "" {
		return fmt.Errorf("%w: effect_id is required", core.ErrInvalidPipelineHookEffect)
	}
	if effect.Name == "" {
		return fmt.Errorf("%w: name is required", core.ErrInvalidPipelineHookEffect)
	}
	if effect.SchemaVersion <= 0 {
		return fmt.Errorf("%w: schema_version must be positive", core.ErrInvalidPipelineHookEffect)
	}
	switch effect.Status {
	case core.PipelineHookEffectPending,
		core.PipelineHookEffectSucceeded,
		core.PipelineHookEffectPartial,
		core.PipelineHookEffectFailed,
		core.PipelineHookEffectSkipped:
	default:
		return fmt.Errorf("%w: unsupported status %q", core.ErrInvalidPipelineHookEffect, effect.Status)
	}
	if len(effect.Data) > 0 && !json.Valid(effect.Data) {
		return fmt.Errorf("%w: data must be valid JSON", core.ErrInvalidPipelineHookEffect)
	}
	return nil
}

func (holder *pipelineHookExecutionHolder) Append(execution PipelineHookExecution) {
	if holder == nil {
		return
	}
	holder.mu.Lock()
	holder.executions = append(holder.executions, clonePipelineHookExecution(execution))
	holder.revision++
	revision := holder.revision
	publisher, snapshot := holder.publisherSnapshotLocked()
	holder.mu.Unlock()
	if publisher != nil {
		publisher(revision, snapshot)
	}
}

func (holder *pipelineHookExecutionHolder) Snapshot() []PipelineHookExecution {
	snapshot, _ := holder.SnapshotWithRevision()
	return snapshot
}

func (holder *pipelineHookExecutionHolder) SnapshotWithRevision() ([]PipelineHookExecution, uint64) {
	if holder == nil {
		return nil, 0
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return clonePipelineHookExecutions(holder.executions), holder.revision
}

// SetTerminalPublisher registers the provider-neutral rewrite used when a
// detached effect completes after the terminal execution snapshot. If a report
// raced with terminal snapshot capture, the newer state is published once.
func (holder *pipelineHookExecutionHolder) SetTerminalPublisher(
	capturedRevision uint64,
	publisher pipelineHookExecutionPublisher,
) {
	if holder == nil || publisher == nil {
		return
	}
	holder.mu.Lock()
	holder.publisher = publisher
	var snapshot []PipelineHookExecution
	revision := holder.revision
	if holder.revision > capturedRevision {
		snapshot = clonePipelineHookExecutions(holder.executions)
	}
	pendingReleases := holder.awaitingPublisher
	holder.awaitingPublisher = 0
	holder.mu.Unlock()
	if snapshot != nil {
		publisher(revision, snapshot)
	}
	if holder.wg != nil {
		for range pendingReleases {
			holder.wg.Done()
		}
	}
}

func (holder *pipelineHookExecutionHolder) HasTerminalPublisher() bool {
	if holder == nil {
		return false
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return holder.publisher != nil
}

func (holder *pipelineHookExecutionHolder) HasExecutions() bool {
	if holder == nil {
		return false
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return len(holder.executions) > 0
}

func (holder *pipelineHookExecutionHolder) publisherSnapshotLocked() (
	pipelineHookExecutionPublisher,
	[]PipelineHookExecution,
) {
	if holder.publisher == nil {
		return nil, nil
	}
	return holder.publisher, clonePipelineHookExecutions(holder.executions)
}

func clonePipelineHookExecutions(source []PipelineHookExecution) []PipelineHookExecution {
	if len(source) == 0 {
		return nil
	}
	cloned := make([]PipelineHookExecution, len(source))
	for index := range source {
		cloned[index] = clonePipelineHookExecution(source[index])
	}
	return cloned
}

func clonePipelineHookExecution(source PipelineHookExecution) PipelineHookExecution {
	cloned := source
	if len(source.Effects) > 0 {
		cloned.Effects = make([]core.PipelineHookEffect, len(source.Effects))
		for index := range source.Effects {
			cloned.Effects[index] = source.Effects[index]
			cloned.Effects[index].Data = bytes.Clone(source.Effects[index].Data)
		}
	}
	return cloned
}

func recordPipelineHookExecution(
	ctx context.Context,
	hookName string,
	phase string,
	status PipelineHookExecutionStatus,
	sequence int,
	planPhase int,
	startedAt time.Time,
	err error,
) {
	invocation := beginPipelineHookInvocation(
		ctx, hookName, phase, sequence, planPhase, startedAt,
	)
	invocation.Complete(status, err)
}
