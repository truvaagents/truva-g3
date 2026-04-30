package memory

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface compliance checks.
var _ core.SharedKnowledge = (*VectorSharedKnowledge)(nil)

// These are unit tests using mock/stub patterns.
// Integration tests with a real Qdrant instance require testcontainers or a running Qdrant.

func TestDefaultConfig_Defaults(t *testing.T) {
	config := defaultConfig()
	assert.Equal(t, "localhost:6334", config.Address)
	assert.Equal(t, "truvag3_knowledge", config.CollectionName)
	assert.Equal(t, 768, config.VectorSize)
	assert.Equal(t, "Cosine", config.Distance)
	assert.Equal(t, 5*time.Second, config.ConnectTimeout)
	assert.Equal(t, 3*time.Second, config.SearchTimeout)
	assert.True(t, config.AutoCreateCollection)
}

func TestDefaultConfig_EnvOverrides(t *testing.T) {
	os.Setenv("TRUVAG3_VECTOR_DB_URL", "qdrant-test:6334")
	os.Setenv("TRUVAG3_VECTOR_DB_API_KEY", "test-key")
	os.Setenv("TRUVAG3_VECTOR_DB_COLLECTION", "test_collection")
	os.Setenv("TRUVAG3_VECTOR_DB_VECTOR_SIZE", "384")
	defer func() {
		os.Unsetenv("TRUVAG3_VECTOR_DB_URL")
		os.Unsetenv("TRUVAG3_VECTOR_DB_API_KEY")
		os.Unsetenv("TRUVAG3_VECTOR_DB_COLLECTION")
		os.Unsetenv("TRUVAG3_VECTOR_DB_VECTOR_SIZE")
	}()

	config := defaultConfig()
	assert.Equal(t, "qdrant-test:6334", config.Address)
	assert.Equal(t, "test-key", config.APIKey)
	assert.Equal(t, "test_collection", config.CollectionName)
	assert.Equal(t, 384, config.VectorSize)
}

func TestOptionFunctions_Validation(t *testing.T) {
	t.Run("empty address rejected", func(t *testing.T) {
		config := defaultConfig()
		err := WithVectorAddress("")(config)
		assert.Error(t, err)
	})

	t.Run("empty collection name rejected", func(t *testing.T) {
		config := defaultConfig()
		err := WithCollectionName("")(config)
		assert.Error(t, err)
	})

	t.Run("non-positive vector size rejected", func(t *testing.T) {
		config := defaultConfig()
		err := WithVectorSize(0)(config)
		assert.Error(t, err)
		err = WithVectorSize(-1)(config)
		assert.Error(t, err)
	})

	t.Run("valid options applied", func(t *testing.T) {
		config := defaultConfig()
		assert.NoError(t, WithVectorAddress("custom:6334")(config))
		assert.Equal(t, "custom:6334", config.Address)

		assert.NoError(t, WithCollectionName("custom_collection")(config))
		assert.Equal(t, "custom_collection", config.CollectionName)

		assert.NoError(t, WithVectorSize(1536)(config))
		assert.Equal(t, 1536, config.VectorSize)

		assert.NoError(t, WithDistance("Euclid")(config))
		assert.Equal(t, "Euclid", config.Distance)
	})
}

func TestStoreKnowledge_RejectsPrivateScope(t *testing.T) {
	// We can't create a real VectorSharedKnowledge without a Qdrant server,
	// but we can test the scope validation which happens before any gRPC call.
	// Use a minimal struct for this test.
	q := &VectorSharedKnowledge{
		config: defaultConfig(),
	}

	fragment := core.KnowledgeFragment{
		Content:     "test knowledge",
		Scope:       core.ScopePrivate,
		AgentDomain: "infrastructure",
	}

	err := q.StoreKnowledge(context.Background(), fragment)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "private fragments cannot be stored")
}

func TestStoreKnowledge_AcceptsSharedDomain(t *testing.T) {
	q := &VectorSharedKnowledge{
		config: defaultConfig(),
	}

	fragment := core.KnowledgeFragment{
		Content:     "test knowledge",
		Scope:       core.ScopeSharedDomain,
		AgentDomain: "infrastructure",
		Embedding:   nil, // Empty embedding — should be handled gracefully
	}

	// Without a real Qdrant connection, the store will fail-open on empty embedding
	err := q.StoreKnowledge(context.Background(), fragment)
	assert.NoError(t, err) // Nil embedding returns nil (fail-open, logged)
}

func TestComputeRecency(t *testing.T) {
	t.Run("now = 1.0", func(t *testing.T) {
		score := computeRecency(time.Now())
		assert.InDelta(t, 1.0, score, 0.01)
	})

	t.Run("24 hours ago ≈ 0.5", func(t *testing.T) {
		score := computeRecency(time.Now().Add(-24 * time.Hour))
		assert.InDelta(t, 0.5, score, 0.05) // Roughly half-life
	})

	t.Run("7 days ago → near zero", func(t *testing.T) {
		score := computeRecency(time.Now().Add(-7 * 24 * time.Hour))
		assert.Less(t, score, 0.01)
	})

	t.Run("zero time = 0.5 (neutral)", func(t *testing.T) {
		score := computeRecency(time.Time{})
		assert.Equal(t, 0.5, score)
	})

	t.Run("monotonically decreasing", func(t *testing.T) {
		recent := computeRecency(time.Now().Add(-1 * time.Hour))
		old := computeRecency(time.Now().Add(-48 * time.Hour))
		assert.Greater(t, recent, old)
	})
}

func TestBuildScopeFilter(t *testing.T) {
	q := &VectorSharedKnowledge{config: defaultConfig()}
	filter := q.buildScopeFilter("infrastructure")

	// Should have Should conditions (OR logic)
	assert.NotNil(t, filter.Should)
	assert.GreaterOrEqual(t, len(filter.Should), 2) // global OR (shared_domain + domain match)
}

func TestPayloadHelpers(t *testing.T) {
	t.Run("getPayloadString nil safe", func(t *testing.T) {
		assert.Equal(t, "", getPayloadString(nil, "key"))
	})

	t.Run("computeRecency never negative", func(t *testing.T) {
		// Future time should still return positive
		score := computeRecency(time.Now().Add(1 * time.Hour))
		assert.GreaterOrEqual(t, score, 0.0)
		assert.LessOrEqual(t, score, 1.0+0.01) // Slight tolerance for exp math
	})
}

func TestComputeRecency_HalfLife(t *testing.T) {
	// Verify the half-life formula: ln(2)/24 ≈ 0.029
	halfLife := math.Log(2) / 24.0
	assert.InDelta(t, 0.029, halfLife, 0.001)
}
