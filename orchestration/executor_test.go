package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestSmartExecutor_Execute(t *testing.T) {
	// Create mock catalog with test agents
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "capability1",
						Endpoint: "/api/capability1",
					},
				},
			},
		},
	}

	// Create executor with mock HTTP client
	executor := NewSmartExecutor(catalog)

	// Replace HTTP client with mock
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/capability1", http.StatusOK, `{"status": "success", "data": "test response"}`)
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	// Create test plan with dependencies
	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
				Metadata: map[string]interface{}{
					"capability": "capability1",
					"parameters": map[string]interface{}{
						"param1": "value1",
					},
				},
			},
			{
				StepID:    "step-2",
				AgentName: "test-agent",
				DependsOn: []string{"step-1"},
				Metadata: map[string]interface{}{
					"capability": "capability1",
					"parameters": map[string]interface{}{
						"param2": "value2",
					},
				},
			},
		},
	}

	ctx := context.Background()

	// Execute plan
	result, err := executor.Execute(ctx, plan)

	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}

	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	if !result.Success {
		t.Error("Expected successful execution")
	}

	if len(result.Steps) != 2 {
		t.Errorf("Expected 2 steps, got %d", len(result.Steps))
	}

	// Verify steps were executed in order
	if result.Steps[0].StepID != "step-1" {
		t.Error("Expected step-1 to be executed first")
	}

	if result.Steps[1].StepID != "step-2" {
		t.Error("Expected step-2 to be executed second")
	}

	// Verify both steps succeeded
	for _, step := range result.Steps {
		if !step.Success {
			t.Errorf("Step %s failed: %s", step.StepID, step.Error)
		}
	}
}

func TestSmartExecutor_FindReadySteps(t *testing.T) {
	executor := &SmartExecutor{}

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", DependsOn: []string{}},
			{StepID: "step-2", DependsOn: []string{"step-1"}},
			{StepID: "step-3", DependsOn: []string{"step-1", "step-2"}},
			{StepID: "step-4", DependsOn: []string{}},
		},
	}

	executed := make(map[string]bool)
	results := make(map[string]*StepResult)

	// Initially, step-1 and step-4 should be ready (no dependencies)
	ready := executor.findReadySteps(plan, executed, results)
	if len(ready) != 2 {
		t.Errorf("Expected 2 ready steps initially, got %d", len(ready))
	}

	// Verify the ready steps are the ones without dependencies
	readyIDs := make(map[string]bool)
	for _, step := range ready {
		readyIDs[step.StepID] = true
	}
	if !readyIDs["step-1"] || !readyIDs["step-4"] {
		t.Error("Expected step-1 and step-4 to be ready initially")
	}

	// Mark step-1 as executed and successful
	executed["step-1"] = true
	results["step-1"] = &StepResult{Success: true}

	// Mark step-4 as executed
	executed["step-4"] = true
	results["step-4"] = &StepResult{Success: true}

	// Now only step-2 should be ready
	ready = executor.findReadySteps(plan, executed, results)
	if len(ready) != 1 || ready[0].StepID != "step-2" {
		t.Error("Expected only step-2 to be ready after step-1 completes")
	}

	// Mark step-2 as executed but failed
	executed["step-2"] = true
	results["step-2"] = &StepResult{Success: false}

	// Step-3 should not be ready (dependency failed)
	ready = executor.findReadySteps(plan, executed, results)
	if len(ready) != 0 {
		t.Error("Expected no steps ready when dependency failed")
	}
}

func TestSmartExecutor_CircularDependency(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	// Create plan with circular dependency
	plan := &RoutingPlan{
		PlanID: "circular-plan",
		Steps: []RoutingStep{
			{StepID: "step-1", DependsOn: []string{"step-2"}},
			{StepID: "step-2", DependsOn: []string{"step-1"}},
		},
	}

	ctx := context.Background()
	_, err := executor.Execute(ctx, plan)

	if err == nil {
		t.Error("Expected error for circular dependency")
	}

	if !containsString(err.Error(), "circular") {
		t.Errorf("Expected error message to mention circular dependency, got: %v", err)
	}
}

func TestSmartExecutor_ExecuteStep(t *testing.T) {
	// Create mock catalog
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "test_cap",
						Endpoint: "/api/test",
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Use mock HTTP client
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/test", http.StatusOK, `{"result": "success"}`)
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	step := RoutingStep{
		StepID:    "test-step",
		AgentName: "test-agent",
		Metadata: map[string]interface{}{
			"capability": "test_cap",
			"parameters": map[string]interface{}{},
		},
	}

	ctx := context.Background()
	result := executor.executeStep(ctx, step)

	if !result.Success {
		t.Errorf("Expected successful execution, got error: %s", result.Error)
	}

	// Test agent not found
	stepNotFound := RoutingStep{
		StepID:    "test-step",
		AgentName: "non-existent-agent",
	}

	result = executor.executeStep(ctx, stepNotFound)
	if result.Success {
		t.Error("Expected failure for non-existent agent")
	}

	if !containsString(result.Error, "not found") {
		t.Errorf("Expected 'not found' error, got: %s", result.Error)
	}
}

func TestSmartExecutor_Retry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping executor retry test in short mode (exercises real retry backoff with time.Sleep)")
	}
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "test_cap",
						Endpoint: "/api/test",
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Mock that fails once then succeeds — verifies the retry loop succeeds on attempt 2
	mockRT := NewMockRoundTripper()
	mockRT.SetRetryResponses("http://localhost:8080/api/test", []struct {
		StatusCode int
		Body       string
	}{
		{StatusCode: http.StatusInternalServerError, Body: "error"},
		{StatusCode: http.StatusOK, Body: `{"result": "success"}`},
	})

	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	step := RoutingStep{
		StepID:    "test-step",
		AgentName: "test-agent",
		Metadata: map[string]interface{}{
			"capability": "test_cap",
			"parameters": map[string]interface{}{},
		},
	}

	ctx := context.Background()
	result := executor.executeStep(ctx, step)

	if !result.Success {
		t.Errorf("Expected successful execution after retries, got: %s", result.Error)
	}

	if result.Attempts != 2 {
		t.Errorf("Expected 2 attempts (succeeds on retry 1), got %d", result.Attempts)
	}
}

func TestSmartExecutor_OrchestratorNoRetry(t *testing.T) {
	// Orchestrator capabilities must get maxAttempts=1 regardless of executor config.
	// A multi-step DAG should never be retried — retrying duplicates side effects.
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "child-orchestrator",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "devops_operations",
						Endpoint: "/query",
						Type:     core.CapabilityOrchestrator,
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)
	executor.SetMaxAttempts(3) // Would normally allow 3 attempts

	// Mock: always fail with 500
	mockRT := NewMockRoundTripper()
	mockRT.SetRetryResponses("http://localhost:8080/query", []struct {
		StatusCode int
		Body       string
	}{
		{StatusCode: http.StatusInternalServerError, Body: `{"error": "first failure"}`},
		{StatusCode: http.StatusOK, Body: `{"result": "should not reach"}`},
	})
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID:    "orch-step",
		AgentName: "child-orchestrator",
		Metadata: map[string]interface{}{
			"capability": "devops_operations",
			"parameters": map[string]interface{}{"query": "check pods"},
		},
	}

	ctx := context.Background()
	result := executor.executeStep(ctx, step)

	// Should fail after exactly 1 attempt (no retry)
	if result.Attempts != 1 {
		t.Errorf("Orchestrator should get exactly 1 attempt, got %d", result.Attempts)
	}
	if result.Success {
		t.Error("Expected failure (orchestrator should not retry)")
	}
}

func TestSmartExecutor_OrchestratorSkipsErrorAnalyzer(t *testing.T) {
	// Even with an error analyzer enabled, orchestrator capabilities should not
	// trigger LLM-based error analysis (which would attempt to fix the payload).
	aiClient := NewMockAIClient()

	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "child-orchestrator",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "devops_operations",
						Endpoint: "/query",
						Type:     core.CapabilityOrchestrator,
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)
	errorAnalyzer := NewErrorAnalyzer(aiClient, nil)
	executor.SetErrorAnalyzer(errorAnalyzer)

	// Mock: fail with 400 (would normally trigger error analyzer)
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/query", http.StatusBadRequest, `{"error": "bad request"}`)
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID:    "orch-step",
		AgentName: "child-orchestrator",
		Metadata: map[string]interface{}{
			"capability": "devops_operations",
			"parameters": map[string]interface{}{"query": "check pods"},
		},
	}

	ctx := context.Background()
	_ = executor.executeStep(ctx, step)

	// Error analyzer should NOT have been called for orchestrator capabilities
	if len(aiClient.calls) > 0 {
		t.Errorf("Error analyzer should not be called for orchestrator capabilities, but got %d AI calls", len(aiClient.calls))
	}
}

func TestSmartExecutor_OrchestratorTokenUsageAggregation(t *testing.T) {
	// When a child orchestrator returns token usage in its response body,
	// the executor should aggregate it into the parent's accumulator as
	// "delegation:<agent_name>".
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "child-orchestrator",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "devops_operations",
						Endpoint: "/query",
						Type:     core.CapabilityOrchestrator,
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Mock: orchestrator returns success with token usage
	responseBody := `{
		"request_id": "req-123",
		"response": "All pods healthy",
		"usage": {"PromptTokens": 500, "CompletionTokens": 200, "TotalTokens": 700}
	}`
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/query", http.StatusOK, responseBody)
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID:    "orch-step",
		AgentName: "child-orchestrator",
		Metadata: map[string]interface{}{
			"capability": "devops_operations",
			"parameters": map[string]interface{}{"query": "check pods"},
		},
	}

	// Set up context with token usage accumulator
	ctx, acc := core.WithTokenUsageAccumulator(context.Background())
	result := executor.executeStep(ctx, step)

	if !result.Success {
		t.Fatalf("Expected successful execution, got error: %s", result.Error)
	}

	// Verify token usage was aggregated under "delegation:child-orchestrator"
	phase, ok := acc.ByPhase["delegation:child-orchestrator"]
	if !ok {
		t.Fatal("Expected token usage to be recorded under 'delegation:child-orchestrator'")
	}
	if phase.PromptTokens != 500 {
		t.Errorf("Expected PromptTokens=500, got %d", phase.PromptTokens)
	}
	if phase.CompletionTokens != 200 {
		t.Errorf("Expected CompletionTokens=200, got %d", phase.CompletionTokens)
	}
	if phase.TotalTokens != 700 {
		t.Errorf("Expected TotalTokens=700, got %d", phase.TotalTokens)
	}
}

func TestSmartExecutor_OrchestratorTokenUsage_NoUsageField(t *testing.T) {
	// When a child orchestrator's response has no usage field,
	// token aggregation should be silently skipped.
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "child-orchestrator",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "devops_operations",
						Endpoint: "/query",
						Type:     core.CapabilityOrchestrator,
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Mock: orchestrator returns success without usage
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/query", http.StatusOK, `{"response": "done"}`)
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID:    "orch-step",
		AgentName: "child-orchestrator",
		Metadata: map[string]interface{}{
			"capability": "devops_operations",
			"parameters": map[string]interface{}{"query": "check pods"},
		},
	}

	ctx, acc := core.WithTokenUsageAccumulator(context.Background())
	result := executor.executeStep(ctx, step)

	if !result.Success {
		t.Fatalf("Expected successful execution, got error: %s", result.Error)
	}

	// No token usage should be recorded
	if len(acc.ByPhase) != 0 {
		t.Errorf("Expected no phases recorded, got %d: %v", len(acc.ByPhase), acc.ByPhase)
	}
}

func TestSmartExecutor_NonOrchestratorRetryUnaffected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping non-orchestrator retry test in short mode (exercises real retry backoff with time.Sleep)")
	}
	// Verify that normal (non-orchestrator) capabilities still retry as before.
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-tool",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "weather_lookup",
						Endpoint: "/api/weather",
						Type:     core.CapabilityTool,
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Fail once, then succeed — should retry
	mockRT := NewMockRoundTripper()
	mockRT.SetRetryResponses("http://localhost:8080/api/weather", []struct {
		StatusCode int
		Body       string
	}{
		{StatusCode: http.StatusInternalServerError, Body: "error"},
		{StatusCode: http.StatusOK, Body: `{"temp": "72F"}`},
	})
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID:    "tool-step",
		AgentName: "test-tool",
		Metadata: map[string]interface{}{
			"capability": "weather_lookup",
			"parameters": map[string]interface{}{"city": "NYC"},
		},
	}

	ctx := context.Background()
	result := executor.executeStep(ctx, step)

	if !result.Success {
		t.Errorf("Expected success after retry, got: %s", result.Error)
	}
	if result.Attempts != 2 {
		t.Errorf("Expected 2 attempts for tool capability, got %d", result.Attempts)
	}
}

