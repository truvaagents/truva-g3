package openai

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestOpenRouterLunaSamplingPolicy(t *testing.T) {
	for _, effort := range []string{"", "none", "high"} {
		for _, stream := range []bool{false, true} {
			client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
			request := core.NewAIRequestFromLegacy("synthetic", "contract", &core.AIOptions{
				Model: "openai/gpt-6-luna", Temperature: 0.01, MaxTokens: 2500, ReasoningEffort: effort,
				Extra: map[string]interface{}{"provider": map[string]interface{}{
					"only": []string{"openai"}, "order": []string{"openai"}, "allow_fallbacks": false,
				}},
			})
			request.Generation.ResponseFormat = core.SetAIParameter("json")
			if effort != "high" {
				request.Generation.Temperature = core.SetAIParameter[float32](0.01)
			}
			invocation, err := client.prepareInvocation(t.Context(), request, stream)
			if err != nil {
				t.Fatal(err)
			}
			body := decodePreparedOpenRouterBody(t, invocation.Request.Body)
			if _, exists := body["temperature"]; exists {
				t.Fatal("routed Luna temperature survived")
			}
			wantBudget := 2500
			if effort != "none" {
				wantBudget *= 3
			}
			if body["max_completion_tokens"] != float64(wantBudget) {
				t.Fatal("wrong routed reasoning allowance")
			}
			provider := requireOpenRouterProviderObject(t, body)
			if provider["zdr"] != true || provider["data_collection"] != "deny" || provider["require_parameters"] != true || provider["allow_fallbacks"] != false || !reflect.DeepEqual(provider["only"], []interface{}{"openai"}) || !reflect.DeepEqual(provider["order"], []interface{}{"openai"}) {
				t.Fatalf("routing changed: %#v", provider)
			}
			if effort != "" && body["reasoning"].(map[string]interface{})["effort"] != effort {
				t.Fatal("effort changed")
			}
			found := false
			for _, a := range invocation.Request.Report.Adjustments {
				if a.Source == "built-in-rule" && a.Rule == "openrouter-luna-sampling@1" && a.Path == "/temperature" {
					found = true
				}
			}
			// Active reasoning already omits inherited sampling in the codec.
			// Explicit incompatible sampling fails its pre-policy validation.
			if effort != "high" && !found || invocation.Request.Report.EffectiveTemperature.Mode != core.AIParameterOmit {
				t.Fatalf("missing adjustment/effective omission: %#v", invocation.Request.Report)
			}
			if _, stable := client.RequestFingerprint(t.Context(), request); !stable {
				t.Fatal("concrete route fingerprint unstable")
			}
		}
	}
}

func TestOpenRouterLunaStrictModeAndUnmatchedPolicy(t *testing.T) {
	client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
	var err error
	client.requestPolicy, err = newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityStrict)
	if err != nil {
		t.Fatal(err)
	}
	request := core.NewAIRequest("synthetic", "contract")
	request.Generation.Model = "openai/gpt-6-luna"
	request.Generation.Temperature = core.SetAIParameter[float32](0.01)
	if _, err := client.prepareInvocation(t.Context(), request, false); err == nil {
		t.Fatal("strict accepted explicit sampling removal")
	}
	client.requestPolicy, err = newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{Name: "allow-omission", Version: "1", Selector: core.AIProviderSelector{ProviderAlias: openRouterProviderAlias}, Remove: []string{"/temperature"}}}, nil, requestpolicy.CompatibilityStrict)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.prepareInvocation(t.Context(), request, false); err != nil {
		t.Fatal(err)
	}
	request.Generation.ReasoningEffort = core.SetAIParameter("high")
	if _, err := client.prepareInvocation(t.Context(), request, false); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatal("pre-policy codec validation of active reasoning sampling changed")
	}
	for _, model := range []string{"deepseek/deepseek-v4.1-flash", "openai/gpt-5.6-luna", "openai/gpt-6-luna-future"} {
		client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
		request := core.NewAIRequestFromLegacy("synthetic", "contract", &core.AIOptions{Model: model, Temperature: 0.1})
		before, err := client.prepareInvocation(t.Context(), request, false)
		if err != nil {
			t.Fatal(err)
		}
		client.requestPolicy, err = requestpolicy.NewEngine(requestpolicy.Config{})
		if err != nil {
			t.Fatal(err)
		}
		after, err := client.prepareInvocation(t.Context(), request, false)
		if err != nil {
			t.Fatal(err)
		}
		if string(before.Request.Body) != string(after.Request.Body) || before.Request.Report.Fingerprint != after.Request.Report.Fingerprint {
			t.Fatalf("unmatched model changed: %s", model)
		}
	}
}

type effortLog struct {
	core.NoOpLogger
	debug, warn int
}

func (l *effortLog) DebugWithContext(context.Context, string, map[string]interface{}) { l.debug++ }
func (l *effortLog) WarnWithContext(context.Context, string, map[string]interface{})  { l.warn++ }

func TestInheritedEffortDiagnosticsAndWireStability(t *testing.T) {
	for _, stream := range []bool{false, true} {
		logger := &effortLog{}
		client := NewClient("key", "", openRouterProviderAlias, logger)
		request := core.NewAIRequestFromLegacy("synthetic", "planning", &core.AIOptions{Model: "deepseek/deepseek-v4.1-flash", Temperature: 0.1, ResponseFormat: "json", MaxTokens: 30000})
		before, err := client.prepareInvocation(t.Context(), request, stream)
		if err != nil {
			t.Fatal(err)
		}
		client.ReasoningEffort = "none"
		after, err := client.prepareInvocation(t.Context(), request, stream)
		if err != nil {
			t.Fatal(err)
		}
		if string(before.Request.Body) != string(after.Request.Body) || len(after.Request.Report.Adjustments) != len(before.Request.Report.Adjustments)+1 || logger.warn != 0 || logger.debug != 1 {
			t.Fatalf("default adaptation mismatch: debug=%d warn=%d report=%#v", logger.debug, logger.warn, after.Request.Report)
		}
		if _, stable := client.RequestFingerprint(t.Context(), request); !stable || logger.warn != 0 {
			t.Fatal("fingerprint preparation warned/failed")
		}
		request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
		priorDebug := logger.debug
		if _, err := client.prepareInvocation(t.Context(), request, stream); err != nil || logger.debug != priorDebug || logger.warn != 0 {
			t.Fatal("explicit omission was not quiet")
		}
		request.Generation.ReasoningEffort = core.SetAIParameter("high")
		if _, err := client.prepareInvocation(t.Context(), request, stream); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
			t.Fatal("explicit unsupported intent accepted")
		}
		legacy := core.NewAIRequestFromLegacy("synthetic", "planning", &core.AIOptions{Model: "deepseek/deepseek-v4.1-flash", ReasoningEffort: "high"})
		if _, err := client.prepareInvocation(t.Context(), legacy, stream); err != nil || logger.warn != 1 {
			t.Fatal("legacy request degradation lost")
		}
	}
}
