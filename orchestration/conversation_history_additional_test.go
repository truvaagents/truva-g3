package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type fixedTokenCounter struct {
	count int
	err   error
}

func (c fixedTokenCounter) CountTokens(_ context.Context, _ string) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.count, nil
}

type lengthTokenCounter struct{}

func (lengthTokenCounter) CountTokens(_ context.Context, text string) (int, error) {
	return len(text), nil
}

type namedConversationCounter struct{}

func (namedConversationCounter) CountTokens(context.Context, string) (int, error) {
	return 1, nil
}

type debugConversationLogger struct {
	debugFields []map[string]interface{}
	infoFields  []map[string]interface{}
	errorFields []map[string]interface{}
	warnFields  []map[string]interface{}
}

func (l *debugConversationLogger) Debug(_ string, fields map[string]interface{}) {
	l.debugFields = append(l.debugFields, cloneConversationHistoryTestFields(fields))
}
func (l *debugConversationLogger) Info(_ string, fields map[string]interface{}) {
	l.infoFields = append(l.infoFields, cloneConversationHistoryTestFields(fields))
}
func (l *debugConversationLogger) Warn(_ string, fields map[string]interface{}) {
	l.warnFields = append(l.warnFields, cloneConversationHistoryTestFields(fields))
}
func (l *debugConversationLogger) Error(_ string, fields map[string]interface{}) {
	l.errorFields = append(l.errorFields, cloneConversationHistoryTestFields(fields))
}
func (l *debugConversationLogger) DebugWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	l.debugFields = append(l.debugFields, cloneConversationHistoryTestFields(fields))
}
func (l *debugConversationLogger) InfoWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	l.infoFields = append(l.infoFields, cloneConversationHistoryTestFields(fields))
}
func (l *debugConversationLogger) WarnWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	l.warnFields = append(l.warnFields, cloneConversationHistoryTestFields(fields))
}
func (l *debugConversationLogger) ErrorWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	l.errorFields = append(l.errorFields, cloneConversationHistoryTestFields(fields))
}

type recordingConversationCompactor struct {
	responses      []string
	err            error
	callCount      int
	priorSummaries []string
	newTurnSets    [][]core.ConversationTurn
	logger         core.Logger
	telemetry      core.Telemetry
	debugStore     LLMDebugStore
}

func (c *recordingConversationCompactor) Compact(_ context.Context, priorSummary string, newTurns []core.ConversationTurn) (string, error) {
	c.callCount++
	c.priorSummaries = append(c.priorSummaries, priorSummary)
	c.newTurnSets = append(c.newTurnSets, cloneConversationTurns(newTurns))
	if c.err != nil {
		return "", c.err
	}
	if len(c.responses) >= c.callCount {
		return c.responses[c.callCount-1], nil
	}
	return priorSummary, nil
}

func (c *recordingConversationCompactor) SetLogger(logger core.Logger) {
	c.logger = logger
}

func (c *recordingConversationCompactor) SetTelemetry(t core.Telemetry) {
	c.telemetry = t
}

func (c *recordingConversationCompactor) SetLLMDebugStore(store LLMDebugStore) {
	c.debugStore = store
}

type capturePreparer struct {
	logger    core.Logger
	telemetry core.Telemetry
	debug     LLMDebugStore
}

func (p *capturePreparer) PrepareFromText(_ context.Context, _ string, formatted string) (string, HistoryPreparationResult, error) {
	return formatted, HistoryPreparationResult{Path: "metadata_text"}, nil
}

func (p *capturePreparer) PrepareFromTurns(_ context.Context, _ string, turns []core.ConversationTurn) (string, HistoryPreparationResult, error) {
	return formatConversationTurns(turns), HistoryPreparationResult{Path: "metadata_turns"}, nil
}

func (p *capturePreparer) SetLogger(logger core.Logger) {
	p.logger = logger
}

func (p *capturePreparer) SetTelemetry(t core.Telemetry) {
	p.telemetry = t
}

func (p *capturePreparer) SetLLMDebugStore(store LLMDebugStore) {
	p.debug = store
}

type basicConversationMemory struct {
	history  []core.ConversationTurn
	err      error
	maxTurns int
}

func (m *basicConversationMemory) AddTurn(context.Context, string, core.ConversationTurn) error {
	return nil
}

func (m *basicConversationMemory) GetHistory(_ context.Context, _ string, maxTurns int) ([]core.ConversationTurn, error) {
	m.maxTurns = maxTurns
	if m.err != nil {
		return nil, m.err
	}
	return cloneConversationTurns(m.history), nil
}

func (m *basicConversationMemory) Clear(context.Context, string) error {
	return nil
}

type staticConversationPreparer struct {
	textPrepared  string
	turnsPrepared string
	err           error
}

func (p *staticConversationPreparer) PrepareFromText(_ context.Context, _ string, _ string) (string, HistoryPreparationResult, error) {
	return p.textPrepared, HistoryPreparationResult{Path: "metadata_text"}, p.err
}

func (p *staticConversationPreparer) PrepareFromTurns(_ context.Context, _ string, _ []core.ConversationTurn) (string, HistoryPreparationResult, error) {
	return p.turnsPrepared, HistoryPreparationResult{Path: "metadata_turns"}, p.err
}

type failingConversationDebugStore struct {
	requestIDs []string
}

func (s *failingConversationDebugStore) RecordInteraction(_ context.Context, requestID string, _ LLMInteraction) error {
	s.requestIDs = append(s.requestIDs, requestID)
	return errors.New("debug store unavailable")
}

func (s *failingConversationDebugStore) GetRecord(_ context.Context, _ string) (*LLMDebugRecord, error) {
	return nil, nil
}

func (s *failingConversationDebugStore) SetMetadata(_ context.Context, _, _, _ string) error {
	return nil
}

