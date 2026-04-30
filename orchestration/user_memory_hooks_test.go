package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// ─── Enrichment Hook Tests ───────────────────────────────────────────────────

func TestUserMemoryEnrichmentHook_SkipsWithoutUserID(t *testing.T) {
	mem := newTestUserMemory()
	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{})

	pctx := &core.PipelineContext{
		Request:  "Plan a trip",
		Metadata: map[string]interface{}{}, // No user_id
	}

	result, err := hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)
	assert.Nil(t, result) // Never short-circuits
	assert.Nil(t, pctx.Enrichments)
}

func TestUserMemoryEnrichmentHook_InjectsUserProfile(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()

	// Store some facts
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Category: "preference",
		Content: "User prefers window seats", Source: core.SourceExplicit, Confidence: 0.95,
	})
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f2", Namespace: "universal", Category: "identity",
		Content: "User name is Sarah", Source: core.SourceExplicit, Confidence: 0.95,
	})

	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{})

	pctx := &core.PipelineContext{
		Request:  "window seats",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	result, err := hook.BeforePlanning(ctx, pctx)
	assert.NoError(t, err)
	assert.Nil(t, result) // Never short-circuits

	// Should have injected user_profile enrichment
	require.NotNil(t, pctx.Enrichments)
	profile, ok := pctx.Enrichments[core.EnrichmentUserProfile].(string)
	require.True(t, ok, "enrichment should be a string")
	assert.Contains(t, profile, "<user_profile>")
	assert.Contains(t, profile, "User prefers window seats")
	assert.Contains(t, profile, "User name is Sarah")
	assert.Contains(t, profile, "</user_profile>")
}

func TestUserMemoryEnrichmentHook_NeverShortCircuits(t *testing.T) {
	mem := newTestUserMemory()
	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{})

	pctx := &core.PipelineContext{
		Request:  "test",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	result, err := hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)
	assert.Nil(t, result, "enrichment hook must never short-circuit the pipeline")
}

func TestUserMemoryEnrichmentHook_FiltersLowConfidence(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()

	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Category: "preference",
		Content: "High confidence fact", Confidence: 0.95,
	})
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f2", Namespace: "travel", Category: "preference",
		Content: "Low confidence fact", Confidence: 0.1,
	})

	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{})

	pctx := &core.PipelineContext{
		Request:  "fact",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, _ = hook.BeforePlanning(ctx, pctx)

	profile := pctx.Enrichments[core.EnrichmentUserProfile].(string)
	assert.Contains(t, profile, "High confidence fact")
	assert.NotContains(t, profile, "Low confidence fact")
}

func TestUserMemoryEnrichmentHook_IncludesRelevantSummariesOnly(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()

	// Store a preference, a relevant summary, and an unrelated summary
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Category: "preference",
		Content: "User prefers window seats", Source: core.SourceExplicit, Confidence: 0.95,
	})
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "s1", Namespace: "travel", Category: "summary",
		Content: "User planned a Tokyo trip via ANA", Source: core.SourceDerived, Confidence: 0.80,
	})
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "s2", Namespace: "travel", Category: "summary",
		Content: "User asked for a Slack reminder to pick up their son from school", Source: core.SourceDerived, Confidence: 0.80,
	})

	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{})

	pctx := &core.PipelineContext{
		Request:  "Can you help me compare hotels in Tokyo?",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, _ = hook.BeforePlanning(ctx, pctx)

	profile, ok := pctx.Enrichments[core.EnrichmentUserProfile].(string)
	require.True(t, ok)
	assert.Contains(t, profile, "User planned a Tokyo trip via ANA")
	assert.NotContains(t, profile, "Slack reminder")
	assert.Contains(t, profile, "Summary:")
}

func TestUserMemoryEnrichmentHook_InjectsStableTravelFactsWithoutQueryMatch(t *testing.T) {
	mem := newSelectiveRecallMemory()
	ctx := context.Background()

	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "id-1", Namespace: "universal", Category: "identity",
		Content: "User lives in Coppell, TX", Source: core.SourceExplicit, Confidence: 0.95,
	})
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "pref-1", Namespace: "travel", Category: "preference",
		Content: "User prefers departing from DFW airport", Source: core.SourceExplicit, Confidence: 0.95,
	})
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "constraint-1", Namespace: "travel", Category: "constraint",
		Content: "User is vegetarian", Source: core.SourceExplicit, Confidence: 0.95,
	})
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "relationship-1", Namespace: "travel", Category: "relationship",
		Content: "User often travels with spouse and two children", Source: core.SourceExplicit, Confidence: 0.95,
	})
	mem.queryResults["travel"] = nil
	mem.queryResults["universal"] = nil

	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{})
	pctx := &core.PipelineContext{
		Request:  "Plan a 3 day trip to Kyoto",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, err := hook.BeforePlanning(ctx, pctx)
	require.NoError(t, err)

	profile, ok := pctx.Enrichments[core.EnrichmentUserProfile].(string)
	require.True(t, ok)
	assert.Contains(t, profile, "User lives in Coppell, TX")
	assert.Contains(t, profile, "User prefers departing from DFW airport")
	assert.Contains(t, profile, "User is vegetarian")
	assert.Contains(t, profile, "User often travels with spouse and two children")
}

func TestSelectFactsForPrompt_FiltersIrrelevantContextAndSummary(t *testing.T) {
	facts := []core.UserFact{
		{FactID: "i1", Category: "identity", Content: "User lives in Coppell, TX", Confidence: 0.95},
		{FactID: "p1", Category: "preference", Content: "User prefers DFW airport", Confidence: 0.95},
		{FactID: "c1", Category: "context", Content: "User is planning a trip to South Korea next month", Confidence: 0.95},
		{FactID: "c2", Category: "context", Content: "User is planning a Maldives trip for late April", Confidence: 0.95},
		{FactID: "s1", Category: "summary", Content: "User asked for a Slack reminder", Confidence: 0.80},
		{FactID: "s2", Category: "summary", Content: "User compared Maldives and Seychelles for a family trip from DFW", Confidence: 0.80},
	}

	selected := selectFactsForPrompt(facts, "I want a Maldives flight from DFW", 8, 2, 2, 15)
	require.Len(t, selected, 4)
	assert.Equal(t, "i1", selected[0].FactID)
	assert.Equal(t, "p1", selected[1].FactID)
	assert.Equal(t, "c2", selected[2].FactID)
	assert.Equal(t, "s2", selected[3].FactID)
}

func TestSelectFactsForPrompt_ReservesRoomForRelevantContextAndSummary(t *testing.T) {
	facts := []core.UserFact{
		{FactID: "i1", Category: "identity", Content: "User lives in Coppell, TX", Confidence: 0.95},
		{FactID: "cst1", Category: "constraint", Content: "User is vegetarian", Confidence: 0.95},
		{FactID: "p1", Category: "preference", Content: "User prefers DFW airport", Confidence: 0.95},
		{FactID: "p2", Category: "preference", Content: "User prefers public transport", Confidence: 0.95},
		{FactID: "r1", Category: "relationship", Content: "User travels with spouse and child", Confidence: 0.95},
		{FactID: "c1", Category: "context", Content: "User is planning a Maldives trip for late April", Confidence: 0.95},
		{FactID: "s1", Category: "summary", Content: "User compared Maldives and Seychelles for a family trip from DFW", Confidence: 0.80},
	}

	selected := selectFactsForPrompt(facts, "I want a Maldives flight from DFW", 2, 1, 1, 4)
	require.Len(t, selected, 4)
	assert.Equal(t, "i1", selected[0].FactID)
	assert.Equal(t, "cst1", selected[1].FactID)
	assert.Equal(t, "c1", selected[2].FactID)
	assert.Equal(t, "s1", selected[3].FactID)
}

func TestSelectFactsForPrompt_PrioritizesDurableLifetime(t *testing.T) {
	facts := []core.UserFact{
		{FactID: "t1", Category: "context", Content: "User is planning a Maldives trip next month", Confidence: 0.95},
		{FactID: "s1", Category: "summary", Content: "User compared Maldives and Seychelles", Confidence: 0.80},
		{FactID: "d1", Category: "preference", Content: "User prefers DFW airport", Confidence: 0.95},
		{FactID: "d2", Category: "constraint", Content: "User is vegetarian", Confidence: 0.95},
	}

	selected := selectFactsForPrompt(facts, "Find me flights to Maldives from DFW", 2, 1, 1, 4)
	require.Len(t, selected, 4)
	assert.Equal(t, "d1", selected[0].FactID)
	assert.Equal(t, "d2", selected[1].FactID)
	assert.Equal(t, "t1", selected[2].FactID)
	assert.Equal(t, "s1", selected[3].FactID)
}

func TestSelectFactsForPrompt_SoftBudgetReallocation(t *testing.T) {
	facts := []core.UserFact{
		{FactID: "d1", Category: "preference", Content: "User prefers DFW airport", Confidence: 0.95},
		{FactID: "t1", Category: "context", Content: "User is planning a Maldives trip next month", Confidence: 0.95},
		{FactID: "t2", Category: "context", Content: "User is comparing Seychelles resorts this month", Confidence: 0.95},
		{FactID: "s1", Category: "summary", Content: "User compared Maldives and Seychelles", Confidence: 0.80},
	}

	selected := selectFactsForPrompt(facts, "Compare Maldives and Seychelles from DFW", 3, 2, 1, 4)
	require.Len(t, selected, 4)
	assert.Equal(t, "d1", selected[0].FactID)
	assert.Equal(t, "t1", selected[1].FactID)
	assert.Equal(t, "t2", selected[2].FactID)
	assert.Equal(t, "s1", selected[3].FactID)
}

