package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

func TestAPISubmitsAndReadsExecution(t *testing.T) {
	store := newFakeWorkflowStore()
	dispatcher := &fakeDispatcher{}
	api := &API{
		workflow:   store,
		dispatcher: dispatcher,
		queue:      DefaultQueue,
		workflowID: DefaultWorkflowID,
		descriptor: BackendDescriptor{
			Validated: true,
			RequiredCapabilities: []string{
				string(orchestration.BackendWorkflowState),
				string(orchestration.BackendTaskDispatcher),
			},
			SelectedBackends: map[string]BackendSelection{
				string(orchestration.BackendWorkflowState): {Provider: "postgresql"},
			},
		},
	}

	submit := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewBufferString(`{"location":"Chicago, IL"}`))
	submitRecorder := httptest.NewRecorder()
	api.Handler().ServeHTTP(submitRecorder, submit)
	if submitRecorder.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", submitRecorder.Code, submitRecorder.Body.String())
	}
	var submitted struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(submitRecorder.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.ExecutionID == "" {
		t.Fatal("submission omitted execution_id")
	}
	if dispatcher.queue != DefaultQueue || dispatcher.task == nil || dispatcher.task.ID != submitted.ExecutionID {
		t.Fatalf("dispatch = (%q, %#v), want queue and matching task", dispatcher.queue, dispatcher.task)
	}

	get := httptest.NewRequest(http.MethodGet, "/tasks/"+submitted.ExecutionID, nil)
	getRecorder := httptest.NewRecorder()
	api.Handler().ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRecorder.Code, getRecorder.Body.String())
	}
	var execution orchestration.WorkflowExecution
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &execution); err != nil {
		t.Fatal(err)
	}
	if execution.Status != orchestration.ExecutionPending || execution.Inputs["location"] != "Chicago, IL" {
		t.Fatalf("execution = %#v", execution)
	}
}

