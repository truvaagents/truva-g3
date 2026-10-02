package core

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMemoryNumericEnvironment(t *testing.T) {
	defaults := DefaultConfig()
	for _, setting := range defaults.memoryIntSettings() {
		t.Run(setting.key, func(t *testing.T) {
			for _, raw := range []string{"", "garbage-private-value", "-1", "1.5", "9999999999999999999999999999999"} {
				cfg := DefaultConfig()
				before := cfg.SharedMemory
				err := cfg.loadMemoryNumericEnv(func(key string) (string, bool) { return raw, key == setting.key })
				if !errors.Is(err, ErrInvalidConfiguration) || !strings.Contains(err.Error(), setting.key) || strings.Contains(err.Error(), "garbage-private-value") {
					t.Fatalf("%q: error missing key/identity or exposed value: %v", raw, err)
				}
				if cfg.SharedMemory != before || cfg.ActivityCoordination != defaults.ActivityCoordination {
					t.Fatal("invalid input partially committed numeric settings")
				}
			}
			for _, value := range []int{0, setting.minimum, setting.maximum} {
				cfg := DefaultConfig()
				err := cfg.loadMemoryNumericEnv(func(key string) (string, bool) { return strconv.Itoa(value), key == setting.key })
				if (err == nil) != (value >= setting.minimum && value <= setting.maximum) {
					t.Fatalf("%d boundary: %v", value, err)
				}
			}
		})
	}
	for _, setting := range defaults.memoryDurationSettings() {
		t.Run(setting.key, func(t *testing.T) {
			for _, raw := range []string{"", "bad", "0", "-1s", "999999999999999999h"} {
				cfg := DefaultConfig()
				if err := cfg.loadMemoryNumericEnv(func(key string) (string, bool) { return raw, key == setting.key }); !errors.Is(err, ErrInvalidConfiguration) {
					t.Fatalf("%q accepted: %v", raw, err)
				}
			}
			cfg := DefaultConfig()
			if err := cfg.loadMemoryNumericEnv(func(key string) (string, bool) { return "1h2m3.5s", key == setting.key }); err != nil {
				t.Fatal(err)
			}
			for _, got := range cfg.memoryDurationSettings() {
				if got.key == setting.key && *got.value != time.Hour+2*time.Minute+3500*time.Millisecond {
					t.Fatal("compound duration not preserved")
				}
			}
		})
	}
	for _, raw := range []string{"", "bad", "0", "-1", "9223372036854775808"} {
		cfg := DefaultConfig()
		if err := cfg.loadMemoryNumericEnv(func(key string) (string, bool) { return raw, key == "TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN" }); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("stream %q accepted: %v", raw, err)
		}
	}
	cfg := DefaultConfig()
	if err := cfg.loadMemoryNumericEnv(func(key string) (string, bool) {
		return "9223372036854775807", key == "TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN"
	}); err != nil || cfg.SharedMemory.StreamMaxLen != math.MaxInt64 {
		t.Fatalf("stream int64 boundary: %v", err)
	}
}

