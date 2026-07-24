package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Compile-time interface compliance checks.
var (
	_ core.BeforePlanningHook = (*MemoryEnrichmentHook)(nil)
	_ core.AfterExecutionHook = (*MemoryRecordHook)(nil)
)

// VectorKnowledgeSearcher is an optional interface that SharedKnowledge backends
// can implement to support pre-embedded vector search. When the MemoryEnrichmentHook
// detects this interface via type assertion, it embeds the query first and uses
// vector search instead of text search.
//
// The memory/ module's QdrantSharedKnowledge implements this interface.
// Backends that handle their own embedding (or use keyword search) only need
// to implement core.SharedKnowledge.
type VectorKnowledgeSearcher interface {
	SearchKnowledgeByVector(ctx context.Context, callerDomain string, namespace string,
		queryVector []float32, topK int, weights core.RetrievalWeights) ([]core.ScoredKnowledge, error)
}

// --- Entity Extraction ---

// Entity represents a discovered entity reference in a request or result.
type Entity struct {
	Type string // "pod", "service", "order", "alert"
	ID   string // "payment-service-pod-7x9k2"
}

// EntityExtractor identifies entities from requests and structured results.
// Implementations should prefer structured metadata over regex on natural language text.
type EntityExtractor interface {
	// ExtractEntities discovers entities from text and/or structured metadata.
	// metadata may contain "entity_type", "entity_id", tool parameters, etc.
	ExtractEntities(text string, metadata map[string]interface{}) []Entity
}

// NoOpEntityExtractor is the framework default when no AIClient is wired
// into BuildMemoryHooks. It performs no extraction of its own and only
// reads explicit metadata fields the caller provided.
//
// The framework treats Entity{Type, ID} as an opaque index key. It has no
// opinion about what counts as a "pod", "user", "order", or "flight".
// Domain semantics are the application's responsibility, not the framework's.
//
// Events with no extracted entity are still recorded against the agent
// and the domain stream — they just do not get a per-entity index.
type NoOpEntityExtractor struct{}

func (NoOpEntityExtractor) ExtractEntities(_ string, metadata map[string]interface{}) []Entity {
	if metadata == nil {
		return nil
	}

	// Multi-entity structured path (preferred for tools that touch multiple entities)
	if refs, ok := metadata["entities"].([]core.EntityRef); ok && len(refs) > 0 {
		var out []Entity
		for _, e := range refs {
			if e.Type != "" && e.ID != "" {
				out = append(out, Entity{Type: e.Type, ID: e.ID})
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	// Singular entity structured path (backward compat)
	et, _ := metadata["entity_type"].(string)
	eid, _ := metadata["entity_id"].(string)
	if et != "" && eid != "" {
		return []Entity{{Type: et, ID: eid}}
	}
	return nil
}

// LLMEntityExtractor reads entities produced by the EventSummarizer LLM
// during the same call that produces the step summary. Adds zero LLM
// calls in steady state.
//
// This is the framework default whenever BuildMemoryHooks is called with
// a non-nil AIClient. Use WithMemoryEntityExtractor(NoOpEntityExtractor{})
// to opt out.
//
// The extractor does not call the LLM itself. Instead, MemoryRecordHook
// plumbs the summarizer's Entities output into the per-step metadata bag
// under the "llm_entities" key, and this extractor reads that key at
// call time. If "llm_entities" is absent, the extractor falls through
// to explicit metadata fields — matching NoOpEntityExtractor behavior.
type LLMEntityExtractor struct{}

func (LLMEntityExtractor) ExtractEntities(_ string, metadata map[string]interface{}) []Entity {
	if metadata == nil {
		return nil
	}

	// Primary path: read entities the summarizer LLM produced.
	if raw, ok := metadata["llm_entities"].([]core.EntityRef); ok && len(raw) > 0 {
		out := make([]Entity, 0, len(raw))
		seen := make(map[string]bool, len(raw))
		for _, e := range raw {
			if e.Type == "" || e.ID == "" {
				continue
			}
			key := e.Type + ":" + e.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Entity{Type: e.Type, ID: e.ID})
		}
		if len(out) > 0 {
			return out
		}
	}

	// Fallback: multi-entity structured path (same as NoOpEntityExtractor).
	if refs, ok := metadata["entities"].([]core.EntityRef); ok && len(refs) > 0 {
		var out []Entity
		for _, e := range refs {
			if e.Type != "" && e.ID != "" {
				out = append(out, Entity{Type: e.Type, ID: e.ID})
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	// Fallback: singular entity structured path (backward compat).
	et, _ := metadata["entity_type"].(string)
	eid, _ := metadata["entity_id"].(string)
	if et != "" && eid != "" {
		return []Entity{{Type: et, ID: eid}}
	}
	return nil
}

// extractorTypeLabel returns the telemetry label for an EntityExtractor
// implementation. Used by MemoryRecordHook and MemoryEnrichmentHook when
// emitting the memory.entity_extraction.completed span event.
func extractorTypeLabel(e EntityExtractor) string {
	if e == nil {
		return "none"
	}
	switch e.(type) {
	case NoOpEntityExtractor, *NoOpEntityExtractor:
		return "noop"
	case LLMEntityExtractor, *LLMEntityExtractor:
		return "llm"
	default:
		return "custom"
	}
}

// --- Memory Enrichment Hook (BeforePlanningHook) ---

// MemoryEnrichmentHook implements core.BeforePlanningHook.
// It queries episodic memory, checks investigation coordination, and optionally
// searches shared knowledge — then injects the results into the planning prompt
// via PipelineContext.Enrichments["rag_context"].
//
// Registered in OrchestratorDependencies.PipelineHooks alongside other hooks.
type MemoryEnrichmentHook struct {
	episodic               core.EpisodicMemory
	coordinator            core.InvestigationCoordinator // may be nil
	knowledge              core.SharedKnowledge          // may be nil (Phase 2)
	embedder               core.EmbeddingClient          // may be nil (Phase 2)
	compactor              core.ActivityCompactor        // may be nil — falls back to raw events
	agentDomain            string
	agentName              string
	weights                core.RetrievalWeights
	maxTokens              int
	recentEventsLimit      int              // Max recent domain events for baseline situational awareness
	compactionMaxTokens    int              // Max tokens for compacted digest (default: 500)
	compactionRawLimit     int              // Max raw events to fetch for compaction (default: 200)
	compactionRecentDetail int              // Raw events appended after digest for immediate detail (default: 15)
	digestCache            core.DigestCache // may be nil — no caching, full compaction every request
	digestCacheTTL         time.Duration    // default: 5m
	incrementalThreshold   int              // max new events for incremental update (default: 20)
	lookbackWindow         time.Duration
	entityExtractor        EntityExtractor
	logger                 core.Logger
}

// MemoryEnrichmentOption configures MemoryEnrichmentHook.
// Returns error if the option value is invalid (fail-fast per core/ARCHITECTURE.md).
type MemoryEnrichmentOption func(*MemoryEnrichmentHook) error

// WithEnrichmentKnowledge enables shared knowledge search (Phase 2).
func WithEnrichmentKnowledge(knowledge core.SharedKnowledge, embedder core.EmbeddingClient) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		h.knowledge = knowledge
		h.embedder = embedder
		return nil
	}
}

// WithEnrichmentWeights sets custom retrieval weights.
func WithEnrichmentWeights(weights core.RetrievalWeights) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		h.weights = weights
		return nil
	}
}

// WithEnrichmentMaxTokens sets the maximum tokens of memory context to inject.
func WithEnrichmentMaxTokens(maxTokens int) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if maxTokens <= 0 {
			return fmt.Errorf("maxTokens must be positive, got %d", maxTokens)
		}
		h.maxTokens = maxTokens
		return nil
	}
}

