package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

type observedCacheFixture core.MockEpisodicMemory

// A deterministic test compactor has no AI policy to vary.
type stableCacheCompactor struct{ core.MockActivityCompactor }

func (*stableCacheCompactor) aiSemanticFingerprint(context.Context) (string, bool) { return "", true }

type stableTestCompactor struct{ core.ActivityCompactor }

func (c stableTestCompactor) aiSemanticFingerprint(ctx context.Context) (string, bool) {
	if f, ok := c.ActivityCompactor.(aiSemanticCacheFingerprinter); ok {
		return f.aiSemanticFingerprint(ctx)
	}
	return "", true
}

func withStableTestCompactor(c core.ActivityCompactor) MemoryEnrichmentOption {
	return WithActivityCompactor(stableTestCompactor{c})
}

func (m *observedCacheFixture) RecordEvent(ctx context.Context, event core.AgentEvent) error {
	return (*core.MockEpisodicMemory)(m).RecordEvent(ctx, event)
}
func (m *observedCacheFixture) QueryEvents(ctx context.Context, domain string, filter core.EventFilter) ([]core.AgentEvent, error) {
	return (*core.MockEpisodicMemory)(m).QueryEvents(ctx, domain, filter)
}
func (m *observedCacheFixture) QueryEventsObserved(ctx context.Context, domain string, filter core.EventFilter) ([]core.AgentEvent, error) {
	return m.QueryRecentEvents(ctx, domain, filter.Since, filter.Limit)
}
func (m *observedCacheFixture) QueryRecentEvents(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
	return (*core.MockEpisodicMemory)(m).QueryRecentEvents(ctx, domain, since, limit)
}
func (m *observedCacheFixture) QueryEntityHistory(ctx context.Context, domain, typ, id string, since time.Time) ([]core.AgentEvent, error) {
	return (*core.MockEpisodicMemory)(m).QueryEntityHistory(ctx, domain, typ, id, since)
}
func (m *observedCacheFixture) DeleteEvents(ctx context.Context, ids []string) error {
	return (*core.MockEpisodicMemory)(m).DeleteEvents(ctx, ids)
}

func testDigestWindow(t *testing.T, events []core.AgentEvent) *digestWindow {
	t.Helper()
	h, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test-agent", "test-domain")
	require.NoError(t, err)
	w, err := h.digestWindow(events)
	require.NoError(t, err)
	return w
}

func TestDigestWindowReuseAndLateEvents(t *testing.T) {
	events := makeEvents(3, time.Now().Add(-time.Hour))
	var stored []byte
	cache := &core.MockDigestCache{
		GetFn: func(context.Context, string) ([]byte, error) { return stored, nil },
		SetFn: func(_ context.Context, _ string, data []byte, _ time.Duration) error {
			stored = append([]byte(nil), data...)
			return nil
		},
	}
	var seenDelta []core.AgentEvent
	compactor := &stableCacheCompactor{core.MockActivityCompactor{
		CompactEventsFn: func(context.Context, []core.AgentEvent, int) (string, error) { return "bounded digest", nil },
		UpdateDigestFn: func(_ context.Context, _ string, delta []core.AgentEvent, _ int) (string, error) {
			seenDelta = delta
			return "updated digest", nil
		},
	}}
	h, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test-agent", "test-domain", WithActivityCompactor(compactor), WithDigestCache(cache), WithCompactionRawLimit(4))
	require.NoError(t, err)
	_, path, _, _ := h.compactWindow(t.Context(), events, true)
	require.Equal(t, "full", path)
	_, path, _, _ = h.compactWindow(t.Context(), events, true)
	require.Equal(t, "cached", path)
	require.Equal(t, 1, compactor.CompactEventsCt)
	require.Zero(t, compactor.UpdateDigestCt)
	late := events[0]
	late.EventID = "late-same-timestamp"
	events = append(events, late)
	_, path, _, coverage := h.compactWindow(t.Context(), events, true)
	require.Equal(t, "incremental", path)
	require.Equal(t, "limited_window", coverage)
	require.Equal(t, []core.AgentEvent{late}, seenDelta)
	_, path, _, _ = h.compactWindow(t.Context(), events, true)
	require.Equal(t, "cached", path, "a saturated unchanged page is still reusable")
	events[0].Summary = "corrected event"
	_, path, _, _ = h.compactWindow(t.Context(), events, true)
	require.Equal(t, "full_recompact", path, "payload edits cannot be mistaken for covered IDs")
	events = events[1:]
	_, path, _, _ = h.compactWindow(t.Context(), events, true)
	require.Equal(t, "full_recompact", path, "expiry/page eviction replaces, rather than accumulates, old evidence")
	var decoded cachedDigestData
	require.NoError(t, json.Unmarshal(stored, &decoded))
	require.Len(t, decoded.Window.Events, 3)
	before := append([]byte(nil), stored...)
	h.compactor = &stableCacheCompactor{core.MockActivityCompactor{CompactEventsFn: func(context.Context, []core.AgentEvent, int) (string, error) { return "", errors.New("unavailable") }}}
	events[0].Summary = "another correction"
	digest, path, _, _ := h.compactWindow(t.Context(), events, true)
	require.Empty(t, digest)
	require.Equal(t, "raw_fallback", path)
	require.Equal(t, before, stored, "failure leaves cache content and evidence together")
}

