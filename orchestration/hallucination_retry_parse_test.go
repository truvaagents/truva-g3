package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

const retryUnknownAgentPlan = `{"steps":[{"step_id":"step-1","agent_name":"unregistered-agent","instruction":"Unsupported action","metadata":{"capability":"unavailable","parameters":{}}}],"terminal":false}`
const retryTerminalPlan = `{"terminal":true,"steps":[]}`

type retryCountingProvider struct {
	CapabilityProvider
	calls int
}

func (provider *retryCountingProvider) GetCapabilities(ctx context.Context, request string, metadata map[string]interface{}) (*CapabilityResult, error) {
	provider.calls++
	return provider.CapabilityProvider.GetCapabilities(ctx, request, metadata)
}

func setupParseRecovery(t *testing.T, client *promptCapturingAIClient, retries int) (*AIOrchestrator, *retryCountingProvider, *MockRoundTripper) {
	t.Helper()
	o := setupTestOrchestrator(t, client)
	o.config.CacheEnabled, o.config.PlanParseRetryEnabled, o.config.PlanRefinementEnabled = false, false, false
	o.config.IterativePlanning.Enabled = false
	o.config.HallucinationRetryEnabled, o.config.HallucinationMaxRetries = true, retries
	provider := &retryCountingProvider{CapabilityProvider: o.capabilityProvider}
	o.SetCapabilityProvider(provider)
	transport := NewMockRoundTripper()
	o.executor.httpClient = &http.Client{Transport: transport}
	return o, provider, transport
}

func TestHallucinationRetryParseCorrectionUsesLatestFailure(t *testing.T) {
	for _, tc := range []struct{ name, invalid, feedback string }{
		{"syntax", `{"steps":[`, "unexpected end of JSON input"},
		{"intent", `{"execution_plan":{"status":"done"}}`, "found_top_level_keys"},
		{"type", `{"terminal":"true","steps":[]}`, "cannot unmarshal string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &promptCapturingAIClient{responses: []string{retryUnknownAgentPlan, tc.invalid, retryTerminalPlan, "Capability unavailable."}}
			o, provider, transport := setupParseRecovery(t, client, 2)
			response, err := o.ProcessRequest(context.Background(), "Unsupported action", nil)
			if err != nil || response == nil || response.Response != "Capability unavailable." {
				t.Fatalf("recovery failed: response=%v err=%v", response, err)
			}
			if client.callCount != 4 || provider.calls != 2 || transport.GetCallCount() != 0 {
				t.Fatalf("budget/catalog/dispatch changed: calls=%d selections=%d dispatches=%d", client.callCount, provider.calls, transport.GetCallCount())
			}
			correction := client.capturedPrompts[2]
			if !strings.Contains(correction, "<parse_error>") || !strings.Contains(correction, tc.feedback) ||
				!strings.Contains(correction, "Agent: test-agent") || strings.Contains(correction, "You used agent '") {
				t.Fatalf("latest error/catalog missing or stale rejection repeated: %s", correction)
			}
		})
	}
}

func TestHallucinationRetryExhaustionReportsLatestCause(t *testing.T) {
	for _, tc := range []struct{ name, last, reason string }{
		{"syntax", `{"terminal":`, "plan_decode_failed"},
		{"intent", `{"execution_plan":{"status":"done"}}`, "unrecognized_plan_envelope"},
		{"type", `{"terminal":"true"}`, "plan_decode_failed"},
		{"new unknown agent", strings.ReplaceAll(retryUnknownAgentPlan, "unregistered-agent", "another-unregistered-agent"), "hallucination"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &promptCapturingAIClient{responses: []string{retryUnknownAgentPlan, `{"steps":[`, tc.last}}
			o, provider, transport := setupParseRecovery(t, client, 2)
			plan, err := o.generateExecutionPlan(context.Background(), "Unsupported action", "retry-exhaustion")
			if plan != nil || err == nil || client.callCount != 3 || provider.calls != 2 || transport.GetCallCount() != 0 {
				t.Fatalf("exhaustion changed: plan=%v err=%v calls=%d selections=%d dispatches=%d", plan, err, client.callCount, provider.calls, transport.GetCallCount())
			}
			if tc.reason == "hallucination" {
				if !strings.Contains(err.Error(), "another-unregistered-agent") || strings.Contains(err.Error(), "plan correction failed") {
					t.Fatalf("valid parse did not clear earlier parse failure: %v", err)
				}
				return
			}
			if !strings.Contains(err.Error(), "plan correction failed after 2 retries") || strings.Contains(err.Error(), "LLM hallucinated agent") || planParseReason(err) != tc.reason {
				t.Fatalf("stale or wrong final cause: %v", err)
			}
			if tc.name == "syntax" {
				var typed *json.SyntaxError
				if !errors.As(err, &typed) {
					t.Fatalf("syntax error identity lost: %v", err)
				}
			}
			if tc.name == "type" {
				var typed *json.UnmarshalTypeError
				if !errors.As(err, &typed) {
					t.Fatalf("type error identity lost: %v", err)
				}
			}
		})
	}
}

