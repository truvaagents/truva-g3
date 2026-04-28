package memory

import (
	"context"
	"testing"

	"time"

	"github.com/truvaagents/truva-g3/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- InMemoryEpisodicMemory Tests ---

func TestInMemoryEpisodicMemory_RecordAndQuery(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("infrastructure", 1000)
	ctx := context.Background()

	// Record an event
	event := core.AgentEvent{
		AgentName:   "event-driven-agent",
		AgentDomain: "infrastructure",
		ActionType:  "pod_restart",
		EntityType:  "pod",
		EntityID:    "payment-service-pod-7x9k2",
		Summary:     "Restarted pod due to OOMKilled",
		Outcome:     "success",
		Scope:       core.ScopeSharedDomain,
		Importance:  7.0,
	}
	err := mem.RecordEvent(ctx, event)
	require.NoError(t, err)

	// Query it back
	events, err := mem.QueryEvents(ctx, "infrastructure", core.EventFilter{
		EntityType: "pod",
		EntityID:   "payment-service-pod-7x9k2",
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "pod_restart", events[0].ActionType)
	assert.Equal(t, "payment-service-pod-7x9k2", events[0].EntityID)
	assert.NotEmpty(t, events[0].EventID, "EventID should be auto-generated")
	assert.False(t, events[0].Timestamp.IsZero(), "Timestamp should be auto-set")
}

func TestInMemoryEpisodicMemory_ScopeFiltering(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("infrastructure", 1000)
	ctx := context.Background()

	// Record events with different scopes
	events := []core.AgentEvent{
		{AgentName: "agent-a", AgentDomain: "infrastructure", Scope: core.ScopeGlobal, EntityType: "svc", EntityID: "svc-1", ActionType: "alert"},
		{AgentName: "agent-a", AgentDomain: "infrastructure", Scope: core.ScopeSharedDomain, EntityType: "svc", EntityID: "svc-2", ActionType: "restart"},
		{AgentName: "agent-a", AgentDomain: "infrastructure", Scope: core.ScopePrivate, EntityType: "svc", EntityID: "svc-3", ActionType: "debug"},
	}
	for _, e := range events {
		require.NoError(t, mem.RecordEvent(ctx, e))
	}

	t.Run("same domain sees global + shared_domain", func(t *testing.T) {
		results, err := mem.QueryEvents(ctx, "infrastructure", core.EventFilter{Limit: 10})
		require.NoError(t, err)
		assert.Len(t, results, 2) // global + shared_domain (private needs matching agent name)
	})

	t.Run("same domain + agent sees private", func(t *testing.T) {
		results, err := mem.QueryEvents(ctx, "infrastructure", core.EventFilter{AgentName: "agent-a", Limit: 10})
		require.NoError(t, err)
		assert.Len(t, results, 3) // all three visible
	})

	t.Run("different domain sees only global", func(t *testing.T) {
		results, err := mem.QueryEvents(ctx, "commerce", core.EventFilter{Limit: 10})
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, core.ScopeGlobal, results[0].Scope)
	})
}

func TestInMemoryEpisodicMemory_Eviction(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 20)
	ctx := context.Background()

	// Fill beyond capacity
	for i := 0; i < 30; i++ {
		require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
			AgentName:   "agent",
			AgentDomain: "default",
			ActionType:  "test",
			Scope:       core.ScopeGlobal,
		}))
	}

	// Should have evicted oldest
	events, _ := mem.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})
	assert.LessOrEqual(t, len(events), 20+1) // maxSize + buffer before next eviction
}

func TestInMemoryEpisodicMemory_TimeFiltering(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	ctx := context.Background()

	now := time.Now()
	old := now.Add(-2 * time.Hour)
	recent := now.Add(-5 * time.Minute)

	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName: "agent", AgentDomain: "default", ActionType: "old",
		Scope: core.ScopeGlobal, Timestamp: old,
	}))
	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName: "agent", AgentDomain: "default", ActionType: "recent",
		Scope: core.ScopeGlobal, Timestamp: recent,
	}))

	// Query with Since filter
	events, err := mem.QueryEvents(ctx, "default", core.EventFilter{
		Since: now.Add(-1 * time.Hour),
		Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "recent", events[0].ActionType)
}

func TestInMemoryEpisodicMemory_QueryEntityHistory(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	ctx := context.Background()

	// Record events at different times
	for i, action := range []string{"first", "second", "third"} {
		require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: action,
			EntityType: "pod", EntityID: "pod-1",
			Scope: core.ScopeGlobal, Timestamp: time.Now().Add(time.Duration(i) * time.Minute),
		}))
	}

	// QueryEntityHistory should return chronological order
	events, err := mem.QueryEntityHistory(ctx, "default", "pod", "pod-1", time.Time{})
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, "first", events[0].ActionType)
	assert.Equal(t, "third", events[2].ActionType)
}

