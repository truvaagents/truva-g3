package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

var (
	// ErrInvalidPipelineShortCircuitDecision identifies an opt-in hook result
	// that omitted its response payload.
	ErrInvalidPipelineShortCircuitDecision = errors.New("invalid pipeline short-circuit decision")
	// ErrUnknownPipelineShortCircuitKind identifies an empty or unsupported
	// provenance kind returned by an opt-in hook.
	ErrUnknownPipelineShortCircuitKind = errors.New("unknown pipeline short-circuit kind")
)

const pipelineShortCircuitDecisionMetric = "orchestration.pipeline.short_circuit.decision"

type pipelineGate struct {
	cacheVary         map[string]string
	cacheReadDisabled bool
}

func newPipelineGate(cacheVary map[string]string, cacheReadDisabled bool) pipelineGate {
	return pipelineGate{
		cacheVary:         cloneStringMap(cacheVary),
		cacheReadDisabled: cacheReadDisabled,
	}
}

func (g pipelineGate) CacheVary() map[string]string {
	return cloneStringMap(g.cacheVary)
}

func (g pipelineGate) ResponseCacheReadDisabled() bool {
	return g.cacheReadDisabled
}

type evaluatedPipelineShortCircuit struct {
	shortCircuit *core.PipelineShortCircuit
	kind         core.PipelineShortCircuitKind
	diagnostic   string
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func acceptShortCircuit(
	expectedCacheVary map[string]string,
	cacheReadDisabled bool,
	candidate *core.PipelineShortCircuitDecision,
	reservedDimensions ...string,
) (accept bool, diagnostic string, err error) {
	if candidate == nil || candidate.ShortCircuit == nil {
		return false, "missing_payload", ErrInvalidPipelineShortCircuitDecision
	}

	switch candidate.Kind {
	case core.PipelineShortCircuitAuthoritative:
		return true, "authoritative", nil
	case core.PipelineShortCircuitCache:
		// Cache decisions require the checks below.
	default:
		return false, "unknown_kind", ErrUnknownPipelineShortCircuitKind
	}

	if cacheReadDisabled {
		return false, "cache_read_disabled", nil
	}

	storedCacheVary := cloneStringMap(candidate.CachedAgainst)
	for _, key := range reservedDimensions {
		current, currentOK := expectedCacheVary[key]
		stored, storedOK := storedCacheVary[key]
		if currentOK != storedOK || (currentOK && current != stored) {
			return false, "cache_dimension_mismatch", nil
		}
	}

	return true, "cache_match", nil
}

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
		conversationID := conversationIDFromMetadata(metadata)
		if turns, ok := conversationTurnsFromMetadata(metadata[MetadataConversationTurns]); ok {
			prepared, _, _ := historyPreparer.PrepareFromTurns(ctx, conversationID, turns)
			if prepared != "" {
				enrichments[core.EnrichmentConversationHistory] = prepared
			}
		} else if raw, ok := metadata[core.EnrichmentConversationHistory].(string); ok && raw != "" {
			prepared, _, _ := historyPreparer.PrepareFromText(ctx, conversationID, raw)
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

// runBeforePlanningHooks executes registered before-planning hooks in order.
// Callback errors retain the legacy fail-open policy. Invalid opt-in decisions
// are framework contract errors and are returned to the request runner.
func (o *AIOrchestrator) runBeforePlanningHooks(
	ctx context.Context,
	pctx *core.PipelineContext,
	gate pipelineGate,
	reservedDimensions ...string,
) (*evaluatedPipelineShortCircuit, error) {
	for _, hook := range o.pipelineHooks {
		decisionHook, hasDecisionHook := hook.(core.BeforePlanningDecisionHook)
		legacyHook, hasLegacyHook := hook.(core.BeforePlanningHook)
		if !hasDecisionHook && !hasLegacyHook {
			continue
		}

		var hookSpan core.Span
		hookCtx := ctx
		if o.telemetry != nil {
			hookCtx, hookSpan = o.telemetry.StartSpan(ctx, "pipeline.hook.before_planning."+hook.Name())
		}

		var (
			decision *core.PipelineShortCircuitDecision
			err      error
			legacy   bool
		)
		if hasDecisionHook {
			// Supply a new defensive gate view to each hook. The gate itself also
			// clones CacheVary on every call.
			hookGate := newPipelineGate(gate.cacheVary, gate.cacheReadDisabled)
			decision, err = decisionHook.BeforePlanningDecision(hookCtx, pctx, hookGate)
		} else {
			legacy = true
			var shortCircuit *core.PipelineShortCircuit
			shortCircuit, err = legacyHook.BeforePlanning(hookCtx, pctx)
			if shortCircuit != nil {
				decision = &core.PipelineShortCircuitDecision{
					ShortCircuit: shortCircuit,
					Kind:         core.PipelineShortCircuitAuthoritative,
				}
			}
		}

		if hookSpan != nil {
			if err != nil {
				hookSpan.RecordError(err)
			}
			hookSpan.End()
		}

		if err != nil {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Pipeline hook failed, skipping", map[string]interface{}{
					"operation":  "before_planning_hook",
					"request_id": requestIDFromBaggage(ctx),
					"hook":       hook.Name(),
					"error":      err.Error(),
				})
			}
			continue
		}

		if decision == nil {
			continue
		}

		// Clone hook-owned provenance before evaluation so a caller cannot race
		// enforcement by retaining and mutating its map.
		decision = &core.PipelineShortCircuitDecision{
			ShortCircuit:  decision.ShortCircuit,
			Kind:          decision.Kind,
			CachedAgainst: cloneStringMap(decision.CachedAgainst),
		}
		decisionStartedAt := time.Now()
		accepted, reason, decisionErr := acceptShortCircuit(
			gate.cacheVary,
			gate.cacheReadDisabled,
			decision,
			reservedDimensions...,
		)
		if legacy && len(reservedDimensions) > 0 {
			reason = "legacy_authoritative"
		}
		o.recordPipelineShortCircuitDecision(ctx, hook.Name(), decision.Kind, reason, accepted)
		o.recordSkillResponseCacheDecision(
			gate.cacheVary, decision.CachedAgainst, decision.Kind,
			accepted, decisionErr, time.Since(decisionStartedAt),
		)
		if decisionErr != nil {
			return nil, fmt.Errorf("before-planning hook %q: %w", hook.Name(), decisionErr)
		}
		if !accepted {
			continue
		}

		return &evaluatedPipelineShortCircuit{
			shortCircuit: decision.ShortCircuit,
			kind:         decision.Kind,
			diagnostic:   reason,
		}, nil
	}
	return nil, nil
}

