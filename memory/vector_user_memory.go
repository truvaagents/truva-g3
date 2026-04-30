package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Compile-time interface compliance checks.
var _ core.UserMemory = (*VectorUserMemory)(nil)
var _ core.UserMemoryAdmin = (*VectorUserMemory)(nil)

// VectorUserMemory implements core.UserMemory and core.UserMemoryAdmin using
// a vector database (Qdrant reference implementation).
//
// Design constraints (per memory/ARCHITECTURE.md §7):
//   - Stateless: no in-memory caches — all state in the vector DB
//   - Thread-safe: gRPC client is safe for concurrent use
//   - Fail-open at runtime: Recall returns (nil, nil) on failure
//   - Fail-fast at startup: constructor returns error if unreachable
//   - User isolation: enforced at storage level via user_id payload filter on every query
//
// Embedding generation is owned by the backend (not the caller). This differs from
// SharedKnowledge because UserFact has no Embedding field — the core struct stays lean.
// Each backend controls its own embedding strategy. The embedder is a core.EmbeddingClient
// interface — no ai module import.
type VectorUserMemory struct {
	conn            *grpc.ClientConn
	pointsClient    pb.PointsClient
	embedder        core.EmbeddingClient
	config          *VectorConfig
	overfetchMul    int
	transientMaxAge time.Duration
	wg              sync.WaitGroup // Tracks background goroutines for graceful shutdown
}

// NewVectorUserMemory creates a vector DB-backed user memory store.
// Reuses VectorConfig and Option from vector_config.go for connection settings.
//
// embedder is required — used for Remember() storage and Recall() semantic search.
// Fail-fast: returns error if embedder is nil or vector DB is unreachable.
//
// The collection name defaults to TRUVAG3_USER_MEMORY_COLLECTION env var, falling back
// to "truvag3_user_memory". Use WithCollectionName() to override explicitly.
func NewVectorUserMemory(embedder core.EmbeddingClient, opts ...Option) (*VectorUserMemory, error) {
	if embedder == nil {
		return nil, fmt.Errorf("user memory: embedding client is required for vector backend")
	}

	// Start from shared defaults, then apply user memory-specific overrides
	config := defaultConfig()

	// User memory collection name: separate from shared knowledge
	config.CollectionName = "truvag3_user_memory"
	if v := os.Getenv("TRUVAG3_USER_MEMORY_COLLECTION"); v != "" {
		config.CollectionName = v
	}

	// Apply explicit options (highest priority — same Option type as VectorSharedKnowledge)
	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, fmt.Errorf("user memory option: %w", err)
		}
	}

	// Component-aware logger wrapping (per LOGGING_IMPLEMENTATION_GUIDE §Component-Aware)
	if cal, ok := config.Logger.(core.ComponentAwareLogger); ok {
		config.Logger = cal.WithComponent("framework/memory")
	}

	// Connect to vector DB — FAIL-FAST if unreachable
	conn, err := grpc.NewClient(config.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("user memory: failed to connect to vector DB at %s: %w (check TRUVAG3_VECTOR_DB_URL)", config.Address, err)
	}

	vum := &VectorUserMemory{
		conn:            conn,
		pointsClient:    pb.NewPointsClient(conn),
		embedder:        embedder,
		config:          config,
		overfetchMul:    recallOverfetchMultiplierFromEnv(),
		transientMaxAge: transientMaxAgeFromEnv(),
	}

	// Auto-create collection if configured
	if config.AutoCreateCollection {
		if err := vum.ensureCollection(context.Background()); err != nil {
			config.Logger.Warn("Failed to auto-create user memory collection", map[string]interface{}{
				"operation":  "user_memory_init",
				"collection": config.CollectionName,
				"error":      err.Error(),
			})
			// Don't fail — collection may already exist or be created externally
		}
	}

	config.Logger.Info("VectorUserMemory initialized", map[string]interface{}{
		"operation":   "user_memory_init",
		"address":     config.Address,
		"collection":  config.CollectionName,
		"vector_size": config.VectorSize,
	})

	return vum, nil
}

