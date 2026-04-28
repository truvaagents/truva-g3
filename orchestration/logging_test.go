package orchestration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// immediateFailTransport is a mock HTTP transport that fails immediately
// instead of waiting for connection timeout. This makes tests run fast.
type immediateFailTransport struct{}

func (t *immediateFailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, errors.New("immediate fail: connection refused (mock)")
}

// TestLogger captures logs for verification in tests
type TestLogger struct {
	logs []LogEntry
	mu   sync.RWMutex
}

type LogEntry struct {
	Level   string
	Message string
	Fields  map[string]interface{}
}

func (t *TestLogger) Info(msg string, fields map[string]interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs = append(t.logs, LogEntry{Level: "INFO", Message: msg, Fields: copyFields(fields)})
}

func (t *TestLogger) Error(msg string, fields map[string]interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs = append(t.logs, LogEntry{Level: "ERROR", Message: msg, Fields: copyFields(fields)})
}

func (t *TestLogger) Warn(msg string, fields map[string]interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs = append(t.logs, LogEntry{Level: "WARN", Message: msg, Fields: copyFields(fields)})
}

func (t *TestLogger) Debug(msg string, fields map[string]interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs = append(t.logs, LogEntry{Level: "DEBUG", Message: msg, Fields: copyFields(fields)})
}

// Context-aware logging methods
func (t *TestLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	t.Info(msg, fields)
}

func (t *TestLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	t.Error(msg, fields)
}

func (t *TestLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	t.Warn(msg, fields)
}

func (t *TestLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	t.Debug(msg, fields)
}

func (t *TestLogger) GetLogs() []LogEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]LogEntry, len(t.logs))
	copy(result, t.logs)
	return result
}

func (t *TestLogger) GetLogsByLevel(level string) []LogEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var result []LogEntry
	for _, log := range t.logs {
		if log.Level == level {
			result = append(result, log)
		}
	}
	return result
}

func (t *TestLogger) GetLogsByOperation(operation string) []LogEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var result []LogEntry
	for _, log := range t.logs {
		if op, exists := log.Fields["operation"]; exists && op == operation {
			result = append(result, log)
		}
	}
	return result
}

func (t *TestLogger) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs = nil
}

func copyFields(fields map[string]interface{}) map[string]interface{} {
	if fields == nil {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range fields {
		result[k] = v
	}
	return result
}

// Mock implementations for testing - using unique names to avoid conflicts
type LoggingMockDiscovery struct {
	services []*core.ServiceInfo
	err      error
}

func (m *LoggingMockDiscovery) Register(ctx context.Context, info *core.ServiceInfo) error {
	return m.err
}

func (m *LoggingMockDiscovery) UpdateHealth(ctx context.Context, id string, status core.HealthStatus) error {
	return m.err
}

func (m *LoggingMockDiscovery) Unregister(ctx context.Context, id string) error {
	return m.err
}

func (m *LoggingMockDiscovery) Discover(ctx context.Context, filter core.DiscoveryFilter) ([]*core.ServiceInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.services, nil
}

func (m *LoggingMockDiscovery) FindService(ctx context.Context, serviceName string) ([]*core.ServiceInfo, error) {
	return m.Discover(ctx, core.DiscoveryFilter{})
}

func (m *LoggingMockDiscovery) FindByCapability(ctx context.Context, capability string) ([]*core.ServiceInfo, error) {
	return m.Discover(ctx, core.DiscoveryFilter{})
}

type LoggingMockAIClient struct {
	response *core.AIResponse
	err      error
}

func (m *LoggingMockAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.response != nil {
		return m.response, nil
	}
	return &core.AIResponse{
		Content: `{
			"plan_id": "test-plan-123",
			"original_request": "test request",
			"mode": "autonomous",
			"steps": [
				{
					"step_id": "step-1",
					"agent_name": "test-agent",
					"namespace": "default",
					"instruction": "Test instruction",
					"depends_on": [],
					"metadata": {
						"capability": "analyze",
						"parameters": {}
					}
				}
			]
		}`,
		Model: "gpt-4",
		Usage: core.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 200,
			TotalTokens:      300,
		},
	}, nil
}

