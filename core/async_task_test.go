package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTaskStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status   TaskStatus
		expected bool
	}{
		{TaskStatusQueued, false},
		{TaskStatusRunning, false},
		{TaskStatusCompleted, true},
		{TaskStatusFailed, true},
		{TaskStatusCancelled, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsTerminal(); got != tt.expected {
				t.Errorf("TaskStatus(%s).IsTerminal() = %v, want %v", tt.status, got, tt.expected)
			}
		})
	}
}

func TestNewTask(t *testing.T) {
	id := "task-123"
	taskType := "orchestration"
	input := map[string]interface{}{
		"query": "test query",
	}

	task := NewTask(id, taskType, input)

	if task.ID != id {
		t.Errorf("NewTask().ID = %v, want %v", task.ID, id)
	}
	if task.Type != taskType {
		t.Errorf("NewTask().Type = %v, want %v", task.Type, taskType)
	}
	if task.Status != TaskStatusQueued {
		t.Errorf("NewTask().Status = %v, want %v", task.Status, TaskStatusQueued)
	}
	if task.Input["query"] != "test query" {
		t.Errorf("NewTask().Input[query] = %v, want %v", task.Input["query"], "test query")
	}
	if task.CreatedAt.IsZero() {
		t.Error("NewTask().CreatedAt should not be zero")
	}
}

func TestNewTaskWithTimeout(t *testing.T) {
	id := "task-456"
	taskType := "research"
	input := map[string]interface{}{"topic": "AI"}
	timeout := 10 * time.Minute

	task := NewTaskWithTimeout(id, taskType, input, timeout)

	if task.ID != id {
		t.Errorf("NewTaskWithTimeout().ID = %v, want %v", task.ID, id)
	}
	if task.Options.Timeout != timeout {
		t.Errorf("NewTaskWithTimeout().Options.Timeout = %v, want %v", task.Options.Timeout, timeout)
	}
}

func TestTask_SetTraceContext(t *testing.T) {
	task := NewTask("task-789", "test", nil)
	traceID := "0af7651916cd43dd8448eb211c80319c"
	spanID := "b7ad6b7169203331"

	task.SetTraceContext(traceID, spanID)

	if task.TraceID != traceID {
		t.Errorf("Task.TraceID = %v, want %v", task.TraceID, traceID)
	}
	if task.ParentSpanID != spanID {
		t.Errorf("Task.ParentSpanID = %v, want %v", task.ParentSpanID, spanID)
	}
}

func TestTaskError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *TaskError
		expected string
	}{
		{
			name: "with details",
			err: &TaskError{
				Code:    TaskErrorCodeTimeout,
				Message: "task exceeded timeout",
				Details: "timeout was 30m",
			},
			expected: "TASK_TIMEOUT: task exceeded timeout (timeout was 30m)",
		},
		{
			name: "without details",
			err: &TaskError{
				Code:    TaskErrorCodeHandlerError,
				Message: "handler failed",
			},
			expected: "HANDLER_ERROR: handler failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.expected {
				t.Errorf("TaskError.Error() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDefaultAsyncTaskConfig(t *testing.T) {
	config := DefaultAsyncTaskConfig()

	if config.QueuePrefix != "truvag3:tasks" {
		t.Errorf("QueuePrefix = %v, want truvag3:tasks", config.QueuePrefix)
	}
	if config.WorkerCount != 5 {
		t.Errorf("WorkerCount = %v, want 5", config.WorkerCount)
	}
	if config.DequeueTimeout != 30*time.Second {
		t.Errorf("DequeueTimeout = %v, want 30s", config.DequeueTimeout)
	}
	if config.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 30s", config.ShutdownTimeout)
	}
	if config.DefaultTimeout != 30*time.Minute {
		t.Errorf("DefaultTimeout = %v, want 30m", config.DefaultTimeout)
	}
	if config.ResultTTL != 24*time.Hour {
		t.Errorf("ResultTTL = %v, want 24h", config.ResultTTL)
	}
}

func TestTaskProgress(t *testing.T) {
	progress := &TaskProgress{
		CurrentStep: 2,
		TotalSteps:  5,
		StepName:    "Processing",
		Percentage:  40.0,
		Message:     "Analyzing data",
	}

	if progress.CurrentStep != 2 {
		t.Errorf("CurrentStep = %v, want 2", progress.CurrentStep)
	}
	if progress.TotalSteps != 5 {
		t.Errorf("TotalSteps = %v, want 5", progress.TotalSteps)
	}
	if progress.Percentage != 40.0 {
		t.Errorf("Percentage = %v, want 40.0", progress.Percentage)
	}
}

func TestTaskOptions(t *testing.T) {
	options := TaskOptions{
		Timeout: 5 * time.Minute,
	}

	if options.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %v, want 5m", options.Timeout)
	}
}

