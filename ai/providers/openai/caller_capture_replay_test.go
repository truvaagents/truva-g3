package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestActualCallerCaptureOfflineChain(t *testing.T) {
	path := os.Getenv("TRUVAG3_TEST_CALLER_CAPTURE")
	if path == "" {
		t.Skip("optional synthetic orchestration request interchange not supplied")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var captures []struct {
		Request *core.AIRequest `json:"request"`
		Legacy  *core.AIOptions `json:"legacy"`
	}
	if err := json.Unmarshal(data, &captures); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 7 {
		t.Fatalf("capture count = %d", len(captures))
	}
	var bodies []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"synthetic"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	t.Setenv("OPENROUTER_API_KEY", "synthetic-not-a-real-key")
	t.Setenv("OPENROUTER_BASE_URL", server.URL)
	t.Setenv("TRUVAG3_OPENROUTER_MODEL_FAST", "openai/gpt-6-luna")
	t.Setenv("TRUVAG3_OPENROUTER_MODEL_DEFAULT", "deepseek/deepseek-v4.1-flash")
	for _, effort := range []string{"", "none"} {
		chain, err := ai.NewChainClient(ai.WithProviderChain(openRouterProviderAlias), ai.WithChainReasoningEffort(effort), ai.WithChainMaxRetries(0))
		if err != nil {
			t.Fatal(err)
		}
		for _, captured := range captures {
			request := core.NewAIRequestFromLegacy(captured.Request.Prompt, captured.Request.Purpose, captured.Legacy)
			request.Generation, request.Patches = captured.Request.Generation, captured.Request.Patches
			if _, err := chain.Generate(t.Context(), request); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(bodies) != 14 {
		t.Fatalf("offline attempts = %d", len(bodies))
	}
	for i, body := range bodies {
		provider := requireOpenRouterProviderObject(t, body)
		if provider["zdr"] != true || provider["data_collection"] != "deny" {
			t.Fatal("privacy lost")
		}
		if i%7 < 5 {
			if body["model"] != "openai/gpt-6-luna" {
				t.Fatal("fast alias not applied")
			}
			if _, exists := body["temperature"]; exists {
				t.Fatal("unsupported sampling reached transport")
			}
			if i >= 7 && (body["reasoning"].(map[string]interface{})["effort"] != "none" || provider["require_parameters"] != true) {
				t.Fatal("effort/routing not applied")
			}
		} else if i >= 7 && !reflect.DeepEqual(body, bodies[i-7]) {
			t.Fatal("DeepSeek wire changed under chain effort")
		}
		t.Logf("capture=%d model=%v effort=%v temperature=%v require_parameters=%v zdr=%v data_collection=%v", i, body["model"], body["reasoning"], body["temperature"], provider["require_parameters"], provider["zdr"], provider["data_collection"])
	}
}
