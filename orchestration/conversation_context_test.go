package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type conversationMetricRecord struct {
	name   string
	value  float64
	labels map[string]string
}

type conversationCaptureTelemetry struct {
	records []conversationMetricRecord
	spans   []*mockSpan
}

func (c *conversationCaptureTelemetry) StartSpan(
	ctx context.Context,
	name string,
) (context.Context, core.Span) {
	span := &mockSpan{name: name}
	c.spans = append(c.spans, span)
	return ctx, span
}

func (c *conversationCaptureTelemetry) RecordMetric(
	name string,
	value float64,
	labels map[string]string,
) {
	labelsCopy := make(map[string]string, len(labels))
	for key, labelValue := range labels {
		labelsCopy[key] = labelValue
	}
	c.records = append(c.records, conversationMetricRecord{
		name:   name,
		value:  value,
		labels: labelsCopy,
	})
}

type conversationShortCircuitHook struct {
	contextMetadata map[string]interface{}
	coreID          string
	baggageID       string
}

func (h *conversationShortCircuitHook) Name() string {
	return "conversation-ingress-capture"
}

func (h *conversationShortCircuitHook) BeforePlanning(
	ctx context.Context,
	_ *core.PipelineContext,
) (*core.PipelineShortCircuit, error) {
	h.contextMetadata = cloneMetadata(GetMetadata(ctx))
	h.coreID = core.GetConversationID(ctx)
	h.baggageID = telemetry.GetBaggage(ctx)[MetadataConversationID]
	return &core.PipelineShortCircuit{
		Response: "short-circuit response",
		Source:   "unit-test",
	}, nil
}

func TestProcessRequestEntryPoints_PromoteConversationID(t *testing.T) {
	const conversationID = "conversation-entry-point"

	tests := []struct {
		name        string
		streaming   bool
		requestSpan string
	}{
		{
			name:        "non-streaming",
			requestSpan: "orchestrator.process_request",
		},
		{
			name:        "streaming",
			streaming:   true,
			requestSpan: "orchestrator.process_request_streaming",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var aiClient core.AIClient = NewMockAIClient()
			if test.streaming {
				aiClient = NewStreamingMockAIClient()
			}

			orchestrator := NewAIOrchestrator(
				DefaultConfig(),
				NewMockDiscovery(),
				aiClient,
			)
			capture := &conversationCaptureTelemetry{}
			orchestrator.SetTelemetry(capture)
			hook := &conversationShortCircuitHook{}
			orchestrator.pipelineHooks = []core.PipelineHook{hook}

			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(
				sdktrace.WithSpanProcessor(recorder),
			)
			t.Cleanup(func() {
				_ = provider.Shutdown(context.Background())
			})
			ctx, parentSpan := provider.Tracer("conversation-entry-test").Start(
				context.Background(),
				"parent-request",
			)

			var (
				responseMetadata map[string]interface{}
				err              error
			)
			if test.streaming {
				var response *StreamingOrchestratorResponse
				response, err = orchestrator.ProcessRequestStreaming(
					ctx,
					"request",
					map[string]interface{}{
						MetadataConversationID: conversationID,
					},
					func(core.StreamChunk) error { return nil },
				)
				if response != nil {
					responseMetadata = response.Metadata
				}
			} else {
				var response *OrchestratorResponse
				response, err = orchestrator.ProcessRequest(
					ctx,
					"request",
					map[string]interface{}{
						MetadataConversationID: conversationID,
					},
				)
				if response != nil {
					responseMetadata = response.Metadata
				}
			}
			parentSpan.End()

			if err != nil {
				t.Fatalf("request entry point error = %v", err)
			}
			if responseMetadata[MetadataConversationID] != conversationID {
				t.Fatalf("response metadata = %v", responseMetadata)
			}
			if hook.coreID != conversationID ||
				hook.baggageID != conversationID ||
				hook.contextMetadata[MetadataConversationID] != conversationID {
				t.Fatalf(
					"hook correlation: core=%q baggage=%q metadata=%v",
					hook.coreID,
					hook.baggageID,
					hook.contextMetadata,
				)
			}

			requestSpan := findConversationMockSpan(capture.spans, test.requestSpan)
			if requestSpan == nil {
				t.Fatalf("missing request span %q", test.requestSpan)
			}
			if got := requestSpan.attributes[MetadataConversationID]; got != conversationID {
				t.Fatalf("request span conversation ID = %v", got)
			}

			ended := recorder.Ended()
			if len(ended) != 1 {
				t.Fatalf("ended OTel spans = %d, want 1", len(ended))
			}
			if got, ok := readOnlySpanStringAttribute(
				ended[0],
				MetadataConversationID,
			); !ok || got != conversationID {
				t.Fatalf("parent span conversation ID = %q, %v", got, ok)
			}
		})
	}
}