func TestUserMemoryEnrichmentHook_Name(t *testing.T) {
	hook := NewUserMemoryEnrichmentHook(&core.NoOpUserMemory{}, "travel", nil)
	assert.Equal(t, "user-memory-enrichment", hook.Name())
}

func TestUserMemoryEnrichmentHook_UsesLegacyPromptBudgetEnvFallbacks(t *testing.T) {
	t.Setenv("TRUVAG3_USER_MEMORY_MAX_CONTEXT_FACTS", "6")
	t.Setenv("TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS", "4")
	t.Setenv("TRUVAG3_USER_MEMORY_MAX_TRANSIENT_FACTS_IN_PROMPT", "")
	t.Setenv("TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS_IN_PROMPT", "")

	hook := NewUserMemoryEnrichmentHook(&core.NoOpUserMemory{}, "travel", &core.NoOpLogger{})

	assert.Equal(t, 6, hook.maxTransientFacts)
	assert.Equal(t, 4, hook.maxSummaryFacts)
}

func TestUserMemoryEnrichmentHook_NewPromptBudgetEnvTakesPrecedence(t *testing.T) {
	t.Setenv("TRUVAG3_USER_MEMORY_MAX_CONTEXT_FACTS", "6")
	t.Setenv("TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS", "4")
	t.Setenv("TRUVAG3_USER_MEMORY_MAX_TRANSIENT_FACTS_IN_PROMPT", "3")
	t.Setenv("TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS_IN_PROMPT", "2")

	hook := NewUserMemoryEnrichmentHook(&core.NoOpUserMemory{}, "travel", &core.NoOpLogger{})

	assert.Equal(t, 3, hook.maxTransientFacts)
	assert.Equal(t, 2, hook.maxSummaryFacts)
}

// ─── Extraction Hook Tests ───────────────────────────────────────────────────

func TestUserMemoryExtractionHook_SkipsWithoutUserID(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "should not be stored", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "test",
		Metadata: map[string]interface{}{}, // No user_id
	}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "agent response")
	assert.NoError(t, err)
	assert.Equal(t, "agent response", response, "should pass response through unmodified")

	// Nothing should be stored
	facts, _ := mem.Recall(context.Background(), "user-1", "", "", 10)
	assert.Empty(t, facts)
}

func TestUserMemoryExtractionHook_ExtractsAndStores(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User is vegetarian", Category: "constraint", Source: core.SourceExplicit, Confidence: 0.95},
		{Content: "User prefers direct flights", Category: "preference", Source: core.SourceInferred, Confidence: 0.70},
	}}
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "I'm vegetarian and I like direct flights",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "noted your preferences")
	assert.NoError(t, err)
	assert.Equal(t, "noted your preferences", response, "should pass response through unmodified")

	// Facts should be stored
	facts, _ := mem.Recall(context.Background(), "user-1", "travel", "", 10)
	assert.Len(t, facts, 2)
}

func TestUserMemoryExtractionHook_SetsNamespace(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "Test fact", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "test",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, _ = hook.AfterSynthesis(context.Background(), pctx, "response")

	facts, _ := mem.Recall(context.Background(), "user-1", "travel", "", 10)
	require.Len(t, facts, 1)
	assert.Equal(t, "travel", facts[0].Namespace, "hook should set namespace on extracted facts")
}

func TestUserMemoryExtractionHook_HandlesContradict(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()

	// Store existing fact
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "old-fact", Namespace: "travel", Category: "preference",
		Content: "User prefers window seats", Source: core.SourceExplicit, Confidence: 0.95,
	})

	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User now prefers aisle seats", Category: "preference", Source: core.SourceCorrection, Confidence: 0.98},
	}}
	reconciler := &contradictReconciler{targetFactID: "old-fact"}

	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "Actually I prefer aisle seats",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, _ = hook.AfterSynthesis(ctx, pctx, "response")

	// Old fact should be removed, new fact stored
	facts, _ := mem.Recall(ctx, "user-1", "travel", "", 10)
	require.Len(t, facts, 1)
	assert.Contains(t, facts[0].Content, "aisle seats")
}

func TestUserMemoryExtractionHook_FailOpen(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &failingFactExtractor{}
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "test",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "original response")
	assert.NoError(t, err, "should not return error on extraction failure")
	assert.Equal(t, "original response", response, "should pass response through on failure")
}

func TestUserMemoryExtractionHook_GeneratesSessionSummary(t *testing.T) {
	mem := newTestUserMemory()
	// Mock AI client that returns a summary JSON (reuses existing mockAIClient from hybrid_resolver_test.go)
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: `{"content": "User planned a trip to Tokyo"}`}, nil
	}}
	extractor := &staticFactExtractor{facts: nil} // No regular facts — only summary
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(mem, nil, mockAI, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "Plan a trip to Tokyo",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "Here's your Tokyo trip plan with ANA flights...")
	assert.NoError(t, err)
	assert.Equal(t, "Here's your Tokyo trip plan with ANA flights...", response, "should pass response through")

	// Summary should be stored as category "summary"
	facts, _ := mem.RecallByCategory(context.Background(), "user-1", "travel", "summary", 10)
	require.Len(t, facts, 1)
	assert.Equal(t, "User planned a trip to Tokyo", facts[0].Content)
	assert.Equal(t, core.SourceDerived, facts[0].Source)
	assert.Equal(t, "summary", facts[0].Category)
	assert.Equal(t, 0.80, facts[0].Confidence)
}

func TestUserMemoryExtractionHook_SummaryFailOpen(t *testing.T) {
	mem := newTestUserMemory()
	// Mock AI that fails
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return nil, fmt.Errorf("LLM unavailable")
	}}
	extractor := &staticFactExtractor{facts: nil}
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(mem, nil, mockAI, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "Plan a trip",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "original response")
	assert.NoError(t, err, "should not return error on summary failure")
	assert.Equal(t, "original response", response, "should pass response through on failure")

	// No summary stored
	facts, _ := mem.RecallByCategory(context.Background(), "user-1", "travel", "summary", 10)
	assert.Empty(t, facts)
}

func TestUserMemoryExtractionHook_SummaryParseFailure(t *testing.T) {
	mem := newTestUserMemory()
	// Mock AI that returns invalid JSON for summary
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: "not valid json at all"}, nil
	}}
	extractor := &staticFactExtractor{facts: nil}
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(mem, nil, mockAI, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "Plan a trip",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "original response")
	assert.NoError(t, err, "should not return error on parse failure (fail-open)")
	assert.Equal(t, "original response", response, "should pass response through")

	// No summary stored due to parse failure
	facts, _ := mem.RecallByCategory(context.Background(), "user-1", "travel", "summary", 10)
	assert.Empty(t, facts)
}

func TestUserMemoryExtractionHook_SummaryRespectsPersistencePolicy(t *testing.T) {
	mem := newTestUserMemory()
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: `{"content": "User plans to fund this trip by selling 100 Tesla stocks."}`}, nil
	}}
	extractor := &staticFactExtractor{facts: nil}
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(mem, nil, mockAI, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "I will sell 100 Tesla stocks to fund this trip",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "Noted.")
	assert.NoError(t, err)
	assert.Equal(t, "Noted.", response)

	facts, _ := mem.RecallByCategory(context.Background(), "user-1", "travel", "summary", 10)
	assert.Empty(t, facts)
}

func TestUserMemoryExtractionHook_SummaryAssignsSummaryLifetimeMetadata(t *testing.T) {
	mem := newTestUserMemory()
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: `{"content": "User compared Maldives and Seychelles for a family trip."}`}, nil
	}}
	extractor := &staticFactExtractor{facts: nil}
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(mem, nil, mockAI, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "Compare Maldives and Seychelles",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "Here is a comparison.")
	assert.NoError(t, err)
	assert.Equal(t, "Here is a comparison.", response)

	facts, _ := mem.RecallByCategory(context.Background(), "user-1", "travel", "summary", 10)
	require.Len(t, facts, 1)
	require.NotNil(t, facts[0].Metadata)
	assert.Equal(t, core.UserFactLifetimeSummary, facts[0].Metadata[core.UserFactMetadataLifetimeKey])
}

func TestUserMemoryExtractionHook_Name(t *testing.T) {
	hook := NewUserMemoryExtractionHook(&core.NoOpUserMemory{}, nil, nil, "travel", nil, nil, nil)
	assert.Equal(t, "user-memory-extraction", hook.Name())
}

// ─── Format/Dedup Tests ─────────────────────────────────────────────────────

func TestDeduplicateAndFilter(t *testing.T) {
	identity := []core.UserFact{
		{FactID: "f1", Content: "Name is Sarah", Confidence: 0.95},
	}
	query := []core.UserFact{
		{FactID: "f1", Content: "Name is Sarah", Confidence: 0.95}, // duplicate
		{FactID: "f2", Content: "Prefers window", Confidence: 0.9},
		{FactID: "f3", Content: "Low confidence", Confidence: 0.1}, // below threshold
	}
	universal := []core.UserFact{
		{FactID: "f4", Content: "Universal fact", Confidence: 0.8},
	}

	result := deduplicateAndFilter(identity, nil, nil, query, universal, 0.3)
	assert.Len(t, result, 3) // f1, f2, f4 (f3 filtered, f1 deduped)

	ids := make(map[string]bool)
	for _, f := range result {
		ids[f.FactID] = true
	}
	assert.True(t, ids["f1"])
	assert.True(t, ids["f2"])
	assert.True(t, ids["f4"])
	assert.False(t, ids["f3"])
}

