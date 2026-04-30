package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// --- Test fakes ---

// recordingKnowledge records every StoreKnowledge call.
type recordingKnowledge struct {
	mu       sync.Mutex
	stored   []core.KnowledgeFragment
	storeErr error
}

func (r *recordingKnowledge) StoreKnowledge(ctx context.Context, fragment core.KnowledgeFragment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storeErr != nil {
		return r.storeErr
	}
	r.stored = append(r.stored, fragment)
	return nil
}
func (r *recordingKnowledge) SearchKnowledge(ctx context.Context, callerDomain, namespace, query string, topK int, weights core.RetrievalWeights) ([]core.ScoredKnowledge, error) {
	return nil, nil
}
func (r *recordingKnowledge) UpdateImportance(ctx context.Context, fragmentID string, newImportance float64) error {
	return nil
}
func (r *recordingKnowledge) StoredCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.stored)
}

// recordingEmbedder counts GenerateEmbeddings calls.
type recordingEmbedder struct {
	mu       sync.Mutex
	calls    int
	embedErr error
}

func (r *recordingEmbedder) GenerateEmbeddings(ctx context.Context, texts []string, opts *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if r.embedErr != nil {
		return nil, r.embedErr
	}
	return &core.EmbeddingResponse{
		Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		Usage:      core.TokenUsage{TotalTokens: 5},
	}, nil
}
func (r *recordingEmbedder) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// stubReflector returns canned KnowledgeFragments.
type stubReflector struct {
	mu          sync.Mutex
	fragments   []core.KnowledgeFragment
	calls       int
	err         error
	lastContext context.Context // captured for assertions about request_id propagation
}

func (s *stubReflector) Reflect(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
	s.mu.Lock()
	s.calls++
	s.lastContext = ctx
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.fragments, nil
}
func (s *stubReflector) Compact(ctx context.Context, config core.CompactionConfig) error {
	return nil
}
func (s *stubReflector) ReflectCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// --- Constructor tests ---

func TestNewReflectionJob_FailFast_NilDeps(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}
	embedder := &recordingEmbedder{}
	reflector := &stubReflector{}

	t.Run("nil reflector", func(t *testing.T) {
		_, err := NewReflectionJob(nil, episodic, knowledge, embedder, "test", &core.NoOpLogger{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reflector is required")
	})

	t.Run("nil episodic", func(t *testing.T) {
		_, err := NewReflectionJob(reflector, nil, knowledge, embedder, "test", &core.NoOpLogger{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "episodic memory is required")
	})

	t.Run("nil knowledge", func(t *testing.T) {
		_, err := NewReflectionJob(reflector, episodic, nil, embedder, "test", &core.NoOpLogger{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "shared knowledge is required")
	})

	t.Run("nil embedder", func(t *testing.T) {
		_, err := NewReflectionJob(reflector, episodic, knowledge, nil, "test", &core.NoOpLogger{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "embedding client is required")
	})
}

func TestNewReflectionJob_NilLogger_DefaultsToNoOp(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", nil)
	require.NoError(t, err)
	assert.NotNil(t, job.logger)
}

func TestNewReflectionJob_EmptyDomain_FallsBackToDefault(t *testing.T) {
	t.Setenv("TRUVAG3_AGENT_DOMAIN", "")
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "", &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Equal(t, "default", job.domain)
}

func TestNewReflectionJob_EmptyDomain_UsesEnvVar(t *testing.T) {
	t.Setenv("TRUVAG3_AGENT_DOMAIN", "infrastructure")
	episodic := NewInMemoryEpisodicMemory("infrastructure", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "", &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Equal(t, "infrastructure", job.domain)
}

func TestNewReflectionJob_DefaultConfig(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, job.interval)
	assert.Equal(t, 7*24*time.Hour, job.ageThreshold)
	assert.Equal(t, 5, job.minEvents)
}

func TestNewReflectionJob_EnvVarOverrides(t *testing.T) {
	t.Setenv("TRUVAG3_REFLECTION_INTERVAL", "1h")
	t.Setenv("TRUVAG3_REFLECTION_AGE_THRESHOLD", "48h")
	t.Setenv("TRUVAG3_REFLECTION_MIN_EVENTS", "10")

	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Equal(t, 1*time.Hour, job.interval)
	assert.Equal(t, 48*time.Hour, job.ageThreshold)
	assert.Equal(t, 10, job.minEvents)
}

func TestNewReflectionJob_InvalidEnvVars_KeepDefaults(t *testing.T) {
	t.Setenv("TRUVAG3_REFLECTION_INTERVAL", "not-a-duration")
	t.Setenv("TRUVAG3_REFLECTION_AGE_THRESHOLD", "garbage")
	t.Setenv("TRUVAG3_REFLECTION_MIN_EVENTS", "abc")

	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, job.interval, "invalid env var should keep default")
	assert.Equal(t, 7*24*time.Hour, job.ageThreshold)
	assert.Equal(t, 5, job.minEvents)
}

func TestNewReflectionJob_NegativeEnvVars_KeepDefaults(t *testing.T) {
	t.Setenv("TRUVAG3_REFLECTION_INTERVAL", "-1h")
	t.Setenv("TRUVAG3_REFLECTION_MIN_EVENTS", "-5")

	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{})
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, job.interval, "negative duration should be rejected")
	assert.Equal(t, 5, job.minEvents, "negative min events should be rejected")
}

func TestNewReflectionJob_OptionError_PropagatesError(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	failingOpt := func(j *ReflectionJob) error {
		return errors.New("option failed")
	}
	_, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{}, failingOpt)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "option failed")
}

