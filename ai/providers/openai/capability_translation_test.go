package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

func TestClient_GenerateResponse_StripsUnsupportedAdvancedFieldsForUnknownAlias(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"model":"test-model"}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, "openai.custom", &core.NoOpLogger{})
	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model:           "custom-model",
		ReasoningEffort: "high",
		ResponseFormat:  "json",
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("expected reasoning field to be stripped for unknown alias, got %#v", captured["reasoning"])
	}
	if _, ok := captured["response_format"]; ok {
		t.Fatalf("expected response_format field to be stripped for unknown alias, got %#v", captured["response_format"])
	}
}

func TestClient_GenerateResponse_StripsUnsupportedAdvancedFieldsFromExtraForUnknownAlias(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"model":"test-model"}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, "openai.custom", &core.NoOpLogger{})
	client.defaultExtra = map[string]interface{}{
		"reasoning": map[string]interface{}{"effort": "high"},
	}
	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model: "custom-model",
		Extra: map[string]interface{}{
			"response_format": map[string]interface{}{"type": "json"},
			"custom_flag":     true,
		},
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("expected reasoning from extra to be stripped for unknown alias, got %#v", captured["reasoning"])
	}
	if _, ok := captured["response_format"]; ok {
		t.Fatalf("expected response_format from extra to be stripped for unknown alias, got %#v", captured["response_format"])
	}
	if got := captured["custom_flag"]; got != true {
		t.Fatalf("expected unrelated extra fields to be preserved, got %#v", got)
	}
}

func TestClient_GenerateResponse_AllowsAdvancedFieldsForNativeOpenAIReasoningModel(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"model":"gpt-5.4"}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, "openai", &core.NoOpLogger{})
	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model:           "gpt-5.4",
		ReasoningEffort: "high",
		ResponseFormat:  "json",
		MaxTokens:       100,
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	reasoning, ok := captured["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning object for native OpenAI reasoning model, got %#v", captured["reasoning"])
	}
	if reasoning["effort"] != "high" {
		t.Fatalf("expected reasoning.effort=high, got %#v", reasoning["effort"])
	}
	responseFormat, ok := captured["response_format"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected response_format object, got %#v", captured["response_format"])
	}
	if responseFormat["type"] != "json" {
		t.Fatalf("expected response_format.type=json, got %#v", responseFormat["type"])
	}
}

func TestClient_GenerateResponse_GroqGPTOSSAllowsJSONButStripsReasoning(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"model":"gpt-oss-120b"}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, "openai.groq", &core.NoOpLogger{})
	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model:           "gpt-oss-120b",
		ReasoningEffort: "high",
		ResponseFormat:  "json",
		MaxTokens:       100,
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("expected reasoning field to be stripped for groq gpt-oss model until provider-specific translation exists, got %#v", captured["reasoning"])
	}
	if _, ok := captured["response_format"]; !ok {
		t.Fatalf("expected response_format field for groq gpt-oss model, got %#v", captured)
	}
}

// Same contract as the slashless test above, but using Groq's canonical
// OpenAI-namespaced model ID ("openai/gpt-oss-120b"). Guards the slashed
// capability row in capabilities.go against accidental removal — without
// that row, response_format would be silently stripped because the matcher
// would fall back to the empty-prefix openai.groq row (SupportsJSONMode:
// false).
func TestClient_GenerateResponse_GroqGPTOSSSlashedIDAllowsJSON(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"model":"openai/gpt-oss-120b"}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, "openai.groq", &core.NoOpLogger{})
	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model:           "openai/gpt-oss-120b",
		ReasoningEffort: "high",
		ResponseFormat:  "json",
		MaxTokens:       100,
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("expected reasoning field to be stripped for groq gpt-oss slashed-ID model, got %#v", captured["reasoning"])
	}
	if _, ok := captured["response_format"]; !ok {
		t.Fatalf("expected response_format field for groq gpt-oss slashed-ID model, got %#v", captured)
	}
}

