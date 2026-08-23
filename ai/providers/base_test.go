package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
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

type errorReader struct {
	err error
}

func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

// mockLogger for testing
type mockLogger struct {
	debugCalls []map[string]interface{}
	infoCalls  []map[string]interface{}
	warnCalls  []map[string]interface{}
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

func (m *mockLogger) Warn(msg string, fields map[string]interface{}) {
	m.warnCalls = append(m.warnCalls, fields)
}

func (m *mockLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.debugCalls = append(m.debugCalls, fields)
}

func (m *mockLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.infoCalls = append(m.infoCalls, fields)
}

func (m *mockLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.warnCalls = append(m.warnCalls, fields)
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
		wantStatus     int
		wantBody       string
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
			wantErr:       false,
			expectedCalls: 3, // Initial + 2 retries
			wantStatus:    http.StatusInternalServerError,
			wantBody:      "server error",
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
			wantErr:       true,                        // All retries exhausted
			errContains:   "non-API provider response", // Must be proxy_error, not client_error
			expectedCalls: 3,                           // Initial + 2 retries (not 1)
		},
		{
			name: "HTML 500 from gateway remains a transport failure",
			serverBehavior: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("<html><body>gateway failure</body></html>"))
			},
			maxRetries:    1,
			wantErr:       true,
			errContains:   "non-API provider response",
			expectedCalls: 2,
			wantStatus:    http.StatusInternalServerError,
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
				if err != nil && tt.errContains == "non-API provider response" {
					var pe core.ProviderError
					if !errors.As(err, &pe) {
						t.Fatal("proxy error should satisfy core.ProviderError")
					}
					if !pe.IsTransient() {
						t.Error("proxy error should have IsTransient() == true")
					}
					wantStatus := tt.wantStatus
					if wantStatus == 0 {
						wantStatus = http.StatusBadRequest
					}
					if pe.StatusCode() != wantStatus {
						t.Errorf("expected status %d, got %d", wantStatus, pe.StatusCode())
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
					if tt.wantStatus != 0 && resp.StatusCode != tt.wantStatus {
						t.Errorf("response status = %d, want %d", resp.StatusCode, tt.wantStatus)
					}
					if tt.wantBody != "" {
						body, readErr := io.ReadAll(resp.Body)
						if readErr != nil || string(body) != tt.wantBody {
							t.Errorf("response body = %q, %v; want %q", body, readErr, tt.wantBody)
						}
					}
					_ = resp.Body.Close()
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

func TestBaseClient_RetryExhaustionReturnsFinalAPIResponseOpen(t *testing.T) {
	client := NewBaseClient(time.Second, nil)
	client.ProviderName = "openai"
	client.MaxRetries = 1
	client.RetryDelay = 0

	var bodies []*trackingReadCloser
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := &trackingReadCloser{Reader: strings.NewReader(`{"error":{"type":"provider_error"}}`)}
		bodies = append(bodies, body)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header: http.Header{
				"Content-Type":    {"application/json"},
				"X-Generation-Id": {"generation-final"},
			},
			Body: body, Request: request,
		}, nil
	})}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://provider.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.ExecuteWithRetry(t.Context(), request)
	if err != nil {
		t.Fatalf("ExecuteWithRetry error = %v", err)
	}
	if response == nil || response.StatusCode != http.StatusServiceUnavailable ||
		response.Header.Get("X-Generation-Id") != "generation-final" {
		t.Fatalf("final response = %#v", response)
	}
	if len(bodies) != 2 || !bodies[0].closed || bodies[1].closed {
		t.Fatalf("response body ownership = %#v", bodies)
	}
	decoded, readErr := io.ReadAll(response.Body)
	if readErr != nil || !strings.Contains(string(decoded), "provider_error") {
		t.Fatalf("final body = %q, %v", decoded, readErr)
	}
	_ = response.Body.Close()
}

