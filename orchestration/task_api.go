// Package orchestration provides HTTP API handlers for async tasks.
//
// This file implements the REST API endpoints for the async task system:
//   - POST /api/v1/tasks          - Submit a new task
//   - GET  /api/v1/tasks/:id       - Get task status and result
//   - POST /api/v1/tasks/:id/cancel - Cancel a task
package orchestration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// TaskAPIHandler provides HTTP handlers for async task operations.
type TaskAPIHandler struct {
	queue  core.TaskQueue
	store  core.TaskStore
	logger core.Logger
}

// NewTaskAPIHandler creates a new task API handler.
func NewTaskAPIHandler(queue core.TaskQueue, store core.TaskStore, logger core.Logger) *TaskAPIHandler {
	h := &TaskAPIHandler{
		queue:  queue,
		store:  store,
		logger: logger,
	}

	// Apply component-aware logging
	if h.logger != nil {
		if cal, ok := h.logger.(core.ComponentAwareLogger); ok {
			h.logger = cal.WithComponent("framework/orchestration")
		}
	}

	return h
}

// SetLogger sets the logger for API operations.
func (h *TaskAPIHandler) SetLogger(logger core.Logger) {
	if logger != nil {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			h.logger = cal.WithComponent("framework/orchestration")
		} else {
			h.logger = logger
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Request/Response Types
// ═══════════════════════════════════════════════════════════════════════════

// TaskSubmitRequest is the request body for task submission.
type TaskSubmitRequest struct {
	// Type is the task type (required)
	Type string `json:"type"`

	// Input is the task parameters
	Input map[string]interface{} `json:"input"`

	// Timeout is the optional task timeout (e.g., "10m", "1h")
	Timeout string `json:"timeout,omitempty"`
}

// TaskSubmitResponse is the response for task submission.
type TaskSubmitResponse struct {
	// TaskID is the unique task identifier
	TaskID string `json:"task_id"`

	// Status is the initial task status
	Status string `json:"status"`

	// StatusURL is the URL to check task status
	StatusURL string `json:"status_url"`
}

// TaskStatusResponse is the response for task status queries.
type TaskStatusResponse struct {
	// TaskID is the unique task identifier
	TaskID string `json:"task_id"`

	// Type is the task type
	Type string `json:"type"`

	// Status is the current task status
	Status string `json:"status"`

	// Progress contains progress information if available
	Progress *core.TaskProgress `json:"progress,omitempty"`

	// Result contains the task result if completed
	Result interface{} `json:"result,omitempty"`

	// Error contains error information if failed
	Error *core.TaskError `json:"error,omitempty"`

	// CreatedAt is when the task was submitted
	CreatedAt time.Time `json:"created_at"`

	// StartedAt is when processing began
	StartedAt *time.Time `json:"started_at,omitempty"`

	// CompletedAt is when the task finished
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TaskCancelResponse is the response for task cancellation.
type TaskCancelResponse struct {
	// TaskID is the task that was cancelled
	TaskID string `json:"task_id"`

	// Status is the new status (should be "cancelled")
	Status string `json:"status"`

	// Message provides additional information
	Message string `json:"message"`
}

// ErrorResponse is a standard error response.
type ErrorResponse struct {
	// Error is the error message
	Error string `json:"error"`

	// Code is an optional error code
	Code string `json:"code,omitempty"`
}

// ═══════════════════════════════════════════════════════════════════════════
// HTTP Handlers
// ═══════════════════════════════════════════════════════════════════════════

// HandleSubmit handles POST /api/v1/tasks - submit a new task.
func (h *TaskAPIHandler) HandleSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse request
	var req TaskSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body", "INVALID_REQUEST")
		return
	}

	// Validate request
	if req.Type == "" {
		h.writeError(w, http.StatusBadRequest, "task type is required", "MISSING_TYPE")
		return
	}

	// Parse timeout if provided
	var timeout time.Duration
	if req.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(req.Timeout)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, "invalid timeout format", "INVALID_TIMEOUT")
			return
		}
	}

	// Generate task ID
	taskID := uuid.New().String()

	// Extract trace context from incoming request
	tc := telemetry.GetTraceContext(ctx)

	// Create task
	task := &core.Task{
		ID:           taskID,
		Type:         req.Type,
		Status:       core.TaskStatusQueued,
		Input:        req.Input,
		CreatedAt:    time.Now(),
		TraceID:      tc.TraceID,
		ParentSpanID: tc.SpanID,
	}

	if timeout > 0 {
		task.Options.Timeout = timeout
	}

	// Store task
	if err := h.store.Create(ctx, task); err != nil {
		if h.logger != nil {
			h.logger.ErrorWithContext(ctx, "Failed to create task", map[string]interface{}{
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
		h.writeError(w, http.StatusInternalServerError, "failed to create task", "STORE_ERROR")
		return
	}

	// Enqueue task
	if err := h.queue.Enqueue(ctx, task); err != nil {
		// Clean up stored task on queue failure
		_ = h.store.Delete(ctx, taskID)

		if h.logger != nil {
			h.logger.ErrorWithContext(ctx, "Failed to enqueue task", map[string]interface{}{
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
		h.writeError(w, http.StatusInternalServerError, "failed to enqueue task", "QUEUE_ERROR")
		return
	}

	// Emit telemetry
	EmitTaskSubmitted(ctx, task)

	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "Task submitted", map[string]interface{}{
			"task_id":   taskID,
			"task_type": req.Type,
			"trace_id":  tc.TraceID,
		})
	}

	// Return 202 Accepted
	resp := TaskSubmitResponse{
		TaskID:    taskID,
		Status:    string(core.TaskStatusQueued),
		StatusURL: fmt.Sprintf("/api/v1/tasks/%s", taskID),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(resp); err != nil && h.logger != nil {
		h.logger.ErrorWithContext(ctx, "Failed to encode response", map[string]interface{}{
			"task_id": taskID,
			"error":   err.Error(),
		})
	}
}

// HandleGetTask handles GET /api/v1/tasks/:id - get task status and result.
func (h *TaskAPIHandler) HandleGetTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract task ID from URL
	taskID := h.extractTaskID(r.URL.Path, "/api/v1/tasks/")
	if taskID == "" {
		h.writeError(w, http.StatusBadRequest, "task ID is required", "MISSING_TASK_ID")
		return
	}

	// Handle cancel suffix
	if strings.HasSuffix(taskID, "/cancel") {
		h.writeError(w, http.StatusMethodNotAllowed, "use POST for cancel", "METHOD_NOT_ALLOWED")
		return
	}

	// Get task from store
	task, err := h.store.Get(ctx, taskID)
	if err != nil {
		if err == core.ErrTaskNotFound {
			h.writeError(w, http.StatusNotFound, "task not found", "TASK_NOT_FOUND")
			return
		}
		if h.logger != nil {
			h.logger.ErrorWithContext(ctx, "Failed to get task", map[string]interface{}{
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
		h.writeError(w, http.StatusInternalServerError, "failed to get task", "STORE_ERROR")
		return
	}

	// Build response
	resp := TaskStatusResponse{
		TaskID:      task.ID,
		Type:        task.Type,
		Status:      string(task.Status),
		Progress:    task.Progress,
		CreatedAt:   task.CreatedAt,
		StartedAt:   task.StartedAt,
		CompletedAt: task.CompletedAt,
	}

	// Include result or error based on status
	if task.Status == core.TaskStatusCompleted {
		resp.Result = task.Result
	}
	if task.Status == core.TaskStatusFailed || task.Status == core.TaskStatusCancelled {
		resp.Error = task.Error
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil && h.logger != nil {
		h.logger.ErrorWithContext(ctx, "Failed to encode response", map[string]interface{}{
			"task_id": taskID,
			"error":   err.Error(),
		})
	}
}

// HandleCancel handles POST /api/v1/tasks/:id/cancel - cancel a task.
func (h *TaskAPIHandler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract task ID from URL (strip /cancel suffix)
	path := strings.TrimSuffix(r.URL.Path, "/cancel")
	taskID := h.extractTaskID(path, "/api/v1/tasks/")
	if taskID == "" {
		h.writeError(w, http.StatusBadRequest, "task ID is required", "MISSING_TASK_ID")
		return
	}

	// Cancel task
	if err := h.store.Cancel(ctx, taskID); err != nil {
		if err == core.ErrTaskNotFound {
			h.writeError(w, http.StatusNotFound, "task not found", "TASK_NOT_FOUND")
			return
		}
		if err == core.ErrTaskNotCancellable {
			h.writeError(w, http.StatusConflict, "task cannot be cancelled (already in terminal state)", "TASK_NOT_CANCELLABLE")
			return
		}
		if h.logger != nil {
			h.logger.ErrorWithContext(ctx, "Failed to cancel task", map[string]interface{}{
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
		h.writeError(w, http.StatusInternalServerError, "failed to cancel task", "STORE_ERROR")
		return
	}

	// Get updated task for response
	task, _ := h.store.Get(ctx, taskID)

	// Emit telemetry
	if task != nil {
		EmitTaskCancelled(ctx, task, 0)
	}

	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "Task cancelled", map[string]interface{}{
			"task_id": taskID,
		})
	}

	resp := TaskCancelResponse{
		TaskID:  taskID,
		Status:  string(core.TaskStatusCancelled),
		Message: "task cancelled successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil && h.logger != nil {
		h.logger.ErrorWithContext(ctx, "Failed to encode response", map[string]interface{}{
			"task_id": taskID,
			"error":   err.Error(),
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Route Registration
// ═══════════════════════════════════════════════════════════════════════════

// RegisterRoutes registers all task API routes with the given mux.
// Uses pattern matching for task ID extraction.
func (h *TaskAPIHandler) RegisterRoutes(mux *http.ServeMux) {
	// Submit task
	mux.HandleFunc("/api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			h.HandleSubmit(w, r)
			return
		}
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
	})

	// Get task / Cancel task (using prefix matching)
	mux.HandleFunc("/api/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			if r.Method == http.MethodPost {
				h.HandleCancel(w, r)
				return
			}
			h.writeError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
			return
		}

		if r.Method == http.MethodGet {
			h.HandleGetTask(w, r)
			return
		}
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
	})
}

// ═══════════════════════════════════════════════════════════════════════════
// Helper Functions
// ═══════════════════════════════════════════════════════════════════════════

// extractTaskID extracts the task ID from a URL path.
func (h *TaskAPIHandler) extractTaskID(path, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	// Remove any trailing path components
	if idx := strings.Index(id, "/"); idx > 0 {
		id = id[:idx]
	}
	return id
}

// writeError writes a JSON error response.
func (h *TaskAPIHandler) writeError(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Error encoding failures are logged but not returned since we're already in error handling
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: message,
		Code:  code,
	})
}