// --- Options tests ---

func TestWithReflectionLock(t *testing.T) {
	mockLock := &core.MockDistributedLock{}
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		WithReflectionLock(mockLock),
	)
	require.NoError(t, err)
	assert.Equal(t, core.DistributedLock(mockLock), job.lock)
}

func TestWithReflectionLock_NilAccepted(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		WithReflectionLock(nil),
	)
	require.NoError(t, err)
	assert.Nil(t, job.lock, "nil lock is valid (disables locking)")
}

func TestWithReflectionTelemetry(t *testing.T) {
	tel := &core.NoOpTelemetry{}
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, err := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		WithReflectionTelemetry(tel),
	)
	require.NoError(t, err)
	assert.Equal(t, core.Telemetry(tel), job.telemetry)
}

// --- Start/RunOnce lifecycle tests ---

func TestStart_ImplementsRunnable(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, _ := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{})

	// Compile-time check (also asserted in source via var _ = ...)
	var _ core.Runnable = job
}

func TestStart_BlocksUntilCtxCancel(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, _ := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		// Set very long interval so the ticker doesn't fire during the test
		func(j *ReflectionJob) error { j.interval = 1 * time.Hour; return nil },
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- job.Start(ctx) }()

	// Should still be running (no result yet)
	select {
	case <-done:
		t.Fatal("Start returned before ctx was cancelled")
	case <-time.After(50 * time.Millisecond):
		// good — still blocking
	}

	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err, "Start should return nil on graceful shutdown via ctx")
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}

func TestRunOnce_NoEntities_ExitsCleanly(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}
	embedder := &recordingEmbedder{}
	reflector := &stubReflector{}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, embedder, "test", &core.NoOpLogger{})

	err := job.RunOnce(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 0, reflector.ReflectCalls(), "no entities → reflector not called")
	assert.Equal(t, 0, knowledge.StoredCount())
}

func TestRunOnce_SkipsEntitiesBelowMinEvents(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}
	embedder := &recordingEmbedder{}
	reflector := &stubReflector{
		fragments: []core.KnowledgeFragment{{Content: "pattern"}},
	}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, embedder, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 5; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)

	// Add only 2 events for the same entity (below the 5-event threshold)
	for i := 0; i < 2; i++ {
		_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
			AgentName:   "test",
			AgentDomain: "test",
			ActionType:  "investigate",
			EntityType:  "pod",
			EntityID:    "pod-1",
			Timestamp:   time.Now().Add(-1 * time.Hour),
			Scope:       core.ScopeSharedDomain,
		})
	}

	err := job.RunOnce(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 0, reflector.ReflectCalls(), "entity below minEvents should be skipped")
}

