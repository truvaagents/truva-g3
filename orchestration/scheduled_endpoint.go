// Package orchestration — agent-side helper for receiving scheduled tasks
// from the scheduled-executor.
//
// RegisterScheduledEndpoint mounts /api/v1/scheduled on a BaseAgent. The
// endpoint accepts ScheduledRequest payloads from scheduled-executor and
// hands the instruction to the provided orchestrator as a fresh user query.
//
// This is the entire consumer side of the agent scheduling system. No
// worker pool, no task queue, no Runnable, no Redis. Just one HTTP handler.

package orchestration

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// ScheduledRequest is the JSON body POSTed by scheduled-executor to
// /api/v1/scheduled.
type ScheduledRequest struct {
	ScheduleID  string                 `json:"schedule_id"`
	TaskID      string                 `json:"task_id"`
	Instruction string                 `json:"instruction"`
	Input       map[string]interface{} `json:"input,omitempty"`
}

// ScheduledQueryBuilder builds the user-query string the orchestrator will
// process. Default reads ScheduledRequest.Instruction.
type ScheduledQueryBuilder func(req *ScheduledRequest) string

// ScheduledMetadataBuilder builds the metadata map passed to the
// orchestrator's ProcessRequest call.
type ScheduledMetadataBuilder func(req *ScheduledRequest) map[string]interface{}

// ScheduledFilter returns true if the request should be processed,
// false to acknowledge without invoking the orchestrator.
type ScheduledFilter func(req *ScheduledRequest) bool

type scheduledEndpointConfig struct {
	queryBuilder    ScheduledQueryBuilder
	metadataBuilder ScheduledMetadataBuilder
	filter          ScheduledFilter
	logger          core.Logger
}

// ScheduledEndpointOption is a functional option for RegisterScheduledEndpoint.
type ScheduledEndpointOption func(*scheduledEndpointConfig)

// WithScheduledQueryBuilder overrides how the user query string is extracted.
func WithScheduledQueryBuilder(fn ScheduledQueryBuilder) ScheduledEndpointOption {
	return func(c *scheduledEndpointConfig) {
		if fn != nil {
			c.queryBuilder = fn
		}
	}
}

// WithScheduledMetadataBuilder overrides how orchestrator metadata is built.
func WithScheduledMetadataBuilder(fn ScheduledMetadataBuilder) ScheduledEndpointOption {
	return func(c *scheduledEndpointConfig) {
		if fn != nil {
			c.metadataBuilder = fn
		}
	}
}

// WithScheduledFilter installs a predicate that decides whether to process
// the request. Returning false acknowledges with status "filtered" and
// does NOT call the orchestrator.
func WithScheduledFilter(fn ScheduledFilter) ScheduledEndpointOption {
	return func(c *scheduledEndpointConfig) {
		if fn != nil {
			c.filter = fn
		}
	}
}

// WithScheduledEndpointLogger overrides the logger used by the endpoint.
// By default, the handler reads agent.Logger dynamically on each call so
// the framework's post-NewFramework ProductionLogger is picked up.
func WithScheduledEndpointLogger(logger core.Logger) ScheduledEndpointOption {
	return func(c *scheduledEndpointConfig) {
		if logger != nil {
			c.logger = logger
		}
	}
}

type scheduledEndpointHandler struct {
	agent          *core.BaseAgent
	orchestratorFn OrchestratorFunc
	cfg            scheduledEndpointConfig
}

func (h *scheduledEndpointHandler) logger() core.Logger {
	if h.cfg.logger != nil {
		return h.cfg.logger
	}
	if h.agent != nil && h.agent.Logger != nil {
		return h.agent.Logger
	}
	return &core.NoOpLogger{}
}

// OrchestratorFunc is a function that returns the orchestrator at request
// time. This supports agents that initialize their orchestrator asynchronously
// (in a goroutine after framework.Run starts). The endpoint is registered
// before Run, and the orchestrator is resolved lazily on each request.
type OrchestratorFunc func() Orchestrator

