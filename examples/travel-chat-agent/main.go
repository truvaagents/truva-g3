package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"

	// Import AI providers for auto-detection
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
	_ "github.com/truvaagents/truva-g3/ai/providers/gemini"
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
	// 1. Validate configuration (fail fast)
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// 2. Set component type for service_type labeling in telemetry
	core.SetCurrentComponentType(core.ComponentTypeAgent)

	// 3. Initialize telemetry BEFORE creating agent/AI client (critical for AI spans)
	initTelemetry("travel-chat-agent")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		telemetry.Shutdown(ctx)
	}()

	// 4. Create agent AFTER telemetry is initialized
	agent, err := NewTravelChatAgent()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	skillRegistry, skillClients, err := newSkillRegistry(agent.Logger)
	if err != nil {
		log.Fatalf("Failed to create skill registry: %v", err)
	}
	defer func() {
		if skillClients != nil {
			_ = skillClients.Close()
		}
	}()

	// 5. Create framework with tracing middleware
	middlewareConfig := &telemetry.TracingMiddlewareConfig{
		ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live", "/api/capabilities"},
	}

	framework, err := core.NewFramework(agent,
		core.WithName("travel-chat-agent"),
		core.WithPort(getPort()),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORSDefaults(), // Browser UI needs Accept + X-User-ID; the strict default only allows Content-Type + Authorization
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("travel-chat-agent", middlewareConfig)),
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
		if err := agent.InitializeOrchestrator(agent.Discovery, skillRegistry); err != nil {
			agent.Logger.Warn("Failed to initialize orchestrator", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			agent.Logger.Info("Orchestrator initialized successfully", nil)
		}
	}()

	// Register scheduled endpoint BEFORE framework.Run() — HandleFunc rejects
	// registrations after the HTTP server starts. The orchestrator is resolved
	// lazily via GetOrchestrator on each request (returns nil until the async
	// init goroutine above completes, producing a 503).
	if err := orchestration.RegisterScheduledEndpoint(agent.BaseAgent, func() orchestration.Orchestrator {
		if o := agent.GetOrchestrator(); o != nil {
			return o
		}
		return nil
	}); err != nil {
		agent.Logger.Warn("Failed to register scheduled endpoint", map[string]interface{}{"error": err.Error()})
	}

	// 7. Emit startup metrics
	telemetry.Counter("travel_chat_agent.startup", "status", "success")

	// 8. Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, initiating graceful shutdown...", sig)
		cancel()
	}()

	// 9. Run the framework
	log.Printf("Starting travel-chat-agent on port %d", getPort())
	if err := framework.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Framework error: %v", err)
	}

	// Drain in-flight async user-memory extraction goroutines BEFORE releasing
	// the backend — extractions call into the vector DB, so Close() order matters.
	if agent.userMemCloser != nil {
		_ = agent.userMemCloser.Close()
	}

	// Close user memory backend (releases vector DB connection if applicable)
	if agent.userMemBackend != nil {
		agent.userMemBackend.Close()
	}
	log.Println("Travel chat agent shutdown complete")
}

// validateConfig validates required configuration
func validateConfig() error {
	// At least one AI provider key is required
	if os.Getenv("OPENAI_API_KEY") == "" &&
		os.Getenv("ANTHROPIC_API_KEY") == "" &&
		os.Getenv("GOOGLE_API_KEY") == "" &&
		os.Getenv("GEMINI_API_KEY") == "" {
		log.Println("Warning: No AI provider API key found. Set OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY, or GEMINI_API_KEY")
	}

	// Redis is required for service discovery and session storage
	if os.Getenv("REDIS_URL") == "" {
		return fmt.Errorf("REDIS_URL is required for service discovery and session storage")
	}

	return nil
}

// initTelemetry initializes OpenTelemetry with environment-aware configuration
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
		profile = telemetry.ProfileProduction // 0.1% sampling
	case "staging", "stage":
		profile = telemetry.ProfileStaging // 10% sampling
	default:
		profile = telemetry.ProfileDevelopment // 100% sampling
	}

	// Use the profile to get base configuration
	config := telemetry.UseProfile(profile)
	config.ServiceName = serviceName

	// Allow environment variables to override specific settings
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		config.Endpoint = endpoint
	}

	// Initialize telemetry
	if err := telemetry.Initialize(config); err != nil {
		// IMPORTANT: Don't let telemetry failures crash your app!
		log.Printf("Warning: Telemetry initialization failed: %v", err)
		log.Printf("Application will continue without telemetry")
		return
	}

	// Enable framework integration - this allows core components (redis_registry, discovery)
	// to emit metrics like discovery.registrations, discovery.health_checks, etc.
	telemetry.EnableFrameworkIntegration(nil)

	log.Printf("Telemetry initialized successfully")
	log.Printf("  Environment: %s", env)
	log.Printf("  Profile: %s", profile)
	log.Printf("  Service: %s", serviceName)
	if config.Endpoint != "" {
		log.Printf("  Endpoint: %s", config.Endpoint)
	}
}

// getPort returns the server port from PORT environment variable.
// PORT is required - no default to avoid port conflicts between examples.
func getPort() int {
	port := os.Getenv("PORT")
	if port == "" {
		log.Fatal("PORT environment variable is required. Set it in .env or k8-deployment.yaml")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 {
		log.Fatalf("PORT must be a positive integer, got: %s", port)
	}
	return p
}
