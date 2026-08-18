package orchestration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

const (
	// Redis key patterns for execution debug store
	executionDebugKeyPrefix = "truvag3:execution:debug:"

	// Size thresholds for compression (same as LLM Debug Store)
	executionCompressionThreshold = 100 * 1024  // 100KB
	executionMaxPayloadSize       = 1024 * 1024 // 1MB

	// go-redis preserves the Redis TTL sentinel integers as raw duration
	// values rather than multiplying them by the command's one-second
	// precision.
	redisTTLKeyMissing time.Duration = -2
	redisTTLPersistent time.Duration = -1
)

var conversationIndexUpsertScript = redis.NewScript(`
local previous_ttl = redis.call("PTTL", KEYS[1])
redis.call("ZADD", KEYS[1], ARGV[1], ARGV[2])
local requested_ttl = tonumber(ARGV[3])
if previous_ttl == -2 or (previous_ttl >= 0 and previous_ttl < requested_ttl) then
	redis.call("PEXPIRE", KEYS[1], requested_ttl)
end
return previous_ttl
`)

// RedisExecutionDebugStoreOption configures the Redis execution debug store
type RedisExecutionDebugStoreOption func(*redisExecutionDebugStoreConfig)

type redisExecutionDebugStoreConfig struct {
	redisURL       string
	redisDB        int
	logger         core.Logger
	circuitBreaker core.CircuitBreaker // Interface - injected by application (optional)
	keyPrefix      string
	ttl            time.Duration
	errorTTL       time.Duration
	queryLimit     int
	indexScanLimit int
}

// WithExecutionDebugRedisURL sets the Redis connection URL
func WithExecutionDebugRedisURL(url string) RedisExecutionDebugStoreOption {
	return func(c *redisExecutionDebugStoreConfig) {
		c.redisURL = url
	}
}

// WithExecutionDebugRedisDB sets the Redis database number (default: 8)
func WithExecutionDebugRedisDB(db int) RedisExecutionDebugStoreOption {
	return func(c *redisExecutionDebugStoreConfig) {
		c.redisDB = db
	}
}

// WithExecutionDebugLogger sets the logger for execution debug store operations
func WithExecutionDebugLogger(logger core.Logger) RedisExecutionDebugStoreOption {
	return func(c *redisExecutionDebugStoreConfig) {
		c.logger = logger
	}
}

// WithExecutionDebugCircuitBreaker sets a circuit breaker for Redis operations.
// The circuit breaker must implement core.CircuitBreaker interface.
// If not provided, built-in Layer 1 resilience (simple retry with backoff) is used.
// This follows ARCHITECTURE.md: circuit breaker is injected by application, not created internally.
func WithExecutionDebugCircuitBreaker(cb core.CircuitBreaker) RedisExecutionDebugStoreOption {
	return func(c *redisExecutionDebugStoreConfig) {
		c.circuitBreaker = cb
	}
}

// WithExecutionDebugKeyPrefix sets a custom key prefix for execution debug records
func WithExecutionDebugKeyPrefix(prefix string) RedisExecutionDebugStoreOption {
	return func(c *redisExecutionDebugStoreConfig) {
		c.keyPrefix = prefix
	}
}

// WithExecutionDebugTTL sets custom TTL for successful execution debug records
func WithExecutionDebugTTL(ttl time.Duration) RedisExecutionDebugStoreOption {
	return func(c *redisExecutionDebugStoreConfig) {
		c.ttl = ttl
	}
}

// WithExecutionDebugErrorTTL sets custom TTL for error execution debug records
func WithExecutionDebugErrorTTL(ttl time.Duration) RedisExecutionDebugStoreOption {
	return func(c *redisExecutionDebugStoreConfig) {
		c.errorTTL = ttl
	}
}

