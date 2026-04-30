package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

// helper — constructor now returns (*T, error)
func newInMemCoord(domain string) *InMemoryActivityCoordinator {
	c, _ := NewInMemoryActivityCoordinator(domain)
	return c
}

func TestInMemoryActivityCoordinator_AnnounceAndDiscover(t *testing.T) {
	coord := newInMemCoord("infrastructure")
	ctx := context.Background()

	signal := core.ActivitySignal{
		AgentName:   "devops-chat-agent",
		AgentDomain: "infrastructure",
		RequestID:   "req-1",
		Query:       "restart product-catalog",
		Status:      "planning",
		StartedAt:   time.Now(),
		TTL:         5 * time.Minute,
	}
	require.NoError(t, coord.AnnounceActivity(ctx, signal))

	activities, err := coord.GetDomainActivities(ctx, "infrastructure")
	require.NoError(t, err)
	require.Len(t, activities, 1)
	assert.Equal(t, "devops-chat-agent", activities[0].AgentName)
	assert.Equal(t, "restart product-catalog", activities[0].Query)
	assert.Equal(t, "planning", activities[0].Status)
}

func TestInMemoryActivityCoordinator_UpdateStatus(t *testing.T) {
	coord := newInMemCoord("infra")
	ctx := context.Background()

	require.NoError(t, coord.AnnounceActivity(ctx, core.ActivitySignal{
		RequestID: "req-1", AgentDomain: "infra", Status: "planning", TTL: 5 * time.Minute,
	}))

	require.NoError(t, coord.UpdateStatus(ctx, "req-1", "executing"))

	activities, _ := coord.GetDomainActivities(ctx, "infra")
	require.Len(t, activities, 1)
	assert.Equal(t, "executing", activities[0].Status)
}

func TestInMemoryActivityCoordinator_UpdateStatus_NonExistent(t *testing.T) {
	coord := newInMemCoord("infra")
	ctx := context.Background()

	// Update a non-existent signal — should not error
	err := coord.UpdateStatus(ctx, "req-999", "executing")
	assert.NoError(t, err)
}

func TestInMemoryActivityCoordinator_CompleteActivity(t *testing.T) {
	coord := newInMemCoord("infra")
	ctx := context.Background()

	require.NoError(t, coord.AnnounceActivity(ctx, core.ActivitySignal{
		RequestID: "req-1", AgentDomain: "infra", Status: "planning", TTL: 5 * time.Minute,
	}))

	require.NoError(t, coord.CompleteActivity(ctx, "req-1"))

	activities, _ := coord.GetDomainActivities(ctx, "infra")
	assert.Len(t, activities, 0)
}

func TestInMemoryActivityCoordinator_CompleteActivity_NonExistent(t *testing.T) {
	coord := newInMemCoord("infra")
	ctx := context.Background()

	err := coord.CompleteActivity(ctx, "req-999")
	assert.NoError(t, err)
}

func TestInMemoryActivityCoordinator_TTLExpiry(t *testing.T) {
	coord := newInMemCoord("infra")
	ctx := context.Background()

	// Signal with very short TTL
	require.NoError(t, coord.AnnounceActivity(ctx, core.ActivitySignal{
		RequestID: "req-1", AgentDomain: "infra", Status: "planning", TTL: 1 * time.Millisecond,
	}))

	time.Sleep(5 * time.Millisecond)

	// Should be expired via lazy cleanup
	activities, _ := coord.GetDomainActivities(ctx, "infra")
	assert.Len(t, activities, 0)
}

func TestInMemoryActivityCoordinator_DomainFiltering(t *testing.T) {
	coord := newInMemCoord("multi")
	ctx := context.Background()

	require.NoError(t, coord.AnnounceActivity(ctx, core.ActivitySignal{
		RequestID: "req-1", AgentDomain: "infrastructure", Status: "planning", TTL: 5 * time.Minute,
	}))
	require.NoError(t, coord.AnnounceActivity(ctx, core.ActivitySignal{
		RequestID: "req-2", AgentDomain: "security", Status: "executing", TTL: 5 * time.Minute,
	}))

	infraActivities, _ := coord.GetDomainActivities(ctx, "infrastructure")
	assert.Len(t, infraActivities, 1)
	assert.Equal(t, "req-1", infraActivities[0].RequestID)

	secActivities, _ := coord.GetDomainActivities(ctx, "security")
	assert.Len(t, secActivities, 1)
	assert.Equal(t, "req-2", secActivities[0].RequestID)

	// Empty domain
	emptyActivities, _ := coord.GetDomainActivities(ctx, "finance")
	assert.Len(t, emptyActivities, 0)
}

