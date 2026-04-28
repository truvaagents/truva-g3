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
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	tool := NewHotelTool()

	initTelemetry("hotel-tool")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			tool.Logger.Warn("Telemetry shutdown error", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	port := 8343
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	framework, err := core.NewFramework(tool,
		core.WithName("hotel-tool"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
		// Distributed tracing middleware — excludes health endpoints to reduce noise
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("hotel-tool",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	tool.Logger.Info("Hotel Tool Service Starting", map[string]interface{}{
		"service":   "hotel-tool",
		"port":      port,
		"telemetry": "enabled",
	})
	tool.Logger.Info("Registered endpoints will be shown in framework logs below", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		tool.Logger.Info("Shutting down gracefully", map[string]interface{}{
			"service": "hotel-tool",
		})

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		cancel()

		select {
		case <-shutdownCtx.Done():
			tool.Logger.Error("Shutdown timeout exceeded", map[string]interface{}{
				"timeout_seconds": 30,
			})
			os.Exit(1)
		case <-time.After(1 * time.Second):
		}

		tool.Logger.Info("Shutdown completed", map[string]interface{}{
			"service": "hotel-tool",
		})
		os.Exit(0)
	}()

	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
	}
}

func validateConfig() error {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return fmt.Errorf("REDIS_URL environment variable required")
	}

	if !strings.HasPrefix(redisURL, "redis://") && !strings.HasPrefix(redisURL, "rediss://") {
		return fmt.Errorf("invalid REDIS_URL format (must start with redis:// or rediss://)")
	}

	if os.Getenv("LITEAPI_KEY") == "" {
		fmt.Fprintln(os.Stderr, "Warning: LITEAPI_KEY not set - API calls will fail")
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
		fmt.Fprintf(os.Stderr, "Warning: Telemetry initialization failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "Tool will continue without telemetry")
		return
	}

	telemetry.EnableFrameworkIntegration(nil)
	fmt.Printf("Telemetry initialized for %s\n", serviceName)
}