func (o *AIOrchestrator) recordPipelineShortCircuitDecision(
	ctx context.Context,
	hookName string,
	kind core.PipelineShortCircuitKind,
	reason string,
	accepted bool,
) {
	status := "rejected"
	if accepted {
		status = "accepted"
	}
	telemetry.Counter(
		pipelineShortCircuitDecisionMetric,
		"module", telemetry.ModuleOrchestration,
		"reason", reason,
		"kind", pipelineShortCircuitKindLabel(kind),
		"status", status,
	)
	requestID := requestIDFromBaggage(ctx)
	telemetry.AddSpanEvent(ctx, "pipeline.short_circuit.decision",
		attribute.String("request_id", requestID),
		attribute.String("hook", hookName),
		attribute.String("kind", pipelineShortCircuitKindLabel(kind)),
		attribute.String("reason", reason),
		attribute.String("status", status),
	)
	if accepted {
		telemetry.SetSpanAttributes(ctx,
			attribute.Bool("pipeline.short_circuit", true),
			attribute.String("pipeline.short_circuit.kind", pipelineShortCircuitKindLabel(kind)),
			attribute.String("pipeline.short_circuit.reason", reason),
		)
	} else if reason == "unknown_kind" || reason == "missing_payload" {
		// Preserve safe contract-failure facts on the request span without
		// recording hook-provided error text.
		telemetry.SetSpanAttributes(ctx,
			attribute.String("failure_stage", "before_planning"),
			attribute.String("failure_reason", reason),
			attribute.String("failure_hook", hookName),
		)
	}
	if o.logger == nil || (accepted && reason != "legacy_authoritative") {
		return
	}
	o.logger.WarnWithContext(ctx, "Pipeline short-circuit decision", map[string]interface{}{
		"operation":  "pipeline_short_circuit_decision",
		"request_id": requestID,
		"hook":       hookName,
		"kind":       pipelineShortCircuitKindLabel(kind),
		"reason":     reason,
		"status":     status,
	})
}

func pipelineShortCircuitKindLabel(kind core.PipelineShortCircuitKind) string {
	switch kind {
	case core.PipelineShortCircuitAuthoritative:
		return "authoritative"
	case core.PipelineShortCircuitCache:
		return "cache"
	default:
		return "unknown"
	}
}

