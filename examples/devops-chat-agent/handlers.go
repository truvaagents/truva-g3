package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// handleCreateSession creates a new chat session.
func (t *DevOpsChatAgent) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Handle CORS preflight
	if r.Method == http.MethodOptions {
		setCORSHeaders(w)
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Only POST requests are supported", nil)
		return
	}

	userID := getUserID(r)

	// Parse optional metadata from request body
	var metadata map[string]interface{}
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
			// Ignore decode errors, proceed without metadata
			metadata = nil
		}
	}

	// Create session
	session := t.sessionStore.Create(userID, metadata)

	t.Logger.InfoWithContext(ctx, "Session created", map[string]interface{}{
		"operation":  "create_session",
		"session_id": session.ID,
		"user_id":    userID,
	})

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"session_id": session.ID,
		"created_at": session.CreatedAt,
		"expires_at": session.CreatedAt.Add(t.sessionStore.GetTTL()),
	})
}

// handleGetSession retrieves session information.
func (t *DevOpsChatAgent) handleGetSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Only GET requests are supported", nil)
		return
	}

	// Extract session ID from URL path
	sessionID := extractPathParam(r.URL.Path, "/chat/session/")
	if sessionID == "" || strings.Contains(sessionID, "/") {
		writeError(w, http.StatusBadRequest, "Invalid session ID", nil)
		return
	}

	session := t.sessionStore.Get(sessionID)
	if session == nil {
		writeError(w, http.StatusNotFound, "Session not found or expired", nil)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":    session.ID,
		"created_at":    session.CreatedAt,
		"updated_at":    session.UpdatedAt,
		"message_count": len(session.Messages),
		"metadata":      session.Metadata,
	})
}

// handleGetHistory retrieves conversation history for a session.
func (t *DevOpsChatAgent) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Only GET requests are supported", nil)
		return
	}

	// Extract session ID from URL path (e.g., /chat/session/{id}/history)
	path := strings.TrimPrefix(r.URL.Path, "/chat/session/")
	path = strings.TrimSuffix(path, "/history")
	sessionID := path

	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "Session ID is required", nil)
		return
	}

	session := t.sessionStore.Get(sessionID)
	if session == nil {
		writeError(w, http.StatusNotFound, "Session not found or expired", nil)
		return
	}

	messages := t.sessionStore.GetHistory(sessionID)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"messages":   messages,
		"count":      len(messages),
	})
}

// handleHealth returns health status with orchestrator metrics.
func (t *DevOpsChatAgent) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
		"service":   "devops-chat-agent",
	}

	// Check Redis/Discovery connection
	if t.Discovery != nil {
		_, err := t.Discovery.Discover(ctx, core.DiscoveryFilter{})
		if err != nil {
			health["status"] = "degraded"
			health["redis"] = "unavailable"
			t.Logger.WarnWithContext(ctx, "Health check: Redis unavailable", map[string]interface{}{
				"operation": "health_check",
				"error":     err.Error(),
			})
		} else {
			health["redis"] = "healthy"
		}
	} else {
		health["redis"] = "not configured"
	}

	// Check orchestrator status
	orch := t.GetOrchestrator()
	if orch != nil {
		metrics := orch.GetMetrics()
		health["orchestrator"] = map[string]interface{}{
			"status":              "active",
			"total_requests":      metrics.TotalRequests,
			"successful_requests": metrics.SuccessfulRequests,
			"failed_requests":     metrics.FailedRequests,
			"average_latency_ms":  metrics.AverageLatency.Milliseconds(),
		}
	} else {
		health["orchestrator"] = "initializing"
	}

	// Check AI provider
	if t.AI != nil {
		health["ai_provider"] = "connected"
	} else {
		health["ai_provider"] = "not configured"
	}

	// Add session stats
	health["active_sessions"] = t.sessionStore.GetActiveSessionCount()

	// Set appropriate status code
	statusCode := http.StatusOK
	if health["status"] == "degraded" || health["status"] == "unhealthy" {
		statusCode = http.StatusServiceUnavailable
	}

	setCORSHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(health)
}

