package openai

import "github.com/truvaagents/truva-g3/ai/providers/openai/modelcatalog"

// OpenAIResponse represents the response from OpenAI API
type OpenAIResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a response choice
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Message represents a chat message
// For reasoning models (GPT-5, o1, o3, o4), content may be in ReasoningContent field
type Message struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // GPT-5/o-series reasoning models
}

// Usage represents token usage information
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ErrorResponse represents an error from OpenAI API
type ErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// StreamChoice represents a choice in a streaming response
type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

// StreamDelta represents the delta content in a streaming chunk
// For reasoning models (GPT-5, o1, o3, o4), content may be in ReasoningContent field
type StreamDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // GPT-5/o-series reasoning models
}

// StreamResponse represents a streaming response chunk from OpenAI API
type StreamResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"` // Only present in final chunk with stream_options
}

// ModelAliases maps common model aliases to provider-specific model names.
// This enables portable model names across different OpenAI-compatible providers.
// Updated December 2025 with latest model offerings from official documentation.
//
// Example usage:
//
//	client, _ := ai.NewClient(
//	    ai.WithProviderAlias("openai.deepseek"),
//	    ai.WithModel("smart"),  // Resolves to "deepseek-reasoner"
//	)
//
// Sources:
//   - OpenAI: https://platform.openai.com/docs/models
//   - DeepSeek: https://api-docs.deepseek.com/quick_start/pricing
//   - Groq: https://console.groq.com/docs/models
//   - Together: https://docs.together.ai/docs/inference/recommended-models
//   - xAI: https://docs.x.ai/docs/models
//   - Mistral: https://docs.mistral.ai/getting-started/models/models_overview/
//   - Qwen: https://www.alibabacloud.com/help/en/model-studio/models
//   - Ollama: https://ollama.com/library
var ModelAliases = modelcatalog.DefaultAliases()

// ResolveModel resolves a model alias to the actual model name (Phase 2)
// This function enables portable model names across providers.
//
// Parameters:
//   - providerAlias: The provider alias (e.g., "openai.deepseek")
//   - model: The model name or alias (e.g., "smart" or "gpt-4")
//
// Returns:
//   - The actual model name to use with the provider
//
// Priority:
//  1. Environment variable override (highest) - TRUVAG3_{PROVIDER}_MODEL_{ALIAS}
//  2. Hardcoded alias mapping
//  3. Pass-through (lowest) - Use model name as-is
//
// Example:
//
//	ResolveModel("openai.deepseek", "smart") → "deepseek-reasoner"
//	ResolveModel("openai.groq", "default") → "openai/gpt-oss-120b"
//	ResolveModel("openai", "gpt-4") → "gpt-4" (pass-through, not an alias)
func ResolveModel(providerAlias string, model string) string {
	return modelcatalog.ResolveWithAliases(ModelAliases, providerAlias, model)
}