func TestTask_FullLifecycle(t *testing.T) {
	// Create task
	task := NewTask("lifecycle-test", "integration", map[string]interface{}{
		"action": "test",
	})

	// Verify initial state
	if task.Status != TaskStatusQueued {
		t.Errorf("Initial status = %v, want queued", task.Status)
	}
	if task.StartedAt != nil {
		t.Error("StartedAt should be nil initially")
	}
	if task.CompletedAt != nil {
		t.Error("CompletedAt should be nil initially")
	}

	// Simulate starting
	now := time.Now()
	task.Status = TaskStatusRunning
	task.StartedAt = &now

	if task.Status != TaskStatusRunning {
		t.Errorf("Status = %v, want running", task.Status)
	}
	if task.StartedAt == nil {
		t.Error("StartedAt should not be nil after starting")
	}

	// Simulate progress
	task.Progress = &TaskProgress{
		CurrentStep: 1,
		TotalSteps:  2,
		StepName:    "Step 1",
		Percentage:  50.0,
	}

	if task.Progress.CurrentStep != 1 {
		t.Errorf("Progress.CurrentStep = %v, want 1", task.Progress.CurrentStep)
	}

	// Simulate completion
	completedAt := time.Now()
	task.Status = TaskStatusCompleted
	task.CompletedAt = &completedAt
	task.Result = map[string]interface{}{"success": true}

	if task.Status != TaskStatusCompleted {
		t.Errorf("Status = %v, want completed", task.Status)
	}
	if task.CompletedAt == nil {
		t.Error("CompletedAt should not be nil after completion")
	}
	if task.Result == nil {
		t.Error("Result should not be nil after completion")
	}
}

func TestTask_FailureScenario(t *testing.T) {
	task := NewTask("failure-test", "test", nil)

	// Simulate failure
	now := time.Now()
	task.Status = TaskStatusFailed
	task.CompletedAt = &now
	task.Error = &TaskError{
		Code:    TaskErrorCodeHandlerError,
		Message: "Something went wrong",
		Details: "Stack trace here",
	}

	if !task.Status.IsTerminal() {
		t.Error("Failed status should be terminal")
	}
	if task.Error == nil {
		t.Error("Error should not be nil for failed task")
	}
	if task.Error.Code != TaskErrorCodeHandlerError {
		t.Errorf("Error.Code = %v, want %v", task.Error.Code, TaskErrorCodeHandlerError)
	}
}

func TestTask_CancellationScenario(t *testing.T) {
	task := NewTask("cancel-test", "test", nil)

	// Start the task
	startedAt := time.Now()
	task.Status = TaskStatusRunning
	task.StartedAt = &startedAt

	// Cancel the task
	cancelledAt := time.Now()
	task.Status = TaskStatusCancelled
	task.CancelledAt = &cancelledAt
	task.Error = &TaskError{
		Code:    TaskErrorCodeCancelled,
		Message: "Task was cancelled by user",
	}

	if !task.Status.IsTerminal() {
		t.Error("Cancelled status should be terminal")
	}
	if task.CancelledAt == nil {
		t.Error("CancelledAt should not be nil for cancelled task")
	}
}