func TestDeduplicateAndFilter_WithSummaries(t *testing.T) {
	identity := []core.UserFact{
		{FactID: "f1", Content: "Name is Sarah", Confidence: 0.95},
	}
	summaries := []core.UserFact{
		{FactID: "s1", Content: "Planned Tokyo trip", Category: "summary", Confidence: 0.80},
		{FactID: "s2", Content: "Booked Paris hotel", Category: "summary", Confidence: 0.80},
	}
	query := []core.UserFact{
		{FactID: "f2", Content: "Prefers window", Confidence: 0.9},
		{FactID: "s1", Content: "Planned Tokyo trip", Confidence: 0.80}, // duplicate with summary
	}
	universal := []core.UserFact{
		{FactID: "f3", Content: "Universal fact", Confidence: 0.8},
	}

	result := deduplicateAndFilter(identity, nil, summaries, query, universal, 0.3)
	// f1, s1, s2, f2, f3 = 5 (s1 deduped from query)
	assert.Len(t, result, 5)

	// Verify priority ordering: identity first, then summaries, then query, then universal
	assert.Equal(t, "f1", result[0].FactID, "identity should come first")
	assert.Equal(t, "s1", result[1].FactID, "summaries should come after identity")
	assert.Equal(t, "s2", result[2].FactID, "summaries should come after identity")
	assert.Equal(t, "f2", result[3].FactID, "query facts after summaries")
	assert.Equal(t, "f3", result[4].FactID, "universal facts last")
}

func TestDeduplicateAndFilter_WithStableFactsPriority(t *testing.T) {
	identity := []core.UserFact{
		{FactID: "f1", Content: "Lives in Coppell", Confidence: 0.95},
	}
	stable := []core.UserFact{
		{FactID: "f2", Content: "Vegetarian", Category: "constraint", Confidence: 0.95},
		{FactID: "f3", Content: "Prefers DFW", Category: "preference", Confidence: 0.95},
	}
	summaries := []core.UserFact{
		{FactID: "s1", Content: "Planned Tokyo trip", Category: "summary", Confidence: 0.80},
	}
	query := []core.UserFact{
		{FactID: "q1", Content: "Likes beaches", Confidence: 0.75},
		{FactID: "f2", Content: "Vegetarian", Confidence: 0.95},
	}
	universal := []core.UserFact{
		{FactID: "u1", Content: "Universal fact", Confidence: 0.8},
	}

	result := deduplicateAndFilter(identity, stable, summaries, query, universal, 0.3)
	require.Len(t, result, 6)
	assert.Equal(t, "f1", result[0].FactID)
	assert.Equal(t, "f2", result[1].FactID)
	assert.Equal(t, "f3", result[2].FactID)
	assert.Equal(t, "s1", result[3].FactID)
	assert.Equal(t, "q1", result[4].FactID)
	assert.Equal(t, "u1", result[5].FactID)
}

func TestFormatUserProfile_Empty(t *testing.T) {
	assert.Equal(t, "", formatUserProfile(nil))
	assert.Equal(t, "", formatUserProfile([]core.UserFact{}))
}

func TestFormatUserProfile_GroupsByCategory(t *testing.T) {
	facts := []core.UserFact{
		{Category: "identity", Content: "Name is Sarah", Source: core.SourceExplicit, Confidence: 0.95},
		{Category: "preference", Content: "Prefers window seats", Source: core.SourceExplicit, Confidence: 0.95},
		{Category: "constraint", Content: "Vegetarian", Source: core.SourceExplicit, Confidence: 0.95},
	}

	profile := formatUserProfile(facts)
	assert.Contains(t, profile, "<user_profile>")
	assert.Contains(t, profile, "</user_profile>")
	assert.Contains(t, profile, "Identity:")
	assert.Contains(t, profile, "Preference:")
	assert.Contains(t, profile, "Constraint:")
	assert.Contains(t, profile, "Name is Sarah")
	assert.Contains(t, profile, "high confidence")
}

// TestFormatUserProfile_TransientUsesRecencyLabel guards the rule that a
// context fact — whose current truth decays with time — must not carry a
// "high confidence" label. The planner needs a staleness signal instead,
// so it can weigh stored context against the live turn.
func TestFormatUserProfile_TransientUsesRecencyLabel(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	facts := []core.UserFact{
		{
			Category:   "context",
			Content:    "User is planning a trip to Switzerland",
			Source:     core.SourceExplicit,
			Confidence: 0.95,
			UpdatedAt:  now.Add(-3 * 24 * time.Hour),
		},
	}

	profile := formatUserProfileAt(facts, now)
	assert.Contains(t, profile, "Context:")
	assert.Contains(t, profile, "recorded 3 days ago")
	assert.NotContains(t, profile, "high confidence")
	assert.NotContains(t, profile, "medium confidence")
	assert.NotContains(t, profile, "low confidence")
}

// TestFormatUserProfile_SummaryUsesRecencyLabel mirrors the transient case
// for summary-lifetime facts.
func TestFormatUserProfile_SummaryUsesRecencyLabel(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	facts := []core.UserFact{
		{
			Category:   "summary",
			Content:    "Planned a Tokyo trip last month",
			Source:     core.SourceDerived,
			Confidence: 0.80,
			UpdatedAt:  now.Add(-10 * time.Hour),
		},
	}

	profile := formatUserProfileAt(facts, now)
	assert.Contains(t, profile, "Summary:")
	assert.Contains(t, profile, "recorded today")
	assert.NotContains(t, profile, "high confidence")
	assert.NotContains(t, profile, "medium confidence")
}

// TestFormatUserProfileAt_DeterministicRendering pins the exact line format
// for both durable (confidence-labeled) and transient (recency-labeled)
// facts against a frozen clock. This guards against regressions in the
// label format that downstream planners would depend on reading.
func TestFormatUserProfileAt_DeterministicRendering(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	facts := []core.UserFact{
		{Category: "identity", Content: "Name is Sarah", Source: core.SourceExplicit, Confidence: 0.95},
		{Category: "preference", Content: "Prefers window seats", Source: core.SourceExplicit, Confidence: 0.80},
		{Category: "context", Content: "User is planning a trip to Switzerland", Source: core.SourceExplicit, Confidence: 0.95, UpdatedAt: now.Add(-3 * 24 * time.Hour)},
		{Category: "summary", Content: "Planned a Tokyo trip", Source: core.SourceDerived, Confidence: 0.80, UpdatedAt: now.Add(-8 * 24 * time.Hour)},
	}

	profile := formatUserProfileAt(facts, now)

	// Category order is identity -> constraint -> preference -> relationship -> context -> summary.
	expectedLines := []string{
		"- Name is Sarah (explicit, high confidence)",
		"- Prefers window seats (explicit, medium confidence)",
		"- User is planning a trip to Switzerland (explicit, recorded 3 days ago)",
		"- Planned a Tokyo trip (derived, recorded 1 week ago)",
	}
	for _, line := range expectedLines {
		assert.Contains(t, profile, line, "profile should contain exact line %q\n---\n%s", line, profile)
	}
}

// TestFactProvenanceLabel_Durable exercises the confidence-band branch for
// each of identity, preference, constraint, and relationship categories.
func TestFactProvenanceLabel_Durable(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		category   string
		confidence float64
		want       string
	}{
		{"identity high", "identity", 0.95, "high confidence"},
		{"preference medium", "preference", 0.80, "medium confidence"},
		{"constraint low", "constraint", 0.50, "low confidence"},
		{"relationship boundary 0.9", "relationship", 0.90, "high confidence"},
		{"preference boundary 0.7", "preference", 0.70, "medium confidence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fact := core.UserFact{Category: tc.category, Confidence: tc.confidence}
			assert.Equal(t, tc.want, factProvenanceLabel(fact, now))
		})
	}
}

// TestFactProvenanceLabel_Transient exercises the recency branch for
// context and summary lifetimes, and verifies metadata overrides category.
func TestFactProvenanceLabel_Transient(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)

	context := core.UserFact{
		Category:   "context",
		Confidence: 0.95,
		UpdatedAt:  now.Add(-5 * time.Hour),
	}
	assert.Equal(t, "recorded today", factProvenanceLabel(context, now))

	summary := core.UserFact{
		Category:   "summary",
		Confidence: 0.80,
		UpdatedAt:  now.Add(-60 * 24 * time.Hour),
	}
	assert.Equal(t, "recorded over 2 months ago", factProvenanceLabel(summary, now))

	// Metadata lifetime override wins over category-derived lifetime: a
	// fact tagged transient via metadata must render with a recency label
	// even when its category would default to durable.
	overridden := core.UserFact{
		Category:   "preference",
		Confidence: 0.95,
		UpdatedAt:  now.Add(-2 * 24 * time.Hour),
		Metadata:   map[string]string{core.UserFactMetadataLifetimeKey: core.UserFactLifetimeTransient},
	}
	assert.Equal(t, "recorded 2 days ago", factProvenanceLabel(overridden, now))
}

