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

// DefaultReasoningTokenMultiplier defaults to 3. Reasoning and visible output
// share the resulting budget; no portion is reserved for either. Explicit 1
// keeps the configured budget exact.
//
// The multiplier is configurable via ai.WithReasoningTokenMultiplier() for single clients
// or ai.WithChainReasoningTokenMultiplier() for chain clients.
//
// Example: an explicit multiplier of 5 turns 2000 into a 10000-token shared budget.
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
// Use DefaultReasoningTokenMultiplier (3) if no custom value is needed.
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
