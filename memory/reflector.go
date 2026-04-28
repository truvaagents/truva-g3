package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Compile-time interface compliance check.
var _ core.MemoryReflector = (*LLMMemoryReflector)(nil)

// LLMMemoryReflector implements MemoryReflector using an core.AIClient to synthesize
// higher-level knowledge from accumulated episodic events.
//
// Not called per-request. Invoked by the application via CronJob or admin endpoint.
// Per FRAMEWORK_DESIGN_PRINCIPLES: framework provides the logic, application decides
// when to run it (same pattern as telemetry.Initialize).
//
// The reflector reads episodic events, asks the LLM to extract patterns, and returns
// KnowledgeFragments with Embedding=nil — the caller is responsible for embedding
// and storing them in SharedKnowledge.
type LLMMemoryReflector struct {
	aiClient  core.AIClient
	episodic  core.EpisodicMemory
	domain    string
	logger    core.Logger
	telemetry core.Telemetry // optional — for span creation
	minEvents int            // Minimum events to trigger reflection (default: 5)
	model     string         // optional model alias or concrete name (empty = chain default)
}

// ReflectorOption configures LLMMemoryReflector.
// Returns error if the option value is invalid (fail-fast per CORE_DESIGN_PRINCIPLES).
type ReflectorOption func(*LLMMemoryReflector) error

// WithReflectorLogger sets the logger.
// Rejects nil — use &core.NoOpLogger{} to explicitly disable logging.
func WithReflectorLogger(logger core.Logger) ReflectorOption {
	return func(r *LLMMemoryReflector) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		// Wrap with component context if supported (LOGGING_IMPLEMENTATION_GUIDE §ComponentAwareLogger)
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			r.logger = cal.WithComponent("framework/memory")
		} else {
			r.logger = logger
		}
		return nil
	}
}

// WithReflectorTelemetry sets the telemetry provider for span creation.
// When set, Reflect() creates a child span visible in Jaeger.
// When nil, span events are still emitted on the parent span via telemetry package globals.
func WithReflectorTelemetry(t core.Telemetry) ReflectorOption {
	return func(r *LLMMemoryReflector) error {
		r.telemetry = t // nil is valid — disables span creation
		return nil
	}
}

// WithReflectorMinEvents sets the minimum number of events to trigger reflection.
func WithReflectorMinEvents(min int) ReflectorOption {
	return func(r *LLMMemoryReflector) error {
		if min <= 0 {
			return fmt.Errorf("minEvents must be positive, got %d", min)
		}
		r.minEvents = min
		return nil
	}
}

// WithReflectorModel sets the model used for reflection LLM calls (Reflect and Compact).
//
// Accepts either a cross-provider alias ("fast", "smart", "default") or a concrete
// model name. Aliases are resolved per-provider by the chain client at call time
// (e.g. "fast" → claude-haiku-4-5 on Anthropic, llama-3.1-8b-instant on Groq).
//
// Empty string (the default) means "use whatever model the wired AIClient picks" —
// for a ChainClient with no override, that's each provider's "default" alias.
//
// Reflection extracts durable knowledge fragments that influence every future request,
// so the trade-off here is real: cheaper/faster models reduce per-pass cost (often 4×+)
// but may produce more generic patterns. Stronger models extract richer rules at higher
// cost. There is no universally right answer — pick based on how much you trust the
// fragments to surface in <agent_memory> across many future requests.
//
// Configuration precedence (per FRAMEWORK_DESIGN_PRINCIPLES §3):
//  1. Explicit WithReflectorModel(...) option (highest)
//  2. TRUVAG3_REFLECTION_MODEL env var (read by BuildReflectionJob)
//  3. AIClient default (empty string)
func WithReflectorModel(model string) ReflectorOption {
	return func(r *LLMMemoryReflector) error {
		r.model = model // empty string is valid — disables override
		return nil
	}
}

