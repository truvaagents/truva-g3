package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// =============================================================================
// HITLHandler - HTTP API for Human-in-the-Loop Operations
// =============================================================================
//
// HITLHandler provides HTTP endpoints for HITL operations:
//   - POST /hitl/command - Submit a command (approve, reject, edit, etc.)
//   - POST /hitl/resume/{checkpoint_id} - Resume workflow execution after command approval
//   - GET /hitl/checkpoints - List pending checkpoints
//   - GET /hitl/checkpoints/{id} - Get checkpoint details
//
// Non-Blocking Two-Phase API Flow:
//   1. POST /hitl/command → Returns ResumeResult (ShouldResume=true/false)
//   2. POST /hitl/resume/{checkpoint_id} → Actually resumes workflow execution
//
// This is the framework's reference implementation. Applications can use it
// directly or implement custom handlers for their specific infrastructure.
//
// Per DISTRIBUTED_TRACING_GUIDE.md, all endpoints propagate trace context.
// Per LOGGING_IMPLEMENTATION_GUIDE.md, logging uses WithContext for correlation.
//
// Usage:
//
//	handler := NewHITLHandler(controller, store,
//	    WithHITLHandlerLogger(logger),
//	)
//
//	// Register routes
//	mux.HandleFunc("/hitl/command", handler.HandleCommand)
//	mux.HandleFunc("/hitl/resume/", handler.HandleResume)
//	mux.HandleFunc("/hitl/checkpoints", handler.HandleListCheckpoints)
//	mux.HandleFunc("/hitl/checkpoints/", handler.HandleGetCheckpoint)
//
// =============================================================================

// HITLHandler provides HTTP API for HITL operations.
type HITLHandler struct {
	controller InterruptController
	store      CheckpointPersistence

	// Optional dependencies (injected per framework patterns)
	logger    core.Logger    // Defaults to NoOp
	telemetry core.Telemetry // Defaults to NoOp
}