// Test AIOrchestrator logging
func TestAIOrchestrator_LoggerPropagation(t *testing.T) {
	testLogger := &TestLogger{}
	mockDiscovery := &LoggingMockDiscovery{
		services: []*core.ServiceInfo{
			{
				ID:      "test-agent-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
				Health:  core.HealthHealthy,
				Capabilities: []core.Capability{
					{Name: "analyze", Description: "Test capability"},
				},
			},
		},
	}
	mockAIClient := &LoggingMockAIClient{}

	config := &OrchestratorConfig{
		RoutingMode:            "intelligent",
		CapabilityProviderType: "default",
	}

	orchestrator := NewAIOrchestrator(config, mockDiscovery, mockAIClient)
	orchestrator.SetLogger(testLogger)

	// Verify logger propagation to sub-components
	if orchestrator.logger != testLogger {
		t.Error("Logger not set on orchestrator")
	}

	// Verify executor has logger
	if orchestrator.executor.logger != testLogger {
		t.Error("Logger not propagated to executor")
	}

	// Verify catalog has logger
	if orchestrator.catalog.logger != testLogger {
		t.Error("Logger not propagated to catalog")
	}
}

func TestAIOrchestrator_ProcessRequest_SuccessLogging(t *testing.T) {
	testLogger := &TestLogger{}
	mockDiscovery := &LoggingMockDiscovery{
		services: []*core.ServiceInfo{
			{
				ID:      "test-agent-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
				Health:  core.HealthHealthy,
				Capabilities: []core.Capability{
					{Name: "analyze", Description: "Test capability"},
				},
			},
		},
	}
	mockAIClient := &LoggingMockAIClient{}

	config := &OrchestratorConfig{
		RoutingMode:            "intelligent",
		CapabilityProviderType: "default",
	}

	orchestrator := NewAIOrchestrator(config, mockDiscovery, mockAIClient)
	orchestrator.SetLogger(testLogger)

	// Inject immediate-fail transport and disable retries to avoid delays
	orchestrator.executor.httpClient = &http.Client{Transport: &immediateFailTransport{}}
	orchestrator.executor.SetMaxAttempts(1) // Disable retries for fast tests

	// Set up capability provider to avoid nil issues
	orchestrator.capabilityProvider = NewDefaultCapabilityProvider(orchestrator.catalog)

	ctx := context.Background()

	// Populate catalog first
	_ = orchestrator.catalog.Refresh(ctx)

	// Clear previous logs from refresh
	testLogger.Clear()

	// Test successful request processing
	_, err := orchestrator.ProcessRequest(ctx, "Analyze Tesla stock", map[string]interface{}{"priority": "high"})

	// Note: This will fail at execution but we should see all the logging up to that point
	logs := testLogger.GetLogs()

	// Verify INFO level logs for main operations
	infoLogs := testLogger.GetLogsByLevel("INFO")
	expectedInfoOperations := []string{
		"process_request",
		"plan_generation",
	}

	for _, expectedOp := range expectedInfoOperations {
		found := false
		for _, log := range infoLogs {
			if op, exists := log.Fields["operation"]; exists && op == expectedOp {
				found = true
				// Verify required fields are present
				if expectedOp == "process_request" {
					verifyLogFields(t, log, []string{"request_id", "request_length", "metadata_keys"})
				}
				if expectedOp == "plan_generation" {
					verifyLogFields(t, log, []string{"request_id", "plan_id", "step_count"})
				}
				break
			}
		}
		if !found {
			t.Errorf("Expected INFO log with operation '%s' not found. Got logs: %+v", expectedOp, logs)
		}
	}

	// Verify DEBUG level logs are present for detailed troubleshooting
	debugLogs := testLogger.GetLogsByLevel("DEBUG")
	if len(debugLogs) < 3 {
		t.Errorf("Expected at least 3 DEBUG logs for detailed troubleshooting, got %d", len(debugLogs))
	}

	// Verify request_id consistency across logs
	if len(infoLogs) > 0 {
		firstRequestID := infoLogs[0].Fields["request_id"]
		for _, log := range logs {
			if reqID, exists := log.Fields["request_id"]; exists {
				if reqID != firstRequestID {
					t.Errorf("Inconsistent request_id across logs: expected %v, got %v", firstRequestID, reqID)
				}
			}
		}
	}

	_ = err // We expect this to fail at execution, focus on logging
}

