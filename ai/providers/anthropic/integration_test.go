package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

type phase4RoundTripFunc func(*http.Request) (*http.Response, error)

type phase4ContextKey struct{}

func (function phase4RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type phase4Resolver struct {
	endpoint         ai.ResolvedEndpoint
	err              error
	wantContextValue string
	calls            atomic.Int64

	mu       sync.Mutex
	requests []ai.EndpointRequest
}

func (resolver *phase4Resolver) ResolveEndpoint(ctx context.Context, request ai.EndpointRequest) (ai.ResolvedEndpoint, error) {
	resolver.calls.Add(1)
	resolver.mu.Lock()
	resolver.requests = append(resolver.requests, request)
	resolver.mu.Unlock()
	if resolver.wantContextValue != "" && ctx.Value(phase4ContextKey{}) != resolver.wantContextValue {
		return ai.ResolvedEndpoint{}, errors.New("endpoint resolver lost request context")
	}
	return resolver.endpoint, resolver.err
}

func (resolver *phase4Resolver) capturedRequests() []ai.EndpointRequest {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return append([]ai.EndpointRequest(nil), resolver.requests...)
}

type phase4CredentialSource struct {
	credential       func(int64, ai.CredentialRequest) (ai.HeaderCredential, error)
	observerErr      error
	wantContextValue string
	calls            atomic.Int64
	rejections       atomic.Int64

	mu                sync.Mutex
	requests          []ai.CredentialRequest
	rejectionRequests []ai.CredentialRequest
	statuses          []int
}

func (source *phase4CredentialSource) Credential(ctx context.Context, request ai.CredentialRequest) (ai.HeaderCredential, error) {
	call := source.calls.Add(1)
	source.mu.Lock()
	source.requests = append(source.requests, request)
	source.mu.Unlock()
	if source.wantContextValue != "" && ctx.Value(phase4ContextKey{}) != source.wantContextValue {
		return ai.HeaderCredential{}, errors.New("credential source lost request context")
	}
	return source.credential(call, request)
}

func (source *phase4CredentialSource) CredentialRejected(_ context.Context, request ai.CredentialRequest, status int) error {
	source.rejections.Add(1)
	source.mu.Lock()
	source.rejectionRequests = append(source.rejectionRequests, request)
	source.statuses = append(source.statuses, status)
	source.mu.Unlock()
	return source.observerErr
}

func (source *phase4CredentialSource) capturedRequests() []ai.CredentialRequest {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]ai.CredentialRequest(nil), source.requests...)
}

type phase4CaptureLogger struct {
	mu      sync.Mutex
	entries []string
}

func (logger *phase4CaptureLogger) record(message string, fields map[string]interface{}) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.entries = append(logger.entries, message+" "+fmt.Sprint(fields))
}

func (logger *phase4CaptureLogger) Info(message string, fields map[string]interface{}) {
	logger.record(message, fields)
}
func (logger *phase4CaptureLogger) Error(message string, fields map[string]interface{}) {
	logger.record(message, fields)
}
func (logger *phase4CaptureLogger) Warn(message string, fields map[string]interface{}) {
	logger.record(message, fields)
}
func (logger *phase4CaptureLogger) Debug(message string, fields map[string]interface{}) {
	logger.record(message, fields)
}
func (logger *phase4CaptureLogger) InfoWithContext(_ context.Context, message string, fields map[string]interface{}) {
	logger.record(message, fields)
}
func (logger *phase4CaptureLogger) ErrorWithContext(_ context.Context, message string, fields map[string]interface{}) {
	logger.record(message, fields)
}
func (logger *phase4CaptureLogger) WarnWithContext(_ context.Context, message string, fields map[string]interface{}) {
	logger.record(message, fields)
}
func (logger *phase4CaptureLogger) DebugWithContext(_ context.Context, message string, fields map[string]interface{}) {
	logger.record(message, fields)
}

func (logger *phase4CaptureLogger) text() string {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	return strings.Join(logger.entries, "\n")
}

type phase4Attempt struct {
	url     string
	headers http.Header
	body    string
}

