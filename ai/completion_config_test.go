package ai

import "testing"

func TestCompletionCompatibilityOptionsAreExplicitAndIndependent(t *testing.T) {
	single := &AIConfig{MaxTokens: 64, MaxRetries: 0}
	chain := &ChainConfig{ReasoningEffort: "none", MaxRetries: 0}
	for _, enabled := range []bool{true, false} {
		WithReasoningContentFallback(enabled)(single)
		WithChainReasoningContentFallback(enabled)(chain)
		if single.ReasoningContentFallback != enabled || chain.ReasoningContentFallback != enabled || single.MaxTokens != 64 || single.MaxRetries != 0 || chain.MaxRetries != 0 || chain.ReasoningEffort != "none" {
			t.Fatalf("compatibility option changed unrelated request policy: single=%+v chain=%+v", single, chain)
		}
	}
}