func TestAIOrchestrator_ProcessRequest_ErrorLogging(t *testing.T) {
	testLogger := &TestLogger{}
	mockDiscovery := &LoggingMockDiscovery{}
	mockAIClient := &LoggingMockAIClient{
		err: errors.New("AI service unavailable"),
	}

	config := &OrchestratorConfig{
		RoutingMode:            "intelligent",
		CapabilityProviderType: "default",
	}

	orchestrator := NewAIOrchestrator(config, mockDiscovery, mockAIClient)
	orchestrator.SetLogger(testLogger)
	orchestrator.capabilityProvider = NewDefaultCapabilityProvider(orchestrator.catalog)

	ctx := context.Background()
	_, err := orchestrator.ProcessRequest(ctx, "Test request", nil)

	if err == nil {
		t.Error("Expected error from failed AI client, got nil")
	}

	// Verify ERROR level logging for AI failure
	errorLogs := testLogger.GetLogsByLevel("ERROR")
	found := false
	for _, log := range errorLogs {
		if op, exists := log.Fields["operation"]; exists && op == "plan_generation" {
			found = true
			verifyLogFields(t, log, []string{"request_id", "error", "duration_ms"})

			// Verify error message contains useful information
			if errorMsg, exists := log.Fields["error"]; exists {
				if !strings.Contains(errorMsg.(string), "AI service unavailable") {
					t.Errorf("Error log should contain original error message, got: %v", errorMsg)
				}
			}
			break
		}
	}
	if !found {
		t.Error("Expected ERROR log for plan generation failure not found")
	}
}

// Test SmartExecutor logging
func TestSmartExecutor_LoggingWithoutCatalog(t *testing.T) {
	testLogger := &TestLogger{}
	executor := NewSmartExecutor(nil) // No catalog
	executor.SetLogger(testLogger)

	// Create a minimal plan
	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "nonexistent-agent",
				Instruction: "Test instruction",
			},
		},
	}

	ctx := context.Background()
	result, err := executor.Execute(ctx, plan)

	// Execution succeeds but steps fail due to missing catalog
	if err != nil {
		t.Errorf("Execute should not return error, got: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result, got nil")
	}
	if result.Success {
		t.Error("Expected execution result to indicate failure due to missing agents")
	}

	logs := testLogger.GetLogs()

	// Verify execution start logging
	debugLogs := testLogger.GetLogsByLevel("DEBUG")
	executionStartFound := false
	for _, log := range debugLogs {
		if op, exists := log.Fields["operation"]; exists && op == "execute_plan" {
			executionStartFound = true
			verifyLogFields(t, log, []string{"plan_id", "step_count", "max_concurrency"})
			break
		}
	}
	if !executionStartFound {
		t.Errorf("Expected DEBUG log for execution start not found. Got logs: %+v", logs)
	}

	// Verify completion logging
	infoLogs := testLogger.GetLogsByLevel("INFO")
	completionFound := false
	for _, log := range infoLogs {
		if op, exists := log.Fields["operation"]; exists && op == "execute_plan_complete" {
			completionFound = true
			verifyLogFields(t, log, []string{"plan_id", "success", "failed_steps", "total_steps", "duration_ms"})
			break
		}
	}
	if !completionFound {
		t.Error("Expected INFO log for execution completion not found")
	}
}