// handleDiscover shows available tools and their capabilities.
func (t *DevOpsChatAgent) handleDiscover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	t.Logger.InfoWithContext(ctx, "Discovering components", map[string]interface{}{
		"operation": "discover_tools",
		"path":      r.URL.Path,
	})

	if t.Discovery == nil {
		writeError(w, http.StatusServiceUnavailable, "Service discovery not configured", nil)
		return
	}

	allComponents, err := t.Discovery.Discover(ctx, core.DiscoveryFilter{})
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Discovery failed", map[string]interface{}{
			"operation": "discover_tools",
			"error":     err.Error(),
		})
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusServiceUnavailable, "Discovery failed", err)
		return
	}

	tools := make([]*core.ServiceInfo, 0)
	agents := make([]*core.ServiceInfo, 0)

	for _, component := range allComponents {
		switch component.Type {
		case core.ComponentTypeTool:
			tools = append(tools, component)
		case core.ComponentTypeAgent:
			if component.ID != t.GetID() {
				agents = append(agents, component)
			}
		}
	}

	response := map[string]interface{}{
		"discovery_summary": map[string]interface{}{
			"total_components": len(allComponents),
			"tools":            len(tools),
			"agents":           len(agents),
			"discovery_time":   time.Now().Format(time.RFC3339),
		},
		"tools":  tools,
		"agents": agents,
	}

	writeJSON(w, http.StatusOK, response)
}

// Helper functions

// setCORSHeaders sets CORS headers for cross-origin requests.
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-User-ID")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
}

// writeJSON writes a JSON response with CORS headers.
func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	setCORSHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

// writeError writes an error response with CORS headers.
func writeError(w http.ResponseWriter, statusCode int, message string, err error) {
	response := map[string]interface{}{
		"error":   message,
		"status":  statusCode,
		"success": false,
	}
	if err != nil {
		response["details"] = err.Error()
	}
	setCORSHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

// extractPathParam extracts a path parameter from a URL path.
func extractPathParam(path, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	param := strings.TrimPrefix(path, prefix)
	// Remove any trailing path segments
	if idx := strings.Index(param, "/"); idx != -1 {
		param = param[:idx]
	}
	return param
}

// getUserID extracts the user ID from the X-User-ID header.
func getUserID(r *http.Request) string {
	return r.Header.Get("X-User-ID")
}

// handleListSessions lists sessions for a user with pagination.
func (t *DevOpsChatAgent) handleListSessions(w http.ResponseWriter, r *http.Request) {
	// Handle CORS preflight
	if r.Method == http.MethodOptions {
		setCORSHeaders(w)
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Only GET requests are supported", nil)
		return
	}

	userID := getUserID(r)
	if userID == "" {
		writeError(w, http.StatusBadRequest, "X-User-ID header is required", nil)
		return
	}

	// Parse pagination params
	offset := 0
	limit := 20
	if v := r.URL.Query().Get("offset"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	sessions, total, err := t.sessionStore.List(userID, offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to list sessions", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": sessions,
		"total":    total,
	})
}

// handleUpdateTitle updates the title of a session.
func (t *DevOpsChatAgent) handleUpdateTitle(w http.ResponseWriter, r *http.Request) {
	// Handle CORS preflight
	if r.Method == http.MethodOptions {
		setCORSHeaders(w)
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "Only PUT requests are supported", nil)
		return
	}

	// Extract session ID from URL path: /chat/session/{id}/title
	path := strings.TrimPrefix(r.URL.Path, "/chat/session/")
	path = strings.TrimSuffix(path, "/title")
	sessionID := path

	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "Session ID is required", nil)
		return
	}

	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if body.Title == "" {
		writeError(w, http.StatusBadRequest, "Title is required", nil)
		return
	}

	if err := t.sessionStore.UpdateTitle(sessionID, body.Title); err != nil {
		writeError(w, http.StatusNotFound, "Session not found or expired", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
	})
}

func (t *DevOpsChatAgent) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	// Handle CORS preflight
	if r.Method == http.MethodOptions {
		setCORSHeaders(w)
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Only POST requests are supported", nil)
		return
	}

	// Parse session ID from request body
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if body.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required", nil)
		return
	}

	session := t.sessionStore.Get(body.SessionID)
	if session == nil {
		writeError(w, http.StatusNotFound, "Session not found or expired", nil)
		return
	}

	t.sessionStore.Delete(body.SessionID)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
	})
}

