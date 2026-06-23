package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// mapDigestCache is a minimal in-memory core.DigestCache for tests.
type mapDigestCache struct {
	mu   sync.Mutex
	data map[string][]byte
	gets int
	sets int
}

func newMapDigestCache() *mapDigestCache { return &mapDigestCache{data: map[string][]byte{}} }

func (m *mapDigestCache) GetDigest(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	return m.data[key], nil
}

func (m *mapDigestCache) SetDigest(_ context.Context, key string, data []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sets++
	m.data[key] = data
	return nil
}

// countingProcessor records how many times ProcessForPrompt actually ran. When
// nonCacheable is set it flags its result via markResultNonCacheable (mimicking the
// distiller's fallback / map-reduce partial paths).
type countingProcessor struct {
	calls        int
	output       string
	nonCacheable bool
}

func (c *countingProcessor) ProcessForPrompt(ctx context.Context, result string, _ int, _ ResultProcessorContext) string {
	c.calls++
	if c.nonCacheable {
		markResultNonCacheable(ctx)
	}
	if c.output != "" {
		return c.output
	}
	return "processed:" + result
}

func TestNewCachingProcessor_NilCacheReturnsInner(t *testing.T) {
	inner := &countingProcessor{}
	got := NewCachingProcessor(inner, nil, time.Minute, 0, "", nil)
	if _, isWrapped := got.(*cachingProcessor); isWrapped {
		t.Fatal("nil cache must return the bare inner processor (fail-open), not a wrapper")
	}
	if got != ResultProcessor(inner) {
		t.Errorf("nil cache must return the same inner processor")
	}
}

func TestCachingProcessor_HitRunsInnerOnce(t *testing.T) {
	inner := &countingProcessor{output: "DISTILLED"}
	cache := newMapDigestCache()
	p := NewCachingProcessor(inner, cache, time.Minute, 10, "", nil)

	big := strings.Repeat("x", 100) // >= minBytes → cacheable compaction path
	sc := ResultProcessorContext{StepID: "s1", Instruction: "find errors"}

	out1 := p.ProcessForPrompt(context.Background(), big, 10, sc)
	out2 := p.ProcessForPrompt(context.Background(), big, 10, sc)

	if out1 != "DISTILLED" || out2 != "DISTILLED" {
		t.Errorf("expected cached output both times, got %q and %q", out1, out2)
	}
	if inner.calls != 1 {
		t.Errorf("inner processor must run once for identical inputs, ran %d times", inner.calls)
	}
	if cache.sets != 1 {
		t.Errorf("expected exactly one cache write, got %d", cache.sets)
	}
}

func TestCachingProcessor_BelowThresholdSkipsCache(t *testing.T) {
	inner := &countingProcessor{}
	cache := newMapDigestCache()
	// minBytes=100: inputs below the distiller's work threshold do no expensive work,
	// so they must bypass the cache entirely (no Redis traffic to recompute cheap output).
	p := NewCachingProcessor(inner, cache, time.Minute, 100, "", nil)

	small := "tiny" // 4 bytes < minBytes → below the work threshold
	_ = p.ProcessForPrompt(context.Background(), small, 1000, ResultProcessorContext{Instruction: "x"})

	if cache.gets != 0 || cache.sets != 0 {
		t.Errorf("below-threshold input must skip the cache, got gets=%d sets=%d", cache.gets, cache.sets)
	}
	if inner.calls != 1 {
		t.Errorf("inner must still be invoked for below-threshold input, ran %d", inner.calls)
	}
}

func TestCachingProcessor_BudgetIsPartOfKey(t *testing.T) {
	inner := &countingProcessor{}
	cache := newMapDigestCache()
	p := NewCachingProcessor(inner, cache, time.Minute, 10, "", nil)

	big := strings.Repeat("y", 100)
	sc := ResultProcessorContext{Instruction: "same"}

	// Same content + instruction but different budgets must NOT share a cache entry —
	// the output is bounded by the budget, so a smaller budget cannot reuse a larger result.
	p.ProcessForPrompt(context.Background(), big, 10, sc)
	p.ProcessForPrompt(context.Background(), big, 20, sc)

	if inner.calls != 2 {
		t.Errorf("different budgets must miss the cache (inner runs twice), ran %d", inner.calls)
	}
}

