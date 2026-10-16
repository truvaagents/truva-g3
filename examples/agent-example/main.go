// Package main implements a research assistant agent that demonstrates intelligent
// tool orchestration and AI-powered analysis using the TruvaG3 framework.
//
// This agent showcases several key capabilities:
//   - Automatic tool discovery via Redis service registry
//   - Multi-entity comparison with parallel execution (e.g., "Compare SF vs LA weather")
//   - Hybrid AI operation (uses tools when available, direct AI when not)
//   - AI-powered payload generation for tool calls
//   - Schema validation caching for performance
//
// Environment Variables:
//
//	REDIS_URL              - Redis connection URL (required)
//	PORT                   - HTTP server port (default: 8090)
//	NAMESPACE              - Kubernetes namespace for service discovery
//	OPENAI_API_KEY         - OpenAI API key for AI capabilities
//	DEV_MODE               - Enable development mode (true/false)
//
// Example Usage:
//
//	export REDIS_URL="redis://localhost:6379"
//	export OPENAI_API_KEY="sk-..."
//	go run .
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/truvaagents/truva-g3/core"

	// Import AI providers for auto-detection
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
	_ "github.com/truvaagents/truva-g3/ai/providers/gemini"
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Service failed: %v", err)
	}
}

func run() error {
	// Validate configuration first
	if err := validateConfig(); err != nil {
		return fmt.Errorf("Configuration error: %w", err)
	}
	redisResolution, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv)
	if err != nil {
		return fmt.Errorf("Redis configuration error: %w", err)
	}

	// Create research agent
	agent, err := NewResearchAgent()
	if err != nil {
		return fmt.Errorf("Failed to create research agent: %w", err)
	}

	// Initialize the optional schema cache from the same topology as discovery.
	if redisClient, cacheErr := core.NewRedisUniversalClient(redisResolution); cacheErr != nil {
		log.Printf("⚠️  Warning: Redis unavailable for schema cache: %v", cacheErr)
		log.Println("   Schema caching will be disabled")
	} else {
		defer redisClient.Close()
		agent.SchemaCache = core.NewSchemaCache(redisClient)
		log.Println("✅ Schema cache initialized with Redis backend")
	}

	// Get port configuration
	port := 8090 // default
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Close recorders after HTTP draining completes or reaches its deadline.
		if agent.instrumentedClient != nil {
			if err := agent.instrumentedClient.Shutdown(shutdownCtx); err != nil {
				log.Printf("Warning: LLM debug shutdown: %v", err)
			}
		}
		if agent.debugRecorder != nil {
			if err := agent.debugRecorder.Close(); err != nil {
				log.Printf("Warning: LLM debug recorder close: %v", err)
			}
		}
	}()

	// Create framework with configuration
	framework, err := core.NewFramework(agent,
		core.WithName("research-assistant"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisConnection(redisResolution),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
	)
	if err != nil {
		return fmt.Errorf("Failed to create framework: %w", err)
	}
	framework.AutoRegisterMemorySweeper() // periodic eviction for the default *MemoryStore

	// Display startup information
	log.Println("🤖 Research Assistant Agent Starting...")
	log.Println("🧠 AI Provider:", getAIProviderStatus())
	log.Printf("🌐 Server Port: %d\n", port)
	log.Println("📋 Registered endpoints will be shown in framework logs below...")
	log.Println()

	// Set up graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run framework: %w", err)
	}
	return nil
}

// validateConfig validates all required configuration at startup
func validateConfig() error {
	if _, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv); err != nil {
		return fmt.Errorf("invalid Redis configuration: %w", err)
	}

	// Validate port if set
	if portStr := os.Getenv("PORT"); portStr != "" {
		if _, err := strconv.Atoi(portStr); err != nil {
			return fmt.Errorf("invalid PORT value: %v", err)
		}
	}

	return nil
}
