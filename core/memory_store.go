package core

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is an in-memory implementation of the Memory interface
type MemoryStore struct {
	mu     sync.RWMutex
	store  map[string]memoryEntry
	logger Logger
}

type memoryEntry struct {
	value     string
	expiresAt time.Time
}

// NewMemoryStore creates a new in-memory store
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		store:  make(map[string]memoryEntry),
		logger: &NoOpLogger{},
	}
}

// SetLogger configures the logger for this memory store
// The logger is wrapped with component "framework/core" to identify logs from this module
func (m *MemoryStore) SetLogger(logger Logger) {
	if logger != nil {
		if cal, ok := logger.(ComponentAwareLogger); ok {
			m.logger = cal.WithComponent("framework/core")
		} else {
			m.logger = logger
		}
	} else {
		m.logger = nil
	}
}

// Get retrieves a value from memory
func (m *MemoryStore) Get(ctx context.Context, key string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.logger != nil {
		m.logger.DebugWithContext(ctx, "Cache lookup", map[string]interface{}{
			"operation": "cache_get",
			"key":       key,
		})
	}

	entry, exists := m.store[key]
	if !exists {
		// Emit framework metrics for cache miss
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("memory.cache.misses", "memory_type", "in_memory")
			registry.Counter("memory.operations", "operation", "get", "memory_type", "in_memory", "result", "miss")
		}

		if m.logger != nil {
			m.logger.DebugWithContext(ctx, "Cache miss", map[string]interface{}{
				"operation": "cache_get",
				"key":       key,
				"result":    "miss",
			})
		}
		return "", nil
	}

	// Check if expired
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		// Emit framework metrics for expired entry (treated as miss)
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("memory.cache.misses", "memory_type", "in_memory")
			registry.Counter("memory.evictions", "memory_type", "in_memory", "reason", "expired")
		}

		if m.logger != nil {
			m.logger.DebugWithContext(ctx, "Cache entry expired", map[string]interface{}{
				"operation":  "cache_get",
				"key":        key,
				"result":     "expired",
				"expired_at": entry.expiresAt.Format(time.RFC3339),
			})
		}
		return "", nil
	}

	// Emit framework metrics for cache hit
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("memory.cache.hits", "memory_type", "in_memory")
		registry.Counter("memory.operations", "operation", "get", "memory_type", "in_memory", "result", "hit")
	}

	if m.logger != nil {
		m.logger.DebugWithContext(ctx, "Cache hit", map[string]interface{}{
			"operation": "cache_get",
			"key":       key,
			"result":    "hit",
		})
	}

	return entry.value, nil
}

// Set stores a value in memory with optional TTL
func (m *MemoryStore) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.logger != nil {
		logFields := map[string]interface{}{
			"operation":  "cache_set",
			"key":        key,
			"value_size": len(value),
			"has_ttl":    ttl > 0,
		}
		if ttl > 0 {
			logFields["ttl"] = ttl.String()
			logFields["expires_at"] = time.Now().Add(ttl).Format(time.RFC3339)
		}
		m.logger.DebugWithContext(ctx, "Cache set", logFields)
	}

	entry := memoryEntry{
		value: value,
	}

	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}

	m.store[key] = entry

	// Emit framework metrics for cache set
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("memory.operations", "operation", "set", "memory_type", "in_memory", "result", "success")
		registry.Gauge("memory.size_bytes", float64(len(value)), "memory_type", "in_memory")
	}

	return nil
}

// Delete removes a value from memory
func (m *MemoryStore) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, existed := m.store[key]
	delete(m.store, key)

	// Emit framework metrics for cache delete
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("memory.operations", "operation", "delete", "memory_type", "in_memory")
		if existed {
			registry.Counter("memory.evictions", "memory_type", "in_memory", "reason", "explicit_delete")
		}
	}

	if m.logger != nil {
		m.logger.DebugWithContext(ctx, "Cache delete", map[string]interface{}{
			"operation": "cache_delete",
			"key":       key,
			"existed":   existed,
		})
	}

	return nil
}

// Exists checks if a key exists in memory
func (m *MemoryStore) Exists(ctx context.Context, key string) (bool, error) {
	if m.logger != nil {
		m.logger.DebugWithContext(ctx, "Cache existence check", map[string]interface{}{
			"operation": "cache_exists",
			"key":       key,
		})
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.store[key]
	if !exists {
		if m.logger != nil {
			m.logger.DebugWithContext(ctx, "Cache existence result", map[string]interface{}{
				"operation": "cache_exists",
				"key":       key,
				"result":    "not_found",
				"exists":    false,
			})
		}
		return false, nil
	}

	// Check if expired
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		if m.logger != nil {
			m.logger.DebugWithContext(ctx, "Cache existence result", map[string]interface{}{
				"operation":  "cache_exists",
				"key":        key,
				"result":     "expired",
				"exists":     false,
				"expired_at": entry.expiresAt.Format(time.RFC3339),
			})
		}
		return false, nil
	}

	if m.logger != nil {
		m.logger.DebugWithContext(ctx, "Cache existence result", map[string]interface{}{
			"operation": "cache_exists",
			"key":       key,
			"result":    "found",
			"exists":    true,
		})
	}

	return true, nil
}