func TestCachingProcessor_FailOpenOnCacheErrors(t *testing.T) {
	inner := &countingProcessor{output: "OK"}
	failing := &errDigestCache{}
	p := NewCachingProcessor(inner, failing, time.Minute, 10, "", nil)

	big := strings.Repeat("z", 100)
	out := p.ProcessForPrompt(context.Background(), big, 10, ResultProcessorContext{Instruction: "q"})

	if out != "OK" {
		t.Errorf("cache errors must fall through to the inner result, got %q", out)
	}
	if inner.calls != 1 {
		t.Errorf("inner must run when the cache errors, ran %d", inner.calls)
	}
}

func TestDistillCacheKey_DistinguishesInputs(t *testing.T) {
	base := distillCacheKey("result-body", "instruction", "query", 4096)

	cases := map[string]string{
		"different body":        distillCacheKey("other-body", "instruction", "query", 4096),
		"different instruction": distillCacheKey("result-body", "other", "query", 4096),
		"different query":       distillCacheKey("result-body", "instruction", "other-query", 4096),
		"different budget":      distillCacheKey("result-body", "instruction", "query", 2048),
		// Boundary ambiguity: ("ab","") must not collide with ("a","b").
		"shifted boundary": distillCacheKey("a", "b", "query", 4096),
	}
	if base == distillCacheKey("ab", "", "query", 4096) {
		t.Error("instruction/body boundary must be unambiguous")
	}
	for name, k := range cases {
		if k == base {
			t.Errorf("%s should produce a distinct key", name)
		}
	}
	// Determinism.
	if base != distillCacheKey("result-body", "instruction", "query", 4096) {
		t.Error("key must be deterministic for identical inputs")
	}
	if !strings.HasPrefix(base, "distill:") {
		t.Errorf("key must be namespaced with 'distill:', got %q", base)
	}
}

func TestCachingProcessor_DoesNotCacheNonCacheableResults(t *testing.T) {
	// A producer flags fallback / partial-on-timeout results via markResultNonCacheable;
	// the cache must honor that, or a transient failure would be served for the full TTL.
	inner := &countingProcessor{output: "degraded fallback", nonCacheable: true}
	cache := newMapDigestCache()
	p := NewCachingProcessor(inner, cache, time.Minute, 10, "", nil)

	big := strings.Repeat("p", 100)
	sc := ResultProcessorContext{Instruction: "q"}
	p.ProcessForPrompt(context.Background(), big, 10, sc)
	p.ProcessForPrompt(context.Background(), big, 10, sc)

	if cache.sets != 0 {
		t.Errorf("non-cacheable results must not be cached, got %d sets", cache.sets)
	}
	if inner.calls != 2 {
		t.Errorf("non-cacheable result must be recomputed each call, inner ran %d", inner.calls)
	}
}

// lifecycleSpy implements ResultProcessor plus the optional Shutdown / SetLLMDebugStore
// hooks, to verify the cache wrapper forwards them to the inner processor.
type lifecycleSpy struct {
	shutdownCalled bool
	debugStoreSet  bool
}

func (s *lifecycleSpy) ProcessForPrompt(_ context.Context, r string, _ int, _ ResultProcessorContext) string {
	return r
}
func (s *lifecycleSpy) Shutdown()                      { s.shutdownCalled = true }
func (s *lifecycleSpy) SetLLMDebugStore(LLMDebugStore) { s.debugStoreSet = true }

func TestCachingProcessor_ForwardsLifecycleHooks(t *testing.T) {
	spy := &lifecycleSpy{}
	p := NewCachingProcessor(spy, newMapDigestCache(), time.Minute, 10, "", nil)

	cp, ok := p.(*cachingProcessor)
	if !ok {
		t.Fatal("expected a *cachingProcessor wrapper")
	}
	cp.SetLLMDebugStore(nil)
	cp.Shutdown()

	if !spy.debugStoreSet {
		t.Error("SetLLMDebugStore must forward to the inner processor")
	}
	if !spy.shutdownCalled {
		t.Error("Shutdown must forward to the inner processor")
	}
}

// errDigestCache fails every operation, to exercise the fail-open path.
type errDigestCache struct{}

func (errDigestCache) GetDigest(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("get boom")
}
func (errDigestCache) SetDigest(context.Context, string, []byte, time.Duration) error {
	return fmt.Errorf("set boom")
}

