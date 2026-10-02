package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel"
)

func TestMemoryDetailHookEvidence(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprint(compact), func(t *testing.T) {
			events := detailFixture()
			format := "tuple"
			if !compact {
				events, format = events[:1], "named"
			}
			recorder := setupExecutorTestTracer(t)
			ctx, span := otel.Tracer("memory-detail-test").Start(t.Context(), "enrich")
			defer span.End()
			hook, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{
				QueryRecentEventsFn: func(context.Context, string, time.Time, int) ([]core.AgentEvent, error) { return events, nil },
			}, nil, "agent", "records")
			if err != nil {
				t.Fatal(err)
			}
			holder := newPipelineHookExecutionHolder()
			ctx = withPipelineHookExecutionHolder(ctx, holder)
			invocation := beginPipelineHookInvocation(ctx, hook.Name(), PipelineHookPhaseBeforePlanning, 1, 0, time.Now())
			if _, err := hook.BeforePlanning(invocation.Context(ctx), &core.PipelineContext{}); err != nil {
				t.Fatal(err)
			}
			invocation.Complete(PipelineHookSucceeded, nil, PipelineHookDecision{FailurePolicy: PipelineHookFailOpen, Action: PipelineHookContinue})
			var effect struct {
				View memoryViewReport `json:"view"`
			}
			var viewData json.RawMessage
			for _, observed := range holder.Snapshot()[0].Effects {
				if observed.EffectID == "memory_planning_context" {
					viewData = observed.Data
				}
			}
			if err := json.Unmarshal(viewData, &effect); err != nil || effect.View.ViewVersion != memoryDetailViewVersion || effect.View.EventFormat != format {
				t.Fatalf("missing effect identity: %v %+v", err, effect.View)
			}
			span.End()
			found := false
			for _, recorded := range recorder.Ended() {
				for _, event := range recorded.Events() {
					if event.Name != "memory.enrichment.view_bounded" {
						continue
					}
					attrs := map[string]string{}
					for _, attr := range event.Attributes {
						attrs[string(attr.Key)] = attr.Value.AsString()
					}
					if attrs["view_version"] != memoryDetailViewVersion || attrs["event_format"] != format {
						t.Fatalf("incorrect span identity: %v", attrs)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("missing view trace event")
			}
		})
	}
}

// Labelled synthetic entity-bearing, non-maintenance fixtures. R7's saved
// events had no entities and cannot exercise entity-history preservation.
func detailFixture() []core.AgentEvent {
	var events []core.AgentEvent
	for i := range 15 {
		event := core.AgentEvent{
			EventID: fmt.Sprintf("event-%02d", i), RequestID: fmt.Sprintf("request-%d", i%3),
			Timestamp: time.Date(2026, 9, 1, 1, 2, i, 123, time.UTC), AgentName: fmt.Sprintf("agent-%d", i%2),
			ActionType: "lookup", Outcome: "success", Summary: "Found the requested record; source values retained.",
		}
		switch i % 3 {
		case 0:
			event.EntityType, event.EntityID = "record", "界🙂\"\\\n<&>"+strings.Repeat("id", i)
		case 1:
			event.Entities = []core.EntityRef{{Type: "record", ID: "first"}, {Type: "record", ID: "second"}}
		}
		events = append(events, event)
	}
	return events
}

