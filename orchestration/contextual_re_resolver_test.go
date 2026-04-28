package orchestration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// contextualReResolverMockAI implements core.AIClient for testing ContextualReResolver
// Named to avoid conflicts with other mock types in the package
type contextualReResolverMockAI struct {
	response   string
	err        error
	lastPrompt string // Captures the prompt for verification
	callCount  int    // Tracks number of calls
}

func (m *contextualReResolverMockAI) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.lastPrompt = prompt
	m.callCount++
	if m.err != nil {
		return nil, m.err
	}
	return &core.AIResponse{Content: m.response}, nil
}

// =============================================================================
// Constructor Tests
// =============================================================================

// TestNewContextualReResolver verifies default construction
func TestNewContextualReResolver(t *testing.T) {
	ai := &contextualReResolverMockAI{}
	logger := &mockLogger{}

	resolver := NewContextualReResolver(ai, logger)

	if resolver.aiClient != ai {
		t.Error("Expected aiClient to be set")
	}
	if resolver.logger == nil {
		t.Error("Expected logger to be set")
	}
}

// TestNewContextualReResolver_NilLogger accepts nil logger
func TestNewContextualReResolver_NilLogger(t *testing.T) {
	ai := &contextualReResolverMockAI{}

	resolver := NewContextualReResolver(ai, nil)

	if resolver.aiClient != ai {
		t.Error("Expected aiClient to be set")
	}
	if resolver.logger != nil {
		t.Error("Expected logger to be nil")
	}
}

// TestNewContextualReResolver_NilAIClient accepts nil AI client
func TestNewContextualReResolver_NilAIClient(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	if resolver.aiClient != nil {
		t.Error("Expected aiClient to be nil")
	}
}

// =============================================================================
// ReResolve - Error Handling Tests
// =============================================================================

// TestReResolve_NilContext returns error for nil execution context
func TestReResolve_NilContext(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	result, err := resolver.ReResolve(context.Background(), nil)

	if err == nil {
		t.Error("Expected error for nil context")
	}
	if result != nil {
		t.Error("Expected nil result for nil context")
	}
	if err.Error() != "execution context is required" {
		t.Errorf("Expected 'execution context is required' error, got: %v", err)
	}
}

// TestReResolve_NilAIClient returns non-retry result gracefully
func TestReResolve_NilAIClient(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil) // No AI client

	execCtx := &ExecutionContext{
		UserQuery:       "test query",
		SourceData:      map[string]interface{}{"key": "value"},
		StepID:          "step-1",
		Capability:      &EnhancedCapability{Name: "test_cap"},
		AttemptedParams: map[string]interface{}{},
		ErrorResponse:   "error",
		HTTPStatus:      400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if result.ShouldRetry {
		t.Error("Expected ShouldRetry=false when AI client is nil")
	}
	if result.Analysis != "AI client not configured for semantic retry" {
		t.Errorf("Expected analysis message about AI client, got: %s", result.Analysis)
	}
}

