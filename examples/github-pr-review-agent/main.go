package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/memory"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
)

func main() {
	startupStart := time.Now()

	cfg, err := LoadReviewConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	// Set component type BEFORE telemetry init so service_type labels are correct.
	core.SetCurrentComponentType(core.ComponentTypeAgent)

	serviceName := "github-pr-review-agent"
	if cfg.Mode != "" {
		serviceName = fmt.Sprintf("github-pr-review-agent-%s", cfg.Mode)
	}
	initTelemetry(serviceName)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = telemetry.Shutdown(ctx)
	}()

	redisClient, err := connectRedis(cfg.RedisURL)
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer func() { _ = redisClient.Close() }()

	taskQueue := orchestration.NewRedisTaskQueue(redisClient, nil)
	taskStore := orchestration.NewRedisTaskStore(redisClient, nil)

	// Shared agent memory (Phase 1 — episodic events). See AGENT_PLAN.md
	// "Shared Agent Memory" section for the full rationale. Phase 2
	// (knowledge extraction) is deliberately disabled: we're a producer of
	// events; devops-chat-agent's reflection job pulls them into shared
	// knowledge on its own cadence.
	//
	// NoOpLogger here because agent.Logger isn't constructed yet at this
	// point — memory setup failures surface via the returned error which we
	// log explicitly; success path doesn't need verbose internal logging.
	memBackends, memErr := memory.NewSharedBackends(redisClient, &core.NoOpLogger{},
		memory.WithAgentName("github-pr-review-agent"),
		memory.WithDomain("infrastructure"),
		memory.WithKnowledgeDisabled(),
	)
	if memErr != nil {
		log.Printf("Warning: shared memory unavailable: %v", memErr)
		// Continue anyway — review still works, just without cross-agent visibility.
	}
	if memBackends != nil {
		defer memBackends.Close()
	}

	switch cfg.Mode {
	case "api":
		runAPIMode(cfg, redisClient, taskQueue, taskStore, memBackends, startupStart)
	case "worker":
		runWorkerMode(cfg, redisClient, taskQueue, taskStore, memBackends, startupStart)
	default:
		runEmbeddedMode(cfg, redisClient, taskQueue, taskStore, memBackends, startupStart)
	}
}

func runAPIMode(
	cfg *ReviewConfig,
	redisClient *redis.Client,
	taskQueue core.TaskQueue,
	taskStore core.TaskStore,
	memBackends *memory.SharedBackends,
	startupStart time.Time,
) {
	agent, err := NewPRReviewAgent(redisClient, taskQueue, taskStore, cfg, memBackends)
	if err != nil {
		log.Fatalf("create agent: %v", err)
	}

	wireTaskAPI(agent, taskQueue, taskStore)

	fw := mustNewFramework(agent, cfg, "github-pr-review-agent-api")

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	installSignalHandler(agent, appCancel, "API")

	agent.Logger.Info("API ready", map[string]interface{}{
		"port":       cfg.Port,
		"startup_ms": time.Since(startupStart).Milliseconds(),
	})

	if err := fw.Run(appCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
	}
}

func runWorkerMode(
	cfg *ReviewConfig,
	redisClient *redis.Client,
	taskQueue core.TaskQueue,
	taskStore core.TaskStore,
	memBackends *memory.SharedBackends,
	startupStart time.Time,
) {
	agent, err := NewPRReviewAgent(redisClient, taskQueue, taskStore, cfg, memBackends)
	if err != nil {
		log.Fatalf("create agent: %v", err)
	}

	// Worker mode does not run the framework, so framework.Run() never wires
	// agent.BaseAgent.Discovery. Without that, every github-tool call would
	// fail at the discovery check in tool_client.go. Construct discovery
	// directly (matches event-driven-agent's runWorkerMode pattern) so
	// worker pods can resolve and call github-tool.
	discovery, derr := core.NewRedisDiscovery(cfg.RedisURL)
	if derr != nil {
		log.Fatalf("worker mode: create discovery: %v", derr)
	}
	agent.Discovery = discovery

	workerPool := mustWorkerPool(agent, cfg, taskQueue, taskStore)

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	healthServer := startHealthServer(cfg.Port, cfg.WorkerCount)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = healthServer.Shutdown(shutdownCtx)
	}()

	installSignalHandler(agent, workerCancel, "worker")

	agent.Logger.Info("Worker ready", map[string]interface{}{
		"workers":    cfg.WorkerCount,
		"startup_ms": time.Since(startupStart).Milliseconds(),
	})

	if err := workerPool.Start(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Worker pool failed: %v", err)
	}
}

