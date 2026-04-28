package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// HITLChatAgent is a streaming chat agent with Human-in-the-Loop enabled.
// It uses orchestration with HITL checkpoints for human oversight.
type HITLChatAgent struct {
	*core.BaseAgent
	orchestrator *orchestration.AIOrchestrator
	sessionStore *SessionStore
	httpClient   *http.Client
	hitl         *HITLInfrastructure
	mu           sync.RWMutex
}

// NewHITLChatAgent creates a new HITL-enabled chat agent.
func NewHITLChatAgent() (*HITLChatAgent, error) {
	agent := core.NewBaseAgent("agent-with-human-approval")

	// Create AI client with provider chain for failover.
	// Auto-detect mode: discovers available providers from API keys and orders by priority.
	//
	// To explicitly control the chain order instead of auto-detecting, use WithProviderChain:
	//   ai.NewChainClient(
	//       ai.WithProviderChain("openai", "anthropic"),  // explicit order, no auto-detection
	//       ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
	//       ai.WithChainLogger(agent.Logger),
	//   )
	chainClient, err := ai.NewChainClient(
		ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
		ai.WithChainLogger(agent.Logger),
	)
	if err != nil {
		agent.Logger.Warn("Failed to create AI chain client, trying single provider", map[string]interface{}{
			"error": err.Error(),
		})
		singleClient, err := ai.NewClient()
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

	// Declare metrics this agent will emit
	telemetry.DeclareMetrics("agent-with-human-approval", telemetry.ModuleConfig{
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
				Name:   "hitl.checkpoints.created",
				Type:   "counter",
				Help:   "Number of HITL checkpoints created",
				Labels: []string{"interrupt_point"},
			},
			{
				Name:   "hitl.approvals",
				Type:   "counter",
				Help:   "Number of HITL approval decisions",
				Labels: []string{"decision"},
			},
		},
	})

	// Create traced HTTP client
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 300 * time.Second

	// Create Redis-backed session store
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return nil, fmt.Errorf("REDIS_URL is required for session storage")
	}
	sessionStore, err := NewSessionStore(redisURL, 48*time.Hour, 50, agent.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create session store: %w", err)
	}

	chatAgent := &HITLChatAgent{
		BaseAgent:    agent,
		sessionStore: sessionStore,
		httpClient:   tracedClient,
	}

	// Register capabilities
	chatAgent.registerCapabilities()

	return chatAgent, nil
}

// InitializeOrchestrator sets up the orchestrator with HITL controller.
func (t *HITLChatAgent) InitializeOrchestrator(
	discovery core.Discovery,
	hitl *HITLInfrastructure,
	hitlConfig orchestration.HITLConfig,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if discovery == nil {
		return fmt.Errorf("discovery service not available")
	}

	// 1. Get base config and apply HITL settings
	config := orchestration.DefaultConfig()
	config.RoutingMode = orchestration.ModeAutonomous
	config.SynthesisStrategy = orchestration.StrategyLLM
	config.MetricsEnabled = true
	config.EnableTelemetry = true
	config.RequestIDPrefix = "awhl" // Agent With Human Loop - custom prefix for distributed tracing

	// LLM token limits are loaded by DefaultConfig() into PlanAIOptions / SynthesisAIOptions.
	if config.SynthesisAIOptions == nil {
		config.SynthesisAIOptions = &orchestration.AIOptionsOverride{}
	}
	config.SynthesisAIOptions.Temperature = orchestration.Float32Ptr(0.7) // Higher temperature for natural streaming responses

	// Increase timeouts for complex orchestration
	config.ExecutionOptions.TotalTimeout = 5 * time.Minute
	config.ExecutionOptions.StepTimeout = 120 * time.Second

	// Apply the hard-coded HITL config from main.go
	config.HITL = hitlConfig

	// Configure prompt builder for travel domain with HITL
	config.PromptConfig = orchestration.PromptConfig{
		SystemInstructions: `You are a friendly travel chat assistant with human oversight.
You help users plan trips by coordinating information from various travel services.
Be conversational and helpful while providing accurate, real-time information.
Note: All plans require human approval before execution.`,

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
		},
		CustomInstructions: []string{
			"For weather queries, always geocode the location first to get coordinates",
			"For currency conversion, extract the destination country's currency code",
			"Prefer parallel execution when steps are independent",
		},
	}

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

	// 2. Create dependencies
	deps := orchestration.OrchestratorDependencies{
		Discovery:                   discovery,
		AIClient:                    t.AI,
		Logger:                      t.Logger,
		Telemetry:                   telemetry.GetTelemetryProvider(),
		EnableErrorAnalyzer:         true,
		ConversationHistoryPreparer: conversationHistoryPreparer,
	}

	// 3. Create orchestrator using factory
	orch, err := orchestration.CreateOrchestrator(config, deps)
	if err != nil {
		return fmt.Errorf("failed to create orchestrator: %w", err)
	}

	// 4. Wire HITL controller AFTER creation
	if hitl != nil {
		orch.SetInterruptController(hitl.Controller)
		t.hitl = hitl
		t.Logger.Info("HITL controller configured", map[string]interface{}{
			"enabled":                hitlConfig.Enabled,
			"require_plan_approval":  hitlConfig.RequirePlanApproval,
			"sensitive_capabilities": hitlConfig.SensitiveCapabilities,
		})
	}

	// 5. Start the orchestrator
	ctx := context.Background()
	if err := orch.Start(ctx); err != nil {
		return fmt.Errorf("failed to start orchestrator: %w", err)
	}

	t.orchestrator = orch

	t.Logger.Info("Orchestrator initialized with HITL", map[string]interface{}{
		"routing_mode":       config.RoutingMode,
		"synthesis_strategy": config.SynthesisStrategy,
		"hitl_enabled":       hitlConfig.Enabled,
	})

	return nil
}

