package orchestration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

var (
	prefixedRequestIDPattern = regexp.MustCompile(`^[a-z0-9-]+-[0-9]+$`)
)

type foundationShortCircuitHook struct {
	response string
	calls    int
}

type foundationInvalidDecisionHook struct{}

func (foundationInvalidDecisionHook) Name() string { return "foundation-invalid-decision" }

func (foundationInvalidDecisionHook) BeforePlanningDecision(
	context.Context,
	*core.PipelineContext,
	core.PipelineGate,
) (*core.PipelineShortCircuitDecision, error) {
	return &core.PipelineShortCircuitDecision{Kind: core.PipelineShortCircuitCache}, nil
}

func (h *foundationShortCircuitHook) Name() string { return "foundation-short-circuit" }

func (h *foundationShortCircuitHook) BeforePlanning(
	_ context.Context,
	pctx *core.PipelineContext,
) (*core.PipelineShortCircuit, error) {
	h.calls++
	pctx.Enrichments["foundation_enrichment"] = "present"
	return &core.PipelineShortCircuit{
		Response: h.response,
		Source:   "foundation-characterization",
	}, nil
}

type foundationStreamingCapabilityClient struct {
	*MockAIClient
	streamCalls int
}

func (c *foundationStreamingCapabilityClient) StreamResponse(
	_ context.Context,
	_ string,
	_ *core.AIOptions,
	_ core.StreamCallback,
) (*core.AIResponse, error) {
	c.streamCalls++
	return nil, errors.New("foundation streaming client should have been short-circuited")
}

func (*foundationStreamingCapabilityClient) SupportsStreaming() bool { return true }

func foundationShortCircuitOrchestrator(
	t *testing.T,
	client core.AIClient,
	response string,
) (*AIOrchestrator, *foundationShortCircuitHook) {
	t.Helper()
	config := DefaultConfig()
	config.EnableTelemetry = false
	config.RequestIDPrefix = "foundation"
	orchestrator := NewAIOrchestrator(config, NewMockDiscovery(), client)
	hook := &foundationShortCircuitHook{response: response}
	orchestrator.pipelineHooks = []core.PipelineHook{hook}
	return orchestrator, hook
}

func TestFoundationCharacterization_PartialLegacyConfigKeepsExecutorDefaults(t *testing.T) {
	orchestrator := NewAIOrchestrator(
		&OrchestratorConfig{RoutingMode: ModeAutonomous},
		NewMockDiscovery(),
		NewMockAIClient(),
	)
	if got := cap(orchestrator.executor.semaphore); got != 5 {
		t.Fatalf("legacy partial-config concurrency = %d, want historical default 5", got)
	}
	if got := orchestrator.executor.httpClient.Timeout; got != 600*time.Second {
		t.Fatalf("legacy partial-config HTTP timeout = %s, want 10m", got)
	}
	if got := orchestrator.executor.maxAttempts; got != 3 {
		t.Fatalf("legacy partial-config max attempts = %d, want 3", got)
	}
	if got := orchestrator.executor.stepRetryBackoff; got != core.DefaultBackoffConfig() {
		t.Fatalf("legacy partial-config backoff = %+v, want %+v", got, core.DefaultBackoffConfig())
	}
}

func TestFoundationCharacterization_BufferedShortCircuitResponseAndRequestID(t *testing.T) {
	orchestrator, hook := foundationShortCircuitOrchestrator(
		t,
		NewMockAIClient(),
		"cached response",
	)

	response, err := orchestrator.ProcessRequest(
		context.Background(),
		"original request",
		map[string]interface{}{"caller_metadata": "preserved"},
	)
	if err != nil {
		t.Fatalf("ProcessRequest() error = %v", err)
	}
	if hook.calls != 1 {
		t.Fatalf("BeforePlanning calls = %d, want 1", hook.calls)
	}
	if !prefixedRequestIDPattern.MatchString(response.RequestID) ||
		!strings.HasPrefix(response.RequestID, "foundation-") {
		t.Fatalf("request ID = %q, want configured prefix", response.RequestID)
	}
	if response.OriginalRequest != "original request" || response.Response != "cached response" {
		t.Fatalf("short-circuit response = %+v", response)
	}
	if response.RoutingMode != orchestrator.config.RoutingMode {
		t.Fatalf("routing mode = %q, want %q", response.RoutingMode, orchestrator.config.RoutingMode)
	}
	if response.Confidence != 1.0 {
		t.Fatalf("confidence = %v, want 1.0", response.Confidence)
	}
	if response.Metadata["caller_metadata"] != "preserved" ||
		response.Metadata["foundation_enrichment"] != "present" {
		t.Fatalf("metadata = %#v", response.Metadata)
	}
	if response.Usage == nil {
		t.Fatal("short-circuit usage must contain the zero accumulator snapshot")
	}
	if response.Usage.PromptTokens != 0 || response.Usage.CompletionTokens != 0 ||
		response.Usage.TotalTokens != 0 {
		t.Fatalf("short-circuit usage = %+v, want zero", *response.Usage)
	}
}

