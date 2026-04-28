//go:build ignore
// +build ignore

// Package main is a comprehensive demonstration of the multi-provider AI system.
//
// This example shows:
// - Provider registration and discovery via the registry
// - Auto-detection from environment variables
// - Universal OpenAI provider for 20+ compatible services
// - Native Anthropic and Gemini providers
// - Provider aliases for OpenAI-compatible services (Phase 2)
// - Various configuration options
//
// The truvag3 AI module follows these design principles:
// 1. Zero-config defaults - works with just environment variables
// 2. Progressive disclosure - simple API for common cases, advanced options available
// 3. Provider-agnostic core - same interface for all providers
// 4. Open for extension - add custom providers without framework changes
//
// Run with: go run multi_provider_demo.go
// Requires: At least one provider's API key set in environment
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"

	// Import all providers - each self-registers via init().
	// The blank import (_) triggers the provider's init() function,
	// which calls ai.MustRegister() to add itself to the global registry.
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic" // Native Claude API
	_ "github.com/truvaagents/truva-g3/ai/providers/gemini"    // Native Gemini API
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"    // Universal OpenAI-compatible
	// Import AWS Bedrock provider (only compiled with -tags bedrock)
	// Bedrock uses the AWS SDK v2 which adds ~2.7MB to binary size.
	// Build with: go build -tags bedrock
	// _ "github.com/truvaagents/truva-g3/ai/providers/bedrock"
)