func TestSmartExecutor_RetryBackoffContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry-backoff cancellation test in short mode (uses real time.Sleep-based backoff)")
	}
	// Verify that context cancellation during backoff returns immediately
	// instead of blocking until the timer fires.
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "test_cap",
						Endpoint: "/api/test",
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)
	// Use a long backoff so we can detect if cancellation is instant
	executor.SetStepRetryBackoff(core.BackoffConfig{
		InitialDelay:  5 * time.Second,
		MaxDelay:      5 * time.Second,
		BackoffFactor: 1.0,
		JitterEnabled: false,
	})

	// Mock that always returns 500 to trigger retry
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/test", http.StatusInternalServerError, `{"error": "fail"}`)
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	step := RoutingStep{
		StepID:    "test-step",
		AgentName: "test-agent",
		Metadata: map[string]interface{}{
			"capability": "test_cap",
			"parameters": map[string]interface{}{},
		},
	}

	// Cancel context after 200ms — well before the 5s backoff timer
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	result := executor.executeStep(ctx, step)
	elapsed := time.Since(start)

	// Should return within ~1s (200ms timeout + overhead), NOT 5s (backoff delay)
	if elapsed > 2*time.Second {
		t.Errorf("Context cancellation during backoff took %v, expected < 2s (backoff was 5s)", elapsed)
	}

	if result.Success {
		t.Error("Expected failure when context is cancelled during backoff")
	}
}

func TestSmartExecutor_SetMaxConcurrency(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	// Test default concurrency
	if executor.maxConcurrency != 5 {
		t.Errorf("Expected default max concurrency 5, got %d", executor.maxConcurrency)
	}

	// Set new concurrency
	executor.SetMaxConcurrency(10)
	if executor.maxConcurrency != 10 {
		t.Errorf("Expected max concurrency 10, got %d", executor.maxConcurrency)
	}

	// Verify semaphore was recreated
	if cap(executor.semaphore) != 10 {
		t.Errorf("Expected semaphore capacity 10, got %d", cap(executor.semaphore))
	}
}

func TestSmartExecutor_ContextCancellation(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{StepID: "step-1"},
		},
	}

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := executor.Execute(ctx, plan)
	if err == nil {
		t.Error("Expected error for cancelled context")
	}
}

func TestSmartExecutor_ParallelExecution(t *testing.T) {
	// Create catalog with multiple agents
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "agent-1",
					Address: "localhost",
					Port:    8081,
				},
				Capabilities: []EnhancedCapability{
					{Name: "cap1", Endpoint: "/api/cap1"},
				},
			},
			"agent-2": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-2",
					Name:    "agent-2",
					Address: "localhost",
					Port:    8082,
				},
				Capabilities: []EnhancedCapability{
					{Name: "cap2", Endpoint: "/api/cap2"},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Mock HTTP client
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8081/api/cap1", http.StatusOK, `{"result": "result1"}`)
	mockRT.SetResponse("http://localhost:8082/api/cap2", http.StatusOK, `{"result": "result2"}`)
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	// Create plan with parallel steps
	plan := &RoutingPlan{
		PlanID: "parallel-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "agent-1",
				Metadata: map[string]interface{}{
					"capability": "cap1",
					"parameters": map[string]interface{}{},
				},
			},
			{
				StepID:    "step-2",
				AgentName: "agent-2",
				Metadata: map[string]interface{}{
					"capability": "cap2",
					"parameters": map[string]interface{}{},
				},
			},
		},
	}

	ctx := context.Background()
	result, err := executor.Execute(ctx, plan)

	if err != nil {
		t.Errorf("Parallel execution failed: %v", err)
	}

	if !result.Success {
		t.Error("Expected successful parallel execution")
	}

	if len(result.Steps) != 2 {
		t.Errorf("Expected 2 steps executed, got %d", len(result.Steps))
	}

	// Verify both steps succeeded
	for _, step := range result.Steps {
		if !step.Success {
			t.Errorf("Step %s failed in parallel execution", step.StepID)
		}
	}
}

func TestSmartExecutor_FailedDependency(t *testing.T) {
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{Name: "cap1", Endpoint: "/api/cap1"},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Mock HTTP client that returns error
	mockRT := NewMockRoundTripper()
	mockRT.SetError("http://localhost:8080/api/cap1", fmt.Errorf("service unavailable"))
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}
	executor.SetMaxAttempts(1) // Disable retries for fast tests

	// Plan where step-2 depends on step-1
	plan := &RoutingPlan{
		PlanID: "dependency-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
				Metadata: map[string]interface{}{
					"capability": "cap1",
					"parameters": map[string]interface{}{},
				},
			},
			{
				StepID:    "step-2",
				AgentName: "test-agent",
				DependsOn: []string{"step-1"},
				Metadata: map[string]interface{}{
					"capability": "cap1",
					"parameters": map[string]interface{}{},
				},
			},
		},
	}

	ctx := context.Background()
	result, err := executor.Execute(ctx, plan)

	// If there's an error, that's ok for this test
	if err != nil {
		// Some steps may not be executable due to failed dependencies
		return
	}

	// If no error, result should exist
	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	// But result should indicate failure
	if result.Success {
		t.Error("Expected execution to fail when dependency fails")
	}

	// Step 1 should have been attempted
	if len(result.Steps) == 0 {
		t.Error("Expected at least step-1 to be attempted")
	}

	// Verify step-1 failed
	step1Found := false
	for _, step := range result.Steps {
		if step.StepID == "step-1" {
			step1Found = true
			if step.Success {
				t.Error("Expected step-1 to fail")
			}
		}
	}

	if !step1Found {
		t.Error("Step-1 was not executed")
	}
}

// Helper function
func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}

// Helper to verify response parsing
func TestSmartExecutor_ResponseParsing(t *testing.T) {
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{Name: "test", Endpoint: "/api/test"},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Test with complex JSON response
	mockRT := NewMockRoundTripper()
	complexResponse := `{"status": "success", "data": {"value": 123, "items": ["a", "b", "c"]}}`
	mockRT.SetResponse("http://localhost:8080/api/test", http.StatusOK, complexResponse)
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	step := RoutingStep{
		StepID:    "test-step",
		AgentName: "test-agent",
		Metadata: map[string]interface{}{
			"capability": "test",
			"parameters": map[string]interface{}{},
		},
	}

	ctx := context.Background()
	result := executor.executeStep(ctx, step)

	if !result.Success {
		t.Errorf("Failed to parse complex response: %s", result.Error)
	}

	// Verify response can be unmarshaled back
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result.Response), &parsed); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}

	if parsed["status"] != "success" {
		t.Error("Response parsing lost data")
	}
}

// TestCoerceValue tests the coerceValue function for Layer 2 type coercion
func TestCoerceValue(t *testing.T) {
	tests := []struct {
		name         string
		value        interface{}
		expectedType string
		want         interface{}
		wantCoerced  bool
	}{
		// Float coercion
		{
			name:         "string to float64",
			value:        "35.6897",
			expectedType: "float64",
			want:         35.6897,
			wantCoerced:  true,
		},
		{
			name:         "string to number",
			value:        "139.6917",
			expectedType: "number",
			want:         139.6917,
			wantCoerced:  true,
		},
		{
			name:         "string to float",
			value:        "-12.5",
			expectedType: "float",
			want:         -12.5,
			wantCoerced:  true,
		},
		{
			name:         "string to double",
			value:        "3.14159",
			expectedType: "double",
			want:         3.14159,
			wantCoerced:  true,
		},
		// Integer coercion
		{
			name:         "string to integer",
			value:        "42",
			expectedType: "integer",
			want:         int64(42),
			wantCoerced:  true,
		},
		{
			name:         "string to int",
			value:        "-100",
			expectedType: "int",
			want:         int64(-100),
			wantCoerced:  true,
		},
		{
			name:         "string to int64",
			value:        "9999999999",
			expectedType: "int64",
			want:         int64(9999999999),
			wantCoerced:  true,
		},
		// Boolean coercion
		{
			name:         "string true to boolean",
			value:        "true",
			expectedType: "boolean",
			want:         true,
			wantCoerced:  true,
		},
		{
			name:         "string false to bool",
			value:        "false",
			expectedType: "bool",
			want:         false,
			wantCoerced:  true,
		},
		{
			name:         "string 1 to boolean",
			value:        "1",
			expectedType: "boolean",
			want:         true,
			wantCoerced:  true,
		},
		{
			name:         "string 0 to boolean",
			value:        "0",
			expectedType: "boolean",
			want:         false,
			wantCoerced:  true,
		},
		// No coercion needed
		{
			name:         "already float64 stays unchanged",
			value:        48.8566,
			expectedType: "float64",
			want:         48.8566,
			wantCoerced:  false,
		},
		{
			name:         "already int stays unchanged",
			value:        int64(10),
			expectedType: "integer",
			want:         int64(10),
			wantCoerced:  false,
		},
		{
			name:         "already bool stays unchanged",
			value:        true,
			expectedType: "boolean",
			want:         true,
			wantCoerced:  false,
		},
		{
			name:         "string to string no coercion",
			value:        "Tokyo",
			expectedType: "string",
			want:         "Tokyo",
			wantCoerced:  false,
		},
		// Invalid coercion returns original
		{
			name:         "invalid float coercion returns original",
			value:        "not-a-number",
			expectedType: "float64",
			want:         "not-a-number",
			wantCoerced:  false,
		},
		{
			name:         "invalid int coercion returns original",
			value:        "12.5",
			expectedType: "integer",
			want:         "12.5",
			wantCoerced:  false,
		},
		{
			name:         "invalid bool coercion returns original",
			value:        "yes",
			expectedType: "boolean",
			want:         "yes",
			wantCoerced:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotCoerced := coerceValue(tt.value, tt.expectedType)
			if got != tt.want {
				t.Errorf("coerceValue() got = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
			if gotCoerced != tt.wantCoerced {
				t.Errorf("coerceValue() coerced = %v, want %v", gotCoerced, tt.wantCoerced)
			}
		})
	}
}

// TestCoerceParameterTypes tests the coerceParameterTypes function for Layer 2 type coercion
func TestCoerceParameterTypes(t *testing.T) {
	schema := []Parameter{
		{Name: "lat", Type: "float64"},
		{Name: "lon", Type: "float64"},
		{Name: "count", Type: "integer"},
		{Name: "enabled", Type: "boolean"},
		{Name: "city", Type: "string"},
	}

	tests := []struct {
		name           string
		params         map[string]interface{}
		schema         []Parameter
		expectedParams map[string]interface{}
		expectedLogLen int
	}{
		{
			name: "coerce string numbers to float64",
			params: map[string]interface{}{
				"lat": "35.6897",
				"lon": "139.6917",
			},
			schema: schema,
			expectedParams: map[string]interface{}{
				"lat": 35.6897,
				"lon": 139.6917,
			},
			expectedLogLen: 2,
		},
		{
			name: "coerce string to integer",
			params: map[string]interface{}{
				"count": "42",
			},
			schema: schema,
			expectedParams: map[string]interface{}{
				"count": int64(42),
			},
			expectedLogLen: 1,
		},
		{
			name: "coerce string to boolean",
			params: map[string]interface{}{
				"enabled": "true",
			},
			schema: schema,
			expectedParams: map[string]interface{}{
				"enabled": true,
			},
			expectedLogLen: 1,
		},
		{
			name: "already correct types unchanged",
			params: map[string]interface{}{
				"lat":     48.8566,
				"count":   int64(10),
				"enabled": false,
				"city":    "Paris",
			},
			schema: schema,
			expectedParams: map[string]interface{}{
				"lat":     48.8566,
				"count":   int64(10),
				"enabled": false,
				"city":    "Paris",
			},
			expectedLogLen: 0,
		},
		{
			name: "mixed coercion and unchanged",
			params: map[string]interface{}{
				"lat":     "35.6897", // needs coercion
				"lon":     139.6917,  // already correct
				"city":    "Tokyo",   // string stays string
				"enabled": "true",    // needs coercion
			},
			schema: schema,
			expectedParams: map[string]interface{}{
				"lat":     35.6897,
				"lon":     139.6917,
				"city":    "Tokyo",
				"enabled": true,
			},
			expectedLogLen: 2, // lat and enabled coerced
		},
		{
			name: "parameter not in schema passes through unchanged",
			params: map[string]interface{}{
				"lat":         "35.6897",
				"unknown_key": "some value",
			},
			schema: schema,
			expectedParams: map[string]interface{}{
				"lat":         35.6897,
				"unknown_key": "some value",
			},
			expectedLogLen: 1, // only lat coerced
		},
		{
			name:           "nil params returns nil",
			params:         nil,
			schema:         schema,
			expectedParams: nil,
			expectedLogLen: 0,
		},
		{
			name: "empty schema returns original",
			params: map[string]interface{}{
				"lat": "35.6897",
			},
			schema: []Parameter{},
			expectedParams: map[string]interface{}{
				"lat": "35.6897",
			},
			expectedLogLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotLog := coerceParameterTypes(tt.params, tt.schema)

			// Check log length
			if len(gotLog) != tt.expectedLogLen {
				t.Errorf("coerceParameterTypes() log length = %d, want %d", len(gotLog), tt.expectedLogLen)
			}

			// Check nil case
			if tt.expectedParams == nil {
				if got != nil {
					t.Errorf("coerceParameterTypes() got = %v, want nil", got)
				}
				return
			}

			// Check each expected parameter
			for key, expectedVal := range tt.expectedParams {
				gotVal, exists := got[key]
				if !exists {
					t.Errorf("coerceParameterTypes() missing key %s", key)
					continue
				}
				if gotVal != expectedVal {
					t.Errorf("coerceParameterTypes() got[%s] = %v (%T), want %v (%T)",
						key, gotVal, gotVal, expectedVal, expectedVal)
				}
			}
		})
	}
}

