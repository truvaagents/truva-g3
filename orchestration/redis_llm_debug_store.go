package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

var (
	// Redis key patterns
	llmDebugKeyPrefix = defaultRedisKeyspace().Plain("llm-debug") + ":"
)

const (

	// Default TTLs
	defaultDebugTTL = 24 * time.Hour
	errorDebugTTL   = 7 * 24 * time.Hour
)

var recordLLMInteractionScript = redis.NewScript(`
local meta_ttl = redis.call("PTTL", KEYS[1])
local interaction_ttl = redis.call("PTTL", KEYS[2])
local floor_ttl = redis.call("PTTL", KEYS[3])
redis.call("RPUSH", KEYS[2], ARGV[1])
redis.call("HSETNX", KEYS[1], "created_at", ARGV[2])
redis.call("HSET", KEYS[1], "updated_at", ARGV[2])
redis.call("HSET", KEYS[1], "trace_id", ARGV[3])
redis.call("HSET", KEYS[1], "request_id", ARGV[4])
redis.call("HSET", KEYS[1], "original_request_id", ARGV[5])
if ARGV[6] ~= "" then
	redis.call("HSETNX", KEYS[1], ARGV[6], ARGV[7])
end
if ARGV[8] ~= "" then
	redis.call("HSETNX", KEYS[1], "source_component", ARGV[8])
end
if ARGV[9] ~= "" then
	redis.call("HSETNX", KEYS[1], "originating_agent", ARGV[9])
end
local requested = tonumber(ARGV[10])
if floor_ttl == -1 then
	requested = -1
elseif floor_ttl > requested then
	requested = floor_ttl
end
if requested == -1 then
	redis.call("PERSIST", KEYS[1])
	redis.call("PERSIST", KEYS[2])
else
	if meta_ttl == -2 or (meta_ttl >= 0 and meta_ttl < requested) then
		redis.call("PEXPIRE", KEYS[1], requested)
	end
	if interaction_ttl == -2 or (interaction_ttl >= 0 and interaction_ttl < requested) then
		redis.call("PEXPIRE", KEYS[2], requested)
	end
end
return 1
`)

var preserveLLMDebugRetentionScript = redis.NewScript(`
local requested = tonumber(ARGV[1])
local floor_ttl = redis.call("PTTL", KEYS[1])
if floor_ttl == -2 then
	redis.call("SET", KEYS[1], "1", "PX", requested)
elseif floor_ttl >= 0 and floor_ttl < requested then
	redis.call("PEXPIRE", KEYS[1], requested)
elseif floor_ttl > requested then
	requested = floor_ttl
end
for index = 2, #KEYS do
	local current = redis.call("PTTL", KEYS[index])
	if floor_ttl == -1 and current >= 0 then
		redis.call("PERSIST", KEYS[index])
	elseif current >= 0 and current < requested then
		redis.call("PEXPIRE", KEYS[index], requested)
	end
end
return floor_ttl
`)

// RedisLLMDebugStoreOption configures the Redis debug store
type RedisLLMDebugStoreOption func(*redisDebugStoreConfig)

type redisDebugStoreConfig struct {
	redisURL         string
	logger           core.Logger
	circuitBreaker   core.CircuitBreaker // Interface - injected by application (optional)
	ttl              time.Duration
	errorTTL         time.Duration
	keyPrefix        string
	keys             telemetry.RedisLLMDebugKeys
	keyspaceExplicit bool
}

// WithDebugRedisURL sets the Redis connection URL
func WithDebugRedisURL(url string) RedisLLMDebugStoreOption {
	return func(c *redisDebugStoreConfig) {
		c.redisURL = url
	}
}

// WithDebugLogger sets the logger for debug store operations
func WithDebugLogger(logger core.Logger) RedisLLMDebugStoreOption {
	return func(c *redisDebugStoreConfig) {
		c.logger = logger
	}
}

