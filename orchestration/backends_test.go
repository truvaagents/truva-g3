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
	want := []BackendCapability{BackendCheckpointExpiry, BackendCheckpoints, BackendCommands, BackendExecutionDebug, BackendLLMDebug}
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
