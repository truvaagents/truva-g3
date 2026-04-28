package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// ============================================================================
// Tests for Intelligent Error Handling Protocol Types
// ============================================================================
//
// These tests verify the core protocol types used for tool-to-agent
// error communication. See: docs/INTELLIGENT_ERROR_HANDLING_ARCHITECTURE.md

func TestHTTPStatusForCategory(t *testing.T) {
	tests := []struct {
		name     string
		category ErrorCategory
		want     int
	}{
		{
			name:     "InputError maps to 400 Bad Request",
			category: CategoryInputError,
			want:     http.StatusBadRequest,
		},
		{
			name:     "NotFound maps to 404 Not Found",
			category: CategoryNotFound,
			want:     http.StatusNotFound,
		},
		{
			name:     "AuthError maps to 401 Unauthorized",
			category: CategoryAuthError,
			want:     http.StatusUnauthorized,
		},
		{
			name:     "RateLimit maps to 429 Too Many Requests",
			category: CategoryRateLimit,
			want:     http.StatusTooManyRequests,
		},
		{
			name:     "ServiceError maps to 503 Service Unavailable",
			category: CategoryServiceError,
			want:     http.StatusServiceUnavailable,
		},
		{
			name:     "Unknown category maps to 500 Internal Server Error",
			category: ErrorCategory("UNKNOWN_CATEGORY"),
			want:     http.StatusInternalServerError,
		},
		{
			name:     "Empty category maps to 500 Internal Server Error",
			category: ErrorCategory(""),
			want:     http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HTTPStatusForCategory(tt.category)
			if got != tt.want {
				t.Errorf("HTTPStatusForCategory(%q) = %d, want %d", tt.category, got, tt.want)
			}
		})
	}
}

func TestToolError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *ToolError
		want string
	}{
		{
			name: "formats code and message correctly",
			err: &ToolError{
				Code:    "LOCATION_NOT_FOUND",
				Message: "City 'Flower Mound, TX' not found",
			},
			want: "[LOCATION_NOT_FOUND] City 'Flower Mound, TX' not found",
		},
		{
			name: "handles empty code",
			err: &ToolError{
				Code:    "",
				Message: "Something went wrong",
			},
			want: "[] Something went wrong",
		},
		{
			name: "handles empty message",
			err: &ToolError{
				Code:    "ERROR",
				Message: "",
			},
			want: "[ERROR] ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("ToolError.Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolError_ImplementsErrorInterface(t *testing.T) {
	// Compile-time check that ToolError implements error interface
	var _ error = &ToolError{}

	err := &ToolError{
		Code:    "TEST_ERROR",
		Message: "Test message",
	}

	// Should be usable as an error
	var genericError error = err
	if genericError.Error() != "[TEST_ERROR] Test message" {
		t.Errorf("ToolError should implement error interface correctly")
	}
}

func TestToolError_JSONSerialization(t *testing.T) {
	original := &ToolError{
		Code:      "LOCATION_NOT_FOUND",
		Message:   "City not found",
		Category:  CategoryNotFound,
		Retryable: true,
		Details: map[string]string{
			"original_location": "Flower Mound, TX",
			"hint":              "Use 'City, Country' format",
		},
	}

	// Marshal to JSON
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal ToolError: %v", err)
	}

	// Verify JSON structure
	var jsonMap map[string]interface{}
	if err := json.Unmarshal(data, &jsonMap); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	// Check required fields
	if jsonMap["code"] != "LOCATION_NOT_FOUND" {
		t.Errorf("Expected code 'LOCATION_NOT_FOUND', got %v", jsonMap["code"])
	}
	if jsonMap["category"] != "NOT_FOUND" {
		t.Errorf("Expected category 'NOT_FOUND', got %v", jsonMap["category"])
	}
	if jsonMap["retryable"] != true {
		t.Errorf("Expected retryable true, got %v", jsonMap["retryable"])
	}

	// Unmarshal back to struct
	var decoded ToolError
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal to ToolError: %v", err)
	}

	if decoded.Code != original.Code {
		t.Errorf("Code mismatch: got %q, want %q", decoded.Code, original.Code)
	}
	if decoded.Category != original.Category {
		t.Errorf("Category mismatch: got %q, want %q", decoded.Category, original.Category)
	}
	if decoded.Retryable != original.Retryable {
		t.Errorf("Retryable mismatch: got %v, want %v", decoded.Retryable, original.Retryable)
	}
	if decoded.Details["hint"] != original.Details["hint"] {
		t.Errorf("Details hint mismatch: got %q, want %q", decoded.Details["hint"], original.Details["hint"])
	}
}

