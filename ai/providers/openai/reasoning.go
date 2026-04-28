package openai

import "strings"

// reasoningModelPrefixes defines model prefixes that indicate OpenAI reasoning models.
// These models require special parameter handling:
// - Use max_completion_tokens instead of max_tokens
// - Temperature must be omitted (or set to 1)
//
// Reference: https://platform.openai.com/docs/guides/reasoning
var reasoningModelPrefixes = []string{
	"gpt-5", // GPT-5 family (gpt-5, gpt-5-mini, gpt-5-turbo, etc.)
	"o1",    // o1 family (o1, o1-mini, o1-preview, etc.)
	"o3",    // o3 family (o3, o3-mini, etc.)
	"o4",    // o4 family (o4-mini, etc.)
}

// IsReasoningModel returns true if the given model is an OpenAI reasoning model
// that requires special parameter handling (max_completion_tokens instead of max_tokens).
// When reasoning effort is "none", these models accept temperature; otherwise temperature
// must be omitted.
//
// The check is case-insensitive and uses prefix matching to support future
// model variants within each family.
func IsReasoningModel(model string) bool {
	modelLower := strings.ToLower(model)
	for _, prefix := range reasoningModelPrefixes {
		if strings.HasPrefix(modelLower, prefix) {
			return true
		}
	}
	return false
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
const DefaultReasoningTokenMultiplier = 5

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
//   - When reasoningEffort is empty: multiplier applied, temperature omitted, no reasoning object sent
//   - If reasoningEffort is set, a "reasoning": {"effort": value} object is included in the body
//
// The reasoningTokenMultiplier parameter allows callers to configure the multiplier.
// Use DefaultReasoningTokenMultiplier (5) if no custom value is needed.
func buildRequestBody(model string, messages []map[string]string, maxTokens int, temperature float32, streaming bool, reasoningTokenMultiplier int, reasoningEffort string) map[string]interface{} {
	reqBody := map[string]interface{}{
		"model":    model,
		"messages": messages,
	}

	// Use default multiplier if not specified or invalid
	if reasoningTokenMultiplier <= 0 {
		reasoningTokenMultiplier = DefaultReasoningTokenMultiplier
	}

	if IsReasoningModel(model) {
		// Reasoning models always require max_completion_tokens (not max_tokens),
		// regardless of reasoning effort level.
		if reasoningEffort == "none" {
			// effort="none" means no extended thinking — no token multiplier needed,
			// and temperature is allowed since the model behaves like a standard model.
			reqBody["max_completion_tokens"] = maxTokens
			reqBody["temperature"] = temperature
		} else {
			// Active reasoning: apply multiplier because reasoning models count internal
			// chain-of-thought tokens against max_completion_tokens, but those tokens
			// are NOT returned. Without this, complex prompts exhaust tokens on reasoning
			// with nothing left for output.
			reqBody["max_completion_tokens"] = maxTokens * reasoningTokenMultiplier
			// Temperature is intentionally omitted for active reasoning models
		}

		// Include reasoning effort in request if explicitly set
		if reasoningEffort != "" {
			reqBody["reasoning"] = map[string]interface{}{
				"effort": reasoningEffort,
			}
		}
	} else {
		// Standard models use max_tokens and accept temperature
		reqBody["max_tokens"] = maxTokens
		reqBody["temperature"] = temperature
	}

	if streaming {
		reqBody["stream"] = true
		reqBody["stream_options"] = map[string]interface{}{
			"include_usage": true, // Request usage info in final chunk
		}
	}

	return reqBody
}