// TestReResolve_CancelledContext returns error for cancelled context
func TestReResolve_CancelledContext(t *testing.T) {
	ai := &contextualReResolverMockAI{response: "{}"}
	resolver := NewContextualReResolver(ai, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	execCtx := &ExecutionContext{
		UserQuery:       "test query",
		SourceData:      map[string]interface{}{"key": "value"},
		StepID:          "step-1",
		Capability:      &EnhancedCapability{Name: "test_cap"},
		AttemptedParams: map[string]interface{}{},
		ErrorResponse:   "error",
		HTTPStatus:      400,
	}

	result, err := resolver.ReResolve(ctx, execCtx)

	if err == nil {
		t.Error("Expected error for cancelled context")
	}
	if result != nil {
		t.Error("Expected nil result for cancelled context")
	}
	if ai.callCount > 0 {
		t.Error("LLM should not be called when context is cancelled")
	}
}

// TestReResolve_AIClientError handles AI client errors gracefully
func TestReResolve_AIClientError(t *testing.T) {
	ai := &contextualReResolverMockAI{err: errors.New("LLM service unavailable")}
	resolver := NewContextualReResolver(ai, &mockLogger{})

	execCtx := &ExecutionContext{
		UserQuery:       "test query",
		SourceData:      map[string]interface{}{"key": "value"},
		StepID:          "step-1",
		Capability:      &EnhancedCapability{Name: "test_cap"},
		AttemptedParams: map[string]interface{}{},
		ErrorResponse:   "error",
		HTTPStatus:      400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err == nil {
		t.Error("Expected error when AI client fails")
	}
	if result != nil {
		t.Error("Expected nil result when AI client fails")
	}
	if ai.callCount != 1 {
		t.Errorf("Expected 1 AI call, got %d", ai.callCount)
	}
}

// =============================================================================
// ReResolve - JSON Parsing Tests
// =============================================================================

// TestReResolve_InvalidJSON handles malformed JSON response
func TestReResolve_InvalidJSON(t *testing.T) {
	ai := &contextualReResolverMockAI{response: "not valid json"}
	resolver := NewContextualReResolver(ai, &mockLogger{})

	execCtx := &ExecutionContext{
		UserQuery:       "test query",
		SourceData:      map[string]interface{}{"key": "value"},
		StepID:          "step-1",
		Capability:      &EnhancedCapability{Name: "test_cap"},
		AttemptedParams: map[string]interface{}{},
		ErrorResponse:   "error",
		HTTPStatus:      400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
	if result != nil {
		t.Error("Expected nil result for invalid JSON")
	}
}

// TestReResolve_JSONWithMarkdown handles markdown-wrapped JSON
func TestReResolve_JSONWithMarkdown(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: "```json\n{\"should_retry\": true, \"analysis\": \"Fixed via markdown\", \"corrected_parameters\": {\"amount\": 100}}\n```",
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery:       "test query",
		SourceData:      map[string]interface{}{"price": 100.0},
		StepID:          "step-1",
		Capability:      &EnhancedCapability{Name: "test_cap"},
		AttemptedParams: map[string]interface{}{"amount": 0},
		ErrorResponse:   "amount must be > 0",
		HTTPStatus:      400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true")
	}
	if result.CorrectedParameters["amount"] != 100.0 {
		t.Errorf("Expected amount=100, got: %v", result.CorrectedParameters["amount"])
	}
}

// TestReResolve_JSONWithExtraText handles JSON with surrounding text
func TestReResolve_JSONWithExtraText(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: "Here's my analysis:\n{\"should_retry\": true, \"analysis\": \"Fixed\", \"corrected_parameters\": {\"value\": 42}}\nHope this helps!",
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery:       "test query",
		SourceData:      map[string]interface{}{"data": 42},
		StepID:          "step-1",
		Capability:      &EnhancedCapability{Name: "test_cap"},
		AttemptedParams: map[string]interface{}{"value": 0},
		ErrorResponse:   "value required",
		HTTPStatus:      400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true")
	}
}

// =============================================================================
// ReResolve - Computation Required Tests (Core Functionality)
// =============================================================================

// TestReResolve_ComputationRequired_StockSale tests LLM computing derived values
// This is the primary use case from the design doc: 100 shares × $468.285 = $46828.5
func TestReResolve_ComputationRequired_StockSale(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: `{
			"should_retry": true,
			"analysis": "User wants to sell 100 shares at $468.285 per share, so amount = 100 × 468.285 = 46828.5",
			"corrected_parameters": {"from": "USD", "to": "KRW", "amount": 46828.5}
		}`,
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery: "I am planning to sell 100 Tesla shares to fund my travel to Seoul",
		SourceData: map[string]interface{}{
			"symbol":        "TSLA",
			"current_price": 468.285,
		},
		StepID: "step-5-convert_currency",
		Capability: &EnhancedCapability{
			Name: "convert_currency",
			Parameters: []Parameter{
				{Name: "from", Type: "string", Required: true, Description: "Source currency code"},
				{Name: "to", Type: "string", Required: true, Description: "Target currency code"},
				{Name: "amount", Type: "number", Required: true, Description: "Amount to convert"},
			},
		},
		AttemptedParams: map[string]interface{}{
			"from":   "USD",
			"to":     "KRW",
			"amount": 0.0, // MicroResolver failed to compute
		},
		ErrorResponse: `{"error": "amount must be greater than 0"}`,
		HTTPStatus:    400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true for fixable computation error")
	}

	// Check corrected amount
	amount, ok := result.CorrectedParameters["amount"].(float64)
	if !ok {
		t.Fatalf("Expected amount to be float64, got %T", result.CorrectedParameters["amount"])
	}
	if amount < 46828.0 || amount > 46829.0 {
		t.Errorf("Expected amount ≈ 46828.5, got: %v", amount)
	}

	// Check analysis mentions the computation
	if result.Analysis == "" {
		t.Error("Expected analysis explaining the fix")
	}
}