func TestInMemoryActivityCoordinator_MultipleSignals(t *testing.T) {
	coord := newInMemCoord("infra")
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, coord.AnnounceActivity(ctx, core.ActivitySignal{
			RequestID:   "req-" + string(rune('A'+i)),
			AgentDomain: "infra",
			Status:      "executing",
			TTL:         5 * time.Minute,
		}))
	}

	activities, _ := coord.GetDomainActivities(ctx, "infra")
	assert.Len(t, activities, 5)
}

func TestInMemoryActivityCoordinator_OverwriteSignal(t *testing.T) {
	coord := newInMemCoord("infra")
	ctx := context.Background()

	require.NoError(t, coord.AnnounceActivity(ctx, core.ActivitySignal{
		RequestID: "req-1", AgentDomain: "infra", Query: "first query", Status: "planning", TTL: 5 * time.Minute,
	}))
	// Re-announce with same requestID — should overwrite
	require.NoError(t, coord.AnnounceActivity(ctx, core.ActivitySignal{
		RequestID: "req-1", AgentDomain: "infra", Query: "updated query", Status: "executing", TTL: 5 * time.Minute,
	}))

	activities, _ := coord.GetDomainActivities(ctx, "infra")
	require.Len(t, activities, 1)
	assert.Equal(t, "updated query", activities[0].Query)
	assert.Equal(t, "executing", activities[0].Status)
}

func TestInMemoryActivityCoordinator_MetadataPreserved(t *testing.T) {
	coord := newInMemCoord("infra")
	ctx := context.Background()

	require.NoError(t, coord.AnnounceActivity(ctx, core.ActivitySignal{
		RequestID: "req-1", AgentDomain: "infra", TTL: 5 * time.Minute,
		Metadata: map[string]string{"entity_id": "pod-xyz", "severity": "critical"},
	}))

	activities, _ := coord.GetDomainActivities(ctx, "infra")
	require.Len(t, activities, 1)
	assert.Equal(t, "pod-xyz", activities[0].Metadata["entity_id"])
	assert.Equal(t, "critical", activities[0].Metadata["severity"])
}

func TestMarshalUnmarshalSignal(t *testing.T) {
	original := core.ActivitySignal{
		AgentName:   "agent",
		AgentDomain: "infra",
		RequestID:   "req-1",
		Query:       "test query",
		Status:      "executing",
		StartedAt:   time.Now().Truncate(time.Second), // Truncate for JSON roundtrip
		Metadata:    map[string]string{"key": "value"},
	}

	data, err := MarshalSignal(original)
	require.NoError(t, err)

	restored, err := UnmarshalSignal(data)
	require.NoError(t, err)
	assert.Equal(t, original.AgentName, restored.AgentName)
	assert.Equal(t, original.RequestID, restored.RequestID)
	assert.Equal(t, original.Query, restored.Query)
	assert.Equal(t, original.Status, restored.Status)
	assert.Equal(t, original.Metadata["key"], restored.Metadata["key"])
}

func TestUnmarshalSignal_Invalid(t *testing.T) {
	_, err := UnmarshalSignal([]byte("not json"))
	assert.Error(t, err)
}

func TestInMemoryActivityCoordinator_ConcurrentAccess(t *testing.T) {
	coord := newInMemCoord("infra")
	ctx := context.Background()
	done := make(chan bool, 20)

	// 10 goroutines announcing
	for i := 0; i < 10; i++ {
		go func(id int) {
			coord.AnnounceActivity(ctx, core.ActivitySignal{
				RequestID:   fmt.Sprintf("req-%d", id),
				AgentDomain: "infra",
				Status:      "executing",
				TTL:         5 * time.Minute,
			})
			done <- true
		}(i)
	}

	// 10 goroutines reading
	for i := 0; i < 10; i++ {
		go func() {
			coord.GetDomainActivities(ctx, "infra")
			done <- true
		}()
	}

	// Wait for all
	for i := 0; i < 20; i++ {
		<-done
	}

	// Verify no panic, data is present
	activities, err := coord.GetDomainActivities(ctx, "infra")
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(activities), 1, "should have at least some signals")
}
