package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
)

// AsyncTravelAgent is an async-capable AI-driven agent that uses
// the Truva-G3 async task system combined with AI orchestration for
// autonomous multi-tool coordination.
//
// Key Features:
//   - Natural language query input (no hardcoded workflows)
//   - AI orchestrator dynamically selects which tools to call
//   - DAG-based parallel execution for efficiency
//   - Per-tool progress reporting via OnStepComplete callback
//   - Async task submission with HTTP 202 + polling pattern
//   - Distributed tracing across async boundaries
//
// Available Tools (auto-discovered):
//   - geocoding-tool: Convert location names to coordinates
//   - weather-tool-v2: Get weather forecasts
//   - news-tool: Search for news articles
//   - currency-tool: Get exchange rates
//   - stock-market-tool: Get market news
//   - country-info-tool: Get country information
//
// Architecture:
//
//	┌─────────────────────────────────────────────────────────────────┐
//	│                    Async AI Agent                               │
//	├─────────────────────────────────────────────────────────────────┤
//	│                                                                  │
//	│  1. User submits natural language query                          │
//	│  2. AI orchestrator generates execution plan                     │
//	│  3. DAG executor runs tools (parallel where possible)           │
//	│  4. OnStepComplete callback reports per-tool progress           │
//	│  5. AI synthesizes final response                               │
//	│                                                                  │
//	│  Adding new tools = Just deploy them (no agent code changes)    │
//	└─────────────────────────────────────────────────────────────────┘
type AsyncTravelAgent struct {
	*core.BaseAgent
	redisClient  *redis.Client
	httpClient   *http.Client
	orchestrator orchestration.Orchestrator // AI orchestrator for dynamic tool selection
	mu           sync.RWMutex
}

// QueryInput represents the input for a natural language query task.
// This replaces the old structured TravelResearchInput.
type QueryInput struct {
	Query string `json:"query"` // Natural language query (e.g., "What's the weather in Tokyo?")
}

// QueryResult contains the result of an AI-orchestrated query.
// This replaces the old hardcoded TravelResearchResult.
type QueryResult struct {
	Query         string                 `json:"query"`          // Original query
	Response      string                 `json:"response"`       // AI-synthesized answer
	ToolsUsed     []string               `json:"tools_used"`     // Tools called during execution
	StepResults   []StepResultSummary    `json:"step_results"`   // Per-tool execution details
	ExecutionTime string                 `json:"execution_time"` // Total execution duration
	Confidence    float64                `json:"confidence"`     // Response confidence (0.0-1.0)
	Metadata      map[string]interface{} `json:"metadata"`       // Additional metadata
}

// StepResultSummary provides a summary of each tool execution step.
// Used for progress tracking and debugging.
type StepResultSummary struct {
	ToolName string `json:"tool_name"` // Name of the tool that was called
	Success  bool   `json:"success"`   // Whether the tool call succeeded
	Duration string `json:"duration"`  // How long the tool call took
}

// NewAsyncTravelAgent creates a new async AI-driven agent.
func NewAsyncTravelAgent(redisClient *redis.Client) (*AsyncTravelAgent, error) {
	baseAgent := core.NewBaseAgent("async-travel-agent")

	// Create AI client for orchestration and synthesis.
	// Auto-detect mode: discovers available providers from API keys and orders by priority.
	//
	// To explicitly control the chain order instead of auto-detecting, use WithProviderChain:
	//   ai.NewChainClient(
	//       ai.WithProviderChain("openai", "anthropic"),  // explicit order, no auto-detection
	//       ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
	//       ai.WithChainLogger(baseAgent.Logger),
	//   )
	aiClient, err := ai.NewChainClient(
		ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
		ai.WithChainLogger(baseAgent.Logger),
	)
	if err != nil {
		baseAgent.Logger.Warn("AI client creation failed, orchestration will be limited", map[string]interface{}{
			"error": err.Error(),
		})
	} else {
		baseAgent.AI = aiClient
	}

	// Create traced HTTP client
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 60 * time.Second

	agent := &AsyncTravelAgent{
		BaseAgent:   baseAgent,
		redisClient: redisClient,
		httpClient:  tracedClient,
	}

	// Declare metrics for AI orchestration
	telemetry.DeclareMetrics("async-ai-agent", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{Name: "async_orchestration.tasks", Type: "counter", Labels: []string{"status"}},
			{Name: "async_orchestration.tool_calls", Type: "counter", Labels: []string{"tool", "status"}},
			{Name: "async_orchestration.duration_ms", Type: "histogram", Buckets: []float64{1000, 5000, 10000, 30000, 60000, 120000}},
			{Name: "async_orchestration.tools_per_query", Type: "histogram", Buckets: []float64{1, 2, 3, 5, 7, 10}},
		},
	})

	return agent, nil
}

// InitializeOrchestrator sets up the AI orchestrator with tool discovery.
// Must be called after framework creation (to access Discovery).
func (a *AsyncTravelAgent) InitializeOrchestrator(discovery core.Discovery) error {
	if a.AI == nil {
		return nil // Orchestrator requires AI, will use fallback behavior
	}

	// Store discovery for per-request orchestrator creation in handlers
	a.Discovery = discovery

	config := orchestration.DefaultConfig()
	config.RoutingMode = orchestration.ModeAutonomous
	// Plan and synthesis token limits are loaded by DefaultConfig() into per-phase AI options.
	if config.SynthesisAIOptions == nil {
		config.SynthesisAIOptions = &orchestration.AIOptionsOverride{}
	}
	config.SynthesisAIOptions.Temperature = orchestration.Float32Ptr(0.5) // Lower temperature for focused, deterministic responses
	config.ExecutionOptions.TotalTimeout = 5 * time.Minute
	config.ExecutionOptions.StepTimeout = 2 * time.Minute
	config.EnableTelemetry = true

	deps := orchestration.OrchestratorDependencies{
		Discovery: discovery,
		AIClient:  a.AI,
		Logger:    a.Logger,
	}

	orch, err := orchestration.CreateOrchestrator(config, deps)
	if err != nil {
		return err
	}

	// Start the orchestrator to initialize the catalog from discovery
	// This populates the catalog with actual tool names from Redis discovery
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := orch.Start(ctx); err != nil {
		return fmt.Errorf("failed to start orchestrator: %w", err)
	}

	a.orchestrator = orch

	a.Logger.Info("AI orchestrator initialized", map[string]interface{}{
		"routing_mode":  string(config.RoutingMode),
		"total_timeout": config.ExecutionOptions.TotalTimeout.String(),
		"step_timeout":  config.ExecutionOptions.StepTimeout.String(),
	})

	return nil
}
