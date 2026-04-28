package orchestration

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── LLMUserFactReconciler.ReconcileBatch tests ─────────────────────────────

// TestReconcileBatch_AllNoNeighbors_NoLLMCall verifies the cost-optimisation
// short-circuit: when every candidate has zero neighbors, the batched path
// resolves all of them to ADD without calling the LLM at all.
func TestReconcileBatch_AllNoNeighbors_NoLLMCall(t *testing.T) {
	var calls int32
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		atomic.AddInt32(&calls, 1)
		return &core.AIResponse{Content: "[]"}, nil
	}}
	r := NewLLMUserFactReconciler(nil, mockAI, "", 0.75, nil)

	candidates := []core.UserFact{
		{Content: "Likes coffee", Source: core.SourceExplicit},
		{Content: "From Coppell, TX", Source: core.SourceExplicit},
		{Content: "Speaks English", Source: core.SourceExplicit},
	}
	neighbors := [][]core.UserFact{nil, {}, nil}

	results, err := r.ReconcileBatch(context.Background(), "user-1", "ns", candidates, neighbors)
	require.NoError(t, err)
	require.Len(t, results, 3)
	for i, res := range results {
		assert.Equal(t, "ADD", res.Operation, "candidate %d", i)
		assert.Nil(t, res.Response, "no LLM call → no Response metadata")
	}
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls), "must NOT invoke LLM when no neighbors")
}

// TestReconcileBatch_SingleLLMCall_ForMultipleCandidates verifies the core
// optimisation: 7 candidates with neighbors should produce exactly ONE
// LLM invocation, not 7.
func TestReconcileBatch_SingleLLMCall_ForMultipleCandidates(t *testing.T) {
	var calls int32
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		atomic.AddInt32(&calls, 1)
		// Verify the prompt contains all 3 candidates and asks for "exactly 3 objects".
		assert.Contains(t, prompt, "1. New fact:")
		assert.Contains(t, prompt, "2. New fact:")
		assert.Contains(t, prompt, "3. New fact:")
		assert.Contains(t, prompt, "exactly 3 objects")
		return &core.AIResponse{
			Content: `[
				{"candidate":1,"operation":"UPDATE","target_fact_id":"f1","merged_content":"updated 1"},
				{"candidate":2,"operation":"DUPLICATE","target_fact_id":"f2"},
				{"candidate":3,"operation":"ADD"}
			]`,
			Model:    "gpt-4o-mini",
			Provider: "openai",
			Usage:    core.TokenUsage{PromptTokens: 1800, CompletionTokens: 400, TotalTokens: 2200},
		}, nil
	}}
	r := NewLLMUserFactReconciler(nil, mockAI, "", 0.75, nil)

	candidates := []core.UserFact{
		{Content: "Wants Japan trip", Source: core.SourceExplicit},
		{Content: "Lives in Coppell", Source: core.SourceExplicit},
		{Content: "New fact about beaches", Source: core.SourceExplicit},
	}
	neighbors := [][]core.UserFact{
		{{FactID: "f1", Content: "Travel: Japan", Confidence: 0.9}},
		{{FactID: "f2", Content: "Lives in Coppell, TX", Confidence: 0.95}},
		{{FactID: "f3", Content: "Travel: Tokyo", Confidence: 0.8}},
	}

	results, err := r.ReconcileBatch(context.Background(), "user-1", "ns", candidates, neighbors)
	require.NoError(t, err)
	require.Len(t, results, 3)

	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "must invoke LLM exactly once for 3 candidates")

	assert.Equal(t, "UPDATE", results[0].Operation)
	assert.Equal(t, "f1", results[0].TargetFactID)
	assert.Equal(t, "updated 1", results[0].MergedFact.Content)
	assert.Equal(t, "f1", results[0].MergedFact.FactID, "UPDATE reuses existing fact ID for upsert")

	assert.Equal(t, "DUPLICATE", results[1].Operation)
	assert.Equal(t, "f2", results[1].TargetFactID)

	assert.Equal(t, "ADD", results[2].Operation)
	assert.NotEmpty(t, results[2].MergedFact.FactID, "ADD must assign a fresh fact ID")

	// All results carry shared metadata so per-candidate debug attribution works.
	for i, res := range results {
		require.NotNil(t, res.Response, "result %d should carry batch response metadata", i)
		assert.Equal(t, "gpt-4o-mini", res.Response.Model)
		assert.Equal(t, 2200, res.Response.Usage.TotalTokens)
	}
}