func TestRunOnce_StoresFragmentsForQualifyingEntities(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}
	embedder := &recordingEmbedder{}
	reflector := &stubReflector{
		fragments: []core.KnowledgeFragment{
			{Content: "pattern A", Namespace: "patterns", Importance: 7.0},
		},
	}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, embedder, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 3; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)

	// Add 3 events for the same entity (meets threshold)
	for i := 0; i < 3; i++ {
		_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
			AgentName:   "test",
			AgentDomain: "test",
			ActionType:  fmt.Sprintf("action_%d", i),
			EntityType:  "pod",
			EntityID:    "pod-busy",
			Timestamp:   time.Now().Add(-1 * time.Hour),
			Scope:       core.ScopeSharedDomain,
		})
	}

	err := job.RunOnce(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, reflector.ReflectCalls())
	assert.Equal(t, 1, embedder.Calls())
	assert.Equal(t, 1, knowledge.StoredCount(), "1 fragment should be stored")
	stored := knowledge.stored[0]
	assert.Equal(t, "pattern A", stored.Content)
	assert.Equal(t, "test", stored.AgentDomain, "domain should be auto-filled from job")
	assert.Equal(t, core.ScopeSharedDomain, stored.Scope, "scope should be auto-filled")
	assert.False(t, stored.CreatedAt.IsZero(), "CreatedAt should be auto-filled")
	assert.NotEmpty(t, stored.Embedding, "embedding should be populated")
}

// TestRunOnce_PropagatesRequestIDForLLMDebugCapture proves that the reflection
// job sets request_id on BOTH propagation paths that the InstrumentedAIClient
// checks — OTel baggage ("request_id") and the core explicit context key —
// so downstream LLM calls made by the reflector are picked up and recorded to
// the LLM debug store.
//
// Without this propagation, reflection LLM calls are silently dropped from the
// registry viewer's LLM Debug screen: resolveRequestID in ai/instrumented_client.go
// checks baggage first then the core key, and if both are empty the recorder
// short-circuits and skips the call. The test asserts both paths explicitly
// because the InstrumentedAIClient's baggage path is its PRIMARY — a future
// refactor that drops core.WithRequestID would still work today, but silently
// break if the baggage write regressed.
func TestRunOnce_PropagatesRequestIDForLLMDebugCapture(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	reflector := &stubReflector{
		fragments: []core.KnowledgeFragment{{Content: "x", Namespace: "patterns", Importance: 5}},
	}
	job, _ := NewReflectionJob(reflector, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 1; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)
	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName: "test", AgentDomain: "test", ActionType: "a",
		EntityType: "pod", EntityID: "p1",
		Timestamp: time.Now().Add(-1 * time.Hour), Scope: core.ScopeSharedDomain,
	})

	require.NoError(t, job.RunOnce(context.Background()))
	require.Equal(t, 1, reflector.ReflectCalls(), "reflector should have been called")

	ctx := reflector.lastContext
	require.NotNil(t, ctx)

	// Path 1: explicit core context key — the fallback path in resolveRequestID.
	coreID := core.GetRequestID(ctx)
	require.NotEmpty(t, coreID, "core.GetRequestID(ctx) must return the pass_id")
	assert.True(t, strings.HasPrefix(coreID, "reflect-"),
		"core request_id should start with 'reflect-'; got %q", coreID)

	// Path 2: OTel baggage "request_id" — the PRIMARY path in resolveRequestID
	// (ai/instrumented_client.go:164 checks baggage before the core key).
	bag := telemetry.GetBaggage(ctx)
	require.NotNil(t, bag, "baggage must be present so InstrumentedAIClient finds request_id")
	bagID := bag["request_id"]
	assert.Equal(t, coreID, bagID,
		"baggage request_id must match core request_id — otherwise an LLM recording made via the baggage path would be filed under a different key than one made via the core path")

	// Also verify pass_id baggage is set (used by framework/memory logs for correlation).
	assert.Equal(t, coreID, bag["pass_id"],
		"pass_id baggage should equal the request_id for log correlation")
}

