package orchestration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	// Redis key patterns
	llmDebugKeyPrefix   = "truvag3:llm:debug:"
	llmDebugIndexKey    = "truvag3:llm:debug:index"
	llmDebugMetaSuffix  = ":meta"
	llmDebugInterSuffix = ":interactions"

	// Size thresholds for compression
	compressionThreshold = 100 * 1024  // 100KB
	maxPayloadSize       = 1024 * 1024 // 1MB

	// Default TTLs
	defaultDebugTTL = 24 * time.Hour
	errorDebugTTL   = 7 * 24 * time.Hour
)

// RedisLLMDebugStoreOption configures the Redis debug store
type RedisLLMDebugStoreOption func(*redisDebugStoreConfig)

type redisDebugStoreConfig struct {
	redisURL       string
	redisDB        int
	logger         core.Logger
	circuitBreaker core.CircuitBreaker // Interface - injected by application (optional)
	ttl            time.Duration
	errorTTL       time.Duration
}

// WithDebugRedisURL sets the Redis connection URL
func WithDebugRedisURL(url string) RedisLLMDebugStoreOption {
	return func(c *redisDebugStoreConfig) {
		c.redisURL = url
	}
}

// WithDebugRedisDB sets the Redis database number (default: 7)
func WithDebugRedisDB(db int) RedisLLMDebugStoreOption {
	return func(c *redisDebugStoreConfig) {
		c.redisDB = db
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

// RedisLLMDebugStore is a Redis-backed implementation of LLMDebugStore.
// It provides persistent storage with TTL-based cleanup, compression for large payloads,
// and resilience protection.
//
// Resilience follows the Three-Layer Architecture from ARCHITECTURE.md:
// - Layer 1: Built-in simple retry with exponential backoff (always active)
// - Layer 2: Optional circuit breaker (injected via WithDebugCircuitBreaker)
// - Layer 3: Fallback to NoOp on persistent failures (handled by factory)
type RedisLLMDebugStore struct {
	client         *redis.Client
	logger         core.Logger
	circuitBreaker core.CircuitBreaker // Optional - injected by application
	ttl            time.Duration
	errorTTL       time.Duration

	// Layer 1 resilience state (simple failure tracking)
	failureCount int
	failureMu    sync.Mutex
	lastFailure  time.Time
}

// NewRedisLLMDebugStore creates a Redis-backed debug store with intelligent defaults.
// Environment variable precedence: explicit options > REDIS_URL > TRUVAG3_REDIS_URL > localhost:6379
func NewRedisLLMDebugStore(opts ...RedisLLMDebugStoreOption) (*RedisLLMDebugStore, error) {
	// Apply intelligent defaults
	cfg := &redisDebugStoreConfig{
		redisURL: getRedisURLWithFallback(),
		redisDB:  getEnvInt("TRUVAG3_LLM_DEBUG_REDIS_DB", core.RedisDBLLMDebug),
		logger:   &core.NoOpLogger{},
		ttl:      getEnvDuration("TRUVAG3_LLM_DEBUG_TTL", defaultDebugTTL),
		errorTTL: getEnvDuration("TRUVAG3_LLM_DEBUG_ERROR_TTL", errorDebugTTL),
	}

	// Apply explicit options (override defaults)
	for _, opt := range opts {
		opt(cfg)
	}

	// Parse Redis URL and create client
	redisOpt, err := redis.ParseURL(cfg.redisURL)
	if err != nil {
		// Try treating it as a simple address if URL parsing fails
		redisOpt = &redis.Options{
			Addr: cfg.redisURL,
		}
	}
	redisOpt.DB = cfg.redisDB

	client := redis.NewClient(redisOpt)

	// Verify connection with actionable error message
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed at %s (DB %d): %w\n"+
			"Hint: Check REDIS_URL or TRUVAG3_REDIS_URL environment variables, "+
			"or use WithDebugRedisURL() option", cfg.redisURL, cfg.redisDB, err)
	}

	// Note: Circuit breaker is optional and injected by application (per ARCHITECTURE.md)
	// If not provided, built-in Layer 1 resilience (simple retry) is used

	cfg.logger.Info("Redis LLM debug store initialized", map[string]interface{}{
		"redis_addr":      redisOpt.Addr,
		"redis_db":        cfg.redisDB,
		"ttl":             cfg.ttl.String(),
		"error_ttl":       cfg.errorTTL.String(),
		"circuit_breaker": cfg.circuitBreaker != nil,
		"resilience":      "layer1_builtin", // Always has Layer 1
	})

	return &RedisLLMDebugStore{
		client:         client,
		logger:         cfg.logger,
		circuitBreaker: cfg.circuitBreaker,
		ttl:            cfg.ttl,
		errorTTL:       cfg.errorTTL,
	}, nil
}

