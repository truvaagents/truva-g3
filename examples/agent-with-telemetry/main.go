// Package main implements a research assistant agent with comprehensive telemetry
// and observability using the TruvaG3 framework's telemetry module.
//
// This example demonstrates how to add production-grade monitoring to an agent with
// minimal code changes. It builds on the agent-example by adding:
//   - Comprehensive metrics collection (counters, histograms, gauges)
//   - Distributed tracing with OpenTelemetry
//   - Environment-based telemetry profiles (dev/staging/prod)
//   - Integration with Prometheus, Jaeger, and Grafana
//
// Environment Variables:
//
//	REDIS_URL                      - Redis connection URL (required)
//	PORT                           - HTTP server port (default: 8092)
//	NAMESPACE                      - Kubernetes namespace for service discovery
//	TRUVAG3_K8S_SERVICE_NAME        - Service name for registration and telemetry (required)
//	OPENAI_API_KEY                 - OpenAI API key for AI capabilities
//	APP_ENV                        - Environment: development, staging, production
//	OTEL_EXPORTER_OTLP_ENDPOINT    - OpenTelemetry collector endpoint
//	DEV_MODE                       - Enable development mode (true/false)
//
// Example Usage:
//
//	export REDIS_URL="redis://localhost:6379"
//	export OPENAI_API_KEY="sk-..."
//	export APP_ENV="development"
//	export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
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
	"github.com/truvaagents/truva-g3/telemetry"

	// Import AI providers for auto-detection
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
	_ "github.com/truvaagents/truva-g3/ai/providers/gemini"
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
	// Track startup time for metrics
	startupStart := time.Now()

	// 1. Validate configuration first (fail fast)
	if err := validateConfig(); err != nil {
		log.Fatalf("❌ Configuration error: %v", err)
	}

	// 2. Set component type FIRST for telemetry auto-inference
	// This must happen before telemetry initialization so service_type is set correctly
	core.SetCurrentComponentType(core.ComponentTypeAgent)

	// 3. Initialize telemetry BEFORE agent creation
	// This ensures telemetry.GetTelemetryProvider() returns a valid provider
	// when the AI client is created in NewResearchAgent()
	initTelemetry("research-agent-telemetry")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			log.Printf("⚠️  Warning: Telemetry shutdown error: %v", err)
		}
	}()

	// 4. Create research agent AFTER telemetry is initialized
	// The AI client will now receive the telemetry provider for distributed tracing
	agent, err := NewResearchAgent()
	if err != nil {
		log.Fatalf("Failed to create research agent: %v", err)
	}

	// Initialize schema cache with Redis (for Phase 3 validation caching)
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		// Parse Redis options from URL
		redisOpt, err := redis.ParseURL(redisURL)
		if err != nil {
			log.Printf("⚠️  Warning: Failed to parse REDIS_URL for schema cache: %v", err)
			log.Println("   Schema caching will be disabled")
		} else {
			// Create Redis client for schema cache
			redisClient := redis.NewClient(redisOpt)

			// Test connection
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			if err := redisClient.Ping(ctx).Err(); err != nil {
				log.Printf("⚠️  Warning: Redis connection failed for schema cache: %v", err)
				log.Println("   Schema caching will be disabled")
				redisClient.Close()
			} else {
				// Initialize schema cache with Redis backend
				agent.SchemaCache = core.NewSchemaCache(redisClient)
				log.Println("✅ Schema cache initialized with Redis backend")
			}
		}
	} else {
		log.Println("ℹ️  Schema caching disabled (no REDIS_URL)")
	}

	// 4. Get port configuration
	port := 8092 // default for agent-with-telemetry
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	// 5. Create framework with configuration
	framework, err := core.NewFramework(agent,
		core.WithName("research-agent-telemetry"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),

		// Distributed tracing middleware — excludes health endpoints to reduce noise
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("research-agent-telemetry",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}
	framework.AutoRegisterMemorySweeper() // periodic eviction for the default *MemoryStore

	// 6. Emit startup metrics
	startupDuration := time.Since(startupStart)
	// Use Microseconds and convert to float ms to preserve precision
	startupMs := float64(startupDuration.Microseconds()) / 1000.0
	telemetry.Histogram("agent.startup.duration_ms",
		startupMs,
		"agent", "research-agent-telemetry",
		"status", "success",
	)
	telemetry.Gauge("agent.capabilities.count",
		float64(len(agent.Capabilities)),
		"agent", "research-agent-telemetry",
	)

	// 6b. Perform initial service discovery to populate metrics
	// This triggers discovery.services.found and discovery.lookups metrics
	initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer initCancel()
	if agent.Discovery != nil {
		tools, err := agent.Discovery.Discover(initCtx, core.DiscoveryFilter{
			Type: core.ComponentTypeTool,
		})
		if err != nil {
			log.Printf("⚠️  Initial tool discovery failed: %v", err)
		} else {
			log.Printf("🔍 Discovered %d tools at startup", len(tools))
			// Emit services found gauge
			telemetry.Gauge("discovery.services.found",
				float64(len(tools)),
				"type", "tool",
			)
		}
	}

	// 7. Display startup information
	log.Println("🤖 Research Assistant Agent (with Telemetry) Starting...")
	log.Println("📊 Telemetry: Enabled")
	log.Println("🧠 AI Provider:", getAIProviderStatus())
	log.Printf("🌐 Server Port: %d\n", port)
	log.Printf("📊 Capabilities Registered: %d\n", len(agent.Capabilities))
	log.Printf("⏱️  Startup Duration: %v\n", startupDuration)
	log.Println("📋 Registered endpoints will be shown in framework logs below...")
	log.Println()

	// 7. Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("\n⚠️  Shutting down gracefully...")

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
			log.Println("❌ Shutdown timeout exceeded")
			os.Exit(1)
		case <-time.After(1 * time.Second):
			// Give framework time to clean up
		}

		log.Println("✅ Shutdown completed")
		os.Exit(0)
	}()

	// 8. Run the framework (blocking)
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

	// TRUVAG3_K8S_SERVICE_NAME is required for consistent naming
	if os.Getenv("TRUVAG3_K8S_SERVICE_NAME") == "" {
		return fmt.Errorf("TRUVAG3_K8S_SERVICE_NAME environment variable required")
	}

	// Validate port if set
	if portStr := os.Getenv("PORT"); portStr != "" {
		if _, err := strconv.Atoi(portStr); err != nil {
			return fmt.Errorf("invalid PORT value: %v", err)
		}
	}

	return nil
}


