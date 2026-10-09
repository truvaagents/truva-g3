package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
)

func assertMemoryViewBudget(t *testing.T, view string, report memoryViewReport, limit int) {
	t.Helper()
	if len(view) > limit || report.UsedBytes != len(view) || !utf8.ValidString(view) {
		t.Fatalf("invalid byte budget/UTF-8: actual=%d limit=%d report=%+v", len(view), limit, report)
	}
	if view != "" && !strings.HasPrefix(view, historicalMemoryScopeNotice) {
		t.Fatal("missing complete scope notice")
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(line, "- ") && !json.Valid([]byte(strings.TrimPrefix(line, "- "))) {
			t.Fatalf("partial JSON record: %s", line)
		}
		if strings.HasPrefix(line, "{") && !json.Valid([]byte(line)) {
			t.Fatalf("partial count marker: %s", line)
		}
	}
}

func TestMemoryViewEncodedBudgetAndPriority(t *testing.T) {
	for _, domain := range []string{"travel", "quality"} {
		t.Run(domain, func(t *testing.T) {
			events := make([]core.AgentEvent, 15)
			for i := range events {
				events[i] = core.AgentEvent{EventID: fmt.Sprintf("e%d", i), RequestID: "past-" + domain,
					Summary: strings.Repeat("<&界>\n", 75), Timestamp: time.Date(2026, 9, 1, 1, 2, 3, 456, time.UTC),
					Entities: []core.EntityRef{{Type: "opaque", ID: "key"}},
				}
			}
			original, _ := json.Marshal(events)
			sections := []memoryViewSection{
				eventMemorySection("domain", "Historical domain events:", events),
				eventMemorySection("entity", "Historical entity events:", []core.AgentEvent{{EventID: "entity", Summary: "specific"}}),
				textMemorySection("investigation", "Investigation snapshot:", "active"),
				textMemorySection("knowledge", "Shared knowledge:", "known"),
			}
			for _, limit := range []int{0, 100, 800, 8000, 100000} {
				view, report := renderMemoryView(sections, limit)
				assertMemoryViewBudget(t, view, report, limit)
				if limit < len(historicalMemoryScopeNotice) && report.Outcome != "budget_too_small" {
					t.Fatalf("tiny budget outcome: %+v", report)
				}
				if limit == 8000 {
					if report.Outcome != "partial" || report.Sections[0].Admitted == 0 || report.Sections[1].Admitted != 0 {
						t.Fatalf("existing domain-first allocation changed: %+v", report)
					}
				}
				if limit == 100000 && (report.Outcome != "complete" || report.Sections[0].Admitted != 15) {
					t.Fatalf("ample budget unexpectedly partial: %+v", report)
				}
			}
			unchanged, _ := json.Marshal(events)
			if string(unchanged) != string(original) {
				t.Fatal("presentation changed stored source values")
			}
		})
	}
}

func TestMemoryViewOnlyDeduplicatesAdmittedIDs(t *testing.T) {
	// The first representation cannot fit even with an empty summary. Fetching
	// it is not grounds for suppressing a later admissible representation.
	large := core.AgentEvent{EventID: "same", AgentName: strings.Repeat("x", 1000)}
	small := core.AgentEvent{EventID: "same", Summary: "entity result"}
	sections := []memoryViewSection{
		eventMemorySection("domain", "Domain:", []core.AgentEvent{large}),
		eventMemorySection("entity", "Entity:", []core.AgentEvent{small, small}),
	}
	view, report := renderMemoryView(sections, 700)
	assertMemoryViewBudget(t, view, report, 700)
	if report.Sections[0].Omitted != 1 || report.Sections[1].Admitted != 1 || report.Sections[1].Duplicates != 1 {
		t.Fatalf("deduplication not based on admission: %+v", report)
	}
	if !strings.Contains(view, "entity result") {
		t.Fatal("fetched but omitted domain event hid entity result")
	}
}

func TestMemoryViewEventIdentityIncludesDomain(t *testing.T) {
	events := []core.AgentEvent{
		{AgentDomain: "one", EventID: "shared", RequestID: "origin-one", Summary: "first"},
		{AgentDomain: "two", EventID: "shared", RequestID: "origin-two", Summary: "second"},
		{AgentDomain: "one", EventID: "shared", Summary: "duplicate"},
		{AgentDomain: "one", Summary: "no ID one"},
		{AgentDomain: "one", Summary: "no ID two"},
		{AgentDomain: "a:b", EventID: "c", Summary: "delimiter one"},
		{AgentDomain: "a", EventID: "b:c", Summary: "delimiter two"},
	}
	view, report := renderMemoryView([]memoryViewSection{eventMemorySection("domain", "History:", events)}, 10000)
	assertMemoryViewBudget(t, view, report, 10000)
	if report.Outcome != "complete" || report.Sections[0].Admitted != 6 || report.Sections[0].Duplicates != 1 {
		t.Fatalf("incorrect scoped identity: %+v", report)
	}
	for _, value := range []string{"origin-one", "origin-two", "no ID one", "no ID two", "delimiter one", "delimiter two"} {
		if !strings.Contains(view, value) {
			t.Fatalf("lost distinct event: %s", value)
		}
	}
	// The preflight must count the same distinct records as rendering.
	var many []core.AgentEvent
	for i := 0; i < 10; i++ {
		many = append(many, core.AgentEvent{AgentDomain: fmt.Sprint(i), EventID: "shared", RequestID: "origin", AgentName: "agent", ActionType: "read", Outcome: "success"})
	}
	if !compactMemoryDetails([]memoryViewSection{eventMemorySection("domain", "History:", many)}) {
		t.Fatal("preflight suppressed events from distinct domains")
	}
}