func TestToolError_JSONOmitsEmptyDetails(t *testing.T) {
	err := &ToolError{
		Code:     "ERROR",
		Message:  "Test",
		Category: CategoryServiceError,
		// Details intentionally nil
	}

	data, _ := json.Marshal(err)
	var jsonMap map[string]interface{}
	json.Unmarshal(data, &jsonMap)

	// Details should be omitted when nil (omitempty tag)
	if _, exists := jsonMap["details"]; exists {
		t.Errorf("Expected details to be omitted when nil, but it exists in JSON")
	}
}

func TestToolResponse_SuccessCase(t *testing.T) {
	response := ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"temperature": 25.5,
			"humidity":    65,
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal ToolResponse: %v", err)
	}

	var decoded ToolResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal ToolResponse: %v", err)
	}

	if !decoded.Success {
		t.Errorf("Expected Success=true")
	}
	if decoded.Error != nil {
		t.Errorf("Expected Error=nil for success response")
	}
	if decoded.Data == nil {
		t.Errorf("Expected Data to be present")
	}
}

func TestToolResponse_ErrorCase(t *testing.T) {
	response := ToolResponse{
		Success: false,
		Error: &ToolError{
			Code:      "LOCATION_NOT_FOUND",
			Message:   "City not found",
			Category:  CategoryNotFound,
			Retryable: true,
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal ToolResponse: %v", err)
	}

	var decoded ToolResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal ToolResponse: %v", err)
	}

	if decoded.Success {
		t.Errorf("Expected Success=false")
	}
	if decoded.Error == nil {
		t.Errorf("Expected Error to be present")
	}
	if decoded.Error.Code != "LOCATION_NOT_FOUND" {
		t.Errorf("Expected error code 'LOCATION_NOT_FOUND', got %q", decoded.Error.Code)
	}
}

func TestToolResponse_OmitsEmptyFields(t *testing.T) {
	// Success response should omit error
	successResp := ToolResponse{
		Success: true,
		Data:    "test data",
	}

	data, _ := json.Marshal(successResp)
	var successMap map[string]interface{}
	json.Unmarshal(data, &successMap)

	if _, exists := successMap["error"]; exists {
		t.Errorf("Expected error to be omitted in success response")
	}

	// Error response should omit data
	errorResp := ToolResponse{
		Success: false,
		Error: &ToolError{
			Code:    "ERROR",
			Message: "Test",
		},
	}

	data, _ = json.Marshal(errorResp)
	var errorMap map[string]interface{}
	json.Unmarshal(data, &errorMap)

	if _, exists := errorMap["data"]; exists {
		t.Errorf("Expected data to be omitted in error response")
	}
}

// ============================================================================
// ClassifyUpstreamError Tests
// ============================================================================