// TestRunOnce_PassIDFormat48BitEntropy pins the pass_id format so a future
// contributor can't accidentally narrow the entropy. The pass_id is the
// reflection job's only defense against LLM-debug-store key collision:
// two passes with the same id silently overwrite each other's interaction
// records in Redis. The ID must be:
//
//   - prefixed with "reflect-" for visual distinction from "orch-*"
//     orchestration request IDs in the registry viewer
//   - followed by exactly 12 lowercase hex characters (48 bits of entropy),
//     NOT the historical 8 hex characters (32 bits — ~45-year half-life to
//     collision at 6h cadence, which was uncomfortably close to production
//     multi-replica deployments with tight intervals)
//   - pure hex with no UUID dashes (readable in logs, greppable in CI)
//
// Widening to 48 bits raises the birthday-collision half-life to ~11,000
// years at 24h cadence and ~2,700 years at 6h cadence. Any future narrowing
// MUST include a redo of the birthday-collision math for the intended
// deployment cadence and multi-replica fanout — this test exists to force
// that conversation.
func TestRunOnce_PassIDFormat48BitEntropy(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	reflector := &stubReflector{
		fragments: []core.KnowledgeFragment{{Content: "x", Namespace: "patterns", Importance: 5}},
	}
	job, _ := NewReflectionJob(reflector, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 1; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)
	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName: "test", AgentDomain: "test", ActionType: "a",
		EntityType: "pod", EntityID: "p1",
		Timestamp: time.Now().Add(-1 * time.Hour), Scope: core.ScopeSharedDomain,
	})

	// Run the pass twice and capture both pass_ids to verify:
	// (a) format is correct for each
	// (b) the two ids are different (sanity check that randomness is flowing through)
	ids := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		require.NoError(t, job.RunOnce(context.Background()))
		id := core.GetRequestID(reflector.lastContext)
		ids = append(ids, id)
	}

	// Pattern match: "reflect-" + exactly 12 lowercase hex characters.
	pattern := regexp.MustCompile(`^reflect-[0-9a-f]{12}$`)
	for i, id := range ids {
		if !pattern.MatchString(id) {
			t.Errorf("pass_id %d does not match expected format %q: got %q",
				i, pattern.String(), id)
		}
		// Explicit length check in addition to the regex — makes the failure
		// message clearer if someone widens the entropy further and forgets
		// to update this test.
		if len(id) != len("reflect-")+12 {
			t.Errorf("pass_id %d length: got %d, want %d (reflect- + 12 hex chars)",
				i, len(id), len("reflect-")+12)
		}
	}

	// Two passes in a row must produce distinct ids. If they match, either
	// randomness is broken, or the same ID is somehow being memoized across
	// calls. Both are bugs.
	if ids[0] == ids[1] {
		t.Errorf("two consecutive passes produced the same pass_id %q — randomness broken or ID is being memoized", ids[0])
	}
}

func TestRunOnce_SkipsDigestEvents(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}
	reflector := &stubReflector{}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 1; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)

	// Add a digest event (should be skipped during entity discovery)
	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName:   "test",
		AgentDomain: "test",
		ActionType:  "digest", // <-- digest events are filtered out
		EntityType:  "pod",
		EntityID:    "pod-digest",
		Timestamp:   time.Now().Add(-1 * time.Hour),
		Scope:       core.ScopeSharedDomain,
	})

	err := job.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, reflector.ReflectCalls(), "digest events should not trigger reflection")
}

func TestRunOnce_LockHeldByAnotherReplica_Skips(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}
	reflector := &stubReflector{
		fragments: []core.KnowledgeFragment{{Content: "x"}},
	}

	// Lock that always returns "not acquired"
	mockLock := &core.MockDistributedLock{
		AcquireFn: func(ctx context.Context, key string, ttl time.Duration) (bool, error) {
			return false, nil
		},
	}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		WithReflectionLock(mockLock),
		func(j *ReflectionJob) error { j.minEvents = 1; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)

	// Even with qualifying events, the lock should make us skip
	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName: "test", AgentDomain: "test", ActionType: "x",
		EntityType: "pod", EntityID: "pod-1", Timestamp: time.Now().Add(-1 * time.Hour),
		Scope: core.ScopeSharedDomain,
	})

	err := job.RunOnce(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, mockLock.AcquireCt)
	assert.Equal(t, 0, reflector.ReflectCalls(), "should not reflect when lock held by another replica")
	assert.Equal(t, 0, knowledge.StoredCount())
}

func TestRunOnce_LockAcquireError_FailOpen(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}
	reflector := &stubReflector{}

	mockLock := &core.MockDistributedLock{
		AcquireFn: func(ctx context.Context, key string, ttl time.Duration) (bool, error) {
			return false, errors.New("redis down")
		},
	}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		WithReflectionLock(mockLock),
	)

	err := job.RunOnce(context.Background())
	assert.NoError(t, err, "lock acquisition error should fail open (return nil)")
}

func TestRunOnce_LockAcquired_ReleasedOnExit(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}
	reflector := &stubReflector{}

	mockLock := &core.MockDistributedLock{
		AcquireFn: func(ctx context.Context, key string, ttl time.Duration) (bool, error) {
			return true, nil
		},
	}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		WithReflectionLock(mockLock),
	)

	err := job.RunOnce(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, mockLock.AcquireCt)
	assert.Equal(t, 1, mockLock.ReleaseCt, "lock should be released after RunOnce completes")
}