// WithEnrichmentLookback sets how far back to query episodic events.
func WithEnrichmentLookback(d time.Duration) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if d <= 0 {
			return fmt.Errorf("lookback window must be positive, got %v", d)
		}
		h.lookbackWindow = d
		return nil
	}
}

// WithEnrichmentEntityExtractor sets a custom entity extractor.
func WithEnrichmentEntityExtractor(extractor EntityExtractor) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if extractor == nil {
			return fmt.Errorf("entity extractor cannot be nil")
		}
		h.entityExtractor = extractor
		return nil
	}
}

// WithEnrichmentRecentEventsLimit sets how many recent domain events to include
// as baseline situational awareness (regardless of entity extraction). Default: 10.
func WithEnrichmentRecentEventsLimit(limit int) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if limit <= 0 {
			return fmt.Errorf("recentEventsLimit must be positive, got %d", limit)
		}
		h.recentEventsLimit = limit
		return nil
	}
}

// WithEnrichmentLogger sets the logger for the enrichment hook.
// If the logger implements ComponentAwareLogger, it is automatically wrapped
// with "framework/orchestration" component context per LOGGING_IMPLEMENTATION_GUIDE §14.
func WithEnrichmentLogger(logger core.Logger) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			h.logger = cal.WithComponent("framework/orchestration")
		} else {
			h.logger = logger
		}
		return nil
	}
}

// WithActivityCompactor sets an LLM-powered activity compactor for generating
// fixed-size domain activity digests. When nil or on error, falls back to raw events.
func WithActivityCompactor(c core.ActivityCompactor) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		h.compactor = c // nil is valid — means raw events only
		return nil
	}
}

// WithCompactionMaxTokens sets the max token budget for compacted activity summaries.
// Default: 500.
func WithCompactionMaxTokens(n int) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if n <= 0 {
			return fmt.Errorf("compactionMaxTokens must be positive, got %d", n)
		}
		h.compactionMaxTokens = n
		return nil
	}
}

// WithCompactionRawLimit sets the max raw events fetched before compaction.
// Default: 200.
func WithCompactionRawLimit(n int) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if n <= 0 {
			return fmt.Errorf("compactionRawLimit must be positive, got %d", n)
		}
		h.compactionRawLimit = n
		return nil
	}
}

// WithDigestCache sets a digest cache for incremental compaction.
// When set, the compacted digest is cached and incrementally updated.
func WithDigestCache(cache core.DigestCache) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		h.digestCache = cache // nil is valid — disables caching
		return nil
	}
}

// WithDigestCacheTTL sets the TTL for cached digests. Default: 5m.
func WithDigestCacheTTL(ttl time.Duration) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if ttl <= 0 {
			return fmt.Errorf("digestCacheTTL must be positive, got %v", ttl)
		}
		h.digestCacheTTL = ttl
		return nil
	}
}

// WithIncrementalThreshold sets the max new events for incremental digest update. Default: 20.
func WithIncrementalThreshold(n int) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if n <= 0 {
			return fmt.Errorf("incrementalThreshold must be positive, got %d", n)
		}
		h.incrementalThreshold = n
		return nil
	}
}

// WithCompactionRecentDetail sets the number of most recent raw events appended
// after the compacted digest for immediate detail access. Default: 15.
func WithCompactionRecentDetail(n int) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if n < 0 {
			return fmt.Errorf("compactionRecentDetail must be non-negative, got %d", n)
		}
		h.compactionRecentDetail = n
		return nil
	}
}

// NewMemoryEnrichmentHook creates a memory enrichment hook.
// episodic is required. coordinator is optional (nil = no investigation claims).
func NewMemoryEnrichmentHook(
	episodic core.EpisodicMemory,
	coordinator core.InvestigationCoordinator,
	agentName string,
	agentDomain string,
	opts ...MemoryEnrichmentOption,
) (*MemoryEnrichmentHook, error) {
	if episodic == nil {
		return nil, fmt.Errorf("episodic memory is required for MemoryEnrichmentHook")
	}
	h := &MemoryEnrichmentHook{
		episodic:               episodic,
		coordinator:            coordinator,
		agentDomain:            agentDomain,
		agentName:              agentName,
		weights:                core.RetrievalWeights{Recency: 0.5, Relevance: 0.3, Importance: 0.2},
		maxTokens:              2000,
		recentEventsLimit:      20,
		compactionMaxTokens:    500,
		compactionRawLimit:     200,
		compactionRecentDetail: 15,
		digestCacheTTL:         5 * time.Minute,
		incrementalThreshold:   20,
		lookbackWindow:         24 * time.Hour,
		entityExtractor:        NoOpEntityExtractor{},
		logger:                 &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(h); err != nil {
			return nil, fmt.Errorf("invalid enrichment hook option: %w", err)
		}
	}
	return h, nil
}