func TestRetryDelayUsesGreaterApplicableRetryAfter(t *testing.T) {
	now := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		status     int
		header     string
		backoff    time.Duration
		want       time.Duration
		wantSource string
	}{
		{name: "429 seconds greater", status: 429, header: "5", backoff: 2 * time.Second, want: 5 * time.Second, wantSource: "retry_after"},
		{name: "503 HTTP date greater", status: 503, header: now.Add(7 * time.Second).Format(http.TimeFormat), backoff: time.Second, want: 7 * time.Second, wantSource: "retry_after"},
		{name: "server value below backoff", status: 429, header: "1", backoff: 2 * time.Second, want: 2 * time.Second, wantSource: "exponential"},
		{name: "other status ignores header", status: 500, header: "30", backoff: 2 * time.Second, want: 2 * time.Second, wantSource: "exponential"},
		{name: "negative ignored", status: 429, header: "-1", backoff: 2 * time.Second, want: 2 * time.Second, wantSource: "exponential"},
		{name: "invalid ignored", status: 503, header: "later", backoff: 2 * time.Second, want: 2 * time.Second, wantSource: "exponential"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{StatusCode: test.status, Header: http.Header{"Retry-After": {test.header}}}
			got, source := retryDelay(test.backoff, response, now)
			if got != test.want || source != test.wantSource {
				t.Fatalf("retryDelay = %s, %q; want %s, %q", got, source, test.want, test.wantSource)
			}
		})
	}
}

func TestParseRetryAfterSaturatesDeltaSecondsOverflow(t *testing.T) {
	const maxDuration = time.Duration(1<<63 - 1)
	maxSeconds := uint64(maxDuration / time.Second)
	boundary, ok := parseRetryAfter(fmt.Sprint(maxSeconds), time.Time{})
	if !ok || boundary != time.Duration(maxSeconds)*time.Second {
		t.Fatalf("boundary = %s, %t", boundary, ok)
	}
	for _, value := range []string{
		fmt.Sprint(maxSeconds + 1),
		"184467440737095516160000000000000000000",
	} {
		got, valid := parseRetryAfter(value, time.Time{})
		if !valid || got != maxDuration {
			t.Fatalf("parseRetryAfter(%q) = %s, %t; want saturation", value, got, valid)
		}
	}
}

func TestBaseClient_RetryAfterBeyondDeadlineDoesNotIssueEarlyAttempt(t *testing.T) {
	client := NewBaseClient(time.Second, nil)
	client.ProviderName = "openai"
	client.MaxRetries = 1
	client.RetryDelay = time.Millisecond
	calls := 0
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": {"3600"}},
			Body:       io.NopCloser(strings.NewReader("rate limited")),
			Request:    request,
		}, nil
	})}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.ExecuteWithRetry(ctx, request)
	if response != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want 1", calls)
	}
}

func TestBaseClient_RetryAfterWithoutCallerDeadlineUsesAbsoluteWaitCap(t *testing.T) {
	client := NewBaseClient(25*time.Millisecond, nil)
	client.ProviderName = "openai"
	client.MaxRetries = 1
	client.RetryDelay = time.Millisecond
	calls := 0
	client.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": {"18446744073709551615"}},
			Body:       io.NopCloser(strings.NewReader("rate limited")),
			Request:    request,
		}, nil
	})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://provider.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := client.ExecuteWithRetry(t.Context(), request)
	if response != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline-free retry wait took %s", elapsed)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want 1", calls)
	}
}

func TestBaseClient_RetryAfterLongCallerDeadlineUsesAbsoluteWaitCap(t *testing.T) {
	client := NewBaseClient(25*time.Millisecond, nil)
	client.ProviderName = "openai"
	client.MaxRetries = 1
	client.RetryDelay = time.Millisecond
	calls := 0
	client.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": {"3600"}},
			Body:       io.NopCloser(strings.NewReader("rate limited")),
			Request:    request,
		}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := client.ExecuteWithRetry(ctx, request)
	if response != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("long caller deadline retry wait took %s", elapsed)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want 1", calls)
	}
}