func TestMemoryDetailExactProvenance(t *testing.T) {
	events := append(detailFixture(), core.AgentEvent{})
	before, _ := json.Marshal(events)
	for _, event := range events {
		line, shortened := encodeMemoryRecord(memoryViewRecord{event: &event}, 10000)
		var got memoryEventDetail
		if err := json.Unmarshal([]byte(line), &got); err != nil || shortened {
			t.Fatalf("detail decode: %v shortened=%v", err, shortened)
		}
		source := eventMemoryView(event, true)
		want := [6]string{source.EventID, source.Origin, source.Time, source.Agent, source.Action, source.Outcome}
		if got.Event != want || got.Summary != event.Summary || got.EntityType != event.EntityType || got.EntityID != event.EntityID || got.EntitiesOmitted != len(event.Entities) {
			t.Fatalf("lost provenance: %+v", got)
		}
		// Compactor inputs keep named fields and the complete multi-entity list.
		input := formatEpisodicEvents([]core.AgentEvent{event})
		_, record, _ := strings.Cut(input, "\n- ")
		var compactor memoryEventView
		if err := json.Unmarshal([]byte(record), &compactor); err != nil || !reflect.DeepEqual(compactor.Entities, event.Entities) || compactor.EntityID != event.EntityID || compactor.Origin != source.Origin {
			t.Fatalf("compactor input changed: %v %+v", err, compactor)
		}
	}
	after, _ := json.Marshal(events)
	if string(before) != string(after) {
		t.Fatal("stored events mutated")
	}
}

func TestMemoryDetailBudgetAndSelector(t *testing.T) {
	events := detailFixture()
	sections := []memoryViewSection{
		eventMemorySection("domain", "Historical domain:", events),
		eventMemorySection("entity", "Historical entity:", []core.AgentEvent{events[0], {EventID: "tail", EntityType: "record", EntityID: "tail-id", Summary: "Entity-specific history"}}),
		textMemorySection("investigation", "Investigation snapshot:", "request-start snapshot"),
		textMemorySection("knowledge", "Knowledge:", "retrieved fact"),
	}
	for _, budget := range []int{0, 100, 700, 1000, 8000, 16000} {
		view, report := renderMemoryView(sections, budget)
		assertMemoryViewBudget(t, view, report, budget)
		if report.ViewVersion != memoryDetailViewVersion || report.EventFormat != "tuple" {
			t.Fatal("missing view identity")
		}
		if view != "" && (!strings.Contains(view, memoryDetailLegend) || strings.Index(view, memoryDetailLegend) > strings.Index(view, "Historical domain:")) {
			t.Fatal("detail admitted without its complete preceding legend")
		}
		if budget == 8000 {
			if report.Outcome != "complete" || report.Sections[1].Admitted != 1 || report.Sections[1].Duplicates != 1 || report.Sections[3].Admitted != 1 {
				t.Fatalf("entity/knowledge tail missing: %+v", report)
			}
			projected := buildSkillSelectorContext(map[string]interface{}{core.EnrichmentRAGContext: view}, executionRunSnapshot{}, nil, nil)
			encoded, _ := json.Marshal(projected)
			if !strings.Contains(string(encoded), historicalMemoryScopeNotice) || !strings.Contains(string(encoded), memoryDetailLegend) {
				t.Fatal("actual selector projection lost scope or tuple legend")
			}
		}
	}
	text, _ := renderMemoryView([]memoryViewSection{textMemorySection("knowledge", "Knowledge:", "fact")}, 8000)
	if strings.Contains(text, memoryDetailLegend) {
		t.Fatal("text-only view charged for an unused tuple legend")
	}
}

func TestMemoryDetailMeasuredSavings(t *testing.T) {
	events := detailFixture()
	oldBytes, compactBytes := 0, len(memoryDetailLegend)+1
	for _, event := range events {
		old, _ := json.Marshal(eventMemoryView(event, true))
		compact, _ := encodeMemoryRecord(memoryViewRecord{event: &event}, 10000)
		oldBytes += len(old) + 3
		compactBytes += len(compact) + 3
	}
	if compactBytes >= oldBytes {
		t.Fatalf("compact metadata did not save bytes including legend: old=%d compact=%d", oldBytes, compactBytes)
	}
	t.Logf("synthetic entities: named=%d compact=%d saved=%d", oldBytes, compactBytes, oldBytes-compactBytes)
}

