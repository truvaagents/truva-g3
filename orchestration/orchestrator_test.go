package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// findJSONEnd finds the end of JSON in a string (simple version, doesn't handle strings).
// This is a test helper for testing basic JSON bracket matching.
func findJSONEnd(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// MockAIClient implements core.AIClient for testing
type MockAIClient struct {
	responses map[string]string
	calls     []string
}

func NewMockAIClient() *MockAIClient {
	return &MockAIClient{
		responses: make(map[string]string),
		calls:     []string{},
	}
}

func (m *MockAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	m.calls = append(m.calls, prompt)

	// Return predefined responses based on prompt content
	if strings.Contains(prompt, "Create an execution plan") {
		return &core.AIResponse{
			Content: m.getPlanResponse(),
			Usage: core.TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		}, nil
	}

	if strings.Contains(prompt, "Synthesize") {
		return &core.AIResponse{
			Content: "This is a synthesized response combining all agent outputs.",
		}, nil
	}

	return &core.AIResponse{
		Content: "Default response",
	}, nil
}

func (m *MockAIClient) getPlanResponse() string {
	plan := map[string]interface{}{
		"plan_id":          "test-plan-1",
		"original_request": "test request",
		"mode":             "autonomous",
		"steps": []map[string]interface{}{
			{
				"step_id":     "step-1",
				"agent_name":  "stock-analyzer",
				"namespace":   "default",
				"instruction": "Analyze stock",
				"depends_on":  []string{},
				"metadata": map[string]interface{}{
					"capability": "analyze_stock",
					"parameters": map[string]interface{}{
						"symbol": "AAPL",
					},
				},
			},
		},
	}

	jsonBytes, _ := json.Marshal(plan)
	return string(jsonBytes)
}

func TestAIOrchestrator_ProcessRequest(t *testing.T) {
	// Setup mocks
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	// Register test agent
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "stock-1",
		Name:         "stock-analyzer",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "analyze_stock"}},
	})

	// Create orchestrator
	config := DefaultConfig()
	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Setup catalog with test data
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"stock-1": {
			Registration: &core.ServiceRegistration{
				ID:           "stock-1",
				Name:         "stock-analyzer",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "analyze_stock"}},
			},
			Capabilities: []EnhancedCapability{
				{Name: "analyze_stock", Description: "Analyzes stocks"},
			},
		},
	}

	// Replace executor with properly initialized one
	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

	ctx := context.Background()

	// Test request processing
	response, err := orchestrator.ProcessRequest(ctx, "Analyze Apple stock", nil)

	if err != nil {
		t.Errorf("ProcessRequest failed: %v", err)
	}

	if response == nil {
		t.Fatal("Expected response, got nil")
	}

	if response.Response == "" {
		t.Error("Expected non-empty response")
	}

	// Verify AI client was called
	if len(aiClient.calls) < 2 {
		t.Error("Expected at least 2 AI calls (planning + synthesis)")
	}
}

func TestAIOrchestrator_ValidatePlan(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	// Register test agents
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "agent-1",
		Name:         "test-agent",
		Capabilities: []core.Capability{{Name: "capability1"}},
	})

	orchestrator := NewAIOrchestrator(DefaultConfig(), discovery, aiClient)

	// Setup catalog
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"agent-1": {
			Registration: &core.ServiceRegistration{
				ID:   "agent-1",
				Name: "test-agent",
			},
			Capabilities: []EnhancedCapability{
				{Name: "capability1"},
			},
		},
	}

	// Test valid plan
	validPlan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
				Metadata: map[string]interface{}{
					"capability": "capability1",
				},
			},
		},
	}

	err := orchestrator.validatePlan(validPlan)
	if err != nil {
		t.Errorf("Valid plan validation failed: %v", err)
	}

	// Test invalid plan - non-existent agent
	invalidPlan1 := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "non-existent-agent",
			},
		},
	}

	err = orchestrator.validatePlan(invalidPlan1)
	if err == nil {
		t.Error("Expected validation to fail for non-existent agent")
	}

	// Test invalid plan - non-existent capability
	invalidPlan2 := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
				Metadata: map[string]interface{}{
					"capability": "non-existent-capability",
				},
			},
		},
	}

	err = orchestrator.validatePlan(invalidPlan2)
	if err == nil {
		t.Error("Expected validation to fail for non-existent capability")
	}

	// Test invalid plan - missing dependency
	invalidPlan3 := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "test-agent",
				DependsOn: []string{"non-existent-step"},
			},
		},
	}

	err = orchestrator.validatePlan(invalidPlan3)
	if err == nil {
		t.Error("Expected validation to fail for missing dependency")
	}
}

