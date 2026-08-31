package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

const maxRequestBytes = 64 << 10

type API struct {
	workflow   orchestration.StateStore
	dispatcher core.TaskDispatcher
	queue      string
	workflowID string
	descriptor BackendDescriptor
}

func NewAPI(backends APIBackends) (*API, error) {
	if backends.Workflow == nil || backends.Dispatcher == nil {
		return nil, fmt.Errorf("live portability: API workflow and dispatcher backends are required")
	}
	return &API{
		workflow:   backends.Workflow,
		dispatcher: backends.Dispatcher,
		queue:      backends.Queue,
		workflowID: backends.WorkflowID,
		descriptor: backends.Descriptor,
	}, nil
}

func (api *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", api.health)
	mux.HandleFunc("GET /backends", api.backends)
	mux.HandleFunc("POST /tasks", api.submit)
	mux.HandleFunc("GET /tasks/{id}", api.get)
	return mux
}

func (api *API) RegisterHandlers(agent *core.BaseAgent) error {
	if agent == nil {
		return fmt.Errorf("live portability: agent is required")
	}
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /backends":   api.backends,
		"POST /tasks":     api.submit,
		"GET /tasks/{id}": api.get,
	} {
		if err := agent.HandleFunc(pattern, handler); err != nil {
			return fmt.Errorf("register %s: %w", pattern, err)
		}
	}
	return nil
}

func (api *API) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]interface{}{
		"status": "healthy",
		"mode":   "api",
	})
}

func (api *API) backends(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, api.descriptor)
}

func (api *API) submit(writer http.ResponseWriter, request *http.Request) {
	defer func() { _ = request.Body.Close() }()
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Location string `json:"location"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "body must be JSON with a location field")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(writer, http.StatusBadRequest, "invalid_request", "body must contain exactly one JSON object")
		return
	}
	input.Location = strings.TrimSpace(input.Location)
	if input.Location == "" || len(input.Location) > 200 {
		writeError(writer, http.StatusBadRequest, "invalid_location", "location must contain 1-200 characters")
		return
	}

	now := time.Now().UTC()
	id := uuid.NewString()
	execution := &orchestration.WorkflowExecution{
		ID:         id,
		WorkflowID: api.workflowID,
		Status:     orchestration.ExecutionPending,
		StartTime:  now,
		Inputs:     map[string]interface{}{"location": input.Location},
		Steps:      make(map[string]*orchestration.StepExecution),
		Context:    make(map[string]interface{}),
	}
	if err := api.workflow.SaveExecution(request.Context(), execution); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "workflow_save_failed", err.Error())
		return
	}
	task := core.NewTask(id, "portable-weather", map[string]interface{}{"location": input.Location})
	if err := api.dispatcher.Dispatch(request.Context(), api.queue, task); err != nil {
		finished := time.Now().UTC()
		execution.Status = orchestration.ExecutionFailed
		execution.EndTime = &finished
		execution.Outputs = map[string]interface{}{"error": err.Error()}
		_ = api.workflow.UpdateExecution(request.Context(), execution)
		writeError(writer, http.StatusServiceUnavailable, "dispatch_failed", err.Error())
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]interface{}{
		"execution_id": id,
		"status":       execution.Status,
		"status_url":   "/tasks/" + id,
	})
}

func (api *API) get(writer http.ResponseWriter, request *http.Request) {
	id := strings.TrimSpace(request.PathValue("id"))
	if id == "" {
		writeError(writer, http.StatusBadRequest, "invalid_execution_id", "execution ID is required")
		return
	}
	execution, err := api.workflow.GetExecution(request.Context(), id)
	if err != nil {
		writeError(writer, http.StatusNotFound, "execution_not_found", err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, execution)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]interface{}{
		"success": false,
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func writeJSON(writer http.ResponseWriter, status int, value interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
