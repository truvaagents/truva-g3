package openai

import (
	"os"
	"strings"
)

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
//   - Together: https://docs.together.ai/docs/chat-models
//   - xAI: https://docs.x.ai/docs/models
//   - Mistral: https://docs.mistral.ai/getting-started/models/models_overview/
//   - Qwen: https://www.alibabacloud.com/help/en/model-studio/models
//   - Ollama: https://ollama.com/library
var ModelAliases = map[string]map[string]string{
	// OpenAI - GPT-4.1 and O-series (December 2025)
	// GPT-4.1 replaced GPT-4o in API with 1M context window
	// O-series provides advanced reasoning capabilities
	"openai": {
		"fast":    "gpt-4.1-mini", // GPT-4.1 Mini: fast, affordable, 1M context
		"smart":   "o3",           // O3: best reasoning model
		"vision":  "gpt-4.1",      // GPT-4.1: vision + 1M context
		"code":    "o3",           // O3: excellent at coding tasks
		"default": "gpt-4.1-mini",
	},
	// DeepSeek - V3.2 family (December 2025)
	// Two main models: chat (general) and reasoner (thinking mode)
	"openai.deepseek": {
		"fast":    "deepseek-chat",     // V3.2 default chat, 128K context
		"smart":   "deepseek-reasoner", // V3.2 thinking mode, 128K context
		"code":    "deepseek-chat",     // V3.2 has strong coding capabilities
		"default": "deepseek-chat",
	},
	// Groq - Ultra-fast inference (December 2025)
	// Known for extremely fast token generation speeds
	"openai.groq": {
		"fast":    "llama-3.1-8b-instant",    // 560 T/sec, fastest
		"smart":   "llama-3.3-70b-versatile", // 280 T/sec, best quality
		"code":    "llama-3.3-70b-versatile", // Best for coding on Groq
		"default": "llama-3.3-70b-versatile",
	},
	// Together AI - Open source models (December 2025)
	// Hosts popular open models with Turbo optimizations
	"openai.together": {
		"fast":    "meta-llama/Llama-3.1-8B-Instruct-Turbo",  // Fast inference
		"smart":   "meta-llama/Llama-3.3-70B-Instruct-Turbo", // Best open model
		"code":    "Qwen/Qwen2.5-Coder-32B-Instruct",         // Specialized coder
		"default": "meta-llama/Llama-3.3-70B-Instruct-Turbo",
	},
	// xAI Grok - Grok 3 and 4 family (December 2025)
	// Note: Model IDs use simple names without x-ai/ prefix for direct API use
	"openai.xai": {
		"fast":    "grok-2",           // Grok 2: fast, 131K context
		"smart":   "grok-3-beta",      // Grok 3: best reasoning, 131K context
		"code":    "grok-3-mini-beta", // Grok 3 Mini: fast reasoning for code
		"vision":  "grok-2-vision-latest",
		"default": "grok-3-beta",
	},
	// Mistral AI - Mistral models (February 2026)
	// Small/Medium/Large generalist models + Codestral for code
	"openai.mistral": {
		"fast":    "mistral-small-latest",  // Small: efficient, fast inference
		"smart":   "mistral-large-latest",  // Large: most capable generalist
		"code":    "codestral-latest",      // Codestral: specialized for code
		"default": "mistral-medium-latest", // Medium 3.1: balanced speed and quality
	},
	// Qwen - Alibaba Cloud DashScope (December 2025)
	// Access via OpenAI-compatible endpoint at dashscope-intl.aliyuncs.com
	"openai.qwen": {
		"fast":    "qwen-turbo",       // Fast, cost-efficient, 1M context
		"smart":   "qwen-max",         // Qwen2.5-Max, most capable
		"code":    "qwen3-coder-plus", // Specialized for coding
		"default": "qwen-plus",        // Good balance of speed and quality
	},
	// Ollama - Local models via OpenAI-compatible API (December 2025)
	// Note: Model availability depends on what user has pulled locally
	// Users can override defaults via TRUVAG3_OLLAMA_MODEL_DEFAULT environment variable
	"openai.ollama": {
		"fast":    "llama3.2:1b", // Smallest/fastest variant
		"smart":   "llama3.2",    // Default 3B model, good balance
		"code":    "codellama",   // Code-specialized model
		"default": "llama3.2",    // Most commonly pulled model
	},
}

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
//	ResolveModel("openai.groq", "fast") → "llama-3.3-70b-versatile"
//	ResolveModel("openai", "gpt-4") → "gpt-4" (pass-through, not an alias)
func ResolveModel(providerAlias string, model string) string {
	// If no alias is set, use vanilla OpenAI
	if providerAlias == "" {
		providerAlias = "openai"
	}

	// Check for environment variable override: TRUVAG3_{PROVIDER}_MODEL_{ALIAS}
	// Normalize provider alias: "openai.deepseek" -> "DEEPSEEK", "openai" -> "OPENAI"
	envProvider := providerAlias
	if strings.HasPrefix(providerAlias, "openai.") {
		envProvider = strings.TrimPrefix(providerAlias, "openai.")
	}
	envKey := "TRUVAG3_" + strings.ToUpper(envProvider) + "_MODEL_" + strings.ToUpper(model)
	if override := os.Getenv(envKey); override != "" {
		return override
	}

	// Check hardcoded aliases
	if aliases, exists := ModelAliases[providerAlias]; exists {
		if actualModel, exists := aliases[model]; exists {
			return actualModel
		}
	}

	// Not an alias, return as-is (pass-through for explicit model names)
	return model
}
