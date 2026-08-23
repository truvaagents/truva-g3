package telemetry

import (
	"context"
	"time"
)

// LLMCallRecord captures a single LLM call for debugging and observability.
// Used by both orchestration-level recording and agent-level recording.
type LLMCallRecord struct {
	// Identity
	CallType        string `json:"call_type"`                  // e.g., "agent_llm_call", "plan_generation"
	SourceComponent string `json:"source_component,omitempty"` // e.g., "research-assistant", "orchestrator"
	Description     string `json:"description,omitempty"`      // Human-readable: "Tool selection for research"
	StepID          string `json:"step_id,omitempty"`          // Orchestration step ID

	// Phase context (multi-phase iterative planning)
	// 0 = single-phase execution or Phase 1 (omitted in JSON).
	// 2+ = agent executing during a continuation phase.
	PhaseNumber int `json:"phase_number,omitempty"`

	// Timing
	Timestamp  time.Time `json:"timestamp"`
	DurationMs int64     `json:"duration_ms"`

	// Request
	Prompt       string  `json:"prompt"`
	SystemPrompt string  `json:"system_prompt,omitempty"`
	Temperature  float64 `json:"temperature"`
	MaxTokens    int     `json:"max_tokens"`
	Model        string  `json:"model,omitempty"`
	Provider     string  `json:"provider,omitempty"`

	// Response
	Response         string `json:"response"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`

	// Status
	Success bool `json:"success"`
	// Error is an observation-only string sanitized by the producing caller
	// before recording. Recorders store it verbatim and do not parse provider
	// payloads.
	Error string `json:"error,omitempty"`
}

// LLMCallRecorder records LLM calls for debugging.
// Implementations must be safe for concurrent use.
// Recording should be non-blocking (async) to avoid impacting LLM call latency.
type LLMCallRecorder interface {
	// RecordLLMCall appends an LLM call record to the debug trace for a request.
	// requestID correlates this call to the orchestration request.
	// If requestID is empty, implementations should silently skip recording.
	RecordLLMCall(ctx context.Context, requestID string, record LLMCallRecord) error
}

// NoOpLLMCallRecorder is a safe default that discards all recordings.
type NoOpLLMCallRecorder struct{}

func (n *NoOpLLMCallRecorder) RecordLLMCall(_ context.Context, _ string, _ LLMCallRecord) error {
	return nil
}
