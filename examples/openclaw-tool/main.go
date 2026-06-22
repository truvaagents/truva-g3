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

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

func main() {
	// Pre-logger fatal: validateConfig runs before the tool/Logger exists, so use stdlib log
	// (LOGGING_IMPLEMENTATION_GUIDE §5 — no request/logger context yet).
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Create the tool FIRST so component type is set for telemetry auto-inference.
	tool := NewOpenClawTool()

	// Initialize telemetry AFTER tool creation.
	initTelemetry("openclaw-tool")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = telemetry.Shutdown(ctx)
	}()

	port := 8393
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	framework, err := core.NewFramework(tool,
		core.WithName("openclaw-tool"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true), // server-to-server, not browser-facing
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),

		// Distributed tracing middleware — excludes health/readiness endpoints to reduce noise
		// (DISTRIBUTED_TRACING_GUIDE §6).
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("openclaw-tool",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		// Framework construction failed → its Logger may be unwired; stay on stdlib log.
		log.Fatalf("Failed to create framework: %v", err)
	}

	// Startup logging via the tool's structured Logger (LOGGING_IMPLEMENTATION_GUIDE §5/§7):
	// basic (non-context) methods in main(), emitting structured JSON with service/component fields.
	if os.Getenv("OPENCLAW_GATEWAY_TOKEN") == "" {
		tool.Logger.Warn("OPENCLAW_GATEWAY_TOKEN not set; OpenClaw gateway calls will fail authentication", nil)
	}
	tool.Logger.Info("OpenClaw tool starting", map[string]interface{}{
		"port":         port,
		"openclaw_url": getenvStr("OPENCLAW_URL", "http://127.0.0.1:18789"),
		"capabilities": len(tool.Capabilities),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		tool.Logger.Info("shutting down", nil)
		cancel()
		time.Sleep(time.Second)
		os.Exit(0)
	}()

	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		tool.Logger.Error("framework error", map[string]interface{}{"error": err.Error()})
		os.Exit(1)
	}
}

func validateConfig() error {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return fmt.Errorf("REDIS_URL required")
	}
	if !strings.HasPrefix(redisURL, "redis://") && !strings.HasPrefix(redisURL, "rediss://") {
		return fmt.Errorf("invalid REDIS_URL format (must start with redis:// or rediss://)")
	}
	return nil
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
	case "staging", "stage", "qa":
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
		// initTelemetry is background init with no logger context (guide §5) → stdlib log.
		log.Printf("Warning: Telemetry init failed: %v", err)
		return
	}

	// Enable framework integration so core components (redis_registry, discovery) emit metrics.
	telemetry.EnableFrameworkIntegration(nil)
}