func TestSmartExecutor_StepExecutionLogging(t *testing.T) {
	testLogger := &TestLogger{}

	// Create catalog with test agent
	mockDiscovery := &LoggingMockDiscovery{
		services: []*core.ServiceInfo{
			{
				ID:      "test-agent-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
				Health:  core.HealthHealthy,
				Capabilities: []core.Capability{
					{Name: "analyze", Description: "Test capability"},
				},
			},
		},
	}

	catalog := NewAgentCatalog(mockDiscovery)
	catalog.SetLogger(testLogger)

	executor := NewSmartExecutor(catalog)
	executor.SetLogger(testLogger)

	// Inject immediate-fail transport and disable retries to avoid delays
	executor.httpClient = &http.Client{Transport: &immediateFailTransport{}}
	executor.SetMaxAttempts(1) // Disable retries for fast tests

	// Populate catalog
	ctx := context.Background()
	_ = catalog.Refresh(ctx)

	// Clear refresh logs
	testLogger.Clear()

	// Create plan with valid agent but will fail on HTTP call
	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Instruction: "Test instruction",
				Metadata: map[string]interface{}{
					"capability": "analyze",
					"parameters": map[string]interface{}{},
				},
			},
		},
	}

	_, err := executor.Execute(ctx, plan)

	// Will fail on HTTP call but should have proper logging
	logs := testLogger.GetLogs()

	// Verify step execution start
	stepStartFound := false
	debugLogs := testLogger.GetLogsByLevel("DEBUG")
	for _, log := range debugLogs {
		if op, exists := log.Fields["operation"]; exists && op == "step_execution_start" {
			stepStartFound = true
			verifyLogFields(t, log, []string{"step_id", "agent_name"})
			break
		}
	}
	if !stepStartFound {
		t.Errorf("Expected DEBUG log for step execution start not found. Got logs: %+v", logs)
	}

	// Verify agent discovery success
	agentDiscoveryFound := false
	for _, log := range debugLogs {
		if op, exists := log.Fields["operation"]; exists && op == "agent_discovery" && log.Level == "DEBUG" {
			agentDiscoveryFound = true
			verifyLogFields(t, log, []string{"step_id", "agent_name", "agent_id", "agent_address"})
			break
		}
	}
	if !agentDiscoveryFound {
		t.Error("Expected DEBUG log for successful agent discovery not found")
	}

	_ = err // Expected to fail on HTTP call
}

// Test AgentCatalog logging
func TestAgentCatalog_RefreshLogging_Success(t *testing.T) {
	testLogger := &TestLogger{}
	mockDiscovery := &LoggingMockDiscovery{
		services: []*core.ServiceInfo{
			{
				ID:      "test-agent-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
				Health:  core.HealthHealthy,
				Capabilities: []core.Capability{
					{Name: "analyze", Description: "Test capability"},
				},
			},
		},
	}

	catalog := NewAgentCatalog(mockDiscovery)
	catalog.SetLogger(testLogger)

	ctx := context.Background()
	err := catalog.Refresh(ctx)

	if err != nil {
		t.Fatalf("Unexpected error from catalog refresh: %v", err)
	}

	logs := testLogger.GetLogs()

	// Verify refresh start logging
	refreshStartLogs := testLogger.GetLogsByOperation("catalog_refresh_start")
	if len(refreshStartLogs) != 1 {
		t.Errorf("Expected 1 catalog refresh start log, got %d", len(refreshStartLogs))
	} else {
		verifyLogFields(t, refreshStartLogs[0], []string{"current_agents"})
	}

	// Verify refresh completion logging
	refreshCompleteLogs := testLogger.GetLogsByOperation("catalog_refresh_complete")
	if len(refreshCompleteLogs) != 1 {
		t.Errorf("Expected 1 catalog refresh complete log, got %d", len(refreshCompleteLogs))
	} else {
		verifyLogFields(t, refreshCompleteLogs[0], []string{
			"success", "total_duration_ms", "successful_fetches",
			"failed_fetches", "final_agent_count", "agent_count_change",
		})

		// Verify success field is true
		if success, exists := refreshCompleteLogs[0].Fields["success"]; !exists || success != true {
			t.Errorf("Expected success=true in refresh complete log, got: %v", success)
		}
	}

	// Verify discovery success debugging
	discoveryLogs := testLogger.GetLogsByOperation("discovery_query")
	discoverySuccessFound := false
	for _, log := range discoveryLogs {
		if log.Level == "DEBUG" {
			discoverySuccessFound = true
			verifyLogFields(t, log, []string{"services_found", "query_time_ms"})
			break
		}
	}
	if !discoverySuccessFound {
		t.Errorf("Expected DEBUG log for discovery success not found. Got logs: %+v", logs)
	}

	// Verify agent fetch attempts
	fetchLogs := testLogger.GetLogsByOperation("fetch_agent_info")
	if len(fetchLogs) < 1 {
		t.Error("Expected at least 1 agent fetch log")
	}
}