func TestResolveConversationContext_PrecedenceAndPromotion(t *testing.T) {
	ctx := telemetry.WithBaggage(context.Background(), MetadataConversationID, "from-baggage")
	ctx = core.WithConversationID(ctx, "from-core")
	input := map[string]interface{}{
		MetadataConversationID: "from-metadata",
		"application_key":      "preserved",
	}
	tel := &conversationCaptureTelemetry{}
	orchestrator := &AIOrchestrator{telemetry: tel}

	gotCtx, gotID, gotMetadata := orchestrator.resolveConversationContext(ctx, input)

	if gotID != "from-metadata" {
		t.Fatalf("conversation ID = %q, want from-metadata", gotID)
	}
	if got := core.GetConversationID(gotCtx); got != gotID {
		t.Fatalf("core conversation ID = %q, want %q", got, gotID)
	}
	if got := telemetry.GetBaggage(gotCtx)[MetadataConversationID]; got != gotID {
		t.Fatalf("baggage conversation ID = %q, want %q", got, gotID)
	}
	if got := gotMetadata[MetadataConversationID]; got != gotID {
		t.Fatalf("metadata conversation ID = %v, want %q", got, gotID)
	}
	if gotMetadata["application_key"] != "preserved" {
		t.Fatalf("unrelated metadata was not preserved: %v", gotMetadata)
	}
	if len(tel.records) != 0 {
		t.Fatalf("valid resolution emitted rejection metrics: %+v", tel.records)
	}
	gotMetadata["application_key"] = "working-copy-only"
	if input["application_key"] != "preserved" {
		t.Fatal("valid resolution mutated caller-owned metadata")
	}

	member := baggage.FromContext(gotCtx).Member(MetadataConversationID)
	if member.Value() != gotID {
		t.Fatalf("promoted baggage member = %q, want %q", member.Value(), gotID)
	}
	if !hasMetricExclusionProperty(member) {
		t.Fatalf("promoted baggage properties = %v, want metric exclusion", member.Properties())
	}
}

func TestResolveConversationContext_ValidCoreAndBaggageSources(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{
			name: "programmatic core",
			ctx:  core.WithConversationID(context.Background(), "from-core"),
			want: "from-core",
		},
		{
			name: "incoming baggage",
			ctx: telemetry.WithBaggage(
				context.Background(),
				MetadataConversationID,
				"from-baggage",
			),
			want: "from-baggage",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tel := &conversationCaptureTelemetry{}
			orchestrator := &AIOrchestrator{telemetry: tel}

			gotCtx, gotID, gotMetadata := orchestrator.resolveConversationContext(test.ctx, nil)

			if gotID != test.want ||
				core.GetConversationID(gotCtx) != test.want ||
				telemetry.GetBaggage(gotCtx)[MetadataConversationID] != test.want ||
				gotMetadata[MetadataConversationID] != test.want {
				t.Fatalf(
					"promotion mismatch: id=%q core=%q baggage=%q metadata=%v",
					gotID,
					core.GetConversationID(gotCtx),
					telemetry.GetBaggage(gotCtx)[MetadataConversationID],
					gotMetadata,
				)
			}
			if len(tel.records) != 0 {
				t.Fatalf("valid source emitted rejection: %+v", tel.records)
			}
			if !hasMetricExclusionProperty(
				baggage.FromContext(gotCtx).Member(MetadataConversationID),
			) {
				t.Fatal("source was not re-promoted with metric exclusion")
			}
		})
	}
}

