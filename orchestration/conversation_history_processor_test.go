package orchestration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// conversationHistoryTestLogger is safe for concurrent use. The processor's
// async debug-recording goroutine (see recordPreparationInteraction) can call
// Warn() while the test reads the captured fields, so every access goes
// through the mutex. Tests read via InfoFields()/WarnFields() which return
// copies holding the lock only for the duration of the copy.
type conversationHistoryTestLogger struct {
	mu         sync.Mutex
	infoFields []map[string]interface{}
	warnFields []map[string]interface{}
}

// conversationHistoryRecordingDebugStore is safe for concurrent use. The
// processor's async debug-recording goroutine calls RecordInteraction while
// tests may still be running; tests must synchronize via processor.Shutdown()
// before reading so the goroutine has finished, and then read via the
// Snapshot accessor which takes a copy under the lock.
type conversationHistoryRecordingDebugStore struct {
	mu           sync.Mutex
	requestIDs   []string
	interactions []LLMInteraction
}

type blockingConversationDebugStore struct {
	started chan struct{}
	release chan struct{}
}

type stubConversationCompactor struct {
	summary string
	calls   int
}

func (c *stubConversationCompactor) Compact(ctx context.Context, priorSummary string, newTurns []core.ConversationTurn) (string, error) {
	c.calls++
	return c.summary, nil
}

// InfoFields returns a snapshot of captured info fields. Safe to call while
// async goroutines may still be writing — the copy is taken under the mutex.
func (l *conversationHistoryTestLogger) InfoFields() []map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]map[string]interface{}, len(l.infoFields))
	copy(out, l.infoFields)
	return out
}

// WarnFields returns a snapshot of captured warn fields. Safe to call while
// async goroutines may still be writing — the copy is taken under the mutex.
func (l *conversationHistoryTestLogger) WarnFields() []map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]map[string]interface{}, len(l.warnFields))
	copy(out, l.warnFields)
	return out
}

func (l *conversationHistoryTestLogger) Debug(string, map[string]interface{}) {}
func (l *conversationHistoryTestLogger) Info(_ string, fields map[string]interface{}) {
	cloned := cloneConversationHistoryTestFields(fields)
	l.mu.Lock()
	l.infoFields = append(l.infoFields, cloned)
	l.mu.Unlock()
}
func (l *conversationHistoryTestLogger) Warn(_ string, fields map[string]interface{}) {
	cloned := cloneConversationHistoryTestFields(fields)
	l.mu.Lock()
	l.warnFields = append(l.warnFields, cloned)
	l.mu.Unlock()
}
func (l *conversationHistoryTestLogger) Error(string, map[string]interface{}) {}
func (l *conversationHistoryTestLogger) DebugWithContext(context.Context, string, map[string]interface{}) {
}
func (l *conversationHistoryTestLogger) InfoWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	cloned := cloneConversationHistoryTestFields(fields)
	l.mu.Lock()
	l.infoFields = append(l.infoFields, cloned)
	l.mu.Unlock()
}
func (l *conversationHistoryTestLogger) WarnWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	cloned := cloneConversationHistoryTestFields(fields)
	l.mu.Lock()
	l.warnFields = append(l.warnFields, cloned)
	l.mu.Unlock()
}
func (l *conversationHistoryTestLogger) ErrorWithContext(context.Context, string, map[string]interface{}) {
}

func (s *conversationHistoryRecordingDebugStore) RecordInteraction(_ context.Context, requestID string, interaction LLMInteraction) error {
	s.mu.Lock()
	s.requestIDs = append(s.requestIDs, requestID)
	s.interactions = append(s.interactions, interaction)
	s.mu.Unlock()
	return nil
}

// Snapshot returns copies of the recorded request IDs and interactions. Take
// the snapshot after waiting on processor.Shutdown() so no goroutines are
// still writing; the lock then covers a consistent read.
func (s *conversationHistoryRecordingDebugStore) Snapshot() (requestIDs []string, interactions []LLMInteraction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	requestIDs = append([]string(nil), s.requestIDs...)
	interactions = append([]LLMInteraction(nil), s.interactions...)
	return
}

func (s *conversationHistoryRecordingDebugStore) GetRecord(context.Context, string) (*LLMDebugRecord, error) {
	return nil, nil
}

func (s *conversationHistoryRecordingDebugStore) SetMetadata(context.Context, string, string, string) error {
	return nil
}

func (s *conversationHistoryRecordingDebugStore) ExtendTTL(context.Context, string, time.Duration) error {
	return nil
}

func (s *conversationHistoryRecordingDebugStore) ListRecent(context.Context, int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}

func (s *blockingConversationDebugStore) RecordInteraction(_ context.Context, _ string, _ LLMInteraction) error {
	close(s.started)
	<-s.release
	return nil
}

