package orchestration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

// --- RecentActivityFilter Tests ---

func TestRecentActivityFilter_ExcludesSelf(t *testing.T) {
	filter := &RecentActivityFilter{MaxSignals: 10}
	signals := []core.ActivitySignal{
		{RequestID: "own-req", AgentName: "me", Status: "planning"},
		{RequestID: "other-req", AgentName: "them", Status: "executing"},
	}

	result := filter.Filter(context.Background(), "own-req", signals)
	require.Len(t, result, 1)
	assert.Equal(t, "other-req", result[0].RequestID)
}

func TestRecentActivityFilter_ExcludesCompleted(t *testing.T) {
	filter := &RecentActivityFilter{MaxSignals: 10}
	signals := []core.ActivitySignal{
		{RequestID: "req-1", Status: "completed"},
		{RequestID: "req-2", Status: "executing"},
	}

	result := filter.Filter(context.Background(), "own", signals)
	require.Len(t, result, 1)
	assert.Equal(t, "req-2", result[0].RequestID)
}

func TestRecentActivityFilter_LimitsToMax(t *testing.T) {
	filter := &RecentActivityFilter{MaxSignals: 3}
	now := time.Now()
	signals := make([]core.ActivitySignal, 10)
	for i := range signals {
		signals[i] = core.ActivitySignal{
			RequestID: fmt.Sprintf("req-%d", i),
			Status:    "executing",
			StartedAt: now.Add(-time.Duration(i) * time.Second), // 0s ago, 1s ago, 2s ago...
		}
	}

	result := filter.Filter(context.Background(), "own", signals)
	assert.Len(t, result, 3)
	// Should be most recent first
	assert.Equal(t, "req-0", result[0].RequestID)
	assert.Equal(t, "req-1", result[1].RequestID)
	assert.Equal(t, "req-2", result[2].RequestID)
}

func TestRecentActivityFilter_SortsMostRecentFirst(t *testing.T) {
	filter := &RecentActivityFilter{MaxSignals: 10}
	now := time.Now()
	signals := []core.ActivitySignal{
		{RequestID: "old", Status: "executing", StartedAt: now.Add(-5 * time.Minute)},
		{RequestID: "new", Status: "executing", StartedAt: now.Add(-1 * time.Second)},
		{RequestID: "mid", Status: "executing", StartedAt: now.Add(-2 * time.Minute)},
	}

	result := filter.Filter(context.Background(), "own", signals)
	require.Len(t, result, 3)
	assert.Equal(t, "new", result[0].RequestID)
	assert.Equal(t, "mid", result[1].RequestID)
	assert.Equal(t, "old", result[2].RequestID)
}

func TestRecentActivityFilter_EmptyInput(t *testing.T) {
	filter := &RecentActivityFilter{MaxSignals: 10}
	result := filter.Filter(context.Background(), "own", nil)
	assert.Len(t, result, 0)
}

// --- ActivityAnnouncementHook Tests ---

func TestNewActivityAnnouncementHook_NilCoordinator(t *testing.T) {
	_, err := NewActivityAnnouncementHook(nil, "agent", "domain", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "activity coordinator is required")
}

func TestNewActivityAnnouncementHook_Defaults(t *testing.T) {
	hook, err := NewActivityAnnouncementHook(&core.MockActivityCoordinator{}, "agent", "domain", 10)
	require.NoError(t, err)
	assert.Equal(t, "activity-announcement", hook.Name())
	assert.Equal(t, 5*time.Minute, hook.signalTTL)
	assert.Equal(t, 200, hook.queryMaxLen)
}

func TestNewActivityAnnouncementHook_WithOptions(t *testing.T) {
	hook, err := NewActivityAnnouncementHook(
		&core.MockActivityCoordinator{}, "agent", "domain", 5,
		WithAnnouncementSignalTTL(10*time.Minute),
		WithAnnouncementQueryMaxLen(500),
		WithAnnouncementLogger(&core.NoOpLogger{}),
	)
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, hook.signalTTL)
	assert.Equal(t, 500, hook.queryMaxLen)
}

