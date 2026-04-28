package memory

import (
	"context"
	"fmt"
	"testing"

	"time"

	"github.com/truvaagents/truva-g3/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface compliance check.
var _ core.MemoryReflector = (*LLMMemoryReflector)(nil)

// --- Mock AIClient for reflector tests ---

type mockReflectorAIClient struct {
	response        string
	err             error
	lastModel       string // captured for assertions about WithReflectorModel
	lastTemperature float32
	lastMaxTokens   int
	callCount       int
}

func (m *mockReflectorAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	m.callCount++
	if options != nil {
		m.lastModel = options.Model
		m.lastTemperature = options.Temperature
		m.lastMaxTokens = options.MaxTokens
	}
	if m.err != nil {
		return nil, m.err
	}
	return &core.AIResponse{Content: m.response}, nil
}

// --- Constructor Tests ---

func TestNewLLMMemoryReflector_FailFast(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)

	t.Run("nil aiClient rejected", func(t *testing.T) {
		_, err := NewLLMMemoryReflector(nil, episodic, "default", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "aiClient is required")
	})

	t.Run("nil episodic rejected", func(t *testing.T) {
		_, err := NewLLMMemoryReflector(&mockReflectorAIClient{}, nil, "default", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "episodic memory is required")
	})

	t.Run("valid construction", func(t *testing.T) {
		r, err := NewLLMMemoryReflector(&mockReflectorAIClient{}, episodic, "infrastructure", nil)
		require.NoError(t, err)
		assert.NotNil(t, r)
	})

	t.Run("empty domain defaults to default", func(t *testing.T) {
		r, err := NewLLMMemoryReflector(&mockReflectorAIClient{}, episodic, "", nil)
		require.NoError(t, err)
		assert.Equal(t, "default", r.domain)
	})
}

// --- Reflect Tests ---

func TestReflect_InsufficientEvents(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	// Add only 2 events (below default minEvents=5)
	for i := 0; i < 2; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
			Outcome: "success", Summary: "test event",
		})
	}

	aiClient := &mockReflectorAIClient{response: `[{"content": "should not be called", "namespace": "patterns", "importance": 5.0}]`}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	fragments, err := r.Reflect(ctx, "pod", "pod-1", time.Time{})
	assert.NoError(t, err)
	assert.Nil(t, fragments, "should return nil for insufficient events")
}

func TestReflect_ExtractsKnowledge(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	// Add enough events
	for i := 0; i < 6; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "pod_restart",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
			Outcome: "success", Summary: fmt.Sprintf("Restarted pod due to OOMKilled (attempt %d)", i+1),
			Importance: 7.0,
		})
	}

	aiClient := &mockReflectorAIClient{
		response: `[{"content": "Pod pod-1 consistently OOMKills. Root cause is likely memory limit too low.", "namespace": "incidents", "importance": 8.0}]`,
	}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	fragments, err := r.Reflect(ctx, "pod", "pod-1", time.Time{})
	require.NoError(t, err)
	require.Len(t, fragments, 1)
	assert.Contains(t, fragments[0].Content, "OOMKills")
	assert.Equal(t, "incidents", fragments[0].Namespace)
	assert.Equal(t, 8.0, fragments[0].Importance)
	assert.Equal(t, core.ScopeSharedDomain, fragments[0].Scope)
	assert.Equal(t, "default", fragments[0].AgentDomain)
	assert.Nil(t, fragments[0].Embedding, "Embedding should be nil — caller handles embedding")
	assert.NotEmpty(t, fragments[0].SourceEvents, "Should include source event IDs")
}

func TestReflect_LLMReturnsEmptyArray(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "health_check",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
			Outcome: "success", Summary: "Routine health check",
		})
	}

	aiClient := &mockReflectorAIClient{response: `[]`}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	fragments, err := r.Reflect(ctx, "pod", "pod-1", time.Time{})
	assert.NoError(t, err)
	assert.Empty(t, fragments, "LLM found no patterns — empty is correct")
}

func TestReflect_FailOpen_LLMError(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
		})
	}

	aiClient := &mockReflectorAIClient{err: fmt.Errorf("LLM unavailable")}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	fragments, err := r.Reflect(ctx, "pod", "pod-1", time.Time{})
	assert.NoError(t, err, "should not propagate LLM errors — fail-open")
	assert.Nil(t, fragments)
}

func TestReflect_FailOpen_EpisodicError(t *testing.T) {
	episodic := &core.MockEpisodicMemory{
		QueryEntityHistFn: func(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]core.AgentEvent, error) {
			return nil, fmt.Errorf("Redis unavailable")
		},
	}

	aiClient := &mockReflectorAIClient{}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	fragments, err := r.Reflect(context.Background(), "pod", "pod-1", time.Time{})
	assert.NoError(t, err, "should not propagate episodic errors — fail-open")
	assert.Nil(t, fragments)
}

