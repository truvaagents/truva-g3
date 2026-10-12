package orchestration

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestMemoryHookConfigurationInvalid(t *testing.T) {
	for _, key := range []string{
		"TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS",
		"TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT",
		"TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS",
		"TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT",
		"TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL",
		"TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD",
		"TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL",
		"TRUVAG3_ACTIVITY_SIGNAL_TTL",
		"TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN",
		"TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT",
		"TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_MAX_TOKENS",
		"TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_TIMEOUT",
	} {
		for _, value := range []string{"", "private-invalid-input", "-1", "9999999999999999999999999999", "0"} {
			if value == "0" && key == "TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL" {
				continue
			}
			t.Run(key+"/"+value, func(t *testing.T) {
				t.Setenv(key, value)
				for _, deps := range []*core.SharedMemoryDeps{nil, {}, {AgentName: "test", Episodic: &core.MockEpisodicMemory{}}} {
					for _, ai := range []core.AIClient{nil, &summarizerMockAI{}} {
						hooks, coord, err := BuildMemoryHooks(deps, ai, nil)
						require.ErrorIs(t, err, core.ErrInvalidConfiguration)
						require.Contains(t, err.Error(), key)
						require.NotContains(t, err.Error(), "private-invalid-input")
						require.Nil(t, hooks)
						require.Nil(t, coord)
					}
				}
			})
		}
	}
	t.Run("four byte overflow", func(t *testing.T) {
		t.Setenv("TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS", strconv.Itoa(int(^uint(0)>>1)/4+1))
		_, _, err := BuildMemoryHooks(nil, nil, nil)
		require.ErrorIs(t, err, core.ErrInvalidConfiguration)
	})
}

func TestMemoryHookBuiltInCompactionOverflowFailsConstruction(t *testing.T) {
	t.Setenv("TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS", strconv.Itoa(math.MaxInt/5+1))
	client := &compactorMockAI{}
	hooks, coordinator, err := BuildMemoryHooks(&core.SharedMemoryDeps{
		AgentName: "test", Episodic: &core.MockEpisodicMemory{},
	}, client, nil)
	require.ErrorIs(t, err, core.ErrInvalidConfiguration)
	require.Nil(t, hooks)
	require.Nil(t, coordinator)
	require.Zero(t, client.calls)
}

func TestMemoryHookConfigurationValid(t *testing.T) {
	for key, value := range map[string]string{
		"TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS":         "4000",
		"TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT":           "30",
		"TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS": "600",
		"TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT":          "210",
		"TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL":      "0",
		"TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD":  "22",
		"TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL":              "1m2.5s",
		"TRUVAG3_ACTIVITY_SIGNAL_TTL":                         "2m3.5s",
		"TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN":               "250",
		"TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT":               "12",
		"TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_MAX_TOKENS":   "2000",
		"TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_TIMEOUT":      "43s",
	} {
		t.Setenv(key, value)
	}
	deps := &core.SharedMemoryDeps{
		AgentName: "test", Episodic: &core.MockEpisodicMemory{},
		ActivityCoordinator: &core.MockActivityCoordinator{},
		DigestCache:         &core.MockDigestCache{},
	}
	for _, ai := range []core.AIClient{nil, &summarizerMockAI{}} {
		hooks, coord, err := BuildMemoryHooks(deps, ai, nil)
		require.NoError(t, err)
		require.Same(t, deps.ActivityCoordinator, coord)
		require.Len(t, hooks, 4)
		require.Equal(t, []string{"activity-announcement", "memory-enrichment", "memory-record", "activity-cleanup"},
			[]string{hooks[0].Name(), hooks[1].Name(), hooks[2].Name(), hooks[3].Name()})
		e := hooks[1].(*MemoryEnrichmentHook)
		require.Equal(t, 4000, e.maxTokens)
		require.Equal(t, 30, e.recentEventsLimit)
		require.Equal(t, 600, e.compactionMaxTokens)
		require.Equal(t, 210, e.compactionRawLimit)
		require.Zero(t, e.compactionRecentDetail)
		require.Equal(t, 22, e.incrementalThreshold)
		require.Equal(t, 62500*time.Millisecond, e.digestCacheTTL)
		a := hooks[0].(*ActivityAnnouncementHook)
		require.Equal(t, 123500*time.Millisecond, a.signalTTL)
		require.Equal(t, 250, a.queryMaxLen)
		require.Equal(t, 12, a.filter.(*RecentActivityFilter).MaxSignals)
		if ai != nil {
			require.NotNil(t, e.compactor)
			require.Same(t, deps.DigestCache, e.digestCache)
			require.Equal(t, 2000, hooks[2].(*MemoryRecordHook).summarizer.(*LLMEventSummarizer).maxTokens)
			require.Equal(t, 43*time.Second, hooks[2].(*MemoryRecordHook).summarizer.(*LLMEventSummarizer).timeout)
		} else {
			require.Nil(t, e.compactor)
			require.Nil(t, hooks[2].(*MemoryRecordHook).summarizer)
		}
	}
	hooks, coord, err := BuildMemoryHooks(nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, hooks)
	require.Nil(t, coord)
}