func TestErrorConstants(t *testing.T) {
	// Verify error constants are defined
	if TaskErrorCodeTimeout != "TASK_TIMEOUT" {
		t.Errorf("TaskErrorCodeTimeout = %v, want TASK_TIMEOUT", TaskErrorCodeTimeout)
	}
	if TaskErrorCodeCancelled != "TASK_CANCELLED" {
		t.Errorf("TaskErrorCodeCancelled = %v, want TASK_CANCELLED", TaskErrorCodeCancelled)
	}
	if TaskErrorCodeHandlerError != "HANDLER_ERROR" {
		t.Errorf("TaskErrorCodeHandlerError = %v, want HANDLER_ERROR", TaskErrorCodeHandlerError)
	}
	if TaskErrorCodePanic != "HANDLER_PANIC" {
		t.Errorf("TaskErrorCodePanic = %v, want HANDLER_PANIC", TaskErrorCodePanic)
	}
	if TaskErrorCodeInvalidInput != "INVALID_INPUT" {
		t.Errorf("TaskErrorCodeInvalidInput = %v, want INVALID_INPUT", TaskErrorCodeInvalidInput)
	}
}

func TestSentinelErrors(t *testing.T) {
	// Verify sentinel errors are defined
	if ErrTaskNotFound == nil {
		t.Error("ErrTaskNotFound should not be nil")
	}
	if ErrTaskNotCancellable == nil {
		t.Error("ErrTaskNotCancellable should not be nil")
	}
	if ErrTaskQueueEmpty == nil {
		t.Error("ErrTaskQueueEmpty should not be nil")
	}
	if ErrInvalidTaskStatus == nil {
		t.Error("ErrInvalidTaskStatus should not be nil")
	}

	// Verify error messages
	if ErrTaskNotFound.Error() != "task not found" {
		t.Errorf("ErrTaskNotFound.Error() = %v", ErrTaskNotFound.Error())
	}
	if ErrTaskNotCancellable.Error() != "task not cancellable" {
		t.Errorf("ErrTaskNotCancellable.Error() = %v", ErrTaskNotCancellable.Error())
	}

	// Phase 1 additions — sentinels used by the Scheduler for idempotent
	// task creation and by capability handlers for schedule store lookup.
	if ErrTaskAlreadyExists == nil {
		t.Error("ErrTaskAlreadyExists should not be nil")
	}
	if ErrTaskAlreadyExists.Error() != "task already exists" {
		t.Errorf("ErrTaskAlreadyExists.Error() = %v", ErrTaskAlreadyExists.Error())
	}
	if ErrScheduleNotFound == nil {
		t.Error("ErrScheduleNotFound should not be nil")
	}
	if ErrScheduleNotFound.Error() != "schedule not found" {
		t.Errorf("ErrScheduleNotFound.Error() = %v", ErrScheduleNotFound.Error())
	}
	if ErrScheduleAlreadyExists == nil {
		t.Error("ErrScheduleAlreadyExists should not be nil")
	}
	if ErrScheduleAlreadyExists.Error() != "schedule already exists" {
		t.Errorf("ErrScheduleAlreadyExists.Error() = %v", ErrScheduleAlreadyExists.Error())
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Phase 1 — Scheduling types and interfaces
// ═══════════════════════════════════════════════════════════════════════════

func TestErrTaskAlreadyExists_WrappedByFmtErrorfIsDetectable(t *testing.T) {
	// This is the exact wrapping pattern used by the Phase 1a-bis surgical
	// fix in orchestration/redis_task_store.go. Scheduler idempotency relies
	// on errors.Is() correctly identifying the sentinel through the wrap.
	id := "task-abc-123"
	wrapped := fmt.Errorf("%w: %s", ErrTaskAlreadyExists, id)
	if !errors.Is(wrapped, ErrTaskAlreadyExists) {
		t.Error("errors.Is should identify ErrTaskAlreadyExists through %%w wrap")
	}
	// Message format is preserved for backwards compatibility with any
	// existing log scrapers.
	expected := "task already exists: " + id
	if wrapped.Error() != expected {
		t.Errorf("wrapped.Error() = %q, want %q", wrapped.Error(), expected)
	}
}

func TestScheduledTaskType_ConstantValue(t *testing.T) {
	// The Scheduler stamps this exact value on every materialized task.
	// Agents register their scheduled-task handler under this name.
	// Changing this constant is a breaking change — lock it in via test.
	if ScheduledTaskType != "truvag3.scheduled" {
		t.Errorf("ScheduledTaskType = %q, want %q", ScheduledTaskType, "truvag3.scheduled")
	}
}

func TestMissedRunPolicy_Constants(t *testing.T) {
	if MissedRunSkip != "skip" {
		t.Errorf("MissedRunSkip = %q, want %q", MissedRunSkip, "skip")
	}
	if MissedRunCatchUp != "catchup" {
		t.Errorf("MissedRunCatchUp = %q, want %q", MissedRunCatchUp, "catchup")
	}
}

func TestSchedule_JSONSerialization_RoundTrip(t *testing.T) {
	lastRun := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	runAt := time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	original := Schedule{
		ID:              "sch-abc123",
		Input:           map[string]interface{}{"instruction": "run this"},
		TargetAgent:     "agent-a",
		CronExpr:        "*/5 * * * *",
		RunAt:           runAt,
		LastRunAt:       &lastRun,
		Enabled:         true,
		MissedRunPolicy: MissedRunCatchUp,
		CreatedBy:       "devops-chat-agent",
		CreatedAt:       createdAt,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Schedule
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, original.ID)
	}
	if decoded.TargetAgent != original.TargetAgent {
		t.Errorf("TargetAgent mismatch: got %q, want %q", decoded.TargetAgent, original.TargetAgent)
	}
	if decoded.CronExpr != original.CronExpr {
		t.Errorf("CronExpr mismatch: got %q, want %q", decoded.CronExpr, original.CronExpr)
	}
	if !decoded.RunAt.Equal(original.RunAt) {
		t.Errorf("RunAt mismatch: got %v, want %v", decoded.RunAt, original.RunAt)
	}
	if decoded.LastRunAt == nil || !decoded.LastRunAt.Equal(*original.LastRunAt) {
		t.Errorf("LastRunAt mismatch")
	}
	if decoded.Enabled != original.Enabled {
		t.Errorf("Enabled mismatch: got %v, want %v", decoded.Enabled, original.Enabled)
	}
	if decoded.MissedRunPolicy != original.MissedRunPolicy {
		t.Errorf("MissedRunPolicy mismatch: got %q, want %q", decoded.MissedRunPolicy, original.MissedRunPolicy)
	}
	if decoded.CreatedBy != original.CreatedBy {
		t.Errorf("CreatedBy mismatch: got %q, want %q", decoded.CreatedBy, original.CreatedBy)
	}
	if decoded.Input["instruction"] != "run this" {
		t.Errorf("Input mismatch")
	}
}

func TestSchedule_OneShot_EmptyCronOmitted(t *testing.T) {
	// A one-shot schedule has no CronExpr. The json tag uses omitempty so
	// the field is absent from serialized output.
	s := Schedule{
		ID:          "sch-oneshot",
		TargetAgent: "agent-a",
		RunAt:       time.Now(),
		Enabled:     true,
		CronExpr:    "",
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "cron_expr") {
		t.Errorf("empty CronExpr should be omitted from JSON, got: %s", string(data))
	}
}

func TestTask_ScheduleID_OmittedByDefault(t *testing.T) {
	// Existing ad-hoc tasks (no ScheduleID) should not have the field in
	// their JSON — backwards compatibility with pre-Phase-1 consumers.
	task := NewTask("task-1", "some-type", map[string]interface{}{"k": "v"})
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "schedule_id") {
		t.Errorf("empty ScheduleID should be omitted from JSON, got: %s", string(data))
	}
	if strings.Contains(string(data), "target_agent") {
		t.Errorf("empty TargetAgent should be omitted from JSON, got: %s", string(data))
	}
}

func TestTask_ScheduleID_SerializedWhenSet(t *testing.T) {
	task := NewTask("task-1", "some-type", nil)
	task.ScheduleID = "sch-parent"
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"schedule_id":"sch-parent"`) {
		t.Errorf("ScheduleID should appear in JSON when set, got: %s", string(data))
	}
}

func TestTask_TargetAgent_JSONRoundTrip(t *testing.T) {
	task := NewTask("task-1", ScheduledTaskType, nil)
	task.ScheduleID = "sch-abc"
	task.TargetAgent = "devops-chat-agent"

	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"target_agent":"devops-chat-agent"`) {
		t.Errorf("TargetAgent should appear in JSON when set, got: %s", string(data))
	}

	var decoded Task
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.TargetAgent != task.TargetAgent {
		t.Errorf("TargetAgent roundtrip: got %q, want %q", decoded.TargetAgent, task.TargetAgent)
	}
}