// TestExtractionPrompt_AttributionRule pins the rule that keeps the extractor
// from promoting destinations, dates, or entities that appear only in the
// assistant response to user facts. Removing this rule re-opens a feedback
// loop where a stale Context fact produces assistant output that is then
// re-extracted and written back as a fresh user fact.
func TestExtractionPrompt_AttributionRule(t *testing.T) {
	assert.Contains(t, extractionPromptTemplate, "Only extract facts stated or agreed to by the user in <user_memory_user_message>")
	assert.Contains(t, extractionPromptTemplate, "appear only in <user_memory_assistant_message>")
	assert.Contains(t, extractionPromptTemplate, "<user_memory_extraction_example_3>")
	assert.Contains(t, extractionPromptTemplate, "\"it\" is a pronoun")
}

// TestExtractionPrompt_DomainNeutrality guards FRAMEWORK_DESIGN_PRINCIPLES.md
// §"Framework is domain-agnostic": the default extraction prompt must not
// anchor the LLM on any single domain's vocabulary. Agents needing
// domain-specific extraction supply WithUserFactExtractor(). The scan
// covers the rules block and the three few-shot examples, which together
// form the prompt the LLM conditions on.
func TestExtractionPrompt_DomainNeutrality(t *testing.T) {
	start := strings.Index(extractionPromptTemplate, "<user_memory_extraction_rules>")
	end := strings.Index(extractionPromptTemplate, "</user_memory_extraction_example_3>")
	require.Greater(t, start, -1, "rules block must be present")
	require.Greater(t, end, start, "example_3 must close after rules open")
	block := extractionPromptTemplate[start:end]

	// Words that would bias the extractor toward one domain if present in
	// the framework default. This list is deliberately narrow — it captures
	// the travel and finance vocabulary that previously leaked through the
	// prompt. Expand the list when new leakage is observed; do not shrink
	// it without replacing anchors.
	domainLeakWords := []string{
		// Travel
		"airport", "flight", "flights", "trip to", "itinerary", "aisle seat",
		"window seat", "hotel", "DFW", "Tokyo", "Japan", "Korea",
		"Switzerland", "Zurich", "ANA", "Park Hyatt",
		// Finance
		"stocks", "shares", "Tesla", "ticker",
	}
	for _, word := range domainLeakWords {
		assert.NotContains(t, block, word,
			"framework default extraction prompt contains domain-specific word %q — move domain vocabulary to agent overrides (WithUserFactExtractor) per FRAMEWORK_DESIGN_PRINCIPLES.md §Framework is domain-agnostic", word)
	}
}

// TestSummaryPrompt_AttributionRule pins the rule that prevents the
// summarizer from echoing assistant-suggested entities back into memory
// as user-asserted facts. Without this rule, a misgenerated answer (e.g.
// the Switzerland itinerary the agent produced when stale context fired)
// gets summarized as "User planned a trip to Switzerland", stored as a
// summary fact, and re-loaded on the next turn — the same feedback loop
// Fix F closes on the extraction path, now closed on the summary path.
func TestSummaryPrompt_AttributionRule(t *testing.T) {
	assert.Contains(t, summaryPromptTemplate, "Attribute named entities")
	assert.Contains(t, summaryPromptTemplate, "appear only in <user_memory_assistant_message>")
	assert.Contains(t, summaryPromptTemplate, "agent proposed")
	assert.Contains(t, summaryPromptTemplate, "<user_memory_summary_example_2>")
	assert.Contains(t, summaryPromptTemplate, "pronoun")
}

// TestSummaryPrompt_NoNegativePhrasing enforces §2.4 (positive directives)
// for the rules block in the summary template, mirroring the extraction
// guard. The summary rules describe what to attribute and what to omit
// using affirmative language only.
func TestSummaryPrompt_NoNegativePhrasing(t *testing.T) {
	start := strings.Index(summaryPromptTemplate, "<user_memory_summary_rules>")
	end := strings.Index(summaryPromptTemplate, "</user_memory_summary_rules>")
	require.Greater(t, start, -1, "rules block must be present")
	require.Greater(t, end, start, "rules block must close after it opens")
	rulesBlock := summaryPromptTemplate[start:end]

	for _, banned := range []string{"NEVER", "WRONG", "Do NOT", "DO NOT"} {
		assert.NotContains(t, rulesBlock, banned, "summary rules block should avoid %q per EFFECTIVE_PROMPTS_GUIDE §2.4", banned)
	}
}

// TestSummaryPrompt_DomainNeutrality mirrors the extraction test for the
// session summarizer. The default summary prompt is the last framework
// prompt visible to every agent; domain bias here leaks into every stored
// summary fact.
func TestSummaryPrompt_DomainNeutrality(t *testing.T) {
	domainLeakWords := []string{
		"airport", "flight", "trip to", "itinerary", "hotel", "DFW",
		"Tokyo", "Japan", "Korea", "Switzerland", "ANA", "Park Hyatt",
		"stocks", "shares", "Tesla", "ticker",
	}
	for _, word := range domainLeakWords {
		assert.NotContains(t, summaryPromptTemplate, word,
			"framework default summary prompt contains domain-specific word %q — use domain-neutral vocabulary in the prompt default", word)
	}
}

// TestExtractionPrompt_NoNegativePhrasing enforces §2.4 (positive directives)
// for the rules block we authored. Negative framing like "do not promote"
// was replaced with affirmative equivalents; this guards against regression.
func TestExtractionPrompt_NoNegativePhrasing(t *testing.T) {
	start := strings.Index(extractionPromptTemplate, "<user_memory_extraction_rules>")
	end := strings.Index(extractionPromptTemplate, "</user_memory_extraction_rules>")
	require.Greater(t, start, -1, "rules block must be present")
	require.Greater(t, end, start, "rules block must close after it opens")
	rulesBlock := extractionPromptTemplate[start:end]

	// §2.4: "NEVER", "WRONG", and "Do NOT" are banned across all our rules.
	// "do not" (lowercase) is permitted in negation of existing user claims
	// ("the user did not name") — guard the uppercase variants that §2.4
	// specifically calls out.
	for _, banned := range []string{"NEVER", "WRONG", "Do NOT", "DO NOT"} {
		assert.NotContains(t, rulesBlock, banned, "rules block should avoid %q per EFFECTIVE_PROMPTS_GUIDE §2.4", banned)
	}
}

func TestHumanizeFactAge(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		updatedAt time.Time
		want      string
	}{
		{"zero time", time.Time{}, "earlier"},
		{"future clamped to today", now.Add(1 * time.Hour), "today"},
		{"within 24h", now.Add(-5 * time.Hour), "today"},
		{"just over a day", now.Add(-25 * time.Hour), "yesterday"},
		{"three days", now.Add(-3 * 24 * time.Hour), "3 days ago"},
		{"one week", now.Add(-8 * 24 * time.Hour), "1 week ago"},
		{"three weeks", now.Add(-22 * 24 * time.Hour), "3 weeks ago"},
		{"over a month", now.Add(-45 * 24 * time.Hour), "over a month ago"},
		{"over N months", now.Add(-120 * 24 * time.Hour), "over 4 months ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := humanizeFactAge(tc.updatedAt, now)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ─── Reconcile Result Tests ──────────────────────────────────────────────────

func TestReconcileResult_ADDForNoExisting(t *testing.T) {
	reconciler := NewLLMUserFactReconciler(nil, nil, "", 0.75, nil)

	candidate := core.UserFact{Content: "New fact", Source: core.SourceExplicit}
	result, err := reconciler.Reconcile(context.Background(), "user-1", "travel", candidate, nil)
	require.NoError(t, err)
	assert.Equal(t, "ADD", result.Operation)
	assert.Equal(t, "New fact", result.MergedFact.Content)
	assert.Nil(t, result.Response, "should not populate Response when no LLM call is made")
}

// ─── Interface compliance ────────────────────────────────────────────────────

func TestUserMemoryHooks_ImplementInterfaces(t *testing.T) {
	// Enrichment hook implements BeforePlanningHook
	var _ core.PipelineHook = (*UserMemoryEnrichmentHook)(nil)
	var _ core.BeforePlanningHook = (*UserMemoryEnrichmentHook)(nil)

	// Extraction hook implements AfterSynthesisHook
	var _ core.PipelineHook = (*UserMemoryExtractionHook)(nil)
	var _ core.AfterSynthesisHook = (*UserMemoryExtractionHook)(nil)
}

// ─── Builder Tests ──────────────────────────────────────────────────────────

func TestBuildUserMemoryHooks_NilDeps(t *testing.T) {
	hooks, closer := BuildUserMemoryHooks(nil, nil, nil)
	assert.Nil(t, hooks, "nil deps should return nil hooks")
	require.NotNil(t, closer, "closer must never be nil so defer Close() is always safe")
	assert.NoError(t, closer.Close(), "no-op closer should succeed")
}

func TestBuildUserMemoryHooks_NilUserMemory(t *testing.T) {
	deps := &core.UserMemoryDeps{UserMemory: nil}
	hooks, closer := BuildUserMemoryHooks(deps, nil, nil)
	assert.Nil(t, hooks, "nil UserMemory should return nil hooks")
	require.NotNil(t, closer)
	assert.NoError(t, closer.Close())
}

func TestBuildUserMemoryHooks_ReturnsCorrectOrder(t *testing.T) {
	mem := newTestUserMemory()
	deps := &core.UserMemoryDeps{UserMemory: mem, Namespace: "travel"}
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: "[]"}, nil
	}}
	hooks, closer := BuildUserMemoryHooks(deps, mockAI, &core.NoOpLogger{})
	defer closer.Close()

	require.Len(t, hooks, 2)
	assert.Equal(t, "user-memory-enrichment", hooks[0].Name())
	assert.Equal(t, "user-memory-extraction", hooks[1].Name())
}

