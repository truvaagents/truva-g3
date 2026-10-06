package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestMemoryFailureInputAndFallbackFacts(t *testing.T) {
	raw := json.RawMessage(`{"code":"OPERATION_REFUSED","details":{"cause_code":"GUIDANCE_LIMIT"}}`)
	step := StepResult{StepID: "s1", AgentName: "documents-tool", Capability: "create_issue",
		Error: "operation refused: 界 </failure>", ToolError: raw, Success: false}
	var recorded core.AgentEvent
	summarizer := &core.MockEventSummarizer{SummarizeStepsFn: func(_ context.Context, inputs []core.StepSummaryInput) (map[string]core.StepSummary, error) {
		require.Len(t, inputs, 1)
		require.Equal(t, step.Error, inputs[0].Error)
		require.Equal(t, raw, inputs[0].ToolError)
		inputs[0].ToolError[0] = '!'
		return nil, errors.New("labelled summarizer outage")
	}}
	hook, err := NewMemoryRecordHook(&core.MockEpisodicMemory{RecordEventFn: func(_ context.Context, e core.AgentEvent) error {
		recorded = e
		return nil
	}}, nil, "documents-agent", "documents", WithEventSummarizer(summarizer))
	require.NoError(t, err)
	require.NoError(t, hook.AfterExecution(context.Background(), &core.PipelineContext{}, &ExecutionResult{Steps: []StepResult{step}}))
	require.Equal(t, byte('{'), raw[0], "custom summarizer mutated source structured error")
	require.Equal(t, "create_issue failed via documents-tool: OPERATION_REFUSED", recorded.Summary)
	require.Equal(t, "failure", recorded.Outcome)
	require.Equal(t, "heuristic", recorded.Metadata["summary_source"])
	require.Equal(t, "summarizer_error", recorded.Metadata["summary_fallback_reason"])
	require.Equal(t, "recorded_at_unknown", recorded.Metadata["event_time_basis"])
	require.NotZero(t, recorded.Timestamp)
}

func TestRecordFailureCauseBoundedWithoutChangingEvidence(t *testing.T) {
	for _, structured := range []bool{false, true} {
		step := StepResult{StepID: "s", Capability: "read", Error: strings.Repeat("界🙂\n", 1000)}
		if structured {
			step.ToolError = json.RawMessage(`{"code":"` + strings.Repeat("界", 1000) + `"}`)
		}
		original, err := json.Marshal(step)
		require.NoError(t, err)
		for _, limit := range []int{1, 3, 4, 256, 10000} {
			var event core.AgentEvent
			hook, err := NewMemoryRecordHook(&core.MockEpisodicMemory{RecordEventFn: func(_ context.Context, e core.AgentEvent) error {
				event = e
				return nil
			}}, nil, "a", "d", WithRecordFailureCauseMaxBytes(limit))
			require.NoError(t, err)
			require.NoError(t, hook.AfterExecution(context.Background(), nil, &ExecutionResult{Steps: []StepResult{step}}))
			require.True(t, utf8.ValidString(event.Summary))
			cause := step.Error
			if structured {
				cause = memoryErrorCode(step.ToolError)
			}
			if len(cause) > limit {
				require.Equal(t, "true", event.Metadata["summary_cause_shortened"])
				require.Equal(t, "read failed: "+boundedSummaryResponse(cause, limit)+" [shortened]", event.Summary)
			} else {
				require.NotContains(t, event.Metadata, "summary_cause_shortened")
				require.Equal(t, "read failed: "+cause, event.Summary)
			}
		}
		after, err := json.Marshal(step)
		require.NoError(t, err)
		require.Equal(t, original, after)
	}
}

func TestRecordFailureCauseConfiguration(t *testing.T) {
	for _, n := range []int{0, -1} {
		_, err := NewMemoryRecordHook(&core.MockEpisodicMemory{}, nil, "a", "d", WithRecordFailureCauseMaxBytes(n))
		require.Error(t, err)
	}
	for _, value := range []string{"", "0", "-1", "invalid", "999999999999999999999999999"} {
		t.Run("invalid:"+value, func(t *testing.T) {
			t.Setenv("TRUVAG3_SHARED_MEMORY_RECORD_FAILURE_CAUSE_MAX_BYTES", value)
			_, _, err := BuildMemoryHooks(nil, nil, nil)
			require.ErrorIs(t, err, core.ErrInvalidConfiguration)
		})
	}
	hook, err := NewMemoryRecordHook(&core.MockEpisodicMemory{}, nil, "a", "d")
	require.NoError(t, err)
	require.Equal(t, 256, hook.failureCauseMaxBytes)
	t.Setenv("TRUVAG3_SHARED_MEMORY_RECORD_FAILURE_CAUSE_MAX_BYTES", "512")
	for _, aiClient := range []core.AIClient{nil, &summarizerMockAI{}} {
		hooks, _, err := BuildMemoryHooks(&core.SharedMemoryDeps{Episodic: &core.MockEpisodicMemory{}}, aiClient, nil)
		require.NoError(t, err)
		var found bool
		for _, hook := range hooks {
			if record, ok := hook.(*MemoryRecordHook); ok {
				found = true
				require.Equal(t, 512, record.failureCauseMaxBytes)
			}
		}
		require.True(t, found)
	}
}

