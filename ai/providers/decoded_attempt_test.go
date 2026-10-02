package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestDecodedOperationCustomAdapterAndCredentials(t *testing.T) {
	b := NewBaseClient(time.Second, nil)
	b.MaxRetries, b.RetryDelay = 1, 0
	prepared, calls := 0, 0
	b.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != strings.Repeat("x", calls) {
			t.Fatal("credential was not acquired per attempt")
		}
		_ = req.Body.Close()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("custom response")), Request: req}, nil
	})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://synthetic.invalid", strings.NewReader("immutable"))
	result, err := b.ExecuteWithRetryDecoded(t.Context(), req, func(_ context.Context, r *http.Request) error {
		prepared++
		r.Header.Set("Authorization", strings.Repeat("x", prepared))
		return nil
	}, func(_ context.Context, _ *http.Response) DecodedAttempt {
		if calls == 1 {
			return DecodedAttempt{Err: errors.New("classified temporary failure"), Retryable: true, SemanticStatus: 503}
		}
		return DecodedAttempt{Result: &core.AIResult{Response: &core.AIResponse{Content: "success"}}}
	})
	if err != nil || calls != 2 || prepared != 2 || result.Response.Content != "success" {
		t.Fatalf("calls=%d prepared=%d result=%#v err=%v", calls, prepared, result, err)
	}
}

func TestDecodedOperationValidationAndTerminalTransport(t *testing.T) {
	b := NewBaseClient(time.Second, nil)
	b.MaxRetries, b.RetryDelay = 1, 0
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://synthetic.invalid", strings.NewReader("input"))
	if _, err := b.ExecuteWithRetryDecoded(t.Context(), req, nil, nil); err == nil {
		t.Fatal("nil decoder accepted")
	}
	calls := 0
	b.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		_ = r.Body.Close()
		if calls == 2 {
			return nil, errors.New("terminal transport failure")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("error")), Request: r}, nil
	})
	result, err := b.ExecuteWithRetryDecoded(t.Context(), req, nil, func(context.Context, *http.Response) DecodedAttempt {
		return DecodedAttempt{Result: &core.AIResult{Response: &core.AIResponse{Content: "old failed attempt"}}, Err: errors.New("temporary"), Retryable: true}
	})
	if err == nil || result != nil || calls != 2 {
		t.Fatalf("stale failed content survived: %#v %v calls=%d", result, err, calls)
	}
}

func TestDecodedSemanticRetryAfterKeepsHTTPStatus(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	for _, header := range []string{"5", now.Add(5 * time.Second).Format(http.TimeFormat)} {
		response := &http.Response{StatusCode: 200, Header: http.Header{"Retry-After": {header}}}
		for _, status := range []int{429, 503} {
			delay, source := retryDelayForStatus(time.Second, response, status, now)
			if delay != 5*time.Second || source != "retry_after" || response.StatusCode != 200 {
				t.Fatalf("delay=%v source=%s response=%d", delay, source, response.StatusCode)
			}
		}
		if delay, source := retryDelayForStatus(time.Second, response, 502, now); delay != time.Second || source != "exponential" {
			t.Fatal("ineligible semantic status used Retry-After")
		}
	}
}