func TestResolveConversationContext_NoConversationPreservesUnrelatedContext(t *testing.T) {
	ctx := telemetry.WithBaggage(context.Background(), "tenant", "tenant-1")
	metadata := map[string]interface{}{"application_key": "preserved"}
	tel := &conversationCaptureTelemetry{}
	orchestrator := &AIOrchestrator{telemetry: tel}

	gotCtx, gotID, gotMetadata := orchestrator.resolveConversationContext(ctx, metadata)

	if gotID != "" || core.GetConversationID(gotCtx) != "" {
		t.Fatalf("unexpected conversation ID = %q", gotID)
	}
	if telemetry.GetBaggage(gotCtx)["tenant"] != "tenant-1" {
		t.Fatalf("unrelated baggage was lost: %v", telemetry.GetBaggage(gotCtx))
	}
	if gotMetadata["application_key"] != "preserved" {
		t.Fatalf("unrelated metadata was lost: %v", gotMetadata)
	}
	if len(tel.records) != 0 {
		t.Fatalf("absent conversation emitted rejection: %+v", tel.records)
	}
}

func TestResolveConversationContext_InvalidMetadataBlocksAndScrubsFallback(t *testing.T) {
	rawInvalid := "invalid conversation"
	ctx := telemetry.WithBaggage(context.Background(), MetadataConversationID, "from-baggage")
	ctx = core.WithConversationID(ctx, "from-core")
	input := map[string]interface{}{
		MetadataConversationID: rawInvalid,
		"application_key":      "preserved",
	}
	tel := &conversationCaptureTelemetry{}
	orchestrator := &AIOrchestrator{telemetry: tel}

	gotCtx, gotID, gotMetadata := orchestrator.resolveConversationContext(ctx, input)

	if gotID != "" || core.GetConversationID(gotCtx) != "" {
		t.Fatalf("rejected conversation leaked into core context: %q, %q", gotID, core.GetConversationID(gotCtx))
	}
	if _, present := telemetry.GetBaggage(gotCtx)[MetadataConversationID]; present {
		t.Fatal("lower-precedence baggage survived invalid metadata")
	}
	if _, present := gotMetadata[MetadataConversationID]; present {
		t.Fatal("rejected reserved metadata survived sanitization")
	}
	if gotMetadata["application_key"] != "preserved" {
		t.Fatalf("unrelated metadata was not preserved: %v", gotMetadata)
	}
	if input[MetadataConversationID] != rawInvalid {
		t.Fatal("caller-owned metadata was mutated")
	}
	assertConversationRejection(
		t,
		tel.records,
		conversationIDSourceMetadata,
		string(core.ConversationIDValidationInvalidCharacter),
		rawInvalid,
	)
}

func TestResolveConversationContext_InvalidIncomingBaggageIsRejected(t *testing.T) {
	carrier := propagation.MapCarrier{
		"baggage": MetadataConversationID + "=invalid%20conversation",
	}
	ctx := propagation.Baggage{}.Extract(context.Background(), carrier)
	if got := telemetry.GetBaggage(ctx)[MetadataConversationID]; got != "invalid conversation" {
		t.Fatalf("decoded test baggage = %q", got)
	}
	tel := &conversationCaptureTelemetry{}
	orchestrator := &AIOrchestrator{telemetry: tel}

	gotCtx, gotID, gotMetadata := orchestrator.resolveConversationContext(ctx, nil)

	if gotID != "" || core.GetConversationID(gotCtx) != "" {
		t.Fatalf("invalid baggage leaked conversation identity: %q", gotID)
	}
	if _, present := telemetry.GetBaggage(gotCtx)[MetadataConversationID]; present {
		t.Fatal("invalid incoming baggage survived sanitization")
	}
	if _, present := gotMetadata[MetadataConversationID]; present {
		t.Fatal("invalid incoming baggage appeared in metadata")
	}
	assertConversationRejection(
		t,
		tel.records,
		conversationIDSourceBaggage,
		string(core.ConversationIDValidationInvalidCharacter),
		"invalid conversation",
	)
}

