// Package orchestration — unit tests for schedule_capabilities.go.
//
// Covers the 5 scheduler-tool HTTP capability handlers:
//   - schedule_task, list_schedules, get_schedule, update_schedule, cancel_schedule
//
// Plus the response writer helpers (writeScheduleSuccess, writeScheduleError)
// and setTraceHeaders.
//
// Tests use the mockScheduleStore defined in scheduler_test.go. The HTTP
// handlers are exercised via httptest.NewRecorder to verify status codes,
// response envelopes, and body contents.

package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ═══════════════════════════════════════════════════════════════════════════
// Test helpers
// ═══════════════════════════════════════════════════════════════════════════

// newTestHandler builds a scheduleCapabilityHandler with the same cron flags
// as the production RegisterScheduleCapabilities path.
func newTestHandler(store core.ScheduleStore) *scheduleCapabilityHandler {
	return &scheduleCapabilityHandler{
		store:      store,
		cronParser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
	}
}

// postJSON builds a POST http.Request with a JSON body.
func postJSON(body interface{}) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/capabilities/test", &buf)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// postRaw builds a POST http.Request with a raw body string. Used to test
// invalid JSON decode paths.
func postRaw(raw string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/capabilities/test", strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// decodeResp parses a core.ToolResponse from an HTTP response body.
func decodeResp(t *testing.T, body io.Reader) (bool, map[string]interface{}, *core.ToolError) {
	t.Helper()
	var resp struct {
		Success bool                   `json:"success"`
		Data    map[string]interface{} `json:"data,omitempty"`
		Error   *core.ToolError        `json:"error,omitempty"`
	}
	err := json.NewDecoder(body).Decode(&resp)
	require.NoError(t, err)
	return resp.Success, resp.Data, resp.Error
}

// ═══════════════════════════════════════════════════════════════════════════
// RegisterScheduleCapabilities — registers 5 capabilities on a BaseTool
// ═══════════════════════════════════════════════════════════════════════════

func TestRegisterScheduleCapabilities_RegistersAllFive(t *testing.T) {
	tool := core.NewTool("test-scheduler-tool")
	store := newMockScheduleStore()
	RegisterScheduleCapabilities(tool, store)

	caps := tool.GetCapabilities()
	names := make(map[string]bool)
	for _, c := range caps {
		names[c.Name] = true
	}

	assert.True(t, names["schedule_task"], "schedule_task must be registered")
	assert.True(t, names["list_schedules"], "list_schedules must be registered")
	assert.True(t, names["get_schedule"], "get_schedule must be registered")
	assert.True(t, names["update_schedule"], "update_schedule must be registered")
	assert.True(t, names["cancel_schedule"], "cancel_schedule must be registered")
}

// ═══════════════════════════════════════════════════════════════════════════
// handleScheduleTask
// ═══════════════════════════════════════════════════════════════════════════

func TestHandleScheduleTask_Delay_Success(t *testing.T) {
	store := newMockScheduleStore()
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"delay":        "10m",
		"input":        map[string]interface{}{"instruction": "do it"},
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
	success, data, toolErr := decodeResp(t, rr.Body)
	assert.True(t, success)
	assert.Nil(t, toolErr)
	require.NotNil(t, data)
	assert.Equal(t, "scheduled", data["status"])
	scheduleID, _ := data["schedule_id"].(string)
	assert.True(t, strings.HasPrefix(scheduleID, "sch-"), "schedule_id should start with sch-")
	assert.Equal(t, 1, store.createCalls)
}

func TestHandleScheduleTask_RunAt_Success(t *testing.T) {
	store := newMockScheduleStore()
	h := newTestHandler(store)

	future := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"run_at":       future,
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
	success, _, _ := decodeResp(t, rr.Body)
	assert.True(t, success)
}

func TestHandleScheduleTask_Cron_Success(t *testing.T) {
	store := newMockScheduleStore()
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"cron_expr":    "*/5 * * * *",
		"input":        map[string]interface{}{"instruction": "check health"},
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
	success, data, _ := decodeResp(t, rr.Body)
	assert.True(t, success)
	assert.Equal(t, "*/5 * * * *", data["cron_expr"])
}

func TestHandleScheduleTask_CatchUpPolicy_Accepted(t *testing.T) {
	store := newMockScheduleStore()
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"target_agent":      "agent-a",
		"cron_expr":         "*/5 * * * *",
		"missed_run_policy": "catchup",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
}

func TestHandleScheduleTask_SkipPolicy_Accepted(t *testing.T) {
	store := newMockScheduleStore()
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"target_agent":      "agent-a",
		"cron_expr":         "*/5 * * * *",
		"missed_run_policy": "skip",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
}

func TestHandleScheduleTask_InvalidMissedRunPolicy(t *testing.T) {
	store := newMockScheduleStore()
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"target_agent":      "agent-a",
		"cron_expr":         "*/5 * * * *",
		"missed_run_policy": "bogus",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_MISSED_RUN_POLICY", toolErr.Code)
}

func TestHandleScheduleTask_CreatedBy_FromHeader(t *testing.T) {
	store := newMockScheduleStore()
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"delay":        "5s",
	})
	req.Header.Set("X-TruvaG3-Agent-Name", "devops-chat-agent")
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)

	// Verify the store got a schedule with CreatedBy = devops-chat-agent.
	schedules, err := store.List(context.Background())
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	assert.Equal(t, "devops-chat-agent", schedules[0].CreatedBy)
}

