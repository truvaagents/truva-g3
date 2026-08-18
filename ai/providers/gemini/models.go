package gemini

import (
	"os"
	"strings"
)

// Content represents a content block returned by Gemini.
type Content struct {
	Role  string `json:"role"` // "user" or "model"
	Parts []Part `json:"parts"`
}

// Part represents a text part returned by Gemini.
type Part struct {
	Text    string `json:"text"`
	Thought bool   `json:"thought,omitempty"`
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
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
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
// Updated August 2026 with Gemini 2.5 and 3 family models.
//
// Source: https://ai.google.dev/gemini-api/docs/models/gemini
//
// The full dated GenerateContent coverage inventory lives in capabilities.go;
// aliases are a smaller, independent set of portable product choices.
var modelAliases = map[string]string{
	"default": "gemini-2.5-flash",       // Best price-performance for general use
	"fast":    "gemini-3.5-flash-lite",  // Current latency/cost-oriented replacement
	"smart":   "gemini-3.1-pro-preview", // Current advanced reasoning model
	"premium": "gemini-3.1-pro-preview", // Highest-tier current catalog choice
	"code":    "gemini-3.1-pro-preview", // Current advanced reasoning model
	"vision":  "gemini-2.5-flash",       // Good vision + speed balance
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
