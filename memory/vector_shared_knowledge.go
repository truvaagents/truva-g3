package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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

// Compile-time interface compliance check.
var _ core.SharedKnowledge = (*VectorSharedKnowledge)(nil)

// VectorSharedKnowledge implements core.SharedKnowledge using the Vector DB vector database.
// It stores knowledge fragments with vector embeddings and retrieves them via
// semantic similarity search with scope-based domain filtering.
//
// Design constraints (per memory/ARCHITECTURE.md §7):
//   - Stateless: no in-memory caches, all state in Vector DB
//   - Thread-safe: gRPC client is safe for concurrent use
//   - Fail-open: runtime errors return (nil, nil), never (nil, error)
//   - Graceful shutdown: Close() releases the gRPC connection
type VectorSharedKnowledge struct {
	conn              *grpc.ClientConn
	pointsClient      pb.PointsClient
	collectionsClient pb.CollectionsClient
	config            *VectorConfig
	wg                sync.WaitGroup // Tracks background goroutines for graceful shutdown
}

// NewVectorSharedKnowledge creates a new Vector DB-backed SharedKnowledge.
// Uses WithXXX option functions per FRAMEWORK_DESIGN_PRINCIPLES §Configuration.
//
// Fails fast on configuration/connection errors. Returns error if Vector DB is unreachable.
func NewVectorSharedKnowledge(opts ...Option) (*VectorSharedKnowledge, error) {
	config := defaultConfig()

	// Apply explicit options (highest priority)
	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, fmt.Errorf("invalid vector DB configuration: %w", err)
		}
	}

	// Connect to Vector DB via gRPC
	if config.TLS {
		return nil, fmt.Errorf("TLS for Vector DB gRPC is not yet implemented — use WithVectorTLS(false) or omit the option")
	}

	conn, err := grpc.NewClient(config.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Vector DB at %s: %w (check TRUVAG3_VECTOR_DB_URL)", config.Address, err)
	}

	q := &VectorSharedKnowledge{
		conn:              conn,
		pointsClient:      pb.NewPointsClient(conn),
		collectionsClient: pb.NewCollectionsClient(conn),
		config:            config,
	}

	// Auto-create collection if configured
	if config.AutoCreateCollection {
		if err := q.ensureCollection(context.Background()); err != nil {
			config.Logger.Warn("Failed to auto-create Vector DB collection", map[string]interface{}{
				"operation":  "qdrant_init",
				"collection": config.CollectionName,
				"error":      err.Error(),
			})
			// Don't fail — collection may already exist or be created externally
		}
	}

	config.Logger.Info("Vector DB SharedKnowledge initialized", map[string]interface{}{
		"operation":   "qdrant_init",
		"address":     config.Address,
		"collection":  config.CollectionName,
		"vector_size": config.VectorSize,
	})

	return q, nil
}

// Close waits for in-flight background goroutines to complete, then releases
// the gRPC connection. Per FRAMEWORK_DESIGN_PRINCIPLES §Component Lifecycle
// and §Performance Requirements: "Goroutines: Must clean up goroutines on shutdown."
func (q *VectorSharedKnowledge) Close() error {
	q.wg.Wait() // Wait for background updateAccessCounts goroutines
	if q.conn != nil {
		return q.conn.Close()
	}
	return nil
}