// RedisExecutionDebugStore is a Redis-backed implementation for execution debugging.
// It provides persistent storage with TTL-based cleanup, compression for large payloads,
// and resilience protection.
//
// Resilience follows the Three-Layer Architecture from ARCHITECTURE.md:
// - Layer 1: Built-in simple retry with exponential backoff (always active)
// - Layer 2: Optional circuit breaker (injected via WithExecutionDebugCircuitBreaker)
// - Layer 3: Fallback to NoOp on persistent failures (handled by factory)
type RedisExecutionDebugStore struct {
	client         redis.UniversalClient
	ownsClient     bool
	logger         core.Logger
	circuitBreaker core.CircuitBreaker // Optional - injected by application
	keyPrefix      string
	ttl            time.Duration
	errorTTL       time.Duration
	queryLimit     int
	indexScanLimit int

	// Layer 1 resilience state (simple failure tracking)
	failureCount int
	failureMu    sync.Mutex
	lastFailure  time.Time
}

// NewRedisExecutionDebugStore creates a Redis-backed execution debug store with intelligent defaults.
// This provides the same zero-configuration experience as NewRedisLLMDebugStore.
//
// Environment variable precedence:
//   - REDIS_URL or TRUVAG3_REDIS_URL: Redis connection URL (default: localhost:6379)
//   - TRUVAG3_EXECUTION_DEBUG_REDIS_DB: Redis database number (default: 8)
//   - TRUVAG3_EXECUTION_DEBUG_TTL: TTL for successful records (default: 24h)
//   - TRUVAG3_EXECUTION_DEBUG_ERROR_TTL: TTL for error records (default: 168h)
//   - TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX: Key prefix (default: truvag3:execution:debug)
//   - TRUVAG3_EXECUTION_DEBUG_CONVERSATION_QUERY_LIMIT: per-query result ceiling (default: 1000)
//   - TRUVAG3_EXECUTION_DEBUG_INDEX_SCAN_LIMIT: per-query index scan ceiling (default: 5000)
//
// Usage:
//
//	// Zero-configuration - uses environment variables
//	store, err := orchestration.NewRedisExecutionDebugStore()
//
//	// With custom options
//	store, err := orchestration.NewRedisExecutionDebugStore(
//	    orchestration.WithExecutionDebugLogger(logger),
//	    orchestration.WithExecutionDebugTTL(48 * time.Hour),
//	)
func NewRedisExecutionDebugStore(opts ...RedisExecutionDebugStoreOption) (*RedisExecutionDebugStore, error) {
	return NewRedisExecutionDebugStoreWithConfig(
		DefaultConfig().ExecutionStore,
		opts...,
	)
}

// NewRedisExecutionDebugStoreWithConfig creates a direct Redis execution
// store using the same normalized framework configuration as provider-backed
// stores. Redis connection settings remain configurable through the existing
// Redis-specific options.
func NewRedisExecutionDebugStoreWithConfig(
	config ExecutionStoreConfig,
	opts ...RedisExecutionDebugStoreOption,
) (*RedisExecutionDebugStore, error) {
	cfg := defaultRedisExecutionDebugStoreConfig(config)

	// Apply explicit options (override defaults)
	for _, opt := range opts {
		opt(cfg)
	}
	cfg.keyPrefix = normalizeExecutionKeyPrefix(cfg.keyPrefix)

	// Parse Redis URL and create client
	redisOpt, err := redis.ParseURL(cfg.redisURL)
	if err != nil {
		// Try treating it as a simple address if URL parsing fails
		redisOpt = &redis.Options{
			Addr: cfg.redisURL,
		}
	}
	redisOpt.DB = cfg.redisDB

	client := redis.NewClient(core.ApplyRedisClientDefaults(redisOpt))

	// Verify connection with actionable error message
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis connection failed (DB %d): %w\n"+
			"Hint: Check REDIS_URL or TRUVAG3_REDIS_URL environment variables, "+
			"or use WithExecutionDebugRedisURL() option", cfg.redisDB, core.RedactSensitiveError(err))
	}

	// Note: Circuit breaker is optional and injected by application (per ARCHITECTURE.md)
	// If not provided, built-in Layer 1 resilience (simple retry) is used

	cfg.logger.Info("Redis execution debug store initialized", map[string]interface{}{
		"redis_addr":                    redisOpt.Addr,
		"redis_db":                      cfg.redisDB,
		"key_prefix":                    cfg.keyPrefix,
		"ttl":                           cfg.ttl.String(),
		"error_ttl":                     cfg.errorTTL.String(),
		"conversation_query_limit":      cfg.queryLimit,
		"conversation_index_scan_limit": cfg.indexScanLimit,
		"circuit_breaker":               cfg.circuitBreaker != nil,
		"resilience":                    "layer1_builtin", // Always has Layer 1
	})

	return newRedisExecutionDebugStore(client, true, cfg), nil
}

