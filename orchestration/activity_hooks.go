package orchestration

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// ActivityFilter controls which signals reach the LLM's planning prompt.
// Default: RecentActivityFilter (most recent N, exclude self).
// Developers implement custom filters for domain-specific needs.
type ActivityFilter interface {
	Filter(ctx context.Context, ownRequestID string, signals []core.ActivitySignal) []core.ActivitySignal
}

// RecentActivityFilter is the default — most recent N signals, exclude self.
// No domain-specific assumptions. The LLM judges relevance.
type RecentActivityFilter struct {
	MaxSignals int
}

func (f *RecentActivityFilter) Filter(ctx context.Context, ownRequestID string, signals []core.ActivitySignal) []core.ActivitySignal {
	var filtered []core.ActivitySignal
	for _, s := range signals {
		if s.RequestID == ownRequestID {
			continue
		}
		if s.Status == "completed" {
			continue
		}
		filtered = append(filtered, s)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].StartedAt.After(filtered[j].StartedAt)
	})
	if len(filtered) > f.MaxSignals {
		filtered = filtered[:f.MaxSignals]
	}
	return filtered
}

// --- ActivityAnnouncementHook ---

// ActivityAnnouncementHook announces what the agent is working on and discovers
// other agents' activity signals for coordination. Runs in BeforePlanning.
type ActivityAnnouncementHook struct {
	coordinator core.ActivityCoordinator
	filter      ActivityFilter
	agentName   string
	agentDomain string
	signalTTL   time.Duration
	queryMaxLen int
	logger      core.Logger
}

// ActivityAnnouncementOption configures ActivityAnnouncementHook.
type ActivityAnnouncementOption func(*ActivityAnnouncementHook) error

// WithAnnouncementFilter sets a custom activity filter. Default: RecentActivityFilter.
func WithAnnouncementFilter(f ActivityFilter) ActivityAnnouncementOption {
	return func(h *ActivityAnnouncementHook) error {
		if f == nil {
			return fmt.Errorf("activity filter cannot be nil")
		}
		h.filter = f
		return nil
	}
}

