package orchestration

import (
	"context"
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
var _ core.ActivityCompactor = (*LLMActivityCompactor)(nil)

// activityCompactorSystemPrompt follows docs/EFFECTIVE_PROMPTS_GUIDE.md:
//   - §2.8 (converged ordering: identity → instructions → example)
//   - §2.9 (system message for identity and rules)
//   - §2.10 (XML tags for section boundaries)
//   - §2.3 (concrete example instead of rule lists)
//   - §2.4 (positive instructions only — no negatives)
const activityCompactorSystemPrompt = `<identity>
You are a domain activity summarizer for a multi-agent orchestration system.
You produce concise factual digests of recent agent activity.
</identity>

<instructions>
1. Summarize the events into a brief activity digest
2. Group related events by theme (e.g., restarts, ticketing, monitoring) into single bullet points
3. Preserve key identifiers: ticket IDs, channel names, pod names, deployment names
4. Note any active/ongoing investigations
5. If a tool is available for detailed queries, mention it
6. State only facts about what occurred
</instructions>

<example>
Input (12 events):
- devops-chat-agent: Created JIRA ticket DEVOPS-49 for rollout restart
- devops-chat-agent: Sent message to #notifications channel
- devops-chat-agent: Performed rollout restart of agent-with-human-approval (200ms)
- devops-chat-agent: get_pods x3, describe_resource x2
- event-driven-agent: query_metrics x4

Output:
Domain activity (last 2 hours, 12 events):
- Deployment restart: agent-with-human-approval restarted via rollout (200ms), verified healthy
- JIRA: ticket DEVOPS-49 created for the restart, comment added with verification details
- Slack: notification sent to #notifications with JIRA reference
- Monitoring: 4 Prometheus queries run by event-driven-agent for product-catalog-api metrics

Key patterns shown above:
- Events grouped by theme (restart, ticketing, notification, monitoring)
- Key identifiers preserved: DEVOPS-49, #notifications, agent-with-human-approval, 200ms
</example>`

// LLMActivityCompactor compresses raw episodic events into a fixed-size digest via LLM.
// Follows the same patterns as LLMEventSummarizer:
//   - Returns ("", err) on failure — caller handles fallback to raw events
//   - ComponentAwareLogger with WithComponent("framework/orchestration")
//   - Telemetry child span (orchestrator.activity_compaction)
//   - LLM debug store recording (type: "activity_compaction")
//   - Configurable model via WithActivityCompactorModel() / TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL
type LLMActivityCompactor struct {
	aiClient    core.AIClient
	model       string
	temperature float32
	logger      core.Logger
	telemetry   core.Telemetry
	debugStore  LLMDebugStore
	debugWg     sync.WaitGroup
	debugSeqID  atomic.Int64
}

// LLMActivityCompactorOption configures LLMActivityCompactor.
type LLMActivityCompactorOption func(*LLMActivityCompactor) error

// WithActivityCompactorModel sets the model to use for compaction calls.
// Supports model aliases ("fast", "smart") which resolve per provider.
func WithActivityCompactorModel(model string) LLMActivityCompactorOption {
	return func(c *LLMActivityCompactor) error {
		c.model = model
		return nil
	}
}

// WithActivityCompactorLogger sets the logger. Wraps with ComponentAwareLogger per §14.
func WithActivityCompactorLogger(logger core.Logger) LLMActivityCompactorOption {
	return func(c *LLMActivityCompactor) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			c.logger = cal.WithComponent("framework/orchestration")
		} else {
			c.logger = logger
		}
		return nil
	}
}

// WithActivityCompactorTelemetry sets the telemetry provider for span creation.
// When set, the compactor creates a child span visible in Jaeger.
func WithActivityCompactorTelemetry(t core.Telemetry) LLMActivityCompactorOption {
	return func(c *LLMActivityCompactor) error {
		c.telemetry = t // nil is valid — disables span creation
		return nil
	}
}