func phase4Response(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

const phase4SuccessBody = `{
  "id":"msg_phase4",
  "type":"message",
  "role":"assistant",
  "content":[{"type":"text","text":"ok"}],
  "model":"claude-test",
  "stop_reason":"end_turn",
  "usage":{"input_tokens":1,"output_tokens":1}
}`

func TestNewRequestClient_Phase4TransportIntegration(t *testing.T) {
	endpointURL, err := url.Parse("https://gateway.example/custom/messages?existing=kept")
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	resolverQuery := url.Values{
		"api-version": []string{"2026-07-01"},
		"credential":  []string{"query-secret"},
	}
	resolver := &phase4Resolver{endpoint: ai.ResolvedEndpoint{
		URL:             endpointURL,
		Deployment:      "tenant-deployment",
		RouteIdentity:   "tenant-primary",
		CredentialScope: "credential-scope-secret",
		Query:           resolverQuery,
	}, wantContextValue: "trace-context"}
	credentials := &phase4CredentialSource{credential: func(call int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", fmt.Sprintf("Bearer rotating-%d", call)), nil
	}, wantContextValue: "trace-context"}

	var attemptsMu sync.Mutex
	var attempts []phase4Attempt
	transport := phase4RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Context().Value(phase4ContextKey{}) != "trace-context" {
			return nil, errors.New("transport lost request context")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		_ = request.Body.Close()
		attemptsMu.Lock()
		attempts = append(attempts, phase4Attempt{
			url:     request.URL.String(),
			headers: request.Header.Clone(),
			body:    string(body),
		})
		attempt := len(attempts)
		attemptsMu.Unlock()
		if attempt == 1 {
			return phase4Response(request, http.StatusInternalServerError, `{"error":"retry"}`), nil
		}
		return phase4Response(request, http.StatusOK, phase4SuccessBody), nil
	})
	checkRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	callerClient := &http.Client{
		Transport:     transport,
		Timeout:       11 * time.Second,
		CheckRedirect: checkRedirect,
	}

	requestClient, err := ai.NewRequestClient(
		ai.WithProvider("anthropic"),
		ai.WithAPIKey("static-key-must-be-suppressed"),
		ai.WithModel("claude-sonnet-4-6"),
		ai.WithMaxRetries(1),
		ai.WithTimeout(2*time.Second),
		ai.WithHeaders(map[string]string{"X-Application": "present"}),
		ai.WithCredentialSource(credentials),
		ai.WithEndpointResolver(resolver),
		ai.WithHTTPClient(callerClient),
	)
	if err != nil {
		t.Fatalf("NewRequestClient returned error: %v", err)
	}
	originalTransport := reflect.ValueOf(transport).Pointer()
	if callerClient.Timeout != 11*time.Second || reflect.ValueOf(callerClient.Transport).Pointer() != originalTransport || callerClient.CheckRedirect == nil {
		t.Fatalf("caller HTTP client was mutated: %#v", callerClient)
	}
	// Mutating the caller-owned client after construction must not affect the
	// provider snapshot. The generated request below must continue to use the
	// original transport and timeout policy.
	callerClient.Transport = phase4RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("mutated caller transport was used")
	})
	callerClient.Timeout = time.Nanosecond
	callerClient.CheckRedirect = nil

	request := core.NewAIRequest("hello", "phase-4-routing")
	ctx := context.WithValue(t.Context(), phase4ContextKey{}, "trace-context")
	result, err := requestClient.Generate(ctx, request)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result == nil || result.Response == nil || result.Response.Content != "ok" {
		t.Fatalf("Generate result = %#v", result)
	}

	attemptsMu.Lock()
	capturedAttempts := append([]phase4Attempt(nil), attempts...)
	attemptsMu.Unlock()
	if len(capturedAttempts) != 2 {
		t.Fatalf("transport attempts = %d, want 2", len(capturedAttempts))
	}
	if capturedAttempts[0].body == "" || capturedAttempts[0].body != capturedAttempts[1].body {
		t.Fatalf("replayed bodies = %#v", capturedAttempts)
	}
	for index, attempt := range capturedAttempts {
		parsed, err := url.Parse(attempt.url)
		if err != nil {
			t.Fatalf("parse captured URL: %v", err)
		}
		if parsed.Path != "/custom/messages" || parsed.Query().Get("existing") != "kept" || parsed.Query().Get("api-version") != "2026-07-01" || parsed.Query().Get("credential") != "query-secret" {
			t.Fatalf("attempt %d URL = %q", index+1, attempt.url)
		}
		if attempt.headers.Get("Authorization") != fmt.Sprintf("Bearer rotating-%d", index+1) {
			t.Fatalf("attempt %d credential = %q", index+1, attempt.headers.Get("Authorization"))
		}
		if attempt.headers.Get("X-Api-Key") != "" {
			t.Fatalf("attempt %d retained static API key", index+1)
		}
		if attempt.headers.Get("X-Application") != "present" || attempt.headers.Get("Content-Type") != "application/json" {
			t.Fatalf("attempt %d headers = %#v", index+1, attempt.headers)
		}
	}

	endpointRequests := resolver.capturedRequests()
	if len(endpointRequests) != 1 || endpointRequests[0].Provider != "anthropic" || endpointRequests[0].Surface != "messages" || endpointRequests[0].Operation != "generate" || endpointRequests[0].Purpose != "phase-4-routing" || endpointRequests[0].ResolvedModel == "" {
		t.Fatalf("endpoint requests = %#v", endpointRequests)
	}
	credentialRequests := credentials.capturedRequests()
	if len(credentialRequests) != 2 {
		t.Fatalf("credential requests = %#v", credentialRequests)
	}
	for _, credentialRequest := range credentialRequests {
		if credentialRequest.RouteIdentity != "tenant-primary" || credentialRequest.Deployment != "tenant-deployment" || credentialRequest.CredentialScope != "credential-scope-secret" || credentialRequest.Operation != "generate" {
			t.Fatalf("credential request = %#v", credentialRequest)
		}
	}

	if endpointURL.RawQuery != "existing=kept" || !reflect.DeepEqual(resolverQuery, url.Values{"api-version": []string{"2026-07-01"}, "credential": []string{"query-secret"}}) {
		t.Fatalf("resolver-owned endpoint state was mutated: URL=%q query=%#v", endpointURL.String(), resolverQuery)
	}
	if result.RequestReport == nil || !result.RequestReport.Stable || len(result.RequestReport.Fingerprint) != 64 {
		t.Fatalf("request report = %#v", result.RequestReport)
	}
	lastAdjustment := result.RequestReport.Adjustments[len(result.RequestReport.Adjustments)-1]
	if lastAdjustment.Source != "endpoint-resolver" || lastAdjustment.Rule != "tenant-primary" || lastAdjustment.Path != "/route" {
		t.Fatalf("route adjustment = %#v", lastAdjustment)
	}
	reportText := fmt.Sprintf("%#v", result.RequestReport)
	for _, secret := range []string{"query-secret", "credential-scope-secret", "rotating-", "static-key-must-be-suppressed"} {
		if strings.Contains(reportText, secret) {
			t.Fatalf("request report exposed %q: %s", secret, reportText)
		}
	}
}