func TestRunOnce_NoLock_NoLockCalls(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, _ := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{})
	// No WithReflectionLock — job.lock is nil
	require.Nil(t, job.lock)

	err := job.RunOnce(context.Background())
	assert.NoError(t, err)
}

func TestRunOnce_WithTelemetry_CreatesSpan(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, _ := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		WithReflectionTelemetry(&core.NoOpTelemetry{}),
	)
	require.NotNil(t, job.telemetry)

	// RunOnce should create + close a span without panic
	err := job.RunOnce(context.Background())
	assert.NoError(t, err)
}

func TestRunOnce_ReflectorError_ContinuesToNextEntity(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}

	// Reflector that errors for all entities
	reflector := &stubReflector{err: errors.New("LLM unavailable")}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 1; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)

	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName: "test", AgentDomain: "test", ActionType: "x",
		EntityType: "pod", EntityID: "pod-1", Timestamp: time.Now().Add(-1 * time.Hour),
		Scope: core.ScopeSharedDomain,
	})

	err := job.RunOnce(context.Background())
	assert.NoError(t, err, "reflector errors should be swallowed (continue loop)")
	assert.Equal(t, 0, knowledge.StoredCount())
}

func TestRunOnce_EmbedderError_SkipsFragment(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{}
	embedder := &recordingEmbedder{embedErr: errors.New("embedding service down")}
	reflector := &stubReflector{
		fragments: []core.KnowledgeFragment{{Content: "p"}},
	}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, embedder, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 1; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)

	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName: "test", AgentDomain: "test", ActionType: "x",
		EntityType: "pod", EntityID: "pod-1", Timestamp: time.Now().Add(-1 * time.Hour),
		Scope: core.ScopeSharedDomain,
	})

	err := job.RunOnce(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 1, embedder.Calls(), "embedder should be called")
	assert.Equal(t, 0, knowledge.StoredCount(), "fragment should not be stored when embedding fails")
}

func TestRunOnce_StoreError_ContinuesProcessing(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	knowledge := &recordingKnowledge{storeErr: errors.New("Qdrant down")}
	reflector := &stubReflector{
		fragments: []core.KnowledgeFragment{{Content: "p"}},
	}

	job, _ := NewReflectionJob(reflector, episodic, knowledge, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 1; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)

	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName: "test", AgentDomain: "test", ActionType: "x",
		EntityType: "pod", EntityID: "pod-1", Timestamp: time.Now().Add(-1 * time.Hour),
		Scope: core.ScopeSharedDomain,
	})

	err := job.RunOnce(context.Background())
	assert.NoError(t, err, "store errors should not propagate")
}

// --- discoverEntities tests ---

func TestDiscoverEntities_MultiEntityEvents(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, _ := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 2; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)

	// Event with multiple entities
	for i := 0; i < 2; i++ {
		_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
			AgentName: "test", AgentDomain: "test", ActionType: "multi",
			Entities: []core.EntityRef{
				{Type: "pod", ID: "pod-x"},
				{Type: "service", ID: "svc-y"},
			},
			Timestamp: time.Now().Add(-1 * time.Hour),
			Scope:     core.ScopeSharedDomain,
		})
	}

	entities, err := job.discoverEntities(context.Background(), time.Now())
	require.NoError(t, err)

	// Both entities should be discovered (each appears in 2 events)
	keys := make(map[string]bool)
	for _, e := range entities {
		keys[e.Type+":"+e.ID] = true
	}
	assert.True(t, keys["pod:pod-x"])
	assert.True(t, keys["service:svc-y"])
}

func TestDiscoverEntities_BackwardCompatSingularFields(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	job, _ := NewReflectionJob(&stubReflector{}, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error { j.minEvents = 1; j.ageThreshold = 1 * time.Nanosecond; return nil },
	)

	// Event using only the singular EntityType/EntityID fields (no Entities slice)
	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName: "test", AgentDomain: "test", ActionType: "legacy",
		EntityType: "pod", EntityID: "pod-legacy",
		Timestamp: time.Now().Add(-1 * time.Hour),
		Scope:     core.ScopeSharedDomain,
	})

	entities, err := job.discoverEntities(context.Background(), time.Now())
	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, "pod", entities[0].Type)
	assert.Equal(t, "pod-legacy", entities[0].ID)
}

