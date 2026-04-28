//go:build bedrock
// +build bedrock

package bedrock

// ConverseRequest represents the request structure for AWS Bedrock Converse API
// This is a simplified version - AWS SDK provides the full types
type ConverseRequest struct {
	ModelId         string          `json:"modelId"`
	Messages        []Message       `json:"messages"`
	System          []SystemMessage `json:"system,omitempty"`
	InferenceConfig InferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *ToolConfig     `json:"toolConfig,omitempty"`
}

// Message represents a message in the conversation
type Message struct {
	Role    string         `json:"role"` // "user" or "assistant"
	Content []ContentBlock `json:"content"`
}

// SystemMessage represents system instructions
type SystemMessage struct {
	Text string `json:"text"`
}

// ContentBlock represents a block of content in a message
type ContentBlock struct {
	Text string `json:"text,omitempty"`
	// Could also have Image, Document, etc. for multimodal
}

// InferenceConfig contains inference parameters
type InferenceConfig struct {
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Temperature   float32  `json:"temperature,omitempty"`
	TopP          float32  `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

// ToolConfig represents tool/function calling configuration
type ToolConfig struct {
	Tools []Tool `json:"tools,omitempty"`
}

// Tool represents a tool that can be called
type Tool struct {
	ToolSpec ToolSpec `json:"toolSpec"`
}

// ToolSpec defines the specification of a tool
type ToolSpec struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema defines the input schema for a tool
type InputSchema struct {
	JSON map[string]interface{} `json:"json"`
}

// ConverseResponse represents the response from Converse API
type ConverseResponse struct {
	Output     Output  `json:"output"`
	StopReason string  `json:"stopReason"`
	Usage      Usage   `json:"usage"`
	Metrics    Metrics `json:"metrics"`
}

// Output contains the model's response
type Output struct {
	Message Message `json:"message"`
}

// Usage contains token usage information
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

// Metrics contains performance metrics
type Metrics struct {
	LatencyMs int `json:"latencyMs"`
}

// ErrorResponse represents an error from AWS Bedrock
type ErrorResponse struct {
	Message string `json:"message"`
	Code    string `json:"__type"`
}

// Common AWS Bedrock model identifiers
const (
	// Anthropic Claude models
	ModelClaude3Opus   = "anthropic.claude-3-opus-20240229-v1:0"
	ModelClaude3Sonnet = "anthropic.claude-3-sonnet-20240229-v1:0"
	ModelClaude3Haiku  = "anthropic.claude-3-haiku-20240307-v1:0"
	ModelClaudeInstant = "anthropic.claude-instant-v1"

	// Amazon Titan models
	ModelTitanTextPremier = "amazon.titan-text-premier-v1:0"
	ModelTitanTextExpress = "amazon.titan-text-express-v1"
	ModelTitanTextLite    = "amazon.titan-text-lite-v1"
	ModelTitanEmbed       = "amazon.titan-embed-text-v1"

	// Meta Llama models
	ModelLlama3_70B = "meta.llama3-70b-instruct-v1:0"
	ModelLlama3_8B  = "meta.llama3-8b-instruct-v1:0"
	ModelLlama2_70B = "meta.llama2-70b-chat-v1"
	ModelLlama2_13B = "meta.llama2-13b-chat-v1"

	// Mistral models
	ModelMistral7B   = "mistral.mistral-7b-instruct-v0:2"
	ModelMixtral8x7B = "mistral.mixtral-8x7b-instruct-v0:1"

	// Cohere models
	ModelCohereCommand = "cohere.command-text-v14"
	ModelCohereEmbed   = "cohere.embed-english-v3"
)