func TestNewRequestClient_Phase4CredentialRejectionObservation(t *testing.T) {
	logger := &phase4CaptureLogger{}
	observerSecret := "observer-error-secret"
	source := &phase4CredentialSource{
		credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
			return ai.NewHeaderCredential("X-Tenant-Auth", "credential-secret"), nil
		},
		observerErr: errors.New(observerSecret),
	}
	transport := phase4RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return phase4Response(request, http.StatusUnauthorized, `{"error":"unauthorized"}`), nil
	})

	client, err := ai.NewRequestClient(
		ai.WithProvider("anthropic"),
		ai.WithModel("claude-sonnet-4-6"),
		ai.WithMaxRetries(0),
		ai.WithLogger(logger),
		ai.WithCredentialSource(source),
		ai.WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewRequestClient returned error: %v", err)
	}
	result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "phase-4-rejection"))
	if err == nil {
		t.Fatal("Generate unexpectedly succeeded")
	}
	var providerError core.ProviderError
	if !errors.As(err, &providerError) || providerError.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("Generate error = %v, want original 401 provider error", err)
	}
	if strings.Contains(err.Error(), observerSecret) {
		t.Fatalf("observer error replaced provider error: %v", err)
	}
	if source.rejections.Load() != 1 {
		t.Fatalf("rejection observer calls = %d, want 1", source.rejections.Load())
	}
	source.mu.Lock()
	statuses := append([]int(nil), source.statuses...)
	source.mu.Unlock()
	if !reflect.DeepEqual(statuses, []int{http.StatusUnauthorized}) {
		t.Fatalf("observed statuses = %#v", statuses)
	}
	if result == nil || result.RequestReport == nil {
		t.Fatalf("failure result = %#v", result)
	}
	for _, secret := range []string{observerSecret, "credential-secret"} {
		if strings.Contains(logger.text(), secret) || strings.Contains(fmt.Sprintf("%#v", result.RequestReport), secret) {
			t.Fatalf("secret %q appeared in logs or report", secret)
		}
	}
	if !strings.Contains(logger.text(), "credential rejection observer failed") {
		t.Fatalf("observer failure was not logged diagnostically: %s", logger.text())
	}
}

