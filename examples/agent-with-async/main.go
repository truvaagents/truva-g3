// Package main implements an Async Travel Research Agent that demonstrates
// the TruvaG3 async task system for long-running operations.
//
// This agent showcases:
//   - Async task submission with HTTP 202 + polling pattern
//   - Background worker pool for task processing
//   - Multi-tool orchestration (5 tools: geocoding, weather, news, currency, stock)
//   - Progress reporting during execution
//   - Distributed tracing across async boundaries
//   - Task cancellation support
//
// Deployment Modes (TRUVAG3_MODE env var):
//
//	api      - HTTP server only, enqueues tasks (workers run separately)
//	worker   - Task processing only, minimal /health endpoint
//	<empty>  - Embedded mode: API + workers in same process (local dev)
//
// Production Architecture (TRUVAG3_MODE=api and TRUVAG3_MODE=worker):
//
//	┌─────────────────────────────┐     ┌─────────────────────────────┐
//	│ async-travel-agent-api      │     │ async-travel-agent-worker   │
//	│ (TRUVAG3_MODE=api)           │     │ (TRUVAG3_MODE=worker)        │
//	├─────────────────────────────┤     ├─────────────────────────────┤
//	│ • POST /api/v1/tasks        │     │ • GET /health               │
//	│ • GET /api/v1/tasks/:id     │     │ • BRPOP from Redis queue    │
//	│ • Scale: HTTP request rate  │     │ • Scale: Redis queue depth  │
//	└──────────────┬──────────────┘     └──────────────┬──────────────┘
//	               │         ┌─────────────────┐       │
//	               └────────>│     Redis       │<──────┘
//	                         │  Task Queue     │
//	                         └─────────────────┘
//
// Local Development Architecture (TRUVAG3_MODE= or unset):
//
//	┌─────────────────────────────────────────────────────────────────┐
//	│                    Async Travel Research Agent                   │
//	├─────────────────────────────────────────────────────────────────┤
//	│  HTTP API + Background Workers (same process)                    │
//	└─────────────────────────────────────────────────────────────────┘
//
// Environment Variables:
//
//	TRUVAG3_MODE                    - Deployment mode: api, worker, or empty
//	REDIS_URL                      - Redis connection URL (required)
//	PORT                           - HTTP server port (default: 8098)
//	WORKER_COUNT                   - Number of background workers (default: 3)
//	NAMESPACE                      - Kubernetes namespace for service discovery
//	OPENAI_API_KEY                 - OpenAI API key for AI synthesis
//	APP_ENV                        - Environment: development, staging, production
//	OTEL_EXPORTER_OTLP_ENDPOINT    - OpenTelemetry collector endpoint
//
// Example Usage:
//
//	# Submit a travel research task
//	curl -X POST http://localhost:8098/api/v1/tasks \
//	  -H "Content-Type: application/json" \
//	  -d '{"type":"travel_research","input":{"destination":"Tokyo, Japan"}}'
//
//	# Poll for status
//	curl http://localhost:8098/api/v1/tasks/{task_id}
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"

	// Import AI providers for auto-detection
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
	startupStart := time.Now()

	// 1. Get deployment mode
	mode := os.Getenv("TRUVAG3_MODE") // "api", "worker", or "" (embedded)

	// 2. Validate configuration
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// 3. Set component type for telemetry
	core.SetCurrentComponentType(core.ComponentTypeAgent)

	// 4. Initialize telemetry
	serviceName := "async-travel-agent"
	if mode != "" {
		serviceName = fmt.Sprintf("async-travel-agent-%s", mode)
	}
	initTelemetry(serviceName)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			log.Printf("Warning: Telemetry shutdown error: %v", err)
		}
	}()

	// 5. Connect to Redis
	redisURL := os.Getenv("REDIS_URL")
	redisOpt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("Failed to parse REDIS_URL: %v", err)
	}
	redisClient := redis.NewClient(core.ApplyRedisClientDefaults(redisOpt))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisClient.Ping(ctx).Err(); err != nil {
		cancel()
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	cancel()
	log.Println("Connected to Redis")

	// 6. Create async task infrastructure
	taskQueue := orchestration.NewRedisTaskQueue(redisClient, nil)
	taskStore := orchestration.NewRedisTaskStore(redisClient, nil)

	// 7. Get port configuration
	port := 8098
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	// 8. Run based on deployment mode
	switch mode {
	case "api":
		runAPIMode(redisURL, redisClient, taskQueue, taskStore, port, startupStart)
	case "worker":
		runWorkerMode(redisClient, taskQueue, taskStore, port, startupStart)
	default:
		runEmbeddedMode(redisURL, redisClient, taskQueue, taskStore, port, startupStart)
	}
}

