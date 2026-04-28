package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

// setupRedisLLMDebugTestStore creates a RedisLLMDebugStore backed by
// miniredis. Mirrors the pattern in hitl_checkpoint_store_test.go. No
// real Redis required — in-process mock, runs in CI without network.
func setupRedisLLMDebugTestStore(t *testing.T) (*miniredis.Miniredis, *RedisLLMDebugStore) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := &RedisLLMDebugStore{
		client:   client,
		logger:   &core.NoOpLogger{},
		ttl:      24 * time.Hour,
		errorTTL: 7 * 24 * time.Hour,
	}
	return mr, store
}

// TestRedisLLMDebugStore_ListRecent_DedupeShadowsInSummary is the
// Finding-2 regression guard for the Redis-backed store (the production
// code path). The MemoryLLMDebugStore variant of this test lives in
// llm_debug_store_test.go; both stores must produce identical summary
// totals on duplicated records.
// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
func TestRedisLLMDebugStore_ListRecent_DedupeShadowsInSummary(t *testing.T) {
	_, store := setupRedisLLMDebugTestStore(t)
	ctx := context.Background()

	// Record a paired write: typed + agent_llm_call shadow with matching
	// join fields. Both carry 500 tokens; the un-deduped view would sum
	// to 1000 and count 2 interactions. The deduped view must report 500
	// and 1 respectively.
	if err := store.RecordInteraction(ctx, "req-paired", LLMInteraction{
		Type:        "plan_generation",
		Prompt:      "plan this",
		Response:    "plan A",
		DurationMs:  142,
		PhaseNumber: 1,
		TotalTokens: 500,
		Success:     true,
	}); err != nil {
		t.Fatalf("record typed row failed: %v", err)
	}
	if err := store.RecordInteraction(ctx, "req-paired", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "devops-chat-agent",
		Prompt:          "plan this",
		Response:        "plan A",
		DurationMs:      142,
		PhaseNumber:     1,
		TotalTokens:     500,
		Success:         true,
	}); err != nil {
		t.Fatalf("record shadow row failed: %v", err)
	}

	summaries, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	s := summaries[0]
	if s.InteractionCount != 1 {
		t.Errorf("InteractionCount must count deduped view (1 physical call); got %d (inflated by shadow)", s.InteractionCount)
	}
	if s.TotalTokens != 500 {
		t.Errorf("TotalTokens must sum deduped view (500); got %d (inflated by shadow)", s.TotalTokens)
	}
	// SourceComponents is derived from the RAW slice so the wrapping-
	// agent attribution surfaces in the list view; that is operator-
	// useful signal and does not depend on dedupe.
	if len(s.SourceComponents) != 1 || s.SourceComponents[0] != "devops-chat-agent" {
		t.Errorf("SourceComponents must include wrapping-agent attribution; got %v", s.SourceComponents)
	}
}

// TestRedisLLMDebugStore_ListRecent_DedupeSummary_HasErrors validates
// the hasErrors computation walks the deduped slice (not the raw slice).
// If the shadow's Success flag were consulted instead of the typed row's,
// we could report a false positive/negative depending on which row has
// the truthful error state. Locks in the "deduped is authoritative"
// invariant for all summary fields, not just counts and tokens.
func TestRedisLLMDebugStore_ListRecent_DedupeSummary_HasErrors(t *testing.T) {
	_, store := setupRedisLLMDebugTestStore(t)
	ctx := context.Background()

	// Typed row records a failed call; the shadow mirrors it as failed.
	// After dedupe the typed row survives — hasErrors must be true.
	if err := store.RecordInteraction(ctx, "req-err", LLMInteraction{
		Type:        "plan_generation",
		Prompt:      "p",
		Response:    "",
		DurationMs:  10,
		PhaseNumber: 1,
		TotalTokens: 0,
		Success:     false,
		Error:       "timeout",
	}); err != nil {
		t.Fatalf("record typed row failed: %v", err)
	}
	if err := store.RecordInteraction(ctx, "req-err", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "devops-chat-agent",
		Prompt:          "p",
		Response:        "",
		DurationMs:      10,
		PhaseNumber:     1,
		Success:         false,
		Error:           "timeout",
	}); err != nil {
		t.Fatalf("record shadow row failed: %v", err)
	}

	summaries, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if !summaries[0].HasErrors {
		t.Fatal("hasErrors must be true after walking the deduped slice when the typed row reports failure")
	}
	if summaries[0].InteractionCount != 1 {
		t.Errorf("InteractionCount must be 1 (deduped); got %d", summaries[0].InteractionCount)
	}
}

// TestRedisLLMDebugStore_ListRecent_NonWrappingUnchanged is the non-
// wrapping-agent regression surface for the Redis path: typed rows
// only, no shadows, dedupe must be a strict no-op. The pre-fix behaviour
// was correct here; this test ensures Finding-2's fix doesn't regress it.
func TestRedisLLMDebugStore_ListRecent_NonWrappingUnchanged(t *testing.T) {
	_, store := setupRedisLLMDebugTestStore(t)
	ctx := context.Background()

	if err := store.RecordInteraction(ctx, "req-typed-only", LLMInteraction{
		Type:        "plan_generation",
		Prompt:      "plan this",
		Response:    "plan A",
		DurationMs:  142,
		PhaseNumber: 1,
		TotalTokens: 500,
		Success:     true,
	}); err != nil {
		t.Fatalf("record typed row failed: %v", err)
	}
	if err := store.RecordInteraction(ctx, "req-typed-only", LLMInteraction{
		Type:        "synthesis_streaming",
		Prompt:      "synthesize",
		Response:    "result",
		DurationMs:  200,
		PhaseNumber: 2,
		TotalTokens: 1200,
		Success:     true,
	}); err != nil {
		t.Fatalf("record second typed row failed: %v", err)
	}

	summaries, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	s := summaries[0]
	if s.InteractionCount != 2 {
		t.Errorf("non-wrapping trace must report count unchanged; got %d (expected 2)", s.InteractionCount)
	}
	if s.TotalTokens != 1700 {
		t.Errorf("non-wrapping trace must report full sum; got %d (expected 1700)", s.TotalTokens)
	}
}