func TestMemoryHookOptionFailures(t *testing.T) {
	for _, opt := range []BuildMemoryHooksOption{
		WithMemoryEntityExtractor(nil), WithBuilderRecordEntityExtractor(nil),
		WithBuilderEnrichmentEntityExtractor(nil), WithMemoryImportanceFunc(nil),
		WithMemoryLookback(0), WithMemoryLookback(-time.Second),
		WithMemoryRetrievalWeights(core.RetrievalWeights{Recency: math.NaN()}),
	} {
		for _, deps := range []*core.SharedMemoryDeps{nil, {AgentName: "test", Episodic: &core.MockEpisodicMemory{}}} {
			hooks, coord, err := BuildMemoryHooks(deps, nil, nil, opt)
			require.Error(t, err)
			require.Nil(t, hooks)
			require.Nil(t, coord)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, weights := range []core.RetrievalWeights{{Recency: value}, {Relevance: value}, {Importance: value}} {
			_, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, nil, "test", "test", WithEnrichmentWeights(weights))
			require.ErrorIs(t, err, core.ErrInvalidConfiguration)
		}
		_, err := NewLLMActivityCompactor(&summarizerMockAI{}, WithActivityCompactorTemperature(float32(value)))
		require.Error(t, err)
		_, err = NewLLMEventSummarizer(&summarizerMockAI{}, WithSummarizerTemperature(float32(value)))
		require.Error(t, err)
	}
	for _, weights := range []core.RetrievalWeights{{}, {Recency: -1, Relevance: 2, Importance: 4}} {
		hooks, _, err := BuildMemoryHooks(&core.SharedMemoryDeps{AgentName: "test", Episodic: &core.MockEpisodicMemory{}}, nil, nil,
			WithMemoryRetrievalWeights(weights), WithMemoryActivityFilter(&RecentActivityFilter{MaxSignals: 2}))
		require.NoError(t, err)
		require.Equal(t, weights, hooks[0].(*MemoryEnrichmentHook).weights)
	}
	_, err := NewActivityAnnouncementHook(&core.MockActivityCoordinator{}, "test", "test", -1)
	require.Error(t, err)
	h, err := NewActivityAnnouncementHook(&core.MockActivityCoordinator{}, "test", "test", 0)
	require.NoError(t, err)
	require.Equal(t, 10, h.filter.(*RecentActivityFilter).MaxSignals)
	_, err = NewLLMEventSummarizer(&summarizerMockAI{}, WithSummarizerTokensPerStep(int(^uint(0)>>1)))
	require.Error(t, err)
	_, err = NewLLMEventSummarizer(&summarizerMockAI{}, WithSummarizerTokensPerStep(int(^uint(0)>>1)), WithSummarizerMaxTokens(300))
	require.NoError(t, err)
}
