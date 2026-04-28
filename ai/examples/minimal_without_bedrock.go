//go:build ignore
// +build ignore

// Package main demonstrates selective provider imports WITHOUT AWS Bedrock.
//
// This example shows:
// - How to include multiple providers except cloud SDK providers
// - Binary size optimization by excluding heavy dependencies
// - Auto-detection works with available providers
//
// Key Concept: AWS Bedrock requires build tags (-tags bedrock) because it
// pulls in the AWS SDK v2, adding ~2.7MB to the binary. By not importing
// Bedrock, you get a lighter binary (~5.5MB vs ~8.2MB).
//
// Run with: go run minimal_without_bedrock.go
// Requires: One of OPENAI_API_KEY, ANTHROPIC_API_KEY, or GEMINI_API_KEY
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"

	// Import all lightweight providers (each adds minimal size to binary).
	// These providers use standard HTTP clients, no heavy cloud SDKs.
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic" // ~50KB, native Messages API
	_ "github.com/truvaagents/truva-g3/ai/providers/gemini"    // ~50KB, native GenerateContent API
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"    // ~50KB, universal OpenAI-compatible
	// NOT imported: Bedrock (requires -tags bedrock, adds AWS SDK ~2.7MB)
	// To include Bedrock, you would:
	// 1. Add: _ "github.com/truvaagents/truva-g3/ai/providers/bedrock"
	// 2. Build with: go build -tags bedrock
)

func main() {
	// ListProviders() shows which providers are available.
	// Without Bedrock import, you'll see: [anthropic, gemini, openai]
	// This confirms our selective import worked.
	fmt.Printf("Providers: %v\n", ai.ListProviders())

	// NewClient() without arguments uses auto-detection.
	// It checks each registered provider's DetectEnvironment() method.
	// The provider with highest priority AND available credentials wins.
	//
	// Priority order (highest first):
	// - OpenAI: 100 (if OPENAI_API_KEY is set)
	// - Anthropic: 80 (if ANTHROPIC_API_KEY is set)
	// - Gemini: 70 (if GEMINI_API_KEY or GOOGLE_API_KEY is set)
	client, err := ai.NewClient()
	if err != nil {
		// This means none of the providers detected valid credentials.
		// The error message lists which environment variables to set.
		log.Printf("Error: %v", err)
		return
	}

	// If client creation succeeded, we can make requests.
	// The client automatically uses the detected provider.
	if client != nil {
		response, err := client.GenerateResponse(
			context.Background(),
			"test",
			&core.AIOptions{
				MaxTokens: 10, // Keep response short for demo
			},
		)
		if err != nil {
			log.Printf("Generation error: %v", err)
		} else {
			fmt.Printf("Response: %s\n", response.Content)
		}
	}
}
