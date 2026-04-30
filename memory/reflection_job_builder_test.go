package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

// --- Test fakes for BuildReflectionJob dependencies ---

type stubAIClient struct{}

func (s *stubAIClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	return &core.AIResponse{Content: "[]"}, nil
}

type stubKnowledge struct{}

func (s *stubKnowledge) StoreKnowledge(ctx context.Context, fragment core.KnowledgeFragment) error {
	return nil
}
func (s *stubKnowledge) SearchKnowledge(ctx context.Context, callerDomain, namespace, query string, topK int, weights core.RetrievalWeights) ([]core.ScoredKnowledge, error) {
	return nil, nil
}
func (s *stubKnowledge) UpdateImportance(ctx context.Context, fragmentID string, newImportance float64) error {
	return nil
}

type stubEmbedder struct{}

func (s *stubEmbedder) GenerateEmbeddings(ctx context.Context, texts []string, opts *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
	return &core.EmbeddingResponse{
		Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		Usage:      core.TokenUsage{TotalTokens: 5},
	}, nil
}

func makeFullDeps(t *testing.T) *core.SharedMemoryDeps {
	t.Helper()
	episodic := NewInMemoryEpisodicMemory("test-domain", 100)
	return &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    episodic,
		Knowledge:   &stubKnowledge{},
		Embedder:    &stubEmbedder{},
	}
}

// --- Phase 2 unavailable scenarios ---

func TestBuildReflectionJob_NilDeps(t *testing.T) {
	job, err := BuildReflectionJob(nil, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Nil(t, job, "nil deps should return nil job (fail-open)")
}

func TestBuildReflectionJob_NilEpisodic(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		Knowledge: &stubKnowledge{},
		Embedder:  &stubEmbedder{},
	}
	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Nil(t, job, "missing Episodic should return nil")
}

func TestBuildReflectionJob_NilKnowledge_ReturnsNil(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentDomain: "test",
		Episodic:    NewInMemoryEpisodicMemory("test", 100),
		Embedder:    &stubEmbedder{},
		// Knowledge is nil
	}
	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Nil(t, job, "missing Phase 2 Knowledge should return nil (fail-open)")
}

func TestBuildReflectionJob_NilEmbedder_ReturnsNil(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentDomain: "test",
		Episodic:    NewInMemoryEpisodicMemory("test", 100),
		Knowledge:   &stubKnowledge{},
		// Embedder is nil
	}
	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Nil(t, job, "missing Phase 2 Embedder should return nil (fail-open)")
}

func TestBuildReflectionJob_NilAIClient_ReturnsNil(t *testing.T) {
	deps := makeFullDeps(t)
	job, err := BuildReflectionJob(deps, nil, &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Nil(t, job, "nil AI client should return nil (fail-open)")
}

// --- Successful build scenarios ---

func TestBuildReflectionJob_AllDepsAvailable_Success(t *testing.T) {
	deps := makeFullDeps(t)
	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.NotNil(t, job.reflector)
	assert.NotNil(t, job.episodic)
	assert.NotNil(t, job.knowledge)
	assert.NotNil(t, job.embedder)
	assert.Equal(t, "test-domain", job.domain)
}

func TestBuildReflectionJob_AutoWiresLockFromDeps(t *testing.T) {
	deps := makeFullDeps(t)
	mockLock := &core.MockDistributedLock{}
	deps.Lock = mockLock

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, core.DistributedLock(mockLock), job.lock, "lock from deps should be auto-wired")
}

func TestBuildReflectionJob_NilLockInDeps_NoLockSet(t *testing.T) {
	deps := makeFullDeps(t)
	deps.Lock = nil

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Nil(t, job.lock, "nil deps.Lock should leave job.lock as nil")
}

func TestBuildReflectionJob_ExplicitLockOverridesDeps(t *testing.T) {
	deps := makeFullDeps(t)
	depsLock := &core.MockDistributedLock{}
	deps.Lock = depsLock

	explicitLock := &core.MockDistributedLock{}
	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{},
		WithReflectionLock(explicitLock),
	)
	require.NoError(t, err)
	require.NotNil(t, job)
	// Explicit option should override the auto-wired lock from deps.
	// Lock from deps is prepended to opts, then explicit opts run after,
	// so explicit lock wins (last assignment wins).
	assert.Equal(t, core.DistributedLock(explicitLock), job.lock,
		"explicit WithReflectionLock should override auto-wired lock from deps")
}

