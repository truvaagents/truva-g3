package orchestration

import "fmt"

// WireOrchestratorBackends fills enabled orchestrator storage dependencies from
// a backend composition. Explicit dependencies already present on config win;
// the helper validates only the still-missing capabilities and mutates config
// only after validation succeeds.
func WireOrchestratorBackends(config *OrchestratorConfig, backends *OrchestrationBackends) error {
	if config == nil {
		return fmt.Errorf("orchestration: orchestrator config is nil")
	}
	if backends == nil {
		return fmt.Errorf("orchestration: backend composition is nil")
	}

	required := make([]BackendCapability, 0, 3)
	if config.ExecutionStore.Enabled && isNilBackendValue(config.ExecutionStoreBackend) {
		required = append(required, BackendExecutionDebug)
	}
	if config.LLMDebug.Enabled && isNilBackendValue(config.LLMDebugStore) {
		required = append(required, BackendLLMDebug)
	}
	if config.Skills.Enabled && len(config.Skills.Bindings) > 0 && isNilBackendValue(config.SkillRegistry) {
		required = append(required, BackendSkillRegistry)
	}
	requirements, err := NewBackendRequirements(required...)
	if err != nil {
		return err
	}
	if err := backends.ValidateFor(requirements); err != nil {
		return err
	}

	if config.ExecutionStore.Enabled && isNilBackendValue(config.ExecutionStoreBackend) {
		config.ExecutionStoreBackend = backends.Execution()
	}
	if config.LLMDebug.Enabled && isNilBackendValue(config.LLMDebugStore) {
		config.LLMDebugStore = backends.LLMDebug()
	}
	if config.Skills.Enabled && len(config.Skills.Bindings) > 0 && isNilBackendValue(config.SkillRegistry) {
		config.SkillRegistry = backends.SkillRegistry()
	}
	return nil
}

// SkillAdministrationDependencies validates and unpacks the complete skill
// control-plane backend set for NewSkillAdminHandler. Ordinary agent execution
// should continue to consume only SkillRegistry.
func (b *OrchestrationBackends) SkillAdministrationDependencies() (SkillAdminHandlerDependencies, error) {
	requirements, err := RequirementsForFeatures(nil, BackendFeatureSkillsAdministration)
	if err != nil {
		return SkillAdminHandlerDependencies{}, err
	}
	if err := b.ValidateFor(requirements); err != nil {
		return SkillAdminHandlerDependencies{}, err
	}
	return SkillAdminHandlerDependencies{
		Registry:       b.SkillRegistry(),
		RevisionReader: b.SkillRevisionReader(),
		Administration: b.SkillAdministrationStore(),
		Deletions:      b.SkillRevisionDeletionStore(),
		Audit:          b.SkillAuditSink(),
	}, nil
}
