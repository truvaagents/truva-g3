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
package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// ErrExecutionRecordNotFound identifies the expected absence of an execution
// debug record. Callers may use errors.Is to distinguish missing optional
// evidence from storage failures.
var ErrExecutionRecordNotFound = errors.New("execution record not found")

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

// ConversationExecutionLister is an optional execution-store capability for
// querying executions by a stable multi-turn conversation identifier. Results
// are the most recent bounded window, ordered chronologically within it.
type ConversationExecutionLister interface {
	ListByConversationID(
		ctx context.Context,
		conversationID string,
		limit int,
	) ([]ExecutionSummary, error)
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

	// Skills is the bounded, body-free lifecycle reconstruction record. Full
	// prompt content remains governed by the separate opt-in LLM debug store.
	Skills *SkillExecutionDebug `json:"skills,omitempty"`

	// FinalResponse is the terminal application response after AfterSynthesis
	// hooks have run. It is intentionally separate from the raw synthesis
	// interaction retained in the opt-in LLM debug store.
	FinalResponse       *string `json:"final_response,omitempty"`
	FinalResponseSource string  `json:"final_response_source,omitempty"`

	// Metadata contains framework-owned correlation metadata plus optional
	// application investigation metadata. MetadataConversationID is reserved
	// for the immutable conversation identity captured at execution time.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// FinalResponseSourceAfterSynthesisHooks identifies a terminal response after
// every registered AfterSynthesis hook has run. Keeping this as a named API
// value prevents producers and observability consumers from inventing subtly
// different spellings for the same response boundary.
const FinalResponseSourceAfterSynthesisHooks = "after_synthesis_hooks"

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

// IndexTTLManager is an optional provider capability. Implementations extend
// an existing sorted index's expiry to at least minTTL and must never shorten a
// longer current TTL. A missing index is a no-op.
type IndexTTLManager interface {
	ExtendIndexTTL(
		ctx context.Context,
		indexKey string,
		minTTL time.Duration,
	) error
}

// KeyTTLManager defines atomic retention maintenance for ordinary values.
type KeyTTLManager interface {
	// ExtendKeyTTL keeps an existing key for at least minTTL from now. It must
	// not create a missing key, shorten a longer TTL, or expire a persistent key.
	ExtendKeyTTL(ctx context.Context, key string, minTTL time.Duration) error

	// SetKeyWithMinimumTTL atomically writes value and keeps the key for at
	// least max(previous remaining TTL, minTTL). It creates a missing key with
	// minTTL and preserves persistent keys as persistent.
	SetKeyWithMinimumTTL(
		ctx context.Context,
		key string,
		value string,
		minTTL time.Duration,
	) error
}

// ExecutionStorageProvider is the complete provider contract required by the
// provider-backed execution store. Keeping the capabilities as small embedded
// interfaces preserves composition while making retention correctness a
// compile-time requirement.
type ExecutionStorageProvider interface {
	StorageProvider
	KeyTTLManager
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
	// Default: 24h. Override via TRUVAG3_EXECUTION_DEBUG_TTL.
	// This is passed to the StorageProvider implementation.
	TTL time.Duration `json:"ttl"`

	// ErrorTTL is the retention period for records with errors.
	// Default: 168h (7 days). Override via TRUVAG3_EXECUTION_DEBUG_ERROR_TTL.
	ErrorTTL time.Duration `json:"error_ttl"`

	// KeyPrefix is the prefix for all storage keys.
	// Default: "truvag3:execution:debug:".
	// Override via TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX.
	// This allows multi-tenant deployments or custom namespacing.
	// Per FRAMEWORK_DESIGN_PRINCIPLES.md: "Explicit Override: Always allow explicit configuration"
	KeyPrefix string `json:"key_prefix"`

	// ConversationQueryLimit bounds the most recent execution window returned
	// by one conversation lookup.
	// Default: 1000. Override via TRUVAG3_EXECUTION_DEBUG_CONVERSATION_QUERY_LIMIT.
	ConversationQueryLimit int `json:"conversation_query_limit"`

	// ConversationIndexScanLimit bounds stale-index scanning performed by one
	// conversation lookup.
	// Default: 5000. Override via TRUVAG3_EXECUTION_DEBUG_INDEX_SCAN_LIMIT.
	ConversationIndexScanLimit int `json:"conversation_index_scan_limit"`
}

// DefaultExecutionStoreConfig returns the default configuration.
// Feature is disabled by default per FRAMEWORK_DESIGN_PRINCIPLES.md.
func DefaultExecutionStoreConfig() ExecutionStoreConfig {
	return ExecutionStoreConfig{
		Enabled:                    false,                      // Disabled by default
		TTL:                        24 * time.Hour,             // 24 hours for success
		ErrorTTL:                   7 * 24 * time.Hour,         // 7 days for errors
		KeyPrefix:                  "truvag3:execution:debug:", // Default prefix with trailing colon
		ConversationQueryLimit:     defaultConversationQueryLimit,
		ConversationIndexScanLimit: defaultConversationIndexScanLimit,
	}
}

// Default key prefix constant (for documentation and backwards compatibility)
const (
	// DefaultExecutionKeyPrefix is the default prefix for execution debug storage keys
	DefaultExecutionKeyPrefix = "truvag3:execution:debug:"

	defaultConversationQueryLimit     = 1000
	defaultConversationIndexScanLimit = 5000
	defaultConversationPageSize       = 50
	conversationIndexReadBatchSize    = 100
)

// executionStoreImpl is the default implementation of ExecutionStore
// backed by a StorageProvider.
type executionStoreImpl struct {
	provider ExecutionStorageProvider
	config   ExecutionStoreConfig
	logger   core.Logger
}

// NewExecutionStoreWithProvider creates an ExecutionStore backed by the given
// ExecutionStorageProvider. The application provides a backend implementation
// (Redis, PostgreSQL, etc.) with atomic minimum-retention operations.
func NewExecutionStoreWithProvider(provider ExecutionStorageProvider, config ExecutionStoreConfig, logger core.Logger) ExecutionStore {
	config = normalizeExecutionStoreConfig(config)
	return &executionStoreImpl{
		provider: provider,
		config:   config,
		logger:   logger,
	}
}

func normalizeExecutionStoreConfig(config ExecutionStoreConfig) ExecutionStoreConfig {
	defaults := DefaultExecutionStoreConfig()
	if config.TTL <= 0 {
		config.TTL = defaults.TTL
	}
	if config.ErrorTTL <= 0 {
		config.ErrorTTL = defaults.ErrorTTL
	}
	config.KeyPrefix = normalizeExecutionKeyPrefix(config.KeyPrefix)
	if config.ConversationQueryLimit <= 0 {
		config.ConversationQueryLimit = defaultConversationQueryLimit
	}
	if config.ConversationIndexScanLimit <= 0 {
		config.ConversationIndexScanLimit = defaultConversationIndexScanLimit
	}
	return config
}

func normalizeExecutionKeyPrefix(prefix string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(prefix), ":")
	if trimmed == "" {
		trimmed = strings.TrimRight(DefaultExecutionKeyPrefix, ":")
	}
	return trimmed + ":"
}

