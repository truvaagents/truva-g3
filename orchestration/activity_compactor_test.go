package orchestration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compactorMockAI implements core.AIClient for activity compactor tests.
type compactorMockAI struct {
	generateFunc func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error)
	calls        int
	lastPrompt   string
	lastOpts     *core.AIOptions
}

func (m *compactorMockAI) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.calls++
	m.lastPrompt = prompt
	m.lastOpts = opts
	if m.generateFunc != nil {
		return m.generateFunc(ctx, prompt, opts)
	}
	return &core.AIResponse{Content: "Compacted summary"}, nil
}

// --- Constructor Tests ---

func TestNewLLMActivityCompactor_NilAIClient(t *testing.T) {
	_, err := NewLLMActivityCompactor(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AI client is required")
}

func TestNewLLMActivityCompactor_Defaults(t *testing.T) {
	c, err := NewLLMActivityCompactor(&compactorMockAI{})
	require.NoError(t, err)
	assert.NotNil(t, c)
	assert.Equal(t, "", c.model)
	assert.NotNil(t, c.logger)
}

func TestNewLLMActivityCompactor_WithOptions(t *testing.T) {
	c, err := NewLLMActivityCompactor(&compactorMockAI{},
		WithActivityCompactorModel("gpt-4o-mini"),
		WithActivityCompactorLogger(&core.NoOpLogger{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o-mini", c.model)
}

func TestNewLLMActivityCompactor_InvalidOptions(t *testing.T) {
	tests := []struct {
		name string
		opt  LLMActivityCompactorOption
		err  string
	}{
		{"nil logger", WithActivityCompactorLogger(nil), "logger cannot be nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewLLMActivityCompactor(&compactorMockAI{}, tt.opt)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)
		})
	}
}

func TestNewLLMActivityCompactor_ComponentAwareLogger(t *testing.T) {
	// ProductionLogger implements ComponentAwareLogger
	pl := core.NewProductionLogger(core.LoggingConfig{Level: "info", Format: "json"}, core.DevelopmentConfig{}, "test-service")
	c, err := NewLLMActivityCompactor(&compactorMockAI{},
		WithActivityCompactorLogger(pl),
	)
	require.NoError(t, err)
	assert.NotNil(t, c.logger)
	// The logger should be wrapped with component context — different instance from pl
	assert.NotEqual(t, fmt.Sprintf("%p", pl), fmt.Sprintf("%p", c.logger))
}

// --- CompactEvents Tests ---

func TestCompactEvents_EmptyEvents(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)

	result, err := c.CompactEvents(context.Background(), nil, 500)
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Equal(t, 0, ai.calls, "should not make LLM call for empty events")
}

func TestCompactEvents_EmptySlice(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)

	result, err := c.CompactEvents(context.Background(), []core.AgentEvent{}, 500)
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Equal(t, 0, ai.calls)
}

func TestCompactEvents_SuccessfulCompaction(t *testing.T) {
	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: "Domain activity (last 1 hour, 3 events):\n- JIRA: ticket DEVOPS-49 created\n- Slack: #notifications notified",
				Usage:   core.TokenUsage{PromptTokens: 200, CompletionTokens: 50, TotalTokens: 250},
			}, nil
		},
	}
	c, _ := NewLLMActivityCompactor(ai)

	events := []core.AgentEvent{
		{AgentName: "devops-chat-agent", ActionType: "create_issue", EntityType: "service", EntityID: "product-catalog", Summary: "Created JIRA DEVOPS-49", Outcome: "success", Timestamp: time.Now()},
		{AgentName: "devops-chat-agent", ActionType: "send_message", EntityType: "service", EntityID: "product-catalog", Summary: "Sent Slack to #notifications", Outcome: "success", Timestamp: time.Now()},
		{AgentName: "devops-chat-agent", ActionType: "rollout_restart", EntityType: "deployment", EntityID: "product-catalog", Summary: "Restarted deployment", Outcome: "success", Timestamp: time.Now()},
	}

	result, err := c.CompactEvents(context.Background(), events, 500)
	require.NoError(t, err)
	assert.Contains(t, result, "DEVOPS-49")
	assert.Contains(t, result, "#notifications")
	assert.Equal(t, 1, ai.calls, "should make exactly 1 LLM call")
}

