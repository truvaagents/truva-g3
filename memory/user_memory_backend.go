package memory

import (
	"os"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// UserMemoryBackend wraps a UserMemory implementation with lifecycle management.
// Created by NewUserMemoryBackend. Provides ToDeps() for BuildUserMemoryHooks.
//
// Follows the same pattern as SharedBackends: factory creates backends,
// application passes ToDeps() to orchestration hooks.
type UserMemoryBackend struct {
	deps   core.UserMemoryDeps
	closer func()
	logger core.Logger
}

// UserMemoryBackendOption configures NewUserMemoryBackend.
type UserMemoryBackendOption func(*userMemoryBackendConfig) error

type userMemoryBackendConfig struct {
	namespace  string
	embedder   core.EmbeddingClient
	vectorOpts []Option // VectorConfig options passed through to VectorUserMemory
}

const defaultTransientMaxAgeHours = 168

// WithUserMemoryNamespace sets the agent type namespace (e.g., "travel", "devops").
func WithUserMemoryNamespace(ns string) UserMemoryBackendOption {
	return func(c *userMemoryBackendConfig) error {
		c.namespace = ns
		return nil
	}
}

// WithUserMemoryEmbeddingClient sets the embedding client for vector-backed backends.
// Required for VectorUserMemory. If nil, factory falls back to InMemoryUserMemory.
// The memory module does not import ai — the client is a core.EmbeddingClient interface.
func WithUserMemoryEmbeddingClient(e core.EmbeddingClient) UserMemoryBackendOption {
	return func(c *userMemoryBackendConfig) error {
		c.embedder = e
		return nil
	}
}

// WithUserMemoryVectorOption passes a VectorConfig option through to the vector backend.
// Use for fine-tuning: WithLogger, WithTelemetry, WithCollectionName, WithVectorSize, etc.
func WithUserMemoryVectorOption(opt Option) UserMemoryBackendOption {
	return func(c *userMemoryBackendConfig) error {
		c.vectorOpts = append(c.vectorOpts, opt)
		return nil
	}
}

// NewUserMemoryBackend creates a UserMemory backend with auto-detection.
// If TRUVAG3_VECTOR_DB_URL is set AND an embedding client is provided → VectorUserMemory.
// Otherwise → InMemoryUserMemory.
//
// Configuration precedence: explicit options > env vars > defaults.
// Same pattern as NewSharedBackends.
func NewUserMemoryBackend(logger core.Logger, opts ...UserMemoryBackendOption) (*UserMemoryBackend, error) {
	if logger == nil {
		logger = &core.NoOpLogger{}
	}

	cfg := &userMemoryBackendConfig{
		namespace: "default",
	}

	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}

	var userMem core.UserMemory
	var closer func()

	vectorDBURL := os.Getenv("TRUVAG3_VECTOR_DB_URL")
	if vectorDBURL != "" && cfg.embedder != nil {
		// Vector backend available — pass logger via VectorConfig option
		allOpts := append([]Option{WithLogger(logger)}, cfg.vectorOpts...)
		vum, err := NewVectorUserMemory(cfg.embedder, allOpts...)
		if err != nil {
			// Degrade to in-memory with warning (per FRAMEWORK_DESIGN_PRINCIPLES §Fail-Safe Defaults)
			if logger != nil {
				logger.Warn("Vector DB unavailable for user memory, falling back to in-memory", map[string]interface{}{
					"operation": "new_user_memory_backend",
					"error":     err.Error(),
				})
			}
			userMem = NewInMemoryUserMemory(0)
		} else {
			userMem = vum
			closer = func() { _ = vum.Close() }
		}
	} else {
		userMem = NewInMemoryUserMemory(0)
	}

	return &UserMemoryBackend{
		deps: core.UserMemoryDeps{
			UserMemory: userMem,
			Embedder:   cfg.embedder,
			Namespace:  cfg.namespace,
		},
		closer: closer,
		logger: logger,
	}, nil
}

// ToDeps returns the deps struct for BuildUserMemoryHooks.
// Returns a copy to prevent external modification of internal state.
func (b *UserMemoryBackend) ToDeps() *core.UserMemoryDeps {
	if b == nil {
		return nil
	}
	cpy := b.deps
	return &cpy
}

// Close releases backend resources (vector DB connection if applicable).
func (b *UserMemoryBackend) Close() {
	if b != nil && b.closer != nil {
		b.closer()
	}
}

func transientMaxAgeFromEnv() time.Duration {
	hours := defaultTransientMaxAgeHours
	if v, _ := strconv.Atoi(os.Getenv("TRUVAG3_USER_MEMORY_TRANSIENT_MAX_AGE_HOURS")); v > 0 {
		hours = v
	}
	return time.Duration(hours) * time.Hour
}

func shouldFilterTransientOnRecall(fact core.UserFact, now time.Time, transientMaxAge time.Duration) bool {
	if core.EffectiveUserFactLifetime(fact) != core.UserFactLifetimeTransient {
		return false
	}
	if fact.UpdatedAt.IsZero() {
		return false
	}
	if transientMaxAge <= 0 {
		transientMaxAge = time.Duration(defaultTransientMaxAgeHours) * time.Hour
	}
	return fact.UpdatedAt.Before(now.Add(-transientMaxAge))
}

func filterRecalledFactsByLifetime(facts []core.UserFact, now time.Time, transientMaxAge time.Duration) ([]core.UserFact, int) {
	if len(facts) == 0 {
		return nil, 0
	}

	filtered := make([]core.UserFact, 0, len(facts))
	filteredCount := 0
	for _, fact := range facts {
		if shouldFilterTransientOnRecall(fact, now, transientMaxAge) {
			filteredCount++
			continue
		}
		filtered = append(filtered, fact)
	}
	return filtered, filteredCount
}
