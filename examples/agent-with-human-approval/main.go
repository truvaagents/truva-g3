// Package main provides a HITL-enabled chat agent demonstration.
//
// This agent demonstrates Human-in-the-Loop (HITL) human oversight for AI orchestration.
// It runs on port 8098 (different from travel-chat-agent on 8095).
//
// HITL is ALWAYS enabled in this agent - no environment variable toggle needed.
// All execution plans require human approval before proceeding.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"

	// Import AI providers for auto-detection
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
	// 1. Validate configuration (fail fast)
	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// 2. Set component type for service_type labeling in telemetry
	core.SetCurrentComponentType(core.ComponentTypeAgent)

	// 3. Initialize telemetry BEFORE creating agent/AI client (critical for AI spans)
	initTelemetry("agent-with-human-approval")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		telemetry.Shutdown(ctx)
	}()

	// 4. Create agent AFTER telemetry is initialized
	agent, err := NewHITLChatAgent()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// ┌────────────────────────────────────────────────────────────────┐
	// │  HITL Configuration from Environment Variables                 │
	// └────────────────────────────────────────────────────────────────┘
	// HITL configuration is loaded from environment variables via DefaultConfig():
	//   TRUVAG3_HITL_ENABLED=true
	//   TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true
	//   TRUVAG3_HITL_SENSITIVE_CAPABILITIES=stock_quote,company_profile,...
	//   TRUVAG3_HITL_DEFAULT_TIMEOUT=5m
	//   TRUVAG3_HITL_ESCALATE_AFTER_RETRIES=3
	//
	// See orchestration/HUMAN_IN_THE_LOOP_PROPOSAL.md for full env var reference.
	hitlConfig := orchestration.DefaultConfig().HITL

	// Validate HITL is enabled (required for this agent)
	if !hitlConfig.Enabled {
		log.Fatalf("HITL must be enabled for this agent. Set TRUVAG3_HITL_ENABLED=true")
	}

	// 5. Setup HITL infrastructure
	hitl, err := SetupHITL(agent.Logger, hitlConfig)
	if err != nil {
		log.Fatalf("HITL setup failed: %v", err)
	}
	defer hitl.Close()

	agent.Logger.Info("HITL infrastructure initialized", map[string]interface{}{
		"sensitive_capabilities": hitlConfig.SensitiveCapabilities,
		"require_plan_approval":  hitlConfig.RequirePlanApproval,
		"default_timeout":        hitlConfig.DefaultTimeout.String(),
	})

	// 6. Create framework with tracing middleware
	middlewareConfig := &telemetry.TracingMiddlewareConfig{
		ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live", "/api/capabilities"},
		// Exclude HITL polling requests from tracing to reduce noise in Jaeger
		// UI polls checkpoint status every 5 seconds with ?poll=true
		RequestFilter: func(r *http.Request) bool {
			return r.URL.Query().Get("poll") != "true"
		},
	}

	framework, err := core.NewFramework(agent,
		core.WithName("agent-with-human-approval"),
		core.WithPort(getPort()),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORSDefaults(), // Allows all headers including X-Truvag3-Original-Request-ID
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("agent-with-human-approval", middlewareConfig)),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	// 7. Initialize orchestrator with HITL in background (after discovery is ready)
	go func() {
		startTime := time.Now()
		lastWarning := time.Time{}

		for agent.BaseAgent.Discovery == nil {
			time.Sleep(100 * time.Millisecond)

			elapsed := time.Since(startTime)
			if elapsed > 30*time.Second && time.Since(lastWarning) > 60*time.Second {
				if lastWarning.IsZero() {
					agent.Logger.Warn("Discovery not available after 30s", map[string]interface{}{
						"hint": "check Redis connectivity (REDIS_URL)",
					})
				} else {
					agent.Logger.Warn("Still waiting for Discovery", map[string]interface{}{
						"elapsed": elapsed.Round(time.Second).String(),
					})
				}
				lastWarning = time.Now()
			}
		}

		// Discovery is available, initialize orchestrator with HITL
		if err := agent.InitializeOrchestrator(agent.BaseAgent.Discovery, hitl, hitlConfig); err != nil {
			agent.Logger.Error("Failed to initialize orchestrator", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			agent.Logger.Info("Orchestrator initialized with HITL successfully", nil)
		}
	}()

	// ┌────────────────────────────────────────────────────────────────┐
	// │  HITL API HANDLERS: Always registered in this agent            │
	// └────────────────────────────────────────────────────────────────┘
	hitlHandler := orchestration.NewHITLHandler(
		hitl.Controller,
		hitl.CheckpointStore,
		orchestration.WithHITLHandlerLogger(agent.Logger),
	)

	// Register HITL-specific routes
	agent.RegisterHITLCapabilities(hitlHandler)

	agent.Logger.Info("HITL API handlers registered", map[string]interface{}{
		"endpoints": []string{
			"/chat",
			"/hitl/command",
			"/hitl/resume/{id}",
			"/hitl/resume-sync/{id}",
			"/hitl/auto-resume/{id}/stream",
			"/hitl/checkpoints",
			"/hitl/checkpoints/{id}",
		},
	})

	// 8. Emit startup metrics
	telemetry.Counter("agent_with_human_approval.startup", "status", "success")

	// 9. Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, initiating graceful shutdown...", sig)
		cancel()
	}()

	// 10. Run the framework
	port := getPort()
	log.Printf("Starting agent-with-human-approval on port %d", port)
	log.Printf("HITL enabled: require_plan_approval=%v", hitlConfig.RequirePlanApproval)
	log.Printf("Sensitive capabilities: %v", hitlConfig.SensitiveCapabilities)
	log.Printf("")
	log.Printf("Endpoints:")
	log.Printf("  Streaming (SSE):")
	log.Printf("    POST /chat/stream              - SSE streaming chat (HITL enabled)")
	log.Printf("    POST /hitl/resume/{id}         - Resume execution after approval (SSE)")
	log.Printf("    GET  /hitl/auto-resume/{id}/stream - Auto-resume after expired_approved (SSE)")
	log.Printf("  Non-Streaming (JSON):")
	log.Printf("    POST /chat                 - JSON chat (HITL enabled)")
	log.Printf("    POST /hitl/resume-sync/{id} - Resume execution (JSON)")
	log.Printf("  HITL Management:")
	log.Printf("    POST /hitl/command         - Submit approval/rejection")
	log.Printf("    GET  /hitl/checkpoints     - List pending checkpoints")
	log.Printf("    GET  /hitl/checkpoints/{id} - Get checkpoint details")
	log.Printf("  Infrastructure:")
	log.Printf("    GET  /health               - Health check")
	log.Printf("    GET  /discover             - Discover available tools")

	if err := framework.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Framework error: %v", err)
	}

	log.Println("Agent with human approval shutdown complete")
}