func TestCompactEvents_LLMError_ReturnsError(t *testing.T) {
	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	c, _ := NewLLMActivityCompactor(ai)

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	result, err := c.CompactEvents(context.Background(), events, 500)
	assert.Error(t, err, "should return error for caller to handle fallback")
	assert.Empty(t, result)
}

func TestCompactEvents_LLMEmptyResponse(t *testing.T) {
	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{Content: "   "}, nil
		},
	}
	c, _ := NewLLMActivityCompactor(ai)

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	result, err := c.CompactEvents(context.Background(), events, 500)
	require.NoError(t, err)
	assert.Empty(t, result, "whitespace-only response should be trimmed to empty")
}

func TestCompactEvents_PromptContainsEvents(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)

	events := []core.AgentEvent{
		{AgentName: "devops-chat-agent", ActionType: "create_issue", EntityType: "service", EntityID: "my-service", Summary: "Created ticket XYZ-123", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 500)
	assert.Contains(t, ai.lastPrompt, "<events>")
	assert.Contains(t, ai.lastPrompt, "</events>")
	assert.Contains(t, ai.lastPrompt, "XYZ-123")
	assert.Contains(t, ai.lastPrompt, "500 tokens")
}

func TestCompactEvents_SystemPromptSet(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 500)
	require.NotNil(t, ai.lastOpts)
	assert.Contains(t, ai.lastOpts.SystemPrompt, "<identity>")
	assert.Contains(t, ai.lastOpts.SystemPrompt, "<instructions>")
	assert.Contains(t, ai.lastOpts.SystemPrompt, "<example>")
}

func TestCompactEvents_ModelOverride(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai, WithActivityCompactorModel("fast"))

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 500)
	require.NotNil(t, ai.lastOpts)
	assert.Equal(t, "fast", ai.lastOpts.Model)
}

func TestCompactEvents_NoModelOverride(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 500)
	require.NotNil(t, ai.lastOpts)
	assert.Empty(t, ai.lastOpts.Model, "should not set model when empty")
}

func TestCompactEvents_MaxTokensScalesWithInput(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 300)
	require.NotNil(t, ai.lastOpts)
	assert.Equal(t, 1500, ai.lastOpts.MaxTokens, "should be maxTokens * 5 (headroom for clean output)")
}

func TestCompactEvents_TrimWhitespace(t *testing.T) {
	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{Content: "\n  Summary with whitespace  \n"}, nil
		},
	}
	c, _ := NewLLMActivityCompactor(ai)

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	result, err := c.CompactEvents(context.Background(), events, 500)
	require.NoError(t, err)
	assert.Equal(t, "Summary with whitespace", result)
}

func TestCompactEvents_Temperature(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 500)
	require.NotNil(t, ai.lastOpts)
	assert.InDelta(t, 0.1, ai.lastOpts.Temperature, 0.01)
}

// --- Debug Store Tests ---

func TestCompactEvents_DebugStoreRecordsSuccess(t *testing.T) {
	recorded := make(chan LLMInteraction, 1)
	mockStore := &compactorMockDebugStore{
		recordFunc: func(ctx context.Context, requestID string, interaction LLMInteraction) error {
			recorded <- interaction
			return nil
		},
	}

	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{Content: "digest", Usage: core.TokenUsage{TotalTokens: 100}}, nil
		},
	}
	c, _ := NewLLMActivityCompactor(ai)
	c.SetLLMDebugStore(mockStore)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, c.Shutdown(ctx))
	})

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 500)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, c.Shutdown(ctx))
	select {
	case interaction := <-recorded:
		assert.Equal(t, "activity_compaction", interaction.Type)
		assert.True(t, interaction.Success)
	case <-ctx.Done():
		t.Fatal("timed out waiting for debug interaction")
	}
}

func TestCompactEvents_DebugStoreRecordsFailure(t *testing.T) {
	recorded := make(chan LLMInteraction, 1)
	mockStore := &compactorMockDebugStore{
		recordFunc: func(ctx context.Context, requestID string, interaction LLMInteraction) error {
			recorded <- interaction
			return nil
		},
	}

	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return nil, fmt.Errorf("fail")
		},
	}
	c, _ := NewLLMActivityCompactor(ai)
	c.SetLLMDebugStore(mockStore)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, c.Shutdown(ctx))
	})

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 500)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, c.Shutdown(ctx))
	select {
	case interaction := <-recorded:
		assert.False(t, interaction.Success)
	case <-ctx.Done():
		t.Fatal("timed out waiting for debug interaction")
	}
}