// formatConversationHistory formats conversation history for the <conversation_history> tag.
// The orchestrator wraps this in XML and keeps it separate from the user request
// per EFFECTIVE_PROMPTS_GUIDE §2.8 and §2.10.
func (t *HITLChatAgent) formatConversationHistory(history []Message) string {
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

func hitlConversationTurnsFromMessages(history []Message) []core.ConversationTurn {
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

func (t *HITLChatAgent) addConversationHistoryMetadata(metadata map[string]interface{}, sessionID string, history []Message) {
	if sessionID == "" || len(history) == 0 {
		return
	}
	metadata[orchestration.MetadataConversationTurns] = hitlConversationTurnsFromMessages(history)
	metadata[orchestration.MetadataConversationSessionKey] = sessionID
	if formattedHistory := t.formatConversationHistory(history); formattedHistory != "" {
		metadata[core.EnrichmentConversationHistory] = formattedHistory
	}
}

// ProcessWithStreaming processes a user query and streams progress via callback.
// If HITL is triggered, it returns orchestration.ErrInterrupted with the checkpoint.
func (t *HITLChatAgent) ProcessWithStreaming(ctx context.Context, sessionID, query string, callback StreamCallback) error {
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

	// Check if this is a resume from HITL checkpoint
	_, isResuming := orchestration.IsResumeMode(ctx)

	// Log start with trace context
	t.Logger.InfoWithContext(ctx, "Processing chat request with HITL", map[string]interface{}{
		"operation":     "process_chat",
		"session_id":    sessionID,
		"query_len":     len(query),
		"history_turns": len(history),
		"is_resume":     isResuming,
	})

	// Send planning status only for initial requests (not resume)
	// Resume flow already sent "Resuming execution..." status in handleResumeSSE
	if !isResuming {
		callback.SendStatus("planning", "Analyzing your request...")
	}

	// Add span event for planning start
	telemetry.AddSpanEvent(ctx, "orchestration.started",
		attribute.String("session_id", sessionID),
		attribute.Int("query_length", len(query)),
		attribute.Int("history_turns", len(history)),
		attribute.Bool("hitl_enabled", true),
	)

	// Add per-request step callback to context for real-time tool progress
	ctx = orchestration.WithStepCallback(ctx, func(stepIndex, totalSteps int, step orchestration.RoutingStep, stepResult orchestration.StepResult) {
		callback.SendStep(
			fmt.Sprintf("step_%d", stepIndex+1),
			step.AgentName,
			stepResult.Success,
			stepResult.Duration.Milliseconds(),
		)
	})

	// Store metadata in context for HITL checkpoint creation.
	// Also pass conversation history so orchestrator injects it as <conversation_history>.
	metadata := map[string]interface{}{
		"session_id": sessionID,
	}
	t.addConversationHistoryMetadata(metadata, sessionID, history)
	ctx = orchestration.WithMetadata(ctx, metadata)

	// Process with streaming - HITL may interrupt this
	result, err := orch.ProcessRequestStreaming(ctx, query, metadata, func(chunk core.StreamChunk) error {
		if chunk.Content != "" {
			callback.SendChunk(chunk.Content)
		}
		return nil
	})

	// Check for HITL interrupt - this is NOT an error
	if err != nil {
		if orchestration.IsInterrupted(err) {
			// This is expected behavior - execution paused for human approval
			checkpoint := orchestration.GetCheckpoint(err)
			if checkpoint != nil {
				t.Logger.InfoWithContext(ctx, "Execution interrupted for human approval", map[string]interface{}{
					"operation":       "process_chat",
					"session_id":      sessionID,
					"checkpoint_id":   checkpoint.CheckpointID,
					"interrupt_point": checkpoint.InterruptPoint,
				})

				// Record metric
				telemetry.Counter("hitl.checkpoints.created",
					"interrupt_point", string(checkpoint.InterruptPoint),
				)

				// Mark planning phase as complete before sending checkpoint
				callback.SendStep("planning", "Request analyzed", true, time.Since(startTime).Milliseconds())

				// Send checkpoint to client via SSE
				// Note: The frontend shows its own "Waiting for approval" via showWaitingApproval()
				// when it receives the checkpoint event, so we don't send a duplicate status here.
				callback.SendCheckpoint(checkpoint)
			}
			return err // Return the error so caller knows it's interrupted
		}

		// Actual error
		t.Logger.ErrorWithContext(ctx, "Streaming orchestration failed", map[string]interface{}{
			"operation":   "process_chat",
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		return fmt.Errorf("streaming orchestration failed: %w", err)
	}

	// Success path - execution completed without HITL interrupt
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

	// Send usage stats if available
	if result.Usage != nil {
		callback.SendUsage(
			result.Usage.PromptTokens,
			result.Usage.CompletionTokens,
			result.Usage.TotalTokens,
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

	// Mark resuming step as complete if this was a resume from HITL
	if isResuming {
		callback.SendStep("resuming", "Execution resumed", true, time.Since(startTime).Milliseconds())
	}

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

// registerCapabilities registers the agent's HTTP endpoints.
func (t *HITLChatAgent) registerCapabilities() {
	// SSE streaming endpoint
	t.RegisterCapability(core.Capability{
		Name:        "chat_stream",
		Description: "SSE streaming chat endpoint with HITL support",
		Endpoint:    "/chat/stream",
		Handler:     NewSSEHandler(t).ServeHTTP,
		Internal:    true,
	})

	// Non-streaming (sync) chat endpoint - JSON request/response
	t.RegisterCapability(core.Capability{
		Name:        "chat_sync",
		Description: "Non-streaming chat endpoint with HITL support (JSON)",
		Endpoint:    "/chat",
		Handler:     t.handleChatSync,
		Internal:    true,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "request_id", Type: "string", Description: "Unique identifier for this orchestration request"},
				{Name: "session_id", Type: "string", Description: "Session identifier for conversation continuity"},
				{Name: "interrupted", Type: "boolean", Description: "Whether execution was paused for human approval"},
				{Name: "duration_ms", Type: "number", Description: "Request duration in milliseconds"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "original_request_id", Type: "string", Description: "Request ID from the first request in a HITL conversation for end-to-end tracing"},
				{Name: "response", Type: "string", Description: "AI-synthesized answer (empty if interrupted)"},
				{Name: "tools_used", Type: "array", Description: "List of tool names invoked during orchestration"},
				{Name: "confidence", Type: "number", Description: "Response confidence score between 0.0 and 1.0"},
				{Name: "checkpoint", Type: "object", Description: "HITL checkpoint details when interrupted (checkpoint_id, plan, decision)"},
				{Name: "metadata", Type: "object", Description: "Additional request metadata"},
			},
		},
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

	// Session management endpoints
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
		Description: "Health check with HITL status",
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

// RegisterHITLCapabilities registers HITL-specific endpoints using the framework's handler.
func (t *HITLChatAgent) RegisterHITLCapabilities(hitlHandler *orchestration.HITLHandler) {
	// Custom SSE resume endpoint (returns SSE, not JSON)
	t.RegisterCapability(core.Capability{
		Name:        "hitl_resume_sse",
		Description: "Resume execution after approval (SSE)",
		Endpoint:    "/hitl/resume/{id}",
		Handler:     t.handleResumeSSE,
		Internal:    true,
	})

	// Non-streaming resume endpoint (returns JSON)
	t.RegisterCapability(core.Capability{
		Name:        "hitl_resume_sync",
		Description: "Resume execution after approval (JSON)",
		Endpoint:    "/hitl/resume-sync/{id}",
		Handler:     t.handleResumeSyncJSON,
		Internal:    true,
		// Output: SyncResponse
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "request_id", Type: "string", Description: "Unique identifier for the resumed request"},
				{Name: "session_id", Type: "string", Description: "Session identifier associated with the conversation"},
				{Name: "interrupted", Type: "boolean", Description: "True if execution was interrupted again by another HITL checkpoint"},
				{Name: "duration_ms", Type: "number", Description: "Request duration in milliseconds"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "original_request_id", Type: "string", Description: "First request_id in this HITL conversation, for end-to-end Jaeger trace correlation"},
				{Name: "response", Type: "string", Description: "AI-synthesized answer (empty when interrupted=true)"},
				{Name: "tools_used", Type: "array", Description: "Names of tools invoked during the resumed execution"},
				{Name: "confidence", Type: "number", Description: "Confidence score between 0.0 and 1.0"},
				{Name: "checkpoint", Type: "object", Description: "New ExecutionCheckpoint when interrupted=true (contains checkpoint_id, plan, current_step, etc.)"},
				{Name: "metadata", Type: "object", Description: "Additional metadata passed through from orchestration"},
			},
		},
	})

	// Auto-resume endpoint for expired_approved checkpoints (SSE)
	// This is used when TRUVAG3_HITL_STREAMING_EXPIRY=apply_default
	// UI detects expired_approved via polling, then calls this endpoint to resume
	t.RegisterCapability(core.Capability{
		Name:        "hitl_auto_resume_sse",
		Description: "Auto-resume execution after expired_approved (SSE)",
		Endpoint:    "/hitl/auto-resume/{id}/stream",
		Handler:     t.handleAutoResumeSSE,
		Internal:    true,
	})

	// Standard HITL endpoints from the framework
	t.RegisterCapability(core.Capability{
		Name:        "hitl_command",
		Description: "Submit HITL command (approve/reject)",
		Endpoint:    "/hitl/command",
		Handler:     hitlHandler.HandleCommand,
		Internal:    true,
		// Output: orchestration.ResumeResult
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "checkpoint_id", Type: "string", Description: "Checkpoint identifier that was acted on"},
				{Name: "action", Type: "string", Description: "Command type that was processed (approve, reject, edit, abort, etc.)"},
				{Name: "should_resume", Type: "boolean", Description: "True if the caller should now POST /hitl/resume/{id} to continue execution"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "modified_plan", Type: "object", Description: "Updated RoutingPlan if the human edited the plan; nil otherwise"},
				{Name: "skip_step", Type: "boolean", Description: "True if the current step should be skipped on resume"},
				{Name: "abort", Type: "boolean", Description: "True if the entire workflow was aborted"},
				{Name: "feedback", Type: "string", Description: "Optional human feedback text captured with the command"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "hitl_checkpoints",
		Description: "List pending checkpoints",
		Endpoint:    "/hitl/checkpoints",
		Handler:     hitlHandler.HandleListCheckpoints,
		Internal:    true,
		// Output: orchestration.ListCheckpointsResponse
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "checkpoints", Type: "array", Description: "List of pending ExecutionCheckpoint objects with checkpoint_id, request_id, plan, current_step, completed_steps, resolved_parameters, created_at, expires_at, status"},
				{Name: "count", Type: "number", Description: "Number of checkpoints returned"},
				{Name: "limit", Type: "number", Description: "Page size that was applied (default 50)"},
				{Name: "offset", Type: "number", Description: "Pagination offset that was applied"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "hitl_checkpoint",
		Description: "Get checkpoint by ID",
		Endpoint:    "/hitl/checkpoints/{id}",
		Handler:     hitlHandler.HandleGetCheckpoint,
		Internal:    true,
		// Output: orchestration.ExecutionCheckpoint
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "checkpoint_id", Type: "string", Description: "Unique checkpoint identifier"},
				{Name: "request_id", Type: "string", Description: "Request identifier that created this checkpoint"},
				{Name: "interrupt_point", Type: "string", Description: "Where in execution the interrupt occurred"},
				{Name: "decision", Type: "object", Description: "InterruptDecision: policy, reason, sensitive agents/capabilities that triggered the interrupt"},
				{Name: "plan", Type: "object", Description: "RoutingPlan being executed when the interrupt occurred"},
				{Name: "completed_steps", Type: "array", Description: "List of StepResult entries for already-executed steps"},
				{Name: "step_results", Type: "object", Description: "Map of step_id to StepResult for all steps seen so far"},
				{Name: "original_request", Type: "string", Description: "The original user request text"},
				{Name: "created_at", Type: "string", Description: "RFC3339 timestamp when the checkpoint was created"},
				{Name: "expires_at", Type: "string", Description: "RFC3339 timestamp when the checkpoint will auto-expire"},
				{Name: "status", Type: "string", Description: "Checkpoint status (pending, approved, rejected, expired, etc.)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "original_request_id", Type: "string", Description: "Preserved across HITL conversations — first request_id in the chain"},
				{Name: "original_trace_id", Type: "string", Description: "W3C trace ID from the creating span, for distributed trace correlation"},
				{Name: "original_span_id", Type: "string", Description: "W3C span ID from the creating span, paired with original_trace_id"},
				{Name: "agent_name", Type: "string", Description: "Name of the agent that owns this checkpoint"},
				{Name: "agent_address", Type: "string", Description: "HTTP base address of the agent, for direct command routing"},
				{Name: "current_step", Type: "object", Description: "The step that was about to execute when interrupted"},
				{Name: "current_step_result", Type: "object", Description: "Partial StepResult if the interrupt happened mid-step"},
				{Name: "resolved_parameters", Type: "object", Description: "Real (template-resolved) parameter values for step-level HITL visibility"},
				{Name: "user_context", Type: "object", Description: "Arbitrary context carried from the request"},
				{Name: "request_mode", Type: "string", Description: "Request submission mode: streaming or non_streaming (affects expiry behavior)"},
				{Name: "phase_number", Type: "number", Description: "Iterative planning phase number (for multi-phase plans resumed after HITL approval)"},
				{Name: "accumulated_results", Type: "object", Description: "Map of step_id to StepResult accumulated across prior phases of iterative planning"},
				{Name: "executed_step_ids", Type: "array", Description: "List of step IDs already executed across phases, used to avoid re-execution"},
				{Name: "continuation_note_checkpoint", Type: "string", Description: "Continuation note for iterative planning resume"},
			},
		},
	})
}

// GetOrchestrator returns the orchestrator.
func (t *HITLChatAgent) GetOrchestrator() *orchestration.AIOrchestrator {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.orchestrator
}

// GetSessionStore returns the session store.
func (t *HITLChatAgent) GetSessionStore() *SessionStore {
	return t.sessionStore
}

// GetHITL returns the HITL infrastructure.
func (t *HITLChatAgent) GetHITL() *HITLInfrastructure {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hitl
}

// SyncResponse represents the response from non-streaming ProcessSync.
// When HITL is triggered, Interrupted=true and Checkpoint is populated.
type SyncResponse struct {
	// Request identification
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`

	// OriginalRequestID is the request_id from the first request in a HITL conversation.
	// Use this for end-to-end tracing in Jaeger (search by original_request_id tag).
	// For non-HITL requests: OriginalRequestID == RequestID
	// For HITL resumes: OriginalRequestID is preserved from the original request
	OriginalRequestID string `json:"original_request_id,omitempty"`

	// Response content (empty if interrupted)
	Response   string   `json:"response,omitempty"`
	ToolsUsed  []string `json:"tools_used,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`

	// HITL interrupt information
	Interrupted bool                               `json:"interrupted"`
	Checkpoint  *orchestration.ExecutionCheckpoint `json:"checkpoint,omitempty"`

	// Timing and metadata
	DurationMs int64                  `json:"duration_ms"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// ProcessSync processes a user query synchronously (non-streaming).
// Returns a SyncResponse with either the complete result or HITL checkpoint info.
// This is the non-streaming equivalent of ProcessWithStreaming.
// requestMetadata is optional user-provided metadata passed to the orchestrator.
func (t *HITLChatAgent) ProcessSync(ctx context.Context, sessionID, query string, requestMetadata map[string]interface{}) (*SyncResponse, error) {
	startTime := time.Now()

	t.mu.RLock()
	orch := t.orchestrator
	t.mu.RUnlock()

	if orch == nil {
		return nil, fmt.Errorf("orchestrator not initialized")
	}

	// Retrieve conversation history for context.
	// History is passed via metadata so the orchestrator injects it as a
	// <conversation_history> tag, keeping it separate from <user_request>.
	history := t.sessionStore.GetHistory(sessionID)

	// Check if this is a resume from HITL checkpoint
	resumedCheckpointID, isResuming := orchestration.IsResumeMode(ctx)

	// Log start with trace context
	t.Logger.InfoWithContext(ctx, "Processing sync chat request with HITL", map[string]interface{}{
		"operation":     "process_sync",
		"session_id":    sessionID,
		"query_len":     len(query),
		"history_turns": len(history),
		"is_resume":     isResuming,
	})

	// Add span event for processing start
	telemetry.AddSpanEvent(ctx, "orchestration.sync.started",
		attribute.String("session_id", sessionID),
		attribute.Int("query_length", len(query)),
		attribute.Int("history_turns", len(history)),
		attribute.Bool("hitl_enabled", true),
		attribute.Bool("is_resume", isResuming),
	)

	// Build metadata: merge user-provided metadata with session_id and history
	metadata := map[string]interface{}{
		"session_id": sessionID,
	}
	t.addConversationHistoryMetadata(metadata, sessionID, history)
	// Merge user-provided metadata (user values take precedence except session_id)
	for k, v := range requestMetadata {
		if k != "session_id" { // Preserve internal session_id
			metadata[k] = v
		}
	}
	ctx = orchestration.WithMetadata(ctx, metadata)

	// Process with non-streaming orchestrator - HITL may interrupt this
	result, err := orch.ProcessRequest(ctx, query, metadata)

	// Check for HITL interrupt - this is NOT an error, it's expected behavior
	if err != nil {
		if orchestration.IsInterrupted(err) {
			checkpoint := orchestration.GetCheckpoint(err)
			if checkpoint != nil {
				t.Logger.InfoWithContext(ctx, "Sync execution interrupted for human approval", map[string]interface{}{
					"operation":       "process_sync",
					"session_id":      sessionID,
					"checkpoint_id":   checkpoint.CheckpointID,
					"interrupt_point": checkpoint.InterruptPoint,
				})

				// Record metric
				telemetry.Counter("hitl.checkpoints.created",
					"interrupt_point", string(checkpoint.InterruptPoint),
				)

				telemetry.AddSpanEvent(ctx, "orchestration.sync.interrupted",
					attribute.String("checkpoint_id", checkpoint.CheckpointID),
					attribute.String("interrupt_point", string(checkpoint.InterruptPoint)),
				)

				return &SyncResponse{
					RequestID:   checkpoint.RequestID,
					SessionID:   sessionID,
					Interrupted: true,
					Checkpoint:  checkpoint,
					DurationMs:  time.Since(startTime).Milliseconds(),
					Metadata:    metadata,
				}, nil
			}
		}

		// Actual error
		t.Logger.ErrorWithContext(ctx, "Sync orchestration failed", map[string]interface{}{
			"operation":   "process_sync",
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(ctx, err)
		return nil, fmt.Errorf("orchestration failed: %w", err)
	}

	// Success path - execution completed
	response := result.Response
	requestID := result.RequestID
	agentsInvolved := result.AgentsInvolved
	confidence := result.Confidence

	// Add span event for completion
	completionAttrs := []attribute.KeyValue{
		attribute.String("request_id", requestID),
		attribute.Int("agents_used", len(agentsInvolved)),
		attribute.Float64("confidence", confidence),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	}
	if isResuming {
		completionAttrs = append(completionAttrs, attribute.String("resumed_from", resumedCheckpointID))
	}
	telemetry.AddSpanEvent(ctx, "orchestration.sync.completed", completionAttrs...)

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
		},
	})

	t.Logger.InfoWithContext(ctx, "Sync chat request completed", map[string]interface{}{
		"operation":   "process_sync",
		"session_id":  sessionID,
		"request_id":  requestID,
		"tools_used":  len(agentsInvolved),
		"duration_ms": time.Since(startTime).Milliseconds(),
		"status":      "success",
	})

	return &SyncResponse{
		RequestID:   requestID,
		SessionID:   sessionID,
		Response:    response,
		ToolsUsed:   agentsInvolved,
		Confidence:  confidence,
		Interrupted: false,
		DurationMs:  time.Since(startTime).Milliseconds(),
		Metadata:    metadata,
	}, nil
}
