package orchestration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// createTestOrchestrator creates an orchestrator configured for testing ExecutePlanWithSynthesis
func createTestOrchestrator(t *testing.T) (*AIOrchestrator, *MockAIClient) {
	t.Helper()

	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	// Register a test agent
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "test-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "test_capability"}},
	})

	config := DefaultConfig()
	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Setup catalog with test data
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:           "test-1",
				Name:         "test-agent",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "test_capability"}},
			},
			Capabilities: []EnhancedCapability{
				{Name: "test_capability", Description: "Test capability"},
			},
		},
	}

	// Replace executor with properly initialized one
	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

	return orchestrator, aiClient
}

// TestExecutePlanWithSynthesis_Success tests successful execution with synthesis
func TestExecutePlanWithSynthesis_Success(t *testing.T) {
	orchestrator, aiClient := createTestOrchestrator(t)

	// Setup mock HTTP client for the executor
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result": "success"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID:          "test-plan-1",
		OriginalRequest: "Test request",
		Mode:            ModeWorkflow,
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	// Execute
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")

	// Assertions
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if response == nil {
		t.Fatal("Expected response, got nil")
	}
	if response.Response == "" {
		t.Error("Expected non-empty response")
	}
	if response.RoutingMode != ModeWorkflow {
		t.Errorf("Expected workflow mode, got: %s", response.RoutingMode)
	}
	if response.RequestID == "" {
		t.Error("Expected request_id to be set")
	}

	// Verify AI client was called for synthesis
	if len(aiClient.calls) == 0 {
		t.Error("Expected at least 1 AI call (synthesis)")
	}
}

// TestExecutePlanWithSynthesis_NoSynthesizer tests fallback when synthesizer is nil
func TestExecutePlanWithSynthesis_NoSynthesizer(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Remove synthesizer to trigger fallback
	orchestrator.synthesizer = nil

	// Setup mock HTTP client for the executor
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result": "success"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	// Execute
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")

	// Assertions
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if response == nil {
		t.Fatal("Expected response, got nil")
	}
	// Should use formatRawExecutionResults fallback
	if !strings.Contains(response.Response, "test-agent") {
		t.Errorf("Expected raw formatted response with agent name, got: %s", response.Response)
	}
}

// TestExecutePlanWithSynthesis_StepFailure tests behavior when individual steps fail
// Note: SmartExecutor.Execute() doesn't return an error for step failures -
// it captures them in the ExecutionResult. The synthesis still proceeds.
func TestExecutePlanWithSynthesis_StepFailure(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Setup mock HTTP client to return error for the step
	mockRT := NewMockRoundTripper()
	mockRT.SetError("http://localhost:8080/process", errors.New("connection refused"))
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	// Execute - should still succeed because SmartExecutor captures step failures in result
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")

	// SmartExecutor returns result with failed steps, not an error
	// The method should complete successfully with synthesized response
	if err != nil {
		t.Fatalf("Expected no error (step failures are in result), got: %v", err)
	}
	if response == nil {
		t.Fatal("Expected response even with step failures")
	}
	if response.Response == "" {
		t.Error("Expected synthesized response even with step failures")
	}
	if response.RoutingMode != ModeWorkflow {
		t.Errorf("Expected workflow mode, got: %s", response.RoutingMode)
	}
}

// TestExecutePlanWithSynthesis_NoExecutor tests behavior when executor is not configured
func TestExecutePlanWithSynthesis_NoExecutor(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Remove executor
	orchestrator.executor = nil

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
			},
		},
	}

	// Execute
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")

	// Assertions
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if response != nil {
		t.Error("Expected nil response when executor not configured")
	}
	if !strings.Contains(err.Error(), "executor not configured") {
		t.Errorf("Expected 'executor not configured' error, got: %v", err)
	}
}

