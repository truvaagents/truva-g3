package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

type resourceTestResolver struct {
	decision SkillResourceDecision
	err      error
	calls    int
	input    SkillResourceResolutionInput
}

func TestClassifySkillResourceSelectorFailure(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{&core.AIRequestFeatureError{ClientType: "legacy", Feature: "generation.temperature"}, "skill_ai_request_feature_unsupported"},
		{ErrInvalidSkillPackage, "skill_resource_selection_invalid"},
		{errors.New("provider unavailable"), "skill_resource_selection_failed"},
	}
	for _, test := range tests {
		if got := classifySkillResourceSelectorFailure(test.err); got != test.want {
			t.Fatalf("classifySkillResourceSelectorFailure(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func (resolver *resourceTestResolver) Resolve(
	_ context.Context,
	input SkillResourceResolutionInput,
) (SkillResourceDecision, error) {
	resolver.calls++
	resolver.input = input
	return resolver.decision, resolver.err
}

func TestSkillTrustedResourceLoadsWithoutSelector(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	request := addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "flood-guide", Description: "Flood response guidance", LoadWhen: "A flood warning is present.",
		AppliesTo: []SkillResourceScope{SkillResourcePlanning}, ContentType: "text/plain",
		Content: "Avoid flooded roads.",
	})
	runtime, _ := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, nil)
	ctx, err := WithTrustedSkillResourceRequests(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := runtime.PinCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx, state, err = runtime.prepareInitialBoundary(ctx, state, "Is the flooded route safe?", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	projection, ok := skillPromptProjectionFromContext(ctx)
	if !ok || len(projection.Skills) != 1 || len(projection.Skills[0].resources) != 1 {
		t.Fatalf("resource projection = %#v", projection)
	}
	if registry.resourceCalls != 1 || len(state.ResourceSelections) != 1 ||
		state.ResourceSelections[0].Selector != SkillDecisionTrusted {
		t.Fatalf("resource selection = %#v; calls = %d", state.ResourceSelections, registry.resourceCalls)
	}
}

func TestSkillResourceSelectorRetriesAndRejectsUnknownReferences(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "storm-guide", Description: "Storm guidance", LoadWhen: "Storm impacts are requested.",
		AppliesTo: []SkillResourceScope{SkillResourcePlanning}, ContentType: "text/plain", Content: "Delay in severe wind.",
	})
	client := &activationTestAIClient{responses: []string{
		`{"selected_resources":[{"namespace":"travel","name":"weather","resource":"invented","reason":"No."}]}`,
		`{"selected_resources":[{"namespace":"travel","name":"weather","resource":"storm-guide","reason":"Storm impacts were requested."}]}`,
	}}
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, client, func(config *OrchestratorConfig) {
		config.SkillPromptGuidance.Resource = "Prefer <b>dated</b> operational references."
	})
	_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "Will the storm delay me?", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 2 || registry.resourceCalls != 1 || len(state.ResourceSelections) != 1 {
		t.Fatalf("calls = %d/%d, selections = %#v", client.calls, registry.resourceCalls, state.ResourceSelections)
	}
	if options := client.options[0]; options.Temperature != 0.01 || options.ResponseFormat != "" ||
		!strings.Contains(options.SystemPrompt, "<resource_candidates>") ||
		strings.Contains(options.SystemPrompt, "<b>") ||
		!strings.Contains(options.SystemPrompt, `\u003cb\u003edated\u003c/b\u003e`) {
		t.Fatalf("resource selector options = %#v", options)
	}
	systemPrompt := client.options[0].SystemPrompt
	if guidance, output := strings.Index(systemPrompt, "<developer_guidance>"), strings.Index(systemPrompt, "<output_contract>"); guidance < 0 || output < 0 || guidance > output {
		t.Fatalf("resource guidance must precede the fixed example: %q", systemPrompt)
	}
}

func TestSkillResourceScopeAndCustomResolverAreEnforced(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	planning := addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "planning", Description: "Planning detail", LoadWhen: "Planning", AppliesTo: []SkillResourceScope{SkillResourcePlanning},
		ContentType: "text/plain", Content: "planning body",
	})
	addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "synthesis", Description: "Synthesis detail", LoadWhen: "Synthesis", AppliesTo: []SkillResourceScope{SkillResourceSynthesis},
		ContentType: "text/plain", Content: "synthesis body",
	})
	key := skillResourceRequestKey(planning)
	resolver := &resourceTestResolver{decision: SkillResourceDecision{
		Select: []SkillResourceRequest{planning}, Reasons: map[string]SkillSelectionReason{key: "deterministic match"},
	}}
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, func(config *OrchestratorConfig) {
		config.SkillResourceResolver = resolver
		config.SkillTokenCounter = skillTokenCounterTestDouble{err: errors.New("counter unavailable")}
		config.Skills.RuntimePolicyID = "test/resource-policy-v1"
	})
	_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || len(resolver.input.Resources) != 1 ||
		resolver.input.Resources[0].Resource.Name != "planning" {
		t.Fatalf("resolver input = %#v", resolver.input)
	}
	if len(state.ResourceSelections) != 1 || state.ResourceSelections[0].Selector != SkillDecisionCustomPolicy ||
		state.ResourceSelections[0].Reason != "deterministic match" {
		t.Fatalf("selection = %#v", state.ResourceSelections)
	}
	if !hasRuntimeSkillDiagnostic(state.Diagnostics, "skill_token_counter_fallback") {
		t.Fatalf("fallback diagnostics = %#v", state.Diagnostics)
	}
}

