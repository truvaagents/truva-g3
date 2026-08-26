package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// vectorKnowledgeSearcher is implemented by backends (e.g., VectorSharedKnowledge)
// that support pre-embedded vector search. Mirrors orchestration.VectorKnowledgeSearcher.
type vectorKnowledgeSearcher interface {
	SearchKnowledgeByVector(ctx context.Context, callerDomain string, namespace string,
		queryVector []float32, topK int, weights core.RetrievalWeights) ([]core.ScoredKnowledge, error)
}

// Error codes
const (
	ErrCodeInvalidRequest = "INVALID_REQUEST"
	ErrCodeMissingField   = "MISSING_FIELD"
	ErrCodeBackendError   = "BACKEND_ERROR"
)

// ---------------------------------------------------------------------------
// query_events handler
// ---------------------------------------------------------------------------

func (m *MemoryTool) handleQueryEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	if m.Logger != nil {
		m.Logger.InfoWithContext(ctx, "Processing query_events request", map[string]interface{}{
			"operation":  "query_events",
			"method":     r.Method,
			"path":       r.URL.Path,
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("operation", "query_events"),
	)

	// Decode request
	var req QueryEventsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("memory.errors.total",
			"module", "agentic-memory-tool",
			"capability", "query_events",
			"error_type", "decode_error",
		)
		telemetry.RecordToolCall("agentic-memory-tool", "query_events",
			float64(time.Since(startTime).Milliseconds()), "error")
		if m.Logger != nil {
			m.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "query_events",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		m.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Defaults and bounds
	if req.SinceHours <= 0 {
		req.SinceHours = 24
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	if m.Logger != nil {
		m.Logger.InfoWithContext(ctx, "Querying episodic memory", map[string]interface{}{
			"operation":   "query_events",
			"entity_type": req.EntityType,
			"entity_id":   req.EntityID,
			"agent_name":  req.AgentName,
			"action_type": req.ActionType,
			"since_hours": req.SinceHours,
			"limit":       req.Limit,
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "querying_episodic_memory",
		attribute.String("request_id", requestID),
		attribute.String("entity_id", req.EntityID),
	)

	since := time.Now().Add(-time.Duration(req.SinceHours) * time.Hour)

	// Use QueryEvents with EventFilter — handles all filter combinations at the query level
	// (entity_type, entity_id, agent_name, action_type, since, limit).
	var actionTypes []string
	if req.ActionType != "" {
		actionTypes = []string{req.ActionType}
	}
	filter := core.EventFilter{
		EntityType:  req.EntityType,
		EntityID:    req.EntityID,
		AgentName:   req.AgentName,
		ActionTypes: actionTypes,
		Since:       since,
		Limit:       req.Limit,
	}

	events, queryErr := m.episodic.QueryEvents(ctx, m.domain, filter)
	if queryErr != nil {
		telemetry.RecordSpanError(ctx, queryErr)
		telemetry.Counter("memory.errors.total",
			"module", "agentic-memory-tool",
			"capability", "query_events",
			"error_type", "episodic_read",
		)
		telemetry.RecordToolCall("agentic-memory-tool", "query_events",
			float64(time.Since(startTime).Milliseconds()), "error")
		if m.Logger != nil {
			m.Logger.WarnWithContext(ctx, "Episodic memory query failed", map[string]interface{}{
				"operation":   "query_events",
				"error":       queryErr.Error(),
				"error_type":  "episodic_read",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		m.sendError(w, fmt.Sprintf("Memory query failed: %v", queryErr), http.StatusServiceUnavailable, ErrCodeBackendError)
		return
	}

	result := events
	if len(result) > req.Limit {
		result = result[:req.Limit]
	}
	resp := EventsResponse{
		Events:     toEventSummaries(result),
		TotalCount: len(events),
		Domain:     m.domain,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    resp,
	})

	// Metrics
	duration := time.Since(startTime)
	telemetry.Counter("memory.requests.total",
		"module", "agentic-memory-tool",
		"capability", "query_events",
		"status", "success",
	)
	telemetry.Histogram("memory.request.duration_ms",
		float64(duration.Milliseconds()),
		"module", "agentic-memory-tool",
		"capability", "query_events",
	)
	telemetry.RecordToolCall("agentic-memory-tool", "query_events",
		float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "events_retrieved",
		attribute.String("request_id", requestID),
		attribute.Int("event_count", len(resp.Events)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	if m.Logger != nil {
		m.Logger.InfoWithContext(ctx, "query_events completed", map[string]interface{}{
			"operation":   "query_events",
			"event_count": len(resp.Events),
			"request_id":  requestID,
			"status":      "success",
			"duration_ms": duration.Milliseconds(),
		})
	}
}

// ---------------------------------------------------------------------------
// query_knowledge handler
// ---------------------------------------------------------------------------

func (m *MemoryTool) handleQueryKnowledge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	if m.Logger != nil {
		m.Logger.InfoWithContext(ctx, "Processing query_knowledge request", map[string]interface{}{
			"operation":  "query_knowledge",
			"method":     r.Method,
			"path":       r.URL.Path,
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("operation", "query_knowledge"),
	)

	// Decode request
	var req QueryKnowledgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("memory.errors.total",
			"module", "agentic-memory-tool",
			"capability", "query_knowledge",
			"error_type", "decode_error",
		)
		telemetry.RecordToolCall("agentic-memory-tool", "query_knowledge",
			float64(time.Since(startTime).Milliseconds()), "error")
		if m.Logger != nil {
			m.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "query_knowledge",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		m.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Validate required field
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		err := fmt.Errorf("query is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("memory.errors.total",
			"module", "agentic-memory-tool",
			"capability", "query_knowledge",
			"error_type", "validation_error",
		)
		telemetry.RecordToolCall("agentic-memory-tool", "query_knowledge",
			float64(time.Since(startTime).Milliseconds()), "error")
		if m.Logger != nil {
			m.Logger.WarnWithContext(ctx, "Empty query in request", map[string]interface{}{
				"operation":   "query_knowledge",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		m.sendError(w, "query field is required", http.StatusBadRequest, ErrCodeMissingField)
		return
	}

	// Defaults
	if req.Limit <= 0 {
		req.Limit = 5
	}
	if req.Limit > 20 {
		req.Limit = 20
	}

	// Check if knowledge backend is available
	if m.knowledge == nil || m.embedder == nil {
		resp := KnowledgeResponse{
			Fragments:  []KnowledgeFragment{},
			TotalCount: 0,
			Domain:     m.domain,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(core.ToolResponse{
			Success: true,
			Data:    resp,
		})

		duration := time.Since(startTime)
		telemetry.Counter("memory.requests.total",
			"module", "agentic-memory-tool",
			"capability", "query_knowledge",
			"status", "success",
		)
		telemetry.RecordToolCall("agentic-memory-tool", "query_knowledge",
			float64(duration.Milliseconds()), "success")

		if m.Logger != nil {
			m.Logger.InfoWithContext(ctx, "query_knowledge completed (backend unavailable, returning empty)", map[string]interface{}{
				"operation":   "query_knowledge",
				"request_id":  requestID,
				"status":      "success",
				"duration_ms": duration.Milliseconds(),
			})
		}
		return
	}

	telemetry.AddSpanEvent(ctx, "querying_knowledge",
		attribute.String("request_id", requestID),
		attribute.String("query", req.Query),
		attribute.String("namespace", req.Namespace),
	)

	// Search knowledge — try vector-based search first, fall back to text-based.
	// VectorSharedKnowledge.SearchKnowledge (text) returns empty because Vector DB
	// needs a pre-computed embedding. The orchestration hook uses the same pattern:
	// embed query → type-assert to VectorKnowledgeSearcher → SearchKnowledgeByVector.
	weights := core.RetrievalWeights{
		Recency:    0.2,
		Relevance:  0.6,
		Importance: 0.2,
	}

	var scored []core.ScoredKnowledge
	var searchErr error

	if vectorSearcher, ok := m.knowledge.(vectorKnowledgeSearcher); ok && m.embedder != nil {
		// Embed the query text into a vector
		embResp, embErr := m.embedder.GenerateEmbeddings(ctx, []string{req.Query}, nil)
		if embErr == nil && len(embResp.Embeddings) > 0 && len(embResp.Embeddings[0]) > 0 {
			scored, searchErr = vectorSearcher.SearchKnowledgeByVector(ctx, m.domain, req.Namespace, embResp.Embeddings[0], req.Limit, weights)
		} else if embErr != nil {
			if m.Logger != nil {
				m.Logger.WarnWithContext(ctx, "Embedding failed, falling back to text search", map[string]interface{}{
					"operation":  "query_knowledge",
					"request_id": requestID,
					"error":      embErr.Error(),
					"error_type": "embedding",
				})
			}
			// Fall through to text-based search below
		}
	}

	// Fall back to text-based search (for backends that handle their own embedding)
	if scored == nil && searchErr == nil {
		scored, searchErr = m.knowledge.SearchKnowledge(ctx, m.domain, req.Namespace, req.Query, req.Limit, weights)
	}

	if searchErr != nil {
		telemetry.RecordSpanError(ctx, searchErr)
		telemetry.Counter("memory.errors.total",
			"module", "agentic-memory-tool",
			"capability", "query_knowledge",
			"error_type", "knowledge_store",
		)
		telemetry.RecordToolCall("agentic-memory-tool", "query_knowledge",
			float64(time.Since(startTime).Milliseconds()), "error")
		if m.Logger != nil {
			m.Logger.WarnWithContext(ctx, "Knowledge search failed", map[string]interface{}{
				"operation":   "query_knowledge",
				"error":       searchErr.Error(),
				"error_type":  "knowledge_store",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		m.sendError(w, fmt.Sprintf("Knowledge search failed: %v", searchErr), http.StatusServiceUnavailable, ErrCodeBackendError)
		return
	}

	// Build response
	fragments := make([]KnowledgeFragment, 0, len(scored))
	for _, s := range scored {
		fragments = append(fragments, KnowledgeFragment{
			Content:      s.Fragment.Content,
			Namespace:    s.Fragment.Namespace,
			Importance:   s.Fragment.Importance,
			Confidence:   s.Score,
			SourceEvents: s.Fragment.SourceEvents,
		})
	}

	resp := KnowledgeResponse{
		Fragments:  fragments,
		TotalCount: len(fragments),
		Domain:     m.domain,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    resp,
	})

	duration := time.Since(startTime)
	telemetry.Counter("memory.requests.total",
		"module", "agentic-memory-tool",
		"capability", "query_knowledge",
		"status", "success",
	)
	telemetry.Histogram("memory.request.duration_ms",
		float64(duration.Milliseconds()),
		"module", "agentic-memory-tool",
		"capability", "query_knowledge",
	)
	telemetry.RecordToolCall("agentic-memory-tool", "query_knowledge",
		float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "knowledge_retrieved",
		attribute.String("request_id", requestID),
		attribute.Int("fragment_count", len(fragments)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	if m.Logger != nil {
		m.Logger.InfoWithContext(ctx, "query_knowledge completed", map[string]interface{}{
			"operation":      "query_knowledge",
			"fragment_count": len(fragments),
			"request_id":     requestID,
			"status":         "success",
			"duration_ms":    duration.Milliseconds(),
		})
	}
}

// ---------------------------------------------------------------------------
// query_investigations handler
// ---------------------------------------------------------------------------

func (m *MemoryTool) handleQueryInvestigations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	if m.Logger != nil {
		m.Logger.InfoWithContext(ctx, "Processing query_investigations request", map[string]interface{}{
			"operation":  "query_investigations",
			"method":     r.Method,
			"path":       r.URL.Path,
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("operation", "query_investigations"),
	)

	// Decode request
	var req QueryInvestigationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("memory.errors.total",
			"module", "agentic-memory-tool",
			"capability", "query_investigations",
			"error_type", "decode_error",
		)
		telemetry.RecordToolCall("agentic-memory-tool", "query_investigations",
			float64(time.Since(startTime).Milliseconds()), "error")
		if m.Logger != nil {
			m.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "query_investigations",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		m.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Check if coordinator is available
	if m.coordinator == nil {
		resp := InvestigationsResponse{
			Investigations: []Investigation{},
			Domain:         m.domain,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: resp})

		telemetry.RecordToolCall("agentic-memory-tool", "query_investigations",
			float64(time.Since(startTime).Milliseconds()), "success")
		return
	}

	telemetry.AddSpanEvent(ctx, "querying_investigations",
		attribute.String("request_id", requestID),
		attribute.String("entity_id", req.EntityID),
	)

	// Get all active investigations
	active, err := m.coordinator.GetActiveInvestigations(ctx)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("memory.errors.total",
			"module", "agentic-memory-tool",
			"capability", "query_investigations",
			"error_type", "coordination_read",
		)
		telemetry.RecordToolCall("agentic-memory-tool", "query_investigations",
			float64(time.Since(startTime).Milliseconds()), "error")
		if m.Logger != nil {
			m.Logger.WarnWithContext(ctx, "Investigation query failed", map[string]interface{}{
				"operation":   "query_investigations",
				"error":       err.Error(),
				"error_type":  "coordination_read",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		m.sendError(w, fmt.Sprintf("Investigation query failed: %v", err), http.StatusServiceUnavailable, ErrCodeBackendError)
		return
	}

	// Build response — filter by entity_id if provided
	investigations := make([]Investigation, 0)
	for entityID, holder := range active {
		if req.EntityID != "" && entityID != req.EntityID {
			continue
		}
		investigations = append(investigations, Investigation{
			EntityID: entityID,
			Holder:   holder,
			Status:   "active",
		})
	}

	resp := InvestigationsResponse{
		Investigations: investigations,
		Domain:         m.domain,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    resp,
	})

	duration := time.Since(startTime)
	telemetry.Counter("memory.requests.total",
		"module", "agentic-memory-tool",
		"capability", "query_investigations",
		"status", "success",
	)
	telemetry.Histogram("memory.request.duration_ms",
		float64(duration.Milliseconds()),
		"module", "agentic-memory-tool",
		"capability", "query_investigations",
	)
	telemetry.RecordToolCall("agentic-memory-tool", "query_investigations",
		float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "investigations_retrieved",
		attribute.String("request_id", requestID),
		attribute.Int("investigation_count", len(investigations)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	if m.Logger != nil {
		m.Logger.InfoWithContext(ctx, "query_investigations completed", map[string]interface{}{
			"operation":           "query_investigations",
			"investigation_count": len(investigations),
			"request_id":          requestID,
			"status":              "success",
			"duration_ms":         duration.Milliseconds(),
		})
	}
}

// ---------------------------------------------------------------------------
// Error helpers
// ---------------------------------------------------------------------------

// sendError sends a structured error response for local errors.
// CRITICAL: Must call rw.WriteHeader(status) before encoding — Go defaults to 200 otherwise.
func (m *MemoryTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: status >= 500,
		},
	})
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

func toEventSummaries(events []core.AgentEvent) []EventSummary {
	summaries := make([]EventSummary, 0, len(events))
	for _, e := range events {
		summaries = append(summaries, EventSummary{
			EventID:    e.EventID,
			Timestamp:  e.Timestamp,
			AgentName:  e.AgentName,
			ActionType: e.ActionType,
			EntityType: e.EntityType,
			EntityID:   e.EntityID,
			Summary:    e.Summary,
			Outcome:    e.Outcome,
			Importance: e.Importance,
		})
	}
	return summaries
}
