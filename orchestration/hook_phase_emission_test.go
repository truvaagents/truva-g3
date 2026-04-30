package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// These tests lock the invariant that every LLMInteraction emitted by a
// pipeline-hook-scoped component carries the correct HookPhase. They are
// integration-style assertions — they exercise the public entrypoint of
// each component end-to-end and inspect the recorded interactions, rather
// than testing individual emit sites.
//
// Why invariant-style rather than per-site:
//   - New emit sites added to any of these components are covered
//     automatically without needing a new test.
//   - The test does not need maintenance when the number of sites changes.
//   - A dropped HookPhase tag (the actual failure mode we care about)
//     surfaces immediately via a loop assertion.
//
// If one of these tests fails, the most likely cause is a new
// LLMInteraction{...} literal that forgot HookPhase: HookPhasePre / Post.

// TestHookPhase_UserMemoryEnrichment_EmitsPre asserts every debug interaction
// emitted by UserMemoryEnrichmentHook.BeforePlanning carries HookPhasePre.
func TestHookPhase_UserMemoryEnrichment_EmitsPre(t *testing.T) {
	mem := newTestUserMemory()
	seedCtx := context.Background()
	// Seed facts across all five recall categories so as many emit sites as
	// possible fire in a single BeforePlanning call (identity, summary,
	// stable-namespace preference, query-relevant, universal).
	_ = mem.Remember(seedCtx, "u1", core.UserFact{FactID: "f1", Namespace: "universal", Category: "identity", Content: "User is Alex", Confidence: 0.9})
	_ = mem.Remember(seedCtx, "u1", core.UserFact{FactID: "f2", Namespace: "travel", Category: "summary", Content: "prior trip planning session", Confidence: 0.9})
	_ = mem.Remember(seedCtx, "u1", core.UserFact{FactID: "f3", Namespace: "travel", Category: "preference", Content: "prefers window seats", Confidence: 0.9})
	_ = mem.Remember(seedCtx, "u1", core.UserFact{FactID: "f4", Namespace: "universal", Category: "preference", Content: "vegetarian", Confidence: 0.9})

	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{})
	store := NewMemoryLLMDebugStore()
	hook.SetLLMDebugStore(store)

	requestID := "req-enrichment-pre"
	// UserMemoryEnrichmentHook reads request ID via GetRequestID(ctx), which
	// uses a context value (not baggage). See orchestrator.go:WithRequestID.
	ctx := WithRequestID(context.Background(), requestID)
	pctx := &core.PipelineContext{
		Request:  "plan my trip",
		Metadata: map[string]interface{}{"user_id": "u1"},
	}
	_, err := hook.BeforePlanning(ctx, pctx)
	require.NoError(t, err)

	// recordDebugInteraction is async (fire-and-forget goroutine). Same-package
	// test can access the unexported WaitGroup to flush in-flight writes.
	hook.debugWg.Wait()

	record, err := store.GetRecord(ctx, requestID)
	require.NoError(t, err)
	require.NotEmpty(t, record.Interactions, "expected BeforePlanning to emit at least one interaction")
	for _, i := range record.Interactions {
		assert.Equalf(t, HookPhasePre, i.HookPhase,
			"interaction Type=%q must be tagged HookPhasePre", i.Type)
	}
}

