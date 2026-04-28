package main

import (
	"context"
	"encoding/json"
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
	serviceName := "event-driven-agent"
	if mode != "" {
		serviceName = fmt.Sprintf("event-driven-agent-%s", mode)
	}
	initTelemetry(serviceName)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		telemetry.Shutdown(ctx)
	}()

	// 5. Connect to Redis
	redisURL := os.Getenv("REDIS_URL")
	redisOpt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("Failed to parse REDIS_URL: %v", err)
	}
	redisClient := redis.NewClient(redisOpt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisClient.Ping(ctx).Err(); err != nil {
		cancel()
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	cancel()
	fmt.Println("Connected to Redis") // Pre-framework: agent not created yet

	// 6. Create async task infrastructure
	// Queue key is auto-namespaced from TRUVAG3_K8S_SERVICE_NAME by the framework
	// (RC1 fix) — no hardcoded key needed here.
	taskQueue := orchestration.NewRedisTaskQueue(redisClient, nil)
	taskStore := orchestration.NewRedisTaskStore(redisClient, nil)

	// 7. Switch on deployment mode
	port := getPort()
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
	agent, err := NewEventDrivenAgent(redisClient)
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

	// HITL endpoint registration (query + command + resume — no orchestrator needed)
	// The worker creates checkpoints in Redis DB 6; the API surfaces them to the UI.
	hitlConfig := orchestration.DefaultConfig().HITL
	var hitl *HITLInfrastructure
	if hitlConfig.Enabled {
		hitl, err = SetupHITL(agent.Logger, hitlConfig)
		if err != nil {
			agent.Logger.Error("HITL setup failed", map[string]interface{}{
				"operation": "startup",
				"error":     err.Error(),
			})
			os.Exit(1)
		}
		defer hitl.Close()

		hitlHandler := orchestration.NewHITLHandler(
			hitl.Controller,
			hitl.CheckpointStore,
			orchestration.WithHITLHandlerLogger(agent.Logger),
		)

		// Query endpoints (UI polling)
		if err := agent.HandleFunc("/hitl/checkpoints", hitlHandler.HandleListCheckpoints); err != nil {
			log.Fatalf("Failed to register HITL checkpoints handler: %v", err)
		}
		if err := agent.HandleFunc("/hitl/checkpoints/", hitlHandler.HandleGetCheckpoint); err != nil {
			log.Fatalf("Failed to register HITL checkpoint detail handler: %v", err)
		}
		// Approval/rejection commands (publishes to Redis Pub/Sub → worker's WaitForCommand)
		if err := agent.HandleFunc("/hitl/command", hitlHandler.HandleCommand); err != nil {
			log.Fatalf("Failed to register HITL command handler: %v", err)
		}

		// Resume endpoint — enqueues a resume task for the worker (Option B: no orchestrator on API)
		if err := agent.HandleFunc("/hitl/resume/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed, use POST", http.StatusMethodNotAllowed)
				return
			}

			// Extract checkpoint ID from path: /hitl/resume/{checkpoint_id}
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
			if len(parts) < 3 || parts[2] == "" {
				http.Error(w, "checkpoint_id required in path", http.StatusBadRequest)
				return
			}
			checkpointID := parts[2]

			// Load and validate checkpoint
			cp, loadErr := hitl.CheckpointStore.LoadCheckpoint(r.Context(), checkpointID)
			if loadErr != nil {
				http.Error(w, "checkpoint not found: "+loadErr.Error(), http.StatusNotFound)
				return
			}

			// Compliance §5: Handle expired checkpoints explicitly
			if strings.HasPrefix(string(cp.Status), "expired") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusGone) // 410
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error":  "checkpoint expired",
					"status": string(cp.Status),
				})
				return
			}

			if cp.Status != orchestration.CheckpointStatusApproved &&
				cp.Status != orchestration.CheckpointStatusPending {
				http.Error(w, fmt.Sprintf("checkpoint status is %s, cannot resume", cp.Status),
					http.StatusConflict)
				return
			}

			// Create resume task for worker to pick up
			resumeTask := &core.Task{
				ID:     fmt.Sprintf("hitl-resume-%s-%d", checkpointID, time.Now().UnixMilli()),
				Type:   "hitl_resume",
				Status: core.TaskStatusQueued,
				Input: map[string]interface{}{
					"checkpoint_id":  checkpointID,
					"request_id":     cp.RequestID,
					"approved_by":    r.Header.Get("X-User-ID"),
					"trace_id":       cp.OriginalTraceID,
					"parent_span_id": cp.OriginalSpanID,
				},
				CreatedAt: time.Now(),
			}

			if enqErr := taskQueue.Enqueue(r.Context(), resumeTask); enqErr != nil {
				http.Error(w, "failed to enqueue resume task: "+enqErr.Error(),
					http.StatusInternalServerError)
				return
			}

			agent.Logger.InfoWithContext(r.Context(), "HITL resume task enqueued", map[string]interface{}{
				"operation":     "hitl_resume_enqueue",
				"checkpoint_id": checkpointID,
				"task_id":       resumeTask.ID,
				"approved_by":   r.Header.Get("X-User-ID"),
			})

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "enqueued",
				"task_id": resumeTask.ID,
				"message": "Resume task enqueued for worker processing",
			})
		}); err != nil {
			log.Fatalf("Failed to register HITL resume handler: %v", err)
		}

		// RC2: Webhook receive endpoint (worker POSTs here when HITL triggers)
		// Just ACK — the checkpoint is already in Redis DB 6. UI discovers via polling.
		if err := agent.HandleFunc("/internal/hitl-webhook", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"received"}`))
		}); err != nil {
			log.Fatalf("Failed to register HITL webhook handler: %v", err)
		}

		agent.Logger.Info("HITL endpoints registered (API mode)", map[string]interface{}{
			"endpoints": []string{"/hitl/checkpoints", "/hitl/checkpoints/{id}", "/hitl/command", "/hitl/resume/{id}", "/internal/hitl-webhook"},
		})
	}

	// Create framework (HTTP server only)
	fw, err := core.NewFramework(agent.BaseAgent,
		core.WithName("event-driven-agent-api"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(redisURL),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		// Distributed tracing middleware — excludes health endpoints to reduce noise
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("event-driven-agent",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// Log startup
	startupDuration := time.Since(startupStart)
	agent.Logger.Info("Event-Driven Agent API started", map[string]interface{}{
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

	// Register scheduled endpoint before Run — HandleFunc rejects after server starts.
	if err := orchestration.RegisterScheduledEndpoint(agent.BaseAgent, func() orchestration.Orchestrator {
		if o := agent.GetOrchestrator(); o != nil {
			return o
		}
		return nil
	}); err != nil {
		agent.Logger.Warn("Failed to register scheduled endpoint", map[string]interface{}{"error": err.Error()})
	}

	// Run framework (HTTP server)
	if err := fw.Run(appCtx); err != nil && !errors.Is(err, context.Canceled) {
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

	defaultWorkerConfig := orchestration.DefaultTaskWorkerConfig()
	defaultWorkerConfig.WorkerCount = workerCount
	defaultWorkerConfig.ShutdownTimeout = 60 * time.Second

	workerPool := orchestration.NewTaskWorkerPool(taskQueue, taskStore, &defaultWorkerConfig)

	// Create the agent for task handling
	agent, err := NewEventDrivenAgent(redisClient)
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// Register task handlers
	if err := workerPool.RegisterHandler("alert_investigation", agent.HandleAlertInvestigation); err != nil {
		log.Fatalf("Failed to register alert_investigation handler: %v", err)
	}
	if err := workerPool.RegisterHandler("hitl_resume", agent.HandleHITLResumeTask); err != nil {
		log.Fatalf("Failed to register hitl_resume handler: %v", err)
	}
	workerPool.SetLogger(agent.Logger)

	// memBackends lifted to function scope so reflection job can use it after workerCtx exists
	var memBackends *memory.SharedBackends

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
		// HITL configuration from environment variables
		hitlConfig := orchestration.DefaultConfig().HITL
		var hitl *HITLInfrastructure
		if hitlConfig.Enabled {
			hitl, err = SetupHITL(agent.Logger, hitlConfig)
			if err != nil {
				agent.Logger.Error("HITL setup failed", map[string]interface{}{
					"operation": "startup",
					"error":     err.Error(),
				})
				os.Exit(1)
			}
			defer hitl.Close()
		}

		// Setup shared agent memory
		var memErr error
		memBackends, memErr = setupMemoryBackends(redisClient, agent)
		if memErr != nil {
			agent.Logger.Warn("Shared memory setup failed, running without cross-agent memory", map[string]interface{}{
				"error": memErr.Error(),
			})
		}
		if memBackends != nil {
			defer memBackends.Close()
		}
		var memoryHooks []core.PipelineHook
		var activityCoord core.ActivityCoordinator
		if memBackends != nil {
			memoryHooks, activityCoord = orchestration.BuildMemoryHooks(memBackends.ToDeps(), agent.AI, agent.Logger)
		}

		if err := agent.InitializeOrchestrator(discovery, hitl, hitlConfig, memoryHooks, activityCoord); err != nil {
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
	agent.Logger.Info("Event-Driven Agent Worker started", map[string]interface{}{
		"mode":         "worker",
		"worker_count": workerCount,
		"ai_provider":  getAIProviderStatus(),
		"startup_ms":   startupDuration.Milliseconds(),
	})

	// Graceful shutdown
	workerCtx, workerCancel := context.WithCancel(context.Background())

	// Reflection Job: bridge episodic events to long-term knowledge.
	// Worker mode has no core.Framework, so we run the Runnable in a goroutine
	// directly tied to workerCtx. Shutdown is driven by ctx cancellation.
	// Returns nil when Phase 2 backends (Qdrant + embedder) are unavailable.
	if reflectionJob, _ := memory.BuildReflectionJob(memBackends.ToDeps(), agent.AI, agent.Logger); reflectionJob != nil {
		go func() {
			if err := reflectionJob.Start(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
				agent.Logger.Error("Reflection job exited with error", map[string]interface{}{
					"operation":  "worker_reflection",
					"error":      err.Error(),
					"error_type": "runnable_exit",
				})
			}
		}()
	}

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

	// Start alert queue consumer (bridges alert queue → task queue)
	consumer := NewAlertQueueConsumer(redisClient, taskQueue, agent.Logger)
	go func() {
		if err := consumer.Start(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
			agent.Logger.Error("Alert queue consumer error", map[string]interface{}{
				"error": err.Error(),
			})
		}
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

	defaultWorkerConfig := orchestration.DefaultTaskWorkerConfig()
	defaultWorkerConfig.WorkerCount = workerCount
	defaultWorkerConfig.ShutdownTimeout = 60 * time.Second

	workerPool := orchestration.NewTaskWorkerPool(taskQueue, taskStore, &defaultWorkerConfig)

	// Create the agent
	agent, err := NewEventDrivenAgent(redisClient)
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// Register task handlers
	if err := workerPool.RegisterHandler("alert_investigation", agent.HandleAlertInvestigation); err != nil {
		log.Fatalf("Failed to register alert_investigation handler: %v", err)
	}
	if err := workerPool.RegisterHandler("hitl_resume", agent.HandleHITLResumeTask); err != nil {
		log.Fatalf("Failed to register hitl_resume handler: %v", err)
	}
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

	// HITL configuration from environment variables
	hitlConfig := orchestration.DefaultConfig().HITL
	var hitl *HITLInfrastructure
	if hitlConfig.Enabled {
		hitl, err = SetupHITL(agent.Logger, hitlConfig)
		if err != nil {
			agent.Logger.Error("HITL setup failed", map[string]interface{}{
				"operation": "startup",
				"error":     err.Error(),
			})
			os.Exit(1)
		}
		defer hitl.Close()

		// Register HITL API routes for approval/rejection via HTTP
		hitlHandler := orchestration.NewHITLHandler(
			hitl.Controller,
			hitl.CheckpointStore,
			orchestration.WithHITLHandlerLogger(agent.Logger),
		)
		if err := agent.HandleFunc("/hitl/command", hitlHandler.HandleCommand); err != nil {
			log.Fatalf("Failed to register HITL command handler: %v", err)
		}
		if err := agent.HandleFunc("/hitl/checkpoints", hitlHandler.HandleListCheckpoints); err != nil {
			log.Fatalf("Failed to register HITL checkpoints handler: %v", err)
		}
		if err := agent.HandleFunc("/hitl/checkpoints/", hitlHandler.HandleGetCheckpoint); err != nil {
			log.Fatalf("Failed to register HITL checkpoint detail handler: %v", err)
		}
		if err := agent.HandleFunc("/hitl/resume/", agent.HandleHITLResume); err != nil {
			log.Fatalf("Failed to register HITL resume handler: %v", err)
		}
		agent.Logger.Info("HITL API routes registered", map[string]interface{}{
			"endpoints": []string{"/hitl/command", "/hitl/checkpoints", "/hitl/checkpoints/{id}", "/hitl/resume/{id}"},
		})
	}

	// Create framework
	fw, err := core.NewFramework(agent.BaseAgent,
		core.WithName("event-driven-agent"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(redisURL),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		// Distributed tracing middleware — excludes health endpoints to reduce noise
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("event-driven-agent",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// Setup shared agent memory (before orchestrator init)
	memBackends, memErr := setupMemoryBackends(redisClient, agent)
	if memErr != nil {
		agent.Logger.Warn("Shared memory setup failed, running without cross-agent memory", map[string]interface{}{
			"error": memErr.Error(),
		})
	}
	if memBackends != nil {
		defer memBackends.Close()
	}

	// Initialize AI orchestrator in background (Discovery is set during framework.Run())
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

	// Start alert queue consumer (bridges alert queue → task queue)
	consumer := NewAlertQueueConsumer(redisClient, taskQueue, agent.Logger)
	go func() {
		if err := consumer.Start(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
			agent.Logger.Error("Alert queue consumer error", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	// Log startup
	startupDuration := time.Since(startupStart)
	agent.Logger.Info("Event-Driven Agent started (embedded)", map[string]interface{}{
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

	// Reflection Job: bridge episodic events to long-term knowledge.
	// Layer 1 wiring — framework manages lifecycle via Runnable interface.
	// Returns nil when Phase 2 backends (Qdrant + embedder) are unavailable.
	if reflectionJob, _ := memory.BuildReflectionJob(memBackends.ToDeps(), agent.AI, agent.Logger); reflectionJob != nil {
		fw.RegisterRunnable(reflectionJob)
	}

	// Register scheduled endpoint before Run — HandleFunc rejects after server starts.
	if err := orchestration.RegisterScheduledEndpoint(agent.BaseAgent, func() orchestration.Orchestrator {
		if o := agent.GetOrchestrator(); o != nil {
			return o
		}
		return nil
	}); err != nil {
		agent.Logger.Warn("Failed to register scheduled endpoint", map[string]interface{}{"error": err.Error()})
	}

	// Run framework
	if err := fw.Run(appCtx); err != nil && !errors.Is(err, context.Canceled) {
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

func getPort() int {
	portStr := os.Getenv("PORT")
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			return p
		}
	}
	return 8372
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

// setupMemoryBackends creates shared memory backends using NewSharedBackends + Phase 2 embedding.
// Used by both worker and embedded modes to avoid duplication.
func setupMemoryBackends(redisClient *redis.Client, agent *EventDrivenAgent) (*memory.SharedBackends, error) {
	// Phase 2: create embedding client if configured (memory module can't import ai)
	var embedOpt memory.SharedBackendsOption
	embedder, err := ai.NewEmbeddingClient(ai.WithEmbeddingLogger(agent.Logger))
	if err == nil && embedder != nil {
		embedOpt = memory.WithEmbeddingClient(embedder)
	}

	opts := []memory.SharedBackendsOption{
		memory.WithAgentName("event-driven-agent"),
		memory.WithDomain("infrastructure"),
	}
	if embedOpt != nil {
		opts = append(opts, embedOpt)
	}

	return memory.NewSharedBackends(redisClient, agent.Logger, opts...)
}