// NewRedisExecutionDebugStoreWithClient creates a store using an
// application-owned Redis client. Close leaves the supplied client open.
func NewRedisExecutionDebugStoreWithClient(
	client redis.UniversalClient,
	config ExecutionStoreConfig,
	opts ...RedisExecutionDebugStoreOption,
) (*RedisExecutionDebugStore, error) {
	if client == nil {
		return nil, fmt.Errorf("redis execution debug client is required")
	}
	cfg := redisExecutionDebugStoreConfigForClient(config)
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	cfg.keyPrefix = normalizeExecutionKeyPrefix(cfg.keyPrefix)
	return newRedisExecutionDebugStore(client, false, cfg), nil
}

func redisExecutionDebugStoreConfigForClient(config ExecutionStoreConfig) *redisExecutionDebugStoreConfig {
	config = normalizeExecutionStoreConfig(config)
	return &redisExecutionDebugStoreConfig{
		logger: &core.NoOpLogger{}, keyPrefix: config.KeyPrefix,
		ttl: config.TTL, errorTTL: config.ErrorTTL,
		queryLimit: config.ConversationQueryLimit, indexScanLimit: config.ConversationIndexScanLimit,
	}
}

func defaultRedisExecutionDebugStoreConfig(config ExecutionStoreConfig) *redisExecutionDebugStoreConfig {
	config = normalizeExecutionStoreConfig(config)
	return &redisExecutionDebugStoreConfig{
		redisURL:       getRedisURLWithFallback(),
		redisDB:        getEnvInt("TRUVAG3_EXECUTION_DEBUG_REDIS_DB", core.RedisDBExecutionDebug),
		logger:         &core.NoOpLogger{},
		keyPrefix:      config.KeyPrefix,
		ttl:            config.TTL,
		errorTTL:       config.ErrorTTL,
		queryLimit:     config.ConversationQueryLimit,
		indexScanLimit: config.ConversationIndexScanLimit,
	}
}

func newRedisExecutionDebugStore(client redis.UniversalClient, ownsClient bool, cfg *redisExecutionDebugStoreConfig) *RedisExecutionDebugStore {
	return &RedisExecutionDebugStore{
		client:         client,
		ownsClient:     ownsClient,
		logger:         cfg.logger,
		circuitBreaker: cfg.circuitBreaker,
		keyPrefix:      cfg.keyPrefix,
		ttl:            cfg.ttl,
		errorTTL:       cfg.errorTTL,
		queryLimit:     cfg.queryLimit,
		indexScanLimit: cfg.indexScanLimit,
	}
}

