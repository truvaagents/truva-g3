package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// =============================================================================
// RedisCheckpointStore - Reference Implementation
// =============================================================================
//
// RedisCheckpointStore implements CheckpointStore using Redis.
// This is the framework's reference implementation for production use.
// Applications can implement their own store (PostgreSQL, S3, DynamoDB, etc.)
//
// Key format:
//   - Checkpoint: {prefix}:checkpoint:{checkpoint_id}
//   - Pending index: {prefix}:pending (Redis Set)
//   - Request index: {prefix}:request:{request_id} (Redis Set)
//
// Per FRAMEWORK_DESIGN_PRINCIPLES.md, optional dependencies use NoOp defaults.
//
// Usage:
//
//	store := NewRedisCheckpointStore(
//	    WithCheckpointRedisURL("redis://localhost:6379"),
//	    WithCheckpointTTL(24 * time.Hour),
//	    WithCheckpointStoreLogger(logger),
//	)
//
// =============================================================================

// RedisCheckpointStore implements CheckpointStore using Redis.
type RedisCheckpointStore struct {
	client    *redis.Client
	keyPrefix string
	ttl       time.Duration
	redisURL  string // For error messages

	// Optional dependencies (injected per framework patterns)
	logger    core.Logger    // Defaults to NoOp
	telemetry core.Telemetry // Defaults to NoOp

	// Agent identity — stamped onto every checkpoint saved by this store (RC3-Backend)
	agentName    string // from TRUVAG3_AGENT_NAME (with TRUVAG3_K8S_SERVICE_NAME fallback)
	agentAddress string // from core.ResolveServiceAddress; empty if address cannot be resolved

	// Expiry processor state
	expiryCtx      context.Context
	expiryCancel   context.CancelFunc
	expiryCallback ExpiryCallback
	expiryWg       sync.WaitGroup
	expiryStarted  bool
	expiryMu       sync.Mutex // Protects expiry processor state
	instanceID     string     // For distributed claim mechanism
	config         ExpiryProcessorConfig
}

// redisCheckpointConfig holds configuration for the checkpoint store
type redisCheckpointConfig struct {
	redisURL   string
	redisDB    int
	keyPrefix  string
	ttl        time.Duration
	logger     core.Logger
	telemetry  core.Telemetry
	instanceID string // For distributed claim mechanism
}

// RedisCheckpointStoreOption configures the checkpoint store
type RedisCheckpointStoreOption func(*redisCheckpointConfig)

// WithCheckpointRedisURL sets the Redis connection URL
func WithCheckpointRedisURL(url string) RedisCheckpointStoreOption {
	return func(c *redisCheckpointConfig) {
		c.redisURL = url
	}
}

// WithCheckpointRedisDB sets the Redis database number
func WithCheckpointRedisDB(db int) RedisCheckpointStoreOption {
	return func(c *redisCheckpointConfig) {
		c.redisDB = db
	}
}

// WithCheckpointKeyPrefix sets the key prefix for checkpoint storage
func WithCheckpointKeyPrefix(prefix string) RedisCheckpointStoreOption {
	return func(c *redisCheckpointConfig) {
		c.keyPrefix = prefix
	}
}

// WithCheckpointTTL sets the TTL for checkpoint storage
func WithCheckpointTTL(ttl time.Duration) RedisCheckpointStoreOption {
	return func(c *redisCheckpointConfig) {
		c.ttl = ttl
	}
}

// WithInstanceID sets a custom instance ID for the distributed claim mechanism.
// If not set, defaults to hostname + random suffix.
//
// Use this for testing or when you need deterministic instance IDs.
// In production, the auto-generated ID is recommended.
func WithInstanceID(id string) RedisCheckpointStoreOption {
	return func(c *redisCheckpointConfig) {
		c.instanceID = id
	}
}

// WithCheckpointStoreLogger sets the logger for the checkpoint store.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md, this option is in the implementation file
// as it configures Redis-specific behavior.
func WithCheckpointStoreLogger(logger core.Logger) RedisCheckpointStoreOption {
	return func(c *redisCheckpointConfig) {
		if logger == nil {
			return
		}
		// Use ComponentAwareLogger for component-based log segregation (per LOGGING_IMPLEMENTATION_GUIDE.md)
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			c.logger = cal.WithComponent("framework/orchestration")
		} else {
			c.logger = logger
		}
	}
}

// WithCheckpointStoreTelemetry sets the telemetry provider for the checkpoint store.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md, this option is in the implementation file
// as it configures Redis-specific behavior.
func WithCheckpointStoreTelemetry(telemetry core.Telemetry) RedisCheckpointStoreOption {
	return func(c *redisCheckpointConfig) {
		c.telemetry = telemetry
	}
}

