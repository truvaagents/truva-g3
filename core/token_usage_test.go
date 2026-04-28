package core

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AggregatedTokenUsage struct tests
// =============================================================================

func TestNewAggregatedTokenUsage(t *testing.T) {
	acc := NewAggregatedTokenUsage()

	require.NotNil(t, acc)
	assert.Equal(t, TokenUsage{}, acc.Total)
	assert.NotNil(t, acc.ByPhase)
	assert.Empty(t, acc.ByPhase)
}

func TestAggregatedTokenUsage_Add_SinglePhase(t *testing.T) {
	acc := NewAggregatedTokenUsage()

	acc.Add("planning", TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})

	assert.Equal(t, TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, acc.Total)
	assert.Equal(t, TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, acc.ByPhase["planning"])
}

func TestAggregatedTokenUsage_Add_SamePhaseAccumulates(t *testing.T) {
	acc := NewAggregatedTokenUsage()

	acc.Add("planning", TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	acc.Add("planning", TokenUsage{PromptTokens: 200, CompletionTokens: 30, TotalTokens: 230})

	assert.Equal(t, TokenUsage{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}, acc.Total)
	assert.Equal(t, TokenUsage{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}, acc.ByPhase["planning"])
}

func TestAggregatedTokenUsage_Add_MultiplePhases(t *testing.T) {
	acc := NewAggregatedTokenUsage()

	acc.Add("planning", TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	acc.Add("micro_resolution", TokenUsage{PromptTokens: 500, CompletionTokens: 10, TotalTokens: 510})
	acc.Add("synthesis", TokenUsage{PromptTokens: 2000, CompletionTokens: 300, TotalTokens: 2300})

	// Total is sum of all phases
	assert.Equal(t, TokenUsage{PromptTokens: 2600, CompletionTokens: 330, TotalTokens: 2930}, acc.Total)

	// Each phase has its own breakdown
	assert.Equal(t, TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, acc.ByPhase["planning"])
	assert.Equal(t, TokenUsage{PromptTokens: 500, CompletionTokens: 10, TotalTokens: 510}, acc.ByPhase["micro_resolution"])
	assert.Equal(t, TokenUsage{PromptTokens: 2000, CompletionTokens: 300, TotalTokens: 2300}, acc.ByPhase["synthesis"])
}

func TestAggregatedTokenUsage_Add_ZeroUsage(t *testing.T) {
	acc := NewAggregatedTokenUsage()

	acc.Add("distillation", TokenUsage{})

	assert.Equal(t, TokenUsage{}, acc.Total)
	// Phase entry is created even with zero values
	_, exists := acc.ByPhase["distillation"]
	assert.True(t, exists)
}

func TestAggregatedTokenUsage_Snapshot_ReturnsCopy(t *testing.T) {
	acc := NewAggregatedTokenUsage()
	acc.Add("planning", TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})

	total, byPhase := acc.Snapshot()

	// Verify values
	assert.Equal(t, TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, total)
	assert.Equal(t, TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, byPhase["planning"])

	// Mutate the returned map — original should be unaffected
	byPhase["planning"] = TokenUsage{PromptTokens: 999}
	byPhase["injected"] = TokenUsage{PromptTokens: 1}

	total2, byPhase2 := acc.Snapshot()
	assert.Equal(t, TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, total2)
	assert.Equal(t, TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, byPhase2["planning"])
	_, hasInjected := byPhase2["injected"]
	assert.False(t, hasInjected)
}

func TestAggregatedTokenUsage_Snapshot_EmptyAccumulator(t *testing.T) {
	acc := NewAggregatedTokenUsage()

	total, byPhase := acc.Snapshot()

	assert.Equal(t, TokenUsage{}, total)
	assert.Empty(t, byPhase)
}

func TestAggregatedTokenUsage_Snapshot_AfterMoreAdds(t *testing.T) {
	acc := NewAggregatedTokenUsage()
	acc.Add("planning", TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})

	// First snapshot
	total1, _ := acc.Snapshot()
	assert.Equal(t, 100, total1.PromptTokens)

	// Add more after snapshot
	acc.Add("synthesis", TokenUsage{PromptTokens: 500, CompletionTokens: 50, TotalTokens: 550})

	// Second snapshot reflects new data
	total2, byPhase2 := acc.Snapshot()
	assert.Equal(t, 600, total2.PromptTokens)
	assert.Len(t, byPhase2, 2)
}

