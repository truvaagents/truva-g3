package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// mockTaskQueue implements core.TaskQueue for testing
type mockTaskQueue struct {
	tasks       []*core.Task
	enqueueErr  error
	dequeueErr  error
	lastEnqueue *core.Task
}

func (m *mockTaskQueue) Enqueue(ctx context.Context, task *core.Task) error {
	if m.enqueueErr != nil {
		return m.enqueueErr
	}
	m.lastEnqueue = task
	m.tasks = append(m.tasks, task)
	return nil
}

func (m *mockTaskQueue) Dequeue(ctx context.Context, timeout time.Duration) (*core.Task, error) {
	if m.dequeueErr != nil {
		return nil, m.dequeueErr
	}
	if len(m.tasks) == 0 {
		return nil, nil
	}
	task := m.tasks[0]
	m.tasks = m.tasks[1:]
	return task, nil
}

func (m *mockTaskQueue) Acknowledge(ctx context.Context, taskID string) error {
	return nil
}

func (m *mockTaskQueue) Reject(ctx context.Context, taskID string, reason string) error {
	return nil
}

// mockTaskStore implements core.TaskStore for testing
type mockTaskStore struct {
	tasks     map[string]*core.Task
	createErr error
	getErr    error
	updateErr error
	cancelErr error
}

func newMockTaskStore() *mockTaskStore {
	return &mockTaskStore{
		tasks: make(map[string]*core.Task),
	}
}

func (m *mockTaskStore) Create(ctx context.Context, task *core.Task) error {
	if m.createErr != nil {
		return m.createErr
	}
	if _, exists := m.tasks[task.ID]; exists {
		return core.ErrTaskNotFound // Should be "already exists" but using this for test
	}
	m.tasks[task.ID] = task
	return nil
}

func (m *mockTaskStore) Get(ctx context.Context, taskID string) (*core.Task, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	task, exists := m.tasks[taskID]
	if !exists {
		return nil, core.ErrTaskNotFound
	}
	return task, nil
}

func (m *mockTaskStore) Update(ctx context.Context, task *core.Task) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	if _, exists := m.tasks[task.ID]; !exists {
		return core.ErrTaskNotFound
	}
	m.tasks[task.ID] = task
	return nil
}

func (m *mockTaskStore) Delete(ctx context.Context, taskID string) error {
	delete(m.tasks, taskID)
	return nil
}

func (m *mockTaskStore) Cancel(ctx context.Context, taskID string) error {
	if m.cancelErr != nil {
		return m.cancelErr
	}
	task, exists := m.tasks[taskID]
	if !exists {
		return core.ErrTaskNotFound
	}
	if task.Status.IsTerminal() {
		return core.ErrTaskNotCancellable
	}
	now := time.Now()
	task.Status = core.TaskStatusCancelled
	task.CancelledAt = &now
	return nil
}

func TestTaskAPIHandler_HandleSubmit(t *testing.T) {
	tests := []struct {
		name           string
		requestBody    string
		expectedStatus int
		setupMocks     func(*mockTaskQueue, *mockTaskStore)
	}{
		{
			name:           "valid submission",
			requestBody:    `{"type": "orchestration", "input": {"query": "test"}}`,
			expectedStatus: http.StatusAccepted,
			setupMocks:     func(q *mockTaskQueue, s *mockTaskStore) {},
		},
		{
			name:           "with timeout",
			requestBody:    `{"type": "research", "input": {"topic": "AI"}, "timeout": "10m"}`,
			expectedStatus: http.StatusAccepted,
			setupMocks:     func(q *mockTaskQueue, s *mockTaskStore) {},
		},
		{
			name:           "missing type",
			requestBody:    `{"input": {"query": "test"}}`,
			expectedStatus: http.StatusBadRequest,
			setupMocks:     func(q *mockTaskQueue, s *mockTaskStore) {},
		},
		{
			name:           "invalid json",
			requestBody:    `{invalid}`,
			expectedStatus: http.StatusBadRequest,
			setupMocks:     func(q *mockTaskQueue, s *mockTaskStore) {},
		},
		{
			name:           "invalid timeout format",
			requestBody:    `{"type": "test", "timeout": "invalid"}`,
			expectedStatus: http.StatusBadRequest,
			setupMocks:     func(q *mockTaskQueue, s *mockTaskStore) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := &mockTaskQueue{}
			store := newMockTaskStore()
			tt.setupMocks(queue, store)

			handler := NewTaskAPIHandler(queue, store, nil)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			handler.HandleSubmit(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("HandleSubmit() status = %v, want %v", rr.Code, tt.expectedStatus)
			}

			if tt.expectedStatus == http.StatusAccepted {
				var resp TaskSubmitResponse
				if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
					t.Errorf("Failed to parse response: %v", err)
				}
				if resp.TaskID == "" {
					t.Error("TaskID should not be empty")
				}
				if resp.Status != "queued" {
					t.Errorf("Status = %v, want queued", resp.Status)
				}
			}
		})
	}
}

