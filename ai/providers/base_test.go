package providers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (b *trackingReadCloser) Close() error {
	b.closed = true
	return nil
}

// mockLogger for testing
type mockLogger struct {
	debugCalls []map[string]interface{}
	infoCalls  []map[string]interface{}
	errorCalls []map[string]interface{}
}

func (m *mockLogger) Debug(msg string, fields map[string]interface{}) {
	m.debugCalls = append(m.debugCalls, fields)
}

func (m *mockLogger) Info(msg string, fields map[string]interface{}) {
	m.infoCalls = append(m.infoCalls, fields)
}

func (m *mockLogger) Error(msg string, fields map[string]interface{}) {
	m.errorCalls = append(m.errorCalls, fields)
}

func (m *mockLogger) Warn(msg string, fields map[string]interface{}) {}

func (m *mockLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.debugCalls = append(m.debugCalls, fields)
}

func (m *mockLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.infoCalls = append(m.infoCalls, fields)
}

func (m *mockLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}

func (m *mockLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.errorCalls = append(m.errorCalls, fields)
}

func TestNewBaseClient(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		logger  core.Logger
	}{
		{
			name:    "with logger",
			timeout: 180 * time.Second,
			logger:  &mockLogger{},
		},
		{
			name:    "without logger",
			timeout: 60 * time.Second,
			logger:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewBaseClient(tt.timeout, tt.logger)

			if client == nil {
				t.Fatal("expected non-nil client")
			}

			if client.HTTPClient.Timeout != tt.timeout {
				t.Errorf("expected timeout %v, got %v", tt.timeout, client.HTTPClient.Timeout)
			}

			if tt.logger == nil {
				// When no logger is provided, we expect a NoOpLogger to be set
				if _, ok := client.Logger.(*core.NoOpLogger); !ok {
					t.Error("expected NoOpLogger when no logger provided")
				}
			}

			if tt.logger != nil && client.Logger != tt.logger {
				t.Error("logger not set correctly")
			}

			if client.MaxRetries != 3 {
				t.Errorf("expected default MaxRetries 3, got %d", client.MaxRetries)
			}
		})
	}
}

func TestBaseClient_ApplyDefaults(t *testing.T) {
	client := NewBaseClient(180*time.Second, nil)
	client.DefaultModel = "default-model"
	client.DefaultMaxTokens = 1000
	client.DefaultTemperature = 0.7
	client.DefaultSystemPrompt = "You are helpful"

	tests := []struct {
		name     string
		input    *core.AIOptions
		expected *core.AIOptions
	}{
		{
			name:  "nil options",
			input: nil,
			expected: &core.AIOptions{
				Model:        "default-model",
				MaxTokens:    1000,
				Temperature:  0.7,
				SystemPrompt: "You are helpful",
			},
		},
		{
			name:  "empty options",
			input: &core.AIOptions{},
			expected: &core.AIOptions{
				Model:        "default-model",
				MaxTokens:    1000,
				Temperature:  0.7,
				SystemPrompt: "You are helpful",
			},
		},
		{
			name: "partial options",
			input: &core.AIOptions{
				Model:       "custom-model",
				Temperature: 0.9,
			},
			expected: &core.AIOptions{
				Model:        "custom-model",
				MaxTokens:    1000,
				Temperature:  0.9,
				SystemPrompt: "You are helpful",
			},
		},
		{
			name: "full options",
			input: &core.AIOptions{
				Model:        "custom-model",
				MaxTokens:    500,
				Temperature:  0.5,
				SystemPrompt: "Custom prompt",
			},
			expected: &core.AIOptions{
				Model:        "custom-model",
				MaxTokens:    500,
				Temperature:  0.5,
				SystemPrompt: "Custom prompt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := client.ApplyDefaults(tt.input)

			if result.Model != tt.expected.Model {
				t.Errorf("expected model %q, got %q", tt.expected.Model, result.Model)
			}
			if result.MaxTokens != tt.expected.MaxTokens {
				t.Errorf("expected MaxTokens %d, got %d", tt.expected.MaxTokens, result.MaxTokens)
			}
			if result.Temperature != tt.expected.Temperature {
				t.Errorf("expected Temperature %f, got %f", tt.expected.Temperature, result.Temperature)
			}
			if result.SystemPrompt != tt.expected.SystemPrompt {
				t.Errorf("expected SystemPrompt %q, got %q", tt.expected.SystemPrompt, result.SystemPrompt)
			}
		})
	}
}

