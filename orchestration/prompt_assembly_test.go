package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

type customSystemPromptBuilder struct{}

func (customSystemPromptBuilder) BuildPlanningPrompt(context.Context, PromptInput) (string, error) {
	return "custom planning prompt", nil
}

func (customSystemPromptBuilder) BuildSystemPrompt(context.Context, PromptInput) string {
	return "custom system prompt"
}

func TestPromptAssembly_PlanningFinalizesRuntimeContextExactlyOnce(t *testing.T) {
	client := &invocationTestClient{}
	result, err := invokeAI(context.Background(), client, aiInvocation{
		Purpose: "planning",
		Prompt:  "user prompt bytes",
		Options: &core.AIOptions{SystemPrompt: "developer persona", Model: "requested-model"},
	})
	if err != nil {
		t.Fatalf("invokeAI() error = %v", err)
	}
	systemPrompt := client.request.Generation.SystemPrompt
	if systemPrompt.Mode != core.AIParameterSet || !strings.HasPrefix(systemPrompt.Value, "developer persona\n\n<runtime_context>") {
		t.Fatalf("prepared system prompt = %#v", systemPrompt)
	}
	if strings.Count(systemPrompt.Value, "<runtime_context>") != 1 ||
		strings.Count(systemPrompt.Value, "</runtime_context>") != 1 {
		t.Fatalf("runtime context not exact-once: %q", systemPrompt.Value)
	}
	if client.request.Prompt != "user prompt bytes" || result.Effective.Prompt != client.request.Prompt ||
		result.Effective.SystemPrompt != systemPrompt.Value {
		t.Fatalf("effective request differs from provider request: %#v / %#v", result.Effective, client.request)
	}
}

func TestPromptAssembly_CanonicalRuntimeContextIsBytePreserved(t *testing.T) {
	canonical := appendRuntimeContext("persona")
	prepared, err := prepareAIInvocation(context.Background(), aiInvocation{
		Purpose: "continuation-planning",
		Prompt:  "prompt",
		Options: &core.AIOptions{SystemPrompt: canonical},
	})
	if err != nil {
		t.Fatalf("prepareAIInvocation() error = %v", err)
	}
	if got := prepared.Request.Generation.SystemPrompt.Value; got != canonical {
		t.Fatalf("canonical system prompt changed:\nwant %q\n got %q", canonical, got)
	}
}

func TestPromptAssembly_CustomBuilderCanonicalRuntimeContextIsBytePreserved(t *testing.T) {
	canonical := appendRuntimeContext("persona")
	prepared, err := prepareAIInvocation(context.Background(), aiInvocation{
		Purpose:      "planning",
		Prompt:       "prompt",
		Options:      &core.AIOptions{SystemPrompt: canonical},
		SystemSource: promptSystemCustomBuilder,
	})
	if err != nil {
		t.Fatalf("prepareAIInvocation() error = %v", err)
	}
	if got := prepared.Request.Generation.SystemPrompt.Value; got != canonical {
		t.Fatalf("custom canonical system prompt changed:\nwant %q\n got %q", canonical, got)
	}
}

func TestPromptAssembly_AIOptionsOverrideCannotOwnReservedRuntimeContext(t *testing.T) {
	canonical := appendRuntimeContext("persona")
	prepared, err := prepareAIInvocation(context.Background(), aiInvocation{
		Purpose:      "planning",
		Prompt:       "prompt",
		Options:      &core.AIOptions{SystemPrompt: canonical},
		SystemSource: promptSystemAIOptionsOverride,
	})
	if prepared != nil || !errors.Is(err, ErrInvalidPromptAssembly) {
		t.Fatalf("override preparation = %#v, %v", prepared, err)
	}
}

