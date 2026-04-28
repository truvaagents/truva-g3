package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExtractRequestContext_PhaseNumber validates that ExtractRequestContext
// extracts X-TruvaG3-Phase-Number header into context and that
// GetPhaseNumber(ctx) returns the correct int. Missing/invalid headers return 0.
func TestExtractRequestContext_PhaseNumber(t *testing.T) {
	tests := []struct {
		name     string
		header   string // value of X-TruvaG3-Phase-Number header ("" = absent)
		expected int
	}{
		{
			name:     "valid phase number",
			header:   "2",
			expected: 2,
		},
		{
			name:     "phase 1",
			header:   "1",
			expected: 1,
		},
		{
			name:     "missing header returns 0",
			header:   "",
			expected: 0,
		},
		{
			name:     "invalid non-numeric returns 0",
			header:   "abc",
			expected: 0,
		},
		{
			name:     "zero is rejected (must be > 0)",
			header:   "0",
			expected: 0,
		},
		{
			name:     "negative is rejected",
			header:   "-1",
			expected: 0,
		},
		{
			name:     "large valid number",
			header:   "10",
			expected: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/execute", nil)
			if tt.header != "" {
				req.Header.Set("X-TruvaG3-Phase-Number", tt.header)
			}

			ctx := ExtractRequestContext(context.Background(), req)
			got := GetPhaseNumber(ctx)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestWithPhaseNumber_RoundTrip validates that WithPhaseNumber and GetPhaseNumber
// form a correct round-trip through context.
func TestWithPhaseNumber_RoundTrip(t *testing.T) {
	ctx := context.Background()

	// Not set → 0
	assert.Equal(t, 0, GetPhaseNumber(ctx))

	// Set → correct value
	ctx = WithPhaseNumber(ctx, 3)
	assert.Equal(t, 3, GetPhaseNumber(ctx))

	// Overwrite → latest value
	ctx = WithPhaseNumber(ctx, 5)
	assert.Equal(t, 5, GetPhaseNumber(ctx))
}

// TestExtractRequestContext_AllHeaders validates that all three headers
// (request ID, step ID, phase number) are extracted together correctly.
func TestExtractRequestContext_AllHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/execute", nil)
	req.Header.Set("X-TruvaG3-Request-ID", "req-123")
	req.Header.Set("X-TruvaG3-Step-ID", "step-5")
	req.Header.Set("X-TruvaG3-Phase-Number", "2")

	ctx := ExtractRequestContext(context.Background(), req)

	assert.Equal(t, "req-123", GetRequestID(ctx))
	assert.Equal(t, "step-5", GetStepID(ctx))
	assert.Equal(t, 2, GetPhaseNumber(ctx))
}