// WithActivityCompactorTemperature sets the LLM temperature. Default: 0.1.
func WithActivityCompactorTemperature(t float32) LLMActivityCompactorOption {
	return func(c *LLMActivityCompactor) error {
		if t < 0 || t > 2 {
			return fmt.Errorf("temperature must be between 0 and 2, got %f", t)
		}
		c.temperature = t
		return nil
	}
}

// NewLLMActivityCompactor creates a new LLM-powered activity compactor.
func NewLLMActivityCompactor(aiClient core.AIClient, opts ...LLMActivityCompactorOption) (*LLMActivityCompactor, error) {
	if aiClient == nil {
		return nil, fmt.Errorf("AI client is required for LLMActivityCompactor")
	}
	c := &LLMActivityCompactor{
		aiClient:    aiClient,
		temperature: 0.1,
		logger:      &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, fmt.Errorf("invalid activity compactor option: %w", err)
		}
	}
	return c, nil
}

// Shutdown waits for in-flight debug recording goroutines to complete.
func (c *LLMActivityCompactor) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		c.debugWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetLLMDebugStore sets the debug store for full payload visibility.
func (c *LLMActivityCompactor) SetLLMDebugStore(store LLMDebugStore) {
	c.debugStore = store
}

// deferLLMRecordingIfWeWillRecord marks ctx so InstrumentedAIClient skips
// its own agent_llm_call emission when LLMActivityCompactor will emit a typed
// activity_compaction record itself. Gated on debugStore presence to preserve
// the graceful-fallback invariant in orchestration/ARCHITECTURE.md.
// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
func (c *LLMActivityCompactor) deferLLMRecordingIfWeWillRecord(ctx context.Context) context.Context {
	if c.debugStore == nil {
		return ctx
	}
	return telemetry.WithLLMCallRecordingDeferred(ctx)
}

// SetTelemetry sets the telemetry provider for span creation.
func (c *LLMActivityCompactor) SetTelemetry(t core.Telemetry) {
	c.telemetry = t
}