func TestClient_GenerateResponse_OllamaAllowsReasoningEffortForNonOpenAIModel(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"model":"gemma4:31b"}`))
	}))
	defer server.Close()

	client := NewClient("", server.URL, "openai.ollama", &core.NoOpLogger{})
	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model:           "gemma4:31b",
		ReasoningEffort: "none",
		MaxTokens:       100,
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	reasoning, ok := captured["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning object for ollama model, got %#v", captured["reasoning"])
	}
	if reasoning["effort"] != "none" {
		t.Fatalf("expected reasoning.effort=none, got %#v", reasoning["effort"])
	}
}

func TestClient_GenerateResponse_OllamaAllowsReasoningFromExtra(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"model":"gemma4:31b"}`))
	}))
	defer server.Close()

	client := NewClient("", server.URL, "openai.ollama", &core.NoOpLogger{})
	_, err := client.GenerateResponse(context.Background(), "hello", &core.AIOptions{
		Model: "gemma4:31b",
		Extra: map[string]interface{}{
			"reasoning": map[string]interface{}{"effort": "none"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}

	reasoning, ok := captured["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning object from extra for ollama model, got %#v", captured["reasoning"])
	}
	if reasoning["effort"] != "none" {
		t.Fatalf("expected reasoning.effort=none, got %#v", reasoning["effort"])
	}
}

func TestFilterOpenAIExtraFields_StripsUnsupportedFields(t *testing.T) {
	caps := providers.ModelCapabilities{ProviderAlias: "openai.custom"}
	filtered := filterOpenAIExtraFields(
		context.Background(),
		&core.NoOpLogger{},
		"openai.custom",
		"custom-model",
		caps,
		map[string]interface{}{
			"reasoning":       map[string]interface{}{"effort": "high"},
			"response_format": map[string]interface{}{"type": "json"},
			"custom_flag":     true,
		},
	)

	if _, ok := filtered["reasoning"]; ok {
		t.Fatal("expected unsupported reasoning field to be stripped")
	}
	if _, ok := filtered["response_format"]; ok {
		t.Fatal("expected unsupported response_format field to be stripped")
	}
	if filtered["custom_flag"] != true {
		t.Fatalf("expected unrelated extra field to be preserved, got %#v", filtered["custom_flag"])
	}
}

func TestFilterOpenAIExtraFields_AllowsSupportedFields(t *testing.T) {
	caps := providers.ModelCapabilities{ProviderAlias: "openai.ollama", ReasoningStyle: "openai", SupportsJSONMode: true}
	filtered := filterOpenAIExtraFields(
		context.Background(),
		&core.NoOpLogger{},
		"openai.ollama",
		"gemma4:31b",
		caps,
		map[string]interface{}{
			"reasoning":       map[string]interface{}{"effort": "none"},
			"response_format": map[string]interface{}{"type": "json"},
		},
	)

	if _, ok := filtered["reasoning"]; !ok {
		t.Fatal("expected reasoning field to be preserved for supported alias")
	}
	if _, ok := filtered["response_format"]; !ok {
		t.Fatal("expected response_format field to be preserved for supported alias")
	}
}

func TestClient_StreamResponse_StripsUnsupportedAdvancedFieldsForUnknownAlias(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, "openai.custom", &core.NoOpLogger{})
	callback := func(chunk core.StreamChunk) error { return nil }

	_, err := client.StreamResponse(context.Background(), "hello", &core.AIOptions{
		Model:           "custom-model",
		ReasoningEffort: "high",
		ResponseFormat:  "json",
	}, callback)
	if err != nil {
		t.Fatalf("StreamResponse returned error: %v", err)
	}

	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("expected reasoning field to be stripped for unknown alias streaming, got %#v", captured["reasoning"])
	}
	if _, ok := captured["response_format"]; ok {
		t.Fatalf("expected response_format field to be stripped for unknown alias streaming, got %#v", captured["response_format"])
	}
}

func TestClient_StreamResponse_OllamaAllowsReasoningEffortForNonOpenAIModel(t *testing.T) {
	var captured map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			_ = r.Body.Close()
		}()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient("", server.URL, "openai.ollama", &core.NoOpLogger{})
	callback := func(chunk core.StreamChunk) error { return nil }

	_, err := client.StreamResponse(context.Background(), "hello", &core.AIOptions{
		Model:           "gemma4:31b",
		ReasoningEffort: "none",
	}, callback)
	if err != nil {
		t.Fatalf("StreamResponse returned error: %v", err)
	}

	reasoning, ok := captured["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning object for ollama streaming model, got %#v", captured["reasoning"])
	}
	if reasoning["effort"] != "none" {
		t.Fatalf("expected reasoning.effort=none, got %#v", reasoning["effort"])
	}
}
