// Package orchestration — capability handlers for the scheduler-tool.
//
// RegisterScheduleCapabilities mounts the 5 scheduling capabilities on any
// core.BaseTool. Agents discover these via the service catalog and call them
// through the SmartExecutor — the LLM sees scheduler-tool like any other
// tool (e.g., jira-tool, slack-tool).
//
// Same pattern as memory.BuildMemoryHooks — the framework provides the
// helper, the application wires it into a BaseTool in main.go.
//
// The helper depends only on core.ScheduleStore — no vendor imports, no
// Redis, no backend-specific code. The scheduler-tool example wires a
// Redis-backed ScheduleStore from the scheduler/ peer module, but any
// implementation satisfying the interface works identically.

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// RegisterScheduleCapabilities mounts all 5 scheduling capabilities on the
// given BaseTool:
//   - schedule_task   — create a new schedule
//   - list_schedules  — list existing schedules (optionally filtered by target)
//   - get_schedule    — fetch one schedule by ID
//   - update_schedule — partial update of a schedule's fields
//   - cancel_schedule — delete a schedule
//
// Usage (from an application's main.go):
//
//	tool := core.NewTool("scheduler-tool")
//	store := scheduler.NewRedisScheduleStore(redisClient)
//	orchestration.RegisterScheduleCapabilities(tool, store)
//
// The handlers use the standard core.ToolResponse envelope for all
// responses — success and error — so agents get consistent error semantics
// across the framework.
func RegisterScheduleCapabilities(tool *core.BaseTool, store core.ScheduleStore) {
	h := &scheduleCapabilityHandler{
		tool:  tool,
		store: store,
		// Standard 5-field cron syntax: minute | hour | day-of-month | month | day-of-week
		cronParser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
	}

	tool.RegisterCapability(core.Capability{
		Name: "schedule_task",
		Description: "Schedule a task for delayed, absolute, or recurring execution on a target agent. " +
			"Provide exactly one of cron_expr (recurring), run_at (absolute one-shot), " +
			"or delay (relative one-shot). The payload in 'input' is delivered verbatim " +
			"to the target agent when the schedule fires.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     h.handleScheduleTask,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name: "target_agent", Type: "string", Example: "travel-chat-agent",
					Description: "Automatically set to the calling agent — do not set this field. " +
						"The scheduled task will fire back to the current agent's orchestrator, " +
						"which will plan and execute the instruction using whatever tools are needed.",
				},
				{
					Name: "cron_expr", Type: "string", Example: "*/5 * * * *",
					Description: "Standard 5-field cron expression for recurring execution. Mutually exclusive with run_at and delay.",
				},
				{
					Name: "run_at", Type: "string", Example: "2026-04-10T15:00:00Z",
					Description: "Absolute fire time in RFC3339 format for one-shot execution. Mutually exclusive with cron_expr and delay.",
				},
				{
					Name: "delay", Type: "string", Example: "10m",
					Description: "Relative delay from now for one-shot execution (Go duration: '30s', '10m', '2h'). Mutually exclusive with cron_expr and run_at.",
				},
				{
					Name: "input", Type: "object",
					Description: "Payload delivered verbatim to the target agent when the schedule fires. Typically includes an 'instruction' field with the natural-language task description.",
				},
				{
					Name: "missed_run_policy", Type: "string", Example: "skip",
					Description: "Behavior when the scheduler was down: 'skip' (default, fire once on catch-up) or 'catchup' (fire once per missed interval).",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "schedule_id", Type: "string", Description: "Unique identifier of the created schedule."},
				{Name: "run_at", Type: "string", Description: "Computed next fire time in RFC3339 format."},
				{Name: "status", Type: "string", Description: "Always 'scheduled' on success."},
			},
		},
	})

	tool.RegisterCapability(core.Capability{
		Name:        "list_schedules",
		Description: "List all active scheduled tasks, optionally filtered by target agent name.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     h.handleListSchedules,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name: "target_agent", Type: "string", Example: "event-driven-agent",
					Description: "If provided, only return schedules whose target_agent matches.",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "schedules", Type: "array", Description: "Array of schedule objects."},
				{Name: "count", Type: "number", Description: "Number of schedules returned."},
			},
		},
	})

	tool.RegisterCapability(core.Capability{
		Name:        "get_schedule",
		Description: "Get full details of a specific schedule by ID.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     h.handleGetSchedule,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name: "schedule_id", Type: "string", Example: "sch-abc123def456",
					Description: "The schedule ID returned from schedule_task.",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "schedule", Type: "object", Description: "Full schedule object."},
			},
		},
	})

	tool.RegisterCapability(core.Capability{
		Name: "update_schedule",
		Description: "Update a schedule's payload, timing, target, or enabled state. " +
			"Only provided fields are changed; omitted fields retain their current values.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     h.handleUpdateSchedule,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "schedule_id", Type: "string", Example: "sch-abc123def456", Description: "The schedule ID to update."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "input", Type: "object", Description: "New payload delivered to the handler on each fire. Full replacement, not merge."},
				{Name: "cron_expr", Type: "string", Example: "*/10 * * * *", Description: "New cron expression. Recomputes next RunAt."},
				{Name: "run_at", Type: "string", Example: "2026-04-10T18:00:00Z", Description: "New absolute fire time (RFC3339). Clears cron_expr."},
				{Name: "delay", Type: "string", Example: "30m", Description: "New relative delay from now. Clears cron_expr."},
				{Name: "target_agent", Type: "string", Example: "devops-chat-agent", Description: "New target agent name. Must be an agent (not a tool)."},
				{Name: "enabled", Type: "boolean", Example: "false", Description: "Enable (true) or disable (false) the schedule without deleting it."},
				{Name: "missed_run_policy", Type: "string", Example: "catchup", Description: "'skip' or 'catchup'."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "status", Type: "string", Description: "Always 'updated' on success."},
				{Name: "schedule", Type: "object", Description: "The full updated schedule object."},
			},
		},
	})

	tool.RegisterCapability(core.Capability{
		Name:        "cancel_schedule",
		Description: "Permanently delete a scheduled task by ID. Cannot be undone.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     h.handleCancelSchedule,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "schedule_id", Type: "string", Example: "sch-abc123def456", Description: "The schedule ID to delete."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "status", Type: "string", Description: "Always 'deleted' on success."},
				{Name: "schedule_id", Type: "string", Description: "ID of the deleted schedule."},
			},
		},
	})
}

