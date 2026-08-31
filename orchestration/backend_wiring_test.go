package orchestration

import "testing"

func TestWireOrchestratorBackendsFillsOnlyEnabledMissingDependencies(t *testing.T) {
	execution := &orderedExecutionStore{}
	debug := &mockLLMDebugStore{}
	skills := &skillContractTestBackend{}
	backends, err := NewOrchestrationBackends(
		WithExecutionBackend(execution),
		WithLLMDebugBackend(debug),
		WithSkillRegistryBackend(skills),
	)
	if err != nil {
		t.Fatal(err)
	}
	config := NewDefaultOrchestratorConfig()
	config.ExecutionStore.Enabled = true
	config.LLMDebug.Enabled = true
	config.Skills.Enabled = true
	config.Skills.Bindings = []SkillBinding{{
		Namespace: "test", Name: "skill", Version: "published", Activation: SkillActivationAlways,
	}}
	if err := WireOrchestratorBackends(config, backends); err != nil {
		t.Fatal(err)
	}
	if config.ExecutionStoreBackend != execution || config.LLMDebugStore != debug || config.SkillRegistry != skills {
		t.Fatal("orchestrator backend wiring did not populate enabled dependencies")
	}
}

func TestWireOrchestratorBackendsPreservesExplicitDependenciesAndIsAtomic(t *testing.T) {
	explicit := &orderedExecutionStore{}
	config := NewDefaultOrchestratorConfig()
	config.ExecutionStore.Enabled = true
	config.ExecutionStoreBackend = explicit
	config.LLMDebug.Enabled = true

	backends, err := NewOrchestrationBackends(WithExecutionBackend(&orderedExecutionStore{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := WireOrchestratorBackends(config, backends); err == nil {
		t.Fatal("incomplete composition was accepted")
	}
	if config.ExecutionStoreBackend != explicit || config.LLMDebugStore != nil {
		t.Fatal("failed wiring partially mutated the orchestrator config")
	}
}

func TestWireOrchestratorBackendsRejectsNilInputs(t *testing.T) {
	backends, err := NewOrchestrationBackends()
	if err != nil {
		t.Fatal(err)
	}
	if err := WireOrchestratorBackends(nil, backends); err == nil {
		t.Fatal("nil orchestrator config was accepted")
	}
	if err := WireOrchestratorBackends(NewDefaultOrchestratorConfig(), nil); err == nil {
		t.Fatal("nil backend composition was accepted")
	}
}