func filterUnseenConversationMembers(
	members []string,
	seen map[string]struct{},
) []string {
	unseen := make([]string, 0, len(members))
	for _, member := range members {
		if _, alreadySeen := seen[member]; alreadySeen {
			continue
		}
		seen[member] = struct{}{}
		unseen = append(unseen, member)
	}
	return unseen
}

// Key building helper methods use a prefix normalized to exactly one trailing
// separator.

// recordKey returns the key for storing an execution record
func (s *executionStoreImpl) recordKey(requestID string) string {
	return s.config.KeyPrefix + requestID
}

// indexKey returns the key for the sorted index of recent executions
func (s *executionStoreImpl) indexKey() string {
	return s.config.KeyPrefix + "index"
}

// traceKey returns the key for trace ID → request ID mapping
func (s *executionStoreImpl) traceKey(traceID string) string {
	return s.config.KeyPrefix + "trace:" + traceID
}

func (s *executionStoreImpl) retentionLinkKey(requestID string) string {
	return executionRetentionLinkKey(s.config.KeyPrefix, requestID)
}

func (s *executionStoreImpl) conversationIndexKey(conversationID string) string {
	return executionConversationIndexKey(s.config.KeyPrefix, conversationID)
}

func executionConversationIndexKey(prefix, conversationID string) string {
	digest := sha256.Sum256([]byte(conversationID))
	return normalizeExecutionKeyPrefix(prefix) + fmt.Sprintf("conversation:%x", digest)
}