// TestHookPhase_UserMemoryExtraction_EmitsPost asserts every debug interaction
// emitted by UserMemoryExtractionHook.AfterSynthesis carries HookPhasePost.
func TestHookPhase_UserMemoryExtraction_EmitsPost(t *testing.T) {
	mem := newTestUserMemory()
	extractor := &staticFactExtractor{facts: []core.UserFact{
		{Content: "User is vegetarian", Category: "constraint", Source: core.SourceExplicit, Confidence: 0.95},
	}}
	reconciler := &addAllReconciler{}

	// Synchronous mode so AfterSynthesis completes before we inspect the store.
	hook := NewUserMemoryExtractionHook(
		mem, nil, nil, "travel", &core.NoOpLogger{},
		extractor, reconciler,
	)
	store := NewMemoryLLMDebugStore()
	hook.SetLLMDebugStore(store)
	t.Cleanup(func() { _ = hook.Close() })

	requestID := "req-extraction-post"
	// See note in TestHookPhase_UserMemoryEnrichment_EmitsPre on request-ID propagation.
	ctx := WithRequestID(context.Background(), requestID)
	pctx := &core.PipelineContext{
		Request:  "any vegetarian restaurants?",
		Metadata: map[string]interface{}{"user_id": "u1"},
	}
	_, err := hook.AfterSynthesis(ctx, pctx, "Yes, here are some vegetarian options")
	require.NoError(t, err)

	// Close() waits for both extraction and debug goroutines.
	require.NoError(t, hook.Close())

	record, err := store.GetRecord(ctx, requestID)
	require.NoError(t, err)
	require.NotEmpty(t, record.Interactions, "expected AfterSynthesis to emit at least one interaction")
	for _, i := range record.Interactions {
		assert.Equalf(t, HookPhasePost, i.HookPhase,
			"interaction Type=%q must be tagged HookPhasePost", i.Type)
	}
}

// TestHookPhase_ActivityCompactor_EmitsPre asserts every debug interaction
// emitted by LLMActivityCompactor.CompactEvents and .UpdateDigest carries
// HookPhasePre. Both methods are currently called only from the
// pre-planning MemoryEnrichment hook (see the invariant note on their
// godoc); this test locks that assumption into the test suite.
func TestHookPhase_ActivityCompactor_EmitsPre(t *testing.T) {
	ai := &compactorMockAI{}
	c, err := NewLLMActivityCompactor(ai)
	require.NoError(t, err)
	store := NewMemoryLLMDebugStore()
	c.SetLLMDebugStore(store)

	requestID := "req-compaction-pre"
	ctx := telemetry.WithBaggage(context.Background(), "request_id", requestID)

	events := []core.AgentEvent{
		{AgentName: "a", ActionType: "x", Outcome: "success", Timestamp: time.Now()},
	}
	_, err = c.CompactEvents(ctx, events, 500)
	require.NoError(t, err)

	// Also exercise the incremental path so both emit sites fire.
	_, err = c.UpdateDigest(ctx, "prev digest", events, 500)
	require.NoError(t, err)

	require.NoError(t, c.Shutdown(context.Background()))

	record, err := store.GetRecord(ctx, requestID)
	require.NoError(t, err)
	require.NotEmpty(t, record.Interactions, "expected compaction calls to emit at least one interaction")
	for _, i := range record.Interactions {
		assert.Equalf(t, HookPhasePre, i.HookPhase,
			"interaction Type=%q must be tagged HookPhasePre", i.Type)
	}
}

// TestHookPhase_EventSummarizer_EmitsPost asserts every debug interaction
// emitted by LLMEventSummarizer.SummarizeSteps carries HookPhasePost.
// The summarizer is called only from the post-synthesis MemoryRecord hook;
// this test locks that assumption.
func TestHookPhase_EventSummarizer_EmitsPost(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{Content: "[]"}, nil
		},
	}
	s, err := NewLLMEventSummarizer(ai)
	require.NoError(t, err)
	store := NewMemoryLLMDebugStore()
	s.SetLLMDebugStore(store)

	requestID := "req-summarization-post"
	ctx := telemetry.WithBaggage(context.Background(), "request_id", requestID)

	steps := []core.StepSummaryInput{
		{StepID: "s1", AgentName: "agent", Capability: "x", Instruction: "do x", Success: true},
	}
	_, err = s.SummarizeSteps(ctx, steps)
	require.NoError(t, err)

	require.NoError(t, s.Shutdown(context.Background()))

	record, err := store.GetRecord(ctx, requestID)
	require.NoError(t, err)
	require.NotEmpty(t, record.Interactions, "expected SummarizeSteps to emit at least one interaction")
	for _, i := range record.Interactions {
		assert.Equalf(t, HookPhasePost, i.HookPhase,
			"interaction Type=%q must be tagged HookPhasePost", i.Type)
	}
}