func TestBuildUserMemoryHooks_DefaultsToAsyncExtraction(t *testing.T) {
	mem := newTestUserMemory()
	deps := &core.UserMemoryDeps{UserMemory: mem, Namespace: "travel"}

	hooks, closer := BuildUserMemoryHooks(deps, nil, &core.NoOpLogger{})
	defer closer.Close()

	require.Len(t, hooks, 2)
	extractHook, ok := hooks[1].(*UserMemoryExtractionHook)
	require.True(t, ok)
	assert.True(t, extractHook.asynchronous, "Layer 1 preset should default to async extraction")
}

func TestBuildUserMemoryHooks_WithSynchronousExtraction_DisablesAsync(t *testing.T) {
	mem := newTestUserMemory()
	deps := &core.UserMemoryDeps{UserMemory: mem, Namespace: "travel"}

	hooks, closer := BuildUserMemoryHooks(deps, nil, &core.NoOpLogger{}, WithSynchronousExtraction())
	defer closer.Close()

	require.Len(t, hooks, 2)
	extractHook, ok := hooks[1].(*UserMemoryExtractionHook)
	require.True(t, ok)
	assert.False(t, extractHook.asynchronous, "WithSynchronousExtraction should opt out of async extraction")
}

func TestBuildUserMemoryHooks_WithCustomExtractor(t *testing.T) {
	mem := newTestUserMemory()
	deps := &core.UserMemoryDeps{UserMemory: mem, Namespace: "travel"}
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: "[]"}, nil
	}}

	customExtractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "Custom extracted", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}

	hooks, closer := BuildUserMemoryHooks(deps, mockAI, &core.NoOpLogger{},
		WithUserFactExtractor(customExtractor),
	)
	defer closer.Close()
	require.Len(t, hooks, 2)
}

func TestBuildUserMemoryHooks_WithCustomPersistencePolicy(t *testing.T) {
	mem := newTestUserMemory()
	deps := &core.UserMemoryDeps{UserMemory: mem, Namespace: "travel"}
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: "[]"}, nil
	}}

	hooks, closer := BuildUserMemoryHooks(deps, mockAI, &core.NoOpLogger{},
		WithUserFactPersistencePolicy(staticPersistencePolicy{
			decision: UserFactPersistenceDecision{Store: false},
		}),
	)
	defer closer.Close()
	require.Len(t, hooks, 2)
}

func TestUserMemoryExtractionHook_AsynchronousReturnsBeforeStorageCompletes(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &blockingFactExtractor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		facts: []core.UserFact{
			{Content: "User prefers DFW airport", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
		},
	}
	hook := NewUserMemoryExtractionHook(
		mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, &addAllReconciler{},
		WithAsynchronousUserExtraction(),
	)

	pctx := &core.PipelineContext{
		Request:  "My preferred airport is DFW",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	done := make(chan error, 1)
	go func() {
		_, err := hook.AfterSynthesis(context.Background(), pctx, "Noted.")
		done <- err
	}()

	select {
	case <-extractor.started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("async extraction did not start")
	}

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("AfterSynthesis should return before async extraction completes")
	}

	mem.mu.RLock()
	assert.Len(t, mem.facts["user-1"], 0, "background extraction should not have stored before release")
	mem.mu.RUnlock()

	close(extractor.release)
	require.NoError(t, hook.Close())

	mem.mu.RLock()
	require.Len(t, mem.facts["user-1"], 1)
	assert.Equal(t, "User prefers DFW airport", mem.facts["user-1"][0].Content)
	mem.mu.RUnlock()
}

func TestUserMemoryExtractionHook_CloseDrainsAsyncExtraction(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &blockingFactExtractor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		facts: []core.UserFact{
			{Content: "User prefers family-friendly travel", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
		},
	}
	hook := NewUserMemoryExtractionHook(
		mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, &addAllReconciler{},
		WithAsynchronousUserExtraction(),
	)

	pctx := &core.PipelineContext{
		Request:  "Please remember that I prefer family-friendly travel",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, err := hook.AfterSynthesis(context.Background(), pctx, "Noted.")
	require.NoError(t, err)

	select {
	case <-extractor.started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("async extraction did not start")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- hook.Close()
	}()

	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before async extraction was released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(extractor.release)

	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not drain in-flight async extraction")
	}

	mem.mu.RLock()
	require.Len(t, mem.facts["user-1"], 1)
	assert.Equal(t, "User prefers family-friendly travel", mem.facts["user-1"][0].Content)
	mem.mu.RUnlock()
}

func TestBuildUserMemoryHooks_DefaultAsyncPreservesPhase4CompactionBehavior(t *testing.T) {
	mem := newTestUserMemory()
	require.NoError(t, mem.Remember(context.Background(), "user-1", core.UserFact{
		FactID:     "pref-1",
		Namespace:  "travel",
		Category:   "preference",
		Content:    "User prefers DFW airport",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable,
		},
	}))

	deps := &core.UserMemoryDeps{UserMemory: mem, Namespace: "travel"}
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User prefers DFW airport; User prefers public transport", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	hooks, closer := BuildUserMemoryHooks(deps, nil, &core.NoOpLogger{},
		WithUserFactExtractor(extractor),
		WithUserFactReconciler(&splitSiblingReconciler{targetFactID: "pref-1"}),
	)
	defer closer.Close()

	extractHook, ok := hooks[1].(core.AfterSynthesisHook)
	require.True(t, ok)

	pctx := &core.PipelineContext{
		Request:  "Remember that I prefer DFW and public transport",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	resp, err := extractHook.AfterSynthesis(context.Background(), pctx, "Noted.")
	require.NoError(t, err)
	assert.Equal(t, "Noted.", resp)

	require.NoError(t, closer.Close())

	mem.mu.RLock()
	require.Len(t, mem.facts["user-1"], 1)
	assert.Contains(t, strings.ToLower(mem.facts["user-1"][0].Content), "dfw airport")
	assert.Contains(t, strings.ToLower(mem.facts["user-1"][0].Content), "public transport")
	mem.mu.RUnlock()
}

func TestWithUserFactExtractor_NilReturnsError(t *testing.T) {
	opt := WithUserFactExtractor(nil)
	err := opt(&userMemoryHookConfig{})
	assert.Error(t, err)
}

func TestWithUserFactReconciler_NilReturnsError(t *testing.T) {
	opt := WithUserFactReconciler(nil)
	err := opt(&userMemoryHookConfig{})
	assert.Error(t, err)
}

func TestWithUserFactPersistencePolicy_NilReturnsError(t *testing.T) {
	opt := WithUserFactPersistencePolicy(nil)
	err := opt(&userMemoryHookConfig{})
	assert.Error(t, err)
}

func TestWithUserMemoryRetrievalWeights_Applied(t *testing.T) {
	cfg := &userMemoryHookConfig{}
	w := core.RetrievalWeights{Recency: 0.1, Relevance: 0.8, Importance: 0.1}
	opt := WithUserMemoryRetrievalWeights(w)
	err := opt(cfg)
	require.NoError(t, err)
	assert.Equal(t, 0.8, cfg.retrievalWeights.Relevance)
}

// ─── Helper Function Tests ──────────────────────────────────────────────────

func TestTruncateUTF8_ASCII(t *testing.T) {
	result := truncateUTF8("hello world", 5)
	assert.Equal(t, "hello", result)
}

func TestTruncateUTF8_MultiByte(t *testing.T) {
	// "héllo" — é is 2 bytes in UTF-8
	result := truncateUTF8("héllo", 3)
	// At byte 3 we're in the middle of 'l' after 'hé' (h=1, é=2, l=1 → 4 bytes for "hél")
	// So maxBytes=3 should give us "hé" (3 bytes exactly)
	assert.Equal(t, "hé", result)
}

func TestTruncateUTF8_ExactBoundary(t *testing.T) {
	result := truncateUTF8("abc", 3)
	assert.Equal(t, "abc", result)
}

func TestTruncateUTF8_LongerThanInput(t *testing.T) {
	result := truncateUTF8("hi", 100)
	assert.Equal(t, "hi", result)
}

func TestTruncateUTF8_Empty(t *testing.T) {
	result := truncateUTF8("", 10)
	assert.Equal(t, "", result)
}

func TestCleanLLMJSONResponse_NoFence(t *testing.T) {
	result := cleanLLMJSONResponse(`{"key": "value"}`)
	assert.Equal(t, `{"key": "value"}`, result)
}

func TestCleanLLMJSONResponse_JSONFence(t *testing.T) {
	result := cleanLLMJSONResponse("```json\n{\"key\": \"value\"}\n```")
	assert.Equal(t, "{\"key\": \"value\"}", result)
}

func TestCleanLLMJSONResponse_PlainFence(t *testing.T) {
	result := cleanLLMJSONResponse("```\n{\"key\": \"value\"}\n```")
	assert.Equal(t, "{\"key\": \"value\"}", result)
}

func TestCleanLLMJSONResponse_WhitespaceOnly(t *testing.T) {
	result := cleanLLMJSONResponse("  \n  ")
	assert.Equal(t, "", result)
}

// ─── DefaultUserFactExtractor Tests ─────────────────────────────────────────