func TestCompactEvents_NoDebugStore_NoPanic(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)
	// debugStore is nil — should not panic

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	result, err := c.CompactEvents(context.Background(), events, 500)
	require.NoError(t, err)
	assert.NotEmpty(t, result)
}

// --- Shutdown Tests ---

func TestShutdown_WaitsForDebugRecording(t *testing.T) {
	recorded := make(chan bool, 1)
	mockStore := &compactorMockDebugStore{
		recordFunc: func(ctx context.Context, requestID string, interaction LLMInteraction) error {
			time.Sleep(50 * time.Millisecond)
			recorded <- true
			return nil
		},
	}

	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)
	c.SetLLMDebugStore(mockStore)

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 500)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.Shutdown(ctx)
	assert.NoError(t, err)
	assert.Len(t, recorded, 1, "debug recording should complete before shutdown returns")
}

func TestShutdown_RespectsContextTimeout(t *testing.T) {
	mockStore := &compactorMockDebugStore{
		recordFunc: func(ctx context.Context, requestID string, interaction LLMInteraction) error {
			time.Sleep(5 * time.Second) // Slow recording
			return nil
		},
	}

	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)
	c.SetLLMDebugStore(mockStore)

	events := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	_, _ = c.CompactEvents(context.Background(), events, 500)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := c.Shutdown(ctx)
	assert.Error(t, err, "should timeout")
}

// --- SetTelemetry / SetLLMDebugStore Tests ---

func TestSetTelemetry(t *testing.T) {
	c, _ := NewLLMActivityCompactor(&compactorMockAI{})
	assert.Nil(t, c.telemetry)

	mockTelemetry := &core.NoOpTelemetry{}
	c.SetTelemetry(mockTelemetry)
	assert.Equal(t, mockTelemetry, c.telemetry)
}

func TestSetLLMDebugStore(t *testing.T) {
	c, _ := NewLLMActivityCompactor(&compactorMockAI{})
	assert.Nil(t, c.debugStore)

	mockStore := &compactorMockDebugStore{}
	c.SetLLMDebugStore(mockStore)
	assert.Equal(t, mockStore, c.debugStore)
}

// --- MemoryEnrichmentHook Integration Tests ---

func TestMemoryEnrichmentHook_WithActivityCompactor(t *testing.T) {
	compactor := &core.MockActivityCompactor{
		CompactEventsFn: func(ctx context.Context, events []core.AgentEvent, maxTokens int) (string, error) {
			return "Compacted: 3 events summarized", nil
		},
	}

	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			return []core.AgentEvent{
				{EventID: "e1", AgentName: "agent", ActionType: "create_issue", EntityType: "service", EntityID: "my-svc", Summary: "Created JIRA DEVOPS-99", Outcome: "success", Timestamp: time.Now()},
				{EventID: "e2", AgentName: "agent", ActionType: "send_message", EntityType: "service", EntityID: "my-svc", Summary: "Sent Slack to #alerts", Outcome: "success", Timestamp: time.Now()},
				{EventID: "e3", AgentName: "agent", ActionType: "get_pods", EntityType: "pod", EntityID: "my-pod", Summary: "Listed pods", Outcome: "success", Timestamp: time.Now()},
			}, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test", "domain",
		WithActivityCompactor(compactor),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "test query",
		Enrichments: make(map[string]interface{}),
	}
	_, err = hook.BeforePlanning(context.Background(), pctx)
	require.NoError(t, err)

	assert.Equal(t, 1, compactor.CompactEventsCt, "compactor should be called")
	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	// Should contain both the digest AND recent raw events
	assert.Contains(t, ragCtx, "Compacted: 3 events summarized", "should have compacted digest")
	assert.Contains(t, ragCtx, "Most recent events (detail):", "should have recent raw events")
	assert.Contains(t, ragCtx, "DEVOPS-99", "recent raw events should preserve identifiers")
}