func TestAIOrchestrator_ParsePlan(t *testing.T) {
	orchestrator := &AIOrchestrator{}

	// Test valid JSON
	validJSON := `{
		"plan_id": "test-123",
		"original_request": "test request",
		"mode": "autonomous",
		"steps": [
			{
				"step_id": "step-1",
				"agent_name": "agent1",
				"namespace": "default",
				"instruction": "do something"
			}
		]
	}`

	plan, err := orchestrator.parsePlan(validJSON)
	if err != nil {
		t.Errorf("Failed to parse valid JSON: %v", err)
	}

	if plan.PlanID != "test-123" {
		t.Errorf("Expected plan_id 'test-123', got %s", plan.PlanID)
	}

	if len(plan.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(plan.Steps))
	}

	// Test JSON with markdown
	jsonWithMarkdown := fmt.Sprintf("```json\n%s\n```", validJSON)
	_, err = orchestrator.parsePlan(jsonWithMarkdown)
	if err != nil {
		t.Errorf("Failed to parse JSON with markdown: %v", err)
	}

	// Test invalid JSON
	invalidJSON := "not json at all"
	_, err = orchestrator.parsePlan(invalidJSON)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestAIOrchestrator_Metrics(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()
	orchestrator := NewAIOrchestrator(DefaultConfig(), discovery, aiClient)

	// Initial metrics should be zero
	metrics := orchestrator.GetMetrics()
	if metrics.TotalRequests != 0 {
		t.Errorf("Expected 0 total requests, got %d", metrics.TotalRequests)
	}

	// Update metrics
	orchestrator.updateMetrics(100*time.Millisecond, true)
	orchestrator.updateMetrics(200*time.Millisecond, false)

	metrics = orchestrator.GetMetrics()
	if metrics.TotalRequests != 2 {
		t.Errorf("Expected 2 total requests, got %d", metrics.TotalRequests)
	}
	if metrics.SuccessfulRequests != 1 {
		t.Errorf("Expected 1 successful request, got %d", metrics.SuccessfulRequests)
	}
	if metrics.FailedRequests != 1 {
		t.Errorf("Expected 1 failed request, got %d", metrics.FailedRequests)
	}
}

func TestAIOrchestrator_History(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	config := DefaultConfig()
	config.HistorySize = 2 // Small size for testing

	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Add history entries
	for i := 0; i < 3; i++ {
		response := &OrchestratorResponse{
			RequestID:       fmt.Sprintf("req-%d", i),
			OriginalRequest: fmt.Sprintf("request %d", i),
			Response:        fmt.Sprintf("response %d", i),
			ExecutionTime:   time.Duration(i) * time.Second,
		}
		orchestrator.addToHistory(response)
	}

	// Check history size is limited
	history := orchestrator.GetExecutionHistory()
	if len(history) != 2 {
		t.Errorf("Expected history size 2, got %d", len(history))
	}

	// Verify oldest entry was removed
	if history[0].Request != "request 1" {
		t.Errorf("Expected oldest entry to be 'request 1', got %s", history[0].Request)
	}
}

func TestAIOrchestrator_ExtractAgentsFromPlan(t *testing.T) {
	orchestrator := &AIOrchestrator{}

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{AgentName: "agent1"},
			{AgentName: "agent2"},
			{AgentName: "agent1"}, // Duplicate
			{AgentName: "agent3"},
		},
	}

	agents := orchestrator.extractAgentsFromPlan(plan)

	// Should have 3 unique agents
	if len(agents) != 3 {
		t.Errorf("Expected 3 unique agents, got %d", len(agents))
	}

	// Check all agents are present
	agentMap := make(map[string]bool)
	for _, agent := range agents {
		agentMap[agent] = true
	}

	for _, expected := range []string{"agent1", "agent2", "agent3"} {
		if !agentMap[expected] {
			t.Errorf("Expected agent %s not found", expected)
		}
	}
}

func TestFindJSONFunctions(t *testing.T) {
	// Test findJSONStart
	cases := []struct {
		input    string
		expected int
	}{
		{"{}", 0},
		{"text before {}", 12},
		{"no json here", -1},
		{"   {  }", 3},
	}

	for _, tc := range cases {
		result := findJSONStart(tc.input)
		if result != tc.expected {
			t.Errorf("findJSONStart(%q) = %d, expected %d", tc.input, result, tc.expected)
		}
	}

	// Test findJSONEnd
	endCases := []struct {
		input    string
		start    int
		expected int
	}{
		{"{}", 0, 2},
		{`{"nested": {}}`, 0, 14},
		{`{"a": 1, "b": 2}`, 0, 16},
		{`{"incomplete": `, 0, -1},
	}

	for _, tc := range endCases {
		result := findJSONEnd(tc.input, tc.start)
		if result != tc.expected {
			t.Errorf("findJSONEnd(%q, %d) = %d, expected %d", tc.input, tc.start, result, tc.expected)
		}
	}
}

// ============================================================================
// Layer 3: extractJSON and requestParameterCorrection Tests
// ============================================================================

