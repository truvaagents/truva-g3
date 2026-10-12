package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

const retrySuccess = `{"model":"openai/gpt-5.6-sol","choices":[{"message":{"content":"recovered"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
const retry429 = `{"error":{"code":429,"metadata":{"error_type":"rate_limit_exceeded"}}}`
const retry502 = `{"model":"openai/gpt-5.6-sol","choices":[{"message":{"content":"failed-attempt"},"error":{"code":502,"metadata":{"error_type":"provider_unavailable"}}}],"usage":{"total_tokens":7}}`

type retryFrame struct {
	status int
	body   string
	err    error
}
type retryBody struct {
	io.Reader
	closes int
}

func (b *retryBody) Close() error { b.closes++; return nil }

func TestQuotaHTTP429SkipsSameProviderRetriesButChainFailsOver(t *testing.T) {
	for _, provider := range []string{"openai", "openai.groq", openRouterProviderAlias} {
		for _, tc := range []struct {
			name, body            string
			recover               bool
			wantFirst, wantBackup int
		}{
			{"quota", `{"error":{"code":"insufficient_quota","type":"insufficient_quota","metadata":{"error_type":"insufficient_quota"},"message":"account quota exhausted"}}`, false, 1, 1},
			{"transient-recovered", `{"error":{"code":429,"type":"rate_limit_exceeded","metadata":{"error_type":"rate_limit_exceeded"}}}`, true, 2, 0},
			{"transient-exhausted", `{"error":{"code":429,"type":"rate_limit_exceeded","metadata":{"error_type":"rate_limit_exceeded"}}}`, false, 3, 1},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				firstCalls, backupCalls := 0, 0
				first := NewClient("synthetic", "https://synthetic.invalid", provider, &core.NoOpLogger{})
				first.MaxRetries, first.RetryDelay = 2, 0
				first.HTTPClient = &http.Client{Transport: googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					_ = req.Body.Close()
					firstCalls++
					status, body := 429, tc.body
					if tc.recover && firstCalls == 2 {
						status, body = 200, retrySuccess
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
				})}
				backup := NewClient("synthetic", "https://backup.invalid", "openai", &core.NoOpLogger{})
				backup.HTTPClient = &http.Client{Transport: googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					_ = req.Body.Close()
					backupCalls++
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(retrySuccess)), Request: req}, nil
				})}
				chain, err := ai.NewChain(ai.ClientEntry("primary", first), ai.ClientEntry("backup", backup))
				if err != nil {
					t.Fatal(err)
				}
				response, err := chain.GenerateResponse(t.Context(), "request", &core.AIOptions{Model: "gpt-4.1"})
				if err != nil || response == nil || response.Content != "recovered" || firstCalls != tc.wantFirst || backupCalls != tc.wantBackup {
					t.Fatalf("first=%d backup=%d response=%+v error=%v", firstCalls, backupCalls, response, err)
				}
			})
		}
	}
}

func TestDecodedRetryUsesOneBudget(t *testing.T) {
	for _, test := range []struct {
		name              string
		frames            []retryFrame
		retries, attempts int
		wantError         bool
	}{
		{"top_level", []retryFrame{{200, retry429, nil}, {200, retrySuccess, nil}}, 1, 2, false},
		{"choice_level", []retryFrame{{200, retry502, nil}, {200, retrySuccess, nil}}, 1, 2, false},
		{"mixed_http_decode_exhaustion", []retryFrame{{503, `{}`, nil}, {200, retry502, nil}, {200, retrySuccess, nil}}, 1, 2, true},
		{"mixed_http_decode_recovery", []retryFrame{{503, `{}`, nil}, {200, retry429, nil}, {200, retrySuccess, nil}}, 2, 3, false},
		{"transport_then_decode", []retryFrame{{0, "", errors.New("synthetic transport")}, {200, retry429, nil}, {200, retrySuccess, nil}}, 2, 3, false},
		{"explicit_zero", []retryFrame{{200, retry502, nil}}, 0, 1, true},
		{"billing_429", []retryFrame{{200, `{"error":{"code":429,"metadata":{"error_type":"token_limit_exceeded"}}}`, nil}}, 3, 1, true},
		{"image_502", []retryFrame{{200, `{"error":{"code":502,"metadata":{"error_type":"image_download_failed"}}}`, nil}}, 3, 1, true},
		{"unmapped_default_500", []retryFrame{{200, `{"error":{}}`, nil}}, 3, 1, true},
		{"malformed", []retryFrame{{200, `{`, nil}}, 3, 1, true},
		{"auth", []retryFrame{{401, `{}`, nil}}, 3, 1, true},
		{"auth_with_conflicting_transient_envelope", []retryFrame{{401, retry429, nil}}, 3, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient("key", "https://synthetic.invalid/v1", openRouterProviderAlias, &core.NoOpLogger{})
			client.MaxRetries, client.RetryDelay = test.retries, 0
			calls := 0
			var firstRequest string
			var bodies []*retryBody
			client.HTTPClient = &http.Client{Transport: googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				data, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				_ = req.Body.Close()
				if calls == 0 {
					firstRequest = string(data)
				} else if string(data) != firstRequest {
					t.Fatal("retry changed prepared request")
				}
				if calls >= len(test.frames) {
					t.Fatal("unexpected wire attempt")
				}
				frame := test.frames[calls]
				calls++
				if frame.err != nil {
					return nil, frame.err
				}
				body := &retryBody{Reader: strings.NewReader(frame.body)}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: frame.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: body, Request: req}, nil
			})}
			result, err := client.Generate(t.Context(), core.NewAIRequestFromLegacy("synthetic", "", &core.AIOptions{Model: "openai/gpt-5.6-sol"}))
			if calls != test.attempts || (err != nil) != test.wantError || result == nil || result.RequestReport == nil {
				t.Fatalf("calls=%d result=%#v err=%v", calls, result, err)
			}
			for _, body := range bodies {
				if body.closes != 1 {
					t.Fatalf("body closed %d times", body.closes)
				}
			}
			if !test.wantError && (result.Response.Content != "recovered" || result.Response.Usage.TotalTokens != 5) {
				t.Fatalf("failed content/usage merged: %#v", result.Response)
			}
			if (test.name == "explicit_zero" || test.name == "mixed_http_decode_exhaustion") && (result.Response == nil || result.Response.Content != "failed-attempt") {
				t.Fatal("terminal partial result lost")
			}
		})
	}
}

func TestDecodedRetryPolicyPreservesPublicFlags(t *testing.T) {
	for _, tc := range []struct {
		typ    string
		status int
		want   bool
	}{
		{"rate_limit_exceeded", 0, true}, {"provider_unavailable", 0, true}, {"provider_overloaded", 0, true}, {"timeout", 0, true}, {"server", 0, true},
		{"", 429, true}, {"", 500, true}, {"", 502, true}, {"", 503, true}, {"", 504, true}, {"", 501, false}, {"", 0, false},
		{"token_limit_exceeded", 429, false}, {"insufficient_quota", 429, false}, {"authentication", 500, false}, {"image_download_failed", 502, false}, {"unknown", 500, false},
	} {
		if got := sameProviderRetryStatus(tc.status, tc.typ, validProviderErrorStatus(tc.status)); got != tc.want {
			t.Errorf("type=%s status=%d retry=%v", tc.typ, tc.status, got)
		}
	}
}

func TestOpenRouterHTTPRetryNarrowing(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body string
		status, attempts     int
	}{
		{"unknown_429", openRouterProviderAlias, `{"error":{"metadata":{"error_type":"future_provider_error"}}}`, 429, 1},
		{"unknown_500", openRouterProviderAlias, `{"error":{"metadata":{"error_type":"future_provider_error"}}}`, 500, 1},
		{"unknown_502", openRouterProviderAlias, `{"error":{"metadata":{"error_type":"future_provider_error"}}}`, 502, 1},
		{"unknown_503", openRouterProviderAlias, `{"error":{"metadata":{"error_type":"future_provider_error"}}}`, 503, 1},
		{"status_only_501", openRouterProviderAlias, `{}`, 501, 1},
		{"status_only_505", openRouterProviderAlias, `{}`, 505, 1},
		{"status_only_429", openRouterProviderAlias, `{}`, 429, 2},
		{"status_only_503", openRouterProviderAlias, `{}`, 503, 2},
		{"native_501_unchanged", "openai", `{}`, 501, 2},
		{"native_505_unchanged", "openai", `{}`, 505, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient("key", "https://synthetic.invalid/v1", tc.provider, &core.NoOpLogger{})
			client.MaxRetries, client.RetryDelay = 1, 0
			calls := 0
			var bodies []*retryBody
			client.HTTPClient = &http.Client{Transport: googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				_ = req.Body.Close()
				calls++
				if calls > tc.attempts {
					t.Fatal("unexpected HTTP retry")
				}
				status, content := tc.status, tc.body
				if calls == 2 {
					status, content = http.StatusOK, retrySuccess
				}
				body := &retryBody{Reader: strings.NewReader(content)}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: body, Request: req}, nil
			})}
			result, err := client.Generate(t.Context(), core.NewAIRequestFromLegacy("synthetic", "", &core.AIOptions{Model: "gpt-5.6-sol"}))
			if calls != tc.attempts || result == nil || result.RequestReport == nil {
				t.Fatalf("attempts=%d want=%d result=%#v err=%v", calls, tc.attempts, result, err)
			}
			if tc.attempts == 1 {
				var providerErr core.ProviderError
				if !errors.As(err, &providerErr) || providerErr.StatusCode() != tc.status {
					t.Fatalf("terminal error lost HTTP status: %v", err)
				}
			} else if err != nil || result.Response == nil || result.Response.Content != "recovered" {
				t.Fatalf("expected recovery: result=%#v err=%v", result, err)
			}
			for _, body := range bodies {
				if body.closes != 1 {
					t.Fatalf("body closed %d times", body.closes)
				}
			}
		})
	}
}

func TestDecodedStreamRetryStopsAfterAnyOutput(t *testing.T) {
	const success = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	const failure = "data: " + retry429 + "\n\n"
	for _, mode := range []string{"before_output", "after_output", "callback_stop"} {
		t.Run(mode, func(t *testing.T) {
			client := NewClient("key", "https://synthetic.invalid/v1", openRouterProviderAlias, &core.NoOpLogger{})
			client.MaxRetries, client.RetryDelay = 2, 0
			calls, callbacks := 0, 0
			client.HTTPClient = &http.Client{Transport: googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				_ = req.Body.Close()
				calls++
				body := success
				if calls == 1 {
					body = failure
					if mode != "before_output" {
						body = "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" + failure
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			result, err := client.Stream(t.Context(), core.NewAIRequestFromLegacy("synthetic", "", &core.AIOptions{Model: "openai/gpt-5.6-sol"}), func(core.StreamChunk) error {
				callbacks++
				if mode == "callback_stop" {
					return errors.New("stop")
				}
				return nil
			})
			if result == nil || result.RequestReport == nil || callbacks == 0 {
				t.Fatalf("missing result/callback: %#v %v", result, err)
			}
			if mode == "before_output" {
				if calls != 2 || err != nil {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			} else if calls != 1 {
				t.Fatalf("replayed stream: %d", calls)
			}
			if mode == "after_output" && !errors.Is(err, core.ErrStreamPartiallyCompleted) {
				t.Fatalf("partial sentinel lost: %v", err)
			}
		})
	}
}

func TestDecodedRetryAfterHonorsCallerDeadline(t *testing.T) {
	client := NewClient("key", "https://synthetic.invalid/v1", openRouterProviderAlias, &core.NoOpLogger{})
	client.MaxRetries, client.RetryDelay = 3, 0
	calls := 0
	client.HTTPClient = &http.Client{Transport: googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		_ = req.Body.Close()
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Retry-After": {"3600"}}, Body: io.NopCloser(strings.NewReader(retry429)), Request: req}, nil
	})}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	result, err := client.Generate(ctx, core.NewAIRequestFromLegacy("synthetic", "", &core.AIOptions{Model: "openai/gpt-5.6-sol"}))
	if calls != 1 || !errors.Is(err, context.DeadlineExceeded) || result == nil || result.RequestReport == nil {
		t.Fatalf("calls=%d result=%#v err=%v", calls, result, err)
	}
}
