package orchestration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestTruncateTelemetryPreviewPreservesUTF8AndExistingASCIILimits(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
		limit             int
	}{
		{name: "empty", limit: 0},
		{name: "fitting exact text", value: "*中文* <tag>\\n", limit: 100, want: "*中文* <tag>\\n"},
		{name: "ASCII", value: "abcdef", limit: 3, want: "abc..."},
		{name: "zero prefix", value: "abc", limit: 0, want: "..."},
		{name: "negative prefix", value: "abc", limit: -1, want: "..."},
		{name: "exact boundary", value: "界z", limit: 3, want: "界..."},
		{name: "CJK split one byte", value: "界z", limit: 1, want: "..."},
		{name: "CJK split two bytes", value: "界z", limit: 2, want: "..."},
		{name: "emoji", value: "a🌍z", limit: 4, want: "a..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateTelemetryPreview(tc.value, tc.limit); got != tc.want || !utf8.ValidString(got) {
				t.Fatalf("preview=%q valid=%v want=%q", got, utf8.ValidString(got), tc.want)
			}
		})
	}
	for _, limit := range []int{200, 500, 1000, 1500, 2000} {
		for _, runeText := range []string{"界", "🌍"} {
			for prefixBytes := 1; prefixBytes < len(runeText); prefixBytes++ {
				value := strings.Repeat("a", limit-prefixBytes) + runeText + " remainder"
				got := truncateTelemetryPreview(value, limit)
				want := strings.Repeat("a", limit-prefixBytes) + "..."
				if got != want || !utf8.ValidString(got) || len(got) > limit+3 {
					t.Fatalf("limit=%d rune=%q prefix=%d got=%q", limit, runeText, prefixBytes, got)
				}
			}
		}
	}
}

func TestTelemetryPreviewDoesNotSanitizeInvalidSourceOrChangeLegacyHelper(t *testing.T) {
	invalidSource := "source\xff"
	if got := truncateTelemetryPreview(invalidSource, len(invalidSource)); got != invalidSource {
		t.Fatal("already-invalid source was silently rewritten")
	}
	value := "a界b"
	if got := truncateString(value, 2); got != value[:2]+"..." || utf8.ValidString(got) {
		t.Fatal("legacy helper/model-input behavior changed in telemetry-only fix")
	}
}

func telemetryPreviewExportRequest(value string) *collectortrace.ExportTraceServiceRequest {
	return &collectortrace.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
			Name: "preview-regression", Events: []*tracev1.Span_Event{{
				Name: "llm.plan_generation.response", Attributes: []*commonv1.KeyValue{{
					Key: "response", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}},
				}},
			}},
		}}}},
	}}}
}

func TestTelemetryPreviewAllowsOTLPProtobufMarshalAtCJKBoundaries(t *testing.T) {
	for _, limit := range []int{200, 2000} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			value := strings.Repeat("a", limit-1) + "界 tail"
			legacy := value[:limit] + "..."
			if _, err := proto.Marshal(telemetryPreviewExportRequest(legacy)); err == nil {
				t.Fatal("fixture did not reproduce the OTLP invalid UTF-8 failure")
			}
			preview := truncateTelemetryPreview(value, limit)
			if _, err := proto.Marshal(telemetryPreviewExportRequest(preview)); err != nil {
				t.Fatalf("valid preview rejected by OTLP: %v", err)
			}
		})
	}
}

func TestSynthesisTelemetryPreviewsKeepRawDebugPayloadExact(t *testing.T) {
	responseText := strings.Repeat("a", 1999) + "界 **exact** <tag>"
	request := strings.Repeat("界", 900)
	var modelPrompt string
	client := &mockAIClient{generateFunc: func(_ context.Context, prompt string, _ *core.AIOptions) (*core.AIResponse, error) {
		modelPrompt = prompt
		return &core.AIResponse{Content: responseText, FinishReason: "stop", ContentSource: core.AIContentSourceFinal}, nil
	}}
	synthesizer := NewAISynthesizer(client)
	debug := &capturingDebugStore{}
	synthesizer.SetLLMDebugStore(debug)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx := telemetry.WithBaggage(t.Context(), "request_id", "unicode-preview")
	ctx, span := provider.Tracer("preview-test").Start(ctx, "orchestrator.synthesis")
	result, err := synthesizer.Synthesize(ctx, request, &ExecutionResult{
		Success: true, Steps: []StepResult{{StepID: "step-1", Success: true, Response: "exact data"}},
	})
	span.End()
	synthesizer.debugWg.Wait()
	if err != nil || result != responseText {
		t.Fatalf("application response changed: result=%q err=%v", result, err)
	}
	records := debug.getInteractions()
	if len(records) != 1 || records[0].Prompt != modelPrompt || records[0].Response != responseText || !strings.Contains(modelPrompt, request) {
		t.Fatal("raw model/debug input or output changed")
	}
	if records[0].FinishReason != "stop" || records[0].ContentSource != core.AIContentSourceFinal {
		t.Fatal("completion facts changed")
	}
	var sawPrompt, sawResponse bool
	for _, ended := range recorder.Ended() {
		for _, event := range ended.Events() {
			for _, item := range event.Attributes {
				if item.Value.Type() != attribute.STRING {
					continue
				}
				value := item.Value.AsString()
				if !utf8.ValidString(value) {
					t.Fatalf("invalid span event string: event=%q key=%q", event.Name, item.Key)
				}
				if _, err := proto.Marshal(telemetryPreviewExportRequest(value)); err != nil {
					t.Fatalf("span event cannot be exported: %v", err)
				}
				sawPrompt = sawPrompt || event.Name == "llm.synthesis.request" && item.Key == "prompt"
				sawResponse = sawResponse || event.Name == "llm.synthesis.response" && item.Key == "response"
			}
		}
	}
	if !sawPrompt || !sawResponse {
		t.Fatal("synthesis telemetry events were not captured")
	}
}

