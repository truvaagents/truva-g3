package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type phase3AnthropicMiddleware struct {
	calls atomic.Int64
}

func (*phase3AnthropicMiddleware) Name() string                  { return "anthropic-test-middleware" }
func (*phase3AnthropicMiddleware) Version() string               { return "1" }
func (*phase3AnthropicMiddleware) StablePolicyFingerprint() bool { return true }
func (m *phase3AnthropicMiddleware) Apply(_ context.Context, editor requestpolicy.RequestEditor) error {
	m.calls.Add(1)
	return editor.SetHeader("X-Policy-Middleware", "enabled")
}

func TestNewRequestClient_WiresAnthropicApplicationPolicy(t *testing.T) {
	var capturedBody []byte
	var capturedHeaders http.Header
	transport := phase4RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var err error
		capturedBody, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		capturedHeaders = request.Header.Clone()
		return phase4Response(request, http.StatusOK, phase4SuccessBody), nil
	})
	client, err := ai.NewRequestClient(
		ai.WithProvider("anthropic"),
		ai.WithAPIKey("anthropic-key"),
		ai.WithModel("claude-sonnet-5"),
		ai.WithMaxTokens(100),
		ai.WithRequestRules(core.AIProviderPatch{
			Name:     "application-sampling-override",
			Version:  "1",
			Selector: core.AIProviderSelector{Provider: "anthropic", Model: "claude-sonnet-5"},
			Set:      map[string]interface{}{`/temperature`: 0.3},
		}),
		ai.WithRequestMiddleware(&phase3AnthropicMiddleware{}),
		ai.WithCompatibilityMode(requestpolicy.CompatibilityStrict),
		ai.WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewRequestClient returned error: %v", err)
	}
	request := core.NewAIRequest("hello", "phase-3-test")
	request.Generation.Temperature = core.SetAIParameter(float32(0.1))
	result, err := client.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("decode prepared request: %v", err)
	}
	if body["temperature"] != 0.3 {
		t.Fatalf("application rule temperature = %#v, want 0.3", body["temperature"])
	}
	if capturedHeaders.Get("X-Policy-Middleware") != "enabled" {
		t.Fatalf("middleware header = %q", capturedHeaders.Get("X-Policy-Middleware"))
	}
	if result.RequestReport == nil || !result.RequestReport.Stable || len(result.RequestReport.Fingerprint) != 64 {
		t.Fatalf("request report = %#v", result.RequestReport)
	}
	wantSources := []string{"built-in-rule", "app-rule", "middleware"}
	if len(result.RequestReport.Adjustments) != len(wantSources) {
		t.Fatalf("adjustments = %#v", result.RequestReport.Adjustments)
	}
	for index, source := range wantSources {
		if result.RequestReport.Adjustments[index].Source != source {
			t.Fatalf("adjustment %d source = %q, want %q", index, result.RequestReport.Adjustments[index].Source, source)
		}
	}
}

func TestNewRequestClient_RetainedMiddlewareIsConcurrentSafe(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	middleware := &phase3AnthropicMiddleware{}
	client, err := ai.NewRequestClient(
		ai.WithProvider("anthropic"),
		ai.WithAPIKey(""),
		ai.WithModel("claude-sonnet-4-6"),
		ai.WithRequestMiddleware(middleware),
	)
	if err != nil {
		t.Fatalf("NewRequestClient returned error: %v", err)
	}
	request := core.NewAIRequest("hello", "phase-3-concurrency")

	const workers = 24
	var wait sync.WaitGroup
	errorsFound := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := client.Generate(context.Background(), request)
			if err == nil || err.Error() != "anthropic API key not configured" {
				errorsFound <- err
				return
			}
			if result == nil || result.RequestReport == nil || !result.RequestReport.Stable {
				errorsFound <- errors.New("missing stable request report")
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent Generate: %v", err)
	}
	if calls := middleware.calls.Load(); calls != workers {
		t.Fatalf("middleware calls = %d, want %d", calls, workers)
	}
}

func TestAnthropicFactory_ValidatedConstructionErrors(t *testing.T) {
	factory := &Factory{}
	if _, err := factory.CreateValidated(nil); err == nil || !strings.Contains(err.Error(), "config is nil") {
		t.Fatalf("CreateValidated(nil) error = %v", err)
	}
	_, err := factory.CreateRequestClient(&ai.AIConfig{}, ai.ProviderIntegrationConfig{
		RequestRules: []core.AIProviderPatch{{
			Selector: core.AIProviderSelector{AllProviders: true},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "rule name is required") {
		t.Fatalf("invalid integration error = %v", err)
	}
}

func TestAnthropicFactory_Phase4StaticEndpointValidation(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "unsupported scheme", baseURL: "ftp://gateway.example/v1", want: "unsupported"},
		{name: "missing host", baseURL: "https:///v1", want: "host is empty"},
		{name: "user information", baseURL: "https://user:secret@gateway.example/v1", want: "user information"},
		{name: "query", baseURL: "https://gateway.example/v1?credential=secret", want: "query parameters"},
		{name: "fragment", baseURL: "https://gateway.example/v1#secret", want: "fragment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (&Factory{}).CreateValidated(&ai.AIConfig{BaseURL: test.baseURL})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CreateValidated error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "credential=secret") || strings.Contains(err.Error(), "user:secret") {
				t.Fatalf("endpoint validation error exposed URL credentials: %v", err)
			}
		})
	}
}
