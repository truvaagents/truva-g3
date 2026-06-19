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

// EventDrivenAgent receives Prometheus AlertManager webhooks and orchestrates
// autonomous incident response using AI-driven DAG planning.
//
// Architecture:
//   - Deterministic pipeline: webhook -> parse -> severity route -> dedup -> enqueue
//   - AI pipeline: worker BRPOP -> context enrichment -> orchestrator -> HITL -> cleanup
type EventDrivenAgent struct {
	*core.BaseAgent
	redisClient  *redis.Client
	httpClient   *http.Client
	orchestrator *orchestration.AIOrchestrator
	hitl         *HITLInfrastructure // HITL infrastructure (checkpoint store, controller, etc.)
	mu           sync.RWMutex
}

// NewEventDrivenAgent creates a new event-driven agent.
func NewEventDrivenAgent(redisClient *redis.Client) (*EventDrivenAgent, error) {
	baseAgent := core.NewBaseAgent("event-driven-agent")

	// Create AI client with provider chain for failover.
	// Auto-detect mode: discovers available providers from API keys and orders by priority.
	chainClient, err := ai.NewChainClient(
		ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
		ai.WithChainLogger(baseAgent.Logger),
	)
	if err != nil {
		baseAgent.Logger.Warn("Failed to create AI chain client, trying single provider", map[string]interface{}{
			"operation": "agent_init",
			"error":     err.Error(),
		})
		singleClient, err := ai.NewClient()
		if err != nil {
			baseAgent.Logger.Warn("AI client creation failed, orchestration will be limited", map[string]interface{}{
				"operation": "agent_init",
				"error":     err.Error(),
			})
		} else {
			baseAgent.AI = singleClient
		}
	} else {
		baseAgent.AI = chainClient
	}

	// Declare metrics for event-driven agent
	telemetry.DeclareMetrics("event-driven-agent", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{Name: "event_agent.alerts_received", Type: "counter", Labels: []string{"severity", "alertname"}},
			{Name: "event_agent.alerts_deduplicated", Type: "counter", Labels: []string{"alertname"}},
			{Name: "event_agent.alerts_enqueued", Type: "counter", Labels: []string{"severity"}},
			{Name: "event_agent.alerts_processed", Type: "counter", Labels: []string{"status"}},
			{Name: "event_agent.processing_duration_ms", Type: "histogram",
				Buckets: []float64{1000, 5000, 10000, 30000, 60000, 120000, 300000}},
			{Name: "event_agent.slack_notifications", Type: "counter", Labels: []string{"status"}},
			{Name: "event_agent.hitl_resume_completed", Type: "counter", Labels: []string{"status"}},
			{Name: "event_agent.hitl_resume_duration_ms", Type: "histogram",
				Buckets: []float64{1000, 5000, 10000, 30000, 60000, 120000, 300000}},
		},
	})

	// Create traced HTTP client for tool calls
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 60 * time.Second

	agent := &EventDrivenAgent{
		BaseAgent:   baseAgent,
		redisClient: redisClient,
		httpClient:  tracedClient,
	}

	// Register capabilities (all Internal: true)
	agent.registerCapabilities()

	return agent, nil
}