func TestDefaultUserFactExtractor_ParsesValidJSON(t *testing.T) {
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: `[{"content":"User is vegetarian","category":"constraint","source":"explicit","confidence":0.95}]`, Model: "gpt-4o-mini", Provider: "openai"}, nil
	}}

	extractor := NewDefaultUserFactExtractor(mockAI, "", &core.NoOpLogger{})
	result, err := extractor.ExtractFacts(context.Background(), "I'm vegetarian", "Noted!", nil)

	require.NoError(t, err)
	require.Len(t, result.Facts, 1)
	assert.Equal(t, "User is vegetarian", result.Facts[0].Content)
	assert.Equal(t, "constraint", result.Facts[0].Category)
	assert.Equal(t, core.SourceExplicit, result.Facts[0].Source)
	assert.Equal(t, 0.95, result.Facts[0].Confidence)
	// Verify AIResponse metadata is returned
	require.NotNil(t, result.Response)
	assert.Equal(t, "gpt-4o-mini", result.Response.Model)
	assert.Equal(t, "openai", result.Response.Provider)
}

func TestDefaultUserFactExtractor_ErrorOnInvalidJSON(t *testing.T) {
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: "not json"}, nil
	}}

	extractor := NewDefaultUserFactExtractor(mockAI, "", &core.NoOpLogger{})
	result, err := extractor.ExtractFacts(context.Background(), "hello", "hi", nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse LLM response")
	assert.Nil(t, result)
}

func TestDefaultUserFactExtractor_LLMError(t *testing.T) {
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return nil, fmt.Errorf("API error")
	}}

	extractor := NewDefaultUserFactExtractor(mockAI, "", &core.NoOpLogger{})
	result, err := extractor.ExtractFacts(context.Background(), "hello", "hi", nil)

	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestDefaultUserFactExtractor_AssignsFactID(t *testing.T) {
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: `[{"content":"fact","category":"preference","source":"explicit","confidence":0.9}]`}, nil
	}}

	extractor := NewDefaultUserFactExtractor(mockAI, "", &core.NoOpLogger{})
	result, err := extractor.ExtractFacts(context.Background(), "q", "a", nil)

	require.NoError(t, err)
	require.Len(t, result.Facts, 1)
	assert.NotEmpty(t, result.Facts[0].FactID, "should auto-assign FactID")
	assert.False(t, result.Facts[0].CreatedAt.IsZero(), "should set CreatedAt")
}

func TestNormalizeExtractedFact_DemotesTripConstraintToContext(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User began planning a 10-day family trip starting the last Sunday of this month from Coppell, TX via DFW to Japan and South Korea.",
		Category:   "constraint",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	assert.Equal(t, "context", fact.Category)
}

func TestNormalizeExtractedFact_DoesNotDependOnTravelWords(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User is planning a kitchen renovation next month and considering appliance packages.",
		Category:   "preference",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	assert.Equal(t, "context", fact.Category)
}

func TestNormalizeExtractedFact_PreservesDurableConstraint(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User is vegetarian",
		Category:   "constraint",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	assert.Equal(t, "constraint", fact.Category)
}

func TestNormalizeExtractedFact_AssignsDurableLifetimeMetadata(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User prefers DFW airport",
		Category:   "preference",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	require.NotNil(t, fact.Metadata)
	assert.Equal(t, core.UserFactLifetimeDurable, fact.Metadata[core.UserFactMetadataLifetimeKey])
}

func TestNormalizeExtractedFact_PreservesMixedDurablePreference(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User prefers public transport and is planning a move next month.",
		Category:   "preference",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	assert.Equal(t, "preference", fact.Category)
}

func TestNormalizeExtractedFact_DemotesTaskScopedPreferenceToContext(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User prefers aisle seats for this flight next week.",
		Category:   "preference",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	assert.Equal(t, "context", fact.Category)
}

func TestNormalizeExtractedFact_PreservesMixedDurableConstraint(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User is vegetarian and is planning a family trip to Japan next month.",
		Category:   "constraint",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	assert.Equal(t, "constraint", fact.Category)
}

func TestNormalizeExtractedFact_PreservesTripFundingFactForPolicyStage(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User plans to fund this trip by selling 100 Tesla stocks.",
		Category:   "preference",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	assert.Equal(t, "User plans to fund this trip by selling 100 Tesla stocks.", fact.Content)
}

func TestDefaultUserFactPersistencePolicy_DropsTripFundingFact(t *testing.T) {
	policy := NewDefaultUserFactPersistencePolicy()
	decision, err := policy.Evaluate(context.Background(), UserFactPersistenceInput{
		Fact: core.UserFact{
			Content:  "User plans to fund this trip by selling 100 Tesla stocks.",
			Category: "preference",
		},
	})

	require.NoError(t, err)
	assert.False(t, decision.Store)
}

func TestDefaultUserFactPersistencePolicy_DropsTransientTaskFunding(t *testing.T) {
	policy := NewDefaultUserFactPersistencePolicy()
	decision, err := policy.Evaluate(context.Background(), UserFactPersistenceInput{
		Fact: core.UserFact{
			Content:  "User is planning to fund the current renovation by selling company shares next month.",
			Category: "preference",
		},
	})

	require.NoError(t, err)
	assert.False(t, decision.Store)
}

func TestDefaultUserFactPersistencePolicy_DropsOneOffPurchaseFundingFact(t *testing.T) {
	policy := NewDefaultUserFactPersistencePolicy()
	decision, err := policy.Evaluate(context.Background(), UserFactPersistenceInput{
		Fact: core.UserFact{
			Content:  "User will use this year's bonus to pay for this laptop purchase.",
			Category: "preference",
		},
	})

	require.NoError(t, err)
	assert.False(t, decision.Store)
}

func TestDefaultUserFactPersistencePolicy_PreservesStandingPreference(t *testing.T) {
	policy := NewDefaultUserFactPersistencePolicy()
	decision, err := policy.Evaluate(context.Background(), UserFactPersistenceInput{
		Fact: core.UserFact{
			Content:  "User usually prefers to keep discretionary spending cash-funded.",
			Category: "preference",
		},
	})

	require.NoError(t, err)
	assert.True(t, decision.Store)
	assert.Equal(t, "preference", decision.Fact.Category)
}

func TestDefaultUserFactPersistencePolicy_PreservesStandingProjectFundingHabit(t *testing.T) {
	policy := NewDefaultUserFactPersistencePolicy()
	decision, err := policy.Evaluate(context.Background(), UserFactPersistenceInput{
		Fact: core.UserFact{
			Content:  "User usually uses cash to cover renovation expenses when planning home projects.",
			Category: "preference",
		},
	})

	require.NoError(t, err)
	assert.True(t, decision.Store)
	assert.Equal(t, "preference", decision.Fact.Category)
}

func TestDefaultUserFactPersistencePolicy_PreservesDurableFinancialPreference(t *testing.T) {
	policy := NewDefaultUserFactPersistencePolicy()
	decision, err := policy.Evaluate(context.Background(), UserFactPersistenceInput{
		Fact: core.UserFact{
			Content:  "User prefers to use reward points for hotel stays when available.",
			Category: "preference",
		},
	})

	require.NoError(t, err)
	assert.True(t, decision.Store)
	assert.Equal(t, "preference", decision.Fact.Category)
}

func TestUserMemoryExtractionHook_DefaultPersistencePolicyDropsTransientFundingFact(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User plans to fund this trip by selling 100 Tesla stocks.", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &addAllReconciler{}
	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "I will sell 100 Tesla stocks to fund this trip",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	resp, err := hook.AfterSynthesis(context.Background(), pctx, "Noted.")
	require.NoError(t, err)
	assert.Equal(t, "Noted.", resp)
	assert.Len(t, mem.facts["user-1"], 0)
}

func TestUserMemoryExtractionHook_CustomPersistencePolicyCanDropFact(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User prefers DFW airport for flights", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &addAllReconciler{}
	hook := NewUserMemoryExtractionHook(
		mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler,
		WithUserExtractionPersistencePolicy(staticPersistencePolicy{decision: UserFactPersistenceDecision{Store: false}}),
	)

	pctx := &core.PipelineContext{
		Request:  "My preferred airport is DFW",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	resp, err := hook.AfterSynthesis(context.Background(), pctx, "Noted.")
	require.NoError(t, err)
	assert.Equal(t, "Noted.", resp)
	assert.Len(t, mem.facts["user-1"], 0)
}

func TestUserMemoryExtractionHook_DefaultsExtractorAndReconciler_WhenNil(t *testing.T) {
	mem := newTestUserMemory()
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{
			Content: `[{"content":"User prefers DFW airport for flights","category":"preference","source":"explicit","confidence":0.95}]`,
		}, nil
	}}

	hook := NewUserMemoryExtractionHook(
		mem, nil, mockAI, "travel", &core.NoOpLogger{}, nil, nil,
		WithUserExtractionPersistencePolicy(staticPersistencePolicy{decision: UserFactPersistenceDecision{Store: false}}),
	)

	pctx := &core.PipelineContext{
		Request:  "My preferred airport is DFW",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	resp, err := hook.AfterSynthesis(context.Background(), pctx, "Noted.")
	require.NoError(t, err)
	assert.Equal(t, "Noted.", resp)
	assert.Len(t, mem.facts["user-1"], 0)
}

