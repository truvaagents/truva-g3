package main

import (
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/resilience"
	"github.com/truvaagents/truva-g3/telemetry"
)

// ResearchAgent is an intelligent agent with built-in resilience capabilities.
// It demonstrates the active agent pattern with fault tolerance using the
// TruvaG3 resilience module.
//
// Key Resilience Features:
//   - Per-tool circuit breakers that protect against cascading failures
//   - Automatic retries with exponential backoff via resilience.RetryWithCircuitBreaker
//   - Timeout management using cb.ExecuteWithTimeout
//   - Graceful degradation returning partial results when tools fail
//
// Circuit Breaker Behavior:
//   - CLOSED: Normal operation, all requests pass through
//   - OPEN: Too many failures (>50% error rate), requests fail fast
//   - HALF-OPEN: Testing recovery with limited requests
type ResearchAgent struct {
	*core.BaseAgent
	aiClient        core.AIClient
	httpClient      *http.Client
	circuitBreakers map[string]*resilience.CircuitBreaker // Per-tool circuit breakers
	retryConfig     *resilience.RetryConfig               // Shared retry configuration
	cbMutex         sync.RWMutex                          // Thread-safe CB access

	// Instrumented AI client for LLM debug recording shutdown.
	// nil when TRUVAG3_LLM_DEBUG_ENABLED is not set.
	instrumentedClient *ai.InstrumentedAIClient
	debugRecorder      *telemetry.RedisLLMCallRecorder // nil when not enabled; Close() on shutdown
}

// ResearchRequest represents the input for research operations.
type ResearchRequest struct {
	// Topic is the research query or question (required)
	Topic string `json:"topic"`

	// Sources optionally specifies which tools to use
	Sources []string `json:"sources,omitempty"`

	// MaxResults limits the number of results to return (default: 5)
	MaxResults int `json:"max_results,omitempty"`

	// Metadata provides additional context or parameters
	Metadata map[string]string `json:"metadata,omitempty"`

	// AISynthesis enables AI-powered synthesis of tool results
	// When false: Returns raw tool data only (AI still used for tool selection)
	AISynthesis bool `json:"ai_synthesis,omitempty"`

	// WorkflowID enables tracking across multiple related requests
	WorkflowID string `json:"workflow_id,omitempty"`
}

// ResearchResponse represents the synthesized research output with resilience metadata.
type ResearchResponse struct {
	Topic          string                 `json:"topic"`
	Summary        string                 `json:"summary"`
	ToolsUsed      []string               `json:"tools_used"`
	Results        []ToolResult           `json:"results"`
	AIAnalysis     string                 `json:"ai_analysis,omitempty"`
	Confidence     float64                `json:"confidence"`
	ProcessingTime string                 `json:"processing_time"`
	WorkflowID     string                 `json:"workflow_id,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`

	// Resilience-specific fields
	Partial     bool     `json:"partial,omitempty"`      // True if some tools failed
	FailedTools []string `json:"failed_tools,omitempty"` // Names of tools that failed
	SuccessRate float64  `json:"success_rate,omitempty"` // Percentage of successful tool calls
}

// ToolResult represents the result from a single tool call.
type ToolResult struct {
	ToolName        string          `json:"tool_name"`
	Capability      string          `json:"capability"`
	Data            interface{}     `json:"data"`
	Success         bool            `json:"success"`
	Error           string          `json:"error,omitempty"`
	StructuredError *core.ToolError `json:"structured_error,omitempty"`
	Duration        string          `json:"duration"`
}