// TestReconcileBatch_MixedNeighbors_OnlyLLMForEligible verifies that candidates
// with empty neighbors are pre-resolved to ADD locally and DON'T appear in the
// LLM prompt, while neighbor-having candidates do.
func TestReconcileBatch_MixedNeighbors_OnlyLLMForEligible(t *testing.T) {
	var capturedPrompt string
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		capturedPrompt = prompt
		return &core.AIResponse{
			Content: `[{"candidate":1,"operation":"DUPLICATE","target_fact_id":"f1"}]`,
		}, nil
	}}
	r := NewLLMUserFactReconciler(nil, mockAI, "", 0.75, nil)

	candidates := []core.UserFact{
		{Content: "Brand new fact A", Source: core.SourceExplicit},  // no neighbors → local ADD
		{Content: "Existing-ish fact", Source: core.SourceExplicit}, // has neighbor → LLM
		{Content: "Brand new fact B", Source: core.SourceExplicit},  // no neighbors → local ADD
	}
	neighbors := [][]core.UserFact{
		nil,
		{{FactID: "f1", Content: "Existing fact", Confidence: 0.9}},
		nil,
	}

	results, err := r.ReconcileBatch(context.Background(), "user-1", "ns", candidates, neighbors)
	require.NoError(t, err)
	require.Len(t, results, 3)

	assert.Equal(t, "ADD", results[0].Operation)
	assert.Equal(t, "DUPLICATE", results[1].Operation)
	assert.Equal(t, "ADD", results[2].Operation)

	// Prompt only contains the eligible candidate (numbered as 1, the only one).
	assert.Contains(t, capturedPrompt, "1. New fact: \"Existing-ish fact\"")
	assert.NotContains(t, capturedPrompt, "Brand new fact A")
	assert.NotContains(t, capturedPrompt, "Brand new fact B")
	assert.Contains(t, capturedPrompt, "exactly 1 objects")
}

// TestReconcileBatch_LengthMismatch_ReturnsError verifies that a malformed
// LLM response (wrong number of decisions) bubbles up an error so the caller
// can fall back to per-candidate reconciliation.
func TestReconcileBatch_LengthMismatch_ReturnsError(t *testing.T) {
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		// 3 candidates expected, only 2 returned
		return &core.AIResponse{
			Content: `[
				{"candidate":1,"operation":"ADD"},
				{"candidate":2,"operation":"ADD"}
			]`,
		}, nil
	}}
	r := NewLLMUserFactReconciler(nil, mockAI, "", 0.75, nil)

	candidates := []core.UserFact{
		{Content: "A", Source: core.SourceExplicit},
		{Content: "B", Source: core.SourceExplicit},
		{Content: "C", Source: core.SourceExplicit},
	}
	neighbors := [][]core.UserFact{
		{{FactID: "f1", Content: "x"}},
		{{FactID: "f2", Content: "y"}},
		{{FactID: "f3", Content: "z"}},
	}

	results, err := r.ReconcileBatch(context.Background(), "u", "ns", candidates, neighbors)
	require.Error(t, err)
	assert.Nil(t, results)
	assert.Contains(t, err.Error(), "expected 3 decisions, got 2")
}

// TestReconcileBatch_ParseFailure_ReturnsError verifies that invalid JSON
// from the LLM bubbles up so the caller can fall back.
func TestReconcileBatch_ParseFailure_ReturnsError(t *testing.T) {
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: "not valid json"}, nil
	}}
	r := NewLLMUserFactReconciler(nil, mockAI, "", 0.75, nil)

	candidates := []core.UserFact{{Content: "A", Source: core.SourceExplicit}}
	neighbors := [][]core.UserFact{{{FactID: "f1", Content: "x"}}}

	results, err := r.ReconcileBatch(context.Background(), "u", "ns", candidates, neighbors)
	require.Error(t, err)
	assert.Nil(t, results)
	assert.Contains(t, err.Error(), "failed to parse LLM response")
}

// TestReconcileBatch_LLMError_ReturnsError verifies provider errors propagate
// so the caller can fall back to per-candidate reconciliation.
func TestReconcileBatch_LLMError_ReturnsError(t *testing.T) {
	mockAI := &mockAIClient{generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		return nil, fmt.Errorf("upstream timeout")
	}}
	r := NewLLMUserFactReconciler(nil, mockAI, "", 0.75, nil)

	candidates := []core.UserFact{{Content: "A", Source: core.SourceExplicit}}
	neighbors := [][]core.UserFact{{{FactID: "f1", Content: "x"}}}

	results, err := r.ReconcileBatch(context.Background(), "u", "ns", candidates, neighbors)
	require.Error(t, err)
	assert.Nil(t, results)
	assert.Contains(t, err.Error(), "batched reconciliation LLM call failed")
}