func TestHandleScheduleTask_CreatedBy_DefaultsToAPI(t *testing.T) {
	store := newMockScheduleStore()
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"delay":        "5s",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	schedules, _ := store.List(context.Background())
	require.Len(t, schedules, 1)
	assert.Equal(t, "api", schedules[0].CreatedBy)
}

func TestHandleScheduleTask_InvalidJSON(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postRaw(`{"target_agent":`)
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_JSON", toolErr.Code)
}

func TestHandleScheduleTask_MissingTargetAgent(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"delay": "10m",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "MISSING_TARGET_AGENT", toolErr.Code)
}

func TestHandleScheduleTask_NoTiming(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_TIMING", toolErr.Code)
}

func TestHandleScheduleTask_MultipleTimings(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"delay":        "10m",
		"cron_expr":    "*/5 * * * *",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_TIMING", toolErr.Code)
}

func TestHandleScheduleTask_InvalidCron(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"cron_expr":    "not-a-cron",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_CRON", toolErr.Code)
}

func TestHandleScheduleTask_CronWithNoFutureFire(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		// Feb 31 — parses fine but has no future fire time.
		"cron_expr": "0 0 31 2 *",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_CRON", toolErr.Code)
}

func TestHandleScheduleTask_InvalidRunAt(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"run_at":       "not-a-time",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_RUN_AT", toolErr.Code)
}

func TestHandleScheduleTask_InvalidDelay_Format(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"delay":        "not-a-duration",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_DELAY", toolErr.Code)
}

func TestHandleScheduleTask_InvalidDelay_Negative(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"delay":        "-5s",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_DELAY", toolErr.Code)
}

func TestHandleScheduleTask_StoreAlreadyExists_Conflict(t *testing.T) {
	store := newMockScheduleStore()
	store.createErr = core.ErrScheduleAlreadyExists
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"delay":        "10m",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "SCHEDULE_ALREADY_EXISTS", toolErr.Code)
}

func TestHandleScheduleTask_StoreError_Internal(t *testing.T) {
	store := newMockScheduleStore()
	store.createErr = errors.New("store down")
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"target_agent": "agent-a",
		"delay":        "10m",
	})
	rr := httptest.NewRecorder()
	h.handleScheduleTask(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "STORE_ERROR", toolErr.Code)
}

// ═══════════════════════════════════════════════════════════════════════════
// handleListSchedules
// ═══════════════════════════════════════════════════════════════════════════

func TestHandleListSchedules_Empty(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(nil)
	req.ContentLength = 0
	rr := httptest.NewRecorder()
	h.handleListSchedules(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	success, data, _ := decodeResp(t, rr.Body)
	assert.True(t, success)
	assert.EqualValues(t, 0, data["count"])
}

func TestHandleListSchedules_ReturnsAll(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "s1", TargetAgent: "a", RunAt: time.Now(), Enabled: true,
	})
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "s2", TargetAgent: "b", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{})
	rr := httptest.NewRecorder()
	h.handleListSchedules(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	_, data, _ := decodeResp(t, rr.Body)
	assert.EqualValues(t, 2, data["count"])
}

