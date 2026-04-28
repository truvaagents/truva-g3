package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Compile-time interface check.
var _ core.EventSummarizer = (*LLMEventSummarizer)(nil)

// eventSummarizerSystemPrompt is the system prompt for event summarization LLM calls.
// Structure follows docs/EFFECTIVE_PROMPTS_GUIDE.md:
//   - §2.8 (converged ordering: identity → instructions → example)
//   - §2.9 (system message for identity and rules)
//   - §2.10 (XML tags for section boundaries)
//   - §2.3 (concrete example instead of rule lists)
//   - §2.6 (few-shot examples for structured output)
const eventSummarizerSystemPrompt = `<identity>
You are an execution step summarizer for a multi-agent orchestration system.
You produce concise, factual one-sentence descriptions of what each tool or agent
did during execution, and identify the primary domain entities each step
operated on.
</identity>

<instructions>
1. For each step, write exactly one sentence stating what happened as a factual record
2. Extract and include key output identifiers from the response — resource IDs, ticket keys,
   record numbers, target names, status codes, metric values, or any unique reference that
   would let a reader identify the specific outcome without re-querying the tool
3. State only facts about what occurred
4. Use the tool's agent name and capability to provide context (e.g., "via jira-tool" or "using devops-tool")
5. If the step failed, state the failure reason from the response
6. For each step, output an "entities" array of {"type": "...", "id": "..."} objects identifying
   the primary domain entities the step operated on. Use whatever entity types make sense for
   the domain (pod, order, flight, patient, ticker, account, ...). Only include entities
   directly observable in the step's parameters or response. If none are identifiable, return
   an empty array.
7. Return a valid JSON array. Start with [ and end with ]
</instructions>

<example>
Input steps:
- step-1: ticket-tool/create_issue, response: {"key": "PROJ-42", "id": "10042"}
- step-2: messaging-tool/send_message, params: {"channel": "#alerts"}, response: {"ok": true}
- step-3: infra-tool/restart_service, params: {"service": "payment-api"}, response: {"status": "restarted", "duration_ms": 3200}
- step-4: query-tool/search, response: {"total_results": 0, "error": "index not found"}

Output:
[
  {
    "step_id": "step-1",
    "summary": "Created ticket PROJ-42 via ticket-tool",
    "entities": [{"type": "ticket", "id": "PROJ-42"}]
  },
  {
    "step_id": "step-2",
    "summary": "Sent message to #alerts channel via messaging-tool",
    "entities": [{"type": "channel", "id": "#alerts"}]
  },
  {
    "step_id": "step-3",
    "summary": "Restarted service payment-api (completed in 3200ms) via infra-tool",
    "entities": [{"type": "service", "id": "payment-api"}]
  },
  {
    "step_id": "step-4",
    "summary": "Search query failed: index not found (0 results) via query-tool",
    "entities": []
  }
]

Key patterns shown above:
- Each summary is one factual sentence with key identifiers from the response
- Ticket key (PROJ-42), channel name (#alerts), service name (payment-api) extracted from response/params
- Failed step states the error reason from the response
- Entities are only populated when directly observable in parameters or response
- Step-4 has no entities because the search failed before operating on any identifiable entity
- "non-fatal error" in an instruction is a description, not an entity — entities come from parameters and responses only
</example>`

// LLMEventSummarizer generates actionable, fact-based event summaries using a batched
// LLM call. For each execution step, it produces a one-sentence summary that includes
// key output identifiers (ticket IDs, channel names, URLs) from the tool response.
//
// Design:
//   - Single batched call per request (not per step) — 6 steps = 1 LLM call
//   - Fail-open: returns empty map on LLM error, caller falls back to heuristic
//   - Tool-agnostic: LLM reads raw response and extracts what matters
//   - Response truncation: configurable per-step char limit (default 4000 chars ≈ 1000 tokens)
//   - MaxTokens floor: minimum 300 completion tokens regardless of batch size
type LLMEventSummarizer struct {
	aiClient            core.AIClient
	model               string // empty = use provider default
	temperature         float32
	maxResponseChars    int
	maxStepsPerBatch    int
	tokensPerStepBudget int
	logger              core.Logger
	telemetry           core.Telemetry // optional — for span creation
	debugStore          LLMDebugStore
	debugWg             sync.WaitGroup
	debugSeqID          atomic.Int64
}

