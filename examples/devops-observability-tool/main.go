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
	if err := validateConfig(); err != nil {
		return fmt.Errorf("Configuration error: %w", err)
	}

	tool := NewLogsTool()

	initTelemetry("devops-observability-tool")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			tool.Logger.Warn("Telemetry shutdown error", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	port := 8378
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	framework, err := core.NewFramework(tool,
		core.WithName("devops-observability-tool"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithDiscovery(true, "redis"),

		core.WithCORS([]string{"*"}, true),

		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),

		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("devops-observability-tool", &telemetry.TracingMiddlewareConfig{
			ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live", "/api/capabilities"},
		})),
	)
	if err != nil {
		return fmt.Errorf("Failed to create framework: %w", err)
	}

	tool.Logger.Info("Logs Analytics Tool Starting", map[string]interface{}{
		"service":      "devops-observability-tool",
		"port":         port,
		"loki_url":     os.Getenv("LOKI_URL"),
		"telemetry":    "enabled",
		"capabilities": 5,
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
	if portStr := os.Getenv("PORT"); portStr != "" {
		if _, err := strconv.Atoi(portStr); err != nil {
			return fmt.Errorf("invalid PORT value: %v", err)
		}
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
		log.Printf("Warning: Telemetry initialization failed: %v", err)
		log.Println("Tool will continue without telemetry")
		return
	}

	telemetry.EnableFrameworkIntegration(nil)
	log.Printf("Telemetry initialized for %s", serviceName)
}
