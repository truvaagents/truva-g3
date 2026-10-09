package openai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/internal/openaimodel"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestGPT6FamilyRequestCompatibility(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6-sol-2026-09-01", "gpt-6-astra-2026-09-01", "gpt-6.1-sol", "gpt-6.1-sol-2026-09-29"} {
		t.Run(model, func(t *testing.T) {
			for _, tc := range []struct {
				name, effort       string
				tokens, wireTokens int
				wantTemperature    bool
			}{
				{"planning-default-effort", "", 30000, 30000, false},
				{"synthesis-default-effort", "", 10000, 10000, false},
				{"explicit-none", "none", 100, 100, true},
				{"explicit-medium", "medium", 100, 300, false},
				{"explicit-low", "low", 100, 300, false},
				{"explicit-high", "high", 100, 300, false},
				{"explicit-xhigh", "xhigh", 100, 300, false},
				{"explicit-max", "max", 100, 300, false},
			} {
				// Exact reviewed IDs reason by default; snapshot defaults remain unknown.
				if tc.effort == "" && (model == "gpt-6-astra" || model == "gpt-6-sol" || model == "gpt-6-luna" || model == "gpt-6.1-sol") {
					tc.wireTokens *= 3
				}
				t.Run(tc.name, func(t *testing.T) {
					for _, stream := range []bool{false, true} {
						client := NewClient("synthetic-key", "", "openai", &core.NoOpLogger{})
						request := core.NewAIRequestFromLegacy("Return JSON", "model-compatibility", &core.AIOptions{Model: model, MaxTokens: tc.tokens, Temperature: 0.1})
						request.Generation.ResponseFormat = core.SetAIParameter("json")
						if tc.effort != "" {
							request.Generation.ReasoningEffort = core.SetAIParameter(tc.effort)
						}
						invocation, err := client.prepareInvocation(t.Context(), request, stream)
						if tc.effort == "none" && !openaimodel.SupportsGPT6NoReasoning(model) {
							if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
								t.Fatalf("Astra none effort: %v", err)
							}
							continue
						}
						if err != nil {
							t.Fatal(err)
						}
						var body map[string]interface{}
						if err := json.Unmarshal(invocation.Request.Body, &body); err != nil {
							t.Fatal(err)
						}
						if body["model"] != model || body["max_completion_tokens"] != float64(tc.wireTokens) {
							t.Fatalf("incorrect model/token fields: %#v", body)
						}
						if _, ok := body["max_tokens"]; ok {
							t.Fatal("legacy token field present")
						}
						if _, ok := body["temperature"]; ok != tc.wantTemperature {
							t.Fatal("incorrect sampling policy")
						}
						if tc.effort != "" && body["reasoning_effort"] != tc.effort {
							t.Fatal("explicit effort lost")
						}
						if tc.effort == "" {
							if _, ok := body["reasoning_effort"]; ok {
								t.Fatal("effort was invented")
							}
						}
						format, ok := body["response_format"].(map[string]interface{})
						if !ok || format["type"] != "json_object" {
							t.Fatal("JSON format lost")
						}
						if invocation.Request.Report.EffectiveMaxTokens.Value != tc.wireTokens {
							t.Fatal("report differs from wire budget")
						}
					}
				})
			}
		})
	}
}

func TestGPT6FamilyRecognitionAndProviderScope(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", " GPT-6-LUNA ", "gpt-6-sol-2026-09-01", "gpt-6-future-variant", "gpt-6.1-sol", "gpt-6.1-sol-2026-09-29"} {
		caps := providers.LookupModelCapabilities("openai", model)
		if !IsReasoningModel(model) || caps.ExactModel || caps.ReasoningStyle != "openai" || !caps.SupportsJSONMode || !caps.SupportsJSONSchema {
			t.Fatalf("missing family capabilities for %q: %+v", model, caps)
		}
	}
	for _, model := range []string{"gpt-60", "gpt-6ish", "vendor/gpt-6-sol", "custom-gpt-6", "gpt-7-sol", "gpt-6.1-solstice", "gpt-6.10-sol", "gpt-6.1-unknown"} {
		if IsReasoningModel(model) || providers.LookupModelCapabilities("openai", model).ReasoningStyle != "" {
			t.Fatalf("family recognition leaked to %q", model)
		}
	}
	for _, alias := range []string{"openai.openrouter", "openai.groq", "openai.custom"} {
		if caps := providers.LookupModelCapabilities(alias, "gpt-6-sol"); caps.ReasoningStyle != "" || caps.SupportsJSONMode {
			t.Fatalf("native contract leaked to %s: %+v", alias, caps)
		}
	}
}