func TestBaseClient_AttemptSpansAndRetryLogsUseBoundedContract(t *testing.T) {
	logger := &mockLogger{}
	tracing := &mockTelemetry{}
	client := NewBaseClient(time.Second, logger)
	client.ProviderName = "openai"
	client.MaxRetries = 1
	client.RetryDelay = 0
	client.SetTelemetry(tracing)
	calls := 0
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		status := http.StatusTooManyRequests
		if calls == 2 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("response")), Request: request}, nil
	})}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://provider.example", nil)
	response, err := client.ExecuteWithRetry(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(tracing.spans) != 2 {
		t.Fatalf("attempt spans = %d", len(tracing.spans))
	}
	for index, span := range tracing.spans {
		if !span.ended || span.attributes["ai.attempt"] != index+1 || span.attributes["ai.max_retries"] != 1 ||
			span.attributes["ai.is_retry"] != (index > 0) || span.attributes["ai.attempt_duration_ms"] == nil {
			t.Fatalf("attempt %d attributes = %#v, ended=%t", index+1, span.attributes, span.ended)
		}
	}
	if tracing.spans[0].attributes["ai.attempt_status"] != "server_error" ||
		tracing.spans[0].attributes["http.status_code"] != http.StatusTooManyRequests ||
		len(tracing.spans[0].errors) != 1 {
		t.Fatalf("first attempt = %#v / %#v", tracing.spans[0].attributes, tracing.spans[0].errors)
	}
	if tracing.spans[1].attributes["ai.attempt_status"] != "success" ||
		tracing.spans[1].attributes["http.status_code"] != http.StatusOK {
		t.Fatalf("second attempt = %#v", tracing.spans[1].attributes)
	}
	for _, fields := range append(append([]map[string]interface{}{}, logger.warnCalls...), logger.infoCalls...) {
		status, present := fields["status"]
		if present && status != "retry" && status != "recovered" {
			t.Fatalf("unbounded log status: %#v", fields)
		}
	}
}

func TestBaseClient_FirstAttemptSuccessUsesSuccessStatus(t *testing.T) {
	logger := &mockLogger{}
	client := NewBaseClient(time.Second, logger)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    request,
		}, nil
	})}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://provider.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.ExecuteWithRetry(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(logger.debugCalls) != 1 || logger.debugCalls[0]["operation"] != "ai_http_success" ||
		logger.debugCalls[0]["status"] != "success" {
		t.Fatalf("first-attempt success log = %#v", logger.debugCalls)
	}
}

func TestBaseClient_ExecuteWithRetryPrepared_PreparesEveryFreshAttempt(t *testing.T) {
	client := NewBaseClient(time.Second, nil)
	client.MaxRetries = 1
	client.RetryDelay = 0

	var bodies []string
	var credentials []string
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read attempt body: %v", err)
		}
		bodies = append(bodies, string(body))
		credentials = append(credentials, request.Header.Get("X-Attempt-Credential"))
		status := http.StatusInternalServerError
		if len(bodies) == 2 {
			status = http.StatusOK
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("response")),
			Request:    request,
		}, nil
	})}

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://provider.example/messages",
		bytes.NewReader([]byte("complete-body")),
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	prepareCalls := 0
	response, err := client.ExecuteWithRetryPrepared(context.Background(), request, func(_ context.Context, attempt *http.Request) error {
		prepareCalls++
		attempt.Header.Set("X-Attempt-Credential", fmt.Sprintf("credential-%d", prepareCalls))
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteWithRetryPrepared returned error: %v", err)
	}
	defer response.Body.Close()

	if prepareCalls != 2 {
		t.Fatalf("preparer calls = %d, want 2", prepareCalls)
	}
	if !reflect.DeepEqual(bodies, []string{"complete-body", "complete-body"}) {
		t.Fatalf("attempt bodies = %#v", bodies)
	}
	if !reflect.DeepEqual(credentials, []string{"credential-1", "credential-2"}) {
		t.Fatalf("attempt credentials = %#v", credentials)
	}
	if request.Header.Get("X-Attempt-Credential") != "" {
		t.Fatalf("logical request was mutated: %#v", request.Header)
	}
}