// TestReResolve_TypeCoercion tests fixing type mismatches
func TestReResolve_TypeCoercion(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: `{
			"should_retry": true,
			"analysis": "lat was sent as string, converting to number",
			"corrected_parameters": {"lat": 35.6762, "lon": 139.6503}
		}`,
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery: "get weather in Tokyo",
		SourceData: map[string]interface{}{
			"latitude":  35.6762,
			"longitude": 139.6503,
		},
		StepID: "step-2-get_weather",
		Capability: &EnhancedCapability{
			Name: "get_weather",
			Parameters: []Parameter{
				{Name: "lat", Type: "number", Required: true, Description: "Latitude"},
				{Name: "lon", Type: "number", Required: true, Description: "Longitude"},
			},
		},
		AttemptedParams: map[string]interface{}{
			"lat": "35.6762", // String instead of number
			"lon": "139.6503",
		},
		ErrorResponse: "lat must be a number",
		HTTPStatus:    400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true for type coercion")
	}

	lat, ok := result.CorrectedParameters["lat"].(float64)
	if !ok {
		t.Fatalf("Expected lat to be float64, got %T", result.CorrectedParameters["lat"])
	}
	if lat < 35.67 || lat > 35.68 {
		t.Errorf("Expected lat ≈ 35.6762, got: %v", lat)
	}
}

// TestReResolve_NegativeToPositive tests fixing sign errors
func TestReResolve_NegativeToPositive(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: `{
			"should_retry": true,
			"analysis": "Amount was negative but should be positive based on source data",
			"corrected_parameters": {"amount": 46828.5}
		}`,
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery: "convert currency",
		SourceData: map[string]interface{}{
			"value": 46828.5,
		},
		StepID: "step-1",
		Capability: &EnhancedCapability{
			Name: "convert_currency",
			Parameters: []Parameter{
				{Name: "amount", Type: "number", Required: true},
			},
		},
		AttemptedParams: map[string]interface{}{
			"amount": -46828.5, // Negative error
		},
		ErrorResponse: "amount must be greater than 0",
		HTTPStatus:    422,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true")
	}

	amount := result.CorrectedParameters["amount"].(float64)
	if amount != 46828.5 {
		t.Errorf("Expected amount=46828.5, got: %v", amount)
	}
}

// =============================================================================
// ReResolve - Cannot Fix Tests
// =============================================================================

// TestReResolve_CannotFix_MissingSourceData tests unfixable errors
func TestReResolve_CannotFix_MissingSourceData(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: `{
			"should_retry": false,
			"analysis": "No stock symbol available in source data to determine which stock to query",
			"corrected_parameters": {}
		}`,
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery:  "get stock price",
		SourceData: map[string]interface{}{}, // Empty - no symbol available
		StepID:     "step-1-get_stock_quote",
		Capability: &EnhancedCapability{
			Name: "get_stock_quote",
			Parameters: []Parameter{
				{Name: "symbol", Type: "string", Required: true, Description: "Stock ticker symbol"},
			},
		},
		AttemptedParams: map[string]interface{}{"symbol": ""},
		ErrorResponse:   "symbol is required",
		HTTPStatus:      400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if result.ShouldRetry {
		t.Error("Expected ShouldRetry=false for unfixable error")
	}
	if result.Analysis == "" {
		t.Error("Expected analysis explaining why it cannot be fixed")
	}
}

// TestReResolve_CannotFix_ExternalServiceError tests non-parameter errors
func TestReResolve_CannotFix_ExternalServiceError(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: `{
			"should_retry": false,
			"analysis": "The error indicates a backend service issue, not a parameter problem",
			"corrected_parameters": {}
		}`,
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery: "get weather",
		SourceData: map[string]interface{}{
			"lat": 35.6762,
			"lon": 139.6503,
		},
		StepID: "step-1",
		Capability: &EnhancedCapability{
			Name: "get_weather",
			Parameters: []Parameter{
				{Name: "lat", Type: "number", Required: true},
				{Name: "lon", Type: "number", Required: true},
			},
		},
		AttemptedParams: map[string]interface{}{
			"lat": 35.6762,
			"lon": 139.6503,
		},
		ErrorResponse: "upstream weather API unavailable",
		HTTPStatus:    503,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if result.ShouldRetry {
		t.Error("Expected ShouldRetry=false for service errors")
	}
}

// =============================================================================
// ReResolve - Previous Errors Tests (Memory Across Attempts)
// =============================================================================