func TestDigestWindowUnknownReadBypassesCache(t *testing.T) {
	cache := &core.MockDigestCache{}
	h, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test-agent", "test-domain", WithActivityCompactor(&core.MockActivityCompactor{}), WithDigestCache(cache))
	require.NoError(t, err)
	_, _, _, coverage := h.compactWindow(t.Context(), makeEvents(2, time.Now()), false)
	require.Equal(t, "unknown", coverage)
	require.Zero(t, cache.GetCt)
	require.Zero(t, cache.SetCt)
}

func TestDigestWindowInvalidIdentityAndPolicy(t *testing.T) {
	events := makeEvents(2, time.Now())
	w := testDigestWindow(t, events)
	require.True(t, validDigestWindow(w))
	reordered := testDigestWindow(t, []core.AgentEvent{events[1], events[0]})
	require.Equal(t, w.Fingerprint, reordered.Fingerprint)
	delta, extends := windowDelta(w, reordered, []core.AgentEvent{events[1], events[0]})
	require.True(t, extends)
	require.Empty(t, delta)
	for _, mutate := range []func(*digestWindow){
		func(w *digestWindow) { w.Fingerprint = "wrong" },
		func(w *digestWindow) { w.Events[0] = "invalid" },
		func(w *digestWindow) { w.Events = append(w.Events, w.Events[0]) },
		func(w *digestWindow) { w.Events = nil },
		func(w *digestWindow) { w.Coverage = "complete-history" },
	} {
		invalid := testDigestWindow(t, events)
		mutate(invalid)
		_, extends = windowDelta(invalid, w, events)
		require.False(t, extends)
	}
	h, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test-agent", "test-domain", WithCompactionRawLimit(1))
	require.NoError(t, err)
	_, err = h.digestWindow(events)
	require.Error(t, err)
	h.compactionRawLimit = 10
	_, err = h.digestWindow(append(events, events[0]))
	require.Error(t, err)
	w2, err := h.digestWindow(events)
	require.NoError(t, err)
	_, extends = windowDelta(w, w2, events)
	require.False(t, extends, "changed configuration requires rebaseline")
}

func TestDigestWindowReadFailureLeavesCacheUntouched(t *testing.T) {
	cache := &core.MockDigestCache{}
	compactor := &stableCacheCompactor{}
	episodic := &observedCacheFixture{QueryRecentEventsFn: func(context.Context, string, time.Time, int) ([]core.AgentEvent, error) {
		return nil, errors.New("backend unavailable")
	}}
	h, err := NewMemoryEnrichmentHook(episodic, nil, "agent", "domain", WithActivityCompactor(compactor), WithDigestCache(cache))
	require.NoError(t, err)
	pctx := &core.PipelineContext{}
	holder := newPipelineHookExecutionHolder()
	ctx := withPipelineHookExecutionHolder(t.Context(), holder)
	invocation := beginPipelineHookInvocation(ctx, h.Name(), PipelineHookPhaseBeforePlanning, 1, 0, time.Now())
	_, err = h.BeforePlanning(invocation.Context(ctx), pctx)
	invocation.Complete(PipelineHookSucceeded, nil, PipelineHookDecision{FailurePolicy: PipelineHookFailOpen, Action: PipelineHookContinue})
	require.NoError(t, err, "optional memory remains fail-open")
	require.Zero(t, cache.GetCt)
	require.Zero(t, cache.SetCt)
	require.Zero(t, compactor.CompactEventsCt)
	require.Empty(t, pctx.Enrichments[core.EnrichmentRAGContext])
	require.NotEmpty(t, holder.Snapshot()[0].Effects)
	read := holder.Snapshot()[0].Effects[0]
	require.Equal(t, "memory_episodic_read", read.EffectID)
	require.Equal(t, core.PipelineHookEffectFailed, read.Status)
	require.Equal(t, "backend unavailable", read.Error)
}

func TestDigestWindowCustomCompactorWithoutIdentityBypassesCache(t *testing.T) {
	cache := &core.MockDigestCache{}
	compactor := &core.MockActivityCompactor{CompactEventsFn: func(context.Context, []core.AgentEvent, int) (string, error) { return "custom", nil }}
	h, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "agent", "domain", WithActivityCompactor(compactor), WithDigestCache(cache))
	require.NoError(t, err)
	digest, _, _, _ := h.compactWindow(t.Context(), makeEvents(1, time.Now()), true)
	require.Equal(t, "custom", digest)
	require.Zero(t, cache.GetCt)
	require.Zero(t, cache.SetCt)
}
