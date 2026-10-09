package openai

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestNumericBudgetsReachWireAndEffectiveReport(t *testing.T) {
	for _, provider := range []string{"openai", openRouterProviderAlias} {
		for _, stream := range []bool{false, true} {
			for _, literal := range []string{"1e3", "1000.0", "1.000e+3"} {
				calls := 0
				transport := gpt6Transport(func(request *http.Request) (*http.Response, error) {
					calls++
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
					require.Equal(t, literal, string(body["max_completion_tokens"]))
					response := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
					if stream {
						response = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
				})
				client, err := ai.NewRequestClient(ai.WithProvider("openai"), ai.WithProviderAlias(provider), ai.WithAPIKey("synthetic"), ai.WithMaxRetries(0), ai.WithHTTPClient(&http.Client{Transport: transport}))
				require.NoError(t, err)
				request := core.NewAIRequest("exact input", "numeric-budget")
				request.Generation.Model = "gpt-6.1-sol"
				if provider == openRouterProviderAlias {
					request.Generation.Model = "openai/gpt-6.1-sol"
				}
				request.Patches = []core.AIProviderPatch{{Name: "app-budget", Version: "1", Selector: core.AIProviderSelector{ProviderAlias: provider}, Set: map[string]interface{}{"/max_completion_tokens": json.Number(literal)}}}
				var result *core.AIResult
				if stream {
					result, err = core.StreamAI(t.Context(), client, request, func(core.StreamChunk) error { return nil })
				} else {
					result, err = client.Generate(t.Context(), request)
				}
				require.NoError(t, err)
				require.Equal(t, 1, calls)
				require.Equal(t, core.SetAIParameter(1000), result.RequestReport.EffectiveMaxTokens)
				require.Equal(t, json.Number(literal), request.Patches[0].Set["/max_completion_tokens"])
				for _, invalid := range []interface{}{json.Number("1000.00000000000000000001"), json.Number("128001e0"), json.Number("1e99999"), "1000"} {
					request.Patches[0].Set["/max_completion_tokens"] = invalid
					_, err = client.Generate(t.Context(), request)
					require.Error(t, err)
					require.Equal(t, 1, calls, "invalid budget dispatched")
				}
			}
		}
	}
}

func TestReasoningMultiplierInvalidatesOutputCacheIdentity(t *testing.T) {
	for _, provider := range []string{"openai", openRouterProviderAlias} {
		for _, stream := range []bool{false, true} {
			fingerprints := make(map[string]bool)
			for _, multiplier := range []int{1, 3, 5} {
				client := NewClient("synthetic", "", provider, nil)
				client.ReasoningTokenMultiplier = multiplier
				client.ReasoningEffort = "low"
				request := core.NewAIRequest("synthetic", "activity-compaction")
				request.Generation.Model = "gpt-6.1-sol"
				if provider == openRouterProviderAlias {
					request.Generation.Model = "openai/gpt-6.1-sol"
				}
				request.Generation.MaxTokens = core.SetAIParameter(2000)
				prepared, err := client.prepareInvocation(t.Context(), request, stream)
				require.NoError(t, err)
				require.True(t, prepared.Request.Report.Stable)
				require.Equal(t, 2000*multiplier, prepared.Request.Report.EffectiveMaxTokens.Value)
				fingerprints[prepared.Request.Report.Fingerprint] = true
				if !stream {
					fingerprint, stable := client.RequestFingerprint(t.Context(), request)
					require.True(t, stable)
					require.Equal(t, fingerprint, prepared.Request.Report.Fingerprint)
				}
			}
			require.Len(t, fingerprints, 3, "different allowances reused a stable cache identity")
		}
	}
}

func TestBudgetIntegerPreservesJSONNumberForms(t *testing.T) {
	for _, value := range []interface{}{1000, float64(1000), json.Number("1e3"), json.Number("1000.0")} {
		n, err := budgetInteger(value)
		require.NoError(t, err)
		require.Equal(t, 1000, n)
	}
	for _, value := range []interface{}{nil, "1000", -1, 1.5, float64(math.MaxInt), uint64(math.MaxUint64)} {
		_, err := budgetInteger(value)
		require.Error(t, err)
	}
}

func TestOutputCapacityPolicyAndOverrides(t *testing.T) {
	for _, provider := range []string{"openai", openRouterProviderAlias} {
		for _, stream := range []bool{false, true} {
			model := "gpt-6.1-sol"
			if provider == openRouterProviderAlias {
				model = "openai/" + model
			}
			client := NewClient("synthetic", "", provider, nil)
			request := core.NewAIRequest("exact input", "capacity")
			request.Generation.Model = model
			request.Generation.ReasoningEffort = core.SetAIParameter("low")
			request.Generation.MaxTokens = core.SetAIParameter(50000)
			prepared, err := client.prepareInvocation(t.Context(), request, stream)
			require.NoError(t, err)
			require.Equal(t, 128000, prepared.Request.Report.EffectiveMaxTokens.Value)
			require.Equal(t, 50000, request.Generation.MaxTokens.Value)
			require.Contains(t, prepared.Request.Report.Adjustments, core.AIRequestAdjustment{
				Source: "wire-profile", Rule: "reasoning-headroom-ceiling-v1", Path: "/max_completion_tokens", Action: "cap", Reason: "derived reasoning headroom bounded by reviewed model capacity",
			})
			client.requestPolicy, err = newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityStrict)
			require.NoError(t, err)
			prepared, err = client.prepareInvocation(t.Context(), request, stream)
			require.NoError(t, err)
			require.Equal(t, 128000, prepared.Request.Report.EffectiveMaxTokens.Value)
			request.Generation.MaxTokens = core.SetAIParameter(128001)
			_, err = client.prepareInvocation(t.Context(), request, stream)
			require.ErrorContains(t, err, "explicit request intent")
			request.Patches = []core.AIProviderPatch{{Name: "app-budget", Version: "1", Selector: core.AIProviderSelector{ProviderAlias: provider}, Set: map[string]interface{}{"/max_completion_tokens": 64000}}}
			prepared, err = client.prepareInvocation(t.Context(), request, stream)
			require.NoError(t, err)
			require.Equal(t, 64000, prepared.Request.Report.EffectiveMaxTokens.Value)
			request.Patches[0].Set["/max_completion_tokens"] = 160000
			_, err = client.prepareInvocation(t.Context(), request, stream)
			require.ErrorContains(t, err, "output-capacity")
		}
	}
}