// NewResearchAgent creates a new AI-powered research assistant with resilience capabilities
func NewResearchAgent() (*ResearchAgent, error) {
	agent := core.NewBaseAgent("research-assistant-resilience")

	// Auto-configured AI client - detects from environment
	aiClient, err := ai.NewClient()
	if err != nil {
		log.Printf("AI client creation failed, using mock: %v", err)
	}

	if aiClient != nil {
		agent.AI = aiClient
	}

	// Wrap AI client for LLM debug recording (opt-in via TRUVAG3_LLM_DEBUG_ENABLED)
	var instrumentedClient *ai.InstrumentedAIClient
	var debugRecorder *telemetry.RedisLLMCallRecorder
	if os.Getenv("TRUVAG3_LLM_DEBUG_ENABLED") == "true" && aiClient != nil {
		// Three-Layer Resilience for agent-side recording:
		// Layer 1: Built-in retry with backoff (inside RedisLLMCallRecorder)
		// Layer 3: If Redis unavailable, fall back to NoOp — never fail agent startup
		var recErr error
		debugRecorder, recErr = telemetry.NewRedisLLMCallRecorder(
			telemetry.WithRecorderLogger(agent.Logger),
		)
		if recErr != nil {
			// Layer 3: Graceful fallback — log warning, continue without recording
			// Use log.Printf — agent.Logger is NoOpLogger until NewFramework runs
			log.Printf("⚠️  LLM debug recording unavailable: %v (check REDIS_URL)", recErr)
		} else {
			instrumentedClient = ai.NewInstrumentedClient(aiClient, debugRecorder,
				ai.WithComponentName("research-assistant-resilience"),
				ai.WithInstrumentedLogger(agent.Logger),
			)
			aiClient = instrumentedClient // Use instrumented client for all LLM calls
			agent.AI = aiClient
			// Use log.Printf — agent.Logger is NoOpLogger until NewFramework runs
			log.Printf("✅ LLM debug recording enabled (component=research-assistant-resilience, redis_db=7)")
		}
	}

	// Create the research agent with resilience support
	researchAgent := &ResearchAgent{
		BaseAgent:          agent,
		aiClient:           aiClient,
		instrumentedClient: instrumentedClient, // nil if not enabled
		debugRecorder:      debugRecorder,      // nil if not enabled; Close() on shutdown
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
				DisableKeepAlives:   false,
				ForceAttemptHTTP2:   true,
			},
		},
		circuitBreakers: make(map[string]*resilience.CircuitBreaker),
		retryConfig:     resilience.DefaultRetryConfig(), // Use framework defaults
	}

	// Register agent capabilities
	researchAgent.registerCapabilities()
	return researchAgent, nil
}

// getOrCreateCircuitBreaker returns existing CB or creates a new one using framework factory
func (r *ResearchAgent) getOrCreateCircuitBreaker(toolName string) *resilience.CircuitBreaker {
	r.cbMutex.RLock()
	if cb, exists := r.circuitBreakers[toolName]; exists {
		r.cbMutex.RUnlock()
		return cb
	}
	r.cbMutex.RUnlock()

	r.cbMutex.Lock()
	defer r.cbMutex.Unlock()

	// Double-check after acquiring write lock
	if cb, exists := r.circuitBreakers[toolName]; exists {
		return cb
	}

	// Use framework factory - auto-detects telemetry, injects logger
	cb, err := resilience.CreateCircuitBreaker(toolName, resilience.ResilienceDependencies{
		Logger: r.Logger, // Inject agent's logger
	})
	if err != nil {
		r.Logger.Error("Circuit breaker creation failed", map[string]interface{}{
			"tool":  toolName,
			"error": err.Error(),
		})
		return nil
	}

	// Listen to state changes for logging/alerting
	cb.AddStateChangeListener(func(name string, from, to resilience.CircuitState) {
		r.Logger.Warn("Circuit breaker state changed", map[string]interface{}{
			"circuit": name,
			"from":    from.String(),
			"to":      to.String(),
		})
	})

	r.circuitBreakers[toolName] = cb

	r.Logger.Info("Created circuit breaker for tool", map[string]interface{}{
		"tool":             toolName,
		"error_threshold":  0.5, // Default from resilience.DefaultConfig()
		"volume_threshold": 10,
		"sleep_window":     "30s",
	})

	return cb
}

