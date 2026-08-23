//go:build integration

package openai

import (
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
	openRouterModelsURL    = "https://openrouter.ai/api/v1/models"
	openRouterChatURL      = "https://openrouter.ai/api/v1/chat/completions"
	openRouterOpenAPIURL   = "https://openrouter.ai/openapi.json"
	openRouterLiveBodyMax  = 16 << 20
	openRouterGenerationID = maxOpenRouterGenerationIDBytes
	openRouterSampleFree   = "liquid/lfm-2.5-2.6b:free"
	openRouterLiveAttempts = 3
)

type openRouterCatalog struct {
	Data []struct {
		ID                  string   `json:"id"`
		SupportedParameters []string `json:"supported_parameters"`
	} `json:"data"`
}

type openRouterLiveCompletion struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string               `json:"finish_reason"`
		Error        *openRouterLiveError `json:"error,omitempty"`
	} `json:"choices"`
	Error *openRouterLiveError `json:"error,omitempty"`
}

type openRouterLiveError struct {
	Code int `json:"code"`
}

func TestOpenRouterLiveContract(t *testing.T) {
	if os.Getenv("TRUVAG3_OPENROUTER_LIVE_TEST") != "1" {
		t.Skip("set TRUVAG3_OPENROUTER_LIVE_TEST=1 to run the OpenRouter contract probe")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY is not configured")
	}

	client := &http.Client{Timeout: 2 * time.Minute}
	catalog := fetchOpenRouterCatalog(t, client, apiKey)

	t.Run("catalog", func(t *testing.T) {
		required := map[string][]string{
			"openrouter/auto":        {"reasoning", "reasoning_effort", "response_format", "structured_outputs"},
			"openrouter/pareto-code": {},
			"openrouter/free":        {"reasoning", "reasoning_effort", "response_format", "structured_outputs"},
			"openai/gpt-5.6-luna":    {"max_completion_tokens", "reasoning", "reasoning_effort", "response_format", "structured_outputs"},
			"openai/gpt-5.6-sol":     {"max_completion_tokens", "reasoning", "response_format", "structured_outputs"},
			openRouterSampleFree:     {"max_completion_tokens", "response_format", "structured_outputs"},
		}
		for model, parameters := range required {
			actual, present := catalog[model]
			if !present {
				t.Errorf("required model %q is absent from the authenticated catalog", model)
				continue
			}
			for _, parameter := range parameters {
				if !containsString(actual, parameter) {
					t.Errorf("model %q does not advertise required parameter %q", model, parameter)
				}
			}
		}
		if parameters := catalog["openrouter/pareto-code"]; len(parameters) != 0 {
			t.Errorf("openrouter/pareto-code advertised parameters changed; review the conservative capability contract")
		}
	})

	t.Run("openapi", func(t *testing.T) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, openRouterOpenAPIURL, nil)
		if err != nil {
			t.Fatal("create OpenRouter OpenAPI request")
		}
		response := doOpenRouterLiveRequest(t, client, request)
		defer func() { _ = response.Body.Close() }()
		var document map[string]interface{}
		decodeOpenRouterLiveJSON(t, response.Body, &document)
		paths, ok := document["paths"].(map[string]interface{})
		if !ok {
			t.Fatal("OpenRouter OpenAPI document has no paths object")
		}
		if _, ok := paths["/chat/completions"]; !ok {
			t.Fatal("OpenRouter OpenAPI document has no /chat/completions operation")
		}
		if !containsOpenRouterReasoningEffortEnum(document) {
			t.Fatal("OpenRouter OpenAPI document does not contain the required reasoning-effort enum")
		}
	})

	for _, model := range []string{
		"openrouter/auto",
		"openrouter/pareto-code",
		"openai/gpt-5.6-luna",
		"openai/gpt-5.6-sol",
	} {
		model := model
		t.Run("buffered_"+sanitizeOpenRouterTestName(model), func(t *testing.T) {
			probeOpenRouterChat(t, client, apiKey, model, nil)
		})
	}

	t.Run("fast_concrete_reasoning", func(t *testing.T) {
		probeOpenRouterChat(t, client, apiKey, "openai/gpt-5.6-luna", func(request map[string]interface{}) {
			request["reasoning"] = map[string]interface{}{"effort": "low"}
		})
	})

	for _, model := range []string{"openai/gpt-5.6-luna"} {
		model := model
		t.Run("json_object_"+sanitizeOpenRouterTestName(model), func(t *testing.T) {
			probeOpenRouterStructuredChat(t, client, apiKey, model, false)
		})
		t.Run("json_schema_"+sanitizeOpenRouterTestName(model), func(t *testing.T) {
			probeOpenRouterStructuredChat(t, client, apiKey, model, true)
		})
	}

	if os.Getenv("TRUVAG3_OPENROUTER_ROUTER_FEATURE_LIVE_TEST") == "1" {
		for _, model := range []string{"openrouter/auto", "openrouter/auto:nitro"} {
			model := model
			t.Run("experimental_reasoning_"+sanitizeOpenRouterTestName(model), func(t *testing.T) {
				probeOpenRouterChat(t, client, apiKey, model, func(request map[string]interface{}) {
					request["reasoning"] = map[string]interface{}{"effort": "low"}
				})
			})
			t.Run("experimental_json_object_"+sanitizeOpenRouterTestName(model), func(t *testing.T) {
				probeOpenRouterStructuredChat(t, client, apiKey, model, false)
			})
			t.Run("experimental_json_schema_"+sanitizeOpenRouterTestName(model), func(t *testing.T) {
				probeOpenRouterStructuredChat(t, client, apiKey, model, true)
			})
		}
	}

	if os.Getenv("TRUVAG3_OPENROUTER_FREE_LIVE_TEST") == "1" {
		for _, model := range []string{"openrouter/free", openRouterSampleFree} {
			model := model
			t.Run("experimental_free_"+sanitizeOpenRouterTestName(model), func(t *testing.T) {
				probeOpenRouterChat(t, client, apiKey, model, nil)
			})
		}
	}
}