// StoreKnowledge persists a knowledge fragment in Vector DB.
// The fragment.Embedding field must be populated by the caller.
// Returns error if fragment.Scope is ScopePrivate.
func (q *VectorSharedKnowledge) StoreKnowledge(ctx context.Context, fragment core.KnowledgeFragment) error {
	// ScopePrivate fragments must NOT be stored in shared knowledge
	if fragment.Scope == core.ScopePrivate {
		return fmt.Errorf("private fragments cannot be stored in shared knowledge")
	}

	// Assign ID if empty
	if fragment.FragmentID == "" {
		fragment.FragmentID = uuid.New().String()
	}
	if fragment.CreatedAt.IsZero() {
		fragment.CreatedAt = time.Now()
	}

	// Extract request_id from context for logging (Pattern 3)
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	// Validate embedding
	if len(fragment.Embedding) == 0 {
		q.config.Logger.WarnWithContext(ctx, "Fragment has no embedding, skipping store", map[string]interface{}{
			"operation":   "store_knowledge",
			"request_id":  requestID,
			"fragment_id": fragment.FragmentID,
		})
		return nil // Fail-open — don't block the pipeline
	}

	// Build Vector DB point
	pointID := pb.NewIDUUID(fragment.FragmentID)
	vectors := pb.NewVectors(fragment.Embedding...)

	// Build payload with all metadata for filtering
	payload := map[string]*pb.Value{
		"fragment_id":  pb.NewValueString(fragment.FragmentID),
		"namespace":    pb.NewValueString(fragment.Namespace),
		"agent_domain": pb.NewValueString(fragment.AgentDomain),
		"scope":        pb.NewValueString(string(fragment.Scope)),
		"content":      pb.NewValueString(fragment.Content),
		"importance":   pb.NewValueDouble(fragment.Importance),
		"created_at":   pb.NewValueInt(fragment.CreatedAt.Unix()),
		"access_count": pb.NewValueInt(int64(fragment.AccessCount)),
	}
	if fragment.LastAccessed.IsZero() {
		payload["last_accessed"] = pb.NewValueInt(time.Now().Unix())
	} else {
		payload["last_accessed"] = pb.NewValueInt(fragment.LastAccessed.Unix())
	}

	// Persist source events as a JSON-encoded string list
	if len(fragment.SourceEvents) > 0 {
		sourceJSON, _ := json.Marshal(fragment.SourceEvents)
		payload["source_events"] = pb.NewValueString(string(sourceJSON))
	}

	// Persist metadata as a JSON-encoded string map
	if len(fragment.Metadata) > 0 {
		metaJSON, _ := json.Marshal(fragment.Metadata)
		payload["metadata"] = pb.NewValueString(string(metaJSON))
	}

	// Upsert to Vector DB
	_, err := q.pointsClient.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: q.config.CollectionName,
		Points: []*pb.PointStruct{
			{
				Id:      pointID,
				Vectors: vectors,
				Payload: payload,
			},
		},
	})
	if err != nil {
		q.config.Logger.WarnWithContext(ctx, "Failed to store knowledge in Vector DB", map[string]interface{}{
			"operation":   "store_knowledge",
			"request_id":  requestID,
			"fragment_id": fragment.FragmentID,
			"error":       err.Error(),
		})
		telemetry.Counter("memory.knowledge.store.errors",
			"module", "memory",
		)
		return nil // Fail-open
	}

	telemetry.Counter("memory.knowledge.store.success",
		"module", "memory",
	)
	return nil
}

// SearchKnowledge performs semantic similarity search with scope-based domain filtering.
// Returns (nil, nil) on infrastructure failure — never blocks the pipeline.
func (q *VectorSharedKnowledge) SearchKnowledge(
	ctx context.Context,
	callerDomain string,
	namespace string,
	query string,
	topK int,
	weights core.RetrievalWeights,
) ([]core.ScoredKnowledge, error) {
	// SearchKnowledge receives a text query, but we need an embedding vector.
	// The caller (orchestration hook) should have embedded the query already
	// and passed the embedding. However, the interface takes a string query.
	// This implementation expects the caller to have set up the embedding
	// pipeline externally. For now, we log a warning if called with a text query
	// and no embedding infrastructure.
	//
	// In practice, the MemoryEnrichmentHook embeds the query using EmbeddingClient
	// and then calls SearchKnowledge. But the interface signature takes string query
	// for flexibility (some backends may do their own embedding).
	//
	// For Vector DB, we need to convert text → vector externally.
	// The hook handles this — see orchestration/memory_hooks.go BeforePlanning.
	//
	// If we reach here with just text, we can't search. Return empty.
	searchRequestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		searchRequestID = baggage["request_id"]
	}
	q.config.Logger.WarnWithContext(ctx, "SearchKnowledge called with text query — Vector DB requires vector. "+
		"Use SearchKnowledgeByVector for direct vector search.", map[string]interface{}{
		"operation":     "search_knowledge",
		"request_id":    searchRequestID,
		"caller_domain": callerDomain,
		"query_length":  len(query),
	})
	return nil, nil
}