func TestBaseClient_ExecuteWithRetry(t *testing.T) {
	tests := []struct {
		name           string
		serverBehavior func(w http.ResponseWriter, r *http.Request)
		maxRetries     int
		wantErr        bool
		errContains    string // if wantErr, assert error message contains this
		expectedCalls  int
	}{
		{
			name: "success on first try",
			serverBehavior: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("success"))
			},
			maxRetries:    3,
			wantErr:       false,
			expectedCalls: 1,
		},
		{
			name: "success after retry",
			serverBehavior: func() func(w http.ResponseWriter, r *http.Request) {
				count := 0
				return func(w http.ResponseWriter, r *http.Request) {
					count++
					if count < 2 {
						w.WriteHeader(http.StatusTooManyRequests)
						w.Write([]byte("rate limited"))
					} else {
						w.WriteHeader(http.StatusOK)
						w.Write([]byte("success"))
					}
				}
			}(),
			maxRetries:    3,
			wantErr:       false,
			expectedCalls: 2,
		},
		{
			name: "max retries exceeded",
			serverBehavior: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("server error"))
			},
			maxRetries:    2,
			wantErr:       true,
			errContains:   "server error: status 500",
			expectedCalls: 3, // Initial + 2 retries
		},
		{
			name: "non-retryable error",
			serverBehavior: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error": "bad request"}`))
			},
			maxRetries:    3,
			wantErr:       false, // Returns response even for non-retryable
			expectedCalls: 1,
		},
		{
			name: "HTML 400 from CDN is retried",
			serverBehavior: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=UTF-8")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("<html><head><title>400 Bad Request</title></head><body><center><h1>400 Bad Request</h1></center><hr><center>cloudflare</center></body></html>"))
			},
			maxRetries:    2,
			wantErr:       true,                     // All retries exhausted
			errContains:   "non-API error response", // Must be proxy_error, not client_error
			expectedCalls: 3,                        // Initial + 2 retries (not 1)
		},
		{
			name: "HTML 400 then success recovers",
			serverBehavior: func() func(w http.ResponseWriter, r *http.Request) {
				count := 0
				return func(w http.ResponseWriter, r *http.Request) {
					count++
					if count < 2 {
						w.Header().Set("Content-Type", "text/html")
						w.WriteHeader(http.StatusBadRequest)
						w.Write([]byte("<html><body>400 Bad Request</body></html>"))
					} else {
						w.WriteHeader(http.StatusOK)
						w.Write([]byte("success"))
					}
				}
			}(),
			maxRetries:    3,
			wantErr:       false,
			expectedCalls: 2, // First attempt HTML 400, second succeeds
		},
		{
			name: "JSON 400 is not retried",
			serverBehavior: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad param"}}`))
			},
			maxRetries:    3,
			wantErr:       false, // Returns response (non-retryable)
			expectedCalls: 1,     // No retry
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				tt.serverBehavior(w, r)
			}))
			defer server.Close()

			client := NewBaseClient(180*time.Second, nil)
			client.ProviderName = "test-provider" // Set provider name for proxy error assertions
			client.MaxRetries = tt.maxRetries
			client.RetryDelay = 10 * time.Millisecond // Short delay for tests

			req, err := http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}

			resp, err := client.ExecuteWithRetry(context.Background(), req)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContains)
				}
				// ORCH-008: Verify transient proxy errors satisfy core.ProviderError
				if err != nil && tt.errContains == "non-API error response" {
					var pe core.ProviderError
					if !errors.As(err, &pe) {
						t.Fatal("proxy error should satisfy core.ProviderError")
					}
					if !pe.IsTransient() {
						t.Error("proxy error should have IsTransient() == true")
					}
					if pe.StatusCode() != http.StatusBadRequest {
						t.Errorf("expected status 400, got %d", pe.StatusCode())
					}
					if pe.Provider() == "" {
						t.Error("proxy error should have non-empty Provider()")
					}
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if resp != nil {
					resp.Body.Close()
				}
			}

			if callCount != tt.expectedCalls {
				t.Errorf("expected %d calls, got %d", tt.expectedCalls, callCount)
			}
		})
	}
}

