package orchestration

import (
	"context"
	"testing"
)

type backendTestRunnable struct{}

func (*backendTestRunnable) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

type backendTestCheckpointPersistence struct{}

func (*backendTestCheckpointPersistence) SaveCheckpoint(context.Context, *ExecutionCheckpoint) error {
	return nil
}
func (*backendTestCheckpointPersistence) LoadCheckpoint(context.Context, string) (*ExecutionCheckpoint, error) {
	return nil, nil
}
func (*backendTestCheckpointPersistence) UpdateCheckpointStatus(context.Context, string, CheckpointStatus) error {
	return nil
}
func (*backendTestCheckpointPersistence) ListPendingCheckpoints(context.Context, CheckpointFilter) ([]*ExecutionCheckpoint, error) {
	return nil, nil
}
func (*backendTestCheckpointPersistence) DeleteCheckpoint(context.Context, string) error { return nil }

type backendTestExpiredCheckpointSource struct{}

func (*backendTestExpiredCheckpointSource) ClaimExpiredCheckpoints(context.Context, ExpiredCheckpointClaimRequest) ([]*ExecutionCheckpoint, error) {
	return nil, nil
}
func (*backendTestExpiredCheckpointSource) ReleaseExpiredCheckpointClaim(context.Context, string, string) error {
	return nil
}

func TestBackendRequirementsRejectUnknownAndDuplicateCapabilities(t *testing.T) {
	if _, err := NewBackendRequirements(BackendExecutionDebug, BackendExecutionDebug); err == nil {
		t.Fatal("duplicate capability was accepted")
	}
	if _, err := NewBackendRequirements(BackendCapability("unknown")); err == nil {
		t.Fatal("unknown capability was accepted")
	}
}

func TestRequirementsForFeaturesUsesEffectiveDebugAndExplicitLifecycleFeatures(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	config.ExecutionStore.Enabled = true
	config.LLMDebug.Enabled = true
	requirements, err := RequirementsForFeatures(config, BackendFeatureCrossInstanceHITL, BackendFeatureCheckpointExpiry)
	if err != nil {
		t.Fatal(err)
	}
	want := []BackendCapability{BackendCheckpointExpiry, BackendCheckpointExpiryProcessor, BackendCheckpoints, BackendCommands, BackendExecutionDebug, BackendLLMDebug}
	got := requirements.Capabilities()
	if len(got) != len(want) {
		t.Fatalf("capabilities = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("capabilities = %#v, want %#v", got, want)
		}
	}
}

func TestSchedulerProducerRequirementsMatchConstructorDependencies(t *testing.T) {
	requirements, err := RequirementsForFeatures(nil, BackendFeatureSchedulerProducer)
	if err != nil {
		t.Fatal(err)
	}
	want := []BackendCapability{BackendLock, BackendSchedules, BackendTaskDispatcher, BackendTasks}
	got := requirements.Capabilities()
	if len(got) != len(want) {
		t.Fatalf("capabilities = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("capabilities = %#v, want %#v", got, want)
		}
	}
}

func TestOrchestrationBackendsWithIsImmutableAndOverrideWins(t *testing.T) {
	first := &orderedExecutionStore{}
	second := &orderedExecutionStore{}
	runnable := &backendTestRunnable{}
	base, err := NewOrchestrationBackends(WithExecutionBackend(first), WithRunnables(runnable))
	if err != nil {
		t.Fatal(err)
	}
	overridden, err := base.With(WithExecutionBackend(second))
	if err != nil {
		t.Fatal(err)
	}
	if base.Execution() != first || overridden.Execution() != second {
		t.Fatalf("base/override execution = %p / %p", base.Execution(), overridden.Execution())
	}
	runnables := overridden.Runnables()
	if len(runnables) != 1 || runnables[0] != runnable {
		t.Fatalf("runnables = %#v", runnables)
	}
	runnables[0] = nil
	if overridden.Runnables()[0] == nil {
		t.Fatal("Runnables returned mutable aggregate state")
	}
}

