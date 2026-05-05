package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
)

// TravelResearchAgent is an intelligent agent that uses AI-powered orchestration
// to coordinate multiple travel-related tools dynamically.
//
// Key Features:
//   - AI-driven request routing through orchestration.AIOrchestrator
//   - Dynamic tool discovery and coordination
//   - Predefined travel research workflows (DAG-based)
//   - Natural language request processing
//   - AI synthesis of multi-tool results
//
// The agent demonstrates two orchestration modes:
//  1. Workflow Mode: Predefined DAG-based workflows for common travel scenarios
//  2. Dynamic Mode: AI generates execution plans from natural language requests
type TravelResearchAgent struct {
	*core.BaseAgent
	orchestrator  *orchestration.AIOrchestrator
	workflows     map[string]*TravelWorkflow // Predefined travel workflows
	workflowMutex sync.RWMutex
	httpClient    *http.Client
}

// TravelWorkflow represents a predefined workflow for travel research
type TravelWorkflow struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Steps       []WorkflowStep         `json:"steps"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// WorkflowStep represents a single step in a travel workflow
type WorkflowStep struct {
	ID          string                 `json:"id"`
	ToolName    string                 `json:"tool_name"`
	Capability  string                 `json:"capability"`
	Description string                 `json:"description"`
	DependsOn   []string               `json:"depends_on,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// OrchestrationRequest represents a request for orchestrated research