func TestMemoryNumericDefaultsAndTypedValidation(t *testing.T) {
	cfg := DefaultConfig()
	before := *cfg
	if err := cfg.loadMemoryNumericEnv(func(string) (string, bool) { return "", false }); err != nil || !reflect.DeepEqual(*cfg, before) {
		t.Fatalf("absent settings changed defaults: %v", err)
	}
	if cfg.SharedMemory.RecentEventsLimit != 20 || cfg.SharedMemory.EnrichmentSummaryMaxTokens != 500 || cfg.SharedMemory.CompactionRawLimit != 200 || cfg.SharedMemory.CompactionRecentDetail != 15 || cfg.SharedMemory.DigestCacheTTL != 5*time.Minute || cfg.SharedMemory.DigestIncrementalThreshold != 20 {
		t.Fatal("documented defaults not installed")
	}
	for index := range cfg.memoryIntSettings() {
		changed := DefaultConfig()
		*changed.memoryIntSettings()[index].value = -1
		if !errors.Is(changed.Validate(), ErrInvalidConfiguration) {
			t.Fatal("invalid typed integer accepted")
		}
	}
	for index := range cfg.memoryDurationSettings() {
		changed := DefaultConfig()
		*changed.memoryDurationSettings()[index].value = 0
		if !errors.Is(changed.Validate(), ErrInvalidConfiguration) {
			t.Fatal("invalid typed duration accepted")
		}
	}
	cfg.SharedMemory.StreamMaxLen = 0
	if !errors.Is(cfg.Validate(), ErrInvalidConfiguration) {
		t.Fatal("invalid typed stream length accepted")
	}
	for _, weight := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, field := range []string{"Recency", "Relevance", "Importance"} {
			cfg := DefaultConfig()
			reflect.ValueOf(&cfg.SharedMemory.DefaultRetrievalWeights).Elem().FieldByName(field).SetFloat(weight)
			if !errors.Is(cfg.Validate(), ErrInvalidConfiguration) {
				t.Fatal("non-finite weight accepted")
			}
		}
	}
	cfg = DefaultConfig()
	cfg.SharedMemory.CompactionRecentDetail = 0
	cfg.SharedMemory.DefaultRetrievalWeights = RetrievalWeights{Recency: -1, Importance: 2}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid zero/relative signed finite weights changed: %v", err)
	}
}

func TestMemoryConfigurationPublicPaths(t *testing.T) {
	const key = "TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT"
	t.Run("invalid environment even when disabled", func(t *testing.T) {
		t.Setenv(key, "")
		cfg := DefaultConfig()
		cfg.SharedMemory.Provider = "noop"
		cfg.ActivityCoordination.Enabled = false
		if !errors.Is(cfg.LoadFromEnv(), ErrInvalidConfiguration) {
			t.Fatal("explicit empty value ignored")
		}
		if _, err := NewConfig(); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("construction lost environment error: %v", err)
		}
	})
	t.Run("valid option overrides valid environment", func(t *testing.T) {
		t.Setenv(key, "21")
		cfg, err := NewConfig(func(c *Config) error { c.SharedMemory.RecentEventsLimit = 22; return nil })
		if err != nil || cfg.SharedMemory.RecentEventsLimit != 22 {
			t.Fatalf("valid precedence changed: %v", err)
		}
		if _, err := NewConfig(func(c *Config) error { c.SharedMemory.RecentEventsLimit = 0; return nil }); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("invalid final option accepted: %v", err)
		}
	})
	t.Run("files", func(t *testing.T) {
		for _, content := range []string{
			`{`, `[]`, `{"shared_memory":[]}`,
			`{"shared_memory":{"default_retrieval_weights":"bad"}}`,
			`{"shared_memory":{"recent_events_limit":0}}`,
			`{"activity_coordination":{"signal_ttl":-1}}`,
			`{"shared_memory":{"recent_events_limit":null}}`,
			`{"shared_memory":null}`, `{"activity_coordination":null}`,
			`{"shared_memory":{"default_retrieval_weights":null}}`,
			`{"shared_memory":{"default_retrieval_weights":{"recency":null}}}`,
			`{"shared_memory":{"max_enrichment_tokens":9223372036854775807}}`,
		} {
			file := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(file, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := DefaultConfig()
			before := cfg.SharedMemory
			if !errors.Is(cfg.LoadFromFile(file), ErrInvalidConfiguration) || cfg.SharedMemory != before {
				t.Fatalf("file silently defaulted/committed invalid memory setting: %s", content)
			}
			if _, err := NewConfig(WithConfigFile(file)); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatal("file error lost by constructor")
			}
		}
		file := filepath.Join(t.TempDir(), "config.json")
		data, err := json.Marshal(map[string]any{"shared_memory": map[string]any{"recent_events_limit": 27, "compaction_recent_detail": 0}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		cfg := DefaultConfig()
		if err := cfg.LoadFromFile(file); err != nil || cfg.SharedMemory.RecentEventsLimit != 27 || cfg.SharedMemory.CompactionRecentDetail != 0 || cfg.SharedMemory.DigestCacheTTL != 5*time.Minute {
			t.Fatalf("valid overlay/defaults lost: %v", err)
		}
	})
}