func TestAPIRejectsInvalidLocation(t *testing.T) {
	api := &API{workflow: newFakeWorkflowStore(), dispatcher: &fakeDispatcher{}}
	for _, body := range []string{
		`{"location":""}`,
		`{"location":"Chicago"} {"location":"Tokyo"}`,
		`{"location":"Chicago","unexpected":true}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewBufferString(body))
		recorder := httptest.NewRecorder()
		api.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, recorder.Code)
		}
	}
}

func TestWorkerPersistsDeterministicResultBeforeAcknowledging(t *testing.T) {
	store := newFakeWorkflowStore()
	execution := &orchestration.WorkflowExecution{
		ID:         "task-1",
		WorkflowID: DefaultWorkflowID,
		Status:     orchestration.ExecutionPending,
		StartTime:  time.Now(),
		Inputs:     map[string]interface{}{"location": "Chicago"},
		Steps:      make(map[string]*orchestration.StepExecution),
		Context:    make(map[string]interface{}),
	}
	if err := store.SaveExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	worker := &Worker{
		workflow: store,
		logger:   &core.NoOpLogger{},
	}
	handle := &fakeTaskHandle{task: core.NewTask("task-1", "portable-weather", map[string]interface{}{"location": "Chicago"})}
	if err := worker.process(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if !handle.acked || handle.nacked {
		t.Fatalf("settlement = acked %t, nacked %t", handle.acked, handle.nacked)
	}
	completed, err := store.GetExecution(t.Context(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != orchestration.ExecutionCompleted {
		t.Fatalf("status = %q, want completed", completed.Status)
	}
	if completed.Outputs["summary"] != "portable task task-1 completed for Chicago" {
		t.Fatalf("deterministic summary = %#v", completed.Outputs["summary"])
	}
	if _, exists := completed.Outputs["backend_proof"]; exists {
		t.Fatalf("business result contains a self-reported backend proof: %#v", completed.Outputs)
	}
}

func TestScheduledExecutorPersistsCompletionBeforeAcknowledging(t *testing.T) {
	task := core.NewTask("schedule-1:1900000000", core.ScheduledTaskType, map[string]interface{}{
		"instruction": "Give a concise weather summary",
	})
	task.ScheduleID = "schedule-1"
	task.TargetAgent = portableTargetService
	store := newFakeTaskStore(task)
	resolver := &fakeServiceResolver{services: []*core.ServiceInfo{{
		Name:    task.TargetAgent,
		Type:    core.ComponentTypeAgent,
		Address: "http://portable-target.invalid",
		Port:    80,
		Health:  core.HealthHealthy,
	}}}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/scheduled" {
			t.Fatalf("path = %q, want scheduled endpoint", request.URL.Path)
		}
		var payload orchestration.ScheduledRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.TaskID != task.ID || payload.ScheduleID != task.ScheduleID || payload.Instruction == "" {
			t.Fatalf("scheduled payload = %#v", payload)
		}
		return jsonResponse(http.StatusOK, `{"success":true,"data":{"status":"completed"}}`), nil
	})}
	executor := &ScheduledExecutor{
		consumer: &fakeConsumer{},
		tasks:    store,
		resolver: resolver,
		http:     httpClient,
		logger:   &core.NoOpLogger{},
		queue:    ScheduledExecutorQueue,
	}
	handle := &fakeTaskHandle{task: task}
	if err := executor.process(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if !handle.acked || handle.nacked {
		t.Fatalf("settlement = acked %t, nacked %t", handle.acked, handle.nacked)
	}
	stored, err := store.Get(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != core.TaskStatusCompleted || stored.CompletedAt == nil || stored.Result == nil {
		t.Fatalf("stored task = %#v", stored)
	}
}

func TestScheduledExecutorPersistsTerminalFailureBeforeNack(t *testing.T) {
	task := core.NewTask("schedule-2:1900000000", core.ScheduledTaskType, map[string]interface{}{
		"instruction": "Run this schedule",
	})
	task.ScheduleID = "schedule-2"
	task.TargetAgent = portableTargetService
	store := newFakeTaskStore(task)
	executor := &ScheduledExecutor{
		consumer: &fakeConsumer{},
		tasks:    store,
		resolver: &fakeServiceResolver{services: []*core.ServiceInfo{{
			Name: task.TargetAgent, Type: core.ComponentTypeAgent,
			Address: "http://portable-target.invalid", Port: 80, Health: core.HealthHealthy,
		}}},
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusBadRequest, `{"success":false}`), nil
		})},
		logger: &core.NoOpLogger{},
		queue:  ScheduledExecutorQueue,
	}
	handle := &fakeTaskHandle{task: task}
	if err := executor.process(t.Context(), handle); err == nil {
		t.Fatal("process error = nil, want terminal target failure")
	}
	if handle.acked || !handle.nacked {
		t.Fatalf("settlement = acked %t, nacked %t", handle.acked, handle.nacked)
	}
	stored, err := store.Get(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != core.TaskStatusFailed || stored.Error == nil {
		t.Fatalf("stored task = %#v", stored)
	}
}

func TestWorkerStopsCleanlyOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	worker := &Worker{
		workflow: newFakeWorkflowStore(),
		consumer: &cancellingConsumer{},
		queue:    DefaultQueue,
		logger:   &core.NoOpLogger{},
	}
	if err := worker.Start(ctx); err != nil {
		t.Fatalf("worker shutdown returned an error: %v", err)
	}
}

func TestScheduledExecutorStopsCleanlyOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	executor := &ScheduledExecutor{
		consumer: &cancellingConsumer{},
		tasks:    newFakeTaskStore(),
		resolver: &fakeServiceResolver{},
		http:     http.DefaultClient,
		logger:   &core.NoOpLogger{},
		queue:    ScheduledExecutorQueue,
	}
	if err := executor.Start(ctx); err != nil {
		t.Fatalf("executor shutdown returned an error: %v", err)
	}
}

type fakeDispatcher struct {
	queue string
	task  *core.Task
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func (dispatcher *fakeDispatcher) Dispatch(_ context.Context, queue string, task *core.Task) error {
	dispatcher.queue = queue
	dispatcher.task = task
	return nil
}

type fakeServiceResolver struct {
	services []*core.ServiceInfo
	err      error
}

func (resolver *fakeServiceResolver) FindService(context.Context, string) ([]*core.ServiceInfo, error) {
	return resolver.services, resolver.err
}

type fakeConsumer struct{}

func (*fakeConsumer) Consume(context.Context, string) (core.TaskHandle, error) { return nil, nil }

type cancellingConsumer struct{}

func (*cancellingConsumer) Consume(ctx context.Context, _ string) (core.TaskHandle, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type fakeTaskHandle struct {
	task   *core.Task
	acked  bool
	nacked bool
}

func (handle *fakeTaskHandle) Task() *core.Task { return handle.task }
func (handle *fakeTaskHandle) Ack(context.Context) error {
	handle.acked = true
	return nil
}
func (handle *fakeTaskHandle) Nack(context.Context, string) error {
	handle.nacked = true
	return nil
}

type fakeWorkflowStore struct {
	mu         sync.Mutex
	executions map[string]*orchestration.WorkflowExecution
}

type fakeTaskStore struct {
	mu    sync.Mutex
	tasks map[string]*core.Task
}

func newFakeTaskStore(tasks ...*core.Task) *fakeTaskStore {
	store := &fakeTaskStore{tasks: make(map[string]*core.Task)}
	for _, task := range tasks {
		store.tasks[task.ID] = task
	}
	return store
}

func (store *fakeTaskStore) Create(_ context.Context, task *core.Task) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.tasks[task.ID]; exists {
		return core.ErrTaskAlreadyExists
	}
	store.tasks[task.ID] = task
	return nil
}

func (store *fakeTaskStore) Get(_ context.Context, taskID string) (*core.Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, exists := store.tasks[taskID]
	if !exists {
		return nil, core.ErrTaskNotFound
	}
	return task, nil
}

func (store *fakeTaskStore) Update(_ context.Context, task *core.Task) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.tasks[task.ID]; !exists {
		return core.ErrTaskNotFound
	}
	store.tasks[task.ID] = task
	return nil
}

func (store *fakeTaskStore) Delete(_ context.Context, taskID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.tasks[taskID]; !exists {
		return core.ErrTaskNotFound
	}
	delete(store.tasks, taskID)
	return nil
}

func (store *fakeTaskStore) Cancel(_ context.Context, taskID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, exists := store.tasks[taskID]
	if !exists {
		return core.ErrTaskNotFound
	}
	if task.Status.IsTerminal() {
		return core.ErrTaskNotCancellable
	}
	task.Status = core.TaskStatusCancelled
	return nil
}

func newFakeWorkflowStore() *fakeWorkflowStore {
	return &fakeWorkflowStore{executions: make(map[string]*orchestration.WorkflowExecution)}
}

func (store *fakeWorkflowStore) SaveExecution(_ context.Context, execution *orchestration.WorkflowExecution) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.executions[execution.ID] = execution
	return nil
}

func (store *fakeWorkflowStore) UpdateExecution(_ context.Context, execution *orchestration.WorkflowExecution) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.executions[execution.ID] = execution
	return nil
}

func (store *fakeWorkflowStore) UpdateStepExecution(
	_ context.Context,
	executionID string,
	step *orchestration.StepExecution,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.executions[executionID].Steps[step.StepID] = step
	return nil
}

func (store *fakeWorkflowStore) GetExecution(
	_ context.Context,
	executionID string,
) (*orchestration.WorkflowExecution, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.executions[executionID], nil
}

func (store *fakeWorkflowStore) ListExecutions(
	context.Context,
	string,
) ([]*orchestration.WorkflowExecution, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]*orchestration.WorkflowExecution, 0, len(store.executions))
	for _, execution := range store.executions {
		result = append(result, execution)
	}
	return result, nil
}
