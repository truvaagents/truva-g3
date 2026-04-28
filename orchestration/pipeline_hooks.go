package orchestration

import (
	"context"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// buildShortCircuitResponse builds an OrchestratorResponse for a pipeline
// short-circuit (e.g. semantic cache hit). Merges any hook-injected enrichments
// into the response metadata.
func (o *AIOrchestrator) buildShortCircuitResponse(
	requestID string,
	request string,
	metadata map[string]interface{},
	pctx *core.PipelineContext,
	shortCircuit *core.PipelineShortCircuit,
	startTime time.Time,
	usage *core.TokenUsage,
	usageByPhase map[string]core.TokenUsage,
) *OrchestratorResponse {
	respMetadata := mergeEnrichments(metadata, pctx.Enrichments)
	return &OrchestratorResponse{
		RequestID:       requestID,
		OriginalRequest: request,
		Response:        shortCircuit.Response,
		RoutingMode:     o.config.RoutingMode,
		ExecutionTime:   time.Since(startTime),
		Metadata:        respMetadata,
		Confidence:      1.0,
		Usage:           usage,
		UsageByPhase:    usageByPhase,
	}
}

// prepareKnownEnrichments copies known enrichments from metadata into enrichments
// and prepares conversation history through the shared processor when configured.
// Runs before hooks, so hooks can still override or augment the prepared values.
func prepareKnownEnrichments(
	ctx context.Context,
	metadata map[string]interface{},
	enrichments map[string]interface{},
	historyPreparer ConversationHistoryPreparer,
) {
	if len(metadata) == 0 {
		return
	}

	if historyPreparer != nil {
		if turns, ok := conversationTurnsFromMetadata(metadata[MetadataConversationTurns]); ok {
			sessionKey, _ := metadata[MetadataConversationSessionKey].(string)
			prepared, _, _ := historyPreparer.PrepareFromTurns(ctx, sessionKey, turns)
			if prepared != "" {
				enrichments[core.EnrichmentConversationHistory] = prepared
			}
		} else if raw, ok := metadata[core.EnrichmentConversationHistory].(string); ok && raw != "" {
			prepared, _, _ := historyPreparer.PrepareFromText(ctx, "", raw)
			if prepared != "" {
				enrichments[core.EnrichmentConversationHistory] = prepared
			}
		}
	} else if raw, ok := metadata[core.EnrichmentConversationHistory].(string); ok && raw != "" {
		enrichments[core.EnrichmentConversationHistory] = raw
	}

	if val, ok := metadata[core.EnrichmentRAGContext]; ok {
		enrichments[core.EnrichmentRAGContext] = val
	}
}

func conversationTurnsFromMetadata(raw interface{}) ([]core.ConversationTurn, bool) {
	switch turns := raw.(type) {
	case []core.ConversationTurn:
		return cloneConversationTurns(turns), true
	case []interface{}:
		decoded := make([]core.ConversationTurn, 0, len(turns))
		for _, rawTurn := range turns {
			turn, ok := decodeConversationTurn(rawTurn)
			if !ok {
				return nil, false
			}
			decoded = append(decoded, turn)
		}
		return decoded, true
	default:
		return nil, false
	}
}

func decodeConversationTurn(raw interface{}) (core.ConversationTurn, bool) {
	switch turn := raw.(type) {
	case core.ConversationTurn:
		return turn, true
	case map[string]interface{}:
		decoded := core.ConversationTurn{}
		if role, ok := turn["role"].(string); ok {
			decoded.Role = role
		}
		if content, ok := turn["content"].(string); ok {
			decoded.Content = content
		}
		if timestamp, ok := turn["timestamp"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
				decoded.Timestamp = parsed
			}
		}
		if metadata, ok := turn["metadata"].(map[string]interface{}); ok {
			decoded.Metadata = metadata
		}
		if decoded.Role == "" && decoded.Content == "" && decoded.Timestamp.IsZero() && len(decoded.Metadata) == 0 {
			return core.ConversationTurn{}, false
		}
		return decoded, true
	default:
		return core.ConversationTurn{}, false
	}
}

// mergeEnrichments returns a new map combining metadata and enrichments.
// Returns metadata unchanged if enrichments is empty. Never mutates the input maps.
func mergeEnrichments(metadata map[string]interface{}, enrichments map[string]interface{}) map[string]interface{} {
	if len(enrichments) == 0 {
		return metadata
	}
	merged := make(map[string]interface{}, len(metadata)+len(enrichments))
	for k, v := range metadata {
		merged[k] = v
	}
	for k, v := range enrichments {
		merged[k] = v
	}
	return merged
}

