package orchestration

import (
	"context"

	"github.com/truvaagents/truva-g3/telemetry"
)

// LLMCallRecorderAdapter wraps an LLMDebugStore as a telemetry.LLMCallRecorder.
// This allows agents to record LLM calls to the same store used by the orchestrator.
type LLMCallRecorderAdapter struct {
	store LLMDebugStore
}

// NewLLMCallRecorderAdapter creates an adapter that bridges telemetry.LLMCallRecorder
// to orchestration.LLMDebugStore. The agent creates a RedisLLMDebugStore and wraps
// it with this adapter to record LLM calls.
func NewLLMCallRecorderAdapter(store LLMDebugStore) telemetry.LLMCallRecorder {
	return &LLMCallRecorderAdapter{store: store}
}

// RecordLLMCall converts a telemetry.LLMCallRecord to orchestration.LLMInteraction
// and writes it to the debug store.
func (a *LLMCallRecorderAdapter) RecordLLMCall(ctx context.Context, requestID string, record telemetry.LLMCallRecord) error {
	if requestID == "" {
		return nil // Silently skip — not called from orchestration
	}

	interaction := LLMInteraction{
		Type:             record.CallType,
		SourceComponent:  record.SourceComponent,
		CallDescription:  record.Description,
		StepID:           record.StepID,
		Timestamp:        record.Timestamp,
		DurationMs:       record.DurationMs,
		Prompt:           record.Prompt,
		SystemPrompt:     record.SystemPrompt,
		Temperature:      record.Temperature,
		MaxTokens:        record.MaxTokens,
		Model:            record.Model,
		Provider:         record.Provider,
		Response:         record.Response,
		PromptTokens:     record.PromptTokens,
		CompletionTokens: record.CompletionTokens,
		TotalTokens:      record.TotalTokens,
		Success:          record.Success,
		Error:            record.Error,
		Attempt:          1, // Agent-side calls don't have retry visibility
		PhaseNumber:      record.PhaseNumber,
	}

	return a.store.RecordInteraction(ctx, requestID, interaction)
}

// Verify interface compliance
var _ telemetry.LLMCallRecorder = (*LLMCallRecorderAdapter)(nil)