// LLMEventSummarizerOption configures LLMEventSummarizer.
type LLMEventSummarizerOption func(*LLMEventSummarizer) error

// WithSummarizerModel sets the model to use for summarization calls.
// Use a fast, cheap model (e.g., "gpt-4o-mini", "claude-haiku-4-5-20251001").
// Supports model aliases ("fast", "smart") which resolve per provider.
// Empty string uses the provider's default model.
func WithSummarizerModel(model string) LLMEventSummarizerOption {
	return func(s *LLMEventSummarizer) error {
		s.model = model
		return nil
	}
}

// WithSummarizerLogger sets the logger for the summarizer.
// If the logger implements ComponentAwareLogger, it is automatically wrapped
// with "framework/orchestration" component context per LOGGING_IMPLEMENTATION_GUIDE.md §14.
func WithSummarizerLogger(logger core.Logger) LLMEventSummarizerOption {
	return func(s *LLMEventSummarizer) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		// Wrap with component context if supported (LOGGING_IMPLEMENTATION_GUIDE §14)
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			s.logger = cal.WithComponent("framework/orchestration")
		} else {
			s.logger = logger
		}
		return nil
	}
}

// WithSummarizerMaxResponseChars sets the maximum characters per step response
// included in the summarization prompt. Default: 4000 (~1000 tokens).
func WithSummarizerMaxResponseChars(n int) LLMEventSummarizerOption {
	return func(s *LLMEventSummarizer) error {
		if n <= 0 {
			return fmt.Errorf("maxResponseChars must be positive, got %d", n)
		}
		s.maxResponseChars = n
		return nil
	}
}

// WithSummarizerMaxStepsPerBatch sets the maximum steps sent in a single LLM call.
// Excess steps fall back to heuristic summaries. Default: 20.
func WithSummarizerMaxStepsPerBatch(n int) LLMEventSummarizerOption {
	return func(s *LLMEventSummarizer) error {
		if n <= 0 {
			return fmt.Errorf("maxStepsPerBatch must be positive, got %d", n)
		}
		s.maxStepsPerBatch = n
		return nil
	}
}

// WithSummarizerTokensPerStep sets the output token budget per step summary.
// Total MaxTokens = tokensPerStep * batchSize. Default: 100.
func WithSummarizerTokensPerStep(n int) LLMEventSummarizerOption {
	return func(s *LLMEventSummarizer) error {
		if n <= 0 {
			return fmt.Errorf("tokensPerStepBudget must be positive, got %d", n)
		}
		s.tokensPerStepBudget = n
		return nil
	}
}

// WithSummarizerTelemetry sets the telemetry provider for span creation.
// When set, the summarizer creates a child span visible in Jaeger.
// When nil, span events are still emitted on the parent span.
func WithSummarizerTelemetry(t core.Telemetry) LLMEventSummarizerOption {
	return func(s *LLMEventSummarizer) error {
		s.telemetry = t // nil is valid — disables span creation
		return nil
	}
}

// WithSummarizerTemperature sets the LLM temperature for summarization calls.
// Lower values produce more deterministic output. Default: 0.1.
func WithSummarizerTemperature(t float32) LLMEventSummarizerOption {
	return func(s *LLMEventSummarizer) error {
		if t < 0 || t > 2 {
			return fmt.Errorf("temperature must be between 0 and 2, got %f", t)
		}
		s.temperature = t
		return nil
	}
}

// NewLLMEventSummarizer creates a new LLM-powered event summarizer.
func NewLLMEventSummarizer(aiClient core.AIClient, opts ...LLMEventSummarizerOption) (*LLMEventSummarizer, error) {
	if aiClient == nil {
		return nil, fmt.Errorf("AI client is required for LLMEventSummarizer")
	}
	s := &LLMEventSummarizer{
		aiClient:            aiClient,
		model:               "",
		temperature:         0.1,
		maxResponseChars:    4000,
		maxStepsPerBatch:    20,
		tokensPerStepBudget: 150,
		logger:              &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, fmt.Errorf("invalid event summarizer option: %w", err)
		}
	}
	return s, nil
}

// Shutdown waits for any in-flight debug recording goroutines to complete.
// Accepts context for graceful shutdown with timeout.
// Same pattern as AISynthesizer.Shutdown.
func (s *LLMEventSummarizer) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.debugWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetLLMDebugStore sets the LLM debug store for full payload visibility.
// When configured, the summarization LLM call (prompt + response) is recorded
// alongside planning, synthesis, and other LLM interactions for the same request.
func (s *LLMEventSummarizer) SetLLMDebugStore(store LLMDebugStore) {
	s.debugStore = store
}

