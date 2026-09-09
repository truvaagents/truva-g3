// Package main implements a research assistant agent that demonstrates the TruvaG3
// resilience module for fault-tolerant tool orchestration.
//
// This example showcases:
//   - Circuit breakers for protecting against failing services
//   - Automatic retries with exponential backoff using resilience.RetryWithCircuitBreaker
//   - Timeout management using bounded HTTP requests
//   - Graceful degradation with partial results
//   - Health monitoring with circuit breaker states via cb.GetMetrics()
//
// Key Framework APIs Used:
//   - resilience.CreateCircuitBreaker(name, deps) - Factory with DI
//   - resilience.DefaultRetryConfig() - Sensible defaults
//   - resilience.RetryWithCircuitBreaker(ctx, config, cb, fn) - Combined pattern
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
	"syscall"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"

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
	redisResolution, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv)
	if err != nil {
		log.Fatalf("Redis configuration error: %v", err)
	}
	// Initialize observability at the application boundary, before constructing
	// clients that capture the provider. Core deliberately does not auto-wire it.
	core.SetCurrentComponentType(core.ComponentTypeAgent)
	if err := telemetry.Initialize(resilienceTelemetryConfig()); err != nil {
		log.Println("Telemetry initialization failed; observability is unavailable")
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := telemetry.Shutdown(shutdownCtx); err != nil {
			log.Println("Telemetry shutdown did not complete")
		}
	}()

	// Create research agent with resilience capabilities
	agent, err := NewResearchAgent()
	if err != nil {
		log.Fatalf("Failed to create research agent: %v", err)
	}

	// Initialize the optional schema cache from the same topology as discovery.
	if redisClient, cacheErr := core.NewRedisUniversalClient(redisResolution); cacheErr != nil {
		log.Printf("Warning: Redis unavailable for schema cache: %v", cacheErr)
		log.Println("   Schema caching will be disabled")
	} else {
		defer func() { _ = redisClient.Close() }()
		agent.SchemaCache = core.NewSchemaCache(redisClient)
		log.Println("Schema cache initialized with Redis backend")
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
		core.WithRedisConnection(redisResolution),
		core.WithDiscovery(true, "redis"),
		core.WithCORSDefaults(),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("research-assistant-resilience",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			})),
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

		log.Println("Shutdown completed")
		// Let main return normally so its telemetry flush and client cleanup run.
		cancel()
	}()

	// Run the framework (blocking)
	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
	}
}

func resilienceTelemetryConfig() telemetry.Config {
	profile := telemetry.ProfileDevelopment
	switch os.Getenv("APP_ENV") {
	case "production", "prod":
		profile = telemetry.ProfileProduction
	case "staging", "stage", "qa":
		profile = telemetry.ProfileStaging
	}
	config := telemetry.UseProfile(profile)
	config.ServiceName = "research-assistant-resilience"
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		config.Endpoint = endpoint
	}
	return config
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