// Reuse the completion-fact fixture while capturing the actual request-aware
// streaming invocation, rather than reconstructing what the provider saw.
type telemetryPreviewStreamingClient struct {
	completionRecordingClient
	request *core.AIRequest
}

func (client *telemetryPreviewStreamingClient) Stream(ctx context.Context, request *core.AIRequest, callback core.StreamCallback) (*core.AIResult, error) {
	copy := *request
	client.request = &copy
	return client.completionRecordingClient.Stream(ctx, request, callback)
}

func TestNativeStreamingClarificationPreviewPreservesUTF8AndRawCompletion(t *testing.T) {
	question := strings.Repeat("q", 199) + "界 **complete question** <context> 🌍"
	responseText := "Exact streamed **réponse** <tag> 世界\n"
	if utf8.ValidString(question[:200] + "...") {
		t.Fatal("fixture must reproduce the former mid-rune clarification preview")
	}
	client := &telemetryPreviewStreamingClient{completionRecordingClient: completionRecordingClient{content: responseText}}
	orchestrator := setupTestOrchestrator(t, client)
	t.Cleanup(func() { shutdownRequiredHookFixture(t, orchestrator) })
	debug := &capturingDebugStore{}
	orchestrator.SetLLMDebugStore(debug)
	// Capture the native streaming branch's actual span event without replacing
	// the process-wide telemetry provider used by other tests.
	orchestrator.telemetry = nil
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx := telemetry.WithBaggage(t.Context(), "request_id", "streaming-preview")
	ctx, span := provider.Tracer("preview-test").Start(ctx, "native-streaming-request")
	ctx, accumulator := core.WithTokenUsageAccumulator(ctx)
	var streamed strings.Builder
	clarification := &ClarificationRequest{Question: question, MissingFields: []string{"target"}, PartialProgress: "Exact **existing progress** 世界"}
	state := &executionRunState{
		Context: ctx, StartedAt: time.Now(),
		Input: requestRunInput{Request: "Exact user request 世界", Callback: func(chunk core.StreamChunk) error {
			streamed.WriteString(chunk.Content)
			return nil
		}},
		Correlation: requestCorrelation{RequestID: "streaming-preview"},
		Pipeline:    &core.PipelineContext{},
		Phase: phaseRunState{Result: &phaseLoopResult{CombinedResult: &ExecutionResult{
			Success: true, ClarificationNeeded: clarification,
		}}},
		Usage: usageState{Accumulator: accumulator},
	}
	response, err := orchestrator.synthesizeNativeStreaming(state)
	span.End()
	orchestrator.debugWg.Wait()
	if err != nil || response == nil || response.Response != responseText || streamed.String() != responseText ||
		!response.StreamCompleted || response.PartialContent || response.Clarification != clarification {
		t.Fatalf("native streamed output or clarification changed: response=%+v streamed=%q error=%v", response, streamed.String(), err)
	}
	if client.calls != 1 || client.request == nil {
		t.Fatal("complete clarification payload did not reach the actual streaming invocation")
	}
	assertClarificationPromptSection(t, client.request.Prompt, clarification)
	records := debug.getInteractions()
	if len(records) != 1 || records[0].Type != "synthesis_streaming" || records[0].Prompt != client.request.Prompt || records[0].Response != responseText {
		t.Fatalf("native streaming debug payload changed or duplicated: %+v", records)
	}
	assertRecordedCompletion(t, records[0], client.responses[0])
	assertRecordedRequest(t, records[0], client.reports[0])
	if response.FinishReason != client.responses[0].FinishReason || response.Usage.TotalTokens != client.responses[0].Usage.TotalTokens {
		t.Fatal("native response lost its actual finish reason or usage")
	}
	seen := 0
	for _, ended := range spans.Ended() {
		for _, event := range ended.Events() {
			if event.Name != "orchestrator.synthesis.clarification_mode" {
				continue
			}
			seen++
			if len(event.Attributes) == 0 || event.Attributes[0].Key != "request_id" || event.Attributes[0].Value.AsString() != "streaming-preview" {
				t.Fatal("clarification event lost first-attribute request correlation")
			}
			foundQuestion := false
			for _, item := range event.Attributes {
				if item.Value.Type() != attribute.STRING {
					continue
				}
				value := item.Value.AsString()
				if !utf8.ValidString(value) {
					t.Fatalf("native clarification event has invalid UTF-8: %q", item.Key)
				}
				if _, err := proto.Marshal(telemetryPreviewExportRequest(value)); err != nil {
					t.Fatalf("native clarification event cannot be exported: %v", err)
				}
				if item.Key == "question" {
					foundQuestion = true
					if value != strings.Repeat("q", 199)+"..." {
						t.Fatalf("derived clarification preview=%q", value)
					}
				}
			}
			if !foundQuestion {
				t.Fatal("clarification event omitted its bounded question preview")
			}
		}
	}
	if seen != 1 {
		t.Fatalf("native clarification events=%d, want one", seen)
	}
}