// Store saves a complete execution record (plan + result).
// Uses Layer 2 circuit breaker if injected, otherwise falls back to Layer 1 simple retry.
func (s *RedisExecutionDebugStore) Store(ctx context.Context, execution *StoredExecution) error {
	if execution == nil {
		return fmt.Errorf("execution cannot be nil")
	}
	if execution.RequestID == "" {
		return fmt.Errorf("request_id is required")
	}
	storedExecution, conversationID := sanitizeExecutionConversationMetadata(execution)

	operation := func() error {
		// Serialize with optional compression
		data, err := s.serialize(storedExecution)
		if err != nil {
			return fmt.Errorf("serialization failed: %w", err)
		}

		// Determine TTL based on success/failure.
		// ORCH-022: interrupted records have Result.Success == false (Result was nil
		// pre-fix, bypassing this branch). Carve them out so pending HITL approvals
		// keep the default TTL rather than the shorter errorTTL.
		ttl := s.ttl
		if storedExecution.Result != nil &&
			!storedExecution.Result.Success &&
			!storedExecution.Interrupted {
			ttl = s.errorTTL
		}

		// Store the main record
		key := s.recordKey(storedExecution.RequestID)
		if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
			return fmt.Errorf("redis set failed: %w", err)
		}

		// Update index for listing (sorted set by timestamp) - best effort
		indexKey := s.indexKey()
		if err := s.client.ZAdd(ctx, indexKey, redis.Z{
			Score:  float64(storedExecution.CreatedAt.UnixNano()),
			Member: storedExecution.RequestID,
		}).Err(); err != nil {
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to update execution debug index", map[string]interface{}{
					"operation":  "execution_store_index",
					"request_id": storedExecution.RequestID,
					"error_type": "index_write",
					"error":      safeExecutionStoreError(err),
				})
			}
			// Don't fail - index is for convenience, not critical
		}

		if conversationID != "" {
			conversationKey := s.conversationIndexKey(conversationID)
			if err := s.upsertConversationIndex(
				ctx,
				conversationKey,
				float64(storedExecution.CreatedAt.UnixNano()),
				storedExecution.RequestID,
				ttl,
			); err != nil {
				if s.logger != nil {
					s.logger.WarnWithContext(ctx, "Failed to update conversation execution index", map[string]interface{}{
						"operation":  "execution_store_conversation_index",
						"request_id": storedExecution.RequestID,
						"error_type": "index_write",
						"error":      safeExecutionStoreError(err),
					})
				}
			}
		}

		// Store trace ID mapping if available - best effort
		if storedExecution.TraceID != "" {
			traceKey := s.traceKey(storedExecution.TraceID)
			if err := s.client.Set(ctx, traceKey, storedExecution.RequestID, ttl).Err(); err != nil {
				if s.logger != nil {
					s.logger.Warn("Failed to store trace ID mapping", map[string]interface{}{
						"request_id": storedExecution.RequestID,
						"trace_id":   storedExecution.TraceID,
						"error":      err.Error(),
					})
				}
				// Don't fail - trace mapping is for convenience
			}
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

// Get retrieves the complete execution record by request ID.
func (s *RedisExecutionDebugStore) Get(ctx context.Context, requestID string) (*StoredExecution, error) {
	if requestID == "" {
		return nil, fmt.Errorf("request_id is required")
	}

	key := s.recordKey(requestID)
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("execution not found: %s", requestID)
	}
	if err != nil {
		return nil, fmt.Errorf("redis get failed: %w", err)
	}

	return s.deserialize(data)
}

// GetByTraceID retrieves an execution by distributed trace ID.
func (s *RedisExecutionDebugStore) GetByTraceID(ctx context.Context, traceID string) (*StoredExecution, error) {
	if traceID == "" {
		return nil, fmt.Errorf("trace_id is required")
	}

	// Look up request ID from trace ID mapping
	traceKey := s.traceKey(traceID)
	requestID, err := s.client.Get(ctx, traceKey).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("execution not found for trace: %s", traceID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lookup trace: %w", err)
	}

	return s.Get(ctx, requestID)
}