// TestReResolve_WithPreviousErrors includes error history in prompt
func TestReResolve_WithPreviousErrors(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: `{
			"should_retry": true,
			"analysis": "After two failed attempts, determined correct amount is 100",
			"corrected_parameters": {"amount": 100}
		}`,
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery: "convert 100 USD",
		SourceData: map[string]interface{}{
			"value": 100,
		},
		StepID: "step-1",
		Capability: &EnhancedCapability{
			Name: "convert_currency",
			Parameters: []Parameter{
				{Name: "amount", Type: "number", Required: true},
			},
		},
		AttemptedParams: map[string]interface{}{"amount": 0},
		ErrorResponse:   "amount must be greater than 0",
		HTTPStatus:      400,
		RetryCount:      2,
		PreviousErrors: []string{
			"amount cannot be negative",
			"amount must be a positive number",
		},
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}

	// Verify the prompt included previous errors
	if ai.lastPrompt == "" {
		t.Fatal("Expected prompt to be captured")
	}
	if !strings.Contains(ai.lastPrompt, "<previous_failed_attempts>") {
		t.Error("Expected prompt to include previous errors section")
	}
	if !strings.Contains(ai.lastPrompt, "amount cannot be negative") {
		t.Error("Expected prompt to include first previous error")
	}
}

// =============================================================================
// SetLogger Tests
// =============================================================================

// TestSetLogger_WithComponentAwareLogger sets logger with component
func TestSetLogger_WithComponentAwareLogger(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	// Use mockLogger which implements ComponentAwareLogger
	logger := &mockLogger{}
	resolver.SetLogger(logger)

	if resolver.logger == nil {
		t.Error("Expected logger to be set")
	}
}

// TestSetLogger_Nil clears logger
func TestSetLogger_Nil(t *testing.T) {
	logger := &mockLogger{}
	resolver := NewContextualReResolver(nil, logger)

	resolver.SetLogger(nil)

	if resolver.logger != nil {
		t.Error("Expected logger to be nil after SetLogger(nil)")
	}
}

// =============================================================================
// parseReResolutionResponse Tests
// =============================================================================

// TestParseReResolutionResponse_ValidJSON parses clean JSON
func TestParseReResolutionResponse_ValidJSON(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	content := `{"should_retry": true, "analysis": "test", "corrected_parameters": {"key": "value"}}`
	result, err := resolver.parseReResolutionResponse(content)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true")
	}
	if result.Analysis != "test" {
		t.Errorf("Expected analysis='test', got: %s", result.Analysis)
	}
	if result.CorrectedParameters["key"] != "value" {
		t.Errorf("Expected key='value', got: %v", result.CorrectedParameters["key"])
	}
}

// TestParseReResolutionResponse_NilCorrectedParams initializes empty map
func TestParseReResolutionResponse_NilCorrectedParams(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	content := `{"should_retry": false, "analysis": "cannot fix"}`
	result, err := resolver.parseReResolutionResponse(content)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
	if result.CorrectedParameters == nil {
		t.Error("Expected CorrectedParameters to be initialized as empty map")
	}
}

// TestParseReResolutionResponse_NoJSON returns error
func TestParseReResolutionResponse_NoJSON(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	content := "This is just text with no JSON"
	result, err := resolver.parseReResolutionResponse(content)

	if err == nil {
		t.Error("Expected error for content without JSON")
	}
	if result != nil {
		t.Error("Expected nil result")
	}
}

// TestParseReResolutionResponse_NestedJSON handles complex structures
func TestParseReResolutionResponse_NestedJSON(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	content := `{
		"should_retry": true,
		"analysis": "Fixed nested structure",
		"corrected_parameters": {
			"address": {
				"city": "Tokyo",
				"country": "Japan"
			},
			"coordinates": [35.6762, 139.6503]
		}
	}`
	result, err := resolver.parseReResolutionResponse(content)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}

	address, ok := result.CorrectedParameters["address"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected address to be map, got %T", result.CorrectedParameters["address"])
	}
	if address["city"] != "Tokyo" {
		t.Errorf("Expected city=Tokyo, got: %v", address["city"])
	}

	coords, ok := result.CorrectedParameters["coordinates"].([]interface{})
	if !ok {
		t.Fatalf("Expected coordinates to be array, got %T", result.CorrectedParameters["coordinates"])
	}
	if len(coords) != 2 {
		t.Errorf("Expected 2 coordinates, got: %d", len(coords))
	}
}

// =============================================================================
// buildReResolutionPrompt Tests
// =============================================================================

