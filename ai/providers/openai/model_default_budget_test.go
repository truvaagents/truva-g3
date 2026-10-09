package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestModelDefaultReasoningBudgetOnWire(t *testing.T) {
	for _, provider := range []string{"openai", openRouterProviderAlias} {
		models := []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol"}
		if provider == openRouterProviderAlias {
			models = []string{"openai/gpt-6-luna", "openai/gpt-6.1-sol"}
		}
		for _, model := range models {
			for _, stream := range []bool{false, true} {
				for _, budget := range []int{500, 512, 1000, 1500, 2000, 2500, 20000, 30000, 50000} {
					want := min(budget*3, 128000)
					calls := 0
					transport := gpt6Transport(func(req *http.Request) (*http.Response, error) {
						calls++
						var body map[string]interface{}
						require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
						require.Equal(t, float64(want), body["max_completion_tokens"])
						require.NotContains(t, body, "reasoning_effort")
						require.NotContains(t, body, "reasoning")
						require.Contains(t, body["messages"].([]interface{})[0].(map[string]interface{})["content"], "exact **payload**")
						response := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
						if stream {
							response = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
						}
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
					})
					client, err := ai.NewRequestClient(ai.WithProvider("openai"), ai.WithProviderAlias(provider), ai.WithAPIKey("synthetic"), ai.WithMaxRetries(0), ai.WithHTTPClient(&http.Client{Transport: transport}))
					require.NoError(t, err)
					request := core.NewAIRequest("exact **payload**", "auxiliary-budget")
					request.Generation.Model = model
					request.Generation.MaxTokens = core.SetAIParameter(budget)
					var result *core.AIResult
					if stream {
						result, err = core.StreamAI(t.Context(), client, request, func(core.StreamChunk) error { return nil })
					} else {
						result, err = client.Generate(t.Context(), request)
					}
					require.NoError(t, err)
					require.Equal(t, 1, calls)
					require.Equal(t, core.SetAIParameter(want), result.RequestReport.EffectiveMaxTokens)
					require.Contains(t, result.RequestReport.Adjustments, core.AIRequestAdjustment{Source: "wire-profile", Rule: "model-default-reasoning-headroom-v1", Path: "/max_completion_tokens", Action: "scale", Reason: "reviewed model default uses shared reasoning and answer allowance"})
					require.Equal(t, budget, request.Generation.MaxTokens.Value)
				}
			}
		}
	}
}

func TestModelDefaultBudgetPreservesOverridesAndUnknowns(t *testing.T) {
	for _, provider := range []string{"openai", openRouterProviderAlias} {
		model := "gpt-6-luna"
		if provider == openRouterProviderAlias {
			model = "openai/" + model
		}
		for _, mode := range []string{"default", "one", "none", "omit", "native", "omit-tokens", "unknown"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				client := NewClient("synthetic", "", provider, nil)
				request := core.NewAIRequest("unchanged", "budget")
				request.Generation.Model = model
				request.Generation.MaxTokens = core.SetAIParameter(512)
				want := 512
				switch mode {
				case "default":
					want = 1536
				case "one":
					client.ReasoningTokenMultiplier = 1
				case "none":
					request.Generation.ReasoningEffort = core.SetAIParameter("none")
				case "omit":
					request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
				case "native":
					extra := map[string]interface{}{"reasoning_effort": "none"}
					if provider == openRouterProviderAlias {
						extra = map[string]interface{}{"reasoning": map[string]interface{}{"enabled": false}}
					}
					request = core.NewAIRequestFromLegacy("unchanged", "budget", &core.AIOptions{Model: model, MaxTokens: 512, Extra: extra})
				case "omit-tokens":
					request.Generation.MaxTokens = core.OmitAIParameter[int]()
					want = 0
				case "unknown":
					request.Generation.Model = "gpt-5.6-terra"
					if provider == openRouterProviderAlias {
						request.Generation.Model = "deepseek/deepseek-v4.1-flash"
					}
				}
				prepared, err := client.prepareInvocation(t.Context(), request, false)
				require.NoError(t, err)
				body := decodePreparedOpenRouterBody(t, prepared.Request.Body)
				if want == 0 {
					require.NotContains(t, body, "max_completion_tokens")
				} else {
					require.Equal(t, float64(want), body["max_completion_tokens"])
				}
			})
		}
	}
}