func (s *failingConversationDebugStore) ExtendTTL(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (s *failingConversationDebugStore) ListRecent(_ context.Context, _ int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}

type captureTelemetry struct {
	spans []string
	last  *mockSpan
}

func (t *captureTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	t.spans = append(t.spans, name)
	t.last = &mockSpan{name: name}
	return ctx, t.last
}

func (t *captureTelemetry) RecordMetric(string, float64, map[string]string) {}

type nilSpanTelemetry struct{}

func (nilSpanTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	return ctx, nil
}

func (nilSpanTelemetry) RecordMetric(string, float64, map[string]string) {}

func TestSummaryCache_LifecycleAndEviction(t *testing.T) {
	if _, err := NewSummaryCache(0); err == nil {
		t.Fatal("expected invalid cache size to fail")
	}

	cache, err := NewSummaryCache(2)
	if err != nil {
		t.Fatalf("NewSummaryCache() error = %v", err)
	}

	if _, ok := cache.Get(""); ok {
		t.Fatal("expected empty session key lookup to miss")
	}

	cache.Set("a", SummaryState{Summary: "first"})
	cache.Set("b", SummaryState{Summary: "second"})
	if got := cache.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}

	got, ok := cache.Get("a")
	if !ok || got.Summary != "first" {
		t.Fatalf("Get(a) = %+v, %v", got, ok)
	}

	cache.Set("a", SummaryState{Summary: "first-updated"})
	got, ok = cache.Get("a")
	if !ok || got.Summary != "first-updated" {
		t.Fatalf("updated Get(a) = %+v, %v", got, ok)
	}

	cache.Set("c", SummaryState{Summary: "third"})
	if _, ok := cache.Get("b"); ok {
		t.Fatal("expected least recently used entry b to be evicted")
	}

	cache.Delete("a")
	if _, ok := cache.Get("a"); ok {
		t.Fatal("expected a to be deleted")
	}
	cache.Delete("")
	cache.Delete("missing")

	var nilCache *SummaryCache
	nilCache.Set("x", SummaryState{Summary: "ignored"})
	nilCache.Delete("x")
	if got := nilCache.Len(); got != 0 {
		t.Fatalf("nil Len() = %d, want 0", got)
	}
}

func TestHeuristicTokenCounter_CountTokens(t *testing.T) {
	counter := HeuristicTokenCounter{}

	tokens, err := counter.CountTokens(context.Background(), "")
	if err != nil || tokens != 0 {
		t.Fatalf("empty CountTokens() = %d, %v", tokens, err)
	}

	tokens, err = counter.CountTokens(context.Background(), "1234567")
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if tokens != 2 {
		t.Fatalf("CountTokens() = %d, want 2", tokens)
	}
}

func TestConversationHistoryObservabilityHelpers(t *testing.T) {
	ctx := WithRequestID(context.Background(), "ctx-req")
	if got := requestIDFromBaggage(ctx); got != "ctx-req" {
		t.Fatalf("requestIDFromBaggage() = %q, want ctx-req", got)
	}
	if got := originalRequestIDFromBaggage(ctx); got != "ctx-req" {
		t.Fatalf("originalRequestIDFromBaggage() = %q, want ctx-req", got)
	}

	baggageCtx := telemetry.WithBaggage(ctx,
		"request_id", "bag-req",
		"original_request_id", "bag-root",
	)
	if got := requestIDFromBaggage(baggageCtx); got != "bag-req" {
		t.Fatalf("requestIDFromBaggage() = %q, want bag-req", got)
	}
	if got := originalRequestIDFromBaggage(baggageCtx); got != "bag-root" {
		t.Fatalf("originalRequestIDFromBaggage() = %q, want bag-root", got)
	}

	tel := &captureTelemetry{}
	_, span := startPrepareSpan(baggageCtx, tel, "hook")
	mock, ok := span.(*mockSpan)
	if !ok {
		t.Fatalf("expected mockSpan, got %T", span)
	}
	if mock.attributes["request_id"] != "bag-req" || mock.attributes["original_request_id"] != "bag-root" || mock.attributes["path"] != "hook" {
		t.Fatalf("unexpected prepare span attrs: %+v", mock.attributes)
	}

	_, span = startCompactionSpan(baggageCtx, tel, "")
	mock, ok = span.(*mockSpan)
	if !ok {
		t.Fatalf("expected mockSpan, got %T", span)
	}
	if len(tel.spans) < 2 || tel.spans[1] != "conversation_history.compact" {
		t.Fatalf("unexpected spans: %v", tel.spans)
	}
	if mock.attributes["request_id"] != "bag-req" || mock.attributes["original_request_id"] != "bag-root" {
		t.Fatalf("unexpected compaction span attrs: %+v", mock.attributes)
	}

	_, span = startCompactionSpan(context.Background(), tel, "custom.compaction")
	mock, ok = span.(*mockSpan)
	if !ok {
		t.Fatalf("expected mockSpan, got %T", span)
	}
	if len(tel.spans) < 3 || tel.spans[2] != "custom.compaction" {
		t.Fatalf("unexpected custom compaction span names: %v", tel.spans)
	}
	if _, hasPath := mock.attributes["path"]; hasPath {
		t.Fatalf("did not expect path attribute on compaction span: %+v", mock.attributes)
	}

	_, span = startPrepareSpan(context.Background(), nil, "")
	if _, ok := span.(*core.NoOpSpan); !ok {
		t.Fatalf("expected NoOpSpan when telemetry is nil, got %T", span)
	}
	_, span = startPrepareSpan(context.Background(), nilSpanTelemetry{}, "")
	if _, ok := span.(*core.NoOpSpan); !ok {
		t.Fatalf("expected NoOpSpan when telemetry returns nil span, got %T", span)
	}
	_, span = startCompactionSpan(context.Background(), nilSpanTelemetry{}, "")
	if _, ok := span.(*core.NoOpSpan); !ok {
		t.Fatalf("expected NoOpSpan for compaction when telemetry returns nil span, got %T", span)
	}
}

func TestEmitConversationHistoryMetrics_CoversCompactionAndDropPaths(t *testing.T) {
	emitConversationHistoryMetrics("agent-a", HistoryPreparationResult{
		Path:                 "metadata_turns",
		TurnsDropped:         2,
		EstimatedTokensPre:   50,
		EstimatedTokensPost:  20,
		CompactionDurationMs: 7,
	}, "dropped", "success", 3)

	emitConversationHistoryMetrics("agent-b", HistoryPreparationResult{
		Path:                "metadata_text",
		EstimatedTokensPre:  10,
		EstimatedTokensPost: 10,
	}, "pass_through", "", 0)
}

