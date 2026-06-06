package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/memory"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// TravelChatAgent is a streaming chat agent that uses orchestration
// to coordinate travel-related tools and provide real-time responses via SSE.
type TravelChatAgent struct {
	*core.BaseAgent
	orchestrator   *orchestration.AIOrchestrator
	sessionStore   *SessionStore
	httpClient     *http.Client              // Traced HTTP client
	userMemBackend *memory.UserMemoryBackend // User memory lifecycle (nil if not configured)
	userMemCloser  io.Closer                 // Drains in-flight async extractions at shutdown
	mu             sync.RWMutex
}

// NewTravelChatAgent creates a new travel chat agent with AI and telemetry configured.
func NewTravelChatAgent() (*TravelChatAgent, error) {
	agent := core.NewBaseAgent("travel-chat-agent")

	// Create AI client with provider chain for failover.
	// Auto-detect mode: discovers available providers from API keys and orders by priority.
	// Timeout: 240s for reasoning models (GPT-5, o1, o3, o4) that need longer for chain-of-thought.
	//
	// To explicitly control the chain order instead of auto-detecting, use WithProviderChain:
	//   ai.NewChainClient(
	//       ai.WithProviderChain("openai", "anthropic"),  // explicit order, no auto-detection
	//       ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
	//       ai.WithChainLogger(agent.Logger),
	//       ai.WithChainTimeout(240*time.Second),
	//   )
	chainClient, err := ai.NewChainClient(
		ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
		ai.WithChainLogger(agent.Logger),
		ai.WithChainTimeout(240*time.Second), // Extended timeout for reasoning models
	)
	if err != nil {
		agent.Logger.Warn("Failed to create AI chain client, trying single provider", map[string]interface{}{
			"error": err.Error(),
		})
		// Fallback to single provider - returns core.AIClient interface
		singleClient, err := ai.NewClient(
			ai.WithTimeout(240*time.Second),                    // Extended timeout for reasoning models
			ai.WithTelemetry(telemetry.GetTelemetryProvider()), // AI span visibility in Jaeger
		)
		if err != nil {
			// AI is optional - some orchestration features still work without it
			agent.Logger.Warn("AI client creation failed, some features will be limited", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			agent.AI = singleClient
		}
	} else {
		agent.AI = chainClient
	}

	// Declare metrics this agent will emit for observability
	telemetry.DeclareMetrics("travel-chat-agent", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:    "chat.request.duration_ms",
				Type:    "histogram",
				Help:    "Chat request duration in milliseconds",
				Labels:  []string{"session_id", "status"},
				Unit:    "milliseconds",
				Buckets: []float64{100, 500, 1000, 2000, 5000, 10000, 30000},
			},
			{
				Name:   "chat.requests",
				Type:   "counter",
				Help:   "Number of chat requests",
				Labels: []string{"status"},
			},
			{
				Name: "chat.sessions.active",
				Type: "gauge",
				Help: "Number of active chat sessions",
			},
			{
				Name:   "chat.orchestration.tool_calls",
				Type:   "counter",
				Help:   "Number of tool calls made during chat orchestration",
				Labels: []string{"tool_name"},
			},
		},
	})

	// Create traced HTTP client with production settings
	// Increased timeout for complex multi-tool orchestration
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 300 * time.Second // Increased for complex orchestration

	// Create Redis-backed session store
	// Uses Redis DB 2 (RedisDBSessions) to isolate from service registry (DB 0)
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return nil, fmt.Errorf("REDIS_URL is required for session storage")
	}
	sessionStore, err := NewSessionStore(redisURL, 48*time.Hour, 50, agent.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create session store: %w", err)
	}

	chatAgent := &TravelChatAgent{
		BaseAgent:    agent,
		sessionStore: sessionStore,
		httpClient:   tracedClient,
	}

	// Register capabilities
	chatAgent.registerCapabilities()

	return chatAgent, nil
}