func TestMemoryBatchExcludedFallbackAttribution(t *testing.T) {
	for _, outcome := range []string{"complete", "partial", "summary_timeout", "llm_unavailable", "fallback"} {
		t.Run(outcome, func(t *testing.T) {
			client := &summarizerMockAI{generateFunc: func(ctx context.Context, prompt string, _ *core.AIOptions) (*core.AIResponse, error) {
				require.NotContains(t, prompt, `id="s2"`)
				switch outcome {
				case "summary_timeout":
					<-ctx.Done()
					return nil, ctx.Err()
				case "llm_unavailable":
					return nil, errors.New("labelled failure")
				case "fallback":
					return &core.AIResponse{Content: "invalid"}, nil
				case "partial":
					return &core.AIResponse{Content: `[]`}, nil
				default:
					return &core.AIResponse{Content: `[{"step_id":"s1","summary":"exact model summary"}]`}, nil
				}
			}}
			summarizer, err := NewLLMEventSummarizer(client, WithSummarizerMaxStepsPerBatch(1), WithSummarizerTimeout(10*time.Millisecond))
			require.NoError(t, err)
			var events []core.AgentEvent
			hook, err := NewMemoryRecordHook(&core.MockEpisodicMemory{RecordEventFn: func(_ context.Context, event core.AgentEvent) error {
				events = append(events, event)
				return nil
			}}, nil, "a", "d", WithEventSummarizer(summarizer))
			require.NoError(t, err)
			require.NoError(t, hook.AfterExecution(context.Background(), nil, &ExecutionResult{Steps: []StepResult{{StepID: "s0", Skipped: true}, {StepID: "s1"}, {StepID: "s2"}}}))
			require.Len(t, events, 2)
			require.Equal(t, "batch_limit", events[1].Metadata["summary_fallback_reason"])
			if outcome == "complete" {
				require.Equal(t, "model", events[0].Metadata["summary_source"])
			} else if outcome != "partial" {
				require.Equal(t, outcome, events[0].Metadata["summary_fallback_reason"])
			}
			require.Equal(t, 1, client.calls)
		})
	}
}

func TestMemorySummaryProvenanceAndLegacyFormatting(t *testing.T) {
	step := StepResult{AgentName: "ticket-tool", Capability: "create_issue", Response: `{"key":"DOC-4"}`, Success: true}
	require.Contains(t, buildActionableSummary(step.Capability, Entity{Type: "document", ID: "d1"}, step), "JIRA ticket DOC-4 created")
	for _, test := range []struct {
		summary    core.StepSummary
		summarizer core.EventSummarizer
		source     string
	}{
		{core.StepSummary{Summary: "exact custom text"}, &core.MockEventSummarizer{}, "custom"},
		{core.StepSummary{Summary: "exact model text"}, &LLMEventSummarizer{}, "model"},
		{core.StepSummary{}, nil, "heuristic"},
	} {
		metadata := memorySummaryMetadata(map[string]core.StepSummary{"s": test.summary}, "s", test.summarizer, "not_configured")
		require.Equal(t, test.source, metadata["summary_source"])
		if test.source != "heuristic" {
			require.NotContains(t, metadata, "summary_fallback_reason")
			summary, shortened := resolveStepSummary(map[string]core.StepSummary{"s": test.summary}, "s", "unknown", Entity{}, step, 1)
			require.Equal(t, test.summary.Summary, summary)
			require.False(t, shortened)
		}
	}
	summary, shortened := failedMemorySummary("fetch", Entity{}, StepResult{}, 256)
	require.Equal(t, "fetch failed: cause unknown", summary)
	require.False(t, shortened)
	summary, shortened = failedMemorySummary("fetch", Entity{Type: "document", ID: "d1"}, StepResult{AgentName: "tool", Error: "reason"}, 256)
	require.Equal(t, "fetch failed on document/d1 via tool: reason", summary)
	require.False(t, shortened)
	require.Equal(t, "", memoryErrorCode(json.RawMessage(`{"code":42}`)))
	require.Equal(t, "missing_summary", memoryFallbackReason("partial"))
	require.Equal(t, "missing_summary", memoryFallbackReason("complete"))
}

