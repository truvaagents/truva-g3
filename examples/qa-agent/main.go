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
	"github.com/truvaagents/truva-g3/memory"
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
	redisResolution, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv)
	if err != nil {
		log.Fatalf("Redis configuration error: %v", err)
	}
	redisKeyspace, err := core.NewRedisKeyspace(os.Getenv("TRUVAG3_REDIS_NAMESPACE"))
	if err != nil {
		log.Fatalf("Redis namespace error: %v", err)
	}

	// 2. Set component type for service_type labeling in telemetry
	core.SetCurrentComponentType(core.ComponentTypeAgent)

	// 3. Initialize telemetry BEFORE creating agent/AI client
	initTelemetry("qa-agent")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		telemetry.Shutdown(ctx)
	}()

	// 4. Create agent AFTER telemetry is initialized
	agent, err := NewQAAgent()
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

	// 5. Setup shared agent memory (episodic events, investigation coordination)
	var memBackends *memory.SharedBackends
	if redisClient, clientErr := core.NewRedisUniversalClient(redisResolution); clientErr != nil {
		agent.Logger.Warn("Shared memory Redis unavailable, running without cross-agent memory", map[string]interface{}{
			"operation":  "shared_memory_setup",
			"status":     "degraded",
			"error":      "redis shared memory startup failed",
			"error_type": "backend_startup",
		})
	} else {
		defer redisClient.Close()
		memBackends, err = memory.NewSharedBackends(redisClient, agent.Logger,
			memory.WithAgentName("qa-agent"),
			memory.WithDomain("infrastructure"),
			memory.WithRedisDeployment(redisKeyspace.Deployment()),
		)
		if err != nil {
			agent.Logger.Warn("Shared memory setup failed, running without cross-agent memory", map[string]interface{}{
				"operation":  "shared_memory_setup",
				"status":     "degraded",
				"error":      "shared memory backend setup failed",
				"error_type": "backend_startup",
			})
		}
	}
	if memBackends != nil {
		defer memBackends.Close()
	}

	// 6. Create framework with tracing middleware
	middlewareConfig := &telemetry.TracingMiddlewareConfig{
		ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live", "/api/capabilities"},
	}

	framework, err := core.NewFramework(agent,
		core.WithName("qa-agent"),
		core.WithPort(getPort()),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisConnection(redisResolution),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("qa-agent", middlewareConfig)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// 7. Initialize orchestrator in background (waits for Discovery)
	go func() {
		startTime := time.Now()
		lastWarning := time.Time{}

		for agent.BaseAgent.Discovery == nil {
			time.Sleep(100 * time.Millisecond)

			elapsed := time.Since(startTime)
			if elapsed > 30*time.Second && time.Since(lastWarning) > 60*time.Second {
				if lastWarning.IsZero() {
					agent.Logger.Warn("Discovery not available after 30s", map[string]interface{}{
						"hint": "check Redis topology configuration and connectivity",
					})
				} else {
					agent.Logger.Warn("Still waiting for Discovery", map[string]interface{}{
						"elapsed": elapsed.Round(time.Second).String(),
					})
				}
				lastWarning = time.Now()
			}
		}

		// Build memory hooks from backends (Layer 1)
		var memHooks []core.PipelineHook
		var activityCoord core.ActivityCoordinator
		if memBackends != nil {
			memHooks, activityCoord = orchestration.BuildMemoryHooks(memBackends.ToDeps(), agent.AI, agent.Logger)
		}

		if err := agent.InitializeOrchestrator(
			agent.Discovery, memHooks, activityCoord, skillRegistry,
		); err != nil {
			agent.Logger.Warn("Failed to initialize orchestrator", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			agent.Logger.Info("Orchestrator initialized successfully", nil)
		}
	}()

	if err := orchestration.RegisterScheduledEndpoint(agent.BaseAgent, func() orchestration.Orchestrator {
		if o := agent.GetOrchestrator(); o != nil {
			return o
		}
		return nil
	}); err != nil {
		agent.Logger.Warn("Failed to register scheduled endpoint", map[string]interface{}{"error": err.Error()})
	}

	// 8. Emit startup metrics
	telemetry.Counter("qa_agent.startup", "status", "success")

	// 9. Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, initiating graceful shutdown...", sig)
		cancel()
	}()

	// 9b. Reflection Job: bridge episodic events to long-term knowledge
	// Layer 1 wiring — framework manages lifecycle via Runnable interface.
	// Returns nil when Phase 2 backends (Qdrant + embedder) are unavailable.
	if memBackends != nil {
		if reflectionJob, _ := memory.BuildReflectionJob(memBackends.ToDeps(), agent.AI, agent.Logger); reflectionJob != nil {
			framework.RegisterRunnable(reflectionJob)
		}
	}

	// 10. Run the framework
	log.Printf("Starting qa-agent on port %d", getPort())
	if err := framework.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Framework error: %v", err)
	}

	log.Println("QA agent shutdown complete")
}

// validateConfig validates required configuration
func validateConfig() error {
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Println("Warning: No AI provider API key found. Set OPENAI_API_KEY or ANTHROPIC_API_KEY")
	}

	if _, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv); err != nil {
		return fmt.Errorf("invalid Redis configuration: %w", err)
	}

	return nil
}

// initTelemetry initializes OpenTelemetry
func initTelemetry(serviceName string) {
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}

	var profile telemetry.Profile
	switch env {
	case "production", "prod":
		profile = telemetry.ProfileProduction
	case "staging", "stage":
		profile = telemetry.ProfileStaging
	default:
		profile = telemetry.ProfileDevelopment
	}

	config := telemetry.UseProfile(profile)
	config.ServiceName = serviceName

	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		config.Endpoint = endpoint
	}

	if err := telemetry.Initialize(config); err != nil {
		log.Printf("Warning: Telemetry initialization failed: %v", err)
		log.Printf("Application will continue without telemetry")
		return
	}

	telemetry.EnableFrameworkIntegration(nil)

	log.Printf("Telemetry initialized: env=%s, profile=%s, service=%s", env, profile, serviceName)
}

// getPort returns the server port from PORT environment variable.
func getPort() int {
	port := os.Getenv("PORT")
	if port == "" {
		log.Fatal("PORT environment variable is required")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 {
		log.Fatalf("PORT must be a positive integer, got: %s", port)
	}
	return p
}