func TestInMemoryEpisodicMemory_DeleteEvents(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	ctx := context.Background()

	// Record 3 events
	for i := 0; i < 3; i++ {
		mem.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1", Scope: core.ScopeGlobal,
		})
	}

	// Get all event IDs (most-recent-first from QueryEvents)
	allEvents, _ := mem.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})
	require.Len(t, allEvents, 3)

	// Delete the first two returned (most recent two)
	deleteIDs := []string{allEvents[0].EventID, allEvents[1].EventID}
	err := mem.DeleteEvents(ctx, deleteIDs)
	require.NoError(t, err)

	// Only the oldest event should remain
	remaining, _ := mem.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})
	assert.Len(t, remaining, 1)
	assert.Equal(t, allEvents[2].EventID, remaining[0].EventID)
}

func TestInMemoryEpisodicMemory_DeleteEvents_Idempotent(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	ctx := context.Background()

	// Delete non-existent events — should not error
	err := mem.DeleteEvents(ctx, []string{"nonexistent-1", "nonexistent-2"})
	assert.NoError(t, err)
}

func TestInMemoryEpisodicMemory_DeleteEvents_Empty(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	err := mem.DeleteEvents(context.Background(), nil)
	assert.NoError(t, err)
}

// --- InMemoryInvestigationCoordinator Tests ---

func TestInMemoryInvestigationCoordinator_ClaimAndRelease(t *testing.T) {
	coord := NewInMemoryInvestigationCoordinator()
	ctx := context.Background()

	// First claim should succeed
	claimed, holder, err := coord.ClaimInvestigation(ctx, "agent-a", "pod-1", 5*time.Minute)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.Empty(t, holder)

	// Second claim by different agent should fail
	claimed, holder, err = coord.ClaimInvestigation(ctx, "agent-b", "pod-1", 5*time.Minute)
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Equal(t, "agent-a", holder)

	// Release by owner should work
	err = coord.ReleaseInvestigation(ctx, "agent-a", "pod-1")
	require.NoError(t, err)

	// Now agent-b can claim
	claimed, holder, err = coord.ClaimInvestigation(ctx, "agent-b", "pod-1", 5*time.Minute)
	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestInMemoryInvestigationCoordinator_OwnershipCheck(t *testing.T) {
	coord := NewInMemoryInvestigationCoordinator()
	ctx := context.Background()

	// Agent-a claims
	coord.ClaimInvestigation(ctx, "agent-a", "pod-1", 5*time.Minute)

	// Agent-b cannot release agent-a's claim
	err := coord.ReleaseInvestigation(ctx, "agent-b", "pod-1")
	require.NoError(t, err) // No error, but claim persists

	// Verify claim still held by agent-a
	claimed, holder, _ := coord.ClaimInvestigation(ctx, "agent-c", "pod-1", 5*time.Minute)
	assert.False(t, claimed)
	assert.Equal(t, "agent-a", holder)
}

func TestInMemoryInvestigationCoordinator_TTLExpiry(t *testing.T) {
	coord := NewInMemoryInvestigationCoordinator()
	ctx := context.Background()

	// Claim with very short TTL
	coord.ClaimInvestigation(ctx, "agent-a", "pod-1", 1*time.Millisecond)

	// Wait for expiry
	time.Sleep(5 * time.Millisecond)

	// Claim should now succeed (expired)
	claimed, _, err := coord.ClaimInvestigation(ctx, "agent-b", "pod-1", 5*time.Minute)
	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestInMemoryInvestigationCoordinator_GetActive(t *testing.T) {
	coord := NewInMemoryInvestigationCoordinator()
	ctx := context.Background()

	coord.ClaimInvestigation(ctx, "agent-a", "pod-1", 5*time.Minute)
	coord.ClaimInvestigation(ctx, "agent-b", "pod-2", 5*time.Minute)
	coord.ClaimInvestigation(ctx, "agent-c", "pod-3", 1*time.Millisecond)

	time.Sleep(5 * time.Millisecond) // Let pod-3 expire

	active, err := coord.GetActiveInvestigations(ctx)
	require.NoError(t, err)
	assert.Len(t, active, 2)
	assert.Equal(t, "agent-a", active["pod-1"])
	assert.Equal(t, "agent-b", active["pod-2"])
}

// --- Multi-Entity Tests (Phase 7) ---

func TestInMemoryEpisodicMemory_MultiEntity_QueryByAnyEntity(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	ctx := context.Background()

	// Record one event with multiple entities
	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName:   "agent",
		AgentDomain: "default",
		ActionType:  "rollout_restart",
		EntityType:  "service",
		EntityID:    "product-catalog",
		Entities: []core.EntityRef{
			{Type: "service", ID: "product-catalog"},
			{Type: "pod", ID: "product-catalog-pod-xyz"},
			{Type: "deployment", ID: "product-catalog"},
		},
		Summary:   "Restarted product-catalog",
		Outcome:   "success",
		Scope:     core.ScopeSharedDomain,
		Timestamp: time.Now(),
	}))

	// Should be findable via any of the 3 entities
	events1, err := mem.QueryEntityHistory(ctx, "default", "service", "product-catalog", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Len(t, events1, 1)
	assert.Equal(t, "Restarted product-catalog", events1[0].Summary)

	events2, err := mem.QueryEntityHistory(ctx, "default", "pod", "product-catalog-pod-xyz", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Len(t, events2, 1)
	assert.Equal(t, "Restarted product-catalog", events2[0].Summary)

	events3, err := mem.QueryEntityHistory(ctx, "default", "deployment", "product-catalog", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Len(t, events3, 1)
	assert.Equal(t, "Restarted product-catalog", events3[0].Summary)

	// All 3 queries return the SAME event
	assert.Equal(t, events1[0].EventID, events2[0].EventID)
	assert.Equal(t, events2[0].EventID, events3[0].EventID)
}

func TestInMemoryEpisodicMemory_MultiEntity_SingleEventInStream(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	ctx := context.Background()

	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName:   "agent",
		AgentDomain: "default",
		ActionType:  "create_issue",
		EntityType:  "service",
		EntityID:    "my-svc",
		Entities: []core.EntityRef{
			{Type: "service", ID: "my-svc"},
			{Type: "pod", ID: "my-pod"},
		},
		Outcome:   "success",
		Scope:     core.ScopeSharedDomain,
		Timestamp: time.Now(),
	}))

	// Only 1 event in the stream despite 2 entities
	allEvents, _ := mem.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})
	assert.Len(t, allEvents, 1)
}