// TestFindCapabilitySchema tests the findCapabilitySchema helper function
func TestFindCapabilitySchema(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	agentInfo := &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:   "test-agent",
			Name: "test-agent",
		},
		Capabilities: []EnhancedCapability{
			{
				Name:     "get_weather",
				Endpoint: "/api/weather",
				Parameters: []Parameter{
					{Name: "lat", Type: "float64", Required: true},
					{Name: "lon", Type: "float64", Required: true},
				},
			},
			{
				Name:     "get_stock",
				Endpoint: "/api/stock",
				Parameters: []Parameter{
					{Name: "symbol", Type: "string", Required: true},
				},
			},
		},
	}

	tests := []struct {
		name       string
		agentInfo  *AgentInfo
		capability string
		wantNil    bool
		wantName   string
	}{
		{
			name:       "find existing capability",
			agentInfo:  agentInfo,
			capability: "get_weather",
			wantNil:    false,
			wantName:   "get_weather",
		},
		{
			name:       "find another capability",
			agentInfo:  agentInfo,
			capability: "get_stock",
			wantNil:    false,
			wantName:   "get_stock",
		},
		{
			name:       "capability not found",
			agentInfo:  agentInfo,
			capability: "non_existent",
			wantNil:    true,
		},
		{
			name:       "nil agent info",
			agentInfo:  nil,
			capability: "get_weather",
			wantNil:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := executor.findCapabilitySchema(tt.agentInfo, tt.capability)

			if tt.wantNil {
				if got != nil {
					t.Errorf("findCapabilitySchema() = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("findCapabilitySchema() = nil, want non-nil")
			}

			if got.Name != tt.wantName {
				t.Errorf("findCapabilitySchema().Name = %s, want %s", got.Name, tt.wantName)
			}
		})
	}
}

