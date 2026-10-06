package azureopenai

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestAzureOmittedEffortPreservesRequestedBudget(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://example.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "test", RouteIdentity: "omission-regression",
	}}
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "placeholder", Model: "o3",
		ReasoningEffort: "high", ReasoningTokenMultiplier: 5,
	}, ai.ProviderIntegrationConfig{EndpointResolver: resolver})
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			effort core.AIParameter[string]
			want   int
			wire   string
		}{
			{name: "inherit", want: 500, wire: "high"},
			{name: "omit", effort: core.OmitAIParameter[string](), want: 100},
			{name: "none", effort: core.SetAIParameter("none"), want: 100, wire: "none"},
			{name: "explicit", effort: core.SetAIParameter("low"), want: 500, wire: "low"},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				req := core.NewAIRequest("hello", "omission-regression")
				req.Generation.MaxTokens = core.SetAIParameter(100)
				req.Generation.ReasoningEffort = tc.effort
				invocation, err := client.prepareInvocation(t.Context(), req, stream)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]interface{}
				if err := json.Unmarshal(invocation.Request.Body, &body); err != nil {
					t.Fatal(err)
				}
				if body["max_completion_tokens"] != float64(tc.want) {
					t.Fatalf("wire budget = %v, want %d", body["max_completion_tokens"], tc.want)
				}
				if tc.wire == "" {
					if _, exists := body["reasoning_effort"]; exists {
						t.Fatal("omitted effort present on wire")
					}
				} else if body["reasoning_effort"] != tc.wire {
					t.Fatalf("wire effort = %v, want %s", body["reasoning_effort"], tc.wire)
				}
				if invocation.Request.Report == nil || invocation.Request.Report.EffectiveMaxTokens != core.SetAIParameter(tc.want) {
					t.Fatalf("report does not match wire budget: %#v", invocation.Request.Report)
				}
			})
		}
	}
}