func TestConversationTurnsFromMetadataAndDecodeConversationTurn(t *testing.T) {
	typed := []core.ConversationTurn{{Role: "user", Content: "hello"}}
	decoded, ok := conversationTurnsFromMetadata(typed)
	if !ok || len(decoded) != 1 || decoded[0].Content != "hello" {
		t.Fatalf("typed conversationTurnsFromMetadata() = %+v, %v", decoded, ok)
	}
	decoded[0].Content = "changed"
	if typed[0].Content != "hello" {
		t.Fatal("expected typed metadata decoding to clone turns")
	}

	now := time.Now().UTC().Round(0)
	raw := map[string]interface{}{
		"role":      "assistant",
		"content":   "world",
		"timestamp": now.Format(time.RFC3339Nano),
		"metadata":  map[string]interface{}{"x": "y"},
	}
	turn, ok := decodeConversationTurn(raw)
	if !ok || turn.Role != "assistant" || turn.Content != "world" || turn.Metadata["x"] != "y" || !turn.Timestamp.Equal(now) {
		t.Fatalf("decodeConversationTurn(raw) = %+v, %v", turn, ok)
	}

	roundTripped, ok := conversationTurnsFromMetadata([]interface{}{raw})
	if !ok || len(roundTripped) != 1 || roundTripped[0].Content != "world" {
		t.Fatalf("round-tripped conversationTurnsFromMetadata() = %+v, %v", roundTripped, ok)
	}

	if _, ok := decodeConversationTurn(map[string]interface{}{"timestamp": "not-a-time"}); ok {
		t.Fatal("expected invalid-only turn payload to fail decoding")
	}
	if _, ok := decodeConversationTurn("bad"); ok {
		t.Fatal("expected unsupported raw turn to fail decoding")
	}
	if _, ok := conversationTurnsFromMetadata([]interface{}{"bad"}); ok {
		t.Fatal("expected unsupported metadata turns to fail decoding")
	}
	if _, ok := conversationTurnsFromMetadata(nil); ok {
		t.Fatal("expected nil metadata turns to fail decoding")
	}
}

func TestPrepareKnownEnrichments_UsesPreparedLegacyTextAndIgnoresInvalidTurnPayload(t *testing.T) {
	preparer := &stubConversationHistoryPreparer{prepared: "prepared legacy"}
	enrichments := map[string]interface{}{}
	metadata := map[string]interface{}{
		MetadataConversationTurns:          []interface{}{"bad"},
		MetadataConversationSessionKey:     "session-1",
		core.EnrichmentConversationHistory: "legacy history",
	}

	prepareKnownEnrichments(context.Background(), metadata, enrichments, preparer)

	if got := enrichments[core.EnrichmentConversationHistory]; got != "prepared legacy" {
		t.Fatalf("prepared history = %v, want prepared legacy", got)
	}
	if preparer.lastText != "legacy history" {
		t.Fatalf("expected preparer to receive legacy history, got %q", preparer.lastText)
	}

	empty := map[string]interface{}{}
	prepareKnownEnrichments(context.Background(), nil, empty, preparer)
	if len(empty) != 0 {
		t.Fatalf("expected empty enrichments to remain empty, got %+v", empty)
	}
}

func TestConversationHistoryProcessor_ValidationAndSetterPropagation(t *testing.T) {
	if _, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget - 1,
		RecentTurnsPreserved: 1,
	}); err == nil {
		t.Fatal("expected invalid token budget to fail")
	}
	if _, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 0,
	}); err == nil {
		t.Fatal("expected invalid recent turns preserved to fail")
	}
	if _, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 1,
	}, WithConversationTokenCounter(nil)); err == nil {
		t.Fatal("expected nil token counter option to fail")
	}
	if _, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 1,
	}, WithConversationSummaryCache(nil)); err == nil {
		t.Fatal("expected nil summary cache option to fail")
	}
	if _, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 1,
	}, WithConversationCompactor(nil)); err == nil {
		t.Fatal("expected nil compactor option to fail")
	}

	cache, err := NewSummaryCache(2)
	if err != nil {
		t.Fatalf("NewSummaryCache() error = %v", err)
	}
	compactor := &recordingConversationCompactor{responses: []string{"summary"}}
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 1,
	}, WithConversationSummaryCache(cache), WithConversationCompactor(compactor))
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}

	componentLogger := &mockComponentAwareLogger{}
	processor.SetLogger(componentLogger)
	if componentLogger.component != "framework/orchestration" {
		t.Fatalf("component logger = %q, want framework/orchestration", componentLogger.component)
	}
	if _, ok := compactor.logger.(*core.NoOpLogger); !ok && compactor.logger == nil {
		t.Fatal("expected compactor logger to be forwarded")
	}

	tel := &mockTelemetry{}
	processor.SetTelemetry(tel)
	if compactor.telemetry != tel {
		t.Fatal("expected telemetry to propagate to compactor")
	}

	store := &mockLLMDebugStore{}
	processor.SetLLMDebugStore(store)
	if compactor.debugStore != store {
		t.Fatal("expected debug store to propagate to compactor")
	}

	processor.SetLogger(nil)
	if _, ok := processor.logger.(*core.NoOpLogger); !ok {
		t.Fatalf("expected nil logger to reset to NoOpLogger, got %T", processor.logger)
	}
	processor.SetTelemetry(nil)
	if _, ok := processor.telemetry.(*core.NoOpTelemetry); !ok {
		t.Fatalf("expected nil telemetry to reset to NoOpTelemetry, got %T", processor.telemetry)
	}
}

