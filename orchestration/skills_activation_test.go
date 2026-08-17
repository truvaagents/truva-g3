package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
)

type activationTestRegistry struct {
	candidates    []SkillCandidate
	manifests     map[SkillVersionRef]SkillManifest
	resources     map[SkillResourceRef]SkillResource
	resolveCalls  int
	manifestCalls int
	resourceCalls int
}

func TestBoundedSkillSelectionReasonPreservesUTF8WithinByteCap(t *testing.T) {
	reason := SkillSelectionReason(strings.Repeat("a", skillActivationReasonMaxBytes-1) + "€")
	bounded := string(boundedSkillSelectionReason(reason))
	if !utf8.ValidString(bounded) {
		t.Fatalf("bounded reason is not valid UTF-8: %q", bounded)
	}
	if len(bounded) > skillActivationReasonMaxBytes {
		t.Fatalf("bounded reason length = %d, want <= %d", len(bounded), skillActivationReasonMaxBytes)
	}
	if bounded != strings.Repeat("a", skillActivationReasonMaxBytes-1) {
		t.Fatalf("bounded reason = %q", bounded)
	}
}

func (*activationTestRegistry) ListMetadata(context.Context, SkillMetadataFilter) ([]SkillMetadata, error) {
	return nil, nil
}

func (registry *activationTestRegistry) ResolveCandidates(
	context.Context,
	[]SkillCandidateRequest,
) ([]SkillCandidate, error) {
	registry.resolveCalls++
	return cloneSkillCandidates(registry.candidates), nil
}

func (registry *activationTestRegistry) GetManifest(
	_ context.Context,
	ref SkillVersionRef,
) (SkillManifest, error) {
	registry.manifestCalls++
	manifest, found := registry.manifests[ref]
	if !found {
		return SkillManifest{}, ErrSkillNotFound
	}
	return cloneSkillManifest(manifest), nil
}

func (registry *activationTestRegistry) GetResource(
	_ context.Context,
	ref SkillResourceRef,
) (SkillResource, error) {
	registry.resourceCalls++
	resource, found := registry.resources[ref]
	if !found {
		return SkillResource{}, ErrSkillNotFound
	}
	return cloneSkillResource(resource), nil
}

type activationTestAIClient struct {
	responses []string
	err       error
	calls     int
	options   []*core.AIOptions
}

func (client *activationTestAIClient) GenerateResponse(
	_ context.Context,
	_ string,
	options *core.AIOptions,
) (*core.AIResponse, error) {
	client.calls++
	client.options = append(client.options, cloneCoreAIOptions(options))
	if client.err != nil {
		return nil, client.err
	}
	index := min(client.calls-1, len(client.responses)-1)
	return &core.AIResponse{Content: client.responses[index]}, nil
}

type activationTestPolicy struct {
	decision SkillActivationPolicyDecision
	err      error
}

type skillTokenCounterTestDouble struct {
	count int
	err   error
}

func (counter skillTokenCounterTestDouble) CountTokens(context.Context, string) (int, error) {
	return counter.count, counter.err
}

func (policy activationTestPolicy) Evaluate(
	context.Context,
	SkillActivationPolicyInput,
) (SkillActivationPolicyDecision, error) {
	return policy.decision, policy.err
}

func TestSkillInitialActivationOrdersDeterministicAndAISelections(t *testing.T) {
	bindings := []SkillBinding{
		{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto},
		{Namespace: "travel", Name: "baseline", Version: "published", Activation: SkillActivationAlways, Required: true},
		{Namespace: "travel", Name: "diagnostic", Version: "published", Activation: SkillActivationExplicit},
	}
	registry := activationRegistryForBindings(t, bindings)
	client := &activationTestAIClient{responses: []string{
		`{"selected_skills":[{"namespace":"travel","name":"weather","reason":"Weather affects this trip."}]}`,
	}}
	runtime, state := activationRuntimeAndState(t, bindings, registry, client, nil)
	ctx, err := WithTrustedSkillActivations(t.Context(), bindings[2].Ref())
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = runtime.PinCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx, state, err = runtime.prepareInitialBoundary(ctx, state, "Will storms affect my trip?", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveSkills) != 3 {
		t.Fatalf("active skills = %#v", state.ActiveSkills)
	}
	got := []string{
		state.ActiveSkills[0].Skill.Ref.String(),
		state.ActiveSkills[1].Skill.Ref.String(),
		state.ActiveSkills[2].Skill.Ref.String(),
	}
	want := []string{"travel/baseline", "travel/diagnostic", "travel/weather"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("activation order = %v, want %v", got, want)
		}
	}
	if client.calls != 1 || client.options[0].Temperature != 0.01 ||
		client.options[0].ResponseFormat != "" ||
		client.options[0].MaxTokens != runtime.config.Limits.ResolutionMaxTokens ||
		!strings.Contains(client.options[0].SystemPrompt, "<output_contract>") {
		t.Fatalf("selector call options = %#v, calls = %d", client.options, client.calls)
	}
	projection, ok := skillPromptProjectionFromContext(ctx)
	if !ok || len(projection.Skills) != 3 {
		t.Fatalf("initial projection = %#v", projection)
	}
	if len(state.Debug.Activations) != 3 || len(state.Debug.ContentLoads) != 3 {
		t.Fatalf("activation debug = %#v / %#v", state.Debug.Activations, state.Debug.ContentLoads)
	}
}