// WithDebugCircuitBreaker sets a circuit breaker for Redis operations.
// The circuit breaker must implement core.CircuitBreaker interface.
// If not provided, built-in Layer 1 resilience (simple retry with backoff) is used.
// This follows ARCHITECTURE.md: circuit breaker is injected by application, not created internally.
func WithDebugCircuitBreaker(cb core.CircuitBreaker) RedisLLMDebugStoreOption {
	return func(c *redisDebugStoreConfig) {
		c.circuitBreaker = cb
	}
}

// WithDebugTTL sets custom TTL for successful debug records
func WithDebugTTL(ttl time.Duration) RedisLLMDebugStoreOption {
	return func(c *redisDebugStoreConfig) {
		c.ttl = ttl
	}
}

// WithDebugErrorTTL sets custom TTL for error debug records
func WithDebugErrorTTL(ttl time.Duration) RedisLLMDebugStoreOption {
	return func(c *redisDebugStoreConfig) {
		c.errorTTL = ttl
	}
}

// WithDebugKeyspace selects the canonical versioned DB-0 key schema.
func WithDebugKeyspace(keyspace core.RedisKeyspace) RedisLLMDebugStoreOption {
	return func(c *redisDebugStoreConfig) {
		c.keys = telemetry.NewRedisLLMDebugKeys(keyspace)
		c.keyPrefix = keyspace.Plain("llm-debug")
		c.keyspaceExplicit = true
	}
}

// RedisLLMDebugStore is a Redis-backed implementation of LLMDebugStore.
// It provides persistent storage with TTL-based cleanup, compression for large payloads,
// and resilience protection.
//
// Resilience follows the Three-Layer Architecture from ARCHITECTURE.md:
// - Layer 1: Built-in simple retry with exponential backoff (always active)
// - Layer 2: Optional circuit breaker (injected via WithDebugCircuitBreaker)
// - Layer 3: Fallback to NoOp on persistent failures (handled by factory)
type RedisLLMDebugStore struct {
	client         redis.UniversalClient
	ownsClient     bool
	logger         core.Logger
	circuitBreaker core.CircuitBreaker // Optional - injected by application
	ttl            time.Duration
	errorTTL       time.Duration
	keyPrefix      string
	keys           telemetry.RedisLLMDebugKeys

	// Layer 1 resilience state (simple failure tracking)
	failureCount int
	failureMu    sync.Mutex
	lastFailure  time.Time
	closeOnce    sync.Once
	closeErr     error
}

// NewRedisLLMDebugStore creates an owning Redis-backed debug store. Connection
// topology is resolved by core.ResolveRedisConnectionConfig.
func NewRedisLLMDebugStore(opts ...RedisLLMDebugStoreOption) (*RedisLLMDebugStore, error) {
	// Apply intelligent defaults
	cfg := defaultRedisLLMDebugStoreConfig()

	// Apply explicit options (override defaults)
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	if !cfg.keyspaceExplicit {
		keyspace, err := redisKeyspaceFromEnvironment()
		if err != nil {
			return nil, fmt.Errorf("resolve Redis LLM debug keyspace: %w", err)
		}
		cfg.keys = telemetry.NewRedisLLMDebugKeys(keyspace)
		cfg.keyPrefix = keyspace.Plain("llm-debug")
	}
	normalizeRedisLLMDebugStoreConfig(cfg)

	client, connection, err := newOwnedRedisUniversalClient(cfg.redisURL, "TRUVAG3_LLM_DEBUG_REDIS_DB", "TRUVAG3_LLM_DEBUG_KEY_PREFIX")
	if err != nil {
		return nil, fmt.Errorf("initialize Redis LLM debug store: %w", err)
	}

	// Note: Circuit breaker is optional and injected by application (per ARCHITECTURE.md)
	// If not provided, built-in Layer 1 resilience (simple retry) is used

	if cfg.logger != nil {
		cfg.logger.Info("Redis LLM debug store initialized", map[string]interface{}{
			"operation":        "llm_debug_store_initialize",
			"redis_mode":       connection.Mode,
			"redis_seed_count": len(connection.Addrs),
			"redis_db":         connection.DB,
			"ttl":              cfg.ttl.String(),
			"error_ttl":        cfg.errorTTL.String(),
			"circuit_breaker":  cfg.circuitBreaker != nil,
			"resilience":       "layer1_builtin", // Always has Layer 1
		})
	}

	return newRedisLLMDebugStore(client, true, cfg), nil
}