// NewHITLHandler creates a new HITL HTTP handler.
// Returns concrete type per Go idiom "return structs, accept interfaces".
func NewHITLHandler(controller InterruptController, store CheckpointPersistence, opts ...HITLHandlerOption) *HITLHandler {
	h := &HITLHandler{
		controller: controller,
		store:      store,
		logger:     &core.NoOpLogger{}, // Safe default per framework
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// HITLHandlerOption configures optional dependencies for HITLHandler.
type HITLHandlerOption func(*HITLHandler)

// WithHITLHandlerLogger sets the logger for the HITL handler.
func WithHITLHandlerLogger(logger core.Logger) HITLHandlerOption {
	return func(h *HITLHandler) {
		if logger == nil {
			return
		}
		// Use ComponentAwareLogger for component-based log segregation (per LOGGING_IMPLEMENTATION_GUIDE.md)
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			h.logger = cal.WithComponent("framework/orchestration")
		} else {
			h.logger = logger
		}
	}
}

// WithHITLHandlerTelemetry sets the telemetry provider for the HITL handler.
func WithHITLHandlerTelemetry(t core.Telemetry) HITLHandlerOption {
	return func(h *HITLHandler) {
		h.telemetry = t
	}
}

// -----------------------------------------------------------------------------
// HTTP Handlers
// -----------------------------------------------------------------------------

// HandleCommand processes human command submissions (approve, reject, edit, etc.).
//
// Method: POST
// Path: /hitl/command
// Body: Command JSON
//
// Responses:
//   - 200 OK: Command processed successfully, returns ResumeResult
//   - 400 Bad Request: Invalid JSON or command validation failed
//   - 404 Not Found: Checkpoint not found
//   - 500 Internal Server Error: Processing error
func (h *HITLHandler) HandleCommand(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Only accept POST
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}

	// Add span event for tracing visibility
	telemetry.AddSpanEvent(ctx, "hitl.api.command.received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
	)

	// Parse command from request body
	var command Command
	if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
		telemetry.RecordSpanError(ctx, err)
		if h.logger != nil {
			h.logger.WarnWithContext(ctx, "Failed to decode command", map[string]interface{}{
				"operation": "hitl_api_command",
				"error":     err.Error(),
			})
		}
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %s", err.Error()))
		return
	}

	// Validate required fields
	if command.CheckpointID == "" {
		h.writeError(w, http.StatusBadRequest, "checkpoint_id is required")
		return
	}
	if command.Type == "" {
		h.writeError(w, http.StatusBadRequest, "type is required")
		return
	}

	// Validate command type
	if !isValidCommandType(command.Type) {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid command type: %s", command.Type))
		return
	}

	// Load checkpoint to get OriginalRequestID for trace correlation.
	// This allows searching all traces in a HITL conversation by original_request_id.
	checkpoint, err := h.store.LoadCheckpoint(ctx, command.CheckpointID)
	if err != nil {
		if IsCheckpointNotFound(err) {
			h.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		telemetry.RecordSpanError(ctx, err)
		if h.logger != nil {
			h.logger.ErrorWithContext(ctx, "Failed to load checkpoint for command", map[string]interface{}{
				"operation":     "hitl_api_command",
				"checkpoint_id": command.CheckpointID,
				"error":         err.Error(),
			})
		}
		h.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to load checkpoint: %s", err.Error()))
		return
	}

	// Set original_request_id for distributed trace correlation.
	// 1. Set baggage so child spans (Redis, controller operations) inherit the attribute
	// 2. Set span attribute on current span for direct searchability
	if checkpoint.OriginalRequestID != "" {
		ctx = telemetry.WithBaggage(ctx, "original_request_id", checkpoint.OriginalRequestID)
		telemetry.SetSpanAttributes(ctx, attribute.String("original_request_id", checkpoint.OriginalRequestID))
	}

	// Log command receipt
	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "Processing command", map[string]interface{}{
			"operation":           "hitl_api_command",
			"checkpoint_id":       command.CheckpointID,
			"command_type":        command.Type,
			"user_id":             command.UserID,
			"original_request_id": checkpoint.OriginalRequestID,
		})
	}

	// Process the command via controller (uses ctx with baggage for child span correlation)
	result, err := h.controller.ProcessCommand(ctx, &command)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)

		// Check for specific error types
		if IsCheckpointNotFound(err) {
			h.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if IsInvalidCommand(err) {
			h.writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		if h.logger != nil {
			h.logger.ErrorWithContext(ctx, "Failed to process command", map[string]interface{}{
				"operation":     "hitl_api_command",
				"checkpoint_id": command.CheckpointID,
				"command_type":  command.Type,
				"error":         err.Error(),
			})
		}
		h.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to process command: %s", err.Error()))
		return
	}
	// Add span event for successful processing
	telemetry.AddSpanEvent(ctx, "hitl.api.command.processed",
		attribute.String("checkpoint_id", command.CheckpointID),
		attribute.String("command_type", string(command.Type)),
		attribute.Bool("should_resume", result.ShouldResume),
	)

	// Record metric
	telemetry.Counter("orchestration.hitl.api.command_processed",
		"command_type", string(command.Type),
		"should_resume", fmt.Sprintf("%t", result.ShouldResume),
		"module", telemetry.ModuleOrchestration,
	)

	// Log success
	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "Command processed successfully", map[string]interface{}{
			"operation":     "hitl_api_command_complete",
			"checkpoint_id": command.CheckpointID,
			"command_type":  command.Type,
			"should_resume": result.ShouldResume,
		})
	}

	h.writeJSON(w, http.StatusOK, result)
}

