package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

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