func executionRetentionLinkKey(prefix, requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return normalizeExecutionKeyPrefix(prefix) + fmt.Sprintf("retention:%x", digest)
}

// executionRetentionLink is the small, backend-neutral projection needed to
// preserve an execution's related evidence. Keeping it beside the full record
// avoids loading and decoding a potentially large debug payload merely to
// extend retention.
type executionRetentionLink struct {
	TraceID           string `json:"trace_id,omitempty"`
	ConversationID    string `json:"conversation_id,omitempty"`
	OriginalRequestID string `json:"original_request_id,omitempty"`
}

func executionRetentionLinkFromStored(execution *StoredExecution) executionRetentionLink {
	return executionRetentionLink{
		TraceID:           execution.TraceID,
		ConversationID:    ExecutionConversationID(execution),
		OriginalRequestID: relatedRootID(execution),
	}
}

func marshalExecutionRetentionLink(execution *StoredExecution) (string, error) {
	encoded, err := json.Marshal(executionRetentionLinkFromStored(execution))
	if err != nil {
		return "", fmt.Errorf("failed to marshal execution retention link: %w", err)
	}
	return string(encoded), nil
}

func unmarshalExecutionRetentionLink(encoded string) (executionRetentionLink, error) {
	var link executionRetentionLink
	if err := json.Unmarshal([]byte(encoded), &link); err != nil {
		return executionRetentionLink{}, fmt.Errorf("failed to unmarshal execution retention link: %w", err)
	}
	return link, nil
}

func maxDuration(left, right time.Duration) time.Duration {
	if left >= right {
		return left
	}
	return right
}

func executionRetentionTTL(
	execution *StoredExecution,
	normalTTL time.Duration,
	errorTTL time.Duration,
) time.Duration {
	return executionRetentionTTLAt(execution, normalTTL, errorTTL, time.Now())
}

func executionRetentionTTLAt(
	execution *StoredExecution,
	normalTTL time.Duration,
	errorTTL time.Duration,
	now time.Time,
) time.Duration {
	if execution == nil {
		return normalTTL
	}
	if execution.Interrupted {
		retention := maxDuration(normalTTL, errorTTL)
		if execution.Checkpoint != nil && execution.Checkpoint.ExpiresAt.After(now) {
			retention = maxDuration(retention, execution.Checkpoint.ExpiresAt.Sub(now))
		}
		return retention
	}
	if execution.Result != nil && !execution.Result.Success {
		return errorTTL
	}
	return normalTTL
}

func relatedRootID(execution *StoredExecution) string {
	if execution == nil {
		return ""
	}
	rootID := strings.TrimSpace(execution.OriginalRequestID)
	if rootID == "" || rootID == execution.RequestID {
		return ""
	}
	return rootID
}

func logLineageRetentionFailure(
	ctx context.Context,
	logger core.Logger,
	execution *StoredExecution,
	err error,
) {
	if logger == nil || execution == nil || err == nil ||
		errors.Is(err, ErrExecutionRecordNotFound) {
		return
	}
	logger.WarnWithContext(ctx, "Failed to preserve related execution root", map[string]interface{}{
		"operation":           "execution_store_lineage_retention",
		"request_id":          execution.RequestID,
		"original_request_id": relatedRootID(execution),
		"error_type":          "retention_extension",
		"error":               safeExecutionStoreError(err),
	})
}

func logExecutionRetentionLinkFailure(
	ctx context.Context,
	logger core.Logger,
	requestID string,
	err error,
) {
	if logger == nil || err == nil {
		return
	}
	logger.WarnWithContext(ctx, "Failed to store execution retention link", map[string]interface{}{
		"operation":  "execution_store_retention_link",
		"request_id": requestID,
		"error_type": "retention_link_write",
		"error":      safeExecutionStoreError(err),
	})
}