// NewRedisCheckpointStore creates a new Redis-backed checkpoint store.
// Returns concrete type per Go idiom "return structs, accept interfaces".
//
// Configuration priority:
//  1. Explicit option (e.g., WithCheckpointRedisURL)
//  2. Environment variable (REDIS_URL, TRUVAG3_HITL_REDIS_DB)
//  3. Default value
func NewRedisCheckpointStore(opts ...interface{}) (*RedisCheckpointStore, error) {
	// Build key prefix with optional agent name for multi-agent isolation.
	// Format: {base_prefix}:{agent_name} or just {base_prefix} if no agent name.
	// Capture source before config/options so we can log accurately after options applied.
	// basePrefixExplicit tracks whether the operator explicitly set TRUVAG3_HITL_KEY_PREFIX
	// — used by the shared-prefix warning below to suppress false positives when the
	// operator has already chosen an isolating prefix via that env var.
	basePrefixExplicit := os.Getenv("TRUVAG3_HITL_KEY_PREFIX") != ""
	basePrefix := getEnvOrDefault("TRUVAG3_HITL_KEY_PREFIX", "truvag3:hitl")
	agentName := getEnvOrDefault("TRUVAG3_AGENT_NAME", "")
	agentNameSource := "TRUVAG3_AGENT_NAME"
	if agentName == "" {
		// Fall back to K8s service name — semantically equivalent in K8s deployments.
		// Uses core.EnvServiceName constant — single source of truth per design principles §3.3.
		agentName = getEnvOrDefault(core.EnvServiceName, "")
		agentNameSource = core.EnvServiceName
	}
	if agentName == "" {
		// Neither env var supplied a value. Mark the source as "none" so the
		// startup log doesn't falsely attribute the prefix to an env var that
		// was checked but empty.
		agentNameSource = "none"
	}
	keyPrefix := basePrefix
	if agentName != "" {
		keyPrefix = fmt.Sprintf("%s:%s", basePrefix, agentName)
	}

	// Initialize config with defaults
	config := &redisCheckpointConfig{
		redisURL:  getEnvOrDefault("REDIS_URL", "redis://localhost:6379"),
		redisDB:   getEnvIntOrDefault("TRUVAG3_HITL_REDIS_DB", 6), // Default to DB 6 for HITL
		keyPrefix: keyPrefix,
		ttl:       24 * time.Hour,
		logger:    &core.NoOpLogger{},
	}

	// Apply options (may inject a real logger via WithCheckpointStoreLogger)
	for _, opt := range opts {
		if o, ok := opt.(RedisCheckpointStoreOption); ok {
			o(config)
		}
	}

	// Log resolved key prefix AFTER options so a real logger (if injected) is used.
	// Read from config.keyPrefix (not the local keyPrefix) so WithCheckpointKeyPrefix
	// overrides are reflected accurately. Emit unconditionally — operators should be
	// able to confirm which prefix this process is actually using.
	if config.logger != nil {
		config.logger.Debug("Checkpoint store key prefix resolved", map[string]interface{}{
			"operation":  "checkpoint_store_init",
			"key_prefix": config.keyPrefix,
			"source":     agentNameSource,
		})
	}

	// Warn when no isolating identifier has been supplied through any of the
	// available paths. Multiple agents that all land in this branch share a single
	// pending index in Redis — anything that fans out across agents (e.g., the
	// registry viewer's HITL list) will see duplicate checkpoints.
	//
	// Suppress the warning when the operator has chosen isolation through any of:
	//   - TRUVAG3_AGENT_NAME / TRUVAG3_K8S_SERVICE_NAME (covered by agentName != "")
	//   - TRUVAG3_HITL_KEY_PREFIX (covered by basePrefixExplicit — they made a deliberate choice)
	//   - WithCheckpointKeyPrefix option (covered by config.keyPrefix != basePrefix)
	if agentName == "" && !basePrefixExplicit && config.keyPrefix == basePrefix && config.logger != nil {
		config.logger.Warn("HITL checkpoint store using shared key prefix — set TRUVAG3_AGENT_NAME (or TRUVAG3_K8S_SERVICE_NAME) to isolate per-agent state", map[string]interface{}{
			"operation":   "checkpoint_store_init",
			"key_prefix":  config.keyPrefix,
			"impact":      "multiple agents sharing this Redis will share the pending index; viewer may show duplicate HITL checkpoints",
			"remediation": "set TRUVAG3_AGENT_NAME, TRUVAG3_K8S_SERVICE_NAME, TRUVAG3_HITL_KEY_PREFIX, or pass WithCheckpointKeyPrefix",
		})
	}

	// Parse Redis URL and create options
	redisOpts, err := redis.ParseURL(config.redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL %s: %w (check REDIS_URL environment variable)", config.redisURL, err)
	}
	redisOpts.DB = config.redisDB

	// Create Redis client
	client := redis.NewClient(redisOpts)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis at %s: %w (check REDIS_URL and Redis connectivity)", config.redisURL, err)
	}

	// Generate instance ID if not provided
	instanceID := config.instanceID
	if instanceID == "" {
		instanceID = generateInstanceID()
	}

	// Resolve agent address for checkpoint stamping (RC3-Backend).
	// DefaultConfig + LoadFromEnv does NOT call Validate — safe even when
	// TRUVAG3_AGENT_NAME / TRUVAG3_K8S_SERVICE_NAME are unset in the environment.
	cfg := core.DefaultConfig()
	_ = cfg.LoadFromEnv() // returns nil on success; no validation called
	resolvedAddr, resolvedPort := core.ResolveServiceAddress(cfg, nil)
	agentAddress := ""
	if resolvedAddr != "" && resolvedPort != 0 {
		agentAddress = fmt.Sprintf("http://%s:%d", resolvedAddr, resolvedPort)
	}

	return &RedisCheckpointStore{
		client:       client,
		keyPrefix:    config.keyPrefix,
		ttl:          config.ttl,
		redisURL:     config.redisURL,
		logger:       config.logger,
		telemetry:    config.telemetry,
		instanceID:   instanceID,
		agentName:    agentName,
		agentAddress: agentAddress,
	}, nil
}

// -----------------------------------------------------------------------------
// CheckpointStore Implementation
// -----------------------------------------------------------------------------

