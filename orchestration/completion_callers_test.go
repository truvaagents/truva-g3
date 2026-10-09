package orchestration

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
	reports     []*core.AIRequestReport
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
	response.Usage = core.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}
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
	return c.result(response), err
}

func (c *completionRecordingClient) Stream(ctx context.Context, request *core.AIRequest, callback core.StreamCallback) (*core.AIResult, error) {
	response, err := c.StreamResponse(ctx, request.Prompt, nil, callback)
	return c.result(response), err
}

func (c *completionRecordingClient) result(response *core.AIResponse) *core.AIResult {
	var report *core.AIRequestReport
	if !c.legacy {
		report = &core.AIRequestReport{
			Provider: "fixture-provider", RequestedModel: "default", ResolvedModel: fmt.Sprintf("resolved-%d", c.calls),
			EffectiveMaxTokens: core.SetAIParameter(90000), EffectiveTemperature: core.SetAIParameter(float32(0.2)),
			Fingerprint: fmt.Sprintf("policy-%d", c.calls), Stable: true,
			Adjustments: []core.AIRequestAdjustment{{Source: "wire-profile", Rule: "reasoning-headroom", Path: "/max_tokens", Action: "scale"}},
		}
	}
	c.reports = append(c.reports, report)
	return &core.AIResult{Response: response, RequestReport: report}
}

func assertRecordedRequest(t *testing.T, record LLMInteraction, report *core.AIRequestReport) {
	t.Helper()
	if report == nil {
		if record.PolicyStable || record.PolicyFingerprint != "" || len(record.Adjustments) != 0 {
			t.Fatal("fabricated provider request evidence")
		}
		return
	}
	if record.RequestedModel != report.RequestedModel || record.EffectiveModel != report.ResolvedModel ||
		record.PolicyFingerprint != report.Fingerprint || record.PolicyStable != report.Stable ||
		!reflect.DeepEqual(record.Adjustments, report.Adjustments) || record.MaxTokens != 90000 || record.Temperature != 0.2 {
		t.Fatalf("provider request evidence lost: %+v; report: %+v", record, report)
	}
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
		for _, outcome := range []string{"success", "partial-error", "nil-error", "legacy", "partial-stream", "wrapped-partial-stream", "nil-partial-stream"} {
			partialStream := outcome == "partial-stream" || outcome == "wrapped-partial-stream"
			if (partialStream || outcome == "nil-partial-stream") && caller != "streaming" {
				continue
			}
			t.Run(caller+"/"+outcome, func(t *testing.T) {
				ctx := core.WithRequestID(t.Context(), "completion-caller")
				client := &completionRecordingClient{content: retryTerminalPlan, legacy: outcome == "legacy", nilResponse: outcome == "nil-error" || outcome == "nil-partial-stream"}
				if outcome == "partial-error" || outcome == "nil-error" {
					client.err = errors.New("provider fixture failure")
				}
				if partialStream || outcome == "nil-partial-stream" {
					client.err = core.ErrStreamPartiallyCompleted
				}
				if outcome == "wrapped-partial-stream" {
					client.err = fmt.Errorf("provider: %w", errors.Join(client.err, errors.New("connection reset")))
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
					var streamed *StreamingOrchestratorResponse
					streamed, err = o.synthesizeNativeStreaming(state)
					if partialStream && (streamed == nil || !streamed.PartialContent || streamed.StreamCompleted || streamed.Response != client.content || streamed.Usage.TotalTokens != 30) {
						t.Fatalf("partial content or usage lost: %+v, %v", streamed, err)
					}
				}
				o.debugWg.Wait()
				// Tier selection intentionally falls back to the full catalog on error.
				if (err != nil) != (client.err != nil && caller != "tiered-selection" && !partialStream) {
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
				assertRecordedRequest(t, records[0], client.reports[0])
				if records[0].Success != (client.err == nil) {
					t.Fatal("record outcome changed")
				}
			})
		}
	}
}

func TestPartialStreamingDiagnosticsPreserveCause(t *testing.T) {
	for _, test := range []struct {
		name       string
		cause      error
		wantReason string
		legacy     bool
	}{
		{name: "bare sentinel", wantReason: "interrupted", legacy: true},
		{name: "transport reset", cause: errors.New("connection reset by peer: upstream detail\nexact * bytes"), wantReason: "interrupted"},
		{name: "cancellation", cause: context.Canceled, wantReason: "cancelled"},
		{name: "deadline", cause: context.DeadlineExceeded, wantReason: "deadline_exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			callErr := core.ErrStreamPartiallyCompleted
			if test.cause != nil {
				callErr = fmt.Errorf("provider stream: %w", errors.Join(callErr, test.cause))
			}
			client := &completionRecordingClient{content: "partial answer", err: callErr, legacy: test.legacy}
			o := setupTestOrchestrator(t, client)
			store := &capturingDebugStore{}
			o.SetLLMDebugStore(store)
			traces := &pipelineHookSpanTelemetry{}
			o.telemetry = traces
			ctx, accumulator := core.WithTokenUsageAccumulator(core.WithRequestID(t.Context(), "partial-diagnostic"))
			chunks := 0
			state := &executionRunState{
				Context: ctx, StartedAt: time.Now(),
				Input:       requestRunInput{Request: "task", Callback: func(core.StreamChunk) error { chunks++; return nil }},
				Correlation: requestCorrelation{RequestID: "partial-diagnostic"},
				Pipeline:    &core.PipelineContext{},
				Phase:       phaseRunState{Result: &phaseLoopResult{CombinedResult: &ExecutionResult{Success: true}}},
				Usage:       usageState{Accumulator: accumulator},
			}
			response, err := o.synthesizeNativeStreaming(state)
			o.debugWg.Wait()
			if err != nil || response == nil {
				t.Fatalf("partial delivery: response=%+v error=%v", response, err)
			}
			if !response.PartialContent || response.StreamCompleted || response.Response != client.content ||
				response.FinishReason != test.wantReason || response.Usage.TotalTokens != 30 || chunks != 1 || response.ChunksDelivered != chunks {
				t.Fatalf("partial response evidence changed: %+v", response)
			}
			if !reflect.DeepEqual(response.Errors, []string{callErr.Error()}) {
				t.Fatalf("response lost exact cause: %q", response.Errors)
			}
			records := store.getInteractions()
			if client.calls != 1 || len(records) != 1 {
				t.Fatalf("unexpected retry or duplicate recording: calls=%d records=%d", client.calls, len(records))
			}
			record := records[0]
			if record.Success || record.Error != callErr.Error() || record.Response != client.content || record.TotalTokens != 30 {
				t.Fatalf("incorrect partial call record: %+v", record)
			}
			assertRecordedCompletion(t, record, client.responses[0])
			assertRecordedRequest(t, record, client.reports[0])
			if len(traces.spans) != 1 {
				t.Fatalf("synthesis spans=%d", len(traces.spans))
			}
			span := traces.spans[0]
			if span.name != "orchestrator.synthesis" || span.endCount != 1 || span.attributesAfterEnd ||
				span.attributes["synthesis.partial"] != true || span.attributes["synthesis.interruption_reason"] != test.wantReason ||
				len(span.errors) != 1 || span.errors[0] != core.ErrStreamPartiallyCompleted {
				t.Fatalf("incorrect/broad span diagnostic: %+v", span)
			}
		})
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
		for i, response := range client.responses {
			if record.Response == response.Content {
				assertRecordedCompletion(t, record, response)
				assertRecordedRequest(t, record, client.reports[i])
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