func TestResolveConversationContext_RejectionDoesNotMarkSpanError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	ctx, span := provider.Tracer("conversation-context-test").Start(
		context.Background(),
		"request",
	)
	tel := &conversationCaptureTelemetry{}
	orchestrator := &AIOrchestrator{telemetry: tel}

	gotCtx, gotID, gotMetadata := orchestrator.resolveConversationContext(
		ctx,
		map[string]interface{}{
			MetadataConversationID: "invalid conversation",
			"application_key":      "preserved",
		},
	)
	span.End()

	if gotID != "" || core.GetConversationID(gotCtx) != "" {
		t.Fatalf("rejected conversation leaked into context: %q", gotID)
	}
	if _, present := gotMetadata[MetadataConversationID]; present {
		t.Fatal("rejected reserved metadata survived sanitization")
	}
	if gotMetadata["application_key"] != "preserved" {
		t.Fatalf("unrelated metadata was not preserved: %v", gotMetadata)
	}
	assertConversationRejection(
		t,
		tel.records,
		conversationIDSourceMetadata,
		string(core.ConversationIDValidationInvalidCharacter),
		"invalid conversation",
	)

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	if status := ended[0].Status(); status.Code != codes.Unset {
		t.Fatalf("span status = %v (%q), want unset", status.Code, status.Description)
	}
	for _, event := range ended[0].Events() {
		if event.Name == "exception" {
			t.Fatalf("conversation rejection recorded an exception event: %+v", event)
		}
	}
}

func TestResolveConversationContext_InvalidTypeBlocksFallback(t *testing.T) {
	ctx := core.WithConversationID(context.Background(), "from-core")
	tel := &conversationCaptureTelemetry{}
	orchestrator := &AIOrchestrator{telemetry: tel}

	gotCtx, gotID, gotMetadata := orchestrator.resolveConversationContext(
		ctx,
		map[string]interface{}{MetadataConversationID: 42},
	)

	if gotID != "" || core.GetConversationID(gotCtx) != "" {
		t.Fatalf("invalid type leaked conversation identity: %q", gotID)
	}
	if _, present := gotMetadata[MetadataConversationID]; present {
		t.Fatal("invalid typed reserved metadata survived sanitization")
	}
	assertConversationRejection(
		t,
		tel.records,
		conversationIDSourceMetadata,
		string(core.ConversationIDValidationInvalidType),
		"42",
	)
}

func TestResolveConversationContext_InvalidCoreBlocksBaggageFallback(t *testing.T) {
	ctx := telemetry.WithBaggage(context.Background(), MetadataConversationID, "from-baggage")
	ctx = core.WithConversationID(ctx, "invalid conversation")
	tel := &conversationCaptureTelemetry{}
	orchestrator := &AIOrchestrator{telemetry: tel}

	gotCtx, gotID, _ := orchestrator.resolveConversationContext(ctx, nil)

	if gotID != "" || core.GetConversationID(gotCtx) != "" {
		t.Fatalf("invalid core candidate leaked conversation identity: %q", gotID)
	}
	if _, present := telemetry.GetBaggage(gotCtx)[MetadataConversationID]; present {
		t.Fatal("baggage fallback survived invalid core candidate")
	}
	assertConversationRejection(
		t,
		tel.records,
		string(core.ConversationIDSourceCoreContext),
		string(core.ConversationIDValidationInvalidCharacter),
		"invalid conversation",
	)
}

