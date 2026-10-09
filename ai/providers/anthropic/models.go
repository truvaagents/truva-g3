package anthropic

import (
	"os"
	"strings"
)

// AnthropicRequest represents the native Anthropic Messages API request
type AnthropicRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float32   `json:"temperature,omitempty"`
	System      string    `json:"system,omitempty"`
	TopP        float32   `json:"top_p,omitempty"`
	TopK        int       `json:"top_k,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// Message represents a message in the conversation
type Message struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// AnthropicResponse represents the response from Anthropic API
type AnthropicResponse struct {
	ID           string        `json:"id"`
	Type         string        `json:"type"`
	Role         string        `json:"role"`
	Content      []ContentItem `json:"content"`
	Model        string        `json:"model"`
	StopReason   string        `json:"stop_reason"`
	StopSequence *string       `json:"stop_sequence"`
	Usage        Usage         `json:"usage"`
}

// ContentItem represents a content block in the response
type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Usage represents token usage information
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ErrorResponse represents an error from Anthropic API
type ErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// StreamEvent represents a streaming event from Anthropic API
type StreamEvent struct {
	Type         string              `json:"type"`
	Message      *StreamMessage      `json:"message,omitempty"`
	Index        int                 `json:"index,omitempty"`
	ContentBlock *StreamContentBlock `json:"content_block,omitempty"`
	Delta        *StreamDelta        `json:"delta,omitempty"`
	Usage        *StreamUsage        `json:"usage,omitempty"`
}

// StreamMessage contains message metadata in message_start event
type StreamMessage struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Role         string `json:"role"`
	Model        string `json:"model"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
	Usage        *Usage `json:"usage,omitempty"`
}

// StreamContentBlock contains content block info
type StreamContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// StreamDelta contains the incremental content
type StreamDelta struct {
	Type       string `json:"type,omitempty"`
	Text       string `json:"text,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

// StreamUsage contains token usage in streaming responses
type StreamUsage struct {
	OutputTokens int `json:"output_tokens"`
}

// modelAliases maps portable names to Anthropic model IDs.
// These aliases enable portable model names across providers when using Chain Client.
// Updated October 2026: Haiku 5.5 and Opus 5.5 lower standard token prices.
//
// Sources: https://platform.claude.com/docs/en/models/overview
// and https://platform.claude.com/docs/en/about-claude/pricing
//
// Sonnet 5 and Fable 5 remain selected: their successors lower cache-read
// prices, but not standard input/output prices. Explicit model IDs and
// environment overrides let applications opt into those migrations separately.
var modelAliases = map[string]string{
	"default": "claude-sonnet-5",  // Balanced general-purpose tier
	"fast":    "claude-haiku-5-5", // Lower-cost fast tier
	"smart":   "claude-opus-5-5",  // Complex analysis and agentic work
	"premium": "claude-fable-5",   // Premium tier
	"code":    "claude-opus-5-5",  // Complex agentic coding
	"vision":  "claude-opus-5-5",  // Image understanding
}

// resolveModel returns the actual model name for an alias.
// Priority: 1) Env var override, 2) Hardcoded alias, 3) Pass-through
func resolveModel(model string) string {
	// Check for environment variable override: TRUVAG3_ANTHROPIC_MODEL_{ALIAS}
	envKey := "TRUVAG3_ANTHROPIC_MODEL_" + strings.ToUpper(model)
	if override := os.Getenv(envKey); override != "" {
		return override
	}

	// Check hardcoded aliases
	if actual, exists := modelAliases[model]; exists {
		return actual
	}

	return model
}