func (s *blockingConversationDebugStore) GetRecord(context.Context, string) (*LLMDebugRecord, error) {
	return nil, nil
}

func (s *blockingConversationDebugStore) SetMetadata(context.Context, string, string, string) error {
	return nil
}

func (s *blockingConversationDebugStore) ExtendTTL(context.Context, string, time.Duration) error {
	return nil
}

func (s *blockingConversationDebugStore) ListRecent(context.Context, int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}

func cloneConversationHistoryTestFields(fields map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(fields))
	for k, v := range fields {
		cloned[k] = v
	}
	return cloned
}

func TestConversationHistoryProcessor_PrepareFromText_LogsAndStartsPrepareSpan(t *testing.T) {
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          2000,
		RecentTurnsPreserved: 2,
	})
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}

	logger := &conversationHistoryTestLogger{}
	tel := &mockTelemetry{}
	processor.SetLogger(logger)
	processor.SetTelemetry(tel)

	ctx := telemetry.WithBaggage(context.Background(),
		"request_id", "req-conv-1",
		"original_request_id", "req-root-1",
	)
	prepared, result, err := processor.PrepareFromText(ctx, "", "User: hello\nAssistant: hi")
	if err != nil {
		t.Fatalf("PrepareFromText() error = %v", err)
	}
	if prepared == "" {
		t.Fatal("expected prepared history")
	}
	if result.Path != "metadata_text" {
		t.Fatalf("result.Path = %q, want metadata_text", result.Path)
	}
	if len(tel.spans) == 0 || tel.spans[0] != "conversation_history.prepare" {
		t.Fatalf("expected conversation_history.prepare span, got %v", tel.spans)
	}
	infoFields := logger.InfoFields()
	if len(infoFields) == 0 {
		t.Fatal("expected info log fields to be captured")
	}
	fields := infoFields[len(infoFields)-1]
	if got := fields["operation"]; got != "conversation_history" {
		t.Fatalf("operation = %v, want conversation_history", got)
	}
	if got := fields["request_id"]; got != "req-conv-1" {
		t.Fatalf("request_id = %v, want req-conv-1", got)
	}
	if got := fields["original_request_id"]; got != "req-root-1" {
		t.Fatalf("original_request_id = %v, want req-root-1", got)
	}
	if got := fields["path"]; got != "metadata_text" {
		t.Fatalf("path = %v, want metadata_text", got)
	}
	if _, ok := fields["duration_ms"]; !ok {
		t.Fatal("expected duration_ms in preparation log")
	}
}

func TestBuildCompactionEnabledConversationHistoryPreparer_EnablesTier2(t *testing.T) {
	config := DefaultConfig()
	config.ConversationRecentTurnsPreserved = 1
	config.ConversationSummaryCacheSize = 8

	processor, err := BuildCompactionEnabledConversationHistoryPreparer(config, &conversationCompactorTestAIClient{
		response: &core.AIResponse{
			Content:  "rolled up summary",
			Model:    "test-model",
			Provider: "test-provider",
		},
	})
	if err != nil {
		t.Fatalf("BuildCompactionEnabledConversationHistoryPreparer() error = %v", err)
	}
	if processor.summaryCache == nil {
		t.Fatal("expected helper to install a default summary cache")
	}
	if processor.compactor == nil {
		t.Fatal("expected helper to install a default compactor")
	}

	prepared, result, err := processor.PrepareFromTurns(context.Background(), "sess-1", []core.ConversationTurn{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("PrepareFromTurns() error = %v", err)
	}
	if prepared == "" {
		t.Fatal("expected prepared history")
	}
	if !result.Compacted {
		t.Fatal("expected helper-enabled preparer to run Tier 2 compaction")
	}
	if result.TurnsCompacted != 1 {
		t.Fatalf("TurnsCompacted = %d, want 1", result.TurnsCompacted)
	}
}

func TestBuildCompactionEnabledConversationHistoryPreparer_AllowsLayer2Overrides(t *testing.T) {
	config := DefaultConfig()
	config.ConversationRecentTurnsPreserved = 1

	customCache, err := NewSummaryCache(2)
	if err != nil {
		t.Fatalf("NewSummaryCache() error = %v", err)
	}
	customCompactor := &stubConversationCompactor{summary: "custom summary"}

	processor, err := BuildCompactionEnabledConversationHistoryPreparer(
		config,
		&conversationCompactorTestAIClient{
			response: &core.AIResponse{Content: "default summary"},
		},
		WithConversationSummaryCache(customCache),
		WithConversationCompactor(customCompactor),
	)
	if err != nil {
		t.Fatalf("BuildCompactionEnabledConversationHistoryPreparer() error = %v", err)
	}
	if processor.summaryCache != customCache {
		t.Fatal("expected custom summary cache to override helper default")
	}
	if processor.compactor != customCompactor {
		t.Fatal("expected custom compactor to override helper default")
	}

	_, result, err := processor.PrepareFromTurns(context.Background(), "sess-override", []core.ConversationTurn{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("PrepareFromTurns() error = %v", err)
	}
	if !result.Compacted {
		t.Fatal("expected override compactor to be used")
	}
	if customCompactor.calls != 1 {
		t.Fatalf("custom compactor calls = %d, want 1", customCompactor.calls)
	}
}

func TestConversationHistoryHook_UsesHookPathInProcessorLogs(t *testing.T) {
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          2000,
		RecentTurnsPreserved: 2,
	})
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}

	logger := &conversationHistoryTestLogger{}
	processor.SetLogger(logger)

	memory := &core.MockConversationMemory{
		GetHistFn: func(ctx context.Context, sessionID string, maxTurns int) ([]core.ConversationTurn, error) {
			return []core.ConversationTurn{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			}, nil
		},
	}
	hook, err := NewConversationHistoryHook(
		memory,
		"session-hook-1",
		WithConversationHistoryPreparer(processor),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}

	pctx := &core.PipelineContext{Enrichments: map[string]interface{}{}}
	ctx := telemetry.WithBaggage(context.Background(),
		"request_id", "req-hook-1",
		"original_request_id", "req-root-hook-1",
	)
	if _, err := hook.BeforePlanning(ctx, pctx); err != nil {
		t.Fatalf("BeforePlanning() error = %v", err)
	}
	infoFields := logger.InfoFields()
	if len(infoFields) == 0 {
		t.Fatal("expected hook-driven preparation log")
	}
	fields := infoFields[len(infoFields)-1]
	if got := fields["path"]; got != "hook" {
		t.Fatalf("path = %v, want hook", got)
	}
}

