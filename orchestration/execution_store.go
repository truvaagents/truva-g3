// Package orchestration provides execution storage for DAG visualization.
// This file defines the interface and data types for storing complete plan
// execution records (plan + result), enabling visualization of LLM-based
// plan execution as a directed acyclic graph (DAG).
//
// Design follows FRAMEWORK_DESIGN_PRINCIPLES.md:
// - Interface-first design for swappable backends
// - Safe defaults (NoOp when unavailable)
// - Disabled by default (enable via TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true)
// - Dependency inversion (StorageProvider interface, not Redis directly)
//
// See orchestration/notes/DAG_VISUALIZATION_PROPOSAL.md for full design rationale.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// ExecutionStore stores execution records (plan + result) for debugging and visualization.
// Implementations must be safe for concurrent use.
//
// Design follows FRAMEWORK_DESIGN_PRINCIPLES.md:
// - Interface-first design for swappable backends
// - Safe defaults (NoOp when unavailable)
// - Disabled by default (enable via TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true)
type ExecutionStore interface {
	// Store saves a complete execution record (plan + result).
	// This is called asynchronously from the orchestrator to avoid latency impact.
	// Errors should be logged but not propagated to avoid blocking orchestration.
	Store(ctx context.Context, execution *StoredExecution) error

	// Get retrieves the complete execution record by request ID.
	// Returns an error if the record is not found or has expired.
	Get(ctx context.Context, requestID string) (*StoredExecution, error)

	// GetByTraceID retrieves an execution by distributed trace ID.
	// Useful for correlating with Jaeger traces.
	GetByTraceID(ctx context.Context, traceID string) (*StoredExecution, error)

	// SetMetadata adds application investigation metadata to an existing
	// record. Framework-owned reserved correlation keys are rejected.
	SetMetadata(ctx context.Context, requestID string, key, value string) error

	// ExtendTTL extends retention for investigation.
	// Allows keeping important records longer than the default TTL.
	ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error

	// ListRecent returns recent records for UI listing.
	// Results are ordered by creation time, newest first.
	ListRecent(ctx context.Context, limit int) ([]ExecutionSummary, error)
}