// SaveCheckpoint persists execution state with trace correlation.
// Per gold standard: use operation field, logger nil check, RecordSpanError, telemetry.Counter.
func (s *RedisCheckpointStore) SaveCheckpoint(ctx context.Context, cp *ExecutionCheckpoint) error {
	key := fmt.Sprintf("%s:checkpoint:%s", s.keyPrefix, cp.CheckpointID)

	// Stamp physical agent identity onto the checkpoint (RC3-Backend).
	// Only set if not already populated — allow callers to override if needed.
	if cp.AgentName == "" && s.agentName != "" {
		cp.AgentName = s.agentName
	}
	if cp.AgentAddress == "" && s.agentAddress != "" {
		cp.AgentAddress = s.agentAddress
	}

	data, err := json.Marshal(cp)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		return fmt.Errorf("failed to marshal checkpoint %s: %w (check checkpoint data for non-serializable fields)", cp.CheckpointID, err)
	}

	// Store checkpoint with TTL
	if err := s.client.Set(ctx, key, data, s.ttl).Err(); err != nil {
		telemetry.RecordSpanError(ctx, err)
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to save checkpoint", map[string]interface{}{
				"operation":     "hitl_checkpoint_save",
				"checkpoint_id": cp.CheckpointID,
				"request_id":    cp.RequestID,
				"error":         err.Error(),
			})
		}
		return fmt.Errorf("failed to save checkpoint %s to Redis: %w (check REDIS_URL=%s and Redis connectivity)", cp.CheckpointID, err, s.redisURL)
	}

	// Add to pending index if status is pending
	if cp.Status == CheckpointStatusPending {
		indexKey := fmt.Sprintf("%s:pending", s.keyPrefix)
		if err := s.client.SAdd(ctx, indexKey, cp.CheckpointID).Err(); err != nil {
			telemetry.RecordSpanError(ctx, err)
			if s.logger != nil {
				s.logger.ErrorWithContext(ctx, "Failed to add to pending index", map[string]interface{}{
					"operation":     "hitl_pending_index_add",
					"checkpoint_id": cp.CheckpointID,
					"request_id":    cp.RequestID,
					"error":         err.Error(),
				})
			}
			return fmt.Errorf("failed to add checkpoint %s to pending index: %w (Redis SADD failed)", cp.CheckpointID, err)
		}
	}

	// Add to request index for lookup by request_id
	if cp.RequestID != "" {
		requestIndexKey := fmt.Sprintf("%s:request:%s", s.keyPrefix, cp.RequestID)
		if err := s.client.SAdd(ctx, requestIndexKey, cp.CheckpointID).Err(); err != nil {
			// Non-fatal - just log warning
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to add to request index", map[string]interface{}{
					"operation":     "hitl_request_index_add",
					"checkpoint_id": cp.CheckpointID,
					"request_id":    cp.RequestID,
					"error":         err.Error(),
				})
			}
		}
	}

	// Add span event for tracing visibility (per DISTRIBUTED_TRACING_GUIDE.md)
	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.saved",
		attribute.String("request_id", cp.RequestID),
		attribute.String("checkpoint_id", cp.CheckpointID),
		attribute.String("status", string(cp.Status)),
	)

	// Record counter metric (per gold standard pattern in executor.go)
	if cp.Decision != nil {
		telemetry.Counter("orchestration.hitl.checkpoint_saved",
			"reason", string(cp.Decision.Reason),
			"module", telemetry.ModuleOrchestration,
		)
	}

	// Log success with operation field (per gold standard pattern)
	if s.logger != nil {
		s.logger.DebugWithContext(ctx, "Checkpoint saved", map[string]interface{}{
			"operation":       "hitl_checkpoint_save_complete",
			"checkpoint_id":   cp.CheckpointID,
			"request_id":      cp.RequestID,
			"status":          cp.Status,
			"interrupt_point": cp.InterruptPoint,
		})
	}

	return nil
}

// LoadCheckpoint retrieves a checkpoint with trace correlation.
// Per gold standard: use operation field, logger nil check, RecordSpanError.
func (s *RedisCheckpointStore) LoadCheckpoint(ctx context.Context, checkpointID string) (*ExecutionCheckpoint, error) {
	key := fmt.Sprintf("%s:checkpoint:%s", s.keyPrefix, checkpointID)

	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		if s.logger != nil {
			s.logger.DebugWithContext(ctx, "Checkpoint not found", map[string]interface{}{
				"operation":     "hitl_checkpoint_load",
				"checkpoint_id": checkpointID,
			})
		}
		return nil, &ErrCheckpointNotFound{CheckpointID: checkpointID}
	}
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to load checkpoint", map[string]interface{}{
				"operation":     "hitl_checkpoint_load",
				"checkpoint_id": checkpointID,
				"error":         err.Error(),
			})
		}
		return nil, fmt.Errorf("failed to load checkpoint %s from Redis: %w (check REDIS_URL=%s)", checkpointID, err, s.redisURL)
	}

	var cp ExecutionCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		telemetry.RecordSpanError(ctx, err)
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to unmarshal checkpoint", map[string]interface{}{
				"operation":     "hitl_checkpoint_load",
				"checkpoint_id": checkpointID,
				"error":         err.Error(),
			})
		}
		return nil, fmt.Errorf("failed to unmarshal checkpoint %s: %w (checkpoint data may be corrupted)", checkpointID, err)
	}

	// Add span event for successful load (request_id first per gold standard)
	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.loaded",
		attribute.String("request_id", cp.RequestID),
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("status", string(cp.Status)),
	)

	// Log successful load with operation field
	if s.logger != nil {
		s.logger.DebugWithContext(ctx, "Checkpoint loaded", map[string]interface{}{
			"operation":     "hitl_checkpoint_load_complete",
			"checkpoint_id": checkpointID,
			"request_id":    cp.RequestID,
			"status":        cp.Status,
		})
	}

	return &cp, nil
}

// UpdateCheckpointStatus updates the status of a checkpoint.
func (s *RedisCheckpointStore) UpdateCheckpointStatus(ctx context.Context, checkpointID string, status CheckpointStatus) error {
	// Load existing checkpoint
	cp, err := s.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		return err
	}

	oldStatus := cp.Status
	cp.Status = status

	// Remove from pending index if no longer pending
	if oldStatus == CheckpointStatusPending && status != CheckpointStatusPending {
		indexKey := fmt.Sprintf("%s:pending", s.keyPrefix)
		if err := s.client.SRem(ctx, indexKey, checkpointID).Err(); err != nil {
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to remove from pending index", map[string]interface{}{
					"operation":     "hitl_pending_index_remove",
					"checkpoint_id": checkpointID,
					"error":         err.Error(),
				})
			}
		}
	}

	// Save updated checkpoint
	if err := s.SaveCheckpoint(ctx, cp); err != nil {
		return err
	}

	// Log status change
	if s.logger != nil {
		s.logger.InfoWithContext(ctx, "Checkpoint status updated", map[string]interface{}{
			"operation":     "hitl_checkpoint_status_update",
			"checkpoint_id": checkpointID,
			"request_id":    cp.RequestID,
			"old_status":    oldStatus,
			"new_status":    status,
		})
	}

	return nil
}