func TestClient_Phase4CredentialRejectionObserverToleratesNilLogger(t *testing.T) {
	source := &phase4CredentialSource{
		observerErr: errors.New("observer failed"),
	}
	client := NewClient("", "", nil)
	client.credentialSource = source
	client.Logger = nil

	client.observeCredentialRejection(t.Context(), ai.CredentialRequest{}, http.StatusUnauthorized)

	if source.rejections.Load() != 1 {
		t.Fatalf("rejection observer calls = %d, want 1", source.rejections.Load())
	}
}

func TestNewRequestClient_Phase4RejectsInvalidRuntimeIntegration(t *testing.T) {
	validURL := &url.URL{Scheme: "https", Host: "gateway.example", Path: "/messages"}
	tests := []struct {
		name       string
		resolver   ai.EndpointResolver
		credential ai.CredentialSource
		want       string
		cause      error
	}{
		{name: "nil endpoint URL", resolver: &phase4Resolver{endpoint: ai.ResolvedEndpoint{RouteIdentity: "route"}}, want: "endpoint URL is nil"},
		{name: "invalid endpoint scheme", resolver: &phase4Resolver{endpoint: ai.ResolvedEndpoint{URL: &url.URL{Scheme: "ftp", Host: "gateway.example", Path: "/messages"}, RouteIdentity: "route"}}, want: "unsupported Anthropic endpoint scheme"},
		{name: "missing route identity", resolver: &phase4Resolver{endpoint: ai.ResolvedEndpoint{URL: validURL}}, want: "route identity is empty"},
		{name: "empty credential", credential: &phase4CredentialSource{credential: func(int64, ai.CredentialRequest) (ai.HeaderCredential, error) {
			return ai.NewHeaderCredential("Authorization", ""), nil
		}}, want: "credential value is empty"},
		{name: "invalid credential name", credential: &phase4CredentialSource{credential: func(int64, ai.CredentialRequest) (ai.HeaderCredential, error) {
			return ai.NewHeaderCredential("bad header", "value"), nil
		}}, want: "header name"},
		{name: "protected header collision", credential: &phase4CredentialSource{credential: func(int64, ai.CredentialRequest) (ai.HeaderCredential, error) {
			return ai.NewHeaderCredential("Content-Type", "secret"), nil
		}}, want: "conflicts with a prepared request header"},
	}
	credentialCause := errors.New("credential-service-secret")
	endpointCause := errors.New("endpoint-service-secret")
	tests = append(tests, struct {
		name       string
		resolver   ai.EndpointResolver
		credential ai.CredentialSource
		want       string
		cause      error
	}{
		name: "credential source failure",
		credential: &phase4CredentialSource{credential: func(int64, ai.CredentialRequest) (ai.HeaderCredential, error) {
			return ai.HeaderCredential{}, credentialCause
		}},
		want:  "credential acquisition failed",
		cause: credentialCause,
	}, struct {
		name       string
		resolver   ai.EndpointResolver
		credential ai.CredentialSource
		want       string
		cause      error
	}{
		name:     "endpoint resolver failure",
		resolver: &phase4Resolver{err: endpointCause},
		want:     "endpoint resolution failed",
		cause:    endpointCause,
	})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var networkCalls atomic.Int64
			options := []ai.ClientOption{
				ai.WithProvider("anthropic"),
				ai.WithAPIKey("static-key"),
				ai.WithModel("claude-sonnet-4-6"),
				ai.WithMaxRetries(0),
				ai.WithHTTPClient(&http.Client{Transport: phase4RoundTripFunc(func(request *http.Request) (*http.Response, error) {
					networkCalls.Add(1)
					return phase4Response(request, http.StatusOK, phase4SuccessBody), nil
				})}),
			}
			if test.resolver != nil {
				options = append(options, ai.WithEndpointResolver(test.resolver))
			}
			if test.credential != nil {
				options = append(options, ai.WithCredentialSource(test.credential))
			}
			client, err := ai.NewRequestClient(options...)
			if err != nil {
				t.Fatalf("NewRequestClient returned error: %v", err)
			}
			_, err = client.Generate(t.Context(), core.NewAIRequest("hello", "phase-4-invalid"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Generate error = %v, want %q", err, test.want)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("Generate error = %v, want wrapped cause", err)
			}
			if test.cause != nil && strings.Contains(err.Error(), test.cause.Error()) {
				t.Fatalf("integration cause detail leaked through error: %v", err)
			}
			if networkCalls.Load() != 0 {
				t.Fatalf("network calls = %d, want 0", networkCalls.Load())
			}
		})
	}
}