// getCircuitBreakerStates returns a map of all CB states for health checks
func (r *ResearchAgent) getCircuitBreakerStates() map[string]interface{} {
	r.cbMutex.RLock()
	defer r.cbMutex.RUnlock()

	states := make(map[string]interface{})
	for name, cb := range r.circuitBreakers {
		// Use framework's comprehensive metrics
		states[name] = cb.GetMetrics()
	}
	return states
}

// registerCapabilities sets up all research-related capabilities
func (r *ResearchAgent) registerCapabilities() {
	// Capability 1: Resilient orchestrated research (AI + tool discovery + circuit breakers)
	// NOTE: This agent uses AI knowledge which may be outdated.
	// For real-time internet search, use web-search-tool instead.
	r.RegisterCapability(core.Capability{
		Name:        "research_topic",
		Description: "Orchestrates multiple tools with resilience patterns (circuit breakers + retries) using AI synthesis. Uses AI knowledge (may be outdated). For real-time internet search, use web-search-tool instead. Best for: fault-tolerant multi-step queries requiring data from several specialized tools, or generating creative answers when no specialized tool is appropriate.",
		InputTypes:  []string{"json", "text"},
		OutputTypes: []string{"json"},
		Handler:     r.handleResearchTopic,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "topic",
					Type:        "string",
					Example:     "latest developments in renewable energy",
					Description: "The research topic or question to investigate",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "sources",
					Type:        "array",
					Example:     `["weather-tool", "stock-market-tool"]`,
					Description: "Specific tool names to use for research",
				},
				{
					Name:        "max_results",
					Type:        "number",
					Example:     "5",
					Description: "Maximum number of results to return",
				},
				{
					Name:        "ai_synthesis",
					Type:        "boolean",
					Example:     "true",
					Description: "Enable AI synthesis of tool results into natural language",
				},
				{
					Name:        "workflow_id",
					Type:        "string",
					Example:     "research-12345",
					Description: "Optional workflow tracking identifier",
				},
			},
		},
	})

	// Capability 2: Component discovery and status
	r.RegisterCapability(core.Capability{
		Name:        "discover_tools",
		Description: "Discovers available tools and their capabilities",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     r.handleDiscoverTools,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "type",
					Type:        "string",
					Example:     "tool",
					Description: "Filter by component type: tool, agent, or workflow",
				},
			},
		},
	})

	// Capability 3: AI-powered analysis
	r.RegisterCapability(core.Capability{
		Name:        "analyze_data",
		Description: "Uses AI to analyze and synthesize data from multiple sources",
		InputTypes:  []string{"json", "text"},
		OutputTypes: []string{"json"},
		Handler:     r.handleAnalyzeData,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "data",
					Type:        "object",
					Example:     `{"results": [...]}`,
					Description: "The data to analyze (can be array or object)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "question",
					Type:        "string",
					Example:     "What are the key trends?",
					Description: "Specific question to answer about the data",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "analysis", Type: "string", Description: "AI-generated analysis of the provided data"},
				{Name: "model", Type: "string", Description: "Name of the AI model used for analysis"},
				{Name: "tokens_used", Type: "number", Description: "Total tokens consumed by the AI call"},
				{Name: "timestamp", Type: "string", Description: "ISO timestamp of when the analysis was performed"},
			},
		},
	})

	// Capability 4: Workflow orchestration
	r.RegisterCapability(core.Capability{
		Name:        "orchestrate_workflow",
		Description: "Orchestrates a multi-step workflow using discovered tools with resilience",
		Endpoint:    "/orchestrate",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     r.handleOrchestateWorkflow,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "workflow_name",
					Type:        "string",
					Example:     "market-research",
					Description: "Name of the workflow to execute",
				},
			},
		},
	})

	// Capability 5: Health check with circuit breaker states
	r.RegisterCapability(core.Capability{
		Name:        "health",
		Description: "Health check endpoint with circuit breaker states and dependency status",
		Endpoint:    "/health",
		InputTypes:  []string{},
		OutputTypes: []string{"json"},
		Handler:     r.handleHealth,
	})
}