// ListPendingCheckpoints returns checkpoints awaiting human response.
func (s *RedisCheckpointStore) ListPendingCheckpoints(ctx context.Context, filter CheckpointFilter) ([]*ExecutionCheckpoint, error) {
	indexKey := fmt.Sprintf("%s:pending", s.keyPrefix)

	// Get all pending checkpoint IDs
	ids, err := s.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		return nil, fmt.Errorf("failed to list pending checkpoints: %w", err)
	}

	checkpoints := make([]*ExecutionCheckpoint, 0, len(ids))

	// Apply offset
	start := filter.Offset
	if start >= len(ids) {
		return checkpoints, nil
	}

	// Apply limit
	end := len(ids)
	if filter.Limit > 0 && start+filter.Limit < end {
		end = start + filter.Limit
	}

	// Load each checkpoint
	for _, id := range ids[start:end] {
		cp, err := s.LoadCheckpoint(ctx, id)
		if err != nil {
			if IsCheckpointNotFound(err) {
				// Checkpoint expired or deleted - remove from index
				s.client.SRem(ctx, indexKey, id)
				continue
			}
			// Log error but continue
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to load checkpoint from pending list", map[string]interface{}{
					"operation":     "hitl_list_pending",
					"checkpoint_id": id,
					"error":         err.Error(),
				})
			}
			continue
		}

		// Apply request_id filter if specified
		if filter.RequestID != "" && cp.RequestID != filter.RequestID {
			continue
		}

		// Apply status filter if specified (though pending index should only have pending)
		if filter.Status != "" && cp.Status != filter.Status {
			continue
		}

		checkpoints = append(checkpoints, cp)
	}

	return checkpoints, nil
}

// DeleteCheckpoint removes a checkpoint after completion.
func (s *RedisCheckpointStore) DeleteCheckpoint(ctx context.Context, checkpointID string) error {
	// Load checkpoint to get request_id for index cleanup
	cp, err := s.LoadCheckpoint(ctx, checkpointID)
	if err != nil && !IsCheckpointNotFound(err) {
		return err
	}

	key := fmt.Sprintf("%s:checkpoint:%s", s.keyPrefix, checkpointID)

	// Delete checkpoint
	if err := s.client.Del(ctx, key).Err(); err != nil {
		telemetry.RecordSpanError(ctx, err)
		return fmt.Errorf("failed to delete checkpoint %s: %w", checkpointID, err)
	}

	// Remove from pending index
	pendingKey := fmt.Sprintf("%s:pending", s.keyPrefix)
	s.client.SRem(ctx, pendingKey, checkpointID)

	// Remove from request index if we have the request_id
	if cp != nil && cp.RequestID != "" {
		requestIndexKey := fmt.Sprintf("%s:request:%s", s.keyPrefix, cp.RequestID)
		s.client.SRem(ctx, requestIndexKey, checkpointID)
	}

	// Add span event
	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.deleted",
		attribute.String("checkpoint_id", checkpointID),
	)

	if s.logger != nil {
		s.logger.DebugWithContext(ctx, "Checkpoint deleted", map[string]interface{}{
			"operation":     "hitl_checkpoint_delete",
			"checkpoint_id": checkpointID,
		})
	}

	return nil
}