// SearchKnowledgeByVector performs semantic similarity search using a pre-computed embedding vector.
// This is the primary search method — the orchestration hook embeds the query first,
// then calls this method.
func (q *VectorSharedKnowledge) SearchKnowledgeByVector(
	ctx context.Context,
	callerDomain string,
	namespace string,
	queryVector []float32,
	topK int,
	weights core.RetrievalWeights,
) ([]core.ScoredKnowledge, error) {
	if len(queryVector) == 0 {
		return nil, nil
	}
	if topK <= 0 {
		topK = 5
	}

	searchStart := time.Now()

	// Extract request_id for logging and span events (Pattern 3 + 6)
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	// Build scope filter: global OR (shared_domain AND matching domain)
	scopeFilter := q.buildScopeFilter(callerDomain)

	// Add namespace filter if specified
	if namespace != "" {
		scopeFilter.Must = append(scopeFilter.Must, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "namespace",
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{Keyword: namespace},
					},
				},
			},
		})
	}

	// Search Vector DB
	searchCtx, cancel := context.WithTimeout(ctx, q.config.SearchTimeout)
	defer cancel()

	result, err := q.pointsClient.Search(searchCtx, &pb.SearchPoints{
		CollectionName: q.config.CollectionName,
		Vector:         queryVector,
		Limit:          uint64(topK),
		Filter:         scopeFilter,
		WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
	})
	if err != nil {
		q.config.Logger.WarnWithContext(ctx, "Vector DB search failed, returning empty results", map[string]interface{}{
			"operation":     "search_knowledge",
			"request_id":    requestID,
			"caller_domain": callerDomain,
			"error":         err.Error(),
			"duration_ms":   time.Since(searchStart).Milliseconds(),
		})
		telemetry.Counter("memory.knowledge.search.errors",
			"module", "memory",
		)
		return nil, nil // Fail-open
	}

	// Map Vector DB results to core.ScoredKnowledge
	var results []core.ScoredKnowledge
	for _, point := range result.GetResult() {
		fragment := q.pointToFragment(point)
		if fragment == nil {
			continue
		}

		// Compute weighted score
		recencyScore := computeRecency(fragment.CreatedAt)
		relevanceScore := float64(point.GetScore())
		importanceScore := fragment.Importance / 10.0 // Normalize to 0-1

		combinedScore := weights.Recency*recencyScore +
			weights.Relevance*relevanceScore +
			weights.Importance*importanceScore

		results = append(results, core.ScoredKnowledge{
			Fragment:   *fragment,
			Score:      combinedScore,
			Recency:    recencyScore,
			Relevance:  relevanceScore,
			Importance: importanceScore,
		})
	}

	// Telemetry for successful search (Pattern 5 + 6)
	telemetry.AddSpanEvent(ctx, "memory.knowledge.search.completed",
		attribute.String("request_id", requestID),
		attribute.Int("results_count", len(results)),
		attribute.Int64("duration_ms", time.Since(searchStart).Milliseconds()),
	)
	telemetry.Counter("memory.knowledge.search.success",
		"module", "memory",
	)

	// Update access counts asynchronously — tracked by WaitGroup for graceful shutdown.
	// Uses detached context with timeout — must outlive the request context
	// (which may be cancelled after the HTTP handler returns).
	q.wg.Add(1)
	go func() { // #nosec G118 -- intentional detached context, must outlive request
		defer q.wg.Done()
		trackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		q.updateAccessCounts(trackCtx, results)
	}()

	return results, nil
}

