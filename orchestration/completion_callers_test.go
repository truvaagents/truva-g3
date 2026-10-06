package orchestration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type completionRecordingClient struct {
	content     string
	err         error
	legacy      bool
	nilResponse bool
	deferred    bool
	calls       int
	contents    []string
	responses   []*core.AIResponse
}

func (c *completionRecordingClient) GenerateResponse(ctx context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
	c.deferred = telemetry.IsLLMCallRecordingDeferred(ctx)
	c.calls++
	if c.nilResponse {
		return nil, c.err
	}
	content := c.content
	if len(c.contents) >= c.calls {
		content = c.contents[c.calls-1]
	}
	response := &core.AIResponse{Content: content, Model: "fixture-model", Provider: "fixture-provider"}
	if !c.legacy {
		response.ReasoningContent = fmt.Sprintf("attempt %d: exact * reasoning\n世界", c.calls)
		response.FinishReason = "length"
		response.ContentSource = core.AIContentSourceFinal
	}
	c.responses = append(c.responses, response)
	return response, c.err
}

func (c *completionRecordingClient) StreamResponse(ctx context.Context, prompt string, opts *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	response, err := c.GenerateResponse(ctx, prompt, opts)
	if response != nil {
		if callbackErr := callback(core.StreamChunk{Content: response.Content, FinishReason: response.FinishReason}); callbackErr != nil {
			return response, callbackErr
		}
	}
	return response, err
}

func (c *completionRecordingClient) Generate(ctx context.Context, request *core.AIRequest) (*core.AIResult, error) {
	response, err := c.GenerateResponse(ctx, request.Prompt, nil)
	return &core.AIResult{Response: response}, err
}

func (*completionRecordingClient) SupportsStreaming() bool { return true }

func assertRecordedCompletion(t *testing.T, record LLMInteraction, response *core.AIResponse) {
	t.Helper()
	if response == nil {
		response = &core.AIResponse{}
	}
	if record.FinishReason != response.FinishReason || record.ContentSource != response.ContentSource || record.ReasoningContent != response.ReasoningContent {
		t.Fatalf("completion facts lost: finish=%q source=%q reasoning=%q; want finish=%q source=%q reasoning=%q",
			record.FinishReason, record.ContentSource, record.ReasoningContent,
			response.FinishReason, response.ContentSource, response.ReasoningContent)
	}
}

