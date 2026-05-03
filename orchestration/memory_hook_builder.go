package orchestration

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// memoryHookConfig holds resolved behavioural configuration for BuildMemoryHooks.
type memoryHookConfig struct {
	recordExtractor     EntityExtractor // for MemoryRecordHook (AfterExecution)
	enrichmentExtractor EntityExtractor // for MemoryEnrichmentHook (BeforePlanning)
	importanceFunc      func(actionType, outcome string) float64
	retrievalWeights    core.RetrievalWeights
	lookbackWindow      time.Duration
	activityFilter      ActivityFilter
}

// BuildMemoryHooksOption configures behavioural overrides for BuildMemoryHooks.
// Only interfaces and functions — numeric tuning uses env vars.
// Returns error for fail-fast validation (per core/ARCHITECTURE.md §Option Function Pattern).
type BuildMemoryHooksOption func(*memoryHookConfig) error

// WithMemoryEntityExtractor overrides the entity extractor used by BOTH
// MemoryEnrichmentHook and MemoryRecordHook. Use WithBuilderRecordEntityExtractor
// or WithBuilderEnrichmentEntityExtractor for per-hook control.
func WithMemoryEntityExtractor(e EntityExtractor) BuildMemoryHooksOption {
	return func(c *memoryHookConfig) error {
		if e == nil {
			return errNilParam("entity extractor")
		}
		c.recordExtractor = e
		c.enrichmentExtractor = e
		return nil
	}
}

// WithBuilderRecordEntityExtractor overrides the entity extractor used by
// MemoryRecordHook (AfterExecution) at the builder level.
//
// Named WithBuilder* to avoid collision with the existing per-hook option
// WithRecordEntityExtractor (which is a MemoryRecordOption for
// direct construction via NewMemoryRecordHook — Layer 3).
func WithBuilderRecordEntityExtractor(e EntityExtractor) BuildMemoryHooksOption {
	return func(c *memoryHookConfig) error {
		if e == nil {
			return errNilParam("record entity extractor")
		}
		c.recordExtractor = e
		return nil
	}
}

// WithBuilderEnrichmentEntityExtractor overrides the entity extractor used
// by MemoryEnrichmentHook (BeforePlanning) at the builder level.
//
// Named WithBuilder* to avoid collision with the existing per-hook option
// WithEnrichmentEntityExtractor (which is a MemoryEnrichmentOption for
// direct construction via NewMemoryEnrichmentHook — Layer 3).
func WithBuilderEnrichmentEntityExtractor(e EntityExtractor) BuildMemoryHooksOption {
	return func(c *memoryHookConfig) error {
		if e == nil {
			return errNilParam("enrichment entity extractor")
		}
		c.enrichmentExtractor = e
		return nil
	}
}

// WithMemoryImportanceFunc overrides the default verb-prefix importance scorer.
// Use for domain-specific importance rules.
func WithMemoryImportanceFunc(fn func(actionType, outcome string) float64) BuildMemoryHooksOption {
	return func(c *memoryHookConfig) error {
		if fn == nil {
			return errNilParam("importance function")
		}
		c.importanceFunc = fn
		return nil
	}
}

// WithMemoryRetrievalWeights overrides the default knowledge search scoring weights.
func WithMemoryRetrievalWeights(w core.RetrievalWeights) BuildMemoryHooksOption {
	return func(c *memoryHookConfig) error {
		c.retrievalWeights = w
		return nil
	}
}

// WithMemoryLookback overrides the default 24h event history window.
func WithMemoryLookback(d time.Duration) BuildMemoryHooksOption {
	return func(c *memoryHookConfig) error {
		if d <= 0 {
			return errPositiveRequired("lookback duration")
		}
		c.lookbackWindow = d
		return nil
	}
}

// WithMemoryActivityFilter overrides the default RecentActivityFilter.
func WithMemoryActivityFilter(f ActivityFilter) BuildMemoryHooksOption {
	return func(c *memoryHookConfig) error {
		c.activityFilter = f // nil is valid — uses default
		return nil
	}
}