// TestReconcileBatch_AlignmentMismatch_ReturnsError verifies that the contract
// is enforced: candidates and neighbors slices must be the same length.
func TestReconcileBatch_AlignmentMismatch_ReturnsError(t *testing.T) {
	r := NewLLMUserFactReconciler(nil, nil, "", 0.75, nil)
	candidates := []core.UserFact{{Content: "A"}, {Content: "B"}}
	neighbors := [][]core.UserFact{{{FactID: "f1"}}}

	results, err := r.ReconcileBatch(context.Background(), "u", "ns", candidates, neighbors)
	require.Error(t, err)
	assert.Nil(t, results)
	assert.Contains(t, err.Error(), "length mismatch")
}

// TestReconcileBatch_EmptyCandidates_ReturnsEmpty verifies the trivial
// no-work case.
func TestReconcileBatch_EmptyCandidates_ReturnsEmpty(t *testing.T) {
	r := NewLLMUserFactReconciler(nil, nil, "", 0.75, nil)
	results, err := r.ReconcileBatch(context.Background(), "u", "ns", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// TestReconcileBatch_NoAIClient_AllADD verifies graceful degradation when
// no AI client is configured: every candidate (regardless of neighbors)
// becomes ADD, mirroring the per-candidate Reconcile fallback.
func TestReconcileBatch_NoAIClient_AllADD(t *testing.T) {
	r := NewLLMUserFactReconciler(nil, nil, "", 0.75, nil)
	candidates := []core.UserFact{
		{Content: "A", Source: core.SourceExplicit},
		{Content: "B", Source: core.SourceExplicit},
	}
	neighbors := [][]core.UserFact{
		{{FactID: "f1", Content: "x"}}, // has neighbors but no AI → ADD
		nil,
	}
	results, err := r.ReconcileBatch(context.Background(), "u", "ns", candidates, neighbors)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "ADD", results[0].Operation)
	assert.Equal(t, "ADD", results[1].Operation)
}

// ─── Extraction hook integration tests (batched path wired through) ─────────

// TestExtractionHook_UsesBatchedPath_OneLLMCallForMultipleCandidates is the
// end-to-end check: the hook receives 3 candidates, each with existing
// neighbors, and the underlying reconciler must be invoked exactly ONCE
// (via ReconcileBatch), not three times (via Reconcile).
func TestExtractionHook_UsesBatchedPath_OneLLMCallForMultipleCandidates(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()
	// Pre-load existing facts so the per-candidate Recall returns neighbors.
	for _, f := range []core.UserFact{
		{FactID: "f1", Namespace: "travel", Content: "Travel: Tokyo", Confidence: 0.9},
		{FactID: "f2", Namespace: "travel", Content: "Lives in Coppell", Confidence: 0.95},
		{FactID: "f3", Namespace: "travel", Content: "Speaks English", Confidence: 0.9},
	} {
		_ = mem.Remember(ctx, "user-1", f)
	}

	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "Wants Japan trip", Category: "preference", Source: core.SourceExplicit, Confidence: 0.9},
		{Content: "Lives in Coppell, TX", Category: "identity", Source: core.SourceExplicit, Confidence: 0.95},
		{Content: "Speaks English fluently", Category: "identity", Source: core.SourceExplicit, Confidence: 0.9},
	}}

	rec := &countingBatchReconciler{
		batchResponse: []ReconcileResult{
			{Operation: "DUPLICATE", TargetFactID: "f1", MergedFact: core.UserFact{FactID: "f1", Namespace: "travel"}},
			{Operation: "DUPLICATE", TargetFactID: "f2", MergedFact: core.UserFact{FactID: "f2", Namespace: "travel"}},
			{Operation: "DUPLICATE", TargetFactID: "f3", MergedFact: core.UserFact{FactID: "f3", Namespace: "travel"}},
		},
	}

	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, rec)

	pctx := &core.PipelineContext{
		Request:  "Do you remember our travel discussion?",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, err := hook.AfterSynthesis(ctx, pctx, "yes I remember")
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&rec.batchCalls), "ReconcileBatch must be called exactly once")
	assert.Equal(t, int32(0), atomic.LoadInt32(&rec.singleCalls), "per-candidate Reconcile must NOT be called when batch succeeds")
	assert.Equal(t, 3, rec.lastBatchSize, "batch must include all 3 candidates")
}

