package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel/trace"
)

func TestEventSummaryParserBoundaries(t *testing.T) {
	entry := `{"step_id":"one","summary":"literal **x** {nested} \\"}`
	batch := []core.StepSummaryInput{{StepID: "one"}, {StepID: "two"}}
	for _, tc := range []struct {
		name, body, outcome string
		accepted            int
		truncated           bool
	}{
		{"complete", `[` + entry + `,{"step_id":"two","summary":"ok"}]`, "complete", 2, false},
		{"prefix", `[` + entry + `,{"step_id":"two","summary":"unfinished`, "partial", 1, true},
		{"escape", `[` + entry + `,{"step_id":"two","summary":"unfinished\`, "partial", 1, true},
		{"between", `[` + entry + `,`, "partial", 1, true},
		{"no_close", `[` + entry, "partial", 1, true},
		{"separator", `[` + entry + ` {"step_id":"two","summary":"bad"}]`, "fallback", 0, false},
		{"trailing_comma", `[` + entry + `,]`, "fallback", 0, false},
		{"extra", `[` + entry + `] {}`, "fallback", 0, false},
		{"unknown", `[{"step_id":"other","summary":"bad"},` + entry + `]`, "partial", 1, false},
		{"duplicate", `[` + entry + `,` + entry + `,` + entry + `]`, "fallback", 0, false},
		{"bad_summary", `[` + entry + `,{"step_id":"two","summary":{}}]`, "partial", 1, false},
		{"invalid_entity", `[{"step_id":"one","summary":"ok","entities":[{"type":"airport","id":"ORD"},null,3,{"id":"missing"}]}]`, "partial", 1, false},
		{"empty", "", "fallback", 0, false},
		{"null", "null", "fallback", 0, false},
		{"prose", "Results: [" + entry + "] Done.", "partial", 1, false},
		{"prose_truncated", "Results: [" + entry, "fallback", 0, false},
		{"two_arrays", "Results: [" + entry + "] and []", "fallback", 0, false},
		{"fence", "```json\n[" + entry + "]\n```", "partial", 1, false},
		{"open_fence", "```JSON\n[" + entry, "partial", 1, true},
		{"fence_only_cut", "```json\n[" + entry + "]", "partial", 1, true},
		{"wrong_fence", "```go\n[" + entry + "]\n```", "fallback", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseEventSummaries(tc.body, batch)
			if got.Outcome != tc.outcome || got.Accepted != tc.accepted || got.Truncated != tc.truncated {
				t.Fatalf("got %+v, want %s/%d/truncated=%v", got, tc.outcome, tc.accepted, tc.truncated)
			}
		})
	}
}

func TestEventSummaryRecoveryTwelveOfTwenty(t *testing.T) {
	var batch []core.StepSummaryInput
	var entries []string
	for i := range 20 {
		id := fmt.Sprintf("s%d", i)
		batch = append(batch, core.StepSummaryInput{StepID: id})
		if i < 12 {
			entries = append(entries, fmt.Sprintf(`{"step_id":%q,"summary":"recorded fact"}`, id))
		}
	}
	got := parseEventSummaries("["+strings.Join(entries, ",")+`,{"step_id":"s12","summary":"cut`, batch)
	if got.Accepted != 12 || got.Expected != 20 || got.Outcome != "partial" || !got.Truncated {
		t.Fatalf("%+v", got)
	}
}

func TestMemorySummaryCoverageAndTrace(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprint(custom), func(t *testing.T) {
			var recorded []core.AgentEvent
			episodic := &core.MockEpisodicMemory{
				RecordEventFn: func(_ context.Context, event core.AgentEvent) error { recorded = append(recorded, event); return nil },
				QueryEntityHistFn: func(_ context.Context, _ string, kind, id string, _ time.Time) ([]core.AgentEvent, error) {
					var events []core.AgentEvent
					for _, e := range recorded {
						if e.EntityType == kind && e.EntityID == id {
							events = append(events, e)
						}
					}
					return events, nil
				},
			}
			ai := &summarizerMockAI{generateFunc: func(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
				return &core.AIResponse{Content: `[{"step_id":"one","summary":"Forecast ORD","entities":[{"type":"airport","id":"ORD"}]}]`}, nil
			}}
			builtin, err := NewLLMEventSummarizer(ai, WithSummarizerMaxStepsPerBatch(1))
			if err != nil {
				t.Fatal(err)
			}
			var summarizer core.EventSummarizer = builtin
			if custom {
				summarizer = &core.MockEventSummarizer{SummarizeStepsFn: func(context.Context, []core.StepSummaryInput) (map[string]core.StepSummary, error) {
					return map[string]core.StepSummary{"one": {Summary: "Forecast ORD", Entities: []core.EntityRef{{Type: "airport", ID: "ORD"}}}}, nil
				}}
			}
			hook, err := NewMemoryRecordHook(episodic, nil, "travel", "travel", WithEventSummarizer(summarizer), WithRecordEntityExtractor(LLMEntityExtractor{}))
			if err != nil {
				t.Fatal(err)
			}
			traceID, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
			spanID, _ := trace.SpanIDFromHex("0123456789abcdef")
			ctx := trace.ContextWithSpanContext(WithRequestID(t.Context(), "request-distinct"), trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID}))
			holder := newPipelineHookExecutionHolder()
			ctx = withPipelineHookExecutionHolder(ctx, holder)
			invocation := beginPipelineHookInvocation(ctx, hook.Name(), PipelineHookPhaseAfterExecution, 1, 0, time.Now())
			result := &ExecutionResult{Steps: []StepResult{
				{StepID: "one", Capability: "forecast", Parameters: map[string]interface{}{"airport": "ORD"}, Success: true},
				{StepID: "two", Capability: "read", Parameters: map[string]interface{}{"entity_type": "station", "entity_id": "B"}, Success: true},
				{StepID: "skip", Skipped: true},
			}}
			if err := hook.AfterExecution(invocation.Context(ctx), &core.PipelineContext{}, result); err != nil {
				t.Fatal(err)
			}
			invocation.Complete(PipelineHookSucceeded, nil, PipelineHookDecision{FailurePolicy: PipelineHookFailOpen, Action: PipelineHookContinue})
			var data struct {
				Coverage memorySummaryCoverage `json:"summary_coverage"`
			}
			if err := json.Unmarshal(holder.Snapshot()[0].Effects[0].Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Coverage.Eligible != 2 || data.Coverage.Summarized != 1 || data.Coverage.Fallback != 1 {
				t.Fatalf("%+v", data.Coverage)
			}
			if custom {
				if data.Coverage.Batch != nil || ai.calls != 0 {
					t.Fatal("invented custom parse metadata or extra call")
				}
			} else if data.Coverage.Batch == nil || data.Coverage.Batch.Outcome != "complete" || ai.calls != 1 {
				t.Fatalf("wrong submitted-batch evidence: %+v calls=%d", data.Coverage, ai.calls)
			}
			if len(recorded) != 2 || recorded[0].TraceID != traceID.String() || recorded[0].RequestID != "request-distinct" || recorded[1].EntityID != "B" {
				t.Fatalf("records: %+v", recorded)
			}
			enrich, err := NewMemoryEnrichmentHook(episodic, nil, "reader", "travel")
			if err != nil {
				t.Fatal(err)
			}
			pctx := &core.PipelineContext{Metadata: map[string]interface{}{"entity_type": "airport", "entity_id": "ORD"}}
			if _, err := enrich.BeforePlanning(t.Context(), pctx); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(pctx.Enrichments[core.EnrichmentRAGContext].(string), "Forecast ORD") {
				t.Fatal("entity-targeted recall lost the summary")
			}
		})
	}
	if memoryTraceID(WithRequestID(t.Context(), "not-a-trace")) != "" {
		t.Fatal("request ID stored as trace ID")
	}
}