// InitializeOrchestrator sets up the AI orchestrator with HITL for write operations.
// memoryHooks are optional pipeline hooks for shared agent memory (episodic, knowledge, coordination).
// activityCoordinator is optional — enables real-time status updates at phase boundaries.
func (a *EventDrivenAgent) InitializeOrchestrator(
	discovery core.Discovery,
	hitl *HITLInfrastructure,
	hitlConfig orchestration.HITLConfig,
	memoryHooks []core.PipelineHook,
	activityCoordinator core.ActivityCoordinator,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if discovery == nil {
		return fmt.Errorf("discovery service not available")
	}

	config := orchestration.DefaultConfig()
	config.RoutingMode = orchestration.ModeAutonomous
	config.SynthesisStrategy = orchestration.StrategyLLM
	config.MetricsEnabled = true
	config.EnableTelemetry = true
	config.RequestIDPrefix = "event-driven-agent"

	// LLM token limits are loaded by DefaultConfig() into PlanAIOptions / SynthesisAIOptions.
	if config.SynthesisAIOptions == nil {
		config.SynthesisAIOptions = &orchestration.AIOptionsOverride{}
	}
	config.SynthesisAIOptions.Temperature = orchestration.Float32Ptr(0.3) // Lower temperature for precise incident response

	// Timeouts: controlled via env vars (TRUVAG3_ORCHESTRATION_TIMEOUT, TRUVAG3_EXECUTION_STEP_TIMEOUT,
	// TRUVAG3_ITERATIVE_PHASE_TIMEOUT) loaded by DefaultConfig(). Async worker tolerates long timeouts
	// because no human is waiting. The orchestrator auto-extends phase/step timeouts for orchestrator
	// capabilities (CapabilityOrchestrator) that may block on HITL approval.

	// Apply HITL config
	config.HITL = hitlConfig

	// PromptConfig: incident response domain
	config.PromptConfig = orchestration.PromptConfig{
		SystemInstructions: `You are an autonomous incident response agent for a microservices platform.
You investigate alerts by checking metrics, pod status, and logs, then diagnose root causes.
You coordinate remediation actions like pod restarts, scaling, and notifications.

CRITICAL: Each phase should contain only one type of work. Plan in this order across phases:
Phase 1 — Investigate: check metrics, pod status, and logs (parallel when independent)
Phase 2 — Remediate: take action for high/critical alerts, then verify success
Phase 3 — Notify: document in JIRA, then send Slack notification

Before creating a JIRA ticket, first check <agent_memory> for a recent ticket key (DEVOPS-NNN) on the same entity. If found, use add_comment directly. If no ticket is visible in memory, use search_issues to query JIRA for open tickets on the same entity or alert. If an open ticket exists, use add_comment with your findings and remediation results. If you remediated the issue, add a resolution comment. Only create a new ticket when both memory and JIRA confirm no recent ticket exists for this entity.

Place JIRA/Slack steps in a separate phase after investigation and remediation complete.`,

		Domain: "incident-response",
		CustomInstructions: []string{
			"For high latency alerts, query Prometheus metrics for the affected service endpoint",
			"For pod failures, inspect pod status and recent logs before restarting",
			"JIRA tickets use project_key 'DEVOPS' with severity, root cause, remediation, verification, and alert fingerprint",
			"Slack messages should read like a human-written incident note: alert summary, investigation findings with metric values and pod names, root cause, remediation taken, verification result, and the JIRA link (use the browse_url returned by create_issue or search_issues)",
			"Group related alerts by fingerprint to avoid duplicate investigations",
		},
	}

	deps := orchestration.OrchestratorDependencies{
		Discovery:           discovery,
		AIClient:            a.AI,
		Logger:              a.Logger,
		Telemetry:           telemetry.GetTelemetryProvider(),
		EnableErrorAnalyzer: true,
		PipelineHooks:       memoryHooks,
		ActivityCoordinator: activityCoordinator,
	}

	orch, err := orchestration.CreateOrchestrator(config, deps)
	if err != nil {
		return fmt.Errorf("failed to create orchestrator: %w", err)
	}

	// Wire HITL controller for write operations
	if hitl != nil {
		orch.SetInterruptController(hitl.Controller)
		a.hitl = hitl
		a.Logger.Info("HITL controller configured for write operations", map[string]interface{}{
			"operation":              "orchestrator_init",
			"enabled":                hitlConfig.Enabled,
			"sensitive_capabilities": hitlConfig.SensitiveCapabilities,
		})
	}

	ctx := context.Background()
	if err := orch.Start(ctx); err != nil {
		return fmt.Errorf("failed to start orchestrator: %w", err)
	}

	a.orchestrator = orch

	a.Logger.Info("Orchestrator initialized for incident response", map[string]interface{}{
		"operation":          "orchestrator_init",
		"routing_mode":       config.RoutingMode,
		"synthesis_strategy": config.SynthesisStrategy,
		"hitl_enabled":       hitlConfig.Enabled,
	})

	return nil
}

// GetOrchestrator returns the agent's orchestrator instance.
func (a *EventDrivenAgent) GetOrchestrator() *orchestration.AIOrchestrator {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.orchestrator
}
