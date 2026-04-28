package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// handleCreateSession creates a new chat session.
func (t *HITLChatAgent) handleCreateSession(w http.ResponseWriter, r *http.Request) {
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

	// Parse optional metadata from request body
	var metadata map[string]interface{}
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
			metadata = nil
		}
	}

	// Create session
	userID := getUserID(r)
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
func (t *HITLChatAgent) handleGetSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Only GET requests are supported", nil)
		return
	}

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
func (t *HITLChatAgent) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Only GET requests are supported", nil)
		return
	}

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

// handleHealth returns health status with HITL and orchestrator metrics.
func (t *HITLChatAgent) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
		"service":   "agent-with-human-approval",
	}

	// Check Redis/Discovery connection
	if t.Discovery != nil {
		_, err := t.Discovery.Discover(ctx, core.DiscoveryFilter{})
		if err != nil {
			health["status"] = "degraded"
			health["redis"] = "unavailable"
			t.Logger.WarnWithContext(ctx, "Health check: Redis unavailable", map[string]interface{}{
				"error": err.Error(),
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

	// Check HITL status
	hitl := t.GetHITL()
	if hitl != nil {
		health["hitl"] = map[string]interface{}{
			"status":  "active",
			"enabled": true,
		}
	} else {
		health["hitl"] = "not configured"
	}

	// Check AI provider
	if t.AI != nil {
		health["ai_provider"] = "connected"
	} else {
		health["ai_provider"] = "not configured"
	}

	// Add session stats
	health["active_sessions"] = t.sessionStore.GetActiveSessionCount()

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
func (t *HITLChatAgent) handleDiscover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	t.Logger.InfoWithContext(ctx, "Discovering components", map[string]interface{}{
		"path": r.URL.Path,
	})

	if t.Discovery == nil {
		writeError(w, http.StatusServiceUnavailable, "Service discovery not configured", nil)
		return
	}

	allComponents, err := t.Discovery.Discover(ctx, core.DiscoveryFilter{})
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Discovery failed", map[string]interface{}{
			"error": err.Error(),
		})
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

// handleResumeSSE handles SSE streaming resume after HITL approval.
// This is a custom endpoint that returns SSE instead of JSON.
func (t *HITLChatAgent) handleResumeSSE(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Handle CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With, X-Truvag3-Original-Request-ID")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "86400")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Extract checkpoint ID from path
	checkpointID := extractPathParam(r.URL.Path, "/hitl/resume/")

	// Extract original request_id from header for trace correlation across HITL resumes
	// This header is sent by the UI to link all traces in a conversation in Jaeger
	originalRequestIDFromHeader := r.Header.Get("X-Truvag3-Original-Request-ID")
	if checkpointID == "" {
		http.Error(w, "Checkpoint ID required", http.StatusBadRequest)
		return
	}

	t.Logger.InfoWithContext(ctx, "SSE resume requested", map[string]interface{}{
		"operation":                       "hitl_resume_sse",
		"checkpoint_id":                   checkpointID,
		"original_request_id_from_header": originalRequestIDFromHeader, // Debug: check if UI sends header
	})

	// Check if SSE is supported
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	callback := NewSSECallbackWithLogger(w, flusher, t.Logger, ctx)

	// Get HITL infrastructure
	hitl := t.GetHITL()
	if hitl == nil {
		callback.SendError("hitl_not_configured", "HITL infrastructure not available", false)
		return
	}

	// Load checkpoint
	checkpoint, err := hitl.CheckpointStore.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to load checkpoint", map[string]interface{}{
			"operation":     "hitl_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		callback.SendError("checkpoint_not_found", "Checkpoint not found or expired", false)
		return
	}

	// Apply header override for original_request_id before BuildResumeContext (RC7-B6).
	// Priority: 1) Header from UI (already read above), 2) Checkpoint's OriginalRequestID.
	if originalRequestIDFromHeader != "" {
		checkpoint.OriginalRequestID = originalRequestIDFromHeader
	}

	// Verify checkpoint is in a resumable state before allocating a trace span
	if !orchestration.IsResumableStatus(checkpoint.Status) {
		callback.SendError("not_approved", "Checkpoint has not been approved", false)
		return
	}

	// BuildResumeContext creates the linked trace span and sets all required context values
	// (WithResumeMode, WithPlanOverride, WithCompletedSteps, WithPreResolvedParams,
	// WithRequestMode, WithMetadata, StartLinkedSpan, WithBaggage). Span is created HERE
	// so all subsequent span events fire on the linked resume span, not the outer HTTP span.
	ctx, endLinkedSpan, err := orchestration.BuildResumeContext(ctx, checkpoint)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to build resume context", map[string]interface{}{
			"operation":     "hitl_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		callback.SendError("invalid_checkpoint", err.Error(), false)
		return
	}
	defer endLinkedSpan()

	// Add telemetry for checkpoint loaded — fires on the linked resume span
	loadedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("status", string(checkpoint.Status)),
	}
	if checkpoint.Decision != nil {
		loadedAttrs = append(loadedAttrs,
			attribute.String("interrupt_reason", string(checkpoint.Decision.Reason)),
		)
	}
	// Use typed fields (populated by createCheckpoint after RC7-B2 deployed)
	if checkpoint.OriginalTraceID != "" {
		loadedAttrs = append(loadedAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
		if checkpoint.OriginalSpanID != "" {
			loadedAttrs = append(loadedAttrs, attribute.String("original_span_id", checkpoint.OriginalSpanID))
		}
	}
	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.loaded", loadedAttrs...)

	// Extract session ID from checkpoint metadata
	sessionID := ""
	if checkpoint.UserContext != nil {
		if sid, ok := checkpoint.UserContext["session_id"].(string); ok {
			sessionID = sid
		}
	}
	if sessionID == "" {
		// Generate new session if not found
		session := t.sessionStore.Create("", nil)
		sessionID = session.ID
		callback.sendEvent("session", map[string]interface{}{
			"id": sessionID,
		})
	}

	// Add span event with detailed checkpoint context
	resumeAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("session_id", sessionID),
		attribute.String("status", string(checkpoint.Status)),
		attribute.String("interrupt_point", string(checkpoint.InterruptPoint)),
	}

	// Include decision context (why approval was needed)
	if checkpoint.Decision != nil {
		resumeAttrs = append(resumeAttrs,
			attribute.String("interrupt_reason", string(checkpoint.Decision.Reason)),
			attribute.String("interrupt_message", checkpoint.Decision.Message),
		)
	}

	// Include plan info
	if checkpoint.Plan != nil {
		resumeAttrs = append(resumeAttrs,
			attribute.String("plan_id", checkpoint.Plan.PlanID),
			attribute.Int("step_count", len(checkpoint.Plan.Steps)),
		)
	}

	// Use typed field for original trace correlation
	if checkpoint.OriginalTraceID != "" {
		resumeAttrs = append(resumeAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}

	telemetry.AddSpanEvent(ctx, "hitl.resume_started", resumeAttrs...)

	callback.SendStatus("resuming", "Resuming execution after approval...")

	t.Logger.InfoWithContext(ctx, "Resume context built from checkpoint", map[string]interface{}{
		"operation":       "hitl_resume_sse",
		"checkpoint_id":   checkpointID,
		"plan_id":         checkpoint.Plan.PlanID,
		"step_count":      len(checkpoint.Plan.Steps),
		"completed_count": len(checkpoint.StepResults),
		"interrupt_point": checkpoint.InterruptPoint,
	})

	// Re-process the original request with HITL bypass
	if err := t.ProcessWithStreaming(ctx, sessionID, checkpoint.OriginalRequest, callback); err != nil {
		// Check if this is another HITL interrupt (step-level)
		if orchestration.IsInterrupted(err) {
			t.Logger.InfoWithContext(ctx, "Resume paused for step-level HITL", map[string]interface{}{
				"operation":         "hitl_resume_sse",
				"checkpoint_id":     checkpointID,
				"new_checkpoint_id": orchestration.GetCheckpointID(err),
			})
			return
		}

		t.Logger.ErrorWithContext(ctx, "Resume processing failed", map[string]interface{}{
			"operation":     "hitl_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
			"duration_ms":   time.Since(startTime).Milliseconds(),
		})
		callback.SendError("resume_failed", err.Error(), true)
		return
	}

	// Mark checkpoint as completed
	checkpoint.Status = orchestration.CheckpointStatusCompleted
	if err := hitl.CheckpointStore.SaveCheckpoint(ctx, checkpoint); err != nil {
		t.Logger.WarnWithContext(ctx, "Failed to mark checkpoint completed", map[string]interface{}{
			"operation":     "hitl_resume_sse",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
	}

	resumeCompletedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.Float64("duration_ms", float64(time.Since(startTime).Milliseconds())),
	}
	// Include original trace ID in completion event for easy reference
	if checkpoint.UserContext != nil {
		if originalTraceID, ok := checkpoint.UserContext["original_trace_id"].(string); ok && originalTraceID != "" {
			resumeCompletedAttrs = append(resumeCompletedAttrs, attribute.String("original_trace_id", originalTraceID))
		}
	}
	telemetry.AddSpanEvent(ctx, "hitl.resume_completed", resumeCompletedAttrs...)

	t.Logger.InfoWithContext(ctx, "SSE resume completed", map[string]interface{}{
		"operation":     "hitl_resume_sse",
		"checkpoint_id": checkpointID,
		"session_id":    sessionID,
		"duration_ms":   time.Since(startTime).Milliseconds(),
	})
}

// Helper functions

// setCORSHeaders sets CORS headers for cross-origin requests.
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-User-ID, X-Truvag3-Original-Request-ID")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
}