func sanitizeExecutionConversationMetadata(
	execution *StoredExecution,
) (*StoredExecution, string) {
	if execution == nil {
		return nil, ""
	}

	cloned := *execution
	if execution.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(execution.Metadata))
		for key, value := range execution.Metadata {
			cloned.Metadata[key] = value
		}
	}

	conversationID, present := cloned.Metadata[MetadataConversationID]
	if !present {
		return &cloned, ""
	}
	if core.ValidateConversationID(conversationID) != core.ConversationIDValidationNone {
		delete(cloned.Metadata, MetadataConversationID)
		return &cloned, ""
	}
	return &cloned, conversationID
}

func executionSummaryFromStored(execution *StoredExecution) ExecutionSummary {
	summary := ExecutionSummary{
		RequestID:         execution.RequestID,
		OriginalRequestID: execution.OriginalRequestID,
		TraceID:           execution.TraceID,
		AgentName:         execution.AgentName,
		OriginalRequest:   execution.OriginalRequest,
		Interrupted:       execution.Interrupted,
		CreatedAt:         execution.CreatedAt,
		PhaseCount:        execution.PhaseCount,
		ForcedTerminal:    execution.ForcedTerminal,
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
	return summary
}

func normalizeConversationQueryLimit(limit, configuredLimit int) int {
	if configuredLimit <= 0 {
		configuredLimit = defaultConversationQueryLimit
	}
	if limit <= 0 {
		if configuredLimit < defaultConversationPageSize {
			return configuredLimit
		}
		return defaultConversationPageSize
	}
	if limit > configuredLimit {
		return configuredLimit
	}
	return limit
}

func validateConversationQueryID(conversationID string) error {
	if reason := core.ValidateConversationID(conversationID); reason != core.ConversationIDValidationNone {
		return fmt.Errorf("invalid conversation_id: %s", reason)
	}
	return nil
}

// Store saves a complete execution record (plan + result).
func (s *executionStoreImpl) Store(ctx context.Context, execution *StoredExecution) error {
	if execution == nil {
		return fmt.Errorf("execution cannot be nil")
	}
	if execution.RequestID == "" {
		return fmt.Errorf("request_id is required")
	}
	storedExecution, conversationID := sanitizeExecutionConversationMetadata(execution)
	retentionLink, err := marshalExecutionRetentionLink(storedExecution)
	if err != nil {
		return err
	}

	// Serialize to JSON
	data, err := json.Marshal(storedExecution)
	if err != nil {
		return fmt.Errorf("failed to marshal execution: %w", err)
	}

	ttl := executionRetentionTTL(storedExecution, s.config.TTL, s.config.ErrorTTL)

	// Store the main record
	key := s.recordKey(storedExecution.RequestID)
	storeErr := s.provider.SetKeyWithMinimumTTL(ctx, key, string(data), ttl)
	if storeErr != nil {
		return fmt.Errorf("failed to store execution: %w", storeErr)
	}
	if err := s.provider.SetKeyWithMinimumTTL(
		ctx,
		s.retentionLinkKey(storedExecution.RequestID),
		retentionLink,
		ttl,
	); err != nil {
		// The primary execution is authoritative. Retention extension can recover
		// from a missing projection by reading that record, so do not report a
		// failed Store after the primary write has already succeeded.
		logExecutionRetentionLinkFailure(ctx, s.logger, storedExecution.RequestID, err)
	}

	// Add to index (sorted set by timestamp)
	score := float64(storedExecution.CreatedAt.UnixNano())
	if err := s.provider.AddToIndex(ctx, s.indexKey(), score, storedExecution.RequestID); err != nil {
		if s.logger != nil {
			s.logger.WarnWithContext(ctx, "Failed to add execution to index", map[string]interface{}{
				"operation":  "execution_store_index",
				"request_id": storedExecution.RequestID,
				"error_type": "index_write",
				"error":      safeExecutionStoreError(err),
			})
		}
		// Continue - main record is stored
	}

	if conversationID != "" {
		conversationKey := s.conversationIndexKey(conversationID)
		if err := s.provider.AddToIndex(
			ctx,
			conversationKey,
			score,
			storedExecution.RequestID,
		); err != nil {
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to update conversation execution index", map[string]interface{}{
					"operation":  "execution_store_conversation_index",
					"request_id": storedExecution.RequestID,
					"error_type": "index_write",
					"error":      safeExecutionStoreError(err),
				})
			}
		} else if ttlManager, ok := s.provider.(IndexTTLManager); ok {
			if err := ttlManager.ExtendIndexTTL(ctx, conversationKey, ttl); err != nil && s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to extend conversation index TTL", map[string]interface{}{
					"operation":  "execution_store_conversation_index_ttl",
					"request_id": storedExecution.RequestID,
					"error_type": "ttl_update",
					"error":      safeExecutionStoreError(err),
				})
			}
		}
	}

	// Store trace ID mapping if available
	if storedExecution.TraceID != "" {
		traceKey := s.traceKey(storedExecution.TraceID)
		traceErr := s.provider.SetKeyWithMinimumTTL(
			ctx,
			traceKey,
			storedExecution.RequestID,
			ttl,
		)
		if traceErr != nil {
			if s.logger != nil {
				s.logger.Warn("Failed to store trace ID mapping", map[string]interface{}{
					"operation":  "execution_store_trace",
					"request_id": storedExecution.RequestID,
					"trace_id":   storedExecution.TraceID,
					"error":      safeExecutionStoreError(traceErr),
				})
			}
			// Continue - main record is stored
		}
	}

	if rootID := relatedRootID(storedExecution); rootID != "" {
		if err := s.ExtendTTL(ctx, rootID, ttl); err != nil {
			logLineageRetentionFailure(ctx, s.logger, storedExecution, err)
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
		return nil, fmt.Errorf("%w: %s", ErrExecutionRecordNotFound, requestID)
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
		return nil, fmt.Errorf("%w for trace: %s", ErrExecutionRecordNotFound, traceID)
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

	ttl := executionRetentionTTL(execution, s.config.TTL, s.config.ErrorTTL)

	storeKey := s.recordKey(requestID)
	return s.provider.SetKeyWithMinimumTTL(ctx, storeKey, string(data), ttl)
}

// ExtendTTL extends retention for investigation.
func (s *executionStoreImpl) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	if requestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}

	return s.extendTTL(ctx, requestID, duration, make(map[string]struct{}), true)
}

