package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// StreamCallback defines the interface for SSE event callbacks.
// Includes SendCheckpoint for HITL support.
type StreamCallback interface {
	SendStatus(step, message string)
	SendStep(stepID, tool string, success bool, durationMs int64)
	SendChunk(text string)
	SendDone(requestID string, toolsUsed []string, totalDurationMs int64)
	SendError(code, message string, retryable bool)
	SendUsage(promptTokens, completionTokens, totalTokens int)
	SendFinish(reason string)
	SendCheckpoint(checkpoint *orchestration.ExecutionCheckpoint)
}

// SSEHandler handles Server-Sent Events for chat streaming.
type SSEHandler struct {
	agent *HITLChatAgent
}

// NewSSEHandler creates a new SSE handler.
func NewSSEHandler(agent *HITLChatAgent) *SSEHandler {
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
	logger  core.Logger
	ctx     context.Context
}

// NewSSECallback creates a new SSE callback.
func NewSSECallback(w http.ResponseWriter, flusher http.Flusher) *SSECallback {
	return &SSECallback{w: w, flusher: flusher}
}

// NewSSECallbackWithLogger creates a new SSE callback with contextual logging.
func NewSSECallbackWithLogger(w http.ResponseWriter, flusher http.Flusher, logger core.Logger, ctx context.Context) *SSECallback {
	return &SSECallback{w: w, flusher: flusher, logger: logger, ctx: ctx}
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
	if c.logger != nil && c.ctx != nil {
		c.logger.DebugWithContext(c.ctx, "Sending SSE step event", map[string]interface{}{
			"operation":   "sse_send_step",
			"step_id":     stepID,
			"tool":        tool,
			"success":     success,
			"duration_ms": durationMs,
		})
	}
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
	if c.logger != nil && c.ctx != nil {
		c.logger.DebugWithContext(c.ctx, "Sending SSE done event", map[string]interface{}{
			"operation":         "sse_send_done",
			"request_id":        requestID,
			"tools_used":        toolsUsed,
			"total_duration_ms": totalDurationMs,
		})
	}
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

// SendUsage sends token usage statistics.
func (c *SSECallback) SendUsage(promptTokens, completionTokens, totalTokens int) {
	c.sendEvent("usage", map[string]interface{}{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      totalTokens,
	})
}

// SendFinish sends the finish reason event.
func (c *SSECallback) SendFinish(reason string) {
	c.sendEvent("finish", map[string]interface{}{
		"reason": reason,
	})
}

// SendCheckpoint sends a HITL checkpoint event.
// This is sent when execution is paused for human approval.
func (c *SSECallback) SendCheckpoint(checkpoint *orchestration.ExecutionCheckpoint) {
	data := map[string]interface{}{
		"checkpoint_id":   checkpoint.CheckpointID,
		"request_id":      checkpoint.RequestID, // For trace correlation across HITL resumes
		"interrupt_point": checkpoint.InterruptPoint,
		"expires_at":      checkpoint.ExpiresAt,
		"status":          checkpoint.Status,
	}

	// Include decision info if available
	if checkpoint.Decision != nil {
		data["reason"] = checkpoint.Decision.Reason
		data["message"] = checkpoint.Decision.Message
		// Include decision metadata (contains trigger info for UI)
		if checkpoint.Decision.Metadata != nil {
			data["decision"] = map[string]interface{}{
				"reason":   checkpoint.Decision.Reason,
				"message":  checkpoint.Decision.Message,
				"priority": checkpoint.Decision.Priority,
				"metadata": checkpoint.Decision.Metadata,
			}
		}
	}

	// Include current step for step-level approvals (Scenario 2)
	// This is critical for the UI to show the specific step being approved
	if checkpoint.CurrentStep != nil {
		currentStepData := map[string]interface{}{
			"step_id":     checkpoint.CurrentStep.StepID,
			"agent_name":  checkpoint.CurrentStep.AgentName,
			"instruction": checkpoint.CurrentStep.Instruction,
			"namespace":   checkpoint.CurrentStep.Namespace,
		}
		if checkpoint.CurrentStep.Metadata != nil {
			currentStepData["metadata"] = checkpoint.CurrentStep.Metadata
		}
		data["current_step"] = currentStepData
	}

	// Include resolved parameters for step-level approvals (Scenario 2)
	// This shows the user the actual values that will be sent to the tool
	if checkpoint.ResolvedParameters != nil && len(checkpoint.ResolvedParameters) > 0 {
		data["resolved_parameters"] = checkpoint.ResolvedParameters
	}

	// Include completed steps for context (shows what has already executed)
	if checkpoint.CompletedSteps != nil && len(checkpoint.CompletedSteps) > 0 {
		completedSteps := make([]map[string]interface{}, len(checkpoint.CompletedSteps))
		for i, step := range checkpoint.CompletedSteps {
			completedSteps[i] = map[string]interface{}{
				"step_id":    step.StepID,
				"agent_name": step.AgentName,
				"success":    step.Success,
			}
		}
		data["completed_steps"] = completedSteps
	}

	// Include plan if available
	if checkpoint.Plan != nil {
		steps := make([]map[string]interface{}, len(checkpoint.Plan.Steps))
		for i, step := range checkpoint.Plan.Steps {
			steps[i] = map[string]interface{}{
				"step_id":     step.StepID,
				"tool":        step.AgentName,
				"instruction": step.Instruction,
				"namespace":   step.Namespace,
			}
			// Include metadata if available (may contain capability info)
			if step.Metadata != nil {
				steps[i]["metadata"] = step.Metadata
			}
		}
		data["plan"] = map[string]interface{}{
			"plan_id":          checkpoint.Plan.PlanID,
			"original_request": checkpoint.Plan.OriginalRequest,
			"steps":            steps,
			"step_count":       len(steps),
		}
	}

	c.sendEvent("checkpoint", data)
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

	// Handle CORS preflight FIRST
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With")
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
		attribute.String("operation", "chat_stream_hitl"),
	)

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
	w.Header().Set("X-Accel-Buffering", "no")

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

	// Create or get session
	userID := getUserID(r)
	sessionID := req.SessionID
	if sessionID == "" {
		session := h.agent.sessionStore.Create(userID, nil)
		sessionID = session.ID

		callback := NewSSECallback(w, flusher)
		callback.sendEvent("session", map[string]interface{}{
			"id": sessionID,
		})
	}

	// Validate session exists
	session := h.agent.sessionStore.Get(sessionID)
	if session == nil {
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

	// Create callback and process with contextual logging
	callback := NewSSECallbackWithLogger(w, flusher, h.agent.Logger, ctx)

	// Check if orchestrator is available
	if h.agent.GetOrchestrator() == nil {
		h.agent.Logger.WarnWithContext(ctx, "Orchestrator not available", map[string]interface{}{
			"operation":  "chat_stream",
			"session_id": sessionID,
		})
		callback.SendError("service_unavailable", "Orchestrator is initializing, please try again", true)
		return
	}

	// Set request mode for HITL expiry behavior
	// Streaming requests use implicit_deny by default (user saw dialog but didn't respond)
	ctx = orchestration.WithRequestMode(ctx, orchestration.RequestModeStreaming)

	// Process with streaming - may return ErrInterrupted for HITL
	if err := h.agent.ProcessWithStreaming(ctx, sessionID, req.Message, callback); err != nil {
		// Check if this is a HITL interrupt (NOT an error)
		if orchestration.IsInterrupted(err) {
			// The checkpoint has already been sent via SendCheckpoint in ProcessWithStreaming
			// Just log and return - don't send error
			h.agent.Logger.InfoWithContext(ctx, "SSE stream paused for HITL approval", map[string]interface{}{
				"operation":     "chat_stream",
				"session_id":    sessionID,
				"checkpoint_id": orchestration.GetCheckpointID(err),
				"duration_ms":   time.Since(startTime).Milliseconds(),
			})
			return
		}

		// Actual error - log and notify client
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
