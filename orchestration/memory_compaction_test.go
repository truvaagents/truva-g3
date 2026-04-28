package orchestration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Constructor Tests ---

func TestNewMemoryCompactor_FailFast(t *testing.T) {
	reflector := &core.NoOpMemoryReflector{}
	episodic := &core.MockEpisodicMemory{}

	t.Run("nil reflector rejected", func(t *testing.T) {
		_, err := NewMemoryCompactor(nil, episodic, "default")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "reflector is required")
	})

	t.Run("nil episodic rejected", func(t *testing.T) {
		_, err := NewMemoryCompactor(reflector, nil, "default")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "episodic memory is required")
	})

	t.Run("valid construction", func(t *testing.T) {
		c, err := NewMemoryCompactor(reflector, episodic, "infrastructure")
		require.NoError(t, err)
		assert.NotNil(t, c)
	})

	t.Run("empty domain defaults", func(t *testing.T) {
		c, err := NewMemoryCompactor(reflector, episodic, "")
		require.NoError(t, err)
		assert.Equal(t, "default", c.domain)
	})
}

// --- ReflectEntity Tests ---

func TestReflectEntity_ReturnsFragments(t *testing.T) {
	var reflectedEntities []string
	reflector := &core.MockMemoryReflector{
		ReflectFn: func(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
			reflectedEntities = append(reflectedEntities, entityType+"/"+entityID)
			return []core.KnowledgeFragment{
				{Content: "OOMKill pattern detected", Namespace: "incidents", Importance: 8.0},
			}, nil
		},
	}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "infrastructure")

	fragments, err := c.ReflectEntity(context.Background(), "pod", "pod-1", time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, fragments, 1)
	assert.Equal(t, "OOMKill pattern detected", fragments[0].Content)
	assert.Contains(t, reflectedEntities, "pod/pod-1")
}

func TestReflectEntity_EmbedsAndStoresWhenKnowledgeConfigured(t *testing.T) {
	reflector := &core.MockMemoryReflector{
		ReflectFn: func(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
			return []core.KnowledgeFragment{
				{Content: "test pattern", Namespace: "patterns", Importance: 6.0, AgentDomain: "infra", Scope: core.ScopeSharedDomain},
			}, nil
		},
	}

	var storedFragments []core.KnowledgeFragment
	knowledge := &core.MockSharedKnowledge{
		StoreFn: func(ctx context.Context, fragment core.KnowledgeFragment) error {
			storedFragments = append(storedFragments, fragment)
			return nil
		},
	}

	embedder := &core.MockEmbeddingClient{
		GenerateEmbedFn: func(ctx context.Context, texts []string, options *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
			return &core.EmbeddingResponse{
				Embeddings: [][]float32{{0.1, 0.2, 0.3}},
			}, nil
		},
	}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "infra",
		WithCompactorKnowledge(knowledge, embedder),
	)

	fragments, err := c.ReflectEntity(context.Background(), "pod", "pod-1", time.Time{})
	require.NoError(t, err)
	require.Len(t, fragments, 1)
	require.Len(t, storedFragments, 1)
	assert.Equal(t, "test pattern", storedFragments[0].Content)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, storedFragments[0].Embedding, "should be embedded before storing")
}

func TestReflectEntity_NoKnowledgeStore(t *testing.T) {
	reflector := &core.MockMemoryReflector{
		ReflectFn: func(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
			return []core.KnowledgeFragment{{Content: "pattern"}}, nil
		},
	}

	// No knowledge store configured — fragments returned but not stored
	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "default")

	fragments, err := c.ReflectEntity(context.Background(), "pod", "pod-1", time.Time{})
	require.NoError(t, err)
	require.Len(t, fragments, 1, "fragments returned even without knowledge store")
}

func TestReflectEntity_FailOpen_ReflectionError(t *testing.T) {
	reflector := &core.MockMemoryReflector{
		ReflectFn: func(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
			return nil, fmt.Errorf("LLM unavailable")
		},
	}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "default")

	fragments, err := c.ReflectEntity(context.Background(), "pod", "pod-1", time.Time{})
	assert.NoError(t, err, "should not propagate errors — fail-open")
	assert.Nil(t, fragments)
}

