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

// ResearchAgent is an intelligent agent that demonstrates the active agent pattern with telemetry.
// It can discover available tools via Redis, orchestrate multiple tool calls, and
// synthesize results using AI, while emitting comprehensive metrics for observability.
//
// Key Features:
//   - Tool Discovery: Automatically finds available tools in the service mesh
//   - Smart Orchestration: Routes requests to appropriate tools based on topic analysis
//   - Multi-Entity Support: Detects comparison queries and makes parallel tool calls
//   - Hybrid AI Mode: Uses tools when available, falls back to direct AI when not
//   - Performance: Connection pooling, caching, and parallel execution
//   - Telemetry: Comprehensive metrics, tracing, and health monitoring
type ResearchAgent struct {
	*core.BaseAgent
	aiClient    core.AIClient
	httpClient  *http.Client     // Shared HTTP client for connection pooling
	SchemaCache core.SchemaCache // Optional schema cache for Phase 3 validation

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
	ToolName   string      `json:"tool_name"`       // Name of the tool that was called
	Capability string      `json:"capability"`      // Specific capability used
	Data       interface{} `json:"data"`            // Tool-specific response data
	Success    bool        `json:"success"`         // Whether the call succeeded
	Error      string      `json:"error,omitempty"` // Error message if failed
	Duration   string      `json:"duration"`        // Time taken for this call
}

