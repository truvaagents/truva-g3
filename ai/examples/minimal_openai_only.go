//go:build ignore
// +build ignore

// Package main demonstrates the minimal setup for using a single AI provider.
//
// This example shows:
// - How to import only the OpenAI provider (reduces binary size)
// - Basic client creation with explicit provider selection
// - Simple request/response flow
//
// Key Concept: Provider imports use Go's init() pattern for self-registration.
// By importing only the providers you need, you keep binary size minimal.
//
// Run with: go run minimal_openai_only.go
// Requires: OPENAI_API_KEY environment variable
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"

	// Import ONLY the OpenAI provider.
	// The blank identifier (_) means we import for side effects only.
	// The provider's init() function registers it with the global registry.
	// This pattern allows selective provider inclusion - import only what you need.
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
	// Create an AI client with explicit provider selection.
	// ai.WithProvider("openai") tells the factory to use the OpenAI provider.
	// The provider looks for OPENAI_API_KEY in the environment automatically.
	client, err := ai.NewClient(ai.WithProvider("openai"))
	if err != nil {
		// This fails if:
		// - The provider isn't imported (not registered)
		// - OPENAI_API_KEY environment variable is not set
		log.Fatal(err)
	}

	// GenerateResponse is the core method of the AIClient interface.
	// Parameters:
	// - ctx: Context for cancellation and timeouts
	// - prompt: The user's message/question
	// - options: Configuration for this specific request (can override defaults)
	response, err := client.GenerateResponse(
		context.Background(),
		"Hello",
		&core.AIOptions{
			MaxTokens: 10, // Limit response length for this demo
		},
	)

	if err != nil {
		// Errors can include: API errors, network issues, rate limits
		fmt.Printf("Error: %v\n", err)
	} else {
		// AIResponse contains:
		// - Content: The generated text
		// - Model: The model that was used
		// - Usage: Token counts (prompt, completion, total)
		fmt.Printf("Response: %s\n", response.Content)
	}
}
