package orchestration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type skillLifecycleAIClient struct {
	responses []string
	prompts   []string
	options   []*core.AIOptions
}

type skillLifecycleStreamingAIClient struct {
	generatePrompt  string
	generateOptions *core.AIOptions
	streamPrompt    string
	streamOptions   *core.AIOptions
}

func (client *skillLifecycleStreamingAIClient) GenerateResponse(
	_ context.Context,
	prompt string,
	options *core.AIOptions,
) (*core.AIResponse, error) {
	client.generatePrompt = prompt
	client.generateOptions = cloneCoreAIOptions(options)
	return &core.AIResponse{
		Content: foundationTerminalEmptyPlan, Model: "planner", Provider: "test",
	}, nil
}

func (client *skillLifecycleStreamingAIClient) StreamResponse(
	_ context.Context,
	prompt string,
	options *core.AIOptions,
	callback core.StreamCallback,
) (*core.AIResponse, error) {
	client.streamPrompt = prompt
	client.streamOptions = cloneCoreAIOptions(options)
	if err := callback(core.StreamChunk{Content: "streamed response", FinishReason: "stop"}); err != nil {
		return nil, err
	}
	return &core.AIResponse{
		Content: "streamed response", Model: "synthesizer", Provider: "test",
	}, nil
}

func (*skillLifecycleStreamingAIClient) SupportsStreaming() bool { return true }

func (client *skillLifecycleAIClient) GenerateResponse(
	_ context.Context,
	prompt string,
	options *core.AIOptions,
) (*core.AIResponse, error) {
	client.prompts = append(client.prompts, prompt)
	client.options = append(client.options, cloneCoreAIOptions(options))
	index := min(len(client.prompts)-1, len(client.responses)-1)
	return &core.AIResponse{
		Content:  client.responses[index],
		Model:    "lifecycle-test-model",
		Provider: "lifecycle-test-provider",
	}, nil
}

type skillLifecycleGateHook struct {
	registryResolveCalls int
	cacheVary            map[string]string
	cacheReadDisabled    bool
	calls                int
	registry             *activationTestRegistry
}

func (*skillLifecycleGateHook) Name() string { return "skill-lifecycle-gate" }

func (hook *skillLifecycleGateHook) BeforePlanningDecision(
	_ context.Context,
	_ *core.PipelineContext,
	gate core.PipelineGate,
) (*core.PipelineShortCircuitDecision, error) {
	hook.calls++
	hook.registryResolveCalls = hook.registry.resolveCalls
	hook.cacheVary = gate.CacheVary()
	hook.cacheReadDisabled = gate.ResponseCacheReadDisabled()
	return nil, nil
}

