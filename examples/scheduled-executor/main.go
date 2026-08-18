// Package main — scheduled-executor wiring.
//
// This is the ONLY file in the executor binary that imports a vendor SDK
// (go-redis). It connects Redis, builds the scheduler backends, constructs
// the AgentCatalog, and wires the vendor-neutral Worker with interface-typed
// deps.

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
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	defaultPort = 9011
	serviceName = "scheduled-executor"
)

func main() {
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	core.SetCurrentComponentType(core.ComponentTypeAgent)

	initTelemetry(serviceName)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			log.Printf("Warning: Telemetry shutdown error: %v", err)
		}
	}()

	// Create the BaseAgent (needs Discovery for target-agent resolution).
	agent := core.NewBaseAgent(serviceName)

	// Connect to Redis.
	redisURL := os.Getenv("REDIS_URL")
	redisClient := connectRedis(redisURL)
	defer redisClient.Close()

	// Build scheduler backends (producer + consumer from same Redis).
	backends, err := orchestration.NewRedisSchedulerBackends(redisClient)
	if err != nil {
		log.Fatalf("Failed to create scheduler backends: %v", err)
	}

	// Create the framework with full agent configuration.
	port := resolvePort()
	framework, err := core.NewFramework(agent,
		core.WithName(serviceName),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(redisURL),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig(serviceName, &telemetry.TracingMiddlewareConfig{
			ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
		})),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// Resolve target agents through the agent's discovery client after the
	// framework initializes it during Run().
	catalog := newDiscoveryCatalog(agent)

	// Traced HTTP client for outbound POSTs to target agents.
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	// No http.Client.Timeout: per-request context.WithTimeout in worker.postOnce
	// (driven by DispatchTimeout / TRUVAG3_EXECUTOR_DISPATCH_TIMEOUT) is the sole authority.

	// Build the worker.
	worker, err := NewWorker(ExecutorDeps{
		Consumer:   backends.TaskConsumer,
		HTTPClient: tracedClient,
		Catalog:    catalog,
		Logger:     agent.Logger,
	})
	if err != nil {
		log.Fatalf("NewWorker: %v", err)
	}

	// Register the worker as a Runnable.
	framework.RegisterRunnable(worker)

	// Periodic catalog refresh (10s cadence, matching the DAG executor).
	refresher := &catalogRefresher{catalog: catalog, logger: agent.Logger}
	framework.RegisterRunnable(refresher)

	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	agent.Logger.Info("Scheduled executor starting", map[string]interface{}{
		"operation": "executor_startup",
		"port":      port,
	})

	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
	}
}

// refreshableCatalog is the subset of the resolver cache the refresher needs.
type refreshableCatalog interface {
	Refresh(ctx context.Context) error
	GetAgentsCount() int
}

// discoveryCatalog is a small discovery-backed cache for target-agent
// resolution. It reads through BaseAgent.Discover so it can be created before
// Framework.Run and still pick up discovery once initialization completes.
type discoveryCatalog struct {
	agent  *core.BaseAgent
	mu     sync.RWMutex
	agents map[string]*core.ServiceInfo
}

func newDiscoveryCatalog(agent *core.BaseAgent) *discoveryCatalog {
	return &discoveryCatalog{
		agent:  agent,
		agents: make(map[string]*core.ServiceInfo),
	}
}

func (c *discoveryCatalog) Refresh(ctx context.Context) error {
	services, err := c.agent.Discover(ctx, core.DiscoveryFilter{Type: core.ComponentTypeAgent})
	if err != nil {
		return err
	}

	agents := make(map[string]*core.ServiceInfo, len(services))
	for _, service := range services {
		if service == nil || service.Name == "" {
			continue
		}
		agents[service.Name] = service
	}

	c.mu.Lock()
	c.agents = agents
	c.mu.Unlock()
	return nil
}

func (c *discoveryCatalog) FindByName(name string) *core.ServiceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.agents[name]
}

func (c *discoveryCatalog) GetAgentsCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.agents)
}

// catalogRefresher is a core.Runnable that periodically refreshes the
// discovery-backed resolver cache.
type catalogRefresher struct {
	catalog  refreshableCatalog
	logger   core.Logger
	interval time.Duration // default 10s; tests use shorter
}

func (r *catalogRefresher) Start(ctx context.Context) error {
	interval := r.interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			refreshStart := time.Now()
			err := r.catalog.Refresh(ctx)
			elapsed := time.Since(refreshStart)

			status := "success"
			if err != nil {
				status = "failure"
			}
			telemetry.Counter("truvag3.scheduled_executor.catalog_refresh_total",
				"status", status,
				"trigger", "periodic",
				"module", "scheduled-executor",
			)
			telemetry.Histogram("truvag3.scheduled_executor.catalog_refresh_duration_ms",
				float64(elapsed.Milliseconds()),
				"status", status,
				"trigger", "periodic",
				"module", "scheduled-executor",
			)

			if err != nil {
				if r.logger != nil {
					r.logger.Warn("Periodic catalog refresh failed", map[string]interface{}{
						"operation":  "executor_catalog_refresh",
						"error":      err.Error(),
						"error_type": "catalog_refresh_error",
					})
				}
				continue
			}
			telemetry.Gauge("truvag3.scheduled_executor.catalog_agents_known",
				float64(r.catalog.GetAgentsCount()),
				"module", "scheduled-executor",
			)
		}
	}
}

func validateConfig() error {
	if os.Getenv("REDIS_URL") == "" {
		return fmt.Errorf("REDIS_URL is required")
	}
	return nil
}

func connectRedis(addr string) *redis.Client {
	var (
		client *redis.Client
		err    error
	)

	// Support both redis:// URLs and bare host:port addresses so the executor
	// matches the rest of the framework's REDIS_URL conventions.
	if strings.Contains(addr, "://") {
		var opts *redis.Options
		opts, err = redis.ParseURL(addr)
		if err != nil {
			log.Fatalf("Invalid Redis URL %q: %v", addr, err)
		}
		client = redis.NewClient(core.ApplyRedisClientDefaults(opts))
	} else {
		client = redis.NewClient(core.ApplyRedisClientDefaults(&redis.Options{
			Addr: addr,
		}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis connection failed: %v", err)
	}
	return client
}

func resolvePort() int {
	if p := os.Getenv("PORT"); p != "" {
		port := 0
		if _, err := fmt.Sscanf(p, "%d", &port); err == nil && port > 0 {
			return port
		}
	}
	return defaultPort
}

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