func TestResolveConversationContext_BaggageCapacityFailureIsFailOpenAndScrubbed(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < telemetry.MaxBaggageItems; i++ {
		var err error
		ctx, err = telemetry.WithBaggageExact(ctx, fmt.Sprintf("key%d", i), "value")
		if err != nil {
			t.Fatalf("fill baggage member %d: %v", i, err)
		}
	}
	ctx = core.WithConversationID(ctx, "conversation-capacity")
	tel := &conversationCaptureTelemetry{}
	orchestrator := &AIOrchestrator{telemetry: tel}

	gotCtx, gotID, gotMetadata := orchestrator.resolveConversationContext(ctx, nil)

	if gotID != "" || core.GetConversationID(gotCtx) != "" {
		t.Fatalf("capacity-rejected conversation leaked: %q", gotID)
	}
	if _, present := telemetry.GetBaggage(gotCtx)[MetadataConversationID]; present {
		t.Fatal("capacity-rejected conversation appeared in baggage")
	}
	if _, present := gotMetadata[MetadataConversationID]; present {
		t.Fatal("capacity-rejected conversation appeared in metadata")
	}
	assertConversationRejection(
		t,
		tel.records,
		conversationIDSourceBaggageExact,
		"item_limit",
		"conversation-capacity",
	)
}

func TestResolveConversationContext_ReusedContextReplacesOldIdentity(t *testing.T) {
	orchestrator := &AIOrchestrator{telemetry: &conversationCaptureTelemetry{}}
	firstCtx, firstID, _ := orchestrator.resolveConversationContext(
		context.Background(),
		map[string]interface{}{MetadataConversationID: "conversation-one"},
	)
	if firstID != "conversation-one" {
		t.Fatalf("first ID = %q", firstID)
	}

	secondCtx, secondID, secondMetadata := orchestrator.resolveConversationContext(
		firstCtx,
		map[string]interface{}{MetadataConversationID: "conversation-two"},
	)

	if secondID != "conversation-two" {
		t.Fatalf("second ID = %q", secondID)
	}
	if got := core.GetConversationID(secondCtx); got != secondID {
		t.Fatalf("core ID = %q, want %q", got, secondID)
	}
	if got := telemetry.GetBaggage(secondCtx)[MetadataConversationID]; got != secondID {
		t.Fatalf("baggage ID = %q, want %q", got, secondID)
	}
	if got := secondMetadata[MetadataConversationID]; got != secondID {
		t.Fatalf("metadata ID = %v, want %q", got, secondID)
	}
}

func TestCheckpointMetadataMergeClonesTopLevelMaps(t *testing.T) {
	nested := map[string]string{"shared": "shallow"}
	existing := map[string]interface{}{
		"resume_key":           "resume",
		MetadataConversationID: "stale",
	}
	request := map[string]interface{}{
		"request_key": "request",
		"nested":      nested,
	}
	ctx := WithMetadata(context.Background(), existing)

	gotCtx := withCheckpointMetadata(ctx, request, "conversation-current")
	got := GetMetadata(gotCtx)

	if got["resume_key"] != "resume" || got["request_key"] != "request" {
		t.Fatalf("metadata merge lost values: %v", got)
	}
	if got[MetadataConversationID] != "conversation-current" {
		t.Fatalf("canonical conversation ID = %v", got[MetadataConversationID])
	}

	got["framework_added"] = "checkpoint-only"
	request["request_key"] = "caller-mutated"
	existing["resume_key"] = "caller-mutated"
	if _, present := request["framework_added"]; present {
		t.Fatal("checkpoint mutation appeared in request map")
	}
	if _, present := existing["framework_added"]; present {
		t.Fatal("checkpoint mutation appeared in existing context map")
	}
	if got["request_key"] != "request" || got["resume_key"] != "resume" {
		t.Fatalf("caller mutation changed checkpoint metadata: %v", got)
	}
	gotNested, ok := got["nested"].(map[string]string)
	if !ok {
		t.Fatalf("nested metadata type = %T", got["nested"])
	}
	gotNested["shared"] = "mutated-through-clone"
	if nested["shared"] != "mutated-through-clone" {
		t.Fatal("metadata clone unexpectedly deep-copied nested application data")
	}
}