// Name returns the hook name for telemetry spans.
func (h *MemoryEnrichmentHook) Name() string { return "memory-enrichment" }

// SetTelemetry propagates the telemetry provider to the activity compactor (if configured).
func (h *MemoryEnrichmentHook) SetTelemetry(t core.Telemetry) {
	if h.compactor != nil {
		if telemetryAware, ok := h.compactor.(*LLMActivityCompactor); ok {
			telemetryAware.SetTelemetry(t)
		}
	}
}

// SetLLMDebugStore propagates the debug store to the activity compactor (if configured).
func (h *MemoryEnrichmentHook) SetLLMDebugStore(store LLMDebugStore) {
	if h.compactor != nil {
		if debuggable, ok := h.compactor.(*LLMActivityCompactor); ok {
			debuggable.SetLLMDebugStore(store)
		}
	}
}

// BeforePlanning queries shared memory and injects context into the planning prompt.
func (h *MemoryEnrichmentHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
	var sections []string

	startTime := time.Now()

	// Get request_id for logs and span events (Logging Pattern 3 + Tracing Pattern 6)
	requestID := GetRequestID(ctx)

	// 1. Extract entities from the request
	entities := h.entityExtractor.ExtractEntities(pctx.Request, pctx.Metadata)

	// Telemetry: entity extraction span event + counter at enrichment site.
	enrichmentSource := "none"
	if len(entities) > 0 {
		enrichmentSource = "explicit_metadata"
	}
	enrichmentExtractorType := extractorTypeLabel(h.entityExtractor)
	telemetry.AddSpanEvent(ctx, "memory.entity_extraction.completed",
		attribute.String("request_id", requestID),
		attribute.String("hook", "enrichment"),
		attribute.String("extractor_type", enrichmentExtractorType),
		attribute.String("source", enrichmentSource),
		attribute.Int("entities_found", len(entities)),
	)
	telemetry.Counter("orchestration.memory.entity_extraction.total",
		"module", telemetry.ModuleOrchestration,
		"hook", "enrichment",
		"extractor_type", enrichmentExtractorType,
		"source", enrichmentSource,
	)

	// 2. Always query recent domain events as a baseline (situational awareness).
	// This ensures the LLM knows what happened recently even when no entities
	// are extracted from natural language chat queries.
	// When compactor is available, fetch more events (compactionRawLimit) and compress them.
	seen := make(map[string]bool) // Dedup event IDs across recent + entity queries
	if h.episodic != nil {
		since := time.Now().Add(-h.lookbackWindow)

		// Use higher limit when compactor is available — it will digest them
		queryLimit := h.recentEventsLimit
		if h.compactor != nil {
			queryLimit = h.compactionRawLimit
		}

		recentEvents, err := h.episodic.QueryRecentEvents(ctx, h.agentDomain, since, queryLimit)
		if err != nil {
			h.logger.WarnWithContext(ctx, "Recent events query failed, skipping", map[string]interface{}{
				"operation":  "memory_enrichment",
				"request_id": requestID,
				"error":      err.Error(),
				"error_type": "episodic_recent_read",
			})
			telemetry.Counter("memory.enrichment.episodic_query_errors",
				"module", telemetry.ModuleOrchestration,
			)
		} else if len(recentEvents) > 0 {
			for _, e := range recentEvents {
				seen[e.EventID] = true
			}

			// Compact if available, otherwise raw
			if h.compactor != nil {
				var digest string
				var detailEvents []core.AgentEvent

				// Check digest cache for incremental compaction
				cacheDecisionStart := time.Now()
				policyFingerprint := ""
				cacheSafe := true
				if semantic, hasAI := h.compactor.(aiSemanticCacheFingerprinter); hasAI {
					policyFingerprint, cacheSafe = semantic.aiSemanticFingerprint(ctx)
				}
				var cached *cachedDigestData
				if cacheSafe {
					cached, _ = h.getCachedDigest(ctx, policyFingerprint)
				}
				var cachePath string

				if cached == nil {
					// Cache miss (or no cache) — full compaction
					cachePath = "full"
					telemetry.Counter("orchestration.digest_cache.miss", "module", telemetry.ModuleOrchestration)
					compactionCtx := ctx
					var producedFingerprint *aiFingerprintCapture
					if policyFingerprint != "" {
						compactionCtx, producedFingerprint = withAIFingerprintCapture(ctx)
					}
					d, compactErr := h.compactor.CompactEvents(compactionCtx, recentEvents, h.compactionMaxTokens)
					if compactErr == nil && d != "" {
						h.storeCachedDigest(ctx, d, newestEventTS(recentEvents), policyFingerprint,
							cacheSafe && producedFingerprint.matches(policyFingerprint))
						digest = d
					} else {
						// Fail-open: fall back to raw events
						if compactErr != nil {
							telemetry.RecordSpanError(ctx, compactErr)
						}
						fallbackEvents := recentEvents
						if len(fallbackEvents) > h.recentEventsLimit {
							fallbackEvents = fallbackEvents[:h.recentEventsLimit]
						}
						section := formatEpisodicEvents(fallbackEvents, "Recent activity in this domain:")
						sections = append(sections, section)
					}
					detailEvents = recentEvents
				} else {
					// Cache hit — check for new events since last compaction
					telemetry.Counter("orchestration.digest_cache.hit", "module", telemetry.ModuleOrchestration)
					newEvents, _ := h.episodic.QueryRecentEvents(ctx, h.agentDomain, cached.LastEventTS, h.compactionRawLimit)

					if len(newEvents) == 0 {
						// No new events — use cached digest (0ms, no LLM)
						cachePath = "cached"
						digest = cached.Content
					} else if len(newEvents) <= h.incrementalThreshold {
						// Incremental update
						cachePath = "incremental"
						compactionCtx := ctx
						var producedFingerprint *aiFingerprintCapture
						if policyFingerprint != "" {
							compactionCtx, producedFingerprint = withAIFingerprintCapture(ctx)
						}
						updated, err := h.compactor.UpdateDigest(compactionCtx, cached.Content, newEvents, h.compactionMaxTokens)
						if err == nil && updated != "" {
							h.storeCachedDigest(ctx, updated, newestEventTS(newEvents), policyFingerprint,
								cacheSafe && producedFingerprint.matches(policyFingerprint))
							digest = updated
						} else {
							digest = cached.Content // fallback to stale cache
						}
					} else {
						// Burst — full recompaction
						cachePath = "full_recompact"
						allEvents, _ := h.episodic.QueryRecentEvents(ctx, h.agentDomain, since, h.compactionRawLimit)
						compactionCtx := ctx
						var producedFingerprint *aiFingerprintCapture
						if policyFingerprint != "" {
							compactionCtx, producedFingerprint = withAIFingerprintCapture(ctx)
						}
						d, err := h.compactor.CompactEvents(compactionCtx, allEvents, h.compactionMaxTokens)
						if err == nil && d != "" {
							h.storeCachedDigest(ctx, d, newestEventTS(allEvents), policyFingerprint,
								cacheSafe && producedFingerprint.matches(policyFingerprint))
							digest = d
						} else {
							digest = cached.Content // fallback to stale cache
						}
						detailEvents = allEvents
					}

					// For detail section on cache hit: only query the N most recent
					if detailEvents == nil {
						detailEvents, _ = h.episodic.QueryRecentEvents(ctx, h.agentDomain, since, h.compactionRecentDetail)
					}
				}

				// Cache decision observability (§6.7.7)
				telemetry.AddSpanEvent(ctx, "activity.compaction.cache_decision",
					attribute.String("request_id", requestID),
					attribute.Bool("cache_hit", cached != nil),
					attribute.String("path", cachePath),
					attribute.Int64("duration_ms", time.Since(cacheDecisionStart).Milliseconds()),
				)
				h.logger.DebugWithContext(ctx, "Digest cache decision", map[string]interface{}{
					"operation":   "memory_enrichment",
					"request_id":  requestID,
					"cache_hit":   cached != nil,
					"path":        cachePath,
					"duration_ms": time.Since(cacheDecisionStart).Milliseconds(),
				})

				// Inject digest + recent detail events
				if digest != "" {
					sections = append(sections, "Domain activity summary:\n"+digest)
					recentDetailCount := h.compactionRecentDetail
					if detailEvents != nil && recentDetailCount > len(detailEvents) {
						recentDetailCount = len(detailEvents)
					}
					if detailEvents != nil && recentDetailCount > 0 {
						section := formatEpisodicEvents(detailEvents[:recentDetailCount], "Most recent events (detail):")
						sections = append(sections, section)
					}
				}
			} else {
				// No compactor — use raw events (Phase 5 behavior)
				section := formatEpisodicEvents(recentEvents, "Recent activity in this domain:")
				sections = append(sections, section)
			}
		}

		// 2b. Layer entity-targeted history on top (precision layer)
		if len(entities) > 0 {
			var entityEvents []core.AgentEvent
			for _, entity := range entities {
				events, err := h.episodic.QueryEntityHistory(ctx, h.agentDomain, entity.Type, entity.ID, since)
				if err != nil {
					h.logger.WarnWithContext(ctx, "Episodic memory query failed, skipping", map[string]interface{}{
						"operation":   "memory_enrichment",
						"request_id":  requestID,
						"entity_type": entity.Type,
						"entity_id":   entity.ID,
						"error":       err.Error(),
						"error_type":  "episodic_read",
					})
					telemetry.Counter("memory.enrichment.episodic_query_errors",
						"module", telemetry.ModuleOrchestration,
					)
					continue
				}
				// Deduplicate against recent events
				for _, e := range events {
					if !seen[e.EventID] {
						seen[e.EventID] = true
						entityEvents = append(entityEvents, e)
					}
				}
			}
			if len(entityEvents) > 0 {
				section := formatEpisodicEvents(entityEvents)
				sections = append(sections, section)
			}
		}
	}

	// 3. Check active investigations and claim entities
	if h.coordinator != nil && len(entities) > 0 {
		active, err := h.coordinator.GetActiveInvestigations(ctx)
		if err == nil && len(active) > 0 {
			section := formatActiveInvestigations(active, entities)
			if section != "" {
				sections = append(sections, section)
			}
		}

		// Claim investigation on discovered entities (prevents duplicate work by other agents)
		for _, entity := range entities {
			claimed, holder, claimErr := h.coordinator.ClaimInvestigation(ctx, h.agentName, entity.ID, 0) // 0 = use default TTL
			if claimErr != nil {
				continue // Fail-open
			}
			if !claimed {
				// Another agent already investigating — add to context
				sections = append(sections, fmt.Sprintf("Note: %s/%s is currently being investigated by %s. Consider coordinating rather than duplicating work.", entity.Type, entity.ID, holder))
			}
		}
	}

	// 4. Search shared knowledge (Phase 2 — only if knowledge + embedder are configured)
	if h.knowledge != nil && h.embedder != nil && pctx.Request != "" {
		var knowledge []core.ScoredKnowledge

		// Try vector-based search first (Qdrant and similar backends)
		if vectorSearcher, ok := h.knowledge.(VectorKnowledgeSearcher); ok {
			// Embed the query
			embResp, embErr := h.embedder.GenerateEmbeddings(ctx, []string{pctx.Request}, nil)
			if embErr == nil && len(embResp.Embeddings) > 0 && len(embResp.Embeddings[0]) > 0 {
				knowledge, _ = vectorSearcher.SearchKnowledgeByVector(ctx, h.agentDomain, "", embResp.Embeddings[0], 3, h.weights)
			} else if embErr != nil {
				h.logger.WarnWithContext(ctx, "Embedding failed for knowledge search, falling back to text search", map[string]interface{}{
					"operation":  "memory_enrichment",
					"request_id": requestID,
					"error":      embErr.Error(),
					"error_type": "embedding",
				})
			}
		}

		// Fall back to text-based search (backends that handle their own embedding)
		if knowledge == nil {
			knowledge, _ = h.knowledge.SearchKnowledge(ctx, h.agentDomain, "", pctx.Request, 3, h.weights)
		}

		if len(knowledge) > 0 {
			section := formatKnowledgeFragments(knowledge)
			sections = append(sections, section)
		}
	}

	// 5. Assemble and inject into enrichments
	if len(sections) > 0 {
		memoryContext := strings.Join(sections, "\n\n")

		// Truncate to max tokens (approximate: 1 token ≈ 4 chars)
		maxChars := h.maxTokens * 4
		if len(memoryContext) > maxChars {
			memoryContext = memoryContext[:maxChars] + "\n[memory context truncated]"
		}

		if pctx.Enrichments == nil {
			pctx.Enrichments = make(map[string]interface{})
		}

		// Append to existing RAG context (other hooks may have injected data)
		if existing, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string); ok && existing != "" {
			pctx.Enrichments[core.EnrichmentRAGContext] = existing + "\n\n" + memoryContext
		} else {
			pctx.Enrichments[core.EnrichmentRAGContext] = memoryContext
		}

		h.logger.InfoWithContext(ctx, "Memory context injected into planning prompt", map[string]interface{}{
			"operation":      "memory_enrichment",
			"request_id":     requestID,
			"entities_found": len(entities),
			"context_chars":  len(memoryContext),
			"duration_ms":    time.Since(startTime).Milliseconds(),
		})

		// Pattern 6: AddSpanEvent with request_id first
		telemetry.AddSpanEvent(ctx, "memory.enrichment.injected",
			attribute.String("request_id", requestID),
			attribute.Int("entities_found", len(entities)),
			attribute.Int("context_chars", len(memoryContext)),
		)
		// Pattern 5: Counter with module label
		telemetry.Counter("memory.enrichment.injected",
			"module", telemetry.ModuleOrchestration,
		)
	}

	return nil, nil // Never short-circuits
}