func fetchOpenRouterCatalog(t *testing.T, client *http.Client, apiKey string) map[string][]string {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, openRouterModelsURL, nil)
	if err != nil {
		t.Fatal("create OpenRouter catalog request")
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response := doOpenRouterLiveRequest(t, client, request)
	defer func() { _ = response.Body.Close() }()

	var catalog openRouterCatalog
	decodeOpenRouterLiveJSON(t, response.Body, &catalog)
	models := make(map[string][]string, len(catalog.Data))
	for _, model := range catalog.Data {
		models[model.ID] = append([]string(nil), model.SupportedParameters...)
	}
	return models
}

func probeOpenRouterChat(
	t *testing.T,
	client *http.Client,
	apiKey string,
	model string,
	mutate func(map[string]interface{}),
) openRouterLiveCompletion {
	t.Helper()
	requestBody := openRouterBaseProbe(model, "Reply with one short word.")
	if mutate != nil {
		mutate(requestBody)
	}
	return executeOpenRouterChatProbe(t, client, apiKey, model, requestBody)
}

func probeOpenRouterStructuredChat(
	t *testing.T,
	client *http.Client,
	apiKey string,
	model string,
	jsonSchema bool,
) {
	t.Helper()
	requestBody := openRouterBaseProbe(model, "Return a JSON object whose only field is ok and whose value is true.")
	if jsonSchema {
		requestBody["response_format"] = map[string]interface{}{
			"type": "json_schema",
			"json_schema": map[string]interface{}{
				"name":   "openrouter_contract_probe",
				"strict": true,
				"schema": map[string]interface{}{
					"type":                 "object",
					"properties":           map[string]interface{}{"ok": map[string]interface{}{"type": "boolean"}},
					"required":             []string{"ok"},
					"additionalProperties": false,
				},
			},
		}
	} else {
		requestBody["response_format"] = map[string]interface{}{"type": "json_object"}
	}
	completion := executeOpenRouterChatProbe(t, client, apiKey, model, requestBody)
	if len(completion.Choices) == 0 {
		t.Fatal("OpenRouter structured-output probe returned no choices")
	}
	var output map[string]interface{}
	if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &output); err != nil {
		t.Fatal("OpenRouter structured-output probe did not return a JSON object")
	}
	value, ok := output["ok"].(bool)
	if !ok || !value || len(output) != 1 {
		t.Fatal("OpenRouter structured-output probe did not satisfy the requested shape")
	}
}

func openRouterBaseProbe(model, prompt string) map[string]interface{} {
	return map[string]interface{}{
		"model":                 model,
		"messages":              []map[string]string{{"role": "user", "content": prompt}},
		"max_completion_tokens": 16,
		"provider": map[string]interface{}{
			"require_parameters": true,
			"data_collection":    "deny",
			"zdr":                true,
		},
	}
}

