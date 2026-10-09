package openai

import (
	"bytes"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestFastAliasesUseLunaContractAndPreserveOverrides(t *testing.T) {
	for _, tc := range []struct {
		provider, model, previous, env string
	}{
		{"openai", "gpt-6-luna", "gpt-5.6-luna", "TRUVAG3_OPENAI_MODEL_FAST"},
		{openRouterProviderAlias, "openai/gpt-6-luna", "openai/gpt-5.6-luna", "TRUVAG3_OPENROUTER_MODEL_FAST"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			t.Setenv(tc.env, "")
			client := NewClient("synthetic", "", tc.provider, &core.NoOpLogger{})
			client.DefaultModel = "fast"
			for _, stream := range []bool{false, true} {
				for _, effort := range []string{"", "none", "high"} {
					request := core.NewAIRequestFromLegacy("Return JSON", "fast-alias", &core.AIOptions{
						MaxTokens: 512, Temperature: 0.01, ReasoningEffort: effort,
					})
					request.Generation.ResponseFormat = core.SetAIParameter("json")
					alias, err := client.prepareInvocation(t.Context(), request, stream)
					if err != nil {
						t.Fatal(err)
					}
					if alias.Request.Report.ResolvedModel != tc.model || !alias.Request.Report.Stable {
						t.Fatalf("incorrect resolved report: %#v", alias.Request.Report)
					}
					request.Generation.Model = tc.model
					concrete, err := client.prepareInvocation(t.Context(), request, stream)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(alias.Request.Body, concrete.Request.Body) {
						t.Fatalf("alias changed concrete wire contract: stream=%t effort=%q", stream, effort)
					}
					body := decodePreparedOpenRouterBody(t, alias.Request.Body)
					wantTokens := 512
					if effort != "none" {
						wantTokens *= 3
					}
					if body["model"] != tc.model || body["max_completion_tokens"] != float64(wantTokens) {
						t.Fatalf("incorrect model/budget: %#v", body)
					}
					if tc.provider == openRouterProviderAlias {
						provider := requireOpenRouterProviderObject(t, body)
						if body["temperature"] != nil || provider["zdr"] != true || provider["data_collection"] != "deny" || provider["require_parameters"] != true {
							t.Fatalf("routed sampling/privacy contract lost: %#v", body)
						}
					}
				}
			}
			request := core.NewAIRequest("Return JSON", "fast-alias")
			before, err := client.prepareInvocation(t.Context(), request, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(tc.env, tc.previous)
			after, err := client.prepareInvocation(t.Context(), request, false)
			if err != nil {
				t.Fatal(err)
			}
			if after.Request.Report.ResolvedModel != tc.previous || before.Request.Report.Fingerprint == after.Request.Report.Fingerprint {
				t.Fatal("environment rollback or resolved-model fingerprint lost")
			}
			request.Generation.Model = tc.model
			explicit, err := client.prepareInvocation(t.Context(), request, false)
			if err != nil {
				t.Fatal(err)
			}
			if explicit.Request.Report.ResolvedModel != tc.model {
				t.Fatal("fast environment override replaced concrete model")
			}
		})
	}
}