// runAPIMode runs HTTP API server only (no workers)
// Workers are deployed separately with TRUVAG3_MODE=worker
func runAPIMode(redisURL string, redisClient *redis.Client, taskQueue *orchestration.RedisTaskQueue, taskStore *orchestration.RedisTaskStore, port int, startupStart time.Time) {
	log.Println("Starting in API mode (HTTP server only, workers run separately)")

	// Create the agent
	agent, err := NewAsyncTravelAgent(redisClient)
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// Create Task API handler
	taskAPI := orchestration.NewTaskAPIHandler(taskQueue, taskStore, agent.Logger)

	// Register task API handlers
	if err := agent.HandleFunc("/api/v1/tasks", taskAPI.HandleSubmit); err != nil {
		log.Fatalf("Failed to register task submit handler: %v", err)
	}
	if err := agent.HandleFunc("/api/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") && r.Method == "POST" {
			taskAPI.HandleCancel(w, r)
		} else if r.Method == "GET" {
			taskAPI.HandleGetTask(w, r)
		}
	}); err != nil {
		log.Fatalf("Failed to register task handler: %v", err)
	}

	// Create framework (HTTP server only)
	framework, err := core.NewFramework(agent.BaseAgent,
		core.WithName("async-travel-agent-api"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(redisURL),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("async-travel-agent-api", &telemetry.TracingMiddlewareConfig{
			ExcludedPaths: []string{"/health", "/ready", "/metrics", "/api/capabilities"},
		})),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// Log startup
	startupDuration := time.Since(startupStart)
	agent.Logger.Info("Async Travel Agent API started", map[string]interface{}{
		"mode":        "api",
		"port":        port,
		"ai_provider": getAIProviderStatus(),
		"startup_ms":  startupDuration.Milliseconds(),
	})

	// Graceful shutdown
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		agent.Logger.Info("Shutting down API gracefully", nil)
		appCancel()
	}()

	// Run framework (HTTP server)
	if err := framework.Run(appCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
	}

	agent.Logger.Info("API shutdown complete", nil)
}

// runWorkerMode runs task workers only with minimal health endpoint
// API is deployed separately with TRUVAG3_MODE=api
func runWorkerMode(redisClient *redis.Client, taskQueue *orchestration.RedisTaskQueue, taskStore *orchestration.RedisTaskStore, port int, startupStart time.Time) {
	log.Println("Starting in Worker mode (task processing only, API runs separately)")

	// Create worker pool configuration
	workerCount := 3
	if wc := os.Getenv("WORKER_COUNT"); wc != "" {
		if w, err := strconv.Atoi(wc); err == nil && w > 0 {
			workerCount = w
		}
	}

	workerConfig := &orchestration.TaskWorkerConfig{
		WorkerCount:        workerCount,
		DequeueTimeout:     30 * time.Second,
		ShutdownTimeout:    60 * time.Second,
		DefaultTaskTimeout: 10 * time.Minute,
	}

	workerPool := orchestration.NewTaskWorkerPool(taskQueue, taskStore, workerConfig)

	// Create the agent for task handling
	agent, err := NewAsyncTravelAgent(redisClient)
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// Register task handlers
	workerPool.RegisterHandler("query", agent.HandleQuery)
	workerPool.RegisterHandler("travel_research", agent.HandleLegacyTravelResearch)
	workerPool.SetLogger(agent.Logger)

	// Initialize AI orchestrator (workers need it for task execution)
	// Create a discovery client directly for worker mode
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		log.Fatalf("REDIS_URL environment variable is required for worker mode")
	}
	discovery, err := core.NewRedisDiscovery(redisURL)
	if err != nil {
		log.Printf("Warning: Failed to create discovery: %v (AI orchestration will be disabled)", err)
	} else {
		if err := agent.InitializeOrchestrator(discovery); err != nil {
			log.Printf("Warning: Failed to initialize orchestrator: %v (AI orchestration will be disabled)", err)
		} else {
			agent.Logger.Info("AI orchestrator initialized", nil)
		}
	}

	// Start minimal health server for K8s probes
	healthServer := &http.Server{
		Addr: fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" || r.URL.Path == "/ready" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, `{"status":"healthy","mode":"worker","workers":%d}`, workerCount)
			} else if r.URL.Path == "/metrics" {
				// Metrics are sent via OTLP to the collector
				// This endpoint returns minimal info for Prometheus annotation compatibility
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "# Metrics exported via OTLP to OTel Collector\n# Worker mode: %d workers\n", workerCount)
			} else {
				http.NotFound(w, r)
			}
		}),
	}

	// Start health server in background
	go func() {
		agent.Logger.Info("Starting health server", map[string]interface{}{"port": port})
		if err := healthServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Health server error: %v", err)
		}
	}()

	// Log startup
	startupDuration := time.Since(startupStart)
	agent.Logger.Info("Async Travel Agent Worker started", map[string]interface{}{
		"mode":         "worker",
		"worker_count": workerCount,
		"ai_provider":  getAIProviderStatus(),
		"startup_ms":   startupDuration.Milliseconds(),
	})

	// Graceful shutdown
	workerCtx, workerCancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		agent.Logger.Info("Shutting down worker gracefully", nil)

		// Stop health server
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		healthServer.Shutdown(shutdownCtx)

		// Stop workers
		workerCancel()
	}()

	// Run worker pool (blocking)
	if err := workerPool.Start(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("Worker pool error: %v", err)
	}

	agent.Logger.Info("Worker shutdown complete", nil)
}

