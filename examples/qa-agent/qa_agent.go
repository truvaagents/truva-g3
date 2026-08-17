package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
)

// QAAgent is a non-streaming orchestration agent that coordinates autonomous
// website testing workflows using the playwright-tool.
// Flow: explore site → generate tests (LLM) → execute tests → report results
type QAAgent struct {
	*core.BaseAgent
	orchestrator *orchestration.AIOrchestrator
	httpClient   *http.Client
	mu           sync.RWMutex
}

// NewQAAgent creates a new QA testing agent with AI and telemetry configured.
func NewQAAgent() (*QAAgent, error) {
	agent := core.NewBaseAgent("qa-agent")

	// Create AI client with provider chain for failover
	chainClient, err := ai.NewChainClient(
		ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
		ai.WithChainLogger(agent.Logger),
		ai.WithChainTimeout(240*time.Second),
	)
	if err != nil {
		agent.Logger.Warn("Failed to create AI chain client, trying single provider", map[string]interface{}{
			"error": err.Error(),
		})
		singleClient, err := ai.NewClient(
			ai.WithTimeout(240*time.Second),
			ai.WithTelemetry(telemetry.GetTelemetryProvider()),
		)
		if err != nil {
			agent.Logger.Warn("AI client creation failed, some features will be limited", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			agent.AI = singleClient
		}
	} else {
		agent.AI = chainClient
	}

	// Declare metrics
	telemetry.DeclareMetrics("qa-agent", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:    "qa.request.duration_ms",
				Type:    "histogram",
				Help:    "QA request duration in milliseconds",
				Labels:  []string{"status"},
				Unit:    "milliseconds",
				Buckets: []float64{1000, 5000, 10000, 30000, 60000, 120000, 300000, 600000},
			},
			{
				Name:   "qa.requests",
				Type:   "counter",
				Help:   "Number of QA requests",
				Labels: []string{"status"},
			},
			{
				Name:   "qa.orchestration.tool_calls",
				Type:   "counter",
				Help:   "Number of tool calls made during QA orchestration",
				Labels: []string{"tool_name"},
			},
		},
	})

	// Create traced HTTP client
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 600 * time.Second // Long timeout for browser operations

	qaAgent := &QAAgent{
		BaseAgent:  agent,
		httpClient: tracedClient,
	}

	qaAgent.registerCapabilities()

	return qaAgent, nil
}