func TestGPT6InvalidNativeParametersAndPolicyRecovery(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol"} {
		for _, tc := range []struct {
			name  string
			value interface{}
		}{
			{"reasoning_effort", "minimal"},
			{"reasoning_effort", nil},
			{"reasoning_effort", ""},
			{"Reasoning_Effort", "none"},
			{"top_p", 0.8},
			{"top_logprobs", 2},
			{"logprobs", true},
			{"tools", []interface{}{map[string]interface{}{"type": "function"}}},
			{"tool_choice", "auto"},
			{"functions", []interface{}{}},
			{"function_call", "auto"},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				client := NewClient("synthetic-key", "", "openai", &core.NoOpLogger{})
				request := core.NewAIRequestFromLegacy("hello", "guard", &core.AIOptions{
					Model: model, MaxTokens: 100, Extra: map[string]interface{}{tc.name: tc.value},
				})
				for _, stream := range []bool{false, true} {
					invocation, err := client.prepareInvocation(t.Context(), request, stream)
					if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
						t.Fatalf("invalid field accepted: %v", err)
					}
					if invocation.Request.Report.Stable || invocation.Request.Report.Fingerprint != "" {
						t.Fatal("invalid request obtained a stable fingerprint")
					}
				}
				request.Patches = []core.AIProviderPatch{{
					Name: "remove-incompatible-extra", Version: "1",
					Selector: core.AIProviderSelector{Provider: "openai"}, Remove: []string{"/" + tc.name},
				}}
				if _, err := client.prepareInvocation(t.Context(), request, false); err != nil {
					t.Fatalf("developer removal could not repair inherited field: %v", err)
				}
			})
		}
	}
}

func TestGPT6PolicyCannotReintroduceInvalidCombination(t *testing.T) {
	client := NewClient("synthetic-key", "", "openai", &core.NoOpLogger{})
	request := core.NewAIRequestFromLegacy("hello", "guard", &core.AIOptions{Model: "gpt-6-astra", MaxTokens: 100})
	for _, field := range []string{"reasoning_effort", "top_p", "logprobs", "tools", "Tools"} {
		request.Patches = []core.AIProviderPatch{{
			Name: "invalid-override", Version: "1", Selector: core.AIProviderSelector{Provider: "openai"},
			Set: map[string]interface{}{"/" + field: "none"},
		}}
		if _, err := client.prepareInvocation(t.Context(), request, false); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
			t.Fatalf("post-policy field %s escaped validation: %v", field, err)
		}
	}
	request.Patches = nil
	client.ReasoningEffort = "high"
	request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
	invocation, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(invocation.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["reasoning_effort"]; exists || body["max_completion_tokens"] != float64(100) {
		t.Fatalf("explicit omit did not clear default effort/budget: %v", body)
	}
}

func TestGPT6NoneAllowsSamplingAndNativeTools(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		client := NewClient("synthetic-key", "", "openai", &core.NoOpLogger{})
		request := core.NewAIRequestFromLegacy("hello", "native", &core.AIOptions{
			Model: model, MaxTokens: 100, ReasoningEffort: "none",
			Extra: map[string]interface{}{"top_p": 0.8, "logprobs": true, "top_logprobs": 2,
				"tools": []interface{}{map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "lookup", "parameters": map[string]interface{}{"type": "object"}}}}},
		})
		for _, stream := range []bool{false, true} {
			invocation, err := client.prepareInvocation(t.Context(), request, stream)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]interface{}
			if err := json.Unmarshal(invocation.Request.Body, &body); err != nil {
				t.Fatal(err)
			}
			if body["reasoning_effort"] != "none" || body["top_p"] != 0.8 || body["tools"] == nil {
				t.Fatalf("native parameters lost: %v", body)
			}
		}
	}
}

