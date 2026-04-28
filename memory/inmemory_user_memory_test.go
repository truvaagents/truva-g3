package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryUserMemory_RememberAndRecall(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	fact := core.UserFact{
		FactID:     "fact-1",
		Namespace:  "travel",
		Category:   "preference",
		Content:    "User prefers window seats",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
	}

	err := mem.Remember(ctx, "user-1", fact)
	require.NoError(t, err)

	// Recall by substring match
	results, err := mem.Recall(ctx, "user-1", "travel", "window", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "User prefers window seats", results[0].Content)
	assert.Equal(t, "user-1", results[0].UserID)
	assert.False(t, results[0].CreatedAt.IsZero())
	assert.False(t, results[0].UpdatedAt.IsZero())
}

func TestInMemoryUserMemory_UpsertByFactID(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	// Store initial fact
	fact := core.UserFact{
		FactID:     "fact-1",
		Namespace:  "travel",
		Category:   "preference",
		Content:    "User prefers window seats",
		Source:     core.SourceExplicit,
		Confidence: 0.95,
	}
	require.NoError(t, mem.Remember(ctx, "user-1", fact))

	// Upsert with same FactID — should update, not duplicate
	fact.Content = "User now prefers aisle seats"
	fact.Source = core.SourceCorrection
	fact.Confidence = 0.98
	require.NoError(t, mem.Remember(ctx, "user-1", fact))

	// Should have exactly 1 fact, with updated content
	results, err := mem.Recall(ctx, "user-1", "", "", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "User now prefers aisle seats", results[0].Content)
	assert.Equal(t, core.SourceCorrection, results[0].Source)
}

func TestInMemoryUserMemory_AutoAssignFactID(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	fact := core.UserFact{
		// No FactID — should be auto-assigned
		Namespace: "travel",
		Category:  "preference",
		Content:   "User likes direct flights",
		Source:    core.SourceInferred,
	}
	require.NoError(t, mem.Remember(ctx, "user-1", fact))

	results, err := mem.Recall(ctx, "user-1", "", "", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.NotEmpty(t, results[0].FactID, "FactID should be auto-assigned")
}

func TestInMemoryUserMemory_NamespaceFiltering(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	// Store facts in different namespaces
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Category: "preference", Content: "Prefers window seats", Confidence: 0.9,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f2", Namespace: "devops", Category: "preference", Content: "Prefers vim", Confidence: 0.9,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f3", Namespace: "universal", Category: "identity", Content: "User name is Sarah", Confidence: 0.95,
	}))

	// Namespace-specific recall
	travel, err := mem.Recall(ctx, "user-1", "travel", "", 10)
	require.NoError(t, err)
	assert.Len(t, travel, 1)
	assert.Equal(t, "Prefers window seats", travel[0].Content)

	// Cross-namespace recall (namespace="")
	all, err := mem.Recall(ctx, "user-1", "", "", 10)
	require.NoError(t, err)
	assert.Len(t, all, 3)
}

func TestInMemoryUserMemory_RecallByCategory(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Category: "identity", Content: "Name is Sarah", Confidence: 0.95,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f2", Namespace: "travel", Category: "preference", Content: "Prefers window seats", Confidence: 0.9,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f3", Namespace: "travel", Category: "identity", Content: "Home airport JFK", Confidence: 0.95,
	}))

	identity, err := mem.RecallByCategory(ctx, "user-1", "travel", "identity", 10)
	require.NoError(t, err)
	assert.Len(t, identity, 2)

	// Sorted by confidence descending
	assert.True(t, identity[0].Confidence >= identity[1].Confidence)
}

func TestInMemoryUserMemory_MetadataRoundTrip(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID:    "f1",
		Namespace: "travel",
		Category:  "preference",
		Content:   "User prefers DFW airport",
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable,
		},
		Confidence: 0.95,
	}))

	results, err := mem.Recall(ctx, "user-1", "travel", "DFW", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Metadata)
	assert.Equal(t, core.UserFactLifetimeDurable, results[0].Metadata[core.UserFactMetadataLifetimeKey])
}

func TestInMemoryUserMemory_RecallFiltersExpiredTransientFacts(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()
	old := time.Now().Add(-200 * time.Hour)

	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID:    "t1",
		Namespace: "travel",
		Category:  "context",
		Content:   "User is planning a Maldives trip next month",
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeTransient,
		},
		UpdatedAt: old,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID:    "d1",
		Namespace: "travel",
		Category:  "preference",
		Content:   "User prefers DFW airport",
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeDurable,
		},
	}))

	mem.mu.Lock()
	mem.facts["user-1"][0].UpdatedAt = old
	mem.mu.Unlock()

	results, err := mem.Recall(ctx, "user-1", "travel", "", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "d1", results[0].FactID)
}

