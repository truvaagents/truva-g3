package orchestration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
)

type boundaryTestSkillResolver struct {
	initial      SkillActivationDecision
	continuation SkillActivationDecision
	initialCalls int
	contCalls    int
	initialInput SkillResolutionInput
	contInput    SkillContinuationInput
}

func (resolver *boundaryTestSkillResolver) ResolveInitial(
	_ context.Context,
	input SkillResolutionInput,
) (SkillActivationDecision, error) {
	resolver.initialCalls++
	resolver.initialInput = input
	return resolver.initial, nil
}

func (resolver *boundaryTestSkillResolver) ResolveContinuation(
	_ context.Context,
	input SkillContinuationInput,
) (SkillActivationDecision, error) {
	resolver.contCalls++
	resolver.contInput = input
	return resolver.continuation, nil
}

type boundaryTestResourceResolver struct {
	mu        sync.Mutex
	requests  map[SkillPromptBoundary]SkillResourceRequest
	calls     map[SkillPromptBoundary]int
	lastInput map[SkillPromptBoundary]SkillResourceResolutionInput
}

func (resolver *boundaryTestResourceResolver) Resolve(
	_ context.Context,
	input SkillResourceResolutionInput,
) (SkillResourceDecision, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.calls == nil {
		resolver.calls = make(map[SkillPromptBoundary]int)
	}
	if resolver.lastInput == nil {
		resolver.lastInput = make(map[SkillPromptBoundary]SkillResourceResolutionInput)
	}
	resolver.calls[input.Boundary]++
	resolver.lastInput[input.Boundary] = input
	request, found := resolver.requests[input.Boundary]
	if !found {
		return SkillResourceDecision{}, nil
	}
	return SkillResourceDecision{
		Select: []SkillResourceRequest{request},
		Reasons: map[string]SkillSelectionReason{
			skillResourceRequestKey(request): "boundary-specific detail",
		},
	}, nil
}