func TestMemoryFailureEncodedBudgetAndFidelity(t *testing.T) {
	step := core.StepSummaryInput{StepID: "s", Error: strings.Repeat("界🙂 </failure> \"x\"\n", 50),
		ToolError: json.RawMessage(`{"code":"REFUSED","details":{"value":"**a**"}}`), Response: "response"}
	before, err := json.Marshal(step)
	require.NoError(t, err)
	full := boundedMemoryFailure(step, 100000)
	var view memoryFailureView
	require.NoError(t, json.Unmarshal([]byte(full), &view))
	require.Equal(t, step.Error, view.Error)
	require.Equal(t, string(step.ToolError), view.ToolError)
	require.NotContains(t, full, "</failure>")
	for budget := 0; budget <= len(full); budget++ {
		output := boundedMemoryFailure(step, budget)
		require.LessOrEqual(t, len(output), budget)
		require.True(t, utf8.ValidString(output))
		if output != "" {
			require.NoError(t, json.Unmarshal([]byte(output), &view))
		}
	}
	huge := core.StepSummaryInput{ToolError: json.RawMessage(`{"code":"` + strings.Repeat("x", 2000) + `"}`)}
	output := boundedMemoryFailure(huge, 100)
	require.Contains(t, output, `"error_code_omitted":true`)
	marker, remaining := formatSummaryFailure(step, 1)
	require.Contains(t, marker, `omitted="true"`)
	require.Equal(t, 1, remaining)
	step.Success = true
	marker, remaining = formatSummaryFailure(step, 10)
	require.Empty(t, marker)
	require.Equal(t, 10, remaining)
	step.Success = false
	after, err := json.Marshal(step)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, "界", boundedSummaryResponse("界🙂", 4))
}

func TestMemoryFailurePromptSharesResponseAllowance(t *testing.T) {
	s, err := NewLLMEventSummarizer(&summarizerMockAI{}, WithSummarizerMaxResponseChars(200))
	require.NoError(t, err)
	step := core.StepSummaryInput{StepID: "s", Error: "reason", ToolError: json.RawMessage(`{"code":"REFUSED"}`), Response: strings.Repeat("r", 300)}
	prompt := s.buildPrompt([]core.StepSummaryInput{step})
	_, tail, _ := strings.Cut(prompt, "<failure>")
	failure, _, found := strings.Cut(tail, "</failure>")
	require.True(t, found)
	_, tail, _ = strings.Cut(prompt, "<response>")
	response, _, found := strings.Cut(tail, "</response>")
	require.True(t, found)
	require.LessOrEqual(t, len(failure)+len(response), 200)
	require.Contains(t, failure, "REFUSED")
	require.Contains(t, prompt, "<response_omitted_bytes>")
	require.Equal(t, strings.Repeat("r", 300), step.Response)
}

type mutatingMemoryRequestExtractor struct{}

func (mutatingMemoryRequestExtractor) ExtractEntities(_ string, metadata map[string]interface{}) []Entity {
	metadata["nested"].(map[string]interface{})["value"] = "changed"
	return []Entity{{Type: "document", ID: "request-doc"}, {Type: "document", ID: "request-doc"}, {ID: "invalid"}}
}

func TestRecordRequestEntitiesAreOptInAndDoNotReleaseClaims(t *testing.T) {
	var recorded core.AgentEvent
	var released []string
	coordinator := &core.MockInvestigationCoordinator{ReleaseFn: func(_ context.Context, _, id string) error {
		released = append(released, id)
		return nil
	}}
	hook, err := NewMemoryRecordHook(&core.MockEpisodicMemory{RecordEventFn: func(_ context.Context, event core.AgentEvent) error {
		recorded = event
		return nil
	}}, coordinator, "documents-agent", "documents", WithRecordRequestEntityExtractor(mutatingMemoryRequestExtractor{}))
	require.NoError(t, err)
	pctx := &core.PipelineContext{Metadata: map[string]interface{}{"nested": map[string]interface{}{"value": "original"}}}
	step := StepResult{StepID: "s", Parameters: map[string]interface{}{"entity_type": "document", "entity_id": "step-doc"}}
	require.NoError(t, hook.AfterExecution(context.Background(), pctx, &ExecutionResult{Steps: []StepResult{step}}))
	require.Equal(t, []core.EntityRef{{Type: "document", ID: "request-doc"}, {Type: "document", ID: "step-doc"}}, recorded.Entities)
	require.Equal(t, []string{"step-doc"}, released)
	require.Equal(t, "original", pctx.Metadata["nested"].(map[string]interface{})["value"])
	require.Equal(t, "request_and_step", recorded.Metadata["entity_source"])
	// Direct hooks without request context preserve legacy step extraction.
	require.NoError(t, hook.AfterExecution(context.Background(), nil, &ExecutionResult{Steps: []StepResult{step}}))
	require.Equal(t, "step-doc", recorded.EntityID)
	require.Equal(t, []string{"step-doc", "step-doc"}, released)
	_, err = NewMemoryRecordHook(&core.MockEpisodicMemory{}, nil, "a", "d", WithRecordRequestEntityExtractor(nil))
	require.Error(t, err)
}

