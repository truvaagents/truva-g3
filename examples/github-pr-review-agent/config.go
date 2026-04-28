package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type ReviewConfig struct {
	Mode        string
	Port        int
	RedisURL    string
	Namespace   string
	WorkerCount int
	TaskTimeout time.Duration

	WebhookSecret string
	ReviewDrafts  bool

	DefaultPost                 bool
	AllowedRepos                AllowedRepoSet
	AllowRequestChanges         bool
	RequestChangesMinConfidence float64
	DryRun                      bool
	PostingDisabled             bool
	PostMinInterval             time.Duration

	MaxWholePRTokens     int
	MaxShardTokens       int
	HugePRTokenThreshold int
	MaxParallelShards    int
	SkipGenerated        bool
	SkipLockfiles        bool

	ManifestModel string
	ShardModel    string
	MergeModel    string
}

type AllowedRepoSet map[string]struct{}

func (s AllowedRepoSet) Contains(fullName string) bool {
	_, ok := s[fullName]
	return ok
}

func LoadReviewConfig() (*ReviewConfig, error) {
	cfg := &ReviewConfig{
		Mode:        os.Getenv("TRUVAG3_MODE"),
		Port:        envInt("PORT", 8382),
		RedisURL:    os.Getenv("REDIS_URL"),
		Namespace:   os.Getenv("NAMESPACE"),
		WorkerCount: envInt("WORKER_COUNT", 3),
		TaskTimeout: envDuration("TRUVAG3_PR_REVIEW_TASK_TIMEOUT", 30*time.Minute),

		WebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		ReviewDrafts:  envBool("TRUVAG3_PR_REVIEW_REVIEW_DRAFTS", false),

		DefaultPost:                 envBool("TRUVAG3_PR_REVIEW_DEFAULT_POST", false),
		AllowedRepos:                parseAllowedRepos(os.Getenv("TRUVAG3_PR_REVIEW_ALLOWED_REPOS")),
		AllowRequestChanges:         envBool("TRUVAG3_PR_REVIEW_ALLOW_REQUEST_CHANGES", false),
		RequestChangesMinConfidence: envFloat("TRUVAG3_PR_REVIEW_REQUEST_CHANGES_MIN_CONFIDENCE", 0.90),
		DryRun:                      envBool("TRUVAG3_PR_REVIEW_DRY_RUN", true),
		PostingDisabled:             envBool("TRUVAG3_PR_REVIEW_POSTING_DISABLED", false),
		PostMinInterval:             envDuration("TRUVAG3_PR_REVIEW_POST_MIN_INTERVAL", time.Hour),

		MaxWholePRTokens:     envInt("TRUVAG3_PR_REVIEW_MAX_WHOLE_PR_TOKENS", 80000),
		MaxShardTokens:       envInt("TRUVAG3_PR_REVIEW_MAX_SHARD_TOKENS", 60000),
		HugePRTokenThreshold: envInt("TRUVAG3_PR_REVIEW_HUGE_PR_TOKEN_THRESHOLD", 500000),
		MaxParallelShards:    envInt("TRUVAG3_PR_REVIEW_MAX_PARALLEL_SHARDS", 4),
		SkipGenerated:        envBool("TRUVAG3_PR_REVIEW_SKIP_GENERATED", true),
		SkipLockfiles:        envBool("TRUVAG3_PR_REVIEW_SKIP_LOCKFILES", true),

		ManifestModel: envOrDefault("TRUVAG3_PR_REVIEW_MANIFEST_MODEL", "fast"),
		ShardModel:    envOrDefault("TRUVAG3_PR_REVIEW_SHARD_MODEL", "smart"),
		MergeModel:    envOrDefault("TRUVAG3_PR_REVIEW_MERGE_MODEL", "fast"),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *ReviewConfig) Validate() error {
	if c.RedisURL == "" {
		return fmt.Errorf("REDIS_URL environment variable required")
	}
	if !strings.HasPrefix(c.RedisURL, "redis://") && !strings.HasPrefix(c.RedisURL, "rediss://") {
		return fmt.Errorf("invalid REDIS_URL format; expected redis:// or rediss://")
	}
	if c.Mode != "" && c.Mode != "api" && c.Mode != "worker" {
		return fmt.Errorf("invalid TRUVAG3_MODE %q; expected 'api', 'worker', or unset", c.Mode)
	}
	if c.WorkerCount < 1 {
		c.WorkerCount = 1
	}
	// Model aliases must be non-empty — they're passed verbatim to the AI
	// client's Model field. An empty Model can lead to silent fallbacks or
	// provider-specific surprises. Restore defaults if an operator set them
	// to empty in .env (e.g. `TRUVAG3_PR_REVIEW_SHARD_MODEL=`).
	if c.ManifestModel == "" {
		c.ManifestModel = "fast"
	}
	if c.ShardModel == "" {
		c.ShardModel = "smart"
	}
	if c.MergeModel == "" {
		c.MergeModel = "fast"
	}
	return nil
}

func parseAllowedRepos(csv string) AllowedRepoSet {
	set := AllowedRepoSet{}
	for _, r := range strings.Split(csv, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			set[r] = struct{}{}
		}
	}
	return set
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return def
	}
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