// deferLLMRecordingIfWeWillRecord marks ctx so InstrumentedAIClient skips
// its own agent_llm_call emission when LLMEventSummarizer will emit a typed
// event_summarization record itself. Gated on debugStore presence to preserve
// the graceful-fallback invariant in orchestration/ARCHITECTURE.md.
// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
func (s *LLMEventSummarizer) deferLLMRecordingIfWeWillRecord(ctx context.Context) context.Context {
	if s.debugStore == nil {
		return ctx
	}
	return telemetry.WithLLMCallRecordingDeferred(ctx)
}

// recordDebugInteraction asynchronously records an LLM interaction to the debug store.
// Same pattern as synthesizer.go:recordDebugInteraction — async, fail-open, WaitGroup-tracked.
func (s *LLMEventSummarizer) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
	if s.debugStore == nil {
		return
	}

	s.debugWg.Add(1)
	go func() {
		defer s.debugWg.Done()

		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()

		if err := s.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			if s.logger != nil {
				s.logger.Warn("Failed to record LLM debug interaction", map[string]interface{}{
					"operation":  "event_summarization",
					"request_id": requestID,
					"type":       interaction.Type,
					"error":      err.Error(),
					"error_type": "debug_recording",
				})
			}
		}
	}()
}

// generateFallbackRequestID generates a request ID when baggage is not available.
func (s *LLMEventSummarizer) generateFallbackRequestID() string {
	seq := s.debugSeqID.Add(1)
	return fmt.Sprintf("summarizer-%d-%d", time.Now().UnixNano(), seq)
}

