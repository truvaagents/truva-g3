package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

type captureLogger struct {
	fields []map[string]interface{}
}

func (logger *captureLogger) record(fields map[string]interface{}) {
	logger.fields = append(logger.fields, fields)
}

func (logger *captureLogger) Debug(string, map[string]interface{}) {}
func (logger *captureLogger) Info(_ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *captureLogger) Warn(string, map[string]interface{})  {}
func (logger *captureLogger) Error(string, map[string]interface{}) {}
func (logger *captureLogger) DebugWithContext(context.Context, string, map[string]interface{}) {
}
func (logger *captureLogger) InfoWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.record(fields)
}
func (logger *captureLogger) WarnWithContext(context.Context, string, map[string]interface{}) {
}
func (logger *captureLogger) ErrorWithContext(context.Context, string, map[string]interface{}) {
}

func TestClient_GenerateResponse_ResponseFormatIsNestedUnderGenerationConfig(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},
			"modelVersion":"gemini-test"
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, &core.NoOpLogger{})
	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model:          "default",
		ResponseFormat: "application/json",
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	if _, exists := captured["responseMimeType"]; exists {
		t.Fatalf("expected responseMimeType to not be top-level, got %#v", captured["responseMimeType"])
	}

	genConfig, ok := captured["generationConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected generationConfig object, got %#v", captured["generationConfig"])
	}
	if got := genConfig["responseMimeType"]; got != "application/json" {
		t.Fatalf("expected generationConfig.responseMimeType to be set, got %#v", got)
	}
}

func TestClient_GenerateResponse_MergesExtrasAndHeaders(t *testing.T) {
	var captured map[string]interface{}
	var capturedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		capturedHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},
			"modelVersion":"gemini-test"
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, &core.NoOpLogger{})
	client.defaultHeaders = map[string]string{
		"x-default":    "default",
		"Content-Type": "text/plain",
	}
	client.defaultExtra = map[string]interface{}{
		"topK":     3,
		"contents": "should-not-win",
	}

	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model:          "default",
		ResponseFormat: "application/json",
		Extra: map[string]interface{}{
			"topK": 7,
			"topP": 0.8,
		},
		Headers: map[string]string{
			"x-default":    "request",
			"Content-Type": "bad-value",
			"x-request":    "present",
		},
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	genConfig, ok := captured["generationConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected generationConfig object, got %#v", captured["generationConfig"])
	}
	if got := genConfig["responseMimeType"]; got != "application/json" {
		t.Fatalf("expected generationConfig.responseMimeType to be set, got %#v", got)
	}
	if got := captured["topK"]; got != float64(7) {
		t.Fatalf("expected request extra topK to override default extra, got %#v", got)
	}
	if got := captured["topP"]; got != 0.8 {
		t.Fatalf("expected request extra topP to be present, got %#v", got)
	}
	if got := captured["contents"]; got == "should-not-win" {
		t.Fatalf("expected framework-managed contents field to win, got %#v", got)
	}

	if got := capturedHeaders.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected protected Content-Type header to win, got %q", got)
	}
	if got := capturedHeaders.Get("x-default"); got != "request" {
		t.Fatalf("expected request header to override default header, got %q", got)
	}
	if got := capturedHeaders.Get("x-request"); got != "present" {
		t.Fatalf("expected request header to be applied, got %q", got)
	}
}

func TestClient_StreamResponse_AppliesHeaders(t *testing.T) {
	var capturedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi\"}]},\"index\":0}]}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\" there\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":2,\"totalTokenCount\":3}}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, &core.NoOpLogger{})
	client.defaultHeaders = map[string]string{
		"x-default": "default",
		"Accept":    "application/json",
	}

	var chunks []string
	_, err := client.StreamResponse(context.Background(), "hello", &core.AIOptions{
		Model: "default",
		Headers: map[string]string{
			"x-default": "request",
			"Accept":    "bad-value",
			"x-request": "present",
		},
	}, func(chunk core.StreamChunk) error {
		if chunk.Content != "" {
			chunks = append(chunks, chunk.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("StreamResponse returned error: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected streaming chunks to be received")
	}
	if got := capturedHeaders.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("expected protected Accept header to win, got %q", got)
	}
	if got := capturedHeaders.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type header to be set, got %q", got)
	}
	if got := capturedHeaders.Get("x-default"); got != "" {
		t.Fatalf("expected stream path to not apply default/request headers yet, got %q", got)
	}
}

func TestFactory_Create_CopiesHeadersAndExtra(t *testing.T) {
	factory := &Factory{}
	config := &ai.AIConfig{
		Headers: map[string]string{"x-test": "1"},
		Extra:   map[string]interface{}{"topP": 0.9},
	}

	clientAny := factory.Create(config)
	client, ok := clientAny.(*Client)
	if !ok {
		t.Fatalf("expected gemini client, got %T", clientAny)
	}

	config.Headers["x-test"] = "mutated"
	config.Extra["topP"] = 0.1

	if got := client.defaultHeaders["x-test"]; got != "1" {
		t.Fatalf("expected factory to copy default headers, got %q", got)
	}
	if got := client.defaultExtra["topP"]; got != 0.9 {
		t.Fatalf("expected factory to copy default extra, got %#v", got)
	}
}

func TestFactoryInitializationLogDoesNotExposeEndpoint(t *testing.T) {
	const endpoint = "https://gemini-endpoint-secret.example/v1beta"
	logger := &captureLogger{}
	factory := &Factory{}
	factory.Create(&ai.AIConfig{
		APIKey:  "test-key",
		BaseURL: endpoint,
		Logger:  logger,
	})

	if len(logger.fields) == 0 {
		t.Fatal("provider initialization log was not captured")
	}
	fields := logger.fields[0]
	if _, exists := fields["base_url"]; exists {
		t.Fatalf("initialization fields contain base_url: %#v", fields)
	}
	if fields["custom_endpoint"] != true {
		t.Fatalf("custom_endpoint = %#v, want true", fields["custom_endpoint"])
	}
	if strings.Contains(fmt.Sprint(fields), "gemini-endpoint-secret") {
		t.Fatalf("initialization fields leaked endpoint: %#v", fields)
	}
}
