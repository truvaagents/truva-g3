package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestValidateConversationID(t *testing.T) {
	t.Parallel()

	maxLengthValue := strings.Repeat("a", MaxConversationIDLength)
	tests := []struct {
		name   string
		value  string
		reason ConversationIDValidationReason
	}{
		{
			name:   "opaque orchestration ID",
			value:  "conversation:orch-1784984026261441029",
			reason: ConversationIDValidationNone,
		},
		{
			name:   "all allowed visible ASCII ranges",
			value:  "!#$%&'()*+-./09:<=>?@AZ[]^_az{|}~",
			reason: ConversationIDValidationNone,
		},
		{
			name:   "maximum length",
			value:  maxLengthValue,
			reason: ConversationIDValidationNone,
		},
		{
			name:   "empty",
			value:  "",
			reason: ConversationIDValidationEmpty,
		},
		{
			name:   "too long",
			value:  maxLengthValue + "a",
			reason: ConversationIDValidationTooLong,
		},
		{
			name:   "invalid UTF-8",
			value:  string([]byte{0xff}),
			reason: ConversationIDValidationInvalidUTF8,
		},
		{
			name:   "space",
			value:  "conversation id",
			reason: ConversationIDValidationInvalidCharacter,
		},
		{
			name:   "double quote",
			value:  `conversation"id`,
			reason: ConversationIDValidationInvalidCharacter,
		},
		{
			name:   "comma",
			value:  "conversation,id",
			reason: ConversationIDValidationInvalidCharacter,
		},
		{
			name:   "semicolon",
			value:  "conversation;id",
			reason: ConversationIDValidationInvalidCharacter,
		},
		{
			name:   "backslash",
			value:  `conversation\id`,
			reason: ConversationIDValidationInvalidCharacter,
		},
		{
			name:   "valid non-ASCII UTF-8",
			value:  "conversation-é",
			reason: ConversationIDValidationInvalidCharacter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.reason, ValidateConversationID(tt.value))
		})
	}
}

func TestValidateConversationIDVisibleASCIIContract(t *testing.T) {
	t.Parallel()

	for value := 0; value < 128; value++ {
		value := byte(value)
		allowed := value == 0x21 ||
			(value >= 0x23 && value <= 0x2b) ||
			(value >= 0x2d && value <= 0x3a) ||
			(value >= 0x3c && value <= 0x5b) ||
			(value >= 0x5d && value <= 0x7e)

		reason := ValidateConversationID(string([]byte{value}))
		if allowed {
			assert.Equalf(t, ConversationIDValidationNone, reason, "byte 0x%02x should be allowed", value)
			continue
		}
		assert.NotEqualf(t, ConversationIDValidationNone, reason, "byte 0x%02x should be rejected", value)
	}
}

func TestConversationIDContextHelpersHandleNilAndUnset(t *testing.T) {
	t.Parallel()

	var nilContext context.Context
	assert.Empty(t, GetConversationID(nilContext))
	assert.Equal(t, ConversationIDCandidate{}, GetConversationIDCandidate(nilContext))
	assert.Empty(t, GetConversationID(context.Background()))
	assert.Equal(t, ConversationIDCandidate{}, GetConversationIDCandidate(context.Background()))

	ctx := WithConversationID(nilContext, "conversation-from-nil")
	assert.Equal(t, "conversation-from-nil", GetConversationID(ctx))

	ctx = withConversationIDProgrammaticCandidate(nilContext, ConversationIDCandidate{
		Value:   "programmatic-from-nil",
		Source:  ConversationIDSourceCoreContext,
		Present: true,
	})
	assert.Equal(t, "programmatic-from-nil", GetConversationID(ctx))

	ctx = withConversationIDHeaderCandidate(nilContext, ConversationIDCandidate{
		Value:   "header-from-nil",
		Source:  ConversationIDSourceCoreHeader,
		Present: true,
	})
	assert.Equal(t, "header-from-nil", GetConversationID(ctx))

	assert.NotNil(t, WithoutConversationID(nilContext))
	assert.Empty(t, GetConversationID(WithoutConversationID(ctx)))
}

