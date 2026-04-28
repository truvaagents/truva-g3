package memory

import (
	"context"
	"testing"

	"time"

	"github.com/truvaagents/truva-g3/core"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(func() { mr.Close() })

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, client
}

// --- StreamEpisodicMemory Tests ---

func TestStreamEpisodicMemory_RecordAndQueryByEntity(t *testing.T) {
	_, client := setupMiniRedis(t)
	ctx := context.Background()

	mem, err := NewStreamEpisodicMemory(
		WithEpisodicRedisClient(client),
		WithEpisodicDomain("infrastructure"),
	)
	require.NoError(t, err)

	// Record an event
	event := core.AgentEvent{
		AgentName:   "event-driven-agent",
		AgentDomain: "infrastructure",
		ActionType:  "pod_restart",
		EntityType:  "pod",
		EntityID:    "payment-service-7x9k2",
		Summary:     "Restarted pod due to OOMKilled",
		Outcome:     "success",
		Scope:       core.ScopeSharedDomain,
		Importance:  7.0,
	}
	err = mem.RecordEvent(ctx, event)
	require.NoError(t, err)

	// Query by entity
	events, err := mem.QueryEvents(ctx, "infrastructure", core.EventFilter{
		EntityType: "pod",
		EntityID:   "payment-service-7x9k2",
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "pod_restart", events[0].ActionType)
	assert.Equal(t, "payment-service-7x9k2", events[0].EntityID)
	assert.NotEmpty(t, events[0].EventID)
}

func TestStreamEpisodicMemory_ScopeFiltering(t *testing.T) {
	_, client := setupMiniRedis(t)
	ctx := context.Background()

	mem, err := NewStreamEpisodicMemory(
		WithEpisodicRedisClient(client),
		WithEpisodicDomain("infrastructure"),
	)
	require.NoError(t, err)

	// Record events with different scopes
	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName: "agent-a", AgentDomain: "infrastructure",
		ActionType: "alert", EntityType: "svc", EntityID: "svc-1",
		Scope: core.ScopeGlobal, Importance: 5.0,
	}))
	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName: "agent-a", AgentDomain: "infrastructure",
		ActionType: "restart", EntityType: "svc", EntityID: "svc-2",
		Scope: core.ScopeSharedDomain, Importance: 5.0,
	}))
	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName: "agent-a", AgentDomain: "infrastructure",
		ActionType: "debug", EntityType: "svc", EntityID: "svc-3",
		Scope: core.ScopePrivate, Importance: 2.0,
	}))

	t.Run("same domain sees global + shared_domain", func(t *testing.T) {
		results, err := mem.QueryEvents(ctx, "infrastructure", core.EventFilter{Limit: 10})
		require.NoError(t, err)
		assert.Len(t, results, 2) // global + shared_domain
	})

	t.Run("different domain sees only global", func(t *testing.T) {
		results, err := mem.QueryEvents(ctx, "commerce", core.EventFilter{Limit: 10})
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, core.ScopeGlobal, results[0].Scope)
	})

	t.Run("same domain + agent name sees private", func(t *testing.T) {
		results, err := mem.QueryEvents(ctx, "infrastructure", core.EventFilter{
			AgentName: "agent-a",
			Limit:     10,
		})
		require.NoError(t, err)
		assert.Len(t, results, 3) // all three
	})
}

func TestStreamEpisodicMemory_GlobalDualWrite(t *testing.T) {
	mr, client := setupMiniRedis(t)
	ctx := context.Background()

	mem, err := NewStreamEpisodicMemory(
		WithEpisodicRedisClient(client),
		WithEpisodicDomain("infrastructure"),
	)
	require.NoError(t, err)

	// Record a global event
	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName: "agent-a", AgentDomain: "infrastructure",
		ActionType: "service_down", EntityType: "svc", EntityID: "svc-1",
		Scope: core.ScopeGlobal,
	}))

	// Verify domain stream has the event
	domainLen, err := client.XLen(ctx, "truvag3:memory:infrastructure:events:stream").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), domainLen)

	// Verify global stream also has the event (dual-write)
	globalLen, err := client.XLen(ctx, "truvag3:memory:global:events:stream").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), globalLen)

	// Record a shared_domain event — should NOT be in global stream
	require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
		AgentName: "agent-a", AgentDomain: "infrastructure",
		ActionType: "restart", EntityType: "svc", EntityID: "svc-2",
		Scope: core.ScopeSharedDomain,
	}))

	globalLen, err = client.XLen(ctx, "truvag3:memory:global:events:stream").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), globalLen) // Still 1 — shared_domain not dual-written

	_ = mr // keep reference
}

func TestStreamEpisodicMemory_QueryEntityHistory(t *testing.T) {
	_, client := setupMiniRedis(t)
	ctx := context.Background()

	mem, err := NewStreamEpisodicMemory(
		WithEpisodicRedisClient(client),
		WithEpisodicDomain("default"),
	)
	require.NoError(t, err)

	// Record events with different timestamps
	for i, action := range []string{"first", "second", "third"} {
		require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: action,
			EntityType: "pod", EntityID: "pod-1",
			Scope: core.ScopeGlobal, Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		}))
	}

	events, err := mem.QueryEntityHistory(ctx, "default", "pod", "pod-1", time.Time{})
	require.NoError(t, err)
	require.Len(t, events, 3)
}