// TestExtractJSON tests the extractJSON helper function for Layer 3
func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain JSON object",
			input:    `{"lat": 35.6897, "lon": 139.6917}`,
			expected: `{"lat": 35.6897, "lon": 139.6917}`,
		},
		{
			name:     "JSON with whitespace",
			input:    `   {"lat": 35.6897}   `,
			expected: `{"lat": 35.6897}`,
		},
		{
			name:     "JSON wrapped in markdown json code block",
			input:    "```json\n{\"lat\": 35.6897}\n```",
			expected: `{"lat": 35.6897}`,
		},
		{
			name:     "JSON wrapped in plain markdown code block",
			input:    "```\n{\"lat\": 35.6897}\n```",
			expected: `{"lat": 35.6897}`,
		},
		{
			name:     "JSON with markdown and extra whitespace",
			input:    "```json\n  {\"lat\": 35.6897, \"lon\": 139.6917}  \n```",
			expected: `{"lat": 35.6897, "lon": 139.6917}`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace only",
			input:    "   \n\t  ",
			expected: "",
		},
		{
			name:     "multiline JSON in code block",
			input:    "```json\n{\n  \"lat\": 35.6897,\n  \"lon\": 139.6917\n}\n```",
			expected: "{\n  \"lat\": 35.6897,\n  \"lon\": 139.6917\n}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractJSON(tt.input)
			if result != tt.expected {
				t.Errorf("extractJSON() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestRequestParameterCorrection tests the requestParameterCorrection method
func TestRequestParameterCorrection(t *testing.T) {
	// Create a mock AI client
	mockAIClient := &mockAIClientForCorrection{
		response: `{"lat": 35.6897, "lon": 139.6917}`,
	}

	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	config := DefaultConfig()

	orchestrator := NewAIOrchestrator(config, nil, mockAIClient)
	orchestrator.catalog = catalog

	step := RoutingStep{
		StepID:    "test-step",
		AgentName: "weather-tool",
		Metadata: map[string]interface{}{
			"capability": "get_weather",
		},
	}

	originalParams := map[string]interface{}{
		"lat": "35.6897",  // String - incorrect
		"lon": "139.6917", // String - incorrect
	}

	schema := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "float64", Required: true},
			{Name: "lon", Type: "float64", Required: true},
		},
	}

	ctx := context.Background()
	corrected, err := orchestrator.requestParameterCorrection(ctx, step, originalParams, "type error", schema)

	if err != nil {
		t.Fatalf("requestParameterCorrection failed: %v", err)
	}

	// Verify corrected params have proper types
	if lat, ok := corrected["lat"].(float64); !ok {
		t.Errorf("Expected lat to be float64, got %T", corrected["lat"])
	} else if lat != 35.6897 {
		t.Errorf("Expected lat=35.6897, got %v", lat)
	}

	// Verify the AI client was called with appropriate prompt
	if !mockAIClient.called {
		t.Error("AI client should have been called")
	}

	if !strings.Contains(mockAIClient.lastPrompt, "type error") {
		t.Error("Prompt should contain error message")
	}

	if !strings.Contains(mockAIClient.lastPrompt, "lat") {
		t.Error("Prompt should contain parameter names")
	}
}

// TestRequestParameterCorrectionNoAIClient tests error handling when AI client is nil
func TestRequestParameterCorrectionNoAIClient(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	config := DefaultConfig()
	config.ExecutionOptions.ValidationFeedbackEnabled = false // Don't wire up callback

	orchestrator := NewAIOrchestrator(config, nil, nil) // nil AI client
	orchestrator.catalog = catalog

	step := RoutingStep{StepID: "test"}
	params := map[string]interface{}{"lat": "35.6897"}
	schema := &EnhancedCapability{}

	_, err := orchestrator.requestParameterCorrection(context.Background(), step, params, "error", schema)

	if err == nil {
		t.Error("Expected error when AI client is nil")
	}
	if !strings.Contains(err.Error(), "AI client not available") {
		t.Errorf("Expected 'AI client not available' error, got: %v", err)
	}
}

// TestRequestParameterCorrectionInvalidJSON tests error handling for invalid LLM response
func TestRequestParameterCorrectionInvalidJSON(t *testing.T) {
	mockAIClient := &mockAIClientForCorrection{
		response: "this is not valid JSON",
	}

	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
	}
	config := DefaultConfig()

	orchestrator := NewAIOrchestrator(config, nil, mockAIClient)
	orchestrator.catalog = catalog

	step := RoutingStep{StepID: "test", Metadata: map[string]interface{}{"capability": "test"}}
	params := map[string]interface{}{"lat": "35.6897"}
	schema := &EnhancedCapability{}

	_, err := orchestrator.requestParameterCorrection(context.Background(), step, params, "error", schema)

	if err == nil {
		t.Error("Expected error for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("Expected 'failed to parse' error, got: %v", err)
	}
}

// mockAIClientForCorrection is a mock AI client for testing Layer 3 correction
type mockAIClientForCorrection struct {
	response   string
	called     bool
	lastPrompt string
	shouldFail bool
	failError  error
}

func (m *mockAIClientForCorrection) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.called = true
	m.lastPrompt = prompt
	if m.shouldFail {
		return nil, m.failError
	}
	return &core.AIResponse{Content: m.response}, nil
}

// =============================================================================
// Plan Parse Retry Tests
// =============================================================================

// TestAIOrchestrator_BuildPlanningPromptWithParseError tests the error feedback prompt
func TestAIOrchestrator_BuildPlanningPromptWithParseError(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()
	config := DefaultConfig()

	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Set up a default prompt builder to avoid nil pointer
	builder, _ := NewDefaultPromptBuilder(nil)
	orchestrator.SetPromptBuilder(builder)

	// Create a sample parse error
	parseErr := fmt.Errorf("invalid character '*' after object key:value pair")

	// Create a previous prompt result to reuse (simulates first planning attempt)
	previousPromptResult := &PlanningPromptResult{
		Prompt:        "Generate an execution plan for: test request",
		AllowedAgents: map[string]bool{"agent-1": true, "agent-2": true},
	}

	promptResult, err := orchestrator.buildPlanningPromptWithParseError(context.Background(), previousPromptResult, parseErr)

	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify the prompt contains error feedback
	if !strings.Contains(promptResult.Prompt, "<parse_error>") {
		t.Error("Expected error feedback header in prompt")
	}

	if !strings.Contains(promptResult.Prompt, "invalid character '*'") {
		t.Error("Expected parse error message in prompt")
	}

	if !strings.Contains(promptResult.Prompt, "All values are literals") {
		t.Error("Expected arithmetic expression warning in prompt")
	}

	if !strings.Contains(promptResult.Prompt, "plain text only") {
		t.Error("Expected markdown formatting warning in prompt")
	}

	if !strings.Contains(promptResult.Prompt, "Omit trailing commas") {
		t.Error("Expected trailing comma warning in prompt")
	}

	// Verify template quoting hint is included (ORCH-004)
	if !strings.Contains(promptResult.Prompt, "quoted strings") {
		t.Error("Expected template quoting hint in prompt")
	}

	// Verify AllowedAgents is preserved from previous result (no fresh tiered selection)
	if len(promptResult.AllowedAgents) != 2 {
		t.Errorf("Expected 2 AllowedAgents (preserved), got %d", len(promptResult.AllowedAgents))
	}
	if !promptResult.AllowedAgents["agent-1"] || !promptResult.AllowedAgents["agent-2"] {
		t.Error("Expected AllowedAgents to be preserved from previous prompt result")
	}

	// Verify the original prompt is still included
	if !strings.Contains(promptResult.Prompt, "Generate an execution plan for: test request") {
		t.Error("Expected original prompt to be preserved in retry prompt")
	}
}

