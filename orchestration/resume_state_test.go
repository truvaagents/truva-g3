package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func resumeStateFixture(t *testing.T) (ResumeState, *skillExecutionStateHolder) {
	t.Helper()
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways, Required: true}
	registry := &resumeTestRegistry{activationTestRegistry: activationRegistryForBindings(t, []SkillBinding{binding})}
	runtime := resumeSkillRuntime(t, binding, registry, "selector-model")
	skills, cache, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, skills, err = runtime.prepareInitialBoundary(t.Context(), skills, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	holder := newSkillExecutionStateHolder(skills, cache)
	ctx := withResumeStateCapture(withSkillExecutionHolder(t.Context(), holder), true)
	state, err := CaptureResumeState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return state, holder
}

func TestResumeStateCapturesCurrentHolderAndIsolatesCopies(t *testing.T) {
	if _, err := CaptureResumeState(t.Context()); !errors.Is(err, ErrResumeStateUnavailable) {
		t.Fatal("captured before execution initialized")
	}
	if _, err := CaptureResumeState(nil); !errors.Is(err, ErrResumeStateUnavailable) {
		t.Fatal("captured nil context")
	}
	state, holder := resumeStateFixture(t)
	ctx := withResumeStateCapture(withSkillExecutionHolder(t.Context(), holder), true)
	updated, _ := holder.Snapshot()
	updated.ActiveSkills[0].Reason = "updated after initial context"
	holder.Store(updated)
	current, err := CaptureResumeState(ctx)
	if err != nil || current.SkillState.ActiveSkills[0].Reason != "updated after initial context" {
		t.Fatal("captured stale context skill state", err)
	}
	if state.SkillState.ActiveSkills[0].Reason == current.SkillState.ActiveSkills[0].Reason {
		t.Fatal("capture aliases mutable holder")
	}
	current.SkillState.ActiveSkills[0].Reason = "caller mutation"
	still, _ := holder.Snapshot()
	if still.ActiveSkills[0].Reason == "caller mutation" {
		t.Fatal("caller changed live request")
	}
	raw, _ := json.Marshal(current)
	for _, body := range []string{"Follow the weather procedure.", "Report the weather outcome.", "planning_instructions", "response_instructions", "access_token"} {
		if strings.Contains(string(raw), body) {
			t.Fatalf("resume state retained body/credential field %q", body)
		}
	}
	// A skill-free initialized child must not inherit another run's holder.
	free, err := CaptureResumeState(withResumeStateCapture(withSkillExecutionHolder(t.Context(), holder), false))
	if err != nil || free.SkillState != nil || free.SkillCacheContext != nil {
		t.Fatal("inherited another execution's skills")
	}
}

func TestResumeStateRejectsCorruptPinnedStateAndBoundsPayload(t *testing.T) {
	valid, _ := resumeStateFixture(t)
	clone := func() ResumeState {
		raw, _ := json.Marshal(valid)
		var result ResumeState
		_ = json.Unmarshal(raw, &result)
		return result
	}
	tests := map[string]func(*ResumeState){
		"version":            func(s *ResumeState) { s.Version++ },
		"orphan_cache":       func(s *ResumeState) { s.SkillState = nil },
		"missing_cache":      func(s *ResumeState) { s.SkillCacheContext = nil },
		"cache_mismatch":     func(s *ResumeState) { s.SkillCacheContext.Fingerprint = "sha256:" + strings.Repeat("a", 64) },
		"manifest_hash":      func(s *ResumeState) { s.SkillState.Pinned.Candidates[0].Resolved.ManifestHash = "bad" },
		"candidate_identity": func(s *ResumeState) { s.SkillState.Pinned.Candidates[0].Resolved.Ref.Name = "different" },
		"candidate_revision": func(s *ResumeState) { s.SkillState.Pinned.Candidates[0].Resolved.Version++ },
		"duplicate_active": func(s *ResumeState) {
			s.SkillState.ActiveSkills = append(s.SkillState.ActiveSkills, s.SkillState.ActiveSkills[0])
		},
		"unpinned_resource": func(s *ResumeState) {
			s.SkillState.ResourceSelections = []SkillResourceSelection{{Resource: SkillResourceRef{Name: "details", ExpectedHash: "sha256:" + strings.Repeat("a", 64)}, Boundary: SkillBoundaryContinuation, PhaseNumber: 2, Selector: SkillDecisionTrusted, Reason: "needed"}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := clone()
			mutate(&state)
			if _, err := WithResumeState(t.Context(), state); !errors.Is(err, ErrInvalidResumeState) {
				t.Fatal("corrupt state accepted", err)
			}
		})
	}
	oversized := clone()
	oversized.SkillState.Diagnostics = []SkillDiagnostic{{Detail: strings.Repeat("x", MaxResumeStateBytes)}}
	if _, err := WithResumeState(t.Context(), oversized); !errors.Is(err, ErrSkillLimitExceeded) {
		t.Fatal("unbounded state accepted", err)
	}
}