// RecordInteraction appends an LLM interaction to the debug record.
// Uses list-based atomic storage (RPUSH) — safe for concurrent writes from
// multiple processes (orchestrator + agents).
// Uses Layer 2 circuit breaker if injected, otherwise falls back to Layer 1 simple retry.
func (s *RedisLLMDebugStore) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	operation := func() error {
		metaKey := llmDebugKeyPrefix + requestID + llmDebugMetaSuffix
		interKey := llmDebugKeyPrefix + requestID + llmDebugInterSuffix

		// Serialize the single interaction as JSON
		data, err := json.Marshal(interaction)
		if err != nil {
			return fmt.Errorf("serialization failed: %w", err)
		}

		// Extract trace context from baggage
		traceID := telemetry.GetTraceContext(ctx).TraceID
		originalRequestID := requestID
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
		ttl := s.ttl
		if !interaction.Success {
			ttl = s.errorTTL
		}

		// Prevent TTL downgrade: if the key already has a longer TTL (e.g., from
		// a prior error interaction with errorTTL=7d), keep the longer one.
		if existingTTL, err := s.client.TTL(ctx, metaKey).Result(); err == nil && existingTTL > ttl {
			ttl = existingTTL
		}

		// Atomic pipeline — no read-modify-write, no race condition
		pipe := s.client.Pipeline()
		pipe.RPush(ctx, interKey, data)                                            // Append interaction
		pipe.HSetNX(ctx, metaKey, "created_at", strconv.FormatInt(now.Unix(), 10)) // Set only if new
		pipe.HSet(ctx, metaKey, "updated_at", strconv.FormatInt(now.Unix(), 10))   // Always update
		pipe.HSet(ctx, metaKey, "trace_id", traceID)
		pipe.HSet(ctx, metaKey, "request_id", requestID)
		pipe.HSet(ctx, metaKey, "original_request_id", originalRequestID)
		if interaction.SourceComponent != "" {
			pipe.HSetNX(ctx, metaKey, "source_component", interaction.SourceComponent)
		}
		if originatingAgent != "" {
			pipe.HSetNX(ctx, metaKey, "originating_agent", originatingAgent)
		}
		pipe.Expire(ctx, metaKey, ttl)
		pipe.Expire(ctx, interKey, ttl)
		pipe.ZAdd(ctx, llmDebugIndexKey, &redis.Z{
			Score:  float64(now.Unix()),
			Member: requestID,
		})
		_, err = pipe.Exec(ctx)
		if err != nil {
			return fmt.Errorf("redis pipeline failed: %w", err)
		}

		return nil
	}

	// Layer 2: Use injected circuit breaker if available
	if s.circuitBreaker != nil {
		return s.circuitBreaker.Execute(ctx, operation)
	}

	// Layer 1: Built-in simple retry with exponential backoff
	return s.executeWithRetry(ctx, operation)
}

// GetRecord retrieves the complete debug record for a request.
// Supports both new list-based format (Phase 2) and old string-based format (backward compat).
func (s *RedisLLMDebugStore) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	metaKey := llmDebugKeyPrefix + requestID + llmDebugMetaSuffix
	interKey := llmDebugKeyPrefix + requestID + llmDebugInterSuffix

	// Check if this is the new list-based format
	keyType, err := s.client.Type(ctx, metaKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis type check failed: %w", err)
	}

	if keyType == "hash" {
		// New list-based format
		return s.getRecordFromList(ctx, requestID, metaKey, interKey)
	}

	// Backward compatibility: old string-based format (pre-migration)
	oldKey := llmDebugKeyPrefix + requestID
	return s.getRecordFromString(ctx, requestID, oldKey)
}

// getRecordFromList reads the new list-based format.
func (s *RedisLLMDebugStore) getRecordFromList(ctx context.Context, requestID, metaKey, interKey string) (*LLMDebugRecord, error) {
	// Get metadata hash
	meta, err := s.client.HGetAll(ctx, metaKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis hgetall failed: %w", err)
	}
	if len(meta) == 0 {
		return nil, fmt.Errorf("record not found: %s", requestID)
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
			s.logger.Warn("Failed to deserialize interaction, skipping", map[string]interface{}{
				"request_id": requestID,
				"error":      err.Error(),
			})
			continue
		}
		record.Interactions = append(record.Interactions, interaction)
	}

	return record, nil
}

// getRecordFromString reads the old string-based format (backward compatibility).
// This is the original format: single string key with compression flag + JSON.
func (s *RedisLLMDebugStore) getRecordFromString(ctx context.Context, requestID, key string) (*LLMDebugRecord, error) {
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("record not found: %s", requestID)
	}
	if err != nil {
		return nil, fmt.Errorf("redis get failed: %w", err)
	}
	return s.deserialize(data)
}