func TestTaskAPIHandler_HandleGetTask(t *testing.T) {
	tests := []struct {
		name           string
		taskID         string
		setupTask      func(*mockTaskStore)
		expectedStatus int
	}{
		{
			name:   "existing task",
			taskID: "task-123",
			setupTask: func(s *mockTaskStore) {
				s.tasks["task-123"] = &core.Task{
					ID:        "task-123",
					Type:      "test",
					Status:    core.TaskStatusRunning,
					CreatedAt: time.Now(),
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:   "completed task with result",
			taskID: "task-456",
			setupTask: func(s *mockTaskStore) {
				completed := time.Now()
				s.tasks["task-456"] = &core.Task{
					ID:          "task-456",
					Type:        "test",
					Status:      core.TaskStatusCompleted,
					Result:      map[string]interface{}{"data": "result"},
					CreatedAt:   time.Now(),
					CompletedAt: &completed,
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:   "failed task with error",
			taskID: "task-789",
			setupTask: func(s *mockTaskStore) {
				completed := time.Now()
				s.tasks["task-789"] = &core.Task{
					ID:          "task-789",
					Type:        "test",
					Status:      core.TaskStatusFailed,
					Error:       &core.TaskError{Code: "ERROR", Message: "failed"},
					CreatedAt:   time.Now(),
					CompletedAt: &completed,
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "non-existent task",
			taskID:         "non-existent",
			setupTask:      func(s *mockTaskStore) {},
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := &mockTaskQueue{}
			store := newMockTaskStore()
			tt.setupTask(store)

			handler := NewTaskAPIHandler(queue, store, nil)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+tt.taskID, nil)
			rr := httptest.NewRecorder()

			handler.HandleGetTask(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("HandleGetTask() status = %v, want %v", rr.Code, tt.expectedStatus)
			}

			if tt.expectedStatus == http.StatusOK {
				var resp TaskStatusResponse
				if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
					t.Errorf("Failed to parse response: %v", err)
				}
				if resp.TaskID != tt.taskID {
					t.Errorf("TaskID = %v, want %v", resp.TaskID, tt.taskID)
				}
			}
		})
	}
}

func TestTaskAPIHandler_HandleCancel(t *testing.T) {
	tests := []struct {
		name           string
		taskID         string
		setupTask      func(*mockTaskStore)
		expectedStatus int
	}{
		{
			name:   "cancel queued task",
			taskID: "task-123",
			setupTask: func(s *mockTaskStore) {
				s.tasks["task-123"] = &core.Task{
					ID:        "task-123",
					Type:      "test",
					Status:    core.TaskStatusQueued,
					CreatedAt: time.Now(),
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:   "cancel running task",
			taskID: "task-456",
			setupTask: func(s *mockTaskStore) {
				started := time.Now()
				s.tasks["task-456"] = &core.Task{
					ID:        "task-456",
					Type:      "test",
					Status:    core.TaskStatusRunning,
					CreatedAt: time.Now(),
					StartedAt: &started,
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:   "cancel already completed task",
			taskID: "task-789",
			setupTask: func(s *mockTaskStore) {
				completed := time.Now()
				s.tasks["task-789"] = &core.Task{
					ID:          "task-789",
					Type:        "test",
					Status:      core.TaskStatusCompleted,
					CreatedAt:   time.Now(),
					CompletedAt: &completed,
				}
			},
			expectedStatus: http.StatusConflict,
		},
		{
			name:           "cancel non-existent task",
			taskID:         "non-existent",
			setupTask:      func(s *mockTaskStore) {},
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := &mockTaskQueue{}
			store := newMockTaskStore()
			tt.setupTask(store)

			handler := NewTaskAPIHandler(queue, store, nil)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+tt.taskID+"/cancel", nil)
			rr := httptest.NewRecorder()

			handler.HandleCancel(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("HandleCancel() status = %v, want %v", rr.Code, tt.expectedStatus)
			}
		})
	}
}

func TestTaskAPIHandler_RegisterRoutes(t *testing.T) {
	queue := &mockTaskQueue{}
	store := newMockTaskStore()
	handler := NewTaskAPIHandler(queue, store, nil)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Test POST /api/v1/tasks
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(`{"type":"test"}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Errorf("POST /api/v1/tasks status = %v, want %v", rr.Code, http.StatusAccepted)
	}

	// Test GET /api/v1/tasks (method not allowed)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/v1/tasks status = %v, want %v", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestTaskAPIHandler_extractTaskID(t *testing.T) {
	handler := &TaskAPIHandler{}

	tests := []struct {
		path     string
		prefix   string
		expected string
	}{
		{"/api/v1/tasks/task-123", "/api/v1/tasks/", "task-123"},
		{"/api/v1/tasks/task-123/cancel", "/api/v1/tasks/", "task-123"},
		{"/api/v1/tasks/", "/api/v1/tasks/", ""},
		{"/other/path", "/api/v1/tasks/", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := handler.extractTaskID(tt.path, tt.prefix)
			if got != tt.expected {
				t.Errorf("extractTaskID(%q, %q) = %q, want %q", tt.path, tt.prefix, got, tt.expected)
			}
		})
	}
}

func TestErrorResponse(t *testing.T) {
	queue := &mockTaskQueue{}
	store := newMockTaskStore()
	handler := NewTaskAPIHandler(queue, store, nil)

	// Test error response format
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/non-existent", nil)
	rr := httptest.NewRecorder()

	handler.HandleGetTask(rr, req)

	var errResp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("Failed to parse error response: %v", err)
	}

	if errResp.Error == "" {
		t.Error("Error message should not be empty")
	}
	if errResp.Code == "" {
		t.Error("Error code should not be empty")
	}
}

func TestTaskAPIHandler_SetLogger(t *testing.T) {
	queue := &mockTaskQueue{}
	store := newMockTaskStore()
	handler := NewTaskAPIHandler(queue, store, nil)

	// Initially logger should be nil
	if handler.logger != nil {
		t.Error("Logger should be nil initially")
	}

	// Create a mock logger
	logger := &mockLogger{}

	// Set the logger
	handler.SetLogger(logger)

	// Verify logger is set
	if handler.logger == nil {
		t.Error("Logger should not be nil after SetLogger")
	}

	// Set nil logger - implementation ignores nil, so logger stays set
	handler.SetLogger(nil)
	if handler.logger == nil {
		t.Error("Logger should remain set after SetLogger(nil) - nil is ignored")
	}
}

func TestTaskAPIHandler_RegisterRoutes_CancelEndpoint(t *testing.T) {
	queue := &mockTaskQueue{}
	store := newMockTaskStore()

	// Add a task that can be cancelled
	store.tasks["task-cancel-test"] = &core.Task{
		ID:        "task-cancel-test",
		Type:      "test",
		Status:    core.TaskStatusRunning,
		CreatedAt: time.Now(),
	}

	handler := NewTaskAPIHandler(queue, store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Test POST /api/v1/tasks/{id}/cancel
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-cancel-test/cancel", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("POST /api/v1/tasks/{id}/cancel status = %v, want %v", rr.Code, http.StatusOK)
	}

	// Verify task was cancelled
	task, _ := store.Get(context.Background(), "task-cancel-test")
	if task.Status != core.TaskStatusCancelled {
		t.Errorf("Task status = %v, want cancelled", task.Status)
	}
}

func TestTaskAPIHandler_HandleSubmit_EnqueueError(t *testing.T) {
	queue := &mockTaskQueue{enqueueErr: errors.New("queue full")}
	store := newMockTaskStore()
	handler := NewTaskAPIHandler(queue, store, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(`{"type": "test"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.HandleSubmit(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("HandleSubmit() with enqueue error status = %v, want %v", rr.Code, http.StatusInternalServerError)
	}
}
