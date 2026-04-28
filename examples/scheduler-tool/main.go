// Package main — scheduler-tool example application.
//
// scheduler-tool is a standard Truva-G3 BaseTool that exposes scheduling as
// 5 discoverable capabilities (schedule_task, list_schedules, get_schedule,
// update_schedule, cancel_schedule). Any agent's LLM that sees the tool in
// its service catalog can include these in its plan — the scheduling API
// is just another tool, same pattern as jira-tool or slack-tool.
//
// Internally this main.go wires three framework modules together:
//
//   - scheduler module: provides ScheduleStore and TaskDispatcher
//     (RedisScheduleStore + RedisTaskDispatcher in production)
//
//   - memory module: provides the DistributedLock for Scheduler leader election
//     (memory.RedisDistributedLock — reused, not duplicated in scheduler module)
//
//   - orchestration module: provides the Scheduler Runnable (tick loop) and
//     the capability handlers that the BaseTool exposes over HTTP
//
// The three modules depend only on core interfaces. This main.go is the
// only file in the system that knows all three exist simultaneously —
// classic framework composition: each module creates primitives, the
// application assembles them.
//
// Required environment variables:
//
//	REDIS_URL                         — e.g. redis://localhost:6379
//	PORT                              — HTTP server port (default: 9010)
//	TRUVAG3_K8S_SERVICE_NAME           — this tool's service identity
//
// Optional environment variables:
//
//	TRUVAG3_SCHEDULER_TICK_INTERVAL    — how often the Scheduler polls (default 5s)
//	TRUVAG3_SCHEDULER_LOCK_TTL         — distributed lock TTL (default 30s)
//	OTEL_EXPORTER_OTLP_ENDPOINT       — telemetry endpoint
//	APP_ENV                           — development | staging | production
//	NAMESPACE                         — framework-level namespace for discovery

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
	"github.com/truvaagents/truva-g3/memory"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	defaultPort = 9010
	serviceName = "scheduler-tool"
)

func main() {
	// 1. Validate configuration up-front (fail fast on missing env vars).
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// 2. Mark this component as a Tool for telemetry labelling.
	core.SetCurrentComponentType(core.ComponentTypeTool)

	// 3. Initialize telemetry BEFORE creating the BaseTool so metrics emitted
	//    during tool construction land in the correct registry.
	initTelemetry(serviceName)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		telemetry.Shutdown(ctx)
	}()

	// 4. Create the BaseTool. Its Logger field starts as NoOpLogger and is
	//    swapped in-place to a ProductionLogger by core.NewFramework
	//    (applyConfigToComponent in core/agent.go). Per
	//    docs/LOGGING_IMPLEMENTATION_GUIDE.md §7, any component that needs to
	//    LOG must read tool.Logger AFTER core.NewFramework returns —
	//    otherwise it captures the silent NoOpLogger.
	tool := core.NewTool(serviceName)

	// 5. Connect to Redis. We construct the client ourselves here (rather
	//    than letting the framework do it) because the scheduler module and
	//    the memory module both need a redis.Cmdable.
	redisURL := os.Getenv("REDIS_URL")
	redisClient, err := connectRedis(redisURL)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer redisClient.Close()

	// 6. Assemble the framework FIRST, before constructing any components
	//    that capture tool.Logger. This ordering is non-negotiable per the
	//    logging guide — see step 4 comment for details.
	port := resolvePort()
	framework, err := core.NewFramework(tool,
		core.WithName(serviceName),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(redisURL),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		// Tracing middleware — exclude health/capabilities endpoints to
		// reduce span noise, same convention as other example tools.
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig(serviceName,
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// At this point tool.Logger is the real ProductionLogger.
	tool.Logger.Info("Connected to Redis", map[string]interface{}{
		"operation": "startup",
		"redis_url": redactRedisURL(redisURL),
	})

	// 7. Build the scheduler backends from the orchestration module.
	//    These are the vendor-specific ScheduleStore + TaskDispatcher
	//    implementations.
	backends, err := orchestration.NewRedisSchedulerBackends(redisClient)
	if err != nil {
		log.Fatalf("Failed to create scheduler backends: %v", err)
	}

	// 8. Build the distributed lock from the existing memory peer module.
	//    We reuse memory.RedisDistributedLock rather than duplicating it in
	//    the scheduler module — a single source of truth for lock semantics
	//    across the framework.
	lock, err := memory.NewRedisDistributedLock(redisClient, tool.Logger)
	if err != nil {
		log.Fatalf("Failed to create distributed lock: %v", err)
	}

	// 9. Build the TaskStore — reusing the existing orchestration.RedisTaskStore.
	//    The Scheduler uses it for idempotent task creation via the
	//    core.ErrTaskAlreadyExists sentinel.
	taskStore := orchestration.NewRedisTaskStore(redisClient, nil)

	// 10. Register the 5 scheduling capabilities on the tool via the
	//     orchestration module's helper. This is the producer side — the
	//     HTTP endpoints that agents call when their LLM includes
	//     scheduler-tool/schedule_task in a plan. The helper retains a
	//     reference to the tool so handlers read tool.Logger dynamically.
	orchestration.RegisterScheduleCapabilities(tool, backends.ScheduleStore)

	// 11. Construct the Scheduler component (orchestration module). This is
	//     the background tick loop that promotes due schedules into target
	//     agent queues. It depends only on core interfaces — any backend
	//     combination satisfying them works identically. Logger is the
	//     post-framework ProductionLogger thanks to step 6's ordering.
	sched, err := orchestration.NewScheduler(orchestration.SchedulerDeps{
		ScheduleStore:  backends.ScheduleStore,
		TaskDispatcher: backends.TaskDispatcher,
		TaskStore:      taskStore,
		Lock:           lock,
		Logger:         tool.Logger,
	})
	if err != nil {
		log.Fatalf("Failed to create Scheduler: %v", err)
	}

	// 12. Register the Scheduler as a Runnable. The framework starts it in
	//     parallel with the HTTP server and drains it on shutdown. No
	//     hand-rolled goroutines, no companion Stop() method — ctx
	//     cancellation drives the shutdown, per FRAMEWORK_DESIGN_PRINCIPLES.md
	//     §3.4 ("Background Jobs Implement core.Runnable").
	framework.RegisterRunnable(sched)

	tool.Logger.Info("scheduler-tool starting", map[string]interface{}{
		"operation": "startup",
		"port":      port,
	})

	// 13. Graceful shutdown on SIGINT/SIGTERM. The framework.Run call
	//     returns when ctx is cancelled; it drains the HTTP server and all
	//     runnables (including the Scheduler) before returning.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		tool.Logger.Info("Shutdown signal received", map[string]interface{}{
			"operation": "shutdown",
		})
		cancel()
	}()

	// 14. Run (blocks until ctx is cancelled).
	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		tool.Logger.Error("Framework error", map[string]interface{}{
			"operation": "run",
			"error":     err.Error(),
		})
		os.Exit(1)
	}

	tool.Logger.Info("scheduler-tool stopped cleanly", nil)
}

