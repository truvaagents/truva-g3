package memory

import (
	"context"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestNewUserMemoryBackend_DefaultsToInMemory(t *testing.T) {
	// No TRUVAG3_VECTOR_DB_URL, no embedder → InMemoryUserMemory
	backend, err := NewUserMemoryBackend(&core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, backend)

	deps := backend.ToDeps()
	require.NotNil(t, deps)
	assert.NotNil(t, deps.UserMemory)
	assert.Equal(t, "default", deps.Namespace)
	assert.Nil(t, deps.Embedder)

	// Should be InMemoryUserMemory
	_, ok := deps.UserMemory.(*InMemoryUserMemory)
	assert.True(t, ok, "should default to InMemoryUserMemory when no vector DB configured")

	backend.Close() // should not panic
}

func TestNewUserMemoryBackend_WithNamespace(t *testing.T) {
	backend, err := NewUserMemoryBackend(&core.NoOpLogger{},
		WithUserMemoryNamespace("travel"),
	)
	require.NoError(t, err)

	deps := backend.ToDeps()
	assert.Equal(t, "travel", deps.Namespace)

	backend.Close()
}

func TestNewUserMemoryBackend_NilLogger(t *testing.T) {
	// Should not panic with nil logger
	backend, err := NewUserMemoryBackend(nil)
	require.NoError(t, err)
	require.NotNil(t, backend)

	backend.Close()
}

func TestNewUserMemoryBackend_ToDeps_ReturnsDefensiveCopy(t *testing.T) {
	backend, err := NewUserMemoryBackend(&core.NoOpLogger{},
		WithUserMemoryNamespace("travel"),
	)
	require.NoError(t, err)

	deps1 := backend.ToDeps()
	deps2 := backend.ToDeps()

	// Modifying deps1 should not affect deps2
	deps1.Namespace = "modified"
	assert.Equal(t, "travel", deps2.Namespace, "ToDeps should return a defensive copy")

	backend.Close()
}

func TestNewUserMemoryBackend_ToDeps_NilBackend(t *testing.T) {
	var backend *UserMemoryBackend
	assert.Nil(t, backend.ToDeps(), "ToDeps on nil backend should return nil")
}

func TestNewUserMemoryBackend_Close_NilBackend(t *testing.T) {
	var backend *UserMemoryBackend
	backend.Close() // should not panic
}

func TestNewUserMemoryBackend_EmbedderWithoutVectorDB(t *testing.T) {
	// Embedder provided but no TRUVAG3_VECTOR_DB_URL → still in-memory
	// (vector DB URL check happens inside the factory)
	backend, err := NewUserMemoryBackend(&core.NoOpLogger{},
		WithUserMemoryEmbeddingClient(&mockEmbedder{}),
	)
	require.NoError(t, err)

	deps := backend.ToDeps()
	_, ok := deps.UserMemory.(*InMemoryUserMemory)
	assert.True(t, ok, "should fall back to InMemoryUserMemory without TRUVAG3_VECTOR_DB_URL")
	assert.NotNil(t, deps.Embedder, "embedder should still be set in deps")

	backend.Close()
}

func TestTransientMaxAgeFromEnv_DefaultsWhenUnsetOrInvalid(t *testing.T) {
	t.Setenv("TRUVAG3_USER_MEMORY_TRANSIENT_MAX_AGE_HOURS", "")
	assert.Equal(t, 168*time.Hour, transientMaxAgeFromEnv())

	t.Setenv("TRUVAG3_USER_MEMORY_TRANSIENT_MAX_AGE_HOURS", "-4")
	assert.Equal(t, 168*time.Hour, transientMaxAgeFromEnv())

	t.Setenv("TRUVAG3_USER_MEMORY_TRANSIENT_MAX_AGE_HOURS", "24")
	assert.Equal(t, 24*time.Hour, transientMaxAgeFromEnv())
}

func TestFilterRecalledFactsByLifetime_ExpiredTransientFactsExcluded(t *testing.T) {
	now := time.Now()
	facts := []core.UserFact{
		{
			FactID:    "t1",
			Category:  "context",
			Content:   "Current task detail",
			UpdatedAt: now.Add(-200 * time.Hour),
			Metadata: map[string]string{
				core.UserFactMetadataLifetimeKey: core.UserFactLifetimeTransient,
			},
		},
		{
			FactID:    "d1",
			Category:  "preference",
			Content:   "User prefers DFW",
			UpdatedAt: now.Add(-400 * time.Hour),
			Metadata: map[string]string{
				core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable,
			},
		},
	}

	filtered, filteredCount := filterRecalledFactsByLifetime(facts, now, 168*time.Hour)
	require.Len(t, filtered, 1)
	assert.Equal(t, 1, filteredCount)
	assert.Equal(t, "d1", filtered[0].FactID)
}

func TestFilterRecalledFactsByLifetime_UpdatedTransientFactSurvives(t *testing.T) {
	now := time.Now()
	facts := []core.UserFact{
		{
			FactID:    "t1",
			Category:  "context",
			Content:   "Current task detail",
			UpdatedAt: now.Add(-2 * time.Hour),
			Metadata: map[string]string{
				core.UserFactMetadataLifetimeKey: core.UserFactLifetimeTransient,
			},
		},
	}

	filtered, filteredCount := filterRecalledFactsByLifetime(facts, now, 168*time.Hour)
	require.Len(t, filtered, 1)
	assert.Equal(t, 0, filteredCount)
	assert.Equal(t, "t1", filtered[0].FactID)
}

func TestFilterRecalledFactsByLifetime_ZeroUpdatedAtFailsOpen(t *testing.T) {
	now := time.Now()
	facts := []core.UserFact{
		{
			FactID:   "t1",
			Category: "context",
			Content:  "Legacy transient fact",
			Metadata: map[string]string{
				core.UserFactMetadataLifetimeKey: core.UserFactLifetimeTransient,
			},
		},
	}

	filtered, filteredCount := filterRecalledFactsByLifetime(facts, now, 168*time.Hour)
	require.Len(t, filtered, 1)
	assert.Equal(t, 0, filteredCount)
	assert.Equal(t, "t1", filtered[0].FactID)
}

func TestRecall_TransientCleanupIsObservable(t *testing.T) {
	recorder, tracer := setupUserMemoryTestTracer(t)

	mem := NewInMemoryUserMemory(0)
	ctx, span := tracer.Start(core.WithRequestID(context.Background(), "req-transient-1"), "test-recall")

	stale := time.Now().Add(-200 * time.Hour)
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID:    "t1",
		Namespace: "travel",
		Category:  "context",
		Content:   "User is planning a Maldives trip next month",
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeTransient,
		},
		UpdatedAt: stale,
	}))
	mem.mu.Lock()
	mem.facts["user-1"][0].UpdatedAt = stale
	mem.mu.Unlock()

	results, err := mem.Recall(ctx, "user-1", "travel", "", 10)
	require.NoError(t, err)
	assert.Empty(t, results)

	span.End()

	spans := recorder.Ended()
	require.NotEmpty(t, spans)

	var found bool
	for _, event := range spans[len(spans)-1].Events() {
		if event.Name != "user_memory.transient_cleanup.filtered" {
			continue
		}
		found = true
		assertEventStringAttr(t, event.Attributes, "request_id", "req-transient-1")
		assertEventIntAttr(t, event.Attributes, "filtered_count", 1)
		assertEventIntAttr(t, event.Attributes, "transient_max_age_hours", 168)
	}
	assert.True(t, found, "expected transient cleanup span event")
}

func setupUserMemoryTestTracer(t *testing.T) (*tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})
	return recorder, tp.Tracer("user-memory-test")
}

func assertEventStringAttr(t *testing.T, attrs []attribute.KeyValue, key string, want string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			assert.Equal(t, want, attr.Value.AsString())
			return
		}
	}
	t.Fatalf("missing attribute %s", key)
}

func assertEventIntAttr(t *testing.T, attrs []attribute.KeyValue, key string, want int) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			assert.Equal(t, int64(want), attr.Value.AsInt64())
			return
		}
	}
	t.Fatalf("missing attribute %s", key)
}

// mockEmbedder satisfies core.EmbeddingClient for tests.
type mockEmbedder struct{}

func (m *mockEmbedder) GenerateEmbeddings(ctx context.Context, texts []string, opts *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		embeddings[i] = make([]float32, 768) // zero vector
	}
	return &core.EmbeddingResponse{Embeddings: embeddings}, nil
}