func TestBaseClient_ExecuteWithRetry_ReplaysCompleteBodyForEveryAttempt(t *testing.T) {
	payload := []byte(`{"model":"test-model","prompt":"non-empty"}`)
	var capturedBodies [][]byte
	var attemptBodies []*trackingReadCloser
	callCount := 0

	client := NewBaseClient(180*time.Second, nil)
	client.MaxRetries = 1
	client.RetryDelay = 0
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		callCount++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read attempt body: %v", err)
		}
		if err := request.Body.Close(); err != nil {
			t.Fatalf("close attempt body: %v", err)
		}
		capturedBodies = append(capturedBodies, append([]byte(nil), body...))

		status := http.StatusInternalServerError
		if callCount == 2 {
			status = http.StatusOK
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(http.StatusText(status))),
			Request:    request,
		}, nil
	})}

	originalBody := &trackingReadCloser{Reader: bytes.NewReader(payload)}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://provider.example/messages",
		originalBody,
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.GetBody = func() (io.ReadCloser, error) {
		body := &trackingReadCloser{Reader: bytes.NewReader(payload)}
		attemptBodies = append(attemptBodies, body)
		return body, nil
	}

	response, err := client.ExecuteWithRetry(context.Background(), request)
	if err != nil {
		t.Fatalf("ExecuteWithRetry returned error: %v", err)
	}
	defer response.Body.Close()

	if callCount != 2 {
		t.Fatalf("expected two network attempts, got %d", callCount)
	}
	if len(capturedBodies) != 2 {
		t.Fatalf("expected two captured bodies, got %d", len(capturedBodies))
	}
	for attempt, body := range capturedBodies {
		if !bytes.Equal(body, payload) {
			t.Errorf("attempt %d body = %q, want %q", attempt+1, body, payload)
		}
	}
	if len(attemptBodies) != 2 || attemptBodies[0] == attemptBodies[1] {
		t.Fatalf("expected a distinct body for each attempt, got %#v", attemptBodies)
	}
	for attempt, body := range attemptBodies {
		if !body.closed {
			t.Errorf("attempt %d body was not closed", attempt+1)
		}
	}
	if !originalBody.closed {
		t.Error("original request body was not closed")
	}
}

func TestBaseClient_ExecuteWithRetry_RejectsNonReplayableBodyBeforeNetwork(t *testing.T) {
	networkCalls := 0
	logger := &mockLogger{}
	client := NewBaseClient(180*time.Second, logger)
	client.MaxRetries = 3
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		networkCalls++
		return nil, errors.New("network should not be called")
	})}
	tracing := &mockTelemetry{}
	client.SetTelemetry(tracing)

	originalBody := &trackingReadCloser{Reader: strings.NewReader("non-replayable")}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://provider.example/messages",
		originalBody,
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if request.GetBody != nil {
		t.Fatal("test request unexpectedly has GetBody")
	}

	response, err := client.ExecuteWithRetry(context.Background(), request)
	if response != nil {
		t.Fatalf("unexpected response: %#v", response)
	}
	if err == nil || !strings.Contains(err.Error(), "AI request body is not replayable") {
		t.Fatalf("expected non-replayable body error, got %v", err)
	}
	if networkCalls != 0 {
		t.Fatalf("network called %d times", networkCalls)
	}
	if !originalBody.closed {
		t.Error("rejected request body was not closed")
	}
	if tracing.lastSpan == nil || !tracing.lastSpan.ended {
		t.Fatal("attempt span was not ended")
	}
	if len(tracing.lastSpan.errors) != 1 || tracing.lastSpan.errors[0] != err {
		t.Fatalf("attempt span did not record the replay error: %#v", tracing.lastSpan.errors)
	}
	if got := tracing.lastSpan.attributes["ai.attempt_status"]; got != "request_error" {
		t.Fatalf("attempt status = %v, want request_error", got)
	}
	if got := tracing.lastSpan.attributes["ai.retryable"]; got != false {
		t.Fatalf("retryable attribute = %v, want false", got)
	}
	if len(logger.errorCalls) != 1 {
		t.Fatalf("error log count = %d, want 1", len(logger.errorCalls))
	}
	fields := logger.errorCalls[0]
	if got := fields["operation"]; got != "ai_request_error" {
		t.Fatalf("log operation = %v, want ai_request_error", got)
	}
	if got := fields["error_type"]; got != "request_body_replay" {
		t.Fatalf("log error_type = %v, want request_body_replay", got)
	}
	if got := fields["retryable"]; got != false {
		t.Fatalf("log retryable = %v, want false", got)
	}
}

