package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type pipelineHookCaptureExecutionStore struct {
	*NoOpExecutionStore
	records chan *StoredExecution
}

func TestTerminalExecutionSnapshotIsRewrittenWhenAsyncEffectCompletes(t *testing.T) {
	store := &pipelineHookCaptureExecutionStore{
		NoOpExecutionStore: NewNoOpExecutionStore(),
		records:            make(chan *StoredExecution, 2),
	}
	config := NewDefaultOrchestratorConfig()
	config.EnableTelemetry = false
	orchestrator := NewAIOrchestrator(config, nil, nil)
	orchestrator.SetExecutionStore(store)
	holder := newPipelineHookExecutionHolder(&orchestrator.executionWg)
	ctx := withPipelineHookExecutionHolder(t.Context(), holder)
	invocation := beginPipelineHookInvocation(
		ctx, "async-hook", PipelineHookPhaseAfterSynthesis, 1, 0, time.Now(),
	)
	hookCtx := invocation.Context(ctx)
	effectStart := time.Now()
	if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{
		EffectID: "background_write", Name: "Background write",
		Status: core.PipelineHookEffectPending, StartedAt: effectStart,
	}); err != nil {
		t.Fatalf("report pending effect: %v", err)
	}
	invocation.Complete(PipelineHookSucceeded, nil)
	orchestrator.storeExecutionWithFinalResponseAsync(
		ctx, "request", "request-id", nil,
		&ExecutionResult{Success: true}, "response",
	)

	initial := <-store.records
	if got := initial.PipelineHooks[0].Effects[0].Status; got != core.PipelineHookEffectPending {
		t.Fatalf("initial effect status = %q", got)
	}
	finalData := json.RawMessage(`{"stored_value":"exact"}`)
	if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{
		EffectID: "background_write", Name: "Background write",
		Status: core.PipelineHookEffectSucceeded, StartedAt: effectStart,
		Data: finalData,
	}); err != nil {
		t.Fatalf("report completed effect: %v", err)
	}

	updated := <-store.records
	if got := updated.PipelineHooks[0].Effects[0]; got.Status != core.PipelineHookEffectSucceeded || string(got.Data) != string(finalData) {
		t.Fatalf("updated effect = %#v", got)
	}
	orchestrator.executionWg.Wait()
}

func TestFailedRequestFallbackIsRewrittenWhenAsyncEffectCompletes(t *testing.T) {
	store := &pipelineHookCaptureExecutionStore{
		NoOpExecutionStore: NewNoOpExecutionStore(),
		records:            make(chan *StoredExecution, 2),
	}
	config := NewDefaultOrchestratorConfig()
	config.EnableTelemetry = false
	orchestrator := NewAIOrchestrator(config, nil, nil)
	orchestrator.SetExecutionStore(store)
	holder := newPipelineHookExecutionHolder(&orchestrator.executionWg)
	ctx := withPipelineHookExecutionHolder(t.Context(), holder)
	invocation := beginPipelineHookInvocation(
		ctx, "async-hook", PipelineHookPhaseBeforePlanning, 1, 0, time.Now(),
	)
	hookCtx := invocation.Context(ctx)
	if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{
		EffectID: "background_write", Name: "Background write",
		Status: core.PipelineHookEffectPending,
	}); err != nil {
		t.Fatalf("report pending effect: %v", err)
	}
	invocation.Complete(PipelineHookSucceeded, nil)
	orchestrator.ensureTerminalPipelineHookSnapshot(&executionRunState{
		Input:       requestRunInput{Request: "request"},
		Context:     ctx,
		Correlation: requestCorrelation{RequestID: "failed-request-id"},
	}, errors.New("request failed before producing an execution result"))

	initial := <-store.records
	if got := initial.PipelineHooks[0].Effects[0].Status; got != core.PipelineHookEffectPending {
		t.Fatalf("initial effect status = %q", got)
	}
	if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{
		EffectID: "background_write", Name: "Background write",
		Status: core.PipelineHookEffectFailed, Error: "provider unavailable",
	}); err != nil {
		t.Fatalf("report completed effect: %v", err)
	}

	updated := <-store.records
	got := updated.PipelineHooks[0].Effects[0]
	if got.Status != core.PipelineHookEffectFailed || got.Error != "provider unavailable" {
		t.Fatalf("updated effect = %#v", got)
	}
	orchestrator.executionWg.Wait()
}