// TestBuildReResolutionPrompt_IncludesAllContext verifies prompt construction
func TestBuildReResolutionPrompt_IncludesAllContext(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	execCtx := &ExecutionContext{
		UserQuery: "sell 100 TSLA shares",
		SourceData: map[string]interface{}{
			"price":  468.285,
			"symbol": "TSLA",
		},
		StepID: "step-5",
		Capability: &EnhancedCapability{
			Name: "convert_currency",
			Parameters: []Parameter{
				{Name: "amount", Type: "number", Required: true, Description: "Amount to convert"},
				{Name: "from", Type: "string", Required: true, Description: "Source currency"},
			},
		},
		AttemptedParams: map[string]interface{}{
			"amount": 0,
			"from":   "USD",
		},
		ErrorResponse: "amount must be greater than 0",
		HTTPStatus:    400,
	}

	prompt := resolver.buildReResolutionPrompt(context.Background(), execCtx)

	// Check all essential parts are included
	checks := []struct {
		content string
		desc    string
	}{
		{"sell 100 TSLA shares", "user query"},
		{"468.285", "source data price"},
		{"TSLA", "source data symbol"},
		{"convert_currency", "capability name"},
		{"amount must be greater than 0", "error response"},
		{"400", "HTTP status"},
		{"amount (number", "parameter schema"},
		{"from (string", "parameter schema"},
	}

	for _, check := range checks {
		if !strings.Contains(prompt, check.content) {
			t.Errorf("Expected prompt to include %s: %s", check.desc, check.content)
		}
	}
}

// TestBuildReResolutionPrompt_WithPreviousErrors includes error history
func TestBuildReResolutionPrompt_WithPreviousErrors(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	execCtx := &ExecutionContext{
		UserQuery:       "test",
		SourceData:      map[string]interface{}{},
		StepID:          "step-1",
		Capability:      &EnhancedCapability{Name: "test"},
		AttemptedParams: map[string]interface{}{},
		ErrorResponse:   "current error",
		HTTPStatus:      400,
		RetryCount:      2,
		PreviousErrors: []string{
			"first error",
			"second error",
		},
	}

	prompt := resolver.buildReResolutionPrompt(context.Background(), execCtx)

	if !strings.Contains(prompt, "<previous_failed_attempts>") {
		t.Error("Expected prompt to include previous errors section")
	}
	if !strings.Contains(prompt, "first error") {
		t.Error("Expected prompt to include first error")
	}
	if !strings.Contains(prompt, "second error") {
		t.Error("Expected prompt to include second error")
	}
}

