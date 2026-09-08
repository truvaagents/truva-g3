package orchestration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestKnowledgeExtractionHook_Name(t *testing.T) {
	hook, _ := NewKnowledgeExtractionHook(
		&core.MockSharedKnowledge{}, &core.MockEmbeddingClient{}, &mockAIClient{},
		"agent", "domain",
	)
	assert.Equal(t, "knowledge-extraction", hook.Name())
}

func TestKnowledgeExtractionHook_FailFastNilParams(t *testing.T) {
	_, err := NewKnowledgeExtractionHook(nil, nil, nil, "agent", "domain")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "knowledge store is required")

	_, err = NewKnowledgeExtractionHook(&core.MockSharedKnowledge{}, nil, nil, "agent", "domain")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "embedding client is required")

	_, err = NewKnowledgeExtractionHook(&core.MockSharedKnowledge{}, &core.MockEmbeddingClient{}, nil, "agent", "domain")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AI client is required")
}

func TestKnowledgeExtractionHook_ScopesComponentAwareLogger(t *testing.T) {
	logger := &mockComponentAwareLogger{}
	hook, err := NewKnowledgeExtractionHook(
		&core.MockSharedKnowledge{},
		&core.MockEmbeddingClient{},
		&mockAIClient{},
		"agent",
		"domain",
		WithExtractionLogger(logger),
	)
	if err != nil {
		t.Fatalf("NewKnowledgeExtractionHook() error = %v", err)
	}
	t.Cleanup(hook.Close)
	if logger.component != "framework/orchestration" {
		t.Fatalf("logger component = %q, want framework/orchestration", logger.component)
	}
}

func TestKnowledgeExtractionHook_AsyncWorkUsesLinkedSpanAndCorrelationBaggage(t *testing.T) {
	originalProvider := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(originalProvider)
		_ = provider.Shutdown(context.Background())
	})

	requestIDObserved := ""
	aiClient := &mockAIClient{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			requestIDObserved = telemetry.GetBaggage(ctx)["request_id"]
			return &core.AIResponse{Content: `[]`}, nil
		},
	}
	hook, err := NewKnowledgeExtractionHook(
		&core.MockSharedKnowledge{},
		&core.MockEmbeddingClient{},
		aiClient,
		"agent",
		"domain",
	)
	if err != nil {
		t.Fatalf("NewKnowledgeExtractionHook() error = %v", err)
	}

	ctx := telemetry.WithBaggage(t.Context(), "request_id", "request-linked-hook")
	ctx = WithRequestID(ctx, "request-linked-hook")
	ctx, requestSpan := provider.Tracer("knowledge-extraction-test").Start(ctx, "request")
	requestSpanContext := requestSpan.SpanContext()
	response, hookErr := hook.AfterSynthesis(
		ctx,
		&core.PipelineContext{Request: "extract reusable knowledge"},
		"no reusable knowledge",
	)
	requestSpan.End()
	if hookErr != nil || response != "no reusable knowledge" {
		t.Fatalf("AfterSynthesis() = %q, %v", response, hookErr)
	}
	hook.Close()

	if requestIDObserved != "request-linked-hook" {
		t.Fatalf("async request_id baggage = %q", requestIDObserved)
	}
	var asyncSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "pipeline.hook.async.knowledge-extraction" {
			asyncSpan = span
			break
		}
	}
	if asyncSpan == nil {
		t.Fatal("linked knowledge-extraction span was not recorded")
	}
	if asyncSpan.Parent().IsValid() {
		t.Fatalf("async span unexpectedly has parent %v", asyncSpan.Parent())
	}
	links := asyncSpan.Links()
	if len(links) != 1 || links[0].SpanContext.TraceID() != requestSpanContext.TraceID() ||
		links[0].SpanContext.SpanID() != requestSpanContext.SpanID() {
		t.Fatalf("async span links = %#v, want link to %s/%s", links, requestSpanContext.TraceID(), requestSpanContext.SpanID())
	}
	attrs := make(map[string]string)
	for _, attr := range asyncSpan.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	if attrs["request_id"] != "request-linked-hook" ||
		attrs["pipeline.hook.name"] != "knowledge-extraction" ||
		attrs["pipeline.hook.phase"] != PipelineHookPhaseAfterSynthesis {
		t.Fatalf("async span attributes = %#v", attrs)
	}
}

func TestKnowledgeExtractionHook_NeverMutatesResponse(t *testing.T) {
	knowledge := &core.MockSharedKnowledge{}
	embedder := &core.MockEmbeddingClient{}
	aiClient := &mockAIClient{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{Content: `[]`}, nil
		},
	}

	hook, _ := NewKnowledgeExtractionHook(knowledge, embedder, aiClient, "agent", "domain")
	resp, err := hook.AfterSynthesis(context.Background(), &core.PipelineContext{Request: "test"}, "original response")
	assert.NoError(t, err)
	assert.Equal(t, "original response", resp, "response must never be mutated")
}