// HandleListCheckpoints returns pending checkpoints awaiting human response.
//
// Method: GET
// Path: /hitl/checkpoints
// Query Parameters:
//   - request_id (optional): Filter by request ID
//   - limit (optional): Max results (default: 50)
//   - offset (optional): Pagination offset (default: 0)
//
// Responses:
//   - 200 OK: List of checkpoints
//   - 500 Internal Server Error: Storage error
func (h *HITLHandler) HandleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Only accept GET
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed, use GET")
		return
	}

	// Add span event for tracing visibility
	telemetry.AddSpanEvent(ctx, "hitl.api.list_checkpoints",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
	)

	// Build filter from query parameters
	filter := CheckpointFilter{
		RequestID: r.URL.Query().Get("request_id"),
		Status:    CheckpointStatusPending, // Only list pending by default
		Limit:     50,                      // Default limit
		Offset:    0,
	}

	// Parse limit if provided
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		var limit int
		if _, err := fmt.Sscanf(limitStr, "%d", &limit); err == nil && limit > 0 {
			filter.Limit = limit
		}
	}

	// Parse offset if provided
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		var offset int
		if _, err := fmt.Sscanf(offsetStr, "%d", &offset); err == nil && offset >= 0 {
			filter.Offset = offset
		}
	}

	// Log request
	if h.logger != nil {
		h.logger.DebugWithContext(ctx, "Listing checkpoints", map[string]interface{}{
			"operation":  "hitl_api_list",
			"request_id": filter.RequestID,
			"limit":      filter.Limit,
			"offset":     filter.Offset,
		})
	}

	// Query checkpoints from store
	checkpoints, err := h.store.ListPendingCheckpoints(ctx, filter)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if h.logger != nil {
			h.logger.ErrorWithContext(ctx, "Failed to list checkpoints", map[string]interface{}{
				"operation": "hitl_api_list",
				"error":     err.Error(),
			})
		}
		h.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list checkpoints: %s", err.Error()))
		return
	}

	// Build response
	response := &ListCheckpointsResponse{
		Checkpoints: checkpoints,
		Count:       len(checkpoints),
		Limit:       filter.Limit,
		Offset:      filter.Offset,
	}

	// Add span event for successful listing
	telemetry.AddSpanEvent(ctx, "hitl.api.list_checkpoints.complete",
		attribute.Int("count", len(checkpoints)),
		attribute.Int("limit", filter.Limit),
		attribute.Int("offset", filter.Offset),
	)

	// Log success
	if h.logger != nil {
		h.logger.DebugWithContext(ctx, "Checkpoints listed", map[string]interface{}{
			"operation": "hitl_api_list_complete",
			"count":     len(checkpoints),
		})
	}

	h.writeJSON(w, http.StatusOK, response)
}

