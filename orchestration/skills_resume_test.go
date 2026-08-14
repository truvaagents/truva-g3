package orchestration

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

type resumeTestRegistry struct {
	*activationTestRegistry
	exactUnavailable bool
	lastRequests     []SkillCandidateRequest
}

func (registry *resumeTestRegistry) ResolveCandidates(
	_ context.Context,
	requests []SkillCandidateRequest,
) ([]SkillCandidate, error) {
	registry.resolveCalls++
	registry.lastRequests = append([]SkillCandidateRequest(nil), requests...)
	result := make([]SkillCandidate, 0, len(requests))
	for _, request := range requests {
		var found *SkillCandidate
		for _, source := range registry.candidates {
			if source.Ref == request.Ref {
				copy := source
				found = &copy
				break
			}
		}
		if found == nil {
			result = append(result, SkillCandidate{
				Ref: request.Ref, RequestedVersion: request.RequestedVersion,
				Status: SkillCandidateNotFound,
			})
			continue
		}
		found.RequestedVersion = request.RequestedVersion
		if registry.exactUnavailable && request.RequestedVersion != "published" {
			found.Status = SkillCandidateDeleted
			found.Resolved = SkillVersionRef{}
		} else if request.RequestedVersion != "published" {
			version, err := strconv.ParseUint(request.RequestedVersion, 10, 64)
			if err != nil || version != found.Resolved.Version {
				found.Status = SkillCandidateInvalidVersion
				found.Resolved = SkillVersionRef{}
			}
		}
		result = append(result, *found)
	}
	return result, nil
}

func TestSkillResumeRevalidatesExactVersionAndAcceptsPolicyFingerprintChange(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways, Required: true,
	}
	base := activationRegistryForBindings(t, []SkillBinding{binding})
	registry := &resumeTestRegistry{activationTestRegistry: base}
	firstRuntime := resumeSkillRuntime(t, binding, registry, "selector-model-a")
	state, oldCache, err := firstRuntime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = firstRuntime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	state.ResourceSelections = []SkillResourceSelection{{
		Resource: SkillResourceRef{
			Skill: state.ActiveSkills[0].Skill, Name: "details",
			ExpectedHash: "sha256:" + strings.Repeat("a", 64),
		},
		Boundary: SkillBoundaryContinuation, PhaseNumber: 2,
		Selector: SkillDecisionDefaultAI, Reason: "continuation needs details",
	}}

	secondRuntime := resumeSkillRuntime(t, binding, registry, "selector-model-b")
	registry.resolveCalls = 0
	resumed, currentCache, err := secondRuntime.ResumeCandidates(t.Context(), state, &oldCache)
	if err != nil {
		t.Fatal(err)
	}
	if registry.resolveCalls != 1 || len(registry.lastRequests) != 1 ||
		registry.lastRequests[0].RequestedVersion != "1" {
		t.Fatalf("resume reads = %d, requests = %#v", registry.resolveCalls, registry.lastRequests)
	}
	if currentCache.Fingerprint == oldCache.Fingerprint {
		t.Fatal("selector-model change did not update resumed cache dimension")
	}
	if resumed.Pinned.CacheFingerprint != currentCache.Fingerprint ||
		resumed.Debug.BindingSource != SkillBindingsFromCheckpoint ||
		!hasRuntimeSkillDiagnostic(resumed.Diagnostics, "skill_resume_cache_context_changed") {
		t.Fatalf("resumed state = %#v", resumed)
	}
	if len(resumed.ActiveSkills) != 1 || resumed.ActiveSkills[0].Skill != state.ActiveSkills[0].Skill {
		t.Fatalf("resume moved pinned activation = %#v", resumed.ActiveSkills)
	}
	if len(resumed.ResourceSelections) != 1 || resumed.ResourceSelections[0] != state.ResourceSelections[0] {
		t.Fatalf("resume moved resource selection = %#v", resumed.ResourceSelections)
	}
}