// Update modifies an existing execution record.
// Uses Layer 2 circuit breaker if injected, otherwise falls back to Layer 1 simple retry.
func (s *RedisExecutionDebugStore) Update(ctx context.Context, requestID string, execution *StoredExecution) error {
	if requestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if execution == nil {
		return fmt.Errorf("execution cannot be nil")
	}

	operation := func() error {
		existing, err := s.Get(ctx, requestID)
		if err != nil {
			return err
		}

		storedExecution, _ := sanitizeExecutionConversationMetadata(execution)
		if existingConversationID := ExecutionConversationID(existing); existingConversationID != "" {
			if storedExecution.Metadata == nil {
				storedExecution.Metadata = make(map[string]string)
			}
			storedExecution.Metadata[MetadataConversationID] = existingConversationID
		} else {
			delete(storedExecution.Metadata, MetadataConversationID)
		}

		// Serialize with optional compression
		data, err := s.serialize(storedExecution)
		if err != nil {
			return fmt.Errorf("serialization failed: %w", err)
		}

		// Use original TTL (we can't know the remaining TTL without Redis-specific commands).
		// ORCH-022: interrupted records have Result.Success == false after the
		// orchestrator fix — carve them out so pending HITL approvals keep the
		// default TTL rather than the shorter errorTTL.
		ttl := s.ttl
		if storedExecution.Result != nil &&
			!storedExecution.Result.Success &&
			!storedExecution.Interrupted {
			ttl = s.errorTTL
		}

		return s.client.Set(ctx, s.recordKey(requestID), data, ttl).Err()
	}

	// Layer 2: Use injected circuit breaker if available
	if s.circuitBreaker != nil {
		return s.circuitBreaker.Execute(ctx, operation)
	}

	// Layer 1: Built-in simple retry with exponential backoff
	return s.executeWithRetry(ctx, operation)
}

// ExtendTTL extends retention for investigation.
func (s *RedisExecutionDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	if requestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}

	execution, err := s.Get(ctx, requestID)
	if err != nil {
		return err
	}
	key := s.recordKey(requestID)

	if err := s.extendIndexTTLWithoutDowngrade(ctx, key, duration, false); err != nil {
		return err
	}

	if execution.TraceID != "" {
		traceKey := s.traceKey(execution.TraceID)
		if err := s.extendIndexTTLWithoutDowngrade(ctx, traceKey, duration, false); err != nil {
			if s.logger != nil {
				s.logger.Warn("Failed to extend trace ID mapping TTL", map[string]interface{}{
					"request_id": requestID,
					"trace_id":   execution.TraceID,
					"error":      err.Error(),
				})
			}
			// Don't fail - trace mapping TTL extension is best effort
		}
	}

	if conversationID := ExecutionConversationID(execution); conversationID != "" {
		conversationKey := s.conversationIndexKey(conversationID)
		if err := s.extendIndexTTLWithoutDowngrade(
			ctx,
			conversationKey,
			duration,
			false,
		); err != nil && s.logger != nil {
			s.logger.WarnWithContext(ctx, "Failed to extend conversation index TTL", map[string]interface{}{
				"operation":  "execution_store_conversation_index_ttl",
				"request_id": requestID,
				"error_type": "ttl_update",
				"error":      safeExecutionStoreError(err),
			})
		}
	}

	return nil
}

// SetMetadata adds metadata to an existing record.
// Uses Layer 2 circuit breaker if injected, otherwise falls back to Layer 1 simple retry.
func (s *RedisExecutionDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	if requestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if key == MetadataConversationID {
		return fmt.Errorf("%s is framework-owned and cannot be changed", MetadataConversationID)
	}

	operation := func() error {
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

		// Serialize with optional compression
		data, err := s.serialize(execution)
		if err != nil {
			return fmt.Errorf("serialization failed: %w", err)
		}

		// Get current TTL to preserve it
		redisKey := s.recordKey(requestID)
		ttl, err := s.client.TTL(ctx, redisKey).Result()
		if err != nil || ttl < 0 {
			ttl = s.ttl
		}

		return s.client.Set(ctx, redisKey, data, ttl).Err()
	}

	// Layer 2: Use injected circuit breaker if available
	if s.circuitBreaker != nil {
		return s.circuitBreaker.Execute(ctx, operation)
	}

	// Layer 1: Built-in simple retry with exponential backoff
	return s.executeWithRetry(ctx, operation)
}

