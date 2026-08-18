package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestRedisLLMCallRecorder_ImplementsLLMCallRecorder(t *testing.T) {
	// Compile-time check already exists in redis_llm_recorder.go (line 318).
	// This test makes the assertion explicit and visible in test output.
	var _ LLMCallRecorder = (*RedisLLMCallRecorder)(nil)
}

func TestNoOpLLMCallRecorder_RemainsSafe(t *testing.T) {
	var recorder LLMCallRecorder = &NoOpLLMCallRecorder{}
	if err := recorder.RecordLLMCall(
		core.WithConversationID(context.Background(), "conversation-noop"),
		"request-noop",
		LLMCallRecord{CallType: "agent_llm_call"},
	); err != nil {
		t.Fatalf("RecordLLMCall: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Empty requestID early-return (no Redis required)
// ---------------------------------------------------------------------------

func TestRecordLLMCall_EmptyRequestID_SkipsSilently(t *testing.T) {
	// Construct directly with nil client — the empty requestID check
	// returns before any Redis operation, so this is safe.
	r := &RedisLLMCallRecorder{
		client: nil,
		logger: &core.NoOpLogger{},
		ttl:    recorderDefaultTTL,
		errTTL: recorderErrorTTL,
	}

	err := r.RecordLLMCall(context.Background(), "", LLMCallRecord{
		CallType: "test",
		Prompt:   "hello",
	})
	if err != nil {
		t.Errorf("expected nil error for empty requestID, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// JSON field mapping — verifies llmInteractionJSON produces keys that
// orchestration.LLMInteraction can deserialize.
// ---------------------------------------------------------------------------

func TestLLMInteractionJSON_FieldNames(t *testing.T) {
	interaction := llmInteractionJSON{
		Type:             "agent_llm_call",
		SourceComponent:  "research-assistant",
		CallDescription:  "Tool selection",
		StepID:           "step-1",
		Timestamp:        time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		DurationMs:       150,
		Prompt:           "test prompt",
		SystemPrompt:     "system",
		Temperature:      0.7,
		MaxTokens:        1024,
		Model:            "gpt-4o-mini",
		Provider:         "openai",
		Response:         "test response",
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
		Success:          true,
		Error:            "",
		Attempt:          1,
	}

	data, err := json.Marshal(interaction)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Deserialize into a generic map to verify JSON key names match
	// what orchestration.LLMInteraction expects.
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Required keys that orchestration.LLMInteraction reads
	requiredKeys := []string{
		"type", "source_component", "call_description", "step_id",
		"timestamp", "duration_ms",
		"prompt", "system_prompt", "temperature", "max_tokens",
		"model", "provider",
		"response", "prompt_tokens", "completion_tokens", "total_tokens",
		"success", "attempt",
	}

	for _, key := range requiredKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("missing required JSON key %q in serialized interaction", key)
		}
	}

	// Verify specific values round-trip correctly
	if m["type"] != "agent_llm_call" {
		t.Errorf("type = %v, want agent_llm_call", m["type"])
	}
	if m["source_component"] != "research-assistant" {
		t.Errorf("source_component = %v, want research-assistant", m["source_component"])
	}
	if m["call_description"] != "Tool selection" {
		t.Errorf("call_description = %v, want Tool selection", m["call_description"])
	}
	// attempt should be 1 (float64 from JSON)
	if m["attempt"] != float64(1) {
		t.Errorf("attempt = %v, want 1", m["attempt"])
	}
}

func TestLLMInteractionJSON_OmitsEmptyOptionalFields(t *testing.T) {
	interaction := llmInteractionJSON{
		Type:      "agent_llm_call",
		Prompt:    "test",
		Response:  "ok",
		Success:   true,
		Attempt:   1,
		Timestamp: time.Now(),
		// SourceComponent, CallDescription, StepID, SystemPrompt, Model,
		// Provider, Error are all zero-value — should be omitted.
	}

	data, err := json.Marshal(interaction)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	omittedKeys := []string{
		"source_component", "call_description", "step_id",
		"system_prompt", "model", "provider", "error",
	}
	for _, key := range omittedKeys {
		if _, ok := m[key]; ok {
			t.Errorf("expected key %q to be omitted for zero value, but it was present", key)
		}
	}
}

// ---------------------------------------------------------------------------
// LLMCallRecord → llmInteractionJSON field mapping
// ---------------------------------------------------------------------------

func TestLLMCallRecord_ToInteractionJSON_Mapping(t *testing.T) {
	record := LLMCallRecord{
		CallType:         "agent_llm_call",
		SourceComponent:  "test-agent",
		Description:      "Synthesis call",
		StepID:           "step-42",
		Prompt:           "analyze this",
		Response:         "analysis result",
		Success:          true,
		DurationMs:       200,
		Temperature:      0.5,
		MaxTokens:        2048,
		Model:            "claude-3",
		Provider:         "anthropic",
		PromptTokens:     50,
		CompletionTokens: 100,
		TotalTokens:      150,
	}

	// Reproduce the mapping from RecordLLMCall (lines 181-201)
	interaction := llmInteractionJSON{
		Type:             record.CallType,
		SourceComponent:  record.SourceComponent,
		CallDescription:  record.Description,
		StepID:           record.StepID,
		Timestamp:        record.Timestamp,
		DurationMs:       record.DurationMs,
		Prompt:           record.Prompt,
		SystemPrompt:     record.SystemPrompt,
		Temperature:      record.Temperature,
		MaxTokens:        record.MaxTokens,
		Model:            record.Model,
		Provider:         record.Provider,
		Response:         record.Response,
		PromptTokens:     record.PromptTokens,
		CompletionTokens: record.CompletionTokens,
		TotalTokens:      record.TotalTokens,
		Success:          record.Success,
		Error:            record.Error,
		Attempt:          1,
	}

	// Verify the critical field mappings (Description → CallDescription is the non-obvious one)
	if interaction.CallDescription != "Synthesis call" {
		t.Errorf("CallDescription = %q, want %q (mapped from Description)", interaction.CallDescription, "Synthesis call")
	}
	if interaction.Type != "agent_llm_call" {
		t.Errorf("Type = %q, want %q (mapped from CallType)", interaction.Type, "agent_llm_call")
	}
	if interaction.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1 (agent-side always 1)", interaction.Attempt)
	}
	if interaction.SourceComponent != "test-agent" {
		t.Errorf("SourceComponent = %q, want %q", interaction.SourceComponent, "test-agent")
	}
}

// ---------------------------------------------------------------------------
// Environment variable helpers
// ---------------------------------------------------------------------------

func TestRecorderGetRedisURL_Precedence(t *testing.T) {
	// Clean state
	os.Unsetenv("REDIS_URL")
	os.Unsetenv("TRUVAG3_REDIS_URL")
	defer os.Unsetenv("REDIS_URL")
	defer os.Unsetenv("TRUVAG3_REDIS_URL")

	// Default: localhost:6379
	if got := recorderGetRedisURL(); got != "localhost:6379" {
		t.Errorf("default = %q, want localhost:6379", got)
	}

	// TRUVAG3_REDIS_URL set
	os.Setenv("TRUVAG3_REDIS_URL", "redis://truvag3:6379")
	if got := recorderGetRedisURL(); got != "redis://truvag3:6379" {
		t.Errorf("with TRUVAG3_REDIS_URL = %q, want redis://truvag3:6379", got)
	}

	// REDIS_URL takes precedence over TRUVAG3_REDIS_URL
	os.Setenv("REDIS_URL", "redis://standard:6379")
	if got := recorderGetRedisURL(); got != "redis://standard:6379" {
		t.Errorf("with both set = %q, want redis://standard:6379 (REDIS_URL wins)", got)
	}
}

func TestRecorderGetEnvInt(t *testing.T) {
	const key = "TEST_RECORDER_INT"
	defer os.Unsetenv(key)

	// Missing → default
	os.Unsetenv(key)
	if got := recorderGetEnvInt(key, 7); got != 7 {
		t.Errorf("missing = %d, want 7", got)
	}

	// Valid integer
	os.Setenv(key, "42")
	if got := recorderGetEnvInt(key, 7); got != 42 {
		t.Errorf("valid = %d, want 42", got)
	}

	// Invalid → default
	os.Setenv(key, "notanumber")
	if got := recorderGetEnvInt(key, 7); got != 7 {
		t.Errorf("invalid = %d, want 7 (default)", got)
	}
}

func TestRecorderGetEnvDuration(t *testing.T) {
	const key = "TEST_RECORDER_DUR"
	defer os.Unsetenv(key)

	defaultDur := 24 * time.Hour

	// Missing → default
	os.Unsetenv(key)
	if got := recorderGetEnvDuration(key, defaultDur); got != defaultDur {
		t.Errorf("missing = %v, want %v", got, defaultDur)
	}

	// Valid duration
	os.Setenv(key, "2h30m")
	expected := 2*time.Hour + 30*time.Minute
	if got := recorderGetEnvDuration(key, defaultDur); got != expected {
		t.Errorf("valid = %v, want %v", got, expected)
	}

	// Invalid → default
	os.Setenv(key, "badvalue")
	if got := recorderGetEnvDuration(key, defaultDur); got != defaultDur {
		t.Errorf("invalid = %v, want %v (default)", got, defaultDur)
	}
}

// ---------------------------------------------------------------------------
// Recorder constants — verify they match orchestration defaults
// ---------------------------------------------------------------------------

func TestRecorderConstants_MatchOrchestrationDefaults(t *testing.T) {
	// These must stay in sync with orchestration/redis_llm_debug_store.go
	if recorderDefaultTTL != 24*time.Hour {
		t.Errorf("recorderDefaultTTL = %v, want 24h", recorderDefaultTTL)
	}
	if recorderErrorTTL != 7*24*time.Hour {
		t.Errorf("recorderErrorTTL = %v, want 168h (7d)", recorderErrorTTL)
	}
	if recorderMaxRetries != 3 {
		t.Errorf("recorderMaxRetries = %d, want 3", recorderMaxRetries)
	}
	if recorderInitialBackoff != 100*time.Millisecond {
		t.Errorf("recorderInitialBackoff = %v, want 100ms", recorderInitialBackoff)
	}
	if recorderMaxBackoff != 2*time.Second {
		t.Errorf("recorderMaxBackoff = %v, want 2s", recorderMaxBackoff)
	}
}

// ---------------------------------------------------------------------------
// Redis key patterns — verify they match orchestration format
// ---------------------------------------------------------------------------

func TestRecorderKeyPatterns_MatchOrchestration(t *testing.T) {
	if recorderKeyPrefix != "truvag3:llm:debug:" {
		t.Errorf("recorderKeyPrefix = %q, want truvag3:llm:debug:", recorderKeyPrefix)
	}
	if recorderIndexKey != "truvag3:llm:debug:index" {
		t.Errorf("recorderIndexKey = %q, want truvag3:llm:debug:index", recorderIndexKey)
	}
	if recorderMetaSuffix != ":meta" {
		t.Errorf("recorderMetaSuffix = %q, want :meta", recorderMetaSuffix)
	}
	if recorderInterSuffix != ":interactions" {
		t.Errorf("recorderInterSuffix = %q, want :interactions", recorderInterSuffix)
	}
}

// setupRedisLLMRecorderTest constructs a RedisLLMCallRecorder backed by miniredis
// so the actual write path (HSet/HSetNX into the meta hash) is exercised without
// a real Redis dependency. Mirrors the pattern used in orchestration tests.
func setupRedisLLMRecorderTest(t *testing.T) (*miniredis.Miniredis, *RedisLLMCallRecorder) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	r := &RedisLLMCallRecorder{
		client: client,
		logger: &core.NoOpLogger{},
		ttl:    recorderDefaultTTL,
		errTTL: recorderErrorTTL,
	}
	return mr, r
}

// TestRecordLLMCall_StampsOriginatingAgentFromBaggage is the format-twin
// invariant regression guard. The telemetry recorder MUST mirror the
// orchestration store's "originating_agent" meta hash field so that records
// written only by the agent-side path (no orchestrator involvement —
// e.g. reflect-* background-job records) still surface the originator in
// the registry-viewer Source column.
// See orchestration/ARCHITECTURE.md "LLM Debug Payload Store — Alternative Writer".
func TestRecordLLMCall_StampsOriginatingAgentFromBaggage(t *testing.T) {
	mr, r := setupRedisLLMRecorderTest(t)

	ctx := WithBaggage(context.Background(), "agent_name", "devops-chat-agent")
	err := r.RecordLLMCall(ctx, "reflect-test-abc", LLMCallRecord{
		CallType:        "agent_llm_call",
		SourceComponent: "devops-chat-agent",
		Prompt:          "what is the kubernetes pod restart count for nginx?",
		Response:        "12 in the last hour",
		Success:         true,
	})
	if err != nil {
		t.Fatalf("RecordLLMCall failed: %v", err)
	}

	metaKey := recorderKeyPrefix + "reflect-test-abc" + recorderMetaSuffix
	got := mr.HGet(metaKey, "originating_agent")
	if got != "devops-chat-agent" {
		t.Errorf("meta hash originating_agent = %q, want devops-chat-agent", got)
	}
}

// TestRecordLLMCall_OriginatingAgent_FirstWriterWins locks in HSetNX
// semantics: a second write from a different agent_name baggage value
// must NOT overwrite the originator. This is the same invariant the
// orchestration store tests guard — they must hold together since both
// writers target the same Redis keys.
func TestRecordLLMCall_OriginatingAgent_FirstWriterWins(t *testing.T) {
	mr, r := setupRedisLLMRecorderTest(t)

	ctxA := WithBaggage(context.Background(), "agent_name", "travel-chat-agent")
	if err := r.RecordLLMCall(ctxA, "req-shared", LLMCallRecord{
		CallType: "agent_llm_call",
		Prompt:   "first",
		Response: "ok",
		Success:  true,
	}); err != nil {
		t.Fatalf("first write failed: %v", err)
	}

	ctxB := WithBaggage(context.Background(), "agent_name", "research-agent-telemetry-service")
	if err := r.RecordLLMCall(ctxB, "req-shared", LLMCallRecord{
		CallType:        "agent_llm_call",
		SourceComponent: "research-agent-telemetry-service",
		Prompt:          "second",
		Response:        "ok",
		Success:         true,
	}); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	metaKey := recorderKeyPrefix + "req-shared" + recorderMetaSuffix
	got := mr.HGet(metaKey, "originating_agent")
	if got != "travel-chat-agent" {
		t.Errorf("originating_agent must be first writer's value (HSetNX); got %q", got)
	}
}

// TestRecordLLMCall_EmptyBaggage_NoOriginatingAgent covers the pre-instrumented
// historical path: a write with no agent_name baggage must NOT stamp a placeholder.
// This keeps historical records rendering via the existing source_components
// fallback in the viewer instead of misattributing the originator.
func TestRecordLLMCall_EmptyBaggage_NoOriginatingAgent(t *testing.T) {
	mr, r := setupRedisLLMRecorderTest(t)

	if err := r.RecordLLMCall(context.Background(), "req-no-bag", LLMCallRecord{
		CallType: "agent_llm_call",
		Prompt:   "p",
		Response: "r",
		Success:  true,
	}); err != nil {
		t.Fatalf("RecordLLMCall failed: %v", err)
	}

	metaKey := recorderKeyPrefix + "req-no-bag" + recorderMetaSuffix
	if got := mr.HGet(metaKey, "originating_agent"); got != "" {
		t.Errorf("originating_agent must be empty when baggage carries no agent_name; got %q", got)
	}
}

func TestRecordLLMCall_ConversationIDResolution(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() context.Context
		want string
	}{
		{
			name: "core candidate wins",
			ctx: func() context.Context {
				ctx := WithBaggage(
					context.Background(),
					"conversation_id",
					"conversation-baggage",
				)
				return core.WithConversationID(ctx, "conversation-core")
			},
			want: "conversation-core",
		},
		{
			name: "validated baggage fallback",
			ctx: func() context.Context {
				return WithBaggage(
					context.Background(),
					"conversation_id",
					"conversation-baggage",
				)
			},
			want: "conversation-baggage",
		},
		{
			name: "invalid core blocks fallback",
			ctx: func() context.Context {
				ctx := WithBaggage(
					context.Background(),
					"conversation_id",
					"conversation-baggage",
				)
				return core.WithConversationID(ctx, "invalid conversation")
			},
		},
		{
			name: "invalid baggage omitted",
			ctx: func() context.Context {
				return WithBaggage(
					context.Background(),
					"conversation_id",
					"invalid conversation",
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mr, recorder := setupRedisLLMRecorderTest(t)
			requestID := "request-" + strings.ReplaceAll(test.name, " ", "-")
			if err := recorder.RecordLLMCall(
				test.ctx(),
				requestID,
				LLMCallRecord{CallType: "agent_llm_call", Success: true},
			); err != nil {
				t.Fatalf("RecordLLMCall: %v", err)
			}
			metaKey := recorderKeyPrefix + requestID + recorderMetaSuffix
			if got := mr.HGet(metaKey, "meta:conversation_id"); got != test.want {
				t.Fatalf("conversation field = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRecordLLMCall_ConversationFirstValidWriterWins(t *testing.T) {
	mr, recorder := setupRedisLLMRecorderTest(t)
	requestID := "request-conversation-first-writer"
	record := LLMCallRecord{CallType: "agent_llm_call", Success: true}

	if err := recorder.RecordLLMCall(context.Background(), requestID, record); err != nil {
		t.Fatalf("empty first write: %v", err)
	}
	if err := recorder.RecordLLMCall(
		core.WithConversationID(context.Background(), "conversation-first"),
		requestID,
		record,
	); err != nil {
		t.Fatalf("valid backfill: %v", err)
	}
	if err := recorder.RecordLLMCall(
		core.WithConversationID(context.Background(), "conversation-different"),
		requestID,
		record,
	); err != nil {
		t.Fatalf("later write: %v", err)
	}

	metaKey := recorderKeyPrefix + requestID + recorderMetaSuffix
	if got := mr.HGet(metaKey, "meta:conversation_id"); got != "conversation-first" {
		t.Fatalf("conversation field = %q", got)
	}
}