func TestBaseClient_ExecuteWithRetry_PropagatesGetBodyErrorBeforeNetwork(t *testing.T) {
	networkCalls := 0
	client := NewBaseClient(180*time.Second, nil)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		networkCalls++
		return nil, errors.New("network should not be called")
	})}

	recreateErr := errors.New("body source unavailable")
	originalBody := &trackingReadCloser{Reader: strings.NewReader("replayable")}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://provider.example/messages",
		originalBody,
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.GetBody = func() (io.ReadCloser, error) {
		return nil, recreateErr
	}

	response, err := client.ExecuteWithRetry(context.Background(), request)
	if response != nil {
		t.Fatalf("unexpected response: %#v", response)
	}
	if !errors.Is(err, recreateErr) || !strings.Contains(err.Error(), "recreate AI request body") {
		t.Fatalf("expected wrapped GetBody error, got %v", err)
	}
	if networkCalls != 0 {
		t.Fatalf("network called %d times", networkCalls)
	}
	if !originalBody.closed {
		t.Error("request body was not closed after GetBody failure")
	}
}

func TestBaseClient_HandleError(t *testing.T) {
	client := NewBaseClient(180*time.Second, nil)

	tests := []struct {
		name       string
		statusCode int
		body       []byte
		provider   string
		wantErr    string
	}{
		{
			name:       "bad request",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error": "invalid request"}`),
			provider:   "TestProvider",
			wantErr:    "TestProvider API error: invalid request - {\"error\": \"invalid request\"}",
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       []byte(`{"error": "invalid api key"}`),
			provider:   "TestProvider",
			wantErr:    "TestProvider API error: invalid or missing API key",
		},
		{
			name:       "rate limit",
			statusCode: http.StatusTooManyRequests,
			body:       []byte(`{"error": "rate limit exceeded"}`),
			provider:   "TestProvider",
			wantErr:    "TestProvider API error: rate limit exceeded",
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			body:       []byte(`{"error": "internal server error"}`),
			provider:   "TestProvider",
			wantErr:    "TestProvider API error: service temporarily unavailable (status 500)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.HandleError(tt.statusCode, tt.body, tt.provider, "test-model")

			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if err.Error() != tt.wantErr {
				t.Errorf("expected error %q, got %q", tt.wantErr, err.Error())
			}

			// Verify it satisfies core.ProviderError
			var pe core.ProviderError
			if !errors.As(err, &pe) {
				t.Fatal("expected error to satisfy core.ProviderError")
			}
			if pe.StatusCode() != tt.statusCode {
				t.Errorf("expected status code %d, got %d", tt.statusCode, pe.StatusCode())
			}
			if pe.Provider() != strings.ToLower(tt.provider) {
				t.Errorf("expected provider %q, got %q", strings.ToLower(tt.provider), pe.Provider())
			}
			if pe.Model() != "test-model" {
				t.Errorf("expected model %q, got %q", "test-model", pe.Model())
			}
			if pe.IsTransient() {
				t.Error("expected IsTransient() to be false for API errors")
			}
		})
	}
}

func TestBaseClient_Logging(t *testing.T) {
	logger := &mockLogger{}
	client := NewBaseClient(180*time.Second, logger)

	// Test LogRequest
	client.LogRequest("test-provider", "test-model", "test prompt")

	if len(logger.infoCalls) != 1 {
		t.Errorf("expected 1 info call, got %d", len(logger.infoCalls))
	}

	fields := logger.infoCalls[0]
	if fields["provider"] != "test-provider" {
		t.Errorf("expected provider test-provider, got %v", fields["provider"])
	}
	if fields["model"] != "test-model" {
		t.Errorf("expected model test-model, got %v", fields["model"])
	}

	// Test LogResponse
	usage := core.TokenUsage{
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
	}
	client.LogResponse(context.Background(), "test-provider", "test-model", usage, 100*time.Millisecond)

	if len(logger.infoCalls) != 2 {
		t.Errorf("expected 2 info calls, got %d", len(logger.infoCalls))
	}

	fields = logger.infoCalls[1] // Second info call is LogResponse
	if fields["provider"] != "test-provider" {
		t.Errorf("expected provider test-provider, got %v", fields["provider"])
	}
	if fields["total_tokens"] != 30 {
		t.Errorf("expected total_tokens 30, got %v", fields["total_tokens"])
	}

	// Test LogError
	client.LogError("test-provider", errors.New("test error"))

	if len(logger.errorCalls) != 1 {
		t.Errorf("expected 1 error call, got %d", len(logger.errorCalls))
	}

	fields = logger.errorCalls[0]
	if fields["provider"] != "test-provider" {
		t.Errorf("expected provider test-provider, got %v", fields["provider"])
	}
	if fields["error"] != "test error" {
		t.Errorf("expected error 'test error', got %v", fields["error"])
	}
}

