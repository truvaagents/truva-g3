package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Mocks
// ═══════════════════════════════════════════════════════════════════════════

type mockOrchestrator struct {
	mu        sync.Mutex
	calls     []mockOrchestratorCall
	returnErr error
}

type mockOrchestratorCall struct {
	request  string
	metadata map[string]interface{}
}

func (m *mockOrchestrator) ProcessRequest(_ context.Context, request string, metadata map[string]interface{}) (*OrchestratorResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mockOrchestratorCall{request: request, metadata: metadata})
	if m.returnErr != nil {
		return nil, m.returnErr
	}
	return &OrchestratorResponse{Response: "ok"}, nil
}

func (m *mockOrchestrator) ExecutePlan(_ context.Context, _ *RoutingPlan) (*ExecutionResult, error) {
	return nil, nil
}

func (m *mockOrchestrator) ExecutePlanWithSynthesis(_ context.Context, _ *RoutingPlan, _ string) (*OrchestratorResponse, error) {
	return nil, nil
}

func (m *mockOrchestrator) GetExecutionHistory() []ExecutionRecord { return nil }
func (m *mockOrchestrator) GetMetrics() OrchestratorMetrics        { return OrchestratorMetrics{} }

var _ Orchestrator = (*mockOrchestrator)(nil)

// ═══════════════════════════════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════════════════════════════

func newTestAgent(t *testing.T) *core.BaseAgent {
	t.Helper()
	agent := core.NewBaseAgent("test-agent")
	return agent
}

func postScheduledRequest(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func decodeToolResponse(t *testing.T, rr *httptest.ResponseRecorder) core.ToolResponse {
	t.Helper()
	var resp core.ToolResponse
	err := json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err, "decode response body")
	return resp
}

// ═══════════════════════════════════════════════════════════════════════════
// Registration validation
// ═══════════════════════════════════════════════════════════════════════════

func TestRegisterScheduledEndpoint_NilAgent(t *testing.T) {
	err := RegisterScheduledEndpoint(nil, func() Orchestrator { return &mockOrchestrator{} })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent is required")
}

func TestRegisterScheduledEndpoint_NilOrchestrator(t *testing.T) {
	agent := newTestAgent(t)
	err := RegisterScheduledEndpoint(agent, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "orchestratorFn is required")
}

// ═══════════════════════════════════════════════════════════════════════════
// Happy path
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduledEndpoint_Success(t *testing.T) {
	orch := &mockOrchestrator{}
	h := &scheduledEndpointHandler{
		agent:          newTestAgent(t),
		orchestratorFn: func() Orchestrator { return orch },
		cfg: scheduledEndpointConfig{
			queryBuilder:    defaultScheduledQueryBuilder,
			metadataBuilder: defaultScheduledMetadataBuilder,
			filter:          func(*ScheduledRequest) bool { return true },
		},
	}

	body := `{"schedule_id":"sch-1","task_id":"t-1","instruction":"check health","input":{"service":"api"}}`
	rr := postScheduledRequest(h.handle, body)

	assert.Equal(t, http.StatusOK, rr.Code)
	resp := decodeToolResponse(t, rr)
	assert.True(t, resp.Success)

	// Orchestrator was called with the right query and metadata.
	orch.mu.Lock()
	defer orch.mu.Unlock()
	require.Len(t, orch.calls, 1)
	assert.Equal(t, "check health", orch.calls[0].request)
	meta := orch.calls[0].metadata
	assert.Equal(t, "sch-1", meta["schedule_id"])
	assert.Equal(t, "t-1", meta["task_id"])
}

// ═══════════════════════════════════════════════════════════════════════════
// Error paths
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduledEndpoint_InvalidJSON(t *testing.T) {
	h := &scheduledEndpointHandler{
		agent:          newTestAgent(t),
		orchestratorFn: func() Orchestrator { return &mockOrchestrator{} },
		cfg: scheduledEndpointConfig{
			queryBuilder:    defaultScheduledQueryBuilder,
			metadataBuilder: defaultScheduledMetadataBuilder,
			filter:          func(*ScheduledRequest) bool { return true },
		},
	}

	rr := postScheduledRequest(h.handle, `{bad json`)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	resp := decodeToolResponse(t, rr)
	assert.False(t, resp.Success)
	assert.Equal(t, "INVALID_JSON", resp.Error.Code)
}

