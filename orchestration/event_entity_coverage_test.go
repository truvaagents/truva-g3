package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel"
)

func TestEventEntityFieldCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        summaryEntityCoverage
		legacy      int
	}{
		{"missing", "", summaryEntityCoverage{Observed: 1, Missing: 1}, 0},
		{"empty", `,"entities":[]`, summaryEntityCoverage{Observed: 1, Empty: 1}, 0},
		{"null", `,"entities":null`, summaryEntityCoverage{Observed: 1, Invalid: 1, Rejected: 1}, 0},
		{"object", `,"entities":{}`, summaryEntityCoverage{Observed: 1, Invalid: 1, Rejected: 1}, 1},
		{"string", `,"entities":"ORD"`, summaryEntityCoverage{Observed: 1, Invalid: 1, Rejected: 1}, 1},
		{"invalid_entries", `,"entities":[null,3,{"id":"x"}]`, summaryEntityCoverage{Observed: 1, Invalid: 1, Rejected: 3}, 3},
		{"valid", `,"entities":[{"type":"airport","id":"ORD"}]`, summaryEntityCoverage{Observed: 1, Valid: 1, Accepted: 1}, 0},
		{"mixed", `,"entities":[{"type":"airport","id":"ORD"},null]`, summaryEntityCoverage{Observed: 1, Mixed: 1, Accepted: 1, Rejected: 1}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `[{"step_id":"one","summary":"literal **fact**"` + tc.field + `}]`
			got := parseEventSummaries(body, []core.StepSummaryInput{{StepID: "one"}})
			require.Equal(t, tc.want, got.EntityFields)
			require.Equal(t, tc.legacy, got.EntitiesRejected, "legacy batch semantics stay unchanged")
			require.Equal(t, "literal **fact**", got.Valid["one"].Summary)
			require.Len(t, got.Valid["one"].Entities, tc.want.Accepted)
			if tc.want.Accepted > 0 {
				require.Equal(t, core.EntityRef{Type: "airport", ID: "ORD"}, got.Valid["one"].Entities[0])
			}
			if tc.legacy == 0 {
				require.Equal(t, "complete", got.Outcome, "field coverage does not redefine summary completeness")
			}
		})
	}
}

func TestEventEntityCoverageRetainedEntriesOnly(t *testing.T) {
	entry := `{"step_id":"one","summary":"valid","entities":[{"type":"flight","id":"AB-42"}]}`
	batch := []core.StepSummaryInput{{StepID: "one"}, {StepID: "two"}}
	for _, tc := range []struct {
		name, body string
		want       summaryEntityCoverage
	}{
		{"duplicate", `[` + entry + `,` + entry + `,{"step_id":"two","summary":"ok","entities":[]}]`, summaryEntityCoverage{Observed: 1, Empty: 1}},
		{"third_duplicate", `[` + entry + `,` + entry + `,` + entry + `]`, summaryEntityCoverage{}},
		{"invalid_summary", `[{"step_id":"one","summary":"","entities":[{"type":"x","id":"y"}]}]`, summaryEntityCoverage{}},
		{"unknown_step", `[{"step_id":"other","summary":"ok","entities":[]}]`, summaryEntityCoverage{}},
		{"truncated", `[` + entry + `,{"step_id":"two","summary":"cut`, summaryEntityCoverage{Observed: 1, Valid: 1, Accepted: 1}},
		{"malformed", `[` + entry + `,]`, summaryEntityCoverage{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseEventSummaries(tc.body, batch)
			require.Equal(t, tc.want, got.EntityFields)
			require.Equal(t, got.Accepted, got.EntityFields.Observed)
			require.Equal(t, got.EntityFields, finishSummaryOutcome(got).EntityFields, "aggregation is idempotent")
		})
	}
}

func TestEventSummaryPromptEntityContractAndRawDebug(t *testing.T) {
	const body = `[{"step_id":"one","summary":"literal **fact**","entities":[{"type":"airport","id":"北\\A\"42"}]}]`
	ai := &summarizerMockAI{generateFunc: func(_ context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
		require.True(t, strings.HasSuffix(prompt, eventSummaryOutputContract))
		require.True(t, strings.HasSuffix(opts.SystemPrompt, eventSummaryOutputContract))
		require.NotContains(t, prompt, `"summary": "..."}, ...]`)
		require.Contains(t, opts.SystemPrompt, `"entities": []`)
		require.Contains(t, opts.SystemPrompt, `"entities": [{"type": "ticket", "id": "PROJ-42"}]`)
		return &core.AIResponse{Content: body}, nil
	}}
	summarizer, err := NewLLMEventSummarizer(ai)
	require.NoError(t, err)
	store := &mockLLMDebugStore{}
	summarizer.SetLLMDebugStore(store)
	got := summarizer.summarizeWithOutcome(WithRequestID(t.Context(), "entity-contract"), []core.StepSummaryInput{{StepID: "one"}})
	require.NoError(t, summarizer.Shutdown(t.Context()))
	require.Equal(t, 1, ai.calls)
	require.Equal(t, "北\\A\"42", got.Valid["one"].Entities[0].ID)
	require.Len(t, store.interactions, 1)
	require.Equal(t, body, store.interactions[0].Response)
}

