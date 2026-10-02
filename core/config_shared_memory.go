package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// These descriptors cover only the numeric shared-memory/activity configuration
// owned by core. Hook-only options and runtime backend errors remain with their
// owning modules. Defaults are installed by DefaultConfig, not during validation.
type memoryIntSetting struct {
	key     string
	value   *int
	minimum int
	maximum int
}

type memoryDurationSetting struct {
	key   string
	value *time.Duration
}

func (c *Config) memoryIntSettings() []memoryIntSetting {
	maxInt := int(^uint(0) >> 1)
	return []memoryIntSetting{
		{"TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS", &c.SharedMemory.MaxEnrichmentTokens, 1, maxInt / 4},
		{"TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT", &c.SharedMemory.RecentEventsLimit, 1, maxInt},
		{"TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS", &c.SharedMemory.EnrichmentSummaryMaxTokens, 1, maxInt},
		{"TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT", &c.SharedMemory.CompactionRawLimit, 1, maxInt},
		{"TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL", &c.SharedMemory.CompactionRecentDetail, 0, maxInt},
		{"TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD", &c.SharedMemory.DigestIncrementalThreshold, 1, maxInt},
		{"TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT", &c.ActivityCoordination.MaxInPrompt, 1, maxInt},
		{"TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN", &c.ActivityCoordination.QueryMaxLen, 1, maxInt},
	}
}

func (c *Config) memoryDurationSettings() []memoryDurationSetting {
	return []memoryDurationSetting{
		{"TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL", &c.SharedMemory.InvestigationTTL},
		{"TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL", &c.SharedMemory.DigestCacheTTL},
		{"TRUVAG3_ACTIVITY_SIGNAL_TTL", &c.ActivityCoordination.SignalTTL},
	}
}

func memoryConfigError(key, requirement string) error {
	// State the setting and constraint, not an untrusted raw environment value.
	return fmt.Errorf("%s %s: %w", key, requirement, ErrInvalidConfiguration)
}

// loadMemoryNumericEnv preserves both sub-configurations on a parsing failure.
// Lookup presence distinguishes an absent setting from an explicitly empty one.
func (c *Config) loadMemoryNumericEnv(lookup func(string) (string, bool)) error {
	next := *c
	for _, setting := range next.memoryIntSettings() {
		if raw, present := lookup(setting.key); present {
			value, err := strconv.Atoi(raw)
			if err != nil || value < setting.minimum || value > setting.maximum {
				return memoryConfigError(setting.key, fmt.Sprintf("must be an integer in [%d, %d]", setting.minimum, setting.maximum))
			}
			*setting.value = value
		}
	}
	for _, setting := range next.memoryDurationSettings() {
		if raw, present := lookup(setting.key); present {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 {
				return memoryConfigError(setting.key, "must be a positive Go duration")
			}
			*setting.value = value
		}
	}
	const streamKey = "TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN"
	if raw, present := lookup(streamKey); present {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return memoryConfigError(streamKey, "must be a positive int64")
		}
		next.SharedMemory.StreamMaxLen = value
	}
	if err := next.validateMemoryNumericConfig(); err != nil {
		return err
	}
	c.SharedMemory, c.ActivityCoordination = next.SharedMemory, next.ActivityCoordination
	return nil
}

func (c *Config) validateMemoryNumericConfig() error {
	for _, setting := range c.memoryIntSettings() {
		if *setting.value < setting.minimum || *setting.value > setting.maximum {
			return memoryConfigError(setting.key, fmt.Sprintf("must be an integer in [%d, %d]", setting.minimum, setting.maximum))
		}
	}
	for _, setting := range c.memoryDurationSettings() {
		if *setting.value <= 0 {
			return memoryConfigError(setting.key, "must be a positive Go duration")
		}
	}
	if c.SharedMemory.StreamMaxLen <= 0 {
		return memoryConfigError("TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN", "must be a positive int64")
	}
	// Relative weights need not sum to one; retain zero and signed finite values.
	// Only non-finite values invalidate the numerical scoring operation.
	weights := c.SharedMemory.DefaultRetrievalWeights
	for _, value := range []float64{weights.Recency, weights.Relevance, weights.Importance} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return memoryConfigError("shared_memory.default_retrieval_weights", "must contain finite values")
		}
	}
	return nil
}

// encoding/json treats null assigned to a scalar as a no-op. A supplied null
// numeric setting is invalid, rather than permission to silently use a default.
func validateMemoryJSONPresence(data []byte) error {
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(data, &sections); err != nil {
		return fmt.Errorf("invalid config document: %w", ErrInvalidConfiguration)
	}
	for _, entry := range []struct {
		section string
		fields  []string
	}{
		{"shared_memory", []string{"stream_max_len", "investigation_ttl", "max_enrichment_tokens", "recent_events_limit", "enrichment_summary_max_tokens", "compaction_raw_limit", "compaction_recent_detail", "digest_cache_ttl", "digest_incremental_threshold", "default_retrieval_weights"}},
		{"activity_coordination", []string{"signal_ttl", "max_in_prompt", "query_max_len"}},
	} {
		section, fields := entry.section, entry.fields
		raw, present := sections[section]
		if !present {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return memoryConfigError(section, "must be an object when supplied")
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return memoryConfigError(section, "must be an object when supplied")
		}
		for _, field := range fields {
			if value, exists := values[field]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return memoryConfigError(section+"."+field, "must have a numeric value when supplied")
			}
		}
		if rawWeights, exists := values["default_retrieval_weights"]; exists {
			var weights map[string]json.RawMessage
			if err := json.Unmarshal(rawWeights, &weights); err != nil {
				return memoryConfigError(section+".default_retrieval_weights", "must be an object")
			}
			for _, name := range []string{"recency", "relevance", "importance"} {
				if value, exists := weights[name]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return memoryConfigError(section+".default_retrieval_weights."+name, "must be finite")
				}
			}
		}
	}
	return nil
}
