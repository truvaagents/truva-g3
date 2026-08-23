package ai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers/openai"
	"github.com/truvaagents/truva-g3/core"
)

type openRouterBackupClient struct {
	generateCalls atomic.Int64
	streamCalls   atomic.Int64
}

func (*openRouterBackupClient) GenerateResponse(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
	return &core.AIResponse{Content: "backup", Provider: "backup"}, nil
}

func (client *openRouterBackupClient) Generate(context.Context, *core.AIRequest) (*core.AIResult, error) {
	client.generateCalls.Add(1)
	return &core.AIResult{
		Response:      &core.AIResponse{Content: "backup", Provider: "backup"},
		RequestReport: &core.AIRequestReport{Provider: "backup", Fingerprint: "backup-v1", Stable: true},
	}, nil
}

func (client *openRouterBackupClient) Stream(_ context.Context, _ *core.AIRequest, callback core.StreamCallback) (*core.AIResult, error) {
	client.streamCalls.Add(1)
	if err := callback(core.StreamChunk{Content: "backup", Delta: true}); err != nil {
		return nil, err
	}
	return &core.AIResult{
		Response:      &core.AIResponse{Content: "backup", Provider: "backup"},
		RequestReport: &core.AIRequestReport{Provider: "backup", Fingerprint: "backup-v1", Stable: true},
	}, nil
}

func (client *openRouterBackupClient) RequestFingerprint(context.Context, *core.AIRequest) (string, bool) {
	return "backup-v1", true
}

func TestOpenRouterCanonicalErrorsDriveChainFailover(t *testing.T) {
	tests := []struct {
		errorType    string
		code         int
		wantFailover bool
	}{
		{errorType: "invalid_request", code: 400},
		{errorType: "permission_denied", code: 403},
		{errorType: "authentication", code: 401, wantFailover: true},
		{errorType: "payment_required", code: 402, wantFailover: true},
		{errorType: "rate_limit_exceeded", code: 429, wantFailover: true},
		{errorType: "timeout", code: 504, wantFailover: true},
		{errorType: "provider_unavailable", code: 502, wantFailover: true},
	}
	for _, test := range tests {
		t.Run(test.errorType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"choices":[{"finish_reason":"error","error":{"code":` +
					strconv.Itoa(test.code) + `,"message":"private","metadata":{"error_type":"` + test.errorType + `"}}}]}`))
			}))
			defer server.Close()

			primary := openai.NewClient("key", server.URL, "openai.openrouter", &core.NoOpLogger{})
			primary.MaxRetries = 0
			backup := &openRouterBackupClient{}
			chain, err := ai.NewChain(ai.ClientEntry("openrouter", primary), ai.ClientEntry("backup", backup))
			if err != nil {
				t.Fatal(err)
			}
			result, err := chain.Generate(t.Context(), concreteOpenRouterRequest())
			if test.wantFailover {
				if err != nil || result == nil || result.Response == nil || result.Response.Content != "backup" || backup.generateCalls.Load() != 1 {
					t.Fatalf("result=%#v calls=%d error=%v", result, backup.generateCalls.Load(), err)
				}
				return
			}
			if err == nil || backup.generateCalls.Load() != 0 {
				t.Fatalf("result=%#v calls=%d error=%v", result, backup.generateCalls.Load(), err)
			}
		})
	}
}

func TestOpenRouterStringCodeGuardrailHTTPErrorDoesNotFailOver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"error":{"code":"403","message":"private","metadata":{"error_type":"permission_denied"}}}`))
	}))
	defer server.Close()

	primary := openai.NewClient("key", server.URL, "openai.openrouter", &core.NoOpLogger{})
	primary.MaxRetries = 0
	backup := &openRouterBackupClient{}
	chain, err := ai.NewChain(ai.ClientEntry("openrouter", primary), ai.ClientEntry("backup", backup))
	if err != nil {
		t.Fatal(err)
	}
	result, err := chain.Generate(t.Context(), concreteOpenRouterRequest())
	var providerErr core.ProviderError
	if result == nil || result.RequestReport == nil || !errors.As(err, &providerErr) ||
		providerErr.StatusCode() != http.StatusBadRequest || backup.generateCalls.Load() != 0 {
		t.Fatalf("result=%#v calls=%d error=%v", result, backup.generateCalls.Load(), err)
	}
}

func TestOpenRouterPartialStreamNeverFailsOver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"error\",\"error\":{\"code\":503,\"metadata\":{\"error_type\":\"provider_overloaded\"}}}]}\n\n"))
	}))
	defer server.Close()

	primary := openai.NewClient("key", server.URL, "openai.openrouter", &core.NoOpLogger{})
	primary.MaxRetries = 0
	backup := &openRouterBackupClient{}
	chain, err := ai.NewChain(ai.ClientEntry("openrouter", primary), ai.ClientEntry("backup", backup))
	if err != nil {
		t.Fatal(err)
	}
	var observed string
	result, err := chain.Stream(t.Context(), concreteOpenRouterRequest(), func(chunk core.StreamChunk) error {
		observed += chunk.Content
		return nil
	})
	if !errors.Is(err, core.ErrStreamPartiallyCompleted) || observed != "partial" || result == nil ||
		result.Response == nil || result.Response.Content != "partial" || backup.streamCalls.Load() != 0 {
		t.Fatalf("result=%#v observed=%q calls=%d error=%v", result, observed, backup.streamCalls.Load(), err)
	}
}

func TestOpenRouterPreContentInBandStreamErrorFailsOver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"error\",\"error\":{\"code\":503,\"metadata\":{\"error_type\":\"provider_overloaded\"}}}]}\n\n"))
	}))
	defer server.Close()

	primary := openai.NewClient("key", server.URL, "openai.openrouter", &core.NoOpLogger{})
	primary.MaxRetries = 0
	backup := &openRouterBackupClient{}
	chain, err := ai.NewChain(ai.ClientEntry("openrouter", primary), ai.ClientEntry("backup", backup))
	if err != nil {
		t.Fatal(err)
	}
	var observed string
	result, err := chain.Stream(t.Context(), concreteOpenRouterRequest(), func(chunk core.StreamChunk) error {
		observed += chunk.Content
		return nil
	})
	if err != nil || observed != "backup" || result == nil || result.Response == nil ||
		result.Response.Content != "backup" || backup.streamCalls.Load() != 1 {
		t.Fatalf("result=%#v observed=%q calls=%d error=%v", result, observed, backup.streamCalls.Load(), err)
	}
}

func concreteOpenRouterRequest() *core.AIRequest {
	return core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "openai/gpt-5.6-sol"})
}