// NewResearchAgent creates a new AI-powered research assistant with telemetry.
// The agent name is read from TRUVAG3_K8S_SERVICE_NAME for consistent naming
// across Redis registration, telemetry, and Kubernetes resources.
func NewResearchAgent() (*ResearchAgent, error) {
	serviceName := os.Getenv("TRUVAG3_K8S_SERVICE_NAME")
	agent := core.NewBaseAgent(serviceName)

	// Auto-configured AI client - detects from environment
	// Pass telemetry to enable distributed tracing for AI operations
	// Pass logger to enable structured logging with trace ID correlation
	aiClient, err := ai.NewClient(
		ai.WithTelemetry(telemetry.GetTelemetryProvider()),
		ai.WithLogger(agent.Logger),
	)
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
			// Use log.Printf — agent.Logger is NoOpLogger until NewFramework runs
			log.Printf("⚠️  LLM debug recording unavailable: %v (check REDIS_URL)", recErr)
		} else {
			instrumentedClient = ai.NewInstrumentedClient(aiClient, debugRecorder,
				ai.WithComponentName(serviceName),
				ai.WithInstrumentedLogger(agent.Logger),
			)
			aiClient = instrumentedClient
			agent.AI = aiClient
			// Use log.Printf — agent.Logger is NoOpLogger until NewFramework runs
			log.Printf("✅ LLM debug recording enabled (component=%s, redis_db=7)", serviceName)
		}
	}

	// NEW: Declare metrics this agent will emit
	// These declarations help with validation and documentation
	telemetry.DeclareMetrics("research-agent", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:    "agent.research.duration_ms",
				Type:    "histogram",
				Help:    "Research operation duration in milliseconds",
				Labels:  []string{"topic", "status"},
				Unit:    "milliseconds",
				Buckets: []float64{100, 500, 1000, 5000, 10000, 30000},
			},
			{
				Name:   "agent.research.tools_called",
				Type:   "counter",
				Help:   "Number of tool calls made during research",
				Labels: []string{"tool_name"},
			},
			{
				Name:   "agent.research.ai_tokens",
				Type:   "gauge",
				Help:   "AI tokens used in synthesis",
				Labels: []string{"provider", "operation"},
			},
			{
				Name:    "agent.tool_call.duration_ms",
				Type:    "histogram",
				Help:    "Individual tool call duration in milliseconds",
				Labels:  []string{"tool"},
				Unit:    "milliseconds",
				Buckets: []float64{50, 100, 250, 500, 1000, 2000, 5000},
			},
			{
				Name:   "agent.tool_call.errors",
				Type:   "counter",
				Help:   "Tool call failures by error type",
				Labels: []string{"tool", "error_type"},
			},
			{
				Name:   "agent.tool_call.success",
				Type:   "counter",
				Help:   "Successful tool calls",
				Labels: []string{"tool"},
			},
			{
				Name: "agent.tools.discovered",
				Type: "gauge",
				Help: "Number of tools discovered via service discovery",
			},
			{
				Name:    "agent.ai_synthesis.duration_ms",
				Type:    "histogram",
				Help:    "AI synthesis operation duration",
				Unit:    "milliseconds",
				Buckets: []float64{100, 500, 1000, 2000, 5000},
			},
			{
				Name:   "agent.ai.requests",
				Type:   "counter",
				Help:   "AI API requests made",
				Labels: []string{"provider", "operation"},
			},
			// Analysis Capabilities Metrics
			{
				Name:   "agent.analysis.requests",
				Type:   "counter",
				Help:   "Analysis capability requests",
				Labels: []string{"type", "status", "module"},
			},
			{
				Name:    "agent.analysis.duration_ms",
				Type:    "histogram",
				Help:    "Analysis operation duration in milliseconds",
				Labels:  []string{"type"},
				Unit:    "milliseconds",
				Buckets: []float64{500, 1000, 2000, 5000, 10000, 30000},
			},
			{
				Name:   "agent.analysis.ai_errors",
				Type:   "counter",
				Help:   "AI call failures during analysis",
				Labels: []string{"type", "module"},
			},
		},
	})

	// Create traced HTTP client for distributed tracing context propagation
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 300 * time.Second

	researchAgent := &ResearchAgent{
		BaseAgent:          agent,
		aiClient:           aiClient,
		httpClient:         tracedClient,
		instrumentedClient: instrumentedClient, // nil if not enabled
		debugRecorder:      debugRecorder,      // nil if not enabled; Close() on shutdown
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
	// Internal: true — prevents recursive orchestration when this agent is part of an
	// AI-orchestrated plan. The orchestrator should select individual tools directly.
	r.RegisterCapability(core.Capability{
		Name:        "research_topic",
		Internal:    true,
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

		// Phase 2b: Output schema — fields match ResearchResponse JSON tags.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "topic", Type: "string", Description: "Echo of the research topic that was investigated"},
				{Name: "summary", Type: "string", Description: "Text summary of findings across all tool calls"},
				{Name: "tools_used", Type: "array", Description: "Names of tools that were called during research"},
				{Name: "results", Type: "array", Description: "Per-tool detailed results with tool_name, capability, data, success, error, duration"},
				{Name: "confidence", Type: "number", Description: "Confidence score between 0.0 and 1.0"},
				{Name: "processing_time", Type: "string", Description: "Total request duration as a Go duration string"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "ai_analysis", Type: "string", Description: "AI-generated insights (present when ai_synthesis is enabled)"},
				{Name: "workflow_id", Type: "string", Description: "Echo of workflow_id from request, if provided"},
				{Name: "metadata", Type: "object", Description: "Additional metadata: tools_discovered, tools_used count, ai_enabled"},
			},
		},
	})

	// Capability 2: Component discovery and status
	// Internal: true — meta-capability for direct API use only.
	// The orchestrator handles discovery via Redis; including this in plans is redundant.
	r.RegisterCapability(core.Capability{
		Name:        "discover_tools",
		Internal:    true,
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

		// Phase 2b: Output schema — shape written by handleDiscoverTools.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "discovery_summary", Type: "object", Description: "Summary with total_components, tools count, agents count, and discovery_time (RFC3339)"},
				{Name: "tools", Type: "array", Description: "List of discovered tool ServiceInfo entries with id, name, type, address, port, capabilities"},
				{Name: "agents", Type: "array", Description: "List of discovered peer agent ServiceInfo entries"},
			},
		},
	})

	// Capability 3: AI-powered analysis (if AI is available)
	r.RegisterCapability(core.Capability{
		Name:        "analyze_data",
		Description: "General-purpose AI data analysis returning key findings, patterns, and recommendations. Accepts structured data from any upstream tool.",
		InputTypes:  []string{"json", "text"},
		OutputTypes: []string{"json"},
		Handler:     r.handleAnalyzeData,
		// Phase 2: Field hints for analysis requests
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "data",
					Type:        "object",
					Example:     `{"results": [{"headline": "...", "summary": "..."}], "source": "news-tool"}`,
					Description: "Structured data to analyze: results from any upstream tool (news articles, financial metrics, weather data). Pass the ENTIRE data object from the source — the AI model will identify relevant patterns and insights.",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "symbol",
					Type:        "string",
					Example:     "AAPL",
					Description: "Stock ticker symbol for contextualizing the analysis",
				},
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
				{Name: "analysis", Type: "string", Description: "AI-generated analysis text"},
				{Name: "model", Type: "string", Description: "AI model used for analysis"},
				{Name: "tokens_used", Type: "number", Description: "Total tokens consumed by the AI call"},
				{Name: "timestamp", Type: "string", Description: "ISO 8601 timestamp of the analysis"},
			},
		},
	})

	// Capability 4: Workflow orchestration
	// Internal: true — prevents recursive orchestration. The AI orchestrator should
	// plan individual steps, not delegate to this meta-orchestration endpoint.
	r.RegisterCapability(core.Capability{
		Name:        "orchestrate_workflow",
		Internal:    true,
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

		// Phase 2b: Output schema — shape written by handleOrchestateWorkflow.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "workflow_id", Type: "string", Description: "Generated workflow identifier (workflow-{unix})"},
				{Name: "workflow_type", Type: "string", Description: "Echo of the workflow_type from the request"},
				{Name: "result", Type: "object", Description: "Workflow-specific result payload; shape varies by workflow_type"},
				{Name: "status", Type: "string", Description: "Final workflow status (always 'completed' on 200)"},
				{Name: "completed_at", Type: "string", Description: "RFC3339 timestamp when the workflow finished"},
			},
		},
	})

	// Capability 5: Health check
	// Internal: true — infrastructure endpoint, not a plannable capability.
	r.RegisterCapability(core.Capability{
		Name:        "health",
		Internal:    true,
		Description: "Health check endpoint with dependency status",
		Endpoint:    "/health",
		InputTypes:  []string{},
		OutputTypes: []string{"json"},
		Handler:     r.handleHealth,
	})

	// =========================================================================
	// AI Analysis Capabilities (pass-through to powerful AI models)
	// =========================================================================

	// Capability 6: Financial Analysis
	r.RegisterCapability(core.Capability{
		Name:        "financial_analysis",
		Description: "AI-powered financial analysis returning structured findings with confidence scores. Accepts large financial datasets from basic_financials or similar sources. See analysis_type for focus options.",
		InputTypes:  []string{"json", "text"},
		OutputTypes: []string{"json"},
		Handler:     r.handleFinancialAnalysis,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "financial_metrics", Type: "object", Example: `{"metric": {"peAnnual": 94.18, "grossMarginTTM": 49.52, "roeTTM": 88.65}, "symbol": "NVDA", "series": {"annual": {}}}`, Description: "Financial metrics object from basic_financials or similar data source. Pass the ENTIRE metrics object including all ratios, series data, and metadata — the AI model will identify the relevant metrics for the analysis type. Do not pre-filter."},
				{Name: "analysis_type", Type: "string", Example: "valuation", Description: "Analysis focus: valuation (PE, PB, PS ratios), profitability (margins, ROE, ROA), growth (revenue/EPS trends), risk (beta, volatility), earnings (EPS surprise patterns), trend (multi-period time series), thesis (comprehensive investment case)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "symbol", Type: "string", Example: "NVDA", Description: "Stock ticker symbol for the company being analyzed. Used for contextualizing findings."},
				{Name: "context", Type: "string", Example: "AI chip market positioning", Description: "Additional context for the analysis"},
				{Name: "timeframe", Type: "string", Example: "Q3 2024", Description: "Relevant timeframe (e.g., Q3 2024, YoY, 5-year)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "summary", Type: "string", Description: "Executive summary of the financial analysis"},
				{Name: "key_findings", Type: "array", Description: "Key findings with title, description, impact (positive/negative/neutral), and confidence score"},
				{Name: "metrics", Type: "array", Description: "Analyzed metrics with name, value, trend (improving/declining/stable), and assessment"},
				{Name: "risk_factors", Type: "array", Description: "Risk factors with severity (high/medium/low) and mitigation strategies"},
				{Name: "recommendation", Type: "string", Description: "Overall investment or action recommendation"},
				{Name: "confidence", Type: "number", Description: "Overall confidence score (0.0-1.0)"},
				{Name: "model", Type: "string", Description: "AI model used for analysis"},
				{Name: "tokens_used", Type: "number", Description: "Total tokens consumed by the AI call"},
				{Name: "timestamp", Type: "string", Description: "ISO 8601 timestamp of the analysis"},
				{Name: "request_id", Type: "string", Description: "Unique request identifier for tracking"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "supporting_evidence", Type: "array", Description: "Data points supporting the conclusions"},
				{Name: "caveats", Type: "array", Description: "Limitations and disclaimers about the analysis"},
			},
		},
	})

	// Capability 7: Sentiment Analysis
	r.RegisterCapability(core.Capability{
		Name:        "sentiment_analysis",
		Description: "AI-powered sentiment analysis returning scores, emotional tone, key themes, and supporting quotes. Accepts bulk content arrays from company_news/market_news or similar sources.",
		InputTypes:  []string{"json", "text"},
		OutputTypes: []string{"json"},
		Handler:     r.handleSentimentAnalysis,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "news_data", Type: "array", Example: `[{"headline":"Strong Q3 results","summary":"The company exceeded analyst expectations...","source":"Reuters"}]`, Description: "Array of news articles or text items to analyze for sentiment. Pass the ENTIRE array from the upstream data source — do not filter or select individual items. Each item should have headline, summary, and source fields."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "symbol", Type: "string", Example: "AAPL", Description: "Stock ticker symbol for contextualizing the sentiment analysis"},
				{Name: "content_type", Type: "string", Example: "news", Description: "Content type: news (from company_news/market_news), earnings_call, social_media, review, general. Helps calibrate sentiment scoring thresholds."},
				{Name: "aspects", Type: "array", Example: `["market_sentiment", "investment_outlook"]`, Description: "Specific aspects to analyze: market_sentiment, investment_outlook, risk_assessment, competitive_position. If omitted, analyzes all detected themes."},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "overall_sentiment", Type: "string", Description: "Overall sentiment: positive, negative, neutral, or mixed"},
				{Name: "sentiment_score", Type: "number", Description: "Sentiment score (-1.0 to 1.0)"},
				{Name: "confidence", Type: "number", Description: "Confidence score (0.0-1.0)"},
				{Name: "emotional_tone", Type: "array", Description: "Detected emotional tones (e.g., optimistic, confident, cautious)"},
				{Name: "key_themes", Type: "array", Description: "Key themes with theme name, sentiment, and importance (high/medium/low)"},
				{Name: "summary", Type: "string", Description: "Overall sentiment summary"},
				{Name: "model", Type: "string", Description: "AI model used for analysis"},
				{Name: "tokens_used", Type: "number", Description: "Total tokens consumed by the AI call"},
				{Name: "timestamp", Type: "string", Description: "ISO 8601 timestamp of the analysis"},
				{Name: "request_id", Type: "string", Description: "Unique request identifier for tracking"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "supporting_quotes", Type: "array", Description: "Supporting quotes with text and associated sentiment"},
			},
		},
	})

	// Capability 8: Comparative Analysis
	r.RegisterCapability(core.Capability{
		Name:        "comparative_analysis",
		Description: "Multi-factor comparative analysis using AI. Returns comparison matrix, trade-offs, and recommendations. Best for: decision-making requiring systematic comparison of alternatives.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     r.handleComparativeAnalysis,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "entities", Type: "array", Example: `[{"name": "NVIDIA", "data": {"metric": {"peAnnual": 94.18}}}, {"name": "AMD", "data": {"metric": {"peAnnual": 120.5}}}]`, Description: "Array of entities to compare. Each entity should be an object with a 'name' field and associated data (financial metrics, profiles, or any structured attributes). When comparing stocks, include the full financial_metrics from each company's basic_financials response."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "comparison_criteria", Type: "array", Example: `["revenue", "market_share", "growth_rate"]`, Description: "Specific criteria to compare on"},
				{Name: "context", Type: "string", Example: "AI chip market investment decision", Description: "Context for the comparison"},
				{Name: "priorities", Type: "object", Example: `{"market_share": 0.4, "growth_rate": 0.6}`, Description: "Weighting of criteria importance (0-1)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "summary", Type: "string", Description: "High-level comparison summary"},
				{Name: "comparison_matrix", Type: "array", Description: "Per-criterion scores for each entity with winner designation"},
				{Name: "rankings", Type: "array", Description: "Ranked entities with total score, strengths, and weaknesses"},
				{Name: "trade_offs", Type: "array", Description: "Key trade-offs between the compared entities"},
				{Name: "recommendation", Type: "object", Description: "Best overall pick, best-for-use-case mapping, and reasoning"},
				{Name: "confidence", Type: "number", Description: "Overall confidence score (0.0-1.0)"},
				{Name: "model", Type: "string", Description: "AI model used for analysis"},
				{Name: "tokens_used", Type: "number", Description: "Total tokens consumed by the AI call"},
				{Name: "timestamp", Type: "string", Description: "ISO 8601 timestamp of the analysis"},
				{Name: "request_id", Type: "string", Description: "Unique request identifier for tracking"},
			},
		},
	})

	// Capability 9: Mathematical Analysis
	r.RegisterCapability(core.Capability{
		Name:        "math_analysis",
		Description: "AI-powered mathematical analysis with step-by-step solutions and verification. See problem_type for supported categories (calculus, statistics, optimization, etc.).",
		InputTypes:  []string{"json", "text"},
		OutputTypes: []string{"json"},
		Handler:     r.handleMathAnalysis,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "problem", Type: "string", Example: "Find the derivative of f(x) = x^3 * ln(x)", Description: "Mathematical problem to solve (equation, word problem, or data)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "problem_type", Type: "string", Example: "calculus", Description: "Type: algebraic, calculus, statistical, optimization, probability, numerical, geometry"},
				{Name: "context", Type: "string", Example: "Physics application for velocity", Description: "Additional context or constraints for the problem"},
				{Name: "show_steps", Type: "boolean", Example: "true", Description: "Whether to show detailed step-by-step solution (default: true)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "problem_type", Type: "string", Description: "Classified problem type (statistical, algebraic, calculus, optimization, etc.)"},
				{Name: "summary", Type: "string", Description: "Brief summary of the problem and solution"},
				{Name: "solution", Type: "object", Description: "Solution with answer, numeric_value, and optional unit"},
				{Name: "steps", Type: "array", Description: "Step-by-step solution with step number, description, calculation, and explanation"},
				{Name: "confidence", Type: "number", Description: "Confidence score (0.0-1.0)"},
				{Name: "model", Type: "string", Description: "AI model used for analysis"},
				{Name: "tokens_used", Type: "number", Description: "Total tokens consumed by the AI call"},
				{Name: "timestamp", Type: "string", Description: "ISO 8601 timestamp of the analysis"},
				{Name: "request_id", Type: "string", Description: "Unique request identifier for tracking"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "verification", Type: "object", Description: "Verification method and result"},
				{Name: "assumptions", Type: "array", Description: "Assumptions made during the solution"},
				{Name: "edge_cases", Type: "array", Description: "Potential edge cases or limitations"},
				{Name: "related_concepts", Type: "array", Description: "Relevant mathematical concepts used"},
				{Name: "caveats", Type: "array", Description: "Limitations or notes about the solution"},
			},
		},
	})
}