func TestInMemoryUserMemory_RecallByCategoryFiltersExpiredTransientFacts(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()
	old := time.Now().Add(-200 * time.Hour)

	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID:    "t1",
		Namespace: "travel",
		Category:  "context",
		Content:   "User is planning a Maldives trip next month",
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeTransient,
		},
		UpdatedAt: old,
	}))
	mem.mu.Lock()
	mem.facts["user-1"][0].UpdatedAt = old
	mem.mu.Unlock()

	results, err := mem.RecallByCategory(ctx, "user-1", "travel", "context", 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestInMemoryUserMemory_TransientFactUpdatedAtRefreshExtendsLifetime(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()
	stale := time.Now().Add(-200 * time.Hour)

	fact := core.UserFact{
		FactID:    "t1",
		Namespace: "travel",
		Category:  "context",
		Content:   "User is planning a Maldives trip next month",
		Metadata: map[string]string{
			core.UserFactMetadataLifetimeKey: core.UserFactLifetimeTransient,
		},
		UpdatedAt: stale,
	}
	require.NoError(t, mem.Remember(ctx, "user-1", fact))
	mem.mu.Lock()
	mem.facts["user-1"][0].UpdatedAt = stale
	mem.mu.Unlock()

	results, err := mem.Recall(ctx, "user-1", "travel", "", 10)
	require.NoError(t, err)
	assert.Empty(t, results)

	require.NoError(t, mem.Remember(ctx, "user-1", fact))
	results, err = mem.Recall(ctx, "user-1", "travel", "", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "t1", results[0].FactID)
}

func TestInMemoryUserMemory_UserIsolation(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Content: "User 1 prefers window", Confidence: 0.9,
	}))
	require.NoError(t, mem.Remember(ctx, "user-2", core.UserFact{
		FactID: "f2", Namespace: "travel", Content: "User 2 prefers aisle", Confidence: 0.9,
	}))

	// User 1 cannot see user 2's facts
	user1Facts, err := mem.Recall(ctx, "user-1", "", "", 10)
	require.NoError(t, err)
	assert.Len(t, user1Facts, 1)
	assert.Equal(t, "User 1 prefers window", user1Facts[0].Content)

	// User 2 cannot see user 1's facts
	user2Facts, err := mem.Recall(ctx, "user-2", "", "", 10)
	require.NoError(t, err)
	assert.Len(t, user2Facts, 1)
	assert.Equal(t, "User 2 prefers aisle", user2Facts[0].Content)
}

func TestInMemoryUserMemory_Forget_GDPR(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	// Store facts in multiple namespaces
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Content: "Fact 1", Confidence: 0.9,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f2", Namespace: "devops", Content: "Fact 2", Confidence: 0.9,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f3", Namespace: "universal", Content: "Fact 3", Confidence: 0.9,
	}))

	// Forget all
	require.NoError(t, mem.Forget(ctx, "user-1"))

	// Verify complete deletion — no residual data
	results, err := mem.Recall(ctx, "user-1", "", "", 10)
	require.NoError(t, err)
	assert.Empty(t, results)

	// ListFacts should also return empty
	facts, total, err := mem.ListFacts(ctx, "user-1", "", 0, 100)
	require.NoError(t, err)
	assert.Empty(t, facts)
	assert.Equal(t, 0, total)
}

func TestInMemoryUserMemory_ForgetNamespace(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Namespace: "travel", Content: "Travel fact", Confidence: 0.9,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f2", Namespace: "devops", Content: "Devops fact", Confidence: 0.9,
	}))

	// Forget only travel namespace
	require.NoError(t, mem.ForgetNamespace(ctx, "user-1", "travel"))

	results, err := mem.Recall(ctx, "user-1", "", "", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Devops fact", results[0].Content)
}

func TestInMemoryUserMemory_ForgetFact(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Content: "Fact 1", Confidence: 0.9,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f2", Content: "Fact 2", Confidence: 0.9,
	}))

	require.NoError(t, mem.ForgetFact(ctx, "user-1", "f1"))

	results, err := mem.Recall(ctx, "user-1", "", "", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "f2", results[0].FactID)
}