func TestStreamEpisodicMemory_DeleteEvents(t *testing.T) {
	_, client := setupMiniRedis(t)
	ctx := context.Background()

	mem, err := NewStreamEpisodicMemory(
		WithEpisodicRedisClient(client),
		WithEpisodicDomain("default"),
	)
	require.NoError(t, err)

	// Record 3 events
	for i := 0; i < 3; i++ {
		require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
			AgentName: "agent", AgentDomain: "default", ActionType: "test",
			EntityType: "pod", EntityID: "pod-1",
			Scope: core.ScopeGlobal, Importance: 5.0,
		}))
	}

	// Query all events to get IDs
	events, err := mem.QueryEvents(ctx, "default", core.EventFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, events, 3)

	// Delete first two
	deleteIDs := []string{events[0].EventID, events[1].EventID}
	err = mem.DeleteEvents(ctx, deleteIDs)
	require.NoError(t, err)

	// Query again — should have 1 remaining
	remaining, err := mem.QueryEvents(ctx, "default", core.EventFilter{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, remaining, 1)
}

func TestStreamEpisodicMemory_DeleteEvents_Idempotent(t *testing.T) {
	_, client := setupMiniRedis(t)
	mem, _ := NewStreamEpisodicMemory(
		WithEpisodicRedisClient(client),
		WithEpisodicDomain("default"),
	)

	// Delete non-existent events — should not error
	err := mem.DeleteEvents(context.Background(), []string{"nonexistent-1"})
	assert.NoError(t, err)
}

func TestStreamEpisodicMemory_ConstructorValidation(t *testing.T) {
	_, err := NewStreamEpisodicMemory() // No client
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis client is required")
}

// --- AtomicLockCoordinator Tests ---

func TestAtomicLockCoordinator_ClaimAndRelease(t *testing.T) {
	_, client := setupMiniRedis(t)
	ctx := context.Background()

	coord, err := NewAtomicLockCoordinator(
		WithCoordinatorRedisClient(client),
		WithCoordinatorDomain("infrastructure"),
	)
	require.NoError(t, err)

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

	// Release by owner
	err = coord.ReleaseInvestigation(ctx, "agent-a", "pod-1")
	require.NoError(t, err)

	// Now agent-b can claim
	claimed, _, err = coord.ClaimInvestigation(ctx, "agent-b", "pod-1", 5*time.Minute)
	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestAtomicLockCoordinator_OwnershipProtection(t *testing.T) {
	_, client := setupMiniRedis(t)
	ctx := context.Background()

	coord, err := NewAtomicLockCoordinator(
		WithCoordinatorRedisClient(client),
		WithCoordinatorDomain("infrastructure"),
	)
	require.NoError(t, err)

	// Agent-a claims
	coord.ClaimInvestigation(ctx, "agent-a", "pod-1", 5*time.Minute)

	// Agent-b tries to release — should not work
	coord.ReleaseInvestigation(ctx, "agent-b", "pod-1")

	// Claim should still be held by agent-a
	claimed, holder, _ := coord.ClaimInvestigation(ctx, "agent-c", "pod-1", 5*time.Minute)
	assert.False(t, claimed)
	assert.Equal(t, "agent-a", holder)
}

func TestAtomicLockCoordinator_TTLExpiry(t *testing.T) {
	mr, client := setupMiniRedis(t)
	ctx := context.Background()

	coord, err := NewAtomicLockCoordinator(
		WithCoordinatorRedisClient(client),
		WithCoordinatorDomain("infrastructure"),
	)
	require.NoError(t, err)

	// Claim with short TTL
	coord.ClaimInvestigation(ctx, "agent-a", "pod-1", 100*time.Millisecond)

	// Fast-forward miniredis time
	mr.FastForward(200 * time.Millisecond)

	// Should be claimable now
	claimed, _, err := coord.ClaimInvestigation(ctx, "agent-b", "pod-1", 5*time.Minute)
	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestAtomicLockCoordinator_GetActive(t *testing.T) {
	_, client := setupMiniRedis(t)
	ctx := context.Background()

	coord, err := NewAtomicLockCoordinator(
		WithCoordinatorRedisClient(client),
		WithCoordinatorDomain("infrastructure"),
	)
	require.NoError(t, err)

	coord.ClaimInvestigation(ctx, "agent-a", "pod-1", 5*time.Minute)
	coord.ClaimInvestigation(ctx, "agent-b", "pod-2", 5*time.Minute)

	active, err := coord.GetActiveInvestigations(ctx)
	require.NoError(t, err)
	assert.Len(t, active, 2)
	assert.Equal(t, "agent-a", active["pod-1"])
	assert.Equal(t, "agent-b", active["pod-2"])
}

func TestAtomicLockCoordinator_ConstructorValidation(t *testing.T) {
	_, err := NewAtomicLockCoordinator() // No client
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis client is required")
}
