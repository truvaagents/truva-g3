//go:build bedrock
// +build bedrock

// Package main demonstrates the multi-provider AI system INCLUDING AWS Bedrock.
//
// This example shows:
// - All providers including AWS Bedrock (enterprise multi-model access)
// - Bedrock configuration options (region, model selection)
// - AWS credential chain integration (IAM roles, env vars, profiles)
//
// Key Concept: AWS Bedrock provides unified access to multiple AI model families
// (Claude, Llama, Titan, Mistral, etc.) through a single AWS service with
// enterprise features like VPC endpoints, KMS encryption, and CloudWatch monitoring.
//
// Build Requirement: This file requires the "bedrock" build tag because it
// imports the Bedrock provider which depends on AWS SDK v2 (~2.7MB binary size).
//
// Build and run:
//
//	go build -tags bedrock multi_provider_demo_with_bedrock.go
//	./multi_provider_demo_with_bedrock
//
// Requires: Valid AWS credentials (any of the following):
//   - AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (explicit credentials)
//   - AWS_PROFILE (named profile from ~/.aws/credentials)
//   - EC2/ECS/Lambda instance role (automatic when running on AWS)
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"

	// Import all providers INCLUDING Bedrock.
	// Each provider self-registers via init() when imported.
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic" // Native Anthropic Messages API
	_ "github.com/truvaagents/truva-g3/ai/providers/bedrock"   // AWS Bedrock (requires -tags bedrock)
	_ "github.com/truvaagents/truva-g3/ai/providers/gemini"    // Native Google Gemini API
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"    // Universal OpenAI-compatible
)

func main() {
	// =========================================================================
	// SECTION 1: Provider Registration Verification
	// Confirm that Bedrock is included in the registered providers
	// =========================================================================

	fmt.Println("=== Registered Providers (with Bedrock) ===")
	// ListProviders() returns all registered provider names.
	// With -tags bedrock build, you should see: [anthropic, bedrock, gemini, openai]
	providers := ai.ListProviders()
	for _, p := range providers {
		fmt.Printf("  - %s\n", p)
	}
	fmt.Println()

	// =========================================================================
	// SECTION 2: AWS Bedrock Provider Test
	// Demonstrates Bedrock usage with AWS credential chain
	// =========================================================================

	fmt.Println("=== AWS Bedrock Provider Test ===")

	// AWS Bedrock supports multiple credential sources (in priority order):
	// 1. Explicit credentials via WithAWSCredentials()
	// 2. Environment variables: AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY
	// 3. Shared credentials file: ~/.aws/credentials with AWS_PROFILE
	// 4. IAM instance role (EC2, ECS, Lambda) via AWS_EXECUTION_ENV
	// 5. IAM task role (ECS) via AWS_CONTAINER_CREDENTIALS_RELATIVE_URI
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" || os.Getenv("AWS_EXECUTION_ENV") != "" {

		// Basic Bedrock client creation with default region.
		// The provider will use:
		// - Region from AWS_REGION or AWS_DEFAULT_REGION env vars, or "us-east-1" as fallback
		// - Credentials from AWS credential chain (described above)
		client, err := ai.NewClient(
			ai.WithProvider("bedrock"),
			// Model ID format for Bedrock: provider.model-version:inference-profile
			// Examples:
			// - "anthropic.claude-3-sonnet-20240229-v1:0" (Claude 3 Sonnet)
			// - "anthropic.claude-3-opus-20240229-v1:0" (Claude 3 Opus)
			// - "meta.llama3-70b-instruct-v1:0" (Llama 3 70B)
			// - "amazon.titan-text-premier-v1:0" (Amazon Titan)
			ai.WithModel("anthropic.claude-3-sonnet-20240229-v1:0"),
		)
		if err != nil {
			log.Printf("Failed to create Bedrock client: %v", err)
		} else {
			fmt.Println("AWS Bedrock provider is available and configured!")

			// GenerateResponse() works the same as other providers.
			// The Bedrock provider uses the Converse API internally,
			// which provides a unified interface across all Bedrock models.
			response, err := client.GenerateResponse(
				context.Background(),
				"Say hello in 5 words",
				&core.AIOptions{
					MaxTokens: 50, // Limit response length
					// Temperature, SystemPrompt also supported
				},
			)

			if err != nil {
				// Bedrock errors include:
				// - AccessDeniedException: Model not enabled or no permissions
				// - ValidationException: Invalid model ID or parameters
				// - ThrottlingException: Rate limit exceeded
				// - ResourceNotFoundException: Model not found in region
				fmt.Printf("  Error: %v\n", err)
			} else {
				fmt.Printf("  Response: %s\n", response.Content)
			}
		}

		// =====================================================================
		// Configuration Examples
		// Shows various ways to configure Bedrock
		// =====================================================================

		// Example 1: With explicit region
		// Use this when you need a specific region (model availability varies by region)
		fmt.Println("\n  With explicit region:")
		fmt.Println("  client, _ := ai.NewClient(")
		fmt.Println("      ai.WithProvider(\"bedrock\"),")
		fmt.Println("      ai.WithModel(\"anthropic.claude-3-sonnet-20240229-v1:0\"),")
		fmt.Println("      ai.WithRegion(\"us-west-2\"),  // Override default region")
		fmt.Println("  )")

		// Example 2: With explicit credentials
		// Use this for cross-account access or when not using default credential chain
		fmt.Println("\n  With explicit credentials:")
		fmt.Println("  client, _ := ai.NewClient(")
		fmt.Println("      ai.WithProvider(\"bedrock\"),")
		fmt.Println("      ai.WithModel(\"meta.llama3-70b-instruct-v1:0\"),  // Use Llama 3")
		fmt.Println("      ai.WithRegion(\"us-east-1\"),")
		fmt.Println("      ai.WithAWSCredentials(accessKey, secretKey, sessionToken),")
		fmt.Println("  )")

		// Model reference for Bedrock:
		fmt.Println("\n  Available model families in Bedrock:")
		fmt.Println("  - Anthropic Claude: anthropic.claude-3-{opus,sonnet,haiku}-*")
		fmt.Println("  - Meta Llama: meta.llama3-{70b,8b}-instruct-v1:0")
		fmt.Println("  - Amazon Titan: amazon.titan-text-{premier,express,lite}-v1:0")
		fmt.Println("  - Mistral: mistral.mistral-{7b,mixtral-8x7b}-instruct-v0:1")
		fmt.Println("  - Cohere: cohere.command-{text,light}-v14")

	} else {
		// No AWS credentials detected
		fmt.Println("  AWS credentials not configured")
		fmt.Println("  Set one of:")
		fmt.Println("    - AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (explicit credentials)")
		fmt.Println("    - AWS_PROFILE (named profile from ~/.aws/credentials)")
		fmt.Println("    - Run on AWS (EC2/ECS/Lambda with IAM role)")
		fmt.Println()
		fmt.Println("  Quick setup:")
		fmt.Println("    export AWS_ACCESS_KEY_ID=AKIA...")
		fmt.Println("    export AWS_SECRET_ACCESS_KEY=...")
		fmt.Println("    export AWS_REGION=us-east-1")
	}
}
