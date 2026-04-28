package orchestration

import (
	"context"
	"errors"
	"testing"

	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// errorAnalyzerMockAI implements core.AIClient for testing ErrorAnalyzer
// Named differently to avoid conflict with mockAIClient in hybrid_resolver_test.go
type errorAnalyzerMockAI struct {
	response string
	err      error
	lastOpts *core.AIOptions
}

func (m *errorAnalyzerMockAI) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	if opts != nil {
		optsCopy := *opts
		m.lastOpts = &optsCopy
	}
	if m.err != nil {
		return nil, m.err
	}
	return &core.AIResponse{Content: m.response}, nil
}

// TestNewErrorAnalyzer verifies default construction
func TestNewErrorAnalyzer(t *testing.T) {
	ai := &errorAnalyzerMockAI{}
	logger := &mockLogger{}

	analyzer := NewErrorAnalyzer(ai, logger)

	if analyzer.aiClient != ai {
		t.Error("Expected aiClient to be set")
	}
	if analyzer.logger == nil {
		t.Error("Expected logger to be set")
	}
	if !analyzer.enabled {
		t.Error("Expected analyzer to be enabled by default")
	}
}

// TestNewErrorAnalyzer_WithDisabled verifies disabled option
func TestNewErrorAnalyzer_WithDisabled(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil, WithErrorAnalysisEnabled(false))

	if analyzer.enabled {
		t.Error("Expected analyzer to be disabled")
	}
}

// TestAnalyzeError_NilContext returns error for nil context
func TestAnalyzeError_NilContext(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil)

	result, err := analyzer.AnalyzeError(context.Background(), nil)

	if err == nil {
		t.Error("Expected error for nil context")
	}
	if result != nil {
		t.Error("Expected nil result for nil context")
	}
}

// TestAnalyzeError_401Unauthorized fails immediately
func TestAnalyzeError_401Unauthorized(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:    401,
		ErrorResponse: "Unauthorized",
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result for 401")
	}
	if result.ShouldRetry {
		t.Error("401 should not be retryable")
	}
	if result.Reason == "" {
		t.Error("Expected reason for 401")
	}
}

// TestAnalyzeError_403Forbidden fails immediately
func TestAnalyzeError_403Forbidden(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:    403,
		ErrorResponse: "Forbidden",
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result for 403")
	}
	if result.ShouldRetry {
		t.Error("403 should not be retryable")
	}
}

// TestAnalyzeError_405MethodNotAllowed fails immediately
func TestAnalyzeError_405MethodNotAllowed(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:    405,
		ErrorResponse: "Method Not Allowed",
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result for 405")
	}
	if result.ShouldRetry {
		t.Error("405 should not be retryable")
	}
}

// TestAnalyzeError_429TooManyRequests delegates to resilience
func TestAnalyzeError_429TooManyRequests(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:    429,
		ErrorResponse: "Too Many Requests",
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result != nil {
		t.Error("429 should return nil (delegate to resilience module)")
	}
}

// TestAnalyzeError_503ServiceUnavailable triggers LLM analysis (may contain semantic errors)
// Note: 503 errors from tools often contain semantic information like "location not found"
// that LLM can analyze to suggest corrections, unlike pure infrastructure failures.
func TestAnalyzeError_503ServiceUnavailable(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{"should_retry": false, "reason": "Service timeout - infrastructure issue, not fixable with parameter changes"}`,
	}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:    503,
		ErrorResponse: "Service Unavailable",
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("503 should trigger LLM analysis, not delegate to resilience")
	}
	// LLM correctly identifies this as not retryable
	if result.ShouldRetry {
		t.Error("Expected ShouldRetry=false for infrastructure timeout")
	}
}

// TestAnalyzeError_503WithSemanticError shows LLM can suggest corrections for semantic 503 errors
// This is why 503 is not delegated to resilience - tools may return 503 with fixable error messages.
func TestAnalyzeError_503WithSemanticError(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{"should_retry": true, "reason": "Location typo detected", "suggested_changes": {"location": "Seoul, South Korea"}}`,
	}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:      503,
		ErrorResponse:   `{"error": "Location 'Soul' not found", "code": "LOCATION_NOT_FOUND"}`,
		OriginalRequest: map[string]interface{}{"location": "Soul"},
		UserQuery:       "Get weather in Seoul",
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("503 with semantic error should trigger LLM analysis")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true for typo that can be fixed")
	}
	if result.SuggestedChanges["location"] != "Seoul, South Korea" {
		t.Errorf("Expected suggested location='Seoul, South Korea', got %v", result.SuggestedChanges["location"])
	}
}