// TestBuildReResolutionPrompt_ArgumentOrdering guards the fmt.Sprintf argument
// ordering in buildReResolutionPrompt. After ORCH-007 Change 2, the argument order is:
//
//	UserQuery, sourceJSON, Name, HTTPStatus, failedJSON, ErrorResponse, previousContext, paramDescs
//
// The prompt layout is: <user_request> -> <source_data> -> <failed_attempt>(Cap,Status,Params,Error) -> <capability_schema>
func TestBuildReResolutionPrompt_ArgumentOrdering(t *testing.T) {
	resolver := NewContextualReResolver(nil, nil)

	execCtx := &ExecutionContext{
		UserQuery:  "Convert 500 EUR to USD",
		SourceData: map[string]interface{}{"rate": 1.08, "base": "EUR"},
		StepID:     "step-3",
		Capability: &EnhancedCapability{
			Name: "convert_currency",
			Parameters: []Parameter{
				{Name: "from", Type: "string", Description: "Source currency code"},
				{Name: "to", Type: "string", Description: "Target currency code"},
				{Name: "amount", Type: "number", Description: "Amount to convert"},
			},
		},
		AttemptedParams: map[string]interface{}{"from": "EUR", "to": "USD", "amount": -500},
		ErrorResponse:   "amount must be positive",
		HTTPStatus:      422,
	}

	prompt := resolver.buildReResolutionPrompt(context.Background(), execCtx)

	// --- 1. No fmt.Sprintf type-mismatch garbage ---
	// If %d receives a string or %s receives an int, Go prints "%!d(string=...)"
	// or "%!s(int=...)". This catches any argument-position misalignment.
	if strings.Contains(prompt, "%!") {
		t.Fatalf("Prompt contains fmt.Sprintf type-mismatch indicator (%%!), "+
			"which means arguments are in the wrong order:\n%s", prompt)
	}

	// --- 2. All values are present ---
	required := map[string]string{
		"UserQuery":      "Convert 500 EUR to USD",
		"CapabilityName": "convert_currency",
		"HTTPStatus":     "422",
		"ErrorResponse":  "amount must be positive",
		"ParamSchema":    "Source currency code",
		"AttemptedParam": `"amount"`,
		"SourceData":     "1.08",
	}
	for label, val := range required {
		if !strings.Contains(prompt, val) {
			t.Fatalf("Expected %s value %q in prompt, got:\n%s", label, val, prompt)
		}
	}

	// --- 3. Positional correctness ---
	// Each value must appear inside its correct section. We locate section
	// headers and verify values fall between the right pair of headers.

	// Locate section boundaries
	userReqSection := strings.Index(prompt, "<user_request>")
	sourceDataSection := strings.Index(prompt, "<source_data>")
	failedAttemptSection := strings.Index(prompt, "<failed_attempt")
	schemaSection := strings.Index(prompt, "<capability_schema>")

	if userReqSection < 0 || sourceDataSection < 0 || failedAttemptSection < 0 || schemaSection < 0 {
		t.Fatalf("Could not locate all section headers in prompt:\n%s", prompt)
	}

	// Locate values
	userQueryIdx := strings.Index(prompt, "Convert 500 EUR to USD")
	sourceDataValueIdx := strings.Index(prompt, "1.08")
	capIdx := strings.Index(prompt, "convert_currency")
	paramsIdx := strings.Index(prompt, `"amount"`)
	errorIdx := strings.Index(prompt, "amount must be positive")
	statusIdx := strings.Index(prompt, "422")
	schemaValueIdx := strings.Index(prompt, "Source currency code")

	// 3a. UserQuery must be inside the USER REQUEST section (between userReqSection and sourceDataSection)
	if userQueryIdx < userReqSection || userQueryIdx > sourceDataSection {
		t.Errorf("UserQuery should appear between USER REQUEST and SOURCE DATA sections\n"+
			"  USER_REQUEST_section@%d, UserQuery@%d, SOURCE_DATA_section@%d",
			userReqSection, userQueryIdx, sourceDataSection)
	}

	// 3b. SourceData values must be between SOURCE DATA and FAILED ATTEMPT sections
	if sourceDataValueIdx < sourceDataSection || sourceDataValueIdx > failedAttemptSection {
		t.Errorf("Source data value should appear between SOURCE DATA and FAILED ATTEMPT sections\n"+
			"  SOURCE_DATA@%d, value@%d, FAILED_ATTEMPT@%d",
			sourceDataSection, sourceDataValueIdx, failedAttemptSection)
	}

	// 3c. Capability, params, error, and status must all be inside FAILED ATTEMPT section
	//     (between failedAttemptSection and schemaSection)
	failedAttemptValues := map[string]int{
		"CapabilityName":  capIdx,
		"AttemptedParams": paramsIdx,
		"ErrorResponse":   errorIdx,
		"HTTPStatus":      statusIdx,
	}
	for label, idx := range failedAttemptValues {
		if idx < failedAttemptSection || idx > schemaSection {
			t.Errorf("%s should appear between FAILED ATTEMPT and SCHEMA sections\n"+
				"  FAILED_ATTEMPT@%d, %s@%d, SCHEMA@%d",
				label, failedAttemptSection, label, idx, schemaSection)
		}
	}

	// 3d. Within FAILED ATTEMPT: Capability < Status < Params < Error (ORCH-007 Change 2 layout)
	if capIdx >= statusIdx || statusIdx >= paramsIdx || paramsIdx >= errorIdx {
		t.Errorf("Within FAILED ATTEMPT section, expected ordering: Capability < Status < Params < Error\n"+
			"  Capability@%d, Status@%d, Params@%d, Error@%d",
			capIdx, statusIdx, paramsIdx, errorIdx)
	}

	// 3e. Schema values must appear after SCHEMA section header
	if schemaValueIdx < schemaSection {
		t.Errorf("Schema description should appear after SCHEMA section header\n"+
			"  SCHEMA@%d, value@%d", schemaSection, schemaValueIdx)
	}

	// --- 4. Section ordering: UserReq < SourceData < FailedAttempt < Schema ---
	if userReqSection >= sourceDataSection ||
		sourceDataSection >= failedAttemptSection ||
		failedAttemptSection >= schemaSection {
		t.Errorf("Section ordering violated.\n"+
			"  USER_REQUEST@%d < SOURCE_DATA@%d < FAILED_ATTEMPT@%d < SCHEMA@%d",
			userReqSection, sourceDataSection, failedAttemptSection, schemaSection)
	}
}

