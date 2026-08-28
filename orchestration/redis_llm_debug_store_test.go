package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

func TestRedisLLMDebugStore_PreservesApplicationPayloads(t *testing.T) {
	_, store := setupRedisLLMDebugTestStore(t)
	payload := "password=local-debug-value"
	requestID := "req-payload-fidelity"
	if err := store.RecordInteraction(context.Background(), requestID, LLMInteraction{
		Type: "semantic_retry", Prompt: "prompt " + payload,
		Response: "response " + payload, Success: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMetadata(context.Background(), requestID, "application_value", payload); err != nil {
		t.Fatal(err)
	}
	record, err := store.GetRecord(context.Background(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), payload) || strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatalf("redis debug record changed application payloads: %s", encoded)
	}
	if record.Metadata["application_value"] != payload {
		t.Fatalf("redis debug metadata = %q, want %q", record.Metadata["application_value"], payload)
	}
}

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

func TestRedisLLMDebugStoreOptionsNormalizeNonPositiveTTLs(t *testing.T) {
	mr := miniredis.RunT(t)

	ownedStore, err := NewRedisLLMDebugStore(
		WithDebugRedisURL(mr.Addr()),
		WithDebugTTL(0),
		WithDebugErrorTTL(-time.Second),
	)
	if err != nil {
		t.Fatalf("NewRedisLLMDebugStore: %v", err)
	}
	t.Cleanup(func() { _ = ownedStore.Close() })
	if ownedStore.ttl != defaultDebugTTL || ownedStore.errorTTL != errorDebugTTL {
		t.Fatalf(
			"owned store TTLs = (%v, %v), want (%v, %v)",
			ownedStore.ttl,
			ownedStore.errorTTL,
			defaultDebugTTL,
			errorDebugTTL,
		)
	}

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	injectedStore, err := NewRedisLLMDebugStoreWithClient(
		client,
		WithDebugTTL(0),
		WithDebugErrorTTL(-time.Second),
	)
	if err != nil {
		t.Fatalf("NewRedisLLMDebugStoreWithClient: %v", err)
	}
	if injectedStore.ttl != defaultDebugTTL || injectedStore.errorTTL != errorDebugTTL {
		t.Fatalf(
			"injected store TTLs = (%v, %v), want (%v, %v)",
			injectedStore.ttl,
			injectedStore.errorTTL,
			defaultDebugTTL,
			errorDebugTTL,
		)
	}

	for _, test := range []struct {
		requestID string
		success   bool
		wantTTL   time.Duration
	}{
		{requestID: "normalized-success", success: true, wantTTL: defaultDebugTTL},
		{requestID: "normalized-error", success: false, wantTTL: errorDebugTTL},
	} {
		if err := injectedStore.RecordInteraction(
			context.Background(),
			test.requestID,
			LLMInteraction{Type: "test", Success: test.success},
		); err != nil {
			t.Fatalf("RecordInteraction(%s): %v", test.requestID, err)
		}
		metaKey := llmDebugKeyPrefix + test.requestID + llmDebugMetaSuffix
		if got := mr.TTL(metaKey); got != test.wantTTL {
			t.Fatalf("TTL(%s) = %v, want %v", metaKey, got, test.wantTTL)
		}
	}
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

// TestRedisLLMDebugStore_OriginatingAgent_FirstWriterWins is the regression
// guard for the LLM Debug Source column. The originator is the agent whose
// orchestrator (or background job) initiated the request — it must persist
// across the typical flow where an orchestrator-hosted agent dispatches to
// a downstream agent that also records LLM calls.
//
// Invariants:
//   - First non-empty agent_name baggage value wins (HSetNX semantics).
//   - Subsequent writes from downstream agents do NOT overwrite it.
//   - GetRecord + ListRecent both surface the field.
//   - Empty baggage yields an empty field (no spurious default).
func TestRedisLLMDebugStore_OriginatingAgent_FirstWriterWins(t *testing.T) {
	_, store := setupRedisLLMDebugTestStore(t)
	base := context.Background()

	// First write: orchestrator-hosted agent stamps its own name into baggage.
	// This mirrors the orchestrator stamping o.config.Name at orchestrator.go:2876.
	ctxOrch := telemetry.WithBaggage(base, "agent_name", "travel-chat-agent")
	if err := store.RecordInteraction(ctxOrch, "req-orig", LLMInteraction{
		Type:        "plan_generation",
		Prompt:      "plan",
		Response:    "ok",
		PhaseNumber: 1,
		Success:     true,
	}); err != nil {
		t.Fatalf("orchestrator-side record failed: %v", err)
	}

	// Second write: downstream agent (different baggage value). Must NOT
	// overwrite the originator — HSetNX should swallow this write of the
	// originating_agent field while still appending the interaction.
	ctxDown := telemetry.WithBaggage(base, "agent_name", "research-agent-telemetry-service")
	if err := store.RecordInteraction(ctxDown, "req-orig", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "research-agent-telemetry-service",
		Prompt:          "math",
		Response:        "42",
		Success:         true,
	}); err != nil {
		t.Fatalf("downstream-side record failed: %v", err)
	}

	rec, err := store.GetRecord(base, "req-orig")
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}
	if rec.OriginatingAgent != "travel-chat-agent" {
		t.Errorf("OriginatingAgent must be first writer's value; got %q (expected travel-chat-agent)", rec.OriginatingAgent)
	}

	summaries, err := store.ListRecent(base, 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].OriginatingAgent != "travel-chat-agent" {
		t.Errorf("summary OriginatingAgent must match record; got %q", summaries[0].OriginatingAgent)
	}
}