func TestErrorAnalyzer_SetAIOptionsOverride_PropagatesToGenerateResponse(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{"should_retry": true, "reason": "fixable"}`,
	}
	analyzer := NewErrorAnalyzer(ai, nil)
	analyzer.SetAIOptionsOverride(&AIOptionsOverride{
		Model:           StringPtr("fast"),
		Temperature:     Float32Ptr(0),
		MaxTokens:       IntPtr(900),
		ReasoningEffort: StringPtr("none"),
		ResponseFormat:  StringPtr("json"),
	})

	_, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:            400,
		ErrorResponse:         "invalid value",
		OriginalRequest:       map[string]interface{}{"foo": "bar"},
		UserQuery:             "fix this",
		CapabilityName:        "test-capability",
		CapabilityDescription: "test",
	})
	if err != nil {
		t.Fatalf("AnalyzeError failed: %v", err)
	}

	if ai.lastOpts == nil {
		t.Fatal("expected AI options to be captured")
	}
	if ai.lastOpts.Model != "fast" || ai.lastOpts.MaxTokens != 900 || ai.lastOpts.Temperature != 0 || ai.lastOpts.ReasoningEffort != "none" || ai.lastOpts.ResponseFormat != "json" {
		t.Fatalf("unexpected override propagation: %#v", ai.lastOpts)
	}
}

// TestAnalyzeError_AllResilienceCodes verifies all codes delegated to resilience
// Note: 503 is intentionally NOT in this list - it triggers LLM analysis instead
// because tool 503 responses often contain semantic error information.
func TestAnalyzeError_AllResilienceCodes(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, &mockLogger{})
	resilienceCodes := []int{408, 429, 500, 502, 504} // 503 removed - now goes to LLM

	for _, code := range resilienceCodes {
		result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
			HTTPStatus: code,
		})

		if err != nil {
			t.Errorf("HTTP %d: unexpected error: %v", code, err)
		}
		if result != nil {
			t.Errorf("HTTP %d: should delegate to resilience (return nil)", code)
		}
	}
}

// TestAnalyzeError_400BadRequest triggers LLM analysis
func TestAnalyzeError_400BadRequest(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{"should_retry": true, "reason": "Typo in city name", "suggested_changes": {"city": "Tokyo"}}`,
	}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:            400,
		ErrorResponse:         `{"error": "City 'Tokio' not found"}`,
		OriginalRequest:       map[string]interface{}{"city": "Tokio"},
		UserQuery:             "What's the weather in Tokyo?",
		CapabilityName:        "get_weather",
		CapabilityDescription: "Get weather for a city",
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result for 400")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true for typo fix")
	}
	if result.SuggestedChanges["city"] != "Tokyo" {
		t.Errorf("Expected suggested city=Tokyo, got %v", result.SuggestedChanges["city"])
	}
}

// TestAnalyzeError_404NotFound triggers LLM analysis
func TestAnalyzeError_404NotFound(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{"should_retry": false, "reason": "Resource genuinely does not exist"}`,
	}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:      404,
		ErrorResponse:   `{"error": "Stock XYZABC not found"}`,
		OriginalRequest: map[string]interface{}{"symbol": "XYZABC"},
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result for 404")
	}
	if result.ShouldRetry {
		t.Error("Expected ShouldRetry=false for non-existent resource")
	}
}

// TestAnalyzeError_409Conflict triggers LLM analysis (per design: might be fixable)
func TestAnalyzeError_409Conflict(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{"should_retry": true, "reason": "Resource version conflict, can retry with updated version", "suggested_changes": {"version": "v2"}}`,
	}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:      409,
		ErrorResponse:   `{"error": "Conflict: resource version mismatch"}`,
		OriginalRequest: map[string]interface{}{"version": "v1"},
		CapabilityName:  "update_resource",
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result for 409")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true for conflict that can be resolved")
	}
	if result.SuggestedChanges["version"] != "v2" {
		t.Errorf("Expected suggested version=v2, got %v", result.SuggestedChanges["version"])
	}
}