func TestAgentCatalog_RefreshLogging_DiscoveryFailure(t *testing.T) {
	testLogger := &TestLogger{}
	mockDiscovery := &LoggingMockDiscovery{
		err: errors.New("discovery service unavailable"),
	}

	catalog := NewAgentCatalog(mockDiscovery)
	catalog.SetLogger(testLogger)

	ctx := context.Background()
	err := catalog.Refresh(ctx)

	if err == nil {
		t.Error("Expected error from discovery failure, got nil")
	}

	// Verify error logging
	errorLogs := testLogger.GetLogsByLevel("ERROR")
	discoveryErrorFound := false
	for _, log := range errorLogs {
		if op, exists := log.Fields["operation"]; exists && op == "discovery_query" {
			discoveryErrorFound = true
			verifyLogFields(t, log, []string{"error", "duration_ms"})

			// Verify error contains useful information
			if errorMsg, exists := log.Fields["error"]; exists {
				if !strings.Contains(errorMsg.(string), "discovery service unavailable") {
					t.Errorf("Error log should contain original error message, got: %v", errorMsg)
				}
			}
			break
		}
	}
	if !discoveryErrorFound {
		t.Error("Expected ERROR log for discovery failure not found")
	}
}

// Test logging performance impact
func TestLogging_PerformanceImpact(t *testing.T) {
	testLogger := &TestLogger{}
	mockDiscovery := &LoggingMockDiscovery{
		services: []*core.ServiceInfo{
			{
				ID:      "test-agent-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
				Health:  core.HealthHealthy,
				Capabilities: []core.Capability{
					{Name: "analyze", Description: "Test capability"},
				},
			},
		},
	}

	catalog1 := NewAgentCatalog(mockDiscovery) // Without logger
	catalog2 := NewAgentCatalog(mockDiscovery) // With logger
	catalog2.SetLogger(testLogger)

	ctx := context.Background()

	// Measure without logging
	start1 := time.Now()
	for i := 0; i < 10; i++ {
		_ = catalog1.Refresh(ctx)
	}
	duration1 := time.Since(start1)

	// Measure with logging
	start2 := time.Now()
	for i := 0; i < 10; i++ {
		_ = catalog2.Refresh(ctx)
	}
	duration2 := time.Since(start2)

	// Logging overhead should be reasonable (less than 100% increase)
	overhead := float64(duration2-duration1) / float64(duration1) * 100
	if overhead > 100 {
		t.Errorf("Logging overhead too high: %.2f%% (should be < 100%%)", overhead)
	}

	t.Logf("Performance: without logging %v, with logging %v (%.2f%% overhead)",
		duration1, duration2, overhead)
}

// Test backward compatibility - components work without logger
func TestLogging_BackwardCompatibility(t *testing.T) {
	mockDiscovery := &LoggingMockDiscovery{
		services: []*core.ServiceInfo{
			{
				ID:      "test-agent-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
				Health:  core.HealthHealthy,
				Capabilities: []core.Capability{
					{Name: "analyze", Description: "Test capability"},
				},
			},
		},
	}
	mockAIClient := &LoggingMockAIClient{}

	// Test orchestrator without logger
	config := &OrchestratorConfig{
		RoutingMode:            "intelligent",
		CapabilityProviderType: "default",
	}

	orchestrator := NewAIOrchestrator(config, mockDiscovery, mockAIClient)
	// Don't set logger - should use NoOpLogger

	// Verify logger defaults to nil (components handle nil gracefully)
	if orchestrator.logger != nil {
		t.Error("Orchestrator logger should default to nil until SetLogger is called")
	}

	// Should work without errors
	ctx := context.Background()
	err := orchestrator.catalog.Refresh(ctx)
	if err != nil {
		t.Errorf("Catalog refresh should work without logger, got error: %v", err)
	}

	// Test that components handle nil logger gracefully
	// Executor
	executor := NewSmartExecutor(orchestrator.catalog)
	if executor.logger != nil {
		t.Error("Executor logger should default to nil until SetLogger is called")
	}

	// Catalog
	catalog := NewAgentCatalog(mockDiscovery)
	if catalog.logger != nil {
		t.Error("Catalog logger should default to nil until SetLogger is called")
	}

	err = catalog.Refresh(ctx)
	if err != nil {
		t.Errorf("Catalog refresh should work without logger, got error: %v", err)
	}
}