// TestAIOrchestrator_GenerateExecutionPlan_RetryDisabled tests behavior when retry is disabled
func TestAIOrchestrator_GenerateExecutionPlan_RetryDisabled(t *testing.T) {
	discovery := NewMockDiscovery()

	// Mock AI client that returns invalid JSON
	mockAI := &mockAIClientForRetry{
		responses: []string{
			"this is not valid JSON",
		},
	}

	config := DefaultConfig()
	config.PlanParseRetryEnabled = false // Disable retry

	orchestrator := NewAIOrchestrator(config, discovery, mockAI)

	// Set up a default prompt builder
	builder, _ := NewDefaultPromptBuilder(nil)
	orchestrator.SetPromptBuilder(builder)

	_, err := orchestrator.generateExecutionPlan(context.Background(), "test request", "test-123")

	if err == nil {
		t.Fatal("Expected error when parsing invalid JSON")
	}

	// Should have only made 1 call (no retry)
	if mockAI.callCount != 1 {
		t.Errorf("Expected 1 call (no retry), got %d", mockAI.callCount)
	}
}

// TestAIOrchestrator_GenerateExecutionPlan_RetrySuccess tests successful retry
func TestAIOrchestrator_GenerateExecutionPlan_RetrySuccess(t *testing.T) {
	discovery := NewMockDiscovery()

	// Mock AI client that fails first, then succeeds
	validJSON := `{
		"plan_id": "test-123",
		"original_request": "test request",
		"mode": "autonomous",
		"steps": [
			{
				"step_id": "step-1",
				"agent_name": "test-agent",
				"namespace": "default",
				"instruction": "do something"
			}
		]
	}`

	mockAI := &mockAIClientForRetry{
		responses: []string{
			"invalid JSON with * arithmetic",
			validJSON,
		},
	}

	config := DefaultConfig()
	config.PlanParseRetryEnabled = true
	config.PlanParseMaxRetries = 2

	orchestrator := NewAIOrchestrator(config, discovery, mockAI)

	// Set up a default prompt builder
	builder, _ := NewDefaultPromptBuilder(nil)
	orchestrator.SetPromptBuilder(builder)

	plan, err := orchestrator.generateExecutionPlan(context.Background(), "test request", "test-123")

	if err != nil {
		t.Fatalf("Expected successful retry, got error: %v", err)
	}

	if plan == nil {
		t.Fatal("Expected plan to be returned")
	}

	if plan.PlanID != "test-123" {
		t.Errorf("Expected plan_id 'test-123', got %s", plan.PlanID)
	}

	// Should have made 2 calls (initial + 1 retry)
	if mockAI.callCount != 2 {
		t.Errorf("Expected 2 calls (initial + 1 retry), got %d", mockAI.callCount)
	}
}

// TestAIOrchestrator_GenerateExecutionPlan_AllRetriesExhausted tests when all retries fail
func TestAIOrchestrator_GenerateExecutionPlan_AllRetriesExhausted(t *testing.T) {
	discovery := NewMockDiscovery()

	// Mock AI client that always returns invalid JSON
	mockAI := &mockAIClientForRetry{
		responses: []string{
			"invalid JSON 1",
			"invalid JSON 2",
			"invalid JSON 3",
		},
	}

	config := DefaultConfig()
	config.PlanParseRetryEnabled = true
	config.PlanParseMaxRetries = 2

	orchestrator := NewAIOrchestrator(config, discovery, mockAI)

	// Set up a default prompt builder
	builder, _ := NewDefaultPromptBuilder(nil)
	orchestrator.SetPromptBuilder(builder)

	_, err := orchestrator.generateExecutionPlan(context.Background(), "test request", "test-123")

	if err == nil {
		t.Fatal("Expected error when all retries exhausted")
	}

	// Should have made 3 calls (initial + 2 retries)
	if mockAI.callCount != 3 {
		t.Errorf("Expected 3 calls (initial + 2 retries), got %d", mockAI.callCount)
	}
}

// mockAIClientForRetry is a mock AI client that returns different responses on each call
type mockAIClientForRetry struct {
	responses []string
	callCount int
}

func (m *mockAIClientForRetry) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	idx := m.callCount
	m.callCount++

	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}

	return &core.AIResponse{
		Content: m.responses[idx],
		Usage: core.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}, nil
}

// =============================================================================
// HITL Resume Context Helpers Tests
// =============================================================================