func TestReflect_CustomMinEvents(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	// Add 3 events
	for i := 0; i < 3; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
			Outcome: "success",
		})
	}

	aiClient := &mockReflectorAIClient{response: `[{"content": "pattern found", "namespace": "patterns", "importance": 5.0}]`}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil, WithReflectorMinEvents(3))

	fragments, err := r.Reflect(ctx, "pod", "pod-1", time.Time{})
	require.NoError(t, err)
	require.Len(t, fragments, 1, "minEvents=3, 3 events present — should reflect")
}

func TestReflect_LLMResponseWithProse(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
		})
	}

	// LLM wraps JSON in prose
	aiClient := &mockReflectorAIClient{
		response: `Here are the patterns I found: [{"content": "test pattern", "namespace": "patterns", "importance": 6.0}] Hope this helps!`,
	}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	fragments, err := r.Reflect(ctx, "pod", "pod-1", time.Time{})
	require.NoError(t, err)
	require.Len(t, fragments, 1, "should extract JSON from prose-wrapped response")
	assert.Equal(t, "test pattern", fragments[0].Content)
}

// --- Model option tests ---

// TestReflect_DefaultModel_PassesEmptyToAIClient proves that without
// WithReflectorModel the reflector lets the AIClient pick its default
// (i.e. AIOptions.Model is empty, which the chain client interprets as
// "use each provider's default alias").
func TestReflect_DefaultModel_PassesEmptyToAIClient(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
		}))
	}

	aiClient := &mockReflectorAIClient{response: `[]`}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	_, err := r.Reflect(ctx, "pod", "pod-1", time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 1, aiClient.callCount, "LLM should be called once")
	assert.Equal(t, "", aiClient.lastModel,
		"AIOptions.Model must be empty when no override is set so chain default kicks in")
}

// TestReflect_WithModelAlias_PropagatesToAIClient proves WithReflectorModel("fast")
// causes Reflect() to set AIOptions.Model="fast", which the chain client resolves
// per-provider (e.g. "fast" → claude-haiku on Anthropic).
func TestReflect_WithModelAlias_PropagatesToAIClient(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
		}))
	}

	aiClient := &mockReflectorAIClient{response: `[]`}
	r, err := NewLLMMemoryReflector(aiClient, episodic, "default", nil, WithReflectorModel("fast"))
	require.NoError(t, err)
	assert.Equal(t, "fast", r.model)

	_, err = r.Reflect(ctx, "pod", "pod-1", time.Time{})
	require.NoError(t, err)
	assert.Equal(t, "fast", aiClient.lastModel,
		"WithReflectorModel value must reach AIOptions.Model on the actual LLM call")
}

// TestReflect_WithConcreteModelName_PropagatesVerbatim ensures that callers who pass
// a concrete model name (e.g. provider-specific) get verbatim pass-through, not alias
// resolution at the reflector layer.
func TestReflect_WithConcreteModelName_PropagatesVerbatim(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
		}))
	}

	aiClient := &mockReflectorAIClient{response: `[]`}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil,
		WithReflectorModel("claude-haiku-4-5-20251001"),
	)

	_, _ = r.Reflect(ctx, "pod", "pod-1", time.Time{})
	assert.Equal(t, "claude-haiku-4-5-20251001", aiClient.lastModel)
}

// TestCompact_UsesSameModelAsReflect proves Compact() honors the same WithReflectorModel
// setting — both LLM call sites must agree, otherwise operators get a confusing split
// where reflection runs on Haiku but compaction silently runs on Sonnet.
func TestCompact_UsesSameModelAsReflect(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	// 5 old events so Compact has something to do
	for i := 0; i < 5; i++ {
		require.NoError(t, episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
			Outcome: "success", Summary: "old",
			Timestamp:  time.Now().Add(-10 * 24 * time.Hour),
			Importance: 5.0,
		}))
	}

	aiClient := &mockReflectorAIClient{response: "summary digest"}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil, WithReflectorModel("fast"))

	err := r.Compact(ctx, core.CompactionConfig{
		EventAgeThreshold: 7 * 24 * time.Hour,
		DigestWindow:      24 * time.Hour,
	})
	require.NoError(t, err)
	assert.Equal(t, "fast", aiClient.lastModel,
		"Compact() must use the same model setting as Reflect()")
}

// --- Compact Tests ---

