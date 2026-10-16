package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Service failed: %v", err)
	}
}

func run() error {
	// Validate before constructing the tool and its logger
	// (LOGGING_IMPLEMENTATION_GUIDE §5 — no request/logger context yet).
	if err := validateConfig(); err != nil {
		return fmt.Errorf("Configuration error: %w", err)
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
		return fmt.Errorf("Failed to create framework: %w", err)
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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run framework: %w", err)
	}
	return nil
}

func validateConfig() error {
	if _, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv); err != nil {
		return fmt.Errorf("invalid Redis configuration: %w", err)
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