// BuildMemoryHooks creates the standard memory pipeline hooks from backends.
// Reads TRUVAG3_SHARED_MEMORY_* env vars for numeric tuning.
// Accepts options for behavioural overrides (entity extractor, importance scorer, etc.).
// Returns hooks in the correct order + the activity coordinator for status updates.
//
// This is a convenience function — equivalent to manually constructing each hook.
// For full control over every option, construct hooks individually (Layer 3).
//
// Hook ordering: announcement → enrichment → record → extraction → cleanup
func BuildMemoryHooks(sm *core.SharedMemoryDeps, aiClient core.AIClient, logger core.Logger, opts ...BuildMemoryHooksOption) ([]core.PipelineHook, core.ActivityCoordinator) {
	if sm == nil || sm.Episodic == nil {
		return nil, nil
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/orchestration")
	}

	// Apply behavioural options.
	//
	// The enrichment hook and record hook have SEPARATE extractor defaults
	// because they operate at different pipeline stages with different data.
	// See orchestration/notes/LLM_NATIVE_ENTITY_EXTRACTION_PROPOSAL.md §6.2.2.
	//
	// Record extractor (AfterExecution):
	//   - aiClient != nil → LLMEntityExtractor (piggybacks on EventSummarizer)
	//   - aiClient == nil → NoOpEntityExtractor
	//
	// Enrichment extractor (BeforePlanning):
	//   - Always NoOpEntityExtractor. Pre-planning, llm_entities is never
	//     populated. Collision avoidance uses ActivityCoordinator (Tier 1).
	//
	// Override options (all BuildMemoryHooksOption functions):
	//   - WithBuilderRecordEntityExtractor(e)       — record hook only
	//   - WithBuilderEnrichmentEntityExtractor(e)   — enrichment hook only
	//   - WithMemoryEntityExtractor(e)              — both (shorthand)
	var defaultRecordExtractor EntityExtractor = NoOpEntityExtractor{}
	if aiClient != nil {
		defaultRecordExtractor = LLMEntityExtractor{}
	}

	cfg := &memoryHookConfig{
		recordExtractor:     defaultRecordExtractor,
		enrichmentExtractor: NoOpEntityExtractor{},
		importanceFunc:      DefaultImportanceScorer,
		retrievalWeights:    core.RetrievalWeights{Recency: 0.5, Relevance: 0.3, Importance: 0.2},
		lookbackWindow:      24 * time.Hour,
	}
	for _, opt := range opts {
		if opt != nil {
			if err := opt(cfg); err != nil {
				logger.Warn("Invalid BuildMemoryHooks option, using default", map[string]interface{}{
					"operation":  "build_memory_hooks",
					"error":      err.Error(),
					"error_type": "invalid_option",
				})
			}
		}
	}

	domain := sm.AgentDomain
	if domain == "" {
		domain = os.Getenv("TRUVAG3_AGENT_DOMAIN")
		if domain == "" {
			domain = "default"
		}
	}

	// Build hooks — these should not fail given valid Episodic (checked above),
	// but guard against nil to prevent panics if an option fails internally.
	enrichOpts := buildEnrichmentOptions(logger, aiClient, sm, cfg)
	enrichHook, err := NewMemoryEnrichmentHook(sm.Episodic, sm.Coordinator, sm.AgentName, domain, enrichOpts...)
	if err != nil {
		logger.Warn("Failed to create memory enrichment hook", map[string]interface{}{
			"operation": "build_memory_hooks", "error": err.Error(), "error_type": "enrichment_hook_init",
		})
		return nil, nil
	}

	recordOpts := buildRecordOptions(logger, aiClient, cfg)
	recordHook, err := NewMemoryRecordHook(sm.Episodic, sm.Coordinator, sm.AgentName, domain, recordOpts...)
	if err != nil {
		logger.Warn("Failed to create memory record hook", map[string]interface{}{
			"operation": "build_memory_hooks", "error": err.Error(), "error_type": "record_hook_init",
		})
		return nil, nil
	}

	// Assemble in correct order
	var hooks []core.PipelineHook

	// 1. Activity announcement (BeforePlanning — runs first)
	if sm.ActivityCoordinator != nil {
		maxInPrompt := 10
		announceOpts := buildAnnouncementOptions(logger, cfg, &maxInPrompt)
		announceHook, err := NewActivityAnnouncementHook(
			sm.ActivityCoordinator, sm.AgentName, domain, maxInPrompt, announceOpts...,
		)
		if err == nil && announceHook != nil {
			hooks = append(hooks, announceHook)
		}
	}

	// 2. Memory enrichment (BeforePlanning)
	// 3. Memory record (AfterExecution)
	hooks = append(hooks, enrichHook, recordHook)

	// 4. Knowledge extraction (AfterSynthesis — Phase 2 only)
	if sm.Knowledge != nil && sm.Embedder != nil && aiClient != nil {
		extractionHook, err := NewKnowledgeExtractionHook(
			sm.Knowledge, sm.Embedder, aiClient, sm.AgentName, domain,
			WithExtractionLogger(logger),
		)
		if err == nil && extractionHook != nil {
			hooks = append(hooks, extractionHook)
		}
	}

	// 5. Activity cleanup (AfterSynthesis — runs last)
	if sm.ActivityCoordinator != nil {
		cleanupHook, err := NewActivityCleanupHook(sm.ActivityCoordinator, logger)
		if err == nil && cleanupHook != nil {
			hooks = append(hooks, cleanupHook)
		}
	}

	return hooks, sm.ActivityCoordinator
}

// --- Internal helpers: env-var-driven numeric tuning + behavioural config passthrough ---