func TestHallucinationRetryCorrectionRespectsBoundaries(t *testing.T) {
	t.Run("feedback limit", func(t *testing.T) {
		client := &promptCapturingAIClient{responses: []string{retryUnknownAgentPlan, `{"execution_plan":{}}`}}
		o, _, transport := setupParseRecovery(t, client, 3)
		o.config.IterativePlanning.RegenerationFeedbackMaxBytes = 1
		_, err := o.generateExecutionPlan(context.Background(), "Unsupported action", "retry-feedback-limit")
		if !errors.Is(err, errRegenerationContextTooLarge) || client.callCount != 2 || transport.GetCallCount() != 0 {
			t.Fatalf("feedback admission did not stop before AI/dispatch: %v calls=%d", err, client.callCount)
		}
	})
	t.Run("provider cancellation", func(t *testing.T) {
		client := &promptCapturingAIClient{responses: []string{retryUnknownAgentPlan, `{"steps":[`, retryTerminalPlan}, errors: []error{nil, nil, context.Canceled}}
		o, provider, transport := setupParseRecovery(t, client, 4)
		_, err := o.generateExecutionPlan(context.Background(), "Unsupported action", "retry-cancelled")
		if !errors.Is(err, context.Canceled) || client.callCount != 3 || provider.calls != 2 || transport.GetCallCount() != 0 {
			t.Fatalf("provider cancellation was retried or reclassified: %v calls=%d", err, client.callCount)
		}
	})
	t.Run("retry disabled", func(t *testing.T) {
		client := &promptCapturingAIClient{responses: []string{retryUnknownAgentPlan}}
		o, provider, transport := setupParseRecovery(t, client, 3)
		o.config.HallucinationRetryEnabled = false
		_, err := o.generateExecutionPlan(context.Background(), "Unsupported action", "retry-disabled")
		if err == nil || !strings.Contains(err.Error(), "after 0 retries") || client.callCount != 1 || provider.calls != 1 || transport.GetCallCount() != 0 {
			t.Fatalf("disabled retry contract changed: %v calls=%d", err, client.callCount)
		}
	})
}

func TestHallucinationRetryPhaseRegenerationReceivesLatestCause(t *testing.T) {
	// The phase loop already owns one fallback regeneration after generation
	// fails. Preserve that recovery and give it the current cause, not a stale
	// unavailable-agent error from the beginning of the inner episode.
	client := &promptCapturingAIClient{responses: []string{
		retryUnknownAgentPlan, `{"steps":[`, `{"execution_plan":{"status":"done"}}`,
		retryTerminalPlan, "Capability unavailable.",
	}}
	o, provider, transport := setupParseRecovery(t, client, 2)
	response, err := o.ProcessRequest(context.Background(), "Unsupported action", nil)
	if err != nil || response == nil || response.Response != "Capability unavailable." || client.callCount != 5 || provider.calls != 2 || transport.GetCallCount() != 0 {
		t.Fatalf("phase fallback changed: response=%v err=%v calls=%d selections=%d dispatches=%d", response, err, client.callCount, provider.calls, transport.GetCallCount())
	}
	prompt := client.capturedPrompts[3]
	if !strings.Contains(prompt, "plan correction failed after 2 retries") || !strings.Contains(prompt, "unrecognized_plan_envelope") ||
		!strings.Contains(prompt, "execution_plan") || strings.Contains(prompt, "LLM hallucinated agent") {
		t.Fatalf("phase regeneration received stale or incomplete feedback: %s", prompt)
	}
}

type retryContextPromptBuilder struct {
	PromptBuilder
	builds int
}

func (builder *retryContextPromptBuilder) BuildPlanningPrompt(ctx context.Context, input PromptInput) (string, error) {
	builder.builds++
	return builder.PromptBuilder.BuildPlanningPrompt(ctx, input)
}

func (builder *retryContextPromptBuilder) BuildSystemPrompt(context.Context, PromptInput) string {
	return fmt.Sprintf("Selected-context system %d", builder.builds)
}

type retrySystemRecordingClient struct {
	*promptCapturingAIClient
	systems []string
}