// StoredExecution contains everything needed for DAG visualization.
// This is stored as a single record to ensure atomicity and self-containment.
//
// # Multi-phase record contract (ORCH-022)
//
// For every multi-phase StoredExecution — success, interrupted, errored, or
// intermediate (inter-phase snapshot) — the following shape applies:
//
//   - Plan is the LAST / CURRENT phase's plan. It is NOT the accumulated plan
//     across phases. Consumers reading only Plan.Steps for a multi-phase record
//     will see only the last phase's steps and UNDER-COUNT.
//   - PhasePlans is the AUTHORITATIVE ordered list of all phase plans. Length
//     is 0 or 1 for single-phase records; >1 for multi-phase records. Consumers
//     reading step definitions for multi-phase records MUST consult PhasePlans
//     first and fall back to Plan only when len(PhasePlans) <= 1.
//   - Result.Steps is the cross-phase list of executed steps, ordered by
//     execution (prior phases in accumulator order, then any current-phase
//     partials populated on interrupt or error).
//   - Interrupted == true signals HITL interruption. This is the canonical
//     "is interrupted" signal. Do NOT use Result == nil as a proxy; post
//     ORCH-022, Result is non-nil for all records that reached the phase loop.
//
// The registry viewer's normalizeSteps helper
// (examples/registry-viewer-app/main.go) follows this contract. API consumers
// reading the Redis record directly should do the same.
type StoredExecution struct {
	// Correlation identifiers
	RequestID         string `json:"request_id"`
	OriginalRequestID string `json:"original_request_id,omitempty"` // For HITL resume correlation
	TraceID           string `json:"trace_id"`                      // For distributed tracing

	// AgentName identifies the orchestrator that created this execution.
	// Populated from OrchestratorConfig.Name or RequestIDPrefix or "orchestrator".
	// Used as root node label in full execution flow visualization.
	AgentName string `json:"agent_name,omitempty"`

	// Original user request (for search and display)
	OriginalRequest string `json:"original_request"`

	// Plan holds the LAST / CURRENT phase's plan (see multi-phase record
	// contract in the type godoc). For multi-phase records, read PhasePlans
	// for the full plan history. Plan.Steps alone is not authoritative for
	// multi-phase executions.
	Plan *RoutingPlan `json:"plan"`

	// Result contains the execution's step-level results. Post ORCH-022,
	// Result is non-nil for all records that reached the phase loop — including
	// interrupted records. Use Interrupted, not Result == nil, to detect HITL
	// interruption.
	Result *ExecutionResult `json:"result"`

	// Timestamps
	CreatedAt time.Time `json:"created_at"`

	// Interrupted is the canonical "is interrupted" signal. True when execution
	// was interrupted for HITL approval.
	Interrupted bool                 `json:"interrupted,omitempty"`
	Checkpoint  *ExecutionCheckpoint `json:"checkpoint,omitempty"` // Checkpoint data if interrupted

	// PhasePlans is the authoritative ordered list of all phase plans for
	// multi-phase executions. See the multi-phase record contract above.
	PhasePlans     []*RoutingPlan `json:"phase_plans,omitempty"`
	PhaseCount     int            `json:"phase_count,omitempty"`     // Number of phases executed
	ForcedTerminal bool           `json:"forced_terminal,omitempty"` // Whether max phases forced termination

	// Metadata contains framework-owned correlation metadata plus optional
	// application investigation metadata. MetadataConversationID is reserved
	// for the immutable conversation identity captured at execution time.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ExecutionSummary is a lightweight version for listing.
// Used by ListRecent to avoid loading full payloads.
// Note: Named ExecutionSummary (not ExecutionResultSummary) to avoid
// collision with existing ExecutionRecord type in interfaces.go:150
type ExecutionSummary struct {
	RequestID         string        `json:"request_id"`
	OriginalRequestID string        `json:"original_request_id,omitempty"`
	TraceID           string        `json:"trace_id"`
	AgentName         string        `json:"agent_name,omitempty"`
	OriginalRequest   string        `json:"original_request"`
	Success           bool          `json:"success"`
	Interrupted       bool          `json:"interrupted,omitempty"`
	StepCount         int           `json:"step_count"`
	FailedSteps       int           `json:"failed_steps"`
	TotalDuration     time.Duration `json:"total_duration"`
	CreatedAt         time.Time     `json:"created_at"`
	PhaseCount        int           `json:"phase_count,omitempty"`
	ForcedTerminal    bool          `json:"forced_terminal,omitempty"`
	// Metadata contains framework-owned correlation metadata plus optional
	// application investigation metadata. MetadataConversationID is reserved.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ExecutionConversationID returns the stored multi-turn correlation ID.
func ExecutionConversationID(execution *StoredExecution) string {
	if execution == nil {
		return ""
	}
	return execution.Metadata[MetadataConversationID]
}

// ExecutionSummaryConversationID returns the summary's multi-turn correlation
// ID.
func ExecutionSummaryConversationID(summary ExecutionSummary) string {
	return summary.Metadata[MetadataConversationID]
}

// StorageProvider abstracts the underlying storage backend.
// Implementations can be Redis, PostgreSQL, S3, etc.
//
// This follows FRAMEWORK_DESIGN_PRINCIPLES.md:
// - "All modules depend on core interfaces, not implementations"
// - "Core module should NOT make assumptions about specific implementations (Redis, OpenAI, etc.)"
//
// The application is responsible for providing a concrete implementation.
//
// NOTE: Method names are intentionally storage-agnostic (not Redis-specific).
// The sorted index operations can be implemented by:
// - Redis: ZADD, ZREVRANGEBYSCORE, ZREM
// - PostgreSQL: INSERT with score column, SELECT ORDER BY score DESC, DELETE
// - DynamoDB: GSI with sort key
type StorageProvider interface {
	// Get retrieves a value by key. Returns empty string and nil error if not found.
	Get(ctx context.Context, key string) (string, error)

	// Set stores a value with TTL. Use 0 for no expiration.
	Set(ctx context.Context, key string, value string, ttl time.Duration) error

	// Del deletes one or more keys.
	Del(ctx context.Context, keys ...string) error

	// Exists checks if a key exists.
	Exists(ctx context.Context, key string) (bool, error)

	// AddToIndex adds a member with score to a sorted index.
	// Used for time-based listing (score = timestamp).
	// Redis implementation: ZADD
	AddToIndex(ctx context.Context, key string, score float64, member string) error

	// ListByScoreDesc returns members from sorted index (highest score first) with pagination.
	// Used for listing recent executions.
	// Redis implementation: ZREVRANGEBYSCORE
	ListByScoreDesc(ctx context.Context, key string, min, max string, offset, count int64) ([]string, error)

	// RemoveFromIndex removes members from a sorted index.
	// Used for cleaning up stale index entries.
	// Redis implementation: ZREM
	RemoveFromIndex(ctx context.Context, key string, members ...string) error
}

// ExecutionStoreConfig holds configuration for execution storage.
// This is embedded in OrchestratorConfig.
//
// Note: Storage-specific settings (Redis DB, connection URL, etc.) are NOT here.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md, the framework doesn't assume specific backends.
// The application provides storage configuration when creating the StorageProvider.
type ExecutionStoreConfig struct {
	// Enabled controls whether execution storage is active.
	// Default: false (disabled). Enable via TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true
	Enabled bool `json:"enabled"`

	// TTL is the retention period for successful records.
	// Default: 24h. Override via TRUVAG3_EXECUTION_TTL
	// This is passed to the StorageProvider implementation.
	TTL time.Duration `json:"ttl"`

	// ErrorTTL is the retention period for records with errors.
	// Default: 168h (7 days). Override via TRUVAG3_EXECUTION_ERROR_TTL
	ErrorTTL time.Duration `json:"error_ttl"`

	// KeyPrefix is the prefix for all storage keys.
	// Default: "truvag3:execution". Override via TRUVAG3_EXECUTION_KEY_PREFIX
	// This allows multi-tenant deployments or custom namespacing.
	// Per FRAMEWORK_DESIGN_PRINCIPLES.md: "Explicit Override: Always allow explicit configuration"
	KeyPrefix string `json:"key_prefix"`
}

// DefaultExecutionStoreConfig returns the default configuration.
// Feature is disabled by default per FRAMEWORK_DESIGN_PRINCIPLES.md.
func DefaultExecutionStoreConfig() ExecutionStoreConfig {
	return ExecutionStoreConfig{
		Enabled:   false,                      // Disabled by default
		TTL:       24 * time.Hour,             // 24 hours for success
		ErrorTTL:  7 * 24 * time.Hour,         // 7 days for errors
		KeyPrefix: "truvag3:execution:debug:", // Default prefix with trailing colon
	}
}

// Default key prefix constant (for documentation and backwards compatibility)
const (
	// DefaultExecutionKeyPrefix is the default prefix for execution debug storage keys
	DefaultExecutionKeyPrefix = "truvag3:execution:debug:"
)

// executionStoreImpl is the default implementation of ExecutionStore
// backed by a StorageProvider.
type executionStoreImpl struct {
	provider StorageProvider
	config   ExecutionStoreConfig
	logger   core.Logger
}

// NewExecutionStoreWithProvider creates an ExecutionStore backed by the given StorageProvider.
// This is the recommended way to create an ExecutionStore - the application provides
// the storage backend implementation (Redis, PostgreSQL, etc.).
func NewExecutionStoreWithProvider(provider StorageProvider, config ExecutionStoreConfig, logger core.Logger) ExecutionStore {
	// Ensure KeyPrefix has a default value
	if config.KeyPrefix == "" {
		config.KeyPrefix = DefaultExecutionKeyPrefix
	}
	return &executionStoreImpl{
		provider: provider,
		config:   config,
		logger:   logger,
	}
}

// Key building helper methods using configurable KeyPrefix
// These follow the pattern: {KeyPrefix}:{suffix}

// recordKey returns the key for storing an execution record
func (s *executionStoreImpl) recordKey(requestID string) string {
	return s.config.KeyPrefix + ":" + requestID
}

// indexKey returns the key for the sorted index of recent executions
func (s *executionStoreImpl) indexKey() string {
	return s.config.KeyPrefix + ":index"
}

// traceKey returns the key for trace ID → request ID mapping
func (s *executionStoreImpl) traceKey(traceID string) string {
	return s.config.KeyPrefix + ":trace:" + traceID
}

// Store saves a complete execution record (plan + result).
func (s *executionStoreImpl) Store(ctx context.Context, execution *StoredExecution) error {
	if execution == nil {
		return fmt.Errorf("execution cannot be nil")
	}
	if execution.RequestID == "" {
		return fmt.Errorf("request_id is required")
	}

	// Serialize to JSON
	data, err := json.Marshal(execution)
	if err != nil {
		return fmt.Errorf("failed to marshal execution: %w", err)
	}

	// Determine TTL based on success/failure.
	// ORCH-022: interrupted records have Result.Success == false after the
	// orchestrator fix (Result was nil pre-fix, bypassing this branch). Carve
	// them out so pending HITL approvals keep the default TTL rather than the
	// shorter ErrorTTL.
	ttl := s.config.TTL
	if execution.Result != nil && !execution.Result.Success && !execution.Interrupted {
		ttl = s.config.ErrorTTL
	}

	// Store the main record
	key := s.recordKey(execution.RequestID)
	if err := s.provider.Set(ctx, key, string(data), ttl); err != nil {
		return fmt.Errorf("failed to store execution: %w", err)
	}

	// Add to index (sorted set by timestamp)
	score := float64(execution.CreatedAt.UnixNano())
	if err := s.provider.AddToIndex(ctx, s.indexKey(), score, execution.RequestID); err != nil {
		if s.logger != nil {
			s.logger.Warn("Failed to add execution to index", map[string]interface{}{
				"operation":  "execution_store_index",
				"request_id": execution.RequestID,
				"error":      err.Error(),
			})
		}
		// Continue - main record is stored
	}

	// Store trace ID mapping if available
	if execution.TraceID != "" {
		traceKey := s.traceKey(execution.TraceID)
		if err := s.provider.Set(ctx, traceKey, execution.RequestID, ttl); err != nil {
			if s.logger != nil {
				s.logger.Warn("Failed to store trace ID mapping", map[string]interface{}{
					"operation":  "execution_store_trace",
					"request_id": execution.RequestID,
					"trace_id":   execution.TraceID,
					"error":      err.Error(),
				})
			}
			// Continue - main record is stored
		}
	}

	return nil
}

// Get retrieves the complete execution record by request ID.
func (s *executionStoreImpl) Get(ctx context.Context, requestID string) (*StoredExecution, error) {
	if requestID == "" {
		return nil, fmt.Errorf("request_id is required")
	}

	key := s.recordKey(requestID)
	data, err := s.provider.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get execution: %w", err)
	}
	if data == "" {
		return nil, fmt.Errorf("execution not found: %s", requestID)
	}

	var execution StoredExecution
	if err := json.Unmarshal([]byte(data), &execution); err != nil {
		return nil, fmt.Errorf("failed to unmarshal execution: %w", err)
	}

	return &execution, nil
}

// GetByTraceID retrieves an execution by distributed trace ID.
func (s *executionStoreImpl) GetByTraceID(ctx context.Context, traceID string) (*StoredExecution, error) {
	if traceID == "" {
		return nil, fmt.Errorf("trace_id is required")
	}

	// Look up request ID from trace ID mapping
	traceKey := s.traceKey(traceID)
	requestID, err := s.provider.Get(ctx, traceKey)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup trace: %w", err)
	}
	if requestID == "" {
		return nil, fmt.Errorf("execution not found for trace: %s", traceID)
	}

	// Get the execution by request ID
	return s.Get(ctx, requestID)
}

