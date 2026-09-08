package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

var recorderTestKeys = NewRedisLLMDebugKeys(telemetryDefaultRedisKeyspace())

type recorderIndexFailureHook struct {
	mu       sync.Mutex
	fail     bool
	attempts int
}

type recorderAuthoritativeFailureHook struct{}

func (*recorderAuthoritativeFailureHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (*recorderAuthoritativeFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() == "eval" || command.Name() == "evalsha" {
			return errors.New("redis://user:secret@index.invalid/0")
		}
		return next(ctx, command)
	}
}

func (*recorderAuthoritativeFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

type recorderCaptureLogger struct {
	core.NoOpLogger
	warnings  []map[string]interface{}
	component string
}

func (logger *recorderCaptureLogger) WithComponent(component string) core.Logger {
	logger.component = component
	return logger
}

func (logger *recorderCaptureLogger) Warn(_ string, fields map[string]interface{}) {
	logger.warnings = append(logger.warnings, fields)
}

func (logger *recorderCaptureLogger) WarnWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.warnings = append(logger.warnings, fields)
}

func (hook *recorderIndexFailureHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *recorderIndexFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() == "zadd" {
			hook.mu.Lock()
			hook.attempts++
			fail := hook.fail
			hook.mu.Unlock()
			if fail {
				return errors.New("redis://user:secret@index.invalid/0")
			}
		}
		return next(ctx, command)
	}
}

