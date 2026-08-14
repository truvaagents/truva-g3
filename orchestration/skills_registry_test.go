package orchestration

import (
	"context"
	"testing"
)

type skillContractTestBackend struct{}

func (*skillContractTestBackend) ListMetadata(context.Context, SkillMetadataFilter) ([]SkillMetadata, error) {
	return nil, nil
}
func (*skillContractTestBackend) ResolveCandidates(context.Context, []SkillCandidateRequest) ([]SkillCandidate, error) {
	return nil, nil
}
func (*skillContractTestBackend) GetManifest(context.Context, SkillVersionRef) (SkillManifest, error) {
	return SkillManifest{}, nil
}
func (*skillContractTestBackend) GetResource(context.Context, SkillResourceRef) (SkillResource, error) {
	return SkillResource{}, nil
}
func (*skillContractTestBackend) GetPublished(context.Context, SkillRef) (SkillRevisionRepresentation, error) {
	return SkillRevisionRepresentation{}, nil
}
func (*skillContractTestBackend) GetVersion(context.Context, SkillRef, uint64) (SkillRevisionRepresentation, error) {
	return SkillRevisionRepresentation{}, nil
}
func (*skillContractTestBackend) ListVersions(context.Context, SkillRef, SkillVersionListOptions) (SkillVersionPage, error) {
	return SkillVersionPage{}, nil
}
func (*skillContractTestBackend) PutPublished(context.Context, PutPublishedSkillInput) (PutPublishedSkillResult, error) {
	return PutPublishedSkillResult{}, nil
}
func (*skillContractTestBackend) DeleteVersions(context.Context, DeleteSkillVersionsInput) (DeleteSkillVersionsResult, error) {
	return DeleteSkillVersionsResult{}, nil
}
func (*skillContractTestBackend) RecordSkillAudit(context.Context, SkillAuditEvent) error {
	return nil
}

func TestSkillBackendCompositionUsesNarrowTypedCapabilities(t *testing.T) {
	backend := &skillContractTestBackend{}
	backends, err := NewOrchestrationBackends(
		WithSkillRegistryBackend(backend),
		WithSkillRevisionReader(backend),
		WithSkillAdministrationStore(backend),
		WithSkillRevisionDeletionStore(backend),
		WithSkillAuditSink(backend),
	)
	if err != nil {
		t.Fatalf("NewOrchestrationBackends() error = %v", err)
	}
	if backends.SkillRegistry() != backend ||
		backends.SkillRevisionReader() != backend ||
		backends.SkillAdministrationStore() != backend ||
		backends.SkillRevisionDeletionStore() != backend ||
		backends.SkillAuditSink() != backend {
		t.Fatal("skill backend composition did not preserve narrow capabilities")
	}
	requirements, err := NewBackendRequirements(BackendSkills)
	if err != nil {
		t.Fatalf("NewBackendRequirements() error = %v", err)
	}
	if err := backends.ValidateFor(requirements); err != nil {
		t.Fatalf("ValidateFor(BackendSkills) error = %v", err)
	}

	override := &skillContractTestBackend{}
	updated, err := backends.With(WithSkillRegistryBackend(override))
	if err != nil {
		t.Fatalf("With() error = %v", err)
	}
	if backends.SkillRegistry() != backend || updated.SkillRegistry() != override {
		t.Fatal("skill registry override mutated the base composition")
	}
}

func TestSkillBackendCompositionRejectsTypedNil(t *testing.T) {
	var backend *skillContractTestBackend
	options := []OrchestrationBackendOption{
		WithSkillRegistryBackend(backend),
		WithSkillRevisionReader(backend),
		WithSkillAdministrationStore(backend),
		WithSkillRevisionDeletionStore(backend),
		WithSkillAuditSink(backend),
	}
	for index, option := range options {
		if _, err := NewOrchestrationBackends(option); err == nil {
			t.Errorf("typed-nil skill backend option %d was accepted", index)
		}
	}
}

func TestSkillBackendRequirementFollowsEffectiveBindings(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	config.Skills.Enabled = true
	config.Skills.Bindings = []SkillBinding{{
		Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways,
	}}
	requirements, err := RequirementsForFeatures(config)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, capability := range requirements.Capabilities() {
		found = found || capability == BackendSkills
	}
	if !found {
		t.Fatalf("requirements = %#v, want %q", requirements.Capabilities(), BackendSkills)
	}

	config.Skills.Bindings = nil
	requirements, err = RequirementsForFeatures(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range requirements.Capabilities() {
		if capability == BackendSkills {
			t.Fatalf("empty bindings unexpectedly require %q", BackendSkills)
		}
	}
}

var (
	_ SkillRegistry              = (*skillContractTestBackend)(nil)
	_ SkillRevisionReader        = (*skillContractTestBackend)(nil)
	_ SkillAdministrationStore   = (*skillContractTestBackend)(nil)
	_ SkillRevisionDeletionStore = (*skillContractTestBackend)(nil)
	_ SkillAuditSink             = (*skillContractTestBackend)(nil)
	_ SkillPackageValidator      = (*DefaultSkillPackageValidator)(nil)
)