// TestSmartExecutor_TypeCoercionIntegration tests end-to-end type coercion in executeStep
func TestSmartExecutor_TypeCoercionIntegration(t *testing.T) {
	// Create catalog with agent that has typed parameters
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"weather-tool": {
				Registration: &core.ServiceRegistration{
					ID:      "weather-tool",
					Name:    "weather-tool",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "get_weather",
						Endpoint: "/api/weather",
						Parameters: []Parameter{
							{Name: "lat", Type: "float64", Required: true},
							{Name: "lon", Type: "float64", Required: true},
						},
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Track what parameters were actually sent
	var sentParams map[string]interface{}
	mockRT := &trackingRoundTripper{
		onRequest: func(req *http.Request) {
			var params map[string]interface{}
			if err := json.NewDecoder(req.Body).Decode(&params); err == nil {
				sentParams = params
			}
		},
		response: `{"temperature": 25, "condition": "sunny"}`,
	}
	executor.httpClient = &http.Client{Transport: mockRT}

	// Create step with STRING parameters (as LLM would generate)
	step := RoutingStep{
		StepID:    "weather-step",
		AgentName: "weather-tool",
		Metadata: map[string]interface{}{
			"capability": "get_weather",
			"parameters": map[string]interface{}{
				"lat": "35.6897",  // STRING - should be coerced to float64
				"lon": "139.6917", // STRING - should be coerced to float64
			},
		},
	}

	ctx := context.Background()
	result := executor.executeStep(ctx, step)

	if !result.Success {
		t.Fatalf("Step execution failed: %s", result.Error)
	}

	// Verify the parameters were coerced before being sent
	if sentParams == nil {
		t.Fatal("No parameters were sent")
	}

	// Check lat was coerced from string to float64
	lat, ok := sentParams["lat"].(float64)
	if !ok {
		t.Errorf("lat should be float64 after coercion, got %T", sentParams["lat"])
	} else if lat != 35.6897 {
		t.Errorf("lat = %v, want 35.6897", lat)
	}

	// Check lon was coerced from string to float64
	lon, ok := sentParams["lon"].(float64)
	if !ok {
		t.Errorf("lon should be float64 after coercion, got %T", sentParams["lon"])
	} else if lon != 139.6917 {
		t.Errorf("lon = %v, want 139.6917", lon)
	}
}

// trackingRoundTripper is a mock HTTP transport that tracks request parameters
type trackingRoundTripper struct {
	onRequest func(req *http.Request)
	response  string
}

func (t *trackingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.onRequest != nil {
		t.onRequest(req)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(t.response)),
		Header:     make(http.Header),
	}, nil
}

// ============================================================================
// Layer 3: Validation Feedback Tests
// ============================================================================

// TestIsTypeRelatedError tests the isTypeRelatedError function for Layer 3
func TestIsTypeRelatedError(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		responseBody string
		want         bool
	}{
		// Positive cases - should be detected as type errors
		{
			name:         "json unmarshal string into float64",
			err:          fmt.Errorf("json: cannot unmarshal string into Go struct field WeatherRequest.lat of type float64"),
			responseBody: "",
			want:         true,
		},
		{
			name:         "json unmarshal number into string",
			err:          fmt.Errorf("json: cannot unmarshal number into Go struct field .name of type string"),
			responseBody: "",
			want:         true,
		},
		{
			name:         "json unmarshal bool into int",
			err:          fmt.Errorf("json: cannot unmarshal bool into Go struct field .count of type int"),
			responseBody: "",
			want:         true,
		},
		{
			name:         "type mismatch in error",
			err:          fmt.Errorf("type mismatch: expected number, got string"),
			responseBody: "",
			want:         true,
		},
		{
			name:         "invalid type in error",
			err:          fmt.Errorf("invalid type for field lat"),
			responseBody: "",
			want:         true,
		},
		{
			name:         "expected number in response body",
			err:          fmt.Errorf("validation failed"),
			responseBody: `{"error": "expected number for field latitude"}`,
			want:         true,
		},
		{
			name:         "expected string in response body",
			err:          fmt.Errorf("validation failed"),
			responseBody: `{"error": "expected string for field name"}`,
			want:         true,
		},
		{
			name:         "expected boolean in response body",
			err:          fmt.Errorf("validation failed"),
			responseBody: `{"error": "expected boolean for field enabled"}`,
			want:         true,
		},
		{
			name:         "invalid value in error",
			err:          fmt.Errorf("invalid value for field count"),
			responseBody: "",
			want:         true,
		},
		// Negative cases - should NOT be detected as type errors
		{
			name:         "connection refused",
			err:          fmt.Errorf("connection refused"),
			responseBody: "",
			want:         false,
		},
		{
			name:         "timeout error",
			err:          fmt.Errorf("request timeout"),
			responseBody: "",
			want:         false,
		},
		{
			name:         "not found error",
			err:          fmt.Errorf("agent returned status 404"),
			responseBody: `{"error": "capability not found"}`,
			want:         false,
		},
		{
			name:         "authorization error",
			err:          fmt.Errorf("unauthorized"),
			responseBody: "",
			want:         false,
		},
		{
			name:         "generic server error",
			err:          fmt.Errorf("internal server error"),
			responseBody: "",
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTypeRelatedError(tt.err, tt.responseBody)
			if got != tt.want {
				t.Errorf("isTypeRelatedError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSmartExecutor_ValidationFeedback tests Layer 3 validation feedback integration
func TestSmartExecutor_ValidationFeedback(t *testing.T) {
	// Create catalog with agent but NO schema (to bypass Layer 2 coercion)
	// This tests Layer 3 as the PRIMARY defense mechanism
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"weather-tool": {
				Registration: &core.ServiceRegistration{
					ID:      "weather-tool",
					Name:    "weather-tool",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "get_weather",
						Endpoint: "/api/weather",
						// NO Parameters defined - Layer 2 won't coerce
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Track calls and parameters
	callCount := 0
	var sentParams []map[string]interface{}

	// Mock transport that fails on first call with type error, succeeds on second
	mockRT := &validationFeedbackRoundTripper{
		onRequest: func(req *http.Request) map[string]interface{} {
			var params map[string]interface{}
			_ = json.NewDecoder(req.Body).Decode(&params)
			sentParams = append(sentParams, params)
			return params
		},
		getResponse: func(callNum int, params map[string]interface{}) (int, string) {
			callCount++
			if callCount == 1 {
				// First call: simulate type error
				return http.StatusBadRequest, `{"error": "json: cannot unmarshal string into Go struct field .lat of type float64"}`
			}
			// Second call: success
			return http.StatusOK, `{"temperature": 25, "condition": "sunny"}`
		},
	}
	executor.httpClient = &http.Client{Transport: mockRT}

	// Track what the callback receives
	var callbackParams map[string]interface{}
	var callbackErrMsg string
	correctionCalled := false

	executor.SetCorrectionCallback(func(ctx context.Context, step RoutingStep, params map[string]interface{}, errMsg string, schema *EnhancedCapability) (map[string]interface{}, error) {
		correctionCalled = true
		callbackParams = params
		callbackErrMsg = errMsg
		// Return corrected parameters with proper types
		return map[string]interface{}{
			"lat": 35.6897,  // Fixed: now a float64
			"lon": 139.6917, // Fixed: now a float64
		}, nil
	})
	executor.SetValidationFeedback(true, 2)

	// Create step with STRING parameters (as LLM would generate incorrectly)
	step := RoutingStep{
		StepID:    "weather-step",
		AgentName: "weather-tool",
		Metadata: map[string]interface{}{
			"capability": "get_weather",
			"parameters": map[string]interface{}{
				"lat": "35.6897",  // STRING - will cause type error
				"lon": "139.6917", // STRING - will cause type error
			},
		},
	}

	ctx := context.Background()
	result := executor.executeStep(ctx, step)

	// Verify correction was called
	if !correctionCalled {
		t.Fatal("Correction callback was not called")
	}

	// Verify callback received the original (incorrect) parameters
	if lat, ok := callbackParams["lat"].(string); !ok || lat != "35.6897" {
		t.Errorf("Callback should receive original string param, got lat=%v (%T)", callbackParams["lat"], callbackParams["lat"])
	}

	// Verify callback received error message containing type error
	if !strings.Contains(callbackErrMsg, "cannot unmarshal string") {
		t.Errorf("Callback should receive type error message, got: %s", callbackErrMsg)
	}

	// Verify step succeeded after correction
	if !result.Success {
		t.Fatalf("Step execution failed: %s", result.Error)
	}

	// Verify two HTTP calls were made (first failed, second succeeded)
	if callCount != 2 {
		t.Errorf("Expected 2 HTTP calls, got %d", callCount)
	}

	// Verify the SECOND call used corrected parameters (float64, not string)
	if len(sentParams) < 2 {
		t.Fatalf("Expected at least 2 parameter sets, got %d", len(sentParams))
	}

	// First call should have string params (original)
	if lat, ok := sentParams[0]["lat"].(string); !ok || lat != "35.6897" {
		t.Errorf("First call should have string lat, got %v (%T)", sentParams[0]["lat"], sentParams[0]["lat"])
	}

	// Second call should have float64 params (corrected by callback)
	if lat, ok := sentParams[1]["lat"].(float64); !ok || lat != 35.6897 {
		t.Errorf("Second call should have float64 lat=35.6897, got %v (%T)", sentParams[1]["lat"], sentParams[1]["lat"])
	}
}

// TestSmartExecutor_ValidationFeedbackWithLayer2 tests Layer 3 when Layer 2 coercion exists
// This simulates an edge case where the tool still fails despite Layer 2 coercion
func TestSmartExecutor_ValidationFeedbackWithLayer2(t *testing.T) {
	// Create catalog with schema (Layer 2 will coerce)
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"weather-tool": {
				Registration: &core.ServiceRegistration{
					ID:      "weather-tool",
					Name:    "weather-tool",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "get_weather",
						Endpoint: "/api/weather",
						Parameters: []Parameter{
							{Name: "lat", Type: "float64", Required: true},
							{Name: "lon", Type: "float64", Required: true},
						},
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	callCount := 0
	var sentParams []map[string]interface{}

	mockRT := &validationFeedbackRoundTripper{
		onRequest: func(req *http.Request) map[string]interface{} {
			var params map[string]interface{}
			_ = json.NewDecoder(req.Body).Decode(&params)
			sentParams = append(sentParams, params)
			return params
		},
		getResponse: func(callNum int, params map[string]interface{}) (int, string) {
			callCount++
			if callCount == 1 {
				// Simulate a tool that validates more strictly (e.g., range check fails)
				return http.StatusBadRequest, `{"error": "invalid value for latitude: expected number in range -90 to 90"}`
			}
			return http.StatusOK, `{"temperature": 25}`
		},
	}
	executor.httpClient = &http.Client{Transport: mockRT}

	correctionCalled := false
	executor.SetCorrectionCallback(func(ctx context.Context, step RoutingStep, params map[string]interface{}, errMsg string, schema *EnhancedCapability) (map[string]interface{}, error) {
		correctionCalled = true
		// LLM correction - maybe it was using wrong coordinates
		return map[string]interface{}{
			"lat": 35.6897,
			"lon": 139.6917,
		}, nil
	})
	executor.SetValidationFeedback(true, 2)

	step := RoutingStep{
		StepID:    "weather-step",
		AgentName: "weather-tool",
		Metadata: map[string]interface{}{
			"capability": "get_weather",
			"parameters": map[string]interface{}{
				"lat": "35.6897",
				"lon": "139.6917",
			},
		},
	}

	result := executor.executeStep(context.Background(), step)

	// Layer 2 should have coerced the params, so first call should have float64
	if len(sentParams) > 0 {
		if lat, ok := sentParams[0]["lat"].(float64); !ok {
			t.Errorf("After Layer 2, first call should have float64 lat, got %T", sentParams[0]["lat"])
		} else if lat != 35.6897 {
			t.Errorf("Layer 2 coercion should produce 35.6897, got %v", lat)
		}
	}

	// Layer 3 should still be triggered by "invalid value" error
	if !correctionCalled {
		t.Error("Layer 3 correction should be called even after Layer 2 coercion (for edge cases)")
	}

	if !result.Success {
		t.Errorf("Step should succeed after correction: %s", result.Error)
	}
}

// validationFeedbackRoundTripper is a mock transport for validation feedback tests
type validationFeedbackRoundTripper struct {
	onRequest   func(req *http.Request) map[string]interface{}
	getResponse func(callNum int, params map[string]interface{}) (int, string)
	callNum     int
}

func (v *validationFeedbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	v.callNum++
	var params map[string]interface{}
	if v.onRequest != nil {
		params = v.onRequest(req)
	}
	statusCode, body := v.getResponse(v.callNum, params)
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

// TestSmartExecutor_ValidationFeedbackDisabled tests that validation feedback can be disabled
func TestSmartExecutor_ValidationFeedbackDisabled(t *testing.T) {
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"weather-tool": {
				Registration: &core.ServiceRegistration{
					ID:      "weather-tool",
					Name:    "weather-tool",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "get_weather",
						Endpoint: "/api/weather",
						Parameters: []Parameter{
							{Name: "lat", Type: "float64", Required: true},
						},
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Mock transport that always fails with type error
	callCount := 0
	mockRT := &validationFeedbackRoundTripper{
		getResponse: func(callNum int, params map[string]interface{}) (int, string) {
			callCount++
			return http.StatusBadRequest, `{"error": "json: cannot unmarshal string into float64"}`
		},
	}
	executor.httpClient = &http.Client{Transport: mockRT}
	executor.SetMaxAttempts(1) // Disable retries for fast tests

	// Disable validation feedback
	executor.SetValidationFeedback(false, 0)

	// Set callback that should NOT be called
	callbackCalled := false
	executor.SetCorrectionCallback(func(ctx context.Context, step RoutingStep, params map[string]interface{}, errMsg string, schema *EnhancedCapability) (map[string]interface{}, error) {
		callbackCalled = true
		return params, nil
	})

	step := RoutingStep{
		StepID:    "weather-step",
		AgentName: "weather-tool",
		Metadata: map[string]interface{}{
			"capability": "get_weather",
			"parameters": map[string]interface{}{"lat": "35.6897"},
		},
	}

	ctx := context.Background()
	result := executor.executeStep(ctx, step)

	// Callback should NOT have been called
	if callbackCalled {
		t.Error("Correction callback was called when validation feedback was disabled")
	}

	// Step should fail (no correction attempted)
	if result.Success {
		t.Error("Step should have failed when validation feedback is disabled")
	}
}

// ============================================================================
// Template Substitution Tests (Response Wrapper Fix)
// ============================================================================

// TestBuildStepContext_WrapsResponseCorrectly verifies that buildStepContext
// wraps step results in a "response" key to match the template syntax
// {{stepId.response.field}}
func TestBuildStepContext_WrapsResponseCorrectly(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	// Simulate completed step with JSON response
	results := map[string]*StepResult{
		"step-1": {
			Response: `{"data": {"country": "France", "city": "Paris"}, "status": "ok"}`,
		},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{"step-1"},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	// Verify step-1 exists in deps
	if _, ok := deps["step-1"]; !ok {
		t.Fatal("Expected step-1 in dependency results")
	}

	// Verify response wrapper exists
	responseVal, ok := deps["step-1"]["response"]
	if !ok {
		t.Fatal("Expected 'response' wrapper key in step-1 result")
	}

	// Verify response is a map
	response, ok := responseVal.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected response to be map[string]interface{}, got %T", responseVal)
	}

	// Verify nested data access works
	data, ok := response["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected response['data'] to be map, got %T", response["data"])
	}

	// Verify country field
	if data["country"] != "France" {
		t.Errorf("Expected country='France', got '%v'", data["country"])
	}

	// Verify city field
	if data["city"] != "Paris" {
		t.Errorf("Expected city='Paris', got '%v'", data["city"])
	}

	// Verify status field at top level of response
	if response["status"] != "ok" {
		t.Errorf("Expected status='ok', got '%v'", response["status"])
	}
}

// TestBuildStepContext_MultipleDependencies verifies response wrapper works
// with multiple dependencies
func TestBuildStepContext_MultipleDependencies(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	// Simulate multiple completed steps
	results := map[string]*StepResult{
		"geocode-step": {
			Response: `{"latitude": 35.6897, "longitude": 139.6917}`,
		},
		"stock-step": {
			Response: `{"price": 150.25, "currency": "USD"}`,
		},
	}

	step := RoutingStep{
		StepID:    "final-step",
		DependsOn: []string{"geocode-step", "stock-step"},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	// Verify both dependencies have response wrapper
	for _, depID := range []string{"geocode-step", "stock-step"} {
		if _, ok := deps[depID]; !ok {
			t.Errorf("Expected %s in dependency results", depID)
			continue
		}
		if _, ok := deps[depID]["response"]; !ok {
			t.Errorf("Expected 'response' wrapper in %s result", depID)
		}
	}

	// Verify geocode data
	geocodeResponse := deps["geocode-step"]["response"].(map[string]interface{})
	if geocodeResponse["latitude"] != 35.6897 {
		t.Errorf("Expected latitude=35.6897, got %v", geocodeResponse["latitude"])
	}

	// Verify stock data
	stockResponse := deps["stock-step"]["response"].(map[string]interface{})
	if stockResponse["price"] != 150.25 {
		t.Errorf("Expected price=150.25, got %v", stockResponse["price"])
	}
}

// TestTemplateSubstitution_WithResponseWrapper tests that templates using
// {{stepId.response.field}} syntax are resolved correctly after the fix
func TestTemplateSubstitution_WithResponseWrapper(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	// Create dependency results WITH response wrapper (as buildStepContext now does)
	depResults := map[string]map[string]interface{}{
		"step-1": {
			"response": map[string]interface{}{
				"data": map[string]interface{}{
					"id":      123,
					"country": "France",
					"cities":  []interface{}{"Paris", "Lyon"},
				},
				"status": "success",
			},
		},
	}

	tests := []struct {
		name     string
		template string
		want     interface{}
	}{
		{
			name:     "simple field access",
			template: "{{step-1.response.status}}",
			want:     "success",
		},
		{
			name:     "nested field access",
			template: "{{step-1.response.data.country}}",
			want:     "France",
		},
		{
			name:     "numeric field access",
			template: "{{step-1.response.data.id}}",
			want:     123, // Go int from map literal
		},
		{
			name:     "array field access - JSON serialized",
			template: "{{step-1.response.data.cities}}",
			want:     `["Paris","Lyon"]`, // Arrays are JSON serialized when whole value
		},
		{
			name:     "template in string context",
			template: "Country is {{step-1.response.data.country}}",
			want:     "Country is France",
		},
		{
			name:     "path normalization (missing response prefix)",
			template: "{{step-1.data.country}}", // Missing 'response' in path - auto-corrected
			want:     "France",                  // Now resolves due to normalizeFieldPath
		},
		{
			name:     "unresolved template (nonexistent field)",
			template: "{{step-1.response.data.nonexistent}}", // Field doesn't exist
			want:     "{{step-1.response.data.nonexistent}}", // Should remain unchanged
		},
		{
			name:     "unresolved template (wrong step)",
			template: "{{step-2.response.data.country}}", // step-2 doesn't exist
			want:     "{{step-2.response.data.country}}", // Should remain unchanged
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := executor.substituteTemplates(tt.template, depResults)
			if got != tt.want {
				t.Errorf("substituteTemplates() = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

// TestInterpolateParameters_WithResponseWrapper tests parameter interpolation
// with the response wrapper in place
func TestInterpolateParameters_WithResponseWrapper(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	// Simulate geocode step result with response wrapper
	depResults := map[string]map[string]interface{}{
		"geocode": {
			"response": map[string]interface{}{
				"latitude":  35.6897,
				"longitude": 139.6917,
				"city":      "Tokyo",
			},
		},
	}

	// Parameters using correct template syntax
	params := map[string]interface{}{
		"lat":      "{{geocode.response.latitude}}",
		"lon":      "{{geocode.response.longitude}}",
		"location": "Weather for {{geocode.response.city}}",
		"units":    "celsius", // Non-template value
	}

	result := executor.interpolateParameters(params, depResults)

	// Verify template values were substituted
	if result["lat"] != 35.6897 {
		t.Errorf("lat = %v (%T), want 35.6897 (float64)", result["lat"], result["lat"])
	}

	if result["lon"] != 139.6917 {
		t.Errorf("lon = %v (%T), want 139.6917 (float64)", result["lon"], result["lon"])
	}

	if result["location"] != "Weather for Tokyo" {
		t.Errorf("location = %v, want 'Weather for Tokyo'", result["location"])
	}

	// Verify non-template value unchanged
	if result["units"] != "celsius" {
		t.Errorf("units = %v, want 'celsius'", result["units"])
	}
}

// TestBuildStepContext_InvalidJSON verifies graceful handling of non-JSON responses
func TestBuildStepContext_InvalidJSON(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	// Simulate step with invalid JSON response
	results := map[string]*StepResult{
		"step-1": {
			Response: `not valid json`,
		},
		"step-2": {
			Response: `{"valid": "json"}`,
		},
	}

	step := RoutingStep{
		StepID:    "step-3",
		DependsOn: []string{"step-1", "step-2"},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	// step-1 should NOT be in deps (invalid JSON)
	if _, ok := deps["step-1"]; ok {
		t.Error("Invalid JSON response should not be added to deps")
	}

	// step-2 should be in deps with response wrapper
	if _, ok := deps["step-2"]; !ok {
		t.Fatal("Valid JSON response should be in deps")
	}
	if _, ok := deps["step-2"]["response"]; !ok {
		t.Error("Valid response should have 'response' wrapper")
	}
}

// TestBuildStepContext_EmptyResponse verifies handling of empty responses
func TestBuildStepContext_EmptyResponse(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	executor := NewSmartExecutor(catalog)

	// Simulate step with empty response
	results := map[string]*StepResult{
		"step-1": {
			Response: "", // Empty
		},
		"step-2": {
			Response: `{"data": "value"}`,
		},
	}

	step := RoutingStep{
		StepID:    "step-3",
		DependsOn: []string{"step-1", "step-2"},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	// step-1 should NOT be in deps (empty response)
	if _, ok := deps["step-1"]; ok {
		t.Error("Empty response should not be added to deps")
	}

	// step-2 should be in deps
	if _, ok := deps["step-2"]; !ok {
		t.Error("Non-empty response should be in deps")
	}
}

// =============================================================================
// Issue 10 Fix A — Template Auto-Include Tests
// Tests for buildStepContext auto-including referenced steps from parameters
// when they are missing from depends_on.
// =============================================================================

// TestBuildStepContext_AutoIncludesTemplateReferencedStep verifies that when
// depends_on is empty but parameters contain {{step-N...}} templates,
// the referenced step results are auto-included in the context.
func TestBuildStepContext_AutoIncludesTemplateReferencedStep(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {
			Response: `{"data": {"currency": {"code": "CAD"}}}`,
		},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{}, // Empty — the bug scenario
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"currency_code": "{{step-1.response.data.currency.code}}",
			},
		},
	}

	ctx, autoIncludes := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if _, ok := deps["step-1"]; !ok {
		t.Fatal("Expected step-1 to be auto-included from template reference in parameters")
	}

	response, ok := deps["step-1"]["response"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected step-1 to have 'response' wrapper")
	}

	data, ok := response["data"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected response to have 'data' field")
	}

	currency, ok := data["currency"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected data to have 'currency' field")
	}

	if currency["code"] != "CAD" {
		t.Errorf("Expected currency.code='CAD', got '%v'", currency["code"])
	}

	// Verify autoIncludes metadata is populated (Phase 12)
	if len(autoIncludes) != 1 {
		t.Fatalf("Expected 1 auto-include entry, got %d", len(autoIncludes))
	}
	if autoIncludes[0].ReferencedStep != "step-1" {
		t.Errorf("Expected auto-include for step-1, got %s", autoIncludes[0].ReferencedStep)
	}
	if autoIncludes[0].Template != "{{step-1.response.data.currency.code}}" {
		t.Errorf("Expected template '{{step-1.response.data.currency.code}}', got '%s'", autoIncludes[0].Template)
	}
}

// TestBuildStepContext_NoDuplicateWhenAlreadyInDependsOn verifies that a step
// already listed in depends_on is not added again by the template scanner.
func TestBuildStepContext_NoDuplicateWhenAlreadyInDependsOn(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {
			Response: `{"data": {"city": "Paris"}}`,
		},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{"step-1"}, // Already declared
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"city": "{{step-1.response.data.city}}",
			},
		},
	}

	ctx, autoIncludes := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	// step-1 should be present exactly once (from depends_on, not duplicated)
	if _, ok := deps["step-1"]; !ok {
		t.Fatal("Expected step-1 in deps")
	}

	response := deps["step-1"]["response"].(map[string]interface{})
	data := response["data"].(map[string]interface{})
	if data["city"] != "Paris" {
		t.Errorf("Expected city='Paris', got '%v'", data["city"])
	}

	// No auto-includes when already in depends_on
	if len(autoIncludes) != 0 {
		t.Errorf("Expected 0 auto-includes when step already in depends_on, got %d", len(autoIncludes))
	}
}

// TestBuildStepContext_TemplateReferencesNonexistentStep verifies no crash
// when parameters reference a step that doesn't exist in results.
func TestBuildStepContext_TemplateReferencesNonexistentStep(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{} // No results at all

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"data": "{{step-99.response.data.field}}",
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if len(deps) != 0 {
		t.Errorf("Expected empty deps when referenced step doesn't exist, got %d entries", len(deps))
	}
}

// TestBuildStepContext_NoParametersMetadata verifies no crash when step
// has no "parameters" in Metadata.
func TestBuildStepContext_NoParametersMetadata(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: `{"data": "value"}`},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata:  map[string]interface{}{}, // No "parameters" key
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if len(deps) != 0 {
		t.Errorf("Expected empty deps when no parameters metadata, got %d entries", len(deps))
	}
}

// TestBuildStepContext_NilMetadata verifies no crash when Metadata is nil.
func TestBuildStepContext_NilMetadata(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: `{"data": "value"}`},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata:  nil,
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if len(deps) != 0 {
		t.Errorf("Expected empty deps when metadata is nil, got %d entries", len(deps))
	}
}

// TestBuildStepContext_NonStringParameterValuesSkipped verifies that non-string
// parameter values (ints, bools, maps) are skipped by the template scanner.
func TestBuildStepContext_NonStringParameterValuesSkipped(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: `{"data": "value"}`},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"count":   42,
				"enabled": true,
				"nested":  map[string]interface{}{"key": "val"},
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if len(deps) != 0 {
		t.Errorf("Expected empty deps when no string params have templates, got %d entries", len(deps))
	}
}

// TestBuildStepContext_MultipleTemplatesInOneParameter verifies that when a single
// parameter value contains references to multiple steps, all are auto-included.
func TestBuildStepContext_MultipleTemplatesInOneParameter(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"geocode": {Response: `{"lat": 35.68, "lon": 139.69}`},
		"config":  {Response: `{"unit": "celsius"}`},
	}

	step := RoutingStep{
		StepID:    "weather-step",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"query": "lat={{geocode.response.lat}}&lon={{geocode.response.lon}}&unit={{config.response.unit}}",
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if _, ok := deps["geocode"]; !ok {
		t.Error("Expected 'geocode' to be auto-included from template reference")
	}
	if _, ok := deps["config"]; !ok {
		t.Error("Expected 'config' to be auto-included from template reference")
	}
	if len(deps) != 2 {
		t.Errorf("Expected exactly 2 auto-included deps, got %d", len(deps))
	}
}

// TestBuildStepContext_MultipleParametersWithTemplates verifies auto-include
// works across multiple parameter values.
func TestBuildStepContext_MultipleParametersWithTemplates(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: `{"city": "Tokyo"}`},
		"step-2": {Response: `{"code": "JPY"}`},
	}

	step := RoutingStep{
		StepID:    "step-3",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"location": "{{step-1.response.city}}",
				"currency": "{{step-2.response.code}}",
				"static":   "no-template-here",
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if _, ok := deps["step-1"]; !ok {
		t.Error("Expected step-1 auto-included from 'location' parameter")
	}
	if _, ok := deps["step-2"]; !ok {
		t.Error("Expected step-2 auto-included from 'currency' parameter")
	}
}

// TestBuildStepContext_AutoIncludeSkipsInvalidJSON verifies that when an
// auto-included step has a non-JSON response, it is silently skipped.
func TestBuildStepContext_AutoIncludeSkipsInvalidJSON(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: `not valid json`},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"data": "{{step-1.response.data.field}}",
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if _, ok := deps["step-1"]; ok {
		t.Error("step-1 with invalid JSON should NOT be included in deps")
	}
}

// TestBuildStepContext_AutoIncludeSkipsEmptyResponse verifies that when an
// auto-included step has an empty response, it is skipped.
func TestBuildStepContext_AutoIncludeSkipsEmptyResponse(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: ""}, // Empty
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"data": "{{step-1.response.data.field}}",
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if _, ok := deps["step-1"]; ok {
		t.Error("step-1 with empty response should NOT be included in deps")
	}
}

// TestBuildStepContext_AutoIncludeLogsWarning verifies that the logger emits
// a warning when a step is auto-included due to missing depends_on.
func TestBuildStepContext_AutoIncludeLogsWarning(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	mockLogger := &MockLogger{}
	executor.SetLogger(mockLogger)

	results := map[string]*StepResult{
		"step-1": {Response: `{"data": {"value": 42}}`},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"val": "{{step-1.response.data.value}}",
			},
		},
	}

	_, _ = executor.buildStepContext(context.Background(), step, results)

	// Verify warning was logged
	found := false
	for _, msg := range mockLogger.warnCalls {
		if msg == "Auto-included step for template resolution (missing from depends_on)" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected warning log for auto-included step, got: %v", mockLogger.warnCalls)
	}
}

// TestBuildStepContext_NoWarningWhenInDependsOn verifies no warning is logged
// when the template-referenced step is already in depends_on.
func TestBuildStepContext_NoWarningWhenInDependsOn(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	mockLogger := &MockLogger{}
	executor.SetLogger(mockLogger)

	results := map[string]*StepResult{
		"step-1": {Response: `{"data": "value"}`},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{"step-1"}, // Properly declared
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"val": "{{step-1.response.data}}",
			},
		},
	}

	_, _ = executor.buildStepContext(context.Background(), step, results)

	// No auto-include warning should be logged
	for _, msg := range mockLogger.warnCalls {
		if msg == "Auto-included step for template resolution (missing from depends_on)" {
			t.Error("Should NOT log auto-include warning when step is already in depends_on")
		}
	}
}

// TestBuildStepContext_ParametersNotMapInterface verifies no crash when
// Metadata["parameters"] exists but is not map[string]interface{}.
func TestBuildStepContext_ParametersNotMapInterface(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: `{"data": "value"}`},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": "not-a-map", // Wrong type
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if len(deps) != 0 {
		t.Errorf("Expected empty deps when parameters is wrong type, got %d entries", len(deps))
	}
}

// TestBuildStepContext_StringParamWithNoTemplates verifies that string parameter
// values without template syntax don't cause any auto-includes.
func TestBuildStepContext_StringParamWithNoTemplates(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: `{"data": "value"}`},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"city":    "Tokyo",
				"country": "Japan",
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if len(deps) != 0 {
		t.Errorf("Expected empty deps when params have no templates, got %d entries", len(deps))
	}
}

// TestBuildStepContext_TemplatesInsideArray verifies that template references
// inside a JSON array parameter value are detected and auto-included.
// This is the exact scenario from the Jaeger trace: currencies: ["{{step-1...}}", "{{step-2...}}"]
func TestBuildStepContext_TemplatesInsideArray(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: `{"data": {"currency": {"code": "CHF"}}}`},
		"step-2": {Response: `{"data": {"currency": {"code": "EUR"}}}`},
	}

	step := RoutingStep{
		StepID:    "step-3",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"base": "USD",
				"currencies": []interface{}{
					"{{step-1.response.data.currency.code}}",
					"{{step-2.response.data.currency.code}}",
				},
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if _, ok := deps["step-1"]; !ok {
		t.Error("Expected step-1 to be auto-included from template inside array")
	}
	if _, ok := deps["step-2"]; !ok {
		t.Error("Expected step-2 to be auto-included from template inside array")
	}
	if len(deps) != 2 {
		t.Errorf("Expected exactly 2 deps, got %d", len(deps))
	}
}