// buildEnrichmentOptions creates MemoryEnrichmentOption slice from env vars + behavioural config.
func buildEnrichmentOptions(logger core.Logger, aiClient core.AIClient, sm *core.SharedMemoryDeps, cfg *memoryHookConfig) []MemoryEnrichmentOption {
	opts := []MemoryEnrichmentOption{
		WithEnrichmentLogger(logger),
		WithEnrichmentEntityExtractor(cfg.enrichmentExtractor),
		WithEnrichmentWeights(cfg.retrievalWeights),
		WithEnrichmentLookback(cfg.lookbackWindow),
	}

	// Numeric tuning from env vars
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			opts = append(opts, WithEnrichmentRecentEventsLimit(val))
		}
	}

	// Activity compactor (LLM-powered digest)
	if aiClient != nil {
		compactorOpts := []LLMActivityCompactorOption{
			WithActivityCompactorLogger(logger),
		}
		if model := os.Getenv("TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL"); model != "" {
			compactorOpts = append(compactorOpts, WithActivityCompactorModel(model))
		}
		compactor, err := NewLLMActivityCompactor(aiClient, compactorOpts...)
		if err == nil && compactor != nil {
			opts = append(opts, WithActivityCompactor(compactor))

			// Compaction tuning
			if v := os.Getenv("TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS"); v != "" {
				if val, err := strconv.Atoi(v); err == nil && val > 0 {
					opts = append(opts, WithCompactionMaxTokens(val))
				}
			}
			if v := os.Getenv("TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT"); v != "" {
				if val, err := strconv.Atoi(v); err == nil && val > 0 {
					opts = append(opts, WithCompactionRawLimit(val))
				}
			}
			if v := os.Getenv("TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL"); v != "" {
				if val, err := strconv.Atoi(v); err == nil && val >= 0 {
					opts = append(opts, WithCompactionRecentDetail(val))
				}
			}

			// Digest cache
			if sm.DigestCache != nil {
				opts = append(opts, WithDigestCache(sm.DigestCache))
				if v := os.Getenv("TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL"); v != "" {
					if val, err := time.ParseDuration(v); err == nil && val > 0 {
						opts = append(opts, WithDigestCacheTTL(val))
					}
				}
				if v := os.Getenv("TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD"); v != "" {
					if val, err := strconv.Atoi(v); err == nil && val > 0 {
						opts = append(opts, WithIncrementalThreshold(val))
					}
				}
			}
		}
	}

	// Phase 2: knowledge search
	if sm.Knowledge != nil && sm.Embedder != nil {
		opts = append(opts, WithEnrichmentKnowledge(sm.Knowledge, sm.Embedder))
	}

	return opts
}

// buildRecordOptions creates MemoryRecordOption slice from env vars + behavioural config.
func buildRecordOptions(logger core.Logger, aiClient core.AIClient, cfg *memoryHookConfig) []MemoryRecordOption {
	opts := []MemoryRecordOption{
		WithRecordLogger(logger),
		WithRecordEntityExtractor(cfg.recordExtractor),
		WithRecordImportanceFunc(cfg.importanceFunc),
	}

	// Event summarizer (LLM-powered)
	if aiClient != nil {
		summarizerOpts := []LLMEventSummarizerOption{
			WithSummarizerLogger(logger),
		}
		if model := os.Getenv("TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL"); model != "" {
			summarizerOpts = append(summarizerOpts, WithSummarizerModel(model))
		}
		summarizer, err := NewLLMEventSummarizer(aiClient, summarizerOpts...)
		if err == nil {
			opts = append(opts, WithEventSummarizer(summarizer))
		}
	}

	return opts
}

// buildAnnouncementOptions creates ActivityAnnouncementOption slice from env vars + behavioural config.
// Also sets *maxInPrompt from env var if present.
func buildAnnouncementOptions(logger core.Logger, cfg *memoryHookConfig, maxInPrompt *int) []ActivityAnnouncementOption {
	opts := []ActivityAnnouncementOption{
		WithAnnouncementLogger(logger),
	}

	if v := os.Getenv("TRUVAG3_ACTIVITY_SIGNAL_TTL"); v != "" {
		if val, err := time.ParseDuration(v); err == nil && val > 0 {
			opts = append(opts, WithAnnouncementSignalTTL(val))
		}
	}
	if v := os.Getenv("TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			opts = append(opts, WithAnnouncementQueryMaxLen(val))
		}
	}
	if v := os.Getenv("TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			*maxInPrompt = val
		}
	}

	if cfg.activityFilter != nil {
		opts = append(opts, WithAnnouncementFilter(cfg.activityFilter))
	}

	return opts
}

// errNilParam returns a formatted error for nil parameter validation.
func errNilParam(name string) error {
	return fmt.Errorf("%s cannot be nil", name)
}

// errPositiveRequired returns a formatted error for non-positive value validation.
func errPositiveRequired(name string) error {
	return fmt.Errorf("%s must be positive", name)
}