// UpdateImportance adjusts the importance score of a knowledge fragment.
// Used by compaction for importance decay on stale fragments.
func (q *VectorSharedKnowledge) UpdateImportance(ctx context.Context, fragmentID string, newImportance float64) error {
	if fragmentID == "" {
		return nil
	}

	pointID := pb.NewIDUUID(fragmentID)
	_, err := q.pointsClient.SetPayload(ctx, &pb.SetPayloadPoints{
		CollectionName: q.config.CollectionName,
		PointsSelector: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Points{
				Points: &pb.PointsIdsList{
					Ids: []*pb.PointId{pointID},
				},
			},
		},
		Payload: map[string]*pb.Value{
			"importance": pb.NewValueDouble(newImportance),
		},
	})
	if err != nil {
		updateReqID := ""
		if baggage := telemetry.GetBaggage(ctx); baggage != nil {
			updateReqID = baggage["request_id"]
		}
		q.config.Logger.WarnWithContext(ctx, "Failed to update importance in Vector DB", map[string]interface{}{
			"operation":      "update_importance",
			"request_id":     updateReqID,
			"fragment_id":    fragmentID,
			"new_importance": newImportance,
			"error":          err.Error(),
		})
		return nil // Fail-open
	}

	return nil
}

// --- Internal helpers ---

