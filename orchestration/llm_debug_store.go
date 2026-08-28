// Package orchestration provides LLM debug payload storage for production debugging.
// This file defines the interface and data types for storing complete LLM prompts
// and responses, enabling operators to debug orchestration issues without truncation.
//
// Design follows FRAMEWORK_DESIGN_PRINCIPLES.md:
// - Interface-first design for swappable backends
// - Safe defaults (NoOp when unavailable)
// - Disabled by default (enable via TRUVAG3_LLM_DEBUG_ENABLED=true)
package orchestration

import (
	"context"
	"errors"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// ErrLLMDebugRecordNotFound identifies the expected absence of an LLM debug
// record. Valid executions do not necessarily call an LLM, so retention
// coordination uses errors.Is to suppress this condition without hiding real
// backend failures.
var ErrLLMDebugRecordNotFound = errors.New("LLM debug record not found")

// LLMDebugStore stores LLM interaction payloads for debugging.
// Implementations must be safe for concurrent use.
//
// The interface supports multiple backends (Redis, PostgreSQL, S3, etc.)
// allowing teams to choose storage that fits their needs.
type LLMDebugStore interface {
	// RecordInteraction appends an LLM interaction to the debug record.
	// This is called asynchronously from the orchestrator to avoid latency impact.
	// Errors should be logged but not propagated to avoid blocking orchestration.
	RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error

	// GetRecord retrieves the complete debug record for a request.
	// Returns an error if the record is not found or has expired.
	GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error)

	// SetMetadata adds metadata to an existing record.
	// Useful for adding investigation notes or flags.
	SetMetadata(ctx context.Context, requestID string, key, value string) error

	// ExtendTTL extends retention for investigation.
	// Allows keeping important records longer than the default TTL.
	ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error

	// ListRecent returns recent records for UI listing.
	// Results are ordered by creation time, newest first.
	ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error)
}

// LLMDebugRetentionPreserver is the optional capability used when final
// execution retention must survive LLM writes that have not started yet or
// originate in another process. Implementations establish a retention floor
// without creating an empty debug record and apply it to any existing record.
type LLMDebugRetentionPreserver interface {
	PreserveRetention(ctx context.Context, requestID string, duration time.Duration) error
}

