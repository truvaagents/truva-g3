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

// ExecutionLineageRetentionFixture exposes only the provider-neutral behavior
// needed to verify that failed descendants promote the authoritative lineage
// retention even when an advisory projection write fails.
type ExecutionLineageRetentionFixture struct {
	Store                   orchestration.ExecutionStore
	RootID                  string
	ChildID                 string
	ShortTTL                time.Duration
	LongTTL                 time.Duration
	InjectProjectionFailure func()
	ProjectionFailureCount  func() int
	AssertMinimumRetention  func(*testing.T, string, time.Duration)
}

// RunExecutionLineageRetentionConformance verifies the cross-writer retention
// invariant without depending on Redis commands or key shapes. Backend-specific
// factories supply fault injection and TTL observation.
func RunExecutionLineageRetentionConformance(
	t *testing.T,
	factory func(*testing.T) ExecutionLineageRetentionFixture,
) {
	t.Helper()
	fixture := factory(t)
	if fixture.Store == nil || fixture.InjectProjectionFailure == nil || fixture.ProjectionFailureCount == nil ||
		fixture.AssertMinimumRetention == nil {
		t.Fatal("execution lineage retention fixture is incomplete")
	}
	if fixture.ShortTTL <= 0 || fixture.LongTTL <= fixture.ShortTTL {
		t.Fatal("execution lineage retention fixture requires positive increasing TTLs")
	}
	root := &orchestration.StoredExecution{
		RequestID: fixture.RootID, TraceID: "trace-" + fixture.RootID, OriginalRequest: "root",
		CreatedAt: time.Now(), Result: &orchestration.ExecutionResult{Success: true},
		Metadata: map[string]string{orchestration.MetadataConversationID: "conversation-" + fixture.RootID},
	}
	if err := fixture.Store.Store(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	fixture.InjectProjectionFailure()
	child := &orchestration.StoredExecution{
		RequestID: fixture.ChildID, OriginalRequestID: fixture.RootID,
		TraceID: "trace-" + fixture.ChildID, OriginalRequest: "child",
		CreatedAt: time.Now(), Result: &orchestration.ExecutionResult{Success: false},
	}
	if err := fixture.Store.Store(t.Context(), child); err != nil {
		t.Fatalf("advisory projection failure became authoritative: %v", err)
	}
	if got := fixture.ProjectionFailureCount(); got < 1 {
		t.Fatal("execution projection fault was not exercised")
	}
	for _, requestID := range []string{fixture.RootID, fixture.ChildID} {
		if _, err := fixture.Store.Get(t.Context(), requestID); err != nil {
			t.Fatalf("authoritative execution %q was not readable: %v", requestID, err)
		}
		fixture.AssertMinimumRetention(t, requestID, fixture.LongTTL-fixture.ShortTTL)
	}
}

// LLMDebugRetentionFixture joins the orchestration retention reader with the
// telemetry format-twin writer and backend-specific projection/TTL probes.
type LLMDebugRetentionFixture struct {
	Store                  orchestration.LLMDebugStore
	RequestID              string
	ShortTTL               time.Duration
	LongTTL                time.Duration
	Advance                func(time.Duration)
	Write                  func(context.Context, string, string) error
	SetProjectionFailure   func(bool)
	ProjectionFailureCount func() int
	InteractionCount       func(*testing.T, string) int64
	RecentIndexPresent     func(*testing.T, string) bool
	AssertMinimumRetention func(*testing.T, string, time.Duration)
}

// RunLLMDebugRetentionConformance verifies the telemetry/orchestration format
// twin, minimum-retention floor, non-duplication under advisory retry failure,
// and repair by a later successful projection write.
func RunLLMDebugRetentionConformance(
	t *testing.T,
	factory func(*testing.T) LLMDebugRetentionFixture,
) {
	t.Helper()
	fixture := factory(t)
	if fixture.Store == nil || fixture.Write == nil || fixture.Advance == nil || fixture.SetProjectionFailure == nil ||
		fixture.ProjectionFailureCount == nil || fixture.InteractionCount == nil ||
		fixture.RecentIndexPresent == nil || fixture.AssertMinimumRetention == nil {
		t.Fatal("LLM debug retention fixture is incomplete")
	}
	if fixture.ShortTTL <= 0 || fixture.LongTTL <= 2*fixture.ShortTTL {
		t.Fatal("LLM debug retention fixture requires a long TTL greater than twice the short TTL")
	}
	if err := fixture.Write(t.Context(), fixture.RequestID, "before-retention"); err != nil {
		t.Fatal(err)
	}
	preserver, ok := fixture.Store.(orchestration.LLMDebugRetentionPreserver)
	if !ok {
		t.Fatal("LLM debug store does not expose retention preservation")
	}
	if err := preserver.PreserveRetention(t.Context(), fixture.RequestID, fixture.LongTTL); err != nil {
		t.Fatal(err)
	}
	fixture.Advance(fixture.ShortTTL)
	fixture.SetProjectionFailure(true)
	if err := fixture.Write(t.Context(), fixture.RequestID, "during-projection-failure"); err != nil {
		t.Fatalf("advisory projection failure became authoritative: %v", err)
	}
	if got := fixture.InteractionCount(t, fixture.RequestID); got != 2 {
		t.Fatalf("authoritative interactions after failed projection = %d, want 2", got)
	}
	if got := fixture.ProjectionFailureCount(); got < 1 {
		t.Fatal("projection fault was not exercised")
	}
	fixture.AssertMinimumRetention(t, fixture.RequestID, fixture.LongTTL-2*fixture.ShortTTL)
	fixture.SetProjectionFailure(false)
	if err := fixture.Write(t.Context(), fixture.RequestID, "after-projection-repair"); err != nil {
		t.Fatal(err)
	}
	if got := fixture.InteractionCount(t, fixture.RequestID); got != 3 {
		t.Fatalf("authoritative interactions after projection repair = %d, want 3", got)
	}
	if !fixture.RecentIndexPresent(t, fixture.RequestID) {
		t.Fatal("later write did not repair the LLM recent projection")
	}
}