func TestSkillResumeBindingRevocationFailsWithoutRegistryRead(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	base := activationRegistryForBindings(t, []SkillBinding{binding})
	registry := &resumeTestRegistry{activationTestRegistry: base}
	firstRuntime := resumeSkillRuntime(t, binding, registry, "selector-model")
	state, cacheContext, err := firstRuntime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	config := NewDefaultOrchestratorConfig()
	config.Skills.Enabled = true
	config.Skills.Bindings = []SkillBinding{{
		Namespace: "travel", Name: "other", Version: "published", Activation: SkillActivationAlways,
	}}
	runtime, err := newSkillRuntime(config, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry.resolveCalls = 0
	resumed, _, err := runtime.ResumeCandidates(t.Context(), state, &cacheContext)
	if !errors.Is(err, ErrSkillUnavailable) || registry.resolveCalls != 0 ||
		!hasRuntimeSkillDiagnostic(resumed.Diagnostics, "skill_binding_revoked") {
		t.Fatalf("ResumeCandidates() = %#v, %v; reads = %d", resumed, err, registry.resolveCalls)
	}
}

func TestSkillResumeRejectsCheckpointCandidateBindingMismatchBeforeRegistryRead(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	base := activationRegistryForBindings(t, []SkillBinding{binding})
	registry := &resumeTestRegistry{activationTestRegistry: base}
	runtime := resumeSkillRuntime(t, binding, registry, "selector-model")
	state, cacheContext, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	other := SkillRef{Namespace: "unbound", Name: "injected"}
	state.Pinned.Candidates[0].Ref = other
	state.Pinned.Candidates[0].Resolved.Ref = other
	state.Pinned.Candidates[0].Metadata.Ref = other
	registry.resolveCalls = 0

	_, _, err = runtime.ResumeCandidates(t.Context(), state, &cacheContext)
	if !errors.Is(err, ErrSkillIntegrity) || registry.resolveCalls != 0 {
		t.Fatalf("ResumeCandidates(corrupt candidate) error = %v; reads = %d", err, registry.resolveCalls)
	}
}

func TestSkillResumeRejectsUnboundedOperationalStateBeforeRegistryRead(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	base := activationRegistryForBindings(t, []SkillBinding{binding})
	registry := &resumeTestRegistry{activationTestRegistry: base}
	runtime := resumeSkillRuntime(t, binding, registry, "selector-model")
	state, cacheContext, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	state.ActiveSkills[0].Reason = SkillSelectionReason(strings.Repeat("x", skillActivationReasonMaxBytes+1))
	registry.resolveCalls = 0

	_, _, err = runtime.ResumeCandidates(t.Context(), state, &cacheContext)
	if !errors.Is(err, ErrSkillIntegrity) || registry.resolveCalls != 0 {
		t.Fatalf("ResumeCandidates(unbounded state) error = %v; reads = %d", err, registry.resolveCalls)
	}
}

func TestSkillResumeRejectsBindingFingerprintMismatch(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	base := activationRegistryForBindings(t, []SkillBinding{binding})
	registry := &resumeTestRegistry{activationTestRegistry: base}
	runtime := resumeSkillRuntime(t, binding, registry, "selector-model")
	state, cacheContext, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.Pinned.DebugProvenance.BindingFingerprint = "sha256:" + strings.Repeat("0", 64)
	registry.resolveCalls = 0

	_, _, err = runtime.ResumeCandidates(t.Context(), state, &cacheContext)
	if !errors.Is(err, ErrSkillIntegrity) || registry.resolveCalls != 0 {
		t.Fatalf("ResumeCandidates(binding fingerprint mismatch) error = %v; reads = %d", err, registry.resolveCalls)
	}
}

func TestSkillResumeOptionalUnavailableVersionIsOmittedButAuditable(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	base := activationRegistryForBindings(t, []SkillBinding{binding})
	registry := &resumeTestRegistry{activationTestRegistry: base}
	runtime := resumeSkillRuntime(t, binding, registry, "selector-model")
	state, cacheContext, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	registry.exactUnavailable = true
	resumed, currentCache, err := runtime.ResumeCandidates(t.Context(), state, &cacheContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.ActiveSkills) != 1 || len(resumed.UnavailableContent) != 1 ||
		!hasRuntimeSkillDiagnostic(resumed.Diagnostics, "skill_version_unavailable") ||
		currentCache.Fingerprint == cacheContext.Fingerprint {
		t.Fatalf("optional unavailable resume = %#v / %#v", resumed, currentCache)
	}
	_, _, projection, err := runtime.prepareContinuationBoundary(
		t.Context(), resumed, "request", nil,
		newExecutionRunSnapshot(2, nil, nil, "continue"), SkillBoundaryResume,
	)
	if err != nil {
		t.Fatal(err)
	}
	if projection == nil || len(projection.Skills) != 0 {
		t.Fatalf("unavailable optional content projected = %#v", projection)
	}
}

func TestSkillResumeKeepsRepeatedOptionalUnavailabilityIdempotent(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	base := activationRegistryForBindings(t, []SkillBinding{binding})
	registry := &resumeTestRegistry{activationTestRegistry: base}
	runtime := resumeSkillRuntime(t, binding, registry, "selector-model")
	state, cacheContext, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	registry.exactUnavailable = true
	first, firstCache, err := runtime.ResumeCandidates(t.Context(), state, &cacheContext)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := runtime.ResumeCandidates(t.Context(), first, &firstCache)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.UnavailableContent) != 1 ||
		second.UnavailableContent[0] != state.ActiveSkills[0].Skill {
		t.Fatalf("repeated unavailable content = %#v", second.UnavailableContent)
	}
}

