package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
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

func TestDecodedAttemptSuccessLogging(t *testing.T) {
	for _, tc := range []struct {
		name      string
		recover   bool
		terminal  error
		nilLogger bool
	}{
		{name: "first attempt"},
		{name: "recovered", recover: true},
		{name: "terminal error", terminal: errors.New("classified failure")},
		{name: "partial stream", terminal: errors.Join(core.ErrStreamPartiallyCompleted, io.ErrUnexpectedEOF)},
		{name: "nil logger", nilLogger: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := &mockLogger{}
			tracing := &mockTelemetry{}
			client := NewBaseClient(time.Second, logger)
			client.Telemetry = tracing
			client.MaxRetries, client.RetryDelay = 1, 0
			if tc.nilLogger {
				client.Logger = nil
			}
			calls := 0
			var bodies []*trackingReadCloser
			client.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				body := &trackingReadCloser{Reader: strings.NewReader("private response content")}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: request}, nil
			})
			ctx := core.WithRequestID(t.Context(), "decoded-success-request")
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://synthetic.invalid", nil)
			if err != nil {
				t.Fatal(err)
			}
			wantResult := &core.AIResult{Response: &core.AIResponse{Content: "private answer"}}
			result, err := client.ExecuteWithRetryDecoded(ctx, req, nil, func(context.Context, *http.Response) DecodedAttempt {
				if tc.recover && calls == 1 {
					return DecodedAttempt{Err: errors.New("temporary"), Retryable: true, SemanticStatus: 503}
				}
				// Make response consumption distinguishable from HTTP-header latency.
				time.Sleep(2 * time.Millisecond)
				return DecodedAttempt{Result: wantResult, Err: tc.terminal, Retryable: tc.terminal != nil}
			})
			wantCalls := 1
			if tc.recover || (tc.terminal != nil && !errors.Is(tc.terminal, core.ErrStreamPartiallyCompleted)) {
				wantCalls = 2
			}
			if !errors.Is(err, tc.terminal) || result != wantResult || calls != wantCalls || len(tracing.spans) != wantCalls {
				t.Fatalf("result=%p err=%v calls=%d spans=%d", result, err, calls, len(tracing.spans))
			}
			for _, body := range bodies {
				if !body.closed {
					t.Fatal("decoded response body was not closed")
				}
			}
			var success []map[string]interface{}
			for _, fields := range append(append([]map[string]interface{}{}, logger.debugCalls...), logger.infoCalls...) {
				if fields["operation"] == "ai_http_success" || fields["operation"] == "ai_request_recovery" {
					success = append(success, fields)
				}
			}
			if tc.terminal != nil || tc.nilLogger {
				if len(success) != 0 {
					t.Fatalf("unexpected success log: %#v", success)
				}
				return
			}
			if len(success) != 1 {
				t.Fatalf("success logs = %#v", success)
			}
			lastSpan := tracing.spans[len(tracing.spans)-1]
			duration, ok := success[0]["duration_ms"].(int64)
			if !ok || duration < 2 || duration != lastSpan.attributes["ai.attempt_duration_ms"] || !lastSpan.ended {
				t.Fatalf("log duration=%v span=%#v", success[0]["duration_ms"], lastSpan)
			}
			want := map[string]interface{}{"request_id": "decoded-success-request", "duration_ms": duration}
			if tc.recover {
				want["operation"], want["status"] = "ai_request_recovery", "recovered"
				want["successful_attempt"], want["total_attempts"] = 2, 2
				if len(logger.infoCalls) != 1 {
					t.Fatalf("recovery not logged once at INFO: %#v", logger.infoCalls)
				}
			} else {
				want["operation"], want["status"], want["status_code"] = "ai_http_success", "success", http.StatusOK
				if len(logger.debugCalls) != 1 || len(logger.infoCalls) != 0 {
					t.Fatalf("first success not DEBUG-only: %#v / %#v", logger.debugCalls, logger.infoCalls)
				}
			}
			if !reflect.DeepEqual(success[0], want) {
				t.Fatalf("success fields = %#v, want %#v", success[0], want)
			}
		})
	}
}