// validateConfig verifies required environment variables are set and
// well-formed. Fails fast at startup per framework error-handling principle §1.
func validateConfig() error {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return fmt.Errorf("REDIS_URL environment variable required")
	}
	if !strings.HasPrefix(redisURL, "redis://") && !strings.HasPrefix(redisURL, "rediss://") {
		return fmt.Errorf("invalid REDIS_URL format (must start with redis:// or rediss://)")
	}
	if portStr := os.Getenv("PORT"); portStr != "" {
		if _, err := strconv.Atoi(portStr); err != nil {
			return fmt.Errorf("invalid PORT value: %v", err)
		}
	}
	return nil
}

// connectRedis parses REDIS_URL and establishes a *redis.Client, verifying
// connectivity with a ping so we fail fast if Redis is unreachable at
// startup rather than later in a capability handler.
func connectRedis(redisURL string) (*redis.Client, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	client := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}

// redactRedisURL strips any password from a Redis URL before logging it.
// Returns the URL unchanged if there's no userinfo component to strip.
func redactRedisURL(redisURL string) string {
	// Simple redaction: if there's an '@' with '//' before it, replace the
	// userinfo. Avoids importing net/url for a one-shot logging helper.
	at := strings.Index(redisURL, "@")
	slashSlash := strings.Index(redisURL, "//")
	if at == -1 || slashSlash == -1 || at < slashSlash {
		return redisURL
	}
	return redisURL[:slashSlash+2] + "***@" + redisURL[at+1:]
}

// resolvePort reads PORT from the environment with a sensible default.
func resolvePort() int {
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			return p
		}
	}
	return defaultPort
}

// initTelemetry sets up the telemetry global singleton with an environment-
// aware profile. Follows the same pattern as other example tools
// (fiscal-data-tool, event-driven-agent, etc.).
func initTelemetry(svcName string) {
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}
	var profile telemetry.Profile
	switch env {
	case "production", "prod":
		profile = telemetry.ProfileProduction
	case "staging", "stage", "qa":
		profile = telemetry.ProfileStaging
	default:
		profile = telemetry.ProfileDevelopment
	}
	config := telemetry.UseProfile(profile)
	config.ServiceName = svcName
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		config.Endpoint = endpoint
	}
	if err := telemetry.Initialize(config); err != nil {
		log.Printf("Warning: Telemetry initialization failed: %v", err)
		return
	}
	telemetry.EnableFrameworkIntegration(nil)
}