// Store is an alias for Set for backward compatibility
func (m *MemoryStore) Store(ctx context.Context, key string, value interface{}) error {
	// Convert value to string
	var strValue string
	switch v := value.(type) {
	case string:
		strValue = v
	default:
		strValue = ""
	}
	return m.Set(ctx, key, strValue, 0)
}

// Retrieve is an alias for Get for backward compatibility
func (m *MemoryStore) Retrieve(ctx context.Context, key string) (interface{}, error) {
	return m.Get(ctx, key)
}

// Compile-time interface check (matches memory.ReflectionJob pattern).
var _ Runnable = (*MemoryStoreSweeper)(nil)

// MemoryStoreSweeper is a core.Runnable that periodically deletes expired
// entries from a MemoryStore. Lifecycle is managed by Framework via ctx
// cancellation — no Stop()/stopCh per FRAMEWORK_DESIGN_PRINCIPLES.md
// §"Background Jobs Implement core.Runnable".
//
// Observability follows docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md and
// docs/observability/DISTRIBUTED_TRACING_GUIDE.md (§15 Background-Job Spans). The reference
// in-tree pattern is memory.ReflectionJob.
//
// The struct is exported so MemoryStoreSweeperOption can carry an exported
// signature (mirrors ReflectionJobOption + *ReflectionJob). External callers
// don't need to manipulate the struct directly — register via Runnable.
type MemoryStoreSweeper struct {
	store     *MemoryStore
	interval  time.Duration
	logger    Logger    // required; component-wrapped to "framework/core"
	telemetry Telemetry // optional; when set, creates memory.sweep_pass spans
}

// MemoryStoreSweeperOption configures a MemoryStoreSweeper with behavioural
// plugs (mirrors ReflectionJobOption). Numeric tuning (interval) is a
// constructor parameter, not an option, per FRAMEWORK_DESIGN_PRINCIPLES.md
// §"Configuration Split".
type MemoryStoreSweeperOption func(*MemoryStoreSweeper) error

// WithMemoryStoreSweeperTelemetry sets the telemetry provider for span
// creation. When set, each sweep tick creates a root memory.sweep_pass span
// grouping the deletion pass under one trace tree (per
// docs/observability/DISTRIBUTED_TRACING_GUIDE.md §15: background jobs detached from a user
// request must create their own root spans). When nil (default), no spans
// are created — metrics + logs only.
func WithMemoryStoreSweeperTelemetry(t Telemetry) MemoryStoreSweeperOption {
	return func(s *MemoryStoreSweeper) error {
		s.telemetry = t
		return nil
	}
}

// NewMemoryStoreSweeper returns a *MemoryStoreSweeper (which implements
// core.Runnable) that periodically deletes expired entries from the given
// MemoryStore. interval <= 0 makes the sweeper a no-op (Start returns nil on
// ctx-cancel). logger is required (use &core.NoOpLogger{} if you want silence).
//
// The concrete return type matches memory.NewReflectionJob — callers pass it
// to Framework.RegisterRunnable, which accepts the Runnable interface.
//
// Register the returned Runnable with Framework.RegisterRunnable before
// calling Framework.Run. Memory is bounded at (active entries) + (entries
// that expired since the last sweep tick). For tighter bounds, also add
// delete-on-expiry-read in Get/Exists — separate optional optimization.
//
// The logger is automatically wrapped via ComponentAwareLogger to
// "framework/core" per docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md §14.
//
// Example (agent):
//
//	framework, _ := core.NewFramework(agent, opts...)
//	framework.AutoRegisterMemorySweeper() // passes agent.Logger automatically
//	framework.Run(ctx)
//
// Example (tool, with optional spans):
//
//	tool := NewMyTool() // tool.cache is a *core.MemoryStore field
//	framework, _ := core.NewFramework(tool, opts...)
//	sweeper, err := core.NewMemoryStoreSweeper(
//	    tool.cache, 10*time.Minute, tool.Logger,
//	    core.WithMemoryStoreSweeperTelemetry(tool.Telemetry),
//	)
//	if err != nil {
//	    log.Fatalf("memory sweeper init failed: %v", err)
//	}
//	framework.RegisterRunnable(sweeper)
//	framework.Run(ctx)
func NewMemoryStoreSweeper(
	store *MemoryStore,
	interval time.Duration,
	logger Logger,
	opts ...MemoryStoreSweeperOption,
) (*MemoryStoreSweeper, error) {
	if logger == nil {
		logger = &NoOpLogger{}
	}
	if cal, ok := logger.(ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/core")
	}
	s := &MemoryStoreSweeper{
		store:    store,
		interval: interval,
		logger:   logger,
	}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, fmt.Errorf("invalid memory store sweeper option: %w", err)
		}
	}
	return s, nil
}

