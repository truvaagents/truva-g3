package core

import (
	"context"
	"net/http"
	"strconv"
	"unicode/utf8"
)

type contextKey string

const (
	contextKeyRequestID                         contextKey = "truvag3_request_id"
	contextKeyStepID                            contextKey = "truvag3_step_id"
	contextKeyPhaseNumber                       contextKey = "truvag3_phase_number"
	contextKeyPlanID                            contextKey = "truvag3_plan_id"
	contextKeyOriginalRequestID                 contextKey = "truvag3_original_request_id"
	contextKeyConversationID                    contextKey = "truvag3_conversation_id"
	contextKeyConversationProgrammaticCandidate contextKey = "truvag3_conversation_programmatic_candidate"
	contextKeyConversationHeaderCandidate       contextKey = "truvag3_conversation_header_candidate"
	contextKeyAgentName                         contextKey = "truvag3_agent_name"
	contextKeyTokenUsageAccumulator             contextKey = "truvag3_token_usage_accumulator" // #nosec G101 -- context key for LLM token-usage accounting, not a credential
)

// MaxConversationIDLength is the maximum byte length of a conversation ID.
// It is a protocol invariant aligned with the telemetry baggage value limit.
const MaxConversationIDLength = 512

// ConversationIDValidationReason is a bounded classification for a rejected
// conversation ID. It never contains the rejected value.
type ConversationIDValidationReason string

const (
	ConversationIDValidationNone             ConversationIDValidationReason = ""
	ConversationIDValidationEmpty            ConversationIDValidationReason = "empty"
	ConversationIDValidationInvalidType      ConversationIDValidationReason = "invalid_type"
	ConversationIDValidationTooLong          ConversationIDValidationReason = "too_long"
	ConversationIDValidationInvalidUTF8      ConversationIDValidationReason = "invalid_utf8"
	ConversationIDValidationInvalidCharacter ConversationIDValidationReason = "invalid_character"
)

// ConversationIDCandidateSource identifies the core source of a conversation
// ID candidate.
type ConversationIDCandidateSource string

const (
	ConversationIDSourceCoreContext ConversationIDCandidateSource = "core_context"
	ConversationIDSourceCoreHeader  ConversationIDCandidateSource = "core_header"
)

// ConversationIDCandidate records source and presence separately from
// validity. Value is populated only for a valid candidate.
type ConversationIDCandidate struct {
	Present         bool
	Value           string
	Source          ConversationIDCandidateSource
	RejectionReason ConversationIDValidationReason
}

// ValidateConversationID validates an opaque conversation ID for exact W3C
// baggage propagation. Valid values contain 1-512 bytes from these visible
// ASCII ranges: 0x21, 0x23-0x2B, 0x2D-0x3A, 0x3C-0x5B, or 0x5D-0x7E.
func ValidateConversationID(id string) ConversationIDValidationReason {
	if id == "" {
		return ConversationIDValidationEmpty
	}
	if len(id) > MaxConversationIDLength {
		return ConversationIDValidationTooLong
	}
	if !utf8.ValidString(id) {
		return ConversationIDValidationInvalidUTF8
	}
	for i := 0; i < len(id); i++ {
		if !isConversationIDByteAllowed(id[i]) {
			return ConversationIDValidationInvalidCharacter
		}
	}
	return ConversationIDValidationNone
}

func isConversationIDByteAllowed(value byte) bool {
	return value == 0x21 ||
		(value >= 0x23 && value <= 0x2b) ||
		(value >= 0x2d && value <= 0x3a) ||
		(value >= 0x3c && value <= 0x5b) ||
		(value >= 0x5d && value <= 0x7e)
}

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

// WithConversationID records a programmatic conversation ID candidate.
// Explicit-header candidates, when present, retain precedence independent of
// the order in which the two sources are applied.
func WithConversationID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	candidate := ConversationIDCandidate{
		Present: true,
		Source:  ConversationIDSourceCoreContext,
	}
	if reason := ValidateConversationID(id); reason != ConversationIDValidationNone {
		candidate.RejectionReason = reason
		return withConversationIDProgrammaticCandidate(ctx, candidate)
	}
	candidate.Value = id
	return withConversationIDProgrammaticCandidate(ctx, candidate)
}

// GetConversationID returns the effective validated conversation ID.
func GetConversationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(contextKeyConversationID).(string); ok {
		return value
	}
	return ""
}

// GetConversationIDCandidate returns the effective core candidate: an
// explicit-header candidate when present, otherwise the programmatic one.
func GetConversationIDCandidate(ctx context.Context) ConversationIDCandidate {
	if ctx == nil {
		return ConversationIDCandidate{}
	}
	if candidate, ok := ctx.Value(contextKeyConversationHeaderCandidate).(ConversationIDCandidate); ok && candidate.Present {
		return candidate
	}
	if candidate, ok := ctx.Value(contextKeyConversationProgrammaticCandidate).(ConversationIDCandidate); ok {
		return candidate
	}
	return ConversationIDCandidate{}
}

// WithoutConversationID shadows inherited conversation identity while
// preserving unrelated context values.
func WithoutConversationID(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = withConversationIDValue(ctx, "")
	ctx = context.WithValue(
		ctx,
		contextKeyConversationProgrammaticCandidate,
		ConversationIDCandidate{},
	)
	return context.WithValue(
		ctx,
		contextKeyConversationHeaderCandidate,
		ConversationIDCandidate{},
	)
}

func withConversationIDProgrammaticCandidate(
	ctx context.Context,
	candidate ConversationIDCandidate,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	candidate = normalizeConversationIDCandidate(candidate)
	ctx = context.WithValue(ctx, contextKeyConversationProgrammaticCandidate, candidate)
	return refreshConversationIDValue(ctx)
}

func withConversationIDHeaderCandidate(
	ctx context.Context,
	candidate ConversationIDCandidate,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	candidate = normalizeConversationIDCandidate(candidate)
	ctx = context.WithValue(ctx, contextKeyConversationHeaderCandidate, candidate)
	return refreshConversationIDValue(ctx)
}

func withoutConversationIDHeaderCandidate(ctx context.Context) context.Context {
	return withConversationIDHeaderCandidate(ctx, ConversationIDCandidate{})
}

func normalizeConversationIDCandidate(candidate ConversationIDCandidate) ConversationIDCandidate {
	if !candidate.Present {
		return ConversationIDCandidate{}
	}
	if candidate.RejectionReason != ConversationIDValidationNone {
		candidate.Value = ""
		return candidate
	}
	if reason := ValidateConversationID(candidate.Value); reason != ConversationIDValidationNone {
		candidate.Value = ""
		candidate.RejectionReason = reason
	}
	return candidate
}

func refreshConversationIDValue(ctx context.Context) context.Context {
	candidate := GetConversationIDCandidate(ctx)
	if candidate.Present && candidate.RejectionReason == ConversationIDValidationNone {
		return withConversationIDValue(ctx, candidate.Value)
	}
	return withConversationIDValue(ctx, "")
}

func withConversationIDValue(ctx context.Context, value string) context.Context {
	return context.WithValue(ctx, contextKeyConversationID, value)
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
