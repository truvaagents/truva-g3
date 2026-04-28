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
)

// =============================================================================
// Mock Implementations for Testing
// =============================================================================

// mockInterruptController implements InterruptController for testing
type mockInterruptController struct {
	processCommandResult *ResumeResult
	processCommandErr    error
	resumeResult         *ExecutionResult
	resumeErr            error

	// Capture calls for verification
	lastCommand *Command
}

func newMockInterruptController() *mockInterruptController {
	return &mockInterruptController{}
}

func (m *mockInterruptController) SetPolicy(policy InterruptPolicy) {}

func (m *mockInterruptController) SetHandler(handler InterruptHandler) {}

func (m *mockInterruptController) SetCheckpointStore(store CheckpointStore) {}

func (m *mockInterruptController) CheckPlanApproval(ctx context.Context, plan *RoutingPlan) (*ExecutionCheckpoint, error) {
	return nil, nil
}

func (m *mockInterruptController) CheckBeforeStep(ctx context.Context, step RoutingStep, plan *RoutingPlan) (*ExecutionCheckpoint, error) {
	return nil, nil
}

func (m *mockInterruptController) CheckAfterStep(ctx context.Context, step RoutingStep, result *StepResult) (*ExecutionCheckpoint, error) {
	return nil, nil
}

func (m *mockInterruptController) CheckOnError(ctx context.Context, step RoutingStep, err error, attempts int) (*ExecutionCheckpoint, error) {
	return nil, nil
}

func (m *mockInterruptController) ProcessCommand(ctx context.Context, command *Command) (*ResumeResult, error) {
	m.lastCommand = command
	if m.processCommandErr != nil {
		return nil, m.processCommandErr
	}
	return m.processCommandResult, nil
}

func (m *mockInterruptController) ResumeExecution(ctx context.Context, checkpointID string) (*ExecutionResult, error) {
	if m.resumeErr != nil {
		return nil, m.resumeErr
	}
	return m.resumeResult, nil
}

func (m *mockInterruptController) UpdateCheckpointProgress(ctx context.Context, checkpointID string, completedSteps []StepResult) error {
	return nil
}

// mockCheckpointStore implements CheckpointStore for testing
type mockCheckpointStore struct {
	checkpoints map[string]*ExecutionCheckpoint
	pendingList []*ExecutionCheckpoint
	saveErr     error
	loadErr     error
	listErr     error
	deleteErr   error
}

func newMockCheckpointStore() *mockCheckpointStore {
	return &mockCheckpointStore{
		checkpoints: make(map[string]*ExecutionCheckpoint),
	}
}

func (m *mockCheckpointStore) SaveCheckpoint(ctx context.Context, cp *ExecutionCheckpoint) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.checkpoints[cp.CheckpointID] = cp
	return nil
}

func (m *mockCheckpointStore) LoadCheckpoint(ctx context.Context, checkpointID string) (*ExecutionCheckpoint, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	cp, exists := m.checkpoints[checkpointID]
	if !exists {
		return nil, &ErrCheckpointNotFound{CheckpointID: checkpointID}
	}
	return cp, nil
}

func (m *mockCheckpointStore) UpdateCheckpointStatus(ctx context.Context, checkpointID string, status CheckpointStatus) error {
	cp, exists := m.checkpoints[checkpointID]
	if !exists {
		return &ErrCheckpointNotFound{CheckpointID: checkpointID}
	}
	cp.Status = status
	return nil
}

func (m *mockCheckpointStore) ListPendingCheckpoints(ctx context.Context, filter CheckpointFilter) ([]*ExecutionCheckpoint, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.pendingList, nil
}

func (m *mockCheckpointStore) DeleteCheckpoint(ctx context.Context, checkpointID string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.checkpoints, checkpointID)
	return nil
}

// Expiry processor methods (no-op for mock)
func (m *mockCheckpointStore) StartExpiryProcessor(ctx context.Context, config ExpiryProcessorConfig) error {
	return nil
}