// TestRedisLLMDebugStore_OriginatingAgent_BackfillOnLaterWrite mirrors the
// MemoryLLMDebugStore test of the same name to lock in parity across the two
// production implementations. When the first write carries no agent_name
// baggage, the HSetNX write is skipped (gated on non-empty value), so the
// meta hash field stays unset. A later write that DOES carry baggage can
// then populate it — this is "first writer with a value wins," not the
// stricter "first writer wins regardless of value."
func TestRedisLLMDebugStore_OriginatingAgent_BackfillOnLaterWrite(t *testing.T) {
	_, store := setupRedisLLMDebugTestStore(t)

	// First write: no baggage. Field should NOT be stamped.
	if err := store.RecordInteraction(context.Background(), "req-backfill", LLMInteraction{
		Type:    "plan_generation",
		Success: true,
	}); err != nil {
		t.Fatalf("first (no-baggage) write failed: %v", err)
	}

	// Second write: baggage present. Field should now backfill.
	ctxNamed := telemetry.WithBaggage(context.Background(), "agent_name", "devops-chat-agent")
	if err := store.RecordInteraction(ctxNamed, "req-backfill", LLMInteraction{
		Type:            "agent_llm_call",
		SourceComponent: "devops-chat-agent",
		Success:         true,
	}); err != nil {
		t.Fatalf("second (with-baggage) write failed: %v", err)
	}

	rec, err := store.GetRecord(context.Background(), "req-backfill")
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}
	if rec.OriginatingAgent != "devops-chat-agent" {
		t.Errorf("OriginatingAgent must backfill from later baggage when first write had none; got %q", rec.OriginatingAgent)
	}
}

// TestRedisLLMDebugStore_OriginatingAgent_EmptyBaggage covers the
// pre-instrumented historical path: writes with no agent_name baggage must
// leave the field empty rather than stamping a placeholder. This is what
// keeps old records rendering via the existing source_components fallback
// in the viewer instead of misattributing the originator.
func TestRedisLLMDebugStore_OriginatingAgent_EmptyBaggage(t *testing.T) {
	_, store := setupRedisLLMDebugTestStore(t)
	ctx := context.Background()

	if err := store.RecordInteraction(ctx, "req-no-bag", LLMInteraction{
		Type:        "plan_generation",
		Prompt:      "x",
		Response:    "y",
		PhaseNumber: 1,
		Success:     true,
	}); err != nil {
		t.Fatalf("record failed: %v", err)
	}

	rec, err := store.GetRecord(ctx, "req-no-bag")
	if err != nil {
		t.Fatalf("GetRecord failed: %v", err)
	}
	if rec.OriginatingAgent != "" {
		t.Errorf("OriginatingAgent must be empty when baggage carries no agent_name; got %q", rec.OriginatingAgent)
	}
}