func TestBaseClient_ContextCancellation(t *testing.T) {
	// Test that context cancellation is respected
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewBaseClient(180*time.Second, nil)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)

	// Cancel context immediately
	cancel()

	_, err := client.ExecuteWithRetry(ctx, req)
	if err == nil {
		t.Error("expected error due to cancelled context, got nil")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
}

// mockTelemetry tracks telemetry calls for testing
type mockTelemetry struct {
	spanStarted bool
	spanName    string
	lastSpan    *mockSpan
}

func (m *mockTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	m.spanStarted = true
	m.spanName = name
	m.lastSpan = &mockSpan{}
	return ctx, m.lastSpan
}

func (m *mockTelemetry) RecordMetric(name string, value float64, labels map[string]string) {}

// mockSpan tracks span operations for testing
type mockSpan struct {
	ended      bool
	attributes map[string]interface{}
	errors     []error
}

func (m *mockSpan) End() {
	m.ended = true
}

func (m *mockSpan) SetAttribute(key string, value interface{}) {
	if m.attributes == nil {
		m.attributes = make(map[string]interface{})
	}
	m.attributes[key] = value
}

func (m *mockSpan) RecordError(err error) {
	m.errors = append(m.errors, err)
}

func TestBaseClient_SetTelemetry(t *testing.T) {
	client := NewBaseClient(180*time.Second, nil)

	// Initially telemetry should be nil
	if client.Telemetry != nil {
		t.Error("expected nil telemetry initially")
	}

	// Set telemetry
	telemetry := &mockTelemetry{}
	client.SetTelemetry(telemetry)

	if client.Telemetry != telemetry {
		t.Error("telemetry not set correctly")
	}

	// Set to nil
	client.SetTelemetry(nil)
	if client.Telemetry != nil {
		t.Error("expected nil telemetry after setting nil")
	}
}

func TestBaseClient_StartSpan(t *testing.T) {
	tests := []struct {
		name           string
		telemetry      core.Telemetry
		spanName       string
		expectSpanType string
	}{
		{
			name:           "with telemetry",
			telemetry:      &mockTelemetry{},
			spanName:       "test.operation",
			expectSpanType: "*providers.mockSpan",
		},
		{
			name:           "without telemetry",
			telemetry:      nil,
			spanName:       "test.operation",
			expectSpanType: "*core.NoOpSpan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewBaseClient(180*time.Second, nil)
			client.Telemetry = tt.telemetry

			ctx := context.Background()
			newCtx, span := client.StartSpan(ctx, tt.spanName)

			if newCtx == nil {
				t.Error("expected non-nil context")
			}

			if span == nil {
				t.Error("expected non-nil span")
			}

			// Verify span type
			spanType := ""
			switch span.(type) {
			case *mockSpan:
				spanType = "*providers.mockSpan"
			case *core.NoOpSpan:
				spanType = "*core.NoOpSpan"
			}

			if spanType != tt.expectSpanType {
				t.Errorf("expected span type %s, got %s", tt.expectSpanType, spanType)
			}

			// Verify telemetry was called when present
			if mt, ok := tt.telemetry.(*mockTelemetry); ok {
				if !mt.spanStarted {
					t.Error("expected span to be started")
				}
				if mt.spanName != tt.spanName {
					t.Errorf("expected span name %q, got %q", tt.spanName, mt.spanName)
				}
			}
		})
	}
}

