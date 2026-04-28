package memory

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Compile-time interface check.
var _ core.Runnable = (*ReflectionJob)(nil)

// ReflectionJob runs periodic knowledge extraction from episodic events.
// It bridges Tier 2 (episodic events) to Tier 3 (semantic knowledge) by
// discovering entities with accumulated events, reflecting via LLM, and
// storing the resulting knowledge fragments in the vector DB.
//
// Implements core.Runnable — lifecycle is managed by the framework via
// RegisterRunnable + Run(ctx). The job runs until ctx is cancelled.
type ReflectionJob struct {
	reflector core.MemoryReflector
	episodic  core.EpisodicMemory
	knowledge core.SharedKnowledge
	embedder  core.EmbeddingClient
	lock      core.DistributedLock // optional — nil = no locking (single-replica)
	telemetry core.Telemetry       // optional — for span creation
	domain    string
	logger    core.Logger

	// Configuration
	interval     time.Duration // How often to run (default: 24h)
	ageThreshold time.Duration // Reflect events older than this (default: 7 days)
	minEvents    int           // Minimum events per entity (default: 5)
}

// ReflectionJobOption configures ReflectionJob with behavioural plugs only.
//
// Per the Configuration Split Principle (UNIFIED_AGENT_MEMORY_IMPL_PLAN_REFACTOR §2):
// numeric tuning is via env vars (TRUVAG3_REFLECTION_*), behavioural plugs are options.
// "If it's a number → env var. If it's an interface → option."
//
// Numeric tuning (interval, age threshold, min events) is read inside NewReflectionJob
// from TRUVAG3_REFLECTION_INTERVAL, TRUVAG3_REFLECTION_AGE_THRESHOLD, TRUVAG3_REFLECTION_MIN_EVENTS —
// no With* options for these.
type ReflectionJobOption func(*ReflectionJob) error

// WithReflectionLock sets the distributed lock for multi-replica safety.
// When set, only one replica runs reflection at a time. Others skip.
// When nil (default), no locking — suitable for single-replica deployments.
func WithReflectionLock(lock core.DistributedLock) ReflectionJobOption {
	return func(j *ReflectionJob) error {
		j.lock = lock // nil is valid — disables locking
		return nil
	}
}

// WithReflectionTelemetry sets the telemetry provider for span creation.
// When set, RunOnce creates a root span grouping all LLM and embedding calls.
func WithReflectionTelemetry(t core.Telemetry) ReflectionJobOption {
	return func(j *ReflectionJob) error {
		j.telemetry = t // nil is valid — disables span creation
		return nil
	}
}

// NewReflectionJob creates a reflection job.
// All dependencies are core interfaces — no module-specific imports.
//
// Configuration precedence (per FRAMEWORK_DESIGN_PRINCIPLES §3):
//  1. Explicit WithXXX() options (highest)
//  2. TRUVAG3_REFLECTION_* env vars
//  3. Sensible defaults
func NewReflectionJob(
	reflector core.MemoryReflector,
	episodic core.EpisodicMemory,
	knowledge core.SharedKnowledge,
	embedder core.EmbeddingClient,
	domain string,
	logger core.Logger,
	opts ...ReflectionJobOption,
) (*ReflectionJob, error) {
	if reflector == nil {
		return nil, fmt.Errorf("reflector is required")
	}
	if episodic == nil {
		return nil, fmt.Errorf("episodic memory is required")
	}
	if knowledge == nil {
		return nil, fmt.Errorf("shared knowledge is required")
	}
	if embedder == nil {
		return nil, fmt.Errorf("embedding client is required")
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/memory")
	}
	if domain == "" {
		domain = os.Getenv("TRUVAG3_AGENT_DOMAIN")
		if domain == "" {
			domain = "default"
		}
	}

	j := &ReflectionJob{
		reflector:    reflector,
		episodic:     episodic,
		knowledge:    knowledge,
		embedder:     embedder,
		domain:       domain,
		logger:       logger,
		interval:     24 * time.Hour,
		ageThreshold: 7 * 24 * time.Hour,
		minEvents:    5,
	}

	// Env var overrides
	if v := os.Getenv("TRUVAG3_REFLECTION_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			j.interval = d
		}
	}
	if v := os.Getenv("TRUVAG3_REFLECTION_AGE_THRESHOLD"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			j.ageThreshold = d
		}
	}
	if v := os.Getenv("TRUVAG3_REFLECTION_MIN_EVENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			j.minEvents = n
		}
	}

	// Explicit options (highest priority)
	for _, opt := range opts {
		if err := opt(j); err != nil {
			return nil, fmt.Errorf("invalid reflection job option: %w", err)
		}
	}

	return j, nil
}