// RegisterScheduledEndpoint mounts /api/v1/scheduled on the given agent.
// The orchestratorFn is called on each request to get the orchestrator —
// this supports async orchestrator initialization (the common pattern where
// agents init the orchestrator in a goroutine after Discovery is available).
//
// IMPORTANT: Call this BEFORE framework.Run() — HandleFunc rejects
// registrations after the HTTP server starts. The orchestratorFn can
// return nil during the startup window; the handler returns 503 until
// the orchestrator is ready.
//
// Layer 1 (convenience):
//
//	orchestration.RegisterScheduledEndpoint(agent.BaseAgent, agent.GetOrchestrator)
//
// Layer 2 (customisation):
//
//	orchestration.RegisterScheduledEndpoint(agent.BaseAgent, agent.GetOrchestrator,
//	    orchestration.WithScheduledFilter(myFilterFn),
//	)
//
// Layer 3 (full control):
//
//	agent.HandleFunc("/api/v1/scheduled", myCustomHandler)
func RegisterScheduledEndpoint(agent *core.BaseAgent, orchestratorFn OrchestratorFunc, opts ...ScheduledEndpointOption) error {
	if agent == nil {
		return errors.New("scheduled endpoint: agent is required")
	}
	if orchestratorFn == nil {
		return errors.New("scheduled endpoint: orchestratorFn is required")
	}

	cfg := scheduledEndpointConfig{
		queryBuilder:    defaultScheduledQueryBuilder,
		metadataBuilder: defaultScheduledMetadataBuilder,
		filter:          func(*ScheduledRequest) bool { return true },
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	h := &scheduledEndpointHandler{agent: agent, orchestratorFn: orchestratorFn, cfg: cfg}
	return agent.HandleFunc("/api/v1/scheduled", h.handle)
}

func (h *scheduledEndpointHandler) handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	setTraceHeaders(ctx, w)

	// Extract request_id from baggage (propagated by the executor via
	// telemetry.WithBaggage). Falls back to empty string if not present.
	bag := telemetry.GetBaggage(ctx)
	requestID := bag["request_id"]

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.endpoint", "scheduled"),
		attribute.String("request_id", requestID),
	)
	logger := h.logger()
	start := time.Now()

	var req ScheduledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.ErrorWithContext(ctx, "scheduled endpoint: decode failed", map[string]interface{}{
			"operation":  "scheduled_task_handle",
			"request_id": requestID,
			"error":      err.Error(),
			"error_type": "invalid_json",
		})
		writeScheduledError(w, http.StatusBadRequest, "INVALID_JSON", "failed to parse request body")
		return
	}

	logger.InfoWithContext(ctx, "scheduled request received", map[string]interface{}{
		"operation":   "scheduled_task_handle",
		"request_id":  requestID,
		"task_id":     req.TaskID,
		"schedule_id": req.ScheduleID,
	})

	if !h.cfg.filter(&req) {
		logger.InfoWithContext(ctx, "scheduled request filtered", map[string]interface{}{
			"operation":   "scheduled_task_handle",
			"request_id":  requestID,
			"task_id":     req.TaskID,
			"schedule_id": req.ScheduleID,
			"status":      "filtered",
		})
		writeScheduledSuccess(w, map[string]interface{}{
			"status":      "filtered",
			"task_id":     req.TaskID,
			"schedule_id": req.ScheduleID,
		})
		return
	}

	query := h.cfg.queryBuilder(&req)
	if query == "" {
		logger.ErrorWithContext(ctx, "scheduled request missing instruction", map[string]interface{}{
			"operation":   "scheduled_task_handle",
			"request_id":  requestID,
			"task_id":     req.TaskID,
			"schedule_id": req.ScheduleID,
			"error_type":  "missing_instruction",
		})
		writeScheduledError(w, http.StatusBadRequest, "MISSING_INSTRUCTION",
			"request must contain a non-empty instruction")
		return
	}

	metadata := h.cfg.metadataBuilder(&req)

	// Resolve orchestrator lazily — supports async init pattern where the
	// orchestrator is created in a goroutine after Discovery is available.
	orch := h.orchestratorFn()
	if orch == nil {
		logger.ErrorWithContext(ctx, "scheduled endpoint: orchestrator not ready", map[string]interface{}{
			"operation":   "scheduled_task_handle",
			"request_id":  requestID,
			"task_id":     req.TaskID,
			"schedule_id": req.ScheduleID,
			"error_type":  "orchestrator_not_ready",
		})
		writeScheduledError(w, http.StatusServiceUnavailable, "ORCHESTRATOR_NOT_READY",
			"agent orchestrator is still initializing — retry in a few seconds")
		return
	}

	resp, err := orch.ProcessRequest(ctx, query, metadata)
	duration := time.Since(start)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("truvag3.scheduled_executor.tasks_handled_total",
			"status", "failure",
			"module", "scheduled-executor",
		)
		logger.ErrorWithContext(ctx, "scheduled orchestrator error", map[string]interface{}{
			"operation":   "scheduled_task_handle",
			"request_id":  requestID,
			"task_id":     req.TaskID,
			"schedule_id": req.ScheduleID,
			"error":       err.Error(),
			"error_type":  "orchestrator_error",
			"duration_ms": duration.Milliseconds(),
		})
		writeScheduledError(w, http.StatusInternalServerError, "ORCHESTRATOR_ERROR", err.Error())
		return
	}

	telemetry.Counter("truvag3.scheduled_executor.tasks_handled_total",
		"status", "success",
		"module", "scheduled-executor",
	)
	telemetry.Histogram("truvag3.scheduled_executor.tasks_handled_duration_ms",
		float64(duration.Milliseconds()),
		"status", "success",
		"module", "scheduled-executor",
	)

	logger.InfoWithContext(ctx, "scheduled request handled", map[string]interface{}{
		"operation":   "scheduled_task_handle",
		"request_id":  requestID,
		"task_id":     req.TaskID,
		"schedule_id": req.ScheduleID,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
	})

	writeScheduledSuccess(w, map[string]interface{}{
		"response":   resp,
		"request_id": requestID,
	})
}

func defaultScheduledQueryBuilder(req *ScheduledRequest) string {
	if req == nil {
		return ""
	}
	return req.Instruction
}

func defaultScheduledMetadataBuilder(req *ScheduledRequest) map[string]interface{} {
	if req == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"scheduled_context": req.Input,
		"schedule_id":       req.ScheduleID,
		"task_id":           req.TaskID,
	}
}

func writeScheduledSuccess(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: data})
}

func writeScheduledError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:     code,
			Message:  message,
			Category: core.CategoryServiceError,
		},
	})
}