// TestExecutePlanWithSynthesis_MultipleAgents tests execution with multiple different agents
func TestExecutePlanWithSynthesis_MultipleAgents(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	// Register multiple test agents
	agents := []struct {
		id   string
		name string
		cap  string
		port int
	}{
		{"weather-1", "weather-tool", "get_weather", 8081},
		{"geo-1", "geocoding-tool", "geocode", 8082},
		{"currency-1", "currency-tool", "convert_currency", 8083},
	}

	for _, a := range agents {
		_ = discovery.Register(context.Background(), &core.ServiceRegistration{
			ID:           a.id,
			Name:         a.name,
			Address:      "localhost",
			Port:         a.port,
			Capabilities: []core.Capability{{Name: a.cap}},
		})
	}

	config := DefaultConfig()
	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Setup catalog with multiple agents
	orchestrator.catalog.agents = map[string]*AgentInfo{}
	for _, a := range agents {
		orchestrator.catalog.agents[a.id] = &AgentInfo{
			Registration: &core.ServiceRegistration{
				ID:           a.id,
				Name:         a.name,
				Address:      "localhost",
				Port:         a.port,
				Capabilities: []core.Capability{{Name: a.cap}},
			},
			Capabilities: []EnhancedCapability{
				{Name: a.cap, Description: a.cap + " capability"},
			},
		}
	}

	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

	// Setup mock HTTP client for all agents
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8081/process", http.StatusOK, `{"weather": "sunny"}`)
	mockRT.SetResponse("http://localhost:8082/process", http.StatusOK, `{"lat": 35.6762, "lng": 139.6503}`)
	mockRT.SetResponse("http://localhost:8083/process", http.StatusOK, `{"rate": 150}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-multi",
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "weather-tool", Metadata: map[string]interface{}{"capability": "get_weather"}},
			{StepID: "step-2", AgentName: "geocoding-tool", DependsOn: []string{"step-1"}, Metadata: map[string]interface{}{"capability": "geocode"}},
			{StepID: "step-3", AgentName: "currency-tool", Metadata: map[string]interface{}{"capability": "convert_currency"}},
		},
	}

	// Execute
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Plan my trip to Tokyo")

	// Assertions
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if response == nil {
		t.Fatal("Expected response, got nil")
	}
	if len(response.AgentsInvolved) != 3 {
		t.Errorf("Expected 3 agents involved, got: %d (%v)", len(response.AgentsInvolved), response.AgentsInvolved)
	}
	if response.OriginalRequest != "Plan my trip to Tokyo" {
		t.Errorf("Expected original request to be preserved, got: %s", response.OriginalRequest)
	}
}

// TestFormatRawExecutionResults tests the fallback formatting function
func TestFormatRawExecutionResults(t *testing.T) {
	tests := []struct {
		name     string
		result   *ExecutionResult
		contains []string
	}{
		{
			name:     "nil result",
			result:   nil,
			contains: []string{},
		},
		{
			name: "successful steps",
			result: &ExecutionResult{
				Steps: []StepResult{
					{
						StepID:    "step-1",
						AgentName: "weather-tool",
						Response:  "Sunny, 25°C",
						Success:   true,
					},
				},
			},
			contains: []string{"weather-tool", "step-1", "Success", "Sunny, 25°C"},
		},
		{
			name: "failed step",
			result: &ExecutionResult{
				Steps: []StepResult{
					{
						StepID:    "step-1",
						AgentName: "weather-tool",
						Error:     "API timeout",
						Success:   false,
					},
				},
			},
			contains: []string{"weather-tool", "step-1", "Failed", "API timeout"},
		},
		{
			name: "mixed steps",
			result: &ExecutionResult{
				Steps: []StepResult{
					{
						StepID:    "step-1",
						AgentName: "weather-tool",
						Response:  "Sunny",
						Success:   true,
					},
					{
						StepID:    "step-2",
						AgentName: "currency-tool",
						Error:     "Rate limit",
						Success:   false,
					},
				},
			},
			contains: []string{"weather-tool", "currency-tool", "Success", "Failed", "Rate limit"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := formatRawExecutionResults(tc.result)

			if tc.result == nil && output != "" {
				t.Error("Expected empty output for nil result")
			}

			for _, s := range tc.contains {
				if !strings.Contains(output, s) {
					t.Errorf("Expected output to contain '%s', got: %s", s, output)
				}
			}
		})
	}
}

// TestExecutePlanWithSynthesis_RequestIDPropagation tests that request_id is properly set
func TestExecutePlanWithSynthesis_RequestIDPropagation(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Setup mock HTTP client for the executor
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result": "success"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	// Execute
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")

	// Assertions
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if response.RequestID == "" {
		t.Error("Expected request_id to be generated")
	}
	// Request ID should follow the format from generateRequestID()
	if !strings.Contains(response.RequestID, "-") {
		t.Errorf("Expected request_id to contain '-', got: %s", response.RequestID)
	}
}

// TestExecutePlanWithSynthesis_MetricsUpdate tests that metrics are updated
func TestExecutePlanWithSynthesis_MetricsUpdate(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Setup mock HTTP client for the executor
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result": "success"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	// Record initial metrics
	initialTotal := orchestrator.metrics.TotalRequests
	initialSuccess := orchestrator.metrics.SuccessfulRequests

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	// Execute successful request
	_, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Check metrics were updated
	if orchestrator.metrics.TotalRequests != initialTotal+1 {
		t.Errorf("Expected TotalRequests to be %d, got: %d", initialTotal+1, orchestrator.metrics.TotalRequests)
	}
	if orchestrator.metrics.SuccessfulRequests != initialSuccess+1 {
		t.Errorf("Expected SuccessfulRequests to be %d, got: %d", initialSuccess+1, orchestrator.metrics.SuccessfulRequests)
	}
}

// TestExecutePlanWithSynthesis_HistoryAdded tests that response is added to history
func TestExecutePlanWithSynthesis_HistoryAdded(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Setup mock HTTP client for the executor
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result": "success"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	// Record initial history length
	initialHistoryLen := len(orchestrator.GetExecutionHistory())

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	// Execute
	_, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request for history")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Check history was updated
	history := orchestrator.GetExecutionHistory()
	if len(history) != initialHistoryLen+1 {
		t.Errorf("Expected %d history entries, got: %d", initialHistoryLen+1, len(history))
	}

	// Find our entry (should be the most recent)
	found := false
	for _, entry := range history {
		if entry.Request == "Test request for history" {
			found = true
			if entry.RoutingMode != ModeWorkflow {
				t.Errorf("Expected workflow mode in history, got: %s", entry.RoutingMode)
			}
			break
		}
	}
	if !found {
		t.Error("Expected to find our request in history")
	}
}

// TestExecutePlanWithSynthesis_ExecutionTime tests that execution time is tracked
func TestExecutePlanWithSynthesis_ExecutionTime(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Setup mock HTTP client for the executor
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result": "success"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	startTime := time.Now()

	// Execute
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	elapsedTime := time.Since(startTime)

	// Assertions
	if response.ExecutionTime <= 0 {
		t.Error("Expected positive execution time")
	}
	if response.ExecutionTime > elapsedTime {
		t.Errorf("Execution time %v should not exceed elapsed time %v", response.ExecutionTime, elapsedTime)
	}
}

// TestExecutePlanWithSynthesis_Confidence tests that confidence is set
func TestExecutePlanWithSynthesis_Confidence(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Setup mock HTTP client for the executor
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result": "success"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	// Execute
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Assertions
	if response.Confidence != 0.95 {
		t.Errorf("Expected confidence to be 0.95, got: %f", response.Confidence)
	}
}

// errorAIClient is a mock AI client that returns errors for testing
type errorAIClient struct {
	err error
}

func (e *errorAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	return nil, e.err
}

// TestExecutePlanWithSynthesis_SynthesisFailure tests behavior when synthesis fails
func TestExecutePlanWithSynthesis_SynthesisFailure(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Replace synthesizer's AI client with one that returns errors
	orchestrator.synthesizer = NewAISynthesizer(&errorAIClient{
		err: errors.New("AI service unavailable"),
	})

	// Setup mock HTTP client for successful execution
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result": "success"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	// Execute
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")

	// Assertions
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if response != nil {
		t.Error("Expected nil response on synthesis failure")
	}
	if !strings.Contains(err.Error(), "synthesis failed") {
		t.Errorf("Expected 'synthesis failed' error, got: %v", err)
	}
}

// TestExecutePlanWithSynthesis_FailedMetricsUpdate tests that failed metrics are updated
func TestExecutePlanWithSynthesis_FailedMetricsUpdate(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	// Replace synthesizer's AI client with one that returns errors
	orchestrator.synthesizer = NewAISynthesizer(&errorAIClient{
		err: errors.New("AI service unavailable"),
	})

	// Setup mock HTTP client for successful execution
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result": "success"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: mockRT}

	// Record initial metrics
	initialTotal := orchestrator.metrics.TotalRequests
	initialFailed := orchestrator.metrics.FailedRequests

	ctx := context.Background()
	plan := &RoutingPlan{
		PlanID: "test-plan-1",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}

	// Execute - will fail at synthesis
	_, _ = orchestrator.ExecutePlanWithSynthesis(ctx, plan, "Test request")

	// Check metrics were updated for failure
	if orchestrator.metrics.TotalRequests != initialTotal+1 {
		t.Errorf("Expected TotalRequests to be %d, got: %d", initialTotal+1, orchestrator.metrics.TotalRequests)
	}
	if orchestrator.metrics.FailedRequests != initialFailed+1 {
		t.Errorf("Expected FailedRequests to be %d, got: %d", initialFailed+1, orchestrator.metrics.FailedRequests)
	}
}

// TestExecutePlanWithSynthesis_NilPlan tests behavior when plan is nil
func TestExecutePlanWithSynthesis_NilPlan(t *testing.T) {
	orchestrator, _ := createTestOrchestrator(t)

	ctx := context.Background()

	// Execute with nil plan
	response, err := orchestrator.ExecutePlanWithSynthesis(ctx, nil, "Test request")

	// Assertions
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if response != nil {
		t.Error("Expected nil response when plan is nil")
	}
	if !strings.Contains(err.Error(), "plan cannot be nil") {
		t.Errorf("Expected 'plan cannot be nil' error, got: %v", err)
	}
}
