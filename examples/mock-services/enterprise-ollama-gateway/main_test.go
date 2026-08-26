package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEnterpriseRoundTrip(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected Ollama request: %s %s", r.Method, r.URL.Path)
		}
		var request ollamaChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode Ollama request: %v", err)
		}
		if request.Model != "llama3.2" {
			t.Fatalf("Ollama model = %q, want llama3.2", request.Model)
		}
		if request.Stream {
			t.Fatal("Ollama request unexpectedly enabled streaming")
		}
		if len(request.Messages) != 1 || request.Messages[0].Content != "What is MCP?" {
			t.Fatalf("unexpected messages: %#v", request.Messages)
		}
		if request.Options["num_predict"] != float64(50) {
			t.Fatalf("num_predict = %#v, want 50", request.Options["num_predict"])
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model":             "llama3.2",
			"message":           map[string]string{"role": "assistant", "content": "MCP connects models to tools."},
			"done":              true,
			"done_reason":       "stop",
			"prompt_eval_count": 12,
			"eval_count":        7,
		})
	}))
	defer ollama.Close()

	g := newGateway(testConfig(ollama.URL), ollama.Client())
	server := httptest.NewServer(g.routes())
	defer server.Close()

	token := requestToken(t, server.URL, "test-client", "test-secret")
	body := `{"messages":[{"role":"user","content":"What is MCP?"}],"user":"{\"appkey\":\"test-app\"}","stop":["<|im_end|>"],"stream":false,"max_tokens":50}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/openai/deployments/gpt-4o-mini/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", token)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("chat status = %d: %s", resp.StatusCode, payload)
	}

	var output struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			ContentFilterResults map[string]any `json:"content_filter_results"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int            `json:"prompt_tokens"`
			CompletionTokens int            `json:"completion_tokens"`
			TotalTokens      int            `json:"total_tokens"`
			Latency          map[string]any `json:"latency_checkpoint"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&output); err != nil {
		t.Fatal(err)
	}
	if output.Model != "gpt-4o-mini" {
		t.Fatalf("model = %q", output.Model)
	}
	if len(output.Choices) != 1 || output.Choices[0].Message.Content != "MCP connects models to tools." {
		t.Fatalf("unexpected choices: %#v", output.Choices)
	}
	if len(output.Choices[0].ContentFilterResults) != 4 {
		t.Fatalf("content filter results = %#v", output.Choices[0].ContentFilterResults)
	}
	if output.Usage.PromptTokens != 12 || output.Usage.CompletionTokens != 7 || output.Usage.TotalTokens != 19 {
		t.Fatalf("unexpected usage: %#v", output.Usage)
	}
	if output.Usage.Latency == nil {
		t.Fatal("latency_checkpoint missing")
	}
}

func TestChatContractValidation(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("invalid request should not reach Ollama")
	}))
	defer ollama.Close()

	g := newGateway(testConfig(ollama.URL), ollama.Client())
	server := httptest.NewServer(g.routes())
	defer server.Close()
	token := requestToken(t, server.URL, "test-client", "test-secret")

	tests := []struct {
		name          string
		token         string
		authorization string
		body          string
		wantStatus    int
	}{
		{
			name:       "missing api-key",
			body:       validChatBody(),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:          "stray bearer header",
			token:         token,
			authorization: "Bearer placeholder",
			body:          validChatBody(),
			wantStatus:    http.StatusBadRequest,
		},
		{
			name:       "wrong app key",
			token:      token,
			body:       `{"messages":[{"role":"user","content":"hello"}],"user":"{\"appkey\":\"wrong\"}","stop":["<|im_end|>"],"stream":false}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "streaming not captured",
			token:      token,
			body:       `{"messages":[{"role":"user","content":"hello"}],"user":"{\"appkey\":\"test-app\"}","stop":["<|im_end|>"],"stream":true}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "required stop missing",
			token:      token,
			body:       `{"messages":[{"role":"user","content":"hello"}],"user":"{\"appkey\":\"test-app\"}","stream":false}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, server.URL+"/openai/deployments/gpt-4o-mini/chat/completions", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if tt.token != "" {
				req.Header.Set("api-key", tt.token)
			}
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				payload, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tt.wantStatus, payload)
			}
		})
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	cfg := testConfig("http://ollama.invalid")
	g := newGateway(cfg, http.DefaultClient)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	server := httptest.NewServer(g.routes())
	defer server.Close()

	token := requestToken(t, server.URL, "test-client", "test-secret")
	now = now.Add(cfg.tokenTTL)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/openai/deployments/gpt-4o-mini/chat/completions", strings.NewReader(validChatBody()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("api-key", token)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestOllamaFailureBecomesBadGateway(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer ollama.Close()

	g := newGateway(testConfig(ollama.URL), ollama.Client())
	server := httptest.NewServer(g.routes())
	defer server.Close()
	token := requestToken(t, server.URL, "test-client", "test-secret")

	req, err := http.NewRequest(http.MethodPost, server.URL+"/openai/deployments/gpt-4o-mini/chat/completions", strings.NewReader(validChatBody()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("api-key", token)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func testConfig(ollamaURL string) config {
	return config{
		listenAddr:        "127.0.0.1:0",
		ollamaBaseURL:     ollamaURL,
		ollamaModel:       "llama3.2",
		clientID:          "test-client",
		clientSecret:      "test-secret",
		appKey:            "test-app",
		deployment:        "gpt-4o-mini",
		requiredStop:      "<|im_end|>",
		tokenTTL:          time.Minute,
		requestTimeout:    time.Second,
		strictAuthHeaders: true,
	}
}

func requestToken(t *testing.T, baseURL, clientID, clientSecret string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/oauth2/token", bytes.NewBufferString(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("token status = %d: %s", resp.StatusCode, payload)
	}
	var output struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&output); err != nil {
		t.Fatal(err)
	}
	if output.AccessToken == "" {
		t.Fatal("empty access token")
	}
	return output.AccessToken
}

func validChatBody() string {
	return `{"messages":[{"role":"user","content":"hello"}],"user":"{\"appkey\":\"test-app\"}","stop":["<|im_end|>"],"stream":false}`
}