func (m *mockCheckpointStore) StopExpiryProcessor(ctx context.Context) error {
	return nil
}

func (m *mockCheckpointStore) SetExpiryCallback(callback ExpiryCallback) error {
	return nil
}

// =============================================================================
// HITLHandler Tests
// =============================================================================

func TestHITLHandler_HandleCommand(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		requestBody    string
		setupMocks     func(*mockInterruptController, *mockCheckpointStore)
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:        "valid approve command",
			method:      http.MethodPost,
			requestBody: `{"checkpoint_id": "cp-123", "type": "approve", "user_id": "user1"}`,
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-123"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-123",
					RequestID:         "req-123",
					OriginalRequestID: "orig-req-123",
					Status:            CheckpointStatusPending,
				}
				ctrl.processCommandResult = &ResumeResult{
					CheckpointID: "cp-123",
					Action:       CommandApprove,
					ShouldResume: true,
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var result ResumeResult
				if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if !result.ShouldResume {
					t.Error("Expected ShouldResume to be true")
				}
				if result.Action != CommandApprove {
					t.Errorf("Expected action %s, got %s", CommandApprove, result.Action)
				}
			},
		},
		{
			name:        "valid reject command",
			method:      http.MethodPost,
			requestBody: `{"checkpoint_id": "cp-456", "type": "reject", "feedback": "Not approved"}`,
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-456"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-456",
					RequestID:         "req-456",
					OriginalRequestID: "orig-req-456",
					Status:            CheckpointStatusPending,
				}
				ctrl.processCommandResult = &ResumeResult{
					CheckpointID: "cp-456",
					Action:       CommandReject,
					ShouldResume: false,
					Feedback:     "Not approved",
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var result ResumeResult
				if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if result.ShouldResume {
					t.Error("Expected ShouldResume to be false for reject")
				}
			},
		},
		{
			name:        "valid abort command",
			method:      http.MethodPost,
			requestBody: `{"checkpoint_id": "cp-789", "type": "abort"}`,
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-789"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-789",
					RequestID:         "req-789",
					OriginalRequestID: "orig-req-789",
					Status:            CheckpointStatusPending,
				}
				ctrl.processCommandResult = &ResumeResult{
					CheckpointID: "cp-789",
					Action:       CommandAbort,
					ShouldResume: false,
					Abort:        true,
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "missing checkpoint_id",
			method:         http.MethodPost,
			requestBody:    `{"type": "approve"}`,
			setupMocks:     func(ctrl *mockInterruptController, store *mockCheckpointStore) {},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "missing type",
			method:         http.MethodPost,
			requestBody:    `{"checkpoint_id": "cp-123"}`,
			setupMocks:     func(ctrl *mockInterruptController, store *mockCheckpointStore) {},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "invalid command type",
			method:         http.MethodPost,
			requestBody:    `{"checkpoint_id": "cp-123", "type": "invalid_type"}`,
			setupMocks:     func(ctrl *mockInterruptController, store *mockCheckpointStore) {},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "invalid json",
			method:         http.MethodPost,
			requestBody:    `{invalid}`,
			setupMocks:     func(ctrl *mockInterruptController, store *mockCheckpointStore) {},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "wrong http method",
			method:         http.MethodGet,
			requestBody:    `{"checkpoint_id": "cp-123", "type": "approve"}`,
			setupMocks:     func(ctrl *mockInterruptController, store *mockCheckpointStore) {},
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:        "checkpoint not found",
			method:      http.MethodPost,
			requestBody: `{"checkpoint_id": "cp-nonexistent", "type": "approve"}`,
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				ctrl.processCommandErr = &ErrCheckpointNotFound{CheckpointID: "cp-nonexistent"}
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:        "invalid command error",
			method:      http.MethodPost,
			requestBody: `{"checkpoint_id": "cp-123", "type": "approve"}`,
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-123"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-123",
					RequestID:         "req-123",
					OriginalRequestID: "orig-req-123",
					Status:            CheckpointStatusApproved, // Already processed
				}
				ctrl.processCommandErr = &ErrInvalidCommand{CommandType: CommandApprove, Reason: "already processed"}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:        "internal error",
			method:      http.MethodPost,
			requestBody: `{"checkpoint_id": "cp-123", "type": "approve"}`,
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-123"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-123",
					RequestID:         "req-123",
					OriginalRequestID: "orig-req-123",
					Status:            CheckpointStatusPending,
				}
				ctrl.processCommandErr = errors.New("database connection failed")
			},
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			ctrl := newMockInterruptController()
			store := newMockCheckpointStore()
			tt.setupMocks(ctrl, store)

			// Create handler
			handler := NewHITLHandler(ctrl, store)

			// Create request
			req := httptest.NewRequest(tt.method, "/hitl/command", bytes.NewBufferString(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")

			// Execute request
			rr := httptest.NewRecorder()
			handler.HandleCommand(rr, req)

			// Check status code
			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d. Body: %s", tt.expectedStatus, rr.Code, rr.Body.String())
			}

			// Check response if provided
			if tt.checkResponse != nil {
				tt.checkResponse(t, rr)
			}
		})
	}
}