// SetMetadata adds metadata to an existing record.
func (s *executionStoreImpl) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	if requestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if key == MetadataConversationID {
		return fmt.Errorf("%s is framework-owned and cannot be changed", MetadataConversationID)
	}

	// Get existing record
	execution, err := s.Get(ctx, requestID)
	if err != nil {
		return err
	}

	// Update metadata
	if execution.Metadata == nil {
		execution.Metadata = make(map[string]string)
	}
	execution.Metadata[key] = value

	// Re-serialize and store
	data, err := json.Marshal(execution)
	if err != nil {
		return fmt.Errorf("failed to marshal execution: %w", err)
	}

	// Use original TTL (we can't know the remaining TTL without Redis-specific commands).
	// ORCH-022: interrupted records have Result.Success == false after the
	// orchestrator fix — carve them out so pending HITL approvals keep the
	// default TTL rather than the shorter ErrorTTL.
	ttl := s.config.TTL
	if execution.Result != nil && !execution.Result.Success && !execution.Interrupted {
		ttl = s.config.ErrorTTL
	}

	storeKey := s.recordKey(requestID)
	return s.provider.Set(ctx, storeKey, string(data), ttl)
}

// ExtendTTL extends retention for investigation.
func (s *executionStoreImpl) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	// Get existing record
	execution, err := s.Get(ctx, requestID)
	if err != nil {
		return err
	}

	// Re-serialize and store with new TTL
	data, err := json.Marshal(execution)
	if err != nil {
		return fmt.Errorf("failed to marshal execution: %w", err)
	}

	storeKey := s.recordKey(requestID)
	if err := s.provider.Set(ctx, storeKey, string(data), duration); err != nil {
		return err
	}

	// Also extend trace ID mapping TTL if present
	if execution.TraceID != "" {
		traceKey := s.traceKey(execution.TraceID)
		if err := s.provider.Set(ctx, traceKey, requestID, duration); err != nil {
			if s.logger != nil {
				s.logger.Warn("Failed to extend trace ID mapping TTL", map[string]interface{}{
					"operation":  "execution_store_extend_ttl",
					"request_id": requestID,
					"trace_id":   execution.TraceID,
					"error":      err.Error(),
				})
			}
			// Continue - main record TTL is extended
		}
	}

	return nil
}