func TestPromptAssembly_CustomBuilderMalformedRuntimeContextIsRejected(t *testing.T) {
	prepared, err := prepareAIInvocation(context.Background(), aiInvocation{
		Purpose:      "planning",
		Prompt:       "prompt",
		Options:      &core.AIOptions{SystemPrompt: "persona <runtime_context>spoof</runtime_context>"},
		SystemSource: promptSystemCustomBuilder,
	})
	if prepared != nil || !errors.Is(err, ErrInvalidPromptAssembly) {
		t.Fatalf("malformed custom preparation = %#v, %v", prepared, err)
	}
}

func TestPromptAssembly_FrameworkRuntimeContextSurvivesDateBoundary(t *testing.T) {
	canonical := "persona\n\n<runtime_context>\n" +
		"Current date (UTC): 2000-01-01. Resolve relative dates (today, tomorrow, next week, etc.) against this value.\n" +
		"</runtime_context>"
	prepared, err := prepareAIInvocation(context.Background(), aiInvocation{
		Purpose: "planning",
		Prompt:  "prompt",
		Options: &core.AIOptions{SystemPrompt: canonical},
	})
	if err != nil {
		t.Fatalf("prepareAIInvocation() error = %v", err)
	}
	if got := prepared.Request.Generation.SystemPrompt.Value; got != canonical {
		t.Fatalf("framework runtime context changed:\nwant %q\n got %q", canonical, got)
	}
}

func TestPromptAssembly_RejectsSpoofedReservedRuntimeContextBeforeProvider(t *testing.T) {
	client := &invocationTestClient{}
	result, err := invokeAI(context.Background(), client, aiInvocation{
		Purpose: "planning",
		Prompt:  "prompt",
		Options: &core.AIOptions{SystemPrompt: "persona <runtime_context>spoof</runtime_context>"},
	})
	if result != nil || !errors.Is(err, ErrInvalidPromptAssembly) {
		t.Fatalf("invokeAI() = %#v, %v", result, err)
	}
	if client.request != nil {
		t.Fatal("provider was invoked for an invalid reserved-tag assembly")
	}
}

func TestPromptAssembly_SynthesisFinalizerIsNoOpWithoutProjection(t *testing.T) {
	prepared, err := prepareAIInvocation(context.Background(), aiInvocation{
		Purpose: "synthesis",
		Prompt:  "prompt",
		Options: &core.AIOptions{SystemPrompt: "synthesis persona"},
	})
	if err != nil {
		t.Fatalf("prepareAIInvocation() error = %v", err)
	}
	if got := prepared.Request.LegacyOptions().SystemPrompt; got != "synthesis persona" {
		t.Fatalf("synthesis system prompt = %q", got)
	}
	if strings.Contains(prepared.Effective.SystemPrompt, "<runtime_context>") ||
		strings.Contains(prepared.Effective.SystemPrompt, "<skill_precedence>") ||
		prepared.Effective.Prompt != "prompt" {
		t.Fatalf("no-projection synthesis changed = %#v", prepared.Effective)
	}
}

func TestPromptAssembly_PlanCorrectionHasNoSystemPromptFinalizer(t *testing.T) {
	prepared, err := prepareAIInvocation(context.Background(), aiInvocation{
		Purpose: "plan-correction",
		Prompt:  "correct this JSON",
	})
	if err != nil {
		t.Fatalf("prepareAIInvocation() error = %v", err)
	}
	if got := prepared.Request.Generation.SystemPrompt.Mode; got != core.AIParameterInherit {
		t.Fatalf("correction system-prompt mode = %v, want inherit", got)
	}
	if got := prepared.Request.LegacyOptions(); got != nil && got.SystemPrompt != "" {
		t.Fatalf("correction legacy system prompt = %q", got.SystemPrompt)
	}
	if prepared.Effective.SystemPrompt != "" || len(promptFinalizers(promptCorrection)) != 0 {
		t.Fatalf("correction received a system finalizer: %#v", prepared.Effective)
	}
}

