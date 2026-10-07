package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestOpenRouterSolReasoningObjectRepresentations(t *testing.T) {
	type reasoningObject map[string]interface{}
	type reasoningKey string
	type reasoningEffort string
	type reasoningFlag bool
	type reasoningBudget int64
	for _, tc := range []struct {
		name    string
		value   interface{}
		invalid bool
	}{
		{name: "generic", value: map[string]interface{}{"effort": "low", "exclude": true}},
		{name: "typed", value: map[string]string{"effort": "low"}},
		{name: "named", value: reasoningObject{"effort": "low"}},
		{name: "named keys and values", value: map[reasoningKey]reasoningEffort{"effort": "low"}},
		{name: "empty object", value: map[string]string{}},
		{name: "enabled", value: map[string]interface{}{"enabled": true}},
		{name: "named booleans", value: map[reasoningKey]reasoningFlag{"enabled": true, "exclude": true}},
		{name: "visible reasoning", value: map[string]interface{}{"exclude": false}},
		{name: "budget only", value: map[string]interface{}{"max_tokens": 100}},
		{name: "named budget", value: map[string]interface{}{"max_tokens": reasoningBudget(100)}},
		{name: "unsigned budget", value: map[string]interface{}{"max_tokens": uint64(100)}},
		{name: "decoded budget", value: map[string]interface{}{"max_tokens": float64(100)}},
		{name: "json budget", value: map[string]interface{}{"max_tokens": json.Number("100")}},
		{name: "native siblings", value: map[string]interface{}{"enabled": true, "exclude": true, "max_tokens": 100, "summary": "auto"}},
		{name: "disabled", value: map[string]interface{}{"enabled": false}, invalid: true},
		{name: "named disabled", value: map[reasoningKey]reasoningFlag{"enabled": false}, invalid: true},
		{name: "string enabled", value: map[string]interface{}{"enabled": "false"}, invalid: true},
		{name: "null enabled", value: map[string]interface{}{"enabled": nil}, invalid: true},
		{name: "string exclude", value: map[string]interface{}{"exclude": "true"}, invalid: true},
		{name: "null exclude", value: map[string]interface{}{"exclude": nil}, invalid: true},
		{name: "conflicting controls", value: map[string]interface{}{"effort": "low", "max_tokens": 100}, invalid: true},
		{name: "wrong enabled case", value: map[string]interface{}{"Enabled": false}, invalid: true},
		{name: "wrong exclude case", value: map[string]interface{}{"Exclude": true}, invalid: true},
		{name: "wrong budget case", value: map[string]interface{}{"Max_Tokens": 100}, invalid: true},
		{name: "zero budget", value: map[string]interface{}{"max_tokens": 0}, invalid: true},
		{name: "negative budget", value: map[string]interface{}{"max_tokens": -1}, invalid: true},
		{name: "fractional budget", value: map[string]interface{}{"max_tokens": 1.5}, invalid: true},
		{name: "string budget", value: map[string]interface{}{"max_tokens": "100"}, invalid: true},
		{name: "null budget", value: map[string]interface{}{"max_tokens": nil}, invalid: true},
		{name: "boolean budget", value: map[string]interface{}{"max_tokens": true}, invalid: true},
		{name: "invalid json budget", value: map[string]interface{}{"max_tokens": json.Number("invalid")}, invalid: true},
		{name: "null", invalid: true},
		{name: "nil map", value: map[string]interface{}(nil), invalid: true},
		{name: "nil named map", value: reasoningObject(nil), invalid: true},
		{name: "scalar", value: "low", invalid: true},
		{name: "array", value: []string{"low"}, invalid: true},
		{name: "non-string keys", value: map[int]string{1: "low"}, invalid: true},
		{name: "noncanonical typed key", value: map[string]string{"Effort": "low"}, invalid: true},
		{name: "noncanonical named key", value: map[reasoningKey]reasoningEffort{"Effort": "low"}, invalid: true},
		{name: "unsupported effort", value: map[string]string{"effort": "none"}, invalid: true},
		{name: "unsupported named effort", value: map[reasoningKey]reasoningEffort{"effort": "none"}, invalid: true},
		{name: "non-string effort", value: map[string]interface{}{"effort": 1}, invalid: true},
		{name: "null effort", value: map[string]interface{}{"effort": nil}, invalid: true},
	} {
		for _, patch := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/patch=%t/stream=%t", tc.name, patch, stream), func(t *testing.T) {
					client := NewClient("synthetic", "", openRouterProviderAlias, &core.NoOpLogger{})
					request := core.NewAIRequestFromLegacy("synthetic", "contract", &core.AIOptions{
						Model: "openai/gpt-6.1-sol", MaxTokens: 100,
						Extra: map[string]interface{}{"reasoning": tc.value},
					})
					if patch {
						request = core.NewAIRequest("synthetic", "contract")
						request.Generation.Model = "openai/gpt-6.1-sol"
						request.Generation.MaxTokens = core.SetAIParameter(100)
						request.Patches = []core.AIProviderPatch{{
							Name: "native-reasoning", Version: "1",
							Selector: core.AIProviderSelector{ProviderAlias: openRouterProviderAlias},
							Set:      map[string]interface{}{"/reasoning": tc.value},
						}}
					}
					before, cloneErr := core.CloneAIRequest(request)
					invocation, err := client.prepareInvocation(t.Context(), request, stream)
					if cloneErr == nil && !reflect.DeepEqual(request, before) {
						t.Fatal("caller input mutated")
					}
					if tc.invalid {
						if err == nil {
							t.Fatal("invalid reasoning accepted")
						}
						if fingerprint, stable := client.RequestFingerprint(t.Context(), request); stable || fingerprint != "" {
							t.Fatal("invalid reasoning obtained stable identity")
						}
						return
					}
					if cloneErr != nil || err != nil {
						t.Fatalf("valid JSON object rejected: clone=%v prepare=%v", cloneErr, err)
					}
					var body map[string]json.RawMessage
					if err := json.Unmarshal(invocation.Request.Body, &body); err != nil {
						t.Fatal(err)
					}
					want, err := json.Marshal(tc.value)
					if err != nil || string(body["reasoning"]) != string(want) {
						t.Fatalf("native reasoning changed: got %s, want %s (error %v)", body["reasoning"], want, err)
					}
					if !invocation.Request.Report.Stable || invocation.Request.Report.Fingerprint == "" {
						t.Fatal("valid reasoning has no stable identity")
					}
				})
			}
		}
	}
}

