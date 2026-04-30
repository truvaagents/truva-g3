// Package main implements a Travel Research Agent that demonstrates the TruvaG3
// orchestration module through intelligent multi-tool coordination with full telemetry.
//
// This agent showcases:
//   - AI-powered orchestrator for dynamic request routing
//   - DAG-based workflow execution with parallel/sequential dependencies
//   - Predefined travel research workflows
//   - Natural language request processing
//   - Distributed tracing across tool calls with OpenTelemetry
//   - Log correlation via trace IDs for production troubleshooting
//   - AI synthesis of multi-tool results
//
// Environment Variables:
//
//	REDIS_URL                      - Redis connection URL (required)
//	PORT                           - HTTP server port (default: 8094)
//	NAMESPACE                      - Kubernetes namespace for service discovery
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
	"github.com/truvaagents/truva-g3/orchestration"
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
	// when the AI client is created in NewTravelResearchAgent()
	initTelemetry("travel-research-orchestration")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			log.Printf("⚠️  Warning: Telemetry shutdown error: %v", err)
		}
	}()

	// 4. Create orchestration agent AFTER telemetry is initialized
	// The AI client will now receive the telemetry provider for distributed tracing
	agent, err := NewTravelResearchAgent()
	if err != nil {
		log.Fatalf("Failed to create travel research agent: %v", err)
	}

	// Initialize Redis client for schema cache
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		redisOpt, err := redis.ParseURL(redisURL)
		if err != nil {
			log.Printf("⚠️  Warning: Failed to parse REDIS_URL for schema cache: %v", err)
			log.Println("   Schema caching will be disabled")
		} else {
			redisClient := redis.NewClient(redisOpt)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			if err := redisClient.Ping(ctx).Err(); err != nil {
				log.Printf("⚠️  Warning: Redis connection failed for schema cache: %v", err)
				log.Println("   Schema caching will be disabled")
				redisClient.Close()
			} else {
				agent.SchemaCache = core.NewSchemaCache(redisClient)
				log.Println("✅ Schema cache initialized with Redis backend")
			}
		}
	} else {
		log.Println("ℹ️  Schema caching disabled (no REDIS_URL)")
	}

	// 4. Get port configuration
	port := 8094 // default for orchestration agent
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	// 5. Create framework with configuration
	framework, err := core.NewFramework(agent,
		core.WithName("travel-research-orchestration"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),

		// Distributed tracing middleware for context propagation
		// Exclude health/readiness endpoints to reduce noise in traces
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("travel-research-orchestration", &telemetry.TracingMiddlewareConfig{
			ExcludedPaths: []string{"/health", "/ready", "/metrics", "/api/capabilities"},
		})),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// 6. Initialize orchestrator in background (Discovery is set during framework.Run())
	// This goroutine waits for Discovery to become available and then initializes the orchestrator.
	// This is cleaner than lazy initialization in handlers because:
	// - Initialization logic is centralized in main.go
	// - Orchestrator is ready as soon as possible after framework starts
	// - No mixing of init logic with health check logic
	go func() {
		// Wait for Discovery, logging warnings if it takes too long
		startTime := time.Now()
		lastWarning := time.Time{}

		for agent.BaseAgent.Discovery == nil {
			time.Sleep(100 * time.Millisecond)

			elapsed := time.Since(startTime)
			// Log warning after 30s, then every 60s thereafter
			if elapsed > 30*time.Second && time.Since(lastWarning) > 60*time.Second {
				if lastWarning.IsZero() {
					agent.Logger.Warn("Discovery not available after 30s", map[string]interface{}{
						"hint": "check Redis connectivity (REDIS_URL)",
					})
				} else {
					agent.Logger.Warn("Still waiting for Discovery", map[string]interface{}{
						"elapsed": elapsed.Round(time.Second).String(),
					})
				}
				lastWarning = time.Now()
			}
		}

		// Discovery is available, initialize orchestrator
		if err := agent.InitializeOrchestrator(agent.BaseAgent.Discovery); err != nil {
			agent.Logger.Warn("Failed to initialize orchestrator", map[string]interface{}{
				"error":  err.Error(),
				"impact": "Orchestration endpoints will be unavailable",
			})
		} else {
			agent.Logger.Info("Orchestrator initialized successfully", nil)
		}
	}()

	// 7. Emit startup metrics
	startupDuration := time.Since(startupStart)
	startupMs := float64(startupDuration.Microseconds()) / 1000.0
	telemetry.Histogram("agent.startup.duration_ms",
		startupMs,
		"agent", "travel-research-orchestration",
		"status", "success",
	)
	telemetry.Gauge("agent.capabilities.count",
		float64(len(agent.BaseAgent.Capabilities)),
		"agent", "travel-research-orchestration",
	)
	telemetry.Gauge("agent.workflows.count",
		float64(len(agent.workflows)),
		"agent", "travel-research-orchestration",
	)

	// 7b. Perform initial service discovery to populate metrics
	initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer initCancel()
	if agent.BaseAgent.Discovery != nil {
		tools, err := agent.BaseAgent.Discovery.Discover(initCtx, core.DiscoveryFilter{
			Type: core.ComponentTypeTool,
		})
		if err != nil {
			agent.Logger.Warn("Initial tool discovery failed", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			agent.Logger.Info("Initial tool discovery completed", map[string]interface{}{
				"tool_count": len(tools),
			})
			telemetry.Gauge("discovery.services.found",
				float64(len(tools)),
				"type", "tool",
			)
		}
	}

	// 8. Log startup information using framework Logger
	agent.Logger.Info("Agent starting", map[string]interface{}{
		"id":                agent.GetID(),
		"port":              port,
		"ai_provider":       getAIProviderStatus(),
		"orchestrator_mode": getOrchestratorMode(),
		"capabilities":      len(agent.BaseAgent.Capabilities),
		"workflows":         len(agent.workflows),
		"startup_ms":        startupDuration.Milliseconds(),
	})

	// 9. Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		agent.Logger.Info("Shutting down gracefully", nil)

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		// Stop orchestrator background processes
		if agent.orchestrator != nil {
			agent.orchestrator.Stop()
			agent.Logger.Debug("Orchestrator stopped", nil)
		}

		cancel()

		select {
		case <-shutdownCtx.Done():
			agent.Logger.Error("Shutdown timeout exceeded", nil)
			os.Exit(1)
		case <-time.After(1 * time.Second):
			// Give framework time to clean up
		}

		agent.Logger.Info("Shutdown completed", nil)
		os.Exit(0)
	}()

	// 10. Run the framework (blocking)
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

	return "None (orchestration will work without AI synthesis)"
}

// getOrchestratorMode returns the configured orchestrator mode
func getOrchestratorMode() string {
	mode := os.Getenv("TRUVAG3_ORCHESTRATOR_MODE")
	if mode == "" {
		mode = string(orchestration.ModeAutonomous)
	}
	return mode
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