// Start runs the reflection job until ctx is cancelled.
// Implements core.Runnable — blocks until shutdown, returns error on failure.
//
// Must be called exactly once. The framework calls this when Run(ctx) is invoked,
// after RegisterRunnable() has registered the job.
//
// Schedule:
//   - T=0:               immediate initial pass (so pod restarts don't gap
//     observability for a full INTERVAL)
//   - T=INTERVAL × N:    subsequent passes on the ticker
//
// The initial pass is what most schedulers (Kubernetes CronJobs with
// startingDeadlineSeconds=0, controller-runtime managers, Prometheus
// scrapes) do — a fresh restart should immediately exercise the job
// rather than wait an entire interval. Without this, every redeploy
// of an agent that uses reflection blackholes the next INTERVAL hours
// of reflection observability, which makes validation after a deploy
// essentially impossible at production cadences (24h, 6h).
//
// Safety: the initial pass runs in the same goroutine as the ticker loop,
// so it cannot race with itself. The distributed lock (when configured)
// still protects against multi-replica overlap on T=0 just as it does
// on every subsequent tick.
func (j *ReflectionJob) Start(ctx context.Context) error {
	if j.logger != nil {
		j.logger.Info("Reflection job started", map[string]interface{}{
			"operation":     "reflection_job",
			"interval":      j.interval.String(),
			"age_threshold": j.ageThreshold.String(),
			"min_events":    j.minEvents,
		})
	}

	// Run an initial pass on startup so pod restarts don't gap reflection
	// observability for a full INTERVAL. RunOnce is fail-open and logs its
	// own errors — discarding the return value is intentional. If ctx is
	// already cancelled (immediate shutdown), the pass returns quickly via
	// the lock-acquire ctx check or the per-entity LLM ctx check.
	_ = j.RunOnce(ctx)

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// RunOnce is fail-open and logs its own errors — discarding the
			// return value is intentional. The next tick will retry.
			_ = j.RunOnce(ctx)
		case <-ctx.Done():
			if j.logger != nil {
				j.logger.Info("Reflection job stopping (context cancelled)", map[string]interface{}{
					"operation": "reflection_job",
				})
			}
			return nil
		}
	}
}