// CompactEvents compresses raw events into a fixed-size digest via a single LLM call.
//
// Emitted LLMInteraction records hardcode HookPhase: HookPhasePre, which
// assumes this method is invoked from a BeforePlanning-style hook (today:
// MemoryEnrichmentHook). If a future caller invokes this from a different
// phase, move the HookPhase assignment to the caller or derive it from
// context baggage — otherwise the registry viewer will misroute the trace.
func (c *LLMActivityCompactor) CompactEvents(ctx context.Context, events []core.AgentEvent, maxTokens int) (string, error) {
	if len(events) == 0 {
		return "", nil
	}

	// Child span for Jaeger visibility
	var span core.Span
	if c.telemetry != nil {
		ctx, span = c.telemetry.StartSpan(ctx, "orchestrator.activity_compaction")
		defer span.End()
	}

	startTime := time.Now()
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = c.generateFallbackRequestID()
	}

	if span != nil {
		span.SetAttribute("request_id", requestID)
		span.SetAttribute("event_count", len(events))
		span.SetAttribute("max_tokens", maxTokens)
		if c.model != "" {
			span.SetAttribute("model", c.model)
		}
	}

	// Format raw events for the prompt
	rawText := formatEpisodicEvents(events, "")
	prompt := fmt.Sprintf("<events>\n%s\n</events>\n\nProduce a summary of at most %d tokens. Return plain text only.", rawText, maxTokens)

	// Span event: request
	telemetry.AddSpanEvent(ctx, "llm.activity_compaction.request",
		attribute.String("request_id", requestID),
		attribute.Int("event_count", len(events)),
		attribute.Int("prompt_length", len(prompt)),
	)

	// MaxTokens is the LLM's generation ceiling (safety limit), not the target.
	// The prompt instructs the model to produce "at most N tokens" — the 5x headroom
	// gives the model generous room to produce clean output without mid-sentence truncation.
	aiOpts := &core.AIOptions{
		Temperature:  c.temperature,
		MaxTokens:    maxTokens * 5,
		SystemPrompt: activityCompactorSystemPrompt,
	}
	if c.model != "" {
		aiOpts.Model = c.model
	}

	callCtx := c.deferLLMRecordingIfWeWillRecord(ctx)
	aiResp, err := c.aiClient.GenerateResponse(callCtx, prompt, aiOpts)
	if err != nil {
		durationMs := float64(time.Since(startTime).Milliseconds())
		telemetry.RecordSpanError(ctx, err)

		c.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:         "activity_compaction",
			HookPhase:    HookPhasePre,
			Timestamp:    startTime,
			DurationMs:   int64(durationMs),
			Prompt:       prompt,
			SystemPrompt: activityCompactorSystemPrompt,
			Temperature:  float64(aiOpts.Temperature),
			MaxTokens:    aiOpts.MaxTokens,
			Model:        aiOpts.Model,
			Success:      false,
			Error:        err.Error(),
			Attempt:      1,
		})

		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Activity compaction LLM call failed, using raw events fallback", map[string]interface{}{
				"operation":   "activity_compaction",
				"request_id":  requestID,
				"error":       err.Error(),
				"error_type":  "llm_unavailable",
				"event_count": len(events),
				"duration_ms": durationMs,
			})
		}
		telemetry.Counter("orchestration.activity_compaction.errors",
			"module", telemetry.ModuleOrchestration,
			"error_type", "llm_unavailable",
		)
		return "", err
	}

	durationMs := float64(time.Since(startTime).Milliseconds())

	core.RecordTokenUsage(ctx, "activity_compaction", aiResp.Usage)

	c.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "activity_compaction",
		HookPhase:        HookPhasePre,
		Timestamp:        startTime,
		DurationMs:       int64(durationMs),
		Prompt:           prompt,
		SystemPrompt:     activityCompactorSystemPrompt,
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

	telemetry.AddSpanEvent(ctx, "llm.activity_compaction.response",
		attribute.String("request_id", requestID),
		attribute.Int("response_length", len(aiResp.Content)),
	)

	if c.logger != nil {
		logFields := map[string]interface{}{
			"operation":     "activity_compaction",
			"request_id":    requestID,
			"event_count":   len(events),
			"digest_length": len(aiResp.Content),
			"duration_ms":   durationMs,
		}
		if c.model != "" {
			logFields["model"] = c.model
		}
		c.logger.InfoWithContext(ctx, "Activity compaction completed", logFields)
	}
	telemetry.Counter("orchestration.activity_compaction.success",
		"module", telemetry.ModuleOrchestration,
	)

	return strings.TrimSpace(aiResp.Content), nil
}