// -----------------------------------------------------------------------------
// Helper functions
// -----------------------------------------------------------------------------

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvIntOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := parseInt(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func parseInt(s string) (int, error) {
	var i int
	_, err := fmt.Sscanf(s, "%d", &i)
	return i, err
}

func getEnvBoolOrDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		switch value {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return defaultValue
}

func getEnvDurationOrDefault(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}

// ExpiryProcessorConfigFromEnv loads ExpiryProcessorConfig from environment variables.
// This enables deployment-specific configuration without code changes.
//
// Per FRAMEWORK_DESIGN_PRINCIPLES.md "Environment Variable Precedence":
//
//	Priority 1: Explicitly set configuration options
//	Priority 2: Environment variables (this function)
//	Priority 3: Sensible defaults
//
// Environment variables:
//
//	TRUVAG3_HITL_EXPIRY_ENABLED   - Enable/disable expiry processor (default: true)
//	TRUVAG3_HITL_EXPIRY_INTERVAL  - Scan interval (default: 10s)
//	TRUVAG3_HITL_EXPIRY_BATCH_SIZE - Max checkpoints per scan (default: 100)
//	TRUVAG3_HITL_EXPIRY_DELIVERY  - Delivery semantics: "at_most_once" or "at_least_once" (default: at_most_once)
//
// Usage:
//
//	// Load from environment variables
//	config := ExpiryProcessorConfigFromEnv()
//
//	// Or use as a base and override specific values
//	config := ExpiryProcessorConfigFromEnv()
//	config.ScanInterval = 5 * time.Second  // Override
//
//	checkpointStore.StartExpiryProcessor(ctx, config)
func ExpiryProcessorConfigFromEnv() ExpiryProcessorConfig {
	// Parse delivery semantics with validation
	deliverySemantics := DeliveryAtMostOnce
	if envValue := os.Getenv("TRUVAG3_HITL_EXPIRY_DELIVERY"); envValue != "" {
		switch envValue {
		case "at_most_once":
			deliverySemantics = DeliveryAtMostOnce
		case "at_least_once":
			deliverySemantics = DeliveryAtLeastOnce
			// Invalid values are silently ignored - will use default
			// This aligns with FRAMEWORK_DESIGN_PRINCIPLES.md: "Fail-Safe Defaults"
		}
	}

	return ExpiryProcessorConfig{
		Enabled:           getEnvBoolOrDefault("TRUVAG3_HITL_EXPIRY_ENABLED", true),
		ScanInterval:      getEnvDurationOrDefault("TRUVAG3_HITL_EXPIRY_INTERVAL", 10*time.Second),
		BatchSize:         getEnvIntOrDefault("TRUVAG3_HITL_EXPIRY_BATCH_SIZE", 100),
		DeliverySemantics: deliverySemantics,
	}
}

// Close closes the Redis connection
func (s *RedisCheckpointStore) Close() error {
	return s.client.Close()
}

// =============================================================================
// Expiry Processor Implementation (RedisCheckpointStore)
// =============================================================================

// validateExpiryConfig validates the expiry processor configuration.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md: fail-fast for configuration errors.
func validateExpiryConfig(config ExpiryProcessorConfig) error {
	if !config.Enabled {
		return nil // Disabled config is always valid
	}

	if config.ScanInterval > 0 && config.ScanInterval < 1*time.Second {
		return fmt.Errorf("ExpiryProcessorConfig.ScanInterval must be at least 1s, got %v "+
			"(use 0 for default of 10s, or set explicit value >= 1s)", config.ScanInterval)
	}

	if config.BatchSize < 0 {
		return fmt.Errorf("ExpiryProcessorConfig.BatchSize cannot be negative, got %d "+
			"(use 0 for default of 100, or set explicit positive value)", config.BatchSize)
	}

	if config.BatchSize > 10000 {
		return fmt.Errorf("ExpiryProcessorConfig.BatchSize exceeds maximum of 10000, got %d "+
			"(large batch sizes can cause memory issues and Redis timeouts)", config.BatchSize)
	}

	// Validate DeliverySemantics
	switch config.DeliverySemantics {
	case "", DeliveryAtMostOnce, DeliveryAtLeastOnce:
		// Valid (empty defaults to at_most_once)
	default:
		return fmt.Errorf("ExpiryProcessorConfig.DeliverySemantics has invalid value %q "+
			"(valid values: %q, %q)", config.DeliverySemantics, DeliveryAtMostOnce, DeliveryAtLeastOnce)
	}

	return nil
}

// StartExpiryProcessor starts the background goroutine that processes expired checkpoints.
// The AGENT calls this method during setup - the framework just provides the implementation.
func (s *RedisCheckpointStore) StartExpiryProcessor(ctx context.Context, config ExpiryProcessorConfig) error {
	// Fail-fast for configuration errors
	if err := validateExpiryConfig(config); err != nil {
		return fmt.Errorf("invalid expiry processor configuration: %w", err)
	}

	s.expiryMu.Lock()
	defer s.expiryMu.Unlock()

	if s.expiryStarted {
		return fmt.Errorf("expiry processor already started")
	}

	if !config.Enabled {
		return nil
	}

	// Apply defaults
	if config.ScanInterval == 0 {
		config.ScanInterval = 10 * time.Second
	}
	if config.BatchSize == 0 {
		config.BatchSize = 100
	}

	s.config = config
	s.expiryCtx, s.expiryCancel = context.WithCancel(ctx)
	s.expiryStarted = true

	// Only generate instance ID if not already set (e.g., via WithInstanceID)
	if s.instanceID == "" {
		s.instanceID = generateInstanceID()
	}

	s.expiryWg.Add(1)
	go s.expiryProcessorLoop()

	if s.logger != nil {
		s.logger.Info("HITL expiry processor started", map[string]interface{}{
			"operation":     "hitl_expiry_processor_start",
			"scan_interval": config.ScanInterval.String(),
			"batch_size":    config.BatchSize,
			"key_prefix":    s.keyPrefix,
			"instance_id":   s.instanceID,
		})
	}

	return nil
}

// StopExpiryProcessor stops the expiry processor gracefully.
func (s *RedisCheckpointStore) StopExpiryProcessor(ctx context.Context) error {
	s.expiryMu.Lock()
	cancel := s.expiryCancel
	s.expiryMu.Unlock()

	if cancel == nil {
		return nil
	}

	cancel()

	// Wait with context timeout
	done := make(chan struct{})
	go func() {
		s.expiryWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if s.logger != nil {
			s.logger.Info("HITL expiry processor stopped", map[string]interface{}{
				"operation": "hitl_expiry_processor_stop",
			})
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("expiry processor shutdown cancelled: %w", ctx.Err())
	}
}

// SetExpiryCallback sets the callback for expired checkpoints.
// Must be called before StartExpiryProcessor.
func (s *RedisCheckpointStore) SetExpiryCallback(callback ExpiryCallback) error {
	s.expiryMu.Lock()
	defer s.expiryMu.Unlock()

	if s.expiryStarted {
		return fmt.Errorf("SetExpiryCallback must be called before StartExpiryProcessor")
	}
	s.expiryCallback = callback
	return nil
}

// expiryProcessorLoop runs the background expiry processor.
func (s *RedisCheckpointStore) expiryProcessorLoop() {
	defer s.expiryWg.Done()

	ticker := time.NewTicker(s.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.processExpiredCheckpoints()
		case <-s.expiryCtx.Done():
			return
		}
	}
}

// processExpiredCheckpoints processes all expired checkpoints.
func (s *RedisCheckpointStore) processExpiredCheckpoints() {
	// Track scan duration for metrics
	scanStartTime := time.Now()

	ctx, cancel := context.WithTimeout(s.expiryCtx, 30*time.Second)
	defer cancel()

	// Get all checkpoint IDs from pending index
	pendingKey := fmt.Sprintf("%s:pending", s.keyPrefix)
	checkpointIDs, err := s.client.SMembers(ctx, pendingKey).Result()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if s.logger != nil {
			s.logger.WarnWithContext(ctx, "Failed to read pending index", map[string]interface{}{
				"operation": "hitl_expiry_processor",
				"error":     err.Error(),
			})
		}
		RecordExpiryScanSkipped("read_pending_index_failed")
		return
	}

	now := time.Now()
	processed := 0

	for _, cpID := range checkpointIDs {
		if processed >= s.config.BatchSize {
			break
		}

		checkpoint, err := s.LoadCheckpoint(ctx, cpID)
		if err != nil {
			// Checkpoint may have been deleted (TTL) - remove from index
			s.client.SRem(ctx, pendingKey, cpID)
			continue
		}

		// Check if expired
		if checkpoint.Status != CheckpointStatusPending || checkpoint.ExpiresAt.After(now) {
			continue
		}

		// ┌────────────────────────────────────────────────────────────────┐
		// │  DISTRIBUTED CLAIM MECHANISM                                   │
		// │                                                                │
		// │  In multi-pod deployments, multiple expiry processors may be   │
		// │  running. We use Redis SETNX to ensure only ONE pod processes  │
		// │  each checkpoint. Per FRAMEWORK_DESIGN_PRINCIPLES.md, this is  │
		// │  built-in reliability, not a documented limitation.            │
		// └────────────────────────────────────────────────────────────────┘

		claimed, err := s.claimExpiredCheckpoint(ctx, cpID)
		if err != nil {
			telemetry.RecordSpanError(ctx, err)
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to claim checkpoint", map[string]interface{}{
					"operation":     "hitl_expiry_processor",
					"checkpoint_id": cpID,
					"error":         err.Error(),
				})
			}
			continue
		}

		if !claimed {
			// Another instance is processing this checkpoint
			if s.logger != nil {
				s.logger.DebugWithContext(ctx, "Checkpoint claimed by another instance", map[string]interface{}{
					"operation":     "hitl_expiry_skip",
					"checkpoint_id": cpID,
				})
			}
			continue
		}

		// Process this expired checkpoint
		s.processExpiredCheckpoint(ctx, checkpoint)

		// Release claim after processing
		if err := s.releaseExpiredCheckpointClaim(ctx, cpID); err != nil {
			// Non-fatal - claim will expire via TTL
			telemetry.RecordSpanError(ctx, err)
		}

		processed++
	}

	// Record scan metrics
	scanDuration := time.Since(scanStartTime).Seconds()
	RecordExpiryScanDuration(scanDuration)
	RecordExpiryBatchSize(processed)

	if processed > 0 && s.logger != nil {
		s.logger.DebugWithContext(ctx, "Expiry processor scan complete", map[string]interface{}{
			"operation":     "hitl_expiry_processor",
			"processed":     processed,
			"total_pending": len(checkpointIDs),
			"duration_ms":   scanDuration * 1000,
		})
	}
}