func TestInMemoryEpisodicMemory_MultiEntity_BackwardCompat_SingularFields(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	ctx := context.Background()

	// Old-style event: no Entities, only singular fields
	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName:   "agent",
		AgentDomain: "default",
		ActionType:  "get_pods",
		EntityType:  "pod",
		EntityID:    "legacy-pod",
		Outcome:     "success",
		Scope:       core.ScopeSharedDomain,
		Timestamp:   time.Now(),
	}))

	// Should still be queryable via singular fields
	events, err := mem.QueryEntityHistory(ctx, "default", "pod", "legacy-pod", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, "legacy-pod", events[0].EntityID)
}

func TestInMemoryEpisodicMemory_MultiEntity_EntityTypeOnlyFilter(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	ctx := context.Background()

	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName: "agent", AgentDomain: "default", ActionType: "action",
		EntityType: "pod", EntityID: "pod-1",
		Entities: []core.EntityRef{{Type: "pod", ID: "pod-1"}, {Type: "service", ID: "svc-1"}},
		Outcome:  "success", Scope: core.ScopeSharedDomain, Timestamp: time.Now(),
	}))

	// Filter by entity type only — should match "service" from Entities
	events, _ := mem.QueryEvents(ctx, "default", core.EventFilter{
		EntityType: "service",
		Limit:      100,
	})
	assert.Len(t, events, 1)

	// Filter by entity type that doesn't exist
	events2, _ := mem.QueryEvents(ctx, "default", core.EventFilter{
		EntityType: "deployment",
		Limit:      100,
	})
	assert.Len(t, events2, 0)
}

func TestInMemoryEpisodicMemory_MultiEntity_DeleteRemovesFromAllQueries(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("default", 1000)
	ctx := context.Background()

	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName: "agent", AgentDomain: "default", ActionType: "action",
		EntityType: "pod", EntityID: "pod-1",
		Entities: []core.EntityRef{{Type: "pod", ID: "pod-1"}, {Type: "service", ID: "svc-1"}},
		Outcome:  "success", Scope: core.ScopeSharedDomain, Timestamp: time.Now(),
	}))

	allEvents, _ := mem.QueryEvents(ctx, "default", core.EventFilter{Limit: 100})
	require.Len(t, allEvents, 1)

	// Delete the event
	require.NoError(t, mem.DeleteEvents(ctx, []string{allEvents[0].EventID}))

	// Should be gone from both entity queries
	e1, _ := mem.QueryEntityHistory(ctx, "default", "pod", "pod-1", time.Now().Add(-1*time.Hour))
	assert.Len(t, e1, 0)
	e2, _ := mem.QueryEntityHistory(ctx, "default", "service", "svc-1", time.Now().Add(-1*time.Hour))
	assert.Len(t, e2, 0)
}