// ensureCollection creates the user memory collection with indexed payload fields
// if it doesn't already exist.
func (v *VectorUserMemory) ensureCollection(ctx context.Context) error {
	collectionsClient := pb.NewCollectionsClient(v.conn)

	// Check if collection exists
	_, err := collectionsClient.Get(ctx, &pb.GetCollectionInfoRequest{
		CollectionName: v.config.CollectionName,
	})
	if err == nil {
		return nil // Collection already exists
	}

	// Create collection
	distance := pb.Distance_Cosine
	if v.config.VectorSize < 0 {
		return fmt.Errorf("user memory: vector size must be non-negative, got %d", v.config.VectorSize)
	}
	vectorSize := uint64(v.config.VectorSize) //nolint:gosec // validated non-negative above
	_, err = collectionsClient.Create(ctx, &pb.CreateCollection{
		CollectionName: v.config.CollectionName,
		VectorsConfig: &pb.VectorsConfig{
			Config: &pb.VectorsConfig_Params{
				Params: &pb.VectorParams{
					Size:     vectorSize,
					Distance: distance,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create collection %s: %w", v.config.CollectionName, err)
	}

	// Create payload indexes for filtered queries
	indexFields := []string{"user_id", "namespace", "category", "fact_id"}
	for _, field := range indexFields {
		_, err = v.pointsClient.CreateFieldIndex(ctx, &pb.CreateFieldIndexCollection{
			CollectionName: v.config.CollectionName,
			FieldName:      field,
			FieldType:      pb.FieldType_FieldTypeKeyword.Enum(),
		})
		if err != nil {
			v.config.Logger.Warn("Failed to create payload index", map[string]interface{}{
				"operation": "user_memory_init",
				"field":     field,
				"error":     err.Error(),
			})
		}
	}

	return nil
}

// Remember upserts a fact. Generates embedding for the fact content,
// stores as a point with user_id, namespace, category as indexed payload.
// Returns error on failure — the calling hook logs and continues (non-fatal).
func (v *VectorUserMemory) Remember(ctx context.Context, userID string, fact core.UserFact) error {
	start := time.Now()
	defer func() {
		telemetry.Histogram("user_memory.remember.duration_ms", float64(time.Since(start).Milliseconds()),
			"module", "memory")
	}()

	// Generate embedding
	embResp, err := v.embedder.GenerateEmbeddings(ctx, []string{fact.Content}, nil)
	if err != nil {
		return fmt.Errorf("user memory: embedding generation failed: %w", err)
	}
	if len(embResp.Embeddings) == 0 {
		return fmt.Errorf("user memory: embedding returned empty result")
	}

	// Assign FactID if empty
	if fact.FactID == "" {
		fact.FactID = uuid.New().String()
	}
	now := time.Now()
	if fact.CreatedAt.IsZero() {
		fact.CreatedAt = now
	}
	fact.UpdatedAt = now
	fact.UserID = userID

	// Build point with payload
	pointID := pb.NewIDUUID(fact.FactID)
	vectors := pb.NewVectors(embResp.Embeddings[0]...)

	// Preserve existing use_count on upsert (UPDATE reconciliation reuses FactID).
	// Default to 0 for new facts; read existing value if point already exists.
	useCount := int64(0)
	existingPoints, err := v.pointsClient.Get(ctx, &pb.GetPoints{
		CollectionName: v.config.CollectionName,
		Ids:            []*pb.PointId{pointID},
		WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
	})
	if err == nil && len(existingPoints.Result) > 0 {
		if val, ok := existingPoints.Result[0].Payload["use_count"]; ok {
			useCount = val.GetIntegerValue()
		}
	}

	payload := map[string]*pb.Value{
		"user_id":      pb.NewValueString(userID),
		"fact_id":      pb.NewValueString(fact.FactID), // indexed for ForgetFact filter
		"namespace":    pb.NewValueString(fact.Namespace),
		"category":     pb.NewValueString(fact.Category),
		"content":      pb.NewValueString(fact.Content),
		"source":       pb.NewValueString(string(fact.Source)),
		"confidence":   pb.NewValueDouble(fact.Confidence),
		"created_at":   pb.NewValueInt(fact.CreatedAt.Unix()),
		"updated_at":   pb.NewValueInt(fact.UpdatedAt.Unix()),
		"use_count":    pb.NewValueInt(useCount),
		"last_used_at": pb.NewValueInt(now.Unix()),
	}

	// Store metadata as JSON string
	if len(fact.Metadata) > 0 {
		metaJSON, _ := json.Marshal(fact.Metadata)
		payload["metadata"] = pb.NewValueString(string(metaJSON))
	}

	// Upsert
	_, err = v.pointsClient.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: v.config.CollectionName,
		Points: []*pb.PointStruct{
			{Id: pointID, Vectors: vectors, Payload: payload},
		},
	})
	if err != nil {
		return fmt.Errorf("user memory: upsert failed: %w", err)
	}

	telemetry.Counter("user_memory.remember.success", "module", "memory")
	return nil
}

// Recall performs semantic search filtered by user_id and optional namespace.
// Fail-open: returns (nil, nil) on infrastructure failure — never blocks the pipeline.
func (v *VectorUserMemory) Recall(ctx context.Context, userID string, namespace string, queryContext string, limit int) ([]core.UserFact, error) {
	start := time.Now()
	requestID := core.GetRequestID(ctx)
	defer func() {
		telemetry.Histogram("user_memory.recall.duration_ms", float64(time.Since(start).Milliseconds()),
			"module", "memory", "namespace", namespace)
	}()

	if limit <= 0 {
		limit = 10
	}

	// Generate query embedding
	embResp, err := v.embedder.GenerateEmbeddings(ctx, []string{queryContext}, nil)
	if err != nil {
		v.config.Logger.WarnWithContext(ctx, "User memory recall embedding failed, returning empty", map[string]interface{}{
			"operation": "user_memory_recall", "error": err.Error(), "error_type": "embedding", "user_id": userID,
		})
		return nil, nil // FAIL-OPEN
	}
	if len(embResp.Embeddings) == 0 {
		return nil, nil
	}

	// Build filter: user_id + optional namespace
	filter := userIDFilter(userID)
	if namespace != "" {
		addCondition(filter, "namespace", namespace)
	}

	// Search with timeout
	searchCtx, cancel := context.WithTimeout(ctx, v.config.SearchTimeout)
	defer cancel()

	searchLimit := safeOverfetchLimit(limit, v.overfetchMul)
	results, err := v.pointsClient.Search(searchCtx, &pb.SearchPoints{
		CollectionName: v.config.CollectionName,
		Vector:         embResp.Embeddings[0],
		Filter:         filter,
		Limit:          searchLimit,
		WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
	})
	if err != nil {
		v.config.Logger.WarnWithContext(ctx, "User memory recall search failed, returning empty", map[string]interface{}{
			"operation": "user_memory_recall", "error": err.Error(), "error_type": "user_memory_read",
			"user_id": userID, "namespace": namespace,
		})
		return nil, nil // FAIL-OPEN
	}

	filteredPoints, filteredFacts, filteredCount := filterScoredPointsByLifetime(results.Result, time.Now(), v.transientMaxAge)
	if filteredCount > 0 {
		telemetry.AddSpanEvent(ctx, "user_memory.transient_cleanup.filtered",
			attribute.String("request_id", requestID),
			attribute.String("user_id", userID),
			attribute.String("namespace", namespace),
			attribute.Int("filtered_count", filteredCount),
			attribute.Int64("duration_ms", time.Since(start).Milliseconds()),
			attribute.Int64("transient_max_age_hours", int64(v.transientMaxAge/time.Hour)),
		)
	}
	returnedPoints := truncateScoredPoints(filteredPoints, limit)
	if limit > 0 && len(filteredFacts) > limit {
		filteredFacts = filteredFacts[:limit]
	}

	// Update use_count and last_used_at (async, fire-and-forget).
	// Uses a detached context with timeout — must outlive the request context
	// (which may be cancelled after the HTTP handler returns) but should not
	// run indefinitely. Same pattern as LLMEventSummarizer.recordDebugInteraction.
	if len(returnedPoints) > 0 {
		v.wg.Add(1)
		go func() { // #nosec G118 -- intentional detached context, must outlive request
			defer v.wg.Done()
			trackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			v.updateAccessTracking(trackCtx, returnedPoints)
		}()
	}

	// Convert scored points → UserFact
	return filteredFacts, nil
}

// RecallByCategory retrieves facts in a specific category, ordered by created_at descending
// (most recent first). This ordering is critical for summaries where all facts have the
// same confidence and the most recent N should be returned.
// Fail-open: returns (nil, nil) on infrastructure failure.
func (v *VectorUserMemory) RecallByCategory(ctx context.Context, userID string, namespace string, category string, limit int) ([]core.UserFact, error) {
	start := time.Now()
	requestID := core.GetRequestID(ctx)
	if limit <= 0 {
		limit = 10
	}

	filter := userIDFilter(userID)
	if namespace != "" {
		addCondition(filter, "namespace", namespace)
	}
	addCondition(filter, "category", category)

	scrollCtx, cancel := context.WithTimeout(ctx, v.config.SearchTimeout)
	defer cancel()

	pageSize := categoryRecallPageSize(limit, v.overfetchMul)
	desc := pb.Direction_Desc
	orderedFetchPage := func(offset *pb.PointId) ([]core.UserFact, *pb.PointId, error) {
		results, err := v.pointsClient.Scroll(scrollCtx, &pb.ScrollPoints{
			CollectionName: v.config.CollectionName,
			Filter:         filter,
			Limit:          &pageSize,
			Offset:         offset,
			WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
			OrderBy:        &pb.OrderBy{Key: "created_at", Direction: &desc},
		})
		if err != nil {
			return nil, nil, err
		}

		facts := make([]core.UserFact, 0, len(results.Result))
		for _, point := range results.Result {
			facts = append(facts, payloadToUserFact(point.Payload, point.Id.GetUuid()))
		}
		return facts, results.GetNextPageOffset(), nil
	}

	facts, filteredCount, err := collectCategoryFactsUntilLimit(orderedFetchPage, limit, time.Now(), v.transientMaxAge)
	if err != nil {
		errText := err.Error()
		if strings.Contains(errText, "No range index") && strings.Contains(errText, "created_at") {
			v.config.Logger.WarnWithContext(ctx, "User memory recall by category missing created_at index, falling back to unsorted scroll", map[string]interface{}{
				"operation": "user_memory_recall_by_category", "user_id": userID, "namespace": namespace, "category": category,
			})
			return v.recallByCategoryUnsorted(ctx, scrollCtx, userID, namespace, category, filter, limit, start, requestID)
		}
		v.config.Logger.WarnWithContext(ctx, "User memory recall by category failed, returning empty", map[string]interface{}{
			"operation": "user_memory_recall_by_category", "error": err.Error(), "error_type": "user_memory_read",
			"user_id": userID, "category": category,
		})
		return nil, nil // FAIL-OPEN
	}
	if filteredCount > 0 {
		telemetry.AddSpanEvent(ctx, "user_memory.transient_cleanup.filtered",
			attribute.String("request_id", requestID),
			attribute.String("user_id", userID),
			attribute.String("namespace", namespace),
			attribute.String("category", category),
			attribute.Int("filtered_count", filteredCount),
			attribute.Int64("duration_ms", time.Since(start).Milliseconds()),
			attribute.Int64("transient_max_age_hours", int64(v.transientMaxAge/time.Hour)),
		)
	}
	sort.Slice(facts, func(i, j int) bool {
		if !facts[i].CreatedAt.Equal(facts[j].CreatedAt) {
			return facts[i].CreatedAt.After(facts[j].CreatedAt)
		}
		return facts[i].UpdatedAt.After(facts[j].UpdatedAt)
	})
	if limit > 0 && len(facts) > limit {
		facts = facts[:limit]
	}
	return facts, nil
}

func (v *VectorUserMemory) recallByCategoryUnsorted(
	ctx context.Context,
	scrollCtx context.Context,
	userID string,
	namespace string,
	category string,
	filter *pb.Filter,
	limit int,
	start time.Time,
	requestID string,
) ([]core.UserFact, error) {
	pageSize := categoryRecallPageSize(limit, v.overfetchMul)
	facts, filteredCount, err := collectCategoryFactsUntilLimit(func(offset *pb.PointId) ([]core.UserFact, *pb.PointId, error) {
		results, err := v.pointsClient.Scroll(scrollCtx, &pb.ScrollPoints{
			CollectionName: v.config.CollectionName,
			Filter:         filter,
			Limit:          &pageSize,
			Offset:         offset,
			WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
		})
		if err != nil {
			return nil, nil, err
		}

		facts := make([]core.UserFact, 0, len(results.Result))
		for _, point := range results.Result {
			facts = append(facts, payloadToUserFact(point.Payload, point.Id.GetUuid()))
		}
		return facts, results.GetNextPageOffset(), nil
	}, limit, time.Now(), v.transientMaxAge)
	if err != nil {
		v.config.Logger.WarnWithContext(ctx, "User memory recall by category failed during unsorted fallback, returning empty", map[string]interface{}{
			"operation": "user_memory_recall_by_category", "error": err.Error(), "error_type": "user_memory_read",
			"user_id": userID, "namespace": namespace, "category": category,
		})
		return nil, nil // FAIL-OPEN
	}

	sort.Slice(facts, func(i, j int) bool {
		if !facts[i].CreatedAt.Equal(facts[j].CreatedAt) {
			return facts[i].CreatedAt.After(facts[j].CreatedAt)
		}
		return facts[i].UpdatedAt.After(facts[j].UpdatedAt)
	})
	if filteredCount > 0 {
		telemetry.AddSpanEvent(ctx, "user_memory.transient_cleanup.filtered",
			attribute.String("request_id", requestID),
			attribute.String("user_id", userID),
			attribute.String("namespace", namespace),
			attribute.String("category", category),
			attribute.Int("filtered_count", filteredCount),
			attribute.Int64("duration_ms", time.Since(start).Milliseconds()),
			attribute.Int64("transient_max_age_hours", int64(v.transientMaxAge/time.Hour)),
		)
	}
	if limit > 0 && len(facts) > limit {
		facts = facts[:limit]
	}
	return facts, nil
}

// Forget deletes all points for a user across all namespaces. GDPR Article 17.
func (v *VectorUserMemory) Forget(ctx context.Context, userID string) error {
	telemetry.Counter("user_memory.forget.total", "module", "memory")
	_, err := v.pointsClient.Delete(ctx, &pb.DeletePoints{
		CollectionName: v.config.CollectionName,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Filter{Filter: userIDFilter(userID)},
		},
	})
	if err != nil {
		return fmt.Errorf("user memory: forget failed for user %s: %w", userID, err)
	}
	return nil
}

// ListFacts returns all active facts for a user with pagination (GDPR data portability).
func (v *VectorUserMemory) ListFacts(ctx context.Context, userID string, namespace string, offset int, limit int) ([]core.UserFact, int, error) {
	filter := userIDFilter(userID)
	if namespace != "" {
		addCondition(filter, "namespace", namespace)
	}

	// Count total
	exact := true
	countResp, err := v.pointsClient.Count(ctx, &pb.CountPoints{
		CollectionName: v.config.CollectionName,
		Filter:         filter,
		Exact:          &exact,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("user memory: count failed: %w", err)
	}
	count := countResp.Result.Count
	if count > uint64(^uint(0)>>1) { // cap at max int to prevent overflow
		count = uint64(^uint(0) >> 1)
	}
	total := int(count) //nolint:gosec // capped above

	// Scroll with client-side offset. Qdrant's Scroll API does not support native offset,
	// so we fetch offset+limit points and discard the first offset results. This is
	// acceptable for typical pagination (offset < 100) but inefficient for large offsets.
	// For large datasets, consider cursor-based pagination using Scroll's offset_id.
	fetchCount := offset + limit
	if fetchCount < 0 || fetchCount > int(^uint32(0)) { // cap at max uint32
		fetchCount = int(^uint32(0))
	}
	scrollLimit := uint32(fetchCount) //nolint:gosec // capped above
	results, err := v.pointsClient.Scroll(ctx, &pb.ScrollPoints{
		CollectionName: v.config.CollectionName,
		Filter:         filter,
		Limit:          &scrollLimit,
		WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
	})
	if err != nil {
		return nil, 0, fmt.Errorf("user memory: list failed: %w", err)
	}

	// Apply offset
	points := results.Result
	if offset >= len(points) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(points) {
		end = len(points)
	}
	points = points[offset:end]

	facts := make([]core.UserFact, 0, len(points))
	for _, point := range points {
		facts = append(facts, payloadToUserFact(point.Payload, point.Id.GetUuid()))
	}
	return facts, total, nil
}

// ForgetNamespace deletes all facts for a user in a specific namespace.
func (v *VectorUserMemory) ForgetNamespace(ctx context.Context, userID string, namespace string) error {
	filter := userIDFilter(userID)
	addCondition(filter, "namespace", namespace)
	_, err := v.pointsClient.Delete(ctx, &pb.DeletePoints{
		CollectionName: v.config.CollectionName,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Filter{Filter: filter},
		},
	})
	if err != nil {
		return fmt.Errorf("user memory: forget namespace failed: %w", err)
	}
	return nil
}

// ForgetFact deletes a single fact by ID, scoped to user_id for defense-in-depth.
// Uses filter-based delete (user_id + fact_id payload match) instead of point-ID-only
// to prevent accidental cross-user deletion if a bug passes the wrong factID.
func (v *VectorUserMemory) ForgetFact(ctx context.Context, userID string, factID string) error {
	filter := userIDFilter(userID)
	addCondition(filter, "fact_id", factID)
	_, err := v.pointsClient.Delete(ctx, &pb.DeletePoints{
		CollectionName: v.config.CollectionName,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Filter{Filter: filter},
		},
	})
	if err != nil {
		return fmt.Errorf("user memory: forget fact failed: %w", err)
	}
	return nil
}