// InitializeOrchestrator sets up the orchestrator after Discovery is available.
// memoryHooks and activityCoordinator are optional — nil disables memory.
func (q *QAAgent) InitializeOrchestrator(
	discovery core.Discovery,
	memoryHooks []core.PipelineHook,
	activityCoordinator core.ActivityCoordinator,
	skillRegistry orchestration.SkillRegistry,
) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if discovery == nil {
		return fmt.Errorf("discovery service not available")
	}

	skillConfig := orchestration.SkillConfig{
		Enabled: true,
		Bindings: []orchestration.SkillBinding{
			{
				Namespace: "qa", Name: "web-application-testing", Version: "published",
				Activation: orchestration.SkillActivationAlways, Required: true,
			},
		},
	}
	resolved, err := orchestration.ResolveOrchestratorConfig(orchestration.ConfigResolution{
		Environment: orchestration.EnvironmentCompatible,
		Options: []orchestration.OrchestratorOption{
			orchestration.WithSkills(skillConfig),
			orchestration.WithSkillRegistry(skillRegistry),
		},
	})
	if err != nil {
		return fmt.Errorf("resolve orchestrator configuration: %w", err)
	}
	config := resolved.Config
	config.RoutingMode = orchestration.ModeAutonomous
	config.SynthesisStrategy = orchestration.StrategyLLM
	config.MetricsEnabled = true
	config.EnableTelemetry = true
	if config.SynthesisAIOptions == nil {
		config.SynthesisAIOptions = &orchestration.AIOptionsOverride{}
	}
	config.SynthesisAIOptions.Temperature = orchestration.Float32Ptr(0.3) // Lower temperature for structured QA reports

	// Extended timeouts for browser operations (explore + test execution can be slow)
	config.ExecutionOptions.TotalTimeout = 30 * time.Minute
	config.ExecutionOptions.StepTimeout = 10 * time.Minute

	// Keep agent identity, lifecycle, and deployment policy in the system prompt.
	// Reusable browser-testing procedures are projected from the bound skill.
	config.PromptConfig = orchestration.PromptConfig{
		SystemInstructions: `<qa_agent_identity>
You are an autonomous QA testing agent that explores websites, generates Playwright tests from what it discovers, executes them, files bug tickets, and sends chat notifications.
</qa_agent_identity>

<qa_workflow_contract>
1. Phase 1 gathers current page evidence and checks for reusable tests in parallel.
2. Phase 2 executes a matching reusable test or a newly authored test suite.
3. Phase 3 records the completed run in project "QA" with labels "qa-automated,playwright", then notifies "#qa-tests" using the returned record reference and artifact location when available.
4. Continue planning after test execution until the current execution contains successful tracking-record and notification results. Set terminal to true only after both results exist.
5. Treat planned, attempted, remembered, and failed follow-up actions as incomplete.
</qa_workflow_contract>

<qa_workflow_example>
Explore and reusable-test discovery complete in phase 1; browser tests complete in phase 2; phase 3 creates the QA record and sends the dependent notification; only phase 3 is terminal.
</qa_workflow_example>`,

		Domain: "qa-testing",
		AdditionalTypeRules: []orchestration.TypeRule{
			{
				TypeNames:   []string{"url", "target_url"},
				JsonType:    "JSON strings",
				Example:     `"https://example.com"`,
				Description: "Full URL including protocol (https://)",
			},
			{
				TypeNames:   []string{"script"},
				JsonType:    "JSON strings",
				Example:     `"import { test, expect } from '@playwright/test';\ntest('homepage loads', async ({ page }) => {\n  await page.goto('/');\n  await expect(page).toHaveTitle(/Example/);\n});"`,
				Description: "Playwright test script content in TypeScript",
			},
			{
				TypeNames:   []string{"summary"},
				JsonType:    "JSON strings",
				Example:     `"[QA] u.cisco.com — 20 passed, 3 failed (2026-03-14)"`,
				Description: "Human-readable JIRA issue title. Compose a plain text string from test results — format: [QA] <site> — <pass count> passed, <fail count> failed (<YYYY-MM-DD>)",
			},
			{
				TypeNames:   []string{"project_key"},
				JsonType:    "JSON strings",
				Example:     `"QA"`,
				Description: "JIRA project key — always use QA for QA tickets",
			},
			{
				TypeNames:   []string{"channel"},
				JsonType:    "JSON strings",
				Example:     `"#qa-tests"`,
				Description: "Slack channel name including # prefix",
			},
			{
				TypeNames:   []string{"labels"},
				JsonType:    "JSON strings",
				Example:     `"qa-automated,playwright"`,
				Description: "Comma-separated JIRA labels",
			},
			{
				TypeNames:   []string{"blocks"},
				JsonType:    "JSON array",
				Example:     `[{"type":"section","text":{"type":"mrkdwn","text":"*QA Results*"}}]`,
				Description: "Slack Block Kit blocks for rich message formatting",
			},
		},
	}

	deps := orchestration.OrchestratorDependencies{
		Discovery:           discovery,
		AIClient:            q.AI,
		Logger:              q.Logger,
		Telemetry:           telemetry.GetTelemetryProvider(),
		EnableErrorAnalyzer: true,
		PipelineHooks:       memoryHooks,
		ActivityCoordinator: activityCoordinator,
	}

	// Use the compatibility constructor after resolving configuration so this
	// example retains its documented environment-backed debug stores. The
	// resolved config remains authoritative; the constructor only supplies the
	// legacy backend bootstrap until this example composes those stores itself.
	orch, err := orchestration.CreateOrchestrator(config, deps)
	if err != nil {
		return fmt.Errorf("failed to create orchestrator: %w", err)
	}

	ctx := context.Background()
	if err := orch.Start(ctx); err != nil {
		return fmt.Errorf("failed to start orchestrator: %w", err)
	}

	q.orchestrator = orch

	q.Logger.Info("Orchestrator initialized successfully", map[string]interface{}{
		"routing_mode":       config.RoutingMode,
		"synthesis_strategy": config.SynthesisStrategy,
		"total_timeout":      config.ExecutionOptions.TotalTimeout.String(),
		"step_timeout":       config.ExecutionOptions.StepTimeout.String(),
	})

	return nil
}