// --- Memory Record Hook (AfterExecutionHook) ---

// MemoryRecordHook implements core.AfterExecutionHook.
// It records structured AgentEvents from execution results into episodic memory,
// and releases investigation claims.
type MemoryRecordHook struct {
	episodic        core.EpisodicMemory
	coordinator     core.InvestigationCoordinator // may be nil
	summarizer      core.EventSummarizer          // may be nil — falls back to heuristic
	agentName       string
	agentDomain     string
	entityExtractor EntityExtractor
	importanceFunc  func(actionType, outcome string) float64
	logger          core.Logger
}

// MemoryRecordOption configures MemoryRecordHook.
// Returns error if the option value is invalid (fail-fast per core/ARCHITECTURE.md).
type MemoryRecordOption func(*MemoryRecordHook) error

// WithRecordImportanceFunc sets a custom importance scoring function.
func WithRecordImportanceFunc(fn func(actionType, outcome string) float64) MemoryRecordOption {
	return func(h *MemoryRecordHook) error {
		if fn == nil {
			return fmt.Errorf("importance function cannot be nil")
		}
		h.importanceFunc = fn
		return nil
	}
}

// WithRecordEntityExtractor sets a custom entity extractor for result parsing.
func WithRecordEntityExtractor(extractor EntityExtractor) MemoryRecordOption {
	return func(h *MemoryRecordHook) error {
		if extractor == nil {
			return fmt.Errorf("entity extractor cannot be nil")
		}
		h.entityExtractor = extractor
		return nil
	}
}

