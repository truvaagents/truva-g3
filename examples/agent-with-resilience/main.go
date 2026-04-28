// Package main implements a research assistant agent that demonstrates the Truva-G3
// resilience module for fault-tolerant tool orchestration.
//
// This example showcases:
//   - Circuit breakers for protecting against failing services
//   - Automatic retries with exponential backoff using resilience.RetryWithCircuitBreaker
//   - Timeout management using cb.ExecuteWithTimeout
//   - Graceful degradation with partial results
//   - Health monitoring with circuit breaker states via cb.GetMetrics()
//
// Key Framework APIs Used:
//   - resilience.CreateCircuitBreaker(name, deps) - Factory with DI
//   - resilience.DefaultRetryConfig() - Sensible defaults
//   - resilience.RetryWithCircuitBreaker(ctx, config, cb, fn) - Combined pattern
//   - cb.ExecuteWithTimeout(ctx, timeout, fn) - Timeout + CB
//   - cb.GetState() / cb.GetMetrics() - Health monitoring
//
// Environment Variables:
//
//	REDIS_URL              - Redis connection URL (required)
//	PORT                   - HTTP server port (default: 8093)
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
	"strings"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"

	// Import AI providers for auto-detection
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
	_ "github.com/truvaagents/truva-g3/ai/providers/gemini"
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
	// Validate configuration first
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Create research agent with resilience capabilities
	agent, err := NewResearchAgent()
	if err != nil {
		log.Fatalf("Failed to create research agent: %v", err)
	}

	// Initialize schema cache with Redis (for Phase 3 validation caching)
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		redisOpt, err := redis.ParseURL(redisURL)
		if err != nil {
			log.Printf("Warning: Failed to parse REDIS_URL for schema cache: %v", err)
			log.Println("   Schema caching will be disabled")
		} else {
			redisClient := redis.NewClient(redisOpt)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			if err := redisClient.Ping(ctx).Err(); err != nil {
				log.Printf("Warning: Redis connection failed for schema cache: %v", err)
				log.Println("   Schema caching will be disabled")
				redisClient.Close()
			} else {
				agent.SchemaCache = core.NewSchemaCache(redisClient)
				log.Println("Schema cache initialized with Redis backend")
			}
		}
	} else {
		log.Println("Schema caching disabled (no REDIS_URL)")
	}

	// Get port configuration (default: 8093 for resilience example)
	port := 8093
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	// Create framework with configuration
	framework, err := core.NewFramework(agent,
		core.WithName("research-assistant-resilience"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}
	framework.AutoRegisterMemorySweeper() // periodic eviction for the default *MemoryStore

	// Display startup information
	log.Println("==============================================")
	log.Println("Research Assistant Agent (with Resilience)")
	log.Println("==============================================")
	log.Println("AI Provider:", getAIProviderStatus())
	log.Printf("Server Port: %d\n", port)
	log.Println("Resilience: Circuit Breakers + Retry enabled")
	log.Println("==============================================")
	log.Println()

	// Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("\nShutting down gracefully...")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		// Drain in-flight LLM debug recordings before stopping.
		// Must happen before cancel() so Redis connections are still alive.
		if agent.instrumentedClient != nil {
			if err := agent.instrumentedClient.Shutdown(shutdownCtx); err != nil {
				log.Printf("Warning: LLM debug shutdown: %v", err)
			}
		}
		// Close the debug recorder's Redis connection after recordings are drained.
		if agent.debugRecorder != nil {
			if err := agent.debugRecorder.Close(); err != nil {
				log.Printf("Warning: LLM debug recorder close: %v", err)
			}
		}

		cancel()

		select {
		case <-shutdownCtx.Done():
			log.Println("Shutdown timeout exceeded")
			os.Exit(1)
		case <-time.After(1 * time.Second):
			// Give framework time to clean up
		}

		log.Println("Shutdown completed")
		os.Exit(0)
	}()

	// Run the framework (blocking)
	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
	}
}

// validateConfig validates all required configuration at startup
func validateConfig() error {
	// REDIS_URL is required for discovery
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return fmt.Errorf("REDIS_URL environment variable required")
	}

	// Validate Redis URL format
	if !strings.HasPrefix(redisURL, "redis://") && !strings.HasPrefix(redisURL, "rediss://") {
		return fmt.Errorf("invalid REDIS_URL format (must start with redis:// or rediss://)")
	}

	// Validate port if set
	if portStr := os.Getenv("PORT"); portStr != "" {
		if _, err := strconv.Atoi(portStr); err != nil {
			return fmt.Errorf("invalid PORT value: %v", err)
		}
	}

	return nil
}

// getAIProviderStatus returns the detected AI provider name
func getAIProviderStatus() string {
	providers := []struct {
		name   string
		envVar string
	}{
		{"OpenAI", "OPENAI_API_KEY"},
		{"Groq", "GROQ_API_KEY"},
		{"Anthropic", "ANTHROPIC_API_KEY"},
		{"Gemini", "GEMINI_API_KEY"},
		{"DeepSeek", "DEEPSEEK_API_KEY"},
	}

	for _, provider := range providers {
		if os.Getenv(provider.envVar) != "" {
			return provider.name
		}
	}

	if os.Getenv("OPENAI_BASE_URL") != "" {
		return "Custom OpenAI-Compatible"
	}

	return "None (will use mock responses)"
}