func TestGPT6AppPolicyAndPerRequestOverride(t *testing.T) {
	client := NewClient("synthetic-key", "", "openai", &core.NoOpLogger{})
	var err error
	client.requestPolicy, err = newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
		Name: "application-budget", Version: "1", Selector: core.AIProviderSelector{Provider: "openai", Model: "gpt-6-*"},
		Set: map[string]interface{}{"/max_completion_tokens": 777},
	}}, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		t.Fatal(err)
	}
	request := core.NewAIRequestFromLegacy("hello", "override", &core.AIOptions{Model: "gpt-6-luna", MaxTokens: 100})
	request.Patches = []core.AIProviderPatch{{
		Name: "call-budget", Version: "1", Selector: core.AIProviderSelector{Provider: "openai"},
		Set: map[string]interface{}{"/max_completion_tokens": 888},
	}}
	invocation, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Request.Report.EffectiveMaxTokens.Value != 888 || !invocation.Request.Report.Stable {
		t.Fatalf("per-request override/report lost: %+v", invocation.Request.Report)
	}
}

type gpt6Transport func(*http.Request) (*http.Response, error)

func (f gpt6Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestGPT6PublicClientOverridesAndStreaming(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol"} {
		t.Run(model, func(t *testing.T) {
			t.Setenv("TRUVAG3_OPENAI_MODEL_DEFAULT", model)
			var captured map[string]interface{}
			calls := 0
			transport := gpt6Transport(func(request *http.Request) (*http.Response, error) {
				calls++
				captured = nil
				if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
					return nil, err
				}
				body := `{"model":"` + model + `","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
				contentType := "application/json"
				if captured["stream"] == true {
					contentType = "text/event-stream"
					body = "data: {\"model\":\"" + model + "\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			client, err := ai.NewRequestClient(
				ai.WithProvider("openai"), ai.WithAPIKey("synthetic-key"),
				ai.WithModel("default"), ai.WithMaxTokens(100),
				ai.WithReasoningEffort("high"), ai.WithReasoningTokenMultiplier(2),
				ai.WithExtra("response_format", map[string]interface{}{"type": "json_schema", "json_schema": map[string]interface{}{"name": "answer", "schema": map[string]interface{}{"type": "object"}}}),
				ai.WithExtra("seed", 7), ai.WithHTTPClient(&http.Client{Transport: transport}),
			)
			if err != nil {
				t.Fatal(err)
			}
			request := core.NewAIRequest("hello", "public-options")
			result, err := client.Generate(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			format, ok := captured["response_format"].(map[string]interface{})
			if !ok || format["type"] != "json_schema" || captured["seed"] != float64(7) || captured["model"] != model || captured["max_completion_tokens"] != float64(200) || captured["reasoning_effort"] != "high" || result.Response.Content != "ok" {
				t.Fatalf("client-level options lost: %v", captured)
			}
			request.Generation.Model = "gpt-6-luna"
			request.Generation.ReasoningEffort = core.SetAIParameter("none")
			request.Generation.MaxTokens = core.SetAIParameter(123)
			request.Generation.ResponseFormat = core.OmitAIParameter[string]()
			var text strings.Builder
			result, err = core.StreamAI(t.Context(), client, request, func(chunk core.StreamChunk) error { text.WriteString(chunk.Content); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if captured["model"] != "gpt-6-luna" || captured["reasoning_effort"] != "none" || captured["max_completion_tokens"] != float64(123) || text.String() != "ok" || result.RequestReport.EffectiveMaxTokens.Value != 123 {
				t.Fatalf("per-call/streaming overrides lost: %v", captured)
			}
			if _, present := captured["response_format"]; present {
				t.Fatal("explicit format omit lost")
			}
			request.Generation.Model = "gpt-6-astra"
			if _, err := client.Generate(t.Context(), request); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
				t.Fatalf("invalid public request: %v", err)
			}
			if calls != 2 {
				t.Fatalf("invalid request reached transport: %d calls", calls)
			}
		})
	}
}

func TestGPT61SolPublicStreaming(t *testing.T) {
	for _, tc := range []struct {
		alias, model string
		wireTokens   int
	}{
		{alias: "openai", model: "gpt-6.1-sol", wireTokens: 246},
		{alias: "openai", model: "default", wireTokens: 246},
		{alias: "openai", model: "", wireTokens: 246},
		{alias: openRouterProviderAlias, model: "openai/gpt-6.1-sol", wireTokens: 246},
	} {
		t.Run(tc.alias+"/"+tc.model, func(t *testing.T) {
			wantModel := tc.model
			if wantModel == "" || wantModel == "default" {
				wantModel = "gpt-6.1-sol"
			}
			var captured map[string]interface{}
			calls := 0
			client, err := ai.NewRequestClient(
				ai.WithProviderAlias(tc.alias), ai.WithAPIKey("synthetic"), ai.WithModel(tc.model),
				ai.WithReasoningEffort("high"), ai.WithReasoningTokenMultiplier(2),
				ai.WithHTTPClient(&http.Client{Transport: gpt6Transport(func(r *http.Request) (*http.Response, error) {
					calls++
					if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
						return nil, err
					}
					body := "data: {\"model\":\"" + wantModel + "\",\"choices\":[{\"delta\":{\"content\":\"{\\\"ok\\\":\"}}]}\n\n" +
						"data: {\"choices\":[{\"delta\":{\"content\":\"true}\"},\"finish_reason\":\"stop\"}]}\n\n" +
						"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7,\"total_tokens\":12}}\n\n" +
						"data: [DONE]\n\n"
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})}),
			)
			if err != nil {
				t.Fatal(err)
			}
			request := core.NewAIRequest("Return a JSON answer", "stream-contract")
			request.Generation.ReasoningEffort = core.SetAIParameter("low")
			request.Generation.MaxTokens = core.SetAIParameter(123)
			request.Generation.ResponseFormat = core.SetAIParameter("json")
			request.Generation.Temperature = core.OmitAIParameter[float32]()
			before, err := core.CloneAIRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			var finish string
			result, err := core.StreamAI(t.Context(), client, request, func(chunk core.StreamChunk) error {
				text.WriteString(chunk.Content)
				if chunk.FinishReason != "" {
					finish = chunk.FinishReason
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || captured["model"] != wantModel || captured["stream"] != true || captured["max_completion_tokens"] != float64(tc.wireTokens) {
				t.Fatalf("stream wire contract changed: calls=%d body=%#v", calls, captured)
			}
			if tc.alias == "openai" {
				if captured["reasoning_effort"] != "low" || captured["reasoning"] != nil {
					t.Fatal("native reasoning spelling changed")
				}
			} else {
				if captured["reasoning"].(map[string]interface{})["effort"] != "low" || captured["reasoning_effort"] != nil {
					t.Fatal("routed reasoning spelling changed")
				}
				provider := requireOpenRouterProviderObject(t, captured)
				if provider["zdr"] != true || provider["data_collection"] != "deny" || provider["require_parameters"] != true {
					t.Fatal("routed privacy/required parameters changed")
				}
			}
			if _, present := captured["temperature"]; present {
				t.Fatal("sampling omission lost")
			}
			if captured["response_format"].(map[string]interface{})["type"] != "json_object" || captured["stream_options"].(map[string]interface{})["include_usage"] != true {
				t.Fatal("JSON/stream usage contract lost")
			}
			if text.String() != `{"ok":true}` || finish != "stop" || result.Response == nil || result.Response.Content != text.String() || result.Response.FinishReason != "stop" || result.Response.ContentSource != core.AIContentSourceFinal || result.Response.Usage.TotalTokens != 12 {
				t.Fatalf("stream response facts lost: text=%q finish=%q response=%#v", text.String(), finish, result.Response)
			}
			report := result.RequestReport
			if report == nil || report.ResolvedModel != wantModel || report.ProviderAlias != tc.alias || report.Operation != "stream" || !report.Stable || report.Fingerprint == "" || report.EffectiveMaxTokens != core.SetAIParameter(tc.wireTokens) || report.EffectiveTemperature.Mode != core.AIParameterOmit {
				t.Fatalf("effective report mismatch: %#v", report)
			}
			if !reflect.DeepEqual(request, before) {
				t.Fatal("streaming mutated caller input")
			}
		})
	}
}
