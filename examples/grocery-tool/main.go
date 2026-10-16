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
	// Validate configuration first
	if err := validateConfig(); err != nil {
		return fmt.Errorf("Configuration error: %w", err)
	}

	// Get API base URL from environment
	apiBaseURL := os.Getenv("GROCERY_API_URL")
	if apiBaseURL == "" {
		apiBaseURL = "http://grocery-store-api.truvag3-examples.svc.cluster.local"
	}

	// Create grocery tool FIRST so component type is set for telemetry
	// The tool constructor calls core.SetCurrentComponentType(ComponentTypeTool)
	// which enables automatic service_type inference in telemetry
	tool := NewGroceryTool(apiBaseURL)

	// Initialize telemetry AFTER tool creation
	// This ensures core.GetCurrentComponentType() returns "tool" for auto-inference
	initTelemetry("grocery-tool")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			log.Printf("Warning: Telemetry shutdown error: %v", err)
		}
	}()

	// Get port configuration from environment
	port := 8083 // default port for grocery-tool
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	// Framework handles all the complexity
	framework, err := core.NewFramework(tool,
		// Core configuration from environment
		core.WithName("grocery-tool"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),

		// Discovery configuration
		core.WithDiscovery(true, "redis"),

		// CORS for web access
		core.WithCORS([]string{"*"}, true),

		// Development mode from environment
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),
	)
	if err != nil {
		return fmt.Errorf("Failed to create framework: %w", err)
	}

	// Display startup information
	log.Println("===========================================")
	log.Println("  Grocery Tool - Proxy to Grocery Store API")
	log.Println("===========================================")
	log.Printf("Tool Server Port:     %d", port)
	log.Printf("Backend API URL:      %s", apiBaseURL)
	log.Println("Telemetry: Enabled")
	log.Println("")
	log.Println("Error Injection is controlled via the API:")
	log.Printf("  POST %s/admin/inject-error", apiBaseURL)
	log.Printf("  GET  %s/admin/status", apiBaseURL)
	log.Printf("  POST %s/admin/reset", apiBaseURL)
	log.Println("")
	log.Println("Error Modes (at API level):")
	log.Println("  normal       - All requests succeed")
	log.Println("  rate_limit   - Return 429 after N requests")
	log.Println("  server_error - Return 500 with probability")
	log.Println("===========================================")
	log.Println("")

	// Set up graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run framework: %w", err)
	}
	return nil
}

// validateConfig validates all required configuration at startup
func validateConfig() error {
	// REDIS_URL is required for discovery
	if _, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv); err != nil {
		return fmt.Errorf("invalid Redis configuration: %w", err)
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
		log.Printf("Tool will continue without telemetry")
		return
	}

	// Enable framework integration - this allows core components (redis_registry, discovery)
	// to emit metrics like discovery.registrations, discovery.health_checks, etc.
	telemetry.EnableFrameworkIntegration(nil)

	log.Printf("Telemetry initialized for %s", serviceName)
}