// processExpiredCheckpoint handles a single expired checkpoint.
func (s *RedisCheckpointStore) processExpiredCheckpoint(ctx context.Context, checkpoint *ExecutionCheckpoint) {
	var newStatus CheckpointStatus
	var appliedAction CommandType

	// ┌────────────────────────────────────────────────────────────────────┐
	// │  TRACE CORRELATION: Extract original trace context from checkpoint │
	// │  RC7-B5: Read typed fields first; fall back to UserContext for    │
	// │  checkpoints created before RC7-B2 was deployed.                  │
	// └────────────────────────────────────────────────────────────────────┘
	originalTraceID := checkpoint.OriginalTraceID
	originalSpanID := checkpoint.OriginalSpanID
	if originalTraceID == "" && checkpoint.UserContext != nil {
		if tid, ok := checkpoint.UserContext["original_trace_id"].(string); ok {
			originalTraceID = tid
		}
		if sid, ok := checkpoint.UserContext["original_span_id"].(string); ok {
			originalSpanID = sid
		}
	}

	// Create linked span for cross-trace correlation in distributed tracing backends
	// This allows searching by original_trace_id or original_request_id to find both
	// the original request and all expiry-related processing
	ctx, endLinkedSpan := telemetry.StartLinkedSpan(
		ctx,
		"hitl.expiry_process",
		originalTraceID,
		originalSpanID,
		map[string]string{
			"checkpoint_id":       checkpoint.CheckpointID,
			"request_id":          checkpoint.RequestID,
			"original_request_id": checkpoint.OriginalRequestID,
			"original_trace_id":   originalTraceID,
			"link.type":           "hitl_expiry",
			"trigger":             "expiry_processor",
		},
	)
	defer endLinkedSpan()

	// Get effective request mode (with observable default behavior)
	effectiveMode := s.getEffectiveRequestMode(ctx, checkpoint)

	// Determine expiry behavior based on mode
	shouldApplyDefault := s.shouldApplyDefaultAction(checkpoint, effectiveMode)

	if !shouldApplyDefault {
		// IMPLICIT DENY: No action applied
		newStatus = CheckpointStatusExpired
		appliedAction = "" // Explicitly no action

		if s.logger != nil {
			s.logger.InfoWithContext(ctx, "Checkpoint expired (implicit deny)", map[string]interface{}{
				"operation":         "hitl_expiry_processor",
				"checkpoint_id":     checkpoint.CheckpointID,
				"request_id":        checkpoint.RequestID,
				"original_trace_id": originalTraceID,
				"request_mode":      string(effectiveMode),
				"expired_at":        checkpoint.ExpiresAt.Format(time.RFC3339),
			})
		}

		telemetry.AddSpanEvent(ctx, "hitl.checkpoint.expired",
			attribute.String("request_id", checkpoint.RequestID),
			attribute.String("checkpoint_id", checkpoint.CheckpointID),
			attribute.String("original_trace_id", originalTraceID),
			attribute.String("request_mode", string(effectiveMode)),
			attribute.String("action", "implicit_deny"),
			attribute.String("new_status", string(newStatus)),
		)

		RecordCheckpointExpired("implicit_deny", string(effectiveMode), checkpoint.InterruptPoint)
	} else {
		// APPLY DEFAULT ACTION
		appliedAction = s.determineExpiryAction(checkpoint)
		newStatus = s.actionToExpiredStatus(appliedAction)

		if s.logger != nil {
			s.logger.InfoWithContext(ctx, "Checkpoint expired, applied default action", map[string]interface{}{
				"operation":         "hitl_expiry_processor",
				"checkpoint_id":     checkpoint.CheckpointID,
				"request_id":        checkpoint.RequestID,
				"original_trace_id": originalTraceID,
				"request_mode":      string(effectiveMode),
				"default_action":    string(appliedAction),
				"new_status":        string(newStatus),
				"expired_at":        checkpoint.ExpiresAt.Format(time.RFC3339),
			})
		}

		telemetry.AddSpanEvent(ctx, "hitl.checkpoint.expired",
			attribute.String("request_id", checkpoint.RequestID),
			attribute.String("checkpoint_id", checkpoint.CheckpointID),
			attribute.String("original_trace_id", originalTraceID),
			attribute.String("request_mode", string(effectiveMode)),
			attribute.String("action", string(appliedAction)),
			attribute.String("new_status", string(newStatus)),
		)

		RecordCheckpointExpired(string(appliedAction), string(effectiveMode), checkpoint.InterruptPoint)
	}

	// Get callback (if set)
	s.expiryMu.Lock()
	callback := s.expiryCallback
	deliverySemantics := s.config.DeliverySemantics
	s.expiryMu.Unlock()

	// Handle delivery semantics
	// Default to at-most-once if not specified
	if deliverySemantics == "" {
		deliverySemantics = DeliveryAtMostOnce
	}

	switch deliverySemantics {
	case DeliveryAtLeastOnce:
		// ┌────────────────────────────────────────────────────────────────┐
		// │  AT-LEAST-ONCE: Callback FIRST, then update status             │
		// │                                                                │
		// │  If callback panics, checkpoint remains "pending" and will be  │
		// │  retried on next scan. Use for critical operations.            │
		// │  WARNING: Callback MUST be idempotent!                         │
		// └────────────────────────────────────────────────────────────────┘

		if callback != nil {
			// Create a copy for the callback so we don't mutate the original
			callbackCp := *checkpoint
			callbackCp.Status = newStatus
			callbackSuccess := s.invokeCallbackSafely(ctx, &callbackCp, appliedAction, callback)

			if !callbackSuccess {
				// Callback panicked - don't update status, will retry on next scan
				if s.logger != nil {
					s.logger.WarnWithContext(ctx, "Callback failed, checkpoint will be retried", map[string]interface{}{
						"operation":          "hitl_expiry_at_least_once",
						"checkpoint_id":      checkpoint.CheckpointID,
						"request_id":         checkpoint.RequestID,
						"delivery_semantics": string(deliverySemantics),
					})
				}
				return
			}
		}

		// Callback succeeded (or no callback) - now update status
		if err := s.UpdateCheckpointStatus(ctx, checkpoint.CheckpointID, newStatus); err != nil {
			telemetry.RecordSpanError(ctx, err)
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to update expired checkpoint after successful callback", map[string]interface{}{
					"operation":     "hitl_expiry_processor",
					"checkpoint_id": checkpoint.CheckpointID,
					"request_id":    checkpoint.RequestID,
					"error":         err.Error(),
				})
			}
		}

	default: // DeliveryAtMostOnce (default)
		// ┌────────────────────────────────────────────────────────────────┐
		// │  AT-MOST-ONCE: Update status FIRST, then callback              │
		// │                                                                │
		// │  If callback panics, checkpoint is already marked processed.   │
		// │  Safest option for notifications and fire-and-forget.          │
		// └────────────────────────────────────────────────────────────────┘

		// Update checkpoint status (removes from pending index)
		if err := s.UpdateCheckpointStatus(ctx, checkpoint.CheckpointID, newStatus); err != nil {
			telemetry.RecordSpanError(ctx, err)
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Failed to update expired checkpoint", map[string]interface{}{
					"operation":     "hitl_expiry_processor",
					"checkpoint_id": checkpoint.CheckpointID,
					"request_id":    checkpoint.RequestID,
					"error":         err.Error(),
				})
			}
			return
		}

		// Invoke callback if set (with panic recovery)
		if callback != nil {
			checkpoint.Status = newStatus
			s.invokeCallbackSafely(ctx, checkpoint, appliedAction, callback)
		}
	}
}