func TestHandleListSchedules_FilterByTargetAgent(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "s1", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "s2", TargetAgent: "agent-b", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{"target_agent": "agent-a"})
	rr := httptest.NewRecorder()
	h.handleListSchedules(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	_, data, _ := decodeResp(t, rr.Body)
	assert.EqualValues(t, 1, data["count"])
}

func TestHandleListSchedules_InvalidJSON(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postRaw("{not-json")
	rr := httptest.NewRecorder()
	h.handleListSchedules(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_JSON", toolErr.Code)
}

func TestHandleListSchedules_StoreError(t *testing.T) {
	store := newMockScheduleStore()
	store.listErr = errors.New("store down")
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{})
	rr := httptest.NewRecorder()
	h.handleListSchedules(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "STORE_ERROR", toolErr.Code)
}

// ═══════════════════════════════════════════════════════════════════════════
// handleGetSchedule
// ═══════════════════════════════════════════════════════════════════════════

func TestHandleGetSchedule_Success(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-known", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{"schedule_id": "sch-known"})
	rr := httptest.NewRecorder()
	h.handleGetSchedule(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	success, data, _ := decodeResp(t, rr.Body)
	assert.True(t, success)
	assert.NotNil(t, data["schedule"])
}

func TestHandleGetSchedule_NotFound(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{"schedule_id": "does-not-exist"})
	rr := httptest.NewRecorder()
	h.handleGetSchedule(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "SCHEDULE_NOT_FOUND", toolErr.Code)
}

func TestHandleGetSchedule_MissingID(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{})
	rr := httptest.NewRecorder()
	h.handleGetSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "MISSING_SCHEDULE_ID", toolErr.Code)
}

func TestHandleGetSchedule_InvalidJSON(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postRaw("{broken")
	rr := httptest.NewRecorder()
	h.handleGetSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_JSON", toolErr.Code)
}

func TestHandleGetSchedule_StoreError(t *testing.T) {
	store := newMockScheduleStore()
	store.getErr = errors.New("store down")
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{"schedule_id": "anything"})
	rr := httptest.NewRecorder()
	h.handleGetSchedule(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "STORE_ERROR", toolErr.Code)
}

// ═══════════════════════════════════════════════════════════════════════════
// handleUpdateSchedule
// ═══════════════════════════════════════════════════════════════════════════

func TestHandleUpdateSchedule_InputOnly_Success(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u1", TargetAgent: "agent-a", CronExpr: "*/5 * * * *",
		RunAt: time.Now(), Enabled: true,
		Input: map[string]interface{}{"old": "value"},
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u1",
		"input":       map[string]interface{}{"new": "value"},
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	success, _, _ := decodeResp(t, rr.Body)
	assert.True(t, success)

	updated, _ := store.Get(context.Background(), "sch-u1")
	assert.Equal(t, "value", updated.Input["new"])
	assert.NotContains(t, updated.Input, "old", "input should be fully replaced")
}

func TestHandleUpdateSchedule_CronExpr_RecomputesRunAt(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u2", TargetAgent: "agent-a", CronExpr: "*/5 * * * *",
		RunAt: time.Now().Add(-1 * time.Hour), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u2",
		"cron_expr":   "*/10 * * * *",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	updated, _ := store.Get(context.Background(), "sch-u2")
	assert.Equal(t, "*/10 * * * *", updated.CronExpr)
	assert.True(t, updated.RunAt.After(time.Now()), "RunAt should be recomputed into the future")
}

func TestHandleUpdateSchedule_RunAt_ClearsCron(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u3", TargetAgent: "agent-a", CronExpr: "*/5 * * * *",
		RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u3",
		"run_at":      future,
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	updated, _ := store.Get(context.Background(), "sch-u3")
	assert.Empty(t, updated.CronExpr, "switching to one-shot clears CronExpr")
}