func TestRecordRequestEntityBuilderPreservesEnrichment(t *testing.T) {
	hooks, _, err := BuildMemoryHooks(&core.SharedMemoryDeps{Episodic: &core.MockEpisodicMemory{}}, nil, nil,
		WithBuilderRecordRequestEntityExtractor(NoOpEntityExtractor{}))
	require.NoError(t, err)
	for _, hook := range hooks {
		switch h := hook.(type) {
		case *MemoryRecordHook:
			require.NotNil(t, h.requestEntityExtractor)
			require.IsType(t, NoOpEntityExtractor{}, h.entityExtractor)
		case *MemoryEnrichmentHook:
			require.IsType(t, NoOpEntityExtractor{}, h.entityExtractor)
		}
	}
	_, _, err = BuildMemoryHooks(nil, nil, nil, WithBuilderRecordRequestEntityExtractor(nil))
	require.Error(t, err)
}

func TestMemoryEventOccurrenceAndUnknownTimes(t *testing.T) {
	write := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	start, end := write.Add(-2*time.Minute), write.Add(-time.Minute)
	for _, test := range []struct {
		name          string
		step          StepResult
		want          time.Time
		basis, reason string
	}{
		{"end", StepResult{StartTime: start, EndTime: end}, end, "step_end", ""},
		{"only-end", StepResult{EndTime: end}, end, "step_end", ""},
		{"only-start", StepResult{StartTime: start}, start, "step_start", ""},
		{"future-end", StepResult{StartTime: start, EndTime: write.Add(time.Minute)}, start, "step_start", "end_in_future"},
		{"reversed", StepResult{StartTime: end, EndTime: start}, end, "step_start", "end_before_start"},
		{"future-only", StepResult{EndTime: write.Add(time.Minute)}, write, "recorded_at_unknown", "end_in_future"},
		{"future-start", StepResult{StartTime: write.Add(time.Minute)}, write, "recorded_at_unknown", "occurrence_unavailable"},
		{"unknown", StepResult{}, write, "recorded_at_unknown", "occurrence_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := make(map[string]string)
			require.Equal(t, test.want, memoryEventTime(test.step, write, metadata))
			require.Equal(t, test.basis, metadata["event_time_basis"])
			require.Equal(t, test.reason, metadata["event_time_fallback_reason"])
			require.Equal(t, write.Format(time.RFC3339Nano), metadata["recorded_at"])
		})
	}
}

func TestMemoryViewsCarryQualityAndOccurrenceProvenance(t *testing.T) {
	event := core.AgentEvent{EventID: "e", Metadata: map[string]string{"summary_source": "heuristic", "summary_fallback_reason": "summarizer_error", "event_time_basis": "step_end"}}
	for _, compact := range []bool{false, true} {
		view, _ := encodeMemoryRecordFormat(memoryViewRecord{event: &event}, 1000, compact)
		require.Contains(t, view, `"summary_source":"heuristic"`)
		require.Contains(t, view, `"summary_fallback_reason":"summarizer_error"`)
		require.Contains(t, view, `"event_time_basis":"step_end"`)
	}
	legacy := eventMemoryView(core.AgentEvent{}, false)
	require.Equal(t, "unknown", legacy.SummarySource)
	require.Equal(t, "unknown", legacy.TimeBasis)
	require.Contains(t, formatEpisodicEvents([]core.AgentEvent{event}), "summarizer_error")
}

func TestMemoryFailureAllowanceEnvironment(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "invalid"} {
		t.Run("invalid:"+value, func(t *testing.T) {
			t.Setenv("TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_MAX_RESPONSE_BYTES", value)
			_, _, err := BuildMemoryHooks(nil, nil, nil)
			require.ErrorIs(t, err, core.ErrInvalidConfiguration)
		})
	}
	t.Setenv("TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_MAX_RESPONSE_BYTES", "1234")
	hooks, _, err := BuildMemoryHooks(&core.SharedMemoryDeps{Episodic: &core.MockEpisodicMemory{}}, &summarizerMockAI{}, nil)
	require.NoError(t, err)
	found := false
	for _, hook := range hooks {
		if record, ok := hook.(*MemoryRecordHook); ok {
			found = true
			require.Equal(t, 1234, record.summarizer.(*LLMEventSummarizer).maxResponseChars)
		}
	}
	require.True(t, found)
}