// WithRecordLogger sets the logger for the record hook.
// If the logger implements ComponentAwareLogger, it is automatically wrapped
// with "framework/orchestration" component context per LOGGING_IMPLEMENTATION_GUIDE §14.
func WithRecordLogger(logger core.Logger) MemoryRecordOption {
	return func(h *MemoryRecordHook) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			h.logger = cal.WithComponent("framework/orchestration")
		} else {
			h.logger = logger
		}
		return nil
	}
}

// WithEventSummarizer sets an LLM-powered event summarizer for generating actionable
// event summaries. When set, step responses are batch-summarized via LLM before recording.
// If nil or on LLM error, falls back to heuristic summaries.
func WithEventSummarizer(s core.EventSummarizer) MemoryRecordOption {
	return func(h *MemoryRecordHook) error {
		h.summarizer = s // nil is valid — means heuristic-only
		return nil
	}
}

// SetTelemetry propagates the telemetry provider to the event summarizer (if configured).
// Called by the orchestrator during SetTelemetry propagation.
func (h *MemoryRecordHook) SetTelemetry(t core.Telemetry) {
	if h.summarizer != nil {
		if telemetryAware, ok := h.summarizer.(*LLMEventSummarizer); ok {
			telemetryAware.telemetry = t
		}
	}
}

// SetLLMDebugStore propagates the debug store to the event summarizer (if configured).
// Called by the orchestrator during SetLLMDebugStore propagation.
func (h *MemoryRecordHook) SetLLMDebugStore(store LLMDebugStore) {
	if h.summarizer != nil {
		if debuggable, ok := h.summarizer.(*LLMEventSummarizer); ok {
			debuggable.SetLLMDebugStore(store)
		}
	}
}

// NewMemoryRecordHook creates a memory recording hook.
func NewMemoryRecordHook(
	episodic core.EpisodicMemory,
	coordinator core.InvestigationCoordinator,
	agentName string,
	agentDomain string,
	opts ...MemoryRecordOption,
) (*MemoryRecordHook, error) {
	if episodic == nil {
		return nil, fmt.Errorf("episodic memory is required for MemoryRecordHook")
	}
	h := &MemoryRecordHook{
		episodic:        episodic,
		coordinator:     coordinator,
		agentName:       agentName,
		agentDomain:     agentDomain,
		entityExtractor: NoOpEntityExtractor{},
		importanceFunc:  DefaultImportanceScorer,
		logger:          &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(h); err != nil {
			return nil, fmt.Errorf("invalid record hook option: %w", err)
		}
	}
	return h, nil
}

// Name returns the hook name for telemetry spans.
func (h *MemoryRecordHook) Name() string { return "memory-record" }