func TestWithPlanOverride(t *testing.T) {
	tests := []struct {
		name    string
		plan    *RoutingPlan
		wantNil bool
	}{
		{
			name: "valid plan",
			plan: &RoutingPlan{
				PlanID: "test-plan-1",
				Steps: []RoutingStep{
					{StepID: "step-1", AgentName: "agent-1"},
					{StepID: "step-2", AgentName: "agent-2"},
				},
			},
			wantNil: false,
		},
		{
			name:    "nil plan returns original context",
			plan:    nil,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			newCtx := WithPlanOverride(ctx, tt.plan)

			if tt.wantNil {
				// Should return same context when plan is nil
				if newCtx != ctx {
					t.Error("Expected same context when plan is nil")
				}
				retrieved := GetPlanOverride(newCtx)
				if retrieved != nil {
					t.Error("Expected nil plan from context")
				}
			} else {
				// Should return new context with plan
				retrieved := GetPlanOverride(newCtx)
				if retrieved == nil {
					t.Fatal("Expected plan from context, got nil")
				}
				if retrieved.PlanID != tt.plan.PlanID {
					t.Errorf("Expected PlanID %s, got %s", tt.plan.PlanID, retrieved.PlanID)
				}
				if len(retrieved.Steps) != len(tt.plan.Steps) {
					t.Errorf("Expected %d steps, got %d", len(tt.plan.Steps), len(retrieved.Steps))
				}
			}
		})
	}
}

func TestGetPlanOverride_NilContext(t *testing.T) {
	// Should safely handle nil context
	result := GetPlanOverride(context.TODO())
	if result != nil {
		t.Error("Expected nil for nil context")
	}
}

func TestGetPlanOverride_NoValue(t *testing.T) {
	// Should return nil when no plan is set
	ctx := context.Background()
	result := GetPlanOverride(ctx)
	if result != nil {
		t.Error("Expected nil when no plan override set")
	}
}

func TestWithCompletedSteps(t *testing.T) {
	tests := []struct {
		name    string
		results map[string]*StepResult
		wantNil bool
	}{
		{
			name: "valid completed steps",
			results: map[string]*StepResult{
				"step-1": {
					StepID:    "step-1",
					AgentName: "agent-1",
					Success:   true,
					Response:  `{"data": "result1"}`,
				},
				"step-2": {
					StepID:    "step-2",
					AgentName: "agent-2",
					Success:   true,
					Response:  `{"data": "result2"}`,
				},
			},
			wantNil: false,
		},
		{
			name:    "nil results returns original context",
			results: nil,
			wantNil: true,
		},
		{
			name:    "empty map is valid",
			results: map[string]*StepResult{},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			newCtx := WithCompletedSteps(ctx, tt.results)

			if tt.wantNil {
				// Should return same context when results is nil
				if newCtx != ctx {
					t.Error("Expected same context when results is nil")
				}
				retrieved := GetCompletedSteps(newCtx)
				if retrieved != nil {
					t.Error("Expected nil from context")
				}
			} else {
				// Should return new context with results
				retrieved := GetCompletedSteps(newCtx)
				if retrieved == nil {
					t.Fatal("Expected results from context, got nil")
				}
				if len(retrieved) != len(tt.results) {
					t.Errorf("Expected %d results, got %d", len(tt.results), len(retrieved))
				}
				// Verify specific entries
				for stepID, expected := range tt.results {
					actual, ok := retrieved[stepID]
					if !ok {
						t.Errorf("Missing step result for %s", stepID)
						continue
					}
					if actual.StepID != expected.StepID {
						t.Errorf("StepID mismatch for %s", stepID)
					}
				}
			}
		})
	}
}

func TestGetCompletedSteps_NilContext(t *testing.T) {
	// Should safely handle nil context
	result := GetCompletedSteps(context.TODO())
	if result != nil {
		t.Error("Expected nil for nil context")
	}
}

func TestGetCompletedSteps_NoValue(t *testing.T) {
	// Should return nil when no completed steps are set
	ctx := context.Background()
	result := GetCompletedSteps(ctx)
	if result != nil {
		t.Error("Expected nil when no completed steps set")
	}
}

// =============================================================================
// Orchestrator Plan Override Tests
// =============================================================================

// mockAIClientWithPromptTracking distinguishes between planning and synthesis calls
type mockAIClientWithPromptTracking struct {
	planningCallCount  int
	synthesisCallCount int
	otherCallCount     int
}

func (m *mockAIClientWithPromptTracking) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	// Detect if this is a planning prompt by looking for planning-related keywords
	// Planning prompts typically contain these patterns
	isPlanningPrompt := strings.Contains(prompt, "execution plan") ||
		strings.Contains(prompt, "routing plan") ||
		strings.Contains(prompt, "analyze the request") ||
		strings.Contains(prompt, "available agents") ||
		strings.Contains(prompt, "plan_id")

	// Synthesis prompts contain these patterns
	isSynthesisPrompt := strings.Contains(prompt, "synthesize") ||
		strings.Contains(prompt, "combine") ||
		strings.Contains(prompt, "Agent Responses")

	if isPlanningPrompt && !isSynthesisPrompt {
		m.planningCallCount++
	} else if isSynthesisPrompt {
		m.synthesisCallCount++
	} else {
		m.otherCallCount++
	}

	// Return a valid plan response (works for both planning and synthesis)
	plan := map[string]interface{}{
		"plan_id":          "llm-generated-plan",
		"original_request": "test request",
		"mode":             "autonomous",
		"steps": []map[string]interface{}{
			{
				"step_id":     "step-1",
				"agent_name":  "test-agent",
				"namespace":   "default",
				"instruction": "Do something",
				"depends_on":  []string{},
				"metadata":    map[string]interface{}{},
			},
		},
	}

	jsonBytes, _ := json.Marshal(plan)
	return &core.AIResponse{
		Content: string(jsonBytes),
		Usage: core.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}, nil
}