// NewRedisLLMDebugStoreWithClient creates a store using an
// application-owned client. Close leaves the supplied client open.
func NewRedisLLMDebugStoreWithClient(client redis.UniversalClient, opts ...RedisLLMDebugStoreOption) (*RedisLLMDebugStore, error) {
	if client == nil {
		return nil, fmt.Errorf("redis LLM debug client is required")
	}
	cfg := &redisDebugStoreConfig{
		logger: &core.NoOpLogger{}, ttl: defaultDebugTTL,
		errorTTL: errorDebugTTL, keyPrefix: strings.TrimSuffix(llmDebugKeyPrefix, ":"),
		keys: telemetry.NewRedisLLMDebugKeys(defaultRedisKeyspace()),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	normalizeRedisLLMDebugStoreConfig(cfg)
	return newRedisLLMDebugStore(client, false, cfg), nil
}

func normalizeRedisLLMDebugStoreConfig(cfg *redisDebugStoreConfig) {
	cfg.logger = orchestrationComponentLogger(cfg.logger)
	if cfg.ttl <= 0 {
		cfg.ttl = defaultDebugTTL
	}
	if cfg.errorTTL <= 0 {
		cfg.errorTTL = errorDebugTTL
	}
}

func defaultRedisLLMDebugStoreConfig() *redisDebugStoreConfig {
	return &redisDebugStoreConfig{
		redisURL:  "",
		logger:    &core.NoOpLogger{},
		ttl:       getEnvDuration("TRUVAG3_LLM_DEBUG_TTL", defaultDebugTTL),
		errorTTL:  getEnvDuration("TRUVAG3_LLM_DEBUG_ERROR_TTL", errorDebugTTL),
		keyPrefix: llmDebugKeyPrefix,
		keys:      telemetry.NewRedisLLMDebugKeys(defaultRedisKeyspace()),
	}
}

func newRedisLLMDebugStore(client redis.UniversalClient, ownsClient bool, cfg *redisDebugStoreConfig) *RedisLLMDebugStore {
	return &RedisLLMDebugStore{
		client:         client,
		ownsClient:     ownsClient,
		logger:         cfg.logger,
		circuitBreaker: cfg.circuitBreaker,
		ttl:            cfg.ttl,
		errorTTL:       cfg.errorTTL,
		keyPrefix:      cfg.keyPrefix,
		keys:           cfg.keys,
	}
}

// RecordInteraction appends an LLM interaction to the debug record.
// A Redis script atomically appends the interaction, updates request metadata,
// and preserves any longer or persistent retention already applied. The global
// recent index is updated separately as a repairable projection.
// It is safe for concurrent writes from multiple processes (orchestrator + agents).
// Uses Layer 2 circuit breaker if injected, otherwise falls back to Layer 1 simple retry.
func (s *RedisLLMDebugStore) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	var indexScore int64
	operation := func() error {
		// Serialize the single interaction as JSON
		data, err := json.Marshal(interaction)
		if err != nil {
			return fmt.Errorf("serialization failed: %w", err)
		}

		// Extract trace context from baggage
		traceID := telemetry.GetTraceContext(ctx).TraceID
		originalRequestID := requestID
		conversationID := llmDebugConversationIDFromContext(ctx)
		// originatingAgent is the agent whose orchestrator (or background job) initiated
		// this request. The orchestrator stamps this into baggage as "agent_name" from
		// o.config.Name (orchestrator.go). HSetNX below ensures first writer wins, so when
		// an orchestrator-hosted agent dispatches to a downstream agent, the originator's
		// name lands first and the downstream worker's write no-ops — giving the LLM Debug
		// table a stable, semantically correct Source column.
		originatingAgent := ""
		if bag := telemetry.GetBaggage(ctx); bag != nil {
			if origID := bag["original_request_id"]; origID != "" {
				originalRequestID = origID
			}
			originatingAgent = bag["agent_name"]
		}

		now := time.Now()
		indexScore = now.Unix()
		ttl := s.ttl
		if !interaction.Success {
			ttl = s.errorTTL
		}

		conversationMetadataKey := ""
		if conversationID != "" {
			conversationMetadataKey = "meta:" + MetadataConversationID
		}
		ttlMilliseconds, err := positiveTTLMilliseconds(ttl)
		if err != nil {
			return err
		}
		if err := recordLLMInteractionScript.Run(
			ctx,
			s.client,
			[]string{
				s.metaKey(requestID),
				s.interactionsKey(requestID),
				s.retentionFloorKey(requestID),
			},
			data,
			strconv.FormatInt(now.Unix(), 10),
			traceID,
			requestID,
			originalRequestID,
			conversationMetadataKey,
			conversationID,
			interaction.SourceComponent,
			originatingAgent,
			strconv.FormatInt(ttlMilliseconds, 10),
		).Err(); err != nil {
			return fmt.Errorf("write authoritative LLM debug record: %w", err)
		}

		return nil
	}

	// Layer 2: Use injected circuit breaker if available
	var err error
	if s.circuitBreaker != nil {
		err = s.circuitBreaker.Execute(ctx, operation)
	} else {
		err = s.executeWithRetry(ctx, operation)
	}
	if err != nil {
		return err
	}
	indexStartedAt := time.Now()
	if err := retryLLMDebugIndexOnly(ctx, func() error {
		return s.client.ZAdd(ctx, s.indexKey(), redis.Z{
			Score:  float64(indexScore),
			Member: requestID,
		}).Err()
	}); err != nil && s.logger != nil {
		s.logger.WarnWithContext(ctx, "Failed to update LLM debug recent index", map[string]interface{}{
			"operation":     "llm_debug_recent_index",
			"request_id":    requestID,
			"error":         "redis LLM debug index update failed",
			"error_type":    "index_write",
			"failure_class": classifyRedisDiagnostic(err),
			"duration_ms":   time.Since(indexStartedAt).Milliseconds(),
		})
	}
	return nil
}