func TestSkillContinuationAddsPinnedAutoSkillAndReselectsResources(t *testing.T) {
	bindings := []SkillBinding{
		{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways},
		{Namespace: "travel", Name: "routing", Version: "published", Activation: SkillActivationAuto},
	}
	registry := activationRegistryForBindings(t, bindings)
	continuationResource := addActivationTestResource(t, registry, bindings[0].Ref(), SkillResourceInput{
		Name: "flood-routing", Description: "Flood routing details", LoadWhen: "Flooding was discovered.",
		AppliesTo: []SkillResourceScope{SkillResourceContinuation}, ContentType: "text/plain",
		Content: "Avoid closed roads and select a verified alternate.",
	})
	resolver := &boundaryTestSkillResolver{continuation: SkillActivationDecision{
		Activate: []SkillRef{bindings[1].Ref()},
		Reasons:  map[string]SkillSelectionReason{bindings[1].Ref().String(): "A new route is needed."},
	}}
	resourceResolver := &boundaryTestResourceResolver{requests: map[SkillPromptBoundary]SkillResourceRequest{
		SkillBoundaryContinuation: continuationResource,
	}}
	runtime, state := activationRuntimeAndState(t, bindings, registry, nil, func(config *OrchestratorConfig) {
		config.SkillResolver = resolver
		config.SkillResourceResolver = resourceResolver
		config.Skills.RuntimePolicyID = "test/multi-phase-v1"
	})
	snapshot := newExecutionRunSnapshot(2, map[string]*StepResult{
		"step-1": {
			StepID: "step-1", AgentName: "weather-tool", Success: true,
			Response: "Flood warning and road closure found.",
		},
	}, []string{"step-1"}, "Find an alternate route")
	ctx, err := WithSkillExpectedCapabilities(t.Context(), "route-planning")
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = runtime.PinCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = runtime.prepareInitialBoundary(ctx, state, "Plan my trip", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveSkills) != 1 {
		t.Fatalf("initial active skills = %#v", state.ActiveSkills)
	}
	ctx, state, projection, err := runtime.prepareContinuationBoundary(
		ctx, state, "Plan my trip", map[string]interface{}{core.EnrichmentRAGContext: "Road authority bulletin"},
		snapshot, SkillBoundaryContinuation,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.initialCalls != 1 || resolver.contCalls != 1 {
		t.Fatalf("resolver calls initial/continuation = %d/%d", resolver.initialCalls, resolver.contCalls)
	}
	if len(state.ActiveSkills) != 2 || state.ActiveSkills[1].Skill.Ref != bindings[1].Ref() {
		t.Fatalf("continuation active skills = %#v", state.ActiveSkills)
	}
	if projection == nil || projection.Boundary != SkillBoundaryContinuation ||
		len(projection.Skills) != 2 || len(projection.Skills[0].resources) != 1 {
		t.Fatalf("continuation projection = %#v", projection)
	}
	if _, ok := skillPromptProjectionFromContext(ctx); !ok {
		t.Fatal("continuation context does not carry projection")
	}
	resourceInput := resourceResolver.lastInput[SkillBoundaryContinuation]
	if resourceInput.Request != "Plan my trip" || len(resourceInput.Context.PriorResults) != 1 ||
		!strings.Contains(resourceInput.Context.PriorResults[0].Result, "Flood warning") ||
		resourceInput.Context.Objective != "Find an alternate route" {
		t.Fatalf("resource resolver compact context = %#v", resourceInput)
	}
	if len(resolver.contInput.Context.ExpectedCapabilities) != 1 ||
		resolver.contInput.Context.ExpectedCapabilities[0] != "route-planning" ||
		resolver.contInput.Context.Enrichments[core.EnrichmentRAGContext] != "Road authority bulletin" ||
		len(resolver.contInput.Context.PriorResults) != 1 {
		t.Fatalf("continuation selector context = %#v", resolver.contInput.Context)
	}
}

func TestSkillContinuationUsesDefaultAIResolverForRemainingPinnedCandidates(t *testing.T) {
	bindings := []SkillBinding{
		{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways},
		{Namespace: "travel", Name: "routing", Version: "published", Activation: SkillActivationAuto},
	}
	registry := activationRegistryForBindings(t, bindings)
	client := &activationTestAIClient{responses: []string{
		`{"selected_skills":[]}`,
		`{"selected_skills":[{"namespace":"travel","name":"routing","reason":"The forecast revealed a route impact."}]}`,
	}}
	runtime, state := activationRuntimeAndState(t, bindings, registry, client, nil)
	_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "Plan my trip", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveSkills) != 1 {
		t.Fatalf("initial active skills = %#v", state.ActiveSkills)
	}
	snapshot := newExecutionRunSnapshot(2, map[string]*StepResult{
		"step-1": {
			StepID: "step-1", AgentName: "weather-tool", Success: true,
			Response: "Flood warning found.",
		},
	}, []string{"step-1"}, "Choose a safe route")
	_, state, projection, err := runtime.prepareContinuationBoundary(
		t.Context(), state, "Plan my trip", nil, snapshot, SkillBoundaryContinuation,
	)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 2 || len(state.ActiveSkills) != 2 || projection == nil ||
		state.ActiveSkills[1].Selector != SkillDecisionDefaultAI {
		t.Fatalf("continuation selection calls=%d active=%#v projection=%#v", client.calls, state.ActiveSkills, projection)
	}
	if !strings.Contains(client.options[1].SystemPrompt, "<output_contract>") {
		t.Fatalf("continuation selector options = %#v", client.options[1])
	}
}

func TestSkillRegenerationProjectionReuseDoesNotReadOrReselect(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, nil)
	ctx, state, err := runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	projection, ok := skillPromptProjectionFromContext(ctx)
	if !ok {
		t.Fatal("initial projection missing")
	}
	manifestCalls := registry.manifestCalls
	reusedCtx := withReusedSkillProjection(t.Context(), projection, SkillBoundaryRegeneration)
	reused, ok := skillPromptProjectionFromContext(reusedCtx)
	if !ok || reused.Boundary != SkillBoundaryRegeneration || registry.manifestCalls != manifestCalls {
		t.Fatalf("reused projection = %#v, manifest calls = %d/%d", reused, registry.manifestCalls, manifestCalls)
	}
	reused.Skills[0].manifest.PlanningInstructions[0] = "mutated"
	if projection.Skills[0].manifest.PlanningInstructions[0] == "mutated" {
		t.Fatal("regeneration projection aliases the prior projection")
	}
	before := len(state.Debug.Projections)
	_, err = runtime.reuseSkillProjection(t.Context(), &state, projection, SkillBoundaryRegeneration)
	if err != nil || len(state.Debug.Projections) != before+1 {
		t.Fatalf("reuseSkillProjection() projections=%#v error=%v", state.Debug.Projections, err)
	}
	got := state.Debug.Projections[len(state.Debug.Projections)-1]
	if got.Boundary != SkillBoundaryRegeneration || got.PromptKind != "regeneration" || got.Outcome != "reused" ||
		registry.manifestCalls != manifestCalls {
		t.Fatalf("regeneration debug=%#v manifest calls=%d/%d", got, registry.manifestCalls, manifestCalls)
	}
}

