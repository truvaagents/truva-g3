package main

import (
	"fmt"
	"os"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/memory"
)

// setupBackends initializes Redis and optional Qdrant backends.
// Redis is required; Qdrant + embedder are optional (graceful degradation).
func (t *MemoryTool) setupBackends() error {
	// --- Redis (required for query_events + query_investigations) ---
	resolution, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv)
	if err != nil {
		return fmt.Errorf("resolve Redis connection: %w", err)
	}
	keyspace, err := core.NewRedisKeyspace(os.Getenv("TRUVAG3_REDIS_NAMESPACE"))
	if err != nil {
		return fmt.Errorf("resolve Redis keyspace: %w", err)
	}
	redisClient, err := core.NewRedisUniversalClient(resolution)
	if err != nil {
		return fmt.Errorf("connect Redis memory backend: %w", err)
	}
	t.backendCloser = redisClient

	episodic, err := memory.NewStreamEpisodicMemory(
		memory.WithEpisodicRedisUniversalClient(redisClient),
		memory.WithEpisodicDomain(t.domain),
		memory.WithEpisodicKeyspace(keyspace),
		memory.WithEpisodicLogger(t.Logger),
	)
	if err != nil {
		return fmt.Errorf("failed to create episodic memory: %w", err)
	}
	t.episodic = episodic

	coordinator, err := memory.NewAtomicLockCoordinator(
		memory.WithCoordinatorRedisUniversalClient(redisClient),
		memory.WithCoordinatorDomain(t.domain),
		memory.WithCoordinatorKeyspace(keyspace),
		memory.WithCoordinatorLogger(t.Logger),
	)
	if err != nil {
		return fmt.Errorf("failed to create investigation coordinator: %w", err)
	}
	t.coordinator = coordinator

	// --- Qdrant + Embedder (optional — needed for query_knowledge) ---
	embedder, err := ai.NewEmbeddingClient(
		ai.WithEmbeddingLogger(t.Logger),
	)
	if err != nil {
		if t.Logger != nil {
			t.Logger.Warn("Embedding client unavailable, query_knowledge will return empty results", map[string]interface{}{
				"error": err.Error(),
				"hint":  "set TRUVAG3_EMBEDDING_BASE_URL and TRUVAG3_EMBEDDING_MODEL",
			})
		}
		return nil // graceful degradation
	}

	knowledgeStore, err := memory.NewVectorSharedKnowledge(
		memory.WithLogger(t.Logger),
	)
	if err != nil {
		if t.Logger != nil {
			t.Logger.Warn("Vector DB unavailable, query_knowledge will return empty results", map[string]interface{}{
				"error": err.Error(),
				"hint":  "set TRUVAG3_VECTOR_DB_URL (default: localhost:6334)",
			})
		}
		return nil // graceful degradation
	}

	t.knowledge = knowledgeStore
	t.embedder = embedder

	if t.Logger != nil {
		t.Logger.Info("Memory backends initialized", map[string]interface{}{
			"domain":            t.domain,
			"episodic":          "redis_streams",
			"coordinator":       "atomic_lock",
			"knowledge_enabled": t.knowledge != nil,
			"embedding_enabled": t.embedder != nil,
		})
	}

	return nil
}