type OrchestrationRequest struct {
	// Request is the natural language query (required for dynamic mode)
	Request string `json:"request"`

	// WorkflowName specifies a predefined workflow to execute (optional)
	WorkflowName string `json:"workflow_name,omitempty"`

	// Parameters are workflow-specific parameters
	Parameters map[string]interface{} `json:"parameters,omitempty"`

	// AISynthesis enables AI synthesis of results (default: true)
	// When false: Returns raw tool data only (AI still used for planning)
	AISynthesis bool `json:"ai_synthesis"`

	// Metadata provides additional context
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// OrchestrationResponse represents the response from orchestrated research
type OrchestrationResponse struct {
	RequestID     string                 `json:"request_id"`
	Request       string                 `json:"request"`
	Response      string                 `json:"response"`
	ToolsUsed     []string               `json:"tools_used"`
	WorkflowUsed  string                 `json:"workflow_used,omitempty"`
	ExecutionTime string                 `json:"execution_time"`
	StepResults   []StepResultSummary    `json:"step_results,omitempty"`
	Confidence    float64                `json:"confidence"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// StepResultSummary provides a summary of each step's execution
type StepResultSummary struct {
	StepID   string `json:"step_id"`
	ToolName string `json:"tool_name"`
	Success  bool   `json:"success"`
	Duration string `json:"duration"`
	Error    string `json:"error,omitempty"`
}

// NewTravelResearchAgent creates a new AI-powered travel research agent
func NewTravelResearchAgent() (*TravelResearchAgent, error) {
	agent := core.NewBaseAgent("travel-research-orchestration")

	// Create Chain Client for automatic failover between providers.
	// Auto-detect mode: discovers available providers from API keys and orders by priority.
	// If primary fails (e.g., rate limit), automatically tries the next provider.
	//
	// To explicitly control the chain order instead of auto-detecting, use WithProviderChain:
	//   ai.NewChainClient(
	//       ai.WithProviderChain("openai", "anthropic", "openai.groq"),  // explicit order
	//       ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
	//       ai.WithChainLogger(agent.Logger),
	//   )
	aiClient, err := ai.NewChainClient(
		ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
		ai.WithChainLogger(agent.Logger),
	)
	if err != nil {
		// AI is optional - orchestration works without it for predefined workflows
		agent.Logger.Warn("AI chain client creation failed, some features will be limited", map[string]interface{}{
			"error": err.Error(),
		})
	} else {
		agent.AI = aiClient
	}

	// Declare metrics this agent will emit for observability
	telemetry.DeclareMetrics("travel-orchestration-agent", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:    "orchestration.workflow.duration_ms",
				Type:    "histogram",
				Help:    "Workflow execution duration in milliseconds",
				Labels:  []string{"workflow", "status"},
				Unit:    "milliseconds",
				Buckets: []float64{100, 500, 1000, 5000, 10000, 30000, 60000},
			},
			{
				Name:   "orchestration.workflow.executions",
				Type:   "counter",
				Help:   "Number of workflow executions",
				Labels: []string{"workflow", "status"},
			},
			{
				Name:   "orchestration.step.executions",
				Type:   "counter",
				Help:   "Number of workflow step executions",
				Labels: []string{"workflow", "step", "tool"},
			},
			{
				Name:    "orchestration.step.duration_ms",
				Type:    "histogram",
				Help:    "Individual step execution duration in milliseconds",
				Labels:  []string{"workflow", "step", "tool"},
				Unit:    "milliseconds",
				Buckets: []float64{50, 100, 250, 500, 1000, 2000, 5000},
			},
			{
				Name:   "orchestration.tool_calls",
				Type:   "counter",
				Help:   "Number of tool calls made during orchestration",
				Labels: []string{"tool_name"},
			},
			{
				Name:   "orchestration.ai_synthesis",
				Type:   "counter",
				Help:   "Number of AI synthesis operations",
				Labels: []string{"status"},
			},
			{
				Name:    "orchestration.ai_synthesis.duration_ms",
				Type:    "histogram",
				Help:    "AI synthesis operation duration in milliseconds",
				Unit:    "milliseconds",
				Buckets: []float64{100, 500, 1000, 2000, 5000, 10000},
			},
			{
				Name: "orchestration.tools.discovered",
				Type: "gauge",
				Help: "Number of tools discovered via service discovery",
			},
			{
				Name:   "orchestration.natural_requests",
				Type:   "counter",
				Help:   "Number of natural language orchestration requests",
				Labels: []string{"status"},
			},
		},
	})

	// Create traced HTTP client for distributed tracing context propagation
	// This ensures trace context (W3C TraceContext headers) is propagated to downstream tools
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 300 * time.Second // Increased for complex multi-tool orchestration

	// Create the travel research agent
	travelAgent := &TravelResearchAgent{
		BaseAgent:  agent,
		workflows:  make(map[string]*TravelWorkflow),
		httpClient: tracedClient,
	}

	// Register predefined workflows
	travelAgent.registerPredefinedWorkflows()

	// Register agent capabilities
	travelAgent.registerCapabilities()

	return travelAgent, nil
}

// InitializeOrchestrator sets up the AI orchestrator after discovery is available
// This must be called after the framework is created and discovery is initialized
//
// This example demonstrates the PromptBuilder feature from the orchestration module:
//   - Uses CreateOrchestrator factory for proper dependency injection
//   - Configures travel-specific type rules for coordinates, currencies, etc.
//   - Adds custom instructions for travel research context
//
// See: docs/orchestration/LLM_PLANNING_PROMPT_GUIDE.md for full documentation
func (t *TravelResearchAgent) InitializeOrchestrator(discovery core.Discovery) error {
	if discovery == nil {
		return fmt.Errorf("discovery service is required for orchestration")
	}

	// Create orchestrator configuration with PromptBuilder settings
	config := orchestration.DefaultConfig()
	config.RoutingMode = orchestration.ModeAutonomous
	config.SynthesisStrategy = orchestration.StrategyLLM
	config.MetricsEnabled = true
	config.EnableTelemetry = true

	// LLM token limits are loaded by DefaultConfig() into PlanAIOptions / SynthesisAIOptions.
	if config.SynthesisAIOptions == nil {
		config.SynthesisAIOptions = &orchestration.AIOptionsOverride{}
	}
	config.SynthesisAIOptions.Temperature = orchestration.Float32Ptr(0.5) // Lower temperature for focused, deterministic responses

	// Increase timeouts for complex multi-tool orchestration scenarios
	config.ExecutionOptions.TotalTimeout = 5 * time.Minute  // Overall orchestration timeout (default: 2m)
	config.ExecutionOptions.StepTimeout = 120 * time.Second // Per-step timeout for AI planning (default: 30s)

	// Configure PromptBuilder with travel-specific type rules
	// This ensures the LLM generates execution plans with correct JSON types
	// See: docs/orchestration/LLM_PLANNING_PROMPT_GUIDE.md
	config.PromptConfig = orchestration.PromptConfig{
		// SystemInstructions defines the orchestrator's persona and behavioral context.
		// This becomes the primary identity, with the orchestrator role as secondary.
		// Similar to LangChain's system_prompt, AutoGen's system_message, or OpenAI's instructions.
		SystemInstructions: `You are a helpful travel planning assistant.
You help users plan trips by coordinating information from various travel services.
Provide practical, actionable recommendations and prioritize traveler needs.`,

		// Domain helps the LLM understand the context
		Domain: "travel",

		// Domain-specific type rules that add information beyond core type rules
		AdditionalTypeRules: []orchestration.TypeRule{
			{
				TypeNames:   []string{"currency_code", "from_currency", "to_currency", "base_currency"},
				JsonType:    "JSON strings",
				Example:     `"USD"`,
				Description: "ISO 4217 currency codes (USD, EUR, JPY, etc.)",
			},
		},

		// Custom instructions specific to travel research orchestration
		CustomInstructions: []string{
			"For weather queries, always geocode the location first to get coordinates",
			"For currency conversion, extract the destination country's currency code",
			"Prefer parallel execution when steps are independent (e.g., news + country-info)",
			"Include destination name in news search queries for relevant results",
		},
	}

	// Use the factory pattern with dependency injection
	// This automatically:
	// - Creates the appropriate PromptBuilder based on config
	// - Injects logger and telemetry
	// - Sets up telemetry metrics for prompt building
	// - Configures LLM-based error analysis (Layer 3)
	deps := orchestration.OrchestratorDependencies{
		Discovery:           discovery,
		AIClient:            t.AI,
		Logger:              t.Logger,
		Telemetry:           telemetry.GetTelemetryProvider(),
		EnableErrorAnalyzer: true, // Enable LLM-based error analysis (Layer 3)
	}

	orchestrator, err := orchestration.CreateOrchestrator(config, deps)
	if err != nil {
		return fmt.Errorf("failed to create orchestrator: %w", err)
	}
	t.orchestrator = orchestrator

	// Log PromptBuilder configuration for debugging
	t.Logger.Debug("PromptBuilder configured", map[string]interface{}{
		"domain":                  config.PromptConfig.Domain,
		"has_system_instructions": config.PromptConfig.SystemInstructions != "",
		"additional_type_rules":   len(config.PromptConfig.AdditionalTypeRules),
		"custom_instructions":     len(config.PromptConfig.CustomInstructions),
	})

	// Start the orchestrator
	ctx := context.Background()
	if err := t.orchestrator.Start(ctx); err != nil {
		return fmt.Errorf("failed to start orchestrator: %w", err)
	}

	t.Logger.Info("Orchestrator initialized successfully", map[string]interface{}{
		"routing_mode":       config.RoutingMode,
		"synthesis_strategy": config.SynthesisStrategy,
		"prompt_domain":      config.PromptConfig.Domain,
		"type_rules_count":   len(config.PromptConfig.AdditionalTypeRules),
	})

	return nil
}

// registerPredefinedWorkflows sets up the predefined travel research workflows
func (t *TravelResearchAgent) registerPredefinedWorkflows() {
	// Workflow 1: Comprehensive Travel Planning
	t.workflows["travel-research"] = &TravelWorkflow{
		Name:        "travel-research",
		Description: "Comprehensive travel research including weather, currency, country info, and local news",
		Steps: []WorkflowStep{
			{
				ID:          "geocode",
				ToolName:    "geocoding-tool",
				Capability:  "geocode_location",
				Description: "Get coordinates for the destination",
				Parameters: map[string]interface{}{
					"location": "{{destination}}",
				},
			},
			{
				ID:          "weather",
				ToolName:    "weather-tool-v2",
				Capability:  "get_current_weather",
				Description: "Get current weather at destination",
				DependsOn:   []string{"geocode"},
				Parameters: map[string]interface{}{
					"lat": "{{geocode.response.data.lat}}",
					"lon": "{{geocode.response.data.lon}}",
				},
			},
			{
				ID:          "country-info",
				ToolName:    "country-info-tool",
				Capability:  "get_country_info",
				Description: "Get country information",
				Parameters: map[string]interface{}{
					"country": "{{country}}",
				},
			},
			{
				ID:          "currency",
				ToolName:    "currency-tool",
				Capability:  "convert_currency",
				Description: "Get currency exchange rates",
				DependsOn:   []string{"country-info"},
				Parameters: map[string]interface{}{
					"from":   "{{base_currency}}",
					"to":     "{{country-info.response.data.currency.code}}",
					"amount": "{{amount}}",
				},
			},
			{
				ID:          "news",
				ToolName:    "news-tool",
				Capability:  "search_news",
				Description: "Get relevant news about the destination",
				Parameters: map[string]interface{}{
					"query":       "{{destination}} travel",
					"max_results": 5,
				},
			},
		},
		Metadata: map[string]interface{}{
			"category":     "travel",
			"version":      "1.0",
			"parallel_ops": []string{"country-info", "news"},
		},
	}

	// Workflow 2: Quick Weather Check
	t.workflows["quick-weather"] = &TravelWorkflow{
		Name:        "quick-weather",
		Description: "Quick weather lookup for a destination",
		Steps: []WorkflowStep{
			{
				ID:          "geocode",
				ToolName:    "geocoding-tool",
				Capability:  "geocode_location",
				Description: "Get coordinates for the location",
				Parameters: map[string]interface{}{
					"location": "{{location}}",
				},
			},
			{
				ID:          "weather",
				ToolName:    "weather-tool-v2",
				Capability:  "get_current_weather",
				Description: "Get current weather",
				DependsOn:   []string{"geocode"},
				Parameters: map[string]interface{}{
					"lat": "{{geocode.response.data.lat}}",
					"lon": "{{geocode.response.data.lon}}",
				},
			},
		},
		Metadata: map[string]interface{}{
			"category": "weather",
			"version":  "1.0",
		},
	}

	// Workflow 3: Currency Exchange
	t.workflows["currency-check"] = &TravelWorkflow{
		Name:        "currency-check",
		Description: "Check currency exchange rates between two currencies",
		Steps: []WorkflowStep{
			{
				ID:          "exchange",
				ToolName:    "currency-tool",
				Capability:  "convert_currency",
				Description: "Convert currency",
				Parameters: map[string]interface{}{
					"from":   "{{from_currency}}",
					"to":     "{{to_currency}}",
					"amount": "{{amount}}",
				},
			},
		},
		Metadata: map[string]interface{}{
			"category": "finance",
			"version":  "1.0",
		},
	}

	t.Logger.Info("Registered predefined workflows", map[string]interface{}{
		"workflow_count": len(t.workflows),
		"workflows":      []string{"travel-research", "quick-weather", "currency-check"},
	})
}

// registerCapabilities sets up all orchestration-related capabilities
func (t *TravelResearchAgent) registerCapabilities() {
	// Track registered capabilities for debug logging
	registeredCaps := []string{}

	// Capability 1: Natural language orchestration
	// Internal: true prevents this capability from appearing in the LLM catalog,
	// which avoids self-referential orchestration loops where the LLM might call
	// this endpoint recursively instead of delegating to the actual tools.
	t.RegisterCapability(core.Capability{
		Name:        "orchestrate_natural",
		Description: "Process natural language travel requests using AI-powered orchestration",
		Endpoint:    "/orchestrate/natural",
		InputTypes:  []string{"json", "text"},
		OutputTypes: []string{"json"},
		Handler:     t.handleNaturalOrchestration,
		Internal:    true, // Exclude from LLM catalog to prevent recursive self-calls
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "request",
					Type:        "string",
					Example:     "I'm planning a trip to Tokyo next week. What's the weather like and what currency should I exchange?",
					Description: "Natural language travel research request",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "ai_synthesis",
					Type:        "boolean",
					Example:     "true",
					Description: "Enable AI synthesis of tool results into natural language (default: true)",
				},
				{
					Name:        "metadata",
					Type:        "object",
					Example:     `{"user_preferences": {"currency": "USD"}}`,
					Description: "Additional context and preferences",
				},
			},
		},

		// Phase 2b: Output schema — fields match OrchestrationResponse JSON tags.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "request_id", Type: "string", Description: "Unique identifier for this orchestration run"},
				{Name: "request", Type: "string", Description: "Echo of the natural language request that was processed"},
				{Name: "response", Type: "string", Description: "AI-synthesized natural language answer"},
				{Name: "tools_used", Type: "array", Description: "Names of tools/agents invoked during orchestration"},
				{Name: "execution_time", Type: "string", Description: "Total request duration as a Go duration string"},
				{Name: "confidence", Type: "number", Description: "Confidence score between 0.0 and 1.0"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "workflow_used", Type: "string", Description: "Name of the workflow used (only set by workflow paths)"},
				{Name: "step_results", Type: "array", Description: "Per-step execution summaries with step_id, tool_name, success, duration, error"},
				{Name: "metadata", Type: "object", Description: "Additional metadata carried through from the request"},
			},
		},
	})
	registeredCaps = append(registeredCaps, "orchestrate_natural")

	// Capability 2: Execute predefined travel workflow
	// Internal: true to prevent LLM from triggering recursive orchestration
	t.RegisterCapability(core.Capability{
		Name:        "execute_workflow",
		Description: "Execute a predefined travel research workflow",
		Endpoint:    "/orchestrate/travel-research",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleWorkflowExecution,
		Internal:    true, // Exclude from LLM catalog to prevent recursive orchestration
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "destination",
					Type:        "string",
					Example:     "Tokyo, Japan",
					Description: "Travel destination",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "base_currency",
					Type:        "string",
					Example:     "USD",
					Description: "Your home currency for exchange rates",
				},
				{
					Name:        "amount",
					Type:        "number",
					Example:     "1000",
					Description: "Amount to convert",
				},
				{
					Name:        "workflow_name",
					Type:        "string",
					Example:     "travel-research",
					Description: "Specific workflow to execute (default: travel-research)",
				},
			},
		},

		// Phase 2b: Output schema — fields match OrchestrationResponse JSON tags.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "request_id", Type: "string", Description: "Unique identifier for this workflow run"},
				{Name: "request", Type: "string", Description: "Synthesized description of the workflow that ran"},
				{Name: "response", Type: "string", Description: "Formatted raw results from the workflow steps"},
				{Name: "tools_used", Type: "array", Description: "Names of tools invoked in workflow steps"},
				{Name: "execution_time", Type: "string", Description: "Total request duration as a Go duration string"},
				{Name: "confidence", Type: "number", Description: "Confidence score between 0.0 and 1.0"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "workflow_used", Type: "string", Description: "Name of the executed workflow (e.g., travel-research)"},
				{Name: "step_results", Type: "array", Description: "Per-step execution summaries with step_id, tool_name, success, duration, error"},
				{Name: "metadata", Type: "object", Description: "Additional metadata carried through from the request"},
			},
		},
	})
	registeredCaps = append(registeredCaps, "execute_workflow")

	// Capability 3: Custom workflow execution
	// Internal: true prevents LLM from calling this directly (caused self-referential loops)
	t.RegisterCapability(core.Capability{
		Name:        "execute_custom",
		Description: "Execute a custom workflow defined in the request",
		Endpoint:    "/orchestrate/custom",
		Internal:    true, // Exclude from LLM catalog - orchestration endpoint
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleCustomWorkflow,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "steps",
					Type:        "array",
					Example:     `[{"tool": "weather-tool-v2", "capability": "get_weather", "params": {"lat": 35.6762, "lon": 139.6503}}]`,
					Description: "Array of workflow steps to execute",
				},
			},
		},

		// Phase 2b: Output schema — shape written by handleCustomWorkflow.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "plan_id", Type: "string", Description: "Generated plan identifier (custom-{unix_nano})"},
				{Name: "success", Type: "boolean", Description: "Whether all steps completed successfully"},
				{Name: "steps", Type: "array", Description: "Per-step orchestration.StepResult entries with step_id, agent_name, capability, namespace, instruction, parameters, response, success, error, duration, attempts, start_time, end_time, metadata, skipped, skip_reason"},
				{Name: "duration", Type: "string", Description: "Total orchestrator duration as a Go duration string"},
				{Name: "execution_time", Type: "string", Description: "End-to-end request duration as a Go duration string"},
			},
		},
	})
	registeredCaps = append(registeredCaps, "execute_custom")

	// Capability 4: List available workflows
	// Internal: true - utility endpoint, not a tool for LLM
	t.RegisterCapability(core.Capability{
		Name:        "list_workflows",
		Description: "List all available predefined workflows",
		Endpoint:    "/orchestrate/workflows",
		Internal:    true, // Exclude from LLM catalog - admin/utility endpoint
		InputTypes:  []string{},
		OutputTypes: []string{"json"},
		Handler:     t.handleListWorkflows,

		// Phase 2b: Output schema — shape written by handleListWorkflows.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "workflows", Type: "array", Description: "List of workflow definitions with name, description, step_count, steps, metadata"},
				{Name: "count", Type: "number", Description: "Number of workflows returned"},
			},
		},
	})
	registeredCaps = append(registeredCaps, "list_workflows")

	// Capability 5: Get execution history
	// Internal: true - utility endpoint, not a tool for LLM
	t.RegisterCapability(core.Capability{
		Name:        "get_history",
		Description: "Get recent orchestration execution history",
		Endpoint:    "/orchestrate/history",
		Internal:    true, // Exclude from LLM catalog - admin/utility endpoint
		InputTypes:  []string{},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetHistory,

		// Phase 2b: Output schema — shape written by handleGetHistory.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "history", Type: "array", Description: "List of recent orchestrator execution entries (shape depends on orchestrator.GetExecutionHistory)"},
				{Name: "count", Type: "number", Description: "Number of history entries returned"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "message", Type: "string", Description: "Informational message (e.g., when orchestrator is not initialized)"},
			},
		},
	})
	registeredCaps = append(registeredCaps, "get_history")

	// Capability 6: Health check with orchestrator status
	// Internal: true - utility endpoint, not a tool for LLM
	t.RegisterCapability(core.Capability{
		Name:        "health",
		Description: "Health check with orchestrator status and metrics",
		Endpoint:    "/health",
		Internal:    true, // Exclude from LLM catalog - utility endpoint
		InputTypes:  []string{},
		OutputTypes: []string{"json"},
		Handler:     t.handleHealth,
	})
	registeredCaps = append(registeredCaps, "health")

	// Capability 7: Discover available tools
	// Internal: true - debugging/discovery endpoint, not a tool for LLM
	t.RegisterCapability(core.Capability{
		Name:        "discover_tools",
		Description: "Discovers available tools and their capabilities",
		Endpoint:    "/discover",
		Internal:    true, // Exclude from LLM catalog - discovery/debug endpoint
		InputTypes:  []string{},
		OutputTypes: []string{"json"},
		Handler:     t.handleDiscoverTools,

		// Phase 2b: Output schema — shape written by handleDiscoverTools.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "discovery_summary", Type: "object", Description: "Summary with total_components, tools count, agents count, and discovery_time (RFC3339)"},
				{Name: "tools", Type: "array", Description: "List of discovered tool ServiceInfo entries with id, name, type, address, port, capabilities"},
				{Name: "agents", Type: "array", Description: "List of discovered peer agent ServiceInfo entries (excludes self)"},
			},
		},
	})
	registeredCaps = append(registeredCaps, "discover_tools")

	// Log capability registration summary
	t.Logger.Debug("Registered capabilities", map[string]interface{}{
		"count":        len(registeredCaps),
		"capabilities": registeredCaps,
	})
}

// parseInput parses input into a struct
func (t *TravelResearchAgent) parseInput(input interface{}, target interface{}) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