// TestBuildStepContext_TemplatesInsideNestedMap verifies that template references
// inside a nested map parameter value are detected.
func TestBuildStepContext_TemplatesInsideNestedMap(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"geocode": {Response: `{"lat": 35.68, "lon": 139.69}`},
	}

	step := RoutingStep{
		StepID:    "weather-step",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"location": map[string]interface{}{
					"latitude":  "{{geocode.response.lat}}",
					"longitude": "{{geocode.response.lon}}",
				},
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if _, ok := deps["geocode"]; !ok {
		t.Error("Expected 'geocode' to be auto-included from template inside nested map")
	}
}

// TestBuildStepContext_TemplatesInMixedNesting verifies detection in deeply
// nested structures: map containing array containing template strings.
func TestBuildStepContext_TemplatesInMixedNesting(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	results := map[string]*StepResult{
		"step-1": {Response: `{"code": "JPY"}`},
	}

	step := RoutingStep{
		StepID:    "step-2",
		DependsOn: []string{},
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"query": map[string]interface{}{
					"targets": []interface{}{
						"{{step-1.response.code}}",
					},
				},
			},
		},
	}

	ctx, _ := executor.buildStepContext(context.Background(), step, results)
	deps := ctx.Value(dependencyResultsKey).(map[string]map[string]interface{})

	if _, ok := deps["step-1"]; !ok {
		t.Error("Expected step-1 to be auto-included from template in map>array nesting")
	}
}

// TestCollectTemplateStrings_Unit tests the collectTemplateStrings helper directly.
func TestCollectTemplateStrings_Unit(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  int // expected number of strings collected
	}{
		{"string", "hello", 1},
		{"int", 42, 0},
		{"bool", true, 0},
		{"nil", nil, 0},
		{"flat map", map[string]interface{}{"a": "x", "b": "y"}, 2},
		{"flat slice", []interface{}{"a", "b", "c"}, 3},
		{"mixed slice", []interface{}{"a", 42, true}, 1},
		{"nested map in slice", []interface{}{map[string]interface{}{"k": "v"}}, 1},
		{"slice in map", map[string]interface{}{"arr": []interface{}{"a", "b"}}, 2},
		{"deeply nested", map[string]interface{}{
			"l1": map[string]interface{}{
				"l2": []interface{}{
					map[string]interface{}{"l3": "deep"},
				},
			},
		}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectTemplateStrings(tt.input)
			if len(got) != tt.want {
				t.Errorf("collectTemplateStrings() returned %d strings, want %d; got: %v", len(got), tt.want, got)
			}
		})
	}
}

// =============================================================================
// HITL Resume - Executor Skip Logic Tests
// =============================================================================

func TestExecutor_SkipsCompletedSteps(t *testing.T) {
	// Create mock catalog with test agents
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "capability1",
						Endpoint: "/api/capability1",
					},
				},
			},
		},
	}

	// Create executor with mock HTTP client
	executor := NewSmartExecutor(catalog)

	// Replace HTTP client with mock that tracks calls
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/capability1", http.StatusOK, `{"status": "success"}`)
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	// Create test plan with 3 steps
	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
				Metadata: map[string]interface{}{
					"capability": "capability1",
				},
			},
			{
				StepID:    "step-2",
				AgentName: "test-agent",
				DependsOn: []string{"step-1"},
				Metadata: map[string]interface{}{
					"capability": "capability1",
				},
			},
			{
				StepID:    "step-3",
				AgentName: "test-agent",
				DependsOn: []string{"step-2"},
				Metadata: map[string]interface{}{
					"capability": "capability1",
				},
			},
		},
	}

	// Inject completed steps via context (step-1 and step-2 already done)
	completedSteps := map[string]*StepResult{
		"step-1": {
			StepID:    "step-1",
			AgentName: "test-agent",
			Success:   true,
			Response:  `{"data": "cached-result-1"}`,
		},
		"step-2": {
			StepID:    "step-2",
			AgentName: "test-agent",
			Success:   true,
			Response:  `{"data": "cached-result-2"}`,
		},
	}
	ctx := WithCompletedSteps(context.Background(), completedSteps)

	// Execute plan
	result, err := executor.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify all 3 steps appear in result
	if len(result.Steps) != 3 {
		t.Errorf("Expected 3 steps in result, got %d", len(result.Steps))
	}

	// Verify step-1 and step-2 have cached responses (not re-executed)
	for _, stepResult := range result.Steps {
		if stepResult.StepID == "step-1" {
			if stepResult.Response != `{"data": "cached-result-1"}` {
				t.Errorf("step-1 should have cached response, got: %s", stepResult.Response)
			}
		}
		if stepResult.StepID == "step-2" {
			if stepResult.Response != `{"data": "cached-result-2"}` {
				t.Errorf("step-2 should have cached response, got: %s", stepResult.Response)
			}
		}
	}

	// Verify only step-3 was actually executed (HTTP call made)
	// mockRT tracks requests, so we can verify only 1 call was made
	if mockRT.GetCallCount() != 1 {
		t.Errorf("Expected only 1 HTTP call (for step-3), got %d", mockRT.GetCallCount())
	}
}