func TestNewActivityAnnouncementHook_InvalidOptions(t *testing.T) {
	tests := []struct {
		name string
		opt  ActivityAnnouncementOption
		err  string
	}{
		{"nil logger", WithAnnouncementLogger(nil), "logger cannot be nil"},
		{"zero TTL", WithAnnouncementSignalTTL(0), "signal TTL must be positive"},
		{"negative TTL", WithAnnouncementSignalTTL(-1 * time.Second), "signal TTL must be positive"},
		{"zero query len", WithAnnouncementQueryMaxLen(0), "query max length must be positive"},
		{"nil filter", WithAnnouncementFilter(nil), "activity filter cannot be nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewActivityAnnouncementHook(&core.MockActivityCoordinator{}, "agent", "domain", 10, tt.opt)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)
		})
	}
}

func TestNewActivityAnnouncementHook_CustomFilter(t *testing.T) {
	customFilter := &RecentActivityFilter{MaxSignals: 3}
	hook, err := NewActivityAnnouncementHook(
		&core.MockActivityCoordinator{}, "agent", "domain", 10,
		WithAnnouncementFilter(customFilter),
	)
	require.NoError(t, err)
	assert.Equal(t, customFilter, hook.filter)
}

func TestActivityAnnouncementHook_AnnouncesAndDiscovers(t *testing.T) {
	var announced core.ActivitySignal
	coord := &core.MockActivityCoordinator{
		AnnounceActivityFn: func(ctx context.Context, signal core.ActivitySignal) error {
			announced = signal
			return nil
		},
		GetDomainActivitiesFn: func(ctx context.Context, domain string) ([]core.ActivitySignal, error) {
			return []core.ActivitySignal{
				{RequestID: "other-req", AgentName: "other-agent", Query: "doing stuff", Status: "executing", StartedAt: time.Now()},
			}, nil
		},
	}

	hook, _ := NewActivityAnnouncementHook(coord, "my-agent", "infra", 10)
	pctx := &core.PipelineContext{
		Request:     "restart product-catalog",
		Enrichments: make(map[string]interface{}),
	}

	shortCircuit, err := hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)
	assert.Nil(t, shortCircuit)

	// Verify announcement
	assert.Equal(t, 1, coord.AnnounceActivityCt)
	assert.Equal(t, "my-agent", announced.AgentName)
	assert.Equal(t, "infra", announced.AgentDomain)
	assert.Equal(t, "planning", announced.Status)
	assert.Contains(t, announced.Query, "restart product-catalog")

	// Verify discovery injected into enrichments
	coordSection, ok := pctx.Enrichments[core.EnrichmentActivityCoordination].(string)
	assert.True(t, ok)
	assert.Contains(t, coordSection, "other-agent")
	assert.Contains(t, coordSection, "doing stuff")
}

func TestActivityAnnouncementHook_NoOtherAgents_NoEnrichment(t *testing.T) {
	coord := &core.MockActivityCoordinator{
		GetDomainActivitiesFn: func(ctx context.Context, domain string) ([]core.ActivitySignal, error) {
			// Empty — no one is active
			return []core.ActivitySignal{}, nil
		},
	}

	hook, _ := NewActivityAnnouncementHook(coord, "agent", "infra", 10)
	pctx := &core.PipelineContext{
		Request:     "test",
		Enrichments: make(map[string]interface{}),
	}

	hook.BeforePlanning(context.Background(), pctx)

	// No signals → no enrichment
	_, hasCoord := pctx.Enrichments[core.EnrichmentActivityCoordination]
	assert.False(t, hasCoord, "should not inject coordination when no other agents active")
}

func TestActivityAnnouncementHook_AnnounceError_Continues(t *testing.T) {
	coord := &core.MockActivityCoordinator{
		AnnounceActivityFn: func(ctx context.Context, signal core.ActivitySignal) error {
			return fmt.Errorf("redis down")
		},
		GetDomainActivitiesFn: func(ctx context.Context, domain string) ([]core.ActivitySignal, error) {
			return nil, nil
		},
	}

	hook, _ := NewActivityAnnouncementHook(coord, "agent", "infra", 10)
	pctx := &core.PipelineContext{Request: "test", Enrichments: make(map[string]interface{})}

	// Should not error — fail-open
	shortCircuit, err := hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)
	assert.Nil(t, shortCircuit)
}