func TestNewRequestClient_Phase4FrameworkTimeoutDoesNotMutateHTTPClient(t *testing.T) {
	querySecret := "timeout-query-secret"
	resolver := &phase4Resolver{endpoint: ai.ResolvedEndpoint{
		URL:           &url.URL{Scheme: "https", Host: "gateway.example", Path: "/messages"},
		RouteIdentity: "timeout-route",
		Query:         url.Values{"credential": []string{querySecret}},
	}}
	callerClient := &http.Client{Transport: phase4RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	client, err := ai.NewRequestClient(
		ai.WithProvider("anthropic"),
		ai.WithAPIKey("static-key"),
		ai.WithModel("claude-sonnet-4-6"),
		ai.WithMaxRetries(0),
		ai.WithTimeout(20*time.Millisecond),
		ai.WithEndpointResolver(resolver),
		ai.WithHTTPClient(callerClient),
	)
	if err != nil {
		t.Fatalf("NewRequestClient returned error: %v", err)
	}
	started := time.Now()
	_, err = client.Generate(context.Background(), core.NewAIRequest("hello", "phase-4-timeout"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Generate error = %v, want deadline exceeded", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("framework timeout was not enforced promptly: %v", time.Since(started))
	}
	if callerClient.Timeout != 0 {
		t.Fatalf("caller HTTP timeout was mutated: %v", callerClient.Timeout)
	}
	if strings.Contains(err.Error(), querySecret) {
		t.Fatalf("transport error exposed resolved query: %v", err)
	}
}

func TestNewRequestClient_Phase4NilTransportUsesDefaultWithoutMutatingCaller(t *testing.T) {
	callerClient := &http.Client{Timeout: 3 * time.Second}
	requestClient, err := (&Factory{}).CreateRequestClient(
		&ai.AIConfig{APIKey: "static-key", Timeout: 180 * time.Second},
		ai.ProviderIntegrationConfig{HTTPClient: callerClient},
	)
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}
	client := requestClient.(*Client)
	if callerClient.Transport != nil {
		t.Fatalf("caller transport was mutated: %T", callerClient.Transport)
	}
	if client.HTTPClient == callerClient || client.HTTPClient.Transport != http.DefaultTransport || client.HTTPClient.Timeout != callerClient.Timeout {
		t.Fatalf("provider HTTP client = %#v", client.HTTPClient)
	}
}

func TestNewRequestClient_Phase4RetainedIntegrationsAreConcurrentSafe(t *testing.T) {
	resolver := &phase4Resolver{endpoint: ai.ResolvedEndpoint{
		URL:           &url.URL{Scheme: "https", Host: "gateway.example", Path: "/messages"},
		RouteIdentity: "concurrent-route",
	}}
	source := &phase4CredentialSource{credential: func(call int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", fmt.Sprintf("Bearer %d", call)), nil
	}}
	transport := phase4RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") == "" {
			return nil, errors.New("missing credential")
		}
		return phase4Response(request, http.StatusOK, phase4SuccessBody), nil
	})
	client, err := ai.NewRequestClient(
		ai.WithProvider("anthropic"),
		ai.WithModel("claude-sonnet-4-6"),
		ai.WithMaxRetries(0),
		ai.WithCredentialSource(source),
		ai.WithEndpointResolver(resolver),
		ai.WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewRequestClient returned error: %v", err)
	}

	const workers = 24
	request := core.NewAIRequest("hello", "phase-4-concurrent")
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := client.Generate(context.Background(), request)
			if err != nil {
				errorsFound <- err
				return
			}
			if result == nil || result.Response == nil || result.Response.Content != "ok" {
				errorsFound <- errors.New("missing response")
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent Generate: %v", err)
	}
	if source.calls.Load() != workers || resolver.calls.Load() != workers {
		t.Fatalf("retained integration calls = credentials %d, resolver %d, want %d", source.calls.Load(), resolver.calls.Load(), workers)
	}
}