// =============================================================================
// Domain-Agnostic Examples (from design doc)
// =============================================================================

// TestReResolve_DomainAgnostic_Shipping tests shipping weight calculation
func TestReResolve_DomainAgnostic_Shipping(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: `{
			"should_retry": true,
			"analysis": "Calculated total weight: 2.5 + 1.2 = 3.7",
			"corrected_parameters": {"weight": 3.7}
		}`,
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery: "ship all items",
		SourceData: map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{"wt": 2.5},
				map[string]interface{}{"wt": 1.2},
			},
		},
		StepID: "step-1",
		Capability: &EnhancedCapability{
			Name: "calculate_shipping",
			Parameters: []Parameter{
				{Name: "weight", Type: "number", Required: true},
			},
		},
		AttemptedParams: map[string]interface{}{"weight": 0},
		ErrorResponse:   "weight must be > 0",
		HTTPStatus:      400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true")
	}
	weight := result.CorrectedParameters["weight"].(float64)
	if weight < 3.6 || weight > 3.8 {
		t.Errorf("Expected weight ≈ 3.7, got: %v", weight)
	}
}

// TestReResolve_DomainAgnostic_Travel tests travel cost calculation
func TestReResolve_DomainAgnostic_Travel(t *testing.T) {
	ai := &contextualReResolverMockAI{
		response: `{
			"should_retry": true,
			"analysis": "2 adults × $100 + 1 child × $50 = $250",
			"corrected_parameters": {"total_cost": 250}
		}`,
	}
	resolver := NewContextualReResolver(ai, nil)

	execCtx := &ExecutionContext{
		UserQuery: "2 adults, 1 child",
		SourceData: map[string]interface{}{
			"adult_price": 100.0,
			"child_price": 50.0,
		},
		StepID: "step-1",
		Capability: &EnhancedCapability{
			Name: "book_tickets",
			Parameters: []Parameter{
				{Name: "total_cost", Type: "number", Required: true},
			},
		},
		AttemptedParams: map[string]interface{}{"total_cost": 0},
		ErrorResponse:   "cost must be > 0",
		HTTPStatus:      400,
	}

	result, err := resolver.ReResolve(context.Background(), execCtx)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true")
	}
	cost := result.CorrectedParameters["total_cost"].(float64)
	if cost != 250 {
		t.Errorf("Expected cost=250, got: %v", cost)
	}
}

// =============================================================================
// Model Override Tests
// =============================================================================

// reResolverOptsCapturingAI captures AIOptions for verifying model override propagation.
type reResolverOptsCapturingAI struct {
	mu           sync.Mutex
	capturedOpts []*core.AIOptions
	response     string
	err          error
}

func (m *reResolverOptsCapturingAI) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if opts != nil {
		optsCopy := *opts
		m.capturedOpts = append(m.capturedOpts, &optsCopy)
	}
	if m.err != nil {
		return nil, m.err
	}
	resp := m.response
	if resp == "" {
		resp = `{"param1": "corrected_value"}`
	}
	return &core.AIResponse{Content: resp, Model: "resolved-model", Provider: "test"}, nil
}

func (m *reResolverOptsCapturingAI) lastOpts() *core.AIOptions {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.capturedOpts) == 0 {
		return nil
	}
	return m.capturedOpts[len(m.capturedOpts)-1]
}

