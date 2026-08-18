package orchestration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

func TestLLMDebugConversationID(t *testing.T) {
	if got := LLMDebugConversationID(nil); got != "" {
		t.Fatalf("nil record conversation ID = %q", got)
	}

	var historical LLMDebugRecord
	if err := json.Unmarshal([]byte(`{"request_id":"historical"}`), &historical); err != nil {
		t.Fatalf("historical record unmarshal: %v", err)
	}
	if got := LLMDebugConversationID(&historical); got != "" {
		t.Fatalf("historical record conversation ID = %q", got)
	}

	current := &LLMDebugRecord{Metadata: map[string]string{
		MetadataConversationID: "conversation-current",
	}}
	if got := LLMDebugConversationID(current); got != "conversation-current" {
		t.Fatalf("current record conversation ID = %q", got)
	}
}

func TestRedisLLMDebugStoreOldStringConversationCompatibility(t *testing.T) {
	_, store := setupRedisLLMDebugTestStore(t)
	record := &LLMDebugRecord{
		RequestID: "request-old-string",
		Metadata: map[string]string{
			MetadataConversationID: "conversation-old-string",
		},
	}
	serialized, err := store.serialize(record)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if err := store.client.Set(
		context.Background(),
		llmDebugKeyPrefix+record.RequestID,
		serialized,
		defaultDebugTTL,
	).Err(); err != nil {
		t.Fatalf("seed old record: %v", err)
	}

	got, err := store.GetRecord(context.Background(), record.RequestID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if conversationID := LLMDebugConversationID(got); conversationID != "conversation-old-string" {
		t.Fatalf("old-string conversation ID = %q", conversationID)
	}
}

func TestLLMDebugStoresConversationFirstValidWriterWins(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T) LLMDebugStore
	}{
		{
			name: "memory",
			store: func(*testing.T) LLMDebugStore {
				return NewMemoryLLMDebugStore()
			},
		},
		{
			name: "redis",
			store: func(t *testing.T) LLMDebugStore {
				_, store := setupRedisLLMDebugTestStore(t)
				return store
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.store(t)
			requestID := "request-first-valid"
			interaction := LLMInteraction{Type: "plan_generation", Success: true}

			if err := store.RecordInteraction(context.Background(), requestID, interaction); err != nil {
				t.Fatalf("empty first write: %v", err)
			}

			baggageCtx := telemetry.WithBaggage(
				context.Background(),
				MetadataConversationID,
				"conversation-first",
			)
			if err := store.RecordInteraction(baggageCtx, requestID, interaction); err != nil {
				t.Fatalf("valid backfill: %v", err)
			}

			differentCtx := core.WithConversationID(
				context.Background(),
				"conversation-different",
			)
			if err := store.RecordInteraction(differentCtx, requestID, interaction); err != nil {
				t.Fatalf("later different write: %v", err)
			}

			invalidCoreCtx := telemetry.WithBaggage(
				context.Background(),
				MetadataConversationID,
				"conversation-fallback-must-not-win",
			)
			invalidCoreCtx = core.WithConversationID(invalidCoreCtx, "invalid conversation")
			if err := store.RecordInteraction(invalidCoreCtx, requestID, interaction); err != nil {
				t.Fatalf("later invalid write: %v", err)
			}

			record, err := store.GetRecord(context.Background(), requestID)
			if err != nil {
				t.Fatalf("GetRecord: %v", err)
			}
			if got := LLMDebugConversationID(record); got != "conversation-first" {
				t.Fatalf("conversation ID = %q, want first valid writer", got)
			}
		})
	}
}

func TestLLMDebugStoresCoreConversationWinsOverBaggage(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T) LLMDebugStore
	}{
		{
			name: "memory",
			store: func(*testing.T) LLMDebugStore {
				return NewMemoryLLMDebugStore()
			},
		},
		{
			name: "redis",
			store: func(t *testing.T) LLMDebugStore {
				_, store := setupRedisLLMDebugTestStore(t)
				return store
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.store(t)
			ctx := telemetry.WithBaggage(
				context.Background(),
				MetadataConversationID,
				"conversation-baggage",
			)
			ctx = core.WithConversationID(ctx, "conversation-core")
			if err := store.RecordInteraction(
				ctx,
				"request-precedence",
				LLMInteraction{Type: "plan_generation", Success: true},
			); err != nil {
				t.Fatalf("RecordInteraction: %v", err)
			}
			record, err := store.GetRecord(context.Background(), "request-precedence")
			if err != nil {
				t.Fatalf("GetRecord: %v", err)
			}
			if got := LLMDebugConversationID(record); got != "conversation-core" {
				t.Fatalf("conversation ID = %q, want core candidate", got)
			}
		})
	}
}

