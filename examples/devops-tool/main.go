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
	// Validate configuration first
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Create tool FIRST so component type is set for telemetry
	// The tool constructor calls core.SetCurrentComponentType(ComponentTypeTool)
	// which enables automatic service_type inference in telemetry
	tool := NewDevOpsTool()

	// Initialize telemetry AFTER tool creation
	// This ensures core.GetCurrentComponentType() returns "tool" for auto-inference
	initTelemetry("devops-tool")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			tool.Logger.Warn("Telemetry shutdown error", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	// Get port configuration from environment
	port := 8347
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	// Framework handles all the complexity
	framework, err := core.NewFramework(tool,
		// Core configuration
		core.WithName("devops-tool"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),

		// Discovery configuration (tools can register but not discover)
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),

		// CORS for web access
		core.WithCORS([]string{"*"}, true),

		// Development mode from environment
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),

		// Distributed tracing middleware with health endpoint exclusion
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("devops-tool", &telemetry.TracingMiddlewareConfig{
			ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live", "/api/capabilities"},
		})),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// Display startup information
	tool.Logger.Info("DevOps Tool Service Starting", map[string]interface{}{
		"service":      "devops-tool",
		"port":         port,
		"telemetry":    "enabled",
		"capabilities": 7,
	})

	// Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		tool.Logger.Info("Shutting down gracefully", map[string]interface{}{
			"service": "devops-tool",
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
			"service": "devops-tool",
		})
		os.Exit(0)
	}()

	// Run the framework (blocking)
	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		tool.Logger.Error("Framework error", map[string]interface{}{
			"error": err.Error(),
		})
		os.Exit(1)
	}
}

// validateConfig validates all required configuration at startup
func validateConfig() error {
	// REDIS_URL is required for discovery
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return fmt.Errorf("REDIS_URL environment variable required")
	}

	// Validate Redis URL format
	if !strings.HasPrefix(redisURL, "redis://") && !strings.HasPrefix(redisURL, "rediss://") {
		return fmt.Errorf("invalid REDIS_URL format (must start with redis:// or rediss://)")
	}

	// Validate port if set
	if portStr := os.Getenv("PORT"); portStr != "" {
		if _, err := strconv.Atoi(portStr); err != nil {
			return fmt.Errorf("invalid PORT value: %v", err)
		}
	}

	return nil
}

// initTelemetry sets up telemetry based on environment
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

	// Enable framework integration so core components (redis_registry, discovery)
	// emit metrics like discovery.registrations, discovery.health_checks, etc.
	telemetry.EnableFrameworkIntegration(nil)

	log.Printf("Telemetry initialized for %s", serviceName)
}