func TestOutputBudgetUnknownOmittedAndOverflow(t *testing.T) {
	client := NewClient("synthetic", "", "openai", nil)
	request := core.NewAIRequestFromLegacy("exact", "budget", &core.AIOptions{Model: "gpt-6.1-sol", MaxTokens: math.MaxInt, ReasoningEffort: "low"})
	_, err := client.prepareInvocation(t.Context(), request, false)
	require.ErrorContains(t, err, "overflows")
	request.Generation.MaxTokens = core.OmitAIParameter[int]()
	prepared, err := client.prepareInvocation(t.Context(), request, false)
	require.NoError(t, err)
	require.Equal(t, core.AIParameterOmit, prepared.Request.Report.EffectiveMaxTokens.Mode)
	for _, model := range []string{"unknown", "gpt-6.1-sol-pro", "gpt-6-sol-unknown"} {
		require.Zero(t, reviewedOutputLimit("openai", model))
	}
	require.Zero(t, reviewedOutputLimit("openai.openrouter", "another/model"))
	request = core.NewAIRequestFromLegacy("exact", "budget", &core.AIOptions{Model: "gpt-6.1-sol", MaxTokens: 50000})
	client.ReasoningEffort = "high"
	request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
	prepared, err = client.prepareInvocation(t.Context(), request, false)
	require.NoError(t, err)
	require.Equal(t, 50000, prepared.Request.Report.EffectiveMaxTokens.Value)
}