// runBeforePlanningHooks executes all registered BeforePlanningHook implementations.
// Returns a PipelineShortCircuit if any hook requests it, nil otherwise.
// Errors are logged and skipped — they never abort the pipeline.
func (o *AIOrchestrator) runBeforePlanningHooks(ctx context.Context, pctx *core.PipelineContext) *core.PipelineShortCircuit {
	for _, hook := range o.pipelineHooks {
		h, ok := hook.(core.BeforePlanningHook)
		if !ok {
			continue
		}

		var hookSpan core.Span
		if o.telemetry != nil {
			ctx, hookSpan = o.telemetry.StartSpan(ctx, "pipeline.hook.before_planning."+hook.Name())
		}

		shortCircuit, err := h.BeforePlanning(ctx, pctx)

		if hookSpan != nil {
			if err != nil {
				hookSpan.RecordError(err)
			}
			hookSpan.End()
		}

		if err != nil {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Pipeline hook failed, skipping", map[string]interface{}{
					"operation": "before_planning_hook",
					"hook":      hook.Name(),
					"error":     err.Error(),
				})
			}
			continue
		}

		if shortCircuit != nil {
			if o.telemetry != nil {
				o.telemetry.RecordMetric("pipeline.hook.short_circuit", 1, map[string]string{
					"hook":   hook.Name(),
					"source": shortCircuit.Source,
				})
			}
			return shortCircuit
		}
	}
	return nil
}

// runAfterPlanningHooks executes all registered AfterPlanningHook implementations.
// Each hook may mutate the plan; the returned plan is passed to the next hook.
//
//nolint:unused // retained for AfterPlanning hook coverage and targeted tests
func (o *AIOrchestrator) runAfterPlanningHooks(ctx context.Context, pctx *core.PipelineContext, plan interface{}) interface{} {
	for _, hook := range o.pipelineHooks {
		h, ok := hook.(core.AfterPlanningHook)
		if !ok {
			continue
		}

		var hookSpan core.Span
		if o.telemetry != nil {
			ctx, hookSpan = o.telemetry.StartSpan(ctx, "pipeline.hook.after_planning."+hook.Name())
		}

		mutated, err := h.AfterPlanning(ctx, pctx, plan)

		if hookSpan != nil {
			if err != nil {
				hookSpan.RecordError(err)
			}
			hookSpan.End()
		}

		if err != nil {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Pipeline hook failed, skipping", map[string]interface{}{
					"operation": "after_planning_hook",
					"hook":      hook.Name(),
					"error":     err.Error(),
				})
			}
			continue
		}

		plan = mutated
	}
	return plan
}

// runAfterExecutionHooks executes all registered AfterExecutionHook implementations.
func (o *AIOrchestrator) runAfterExecutionHooks(ctx context.Context, pctx *core.PipelineContext, results interface{}) {
	for _, hook := range o.pipelineHooks {
		h, ok := hook.(core.AfterExecutionHook)
		if !ok {
			continue
		}

		var hookSpan core.Span
		if o.telemetry != nil {
			ctx, hookSpan = o.telemetry.StartSpan(ctx, "pipeline.hook.after_execution."+hook.Name())
		}

		err := h.AfterExecution(ctx, pctx, results)

		if hookSpan != nil {
			if err != nil {
				hookSpan.RecordError(err)
			}
			hookSpan.End()
		}

		if err != nil {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Pipeline hook failed, skipping", map[string]interface{}{
					"operation": "after_execution_hook",
					"hook":      hook.Name(),
					"error":     err.Error(),
				})
			}
		}
	}
}

// runAfterSynthesisHooks executes all registered AfterSynthesisHook implementations.
// Each hook may mutate the response; the returned response is passed to the next hook.
func (o *AIOrchestrator) runAfterSynthesisHooks(ctx context.Context, pctx *core.PipelineContext, response string) string {
	for _, hook := range o.pipelineHooks {
		h, ok := hook.(core.AfterSynthesisHook)
		if !ok {
			continue
		}

		var hookSpan core.Span
		if o.telemetry != nil {
			ctx, hookSpan = o.telemetry.StartSpan(ctx, "pipeline.hook.after_synthesis."+hook.Name())
		}

		mutated, err := h.AfterSynthesis(ctx, pctx, response)

		if hookSpan != nil {
			if err != nil {
				hookSpan.RecordError(err)
			}
			hookSpan.End()
		}

		if err != nil {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Pipeline hook failed, skipping", map[string]interface{}{
					"operation": "after_synthesis_hook",
					"hook":      hook.Name(),
					"error":     err.Error(),
				})
			}
			continue
		}

		response = mutated
	}
	return response
}