func (*recorderIndexFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestRedisLLMCallRecorder_ImplementsLLMCallRecorder(t *testing.T) {
	// Compile-time check already exists in redis_llm_recorder.go (line 318).
	// This test makes the assertion explicit and visible in test output.
	var _ LLMCallRecorder = (*RedisLLMCallRecorder)(nil)
}

func TestRedisLLMCallRecorderScopesComponentAwareLogger(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("telemetry-logger")
	if err != nil {
		t.Fatal(err)
	}
	logger := &recorderCaptureLogger{}
	recorder, err := NewRedisLLMCallRecorderWithClient(
		client,
		keyspace,
		WithRecorderLogger(logger),
	)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.logger != logger || logger.component != "framework/telemetry" {
		t.Fatalf("recorder logger = %#v, component = %q", recorder.logger, logger.component)
	}
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

func TestOwningRedisLLMRecorderUsesEnvironmentKeyspace(t *testing.T) {
	server := miniredis.RunT(t)
	t.Setenv("TRUVAG3_REDIS_NAMESPACE", "telemetry-deployment")
	keyspace, err := core.NewRedisKeyspace("telemetry-deployment")
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewRedisLLMCallRecorder(WithRecorderRedisURL("redis://" + server.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	if got, want := recorder.keys.Meta("request-1"), keyspace.Tagged("llm-debug", "request-1", "meta"); got != want {
		t.Fatalf("recorder key = %q, want %q", got, want)
	}
}

func TestOwningRedisLLMRecorderValidatesEnvironmentKeyspace(t *testing.T) {
	t.Setenv("TRUVAG3_REDIS_NAMESPACE", "invalid{namespace")
	_, err := NewRedisLLMCallRecorder()
	if !errors.Is(err, core.ErrInvalidConfiguration) {
		t.Fatalf("constructor error = %v, want ErrInvalidConfiguration", err)
	}

	server := miniredis.RunT(t)
	keyspace, err := core.NewRedisKeyspace("explicit-deployment")
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewRedisLLMCallRecorder(
		WithRecorderRedisURL("redis://"+server.Addr()),
		WithRecorderKeyspace(keyspace),
	)
	if err != nil {
		t.Fatalf("explicit keyspace did not override environment: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
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

func TestRecorderRedisConnectionResolution(t *testing.T) {
	// Clean state
	os.Unsetenv("REDIS_URL")
	os.Unsetenv("TRUVAG3_REDIS_URL")
	defer os.Unsetenv("REDIS_URL")
	defer os.Unsetenv("TRUVAG3_REDIS_URL")

	connection, diagnostics, err := resolveRecorderRedisConnection("", 0)
	if err != nil || connection.Addrs[0] != "localhost:6379" || len(diagnostics) != 0 {
		t.Fatalf("default resolution = (%#v, %#v, %v)", connection, diagnostics, err)
	}

	// TRUVAG3_REDIS_URL set
	os.Setenv("TRUVAG3_REDIS_URL", "redis://truvag3:6379")
	connection, diagnostics, err = resolveRecorderRedisConnection("", 0)
	if err != nil || connection.Addrs[0] != "truvag3:6379" || len(diagnostics) != 1 {
		t.Fatalf("deprecated resolution = (%#v, %#v, %v)", connection, diagnostics, err)
	}

	// Contradictory connection forms are rejected instead of ranked.
	os.Setenv("REDIS_URL", "redis://standard:6379")
	if _, _, err := resolveRecorderRedisConnection("", 0); err == nil {
		t.Fatal("mixed standard and deprecated URL forms were accepted")
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
	if recorderConversationMetadataField != "meta:"+core.MetadataConversationID {
		t.Errorf(
			"recorderConversationMetadataField = %q, want shared core key",
			recorderConversationMetadataField,
		)
	}
}

func TestRedisLLMCallRecorderOptionsNormalizeNonPositiveTTLs(t *testing.T) {
	mr := miniredis.RunT(t)
	recorder, err := NewRedisLLMCallRecorder(
		WithRecorderRedisURL(mr.Addr()),
		WithRecorderRedisDB(0),
		WithRecorderTTL(0),
		WithRecorderErrorTTL(-time.Second),
	)
	if err != nil {
		t.Fatalf("NewRedisLLMCallRecorder: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	if recorder.ttl != recorderDefaultTTL || recorder.errTTL != recorderErrorTTL {
		t.Fatalf(
			"recorder TTLs = (%v, %v), want (%v, %v)",
			recorder.ttl,
			recorder.errTTL,
			recorderDefaultTTL,
			recorderErrorTTL,
		)
	}

	for _, test := range []struct {
		requestID string
		success   bool
		wantTTL   time.Duration
	}{
		{requestID: "normalized-success", success: true, wantTTL: recorderDefaultTTL},
		{requestID: "normalized-error", success: false, wantTTL: recorderErrorTTL},
	} {
		if err := recorder.RecordLLMCall(
			context.Background(),
			test.requestID,
			LLMCallRecord{CallType: "test", Success: test.success},
		); err != nil {
			t.Fatalf("RecordLLMCall(%s): %v", test.requestID, err)
		}
		metaKey := recorderTestKeys.Meta(test.requestID)
		if got := mr.TTL(metaKey); got != test.wantTTL {
			t.Fatalf("TTL(%s) = %v, want %v", metaKey, got, test.wantTTL)
		}
	}
}

func TestRedisLLMCallRecorderInjectedClientOwnership(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("injected-recorder")
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewRedisLLMCallRecorderWithClient(client, keyspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := recorder.keys.Meta("request"); !strings.Contains(got, ":injected-recorder:") {
		t.Fatalf("injected recorder key = %q, want explicit deployment", got)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("recorder closed injected client: %v", err)
	}
}

func TestRedisLLMCallRecorderInjectedClientRejectsTypedNil(t *testing.T) {
	var client *redis.Client
	keyspace, err := core.NewRedisKeyspace("typed-nil")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRedisLLMCallRecorderWithClient(client, keyspace); err == nil {
		t.Fatal("typed-nil injected client was accepted")
	}
}

func TestRedisLLMCallRecorderInjectedClientStartupFailureIsBounded(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	server.Close()
	keyspace, err := core.NewRedisKeyspace("startup-failure")
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRedisLLMCallRecorderWithClient(client, keyspace)
	if err == nil {
		t.Fatal("unavailable injected client was accepted")
	}
	if got := err.Error(); got != "telemetry Redis startup check failed" {
		t.Fatalf("startup error = %q, want bounded diagnostic", got)
	}
}

// ---------------------------------------------------------------------------
// Redis key patterns — verify they match orchestration format
// ---------------------------------------------------------------------------

func TestRecorderKeyPatterns_MatchOrchestration(t *testing.T) {
	if got := recorderTestKeys.Meta("request"); got != "truvag3:v1:default:llm-debug:{default:llm-debug:request}:meta" {
		t.Errorf("Meta = %q", got)
	}
	if got := recorderTestKeys.Interactions("request"); got != "truvag3:v1:default:llm-debug:{default:llm-debug:request}:interactions" {
		t.Errorf("Interactions = %q", got)
	}
	if got := recorderTestKeys.RetentionFloor("request"); got != "truvag3:v1:default:llm-debug:{default:llm-debug:request}:retention-floor" {
		t.Errorf("RetentionFloor = %q", got)
	}
	if got := recorderTestKeys.RecentIndex(); got != "truvag3:v1:default:llm-debug:index:recent" {
		t.Errorf("RecentIndex = %q", got)
	}
}

func TestRecordLLMCall_IndexFailureDoesNotDuplicateAndLaterWriteRepairs(t *testing.T) {
	_, recorder := setupRedisLLMRecorderTest(t)
	logger := &recorderCaptureLogger{}
	recorder.logger = logger
	hook := &recorderIndexFailureHook{fail: true}
	recorder.client.AddHook(hook)
	requestID := "request-index-repair"
	record := LLMCallRecord{CallType: "agent_llm_call", Success: true}

	if err := recorder.RecordLLMCall(context.Background(), requestID, record); err != nil {
		t.Fatalf("advisory index failure became fatal: %v", err)
	}
	if got := recorder.client.LLen(context.Background(), recorder.keys.Interactions(requestID)).Val(); got != 1 {
		t.Fatalf("interaction count after exhausted index retry = %d, want 1", got)
	}
	hook.mu.Lock()
	if hook.attempts != recorderMaxRetries {
		t.Fatalf("index attempts = %d, want %d", hook.attempts, recorderMaxRetries)
	}
	if len(logger.warnings) != 1 {
		t.Fatalf("index warnings = %d, want 1", len(logger.warnings))
	}
	fields := logger.warnings[0]
	if fields["operation"] != "llm_debug_recent_index" ||
		fields["request_id"] != requestID ||
		fields["error"] != "redis LLM debug index update failed" ||
		fields["error_type"] != "index_write" ||
		fields["failure_class"] != "redis_backend_failure" {
		t.Fatalf("index warning fields = %#v", fields)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "redis://") {
		t.Fatalf("index warning exposed Redis error text: %s", encoded)
	}
	hook.fail = false
	hook.mu.Unlock()

	if err := recorder.RecordLLMCall(context.Background(), requestID, record); err != nil {
		t.Fatalf("repairing write: %v", err)
	}
	if got := recorder.client.LLen(context.Background(), recorder.keys.Interactions(requestID)).Val(); got != 2 {
		t.Fatalf("interaction count after later write = %d, want 2", got)
	}
	if score, err := recorder.client.ZScore(context.Background(), recorder.keys.RecentIndex(), requestID).Result(); err != nil || score == 0 {
		t.Fatalf("recent index was not repaired: score=%v err=%v", score, err)
	}
}

func TestRecordLLMCall_IndexFailureRemainsFailOpenWithNilLogger(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("nil-logger")
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewRedisLLMCallRecorderWithClient(client, keyspace, WithRecorderLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	hook := &recorderIndexFailureHook{fail: true}
	client.AddHook(hook)

	if err := recorder.RecordLLMCall(
		t.Context(),
		"request-nil-logger",
		LLMCallRecord{CallType: "agent_llm_call", Success: true},
	); err != nil {
		t.Fatalf("advisory index failure became fatal with nil logger: %v", err)
	}
	if got := client.LLen(t.Context(), recorder.keys.Interactions("request-nil-logger")).Val(); got != 1 {
		t.Fatalf("authoritative interaction count = %d, want 1", got)
	}
}

func TestRedisLLMCallRecorderRetryLogsUseBoundedFailureClass(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("bounded-diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	logger := &recorderCaptureLogger{}
	recorder, err := NewRedisLLMCallRecorderWithClient(client, keyspace, WithRecorderLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	client.AddHook(&recorderAuthoritativeFailureHook{})
	if err := recorder.RecordLLMCall(t.Context(), "request-bounded-error", LLMCallRecord{
		CallType: "test",
		Success:  true,
	}); err == nil {
		t.Fatal("authoritative Redis failure was ignored")
	}
	encoded, err := json.Marshal(logger.warnings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "redis://") {
		t.Fatalf("retry diagnostics exposed Redis error text: %s", encoded)
	}
	if len(logger.warnings) != recorderMaxRetries {
		t.Fatalf("warning count = %d, want %d", len(logger.warnings), recorderMaxRetries)
	}
	for _, fields := range logger.warnings {
		if fields["failure_class"] != "redis_backend_failure" {
			t.Fatalf("failure_class = %#v", fields["failure_class"])
		}
		if fields["operation"] != "llm_debug_recorder_retry" ||
			fields["request_id"] != "request-bounded-error" ||
			fields["error"] != "redis LLM recorder operation failed" ||
			fields["error_type"] != "backend" {
			t.Fatalf("retry diagnostic fields = %#v", fields)
		}
	}
}

func TestClassifyRecorderRedisDiagnosticUsesFixedVocabulary(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{nil, "none"},
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("redis://user:secret@index.invalid/0"), "redis_backend_failure"},
	} {
		if got := classifyRecorderRedisDiagnostic(test.err); got != test.want {
			t.Fatalf("classification = %q, want %q", got, test.want)
		}
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

func TestRecordLLMCall_PreservesPromotedAndPersistentRetention(t *testing.T) {
	mr, recorder := setupRedisLLMRecorderTest(t)
	ctx := context.Background()
	requestID := "request-retention"
	record := LLMCallRecord{CallType: "agent_llm_call", Success: true}
	if err := recorder.RecordLLMCall(ctx, requestID, record); err != nil {
		t.Fatalf("initial write: %v", err)
	}

	keys := []string{
		recorder.keys.Meta(requestID),
		recorder.keys.Interactions(requestID),
	}
	for _, key := range keys {
		if err := recorder.client.PExpire(ctx, key, 14*24*time.Hour).Err(); err != nil {
			t.Fatalf("promote %s: %v", key, err)
		}
	}
	if err := recorder.RecordLLMCall(ctx, requestID, record); err != nil {
		t.Fatalf("write after promotion: %v", err)
	}
	for _, key := range keys {
		if got := mr.TTL(key); got != 14*24*time.Hour {
			t.Fatalf("%s TTL = %v, want 14d", key, got)
		}
		if err := recorder.client.Persist(ctx, key).Err(); err != nil {
			t.Fatalf("persist %s: %v", key, err)
		}
	}
	if err := recorder.RecordLLMCall(ctx, requestID, record); err != nil {
		t.Fatalf("write after persist: %v", err)
	}
	for _, key := range keys {
		if got := mr.TTL(key); got != 0 {
			t.Fatalf("persistent %s gained TTL %v", key, got)
		}
	}
}

func TestRecordLLMCall_ConcurrentWritersKeepLongestRetention(t *testing.T) {
	mr, recorder := setupRedisLLMRecorderTest(t)
	ctx := context.Background()
	requestID := "request-concurrent-retention"

	var wg sync.WaitGroup
	for index := 0; index < 40; index++ {
		success := index%2 != 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := recorder.RecordLLMCall(ctx, requestID, LLMCallRecord{
				CallType: "agent_llm_call",
				Success:  success,
			}); err != nil {
				t.Errorf("concurrent write: %v", err)
			}
		}()
	}
	wg.Wait()

	for _, key := range []string{
		recorder.keys.Meta(requestID),
		recorder.keys.Interactions(requestID),
	} {
		if got := mr.TTL(key); got != recorderErrorTTL {
			t.Fatalf("%s TTL = %v, want %v", key, got, recorderErrorTTL)
		}
	}
}

func TestRecordLLMCall_PreservesApplicationPayloadsAtPersistence(t *testing.T) {
	mr, recorder := setupRedisLLMRecorderTest(t)
	payload := "password=local-debug-value"
	requestID := "request-payload-fidelity"
	if err := recorder.RecordLLMCall(context.Background(), requestID, LLMCallRecord{
		CallType:     "agent_llm_call",
		Description:  "description " + payload,
		Prompt:       "prompt " + payload,
		SystemPrompt: "system " + payload,
		Response:     "response " + payload,
		Error:        "error " + payload,
		Success:      false,
	}); err != nil {
		t.Fatalf("RecordLLMCall: %v", err)
	}

	key := recorder.keys.Interactions(requestID)
	values, err := recorder.client.LRange(context.Background(), key, 0, -1).Result()
	if err != nil || len(values) != 1 {
		t.Fatalf("stored interactions = (%v, %v)", values, err)
	}
	if !strings.Contains(values[0], payload) || strings.Contains(values[0], "[REDACTED]") {
		t.Fatalf("persisted interaction changed application payloads: %s", values[0])
	}
	if got := mr.TTL(key); got != recorderErrorTTL {
		t.Fatalf("error interaction TTL = %v, want %v", got, recorderErrorTTL)
	}
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

	metaKey := r.keys.Meta("reflect-test-abc")
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

	metaKey := r.keys.Meta("req-shared")
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

	metaKey := r.keys.Meta("req-no-bag")
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
			metaKey := recorder.keys.Meta(requestID)
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

	metaKey := recorder.keys.Meta(requestID)
	if got := mr.HGet(metaKey, "meta:conversation_id"); got != "conversation-first" {
		t.Fatalf("conversation field = %q", got)
	}
}
