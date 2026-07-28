package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/baggage"
)

type conversationCompactorTestAIClient struct {
	response *core.AIResponse
	err      error
}

func (c *conversationCompactorTestAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.response, nil
}

func TestLLMConversationCompactor_RecordsDebugInteractionOnSuccess(t *testing.T) {
	client := &conversationCompactorTestAIClient{
		response: &core.AIResponse{
			Content:  "short summary",
			Model:    "test-model",
			Provider: "test-provider",
			Usage: core.TokenUsage{
				PromptTokens:     11,
				CompletionTokens: 7,
				TotalTokens:      18,
			},
		},
	}
	compactor, err := NewLLMConversationCompactor(client, &AIOptionsOverride{
		Model:     StringPtr("fallback-model"),
		MaxTokens: IntPtr(128),
	})
	if err != nil {
		t.Fatalf("NewLLMConversationCompactor() error = %v", err)
	}

	debugStore := &mockLLMDebugStore{}
	compactor.SetLLMDebugStore(debugStore)

	_, err = compactor.Compact(context.Background(), "prior summary", []core.ConversationTurn{
		{Role: "user", Content: "new turn"},
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}

	if len(debugStore.interactions) != 1 {
		t.Fatalf("expected 1 debug interaction, got %d", len(debugStore.interactions))
	}
	interaction := debugStore.interactions[0]
	if interaction.Type != "conversation_history_compaction" {
		t.Fatalf("expected interaction type conversation_history_compaction, got %q", interaction.Type)
	}
	if !interaction.Success {
		t.Fatal("expected successful debug interaction")
	}
	if interaction.Response != "short summary" {
		t.Fatalf("expected debug interaction response to be recorded, got %q", interaction.Response)
	}
}

func TestLLMConversationCompactor_PreservesBaggageMemberPropertiesForDebugStore(t *testing.T) {
	client := &conversationCompactorTestAIClient{
		response: &core.AIResponse{Content: "short summary"},
	}
	compactor, err := NewLLMConversationCompactor(client, nil)
	if err != nil {
		t.Fatalf("NewLLMConversationCompactor() error = %v", err)
	}
	debugStore := &mockLLMDebugStore{}
	compactor.SetLLMDebugStore(debugStore)

	ctx, err := telemetry.WithBaggageExact(
		context.Background(),
		MetadataConversationID,
		"conversation-debug",
		telemetry.WithMetricLabelEligibility(false),
	)
	if err != nil {
		t.Fatalf("WithBaggageExact() error = %v", err)
	}
	ctx = telemetry.WithBaggage(ctx, "request_id", "request-debug")

	if _, err := compactor.Compact(ctx, "", []core.ConversationTurn{
		{Role: "user", Content: "new turn"},
	}); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}

	debugStore.mu.Lock()
	if len(debugStore.contexts) != 1 {
		debugStore.mu.Unlock()
		t.Fatalf("captured context count = %d, want 1", len(debugStore.contexts))
	}
	recordCtx := debugStore.contexts[0]
	debugStore.mu.Unlock()

	member := baggage.FromContext(recordCtx).Member(MetadataConversationID)
	if member.Value() != "conversation-debug" {
		t.Fatalf("copied conversation ID = %q", member.Value())
	}
	if !hasMetricExclusionProperty(member) {
		t.Fatalf("copied baggage properties = %v, want metric exclusion", member.Properties())
	}
}

func TestLLMConversationCompactor_RecordsDebugInteractionOnFailure(t *testing.T) {
	client := &conversationCompactorTestAIClient{
		err: errors.New("provider unavailable"),
	}
	compactor, err := NewLLMConversationCompactor(client, &AIOptionsOverride{
		Model:     StringPtr("fallback-model"),
		MaxTokens: IntPtr(128),
	})
	if err != nil {
		t.Fatalf("NewLLMConversationCompactor() error = %v", err)
	}

	debugStore := &mockLLMDebugStore{}
	compactor.SetLLMDebugStore(debugStore)

	summary, err := compactor.Compact(context.Background(), "prior summary", []core.ConversationTurn{
		{Role: "user", Content: "new turn"},
	})
	if err != nil {
		t.Fatalf("Compact() should fail open, got error = %v", err)
	}
	if summary != "" {
		t.Fatalf("expected fail-open empty summary, got %q", summary)
	}
	if len(debugStore.interactions) != 1 {
		t.Fatalf("expected 1 debug interaction on failure, got %d", len(debugStore.interactions))
	}
	if debugStore.interactions[0].Success {
		t.Fatal("expected failed debug interaction to be marked unsuccessful")
	}
	if debugStore.interactions[0].Error == "" {
		t.Fatal("expected failed debug interaction to capture the error message")
	}
}

func TestLLMConversationCompactor_RecordsSpanAndWarnLogOnFailOpen(t *testing.T) {
	client := &conversationCompactorTestAIClient{
		err: errors.New("provider unavailable"),
	}
	compactor, err := NewLLMConversationCompactor(client, &AIOptionsOverride{
		Model:     StringPtr("fallback-model"),
		MaxTokens: IntPtr(128),
	})
	if err != nil {
		t.Fatalf("NewLLMConversationCompactor() error = %v", err)
	}

	tel := &mockTelemetry{}
	logger := &conversationHistoryTestLogger{}
	compactor.SetTelemetry(tel)
	compactor.SetLogger(logger)

	ctx := telemetry.WithBaggage(context.Background(),
		"request_id", "req-compact-1",
		"original_request_id", "req-root-compact-1",
	)
	summary, err := compactor.Compact(ctx, "prior summary", []core.ConversationTurn{
		{Role: "user", Content: "new turn"},
	})
	if err != nil {
		t.Fatalf("Compact() should fail open, got error = %v", err)
	}
	if summary != "" {
		t.Fatalf("expected fail-open empty summary, got %q", summary)
	}
	if len(tel.spans) == 0 || tel.spans[0] != "conversation_history.compact" {
		t.Fatalf("expected conversation_history.compact span, got %v", tel.spans)
	}
	warnFields := logger.WarnFields()
	if len(warnFields) == 0 {
		t.Fatal("expected warn log fields to be captured")
	}
	fields := warnFields[len(warnFields)-1]
	if got := fields["request_id"]; got != "req-compact-1" {
		t.Fatalf("request_id = %v, want req-compact-1", got)
	}
	if got := fields["error_type"]; got != "compaction" {
		t.Fatalf("error_type = %v, want compaction", got)
	}
	if _, ok := fields["duration_ms"]; !ok {
		t.Fatal("expected duration_ms in compaction warn log")
	}
}

func TestBuildConversationCompactionPrompt_PrefersDurableStateOverProceduralNarration(t *testing.T) {
	prompt := buildConversationCompactionPrompt("prior summary", []core.ConversationTurn{
		{Role: "user", Content: "please improve this draft"},
	})

	wantSubstrings := []string{
		"Avoid procedural or stale workflow narration",
		"the next step is",
		"the assistant will",
		"Summarize only durable conversational state",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}