func TestHandleUpdateSchedule_Delay_ClearsCron(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u4", TargetAgent: "agent-a", CronExpr: "*/5 * * * *",
		RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u4",
		"delay":       "30m",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	updated, _ := store.Get(context.Background(), "sch-u4")
	assert.Empty(t, updated.CronExpr)
	assert.True(t, updated.RunAt.After(time.Now().Add(25*time.Minute)))
}

func TestHandleUpdateSchedule_Enabled_Toggle(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u5", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	disabled := false
	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u5",
		"enabled":     disabled,
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	updated, _ := store.Get(context.Background(), "sch-u5")
	assert.False(t, updated.Enabled)
}

func TestHandleUpdateSchedule_TargetAgent_Update(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u6", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id":  "sch-u6",
		"target_agent": "agent-b",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	updated, _ := store.Get(context.Background(), "sch-u6")
	assert.Equal(t, "agent-b", updated.TargetAgent)
}

func TestHandleUpdateSchedule_TargetAgent_Empty_Rejected(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u7", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	empty := ""
	req := postJSON(map[string]interface{}{
		"schedule_id":  "sch-u7",
		"target_agent": empty,
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_TARGET_QUEUE", toolErr.Code)
}

func TestHandleUpdateSchedule_MissedRunPolicy_Update(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u8", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
		MissedRunPolicy: core.MissedRunSkip,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id":       "sch-u8",
		"missed_run_policy": "catchup",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	updated, _ := store.Get(context.Background(), "sch-u8")
	assert.Equal(t, core.MissedRunCatchUp, updated.MissedRunPolicy)
}

func TestHandleUpdateSchedule_InvalidMissedRunPolicy(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u9", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id":       "sch-u9",
		"missed_run_policy": "bogus",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_MISSED_RUN_POLICY", toolErr.Code)
}

func TestHandleUpdateSchedule_MultipleTimings_Rejected(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u10", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u10",
		"cron_expr":   "*/5 * * * *",
		"delay":       "10m",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_TIMING", toolErr.Code)
}

func TestHandleUpdateSchedule_InvalidCron(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u11", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u11",
		"cron_expr":   "not-a-cron",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_CRON", toolErr.Code)
}

func TestHandleUpdateSchedule_InvalidRunAt(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u12", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u12",
		"run_at":      "not-a-time",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_RUN_AT", toolErr.Code)
}

func TestHandleUpdateSchedule_InvalidDelay_Format(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u13", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u13",
		"delay":       "not-a-duration",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_DELAY", toolErr.Code)
}

func TestHandleUpdateSchedule_InvalidDelay_Negative(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u14", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u14",
		"delay":       "-5s",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_DELAY", toolErr.Code)
}

func TestHandleUpdateSchedule_NotFound(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"schedule_id": "does-not-exist",
		"delay":       "10m",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "SCHEDULE_NOT_FOUND", toolErr.Code)
}

func TestHandleUpdateSchedule_GetStoreError(t *testing.T) {
	store := newMockScheduleStore()
	store.getErr = errors.New("store down")
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-anything",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "STORE_ERROR", toolErr.Code)
}

func TestHandleUpdateSchedule_UpdateStoreError(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u15", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	store.updateErr = errors.New("update down")
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u15",
		"delay":       "10m",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "STORE_ERROR", toolErr.Code)
}

func TestHandleUpdateSchedule_UpdateRaceNotFound(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-u16", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	// Get succeeds, Update fails with ErrScheduleNotFound — simulating a
	// deletion between the Get and Update calls in the handler.
	store.updateErr = core.ErrScheduleNotFound
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{
		"schedule_id": "sch-u16",
		"delay":       "10m",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "SCHEDULE_NOT_FOUND", toolErr.Code)
}

func TestHandleUpdateSchedule_MissingScheduleID(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{
		"delay": "10m",
	})
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "MISSING_SCHEDULE_ID", toolErr.Code)
}

func TestHandleUpdateSchedule_InvalidJSON(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postRaw("{broken")
	rr := httptest.NewRecorder()
	h.handleUpdateSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_JSON", toolErr.Code)
}

// ═══════════════════════════════════════════════════════════════════════════
// handleCancelSchedule
// ═══════════════════════════════════════════════════════════════════════════