func TestCheckpointMetadataMergePreservesExistingOnNilRequest(t *testing.T) {
	existing := map[string]interface{}{"session_id": "session-1"}
	ctx := WithMetadata(context.Background(), existing)

	got := GetMetadata(withCheckpointMetadata(ctx, nil, ""))

	if got["session_id"] != "session-1" {
		t.Fatalf("existing resume metadata was lost: %v", got)
	}
	got["checkpoint_only"] = true
	if _, present := existing["checkpoint_only"]; present {
		t.Fatal("existing caller-owned metadata map was not cloned")
	}
}

func TestCreateCheckpoint_DoesNotMutateCallerMetadataMap(t *testing.T) {
	callerMetadata := map[string]interface{}{"session_id": "session-1"}
	ctx := withCheckpointMetadata(
		context.Background(),
		callerMetadata,
		"conversation-checkpoint",
	)
	traceID, err := trace.TraceIDFromHex("11111111111111111111111111111111")
	if err != nil {
		t.Fatalf("TraceIDFromHex() error = %v", err)
	}
	spanID, err := trace.SpanIDFromHex("2222222222222222")
	if err != nil {
		t.Fatalf("SpanIDFromHex() error = %v", err)
	}
	ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))
	ctx = WithRequestMode(ctx, RequestModeNonStreaming)

	controller := &DefaultInterruptController{logger: &core.NoOpLogger{}}
	checkpoint := controller.createCheckpoint(
		ctx,
		&RoutingPlan{OriginalRequest: "request"},
		nil,
		nil,
		&InterruptDecision{},
		InterruptPointPlanGenerated,
	)

	if checkpoint.UserContext[MetadataConversationID] != "conversation-checkpoint" {
		t.Fatalf("checkpoint conversation ID = %v", checkpoint.UserContext[MetadataConversationID])
	}
	if checkpoint.UserContext["original_trace_id"] != traceID.String() {
		t.Fatalf("checkpoint trace metadata = %v", checkpoint.UserContext)
	}
	if _, present := callerMetadata[MetadataConversationID]; present {
		t.Fatal("framework conversation ID appeared in caller-owned metadata")
	}
	if _, present := callerMetadata["original_trace_id"]; present {
		t.Fatal("framework trace ID appeared in caller-owned metadata")
	}

	checkpoint.UserContext["checkpoint_only"] = true
	callerMetadata["caller_only"] = true
	if _, present := callerMetadata["checkpoint_only"]; present {
		t.Fatal("checkpoint mutation appeared in caller-owned metadata")
	}
	if _, present := checkpoint.UserContext["caller_only"]; present {
		t.Fatal("caller mutation appeared in checkpoint metadata")
	}
}

func TestStoreExecutionAsync_CapturesTraceAndConversationFromCanonicalContexts(t *testing.T) {
	orchestrator, store := storeExecutionAsyncFixture(t)
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("TraceIDFromHex() error = %v", err)
	}
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	if err != nil {
		t.Fatalf("SpanIDFromHex() error = %v", err)
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(
		trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, Remote: true},
	))
	ctx = telemetry.WithBaggage(ctx,
		"trace_id", "baggage-trace-must-not-win",
		"original_request_id", "original-request",
	)
	ctx = core.WithConversationID(ctx, "conversation-store")

	orchestrator.storeExecutionAsync(ctx, "request", "request-current", nil, nil, nil)
	orchestrator.executionWg.Wait()

	stored := lastStored(store)
	if stored == nil {
		t.Fatal("execution was not stored")
	}
	if stored.TraceID != traceID.String() {
		t.Fatalf("stored trace ID = %q, want span-context trace %q", stored.TraceID, traceID)
	}
	if stored.OriginalRequestID != "original-request" {
		t.Fatalf("original request ID = %q", stored.OriginalRequestID)
	}
	if got := ExecutionConversationID(stored); got != "conversation-store" {
		t.Fatalf("stored conversation ID = %q", got)
	}
}