// runEmbeddedMode runs both API and workers in the same process
// This is the default for local development
func runEmbeddedMode(redisURL string, redisClient *redis.Client, taskQueue *orchestration.RedisTaskQueue, taskStore *orchestration.RedisTaskStore, port int, startupStart time.Time) {
	log.Println("Starting in Embedded mode (API + workers in same process)")

	// Create worker pool configuration
	workerCount := 3
	if wc := os.Getenv("WORKER_COUNT"); wc != "" {
		if w, err := strconv.Atoi(wc); err == nil && w > 0 {
			workerCount = w
		}
	}

	workerConfig := &orchestration.TaskWorkerConfig{
		WorkerCount:        workerCount,
		DequeueTimeout:     30 * time.Second,
		ShutdownTimeout:    60 * time.Second,
		DefaultTaskTimeout: 10 * time.Minute,
	}

	workerPool := orchestration.NewTaskWorkerPool(taskQueue, taskStore, workerConfig)

	// Create the agent
	agent, err := NewAsyncTravelAgent(redisClient)
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// Register task handlers
	workerPool.RegisterHandler("query", agent.HandleQuery)
	workerPool.RegisterHandler("travel_research", agent.HandleLegacyTravelResearch)
	workerPool.SetLogger(agent.Logger)

	// Create Task API handler
	taskAPI := orchestration.NewTaskAPIHandler(taskQueue, taskStore, agent.Logger)

	// Register task API handlers
	if err := agent.HandleFunc("/api/v1/tasks", taskAPI.HandleSubmit); err != nil {
		log.Fatalf("Failed to register task submit handler: %v", err)
	}
	if err := agent.HandleFunc("/api/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") && r.Method == "POST" {
			taskAPI.HandleCancel(w, r)
		} else if r.Method == "GET" {
			taskAPI.HandleGetTask(w, r)
		}
	}); err != nil {
		log.Fatalf("Failed to register task handler: %v", err)
	}

	// Create framework
	framework, err := core.NewFramework(agent.BaseAgent,
		core.WithName("async-travel-agent"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(redisURL),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("async-travel-agent", &telemetry.TracingMiddlewareConfig{
			ExcludedPaths: []string{"/health", "/ready", "/metrics", "/api/capabilities"},
		})),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// Initialize AI orchestrator in background (Discovery is set during framework.Run())
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
				"error": err.Error(),
			})
		} else {
			agent.Logger.Info("AI orchestrator initialized successfully", nil)
		}
	}()

	// Start worker pool in background
	workerCtx, workerCancel := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		agent.Logger.Info("Starting worker pool", map[string]interface{}{
			"worker_count": workerCount,
		})
		if err := workerPool.Start(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
			agent.Logger.Error("Worker pool error", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	// Log startup
	startupDuration := time.Since(startupStart)
	agent.Logger.Info("Async Travel Agent started (embedded)", map[string]interface{}{
		"mode":         "embedded",
		"port":         port,
		"worker_count": workerCount,
		"ai_provider":  getAIProviderStatus(),
		"startup_ms":   startupDuration.Milliseconds(),
	})

	// Graceful shutdown
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		agent.Logger.Info("Shutting down gracefully", nil)

		// Stop accepting new tasks
		workerCancel()

		// Wait for workers to finish (with timeout)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer shutdownCancel()

		select {
		case <-workerDone:
			agent.Logger.Info("Worker pool stopped", nil)
		case <-shutdownCtx.Done():
			agent.Logger.Warn("Worker shutdown timeout", nil)
		}

		appCancel()
	}()

	// Run framework
	if err := framework.Run(appCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
	}

	// Wait for worker pool cleanup
	<-workerDone
	agent.Logger.Info("Shutdown complete", nil)
}

func validateConfig() error {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return fmt.Errorf("REDIS_URL environment variable required")
	}
	if !strings.HasPrefix(redisURL, "redis://") && !strings.HasPrefix(redisURL, "rediss://") {
		return fmt.Errorf("invalid REDIS_URL format")
	}
	return nil
}

func getAIProviderStatus() string {
	providers := []struct {
		name   string
		envVar string
	}{
		{"OpenAI", "OPENAI_API_KEY"},
		{"Anthropic", "ANTHROPIC_API_KEY"},
		{"Groq", "GROQ_API_KEY"},
	}
	for _, p := range providers {
		if os.Getenv(p.envVar) != "" {
			return p.name
		}
	}
	return "None"
}

func initTelemetry(serviceName string) {
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}

	var profile telemetry.Profile
	switch env {
	case "production", "prod":
		profile = telemetry.ProfileProduction
	case "staging":
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
		log.Printf("Warning: Telemetry init failed: %v", err)
		return
	}

	telemetry.EnableFrameworkIntegration(nil)
	log.Printf("Telemetry initialized: %s (%s)", serviceName, env)
}