func TestLLMDebugStoresInvalidCoreBlocksBaggageFallback(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T) LLMDebugStore
	}{
		{
			name: "memory",
			store: func(*testing.T) LLMDebugStore {
				return NewMemoryLLMDebugStore()
			},
		},
		{
			name: "redis",
			store: func(t *testing.T) LLMDebugStore {
				_, store := setupRedisLLMDebugTestStore(t)
				return store
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.store(t)
			ctx := telemetry.WithBaggage(
				context.Background(),
				MetadataConversationID,
				"conversation-baggage",
			)
			ctx = core.WithConversationID(ctx, "invalid conversation")
			if err := store.RecordInteraction(
				ctx,
				"request-invalid-precedence",
				LLMInteraction{Type: "plan_generation", Success: true},
			); err != nil {
				t.Fatalf("RecordInteraction: %v", err)
			}
			record, err := store.GetRecord(
				context.Background(),
				"request-invalid-precedence",
			)
			if err != nil {
				t.Fatalf("GetRecord: %v", err)
			}
			if got := LLMDebugConversationID(record); got != "" {
				t.Fatalf("invalid core candidate fell through to baggage: %q", got)
			}
		})
	}
}

func TestLLMDebugStoresRejectReservedConversationMetadata(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T) LLMDebugStore
	}{
		{
			name: "memory",
			store: func(*testing.T) LLMDebugStore {
				return NewMemoryLLMDebugStore()
			},
		},
		{
			name: "redis",
			store: func(t *testing.T) LLMDebugStore {
				_, store := setupRedisLLMDebugTestStore(t)
				return store
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.store(t)
			ctx := core.WithConversationID(context.Background(), "conversation-original")
			if err := store.RecordInteraction(
				ctx,
				"request-metadata",
				LLMInteraction{Type: "plan_generation", Success: true},
			); err != nil {
				t.Fatalf("RecordInteraction: %v", err)
			}
			if err := store.SetMetadata(
				context.Background(),
				"request-metadata",
				MetadataConversationID,
				"conversation-replacement",
			); err == nil {
				t.Fatal("expected reserved metadata error")
			}
			if err := store.SetMetadata(
				context.Background(),
				"request-metadata",
				"investigation",
				"keep",
			); err != nil {
				t.Fatalf("unrelated SetMetadata: %v", err)
			}
			record, err := store.GetRecord(context.Background(), "request-metadata")
			if err != nil {
				t.Fatalf("GetRecord: %v", err)
			}
			if got := LLMDebugConversationID(record); got != "conversation-original" {
				t.Fatalf("conversation ID = %q", got)
			}
			if record.Metadata["investigation"] != "keep" {
				t.Fatalf("investigation metadata = %v", record.Metadata)
			}
		})
	}
}

func TestLLMDebugRedisWritersUseCompatibleConversationField(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	orchestrationStore := &RedisLLMDebugStore{
		client:   redis.NewClient(&redis.Options{Addr: mr.Addr()}),
		logger:   &core.NoOpLogger{},
		ttl:      defaultDebugTTL,
		errorTTL: errorDebugTTL,
	}
	recorder, err := telemetry.NewRedisLLMCallRecorder(
		telemetry.WithRecorderRedisURL("redis://"+mr.Addr()),
		telemetry.WithRecorderRedisDB(0),
	)
	if err != nil {
		t.Fatalf("NewRedisLLMCallRecorder: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })

	ctx := core.WithConversationID(context.Background(), "conversation-format")
	if err := orchestrationStore.RecordInteraction(
		ctx,
		"request-orchestration-writer",
		LLMInteraction{Type: "plan_generation", Success: true},
	); err != nil {
		t.Fatalf("orchestration writer: %v", err)
	}
	if err := recorder.RecordLLMCall(
		ctx,
		"request-telemetry-writer",
		telemetry.LLMCallRecord{CallType: "agent_llm_call", Success: true},
	); err != nil {
		t.Fatalf("telemetry writer: %v", err)
	}

	for _, requestID := range []string{
		"request-orchestration-writer",
		"request-telemetry-writer",
	} {
		metaKey := llmDebugKeyPrefix + requestID + llmDebugMetaSuffix
		if got := mr.HGet(metaKey, "meta:"+MetadataConversationID); got != "conversation-format" {
			t.Fatalf("%s conversation field = %q", requestID, got)
		}
		record, getErr := orchestrationStore.GetRecord(context.Background(), requestID)
		if getErr != nil {
			t.Fatalf("GetRecord(%s): %v", requestID, getErr)
		}
		if got := LLMDebugConversationID(record); got != "conversation-format" {
			t.Fatalf("%s typed conversation ID = %q", requestID, got)
		}
	}
}

func TestNoOpLLMDebugConversationDefaultsRemainSafe(t *testing.T) {
	store := NewNoOpLLMDebugStore()
	if err := store.RecordInteraction(
		context.Background(),
		"request-noop",
		LLMInteraction{},
	); err != nil {
		t.Fatalf("RecordInteraction: %v", err)
	}
	if err := store.SetMetadata(
		context.Background(),
		"request-noop",
		MetadataConversationID,
		"conversation-noop",
	); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	record, err := store.GetRecord(context.Background(), "request-noop")
	if err == nil || LLMDebugConversationID(record) != "" {
		t.Fatalf("NoOp GetRecord = (%v, %v)", record, err)
	}
}