// HandleResume triggers workflow execution resumption from a checkpoint.
//
// Method: POST
// Path: /hitl/resume/{checkpoint_id}
//
// This endpoint is called after ProcessCommand returns ShouldResume=true.
// The non-blocking two-phase API flow is:
//  1. POST /hitl/command → Returns ResumeResult (ShouldResume=true/false)
//  2. POST /hitl/resume/{checkpoint_id} → Actually resumes workflow execution
//
// Responses:
//   - 200 OK: Execution completed successfully, returns ExecutionResult
//   - 404 Not Found: Checkpoint not found or not in resumable state
//   - 400 Bad Request: Invalid checkpoint state for resumption
//   - 500 Internal Server Error: Execution error
func (h *HITLHandler) HandleResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Only accept POST
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}

	// Extract checkpoint ID from path
	// Expects path like /hitl/resume/{checkpoint_id}
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(pathParts) < 3 || pathParts[2] == "" {
		h.writeError(w, http.StatusBadRequest, "checkpoint_id is required in path")
		return
	}
	checkpointID := pathParts[2]

	// Add span event for tracing visibility
	telemetry.AddSpanEvent(ctx, "hitl.api.resume.received",
		attribute.String("checkpoint_id", checkpointID),
	)

	// Load checkpoint to get OriginalRequestID for trace correlation.
	// This allows searching all traces in a HITL conversation by original_request_id.
	checkpoint, loadErr := h.store.LoadCheckpoint(ctx, checkpointID)
	if loadErr != nil {
		if IsCheckpointNotFound(loadErr) {
			h.writeError(w, http.StatusNotFound, loadErr.Error())
			return
		}
		// Log warning but continue - ResumeExecution will handle the error
		if h.logger != nil {
			h.logger.WarnWithContext(ctx, "Failed to load checkpoint for trace correlation", map[string]interface{}{
				"operation":     "hitl_api_resume",
				"checkpoint_id": checkpointID,
				"error":         loadErr.Error(),
			})
		}
	} else if checkpoint.OriginalRequestID != "" {
		// Set original_request_id for distributed trace correlation.
		// 1. Set baggage so child spans (controller operations) inherit the attribute
		// 2. Set span attribute on current span for direct searchability
		ctx = telemetry.WithBaggage(ctx, "original_request_id", checkpoint.OriginalRequestID)
		telemetry.SetSpanAttributes(ctx, attribute.String("original_request_id", checkpoint.OriginalRequestID))
	}

	// Log request
	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "Resuming execution", map[string]interface{}{
			"operation":     "hitl_api_resume",
			"checkpoint_id": checkpointID,
		})
	}

	// Resume execution via controller (uses ctx with baggage for child span correlation)
	result, err := h.controller.ResumeExecution(ctx, checkpointID)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)

		// Check for specific error types
		if IsCheckpointNotFound(err) {
			h.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if IsCheckpointExpired(err) {
			h.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if IsInvalidCommand(err) {
			h.writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		if h.logger != nil {
			h.logger.ErrorWithContext(ctx, "Failed to resume execution", map[string]interface{}{
				"operation":     "hitl_api_resume",
				"checkpoint_id": checkpointID,
				"error":         err.Error(),
			})
		}
		h.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to resume execution: %s", err.Error()))
		return
	}

	// Add span event for successful resumption
	telemetry.AddSpanEvent(ctx, "hitl.api.resume.complete",
		attribute.String("checkpoint_id", checkpointID),
		attribute.Bool("success", result.Success),
	)

	// Record metric
	telemetry.Counter("orchestration.hitl.api.execution_resumed",
		"success", fmt.Sprintf("%t", result.Success),
		"module", telemetry.ModuleOrchestration,
	)

	// Log success
	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "Execution resumed successfully", map[string]interface{}{
			"operation":     "hitl_api_resume_complete",
			"checkpoint_id": checkpointID,
			"success":       result.Success,
			"steps_count":   len(result.Steps),
		})
	}

	h.writeJSON(w, http.StatusOK, result)
}