// --- Concurrency / race tests ---

func TestStart_TickerFiresRunOnce(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	reflectorCalls := int32(0)
	reflector := &countingReflector{counter: &reflectorCalls}

	// Use a very short interval and seed an event to trigger Reflect when ticker fires
	job, _ := NewReflectionJob(reflector, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error {
			j.interval = 50 * time.Millisecond
			j.minEvents = 1
			j.ageThreshold = 1 * time.Nanosecond
			return nil
		},
	)

	// Seed an event old enough to be discovered
	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName: "test", AgentDomain: "test", ActionType: "x",
		EntityType: "pod", EntityID: "pod-1", Timestamp: time.Now().Add(-1 * time.Hour),
		Scope: core.ScopeSharedDomain,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- job.Start(ctx) }()

	// Wait long enough for the initial pass (T=0) AND at least 1 ticker pass
	// (T=50ms) to fire. Total of 2 calls expected at minimum.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	// >= 2 because Start now fires an immediate pass on startup AND at least
	// one ticker pass should have completed within 150ms (3× the 50ms interval).
	// Tightened from the previous >= 1 because the initial-pass-on-startup
	// behavior makes 1 the trivially-passing case — the test would no longer
	// distinguish "ticker working" from "ticker dead but initial pass happened".
	assert.GreaterOrEqual(t, atomic.LoadInt32(&reflectorCalls), int32(2),
		"Start should fire initial pass on startup AND ticker should fire at least one more pass within 150ms")
}

// TestStart_FiresInitialPassOnStartup pins the run-on-startup contract added
// to ReflectionJob.Start. Without this behavior, every redeploy of an agent
// using reflection blackholes the next INTERVAL hours of reflection
// observability, which makes validation after a deploy essentially impossible
// at production cadences (24h, 6h).
//
// The test uses a deliberately LARGE interval so that any reflector calls
// observed during a short test window MUST come from the initial pass, not
// from a ticker tick. If we used a short interval here, a ticker tick could
// race the assertion and produce a false positive — the test would pass even
// if the initial-pass code path were silently broken.
func TestStart_FiresInitialPassOnStartup(t *testing.T) {
	episodic := NewInMemoryEpisodicMemory("test", 100)
	reflectorCalls := int32(0)
	reflector := &countingReflector{counter: &reflectorCalls}

	// Interval of 1 hour is the key part of this test design — it guarantees
	// that any reflector call observed within the 100ms test window cannot
	// have come from a ticker tick. The only path to a non-zero call count
	// is the immediate-pass-on-startup branch.
	job, _ := NewReflectionJob(reflector, episodic, &recordingKnowledge{}, &recordingEmbedder{}, "test", &core.NoOpLogger{},
		func(j *ReflectionJob) error {
			j.interval = 1 * time.Hour
			j.minEvents = 1
			j.ageThreshold = 1 * time.Nanosecond
			return nil
		},
	)

	// Seed an event old enough to be discovered by the initial pass.
	_ = episodic.RecordEvent(context.Background(), core.AgentEvent{
		AgentName: "test", AgentDomain: "test", ActionType: "x",
		EntityType: "pod", EntityID: "pod-1", Timestamp: time.Now().Add(-1 * time.Hour),
		Scope: core.ScopeSharedDomain,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- job.Start(ctx) }()

	// 100ms is far less than the 1h ticker interval, but well within the
	// time it takes the initial pass to invoke the (in-memory, instant)
	// reflector for one entity.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Exactly 1 call expected: the initial pass. The ticker tick at T=1h
	// cannot have fired in our 100ms window. If this assertion sees 0 calls,
	// the initial-pass branch has regressed. If it sees more than 1, the
	// ticker has somehow fired early — also a bug.
	got := atomic.LoadInt32(&reflectorCalls)
	if got != 1 {
		t.Errorf("expected exactly 1 reflector call from initial pass on startup, got %d (interval=1h means any extra calls indicate a ticker bug)", got)
	}
}

// countingReflector is like stubReflector but uses an external atomic counter
// for safe concurrent reads from test code.
type countingReflector struct {
	counter *int32
}

func (c *countingReflector) Reflect(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
	atomic.AddInt32(c.counter, 1)
	return nil, nil
}
func (c *countingReflector) Compact(ctx context.Context, config core.CompactionConfig) error {
	return nil
}
