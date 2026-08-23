//go:build integration

package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	togetherPreferredBaseURL = "https://api.together.ai/v1"
	togetherLegacyBaseURL    = "https://api.together.xyz/v1"
	togetherLiveBodyMax      = 16 << 20
)

type togetherLiveCompletion struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []any  `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

func TestTogetherLiveContract(t *testing.T) {
	if os.Getenv("TRUVAG3_TOGETHER_LIVE_TEST") != "1" {
		t.Skip("set TRUVAG3_TOGETHER_LIVE_TEST=1 to run the Together contract probe")
	}
	apiKey := strings.TrimSpace(os.Getenv("TOGETHER_API_KEY"))
	if apiKey == "" {
		t.Skip("TOGETHER_API_KEY is not configured")
	}

	client := &http.Client{Timeout: 2 * time.Minute}
	snapshot := loadTogetherContractSnapshot(t)
	catalog := togetherFetchCatalog(t, client, apiKey, togetherPreferredBaseURL)
	_ = togetherFetchCatalog(t, client, apiKey, togetherLegacyBaseURL)

	for alias, model := range snapshot.Aliases {
		if _, found := catalog[model]; !found {
			t.Errorf("Together %s model %q is absent from the authenticated catalog", alias, model)
		}
	}

	seen := make(map[string]struct{})
	for alias, model := range snapshot.Aliases {
		if _, duplicate := seen[model]; duplicate {
			continue
		}
		seen[model] = struct{}{}
		t.Run("buffered_"+alias, func(t *testing.T) {
			completion := togetherChat(t, client, apiKey, map[string]any{
				"model":       model,
				"messages":    []map[string]string{{"role": "user", "content": "Reply with OK."}},
				"max_tokens":  8,
				"temperature": 0,
			})
			if completion.Model == "" || len(completion.Choices) == 0 || completion.Usage.TotalTokens <= 0 {
				t.Fatal("Together buffered response omitted model, choices, or usage")
			}
		})
	}

	defaultModel := snapshot.Aliases["default"]
	t.Run("json_mode", func(t *testing.T) {
		completion := togetherChat(t, client, apiKey, map[string]any{
			"model":           defaultModel,
			"messages":        []map[string]string{{"role": "user", "content": "Return a JSON object with ok=true."}},
			"max_tokens":      32,
			"temperature":     0,
			"response_format": map[string]string{"type": "json_object"},
		})
		if len(completion.Choices) == 0 {
			t.Fatal("Together JSON-mode response has no choices")
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &decoded); err != nil {
			t.Fatal("Together JSON-mode response content is not a JSON object")
		}
	})

	t.Run("tool_use", func(t *testing.T) {
		completion := togetherChat(t, client, apiKey, map[string]any{
			"model":       defaultModel,
			"messages":    []map[string]string{{"role": "user", "content": "Call the ping tool."}},
			"max_tokens":  64,
			"temperature": 0,
			"tools": []map[string]any{{
				"type": "function",
				"function": map[string]any{
					"name": "ping", "description": "Return pong",
					"parameters": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
				},
			}},
			"tool_choice": map[string]any{"type": "function", "function": map[string]string{"name": "ping"}},
		})
		if len(completion.Choices) == 0 || len(completion.Choices[0].Message.ToolCalls) == 0 {
			t.Fatal("Together forced-tool response has no tool call")
		}
	})

	t.Run("streaming", func(t *testing.T) {
		body := togetherRequest(t, client, apiKey, map[string]any{
			"model":          defaultModel,
			"messages":       []map[string]string{{"role": "user", "content": "Reply with OK."}},
			"max_tokens":     8,
			"temperature":    0,
			"stream":         true,
			"stream_options": map[string]bool{"include_usage": true},
		})
		defer func() { _ = body.Close() }()
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		dataEvents, sawDone, sawUsage := 0, false, false
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			dataEvents++
			if line == "data: [DONE]" {
				sawDone = true
			}
			if strings.Contains(line, `"usage":{"prompt_tokens"`) {
				sawUsage = true
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal("read Together stream")
		}
		if dataEvents == 0 || !sawDone || !sawUsage {
			t.Fatalf("Together stream contract missing events, done marker, or usage: events=%d done=%t usage=%t", dataEvents, sawDone, sawUsage)
		}
	})

	t.Run("safe_error", func(t *testing.T) {
		const canary = "TRUVAG3_TOGETHER_PROMPT_CANARY"
		encoded, err := json.Marshal(map[string]any{
			"model":      "truvag3/does-not-exist",
			"messages":   []map[string]string{{"role": "user", "content": canary}},
			"max_tokens": 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, togetherPreferredBaseURL+"/chat/completions", bytes.NewReader(encoded))
		if err != nil {
			t.Fatal("create Together error probe")
		}
		request.Header.Set("Authorization", "Bearer "+apiKey)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request) // #nosec G704 -- fixed Together contract-test endpoint.
		if err != nil {
			t.Fatal("send Together error probe")
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(response.Body, togetherLiveBodyMax+1))
		if err != nil || len(body) > togetherLiveBodyMax {
			t.Fatal("read Together error probe")
		}
		if response.StatusCode < 400 || response.StatusCode >= 500 {
			t.Fatalf("Together invalid-model status = %d", response.StatusCode)
		}
		if bytes.Contains(body, []byte(canary)) {
			t.Fatal("Together error response reflected prompt content")
		}
	})
}

func togetherFetchCatalog(t *testing.T, client *http.Client, apiKey, baseURL string) map[string]struct{} {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		t.Fatal("create Together catalog request")
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := client.Do(request) // #nosec G704 -- allow-listed Together contract-test endpoints.
	if err != nil {
		t.Fatal("send Together catalog request")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Together catalog at %s returned HTTP %d", baseURL, response.StatusCode)
	}
	var models []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, togetherLiveBodyMax)).Decode(&models); err != nil {
		t.Fatal("decode Together catalog")
	}
	result := make(map[string]struct{}, len(models))
	for _, model := range models {
		result[model.ID] = struct{}{}
	}
	return result
}

func togetherChat(t *testing.T, client *http.Client, apiKey string, payload map[string]any) togetherLiveCompletion {
	t.Helper()
	body := togetherRequest(t, client, apiKey, payload)
	defer func() { _ = body.Close() }()
	var completion togetherLiveCompletion
	if err := json.NewDecoder(io.LimitReader(body, togetherLiveBodyMax)).Decode(&completion); err != nil {
		t.Fatal("decode Together chat response")
	}
	return completion
}

func togetherRequest(t *testing.T, client *http.Client, apiKey string, payload map[string]any) io.ReadCloser {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal("encode Together probe request")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, togetherPreferredBaseURL+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal("create Together chat request")
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request) // #nosec G704 -- fixed Together contract-test endpoint.
	if err != nil {
		t.Fatal("send Together chat request")
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		t.Fatalf("Together chat probe returned HTTP %d", response.StatusCode)
	}
	return response.Body
}