func TestOrchestrator_SkipsLLMPlanGenerationWhenPlanProvided(t *testing.T) {
	// This test verifies that when a plan is injected via context,
	// the LLM is NOT called for plan generation (it may still be called for synthesis).
	//
	// We detect plan generation calls by checking if the prompt contains
	// planning-related keywords like "execution plan" or "routing plan".

	discovery := NewMockDiscovery()
	aiClientWithOverride := &mockAIClientWithPromptTracking{}
	aiClientWithoutOverride := &mockAIClientWithPromptTracking{}

	// Register test agent
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "test-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "test_capability"}},
	})

	config := DefaultConfig()

	// Setup catalog (shared)
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"test-1": {
				Registration: &core.ServiceRegistration{
					ID:      "test-1",
					Name:    "test-agent",
					Address: "localhost",
					Port:    8080,
				},
				Capabilities: []EnhancedCapability{
					{Name: "test_capability", Endpoint: "/api/test"},
				},
			},
		},
	}

	// Test 1: WITH plan override - LLM should NOT be called for plan GENERATION
	orchestratorWithOverride := NewAIOrchestrator(config, discovery, aiClientWithOverride)
	orchestratorWithOverride.catalog = catalog

	injectedPlan := &RoutingPlan{
		PlanID:          "injected-plan-123",
		OriginalRequest: "test request",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Namespace:   "default",
				Instruction: "Test instruction",
				Metadata:    map[string]interface{}{"capability": "test_capability"},
			},
		},
	}

	ctx := WithPlanOverride(context.Background(), injectedPlan)

	// Attempt to process - will fail during execution due to no HTTP server,
	// but plan generation phase should use the override
	_, _ = orchestratorWithOverride.ProcessRequest(ctx, "test request", nil)

	// With plan override, the LLM should NOT be called for plan GENERATION
	// (synthesis calls are okay - they use different prompts)
	if aiClientWithOverride.planningCallCount > 0 {
		t.Errorf("LLM should NOT be called for plan generation when plan override is provided, but got %d planning calls", aiClientWithOverride.planningCallCount)
	}

	// Test 2: WITHOUT plan override - LLM SHOULD be called for planning
	orchestratorWithoutOverride := NewAIOrchestrator(config, discovery, aiClientWithoutOverride)
	orchestratorWithoutOverride.catalog = catalog

	// Process WITHOUT plan override
	_, _ = orchestratorWithoutOverride.ProcessRequest(context.Background(), "test request", nil)

	// Without plan override, the LLM SHOULD be called for plan generation
	if aiClientWithoutOverride.planningCallCount == 0 {
		t.Error("LLM SHOULD be called for plan generation when no plan override is provided")
	}
}