func TestBuildConversationHistoryProcessor_UsesDefaultsAndAgentName(t *testing.T) {
	config := DefaultConfig()
	config.Name = "travel"
	config.ConversationTokenBudget = 0
	config.ConversationRecentTurnsPreserved = 0

	processor, err := BuildConversationHistoryProcessor(config)
	if err != nil {
		t.Fatalf("BuildConversationHistoryProcessor() error = %v", err)
	}
	if processor.tokenBudget != DefaultConfig().ConversationTokenBudget {
		t.Fatalf("tokenBudget = %d, want %d", processor.tokenBudget, DefaultConfig().ConversationTokenBudget)
	}
	if processor.recentTurnsPreserved != DefaultConfig().ConversationRecentTurnsPreserved {
		t.Fatalf("recentTurnsPreserved = %d, want %d", processor.recentTurnsPreserved, DefaultConfig().ConversationRecentTurnsPreserved)
	}
	if processor.agentName != "travel" {
		t.Fatalf("agentName = %q, want travel", processor.agentName)
	}

	processor, err = BuildConversationHistoryProcessor(nil)
	if err != nil {
		t.Fatalf("BuildConversationHistoryProcessor(nil) error = %v", err)
	}
	if processor.tokenBudget != DefaultConfig().ConversationTokenBudget {
		t.Fatalf("nil config tokenBudget = %d, want %d", processor.tokenBudget, DefaultConfig().ConversationTokenBudget)
	}

	invalid := DefaultConfig()
	invalid.ConversationTokenBudget = minConversationTokenBudget - 1
	if _, err := BuildConversationHistoryProcessor(invalid); err == nil {
		t.Fatal("expected invalid config to fail through builder")
	}
}

func TestBuildCompactionEnabledConversationHistoryPreparer_ValidatesDependenciesAndDefaults(t *testing.T) {
	if _, err := BuildCompactionEnabledConversationHistoryPreparer(DefaultConfig(), nil); err == nil {
		t.Fatal("expected nil aiClient to fail")
	}

	config := DefaultConfig()
	config.ConversationSummaryCacheSize = 0
	preparer, err := BuildCompactionEnabledConversationHistoryPreparer(config, guideSnippetAIClient{})
	if err != nil {
		t.Fatalf("BuildCompactionEnabledConversationHistoryPreparer() error = %v", err)
	}
	if preparer.summaryCache == nil {
		t.Fatal("expected default summary cache to be installed")
	}

	preparer, err = BuildCompactionEnabledConversationHistoryPreparer(nil, guideSnippetAIClient{})
	if err != nil {
		t.Fatalf("BuildCompactionEnabledConversationHistoryPreparer(nil) error = %v", err)
	}
	if preparer.summaryCache == nil || preparer.compactor == nil {
		t.Fatal("expected nil-config builder to install defaults")
	}

	badCacheConfig := DefaultConfig()
	badCacheConfig.ConversationSummaryCacheSize = -1
	if _, err := BuildCompactionEnabledConversationHistoryPreparer(badCacheConfig, guideSnippetAIClient{}); err == nil {
		t.Fatal("expected invalid cache size to fail")
	}
}

func TestConversationHistoryProcessor_RecursiveCompactionLifecycle(t *testing.T) {
	cache, err := NewSummaryCache(4)
	if err != nil {
		t.Fatalf("NewSummaryCache() error = %v", err)
	}
	compactor := &recordingConversationCompactor{
		responses: []string{"summary-1", "summary-2", "summary-reset"},
	}
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 2,
	}, WithConversationSummaryCache(cache), WithConversationCompactor(compactor))
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}
	logger := &debugConversationLogger{}
	processor.SetLogger(logger)

	turns := []core.ConversationTurn{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "u3"},
	}

	prepared, result, err := processor.PrepareFromTurns(context.Background(), "sess", turns)
	if err != nil {
		t.Fatalf("PrepareFromTurns(first) error = %v", err)
	}
	if !result.Compacted || result.TurnsCompacted != 3 || result.TurnsKeptVerbatim != 2 {
		t.Fatalf("unexpected first result: %+v", result)
	}
	if !strings.Contains(prepared, "Conversation Summary:\nsummary-1") {
		t.Fatalf("expected prepared history to include summary-1, got %q", prepared)
	}
	if compactor.callCount != 1 || len(compactor.newTurnSets[0]) != 3 {
		t.Fatalf("unexpected first compactor calls: count=%d turns=%d", compactor.callCount, len(compactor.newTurnSets[0]))
	}

	emptyPrepared, emptyResult, err := processor.prepareFromTurnsWithPath(context.Background(), "sess-empty", nil, "metadata_turns")
	if err != nil || emptyPrepared != "" || emptyResult.Path != "metadata_turns" || emptyResult.TurnsRaw != 0 {
		t.Fatalf("prepareFromTurnsWithPath(empty) = %q %+v %v", emptyPrepared, emptyResult, err)
	}

	turns = append(turns, core.ConversationTurn{Role: "assistant", Content: "a3"})
	_, result, err = processor.PrepareFromTurns(context.Background(), "sess", turns)
	if err != nil {
		t.Fatalf("PrepareFromTurns(second) error = %v", err)
	}
	if !result.Compacted || result.TurnsCompacted != 4 {
		t.Fatalf("unexpected second result: %+v", result)
	}
	if compactor.callCount != 2 {
		t.Fatalf("expected incremental compaction call, got %d", compactor.callCount)
	}
	if compactor.priorSummaries[1] != "summary-1" {
		t.Fatalf("expected second call prior summary to be summary-1, got %q", compactor.priorSummaries[1])
	}
	if len(compactor.newTurnSets[1]) != 1 || compactor.newTurnSets[1][0].Content != "a2" {
		t.Fatalf("unexpected incremental compacted turns: %+v", compactor.newTurnSets[1])
	}

	_, result, err = processor.PrepareFromTurns(context.Background(), "sess", turns)
	if err != nil {
		t.Fatalf("PrepareFromTurns(third) error = %v", err)
	}
	if !result.Compacted || result.TurnsCompacted != 4 {
		t.Fatalf("unexpected cached result: %+v", result)
	}
	if compactor.callCount != 2 {
		t.Fatalf("expected cached summary reuse without new compaction, got %d calls", compactor.callCount)
	}

	mutated := cloneConversationTurns(turns)
	mutated[3].Content = "a2-mutated"
	_, result, err = processor.PrepareFromTurns(context.Background(), "sess", mutated)
	if err != nil {
		t.Fatalf("PrepareFromTurns(reset) error = %v", err)
	}
	if !result.Compacted || result.TurnsCompacted != 4 {
		t.Fatalf("unexpected reset result: %+v", result)
	}
	if compactor.callCount != 3 {
		t.Fatalf("expected watermark mismatch to trigger full recompute, got %d calls", compactor.callCount)
	}
	if len(logger.warnFields) == 0 {
		t.Fatal("expected watermark mismatch warning to be logged")
	}
	if got := logger.warnFields[len(logger.warnFields)-1]["error_type"]; got != "watermark_mismatch" {
		t.Fatalf("error_type = %v, want watermark_mismatch", got)
	}
}