func main() {
	// =========================================================================
	// SECTION 1: Provider Discovery
	// Shows how to inspect registered providers at runtime
	// =========================================================================

	fmt.Println("=== Registered Providers ===")
	// ListProviders() returns sorted list of all registered provider names.
	// Useful for debugging and runtime inspection.
	providers := ai.ListProviders()
	for _, p := range providers {
		fmt.Printf("  - %s\n", p)
	}
	fmt.Println()

	// =========================================================================
	// SECTION 2: Provider Information
	// Shows detailed info including availability and priority
	// =========================================================================

	fmt.Println("=== Provider Information ===")
	// GetProviderInfo() returns detailed information about each provider:
	// - Name: Provider identifier
	// - Description: Human-readable description
	// - Available: Whether credentials are detected in environment
	// - Priority: Auto-detection priority (higher = preferred)
	info := ai.GetProviderInfo()
	for _, p := range info {
		status := "❌ unavailable"
		if p.Available {
			status = "✅ available"
		}
		fmt.Printf("  %s: %s\n    Status: %s (priority: %d)\n",
			p.Name, p.Description, status, p.Priority)
	}
	fmt.Println()

	// =========================================================================
	// SECTION 3: Auto-Detection
	// Shows how the framework automatically selects the best provider
	// =========================================================================

	fmt.Println("=== Auto-Detection ===")
	// NewClient() without options uses auto-detection:
	// 1. Scans all registered providers
	// 2. Each provider checks its environment (API keys, local services)
	// 3. Selects provider with highest priority that's available
	// 4. Creates and returns the client
	client, err := ai.NewClient()
	if err != nil {
		// Auto-detection failed - no providers have valid credentials
		fmt.Printf("Auto-detection failed: %v\n", err)
		fmt.Println("Please set one of: OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY")
		fmt.Println("Or ensure Ollama is running locally")
	} else {
		fmt.Println("Successfully created client with auto-detected provider")
		testClient(client, "auto-detected")
	}
	fmt.Println()

	// =========================================================================
	// SECTION 4: Testing Different Provider Types
	// Demonstrates various ways to configure and use providers
	// =========================================================================

	fmt.Println("=== Testing Different Provider Types ===")

	// -------------------------------------------------------------------------
	// A. Universal OpenAI Provider
	// This single provider works with 20+ OpenAI-compatible services
	// -------------------------------------------------------------------------
	fmt.Println("\n--- Universal OpenAI Provider ---")

	// OpenAI (original) - The native OpenAI service
	// Uses OPENAI_API_KEY from environment automatically
	if os.Getenv("OPENAI_API_KEY") != "" {
		client, err := ai.NewClient(ai.WithProvider("openai"))
		if err != nil {
			log.Printf("Failed to create OpenAI client: %v", err)
		} else {
			testClient(client, "OpenAI")
		}
	}

	// Groq (OpenAI-compatible) - Fast inference service
	// Same provider, different base URL and API key
	if os.Getenv("GROQ_API_KEY") != "" {
		// WithProvider("openai") uses the universal OpenAI provider
		// WithBaseURL() points it to Groq's API endpoint
		// WithAPIKey() provides Groq's API key
		client, err := ai.NewClient(
			ai.WithProvider("openai"),
			ai.WithBaseURL("https://api.groq.com/openai/v1"),
			ai.WithAPIKey(os.Getenv("GROQ_API_KEY")),
		)
		if err != nil {
			log.Printf("Failed to create Groq client: %v", err)
		} else {
			testClient(client, "Groq (via universal OpenAI provider)")
		}
	}

	// Ollama (local, OpenAI-compatible) - Local model inference
	// No API key needed for local services
	fmt.Println("\n  Testing Ollama (local)...")
	client, err = ai.NewClient(
		ai.WithProvider("openai"),
		ai.WithBaseURL("http://localhost:11434/v1"), // Ollama's OpenAI-compatible endpoint
		ai.WithModel("llama3.2"),                    // Specify which model to use
		// No WithAPIKey() - Ollama doesn't require authentication
	)
	if err != nil {
		fmt.Println("  Ollama client creation failed (is Ollama running?)")
	} else {
		testClient(client, "Ollama (via universal OpenAI provider)")
	}

	// -------------------------------------------------------------------------
	// E. Provider Aliases (Phase 2 Feature)
	// Cleaner syntax for OpenAI-compatible services
	// -------------------------------------------------------------------------
	fmt.Println("\n--- Provider Aliases (Phase 2) ---")
	fmt.Println("Provider aliases auto-configure API keys and base URLs from environment")

	// DeepSeek via alias
	// WithProviderAlias("openai.deepseek") automatically:
	// 1. Sets provider to "openai"
	// 2. Reads DEEPSEEK_API_KEY from environment
	// 3. Sets base URL to https://api.deepseek.com
	if os.Getenv("DEEPSEEK_API_KEY") != "" {
		client, err := ai.NewClient(
			ai.WithProviderAlias("openai.deepseek"),
			ai.WithModel("deepseek-chat"), // DeepSeek's model name
		)
		if err != nil {
			log.Printf("Failed to create DeepSeek client: %v", err)
		} else {
			testClient(client, "DeepSeek (via provider alias)")
		}
	} else {
		fmt.Println("  DEEPSEEK_API_KEY not set - skipping DeepSeek alias test")
	}

	// Groq via alias - even simpler than manual configuration
	if os.Getenv("GROQ_API_KEY") != "" {
		client, err := ai.NewClient(
			ai.WithProviderAlias("openai.groq"),     // Auto-configures base URL and API key
			ai.WithModel("llama-3.3-70b-versatile"), // Groq's fast Llama model
		)
		if err != nil {
			log.Printf("Failed to create Groq client: %v", err)
		} else {
			testClient(client, "Groq (via provider alias)")
		}
	} else {
		fmt.Println("  GROQ_API_KEY not set - skipping Groq alias test")
	}

	// -------------------------------------------------------------------------
	// B. Native Anthropic Provider
	// Uses Anthropic's native Messages API (not OpenAI-compatible)
	// -------------------------------------------------------------------------
	fmt.Println("\n--- Native Anthropic Provider ---")
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		// The anthropic provider uses the native Messages API
		// This provides access to Anthropic-specific features:
		// - Message prefilling
		// - XML structured responses
		// - Constitutional AI settings
		client, err := ai.NewClient(ai.WithProvider("anthropic"))
		if err != nil {
			log.Printf("Failed to create Anthropic client: %v", err)
		} else {
			testClient(client, "Anthropic (native Messages API)")
		}
	} else {
		fmt.Println("  ANTHROPIC_API_KEY not set - skipping")
	}

	// -------------------------------------------------------------------------
	// C. Native Gemini Provider
	// Uses Google's native GenerateContent API
	// -------------------------------------------------------------------------
	fmt.Println("\n--- Native Gemini Provider ---")
	// Gemini accepts either GEMINI_API_KEY or GOOGLE_API_KEY
	if os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != "" {
		// The gemini provider uses the native GenerateContent API
		// This provides access to Gemini-specific features:
		// - Multimodal input (images, video)
		// - Grounding with Google Search
		// - Response schema enforcement
		client, err := ai.NewClient(ai.WithProvider("gemini"))
		if err != nil {
			log.Printf("Failed to create Gemini client: %v", err)
		} else {
			testClient(client, "Gemini (native GenerateContent API)")
		}
	} else {
		fmt.Println("  GEMINI_API_KEY/GOOGLE_API_KEY not set - skipping")
	}

	// -------------------------------------------------------------------------
	// D. AWS Bedrock Provider
	// Enterprise multi-model access (requires -tags bedrock build)
	// -------------------------------------------------------------------------
	fmt.Println("\n--- AWS Bedrock Provider ---")
	// Bedrock supports multiple credential sources:
	// - AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (explicit credentials)
	// - AWS_PROFILE (named profile from ~/.aws/credentials)
	// - AWS_EXECUTION_ENV (EC2/ECS/Lambda instance role)
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" || os.Getenv("AWS_EXECUTION_ENV") != "" {
		client, err := ai.NewClient(
			ai.WithProvider("bedrock"),
			ai.WithModel("anthropic.claude-3-sonnet-20240229-v1:0"), // Bedrock model ID format
			// Optional: ai.WithRegion("us-east-1")
		)
		if err != nil {
			log.Printf("Failed to create Bedrock client: %v", err)
		} else {
			testClient(client, "AWS Bedrock (unified access to multiple models)")
		}
	} else {
		fmt.Println("  AWS credentials not configured - skipping")
		fmt.Println("  Set AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY or configure AWS CLI")
	}

	// =========================================================================
	// SECTION 5: Configuration Reference
	// Shows all the different ways to configure providers
	// =========================================================================

	fmt.Println("\n=== Configuration Examples ===")
	fmt.Println("You can configure providers in multiple ways:")
	fmt.Println()
	fmt.Println("1. Auto-detection (checks environment variables):")
	fmt.Println("   client, _ := ai.NewClient()")
	fmt.Println()
	fmt.Println("2. Explicit provider selection:")
	fmt.Println("   client, _ := ai.NewClient(ai.WithProvider(\"anthropic\"))")
	fmt.Println()
	fmt.Println("3. Universal OpenAI provider for compatible services:")
	fmt.Println("   client, _ := ai.NewClient(")
	fmt.Println("       ai.WithProvider(\"openai\"),")
	fmt.Println("       ai.WithBaseURL(\"https://api.groq.com/openai/v1\"),")
	fmt.Println("       ai.WithAPIKey(os.Getenv(\"GROQ_API_KEY\")),")
	fmt.Println("   )")
	fmt.Println()
	fmt.Println("4. Provider Aliases (Phase 2 - cleaner configuration):")
	fmt.Println("   // Auto-configures from DEEPSEEK_API_KEY environment variable")
	fmt.Println("   client, _ := ai.NewClient(")
	fmt.Println("       ai.WithProviderAlias(\"openai.deepseek\"),")
	fmt.Println("       ai.WithModel(\"deepseek-chat\"),")
	fmt.Println("   )")
	fmt.Println()
	fmt.Println("   // Supported aliases: openai.deepseek, openai.groq, openai.xai,")
	fmt.Println("   //                    openai.qwen, openai.together, openai.ollama")
	fmt.Println()
	fmt.Println("5. AWS Bedrock for enterprise multi-model access:")
	fmt.Println("   // Build with: go build -tags bedrock")
	fmt.Println("   client, _ := ai.NewClient(")
	fmt.Println("       ai.WithProvider(\"bedrock\"),")
	fmt.Println("       ai.WithModel(\"anthropic.claude-3-sonnet-20240229-v1:0\"),")
	fmt.Println("       ai.WithRegion(\"us-east-1\"),")
	fmt.Println("       // Uses AWS credential chain (IAM role, env vars, ~/.aws/credentials)")
	fmt.Println("   )")
}