func TestSkillsLifecycleRunsThroughFactoryRequestPath(t *testing.T) {
	binding := SkillBinding{
		Namespace:  "travel",
		Name:       "weather",
		Version:    "published",
		Activation: SkillActivationAlways,
		Required:   true,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	client := &skillLifecycleAIClient{responses: []string{
		foundationTerminalEmptyPlan,
		"synthesized response",
	}}
	hook := &skillLifecycleGateHook{registry: registry}
	config := NewDefaultOrchestratorConfig()
	config.EnableTelemetry = false
	config.HallucinationValidationEnabled = false
	config.EnableTieredResolution = false
	config.IterativePlanning.Enabled = false
	config.ResultDistill.Enabled = false
	config.PromptConfig.Domain = "travel"
	config.Skills.Enabled = true
	config.Skills.Bindings = []SkillBinding{binding}
	config.SkillRegistry = registry
	config.SkillActivationAIOptions = &AIOptionsOverride{Model: stringPointer("selector-test-model")}
	config.SkillResourceAIOptions = &AIOptionsOverride{Model: stringPointer("selector-test-model")}

	orchestrator, err := CreateResolvedOrchestrator(config, OrchestratorDependencies{
		Discovery:     NewMockDiscovery(),
		AIClient:      client,
		PipelineHooks: []core.PipelineHook{hook},
	})
	if err != nil {
		t.Fatalf("CreateResolvedOrchestrator() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = orchestrator.Shutdown(ctx)
	})
	orchestrator.SetCapabilityProvider(&testCapabilityProvider{})

	response, err := orchestrator.ProcessRequest(t.Context(), "request", nil)
	if err != nil {
		t.Fatalf("ProcessRequest() error = %v (AI calls=%d)", err, len(client.prompts))
	}
	if response == nil || response.Response != "synthesized response" {
		t.Fatalf("ProcessRequest() response = %#v", response)
	}
	if hook.calls != 1 || hook.registryResolveCalls != 1 {
		t.Fatalf("before-planning hook calls = %d, registry calls at hook = %d", hook.calls, hook.registryResolveCalls)
	}
	if hook.cacheReadDisabled {
		t.Fatal("response cache reads disabled despite explicit selector model identity")
	}
	if hook.cacheVary[reservedSkillCacheDimension] == "" {
		t.Fatalf("before-planning cache vary = %#v", hook.cacheVary)
	}
	if registry.resolveCalls != 1 {
		t.Fatalf("ResolveCandidates() calls = %d, want one batched request-start read", registry.resolveCalls)
	}
	if registry.manifestCalls != 1 {
		t.Fatalf("GetManifest() calls = %d, want one immutable content read", registry.manifestCalls)
	}
	if len(client.prompts) != 2 || len(client.options) != 2 {
		t.Fatalf("AI calls = %d, want planning and synthesis", len(client.prompts))
	}
	if !strings.Contains(client.prompts[0], `<active_skills boundary="initial_planning">`) ||
		!strings.Contains(client.prompts[0], "Follow the weather procedure.") ||
		strings.Contains(client.prompts[0], "Report the weather outcome.") {
		t.Fatalf("planning prompt did not receive only planning skill guidance: %q", client.prompts[0])
	}
	if !strings.Contains(client.prompts[1], `<active_skills boundary="synthesis">`) ||
		!strings.Contains(client.prompts[1], "Report the weather outcome.") ||
		strings.Contains(client.prompts[1], "Follow the weather procedure.") {
		t.Fatalf("synthesis prompt did not receive only response skill guidance: %q", client.prompts[1])
	}
	for index, options := range client.options {
		if options == nil || !strings.Contains(options.SystemPrompt, "<skill_precedence>") {
			t.Fatalf("AI call %d system prompt lacks skill precedence contract: %#v", index, options)
		}
	}
}

func TestSkillsLifecycleKeepsCompatibilityCheckpointSkillFree(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways, Required: true,
	}
	tests := []struct {
		name       string
		skillState *SkillExecutionState
		cache      *SkillCacheContext
	}{
		{name: "legacy checkpoint without skill fields"},
		{
			name:       "explicitly empty compatibility checkpoint",
			skillState: &SkillExecutionState{Pinned: &SkillSnapshot{}},
			cache:      &SkillCacheContext{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := activationRegistryForBindings(t, []SkillBinding{binding})
			client := &skillLifecycleAIClient{responses: []string{"synthesized response"}}
			hook := &skillLifecycleGateHook{registry: registry}
			config := NewDefaultOrchestratorConfig()
			config.EnableTelemetry = false
			config.HallucinationValidationEnabled = false
			config.EnableTieredResolution = false
			config.IterativePlanning.Enabled = false
			config.ResultDistill.Enabled = false
			config.PromptConfig.Domain = "travel"
			config.Skills.Enabled = true
			config.Skills.Bindings = []SkillBinding{binding}
			config.SkillRegistry = registry
			config.SkillActivationAIOptions = &AIOptionsOverride{Model: stringPointer("selector-test-model")}
			config.SkillResourceAIOptions = &AIOptionsOverride{Model: stringPointer("selector-test-model")}

			orchestrator, err := CreateResolvedOrchestrator(config, OrchestratorDependencies{
				Discovery: NewMockDiscovery(), AIClient: client,
				PipelineHooks: []core.PipelineHook{hook},
			})
			if err != nil {
				t.Fatalf("CreateResolvedOrchestrator() error = %v", err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = orchestrator.Shutdown(ctx)
			})
			orchestrator.SetCapabilityProvider(&testCapabilityProvider{})

			checkpoint := &ExecutionCheckpoint{
				CheckpointID: "checkpoint-before-skills",
				RequestID:    "request-before-skills",
				Status:       CheckpointStatusApproved,
				Plan: &RoutingPlan{
					PlanID: "approved-plan", PhaseNumber: 1, Terminal: boolPtr(true),
				},
				SkillState: test.skillState, SkillCacheContext: test.cache,
			}
			resumeCtx, endSpan, err := BuildResumeContext(t.Context(), checkpoint)
			if err != nil {
				t.Fatalf("BuildResumeContext() error = %v", err)
			}
			defer endSpan()
			if !isSkillFreeCheckpointResume(resumeCtx) {
				t.Fatal("compatibility checkpoint lacks private skill-free provenance")
			}

			response, err := orchestrator.ProcessRequest(resumeCtx, "resume request", nil)
			if err != nil {
				t.Fatalf("ProcessRequest() error = %v", err)
			}
			if response == nil || response.Response != "synthesized response" {
				t.Fatalf("ProcessRequest() response = %#v", response)
			}
			if registry.resolveCalls != 0 || registry.manifestCalls != 0 {
				t.Fatalf("skill reads = candidates %d, manifests %d; want none", registry.resolveCalls, registry.manifestCalls)
			}
			if hook.calls != 1 || hook.cacheVary[reservedSkillCacheDimension] != "" || hook.cacheReadDisabled {
				t.Fatalf("skill-free pipeline gate = calls %d, vary %#v, disabled %v",
					hook.calls, hook.cacheVary, hook.cacheReadDisabled)
			}
			if len(client.prompts) != 1 || strings.Contains(client.prompts[0], "<active_skills") ||
				client.options[0] != nil && strings.Contains(client.options[0].SystemPrompt, "<skill_precedence>") {
				t.Fatalf("skill-free resume leaked skill prompt content: prompts=%#v options=%#v", client.prompts, client.options)
			}
		})
	}
}