func TestNewRequestClient_Phase4StreamingUsesRouteCredentialAndTransport(t *testing.T) {
	resolver := &phase4Resolver{endpoint: ai.ResolvedEndpoint{
		URL:           &url.URL{Scheme: "https", Host: "stream.example", Path: "/tenant/messages"},
		RouteIdentity: "stream-route",
	}}
	source := &phase4CredentialSource{credential: func(_ int64, _ ai.CredentialRequest) (ai.HeaderCredential, error) {
		return ai.NewHeaderCredential("Authorization", "Bearer stream"), nil
	}}
	transport := phase4RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/tenant/messages" || request.Header.Get("Authorization") != "Bearer stream" || request.Header.Get("Accept") != "text/event-stream" {
			return nil, fmt.Errorf("unexpected streaming request: URL=%s headers=%v", request.URL, request.Header)
		}
		body := strings.Join([]string{
			`data: {"type":"message_start","message":{"model":"claude-test","usage":{"input_tokens":1}}}`,
			`data: {"type":"content_block_delta","delta":{"text":"hello"}}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`,
			"",
		}, "\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	requestClient, err := ai.NewRequestClient(
		ai.WithProvider("anthropic"),
		ai.WithModel("claude-sonnet-4-6"),
		ai.WithCredentialSource(source),
		ai.WithEndpointResolver(resolver),
		ai.WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewRequestClient returned error: %v", err)
	}
	streaming, ok := requestClient.(core.StreamingAIRequestClient)
	if !ok {
		t.Fatalf("request client %T is not streaming-capable", requestClient)
	}
	var content strings.Builder
	result, err := streaming.Stream(t.Context(), core.NewAIRequest("hello", "phase-4-stream"), func(chunk core.StreamChunk) error {
		content.WriteString(chunk.Content)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if content.String() != "hello" || result == nil || result.Response == nil || result.Response.Content != "hello" {
		t.Fatalf("stream content=%q result=%#v", content.String(), result)
	}
	if source.calls.Load() != 1 || resolver.calls.Load() != 1 {
		t.Fatalf("stream integration calls = credentials %d resolver %d", source.calls.Load(), resolver.calls.Load())
	}
}