func TestSkillActivationGuidanceIsCanonicallyEncoded(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	client := &activationTestAIClient{responses: []string{`{"selected_skills":[]}`}}
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, client, func(config *OrchestratorConfig) {
		config.SkillPromptGuidance.Activation = "Prefer <b>dated</b> hazard requests."
	})
	_, _, err := runtime.prepareInitialBoundary(t.Context(), state, "weather request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("selector calls = %d, want 1", client.calls)
	}
	systemPrompt := client.options[0].SystemPrompt
	if !strings.Contains(systemPrompt, "<developer_guidance>") ||
		strings.Contains(systemPrompt, "<b>") ||
		!strings.Contains(systemPrompt, `\u003cb\u003edated\u003c/b\u003e`) {
		t.Fatalf("activation guidance was not canonically encoded: %q", systemPrompt)
	}
	if guidance, output := strings.Index(systemPrompt, "<developer_guidance>"), strings.Index(systemPrompt, "<output_contract>"); guidance < 0 || output < 0 || guidance > output {
		t.Fatalf("activation guidance must precede the fixed example: %q", systemPrompt)
	}
}

func TestSkillRuntimeTokenCounterControlsAdmissionAndFallsBackSafely(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})

	t.Run("custom count controls admission", func(t *testing.T) {
		runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, func(config *OrchestratorConfig) {
			config.SkillTokenCounter = skillTokenCounterTestDouble{count: config.Skills.Limits.TotalTokenBudget + 1}
			config.Skills.RuntimePolicyID = "test/token-counter-v1"
		})
		_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
		if err != nil || len(state.ActiveSkills) != 0 {
			t.Fatalf("prepareInitialBoundary() state=%#v error=%v", state, err)
		}
	})

	t.Run("invalid output falls back with diagnostic", func(t *testing.T) {
		runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, func(config *OrchestratorConfig) {
			config.SkillTokenCounter = skillTokenCounterTestDouble{err: errors.New("counter unavailable")}
			config.Skills.RuntimePolicyID = "test/token-counter-v1"
		})
		_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
		if err != nil || len(state.ActiveSkills) != 1 {
			t.Fatalf("prepareInitialBoundary() state=%#v error=%v", state, err)
		}
		found := false
		for _, diagnostic := range state.Diagnostics {
			found = found || diagnostic.Code == "skill_token_counter_fallback"
		}
		if !found {
			t.Fatalf("fallback diagnostics = %#v", state.Diagnostics)
		}
	})
}

func TestSkillActivationPolicyRefinesOnlyRemainingAutoCandidates(t *testing.T) {
	bindings := []SkillBinding{
		{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto},
		{Namespace: "travel", Name: "visa", Version: "published", Activation: SkillActivationAuto},
	}
	registry := activationRegistryForBindings(t, bindings)
	client := &activationTestAIClient{responses: []string{`{"selected_skills":[]}`}}
	runtime, state := activationRuntimeAndState(t, bindings, registry, client, func(config *OrchestratorConfig) {
		config.SkillActivationPolicy = activationTestPolicy{decision: SkillActivationPolicyDecision{
			Include: []SkillRef{bindings[0].Ref()}, Exclude: []SkillRef{bindings[1].Ref()},
		}}
		config.Skills.RuntimePolicyID = "test/activation-policy-v1"
	})
	_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveSkills) != 1 || state.ActiveSkills[0].Skill.Ref != bindings[0].Ref() || client.calls != 0 {
		t.Fatalf("policy activation = %#v, selector calls = %d", state.ActiveSkills, client.calls)
	}
	if state.ActiveSkills[0].Selector != SkillDecisionCustomPolicy {
		t.Fatalf("policy selector = %q", state.ActiveSkills[0].Selector)
	}
}

