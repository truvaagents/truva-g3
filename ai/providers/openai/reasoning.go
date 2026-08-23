package openai

import "github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"

// IsReasoningModel returns true if the given model is an OpenAI reasoning model
// that requires special parameter handling (max_completion_tokens instead of max_tokens).
// When reasoning effort is "none", these models accept temperature; otherwise temperature
// must be omitted.
//
// The check is case-insensitive and uses prefix matching to support future
// model variants within each family.
func IsReasoningModel(model string) bool {
	return openaiwire.IsReasoningModel(model)
}

// DefaultReasoningTokenMultiplier is the default factor by which max_tokens is increased
// for reasoning models. Reasoning models (GPT-5, o1, o3, o4) count internal chain-of-thought
// reasoning tokens against max_completion_tokens, but these tokens are NOT returned in the
// response. Without this multiplier, complex prompts may exhaust all tokens on reasoning,
// leaving nothing for the visible output (resulting in empty content).
//
// The multiplier is configurable via ai.WithReasoningTokenMultiplier() for single clients
// or ai.WithChainReasoningTokenMultiplier() for chain clients.
//
// Example: If caller requests 2000 tokens, reasoning models get 2000 * 5 = 10000,
// ensuring ~4000 for internal reasoning + ~6000 for visible output.
const DefaultReasoningTokenMultiplier = openaiwire.DefaultReasoningTokenMultiplier

// buildRequestBody constructs the request body for OpenAI chat completions API.
// It handles the differences between standard models and reasoning models:
//
// Standard models (gpt-4, gpt-4o, etc.):
//   - Uses max_tokens parameter
//   - Includes temperature parameter
//
// Reasoning models (gpt-5, o1, o3, o4):
//   - Always uses max_completion_tokens (max_tokens is rejected by the API)
//   - When reasoningEffort is "none": no token multiplier, temperature included
//   - When reasoningEffort is non-empty and not "none": multiplier applied, temperature omitted
//   - When reasoningEffort is empty: no multiplier, temperature omitted, no reasoning object sent
//   - If reasoningEffort is set, a "reasoning": {"effort": value} object is included in the body
//
// The reasoningTokenMultiplier parameter allows callers to configure the multiplier.
// Use DefaultReasoningTokenMultiplier (5) if no custom value is needed.
func buildRequestBody(model string, messages []map[string]string, maxTokens int, temperature float32, streaming bool, reasoningTokenMultiplier int, reasoningEffort string) map[string]interface{} {
	return openaiwire.BuildRequestBody(
		model,
		messages,
		maxTokens,
		temperature,
		streaming,
		reasoningTokenMultiplier,
		reasoningEffort,
	)
}