// AfterExecution records execution outcomes as episodic events and releases investigation claims.
func (h *MemoryRecordHook) AfterExecution(ctx context.Context, pctx *core.PipelineContext, results interface{}) error {
	execResult, ok := results.(*ExecutionResult)
	if !ok || execResult == nil {
		return nil
	}

	// Get request_id for span events and nested delegation tagging
	parentRequestID := ""
	requestID := GetRequestID(ctx)
	if baggage := getBaggageFromContext(ctx); baggage != nil {
		parentRequestID = baggage["request_id"]
	}

	// Batch-summarize steps via LLM if summarizer is configured
	var llmSummaries map[string]core.StepSummary
	if h.summarizer != nil {
		var inputs []core.StepSummaryInput
		for _, step := range execResult.Steps {
			if step.Skipped {
				continue
			}
			inputs = append(inputs, core.StepSummaryInput{
				StepID:      step.StepID,
				AgentName:   step.AgentName,
				Capability:  step.Capability,
				Instruction: step.Instruction,
				Parameters:  step.Parameters,
				Response:    step.Response,
				Success:     step.Success,
			})
		}
		if len(inputs) > 0 {
			summaries, err := h.summarizer.SummarizeSteps(ctx, inputs)
			if err != nil {
				h.logger.WarnWithContext(ctx, "Event summarizer failed, using heuristic fallback", map[string]interface{}{
					"operation":  "memory_record",
					"request_id": requestID,
					"error":      err.Error(),
					"error_type": "summarizer_error",
				})
			} else {
				llmSummaries = summaries
			}
		}
	}

	// Track entities for investigation release
	var investigatedEntities []string

	for _, step := range execResult.Steps {
		if step.Skipped {
			continue // Skipped steps had no execution — nothing to record
		}

		// Build the per-step metadata bag. Augment with llm_entities from
		// the summarizer when available (copy-on-write to avoid mutating step.Parameters).
		extractionMetadata := step.Parameters
		var augmented bool
		if llmSummaries != nil {
			if summary, ok := llmSummaries[step.StepID]; ok && len(summary.Entities) > 0 {
				newMeta := make(map[string]interface{}, len(step.Parameters)+1)
				for k, v := range step.Parameters {
					newMeta[k] = v
				}
				newMeta["llm_entities"] = summary.Entities
				extractionMetadata = newMeta
				augmented = true
			}
		}

		entities := h.entityExtractor.ExtractEntities(step.Instruction, extractionMetadata)

		// Telemetry: emit memory.entity_extraction.completed span event + counter.
		extractionSource := "none"
		if len(entities) > 0 {
			if augmented {
				if raw, ok := extractionMetadata["llm_entities"].([]core.EntityRef); ok && len(raw) > 0 {
					extractionSource = "llm_entities"
				} else {
					extractionSource = "explicit_metadata"
				}
			} else {
				extractionSource = "explicit_metadata"
			}
		}
		extractorType := extractorTypeLabel(h.entityExtractor)
		telemetry.AddSpanEvent(ctx, "memory.entity_extraction.completed",
			attribute.String("request_id", requestID),
			attribute.String("step_id", step.StepID),
			attribute.String("hook", "record"),
			attribute.String("extractor_type", extractorType),
			attribute.String("source", extractionSource),
			attribute.Int("entities_found", len(entities)),
		)
		telemetry.Counter("orchestration.memory.entity_extraction.total",
			"module", telemetry.ModuleOrchestration,
			"hook", "record",
			"extractor_type", extractorType,
			"source", extractionSource,
		)

		// Determine action type from capability
		actionType := step.Capability
		if actionType == "" {
			actionType = "unknown"
		}

		outcome := "success"
		if !step.Success {
			outcome = "failure"
		}

		// For orchestrator steps, record a lightweight delegation event
		// (the child agent already recorded detailed events)
		capType, _ := step.Metadata["capability_type"].(string)
		if capType == string(core.CapabilityOrchestrator) {
			actionType = "delegated"
		}

		if len(entities) > 0 {
			// One event per step, indexed under all entities
			primary := entities[0]
			event := core.AgentEvent{
				AgentName:   h.agentName,
				AgentDomain: h.agentDomain,
				ActionType:  actionType,
				EntityType:  primary.Type,
				EntityID:    primary.ID,
				Entities:    toEntityRefs(entities),
				Summary:     resolveStepSummary(llmSummaries, step.StepID, actionType, primary, step),
				Outcome:     outcome,
				TraceID:     memoryTraceID(ctx),
				RequestID:   parentRequestID,
				ParentEvent: getParentEventFromContext(ctx),
				Scope:       determineEventScope(actionType, step),
				Importance:  h.importanceFunc(actionType, outcome),
			}

			if err := h.episodic.RecordEvent(ctx, event); err != nil {
				h.logger.WarnWithContext(ctx, "Failed to record episodic event, continuing", map[string]interface{}{
					"operation":   "memory_record",
					"request_id":  requestID,
					"entity_type": primary.Type,
					"entity_id":   primary.ID,
					"error":       err.Error(),
					"error_type":  "episodic_write",
				})
			}

			for _, entity := range entities {
				investigatedEntities = append(investigatedEntities, entity.ID)
			}
		} else {
			// Record a domain-level event with no entity index.
			event := core.AgentEvent{
				AgentName:   h.agentName,
				AgentDomain: h.agentDomain,
				ActionType:  actionType,
				Summary:     resolveStepSummary(llmSummaries, step.StepID, actionType, Entity{}, step),
				Outcome:     outcome,
				TraceID:     memoryTraceID(ctx),
				RequestID:   parentRequestID,
				ParentEvent: getParentEventFromContext(ctx),
				Scope:       determineEventScope(actionType, step),
				Importance:  h.importanceFunc(actionType, outcome),
			}
			if err := h.episodic.RecordEvent(ctx, event); err != nil {
				h.logger.WarnWithContext(ctx, "Failed to record entity-less episodic event, continuing", map[string]interface{}{
					"operation":  "memory_record",
					"request_id": requestID,
					"error":      err.Error(),
					"error_type": "episodic_write",
				})
			}
		}
	}

	// Emit telemetry for recorded events
	if len(investigatedEntities) > 0 {
		telemetry.AddSpanEvent(ctx, "memory.record.events_written",
			attribute.String("request_id", requestID),
			attribute.Int("events_recorded", len(investigatedEntities)),
			attribute.String("agent_name", h.agentName),
		)
		telemetry.Counter("memory.record.events_total",
			"module", telemetry.ModuleOrchestration,
			"agent", h.agentName,
		)
	}

	// Release investigation claims for all entities we acted on
	if h.coordinator != nil {
		for _, entityID := range investigatedEntities {
			if err := h.coordinator.ReleaseInvestigation(ctx, h.agentName, entityID); err != nil {
				h.logger.WarnWithContext(ctx, "Failed to release investigation claim", map[string]interface{}{
					"operation":  "memory_record",
					"request_id": requestID,
					"entity_id":  entityID,
					"error":      err.Error(),
					"error_type": "claim_release",
				})
			}
		}
	}

	return nil
}