func TestCompletionFactsAtProductionCallers(t *testing.T) {
	for _, caller := range []string{"planning", "continuation", "regeneration", "continuation-regeneration", "correction", "synthesis", "tiered-selection", "streaming"} {
		for _, outcome := range []string{"success", "partial-error", "nil-error", "legacy", "partial-stream"} {
			if outcome == "partial-stream" && caller != "streaming" {
				continue
			}
			t.Run(caller+"/"+outcome, func(t *testing.T) {
				ctx := core.WithRequestID(t.Context(), "completion-caller")
				client := &completionRecordingClient{content: retryTerminalPlan, legacy: outcome == "legacy", nilResponse: outcome == "nil-error"}
				if outcome == "partial-error" || outcome == "nil-error" {
					client.err = errors.New("provider fixture failure")
				}
				if outcome == "partial-stream" {
					client.err = core.ErrStreamPartiallyCompleted
				}
				store := &capturingDebugStore{}
				o := setupTestOrchestrator(t, client)
				o.SetLLMDebugStore(store)
				o.config.PlanParseRetryEnabled = false
				var err error
				results := &ExecutionResult{Success: true, Steps: []StepResult{{StepID: "step-1", AgentName: "test-agent", Response: "data", Success: true}}}
				switch caller {
				case "planning":
					_, err = o.generateExecutionPlan(ctx, "No available work", "completion-caller")
				case "continuation":
					_, err = o.generateContinuationPlan(ctx, "task", "completion-caller", nil, nil, "", 2)
				case "regeneration":
					_, err = o.regeneratePlan(ctx, "task", "completion-caller", errors.New("invalid plan"))
				case "continuation-regeneration":
					_, err = o.regenerateContinuationPlan(ctx, "task", "completion-caller", nil, nil, "", 2, errors.New("invalid plan"), nil)
				case "correction":
					client.content = "{\"value\":\"corrected\"}"
					_, err = o.requestParameterCorrection(ctx, RoutingStep{StepID: "step-1"}, nil, "bad value", nil)
				case "synthesis":
					client.content = "answer"
					_, err = o.synthesizer.Synthesize(ctx, "Explain the result", results)
					o.synthesizer.debugWg.Wait()
				case "tiered-selection":
					client.content = "[\"test-agent/capability_0\"]"
					p := NewTieredCapabilityProvider(setupTestCatalog(25), client, nil)
					p.SetLLMDebugStore(store)
					_, err = p.GetCapabilities(ctx, "test request", nil)
					if shutdownErr := p.Shutdown(ctx); shutdownErr != nil {
						t.Fatal(shutdownErr)
					}
				case "streaming":
					client.content = "streamed answer"
					streamCtx, accumulator := core.WithTokenUsageAccumulator(ctx)
					state := &executionRunState{
						Context: streamCtx, StartedAt: time.Now(),
						Input:       requestRunInput{Request: "task", Callback: func(core.StreamChunk) error { return nil }},
						Correlation: requestCorrelation{RequestID: "completion-caller"},
						Pipeline:    &core.PipelineContext{},
						Phase:       phaseRunState{Result: &phaseLoopResult{CombinedResult: results}},
						Usage:       usageState{Accumulator: accumulator},
					}
					_, err = o.synthesizeNativeStreaming(state)
				}
				o.debugWg.Wait()
				// Tier selection intentionally falls back to the full catalog on error.
				if (err != nil) != (client.err != nil && caller != "tiered-selection" && outcome != "partial-stream") {
					t.Fatalf("outcome changed: %v", err)
				}
				if !client.deferred {
					t.Fatal("generic recording was not deferred")
				}
				records := store.getInteractions()
				if len(records) != 1 || client.calls != 1 {
					t.Fatalf("records=%d calls=%d; want one authoritative record", len(records), client.calls)
				}
				var response *core.AIResponse
				if len(client.responses) > 0 {
					response = client.responses[0]
				}
				assertRecordedCompletion(t, records[0], response)
				if records[0].Success != (client.err == nil || outcome == "partial-stream") {
					t.Fatal("record outcome changed")
				}
			})
		}
	}
}

func TestCompletionFactsFollowHallucinationRetryAttempt(t *testing.T) {
	client := &completionRecordingClient{contents: []string{retryUnknownAgentPlan, retryTerminalPlan}}
	o := setupTestOrchestrator(t, client)
	o.config.HallucinationRetryEnabled, o.config.HallucinationMaxRetries = true, 1
	store := &capturingDebugStore{}
	o.SetLLMDebugStore(store)
	if _, err := o.generateExecutionPlan(t.Context(), "unsupported", "completion-retry"); err != nil {
		t.Fatal(err)
	}
	o.debugWg.Wait()
	seen := map[string]bool{}
	for _, record := range store.getInteractions() {
		if record.Type == "hallucination_detection" {
			// This is a validation audit, not an additional model call.
			assertRecordedCompletion(t, record, nil)
			continue
		}
		matched := false
		for _, response := range client.responses {
			if record.Response == response.Content {
				assertRecordedCompletion(t, record, response)
				seen[record.Response] = true
				matched = true
			}
		}
		if !matched {
			t.Fatalf("unexpected call record: %+v", record)
		}
	}
	if client.calls != 2 || len(seen) != 2 {
		t.Fatalf("attempt coverage: calls=%d seen=%d", client.calls, len(seen))
	}
}
