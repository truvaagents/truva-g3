package gemini

import (
	"os"
	"strings"
)

// GeminiRequest represents the native Gemini GenerateContent API request
type GeminiRequest struct {
	Contents          []Content          `json:"contents"`
	GenerationConfig  *GenerationConfig  `json:"generationConfig,omitempty"`
	SafetySettings    []SafetySetting    `json:"safetySettings,omitempty"`
	SystemInstruction *SystemInstruction `json:"systemInstruction,omitempty"`
}

// Content represents a content block in the request
type Content struct {
	Role  string `json:"role"` // "user" or "model"
	Parts []Part `json:"parts"`
}

// Part represents a part of content
type Part struct {
	Text string `json:"text"`
}

// SystemInstruction represents system instructions
type SystemInstruction struct {
	Parts []Part `json:"parts"`
}

// GenerationConfig represents generation configuration
type GenerationConfig struct {
	Temperature      float32  `json:"temperature,omitempty"`
	TopP             float32  `json:"topP,omitempty"`
	TopK             int      `json:"topK,omitempty"`
	MaxOutputTokens  int      `json:"maxOutputTokens,omitempty"`
	ResponseMimeType string   `json:"responseMimeType,omitempty"`
	StopSequences    []string `json:"stopSequences,omitempty"`
}

// SafetySetting represents safety configuration
type SafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// GeminiResponse represents the response from Gemini API
type GeminiResponse struct {
	Candidates    []Candidate   `json:"candidates"`
	UsageMetadata UsageMetadata `json:"usageMetadata"`
	ModelVersion  string        `json:"modelVersion"`
}

// Candidate represents a response candidate
type Candidate struct {
	Content       Content        `json:"content"`
	FinishReason  string         `json:"finishReason"`
	Index         int            `json:"index"`
	SafetyRatings []SafetyRating `json:"safetyRatings"`
}

// SafetyRating represents safety rating information
type SafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
	Blocked     bool   `json:"blocked"`
}

// UsageMetadata represents token usage information
type UsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// ErrorResponse represents an error from Gemini API
type ErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// StreamChunk represents a streaming response chunk from Gemini API
// Gemini streams complete GeminiResponse objects for each chunk
type StreamChunk struct {
	Candidates    []StreamCandidate `json:"candidates,omitempty"`
	UsageMetadata *UsageMetadata    `json:"usageMetadata,omitempty"`
}

// StreamCandidate represents a candidate in streaming response
type StreamCandidate struct {
	Content      Content `json:"content"`
	FinishReason string  `json:"finishReason,omitempty"`
	Index        int     `json:"index"`
}

// modelAliases maps portable names to Gemini model IDs.
// These aliases enable portable model names across providers when using Chain Client.
// Updated December 2025 with Gemini 2.5 and 3 family models.
//
// Source: https://ai.google.dev/gemini-api/docs/models/gemini
//
// Available models:
//   - gemini-3-pro-preview: Best multimodal understanding (1M input, 65K output)
//   - gemini-2.5-pro: State-of-the-art thinking model for complex reasoning
//   - gemini-2.5-flash: Best price-performance, optimized for scale
//   - gemini-2.5-flash-lite: Fastest flash model, cost-efficient
//   - gemini-2.0-flash: Previous generation workhorse (1M context)
var modelAliases = map[string]string{
	"default": "gemini-2.5-flash",      // Best price-performance for general use
	"fast":    "gemini-2.5-flash-lite", // Fastest, most cost-efficient
	"smart":   "gemini-2.5-pro",        // State-of-the-art reasoning
	"premium": "gemini-3-pro-preview",  // Best multimodal understanding
	"code":    "gemini-2.5-pro",        // Excellent for coding tasks
	"vision":  "gemini-2.5-flash",      // Good vision + speed balance
}

// resolveModel returns the actual model name for an alias.
// Priority: 1) Env var override, 2) Hardcoded alias, 3) Pass-through
func resolveModel(model string) string {
	// Check for environment variable override: TRUVAG3_GEMINI_MODEL_{ALIAS}
	envKey := "TRUVAG3_GEMINI_MODEL_" + strings.ToUpper(model)
	if override := os.Getenv(envKey); override != "" {
		return override
	}

	// Check hardcoded aliases
	if actual, exists := modelAliases[model]; exists {
		return actual
	}

	return model
}