// TestCachingProcessor_DistillerFallbackNotCached is an end-to-end guard for Finding 1:
// when the LLM call fails and the distiller falls back to a structural trim, the cache
// must not store that degraded output (a transient provider failure must not poison the
// cache for the TTL).
func TestCachingProcessor_DistillerFallbackNotCached(t *testing.T) {
	failAI := &distillerMockAI{err: fmt.Errorf("provider timeout")}
	cfg := ResultDistillConfig{Enabled: true, DistillThreshold: 10, PreFilterBudget: 500, TargetSize: 200}
	distiller := NewLLMDistiller(failAI, cfg, NewStructuralTrimmer(nil, nil), nil)
	cache := newMapDigestCache()
	p := NewCachingProcessor(distiller, cache, time.Minute, cfg.DistillThreshold, "", nil)

	// >= DistillThreshold so distillation is attempted; > maxBytes so the fallback trims.
	input := `{"a":"` + strings.Repeat("z", 100) + `"}`
	out := p.ProcessForPrompt(context.Background(), input, 50, ResultProcessorContext{Instruction: "summarize"})

	if out == "" {
		t.Error("expected non-empty fallback output")
	}
	if cache.sets != 0 {
		t.Errorf("distiller fallback after LLM error must not be cached, got %d sets", cache.sets)
	}
}

// TestCachingProcessor_KeySaltSeparatesConfigs guards Finding 5: caches with different
// output-affecting config (model/target/prompt version → different keySalt) must not
// share entries, even for identical (result, instruction, budget).
func TestCachingProcessor_KeySaltSeparatesConfigs(t *testing.T) {
	cache := newMapDigestCache() // shared backing store (e.g. same Redis instance)
	innerA := &countingProcessor{output: "FROM-MODEL-A"}
	innerB := &countingProcessor{output: "FROM-MODEL-B"}
	pA := NewCachingProcessor(innerA, cache, time.Minute, 10, distillKeySalt(ResultDistillConfig{Model: "modelA", TargetSize: 4096}, nil), nil)
	pB := NewCachingProcessor(innerB, cache, time.Minute, 10, distillKeySalt(ResultDistillConfig{Model: "modelB", TargetSize: 4096}, nil), nil)

	big := strings.Repeat("x", 100)
	sc := ResultProcessorContext{Instruction: "same"}

	if got := pA.ProcessForPrompt(context.Background(), big, 10, sc); got != "FROM-MODEL-A" {
		t.Fatalf("A: got %q", got)
	}
	// Same content/instruction/budget but a different model must NOT hit A's entry.
	if got := pB.ProcessForPrompt(context.Background(), big, 10, sc); got != "FROM-MODEL-B" {
		t.Errorf("different model must not reuse another config's cached output, got %q", got)
	}
	if innerB.calls != 1 {
		t.Errorf("model B must compute its own result (cache miss), inner ran %d", innerB.calls)
	}
}

func TestDistillKeySalt_DistinguishesConfig(t *testing.T) {
	base := distillKeySalt(ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 32768}, nil)
	if base == distillKeySalt(ResultDistillConfig{Model: "smart", TargetSize: 4096, PreFilterBudget: 32768}, nil) {
		t.Error("different model must produce a different salt")
	}
	if base == distillKeySalt(ResultDistillConfig{Model: "fast", TargetSize: 2048, PreFilterBudget: 32768}, nil) {
		t.Error("different target size must produce a different salt")
	}
	if base == distillKeySalt(ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 65536}, nil) {
		t.Error("different pre-filter budget must produce a different salt")
	}
	cfg := ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 32768}
	if distillKeySalt(cfg, nil) == distillKeySalt(cfg, &AIOptionsOverride{Model: StringPtr("override-model")}) {
		t.Error("an AI options override must produce a different salt")
	}
	if distillKeySalt(cfg, &AIOptionsOverride{Temperature: Float32Ptr(0.1)}) ==
		distillKeySalt(cfg, &AIOptionsOverride{Temperature: Float32Ptr(0.9)}) {
		t.Error("a different override temperature must produce a different salt")
	}
	// Determinism.
	if base != distillKeySalt(ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 32768}, nil) {
		t.Error("salt must be deterministic for identical config")
	}
}
