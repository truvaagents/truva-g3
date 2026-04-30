package orchestration

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

// slowFactExtractor blocks for a configurable duration before returning the
// configured facts. Used to assert that AfterSynthesis returns before the
// extraction completes when running in asynchronous mode.
type slowFactExtractor struct {
	facts      []core.UserFact
	delay      time.Duration
	callCount  atomic.Int32
	startedAt  chan struct{} // closed when ExtractFacts begins
	finishedAt chan struct{} // closed when ExtractFacts returns
}

func newSlowFactExtractor(facts []core.UserFact, delay time.Duration) *slowFactExtractor {
	return &slowFactExtractor{
		facts:      facts,
		delay:      delay,
		startedAt:  make(chan struct{}),
		finishedAt: make(chan struct{}),
	}
}

func (e *slowFactExtractor) ExtractFacts(ctx context.Context, userRequest string, agentResponse string, corrections []string) (*ExtractResult, error) {
	e.callCount.Add(1)
	close(e.startedAt)
	select {
	case <-time.After(e.delay):
	case <-ctx.Done():
	}
	close(e.finishedAt)
	return &ExtractResult{Facts: e.facts}, nil
}

// TestUserMemoryExtractionHook_AsyncReturnsImmediately asserts that when the
// hook is configured asynchronous, AfterSynthesis returns well before the
// underlying extractor completes.
func TestUserMemoryExtractionHook_AsyncReturnsImmediately(t *testing.T) {
	mem := newTestUserMemory()
	// 500ms is long enough that sync mode would obviously delay the return
	// but short enough to keep the test fast.
	extractor := newSlowFactExtractor([]core.UserFact{
		{Content: "User is vegetarian", Category: "constraint", Source: core.SourceExplicit, Confidence: 0.95},
	}, 500*time.Millisecond)
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(
		mem, nil, nil, "travel", &core.NoOpLogger{},
		extractor, reconciler,
		WithAsynchronousUserExtraction(),
	)
	t.Cleanup(func() { _ = hook.Close() })

	pctx := &core.PipelineContext{
		Request:  "tell me about vegan restaurants",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	start := time.Now()
	response, err := hook.AfterSynthesis(context.Background(), pctx, "here are some options")
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "here are some options", response, "response passed through unmodified")
	// AfterSynthesis must return without waiting for the 500ms extraction.
	assert.Less(t, elapsed, 100*time.Millisecond, "async AfterSynthesis must return before extraction completes")

	// The extractor should have started but not finished yet.
	<-extractor.startedAt // deterministic — dispatch ran
	select {
	case <-extractor.finishedAt:
		t.Fatal("extractor finished before AfterSynthesis returned — async is not actually async")
	default:
	}

	// Drain to verify extraction eventually completes and fact is stored.
	require.NoError(t, hook.Close())
	facts, _ := mem.Recall(context.Background(), "user-1", "travel", "", 10)
	require.Len(t, facts, 1, "fact should be stored after Close drains in-flight extraction")
	assert.Equal(t, "User is vegetarian", facts[0].Content)
	assert.Equal(t, int32(1), extractor.callCount.Load(), "extractor invoked exactly once")
}