// TestContextualReResolver_SetModel_PropagatesModelToAIOptions verifies that
// SetModel causes opts.Model to be set on GenerateResponse calls.
func TestContextualReResolver_SetModel_PropagatesModelToAIOptions(t *testing.T) {
	t.Run("model override passed to GenerateResponse", func(t *testing.T) {
		mock := &reResolverOptsCapturingAI{}
		resolver := NewContextualReResolver(mock, nil)
		resolver.SetModel("fast")

		execCtx := &ExecutionContext{
			StepID: "step-1",
			Capability: &EnhancedCapability{
				Name:       "test_cap",
				Parameters: []Parameter{{Name: "param1", Type: "string", Required: true}},
			},
			SourceData:      map[string]interface{}{"key": "value"},
			AttemptedParams: map[string]interface{}{"param1": "old_value"},
			ErrorResponse:   "invalid param1",
			HTTPStatus:      400,
			RetryCount:      1,
		}

		_, _ = resolver.ReResolve(context.Background(), execCtx)

		opts := mock.lastOpts()
		if opts == nil {
			t.Fatal("Expected AI options to be captured")
		}
		if opts.Model != "fast" {
			t.Errorf("Expected opts.Model='fast', got %q", opts.Model)
		}
	})

	t.Run("empty model leaves opts.Model unset", func(t *testing.T) {
		mock := &reResolverOptsCapturingAI{}
		resolver := NewContextualReResolver(mock, nil)
		// Do NOT call SetModel

		execCtx := &ExecutionContext{
			StepID: "step-1",
			Capability: &EnhancedCapability{
				Name:       "test_cap",
				Parameters: []Parameter{{Name: "param1", Type: "string", Required: true}},
			},
			SourceData:      map[string]interface{}{"key": "value"},
			AttemptedParams: map[string]interface{}{"param1": "old_value"},
			ErrorResponse:   "invalid param1",
			HTTPStatus:      400,
			RetryCount:      1,
		}

		_, _ = resolver.ReResolve(context.Background(), execCtx)

		opts := mock.lastOpts()
		if opts == nil {
			t.Fatal("Expected AI options to be captured")
		}
		if opts.Model != "" {
			t.Errorf("Expected opts.Model='', got %q", opts.Model)
		}
	})
}

// TestContextualReResolver_SetModel_ErrorPath_RecordsModelInDebugPayload verifies
// that when GenerateResponse fails, the debug interaction records Model = r.model.
func TestContextualReResolver_SetModel_ErrorPath_RecordsModelInDebugPayload(t *testing.T) {
	mock := &reResolverOptsCapturingAI{err: errors.New("LLM unavailable")}
	resolver := NewContextualReResolver(mock, nil)
	resolver.SetModel("fast")

	debugStore := &capturingDebugStore{}
	resolver.SetLLMDebugStore(debugStore)

	execCtx := &ExecutionContext{
		StepID: "step-1",
		Capability: &EnhancedCapability{
			Name:       "test_cap",
			Parameters: []Parameter{{Name: "param1", Type: "string", Required: true}},
		},
		SourceData:      map[string]interface{}{"key": "value"},
		AttemptedParams: map[string]interface{}{"param1": "old_value"},
		ErrorResponse:   "invalid param1",
		HTTPStatus:      400,
		RetryCount:      1,
	}

	_, _ = resolver.ReResolve(context.Background(), execCtx)

	// Wait for async debug recording
	resolver.debugWg.Wait()
	time.Sleep(10 * time.Millisecond)

	interactions := debugStore.getInteractions()
	if len(interactions) == 0 {
		t.Fatal("Expected at least one debug interaction")
	}
	if interactions[0].Model != "fast" {
		t.Errorf("Expected debug interaction Model='fast', got %q", interactions[0].Model)
	}
	if interactions[0].Type != "semantic_retry" {
		t.Errorf("Expected Type='semantic_retry', got %q", interactions[0].Type)
	}
	if interactions[0].Success {
		t.Error("Expected Success=false for error path")
	}
}

func TestContextualReResolver_SetAIOptionsOverride_PropagatesToGenerateResponse(t *testing.T) {
	mock := &reResolverOptsCapturingAI{}
	resolver := NewContextualReResolver(mock, nil)
	resolver.SetAIOptionsOverride(&AIOptionsOverride{
		Model:           StringPtr("fast"),
		Temperature:     Float32Ptr(0),
		MaxTokens:       IntPtr(2222),
		ReasoningEffort: StringPtr("none"),
		ResponseFormat:  StringPtr("json"),
	})

	execCtx := &ExecutionContext{
		StepID: "step-1",
		Capability: &EnhancedCapability{
			Name:       "test_cap",
			Parameters: []Parameter{{Name: "param1", Type: "string", Required: true}},
		},
		SourceData:      map[string]interface{}{"key": "value"},
		AttemptedParams: map[string]interface{}{"param1": "old_value"},
		ErrorResponse:   "invalid param1",
		HTTPStatus:      400,
		RetryCount:      1,
	}

	_, _ = resolver.ReResolve(context.Background(), execCtx)
	opts := mock.lastOpts()
	if opts == nil {
		t.Fatal("expected AI options to be captured")
	}
	if opts.Model != "fast" || opts.Temperature != 0 || opts.MaxTokens != 2222 || opts.ReasoningEffort != "none" || opts.ResponseFormat != "json" {
		t.Fatalf("unexpected override propagation: %#v", opts)
	}
}