// RunOnce executes a single reflection pass. Safe to call from admin endpoints.
//
// IMPORTANT: This calls Reflect() (knowledge extraction → Qdrant), NOT Compact().
// Compact() creates digest events in episodic memory and DELETES originals — that's
// a Tier 2 maintenance operation, not Tier 2→Tier 3 bridging.
// Reflect() extracts patterns into KnowledgeFragments for Qdrant — that's the bridge.
//
// Sequencing matters: if Compact() ran first, it would delete the events that
// Reflect() needs to read. We only call Reflect().
// Events expire naturally via 60-day TTL — no active deletion needed.
func (j *ReflectionJob) RunOnce(ctx context.Context) error {
	startTime := time.Now()
	since := time.Now().Add(-j.ageThreshold)

	// Generate pass_id for correlation (Pattern 3 — request_id equivalent for background jobs).
	// Also set it as the canonical request_id baggage so the InstrumentedAIClient records every
	// reflection LLM call to the LLM debug store under this pass_id — making background reflection
	// passes visible alongside user requests in the registry viewer's LLM Debug screen.
	//
	// Entropy: 48 bits (12 hex chars from the first 6 bytes of a UUID v4). This is the
	// pass_id's only defense against LLM-debug-store key collision — two passes with
	// the same id would silently overwrite each other's interaction records in Redis.
	// 48 bits gives a birthday-collision half-life of ~16 million passes, or roughly
	// 11,000 years at the default 24h cadence and ~2,700 years at the 6h cadence used
	// by the devops-chat-agent. Widening from the historical 8-char / 32-bit format
	// (~45 years at 6h cadence) preserves the same visual shape in logs while making
	// collision concerns disappear under any reasonable operational cadence or
	// multi-replica fanout.
	u := uuid.New()
	passID := fmt.Sprintf("reflect-%x", u[:6])
	ctx = telemetry.WithBaggage(ctx, "pass_id", passID, "request_id", passID)
	ctx = core.WithRequestID(ctx, passID)

	// Root span for the entire pass — groups LLM + embedding calls under one trace
	var span core.Span
	if j.telemetry != nil {
		ctx, span = j.telemetry.StartSpan(ctx, "memory.reflection_pass")
		defer span.End()
		span.SetAttribute("pass_id", passID)
		span.SetAttribute("domain", j.domain)
		span.SetAttribute("age_threshold", j.ageThreshold.String())
	}

	// Acquire distributed lock if configured (multi-replica safety)
	if j.lock != nil {
		acquired, err := j.lock.Acquire(ctx, "reflection:"+j.domain, j.interval)
		if err != nil {
			telemetry.RecordSpanError(ctx, err)
			if j.logger != nil {
				j.logger.WarnWithContext(ctx, "Reflection lock acquisition failed, skipping pass", map[string]interface{}{
					"operation":  "reflection_pass",
					"pass_id":    passID,
					"error":      err.Error(),
					"error_type": "lock_acquire",
				})
			}
			return nil // fail-open
		}
		if !acquired {
			if j.logger != nil {
				j.logger.InfoWithContext(ctx, "Reflection lock held by another replica, skipping", map[string]interface{}{
					"operation": "reflection_pass",
					"pass_id":   passID,
				})
			}
			return nil
		}
		defer func() {
			if err := j.lock.Release(ctx, "reflection:"+j.domain); err != nil && j.logger != nil {
				j.logger.WarnWithContext(ctx, "Failed to release reflection lock", map[string]interface{}{
					"operation": "reflection_pass",
					"pass_id":   passID,
					"error":     err.Error(),
				})
			}
		}()
	}

	if j.logger != nil {
		j.logger.InfoWithContext(ctx, "Reflection pass starting", map[string]interface{}{
			"operation":     "reflection_pass",
			"pass_id":       passID,
			"age_threshold": j.ageThreshold.String(),
			"since":         since.Format(time.RFC3339),
		})
	}

	// 1. Discover entities with enough old events to warrant reflection
	entities, err := j.discoverEntities(ctx, since)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if j.logger != nil {
			j.logger.WarnWithContext(ctx, "Reflection pass: entity discovery failed", map[string]interface{}{
				"operation":  "reflection_pass",
				"pass_id":    passID,
				"error":      err.Error(),
				"error_type": "entity_discovery",
			})
		}
		return err
	}

	if len(entities) == 0 {
		if j.logger != nil {
			j.logger.InfoWithContext(ctx, "Reflection pass: no entities with enough old events", map[string]interface{}{
				"operation":     "reflection_pass",
				"pass_id":       passID,
				"age_threshold": j.ageThreshold.String(),
				"min_events":    j.minEvents,
			})
		}
		return nil
	}

	// 2. For each entity, reflect (extract knowledge) then embed and store
	//    Reflect() takes `since` = how far back to query events for this entity.
	//    We use the full event TTL window (60 days) to capture the complete picture,
	//    not just the ageThreshold. discoverEntities already filtered for entities
	//    with enough OLD events — Reflect reads their full history for pattern extraction.
	//    60 days matches the default eventTTL in StreamEpisodicMemory.
	reflectSince := time.Now().Add(-60 * 24 * time.Hour)
	fragmentsStored := 0
	entitiesProcessed := 0
	embeddingTokens := 0
	for _, entity := range entities {
		// Reflect: LLM extracts 1-3 knowledge fragments from entity's events
		// Returns []KnowledgeFragment with Embedding=nil
		fragments, reflectErr := j.reflector.Reflect(ctx, entity.Type, entity.ID, reflectSince)
		if reflectErr != nil || len(fragments) == 0 {
			continue
		}
		entitiesProcessed++

		telemetry.AddSpanEvent(ctx, "memory.reflection.entity_processed",
			attribute.String("pass_id", passID),
			attribute.String("entity_type", entity.Type),
			attribute.String("entity_id", entity.ID),
			attribute.Int("fragments", len(fragments)),
		)

		// Embed and store each fragment
		for _, f := range fragments {
			if f.Content == "" {
				continue
			}

			embResp, embErr := j.embedder.GenerateEmbeddings(ctx, []string{f.Content}, nil)
			if embErr != nil || len(embResp.Embeddings) == 0 || len(embResp.Embeddings[0]) == 0 {
				if embErr != nil {
					telemetry.RecordSpanError(ctx, embErr)
				}
				if j.logger != nil {
					j.logger.WarnWithContext(ctx, "Reflection: failed to embed fragment", map[string]interface{}{
						"operation":  "reflection_pass",
						"pass_id":    passID,
						"entity":     entity.Type + "/" + entity.ID,
						"error_type": "embedding",
					})
				}
				continue
			}

			// Record embedding token usage for billing attribution
			core.RecordTokenUsage(ctx, "reflection_embedding", embResp.Usage)
			embeddingTokens += embResp.Usage.TotalTokens

			f.Embedding = embResp.Embeddings[0]
			if f.AgentDomain == "" {
				f.AgentDomain = j.domain
			}
			if f.Scope == "" {
				f.Scope = core.ScopeSharedDomain
			}
			if f.CreatedAt.IsZero() {
				f.CreatedAt = time.Now()
			}

			if storeErr := j.knowledge.StoreKnowledge(ctx, f); storeErr != nil {
				telemetry.RecordSpanError(ctx, storeErr)
				if j.logger != nil {
					j.logger.WarnWithContext(ctx, "Reflection: failed to store fragment", map[string]interface{}{
						"operation":  "reflection_pass",
						"pass_id":    passID,
						"entity":     entity.Type + "/" + entity.ID,
						"error":      storeErr.Error(),
						"error_type": "knowledge_store",
					})
				}
				continue
			}
			fragmentsStored++
		}
	}

	// Counters for Prometheus dashboards
	telemetry.Counter("memory.reflection.pass", "module", telemetry.ModuleMemory)
	telemetry.Counter("memory.reflection.fragments_stored",
		"module", telemetry.ModuleMemory, "count", fmt.Sprintf("%d", fragmentsStored))

	if j.logger != nil {
		j.logger.InfoWithContext(ctx, "Reflection pass completed", map[string]interface{}{
			"operation":           "reflection_pass",
			"pass_id":             passID,
			"entities_discovered": len(entities),
			"entities_processed":  entitiesProcessed,
			"fragments_stored":    fragmentsStored,
			"embedding_tokens":    embeddingTokens,
			"duration_ms":         time.Since(startTime).Milliseconds(),
		})
	}

	return nil
}