func TestReflectEntity_FailOpen_EmbeddingError(t *testing.T) {
	reflector := &core.MockMemoryReflector{
		ReflectFn: func(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
			return []core.KnowledgeFragment{{Content: "pattern", AgentDomain: "infra", Scope: core.ScopeSharedDomain}}, nil
		},
	}
	embedder := &core.MockEmbeddingClient{
		GenerateEmbedFn: func(ctx context.Context, texts []string, options *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
			return nil, fmt.Errorf("embedding service down")
		},
	}
	knowledge := &core.MockSharedKnowledge{}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "infra",
		WithCompactorKnowledge(knowledge, embedder),
	)

	fragments, err := c.ReflectEntity(context.Background(), "pod", "pod-1", time.Time{})
	assert.NoError(t, err)
	assert.Len(t, fragments, 1, "fragments returned even when embedding fails")
	assert.Equal(t, 0, knowledge.StoreCt, "should not store without embedding")
}

// --- RunCompaction Tests ---

func TestRunCompaction_DelegatesAndSucceeds(t *testing.T) {
	var compactCalled bool
	reflector := &core.MockMemoryReflector{
		CompactFn: func(ctx context.Context, config core.CompactionConfig) error {
			compactCalled = true
			return nil
		},
	}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "default",
		WithCompactorEnabled(true),
	)
	err := c.RunCompaction(context.Background(), core.CompactionConfig{
		EventAgeThreshold:   7 * 24 * time.Hour,
		ImportanceThreshold: 2.0,
	})
	assert.NoError(t, err)
	assert.True(t, compactCalled)
}

func TestRunCompaction_FailOpen(t *testing.T) {
	reflector := &core.MockMemoryReflector{
		CompactFn: func(ctx context.Context, config core.CompactionConfig) error {
			return fmt.Errorf("compaction error")
		},
	}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "default",
		WithCompactorEnabled(true),
	)
	err := c.RunCompaction(context.Background(), core.CompactionConfig{})
	assert.NoError(t, err, "compaction errors should not propagate — fail-open")
}

func TestRunCompaction_DryRun(t *testing.T) {
	reflector := &core.MockMemoryReflector{
		CompactFn: func(ctx context.Context, config core.CompactionConfig) error {
			assert.True(t, config.DryRun, "dry run flag should be passed through")
			return nil
		},
	}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "default",
		WithCompactorEnabled(true),
	)
	err := c.RunCompaction(context.Background(), core.CompactionConfig{DryRun: true})
	assert.NoError(t, err)
}

func TestRunCompaction_DisabledByDefault(t *testing.T) {
	compactCalled := false
	reflector := &core.MockMemoryReflector{
		CompactFn: func(ctx context.Context, config core.CompactionConfig) error {
			compactCalled = true
			return nil
		},
	}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "default")
	// compactionEnabled defaults to false
	err := c.RunCompaction(context.Background(), core.CompactionConfig{})
	assert.NoError(t, err)
	assert.False(t, compactCalled, "compaction should not run when disabled")
}

func TestRunCompaction_EnabledExplicitly(t *testing.T) {
	compactCalled := false
	reflector := &core.MockMemoryReflector{
		CompactFn: func(ctx context.Context, config core.CompactionConfig) error {
			compactCalled = true
			return nil
		},
	}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "default",
		WithCompactorEnabled(true),
	)
	err := c.RunCompaction(context.Background(), core.CompactionConfig{})
	assert.NoError(t, err)
	assert.True(t, compactCalled, "compaction should run when enabled")
}

func TestReflectEntity_StoreKnowledgeError_FailOpen(t *testing.T) {
	reflector := &core.MockMemoryReflector{
		ReflectFn: func(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
			return []core.KnowledgeFragment{{Content: "pattern", AgentDomain: "infra", Scope: core.ScopeSharedDomain}}, nil
		},
	}
	embedder := &core.MockEmbeddingClient{
		GenerateEmbedFn: func(ctx context.Context, texts []string, options *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
			return &core.EmbeddingResponse{Embeddings: [][]float32{{0.1, 0.2}}}, nil
		},
	}
	knowledge := &core.MockSharedKnowledge{
		StoreFn: func(ctx context.Context, fragment core.KnowledgeFragment) error {
			return fmt.Errorf("Qdrant unavailable")
		},
	}

	c, _ := NewMemoryCompactor(reflector, &core.MockEpisodicMemory{}, "infra",
		WithCompactorKnowledge(knowledge, embedder),
	)

	fragments, err := c.ReflectEntity(context.Background(), "pod", "pod-1", time.Time{})
	assert.NoError(t, err, "store failure should not propagate — fail-open")
	assert.Len(t, fragments, 1, "fragments returned even when store fails")
	assert.Equal(t, 1, knowledge.StoreCt, "store should have been attempted")
}