// --- Default Importance Scorer ---

// DefaultImportanceScorer provides heuristic importance without LLM calls.
// Scores are based on semantic categories (read vs write, external effect vs internal)
// rather than domain-specific action types. Developers can override with
// WithRecordImportanceFunc() for domain-specific scoring.
func DefaultImportanceScorer(actionType, outcome string) float64 {
	// Classify by action verb prefix — framework-agnostic heuristic.
	// "create", "delete", "restart" → mutations (high importance)
	// "query", "get", "list", "describe", "check" → reads (low importance)
	// "send", "notify", "publish" → external effects (medium-high)
	// "delegated" → orchestration delegation (medium)
	var score float64
	switch {
	case hasPrefix(actionType, "create", "delete", "remove", "restart", "rollout", "scale", "deploy", "update", "patch"):
		score = 7.0 // Mutations — change system state
	case hasPrefix(actionType, "alert", "incident", "error", "fail"):
		score = 8.0 // Alerts/incidents — high urgency
	case hasPrefix(actionType, "send", "notify", "publish", "post"):
		score = 5.0 // External communication
	case hasPrefix(actionType, "query", "get", "list", "describe", "check", "search", "read", "fetch"):
		score = 3.0 // Read-only operations
	case actionType == "delegated":
		score = 5.0 // Orchestration delegation
	default:
		score = 5.0 // Unknown actions get medium importance
	}
	if outcome == "failure" {
		score = min64(score+2.0, 10.0) // Failures are more important
	}
	return score
}

// hasPrefix checks if s starts with any of the given prefixes.
func hasPrefix(s string, prefixes ...string) bool {
	lower := strings.ToLower(s)
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// determineEventScope decides the visibility scope for a recorded event.
// Service-down and alert events are global (cross-domain relevant).
// Delegation events inherit shared_domain scope.
// All others default to shared_domain.
func determineEventScope(actionType string, step StepResult) core.MemoryScope {
	switch actionType {
	case "alert_fired", "service_down":
		return core.ScopeGlobal
	default:
		return core.ScopeSharedDomain
	}
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// --- Formatting Helpers ---

func formatEpisodicEvents(events []core.AgentEvent, headers ...string) string {
	header := "Recent agent activity for entities in this request:"
	if len(headers) > 0 && headers[0] != "" {
		header = headers[0]
	}
	var sb strings.Builder
	sb.WriteString(header)
	for _, e := range events {
		if e.Summary != "" {
			fmt.Fprintf(&sb, "\n- [%s] %s: %s (outcome: %s)",
				e.Timestamp.Format("2006-01-02 15:04"),
				e.AgentName,
				e.Summary,
				e.Outcome,
			)
		} else {
			fmt.Fprintf(&sb, "\n- [%s] %s %s on %s/%s (outcome: %s)",
				e.Timestamp.Format("2006-01-02 15:04"),
				e.AgentName,
				e.ActionType,
				e.EntityType,
				e.EntityID,
				e.Outcome,
			)
		}
	}
	return sb.String()
}

// resolveStepSummary returns the LLM-generated summary if available, otherwise falls back
// to the heuristic buildActionableSummary.
// toEntityRefs converts orchestration Entity slice to core EntityRef slice.
func toEntityRefs(entities []Entity) []core.EntityRef {
	refs := make([]core.EntityRef, len(entities))
	for i, e := range entities {
		refs[i] = core.EntityRef{Type: e.Type, ID: e.ID}
	}
	return refs
}

func resolveStepSummary(llmSummaries map[string]core.StepSummary, stepID, actionType string, entity Entity, step StepResult) string {
	if llmSummaries != nil {
		if s, ok := llmSummaries[stepID]; ok && s.Summary != "" {
			return s.Summary
		}
	}
	return buildActionableSummary(actionType, entity, step)
}

// buildActionableSummary creates a fact-based event summary that includes key output
// details (ticket IDs, channels, URLs) extracted from the tool response. This enables
// downstream agents to make informed decisions based on what actually happened.
func buildActionableSummary(actionType string, entity Entity, step StepResult) string {
	toolName := step.AgentName

	// Try to parse response JSON for key output fields
	var respData map[string]interface{}
	if step.Response != "" {
		// Response may be wrapped in a data envelope
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(step.Response), &raw); err == nil {
			if data, ok := raw["data"].(map[string]interface{}); ok {
				respData = data
			} else {
				respData = raw
			}
		}
	}

	// Build summary based on capability + extracted response fields
	switch actionType {
	case "create_issue":
		ticketKey := extractStringField(respData, "key", "issue_key", "ticket_key")
		project := extractStringField(respData, "project_key", "project")
		if project == "" {
			if p, ok := step.Parameters["project_key"].(string); ok {
				project = p
			}
		}
		priority := extractStringField(respData, "priority")
		if priority == "" {
			if p, ok := step.Parameters["priority"].(string); ok {
				priority = p
			}
		}
		summary := extractStringField(respData, "summary")
		parts := []string{fmt.Sprintf("JIRA ticket %s created", ticketKey)}
		if project != "" {
			parts = append(parts, fmt.Sprintf("project: %s", project))
		}
		if priority != "" {
			parts = append(parts, fmt.Sprintf("priority: %s", priority))
		}
		if summary != "" && len(summary) <= 80 {
			parts = append(parts, fmt.Sprintf("summary: %s", summary))
		}
		return fmt.Sprintf("%s (%s) for %s/%s via %s",
			parts[0], strings.Join(parts[1:], ", "), entity.Type, entity.ID, toolName)

	case "send_message", "send_rich_message":
		channel := ""
		if c, ok := step.Parameters["channel"].(string); ok {
			channel = c
		}
		serviceName := "Slack" // default; derive from tool name
		if strings.Contains(strings.ToLower(toolName), "teams") {
			serviceName = "Teams"
		} else if strings.Contains(strings.ToLower(toolName), "webex") {
			serviceName = "Webex"
		}
		if channel != "" {
			return fmt.Sprintf("%s message sent to %s %s for %s/%s via %s",
				serviceName, channel, "channel", entity.Type, entity.ID, toolName)
		}
		return fmt.Sprintf("%s message sent for %s/%s via %s",
			serviceName, entity.Type, entity.ID, toolName)

	case "rollout_restart":
		ns := ""
		deploy := ""
		if n, ok := step.Parameters["namespace"].(string); ok {
			ns = n
		}
		if d, ok := step.Parameters["deployment_name"].(string); ok {
			deploy = d
		}
		if deploy != "" && ns != "" {
			return fmt.Sprintf("rollout restart executed on deployment/%s in %s for %s/%s via %s",
				deploy, ns, entity.Type, entity.ID, toolName)
		}
		return fmt.Sprintf("rollout restart executed for %s/%s via %s",
			entity.Type, entity.ID, toolName)

	case "delegated":
		return fmt.Sprintf("delegated to %s for %s/%s",
			toolName, entity.Type, entity.ID)

	default:
		return fmt.Sprintf("%s on %s/%s via %s",
			actionType, entity.Type, entity.ID, toolName)
	}
}

// extractStringField looks for the first matching key in a map and returns its string value.
func extractStringField(data map[string]interface{}, keys ...string) string {
	if data == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := data[key]; ok {
			if s, isStr := v.(string); isStr && s != "" {
				return s
			}
		}
	}
	return ""
}