// Start runs the sweeper until ctx is cancelled. Implements core.Runnable.
// Returns nil on graceful shutdown.
//
// Observability:
//   - Lifecycle logs (Info) on start and stop, with operation="memory_sweeper".
//   - Per-pass log (InfoWithContext) with operation="memory_sweep_pass",
//     sweep_id, deleted_count, duration_ms — only when deletions occurred.
//     Debug-level "no expirations" log otherwise.
//   - Optional memory.sweep_pass span per tick (when WithMemoryStoreSweeperTelemetry
//     is set), with sweep_id, interval, deleted_count attributes.
//   - sweep_id is set as request_id via core.WithRequestID so log correlation
//     and Jaeger filtering work the same way as for user requests
//     (docs/observability/DISTRIBUTED_TRACING_GUIDE.md §5 Trace-Log Correlation).
//   - On unexpected exit (ctx not cancelled), emits the
//     memory.sweeper.unexpected_exits counter and a Warn log with
//     error_type="runnable_exit" (per docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md
//     §10 standard error_type enum).
func (s *MemoryStoreSweeper) Start(ctx context.Context) error {
	defer func() {
		if ctx.Err() == nil {
			if registry := GetGlobalMetricsRegistry(); registry != nil {
				registry.Counter("memory.sweeper.unexpected_exits",
					"memory_type", "in_memory",
				)
			}
			if s.logger != nil {
				s.logger.Warn("Memory sweeper exited unexpectedly", map[string]interface{}{
					"operation":  "memory_sweeper",
					"error_type": "runnable_exit",
				})
			}
		}
	}()

	if s.logger != nil {
		s.logger.Info("Memory sweeper started", map[string]interface{}{
			"operation": "memory_sweeper",
			"interval":  s.interval.String(),
			"active":    s.store != nil && s.interval > 0,
		})
	}

	if s.store == nil || s.interval <= 0 {
		// No-op runnable. Block on ctx so the framework's drain logic sees
		// the same lifecycle for active and inactive sweepers.
		<-ctx.Done()
		if s.logger != nil {
			s.logger.Info("Memory sweeper stopping (no-op, context cancelled)", map[string]interface{}{
				"operation": "memory_sweeper",
			})
		}
		return nil
	}

	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if s.logger != nil {
				s.logger.Info("Memory sweeper stopping (context cancelled)", map[string]interface{}{
					"operation": "memory_sweeper",
				})
			}
			return nil
		case <-t.C:
			s.runSweepPass(ctx)
		}
	}
}

// runSweepPass executes one sweep iteration. Extracted so the per-pass
// span/log/correlation setup is co-located with the deletion logic.
func (s *MemoryStoreSweeper) runSweepPass(ctx context.Context) {
	// Generate sweep_id for log + trace correlation. 48-bit entropy mirrors
	// the reflection-job pass_id pattern (memory/reflection_job.go).
	u := uuid.New()
	sweepID := fmt.Sprintf("sweep-%x", u[:6])
	ctx = WithRequestID(ctx, sweepID)

	// Optional root span for the pass (tracing guide §15: background jobs
	// create their own root spans, detached from any user request).
	var span Span
	if s.telemetry != nil {
		ctx, span = s.telemetry.StartSpan(ctx, "memory.sweep_pass")
		defer span.End()
		span.SetAttribute("sweep_id", sweepID)
		span.SetAttribute("interval", s.interval.String())
	}

	startTime := time.Now()
	now := startTime
	deleted := 0
	// NOTE: Single-pass write-lock for the full iteration. For the cache sizes
	// MemoryStore is intended to back (per-agent scratch space, ~10³ keys), the
	// pause is sub-millisecond and simpler than a two-phase scan/delete. If a
	// future workload pushes this much higher, switch to: snapshot expired keys
	// under RLock, then delete each under a short Lock.
	s.store.mu.Lock()
	for k, e := range s.store.store {
		if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
			delete(s.store.store, k)
			deleted++
		}
	}
	s.store.mu.Unlock()
	durationMs := time.Since(startTime).Milliseconds()

	if span != nil {
		span.SetAttribute("deleted_count", deleted)
		span.SetAttribute("duration_ms", durationMs)
	}

	if deleted > 0 {
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("memory.evictions",
				"memory_type", "in_memory",
				"reason", "sweeper",
			)
		}
		if s.logger != nil {
			s.logger.InfoWithContext(ctx, "Memory sweep pass completed", map[string]interface{}{
				"operation":     "memory_sweep_pass",
				"sweep_id":      sweepID,
				"deleted_count": deleted,
				"duration_ms":   durationMs,
				"status":        "success",
			})
		}
	} else if s.logger != nil {
		s.logger.DebugWithContext(ctx, "Memory sweep pass completed (no expirations)", map[string]interface{}{
			"operation":     "memory_sweep_pass",
			"sweep_id":      sweepID,
			"deleted_count": 0,
			"duration_ms":   durationMs,
		})
	}
}