func TestSkillSynthesisUsesResponseGuidanceAndSynthesisResourcesOnly(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	synthesisResource := addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "reporting", Description: "Reporting details", LoadWhen: "A final report is produced.",
		AppliesTo: []SkillResourceScope{SkillResourceSynthesis}, ContentType: "text/plain",
		Content: "Include forecast confidence in the final answer.",
	})
	resourceResolver := &boundaryTestResourceResolver{requests: map[SkillPromptBoundary]SkillResourceRequest{
		SkillBoundarySynthesis: synthesisResource,
	}}
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, func(config *OrchestratorConfig) {
		config.SkillResourceResolver = resourceResolver
		config.Skills.RuntimePolicyID = "test/synthesis-v1"
	})
	_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, state, projection, err := runtime.prepareSynthesisBoundary(
		t.Context(), state, "request", newExecutionRunSnapshot(1, nil, nil, ""),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := prepareAIInvocation(ctx, aiInvocation{
		Purpose: "synthesis", Prompt: "base", Options: &core.AIOptions{SystemPrompt: "system"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if projection == nil || len(projection.Skills[0].resources) != 1 ||
		!strings.Contains(result.Effective.Prompt, "Report the weather outcome") ||
		!strings.Contains(result.Effective.Prompt, "Include forecast confidence") ||
		strings.Contains(result.Effective.Prompt, "Follow the weather procedure") {
		t.Fatalf("synthesis prompt/projection = %s / %#v", result.Effective.Prompt, projection)
	}
	if len(state.Debug.Projections) != 2 || state.Debug.Projections[1].PromptKind != "synthesis" {
		t.Fatalf("projection debug = %#v", state.Debug.Projections)
	}
}

func TestSkillProjectionIsRequestScopedUnderConcurrency(t *testing.T) {
	left := skillPromptTestProjection(t)
	right := skillPromptTestProjection(t)
	right.Skills[0].active.Skill.Ref = SkillRef{Namespace: "devops", Name: "incident"}
	right.Skills[0].manifest.Ref = right.Skills[0].active.Skill
	right.Skills[0].manifest.PlanningInstructions = []string{"Use incident evidence."}

	contexts := []context.Context{
		withSkillPromptProjection(t.Context(), left),
		withSkillPromptProjection(t.Context(), right),
	}
	wants := []string{"weather", "incident"}
	for index := range contexts {
		contexts[index] = withPromptInputPreparer(contexts[index], skillPromptInputPreparer{})
	}
	var wg sync.WaitGroup
	errors := make(chan string, 2)
	for index := range contexts {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for iteration := 0; iteration < 25; iteration++ {
				result, err := prepareAIInvocation(contexts[index], aiInvocation{
					Purpose: "planning", Prompt: "base", Options: &core.AIOptions{},
				})
				if err != nil || !strings.Contains(result.Effective.Prompt, wants[index]) ||
					strings.Contains(result.Effective.Prompt, wants[1-index]) {
					errors <- wants[index]
					return
				}
			}
		}(index)
	}
	wg.Wait()
	close(errors)
	if value := <-errors; value != "" {
		t.Fatalf("request-scoped projection leaked for %s", value)
	}
}

func TestTruncateSkillSelectorTextPreservesUTF8(t *testing.T) {
	got := truncateSkillSelectorText("weather ☀️ advisory", 10)
	if !utf8.ValidString(got) || len(got) > 13 || !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated selector text = %q", got)
	}
	if got := truncateSkillSelectorText("valid", 10); got != "valid" {
		t.Fatalf("untruncated selector text = %q", got)
	}
}