func formatActiveInvestigations(active map[string]string, entities []Entity) string {
	// Only show investigations relevant to the entities in the request
	var relevant []string
	for _, entity := range entities {
		if holder, ok := active[entity.ID]; ok {
			relevant = append(relevant, fmt.Sprintf("- %s/%s is currently being investigated by %s", entity.Type, entity.ID, holder))
		}
	}
	if len(relevant) == 0 {
		return ""
	}
	return "Active investigations:\n" + strings.Join(relevant, "\n")
}

func formatKnowledgeFragments(knowledge []core.ScoredKnowledge) string {
	var sb strings.Builder
	sb.WriteString("Prior knowledge relevant to this request:")
	for _, k := range knowledge {
		fmt.Fprintf(&sb, "\n- [confidence: %.0f%%] %s", k.Score*100, k.Fragment.Content)
	}
	return sb.String()
}

// --- Context Helpers ---

// getBaggageFromContext extracts telemetry baggage from context.
// Returns nil if not available (telemetry not initialized).
func getBaggageFromContext(ctx context.Context) map[string]string {
	// Use the enrichments from PipelineContext if available
	enrichments := core.GetPipelineEnrichments(ctx)
	if enrichments == nil {
		return nil
	}
	// The request_id is also available via orchestration context
	result := make(map[string]string)
	if reqID := GetRequestID(ctx); reqID != "" {
		result["request_id"] = reqID
	}
	return result
}

// memoryTraceID extracts a trace/correlation ID from context for episodic event tagging.
func memoryTraceID(ctx context.Context) string {
	return GetRequestID(ctx)
}

// getParentEventFromContext extracts the parent event ID for nested delegation.
// When an agent is invoked as a delegated step by a parent orchestrator,
// the parent's request_id arrives via X-TruvaG3-Request-ID header (extracted by
// core.ExtractRequestContext into the Go context). If the current request_id
// differs from the step_id context, we're in a delegation — the parent's
// request_id becomes the ParentEvent linkage.
func getParentEventFromContext(ctx context.Context) string {
	// If there's a step_id in context, we were invoked as a step in a parent's plan.
	// The request_id in that case is the parent's orchestration request.
	stepID := core.GetStepID(ctx)
	if stepID != "" {
		// We're inside a delegated execution — return the parent's request_id
		return GetRequestID(ctx)
	}
	return "" // Top-level orchestrator — no parent
}

// --- Digest caching helpers ---

type cachedDigestData struct {
	Content           string    `json:"content"`
	LastEventTS       time.Time `json:"last_event_ts"`
	GeneratedAt       time.Time `json:"generated_at"`
	PolicyFingerprint string    `json:"policy_fingerprint,omitempty"`
}

func (h *MemoryEnrichmentHook) getCachedDigest(ctx context.Context, policyFingerprint string) (*cachedDigestData, error) {
	if h.digestCache == nil {
		return nil, nil
	}
	data, err := h.digestCache.GetDigest(ctx, h.agentDomain)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var cached cachedDigestData
	if err := json.Unmarshal(data, &cached); err != nil {
		h.logger.WarnWithContext(ctx, "Corrupt digest cache entry, treating as cache miss", map[string]interface{}{
			"operation":  "memory_enrichment",
			"error":      err.Error(),
			"error_type": "cache_unmarshal",
			"domain":     h.agentDomain,
		})
		telemetry.RecordSpanError(ctx, err)
		return nil, err
	}
	if cached.PolicyFingerprint != policyFingerprint {
		return nil, nil
	}
	return &cached, nil
}

func (h *MemoryEnrichmentHook) storeCachedDigest(
	ctx context.Context,
	content string,
	lastEventTS time.Time,
	policyFingerprint string,
	cacheSafe bool,
) {
	if h.digestCache == nil || !cacheSafe {
		return
	}
	cached := cachedDigestData{
		Content:           content,
		LastEventTS:       lastEventTS,
		GeneratedAt:       time.Now(),
		PolicyFingerprint: policyFingerprint,
	}
	data, err := json.Marshal(cached)
	if err != nil {
		return
	}
	if setErr := h.digestCache.SetDigest(ctx, h.agentDomain, data, h.digestCacheTTL); setErr != nil {
		telemetry.RecordSpanError(ctx, setErr)
	}
}

func newestEventTS(events []core.AgentEvent) time.Time {
	var newest time.Time
	for _, e := range events {
		if e.Timestamp.After(newest) {
			newest = e.Timestamp
		}
	}
	return newest
}