// invokeCallbackSafely invokes the expiry callback with panic recovery.
// Returns true if callback completed without panic, false if it panicked.
// For at-least-once delivery, this return value determines if status should be updated.
func (s *RedisCheckpointStore) invokeCallbackSafely(ctx context.Context, checkpoint *ExecutionCheckpoint, appliedAction CommandType, callback ExpiryCallback) (success bool) {
	defer func() {
		if r := recover(); r != nil {
			success = false
			RecordCallbackPanic()
			if s.logger != nil {
				s.logger.ErrorWithContext(ctx, "Expiry callback panicked", map[string]interface{}{
					"operation":     "hitl_expiry_callback",
					"checkpoint_id": checkpoint.CheckpointID,
					"request_id":    checkpoint.RequestID,
					"panic":         fmt.Sprintf("%v", r),
					"stack":         string(debug.Stack()),
				})
			}
		}
	}()

	callback(ctx, checkpoint, appliedAction)
	return true
}

// getEffectiveRequestMode returns the request mode, using default if not set.
func (s *RedisCheckpointStore) getEffectiveRequestMode(ctx context.Context, checkpoint *ExecutionCheckpoint) RequestMode {
	if checkpoint.RequestMode != "" {
		return checkpoint.RequestMode
	}

	// Use configured default
	defaultMode := s.getDefaultRequestMode(checkpoint)

	// Log warning when using default
	if s.logger != nil {
		s.logger.WarnWithContext(ctx, "RequestMode not set, using default behavior", map[string]interface{}{
			"operation":            "hitl_expiry_processor",
			"checkpoint_id":        checkpoint.CheckpointID,
			"request_id":           checkpoint.RequestID,
			"default_request_mode": string(defaultMode),
		})
	}

	telemetry.AddSpanEvent(ctx, "hitl.request_mode.default_used",
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("checkpoint_id", checkpoint.CheckpointID),
		attribute.String("default_request_mode", string(defaultMode)),
	)

	return defaultMode
}

// getDefaultRequestMode returns the configured default request mode.
func (s *RedisCheckpointStore) getDefaultRequestMode(checkpoint *ExecutionCheckpoint) RequestMode {
	// Check checkpoint's decision first
	if checkpoint.Decision != nil && checkpoint.Decision.DefaultRequestMode != "" {
		return checkpoint.Decision.DefaultRequestMode
	}

	// Check environment variable
	if envMode := os.Getenv("TRUVAG3_HITL_DEFAULT_REQUEST_MODE"); envMode != "" {
		switch envMode {
		case "streaming":
			return RequestModeStreaming
		case "non_streaming":
			return RequestModeNonStreaming
		}
	}

	// Default to non_streaming
	return RequestModeNonStreaming
}

// shouldApplyDefaultAction determines whether to apply DefaultAction or use implicit deny.
func (s *RedisCheckpointStore) shouldApplyDefaultAction(checkpoint *ExecutionCheckpoint, mode RequestMode) bool {
	if mode == RequestModeStreaming {
		behavior := s.getStreamingExpiryBehavior(checkpoint)
		return behavior == StreamingExpiryApplyDefault
	}
	behavior := s.getNonStreamingExpiryBehavior(checkpoint)
	return behavior == NonStreamingExpiryApplyDefault
}

