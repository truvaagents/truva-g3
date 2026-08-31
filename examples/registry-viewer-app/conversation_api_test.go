package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newConversationAPIRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func storeViewerExecution(
	t *testing.T,
	client *redis.Client,
	execution StoredExecution,
) {
	t.Helper()
	data, err := json.Marshal(execution)
	if err != nil {
		t.Fatalf("marshal execution: %v", err)
	}
	if err := client.Set(
		context.Background(),
		executionKeyPrefix+execution.RequestID,
		data,
		time.Hour,
	).Err(); err != nil {
		t.Fatalf("store execution: %v", err)
	}
}

func TestBuildGroupedExecutionUnitsMatchesRelatedResumeRequestID(t *testing.T) {
	createdAt := time.Date(2026, time.August, 27, 15, 35, 0, 0, time.UTC)
	const (
		originalRequestID = "workflow-original-1787844868392776305"
		resumeRequestID   = "workflow-resume-1787844987556516340"
	)
	summaries := []ExecutionSummary{
		{
			RequestID:       originalRequestID,
			OriginalRequest: "Investigate delayed operation",
			Interrupted:     true,
			CreatedAt:       createdAt,
		},
		{
			RequestID:         resumeRequestID,
			OriginalRequestID: originalRequestID,
			OriginalRequest:   "Investigate delayed operation",
			Success:           true,
			CreatedAt:         createdAt.Add(time.Minute),
		},
	}
	query := &groupedExecutionQuery{
		Search:    resumeRequestID,
		Status:    "all",
		Sort:      "created_at",
		Direction: "desc",
	}

	groups := buildGroupedExecutionUnits(summaries, nil, nil, query)

	if len(groups) != 1 {
		t.Fatalf("group count = %d, want 1", len(groups))
	}
	if len(groups[0].Turns) != 1 {
		t.Fatalf("turn count = %d, want 1", len(groups[0].Turns))
	}
	turn := groups[0].Turns[0]
	if turn.Execution.RequestID != originalRequestID {
		t.Fatalf("owner request ID = %q, want %q", turn.Execution.RequestID, originalRequestID)
	}
	if len(turn.RelatedExecutions) != 1 ||
		turn.RelatedExecutions[0].RequestID != resumeRequestID {
		t.Fatalf("related executions = %#v, want resume %q", turn.RelatedExecutions, resumeRequestID)
	}
}

func TestBuildGroupedExecutionUnitsDoesNotMatchUnrelatedChild(t *testing.T) {
	createdAt := time.Date(2026, time.August, 27, 15, 35, 0, 0, time.UTC)
	summaries := []ExecutionSummary{
		{
			RequestID:       "original-request",
			OriginalRequest: "Investigate delayed operation",
			Interrupted:     true,
			CreatedAt:       createdAt,
		},
		{
			RequestID:         "resume-request",
			OriginalRequestID: "original-request",
			OriginalRequest:   "Investigate delayed operation",
			Success:           true,
			CreatedAt:         createdAt.Add(time.Minute),
		},
	}
	query := &groupedExecutionQuery{
		Search:    "some-other-request",
		Status:    "all",
		Sort:      "created_at",
		Direction: "desc",
	}

	groups := buildGroupedExecutionUnits(summaries, nil, nil, query)

	if len(groups) != 0 {
		t.Fatalf("group count = %d, want 0", len(groups))
	}
}

func TestExactOwnerHydrationAttachesOwnerOutsideInitialSnapshot(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	ctx := context.Background()
	createdAt := time.Date(2026, time.August, 28, 1, 0, 0, 0, time.UTC)
	storeViewerExecution(t, client, StoredExecution{
		RequestID:         "exact-owner",
		OriginalRequestID: "exact-owner",
		OriginalRequest:   "Initial request",
		CreatedAt:         createdAt,
	})
	summaries := map[string]ExecutionSummary{
		"related-child": {
			RequestID:         "related-child",
			OriginalRequestID: "exact-owner",
			OriginalRequest:   "Resume request",
			CreatedAt:         createdAt.Add(time.Minute),
		},
	}
	partial, err := hydrateAndClassifyGroupedExecutionOwners(
		ctx,
		client,
		newExecutionReadCache(),
		summaries,
	)
	if err != nil || partial {
		t.Fatalf("owner hydration = (partial=%v, err=%v)", partial, err)
	}
	if _, exists := summaries["exact-owner"]; !exists {
		t.Fatal("exact owner was not hydrated")
	}

	groups := buildGroupedExecutionUnits(summariesToSlice(summaries), nil, nil, &groupedExecutionQuery{
		Status: "all", Sort: "created_at", Direction: "desc", Limit: 50,
	})
	if len(groups) != 1 || len(groups[0].Turns) != 1 ||
		len(groups[0].Turns[0].RelatedExecutions) != 1 {
		t.Fatalf("exact owner lineage was not attached: %#v", groups)
	}
}