func executeOpenRouterChatProbe(
	t *testing.T,
	client *http.Client,
	apiKey string,
	requestedModel string,
	requestBody map[string]interface{},
) openRouterLiveCompletion {
	t.Helper()
	body, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatal("encode OpenRouter probe request")
	}
	for attempt := 1; attempt <= openRouterLiveAttempts; attempt++ {
		request, requestErr := http.NewRequestWithContext(t.Context(), http.MethodPost, openRouterChatURL, bytes.NewReader(body))
		if requestErr != nil {
			t.Fatal("create OpenRouter chat request")
		}
		request.Header.Set("Authorization", "Bearer "+apiKey)
		request.Header.Set("Content-Type", "application/json")
		response := doOpenRouterLiveRequest(t, client, request)

		generationID := response.Header.Get("X-Generation-Id")
		if generationID == "" || len(generationID) > openRouterGenerationID || containsControl(generationID) ||
			sanitizeGenerationID(generationID) != generationID {
			_ = response.Body.Close()
			t.Fatalf("OpenRouter probe for %q returned an absent or invalid generation identifier", requestedModel)
		}

		var completion openRouterLiveCompletion
		decodeOpenRouterLiveJSON(t, response.Body, &completion)
		_ = response.Body.Close()
		if code := openRouterLiveCompletionErrorCode(completion); code != 0 {
			if attempt < openRouterLiveAttempts && openRouterLiveErrorIsTransient(code) {
				time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
				continue
			}
			t.Fatalf("OpenRouter probe for %q returned in-band error code %d", requestedModel, code)
		}
		if strings.TrimSpace(completion.Model) == "" {
			t.Fatalf("OpenRouter probe for %q returned no response model", requestedModel)
		}
		if strings.HasPrefix(requestedModel, "openrouter/") && strings.HasPrefix(completion.Model, "openrouter/") {
			t.Fatalf("OpenRouter router probe for %q did not report a concrete response model", requestedModel)
		}
		if len(completion.Choices) == 0 || strings.TrimSpace(completion.Choices[0].FinishReason) == "" {
			t.Fatalf("OpenRouter probe for %q returned no terminal choice", requestedModel)
		}
		return completion
	}
	t.Fatal("OpenRouter live probe exhausted attempts unexpectedly")
	return openRouterLiveCompletion{}
}

func openRouterLiveCompletionErrorCode(completion openRouterLiveCompletion) int {
	if completion.Error != nil {
		return completion.Error.Code
	}
	for _, choice := range completion.Choices {
		if choice.Error != nil {
			return choice.Error.Code
		}
	}
	return 0
}

func openRouterLiveErrorIsTransient(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

func doOpenRouterLiveRequest(t *testing.T, client *http.Client, request *http.Request) *http.Response {
	t.Helper()
	response, err := client.Do(request) // #nosec G704 -- fixed OpenRouter contract-test endpoints.
	if err != nil {
		t.Fatal("OpenRouter live request failed")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		t.Fatalf("OpenRouter live request returned HTTP %d", response.StatusCode)
	}
	return response
}

func decodeOpenRouterLiveJSON(t *testing.T, reader io.Reader, target interface{}) {
	t.Helper()
	if reader == nil {
		t.Fatal("OpenRouter live response body is nil")
	}
	body, err := io.ReadAll(io.LimitReader(reader, openRouterLiveBodyMax+1))
	if err != nil {
		t.Fatal("read OpenRouter live response")
	}
	if len(body) > openRouterLiveBodyMax {
		t.Fatal("OpenRouter live response exceeds the test limit")
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatal("decode OpenRouter live response")
	}
}

func containsOpenRouterReasoningEffortEnum(value interface{}) bool {
	required := []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"}
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			if key == "enum" {
				values, ok := child.([]interface{})
				if ok {
					present := make(map[string]bool, len(values))
					for _, item := range values {
						if text, ok := item.(string); ok {
							present[text] = true
						}
					}
					complete := true
					for _, effort := range required {
						complete = complete && present[effort]
					}
					if complete {
						return true
					}
				}
			}
			if containsOpenRouterReasoningEffortEnum(child) {
				return true
			}
		}
	case []interface{}:
		for _, child := range typed {
			if containsOpenRouterReasoningEffortEnum(child) {
				return true
			}
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func sanitizeOpenRouterTestName(value string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(value)
}