func TestPublicResumeModeDoesNotMarkExecutionSkillFree(t *testing.T) {
	ctx := WithResumeMode(t.Context(), "caller-supplied-checkpoint")
	if isSkillFreeCheckpointResume(ctx) {
		t.Fatal("WithResumeMode alone suppressed developer-configured skills")
	}
}

func TestSkillsLifecycleProjectsSynthesisGuidanceIntoNativeStreamingCall(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways, Required: true,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	client := &skillLifecycleStreamingAIClient{}
	config := NewDefaultOrchestratorConfig()
	config.EnableTelemetry = false
	config.HallucinationValidationEnabled = false
	config.EnableTieredResolution = false
	config.IterativePlanning.Enabled = false
	config.ResultDistill.Enabled = false
	config.PromptConfig.Domain = "travel"
	config.Skills.Enabled = true
	config.Skills.Bindings = []SkillBinding{binding}
	config.SkillRegistry = registry
	config.SkillActivationAIOptions = &AIOptionsOverride{Model: stringPointer("selector-test-model")}
	config.SkillResourceAIOptions = &AIOptionsOverride{Model: stringPointer("selector-test-model")}

	orchestrator, err := CreateResolvedOrchestrator(config, OrchestratorDependencies{
		Discovery: NewMockDiscovery(), AIClient: client,
	})
	if err != nil {
		t.Fatalf("CreateResolvedOrchestrator() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = orchestrator.Shutdown(ctx)
	})
	orchestrator.SetCapabilityProvider(&testCapabilityProvider{})

	response, err := orchestrator.ProcessRequestStreaming(
		t.Context(), "request", nil, func(core.StreamChunk) error { return nil },
	)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming() error = %v", err)
	}
	if response == nil || response.Response != "streamed response" {
		t.Fatalf("ProcessRequestStreaming() response = %#v", response)
	}
	if !strings.Contains(client.generatePrompt, `<active_skills boundary="initial_planning">`) ||
		!strings.Contains(client.generatePrompt, "Follow the weather procedure.") {
		t.Fatalf("planning prompt lacks skill projection: %q", client.generatePrompt)
	}
	if !strings.Contains(client.streamPrompt, `<active_skills boundary="synthesis">`) ||
		!strings.Contains(client.streamPrompt, "Report the weather outcome.") ||
		strings.Contains(client.streamPrompt, "Follow the weather procedure.") {
		t.Fatalf("streaming synthesis prompt has the wrong skill projection: %q", client.streamPrompt)
	}
	if client.streamOptions == nil ||
		!strings.Contains(client.streamOptions.SystemPrompt, "<skill_precedence>") {
		t.Fatalf("streaming synthesis system prompt lacks skill precedence: %#v", client.streamOptions)
	}
}

func stringPointer(value string) *string { return &value }