// TestAnalyzeError_422UnprocessableEntity triggers LLM analysis (per design: might be fixable)
func TestAnalyzeError_422UnprocessableEntity(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{"should_retry": true, "reason": "Invalid date format", "suggested_changes": {"date": "2024-01-15"}}`,
	}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:      422,
		ErrorResponse:   `{"error": "Invalid date format: expected YYYY-MM-DD"}`,
		OriginalRequest: map[string]interface{}{"date": "15/01/2024"},
		CapabilityName:  "schedule_meeting",
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result for 422")
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true for format error that can be fixed")
	}
	if result.SuggestedChanges["date"] != "2024-01-15" {
		t.Errorf("Expected suggested date=2024-01-15, got %v", result.SuggestedChanges["date"])
	}
}

// TestAnalyzeError_ContextCancelled returns context error before LLM call
func TestAnalyzeError_ContextCancelled(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{"should_retry": true}`, // Should never be called
	}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := analyzer.AnalyzeError(ctx, &ErrorAnalysisContext{
		HTTPStatus: 400, // Would normally trigger LLM analysis
	})

	if err == nil {
		t.Error("Expected error for cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled error, got: %v", err)
	}
	if result != nil {
		t.Error("Expected nil result for cancelled context")
	}
}

// TestAnalyzeError_LLMDisabled returns default result
func TestAnalyzeError_LLMDisabled(t *testing.T) {
	ai := &errorAnalyzerMockAI{response: `{"should_retry": true}`}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{}, WithErrorAnalysisEnabled(false))

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus: 400,
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result when LLM disabled")
	}
	if result.ShouldRetry {
		t.Error("Expected ShouldRetry=false when LLM disabled")
	}
}

// TestAnalyzeError_NilAIClient returns default result
func TestAnalyzeError_NilAIClient(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus: 400,
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result when AI client nil")
	}
	if result.ShouldRetry {
		t.Error("Expected ShouldRetry=false when AI client nil")
	}
}

// TestAnalyzeError_LLMError returns error
func TestAnalyzeError_LLMError(t *testing.T) {
	ai := &errorAnalyzerMockAI{err: errors.New("LLM unavailable")}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus: 400,
	})

	if err == nil {
		t.Error("Expected error when LLM fails")
	}
	if result != nil {
		t.Error("Expected nil result when LLM fails")
	}
}

// TestParseAnalysisResponse_ValidJSON parses clean JSON
func TestParseAnalysisResponse_ValidJSON(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil)

	result, err := analyzer.parseAnalysisResponse(`{"should_retry": true, "reason": "Typo", "suggested_changes": {"city": "Tokyo"}}`)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true")
	}
	if result.Reason != "Typo" {
		t.Errorf("Expected reason='Typo', got '%s'", result.Reason)
	}
	if result.SuggestedChanges["city"] != "Tokyo" {
		t.Errorf("Expected city=Tokyo, got %v", result.SuggestedChanges["city"])
	}
}

// TestParseAnalysisResponse_MarkdownCodeBlock handles markdown wrapped JSON
func TestParseAnalysisResponse_MarkdownCodeBlock(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil)

	response := "```json\n{\"should_retry\": true, \"reason\": \"Test\"}\n```"
	result, err := analyzer.parseAnalysisResponse(response)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true from markdown block")
	}
}

// TestParseAnalysisResponse_ExtraText handles JSON with surrounding text
func TestParseAnalysisResponse_ExtraText(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil)

	response := `Here is my analysis:
{"should_retry": false, "reason": "Not fixable"}
I hope this helps!`

	result, err := analyzer.parseAnalysisResponse(response)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result.ShouldRetry {
		t.Error("Expected ShouldRetry=false")
	}
	if result.Reason != "Not fixable" {
		t.Errorf("Expected reason='Not fixable', got '%s'", result.Reason)
	}
}

// TestParseAnalysisResponse_NoJSON returns error
func TestParseAnalysisResponse_NoJSON(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil)

	result, err := analyzer.parseAnalysisResponse("This response has no JSON at all")

	if err == nil {
		t.Error("Expected error for missing JSON")
	}
	if result != nil {
		t.Error("Expected nil result for missing JSON")
	}
}

// TestParseAnalysisResponse_InvalidJSON returns error
func TestParseAnalysisResponse_InvalidJSON(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil)

	result, err := analyzer.parseAnalysisResponse(`{"should_retry": true, "reason":}`)

	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
	if result != nil {
		t.Error("Expected nil result for invalid JSON")
	}
}

// TestParseAnalysisResponse_NestedJSON handles nested objects correctly
func TestParseAnalysisResponse_NestedJSON(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil)

	response := `{"should_retry": true, "reason": "Fix params", "suggested_changes": {"location": {"city": "Tokyo", "country": "Japan"}}}`
	result, err := analyzer.parseAnalysisResponse(response)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !result.ShouldRetry {
		t.Error("Expected ShouldRetry=true")
	}
	if result.SuggestedChanges["location"] == nil {
		t.Error("Expected nested location object")
	}
}