// ListRecent returns recent executions ordered by creation time.
func (s *RedisExecutionDebugStore) ListRecent(ctx context.Context, limit int) ([]ExecutionSummary, error) {
	const maxLimit = 1000 // Prevent unbounded queries
	if limit <= 0 {
		limit = 50 // Default limit
	} else if limit > maxLimit {
		limit = maxLimit
	}

	// Get recent request IDs from sorted set (newest first)
	indexKey := s.indexKey()
	// Keep the legacy command for Redis-compatible providers without ZRANGE REV.
	//nolint:staticcheck // ZRevRange remains supported by go-redis/v9.
	ids, err := s.client.ZRevRange(ctx, indexKey, 0, int64(limit-1)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list recent executions: %w", err)
	}

	summaries := make([]ExecutionSummary, 0, len(ids))
	for _, id := range ids {
		execution, err := s.Get(ctx, id)
		if err != nil {
			// Clean up stale index entry
			_ = s.client.ZRem(ctx, indexKey, id)
			continue // Skip missing records (TTL expired)
		}

		summaries = append(summaries, executionSummaryFromStored(execution))
	}

	return summaries, nil
}

// ListByConversationID returns the most recent bounded window of executions for
// one conversation, ordered chronologically within that window.
func (s *RedisExecutionDebugStore) ListByConversationID(
	ctx context.Context,
	conversationID string,
	limit int,
) ([]ExecutionSummary, error) {
	if err := validateConversationQueryID(conversationID); err != nil {
		return nil, err
	}

	limit = normalizeConversationQueryLimit(limit, s.queryLimit)
	scanLimit := s.indexScanLimit
	if scanLimit <= 0 {
		scanLimit = defaultConversationIndexScanLimit
	}
	indexKey := s.conversationIndexKey(conversationID)
	summaries := make([]ExecutionSummary, 0, limit)
	staleMembers := make([]interface{}, 0)
	seenMembers := make(map[string]struct{})

	var offset int64
	for scanned := 0; scanned < scanLimit && len(summaries) < limit; {
		batchSize := conversationIndexReadBatchSize
		if remaining := scanLimit - scanned; remaining < batchSize {
			batchSize = remaining
		}
		// Keep the legacy command for Redis-compatible providers without ZRANGE REV.
		//nolint:staticcheck // ZRevRange remains supported by go-redis/v9.
		requestIDs, err := s.client.ZRevRange(
			ctx,
			indexKey,
			offset,
			offset+int64(batchSize)-1,
		).Result()
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

		if len(requestIDs) > 0 {
			recordKeys := make([]string, len(requestIDs))
			for i, requestID := range requestIDs {
				recordKeys[i] = s.recordKey(requestID)
			}
			rawRecords, err := s.client.MGet(ctx, recordKeys...).Result()
			if err != nil {
				return nil, fmt.Errorf("failed to load conversation executions: %w", err)
			}
			for i, rawRecord := range rawRecords {
				requestID := requestIDs[i]
				if rawRecord == nil {
					staleMembers = append(staleMembers, requestID)
					continue
				}
				serialized, ok := rawRecord.(string)
				if !ok {
					staleMembers = append(staleMembers, requestID)
					continue
				}
				execution, err := s.deserialize([]byte(serialized))
				if err != nil || ExecutionConversationID(execution) != conversationID {
					staleMembers = append(staleMembers, requestID)
					continue
				}
				summaries = append(
					summaries,
					executionSummaryFromStored(execution),
				)
				if len(summaries) == limit {
					break
				}
			}
		}
		if batchCount < batchSize {
			break
		}
	}

	if len(staleMembers) > 0 {
		if err := s.client.ZRem(ctx, indexKey, staleMembers...).Err(); err != nil && s.logger != nil {
			s.logger.WarnWithContext(ctx, "Failed to prune stale conversation index entries", map[string]interface{}{
				"operation":  "execution_store_conversation_index_cleanup",
				"request_id": fmt.Sprint(staleMembers[0]),
				"error_type": "index_write",
				"error":      safeExecutionStoreError(err),
			})
		}
	}

	for left, right := 0, len(summaries)-1; left < right; left, right = left+1, right-1 {
		summaries[left], summaries[right] = summaries[right], summaries[left]
	}
	return summaries, nil
}