type failingConversationExecutionStore struct {
	err error
}

func (s *failingConversationExecutionStore) Store(context.Context, *StoredExecution) error {
	return s.err
}
func (s *failingConversationExecutionStore) Get(context.Context, string) (*StoredExecution, error) {
	return nil, errors.New("not implemented")
}
func (s *failingConversationExecutionStore) GetByTraceID(context.Context, string) (*StoredExecution, error) {
	return nil, errors.New("not implemented")
}
func (s *failingConversationExecutionStore) SetMetadata(context.Context, string, string, string) error {
	return errors.New("not implemented")
}
func (s *failingConversationExecutionStore) ExtendTTL(context.Context, string, time.Duration) error {
	return errors.New("not implemented")
}
func (s *failingConversationExecutionStore) ListRecent(context.Context, int) ([]ExecutionSummary, error) {
	return nil, errors.New("not implemented")
}

func TestStoreExecutionAsync_FailureLogIsSanitizedAndCorrelated(t *testing.T) {
	const sensitiveError = "redis://user:secret@redis.internal:6379/8?conversation_id=raw-secret"
	traceID, err := trace.TraceIDFromHex("abcdef0123456789abcdef0123456789")
	if err != nil {
		t.Fatalf("TraceIDFromHex() error = %v", err)
	}
	spanID, err := trace.SpanIDFromHex("abcdef0123456789")
	if err != nil {
		t.Fatalf("SpanIDFromHex() error = %v", err)
	}
	var warningFields map[string]interface{}
	logger := &mockLogger{
		warnFunc: func(_ string, fields map[string]interface{}) {
			warningFields = fields
		},
	}
	orchestrator := &AIOrchestrator{
		config: DefaultConfig(),
		executionStore: &failingConversationExecutionStore{
			err: errors.New(sensitiveError),
		},
		logger: logger,
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(
		trace.SpanContextConfig{TraceID: traceID, SpanID: spanID},
	))
	ctx = core.WithConversationID(ctx, "conversation-log")

	orchestrator.storeExecutionAsync(ctx, "request", "request-log", nil, nil, nil)
	orchestrator.executionWg.Wait()

	if warningFields == nil {
		t.Fatal("expected execution-store warning")
	}
	if warningFields["operation"] != "execution_store" ||
		warningFields["request_id"] != "request-log" ||
		warningFields["error_type"] != "store_write" ||
		warningFields["trace_id"] != traceID.String() ||
		warningFields[MetadataConversationID] != "conversation-log" {
		t.Fatalf("warning fields = %v", warningFields)
	}
	safeMessage, _ := warningFields["error"].(string)
	if safeMessage == "" || strings.Contains(safeMessage, "redis://") ||
		strings.Contains(safeMessage, "secret") ||
		strings.Contains(safeMessage, "raw-secret") {
		t.Fatalf("unsafe execution-store error = %q", safeMessage)
	}
}

func findConversationMockSpan(spans []*mockSpan, name string) *mockSpan {
	for _, span := range spans {
		if span.name == name {
			return span
		}
	}
	return nil
}

func readOnlySpanStringAttribute(
	span sdktrace.ReadOnlySpan,
	key string,
) (string, bool) {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value.AsString(), true
		}
	}
	return "", false
}

func TestExecutionConversationAccessors(t *testing.T) {
	if got := ExecutionConversationID(nil); got != "" {
		t.Fatalf("nil execution conversation ID = %q", got)
	}
	execution := &StoredExecution{
		Metadata: map[string]string{MetadataConversationID: "conversation-record"},
	}
	if got := ExecutionConversationID(execution); got != "conversation-record" {
		t.Fatalf("execution accessor = %q", got)
	}
	summary := ExecutionSummary{
		Metadata: map[string]string{MetadataConversationID: "conversation-summary"},
	}
	if got := ExecutionSummaryConversationID(summary); got != "conversation-summary" {
		t.Fatalf("summary accessor = %q", got)
	}
}