func TestHITLHandler_HandleListCheckpoints(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		queryParams    string
		setupMocks     func(*mockCheckpointStore)
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:        "list pending checkpoints",
			method:      http.MethodGet,
			queryParams: "",
			setupMocks: func(store *mockCheckpointStore) {
				store.pendingList = []*ExecutionCheckpoint{
					{
						CheckpointID: "cp-1",
						RequestID:    "req-1",
						Status:       CheckpointStatusPending,
						CreatedAt:    time.Now(),
					},
					{
						CheckpointID: "cp-2",
						RequestID:    "req-2",
						Status:       CheckpointStatusPending,
						CreatedAt:    time.Now(),
					},
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var response ListCheckpointsResponse
				if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if response.Count != 2 {
					t.Errorf("Expected count 2, got %d", response.Count)
				}
				if len(response.Checkpoints) != 2 {
					t.Errorf("Expected 2 checkpoints, got %d", len(response.Checkpoints))
				}
			},
		},
		{
			name:        "list with request_id filter",
			method:      http.MethodGet,
			queryParams: "?request_id=req-1",
			setupMocks: func(store *mockCheckpointStore) {
				store.pendingList = []*ExecutionCheckpoint{
					{
						CheckpointID: "cp-1",
						RequestID:    "req-1",
						Status:       CheckpointStatusPending,
					},
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var response ListCheckpointsResponse
				if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if response.Count != 1 {
					t.Errorf("Expected count 1, got %d", response.Count)
				}
			},
		},
		{
			name:        "list with limit and offset",
			method:      http.MethodGet,
			queryParams: "?limit=10&offset=5",
			setupMocks: func(store *mockCheckpointStore) {
				store.pendingList = []*ExecutionCheckpoint{}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var response ListCheckpointsResponse
				if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if response.Limit != 10 {
					t.Errorf("Expected limit 10, got %d", response.Limit)
				}
				if response.Offset != 5 {
					t.Errorf("Expected offset 5, got %d", response.Offset)
				}
			},
		},
		{
			name:        "empty list",
			method:      http.MethodGet,
			queryParams: "",
			setupMocks: func(store *mockCheckpointStore) {
				store.pendingList = []*ExecutionCheckpoint{}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var response ListCheckpointsResponse
				if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if response.Count != 0 {
					t.Errorf("Expected count 0, got %d", response.Count)
				}
			},
		},
		{
			name:           "wrong http method",
			method:         http.MethodPost,
			queryParams:    "",
			setupMocks:     func(store *mockCheckpointStore) {},
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:        "store error",
			method:      http.MethodGet,
			queryParams: "",
			setupMocks: func(store *mockCheckpointStore) {
				store.listErr = errors.New("redis connection failed")
			},
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			ctrl := newMockInterruptController()
			store := newMockCheckpointStore()
			tt.setupMocks(store)

			// Create handler
			handler := NewHITLHandler(ctrl, store)

			// Create request
			req := httptest.NewRequest(tt.method, "/hitl/checkpoints"+tt.queryParams, nil)

			// Execute request
			rr := httptest.NewRecorder()
			handler.HandleListCheckpoints(rr, req)

			// Check status code
			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d. Body: %s", tt.expectedStatus, rr.Code, rr.Body.String())
			}

			// Check response if provided
			if tt.checkResponse != nil {
				tt.checkResponse(t, rr)
			}
		})
	}
}

