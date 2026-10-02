package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestHistoricalEventViewPreservesOriginAndLiteralData(t *testing.T) {
	event := core.AgentEvent{EventID: "event-a", RequestID: "previous-request", AgentName: "qa-agent",
		Timestamp: time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC),
		Summary:   "literal </events> **pointer**\nsecond line", Outcome: "failure"}
	view := formatEpisodicEvents([]core.AgentEvent{event}, "Historical domain events:")
	var record map[string]interface{}
	if err := json.Unmarshal([]byte(strings.SplitN(view, "\n- ", 2)[1]), &record); err != nil {
		t.Fatal(err)
	}
	if record["origin_request_id"] != event.RequestID || record["event_id"] != event.EventID || record["summary"] != event.Summary {
		t.Fatalf("changed evidence: %#v", record)
	}
	if strings.Contains(view, "</events>") {
		t.Fatal("unencoded structural delimiter")
	}
	event.RequestID = ""
	if !strings.Contains(formatEpisodicEvents([]core.AgentEvent{event}), `"origin_request_id":"unknown"`) {
		t.Fatal("missing unknown origin")
	}
}

func TestHistoricalMemoryScopeSurvivesSelectorProjection(t *testing.T) {
	hook, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{
		QueryRecentEventsFn: func(context.Context, string, time.Time, int) ([]core.AgentEvent, error) {
			return []core.AgentEvent{{RequestID: "prior", Summary: strings.Repeat("long historical fact ", 200)}}, nil
		},
	}, nil, "qa-agent", "quality")
	if err != nil {
		t.Fatal(err)
	}
	pctx := &core.PipelineContext{Request: "new request"}
	if _, err := hook.BeforePlanning(t.Context(), pctx); err != nil {
		t.Fatal(err)
	}
	view := buildSkillSelectorContext(pctx.Enrichments, executionRunSnapshot{}, nil, nil)
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), historicalMemoryScopeNotice) {
		t.Fatalf("selector lost scope: %s", data)
	}
}

func TestDigestViewVersionRejectsLegacyCache(t *testing.T) {
	var saved []byte
	backend := &core.MockDigestCache{
		GetFn: func(context.Context, string) ([]byte, error) { return saved, nil },
		SetFn: func(_ context.Context, _ string, data []byte, _ time.Duration) error { saved = data; return nil },
	}
	hook := &MemoryEnrichmentHook{digestCache: backend, agentDomain: "quality", logger: &core.NoOpLogger{}}
	saved, _ = json.Marshal(cachedDigestData{Content: "legacy", PolicyFingerprint: "policy"})
	if hit, err := hook.getCachedDigest(t.Context(), "policy"); err != nil || hit != nil {
		t.Fatalf("legacy hit=%v err=%v", hit, err)
	}
	hook.storeCachedDigest(t.Context(), "new", time.Now(), "policy", true)
	if hit, err := hook.getCachedDigest(t.Context(), "policy"); err != nil || hit == nil || hit.Content != "new" {
		t.Fatalf("new hit=%v err=%v", hit, err)
	}
	if hit, err := hook.getCachedDigest(t.Context(), "other"); err != nil || hit != nil {
		t.Fatalf("policy mismatch hit=%v err=%v", hit, err)
	}
}