func TestMemoryEnrichmentHook_CompactorFailsFallsBackToRaw(t *testing.T) {
	compactor := &core.MockActivityCompactor{
		CompactEventsFn: func(ctx context.Context, events []core.AgentEvent, maxTokens int) (string, error) {
			return "", fmt.Errorf("LLM unavailable")
		},
	}

	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			return []core.AgentEvent{
				{EventID: "e1", AgentName: "agent", ActionType: "get_pods", EntityType: "pod", EntityID: "my-pod", Summary: "got pods", Outcome: "success", Timestamp: time.Now()},
			}, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test", "domain",
		WithActivityCompactor(compactor),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "test query",
		Enrichments: make(map[string]interface{}),
	}
	_, err = hook.BeforePlanning(context.Background(), pctx)
	require.NoError(t, err)

	// Should fall back to raw events
	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "Recent activity in this domain:")
}

func TestMemoryEnrichmentHook_NilCompactorUsesRaw(t *testing.T) {
	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			return []core.AgentEvent{
				{EventID: "e1", AgentName: "agent", ActionType: "get_pods", EntityType: "pod", EntityID: "my-pod", Summary: "got pods", Outcome: "success", Timestamp: time.Now()},
			}, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test", "domain")
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "test query",
		Enrichments: make(map[string]interface{}),
	}
	_, err = hook.BeforePlanning(context.Background(), pctx)
	require.NoError(t, err)

	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "Recent activity in this domain:")
}

func TestMemoryEnrichmentHook_CompactorQueryLimit(t *testing.T) {
	var queriedLimit int
	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			queriedLimit = limit
			return []core.AgentEvent{}, nil
		},
	}

	// With compactor — should use compactionRawLimit (200)
	hook, _ := NewMemoryEnrichmentHook(episodic, nil, "test", "domain",
		WithActivityCompactor(&core.MockActivityCompactor{}),
	)
	pctx := &core.PipelineContext{Request: "test", Enrichments: make(map[string]interface{})}
	hook.BeforePlanning(context.Background(), pctx)
	assert.Equal(t, 200, queriedLimit, "should use compactionRawLimit when compactor is set")

	// Without compactor — should use recentEventsLimit (20)
	hook2, _ := NewMemoryEnrichmentHook(episodic, nil, "test", "domain")
	pctx2 := &core.PipelineContext{Request: "test", Enrichments: make(map[string]interface{})}
	hook2.BeforePlanning(context.Background(), pctx2)
	assert.Equal(t, 20, queriedLimit, "should use recentEventsLimit when no compactor")
}

func TestWithCompactionMaxTokens(t *testing.T) {
	hook, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithCompactionMaxTokens(1000),
	)
	require.NoError(t, err)
	assert.Equal(t, 1000, hook.compactionMaxTokens)
}

func TestWithCompactionMaxTokens_Invalid(t *testing.T) {
	_, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithCompactionMaxTokens(0),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compactionMaxTokens must be positive")
}

func TestWithCompactionRawLimit(t *testing.T) {
	hook, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithCompactionRawLimit(500),
	)
	require.NoError(t, err)
	assert.Equal(t, 500, hook.compactionRawLimit)
}

func TestWithCompactionRawLimit_Invalid(t *testing.T) {
	_, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithCompactionRawLimit(-1),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compactionRawLimit must be positive")
}

func TestWithActivityCompactor_AcceptsNil(t *testing.T) {
	hook, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithActivityCompactor(nil),
	)
	require.NoError(t, err)
	assert.Nil(t, hook.compactor)
}

// --- Propagation Tests ---

func TestMemoryEnrichmentHook_SetTelemetryPropagates(t *testing.T) {
	compactor, _ := NewLLMActivityCompactor(&compactorMockAI{})
	hook, _ := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithActivityCompactor(compactor),
	)

	assert.Nil(t, compactor.telemetry)
	hook.SetTelemetry(&core.NoOpTelemetry{})
	assert.NotNil(t, compactor.telemetry)
}

func TestMemoryEnrichmentHook_SetLLMDebugStorePropagates(t *testing.T) {
	compactor, _ := NewLLMActivityCompactor(&compactorMockAI{})
	hook, _ := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithActivityCompactor(compactor),
	)

	assert.Nil(t, compactor.debugStore)
	hook.SetLLMDebugStore(&compactorMockDebugStore{})
	assert.NotNil(t, compactor.debugStore)
}