func TestCompact_DryRun(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	// Add old events
	for i := 0; i < 5; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
			Outcome: "success", Summary: "old event",
			Timestamp:  time.Now().Add(-10 * 24 * time.Hour), // 10 days old
			Importance: 5.0,
		})
	}

	aiClient := &mockReflectorAIClient{response: "Digest summary"}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	err := r.Compact(ctx, core.CompactionConfig{
		EventAgeThreshold: 7 * 24 * time.Hour,
		DryRun:            true,
	})
	assert.NoError(t, err)

	// Dry run should NOT delete events
	events, _ := episodic.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})
	assert.Len(t, events, 5, "dry run should not modify events")
}

func TestCompact_DigestsOldEvents(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	// Add 4 old events for one entity
	for i := 0; i < 4; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "pod_restart",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
			Outcome: "success", Summary: fmt.Sprintf("restart %d", i+1),
			Timestamp:  time.Now().Add(-10 * 24 * time.Hour),
			Importance: 7.0,
		})
	}

	aiClient := &mockReflectorAIClient{response: "Pod pod-1 was restarted 4 times due to recurring issues."}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	err := r.Compact(ctx, core.CompactionConfig{
		EventAgeThreshold: 7 * 24 * time.Hour,
	})
	assert.NoError(t, err)

	// Original events should be deleted, digest event should be created
	events, _ := episodic.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})

	digestFound := false
	for _, e := range events {
		if e.ActionType == "digest" {
			digestFound = true
			assert.Contains(t, e.Summary, "restarted 4 times")
			assert.Equal(t, "pod", e.EntityType)
			assert.Equal(t, "pod-1", e.EntityID)
		}
	}
	assert.True(t, digestFound, "should create a digest event")
}

func TestCompact_SkipsDigestEvents(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	// Add a digest event (should not be re-compacted)
	episodic.RecordEvent(ctx, core.AgentEvent{
		AgentName: "compaction", AgentDomain: "default", ActionType: "digest",
		EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
		Outcome: "success", Summary: "Previous digest",
		Timestamp: time.Now().Add(-10 * 24 * time.Hour),
	})

	aiClient := &mockReflectorAIClient{}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	err := r.Compact(ctx, core.CompactionConfig{
		EventAgeThreshold: 7 * 24 * time.Hour,
	})
	assert.NoError(t, err)

	// Digest event should still exist (not re-compacted)
	events, _ := episodic.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})
	assert.Len(t, events, 1)
	assert.Equal(t, "digest", events[0].ActionType)
}

func TestCompact_NoOldEvents(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	// Add recent events (not old enough for compaction)
	for i := 0; i < 5; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
			Timestamp: time.Now(), // Recent
		})
	}

	aiClient := &mockReflectorAIClient{}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	err := r.Compact(ctx, core.CompactionConfig{
		EventAgeThreshold: 7 * 24 * time.Hour,
	})
	assert.NoError(t, err)

	// All events should remain (too recent)
	events, _ := episodic.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})
	assert.Len(t, events, 5)
}

func TestCompact_FailOpen_LLMError(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("default", 100)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
			Timestamp: time.Now().Add(-10 * 24 * time.Hour),
		})
	}

	aiClient := &mockReflectorAIClient{err: fmt.Errorf("LLM unavailable")}
	r, _ := NewLLMMemoryReflector(aiClient, episodic, "default", nil)

	err := r.Compact(ctx, core.CompactionConfig{EventAgeThreshold: 7 * 24 * time.Hour})
	assert.NoError(t, err, "LLM failure should not propagate — fail-open")

	// Events should still exist (not deleted without a digest)
	events, _ := episodic.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})
	assert.Len(t, events, 3)
}

// --- Helper Tests ---

func TestExtractEventIDs(t *testing.T) {
	events := []core.AgentEvent{
		{EventID: "c"}, {EventID: "a"}, {EventID: "b"}, {EventID: "a"}, {EventID: ""},
	}
	ids := extractEventIDs(events)
	assert.Equal(t, []string{"a", "b", "c"}, ids, "should be sorted and deduped, empty excluded")
}

func TestFormatEventsForReflection(t *testing.T) {
	events := []core.AgentEvent{
		{
			Timestamp:  time.Date(2026, 3, 14, 4, 49, 0, 0, time.UTC),
			AgentName:  "event-driven-agent",
			ActionType: "pod_restart",
			EntityType: "pod",
			EntityID:   "pod-1",
			Summary:    "Restarted due to OOMKilled",
			Outcome:    "success",
			Importance: 7.0,
		},
	}
	result := formatEventsForReflection(events)
	assert.Contains(t, result, "event-driven-agent")
	assert.Contains(t, result, "pod_restart")
	assert.Contains(t, result, "OOMKilled")
}