func TestMemoryEntityCoverageEffectsAndSpans(t *testing.T) {
	for _, mode := range []string{"builtin", "custom", "heuristic"} {
		t.Run(mode, func(t *testing.T) {
			recorder := setupExecutorTestTracer(t)
			ctx, span := otel.Tracer("entity-coverage-test").Start(WithRequestID(t.Context(), "coverage-request"), "record")
			defer span.End()
			var events []core.AgentEvent
			backend := &core.MockEpisodicMemory{RecordEventFn: func(_ context.Context, event core.AgentEvent) error {
				events = append(events, event)
				return nil
			}}
			opts := []MemoryRecordOption{WithRecordEntityExtractor(LLMEntityExtractor{})}
			switch mode {
			case "builtin":
				summarizer, err := NewLLMEventSummarizer(&summarizerMockAI{generateFunc: func(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
					return &core.AIResponse{Content: `[{"step_id":"one","summary":"Forecast fetched"}]`}, nil
				}})
				require.NoError(t, err)
				opts = append(opts, WithEventSummarizer(summarizer))
			case "custom":
				opts = append(opts, WithEventSummarizer(&core.MockEventSummarizer{SummarizeStepsFn: func(context.Context, []core.StepSummaryInput) (map[string]core.StepSummary, error) {
					return map[string]core.StepSummary{"one": {Summary: "Forecast fetched"}}, nil
				}}))
			}
			hook, err := NewMemoryRecordHook(backend, nil, "weather-agent", "weather", opts...)
			require.NoError(t, err)
			holder := newPipelineHookExecutionHolder()
			ctx = withPipelineHookExecutionHolder(ctx, holder)
			invocation := beginPipelineHookInvocation(ctx, hook.Name(), PipelineHookPhaseAfterExecution, 1, 0, time.Now())
			result := &ExecutionResult{Steps: []StepResult{
				{StepID: "one", Capability: "forecast", Success: true},
				{StepID: "two", Capability: "read", Success: true, Parameters: map[string]interface{}{"entity_type": "station", "entity_id": "EXACT-ID"}},
			}}
			require.NoError(t, hook.AfterExecution(invocation.Context(ctx), &core.PipelineContext{}, result))
			invocation.Complete(PipelineHookSucceeded, nil, PipelineHookDecision{FailurePolicy: PipelineHookFailOpen, Action: PipelineHookContinue})
			require.Len(t, events, 2)
			require.Empty(t, events[0].EntityID, "missing entity field still records a domain event")
			require.Equal(t, "EXACT-ID", events[1].EntityID, "explicit metadata remains available on fallback")
			var effect struct {
				Coverage memorySummaryCoverage `json:"summary_coverage"`
			}
			require.NoError(t, json.Unmarshal(holder.Snapshot()[0].Effects[0].Data, &effect))
			require.Equal(t, mode, effect.Coverage.Source)
			if mode == "builtin" {
				require.NotNil(t, effect.Coverage.Batch)
				require.Equal(t, summaryEntityCoverage{Observed: 1, Missing: 1}, effect.Coverage.Batch.EntityFields)
			} else {
				require.Nil(t, effect.Coverage.Batch, "unobserved is not zero entity coverage")
			}
			span.End()
			seen := map[string]int{}
			for _, recorded := range recorder.Ended() {
				for _, event := range recorded.Events() {
					if event.Name != "memory.event_summarization.outcome" && event.Name != "memory.record.summary_coverage" {
						continue
					}
					seen[event.Name]++
					attributes := map[string]int64{}
					for _, attr := range event.Attributes {
						if strings.HasPrefix(string(attr.Key), "entity_fields.") {
							attributes[string(attr.Key)] = attr.Value.AsInt64()
						}
						require.NotContains(t, attr.Value.String(), "EXACT-ID")
					}
					if mode == "builtin" {
						require.Len(t, attributes, 8)
						require.EqualValues(t, 1, attributes["entity_fields.missing"])
						require.EqualValues(t, 1, attributes["entity_fields.observed"])
					} else {
						require.Empty(t, attributes)
					}
				}
			}
			require.Equal(t, 1, seen["memory.record.summary_coverage"])
			if mode == "builtin" {
				require.Equal(t, 1, seen["memory.event_summarization.outcome"])
			} else {
				require.Zero(t, seen["memory.event_summarization.outcome"])
			}
		})
	}
}