func TestBaseClient_LogResponseContent(t *testing.T) {
	logger := &mockLogger{}
	client := NewBaseClient(180*time.Second, logger)

	// Call LogResponseContent
	client.LogResponseContent("test-provider", "test-model", "This is a test response")

	// Verify debug call was made
	if len(logger.debugCalls) != 1 {
		t.Errorf("expected 1 debug call, got %d", len(logger.debugCalls))
	}

	fields := logger.debugCalls[0]
	if fields["provider"] != "test-provider" {
		t.Errorf("expected provider test-provider, got %v", fields["provider"])
	}
	if fields["model"] != "test-model" {
		t.Errorf("expected model test-model, got %v", fields["model"])
	}
	if fields["response"] != "This is a test response" {
		t.Errorf("expected response 'This is a test response', got %v", fields["response"])
	}
	if fields["response_length"] != 23 {
		t.Errorf("expected response_length 23, got %v", fields["response_length"])
	}
}

func TestDefaultRetryConfig(t *testing.T) {
	config := DefaultRetryConfig()

	// Verify defaults
	if config.MaxRetries != 3 {
		t.Errorf("expected MaxRetries 3, got %d", config.MaxRetries)
	}

	if config.RetryDelay != time.Second {
		t.Errorf("expected RetryDelay 1s, got %v", config.RetryDelay)
	}

	if config.ShouldRetry == nil {
		t.Fatal("expected non-nil ShouldRetry function")
	}

	// Test ShouldRetry function with various scenarios
	tests := []struct {
		name     string
		resp     *http.Response
		err      error
		expected bool
	}{
		{
			name:     "network error",
			resp:     nil,
			err:      errors.New("network error"),
			expected: true,
		},
		{
			name:     "500 server error",
			resp:     &http.Response{StatusCode: 500},
			err:      nil,
			expected: true,
		},
		{
			name:     "502 bad gateway",
			resp:     &http.Response{StatusCode: 502},
			err:      nil,
			expected: true,
		},
		{
			name:     "429 rate limit",
			resp:     &http.Response{StatusCode: 429},
			err:      nil,
			expected: true,
		},
		{
			name:     "400 bad request",
			resp:     &http.Response{StatusCode: 400},
			err:      nil,
			expected: false,
		},
		{
			name:     "200 success",
			resp:     &http.Response{StatusCode: 200},
			err:      nil,
			expected: false,
		},
		{
			name:     "401 unauthorized",
			resp:     &http.Response{StatusCode: 401},
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := config.ShouldRetry(tt.resp, tt.err)
			if result != tt.expected {
				t.Errorf("expected ShouldRetry=%v, got %v", tt.expected, result)
			}
		})
	}
}