func TestResumeStateShadowsInheritedMarkersWithoutHITLAuthority(t *testing.T) {
	state, holder := resumeStateFixture(t)
	ctx := withSkillFreeCheckpointResume(t.Context())
	ctx = withSkillExecutionHolder(ctx, holder)
	restored, err := WithResumeState(ctx, state)
	if err != nil || isSkillFreeCheckpointResume(restored) {
		t.Fatal("inherited skill-free marker disabled restored pins", err)
	}
	if _, active := IsResumeMode(restored); active {
		t.Fatal("ordinary state conferred HITL approval mode")
	}
	if restored.Value(resumeLineageKey{}) != nil {
		t.Fatal("ordinary state conferred HITL ownership")
	}
	if _, present := skillExecutionHolderFromContext(restored); present {
		t.Fatal("inherited mutable holder survived restore")
	}
	state.SkillState.ActiveSkills[0].Reason = "caller mutation after restore"
	loaded, cache, present := checkpointSkillStateFromContext(restored)
	if !present || cache == nil || loaded.ActiveSkills[0].Reason == "caller mutation after restore" {
		t.Fatal("restore aliases caller state")
	}
	free, err := WithResumeState(restored, ResumeState{Version: ResumeStateVersion})
	if err != nil || !isSkillFreeCheckpointResume(free) {
		t.Fatal("recorded skill-free execution not preserved", err)
	}
	loaded, cache, present = checkpointSkillStateFromContext(free)
	if !present || cache != nil || checkpointHasEffectiveSkillState(loaded, cache) {
		t.Fatal("inherited checkpoint skills reappeared")
	}
	active := withResumeStateCapture(t.Context(), false)
	if _, err := WithResumeState(active, ResumeState{Version: ResumeStateVersion}); !errors.Is(err, ErrInvalidResumeState) {
		t.Fatal("restored over an active execution")
	}
}

type resumeStateCaptureHook struct {
	state ResumeState
	plan  *RoutingPlan
	calls int
}

func (*resumeStateCaptureHook) Name() string                 { return "capture-resume-state" }
func (*resumeStateCaptureHook) RequireAfterPlanningSuccess() {}
func (h *resumeStateCaptureHook) AfterPlanning(ctx context.Context, _ *core.PipelineContext, value interface{}) (interface{}, error) {
	state, err := CaptureResumeState(ctx)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(value)
	var plan RoutingPlan
	_ = json.Unmarshal(raw, &plan)
	plan.PhaseNumber = 1
	h.state, h.plan, h.calls = state, &plan, h.calls+1
	return value, nil
}