func TestFoundationCharacterization_NativeShortCircuitStreamingUsesByteChunks(t *testing.T) {
	// The two-byte UTF-8 encoding of é straddles the current fixed 50-byte
	// boundary. F3 intentionally replaces this behavior with rune-safe chunks.
	shortCircuitResponse := strings.Repeat("a", 49) + "é" + strings.Repeat("b", 60)
	client := &foundationStreamingCapabilityClient{MockAIClient: NewMockAIClient()}
	orchestrator, hook := foundationShortCircuitOrchestrator(t, client, shortCircuitResponse)

	var chunks []core.StreamChunk
	response, err := orchestrator.ProcessRequestStreaming(
		context.Background(),
		"request",
		nil,
		func(chunk core.StreamChunk) error {
			chunks = append(chunks, chunk)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming() error = %v", err)
	}
	if hook.calls != 1 || client.streamCalls != 0 {
		t.Fatalf("hook calls = %d, provider stream calls = %d", hook.calls, client.streamCalls)
	}
	if !prefixedRequestIDPattern.MatchString(response.RequestID) ||
		!strings.HasPrefix(response.RequestID, "foundation-") {
		t.Fatalf("native streaming request ID = %q, want configured prefix", response.RequestID)
	}
	if len(chunks) != 4 {
		t.Fatalf("chunks = %d, want 3 data chunks plus final chunk", len(chunks))
	}
	if len(chunks[0].Content) != 49 || !utf8.ValidString(chunks[0].Content) {
		t.Fatalf("first chunk bytes/validity = %d/%t, want 49/true", len(chunks[0].Content), utf8.ValidString(chunks[0].Content))
	}
	for index, chunk := range chunks {
		if !utf8.ValidString(chunk.Content) {
			t.Fatalf("chunk %d splits a UTF-8 rune: %q", index, chunk.Content)
		}
	}
	var concatenated strings.Builder
	for _, chunk := range chunks {
		concatenated.WriteString(chunk.Content)
	}
	if concatenated.String() != shortCircuitResponse {
		t.Fatalf("concatenated chunks differ from source: %q", concatenated.String())
	}
	final := chunks[len(chunks)-1]
	if final.Content != "" || final.Delta || final.FinishReason != "stop" || final.Index != 3 {
		t.Fatalf("final chunk = %+v", final)
	}
	if response.ChunksDelivered != 3 || !response.StreamCompleted || response.PartialContent ||
		response.FinishReason != "stop" || response.Response != shortCircuitResponse ||
		response.Confidence != 1.0 {
		t.Fatalf("streaming short-circuit response = %+v", response)
	}
}

func TestFoundationCharacterization_NativeShortCircuitIgnoresCallbackStop(t *testing.T) {
	client := &foundationStreamingCapabilityClient{MockAIClient: NewMockAIClient()}
	orchestrator, _ := foundationShortCircuitOrchestrator(t, client, strings.Repeat("x", 120))
	stopErr := errors.New("stop delivery")
	callbackCalls := 0

	response, err := orchestrator.ProcessRequestStreaming(
		context.Background(),
		"request",
		nil,
		func(core.StreamChunk) error {
			callbackCalls++
			if callbackCalls == 1 {
				return stopErr
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming() error = %v", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("callback calls = %d, want delivery to stop immediately", callbackCalls)
	}
	if response.ChunksDelivered != 1 || response.StreamCompleted || !response.PartialContent ||
		response.FinishReason != "cancelled" || response.Response != strings.Repeat("x", 120) || len(response.Errors) != 1 {
		t.Fatalf("native short-circuit callback-stop response = %+v", response)
	}
}

func TestFoundationLifecycle_SuccessCompletionLogsAndSpansHaveParity(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		spanName  string
		invoke    func(*AIOrchestrator) error
	}{
		{
			name: "buffered", operation: "process_request_complete", spanName: "orchestrator.process_request",
			invoke: func(orchestrator *AIOrchestrator) error {
				_, err := orchestrator.ProcessRequest(t.Context(), "request", nil)
				return err
			},
		},
		{
			name: "streaming", operation: "streaming_complete", spanName: "orchestrator.process_request_streaming",
			invoke: func(orchestrator *AIOrchestrator) error {
				_, err := orchestrator.ProcessRequestStreaming(t.Context(), "request", nil, func(core.StreamChunk) error { return nil })
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			orchestrator, _ := foundationShortCircuitOrchestrator(t, NewMockAIClient(), "response")
			logger := &TestLogger{}
			capture := &conversationCaptureTelemetry{}
			orchestrator.SetLogger(logger)
			orchestrator.telemetry = capture

			if err := test.invoke(orchestrator); err != nil {
				t.Fatalf("request error = %v", err)
			}
			logs := logger.GetLogsByOperation(test.operation)
			if len(logs) != 1 || logs[0].Level != "INFO" ||
				logs[0].Fields["success"] != true || logs[0].Fields["status"] != "success" ||
				logs[0].Fields["termination_reason"] != "completed" {
				t.Fatalf("completion logs = %#v", logs)
			}
			if _, ok := logs[0].Fields["duration_ms"]; !ok {
				t.Fatalf("completion log has no duration_ms: %#v", logs[0].Fields)
			}
			if _, ok := logs[0].Fields["total_duration_ms"]; !ok {
				t.Fatalf("completion log has no compatibility total_duration_ms: %#v", logs[0].Fields)
			}
			if test.name == "streaming" {
				fallbackLogs := logger.GetLogsByOperation("streaming_fallback")
				if len(fallbackLogs) != 1 || fallbackLogs[0].Level != "WARN" ||
					fallbackLogs[0].Fields["request_id"] == "" ||
					fallbackLogs[0].Fields["reason"] != "client_streaming_unsupported" {
					t.Fatalf("streaming fallback logs = %#v", fallbackLogs)
				}
			}
			for _, metric := range capture.records {
				switch metric.name {
				case "orchestrator.requests.total", "orchestrator.requests.success", "orchestrator.latency_ms":
					if metric.labels["module"] != telemetry.ModuleOrchestration {
						t.Fatalf("metric %s labels = %#v, want module=orchestration", metric.name, metric.labels)
					}
				}
			}
			var requestSpans []*mockSpan
			for _, span := range capture.spans {
				if span.name == test.spanName {
					requestSpans = append(requestSpans, span)
				}
			}
			if len(requestSpans) != 1 || !requestSpans[0].ended ||
				requestSpans[0].attributes["status"] != "success" || len(requestSpans[0].errors) != 0 {
				t.Fatalf("request spans = %#v", requestSpans)
			}
		})
	}
}

func TestFoundationLifecycle_SimulatedStreamingUsesOnePrefixedRequest(t *testing.T) {
	const responseBody = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	t.Run("completed response", func(t *testing.T) {
		orchestrator, hook := foundationShortCircuitOrchestrator(t, NewMockAIClient(), responseBody)
		var chunks []core.StreamChunk
		response, err := orchestrator.ProcessRequestStreaming(
			context.Background(),
			"request",
			nil,
			func(chunk core.StreamChunk) error {
				chunks = append(chunks, chunk)
				return nil
			},
		)
		if err != nil {
			t.Fatalf("ProcessRequestStreaming() error = %v", err)
		}
		if hook.calls != 1 {
			t.Fatalf("BeforePlanning calls = %d, want inner buffered call only", hook.calls)
		}
		if !strings.HasPrefix(response.RequestID, "foundation-") {
			t.Fatalf("completed simulated request ID = %q, want configured prefix", response.RequestID)
		}
		if len(chunks) != 3 || response.ChunksDelivered != 2 || !response.StreamCompleted ||
			response.PartialContent || response.Response != responseBody {
			t.Fatalf("completed simulated response = %+v, chunks = %+v", response, chunks)
		}
	})

	t.Run("callback stop preserves same request", func(t *testing.T) {
		orchestrator, _ := foundationShortCircuitOrchestrator(t, NewMockAIClient(), responseBody)
		stopErr := errors.New("stop simulated delivery")
		response, err := orchestrator.ProcessRequestStreaming(
			context.Background(),
			"request",
			nil,
			func(core.StreamChunk) error { return stopErr },
		)
		if err != nil {
			t.Fatalf("ProcessRequestStreaming() error = %v", err)
		}
		if !strings.HasPrefix(response.RequestID, "foundation-") {
			t.Fatalf("stopped simulated request ID = %q, want outer configured prefix", response.RequestID)
		}
		if response.ChunksDelivered != 1 || response.StreamCompleted || !response.PartialContent ||
			response.FinishReason != "cancelled" || response.Response != responseBody || len(response.Errors) != 1 {
			t.Fatalf("stopped simulated response = %+v", response)
		}
	})
}

func TestFoundationLifecycle_FailuresEmitExactlyOneDeliveryCompletion(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		spanName  string
		invoke    func(*AIOrchestrator) error
	}{
		{
			name: "buffered", operation: "process_request_complete", spanName: "orchestrator.process_request",
			invoke: func(orchestrator *AIOrchestrator) error {
				_, err := orchestrator.ProcessRequest(t.Context(), "request", nil)
				return err
			},
		},
		{
			name: "streaming", operation: "streaming_complete", spanName: "orchestrator.process_request_streaming",
			invoke: func(orchestrator *AIOrchestrator) error {
				_, err := orchestrator.ProcessRequestStreaming(t.Context(), "request", nil, func(core.StreamChunk) error { return nil })
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := NewDefaultOrchestratorConfig()
			config.EnableTelemetry = false
			orchestrator := NewAIOrchestrator(config, NewMockDiscovery(), NewMockAIClient())
			logger := &TestLogger{}
			capture := &conversationCaptureTelemetry{}
			orchestrator.SetLogger(logger)
			orchestrator.telemetry = capture
			orchestrator.pipelineHooks = []core.PipelineHook{foundationInvalidDecisionHook{}}

			if err := test.invoke(orchestrator); err == nil {
				t.Fatal("invalid decision unexpectedly completed")
			}
			logs := logger.GetLogsByOperation(test.operation)
			if len(logs) != 1 {
				t.Fatalf("%s completion logs = %d, want exactly one", test.operation, len(logs))
			}
			if logs[0].Level != "ERROR" || logs[0].Fields["success"] != false ||
				logs[0].Fields["status"] != "error" ||
				logs[0].Fields["termination_reason"] != "before_planning_failed" ||
				logs[0].Fields["error_type"] != "request_failed" ||
				logs[0].Fields["error"] != "orchestration request failed" {
				t.Fatalf("completion fields = %#v", logs[0].Fields)
			}
			if _, ok := logs[0].Fields["duration_ms"]; !ok {
				t.Fatalf("completion log has no duration_ms: %#v", logs[0].Fields)
			}
			if _, ok := logs[0].Fields["total_duration_ms"]; !ok {
				t.Fatalf("completion log has no compatibility total_duration_ms: %#v", logs[0].Fields)
			}
			var requestSpans []*mockSpan
			for _, span := range capture.spans {
				if span.name == test.spanName {
					requestSpans = append(requestSpans, span)
				}
			}
			if len(requestSpans) != 1 {
				t.Fatalf("request spans = %d, want exactly one", len(requestSpans))
			}
			requestSpan := requestSpans[0]
			if !requestSpan.ended || requestSpan.attributes["status"] != "error" ||
				requestSpan.attributes["termination_reason"] != "before_planning_failed" ||
				requestSpan.attributes["error_type"] != "request_failed" || len(requestSpan.errors) != 1 {
				t.Fatalf("request span = %#v", requestSpan)
			}
			if strings.Contains(requestSpan.errors[0].Error(), "invalid short circuit") {
				t.Fatalf("request span exposed implementation error: %v", requestSpan.errors[0])
			}
			metrics := orchestrator.GetMetrics()
			if metrics.TotalRequests != 1 || metrics.FailedRequests != 1 || metrics.SuccessfulRequests != 0 {
				t.Fatalf("request metrics = %+v, want exactly one failure", metrics)
			}
		})
	}
}

type foundationContextCapturingTransport struct {
	mu              sync.Mutex
	baggageIDs      []string
	contextValueIDs []string
}

func (t *foundationContextCapturingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.baggageIDs = append(t.baggageIDs, telemetry.GetBaggage(request.Context())["request_id"])
	t.contextValueIDs = append(t.contextValueIDs, GetRequestID(request.Context()))
	t.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"result":"success"}`)),
		Request:    request,
	}, nil
}

func (t *foundationContextCapturingTransport) requestIDs(tst *testing.T) (string, string) {
	tst.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.baggageIDs) != 1 || len(t.contextValueIDs) != 1 {
		tst.Fatalf("captured IDs: baggage=%v context=%v", t.baggageIDs, t.contextValueIDs)
	}
	return t.baggageIDs[0], t.contextValueIDs[0]
}

func foundationDirectPlan() *RoutingPlan {
	return &RoutingPlan{
		PlanID:          "foundation-direct-plan",
		OriginalRequest: "direct request",
		Mode:            ModeWorkflow,
		Steps: []RoutingStep{{
			StepID:      "step-1",
			AgentName:   "test-agent",
			Instruction: "test direct execution",
			Metadata: map[string]interface{}{
				"capability": "test_capability",
			},
		}},
	}
}

func TestFoundationCharacterization_DirectEntryPointIDsAndLifecycleExclusions(t *testing.T) {
	t.Run("ExecutePlan", func(t *testing.T) {
		orchestrator, aiClient := createTestOrchestrator(t)
		orchestrator.config.RequestIDPrefix = "foundation"
		orchestrator.catalog.agents["test-1"].Capabilities[0].Endpoint = "/process"
		hook := &allStagesHook{name: "direct-plan-hook"}
		orchestrator.pipelineHooks = []core.PipelineHook{hook}
		transport := &foundationContextCapturingTransport{}
		orchestrator.executor.httpClient = &http.Client{Transport: transport}

		result, err := orchestrator.ExecutePlan(context.Background(), foundationDirectPlan())
		if err != nil {
			t.Fatalf("ExecutePlan() error = %v", err)
		}
		if result == nil {
			t.Fatal("ExecutePlan() returned nil result")
		}
		if len(result.Steps) != 1 || !result.Steps[0].Success {
			t.Fatalf("ExecutePlan() result = %+v", result)
		}
		baggageID, contextID := transport.requestIDs(t)
		if baggageID != contextID || !strings.HasPrefix(baggageID, "foundation-") {
			t.Fatalf("direct IDs: baggage=%q context=%q", baggageID, contextID)
		}
		if hook.beforePlanCalls != 0 || hook.afterPlanCalls != 0 ||
			hook.afterExecCalls != 0 || hook.afterSynthCalls != 0 {
			t.Fatalf("ExecutePlan unexpectedly ran pipeline hooks: %+v", hook)
		}
		if len(aiClient.calls) != 0 {
			t.Fatalf("ExecutePlan AI calls = %d, want 0", len(aiClient.calls))
		}
	})

	t.Run("ExecutePlanWithSynthesis", func(t *testing.T) {
		orchestrator, aiClient := createTestOrchestrator(t)
		orchestrator.config.RequestIDPrefix = "foundation"
		orchestrator.catalog.agents["test-1"].Capabilities[0].Endpoint = "/process"
		hook := &allStagesHook{name: "direct-synthesis-hook"}
		orchestrator.pipelineHooks = []core.PipelineHook{hook}
		transport := &foundationContextCapturingTransport{}
		orchestrator.executor.httpClient = &http.Client{Transport: transport}

		response, err := orchestrator.ExecutePlanWithSynthesis(
			context.Background(),
			foundationDirectPlan(),
			"direct request",
		)
		if err != nil {
			t.Fatalf("ExecutePlanWithSynthesis() error = %v", err)
		}
		if len(response.Steps) != 1 || !response.Steps[0].Success {
			t.Fatalf("ExecutePlanWithSynthesis() response = %+v", response)
		}
		baggageID, contextID := transport.requestIDs(t)
		if baggageID != response.RequestID || contextID != response.RequestID ||
			!strings.HasPrefix(response.RequestID, "foundation-") {
			t.Fatalf("direct synthesis IDs: response=%q baggage=%q context=%q", response.RequestID, baggageID, contextID)
		}
		if hook.beforePlanCalls != 0 || hook.afterPlanCalls != 0 ||
			hook.afterExecCalls != 0 || hook.afterSynthCalls != 0 {
			t.Fatalf("ExecutePlanWithSynthesis unexpectedly ran pipeline hooks: %+v", hook)
		}
		if len(aiClient.calls) != 1 {
			t.Fatalf("ExecutePlanWithSynthesis AI calls = %d, want synthesis only", len(aiClient.calls))
		}
	})
}