func TestConversationIDBaggageRejectionReasonIsBounded(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "invalid UTF-8",
			err:  &telemetry.BaggageExactError{Reason: telemetry.BaggageExactInvalidUTF8},
			want: string(core.ConversationIDValidationInvalidUTF8),
		},
		{
			name: "key too long",
			err:  &telemetry.BaggageExactError{Reason: telemetry.BaggageExactKeyTooLong},
			want: string(core.ConversationIDValidationTooLong),
		},
		{
			name: "value too long",
			err:  &telemetry.BaggageExactError{Reason: telemetry.BaggageExactValueTooLong},
			want: string(core.ConversationIDValidationTooLong),
		},
		{
			name: "item limit",
			err:  &telemetry.BaggageExactError{Reason: telemetry.BaggageExactItemLimit},
			want: "item_limit",
		},
		{
			name: "total size",
			err:  &telemetry.BaggageExactError{Reason: telemetry.BaggageExactTotalSize},
			want: "total_size",
		},
		{
			name: "invalid key",
			err:  &telemetry.BaggageExactError{Reason: telemetry.BaggageExactInvalidKey},
			want: "invalid_baggage_key",
		},
		{
			name: "unknown error",
			err:  errors.New("raw backend details"),
			want: "invalid_baggage_key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := conversationIDBaggageRejectionReason(test.err); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConversationIDDiagnosticHelpersAreNilSafeAndBounded(t *testing.T) {
	if got := boundedCoreConversationSource(core.ConversationIDSourceCoreHeader); got != "core_header" {
		t.Fatalf("header source = %q", got)
	}
	if got := boundedCoreConversationSource("unexpected"); got != "core_context" {
		t.Fatalf("unexpected source = %q", got)
	}
	var orchestrator *AIOrchestrator
	orchestrator.emitConversationIDRejection("metadata", "empty")
	(&AIOrchestrator{}).emitConversationIDRejection("metadata", "empty")
}

func TestSafeExecutionStoreError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{
			name: "deadline",
			err:  fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
			want: "execution store write timed out",
		},
		{
			name: "canceled",
			err:  fmt.Errorf("wrapped: %w", context.Canceled),
			want: "execution store write canceled",
		},
		{
			name: "backend",
			err:  errors.New("redis://user:secret@host/8?raw=value"),
			want: "execution store backend write failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeExecutionStoreError(test.err); got != test.want {
				t.Fatalf("safe error = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRedisExecutionStore_SetMetadataRejectsReservedConversationIDBeforeIO(t *testing.T) {
	store := &RedisExecutionDebugStore{}
	err := store.SetMetadata(
		context.Background(),
		"request-1",
		MetadataConversationID,
		"conversation-replacement",
	)
	if err == nil {
		t.Fatal("expected reserved metadata validation error")
	}
}

func hasMetricExclusionProperty(member baggage.Member) bool {
	for _, property := range member.Properties() {
		value, ok := property.Value()
		if property.Key() == "truvag3_metric_label" && ok && value == "false" {
			return true
		}
	}
	return false
}

func assertConversationRejection(
	t *testing.T,
	records []conversationMetricRecord,
	source string,
	reason string,
	rejectedValue string,
) {
	t.Helper()
	if len(records) != 1 {
		t.Fatalf("rejection records = %+v, want exactly one", records)
	}
	record := records[0]
	if record.name != conversationIDRejectionMetric || record.value != 1 {
		t.Fatalf("rejection metric = %+v", record)
	}
	wantLabels := map[string]string{
		"source": source,
		"reason": reason,
		"module": telemetry.ModuleOrchestration,
	}
	if len(record.labels) != len(wantLabels) {
		t.Fatalf("rejection labels = %v, want %v", record.labels, wantLabels)
	}
	for key, want := range wantLabels {
		if got := record.labels[key]; got != want {
			t.Fatalf("rejection label %s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(fmt.Sprint(record), rejectedValue) {
		t.Fatalf("rejection metric exposed raw value %q: %+v", rejectedValue, record)
	}
}
