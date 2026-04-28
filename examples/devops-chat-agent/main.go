package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/memory"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"

	// Import AI providers for auto-detection
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
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
	initTelemetry("devops-chat-agent")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		telemetry.Shutdown(ctx)
	}()

	// 4. Create agent AFTER telemetry is initialized
	agent, err := NewDevOpsChatAgent()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// 4a. Conditionally initialize HITL infrastructure
	hitlConfig := orchestration.DefaultConfig().HITL
	var hitl *HITLInfrastructure
	if hitlConfig.Enabled {
		var hitlErr error
		hitl, hitlErr = SetupHITL(agent.Logger, hitlConfig)
		if hitlErr != nil {
			log.Fatalf("HITL setup failed: %v", hitlErr)
		}
		defer hitl.Close()
		log.Printf("HITL enabled: step_sensitive_capabilities=%v", hitlConfig.StepSensitiveCapabilities)
	}

	// 5. Create framework with tracing middleware
	middlewareConfig := &telemetry.TracingMiddlewareConfig{
		ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live", "/api/capabilities"},
		RequestFilter: func(r *http.Request) bool {
			return r.URL.Query().Get("poll") != "true"
		},
	}

	framework, err := core.NewFramework(agent,
		core.WithName("devops-chat-agent"),
		core.WithPort(getPort()),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORSDefaults(), // Allows all headers including X-Truvag3-Original-Request-ID
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("devops-chat-agent", middlewareConfig)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// 5a. Register HITL endpoints (before framework.Run starts serving)
	if hitl != nil {
		hitlHandler := orchestration.NewHITLHandler(
			hitl.Controller,
			hitl.CheckpointStore,
			orchestration.WithHITLHandlerLogger(agent.Logger),
		)
		agent.RegisterHITLCapabilities(hitlHandler)
	}

	// 6. Setup shared agent memory (episodic events, investigation coordination, knowledge)
	// Phase 1 (Redis): episodic + coordination + activity signals + digest cache
	// Phase 2 (Qdrant + embeddings): knowledge search + extraction — requires WithEmbeddingClient
	var memBackends *memory.SharedBackends
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		redisOpt, err := redis.ParseURL(redisURL)
		if err == nil {
			redisClient := redis.NewClient(redisOpt)

			// Phase 2: create embedding client if configured (memory module can't import ai)
			var embedOpt memory.SharedBackendsOption
			embedder, embErr := ai.NewEmbeddingClient(ai.WithEmbeddingLogger(agent.Logger))
			if embErr == nil && embedder != nil {
				embedOpt = memory.WithEmbeddingClient(embedder)
			}

			opts := []memory.SharedBackendsOption{
				memory.WithAgentName("devops-chat-agent"),
				memory.WithDomain("infrastructure"),
			}
			if embedOpt != nil {
				opts = append(opts, embedOpt)
			}

			memBackends, err = memory.NewSharedBackends(redisClient, agent.Logger, opts...)
			if err != nil {
				agent.Logger.Warn("Shared memory setup failed, running without cross-agent memory", map[string]interface{}{
					"error": err.Error(),
				})
			}
		}
	}
	if memBackends != nil {
		defer memBackends.Close()
	}

	// 7. Initialize orchestrator in background (Discovery is set during framework.Run())
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

		// Discovery is available, build memory hooks and initialize orchestrator
		var memoryHooks []core.PipelineHook
		var activityCoord core.ActivityCoordinator
		if memBackends != nil {
			memoryHooks, activityCoord = orchestration.BuildMemoryHooks(memBackends.ToDeps(), agent.AI, agent.Logger)
		}
		if err := agent.InitializeOrchestrator(agent.BaseAgent.Discovery, hitl, hitlConfig, memoryHooks, activityCoord); err != nil {
			agent.Logger.Warn("Failed to initialize orchestrator", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			agent.Logger.Info("Orchestrator initialized successfully", nil)
		}
	}()

	// Register scheduled endpoint BEFORE framework.Run() — HandleFunc rejects
	// registrations after the HTTP server starts. Orchestrator resolved lazily.
	if err := orchestration.RegisterScheduledEndpoint(agent.BaseAgent, func() orchestration.Orchestrator {
		if o := agent.GetOrchestrator(); o != nil {
			return o
		}
		return nil
	}); err != nil {
		agent.Logger.Warn("Failed to register scheduled endpoint", map[string]interface{}{"error": err.Error()})
	}

	// 8. Emit startup metrics
	telemetry.Counter("devops_chat_agent.startup", "status", "success", "module", "devops-chat-agent")

	// 9. Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		agent.Logger.Info("Received shutdown signal", map[string]interface{}{
			"signal": sig.String(),
		})
		cancel()
	}()

	// 9b. Reflection Job: bridge episodic events to long-term knowledge
	// Layer 1 wiring — framework manages lifecycle via Runnable interface.
	// Returns nil when Phase 2 backends (Qdrant + embedder) are unavailable.
	if reflectionJob, _ := memory.BuildReflectionJob(memBackends.ToDeps(), agent.AI, agent.Logger); reflectionJob != nil {
		framework.RegisterRunnable(reflectionJob)
	}

	// 10. Run the framework
	agent.Logger.Info("Starting devops-chat-agent", map[string]interface{}{
		"port": getPort(),
	})
	if err := framework.Run(ctx); err != nil && err != context.Canceled {
		agent.Logger.Error("Framework error", map[string]interface{}{
			"error": err.Error(),
		})
		os.Exit(1)
	}

	// 11. Drain LLM debug recordings before releasing the Redis connection.
	// Ordering: framework.Run has already returned (ctx cancelled → runnables stopped,
	// orchestrator LLM calls done), so no new async recordings will be spawned.
	// Shutdown waits for any in-flight async recordings to complete, then Close
	// releases the recorder's dedicated Redis connection.
	// Both fields are nil when TRUVAG3_LLM_DEBUG_ENABLED != "true" — the nil checks
	// keep the shutdown path uniform in both cases.
	if agent.instrumentedClient != nil {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := agent.instrumentedClient.Shutdown(drainCtx); err != nil {
			agent.Logger.Warn("LLM debug recorder drain timed out", map[string]interface{}{
				"error": err.Error(),
			})
		}
		drainCancel()
	}
	if agent.debugRecorder != nil {
		if err := agent.debugRecorder.Close(); err != nil {
			agent.Logger.Warn("LLM debug recorder close failed", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	agent.Logger.Info("Shutdown completed", nil)
}

// validateConfig validates required configuration
func validateConfig() error {
	// At least one AI provider key is required
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Println("Warning: No AI provider API key found. Set OPENAI_API_KEY or ANTHROPIC_API_KEY")
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