// TestGetAgentName tests the getAgentName helper method fallback chain.
// Priority: config.Name > config.RequestIDPrefix > "orchestrator"
// Note: config.Name can be set via TRUVAG3_AGENT_NAME env var in DefaultConfig()
func TestGetAgentName(t *testing.T) {
	tests := []struct {
		name     string
		config   *OrchestratorConfig
		expected string
	}{
		{
			name:     "nil config returns default",
			config:   nil,
			expected: "orchestrator",
		},
		{
			name:     "empty config returns default",
			config:   &OrchestratorConfig{},
			expected: "orchestrator",
		},
		{
			name: "only RequestIDPrefix set",
			config: &OrchestratorConfig{
				RequestIDPrefix: "awhl",
			},
			expected: "awhl",
		},
		{
			name: "only Name set",
			config: &OrchestratorConfig{
				Name: "travel-agent",
			},
			expected: "travel-agent",
		},
		{
			name: "both Name and RequestIDPrefix set - Name wins",
			config: &OrchestratorConfig{
				Name:            "my-agent",
				RequestIDPrefix: "prefix",
			},
			expected: "my-agent",
		},
		{
			name: "empty Name falls back to RequestIDPrefix",
			config: &OrchestratorConfig{
				Name:            "",
				RequestIDPrefix: "fallback-prefix",
			},
			expected: "fallback-prefix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create orchestrator with the test config
			o := &AIOrchestrator{
				config: tt.config,
			}

			got := o.getAgentName()
			if got != tt.expected {
				t.Errorf("getAgentName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// =============================================================================
// ORCH-004: Template Quoting, JSON Repair, and Parse Recovery Tests
// =============================================================================

func TestQuoteUnquotedTemplates(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "numeric param with unquoted template",
			input:    `{"lat": {{step-1.response.data.lat}}, "lon": {{step-1.response.data.lon}}}`,
			expected: `{"lat": "{{step-1.response.data.lat}}", "lon": "{{step-1.response.data.lon}}"}`,
		},
		{
			name:     "already quoted template unchanged",
			input:    `{"lat": "{{step-1.response.data.lat}}"}`,
			expected: `{"lat": "{{step-1.response.data.lat}}"}`,
		},
		{
			name:     "multiple templates in object",
			input:    `{"a": {{step-1.response.x}}, "b": "hello", "c": {{step-2.response.y}}}`,
			expected: `{"a": "{{step-1.response.x}}", "b": "hello", "c": "{{step-2.response.y}}"}`,
		},
		{
			name:     "nested path template",
			input:    `{"lat": {{step-1.response.data.results.0.geometry.lat}}}`,
			expected: `{"lat": "{{step-1.response.data.results.0.geometry.lat}}"}`,
		},
		{
			name:     "no templates returns unchanged",
			input:    `{"lat": 35.6897, "name": "Tokyo"}`,
			expected: `{"lat": 35.6897, "name": "Tokyo"}`,
		},
		{
			name:     "template in array position",
			input:    `{"ids": [{{step-1.response.id}}, {{step-2.response.id}}]}`,
			expected: `{"ids": ["{{step-1.response.id}}", "{{step-2.response.id}}"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteUnquotedTemplates(tt.input)
			if got != tt.expected {
				t.Errorf("quoteUnquotedTemplates():\n  got:  %s\n  want: %s", got, tt.expected)
			}
		})
	}
}

func TestAttemptJSONRepair(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		expectedOutput  string
		expectedChanged bool
	}{
		{
			name:            "trailing comma before closing brace",
			input:           `{"a": 1, "b": 2,}`,
			expectedOutput:  `{"a": 1, "b": 2}`,
			expectedChanged: true,
		},
		{
			name:            "trailing comma before closing bracket",
			input:           `{"items": [1, 2, 3,]}`,
			expectedOutput:  `{"items": [1, 2, 3]}`,
			expectedChanged: true,
		},
		{
			name:            "BOM prefix",
			input:           "\uFEFF" + `{"a": 1}`,
			expectedOutput:  `{"a": 1}`,
			expectedChanged: true,
		},
		{
			name:            "zero-width characters prefix",
			input:           "\u200B\u200C" + `{"a": 1}`,
			expectedOutput:  `{"a": 1}`,
			expectedChanged: true,
		},
		{
			name:            "valid JSON unchanged",
			input:           `{"a": 1, "b": 2}`,
			expectedOutput:  `{"a": 1, "b": 2}`,
			expectedChanged: false,
		},
		{
			name:            "trailing comma with whitespace",
			input:           "{\"a\": 1,\n  }",
			expectedOutput:  "{\"a\": 1}",
			expectedChanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := attemptJSONRepair(tt.input)
			if got != tt.expectedOutput {
				t.Errorf("attemptJSONRepair() output:\n  got:  %q\n  want: %q", got, tt.expectedOutput)
			}
			if changed != tt.expectedChanged {
				t.Errorf("attemptJSONRepair() changed = %v, want %v", changed, tt.expectedChanged)
			}
		})
	}
}

func TestParsePlan_UnquotedTemplateRecovery(t *testing.T) {
	orchestrator := &AIOrchestrator{}

	// JSON with unquoted templates (the exact bug from ORCH-004)
	jsonWithUnquotedTemplates := `{
		"plan_id": "test-recovery",
		"original_request": "weather in Tokyo",
		"mode": "autonomous",
		"steps": [
			{
				"step_id": "step-1",
				"agent_name": "geocoding-tool",
				"namespace": "default",
				"instruction": "geocode Tokyo",
				"payload": {"location": "Tokyo, Japan"}
			},
			{
				"step_id": "step-2",
				"agent_name": "weather-tool",
				"namespace": "default",
				"instruction": "get weather",
				"depends_on": ["step-1"],
				"payload": {"lat": {{step-1.response.data.lat}}, "lon": {{step-1.response.data.lon}}}
			}
		]
	}`

	plan, err := orchestrator.parsePlan(jsonWithUnquotedTemplates)
	if err != nil {
		t.Fatalf("Expected parsePlan to recover unquoted templates, got error: %v", err)
	}

	if plan.PlanID != "test-recovery" {
		t.Errorf("Expected plan_id 'test-recovery', got %s", plan.PlanID)
	}

	if len(plan.Steps) != 2 {
		t.Fatalf("Expected 2 steps, got %d", len(plan.Steps))
	}

	// Verify the template references were preserved as strings
	step2 := plan.Steps[1]
	if step2.AgentName != "weather-tool" {
		t.Errorf("Expected step-2 agent 'weather-tool', got %s", step2.AgentName)
	}
}

func TestParsePlan_TrailingCommaRecovery(t *testing.T) {
	orchestrator := &AIOrchestrator{}

	jsonWithTrailingComma := `{
		"plan_id": "test-comma",
		"original_request": "test",
		"mode": "autonomous",
		"steps": [
			{
				"step_id": "step-1",
				"agent_name": "test-agent",
				"namespace": "default",
				"instruction": "do something",
			}
		]
	}`

	plan, err := orchestrator.parsePlan(jsonWithTrailingComma)
	if err != nil {
		t.Fatalf("Expected parsePlan to recover trailing comma, got error: %v", err)
	}

	if plan.PlanID != "test-comma" {
		t.Errorf("Expected plan_id 'test-comma', got %s", plan.PlanID)
	}
}

func TestParsePlan_CaseInsensitiveMarkdown(t *testing.T) {
	orchestrator := &AIOrchestrator{}

	// LLMs sometimes output ```JSON (uppercase) instead of ```json
	jsonWithUpperMarkdown := "```JSON\n" + `{
		"plan_id": "test-upper",
		"original_request": "test",
		"mode": "autonomous",
		"steps": [
			{
				"step_id": "step-1",
				"agent_name": "test-agent",
				"namespace": "default",
				"instruction": "do something"
			}
		]
	}` + "\n```"

	plan, err := orchestrator.parsePlan(jsonWithUpperMarkdown)
	if err != nil {
		t.Fatalf("Expected parsePlan to handle uppercase ```JSON, got error: %v", err)
	}

	if plan.PlanID != "test-upper" {
		t.Errorf("Expected plan_id 'test-upper', got %s", plan.PlanID)
	}
}

func TestParsePlan_IrrecoverableError(t *testing.T) {
	orchestrator := &AIOrchestrator{}

	// Completely invalid input that no recovery can fix
	_, err := orchestrator.parsePlan("I cannot generate a plan for this request.")
	if err == nil {
		t.Error("Expected error for completely invalid input")
	}
}

func TestBuildPlanningPromptWithParseError_ReusesPrompt(t *testing.T) {
	orchestrator := &AIOrchestrator{}

	// Simulate a previous prompt result with specific AllowedAgents
	previousPromptResult := &PlanningPromptResult{
		Prompt: "Original planning prompt with tool catalog",
		AllowedAgents: map[string]bool{
			"weather-tool":   true,
			"geocoding-tool": true,
			"currency-tool":  true,
		},
	}

	parseErr := fmt.Errorf("unexpected end of JSON input")

	result, err := orchestrator.buildPlanningPromptWithParseError(
		context.Background(),
		previousPromptResult,
		parseErr,
	)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// AllowedAgents must be identical (no fresh tiered selection)
	if len(result.AllowedAgents) != 3 {
		t.Errorf("Expected 3 AllowedAgents preserved, got %d", len(result.AllowedAgents))
	}
	for agent := range previousPromptResult.AllowedAgents {
		if !result.AllowedAgents[agent] {
			t.Errorf("AllowedAgents missing %q — tiered selection was not reused", agent)
		}
	}

	// The retry prompt should contain the error feedback
	if !strings.Contains(result.Prompt, "unexpected end of JSON input") {
		t.Error("Expected parse error to be included in retry prompt")
	}

	// The retry prompt should contain the original prompt
	if !strings.Contains(result.Prompt, "Original planning prompt with tool catalog") {
		t.Error("Expected original prompt to be preserved in retry prompt")
	}

	// Error feedback should come before the original prompt
	errIdx := strings.Index(result.Prompt, "<parse_error>")
	origIdx := strings.Index(result.Prompt, "Original planning prompt")
	if errIdx >= origIdx {
		t.Error("Expected error feedback to precede original prompt in retry prompt")
	}
}

func TestParsePlan_CommaBeforeTemplateInString_NoFalsePositive(t *testing.T) {
	orchestrator := &AIOrchestrator{}

	// Valid JSON where a comma appears before a template INSIDE a quoted string.
	// quoteUnquotedTemplates should NOT run on valid JSON (try-parse-first).
	jsonWithCommaInString := `{
		"plan_id": "test-no-fp",
		"original_request": "test",
		"mode": "autonomous",
		"steps": [
			{
				"step_id": "step-1",
				"agent_name": "geocoding-tool",
				"namespace": "default",
				"instruction": "geocode city",
				"payload": {"location": "Tokyo, Japan"}
			},
			{
				"step_id": "step-2",
				"agent_name": "search-tool",
				"namespace": "default",
				"instruction": "search",
				"depends_on": ["step-1"],
				"payload": {"query": "weather in {{step-1.response.data.city}}, {{step-1.response.data.country}}"}
			}
		]
	}`

	plan, err := orchestrator.parsePlan(jsonWithCommaInString)
	if err != nil {
		t.Fatalf("Expected valid JSON to parse on first try (no C2 false positive), got error: %v", err)
	}

	if plan.PlanID != "test-no-fp" {
		t.Errorf("Expected plan_id 'test-no-fp', got %s", plan.PlanID)
	}

	if len(plan.Steps) != 2 {
		t.Errorf("Expected 2 steps, got %d", len(plan.Steps))
	}
}

func TestGenerateExecutionPlan_RetryReusesCapabilities(t *testing.T) {
	discovery := NewMockDiscovery()

	// First attempt returns invalid JSON, second attempt returns valid JSON
	validJSON := `{
		"plan_id": "test-reuse",
		"original_request": "test",
		"mode": "autonomous",
		"steps": [
			{
				"step_id": "step-1",
				"agent_name": "test-agent",
				"namespace": "default",
				"instruction": "do something"
			}
		]
	}`

	mockAI := &mockAIClientForRetry{
		responses: []string{
			"invalid JSON with * arithmetic",
			validJSON,
		},
	}

	config := DefaultConfig()
	config.PlanParseRetryEnabled = true
	config.PlanParseMaxRetries = 2

	orchestrator := NewAIOrchestrator(config, discovery, mockAI)

	// Use a counting prompt builder to track how many times BuildPlanningPrompt is called
	countingBuilder := &countingPromptBuilder{calls: 0}
	orchestrator.SetPromptBuilder(countingBuilder)

	plan, err := orchestrator.generateExecutionPlan(context.Background(), "test request", "test-123")

	if err != nil {
		t.Fatalf("Expected successful retry, got error: %v", err)
	}

	if plan == nil {
		t.Fatal("Expected plan to be returned")
	}

	// BuildPlanningPrompt should be called exactly ONCE (initial attempt only)
	// The retry should reuse the previous prompt result, not rebuild from scratch
	if countingBuilder.calls != 1 {
		t.Errorf("Expected BuildPlanningPrompt to be called 1 time, got %d (retry should reuse prompt)", countingBuilder.calls)
	}

	// AI client should be called twice (initial + 1 retry)
	if mockAI.callCount != 2 {
		t.Errorf("Expected 2 AI calls (initial + 1 retry), got %d", mockAI.callCount)
	}
}

// countingPromptBuilder tracks how many times BuildPlanningPrompt is called
type countingPromptBuilder struct {
	calls int
}

func (c *countingPromptBuilder) BuildPlanningPrompt(ctx context.Context, input PromptInput) (string, error) {
	c.calls++
	return fmt.Sprintf("Generate a plan for: %s\nTools: %s", input.Request, input.CapabilityInfo), nil
}