func TestHandleError_DefaultCase(t *testing.T) {
	client := NewBaseClient(180*time.Second, nil)

	// Test with an unknown status code (not handled by specific cases)
	err := client.HandleError(418, []byte("I'm a teapot"), "TestProvider", "test-model")

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	expected := "TestProvider API error (status 418): I'm a teapot"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

// --- isBillingExhausted: phrase detection ---
//
// isBillingExhausted is the package-private detector that decides whether a
// 4xx error body looks like billing/quota exhaustion. The chain client uses
// this to decide whether to fail over to a different provider — so false
// positives are expensive (they cause chain-wide retry storms on real
// malformed-input errors). Tests are intentionally tight on the positive set
// and have explicit negative cases for adjacent phrases that must NOT trip.

func TestIsBillingExhausted_PositiveAnthropicCreditBalance(t *testing.T) {
	// The exact body Anthropic returns when the account credit is exhausted,
	// captured from a real failing reflection pass against the deployed
	// devops-chat-agent. This is the canonical case the fix exists for.
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`)
	if !isBillingExhausted(body) {
		t.Error("Anthropic credit-balance body must be detected as billing-exhausted")
	}
}

func TestIsBillingExhausted_PositiveOpenAIInsufficientQuota(t *testing.T) {
	// OpenAI returns this code in the structured error body when usage
	// limits are hit. They typically return it with HTTP 429 (already
	// retryable), but some plans surface it as a 4xx — defensive check.
	body := []byte(`{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`)
	if !isBillingExhausted(body) {
		t.Error("OpenAI insufficient_quota body must be detected as billing-exhausted")
	}
}

func TestIsBillingExhausted_PositiveCaseInsensitive(t *testing.T) {
	// Phrases must match regardless of casing — providers are inconsistent
	// about capitalization in error messages.
	cases := [][]byte{
		[]byte(`{"error":"CREDIT BALANCE too low"}`),
		[]byte(`{"error":"Your Credit Balance is exhausted"}`),
		[]byte(`{"error":"INSUFFICIENT_QUOTA"}`),
		[]byte(`Payment Required`),
		[]byte(`payment_required`),
	}
	for i, body := range cases {
		if !isBillingExhausted(body) {
			t.Errorf("case %d: %q should be detected", i, body)
		}
	}
}

func TestIsBillingExhausted_NegativeBareBillingMention(t *testing.T) {
	// The bare phrase "billing" was deliberately removed from
	// billingExhaustedPhrases because it's too broad — it would match
	// unrelated 4xx errors like "billing address invalid" or
	// "billing-enabled project required". This test pins that decision so
	// nobody re-adds the phrase without realizing it re-introduces false
	// positives.
	//
	// If a future provider returns billing-exhaustion errors that don't
	// match credit balance / insufficient_quota / payment required, prefer
	// adding a NEW specific phrase rather than reverting to bare "billing".
	cases := [][]byte{
		[]byte(`{"error":"billing address invalid"}`),
		[]byte(`{"error":"this API requires a billing-enabled project"}`),
		[]byte(`{"error":"Update your billing details in account settings"}`),
		[]byte(`{"error":"Account billing issue. Please contact support."}`), // intentionally borderline — still should not match
	}
	for i, body := range cases {
		if isBillingExhausted(body) {
			t.Errorf("case %d: %q must NOT be flagged — bare 'billing' is too broad", i, body)
		}
	}
}

func TestIsBillingExhausted_NegativeMalformedRequest(t *testing.T) {
	// A real 400 from Anthropic for a malformed parameter — must NOT trigger
	// billing detection. This is the false-positive guard.
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.0: Input should be a valid string"}}`)
	if isBillingExhausted(body) {
		t.Error("malformed-request body must NOT be detected as billing-exhausted")
	}
}

func TestIsBillingExhausted_NegativeContentPolicy(t *testing.T) {
	// A content-policy violation — definitely not billing.
	body := []byte(`{"error":{"message":"Your request was rejected as a result of our safety system","type":"content_policy_violation"}}`)
	if isBillingExhausted(body) {
		t.Error("content-policy body must NOT be detected as billing-exhausted")
	}
}

func TestIsBillingExhausted_NegativeEmptyBody(t *testing.T) {
	if isBillingExhausted(nil) {
		t.Error("nil body must not be flagged")
	}
	if isBillingExhausted([]byte{}) {
		t.Error("empty body must not be flagged")
	}
}

func TestIsBillingExhausted_NegativeUnrelatedJSON(t *testing.T) {
	// Generic JSON that mentions none of the marker phrases — must not match.
	body := []byte(`{"error":{"message":"Rate limit on tokens exceeded for organization"}}`)
	if isBillingExhausted(body) {
		t.Error("unrelated rate-limit body must not be flagged")
	}
}

// --- HandleError: retryable flag wiring ---
//
// HandleError is the single funnel that turns an HTTP response into a
// providerError. The retryable flag must be set when the body looks like
// billing exhaustion, and only then. These tests pin the integration
// between the detector and the error constructor.

func TestHandleError_BillingExhausted400IsRetryable(t *testing.T) {
	client := NewBaseClient(180*time.Second, nil)
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API"}}`)

	err := client.HandleError(http.StatusBadRequest, body, "Anthropic", "claude-sonnet-4-5-20250929")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe core.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error must be a core.ProviderError, got %T", err)
	}
	if pe.StatusCode() != 400 {
		t.Errorf("status code preserved: got %d, want 400", pe.StatusCode())
	}
	if !pe.IsRetryable() {
		t.Error("400 with credit-balance body MUST be marked retryable so the chain client fails over")
	}
	if pe.IsTransient() {
		t.Error("billing-exhausted is not a proxy/transient error — IsTransient() must stay false")
	}
}

func TestHandleError_NonBilling400IsNotRetryable(t *testing.T) {
	client := NewBaseClient(180*time.Second, nil)
	// Real malformed-request error — no billing markers in the body
	body := []byte(`{"error":{"message":"messages.0: invalid input format"}}`)

	err := client.HandleError(http.StatusBadRequest, body, "Anthropic", "claude-sonnet-4-5-20250929")
	var pe core.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected core.ProviderError, got %T", err)
	}
	if pe.IsRetryable() {
		t.Error("genuine malformed-input 400 must NOT be marked retryable — false positive would cause chain-wide retry storms")
	}
}