func TestConversationHistoryProcessor_CompactorFailureFallsBackToTier1(t *testing.T) {
	cache, err := NewSummaryCache(2)
	if err != nil {
		t.Fatalf("NewSummaryCache() error = %v", err)
	}
	compactor := &recordingConversationCompactor{err: errors.New("boom")}
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 1,
	}, WithConversationSummaryCache(cache), WithConversationCompactor(compactor))
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}

	prepared, result, err := processor.PrepareFromTurns(context.Background(), "sess", []core.ConversationTurn{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	})
	if err != nil {
		t.Fatalf("PrepareFromTurns() error = %v", err)
	}
	if result.Compacted {
		t.Fatalf("expected compaction failure to fall back, got %+v", result)
	}
	if strings.Contains(prepared, "Conversation Summary:") {
		t.Fatalf("expected no summary after fail-open fallback, got %q", prepared)
	}
}

func TestConversationHistoryProcessor_Tier1BehaviorAndHelpers(t *testing.T) {
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 1,
	})
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}

	// Force simple length-based accounting so the drop/elide/truncate branches are deterministic.
	processor.tokenCounter = lengthTokenCounter{}
	processor.tokenBudget = 60
	kept, result := processor.applyTier1ToTurns(context.Background(), "", []core.ConversationTurn{
		{Role: "user", Content: strings.Repeat("A", 18)},
		{Role: "assistant", Content: strings.Repeat("B", 18)},
		{Role: "user", Content: strings.Repeat("C", 18)},
	}, HistoryPreparationResult{})
	if result.TurnsDropped == 0 || result.TurnsKeptVerbatim >= 3 {
		t.Fatalf("expected drop behavior, got %+v", result)
	}
	if !strings.Contains(kept, "Assistant:") || !strings.Contains(kept, "User:") {
		t.Fatalf("expected kept history to retain the newest turns, got %q", kept)
	}

	processor.recentTurnsPreserved = 2
	processor.tokenBudget = 180
	kept, result = processor.applyTier1ToTurns(context.Background(), "", []core.ConversationTurn{
		{Role: "user", Content: strings.Repeat("X", 120)},
		{Role: "assistant", Content: strings.Repeat("Y", 120)},
	}, HistoryPreparationResult{})
	if result.ElidedTurns == 0 || result.Truncated {
		t.Fatalf("expected elision without truncation, got %+v", result)
	}
	if !strings.Contains(kept, "\n...\n") {
		t.Fatalf("expected elided output marker, got %q", kept)
	}

	processor.tokenCounter = fixedTokenCounter{count: 9999}
	processor.tokenBudget = 10
	kept, result = processor.applyTier1ToTurns(context.Background(), "", []core.ConversationTurn{
		{Role: "user", Content: strings.Repeat("Z", 220)},
	}, HistoryPreparationResult{})
	if !result.Truncated {
		t.Fatalf("expected hard truncation, got %+v", result)
	}
	if !strings.HasPrefix(kept, "[conversation history truncated]\n") {
		t.Fatalf("expected truncation prefix, got %q", kept)
	}

	processor.tokenCounter = lengthTokenCounter{}
	processor.tokenBudget = 20
	text, textResult := processor.applyTier1ToText(context.Background(), strings.Repeat("q", 80), HistoryPreparationResult{})
	if !textResult.Truncated || !strings.HasPrefix(text, "[conversation history truncated]\n") {
		t.Fatalf("expected text truncation, got %q %+v", text, textResult)
	}

	if got, ok := elideMiddle(strings.Repeat("m", 40)); ok || got != strings.Repeat("m", 40) {
		t.Fatalf("expected short content to skip elision, got %q %v", got, ok)
	}
	if got, ok := elideMiddle(strings.Repeat("m", 140)); !ok || !strings.Contains(got, "\n...\n") {
		t.Fatalf("expected long content to elide, got %q %v", got, ok)
	}
	if got := hardTruncateText("", 10); got != "" {
		t.Fatalf("hardTruncateText(empty) = %q, want empty", got)
	}
	if got := hardTruncateText(strings.Repeat("n", 400), 0); !strings.HasPrefix(got, "[conversation history truncated]\n") {
		t.Fatalf("expected truncation prefix for zero budget, got %q", got)
	}
	if got := minConversationInt(2, 5); got != 2 {
		t.Fatalf("minConversationInt(2,5) = %d, want 2", got)
	}
}

func TestConversationHistoryProcessor_EstimateTokensAndPreparationLogging(t *testing.T) {
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 1,
	}, WithConversationTokenCounter(fixedTokenCounter{err: errors.New("counter failed")}))
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}
	logger := &debugConversationLogger{}
	tel := &mockTelemetry{}
	processor.SetLogger(logger)
	processor.SetTelemetry(tel)

	ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-1", "original_request_id", "root-1")
	tokens, counterName := processor.estimateTokens(ctx, "hello world")
	if counterName != "heuristic" || tokens == 0 {
		t.Fatalf("estimateTokens() = %d, %q", tokens, counterName)
	}
	if len(logger.debugFields) == 0 {
		t.Fatal("expected fallback debug log")
	}
	if got := logger.debugFields[0]["error_type"]; got != "count_tokens" {
		t.Fatalf("error_type = %v, want count_tokens", got)
	}

	processor.logPreparationOutcome(ctx, HistoryPreparationResult{Path: "hook"}, time.Now(), errors.New("prepare failed"))
	if len(logger.errorFields) == 0 {
		t.Fatal("expected preparation error log")
	}
	if got := logger.errorFields[0]["error_type"]; got != "preparation" {
		t.Fatalf("error_type = %v, want preparation", got)
	}

	processor.logger = nil
	processor.logPreparationOutcome(ctx, HistoryPreparationResult{}, time.Now(), nil)
}