// Close waits for background goroutines and releases the gRPC connection.
func (v *VectorUserMemory) Close() error {
	v.wg.Wait()
	if v.conn != nil {
		return v.conn.Close()
	}
	return nil
}

// ─── Internal helpers ────────────────────────────────────────────────────────

// userIDFilter builds the mandatory user_id payload filter for every query.
func userIDFilter(userID string) *pb.Filter {
	return &pb.Filter{
		Must: []*pb.Condition{
			{ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key:   "user_id",
					Match: &pb.Match{MatchValue: &pb.Match_Keyword{Keyword: userID}},
				},
			}},
		},
	}
}

// addCondition appends a keyword match condition to an existing filter.
func addCondition(filter *pb.Filter, key, value string) {
	filter.Must = append(filter.Must, &pb.Condition{
		ConditionOneOf: &pb.Condition_Field{
			Field: &pb.FieldCondition{
				Key:   key,
				Match: &pb.Match{MatchValue: &pb.Match_Keyword{Keyword: value}},
			},
		},
	})
}

// updateAccessTracking increments use_count and updates last_used_at for retrieved points.
// Runs async (fire-and-forget) — failure is logged but never blocks the caller.
// Stateless: writes directly to vector DB, not to a local cache.
func (v *VectorUserMemory) updateAccessTracking(ctx context.Context, points []*pb.ScoredPoint) {
	for _, point := range points {
		currentCount := payloadInt(point.Payload, "use_count")
		_, err := v.pointsClient.SetPayload(ctx, &pb.SetPayloadPoints{
			CollectionName: v.config.CollectionName,
			Payload: map[string]*pb.Value{
				"use_count":    pb.NewValueInt(currentCount + 1),
				"last_used_at": pb.NewValueInt(time.Now().Unix()),
			},
			PointsSelector: &pb.PointsSelector{
				PointsSelectorOneOf: &pb.PointsSelector_Points{
					Points: &pb.PointsIdsList{Ids: []*pb.PointId{point.Id}},
				},
			},
		})
		if err != nil {
			v.config.Logger.Warn("User memory access tracking update failed", map[string]interface{}{
				"operation": "user_memory_access_tracking", "error": err.Error(),
			})
		}
	}
}