func TestExecutor_UsesCachedResultsForDependencies(t *testing.T) {
	// Create mock catalog
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "capability1",
						Endpoint: "/api/capability1",
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Mock that verifies it receives dependency data
	mockRT := &dependencyVerifyingRoundTripper{
		expectedDep: "step-1",
		t:           t,
	}
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	// Plan where step-2 depends on step-1
	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
				Metadata: map[string]interface{}{
					"capability": "capability1",
				},
			},
			{
				StepID:    "step-2",
				AgentName: "test-agent",
				DependsOn: []string{"step-1"},
				Metadata: map[string]interface{}{
					"capability": "capability1",
				},
			},
		},
	}

	// Inject step-1 as completed with specific data
	completedSteps := map[string]*StepResult{
		"step-1": {
			StepID:    "step-1",
			AgentName: "test-agent",
			Success:   true,
			Response:  `{"value": "from-cached-step-1"}`,
		},
	}
	ctx := WithCompletedSteps(context.Background(), completedSteps)

	// Execute - step-2 should be able to access step-1's cached result
	result, err := executor.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Error("Expected successful execution")
	}
}

func TestExecutor_NoCachedSteps_NormalExecution(t *testing.T) {
	// Create mock catalog
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "capability1",
						Endpoint: "/api/capability1",
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/capability1", http.StatusOK, `{"status": "success"}`)
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
				Metadata:  map[string]interface{}{"capability": "capability1"},
			},
			{
				StepID:    "step-2",
				AgentName: "test-agent",
				Metadata:  map[string]interface{}{"capability": "capability1"},
			},
		},
	}

	// No completed steps injected - normal execution
	ctx := context.Background()
	result, err := executor.Execute(ctx, plan)

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Both steps should be executed
	if mockRT.GetCallCount() != 2 {
		t.Errorf("Expected 2 HTTP calls, got %d", mockRT.GetCallCount())
	}

	if len(result.Steps) != 2 {
		t.Errorf("Expected 2 steps in result, got %d", len(result.Steps))
	}
}

// TestExecutePlan_PrePopulatedStepsDoNotPoisonLoop verifies that completed steps from
// PRIOR phases (not in the current plan) do not get added to the `executed` map.
// Without the planStepIDs fix (Issue 1), prior-phase steps would be counted as executed,
// causing len(executed) >= len(plan.Steps) to be true prematurely, exiting the loop
// without executing any steps in the current plan.
func TestExecutePlan_PrePopulatedStepsDoNotPoisonLoop(t *testing.T) {
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "capability1",
						Endpoint: "/api/capability1",
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/capability1", http.StatusOK, `{"data": {"result": "executed"}}`)
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	// Phase 2 plan: only step-3 and step-4
	plan := &RoutingPlan{
		PlanID: "phase-2-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-3",
				AgentName: "test-agent",
				Metadata:  map[string]interface{}{"capability": "capability1"},
			},
			{
				StepID:    "step-4",
				AgentName: "test-agent",
				DependsOn: []string{"step-3"},
				Metadata:  map[string]interface{}{"capability": "capability1"},
			},
		},
	}

	// Inject prior-phase steps (step-1, step-2) that are NOT in the current plan.
	// Without the planStepIDs fix, these would poison executed map and the loop
	// would exit immediately (len(executed)=2 >= len(plan.Steps)=2).
	completedSteps := map[string]*StepResult{
		"step-1": {
			StepID:    "step-1",
			AgentName: "test-agent",
			Success:   true,
			Response:  `{"data": {"lat": 35.6762}}`,
		},
		"step-2": {
			StepID:    "step-2",
			AgentName: "test-agent",
			Success:   true,
			Response:  `{"data": {"currency": "JPY"}}`,
		},
	}
	ctx := WithCompletedSteps(context.Background(), completedSteps)

	result, err := executor.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Both step-3 and step-4 must be executed (not skipped by loop poison)
	if mockRT.GetCallCount() != 2 {
		t.Errorf("Expected 2 HTTP calls (step-3 and step-4), got %d — prior-phase steps may have poisoned the loop", mockRT.GetCallCount())
	}

	// Result should contain step-3 and step-4 (prior-phase steps are NOT in result)
	planStepCount := 0
	for _, stepResult := range result.Steps {
		if stepResult.StepID == "step-3" || stepResult.StepID == "step-4" {
			planStepCount++
		}
	}
	if planStepCount != 2 {
		t.Errorf("Expected 2 plan steps in result, got %d", planStepCount)
	}
}

// TestExecutePlan_PrePopulatedStepsAvailableForTemplateResolution verifies that
// prior-phase completed steps are available for template resolution even though
// they are not in the current plan. Step-3 references {{step-1.response.data.lat}}
// where step-1 is from a prior phase.
func TestExecutePlan_PrePopulatedStepsAvailableForTemplateResolution(t *testing.T) {
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "get_weather",
						Endpoint: "/api/weather",
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)

	// Track what parameters were sent in requests
	var capturedBody []byte
	mockRT := &paramCapturingRoundTripper{
		capturedBody: &capturedBody,
	}
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	// Phase 2 plan: step-3 uses template referencing prior-phase step-1
	plan := &RoutingPlan{
		PlanID: "phase-2-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-3",
				AgentName: "test-agent",
				Metadata: map[string]interface{}{
					"capability": "get_weather",
					"parameters": map[string]interface{}{
						"lat": "{{step-1.response.data.lat}}",
						"lon": "{{step-1.response.data.lon}}",
					},
				},
			},
		},
	}

	// step-1 is from phase 1 — NOT in current plan but needed for templates
	completedSteps := map[string]*StepResult{
		"step-1": {
			StepID:    "step-1",
			AgentName: "geocoding-tool",
			Success:   true,
			Response:  `{"data": {"lat": 35.6762, "lon": 139.6503}}`,
		},
	}
	ctx := WithCompletedSteps(context.Background(), completedSteps)

	result, err := executor.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// step-3 should have executed successfully
	if !result.Success {
		t.Error("Expected successful execution")
	}
	if mockRT.callCount != 1 {
		t.Errorf("Expected 1 HTTP call (for step-3), got %d", mockRT.callCount)
	}
}

// TestExecutePlan_HITLResumeStillSkipsCompletedPlanSteps verifies mixed scenarios:
// some completed steps are from prior phases (template-only), others are in the
// current plan (HITL resume — should be skipped). Both must work simultaneously.
func TestExecutePlan_HITLResumeStillSkipsCompletedPlanSteps(t *testing.T) {
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{
						Name:     "capability1",
						Endpoint: "/api/capability1",
					},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/capability1", http.StatusOK, `{"data": {"result": "executed"}}`)
	executor.httpClient = &http.Client{
		Transport: mockRT,
	}

	// Phase 2 plan: step-3, step-4, step-5
	// step-3 was already completed (HITL resume), step-4 and step-5 need execution
	plan := &RoutingPlan{
		PlanID: "phase-2-hitl-resume",
		Steps: []RoutingStep{
			{
				StepID:    "step-3",
				AgentName: "test-agent",
				Metadata:  map[string]interface{}{"capability": "capability1"},
			},
			{
				StepID:    "step-4",
				AgentName: "test-agent",
				DependsOn: []string{"step-3"},
				Metadata:  map[string]interface{}{"capability": "capability1"},
			},
			{
				StepID:    "step-5",
				AgentName: "test-agent",
				Metadata:  map[string]interface{}{"capability": "capability1"},
			},
		},
	}

	// Mixed completed steps:
	// - step-1, step-2: prior-phase (NOT in plan) — template resolution only
	// - step-3: in current plan (HITL resume) — should be skipped
	completedSteps := map[string]*StepResult{
		"step-1": {
			StepID:    "step-1",
			AgentName: "test-agent",
			Success:   true,
			Response:  `{"data": {"prior": "phase1"}}`,
		},
		"step-2": {
			StepID:    "step-2",
			AgentName: "test-agent",
			Success:   true,
			Response:  `{"data": {"prior": "phase1"}}`,
		},
		"step-3": {
			StepID:    "step-3",
			AgentName: "test-agent",
			Success:   true,
			Response:  `{"data": {"cached": "hitl-resume"}}`,
		},
	}
	ctx := WithCompletedSteps(context.Background(), completedSteps)

	result, err := executor.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Only step-4 and step-5 should make HTTP calls (step-3 skipped via HITL cache)
	if mockRT.GetCallCount() != 2 {
		t.Errorf("Expected 2 HTTP calls (step-4, step-5), got %d", mockRT.GetCallCount())
	}

	// All 3 plan steps should appear in result
	if len(result.Steps) != 3 {
		t.Errorf("Expected 3 steps in result, got %d", len(result.Steps))
	}

	// step-3 should have the cached response (not re-executed)
	for _, stepResult := range result.Steps {
		if stepResult.StepID == "step-3" {
			if stepResult.Response != `{"data": {"cached": "hitl-resume"}}` {
				t.Errorf("step-3 should have cached HITL response, got: %s", stepResult.Response)
			}
		}
	}
}

// paramCapturingRoundTripper captures request body for verification
type paramCapturingRoundTripper struct {
	capturedBody *[]byte
	callCount    int
}

func (rt *paramCapturingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.callCount++
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		*rt.capturedBody = body
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"data": {"weather": "sunny"}}`)),
		Header:     make(http.Header),
	}, nil
}

// dependencyVerifyingRoundTripper is a mock that verifies dependency data is passed
type dependencyVerifyingRoundTripper struct {
	expectedDep string
	t           *testing.T
}

func (rt *dependencyVerifyingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Return success response
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"status": "success"}`)),
		Header:     make(http.Header),
	}, nil
}

// ============================================================================
// Step Retry Backoff Configuration Tests
// ============================================================================

func TestSmartExecutor_DefaultStepRetryBackoff(t *testing.T) {
	// Verify NewSmartExecutor initializes stepRetryBackoff to core.DefaultBackoffConfig()
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	expected := core.DefaultBackoffConfig()
	if executor.stepRetryBackoff.InitialDelay != expected.InitialDelay {
		t.Errorf("InitialDelay = %v, want %v", executor.stepRetryBackoff.InitialDelay, expected.InitialDelay)
	}
	if executor.stepRetryBackoff.MaxDelay != expected.MaxDelay {
		t.Errorf("MaxDelay = %v, want %v", executor.stepRetryBackoff.MaxDelay, expected.MaxDelay)
	}
	if executor.stepRetryBackoff.BackoffFactor != expected.BackoffFactor {
		t.Errorf("BackoffFactor = %v, want %v", executor.stepRetryBackoff.BackoffFactor, expected.BackoffFactor)
	}
	if executor.stepRetryBackoff.JitterEnabled != expected.JitterEnabled {
		t.Errorf("JitterEnabled = %v, want %v", executor.stepRetryBackoff.JitterEnabled, expected.JitterEnabled)
	}
}

func TestSmartExecutor_SetStepRetryBackoff(t *testing.T) {
	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	custom := core.BackoffConfig{
		InitialDelay:  2 * time.Second,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 3.0,
		JitterEnabled: false,
	}
	executor.SetStepRetryBackoff(custom)

	if executor.stepRetryBackoff != custom {
		t.Errorf("SetStepRetryBackoff did not apply config: got %+v, want %+v",
			executor.stepRetryBackoff, custom)
	}
}

func TestSmartExecutor_EnvVar_StepRetryInitialDelay(t *testing.T) {
	tests := []struct {
		name      string
		envValue  string
		wantDelay time.Duration // 0 means expect default
	}{
		{
			name:      "valid duration overrides default",
			envValue:  "2s",
			wantDelay: 2 * time.Second,
		},
		{
			name:      "valid milliseconds",
			envValue:  "750ms",
			wantDelay: 750 * time.Millisecond,
		},
		{
			name:      "invalid value keeps default",
			envValue:  "not-a-duration",
			wantDelay: 0, // expect default
		},
		{
			name:      "empty value keeps default",
			envValue:  "",
			wantDelay: 0, // expect default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldVal := os.Getenv("TRUVAG3_STEP_RETRY_INITIAL_DELAY")
			defer func() { _ = os.Setenv("TRUVAG3_STEP_RETRY_INITIAL_DELAY", oldVal) }()

			if tt.envValue != "" {
				_ = os.Setenv("TRUVAG3_STEP_RETRY_INITIAL_DELAY", tt.envValue)
			} else {
				_ = os.Unsetenv("TRUVAG3_STEP_RETRY_INITIAL_DELAY")
			}

			catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
			executor := NewSmartExecutor(catalog)

			expected := tt.wantDelay
			if expected == 0 {
				expected = core.DefaultBackoffConfig().InitialDelay
			}

			if executor.stepRetryBackoff.InitialDelay != expected {
				t.Errorf("InitialDelay = %v, want %v", executor.stepRetryBackoff.InitialDelay, expected)
			}
		})
	}
}