func TestSkillActivationSelectorRetriesMalformedOutputOnce(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	client := &activationTestAIClient{responses: []string{
		`{"selected_skills":`,
		`{"selected_skills":[{"namespace":"travel","name":"weather","reason":"Relevant."}]}`,
	}}
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, client, nil)
	_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "weather", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 2 || len(state.ActiveSkills) != 1 {
		t.Fatalf("selector retry calls = %d, active = %#v", client.calls, state.ActiveSkills)
	}
}

func TestSkillActivationSelectorFailureDoesNotExpandAutoCandidates(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto, Required: true}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	client := &activationTestAIClient{responses: []string{
		`{"selected_skills":[{"namespace":"unknown","name":"invented","reason":"No."}]}`,
	}}
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, client, nil)
	_, state, err := runtime.prepareInitialBoundary(t.Context(), state, "weather", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveSkills) != 0 || client.calls != 2 {
		t.Fatalf("invalid selection active = %#v, calls = %d", state.ActiveSkills, client.calls)
	}
	if got := state.Diagnostics[len(state.Diagnostics)-1].Code; got != "skill_activation_selection_invalid" {
		t.Fatalf("selector diagnostic = %q", got)
	}
}

func TestSkillActivationAdmissionRequiredAndOptionalFailures(t *testing.T) {
	tests := []struct {
		name      string
		required  bool
		wantError error
	}{
		{name: "optional omitted"},
		{name: "required fails", required: true, wantError: ErrSkillLimitExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding := SkillBinding{
				Namespace: "travel", Name: "large", Version: "published",
				Activation: SkillActivationAlways, Required: test.required,
			}
			registry := activationRegistryForBindings(t, []SkillBinding{binding})
			manifest := registry.manifests[registry.candidates[0].Resolved]
			manifest.PlanningInstructions = []string{strings.Repeat("large ", 100)}
			manifest.Ref.ManifestHash = ""
			hash, err := ComputeSkillManifestHash(manifest)
			if err != nil {
				t.Fatal(err)
			}
			manifest.Ref.ManifestHash = hash
			registry.candidates[0].Resolved = manifest.Ref
			registry.candidates[0].Metadata.PublishedVersion = manifest.Ref.Version
			registry.manifests = map[SkillVersionRef]SkillManifest{manifest.Ref: manifest}
			runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, func(config *OrchestratorConfig) {
				config.Skills.Limits.MainTokenBudget = 10
			})
			_, state, err = runtime.prepareInitialBoundary(t.Context(), state, "request", nil, 1)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("prepareInitialBoundary() error = %v, want %v", err, test.wantError)
			}
			if test.wantError == nil && len(state.ActiveSkills) != 0 {
				t.Fatalf("optional oversized skill became active: %#v", state.ActiveSkills)
			}
		})
	}
}

func activationRuntimeAndState(
	t *testing.T,
	bindings []SkillBinding,
	registry *activationTestRegistry,
	client core.AIClient,
	mutate func(*OrchestratorConfig),
) (*skillRuntime, SkillExecutionState) {
	t.Helper()
	config := NewDefaultOrchestratorConfig()
	config.Skills.Enabled = true
	config.Skills.Bindings = append([]SkillBinding(nil), bindings...)
	config.SkillRegistry = registry
	if mutate != nil {
		mutate(config)
	}
	runtime, err := newSkillRuntime(config, registry, client)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return runtime, state
}

func activationRegistryForBindings(t *testing.T, bindings []SkillBinding) *activationTestRegistry {
	t.Helper()
	registry := &activationTestRegistry{
		manifests: make(map[SkillVersionRef]SkillManifest),
		resources: make(map[SkillResourceRef]SkillResource),
	}
	for index, binding := range bindings {
		manifest := SkillManifest{
			Ref:         SkillVersionRef{Ref: binding.Ref(), Version: uint64(index + 1)},
			DisplayName: binding.Name, Description: "Use for " + binding.Name + " requests.",
			Domains: []string{binding.Namespace}, Tags: []string{binding.Name},
			PlanningInstructions: []string{"Follow the " + binding.Name + " procedure."},
			ResponseInstructions: []string{"Report the " + binding.Name + " outcome."},
		}
		hash, err := ComputeSkillManifestHash(manifest)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Ref.ManifestHash = hash
		registry.manifests[manifest.Ref] = manifest
		registry.candidates = append(registry.candidates, SkillCandidate{
			Ref: binding.Ref(), RequestedVersion: binding.Version, Resolved: manifest.Ref,
			Metadata: SkillMetadata{
				Ref: binding.Ref(), DisplayName: manifest.DisplayName, Description: manifest.Description,
				Domains: append([]string(nil), manifest.Domains...), Tags: append([]string(nil), manifest.Tags...),
				PublishedVersion: manifest.Ref.Version, Status: SkillPublicationPublished,
			},
			Status: SkillCandidateResolved,
		})
	}
	return registry
}