func TestOpenRouterSolContract(t *testing.T) {
	for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		for _, stream := range []bool{false, true} {
			client := NewClient("synthetic", "", openRouterProviderAlias, &core.NoOpLogger{})
			request := core.NewAIRequestFromLegacy("Return JSON", "planning", &core.AIOptions{
				Model: "openai/gpt-6.1-sol", MaxTokens: 128000, Temperature: 0.2, ReasoningEffort: effort, ResponseFormat: "json",
				Extra: map[string]interface{}{"provider": map[string]interface{}{"only": []string{"openai"}, "allow_fallbacks": false}},
			})
			invocation, err := client.prepareInvocation(t.Context(), request, stream)
			if err != nil {
				t.Fatalf("effort=%q stream=%v: %v", effort, stream, err)
			}
			body := decodePreparedOpenRouterBody(t, invocation.Request.Body)
			if body["model"] != "openai/gpt-6.1-sol" || body["max_completion_tokens"] != float64(128000) || body["temperature"] != nil {
				t.Fatalf("incorrect request: %#v", body)
			}
			if body["response_format"].(map[string]interface{})["type"] != "json_object" {
				t.Fatal("JSON format lost")
			}
			provider := requireOpenRouterProviderObject(t, body)
			if provider["zdr"] != true || provider["data_collection"] != "deny" || provider["require_parameters"] != true || provider["allow_fallbacks"] != false || provider["only"].([]interface{})[0] != "openai" {
				t.Fatal("privacy/routing changed")
			}
			if effort != "" && body["reasoning"].(map[string]interface{})["effort"] != effort {
				t.Fatal("effort changed")
			}
			if effort == "" && body["reasoning"] != nil {
				t.Fatal("effort invented")
			}
			if invocation.Request.Report.EffectiveTemperature.Mode != core.AIParameterOmit || invocation.Request.Report.EffectiveMaxTokens.Value != 128000 || !invocation.Request.Report.Stable {
				t.Fatal("effective report not preserved")
			}
			fingerprint, stable := client.RequestFingerprint(t.Context(), request)
			again, againStable := client.RequestFingerprint(t.Context(), request)
			if !stable || !againStable || fingerprint == "" || fingerprint != again {
				t.Fatal("fingerprint mismatch")
			}
		}
	}
}