// LLMDebugRecord stores all LLM interactions for a single orchestration request.
// This is the complete record stored in the backend.
type LLMDebugRecord struct {
	// RequestID is the orchestration request identifier
	RequestID string `json:"request_id"`

	// OriginalRequestID links related records across HITL resumes.
	// For initial requests: same as RequestID
	// For resume requests: the original conversation's RequestID
	// This enables finding all LLM calls in a HITL conversation.
	OriginalRequestID string `json:"original_request_id,omitempty"`

	// TraceID links to distributed tracing backends (e.g., via OTLP)
	TraceID string `json:"trace_id"`

	// CreatedAt is when the first interaction was recorded
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the record was last modified
	UpdatedAt time.Time `json:"updated_at"`

	// Interactions contains all LLM calls for this request
	Interactions []LLMInteraction `json:"interactions"`

	// OriginatingAgent is the agent whose orchestrator (or background job) initiated
	// this request — the "originator" surfaced in the registry-viewer LLM Debug
	// table Source column. Sourced from OTel baggage key "agent_name" (which the
	// orchestrator stamps from o.config.Name) and persisted via HSetNX so the
	// first writer wins. Empty for records written before this field was added.
	OriginatingAgent string `json:"originating_agent,omitempty"`

	// Metadata contains framework-owned correlation metadata plus optional
	// application investigation metadata. MetadataConversationID is reserved
	// for the immutable conversation identity captured by the first valid
	// writer.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// LLMDebugConversationID returns the record's multi-turn correlation ID.
func LLMDebugConversationID(record *LLMDebugRecord) string {
	if record == nil {
		return ""
	}
	return record.Metadata[MetadataConversationID]
}

func llmDebugConversationIDFromContext(ctx context.Context) string {
	coreCandidate := core.GetConversationIDCandidate(ctx)
	if coreCandidate.Present {
		if coreCandidate.RejectionReason != core.ConversationIDValidationNone ||
			core.ValidateConversationID(coreCandidate.Value) != core.ConversationIDValidationNone {
			return ""
		}
		return coreCandidate.Value
	}

	conversationID := telemetry.GetBaggage(ctx)[MetadataConversationID]
	if core.ValidateConversationID(conversationID) != core.ConversationIDValidationNone {
		return ""
	}
	return conversationID
}

// Hook execution phases used in LLMInteraction.HookPhase.
// Empty HookPhase means the interaction is an orchestration-level LLM call
// (plan generation, synthesis, tiered selection, etc.) — not part of a
// pipeline hook. Kept as a string (rather than a typed enum) so downstream
// consumers that don't know about future phases deserialize safely.
const (
	// HookPhasePre marks interactions emitted by BeforePlanning-style hooks:
	// user-memory recall, enrichment injection, and the activity compactor.
	HookPhasePre = "pre"

	// HookPhasePost marks interactions emitted by AfterSynthesis-style hooks:
	// fact extraction, persistence policy, reconciliation, remember, summary,
	// and the event summarizer.
	HookPhasePost = "post"

	// Reserved for future async/scheduled work (e.g., the reflection job in
	// memory/reflection_job.go if it ever records LLM interactions):
	//   HookPhaseBackground = "background"
)

// LLMInteraction captures a single LLM call (request + response).
// This includes the complete prompt and response without truncation.
type LLMInteraction struct {
	// Type identifies the interaction purpose.
	// Orchestration: "plan_generation", "continuation_plan_generation", "continuation_plan_regeneration", "synthesis", "synthesis_streaming", "correction"
	// Resolution: "micro_resolution", "schema_mapping", "semantic_retry"
	// Analysis: "error_analysis", "hallucination_detection", "result_distillation"
	// Selection: "tiered_selection"
	// Memory (shared): "activity_compaction", "activity_compaction_incremental", "event_summarization"
	// Memory (user): "user_memory_recall_identity", "user_memory_recall_summary",
	//   "user_memory_recall_stable_namespace", "user_memory_recall_query",
	//   "user_memory_recall_universal", "user_memory_enrichment_injected", "user_memory_extraction",
	//   "user_memory_embed_candidate", "user_memory_similarity_search",
	//   "user_memory_persistence_policy", "user_memory_summary_persistence_policy",
	//   "user_memory_reconciliation", "user_memory_reconciliation_skip",
	//   "user_memory_reconciliation_batch", "user_memory_reconciliation_batch_item",
	//   "user_memory_remember", "user_memory_summary", "user_memory_summary_remember"
	// Agent: "agent_llm_call"
	Type string `json:"type"`

	// HookPhase classifies interactions emitted inside pipeline hooks so the
	// registry viewer can route them to the Pre-Execution / Post-Execution
	// tabs without maintaining a brittle type-string allowlist in JS.
	// Empty means the interaction is an orchestration-level call.
	// See HookPhasePre, HookPhasePost for the valid non-empty values.
	HookPhase string `json:"hook_phase,omitempty"`

	// Timestamp is when the interaction started
	Timestamp time.Time `json:"timestamp"`

	// DurationMs is the LLM call duration in milliseconds
	DurationMs int64 `json:"duration_ms"`

	// Request fields
	Prompt            string                     `json:"prompt"`                  // Complete prompt sent to LLM
	SystemPrompt      string                     `json:"system_prompt,omitempty"` // System prompt if used
	Temperature       float64                    `json:"temperature"`
	MaxTokens         int                        `json:"max_tokens"`
	Model             string                     `json:"model,omitempty"`    // Model identifier (e.g., "gpt-4o-mini")
	Provider          string                     `json:"provider,omitempty"` // Provider (e.g., "openai", "anthropic")
	RequestedModel    string                     `json:"requested_model,omitempty"`
	EffectiveModel    string                     `json:"effective_model,omitempty"`
	Adjustments       []core.AIRequestAdjustment `json:"adjustments,omitempty"`
	PolicyFingerprint string                     `json:"policy_fingerprint,omitempty"`
	PolicyStable      bool                       `json:"policy_stable"`

	// Response fields
	Response         string `json:"response"` // Complete LLM response
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`

	// Status fields
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"` // Error message if failed
	Attempt int    `json:"attempt"`         // Attempt number (for retries)

	// StepID associates this LLM call with a specific execution step.
	// Populated for: micro_resolution, semantic_retry (step-specific calls)
	// Empty for: plan_generation, correction, synthesis, tiered_selection (orchestrator-level)
	StepID string `json:"step_id,omitempty"`

	// SourceComponent identifies which component made this LLM call.
	// Empty for orchestrator-level calls (plan_generation, synthesis, etc.)
	// Populated for agent/tool-level calls (e.g., "research-assistant-resilience")
	SourceComponent string `json:"source_component,omitempty"`

	// CallDescription provides human-readable context for what the LLM call does.
	// E.g., "Tool selection for research topic", "Payload generation for weather API"
	CallDescription string `json:"call_description,omitempty"`

	// PhaseNumber identifies which phase this LLM call belongs to.
	// 0 or 1 = single-phase execution (backward compatible, omitted in JSON).
	// 2+ = multi-phase execution (continuation plan, Phase N micro_resolution, etc.)
	PhaseNumber int `json:"phase_number,omitempty"`

	// Category classifies the interaction type for rendering purposes.
	// Values: "llm" (LLM API call), "embedding" (embedding API call),
	// "vector_db" (vector database query/write), "storage" (data store write),
	// "logic" (application logic, no external call).
	// Empty string is treated as "llm" for backward compatibility —
	// existing hooks don't set this field, so all current interactions
	// render as full LLM cards. User memory hooks (Phase 4) set it explicitly.
	Category string `json:"category,omitempty"`

	// PrecedenceAudit records how the <context_precedence> rule interacted
	// with this interaction's enrichments. Populated by the central
	// recordDebugInteraction path via DerivePrecedenceAudit; nil for
	// interactions whose prompt carries no conflict-eligible enrichment
	// (hook LLM calls, micro-resolution, tiered selection, etc.).
	//
	// Cheap fields (DirectiveEmitted, ProfilePresent, HistoryPresent,
	// PromptKind) populate without any extractor. Entity fields and
	// Compliance populate only when the orchestrator has a
	// PrecedenceEntityExtractor configured. See precedence_audit.go.
	PrecedenceAudit *PrecedenceAudit `json:"precedence_audit,omitempty"`
}

// LLMDebugRecordSummary is a lightweight version for listing.
// Used by the ListRecent API to avoid loading full payloads.
type LLMDebugRecordSummary struct {
	RequestID         string    `json:"request_id"`
	OriginalRequestID string    `json:"original_request_id,omitempty"`
	TraceID           string    `json:"trace_id"`
	CreatedAt         time.Time `json:"created_at"`
	InteractionCount  int       `json:"interaction_count"`
	TotalTokens       int       `json:"total_tokens"`
	HasErrors         bool      `json:"has_errors"`
	// SourceComponents lists the unique agent/component names that made LLM calls
	// in this record. Empty for orchestrator-only records (plan_generation, synthesis, etc.).
	// Populated when agents use InstrumentedAIClient with WithComponentName().
	SourceComponents []string `json:"source_components,omitempty"`

	// OriginatingAgent is the agent whose orchestrator (or background job) initiated
	// this request. See LLMDebugRecord.OriginatingAgent for the full description.
	OriginatingAgent string `json:"originating_agent,omitempty"`
}

// LLMDebugConfig holds configuration for LLM debug storage.
// This is embedded in OrchestratorConfig.
type LLMDebugConfig struct {
	// Enabled controls whether debug payload storage is active.
	// Default: false (disabled). Enable via TRUVAG3_LLM_DEBUG_ENABLED=true
	Enabled bool `json:"enabled"`

	// TTL is the retention period for successful records.
	// Default: 24h. Override via TRUVAG3_LLM_DEBUG_TTL
	TTL time.Duration `json:"ttl"`

	// ErrorTTL is the retention period for records with errors.
	// Default: 168h (7 days). Override via TRUVAG3_LLM_DEBUG_ERROR_TTL
	ErrorTTL time.Duration `json:"error_ttl"`

	// RedisDB is the Redis database number for storage.
	// Default: 7 (core.RedisDBLLMDebug). Override via TRUVAG3_LLM_DEBUG_REDIS_DB
	RedisDB int `json:"redis_db"`
}

// DefaultLLMDebugConfig returns the default configuration for LLM debug storage.
// Feature is disabled by default per design principles.
func DefaultLLMDebugConfig() LLMDebugConfig {
	return LLMDebugConfig{
		Enabled:  false,              // Disabled by default
		TTL:      24 * time.Hour,     // 24 hours for success
		ErrorTTL: 7 * 24 * time.Hour, // 7 days for errors
		RedisDB:  7,                  // core.RedisDBLLMDebug
	}
}