func TestBaseClient_ExecuteWithRetryPrepared_RejectsNilPreparer(t *testing.T) {
	client := NewBaseClient(time.Second, nil)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://provider.example", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if _, err := client.ExecuteWithRetryPrepared(context.Background(), request, nil); err == nil || !strings.Contains(err.Error(), "preparer is nil") {
		t.Fatalf("nil preparer error = %v", err)
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
	if len(tracing.lastSpan.errors) != 1 || tracing.lastSpan.errors[0].Error() != "AI provider request failed: invalid_request" {
		t.Fatalf("attempt span did not record the sanitized replay error: %#v", tracing.lastSpan.errors)
	}
	if got := tracing.lastSpan.attributes["ai.error_type"]; got != "invalid_request" {
		t.Fatalf("span error type = %v, want invalid_request", got)
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
	if got := fields["error_type"]; got != "invalid_request" {
		t.Fatalf("log error_type = %v, want invalid_request", got)
	}
	if got := fields["error"]; got != "AI provider request failed: invalid_request" {
		t.Fatalf("log error = %v, want sanitized invalid_request", got)
	}
	if strings.Contains(fmt.Sprint(fields), err.Error()) {
		t.Fatalf("log leaked original replay error: %#v", fields)
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

func TestBaseClient_RetryObservationsSanitizeTransportDetails(t *testing.T) {
	const (
		querySecret = "query-credential-secret"
		causeSecret = "transport-cause-secret"
	)
	wantCause := errors.New(causeSecret)
	logger := &mockLogger{}
	tracing := &mockTelemetry{}
	client := NewBaseClient(time.Second, logger)
	client.ProviderName = "openai"
	client.MaxRetries = 1
	client.RetryDelay = 0
	client.SetTelemetry(tracing)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, wantCause
	})}

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"https://provider.example/v1/chat/completions?credential="+querySecret,
		nil,
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	response, err := client.ExecuteWithRetry(context.Background(), request)
	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	if !errors.Is(err, wantCause) {
		t.Fatalf("returned error = %v, want cause identity", err)
	}
	if len(tracing.spans) != 2 {
		t.Fatalf("attempt spans = %d, want 2", len(tracing.spans))
	}

	var observed strings.Builder
	for _, fields := range logger.debugCalls {
		fmt.Fprint(&observed, fields)
	}
	for _, fields := range logger.infoCalls {
		fmt.Fprint(&observed, fields)
	}
	for _, fields := range logger.warnCalls {
		fmt.Fprint(&observed, fields)
	}
	for _, fields := range logger.errorCalls {
		fmt.Fprint(&observed, fields)
	}
	for attempt, span := range tracing.spans {
		if !span.ended {
			t.Errorf("attempt span %d was not ended", attempt+1)
		}
		if span.attributes["ai.error_type"] != "transport" {
			t.Errorf("attempt span %d error type = %#v", attempt+1, span.attributes["ai.error_type"])
		}
		fmt.Fprint(&observed, span.attributes)
		for _, spanErr := range span.errors {
			fmt.Fprint(&observed, spanErr.Error())
			if spanErr.Error() != "AI provider request failed: transport" {
				t.Errorf("attempt span %d error = %q", attempt+1, spanErr.Error())
			}
		}
	}
	for _, forbidden := range []string{querySecret, causeSecret, "provider.example"} {
		if strings.Contains(observed.String(), forbidden) {
			t.Fatalf("observations leaked %q: %s", forbidden, observed.String())
		}
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
			wantErr:    "TestProvider API error: invalid request",
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

	// Legacy content-bearing helpers are safe no-ops.
	client.LogRequest("test-provider", "test-model", "test prompt")
	client.LogResponseContent("test-provider", "test-model", "test response")

	if len(logger.infoCalls) != 0 || len(logger.debugCalls) != 0 {
		t.Fatalf("legacy content helpers emitted logs: info=%#v debug=%#v", logger.infoCalls, logger.debugCalls)
	}

	ctx := core.WithRequestID(context.Background(), "core-request")
	ctx = telemetry.WithBaggage(ctx, "request_id", "baggage-request")
	client.LogRequestMetadata(ctx, RequestObservation{
		Provider:      "openai",
		ProviderAlias: "openai.groq",
		SemanticModel: "semantic-model",
		PromptLength:  11,
	})
	if len(logger.infoCalls) != 1 {
		t.Fatalf("request metadata info calls = %d, want 1", len(logger.infoCalls))
	}
	fields := logger.infoCalls[0]
	if fields["provider"] != "openai" || fields["model"] != "semantic-model" {
		t.Fatalf("request metadata fields = %#v", fields)
	}
	if fields["request_id"] != "baggage-request" {
		t.Fatalf("request ID precedence = %v, want baggage-request", fields["request_id"])
	}

	usage := core.TokenUsage{
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
	}
	client.LogResponseMetadata(ctx, ResponseObservation{
		Provider:      "openai",
		ProviderAlias: "openai.groq",
		SemanticModel: "semantic-model",
		Usage:         usage,
		Duration:      100 * time.Millisecond,
	})

	if len(logger.infoCalls) != 2 {
		t.Fatalf("expected 2 info calls, got %d", len(logger.infoCalls))
	}

	fields = logger.infoCalls[1]
	if fields["provider"] != "openai" || fields["model"] != "semantic-model" {
		t.Fatalf("response metadata fields = %#v", fields)
	}
	if fields["total_tokens"] != 30 {
		t.Errorf("expected total_tokens 30, got %v", fields["total_tokens"])
	}

	client.LogError("openai", errors.New("provider-body-secret"))

	if len(logger.errorCalls) != 1 {
		t.Errorf("expected 1 error call, got %d", len(logger.errorCalls))
	}

	fields = logger.errorCalls[0]
	if fields["provider"] != "openai" {
		t.Errorf("expected provider openai, got %v", fields["provider"])
	}
	if fields["error"] != "AI provider request failed: unknown" || fields["error_type"] != "unknown" {
		t.Fatalf("legacy error log was not sanitized: %#v", fields)
	}
	if strings.Contains(fmt.Sprint(fields), "provider-body-secret") {
		t.Fatalf("legacy error log leaked provider detail: %#v", fields)
	}
}

func TestResponseMetricProjectionExcludesProviderResponseIdentity(t *testing.T) {
	baseline := ResponseObservation{
		Provider: "openai",
		Usage: core.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
		},
		Duration: 125 * time.Millisecond,
	}
	withIdentity := baseline
	withIdentity.ProviderAlias = "openai.openrouter"
	withIdentity.SemanticModel = "openrouter/auto"
	withIdentity.ResponseModel = "publisher/private-deployment"
	withIdentity.ProviderRequestID = "gen-private-request"

	got := projectResponseMetrics(withIdentity)
	want := projectResponseMetrics(baseline)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response identity changed metric projection: got %#v, want %#v", got, want)
	}
	if got.Provider != "openai" || got.DurationMillis != 125 || got.PromptTokens != 10 || got.CompletionTokens != 20 {
		t.Fatalf("metric projection = %#v", got)
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
	spans       []*mockSpan
}