// InitializeOrchestrator sets up the orchestrator after Discovery is available.
func (t *TravelChatAgent) InitializeOrchestrator(discovery core.Discovery) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if discovery == nil {
		return fmt.Errorf("discovery service not available")
	}

	// Create orchestrator config
	config := orchestration.DefaultConfig()
	config.RoutingMode = orchestration.ModeAutonomous
	config.SynthesisStrategy = orchestration.StrategyLLM
	config.MetricsEnabled = true
	config.EnableTelemetry = true

	// LLM token limits for plan generation and synthesis are loaded by DefaultConfig()
	// into PlanAIOptions / SynthesisAIOptions:
	//   TRUVAG3_PLAN_MAX_TOKENS (default: 15000)
	//   TRUVAG3_SYNTHESIS_MAX_TOKENS (default: 5000)
	if config.SynthesisAIOptions == nil {
		config.SynthesisAIOptions = &orchestration.AIOptionsOverride{}
	}
	config.SynthesisAIOptions.Temperature = orchestration.Float32Ptr(0.7)

	// Increase timeouts for complex multi-tool orchestration scenarios
	config.ExecutionOptions.TotalTimeout = 5 * time.Minute  // Overall orchestration timeout
	config.ExecutionOptions.StepTimeout = 120 * time.Second // Per-step timeout for AI planning

	// Configure prompt builder for travel domain
	config.PromptConfig = orchestration.PromptConfig{
		// SystemInstructions defines the chat agent's persona and behavioral context.
		// This becomes the primary identity, with the orchestrator role as secondary.
		// Similar to LangChain's system_prompt, AutoGen's system_message, or OpenAI's instructions.
		SystemInstructions: `You are a friendly travel chat assistant.
You help users plan trips by coordinating information from various travel services.
Be conversational and helpful while providing accurate, real-time information.

For Kubernetes, cluster, or DevOps questions that fall outside the travel domain,
delegate the user's request to the devops_operations capability exposed by the
devops-chat-agent and return its synthesized response.`,

		Domain: "travel",
		AdditionalTypeRules: []orchestration.TypeRule{
			{
				TypeNames:   []string{"currency_code", "from_currency", "to_currency", "from", "to"},
				JsonType:    "JSON strings",
				Example:     `"USD"`,
				Description: "ISO 4217 currency codes (USD, EUR, JPY, etc.)",
			},
			{
				TypeNames:   []string{"location", "destination", "city"},
				JsonType:    "JSON strings",
				Example:     `"Tokyo, Japan"`,
				Description: "Location or destination names",
			},
			{
				TypeNames:   []string{"airport_code", "origin", "destination", "departure_airport", "city_code"},
				JsonType:    "JSON strings",
				Example:     `"JFK"`,
				Description: "3-letter IATA airport or city codes (JFK, LAX, NRT, LHR, PAR)",
			},
			{
				TypeNames:   []string{"departure_date", "return_date", "check_in", "check_out"},
				JsonType:    "JSON strings",
				Example:     `"2026-04-15"`,
				Description: "Dates in YYYY-MM-DD format",
			},
		},
		CustomInstructions: []string{
			"Before processing any planning query, first find out today's date so you can resolve relative dates like 'next week' or 'this weekend' accurately",
			"If the user's query lacks critical information needed for accurate results (e.g., departure city, travel dates, number of travelers, budget range), ask clarifying questions in your response instead of making assumptions",
			"Do not assume the user's home currency is USD — ask or infer from their departure location",
			"Consider seasonal factors (weather, peak/off-peak pricing, local holidays) when recommending travel dates or destinations",
			"For weather queries, always geocode the location first to get coordinates",
			"For currency conversion, extract the destination country's currency code",
			"Prefer parallel execution when steps are independent",
			"For flight searches, use search_airports first to resolve city names to IATA codes if the user provides city names instead of airport codes",
			"For hotel searches, use the city IATA code — use search_airports to find it if needed",
			"When planning a trip to a country, check get_travel_advisory for safety information",
			"For local dining and activities, use search_places or nearby_places with the destination coordinates",
			"For any Kubernetes, cluster, pod, namespace, deployment, log, or DevOps-related query, delegate to the devops_operations capability (devops-chat-agent) by passing the user's natural-language question as the `query` field",
		},
	}

	// ── User memory setup ──────────────────────────────────────────────────
	embedder, embErr := ai.NewEmbeddingClient(ai.WithEmbeddingLogger(t.Logger))
	if embErr != nil {
		t.Logger.Warn("Embedding client creation failed, user memory will use in-memory fallback", map[string]interface{}{
			"operation": "initialize_orchestrator",
			"error":     embErr.Error(),
		})
	}

	userMemBackend, _ := memory.NewUserMemoryBackend(t.Logger,
		memory.WithUserMemoryNamespace("travel"),
		memory.WithUserMemoryEmbeddingClient(embedder), // nil-safe: factory falls back to in-memory
	)
	t.userMemBackend = userMemBackend

	// Activate user_id canonicalization in the memory hooks as defense-in-depth.
	// The app already normalizes at the edge (getUserID), so metadata["user_id"]
	// arrives canonical; this keeps recall and storage consistent even for any
	// future caller that reaches the hooks with a non-normalized id.
	userHooks, userHooksCloser := orchestration.BuildUserMemoryHooks(userMemBackend.ToDeps(), t.AI, t.Logger,
		orchestration.WithUserIDNormalizer(orchestration.NormalizeUserIDLowercaseTrim),
	)
	t.userMemCloser = userHooksCloser
	// ── End user memory setup ───────────────────────────────────────────

	var conversationHistoryPreparer orchestration.ConversationHistoryPreparer
	if t.AI != nil {
		preparer, err := orchestration.BuildCompactionEnabledConversationHistoryPreparer(config, t.AI)
		if err != nil {
			return fmt.Errorf("failed to build compaction-enabled conversation history preparer: %w", err)
		}
		conversationHistoryPreparer = preparer
	} else {
		t.Logger.Warn("Conversation compaction disabled because AI client is unavailable", map[string]interface{}{
			"operation": "initialize_orchestrator",
		})
	}

	// Create dependencies using factory pattern with dependency injection
	deps := orchestration.OrchestratorDependencies{
		Discovery:                   discovery,
		AIClient:                    t.AI,
		Logger:                      t.Logger,
		Telemetry:                   telemetry.GetTelemetryProvider(),
		EnableErrorAnalyzer:         true,
		PipelineHooks:               userHooks,
		ConversationHistoryPreparer: conversationHistoryPreparer,
	}

	// Create orchestrator
	orch, err := orchestration.CreateOrchestrator(config, deps)
	if err != nil {
		return fmt.Errorf("failed to create orchestrator: %w", err)
	}

	// Start the orchestrator
	ctx := context.Background()
	if err := orch.Start(ctx); err != nil {
		return fmt.Errorf("failed to start orchestrator: %w", err)
	}

	t.orchestrator = orch

	t.Logger.Info("Orchestrator initialized successfully", map[string]interface{}{
		"routing_mode":       config.RoutingMode,
		"synthesis_strategy": config.SynthesisStrategy,
	})

	return nil
}