// Test log level filtering behavior
func TestLogging_LevelFiltering(t *testing.T) {
	testLogger := &TestLogger{}
	mockDiscovery := &LoggingMockDiscovery{
		services: []*core.ServiceInfo{
			{
				ID:      "test-agent-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
				Health:  core.HealthHealthy,
				Capabilities: []core.Capability{
					{Name: "analyze", Description: "Test capability"},
				},
			},
		},
	}

	catalog := NewAgentCatalog(mockDiscovery)
	catalog.SetLogger(testLogger)

	ctx := context.Background()
	_ = catalog.Refresh(ctx)

	logs := testLogger.GetLogs()

	// Verify we have appropriate distribution of log levels
	infoLogs := testLogger.GetLogsByLevel("INFO")
	debugLogs := testLogger.GetLogsByLevel("DEBUG")
	warnLogs := testLogger.GetLogsByLevel("WARN")
	errorLogs := testLogger.GetLogsByLevel("ERROR")

	// Should have INFO logs for important operations
	if len(infoLogs) < 2 {
		t.Errorf("Expected at least 2 INFO logs, got %d", len(infoLogs))
	}

	// Should have DEBUG logs for detailed information
	if len(debugLogs) < 3 {
		t.Errorf("Expected at least 3 DEBUG logs, got %d", len(debugLogs))
	}

	// WARN logs are only emitted when HTTP fallbacks actually occur
	// In this test scenario, the mock discovery succeeds without fallbacks,
	// so no WARN logs are expected (0 is acceptable)
	_ = warnLogs // WARN logs may or may not be present depending on fallback behavior

	// No ERROR logs in successful case
	if len(errorLogs) > 0 {
		t.Errorf("Expected no ERROR logs in success case, got %d", len(errorLogs))
	}

	t.Logf("Log distribution: INFO=%d, DEBUG=%d, WARN=%d, ERROR=%d, TOTAL=%d",
		len(infoLogs), len(debugLogs), len(warnLogs), len(errorLogs), len(logs))
}

// Helper function to verify required fields are present in log entry
func verifyLogFields(t *testing.T, log LogEntry, requiredFields []string) {
	for _, field := range requiredFields {
		if _, exists := log.Fields[field]; !exists {
			t.Errorf("Required field '%s' missing from log entry. Fields: %+v", field, log.Fields)
		}
	}
}

// Test that proper operations are logged for each log level
func TestLogging_OperationCoverage(t *testing.T) {
	testLogger := &TestLogger{}
	mockDiscovery := &LoggingMockDiscovery{
		services: []*core.ServiceInfo{
			{
				ID:      "test-agent-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
				Health:  core.HealthHealthy,
				Capabilities: []core.Capability{
					{Name: "analyze", Description: "Test capability"},
				},
			},
		},
	}
	mockAIClient := &LoggingMockAIClient{}

	config := &OrchestratorConfig{
		RoutingMode:            "intelligent",
		CapabilityProviderType: "default",
	}

	orchestrator := NewAIOrchestrator(config, mockDiscovery, mockAIClient)
	orchestrator.SetLogger(testLogger)

	// Inject immediate-fail transport and disable retries to avoid delays
	orchestrator.executor.httpClient = &http.Client{Transport: &immediateFailTransport{}}
	orchestrator.executor.SetMaxAttempts(1) // Disable retries for fast tests

	ctx := context.Background()
	_ = orchestrator.catalog.Refresh(ctx)
	testLogger.Clear() // Clear catalog refresh logs

	// Attempt orchestration (will fail at execution but should log planning)
	_, _ = orchestrator.ProcessRequest(ctx, "Test request", nil)

	logs := testLogger.GetLogs()

	// Verify coverage of key operations at INFO level
	infoOperations := []string{
		"process_request",
		"plan_generation",
	}

	for _, op := range infoOperations {
		found := false
		infoLogs := testLogger.GetLogsByLevel("INFO")
		for _, log := range infoLogs {
			if operation, exists := log.Fields["operation"]; exists && operation == op {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected INFO operation '%s' not found. Available logs: %+v", op, logs)
		}
	}

	// Verify coverage of key operations at DEBUG level
	debugOperations := []string{
		"plan_generation_start",
		"prompt_construction",
		"llm_call",
	}

	for _, op := range debugOperations {
		found := false
		debugLogs := testLogger.GetLogsByLevel("DEBUG")
		for _, log := range debugLogs {
			if operation, exists := log.Fields["operation"]; exists && operation == op {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected DEBUG operation '%s' not found", op)
		}
	}
}
