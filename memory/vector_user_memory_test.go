package memory

import (
	"encoding/json"
	"testing"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
	"github.com/stretchr/testify/assert"
	"github.com/truvaagents/truva-g3/core"
)

// VectorUserMemory integration tests require a running Qdrant instance.
// Run with: go test ./memory/... -run TestVectorUserMemory -tags integration
//
// These tests are skipped by default in CI and local development.
// To run them locally:
//   1. Start Qdrant: docker run -p 6334:6334 qdrant/qdrant
//   2. Set TRUVAG3_VECTOR_DB_URL=localhost:6334
//   3. Run: go test ./memory/... -run TestVectorUserMemory -count=1

func skipIfNoQdrant(t *testing.T) {
	t.Helper()
	t.Skip("Skipping VectorUserMemory test: requires running Qdrant instance (set TRUVAG3_VECTOR_DB_URL)")
}

func TestVectorUserMemory_RememberAndRecall(t *testing.T) {
	skipIfNoQdrant(t)
	// TODO: Integration test — Remember a fact, Recall by query, verify content
}

func TestVectorUserMemory_UserIsolation(t *testing.T) {
	skipIfNoQdrant(t)
	// TODO: Integration test — Store facts for user-1 and user-2, verify isolation
}

func TestVectorUserMemory_Forget_GDPR(t *testing.T) {
	skipIfNoQdrant(t)
	// TODO: Integration test — Forget user, verify all facts deleted
}

func TestVectorUserMemory_ForgetFact_UserScoped(t *testing.T) {
	skipIfNoQdrant(t)
	// TODO: Integration test — ForgetFact verifies user_id ownership via filter
}

func TestVectorUserMemory_NamespaceFiltering(t *testing.T) {
	skipIfNoQdrant(t)
	// TODO: Integration test — Facts in different namespaces, verify filtering
}

func TestVectorUserMemory_RecallByCategory(t *testing.T) {
	skipIfNoQdrant(t)
	// TODO: Integration test — RecallByCategory returns correct category
}

func TestVectorUserMemory_ListFacts_Pagination(t *testing.T) {
	skipIfNoQdrant(t)
	// TODO: Integration test — ListFacts with offset/limit
}

func TestVectorUserMemory_UseCountPreservedOnUpsert(t *testing.T) {
	skipIfNoQdrant(t)
	// TODO: Integration test — Remember with same FactID preserves use_count
}

func TestTruncateScoredPoints(t *testing.T) {
	points := []*pb.ScoredPoint{
		{Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "1"}}},
		{Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "2"}}},
		{Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "3"}}},
	}

	assert.Len(t, truncateScoredPoints(points, 2), 2)
	assert.Len(t, truncateScoredPoints(points, 3), 3)
	assert.Len(t, truncateScoredPoints(points, 0), 3)
}

func TestVectorRecall_OverFetchPreservesRequestedLimitAfterFiltering(t *testing.T) {
	now := time.Now()
	points := []*pb.ScoredPoint{
		scoredPointWithLifetime("t1", core.UserFactLifetimeTransient, now.Add(-200*time.Hour)),
		scoredPointWithLifetime("d1", core.UserFactLifetimeDurable, now.Add(-10*time.Hour)),
		scoredPointWithLifetime("d2", core.UserFactLifetimeDurable, now.Add(-5*time.Hour)),
	}

	filteredPoints, filteredFacts, filteredCount := filterScoredPointsByLifetime(points, now, 168*time.Hour)
	assert.Equal(t, 1, filteredCount)
	assert.Len(t, filteredPoints, 2)
	assert.Len(t, filteredFacts, 2)
	assert.Len(t, truncateScoredPoints(filteredPoints, 2), 2)
}

func TestCollectCategoryFactsUntilLimit_BackfillsAfterTransientFiltering(t *testing.T) {
	now := time.Now()
	page1 := []core.UserFact{
		{
			FactID:    "t1",
			Category:  "context",
			Content:   "expired",
			UpdatedAt: now.Add(-200 * time.Hour),
			Metadata: map[string]string{
				core.UserFactMetadataLifetimeKey: core.UserFactLifetimeTransient,
			},
		},
		{
			FactID:    "d1",
			Category:  "context",
			Content:   "fresh-1",
			UpdatedAt: now.Add(-2 * time.Hour),
		},
	}
	page2 := []core.UserFact{
		{
			FactID:    "d2",
			Category:  "context",
			Content:   "fresh-2",
			UpdatedAt: now.Add(-1 * time.Hour),
		},
	}

	pageCalls := 0
	facts, filteredCount, err := collectCategoryFactsUntilLimit(func(offset *pb.PointId) ([]core.UserFact, *pb.PointId, error) {
		pageCalls++
		switch pageCalls {
		case 1:
			return page1, &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "page-2"}}, nil
		case 2:
			return page2, nil, nil
		default:
			return nil, nil, nil
		}
	}, 2, now, 168*time.Hour)

	assert.NoError(t, err)
	assert.Equal(t, 1, filteredCount)
	assert.Len(t, facts, 2)
	assert.Equal(t, 2, pageCalls)
	assert.Equal(t, "d1", facts[0].FactID)
	assert.Equal(t, "d2", facts[1].FactID)
}

func scoredPointWithLifetime(id string, lifetime string, updatedAt time.Time) *pb.ScoredPoint {
	metaJSON, _ := json.Marshal(map[string]string{
		core.UserFactMetadataLifetimeKey: lifetime,
	})

	return &pb.ScoredPoint{
		Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: id}},
		Payload: map[string]*pb.Value{
			"user_id":    pb.NewValueString("user-1"),
			"namespace":  pb.NewValueString("travel"),
			"category":   pb.NewValueString("context"),
			"content":    pb.NewValueString(id),
			"source":     pb.NewValueString(string(core.SourceExplicit)),
			"confidence": pb.NewValueDouble(0.95),
			"created_at": pb.NewValueInt(updatedAt.Unix()),
			"updated_at": pb.NewValueInt(updatedAt.Unix()),
			"metadata":   pb.NewValueString(string(metaJSON)),
		},
	}
}

// Compile-time interface compliance checks.
var _ core.UserMemory = (*VectorUserMemory)(nil)
var _ core.UserMemoryAdmin = (*VectorUserMemory)(nil)