// testClient is a helper function that tests a provider with a simple prompt.
// It demonstrates the standard request/response pattern used with all providers.
func testClient(client core.AIClient, provider string) {
	fmt.Printf("\nTesting %s provider:\n", provider)

	// Create a context - in production, use context.WithTimeout() for safety
	ctx := context.Background()

	// GenerateResponse() is the core method - same interface for all providers.
	// The framework normalizes different provider APIs into this common interface.
	response, err := client.GenerateResponse(
		ctx,
		"Say 'Hello from ' followed by your model name in 10 words or less",
		&core.AIOptions{
			Temperature: 0.5, // Lower = more deterministic, Higher = more creative
			MaxTokens:   50,  // Limit response length
			// Other options:
			// Model: "gpt-4"           // Override default model
			// SystemPrompt: "..."      // Set system/instruction prompt
		},
	)

	if err != nil {
		// Errors are normalized across providers:
		// - Authentication errors
		// - Rate limit errors
		// - Model not found errors
		// - Network errors
		fmt.Printf("  Error: %v\n", err)
		return
	}

	// AIResponse contains:
	// - Content: The generated text
	// - Model: The actual model used (useful when using aliases like "gpt-4")
	// - Usage: Token counts for billing/monitoring
	fmt.Printf("  Response: %s\n", response.Content)
	if response.Model != "" {
		fmt.Printf("  Model: %s\n", response.Model)
	}
}