func TestOwnerClassificationDistinguishesUnavailableFromUnknown(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	ctx := context.Background()
	newSummaries := func() map[string]ExecutionSummary {
		return map[string]ExecutionSummary{
			"child": {
				RequestID:         "child",
				OriginalRequestID: "absent-owner",
				CreatedAt:         time.Now(),
			},
		}
	}

	confirmed := newSummaries()
	partial, err := hydrateAndClassifyGroupedExecutionOwners(
		ctx,
		client,
		newExecutionReadCache(),
		confirmed,
	)
	if err != nil || partial {
		t.Fatalf("confirmed missing classification = (partial=%v, err=%v)", partial, err)
	}
	if got := confirmed["child"]; got.RelationStatus != "owner_unavailable" ||
		got.MissingOwnerID != "absent-owner" {
		t.Fatalf("confirmed missing summary = %#v", got)
	}

	unknown := newSummaries()
	cache := newExecutionReadCache()
	cache.exactHydrated = viewerExactOwnerHydrationLimit
	partial, err = hydrateAndClassifyGroupedExecutionOwners(ctx, client, cache, unknown)
	if err != nil || !partial {
		t.Fatalf("bounded owner classification = (partial=%v, err=%v)", partial, err)
	}
	if got := unknown["child"]; got.RelationStatus != "owner_unknown" ||
		got.MissingOwnerID != "" {
		t.Fatalf("unknown owner summary = %#v", got)
	}
}

func TestConfirmedMissingIndexCleanupPreservesUnreadableRecords(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	ctx := context.Background()
	for index, requestID := range []string{"missing", "unreadable"} {
		if err := client.ZAdd(ctx, executionIndexKey, redis.Z{
			Score: float64(index), Member: requestID,
		}).Err(); err != nil {
			t.Fatalf("seed index: %v", err)
		}
	}
	if err := client.Set(ctx, executionKeyPrefix+"unreadable", "not-json", time.Hour).Err(); err != nil {
		t.Fatalf("seed unreadable record: %v", err)
	}
	cache := newExecutionReadCache()
	partial, err := cache.load(ctx, client, []string{"missing", "unreadable"})
	if err != nil || !partial {
		t.Fatalf("cache load = (partial=%v, err=%v)", partial, err)
	}
	pruneConfirmedMissingIndexMembers(
		ctx,
		client,
		executionIndexKey,
		[]string{"missing", "unreadable"},
		cache.missing,
	)
	if _, err := client.ZScore(ctx, executionIndexKey, "missing").Result(); err != redis.Nil {
		t.Fatal("confirmed missing member remains indexed")
	}
	if _, err := client.ZScore(ctx, executionIndexKey, "unreadable").Result(); err != nil {
		t.Fatal("unreadable but existing record was incorrectly pruned")
	}
}

func TestBoundedMaintenanceEventuallyPrunesOldStaleMembers(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	ctx := context.Background()
	for index := 0; index < viewerStaleIndexMaintenanceBatch*3; index++ {
		requestID := fmt.Sprintf("stale-%04d", index)
		if err := client.ZAdd(ctx, executionIndexKey, redis.Z{
			Score: float64(index), Member: requestID,
		}).Err(); err != nil {
			t.Fatalf("seed stale index: %v", err)
		}
	}
	staleIndexSweepMu.Lock()
	staleIndexSweepCursor = 0
	staleIndexSweepLastRun = time.Time{}
	staleIndexSweepMu.Unlock()
	for attempt := 0; attempt < 20; attempt++ {
		maintainStaleGlobalExecutionIndex(ctx, client)
		remaining, err := client.ZCard(ctx, executionIndexKey).Result()
		if err != nil {
			t.Fatalf("count stale index: %v", err)
		}
		if remaining == 0 {
			break
		}
		staleIndexSweepMu.Lock()
		staleIndexSweepLastRun = time.Time{}
		staleIndexSweepMu.Unlock()
	}
	if remaining, err := client.ZCard(ctx, executionIndexKey).Result(); err != nil || remaining != 0 {
		t.Fatalf("stale members remaining after bounded sweeps = %d", remaining)
	}
}

func TestStaleIndexMaintenanceIsRateLimited(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	ctx := context.Background()
	staleIndexSweepMu.Lock()
	staleIndexSweepCursor = 0
	staleIndexSweepLastRun = time.Time{}
	staleIndexSweepMu.Unlock()
	t.Cleanup(func() {
		staleIndexSweepMu.Lock()
		staleIndexSweepCursor = 0
		staleIndexSweepLastRun = time.Time{}
		staleIndexSweepMu.Unlock()
	})

	if err := client.ZAdd(ctx, executionIndexKey, redis.Z{
		Score: 1, Member: "first-stale",
	}).Err(); err != nil {
		t.Fatalf("seed first stale member: %v", err)
	}
	maintainStaleGlobalExecutionIndex(ctx, client)
	if _, err := client.ZScore(ctx, executionIndexKey, "first-stale").Result(); err != redis.Nil {
		t.Fatalf("eligible sweep did not remove first stale member: %v", err)
	}

	if err := client.ZAdd(ctx, executionIndexKey, redis.Z{
		Score: 2, Member: "second-stale",
	}).Err(); err != nil {
		t.Fatalf("seed second stale member: %v", err)
	}
	maintainStaleGlobalExecutionIndex(ctx, client)
	if _, err := client.ZScore(ctx, executionIndexKey, "second-stale").Result(); err != nil {
		t.Fatalf("rate-limited sweep unexpectedly removed second member: %v", err)
	}

	staleIndexSweepMu.Lock()
	staleIndexSweepLastRun = time.Now().Add(-viewerStaleIndexMaintenanceInterval)
	staleIndexSweepMu.Unlock()
	maintainStaleGlobalExecutionIndex(ctx, client)
	if _, err := client.ZScore(ctx, executionIndexKey, "second-stale").Result(); err != redis.Nil {
		t.Fatalf("eligible follow-up sweep did not remove second stale member: %v", err)
	}
}

func summariesToSlice(summaries map[string]ExecutionSummary) []ExecutionSummary {
	result := make([]ExecutionSummary, 0, len(summaries))
	for _, summary := range summaries {
		result = append(result, summary)
	}
	return result
}