func TestOpenRouterSolReasoningFinalBoundary(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, model, effort, remove string
			omit, patch, invalid        bool
			controls                    map[string]interface{}
		}{
			{name: "inherited effort conflicts", effort: "high", controls: map[string]interface{}{"max_tokens": 100}, invalid: true},
			{name: "patch conflicts with inherited effort", effort: "high", patch: true, controls: map[string]interface{}{"max_tokens": 100}, invalid: true},
			{name: "explicit omission permits budget", effort: "high", omit: true, controls: map[string]interface{}{"max_tokens": 100}},
			{name: "patch repairs conflict", effort: "high", remove: "/reasoning/max_tokens", controls: map[string]interface{}{"max_tokens": 100}},
			{name: "patch repairs disabled", remove: "/reasoning/enabled", controls: map[string]interface{}{"enabled": false}},
			{name: "patch disables reasoning", effort: "high", patch: true, controls: map[string]interface{}{"enabled": false}, invalid: true},
			{name: "excluded output still reasons", effort: "high", controls: map[string]interface{}{"exclude": true}},
			{name: "Luna unchanged", model: "openai/gpt-6-luna", controls: map[string]interface{}{"enabled": false}},
			{name: "DeepSeek unchanged", model: "deepseek/deepseek-v4.1-flash", controls: map[string]interface{}{"enabled": false}},
			{name: "routed pro not opted in", model: "openai/gpt-6.1-sol-pro", controls: map[string]interface{}{"enabled": false}},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				model := tc.model
				if model == "" {
					model = "openai/gpt-6.1-sol"
				}
				calls := 0
				client, err := ai.NewRequestClient(ai.WithProviderAlias(openRouterProviderAlias),
					ai.WithAPIKey("synthetic"), ai.WithReasoningEffort(tc.effort),
					ai.WithHTTPClient(&http.Client{Transport: gpt6Transport(func(r *http.Request) (*http.Response, error) {
						calls++
						body := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
						contentType := "application/json"
						if stream {
							contentType = "text/event-stream"
							body = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
					})}))
				if err != nil {
					t.Fatal(err)
				}
				request := core.NewAIRequestFromLegacy("synthetic", "review", &core.AIOptions{
					Model: model, MaxTokens: 200, Extra: map[string]interface{}{"reasoning": tc.controls},
				})
				if tc.omit {
					request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
				}
				if tc.patch {
					request = core.NewAIRequestFromLegacy("synthetic", "review", &core.AIOptions{Model: model, MaxTokens: 200})
					set := make(map[string]interface{})
					for field, value := range tc.controls {
						set["/reasoning/"+field] = value
					}
					request.Patches = []core.AIProviderPatch{{Name: "controls", Version: "1", Set: set}}
				}
				if tc.remove != "" {
					request.Patches = []core.AIProviderPatch{{Name: "repair", Version: "1", Remove: []string{tc.remove}}}
				}
				for i := range request.Patches {
					request.Patches[i].Selector = core.AIProviderSelector{ProviderAlias: openRouterProviderAlias, Model: model}
				}
				before, err := core.CloneAIRequest(request)
				if err != nil {
					t.Fatal(err)
				}
				if stream {
					_, err = core.StreamAI(t.Context(), client, request, func(core.StreamChunk) error { return nil })
				} else {
					_, err = client.Generate(t.Context(), request)
				}
				if tc.invalid {
					if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) || calls != 0 {
						t.Fatalf("invalid request reached transport or lost error identity: calls=%d err=%v", calls, err)
					}
				} else if err != nil || calls != 1 {
					t.Fatalf("valid request failed: calls=%d err=%v", calls, err)
				}
				if !reflect.DeepEqual(request, before) {
					t.Fatal("caller request mutated")
				}
			})
		}
	}
}