func TestOrchestrationBackendsRunnablesComposeAdditively(t *testing.T) {
	first := &backendTestRunnable{}
	second := &backendTestRunnable{}
	base, err := NewOrchestrationBackends(WithRunnables(first))
	if err != nil {
		t.Fatal(err)
	}
	composed, err := base.With(WithRunnables(second))
	if err != nil {
		t.Fatal(err)
	}
	if got := base.Runnables(); len(got) != 1 || got[0] != first {
		t.Fatalf("base runnables = %#v", got)
	}
	if got := composed.Runnables(); len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("composed runnables = %#v", got)
	}
}

func TestCheckpointExpiryCapabilityRequiresCoherentProcessor(t *testing.T) {
	requirements, err := RequirementsForFeatures(nil, BackendFeatureCheckpointExpiry)
	if err != nil {
		t.Fatal(err)
	}
	persistence := &backendTestCheckpointPersistence{}
	source := &backendTestExpiredCheckpointSource{}
	backends, err := NewOrchestrationBackends(
		WithCheckpointPersistence(persistence),
		WithCheckpointExpiry(source),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := backends.ValidateFor(requirements); err == nil {
		t.Fatal("checkpoint expiry without a runnable processor was accepted")
	}
	processor := &backendTestRunnable{}
	backends, err = backends.With(WithCheckpointExpiryProcessor(processor))
	if err != nil {
		t.Fatal(err)
	}
	if err := backends.ValidateFor(requirements); err != nil {
		t.Fatalf("complete checkpoint expiry composition was rejected: %v", err)
	}
	if got := backends.Runnables(); len(got) != 1 || got[0] != processor {
		t.Fatalf("checkpoint expiry runnables = %#v", got)
	}

	overridden, err := backends.With(WithCheckpointPersistence(&backendTestCheckpointPersistence{}))
	if err != nil {
		t.Fatal(err)
	}
	if overridden.CheckpointExpiryProcessor() != nil {
		t.Fatal("checkpoint persistence override retained a processor bound to the old store")
	}
	if err := overridden.ValidateFor(requirements); err == nil {
		t.Fatal("checkpoint dependency override retained a stale expiry capability")
	}
}

func TestCheckpointExpiryProcessorMustFollowDependencyOptions(t *testing.T) {
	_, err := NewOrchestrationBackends(
		WithCheckpointExpiryProcessor(&backendTestRunnable{}),
		WithCheckpointPersistence(&backendTestCheckpointPersistence{}),
	)
	if err == nil {
		t.Fatal("processor followed by a checkpoint dependency was accepted")
	}

	backends, err := NewOrchestrationBackends(
		WithCheckpointPersistence(&backendTestCheckpointPersistence{}),
		WithCheckpointExpiry(&backendTestExpiredCheckpointSource{}),
		WithCheckpointExpiryProcessor(&backendTestRunnable{}),
	)
	if err != nil {
		t.Fatalf("dependency-first checkpoint composition failed: %v", err)
	}
	if backends.CheckpointExpiryProcessor() == nil {
		t.Fatal("dependency-first checkpoint composition lost its processor")
	}
}

func TestHITLRequestPathAcceptsPersistenceOnlyBackend(t *testing.T) {
	persistence := &backendTestCheckpointPersistence{}
	controller := NewInterruptController(nil, persistence, nil)
	controller.SetCheckpointPersistence(persistence)
	handler := NewHITLHandler(controller, persistence)
	if controller.store != persistence || handler.store != persistence {
		t.Fatal("HITL request path did not retain the narrow checkpoint persistence backend")
	}
}

func TestOrchestrationBackendsValidationAndTypedNil(t *testing.T) {
	var typedNil *orderedExecutionStore
	if _, err := NewOrchestrationBackends(WithExecutionBackend(typedNil)); err == nil {
		t.Fatal("typed-nil backend was accepted")
	}
	backends, err := NewOrchestrationBackends()
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := NewBackendRequirements(BackendExecutionDebug, BackendCommands)
	if err != nil {
		t.Fatal(err)
	}
	if err := backends.ValidateFor(requirements); err == nil {
		t.Fatal("missing required capabilities were accepted")
	}
}