func TestNormalizeConversationIDCandidate(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ConversationIDCandidate{}, normalizeConversationIDCandidate(ConversationIDCandidate{
		Value:  "ignored",
		Source: ConversationIDSourceCoreHeader,
	}))

	invalid := normalizeConversationIDCandidate(ConversationIDCandidate{
		Value:   "contains space",
		Source:  ConversationIDSourceCoreHeader,
		Present: true,
	})
	assert.Empty(t, invalid.Value)
	assert.Equal(t, ConversationIDValidationInvalidCharacter, invalid.RejectionReason)
}

func TestConversationIDCandidatePrecedenceIsCallOrderIndependent(t *testing.T) {
	t.Parallel()

	headerCandidate := ConversationIDCandidate{
		Present: true,
		Value:   "header-conversation",
		Source:  ConversationIDSourceCoreHeader,
	}

	programmaticThenHeader := WithConversationID(context.Background(), "programmatic-conversation")
	programmaticThenHeader = withConversationIDHeaderCandidate(programmaticThenHeader, headerCandidate)
	programmaticThenHeader = WithConversationID(programmaticThenHeader, "replacement-programmatic")

	headerThenProgrammatic := withConversationIDHeaderCandidate(context.Background(), headerCandidate)
	headerThenProgrammatic = WithConversationID(headerThenProgrammatic, "programmatic-conversation")

	for name, ctx := range map[string]context.Context{
		"programmatic then header": programmaticThenHeader,
		"header then programmatic": headerThenProgrammatic,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, "header-conversation", GetConversationID(ctx))
			assert.Equal(t, headerCandidate, GetConversationIDCandidate(ctx))
		})
	}

	revealed := withoutConversationIDHeaderCandidate(programmaticThenHeader)
	assert.Equal(t, "replacement-programmatic", GetConversationID(revealed))
	assert.Equal(t, ConversationIDSourceCoreContext, GetConversationIDCandidate(revealed).Source)
}

func TestInvalidConversationIDCandidatesDoNotRetainRawValues(t *testing.T) {
	t.Parallel()

	ctx := WithConversationID(context.Background(), "invalid programmatic")
	candidate := GetConversationIDCandidate(ctx)
	assert.True(t, candidate.Present)
	assert.Empty(t, candidate.Value)
	assert.Equal(t, ConversationIDSourceCoreContext, candidate.Source)
	assert.Equal(t, ConversationIDValidationInvalidCharacter, candidate.RejectionReason)
	assert.Empty(t, GetConversationID(ctx))

	invalidHeader := ConversationIDCandidate{
		Present:         true,
		Value:           "invalid header",
		Source:          ConversationIDSourceCoreHeader,
		RejectionReason: ConversationIDValidationInvalidCharacter,
	}
	ctx = withConversationIDHeaderCandidate(
		WithConversationID(context.Background(), "programmatic-conversation"),
		invalidHeader,
	)
	candidate = GetConversationIDCandidate(ctx)
	assert.True(t, candidate.Present)
	assert.Empty(t, candidate.Value)
	assert.Equal(t, ConversationIDSourceCoreHeader, candidate.Source)
	assert.Equal(t, ConversationIDValidationInvalidCharacter, candidate.RejectionReason)
	assert.Empty(t, GetConversationID(ctx))

	ctx = WithConversationID(ctx, "replacement-programmatic")
	assert.Empty(t, GetConversationID(ctx), "invalid header must retain precedence")
	assert.Equal(t, ConversationIDSourceCoreHeader, GetConversationIDCandidate(ctx).Source)
}

func TestWithoutConversationIDClearsAllConversationState(t *testing.T) {
	t.Parallel()

	ctx := WithRequestID(context.Background(), "request-1")
	ctx = WithConversationID(ctx, "programmatic-conversation")
	ctx = withConversationIDHeaderCandidate(ctx, ConversationIDCandidate{
		Present: true,
		Value:   "header-conversation",
		Source:  ConversationIDSourceCoreHeader,
	})

	ctx = WithoutConversationID(ctx)

	assert.Empty(t, GetConversationID(ctx))
	assert.Equal(t, ConversationIDCandidate{}, GetConversationIDCandidate(ctx))
	assert.Equal(t, "request-1", GetRequestID(ctx))
}
