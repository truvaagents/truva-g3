package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestClient_GenerateResponse_MergesExtrasResponseFormatAndHeaders(t *testing.T) {
	var capturedBody map[string]interface{}
	var capturedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		capturedHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1",
			"type":"message",
			"role":"assistant",
			"content":[{"type":"text","text":"ok"}],
			"model":"claude-test",
			"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	client := NewClient("anthropic-key", server.URL, &core.NoOpLogger{})
	client.defaultHeaders = map[string]string{
		"x-default":         "default",
		"anthropic-version": "should-not-win",
	}
	client.defaultExtra = map[string]interface{}{
		"top_p": 0.9,
		"model": "should-not-win",
	}

	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model:          "default",
		ResponseFormat: "json",
		Extra: map[string]interface{}{
			"top_k": 7,
			"top_p": 0.8,
		},
		Headers: map[string]string{
			"x-default":         "request",
			"anthropic-version": "wrong",
			"x-request":         "present",
		},
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	if got := capturedBody["response_format"]; got != "json" {
		t.Fatalf("expected response_format=json, got %#v", got)
	}
	if got := capturedBody["top_p"]; got != 0.8 {
		t.Fatalf("expected request extra top_p to override default extra, got %#v", got)
	}
	if got := capturedBody["top_k"]; got != float64(7) {
		t.Fatalf("expected request extra top_k to be present, got %#v", got)
	}
	if got := capturedBody["model"]; got == "should-not-win" {
		t.Fatalf("expected framework-managed model field to win, got %#v", got)
	}

	if got := capturedHeaders.Get("anthropic-version"); got != APIVersion {
		t.Fatalf("expected protected anthropic-version header to win, got %q", got)
	}
	if got := capturedHeaders.Get("x-api-key"); got != "anthropic-key" {
		t.Fatalf("expected protected x-api-key header to be preserved, got %q", got)
	}
	if got := capturedHeaders.Get("x-default"); got != "request" {
		t.Fatalf("expected request header to override default header, got %q", got)
	}
	if got := capturedHeaders.Get("x-request"); got != "present" {
		t.Fatalf("expected request header to be applied, got %q", got)
	}
}

func TestFactory_Create_CopiesHeadersAndExtra(t *testing.T) {
	factory := &Factory{}
	config := &ai.AIConfig{
		Headers: map[string]string{"x-test": "1"},
		Extra: map[string]interface{}{
			"top_p": 0.9,
			"metadata": map[string]interface{}{
				"source": "original",
			},
		},
	}

	clientAny := factory.Create(config)
	client, ok := clientAny.(*Client)
	if !ok {
		t.Fatalf("expected anthropic client, got %T", clientAny)
	}

	config.Headers["x-test"] = "mutated"
	config.Extra["top_p"] = 0.1
	config.Extra["metadata"].(map[string]interface{})["source"] = "mutated"

	if got := client.defaultHeaders["x-test"]; got != "1" {
		t.Fatalf("expected factory to copy default headers, got %q", got)
	}
	if got := client.defaultExtra["top_p"]; got != 0.9 {
		t.Fatalf("expected factory to copy default extra, got %#v", got)
	}
	if got := client.defaultExtra["metadata"].(map[string]interface{})["source"]; got != "original" {
		t.Fatalf("expected factory to deep-copy default extra, got %#v", got)
	}
}

func TestClient_StreamResponse_SetsProtectedStreamingHeaders(t *testing.T) {
	var capturedBody map[string]interface{}
	var capturedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		capturedHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1}}}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient("anthropic-key", server.URL, &core.NoOpLogger{})
	client.defaultHeaders = map[string]string{
		"x-default":         "default",
		"anthropic-version": "should-not-win",
	}

	var chunks []string
	_, err := client.StreamResponse(context.Background(), "hello", &core.AIOptions{
		Model: "default",
		Headers: map[string]string{
			"x-default":         "request",
			"anthropic-version": "wrong",
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
	if got := capturedBody["stream"]; got != true {
		t.Fatalf("expected stream flag in request body, got %#v", got)
	}
	if got := capturedHeaders.Get("anthropic-version"); got != APIVersion {
		t.Fatalf("expected protected anthropic-version header to win, got %q", got)
	}
	if got := capturedHeaders.Get("x-api-key"); got != "anthropic-key" {
		t.Fatalf("expected protected x-api-key header to be preserved, got %q", got)
	}
	if got := capturedHeaders.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("expected Accept header to be text/event-stream, got %q", got)
	}
	if got := capturedHeaders.Get("x-default"); got != "request" {
		t.Fatalf("expected request header to override the streaming default header, got %q", got)
	}
}