// WithAnnouncementLogger sets the logger.
func WithAnnouncementLogger(logger core.Logger) ActivityAnnouncementOption {
	return func(h *ActivityAnnouncementHook) error {
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

// WithAnnouncementSignalTTL overrides the signal TTL. Default: 5m.
func WithAnnouncementSignalTTL(ttl time.Duration) ActivityAnnouncementOption {
	return func(h *ActivityAnnouncementHook) error {
		if ttl <= 0 {
			return fmt.Errorf("signal TTL must be positive, got %v", ttl)
		}
		h.signalTTL = ttl
		return nil
	}
}

// WithAnnouncementQueryMaxLen overrides the query truncation limit. Default: 200.
func WithAnnouncementQueryMaxLen(n int) ActivityAnnouncementOption {
	return func(h *ActivityAnnouncementHook) error {
		if n <= 0 {
			return fmt.Errorf("query max length must be positive, got %d", n)
		}
		h.queryMaxLen = n
		return nil
	}
}

// NewActivityAnnouncementHook creates a new activity announcement hook.
func NewActivityAnnouncementHook(
	coordinator core.ActivityCoordinator,
	agentName, agentDomain string,
	maxInPrompt int,
	opts ...ActivityAnnouncementOption,
) (*ActivityAnnouncementHook, error) {
	if coordinator == nil {
		return nil, fmt.Errorf("activity coordinator is required for ActivityAnnouncementHook")
	}
	if maxInPrompt <= 0 {
		maxInPrompt = 10
	}
	h := &ActivityAnnouncementHook{
		coordinator: coordinator,
		filter:      &RecentActivityFilter{MaxSignals: maxInPrompt},
		agentName:   agentName,
		agentDomain: agentDomain,
		signalTTL:   5 * time.Minute,
		queryMaxLen: 200,
		logger:      &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(h); err != nil {
			return nil, fmt.Errorf("invalid activity announcement option: %w", err)
		}
	}
	return h, nil
}

func (h *ActivityAnnouncementHook) Name() string { return "activity-announcement" }

func (h *ActivityAnnouncementHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
	requestID := GetRequestID(ctx)
	startTime := time.Now()

	// 1. Announce own activity
	signal := core.ActivitySignal{
		AgentName:   h.agentName,
		AgentDomain: h.agentDomain,
		RequestID:   requestID,
		Query:       truncateString(pctx.Request, h.queryMaxLen),
		Status:      "planning",
		StartedAt:   time.Now(),
		TTL:         h.signalTTL,
	}
	if err := h.coordinator.AnnounceActivity(ctx, signal); err != nil {
		telemetry.RecordSpanError(ctx, err)
		if h.logger != nil {
			h.logger.WarnWithContext(ctx, "Failed to announce activity, continuing", map[string]interface{}{
				"operation":  "activity_coordination",
				"request_id": requestID,
				"error":      err.Error(),
				"error_type": "activity_announce",
			})
		}
		telemetry.Counter("orchestration.activity_coordination.errors",
			"module", telemetry.ModuleOrchestration, "error_type", "activity_announce")
	} else {
		telemetry.Counter("orchestration.activity_coordination.announced",
			"module", telemetry.ModuleOrchestration)
	}

	// 2. Discover domain activities
	domainActivities, err := h.coordinator.GetDomainActivities(ctx, h.agentDomain)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if h.logger != nil {
			h.logger.WarnWithContext(ctx, "Failed to discover domain activities, continuing", map[string]interface{}{
				"operation":  "activity_coordination",
				"request_id": requestID,
				"error":      err.Error(),
				"error_type": "activity_discover",
			})
		}
		telemetry.Counter("orchestration.activity_coordination.errors",
			"module", telemetry.ModuleOrchestration, "error_type", "activity_discover")
		return nil, nil
	}

	// 3. Filter
	relevant := h.filter.Filter(ctx, requestID, domainActivities)

	// 4. Inject into enrichments
	if len(relevant) > 0 {
		section := formatActivitySignals(relevant)
		pctx.Enrichments[core.EnrichmentActivityCoordination] = section
	}

	durationMs := float64(time.Since(startTime).Milliseconds())
	telemetry.AddSpanEvent(ctx, "activity.coordination.complete",
		attribute.String("request_id", requestID),
		attribute.Int("signals_discovered", len(domainActivities)),
		attribute.Int("signals_shown", len(relevant)),
	)
	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "Activity coordination completed", map[string]interface{}{
			"operation":          "activity_coordination",
			"request_id":         requestID,
			"signals_discovered": len(domainActivities),
			"signals_shown":      len(relevant),
			"duration_ms":        durationMs,
		})
	}
	telemetry.Counter("orchestration.activity_coordination.discovered",
		"module", telemetry.ModuleOrchestration)
	telemetry.Histogram("orchestration.activity_coordination.duration_ms", durationMs,
		"module", telemetry.ModuleOrchestration)

	return nil, nil
}

// --- ActivityCleanupHook ---

// ActivityCleanupHook removes the agent's activity signal after synthesis completes.
type ActivityCleanupHook struct {
	coordinator core.ActivityCoordinator
	logger      core.Logger
}

// NewActivityCleanupHook creates a new activity cleanup hook.
func NewActivityCleanupHook(coordinator core.ActivityCoordinator, logger core.Logger) (*ActivityCleanupHook, error) {
	if coordinator == nil {
		return nil, fmt.Errorf("activity coordinator is required for ActivityCleanupHook")
	}
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/orchestration")
	}
	return &ActivityCleanupHook{
		coordinator: coordinator,
		logger:      logger,
	}, nil
}

func (h *ActivityCleanupHook) Name() string { return "activity-cleanup" }

func (h *ActivityCleanupHook) AfterSynthesis(ctx context.Context, pctx *core.PipelineContext, response string) (string, error) {
	requestID := GetRequestID(ctx)
	if err := h.coordinator.CompleteActivity(ctx, requestID); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "activity.cleanup.failed",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
		)
		if h.logger != nil {
			h.logger.WarnWithContext(ctx, "Failed to complete activity signal, TTL will expire", map[string]interface{}{
				"operation":  "activity_cleanup",
				"request_id": requestID,
				"error":      err.Error(),
				"error_type": "activity_complete",
			})
		}
	} else {
		telemetry.AddSpanEvent(ctx, "activity.cleanup.complete",
			attribute.String("request_id", requestID),
		)
	}
	return response, nil
}

// --- Formatting ---

func formatActivitySignals(signals []core.ActivitySignal) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Active in this domain (%d):", len(signals))
	for _, s := range signals {
		elapsed := time.Since(s.StartedAt).Round(time.Second)
		fmt.Fprintf(&sb, "\n- %s: %q (status: %s, started %s ago)",
			s.AgentName, s.Query, s.Status, elapsed)
	}
	return sb.String()
}