func (s *executionStoreImpl) extendTTL(
	ctx context.Context,
	requestID string,
	duration time.Duration,
	visited map[string]struct{},
	required bool,
) error {
	if _, seen := visited[requestID]; seen {
		return nil
	}
	visited[requestID] = struct{}{}

	exists, err := s.provider.Exists(ctx, s.recordKey(requestID))
	if err != nil {
		return fmt.Errorf("failed to check execution retention target: %w", err)
	}
	if !exists {
		if !required {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrExecutionRecordNotFound, requestID)
	}

	linkKey := s.retentionLinkKey(requestID)
	link, err := s.loadExecutionRetentionLink(ctx, requestID, linkKey)
	if err != nil {
		if !required && errors.Is(err, ErrExecutionRecordNotFound) {
			return nil
		}
		return err
	}
	if err := s.provider.ExtendKeyTTL(ctx, s.recordKey(requestID), duration); err != nil {
		return err
	}
	if err := s.provider.ExtendKeyTTL(ctx, linkKey, duration); err != nil {
		return err
	}
	if link.TraceID != "" {
		if err := s.provider.ExtendKeyTTL(ctx, s.traceKey(link.TraceID), duration); err != nil && s.logger != nil {
			s.logger.Warn("Failed to extend trace ID mapping TTL", map[string]interface{}{
				"operation":  "execution_store_extend_ttl",
				"request_id": requestID,
				"trace_id":   link.TraceID,
				"error":      safeExecutionStoreError(err),
			})
		}
	}
	if link.ConversationID != "" {
		if ttlManager, ok := s.provider.(IndexTTLManager); ok {
			conversationKey := s.conversationIndexKey(link.ConversationID)
			if err := ttlManager.ExtendIndexTTL(ctx, conversationKey, duration); err != nil && s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to extend conversation index TTL", map[string]interface{}{
					"operation":  "execution_store_conversation_index_ttl",
					"request_id": requestID,
					"error_type": "ttl_update",
					"error":      safeExecutionStoreError(err),
				})
			}
		}
	}
	if rootID := strings.TrimSpace(link.OriginalRequestID); rootID != "" && rootID != requestID {
		return s.extendTTL(ctx, rootID, duration, visited, false)
	}
	return nil
}