func TestInMemoryUserMemory_ListFacts_Pagination(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
			FactID: fmt.Sprintf("f%d", i), Namespace: "travel", Content: fmt.Sprintf("Fact %d", i), Confidence: 0.9,
		}))
	}

	// Page 1
	page1, total, err := mem.ListFacts(ctx, "user-1", "travel", 0, 3)
	require.NoError(t, err)
	assert.Equal(t, 10, total)
	assert.Len(t, page1, 3)

	// Page 2
	page2, total, err := mem.ListFacts(ctx, "user-1", "travel", 3, 3)
	require.NoError(t, err)
	assert.Equal(t, 10, total)
	assert.Len(t, page2, 3)

	// Beyond end
	beyond, total, err := mem.ListFacts(ctx, "user-1", "travel", 20, 3)
	require.NoError(t, err)
	assert.Equal(t, 10, total)
	assert.Empty(t, beyond)
}

func TestInMemoryUserMemory_EvictionOnCapacity(t *testing.T) {
	mem := NewInMemoryUserMemory(3) // max 3 facts per user
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
			FactID: fmt.Sprintf("f%d", i), Content: fmt.Sprintf("Fact %d", i), Confidence: 0.9,
		}))
	}

	// Should have 3 facts (oldest 2 evicted)
	results, err := mem.Recall(ctx, "user-1", "", "", 10)
	require.NoError(t, err)
	assert.Len(t, results, 3)
	// Oldest (f0, f1) should be evicted; f2, f3, f4 remain
	factIDs := make([]string, len(results))
	for i, f := range results {
		factIDs[i] = f.FactID
	}
	assert.NotContains(t, factIDs, "f0")
	assert.NotContains(t, factIDs, "f1")
}

func TestInMemoryUserMemory_RecallSortOrder(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f1", Content: "Low confidence", Confidence: 0.5,
	}))
	time.Sleep(time.Millisecond) // ensure different UpdatedAt
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f2", Content: "High confidence", Confidence: 0.95,
	}))
	require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
		FactID: "f3", Content: "Also high confidence", Confidence: 0.95,
	}))

	results, err := mem.Recall(ctx, "user-1", "", "", 10)
	require.NoError(t, err)
	require.Len(t, results, 3)

	// Highest confidence first
	assert.Equal(t, 0.95, results[0].Confidence)
	assert.Equal(t, 0.95, results[1].Confidence)
	assert.Equal(t, 0.5, results[2].Confidence)

	// Among same-confidence, most recently updated first
	assert.True(t, results[0].UpdatedAt.After(results[1].UpdatedAt) || results[0].UpdatedAt.Equal(results[1].UpdatedAt))
}

func TestInMemoryUserMemory_Limit(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		require.NoError(t, mem.Remember(ctx, "user-1", core.UserFact{
			FactID: fmt.Sprintf("f%d", i), Content: fmt.Sprintf("Fact %d", i), Confidence: 0.9,
		}))
	}

	results, err := mem.Recall(ctx, "user-1", "", "", 3)
	require.NoError(t, err)
	assert.Len(t, results, 3)
}

func TestInMemoryUserMemory_ConcurrentAccess(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	var wg sync.WaitGroup
	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = mem.Remember(ctx, "user-1", core.UserFact{
				FactID:     fmt.Sprintf("f%d", i),
				Namespace:  "travel",
				Content:    fmt.Sprintf("Fact %d", i),
				Confidence: 0.9,
			})
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = mem.Recall(ctx, "user-1", "", "", 10)
		}()
	}

	wg.Wait()

	// Should not panic — data integrity verified by no race detector errors
	results, err := mem.Recall(ctx, "user-1", "", "", 100)
	require.NoError(t, err)
	assert.True(t, len(results) > 0, "Should have stored facts")
}

func TestInMemoryUserMemory_EmptyUser(t *testing.T) {
	mem := NewInMemoryUserMemory(0)
	ctx := context.Background()

	results, err := mem.Recall(ctx, "nonexistent", "", "", 10)
	require.NoError(t, err)
	assert.Empty(t, results)

	results, err = mem.RecallByCategory(ctx, "nonexistent", "", "preference", 10)
	require.NoError(t, err)
	assert.Empty(t, results)

	facts, total, err := mem.ListFacts(ctx, "nonexistent", "", 0, 10)
	require.NoError(t, err)
	assert.Empty(t, facts)
	assert.Equal(t, 0, total)

	// Forget on nonexistent user should not error
	assert.NoError(t, mem.Forget(ctx, "nonexistent"))
}

// Compile-time interface compliance checks
var _ core.UserMemory = (*InMemoryUserMemory)(nil)
var _ core.UserMemoryAdmin = (*InMemoryUserMemory)(nil)
var _ core.UserMemory = (*core.NoOpUserMemory)(nil)
var _ core.UserMemoryAdmin = (*core.NoOpUserMemory)(nil)