// SummarizeSteps generates actionable summaries for a batch of execution steps.
// Makes a single LLM call for all steps. Returns a map of stepID -> summary.
// Fail-open: returns empty map on any error.
//
// Emitted LLMInteraction records hardcode HookPhase: HookPhasePost, which
// assumes this method is invoked from an AfterSynthesis-style hook (today:
// MemoryRecordHook). If a future caller invokes this from a different
// phase, move the HookPhase assignment to the caller or derive it from
// context baggage — otherwise the registry viewer will misroute the trace.
func (s *LLMEventSummarizer) SummarizeSteps(ctx context.Context, steps []core.StepSummaryInput) (map[string]core.StepSummary, error) {
	if len(steps) == 0 {
		return map[string]core.StepSummary{}, nil
	}

	// Create child span for Jaeger visibility (visible as separate bar in trace waterfall)
	var span core.Span
	if s.telemetry != nil {
		ctx, span = s.telemetry.StartSpan(ctx, "orchestrator.event_summarization")
		defer span.End()
	}

	startTime := time.Now()
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = s.generateFallbackRequestID()
	}

	if span != nil {
		span.SetAttribute("request_id", requestID)
		span.SetAttribute("step_count", len(steps))
		if s.model != "" {
			span.SetAttribute("model", s.model)
		}
	}

	// Cap batch size to avoid exceeding model context
	batch := steps
	if len(batch) > s.maxStepsPerBatch {
		if s.logger != nil {
			s.logger.InfoWithContext(ctx, "Event summarization batch truncated, excess steps will use heuristic fallback", map[string]interface{}{
				"operation":       "event_summarization",
				"request_id":      requestID,
				"total_steps":     len(steps),
				"batch_limit":     s.maxStepsPerBatch,
				"truncated_count": len(steps) - s.maxStepsPerBatch,
			})
		}
		batch = batch[:s.maxStepsPerBatch]
	}

	// Build prompt
	prompt := s.buildPrompt(batch)

	// Span events for Jaeger visibility (runs within orchestrator's existing span)
	requestAttrs := []attribute.KeyValue{
		attribute.String("request_id", requestID),
		attribute.Int("step_count", len(batch)),
		attribute.Int("prompt_length", len(prompt)),
	}
	if s.model != "" {
		requestAttrs = append(requestAttrs, attribute.String("model", s.model))
	}
	telemetry.AddSpanEvent(ctx, "llm.event_summarization.request", requestAttrs...)

	// Floor at 300 tokens — JSON array overhead (~50 tokens) + room for summaries
	// with identifiers. Without a floor, small batches (1-2 steps) get too few tokens,
	// causing truncated JSON responses (observed in production with 100-token budget).
	// Configurable via WithSummarizerTokensPerStep() for higher budgets.
	maxTokens := s.tokensPerStepBudget * len(batch)
	if maxTokens < 300 {
		maxTokens = 300
	}

	aiOpts := &core.AIOptions{
		Temperature:  s.temperature,
		MaxTokens:    maxTokens,
		SystemPrompt: eventSummarizerSystemPrompt,
	}
	if s.model != "" {
		aiOpts.Model = s.model
	}

	callCtx := s.deferLLMRecordingIfWeWillRecord(ctx)
	aiResp, err := s.aiClient.GenerateResponse(callCtx, prompt, aiOpts)
	if err != nil {
		durationMs := float64(time.Since(startTime).Milliseconds())
		telemetry.RecordSpanError(ctx, err)

		// Record failed interaction in debug store
		s.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:         "event_summarization",
			HookPhase:    HookPhasePost,
			Timestamp:    startTime,
			DurationMs:   int64(durationMs),
			Prompt:       prompt,
			SystemPrompt: eventSummarizerSystemPrompt,
			Temperature:  float64(aiOpts.Temperature),
			MaxTokens:    aiOpts.MaxTokens,
			Model:        aiOpts.Model,
			Success:      false,
			Error:        err.Error(),
			Attempt:      1,
		})

		if s.logger != nil {
			s.logger.WarnWithContext(ctx, "Event summarization LLM call failed, using heuristic fallback", map[string]interface{}{
				"operation":   "event_summarization",
				"request_id":  requestID,
				"error":       err.Error(),
				"error_type":  "llm_unavailable",
				"step_count":  len(batch),
				"duration_ms": durationMs,
			})
		}
		telemetry.Counter("orchestration.event_summarization.errors",
			"module", telemetry.ModuleOrchestration,
			"error_type", "llm_unavailable",
		)
		return map[string]core.StepSummary{}, nil // Fail-open
	}

	durationMs := float64(time.Since(startTime).Milliseconds())

	// Record token usage for shared tracking
	core.RecordTokenUsage(ctx, "event_summarization", aiResp.Usage)

	// Record successful interaction in debug store
	s.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "event_summarization",
		HookPhase:        HookPhasePost,
		Timestamp:        startTime,
		DurationMs:       int64(durationMs),
		Prompt:           prompt,
		SystemPrompt:     eventSummarizerSystemPrompt,
		Temperature:      float64(aiOpts.Temperature),
		MaxTokens:        aiOpts.MaxTokens,
		Model:            aiResp.Model,
		Provider:         aiResp.Provider,
		Response:         aiResp.Content,
		PromptTokens:     aiResp.Usage.PromptTokens,
		CompletionTokens: aiResp.Usage.CompletionTokens,
		TotalTokens:      aiResp.Usage.TotalTokens,
		Success:          true,
		Attempt:          1,
	})

	responseAttrs := []attribute.KeyValue{
		attribute.String("request_id", requestID),
		attribute.Int("response_length", len(aiResp.Content)),
	}
	if s.model != "" {
		responseAttrs = append(responseAttrs, attribute.String("model", s.model))
	}
	telemetry.AddSpanEvent(ctx, "llm.event_summarization.response", responseAttrs...)

	// Parse response
	summaries := s.parseResponse(ctx, aiResp.Content, requestID)
	if s.logger != nil {
		logFields := map[string]interface{}{
			"operation":       "event_summarization",
			"request_id":      requestID,
			"step_count":      len(batch),
			"summaries_count": len(summaries),
			"duration_ms":     durationMs,
		}
		if s.model != "" {
			logFields["model"] = s.model
		}
		s.logger.InfoWithContext(ctx, "Event summarization completed", logFields)
	}
	telemetry.Counter("orchestration.event_summarization.success",
		"module", telemetry.ModuleOrchestration,
	)

	return summaries, nil
}