// GetRecord retrieves the complete debug record for a request.
func (s *RedisLLMDebugStore) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	metaKey := s.metaKey(requestID)
	interKey := s.interactionsKey(requestID)

	// Check if this is the new list-based format
	keyType, err := s.client.Type(ctx, metaKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis type check failed: %w", err)
	}

	if keyType == "none" {
		return nil, fmt.Errorf("%w: %s", ErrLLMDebugRecordNotFound, requestID)
	}
	if keyType != "hash" {
		return nil, fmt.Errorf("LLM debug metadata has unsupported Redis type %q", keyType)
	}
	return s.getRecordFromList(ctx, requestID, metaKey, interKey)
}

// getRecordFromList reads the new list-based format.
func (s *RedisLLMDebugStore) getRecordFromList(ctx context.Context, requestID, metaKey, interKey string) (*LLMDebugRecord, error) {
	// Get metadata hash
	meta, err := s.client.HGetAll(ctx, metaKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis hgetall failed: %w", err)
	}
	if len(meta) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrLLMDebugRecordNotFound, requestID)
	}

	// Get all interactions
	interData, err := s.client.LRange(ctx, interKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("redis lrange failed: %w", err)
	}

	// Build record from metadata
	record := &LLMDebugRecord{
		RequestID:         meta["request_id"],
		OriginalRequestID: meta["original_request_id"],
		TraceID:           meta["trace_id"],
		OriginatingAgent:  meta["originating_agent"],
		Interactions:      make([]LLMInteraction, 0, len(interData)),
		Metadata:          make(map[string]string),
	}

	// Parse timestamps
	if ts, err := strconv.ParseInt(meta["created_at"], 10, 64); err == nil {
		record.CreatedAt = time.Unix(ts, 0)
	}
	if ts, err := strconv.ParseInt(meta["updated_at"], 10, 64); err == nil {
		record.UpdatedAt = time.Unix(ts, 0)
	}

	// Parse metadata fields (any key starting with "meta:")
	for k, v := range meta {
		if strings.HasPrefix(k, "meta:") {
			record.Metadata[strings.TrimPrefix(k, "meta:")] = v
		}
	}

	// Deserialize each interaction
	for _, raw := range interData {
		var interaction LLMInteraction
		if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to deserialize interaction, skipping", map[string]interface{}{
					"operation":  "llm_debug_interaction_decode",
					"request_id": requestID,
					"error":      "stored LLM interaction is malformed",
					"error_type": "unmarshal",
				})
			}
			continue
		}
		record.Interactions = append(record.Interactions, interaction)
	}

	return record, nil
}