// scheduleCapabilityHandler holds the store reference and cron parser used
// by all 5 capability HTTP handlers.
//
// The *core.BaseTool reference is retained so handlers can read tool.Logger
// dynamically on each call. This matters because the framework reassigns
// tool.Logger inside core.NewFramework — captured snapshots taken at
// registration time would be the silent NoOpLogger. Reading the field on
// each call ensures handlers always use the current (production) logger.
type scheduleCapabilityHandler struct {
	tool       *core.BaseTool
	store      core.ScheduleStore
	cronParser cron.Parser
}

// logger returns the tool's current logger, falling back to NoOpLogger if
// the tool reference or its logger is nil. Centralised here so every handler
// gets the same nil-safety guarantee per LOGGING_IMPLEMENTATION_GUIDE §11.
func (h *scheduleCapabilityHandler) logger() core.Logger {
	if h.tool == nil || h.tool.Logger == nil {
		return &core.NoOpLogger{}
	}
	return h.tool.Logger
}

// ─────────────────────────────────────────────────────────────────────────
// Request bodies
// ─────────────────────────────────────────────────────────────────────────

type scheduleTaskRequest struct {
	TargetAgent     string                 `json:"target_agent"`
	CronExpr        string                 `json:"cron_expr,omitempty"`
	RunAt           string                 `json:"run_at,omitempty"`
	Delay           string                 `json:"delay,omitempty"`
	Input           map[string]interface{} `json:"input,omitempty"`
	MissedRunPolicy string                 `json:"missed_run_policy,omitempty"`
}

type listSchedulesRequest struct {
	TargetAgent string `json:"target_agent,omitempty"`
}

type idRequest struct {
	ScheduleID string `json:"schedule_id"`
}