func TestHITLHandler_HandleGetCheckpoint(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		path           string
		setupMocks     func(*mockCheckpointStore)
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:   "get existing checkpoint",
			method: http.MethodGet,
			path:   "/hitl/checkpoints/cp-123",
			setupMocks: func(store *mockCheckpointStore) {
				store.checkpoints["cp-123"] = &ExecutionCheckpoint{
					CheckpointID:   "cp-123",
					RequestID:      "req-456",
					Status:         CheckpointStatusPending,
					InterruptPoint: InterruptPointPlanGenerated,
					Decision: &InterruptDecision{
						ShouldInterrupt: true,
						Reason:          ReasonPlanApproval,
						Message:         "Plan requires approval",
						Priority:        PriorityNormal,
					},
					CreatedAt: time.Now(),
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var cp ExecutionCheckpoint
				if err := json.NewDecoder(rr.Body).Decode(&cp); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if cp.CheckpointID != "cp-123" {
					t.Errorf("Expected checkpoint_id cp-123, got %s", cp.CheckpointID)
				}
				if cp.Status != CheckpointStatusPending {
					t.Errorf("Expected status pending, got %s", cp.Status)
				}
			},
		},
		{
			name:           "checkpoint not found",
			method:         http.MethodGet,
			path:           "/hitl/checkpoints/cp-nonexistent",
			setupMocks:     func(store *mockCheckpointStore) {},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "missing checkpoint id",
			method:         http.MethodGet,
			path:           "/hitl/checkpoints/",
			setupMocks:     func(store *mockCheckpointStore) {},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "wrong http method",
			method:         http.MethodPost,
			path:           "/hitl/checkpoints/cp-123",
			setupMocks:     func(store *mockCheckpointStore) {},
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:   "store error",
			method: http.MethodGet,
			path:   "/hitl/checkpoints/cp-123",
			setupMocks: func(store *mockCheckpointStore) {
				store.loadErr = errors.New("redis timeout")
			},
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			ctrl := newMockInterruptController()
			store := newMockCheckpointStore()
			tt.setupMocks(store)

			// Create handler
			handler := NewHITLHandler(ctrl, store)

			// Create request
			req := httptest.NewRequest(tt.method, tt.path, nil)

			// Execute request
			rr := httptest.NewRecorder()
			handler.HandleGetCheckpoint(rr, req)

			// Check status code
			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d. Body: %s", tt.expectedStatus, rr.Code, rr.Body.String())
			}

			// Check response if provided
			if tt.checkResponse != nil {
				tt.checkResponse(t, rr)
			}
		})
	}
}

