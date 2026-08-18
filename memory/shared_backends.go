package memory

import (
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// SharedBackends holds all memory backend instances created from a single Redis client.
// Call Close() on shutdown to drain background goroutines and close connections.
//
// Usage:
//
//	backends, err := memory.NewSharedBackends(redisClient, logger,
//	    memory.WithAgentName("my-agent"),
//	    memory.WithDomain("infrastructure"),
//	)
//	defer backends.Close()
//	hooks, coord := orchestration.BuildMemoryHooks(backends.ToDeps(), aiClient, logger)
type SharedBackends struct {
	deps    core.SharedMemoryDeps
	closers []func() // shutdown functions for graceful cleanup
	logger  core.Logger
}

// sharedBackendsConfig holds resolved configuration for NewSharedBackends.
type sharedBackendsConfig struct {
	domain            string
	agentName         string
	knowledgeDisabled bool
	embedder          core.EmbeddingClient // from WithEmbeddingClient option
}

// SharedBackendsOption configures NewSharedBackends.
// Returns error for fail-fast validation (per core/ARCHITECTURE.md §Option Function Pattern).
type SharedBackendsOption func(*sharedBackendsConfig) error

// WithDomain overrides the TRUVAG3_AGENT_DOMAIN env var.
func WithDomain(domain string) SharedBackendsOption {
	return func(c *sharedBackendsConfig) error {
		if domain == "" {
			return fmt.Errorf("domain cannot be empty")
		}
		c.domain = domain
		return nil
	}
}

// WithAgentName overrides the TRUVAG3_AGENT_NAME env var.
func WithAgentName(name string) SharedBackendsOption {
	return func(c *sharedBackendsConfig) error {
		if name == "" {
			return fmt.Errorf("agent name cannot be empty")
		}
		c.agentName = name
		return nil
	}
}

// WithKnowledgeDisabled skips Phase 2 (Qdrant) even when TRUVAG3_VECTOR_DB_URL is set.
func WithKnowledgeDisabled() SharedBackendsOption {
	return func(c *sharedBackendsConfig) error {
		c.knowledgeDisabled = true
		return nil
	}
}

// WithEmbeddingClient provides the embedding client for Phase 2 knowledge search.
// Required for Phase 2 because the memory module cannot import the ai module
// (per memory/ARCHITECTURE.md §Module Dependencies).
// The application creates the client via ai.NewEmbeddingClient() and passes it here.
func WithEmbeddingClient(embedder core.EmbeddingClient) SharedBackendsOption {
	return func(c *sharedBackendsConfig) error {
		c.embedder = embedder // nil is valid — disables Phase 2
		return nil
	}
}

// NewSharedBackends creates all memory backends from a Redis client.
//
// Phase 1 (episodic + coordination + activity + digest cache) is always created.
// Phase 2 (knowledge store) is auto-detected from TRUVAG3_VECTOR_DB_URL.
// Phase 2 knowledge search requires an embedding client via WithEmbeddingClient.
//
// Configuration precedence (per FRAMEWORK_DESIGN_PRINCIPLES §Env Var Precedence):
//  1. Explicit WithXXX() options (highest)
//  2. TRUVAG3_AGENT_DOMAIN, TRUVAG3_AGENT_NAME env vars
//  3. Sensible defaults ("default" domain)
func NewSharedBackends(redisClient *redis.Client, logger core.Logger, opts ...SharedBackendsOption) (*SharedBackends, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("redis client is required for shared memory backends")
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/memory")
	}

	// 1. Defaults
	cfg := &sharedBackendsConfig{
		domain:    "default",
		agentName: os.Getenv("TRUVAG3_AGENT_NAME"), // Reuse existing env var (§3 No Duplicates)
	}

	// 2. Env vars
	if v := os.Getenv("TRUVAG3_AGENT_DOMAIN"); v != "" {
		cfg.domain = v
	}

	// 3. Explicit options (highest priority)
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return nil, fmt.Errorf("invalid shared backends option: %w", err)
		}
	}

	sb := &SharedBackends{logger: logger}

	// --- Phase 1: Episodic Memory + Coordination (Redis) ---

	// Episodic memory is the minimum requirement — fail-fast if it fails
	episodic, err := NewStreamEpisodicMemory(
		WithEpisodicRedisClient(redisClient),
		WithEpisodicDomain(cfg.domain),
		WithEpisodicLogger(logger),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create episodic memory: %w", err)
	}
	sb.deps.Episodic = episodic

	// Coordinator — optional, degrade gracefully
	coordinator, err := NewAtomicLockCoordinator(
		WithCoordinatorRedisClient(redisClient),
		WithCoordinatorDomain(cfg.domain),
		WithCoordinatorLogger(logger),
	)
	if err != nil {
		logger.Warn("Investigation coordinator unavailable, dedup disabled", map[string]interface{}{
			"operation":  "shared_backends_setup",
			"error":      err.Error(),
			"error_type": "coordinator_init",
		})
	} else {
		sb.deps.Coordinator = coordinator
	}

	// Activity coordinator — optional, degrade gracefully
	activityCoord, err := NewRedisActivityCoordinator(
		redisClient, cfg.domain,
		WithActivityCoordinatorLogger(logger),
	)
	if err != nil {
		logger.Warn("Activity coordinator unavailable, coordination disabled", map[string]interface{}{
			"operation":  "shared_backends_setup",
			"error":      err.Error(),
			"error_type": "activity_coordinator_init",
		})
	} else {
		sb.deps.ActivityCoordinator = activityCoord
	}

	// Digest cache — optional, degrade gracefully (full compaction every request)
	digestCache, err := NewRedisDigestCache(redisClient, logger)
	if err != nil {
		logger.Warn("Digest cache unavailable, full compaction every request", map[string]interface{}{
			"operation":  "shared_backends_setup",
			"error":      err.Error(),
			"error_type": "digest_cache_init",
		})
	} else {
		sb.deps.DigestCache = digestCache
	}

	// Distributed lock — optional, used by background jobs (reflection, future
	// scheduled compaction) for multi-replica safety. Without this, every replica
	// runs background jobs independently — wasteful but not incorrect.
	lock, err := NewRedisDistributedLock(redisClient, logger)
	if err != nil {
		logger.Warn("Distributed lock unavailable, background jobs will run on all replicas", map[string]interface{}{
			"operation":  "shared_backends_setup",
			"error":      err.Error(),
			"error_type": "distributed_lock_init",
		})
	} else {
		sb.deps.Lock = lock
	}

	// --- Phase 2: Knowledge + Embeddings (Qdrant + embedding endpoint) ---

	if !cfg.knowledgeDisabled && cfg.embedder != nil {
		knowledgeStore, err := NewVectorSharedKnowledge(WithLogger(logger))
		if err != nil {
			logger.Warn("Vector DB unavailable, semantic knowledge search disabled", map[string]interface{}{
				"operation":  "shared_backends_setup",
				"error":      err.Error(),
				"error_type": "knowledge_store_init",
				"hint":       "set TRUVAG3_VECTOR_DB_URL (default: localhost:6334)",
			})
		} else {
			sb.deps.Knowledge = knowledgeStore
			sb.deps.Embedder = cfg.embedder
			sb.closers = append(sb.closers, func() {
				if err := knowledgeStore.Close(); err != nil {
					sb.logger.Warn("Failed to close knowledge store", map[string]interface{}{
						"operation":  "shared_backends_shutdown",
						"error":      err.Error(),
						"error_type": "knowledge_store_close",
					})
				}
			})
		}
	}

	// Set identity
	sb.deps.AgentName = cfg.agentName
	sb.deps.AgentDomain = cfg.domain

	logger.Info("Shared memory backends initialized", map[string]interface{}{
		"operation":         "shared_backends_setup",
		"agent_name":        cfg.agentName,
		"domain":            cfg.domain,
		"episodic":          true,
		"coordinator":       sb.deps.Coordinator != nil,
		"activity_coord":    sb.deps.ActivityCoordinator != nil,
		"digest_cache":      sb.deps.DigestCache != nil,
		"distributed_lock":  sb.deps.Lock != nil,
		"knowledge_enabled": sb.deps.Knowledge != nil,
		"embedding_enabled": sb.deps.Embedder != nil,
	})

	return sb, nil
}

// ToDeps returns a copy of the SharedMemoryDeps struct for passing to orchestration.BuildMemoryHooks.
// Returns a copy to prevent external modification of internal state
// (per core/ARCHITECTURE.md §Memory Management).
func (sb *SharedBackends) ToDeps() *core.SharedMemoryDeps {
	if sb == nil {
		return nil
	}
	copy := sb.deps
	return &copy
}

// Close drains background goroutines and closes connections.
// Call from main() via defer.
func (sb *SharedBackends) Close() {
	if sb == nil {
		return
	}
	for _, closer := range sb.closers {
		closer()
	}
}