func TestConversationHistoryProcessor_HelperFunctions(t *testing.T) {
	if got := tokenCounterName(nil); got != "heuristic" {
		t.Fatalf("tokenCounterName(nil) = %q, want heuristic", got)
	}
	if got := tokenCounterName(HeuristicTokenCounter{}); got != "heuristic" {
		t.Fatalf("tokenCounterName(heuristic) = %q, want heuristic", got)
	}
	if got := tokenCounterName(&namedConversationCounter{}); got != "namedConversationCounter" {
		t.Fatalf("tokenCounterName(named) = %q, want namedConversationCounter", got)
	}
	if got := tokenCounterName(fixedTokenCounter{}); got != "fixedTokenCounter" {
		t.Fatalf("tokenCounterName(fixed) = %q, want fixedTokenCounter", got)
	}

	cases := []struct {
		result HistoryPreparationResult
		want   string
	}{
		{HistoryPreparationResult{Truncated: true}, "truncated"},
		{HistoryPreparationResult{ElidedTurns: 1}, "elided"},
		{HistoryPreparationResult{Compacted: true}, "compacted"},
		{HistoryPreparationResult{TurnsDropped: 1}, "dropped"},
		{HistoryPreparationResult{EstimatedTokensPre: 0}, "empty"},
		{HistoryPreparationResult{EstimatedTokensPre: 1}, "pass_through"},
	}
	for _, tc := range cases {
		if got := outcomeFromResult(tc.result); got != tc.want {
			t.Fatalf("outcomeFromResult(%+v) = %q, want %q", tc.result, got, tc.want)
		}
	}

	turns := []core.ConversationTurn{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	}
	state := SummaryState{
		LastCompactedCount:  2,
		LastTurnFingerprint: fingerprintTurn(turns[1]),
	}
	if !watermarkMatches(state, turns) {
		t.Fatal("expected watermark to match")
	}
	if watermarkMatches(state, turns[:1]) {
		t.Fatal("expected short turn set to fail watermark match")
	}
	turns[1].Content = "changed"
	if watermarkMatches(state, turns) {
		t.Fatal("expected changed turn to fail watermark match")
	}
	if !watermarkMatches(SummaryState{}, nil) {
		t.Fatal("expected empty state to always match")
	}

	clone := cloneConversationTurns([]core.ConversationTurn{{Role: "user", Content: "hello"}})
	if len(clone) != 1 {
		t.Fatalf("cloneConversationTurns() length = %d, want 1", len(clone))
	}
	clone[0].Content = "changed"
	original := []core.ConversationTurn{{Role: "user", Content: "hello"}}
	cloned := cloneConversationTurns(original)
	cloned[0].Content = "changed"
	if original[0].Content != "hello" {
		t.Fatal("expected cloned turns to be independent")
	}
	if cloneConversationTurns(nil) != nil {
		t.Fatal("expected cloneConversationTurns(nil) to be nil")
	}

	formatted := formatConversationTurns([]core.ConversationTurn{
		{Role: "assistant", Content: "hello"},
		{Role: "tool", Content: "ran tool"},
		{Content: "default role"},
	})
	if !strings.Contains(formatted, "Assistant: hello") || !strings.Contains(formatted, "Tool: ran tool") || !strings.Contains(formatted, "User: default role") {
		t.Fatalf("unexpected formatted turns: %q", formatted)
	}
	summaryText := formatSummaryAndTurns("summary", []core.ConversationTurn{{Role: "user", Content: "hello"}})
	if !strings.Contains(summaryText, "Conversation Summary:\nsummary") || !strings.Contains(summaryText, "User: hello") {
		t.Fatalf("unexpected summary+turns formatting: %q", summaryText)
	}
	if got := formatConversationTurns(nil); got != "" {
		t.Fatalf("formatConversationTurns(nil) = %q, want empty", got)
	}
}

func TestConversationCompactor_SettersAndEdgeCases(t *testing.T) {
	if _, err := NewLLMConversationCompactor(nil, nil); err == nil {
		t.Fatal("expected nil AI client to fail")
	}

	compactor, err := NewLLMConversationCompactor(guideSnippetAIClient{}, nil)
	if err != nil {
		t.Fatalf("NewLLMConversationCompactor() error = %v", err)
	}

	componentLogger := &mockComponentAwareLogger{}
	compactor.SetLogger(componentLogger)
	if componentLogger.component != "framework/orchestration" {
		t.Fatalf("component logger = %q, want framework/orchestration", componentLogger.component)
	}
	compactor.SetLogger(nil)
	if _, ok := compactor.logger.(*core.NoOpLogger); !ok {
		t.Fatalf("expected nil logger to reset to NoOpLogger, got %T", compactor.logger)
	}

	compactor.SetTelemetry(nil)
	if _, ok := compactor.telemetry.(*core.NoOpTelemetry); !ok {
		t.Fatalf("expected nil telemetry to reset to NoOpTelemetry, got %T", compactor.telemetry)
	}

	summary, err := compactor.Compact(context.Background(), "prior", nil)
	if err != nil || summary != "prior" {
		t.Fatalf("Compact(empty) = %q, %v", summary, err)
	}
}

