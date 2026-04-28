//go:build integration

package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Integration tests for OpenAI client retry behavior.
// These tests involve real delays and should not run in CI unit test suites.
// Run with: go test -tags=integration ./...

func TestClient_RetryBehavior_Integration(t *testing.T) {
	tests := []struct {
		name          string
		failCount     int // Number of 429s before success
		maxRetries    int
		expectSuccess bool
		minDuration   time.Duration
	}{
		{
			name:          "success after one retry",
			failCount:     1,
			maxRetries:    3,
			expectSuccess: true,
			minDuration:   900 * time.Millisecond, // ~1s retry delay
		},
		{
			name:          "success after two retries",
			failCount:     2,
			maxRetries:    3,
			expectSuccess: true,
			minDuration:   2900 * time.Millisecond, // ~1s + 2s retry delays
		},
		{
			name:          "exhausts retries",
			failCount:     10, // More than max retries
			maxRetries:    3,
			expectSuccess: false,
			minDuration:   6900 * time.Millisecond, // ~1s + 2s + 4s retry delays
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var callCount int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := atomic.AddInt32(&callCount, 1)
				if int(count) <= tt.failCount {
					w.WriteHeader(http.StatusTooManyRequests)
					w.Write([]byte(`{"error": {"message": "Rate limit exceeded", "type": "rate_limit_error"}}`))
					return
				}
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{
					"id": "chatcmpl-123",
					"model": "gpt-4",
					"choices": [{"message": {"content": "Success after retries"}, "finish_reason": "stop"}],
					"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
				}`))
			}))
			defer server.Close()

			client := NewClient("test-key", server.URL, "", nil)

			start := time.Now()
			resp, err := client.GenerateResponse(
				context.Background(),
				"test prompt",
				&core.AIOptions{Model: "gpt-4"},
			)
			duration := time.Since(start)

			if tt.expectSuccess {
				if err != nil {
					t.Errorf("expected success, got error: %v", err)
				}
				if resp == nil || resp.Content != "Success after retries" {
					t.Errorf("unexpected response content")
				}
			} else {
				if err == nil {
					t.Error("expected error after exhausting retries")
				}
			}

			if duration < tt.minDuration {
				t.Errorf("expected minimum duration %v, got %v", tt.minDuration, duration)
			}
		})
	}
}

func TestClient_RateLimitWithRetryAfterHeader_Integration(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error": {"message": "Rate limit exceeded"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [{"message": {"content": "Success"}}],
			"usage": {"total_tokens": 10}
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, "", nil)

	start := time.Now()
	resp, err := client.GenerateResponse(
		context.Background(),
		"test",
		&core.AIOptions{Model: "gpt-4"},
	)
	duration := time.Since(start)

	if err != nil {
		t.Errorf("expected success after retry, got: %v", err)
	}
	if resp == nil || resp.Content != "Success" {
		t.Errorf("unexpected response")
	}
	if duration < 900*time.Millisecond {
		t.Errorf("expected retry delay of ~1s, got %v", duration)
	}
}