func TestAggregatedTokenUsage_Add_ConcurrentSafety(t *testing.T) {
	acc := NewAggregatedTokenUsage()
	const goroutines = 50
	const addsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			phase := "phase_a"
			if id%2 == 1 {
				phase = "phase_b"
			}
			for j := 0; j < addsPerGoroutine; j++ {
				acc.Add(phase, TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})
			}
		}(i)
	}

	wg.Wait()

	total, byPhase := acc.Snapshot()

	expectedTotal := goroutines * addsPerGoroutine
	assert.Equal(t, expectedTotal, total.PromptTokens)
	assert.Equal(t, expectedTotal, total.CompletionTokens)
	assert.Equal(t, expectedTotal*2, total.TotalTokens)

	// 25 goroutines write phase_a, 25 write phase_b
	expectedPerPhase := (goroutines / 2) * addsPerGoroutine
	assert.Equal(t, expectedPerPhase, byPhase["phase_a"].PromptTokens)
	assert.Equal(t, expectedPerPhase, byPhase["phase_b"].PromptTokens)
}

func TestAggregatedTokenUsage_Snapshot_ConcurrentWithAdd(t *testing.T) {
	acc := NewAggregatedTokenUsage()
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer goroutine
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			acc.Add("planning", TokenUsage{PromptTokens: 1, CompletionTokens: 0, TotalTokens: 1})
		}
	}()

	// Reader goroutine
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			total, byPhase := acc.Snapshot()
			// Total and per-phase should always be consistent
			assert.Equal(t, total.PromptTokens, byPhase["planning"].PromptTokens)
		}
	}()

	wg.Wait()

	// Final state should have all adds
	total, _ := acc.Snapshot()
	assert.Equal(t, iterations, total.PromptTokens)
}

// =============================================================================
// Context helper tests
// =============================================================================

func TestWithTokenUsageAccumulator(t *testing.T) {
	ctx := context.Background()

	ctx, acc := WithTokenUsageAccumulator(ctx)

	require.NotNil(t, acc)
	// Accumulator should be retrievable from context
	retrieved := GetTokenUsageAccumulator(ctx)
	assert.Same(t, acc, retrieved)
}

func TestGetTokenUsageAccumulator_NotSet(t *testing.T) {
	ctx := context.Background()

	acc := GetTokenUsageAccumulator(ctx)

	assert.Nil(t, acc)
}

func TestGetTokenUsageAccumulator_WrongType(t *testing.T) {
	// Simulate a context with the right key but wrong type (defensive check)
	ctx := context.WithValue(context.Background(), contextKeyTokenUsageAccumulator, "not-an-accumulator")

	acc := GetTokenUsageAccumulator(ctx)

	assert.Nil(t, acc)
}

func TestRecordTokenUsage_WithAccumulator(t *testing.T) {
	ctx, acc := WithTokenUsageAccumulator(context.Background())

	RecordTokenUsage(ctx, "planning", TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	RecordTokenUsage(ctx, "synthesis", TokenUsage{PromptTokens: 500, CompletionTokens: 50, TotalTokens: 550})

	total, byPhase := acc.Snapshot()
	assert.Equal(t, 600, total.PromptTokens)
	assert.Equal(t, 70, total.CompletionTokens)
	assert.Equal(t, 670, total.TotalTokens)
	assert.Len(t, byPhase, 2)
	assert.Equal(t, 100, byPhase["planning"].PromptTokens)
	assert.Equal(t, 500, byPhase["synthesis"].PromptTokens)
}

func TestRecordTokenUsage_WithoutAccumulator_NoOp(t *testing.T) {
	ctx := context.Background()

	// Should not panic — silent no-op
	RecordTokenUsage(ctx, "planning", TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
}

func TestRecordTokenUsage_SamePhaseAccumulates(t *testing.T) {
	ctx, acc := WithTokenUsageAccumulator(context.Background())

	RecordTokenUsage(ctx, "planning", TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	RecordTokenUsage(ctx, "planning", TokenUsage{PromptTokens: 200, CompletionTokens: 30, TotalTokens: 230})

	total, byPhase := acc.Snapshot()
	assert.Equal(t, 300, total.PromptTokens)
	assert.Equal(t, 300, byPhase["planning"].PromptTokens)
}

func TestRecordTokenUsage_ZeroUsage(t *testing.T) {
	ctx, acc := WithTokenUsageAccumulator(context.Background())

	RecordTokenUsage(ctx, "distillation", TokenUsage{})

	total, byPhase := acc.Snapshot()
	assert.Equal(t, TokenUsage{}, total)
	_, exists := byPhase["distillation"]
	assert.True(t, exists)
}

func TestWithTokenUsageAccumulator_DoesNotAffectOtherContextKeys(t *testing.T) {
	ctx := context.Background()
	ctx = WithRequestID(ctx, "req-123")
	ctx = WithStepID(ctx, "step-5")
	ctx = WithPhaseNumber(ctx, 2)

	ctx, acc := WithTokenUsageAccumulator(ctx)

	// Token usage accumulator should be set
	require.NotNil(t, acc)

	// Other context values should be preserved
	assert.Equal(t, "req-123", GetRequestID(ctx))
	assert.Equal(t, "step-5", GetStepID(ctx))
	assert.Equal(t, 2, GetPhaseNumber(ctx))
}