// SetMetadata adds metadata to an existing record.
// Uses Layer 2 circuit breaker if injected, otherwise falls back to Layer 1 simple retry.
func (s *RedisLLMDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	if key == MetadataConversationID {
		return fmt.Errorf("%s is framework-owned and cannot be changed", MetadataConversationID)
	}
	operation := func() error {
		metaKey := s.metaKey(requestID)

		keyType, err := s.client.Type(ctx, metaKey).Result()
		if err != nil {
			return fmt.Errorf("redis type check failed: %w", err)
		}
		if keyType == "none" {
			return fmt.Errorf("%w: %s", ErrLLMDebugRecordNotFound, requestID)
		}
		if keyType != "hash" {
			return fmt.Errorf("LLM debug metadata has unsupported Redis type %q", keyType)
		}
		return s.client.HSet(ctx, metaKey, "meta:"+key, value).Err()
	}

	if s.circuitBreaker != nil {
		return s.circuitBreaker.Execute(ctx, operation)
	}
	return s.executeWithRetry(ctx, operation)
}

// ExtendTTL extends retention for investigation.
func (s *RedisLLMDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	if requestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	found, err := extendRedisKeysMinimumTTL(ctx, s.client, []string{
		s.metaKey(requestID),
		s.interactionsKey(requestID),
	}, duration)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrLLMDebugRecordNotFound, requestID)
	}
	return nil
}

// PreserveRetention establishes a request-level retention floor and applies it
// to any existing debug keys in one Redis operation. The floor is intentionally
// separate from the debug record, so late in-process or cross-process writers
// inherit the final execution retention without creating an empty UI record.
func (s *RedisLLMDebugStore) PreserveRetention(
	ctx context.Context,
	requestID string,
	duration time.Duration,
) error {
	if requestID == "" {
		return fmt.Errorf("request_id is required")
	}
	milliseconds, err := positiveTTLMilliseconds(duration)
	if err != nil {
		return err
	}
	operation := func() error {
		return preserveLLMDebugRetentionScript.Run(
			ctx,
			s.client,
			[]string{
				s.retentionFloorKey(requestID),
				s.metaKey(requestID),
				s.interactionsKey(requestID),
			},
			strconv.FormatInt(milliseconds, 10),
		).Err()
	}
	if s.circuitBreaker != nil {
		return s.circuitBreaker.Execute(ctx, operation)
	}
	return s.executeWithRetry(ctx, operation)
}

