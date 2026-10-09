package openai

import (
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestDefaultBudgetHasHeadroomAndExplicitScalingIsPreserved(t *testing.T) {
	for _, provider := range []string{"openai", openRouterProviderAlias} {
		for _, stream := range []bool{false, true} {
			for _, multiplier := range []int{0, 1, 2} {
				client := NewClient("synthetic", "", provider, &core.NoOpLogger{})
				if client.requestTimeout != 5*time.Minute {
					t.Fatalf("default request timeout = %s", client.requestTimeout)
				}
				client.ReasoningTokenMultiplier = multiplier
				client.ReasoningEffort = "high" // inherited effort follows the same budget rule
				model := "gpt-6.1-sol"
				if provider == openRouterProviderAlias {
					model = "openai/" + model
				}
				for _, budget := range []int{30000, 50000} {
					request := core.NewAIRequest("Return JSON", "budget-default")
					request.Generation.Model = model
					request.Generation.MaxTokens = core.SetAIParameter(budget)
					prepared, err := client.prepareInvocation(t.Context(), request, stream)
					if err != nil {
						t.Fatal(err)
					}
					want := budget
					if multiplier == 0 {
						want *= 3
					} else {
						want *= multiplier
					}
					want = min(want, 128000)
					body := decodePreparedOpenRouterBody(t, prepared.Request.Body)
					if body["max_completion_tokens"] != float64(want) || prepared.Request.Report.EffectiveMaxTokens.Value != want {
						t.Fatalf("provider=%s stream=%t multiplier=%d: wire=%v report=%v want=%d", provider, stream, multiplier, body["max_completion_tokens"], prepared.Request.Report.EffectiveMaxTokens, want)
					}
				}
			}
		}
	}
}