// TestExtractionHook_FallsBackToPerCandidate_OnBatchFailure verifies the
// safety net: when the batched path returns an error, the hook falls back
// to per-candidate Reconcile and processes everything correctly.
func TestExtractionHook_FallsBackToPerCandidate_OnBatchFailure(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Content: "Existing", Confidence: 0.9,
	})

	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "Fact A", Category: "preference", Source: core.SourceExplicit, Confidence: 0.9},
		{Content: "Fact B", Category: "preference", Source: core.SourceExplicit, Confidence: 0.9},
	}}

	rec := &countingBatchReconciler{
		batchErr: fmt.Errorf("simulated batch parse failure"),
	}

	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, rec)

	pctx := &core.PipelineContext{
		Request:  "test",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, err := hook.AfterSynthesis(ctx, pctx, "ok")
	require.NoError(t, err, "extraction hook fails open even on reconciliation failure")

	assert.Equal(t, int32(1), atomic.LoadInt32(&rec.batchCalls), "batch attempted once")
	assert.Equal(t, int32(2), atomic.LoadInt32(&rec.singleCalls), "fell back to per-candidate Reconcile for both")
}

// TestExtractionHook_FallsBackToPerCandidate_OnLengthMismatch verifies that
// when ReconcileBatch returns the wrong number of results (no error), the
// hook still falls back rather than panicking or applying garbage.
func TestExtractionHook_FallsBackToPerCandidate_OnLengthMismatch(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Content: "Existing", Confidence: 0.9,
	})

	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "Fact A", Category: "preference", Source: core.SourceExplicit, Confidence: 0.9},
		{Content: "Fact B", Category: "preference", Source: core.SourceExplicit, Confidence: 0.9},
	}}

	rec := &countingBatchReconciler{
		batchResponse: []ReconcileResult{
			{Operation: "ADD", MergedFact: core.UserFact{FactID: "wrong"}},
			// only 1 result for 2 candidates → length mismatch
		},
	}

	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, rec)

	pctx := &core.PipelineContext{
		Request:  "test",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, err := hook.AfterSynthesis(ctx, pctx, "ok")
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&rec.batchCalls))
	assert.Equal(t, int32(2), atomic.LoadInt32(&rec.singleCalls), "fell back per-candidate after length mismatch")
}

// TestExtractionHook_UsesPerCandidate_WhenReconcilerHasNoBatchInterface
// verifies backward compatibility: a custom reconciler that does NOT
// implement BatchUserFactReconciler still works via the per-candidate path.
func TestExtractionHook_UsesPerCandidate_WhenReconcilerHasNoBatchInterface(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()
	_ = mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Content: "Existing", Confidence: 0.9,
	})

	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "Fact A", Category: "preference", Source: core.SourceExplicit, Confidence: 0.9},
		{Content: "Fact B", Category: "preference", Source: core.SourceExplicit, Confidence: 0.9},
	}}

	// addAllReconciler implements UserFactReconciler but NOT BatchUserFactReconciler.
	rec := &addAllReconciler{}
	hook := NewUserMemoryExtractionHook(mem, nil, nil, "travel", &core.NoOpLogger{}, extractor, rec)

	pctx := &core.PipelineContext{
		Request:  "test",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, err := hook.AfterSynthesis(ctx, pctx, "ok")
	require.NoError(t, err)

	// Both facts should have been stored (per-candidate path applies ADD).
	facts, _ := mem.Recall(ctx, "user-1", "travel", "", 10)
	// 1 pre-existing + 2 new = 3
	assert.Len(t, facts, 3)
}

// ─── Test doubles ────────────────────────────────────────────────────────────

// countingBatchReconciler implements BatchUserFactReconciler and counts both
// batch and per-candidate calls. Used to verify which path the hook chose.
type countingBatchReconciler struct {
	batchCalls    int32
	singleCalls   int32
	lastBatchSize int

	// batchResponse is returned from ReconcileBatch when batchErr is nil.
	// Length-mismatch testing: callers can deliberately set this shorter than
	// the candidates slice to exercise the fallback path.
	batchResponse []ReconcileResult
	batchErr      error
}

func (r *countingBatchReconciler) Reconcile(
	ctx context.Context,
	userID string,
	namespace string,
	candidate core.UserFact,
	existing []core.UserFact,
) (ReconcileResult, error) {
	atomic.AddInt32(&r.singleCalls, 1)
	// Per-candidate fallback always succeeds with ADD.
	candidate.Namespace = namespace
	if candidate.FactID == "" {
		candidate.FactID = fmt.Sprintf("single-%d", atomic.LoadInt32(&r.singleCalls))
	}
	return ReconcileResult{Operation: "ADD", MergedFact: candidate}, nil
}

func (r *countingBatchReconciler) ReconcileBatch(
	ctx context.Context,
	userID string,
	namespace string,
	candidates []core.UserFact,
	neighbors [][]core.UserFact,
) ([]ReconcileResult, error) {
	atomic.AddInt32(&r.batchCalls, 1)
	r.lastBatchSize = len(candidates)
	if r.batchErr != nil {
		return nil, r.batchErr
	}
	return r.batchResponse, nil
}

// Compile-time check
var _ BatchUserFactReconciler = (*countingBatchReconciler)(nil)