func TestConversationCompactor_DebugStoreFallbackRequestIDAndWarnOnStoreFailure(t *testing.T) {
	client := &conversationCompactorTestAIClient{
		response: &core.AIResponse{
			Content: "summary",
		},
	}
	compactor, err := NewLLMConversationCompactor(client, nil)
	if err != nil {
		t.Fatalf("NewLLMConversationCompactor() error = %v", err)
	}
	logger := &debugConversationLogger{}
	store := &failingConversationDebugStore{}
	compactor.SetLogger(logger)
	compactor.SetLLMDebugStore(store)

	_, err = compactor.Compact(context.Background(), "", []core.ConversationTurn{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if len(store.requestIDs) != 1 || store.requestIDs[0] == "" || !strings.HasPrefix(store.requestIDs[0], "conversation-compaction-") {
		t.Fatalf("unexpected fallback request IDs: %v", store.requestIDs)
	}
	if len(logger.warnFields) == 0 {
		t.Fatal("expected debug-store warning log")
	}
	if got := logger.warnFields[0]["error_type"]; got != "debug_recording" {
		t.Fatalf("error_type = %v, want debug_recording", got)
	}
}

func TestConversationHistoryHook_OptionsSettersAndFallbacks(t *testing.T) {
	if err := WithHistoryMaxTurns(0)(&ConversationHistoryHook{}); err == nil {
		t.Fatal("expected invalid maxTurns to fail")
	}
	if err := WithHistoryLogger(nil)(&ConversationHistoryHook{}); err == nil {
		t.Fatal("expected nil logger option to fail")
	}
	if err := WithConversationHistoryPreparer(nil)(&ConversationHistoryHook{}); err == nil {
		t.Fatal("expected nil preparer option to fail")
	}

	preparer := &capturePreparer{}
	hook, err := NewConversationHistoryHook(
		&core.MockConversationMemory{},
		"session-1",
		WithHistoryMaxTurns(7),
		WithHistoryLogger(&core.NoOpLogger{}),
		WithConversationHistoryPreparer(preparer),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	if hook.maxTurns != 7 {
		t.Fatalf("maxTurns = %d, want 7", hook.maxTurns)
	}

	componentLogger := &mockComponentAwareLogger{}
	hook.SetLogger(componentLogger)
	if componentLogger.component != "framework/orchestration" {
		t.Fatalf("component logger = %q, want framework/orchestration", componentLogger.component)
	}
	if preparer.logger == nil {
		t.Fatal("expected logger to propagate to preparer")
	}
	tel := &mockTelemetry{}
	hook.SetTelemetry(tel)
	if preparer.telemetry != tel {
		t.Fatal("expected telemetry to propagate to preparer")
	}
	store := &mockLLMDebugStore{}
	hook.SetLLMDebugStore(store)
	if preparer.debug != store {
		t.Fatal("expected debug store to propagate to preparer")
	}

	hook.SetLogger(nil)
	if _, ok := hook.logger.(*core.NoOpLogger); !ok {
		t.Fatalf("expected nil logger to reset to NoOpLogger, got %T", hook.logger)
	}

	memory := &core.MockConversationMemory{
		GetFullFn: func(ctx context.Context, sessionID string) ([]core.ConversationTurn, error) {
			return nil, nil
		},
		GetHistFn: func(ctx context.Context, sessionID string, maxTurns int) ([]core.ConversationTurn, error) {
			return []core.ConversationTurn{{Role: "user", Content: "fallback"}}, nil
		},
	}
	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 1,
	})
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}
	hook, err = NewConversationHistoryHook(memory, "session-2", WithConversationHistoryPreparer(processor))
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}

	pctx := &core.PipelineContext{}
	if _, err := hook.BeforePlanning(context.Background(), pctx); err != nil {
		t.Fatalf("BeforePlanning() error = %v", err)
	}
	if got := pctx.Enrichments[core.EnrichmentConversationHistory]; got == nil {
		t.Fatal("expected fallback GetHistory path to populate enrichments")
	}

	manualHook := &ConversationHistoryHook{}
	if shortCircuit, err := manualHook.BeforePlanning(context.Background(), &core.PipelineContext{}); err != nil || shortCircuit != nil {
		t.Fatalf("manual hook no-op = %v, %v", shortCircuit, err)
	}
}