// (Stop method removed — context cancellation drives shutdown via core.Runnable pattern)

// discoverEntities finds entities with events older than the age threshold.
func (j *ReflectionJob) discoverEntities(ctx context.Context, since time.Time) ([]core.EntityRef, error) {
	oldEvents, err := j.episodic.QueryEvents(ctx, j.domain, core.EventFilter{
		Until: since, // Events older than the age threshold
		Limit: 500,
	})
	if err != nil {
		return nil, err
	}

	entityCounts := make(map[string]int)
	entityRefs := make(map[string]core.EntityRef)
	for _, e := range oldEvents {
		if e.ActionType == "digest" {
			continue
		}
		if len(e.Entities) > 0 {
			for _, entity := range e.Entities {
				key := entity.Type + ":" + entity.ID
				entityCounts[key]++
				entityRefs[key] = entity
			}
		} else if e.EntityType != "" && e.EntityID != "" {
			key := e.EntityType + ":" + e.EntityID
			entityCounts[key]++
			entityRefs[key] = core.EntityRef{Type: e.EntityType, ID: e.EntityID}
		}
	}

	var result []core.EntityRef
	for key, count := range entityCounts {
		if count >= j.minEvents {
			result = append(result, entityRefs[key])
		}
	}
	return result, nil
}