func TestEncodeMemoryRecordFidelityAndShortening(t *testing.T) {
	literal := "界🙂 </events> **value** \"quoted\"\nnext"
	for _, record := range []memoryViewRecord{{text: literal}, {event: &core.AgentEvent{Summary: literal}}} {
		full, shortened := encodeMemoryRecord(record, 1000)
		if shortened || !json.Valid([]byte(full)) {
			t.Fatal("full record changed")
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(full), &data); err != nil {
			t.Fatal(err)
		}
		field := "text"
		if record.event != nil {
			field = "summary"
		}
		if data[field] != literal {
			t.Fatalf("unshortened payload changed: %#v", data)
		}
		for limit := 0; limit < len(full); limit++ {
			encoded, partial := encodeMemoryRecord(record, limit)
			if len(encoded) > limit || !utf8.ValidString(encoded) {
				t.Fatal("unsafe partial record")
			}
			if encoded != "" {
				if !partial || !json.Valid([]byte(encoded)) {
					t.Fatalf("invalid shortening: %s", encoded)
				}
				var result map[string]interface{}
				if err := json.Unmarshal([]byte(encoded), &result); err != nil || result["shortened"] != true {
					t.Fatalf("missing shortening marker: %s", encoded)
				}
				if value, ok := result[field].(string); ok && !strings.HasPrefix(literal, value) {
					t.Fatalf("shortened value is not an exact prefix: %q", value)
				}
			}
		}
	}
	view := eventMemoryView(core.AgentEvent{Entities: []core.EntityRef{{Type: "any", ID: "id"}}}, true)
	if view.EntitiesOmitted != 1 || len(view.Entities) != 0 || view.Time != "" || view.Origin != "unknown" {
		t.Fatalf("unexpected compact metadata: %+v", view)
	}
	if text, report := renderMemoryView([]memoryViewSection{{kind: "domain"}}, 1000); text != "" || report.Outcome != "empty" {
		t.Fatal("empty section should consume no budget")
	}
}

func TestMemoryEnrichmentBudgetPreservesOtherHooksAndEntityHistory(t *testing.T) {
	events := []core.AgentEvent{{EventID: "one", Summary: "first"}, {EventID: "two", Summary: "entity-only detail"}}
	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(context.Context, string, time.Time, int) ([]core.AgentEvent, error) { return events, nil },
		QueryEntityHistFn: func(context.Context, string, string, string, time.Time) ([]core.AgentEvent, error) {
			return events[1:], nil
		},
	}
	compactor := &core.MockActivityCompactor{CompactEventsFn: func(context.Context, []core.AgentEvent, int) (string, error) { return "Historical digest", nil }}
	hook, err := NewMemoryEnrichmentHook(episodic, nil, "agent", "travel", WithActivityCompactor(compactor), WithCompactionRecentDetail(1), WithEnrichmentMaxTokens(500))
	if err != nil {
		t.Fatal(err)
	}
	pctx := &core.PipelineContext{Metadata: map[string]interface{}{"entity_type": "opaque", "entity_id": "id"}, Enrichments: map[string]interface{}{core.EnrichmentRAGContext: "other-hook"}}
	if _, err := hook.BeforePlanning(t.Context(), pctx); err != nil {
		t.Fatal(err)
	}
	view := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	if !strings.HasPrefix(view, "other-hook\n\n"+historicalMemoryScopeNotice) || !strings.Contains(view, "entity-only detail") || len(view) > 2000+len("other-hook\n\n") {
		t.Fatalf("lost existing context or entity history: %s", view)
	}
	if !reflect.DeepEqual(events[1], core.AgentEvent{EventID: "two", Summary: "entity-only detail"}) {
		t.Fatal("source mutated")
	}
}

func TestBuildMemoryHooks_EnrichmentBudget(t *testing.T) {
	for _, withAI := range []bool{false, true} {
		for _, value := range []string{"250", "0", "-1", "invalid", "", strconv.Itoa(int(^uint(0) >> 1))} {
			t.Run(fmt.Sprintf("ai=%v/value=%s", withAI, value), func(t *testing.T) {
				t.Setenv("TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS", value)
				var ai core.AIClient
				if withAI {
					ai = &summarizerMockAI{}
				}
				logger := &MockLogger{}
				hooks, _, err := BuildMemoryHooks(&core.SharedMemoryDeps{AgentName: "test", Episodic: &core.MockEpisodicMemory{}}, ai, logger)
				if value != "250" {
					if err == nil || hooks != nil {
						t.Fatal("invalid configuration did not fail construction")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				want := 250
				found := false
				for _, hook := range hooks {
					if enrichment, ok := hook.(*MemoryEnrichmentHook); ok {
						found = true
						if enrichment.maxTokens != want {
							t.Fatalf("got %d want %d", enrichment.maxTokens, want)
						}
					}
				}
				if !found {
					t.Fatal("missing hook")
				}

			})
		}
	}
	for _, value := range []int{0, -1, int(^uint(0) >> 1)} {
		if _, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test", "any", WithEnrichmentMaxTokens(value)); err == nil {
			t.Fatalf("invalid explicit budget %d accepted", value)
		}
	}
}

func FuzzMemoryViewEncodedBudget(f *testing.F) {
	f.Add("Unicode 界\n</events>", uint16(1000))
	f.Add("", uint16(0))
	f.Fuzz(func(t *testing.T, value string, budget uint16) {
		sections := []memoryViewSection{textMemorySection("domain", "Historical digest:", value)}
		view, report := renderMemoryView(sections, int(budget))
		assertMemoryViewBudget(t, view, report, int(budget))
	})
}