// --- Fallback Request ID ---

func TestGenerateFallbackRequestID(t *testing.T) {
	c, _ := NewLLMActivityCompactor(&compactorMockAI{})
	id1 := c.generateFallbackRequestID()
	id2 := c.generateFallbackRequestID()
	assert.True(t, strings.HasPrefix(id1, "compactor-"))
	assert.NotEqual(t, id1, id2, "should generate unique IDs")
}

// --- compactorMockDebugStore for tests (avoids redeclaration with other test files) ---

type compactorMockDebugStore struct {
	recordFunc func(ctx context.Context, requestID string, interaction LLMInteraction) error
}

func (m *compactorMockDebugStore) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	if m.recordFunc != nil {
		return m.recordFunc(ctx, requestID, interaction)
	}
	return nil
}

func (m *compactorMockDebugStore) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	return nil, nil
}

func (m *compactorMockDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	return nil
}

func (m *compactorMockDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	return nil
}

func (m *compactorMockDebugStore) ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}

// --- UpdateDigest Tests ---

func TestUpdateDigest_EmptyNewEvents(t *testing.T) {
	ai := &compactorMockAI{}
	c, _ := NewLLMActivityCompactor(ai)

	result, err := c.UpdateDigest(context.Background(), "existing digest", nil, 500)
	require.NoError(t, err)
	assert.Equal(t, "existing digest", result)
	assert.Equal(t, 0, ai.calls, "should not make LLM call for empty events")
}

func TestUpdateDigest_Success(t *testing.T) {
	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			assert.Contains(t, prompt, "<previous_digest>")
			assert.Contains(t, prompt, "existing digest")
			assert.Contains(t, prompt, "<new_events>")
			assert.Contains(t, prompt, "new action happened")
			return &core.AIResponse{
				Content: "Updated digest with new action",
				Usage:   core.TokenUsage{TotalTokens: 100},
			}, nil
		},
	}
	c, _ := NewLLMActivityCompactor(ai)

	newEvents := []core.AgentEvent{
		{AgentName: "agent", ActionType: "create_issue", Summary: "new action happened", Outcome: "success", Timestamp: time.Now()},
	}

	result, err := c.UpdateDigest(context.Background(), "existing digest", newEvents, 500)
	require.NoError(t, err)
	assert.Equal(t, "Updated digest with new action", result)
	assert.Equal(t, 1, ai.calls)
}

func TestUpdateDigest_LLMError_ReturnsPrevious(t *testing.T) {
	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return nil, fmt.Errorf("LLM unavailable")
		},
	}
	c, _ := NewLLMActivityCompactor(ai)

	newEvents := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	result, err := c.UpdateDigest(context.Background(), "previous digest", newEvents, 500)
	assert.Error(t, err)
	assert.Equal(t, "previous digest", result, "should return previous digest on error")
}

func TestUpdateDigest_PromptStructure(t *testing.T) {
	var capturedPrompt string
	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			capturedPrompt = prompt
			return &core.AIResponse{Content: "updated"}, nil
		},
	}
	c, _ := NewLLMActivityCompactor(ai)

	newEvents := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Summary: "test event", Outcome: "success", Timestamp: time.Now()},
	}

	c.UpdateDigest(context.Background(), "old digest content", newEvents, 300)

	assert.Contains(t, capturedPrompt, "<previous_digest>")
	assert.Contains(t, capturedPrompt, "old digest content")
	assert.Contains(t, capturedPrompt, "</previous_digest>")
	assert.Contains(t, capturedPrompt, "<new_events>")
	assert.Contains(t, capturedPrompt, "</new_events>")
	assert.Contains(t, capturedPrompt, "300 tokens")
}

func TestUpdateDigest_TrimWhitespace(t *testing.T) {
	ai := &compactorMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{Content: "\n  Updated content  \n"}, nil
		},
	}
	c, _ := NewLLMActivityCompactor(ai)

	newEvents := []core.AgentEvent{
		{AgentName: "agent", ActionType: "action", Outcome: "success", Timestamp: time.Now()},
	}

	result, _ := c.UpdateDigest(context.Background(), "old", newEvents, 500)
	assert.Equal(t, "Updated content", result)
}