// TestFindJSONEndSimple handles various JSON structures
func TestFindJSONEndSimple(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		start    int
		expected int
	}{
		{"simple object", `{"key": "value"}`, 0, 16},
		{"nested object", `{"outer": {"inner": "value"}}`, 0, 29},
		{"with string braces", `{"text": "has { and } in it"}`, 0, 29},
		{"with escaped quotes", `{"text": "has \"quotes\""}`, 0, 26},
		{"array value", `{"arr": [1, 2, 3]}`, 0, 18},
		{"prefix text", `text{"key": "value"}more`, 4, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findJSONEndSimple(tt.input, tt.start)
			if result != tt.expected {
				t.Errorf("findJSONEndSimple(%q, %d) = %d, want %d", tt.input, tt.start, result, tt.expected)
			}
		})
	}
}

// TestSetLogger sets logger correctly
func TestErrorAnalyzer_SetLogger(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil)

	logger := &mockLogger{}
	analyzer.SetLogger(logger)

	if analyzer.logger == nil {
		t.Error("Expected logger to be set")
	}
}

// TestSetLogger_Nil clears logger
func TestErrorAnalyzer_SetLogger_Nil(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, &mockLogger{})
	analyzer.SetLogger(nil)

	if analyzer.logger != nil {
		t.Error("Expected logger to be nil")
	}
}

// TestEnable toggles enabled state
func TestErrorAnalyzer_Enable(t *testing.T) {
	analyzer := NewErrorAnalyzer(&errorAnalyzerMockAI{}, nil)

	if !analyzer.IsEnabled() {
		t.Error("Expected enabled by default with AI client")
	}

	analyzer.Enable(false)
	if analyzer.IsEnabled() {
		t.Error("Expected disabled after Enable(false)")
	}

	analyzer.Enable(true)
	if !analyzer.IsEnabled() {
		t.Error("Expected enabled after Enable(true)")
	}
}

// TestIsEnabled_RequiresAIClient checks both conditions
func TestIsEnabled_RequiresAIClient(t *testing.T) {
	// Enabled=true but no AI client
	analyzer := NewErrorAnalyzer(nil, nil)
	if analyzer.IsEnabled() {
		t.Error("Expected disabled without AI client")
	}

	// Enabled=true with AI client
	analyzer = NewErrorAnalyzer(&errorAnalyzerMockAI{}, nil)
	if !analyzer.IsEnabled() {
		t.Error("Expected enabled with AI client")
	}

	// Enabled=false with AI client
	analyzer = NewErrorAnalyzer(&errorAnalyzerMockAI{}, nil, WithErrorAnalysisEnabled(false))
	if analyzer.IsEnabled() {
		t.Error("Expected disabled when explicitly disabled")
	}
}

// TestBuildAnalysisPrompt verifies prompt structure
func TestBuildAnalysisPrompt(t *testing.T) {
	analyzer := NewErrorAnalyzer(nil, nil)

	errCtx := &ErrorAnalysisContext{
		HTTPStatus:            400,
		ErrorResponse:         `{"error": "City not found"}`,
		OriginalRequest:       map[string]interface{}{"city": "Tokio"},
		UserQuery:             "Weather in Tokyo",
		CapabilityName:        "get_weather",
		CapabilityDescription: "Get weather for a city",
	}

	prompt := analyzer.buildAnalysisPrompt(errCtx)

	// Check key sections are present
	if !contains(prompt, "get_weather") {
		t.Error("Expected capability name in prompt")
	}
	if !contains(prompt, "Get weather for a city") {
		t.Error("Expected capability description in prompt")
	}
	if !contains(prompt, "Tokio") {
		t.Error("Expected original request in prompt")
	}
	if !contains(prompt, "City not found") {
		t.Error("Expected error response in prompt")
	}
	if !contains(prompt, "Weather in Tokyo") {
		t.Error("Expected user query in prompt")
	}
	if !contains(prompt, "400") {
		t.Error("Expected HTTP status in prompt")
	}
	if !contains(prompt, "should_retry") {
		t.Error("Expected response format in prompt")
	}
}