// initTelemetry sets up telemetry based on environment with graceful degradation
func initTelemetry(serviceName string) {
	// Detect environment from APP_ENV variable
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development" // Safe default
	}

	// Select the appropriate telemetry profile
	var profile telemetry.Profile
	switch env {
	case "production", "prod":
		profile = telemetry.ProfileProduction
	case "staging", "stage", "qa":
		profile = telemetry.ProfileStaging
	default:
		profile = telemetry.ProfileDevelopment
	}

	// Use the profile to get base configuration
	config := telemetry.UseProfile(profile)

	// Override with service name
	config.ServiceName = serviceName

	// Allow environment variables to override specific settings
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		config.Endpoint = endpoint
	}

	// Initialize telemetry
	if err := telemetry.Initialize(config); err != nil {
		// IMPORTANT: Don't let telemetry failures crash your app!
		log.Printf("⚠️  Warning: Telemetry initialization failed: %v", err)
		log.Printf("   Application will continue without telemetry")
		return
	}

	// Enable framework integration - this allows core components (redis_registry, discovery)
	// to emit metrics like discovery.registrations, discovery.health_checks, etc.
	telemetry.EnableFrameworkIntegration(nil)

	log.Printf("✅ Telemetry initialized successfully")
	log.Printf("   Environment: %s", env)
	log.Printf("   Profile: %s", profile)
	log.Printf("   Service: %s", serviceName)
	if config.Endpoint != "" {
		log.Printf("   Endpoint: %s", config.Endpoint)
	}
}

// getAIProviderStatus returns the detected AI provider name
func getAIProviderStatus() string {
	// Check for common AI provider environment variables
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

	// Check for custom OpenAI-compatible endpoints
	if os.Getenv("OPENAI_BASE_URL") != "" {
		return "Custom OpenAI-Compatible"
	}

	return "None (will use mock responses)"
}