func TestOpenRouterSolPolicyAndInvalidEffort(t *testing.T) {
	client := NewClient("synthetic", "", openRouterProviderAlias, &core.NoOpLogger{})
	client.ReasoningEffort = "none"
	request := core.NewAIRequestFromLegacy("synthetic", "planning", &core.AIOptions{Model: "openai/gpt-6.1-sol", MaxTokens: 100})
	for _, effort := range []string{"none", "minimal", "", "ultra"} {
		request.Generation.ReasoningEffort = core.SetAIParameter(effort)
		if _, err := client.prepareInvocation(t.Context(), request, false); err == nil {
			t.Fatalf("accepted invalid effort %q", effort)
		}
	}
	request.Generation.ReasoningEffort = core.AIParameter[string]{}
	if _, err := client.prepareInvocation(t.Context(), request, false); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("inherited none silently rewritten: %v", err)
	}
	request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
	if _, err := client.prepareInvocation(t.Context(), request, false); err != nil {
		t.Fatalf("explicit omission failed: %v", err)
	}
	request.Generation.ReasoningEffort = core.SetAIParameter("low")
	if _, err := client.prepareInvocation(t.Context(), request, false); err != nil {
		t.Fatalf("phase override failed: %v", err)
	}
	request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
	request.Generation.Temperature = core.SetAIParameter[float32](0.2)
	invocation, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range invocation.Request.Report.Adjustments {
		if a.Source == "built-in-rule" && a.Rule == "openrouter-sol-6.1-sampling@1" && a.Path == "/temperature" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing adjustment")
	}
	client.requestPolicy, err = newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityStrict)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.prepareInvocation(t.Context(), request, false); err == nil {
		t.Fatal("strict accepted unacknowledged sampling removal")
	}
	client.requestPolicy, err = newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{Name: "allow-omission", Version: "1", Selector: core.AIProviderSelector{ProviderAlias: openRouterProviderAlias}, Remove: []string{"/temperature"}}}, nil, requestpolicy.CompatibilityStrict)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.prepareInvocation(t.Context(), request, false); err != nil {
		t.Fatal(err)
	}
	request.Generation.Temperature = core.OmitAIParameter[float32]()
	for _, patch := range []core.AIProviderPatch{
		{Name: "bad-effort", Version: "1", Set: map[string]interface{}{"/reasoning": map[string]interface{}{"effort": "none"}}},
		{Name: "bad-sampling", Version: "1", Set: map[string]interface{}{"/temperature": 0.2}},
		{Name: "bad-top-p", Version: "1", Set: map[string]interface{}{"/top_p": 0.9}},
		{Name: "bad-top-k", Version: "1", Set: map[string]interface{}{"/top_k": 10}},
		{Name: "bad-top-k-case", Version: "1", Set: map[string]interface{}{"/Top_K": 10}},
		{Name: "bad-reasoning-case", Version: "1", Set: map[string]interface{}{"/Reasoning": map[string]interface{}{"effort": "low"}}},
		{Name: "bad-case", Version: "1", Set: map[string]interface{}{"/reasoning": map[string]interface{}{"Effort": "none"}}},
		{Name: "bad-object", Version: "1", Set: map[string]interface{}{"/reasoning": "none"}},
	} {
		request.Patches = []core.AIProviderPatch{patch}
		if _, err := client.prepareInvocation(t.Context(), request, false); err == nil {
			t.Fatal("policy bypassed final validation")
		}
	}
}