// TestTruncateString verifies string truncation
func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is longer than ten", 10, "this is lo..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		result := truncateString(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

// TestAnalyzeError_503TransientError verifies that transient errors (timeouts, service unavailable)
// are correctly identified by LLM with is_transient_error=true
func TestAnalyzeError_503TransientError(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{
			"should_retry": false,
			"reason": "Service timeout - this is a temporary infrastructure issue",
			"is_transient_error": true
		}`,
	}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:    503,
		ErrorResponse: `{"error": "request failed: net/http: request canceled (Client.Timeout exceeded)"}`,
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result for 503 transient error")
	}
	if result.ShouldRetry {
		t.Error("Expected ShouldRetry=false for transient error (not a parameter issue)")
	}
	if !result.IsTransientError {
		t.Error("Expected IsTransientError=true for timeout error")
	}
}

// TestAnalyzeError_503NonTransientError verifies that non-transient 503 errors
// (semantic errors like "location not found") are NOT marked as transient
func TestAnalyzeError_503NonTransientError(t *testing.T) {
	ai := &errorAnalyzerMockAI{
		response: `{
			"should_retry": false,
			"reason": "Location 'Narnia' does not exist",
			"is_transient_error": false
		}`,
	}
	analyzer := NewErrorAnalyzer(ai, &mockLogger{})

	result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
		HTTPStatus:      503,
		ErrorResponse:   `{"error": "Location 'Narnia' not found", "code": "LOCATION_NOT_FOUND"}`,
		OriginalRequest: map[string]interface{}{"location": "Narnia"},
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result for 503 semantic error")
	}
	if result.IsTransientError {
		t.Error("Expected IsTransientError=false for semantic error (location doesn't exist)")
	}
}

// TestAnalyzeError_TransientErrorWithResilienceRetry verifies that when LLM identifies
// a transient error, the executor should continue with resilience retry (same payload + backoff)
// rather than breaking out of the retry loop
func TestAnalyzeError_TransientErrorField(t *testing.T) {
	tests := []struct {
		name              string
		response          string
		expectTransient   bool
		expectShouldRetry bool
	}{
		{
			name: "timeout_error",
			response: `{
				"should_retry": false,
				"reason": "Request timed out - temporary issue",
				"is_transient_error": true
			}`,
			expectTransient:   true,
			expectShouldRetry: false,
		},
		{
			name: "service_unavailable",
			response: `{
				"should_retry": false,
				"reason": "Service is temporarily unavailable",
				"is_transient_error": true
			}`,
			expectTransient:   true,
			expectShouldRetry: false,
		},
		{
			name: "connection_refused",
			response: `{
				"should_retry": false,
				"reason": "Connection refused - service may be restarting",
				"is_transient_error": true
			}`,
			expectTransient:   true,
			expectShouldRetry: false,
		},
		{
			name: "semantic_error_not_transient",
			response: `{
				"should_retry": false,
				"reason": "Invalid country name - cannot be fixed",
				"is_transient_error": false
			}`,
			expectTransient:   false,
			expectShouldRetry: false,
		},
		{
			name: "fixable_typo",
			response: `{
				"should_retry": true,
				"reason": "Typo detected, can be fixed",
				"suggested_changes": {"city": "Tokyo"},
				"is_transient_error": false
			}`,
			expectTransient:   false,
			expectShouldRetry: true,
		},
		{
			name: "transient_retry_same_params",
			response: `{
				"should_retry": true,
				"reason": "Service timeout - retry with same parameters may succeed",
				"suggested_changes": {},
				"is_transient_error": true
			}`,
			expectTransient:   true,
			expectShouldRetry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ai := &errorAnalyzerMockAI{response: tt.response}
			analyzer := NewErrorAnalyzer(ai, &mockLogger{})

			result, err := analyzer.AnalyzeError(context.Background(), &ErrorAnalysisContext{
				HTTPStatus:    503,
				ErrorResponse: "test error",
			})

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if result == nil {
				t.Fatal("Expected result")
			}
			if result.IsTransientError != tt.expectTransient {
				t.Errorf("Expected IsTransientError=%v, got %v", tt.expectTransient, result.IsTransientError)
			}
			if result.ShouldRetry != tt.expectShouldRetry {
				t.Errorf("Expected ShouldRetry=%v, got %v", tt.expectShouldRetry, result.ShouldRetry)
			}
		})
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || contains(s[1:], substr)))
}

// =============================================================================
// Phase 9: ErrorAnalyzer LLM Call Recording — debug store integration
// Uses mockLLMDebugStore from llm_call_recorder_adapter_test.go (same package).
// HTTP status 400 is used to force LLM analysis (not routed by heuristics).
// =============================================================================

// TestErrorAnalyzer_DebugStore_RecordsSuccessInteraction verifies that a successful
// LLM error analysis call records an error_analysis interaction with Success=true.
func TestErrorAnalyzer_DebugStore_RecordsSuccessInteraction(t *testing.T) {
	debugStore := &mockLLMDebugStore{}
	mockAI := &errorAnalyzerMockAI{
		response: `{"should_retry": false, "reason": "invalid input parameter"}`,
	}
	analyzer := NewErrorAnalyzer(mockAI, nil)
	analyzer.SetLLMDebugStore(debugStore)

	ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-ea-success")
	errCtx := &ErrorAnalysisContext{
		HTTPStatus:     400,
		ErrorResponse:  `{"error": "invalid city name"}`,
		CapabilityName: "geocoding",
		StepID:         "step-5",
	}

	result, err := analyzer.AnalyzeError(ctx, errCtx)
	if err != nil {
		t.Fatalf("AnalyzeError returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = analyzer.Shutdown(ctx2)

	if len(debugStore.interactions) != 1 {
		t.Fatalf("expected 1 debug interaction, got %d", len(debugStore.interactions))
	}

	i := debugStore.interactions[0]
	if i.Type != "error_analysis" {
		t.Errorf("expected type 'error_analysis', got %q", i.Type)
	}
	if !i.Success {
		t.Error("expected Success=true for successful analysis")
	}
	if i.StepID != "step-5" {
		t.Errorf("expected StepID='step-5', got %q", i.StepID)
	}
	if debugStore.requestIDs[0] != "req-ea-success" {
		t.Errorf("expected requestID='req-ea-success', got %q", debugStore.requestIDs[0])
	}
}

// TestErrorAnalyzer_DebugStore_RecordsFailureInteraction verifies that when the LLM call
// fails, an error_analysis interaction with Success=false is recorded.
func TestErrorAnalyzer_DebugStore_RecordsFailureInteraction(t *testing.T) {
	debugStore := &mockLLMDebugStore{}
	mockAI := &errorAnalyzerMockAI{
		err: errors.New("LLM timeout"),
	}
	analyzer := NewErrorAnalyzer(mockAI, nil)
	analyzer.SetLLMDebugStore(debugStore)

	ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-ea-fail")
	errCtx := &ErrorAnalysisContext{
		HTTPStatus:     400,
		ErrorResponse:  `{"error": "bad request"}`,
		CapabilityName: "currency-tool",
		StepID:         "step-7",
	}

	_, _ = analyzer.AnalyzeError(ctx, errCtx)

	ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = analyzer.Shutdown(ctx2)

	if len(debugStore.interactions) != 1 {
		t.Fatalf("expected 1 debug interaction, got %d", len(debugStore.interactions))
	}

	i := debugStore.interactions[0]
	if i.Type != "error_analysis" {
		t.Errorf("expected type 'error_analysis', got %q", i.Type)
	}
	if i.Success {
		t.Error("expected Success=false for LLM failure")
	}
	if i.Error == "" {
		t.Error("expected Error field to be populated")
	}
	if i.StepID != "step-7" {
		t.Errorf("expected StepID='step-7', got %q", i.StepID)
	}
}

// TestErrorAnalyzer_DebugStore_StepIDPopulated verifies that the StepID from
// ErrorAnalysisContext flows correctly into the recorded LLMInteraction.
// This tests the Phase 9 executor.go change that passes step.StepID into the context.
func TestErrorAnalyzer_DebugStore_StepIDPopulated(t *testing.T) {
	debugStore := &mockLLMDebugStore{}
	mockAI := &errorAnalyzerMockAI{
		response: `{"should_retry": true, "reason": "typo in city name", "suggested_changes": {"city": "Tokyo"}}`,
	}
	analyzer := NewErrorAnalyzer(mockAI, nil)
	analyzer.SetLLMDebugStore(debugStore)

	ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-ea-stepid")
	errCtx := &ErrorAnalysisContext{
		HTTPStatus:     400,
		ErrorResponse:  `{"error": "city not found: Tokio"}`,
		CapabilityName: "geocoding",
		StepID:         "step-12",
	}

	_, _ = analyzer.AnalyzeError(ctx, errCtx)

	ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = analyzer.Shutdown(ctx2)

	if len(debugStore.interactions) != 1 {
		t.Fatalf("expected 1 debug interaction, got %d", len(debugStore.interactions))
	}
	if debugStore.interactions[0].StepID != "step-12" {
		t.Errorf("expected StepID='step-12', got %q", debugStore.interactions[0].StepID)
	}
}
