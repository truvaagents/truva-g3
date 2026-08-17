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
// Updated August 2026 with Claude 5 family models.
//
// Source: https://platform.claude.com/docs/en/about-claude/models
//
// Available models:
//   - claude-fable-5: Most capable widely released model for demanding agentic work (1M context)
//   - claude-opus-5: Complex agentic coding and enterprise work (1M context)
//   - claude-sonnet-5: Best balance of intelligence and speed (1M context)
//   - claude-haiku-4-5: Convenience alias for the current Haiku 4.5 snapshot
var modelAliases = map[string]string{
	"default": "claude-sonnet-5",  // Sonnet 5: best balance of intelligence and speed
	"fast":    "claude-haiku-4-5", // Haiku 4.5: provider-maintained convenience alias
	"smart":   "claude-opus-5",    // Opus 5: complex analysis and agentic work
	"premium": "claude-fable-5",   // Fable 5: most capable widely released model
	"code":    "claude-opus-5",    // Opus 5: complex agentic coding
	"vision":  "claude-opus-5",    // Opus 5: strongest vision in this catalog
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