func TestEmptyCustomCompactionDoesNotReplaceDigest(t *testing.T) {
	for _, cached := range []bool{false, true} {
		var saved []byte
		sets := 0
		if cached {
			saved, _ = json.Marshal(cachedDigestData{ViewVersion: activityDigestViewVersion, Content: "previous facts", LastEventTS: time.Now().Add(-time.Hour)})
		}
		events := []core.AgentEvent{{EventID: "new", Summary: "raw evidence", Timestamp: time.Now()}}
		hook, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{QueryRecentEventsFn: func(context.Context, string, time.Time, int) ([]core.AgentEvent, error) { return events, nil }}, nil, "reader", "travel",
			WithActivityCompactor(&core.MockActivityCompactor{CompactEventsFn: func(context.Context, []core.AgentEvent, int) (string, error) { return " \n ", nil }, UpdateDigestFn: func(context.Context, string, []core.AgentEvent, int) (string, error) { return " \n ", nil }}),
			WithDigestCache(&core.MockDigestCache{GetFn: func(context.Context, string) ([]byte, error) { return saved, nil }, SetFn: func(context.Context, string, []byte, time.Duration) error { sets++; return nil }}))
		if err != nil {
			t.Fatal(err)
		}
		pctx := &core.PipelineContext{}
		if _, err := hook.BeforePlanning(t.Context(), pctx); err != nil {
			t.Fatal(err)
		}
		if sets != 0 {
			t.Fatal("empty output replaced cache")
		}
		if !strings.Contains(pctx.Enrichments[core.EnrichmentRAGContext].(string), "raw evidence") {
			t.Fatal("lost raw fallback/detail")
		}
	}
}