func TestPromptAssembly_PlanningOmitCannotRemoveRuntimeContext(t *testing.T) {
	prepared, err := prepareAIInvocation(context.Background(), aiInvocation{
		Purpose: "planning",
		Prompt:  "prompt",
		Options: &core.AIOptions{SystemPrompt: "developer persona"},
		Generation: core.AIGenerationOptions{
			SystemPrompt: core.OmitAIParameter[string](),
		},
	})
	if err != nil {
		t.Fatalf("prepareAIInvocation() error = %v", err)
	}
	if prepared.Request.Generation.SystemPrompt.Mode != core.AIParameterSet {
		t.Fatalf("planning system-prompt mode = %v, want set", prepared.Request.Generation.SystemPrompt.Mode)
	}
	actual := prepared.Request.Generation.SystemPrompt.Value
	if !strings.Contains(actual, "<runtime_context>") || strings.Contains(actual, "developer persona") {
		t.Fatalf("final planning system prompt = %q", actual)
	}
	if prepared.Effective.SystemPrompt != actual {
		t.Fatalf("effective system prompt = %q, provider request = %q", prepared.Effective.SystemPrompt, actual)
	}
}

func TestPromptAssembly_SynthesisOmitRemainsOmitted(t *testing.T) {
	prepared, err := prepareAIInvocation(context.Background(), aiInvocation{
		Purpose: "synthesis",
		Prompt:  "prompt",
		Options: &core.AIOptions{SystemPrompt: "synthesis persona"},
		Generation: core.AIGenerationOptions{
			SystemPrompt: core.OmitAIParameter[string](),
		},
	})
	if err != nil {
		t.Fatalf("prepareAIInvocation() error = %v", err)
	}
	if prepared.Request.Generation.SystemPrompt.Mode != core.AIParameterOmit {
		t.Fatalf("synthesis system-prompt mode = %v, want omit", prepared.Request.Generation.SystemPrompt.Mode)
	}
	if prepared.Effective.SystemPrompt != "" {
		t.Fatalf("omitted system prompt was reported as %q", prepared.Effective.SystemPrompt)
	}
}

func TestFingerprintAIUsesFinalizedProviderRequest(t *testing.T) {
	client := &invocationFingerprintClient{fingerprint: "planning-policy", stable: true}
	fingerprint, stable := fingerprintAI(t.Context(), client, aiInvocation{
		Purpose: "planning",
		Prompt:  "prompt",
		Options: &core.AIOptions{SystemPrompt: "developer persona"},
	})
	if !stable || fingerprint != "planning-policy" {
		t.Fatalf("fingerprint = %q, stable = %t", fingerprint, stable)
	}
	if client.fingerprintRequest == nil ||
		client.fingerprintRequest.Generation.SystemPrompt.Mode != core.AIParameterSet ||
		!strings.Contains(client.fingerprintRequest.Generation.SystemPrompt.Value, "<runtime_context>") {
		t.Fatalf("fingerprint request was not finalized: %#v", client.fingerprintRequest)
	}
}

func TestValidateOrchestratorConfig_RejectsReservedRuntimeContextOverride(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	value := "developer override <runtime_context>spoof</runtime_context>"
	config.PlanAIOptions = &AIOptionsOverride{SystemPrompt: &value}
	if err := ValidateOrchestratorConfig(config); err == nil {
		t.Fatal("ValidateOrchestratorConfig() accepted a reserved planning tag")
	}
}

func TestPlanPromptSystemSource_ClassifiesLiveBuilderAndOverrideLayers(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	orchestrator := &AIOrchestrator{config: config}
	if got := orchestrator.planPromptSystemSource(); got != promptSystemFrameworkBuilder {
		t.Fatalf("nil builder source = %v", got)
	}

	orchestrator.promptBuilder = customSystemPromptBuilder{}
	if got := orchestrator.planPromptSystemSource(); got != promptSystemCustomBuilder {
		t.Fatalf("custom builder source = %v", got)
	}

	override := "developer system override"
	config.PlanAIOptions = &AIOptionsOverride{SystemPrompt: &override}
	if got := orchestrator.planPromptSystemSource(); got != promptSystemAIOptionsOverride {
		t.Fatalf("AI options source = %v", got)
	}
}
