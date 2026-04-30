package core

import (
	"context"
	"net/http"
	"strconv"
)

type contextKey string

const (
	contextKeyRequestID             contextKey = "truvag3_request_id"
	contextKeyStepID                contextKey = "truvag3_step_id"
	contextKeyPhaseNumber           contextKey = "truvag3_phase_number"
	contextKeyPlanID                contextKey = "truvag3_plan_id"
	contextKeyOriginalRequestID     contextKey = "truvag3_original_request_id"
	contextKeyAgentName             contextKey = "truvag3_agent_name"
	contextKeyTokenUsageAccumulator contextKey = "truvag3_token_usage_accumulator"
)

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKeyRequestID, id)
}
func GetRequestID(ctx context.Context) string {
	if v, ok := ctx.Value(contextKeyRequestID).(string); ok {
		return v
	}
	return ""
}

func WithStepID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKeyStepID, id)
}
func GetStepID(ctx context.Context) string {
	if v, ok := ctx.Value(contextKeyStepID).(string); ok {
		return v
	}
	return ""
}

// WithPhaseNumber adds the phase number to the context for multi-phase iterative planning.
func WithPhaseNumber(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, contextKeyPhaseNumber, n)
}

// GetPhaseNumber retrieves the phase number from context. Returns 0 if not set.
func GetPhaseNumber(ctx context.Context) int {
	if v, ok := ctx.Value(contextKeyPhaseNumber).(int); ok {
		return v
	}
	return 0
}

// WithPlanID / GetPlanID carry the orchestrator's plan_id onto the tool side
// so server spans and logs can be joined back to the originating plan.
func WithPlanID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKeyPlanID, id)
}
func GetPlanID(ctx context.Context) string {
	if v, ok := ctx.Value(contextKeyPlanID).(string); ok {
		return v
	}
	return ""
}

// WithOriginalRequestID / GetOriginalRequestID expose the original request_id
// (preserved across HITL resumes) to tool-side instrumentation.
func WithOriginalRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKeyOriginalRequestID, id)
}
func GetOriginalRequestID(ctx context.Context) string {
	if v, ok := ctx.Value(contextKeyOriginalRequestID).(string); ok {
		return v
	}
	return ""
}

// WithAgentName / GetAgentName carry the calling agent identity to tool spans.
func WithAgentName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, contextKeyAgentName, name)
}
func GetAgentName(ctx context.Context) string {
	if v, ok := ctx.Value(contextKeyAgentName).(string); ok {
		return v
	}
	return ""
}

// ExtractRequestContext extracts TruvaG3 orchestration context from HTTP headers.
// This is the primary mechanism for receiving request_id and step_id from the orchestrator
// (Issue 18: executor's httpClient does not use otelhttp transport).
// Agents using telemetry.NewTracedHTTPHandler may also get OTel baggage as a bonus.
//
// Issue 11 fix: No componentName parameter — the component identity is set at
// InstrumentedAIClient construction time via WithComponentName(), not per-request.
func ExtractRequestContext(ctx context.Context, r *http.Request) context.Context {
	if reqID := r.Header.Get("X-TruvaG3-Request-ID"); reqID != "" {
		ctx = WithRequestID(ctx, reqID)
	}
	if stepID := r.Header.Get("X-TruvaG3-Step-ID"); stepID != "" {
		ctx = WithStepID(ctx, stepID)
	}
	// Extract phase number for agent-side LLM debug recording
	if phaseStr := r.Header.Get("X-TruvaG3-Phase-Number"); phaseStr != "" {
		if n, err := strconv.Atoi(phaseStr); err == nil && n > 0 {
			ctx = WithPhaseNumber(ctx, n)
		}
	}
	if planID := r.Header.Get("X-TruvaG3-Plan-ID"); planID != "" {
		ctx = WithPlanID(ctx, planID)
	}
	if origID := r.Header.Get("X-TruvaG3-Original-Request-ID"); origID != "" {
		ctx = WithOriginalRequestID(ctx, origID)
	}
	if agentName := r.Header.Get("X-TruvaG3-Agent-Name"); agentName != "" {
		ctx = WithAgentName(ctx, agentName)
	}
	return ctx
}

// WithTokenUsageAccumulator injects a new AggregatedTokenUsage into the context.
func WithTokenUsageAccumulator(ctx context.Context) (context.Context, *AggregatedTokenUsage) {
	acc := NewAggregatedTokenUsage()
	return context.WithValue(ctx, contextKeyTokenUsageAccumulator, acc), acc
}

// GetTokenUsageAccumulator retrieves the accumulator from context, or nil.
func GetTokenUsageAccumulator(ctx context.Context) *AggregatedTokenUsage {
	if acc, ok := ctx.Value(contextKeyTokenUsageAccumulator).(*AggregatedTokenUsage); ok {
		return acc
	}
	return nil
}

// RecordTokenUsage is a convenience function that records usage if an
// accumulator is present in the context. No-op if not present.
func RecordTokenUsage(ctx context.Context, phase string, usage TokenUsage) {
	if acc := GetTokenUsageAccumulator(ctx); acc != nil {
		acc.Add(phase, usage)
	}
}
