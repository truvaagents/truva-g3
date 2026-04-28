package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// ResearchAgent is an intelligent agent that demonstrates the active agent pattern.
// It can discover available tools via Redis, orchestrate multiple tool calls, and
// synthesize results using AI.
//
// Key Features:
//   - Tool Discovery: Automatically finds available tools in the service mesh
//   - Smart Orchestration: Routes requests to appropriate tools based on topic analysis
//   - Multi-Entity Support: Detects comparison queries and makes parallel tool calls
//   - Hybrid AI Mode: Uses tools when available, falls back to direct AI when not
//   - Performance: Connection pooling, caching, and parallel execution
type ResearchAgent struct {
	*core.BaseAgent
	aiClient   core.AIClient
	httpClient *http.Client // Shared HTTP client for connection pooling

	// Instrumented AI client for LLM debug recording shutdown.
	// nil when TRUVAG3_LLM_DEBUG_ENABLED is not set.
	instrumentedClient *ai.InstrumentedAIClient
	debugRecorder      *telemetry.RedisLLMCallRecorder // nil when not enabled; Close() on shutdown
}

// ResearchRequest represents the input for research operations.
// This is the main request format accepted by the research_topic capability.
type ResearchRequest struct {
	// Topic is the research query or question (required)
	// Examples: "weather in Paris", "Compare SF vs LA weather"
	Topic string `json:"topic"`

	// Sources optionally specifies which tools to use
	// If empty, agent automatically discovers and selects tools
	Sources []string `json:"sources,omitempty"`

	// MaxResults limits the number of results to return (default: 5)
	MaxResults int `json:"max_results,omitempty"`

	// Metadata provides additional context or parameters
	Metadata map[string]string `json:"metadata,omitempty"`

	// AISynthesis enables AI-powered synthesis of tool results
	// When true with no tools: AI answers directly (hybrid mode)
	// When true with tools: AI synthesizes tool results into natural language
	// When false: Returns raw tool data only (AI still used for tool selection)
	AISynthesis bool `json:"ai_synthesis,omitempty"`

	// WorkflowID enables tracking across multiple related requests
	WorkflowID string `json:"workflow_id,omitempty"`
}

// ResearchResponse represents the synthesized research output.
// Contains both raw tool results and AI-generated analysis when enabled.
type ResearchResponse struct {
	Topic          string                 `json:"topic"`                 // Original research topic
	Summary        string                 `json:"summary"`               // Text summary of findings
	ToolsUsed      []string               `json:"tools_used"`            // Names of tools that were called
	Results        []ToolResult           `json:"results"`               // Detailed results from each tool
	AIAnalysis     string                 `json:"ai_analysis,omitempty"` // AI-generated insights
	Confidence     float64                `json:"confidence"`            // Confidence score (0-1)
	ProcessingTime string                 `json:"processing_time"`       // Total time taken
	WorkflowID     string                 `json:"workflow_id,omitempty"` // Workflow tracking ID
	Metadata       map[string]interface{} `json:"metadata,omitempty"`    // Additional metadata
}

// ToolResult represents the result from a single tool call.
// For multi-entity queries, there will be one result per entity.
type ToolResult struct {
	ToolName        string          `json:"tool_name"`                  // Name of the tool that was called
	Capability      string          `json:"capability"`                 // Specific capability used
	Data            interface{}     `json:"data"`                       // Tool-specific response data
	Success         bool            `json:"success"`                    // Whether the call succeeded
	Error           string          `json:"error,omitempty"`            // Error message if failed
	StructuredError *core.ToolError `json:"structured_error,omitempty"` // Structured error from tool (if available)
	Duration        string          `json:"duration"`                   // Time taken for this call
}

// NewResearchAgent creates a new AI-powered research assistant
func NewResearchAgent() (*ResearchAgent, error) {
	agent := core.NewBaseAgent("research-assistant")

	// Auto-configured AI client - detects from environment
	aiClient, err := ai.NewClient() // Auto-detects best available provider
	if err != nil {
		log.Printf("AI client creation failed, using mock: %v", err)
		// In production, you might want to fail here or use a fallback
		// For the example, we'll continue without AI for basic orchestration
	}

	// Store AI client in agent
	if aiClient != nil {
		agent.AI = aiClient
	}

	// Wrap AI client for LLM debug recording (opt-in via TRUVAG3_LLM_DEBUG_ENABLED)
	var instrumentedClient *ai.InstrumentedAIClient
	var debugRecorder *telemetry.RedisLLMCallRecorder
	if os.Getenv("TRUVAG3_LLM_DEBUG_ENABLED") == "true" && aiClient != nil {
		var recErr error
		debugRecorder, recErr = telemetry.NewRedisLLMCallRecorder(
			telemetry.WithRecorderLogger(agent.Logger),
		)
		if recErr != nil {
			log.Printf("⚠️  LLM debug recording unavailable: %v (check REDIS_URL)", recErr)
		} else {
			instrumentedClient = ai.NewInstrumentedClient(aiClient, debugRecorder,
				ai.WithComponentName("research-assistant"),
				ai.WithInstrumentedLogger(agent.Logger),
			)
			aiClient = instrumentedClient
			agent.AI = aiClient
			log.Printf("✅ LLM debug recording enabled (component=research-assistant, redis_db=7)")
		}
	}

	researchAgent := &ResearchAgent{
		BaseAgent:          agent,
		aiClient:           aiClient,
		instrumentedClient: instrumentedClient,
		debugRecorder:      debugRecorder,
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
	}

	// Register agent capabilities
	researchAgent.registerCapabilities()
	return researchAgent, nil
}

