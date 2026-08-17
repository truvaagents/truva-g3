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
	skillRegistry orchestration.SkillRegistry,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if discovery == nil {
		return fmt.Errorf("discovery service not available")
	}

	skillConfig := orchestration.SkillConfig{
		Enabled: true,
		Bindings: []orchestration.SkillBinding{
			{
				Namespace: "incident-response", Name: "evidence-driven-incident-response", Version: "published",
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

	// Keep agent identity, lifecycle, and deployment policy in the system prompt.
	// Reusable investigation and remediation procedures come from the bound skill.
	config.PromptConfig = orchestration.PromptConfig{
		SystemInstructions: `<incident_agent_identity>
You are an autonomous incident response agent for a microservices platform. You investigate alerts, coordinate justified remediation under configured approval policy, and complete operational follow-up.
</incident_agent_identity>

<incident_workflow_contract>
1. Use one work type per phase in this order: investigate; remediate and verify when evidence justifies a change; document and notify.
2. Complete the documentation and notification phase for every critical investigation, including a false positive or a no-change decision.
3. Group related alerts by fingerprint. Before creating a record, use recent memory to find a matching DEVOPS reference, then search the configured work-tracking system when memory has no match. Update an open match and create a DEVOPS record only after both checks find none.
4. Make the notification depend on the successful record action and include its returned human-facing URL.
5. Continue planning until the current execution contains successful record and notification results. Set terminal to true only after both results exist; a planned, attempted, remembered, or failed action remains incomplete.
</incident_workflow_contract>

<incident_workflow_example>
Phase 1 establishes whether the alert reflects a real incident. Phase 2 performs and verifies the smallest justified change, or produces a verified no-change decision. Phase 3 updates or creates the DEVOPS record and sends the dependent notification; only phase 3 is terminal.
</incident_workflow_example>`,

		Domain: "incident-response",
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

	// Use the compatibility constructor after resolving configuration so this
	// example retains its documented environment-backed debug stores. The
	// resolved config remains authoritative; the constructor only supplies the
	// legacy backend bootstrap until this example composes those stores itself.
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