// Close closes the Redis connection.
func (s *RedisExecutionDebugStore) Close() error {
	if !s.ownsClient {
		return nil
	}
	return s.client.Close()
}

// Key building helper methods using configurable keyPrefix

func (s *RedisExecutionDebugStore) recordKey(requestID string) string {
	return normalizeExecutionKeyPrefix(s.keyPrefix) + requestID
}

func (s *RedisExecutionDebugStore) indexKey() string {
	return normalizeExecutionKeyPrefix(s.keyPrefix) + "index"
}

func (s *RedisExecutionDebugStore) traceKey(traceID string) string {
	return normalizeExecutionKeyPrefix(s.keyPrefix) + "trace:" + traceID
}

func (s *RedisExecutionDebugStore) conversationIndexKey(conversationID string) string {
	return executionConversationIndexKey(s.keyPrefix, conversationID)
}

func (s *RedisExecutionDebugStore) upsertConversationIndex(
	ctx context.Context,
	key string,
	score float64,
	requestID string,
	minTTL time.Duration,
) error {
	ttlMilliseconds := minTTL.Milliseconds()
	if ttlMilliseconds <= 0 {
		ttlMilliseconds = 1
	}
	return conversationIndexUpsertScript.Run(
		ctx,
		s.client,
		[]string{key},
		strconv.FormatFloat(score, 'g', -1, 64),
		requestID,
		strconv.FormatInt(ttlMilliseconds, 10),
	).Err()
}

func (s *RedisExecutionDebugStore) extendIndexTTLWithoutDowngrade(
	ctx context.Context,
	key string,
	minTTL time.Duration,
	expirePersistent bool,
) error {
	currentTTL, err := s.client.TTL(ctx, key).Result()
	if err != nil {
		return err
	}
	switch {
	case currentTTL == redisTTLKeyMissing:
		return nil
	case currentTTL == redisTTLPersistent:
		if !expirePersistent {
			return nil
		}
		return s.client.Expire(ctx, key, minTTL).Err()
	case currentTTL >= minTTL:
		return nil
	default:
		return s.client.Expire(ctx, key, minTTL).Err()
	}
}

// Layer 1 Resilience Constants (same as LLM Debug Store)
const (
	execLayer1MaxRetries     = 3
	execLayer1InitialBackoff = 100 * time.Millisecond
	execLayer1MaxBackoff     = 2 * time.Second
	execLayer1FailureWindow  = 30 * time.Second
	execLayer1MaxFailures    = 5
)

// executeWithRetry implements Layer 1 built-in resilience with simple retry and exponential backoff.
// This is always available, even without an injected circuit breaker.
// Per ARCHITECTURE.md Layer 1: "3 retries with exponential backoff, simple failure tracking"
func (s *RedisExecutionDebugStore) executeWithRetry(ctx context.Context, operation func() error) error {
	// Check if we're in cooldown due to too many failures
	s.failureMu.Lock()
	if s.failureCount >= execLayer1MaxFailures && time.Since(s.lastFailure) < execLayer1FailureWindow {
		s.failureMu.Unlock()
		s.logger.Warn("Layer 1 resilience: in cooldown period", map[string]interface{}{
			"failures":     s.failureCount,
			"cooldown_sec": execLayer1FailureWindow.Seconds(),
		})
		return fmt.Errorf("execution debug store in cooldown after %d failures", s.failureCount)
	}
	s.failureMu.Unlock()

	var lastErr error
	backoff := execLayer1InitialBackoff

	for attempt := 1; attempt <= execLayer1MaxRetries; attempt++ {
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
			"max":     execLayer1MaxRetries,
			"backoff": backoff.String(),
			"error":   err.Error(),
		})

		// Don't sleep on last attempt
		if attempt < execLayer1MaxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}

			// Exponential backoff with cap
			backoff *= 2
			if backoff > execLayer1MaxBackoff {
				backoff = execLayer1MaxBackoff
			}
		}
	}

	// All retries failed - track failure
	s.failureMu.Lock()
	s.failureCount++
	s.lastFailure = time.Now()
	s.failureMu.Unlock()

	return fmt.Errorf("operation failed after %d attempts: %w", execLayer1MaxRetries, lastErr)
}