func TestHandleCancelSchedule_Success(t *testing.T) {
	store := newMockScheduleStore()
	_ = store.Create(context.Background(), &core.Schedule{
		ID: "sch-cancel", TargetAgent: "agent-a", RunAt: time.Now(), Enabled: true,
	})
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{"schedule_id": "sch-cancel"})
	rr := httptest.NewRecorder()
	h.handleCancelSchedule(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	success, data, _ := decodeResp(t, rr.Body)
	assert.True(t, success)
	assert.Equal(t, "deleted", data["status"])

	_, err := store.Get(context.Background(), "sch-cancel")
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

func TestHandleCancelSchedule_NotFound(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{"schedule_id": "does-not-exist"})
	rr := httptest.NewRecorder()
	h.handleCancelSchedule(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "SCHEDULE_NOT_FOUND", toolErr.Code)
}

func TestHandleCancelSchedule_MissingID(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postJSON(map[string]interface{}{})
	rr := httptest.NewRecorder()
	h.handleCancelSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "MISSING_SCHEDULE_ID", toolErr.Code)
}

func TestHandleCancelSchedule_InvalidJSON(t *testing.T) {
	h := newTestHandler(newMockScheduleStore())
	req := postRaw("{broken")
	rr := httptest.NewRecorder()
	h.handleCancelSchedule(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "INVALID_JSON", toolErr.Code)
}

func TestHandleCancelSchedule_StoreError(t *testing.T) {
	store := newMockScheduleStore()
	store.deleteErr = errors.New("store down")
	h := newTestHandler(store)

	req := postJSON(map[string]interface{}{"schedule_id": "anything"})
	rr := httptest.NewRecorder()
	h.handleCancelSchedule(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	_, _, toolErr := decodeResp(t, rr.Body)
	require.NotNil(t, toolErr)
	assert.Equal(t, "STORE_ERROR", toolErr.Code)
}

// ═══════════════════════════════════════════════════════════════════════════
// Response writer helpers
// ═══════════════════════════════════════════════════════════════════════════

func TestWriteScheduleSuccess(t *testing.T) {
	rr := httptest.NewRecorder()
	writeScheduleSuccess(rr, http.StatusCreated, map[string]interface{}{"k": "v"})

	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	success, data, toolErr := decodeResp(t, rr.Body)
	assert.True(t, success)
	assert.Nil(t, toolErr)
	assert.Equal(t, "v", data["k"])
}

func TestWriteScheduleError(t *testing.T) {
	rr := httptest.NewRecorder()
	writeScheduleError(rr, http.StatusBadRequest, &core.ToolError{
		Code:    "TEST_CODE",
		Message: "test message",
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	success, data, toolErr := decodeResp(t, rr.Body)
	assert.False(t, success)
	assert.Nil(t, data)
	require.NotNil(t, toolErr)
	assert.Equal(t, "TEST_CODE", toolErr.Code)
}

// ═══════════════════════════════════════════════════════════════════════════
// setTraceHeaders
// ═══════════════════════════════════════════════════════════════════════════

func TestSetTraceHeaders_NoTraceContext_NoHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	setTraceHeaders(context.Background(), rr)

	// No trace context in the plain context, so no headers should be set.
	assert.Empty(t, rr.Header().Get("X-Trace-ID"))
	assert.Empty(t, rr.Header().Get("X-Span-ID"))
}

func TestSetTraceHeaders_WithTraceContext_SetsHeaders(t *testing.T) {
	// Build a real span context using the OpenTelemetry SDK's test tracer
	// provider so telemetry.GetTraceContext() returns a valid trace ID.
	tp := sdktrace.NewTracerProvider()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	// Make sure the global tracer provider points at ours so any nested
	// helpers see the same tracing fabric.
	otel.SetTracerProvider(tp)

	rr := httptest.NewRecorder()
	setTraceHeaders(ctx, rr)

	assert.NotEmpty(t, rr.Header().Get("X-Trace-ID"), "trace ID header should be set when a span is active")
	assert.NotEmpty(t, rr.Header().Get("X-Span-ID"), "span ID header should be set when a span is active")
}