func TestConversationHistoryProcessor_PrepareFromTurns_RecordsPreparationInteraction(t *testing.T) {
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          2000,
		RecentTurnsPreserved: 2,
	})
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}

	debugStore := &conversationHistoryRecordingDebugStore{}
	processor.SetLLMDebugStore(debugStore)

	ctx := telemetry.WithBaggage(context.Background(),
		"request_id", "req-history-prepare-1",
		"original_request_id", "req-history-root-1",
	)
	_, result, err := processor.PrepareFromTurns(ctx, "sess-prepare-1", []core.ConversationTurn{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("PrepareFromTurns() error = %v", err)
	}
	// Shutdown waits on the processor's debugWg, guaranteeing the async
	// recordPreparationInteraction goroutine has finished. This replaces the
	// prior polling loop and gives -race a happens-before edge between the
	// goroutine's append and the test's read.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := processor.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	requestIDs, interactions := debugStore.Snapshot()
	if len(interactions) != 1 {
		t.Fatalf("expected 1 debug interaction, got %d", len(interactions))
	}
	interaction := interactions[0]
	if interaction.Type != "conversation_history_prepare" {
		t.Fatalf("interaction.Type = %q, want conversation_history_prepare", interaction.Type)
	}
	if interaction.HookPhase != HookPhasePre {
		t.Fatalf("interaction.HookPhase = %q, want %q", interaction.HookPhase, HookPhasePre)
	}
	if interaction.Category != "logic" {
		t.Fatalf("interaction.Category = %q, want logic", interaction.Category)
	}
	if interaction.CallDescription != "Token-aware conversation history preparation" {
		t.Fatalf("interaction.CallDescription = %q", interaction.CallDescription)
	}
	if !interaction.Success {
		t.Fatal("expected successful preparation interaction")
	}
	if got := requestIDs[0]; got != "req-history-prepare-1" {
		t.Fatalf("recorded request id = %q, want req-history-prepare-1", got)
	}
	if interaction.Response == "" {
		t.Fatal("expected preparation response summary to be populated")
	}
	if want := outcomeFromResult(result); want == "" || !strings.Contains(interaction.Response, "outcome="+want) {
		t.Fatalf("interaction.Response = %q, want outcome summary for %q", interaction.Response, want)
	}
	if !strings.Contains(interaction.Response, "path=metadata_turns") {
		t.Fatalf("interaction.Response = %q, want metadata_turns path", interaction.Response)
	}
}

func TestConversationHistoryProcessor_Shutdown_WaitsForDebugRecording(t *testing.T) {
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          2000,
		RecentTurnsPreserved: 2,
	})
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}

	store := &blockingConversationDebugStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	processor.SetLLMDebugStore(store)

	_, _, err = processor.PrepareFromTurns(context.Background(), "sess-shutdown-1", []core.ConversationTurn{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("PrepareFromTurns() error = %v", err)
	}

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for debug recording to start")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- processor.Shutdown(context.Background())
	}()

	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown() returned early with %v; expected it to wait for in-flight recording", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(store.release)

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown() did not complete after releasing debug store")
	}
}