func runEmbeddedMode(
	cfg *ReviewConfig,
	redisClient *redis.Client,
	taskQueue core.TaskQueue,
	taskStore core.TaskStore,
	memBackends *memory.SharedBackends,
	startupStart time.Time,
) {
	agent, err := NewPRReviewAgent(redisClient, taskQueue, taskStore, cfg, memBackends)
	if err != nil {
		log.Fatalf("create agent: %v", err)
	}

	wireTaskAPI(agent, taskQueue, taskStore)
	workerPool := mustWorkerPool(agent, cfg, taskQueue, taskStore)

	fw := mustNewFramework(agent, cfg, "github-pr-review-agent")

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	installSignalHandler(agent, appCancel, "embedded")

	go func() {
		if err := workerPool.Start(appCtx); err != nil && !errors.Is(err, context.Canceled) {
			agent.Logger.Error("Worker pool failed", map[string]interface{}{"error": err.Error()})
			appCancel()
		}
	}()

	agent.Logger.Info("Embedded ready", map[string]interface{}{
		"port":       cfg.Port,
		"workers":    cfg.WorkerCount,
		"startup_ms": time.Since(startupStart).Milliseconds(),
	})

	if err := fw.Run(appCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
	}
}

// --- shared wiring helpers ---

func wireTaskAPI(agent *PRReviewAgent, taskQueue core.TaskQueue, taskStore core.TaskStore) {
	taskAPI := orchestration.NewTaskAPIHandler(taskQueue, taskStore, agent.Logger)

	if err := agent.HandleFunc("/api/v1/tasks", taskAPI.HandleSubmit); err != nil {
		log.Fatalf("register task submit: %v", err)
	}
	if err := agent.HandleFunc("/api/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") && r.Method == http.MethodPost {
			taskAPI.HandleCancel(w, r)
			return
		}
		if r.Method == http.MethodGet {
			taskAPI.HandleGetTask(w, r)
			return
		}
		http.NotFound(w, r)
	}); err != nil {
		log.Fatalf("register task handler: %v", err)
	}
}

func mustWorkerPool(
	agent *PRReviewAgent,
	cfg *ReviewConfig,
	taskQueue core.TaskQueue,
	taskStore core.TaskStore,
) *orchestration.TaskWorkerPool {
	workerConfig := &orchestration.TaskWorkerConfig{
		WorkerCount:        cfg.WorkerCount,
		DequeueTimeout:     30 * time.Second,
		ShutdownTimeout:    60 * time.Second,
		DefaultTaskTimeout: cfg.TaskTimeout,
	}
	pool := orchestration.NewTaskWorkerPool(taskQueue, taskStore, workerConfig)
	if err := pool.RegisterHandler("review_pr", agent.HandlePullRequestReview); err != nil {
		log.Fatalf("register review_pr handler: %v", err)
	}
	pool.SetLogger(agent.Logger)
	return pool
}

func mustNewFramework(agent *PRReviewAgent, cfg *ReviewConfig, name string) *core.Framework {
	fw, err := core.NewFramework(agent.BaseAgent,
		core.WithName(name),
		core.WithPort(cfg.Port),
		core.WithNamespace(cfg.Namespace),
		core.WithRedisURL(cfg.RedisURL),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("github-pr-review-agent",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		log.Fatalf("create framework: %v", err)
	}
	return fw
}

func installSignalHandler(agent *PRReviewAgent, cancel context.CancelFunc, label string) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		agent.Logger.Info(fmt.Sprintf("Shutting down %s gracefully", label), nil)
		cancel()
	}()
}

// --- bootstrap helpers ---

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
		log.Printf("Warning: telemetry init failed: %v", err)
		return
	}
	telemetry.EnableFrameworkIntegration(nil)
}

func connectRedis(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(core.ApplyRedisClientDefaults(opts))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return client, nil
}

// startHealthServer provides a minimal /health and /ready for K8s probes
// when running in worker mode (where no core.Framework HTTP server exists).
func startHealthServer(port, workers int) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"healthy","mode":"worker","workers":%d}`, workers)
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("health server error: %v", err)
		}
	}()
	return srv
}
