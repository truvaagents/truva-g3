package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type LLMConversationCompactor struct {
	aiClient   core.AIClient
	aiOptions  *AIOptionsOverride
	logger     core.Logger
	telemetry  core.Telemetry
	debugStore LLMDebugStore
}

func NewLLMConversationCompactor(aiClient core.AIClient, aiOptions *AIOptionsOverride) (*LLMConversationCompactor, error) {
	if aiClient == nil {
		return nil, fmt.Errorf("conversation compactor ai client cannot be nil")
	}
	return &LLMConversationCompactor{
		aiClient:  aiClient,
		aiOptions: aiOptions,
		logger:    &core.NoOpLogger{},
		telemetry: &core.NoOpTelemetry{},
	}, nil
}

func (c *LLMConversationCompactor) SetLogger(logger core.Logger) {
	if logger == nil {
		c.logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		c.logger = cal.WithComponent("framework/orchestration")
	} else {
		c.logger = logger
	}
}

func (c *LLMConversationCompactor) SetTelemetry(t core.Telemetry) {
	if t == nil {
		c.telemetry = &core.NoOpTelemetry{}
	} else {
		c.telemetry = t
	}
}

func (c *LLMConversationCompactor) SetLLMDebugStore(store LLMDebugStore) {
	c.debugStore = store
}

func (c *LLMConversationCompactor) aiSemanticFingerprint(ctx context.Context) (string, bool) {
	return fingerprintAI(ctx, c.aiClient, aiInvocation{
		Purpose: "conversation-compaction",
		Options: mergeAIOptions(&core.AIOptions{}, c.aiOptions),
	})
}

func (c *LLMConversationCompactor) Compact(ctx context.Context, priorSummary string, newTurns []core.ConversationTurn) (string, error) {
	if len(newTurns) == 0 {
		return priorSummary, nil
	}

	startTime := time.Now()
	ctx, span := startCompactionSpan(ctx, c.telemetry, "conversation_history.compact")
	defer span.End()
	span.SetAttribute("prior_summary_chars", len(priorSummary))
	span.SetAttribute("new_turns", len(newTurns))

	prompt := buildConversationCompactionPrompt(priorSummary, newTurns)
	opts := mergeAIOptions(&core.AIOptions{}, c.aiOptions)
	ctx = telemetry.WithBaggage(ctx, "ai.purpose", "conversation_history_compaction")
	resp, _, err := invokeAI(ctx, c.aiClient, aiInvocation{
		Purpose:        "conversation-compaction",
		Prompt:         prompt,
		Options:        opts,
		DeferRecording: c.debugStore != nil,
	})
	if err != nil {
		c.recordDebugInteraction(ctx, startTime, prompt, opts, nil, err)
		telemetry.RecordSpanError(ctx, err)
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Conversation compaction failed; falling back to Tier 1", map[string]interface{}{
				"operation":           "conversation_history",
				"request_id":          requestIDFromBaggage(ctx),
				"original_request_id": originalRequestIDFromBaggage(ctx),
				"error":               err.Error(),
				"error_type":          "compaction",
				"duration_ms":         time.Since(startTime).Milliseconds(),
				"status":              "tier1_fallback",
			})
		}
		telemetry.AddSpanEvent(ctx, "conversation_history.compact.fail_open",
			attribute.String("request_id", requestIDFromBaggage(ctx)),
			attribute.String("error", err.Error()),
		)
		return "", nil
	}

	content := strings.TrimSpace(resp.Content)
	c.recordDebugInteraction(ctx, startTime, prompt, opts, resp, nil)
	span.SetAttribute("result_chars", len(content))
	return content, nil
}

func (c *LLMConversationCompactor) recordDebugInteraction(
	ctx context.Context,
	startTime time.Time,
	prompt string,
	opts *core.AIOptions,
	resp *core.AIResponse,
	callErr error,
) {
	if c.debugStore == nil {
		return
	}

	requestID := requestIDFromBaggage(ctx)
	if requestID == "" {
		requestID = fmt.Sprintf("conversation-compaction-%d", time.Now().UnixNano())
	}

	recordCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		pairs := make([]string, 0, len(bag)*2)
		for k, v := range bag {
			pairs = append(pairs, k, v)
		}
		recordCtx = telemetry.WithBaggage(recordCtx, pairs...)
	}

	interaction := LLMInteraction{
		Type:            "conversation_history_compaction",
		HookPhase:       HookPhasePre,
		Timestamp:       startTime,
		DurationMs:      time.Since(startTime).Milliseconds(),
		Prompt:          prompt,
		SystemPrompt:    opts.SystemPrompt,
		Temperature:     float64(opts.Temperature),
		MaxTokens:       opts.MaxTokens,
		Model:           opts.Model,
		CallDescription: "Recursive conversation history compaction",
		Success:         callErr == nil,
		Attempt:         1,
	}
	if resp != nil {
		interaction.Response = resp.Content
		interaction.Model = resp.Model
		interaction.Provider = resp.Provider
		interaction.PromptTokens = resp.Usage.PromptTokens
		interaction.CompletionTokens = resp.Usage.CompletionTokens
		interaction.TotalTokens = resp.Usage.TotalTokens
	}
	if callErr != nil {
		interaction.Error = callErr.Error()
	}

	if err := c.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil && c.logger != nil {
		c.logger.Warn("Failed to record conversation compaction debug interaction", map[string]interface{}{
			"operation":  "conversation_history",
			"request_id": requestID,
			"type":       interaction.Type,
			"error":      err.Error(),
			"error_type": "debug_recording",
		})
	}
}

func buildConversationCompactionPrompt(priorSummary string, newTurns []core.ConversationTurn) string {
	var sb strings.Builder
	sb.WriteString("You are compacting conversation history for a planning system.\n")
	sb.WriteString("Produce a concise factual summary of the prior conversation in 20 sentences or fewer.\n")
	sb.WriteString("Preserve user goals, constraints, decisions, unresolved questions, and any durable facts needed for future turns.\n")
	sb.WriteString("Do not invent details. Prefer concrete facts over interpretation.\n")
	sb.WriteString("Avoid procedural or stale workflow narration such as 'the next step is' or 'the assistant will'.\n")
	sb.WriteString("Summarize only durable conversational state that should still matter in later turns.\n\n")
	if priorSummary != "" {
		sb.WriteString("Existing summary:\n")
		sb.WriteString(priorSummary)
		sb.WriteString("\n\n")
	}
	sb.WriteString("New turns to fold in:\n")
	sb.WriteString(formatConversationTurns(newTurns))
	sb.WriteString("\n\nUpdated summary:")
	return sb.String()
}