func (m *mockTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	m.spanStarted = true
	m.spanName = name
	m.lastSpan = &mockSpan{}
	m.spans = append(m.spans, m.lastSpan)
	return ctx, m.lastSpan
}

func (m *mockTelemetry) RecordMetric(name string, value float64, labels map[string]string) {}

type nilReturningTelemetry struct{}

func (nilReturningTelemetry) StartSpan(context.Context, string) (context.Context, core.Span) {
	return nil, nil
}

func (nilReturningTelemetry) RecordMetric(string, float64, map[string]string) {}

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

func TestBaseClient_StartSpanNormalizesNilTelemetryResults(t *testing.T) {
	client := NewBaseClient(time.Second, nil)
	client.Telemetry = nilReturningTelemetry{}

	ctx, span := client.StartSpan(nil, "ai.nil_telemetry")
	if ctx == nil {
		t.Fatal("StartSpan returned nil context")
	}
	if span == nil {
		t.Fatal("StartSpan returned nil span")
	}
	if _, ok := span.(*core.NoOpSpan); !ok {
		t.Fatalf("span type = %T, want *core.NoOpSpan", span)
	}
	span.SetAttribute("safe", true)
	span.RecordError(errors.New("ignored"))
	span.End()
}

func TestBaseClient_LogResponseContent(t *testing.T) {
	logger := &mockLogger{}
	client := NewBaseClient(180*time.Second, logger)

	client.LogResponseContent("test-provider", "test-model", "This is a test response")
	if len(logger.debugCalls) != 0 {
		t.Fatalf("deprecated content helper emitted debug fields: %#v", logger.debugCalls)
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

	expected := "TestProvider API error (status 418)"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestReadErrorBody(t *testing.T) {
	t.Parallel()

	t.Run("boundary", func(t *testing.T) {
		t.Parallel()
		want := bytes.Repeat([]byte("a"), maxProviderErrorBodyBytes)
		got, err := ReadErrorBody(bytes.NewReader(want))
		if err != nil {
			t.Fatalf("ReadErrorBody() error = %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("ReadErrorBody() returned %d bytes, want %d", len(got), len(want))
		}
	})

	t.Run("overflow discards prefix", func(t *testing.T) {
		t.Parallel()
		got, err := ReadErrorBody(bytes.NewReader(bytes.Repeat(
			[]byte("b"), maxProviderErrorBodyBytes+1,
		)))
		if err == nil {
			t.Fatal("ReadErrorBody() error = nil")
		}
		if got != nil {
			t.Fatalf("ReadErrorBody() retained %d bytes after overflow", len(got))
		}
	})

	t.Run("read failure discards prefix and cause text", func(t *testing.T) {
		t.Parallel()
		const canary = "reader-error-secret"
		got, err := ReadErrorBody(io.MultiReader(
			strings.NewReader("captured-prefix"),
			errorReader{err: errors.New(canary)},
		))
		if err == nil {
			t.Fatal("ReadErrorBody() error = nil")
		}
		if got != nil {
			t.Fatalf("ReadErrorBody() retained %q after read failure", got)
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("ReadErrorBody() exposed reader error: %v", err)
		}
	})

	t.Run("nil reader", func(t *testing.T) {
		t.Parallel()
		if body, err := ReadErrorBody(nil); err == nil || body != nil {
			t.Fatalf("ReadErrorBody(nil) = %q, %v", body, err)
		}
	})
}

func TestHandleErrorNeverExposesResponseBody(t *testing.T) {
	t.Parallel()
	logger := &mockLogger{}
	client := NewBaseClient(180*time.Second, logger)
	canaries := []string{
		"sk-error-body-canary",
		"prompt-fragment-error-body-canary",
		"moderation-error-body-canary",
		"html-error-body-canary",
	}
	body := []byte(`{"error":{"message":"prompt-fragment-error-body-canary",` +
		`"api_key":"sk-error-body-canary",` +
		`"flagged_input":"moderation-error-body-canary",` +
		`"html":"<html>html-error-body-canary</html>"}}`)

	for _, status := range []int{http.StatusBadRequest, http.StatusTeapot} {
		err := client.HandleError(status, body, "TestProvider", "test-model")
		span := &mockSpan{}
		errorType := RecordObservationError(span, err, "unknown")
		client.LogErrorMetadata(t.Context(), ErrorObservation{
			Operation: "ai_generate", Provider: "openai", ProviderAlias: "openai",
			ErrorType: errorType,
		})

		observed := fmt.Sprint(err, span.attributes, span.errors, logger.errorCalls)
		for _, canary := range canaries {
			if strings.Contains(observed, canary) {
				t.Fatalf("status %d observations exposed %q: %s", status, canary, observed)
			}
		}
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

func TestHandleError_Bare402IsRetryable(t *testing.T) {
	client := NewBaseClient(180*time.Second, nil)
	err := client.HandleError(http.StatusPaymentRequired, nil, "TestProvider", "test-model")
	var providerErr core.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected core.ProviderError, got %T", err)
	}
	if !providerErr.IsRetryable() {
		t.Fatal("bare 402 must be status-authoritative for provider-chain failover")
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
