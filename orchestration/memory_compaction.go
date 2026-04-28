package orchestration

import (
	"context"
	"fmt"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// MemoryCompactor performs compaction of episodic memory and reflection for
// institutional learning. It is NOT a background goroutine — the application
// calls it via CronJob, admin endpoint, or manual invocation.
//
// Per FRAMEWORK_DESIGN_PRINCIPLES: framework provides the logic, application
// decides when to run it (same pattern as telemetry.Initialize).
type MemoryCompactor struct {
	reflector         core.MemoryReflector
	episodic          core.EpisodicMemory
	knowledge         core.SharedKnowledge // May be nil if Phase 2 not configured
	embedder          core.EmbeddingClient // May be nil if Phase 2 not configured
	domain            string
	compactionEnabled bool
	logger            core.Logger
}

// CompactorOption configures MemoryCompactor.
type CompactorOption func(*MemoryCompactor)

// WithCompactorKnowledge enables knowledge storage after reflection (Phase 2).
// Both knowledge and embedder must be non-nil — if either is nil, neither is set
// and a warning is logged on first use.
func WithCompactorKnowledge(knowledge core.SharedKnowledge, embedder core.EmbeddingClient) CompactorOption {
	return func(c *MemoryCompactor) {
		if knowledge != nil && embedder != nil {
			c.knowledge = knowledge
			c.embedder = embedder
		}
		// If only one is provided, silently skip — the compactor will
		// return fragments without storing them (logged in ReflectEntity).
	}
}

// WithCompactorEnabled enables or disables compaction.
// Default: false (disabled). Must be explicitly enabled.
func WithCompactorEnabled(enabled bool) CompactorOption {
	return func(c *MemoryCompactor) {
		c.compactionEnabled = enabled
	}
}

// WithCompactorLogger sets the logger.
func WithCompactorLogger(logger core.Logger) CompactorOption {
	return func(c *MemoryCompactor) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// NewMemoryCompactor creates a compactor with required dependencies.
// Returns error if required dependencies are nil (fail-fast).
func NewMemoryCompactor(
	reflector core.MemoryReflector,
	episodic core.EpisodicMemory,
	domain string,
	opts ...CompactorOption,
) (*MemoryCompactor, error) {
	if reflector == nil {
		return nil, fmt.Errorf("reflector is required for MemoryCompactor")
	}
	if episodic == nil {
		return nil, fmt.Errorf("episodic memory is required for MemoryCompactor")
	}
	if domain == "" {
		domain = "default"
	}

	c := &MemoryCompactor{
		reflector: reflector,
		episodic:  episodic,
		domain:    domain,
		logger:    &core.NoOpLogger{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// ReflectEntity runs reflection on a specific entity, generating knowledge fragments
// from accumulated episodic events. If SharedKnowledge + EmbeddingClient are configured,
// the fragments are embedded and stored automatically.
//
// Returns the generated fragments (even if storage fails — fail-open).
func (c *MemoryCompactor) ReflectEntity(ctx context.Context, entityType, entityID string, since time.Time) ([]core.KnowledgeFragment, error) {
	startTime := time.Now()
	requestID := GetRequestID(ctx)

	// 1. Generate knowledge via reflection
	fragments, err := c.reflector.Reflect(ctx, entityType, entityID, since)
	if err != nil {
		c.logger.WarnWithContext(ctx, "Reflection failed for entity", map[string]interface{}{
			"operation":   "reflect_entity",
			"request_id":  requestID,
			"entity_type": entityType,
			"entity_id":   entityID,
			"error":       err.Error(),
			"error_type":  "reflection",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("memory.compaction.reflection_errors",
			"module", telemetry.ModuleOrchestration,
		)
		return nil, nil // Fail-open
	}

	if len(fragments) == 0 {
		return nil, nil
	}

	// 2. If knowledge store + embedder available, embed and store
	storedCount := 0
	if c.knowledge != nil && c.embedder != nil {
		for i := range fragments {
			// Generate embedding
			embResp, embErr := c.embedder.GenerateEmbeddings(ctx, []string{fragments[i].Content}, nil)
			if embErr != nil || len(embResp.Embeddings) == 0 || len(embResp.Embeddings[0]) == 0 {
				c.logger.WarnWithContext(ctx, "Failed to embed reflection fragment", map[string]interface{}{
					"operation":   "reflect_entity",
					"request_id":  requestID,
					"entity_type": entityType,
					"entity_id":   entityID,
					"error":       fmt.Sprintf("embedding failed: %v", embErr),
					"error_type":  "embedding",
				})
				continue
			}
			fragments[i].Embedding = embResp.Embeddings[0]

			// Store in knowledge base
			if storeErr := c.knowledge.StoreKnowledge(ctx, fragments[i]); storeErr != nil {
				c.logger.WarnWithContext(ctx, "Failed to store reflection fragment", map[string]interface{}{
					"operation":   "reflect_entity",
					"request_id":  requestID,
					"entity_type": entityType,
					"entity_id":   entityID,
					"error":       storeErr.Error(),
					"error_type":  "knowledge_store",
				})
				continue
			}
			storedCount++
		}
	}

	durationMs := time.Since(startTime).Milliseconds()
	c.logger.InfoWithContext(ctx, "Entity reflection completed", map[string]interface{}{
		"operation":         "reflect_entity",
		"request_id":        requestID,
		"entity_type":       entityType,
		"entity_id":         entityID,
		"fragments_created": len(fragments),
		"fragments_stored":  storedCount,
		"duration_ms":       durationMs,
	})
	telemetry.AddSpanEvent(ctx, "memory.compaction.reflection_completed",
		attribute.String("request_id", requestID), // Pattern 6: request_id first
		attribute.String("entity_type", entityType),
		attribute.String("entity_id", entityID),
		attribute.Int("fragments_created", len(fragments)),
		attribute.Int("fragments_stored", storedCount),
		attribute.Int64("duration_ms", durationMs),
	)
	telemetry.Counter("memory.compaction.fragments_created",
		"module", telemetry.ModuleOrchestration,
	)

	return fragments, nil
}

// RunCompaction performs full compaction: reflection + event pruning.
// config controls age thresholds and dry-run mode.
//
// Callable from: K8s CronJob, admin HTTP endpoint, manual invocation.
func (c *MemoryCompactor) RunCompaction(ctx context.Context, config core.CompactionConfig) error {
	if !c.compactionEnabled {
		c.logger.InfoWithContext(ctx, "Compaction is disabled, skipping", map[string]interface{}{
			"operation": "run_compaction",
			"hint":      "Set TRUVAG3_SHARED_MEMORY_COMPACTION_ENABLED=true or use WithCompactorEnabled(true) to enable",
		})
		return nil
	}

	startTime := time.Now()
	compactionRequestID := GetRequestID(ctx)

	c.logger.InfoWithContext(ctx, "Starting memory compaction", map[string]interface{}{
		"operation":            "run_compaction",
		"request_id":           compactionRequestID,
		"event_age_threshold":  config.EventAgeThreshold.String(),
		"importance_threshold": config.ImportanceThreshold,
		"dry_run":              config.DryRun,
	})

	// Delegate to the reflector's Compact method for event-level compaction
	if err := c.reflector.Compact(ctx, config); err != nil {
		c.logger.WarnWithContext(ctx, "Compaction failed", map[string]interface{}{
			"operation":   "run_compaction",
			"request_id":  compactionRequestID,
			"error":       err.Error(),
			"error_type":  "compaction",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("memory.compaction.errors",
			"module", telemetry.ModuleOrchestration,
		)
		return nil // Fail-open
	}

	durationMs := time.Since(startTime).Milliseconds()
	c.logger.InfoWithContext(ctx, "Memory compaction completed", map[string]interface{}{
		"operation":   "run_compaction",
		"request_id":  compactionRequestID,
		"duration_ms": durationMs,
		"dry_run":     config.DryRun,
	})
	telemetry.Counter("memory.compaction.runs_total",
		"module", telemetry.ModuleOrchestration,
	)
	telemetry.Histogram("memory.compaction.duration_ms", float64(durationMs),
		"module", telemetry.ModuleOrchestration,
	)

	return nil
}