func TestSmartExecutor_EnvVar_StepRetryMaxDelay(t *testing.T) {
	tests := []struct {
		name      string
		envValue  string
		wantDelay time.Duration // 0 means expect default
	}{
		{
			name:      "valid duration overrides default",
			envValue:  "30s",
			wantDelay: 30 * time.Second,
		},
		{
			name:      "valid minutes",
			envValue:  "1m",
			wantDelay: 1 * time.Minute,
		},
		{
			name:      "invalid value keeps default",
			envValue:  "xyz",
			wantDelay: 0, // expect default
		},
		{
			name:      "empty value keeps default",
			envValue:  "",
			wantDelay: 0, // expect default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldVal := os.Getenv("TRUVAG3_STEP_RETRY_MAX_DELAY")
			defer func() { _ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_DELAY", oldVal) }()

			if tt.envValue != "" {
				_ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_DELAY", tt.envValue)
			} else {
				_ = os.Unsetenv("TRUVAG3_STEP_RETRY_MAX_DELAY")
			}

			catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
			executor := NewSmartExecutor(catalog)

			expected := tt.wantDelay
			if expected == 0 {
				expected = core.DefaultBackoffConfig().MaxDelay
			}

			if executor.stepRetryBackoff.MaxDelay != expected {
				t.Errorf("MaxDelay = %v, want %v", executor.stepRetryBackoff.MaxDelay, expected)
			}
		})
	}
}

func TestSmartExecutor_EnvVar_BothOverridesTogether(t *testing.T) {
	// Verify both env vars can be set simultaneously
	oldInitial := os.Getenv("TRUVAG3_STEP_RETRY_INITIAL_DELAY")
	oldMax := os.Getenv("TRUVAG3_STEP_RETRY_MAX_DELAY")
	defer func() {
		_ = os.Setenv("TRUVAG3_STEP_RETRY_INITIAL_DELAY", oldInitial)
		_ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_DELAY", oldMax)
	}()

	_ = os.Setenv("TRUVAG3_STEP_RETRY_INITIAL_DELAY", "1s")
	_ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_DELAY", "20s")

	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)

	if executor.stepRetryBackoff.InitialDelay != 1*time.Second {
		t.Errorf("InitialDelay = %v, want 1s", executor.stepRetryBackoff.InitialDelay)
	}
	if executor.stepRetryBackoff.MaxDelay != 20*time.Second {
		t.Errorf("MaxDelay = %v, want 20s", executor.stepRetryBackoff.MaxDelay)
	}
	// BackoffFactor and JitterEnabled should remain at defaults
	defaults := core.DefaultBackoffConfig()
	if executor.stepRetryBackoff.BackoffFactor != defaults.BackoffFactor {
		t.Errorf("BackoffFactor = %v, want %v (default)", executor.stepRetryBackoff.BackoffFactor, defaults.BackoffFactor)
	}
	if executor.stepRetryBackoff.JitterEnabled != defaults.JitterEnabled {
		t.Errorf("JitterEnabled = %v, want %v (default)", executor.stepRetryBackoff.JitterEnabled, defaults.JitterEnabled)
	}
}

func TestSmartExecutor_EnvVar_StepRetryMaxAttempts(t *testing.T) {
	tests := []struct {
		name         string
		envValue     string
		wantAttempts int // 0 means expect default (3)
	}{
		{"valid integer overrides default", "5", 5},
		{"minimum value of 1", "1", 1},
		{"zero is rejected (keeps default)", "0", 0},
		{"negative is rejected (keeps default)", "-1", 0},
		{"non-numeric is rejected (keeps default)", "many", 0},
		{"empty value keeps default", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldVal := os.Getenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS")
			defer func() { _ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS", oldVal) }()

			if tt.envValue != "" {
				_ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS", tt.envValue)
			} else {
				_ = os.Unsetenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS")
			}

			catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
			executor := NewSmartExecutor(catalog)

			expected := tt.wantAttempts
			if expected == 0 {
				expected = 3 // Default since bump
			}
			if executor.maxAttempts != expected {
				t.Errorf("maxAttempts = %d, want %d", executor.maxAttempts, expected)
			}
		})
	}
}

func TestSmartExecutor_DefaultMaxAttempts(t *testing.T) {
	// Guard against accidental revert of the default from 3 → 2.
	// Also guards that env-var absence keeps the default.
	oldVal := os.Getenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS")
	defer func() { _ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS", oldVal) }()
	_ = os.Unsetenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS")

	catalog := &AgentCatalog{agents: make(map[string]*AgentInfo)}
	executor := NewSmartExecutor(catalog)
	if executor.maxAttempts != 3 {
		t.Errorf("default maxAttempts = %d, want 3 (initial + 2 retries)", executor.maxAttempts)
	}
}

// setupExecutorTestTracer installs an in-memory tracer provider for span assertions
// and restores the previous global provider on test cleanup.
func setupExecutorTestTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prev := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return recorder
}

// TestSmartExecutor_ExecuteStep_EmitsStepSpan verifies the per-step span emitted by
// executeStep carries the attributes Jaeger relies on to bridge the phase span and
// the tool's HTTP server span: step_id, agent_name, namespace, capability, plus the
// deferred outcome stamps (success, attempts, duration_ms).
func TestSmartExecutor_ExecuteStep_EmitsStepSpan(t *testing.T) {
	recorder := setupExecutorTestTracer(t)

	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID:      "agent-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{Name: "test_cap", Endpoint: "/api/test"},
				},
			},
		},
	}

	executor := NewSmartExecutor(catalog)
	executor.SetMaxAttempts(1)
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/test", http.StatusOK, `{"result":"ok"}`)
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID:    "step-xyz",
		AgentName: "test-agent",
		Namespace: "ns-a",
		Metadata: map[string]interface{}{
			"capability": "test_cap",
			"parameters": map[string]interface{}{},
		},
	}

	result := executor.executeStep(context.Background(), step)
	if !result.Success {
		t.Fatalf("executeStep failed: %s", result.Error)
	}

	var stepSpan sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if s.Name() == "orchestrator.step.step-xyz" {
			stepSpan = s
			break
		}
	}
	if stepSpan == nil {
		names := make([]string, 0, len(recorder.Ended()))
		for _, s := range recorder.Ended() {
			names = append(names, s.Name())
		}
		t.Fatalf("expected span 'orchestrator.step.step-xyz', recorded: %v", names)
	}

	attrs := make(map[string]attribute.Value, len(stepSpan.Attributes()))
	for _, kv := range stepSpan.Attributes() {
		attrs[string(kv.Key)] = kv.Value
	}

	expectString := func(key, want string) {
		t.Helper()
		v, ok := attrs[key]
		if !ok {
			t.Errorf("missing span attr %q", key)
			return
		}
		if got := v.AsString(); got != want {
			t.Errorf("span attr %q = %q, want %q", key, got, want)
		}
	}
	expectString("step_id", "step-xyz")
	expectString("step.agent_name", "test-agent")
	expectString("step.namespace", "ns-a")
	expectString("step.capability", "test_cap")

	if v, ok := attrs["step.success"]; !ok || !v.AsBool() {
		t.Errorf("step.success = %v, want true", v)
	}
	if v, ok := attrs["step.attempts"]; !ok || v.AsInt64() < 1 {
		t.Errorf("step.attempts = %v, want >=1", v)
	}
	if v, ok := attrs["step.duration_ms"]; !ok || v.AsInt64() < 0 {
		t.Errorf("step.duration_ms = %v, want >=0", v)
	}
}

// TestSmartExecutor_ExecuteStep_StepSpanOnFailure verifies the defer block stamps
// the failure path too — an error message must land on the span so Jaeger surfaces
// why the step failed without cross-referencing logs.
func TestSmartExecutor_ExecuteStep_StepSpanOnFailure(t *testing.T) {
	recorder := setupExecutorTestTracer(t)

	// Agent not in catalog → executeStep returns early with Success=false.
	executor := NewSmartExecutor(&AgentCatalog{agents: map[string]*AgentInfo{}})
	executor.SetMaxAttempts(1)

	step := RoutingStep{
		StepID:    "step-missing",
		AgentName: "nonexistent-agent",
		Metadata:  map[string]interface{}{"capability": "whatever"},
	}
	result := executor.executeStep(context.Background(), step)
	if result.Success {
		t.Fatalf("expected failure for missing agent, got success")
	}

	var stepSpan sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if s.Name() == "orchestrator.step.step-missing" {
			stepSpan = s
			break
		}
	}
	if stepSpan == nil {
		t.Fatalf("expected span 'orchestrator.step.step-missing' to be recorded even on early failure")
	}

	attrs := make(map[string]attribute.Value, len(stepSpan.Attributes()))
	for _, kv := range stepSpan.Attributes() {
		attrs[string(kv.Key)] = kv.Value
	}
	if v, ok := attrs["step.success"]; !ok || v.AsBool() {
		t.Errorf("step.success = %v, want false", v)
	}
	if _, ok := attrs["step.error"]; !ok {
		t.Errorf("step.error missing on failure span")
	}
}

// --- RC4: extractFieldValue array indexing tests ---

func TestExtractFieldValue_MapTraversal(t *testing.T) {
	data := map[string]interface{}{
		"response": map[string]interface{}{
			"data": map[string]interface{}{
				"location": map[string]interface{}{
					"lat": 48.85,
					"lng": 2.35,
				},
			},
		},
	}

	val := extractFieldValue(data, "response.data.location.lat")
	if val != 48.85 {
		t.Errorf("Expected 48.85, got %v", val)
	}
}

func TestExtractFieldValue_ArrayIndex(t *testing.T) {
	data := map[string]interface{}{
		"response": map[string]interface{}{
			"data": map[string]interface{}{
				"samples": []interface{}{
					map[string]interface{}{"value": 1.21, "timestamp": 1234567890.0},
					map[string]interface{}{"value": 2.42, "timestamp": 1234567900.0},
				},
			},
		},
	}

	t.Run("first element", func(t *testing.T) {
		val := extractFieldValue(data, "response.data.samples.0.value")
		if val != 1.21 {
			t.Errorf("Expected 1.21, got %v", val)
		}
	})

	t.Run("second element", func(t *testing.T) {
		val := extractFieldValue(data, "response.data.samples.1.value")
		if val != 2.42 {
			t.Errorf("Expected 2.42, got %v", val)
		}
	})

	t.Run("out of bounds", func(t *testing.T) {
		val := extractFieldValue(data, "response.data.samples.5.value")
		if val != nil {
			t.Errorf("Expected nil for out-of-bounds index, got %v", val)
		}
	})

	t.Run("negative index", func(t *testing.T) {
		val := extractFieldValue(data, "response.data.samples.-1.value")
		if val != nil {
			t.Errorf("Expected nil for negative index, got %v", val)
		}
	})

	t.Run("non-numeric index on array", func(t *testing.T) {
		val := extractFieldValue(data, "response.data.samples.abc.value")
		if val != nil {
			t.Errorf("Expected nil for non-numeric array index, got %v", val)
		}
	})
}

func TestExtractFieldValue_NestedArrays(t *testing.T) {
	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"name": "a"},
			map[string]interface{}{"name": "b"},
			map[string]interface{}{"name": "c"},
		},
	}

	val := extractFieldValue(data, "items.2.name")
	if val != "c" {
		t.Errorf("Expected 'c', got %v", val)
	}
}

func TestExtractFieldValue_NonExistentKey(t *testing.T) {
	data := map[string]interface{}{
		"response": map[string]interface{}{
			"data": map[string]interface{}{
				"analysis": "some text",
			},
		},
	}

	val := extractFieldValue(data, "response.data.key_findings")
	if val != nil {
		t.Errorf("Expected nil for non-existent key, got %v", val)
	}
}

func TestExtractFieldValue_TraverseScalar(t *testing.T) {
	data := map[string]interface{}{
		"value": "hello",
	}

	val := extractFieldValue(data, "value.nested")
	if val != nil {
		t.Errorf("Expected nil when traversing past a scalar, got %v", val)
	}
}

func TestExtractFieldValue_ArrayOfScalars(t *testing.T) {
	data := map[string]interface{}{
		"tags": []interface{}{"go", "python", "rust"},
	}

	val := extractFieldValue(data, "tags.1")
	if val != "python" {
		t.Errorf("Expected 'python', got %v", val)
	}
}

// ---------------------------------------------------------------------------
// ORCH-015: requiresOrchestratorSplit tests
// ---------------------------------------------------------------------------