func TestHandleError_500IsNotMarkedRetryableByThisFlag(t *testing.T) {
	// 5xx errors are already retryable by status-code rules in
	// chain_client.isClientError; the new IsRetryable flag is *only* for
	// the specific 4xx-with-billing-marker case. Verify a 500 doesn't
	// accidentally get the flag set.
	client := NewBaseClient(180*time.Second, nil)
	body := []byte(`Internal Server Error`)
	err := client.HandleError(http.StatusInternalServerError, body, "Anthropic", "test-model")
	var pe core.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected core.ProviderError, got %T", err)
	}
	if pe.IsRetryable() {
		t.Error("5xx errors are retryable by status code; the IsRetryable flag should remain false to avoid double-meaning")
	}
}

func TestHandleError_429IsNotMarkedRetryableByThisFlag(t *testing.T) {
	// 429 is already retryable by status code in chain_client.isClientError.
	// IsRetryable() should stay false here for the same single-meaning reason.
	client := NewBaseClient(180*time.Second, nil)
	body := []byte(`{"error":"rate limit exceeded"}`)
	err := client.HandleError(http.StatusTooManyRequests, body, "OpenAI", "gpt-4")
	var pe core.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected core.ProviderError, got %T", err)
	}
	if pe.IsRetryable() {
		t.Error("429 is retryable by status code; IsRetryable flag should remain false")
	}
}

func TestHandleError_BillingExhausted402IsRetryable(t *testing.T) {
	// HTTP 402 Payment Required is the RFC-defined billing status code.
	// Any provider that uses it should also set IsRetryable.
	client := NewBaseClient(180*time.Second, nil)
	body := []byte(`Payment Required`)
	err := client.HandleError(402, body, "TestProvider", "test-model")
	var pe core.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected core.ProviderError, got %T", err)
	}
	if !pe.IsRetryable() {
		t.Error("402 Payment Required must be marked retryable")
	}
}

// TestHandleError_ProductionAnthropicCreditExhausted is the regression test
// pinned to the EXACT error payload Anthropic returned in the production
// reflect-98b3e4c0 incident that motivated this fix. The body — including
// the request_id field that real Anthropic responses carry, the punctuation,
// and the marketing-copy-style "Plans & Billing" phrase — must continue to
// be classified as retryable so the chain client fails over to Groq instead
// of fail-fast 4xx-aborting on Anthropic.
//
// If this test ever starts failing, the fix has regressed and the production
// failure mode is back. The test name and detail intentionally make it
// obvious in CI output why this matters.
func TestHandleError_ProductionAnthropicCreditExhausted(t *testing.T) {
	// Verbatim from the deployed devops-chat-agent's LLM debug record for
	// reflect-98b3e4c0 (26 entities, all 26 calls failed with this exact body).
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."},"request_id":"req_011CZq19HfK1sWF2o3ArAFjW"}`)

	client := NewBaseClient(180*time.Second, nil)
	err := client.HandleError(http.StatusBadRequest, body, "Anthropic", "claude-sonnet-4-5-20250929")
	if err == nil {
		t.Fatal("expected error from HandleError, got nil")
	}

	var pe core.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error must be a core.ProviderError, got %T", err)
	}

	// Status preserved end-to-end so consumers (registry viewer, instrumented
	// client, agent handlers) see the original 400 from the wire.
	if pe.StatusCode() != 400 {
		t.Errorf("status code: got %d, want 400", pe.StatusCode())
	}

	// IsRetryable() must be true — this is the entire point of the fix.
	// Without it, isClientError in the chain client returns true on a 400
	// (the status arithmetic kicks in), the chain fails fast, and the
	// devops-chat-agent burns 26 LLM calls per pass on the dead provider.
	if !pe.IsRetryable() {
		t.Fatal("CRITICAL REGRESSION: production credit-exhausted body must be marked retryable so chain client fails over to next provider — see reflect-98b3e4c0 incident")
	}

	// IsTransient() stays false — this is a real API response, not a CDN/proxy
	// hiccup. The two flags are independent and must not collide.
	if pe.IsTransient() {
		t.Error("billing exhaustion is not a proxy/transient error")
	}

	// Provider name normalized to lowercase for consistency with other code paths.
	if pe.Provider() != "anthropic" {
		t.Errorf("provider should be normalized to lowercase: got %q, want %q", pe.Provider(), "anthropic")
	}
}