// ListRecent returns recent records ordered by creation time.
// Includes lazy pruning of orphaned index entries (records expired via TTL
// but their sorted set entries remain).
func (s *RedisLLMDebugStore) ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error) {
	if s.client == nil {
		return nil, fmt.Errorf("redis client not initialized")
	}

	// Overfetch to account for orphaned index entries (expired records)
	fetchLimit := int64(limit * 2)
	if fetchLimit < 20 {
		fetchLimit = 20
	}

	// Keep the legacy command for Redis-compatible providers without ZRANGE REV.
	//nolint:staticcheck // ZRevRangeByScore remains supported by go-redis/v9.
	ids, err := s.client.ZRevRangeByScore(ctx, s.indexKey(), &redis.ZRangeBy{
		Min:   "-inf",
		Max:   "+inf",
		Count: fetchLimit,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list recent: %w", err)
	}

	var summaries []LLMDebugRecordSummary
	var orphanedIDs []interface{} // IDs whose records have expired

	for _, id := range ids {
		if len(summaries) >= limit {
			break
		}
		record, err := s.GetRecord(ctx, id)
		if errors.Is(err, ErrLLMDebugRecordNotFound) {
			// Only confirmed expired records are safe to prune from the index.
			orphanedIDs = append(orphanedIDs, id)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load recent LLM debug record %q: %w", id, err)
		}

		// Build the lightweight summary from the deduplicated view so paired
		// instrumented and typed rows contribute only once to list-view totals.
		// SourceComponents is derived from the ORIGINAL slice — typed rows
		// always carry an empty SourceComponent by invariant, so the only
		// rows that contribute here are agent_llm_call partners and orphans;
		// deduping would discard the wrapping-agent attribution that makes
		// the list useful. Totals use the deduped slice.
		deduped := DedupeLLMInteractions(record.Interactions)
		totalTokens := 0
		hasErrors := false
		for _, interaction := range deduped {
			totalTokens += interaction.TotalTokens
			if !interaction.Success {
				hasErrors = true
			}
		}
		sourceSet := make(map[string]struct{})
		for _, interaction := range record.Interactions {
			if interaction.SourceComponent != "" {
				sourceSet[interaction.SourceComponent] = struct{}{}
			}
		}
		var sourceComponents []string
		for src := range sourceSet {
			sourceComponents = append(sourceComponents, src)
		}
		sort.Strings(sourceComponents)

		summaries = append(summaries, LLMDebugRecordSummary{
			RequestID:         record.RequestID,
			OriginalRequestID: record.OriginalRequestID,
			TraceID:           record.TraceID,
			CreatedAt:         record.CreatedAt,
			InteractionCount:  len(deduped),
			TotalTokens:       totalTokens,
			HasErrors:         hasErrors,
			SourceComponents:  sourceComponents,
			OriginatingAgent:  record.OriginatingAgent,
		})
	}

	// Lazy prune: remove orphaned index entries in background.
	// This is a maintenance task not tied to any user request — no trace context to propagate.
	if len(orphanedIDs) > 0 {
		go func() {
			startedAt := time.Now()
			pruneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			removed, err := s.client.ZRem(pruneCtx, s.indexKey(), orphanedIDs...).Result()
			if err != nil {
				if s.logger != nil {
					s.logger.Warn("Failed to prune orphaned index entries", map[string]interface{}{
						"operation":      "llm_debug_orphan_cleanup",
						"error":          "redis LLM debug index cleanup failed",
						"error_type":     "index_write",
						"orphaned_count": len(orphanedIDs),
						"duration_ms":    time.Since(startedAt).Milliseconds(),
					})
				}
			} else if removed > 0 {
				if s.logger != nil {
					s.logger.Info("Pruned orphaned index entries from sorted set", map[string]interface{}{
						"operation":   "llm_debug_orphan_cleanup",
						"status":      "success",
						"removed":     removed,
						"duration_ms": time.Since(startedAt).Milliseconds(),
					})
				}
			}
		}()
	}

	return summaries, nil
}

// Close closes the Redis connection.
func (s *RedisLLMDebugStore) Close() error {
	if !s.ownsClient {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.client.Close() })
	return s.closeErr
}

func (s *RedisLLMDebugStore) indexKey() string {
	return s.keys.RecentIndex()
}

func (s *RedisLLMDebugStore) metaKey(requestID string) string {
	return s.keys.Meta(requestID)
}