func TestClassifyUpstreamError(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		wantHTTPStatus int
		wantCategory   ErrorCategory
		wantRetryable  bool
		wantCode       string
	}{
		// Amadeus "error NNN" format (the bug this fixes)
		{
			name:           "Amadeus 400 — parameter error",
			err:            errors.New("Amadeus API error 400: Provider Error - INVALID PROPERTY CODE"),
			wantHTTPStatus: http.StatusBadRequest,
			wantCategory:   CategoryInputError,
			wantRetryable:  false,
			wantCode:       "API_ERROR",
		},
		{
			name:           "Amadeus 429 — rate limit",
			err:            errors.New("Amadeus API error 429: The network rate limit is exceeded, please try again later"),
			wantHTTPStatus: http.StatusTooManyRequests,
			wantCategory:   CategoryRateLimit,
			wantRetryable:  true,
			wantCode:       "RATE_LIMIT",
		},
		{
			name:           "Amadeus 401 — unauthorized",
			err:            errors.New("Amadeus API error 401: unauthorized"),
			wantHTTPStatus: http.StatusUnauthorized,
			wantCategory:   CategoryAuthError,
			wantRetryable:  false,
			wantCode:       "AUTH_ERROR",
		},
		{
			name:           "Amadeus 403 — forbidden",
			err:            errors.New("Amadeus API error 403: access denied"),
			wantHTTPStatus: http.StatusForbidden,
			wantCategory:   CategoryAuthError,
			wantRetryable:  false,
			wantCode:       "AUTH_ERROR",
		},
		// "status NNN" format (existing tools — should still work)
		{
			name:           "status 400 format — bad request",
			err:            errors.New("API returned status 400: bad request"),
			wantHTTPStatus: http.StatusBadRequest,
			wantCategory:   CategoryInputError,
			wantRetryable:  false,
			wantCode:       "API_ERROR",
		},
		{
			name:           "status 502 format — gateway error",
			err:            errors.New("API returned status 502: gateway error"),
			wantHTTPStatus: http.StatusBadGateway,
			wantCategory:   CategoryServiceError,
			wantRetryable:  true,
			wantCode:       "SERVICE_ERROR",
		},
		{
			name:           "status 422 format — unprocessable",
			err:            errors.New("API error (status 422): invalid param"),
			wantHTTPStatus: http.StatusBadRequest,
			wantCategory:   CategoryInputError,
			wantRetryable:  false,
			wantCode:       "API_ERROR",
		},
		{
			name:           "status 404 format — not found",
			err:            errors.New("API returned status 404: resource not found"),
			wantHTTPStatus: http.StatusBadRequest,
			wantCategory:   CategoryInputError,
			wantRetryable:  false,
			wantCode:       "API_ERROR",
		},
		{
			name:           "status 409 format — conflict",
			err:            errors.New("API returned status 409: conflict"),
			wantHTTPStatus: http.StatusBadRequest,
			wantCategory:   CategoryInputError,
			wantRetryable:  false,
			wantCode:       "API_ERROR",
		},
		{
			name:           "status 500 format — internal server error",
			err:            errors.New("API returned status 500: internal server error"),
			wantHTTPStatus: http.StatusBadGateway,
			wantCategory:   CategoryServiceError,
			wantRetryable:  true,
			wantCode:       "SERVICE_ERROR",
		},
		// Edge cases
		{
			name:           "no status code in message — defaults to 502",
			err:            errors.New("connection refused"),
			wantHTTPStatus: http.StatusBadGateway,
			wantCategory:   CategoryServiceError,
			wantRetryable:  true,
			wantCode:       "SERVICE_ERROR",
		},
		{
			name:           "nil error — defaults to 502",
			err:            nil,
			wantHTTPStatus: http.StatusBadGateway,
			wantCategory:   CategoryServiceError,
			wantRetryable:  true,
			wantCode:       "SERVICE_ERROR",
		},
		{
			name:           "first match wins — error 400 before status 502",
			err:            errors.New("error 400: upstream returned status 502"),
			wantHTTPStatus: http.StatusBadRequest,
			wantCategory:   CategoryInputError,
			wantRetryable:  false,
			wantCode:       "API_ERROR",
		},
		// "code: NNN" format
		{
			name:           "code: 429 format",
			err:            errors.New("upstream returned code: 429"),
			wantHTTPStatus: http.StatusTooManyRequests,
			wantCategory:   CategoryRateLimit,
			wantRetryable:  true,
			wantCode:       "RATE_LIMIT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyUpstreamError(tt.err)
			if got.HTTPStatus != tt.wantHTTPStatus {
				t.Errorf("HTTPStatus = %d, want %d", got.HTTPStatus, tt.wantHTTPStatus)
			}
			if got.Category != tt.wantCategory {
				t.Errorf("Category = %q, want %q", got.Category, tt.wantCategory)
			}
			if got.Retryable != tt.wantRetryable {
				t.Errorf("Retryable = %v, want %v", got.Retryable, tt.wantRetryable)
			}
			if got.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", got.Code, tt.wantCode)
			}
		})
	}
}

func TestErrorCategory_StringValues(t *testing.T) {
	// Verify the string values match what the architecture doc specifies
	tests := []struct {
		category ErrorCategory
		want     string
	}{
		{CategoryInputError, "INPUT_ERROR"},
		{CategoryNotFound, "NOT_FOUND"},
		{CategoryRateLimit, "RATE_LIMIT"},
		{CategoryAuthError, "AUTH_ERROR"},
		{CategoryServiceError, "SERVICE_ERROR"},
	}

	for _, tt := range tests {
		if string(tt.category) != tt.want {
			t.Errorf("ErrorCategory constant %v = %q, want %q", tt.category, string(tt.category), tt.want)
		}
	}
}
