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

	// Create tool FIRST so component type is set for telemetry
	tool := NewPubMedTool()

	// Initialize telemetry AFTER tool creation
	initTelemetry("pubmed-tool")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		telemetry.Shutdown(ctx)
	}()

	port := 8366
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	framework, err := core.NewFramework(tool,
		core.WithName("pubmed-tool"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		// Distributed tracing middleware — excludes health endpoints to reduce noise
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("pubmed-tool",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		tool.Logger.Error("Failed to create framework", map[string]interface{}{
			"operation": "startup",
			"error":     err.Error(),
		})
		return fmt.Errorf("create framework: %w", err)
	}

	tool.Logger.Info("PubMed Tool Service Starting", map[string]interface{}{
		"operation": "startup",
		"service":   "pubmed-tool",
		"port":      port,
		"telemetry": "enabled",
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run framework: %w", err)
	}
	return nil
}

func validateConfig() error {
	// REDIS_URL is required for discovery
	if _, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv); err != nil {
		return fmt.Errorf("invalid Redis configuration: %w", err)
	}

	// NCBI_API_KEY is optional but recommended
	if os.Getenv("NCBI_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "Warning: NCBI_API_KEY not set - rate limited to 3 req/sec (10 with key)")
	}

	// NCBI_EMAIL is required by NCBI policy
	if os.Getenv("NCBI_EMAIL") == "" {
		return fmt.Errorf("NCBI_EMAIL environment variable required (NCBI policy)")
	}

	// NCBI_TOOL_NAME is required by NCBI policy
	if os.Getenv("NCBI_TOOL_NAME") == "" {
		return fmt.Errorf("NCBI_TOOL_NAME environment variable required (NCBI policy)")
	}

	// Validate port if set
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
		fmt.Fprintf(os.Stderr, "Warning: Telemetry initialization failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "Tool will continue without telemetry")
		return
	}

	telemetry.EnableFrameworkIntegration(nil)
	fmt.Printf("Telemetry initialized for %s\n", serviceName)
}