// getStreamingExpiryBehavior returns the configured streaming expiry behavior.
func (s *RedisCheckpointStore) getStreamingExpiryBehavior(checkpoint *ExecutionCheckpoint) StreamingExpiryBehavior {
	// Check checkpoint's decision first
	if checkpoint.Decision != nil && checkpoint.Decision.StreamingExpiryBehavior != "" {
		return checkpoint.Decision.StreamingExpiryBehavior
	}

	// Check environment variable
	if envBehavior := os.Getenv("TRUVAG3_HITL_STREAMING_EXPIRY"); envBehavior != "" {
		switch envBehavior {
		case "apply_default":
			return StreamingExpiryApplyDefault
		case "implicit_deny":
			return StreamingExpiryImplicitDeny
		}
	}

	// Default to implicit_deny for streaming
	return StreamingExpiryImplicitDeny
}

// getNonStreamingExpiryBehavior returns the configured non-streaming expiry behavior.
func (s *RedisCheckpointStore) getNonStreamingExpiryBehavior(checkpoint *ExecutionCheckpoint) NonStreamingExpiryBehavior {
	// Check checkpoint's decision first
	if checkpoint.Decision != nil && checkpoint.Decision.NonStreamingExpiryBehavior != "" {
		return checkpoint.Decision.NonStreamingExpiryBehavior
	}

	// Check environment variable
	if envBehavior := os.Getenv("TRUVAG3_HITL_NON_STREAMING_EXPIRY"); envBehavior != "" {
		switch envBehavior {
		case "implicit_deny":
			return NonStreamingExpiryImplicitDeny
		case "apply_default":
			return NonStreamingExpiryApplyDefault
		}
	}

	// Default to apply_default for non-streaming
	return NonStreamingExpiryApplyDefault
}

// determineExpiryAction determines what action to apply on expiry.
// Design Decision (2026-01-24): HITL enabled = require explicit approval.
// All checkpoints default to reject on expiry (fail-safe), except errors which abort.
func (s *RedisCheckpointStore) determineExpiryAction(checkpoint *ExecutionCheckpoint) CommandType {
	// Use DefaultAction from the checkpoint's decision (set by policy)
	if checkpoint.Decision != nil && checkpoint.Decision.DefaultAction != "" {
		return checkpoint.Decision.DefaultAction
	}

	// Fallback: HITL enabled = require explicit approval, so reject on expiry
	switch checkpoint.InterruptPoint {
	case InterruptPointOnError:
		return CommandAbort // Errors require human attention
	default:
		return CommandReject // All other checkpoints fail-safe to reject
	}
}

// actionToExpiredStatus converts an action to the appropriate expired status.
func (s *RedisCheckpointStore) actionToExpiredStatus(action CommandType) CheckpointStatus {
	switch action {
	case CommandApprove:
		return CheckpointStatusExpiredApproved
	case CommandReject:
		return CheckpointStatusExpiredRejected
	case CommandAbort:
		return CheckpointStatusExpiredAborted
	default:
		return CheckpointStatusExpiredRejected
	}
}

// generateInstanceID creates a unique identifier for this instance.
func generateInstanceID() string {
	hostname, _ := os.Hostname()
	return fmt.Sprintf("%s-%s", hostname, uuid.New().String()[:8])
}

// -----------------------------------------------------------------------------
// Distributed Claim Mechanism (Multi-Pod Safety)
// -----------------------------------------------------------------------------

// claimExpiredCheckpoint attempts to claim exclusive processing rights for a checkpoint.
// Returns true if this instance successfully claimed it, false if another instance claimed it.
//
// Uses Redis SETNX with TTL for distributed locking:
//   - Key: {keyPrefix}:expiry:claim:{checkpointID}
//   - Value: instanceID (unique per pod)
//   - TTL: 30 seconds (prevents orphaned claims if pod crashes)
//
// This ensures that in a multi-pod deployment, only ONE pod processes each expired checkpoint.
func (s *RedisCheckpointStore) claimExpiredCheckpoint(ctx context.Context, checkpointID string) (bool, error) {
	claimKey := fmt.Sprintf("%s:expiry:claim:%s", s.keyPrefix, checkpointID)
	claimTTL := 30 * time.Second

	// SETNX with TTL - only succeeds if key doesn't exist
	success, err := s.client.SetNX(ctx, claimKey, s.instanceID, claimTTL).Result()
	if err != nil {
		return false, fmt.Errorf("failed to claim checkpoint %s via Redis SETNX at key %s: %w (check Redis connectivity and permissions)",
			checkpointID, claimKey, err)
	}

	if success {
		RecordClaimSuccess()
		if s.logger != nil {
			s.logger.DebugWithContext(ctx, "Claimed expired checkpoint", map[string]interface{}{
				"operation":     "hitl_expiry_claim",
				"checkpoint_id": checkpointID,
				"instance_id":   s.instanceID,
			})
		}
	} else {
		RecordClaimSkipped()
	}

	return success, nil
}

// releaseExpiredCheckpointClaim releases the claim after processing.
// Only releases if this instance holds the claim (atomic check-and-delete using Lua script).
func (s *RedisCheckpointStore) releaseExpiredCheckpointClaim(ctx context.Context, checkpointID string) error {
	claimKey := fmt.Sprintf("%s:expiry:claim:%s", s.keyPrefix, checkpointID)

	// Use Lua script for atomic check-and-delete
	// Only delete if the value matches our instance ID
	script := `
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("DEL", KEYS[1])
		end
		return 0
	`
	_, err := s.client.Eval(ctx, script, []string{claimKey}, s.instanceID).Result()
	if err != nil {
		if s.logger != nil {
			s.logger.WarnWithContext(ctx, "Failed to release checkpoint claim", map[string]interface{}{
				"operation":     "hitl_expiry_claim_release",
				"checkpoint_id": checkpointID,
				"instance_id":   s.instanceID,
				"error":         err.Error(),
			})
		}
		return fmt.Errorf("failed to release claim for checkpoint %s: %w", checkpointID, err)
	}
	return nil
}

// Compile-time interface compliance check
var _ CheckpointStore = (*RedisCheckpointStore)(nil)