// UpdateDigest incrementally updates an existing digest with new events.
// Uses a smaller prompt (<previous_digest> + <new_events>) for faster LLM response.
//
// Emitted LLMInteraction records hardcode HookPhase: HookPhasePre — see
// the note on CompactEvents for the invariant and how to revisit it if a
// non-pre-phase caller is added.
func (c *LLMActivityCompactor) UpdateDigest(ctx context.Context, previousDigest string, newEvents []core.AgentEvent, maxTokens int) (string, error) {
	if len(newEvents) == 0 {
		return previousDigest, nil
	}

	var span core.Span
	if c.telemetry != nil {
		ctx, span = c.telemetry.StartSpan(ctx, "orchestrator.activity_compaction_incremental")
		defer span.End()
	}

	startTime := time.Now()
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = c.generateFallbackRequestID()
	}

	if span != nil {
		span.SetAttribute("request_id", requestID)
		span.SetAttribute("new_event_count", len(newEvents))
		span.SetAttribute("previous_digest_length", len(previousDigest))
	}

	rawText := formatEpisodicEvents(newEvents, "")
	prompt := fmt.Sprintf("<previous_digest>\n%s\n</previous_digest>\n\n<new_events>\n%s\n</new_events>\n\nUpdate the digest to incorporate the new events. Keep within %d tokens. Return plain text only.",
		previousDigest, rawText, maxTokens)

	telemetry.AddSpanEvent(ctx, "llm.activity_compaction.incremental_request",
		attribute.String("request_id", requestID),
		attribute.Int("new_event_count", len(newEvents)),
		attribute.Int("prompt_length", len(prompt)),
	)

	aiOpts := &core.AIOptions{
		Temperature:  c.temperature,
		MaxTokens:    maxTokens * 5,
		SystemPrompt: activityCompactorSystemPrompt,
	}
	if c.model != "" {
		aiOpts.Model = c.model
	}

	callCtx := c.deferLLMRecordingIfWeWillRecord(ctx)
	aiResp, err := c.aiClient.GenerateResponse(callCtx, prompt, aiOpts)
	if err != nil {
		durationMs := float64(time.Since(startTime).Milliseconds())
		telemetry.RecordSpanError(ctx, err)
		c.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type: "activity_compaction_incremental", HookPhase: HookPhasePre,
			Timestamp: startTime, DurationMs: int64(durationMs),
			Prompt: prompt, SystemPrompt: activityCompactorSystemPrompt,
			Temperature: float64(aiOpts.Temperature), MaxTokens: aiOpts.MaxTokens,
			Model: aiOpts.Model, Success: false, Error: err.Error(), Attempt: 1,
		})
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Incremental digest update failed, returning previous digest", map[string]interface{}{
				"operation":       "activity_compaction_incremental",
				"request_id":      requestID,
				"error":           err.Error(),
				"error_type":      "llm_unavailable",
				"new_event_count": len(newEvents),
				"duration_ms":     durationMs,
			})
		}
		telemetry.Counter("orchestration.activity_compaction.incremental_errors",
			"module", telemetry.ModuleOrchestration, "error_type", "llm_unavailable")
		return previousDigest, err
	}

	durationMs := float64(time.Since(startTime).Milliseconds())
	core.RecordTokenUsage(ctx, "activity_compaction_incremental", aiResp.Usage)

	c.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type: "activity_compaction_incremental", HookPhase: HookPhasePre,
		Timestamp: startTime, DurationMs: int64(durationMs),
		Prompt: prompt, SystemPrompt: activityCompactorSystemPrompt,
		Temperature: float64(aiOpts.Temperature), MaxTokens: aiOpts.MaxTokens,
		Model: aiResp.Model, Provider: aiResp.Provider, Response: aiResp.Content,
		PromptTokens: aiResp.Usage.PromptTokens, CompletionTokens: aiResp.Usage.CompletionTokens,
		TotalTokens: aiResp.Usage.TotalTokens, Success: true, Attempt: 1,
	})

	telemetry.AddSpanEvent(ctx, "llm.activity_compaction.incremental_response",
		attribute.String("request_id", requestID),
		attribute.Int("response_length", len(aiResp.Content)),
	)

	if c.logger != nil {
		logFields := map[string]interface{}{
			"operation":       "activity_compaction_incremental",
			"request_id":      requestID,
			"new_event_count": len(newEvents),
			"digest_length":   len(aiResp.Content),
			"duration_ms":     durationMs,
		}
		if c.model != "" {
			logFields["model"] = c.model
		}
		c.logger.InfoWithContext(ctx, "Incremental digest update completed", logFields)
	}
	telemetry.Counter("orchestration.activity_compaction.incremental_success",
		"module", telemetry.ModuleOrchestration)

	return strings.TrimSpace(aiResp.Content), nil
}

// recordDebugInteraction — same async pattern as LLMEventSummarizer.
func (c *LLMActivityCompactor) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
	if c.debugStore == nil {
		return
	}

	c.debugWg.Add(1)
	go func() {
		defer c.debugWg.Done()

		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()

		if err := c.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			if c.logger != nil {
				c.logger.Warn("Failed to record LLM debug interaction", map[string]interface{}{
					"operation":  "activity_compaction",
					"request_id": requestID,
					"type":       interaction.Type,
					"error":      err.Error(),
					"error_type": "debug_recording",
				})
			}
		}
	}()
}

func (c *LLMActivityCompactor) generateFallbackRequestID() string {
	seq := c.debugSeqID.Add(1)
	return fmt.Sprintf("compactor-%d-%d", time.Now().UnixNano(), seq)
}