func truncateScoredPoints(points []*pb.ScoredPoint, limit int) []*pb.ScoredPoint {
	if limit <= 0 || len(points) <= limit {
		return points
	}
	return points[:limit]
}

func categoryRecallPageSize(limit, overfetchMul int) uint32 {
	pageSize := safeOverfetchLimit(limit, overfetchMul)
	if pageSize < 64 {
		pageSize = 64
	}
	if pageSize > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(pageSize)
}

func safeOverfetchLimit(limit, overfetchMul int) uint64 {
	if limit <= 0 || overfetchMul <= 0 {
		return 0
	}

	left := uint64(limit)
	right := uint64(overfetchMul)
	if left > math.MaxUint64/right {
		return math.MaxUint64
	}
	return left * right
}

func collectCategoryFactsUntilLimit(
	fetchPage func(offset *pb.PointId) ([]core.UserFact, *pb.PointId, error),
	limit int,
	now time.Time,
	transientMaxAge time.Duration,
) ([]core.UserFact, int, error) {
	var (
		offset        *pb.PointId
		collected     []core.UserFact
		filteredCount int
	)

	for {
		pageFacts, nextOffset, err := fetchPage(offset)
		if err != nil {
			return nil, filteredCount, err
		}
		if len(pageFacts) == 0 {
			break
		}

		filteredPage, pageFilteredCount := filterRecalledFactsByLifetime(pageFacts, now, transientMaxAge)
		collected = append(collected, filteredPage...)
		filteredCount += pageFilteredCount

		if limit > 0 && len(collected) >= limit {
			break
		}
		if nextOffset == nil {
			break
		}
		offset = nextOffset
	}

	return collected, filteredCount, nil
}