func TestHITLHandler_RegisterRoutes(t *testing.T) {
	ctrl := newMockInterruptController()
	store := newMockCheckpointStore()

	// Add a checkpoint so the get endpoint returns 200
	store.checkpoints["cp-123"] = &ExecutionCheckpoint{
		CheckpointID: "cp-123",
		Status:       CheckpointStatusPending,
	}

	handler := NewHITLHandler(ctrl, store)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Test that routes are registered by making requests
	tests := []struct {
		method         string
		path           string
		body           string
		expectedStatus int
	}{
		// POST /hitl/command - requires valid JSON body but missing fields returns 400 (not 404)
		{http.MethodPost, "/hitl/command", `{}`, http.StatusBadRequest},
		// GET /hitl/checkpoints - returns 200 with empty list
		{http.MethodGet, "/hitl/checkpoints", "", http.StatusOK},
		// GET /hitl/checkpoints/{id} - returns 200 since checkpoint exists
		{http.MethodGet, "/hitl/checkpoints/cp-123", "", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			var req *http.Request
			if tt.body != "" {
				req = httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Verify expected status
			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d. Body: %s", tt.expectedStatus, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestIsValidCommandType(t *testing.T) {
	validTypes := []CommandType{
		CommandApprove,
		CommandEdit,
		CommandReject,
		CommandSkip,
		CommandAbort,
		CommandRetry,
		CommandRespond,
	}

	for _, ct := range validTypes {
		t.Run(string(ct), func(t *testing.T) {
			if !isValidCommandType(ct) {
				t.Errorf("Expected %s to be valid", ct)
			}
		})
	}

	invalidTypes := []CommandType{
		"invalid",
		"",
		"unknown",
	}

	for _, ct := range invalidTypes {
		t.Run("invalid_"+string(ct), func(t *testing.T) {
			if isValidCommandType(ct) {
				t.Errorf("Expected %s to be invalid", ct)
			}
		})
	}
}

func TestNewHITLHandler_WithOptions(t *testing.T) {
	ctrl := newMockInterruptController()
	store := newMockCheckpointStore()

	// Test with logger option
	handler := NewHITLHandler(ctrl, store, WithHITLHandlerLogger(nil))
	if handler == nil {
		t.Fatal("Expected handler to be created")
	}

	// Handler should work even with nil logger option
	req := httptest.NewRequest(http.MethodGet, "/hitl/checkpoints", nil)
	rr := httptest.NewRecorder()
	handler.HandleListCheckpoints(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status OK, got %d", rr.Code)
	}
}

func TestHITLHandler_HandleResume(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		path           string
		setupMocks     func(*mockInterruptController, *mockCheckpointStore)
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:   "successful resume execution",
			method: http.MethodPost,
			path:   "/hitl/resume/cp-123",
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-123"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-123",
					RequestID:         "req-123",
					OriginalRequestID: "orig-req-123",
					Status:            CheckpointStatusApproved,
				}
				ctrl.resumeResult = &ExecutionResult{
					PlanID:  "plan-123",
					Success: true,
					Steps: []StepResult{
						{StepID: "step-1", Success: true},
						{StepID: "step-2", Success: true},
					},
					TotalDuration: 5 * time.Second,
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var result ExecutionResult
				if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if !result.Success {
					t.Error("Expected Success to be true")
				}
				if len(result.Steps) != 2 {
					t.Errorf("Expected 2 steps, got %d", len(result.Steps))
				}
				if result.PlanID != "plan-123" {
					t.Errorf("Expected plan_id plan-123, got %s", result.PlanID)
				}
			},
		},
		{
			name:   "resume with failed execution",
			method: http.MethodPost,
			path:   "/hitl/resume/cp-456",
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-456"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-456",
					RequestID:         "req-456",
					OriginalRequestID: "orig-req-456",
					Status:            CheckpointStatusApproved,
				}
				ctrl.resumeResult = &ExecutionResult{
					PlanID:  "plan-456",
					Success: false,
					Steps: []StepResult{
						{StepID: "step-1", Success: true},
						{StepID: "step-2", Success: false, Error: "tool execution failed"},
					},
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var result ExecutionResult
				if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if result.Success {
					t.Error("Expected Success to be false")
				}
			},
		},
		{
			name:   "checkpoint not found",
			method: http.MethodPost,
			path:   "/hitl/resume/cp-nonexistent",
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// No checkpoint in store - handler returns 404 from store lookup
				ctrl.resumeErr = &ErrCheckpointNotFound{CheckpointID: "cp-nonexistent"}
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:   "checkpoint expired",
			method: http.MethodPost,
			path:   "/hitl/resume/cp-expired",
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-expired"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-expired",
					RequestID:         "req-expired",
					OriginalRequestID: "orig-req-expired",
					Status:            CheckpointStatusExpiredRejected,
				}
				ctrl.resumeErr = &ErrCheckpointExpired{CheckpointID: "cp-expired"}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "invalid command error",
			method: http.MethodPost,
			path:   "/hitl/resume/cp-invalid",
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-invalid"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-invalid",
					RequestID:         "req-invalid",
					OriginalRequestID: "orig-req-invalid",
					Status:            CheckpointStatusPending,
				}
				ctrl.resumeErr = &ErrInvalidCommand{CommandType: CommandApprove, Reason: "checkpoint not approved"}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "internal error",
			method: http.MethodPost,
			path:   "/hitl/resume/cp-error",
			setupMocks: func(ctrl *mockInterruptController, store *mockCheckpointStore) {
				// Pre-populate checkpoint in store (handler loads it for trace correlation)
				store.checkpoints["cp-error"] = &ExecutionCheckpoint{
					CheckpointID:      "cp-error",
					RequestID:         "req-error",
					OriginalRequestID: "orig-req-error",
					Status:            CheckpointStatusApproved,
				}
				ctrl.resumeErr = errors.New("execution engine failed")
			},
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:           "missing checkpoint id",
			method:         http.MethodPost,
			path:           "/hitl/resume/",
			setupMocks:     func(ctrl *mockInterruptController, store *mockCheckpointStore) {},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "wrong http method GET",
			method:         http.MethodGet,
			path:           "/hitl/resume/cp-123",
			setupMocks:     func(ctrl *mockInterruptController, store *mockCheckpointStore) {},
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "wrong http method PUT",
			method:         http.MethodPut,
			path:           "/hitl/resume/cp-123",
			setupMocks:     func(ctrl *mockInterruptController, store *mockCheckpointStore) {},
			expectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			ctrl := newMockInterruptController()
			store := newMockCheckpointStore()
			tt.setupMocks(ctrl, store)

			// Create handler
			handler := NewHITLHandler(ctrl, store)

			// Create request
			req := httptest.NewRequest(tt.method, tt.path, nil)

			// Execute request
			rr := httptest.NewRecorder()
			handler.HandleResume(rr, req)

			// Check status code
			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d. Body: %s", tt.expectedStatus, rr.Code, rr.Body.String())
			}

			// Check response if provided
			if tt.checkResponse != nil {
				tt.checkResponse(t, rr)
			}
		})
	}
}

func TestHITLHandler_RegisterRoutes_IncludesResume(t *testing.T) {
	ctrl := newMockInterruptController()
	store := newMockCheckpointStore()

	// Pre-populate checkpoint in store (handler loads it for trace correlation)
	store.checkpoints["cp-123"] = &ExecutionCheckpoint{
		CheckpointID:      "cp-123",
		RequestID:         "req-123",
		OriginalRequestID: "orig-req-123",
		Status:            CheckpointStatusApproved,
	}

	// Setup controller to return a successful result for resume
	ctrl.resumeResult = &ExecutionResult{
		PlanID:  "plan-123",
		Success: true,
	}

	handler := NewHITLHandler(ctrl, store)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Test that resume route is registered
	req := httptest.NewRequest(http.MethodPost, "/hitl/resume/cp-123", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// Should get 200 OK since mock returns successful result
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d. Body: %s", rr.Code, rr.Body.String())
	}

	// Verify the response is an ExecutionResult
	var result ExecutionResult
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if result.PlanID != "plan-123" {
		t.Errorf("Expected plan_id plan-123, got %s", result.PlanID)
	}
}
