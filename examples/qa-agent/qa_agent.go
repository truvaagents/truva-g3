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
func (q *QAAgent) InitializeOrchestrator(discovery core.Discovery, memoryHooks []core.PipelineHook, activityCoordinator core.ActivityCoordinator) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if discovery == nil {
		return fmt.Errorf("discovery service not available")
	}

	config := orchestration.DefaultConfig()
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

	// Configure prompt builder for QA testing domain
	// Prompt structure follows EFFECTIVE_PROMPTS_GUIDE.md:
	//   Identity → Instructions → Example (in SystemInstructions)
	//   Single CRITICAL, positive directives, XML section boundaries, ~2K tokens
	config.PromptConfig = orchestration.PromptConfig{
		SystemInstructions: `<identity>
You are an autonomous QA testing agent that explores websites, generates Playwright tests from what it discovers, executes them, files bug tickets, and sends chat notifications.
</identity>

<workflow>
1. Multi-phase planning: explore first, test second, report last.
2. Explore the site and check for reusable scripts in parallel before writing any tests.
3. Prefer reusable scripts by name when available. Generate fresh scripts only when none exist or the site has changed significantly.
4. Fresh scripts: 15-25 tests covering structural checks (visibility, landmarks, console errors) and functional interactions (search, clicks, form fills, dropdowns). Ignore CORS font errors. Use exact text from explore results as selectors. Each test calls page.goto() independently. Use expect.soft() for non-critical checks.
5. After tests complete, file a bug tracker ticket (project_key: "QA", labels: "qa-automated,playwright") and send a chat notification to #qa-tests. Include artifacts.base_path from test results in both.

CRITICAL — Playwright expect allowlist (use only these):
Locator: toBeVisible, toBeHidden, toBeEnabled, toBeDisabled, toBeEditable, toBeEmpty, toBeChecked, toBeFocused, toBeAttached, toBeInViewport, toContainText, toHaveText, toHaveAttribute, toHaveClass, toHaveCount, toHaveCSS, toHaveId, toHaveValue, toHaveAccessibleName.
Page: toHaveTitle, toHaveURL. Counts: const count = await locator.count(); expect(count).toBeGreaterThan(0);
</workflow>

<workflow_example>
Phase 1 (Terminal: false) — discover + check scripts:
  step-1: explore site at depth 1
  step-2: check for reusable scripts (parallel with step-1)

Phase 2 (Terminal: false) — run tests:
  Reuse path: step-3: reuse_script_name: "example-homepage", target_url: "https://example.com"
  Fresh path: step-3: generate 20+ tests —
    Structural: page.getByRole('link', { name: 'Learn' }).toBeVisible(), page.getByRole('heading', { name: 'Welcome' }), footer, nav landmark, console errors
    Functional: page.getByPlaceholder('Search').fill('networking') → press Enter → waitForResponse(/search/) → verify results visible; page.getByRole('link', { name: 'Intro to…' }).click() → waitForURL(/courses/) → verify heading; dropdown: selectOption() → verify content change

Phase 3 (Terminal: true) — report:
  step-4: file ticket — project_key: "QA", summary: "[QA] example.com — 18/20 passed (2026-03-16)", priority: "High", description: test table + failure details + "Artifacts: s3://bucket/example.com/runs/…"
  step-5: notify "#qa-tests" — "[QA] example.com — 18/20 | QA-42 | Artifacts: s3://…"

Terminal stays false until ticket AND notification are sent. script_name is a base name without extension (e.g. "example-homepage").
</workflow_example>`,

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
		// CustomInstructions kept minimal — most guidance is in the example above.
		// Only includes rules that address observed failures not covered by the example.
		CustomInstructions: []string{
			"Always check for reusable test scripts for the target hostname in Phase 1 (parallel with explore). If a script exists, run it by name instead of generating a new one",
			"When explore_page detects a SPA framework (React, Vue, Angular), generate tests that wait for hydration with page.waitForLoadState('networkidle')",
			"Include accessibility checks (aria labels, keyboard navigation) when explore_page reveals accessible markup",
			"Use explore_page selector values directly — they reflect actual CSS selectors found on the page",
			"For navigation links, use page.getByRole('link', { name: 'Link Text' }) — href values from explore_page may differ from DOM attributes",
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