// NewLLMMemoryReflector creates a reflector that uses an core.AIClient to synthesize knowledge.
// Returns error if required dependencies are nil (fail-fast per FRAMEWORK_DESIGN_PRINCIPLES).
func NewLLMMemoryReflector(
	aiClient core.AIClient,
	episodic core.EpisodicMemory,
	domain string,
	logger core.Logger,
	opts ...ReflectorOption,
) (*LLMMemoryReflector, error) {
	if aiClient == nil {
		return nil, fmt.Errorf("aiClient is required for LLMMemoryReflector")
	}
	if episodic == nil {
		return nil, fmt.Errorf("episodic memory is required for LLMMemoryReflector")
	}
	if domain == "" {
		domain = "default"
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/memory")
	}

	r := &LLMMemoryReflector{
		aiClient:  aiClient,
		episodic:  episodic,
		domain:    domain,
		logger:    logger,
		minEvents: 5,
	}
	for _, opt := range opts {
		if err := opt(r); err != nil {
			return nil, fmt.Errorf("invalid reflector option: %w", err)
		}
	}
	return r, nil
}

// Reflect examines recent events for an entity and generates knowledge fragments.
// Returns fragments with Embedding=nil — the caller must embed before storing.
func (r *LLMMemoryReflector) Reflect(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
	startTime := time.Now()
	requestID := core.GetRequestID(ctx)

	// Child span for Jaeger visibility (Pattern 6 — request_id as first attribute)
	var span core.Span
	if r.telemetry != nil {
		ctx, span = r.telemetry.StartSpan(ctx, "memory.reflection")
		defer span.End()
		span.SetAttribute("request_id", requestID)
		span.SetAttribute("entity_type", entityType)
		span.SetAttribute("entity_id", entityID)
	}

	// 1. Fetch episodic events for this entity
	events, err := r.episodic.QueryEntityHistory(ctx, r.domain, entityType, entityID, since)
	if err != nil {
		r.logger.WarnWithContext(ctx, "Reflection: failed to query episodic events", map[string]interface{}{
			"operation":   "reflect",
			"request_id":  requestID,
			"entity_type": entityType,
			"entity_id":   entityID,
			"error":       err.Error(),
			"error_type":  "episodic_read",
		})
		return nil, nil // Fail-open
	}

	if len(events) < r.minEvents {
		r.logger.InfoWithContext(ctx, "Reflection: insufficient events, skipping", map[string]interface{}{
			"operation":   "reflect",
			"request_id":  requestID,
			"entity_type": entityType,
			"entity_id":   entityID,
			"event_count": len(events),
			"min_events":  r.minEvents,
		})
		return nil, nil // Not enough data to reflect on
	}

	// 2. Format events for LLM
	eventSummaries := formatEventsForReflection(events)

	// 3. Ask LLM to synthesize patterns
	prompt := fmt.Sprintf(`You are analyzing %d events related to %s/%s to extract reusable knowledge.

Events (chronological):
%s

Extract 1-3 actionable knowledge fragments from these events. Each fragment should be:
- A reusable pattern or resolution strategy (not a description of a single event)
- Concise (1-2 sentences)
- Actionable for future agents handling similar situations

Return a JSON array. Each object has:
- "content": string — the knowledge statement
- "namespace": string — one of: "incidents", "runbooks", "decisions", "patterns"
- "importance": number — 1.0 to 10.0

Return [] if no reusable patterns are found.`, len(events), entityType, entityID, eventSummaries)

	aiResp, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Model:       r.model, // empty = chain default; alias ("fast"/"smart") resolved per-provider
		Temperature: 0.3,
		MaxTokens:   1000,
	})
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		r.logger.WarnWithContext(ctx, "Reflection: LLM call failed", map[string]interface{}{
			"operation":   "reflect",
			"request_id":  requestID,
			"entity_type": entityType,
			"entity_id":   entityID,
			"error":       err.Error(),
			"error_type":  "llm_unavailable",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		return nil, nil // Fail-open
	}

	// Record token usage for billing attribution
	core.RecordTokenUsage(ctx, "reflection", aiResp.Usage)

	telemetry.AddSpanEvent(ctx, "memory.reflection.llm_response",
		attribute.String("request_id", requestID),
		attribute.Int("response_length", len(aiResp.Content)),
	)

	// 4. Parse LLM response
	var extracted []struct {
		Content    string  `json:"content"`
		Namespace  string  `json:"namespace"`
		Importance float64 `json:"importance"`
	}

	content := aiResp.Content
	// Try to extract JSON array from response (LLM may add prose)
	if idx := strings.Index(content, "["); idx >= 0 {
		if end := strings.LastIndex(content, "]"); end > idx {
			content = content[idx : end+1]
		}
	}

	if err := json.Unmarshal([]byte(content), &extracted); err != nil {
		r.logger.WarnWithContext(ctx, "Reflection: failed to parse LLM response", map[string]interface{}{
			"operation":   "reflect",
			"request_id":  requestID,
			"entity_type": entityType,
			"entity_id":   entityID,
			"error":       err.Error(),
			"error_type":  "parse_failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		return nil, nil // Fail-open
	}

	// 5. Build KnowledgeFragments (Embedding=nil — caller handles embedding)
	var fragments []core.KnowledgeFragment
	sourceEventIDs := extractEventIDs(events)
	for _, e := range extracted {
		if e.Content == "" {
			continue
		}
		namespace := e.Namespace
		if namespace == "" {
			namespace = "patterns"
		}
		importance := e.Importance
		if importance <= 0 {
			importance = 5.0
		}

		fragments = append(fragments, core.KnowledgeFragment{
			Namespace:    namespace,
			Content:      e.Content,
			SourceEvents: sourceEventIDs,
			AgentDomain:  r.domain,
			Scope:        core.ScopeSharedDomain,
			Importance:   importance,
			CreatedAt:    time.Now(),
		})
	}

	if len(fragments) > 0 {
		r.logger.InfoWithContext(ctx, "Reflection: knowledge fragments generated", map[string]interface{}{
			"operation":         "reflect",
			"request_id":        requestID,
			"entity_type":       entityType,
			"entity_id":         entityID,
			"events_analyzed":   len(events),
			"fragments_created": len(fragments),
			"duration_ms":       time.Since(startTime).Milliseconds(),
		})
	}

	return fragments, nil
}

// Compact executes the memory compaction process:
// 1. Queries events older than config.EventAgeThreshold
// 2. Groups them by entity into digest windows
// 3. Creates summary digest events via LLM
// 4. Deletes the original events (replaced by digests)
//
// Idempotent — digest events have ActionType "digest" and are not re-digested.
func (r *LLMMemoryReflector) Compact(ctx context.Context, config core.CompactionConfig) error {
	cutoff := time.Now().Add(-config.EventAgeThreshold)

	// 1. Query old events
	oldEvents, err := r.episodic.QueryEvents(ctx, r.domain, core.EventFilter{
		Until: cutoff,
		Limit: 500, // Process in batches
	})
	if err != nil {
		r.logger.WarnWithContext(ctx, "Compaction: failed to query old events", map[string]interface{}{
			"operation": "compact",
			"error":     err.Error(),
		})
		return nil // Fail-open
	}

	// Filter out digest events (don't re-compact digests)
	var compactable []core.AgentEvent
	for _, e := range oldEvents {
		if e.ActionType != "digest" {
			compactable = append(compactable, e)
		}
	}

	if len(compactable) == 0 {
		r.logger.InfoWithContext(ctx, "Compaction: no events to compact", map[string]interface{}{
			"operation": "compact",
			"cutoff":    cutoff.Format(time.RFC3339),
		})
		return nil
	}

	// 2. Group events by entity — fan out multi-entity events into all groups
	entityGroups := make(map[string][]core.AgentEvent) // "type:id" → events
	for _, e := range compactable {
		if len(e.Entities) > 0 {
			for _, entity := range e.Entities {
				key := entity.Type + ":" + entity.ID
				entityGroups[key] = append(entityGroups[key], e)
			}
		} else {
			// Backward compat: singular fields
			key := e.EntityType + ":" + e.EntityID
			entityGroups[key] = append(entityGroups[key], e)
		}
	}

	digestsCreated := 0
	eventsDeleted := 0

	for entityKey, events := range entityGroups {
		if len(events) < 2 {
			continue // Not worth digesting a single event
		}

		if config.DryRun {
			r.logger.InfoWithContext(ctx, "Compaction dry run: would digest entity events", map[string]interface{}{
				"operation":   "compact",
				"entity":      entityKey,
				"event_count": len(events),
			})
			continue
		}

		// 3. Create digest via LLM
		eventSummaries := formatEventsForReflection(events)
		digestPrompt := fmt.Sprintf(`Summarize these %d events for entity %s into a single concise digest (2-3 sentences).
Focus on: what happened, outcomes, and any patterns.

Events:
%s

Return ONLY the summary text, no JSON.`, len(events), entityKey, eventSummaries)

		aiResp, aiErr := r.aiClient.GenerateResponse(ctx, digestPrompt, &core.AIOptions{
			Model:       r.model, // same model selection as Reflect()
			Temperature: 0.3,
			MaxTokens:   300,
		})
		if aiErr != nil {
			telemetry.RecordSpanError(ctx, aiErr)
			r.logger.WarnWithContext(ctx, "Compaction: LLM digest failed, skipping entity", map[string]interface{}{
				"operation":  "compact",
				"entity":     entityKey,
				"error":      aiErr.Error(),
				"error_type": "llm_unavailable",
			})
			continue
		}

		core.RecordTokenUsage(ctx, "compaction", aiResp.Usage)

		// 4. Store digest event — carry Entities from source if available
		digestEvent := core.AgentEvent{
			AgentName:   "compaction",
			AgentDomain: r.domain,
			ActionType:  "digest",
			EntityType:  events[0].EntityType,
			EntityID:    events[0].EntityID,
			Entities:    events[0].Entities, // Carry multi-entity refs from source
			Summary:     aiResp.Content,
			Outcome:     "success",
			Scope:       core.ScopeSharedDomain,
			Importance:  averageImportance(events),
			Timestamp:   time.Now(),
		}

		if recErr := r.episodic.RecordEvent(ctx, digestEvent); recErr != nil {
			r.logger.WarnWithContext(ctx, "Compaction: failed to store digest, skipping delete", map[string]interface{}{
				"operation": "compact",
				"entity":    entityKey,
				"error":     recErr.Error(),
			})
			continue // Don't delete originals if digest wasn't stored
		}
		digestsCreated++

		// 5. Delete original events (digest replaces them)
		var deleteIDs []string
		for _, e := range events {
			deleteIDs = append(deleteIDs, e.EventID)
		}
		if delErr := r.episodic.DeleteEvents(ctx, deleteIDs); delErr != nil {
			r.logger.WarnWithContext(ctx, "Compaction: failed to delete original events", map[string]interface{}{
				"operation":   "compact",
				"entity":      entityKey,
				"event_count": len(deleteIDs),
				"error":       delErr.Error(),
			})
		} else {
			eventsDeleted += len(deleteIDs)
		}
	}

	r.logger.InfoWithContext(ctx, "Compaction completed", map[string]interface{}{
		"operation":          "compact",
		"entities_processed": len(entityGroups),
		"digests_created":    digestsCreated,
		"events_deleted":     eventsDeleted,
		"dry_run":            config.DryRun,
	})

	return nil
}

func averageImportance(events []core.AgentEvent) float64 {
	if len(events) == 0 {
		return 5.0
	}
	sum := 0.0
	for _, e := range events {
		sum += e.Importance
	}
	return sum / float64(len(events))
}

// --- Helpers ---

func formatEventsForReflection(events []core.AgentEvent) string {
	var sb strings.Builder
	for i, e := range events {
		fmt.Fprintf(&sb, "%d. [%s] %s performed %s on %s/%s — %s (outcome: %s, importance: %.0f)\n",
			i+1,
			e.Timestamp.Format("2006-01-02 15:04"),
			e.AgentName,
			e.ActionType,
			e.EntityType,
			e.EntityID,
			e.Summary,
			e.Outcome,
			e.Importance,
		)
	}
	return sb.String()
}

func extractEventIDs(events []core.AgentEvent) []string {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		if e.EventID != "" {
			ids = append(ids, e.EventID)
		}
	}
	// Deduplicate
	sort.Strings(ids)
	deduped := ids[:0]
	for i, id := range ids {
		if i == 0 || id != ids[i-1] {
			deduped = append(deduped, id)
		}
	}
	return deduped
}