// serialize with optional gzip compression (same pattern as LLM Debug Store)
func (s *RedisExecutionDebugStore) serialize(execution *StoredExecution) ([]byte, error) {
	data, err := json.Marshal(execution)
	if err != nil {
		return nil, err
	}

	// Compress if over threshold
	if len(data) > executionCompressionThreshold {
		var buf bytes.Buffer
		buf.WriteByte(1) // Compression flag
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(data); err != nil {
			return nil, err
		}
		if err := gz.Close(); err != nil {
			return nil, err
		}
		s.logger.Debug("Compressed execution debug record", map[string]interface{}{
			"original_size":   len(data),
			"compressed_size": buf.Len(),
		})
		return buf.Bytes(), nil
	}

	// Prepend 0 byte to indicate no compression
	return append([]byte{0}, data...), nil
}

// deserialize with optional gzip decompression (same pattern as LLM Debug Store)
func (s *RedisExecutionDebugStore) deserialize(data []byte) (*StoredExecution, error) {
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

	var execution StoredExecution
	if err := json.Unmarshal(jsonData, &execution); err != nil {
		return nil, err
	}
	return &execution, nil
}

// Ensure RedisExecutionDebugStore implements its framework contracts.
var (
	_ ExecutionStore              = (*RedisExecutionDebugStore)(nil)
	_ ConversationExecutionLister = (*RedisExecutionDebugStore)(nil)
)

// Deprecated option function aliases for backwards compatibility
// These will be removed in a future version

// WithExecutionRedisURL is deprecated. Use WithExecutionDebugRedisURL instead.
func WithExecutionRedisURL(url string) RedisExecutionDebugStoreOption {
	return WithExecutionDebugRedisURL(url)
}

// WithExecutionRedisDB is deprecated. Use WithExecutionDebugRedisDB instead.
func WithExecutionRedisDB(db int) RedisExecutionDebugStoreOption {
	return WithExecutionDebugRedisDB(db)
}

// WithExecutionLogger is deprecated. Use WithExecutionDebugLogger instead.
func WithExecutionLogger(logger core.Logger) RedisExecutionDebugStoreOption {
	return WithExecutionDebugLogger(logger)
}

// WithExecutionKeyPrefix is deprecated. Use WithExecutionDebugKeyPrefix instead.
func WithExecutionKeyPrefix(prefix string) RedisExecutionDebugStoreOption {
	return WithExecutionDebugKeyPrefix(prefix)
}

// WithExecutionTTL is deprecated. Use WithExecutionDebugTTL instead.
func WithExecutionTTL(ttl time.Duration) RedisExecutionDebugStoreOption {
	return WithExecutionDebugTTL(ttl)
}

// WithExecutionErrorTTL is deprecated. Use WithExecutionDebugErrorTTL instead.
func WithExecutionErrorTTL(ttl time.Duration) RedisExecutionDebugStoreOption {
	return WithExecutionDebugErrorTTL(ttl)
}

// NewRedisExecutionStore is deprecated. Use NewRedisExecutionDebugStore instead.
// This alias is provided for backwards compatibility and will be removed in a future version.
func NewRedisExecutionStore(opts ...RedisExecutionDebugStoreOption) (*RedisExecutionDebugStore, error) {
	return NewRedisExecutionDebugStore(opts...)
}