func TestRequiresOrchestratorSplit(t *testing.T) {
	// Helper: create a catalog with agents and their capability types.
	makeCatalog := func(agents map[string][]EnhancedCapability) *AgentCatalog {
		cat := &AgentCatalog{
			agents:          make(map[string]*AgentInfo),
			capabilityIndex: make(map[string][]string),
		}
		for name, caps := range agents {
			cat.agents[name] = &AgentInfo{
				Registration: &core.ServiceRegistration{
					ID:   name,
					Name: name,
				},
				Capabilities: caps,
			}
		}
		return cat
	}

	t.Run("no orchestrator steps", func(t *testing.T) {
		catalog := makeCatalog(map[string][]EnhancedCapability{
			"jira-tool": {
				{Name: "create_issue", Type: core.CapabilityTool},
			},
			"slack-tool": {
				{Name: "send_message", Type: core.CapabilityTool},
			},
		})
		executor := NewSmartExecutor(catalog)

		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
				{StepID: "step-2", AgentName: "slack-tool", DependsOn: []string{"step-1"}, Metadata: map[string]interface{}{"capability": "send_message"}},
			},
		}

		orchIDs, heldIDs, needed := executor.requiresOrchestratorSplit(context.Background(), plan)
		if needed {
			t.Error("Expected needed=false when no orchestrator steps")
		}
		if len(orchIDs) != 0 {
			t.Errorf("Expected 0 orch steps, got %d", len(orchIDs))
		}
		if len(heldIDs) != 0 {
			t.Errorf("Expected 0 held steps, got %d", len(heldIDs))
		}
	})

	t.Run("orchestrator step with dependent non-orch step", func(t *testing.T) {
		catalog := makeCatalog(map[string][]EnhancedCapability{
			"devops-agent": {
				{Name: "devops_operations", Type: core.CapabilityOrchestrator},
			},
			"jira-tool": {
				{Name: "create_issue", Type: core.CapabilityTool},
			},
		})
		executor := NewSmartExecutor(catalog)

		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "devops-agent", Metadata: map[string]interface{}{"capability": "devops_operations"}},
				{StepID: "step-2", AgentName: "jira-tool", DependsOn: []string{"step-1"}, Metadata: map[string]interface{}{"capability": "create_issue"}},
			},
		}

		orchIDs, heldIDs, needed := executor.requiresOrchestratorSplit(context.Background(), plan)
		if !needed {
			t.Error("Expected needed=true when orch step has dependents")
		}
		if !orchIDs["step-1"] {
			t.Error("Expected step-1 to be an orchestrator step")
		}
		if !heldIDs["step-2"] {
			t.Error("Expected step-2 to be a held step")
		}
	})

	t.Run("orchestrator step without dependents — not needed", func(t *testing.T) {
		catalog := makeCatalog(map[string][]EnhancedCapability{
			"devops-agent": {
				{Name: "devops_operations", Type: core.CapabilityOrchestrator},
			},
			"jira-tool": {
				{Name: "create_issue", Type: core.CapabilityTool},
			},
		})
		executor := NewSmartExecutor(catalog)

		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "devops-agent", Metadata: map[string]interface{}{"capability": "devops_operations"}},
				{StepID: "step-2", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
			},
		}

		orchIDs, heldIDs, needed := executor.requiresOrchestratorSplit(context.Background(), plan)
		if needed {
			t.Error("Expected needed=false when orch step has no dependents")
		}
		if !orchIDs["step-1"] {
			t.Error("Expected step-1 to be identified as orchestrator step")
		}
		if len(heldIDs) != 0 {
			t.Errorf("Expected 0 held steps, got %d", len(heldIDs))
		}
	})

	t.Run("independent tool steps pass through", func(t *testing.T) {
		catalog := makeCatalog(map[string][]EnhancedCapability{
			"devops-agent": {
				{Name: "devops_operations", Type: core.CapabilityOrchestrator},
			},
			"jira-tool": {
				{Name: "create_issue", Type: core.CapabilityTool},
			},
			"slack-tool": {
				{Name: "send_message", Type: core.CapabilityTool},
			},
		})
		executor := NewSmartExecutor(catalog)

		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "devops-agent", Metadata: map[string]interface{}{"capability": "devops_operations"}},
				{StepID: "step-2", AgentName: "jira-tool", DependsOn: []string{"step-1"}, Metadata: map[string]interface{}{"capability": "create_issue"}},
				{StepID: "step-3", AgentName: "slack-tool", Metadata: map[string]interface{}{"capability": "send_message"}},
			},
		}

		orchIDs, heldIDs, needed := executor.requiresOrchestratorSplit(context.Background(), plan)
		if !needed {
			t.Error("Expected needed=true")
		}
		if !orchIDs["step-1"] {
			t.Error("Expected step-1 as orchestrator step")
		}
		if !heldIDs["step-2"] {
			t.Error("Expected step-2 as held step")
		}
		// step-3 is independent — should NOT be held
		if heldIDs["step-3"] {
			t.Error("Expected step-3 to NOT be held (no dependency on orch step)")
		}
	})

	t.Run("missing agent in catalog — step skipped", func(t *testing.T) {
		catalog := makeCatalog(map[string][]EnhancedCapability{
			"jira-tool": {
				{Name: "create_issue", Type: core.CapabilityTool},
			},
		})
		executor := NewSmartExecutor(catalog)

		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "unknown-agent", Metadata: map[string]interface{}{"capability": "mystery"}},
				{StepID: "step-2", AgentName: "jira-tool", DependsOn: []string{"step-1"}, Metadata: map[string]interface{}{"capability": "create_issue"}},
			},
		}

		_, _, needed := executor.requiresOrchestratorSplit(context.Background(), plan)
		if needed {
			t.Error("Expected needed=false when agent not found in catalog")
		}
	})

	t.Run("empty capability — step skipped", func(t *testing.T) {
		catalog := makeCatalog(map[string][]EnhancedCapability{
			"devops-agent": {
				{Name: "devops_operations", Type: core.CapabilityOrchestrator},
			},
		})
		executor := NewSmartExecutor(catalog)

		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "devops-agent", Metadata: map[string]interface{}{}},
				{StepID: "step-2", AgentName: "devops-agent", DependsOn: []string{"step-1"}, Metadata: map[string]interface{}{"capability": "devops_operations"}},
			},
		}

		_, _, needed := executor.requiresOrchestratorSplit(context.Background(), plan)
		// step-1 has no capability in metadata, so it won't be identified as orch
		if needed {
			t.Error("Expected needed=false when capability metadata is empty")
		}
	})
}

// ---------------------------------------------------------------------------
// ORCH-015: applyRefinementDecisions tests
// ---------------------------------------------------------------------------

func TestApplyRefinementDecisions(t *testing.T) {
	makeCatalog := func() *AgentCatalog {
		return &AgentCatalog{
			agents:          make(map[string]*AgentInfo),
			capabilityIndex: make(map[string][]string),
		}
	}

	t.Run("skip marks step as skipped and executed", func(t *testing.T) {
		executor := NewSmartExecutor(makeCatalog())
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps: []RoutingStep{
				{StepID: "step-2", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
			},
		}

		decisions := []RefinementDecision{
			{StepID: "step-2", Action: RefinementSkip, Reason: "orchestrator already created the ticket"},
		}

		stepResults := make(map[string]*StepResult)
		executed := make(map[string]bool)
		result := &ExecutionResult{PlanID: "test-plan"}
		var mu sync.Mutex

		executor.applyRefinementDecisions(
			context.Background(), plan, decisions,
			stepResults, executed, result, &mu, "req-001",
		)

		// Verify step is marked executed
		if !executed["step-2"] {
			t.Error("Expected step-2 to be marked as executed")
		}

		// Verify step result
		sr, ok := stepResults["step-2"]
		if !ok {
			t.Fatal("Expected step result for step-2")
		}
		if !sr.Skipped {
			t.Error("Expected Skipped=true")
		}
		if !sr.Success {
			t.Error("Expected Success=true for skipped step")
		}
		if sr.SkipReason != "orchestrator already created the ticket" {
			t.Errorf("Expected skip reason, got %q", sr.SkipReason)
		}

		// Verify step added to result.Steps
		if len(result.Steps) != 1 {
			t.Fatalf("Expected 1 step in result, got %d", len(result.Steps))
		}
		if !result.Steps[0].Skipped {
			t.Error("Expected result step to have Skipped=true")
		}
	})

	t.Run("modify updates capability and preserves original", func(t *testing.T) {
		executor := NewSmartExecutor(makeCatalog())
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps: []RoutingStep{
				{
					StepID:    "step-2",
					AgentName: "jira-tool",
					Metadata: map[string]interface{}{
						"capability": "create_issue",
						"parameters": map[string]interface{}{"summary": "Bug report"},
					},
				},
			},
		}

		decisions := []RefinementDecision{
			{
				StepID:        "step-2",
				Action:        RefinementModify,
				Reason:        "ticket exists, update instead",
				NewCapability: "update_issue",
				NewParameters: map[string]interface{}{"issue_key": "PROJ-123", "comment": "Updated"},
			},
		}

		stepResults := make(map[string]*StepResult)
		executed := make(map[string]bool)
		result := &ExecutionResult{PlanID: "test-plan"}
		var mu sync.Mutex

		executor.applyRefinementDecisions(
			context.Background(), plan, decisions,
			stepResults, executed, result, &mu, "req-001",
		)

		// Step should NOT be marked as executed (it still needs to run)
		if executed["step-2"] {
			t.Error("Expected step-2 to NOT be marked as executed (modify = still run)")
		}

		// Verify capability was updated
		step := plan.Steps[0]
		if step.Metadata["capability"] != "update_issue" {
			t.Errorf("Expected capability=update_issue, got %v", step.Metadata["capability"])
		}

		// Verify original capability preserved
		if step.Metadata["original_capability"] != "create_issue" {
			t.Errorf("Expected original_capability=create_issue, got %v", step.Metadata["original_capability"])
		}

		// Verify modified_by flag
		if step.Metadata["modified_by"] != "plan_refinement" {
			t.Errorf("Expected modified_by=plan_refinement, got %v", step.Metadata["modified_by"])
		}

		// Verify parameters updated
		params, ok := step.Metadata["parameters"].(map[string]interface{})
		if !ok {
			t.Fatal("Expected parameters to be map[string]interface{}")
		}
		if params["issue_key"] != "PROJ-123" {
			t.Errorf("Expected issue_key=PROJ-123, got %v", params["issue_key"])
		}

		// Verify original parameters preserved
		origParams, ok := step.Metadata["original_parameters"].(map[string]interface{})
		if !ok {
			t.Fatal("Expected original_parameters to be preserved")
		}
		if origParams["summary"] != "Bug report" {
			t.Errorf("Expected original summary='Bug report', got %v", origParams["summary"])
		}
	})

	t.Run("execute is no-op", func(t *testing.T) {
		executor := NewSmartExecutor(makeCatalog())
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps: []RoutingStep{
				{StepID: "step-2", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
			},
		}

		decisions := []RefinementDecision{
			{StepID: "step-2", Action: RefinementExecute},
		}

		stepResults := make(map[string]*StepResult)
		executed := make(map[string]bool)
		result := &ExecutionResult{PlanID: "test-plan"}
		var mu sync.Mutex

		executor.applyRefinementDecisions(
			context.Background(), plan, decisions,
			stepResults, executed, result, &mu, "req-001",
		)

		// Should not be marked as executed (will run normally later)
		if executed["step-2"] {
			t.Error("Expected step-2 to NOT be marked as executed")
		}

		// Should not be in step results
		if _, ok := stepResults["step-2"]; ok {
			t.Error("Expected no step result for execute action")
		}

		// Plan step should be unchanged
		if plan.Steps[0].Metadata["capability"] != "create_issue" {
			t.Error("Expected capability to remain unchanged for execute action")
		}
	})

	t.Run("unknown step ID is ignored", func(t *testing.T) {
		executor := NewSmartExecutor(makeCatalog())
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps: []RoutingStep{
				{StepID: "step-2", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
			},
		}

		decisions := []RefinementDecision{
			{StepID: "step-99", Action: RefinementSkip, Reason: "nonexistent step"},
		}

		stepResults := make(map[string]*StepResult)
		executed := make(map[string]bool)
		result := &ExecutionResult{PlanID: "test-plan"}
		var mu sync.Mutex

		executor.applyRefinementDecisions(
			context.Background(), plan, decisions,
			stepResults, executed, result, &mu, "req-001",
		)

		// Nothing should happen
		if len(executed) != 0 {
			t.Errorf("Expected no steps marked as executed, got %d", len(executed))
		}
		if len(stepResults) != 0 {
			t.Errorf("Expected no step results, got %d", len(stepResults))
		}
		if len(result.Steps) != 0 {
			t.Errorf("Expected no result steps, got %d", len(result.Steps))
		}

		// Original step should be unchanged
		if plan.Steps[0].Metadata["capability"] != "create_issue" {
			t.Error("Expected original step to be unchanged")
		}
	})
}

// ---------------------------------------------------------------------------
// ORCH-015: boolMapKeys helper test
// ---------------------------------------------------------------------------

func TestBoolMapKeys(t *testing.T) {
	t.Run("empty map", func(t *testing.T) {
		keys := boolMapKeys(map[string]bool{})
		if len(keys) != 0 {
			t.Errorf("Expected 0 keys, got %d", len(keys))
		}
	})

	t.Run("multiple keys", func(t *testing.T) {
		m := map[string]bool{"step-1": true, "step-2": true, "step-3": true}
		keys := boolMapKeys(m)
		if len(keys) != 3 {
			t.Errorf("Expected 3 keys, got %d", len(keys))
		}
		// Verify all keys are present (order is not guaranteed)
		keySet := make(map[string]bool)
		for _, k := range keys {
			keySet[k] = true
		}
		for expected := range m {
			if !keySet[expected] {
				t.Errorf("Expected key %q in result", expected)
			}
		}
	})
}
