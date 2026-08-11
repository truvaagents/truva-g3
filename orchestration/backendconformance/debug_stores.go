package backendconformance

import (
	"context"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

type ExecutionFixture struct {
	First    orchestration.ExecutionStore
	Second   orchestration.ExecutionStore
	Isolated orchestration.ExecutionStore
}

func RunExecutionStoreConformance(t *testing.T, factory func(*testing.T) ExecutionFixture) {
	t.Helper()
	fixture := factory(t)
	record := &orchestration.StoredExecution{
		RequestID: "execution-1", TraceID: "trace-1", OriginalRequest: "request",
		CreatedAt: time.Now(), Result: &orchestration.ExecutionResult{Success: true},
	}
	if err := fixture.First.Store(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := fixture.Second.Get(t.Context(), record.RequestID)
	if err != nil || loaded.RequestID != record.RequestID {
		t.Fatalf("cross-instance Get = %#v, %v", loaded, err)
	}
	byTrace, err := fixture.Second.GetByTraceID(t.Context(), record.TraceID)
	if err != nil || byTrace.RequestID != record.RequestID {
		t.Fatalf("GetByTraceID = %#v, %v", byTrace, err)
	}
	if err := fixture.Second.SetMetadata(t.Context(), record.RequestID, "investigation", "open"); err != nil {
		t.Fatal(err)
	}
	loaded, err = fixture.First.Get(t.Context(), record.RequestID)
	if err != nil || loaded.Metadata["investigation"] != "open" {
		t.Fatalf("metadata = %#v, %v", loaded, err)
	}
	recent, err := fixture.Second.ListRecent(t.Context(), 10)
	if err != nil || len(recent) != 1 || recent[0].RequestID != record.RequestID {
		t.Fatalf("recent = %#v, %v", recent, err)
	}
	if _, err := fixture.Isolated.Get(t.Context(), record.RequestID); err == nil {
		t.Fatal("namespace isolation failed")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := fixture.First.Store(canceled, &orchestration.StoredExecution{RequestID: "cancelled", CreatedAt: time.Now()}); err == nil {
		t.Fatal("cancelled Store returned nil error")
	}
}

type LLMDebugFixture struct {
	First    orchestration.LLMDebugStore
	Second   orchestration.LLMDebugStore
	Isolated orchestration.LLMDebugStore
}

func RunLLMDebugStoreConformance(t *testing.T, factory func(*testing.T) LLMDebugFixture) {
	t.Helper()
	fixture := factory(t)
	interaction := orchestration.LLMInteraction{
		Type: "conformance", Timestamp: time.Now(), Prompt: "prompt", Response: "response", Success: true, Attempt: 1,
	}
	if err := fixture.First.RecordInteraction(t.Context(), "llm-1", interaction); err != nil {
		t.Fatal(err)
	}
	record, err := fixture.Second.GetRecord(t.Context(), "llm-1")
	if err != nil || len(record.Interactions) != 1 || record.Interactions[0].Prompt != interaction.Prompt {
		t.Fatalf("cross-instance record = %#v, %v", record, err)
	}
	if err := fixture.Second.SetMetadata(t.Context(), "llm-1", "investigation", "open"); err != nil {
		t.Fatal(err)
	}
	record, err = fixture.First.GetRecord(t.Context(), "llm-1")
	if err != nil || record.Metadata["investigation"] != "open" {
		t.Fatalf("metadata = %#v, %v", record, err)
	}
	recent, err := fixture.Second.ListRecent(t.Context(), 10)
	if err != nil || len(recent) != 1 || recent[0].RequestID != "llm-1" {
		t.Fatalf("recent = %#v, %v", recent, err)
	}
	if _, err := fixture.Isolated.GetRecord(t.Context(), "llm-1"); err == nil {
		t.Fatal("namespace isolation failed")
	}
}