// validateConfig validates required configuration
func validateConfig() error {
	// At least one AI provider key is required
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Println("Warning: No AI provider API key found. Set OPENAI_API_KEY or ANTHROPIC_API_KEY")
	}

	// Redis is required for service discovery, session storage, and HITL checkpoints
	if os.Getenv("REDIS_URL") == "" {
		return fmt.Errorf("REDIS_URL is required for service discovery, sessions, and HITL checkpoints")
	}

	return nil
}

// initTelemetry initializes OpenTelemetry with environment-aware configuration
func initTelemetry(serviceName string) {
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}

	var profile telemetry.Profile
	switch env {
	case "production", "prod":
		profile = telemetry.ProfileProduction
	case "staging", "stage":
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
		log.Printf("Application will continue without telemetry")
		return
	}

	telemetry.EnableFrameworkIntegration(nil)

	log.Printf("Telemetry initialized successfully")
	log.Printf("  Environment: %s", env)
	log.Printf("  Profile: %s", profile)
	log.Printf("  Service: %s", serviceName)
	if config.Endpoint != "" {
		log.Printf("  Endpoint: %s", config.Endpoint)
	}
}

// getPort returns the server port from PORT environment variable.
// PORT is required - no default to avoid port conflicts between examples.
func getPort() int {
	port := os.Getenv("PORT")
	if port == "" {
		log.Fatal("PORT environment variable is required. Set it in .env or k8-deployment.yaml")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 {
		log.Fatalf("PORT must be a positive integer, got: %s", port)
	}
	return p
}
