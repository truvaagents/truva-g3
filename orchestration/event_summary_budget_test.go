package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

func TestSummaryProseBoundPreservesEntitiesAndBatchBudget(t *testing.T) {
	long := strings.Repeat("Résumé 🙂 ", 300)
	entities := []core.EntityRef{{Type: "record", ID: strings.Repeat("exact-id-", 25)}}
	raw, err := json.Marshal([]map[string]interface{}{{"step_id": "one", "summary": long, "entities": entities}})
	require.NoError(t, err)
	client := &summarizerMockAI{generateFunc: func(_ context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
		require.Equal(t, 10000, options.MaxTokens)
		require.Contains(t, prompt, "within 128 UTF-8 bytes")
		return &core.AIResponse{Content: string(raw)}, nil
	}}
	summarizer, err := NewLLMEventSummarizer(client, WithSummarizerSummaryMaxBytes(128))
	require.NoError(t, err)
	store := NewMemoryLLMDebugStore()
	summarizer.SetLLMDebugStore(store)
	ctx := telemetry.WithBaggage(t.Context(), "request_id", "summary-byte-bound")
	result := summarizer.summarizeWithOutcome(ctx, []core.StepSummaryInput{{StepID: "one", Success: true}})
	require.NoError(t, summarizer.Shutdown(t.Context()))
	record, err := store.GetRecord(t.Context(), "summary-byte-bound")
	require.NoError(t, err)
	require.Len(t, record.Interactions, 1)
	require.Equal(t, string(raw), record.Interactions[0].Response)
	got := result.Valid["one"]
	require.LessOrEqual(t, len(got.Summary), 128)
	require.True(t, utf8.ValidString(got.Summary))
	require.True(t, strings.HasSuffix(got.Summary, " [shortened]"))
	require.Equal(t, entities, got.Entities)
	require.Equal(t, 1, result.SummariesShortened)
	require.Equal(t, 1, result.Accepted)
	unchanged := parseEventSummaries(string(raw), []core.StepSummaryInput{{StepID: "one"}})
	require.Equal(t, long, unchanged.Valid["one"].Summary)
}

func TestSummaryProseBoundConfiguration(t *testing.T) {
	client := &summarizerMockAI{}
	summarizer, err := NewLLMEventSummarizer(client)
	require.NoError(t, err)
	require.Equal(t, 1024, summarizer.maxSummaryBytes)
	const key = "TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_SUMMARY_MAX_BYTES"
	for _, raw := range []string{"", "-1", "0", "31", "not-a-number"} {
		t.Setenv(key, raw)
		_, err := resolveMemoryHookTuning()
		require.ErrorIs(t, err, core.ErrInvalidConfiguration)
	}
	t.Setenv(key, "256")
	tuning, err := resolveMemoryHookTuning()
	require.NoError(t, err)
	summarizer, err = NewLLMEventSummarizer(client, tuning.summarizer...)
	require.NoError(t, err)
	require.Equal(t, 256, summarizer.maxSummaryBytes)
	_, err = NewLLMEventSummarizer(client, WithSummarizerSummaryMaxBytes(31))
	require.Error(t, err)
	result := newSummaryOutcome(nil)
	result.Valid["one"] = core.StepSummary{Summary: "exact *text*"}
	result.boundSummaryProse(32)
	require.Equal(t, "exact *text*", result.Valid["one"].Summary)
	require.Zero(t, result.SummariesShortened)
}