func (client *retrySystemRecordingClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	client.systems = append(client.systems, options.SystemPrompt)
	return client.promptCapturingAIClient.GenerateResponse(ctx, prompt, options)
}

func TestHallucinationRetryParseCorrectionPreservesSelectedSystem(t *testing.T) {
	baseClient := &promptCapturingAIClient{responses: []string{retryUnknownAgentPlan, `{"steps":[`, retryTerminalPlan}}
	o, provider, _ := setupParseRecovery(t, baseClient, 2)
	client := &retrySystemRecordingClient{promptCapturingAIClient: baseClient}
	o.aiClient = client
	o.SetPromptBuilder(&retryContextPromptBuilder{PromptBuilder: o.promptBuilder})
	plan, err := o.generateExecutionPlan(context.Background(), "Unsupported action", "retry-system-context")
	if err != nil || plan == nil || provider.calls != 2 || len(client.systems) != 3 {
		t.Fatalf("context test did not recover: plan=%v err=%v selections=%d systems=%v", plan, err, provider.calls, client.systems)
	}
	if !strings.Contains(client.systems[0], "Selected-context system 1") ||
		!strings.Contains(client.systems[1], "Selected-context system 2") || client.systems[1] != client.systems[2] {
		t.Fatalf("retry/correction used a stale or altered selected system: %v", client.systems)
	}
}

type retryChangingCatalogProvider struct{ calls int }

func (provider *retryChangingCatalogProvider) GetCapabilities(context.Context, string, map[string]interface{}) (*CapabilityResult, error) {
	provider.calls++
	name := fmt.Sprintf("available-agent-%d", provider.calls)
	return &CapabilityResult{FormattedInfo: "Agent: " + name, AgentNames: []string{name}}, nil
}

func TestHallucinationRetryDiagnosticUsesRejectedAttempt(t *testing.T) {
	for _, malformedBetween := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed_between=%t", malformedBetween), func(t *testing.T) {
			first := retryUnknownAgentPlan
			second := strings.ReplaceAll(first, "unregistered-agent", "another-unregistered-agent")
			responses := []string{first}
			if malformedBetween {
				responses = append(responses, `{"steps":[`)
			}
			secondIndex := len(responses)
			responses = append(responses, second, retryTerminalPlan)
			baseClient := &promptCapturingAIClient{responses: responses}
			o, _, transport := setupParseRecovery(t, baseClient, len(responses)-1)
			client := &retrySystemRecordingClient{promptCapturingAIClient: baseClient}
			o.aiClient = client
			catalog := &retryChangingCatalogProvider{}
			o.SetCapabilityProvider(catalog)
			o.SetPromptBuilder(&retryContextPromptBuilder{PromptBuilder: o.promptBuilder})
			store := &mockLLMDebugStore{}
			o.SetLLMDebugStore(store)
			plan, err := o.generateExecutionPlan(context.Background(), "Unsupported action", "retry-diagnostic-provenance")
			o.debugWg.Wait()
			if err != nil || plan == nil || client.callCount != len(responses) || catalog.calls != 3 || transport.GetCallCount() != 0 {
				t.Fatalf("recovery changed: plan=%v err=%v calls=%d catalogs=%d", plan, err, client.callCount, catalog.calls)
			}
			detections := 0
			seenAttempts := make(map[int]bool)
			for _, interaction := range store.interactions {
				if interaction.Type != "hallucination_detection" {
					continue
				}
				detections++
				index, selected := 0, "available-agent-1"
				if strings.Contains(interaction.Error, "another-unregistered-agent") {
					index, selected = secondIndex, "available-agent-2"
				}
				if seenAttempts[index] || interaction.Prompt != client.capturedPrompts[index] || interaction.SystemPrompt != client.systems[index] || interaction.Response != responses[index] {
					t.Errorf("rejection diagnostic is not paired with attempt %d", index+1)
				}
				seenAttempts[index] = true
				if !strings.Contains(interaction.Error, "allowed_agents=["+selected+"]") || interaction.Attempt != index+1 {
					t.Errorf("rejection catalog/attempt is stale: %+v", interaction)
				}
				if interaction.TotalTokens != 0 || interaction.DurationMs != 0 || interaction.Provider != "" {
					t.Error("deterministic rejection was recorded as another provider call")
				}
			}
			if detections != 2 {
				t.Fatalf("got %d detections, want exactly two rejected agents", detections)
			}
			if !strings.Contains(client.capturedPrompts[len(responses)-1], "You used agent 'another-unregistered-agent'") {
				t.Fatal("model lost the latest unavailable-agent feedback")
			}
		})
	}
}