func TestOrderedPipelineHookExecutionPublisherRejectsStaleRevisions(t *testing.T) {
	var published []string
	publisher := newOrderedPipelineHookExecutionPublisher(
		1,
		func(executions []PipelineHookExecution) {
			published = append(published, executions[0].HookName)
		},
	)
	publisher(3, []PipelineHookExecution{{HookName: "newer"}})
	publisher(2, []PipelineHookExecution{{HookName: "stale"}})
	publisher(3, []PipelineHookExecution{{HookName: "duplicate"}})
	publisher(4, []PipelineHookExecution{{HookName: "newest"}})

	if want := []string{"newer", "newest"}; !reflect.DeepEqual(published, want) {
		t.Fatalf("published snapshots = %#v, want %#v", published, want)
	}
}

func TestPipelineHookEffectValidationAndDefensiveSnapshots(t *testing.T) {
	holder := newPipelineHookExecutionHolder()
	ctx := withPipelineHookExecutionHolder(t.Context(), holder)
	invocation := beginPipelineHookInvocation(
		ctx, "custom", PipelineHookPhaseBeforePlanning, 1, 0, time.Now(),
	)
	hookCtx := invocation.Context(ctx)
	if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{
		EffectID: "invalid", Name: "Invalid", Status: core.PipelineHookEffectSucceeded,
		Data: []byte(`not-json`),
	}); !errors.Is(err, core.ErrInvalidPipelineHookEffect) {
		t.Fatal("invalid JSON effect was accepted")
	}
	if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{
		EffectID: "invalid-version", SchemaVersion: -1,
		Name: "Invalid version", Status: core.PipelineHookEffectSucceeded,
	}); !errors.Is(err, core.ErrInvalidPipelineHookEffect) {
		t.Fatal("negative schema version was accepted")
	}
	payload := json.RawMessage(`{"secret":"preserved exactly"}`)
	if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{
		EffectID: "valid", Name: "Valid", Status: core.PipelineHookEffectSucceeded,
		Data: payload,
	}); err != nil {
		t.Fatalf("valid effect rejected: %v", err)
	}
	invocation.Complete(PipelineHookSucceeded, nil)
	payload[2] = 'X'

	snapshot := holder.Snapshot()
	if got := string(snapshot[0].Effects[0].Data); got != `{"secret":"preserved exactly"}` {
		t.Fatalf("holder retained caller-owned effect bytes: %q", got)
	}
	snapshot[0].Effects[0].Data[2] = 'Y'
	if got := string(holder.Snapshot()[0].Effects[0].Data); got != `{"secret":"preserved exactly"}` {
		t.Fatalf("Snapshot returned holder-owned effect bytes: %q", got)
	}
	if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{
		EffectID: "late", Name: "Late effect", Status: core.PipelineHookEffectPending,
	}); !errors.Is(err, core.ErrInvalidPipelineHookEffect) {
		t.Fatal("new effect reported after invocation completion was accepted")
	}
	if err := core.ReportPipelineHookEffect(hookCtx, core.PipelineHookEffect{
		EffectID: "valid", Name: "Valid", Status: core.PipelineHookEffectPending,
	}); !errors.Is(err, core.ErrInvalidPipelineHookEffect) {
		t.Fatal("terminal effect was allowed to return to pending")
	}
}

func (store *pipelineHookCaptureExecutionStore) Store(
	_ context.Context,
	execution *StoredExecution,
) error {
	store.records <- execution
	return nil
}

func TestExecutionSnapshotCarriesRequestLocalPipelineHookDiagnostics(t *testing.T) {
	store := &pipelineHookCaptureExecutionStore{
		NoOpExecutionStore: NewNoOpExecutionStore(),
		records:            make(chan *StoredExecution, 1),
	}
	config := NewDefaultOrchestratorConfig()
	config.EnableTelemetry = false
	orchestrator := NewAIOrchestrator(config, nil, nil)
	orchestrator.SetExecutionStore(store)
	holder := newPipelineHookExecutionHolder()
	ctx := withPipelineHookExecutionHolder(t.Context(), holder)
	startedAt := time.Now()
	recordPipelineHookExecution(
		ctx,
		"memory-record",
		PipelineHookPhaseAfterExecution,
		PipelineHookSucceeded,
		1,
		0,
		startedAt,
		nil,
	)

	orchestrator.storeExecutionAsync(ctx, "request", "request-id", nil, nil, nil)
	select {
	case record := <-store.records:
		if len(record.PipelineHooks) != 1 {
			t.Fatalf("stored hook diagnostics = %#v, want one", record.PipelineHooks)
		}
		hook := record.PipelineHooks[0]
		if hook.HookName != "memory-record" ||
			hook.Phase != PipelineHookPhaseAfterExecution ||
			hook.Status != PipelineHookSucceeded {
			t.Fatalf("stored hook diagnostic = %#v", hook)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("execution store did not receive snapshot")
	}
	orchestrator.executionWg.Wait()
}