// getUserID extracts the user ID from the X-User-ID header.
func getUserID(r *http.Request) string {
	return r.Header.Get("X-User-ID")
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
	if idx := strings.Index(param, "/"); idx != -1 {
		param = param[:idx]
	}
	return param
}

// handleListSessions lists sessions for a user with pagination.
func (t *HITLChatAgent) handleListSessions(w http.ResponseWriter, r *http.Request) {
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

// handleDeleteSession deletes a chat session.
func (t *HITLChatAgent) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
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

// handleUpdateTitle updates the title of a session.
func (t *HITLChatAgent) handleUpdateTitle(w http.ResponseWriter, r *http.Request) {
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

// ============================================================================
// Non-Streaming (Sync) HITL Endpoints
// ============================================================================
// These endpoints provide JSON request/response for programmatic access to HITL.
// Use these for non-browser clients, API integrations, or testing.

// ChatSyncRequest represents the request body for POST /chat.
// Aligned with OrchestrationRequest from agent-with-orchestration for API consistency.
type ChatSyncRequest struct {
	// Request is the natural language query (required)
	Request string `json:"request"`

	// SessionID for conversation continuity (optional, chat-specific)
	SessionID string `json:"session_id,omitempty"`

	// Metadata provides additional context passed to orchestrator
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// handleChatSync handles non-streaming chat requests with HITL support.
// POST /chat
//
// Request body (aligned with OrchestrationRequest):
//
//	{
//	  "request": "What is the stock price of AAPL?",
//	  "session_id": "optional-session-id",
//	  "metadata": {"user_id": "123"}
//	}
//
// Response (normal completion):
//
//	{
//	  "request_id": "awhl-123",
//	  "session_id": "sess-456",
//	  "response": "The current price of AAPL is...",
//	  "tools_used": ["stock-market-tool"],
//	  "confidence": 0.95,
//	  "interrupted": false,
//	  "duration_ms": 1234
//	}
//
// Response (HITL interrupt):
//
//	{
//	  "request_id": "awhl-123",
//	  "session_id": "sess-456",
//	  "interrupted": true,
//	  "checkpoint": {
//	    "checkpoint_id": "cp-abc123",
//	    "plan": {...},
//	    "decision": {...}
//	  },
//	  "duration_ms": 500
//	}
func (t *HITLChatAgent) handleChatSync(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Add span event for Jaeger visibility (standard pattern across all handlers)
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "chat_sync"),
	)

	// Handle CORS
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Only POST requests are supported", nil)
		return
	}

	// Parse request
	var req ChatSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to decode chat sync request", map[string]interface{}{
			"error": err.Error(),
		})
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusBadRequest, "Invalid request format", err)
		return
	}

	if req.Request == "" {
		writeError(w, http.StatusBadRequest, "Request field is required", nil)
		return
	}

	// Get or create session
	userID := getUserID(r)
	sessionID := req.SessionID
	if sessionID == "" {
		session := t.sessionStore.Create(userID, nil)
		sessionID = session.ID
	} else {
		// Verify session exists
		if t.sessionStore.Get(sessionID) == nil {
			// Create new session with the provided ID
			session := t.sessionStore.Create(userID, nil)
			sessionID = session.ID
		}
	}

	// Add span event
	telemetry.AddSpanEvent(ctx, "chat.sync.started",
		attribute.String("session_id", sessionID),
		attribute.Int("request_length", len(req.Request)),
	)

	// Store user message in session
	t.sessionStore.AddMessage(sessionID, Message{
		Role:      "user",
		Content:   req.Request,
		Timestamp: time.Now(),
	})

	t.Logger.InfoWithContext(ctx, "Processing sync chat request", map[string]interface{}{
		"operation":      "chat_sync",
		"session_id":     sessionID,
		"request_length": len(req.Request),
	})

	// Process the request (pass metadata to ProcessSync)
	response, err := t.ProcessSync(ctx, sessionID, req.Request, req.Metadata)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Sync chat processing failed", map[string]interface{}{
			"operation":   "chat_sync",
			"session_id":  sessionID,
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusInternalServerError, "Chat processing failed", err)
		return
	}

	// Add completion span event
	telemetry.AddSpanEvent(ctx, "chat.sync.completed",
		attribute.String("session_id", sessionID),
		attribute.Bool("interrupted", response.Interrupted),
		attribute.Int64("duration_ms", response.DurationMs),
	)

	t.Logger.InfoWithContext(ctx, "Sync chat request completed", map[string]interface{}{
		"operation":   "chat_sync",
		"session_id":  sessionID,
		"request_id":  response.RequestID,
		"interrupted": response.Interrupted,
		"duration_ms": response.DurationMs,
	})

	// For initial requests, original_request_id == request_id (start of conversation)
	response.OriginalRequestID = response.RequestID

	writeJSON(w, http.StatusOK, response)
}

