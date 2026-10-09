package anthropic

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestRefreshedAliasesGenerateAndStream(t *testing.T) {
	for alias, model := range map[string]string{
		"fast": "claude-haiku-5-5", "smart": "claude-opus-5-5",
		"code": "claude-opus-5-5", "vision": "claude-opus-5-5",
	} {
		for _, stream := range []bool{false, true} {
			name := alias + "/generate"
			if stream {
				name = alias + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("TRUVAG3_ANTHROPIC_MODEL_"+strings.ToUpper(alias), "")
				calls := 0
				transport := phase4RoundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					var body map[string]interface{}
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					assertNoSamplingKeys(t, body)
					if body["model"] != model || body["max_tokens"] != float64(2048) {
						t.Fatalf("model/budget changed: %#v", body)
					}
					if body["output_config"].(map[string]interface{})["effort"] != "low" {
						t.Fatal("native effort override was lost")
					}
					if _, exists := body["thinking"]; exists {
						t.Fatal("adapter imposed a thinking policy")
					}
					response := `{"model":"` + model + `","content":[{"type":"thinking","thinking":"internal","signature":"opaque"},{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":5}}`
					if stream {
						response = "data: {\"type\":\"message_start\",\"message\":{\"model\":\"" + model + "\",\"usage\":{\"input_tokens\":3}}}\n\n" +
							"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"internal\"}}\n\n" +
							"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
							"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
							"data: {\"type\":\"message_stop\"}\n\n"
					}
					return phase4Response(request, http.StatusOK, response), nil
				})
				client, err := ai.NewRequestClient(
					ai.WithProvider("anthropic"), ai.WithAPIKey("test-key"), ai.WithModel(alias),
					ai.WithHTTPClient(&http.Client{Transport: transport}),
				)
				if err != nil {
					t.Fatal(err)
				}
				request := core.NewAIRequestFromLegacy("hello", "refresh-test", &core.AIOptions{
					MaxTokens: 2048, Extra: map[string]interface{}{"output_config": map[string]interface{}{"effort": "low"}},
				})
				request.Generation.Temperature = core.SetAIParameter(float32(0.2))
				var result *core.AIResult
				var chunks strings.Builder
				if stream {
					streamClient, ok := client.(core.StreamingAIRequestClient)
					if !ok {
						t.Fatal("Anthropic client lost streaming support")
					}
					result, err = streamClient.Stream(t.Context(), request, func(chunk core.StreamChunk) error {
						chunks.WriteString(chunk.Content)
						return nil
					})
				} else {
					result, err = client.Generate(t.Context(), request)
				}
				if err != nil {
					t.Fatal(err)
				}
				if calls != 1 || result.Response.Content != "ok" || result.Response.Model != model || (stream && chunks.String() != "ok") {
					t.Fatalf("calls=%d result=%#v streamed=%q", calls, result, chunks.String())
				}
				report := result.RequestReport
				if report == nil || report.ResolvedModel != model || report.EffectiveTemperature.Mode != core.AIParameterOmit ||
					report.EffectiveMaxTokens.Value != 2048 || !report.Stable || len(report.Adjustments) != 1 ||
					report.Adjustments[0].Source != "built-in-rule" {
					t.Fatalf("request report = %#v", report)
				}
			})
		}
	}
}

func TestHaiku55StrictSamplingAndApplicationAcknowledgement(t *testing.T) {
	for _, acknowledge := range []bool{false, true} {
		client := NewClient("test-key", "", &core.NoOpLogger{})
		var rules []core.AIProviderPatch
		if acknowledge {
			rules = []core.AIProviderPatch{{
				Name: "application-sampling-omission", Version: "1",
				Selector: core.AIProviderSelector{Provider: "anthropic", Model: "claude-haiku-5-5"},
				Remove:   []string{"/temperature"},
			}}
		}
		var err error
		client.requestPolicy, err = newRequestPolicyEngineWithIntegration(rules, nil, requestpolicy.CompatibilityStrict)
		if err != nil {
			t.Fatal(err)
		}
		request := core.NewAIRequest("hello", "strict-test")
		request.Generation.Model = "claude-haiku-5-5"
		request.Generation.Temperature = core.SetAIParameter(float32(0.2))
		invocation, err := client.prepareInvocation(t.Context(), request, false)
		if !acknowledge {
			if err == nil {
				t.Fatal("strict mode accepted an unacknowledged explicit temperature change")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		assertNoSamplingKeys(t, decodePreparedBody(t, invocation.Request.Body))
	}
}

func TestFastAliasRollbackChangesFingerprintAndRetainsSampling(t *testing.T) {
	t.Setenv("TRUVAG3_ANTHROPIC_MODEL_FAST", "")
	client := NewClient("test-key", "", &core.NoOpLogger{})
	request := core.NewAIRequestFromLegacy("hello", "rollback", &core.AIOptions{Model: "fast", Temperature: 0.3})
	current, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRUVAG3_ANTHROPIC_MODEL_FAST", "claude-haiku-4-5")
	previous, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Request.Model != "claude-haiku-4-5" || !previous.Request.TemperatureSent || len(previous.Request.Adjustments) != 0 {
		t.Fatalf("rollback did not preserve previous model behavior: %#v", previous.Request)
	}
	if current.Request.Report.Fingerprint == previous.Request.Report.Fingerprint {
		t.Fatal("alias rollback reused the new model's fingerprint")
	}
}
