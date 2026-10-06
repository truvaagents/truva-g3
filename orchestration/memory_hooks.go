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
		if err := validateMemoryRetrievalWeights(weights); err != nil {
			return err
		}
		h.weights = weights
		return nil
	}
}

// WithEnrichmentMaxTokens bounds this hook's derived memory view at four encoded
// bytes per token (an approximation, not a tokenizer count).
func WithEnrichmentMaxTokens(maxTokens int) MemoryEnrichmentOption {
	return func(h *MemoryEnrichmentHook) error {
		if maxTokens <= 0 || maxTokens > int(^uint(0)>>1)/4 {
			return fmt.Errorf("maxTokens must be positive and fit its four-byte budget, got %d", maxTokens)
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
// as baseline situational awareness (regardless of entity extraction). Default: 20.
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
	var sections []memoryViewSection
	var memoryContext string
	var viewReport memoryViewReport

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
	// Deduplicate admitted display records, not fetched events. A domain event
	// excluded from the detail view remains eligible in entity-specific history.
	if h.episodic != nil {
		since := time.Now().Add(-h.lookbackWindow)

		// Use higher limit when compactor is available — it will digest them
		queryLimit := h.recentEventsLimit
		if h.compactor != nil {
			queryLimit = h.compactionRawLimit
		}

		recentEvents, observedRead, err := h.readRecentWindow(ctx, since, queryLimit)
		if err != nil {
			if h.logger != nil {
				h.logger.WarnWithContext(ctx, "Recent events query failed, skipping", map[string]interface{}{
					"operation":  "memory_enrichment",
					"request_id": requestID,
					"error_type": "episodic_recent_read",
				})
			}
			telemetry.Counter("memory.enrichment.episodic_query_errors",
				"module", telemetry.ModuleOrchestration,
			)
		} else if len(recentEvents) > 0 {
			// Compact if available, otherwise raw
			if h.compactor != nil {
				cacheDecisionStart := time.Now()
				digest, cachePath, cacheHit, coverage := h.compactWindow(ctx, recentEvents, observedRead)
				effectStatus := core.PipelineHookEffectSucceeded
				if digest == "" {
					effectStatus = core.PipelineHookEffectSkipped
				}
				reportStructuredPipelineHookEffect(ctx, "memory_digest_window", "Activity digest window", effectStatus,
					"Bounded digest selection; coverage is not a full-history guarantee",
					map[string]interface{}{"path": cachePath, "cache_entry_found": cacheHit, "coverage": coverage, "returned_events": len(recentEvents)}, nil, cacheDecisionStart)
				detailEvents := recentEvents
				if digest == "" {
					fallbackEvents := recentEvents[:min(len(recentEvents), h.recentEventsLimit)]
					sections = append(sections, eventMemorySection("domain", "Historical domain events:", fallbackEvents))
				}

				// Cache decision observability (§6.7.7)
				telemetry.AddSpanEvent(ctx, "activity.compaction.cache_decision",
					attribute.String("request_id", requestID),
					attribute.Bool("cache_hit", cacheHit),
					attribute.String("coverage", coverage),
					attribute.String("path", cachePath),
					attribute.Int64("duration_ms", time.Since(cacheDecisionStart).Milliseconds()),
				)
				h.logger.DebugWithContext(ctx, "Digest cache decision", map[string]interface{}{
					"operation":   "memory_enrichment",
					"request_id":  requestID,
					"cache_hit":   cacheHit,
					"coverage":    coverage,
					"path":        cachePath,
					"duration_ms": time.Since(cacheDecisionStart).Milliseconds(),
				})

				// Inject digest + recent detail events
				if digest != "" {
					sections = append(sections, textMemorySection("domain", "Historical domain activity digest:", "Coverage: bounded returned event window; older history may be omitted.\n"+digest))
					recentDetailCount := h.compactionRecentDetail
					if detailEvents != nil && recentDetailCount > len(detailEvents) {
						recentDetailCount = len(detailEvents)
					}
					if detailEvents != nil && recentDetailCount > 0 {
						sections = append(sections, eventMemorySection("domain", "Historical domain events (detail):", detailEvents[:recentDetailCount]))
					}
				}
			} else {
				// No compactor — use raw events (Phase 5 behavior)
				sections = append(sections, eventMemorySection("domain", "Historical domain events:", recentEvents))
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
				entityEvents = append(entityEvents, events...)
			}
			if len(entityEvents) > 0 {
				sections = append(sections, eventMemorySection("entity", "Historical activity for the requested entities:", entityEvents))
			}
		}
	}

	// 3. Check active investigations and claim entities
	if h.coordinator != nil && len(entities) > 0 {
		active, err := h.coordinator.GetActiveInvestigations(ctx)
		if err == nil && len(active) > 0 {
			section := formatActiveInvestigations(active, entities)
			if section != "" {
				sections = append(sections, textMemorySection("investigation", "Investigation snapshot at request start:", section))
			}
		}

		// Claim investigation on discovered entities (prevents duplicate work by other agents)
		seenClaims := make(map[string]bool)
		for _, entity := range entities {
			if seenClaims[entity.ID] {
				continue
			}
			seenClaims[entity.ID] = true
			var claimed bool
			var holder string
			var claimErr error
			if coordinator, ok := h.coordinator.(core.InvestigationLeaseCoordinator); ok && resourcesFromContext(ctx) != nil {
				claimed, holder, claimErr = resourcesFromContext(ctx).acquire(ctx, coordinator, h.agentName, entity.ID)
			} else {
				claimed, holder, claimErr = h.coordinator.ClaimInvestigation(ctx, h.agentName, entity.ID, 0)
				telemetry.AddSpanEvent(ctx, "memory.investigation.legacy_ownership",
					attribute.String("request_id", requestID),
					attribute.String("ownership", "agent_name_only"))
			}
			if claimErr != nil {
				h.logger.WarnWithContext(ctx, "Investigation acquisition could not confirm ownership", map[string]interface{}{"operation": "investigation_acquire", "error_type": "ownership_unknown", "request_id": GetRequestID(ctx)})
				continue // Fail-open
			}
			if !claimed {
				// Another agent already investigating — add to context
				sections = append(sections, textMemorySection("investigation", "Investigation claim observation:", fmt.Sprintf("Note: %s/%s is currently being investigated by %s. Consider coordinating rather than duplicating work.", entity.Type, entity.ID, holder)))
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
			sections = append(sections, textMemorySection("knowledge", "Retrieved shared knowledge:", section))
		}
	}

	// 5. Assemble and inject into enrichments
	memoryContext, viewReport = renderMemoryView(sections, h.maxTokens*4)
	telemetry.AddSpanEvent(ctx, "memory.enrichment.view_bounded",
		attribute.String("request_id", requestID),
		attribute.String("view_version", viewReport.ViewVersion),
		attribute.String("event_format", viewReport.EventFormat),
		attribute.Int("budget_bytes", viewReport.BudgetBytes),
		attribute.Int("context_bytes", viewReport.UsedBytes),
		attribute.String("outcome", viewReport.Outcome),
	)
	for _, counts := range viewReport.Sections {
		telemetry.AddSpanEvent(ctx, "memory.enrichment.section_bounded",
			attribute.String("request_id", requestID),
			attribute.String("section", counts.Kind),
			attribute.Int("admitted", counts.Admitted),
			attribute.Int("omitted", counts.Omitted),
			attribute.Int("shortened", counts.Shortened),
			attribute.Int("duplicates", counts.Duplicates),
		)
	}
	if memoryContext != "" {
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
			"context_bytes":  viewReport.UsedBytes,
			"budget_bytes":   viewReport.BudgetBytes,
			"view_outcome":   viewReport.Outcome,
			"duration_ms":    time.Since(startTime).Milliseconds(),
		})

		// Pattern 6: AddSpanEvent with request_id first
		telemetry.AddSpanEvent(ctx, "memory.enrichment.injected",
			attribute.String("request_id", requestID),
			attribute.Int("entities_found", len(entities)),
			attribute.Int("context_chars", len(memoryContext)),
			attribute.Int("context_bytes", viewReport.UsedBytes),
			attribute.Int("budget_bytes", viewReport.BudgetBytes),
			attribute.String("view_outcome", viewReport.Outcome),
		)
		// Pattern 5: Counter with module label
		telemetry.Counter("memory.enrichment.injected",
			"module", telemetry.ModuleOrchestration,
		)
	}
	effectStatus := core.PipelineHookEffectSucceeded
	effectSummary := "Prepared shared-memory context for planning"
	if memoryContext == "" {
		effectStatus = core.PipelineHookEffectSkipped
		effectSummary = "No shared-memory context was available to inject"
	}
	if viewReport.Outcome == "budget_too_small" {
		effectSummary = "Memory view omitted: budget_too_small for scope and section metadata"
		h.logger.DebugWithContext(ctx, "Memory view budget too small", map[string]interface{}{
			"operation": "memory_enrichment", "request_id": requestID,
			"budget_bytes": viewReport.BudgetBytes, "view_outcome": viewReport.Outcome,
		})
	}
	reportStructuredPipelineHookEffect(
		ctx,
		"memory_planning_context",
		"Memory planning context",
		effectStatus,
		effectSummary,
		struct {
			AgentDomain string           `json:"agent_domain"`
			Entities    []core.EntityRef `json:"entities"`
			Sections    int              `json:"sections"`
			View        memoryViewReport `json:"view"`
		}{
			AgentDomain: h.agentDomain,
			Entities:    toEntityRefs(entities),
			Sections:    len(sections),
			View:        viewReport,
		},
		nil,
		startTime,
	)

	return nil, nil // Never short-circuits
}

// --- Memory Record Hook (AfterExecutionHook) ---

type memoryEventEffect struct {
	StepID                  string                        `json:"step_id"`
	SubmittedEvent          core.AgentEvent               `json:"submitted_event"`
	ProviderCallStatus      core.PipelineHookEffectStatus `json:"provider_call_status"`
	BackendAssignsEventID   bool                          `json:"backend_assigns_event_id"`
	BackendAssignsTimestamp bool                          `json:"backend_assigns_timestamp"`
	ProviderError           string                        `json:"provider_error,omitempty"`
}

type memoryClaimReleaseEffect struct {
	EntityID           string                        `json:"entity_id"`
	ProviderCallStatus core.PipelineHookEffectStatus `json:"provider_call_status"`
	ProviderError      string                        `json:"provider_error,omitempty"`
}

// MemoryRecordHook implements core.AfterExecutionHook.
// It records structured AgentEvents from execution results into episodic memory,
// and releases investigation claims.
type MemoryRecordHook struct {
	episodic               core.EpisodicMemory
	coordinator            core.InvestigationCoordinator // may be nil
	summarizer             core.EventSummarizer          // may be nil — falls back to heuristic
	agentName              string
	agentDomain            string
	entityExtractor        EntityExtractor
	requestEntityExtractor EntityExtractor // opt-in; recording only, never owns claims
	failureCauseMaxBytes   int
	importanceFunc         func(actionType, outcome string) float64
	logger                 core.Logger
}

// MemoryRecordOption configures MemoryRecordHook.
// Returns error if the option value is invalid (fail-fast per core/ARCHITECTURE.md).
type MemoryRecordOption func(*MemoryRecordHook) error

// WithRecordFailureCauseMaxBytes bounds only the cause text in heuristic failure
// summaries (default 256 bytes). A shortening marker is additional to this limit.
// Model/custom summaries and original execution errors remain unchanged.
func WithRecordFailureCauseMaxBytes(n int) MemoryRecordOption {
	return func(h *MemoryRecordHook) error {
		if n <= 0 {
			return errPositiveRequired("record failure cause byte limit")
		}
		h.failureCauseMaxBytes = n
		return nil
	}
}

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

// WithRecordRequestEntityExtractor adds explicit request entities to each event.
// The hook passes PipelineContext.Request and copied JSON-like Metadata containers
// at recording time; opaque values are application-owned and read-only. Request
// entities precede distinct step entities. These added indexes acquire or release
// no investigation claims. Nil pipeline contexts provide no request entities.
// Unset retains existing step-only extraction; nil is an invalid explicit option.
func WithRecordRequestEntityExtractor(extractor EntityExtractor) MemoryRecordOption {
	return func(h *MemoryRecordHook) error {
		if extractor == nil {
			return fmt.Errorf("record request entity extractor cannot be nil")
		}
		h.requestEntityExtractor = extractor
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
		episodic:             episodic,
		coordinator:          coordinator,
		agentName:            agentName,
		agentDomain:          agentDomain,
		entityExtractor:      NoOpEntityExtractor{},
		failureCauseMaxBytes: defaultRecordFailureCauseMaxBytes,
		importanceFunc:       DefaultImportanceScorer,
		logger:               &core.NoOpLogger{},
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
	effectStart := time.Now()
	execResult, ok := results.(*ExecutionResult)
	if !ok || execResult == nil {
		reportStructuredPipelineHookEffect(
			ctx,
			"episodic_memory_records",
			"Episodic memory records",
			core.PipelineHookEffectSkipped,
			"No execution result was available to record",
			nil,
			nil,
			effectStart,
		)
		return nil
	}

	// Get request_id for span events and nested delegation tagging
	requestID := GetRequestID(ctx)
	parentRequestID := requestID
	if baggage := getBaggageFromContext(ctx); baggage != nil {
		parentRequestID = baggage["request_id"]
	}

	// Batch-summarize steps via LLM if summarizer is configured
	var llmSummaries map[string]core.StepSummary
	var batchExcluded map[string]bool
	fallbackReason := "not_configured"
	summaryCoverage := memorySummaryCoverage{Source: "heuristic"}
	if h.summarizer != nil {
		fallbackReason = "missing_summary"
		summaryCoverage.Source = "custom"
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
				Error:       step.Error,
				ToolError:   append(json.RawMessage(nil), step.ToolError...),
			})
		}
		if len(inputs) > 0 {
			var summaries map[string]core.StepSummary
			var err error
			if builtin, ok := h.summarizer.(*LLMEventSummarizer); ok {
				parsed := builtin.summarizeWithOutcome(ctx, inputs)
				batchExcluded = parsed.batchExcludedIDs
				summaryCoverage.Source, summaryCoverage.Batch = "builtin", &parsed
				fallbackReason = parsed.Outcome
				if ctx.Err() != nil {
					reportStructuredPipelineHookEffect(ctx, "episodic_memory_records", "Episodic memory records",
						core.PipelineHookEffectFailed, "Parent request cancelled during summarization; no fallback writes attempted",
						summaryCoverage, ctx.Err(), effectStart)
					return ctx.Err()
				}
				summaries = parsed.Valid
			} else {
				summaries, err = h.summarizer.SummarizeSteps(ctx, inputs)
			}
			if err != nil {
				fallbackReason = "summarizer_error"
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
	var eventEffects []memoryEventEffect
	var releaseEffects []memoryClaimReleaseEffect
	var firstEffectErr error
	var requestEntities []Entity
	if h.requestEntityExtractor != nil && pctx != nil {
		requestEntities = mergeMemoryEntities(h.requestEntityExtractor.ExtractEntities(pctx.Request, cloneParameterMap(pctx.Metadata)), nil)
	}

	for _, step := range execResult.Steps {
		if step.Skipped {
			// Neither dependency-blocked nor refinement-satisfied steps were
			// dispatched. Record actual attempts, including root failures;
			// retain skip reasons and causal links in execution evidence only.
			continue
		}
		summaryCoverage.Eligible++
		if summary, ok := llmSummaries[step.StepID]; ok && summary.Summary != "" {
			summaryCoverage.Summarized++
		} else {
			summaryCoverage.Fallback++
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

		stepEntities := h.entityExtractor.ExtractEntities(step.Instruction, extractionMetadata)
		entities := stepEntities
		if h.requestEntityExtractor != nil {
			entities = mergeMemoryEntities(requestEntities, stepEntities)
		}

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
			if len(requestEntities) > 0 {
				extractionSource = "request_metadata"
				if len(stepEntities) > 0 {
					extractionSource = "request_and_step"
				}
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
		stepFallbackReason := memoryFallbackReason(fallbackReason)
		if batchExcluded[step.StepID] {
			stepFallbackReason = "batch_limit"
		}
		eventMetadata := memorySummaryMetadata(llmSummaries, step.StepID, h.summarizer, stepFallbackReason)
		eventMetadata["entity_source"] = extractionSource
		occurredAt := memoryEventTime(step, time.Now(), eventMetadata)
		var primary Entity
		if len(entities) > 0 {
			primary = entities[0]
		}
		summary, causeShortened := resolveStepSummary(llmSummaries, step.StepID, actionType, primary, step, h.failureCauseMaxBytes)
		if causeShortened {
			eventMetadata["summary_cause_shortened"] = "true"
		}

		if len(entities) > 0 {
			// One event per step, indexed under all entities
			event := core.AgentEvent{
				Timestamp:   occurredAt,
				Metadata:    eventMetadata,
				AgentName:   h.agentName,
				AgentDomain: h.agentDomain,
				ActionType:  actionType,
				EntityType:  primary.Type,
				EntityID:    primary.ID,
				Entities:    toEntityRefs(entities),
				Summary:     summary,
				Outcome:     outcome,
				TraceID:     memoryTraceID(ctx),
				RequestID:   parentRequestID,
				ParentEvent: getParentEventFromContext(ctx),
				Scope:       determineEventScope(actionType, step),
				Importance:  h.importanceFunc(actionType, outcome),
			}

			recordErr := h.episodic.RecordEvent(ctx, event)
			eventEffect := memoryEventEffect{
				StepID:                  step.StepID,
				SubmittedEvent:          event,
				ProviderCallStatus:      core.PipelineHookEffectSucceeded,
				BackendAssignsEventID:   event.EventID == "",
				BackendAssignsTimestamp: event.Timestamp.IsZero(),
			}
			if recordErr != nil {
				if firstEffectErr == nil {
					firstEffectErr = recordErr
				}
				eventEffect.ProviderCallStatus = core.PipelineHookEffectFailed
				eventEffect.ProviderError = recordErr.Error()
				recordPipelineHookEffectSpanError(ctx, "episodic_write")
			}
			eventEffects = append(eventEffects, eventEffect)
			if err := recordErr; err != nil {
				h.logger.WarnWithContext(ctx, "Failed to record episodic event, continuing", map[string]interface{}{
					"operation":   "memory_record",
					"request_id":  requestID,
					"entity_type": primary.Type,
					"entity_id":   primary.ID,
					"error_type":  "episodic_write",
				})
			}

			for _, entity := range stepEntities {
				investigatedEntities = append(investigatedEntities, entity.ID)
			}
		} else {
			// Record a domain-level event with no entity index.
			event := core.AgentEvent{
				Timestamp:   occurredAt,
				Metadata:    eventMetadata,
				AgentName:   h.agentName,
				AgentDomain: h.agentDomain,
				ActionType:  actionType,
				Summary:     summary,
				Outcome:     outcome,
				TraceID:     memoryTraceID(ctx),
				RequestID:   parentRequestID,
				ParentEvent: getParentEventFromContext(ctx),
				Scope:       determineEventScope(actionType, step),
				Importance:  h.importanceFunc(actionType, outcome),
			}
			recordErr := h.episodic.RecordEvent(ctx, event)
			eventEffect := memoryEventEffect{
				StepID:                  step.StepID,
				SubmittedEvent:          event,
				ProviderCallStatus:      core.PipelineHookEffectSucceeded,
				BackendAssignsEventID:   event.EventID == "",
				BackendAssignsTimestamp: event.Timestamp.IsZero(),
			}
			if recordErr != nil {
				if firstEffectErr == nil {
					firstEffectErr = recordErr
				}
				eventEffect.ProviderCallStatus = core.PipelineHookEffectFailed
				eventEffect.ProviderError = recordErr.Error()
				recordPipelineHookEffectSpanError(ctx, "episodic_write")
			}
			eventEffects = append(eventEffects, eventEffect)
			if err := recordErr; err != nil {
				h.logger.WarnWithContext(ctx, "Failed to record entity-less episodic event, continuing", map[string]interface{}{
					"operation":  "memory_record",
					"request_id": requestID,
					"error_type": "episodic_write",
				})
			}
		}
	}

	// Emit telemetry for recorded events
	if len(eventEffects) > 0 {
		telemetry.AddSpanEvent(ctx, "memory.record.events_written",
			attribute.String("request_id", requestID),
			attribute.Int("events_recorded", len(eventEffects)),
			attribute.String("agent_name", h.agentName),
		)
		telemetry.Counter("memory.record.events_total",
			"module", telemetry.ModuleOrchestration,
			"agent", h.agentName,
		)
	}

	// Release investigation claims for all entities we acted on
	_, tokenCoordinator := h.coordinator.(core.InvestigationLeaseCoordinator)
	if h.coordinator != nil && (!tokenCoordinator || resourcesFromContext(ctx) == nil) {
		for _, entityID := range investigatedEntities {
			releaseErr := h.coordinator.ReleaseInvestigation(ctx, h.agentName, entityID)
			releaseEffect := memoryClaimReleaseEffect{
				EntityID:           entityID,
				ProviderCallStatus: core.PipelineHookEffectSucceeded,
			}
			if releaseErr != nil {
				if firstEffectErr == nil {
					firstEffectErr = releaseErr
				}
				releaseEffect.ProviderCallStatus = core.PipelineHookEffectFailed
				releaseEffect.ProviderError = releaseErr.Error()
				recordPipelineHookEffectSpanError(ctx, "claim_release")
			}
			releaseEffects = append(releaseEffects, releaseEffect)
			if err := releaseErr; err != nil {
				h.logger.WarnWithContext(ctx, "Failed to release investigation claim", map[string]interface{}{
					"operation":  "memory_record",
					"request_id": requestID,
					"entity_id":  entityID,
					"error_type": "claim_release",
				})
			}
		}
	}

	failedEffects := 0
	for _, effect := range eventEffects {
		if effect.ProviderCallStatus == core.PipelineHookEffectFailed {
			failedEffects++
		}
	}
	for _, effect := range releaseEffects {
		if effect.ProviderCallStatus == core.PipelineHookEffectFailed {
			failedEffects++
		}
	}
	totalEffects := len(eventEffects) + len(releaseEffects)
	effectStatus := core.PipelineHookEffectSucceeded
	effectSummary := fmt.Sprintf("Submitted %d episodic event(s) to the fail-open memory backend", len(eventEffects))
	switch {
	case totalEffects == 0:
		effectStatus = core.PipelineHookEffectSkipped
		effectSummary = "No executable steps produced episodic memory records"
	case failedEffects == totalEffects:
		effectStatus = core.PipelineHookEffectFailed
		effectSummary = "Every episodic-memory and claim-release provider call returned an error"
	case failedEffects > 0:
		effectStatus = core.PipelineHookEffectPartial
		effectSummary = fmt.Sprintf(
			"Provider calls returned without error for %d of %d episodic-memory and claim-release submissions",
			totalEffects-failedEffects,
			totalEffects,
		)
	}
	summaryAttrs := []attribute.KeyValue{
		attribute.String("request_id", requestID), attribute.String("source", summaryCoverage.Source),
		attribute.Int("eligible", summaryCoverage.Eligible), attribute.Int("summarized", summaryCoverage.Summarized),
		attribute.Int("fallback", summaryCoverage.Fallback),
	}
	if summaryCoverage.Batch != nil {
		summaryAttrs = append(summaryAttrs, summaryCoverage.Batch.EntityFields.spanAttributes()...)
	}
	telemetry.AddSpanEvent(ctx, "memory.record.summary_coverage", summaryAttrs...)
	reportStructuredPipelineHookEffect(
		ctx,
		"episodic_memory_records",
		"Episodic memory records",
		effectStatus,
		effectSummary,
		struct {
			Events        []memoryEventEffect        `json:"events"`
			ClaimReleases []memoryClaimReleaseEffect `json:"claim_releases"`
			Summaries     memorySummaryCoverage      `json:"summary_coverage"`
		}{Events: eventEffects, ClaimReleases: releaseEffects, Summaries: summaryCoverage},
		firstEffectErr,
		effectStart,
	)

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

// Keep this scope notice before digest/detail text so bounded selectors retain
// it. This enrichment is a request-start snapshot, not current phase progress.
const historicalMemoryScopeNotice = "Memory scope: historical domain/entity evidence at request start. Attribute outcomes to their recorded origin; establish current-request progress from current execution results."

func formatEpisodicEvents(events []core.AgentEvent, headers ...string) string {
	header := "Historical activity for the requested entities:"
	if len(headers) > 0 && headers[0] != "" {
		header = headers[0]
	}
	var sb strings.Builder
	sb.WriteString(header)
	for _, e := range events {
		// All fields are strings or entity references; JSON encoding preserves
		// literal data and prevents it from terminating a surrounding XML section.
		data, _ := json.Marshal(eventMemoryView(e, false))
		sb.WriteString("\n- ")
		sb.Write(data)
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

func resolveStepSummary(llmSummaries map[string]core.StepSummary, stepID, actionType string, entity Entity, step StepResult, maxCauseBytes int) (string, bool) {
	if llmSummaries != nil {
		if s, ok := llmSummaries[stepID]; ok && s.Summary != "" {
			return s.Summary, false
		}
	}
	if !step.Success {
		return failedMemorySummary(actionType, entity, step, maxCauseBytes)
	}
	return buildActionableSummary(actionType, entity, step), false
}

// buildActionableSummary creates a fact-based event summary that includes key output
// details (ticket IDs, channels, URLs) extracted from the tool response. This enables
// downstream agents to make informed decisions based on what actually happened.
func buildActionableSummary(actionType string, entity Entity, step StepResult) string {
	toolName := step.AgentName
	if !step.Success {
		summary, _ := failedMemorySummary(actionType, entity, step, defaultRecordFailureCauseMaxBytes)
		return summary
	}

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
		if entity.Type == "" || entity.ID == "" {
			return fmt.Sprintf("delegated to %s", toolName)
		}
		return fmt.Sprintf("delegated to %s for %s/%s",
			toolName, entity.Type, entity.ID)

	default:
		if entity.Type == "" || entity.ID == "" {
			return fmt.Sprintf("%s succeeded via %s", actionType, toolName)
		}
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

// memoryTraceID returns only an actual trace ID, not request correlation identity.
func memoryTraceID(ctx context.Context) string {
	return telemetry.GetTraceContext(ctx).TraceID
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

const activityDigestViewVersion = "historical-origin/v5-record-facts"

type cachedDigestData struct {
	Window            *digestWindow `json:"window,omitempty"`
	ViewVersion       string        `json:"view_version,omitempty"`
	Content           string        `json:"content"`
	LastEventTS       time.Time     `json:"last_event_ts"`
	GeneratedAt       time.Time     `json:"generated_at"`
	PolicyFingerprint string        `json:"policy_fingerprint,omitempty"`
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
	if cached.ViewVersion != activityDigestViewVersion || cached.PolicyFingerprint != policyFingerprint || strings.TrimSpace(cached.Content) == "" {
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
	windows ...*digestWindow,
) {
	if h.digestCache == nil || !cacheSafe {
		return
	}
	cached := cachedDigestData{
		ViewVersion:       activityDigestViewVersion,
		Content:           content,
		LastEventTS:       lastEventTS,
		GeneratedAt:       time.Now(),
		PolicyFingerprint: policyFingerprint,
	}
	if len(windows) > 0 {
		cached.Window = windows[0]
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