func TestConversationHistoryHook_NonFullMemoryAndFailOpenBranches(t *testing.T) {
	pctx := &core.PipelineContext{}

	// Non-full-memory path uses PrepareFromText and respects maxTurns.
	textPreparer := &staticConversationPreparer{textPrepared: "prepared-text"}
	memory := &basicConversationMemory{
		history: []core.ConversationTurn{{Role: "user", Content: "hello"}},
	}
	hook, err := NewConversationHistoryHook(
		memory,
		"session-basic",
		WithHistoryMaxTurns(3),
		WithConversationHistoryPreparer(textPreparer),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	if _, err := hook.BeforePlanning(context.Background(), pctx); err != nil {
		t.Fatalf("BeforePlanning(basic) error = %v", err)
	}
	if got := pctx.Enrichments[core.EnrichmentConversationHistory]; got != "prepared-text" {
		t.Fatalf("prepared text history = %v, want prepared-text", got)
	}
	if memory.maxTurns != 3 {
		t.Fatalf("maxTurns = %d, want 3", memory.maxTurns)
	}

	// Non-full-memory path with path-aware preparer uses the hook path.
	pctx = &core.PipelineContext{}
	pathAwareProcessor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          minConversationTokenBudget,
		RecentTurnsPreserved: 1,
	})
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}
	hook, err = NewConversationHistoryHook(
		&basicConversationMemory{history: []core.ConversationTurn{{Role: "user", Content: "path-aware"}}},
		"session-path-aware",
		WithConversationHistoryPreparer(pathAwareProcessor),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	if _, err := hook.BeforePlanning(context.Background(), pctx); err != nil {
		t.Fatalf("BeforePlanning(path-aware basic) error = %v", err)
	}
	if got := pctx.Enrichments[core.EnrichmentConversationHistory]; got == nil {
		t.Fatal("expected path-aware basic-memory hook to inject history")
	}

	// Full-history error path logs and fails open.
	logger := &debugConversationLogger{}
	fullMemory := &core.MockConversationMemory{
		GetFullFn: func(context.Context, string) ([]core.ConversationTurn, error) {
			return nil, errors.New("full-history unavailable")
		},
	}
	hook, err = NewConversationHistoryHook(
		fullMemory,
		"session-full",
		WithConversationHistoryPreparer(&staticConversationPreparer{turnsPrepared: "ignored"}),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	hook.SetLogger(logger)
	if _, err := hook.BeforePlanning(context.Background(), &core.PipelineContext{}); err != nil {
		t.Fatalf("BeforePlanning(full error) error = %v", err)
	}
	if len(logger.warnFields) == 0 || logger.warnFields[len(logger.warnFields)-1]["error_type"] != "session_read" {
		t.Fatalf("expected session_read warning, got %+v", logger.warnFields)
	}

	// Full-memory path with non-path-aware preparer uses PrepareFromTurns.
	pctx = &core.PipelineContext{}
	fullMemory = &core.MockConversationMemory{
		GetFullFn: func(context.Context, string) ([]core.ConversationTurn, error) {
			return []core.ConversationTurn{{Role: "user", Content: "full"}}, nil
		},
	}
	hook, err = NewConversationHistoryHook(
		fullMemory,
		"session-turns",
		WithConversationHistoryPreparer(&staticConversationPreparer{turnsPrepared: "prepared-turns"}),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	if _, err := hook.BeforePlanning(context.Background(), pctx); err != nil {
		t.Fatalf("BeforePlanning(full turns) error = %v", err)
	}
	if got := pctx.Enrichments[core.EnrichmentConversationHistory]; got != "prepared-turns" {
		t.Fatalf("prepared turns history = %v, want prepared-turns", got)
	}

	// Full-memory fallback to GetHistory with non-path-aware preparer uses PrepareFromText.
	pctx = &core.PipelineContext{}
	fullMemory = &core.MockConversationMemory{
		GetFullFn: func(context.Context, string) ([]core.ConversationTurn, error) {
			return nil, nil
		},
		GetHistFn: func(context.Context, string, int) ([]core.ConversationTurn, error) {
			return []core.ConversationTurn{{Role: "user", Content: "from-history"}}, nil
		},
	}
	hook, err = NewConversationHistoryHook(
		fullMemory,
		"session-text-fallback",
		WithConversationHistoryPreparer(&staticConversationPreparer{textPrepared: "prepared-fallback-text"}),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	if _, err := hook.BeforePlanning(context.Background(), pctx); err != nil {
		t.Fatalf("BeforePlanning(full fallback text) error = %v", err)
	}
	if got := pctx.Enrichments[core.EnrichmentConversationHistory]; got != "prepared-fallback-text" {
		t.Fatalf("prepared fallback text history = %v, want prepared-fallback-text", got)
	}

	// Prepare error path logs and fails open.
	logger = &debugConversationLogger{}
	hook, err = NewConversationHistoryHook(
		&basicConversationMemory{history: []core.ConversationTurn{{Role: "user", Content: "bad"}}},
		"session-prep-error",
		WithConversationHistoryPreparer(&staticConversationPreparer{err: errors.New("prepare failed")}),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	hook.SetLogger(logger)
	if _, err := hook.BeforePlanning(context.Background(), &core.PipelineContext{}); err != nil {
		t.Fatalf("BeforePlanning(prep error) error = %v", err)
	}
	if len(logger.warnFields) == 0 || logger.warnFields[len(logger.warnFields)-1]["error_type"] != "preparation" {
		t.Fatalf("expected preparation warning, got %+v", logger.warnFields)
	}

	// Empty prepared output should fail open without mutating enrichments.
	pctx = &core.PipelineContext{Enrichments: map[string]interface{}{"existing": "value"}}
	hook, err = NewConversationHistoryHook(
		&basicConversationMemory{history: []core.ConversationTurn{{Role: "user", Content: "hello"}}},
		"session-empty",
		WithConversationHistoryPreparer(&staticConversationPreparer{}),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	if _, err := hook.BeforePlanning(context.Background(), pctx); err != nil {
		t.Fatalf("BeforePlanning(empty prepared) error = %v", err)
	}
	if _, ok := pctx.Enrichments[core.EnrichmentConversationHistory]; ok {
		t.Fatalf("expected empty prepared output to skip injection, got %+v", pctx.Enrichments)
	}

	// Basic-memory error and empty-history branches both fail open.
	logger = &debugConversationLogger{}
	hook, err = NewConversationHistoryHook(
		&basicConversationMemory{err: errors.New("history unavailable")},
		"session-history-error",
		WithConversationHistoryPreparer(&staticConversationPreparer{textPrepared: "ignored"}),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	hook.SetLogger(logger)
	if _, err := hook.BeforePlanning(context.Background(), &core.PipelineContext{}); err != nil {
		t.Fatalf("BeforePlanning(history error) error = %v", err)
	}
	if len(logger.warnFields) == 0 || logger.warnFields[len(logger.warnFields)-1]["error_type"] != "session_read" {
		t.Fatalf("expected history error warning, got %+v", logger.warnFields)
	}

	pctx = &core.PipelineContext{}
	hook, err = NewConversationHistoryHook(
		&basicConversationMemory{},
		"session-empty-history",
		WithConversationHistoryPreparer(&staticConversationPreparer{textPrepared: "ignored"}),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryHook() error = %v", err)
	}
	if _, err := hook.BeforePlanning(context.Background(), pctx); err != nil {
		t.Fatalf("BeforePlanning(empty history) error = %v", err)
	}
	if len(pctx.Enrichments) != 0 {
		t.Fatalf("expected empty history to keep enrichments empty, got %+v", pctx.Enrichments)
	}

	// Constructor wraps option failures.
	if _, err := NewConversationHistoryHook(
		&basicConversationMemory{},
		"session-invalid",
		func(*ConversationHistoryHook) error { return errors.New("bad option") },
	); err == nil || !strings.Contains(err.Error(), "invalid conversation history option") {
		t.Fatalf("expected wrapped option error, got %v", err)
	}
}

func TestConversationHistoryProcessor_DirectOptionAndHelperBranches(t *testing.T) {
	p := &ConversationHistoryProcessor{
		logger:     &core.NoOpLogger{},
		telemetry:  &core.NoOpTelemetry{},
		debugStore: &mockLLMDebugStore{},
	}
	compactor := &recordingConversationCompactor{}
	if err := WithConversationCompactor(compactor)(p); err != nil {
		t.Fatalf("WithConversationCompactor() error = %v", err)
	}
	if compactor.debugStore == nil || compactor.telemetry == nil || compactor.logger == nil {
		t.Fatal("expected option-time propagation to compactor")
	}

	if summary, compacted, ok := p.compactTurns(context.Background(), "prior", nil); !ok || summary != "prior" || compacted != 0 {
		t.Fatalf("compactTurns(empty) = %q, %d, %v", summary, compacted, ok)
	}
	blankCompactor := &recordingConversationCompactor{responses: []string{"   "}}
	p.compactor = blankCompactor
	if summary, compacted, ok := p.compactTurns(context.Background(), "", []core.ConversationTurn{{Role: "user", Content: "x"}}); ok || summary != "" || compacted != 0 {
		t.Fatalf("compactTurns(blank) = %q, %d, %v", summary, compacted, ok)
	}
}
