package orchestration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface compliance check.
var _ core.BeforePlanningHook = (*ConversationHistoryHook)(nil)

func newTestConversationHistoryPreparer(t *testing.T) ConversationHistoryPreparer {
	t.Helper()
	preparer, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          2000,
		RecentTurnsPreserved: 2,
	})
	require.NoError(t, err)
	return preparer
}

func TestConversationHistoryHook_Name(t *testing.T) {
	hook, err := NewConversationHistoryHook(
		nil,
		"",
		WithConversationHistoryPreparer(newTestConversationHistoryPreparer(t)),
	)
	require.NoError(t, err)
	assert.Equal(t, "conversation-history", hook.Name())
}

func TestConversationHistoryHook_RequiresPreparer(t *testing.T) {
	hook, err := NewConversationHistoryHook(&core.MockConversationMemory{}, "session-123")
	require.Error(t, err)
	assert.Nil(t, hook)
	assert.Contains(t, err.Error(), "conversation history preparer is required")
}

func TestConversationHistoryHook_InjectsHistory(t *testing.T) {
	memory := &core.MockConversationMemory{
		GetHistFn: func(ctx context.Context, sessionID string, maxTurns int) ([]core.ConversationTurn, error) {
			return []core.ConversationTurn{
				{Role: "user", Content: "I want to visit Tokyo"},
				{Role: "assistant", Content: "Great choice! When are you planning to go?"},
				{Role: "user", Content: "In March"},
			}, nil
		},
	}

	hook, err := NewConversationHistoryHook(
		memory,
		"session-123",
		WithConversationHistoryPreparer(newTestConversationHistoryPreparer(t)),
	)
	require.NoError(t, err)
	pctx := &core.PipelineContext{
		Request:     "What's the cheapest flight?",
		Enrichments: make(map[string]interface{}),
	}

	shortCircuit, err := hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)
	assert.Nil(t, shortCircuit, "should never short-circuit")

	history, ok := pctx.Enrichments[core.EnrichmentConversationHistory].(string)
	require.True(t, ok, "enrichments should contain conversation_history")
	assert.Contains(t, history, "User: I want to visit Tokyo")
	assert.Contains(t, history, "Assistant: Great choice!")
	assert.Contains(t, history, "User: In March")
	assert.Equal(t, 1, memory.GetHistCt)
}

func TestConversationHistoryHook_PrefersFullHistoryWhenAvailable(t *testing.T) {
	memory := &core.MockConversationMemory{
		GetHistFn: func(ctx context.Context, sessionID string, maxTurns int) ([]core.ConversationTurn, error) {
			t.Fatal("expected GetHistory not to be used when GetFullHistory is available")
			return nil, nil
		},
		GetFullFn: func(ctx context.Context, sessionID string) ([]core.ConversationTurn, error) {
			return []core.ConversationTurn{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "second"},
			}, nil
		},
	}

	hook, err := NewConversationHistoryHook(
		memory,
		"session-123",
		WithConversationHistoryPreparer(newTestConversationHistoryPreparer(t)),
	)
	require.NoError(t, err)
	pctx := &core.PipelineContext{Enrichments: make(map[string]interface{})}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	require.NoError(t, err)
	assert.Equal(t, 1, memory.GetFullCt)
	assert.Equal(t, 0, memory.GetHistCt)
}

func TestConversationHistoryHook_NoMemory(t *testing.T) {
	hook, err := NewConversationHistoryHook(
		nil,
		"session-123",
		WithConversationHistoryPreparer(newTestConversationHistoryPreparer(t)),
	)
	require.NoError(t, err)
	pctx := &core.PipelineContext{
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	require.NoError(t, err)
	_, hasHistory := pctx.Enrichments[core.EnrichmentConversationHistory]
	assert.False(t, hasHistory, "no memory = no history injected")
}

func TestConversationHistoryHook_EmptySession(t *testing.T) {
	hook, err := NewConversationHistoryHook(
		&core.MockConversationMemory{},
		"",
		WithConversationHistoryPreparer(newTestConversationHistoryPreparer(t)),
	)
	require.NoError(t, err)
	pctx := &core.PipelineContext{
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	require.NoError(t, err)
	_, hasHistory := pctx.Enrichments[core.EnrichmentConversationHistory]
	assert.False(t, hasHistory, "empty session ID = no history injected")
}

func TestConversationHistoryHook_EmptyHistory(t *testing.T) {
	memory := &core.MockConversationMemory{
		GetHistFn: func(ctx context.Context, sessionID string, maxTurns int) ([]core.ConversationTurn, error) {
			return nil, nil
		},
	}

	hook, err := NewConversationHistoryHook(
		memory,
		"session-123",
		WithConversationHistoryPreparer(newTestConversationHistoryPreparer(t)),
	)
	require.NoError(t, err)
	pctx := &core.PipelineContext{
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	require.NoError(t, err)
	_, hasHistory := pctx.Enrichments[core.EnrichmentConversationHistory]
	assert.False(t, hasHistory, "empty history = no tag injected")
}

func TestConversationHistoryHook_FailOpen(t *testing.T) {
	memory := &core.MockConversationMemory{
		GetHistFn: func(ctx context.Context, sessionID string, maxTurns int) ([]core.ConversationTurn, error) {
			return nil, assert.AnError
		},
	}

	hook, err := NewConversationHistoryHook(
		memory,
		"session-123",
		WithConversationHistoryPreparer(newTestConversationHistoryPreparer(t)),
	)
	require.NoError(t, err)
	pctx := &core.PipelineContext{
		Enrichments: make(map[string]interface{}),
	}

	shortCircuit, err := hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err, "memory errors should not propagate — fail-open")
	assert.Nil(t, shortCircuit)
}

func TestConversationHistoryHook_MaxTurns(t *testing.T) {
	memory := &core.MockConversationMemory{
		GetHistFn: func(ctx context.Context, sessionID string, maxTurns int) ([]core.ConversationTurn, error) {
			assert.Equal(t, 5, maxTurns, "should pass configured maxTurns")
			return []core.ConversationTurn{
				{Role: "user", Content: "Hello"},
			}, nil
		},
	}

	hook, err := NewConversationHistoryHook(
		memory,
		"session-123",
		WithHistoryMaxTurns(5),
		WithConversationHistoryPreparer(newTestConversationHistoryPreparer(t)),
	)
	require.NoError(t, err)
	pctx := &core.PipelineContext{
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	require.NoError(t, err)
	assert.Equal(t, 1, memory.GetHistCt)
}

func TestConversationHistoryHook_DoesNotOverrideExistingEnrichments(t *testing.T) {
	memory := &core.MockConversationMemory{
		GetHistFn: func(ctx context.Context, sessionID string, maxTurns int) ([]core.ConversationTurn, error) {
			return []core.ConversationTurn{
				{Role: "user", Content: "Hello"},
			}, nil
		},
	}

	hook, err := NewConversationHistoryHook(
		memory,
		"session-123",
		WithConversationHistoryPreparer(newTestConversationHistoryPreparer(t)),
	)
	require.NoError(t, err)
	pctx := &core.PipelineContext{
		Enrichments: map[string]interface{}{
			core.EnrichmentRAGContext: "existing RAG data",
		},
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	require.NoError(t, err)

	_, hasHistory := pctx.Enrichments[core.EnrichmentConversationHistory]
	assert.True(t, hasHistory)
	assert.Equal(t, "existing RAG data", pctx.Enrichments[core.EnrichmentRAGContext])
}