// handleQuery handles non-streaming queries for agent-to-agent delegation.
// Follows the Non-Streaming Handler pattern from AGENT_DEVELOPMENT_GUIDE.md §7.
//
// Request formats (all accepted):
//
//	Orchestrator: POST /query  {"data": {"query": "..."}}                          — callAgentService wrapping
//	Orchestrator: POST /query  {"data": {"query": "...", "session_id": "..."}}
//	Direct:       POST /query  {"query": "..."}                                    — curl / testing
//	Direct:       POST /query  {"message": "..."}                                  — alternative field name
//
// Response follows OrchestrationResponse from guide (line 662-671):
//
//	{"request_id", "request", "response", "tools_used", "execution_time", "confidence", "metadata"}
func (t *DevOpsChatAgent) handleQuery(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// Handle CORS preflight
	if r.Method == http.MethodOptions {
		setCORSHeaders(w)
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Only POST requests are supported", nil)
		return
	}

	// 1. Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("operation", "agent_delegation_query"),
	)

	// 2. Log with trace context for correlation
	t.Logger.InfoWithContext(ctx, "Processing delegation query", map[string]interface{}{
		"operation": "query",
	})

	// 3. Parse and validate request
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "Request body is required", nil)
		return
	}
	var raw map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		telemetry.RecordSpanError(ctx, err)
		t.Logger.ErrorWithContext(ctx, "Failed to decode query request", map[string]interface{}{
			"operation": "query",
			"error":     err.Error(),
		})
		writeError(w, http.StatusBadRequest, "Invalid JSON request body", err)
		return
	}

	// Extract query and session_id from either wrapped {"data": {...}} or flat format.
	// The orchestrator's callAgentService() wraps params: {"data": {"query": "..."}}.
	// Direct calls send flat: {"query": "..."} or {"message": "..."}.
	var query, sessionID string

	if dataRaw, ok := raw["data"]; ok {
		if data, ok := dataRaw.(map[string]interface{}); ok {
			if q, ok := data["query"].(string); ok {
				query = q
			} else if m, ok := data["message"].(string); ok {
				query = m
			}
			if s, ok := data["session_id"].(string); ok {
				sessionID = s
			}
		}
	}
	if query == "" {
		if q, ok := raw["query"].(string); ok {
			query = q
		} else if m, ok := raw["message"].(string); ok {
			query = m
		}
	}
	if sessionID == "" {
		if s, ok := raw["session_id"].(string); ok {
			sessionID = s
		}
	}

	if query == "" {
		writeError(w, http.StatusBadRequest, "query field is required", nil)
		return
	}

	// 4. Check orchestrator availability (Best Practice #4: return 503 if initializing)
	if t.GetOrchestrator() == nil {
		writeError(w, http.StatusServiceUnavailable, "Orchestrator initializing", nil)
		return
	}

	// Store user message in session (if session provided)
	if sessionID != "" {
		t.sessionStore.AddMessage(sessionID, Message{
			Role:      "user",
			Content:   query,
			Timestamp: time.Now(),
			Metadata:  map[string]interface{}{"source": "agent_delegation"},
		})
	}

	// 5. Process through AI orchestrator
	telemetry.AddSpanEvent(ctx, "orchestration_started",
		attribute.String("query", query),
	)

	result, err := t.ProcessQuery(ctx, sessionID, query)
	if err != nil {
		// Note: ProcessQuery already calls telemetry.RecordSpanError, so we only log here
		t.Logger.ErrorWithContext(ctx, "Query orchestration failed", map[string]interface{}{
			"operation":   "query",
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		writeError(w, http.StatusInternalServerError, "Orchestration failed", err)
		return
	}

	// 6. Set request_id as span attribute for Jaeger searchability (Pattern 3)
	//    This makes the handler-level span findable via: Tags → request_id=<value>
	telemetry.SetSpanAttributes(ctx, attribute.String("request_id", result.RequestID))

	// 7. Build response — matches OrchestrationResponse from guide (line 662-671)
	response := map[string]interface{}{
		"request_id":     result.RequestID,
		"request":        query,
		"response":       result.Response,
		"tools_used":     result.AgentsInvolved,
		"execution_time": time.Since(startTime).String(),
		"confidence":     result.Confidence,
		"metadata":       result.Metadata,
	}
	if len(result.Steps) > 0 {
		response["steps"] = result.Steps
	}
	if result.Usage != nil {
		response["usage"] = result.Usage
		response["usage_by_phase"] = result.UsageByPhase
	}

	// 8. Record metrics
	durationMs := float64(time.Since(startTime).Milliseconds())
	telemetry.Counter("chat.requests", "status", "success", "module", "devops-chat-agent")
	telemetry.RecordRequest("devops-chat-agent", "query", durationMs, "success")

	// 9. Add completion span event (request_id first per Pattern 6)
	telemetry.AddSpanEvent(ctx, "orchestration_completed",
		attribute.String("request_id", result.RequestID),
		attribute.Int("tools_used", len(result.AgentsInvolved)),
		attribute.Float64("confidence", result.Confidence),
		attribute.String("execution_time", time.Since(startTime).String()),
	)

	// 10. Log completion
	t.Logger.InfoWithContext(ctx, "Query completed", map[string]interface{}{
		"operation":   "query",
		"request_id":  result.RequestID,
		"tools_used":  len(result.AgentsInvolved),
		"duration_ms": int64(durationMs),
		"status":      "success",
	})

	writeJSON(w, http.StatusOK, response)
}
