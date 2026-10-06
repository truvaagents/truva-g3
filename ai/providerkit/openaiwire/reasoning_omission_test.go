package openaiwire_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

func TestReasoningOmissionPrecedesBudgetPreparation(t *testing.T) {
	codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{
		SurfaceVersion:         openaiwire.DefaultSurfaceVersion,
		DefaultReasoningEffort: "high", ReasoningTokenMultiplier: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		for _, style := range []openaiwire.ReasoningEffortStyle{openaiwire.ReasoningEffortTopLevel, openaiwire.ReasoningEffortNestedObject} {
			for _, tc := range []struct {
				name   string
				effort core.AIParameter[string]
				want   int
			}{
				{name: "inherit", want: 500},
				{name: "omit", effort: core.OmitAIParameter[string](), want: 100},
				{name: "none", effort: core.SetAIParameter("none"), want: 100},
				{name: "explicit", effort: core.SetAIParameter("low"), want: 500},
			} {
				t.Run(fmt.Sprintf("stream=%t/style=%v/%s", stream, style, tc.name), func(t *testing.T) {
					req := core.NewAIRequest("hello", "omission-regression")
					req.Generation.MaxTokens = core.SetAIParameter(100)
					req.Generation.ReasoningEffort = tc.effort
					draft, err := codec.BuildDraftWithProfile(req, openaiwire.RequestProfile{
						SemanticModel: "gpt-5", WireModel: "gpt-5", ModelField: openaiwire.ModelFieldRequired,
						TokenLimit: openaiwire.TokenLimitMaxCompletionTokens, TokenBudget: openaiwire.TokenBudgetScaleForReasoning,
						ReasoningEffort: style, Sampling: openaiwire.SamplingReasoningRestricted,
					}, stream)
					if err != nil {
						t.Fatal(err)
					}
					body := draft.Body()
					if strings.HasSuffix(draft.PolicyFingerprintIdentity(), ":reasoning-omission-v1") != (tc.name == "omit") {
						t.Fatalf("omission identity scope = %s", draft.PolicyFingerprintIdentity())
					}
					if body["max_completion_tokens"] != tc.want {
						t.Fatalf("budget = %v, want %d", body["max_completion_tokens"], tc.want)
					}
					if tc.name == "omit" {
						for _, key := range []string{"reasoning", "reasoning_effort"} {
							if _, exists := body[key]; exists {
								t.Fatalf("omitted %s present in body", key)
							}
						}
					}
					if req.Generation.ReasoningEffort != tc.effort {
						t.Fatal("caller effort mutated")
					}
				})
			}
		}
	}
}