func TestUserMemoryExtractionHook_AssignsTransientLifetimeMetadataFromSafetyNetInference(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User prefers aisle seats for this flight next week.", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &addAllReconciler{}
	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "Remember that I want aisle seats for this flight next week",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	resp, err := hook.AfterSynthesis(context.Background(), pctx, "Noted.")
	require.NoError(t, err)
	assert.Equal(t, "Noted.", resp)
	require.Len(t, mem.facts["user-1"], 1)
	require.NotNil(t, mem.facts["user-1"][0].Metadata)
	assert.Equal(t, core.UserFactLifetimeTransient, mem.facts["user-1"][0].Metadata[core.UserFactMetadataLifetimeKey])
}

func TestUserMemoryExtractionHook_ReconcileMergesDurableProfileFacts(t *testing.T) {
	mem := newTestUserMemory()
	require.NoError(t, mem.Remember(context.Background(), "user-1", core.UserFact{
		FactID:     "pref-1",
		Namespace:  "travel",
		Category:   "preference",
		Content:    "User prefers DFW airport",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable,
		},
	}))

	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User prefers DFW airport", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &updateReconciler{
		targetFactID:  "pref-1",
		mergedContent: "User prefers DFW airport; User prefers public transport",
	}
	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "Remember that I prefer DFW and public transport",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	resp, err := hook.AfterSynthesis(context.Background(), pctx, "Noted.")
	require.NoError(t, err)
	assert.Equal(t, "Noted.", resp)
	require.Len(t, mem.facts["user-1"], 1)
	assert.Contains(t, strings.ToLower(mem.facts["user-1"][0].Content), "dfw airport")
	assert.Contains(t, strings.ToLower(mem.facts["user-1"][0].Content), "public transport")
}

func TestUserMemoryExtractionHook_DedupesSameTurnDurableSplitSiblings(t *testing.T) {
	mem := newTestUserMemory()
	require.NoError(t, mem.Remember(context.Background(), "user-1", core.UserFact{
		FactID:     "pref-1",
		Namespace:  "travel",
		Category:   "preference",
		Content:    "User prefers DFW airport",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable,
		},
	}))

	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User prefers DFW airport; User prefers public transport", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &splitSiblingReconciler{targetFactID: "pref-1"}
	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)

	pctx := &core.PipelineContext{
		Request:  "Remember that I prefer DFW and public transport",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	resp, err := hook.AfterSynthesis(context.Background(), pctx, "Noted.")
	require.NoError(t, err)
	assert.Equal(t, "Noted.", resp)
	require.Len(t, mem.facts["user-1"], 1)
	assert.Contains(t, strings.ToLower(mem.facts["user-1"][0].Content), "dfw airport")
	assert.Contains(t, strings.ToLower(mem.facts["user-1"][0].Content), "public transport")
}

func TestUserMemoryExtractionHook_PolicyFailureIsObservable(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User prefers DFW airport for flights", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &addAllReconciler{}

	var warned bool
	logger := &mockLogger{
		warnFunc: func(msg string, fields map[string]interface{}) {
			if msg != "User fact persistence policy failed" {
				return
			}
			warned = true
			assert.Equal(t, "user_memory_persistence_policy", fields["operation"])
			assert.Equal(t, "req-123", fields["request_id"])
			assert.Equal(t, "user-1", fields["user_id"])
			assert.Equal(t, "policy_eval_error", fields["error_type"])
			require.NotEmpty(t, fields["error"])
		},
	}

	hook := NewUserMemoryExtractionHook(
		mem, nil, nil, "travel", logger, extractor, reconciler,
		WithUserExtractionPersistencePolicy(staticPersistencePolicy{err: fmt.Errorf("boom")}),
	)

	pctx := &core.PipelineContext{
		Request:  "My preferred airport is DFW",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	ctx := WithRequestID(context.Background(), "req-123")
	resp, err := hook.AfterSynthesis(ctx, pctx, "Noted.")
	require.NoError(t, err)
	assert.Equal(t, "Noted.", resp)
	assert.Len(t, mem.facts["user-1"], 0)
	assert.True(t, warned, "expected policy failure warning log")
}

func TestNormalizeExtractedFact_DemotesOneOffPurchaseFundingFactToContext(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User will use this year's bonus to pay for this laptop purchase.",
		Category:   "preference",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	assert.Equal(t, "context", fact.Category)
}

func TestNormalizeExtractedFact_PreservesStandingFundingPreference(t *testing.T) {
	fact, ok := normalizeExtractedFact(rawExtractedFact{
		Content:    "User prefers to use reward points for hotel stays when available.",
		Category:   "preference",
		Source:     "explicit",
		Confidence: 0.95,
	}, time.Now())

	require.True(t, ok)
	assert.Equal(t, "preference", fact.Category)
}

func TestSplitMixedExtractedFact_CapsClauseFanout(t *testing.T) {
	fact := core.UserFact{
		Category:   "preference",
		Content:    "User prefers DFW airport; User prefers public transport; User travels with family; User is planning a trip next month",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
	}

	split := splitMixedExtractedFact(fact, 3)
	require.Len(t, split, 3)
}

func TestSplitMixedExtractedFact_SplitsMixedProfileAndContextFacts(t *testing.T) {
	fact := core.UserFact{
		Category:   "context",
		Content:    "User prefers public transport; User is planning a move next month",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
	}

	split := splitMixedExtractedFact(fact, 3)
	require.Len(t, split, 2)
	assert.Equal(t, "preference", split[0].Category)
	assert.Equal(t, core.UserFactLifetimeDurable, core.EffectiveUserFactLifetime(split[0]))
	assert.Equal(t, "context", split[1].Category)
	assert.Equal(t, core.UserFactLifetimeTransient, core.EffectiveUserFactLifetime(split[1]))
}

func TestSplitMixedExtractedFact_DoesNotRewriteIdentityFacts(t *testing.T) {
	fact := core.UserFact{
		Category:   "identity",
		Content:    "User lives in Coppell, TX and prefers DFW airport",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
	}

	split := splitMixedExtractedFact(fact, 3)
	require.Len(t, split, 1)
	assert.Equal(t, "identity", split[0].Category)
	assert.Equal(t, "User lives in Coppell, TX and prefers DFW airport", split[0].Content)
}

func TestSplitMixedExtractedFact_AssignsLifetimePerClause(t *testing.T) {
	fact := core.UserFact{
		Category:   "context",
		Content:    "User prefers public transport; User is planning a move next month",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
	}

	split := splitMixedExtractedFact(fact, 3)
	require.Len(t, split, 2)
	require.NotNil(t, split[0].Metadata)
	require.NotNil(t, split[1].Metadata)
	assert.Equal(t, core.UserFactLifetimeDurable, split[0].Metadata[core.UserFactMetadataLifetimeKey])
	assert.Equal(t, core.UserFactLifetimeTransient, split[1].Metadata[core.UserFactMetadataLifetimeKey])
}

func TestPreferCompactDurableMerge_ChoosesShorterSubsumingFact(t *testing.T) {
	existing := core.UserFact{
		Category: "preference",
		Content:  "User prefers DFW airport; User prefers DFW airport",
		Metadata: map[string]string{core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable},
	}
	merged := core.UserFact{
		Category: "preference",
		Content:  "User prefers DFW airport",
		Metadata: map[string]string{core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable},
	}

	result := preferCompactDurableMerge(existing, merged)
	assert.Equal(t, "User prefers DFW airport", result.Content)
}

func TestPreferCompactDurableMerge_PreservesAdditiveDurableDetail(t *testing.T) {
	existing := core.UserFact{
		Category: "preference",
		Content:  "User prefers DFW airport",
		Metadata: map[string]string{core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable},
	}
	merged := core.UserFact{
		Category: "preference",
		Content:  "User prefers DFW airport; User prefers public transport",
		Metadata: map[string]string{core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable},
	}

	result := preferCompactDurableMerge(existing, merged)
	assert.Contains(t, strings.ToLower(result.Content), "dfw airport")
	assert.Contains(t, strings.ToLower(result.Content), "public transport")
}

func TestUserMemoryExtractionHook_CompactionPathIsObservable(t *testing.T) {
	recorder, tracer := setupUserMemoryHookTestTracer(t)

	mem := newTestUserMemory()
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User prefers DFW airport; User prefers public transport", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &splitSiblingReconciler{targetFactID: "pref-1"}
	require.NoError(t, mem.Remember(context.Background(), "user-1", core.UserFact{
		FactID:     "pref-1",
		Namespace:  "travel",
		Category:   "preference",
		Content:    "User prefers DFW airport",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable,
		},
	}))

	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, reconciler)
	pctx := &core.PipelineContext{
		Request:  "Remember that I prefer DFW and public transport",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	ctx, span := tracer.Start(WithRequestID(context.Background(), "req-compaction-1"), "test-compaction")
	resp, err := hook.AfterSynthesis(ctx, pctx, "Noted.")
	require.NoError(t, err)
	assert.Equal(t, "Noted.", resp)
	span.End()

	spans := recorder.Ended()
	require.NotEmpty(t, spans)

	// extractAndStore now opens its own user_memory.extraction child span, so
	// compaction events live on that span — not on the outer test span. Walk
	// every recorded span looking for the events.
	var sawSplit, sawDeduped bool
	for _, sp := range spans {
		for _, event := range sp.Events() {
			switch event.Name {
			case "user_memory.compaction.split":
				sawSplit = true
				assertTraceEventStringAttr(t, event.Attributes, "request_id", "req-compaction-1")
			case "user_memory.compaction.deduped":
				sawDeduped = true
				assertTraceEventStringAttr(t, event.Attributes, "request_id", "req-compaction-1")
			}
		}
	}
	assert.True(t, sawSplit, "expected compaction split event")
	assert.True(t, sawDeduped, "expected compaction dedupe event")
}