// ListRecent returns recent records for UI listing.
func (s *executionStoreImpl) ListRecent(ctx context.Context, limit int) ([]ExecutionSummary, error) {
	const maxLimit = 1000 // Prevent unbounded queries
	if limit <= 0 {
		limit = 50 // Default limit
	} else if limit > maxLimit {
		limit = maxLimit
	}

	// Get recent request IDs from sorted set (newest first)
	requestIDs, err := s.provider.ListByScoreDesc(ctx, s.indexKey(), "-inf", "+inf", 0, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("failed to list recent executions: %w", err)
	}

	summaries := make([]ExecutionSummary, 0, len(requestIDs))
	for _, requestID := range requestIDs {
		execution, err := s.Get(ctx, requestID)
		if err != nil {
			// Skip records that can't be loaded (may have expired)
			if s.logger != nil {
				s.logger.Warn("Failed to load execution for list", map[string]interface{}{
					"operation":  "execution_store_list",
					"request_id": requestID,
					"error":      err.Error(),
				})
			}
			// Clean up stale index entry
			_ = s.provider.RemoveFromIndex(ctx, s.indexKey(), requestID)
			continue
		}

		// Build summary
		summary := ExecutionSummary{
			RequestID:         execution.RequestID,
			OriginalRequestID: execution.OriginalRequestID,
			TraceID:           execution.TraceID,
			AgentName:         execution.AgentName,
			OriginalRequest:   execution.OriginalRequest,
			Interrupted:       execution.Interrupted,
			CreatedAt:         execution.CreatedAt,
			Metadata:          execution.Metadata,
		}

		if execution.Result != nil {
			summary.Success = execution.Result.Success
			summary.TotalDuration = execution.Result.TotalDuration
			summary.StepCount = len(execution.Result.Steps)
			for _, step := range execution.Result.Steps {
				if !step.Success {
					summary.FailedSteps++
				}
			}
		}

		// Multi-phase execution data (Step 16 F1)
		summary.PhaseCount = execution.PhaseCount
		summary.ForcedTerminal = execution.ForcedTerminal

		summaries = append(summaries, summary)
	}

	return summaries, nil
}

// Ensure executionStoreImpl implements ExecutionStore
var _ ExecutionStore = (*executionStoreImpl)(nil)