// registerCapabilities sets up all research-related capabilities
func (r *ResearchAgent) registerCapabilities() {
	// Capability 1: Orchestrated research (AI + tool discovery)
	// NOTE: This agent uses AI knowledge which may be outdated.
	// For real-time internet search, use web-search-tool instead.
	r.RegisterCapability(core.Capability{
		Name:        "research_topic",
		Description: "Orchestrates multiple tools to answer complex questions using AI synthesis. Uses AI knowledge (may be outdated). For real-time internet search, use web-search-tool instead. Best for: combining results from multi-step queries requiring data from several specialized tools into a unified response, or generating creative answers when no specialized tool is appropriate.",
		InputTypes:  []string{"json", "text"},
		OutputTypes: []string{"json"},
		Handler:     r.handleResearchTopic,
		// Phase 2: Field hints for AI-powered payload generation
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
		// Phase 2: Field hints for filtering discovery
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "type",
					Type:        "string",
					Example:     "tool",
					Description: "Filter by component type: tool, agent, or workflow",
				},
				{
					Name:        "capabilities",
					Type:        "array",
					Example:     `["weather", "stocks"]`,
					Description: "Filter tools by required capabilities",
				},
			},
		},
	})

	// Capability 3: AI-powered analysis (if AI is available)
	r.RegisterCapability(core.Capability{
		Name:        "analyze_data",
		Description: "Uses AI to analyze and synthesize data from multiple sources",
		InputTypes:  []string{"json", "text"},
		OutputTypes: []string{"json"},
		Handler:     r.handleAnalyzeData,
		// Phase 2: Field hints for analysis requests
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
				{
					Name:        "format",
					Type:        "string",
					Example:     "summary",
					Description: "Output format: summary, detailed, or bullet-points",
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
		Description: "Orchestrates a multi-step workflow using discovered tools",
		Endpoint:    "/orchestrate", // Custom endpoint
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     r.handleOrchestateWorkflow,
		// Phase 2: Field hints for workflow definitions
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "workflow_name",
					Type:        "string",
					Example:     "market-research",
					Description: "Name of the workflow to execute",
				},
				{
					Name:        "steps",
					Type:        "array",
					Example:     `[{"tool": "weather-tool", "capability": "current_weather"}]`,
					Description: "Array of workflow steps with tool and capability names",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "parallel",
					Type:        "boolean",
					Example:     "false",
					Description: "Whether to execute steps in parallel (default: sequential)",
				},
				{
					Name:        "workflow_id",
					Type:        "string",
					Example:     "workflow-67890",
					Description: "Optional workflow tracking identifier",
				},
			},
		},
	})

	// Capability 5: Health check
	r.RegisterCapability(core.Capability{
		Name:        "health",
		Description: "Health check endpoint with dependency status",
		Endpoint:    "/health",
		InputTypes:  []string{},
		OutputTypes: []string{"json"},
		Handler:     r.handleHealth,
	})
}

// getAIProviderStatus returns the detected AI provider name
func getAIProviderStatus() string {
	// Check for common AI provider environment variables
	providers := []struct {
		name   string
		envVar string
	}{
		{"OpenAI", "OPENAI_API_KEY"},
		{"Groq", "GROQ_API_KEY"},
		{"Anthropic", "ANTHROPIC_API_KEY"},
		{"Gemini", "GEMINI_API_KEY"},
		{"DeepSeek", "DEEPSEEK_API_KEY"},
	}

	for _, provider := range providers {
		if os.Getenv(provider.envVar) != "" {
			return provider.name
		}
	}

	// Check for custom OpenAI-compatible endpoints
	if os.Getenv("OPENAI_BASE_URL") != "" {
		return "Custom OpenAI-Compatible"
	}

	return "None (will use mock responses)"
}