// buildScopeFilter creates a Vector DB filter for domain-scoped visibility.
// ScopeGlobal: visible to everyone.
// ScopeSharedDomain: visible only to same domain.
// ScopePrivate: never stored in SharedKnowledge (rejected by StoreKnowledge).
func (q *VectorSharedKnowledge) buildScopeFilter(callerDomain string) *pb.Filter {
	return &pb.Filter{
		Should: []*pb.Condition{
			// Global fragments: visible to everyone
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "scope",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{Keyword: string(core.ScopeGlobal)},
						},
					},
				},
			},
			// Domain fragments: visible to same domain only
			{
				ConditionOneOf: &pb.Condition_Filter{
					Filter: &pb.Filter{
						Must: []*pb.Condition{
							{
								ConditionOneOf: &pb.Condition_Field{
									Field: &pb.FieldCondition{
										Key: "scope",
										Match: &pb.Match{
											MatchValue: &pb.Match_Keyword{Keyword: string(core.ScopeSharedDomain)},
										},
									},
								},
							},
							{
								ConditionOneOf: &pb.Condition_Field{
									Field: &pb.FieldCondition{
										Key: "agent_domain",
										Match: &pb.Match{
											MatchValue: &pb.Match_Keyword{Keyword: callerDomain},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// ensureCollection creates the Vector DB collection if it doesn't exist.
func (q *VectorSharedKnowledge) ensureCollection(ctx context.Context) error {
	// Check if collection exists
	_, err := q.collectionsClient.Get(ctx, &pb.GetCollectionInfoRequest{
		CollectionName: q.config.CollectionName,
	})
	if err == nil {
		return nil // Collection already exists
	}

	// Create collection with HNSW config
	distance := pb.Distance_Cosine
	switch q.config.Distance {
	case "Euclid":
		distance = pb.Distance_Euclid
	case "Dot":
		distance = pb.Distance_Dot
	}

	_, err = q.collectionsClient.Create(ctx, &pb.CreateCollection{
		CollectionName: q.config.CollectionName,
		VectorsConfig: &pb.VectorsConfig{
			Config: &pb.VectorsConfig_Params{
				Params: &pb.VectorParams{
					Size:     uint64(max(q.config.VectorSize, 0)),
					Distance: distance,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create collection %s: %w", q.config.CollectionName, err)
	}

	// Create payload indexes for scope filtering
	for _, field := range []string{"scope", "agent_domain", "namespace"} {
		_, indexErr := q.pointsClient.CreateFieldIndex(ctx, &pb.CreateFieldIndexCollection{
			CollectionName: q.config.CollectionName,
			FieldName:      field,
			FieldType:      pb.FieldType_FieldTypeKeyword.Enum(),
		})
		if indexErr != nil {
			q.config.Logger.Warn("Failed to create payload index", map[string]interface{}{
				"operation":  "qdrant_init",
				"field":      field,
				"collection": q.config.CollectionName,
				"error":      indexErr.Error(),
			})
		}
	}

	return nil
}

// pointToFragment converts a Vector DB ScoredPoint to a core.KnowledgeFragment.
func (q *VectorSharedKnowledge) pointToFragment(point *pb.ScoredPoint) *core.KnowledgeFragment {
	payload := point.GetPayload()
	if payload == nil {
		return nil
	}

	fragment := &core.KnowledgeFragment{
		FragmentID:  getPayloadString(payload, "fragment_id"),
		Namespace:   getPayloadString(payload, "namespace"),
		AgentDomain: getPayloadString(payload, "agent_domain"),
		Scope:       core.MemoryScope(getPayloadString(payload, "scope")),
		Content:     getPayloadString(payload, "content"),
		Importance:  getPayloadFloat(payload, "importance"),
		AccessCount: int(getPayloadInt(payload, "access_count")),
	}

	if ts := getPayloadInt(payload, "created_at"); ts > 0 {
		fragment.CreatedAt = time.Unix(ts, 0)
	}
	if ts := getPayloadInt(payload, "last_accessed"); ts > 0 {
		fragment.LastAccessed = time.Unix(ts, 0)
	}

	return fragment
}

// updateAccessCounts increments access_count and updates last_accessed for retrieved fragments.
// Runs asynchronously — errors are silently logged.
func (q *VectorSharedKnowledge) updateAccessCounts(ctx context.Context, results []core.ScoredKnowledge) {
	for _, r := range results {
		if r.Fragment.FragmentID == "" {
			continue
		}
		pointID := pb.NewIDUUID(r.Fragment.FragmentID)
		_, err := q.pointsClient.SetPayload(ctx, &pb.SetPayloadPoints{
			CollectionName: q.config.CollectionName,
			PointsSelector: &pb.PointsSelector{
				PointsSelectorOneOf: &pb.PointsSelector_Points{
					Points: &pb.PointsIdsList{
						Ids: []*pb.PointId{pointID},
					},
				},
			},
			Payload: map[string]*pb.Value{
				"last_accessed": pb.NewValueInt(time.Now().Unix()),
				"access_count":  pb.NewValueInt(int64(r.Fragment.AccessCount + 1)),
			},
		})
		if err != nil {
			q.config.Logger.Warn("Failed to update access count", map[string]interface{}{
				"operation":   "update_access_count",
				"fragment_id": r.Fragment.FragmentID,
				"error":       err.Error(),
			})
		}
	}
}

// --- Payload helpers ---

func getPayloadString(payload map[string]*pb.Value, key string) string {
	if v, ok := payload[key]; ok {
		return v.GetStringValue()
	}
	return ""
}

func getPayloadFloat(payload map[string]*pb.Value, key string) float64 {
	if v, ok := payload[key]; ok {
		return v.GetDoubleValue()
	}
	return 0
}

func getPayloadInt(payload map[string]*pb.Value, key string) int64 {
	if v, ok := payload[key]; ok {
		return v.GetIntegerValue()
	}
	return 0
}

// computeRecency returns a 0-1 score based on how recent a fragment is.
// Uses exponential decay: 1.0 for now, ~0.5 for 1 day ago, ~0.0 for 7+ days ago.
func computeRecency(createdAt time.Time) float64 {
	if createdAt.IsZero() {
		return 0.5 // Unknown age — neutral score
	}
	hoursAgo := time.Since(createdAt).Hours()
	if hoursAgo < 0 {
		hoursAgo = 0
	}
	// Exponential decay with half-life of ~24 hours
	return math.Exp(-0.029 * hoursAgo) // ln(2)/24 ≈ 0.029
}