// ProcessQuery handles a non-streaming query through the orchestrator.
func (q *QAAgent) ProcessQuery(ctx context.Context, query string) (*orchestration.OrchestratorResponse, error) {
	startTime := time.Now()

	q.mu.RLock()
	orch := q.orchestrator
	q.mu.RUnlock()

	if orch == nil {
		return nil, fmt.Errorf("orchestrator not initialized")
	}

	q.Logger.InfoWithContext(ctx, "Processing QA query", map[string]interface{}{
		"operation": "process_query",
		"query_len": len(query),
	})

	telemetry.AddSpanEvent(ctx, "qa_orchestration.started",
		nil...,
	)

	result, err := orch.ProcessRequest(ctx, query, nil)
	if err != nil {
		q.Logger.ErrorWithContext(ctx, "QA orchestration failed", map[string]interface{}{
			"operation":   "process_query",
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		return nil, fmt.Errorf("orchestration failed: %w", err)
	}

	q.Logger.InfoWithContext(ctx, "QA query completed", map[string]interface{}{
		"operation":   "process_query",
		"request_id":  result.RequestID,
		"tools_used":  len(result.AgentsInvolved),
		"confidence":  result.Confidence,
		"duration_ms": time.Since(startTime).Milliseconds(),
	})

	return result, nil
}

// registerCapabilities registers the agent's HTTP endpoints.
func (q *QAAgent) registerCapabilities() {
	// Agent-as-Tool: Non-streaming query endpoint for agent-to-agent delegation.
	// Internal: true — hidden from this agent's own LLM planner to prevent self-calling.
	q.RegisterCapability(core.Capability{
		Name:     "qa_query",
		Internal: true,
		Description: "Autonomous QA testing agent that explores websites, generates Playwright test scripts using AI, " +
			"executes them, and reports results with artifact links. " +
			"Send a natural language request describing what to test and receive a comprehensive QA report.",
		Endpoint:    "/query",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     q.handleQuery,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "Explore and test https://example.com — check navigation, forms, and accessibility",
					Description: "Natural language QA testing request",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "success", Type: "boolean", Description: "Whether the request completed successfully"},
				{Name: "request_id", Type: "string", Description: "Unique identifier for this orchestration request"},
				{Name: "request", Type: "string", Description: "The original query that was submitted"},
				{Name: "response", Type: "string", Description: "AI-synthesized QA report with test results and artifact links"},
				{Name: "tools_used", Type: "array", Description: "List of tool names invoked during orchestration"},
				{Name: "execution_time", Type: "string", Description: "Total request duration as a human-readable string"},
				{Name: "confidence", Type: "number", Description: "Response confidence score between 0.0 and 1.0"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "usage", Type: "object", Description: "LLM token usage breakdown (prompt_tokens, completion_tokens, total_tokens)"},
			},
		},
	})

	// Health check with orchestrator status
	q.RegisterCapability(core.Capability{
		Name:        "health",
		Description: "Health check with orchestrator status",
		Endpoint:    "/health",
		Handler:     q.handleHealth,
		Internal:    true,
	})

	// Discover available tools
	q.RegisterCapability(core.Capability{
		Name:        "discover",
		Description: "Discover available tools for QA testing",
		Endpoint:    "/discover",
		Handler:     q.handleDiscover,
		Internal:    true,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "discovery_summary", Type: "object", Description: "Summary with total_components, tools count, agents count, and discovery_time (RFC3339)"},
				{Name: "tools", Type: "array", Description: "List of discovered tool ServiceInfo entries with id, name, type, address, port, capabilities"},
				{Name: "agents", Type: "array", Description: "List of discovered peer agent ServiceInfo entries (excludes self)"},
			},
		},
	})
}

// GetOrchestrator returns the orchestrator.
func (q *QAAgent) GetOrchestrator() *orchestration.AIOrchestrator {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.orchestrator
}