func TestOpenRouterSolFinalDraftBoundary(t *testing.T) {
	codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{SurfaceVersion: openaiwire.DefaultSurfaceVersion})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		field string
		value interface{}
	}{
		{"temperature", 0.2}, {"top_k", 10}, {"Top_K", 10},
		{"Reasoning", map[string]interface{}{"effort": "low"}},
		{"reasoning", map[string]interface{}{"Effort": "low"}},
		{"reasoning", "low"}, {"reasoning", map[string]interface{}{"effort": "none"}},
		{"messages", nil},
	} {
		t.Run(tc.field, func(t *testing.T) {
			request := core.NewAIRequestFromLegacy("synthetic", "planning", &core.AIOptions{MaxTokens: 100})
			draft, err := codec.BuildDraftWithProfile(request, openaiwire.RequestProfile{
				SemanticModel: "openai/gpt-6.1-sol", WireModel: "openai/gpt-6.1-sol",
				ModelField: openaiwire.ModelFieldRequired, TokenLimit: openaiwire.TokenLimitMaxCompletionTokens,
				TokenBudget: openaiwire.TokenBudgetExact, ReasoningEffort: openaiwire.ReasoningEffortNestedObject,
				Sampling: openaiwire.SamplingOrdinary,
			}, false)
			if err != nil {
				t.Fatal(err)
			}
			// Isolate final validation from the policy engine's own earlier rejection.
			wrapped := &openRouterSolDraft{Draft: draft}
			if got := wrapped.PolicyFingerprintIdentity(); got != draft.PolicyFingerprintIdentity()+"|openrouter-sol-6.1-v3" {
				t.Fatalf("wrong final-contract identity: %s", got)
			}
			draft.Body()[tc.field] = tc.value
			if err := wrapped.Validate(); err == nil {
				t.Fatal("invalid final draft accepted")
			}
		})
	}
}

func TestSolPlanningOverridePreservesOtherModelRoles(t *testing.T) {
	t.Setenv("TRUVAG3_OPENROUTER_MODEL_PLANNING", "openai/gpt-6.1-sol")
	t.Setenv("TRUVAG3_OPENROUTER_MODEL_FAST", "openai/gpt-6-luna")
	t.Setenv("TRUVAG3_OPENROUTER_MODEL_DEFAULT", "deepseek/deepseek-v4.1-flash")
	var captured map[string]interface{}
	client, err := ai.NewRequestClient(ai.WithProviderAlias(openRouterProviderAlias), ai.WithAPIKey("synthetic"),
		ai.WithReasoningEffort("none"), ai.WithReasoningTokenMultiplier(1),
		ai.WithHTTPClient(&http.Client{Transport: gpt6Transport(func(r *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)), Request: r}, nil
		})}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ alias, effort, model, wireEffort string }{
		{"planning", "low", "openai/gpt-6.1-sol", "low"},
		{"fast", "", "openai/gpt-6-luna", "none"},
		{"default", "", "deepseek/deepseek-v4.1-flash", ""},
	} {
		captured = nil
		request := core.NewAIRequestFromLegacy("synthetic", tc.alias, &core.AIOptions{Model: tc.alias, MaxTokens: 128000, Temperature: 0.2, ResponseFormat: "json", ReasoningEffort: tc.effort})
		response, err := client.Generate(t.Context(), request)
		if err != nil {
			t.Fatalf("%s: %v", tc.alias, err)
		}
		if captured["model"] != tc.model || captured["max_completion_tokens"] != float64(128000) || response.RequestReport.EffectiveMaxTokens.Value != 128000 {
			t.Fatalf("wrong model/budget: %#v", captured)
		}
		if tc.wireEffort == "" {
			if captured["reasoning"] != nil || captured["temperature"] == nil {
				t.Fatal("DeepSeek wire settings changed")
			}
		} else if captured["reasoning"].(map[string]interface{})["effort"] != tc.wireEffort || captured["temperature"] != nil {
			t.Fatal("wrong reasoning/sampling settings")
		}
	}
}