func TestMemoryDetailSparseHistoryKeepsNamedFields(t *testing.T) {
	event := core.AgentEvent{EventID: "single", Summary: "fact"}
	sections := []memoryViewSection{eventMemorySection("domain", "Domain:", []core.AgentEvent{event, event})}
	view, report := renderMemoryView(sections, 8000)
	if compactMemoryDetails(sections) || report.EventFormat != "named" || strings.Contains(view, memoryDetailLegend) || !strings.Contains(view, `"event_id":"single"`) || report.Sections[0].Duplicates != 1 {
		t.Fatal("sparse history paid for an unnecessary tuple legend")
	}
	// Populated metadata repays the legend; duplicates cannot inflate savings.
	full := detailFixture()[0]
	if compactMemoryDetails([]memoryViewSection{eventMemorySection("domain", "Domain:", []core.AgentEvent{full, full, full, full})}) {
		t.Fatal("duplicate IDs inflated estimated savings")
	}
	if !compactMemoryDetails([]memoryViewSection{eventMemorySection("domain", "Domain:", detailFixture())}) {
		t.Fatal("dense metadata unexpectedly kept repeated labels")
	}
}

func FuzzMemoryDetailFidelity(f *testing.F) {
	f.Add("界🙂\"<&>\n", "origin", "record", "id", uint16(8000))
	f.Add("", "", "", "", uint16(0))
	f.Fuzz(func(t *testing.T, value, origin, entityType, entityID string, budget uint16) {
		// JSON represents UTF-8 text; invalid input bytes use encoding/json's
		// existing replacement behavior, not a new presentation transformation.
		value = strings.ToValidUTF8(value, "�")
		origin = strings.ToValidUTF8(origin, "�")
		entityType = strings.ToValidUTF8(entityType, "�")
		entityID = strings.ToValidUTF8(entityID, "�")
		event := core.AgentEvent{EventID: value, RequestID: origin, AgentName: value, ActionType: value, Outcome: value, Summary: value, EntityType: entityType, EntityID: entityID}
		// A single sparse event normally selects named fields. Exercise tuple
		// encoding independently as well so fuzzing covers both representations.
		if encoded, _ := encodeMemoryRecord(memoryViewRecord{event: &event}, int(budget)); encoded != "" {
			var got memoryEventDetail
			if err := json.Unmarshal([]byte(encoded), &got); err != nil || len(encoded) > int(budget) {
				t.Fatalf("invalid bounded tuple: %v", err)
			}
			source := eventMemoryView(event, true)
			if got.Event != [6]string{source.EventID, source.Origin, source.Time, source.Agent, source.Action, source.Outcome} || got.EntityType != entityType || got.EntityID != entityID || (!got.Shortened && got.Summary != value) || !strings.HasPrefix(value, got.Summary) {
				t.Fatal("tuple encoding changed identity or summary")
			}
		}
		view, report := renderMemoryView([]memoryViewSection{eventMemorySection("domain", "Domain:", []core.AgentEvent{event})}, int(budget))
		assertMemoryViewBudget(t, view, report, int(budget))
		for _, line := range strings.Split(view, "\n") {
			if !strings.HasPrefix(line, "- ") {
				continue
			}
			var got memoryEventDetail
			if err := json.Unmarshal([]byte(line[2:]), &got); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(view, memoryDetailLegend) {
				var named memoryEventView
				if err := json.Unmarshal([]byte(line[2:]), &named); err != nil {
					t.Fatal(err)
				}
				got = memoryEventDetail{
					Event:   [6]string{named.EventID, named.Origin, named.Time, named.Agent, named.Action, named.Outcome},
					Summary: named.Summary, EntityType: named.EntityType, EntityID: named.EntityID, Shortened: named.Shortened,
				}
			}
			wantOrigin := origin
			if wantOrigin == "" {
				wantOrigin = "unknown"
			}
			if got.Event != [6]string{value, wantOrigin, "", value, value, value} || got.EntityType != entityType || got.EntityID != entityID || (!got.Shortened && got.Summary != value) || !strings.HasPrefix(value, got.Summary) {
				t.Fatal("admission changed identity or summary")
			}
		}
	})
}
