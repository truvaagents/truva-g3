//go:build ignore
// +build ignore

// Package main demonstrates what happens when NO providers are imported.
//
// This example shows:
// - The registry is empty without provider imports
// - Client creation fails gracefully with a helpful error
// - How the provider registration system works
//
// Key Concept: Providers self-register via init() functions.
// Without importing any provider package, the registry remains empty.
// This demonstrates the framework's fail-safe behavior.
//
// Run with: go run minimal_no_providers.go
// Expected: Error message indicating no providers are available
package main

import (
	"fmt"

	"github.com/truvaagents/truva-g3/ai"
	// NOTE: No provider imports here!
	// Without these imports, no providers are registered:
	// _ "github.com/truvaagents/truva-g3/ai/providers/openai"
	// _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
	// _ "github.com/truvaagents/truva-g3/ai/providers/gemini"
)

func main() {
	// ListProviders() returns all registered provider names.
	// Since we didn't import any providers, this will be empty.
	// This is useful for debugging: "Why isn't my provider working?"
	providers := ai.ListProviders()
	fmt.Printf("Registered providers: %v\n", providers)
	// Output: Registered providers: []

	// NewClient() with no arguments uses auto-detection.
	// Auto-detection scans all registered providers for available credentials.
	// With no providers registered, this will fail with a clear error.
	_, err := ai.NewClient()
	if err != nil {
		// Expected error: "no AI provider available: no provider detected in environment"
		// The error message guides users to:
		// 1. Import a provider package
		// 2. Set the required environment variables
		fmt.Printf("Expected error: %v\n", err)
	}

	// Lesson: Always import at least one provider!
	// Example fix:
	//   import _ "github.com/truvaagents/truva-g3/ai/providers/openai"
}