func TestSkillCheckpointAndResumeContextRoundTripBodyFreeState(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := &resumeTestRegistry{activationTestRegistry: activationRegistryForBindings(t, []SkillBinding{binding})}
	runtime := resumeSkillRuntime(t, binding, registry, "selector-model")
	state, cacheContext, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	holder := newSkillExecutionStateHolder(state, cacheContext)
	ctx := withSkillExecutionHolder(t.Context(), holder)
	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-skills", RequestID: "request", Status: CheckpointStatusApproved,
	}
	snapshot := newExecutionRunSnapshot(1, nil, nil, "")
	snapshotState, snapshotCache := holder.Snapshot()
	snapshot.SkillState, snapshot.SkillCacheContext = &snapshotState, &snapshotCache
	applyRunSnapshot(checkpoint, snapshot)
	if checkpoint.SkillState == nil || checkpoint.SkillCacheContext == nil {
		t.Fatalf("checkpoint skill state = %#v", checkpoint)
	}
	checkpoint.SkillState.ActiveSkills[0].Reason = "changed"
	unchanged, _ := holder.Snapshot()
	if unchanged.ActiveSkills[0].Reason == "changed" {
		t.Fatal("checkpoint aliases request-local skill state")
	}
	resumeCtx, end, err := BuildResumeContext(ctx, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	restored, restoredCache, ok := checkpointSkillStateFromContext(resumeCtx)
	if !ok || restored.Pinned == nil || restoredCache == nil ||
		restoredCache.Fingerprint != checkpoint.SkillCacheContext.Fingerprint {
		t.Fatalf("restored checkpoint state = %#v / %#v / %v", restored, restoredCache, ok)
	}
}

func TestBuildResumeContextRejectsOrphanedSkillCacheContext(t *testing.T) {
	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-orphaned-skill-cache", RequestID: "request",
		Status: CheckpointStatusApproved,
		SkillCacheContext: &SkillCacheContext{
			Fingerprint: "sha256:" + strings.Repeat("a", 64),
		},
	}

	resumeCtx, end, err := BuildResumeContext(t.Context(), checkpoint)
	if resumeCtx != nil || !errors.Is(err, ErrSkillIntegrity) {
		t.Fatalf("BuildResumeContext(orphaned cache) = %#v, %v", resumeCtx, err)
	}
	end()
}

func resumeSkillRuntime(
	t *testing.T,
	binding SkillBinding,
	registry SkillRegistry,
	model string,
) *skillRuntime {
	t.Helper()
	config := NewDefaultOrchestratorConfig()
	config.Skills.Enabled = true
	config.Skills.Bindings = []SkillBinding{binding}
	config.SkillActivationAIOptions = &AIOptionsOverride{Model: &model}
	config.SkillResourceAIOptions = &AIOptionsOverride{Model: &model}
	runtime, err := newSkillRuntime(config, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func hasRuntimeSkillDiagnostic(values []SkillDiagnostic, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}