func resumeStateOrchestrator(t *testing.T, binding *SkillBinding, registry SkillRegistry, client core.AIClient, hook core.PipelineHook) *AIOrchestrator {
	t.Helper()
	config := NewDefaultOrchestratorConfig()
	config.EnableTelemetry = false
	config.HallucinationValidationEnabled = false
	config.EnableTieredResolution = false
	config.IterativePlanning.Enabled = false
	config.ResultDistill.Enabled = false
	config.PromptConfig.Domain = "travel"
	if binding != nil {
		config.Skills.Enabled = true
		config.Skills.Bindings = []SkillBinding{*binding}
		config.SkillRegistry = registry
	}
	config.SkillActivationAIOptions = &AIOptionsOverride{Model: stringPointer("selector-test-model")}
	config.SkillResourceAIOptions = &AIOptionsOverride{Model: stringPointer("selector-test-model")}
	hooks := []core.PipelineHook{}
	if hook != nil {
		hooks = append(hooks, hook)
	}
	orchestrator, err := CreateResolvedOrchestrator(config, OrchestratorDependencies{Discovery: NewMockDiscovery(), AIClient: client, PipelineHooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.SetCapabilityProvider(&testCapabilityProvider{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = orchestrator.Shutdown(ctx)
	})
	return orchestrator
}

func TestResumeStateContinuesOrdinaryExecutionWithExactPinnedRevision(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways, Required: true}
	registry := &resumeTestRegistry{activationTestRegistry: activationRegistryForBindings(t, []SkillBinding{binding})}
	hook := &resumeStateCaptureHook{}
	first := resumeStateOrchestrator(t, &binding, registry, &skillLifecycleAIClient{responses: []string{foundationTerminalEmptyPlan, "first result"}}, hook)
	if _, err := first.ProcessRequest(t.Context(), "request", nil); err != nil {
		t.Fatal(err)
	}
	if hook.calls != 1 || hook.state.SkillState == nil || len(hook.state.SkillState.ActiveSkills) != 1 {
		t.Fatal("actual final plan context not captured")
	}
	ctx, err := WithResumeState(t.Context(), hook.state)
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithPlanOverride(ctx, hook.plan)
	ctx = WithCompletedSteps(ctx, map[string]*StepResult{})
	// Changing the current binding's revision does not repin saved state.
	// The runtime still checks membership and asks the registry for exact v1.
	binding.Version = "2"
	registry.lastRequests = nil
	secondClient := &skillLifecycleAIClient{responses: []string{"continued result"}}
	second := resumeStateOrchestrator(t, &binding, registry, secondClient, nil)
	response, err := second.ProcessRequest(ctx, "request", nil)
	if err != nil || response.Response != "continued result" {
		t.Fatal("ordinary continuation failed", err)
	}
	if len(registry.lastRequests) != 1 || registry.lastRequests[0].RequestedVersion != "1" {
		t.Fatal("saved immutable revision was repinned")
	}
	if len(secondClient.prompts) != 1 || !strings.Contains(secondClient.prompts[0], "Report the weather outcome.") {
		t.Fatal("ordinary continuation did not retain skill synthesis guidance")
	}
	if _, approved := IsResumeMode(ctx); approved {
		t.Fatal("ordinary continuation used HITL approval")
	}
	// Removing current skill membership remains a hard revocation failure.
	revoked := resumeStateOrchestrator(t, nil, nil, &skillLifecycleAIClient{responses: []string{"must not execute"}}, nil)
	if _, err := revoked.ProcessRequest(ctx, "request", nil); !errors.Is(err, ErrSkillUnavailable) {
		t.Fatal("restoration bypassed current binding revocation", err)
	}
}

func TestResumeStateSkillFreeCaptureStaysFreeWithNewBindings(t *testing.T) {
	hook := &resumeStateCaptureHook{}
	first := resumeStateOrchestrator(t, nil, nil, &skillLifecycleAIClient{responses: []string{foundationTerminalEmptyPlan, "first result"}}, hook)
	if _, err := first.ProcessRequest(t.Context(), "request", nil); err != nil {
		t.Fatal(err)
	}
	if hook.calls != 1 || hook.state.SkillState != nil {
		t.Fatal("skill-free lifecycle not captured")
	}
	ctx, err := WithResumeState(t.Context(), hook.state)
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithPlanOverride(ctx, hook.plan)
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways, Required: true}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	client := &skillLifecycleAIClient{responses: []string{"continued result"}}
	second := resumeStateOrchestrator(t, &binding, registry, client, nil)
	if _, err := second.ProcessRequest(ctx, "request", nil); err != nil {
		t.Fatal(err)
	}
	if registry.resolveCalls != 0 || strings.Contains(client.prompts[0], "Follow the weather procedure.") || strings.Contains(client.prompts[0], "Report the weather outcome.") {
		t.Fatal("new bindings changed recorded skill-free execution")
	}
}
