package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

// Exercise the public request client and final wire body, not only the helper.
// In Strict mode only the caller's over-cap base budget is a conflict; derived
// allowance is bounded before compatibility rules evaluate explicit intent.
func TestHeadroomCeilingOnWire(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "gpt-5.6-sol", "openai/gpt-6.1-sol", "openai/gpt-5.6-luna"} {
		provider := "openai"
		if strings.HasPrefix(model, "openai/") {
			provider = openRouterProviderAlias
		}
		for _, mode := range []requestpolicy.CompatibilityMode{requestpolicy.CompatibilityCompatible, requestpolicy.CompatibilityStrict} {
			for _, stream := range []bool{false, true} {
				for _, multiplier := range []int{1, 3, 5} {
					for _, effort := range []string{"default", "low", "omit"} {
						for _, base := range []int{50000, 128000, 128001} {
							t.Run(fmt.Sprintf("%s/mode=%d/stream=%t/multiplier=%d/%s/base=%d", model, mode, stream, multiplier, effort, base), func(t *testing.T) {
								active := effort == "low" || (effort == "default" && strings.Contains(model, "6.1-sol"))
								want := base
								if active {
									want *= multiplier
								}
								bounded := active && base <= 128000 && want > 128000
								want = min(want, 128000)
								calls := 0
								transport := gpt6Transport(func(req *http.Request) (*http.Response, error) {
									calls++
									var body map[string]interface{}
									require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
									require.Equal(t, float64(want), body["max_completion_tokens"])
									require.NotContains(t, body, "max_tokens")
									require.Equal(t, "exact **payload**", body["messages"].([]interface{})[0].(map[string]interface{})["content"])
									if effort != "low" {
										require.NotContains(t, body, "reasoning_effort")
										require.NotContains(t, body, "reasoning")
									}
									response := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
									if stream {
										response = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
									}
									return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
								})
								client, err := ai.NewRequestClient(ai.WithProvider("openai"), ai.WithProviderAlias(provider), ai.WithAPIKey("synthetic"),
									ai.WithMaxRetries(0), ai.WithReasoningTokenMultiplier(multiplier), ai.WithCompatibilityMode(mode), ai.WithHTTPClient(&http.Client{Transport: transport}))
								require.NoError(t, err)
								request := core.NewAIRequest("exact **payload**", "headroom-regression")
								request.Generation.Model = model
								request.Generation.MaxTokens = core.SetAIParameter(base)
								switch effort {
								case "low":
									request.Generation.ReasoningEffort = core.SetAIParameter("low")
								case "omit":
									request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
								}
								var result *core.AIResult
								if stream {
									result, err = core.StreamAI(t.Context(), client, request, func(core.StreamChunk) error { return nil })
								} else {
									result, err = client.Generate(t.Context(), request)
								}
								if mode == requestpolicy.CompatibilityStrict && base > 128000 {
									require.ErrorContains(t, err, "explicit request intent")
									require.Zero(t, calls)
									return
								}
								require.NoError(t, err)
								require.Equal(t, 1, calls)
								require.Equal(t, core.SetAIParameter(want), result.RequestReport.EffectiveMaxTokens)
								require.Equal(t, core.SetAIParameter(base), request.Generation.MaxTokens)
								var headroomCapped, callerClamped bool
								for _, adjustment := range result.RequestReport.Adjustments {
									headroomCapped = headroomCapped || adjustment.Rule == "reasoning-headroom-ceiling-v1"
									callerClamped = callerClamped || adjustment.Rule == "model-output-ceiling@1"
								}
								require.Equal(t, bounded, headroomCapped)
								require.Equal(t, base > 128000, callerClamped)
							})
						}
					}
				}
			}
		}
	}
}

func TestCapacityOnlyModelsAndAliases(t *testing.T) {
	for _, alias := range []string{"gpt-5.6-sol", "smart", "code", "premium"} {
		client := NewClient("synthetic", "", "openai", nil)
		request := core.NewAIRequest("exact", "alias-capacity")
		request.Generation.Model = alias
		request.Generation.MaxTokens = core.SetAIParameter(50000)
		prepared, err := client.prepareInvocation(t.Context(), request, false)
		require.NoError(t, err)
		require.Equal(t, 50000, prepared.Request.Report.EffectiveMaxTokens.Value, "capacity row must not add implicit reasoning")
		request.Generation.ReasoningEffort = core.SetAIParameter("low")
		prepared, err = client.prepareInvocation(t.Context(), request, false)
		require.NoError(t, err)
		require.Equal(t, 128000, prepared.Request.Report.EffectiveMaxTokens.Value)
	}
	for _, pair := range [][2]string{{"openai", "gpt-5.6-sol"}, {openRouterProviderAlias, "openai/gpt-5.6-luna"}} {
		contract := reviewedModelBudget(pair[0], pair[1])
		require.Equal(t, 128000, contract.outputLimit)
		require.False(t, contract.reasoningActiveByDefault)
		require.Zero(t, reviewedOutputLimit(pair[0], pair[1]+"-2026-10-08"))
		require.Zero(t, reviewedOutputLimit(pair[0], pair[1]+"-pro"))
	}
}