func TestKnowledgeExtractionHook_ExtractsAndStores(t *testing.T) {
	storedFragments := make(chan core.KnowledgeFragment, 4)
	knowledge := &core.MockSharedKnowledge{
		StoreFn: func(ctx context.Context, fragment core.KnowledgeFragment) error {
			storedFragments <- fragment
			return nil
		},
	}

	embedder := &core.MockEmbeddingClient{
		GenerateEmbedFn: func(ctx context.Context, texts []string, options *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
			return &core.EmbeddingResponse{
				Embeddings: [][]float32{{0.1, 0.2, 0.3}},
			}, nil
		},
	}

	aiClient := &mockAIClient{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: `[{"content": "High latency is caused by GC pressure", "namespace": "incidents", "importance": 8.0}]`,
			}, nil
		},
	}

	hook, _ := NewKnowledgeExtractionHook(knowledge, embedder, aiClient, "test-agent", "infrastructure")
	t.Cleanup(hook.Close)
	holder := newPipelineHookExecutionHolder()
	ctx := withPipelineHookExecutionHolder(t.Context(), holder)
	invocation := beginPipelineHookInvocation(
		ctx, hook.Name(), PipelineHookPhaseAfterSynthesis, 1, 0, time.Now(),
	)

	// Call AfterSynthesis — extraction runs async
	hook.AfterSynthesis(invocation.Context(ctx), &core.PipelineContext{Request: "investigate latency"}, "Found high latency caused by GC pressure")
	invocation.Complete(PipelineHookSucceeded, nil)

	select {
	case fragment := <-storedFragments:
		assert.Equal(t, "High latency is caused by GC pressure", fragment.Content)
		assert.Equal(t, "incidents", fragment.Namespace)
		assert.Equal(t, 8.0, fragment.Importance)
		assert.Equal(t, core.ScopeSharedDomain, fragment.Scope)
		assert.Equal(t, "infrastructure", fragment.AgentDomain)
		assert.NotEmpty(t, fragment.Embedding)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stored fragment")
	}
	hook.Close()
	records := holder.Snapshot()
	if len(records) != 1 || len(records[0].Effects) != 1 {
		t.Fatalf("knowledge extraction effects = %#v", records)
	}
	effect := records[0].Effects[0]
	if effect.Status != core.PipelineHookEffectSucceeded {
		t.Fatalf("knowledge extraction effect = %#v", effect)
	}
	var data knowledgeExtractionEffectData
	if err := json.Unmarshal(effect.Data, &data); err != nil {
		t.Fatalf("decode knowledge extraction effect: %v", err)
	}
	if len(data.Fragments) != 1 ||
		data.Fragments[0].Content != "High latency is caused by GC pressure" ||
		data.Fragments[0].Namespace != "incidents" ||
		data.Fragments[0].Status != core.PipelineHookEffectSucceeded ||
		data.Fragments[0].ObservedOutcome != "provider_call_returned_without_error" {
		t.Fatalf("knowledge extraction effect data = %#v", data)
	}
}

func TestKnowledgeExtractionHook_SkipsEmptyResponse(t *testing.T) {
	knowledge := &core.MockSharedKnowledge{}
	embedder := &core.MockEmbeddingClient{}
	aiClient := &mockAIClient{}
	hook, _ := NewKnowledgeExtractionHook(knowledge, embedder, aiClient, "agent", "domain")

	resp, err := hook.AfterSynthesis(context.Background(), &core.PipelineContext{}, "")
	assert.NoError(t, err)
	assert.Equal(t, "", resp)
	assert.Equal(t, 0, knowledge.StoreCt)
}

func TestExtractJSONArray(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"clean array", `[{"a": 1}]`, `[{"a": 1}]`},
		{"with prose", `Here are the results: [{"a": 1}] Hope this helps!`, `[{"a": 1}]`},
		{"empty array", `[]`, `[]`},
		{"nested arrays", `[[1, 2], [3, 4]]`, `[[1, 2], [3, 4]]`},
		{"no array", `just text`, ""},
		{"unclosed bracket", `[1, 2, 3`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractJSONArray(tt.input))
		})
	}
}

func TestTruncateForExtraction(t *testing.T) {
	assert.Equal(t, "short", truncateForExtraction("short", 100))
	assert.Equal(t, "ab...[truncated]", truncateForExtraction("abcdef", 2))
}

// Reuses mockAIClient from hybrid_resolver_test.go (same package).

// Verify interface compliance at compile time.
var _ core.AfterSynthesisHook = (*KnowledgeExtractionHook)(nil)
