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
	"github.com/truvaagents/truva-g3/examples/web-search-tool/providers"
	"github.com/truvaagents/truva-g3/telemetry"
)

// WebSearchTool wraps a search provider and exposes the web_search capability.
// This tool is a pure API wrapper - no AI capabilities.
// Data extraction between orchestration steps is handled by the orchestrator's
// Layer 2 (Micro-Resolution). See: orchestration/INTELLIGENT_PARAMETER_BINDING.md
type WebSearchTool struct {
	*core.BaseTool
	provider providers.SearchProvider
	cache    *core.MemoryStore // local response cache
}

func main() {
	// 1. Validate configuration first
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// 2. Create the tool FIRST so component type is set for telemetry
	// The tool constructor calls core.SetCurrentComponentType(ComponentTypeTool)
	// which enables automatic service_type inference in telemetry
	// NOTE: This tool does NOT use AI (pure HTTP API wrapper), so tool-first order is correct.
	// See stock-market-tool for the same pattern.
	tool := NewWebSearchTool()

	// 3. Initialize telemetry AFTER tool creation
	initTelemetry("web-search")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(ctx); err != nil {
			tool.Logger.Warn("Telemetry shutdown error", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	// 4. Get port configuration from environment (as int)
	port := 8341 // default - allocated in examples/README.md port table
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	// 5. Create the framework with options
	framework, err := core.NewFramework(tool,
		// Core configuration
		core.WithName("web-search-tool"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),

		// Discovery configuration (tools can register but not discover)
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),

		// CORS for web access
		core.WithCORS([]string{"*"}, true),

		// Development mode from environment
		core.WithDevelopmentMode(os.Getenv("DEV_MODE") == "true"),

		// Distributed tracing middleware — excludes health endpoints to reduce noise
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("web-search",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// Register a periodic sweeper Runnable for the tool's response cache so
	// the framework drains it on shutdown and bounds memory under load.
	// Interval is read from tool.Config.Memory.CleanupInterval so the
	// TRUVAG3_MEMORY_CLEANUP_INTERVAL env var (default 10m) actually reaches
	// tool sweepers per FRAMEWORK_DESIGN_PRINCIPLES "Externalize Hardcoded
	// Limits". Config is populated by core.NewFramework via applyConfigToComponent.
	sweeper, err := core.NewMemoryStoreSweeper(tool.cache, tool.Config.Memory.CleanupInterval, tool.Logger)
	if err != nil {
		log.Fatalf("Failed to create memory store sweeper: %v", err)
	}
	framework.RegisterRunnable(sweeper)

	// 6. Display startup information
	tool.Logger.Info("Web Search Tool Service Starting", map[string]interface{}{
		"service":   "web-search",
		"port":      port,
		"provider":  tool.provider.Name(),
		"telemetry": "enabled",
	})
	tool.Logger.Info("Registered endpoints will be shown in framework logs below", nil)

	// 7. Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		tool.Logger.Info("Shutting down gracefully", map[string]interface{}{
			"service": "web-search",
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
			// Give framework time to clean up
		}

		tool.Logger.Info("Shutdown completed", map[string]interface{}{
			"service": "web-search",
		})
		os.Exit(0)
	}()

	// 8. Run the framework (blocking call)
	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
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

	// Warn about optional but recommended variables (API keys, etc.)
	if os.Getenv("SEARCH_API_KEY") == "" {
		log.Println("Warning: SEARCH_API_KEY not set - tool will use mock provider")
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
	// Determine environment profile
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

	// Create config from profile
	config := telemetry.UseProfile(profile)
	config.ServiceName = serviceName

	// Override endpoint from environment if set
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		config.Endpoint = endpoint
	}

	// Initialize telemetry
	if err := telemetry.Initialize(config); err != nil {
		log.Printf("Warning: Telemetry initialization failed: %v", err)
		log.Println("Tool will continue without telemetry")
		return
	}

	// Enable framework integration - this allows core components (redis_registry, discovery)
	// to emit metrics like discovery.registrations, discovery.health_checks, etc.
	telemetry.EnableFrameworkIntegration(nil)

	log.Printf("Telemetry initialized for %s", serviceName)
}

// NewWebSearchTool creates and initializes the web search tool
func NewWebSearchTool() *WebSearchTool {
	providerName := getEnvOrDefault("SEARCH_PROVIDER", "mock")
	apiKey := os.Getenv("SEARCH_API_KEY")

	tool := &WebSearchTool{
		BaseTool: core.NewTool("web-search-tool"),
		provider: createProvider(providerName, apiKey),
		cache:    core.NewMemoryStore(),
	}

	tool.registerCapabilities()
	return tool
}

// createProvider creates the appropriate search provider based on configuration
func createProvider(providerName, apiKey string) providers.SearchProvider {
	switch strings.ToLower(providerName) {
	case "tavily":
		if apiKey == "" {
			log.Println("Warning: Tavily provider requested but SEARCH_API_KEY not set, falling back to mock")
			return providers.NewMockProvider()
		}
		return NewTavilyClient(apiKey)
	case "mock":
		return providers.NewMockProvider()
	default:
		log.Printf("Warning: Unknown provider '%s', using mock provider", providerName)
		return providers.NewMockProvider()
	}
}

func (w *WebSearchTool) registerCapabilities() {
	// web_search capability
	// Description optimized for LLM capability selection:
	// 1. WHEN to use it (vs specialized tools)
	// 2. WHAT it returns
	// 3. Required/Optional fields
	w.RegisterCapability(core.Capability{
		Name:        "web_search",
		Description: "Searches the web for general information when no specialized tool exists.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     w.handleWebSearch,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Example: "best beach destinations Caribbean", Description: "Search terms - be specific for better results"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "max_results", Type: "integer", Example: "5", Description: "Number of results to return (1-10, default 5)"},
				{Name: "search_type", Type: "string", Example: "web", Description: "Type of search: web or news (default web)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The search query that was executed"},
				{Name: "results", Type: "array", Description: "Array of search results, each with title, snippet, url, and optional display_url, published_at, score"},
				{Name: "search_time", Type: "string", Description: "Time taken to complete the search"},
				{Name: "provider", Type: "string", Description: "Name of the search provider used"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "total_results", Type: "number", Description: "Total number of results available"},
				{Name: "cached", Type: "boolean", Description: "Whether the result was served from cache"},
			},
		},
	})
	// NOTE: No extract_structured_data capability.
	// Data extraction between orchestration steps is handled automatically by
	// the orchestrator's Layer 2 (Micro-Resolution). See:
	// orchestration/INTELLIGENT_PARAMETER_BINDING.md
}

func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