func (s *executionStoreImpl) loadExecutionRetentionLink(
	ctx context.Context,
	requestID string,
	linkKey string,
) (executionRetentionLink, error) {
	encoded, err := s.provider.Get(ctx, linkKey)
	if err != nil {
		return executionRetentionLink{}, fmt.Errorf("failed to get execution retention link: %w", err)
	}
	if encoded != "" {
		return unmarshalExecutionRetentionLink(encoded)
	}

	// Compatibility fallback for records stored before retention links existed.
	execution, err := s.Get(ctx, requestID)
	if err != nil {
		return executionRetentionLink{}, err
	}
	return executionRetentionLinkFromStored(execution), nil
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

		summaries = append(summaries, executionSummaryFromStored(execution))
	}

	return summaries, nil
}

// ListByConversationID returns the most recent bounded window of executions for
// one conversation, ordered chronologically within that window.
func (s *executionStoreImpl) ListByConversationID(
	ctx context.Context,
	conversationID string,
	limit int,
) ([]ExecutionSummary, error) {
	if err := validateConversationQueryID(conversationID); err != nil {
		return nil, err
	}

	limit = normalizeConversationQueryLimit(limit, s.config.ConversationQueryLimit)
	scanLimit := s.config.ConversationIndexScanLimit
	indexKey := s.conversationIndexKey(conversationID)
	summariesNewestFirst := make([]ExecutionSummary, 0, limit)
	staleMembers := make([]string, 0)
	seenMembers := make(map[string]struct{})

	var offset int64
	for scanned := 0; scanned < scanLimit && len(summariesNewestFirst) < limit; {
		batchSize := conversationIndexReadBatchSize
		if remaining := scanLimit - scanned; remaining < batchSize {
			batchSize = remaining
		}
		requestIDs, err := s.provider.ListByScoreDesc(
			ctx,
			indexKey,
			"-inf",
			"+inf",
			offset,
			int64(batchSize),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to list conversation executions: %w", err)
		}
		if len(requestIDs) == 0 {
			break
		}
		batchCount := len(requestIDs)
		offset += int64(batchCount)
		scanned += batchCount
		requestIDs = filterUnseenConversationMembers(requestIDs, seenMembers)

		for _, requestID := range requestIDs {
			execution, err := s.Get(ctx, requestID)
			if err != nil {
				staleMembers = append(staleMembers, requestID)
				continue
			}
			if ExecutionConversationID(execution) != conversationID {
				staleMembers = append(staleMembers, requestID)
				continue
			}
			summariesNewestFirst = append(
				summariesNewestFirst,
				executionSummaryFromStored(execution),
			)
			if len(summariesNewestFirst) == limit {
				break
			}
		}
		if batchCount < batchSize {
			break
		}
	}

	if len(staleMembers) > 0 {
		if err := s.provider.RemoveFromIndex(ctx, indexKey, staleMembers...); err != nil && s.logger != nil {
			s.logger.WarnWithContext(ctx, "Failed to prune stale conversation index entries", map[string]interface{}{
				"operation":  "execution_store_conversation_index_cleanup",
				"request_id": staleMembers[0],
				"error_type": "index_write",
				"error":      safeExecutionStoreError(err),
			})
		}
	}

	for left, right := 0, len(summariesNewestFirst)-1; left < right; left, right = left+1, right-1 {
		summariesNewestFirst[left], summariesNewestFirst[right] =
			summariesNewestFirst[right], summariesNewestFirst[left]
	}
	return summariesNewestFirst, nil
}

// Ensure executionStoreImpl implements ExecutionStore
var (
	_ ExecutionStore              = (*executionStoreImpl)(nil)
	_ ConversationExecutionLister = (*executionStoreImpl)(nil)
)
