package orchestration

import (
	"fmt"
	"os"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// memoryHookConfig holds resolved behavioural configuration for BuildMemoryHooks.
type memoryHookConfig struct {
	recordExtractor        EntityExtractor // for MemoryRecordHook (AfterExecution)
	recordRequestExtractor EntityExtractor // opt-in request entities for recording only
	enrichmentExtractor    EntityExtractor // for MemoryEnrichmentHook (BeforePlanning)
	importanceFunc         func(actionType, outcome string) float64
	retrievalWeights       core.RetrievalWeights
	lookbackWindow         time.Duration
	activityFilter         ActivityFilter
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

// WithBuilderRecordRequestEntityExtractor adds request-local entities to recorded
// events. It does not change enrichment extraction or investigation acquisition.
// Unset preserves step-only extraction. See WithRecordRequestEntityExtractor.
func WithBuilderRecordRequestEntityExtractor(e EntityExtractor) BuildMemoryHooksOption {
	return func(c *memoryHookConfig) error {
		if e == nil {
			return errNilParam("record request entity extractor")
		}
		c.recordRequestExtractor = e
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
		if err := validateMemoryRetrievalWeights(w); err != nil {
			return err
		}
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
// Returns hooks in order, the activity coordinator, and any construction error.
// Explicit invalid settings/options fail even when dependencies are absent.
// With valid configuration, absent episodic memory returns a successful no-op.
//
// This is a convenience function — equivalent to manually constructing each hook.
// For full control over every option, construct hooks individually (Layer 3).
//
// Hook ordering: announcement → enrichment → record → extraction → cleanup
func BuildMemoryHooks(sm *core.SharedMemoryDeps, aiClient core.AIClient, logger core.Logger, opts ...BuildMemoryHooksOption) ([]core.PipelineHook, core.ActivityCoordinator, error) {
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
				return nil, nil, fmt.Errorf("configure memory hooks: %w", err)
			}
		}
	}

	tuning, err := resolveMemoryHookTuning()
	if err != nil {
		return nil, nil, err
	}
	if sm == nil || sm.Episodic == nil {
		return nil, nil, nil
	}

	domain := sm.AgentDomain
	if domain == "" {
		domain = os.Getenv("TRUVAG3_AGENT_DOMAIN")
		if domain == "" {
			domain = "default"
		}
	}

	// Publish the bundle only when every selected hook is constructed.
	enrichOpts, err := buildEnrichmentOptions(logger, aiClient, sm, cfg, tuning)
	if err != nil {
		return nil, nil, err
	}
	enrichHook, err := NewMemoryEnrichmentHook(sm.Episodic, sm.Coordinator, sm.AgentName, domain, enrichOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("create memory enrichment hook: %w", err)
	}

	recordOpts, err := buildRecordOptions(logger, aiClient, cfg, tuning)
	if err != nil {
		return nil, nil, err
	}
	recordHook, err := NewMemoryRecordHook(sm.Episodic, sm.Coordinator, sm.AgentName, domain, recordOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("create memory record hook: %w", err)
	}

	// Assemble in correct order
	var hooks []core.PipelineHook

	// 1. Activity announcement (BeforePlanning — runs first)
	if sm.ActivityCoordinator != nil {
		announceOpts := buildAnnouncementOptions(logger, cfg, tuning)
		announceHook, err := NewActivityAnnouncementHook(
			sm.ActivityCoordinator, sm.AgentName, domain, tuning.maxInPrompt, announceOpts...,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("create activity announcement hook: %w", err)
		}
		hooks = append(hooks, announceHook)
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
		if err != nil {
			return nil, nil, fmt.Errorf("create knowledge extraction hook: %w", err)
		}
		hooks = append(hooks, extractionHook)
	}

	// 5. Activity cleanup (AfterSynthesis — runs last)
	if sm.ActivityCoordinator != nil {
		cleanupHook, err := NewActivityCleanupHook(sm.ActivityCoordinator, logger)
		if err != nil {
			return nil, nil, fmt.Errorf("create activity cleanup hook: %w", err)
		}
		hooks = append(hooks, cleanupHook)
	}

	return hooks, sm.ActivityCoordinator, nil
}

// buildEnrichmentOptions preserves direct-hook defaults for absent settings.
func buildEnrichmentOptions(logger core.Logger, aiClient core.AIClient, sm *core.SharedMemoryDeps, cfg *memoryHookConfig, tuning memoryHookTuning) ([]MemoryEnrichmentOption, error) {
	opts := []MemoryEnrichmentOption{
		WithEnrichmentLogger(logger),
		WithEnrichmentEntityExtractor(cfg.enrichmentExtractor),
		WithEnrichmentWeights(cfg.retrievalWeights),
		WithEnrichmentLookback(cfg.lookbackWindow),
	}
	opts = append(opts, tuning.enrichment...)
	if aiClient != nil {
		compactorOpts := []LLMActivityCompactorOption{WithActivityCompactorLogger(logger)}
		if model := os.Getenv("TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL"); model != "" {
			compactorOpts = append(compactorOpts, WithActivityCompactorModel(model))
		}
		compactor, err := NewLLMActivityCompactor(aiClient, compactorOpts...)
		if err != nil {
			return nil, fmt.Errorf("create memory activity compactor: %w", err)
		}
		opts = append(opts, WithActivityCompactor(compactor))
		if sm.DigestCache != nil {
			opts = append(opts, WithDigestCache(sm.DigestCache))
		}
	}
	if sm.Knowledge != nil && sm.Embedder != nil {
		opts = append(opts, WithEnrichmentKnowledge(sm.Knowledge, sm.Embedder))
	}
	return opts, nil
}

func buildRecordOptions(logger core.Logger, aiClient core.AIClient, cfg *memoryHookConfig, tuning memoryHookTuning) ([]MemoryRecordOption, error) {
	opts := []MemoryRecordOption{
		WithRecordLogger(logger),
		WithRecordEntityExtractor(cfg.recordExtractor),
		WithRecordImportanceFunc(cfg.importanceFunc),
	}
	opts = append(opts, tuning.record...)
	if cfg.recordRequestExtractor != nil {
		opts = append(opts, WithRecordRequestEntityExtractor(cfg.recordRequestExtractor))
	}
	if aiClient != nil {
		summarizerOpts := []LLMEventSummarizerOption{WithSummarizerLogger(logger)}
		summarizerOpts = append(summarizerOpts, tuning.summarizer...)
		if model := os.Getenv("TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL"); model != "" {
			summarizerOpts = append(summarizerOpts, WithSummarizerModel(model))
		}
		summarizer, err := NewLLMEventSummarizer(aiClient, summarizerOpts...)
		if err != nil {
			return nil, fmt.Errorf("create memory event summarizer: %w", err)
		}
		opts = append(opts, WithEventSummarizer(summarizer))
	}
	return opts, nil
}

func buildAnnouncementOptions(logger core.Logger, cfg *memoryHookConfig, tuning memoryHookTuning) []ActivityAnnouncementOption {
	opts := []ActivityAnnouncementOption{WithAnnouncementLogger(logger)}
	opts = append(opts, tuning.announcement...)
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