// ─── LLMUserFactReconciler Tests ────────────────────────────────────────────

func TestLLMUserFactReconciler_ADDWhenNoExisting(t *testing.T) {
	// Already tested in TestReconcileResult_ADDForNoExisting, but verify no LLM call
	callCount := 0
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		callCount++
		return &core.AIResponse{Content: "{}"}, nil
	}}

	reconciler := NewLLMUserFactReconciler(nil, mockAI, "", 0.75, nil)
	candidate := core.UserFact{Content: "New fact", Source: core.SourceExplicit}
	result, err := reconciler.Reconcile(context.Background(), "user-1", "ns", candidate, nil)

	require.NoError(t, err)
	assert.Equal(t, "ADD", result.Operation)
	assert.Equal(t, 0, callCount, "should NOT call LLM when no existing facts")
	assert.Nil(t, result.Response, "should not populate Response when no LLM call is made")
}

func TestLLMUserFactReconciler_ADDWhenEmptyExisting(t *testing.T) {
	reconciler := NewLLMUserFactReconciler(nil, nil, "", 0.75, nil)
	candidate := core.UserFact{Content: "New fact", Source: core.SourceExplicit}
	result, err := reconciler.Reconcile(context.Background(), "user-1", "ns", candidate, []core.UserFact{})

	require.NoError(t, err)
	assert.Equal(t, "ADD", result.Operation)
	assert.Nil(t, result.Response, "should not populate Response when no LLM call is made")
}

// ─── Test doubles ────────────────────────────────────────────────────────────

// newTestUserMemory creates an InMemoryUserMemory for testing (imported from memory
// module via the core.UserMemory interface — tests use a local implementation to
// avoid circular imports).
func newTestUserMemory() *testUserMemory {
	return &testUserMemory{facts: make(map[string][]core.UserFact)}
}

// testUserMemory is guarded by an RWMutex so tests exercising the async
// extraction path can safely read facts while the background goroutine is
// still writing. Production UserMemory implementations (VectorUserMemory) are
// thread-safe; this mirrors that contract for the in-memory helper.

func newSelectiveRecallMemory() *selectiveRecallMemory {
	return &selectiveRecallMemory{
		testUserMemory: newTestUserMemory(),
		queryResults:   make(map[string][]core.UserFact),
	}
}

type testUserMemory struct {
	mu    sync.RWMutex
	facts map[string][]core.UserFact
}

type selectiveRecallMemory struct {
	*testUserMemory
	queryResults map[string][]core.UserFact
}

func (m *testUserMemory) Remember(ctx context.Context, userID string, fact core.UserFact) error {
	fact.UserID = userID
	if fact.FactID == "" {
		fact.FactID = time.Now().String()
	}
	now := time.Now()
	if fact.CreatedAt.IsZero() {
		fact.CreatedAt = now
	}
	fact.UpdatedAt = now

	m.mu.Lock()
	defer m.mu.Unlock()
	// Upsert
	for i, existing := range m.facts[userID] {
		if existing.FactID == fact.FactID {
			m.facts[userID][i] = fact
			return nil
		}
	}
	m.facts[userID] = append(m.facts[userID], fact)
	return nil
}

func (m *testUserMemory) Recall(ctx context.Context, userID string, namespace string, queryContext string, limit int) ([]core.UserFact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var results []core.UserFact
	for _, fact := range m.facts[userID] {
		if namespace != "" && fact.Namespace != namespace {
			continue
		}
		results = append(results, fact)
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (m *selectiveRecallMemory) Recall(ctx context.Context, userID string, namespace string, queryContext string, limit int) ([]core.UserFact, error) {
	results := m.queryResults[namespace]
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (m *testUserMemory) RecallByCategory(ctx context.Context, userID string, namespace string, category string, limit int) ([]core.UserFact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var results []core.UserFact
	for _, fact := range m.facts[userID] {
		if namespace != "" && fact.Namespace != namespace {
			continue
		}
		if fact.Category == category {
			results = append(results, fact)
		}
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (m *testUserMemory) Forget(ctx context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.facts, userID)
	return nil
}

func (m *testUserMemory) ForgetFact(ctx context.Context, userID string, factID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var kept []core.UserFact
	for _, f := range m.facts[userID] {
		if f.FactID != factID {
			kept = append(kept, f)
		}
	}
	m.facts[userID] = kept
	return nil
}

func (m *testUserMemory) ListFacts(ctx context.Context, userID string, namespace string, offset int, limit int) ([]core.UserFact, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var filtered []core.UserFact
	for _, f := range m.facts[userID] {
		if namespace != "" && f.Namespace != namespace {
			continue
		}
		filtered = append(filtered, f)
	}
	total := len(filtered)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total, nil
}

func (m *testUserMemory) ForgetNamespace(ctx context.Context, userID string, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var kept []core.UserFact
	for _, f := range m.facts[userID] {
		if f.Namespace != namespace {
			kept = append(kept, f)
		}
	}
	m.facts[userID] = kept
	return nil
}

// Compile-time check: testUserMemory implements UserMemoryAdmin
var _ core.UserMemoryAdmin = (*testUserMemory)(nil)

// staticFactExtractor returns pre-defined facts (for testing).
type staticFactExtractor struct {
	facts []core.UserFact
}

func (e *staticFactExtractor) ExtractFacts(ctx context.Context, userRequest string, agentResponse string, corrections []string) (*ExtractResult, error) {
	return &ExtractResult{Facts: e.facts}, nil
}

type blockingFactExtractor struct {
	started chan struct{}
	release chan struct{}
	facts   []core.UserFact
	once    sync.Once
}

func (e *blockingFactExtractor) ExtractFacts(ctx context.Context, userRequest string, agentResponse string, corrections []string) (*ExtractResult, error) {
	e.once.Do(func() {
		if e.started != nil {
			close(e.started)
		}
	})
	if e.release != nil {
		<-e.release
	}
	return &ExtractResult{Facts: e.facts}, nil
}

type staticPersistencePolicy struct {
	decision UserFactPersistenceDecision
	err      error
}

func (p staticPersistencePolicy) Evaluate(ctx context.Context, input UserFactPersistenceInput) (UserFactPersistenceDecision, error) {
	if p.err != nil {
		return UserFactPersistenceDecision{}, p.err
	}
	return p.decision, nil
}

func setupUserMemoryHookTestTracer(t *testing.T) (*tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})
	return recorder, tp.Tracer("user-memory-hook-test")
}

func assertTraceEventStringAttr(t *testing.T, attrs []attribute.KeyValue, key string, want string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			assert.Equal(t, want, attr.Value.AsString())
			return
		}
	}
	t.Fatalf("missing attribute %s", key)
}

// failingFactExtractor always returns an error (for fail-open testing).
type failingFactExtractor struct{}

func (e *failingFactExtractor) ExtractFacts(ctx context.Context, userRequest string, agentResponse string, corrections []string) (*ExtractResult, error) {
	return nil, fmt.Errorf("extraction failed")
}

// addAllReconciler always returns ADD (for testing).
type addAllReconciler struct{}

func (r *addAllReconciler) Reconcile(ctx context.Context, userID string, namespace string, candidate core.UserFact, existing []core.UserFact) (ReconcileResult, error) {
	if candidate.FactID == "" {
		candidate.FactID = time.Now().String()
	}
	return ReconcileResult{Operation: "ADD", MergedFact: candidate}, nil
}

// contradictReconciler always returns CONTRADICT (for testing).
type contradictReconciler struct {
	targetFactID string
}

func (r *contradictReconciler) Reconcile(ctx context.Context, userID string, namespace string, candidate core.UserFact, existing []core.UserFact) (ReconcileResult, error) {
	if candidate.FactID == "" {
		candidate.FactID = time.Now().String()
	}
	return ReconcileResult{
		Operation:    "CONTRADICT",
		TargetFactID: r.targetFactID,
		MergedFact:   candidate,
	}, nil
}

type updateReconciler struct {
	targetFactID  string
	mergedContent string
}

func (r *updateReconciler) Reconcile(ctx context.Context, userID string, namespace string, candidate core.UserFact, existing []core.UserFact) (ReconcileResult, error) {
	candidate.FactID = r.targetFactID
	candidate.Content = r.mergedContent
	return ReconcileResult{
		Operation:    "UPDATE",
		TargetFactID: r.targetFactID,
		MergedFact:   candidate,
	}, nil
}

type splitSiblingReconciler struct {
	targetFactID string
}

func (r *splitSiblingReconciler) Reconcile(ctx context.Context, userID string, namespace string, candidate core.UserFact, existing []core.UserFact) (ReconcileResult, error) {
	lower := strings.ToLower(candidate.Content)
	if strings.Contains(lower, "dfw airport") {
		candidate.FactID = r.targetFactID
		candidate.Content = "User prefers DFW airport; User prefers public transport"
		return ReconcileResult{
			Operation:    "UPDATE",
			TargetFactID: r.targetFactID,
			MergedFact:   candidate,
		}, nil
	}
	if candidate.FactID == "" {
		candidate.FactID = time.Now().String()
	}
	return ReconcileResult{Operation: "ADD", MergedFact: candidate}, nil
}