func TestScheduledEndpoint_MissingInstruction(t *testing.T) {
	h := &scheduledEndpointHandler{
		agent:          newTestAgent(t),
		orchestratorFn: func() Orchestrator { return &mockOrchestrator{} },
		cfg: scheduledEndpointConfig{
			queryBuilder:    defaultScheduledQueryBuilder,
			metadataBuilder: defaultScheduledMetadataBuilder,
			filter:          func(*ScheduledRequest) bool { return true },
		},
	}

	body := `{"schedule_id":"sch-1","task_id":"t-1","instruction":""}`
	rr := postScheduledRequest(h.handle, body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	resp := decodeToolResponse(t, rr)
	assert.False(t, resp.Success)
	assert.Equal(t, "MISSING_INSTRUCTION", resp.Error.Code)
}

func TestScheduledEndpoint_OrchestratorError(t *testing.T) {
	orch := &mockOrchestrator{returnErr: errors.New("llm rate limit")}
	h := &scheduledEndpointHandler{
		agent:          newTestAgent(t),
		orchestratorFn: func() Orchestrator { return orch },
		cfg: scheduledEndpointConfig{
			queryBuilder:    defaultScheduledQueryBuilder,
			metadataBuilder: defaultScheduledMetadataBuilder,
			filter:          func(*ScheduledRequest) bool { return true },
		},
	}

	body := `{"schedule_id":"sch-1","task_id":"t-1","instruction":"do thing"}`
	rr := postScheduledRequest(h.handle, body)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	resp := decodeToolResponse(t, rr)
	assert.False(t, resp.Success)
	assert.Equal(t, "ORCHESTRATOR_ERROR", resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "llm rate limit")
}

// ═══════════════════════════════════════════════════════════════════════════
// Filter
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduledEndpoint_FilterReturnsFalse(t *testing.T) {
	orch := &mockOrchestrator{}
	h := &scheduledEndpointHandler{
		agent:          newTestAgent(t),
		orchestratorFn: func() Orchestrator { return orch },
		cfg: scheduledEndpointConfig{
			queryBuilder:    defaultScheduledQueryBuilder,
			metadataBuilder: defaultScheduledMetadataBuilder,
			filter:          func(*ScheduledRequest) bool { return false },
		},
	}

	body := `{"schedule_id":"sch-1","task_id":"t-1","instruction":"do thing"}`
	rr := postScheduledRequest(h.handle, body)
	assert.Equal(t, http.StatusOK, rr.Code)
	resp := decodeToolResponse(t, rr)
	assert.True(t, resp.Success) // Acknowledged but not processed.

	// Orchestrator must NOT have been called.
	orch.mu.Lock()
	defer orch.mu.Unlock()
	assert.Len(t, orch.calls, 0, "orchestrator should not be called when filter returns false")
}

// ═══════════════════════════════════════════════════════════════════════════
// Custom options
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduledEndpoint_CustomQueryBuilder(t *testing.T) {
	orch := &mockOrchestrator{}
	h := &scheduledEndpointHandler{
		agent:          newTestAgent(t),
		orchestratorFn: func() Orchestrator { return orch },
		cfg: scheduledEndpointConfig{
			queryBuilder: func(req *ScheduledRequest) string {
				return "custom: " + req.Instruction
			},
			metadataBuilder: defaultScheduledMetadataBuilder,
			filter:          func(*ScheduledRequest) bool { return true },
		},
	}

	body := `{"schedule_id":"sch-1","task_id":"t-1","instruction":"original"}`
	rr := postScheduledRequest(h.handle, body)
	assert.Equal(t, http.StatusOK, rr.Code)

	orch.mu.Lock()
	defer orch.mu.Unlock()
	require.Len(t, orch.calls, 1)
	assert.Equal(t, "custom: original", orch.calls[0].request)
}

func TestScheduledEndpoint_CustomMetadataBuilder(t *testing.T) {
	orch := &mockOrchestrator{}
	h := &scheduledEndpointHandler{
		agent:          newTestAgent(t),
		orchestratorFn: func() Orchestrator { return orch },
		cfg: scheduledEndpointConfig{
			queryBuilder: defaultScheduledQueryBuilder,
			metadataBuilder: func(req *ScheduledRequest) map[string]interface{} {
				return map[string]interface{}{"custom_key": "custom_value"}
			},
			filter: func(*ScheduledRequest) bool { return true },
		},
	}

	body := `{"schedule_id":"sch-1","task_id":"t-1","instruction":"go"}`
	rr := postScheduledRequest(h.handle, body)
	assert.Equal(t, http.StatusOK, rr.Code)

	orch.mu.Lock()
	defer orch.mu.Unlock()
	require.Len(t, orch.calls, 1)
	assert.Equal(t, "custom_value", orch.calls[0].metadata["custom_key"])
}

// ═══════════════════════════════════════════════════════════════════════════
// Logger nil-safety
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduledEndpoint_NilLogger_NoPanic(t *testing.T) {
	agent := core.NewBaseAgent("test-agent")
	agent.Logger = nil // Explicitly nil.
	orch := &mockOrchestrator{}
	h := &scheduledEndpointHandler{
		agent:          agent,
		orchestratorFn: func() Orchestrator { return orch },
		cfg: scheduledEndpointConfig{
			queryBuilder:    defaultScheduledQueryBuilder,
			metadataBuilder: defaultScheduledMetadataBuilder,
			filter:          func(*ScheduledRequest) bool { return true },
		},
	}

	body := `{"schedule_id":"sch-1","task_id":"t-1","instruction":"go"}`
	rr := postScheduledRequest(h.handle, body)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ═══════════════════════════════════════════════════════════════════════════
// Functional options via public API
// ═══════════════════════════════════════════════════════════════════════════

func TestWithScheduledQueryBuilder_NilIsIgnored(t *testing.T) {
	cfg := scheduledEndpointConfig{queryBuilder: defaultScheduledQueryBuilder}
	WithScheduledQueryBuilder(nil)(&cfg)
	// Should not have overwritten the default.
	assert.NotNil(t, cfg.queryBuilder)
}

func TestWithScheduledFilter_NilIsIgnored(t *testing.T) {
	original := func(*ScheduledRequest) bool { return true }
	cfg := scheduledEndpointConfig{filter: original}
	WithScheduledFilter(nil)(&cfg)
	assert.NotNil(t, cfg.filter)
}

func TestWithScheduledMetadataBuilder_NilIsIgnored(t *testing.T) {
	cfg := scheduledEndpointConfig{metadataBuilder: defaultScheduledMetadataBuilder}
	WithScheduledMetadataBuilder(nil)(&cfg)
	assert.NotNil(t, cfg.metadataBuilder)
}

func TestWithScheduledEndpointLogger_SetsLogger(t *testing.T) {
	cfg := scheduledEndpointConfig{}
	logger := &core.NoOpLogger{}
	WithScheduledEndpointLogger(logger)(&cfg)
	assert.Equal(t, logger, cfg.logger)
}

// ═══════════════════════════════════════════════════════════════════════════
// Default builders
// ═══════════════════════════════════════════════════════════════════════════

func TestDefaultScheduledQueryBuilder_NilReq(t *testing.T) {
	assert.Equal(t, "", defaultScheduledQueryBuilder(nil))
}

func TestDefaultScheduledQueryBuilder_ReturnsInstruction(t *testing.T) {
	req := &ScheduledRequest{Instruction: "hello"}
	assert.Equal(t, "hello", defaultScheduledQueryBuilder(req))
}

func TestDefaultScheduledMetadataBuilder_NilReq(t *testing.T) {
	meta := defaultScheduledMetadataBuilder(nil)
	assert.NotNil(t, meta)
	assert.Len(t, meta, 0)
}

func TestDefaultScheduledMetadataBuilder_IncludesCorrelation(t *testing.T) {
	req := &ScheduledRequest{
		ScheduleID: "sch-1",
		TaskID:     "t-1",
		Input:      map[string]interface{}{"key": "val"},
	}
	meta := defaultScheduledMetadataBuilder(req)
	assert.Equal(t, "sch-1", meta["schedule_id"])
	assert.Equal(t, "t-1", meta["task_id"])
	assert.NotNil(t, meta["scheduled_context"])
}