func TestSkillRequiredSelectedResourceFailureFailsBoundary(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	request := addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "required", Description: "Required detail", LoadWhen: "Requested", RequiredWhenSelected: true,
		AppliesTo: []SkillResourceScope{SkillResourcePlanning}, ContentType: "text/plain", Content: "body",
	})
	delete(registry.resources, SkillResourceRef{
		Skill: registry.candidates[0].Resolved, Name: request.Name,
		ExpectedHash: registry.manifests[registry.candidates[0].Resolved].Resources[0].ResourceHash,
	})
	runtime, _ := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, nil)
	ctx, err := WithTrustedSkillResourceRequests(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := runtime.PinCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runtime.prepareInitialBoundary(ctx, state, "request", nil, 1)
	if !errors.Is(err, ErrSkillUnavailable) {
		t.Fatalf("prepareInitialBoundary() error = %v", err)
	}
}

func TestSkillOptionalResourceBudgetOmissionDoesNotLeakBodyIntoState(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	request := addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "large", Description: "Large detail", LoadWhen: "Requested",
		AppliesTo: []SkillResourceScope{SkillResourcePlanning}, ContentType: "text/plain", Content: strings.Repeat("large ", 100),
	})
	runtime, _ := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, func(config *OrchestratorConfig) {
		config.Skills.Limits.ResourceTokenBudget = 5
	})
	ctx, err := WithTrustedSkillResourceRequests(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := runtime.PinCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = runtime.prepareInitialBoundary(ctx, state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ResourceSelections) != 0 || len(state.Diagnostics) == 0 {
		t.Fatalf("optional resource state = %#v", state)
	}
	encoded := mustMarshalSkillValue(state)
	if strings.Contains(encoded, "large large") {
		t.Fatalf("resource body leaked into execution state: %s", encoded)
	}
}

func TestSkillResourceExecutionLimitCountsDistinctReferences(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	request := addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "shared-guide", Description: "Shared planning and response detail",
		LoadWhen:    "The same detailed guidance is needed while planning and responding.",
		AppliesTo:   []SkillResourceScope{SkillResourcePlanning, SkillResourceSynthesis},
		ContentType: "text/plain", Content: "Use the same verified procedure.",
	})
	resolver := &resourceTestResolver{decision: SkillResourceDecision{
		Select: []SkillResourceRequest{request},
		Reasons: map[string]SkillSelectionReason{
			skillResourceRequestKey(request): "The same detail is relevant at this boundary.",
		},
	}}
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, func(config *OrchestratorConfig) {
		config.SkillResourceResolver = resolver
		config.Skills.RuntimePolicyID = "test/distinct-resources-v1"
		config.Skills.Limits.MaxResourcesPerPhase = 1
		config.Skills.Limits.MaxResourcesPerExecution = 1
	})
	_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, state, projection, err := runtime.prepareSynthesisBoundary(
		t.Context(), state, "request", newExecutionRunSnapshot(1, nil, nil, ""),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ResourceSelections) != 2 || projection == nil ||
		len(projection.Skills) != 1 || len(projection.Skills[0].resources) != 1 {
		t.Fatalf("reselected resource state = %#v; projection = %#v", state.ResourceSelections, projection)
	}
	if state.ResourceSelections[0].Resource != state.ResourceSelections[1].Resource {
		t.Fatalf("resource references differ: %#v", state.ResourceSelections)
	}
}

func addActivationTestResource(
	t *testing.T,
	registry *activationTestRegistry,
	skill SkillRef,
	input SkillResourceInput,
) SkillResourceRequest {
	t.Helper()
	for index := range registry.candidates {
		if registry.candidates[index].Ref != skill {
			continue
		}
		oldRef := registry.candidates[index].Resolved
		manifest := registry.manifests[oldRef]
		resource := SkillResource{
			Ref:         SkillResourceRef{Skill: oldRef, Name: input.Name},
			ContentType: input.ContentType, Content: input.Content,
		}
		resourceHash, err := ComputeSkillResourceHash(resource)
		if err != nil {
			t.Fatal(err)
		}
		resource.Ref.ExpectedHash = resourceHash
		manifest.Resources = append(manifest.Resources, SkillResourceMetadata{
			Name: input.Name, Description: input.Description, LoadWhen: input.LoadWhen,
			AppliesTo:            append([]SkillResourceScope(nil), input.AppliesTo...),
			RequiredWhenSelected: input.RequiredWhenSelected, ContentType: input.ContentType,
			ResourceHash: resourceHash,
		})
		manifest.Ref.ManifestHash = ""
		manifestHash, hashErr := ComputeSkillManifestHash(manifest)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		manifest.Ref.ManifestHash = manifestHash
		resource.Ref.Skill = manifest.Ref
		for oldResourceRef, existing := range registry.resources {
			if oldResourceRef.Skill != oldRef {
				continue
			}
			delete(registry.resources, oldResourceRef)
			existing.Ref.Skill = manifest.Ref
			registry.resources[existing.Ref] = existing
		}
		delete(registry.manifests, oldRef)
		registry.manifests[manifest.Ref] = manifest
		registry.resources[resource.Ref] = resource
		registry.candidates[index].Resolved = manifest.Ref
		return SkillResourceRequest{Skill: skill, Name: input.Name}
	}
	t.Fatalf("skill %s not found", skill.String())
	return SkillResourceRequest{}
}