func TestBuildReflectionJob_TelemetryOption_PassThrough(t *testing.T) {
	deps := makeFullDeps(t)
	tel := &core.NoOpTelemetry{}

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{},
		WithReflectionTelemetry(tel),
	)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, core.Telemetry(tel), job.telemetry)
}

func TestBuildReflectionJob_DomainFromDeps(t *testing.T) {
	deps := makeFullDeps(t)
	deps.AgentDomain = "personal-assistant"

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "personal-assistant", job.domain)
}

func TestBuildReflectionJob_NilLogger_DefaultsToNoOp(t *testing.T) {
	deps := makeFullDeps(t)
	job, err := BuildReflectionJob(deps, &stubAIClient{}, nil)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.NotNil(t, job.logger, "nil logger should be defaulted")
}

// --- minEvents propagation ---

func TestBuildReflectionJob_PropagatesMinEventsToReflector(t *testing.T) {
	t.Setenv("TRUVAG3_REFLECTION_MIN_EVENTS", "2")
	deps := makeFullDeps(t)

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)

	assert.Equal(t, 2, job.minEvents, "job should respect env var")

	r, ok := job.reflector.(*LLMMemoryReflector)
	require.True(t, ok, "reflector should be *LLMMemoryReflector")
	assert.Equal(t, 2, r.minEvents,
		"reflector minEvents should match the job (propagated via builder)")
}

func TestBuildReflectionJob_NoEnvVar_ReflectorUsesDefault(t *testing.T) {
	// Ensure env var is unset for this test
	t.Setenv("TRUVAG3_REFLECTION_MIN_EVENTS", "")
	deps := makeFullDeps(t)

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)

	r, ok := job.reflector.(*LLMMemoryReflector)
	require.True(t, ok)
	assert.Equal(t, 5, r.minEvents, "reflector should use default when env var unset")
	assert.Equal(t, 5, job.minEvents, "job should use default when env var unset")
}

func TestBuildReflectionJob_InvalidMinEventsEnvVar_UsesDefault(t *testing.T) {
	t.Setenv("TRUVAG3_REFLECTION_MIN_EVENTS", "not-a-number")
	deps := makeFullDeps(t)

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)

	r, ok := job.reflector.(*LLMMemoryReflector)
	require.True(t, ok)
	assert.Equal(t, 5, r.minEvents, "invalid value should fall back to default")
}

// --- model propagation ---

func TestBuildReflectionJob_PropagatesModelEnvVarToReflector(t *testing.T) {
	t.Setenv("TRUVAG3_REFLECTION_MODEL", "fast")
	deps := makeFullDeps(t)

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)

	r, ok := job.reflector.(*LLMMemoryReflector)
	require.True(t, ok)
	assert.Equal(t, "fast", r.model,
		"TRUVAG3_REFLECTION_MODEL should be propagated to the reflector so AIOptions.Model is set")
}

func TestBuildReflectionJob_NoModelEnvVar_ReflectorUsesEmpty(t *testing.T) {
	t.Setenv("TRUVAG3_REFLECTION_MODEL", "")
	deps := makeFullDeps(t)

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)

	r, ok := job.reflector.(*LLMMemoryReflector)
	require.True(t, ok)
	assert.Equal(t, "", r.model,
		"unset env var must leave model empty so the AIClient default kicks in")
}

func TestBuildReflectionJob_ConcreteModelName_PassedThrough(t *testing.T) {
	t.Setenv("TRUVAG3_REFLECTION_MODEL", "claude-haiku-4-5-20251001")
	deps := makeFullDeps(t)

	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)

	r, ok := job.reflector.(*LLMMemoryReflector)
	require.True(t, ok)
	assert.Equal(t, "claude-haiku-4-5-20251001", r.model,
		"concrete model names (not just aliases) must propagate verbatim")
}

// --- Avoid timing dependency: just verify the job doesn't panic on quick start/stop ---

func TestBuildReflectionJob_StartReturnsRunnable(t *testing.T) {
	deps := makeFullDeps(t)
	job, err := BuildReflectionJob(deps, &stubAIClient{}, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, job)

	// Verify it satisfies core.Runnable
	var _ core.Runnable = job

	// Quick start/stop test — start in goroutine, cancel immediately
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- job.Start(ctx) }()

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}
