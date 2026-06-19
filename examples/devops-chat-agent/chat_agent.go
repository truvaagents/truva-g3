package main

import (
	"context"
	"errors"
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

// DevOpsChatAgent is a streaming chat agent that uses orchestration
// to coordinate DevOps and Kubernetes tools and provide real-time responses via SSE.
type DevOpsChatAgent struct {
	*core.BaseAgent
	orchestrator *orchestration.AIOrchestrator
	sessionStore *SessionStore
	httpClient   *http.Client // Traced HTTP client
	hitl         *HITLInfrastructure
	mu           sync.RWMutex

	// LLM debug recording — populated only when TRUVAG3_LLM_DEBUG_ENABLED=true.
	// Both are nil when the env var is unset. main.go calls Shutdown(ctx) /
	// Close() on these during graceful shutdown to drain in-flight async
	// recordings and release the recorder's Redis connection.
	instrumentedClient *ai.InstrumentedAIClient
	debugRecorder      *telemetry.RedisLLMCallRecorder
}

// NewDevOpsChatAgent creates a new DevOps chat agent with AI and telemetry configured.
func NewDevOpsChatAgent() (*DevOpsChatAgent, error) {
	agent := core.NewBaseAgent("devops-chat-agent")

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

	// Wrap agent.AI with InstrumentedAIClient so background LLM calls (e.g. the
	// ReflectionJob and KnowledgeExtractionHook) are recorded to the LLM debug store
	// and visible in the registry-viewer LLM Debug screen alongside orchestrator calls.
	// Recording is keyed by request_id baggage on the call context — the reflection
	// job sets this to its pass_id ("reflect-XXXXXXXX").
	//
	// The recorder and wrapped client are captured in local variables here and stored
	// on the DevOpsChatAgent struct below so main.go can Shutdown/Close them during
	// graceful shutdown to drain in-flight async recordings.
	var instrumentedClient *ai.InstrumentedAIClient
	var debugRecorder *telemetry.RedisLLMCallRecorder
	if os.Getenv("TRUVAG3_LLM_DEBUG_ENABLED") == "true" && agent.AI != nil {
		var recErr error
		debugRecorder, recErr = telemetry.NewRedisLLMCallRecorder(
			telemetry.WithRecorderLogger(agent.Logger),
		)
		if recErr != nil {
			agent.Logger.Warn("LLM debug recording unavailable for agent-side calls", map[string]interface{}{
				"error": recErr.Error(),
			})
			debugRecorder = nil
		} else {
			instrumentedClient = ai.NewInstrumentedClient(agent.AI, debugRecorder,
				ai.WithComponentName("devops-chat-agent"),
				ai.WithInstrumentedLogger(agent.Logger),
			)
			agent.AI = instrumentedClient
		}
	}

	// Declare metrics this agent will emit for observability
	telemetry.DeclareMetrics("devops-chat-agent", telemetry.ModuleConfig{
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

	chatAgent := &DevOpsChatAgent{
		BaseAgent:          agent,
		sessionStore:       sessionStore,
		httpClient:         tracedClient,
		instrumentedClient: instrumentedClient, // nil unless TRUVAG3_LLM_DEBUG_ENABLED=true
		debugRecorder:      debugRecorder,      // nil unless TRUVAG3_LLM_DEBUG_ENABLED=true
	}

	// Register capabilities
	chatAgent.registerCapabilities()

	return chatAgent, nil
}

// InitializeOrchestrator sets up the orchestrator after Discovery is available.
// memoryHooks are optional pipeline hooks for shared agent memory (episodic, knowledge, coordination).
// activityCoordinator is optional — enables real-time status updates at phase boundaries.
func (t *DevOpsChatAgent) InitializeOrchestrator(
	discovery core.Discovery,
	hitl *HITLInfrastructure,
	hitlConfig orchestration.HITLConfig,
	memoryHooks []core.PipelineHook,
	activityCoordinator core.ActivityCoordinator,
) error {
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
	config.HITL = hitlConfig

	// LLM token limits are read from env vars by DefaultConfig()
	// and stored in PlanAIOptions / SynthesisAIOptions.
	if config.SynthesisAIOptions == nil {
		config.SynthesisAIOptions = &orchestration.AIOptionsOverride{}
	}
	config.SynthesisAIOptions.Temperature = orchestration.Float32Ptr(0.7) // Higher temperature for natural streaming responses

	// Increase timeouts for complex multi-tool orchestration scenarios
	config.ExecutionOptions.TotalTimeout = 5 * time.Minute  // Overall orchestration timeout
	config.ExecutionOptions.StepTimeout = 120 * time.Second // Per-step timeout for AI planning

	// Configure prompt builder for DevOps domain
	config.PromptConfig = orchestration.PromptConfig{
		// SystemInstructions defines the chat agent's persona and behavioral context.
		// This becomes the primary identity, with the orchestrator role as secondary.
		SystemInstructions: `You are a helpful operations assistant running inside a Kubernetes cluster.
Your primary focus is helping users manage, monitor, and troubleshoot the cluster and the services
running on it. You have access to a variety of tools discovered at runtime through a service registry —
use them to answer questions, gather data, and take actions on behalf of the user.

You can handle a wide range of queries: checking cluster health, inspecting workloads, reading logs,
scaling services, looking up real-time information (weather, news, financials), and performing general
utility tasks. When a user's request spans multiple tools or steps, coordinate them into a coherent plan.

Be precise and cautious with any operation that mutates cluster state. Clearly communicate what you are
about to do before executing it, especially for destructive or irreversible actions. After completing an
action, summarize what happened and what the current state is.

If a request is ambiguous or underspecified — for example, the target resource, namespace, or intended
action is unclear — ask the user to clarify before proceeding. It is always better to ask than to act
on an incorrect assumption in an operations context.

Infrastructure systems are complex — commands fail, resources move, and state changes between steps.
Plan your work iteratively: start with investigation, act on what you learn, and adapt when something
fails. When a step errors, diagnose why (check logs, describe the resource, verify names), adjust your
approach, and retry. Exhaust available alternatives before concluding that a task cannot be completed.
The operator is relying on you to give your best effort — a partial resolution with clear next steps
is always more valuable than giving up early.`,

		Domain: "devops",

		AdditionalTypeRules: []orchestration.TypeRule{
			{
				TypeNames:   []string{"namespace"},
				JsonType:    "JSON strings",
				Example:     `"truvag3-examples"`,
				Description: "Kubernetes namespace names (e.g., truvag3-examples, default, kube-system)",
			},
			{
				TypeNames:   []string{"replicas"},
				JsonType:    "JSON numbers",
				Example:     `3`,
				Description: "Integer replica counts between 0 and 10",
			},
			{
				TypeNames:   []string{"resource_type"},
				JsonType:    "JSON strings",
				Example:     `"deployment"`,
				Description: "Kubernetes resource types: pod, deployment, service, node, configmap, ingress, secret",
			},
			{
				TypeNames:   []string{"pod_name", "deployment_name", "resource_name"},
				JsonType:    "JSON strings",
				Example:     `"weather-tool-abc123"`,
				Description: "Kubernetes resource names as shown by kubectl get",
			},
			{
				TypeNames:   []string{"args"},
				JsonType:    "JSON strings",
				Example:     `"get nodes -o wide"`,
				Description: "kubectl arguments without the 'kubectl' prefix",
			},
			{
				TypeNames:   []string{"timezone"},
				JsonType:    "JSON strings",
				Example:     `"America/New_York"`,
				Description: "IANA timezone names (e.g., UTC, Asia/Tokyo, Europe/London)",
			},
			{
				TypeNames:   []string{"command"},
				JsonType:    "JSON strings",
				Example:     `"echo hello"`,
				Description: "Shell commands for execute_command (run inside isolated container)",
			},
		},

		CustomInstructions: []string{
			"When a Kubernetes namespace is not specified, default to 'truvag3-examples'",
			"Before any mutating operation (scaling, restarting), first inspect the current state of the target resource",
			"Prefer parallel execution when steps are independent AND target different backends; when multiple steps would query the same upstream service, sequence them across phases instead",
			"When troubleshooting pod issues, start by examining logs and resource descriptions",
			"When a step fails, investigate the error using available tools (logs, describe, status), adjust parameters or approach, and retry — escalate to the operator only after exhausting alternatives",
			"When a dedicated capability exists for an operation, prefer it over generic command execution",
			"Always summarize results in plain language — avoid dumping raw output without context",

			// Observability tool usage patterns (logs + traces)
			"To investigate a specific request_id, always use query_logs with {service_name=~\".+\"} |= \"request-id\" first — this finds the trace_id in stream labels, which you then pass to get_trace for the full distributed trace with all spans, errors, and durations",
			"Always follow up find_traces results with get_trace on the specific trace_id to get actual span details — find_traces returns summaries only, get_trace returns the full span tree with errors and tags",
			"When using find_traces, target the specific service the user asked about and use filters (min_duration, operation) — unfiltered queries across multiple services return health-check heartbeat noise",
			"For end-to-end request troubleshooting, prefer this 3-step pattern: query_logs (find errors + trace_id) → get_trace (full span tree) → query_logs (service-specific error details)",
			"When investigating logs across many services or a wide time range (questions like 'does X happen anywhere?', 'are any services doing Y?'), decompose into a discover-then-iterate plan: a discovery step to identify the relevant services or time bounds, then narrow per-service or per-window log queries in subsequent phases.",
			"After any action that changes infrastructure state (e.g., rollout restart, scaling, pod deletion), document it in JIRA and notify via Slack. First check <agent_memory> for a recent ticket key (DEVOPS-NNN) on the same entity — if found, use add_comment directly. If no ticket is visible in memory, use search_issues to query JIRA for open tickets on this entity. If an open ticket exists, use add_comment with your actions and findings. Only create a new ticket under project key 'DEVOPS' when both memory and JIRA confirm no recent ticket exists. Then send a Slack notification to #notifications with an informative incident note: alert summary, findings with specific metrics and pod names, root cause, remediation taken, verification results, and the JIRA ticket link (use the browse_url returned by create_issue or search_issues). Write naturally using short paragraphs.",
			"When referencing a JIRA ticket, use the browse_url field returned by the jira-tool (create_issue, search_issues, get_issue)",
			"When the user asks you to perform an action (scale pods, restart a rollout, comment on a JIRA ticket, send a Slack message), invoke the corresponding capability and confirm it completed before reporting back. For asynchronous actions (a rollout settling, pods scaling up or down), poll the relevant status capability until you observe concrete evidence of completion. Report success only on that evidence; if the action failed, report what failed and what you tried.",
			"The devops_operations capability is this agent's own delegation endpoint — use the specific tool capabilities (devops-tool, jira-tool, slack-tool, etc.) directly instead",
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

	// Create dependencies using factory pattern with dependency injection
	deps := orchestration.OrchestratorDependencies{
		Discovery:                   discovery,
		AIClient:                    t.AI,
		Logger:                      t.Logger,
		Telemetry:                   telemetry.GetTelemetryProvider(),
		EnableErrorAnalyzer:         true,
		PipelineHooks:               memoryHooks,
		ActivityCoordinator:         activityCoordinator,
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

	// Wire HITL controller if infrastructure is available
	if hitl != nil {
		orch.SetInterruptController(hitl.Controller)
		t.hitl = hitl
	}

	t.orchestrator = orch

	t.Logger.Info("Orchestrator initialized successfully", map[string]interface{}{
		"routing_mode":       config.RoutingMode,
		"synthesis_strategy": config.SynthesisStrategy,
		"hitl_enabled":       hitlConfig.Enabled,
	})

	return nil
}

// formatConversationHistory formats conversation history for the <conversation_history> tag.
// The orchestrator wraps this in XML and keeps it separate from the user request
// per EFFECTIVE_PROMPTS_GUIDE §2.8 and §2.10.
func (t *DevOpsChatAgent) formatConversationHistory(history []Message) string {
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

func conversationTurnsFromMessages(history []Message) []core.ConversationTurn {
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

func (t *DevOpsChatAgent) addConversationHistoryMetadata(metadata map[string]interface{}, sessionID string, history []Message) {
	if sessionID == "" || len(history) == 0 {
		return
	}
	metadata[orchestration.MetadataConversationTurns] = conversationTurnsFromMessages(history)
	metadata[orchestration.MetadataConversationSessionKey] = sessionID
	if formattedHistory := t.formatConversationHistory(history); formattedHistory != "" {
		metadata[core.EnrichmentConversationHistory] = formattedHistory
	}
}

// ProcessWithStreaming processes a user query and streams progress via callback.
// It uses true streaming when the orchestrator supports it, falling back to
// simulated streaming (chunking the complete response) otherwise.
func (t *DevOpsChatAgent) ProcessWithStreaming(ctx context.Context, sessionID, query string, callback StreamCallback) error {
	startTime := time.Now()

	t.mu.RLock()
	orch := t.orchestrator
	t.mu.RUnlock()

	if orch == nil {
		return fmt.Errorf("orchestrator not initialized")
	}

	// Detect if this is a HITL resume (skip "Analyzing..." status)
	_, isResuming := orchestration.IsResumeMode(ctx)

	// Retrieve conversation history for context.
	// History is passed via metadata so the orchestrator injects it as a
	// <conversation_history> tag, keeping it separate from <user_request>.
	history := t.sessionStore.GetHistory(sessionID)
	metadata := map[string]interface{}{}
	t.addConversationHistoryMetadata(metadata, sessionID, history)

	// Log start with trace context
	t.Logger.InfoWithContext(ctx, "Processing chat request", map[string]interface{}{
		"operation":     "process_chat",
		"session_id":    sessionID,
		"query_len":     len(query),
		"history_turns": len(history),
		"is_resuming":   isResuming,
	})

	// Send planning status (skip for resume — already sent from handleResumeSSE)
	if !isResuming {
		callback.SendStatus("planning", "Analyzing your request...")
	}

	// Set request mode and metadata for HITL checkpoint preservation
	ctx = orchestration.WithRequestMode(ctx, orchestration.RequestModeStreaming)
	ctx = orchestration.WithMetadata(ctx, map[string]interface{}{
		"session_id": sessionID,
	})

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
		// Check for HITL interrupt — execution paused for human approval
		if orchestration.IsInterrupted(err) {
			checkpoint := orchestration.GetCheckpoint(err)
			if checkpoint != nil {
				callback.SendCheckpoint(checkpoint)
			}
			return err // Signal interrupt to caller
		}

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
		"request_id":       requestID,
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
	callback.SendDone(requestID, agentsInvolved, time.Since(startTime).Milliseconds(), &result.OrchestratorResponse)

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

// ProcessQuery handles a non-streaming query for agent-to-agent delegation.
// Uses the synchronous ProcessRequest path (no SSE). When sessionID is provided,
// conversation history is included for contextual follow-ups.
func (t *DevOpsChatAgent) ProcessQuery(ctx context.Context, sessionID, query string) (*orchestration.OrchestratorResponse, error) {
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
		attribute.String("agent", "devops-chat-agent"),
		attribute.Int("query_length", len(query)),
	)

	// Mark as non-streaming so HITL checkpoints get the correct request_mode.
	// Without this, checkpoints have empty request_mode and the registry viewer
	// cannot distinguish them from streaming requests.
	ctx = orchestration.WithRequestMode(ctx, orchestration.RequestModeNonStreaming)

	// Non-streaming orchestration — blocks until plan + execute + synthesize complete.
	// Loop handles multi-gate HITL: a resumed plan may hit a second gate.
	currentCtx := ctx
	currentQuery := query
	result, err := orch.ProcessRequest(currentCtx, currentQuery, metadata)
	for err != nil && orchestration.IsInterrupted(err) {
		checkpoint := orchestration.GetCheckpoint(err)
		if checkpoint == nil {
			noCheckpointErr := fmt.Errorf("HITL interrupted but no checkpoint available")
			telemetry.RecordSpanError(currentCtx, noCheckpointErr)
			return nil, noCheckpointErr
		}

		telemetry.AddSpanEvent(currentCtx, "hitl.delegation.wait_started",
			attribute.String("request_id", checkpoint.RequestID),
			attribute.String("checkpoint_id", checkpoint.CheckpointID),
			attribute.String("interrupt_point", string(checkpoint.InterruptPoint)),
		)
		t.Logger.InfoWithContext(currentCtx, "Delegation paused for HITL approval", map[string]interface{}{
			"operation":           "process_query",
			"checkpoint_id":       checkpoint.CheckpointID,
			"request_id":          checkpoint.RequestID,
			"original_request_id": checkpoint.OriginalRequestID,
		})

		// Wait for human decision via Redis SUBSCRIBE (event-driven, not polling)
		hitl := t.GetHITL()
		if hitl == nil {
			noHITLErr := fmt.Errorf("HITL interrupted but HITL infrastructure not configured")
			telemetry.RecordSpanError(currentCtx, noHITLErr)
			return nil, noHITLErr
		}
		hitlTimeout := 15 * time.Minute // matches TRUVAG3_HITL_DEFAULT_TIMEOUT
		// Use CommandStore.SubscribeCommand directly (interface-first design).
		// WaitForCommand on WebhookInterruptHandler is just a thin wrapper around this.
		// Using the interface avoids constructing an unnecessary concrete handler.
		cmdChan, cleanup, subErr := hitl.CommandStore.SubscribeCommand(currentCtx, checkpoint.CheckpointID)
		if subErr != nil {
			telemetry.RecordSpanError(currentCtx, subErr)
			return nil, fmt.Errorf("failed to subscribe for HITL command: %w", subErr)
		}

		timer := time.NewTimer(hitlTimeout)
		var cmd *orchestration.Command
		var waitErr error
		select {
		case cmd = <-cmdChan:
			// Human responded
		case <-timer.C:
			waitErr = fmt.Errorf("HITL timeout: no decision within %v", hitlTimeout)
		case <-currentCtx.Done():
			waitErr = currentCtx.Err()
		}
		timer.Stop()
		cleanup()

		if waitErr != nil {
			// Timeout or context cancelled — return structured response, not error
			outcome := "expired"
			if errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) {
				outcome = "cancelled"
			}
			telemetry.AddSpanEvent(currentCtx, "hitl.delegation.wait_completed",
				attribute.String("request_id", checkpoint.RequestID),
				attribute.String("outcome", outcome),
				attribute.String("checkpoint_id", checkpoint.CheckpointID),
			)
			return &orchestration.OrchestratorResponse{
				RequestID: checkpoint.RequestID,
				Response:  fmt.Sprintf("HITL %s: no decision within timeout for checkpoint %s", outcome, checkpoint.CheckpointID),
			}, nil
		}

		telemetry.AddSpanEvent(currentCtx, "hitl.delegation.wait_completed",
			attribute.String("request_id", checkpoint.RequestID),
			attribute.String("outcome", string(cmd.Type)),
			attribute.String("checkpoint_id", checkpoint.CheckpointID),
		)

		// Rejected — return structured response, not error
		if cmd.Type == orchestration.CommandReject {
			return &orchestration.OrchestratorResponse{
				RequestID: checkpoint.RequestID,
				Response:  fmt.Sprintf("HITL rejected: %s", cmd.Feedback),
			}, nil
		}

		// Approved — resume execution from checkpoint
		checkpoint.Status = orchestration.CheckpointStatusApproved
		resumeCtx, endSpan, buildErr := orchestration.BuildResumeContext(currentCtx, checkpoint)
		if buildErr != nil {
			telemetry.RecordSpanError(currentCtx, buildErr)
			return nil, fmt.Errorf("failed to build resume context: %w", buildErr)
		}

		result, err = orch.ProcessRequest(resumeCtx, checkpoint.OriginalRequest, nil)

		// Mark checkpoint completed (or failed) regardless of outcome
		if err == nil || !orchestration.IsInterrupted(err) {
			checkpoint.Status = orchestration.CheckpointStatusCompleted
			if saveErr := hitl.CheckpointStore.SaveCheckpoint(resumeCtx, checkpoint); saveErr != nil {
				t.Logger.WarnWithContext(resumeCtx, "Failed to mark checkpoint completed", map[string]interface{}{
					"operation":     "process_query",
					"request_id":    checkpoint.RequestID,
					"checkpoint_id": checkpoint.CheckpointID,
					"error":         saveErr.Error(),
				})
			}
		}

		endSpan()
		currentCtx = resumeCtx
		// Loop continues if err is another ErrInterrupted (multi-gate plan)
	}
	if err != nil {
		// Real error — not HITL
		t.Logger.ErrorWithContext(currentCtx, "Query orchestration failed", map[string]interface{}{
			"operation":   "process_query",
			"error":       err.Error(),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		telemetry.RecordSpanError(currentCtx, err)
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
func (t *DevOpsChatAgent) registerCapabilities() {
	// Agent-as-Tool: Non-streaming query endpoint for agent-to-agent delegation.
	// Internal: false (omitted) so other agents' orchestrators can discover and call this.
	// Ref: AGENT_DEVELOPMENT_GUIDE.md §6 "When to Use Internal: false (Agent-as-Tool)"
	t.RegisterCapability(core.Capability{
		Name: "devops_operations",
		Description: "Handles Kubernetes and infrastructure operations end-to-end: from investigation and diagnostics " +
			"to remediation and verification. Delegate any DevOps task as a natural language request and receive " +
			"a synthesized response with findings, actions taken, and current state.",
		Endpoint:    "/query",
		Type:        core.CapabilityOrchestrator,
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleQuery,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "Which pods in truvag3-examples are not Ready and what do their logs say?",
					Description: "Natural language DevOps or infrastructure question to delegate",
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
				{Name: "response", Type: "string", Description: "AI-synthesized DevOps answer"},
				{Name: "tools_used", Type: "array", Description: "List of tool names invoked during orchestration"},
				{Name: "execution_time", Type: "string", Description: "Total request duration as a human-readable string"},
				{Name: "confidence", Type: "number", Description: "Response confidence score between 0.0 and 1.0"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "metadata", Type: "object", Description: "Additional orchestration metadata"},
				{Name: "steps", Type: "array", Description: "Structured execution steps: tool name, instruction, response, success, duration, HITL metadata"},
				{Name: "usage", Type: "object", Description: "Total AI token consumption: prompt_tokens, completion_tokens, total_tokens"},
				{Name: "usage_by_phase", Type: "object", Description: "Token usage breakdown by orchestration phase: planning, synthesis, error_analysis, etc."},
			},
		},
	})

	// SSE streaming endpoint
	t.RegisterCapability(core.Capability{
		Name:        "chat_stream",
		Description: "SSE streaming chat endpoint for DevOps queries",
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
func (t *DevOpsChatAgent) GetOrchestrator() *orchestration.AIOrchestrator {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.orchestrator
}

// GetSessionStore returns the session store.
func (t *DevOpsChatAgent) GetSessionStore() *SessionStore {
	return t.sessionStore
}

// GetHITL returns the HITL infrastructure (nil if not enabled).
func (t *DevOpsChatAgent) GetHITL() *HITLInfrastructure {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hitl
}

// RegisterHITLCapabilities registers HITL-specific endpoints using the framework's handler.
func (t *DevOpsChatAgent) RegisterHITLCapabilities(hitlHandler *orchestration.HITLHandler) {
	// Custom SSE resume endpoint (returns SSE, not JSON)
	t.RegisterCapability(core.Capability{
		Name:        "hitl_resume_sse",
		Description: "Resume execution after approval (SSE)",
		Endpoint:    "/hitl/resume/{id}",
		Handler:     t.handleResumeSSE,
		Internal:    true,
	})

	// Auto-resume endpoint for expired_approved checkpoints (SSE)
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