// HandleGetCheckpoint returns details for a specific checkpoint.
//
// Method: GET
// Path: /hitl/checkpoints/{id}
//
// Responses:
//   - 200 OK: Checkpoint details
//   - 404 Not Found: Checkpoint not found
//   - 500 Internal Server Error: Storage error
func (h *HITLHandler) HandleGetCheckpoint(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Only accept GET
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed, use GET")
		return
	}

	// Extract checkpoint ID from path
	// Expects path like /hitl/checkpoints/{id}
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(pathParts) < 3 || pathParts[2] == "" {
		h.writeError(w, http.StatusBadRequest, "checkpoint ID is required in path")
		return
	}
	checkpointID := pathParts[2]

	// Add span event for tracing visibility
	telemetry.AddSpanEvent(ctx, "hitl.api.get_checkpoint",
		attribute.String("checkpoint_id", checkpointID),
	)

	// Log request
	if h.logger != nil {
		h.logger.DebugWithContext(ctx, "Getting checkpoint", map[string]interface{}{
			"operation":     "hitl_api_get",
			"checkpoint_id": checkpointID,
		})
	}

	// Load checkpoint from store
	checkpoint, err := h.store.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)

		if IsCheckpointNotFound(err) {
			h.writeError(w, http.StatusNotFound, err.Error())
			return
		}

		if h.logger != nil {
			h.logger.ErrorWithContext(ctx, "Failed to load checkpoint", map[string]interface{}{
				"operation":     "hitl_api_get",
				"checkpoint_id": checkpointID,
				"error":         err.Error(),
			})
		}
		h.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to load checkpoint: %s", err.Error()))
		return
	}

	// Set original_request_id for distributed trace correlation.
	// 1. Set baggage for consistency with other handlers (though no significant child spans here)
	// 2. Set span attribute on current span for direct searchability
	if checkpoint.OriginalRequestID != "" {
		ctx = telemetry.WithBaggage(ctx, "original_request_id", checkpoint.OriginalRequestID)
		telemetry.SetSpanAttributes(ctx, attribute.String("original_request_id", checkpoint.OriginalRequestID))
	}

	// Add span event for successful load
	telemetry.AddSpanEvent(ctx, "hitl.api.get_checkpoint.complete",
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("status", string(checkpoint.Status)),
	)

	// Log success
	if h.logger != nil {
		h.logger.DebugWithContext(ctx, "Checkpoint retrieved", map[string]interface{}{
			"operation":     "hitl_api_get_complete",
			"checkpoint_id": checkpointID,
			"status":        checkpoint.Status,
		})
	}

	h.writeJSON(w, http.StatusOK, checkpoint)
}

// -----------------------------------------------------------------------------
// Response Types
// -----------------------------------------------------------------------------

// ListCheckpointsResponse is the response for the list checkpoints endpoint.
type ListCheckpointsResponse struct {
	Checkpoints []*ExecutionCheckpoint `json:"checkpoints"`
	Count       int                    `json:"count"`
	Limit       int                    `json:"limit"`
	Offset      int                    `json:"offset"`
}

// Note: ErrorResponse is defined in task_api.go and reused here

// -----------------------------------------------------------------------------
// Helper Methods
// -----------------------------------------------------------------------------

// writeJSON writes a JSON response with the given status code.
func (h *HITLHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Log encoding error but response headers already sent
		if h.logger != nil {
			h.logger.ErrorWithContext(context.Background(), "Failed to encode response", map[string]interface{}{
				"operation": "hitl_api_response",
				"error":     err.Error(),
			})
		}
	}
}

// writeError writes a JSON error response.
func (h *HITLHandler) writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Error intentionally ignored - we're already in error handling path
	_ = json.NewEncoder(w).Encode(&ErrorResponse{
		Error: message,
		Code:  http.StatusText(status),
	})
}

// isValidCommandType checks if a command type is valid.
func isValidCommandType(t CommandType) bool {
	switch t {
	case CommandApprove, CommandEdit, CommandReject, CommandSkip, CommandAbort, CommandRetry, CommandRespond:
		return true
	default:
		return false
	}
}

// -----------------------------------------------------------------------------
// Convenience Registration
// -----------------------------------------------------------------------------

// RegisterRoutes registers all HITL HTTP handlers with the given ServeMux.
// This is a convenience method for standard setups.
//
// Registered routes:
//   - POST /hitl/command
//   - POST /hitl/resume/{checkpoint_id}
//   - GET /hitl/checkpoints
//   - GET /hitl/checkpoints/{id}
func (h *HITLHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/hitl/command", h.HandleCommand)
	// Use prefix matching for resume (handles /hitl/resume/{checkpoint_id})
	mux.HandleFunc("/hitl/resume/", h.HandleResume)
	mux.HandleFunc("/hitl/checkpoints", h.HandleListCheckpoints)
	// Use prefix matching for checkpoint details (handles /hitl/checkpoints/{id})
	mux.HandleFunc("/hitl/checkpoints/", h.HandleGetCheckpoint)
}
