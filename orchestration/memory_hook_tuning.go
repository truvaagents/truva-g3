package orchestration

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Only explicit environment overrides are retained. Absent settings keep the
// owning constructor's defaults. Validate with those same option functions,
// before checking optional dependencies; this does not construct or call AI.
type memoryHookTuning struct {
	enrichment   []MemoryEnrichmentOption
	announcement []ActivityAnnouncementOption
	summarizer   []LLMEventSummarizerOption
	maxInPrompt  int
}

func resolveMemoryHookTuning() (memoryHookTuning, error) {
	tuning := memoryHookTuning{maxInPrompt: 10}
	if err := readMemoryHookDuration("TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_TIMEOUT", func(d time.Duration) {
		tuning.summarizer = append(tuning.summarizer, WithSummarizerTimeout(d))
	}); err != nil {
		return memoryHookTuning{}, err
	}
	for _, setting := range []struct {
		key    string
		option func(int) MemoryEnrichmentOption
	}{
		{"TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS", WithEnrichmentMaxTokens},
		{"TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT", WithEnrichmentRecentEventsLimit},
		{"TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS", WithCompactionMaxTokens},
		{"TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT", WithCompactionRawLimit},
		{"TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL", WithCompactionRecentDetail},
		{"TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD", WithIncrementalThreshold},
	} {
		if err := readMemoryHookInt(setting.key, func(n int) error {
			opt := setting.option(n)
			if err := opt(&MemoryEnrichmentHook{}); err != nil {
				return err
			}
			tuning.enrichment = append(tuning.enrichment, opt)
			return nil
		}); err != nil {
			return memoryHookTuning{}, err
		}
	}
	if err := readMemoryHookDuration("TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL", func(d time.Duration) {
		tuning.enrichment = append(tuning.enrichment, WithDigestCacheTTL(d))
	}); err != nil {
		return memoryHookTuning{}, err
	}
	if err := readMemoryHookDuration("TRUVAG3_ACTIVITY_SIGNAL_TTL", func(d time.Duration) {
		tuning.announcement = append(tuning.announcement, WithAnnouncementSignalTTL(d))
	}); err != nil {
		return memoryHookTuning{}, err
	}
	if err := readMemoryHookInt("TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN", func(n int) error {
		opt := WithAnnouncementQueryMaxLen(n)
		if err := opt(&ActivityAnnouncementHook{}); err != nil {
			return err
		}
		tuning.announcement = append(tuning.announcement, opt)
		return nil
	}); err != nil {
		return memoryHookTuning{}, err
	}
	if err := readMemoryHookInt("TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT", func(n int) error {
		if n <= 0 {
			return errPositiveRequired("activity prompt limit")
		}
		tuning.maxInPrompt = n
		return nil
	}); err != nil {
		return memoryHookTuning{}, err
	}
	if err := readMemoryHookInt("TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_MAX_TOKENS", func(n int) error {
		opt := WithSummarizerMaxTokens(n)
		if err := opt(&LLMEventSummarizer{}); err != nil {
			return err
		}
		tuning.summarizer = append(tuning.summarizer, opt)
		return nil
	}); err != nil {
		return memoryHookTuning{}, err
	}
	return tuning, nil
}

func readMemoryHookInt(key string, apply func(int) error) error {
	value, present := os.LookupEnv(key)
	if !present {
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%w: %s must be a representable integer", core.ErrInvalidConfiguration, key)
	}
	if err := apply(n); err != nil {
		// Exclude raw environment text and option errors that echo its value.
		return fmt.Errorf("%w: %s is outside its supported range", core.ErrInvalidConfiguration, key)
	}
	return nil
}

func readMemoryHookDuration(key string, apply func(time.Duration)) error {
	value, present := os.LookupEnv(key)
	if !present {
		return nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return fmt.Errorf("%w: %s must be a positive Go duration", core.ErrInvalidConfiguration, key)
	}
	apply(d)
	return nil
}

func validateMemoryRetrievalWeights(w core.RetrievalWeights) error {
	for _, v := range []float64{w.Recency, w.Relevance, w.Importance} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: memory retrieval weights must be finite", core.ErrInvalidConfiguration)
		}
	}
	// Relative weights may be signed or zero; their sum is not constrained.
	return nil
}
