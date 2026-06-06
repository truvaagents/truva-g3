package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// StreamCallback defines the interface for SSE event callbacks.
type StreamCallback interface {
	SendStatus(step, message string)
	SendStep(stepID, tool string, success bool, durationMs int64)
	SendChunk(text string)
	SendDone(requestID string, toolsUsed []string, totalDurationMs int64)
	SendError(code, message string, retryable bool)
	SendUsage(promptTokens, completionTokens, totalTokens int, byPhase map[string]core.TokenUsage)
	SendFinish(reason string)
}

// SSEHandler handles Server-Sent Events for chat streaming.
type SSEHandler struct {
	agent *TravelChatAgent
}

// NewSSEHandler creates a new SSE handler.
func NewSSEHandler(agent *TravelChatAgent) *SSEHandler {
	return &SSEHandler{agent: agent}
}

// ChatRequest represents an incoming chat request.
type ChatRequest struct {
	SessionID string                 `json:"session_id,omitempty"`
	Message   string                 `json:"message"`
	Options   map[string]interface{} `json:"options,omitempty"`
}

// SSECallback implements StreamCallback for SSE responses.
type SSECallback struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSECallback creates a new SSE callback.
func NewSSECallback(w http.ResponseWriter, flusher http.Flusher) *SSECallback {
	return &SSECallback{w: w, flusher: flusher}
}

// SendStatus sends a status update event.
func (c *SSECallback) SendStatus(step, message string) {
	c.sendEvent("status", map[string]interface{}{
		"step":    step,
		"message": message,
	})
}

// SendStep sends a step completion event.
func (c *SSECallback) SendStep(stepID, tool string, success bool, durationMs int64) {
	c.sendEvent("step", map[string]interface{}{
		"step_id":     stepID,
		"tool":        tool,
		"success":     success,
		"duration_ms": durationMs,
	})
}

// SendChunk sends a response text chunk.
func (c *SSECallback) SendChunk(text string) {
	c.sendEvent("chunk", map[string]interface{}{
		"text": text,
	})
}

// SendDone sends the completion event.
func (c *SSECallback) SendDone(requestID string, toolsUsed []string, totalDurationMs int64) {
	c.sendEvent("done", map[string]interface{}{
		"request_id":        requestID,
		"tools_used":        toolsUsed,
		"total_duration_ms": totalDurationMs,
	})
}

// SendError sends an error event.
func (c *SSECallback) SendError(code, message string, retryable bool) {
	c.sendEvent("error", map[string]interface{}{
		"code":      code,
		"message":   message,
		"retryable": retryable,
	})
}

// SendUsage sends token usage statistics with optional per-phase breakdown.
func (c *SSECallback) SendUsage(promptTokens, completionTokens, totalTokens int, byPhase map[string]core.TokenUsage) {
	data := map[string]interface{}{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      totalTokens,
	}
	if len(byPhase) > 0 {
		data["by_phase"] = byPhase
	}
	c.sendEvent("usage", data)
}

// SendFinish sends the finish reason event.
func (c *SSECallback) SendFinish(reason string) {
	c.sendEvent("finish", map[string]interface{}{
		"reason": reason,
	})
}

// sendEvent sends a generic SSE event.
func (c *SSECallback) sendEvent(eventType string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}

	fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", eventType, jsonData)
	c.flusher.Flush()
}

// ServeHTTP handles the SSE streaming endpoint.
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Handle CORS preflight FIRST (before any other processing)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With, X-User-ID")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "86400")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Add span event for request received
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "chat_stream"),
	)

	// Log with trace context
	h.agent.Logger.InfoWithContext(ctx, "SSE stream started", map[string]interface{}{
		"operation": "chat_stream",
		"method":    r.Method,
		"path":      r.URL.Path,
	})

	// Check if SSE is supported
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.agent.Logger.ErrorWithContext(ctx, "SSE not supported", map[string]interface{}{
			"operation": "chat_stream",
		})
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable Nginx buffering

	// Only accept POST requests
	if r.Method != http.MethodPost {
		callback := NewSSECallback(w, flusher)
		callback.SendError("method_not_allowed", "Only POST requests are supported", false)
		return
	}

	// Parse request
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.agent.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation": "chat_stream",
			"error":     err.Error(),
		})
		callback := NewSSECallback(w, flusher)
		callback.SendError("invalid_request", "Invalid JSON request body", false)
		return
	}

	// Validate request
	if req.Message == "" {
		callback := NewSSECallback(w, flusher)
		callback.SendError("validation_error", "Message is required", false)
		return
	}

	// Extract user ID from header (canonicalized — see getUserID)
	userID := getUserID(r)

	// Create or get session
	sessionID := req.SessionID
	if sessionID == "" {
		session := h.agent.sessionStore.Create(userID, nil)
		sessionID = session.ID

		// Send session event
		callback := NewSSECallback(w, flusher)
		callback.sendEvent("session", map[string]interface{}{
			"id": sessionID,
		})
	}

	// Validate session exists
	session := h.agent.sessionStore.Get(sessionID)
	if session == nil {
		// Create new session if not found
		session = h.agent.sessionStore.Create(userID, nil)
		sessionID = session.ID

		callback := NewSSECallback(w, flusher)
		callback.sendEvent("session", map[string]interface{}{
			"id": sessionID,
		})
	}

	// Store user message
	h.agent.sessionStore.AddMessage(sessionID, Message{
		Role:      "user",
		Content:   req.Message,
		Timestamp: time.Now(),
	})

	// Add span event for processing start
	telemetry.AddSpanEvent(ctx, "processing_started",
		attribute.String("session_id", sessionID),
		attribute.Int("message_length", len(req.Message)),
	)

	// Create callback and process
	callback := NewSSECallback(w, flusher)

	// Check if orchestrator is available
	if h.agent.GetOrchestrator() == nil {
		h.agent.Logger.WarnWithContext(ctx, "Orchestrator not available", map[string]interface{}{
			"operation":  "chat_stream",
			"session_id": sessionID,
		})
		callback.SendError("service_unavailable", "Orchestrator is initializing, please try again", true)
		return
	}

	// Process with streaming
	if err := h.agent.ProcessWithStreaming(ctx, sessionID, req.Message, callback); err != nil {
		h.agent.Logger.ErrorWithContext(ctx, "Stream processing failed", map[string]interface{}{
			"operation":   "chat_stream",
			"session_id":  sessionID,
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		callback.SendError("processing_failed", err.Error(), true)
		return
	}

	// Add completion span event
	telemetry.AddSpanEvent(ctx, "stream_completed",
		attribute.String("session_id", sessionID),
		attribute.Float64("duration_ms", float64(time.Since(startTime).Milliseconds())),
	)

	h.agent.Logger.InfoWithContext(ctx, "SSE stream completed", map[string]interface{}{
		"operation":   "chat_stream",
		"session_id":  sessionID,
		"duration_ms": time.Since(startTime).Milliseconds(),
		"status":      "success",
	})
}