// buildPrompt constructs the batched summarization prompt.
func (s *LLMEventSummarizer) buildPrompt(steps []core.StepSummaryInput) string {
	var sb strings.Builder

	sb.WriteString(`For each tool execution step below, write exactly one factual sentence describing what happened. Include key identifiers from the response (ticket IDs, channel names, deployment names, URLs, error codes, metric values). State only facts — no recommendations, instructions, or opinions.

Return a JSON array: [{"step_id": "step-1", "summary": "..."}, ...]

<steps>
`)

	for _, step := range steps {
		fmt.Fprintf(&sb, "<step id=%q>\n", step.StepID)
		fmt.Fprintf(&sb, "  <agent>%s</agent>\n", step.AgentName)
		fmt.Fprintf(&sb, "  <capability>%s</capability>\n", step.Capability)
		fmt.Fprintf(&sb, "  <instruction>%s</instruction>\n", truncateString(step.Instruction, 500))

		if len(step.Parameters) > 0 {
			if paramJSON, err := json.Marshal(step.Parameters); err == nil {
				fmt.Fprintf(&sb, "  <parameters>%s</parameters>\n", truncateString(string(paramJSON), 1000))
			}
		}

		if step.Response != "" {
			fmt.Fprintf(&sb, "  <response>%s</response>\n", truncateString(step.Response, s.maxResponseChars))
		}

		outcome := "success"
		if !step.Success {
			outcome = "failure"
		}
		fmt.Fprintf(&sb, "  <outcome>%s</outcome>\n", outcome)
		sb.WriteString("</step>\n\n")
	}

	sb.WriteString("</steps>")
	return sb.String()
}

// stripMarkdownCodeFence removes ```json ... ``` wrapping that LLMs often add.
func stripMarkdownCodeFence(s string) string {
	trimmed := strings.TrimSpace(s)
	// Check for ```json or ``` prefix
	if strings.HasPrefix(trimmed, "```") {
		// Remove opening fence (```json, ```JSON, or just ```)
		if idx := strings.Index(trimmed, "\n"); idx >= 0 {
			trimmed = trimmed[idx+1:]
		}
		// Remove closing fence
		trimmed = strings.TrimSuffix(trimmed, "```")
		return strings.TrimSpace(trimmed)
	}
	return s
}

// parseResponse extracts step summaries and LLM-identified entities from the
// LLM response. Returns a map of stepID -> core.StepSummary.
//
// Fail-open: on JSON parse failure, returns an empty map and the caller falls
// back to the heuristic summary path. Empty entities arrays are preserved.
func (s *LLMEventSummarizer) parseResponse(ctx context.Context, content, requestID string) map[string]core.StepSummary {
	type stepSummary struct {
		StepID   string           `json:"step_id"`
		Summary  string           `json:"summary"`
		Entities []core.EntityRef `json:"entities,omitempty"`
	}

	var summaries []stepSummary

	// Strip markdown code fences (```json ... ```) that LLMs frequently add
	content = stripMarkdownCodeFence(content)

	// Try direct parse first
	if err := json.Unmarshal([]byte(content), &summaries); err != nil {
		// Try to find JSON array in prose-wrapped response
		if extracted := extractJSONArray(content); extracted != "" {
			if parseErr := json.Unmarshal([]byte(extracted), &summaries); parseErr != nil {
				telemetry.RecordSpanError(ctx, parseErr)
				if s.logger != nil {
					s.logger.WarnWithContext(ctx, "Failed to parse event summarization response", map[string]interface{}{
						"operation":  "event_summarization",
						"request_id": requestID,
						"error":      parseErr.Error(),
						"error_type": "parse_failure",
					})
				}
				return map[string]core.StepSummary{}
			}
		} else {
			telemetry.RecordSpanError(ctx, fmt.Errorf("LLM response contained no JSON array (length: %d)", len(content)))
			if s.logger != nil {
				s.logger.DebugWithContext(ctx, "LLM response contained no JSON array, using heuristic fallback", map[string]interface{}{
					"operation":       "event_summarization",
					"request_id":      requestID,
					"response_length": len(content),
				})
			}
			return map[string]core.StepSummary{}
		}
	}

	result := make(map[string]core.StepSummary, len(summaries))
	for _, entry := range summaries {
		if entry.StepID == "" || entry.Summary == "" {
			continue
		}
		// Filter out empty/invalid entity entries the LLM might emit.
		var cleaned []core.EntityRef
		for _, e := range entry.Entities {
			if e.Type != "" && e.ID != "" {
				cleaned = append(cleaned, e)
			}
		}
		result[entry.StepID] = core.StepSummary{
			Summary:  entry.Summary,
			Entities: cleaned,
		}
	}
	return result
}