func (s *RedisLLMDebugStore) interactionsKey(requestID string) string {
	return s.keys.Interactions(requestID)
}

func (s *RedisLLMDebugStore) retentionFloorKey(requestID string) string {
	return s.keys.RetentionFloor(requestID)
}

func retryLLMDebugIndexOnly(ctx context.Context, operation func() error) error {
	var lastErr error
	backoff := layer1InitialBackoff
	for attempt := 1; attempt <= layer1MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := operation(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < layer1MaxRetries {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			backoff *= 2
			if backoff > layer1MaxBackoff {
				backoff = layer1MaxBackoff
			}
		}
	}
	return lastErr
}

func classifyRedisDiagnostic(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return "timeout"
		}
		return "redis_backend_failure"
	}
}

var _ LLMDebugRetentionPreserver = (*RedisLLMDebugStore)(nil)

// Layer 1 Resilience Constants
const (
	layer1MaxRetries     = 3
	layer1InitialBackoff = 100 * time.Millisecond
	layer1MaxBackoff     = 2 * time.Second
	layer1FailureWindow  = 30 * time.Second
	layer1MaxFailures    = 5
)

// executeWithRetry implements Layer 1 built-in resilience with simple retry and exponential backoff.
// This is always available, even without an injected circuit breaker.
// Per ARCHITECTURE.md Layer 1: "3 retries with exponential backoff, simple failure tracking"
func (s *RedisLLMDebugStore) executeWithRetry(ctx context.Context, operation func() error) error {
	// Check if we're in cooldown due to too many failures
	s.failureMu.Lock()
	if s.failureCount >= layer1MaxFailures && time.Since(s.lastFailure) < layer1FailureWindow {
		s.failureMu.Unlock()
		if s.logger != nil {
			s.logger.WarnWithContext(ctx, "Layer 1 resilience: in cooldown period", map[string]interface{}{
				"operation":    "llm_debug_store_retry",
				"request_id":   core.GetRequestID(ctx),
				"status":       "cooldown",
				"failures":     s.failureCount,
				"cooldown_sec": layer1FailureWindow.Seconds(),
			})
		}
		return fmt.Errorf("debug store in cooldown after %d failures", s.failureCount)
	}
	s.failureMu.Unlock()

	var lastErr error
	backoff := layer1InitialBackoff

	for attempt := 1; attempt <= layer1MaxRetries; attempt++ {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := operation()
		if err == nil {
			// Success - reset failure count
			s.failureMu.Lock()
			s.failureCount = 0
			s.failureMu.Unlock()
			return nil
		}

		lastErr = err
		if s.logger != nil {
			s.logger.WarnWithContext(ctx, "Layer 1 resilience: operation failed, retrying", map[string]interface{}{
				"operation":     "llm_debug_store_retry",
				"request_id":    core.GetRequestID(ctx),
				"attempt":       attempt,
				"max":           layer1MaxRetries,
				"backoff":       backoff.String(),
				"error":         "redis LLM debug operation failed",
				"error_type":    "backend",
				"failure_class": classifyRedisDiagnostic(err),
			})
		}

		// Don't sleep on last attempt
		if attempt < layer1MaxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}

			// Exponential backoff with cap
			backoff *= 2
			if backoff > layer1MaxBackoff {
				backoff = layer1MaxBackoff
			}
		}
	}

	// All retries failed - track failure
	s.failureMu.Lock()
	s.failureCount++
	s.lastFailure = time.Now()
	s.failureMu.Unlock()

	return fmt.Errorf("operation failed after %d attempts: %w", layer1MaxRetries, lastErr)
}

// getEnvDuration parses a duration from environment variable with fallback
func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if result, err := time.ParseDuration(val); err == nil {
			return result
		}
	}
	return defaultVal
}

// Ensure RedisLLMDebugStore implements LLMDebugStore
var _ LLMDebugStore = (*RedisLLMDebugStore)(nil)