// TestUserMemoryExtractionHook_CloseWaitsForInFlight asserts that Close()
// blocks until every in-flight asynchronous extraction has finished, and
// that all pending facts are stored by the time Close returns.
func TestUserMemoryExtractionHook_CloseWaitsForInFlight(t *testing.T) {
	mem := newTestUserMemory()
	extractor := newSlowFactExtractor([]core.UserFact{
		{Content: "Prefers aisle seats", Category: "preference", Source: core.SourceExplicit, Confidence: 0.9},
	}, 200*time.Millisecond)
	reconciler := &addAllReconciler{}

	hook := NewUserMemoryExtractionHook(
		mem, nil, nil, "travel", &core.NoOpLogger{},
		extractor, reconciler,
		WithAsynchronousUserExtraction(),
	)

	pctx := &core.PipelineContext{
		Request:  "book me a flight",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	_, err := hook.AfterSynthesis(context.Background(), pctx, "flight booked")
	require.NoError(t, err)

	// Facts must not yet be stored — extraction is in-flight.
	<-extractor.startedAt
	facts, _ := mem.Recall(context.Background(), "user-1", "travel", "", 10)
	assert.Empty(t, facts, "fact must not be stored before Close drains")

	// Close must block until extraction completes.
	closeStart := time.Now()
	require.NoError(t, hook.Close())
	closeElapsed := time.Since(closeStart)

	// Close should have waited roughly the remaining extraction time.
	// Allow generous slack for test-runner jitter.
	assert.Greater(t, closeElapsed, 50*time.Millisecond, "Close should block on in-flight extraction, not return instantly")

	// Fact must now be stored.
	facts, _ = mem.Recall(context.Background(), "user-1", "travel", "", 10)
	require.Len(t, facts, 1, "Close must not return until the in-flight extraction has persisted the fact")
	assert.Equal(t, "Prefers aisle seats", facts[0].Content)
}

// TestUserMemoryExtractionHook_SyncAsyncOutcomeParity asserts that the set of
// stored facts is identical whether the hook runs synchronously or
// asynchronously, for a mixed-fact input spanning multiple categories.
// Guards against the async path silently diverging from sync behavior.
func TestUserMemoryExtractionHook_SyncAsyncOutcomeParity(t *testing.T) {
	mixedFacts := []core.UserFact{
		{Content: "User is vegetarian", Category: "constraint", Source: core.SourceExplicit, Confidence: 0.95},
		{Content: "Prefers direct flights", Category: "preference", Source: core.SourceInferred, Confidence: 0.80},
		{Content: "Home airport is SFO", Category: "identity", Source: core.SourceExplicit, Confidence: 0.99},
		{Content: "Family of four", Category: "relationship", Source: core.SourceExplicit, Confidence: 0.90},
	}

	buildHook := func(mem *testUserMemory, async bool) *UserMemoryExtractionHook {
		opts := []UserMemoryExtractionOption{}
		if async {
			opts = append(opts, WithAsynchronousUserExtraction())
		}
		return NewUserMemoryExtractionHook(
			mem, nil, nil, "travel", &core.NoOpLogger{},
			&staticFactExtractor{facts: mixedFacts},
			&addAllReconciler{},
			opts...,
		)
	}

	pctx := &core.PipelineContext{
		Request:  "plan my next trip",
		Metadata: map[string]interface{}{"user_id": "user-1"},
	}

	// Run in sync mode and collect stored facts.
	syncMem := newTestUserMemory()
	syncHook := buildHook(syncMem, false)
	_, err := syncHook.AfterSynthesis(context.Background(), pctx, "here is your plan")
	require.NoError(t, err)
	require.NoError(t, syncHook.Close())
	syncFacts, _ := syncMem.Recall(context.Background(), "user-1", "travel", "", 50)

	// Run in async mode, drain via Close, collect stored facts.
	asyncMem := newTestUserMemory()
	asyncHook := buildHook(asyncMem, true)
	_, err = asyncHook.AfterSynthesis(context.Background(), pctx, "here is your plan")
	require.NoError(t, err)
	require.NoError(t, asyncHook.Close())
	asyncFacts, _ := asyncMem.Recall(context.Background(), "user-1", "travel", "", 50)

	// Same count.
	require.Equal(t, len(syncFacts), len(asyncFacts), "async must store the same number of facts as sync")

	// Same content sets. FactIDs differ between runs because the stub reconciler
	// generates them from time.Now(), so compare on (Content, Category, Source).
	type factKey struct {
		content  string
		category string
		source   core.FactSource
	}
	toSet := func(facts []core.UserFact) map[factKey]struct{} {
		out := make(map[factKey]struct{}, len(facts))
		for _, f := range facts {
			out[factKey{f.Content, f.Category, f.Source}] = struct{}{}
		}
		return out
	}
	assert.Equal(t, toSet(syncFacts), toSet(asyncFacts), "async must store the same facts (by content/category/source) as sync")
}