// handleResumeSyncJSON handles non-streaming resume after HITL approval.
// POST /hitl/resume-sync/{id}
//
// This endpoint resumes execution after a checkpoint has been approved
// and returns a JSON response (not SSE).
//
// Response (normal completion):
//
//	{
//	  "request_id": "awhl-123",
//	  "session_id": "sess-456",
//	  "response": "The current price of AAPL is...",
//	  "tools_used": ["stock-market-tool"],
//	  "confidence": 0.95,
//	  "interrupted": false,
//	  "duration_ms": 1234
//	}
//
// Response (step-level HITL interrupt):
//
//	{
//	  "request_id": "awhl-123",
//	  "session_id": "sess-456",
//	  "interrupted": true,
//	  "checkpoint": {...},
//	  "duration_ms": 500
//	}
func (t *HITLChatAgent) handleResumeSyncJSON(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Add span event for Jaeger visibility (standard pattern across all handlers)
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "hitl_resume_sync"),
	)

	// Handle CORS
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Only POST requests are supported", nil)
		return
	}

	// Extract checkpoint ID from path
	checkpointID := extractPathParam(r.URL.Path, "/hitl/resume-sync/")
	if checkpointID == "" {
		writeError(w, http.StatusBadRequest, "Checkpoint ID required", nil)
		return
	}

	// Extract original request_id from header for trace correlation across HITL resumes
	// This header is sent by the UI to link all traces in a conversation in Jaeger
	originalRequestIDFromHeader := r.Header.Get("X-Truvag3-Original-Request-ID")

	t.Logger.InfoWithContext(ctx, "Sync resume requested", map[string]interface{}{
		"operation":     "hitl_resume_sync",
		"checkpoint_id": checkpointID,
	})

	// Get HITL infrastructure
	hitl := t.GetHITL()
	if hitl == nil {
		writeError(w, http.StatusServiceUnavailable, "HITL infrastructure not available", nil)
		return
	}

	// Load checkpoint
	checkpoint, err := hitl.CheckpointStore.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to load checkpoint", map[string]interface{}{
			"operation":     "hitl_resume_sync",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusNotFound, "Checkpoint not found or expired", err)
		return
	}

	// Apply header override for original_request_id before BuildResumeContext (RC7-B7).
	if originalRequestIDFromHeader != "" {
		checkpoint.OriginalRequestID = originalRequestIDFromHeader
	}

	// Validate resumable state before allocating a trace span
	if !orchestration.IsResumableStatus(checkpoint.Status) {
		writeError(w, http.StatusConflict, "Checkpoint has not been approved", nil)
		return
	}

	// Validate checkpoint has required data for resume (before span allocation)
	if checkpoint.Plan == nil {
		t.Logger.ErrorWithContext(ctx, "Invalid checkpoint - no plan stored", map[string]interface{}{
			"operation":     "hitl_resume_sync",
			"checkpoint_id": checkpointID,
		})
		writeError(w, http.StatusBadRequest, "Checkpoint has no plan - cannot resume", nil)
		return
	}

	// BuildResumeContext creates the linked trace span and sets all required context values.
	// All subsequent span events fire on the linked resume span, not the outer HTTP span.
	ctx, endLinkedSpan, err := orchestration.BuildResumeContext(ctx, checkpoint)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to build resume context", map[string]interface{}{
			"operation":     "hitl_resume_sync",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
		})
		writeError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	defer endLinkedSpan()

	// Add telemetry for checkpoint loaded — fires on the linked resume span
	loadedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("status", string(checkpoint.Status)),
	}
	if checkpoint.Decision != nil {
		loadedAttrs = append(loadedAttrs,
			attribute.String("interrupt_reason", string(checkpoint.Decision.Reason)),
		)
	}
	// Use typed field for original trace correlation
	if checkpoint.OriginalTraceID != "" {
		loadedAttrs = append(loadedAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.loaded", loadedAttrs...)

	// Extract session ID from checkpoint metadata
	sessionID := ""
	if checkpoint.UserContext != nil {
		if sid, ok := checkpoint.UserContext["session_id"].(string); ok {
			sessionID = sid
		}
	}
	if sessionID == "" {
		// Create new session if not found
		session := t.sessionStore.Create("", nil)
		sessionID = session.ID
	}

	// Add span event for resume started
	resumeAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("session_id", sessionID),
		attribute.String("interrupt_point", string(checkpoint.InterruptPoint)),
	}
	if checkpoint.OriginalTraceID != "" {
		resumeAttrs = append(resumeAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.resume_sync.started", resumeAttrs...)

	// Re-process the original request with HITL bypass
	// Note: Original request metadata is not preserved in checkpoint, pass nil
	response, err := t.ProcessSync(ctx, sessionID, checkpoint.OriginalRequest, nil)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Sync resume processing failed", map[string]interface{}{
			"operation":     "hitl_resume_sync",
			"checkpoint_id": checkpointID,
			"error":         err.Error(),
			"duration_ms":   time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusInternalServerError, "Resume processing failed", err)
		return
	}

	// Mark checkpoint as completed (unless another interrupt occurred)
	if !response.Interrupted {
		checkpoint.Status = orchestration.CheckpointStatusCompleted
		if err := hitl.CheckpointStore.SaveCheckpoint(ctx, checkpoint); err != nil {
			t.Logger.WarnWithContext(ctx, "Failed to mark checkpoint completed", map[string]interface{}{
				"operation":     "hitl_resume_sync",
				"checkpoint_id": checkpointID,
				"error":         err.Error(),
			})
		}
	}

	// Add completion span event
	completionAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", checkpointID),
		attribute.Bool("interrupted", response.Interrupted),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	}
	if checkpoint.OriginalTraceID != "" {
		completionAttrs = append(completionAttrs, attribute.String("original_trace_id", checkpoint.OriginalTraceID))
	}
	telemetry.AddSpanEvent(ctx, "hitl.resume_sync.completed", completionAttrs...)

	t.Logger.InfoWithContext(ctx, "Sync resume completed", map[string]interface{}{
		"operation":     "hitl_resume_sync",
		"checkpoint_id": checkpointID,
		"session_id":    sessionID,
		"interrupted":   response.Interrupted,
		"duration_ms":   time.Since(startTime).Milliseconds(),
	})

	// Add original_request_id for end-to-end tracing (allows searching in Jaeger)
	response.OriginalRequestID = checkpoint.OriginalRequestID

	writeJSON(w, http.StatusOK, response)
}