// updateScheduleRequest uses pointer fields so that omitted fields are
// distinguishable from explicit zero values. Only non-nil fields are applied.
type updateScheduleRequest struct {
	ScheduleID      string                  `json:"schedule_id"`
	Input           *map[string]interface{} `json:"input,omitempty"`
	CronExpr        *string                 `json:"cron_expr,omitempty"`
	RunAt           *string                 `json:"run_at,omitempty"`
	Delay           *string                 `json:"delay,omitempty"`
	TargetAgent     *string                 `json:"target_agent,omitempty"`
	Enabled         *bool                   `json:"enabled,omitempty"`
	MissedRunPolicy *string                 `json:"missed_run_policy,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────
// Handlers
// ─────────────────────────────────────────────────────────────────────────

// handleScheduleTask implements POST /api/capabilities/schedule_task.
//
// Timing resolution priority: cron_expr > run_at > delay. Exactly one must
// be provided; otherwise 400.
func (h *scheduleCapabilityHandler) handleScheduleTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	setTraceHeaders(ctx, w)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "scheduler-tool"),
		attribute.String("truvag3.capability", "schedule_task"),
	)
	logger := h.logger()
	logger.InfoWithContext(ctx, "schedule_task request received", map[string]interface{}{
		"operation":  "schedule_task",
		"capability": "schedule_task",
	})

	var req scheduleTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_JSON",
			Message:   "failed to parse request body",
			Category:  core.CategoryInputError,
			Retryable: false,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}

	// Always set target_agent to the calling agent. The scheduled task must
	// fire back to the agent that requested it — the agent's orchestrator
	// will plan and execute the instruction using whatever tools are needed.
	// The LLM may set target_agent to a tool name (e.g., "slack-tool") based
	// on user intent, which is architecturally wrong — tools can't receive
	// scheduled tasks. The X-TruvaG3-Agent-Name header carries the correct
	// agent name, propagated via baggage from the orchestrator.
	callingAgent := r.Header.Get("X-TruvaG3-Agent-Name")
	if callingAgent != "" {
		req.TargetAgent = callingAgent
	}
	if req.TargetAgent == "" {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "MISSING_TARGET_AGENT",
			Message:   "could not determine calling agent (X-TruvaG3-Agent-Name header missing)",
			Category:  core.CategoryInputError,
			Retryable: false,
			Details:   map[string]string{"hint": "the orchestrator must propagate agent_name in context baggage"},
		})
		return
	}

	// Exactly one timing field required.
	timingCount := 0
	if req.CronExpr != "" {
		timingCount++
	}
	if req.RunAt != "" {
		timingCount++
	}
	if req.Delay != "" {
		timingCount++
	}
	if timingCount != 1 {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_TIMING",
			Message:   "exactly one of cron_expr, run_at, or delay is required",
			Category:  core.CategoryInputError,
			Retryable: true,
			Details: map[string]string{
				"hint": "provide cron_expr for recurring, run_at for absolute one-shot, or delay for relative one-shot",
			},
		})
		return
	}

	// Resolve the first RunAt based on the chosen timing field.
	now := time.Now()
	var runAt time.Time
	switch {
	case req.CronExpr != "":
		sched, err := h.cronParser.Parse(req.CronExpr)
		if err != nil {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_CRON",
				Message:   fmt.Sprintf("invalid cron expression: %s", err.Error()),
				Category:  core.CategoryInputError,
				Retryable: true,
				Details:   map[string]string{"cron_expr": req.CronExpr},
			})
			return
		}
		runAt = sched.Next(now)
		if runAt.IsZero() {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_CRON",
				Message:   "cron expression has no upcoming fire time",
				Category:  core.CategoryInputError,
				Retryable: false,
				Details:   map[string]string{"cron_expr": req.CronExpr},
			})
			return
		}
	case req.RunAt != "":
		parsed, err := time.Parse(time.RFC3339, req.RunAt)
		if err != nil {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_RUN_AT",
				Message:   fmt.Sprintf("run_at must be RFC3339 format: %s", err.Error()),
				Category:  core.CategoryInputError,
				Retryable: true,
				Details:   map[string]string{"run_at": req.RunAt, "example": "2026-04-10T15:00:00Z"},
			})
			return
		}
		runAt = parsed
	case req.Delay != "":
		d, err := time.ParseDuration(req.Delay)
		if err != nil {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_DELAY",
				Message:   fmt.Sprintf("delay must be a Go duration: %s", err.Error()),
				Category:  core.CategoryInputError,
				Retryable: true,
				Details:   map[string]string{"delay": req.Delay, "example": "10m"},
			})
			return
		}
		if d <= 0 {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_DELAY",
				Message:   "delay must be positive",
				Category:  core.CategoryInputError,
				Retryable: true,
			})
			return
		}
		runAt = now.Add(d)
	}

	// Resolve missed-run policy.
	missedPolicy := core.MissedRunSkip
	if req.MissedRunPolicy != "" {
		switch req.MissedRunPolicy {
		case string(core.MissedRunSkip), string(core.MissedRunCatchUp):
			missedPolicy = core.MissedRunPolicy(req.MissedRunPolicy)
		default:
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_MISSED_RUN_POLICY",
				Message:   fmt.Sprintf("missed_run_policy must be 'skip' or 'catchup', got %q", req.MissedRunPolicy),
				Category:  core.CategoryInputError,
				Retryable: true,
			})
			return
		}
	}

	// Who created this? Prefer the forwarded agent name from SmartExecutor,
	// then fall back to "api" for direct HTTP callers.
	createdBy := r.Header.Get("X-TruvaG3-Agent-Name")
	if createdBy == "" {
		createdBy = "api"
	}

	schedule := &core.Schedule{
		ID:              "sch-" + uuid.New().String()[:12],
		Input:           req.Input,
		TargetAgent:     req.TargetAgent,
		CronExpr:        req.CronExpr,
		RunAt:           runAt,
		Enabled:         true,
		MissedRunPolicy: missedPolicy,
		CreatedBy:       createdBy,
		CreatedAt:       now,
	}

	if err := h.store.Create(ctx, schedule); err != nil {
		if errors.Is(err, core.ErrScheduleAlreadyExists) {
			writeScheduleError(w, http.StatusConflict, &core.ToolError{
				Code:      "SCHEDULE_ALREADY_EXISTS",
				Message:   "a schedule with this ID already exists",
				Category:  core.CategoryInputError,
				Retryable: false,
				Details:   map[string]string{"schedule_id": schedule.ID},
			})
			return
		}
		logger.ErrorWithContext(ctx, "schedule_task store create failed", map[string]interface{}{
			"operation":   "schedule_task",
			"schedule_id": schedule.ID,
			"error":       err.Error(),
			"error_type":  "schedule_store_write",
		})
		writeScheduleError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "STORE_ERROR",
			Message:   "failed to persist schedule",
			Category:  core.CategoryServiceError,
			Retryable: true,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}

	EmitScheduleCreated(ctx, schedule)

	logger.InfoWithContext(ctx, "schedule_task created", map[string]interface{}{
		"operation":    "schedule_task",
		"schedule_id":  schedule.ID,
		"target_agent": schedule.TargetAgent,
		"cron_expr":    schedule.CronExpr,
		"run_at":       schedule.RunAt.Format(time.RFC3339),
		"status":       "success",
	})

	writeScheduleSuccess(w, http.StatusCreated, map[string]interface{}{
		"status":      "scheduled",
		"schedule_id": schedule.ID,
		"run_at":      schedule.RunAt.Format(time.RFC3339),
		"cron_expr":   schedule.CronExpr,
	})
}

// handleListSchedules implements POST /api/capabilities/list_schedules.
func (h *scheduleCapabilityHandler) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	setTraceHeaders(ctx, w)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "scheduler-tool"),
		attribute.String("truvag3.capability", "list_schedules"),
	)
	logger := h.logger()
	logger.InfoWithContext(ctx, "list_schedules request received", map[string]interface{}{
		"operation":  "list_schedules",
		"capability": "list_schedules",
	})

	var req listSchedulesRequest
	// Empty body is allowed.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_JSON",
				Message:   "failed to parse request body",
				Category:  core.CategoryInputError,
				Retryable: false,
				Details:   map[string]string{"error": err.Error()},
			})
			return
		}
	}

	schedules, err := h.store.List(ctx)
	if err != nil {
		logger.ErrorWithContext(ctx, "list_schedules store read failed", map[string]interface{}{
			"operation":  "list_schedules",
			"error":      err.Error(),
			"error_type": "schedule_store_read",
		})
		writeScheduleError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "STORE_ERROR",
			Message:   "failed to list schedules",
			Category:  core.CategoryServiceError,
			Retryable: true,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}

	// Optional filter by target_agent.
	if req.TargetAgent != "" {
		filtered := make([]*core.Schedule, 0, len(schedules))
		for _, s := range schedules {
			if s.TargetAgent == req.TargetAgent {
				filtered = append(filtered, s)
			}
		}
		schedules = filtered
	}

	writeScheduleSuccess(w, http.StatusOK, map[string]interface{}{
		"schedules": schedules,
		"count":     len(schedules),
	})
}

// handleGetSchedule implements POST /api/capabilities/get_schedule.
func (h *scheduleCapabilityHandler) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	setTraceHeaders(ctx, w)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "scheduler-tool"),
		attribute.String("truvag3.capability", "get_schedule"),
	)
	logger := h.logger()
	logger.InfoWithContext(ctx, "get_schedule request received", map[string]interface{}{
		"operation":  "get_schedule",
		"capability": "get_schedule",
	})

	var req idRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_JSON",
			Message:   "failed to parse request body",
			Category:  core.CategoryInputError,
			Retryable: false,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}
	if req.ScheduleID == "" {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "MISSING_SCHEDULE_ID",
			Message:   "schedule_id is required",
			Category:  core.CategoryInputError,
			Retryable: true,
		})
		return
	}

	schedule, err := h.store.Get(ctx, req.ScheduleID)
	if err != nil {
		if errors.Is(err, core.ErrScheduleNotFound) {
			writeScheduleError(w, http.StatusNotFound, &core.ToolError{
				Code:      "SCHEDULE_NOT_FOUND",
				Message:   "no schedule with the given ID",
				Category:  core.CategoryNotFound,
				Retryable: false,
				Details:   map[string]string{"schedule_id": req.ScheduleID},
			})
			return
		}
		logger.ErrorWithContext(ctx, "get_schedule store read failed", map[string]interface{}{
			"operation":   "get_schedule",
			"schedule_id": req.ScheduleID,
			"error":       err.Error(),
			"error_type":  "schedule_store_read",
		})
		writeScheduleError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "STORE_ERROR",
			Message:   "failed to retrieve schedule",
			Category:  core.CategoryServiceError,
			Retryable: true,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}

	writeScheduleSuccess(w, http.StatusOK, map[string]interface{}{
		"schedule": schedule,
	})
}

// handleUpdateSchedule implements POST /api/capabilities/update_schedule.
// Only provided fields are merged. Timing fields (cron_expr / run_at / delay)
// are mutually exclusive with each other within a single update call.
func (h *scheduleCapabilityHandler) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	setTraceHeaders(ctx, w)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "scheduler-tool"),
		attribute.String("truvag3.capability", "update_schedule"),
	)
	logger := h.logger()
	logger.InfoWithContext(ctx, "update_schedule request received", map[string]interface{}{
		"operation":  "update_schedule",
		"capability": "update_schedule",
	})

	var req updateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_JSON",
			Message:   "failed to parse request body",
			Category:  core.CategoryInputError,
			Retryable: false,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}
	if req.ScheduleID == "" {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "MISSING_SCHEDULE_ID",
			Message:   "schedule_id is required",
			Category:  core.CategoryInputError,
			Retryable: true,
		})
		return
	}

	// Reject conflicting timing fields in the same update.
	timingCount := 0
	if req.CronExpr != nil {
		timingCount++
	}
	if req.RunAt != nil {
		timingCount++
	}
	if req.Delay != nil {
		timingCount++
	}
	if timingCount > 1 {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_TIMING",
			Message:   "cron_expr, run_at, and delay are mutually exclusive in a single update",
			Category:  core.CategoryInputError,
			Retryable: true,
		})
		return
	}

	existing, err := h.store.Get(ctx, req.ScheduleID)
	if err != nil {
		if errors.Is(err, core.ErrScheduleNotFound) {
			writeScheduleError(w, http.StatusNotFound, &core.ToolError{
				Code:      "SCHEDULE_NOT_FOUND",
				Message:   "no schedule with the given ID",
				Category:  core.CategoryNotFound,
				Retryable: false,
				Details:   map[string]string{"schedule_id": req.ScheduleID},
			})
			return
		}
		logger.ErrorWithContext(ctx, "update_schedule pre-read failed", map[string]interface{}{
			"operation":   "update_schedule",
			"schedule_id": req.ScheduleID,
			"error":       err.Error(),
			"error_type":  "schedule_store_read",
		})
		writeScheduleError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "STORE_ERROR",
			Message:   "failed to retrieve schedule",
			Category:  core.CategoryServiceError,
			Retryable: true,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}

	now := time.Now()

	// Apply field updates.
	if req.Input != nil {
		existing.Input = *req.Input
	}
	if req.TargetAgent != nil {
		if *req.TargetAgent == "" {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_TARGET_QUEUE",
				Message:   "target_agent cannot be empty",
				Category:  core.CategoryInputError,
				Retryable: true,
			})
			return
		}
		existing.TargetAgent = *req.TargetAgent
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if req.MissedRunPolicy != nil {
		switch *req.MissedRunPolicy {
		case string(core.MissedRunSkip), string(core.MissedRunCatchUp):
			existing.MissedRunPolicy = core.MissedRunPolicy(*req.MissedRunPolicy)
		default:
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_MISSED_RUN_POLICY",
				Message:   fmt.Sprintf("missed_run_policy must be 'skip' or 'catchup', got %q", *req.MissedRunPolicy),
				Category:  core.CategoryInputError,
				Retryable: true,
			})
			return
		}
	}

	// Timing updates: if any of cron_expr / run_at / delay is provided,
	// recompute RunAt and adjust CronExpr accordingly.
	switch {
	case req.CronExpr != nil:
		sched, err := h.cronParser.Parse(*req.CronExpr)
		if err != nil {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_CRON",
				Message:   fmt.Sprintf("invalid cron expression: %s", err.Error()),
				Category:  core.CategoryInputError,
				Retryable: true,
				Details:   map[string]string{"cron_expr": *req.CronExpr},
			})
			return
		}
		existing.CronExpr = *req.CronExpr
		existing.RunAt = sched.Next(now)
	case req.RunAt != nil:
		parsed, err := time.Parse(time.RFC3339, *req.RunAt)
		if err != nil {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_RUN_AT",
				Message:   fmt.Sprintf("run_at must be RFC3339 format: %s", err.Error()),
				Category:  core.CategoryInputError,
				Retryable: true,
				Details:   map[string]string{"run_at": *req.RunAt},
			})
			return
		}
		existing.RunAt = parsed
		existing.CronExpr = "" // switching to one-shot
	case req.Delay != nil:
		d, err := time.ParseDuration(*req.Delay)
		if err != nil {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_DELAY",
				Message:   fmt.Sprintf("delay must be a Go duration: %s", err.Error()),
				Category:  core.CategoryInputError,
				Retryable: true,
				Details:   map[string]string{"delay": *req.Delay},
			})
			return
		}
		if d <= 0 {
			writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
				Code:      "INVALID_DELAY",
				Message:   "delay must be positive",
				Category:  core.CategoryInputError,
				Retryable: true,
			})
			return
		}
		existing.RunAt = now.Add(d)
		existing.CronExpr = "" // switching to one-shot
	}

	if err := h.store.Update(ctx, existing); err != nil {
		if errors.Is(err, core.ErrScheduleNotFound) {
			writeScheduleError(w, http.StatusNotFound, &core.ToolError{
				Code:      "SCHEDULE_NOT_FOUND",
				Message:   "schedule was deleted before update could complete",
				Category:  core.CategoryNotFound,
				Retryable: false,
				Details:   map[string]string{"schedule_id": req.ScheduleID},
			})
			return
		}
		logger.ErrorWithContext(ctx, "update_schedule store write failed", map[string]interface{}{
			"operation":   "update_schedule",
			"schedule_id": req.ScheduleID,
			"error":       err.Error(),
			"error_type":  "schedule_store_write",
		})
		writeScheduleError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "STORE_ERROR",
			Message:   "failed to persist schedule update",
			Category:  core.CategoryServiceError,
			Retryable: true,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}

	logger.InfoWithContext(ctx, "update_schedule applied", map[string]interface{}{
		"operation":   "update_schedule",
		"schedule_id": req.ScheduleID,
		"status":      "success",
	})

	writeScheduleSuccess(w, http.StatusOK, map[string]interface{}{
		"status":   "updated",
		"schedule": existing,
	})
}

// handleCancelSchedule implements POST /api/capabilities/cancel_schedule.
func (h *scheduleCapabilityHandler) handleCancelSchedule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	setTraceHeaders(ctx, w)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "scheduler-tool"),
		attribute.String("truvag3.capability", "cancel_schedule"),
	)
	logger := h.logger()
	logger.InfoWithContext(ctx, "cancel_schedule request received", map[string]interface{}{
		"operation":  "cancel_schedule",
		"capability": "cancel_schedule",
	})

	var req idRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_JSON",
			Message:   "failed to parse request body",
			Category:  core.CategoryInputError,
			Retryable: false,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}
	if req.ScheduleID == "" {
		writeScheduleError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "MISSING_SCHEDULE_ID",
			Message:   "schedule_id is required",
			Category:  core.CategoryInputError,
			Retryable: true,
		})
		return
	}

	if err := h.store.Delete(ctx, req.ScheduleID); err != nil {
		if errors.Is(err, core.ErrScheduleNotFound) {
			writeScheduleError(w, http.StatusNotFound, &core.ToolError{
				Code:      "SCHEDULE_NOT_FOUND",
				Message:   "no schedule with the given ID",
				Category:  core.CategoryNotFound,
				Retryable: false,
				Details:   map[string]string{"schedule_id": req.ScheduleID},
			})
			return
		}
		logger.ErrorWithContext(ctx, "cancel_schedule store delete failed", map[string]interface{}{
			"operation":   "cancel_schedule",
			"schedule_id": req.ScheduleID,
			"error":       err.Error(),
			"error_type":  "schedule_store_write",
		})
		writeScheduleError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "STORE_ERROR",
			Message:   "failed to delete schedule",
			Category:  core.CategoryServiceError,
			Retryable: true,
			Details:   map[string]string{"error": err.Error()},
		})
		return
	}

	EmitScheduleDeleted(ctx, req.ScheduleID)

	logger.InfoWithContext(ctx, "cancel_schedule deleted", map[string]interface{}{
		"operation":   "cancel_schedule",
		"schedule_id": req.ScheduleID,
		"status":      "success",
	})

	writeScheduleSuccess(w, http.StatusOK, map[string]interface{}{
		"status":      "deleted",
		"schedule_id": req.ScheduleID,
	})
}

// ─────────────────────────────────────────────────────────────────────────
// Response helpers
// ─────────────────────────────────────────────────────────────────────────

// writeScheduleSuccess writes a 2xx response wrapped in core.ToolResponse.
// Prefixed with "schedule" to avoid collision with future helpers of the
// same shape elsewhere in the orchestration package.
func writeScheduleSuccess(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    data,
	})
}

// writeScheduleError writes an error response wrapped in core.ToolResponse
// with the given HTTP status code. Prefixed with "schedule" to avoid
// collision with future helpers of the same shape elsewhere in the
// orchestration package.
func writeScheduleError(w http.ResponseWriter, status int, toolErr *core.ToolError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error:   toolErr,
	})
}

// setTraceHeaders echoes the current span's trace/span IDs on the response
// so callers can correlate distributed traces.
//
// Per CORE_DESIGN_PRINCIPLES.md §Interface Design, context.Context is always
// the first parameter.
func setTraceHeaders(ctx context.Context, w http.ResponseWriter) {
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}
}