// formatConversationHistory formats conversation history for the <conversation_history> tag.
// The orchestrator wraps this in XML and keeps it separate from the user request
// per EFFECTIVE_PROMPTS_GUIDE §2.8 and §2.10.
func (t *TravelChatAgent) formatConversationHistory(history []Message) string {
	if len(history) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, msg := range history {
		role := "User"
		if msg.Role == "assistant" {
			role = "Assistant"
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
	}
	return strings.TrimSpace(sb.String())
}

func travelConversationTurnsFromMessages(history []Message) []core.ConversationTurn {
	if len(history) == 0 {
		return nil
	}
	turns := make([]core.ConversationTurn, 0, len(history))
	for _, msg := range history {
		turns = append(turns, core.ConversationTurn{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	return turns
}

func (t *TravelChatAgent) addConversationHistoryMetadata(metadata map[string]interface{}, sessionID string, history []Message) {
	if sessionID == "" || len(history) == 0 {
		return
	}
	metadata[orchestration.MetadataConversationTurns] = travelConversationTurnsFromMessages(history)
	metadata[orchestration.MetadataConversationSessionKey] = sessionID
	if formattedHistory := t.formatConversationHistory(history); formattedHistory != "" {
		metadata[core.EnrichmentConversationHistory] = formattedHistory
	}
}

// ProcessWithStreaming processes a user query and streams progress via callback.
// It uses true streaming when the orchestrator supports it, falling back to
// simulated streaming (chunking the complete response) otherwise.
func (t *TravelChatAgent) ProcessWithStreaming(ctx context.Context, sessionID, query string, callback StreamCallback) error {
	startTime := time.Now()

	t.mu.RLock()
	orch := t.orchestrator
	t.mu.RUnlock()

	if orch == nil {
		return fmt.Errorf("orchestrator not initialized")
	}

	// Retrieve conversation history for context.
	// History is passed via metadata so the orchestrator injects it as a
	// <conversation_history> tag, keeping it separate from <user_request>.
	history := t.sessionStore.GetHistory(sessionID)
	metadata := map[string]interface{}{}

	// Pass user_id from session into metadata for user memory hooks
	if session := t.sessionStore.Get(sessionID); session != nil && session.UserID != "" {
		metadata["user_id"] = session.UserID
	}

	t.addConversationHistoryMetadata(metadata, sessionID, history)

	// Log start with trace context
	t.Logger.InfoWithContext(ctx, "Processing chat request", map[string]interface{}{
		"operation":     "process_chat",
		"session_id":    sessionID,
		"query_len":     len(query),
		"history_turns": len(history),
	})

	// Send planning status
	callback.SendStatus("planning", "Analyzing your request...")

	// Add span event for planning start
	telemetry.AddSpanEvent(ctx, "orchestration.started",
		attribute.String("session_id", sessionID),
		attribute.Int("query_length", len(query)),
		attribute.Int("history_turns", len(history)),
	)

	// Use true streaming - tokens are delivered as they're generated by the AI provider
	// The orchestrator handles fallback to simulated streaming internally if needed
	t.Logger.DebugWithContext(ctx, "Using streaming orchestration", map[string]interface{}{
		"operation":  "process_chat",
		"session_id": sessionID,
	})

	// Add per-request step callback to context for real-time tool progress.
	// This sends SSE events as each tool completes during execution.
	// Use a monotonic counter for step IDs to avoid collisions in multi-phase plans
	// where both stepIndex and step.StepID (e.g. "step-1") reset/reuse across phases.
	globalStepCounter := 0
	ctx = orchestration.WithStepCallback(ctx, func(stepIndex, totalSteps int, step orchestration.RoutingStep, stepResult orchestration.StepResult) {
		globalStepCounter++
		callback.SendStep(
			fmt.Sprintf("step_%d", globalStepCounter),
			step.AgentName,
			stepResult.Success,
			stepResult.Duration.Milliseconds(),
		)
	})

	// Pass raw query + metadata (history auto-promoted to enrichments by orchestrator)
	result, err := orch.ProcessRequestStreaming(ctx, query, metadata, func(chunk core.StreamChunk) error {
		// Phase-complete chunks are transient progress indicators, not response content.
		// Send them as status events so the UI shows them temporarily during processing
		// rather than accumulating them into the final response text.
		if chunk.Metadata != nil && chunk.Metadata["type"] == "phase_complete" {
			phaseNum, _ := chunk.Metadata["phase"].(int)
			callback.SendStatus("phase_complete", fmt.Sprintf("Phase %d complete. Planning next phase...", phaseNum))
			return nil
		}
		// Forward content chunks to SSE callback
		if chunk.Content != "" {
			callback.SendChunk(chunk.Content)
		}
		return nil
	})
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Streaming orchestration failed", map[string]interface{}{
			"operation":   "process_chat",
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		return fmt.Errorf("streaming orchestration failed: %w", err)
	}

	response := result.Response
	requestID := result.RequestID
	agentsInvolved := result.AgentsInvolved
	confidence := result.Confidence
	executionTime := result.ExecutionTime

	// Log streaming stats
	t.Logger.DebugWithContext(ctx, "Streaming completed", map[string]interface{}{
		"operation":        "process_chat",
		"chunks_delivered": result.ChunksDelivered,
		"stream_completed": result.StreamCompleted,
		"partial_content":  result.PartialContent,
		"finish_reason":    result.FinishReason,
	})

	// Note: Step events are now sent in real-time via the context callback
	// (WithStepCallback above) so we don't need to send them again here.

	// Send usage stats if available
	if result.Usage != nil {
		callback.SendUsage(
			result.Usage.PromptTokens,
			result.Usage.CompletionTokens,
			result.Usage.TotalTokens,
			result.UsageByPhase,
		)
	}

	// Send finish reason if available
	if result.FinishReason != "" {
		callback.SendFinish(result.FinishReason)
	}

	// Add span event for completion
	telemetry.AddSpanEvent(ctx, "orchestration.completed",
		attribute.String("request_id", requestID),
		attribute.Int("agents_used", len(agentsInvolved)),
		attribute.Float64("confidence", confidence),
		attribute.String("execution_time", executionTime.String()),
	)

	// Send completion
	callback.SendDone(requestID, agentsInvolved, time.Since(startTime).Milliseconds())

	// Store assistant message in session
	t.sessionStore.AddMessage(sessionID, Message{
		Role:      "assistant",
		Content:   response,
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"request_id":  requestID,
			"tools_used":  agentsInvolved,
			"confidence":  confidence,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"prompt_tokens": func() int {
				if result.Usage != nil {
					return result.Usage.PromptTokens
				}
				return 0
			}(),
			"completion_tokens": func() int {
				if result.Usage != nil {
					return result.Usage.CompletionTokens
				}
				return 0
			}(),
			"total_tokens": func() int {
				if result.Usage != nil {
					return result.Usage.TotalTokens
				}
				return 0
			}(),
			"usage_by_phase": result.UsageByPhase,
		},
	})

	t.Logger.InfoWithContext(ctx, "Chat request completed", map[string]interface{}{
		"operation":   "process_chat",
		"session_id":  sessionID,
		"request_id":  requestID,
		"tools_used":  len(agentsInvolved),
		"duration_ms": time.Since(startTime).Milliseconds(),
		"status":      "success",
	})

	return nil
}

// ProcessQuery handles a non-streaming query for agent-to-agent delegation.
// Uses the synchronous ProcessRequest path (no SSE). When sessionID is provided,
// conversation history is included for contextual follow-ups.
func (t *TravelChatAgent) ProcessQuery(ctx context.Context, sessionID, query string) (*orchestration.OrchestratorResponse, error) {
	startTime := time.Now()

	t.mu.RLock()
	orch := t.orchestrator
	t.mu.RUnlock()

	if orch == nil {
		return nil, fmt.Errorf("orchestrator not initialized")
	}

	// Build metadata — include session history only when session_id was provided.
	// History is passed via metadata so the orchestrator injects it as a
	// <conversation_history> tag, keeping it separate from <user_request>.
	metadata := map[string]interface{}{}
	if sessionID != "" {
		// Pass user_id from session into metadata for user memory hooks
		if session := t.sessionStore.Get(sessionID); session != nil && session.UserID != "" {
			metadata["user_id"] = session.UserID
		}
		history := t.sessionStore.GetHistory(sessionID)
		t.addConversationHistoryMetadata(metadata, sessionID, history)
		t.Logger.InfoWithContext(ctx, "Processing query with session context", map[string]interface{}{
			"operation":     "process_query",
			"session_id":    sessionID,
			"history_turns": len(history),
			"query_len":     len(query),
		})
	} else {
		t.Logger.InfoWithContext(ctx, "Processing stateless delegation query", map[string]interface{}{
			"operation": "process_query",
			"query_len": len(query),
		})
	}

	telemetry.AddSpanEvent(ctx, "agent_delegation.started",
		attribute.String("agent", "travel-chat-agent"),
		attribute.Int("query_length", len(query)),
	)

	// Non-streaming orchestration — blocks until plan + execute + synthesize complete
	result, err := orch.ProcessRequest(ctx, query, metadata)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Query orchestration failed", map[string]interface{}{
			"operation":   "process_query",
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		return nil, fmt.Errorf("orchestration failed: %w", err)
	}

	// Store response in session for continuity (only when session was provided)
	if sessionID != "" {
		t.sessionStore.AddMessage(sessionID, Message{
			Role:      "assistant",
			Content:   result.Response,
			Timestamp: time.Now(),
			Metadata: map[string]interface{}{
				"request_id":  result.RequestID,
				"tools_used":  result.AgentsInvolved,
				"confidence":  result.Confidence,
				"duration_ms": time.Since(startTime).Milliseconds(),
				"source":      "agent_delegation",
			},
		})
	}

	telemetry.AddSpanEvent(ctx, "agent_delegation.completed",
		attribute.String("request_id", result.RequestID),
		attribute.Int("tools_used", len(result.AgentsInvolved)),
		attribute.Float64("confidence", result.Confidence),
	)

	t.Logger.InfoWithContext(ctx, "Query completed", map[string]interface{}{
		"operation":   "process_query",
		"request_id":  result.RequestID,
		"tools_used":  len(result.AgentsInvolved),
		"confidence":  result.Confidence,
		"duration_ms": time.Since(startTime).Milliseconds(),
	})

	return result, nil
}

// registerCapabilities registers the agent's HTTP endpoints.
func (t *TravelChatAgent) registerCapabilities() {
	// Agent-as-Tool: Non-streaming query endpoint for agent-to-agent delegation.
	// Internal: true — hidden from this agent's own LLM planner to prevent self-calling.
	// Other agents can still call this via direct HTTP POST to /query.
	t.RegisterCapability(core.Capability{
		Name:     "travel_query",
		Internal: true,
		Description: "Specialized travel planning agent with its own orchestrator that handles trip planning and travel research. " +
			"Delegate any travel-related task to this agent — it automatically discovers all available agentic tools available in the platform, " +
			"plans multi-step workflows, and executes them to fulfill the request. " +
			"Send a natural language query and receive a fully synthesized answer.",
		Endpoint:    "/query",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleQuery,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "Find flights from NYC to Tokyo next month, check the weather there, and convert 2000 USD to JPY",
					Description: "Natural language travel question or request to delegate",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "session_id",
					Type:        "string",
					Example:     "sess_abc123",
					Description: "Session ID for conversational context (omit for stateless one-shot queries)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "request_id", Type: "string", Description: "Unique identifier for this orchestration request"},
				{Name: "request", Type: "string", Description: "The original query that was submitted"},
				{Name: "response", Type: "string", Description: "AI-synthesized travel answer"},
				{Name: "tools_used", Type: "array", Description: "List of tool names invoked during orchestration"},
				{Name: "execution_time", Type: "string", Description: "Total request duration as a human-readable string"},
				{Name: "confidence", Type: "number", Description: "Response confidence score between 0.0 and 1.0"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "metadata", Type: "object", Description: "Additional orchestration metadata"},
			},
		},
	})

	// SSE streaming endpoint
	t.RegisterCapability(core.Capability{
		Name:        "chat_stream",
		Description: "SSE streaming chat endpoint for travel queries",
		Endpoint:    "/chat/stream",
		Handler:     NewSSEHandler(t).ServeHTTP,
		Internal:    true, // Don't include in orchestrator's tool catalog
	})

	// Session management endpoints
	t.RegisterCapability(core.Capability{
		Name:        "create_session",
		Description: "Create a new chat session",
		Endpoint:    "/chat/session",
		Handler:     t.handleCreateSession,
		Internal:    true,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "session_id", Type: "string", Description: "Unique identifier for the created session"},
				{Name: "created_at", Type: "string", Description: "Session creation timestamp"},
				{Name: "expires_at", Type: "string", Description: "Session expiration timestamp"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "get_session",
		Description: "Get session information",
		Endpoint:    "/chat/session/{id}",
		Handler:     t.handleGetSession,
		Internal:    true,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "session_id", Type: "string", Description: "Session identifier"},
				{Name: "created_at", Type: "string", Description: "Session creation timestamp"},
				{Name: "updated_at", Type: "string", Description: "Last update timestamp"},
				{Name: "message_count", Type: "number", Description: "Number of messages in the session"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "metadata", Type: "object", Description: "Session metadata"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "get_history",
		Description: "Get conversation history for a session",
		Endpoint:    "/chat/session/{id}/history",
		Handler:     t.handleGetHistory,
		Internal:    true,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "session_id", Type: "string", Description: "Session identifier"},
				{Name: "messages", Type: "array", Description: "Array of conversation messages with role, content, and timestamp"},
				{Name: "count", Type: "number", Description: "Number of messages returned"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "list_sessions",
		Description: "List user's chat sessions",
		Endpoint:    "/chat/sessions",
		Handler:     t.handleListSessions,
		Internal:    true,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "sessions", Type: "array", Description: "Array of session summary objects"},
				{Name: "total", Type: "number", Description: "Total number of sessions for the user"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "update_title",
		Description: "Update session title",
		Endpoint:    "/chat/session/{id}/title",
		Handler:     t.handleUpdateTitle,
		Internal:    true,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "ok", Type: "boolean", Description: "Whether the title was updated successfully"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "delete_session",
		Description: "Delete a chat session",
		Endpoint:    "/chat/session/delete",
		Handler:     t.handleDeleteSession,
		Internal:    true,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "ok", Type: "boolean", Description: "Whether the session was deleted successfully"},
			},
		},
	})

	// Health and discovery
	t.RegisterCapability(core.Capability{
		Name:        "health",
		Description: "Health check with orchestrator status",
		Endpoint:    "/health",
		Handler:     t.handleHealth,
		Internal:    true,
	})

	t.RegisterCapability(core.Capability{
		Name:        "discover",
		Description: "Discover available tools",
		Endpoint:    "/discover",
		Handler:     t.handleDiscover,
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

// GetOrchestrator returns the orchestrator (for handlers that need it).
func (t *TravelChatAgent) GetOrchestrator() *orchestration.AIOrchestrator {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.orchestrator
}

// GetSessionStore returns the session store.
func (t *TravelChatAgent) GetSessionStore() *SessionStore {
	return t.sessionStore
}