// runValidatedAfterPlanningHooks activates the documented AfterPlanning stage
// with copy-on-write mutation and full plan validation. A bad hook cannot
// corrupt the last valid plan or trigger an LLM regeneration.
func (o *AIOrchestrator) runValidatedAfterPlanningHooks(
	ctx context.Context,
	pctx *core.PipelineContext,
	plan *RoutingPlan,
	completed map[string]*StepResult,
	executedStepIDs []string,
	phaseCount int,
	requestID string,
) *RoutingPlan {
	current := plan
	for _, hook := range o.pipelineHooks {
		afterPlanning, ok := hook.(core.AfterPlanningHook)
		if !ok {
			continue
		}

		candidate, cloneErr := cloneRoutingPlanForHook(current)
		if cloneErr != nil {
			o.recordAfterPlanningDecision(ctx, hook.Name(), "clone_failed", false)
			continue
		}

		var hookSpan core.Span
		hookCtx := ctx
		if o.telemetry != nil {
			hookCtx, hookSpan = o.telemetry.StartSpan(ctx, "pipeline.hook.after_planning."+hook.Name())
		}
		mutated, err := afterPlanning.AfterPlanning(hookCtx, pctx, candidate)
		if hookSpan != nil {
			if err != nil {
				hookSpan.RecordError(err)
			}
			hookSpan.End()
		}
		if err != nil {
			o.recordAfterPlanningDecision(ctx, hook.Name(), "hook_error", false)
			continue
		}
		mutatedPlan, ok := mutated.(*RoutingPlan)
		if !ok || mutatedPlan == nil {
			o.recordAfterPlanningDecision(ctx, hook.Name(), "invalid_type", false)
			continue
		}

		o.normalizeTerminalSynthesisPlan(ctx, mutatedPlan, knownStepIDSet(executedStepIDs, mutatedPlan), requestID)
		executedCaps := make(map[string]stepCapability, len(completed))
		for id, result := range completed {
			if result != nil {
				executedCaps[id] = stepCapability{agent: result.AgentName, capability: result.Capability}
			}
		}
		if validationErr := o.runPlanValidationGauntlet(ctx, mutatedPlan, executedCaps, executedStepIDs, phaseCount, requestID); validationErr != nil {
			o.recordAfterPlanningDecision(ctx, hook.Name(), "invalid_plan", false)
			continue
		}
		current = mutatedPlan
		o.recordAfterPlanningDecision(ctx, hook.Name(), "accepted", true)
	}
	return current
}

func cloneRoutingPlanForHook(plan *RoutingPlan) (*RoutingPlan, error) {
	if plan == nil {
		return nil, ErrInvalidPipelineShortCircuitDecision
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var clone RoutingPlan
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func (o *AIOrchestrator) recordAfterPlanningDecision(ctx context.Context, hook, reason string, accepted bool) {
	status := "rejected"
	if accepted {
		status = "accepted"
	}
	telemetry.Counter("orchestration.pipeline.after_planning",
		"module", telemetry.ModuleOrchestration,
		"reason", reason,
		"status", status,
	)
	if !accepted && o.logger != nil {
		o.logger.WarnWithContext(ctx, "After-planning hook mutation rejected", map[string]interface{}{
			"operation":  "after_planning_hook",
			"request_id": requestIDFromBaggage(ctx),
			"hook":       hook,
			"reason":     reason,
		})
	}
}

// runAfterExecutionHooks executes all registered AfterExecutionHook implementations.
func (o *AIOrchestrator) runAfterExecutionHooks(ctx context.Context, pctx *core.PipelineContext, results interface{}) {
	for _, hook := range o.pipelineHooks {
		h, ok := hook.(core.AfterExecutionHook)
		if !ok {
			continue
		}

		var hookSpan core.Span
		hookCtx := ctx
		if o.telemetry != nil {
			hookCtx, hookSpan = o.telemetry.StartSpan(ctx, "pipeline.hook.after_execution."+hook.Name())
		}

		err := h.AfterExecution(hookCtx, pctx, results)

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
		hookCtx := ctx
		if o.telemetry != nil {
			hookCtx, hookSpan = o.telemetry.StartSpan(ctx, "pipeline.hook.after_synthesis."+hook.Name())
		}

		mutated, err := h.AfterSynthesis(hookCtx, pctx, response)

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