func TestActivityAnnouncementHook_DiscoverError_Continues(t *testing.T) {
	coord := &core.MockActivityCoordinator{
		GetDomainActivitiesFn: func(ctx context.Context, domain string) ([]core.ActivitySignal, error) {
			return nil, fmt.Errorf("redis down")
		},
	}

	hook, _ := NewActivityAnnouncementHook(coord, "agent", "infra", 10)
	pctx := &core.PipelineContext{Request: "test", Enrichments: make(map[string]interface{})}

	shortCircuit, err := hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)
	assert.Nil(t, shortCircuit)
}

func TestActivityAnnouncementHook_QueryTruncation(t *testing.T) {
	var announced core.ActivitySignal
	coord := &core.MockActivityCoordinator{
		AnnounceActivityFn: func(ctx context.Context, signal core.ActivitySignal) error {
			announced = signal
			return nil
		},
	}

	hook, _ := NewActivityAnnouncementHook(coord, "agent", "infra", 10,
		WithAnnouncementQueryMaxLen(20),
	)
	pctx := &core.PipelineContext{
		Request:     "this is a very long query that should be truncated to 20 chars",
		Enrichments: make(map[string]interface{}),
	}

	hook.BeforePlanning(context.Background(), pctx)
	assert.LessOrEqual(t, len(announced.Query), 30) // truncateString adds "..." suffix
}

// --- ActivityCleanupHook Tests ---

func TestNewActivityCleanupHook_NilCoordinator(t *testing.T) {
	_, err := NewActivityCleanupHook(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "activity coordinator is required")
}

func TestNewActivityCleanupHook_Defaults(t *testing.T) {
	hook, err := NewActivityCleanupHook(&core.MockActivityCoordinator{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "activity-cleanup", hook.Name())
}

func TestActivityCleanupHook_CompletesActivity(t *testing.T) {
	var completedRequestID string
	coord := &core.MockActivityCoordinator{
		CompleteActivityFn: func(ctx context.Context, requestID string) error {
			completedRequestID = requestID
			return nil
		},
	}

	hook, _ := NewActivityCleanupHook(coord, &core.NoOpLogger{})
	pctx := &core.PipelineContext{}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "synthesis response")
	assert.NoError(t, err)
	assert.Equal(t, "synthesis response", response, "should never mutate response")
	assert.Equal(t, 1, coord.CompleteActivityCt)
	// requestID will be empty since no baggage in context, but the call should still be made
	assert.Empty(t, completedRequestID) // GetRequestID returns "" without baggage
}

func TestActivityCleanupHook_CompleteError_Continues(t *testing.T) {
	coord := &core.MockActivityCoordinator{
		CompleteActivityFn: func(ctx context.Context, requestID string) error {
			return fmt.Errorf("redis down")
		},
	}

	hook, _ := NewActivityCleanupHook(coord, &core.NoOpLogger{})
	pctx := &core.PipelineContext{}

	response, err := hook.AfterSynthesis(context.Background(), pctx, "response text")
	assert.NoError(t, err)
	assert.Equal(t, "response text", response, "should not mutate response on error")
}

// --- formatActivitySignals Tests ---

func TestFormatActivitySignals(t *testing.T) {
	signals := []core.ActivitySignal{
		{AgentName: "agent-a", Query: "restart pods", Status: "executing", StartedAt: time.Now().Add(-30 * time.Second)},
		{AgentName: "agent-b", Query: "check metrics", Status: "planning", StartedAt: time.Now().Add(-5 * time.Second)},
	}

	result := formatActivitySignals(signals)
	assert.Contains(t, result, "Active in this domain (2):")
	assert.Contains(t, result, "agent-a")
	assert.Contains(t, result, "restart pods")
	assert.Contains(t, result, "executing")
	assert.Contains(t, result, "agent-b")
	assert.Contains(t, result, "check metrics")
	assert.Contains(t, result, "planning")
}

func TestFormatActivitySignals_Empty(t *testing.T) {
	result := formatActivitySignals(nil)
	assert.Contains(t, result, "Active in this domain (0):")
}
