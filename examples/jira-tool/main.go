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

	// Set component type for telemetry service_type inference, then
	// initialize telemetry BEFORE creating tool (per Logging Implementation Guide §7, §14).
	core.SetCurrentComponentType(core.ComponentTypeTool)
	initTelemetry("jira-tool")
	tool := NewJiraTool()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			log.Printf("Warning: Telemetry shutdown error: %v", err)
		}
	}()

	port := 8366
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	framework, err := core.NewFramework(tool,
		core.WithName("jira-tool"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),

		// Distributed tracing middleware — excludes health endpoints to reduce noise
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("jira-tool",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	tool.Logger.Info("JIRA Tool Service starting", map[string]interface{}{
		"operation":    "startup",
		"port":         port,
		"jira_base_url": os.Getenv("JIRA_BASE_URL"),
		"capabilities": len(tool.Capabilities),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		tool.Logger.Info("Shutting down gracefully", map[string]interface{}{
			"operation": "shutdown",
		})
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		cancel()
		select {
		case <-shutdownCtx.Done():
			tool.Logger.Error("Shutdown timeout exceeded", map[string]interface{}{
				"operation": "shutdown",
			})
			os.Exit(1)
		case <-time.After(1 * time.Second):
		}
		tool.Logger.Info("Shutdown completed", map[string]interface{}{
			"operation": "shutdown",
		})
		os.Exit(0)
	}()

	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		tool.Logger.Error("Framework error", map[string]interface{}{
			"operation": "framework_run",
			"error":     err.Error(),
		})
	}
}

func validateConfig() error {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return fmt.Errorf("REDIS_URL environment variable required")
	}
	if !strings.HasPrefix(redisURL, "redis://") && !strings.HasPrefix(redisURL, "rediss://") {
		return fmt.Errorf("invalid REDIS_URL format")
	}

	if os.Getenv("JIRA_BASE_URL") == "" {
		return fmt.Errorf("JIRA_BASE_URL required (e.g. https://mycompany.atlassian.net)")
	}
	if os.Getenv("JIRA_USER_EMAIL") == "" {
		return fmt.Errorf("JIRA_USER_EMAIL required")
	}
	if os.Getenv("JIRA_API_TOKEN") == "" {
		return fmt.Errorf("JIRA_API_TOKEN required")
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
		return
	}

	telemetry.EnableFrameworkIntegration(nil)

	log.Printf("Telemetry initialized for %s", serviceName)
}