func filterScoredPointsByLifetime(points []*pb.ScoredPoint, now time.Time, transientMaxAge time.Duration) ([]*pb.ScoredPoint, []core.UserFact, int) {
	if len(points) == 0 {
		return nil, nil, 0
	}

	filteredPoints := make([]*pb.ScoredPoint, 0, len(points))
	filteredFacts := make([]core.UserFact, 0, len(points))
	filteredCount := 0
	for _, point := range points {
		fact := payloadToUserFact(point.Payload, point.Id.GetUuid())
		if shouldFilterTransientOnRecall(fact, now, transientMaxAge) {
			filteredCount++
			continue
		}
		filteredPoints = append(filteredPoints, point)
		filteredFacts = append(filteredFacts, fact)
	}
	return filteredPoints, filteredFacts, filteredCount
}

// payloadToUserFact converts a vector DB point payload to core.UserFact.
// Uses nil-safe accessors — missing payload fields return zero values rather than panicking.
func payloadToUserFact(payload map[string]*pb.Value, factID string) core.UserFact {
	fact := core.UserFact{
		FactID:     factID,
		UserID:     payloadString(payload, "user_id"),
		Namespace:  payloadString(payload, "namespace"),
		Category:   payloadString(payload, "category"),
		Content:    payloadString(payload, "content"),
		Source:     core.FactSource(payloadString(payload, "source")),
		Confidence: payloadDouble(payload, "confidence"),
		CreatedAt:  time.Unix(payloadInt(payload, "created_at"), 0),
		UpdatedAt:  time.Unix(payloadInt(payload, "updated_at"), 0),
	}

	// Parse metadata JSON if present
	if metaStr := payloadString(payload, "metadata"); metaStr != "" {
		var meta map[string]string
		if err := json.Unmarshal([]byte(metaStr), &meta); err == nil {
			fact.Metadata = meta
		}
	}

	return fact
}

// Nil-safe payload accessors — return zero values for missing keys.
func payloadString(payload map[string]*pb.Value, key string) string {
	if v, ok := payload[key]; ok && v != nil {
		return v.GetStringValue()
	}
	return ""
}

func payloadDouble(payload map[string]*pb.Value, key string) float64 {
	if v, ok := payload[key]; ok && v != nil {
		return v.GetDoubleValue()
	}
	return 0
}

func payloadInt(payload map[string]*pb.Value, key string) int64 {
	if v, ok := payload[key]; ok && v != nil {
		return v.GetIntegerValue()
	}
	return 0
}

func recallOverfetchMultiplierFromEnv() int {
	if v, err := strconv.Atoi(os.Getenv("TRUVAG3_USER_MEMORY_RECALL_OVERFETCH_MULTIPLIER")); err == nil && v > 0 {
		return v
	}
	return 3
}