// SetMetadata adds metadata to an existing record.
// Uses Layer 2 circuit breaker if injected, otherwise falls back to Layer 1 simple retry.
func (s *RedisLLMDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	operation := func() error {
		metaKey := llmDebugKeyPrefix + requestID + llmDebugMetaSuffix

		// Check format
		keyType, err := s.client.Type(ctx, metaKey).Result()
		if err != nil {
			return fmt.Errorf("redis type check failed: %w", err)
		}

		if keyType == "hash" {
			// New format: store metadata directly in hash with "meta:" prefix
			return s.client.HSet(ctx, metaKey, "meta:"+key, value).Err()
		}

		// Old format: read-modify-write (single-writer safe, no migration needed)
		oldKey := llmDebugKeyPrefix + requestID
		record, err := s.getRecordFromString(ctx, requestID, oldKey)
		if err != nil {
			return err
		}
		if record.Metadata == nil {
			record.Metadata = make(map[string]string)
		}
		record.Metadata[key] = value
		record.UpdatedAt = time.Now()
		data, err := s.serialize(record)
		if err != nil {
			return err
		}
		ttl, err := s.client.TTL(ctx, oldKey).Result()
		if err != nil || ttl < 0 {
			ttl = s.ttl
		}
		return s.client.Set(ctx, oldKey, data, ttl).Err()
	}

	if s.circuitBreaker != nil {
		return s.circuitBreaker.Execute(ctx, operation)
	}
	return s.executeWithRetry(ctx, operation)
}

// ExtendTTL extends retention for investigation.
func (s *RedisLLMDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	metaKey := llmDebugKeyPrefix + requestID + llmDebugMetaSuffix
	interKey := llmDebugKeyPrefix + requestID + llmDebugInterSuffix

	// Try new format first
	pipe := s.client.Pipeline()
	pipe.Expire(ctx, metaKey, duration)
	pipe.Expire(ctx, interKey, duration)
	// Also try old format key (backward compat)
	pipe.Expire(ctx, llmDebugKeyPrefix+requestID, duration)
	_, err := pipe.Exec(ctx)
	return err
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

	ids, err := s.client.ZRevRangeByScore(ctx, llmDebugIndexKey, &redis.ZRangeBy{
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
		if err != nil {
			// Record expired but index entry remains — mark for cleanup
			orphanedIDs = append(orphanedIDs, id)
			continue
		}

		// Build lightweight summary from the deduped view so historical
		// records (written before Layer 2 landed) and any still-unmigrated
		// call sites produce correct totals in the list view.
		// SourceComponents is derived from the ORIGINAL slice — typed rows
		// always carry an empty SourceComponent by invariant, so the only
		// rows that contribute here are agent_llm_call partners and orphans;
		// deduping would discard the wrapping-agent attribution that makes
		// the list useful. Totals use the deduped slice.
		// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
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
			pruneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			removed, err := s.client.ZRem(pruneCtx, llmDebugIndexKey, orphanedIDs...).Result()
			if err != nil {
				s.logger.Warn("Failed to prune orphaned index entries", map[string]interface{}{
					"orphaned_count": len(orphanedIDs),
					"error":          err.Error(),
				})
			} else if removed > 0 {
				s.logger.Info("Pruned orphaned index entries from sorted set", map[string]interface{}{
					"removed": removed,
				})
			}
		}()
	}

	return summaries, nil
}

// Close closes the Redis connection.
func (s *RedisLLMDebugStore) Close() error {
	return s.client.Close()
}

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
		s.logger.Warn("Layer 1 resilience: in cooldown period", map[string]interface{}{
			"failures":     s.failureCount,
			"cooldown_sec": layer1FailureWindow.Seconds(),
		})
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
		s.logger.Warn("Layer 1 resilience: operation failed, retrying", map[string]interface{}{
			"attempt": attempt,
			"max":     layer1MaxRetries,
			"backoff": backoff.String(),
			"error":   err.Error(),
		})

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

// serialize with optional gzip compression
func (s *RedisLLMDebugStore) serialize(record *LLMDebugRecord) ([]byte, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}

	// Compress if over threshold
	if len(data) > compressionThreshold {
		var buf bytes.Buffer
		buf.WriteByte(1) // Compression flag
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(data); err != nil {
			return nil, err
		}
		if err := gz.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	// Prepend 0 byte to indicate no compression
	return append([]byte{0}, data...), nil
}

// deserialize with optional gzip decompression
func (s *RedisLLMDebugStore) deserialize(data []byte) (*LLMDebugRecord, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	var jsonData []byte
	if data[0] == 1 { // Compressed
		gz, err := gzip.NewReader(bytes.NewReader(data[1:]))
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }() // Error intentionally ignored for reader

		var buf bytes.Buffer
		if _, err := buf.ReadFrom(gz); err != nil {
			return nil, err
		}
		jsonData = buf.Bytes()
	} else {
		jsonData = data[1:]
	}

	var record LLMDebugRecord
	if err := json.Unmarshal(jsonData, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// Helper functions for environment variable parsing

// getRedisURLWithFallback returns Redis URL with environment variable precedence
func getRedisURLWithFallback() string {
	if url := os.Getenv("REDIS_URL"); url != "" {
		return url
	}
	if url := os.Getenv("TRUVAG3_REDIS_URL"); url != "" {
		return url
	}
	return "localhost:6379"
}

// getEnvInt parses an integer from environment variable with fallback
func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if result, err := strconv.Atoi(val); err == nil {
			return result
		}
	}
	return defaultVal
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
