package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// orchestratorContextKey is a custom type for orchestrator context keys to avoid collisions
type orchestratorContextKey string

const (
	// requestIDContextKey holds the orchestrator's request ID for correlation across components
	requestIDContextKey orchestratorContextKey = "orchestrator_request_id"
)

// WithRequestID adds the orchestrator's request ID to the context.
// This enables child components (like TieredCapabilityProvider) to correlate
// their debug recordings with the orchestrator's request.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, requestID)
}

// GetRequestID retrieves the orchestrator's request ID from context.
// Returns empty string if not set.
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(requestIDContextKey); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// resumeModeContextKey holds the checkpoint ID when resuming from a HITL checkpoint.
// This allows CheckPlanApproval to skip HITL checks during resume execution.
const resumeModeContextKey orchestratorContextKey = "orchestrator_resume_mode"

// WithResumeMode marks the context as resuming from a HITL checkpoint.
// When set, HITL checks (CheckPlanApproval, CheckBeforeStep) will be bypassed
// to prevent infinite loops during resume execution.
//
// Usage:
//
//	ctx = orchestration.WithResumeMode(ctx, checkpoint.CheckpointID)
//	result, err := orchestrator.ProcessRequestStreaming(ctx, checkpoint.OriginalRequest, metadata, callback)
func WithResumeMode(ctx context.Context, checkpointID string) context.Context {
	return context.WithValue(ctx, resumeModeContextKey, checkpointID)
}

// IsResumeMode checks if the context is in resume mode.
// Returns the checkpoint ID and true if resuming, empty string and false otherwise.
func IsResumeMode(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	if v := ctx.Value(resumeModeContextKey); v != nil {
		if id, ok := v.(string); ok && id != "" {
			return id, true
		}
	}
	return "", false
}

// clearResumeMode returns a context where IsResumeMode returns false.
// Used in multi-phase planning to ensure that only the pre-approved
// resume plan skips HITL checks. Phase 2+ plans are new and must go
// through independent HITL policy evaluation.
func clearResumeMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, resumeModeContextKey, "")
}

// metadataContextKey holds request metadata that should be preserved in checkpoints.
const metadataContextKey orchestratorContextKey = "orchestrator_metadata"

// WithMetadata attaches metadata to the context for HITL checkpoint preservation.
// This metadata (e.g., session_id, user_id) will be stored in checkpoint.UserContext
// and can be retrieved when resuming execution.
//
// Usage:
//
//	metadata := map[string]interface{}{"session_id": sessionID, "user_id": userID}
//	ctx = orchestration.WithMetadata(ctx, metadata)
//	result, err := orchestrator.ProcessRequestStreaming(ctx, query, nil, callback)
func WithMetadata(ctx context.Context, metadata map[string]interface{}) context.Context {
	if metadata == nil {
		return ctx
	}
	return context.WithValue(ctx, metadataContextKey, metadata)
}

// GetMetadata retrieves metadata from the context.
// Returns nil if no metadata is set.
func GetMetadata(ctx context.Context) map[string]interface{} {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(metadataContextKey); v != nil {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
	}
	return nil
}

// planOverrideContextKey holds a pre-approved plan for HITL resume flows.
// When set, ProcessRequest/ProcessRequestStreaming will skip LLM plan generation.
const planOverrideContextKey orchestratorContextKey = "orchestrator_plan_override"

// WithPlanOverride injects a pre-approved plan into context for HITL resume.
// When set, the orchestrator will use this plan instead of generating a new one via LLM.
// This is critical for HITL resume flows to ensure step IDs remain stable.
//
// Usage:
//
//	ctx = orchestration.WithPlanOverride(ctx, checkpoint.Plan)
//	result, err := orchestrator.ProcessRequestStreaming(ctx, checkpoint.OriginalRequest, metadata, callback)
func WithPlanOverride(ctx context.Context, plan *RoutingPlan) context.Context {
	if plan == nil {
		return ctx
	}
	return context.WithValue(ctx, planOverrideContextKey, plan)
}

// GetPlanOverride retrieves the injected plan from context.
// Returns nil if no plan override is set.
func GetPlanOverride(ctx context.Context) *RoutingPlan {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(planOverrideContextKey); v != nil {
		if p, ok := v.(*RoutingPlan); ok {
			return p
		}
	}
	return nil
}

// completedStepsContextKey holds step results from a HITL checkpoint.
// The executor will skip these steps and use the cached results.
const completedStepsContextKey orchestratorContextKey = "orchestrator_completed_steps"

// WithCompletedSteps injects already-completed step results into context.
// The executor will skip these steps and use the provided results for dependency resolution.
// This prevents re-execution of steps that were completed before a HITL checkpoint.
//
// Usage:
//
//	ctx = orchestration.WithCompletedSteps(ctx, checkpoint.StepResults)
//	result, err := orchestrator.ProcessRequestStreaming(ctx, checkpoint.OriginalRequest, metadata, callback)
func WithCompletedSteps(ctx context.Context, results map[string]*StepResult) context.Context {
	if results == nil {
		return ctx
	}
	return context.WithValue(ctx, completedStepsContextKey, results)
}

// GetCompletedSteps retrieves completed step results from context.
// Returns nil if no completed steps are set.
func GetCompletedSteps(ctx context.Context) map[string]*StepResult {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(completedStepsContextKey); v != nil {
		if r, ok := v.(map[string]*StepResult); ok {
			return r
		}
	}
	return nil
}

// oauthTokenContextKey holds the Bearer token for outbound HTTP calls.
// When set via WithOAuthToken(), this per-request token takes priority over
// the configured OAuthToken in OrchestratorConfig. This supports user-facing
// agents that pass through the end-user's token.
const oauthTokenContextKey orchestratorContextKey = "orchestrator_oauth_token"

// WithOAuthToken attaches a Bearer token to the context for downstream propagation.
// The executor will include this token as an Authorization: Bearer header on all
// outbound HTTP calls to tool/agent endpoints.
//
// This is the primary mechanism for Scenario 1 (user token pass-through):
// the agent extracts the token from the incoming request and injects it into
// context before calling ProcessRequest.
//
// Usage:
//
//	func handleRequest(w http.ResponseWriter, r *http.Request) {
//	    token := extractBearerToken(r)  // from Authorization header
//	    ctx := orchestration.WithOAuthToken(r.Context(), token)
//	    result, err := orchestrator.ProcessRequest(ctx, request, nil)
//	}
func WithOAuthToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, oauthTokenContextKey, token)
}

// GetOAuthToken retrieves the Bearer token from context.
// Returns empty string if no token is set.
func GetOAuthToken(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(oauthTokenContextKey); v != nil {
		if t, ok := v.(string); ok {
			return t
		}
	}
	return ""
}

// propagatedHeadersContextKey holds a map[string]string of custom headers
// to inject into outbound HTTP calls. Context headers override config headers.
const propagatedHeadersContextKey orchestratorContextKey = "orchestrator_propagated_headers"

// WithPropagatedHeaders attaches custom headers to the context for downstream propagation.
// The executor will include these headers on all outbound HTTP calls to tool/agent endpoints.
// Context headers override config-level PropagatedHeaders on key conflict.
//
// Usage:
//
//	func handleRequest(w http.ResponseWriter, r *http.Request) {
//	    headers := map[string]string{
//	        "X-Correlation-ID": r.Header.Get("X-Correlation-ID"),
//	        "X-Tenant-ID":      r.Header.Get("X-Tenant-ID"),
//	    }
//	    ctx := orchestration.WithPropagatedHeaders(r.Context(), headers)
//	    result, err := orchestrator.ProcessRequest(ctx, request, nil)
//	}
func WithPropagatedHeaders(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return context.WithValue(ctx, propagatedHeadersContextKey, headers)
}

// AddPropagatedHeader adds a single header to the context's propagated headers.
// If the context already has propagated headers, the new key is merged in.
// Empty keys are ignored (returns ctx unchanged).
func AddPropagatedHeader(ctx context.Context, key, value string) context.Context {
	if key == "" {
		return ctx
	}
	existing := GetPropagatedHeaders(ctx)
	merged := make(map[string]string, len(existing)+1)
	for k, v := range existing {
		merged[k] = v
	}
	merged[key] = value
	return context.WithValue(ctx, propagatedHeadersContextKey, merged)
}

// GetPropagatedHeaders retrieves the custom headers map from context.
// Returns nil if no headers are set.
func GetPropagatedHeaders(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(propagatedHeadersContextKey); v != nil {
		if h, ok := v.(map[string]string); ok {
			return h
		}
	}
	return nil
}

// reservedPropagationHeaders contains header names that propagated headers must not
// override. These are set by the framework itself and overriding them would break
// OAuth authentication, content negotiation, or distributed tracing.
//
// Keys are canonical (http.CanonicalHeaderKey) for case-insensitive matching.
var reservedPropagationHeaders = map[string]bool{
	"Authorization":                 true, // OAuth Bearer token (set by executor)
	"Content-Type":                  true, // Always application/json (set by executor)
	"X-Truvag3-Request-Id":          true, // Distributed tracing (set by executor)
	"X-Truvag3-Original-Request-Id": true, // Original request id across HITL resume (set by executor)
	"X-Truvag3-Conversation-Id":     true, // Multi-turn conversation correlation (set by executor)
	"X-Truvag3-Step-Id":             true, // Step correlation (set by executor)
	"X-Truvag3-Phase-Number":        true, // Phase correlation (set by executor)
	"X-Truvag3-Plan-Id":             true, // Plan correlation (set by executor)
	"X-Truvag3-Agent-Name":          true, // Caller agent identity (set by executor)
	"X-Workflow-Id":                 true, // Workflow correlation (set by workflow executor)
	"X-Step-Id":                     true, // Workflow step (set by workflow executor)
}

// isReservedPropagationHeader returns true if the given header name must not be
// overridden by propagated headers. Comparison is case-insensitive via
// http.CanonicalHeaderKey.
func isReservedPropagationHeader(name string) bool {
	return reservedPropagationHeaders[http.CanonicalHeaderKey(name)]
}

// PlanningPromptResult contains the prompt and metadata for hallucination validation.
// When buildPlanningPrompt returns this, the caller can validate that LLM-generated
// plans only reference agents that were included in the prompt.
// See orchestration/bugs/BUG_LLM_HALLUCINATED_TOOL.md for detailed analysis.
type PlanningPromptResult struct {
	// Prompt is the complete prompt to send to the LLM
	Prompt string
	// AllowedAgents contains agent names that were included in the prompt.
	// Used to validate that the LLM didn't hallucinate non-existent agents.
	AllowedAgents map[string]bool
	// SystemPrompt is the system-level message for LLM providers that support
	// separate system/user message roles (e.g., Anthropic, OpenAI).
	// When empty, the prompt is sent as a single user message.
	// See BUG_PHASE3_SKIPPED_EXECUTION.md Issue 5 P10.
	SystemPrompt string
}

// HallucinationContext captures context about a hallucinated agent for enhanced retry.
// This is a GENERIC structure - no domain-specific knowledge required.
// See orchestration/bugs/BUG_LLM_HALLUCINATED_TOOL.md Fix 3 for detailed design.
type HallucinationContext struct {
	// AgentName is the hallucinated agent name (e.g., "calculator")
	AgentName string
	// Capability is from the plan step's metadata (e.g., "calculate")
	Capability string
	// Instruction is what the LLM was trying to accomplish (e.g., "Multiply 100 by stock price")
	Instruction string
}

// extractHallucinationContext extracts context from a failed plan for enhanced retry.
// This function is GENERIC - it extracts whatever the LLM was trying to do without
// any domain-specific interpretation.
func extractHallucinationContext(plan *RoutingPlan, hallucinatedAgent string) *HallucinationContext {
	ctx := &HallucinationContext{
		AgentName: hallucinatedAgent,
	}

	if plan == nil {
		return ctx
	}

	// Find the step with the hallucinated agent
	for _, step := range plan.Steps {
		if step.AgentName == hallucinatedAgent {
			ctx.Instruction = step.Instruction
			if step.Metadata != nil {
				// Extract capability from metadata
				if cap, ok := step.Metadata["capability"].(string); ok {
					ctx.Capability = cap
				}
			}
			break
		}
	}

	return ctx
}

// buildEnhancedRequestForRetry creates an enhanced request for tiered selection.
//
// DESIGN: This is GENERIC and domain-agnostic. Instead of mapping "calculator" to
// ["math", "calculation"] (which would require domain knowledge), we pass the
// hallucinated agent/capability/instruction directly to the tiered selection LLM,
// which can semantically match them to available tools.
func buildEnhancedRequestForRetry(originalRequest string, hallCtx *HallucinationContext) string {
	if hallCtx == nil {
		return originalRequest
	}

	// Build descriptive hint from actual hallucination context
	// NO hard-coded domain knowledge - just describe what the LLM was trying to do
	var hintParts []string

	if hallCtx.Instruction != "" {
		hintParts = append(hintParts, fmt.Sprintf("perform: %s", hallCtx.Instruction))
	}
	if hallCtx.AgentName != "" {
		hintParts = append(hintParts, fmt.Sprintf("agent type: %s", hallCtx.AgentName))
	}
	if hallCtx.Capability != "" {
		hintParts = append(hintParts, fmt.Sprintf("capability: %s", hallCtx.Capability))
	}

	if len(hintParts) == 0 {
		return originalRequest
	}

	return fmt.Sprintf(`%s

[CAPABILITY_HINT: The request requires a tool that can %s.
The planning LLM attempted to use a non-existent tool. Please ensure any tools
with similar capabilities are included in the selection.]`,
		originalRequest,
		strings.Join(hintParts, "; "))
}

// validatePlanAgainstAllowedAgents checks if all agents in the plan were in the allowed list.
// Returns the hallucinated agent name and an error if validation fails.
// This catches LLM hallucinations where the model invents agents not provided in the prompt.
//
// Important: If an agent is not in the allowed list but EXISTS in the full catalog,
// this is considered a "tiered selection miss" (not a true hallucination). The tool exists
// but wasn't selected by tiered capability resolution. In this case, we add it to the
// allowed list and continue, logging a warning for observability.
//
// Pattern 3 (Tracing): Accepts ctx for trace correlation - logs will include trace_id/span_id.
func (o *AIOrchestrator) validatePlanAgainstAllowedAgents(ctx context.Context, plan *RoutingPlan, allowedAgents map[string]bool) (string, error) {
	if plan == nil {
		return "", fmt.Errorf("plan is nil")
	}

	// Pattern 3 (Logging): Retrieve request_id from baggage for inclusion in all logs
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	// If no allowed agents were extracted (empty prompt or parsing issue), skip validation
	// This provides graceful degradation - better to let validatePlan() catch issues later
	if len(allowedAgents) == 0 {
		if o.logger != nil {
			o.logger.DebugWithContext(ctx, "Skipping hallucination validation - no allowed agents extracted", map[string]interface{}{
				"operation":  "hallucination_validation",
				"request_id": requestID,
				"reason":     "empty_allowed_agents",
			})
		}
		return "", nil
	}

	for _, step := range plan.Steps {
		// Normalize agent name to lowercase for case-insensitive comparison
		// This ensures "Weather-Tool-V2" matches "weather-tool-v2"
		normalizedAgentName := strings.ToLower(step.AgentName)
		if !allowedAgents[normalizedAgentName] {
			// Check if the agent exists in the full catalog before flagging as hallucination.
			// This handles the case where tiered selection missed a tool that the LLM
			// correctly identified as needed for the task.
			if o.catalog != nil {
				// Get all agents from the catalog and check if this agent exists
				agents := o.catalog.GetAgents()

				// Diagnostic logging: what agents are in the catalog?
				if o.logger != nil {
					catalogAgentNames := make([]string, 0, len(agents))
					for _, agentInfo := range agents {
						if agentInfo.Registration != nil {
							catalogAgentNames = append(catalogAgentNames, agentInfo.Registration.Name)
						}
					}
					o.logger.DebugWithContext(ctx, "Checking catalog for agent", map[string]interface{}{
						"operation":           "hallucination_validation",
						"request_id":          requestID,
						"agent_in_plan":       step.AgentName,
						"normalized_name":     normalizedAgentName,
						"catalog_agent_count": len(agents),
						"catalog_agents":      catalogAgentNames,
						"allowed_agents_keys": getAllowedAgentKeys(allowedAgents),
					})
				}

				foundInCatalog := false
				for _, agentInfo := range agents {
					// Case-insensitive comparison for catalog lookup
					if agentInfo.Registration != nil && strings.EqualFold(agentInfo.Registration.Name, step.AgentName) {
						foundInCatalog = true
						// Agent exists in catalog but wasn't selected by tiered resolution.
						// This is a "tiered selection miss", not a true hallucination.
						// Add it to allowed agents and log a warning.
						allowedAgents[normalizedAgentName] = true

						if o.logger != nil {
							o.logger.WarnWithContext(ctx, "Tiered selection missed a valid tool", map[string]interface{}{
								"operation":          "hallucination_validation",
								"request_id":         requestID,
								"agent_name":         step.AgentName,
								"catalog_agent_name": agentInfo.Registration.Name,
								"reason":             "tiered_selection_miss",
								"action":             "added_to_allowed_agents",
								"hint":               "Consider adjusting tiered selection prompt or threshold",
							})
						}

						// Record metric for observability
						telemetry.Counter("plan_generation.tiered_selection_miss",
							"module", telemetry.ModuleOrchestration,
							"agent", step.AgentName,
						)

						// Continue validation - this agent is now allowed
						break
					}
				}

				// If still not in allowed list after catalog check, it's a true hallucination
				if !foundInCatalog {
					if o.logger != nil {
						o.logger.ErrorWithContext(ctx, "Agent not found in catalog - flagging as hallucination", map[string]interface{}{
							"operation":           "hallucination_validation",
							"request_id":          requestID,
							"agent_in_plan":       step.AgentName,
							"normalized_name":     normalizedAgentName,
							"catalog_agent_count": len(agents),
							"reason":              "not_in_catalog",
						})
					}
					return step.AgentName, fmt.Errorf("LLM hallucinated agent '%s' which was not in the allowed list provided in the prompt", step.AgentName)
				}
			} else {
				// No catalog available - can't verify, treat as hallucination
				if o.logger != nil {
					o.logger.ErrorWithContext(ctx, "No catalog available for hallucination validation fallback", map[string]interface{}{
						"operation":     "hallucination_validation",
						"request_id":    requestID,
						"agent_in_plan": step.AgentName,
						"reason":        "no_catalog",
					})
				}
				return step.AgentName, fmt.Errorf("LLM hallucinated agent '%s' which was not in the allowed list provided in the prompt", step.AgentName)
			}
		}
	}
	return "", nil
}

// getAllowedAgentKeys returns the keys from the allowedAgents map for diagnostic logging
func getAllowedAgentKeys(allowedAgents map[string]bool) []string {
	keys := make([]string, 0, len(allowedAgents))
	for k := range allowedAgents {
		keys = append(keys, k)
	}
	return keys
}

// AIOrchestrator is an AI-powered orchestrator that uses LLM for intelligent routing
type AIOrchestrator struct {
	config *OrchestratorConfig
	// constructionErr is populated only by the source-compatible simple
	// constructor when its error-returning counterpart fails. Every public
	// execution entry point checks it before doing work.
	constructionErr error
	discovery       core.Discovery
	aiClient        core.AIClient
	catalog         *AgentCatalog
	executor        *SmartExecutor
	synthesizer     *AISynthesizer

	// Capability provider for flexible capability discovery
	capabilityProvider CapabilityProvider

	// Prompt builder for extensible prompt customization (Layer 1-3)
	// If nil, uses the hardcoded default prompt for backwards compatibility
	promptBuilder PromptBuilder

	// Result processing for large data management (Layer 2+)
	resultProcessor  ResultProcessor
	resultTrimConfig *ResultTrimConfig

	// continuationDistiller is the Phase-14 continuation-scoped distiller (C). It distills a non-JSON
	// step blob into a relevant summary for the planner. Kept SEPARATE from resultProcessor (the
	// synthesis chain) so the planning path is not coupled to the synthesis processor (Phase 8). Nil
	// when no AI client is available — C is then a no-op (the structural-floor body stands).
	continuationDistiller ResultProcessor

	// LLM Debug Store for full payload visibility
	// When enabled, stores complete prompts/responses for debugging
	debugStore LLMDebugStore
	// debugWg tracks in-flight debug recording goroutines for graceful shutdown
	debugWg sync.WaitGroup
	// debugSeqID provides fallback correlation IDs when TraceID is not available
	debugSeqID atomic.Uint64
	// precedenceEntityExtractor is an optional domain-specific extractor
	// used by the central recordDebugInteraction path to populate
	// LLMInteraction.PrecedenceAudit with entity lists and a compliance
	// signal. Nil by default (framework stays domain-agnostic); wired via
	// SetPrecedenceEntityExtractor.
	precedenceEntityExtractor PrecedenceEntityExtractor

	// Execution Store for DAG visualization
	// When enabled, stores plan + execution results for debugging
	executionStore ExecutionStore
	// executionWg tracks in-flight execution storage goroutines for graceful shutdown
	executionWg sync.WaitGroup
	// executionRecorders serialize writes within a request while allowing
	// independent requests to persist concurrently.
	executionRecorders sync.Map // map[string]*executionRecorder
	recordingCtx       context.Context
	recordingCancel    context.CancelFunc

	// Observability (follows framework design principles)
	telemetry core.Telemetry // For metrics and tracing
	logger    core.Logger    // For structured logging

	// Metrics and history
	metrics      *OrchestratorMetrics
	history      []ExecutionRecord
	historyMutex sync.RWMutex
	metricsMutex sync.RWMutex

	// Context for background operations
	ctx    context.Context
	cancel context.CancelFunc

	// HITL (Human-in-the-Loop) support
	// When set, enables human oversight at plan/step execution points
	interruptController InterruptController

	// Pipeline hooks for context engineering (BeforePlanning, AfterPlanning, etc.)
	pipelineHooks []core.PipelineHook

	// Shared conversation-history preparation for metadata/hook ingress paths.
	conversationHistoryPreparer ConversationHistoryPreparer

	// Activity coordination for real-time agent signals (Phase 8)
	activityCoordinator core.ActivityCoordinator // may be nil

	// skillRuntime is present only when skills are enabled with at least one
	// effective binding. It observes provider-neutral registry/cache contracts.
	skillRuntime *skillRuntime
}

// NewAIOrchestrator creates a new AI-powered orchestrator
func NewAIOrchestrator(config *OrchestratorConfig, discovery core.Discovery, aiClient core.AIClient) *AIOrchestrator {
	// Immediate startup marker - uses stderr for guaranteed output
	log.Printf("[TRUVAG3-ORCH-V2] NewAIOrchestrator starting - EnableHybridResolution=%v", config != nil && config.EnableHybridResolution)

	if config == nil {
		config = DefaultConfig()
	}
	applyLegacyAIOptionFields(config)

	// Wire iterative planning config into PromptConfig for budget-aware prompts
	if config.IterativePlanning.Enabled {
		config.PromptConfig.IterativePlanConfig = &config.IterativePlanning
	}

	ctx, cancel := context.WithCancel(context.Background())
	recordingCtx, recordingCancel := context.WithCancel(context.Background())
	catalog := NewAgentCatalog(discovery)

	// Apply excluded capabilities (prevents self-referential orchestration)
	if len(config.ExcludedCapabilities) > 0 {
		catalog.SetExcludedCapabilities(config.ExcludedCapabilities)
	}

	o := &AIOrchestrator{
		config:    config,
		discovery: discovery,
		aiClient:  aiClient,
		catalog:   catalog,
		executor: newSmartExecutor(
			catalog,
			config.ExecutionOptions.MaxConcurrency,
			config.ExecutionOptions.TotalTimeout,
			config.ExecutionOptions.RetryAttempts,
			config.stepRetryBackoff,
		),
		synthesizer:     NewAISynthesizer(aiClient),
		metrics:         &OrchestratorMetrics{},
		history:         make([]ExecutionRecord, 0, config.HistorySize),
		ctx:             ctx,
		cancel:          cancel,
		recordingCtx:    recordingCtx,
		recordingCancel: recordingCancel,
		// Default to no-op telemetry
		telemetry: &core.NoOpTelemetry{},
	}

	// Propagate result trim config and synthesis parameters to synthesizer
	o.synthesizer.SetResultTrimConfig(&config.ResultTrim)
	o.synthesizer.SetAIOptionsOverride(config.SynthesisAIOptions)

	// Initialize capability provider based on configuration
	switch config.CapabilityProviderType {
	case "service":
		// Use service-based provider for large-scale deployments
		if config.EnableFallback {
			// Use default provider as fallback for graceful degradation
			config.CapabilityService.FallbackProvider = NewDefaultCapabilityProvider(catalog)
		}
		o.capabilityProvider = NewServiceCapabilityProvider(&config.CapabilityService)
	default:
		// Default to catalog-based provider (sends all capabilities to LLM)
		// This is the quick-start default that works without additional setup
		o.capabilityProvider = NewDefaultCapabilityProvider(catalog)
	}

	// Layer 3: Wire up validation feedback if enabled
	if config.ExecutionOptions.ValidationFeedbackEnabled {
		o.executor.SetCorrectionCallback(o.requestParameterCorrection)
		o.executor.SetValidationFeedback(true, config.ExecutionOptions.MaxValidationRetries)
	}

	// Configure hybrid parameter resolution if enabled
	// This uses auto-wiring (schema-based) + micro-resolution (LLM fallback) for parameter binding
	if config.EnableHybridResolution {
		hybridResolver := NewHybridResolver(aiClient, nil) // Logger will be set later via SetLogger
		hybridResolver.SetMicroResolverAIOptions(config.MicroResolutionAIOptions)
		o.executor.SetHybridResolver(hybridResolver)
		o.executor.EnableHybridResolution(true)
		// Debug log to confirm hybrid resolution was configured
		fmt.Printf("[ORCHESTRATOR] Hybrid resolution enabled: hybridResolver=%v, useHybridResolution=true\n", hybridResolver != nil)
	} else {
		fmt.Printf("[ORCHESTRATOR] Hybrid resolution DISABLED in config (EnableHybridResolution=%v)\n", config.EnableHybridResolution)
	}

	// Layer 4: Wire up Semantic Retry (Contextual Re-Resolution) if enabled
	// This enables the executor to compute derived values when ErrorAnalyzer says "cannot fix"
	// but source data from dependencies is available. See SEMANTIC_RETRY_DESIGN.md for details.
	if config.SemanticRetry.Enabled {
		reResolver := NewContextualReResolver(aiClient, nil) // Logger will be set later via SetLogger
		reResolver.SetAIOptionsOverride(config.MicroResolutionAIOptions)
		o.executor.SetContextualReResolver(reResolver)
		o.executor.SetMaxSemanticRetries(config.SemanticRetry.MaxAttempts)
		o.executor.SetSemanticRetryForIndependentSteps(config.SemanticRetry.EnableForIndependentSteps)
	}

	// Wire step completion callback for async progress reporting (v1 addition)
	// This enables async task handlers to receive per-tool progress updates.
	if config.ExecutionOptions.OnStepComplete != nil {
		o.executor.SetOnStepComplete(config.ExecutionOptions.OnStepComplete)
	}

	// Wire OAuth token from config to executor
	if config.OAuthToken != "" {
		o.executor.SetOAuthToken(config.OAuthToken)
	}

	// Wire propagated headers from config to executor
	if len(config.PropagatedHeaders) > 0 {
		o.executor.SetPropagatedHeaders(config.PropagatedHeaders)
	}

	// Wire execution options from config to executor
	if config.ExecutionOptions.TotalTimeout > 0 {
		o.executor.SetHTTPTimeout(config.ExecutionOptions.TotalTimeout)
	}
	if config.ExecutionOptions.MaxConcurrency > 0 {
		o.executor.SetMaxConcurrency(config.ExecutionOptions.MaxConcurrency)
	}
	if config.ExecutionOptions.RetryAttempts > 0 {
		o.executor.SetMaxAttempts(config.ExecutionOptions.RetryAttempts)
		if o.logger != nil {
			o.logger.Info("Executor retry attempts configured from config", map[string]interface{}{
				"operation":      "executor_configuration",
				"retry_attempts": config.ExecutionOptions.RetryAttempts,
			})
		}
	}

	// Wire extended timeout for orchestrator capabilities (agent-as-tool delegation).
	// When a parent orchestrator calls a child orchestrator via /query, the child may
	// block on HITL approval. The per-request timeout must accommodate:
	//   - HITL wait time (config.HITL.DefaultTimeout, e.g., 15 min)
	//   - Execution time after resume (config.ExecutionOptions.StepTimeout, e.g., 5 min)
	// Without this, the parent's HTTP client kills the connection before the human responds.
	if config.HITL.DefaultTimeout > 0 || config.ExecutionOptions.StepTimeout > 0 {
		orchTimeout := config.HITL.DefaultTimeout + config.ExecutionOptions.StepTimeout
		if orchTimeout > 0 {
			o.executor.SetOrchestratorStepTimeout(orchTimeout)
			// The HTTP client timeout must be >= orchestrator step timeout,
			// otherwise the transport kills the connection before the context deadline.
			if orchTimeout > config.ExecutionOptions.TotalTimeout {
				o.executor.SetHTTPTimeout(orchTimeout)
			}
			if o.logger != nil {
				o.logger.Info("Orchestrator step timeout configured for delegation HITL", map[string]interface{}{
					"operation":                 "executor_configuration",
					"orchestrator_step_timeout": orchTimeout.String(),
					"hitl_default_timeout":      config.HITL.DefaultTimeout.String(),
					"step_timeout":              config.ExecutionOptions.StepTimeout.String(),
				})
			}
		}
	}

	// Post-orchestrator plan refinement (ORCH-015).
	// Independent of hybrid resolution — works with any parameter resolution strategy.
	// Opt-in for initial rollout. Environment is resolved into typed config at
	// construction; request execution never reads process-global state.
	if config.PlanRefinementEnabled {
		planRefiner := NewPlanRefiner(aiClient, o.logger)
		if planRefiner != nil {
			planRefiner.SetAIOptionsOverride(config.MicroResolutionAIOptions)
			if o.telemetry != nil {
				planRefiner.SetTelemetry(o.telemetry)
			}
			if o.debugStore != nil {
				planRefiner.SetDebugStore(o.debugStore)
			}
			o.executor.SetPlanRefiner(planRefiner)
		}
	}

	return o
}

// Start initializes the orchestrator and starts background processes
func (o *AIOrchestrator) Start(ctx context.Context) error {
	if err := o.rejectIfConstructionFailed(ctx, "start"); err != nil {
		return err
	}
	// Initial catalog refresh
	if err := o.catalog.Refresh(ctx); err != nil {
		return fmt.Errorf("failed to initialize catalog: %w", err)
	}

	// Start background catalog refresh
	go o.catalogRefreshLoop()

	return nil
}

func (o *AIOrchestrator) rejectIfConstructionFailed(ctx context.Context, operation string) error {
	if o == nil || o.constructionErr == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestID := requestIDFromBaggage(ctx)
	telemetry.Counter("orchestration.construction.rejected",
		"module", telemetry.ModuleOrchestration,
		"operation", operation,
	)
	telemetry.AddSpanEvent(ctx, "orchestrator.construction.rejected",
		attribute.String("request_id", requestID),
		attribute.String("operation", operation),
		attribute.String("reason", "invalid_configuration"),
	)
	telemetry.RecordSpanError(ctx, ErrOrchestratorConstruction)
	if o.logger != nil {
		o.logger.ErrorWithContext(ctx, "Orchestrator operation rejected after construction failure", map[string]interface{}{
			"operation":           "orchestrator_construction_rejection",
			"requested_operation": operation,
			"request_id":          requestID,
			"status":              "rejected",
			"error_type":          "preparation",
			"error":               "orchestrator construction failed",
		})
	}
	return o.constructionErr
}

// Stop gracefully shuts down the orchestrator
func (o *AIOrchestrator) Stop() {
	o.cancel()
}

// SetCapabilityProvider sets a custom capability provider
func (o *AIOrchestrator) SetCapabilityProvider(provider CapabilityProvider) {
	o.capabilityProvider = provider
}

// SetTelemetry sets the telemetry provider (integrates with framework telemetry module)
func (o *AIOrchestrator) SetTelemetry(telemetry core.Telemetry) {
	if telemetry == nil {
		o.telemetry = &core.NoOpTelemetry{}
	} else {
		o.telemetry = telemetry
	}
	if o.skillRuntime != nil {
		o.skillRuntime.telemetry = o.telemetry
	}
	// Propagate telemetry to hybrid resolver (micro-resolution spans)
	if o.executor != nil && o.executor.hybridResolver != nil {
		o.executor.hybridResolver.SetTelemetry(o.telemetry)
	}
	if telemetryAware, ok := o.conversationHistoryPreparer.(interface{ SetTelemetry(core.Telemetry) }); ok {
		telemetryAware.SetTelemetry(o.telemetry)
	}

	// Propagate telemetry to pipeline hooks that create spans (e.g., EventSummarizer)
	for _, hook := range o.pipelineHooks {
		if telemetryAware, ok := hook.(interface{ SetTelemetry(core.Telemetry) }); ok {
			telemetryAware.SetTelemetry(o.telemetry)
		}
	}
}

// SetLogger sets the logger provider (follows framework design principles)
// The component is always set to "framework/orchestration" to ensure proper log attribution
// regardless of which agent or tool is using the orchestration module.
func (o *AIOrchestrator) SetLogger(logger core.Logger) {
	if logger == nil {
		o.logger = &core.NoOpLogger{}
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			o.logger = cal.WithComponent("framework/orchestration")
		} else {
			o.logger = logger
		}
	}
	if o.skillRuntime != nil {
		o.skillRuntime.logger = o.logger
	}

	// Propagate logger to sub-components (they will apply their own WithComponent)
	if o.executor != nil {
		o.executor.SetLogger(logger)
	}
	if o.catalog != nil {
		o.catalog.SetLogger(logger)
	}
	if o.synthesizer != nil {
		o.synthesizer.SetLogger(logger)
	}
	if loggerAware, ok := o.conversationHistoryPreparer.(interface{ SetLogger(core.Logger) }); ok {
		loggerAware.SetLogger(logger)
	}
	for _, hook := range o.pipelineHooks {
		if loggerAware, ok := hook.(interface{ SetLogger(core.Logger) }); ok {
			loggerAware.SetLogger(logger)
		}
	}
}

// SetPrecedenceEntityExtractor wires a domain-specific entity extractor
// that populates LLMInteraction.PrecedenceAudit entity fields and the
// derived compliance label. Pass nil to disable entity extraction (the
// cheap structural audit fields remain populated either way).
//
// Called on startup; not safe to change concurrently with in-flight
// requests. Propagates to the synthesizer when one is configured so
// synthesis interactions get the same audit treatment.
func (o *AIOrchestrator) SetPrecedenceEntityExtractor(extractor PrecedenceEntityExtractor) {
	o.precedenceEntityExtractor = extractor
	if o.synthesizer != nil {
		o.synthesizer.SetPrecedenceEntityExtractor(extractor)
	}
}

// SetPromptBuilder allows runtime injection of a custom prompt builder.
// This follows the existing pattern used by SetCapabilityProvider.
//
// Use cases:
//   - Layer 1: DefaultPromptBuilder with additional type rules
//   - Layer 2: TemplatePromptBuilder for structural customization
//   - Layer 3: Custom PromptBuilder for full control (compliance, audit logging)
func (o *AIOrchestrator) SetPromptBuilder(builder PromptBuilder) {
	if builder != nil {
		o.promptBuilder = builder
		if o.logger != nil {
			o.logger.Info("PromptBuilder updated at runtime", map[string]interface{}{
				"operation": "set_prompt_builder",
			})
		}
	}
}

// SetErrorAnalyzer configures the LLM-based error analyzer for the executor.
// When set, the executor uses LLM to analyze errors and determine if they can be
// fixed with different parameters. This removes the need for tools to set Retryable flags.
// See PARAMETER_BINDING_FIX.md for the complete design rationale.
func (o *AIOrchestrator) SetErrorAnalyzer(analyzer *ErrorAnalyzer) {
	if o.executor != nil && analyzer != nil {
		o.executor.SetErrorAnalyzer(analyzer)
		if o.logger != nil {
			o.logger.Info("Error analyzer configured", map[string]interface{}{
				"operation": "set_error_analyzer",
			})
		}
	}
}

// SetResultProcessor sets the processor that trims step results for the SYNTHESIS prompt
// (non-streaming synthesizer + streaming orchestrator). Resolution-source trimming and
// agent-input transformation are configured independently via SetSourceResultProcessor and
// SetAgentInputProcessor — three seams, three plugs (Phase 8). This keeps a (possibly distilling)
// synthesis processor from leaking into the deterministic input/resolution paths.
func (o *AIOrchestrator) SetResultProcessor(processor ResultProcessor) {
	o.resultProcessor = processor
	o.resultTrimConfig = &o.config.ResultTrim
	if o.synthesizer != nil {
		o.synthesizer.SetResultProcessor(processor)
		o.synthesizer.SetResultTrimConfig(&o.config.ResultTrim)
	}
	if o.logger != nil {
		o.logger.Info("Synthesis ResultProcessor configured", map[string]interface{}{
			"operation": "set_result_processor",
			"enabled":   o.config.ResultTrim.Enabled,
		})
	}
}

// SetContinuationDistiller sets the Phase-14 continuation-scoped distiller (C): used to summarize a
// non-JSON step blob for the planner. Deliberately separate from the synthesis ResultProcessor so the
// planning path is not coupled to the synthesis chain (Phase 8). A nil processor disables C.
func (o *AIOrchestrator) SetContinuationDistiller(processor ResultProcessor) {
	o.continuationDistiller = processor
}

// SetSourceResultProcessor sets the DETERMINISTIC processor used to trim resolver source data for
// micro-resolution (Phase 5) and contextual re-resolution (semantic retry) prompts. Those prompts
// already drive their own resolution LLM call, so this processor must never distill via an LLM.
// Configured independently of the synthesis processor — no cliff (Phase 8).
func (o *AIOrchestrator) SetSourceResultProcessor(processor ResultProcessor) {
	if o.executor == nil {
		return
	}
	// Micro-resolution source data trimming (Phase 5)
	if o.executor.hybridResolver != nil {
		o.executor.hybridResolver.SetMicroResolverResultProcessor(processor, o.config.ResultTrim.MaxMicroResolutionBytes)
		// Schema-guided mapping threshold (Phase 10)
		if o.config.ResultTrim.SchemaGuidedMappingThreshold > 0 {
			o.executor.hybridResolver.SetMicroResolverSchemaMappingThreshold(o.config.ResultTrim.SchemaGuidedMappingThreshold)
		}
	}
	// Semantic-retry source data trimming
	if o.executor.contextualReResolver != nil {
		o.executor.contextualReResolver.SetResultProcessor(processor, o.config.ResultTrim.MaxMicroResolutionBytes)
	}
	if o.logger != nil {
		o.logger.Info("Source ResultProcessor configured", map[string]interface{}{
			"operation":                "set_source_result_processor",
			"max_micro_resolution":     o.config.ResultTrim.MaxMicroResolutionBytes,
			"schema_mapping_threshold": o.config.ResultTrim.SchemaGuidedMappingThreshold,
		})
	}
}

// SetAgentInputProcessor sets the transform applied to tool/agent INPUT parameters before dispatch
// (executor). This is a data-flow seam, not a prompt trim — see AgentInputProcessor. Configured
// independently of the synthesis and resolution-source processors (Phase 8).
func (o *AIOrchestrator) SetAgentInputProcessor(processor AgentInputProcessor) {
	if o.executor == nil {
		return
	}
	o.executor.SetAgentInputProcessor(processor)
	if o.logger != nil {
		o.logger.Info("AgentInputProcessor configured", map[string]interface{}{
			"operation":       "set_agent_input_processor",
			"max_agent_input": o.config.ResultTrim.MaxAgentInputBytes,
		})
	}
}

// SetLLMDebugStore sets the LLM debug store for full payload visibility.
// When configured, complete LLM prompts and responses are stored for debugging.
// This enables operators to see exactly what was sent to and received from the LLM.
// The store is propagated to all sub-components that make LLM calls:
// synthesizer, micro_resolver, and contextual_re_resolver.
func (o *AIOrchestrator) SetLLMDebugStore(store LLMDebugStore) {
	if store == nil {
		return
	}

	o.debugStore = store

	// Propagate to synthesizer
	if o.synthesizer != nil {
		o.synthesizer.SetLLMDebugStore(store)
	}

	// Propagate to executor's sub-components
	if o.executor != nil {
		// Propagate to HybridResolver's MicroResolver
		if o.executor.hybridResolver != nil && o.executor.hybridResolver.microResolver != nil {
			o.executor.hybridResolver.microResolver.SetLLMDebugStore(store)
		}
		// Propagate to ContextualReResolver
		if o.executor.contextualReResolver != nil {
			o.executor.contextualReResolver.SetLLMDebugStore(store)
		}
		// Propagate to ErrorAnalyzer
		if o.executor.errorAnalyzer != nil {
			o.executor.errorAnalyzer.SetLLMDebugStore(store)
		}
	}

	// Propagate to the result processor when it (or a wrapper, e.g. the distillation
	// cache) accepts a debug store. Interface assertion so the cache wrapper forwards.
	if debuggable, ok := o.resultProcessor.(interface{ SetLLMDebugStore(LLMDebugStore) }); ok {
		debuggable.SetLLMDebugStore(store)
	}

	if debuggable, ok := o.conversationHistoryPreparer.(interface{ SetLLMDebugStore(LLMDebugStore) }); ok {
		debuggable.SetLLMDebugStore(store)
	}

	// Propagate to pipeline hooks that make LLM calls (e.g., EventSummarizer in MemoryRecordHook)
	for _, hook := range o.pipelineHooks {
		if debuggable, ok := hook.(interface{ SetLLMDebugStore(LLMDebugStore) }); ok {
			debuggable.SetLLMDebugStore(store)
		}
	}

	if o.logger != nil {
		o.logger.Info("LLM debug store configured", map[string]interface{}{
			"operation": "set_llm_debug_store",
		})
	}
}

// SetConversationHistoryPreparer configures the shared conversation-history preparation path.
func (o *AIOrchestrator) SetConversationHistoryPreparer(preparer ConversationHistoryPreparer) {
	o.conversationHistoryPreparer = preparer
	if preparer == nil {
		return
	}
	if loggerAware, ok := preparer.(interface{ SetLogger(core.Logger) }); ok {
		loggerAware.SetLogger(o.logger)
	}
	if telemetryAware, ok := preparer.(interface{ SetTelemetry(core.Telemetry) }); ok {
		telemetryAware.SetTelemetry(o.telemetry)
	}
	if debuggable, ok := preparer.(interface{ SetLLMDebugStore(LLMDebugStore) }); ok && o.debugStore != nil {
		debuggable.SetLLMDebugStore(o.debugStore)
	}
}

// GetLLMDebugStore returns the configured LLM debug store (for API handlers).
func (o *AIOrchestrator) GetLLMDebugStore() LLMDebugStore {
	return o.debugStore
}

// SetExecutionStore sets the execution store for DAG visualization.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md, nil values are safely ignored.
func (o *AIOrchestrator) SetExecutionStore(store ExecutionStore) {
	if store == nil {
		return // Safe default: ignore nil
	}
	o.executionStore = store

	if o.logger != nil {
		o.logger.Info("Execution store configured", map[string]interface{}{
			"operation": "set_execution_store",
		})
	}
}

// GetExecutionStore returns the configured execution store (for API handlers).
func (o *AIOrchestrator) GetExecutionStore() ExecutionStore {
	return o.executionStore
}

// getAgentName returns the agent name for DAG visualization.
// Priority: config.Name > config.RequestIDPrefix > "orchestrator"
// This is used when storing executions to identify the orchestrator agent.
func (o *AIOrchestrator) getAgentName() string {
	if o.config == nil {
		return "orchestrator"
	}
	if o.config.Name != "" {
		return o.config.Name
	}
	if o.config.RequestIDPrefix != "" {
		return o.config.RequestIDPrefix
	}
	return "orchestrator"
}

// buildNonSuccessResult constructs an ExecutionResult for the four non-success
// storeExecutionAsync call sites (HITL interrupt plan-level, HITL interrupt
// step-level, phase execution error, inter-phase intermediate store). It packs
// multi-phase metadata so the stored record's PhasePlans / PhaseCount round-trip
// through storeExecutionAsync, and merges cross-phase step results so the DAG
// visualization shows complete status.
//
// currentPhaseSteps carries step results that belong to the phase in flight but
// are NOT in allStepsList yet (the accumulator at ~line 2311 has not run for
// them). The caller is responsible for sourcing these per site:
//   - plan-level HITL:        nil — no execution in this phase yet
//   - step-level HITL:        extractCurrentPhaseFromCheckpoint(checkpoint, allStepResults)
//   - phase execution error:  phaseResult.Steps (nil-safe)
//   - intermediate store:     nil — allStepsList already contains the just-completed phase
//
// phasePlans is shallow-copied (slice-header) to close the slice-header race
// with the phase loop's continued appends. Plan pointers are shared — see
// BUG_HITL_INTERRUPTED_EXECUTION_MISSING_PRIOR_PHASE_STEPS.md "Known narrow
// race window" for the remediation-override mutation on plan.Terminal and
// the follow-up mitigation options.
func buildNonSuccessResult(
	currentPhaseSteps []StepResult,
	phasePlans []*RoutingPlan,
	phaseCount int,
	forcedTerminal bool,
	allStepsList []StepResult,
	planID string,
	success bool,
) *ExecutionResult {
	// allStepsList is already ordered and each step already carries
	// Metadata["phase_number"] stamped by the accumulator.
	merged := make([]StepResult, 0, len(allStepsList)+len(currentPhaseSteps))
	merged = append(merged, allStepsList...)

	// currentPhaseSteps have NOT been annotated with phase_number — the
	// accumulator never ran for them (they come from a checkpoint or an
	// interrupt-path result). Stamp uniformly so the DAG phase-grouping UI
	// has data for every node.
	//
	// Important: callers may share Metadata map references with their upstream
	// (e.g. extractCurrentPhaseFromCheckpoint returns *sr derefs whose Metadata
	// pointer is still the checkpoint's; phaseResult.Steps entries share maps
	// with the executor's internal state). Writing to step.Metadata directly
	// would mutate the caller's map. To keep the helper side-effect-free,
	// allocate a fresh Metadata map whenever we need to add phase_number.
	for i := range currentPhaseSteps {
		step := currentPhaseSteps[i] // struct value-copy; Metadata pointer still shared
		if _, hasPhase := step.Metadata["phase_number"]; !hasPhase {
			// Allocate a fresh map that owns the phase_number annotation,
			// leaving the caller's map untouched.
			newMeta := make(map[string]interface{}, len(step.Metadata)+1)
			for k, v := range step.Metadata {
				newMeta[k] = v
			}
			newMeta["phase_number"] = phaseCount
			step.Metadata = newMeta
		}
		merged = append(merged, step)
	}

	// Shallow copy of phasePlans — closes the slice-header race.
	phasePlansCopy := make([]*RoutingPlan, len(phasePlans))
	copy(phasePlansCopy, phasePlans)

	return &ExecutionResult{
		PlanID:     planID,
		Steps:      merged,
		Success:    success,
		PhaseCount: phaseCount,
		Metadata: map[string]interface{}{
			MetadataKeyPhasePlans:     phasePlansCopy,
			MetadataKeyPhaseCount:     phaseCount,
			MetadataKeyForcedTerminal: forcedTerminal,
		},
	}
}

// extractCurrentPhaseFromCheckpoint returns step results from
// checkpoint.StepResults whose step IDs are NOT in priorPhase. Used at the
// step-level HITL interrupt site where the executor returns
// (nil, ErrInterrupted) but the checkpoint carries the successful siblings
// collected by the executor before returning.
//
// Output is sorted by StartTime so Result.Steps preserves the "ordered by
// execution" contract. StepResult.StartTime is time.Time (value type, not
// pointer) per interfaces.go — no nil-check required.
func extractCurrentPhaseFromCheckpoint(checkpoint *ExecutionCheckpoint, priorPhase map[string]*StepResult) []StepResult {
	if checkpoint == nil || len(checkpoint.StepResults) == 0 {
		return nil
	}
	out := make([]StepResult, 0)
	for stepID, sr := range checkpoint.StepResults {
		if sr == nil {
			continue
		}
		if _, inPrior := priorPhase[stepID]; inPrior {
			continue
		}
		out = append(out, *sr)
	}
	// Map iteration is unordered — sort by (StartTime, StepID) for a stable
	// execution-order slice. StepID is the tiebreaker when StartTimes are
	// equal or zero (sort.Slice is not stable).
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartTime.Equal(out[j].StartTime) {
			return out[i].StartTime.Before(out[j].StartTime)
		}
		return out[i].StepID < out[j].StepID
	})
	return out
}

// rebuildCheckpointCompletedSteps mirrors the (already-enriched)
// checkpoint.StepResults map into the checkpoint.CompletedSteps slice,
// including successful results from all phases — not just the current batch.
// Must be called AFTER StepResults has been enriched with prior-phase
// results. Ordered by (StartTime, StepID) for stable UI rendering.
//
// Semantics: this is a REPLACE, not a merge. Any existing CompletedSteps
// content is discarded when StepResults is non-empty. This matches the
// intended call pattern — call once per enrichment, after all prior-phase
// folds into StepResults have completed. If StepResults is nil or empty,
// CompletedSteps is left untouched (preserves executor-populated content
// for the phaseCount == 1 path where orchestrator enrichment never ran).
func rebuildCheckpointCompletedSteps(checkpoint *ExecutionCheckpoint) {
	if checkpoint == nil || len(checkpoint.StepResults) == 0 {
		return
	}
	completed := make([]StepResult, 0, len(checkpoint.StepResults))
	for _, sr := range checkpoint.StepResults {
		if sr != nil && sr.Success {
			completed = append(completed, *sr)
		}
	}
	// StepResult.StartTime is time.Time (value) — no nil-check. StepID
	// tiebreaker because sort.Slice is not stable and StartTimes may be
	// equal or zero in edge cases.
	sort.Slice(completed, func(i, j int) bool {
		if !completed[i].StartTime.Equal(completed[j].StartTime) {
			return completed[i].StartTime.Before(completed[j].StartTime)
		}
		return completed[i].StepID < completed[j].StepID
	})
	checkpoint.CompletedSteps = completed
}

// storeExecutionAsync stores execution data asynchronously for DAG visualization.
// This helper is used by both success and non-success paths.
//
// Contract (post ORCH-022):
//   - For executions that have reached model execution, result is non-nil.
//   - A typed lifecycle-boundary failure may store a nil result with its
//     request-local debug evidence before any model call occurs.
//   - checkpoint != nil signals HITL interruption.
//   - checkpoint == nil with result.Success == true signals successful completion
//     or intermediate inter-phase store.
//   - checkpoint == nil with result.Success == false signals a phase execution error.
//
// The canonical "is interrupted" signal on StoredExecution is Interrupted == true
// (set from checkpoint != nil below). Do NOT use Result == nil as a proxy.
//
// Runs asynchronously to avoid blocking orchestration. Errors are logged, not propagated.
// Uses WaitGroup to track in-flight recordings for graceful shutdown.
func (o *AIOrchestrator) storeExecutionAsync(
	ctx context.Context,
	request string,
	requestID string,
	plan *RoutingPlan,
	result *ExecutionResult,
	checkpoint *ExecutionCheckpoint,
) {
	o.storeExecutionSnapshotAsync(ctx, request, requestID, plan, result, checkpoint, nil, "")
}

func (o *AIOrchestrator) storeExecutionWithFinalResponseAsync(
	ctx context.Context,
	request string,
	requestID string,
	plan *RoutingPlan,
	result *ExecutionResult,
	finalResponse string,
) {
	responseCopy := finalResponse
	o.storeExecutionSnapshotAsync(
		ctx,
		request,
		requestID,
		plan,
		result,
		nil,
		&responseCopy,
		FinalResponseSourceAfterSynthesisHooks,
	)
}

func (o *AIOrchestrator) storeExecutionSnapshotAsync(
	ctx context.Context,
	request string,
	requestID string,
	plan *RoutingPlan,
	result *ExecutionResult,
	checkpoint *ExecutionCheckpoint,
	finalResponse *string,
	finalResponseSource string,
) {
	if o.executionStore == nil {
		return
	}

	// Capture timestamp now, not when goroutine runs (avoids timing drift)
	createdAt := time.Now()

	// Extract baggage BEFORE spawning goroutine to preserve correlation data.
	// The parent context may be canceled after the HTTP handler returns,
	// but we still want the async recording to complete.
	bag := telemetry.GetBaggage(ctx)
	traceID := telemetry.GetTraceContext(ctx).TraceID
	conversationID := core.GetConversationID(ctx)
	if core.ValidateConversationID(conversationID) != core.ConversationIDValidationNone {
		conversationID = ""
	}
	originalRequestID := requestID
	if bag != nil {
		if origID := bag["original_request_id"]; origID != "" {
			originalRequestID = origID
		}
	}

	// Capture agentName now (accesses o.config which should be immutable)
	agentName := o.getAgentName()

	// Capture resume checkpoint ID before goroutine (ctx may be cancelled later)
	resumeCheckpointID, isResume := IsResumeMode(ctx)

	stored := &StoredExecution{
		RequestID:           requestID,
		OriginalRequestID:   originalRequestID,
		TraceID:             traceID,
		AgentName:           agentName,
		OriginalRequest:     request,
		Plan:                plan,
		Result:              result,
		Interrupted:         checkpoint != nil,
		Checkpoint:          checkpoint,
		CreatedAt:           createdAt,
		FinalResponse:       finalResponse,
		FinalResponseSource: finalResponseSource,
	}
	if holder, ok := skillExecutionHolderFromContext(ctx); ok {
		skillState, _ := holder.Snapshot()
		debug := cloneSkillExecutionState(skillState).Debug
		stored.Skills = &debug
	}

	if conversationID != "" {
		stored.Metadata = map[string]string{
			MetadataConversationID: conversationID,
		}
	}

	// Tag resumed executions so the registry viewer can link them to the interrupted one.
	if isResume && resumeCheckpointID != "" {
		if stored.Metadata == nil {
			stored.Metadata = make(map[string]string)
		}
		stored.Metadata["resume_checkpoint_id"] = resumeCheckpointID
	}

	// Extract multi-phase data from result metadata into typed StoredExecution fields
	if result != nil {
		if result.Metadata != nil {
			if plans, ok := result.Metadata[MetadataKeyPhasePlans].([]*RoutingPlan); ok {
				stored.PhasePlans = plans
			}
			if count, ok := result.Metadata[MetadataKeyPhaseCount].(int); ok {
				stored.PhaseCount = count
			}
			if forced, ok := result.Metadata[MetadataKeyForcedTerminal].(bool); ok {
				stored.ForcedTerminal = forced
			}
			// Serialize plan regeneration events into StoredExecution.Metadata (map[string]string)
			if regenEvts, ok := result.Metadata[MetadataKeyPlanRegenerations]; ok {
				if regenJSON, jsonErr := json.Marshal(regenEvts); jsonErr == nil {
					if stored.Metadata == nil {
						stored.Metadata = make(map[string]string)
					}
					stored.Metadata[MetadataKeyPlanRegenerations] = string(regenJSON)
				}
			}
		}
		// Fall back to the struct field when Metadata didn't carry PhaseCount.
		// Honours callers (tests, future paths) that set PhaseCount on the struct
		// directly without going through Metadata.
		if stored.PhaseCount == 0 && result.PhaseCount > 0 {
			stored.PhaseCount = result.PhaseCount
		}
	}

	checkpointID := ""
	if checkpoint != nil {
		checkpointID = checkpoint.CheckpointID
	}
	recorder := o.executionRecorderFor(requestID)
	recorder.Record(executionRecordSnapshot{
		Record:             stored,
		CorrelationContext: ctx,
		RequestID:          requestID,
		TraceID:            traceID,
		ConversationID:     conversationID,
		CheckpointID:       checkpointID,
		Interrupted:        checkpoint != nil,
		RetentionTTL: executionRetentionTTL(
			stored,
			o.config.ExecutionStore.TTL,
			o.config.ExecutionStore.ErrorTTL,
		),
	})
}

func (o *AIOrchestrator) executionRecorderFor(requestID string) *executionRecorder {
	created := newExecutionRecorder(
		o.recordingCtx, o.executionStore, o.debugStore, o.logger, &o.executionWg,
		o.config.executionStoreWriteTimeout,
	)
	actual, _ := o.executionRecorders.LoadOrStore(requestID, created)
	return actual.(*executionRecorder)
}

func (o *AIOrchestrator) releaseExecutionRecorder(requestID string) {
	o.executionRecorders.Delete(requestID)
}

func safeExecutionStoreError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "execution store write timed out"
	case errors.Is(err, context.Canceled):
		return "execution store write canceled"
	default:
		return "execution store backend write failed"
	}
}

// SetInterruptController sets the HITL interrupt controller.
// When set, enables human oversight at plan/step execution points.
// The controller is propagated to the executor for step-level checks.
func (o *AIOrchestrator) SetInterruptController(controller InterruptController) {
	if controller == nil {
		return
	}

	o.interruptController = controller

	// Propagate to executor for step-level HITL checks
	if o.executor != nil {
		o.executor.SetInterruptController(controller)
	}

	if o.logger != nil {
		o.logger.Info("HITL interrupt controller configured", map[string]interface{}{
			"operation": "set_interrupt_controller",
		})
	}
}

// GetInterruptController returns the configured interrupt controller (for API handlers).
func (o *AIOrchestrator) GetInterruptController() InterruptController {
	return o.interruptController
}

// SetOAuthToken updates the Bearer token for outbound HTTP requests.
// This enables agents to push refreshed M2M tokens without restart.
// Per-request tokens via WithOAuthToken(ctx) still take priority.
func (o *AIOrchestrator) SetOAuthToken(token string) {
	o.executor.SetOAuthToken(token)
}

// SetPropagatedHeaders updates the default custom headers for outbound HTTP requests.
// This enables agents to push updated headers at runtime without restart.
// Per-request headers via WithPropagatedHeaders(ctx) still take priority on key conflict.
func (o *AIOrchestrator) SetPropagatedHeaders(headers map[string]string) {
	o.executor.SetPropagatedHeaders(headers)
}

// recordDebugInteraction stores an LLM interaction for debugging.
// Runs asynchronously to avoid blocking orchestration. Errors are logged, not propagated.
// extractErrorProviderInfo extracts model and provider from a core.ProviderError if present.
// Used by error-path recordDebugInteraction calls to populate LLMInteraction model/provider
// when the LLM response is nil (ORCH-008 Fix 4).
func extractErrorProviderInfo(err error) (model, provider string) {
	var pe core.ProviderError
	if errors.As(err, &pe) {
		return pe.Model(), pe.Provider()
	}
	return "", ""
}

// Uses WaitGroup to track in-flight recordings for graceful shutdown.
// This follows FRAMEWORK_DESIGN_PRINCIPLES.md: Resilient Runtime Behavior.
func (o *AIOrchestrator) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
	if o.debugStore == nil {
		return
	}

	// Derive context-precedence audit synchronously (cheap: regex over the
	// already-built prompt). Self-gated — returns nil for interactions
	// whose prompt has no conflict-eligible enrichments, so unrelated
	// hook/micro-resolution records stay clean. Honours any
	// PrecedenceEntityExtractor the caller wired.
	if interaction.PrecedenceAudit == nil {
		interaction.PrecedenceAudit = DerivePrecedenceAudit(ctx, interaction, o.precedenceEntityExtractor)
	}

	// Track this goroutine for graceful shutdown
	o.debugWg.Add(1)

	// Run async to avoid blocking orchestration
	go func() {
		defer o.debugWg.Done()

		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()

		if err := o.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			// Log but don't fail - debug is observability, not critical path
			if o.logger != nil {
				o.logger.Warn("Failed to record LLM debug interaction", map[string]interface{}{
					"request_id": requestID,
					"type":       interaction.Type,
					"error":      err.Error(),
				})
			}
		}
	}()
}

// Shutdown gracefully shuts down the orchestrator, waiting for pending recordings.
// This follows FRAMEWORK_DESIGN_PRINCIPLES.md: Component Lifecycle Rules.
func (o *AIOrchestrator) Shutdown(ctx context.Context) error {
	// Stop background operations first
	o.cancel()

	// Wait for pending debug recordings AND execution storage with timeout
	done := make(chan struct{})
	go func() {
		o.debugWg.Wait()
		o.executionWg.Wait() // Also wait for execution store goroutines
		// Wait for ErrorAnalyzer's pending debug recordings
		if o.executor != nil && o.executor.errorAnalyzer != nil {
			_ = o.executor.errorAnalyzer.Shutdown(ctx)
		}
		// Wait for ConversationHistoryProcessor's pending debug recordings
		if preparer, ok := o.conversationHistoryPreparer.(interface{ Shutdown(context.Context) error }); ok {
			_ = preparer.Shutdown(ctx)
		}
		// Wait for the result processor's pending debug recordings (the distiller,
		// reached directly or through the cache wrapper). Interface assertion so the
		// cache wrapper forwards to the inner distiller.
		if shutdownable, ok := o.resultProcessor.(interface{ Shutdown() }); ok {
			shutdownable.Shutdown()
		}
		close(done)
	}()

	select {
	case <-done:
		if o.recordingCancel != nil {
			o.recordingCancel()
		}
		if o.logger != nil {
			o.logger.Info("Orchestrator shutdown complete", map[string]interface{}{
				"operation": "shutdown",
			})
		}
		return nil
	case <-ctx.Done():
		if o.recordingCancel != nil {
			o.recordingCancel()
		}
		if o.logger != nil {
			o.logger.Warn("Orchestrator shutdown timed out, some recordings may be lost", map[string]interface{}{
				"operation": "shutdown",
				"error":     ctx.Err().Error(),
			})
		}
		return ctx.Err()
	}
}

// generateFallbackRequestID generates a request ID when TraceID is not available.
// Uses atomic counter for uniqueness.
func (o *AIOrchestrator) generateFallbackRequestID() string {
	seq := o.debugSeqID.Add(1)
	return fmt.Sprintf("debug-%d-%d", time.Now().UnixNano(), seq)
}

// requestParameterCorrection asks the LLM to fix parameters based on type error feedback.
// This is the Layer 3 (Validation Feedback) mechanism that enables recovery from type errors
// that slip through Layers 1 and 2.
//
// The method constructs a correction prompt that includes:
//   - Original parameters that caused the error
//   - Error message from the tool
//   - Expected parameter schema from the capability definition
//
// Returns corrected parameters or an error if correction fails.
func (o *AIOrchestrator) requestParameterCorrection(
	ctx context.Context,
	step RoutingStep,
	originalParams map[string]interface{},
	errorMessage string,
	capabilitySchema *EnhancedCapability,
) (map[string]interface{}, error) {
	if o.aiClient == nil {
		return nil, fmt.Errorf("AI client not available for parameter correction")
	}

	// Build schema JSON for the prompt
	var schemaJSON []byte
	if capabilitySchema != nil && len(capabilitySchema.Parameters) > 0 {
		schemaJSON, _ = json.MarshalIndent(capabilitySchema.Parameters, "", "  ")
	}
	paramsJSON, _ := json.MarshalIndent(originalParams, "", "  ")

	// Build the correction prompt
	correctionPrompt := fmt.Sprintf(`The following tool call failed with a type error. Please fix the parameters.

Tool: %s
Capability: %s
Error: %s

Original Parameters (INCORRECT - caused the error above):
%s

Expected Parameter Schema:
%s

CRITICAL RULES for correction:
1. Numbers (type: number, float64, integer, int) must NOT be in quotes
   CORRECT: "lat": 35.6897
   WRONG:   "lat": "35.6897"

2. Booleans (type: boolean, bool) must NOT be in quotes
   CORRECT: "enabled": true
   WRONG:   "enabled": "true"

3. Only strings should be quoted

Respond with ONLY the corrected JSON parameters object. No explanation, no markdown, just the JSON object.`,
		step.AgentName,
		step.Metadata["capability"],
		errorMessage,
		string(paramsJSON),
		string(schemaJSON),
	)

	if o.logger != nil {
		o.logger.DebugWithContext(ctx, "Requesting LLM parameter correction", map[string]interface{}{
			"operation":  "layer3_correction_request",
			"step_id":    step.StepID,
			"capability": step.Metadata["capability"],
		})
	}

	// Get request ID from context baggage for debug correlation
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = o.generateFallbackRequestID()
	}

	// Call LLM for correction. Defer wrapper-side recording — we write
	// the authoritative `correction`-typed LLMInteraction ourselves below.
	llmStartTime := time.Now()
	invocation := aiInvocation{
		Purpose:        "plan-correction",
		Prompt:         correctionPrompt,
		DeferRecording: o.debugStore != nil,
	}
	invocationResult, err := invokeAI(ctx, o.aiClient, invocation)
	var response *core.AIResponse
	if invocationResult != nil {
		response = invocationResult.Response
	}
	effective := effectiveAIRequestForDebug(invocationResult, invocation)
	llmDuration := time.Since(llmStartTime)
	if err == nil {
		core.RecordTokenUsage(ctx, "correction", response.Usage)
	}

	if err != nil {
		// LLM Debug: Record failed correction attempt
		errModel, errProvider := effectiveAIIdentity(invocationResult, response, err)
		o.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:         "correction",
			Timestamp:    llmStartTime,
			DurationMs:   llmDuration.Milliseconds(),
			Prompt:       effective.Prompt,
			SystemPrompt: effective.SystemPrompt,
			Temperature:  effectiveAITemperature(effective, 0),
			MaxTokens:    effectiveAIMaxTokens(effective, 0),
			Model:        errModel,
			Provider:     errProvider,
			Success:      false,
			Error:        err.Error(),
			Attempt:      1,
		})
		return nil, fmt.Errorf("LLM correction request failed: %w", err)
	}

	// LLM Debug: Record successful correction attempt
	model, provider := effectiveAIIdentity(invocationResult, response, nil)
	o.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "correction",
		Timestamp:        llmStartTime,
		DurationMs:       llmDuration.Milliseconds(),
		Prompt:           effective.Prompt,
		SystemPrompt:     effective.SystemPrompt,
		Temperature:      effectiveAITemperature(effective, 0),
		MaxTokens:        effectiveAIMaxTokens(effective, 0),
		Response:         response.Content,
		Model:            model,
		Provider:         provider,
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
		TotalTokens:      response.Usage.TotalTokens,
		Success:          true,
		Attempt:          1,
	})

	// Extract JSON from response (handle potential markdown wrapping)
	content := response.Content
	content = extractJSON(content)

	// Parse corrected parameters
	var correctedParams map[string]interface{}
	if err := json.Unmarshal([]byte(content), &correctedParams); err != nil {
		return nil, fmt.Errorf("failed to parse corrected parameters: %w", err)
	}

	if o.logger != nil {
		o.logger.DebugWithContext(ctx, "LLM parameter correction successful", map[string]interface{}{
			"operation":        "layer3_correction_success",
			"step_id":          step.StepID,
			"corrected_params": correctedParams,
		})
	}

	return correctedParams, nil
}

// extractJSON attempts to extract a JSON object from text that might be wrapped in markdown.
func extractJSON(text string) string {
	// Trim whitespace
	text = strings.TrimSpace(text)

	// Check for markdown code blocks
	if strings.HasPrefix(text, "```json") {
		text = strings.TrimPrefix(text, "```json")
		if idx := strings.Index(text, "```"); idx != -1 {
			text = text[:idx]
		}
	} else if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		if idx := strings.Index(text, "```"); idx != -1 {
			text = text[:idx]
		}
	}

	return strings.TrimSpace(text)
}

// catalogRefreshLoop periodically refreshes the agent catalog
func (o *AIOrchestrator) catalogRefreshLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := o.catalog.Refresh(o.ctx); err != nil {
				// Log error but continue
				if o.logger != nil {
					o.logger.Error("Catalog refresh error", map[string]interface{}{
						"operation": "catalog_refresh",
						"error":     err.Error(),
					})
				}
			}
		case <-o.ctx.Done():
			return
		}
	}
}

// phaseLoopResult holds accumulated results from multi-phase execution.
// Returned by executePhaseLoop for the caller to synthesize and respond.
type phaseLoopResult struct {
	CombinedResult *ExecutionResult // All steps from all phases
	PhasePlans     []*RoutingPlan   // Ordered phase plans (for observability)
	ForcedTerminal bool             // True if MaxPhases/MaxTotalSteps forced termination
	AgentsList     []string         // Deduped agents from all phases
	LastPlan       *RoutingPlan     // Final phase's plan (for store/response)
}

// phaseProgressFn is called when a non-terminal phase completes and the
// loop will continue to the next phase. Used by ProcessRequestStreaming to
// send progress events to the client. Not called on the terminal phase.
type phaseProgressFn func(phaseNumber int, stepsInPhase int)

// executePhaseLoop runs the iterative planning loop:
//
//	generate plan → validate → HITL approval → execute → accumulate → check termination → repeat
//
// Returns accumulated results from all phases, or an error (including ErrInterrupted
// for HITL pauses). The caller is responsible for:
//   - Calling updateMetrics() on both success and failure paths
//   - Synthesis (sync or streaming)
//   - Building the final response
func (o *AIOrchestrator) executePhaseLoop(
	ctx context.Context,
	request string,
	requestID string,
	startTime time.Time,
	span core.Span,
	pipelineContext *core.PipelineContext,
	onPhaseProgress phaseProgressFn,
) (*phaseLoopResult, error) {
	// --- Phase tracking ---
	var (
		allStepResults      = make(map[string]*StepResult)
		allStepsList        []StepResult
		executedStepIDs     []string
		phaseCount          int
		totalSteps          int
		phasePlans          []*RoutingPlan
		continuationNote    string
		lastPlan            *RoutingPlan
		forcedTerminal      bool
		regenEvents         []map[string]interface{} // plan regeneration events for DAG observability
		clarificationNeeded *ClarificationRequest    // ORCH-018: set when planner emits NeedsUserInput
		// ORCH-020 RC8: one-shot guard for template-induced-skip remediation.
		// Flipped to true the first time RC4 skips produce a remediation
		// replan; prevents infinite loops if the remediation attempt also
		// produces skips (the second skip round falls through to normal
		// termination, and the synthesizer tells the user the service is
		// unavailable).
		remediationAttempted bool
	)

	iterativeEnabled := o.config.IterativePlanning.Enabled
	maxPhases := o.config.IterativePlanning.MaxPhases
	maxTotalSteps := o.config.IterativePlanning.MaxTotalSteps
	phaseTimeout := o.config.IterativePlanning.PhaseTimeout

	// Ensure phase timeout accommodates orchestrator delegation with HITL.
	// If the executor has an orchestrator step timeout (HITL wait + execution),
	// the phase timeout must be at least as large — otherwise the phase context
	// cancels the delegation call before the human can approve.
	if orchTimeout := o.executor.orchestratorStepTimeout; orchTimeout > 0 && orchTimeout > phaseTimeout {
		phaseTimeout = orchTimeout
	}

	// --- HITL resume: restore accumulated phase state ---
	var resumeOverride *RoutingPlan
	if override := GetPlanOverride(ctx); override != nil {
		resumeOverride = override
		if priorResults := GetCompletedSteps(ctx); priorResults != nil {
			for stepID, result := range priorResults {
				allStepResults[stepID] = result
				allStepsList = append(allStepsList, *result)
				executedStepIDs = append(executedStepIDs, stepID)
			}
			totalSteps = len(executedStepIDs)
		}
		continuationNote = override.ContinuationNote
	}

	// --- Phase loop entry log ---
	if o.logger != nil {
		o.logger.InfoWithContext(ctx, "Starting iterative phase loop", map[string]interface{}{
			"operation":         "phase_loop_start",
			"request_id":        requestID,
			"iterative_enabled": iterativeEnabled,
			"max_phases":        maxPhases,
			"max_total_steps":   maxTotalSteps,
			"has_resume":        resumeOverride != nil,
		})
	}

	// --- Phase loop ---
	for {
		phaseCount++

		// Update activity signal status at phase boundary
		if o.activityCoordinator != nil {
			status := fmt.Sprintf("executing-phase-%d", phaseCount)
			if phaseCount == 1 {
				status = "executing"
			}
			if err := o.activityCoordinator.UpdateStatus(ctx, requestID, status); err != nil && o.logger != nil {
				o.logger.WarnWithContext(ctx, "Failed to update activity status", map[string]interface{}{
					"operation":  "activity_status_update",
					"request_id": requestID,
					"status":     status,
					"error":      err.Error(),
				})
			}
			telemetry.AddSpanEvent(ctx, "activity.status_update",
				attribute.String("request_id", requestID),
				attribute.String("status", status),
			)
		}

		// Resume mode isolation: clear resume mode for non-resume iterations
		// so that Phase 2+ plans go through HITL evaluation
		loopCtx := ctx
		if resumeOverride == nil {
			if _, isResume := IsResumeMode(ctx); isResume {
				loopCtx = clearResumeMode(ctx)
				if o.logger != nil {
					o.logger.DebugWithContext(ctx, "Resume mode cleared for phase boundary HITL evaluation", map[string]interface{}{
						"operation":  "clear_resume_mode",
						"request_id": requestID,
						"phase":      phaseCount,
					})
				}
			}
		}

		// Per-phase timeout
		phaseCtx := loopCtx
		var phaseCancel context.CancelFunc
		if phaseTimeout > 0 {
			phaseCtx, phaseCancel = context.WithTimeout(loopCtx, phaseTimeout)
		}

		// Phase-level span
		var phaseSpan core.Span
		if o.telemetry != nil {
			phaseCtx, phaseSpan = o.telemetry.StartSpan(phaseCtx,
				fmt.Sprintf("orchestrator.phase.%d", phaseCount))
			// Mirror request_id / agent_name from baggage so the phase span is
			// reachable from a request_id search in Jaeger. plan_id is set
			// further down once the plan has been generated.
			telemetry.SetCommonAttrsOn(phaseCtx, phaseSpan)
		} else {
			phaseSpan = &core.NoOpSpan{}
		}

		// --- Plan generation ---
		var plan *RoutingPlan
		var err error
		var planSource string
		snapshotPhase := phaseCount
		if resumeOverride != nil && resumeOverride.PhaseNumber > 0 {
			snapshotPhase = resumeOverride.PhaseNumber
		}
		phaseCtx = withExecutionRunSnapshot(
			phaseCtx,
			newExecutionRunSnapshot(snapshotPhase, allStepResults, executedStepIDs, continuationNote),
		)

		if resumeOverride != nil {
			if phaseCtx, err = prepareOrchestrationBoundary(phaseCtx, boundaryResume); err != nil {
				phaseSpan.RecordError(err)
				phaseSpan.End()
				if phaseCancel != nil {
					phaseCancel()
				}
				return nil, fmt.Errorf("failed to prepare resume boundary (phase %d): %w", phaseCount, err)
			}
			planSource = "hitl_resume"
			plan = resumeOverride
			phaseCount = resumeOverride.PhaseNumber
			resumeOverride = nil

			if o.logger != nil {
				o.logger.DebugWithContext(phaseCtx, "Skipping plan validation and conflict check for HITL resume plan", map[string]interface{}{
					"operation":         "hitl_resume_skip_validation",
					"request_id":        requestID,
					"plan_id":           plan.PlanID,
					"phase_number":      phaseCount,
					"plan_step_count":   len(plan.Steps),
					"executed_step_ids": len(executedStepIDs),
				})
			}
			telemetry.AddSpanEvent(phaseCtx, "hitl.resume.validation_skipped",
				attribute.String("request_id", requestID),
				attribute.String("plan_id", plan.PlanID),
				attribute.Int("phase_number", phaseCount),
			)
		} else if len(allStepResults) == 0 {
			planSource = "initial_generation"
			plan, err = o.generateExecutionPlan(phaseCtx, request, requestID)
		} else {
			planSource = "continuation_generation"
			plan, err = o.generateContinuationPlan(
				phaseCtx, request, requestID,
				allStepResults, executedStepIDs,
				continuationNote, phaseCount,
			)
		}

		if err != nil {
			var preparationErr *boundaryPreparationError
			if errors.As(err, &preparationErr) {
				phaseSpan.RecordError(err)
				phaseSpan.End()
				if phaseCancel != nil {
					phaseCancel()
				}
				return nil, fmt.Errorf("failed to prepare orchestration boundary (phase %d): %w", phaseCount, err)
			}
			// Phase-appropriate retry
			if len(allStepResults) > 0 {
				genErr := err // capture before regenerateContinuationPlan overwrites err
				plan, err = o.regenerateContinuationPlan(
					phaseCtx, request, requestID,
					allStepResults, executedStepIDs,
					continuationNote, phaseCount, err,
					nil, // plan may be nil — no terminal to preserve
				)
				if err == nil {
					regenEvents = append(regenEvents, map[string]interface{}{
						"phase_number":         phaseCount,
						"validation_error":     genErr.Error(),
						"regenerated_plan_id":  plan.PlanID,
						"regenerated_terminal": plan.IsTerminal(),
						"regenerated_steps":    len(plan.Steps),
					})
				}
			} else {
				plan, err = o.regeneratePlan(phaseCtx, request, requestID, err)
			}
			if err != nil {
				phaseSpan.RecordError(err)
				phaseSpan.End()
				if phaseCancel != nil {
					phaseCancel()
				}
				return nil, fmt.Errorf("failed to generate execution plan (phase %d): %w", phaseCount, err)
			}
		}

		// --- Normalize + validate plan (fixpoint) ---
		// Skip for HITL resume plans: they were validated at generation time and contain
		// the exact checkpoint plan restored from DB 6. Re-validating against the current
		// agent registry could reject the plan for transient agent availability changes
		// between interrupt and resume, discarding the user-approved plan.
		// See Issue 5 in BUG_CONTINUATION_PROMPT_MISSING_CUSTOM_INSTRUCTIONS_AND_RESUME_REPLAY.md.
		//
		// The validators run as a FIXPOINT loop (not a single pass): any regeneration restarts
		// validation from the top, so a regeneration triggered by a later validator cannot slip a
		// defect past an earlier one (e.g. a hallucinated agent past validatePlan). The per-step
		// telemetry lives in runPlanValidationGauntlet; A2 (normalizeTerminalSynthesisPlan) runs at
		// the top of each round.
		if planSource != "hitl_resume" {
			// Build executedStepCaps from prior-phase completed results so RC1
			// (validateTemplatePaths) can validate cross-phase references. Sourced from actual
			// StepResult data, NOT from LLM-declared implicit_deps (per RC7 tightening:
			// implicit_deps is advisory, not authoritative). Stable across the loop.
			var executedStepCaps map[string]stepCapability
			if len(allStepResults) > 0 {
				executedStepCaps = make(map[string]stepCapability, len(allStepResults))
				for id, res := range allStepResults {
					if res == nil {
						continue
					}
					executedStepCaps[id] = stepCapability{agent: res.AgentName, capability: res.Capability}
				}
			}

			maxRounds := o.config.IterativePlanning.MaxValidationRounds
			if maxRounds <= 0 {
				// A config built without DefaultConfig() leaves this 0, which would otherwise give
				// up after zero regenerations. Fall back to the shared default (no drift).
				maxRounds = defaultMaxValidationRounds
			}
			validationStart := time.Now() // for duration_ms on the give-up path

			for round := 0; ; round++ {
				// A2: collapse terminal synthesis pseudo-steps (→ zero-step terminal plan if all
				// dropped). knownStepIDs rebuilt each round because regeneration changes plan.Steps.
				o.normalizeTerminalSynthesisPlan(phaseCtx, plan, knownStepIDSet(executedStepIDs, plan), requestID)

				valErr := o.runPlanValidationGauntlet(phaseCtx, plan, executedStepCaps, executedStepIDs, phaseCount, requestID)
				if valErr == nil {
					break // passed every validator
				}
				if round >= maxRounds {
					// Give up: an explicit phase failure beats dispatching a known-bad plan.
					err = fmt.Errorf("plan failed validation after %d regeneration rounds (phase %d): %w", maxRounds, phaseCount, valErr)
					telemetry.RecordSpanError(phaseCtx, err)
					telemetry.AddSpanEvent(phaseCtx, "orchestrator.plan_validation.exhausted",
						attribute.String("request_id", requestID),
						attribute.Int("phase_number", phaseCount),
						attribute.Int("rounds", maxRounds),
					)
					telemetry.Counter("orchestration.plan.validation_exhausted",
						"module", telemetry.ModuleOrchestration,
						"error_type", "plan_validation_exhausted",
					)
					if o.logger != nil {
						o.logger.ErrorWithContext(phaseCtx, "Plan validation exhausted — failing phase", map[string]interface{}{
							"operation":   "plan_validation",
							"request_id":  requestID,
							"phase":       phaseCount,
							"error":       valErr.Error(),
							"error_type":  "plan_validation_exhausted",
							"rounds":      maxRounds,
							"duration_ms": time.Since(validationStart).Milliseconds(),
						})
					}
					// Note: telemetry.RecordSpanError above already recorded err on this
					// span (it is the active span in phaseCtx) and set its status, so we do
					// not call phaseSpan.RecordError again here — that would duplicate the
					// error event in traces.
					phaseSpan.End()
					if phaseCancel != nil {
						phaseCancel()
					}
					return nil, err
				}

				// Regenerate from the validation error and loop (re-normalize + re-validate from top).
				originalPlan := plan // capture before overwrite
				if len(allStepResults) > 0 {
					plan, err = o.regenerateContinuationPlan(
						phaseCtx, request, requestID,
						allStepResults, executedStepIDs,
						continuationNote, phaseCount, valErr,
						originalPlan.Terminal,
					)
				} else {
					plan, err = o.regeneratePlan(phaseCtx, request, requestID, valErr)
				}
				if err != nil {
					phaseSpan.RecordError(err)
					phaseSpan.End()
					if phaseCancel != nil {
						phaseCancel()
					}
					return nil, fmt.Errorf("failed to generate valid plan (phase %d): %w", phaseCount, err)
				}
				regenEvents = append(regenEvents, map[string]interface{}{
					"phase_number":         phaseCount,
					"validation_error":     valErr.Error(),
					"original_plan_id":     originalPlan.PlanID,
					"original_terminal":    originalPlan.IsTerminal(),
					"original_steps":       len(originalPlan.Steps),
					"regenerated_plan_id":  plan.PlanID,
					"regenerated_terminal": plan.IsTerminal(),
					"regenerated_steps":    len(plan.Steps),
				})
			}
		}

		// The documented AfterPlanning stage observes exactly one final,
		// planner-produced candidate per phase. It runs after the validation
		// fixpoint and before HITL, persistence, or execution. Resume plans are
		// approved checkpoint state rather than newly planner-produced values.
		if planSource != "hitl_resume" && pipelineContext != nil {
			plan = o.runValidatedAfterPlanningHooks(
				phaseCtx,
				pipelineContext,
				plan,
				allStepResults,
				executedStepIDs,
				phaseCount,
				requestID,
			)
		}

		// Set phase metadata on plan
		plan.PhaseNumber = phaseCount
		totalSteps += len(plan.Steps)
		phasePlans = append(phasePlans, plan)
		lastPlan = plan

		// --- ORCH-018: Clarification short-circuit ---
		// If the planner emitted needs_user_input, terminate the phase loop
		// without running HITL, the executor, or the next continuation planner.
		// The synthesizer will produce a clarification-aware user-facing response
		// from clarificationNeeded + already-completed steps in allStepsList.
		//
		// All telemetry follows docs/observability/DISTRIBUTED_TRACING_GUIDE.md §11 patterns
		// and docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md §11 patterns:
		//   - Pattern 1: o.logger nil check
		//   - Pattern 2: operation field in log
		//   - Pattern 3: request_id (already available in this scope)
		//   - Pattern 5: Counter with module label
		//   - Pattern 6: request_id as FIRST AddSpanEvent attribute
		// (Pattern 4 RecordSpanError N/A — clarification is a success path.)
		if plan.NeedsUserInput != nil {
			clarificationNeeded = plan.NeedsUserInput

			// Phase span attributes — filterable in trace search
			phaseSpan.SetAttribute("request_id", requestID)
			phaseSpan.SetAttribute("phase_number", phaseCount)
			phaseSpan.SetAttribute("clarification_short_circuit", true)
			phaseSpan.SetAttribute("clarification_question", truncateString(plan.NeedsUserInput.Question, 200))
			if len(plan.NeedsUserInput.MissingFields) > 0 {
				phaseSpan.SetAttribute("clarification_missing_fields", strings.Join(plan.NeedsUserInput.MissingFields, ","))
				phaseSpan.SetAttribute("clarification_missing_field_count", len(plan.NeedsUserInput.MissingFields))
			}
			if plan.NeedsUserInput.PartialProgress != "" {
				phaseSpan.SetAttribute("clarification_partial_progress_len", len(plan.NeedsUserInput.PartialProgress))
			}

			// Discrete span event — request_id FIRST per Pattern 6
			telemetry.AddSpanEvent(ctx, "orchestrator.clarification_short_circuit",
				attribute.String("request_id", requestID),
				attribute.Int("phase_number", phaseCount),
				attribute.String("question", truncateString(plan.NeedsUserInput.Question, 200)),
				attribute.Int("missing_field_count", len(plan.NeedsUserInput.MissingFields)),
				attribute.Int("prior_completed_steps", len(allStepResults)),
			)

			// Counters — module label per Pattern 5
			telemetry.Counter("orchestrator.clarification_turns",
				"module", telemetry.ModuleOrchestration,
			)
			// Per-missing-field counter for product insight (which fields
			// users most commonly need to provide)
			for _, field := range plan.NeedsUserInput.MissingFields {
				telemetry.Counter("orchestrator.clarification.missing_field",
					"module", telemetry.ModuleOrchestration,
					"field", field,
				)
			}

			// Histograms — prior work distribution
			telemetry.Histogram("orchestrator.clarification_turn.prior_phase_count", float64(phaseCount-1),
				"module", telemetry.ModuleOrchestration,
			)
			telemetry.Histogram("orchestrator.clarification_turn.prior_step_count", float64(len(allStepResults)),
				"module", telemetry.ModuleOrchestration,
			)

			// Log — Pattern 1 nil check, Pattern 2 operation field, Pattern 3 request_id
			if o.logger != nil {
				o.logger.InfoWithContext(ctx, "Planner requested user clarification — terminating phase loop", map[string]interface{}{
					"operation":             "clarification_short_circuit",
					"request_id":            requestID,
					"phase_number":          phaseCount,
					"missing_fields":        plan.NeedsUserInput.MissingFields,
					"prior_completed_steps": len(allStepResults),
				})
			}

			phaseSpan.End()
			if phaseCancel != nil {
				phaseCancel()
			}
			break
		}

		// Phase span attributes
		phaseSpan.SetAttribute("request_id", requestID)
		phaseSpan.SetAttribute("phase_number", phaseCount)
		phaseSpan.SetAttribute("terminal", plan.IsTerminal())
		phaseSpan.SetAttribute("steps_in_phase", len(plan.Steps))
		phaseSpan.SetAttribute("plan_source", planSource)
		if plan.PlanID != "" {
			phaseSpan.SetAttribute("plan_id", plan.PlanID)
		}
		if plan.ContinuationNote != "" {
			phaseSpan.SetAttribute("continuation_note", plan.ContinuationNote)
		}

		// --- HITL Plan Approval (per-phase) ---
		if o.config.HITL.Enabled && o.interruptController != nil {
			hitlCtx := withCheckpointEnrichmentRequired(WithRequestID(phaseCtx, requestID))
			checkpoint, hitlErr := o.interruptController.CheckPlanApproval(hitlCtx, plan)
			if hitlErr != nil {
				phaseSpan.RecordError(hitlErr)
				phaseSpan.End()
				if phaseCancel != nil {
					phaseCancel()
				}
				return nil, fmt.Errorf("HITL plan check failed (phase %d): %w", phaseCount, hitlErr)
			}
			if checkpoint != nil {
				snapshot := newExecutionRunSnapshot(phaseCount, allStepResults, executedStepIDs, continuationNote)
				if saveErr := o.saveAuthoritativeCheckpoint(hitlCtx, checkpoint, snapshot, "plan_level"); saveErr != nil {
					phaseSpan.RecordError(saveErr)
					phaseSpan.End()
					if phaseCancel != nil {
						phaseCancel()
					}
					return nil, saveErr
				}

				// ORCH-022: route through buildNonSuccessResult so the interrupted
				// record carries full PhasePlans/PhaseCount metadata and a cross-phase
				// Result.Steps slice. Plan-level HITL fires BEFORE executor runs for
				// this phase, so currentPhaseSteps is nil.
				rebuildCheckpointCompletedSteps(checkpoint)
				o.storeExecutionAsync(ctx, request, requestID, plan,
					buildNonSuccessResult(nil, phasePlans, phaseCount, forcedTerminal, allStepsList, plan.PlanID, false),
					checkpoint,
				)
				phaseSpan.SetAttribute("hitl.interrupted", true)
				phaseSpan.SetAttribute("hitl.checkpoint_id", checkpoint.CheckpointID)
				phaseSpan.End()
				if phaseCancel != nil {
					phaseCancel()
				}
				return nil, NewInterruptError(checkpoint)
			}
		}

		// --- Execute this phase ---
		execCtx := phaseCtx
		if o.config.HITL.Enabled && o.interruptController != nil {
			execCtx = withCheckpointEnrichmentRequired(execCtx)
		}
		if len(allStepResults) > 0 {
			execCtx = WithCompletedSteps(execCtx, allStepResults)
		}
		// Propagate phase_number in OTel baggage for agent-side debug recording
		execCtx = telemetry.WithBaggage(execCtx, "phase_number", strconv.Itoa(phaseCount))
		// Propagate plan_id so the executor can stamp it on the X-TruvaG3-Plan-ID
		// header — tools then attach it to their server span for cross-service
		// joins back to the orchestrator plan.
		if plan != nil && plan.PlanID != "" {
			execCtx = telemetry.WithBaggage(execCtx, "plan_id", plan.PlanID)
		}

		phaseResult, err := o.executor.Execute(execCtx, plan)

		if err != nil {
			if IsInterrupted(err) {
				// Step-level HITL interrupt — enrich checkpoint with accumulated state
				checkpoint := GetCheckpoint(err)
				if checkpoint != nil {
					combined := make(map[string]*StepResult, len(allStepResults)+len(checkpoint.StepResults))
					for key, value := range allStepResults {
						combined[key] = value
					}
					for key, value := range checkpoint.StepResults {
						combined[key] = value
					}
					snapshot := newExecutionRunSnapshot(phaseCount, combined, executedStepIDs, continuationNote)
					if saveErr := o.saveAuthoritativeCheckpoint(execCtx, checkpoint, snapshot, "step_level"); saveErr != nil {
						phaseSpan.RecordError(saveErr)
						phaseSpan.End()
						if phaseCancel != nil {
							phaseCancel()
						}
						return nil, saveErr
					}
				}
				// ORCH-022: route through buildNonSuccessResult so the interrupted
				// record carries full PhasePlans/PhaseCount metadata and a cross-phase
				// Result.Steps slice. Executor returns (nil, ErrInterrupted) at
				// step-level HITL — phaseResult is nil. Current-phase siblings that
				// completed before the interrupt live on the checkpoint (populated by
				// executor.go:831-838); pull them via extractCurrentPhaseFromCheckpoint.
				currentPhaseSteps := extractCurrentPhaseFromCheckpoint(checkpoint, allStepResults)
				rebuildCheckpointCompletedSteps(checkpoint)
				o.storeExecutionAsync(ctx, request, requestID, plan,
					buildNonSuccessResult(currentPhaseSteps, phasePlans, phaseCount, forcedTerminal, allStepsList, plan.PlanID, false),
					checkpoint,
				)
				phaseSpan.SetAttribute("hitl.interrupted", true)
				phaseSpan.End()
				if phaseCancel != nil {
					phaseCancel()
				}
				return nil, err
			}

			// Execution error
			if o.logger != nil {
				o.logger.ErrorWithContext(ctx, "Phase execution failed", map[string]interface{}{
					"operation":   "phase_execution",
					"request_id":  requestID,
					"plan_id":     plan.PlanID,
					"phase":       phaseCount,
					"error":       err.Error(),
					"duration_ms": time.Since(startTime).Milliseconds(),
				})
			}
			// ORCH-022: route through buildNonSuccessResult so the errored record
			// carries full PhasePlans/PhaseCount metadata and a cross-phase
			// Result.Steps slice. For non-interrupt errors, phaseResult may or may
			// not be nil; surface any partial steps the executor returned.
			var errorCurrentPhaseSteps []StepResult
			if phaseResult != nil {
				errorCurrentPhaseSteps = phaseResult.Steps
			}
			o.storeExecutionAsync(ctx, request, requestID, plan,
				buildNonSuccessResult(errorCurrentPhaseSteps, phasePlans, phaseCount, forcedTerminal, allStepsList, plan.PlanID, false),
				nil,
			)
			phaseSpan.RecordError(err)
			phaseSpan.End()
			if phaseCancel != nil {
				phaseCancel()
			}
			return nil, fmt.Errorf("execution failed (phase %d): %w", phaseCount, err)
		}

		// --- Accumulate results ---
		// Defensive duplicate check: skip steps already accumulated from prior phases.
		// With Change 1A (executor pre-population filter), the executor no longer returns
		// prior-phase steps in result.Steps for the iterative planning path. However,
		// the HITL resume path pre-populates allStepsList before the phase loop
		// (lines 1316-1318), so duplicates could still occur if the executor returns a
		// step that was already accumulated. This guard prevents double-counting.
		// See BUG_PHASE3_SKIPPED_EXECUTION.md Change 2A.
		duplicateCount := 0
		for i := range phaseResult.Steps {
			step := &phaseResult.Steps[i]

			if _, alreadyAccumulated := allStepResults[step.StepID]; alreadyAccumulated {
				duplicateCount++
				continue // Safety net — skip duplicates
			}

			// Annotate step with phase number for DAG visualization grouping.
			// This is persisted in StoredExecution.Result.Steps[].Metadata and
			// visible in the "Raw JSON" view for troubleshooting. (Step 16 F2)
			if step.Metadata == nil {
				step.Metadata = make(map[string]interface{})
			}
			step.Metadata["phase_number"] = phaseCount
			step.Metadata["plan_source"] = planSource // "initial_generation", "continuation_generation", "hitl_resume"

			allStepResults[step.StepID] = step
			allStepsList = append(allStepsList, *step)
			executedStepIDs = append(executedStepIDs, step.StepID)
		}

		// Span event: accumulation summary
		telemetry.AddSpanEvent(ctx, "orchestrator.accumulation.summary",
			attribute.String("request_id", requestID),
			attribute.Int("phase_number", phaseCount),
			attribute.Int("phase_results_count", len(phaseResult.Steps)),
			attribute.Int("duplicates_skipped", duplicateCount),
			attribute.Int("total_accumulated", len(allStepsList)),
		)
		if duplicateCount > 0 {
			telemetry.Counter("orchestration.accumulation.duplicates_skipped",
				"module", telemetry.ModuleOrchestration,
				"phase_number", fmt.Sprintf("%d", phaseCount),
			)
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Duplicate steps skipped during accumulation", map[string]interface{}{
					"operation":        "phase_accumulation",
					"request_id":       requestID,
					"phase_number":     phaseCount,
					"duplicates_count": duplicateCount,
				})
			}
		}

		continuationNote = plan.ContinuationNote

		if o.logger != nil {
			o.logger.InfoWithContext(ctx, "Phase execution completed", map[string]interface{}{
				"operation":      "phase_execution",
				"request_id":     requestID,
				"plan_id":        plan.PlanID,
				"phase":          phaseCount,
				"steps_in_phase": len(phaseResult.Steps),
				"total_steps":    totalSteps,
				"terminal":       plan.IsTerminal(),
				"duration_ms":    time.Since(startTime).Milliseconds(),
			})
		}

		// --- Phase boundary observability (C1 fix) ---
		// The original design referenced o.checkpointStore, but AIOrchestrator has
		// no such field (line 527-573). CheckpointStore is internal to the HITL
		// controller, not exposed to the orchestrator.
		//
		// For phase boundary observability, we use the existing executionStore
		// with intermediate saves between phases. This records partial progress
		// for failed multi-phase requests and enables DAG visualization of
		// in-progress phase boundaries.
		if o.executionStore != nil && !plan.IsTerminal() {
			if o.logger != nil {
				o.logger.DebugWithContext(ctx, "Storing intermediate phase results for DAG visualization", map[string]interface{}{
					"operation":   "intermediate_store",
					"request_id":  requestID,
					"phase":       phaseCount,
					"total_steps": totalSteps,
				})
			}
			// ORCH-022: route through buildNonSuccessResult for schema parity with
			// other records. Inter-phase intermediate store fires AFTER the
			// accumulator has appended the just-completed phase's steps to
			// allStepsList, so currentPhaseSteps is nil.
			o.storeExecutionAsync(ctx, request, requestID, plan,
				buildNonSuccessResult(nil, phasePlans, phaseCount, forcedTerminal, allStepsList, plan.PlanID, true),
				nil,
			)
		}

		// End phase span and cancel timeout (C3 fix: explicit cancel,
		// not defer, to prevent resource leak across loop iterations)
		phaseSpan.End()
		if phaseCancel != nil {
			phaseCancel()
		}

		// --- ORCH-020 RC8: Remediation on template-induced skips ---
		// When RC4 skipped one or more steps in this phase because their
		// upstream template-referenced dependencies failed, detection alone
		// is abrupt termination — the user gets no useful answer. Trigger a
		// remediation continuation so the planner sees the failure context
		// and can either (a) propose an alternative approach or (b) return a
		// terminal empty-steps plan so the synthesizer tells the user the
		// upstream service is unavailable. Bounded to one attempt per
		// orchestration to prevent infinite loops. The gate decision is
		// extracted into decideRemediation so it can be unit-tested in
		// isolation from the phase-loop scaffolding.
		// ORCH-020 RC9: copy the three failure-pattern tunables into a small
		// value object so decideRemediation stays free of the full
		// *OrchestratorConfig. Values are env-overridable via
		// TRUVAG3_FAILURE_PATTERN_* per FRAMEWORK_DESIGN_PRINCIPLES §5.
		patternCfg := FailurePatternConfig{
			MinFailures:  o.config.RemediationFailurePatternMinFailures,
			SignatureLen: o.config.RemediationFailurePatternSignatureLen,
			DisplayLen:   o.config.RemediationFailurePatternDisplayLen,
		}
		remDecision := decideRemediation(
			remediationAttempted, iterativeEnabled,
			phaseCount, maxPhases, totalSteps, maxTotalSteps,
			phaseResult.Steps, allStepResults,
			patternCfg,
		)
		// Always emit the evaluation counter for reasons that represent a
		// meaningful decision about detected skips — lets dashboards answer
		// "did remediation fire, and if not why?" without cross-referencing
		// span events. Silenced for RemediationNoTemplateSkips (the healthy
		// case, most phases) and RemediationIterativeDisabled (config-level,
		// not a per-request signal) to keep the counter cardinality signal-
		// to-noise ratio high. Reason values are bounded (6-value typed
		// enum) so no cardinality risk.
		if remDecision.Reason != RemediationNoTemplateSkips && remDecision.Reason != RemediationIterativeDisabled {
			telemetry.Counter("orchestration.remediation.evaluated",
				"reason", string(remDecision.Reason),
				"module", telemetry.ModuleOrchestration,
			)
		}

		if remDecision.Trigger {
			remediationAttempted = true

			// Override any terminal plan — force another phase so the
			// planner gets a chance to adapt. The override only applies
			// to this iteration's termination check; the next phase's
			// plan is freshly generated via generateContinuationPlan.
			//
			// Edge case note: if the current plan had `len(Steps) == 0`
			// the forced-terminal guard below (line ~2456) would preempt
			// this override. That cannot actually occur in practice
			// because RC4's skip sweep requires non-empty steps to fire,
			// but the invariant is worth noting for future edits.
			falseVal := false
			plan.Terminal = &falseVal
			continuationNote = remDecision.Note

			// Trigger-specific span event + WARN log with skip-id details.
			// The counter above already recorded the trigger via
			// reason="triggered"; no separate trigger-only counter needed.
			// ORCH-020 RC9: stamp has_failure_pattern on the existing span
			// event so operators can answer "did RC9 fire on this trace?"
			// without reading prompt text. Zero cardinality cost — bool.
			telemetry.AddSpanEvent(ctx, "orchestrator.remediation.triggered",
				attribute.String("request_id", requestID),
				attribute.Int("phase_number", phaseCount),
				attribute.String("plan_id", plan.PlanID),
				attribute.Int("skipped_count", remDecision.SkipCount()),
				attribute.String("skipped_step_ids", strings.Join(remDecision.SkipIDs, ",")),
				attribute.Bool("has_failure_pattern", remDecision.Pattern != nil),
			)
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Triggering remediation replan for template-induced skips", map[string]interface{}{
					"operation":        "remediation_trigger",
					"request_id":       requestID,
					"phase_number":     phaseCount,
					"plan_id":          plan.PlanID,
					"skipped_count":    remDecision.SkipCount(),
					"skipped_step_ids": remDecision.SkipIDs,
					"error_type":       "template_induced_skip",
				})

				// ORCH-020 RC9: one DEBUG log diagnosing the pattern
				// analyzer — mirrors decideRemediation's Reason prior art.
				// Lets operators answer "why didn't the pattern fire?"
				// without re-running the computation. Fields are
				// intentionally minimal.
				patternFields := map[string]interface{}{
					"operation":    "remediation_failure_pattern",
					"request_id":   requestID,
					"phase_number": phaseCount,
					"emitted":      remDecision.Pattern != nil,
				}
				if remDecision.Pattern != nil {
					patternFields["total_failed"] = remDecision.Pattern.TotalFailed
					patternFields["dominant_count"] = remDecision.Pattern.DominantCount
				} else {
					patternFields["reject_reason"] = remDecision.PatternRejectReason
				}
				o.logger.DebugWithContext(ctx, "Remediation failure-pattern analyzer", patternFields)
			}
		}

		// --- Check termination ---
		if !iterativeEnabled || plan.IsTerminal() {
			break
		}
		if len(plan.Steps) == 0 {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Iterative planning: non-terminal plan with zero steps, forcing terminal",
					map[string]interface{}{
						"operation":   "forced_terminal",
						"request_id":  requestID,
						"reason":      "zero_steps",
						"phase":       phaseCount,
						"total_steps": totalSteps,
					})
			}
			telemetry.AddSpanEvent(ctx, "orchestrator.forced_terminal",
				attribute.String("request_id", requestID),
				attribute.String("reason", "zero_steps"),
				attribute.Int("phase_number", phaseCount),
				attribute.Int("total_steps", totalSteps),
			)
			forcedTerminal = true
			break
		}
		if phaseCount >= maxPhases {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Iterative planning: max phases reached, forcing terminal",
					map[string]interface{}{
						"operation":   "forced_terminal",
						"request_id":  requestID,
						"reason":      "max_phases_reached",
						"phase":       phaseCount,
						"max_phases":  maxPhases,
						"total_steps": totalSteps,
					})
			}
			telemetry.AddSpanEvent(ctx, "orchestrator.forced_terminal",
				attribute.String("request_id", requestID),
				attribute.String("reason", "max_phases_reached"),
				attribute.Int("phase_number", phaseCount),
				attribute.Int("max_phases", maxPhases),
				attribute.Int("total_steps", totalSteps),
			)
			forcedTerminal = true
			break
		}
		if totalSteps >= maxTotalSteps {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Iterative planning: max total steps reached, forcing terminal",
					map[string]interface{}{
						"operation":       "forced_terminal",
						"request_id":      requestID,
						"reason":          "max_total_steps_reached",
						"phase":           phaseCount,
						"total_steps":     totalSteps,
						"max_total_steps": maxTotalSteps,
					})
			}
			telemetry.AddSpanEvent(ctx, "orchestrator.forced_terminal",
				attribute.String("request_id", requestID),
				attribute.String("reason", "max_total_steps_reached"),
				attribute.Int("phase_number", phaseCount),
				attribute.Int("total_steps", totalSteps),
				attribute.Int("max_total_steps", maxTotalSteps),
			)
			forcedTerminal = true
			break
		}

		// Phase transition span event
		telemetry.AddSpanEvent(ctx, "orchestrator.phase_completed",
			attribute.String("request_id", requestID),
			attribute.Int("phase_number", phaseCount),
			attribute.Bool("terminal", false),
			attribute.Int("steps_executed", len(phaseResult.Steps)),
			attribute.Int("total_steps_so_far", totalSteps),
			attribute.String("continuation_note", continuationNote),
		)

		// Notify caller that we're continuing to next phase
		if onPhaseProgress != nil {
			onPhaseProgress(phaseCount, len(phaseResult.Steps))
		}
	}

	// --- Root span summary attributes ---
	if span != nil {
		span.SetAttribute("phase_count", phaseCount)
		span.SetAttribute("iterative_planning", phaseCount > 1)
		span.SetAttribute("total_steps", totalSteps)
		span.SetAttribute("forced_terminal", forcedTerminal)

		// ORCH-018: classified termination reason for dashboard filtering.
		// Filterable in trace search (e.g. termination_reason=clarification)
		// and usable as a dimension in OTLP metric queries.
		switch {
		case clarificationNeeded != nil:
			span.SetAttribute("termination_reason", "clarification")
			span.SetAttribute("clarification_short_circuit", true)
		case forcedTerminal:
			span.SetAttribute("termination_reason", "forced_terminal")
		default:
			span.SetAttribute("termination_reason", "completed")
		}
	}

	// Telemetry: record phase count histogram
	if o.telemetry != nil {
		o.telemetry.RecordMetric("orchestration.phases_per_request", float64(phaseCount),
			map[string]string{"agent": o.getAgentName()})
	}

	if o.logger != nil {
		o.logger.InfoWithContext(ctx, "Phase loop completed", map[string]interface{}{
			"operation":          "phase_loop_complete",
			"request_id":         requestID,
			"phase_count":        phaseCount,
			"total_steps":        totalSteps,
			"forced_terminal":    forcedTerminal,
			"iterative_planning": phaseCount > 1,
			"duration_ms":        time.Since(startTime).Milliseconds(),
		})
	}

	// --- Build combined ExecutionResult ---
	combinedResult := &ExecutionResult{
		PlanID:              lastPlan.PlanID,
		Steps:               allStepsList,
		Success:             true,
		TotalDuration:       time.Since(startTime),
		PhaseCount:          phaseCount,
		ClarificationNeeded: clarificationNeeded, // ORCH-018: propagate to synthesizer
	}
	for _, step := range allStepsList {
		if !step.Success {
			combinedResult.Success = false
			break
		}
	}

	// Collect agents from ALL phase plans
	allAgents := make(map[string]bool)
	for _, phasePlan := range phasePlans {
		for _, step := range phasePlan.Steps {
			allAgents[step.AgentName] = true
		}
	}
	agentsList := make([]string, 0, len(allAgents))
	for agent := range allAgents {
		agentsList = append(agentsList, agent)
	}

	// Pack phase metadata for execution store (Step 11)
	if combinedResult.Metadata == nil {
		combinedResult.Metadata = make(map[string]interface{})
	}
	combinedResult.Metadata[MetadataKeyPhasePlans] = phasePlans
	combinedResult.Metadata[MetadataKeyPhaseCount] = phaseCount
	combinedResult.Metadata[MetadataKeyForcedTerminal] = forcedTerminal
	if len(regenEvents) > 0 {
		combinedResult.Metadata[MetadataKeyPlanRegenerations] = regenEvents
	}

	// Store final execution
	o.storeExecutionAsync(ctx, request, requestID, lastPlan, combinedResult, nil)

	return &phaseLoopResult{
		CombinedResult: combinedResult,
		PhasePlans:     phasePlans,
		ForcedTerminal: forcedTerminal,
		AgentsList:     agentsList,
		LastPlan:       lastPlan,
	}, nil
}

// synthesizeBuffered owns only buffered synthesis, final response construction,
// and delivery-specific completion observability. Shared lifecycle work has
// already completed in runRequest.
func (o *AIOrchestrator) synthesizeBuffered(state *executionRunState) (*OrchestratorResponse, error) {
	ctx := state.Context
	request := state.Input.Request
	metadata := state.Pipeline.Metadata
	requestID := state.Correlation.RequestID
	startTime := state.StartedAt
	pctx := state.Pipeline
	loopResult := state.Phase.Result
	usageAcc := state.Usage.Accumulator

	// --- Synthesize (child span for Jaeger drill-down) ---
	synthesisCtx := ctx
	var synthesisSpan core.Span
	if o.telemetry != nil {
		synthesisCtx, synthesisSpan = o.telemetry.StartSpan(ctx, "orchestrator.synthesis")
		// Mirror request_id / agent_name / user_id from baggage so the synthesis
		// span is reachable from a request_id search in Jaeger without joining
		// to the parent. The synthesizer adds output attrs (model, tokens,
		// duration) on this same span context.
		telemetry.SetCommonAttrsOn(synthesisCtx, synthesisSpan)
		synthesisSpan.SetAttribute("synthesis.streaming", false)
		successCount, failedCount := countStepOutcomes(loopResult.CombinedResult.Steps)
		synthesisSpan.SetAttribute("synthesis.step_count", len(loopResult.CombinedResult.Steps))
		synthesisSpan.SetAttribute("synthesis.success_step_count", successCount)
		synthesisSpan.SetAttribute("synthesis.failed_step_count", failedCount)
	}
	synthesisStart := time.Now()
	synthesizedResponse, err := o.synthesizer.Synthesize(synthesisCtx, request, loopResult.CombinedResult)
	if synthesisSpan != nil {
		synthesisSpan.SetAttribute("synthesis.duration_ms", time.Since(synthesisStart).Milliseconds())
		synthesisSpan.SetAttribute("synthesis.result_length", len(synthesizedResponse))
		if err != nil {
			synthesisSpan.RecordError(err)
		}
		synthesisSpan.End()
	}
	if err != nil {
		return nil, fmt.Errorf("synthesis failed: %w", err)
	}

	// --- Pipeline hooks: after synthesis ---
	synthesizedResponse = o.runAfterSynthesisHooks(ctx, pctx, synthesizedResponse)

	// Second store: persist result_trim metadata written by Synthesize into StepResult.Metadata.
	// The first store (inside executePhaseLoop) fires before synthesis runs, so result_trim is absent.
	// This second fire-and-forget write picks up the metadata without blocking the response path.
	o.storeExecutionWithFinalResponseAsync(
		ctx,
		request,
		requestID,
		loopResult.LastPlan,
		loopResult.CombinedResult,
		synthesizedResponse,
	)

	// --- Build response ---
	totalUsage, usageByPhase := usageAcc.Snapshot()

	// Merge pipeline enrichments into metadata for response propagation
	responseMetadata := mergeEnrichments(metadata, core.GetPipelineEnrichments(ctx))

	response := &OrchestratorResponse{
		RequestID:       requestID,
		OriginalRequest: request,
		Response:        synthesizedResponse,
		RoutingMode:     o.config.RoutingMode,
		ExecutionTime:   time.Since(startTime),
		AgentsInvolved:  loopResult.AgentsList,
		Metadata:        responseMetadata,
		Confidence:      0.95,                            // TODO: Calculate based on execution success
		Steps:           loopResult.CombinedResult.Steps, // Include step-level details for API consumers (Change 2)
		Usage:           &totalUsage,
		UsageByPhase:    usageByPhase,
		Clarification:   loopResult.CombinedResult.ClarificationNeeded, // ORCH-018: surface to UI consumers
	}

	return response, nil
}

// synthesizeNativeStreaming owns only provider streaming, final response
// construction, and streaming completion observability.
func (o *AIOrchestrator) synthesizeNativeStreaming(state *executionRunState) (*StreamingOrchestratorResponse, error) {
	ctx := state.Context
	request := state.Input.Request
	callback := state.Input.Callback
	requestID := state.Correlation.RequestID
	startTime := state.StartedAt
	pctx := state.Pipeline
	loopResult := state.Phase.Result
	usageAcc := state.Usage.Accumulator

	// --- Synthesis child span for Jaeger drill-down ---
	synthesisCtx := ctx
	var synthesisSpan core.Span
	if o.telemetry != nil {
		synthesisCtx, synthesisSpan = o.telemetry.StartSpan(ctx, "orchestrator.synthesis")
		// Mirror request_id / agent_name / user_id from baggage and set the
		// shape-of-work attrs known up front. The model/tokens/duration attrs
		// are set after streaming completes (see "Streaming finished" block).
		telemetry.SetCommonAttrsOn(synthesisCtx, synthesisSpan)
		synthesisSpan.SetAttribute("synthesis.streaming", true)
		streamSuccessCount, streamFailedCount := countStepOutcomes(loopResult.CombinedResult.Steps)
		synthesisSpan.SetAttribute("synthesis.step_count", len(loopResult.CombinedResult.Steps))
		synthesisSpan.SetAttribute("synthesis.success_step_count", streamSuccessCount)
		synthesisSpan.SetAttribute("synthesis.failed_step_count", streamFailedCount)
	}

	// Build synthesis prompt from combined phase results
	synthesisPrompt, promptErr := o.buildPreparedSynthesisPrompt(synthesisCtx, request, loopResult.CombinedResult)
	if promptErr != nil {
		if synthesisSpan != nil {
			synthesisSpan.RecordError(promptErr)
			synthesisSpan.End()
		}
		return nil, fmt.Errorf("prepare streaming synthesis prompt input: %w", promptErr)
	}
	if synthesisSpan != nil {
		synthesisSpan.SetAttribute("synthesis.prompt_length", len(synthesisPrompt))
	}

	// Emit result_trim.synthesis summary event on streaming path (parity with synthesizer.go)
	if o.config.ResultTrim.Enabled {
		var totalOriginalBytes int
		for _, step := range loopResult.CombinedResult.Steps {
			if step.Success {
				totalOriginalBytes += len(step.Response)
			}
		}
		telemetry.AddSpanEvent(synthesisCtx, "result_trim.synthesis",
			attribute.String("request_id", requestID),
			attribute.Int("original_total_bytes", totalOriginalBytes),
			attribute.Int("prompt_length", len(synthesisPrompt)),
			attribute.Int("step_count", len(loopResult.CombinedResult.Steps)),
		)
	}

	// Agents involved from all phases
	agentsInvolved := loopResult.AgentsList

	// Stream the synthesis response
	// Capture start time for LLM debug recording
	synthesisStart := time.Now()
	systemPrompt := synthesisSystemPromptFor(loopResult.CombinedResult) // ORCH-018: clarification-aware

	// ORCH-018: annotate synthesis span + emit telemetry when clarification
	// mode is active (parity with the non-streaming path in synthesizer.go).
	// Consistent with the surrounding code's assumption that
	// loopResult.CombinedResult is non-nil (see Steps iteration above).
	if loopResult.CombinedResult.ClarificationNeeded != nil {
		cr := loopResult.CombinedResult.ClarificationNeeded
		telemetry.SetSpanAttributes(synthesisCtx,
			attribute.Bool("synthesis.clarification_mode", true),
			attribute.Int("synthesis.partial_progress_len", len(cr.PartialProgress)),
			attribute.Int("synthesis.missing_field_count", len(cr.MissingFields)),
		)
		telemetry.AddSpanEvent(synthesisCtx, "orchestrator.synthesis.clarification_mode",
			attribute.String("request_id", requestID),
			attribute.String("question", truncateString(cr.Question, 200)),
			attribute.Int("missing_field_count", len(cr.MissingFields)),
			attribute.Bool("has_partial_progress", cr.PartialProgress != ""),
		)
		telemetry.Counter("orchestrator.synthesis.clarification_mode",
			"module", telemetry.ModuleOrchestration,
		)
		if o.logger != nil {
			o.logger.InfoWithContext(ctx, "Synthesizer entered clarification mode (streaming)", map[string]interface{}{
				"operation":            "synthesis_clarification_mode",
				"request_id":           requestID,
				"missing_field_count":  len(cr.MissingFields),
				"has_partial_progress": cr.PartialProgress != "",
				"question_length":      len(cr.Question),
			})
		}
	}

	var fullContent strings.Builder
	chunkIndex := 0
	var finishReason string
	streamCallback := func(chunk core.StreamChunk) error {
		if chunk.Content != "" {
			fullContent.WriteString(chunk.Content)
		}
		// Capture finish reason from final chunk
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
		chunkIndex++
		return callback(chunk)
	}

	streamSynthesisOpts := o.synthesisAIOptions(systemPrompt)
	// Scope the ai.purpose baggage to the LLM call only — it would otherwise
	// leak into AfterSynthesis hooks and mis-tag user_memory.extraction spans.
	streamCtx := telemetry.WithBaggage(synthesisCtx, "ai.purpose", "synthesis_streaming")
	invocation := aiInvocation{
		Purpose:        "synthesis",
		Prompt:         synthesisPrompt,
		Options:        streamSynthesisOpts,
		DeferRecording: o.debugStore != nil,
	}
	invocationResult, err := streamAI(streamCtx, o.aiClient, invocation, streamCallback)
	var aiResponse *core.AIResponse
	if invocationResult != nil {
		aiResponse = invocationResult.Response
	}
	effective := effectiveAIRequestForDebug(invocationResult, invocation)
	if err == nil || err == core.ErrStreamPartiallyCompleted {
		core.RecordTokenUsage(ctx, "synthesis", aiResponse.Usage)
	}

	// Handle streaming errors
	if err != nil {
		if err == core.ErrStreamPartiallyCompleted {
			// LLM Debug: Record partial streaming synthesis (interrupted but has content)
			model, provider := effectiveAIIdentity(invocationResult, aiResponse, err)
			o.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type:             "synthesis_streaming",
				Timestamp:        synthesisStart,
				DurationMs:       time.Since(synthesisStart).Milliseconds(),
				Prompt:           effective.Prompt,
				SystemPrompt:     effective.SystemPrompt,
				Temperature:      effectiveAITemperature(effective, streamSynthesisOpts.Temperature),
				MaxTokens:        effectiveAIMaxTokens(effective, streamSynthesisOpts.MaxTokens),
				Model:            model,
				Provider:         provider,
				Response:         aiResponse.Content,
				PromptTokens:     aiResponse.Usage.PromptTokens,     // May be partial
				CompletionTokens: aiResponse.Usage.CompletionTokens, // May be partial
				TotalTokens:      aiResponse.Usage.TotalTokens,      // May be partial
				Success:          true,                              // Partial success - we have content
				Error:            "stream partially completed",
				Attempt:          1,
			})

			if synthesisSpan != nil {
				synthesisSpan.SetAttribute("synthesis.partial", true)
				synthesisSpan.SetAttribute("synthesis.chunks_sent", chunkIndex)
				synthesisSpan.SetAttribute("synthesis.response_length", fullContent.Len())
				synthesisSpan.SetAttribute("synthesis.duration_ms", time.Since(synthesisStart).Milliseconds())
				if aiResponse != nil {
					synthesisSpan.SetAttribute("synthesis.model", aiResponse.Model)
					synthesisSpan.SetAttribute("synthesis.provider", aiResponse.Provider)
				}
				synthesisSpan.End()
			}
			o.storeExecutionAsync(ctx, request, requestID, loopResult.LastPlan, loopResult.CombinedResult, nil)
			partialUsage, partialByPhase := usageAcc.Snapshot()
			return &StreamingOrchestratorResponse{
				OrchestratorResponse: OrchestratorResponse{
					RequestID:       requestID,
					OriginalRequest: request,
					Response:        fullContent.String(),
					RoutingMode:     o.config.RoutingMode,
					ExecutionTime:   time.Since(startTime),
					AgentsInvolved:  agentsInvolved,
					Metadata:        mergeEnrichments(state.Pipeline.Metadata, core.GetPipelineEnrichments(ctx)),
					Errors:          []string{"stream partially completed"},
					Confidence:      0.7,
					Steps:           loopResult.CombinedResult.Steps,
					Usage:           &partialUsage,
					UsageByPhase:    partialByPhase,
					Clarification:   loopResult.CombinedResult.ClarificationNeeded,
				},
				ChunksDelivered: chunkIndex,
				StreamCompleted: false,
				PartialContent:  true,
				StepResults:     loopResult.CombinedResult.Steps,
				FinishReason:    "cancelled",
			}, nil
		}

		// LLM Debug: Record failed streaming synthesis
		errModel, errProvider := effectiveAIIdentity(invocationResult, aiResponse, err)
		o.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:         "synthesis_streaming",
			Timestamp:    synthesisStart,
			DurationMs:   time.Since(synthesisStart).Milliseconds(),
			Prompt:       effective.Prompt,
			SystemPrompt: effective.SystemPrompt,
			Temperature:  effectiveAITemperature(effective, streamSynthesisOpts.Temperature),
			MaxTokens:    effectiveAIMaxTokens(effective, streamSynthesisOpts.MaxTokens),
			Model:        errModel,
			Provider:     errProvider,
			Success:      false,
			Error:        err.Error(),
			Attempt:      1,
		})

		if synthesisSpan != nil {
			synthesisSpan.RecordError(err)
			synthesisSpan.SetAttribute("synthesis.duration_ms", time.Since(synthesisStart).Milliseconds())
			synthesisSpan.End()
		}
		o.storeExecutionAsync(ctx, request, requestID, loopResult.LastPlan, loopResult.CombinedResult, nil)
		return nil, fmt.Errorf("synthesis streaming failed: %w", err)
	}

	// --- Pipeline hooks: after synthesis (streaming path) ---
	// Note: tokens were already streamed to the client. AfterSynthesis hooks
	// operate on the accumulated full content for post-processing (logging, memory storage, etc.).
	finalContent := o.runAfterSynthesisHooks(ctx, pctx, aiResponse.Content)

	// The terminal native-streaming snapshot is recorded only after synthesis
	// and AfterSynthesis hooks reach their terminal outcome. Request-local
	// ordering prevents this view from being overwritten by an earlier phase
	// snapshot.
	o.storeExecutionWithFinalResponseAsync(
		ctx,
		request,
		requestID,
		loopResult.LastPlan,
		loopResult.CombinedResult,
		finalContent,
	)

	// Build final response with all enhanced fields
	totalUsage, usageByPhase := usageAcc.Snapshot()
	response := &StreamingOrchestratorResponse{
		OrchestratorResponse: OrchestratorResponse{
			RequestID:       requestID,
			OriginalRequest: request,
			Response:        finalContent,
			RoutingMode:     o.config.RoutingMode,
			ExecutionTime:   time.Since(startTime),
			AgentsInvolved:  agentsInvolved,
			Metadata:        mergeEnrichments(state.Pipeline.Metadata, core.GetPipelineEnrichments(ctx)),
			Confidence:      0.95,
			Steps:           loopResult.CombinedResult.Steps,
			Usage:           &totalUsage,
			UsageByPhase:    usageByPhase,
			Clarification:   loopResult.CombinedResult.ClarificationNeeded, // ORCH-018: surface to UI consumers
		},
		ChunksDelivered: chunkIndex,
		StreamCompleted: true,
		PartialContent:  false,
		StepResults:     loopResult.CombinedResult.Steps,
		FinishReason:    finishReason,
	}

	// Stamp final synthesis attrs onto the parent span before End() so they're
	// visible in Jaeger alongside the input attrs. The 429-then-failover case
	// shows up here as model/provider being the *successful* (failover) ones.
	if synthesisSpan != nil {
		synthesisSpan.SetAttribute("synthesis.model", aiResponse.Model)
		synthesisSpan.SetAttribute("synthesis.provider", aiResponse.Provider)
		synthesisSpan.SetAttribute("synthesis.response_length", len(aiResponse.Content))
		synthesisSpan.SetAttribute("synthesis.prompt_tokens", aiResponse.Usage.PromptTokens)
		synthesisSpan.SetAttribute("synthesis.completion_tokens", aiResponse.Usage.CompletionTokens)
		synthesisSpan.SetAttribute("synthesis.total_tokens", aiResponse.Usage.TotalTokens)
		synthesisSpan.SetAttribute("synthesis.chunks_sent", chunkIndex)
		synthesisSpan.SetAttribute("synthesis.duration_ms", time.Since(synthesisStart).Milliseconds())
		if finishReason != "" {
			synthesisSpan.SetAttribute("synthesis.finish_reason", finishReason)
		}
	}

	// LLM Debug: Record successful streaming synthesis
	model, provider := effectiveAIIdentity(invocationResult, aiResponse, nil)
	o.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "synthesis_streaming",
		Timestamp:        synthesisStart,
		DurationMs:       time.Since(synthesisStart).Milliseconds(),
		Prompt:           effective.Prompt,
		SystemPrompt:     effective.SystemPrompt,
		Temperature:      effectiveAITemperature(effective, streamSynthesisOpts.Temperature),
		MaxTokens:        effectiveAIMaxTokens(effective, streamSynthesisOpts.MaxTokens),
		Model:            model,
		Provider:         provider,
		Response:         aiResponse.Content,
		PromptTokens:     aiResponse.Usage.PromptTokens,
		CompletionTokens: aiResponse.Usage.CompletionTokens,
		TotalTokens:      aiResponse.Usage.TotalTokens,
		Success:          true,
		Attempt:          1,
	})

	if synthesisSpan != nil {
		synthesisSpan.End()
	}
	return response, nil
}

// buildSynthesisPrompt creates the prompt for synthesizing agent responses.
// ctx is threaded for OTEL span event correlation in ResultProcessor.
// Uses XML-tagged structure following docs/building/EFFECTIVE_PROMPTS_GUIDE.md §8.3.
// Budget allocation via ProcessMultipleForBudget mirrors synthesizer.go (Gap 1 fix).
func (o *AIOrchestrator) buildSynthesisPrompt(ctx context.Context, request string, result *ExecutionResult) string {
	prompt, _ := o.buildPreparedSynthesisPrompt(ctx, request, result)
	return prompt
}

func (o *AIOrchestrator) buildPreparedSynthesisPrompt(
	ctx context.Context,
	request string,
	result *ExecutionResult,
) (string, error) {
	var sb strings.Builder
	preparedRequest, err := preparePromptValue(
		ctx, promptSynthesis, promptValueRequest, promptFieldRequest, request,
	)
	if err != nil {
		return "", err
	}
	preparedEnrichments, err := prepareKnownPromptEnrichments(
		ctx, promptSynthesis, core.GetPipelineEnrichments(ctx),
	)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&sb, "<user_request>\n%s\n</user_request>\n\n", preparedRequest)

	// Include agent coordination, memory, and conversation history from pipeline enrichments.
	// Gives the synthesizer awareness of active agents, prior cross-agent activity, and
	// session context so it can produce more informed, deduplicated summaries.
	enrichments := preparedEnrichments
	if len(enrichments) > 0 {
		if coordCtx, ok := enrichments[core.EnrichmentActivityCoordination]; ok {
			if coordStr, isStr := coordCtx.(string); isStr && coordStr != "" {
				sb.WriteString("<agent_coordination>\n")
				sb.WriteString(coordStr)
				sb.WriteString("\n</agent_coordination>\n\n")
			}
		}
		// User profile from UserMemoryEnrichmentHook (per-user private facts)
		if userProfile, ok := enrichments[core.EnrichmentUserProfile]; ok {
			if profileStr, isStr := userProfile.(string); isStr && profileStr != "" {
				sb.WriteString(profileStr)
				sb.WriteString("\n\n")
			}
		}
		if ragCtx, ok := enrichments[core.EnrichmentRAGContext]; ok {
			if ragStr, isStr := ragCtx.(string); isStr && ragStr != "" {
				sb.WriteString("<agent_memory>\n")
				sb.WriteString(ragStr)
				sb.WriteString("\n</agent_memory>\n\n")
			}
		}
		if convHistory, ok := enrichments[core.EnrichmentConversationHistory]; ok {
			if convStr, isStr := convHistory.(string); isStr && convStr != "" {
				sb.WriteString("<conversation_history>\n")
				sb.WriteString(convStr)
				sb.WriteString("\n</conversation_history>\n\n")
			}
		}
	}

	// Precedence rule: emitted right after the enrichments so the synthesizer
	// sees it immediately before the agent_responses block. A synthesizer
	// composing the user-facing answer can otherwise echo a stale
	// <user_profile> "Context" entry over what the agents actually returned.
	writeContextPrecedence(ctx, &sb, enrichments, PromptKindSynthesisOrchestrator)

	sb.WriteString("<agent_responses>\n\n")

	trimConfig := o.config.ResultTrim

	// Extract request_id for logging (LOGGING_IMPLEMENTATION_GUIDE.md Pattern 3)
	promptRequestID := ""
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		promptRequestID = bag["request_id"]
	}

	// Collect successful steps for budget-aware processing (Gap 1: ProcessMultipleForBudget)
	var successfulSteps []StepResult
	for _, step := range result.Steps {
		if step.Success {
			successfulSteps = append(successfulSteps, step)
		}
	}

	// Pre-process with budget allocation when multiple results and total budget configured.
	// Guard mirrors synthesizer.go:188-190. trimConfig is a struct value (never nil),
	// so only Enabled, resultProcessor, and MaxTotalPromptBytes are checked.
	var budgetProcessed map[string]string // stepID → processed response
	var budgetMeta map[string]*ResultTrimMetadata
	useBudgetAlloc := len(successfulSteps) > 1 &&
		trimConfig.Enabled &&
		o.resultProcessor != nil && trimConfig.MaxTotalPromptBytes > 0

	if useBudgetAlloc {
		processed, bm := ProcessMultipleForBudget(ctx, o.resultProcessor, successfulSteps,
			trimConfig.MaxTotalPromptBytes, trimConfig.MaxResultBytes, request)
		budgetProcessed = make(map[string]string, len(successfulSteps))
		for i, step := range successfulSteps {
			budgetProcessed[step.StepID] = processed[i]
		}
		budgetMeta = bm
	}

	for i, step := range result.Steps {
		if !step.Success {
			preparedAgent, preparedInstruction, preparedError, prepareErr := prepareSynthesisStepValues(
				ctx, step.AgentName, step.Instruction, step.Error, promptFieldPriorResultError,
			)
			if prepareErr != nil {
				return "", prepareErr
			}
			fmt.Fprintf(&sb, "<agent name=%q task=%q status=\"failed\">\n%s\n</agent>\n\n", preparedAgent, preparedInstruction, preparedError)
			continue
		}

		response := step.Response
		originalSize := len(response)

		// Apply result processing — budget-allocated, per-result, or truncation fallback
		var trimMeta *ResultTrimMetadata
		if bp, ok := budgetProcessed[step.StepID]; ok {
			// Budget-allocated response (Phase 3)
			response = bp
			trimMeta = budgetMeta[step.StepID]
		} else if trimConfig.Enabled && o.resultProcessor != nil {
			// Per-result trimming (single result or no total budget)
			trimCtx, meta := WithTrimMetadataCapture(ctx)
			response = o.resultProcessor.ProcessForPrompt(trimCtx, response, trimConfig.MaxTotalPromptBytes, ResultProcessorContext{
				StepID: step.StepID, AgentName: step.AgentName, Instruction: step.Instruction,
				OriginalQuery: request,
			})
			trimMeta = meta
		} else if trimConfig.Enabled && len(response) > trimConfig.MaxTotalPromptBytes {
			// Byte truncation fallback (no result processor ran → no model analyzed the dropped
			// tail). appendDisclosure cuts + discloses within the budget in one step. (Phase 16)
			response = appendDisclosure(response, truncationDisclosure(), trimConfig.MaxTotalPromptBytes)
			trimMeta = &ResultTrimMetadata{
				OriginalBytes: originalSize, TrimmedBytes: len(response), Method: "truncate",
				PartialCoverage: true, ContentLost: true,
			}
		}

		// Write trim metadata to step
		if trimMeta != nil {
			if result.Steps[i].Metadata == nil {
				result.Steps[i].Metadata = make(map[string]interface{})
			}
			result.Steps[i].Metadata["result_trim"] = cloneResultTrimMetadata(trimMeta)
		}

		trimmedSize := len(response)

		// Emit span event for result trim decisions (visible in Jaeger) — see
		// lossyTrimEvent for why the gate is not byte inequality alone.
		if lossyTrimEvent(trimMeta, originalSize, trimmedSize) {
			attrs := []attribute.KeyValue{
				attribute.String("request_id", promptRequestID),
				attribute.String("step_id", step.StepID),
				attribute.String("agent_name", step.AgentName),
				attribute.String("method", trimMeta.Method),
				// Unconditional: explicit false = verified lossless, distinct from a
				// legacy span with no signal — the same tri-state the JSON field carries.
				attribute.Bool("content_lost", trimMeta.ContentLost),
				attribute.Int("original_bytes", trimMeta.OriginalBytes),
				attribute.Int("trimmed_bytes", trimMeta.TrimmedBytes),
				attribute.Int("fields_kept", trimMeta.FieldsKept),
				attribute.Int("fields_dropped", trimMeta.FieldsDropped),
			}
			if trimMeta.BackfilledCount > 0 {
				attrs = append(attrs, attribute.Int("backfilled_count", trimMeta.BackfilledCount))
			}
			if trimMeta.ThresholdSkipped > 0 {
				attrs = append(attrs, attribute.Int("threshold_skipped", trimMeta.ThresholdSkipped))
			}
			if trimMeta.BudgetAllocated > 0 {
				attrs = append(attrs, attribute.Int("budget_allocated", trimMeta.BudgetAllocated))
			}
			if len(trimMeta.Keywords) > 0 {
				attrs = append(attrs, attribute.String("keywords", strings.Join(trimMeta.Keywords, ",")))
			}
			if len(trimMeta.MatchedPaths) > 0 {
				attrs = append(attrs, attribute.String("matched_paths", strings.Join(trimMeta.MatchedPaths, ",")))
			}
			// Phase 16 coverage fields — surfaced so Jaeger can distinguish a 28%-seen
			// distill from a 100%-seen one without digging into step metadata.
			if trimMeta.SourceCoverageRatio > 0 {
				attrs = append(attrs, attribute.Float64("source_coverage_ratio", trimMeta.SourceCoverageRatio))
			}
			if trimMeta.LLMInputBytes > 0 {
				attrs = append(attrs, attribute.Int("llm_input_bytes", trimMeta.LLMInputBytes))
			}
			if trimMeta.SegmentsTotal > 0 {
				attrs = append(attrs,
					attribute.Int("segments_analyzed", trimMeta.SegmentsAnalyzed),
					attribute.Int("segments_total", trimMeta.SegmentsTotal))
			}
			if trimMeta.PartialCoverage {
				attrs = append(attrs, attribute.Bool("partial_coverage", true))
			}
			if trimMeta.CombineTruncated {
				attrs = append(attrs, attribute.Bool("combine_truncated", true))
			}
			telemetry.AddSpanEvent(ctx, "result_trim.completed", attrs...)
		}

		// Format response: pretty-print JSON, or use plain text as-is.
		// UseNumber so large IDs survive this last re-parse before the streaming synthesis prompt.
		if parsed, perr := unmarshalPreservingNumbers([]byte(response)); perr == nil {
			parsed = deserializeStringValues(parsed)
			if formatted, err := json.MarshalIndent(parsed, "", "  "); err == nil {
				response = string(formatted)
			}
		}

		if o.logger != nil && lossyTrimEvent(trimMeta, originalSize, trimmedSize) {
			o.logger.DebugWithContext(ctx, "Result trimmed for streaming synthesis", map[string]interface{}{
				"operation":        "result_trim",
				"request_id":       promptRequestID,
				"step_id":          step.StepID,
				"agent_name":       step.AgentName,
				"original_bytes":   originalSize,
				"trimmed_bytes":    trimmedSize,
				"budget_allocated": useBudgetAlloc,
			})
		}

		preparedAgent, preparedInstruction, preparedResponse, prepareErr := prepareSynthesisStepValues(
			ctx, step.AgentName, step.Instruction, response, promptFieldPriorResultResponse,
		)
		if prepareErr != nil {
			return "", prepareErr
		}
		fmt.Fprintf(&sb, "<agent name=%q task=%q status=\"success\">\n%s\n</agent>\n\n", preparedAgent, preparedInstruction, preparedResponse)
	}

	sb.WriteString("</agent_responses>\n\n")

	// ORCH-018: clarification-aware section (parity with synthesizer.go).
	// Only present when the planner emitted needs_user_input and the phase
	// loop short-circuited. Consistent with the surrounding code's assumption
	// that `result` is non-nil (see result.Steps iteration above).
	if result.ClarificationNeeded != nil {
		cr := result.ClarificationNeeded
		sb.WriteString("<clarification_needed>\n")
		fmt.Fprintf(&sb, "Question to ask the user: %s\n", cr.Question)
		if len(cr.MissingFields) > 0 {
			fmt.Fprintf(&sb, "Missing fields: %s\n", strings.Join(cr.MissingFields, ", "))
		}
		if cr.PartialProgress != "" {
			fmt.Fprintf(&sb, "Partial progress to mention: %s\n", cr.PartialProgress)
		}
		sb.WriteString("</clarification_needed>\n\n")
	}

	sb.WriteString("Synthesize the above into a helpful answer.")

	prompt := sb.String()

	// Emit synthesis prompt size metric
	if registry := core.GetGlobalMetricsRegistry(); registry != nil {
		registry.Histogram("orchestration.synthesis_prompt.size_bytes", float64(len(prompt)))
	}

	return prompt, nil
}

func (o *AIOrchestrator) synthesisAIOptions(systemPrompt string) *core.AIOptions {
	base := &core.AIOptions{
		Temperature:  0.5,
		MaxTokens:    5000,
		SystemPrompt: systemPrompt,
	}
	return mergeAIOptions(base, o.config.SynthesisAIOptions)
}

// planAIOptions returns AIOptions for plan generation calls.
func (o *AIOrchestrator) planAIOptions(temperature float32, systemPrompt string) *core.AIOptions {
	base := &core.AIOptions{
		Temperature:  temperature,
		MaxTokens:    15000,
		SystemPrompt: systemPrompt,
	}
	return mergeAIOptions(base, o.config.PlanAIOptions)
}

// planPromptSystemSource records whether the effective planning system prompt
// came from a framework-owned builder/fallback, an arbitrary developer
// builder, or the raw PlanAIOptions replacement layer. Prompt finalization uses
// this provenance to enforce ownership of reserved framework sections.
func (o *AIOrchestrator) planPromptSystemSource() promptSystemSource {
	if o != nil && o.config != nil && o.config.PlanAIOptions != nil &&
		o.config.PlanAIOptions.SystemPrompt != nil {
		return promptSystemAIOptionsOverride
	}
	if o == nil || o.promptBuilder == nil {
		return promptSystemFrameworkBuilder
	}
	if _, ok := o.promptBuilder.(SystemPromptBuilder); !ok {
		return promptSystemFrameworkBuilder
	}
	switch o.promptBuilder.(type) {
	case *DefaultPromptBuilder, *TemplatePromptBuilder:
		return promptSystemFrameworkBuilder
	default:
		return promptSystemCustomBuilder
	}
}

// generateExecutionPlan uses LLM to create an execution plan
func (o *AIOrchestrator) generateExecutionPlan(ctx context.Context, request string, requestID string) (*RoutingPlan, error) {
	var err error
	ctx, err = prepareOrchestrationBoundary(ctx, boundaryInitialPlanning)
	if err != nil {
		return nil, fmt.Errorf("prepare initial planning boundary: %w", err)
	}
	planGenStart := time.Now()

	if o.logger != nil {
		o.logger.DebugWithContext(ctx, "Starting plan generation", map[string]interface{}{
			"operation":  "plan_generation_start",
			"request_id": requestID,
		})
	}
	// Check if AI client is available
	if o.aiClient == nil {
		return nil, fmt.Errorf("AI client not configured")
	}

	// Inject requestID into context for child components (e.g., TieredCapabilityProvider)
	// to correlate their debug recordings with this orchestrator request.
	ctx = WithRequestID(ctx, requestID)

	// Build initial prompt with capability information
	// Returns PlanningPromptResult with both prompt and allowed agents for hallucination validation
	promptResult, err := o.buildPlanningPrompt(ctx, request)
	if err != nil {
		return nil, err
	}

	// Determine max attempts: 1 initial + retries (if enabled)
	maxAttempts := 1
	if o.config != nil && o.config.PlanParseRetryEnabled {
		maxAttempts = 1 + o.config.PlanParseMaxRetries
	}

	var lastParseErr error
	var totalTokensUsed int

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		planOpts := o.planAIOptions(0.3, promptResult.SystemPrompt)
		if o.logger != nil {
			o.logger.DebugWithContext(ctx, "LLM prompt constructed", map[string]interface{}{
				"operation":        "prompt_construction",
				"request_id":       requestID,
				"prompt_length":    len(promptResult.Prompt),
				"estimated_tokens": len(promptResult.Prompt) / 4, // Rough estimate: 4 chars per token
				"attempt":          attempt,
				"max_attempts":     maxAttempts,
			})
		}

		if o.logger != nil {
			o.logger.DebugWithContext(ctx, "Calling LLM for plan generation", map[string]interface{}{
				"operation":   "llm_call",
				"request_id":  requestID,
				"temperature": 0.3,
				"max_tokens":  planOpts.MaxTokens,
				"attempt":     attempt,
			})
		}

		// Telemetry: Record LLM prompt for visibility in distributed traces
		planEventAttrs := []attribute.KeyValue{
			attribute.String("request_id", requestID),
			attribute.String("prompt", truncateString(promptResult.Prompt, 2000)),
			attribute.Int("prompt_length", len(promptResult.Prompt)),
			attribute.Float64("temperature", 0.3),
			attribute.Int("max_tokens", planOpts.MaxTokens),
			attribute.Int("attempt", attempt),
		}
		if planOpts.Model != "" {
			planEventAttrs = append(planEventAttrs, attribute.String("model", planOpts.Model))
		}
		telemetry.AddSpanEvent(ctx, "llm.plan_generation.request", planEventAttrs...)

		// Call LLM. ai.purpose baggage stamps the provider's ai.generate_response
		// span with what this call is FOR — without it, all 14+ AI spans look
		// the same in Jaeger.
		ctx := telemetry.WithBaggage(ctx, "ai.purpose", "plan_generation")
		// Defer wrapper-side recording — we emit the authoritative
		// `plan_generation` typed LLMInteraction below.
		llmStartTime := time.Now()
		invocation := aiInvocation{
			Purpose:        "planning",
			Prompt:         promptResult.Prompt,
			Options:        planOpts,
			SystemSource:   o.planPromptSystemSource(),
			DeferRecording: o.debugStore != nil,
		}
		invocationResult, err := invokeAI(ctx, o.aiClient, invocation)
		var aiResponse *core.AIResponse
		if invocationResult != nil {
			aiResponse = invocationResult.Response
		}
		effective := effectiveAIRequestForDebug(invocationResult, invocation)
		llmDuration := time.Since(llmStartTime)
		if err == nil {
			core.RecordTokenUsage(ctx, "planning", aiResponse.Usage)
		}

		if err != nil {
			telemetry.AddSpanEvent(ctx, "llm.plan_generation.error",
				attribute.String("request_id", requestID),
				attribute.String("error", err.Error()),
				attribute.Int64("duration_ms", llmDuration.Milliseconds()),
				attribute.Int("attempt", attempt),
			)
			// Unified Metrics: Record failed AI request
			telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "plan_generation",
				float64(llmDuration.Milliseconds()), "error")
			// Record overall plan generation failure (orchestration-local metrics)
			telemetry.Histogram("plan_generation.duration_ms", float64(time.Since(planGenStart).Milliseconds()),
				"module", telemetry.ModuleOrchestration, "status", "error")
			telemetry.Counter("plan_generation.total",
				"module", telemetry.ModuleOrchestration, "status", "error")

			// LLM Debug: Record the actual prepared request, even on failure.
			errModel, errProvider := effectiveAIIdentity(invocationResult, aiResponse, err)
			o.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type:         "plan_generation",
				Timestamp:    llmStartTime,
				DurationMs:   llmDuration.Milliseconds(),
				Prompt:       effective.Prompt,
				SystemPrompt: effective.SystemPrompt,
				Temperature:  effectiveAITemperature(effective, planOpts.Temperature),
				MaxTokens:    effectiveAIMaxTokens(effective, planOpts.MaxTokens),
				Model:        errModel,
				Provider:     errProvider,
				Success:      false,
				Error:        err.Error(),
				Attempt:      attempt,
			})

			if o.logger != nil {
				o.logger.ErrorWithContext(ctx, "Plan generation failed", map[string]interface{}{
					"operation":   "plan_generation",
					"request_id":  requestID,
					"error":       err.Error(),
					"duration_ms": llmDuration.Milliseconds(),
					"attempt":     attempt,
				})
			}

			return nil, err
		}

		totalTokensUsed += aiResponse.Usage.TotalTokens

		// Telemetry: Record LLM response for visibility in distributed traces
		telemetry.AddSpanEvent(ctx, "llm.plan_generation.response",
			attribute.String("request_id", requestID),
			attribute.String("response", truncateString(aiResponse.Content, 2000)),
			attribute.Int("response_length", len(aiResponse.Content)),
			attribute.Int("prompt_tokens", aiResponse.Usage.PromptTokens),
			attribute.Int("completion_tokens", aiResponse.Usage.CompletionTokens),
			attribute.Int("total_tokens", aiResponse.Usage.TotalTokens),
			attribute.Int64("duration_ms", llmDuration.Milliseconds()),
			attribute.Int("attempt", attempt),
		)

		// Unified Metrics: Record successful AI request
		telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "plan_generation",
			float64(llmDuration.Milliseconds()), "success")
		// Record token usage (input and output separately)
		telemetry.RecordAITokens(telemetry.ModuleOrchestration, "plan_generation",
			"input", int64(aiResponse.Usage.PromptTokens))
		telemetry.RecordAITokens(telemetry.ModuleOrchestration, "plan_generation",
			"output", int64(aiResponse.Usage.CompletionTokens))

		// LLM Debug: Record successful interaction with full prompt and response
		model, provider := effectiveAIIdentity(invocationResult, aiResponse, nil)
		o.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:             "plan_generation",
			Timestamp:        llmStartTime,
			DurationMs:       llmDuration.Milliseconds(),
			Prompt:           effective.Prompt,
			SystemPrompt:     effective.SystemPrompt,
			Temperature:      effectiveAITemperature(effective, planOpts.Temperature),
			MaxTokens:        effectiveAIMaxTokens(effective, planOpts.MaxTokens),
			Model:            model,
			Provider:         provider,
			Response:         aiResponse.Content,
			PromptTokens:     aiResponse.Usage.PromptTokens,
			CompletionTokens: aiResponse.Usage.CompletionTokens,
			TotalTokens:      aiResponse.Usage.TotalTokens,
			Success:          true,
			Attempt:          attempt,
		})

		if o.logger != nil {
			o.logger.DebugWithContext(ctx, "LLM response received", map[string]interface{}{
				"operation":       "llm_response",
				"request_id":      requestID,
				"tokens_used":     aiResponse.Usage.TotalTokens,
				"response_length": len(aiResponse.Content),
				"attempt":         attempt,
			})
		}

		// Parse the LLM response into a plan
		plan, parseErr := o.parsePlan(aiResponse.Content)
		if parseErr == nil {
			// Collapse a terminal synthesis pseudo-step BEFORE the hallucination check below, so an
			// initial plan whose only remaining work is the final answer is accepted as a zero-step
			// terminal plan instead of bouncing on the pseudo-step's unregistered agent and burning a
			// hallucination-triggered regeneration. Mirrors the parse-time call in
			// generateContinuationPlan. Phase 1 has no prior executed steps → knownStepIDSet(nil, plan).
			o.normalizeTerminalSynthesisPlan(ctx, plan, knownStepIDSet(nil, plan), requestID)

			// Parse succeeded - optionally validate against allowed agents (hallucination detection)
			// Validation can be disabled via HallucinationValidationEnabled: false
			validationEnabled := true
			if o.config != nil && !o.config.HallucinationValidationEnabled {
				validationEnabled = false
				if o.logger != nil {
					o.logger.DebugWithContext(ctx, "Hallucination validation disabled by config", map[string]interface{}{
						"operation":  "hallucination_detection",
						"request_id": requestID,
						"reason":     "config.HallucinationValidationEnabled=false",
					})
				}
			}

			var hallucinatedAgent string
			var hallErr error
			hallStartTime := time.Now()

			if validationEnabled {
				hallucinatedAgent, hallErr = o.validatePlanAgainstAllowedAgents(ctx, plan, promptResult.AllowedAgents)
			}

			if hallErr != nil {
				// Determine max hallucination retries
				// Default to 0 retries if config is nil or retry is disabled
				maxHallRetries := 0
				if o.config != nil {
					if o.config.HallucinationRetryEnabled {
						maxHallRetries = o.config.HallucinationMaxRetries
					}
					// If disabled, maxHallRetries stays 0 - skip retry loop entirely
				}

				// Build allowed agents list for logging and error messages
				// Done outside retry loop since AllowedAgents doesn't change
				allowedList := make([]string, 0, len(promptResult.AllowedAgents))
				for name := range promptResult.AllowedAgents {
					allowedList = append(allowedList, name)
				}

				// Retry loop for hallucination recovery
				for hallRetry := 0; hallRetry < maxHallRetries; hallRetry++ {
					retryStartTime := time.Now()

					// Pattern 4 (Tracing): Record error on span FIRST (visible in Jaeger)
					telemetry.RecordSpanError(ctx, hallErr)

					// Pattern 6 (Tracing): Span event with request_id as FIRST attribute
					telemetry.AddSpanEvent(ctx, "llm.hallucination_detected",
						attribute.String("request_id", requestID), // FIRST attribute per Pattern 6
						attribute.String("hallucinated_agent", hallucinatedAgent),
						attribute.Int("allowed_agent_count", len(allowedList)),
						attribute.Int("hall_retry", hallRetry+1),
						attribute.Int("max_hall_retries", maxHallRetries),
						attribute.Int("attempt", attempt),
					)

					// Pattern 5 (Tracing): Counter with module label
					telemetry.Counter("plan_generation.hallucinations",
						"module", telemetry.ModuleOrchestration, // REQUIRED per Pattern 5
						"agent", hallucinatedAgent,
					)

					// Logging Patterns 1, 2, 3: nil check + operation + request_id + duration_ms
					if o.logger != nil {
						o.logger.WarnWithContext(ctx, "LLM hallucinated non-existent agent", map[string]interface{}{
							"operation":          "hallucination_detection", // REQUIRED: Pattern 2
							"request_id":         requestID,                 // REQUIRED: Pattern 3
							"hallucinated_agent": hallucinatedAgent,
							"allowed_agents":     allowedList,
							"attempt":            attempt,
							"hall_retry":         hallRetry + 1,
							"max_hall_retries":   maxHallRetries,
							"error":              hallErr.Error(),                          // REQUIRED: error field for warn/error logs
							"duration_ms":        time.Since(hallStartTime).Milliseconds(), // Time since hallucination first detected
						})
					}

					// LLM Debug Store: Record hallucination for production debugging (per ARCHITECTURE.md §9.9)
					if o.debugStore != nil {
						// Embed hallucination details in the error message since LLMInteraction has no Metadata field
						hallErrDetail := fmt.Sprintf("%s | hallucinated_agent=%s | allowed_agents=%v | hall_retry=%d",
							hallErr.Error(), hallucinatedAgent, allowedList, hallRetry+1)
						o.recordDebugInteraction(ctx, requestID, LLMInteraction{
							Type:      "hallucination_detection",
							Timestamp: time.Now(),
							Prompt:    promptResult.Prompt,
							Response:  aiResponse.Content,
							Success:   false,
							Error:     hallErrDetail,
							Attempt:   hallRetry + 1,
						})
					}

					// Enhanced Hallucination Retry Strategy (Fix 3 from BUG_LLM_HALLUCINATED_TOOL.md)
					// Instead of retrying with the same tool list, we:
					// 1. Extract context about what the LLM was trying to do
					// 2. Build an enhanced request with capability hints
					// 3. Re-run tiered selection (may find different/better tools)
					// 4. Prepend critical feedback to the new prompt

					// Step 1: Extract hallucination context (agent name, capability, instruction)
					hallCtx := extractHallucinationContext(plan, hallucinatedAgent)

					// Step 2: Build enhanced request with context for tiered selection
					// This is GENERIC - no domain-specific keyword mappings
					enhancedRequest := buildEnhancedRequestForRetry(request, hallCtx)

					// Step 3: Re-run tiered selection with enhanced request
					// This may discover tools that match the hallucinated capability
					retryPromptResult, retryPromptErr := o.buildPlanningPrompt(ctx, enhancedRequest)
					if retryPromptErr != nil {
						// Pattern 6 (Tracing): Span event with request_id as FIRST attribute
						telemetry.AddSpanEvent(ctx, "llm.hallucination_enhanced_retry_fallback",
							attribute.String("request_id", requestID), // FIRST attribute per Pattern 6
							attribute.String("error", retryPromptErr.Error()),
							attribute.String("hallucinated_agent", hallucinatedAgent),
							attribute.Int("hall_retry", hallRetry+1),
							attribute.String("fallback_reason", "enhanced_tiered_selection_failed"),
						)

						// Pattern 1, 2, 3 (Logging): Warn log for graceful degradation
						if o.logger != nil {
							o.logger.WarnWithContext(ctx, "Enhanced tiered selection failed, falling back to original prompt", map[string]interface{}{
								"operation":          "hallucination_retry", // REQUIRED: Pattern 2
								"request_id":         requestID,             // REQUIRED: Pattern 3
								"hall_retry":         hallRetry + 1,
								"hallucinated_agent": hallucinatedAgent,
								"error":              retryPromptErr.Error(),
								"fallback":           "original_prompt_result",
							})
						}
						// Fall back to original prompt result if enhanced retry fails
						retryPromptResult = promptResult
					}

					// Update allowed agents list from the NEW prompt (may have different tools)
					newAllowedList := make([]string, 0, len(retryPromptResult.AllowedAgents))
					for name := range retryPromptResult.AllowedAgents {
						newAllowedList = append(newAllowedList, name)
					}

					// Step 4: Build retry prompt with CRITICAL FEEDBACK FIRST
					capabilityHint := hallCtx.Capability
					if capabilityHint == "" {
						capabilityHint = hallCtx.AgentName
					}
					hallucinationFeedback := fmt.Sprintf(`CRITICAL ERROR - YOUR PREVIOUS PLAN WAS REJECTED:
You used agent '%s' which does NOT exist in the available agents list.

STRICT RULES FOR THIS RETRY:
1. You MUST ONLY use agents from the "Available Agents" section below
2. DO NOT invent, guess, or hallucinate any agent names
3. If you cannot fulfill the request with available agents, return a plan with ZERO steps
4. The capability you were trying to use ('%s') may be available under a DIFFERENT agent name - check the list carefully!

%s`, hallucinatedAgent, capabilityHint, retryPromptResult.Prompt)

					// Log the retry attempt
					if o.logger != nil {
						o.logger.DebugWithContext(ctx, "Retrying plan generation with enhanced tiered selection", map[string]interface{}{
							"operation":           "hallucination_retry",
							"request_id":          requestID,
							"hall_retry":          hallRetry + 1,
							"hallucinated_agent":  hallucinatedAgent,
							"hallucinated_cap":    hallCtx.Capability,
							"hallucinated_instr":  hallCtx.Instruction,
							"original_tool_count": len(allowedList),
							"new_tool_count":      len(newAllowedList),
							"prompt_length":       len(hallucinationFeedback),
						})
					}

					// Call LLM with the enhanced prompt (may have NEW tools from tiered selection).
					// Defer wrapper-side recording — the retry records its own
					// `plan_generation` typed interaction below.
					retryLLMStartTime := time.Now()
					retryPlanOpts := o.planAIOptions(0.2, promptResult.SystemPrompt)
					retryInvocation := aiInvocation{
						Purpose:        "planning",
						Prompt:         hallucinationFeedback,
						Options:        retryPlanOpts,
						SystemSource:   o.planPromptSystemSource(),
						DeferRecording: o.debugStore != nil,
					}
					retryInvocationResult, retryErr := invokeAI(ctx, o.aiClient, retryInvocation)
					var retryResponse *core.AIResponse
					if retryInvocationResult != nil {
						retryResponse = retryInvocationResult.Response
					}
					retryEffective := effectiveAIRequestForDebug(retryInvocationResult, retryInvocation)
					retryLLMDuration := time.Since(retryLLMStartTime)
					if retryErr == nil {
						core.RecordTokenUsage(ctx, "planning", retryResponse.Usage)
					}
					if retryErr != nil {
						// Pattern 4 (Tracing): Record regeneration error on span
						telemetry.RecordSpanError(ctx, retryErr)
						telemetry.AddSpanEvent(ctx, "llm.hallucination_regeneration_failed",
							attribute.String("request_id", requestID),
							attribute.String("error", retryErr.Error()),
							attribute.Int("hall_retry", hallRetry+1),
						)

						// Logging: Log regeneration failure with error field
						if o.logger != nil {
							o.logger.ErrorWithContext(ctx, "Plan regeneration failed during hallucination retry", map[string]interface{}{
								"operation":   "hallucination_retry",
								"request_id":  requestID,
								"hall_retry":  hallRetry + 1,
								"error":       retryErr.Error(), // REQUIRED: error field
								"duration_ms": time.Since(retryStartTime).Milliseconds(),
							})
						}

						// LLM Debug: Record failed hallucination retry plan generation
						retryErrModel, retryErrProvider := effectiveAIIdentity(retryInvocationResult, retryResponse, retryErr)
						o.recordDebugInteraction(ctx, requestID, LLMInteraction{
							Type:         "plan_generation",
							Timestamp:    retryLLMStartTime,
							DurationMs:   retryLLMDuration.Milliseconds(),
							Prompt:       retryEffective.Prompt,
							SystemPrompt: retryEffective.SystemPrompt,
							Temperature:  effectiveAITemperature(retryEffective, retryPlanOpts.Temperature),
							MaxTokens:    effectiveAIMaxTokens(retryEffective, retryPlanOpts.MaxTokens),
							Model:        retryErrModel,
							Provider:     retryErrProvider,
							Success:      false,
							Error:        fmt.Sprintf("hallucination_retry (attempt %d): %s", hallRetry+1, retryErr.Error()),
							Attempt:      attempt, // Keep original attempt, error indicates it's a hallucination retry
						})

						return nil, fmt.Errorf("plan regeneration failed: %w", retryErr)
					}

					// LLM Debug: Record successful hallucination retry plan generation
					retryModel, retryProvider := effectiveAIIdentity(retryInvocationResult, retryResponse, nil)
					o.recordDebugInteraction(ctx, requestID, LLMInteraction{
						Type:             "plan_generation",
						Timestamp:        retryLLMStartTime,
						DurationMs:       retryLLMDuration.Milliseconds(),
						Prompt:           retryEffective.Prompt,
						SystemPrompt:     retryEffective.SystemPrompt,
						Temperature:      effectiveAITemperature(retryEffective, retryPlanOpts.Temperature),
						MaxTokens:        effectiveAIMaxTokens(retryEffective, retryPlanOpts.MaxTokens),
						Model:            retryModel,
						Provider:         retryProvider,
						Response:         retryResponse.Content,
						PromptTokens:     retryResponse.Usage.PromptTokens,
						CompletionTokens: retryResponse.Usage.CompletionTokens,
						TotalTokens:      retryResponse.Usage.TotalTokens,
						Success:          true,
						Attempt:          attempt, // Original attempt number; hallRetry context in prompt
					})

					// Parse the retry response
					plan, retryErr = o.parsePlan(retryResponse.Content)
					if retryErr != nil {
						// Parse error on retry - log and continue to next retry attempt
						if o.logger != nil {
							o.logger.WarnWithContext(ctx, "Failed to parse retry plan response", map[string]interface{}{
								"operation":   "hallucination_retry",
								"request_id":  requestID,
								"hall_retry":  hallRetry + 1,
								"error":       retryErr.Error(),
								"duration_ms": time.Since(retryStartTime).Milliseconds(),
							})
						}
						// Continue to next retry - hallErr is still set from previous validation
						continue
					}

					// Normalize before re-validating, same as the first-parse path above.
					o.normalizeTerminalSynthesisPlan(ctx, plan, knownStepIDSet(nil, plan), requestID)

					// Record successful regeneration attempt
					telemetry.AddSpanEvent(ctx, "llm.hallucination_regeneration_complete",
						attribute.String("request_id", requestID),
						attribute.Int("hall_retry", hallRetry+1),
					)

					// Validate the regenerated plan against the NEW allowed agents
					// (retryPromptResult may have different tools from enhanced tiered selection)
					hallucinatedAgent, hallErr = o.validatePlanAgainstAllowedAgents(ctx, plan, retryPromptResult.AllowedAgents)
					if hallErr == nil {
						// Pattern 5 (Tracing): Counter for successful recovery
						telemetry.Counter("plan_generation.hallucination_recovered",
							"module", telemetry.ModuleOrchestration,
							"retries_used", strconv.Itoa(hallRetry+1),
						)
						telemetry.AddSpanEvent(ctx, "llm.hallucination_recovered",
							attribute.String("request_id", requestID),
							attribute.Int("retries_used", hallRetry+1),
						)

						// Logging: Log successful recovery
						if o.logger != nil {
							o.logger.InfoWithContext(ctx, "LLM hallucination recovered after retry", map[string]interface{}{
								"operation":    "hallucination_detection",
								"request_id":   requestID,
								"retries_used": hallRetry + 1,
								"duration_ms":  time.Since(hallStartTime).Milliseconds(),
								"status":       "recovered",
							})
						}
						break // Success!
					}
				}

				// Check if we exhausted retries
				if hallErr != nil {
					// Actionable error message per FRAMEWORK_DESIGN_PRINCIPLES.md
					finalErr := fmt.Errorf("LLM hallucinated agent '%s' after %d retries: %w "+
						"(verify the agent is registered in the discovery system and included in tiered selection)",
						hallucinatedAgent, maxHallRetries, hallErr)

					// Pattern 4 (Tracing): Record final error on span
					telemetry.RecordSpanError(ctx, finalErr)
					telemetry.AddSpanEvent(ctx, "llm.hallucination_unrecoverable",
						attribute.String("request_id", requestID),
						attribute.String("hallucinated_agent", hallucinatedAgent),
						attribute.Int("retries_exhausted", maxHallRetries),
					)

					// Pattern 5 (Tracing): Counter for unrecoverable hallucinations
					telemetry.Counter("plan_generation.hallucination_unrecoverable",
						"module", telemetry.ModuleOrchestration,
						"agent", hallucinatedAgent,
					)

					// Logging Patterns 1, 2, 3 + error field + duration_ms
					if o.logger != nil {
						o.logger.ErrorWithContext(ctx, "LLM hallucination unrecoverable after retries", map[string]interface{}{
							"operation":          "hallucination_detection", // REQUIRED: Pattern 2
							"request_id":         requestID,                 // REQUIRED: Pattern 3
							"hallucinated_agent": hallucinatedAgent,
							"allowed_agents":     allowedList, // Include allowed list for debugging
							"retries_exhausted":  maxHallRetries,
							"error":              hallErr.Error(), // REQUIRED: error field
							"duration_ms":        time.Since(hallStartTime).Milliseconds(),
							"status":             "unrecoverable",
						})
					}

					// Record metrics for unrecoverable failure
					telemetry.Histogram("plan_generation.duration_ms", float64(time.Since(planGenStart).Milliseconds()),
						"module", telemetry.ModuleOrchestration, "status", "error")
					telemetry.Counter("plan_generation.total",
						"module", telemetry.ModuleOrchestration, "status", "error")

					return nil, finalErr
				}
			}

			// Success - hallucination validation passed (or no agents to validate)
			if o.logger != nil {
				o.logger.InfoWithContext(ctx, "Plan generated successfully", map[string]interface{}{
					"operation":        "plan_generation",
					"request_id":       requestID,
					"plan_id":          plan.PlanID,
					"step_count":       len(plan.Steps),
					"total_time_ms":    time.Since(planGenStart).Milliseconds(),
					"tokens_used":      totalTokensUsed,
					"attempts_used":    attempt,
					"retries_required": attempt - 1,
				})
			}
			// Metrics: Record successful plan generation (orchestration-local)
			telemetry.Histogram("plan_generation.duration_ms", float64(time.Since(planGenStart).Milliseconds()),
				"module", telemetry.ModuleOrchestration, "status", "success")
			telemetry.Counter("plan_generation.total",
				"module", telemetry.ModuleOrchestration, "status", "success")
			return plan, nil
		}

		// Parse failed - check if we should retry
		lastParseErr = parseErr

		// Telemetry: Record parse failure
		willRetry := attempt < maxAttempts
		telemetry.AddSpanEvent(ctx, "llm.plan_generation.parse_error",
			attribute.String("request_id", requestID),
			attribute.String("error", parseErr.Error()),
			attribute.Int("attempt", attempt),
			attribute.Bool("will_retry", willRetry),
		)
		// Metrics: Record parse error counter (orchestration-local)
		willRetryStr := "false"
		if willRetry {
			willRetryStr = "true"
		}
		telemetry.Counter("plan_generation.parse_errors",
			"module", telemetry.ModuleOrchestration, "will_retry", willRetryStr)

		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "Plan parsing failed", map[string]interface{}{
				"operation":    "plan_parse_error",
				"request_id":   requestID,
				"error":        parseErr.Error(),
				"attempt":      attempt,
				"max_attempts": maxAttempts,
				"will_retry":   willRetry,
			})
		}

		// If we have retries left, build a new prompt with error feedback
		if willRetry {
			// Metrics: Record retry attempt (orchestration-local)
			telemetry.Counter("plan_generation.retries", "module", telemetry.ModuleOrchestration)
			promptResult, err = o.buildPlanningPromptWithParseError(ctx, promptResult, parseErr)
			if err != nil {
				// If we can't build the retry prompt, return the original parse error
				telemetry.Histogram("plan_generation.duration_ms", float64(time.Since(planGenStart).Milliseconds()),
					"module", telemetry.ModuleOrchestration, "status", "error")
				telemetry.Counter("plan_generation.total",
					"module", telemetry.ModuleOrchestration, "status", "error")
				return nil, lastParseErr
			}
		}
	}

	// All attempts exhausted
	if o.logger != nil {
		o.logger.ErrorWithContext(ctx, "Plan generation failed after all retries", map[string]interface{}{
			"operation":     "plan_generation_exhausted",
			"request_id":    requestID,
			"error":         lastParseErr.Error(),
			"attempts_made": maxAttempts,
			"total_tokens":  totalTokensUsed,
			"total_time_ms": time.Since(planGenStart).Milliseconds(),
		})
	}

	// Metrics: Record final plan generation failure after all retries (orchestration-local)
	telemetry.Histogram("plan_generation.duration_ms", float64(time.Since(planGenStart).Milliseconds()),
		"module", telemetry.ModuleOrchestration, "status", "error")
	telemetry.Counter("plan_generation.total",
		"module", telemetry.ModuleOrchestration, "status", "error")
	return nil, lastParseErr
}

// generateContinuationPlan generates the next phase of an iterative plan,
// incorporating results from previously executed phases.
// Follows the same structure as generateExecutionPlan(): LLM call → parse → validate → retry.
func (o *AIOrchestrator) generateContinuationPlan(
	ctx context.Context,
	request string,
	requestID string,
	completedResults map[string]*StepResult,
	executedStepIDs []string,
	continuationNote string,
	phaseNumber int,
) (*RoutingPlan, error) {
	var err error
	ctx, err = prepareOrchestrationBoundary(ctx, boundaryContinuationPlanning)
	if err != nil {
		return nil, fmt.Errorf("prepare continuation planning boundary: %w", err)
	}
	planGenStart := time.Now()

	if o.logger != nil {
		o.logger.DebugWithContext(ctx, "Starting continuation plan generation", map[string]interface{}{
			"operation":           "continuation_plan_generation_start",
			"request_id":          requestID,
			"phase":               phaseNumber,
			"completed_steps":     len(completedResults),
			"executed_step_count": len(executedStepIDs),
		})
	}

	if o.aiClient == nil {
		return nil, fmt.Errorf("AI client not configured")
	}

	// Build continuation prompt with accumulated phase context
	promptResult, err := o.buildContinuationPrompt(
		ctx, request, completedResults, executedStepIDs,
		continuationNote, phaseNumber,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build continuation prompt: %w", err)
	}

	if o.logger != nil {
		o.logger.DebugWithContext(ctx, "Continuation prompt constructed", map[string]interface{}{
			"operation":             "prompt_construction",
			"request_id":            requestID,
			"phase":                 phaseNumber,
			"prompt_length":         len(promptResult.Prompt),
			"completed_steps_count": len(completedResults),
		})
	}

	// Determine max attempts: 1 initial + retries (if enabled)
	maxAttempts := 1
	if o.config != nil && o.config.PlanParseRetryEnabled {
		maxAttempts = 1 + o.config.PlanParseMaxRetries
	}

	var lastParseErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		planOpts := o.planAIOptions(0.2, promptResult.SystemPrompt)
		if o.logger != nil {
			o.logger.DebugWithContext(ctx, "Calling LLM for continuation plan", map[string]interface{}{
				"operation":   "llm_call",
				"request_id":  requestID,
				"phase":       phaseNumber,
				"temperature": 0.2,
				"max_tokens":  planOpts.MaxTokens,
				"attempt":     attempt,
			})
		}

		// Span event: pre-LLM call
		contPlanEventAttrs := []attribute.KeyValue{
			attribute.String("request_id", requestID),
			attribute.String("prompt", truncateString(promptResult.Prompt, 2000)),
			attribute.Int("prompt_length", len(promptResult.Prompt)),
			attribute.Float64("temperature", 0.2),
			attribute.Int("max_tokens", planOpts.MaxTokens),
			attribute.Int("attempt", attempt),
			attribute.Int("phase_number", phaseNumber),
		}
		if planOpts.Model != "" {
			contPlanEventAttrs = append(contPlanEventAttrs, attribute.String("model", planOpts.Model))
		}
		telemetry.AddSpanEvent(ctx, "llm.continuation_plan_generation.request", contPlanEventAttrs...)

		ctx := telemetry.WithBaggage(ctx, "ai.purpose", "continuation_plan_generation")
		// Defer wrapper-side recording — we emit the authoritative
		// `continuation_plan_generation` typed LLMInteraction below.
		llmStartTime := time.Now()
		invocation := aiInvocation{
			Purpose:        "continuation-planning",
			Prompt:         promptResult.Prompt,
			Options:        planOpts,
			SystemSource:   o.planPromptSystemSource(),
			DeferRecording: o.debugStore != nil,
		}
		invocationResult, err := invokeAI(ctx, o.aiClient, invocation)
		var aiResponse *core.AIResponse
		if invocationResult != nil {
			aiResponse = invocationResult.Response
		}
		effective := effectiveAIRequestForDebug(invocationResult, invocation)
		llmDuration := time.Since(llmStartTime)
		if err == nil {
			core.RecordTokenUsage(ctx, "planning", aiResponse.Usage)
		}

		if err != nil {
			// Span event: LLM error
			telemetry.AddSpanEvent(ctx, "llm.continuation_plan_generation.error",
				attribute.String("request_id", requestID),
				attribute.String("error", err.Error()),
				attribute.Int64("duration_ms", llmDuration.Milliseconds()),
				attribute.Int("attempt", attempt),
				attribute.Int("phase_number", phaseNumber),
			)

			// Unified Metrics: Record failed AI request
			telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "continuation_plan_generation",
				float64(llmDuration.Milliseconds()), "error")
			telemetry.Histogram("continuation_plan_generation.duration_ms", float64(llmDuration.Milliseconds()),
				"module", telemetry.ModuleOrchestration, "status", "error")
			telemetry.Counter("continuation_plan_generation.total",
				"module", telemetry.ModuleOrchestration, "status", "error")

			// Debug store: record the actual prepared request on failure.
			errModel, errProvider := effectiveAIIdentity(invocationResult, aiResponse, err)
			o.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type:            "continuation_plan_generation",
				PhaseNumber:     phaseNumber,
				Timestamp:       llmStartTime,
				DurationMs:      llmDuration.Milliseconds(),
				Prompt:          effective.Prompt,
				SystemPrompt:    effective.SystemPrompt,
				Temperature:     effectiveAITemperature(effective, planOpts.Temperature),
				MaxTokens:       effectiveAIMaxTokens(effective, planOpts.MaxTokens),
				Model:           errModel,
				Provider:        errProvider,
				Success:         false,
				Error:           err.Error(),
				Attempt:         attempt,
				CallDescription: fmt.Sprintf("Phase %d continuation plan generation", phaseNumber),
			})

			if o.logger != nil {
				o.logger.ErrorWithContext(ctx, "Continuation plan LLM call failed", map[string]interface{}{
					"operation":   "continuation_plan_generation_exhausted",
					"request_id":  requestID,
					"phase":       phaseNumber,
					"error":       err.Error(),
					"attempts":    attempt,
					"duration_ms": llmDuration.Milliseconds(),
				})
			}
			return nil, fmt.Errorf("continuation plan LLM call failed: %w", err)
		}

		if o.logger != nil {
			o.logger.DebugWithContext(ctx, "LLM continuation response received", map[string]interface{}{
				"operation":   "llm_response",
				"request_id":  requestID,
				"phase":       phaseNumber,
				"duration_ms": llmDuration.Milliseconds(),
			})
		}

		// Span event: LLM success
		responseContent := ""
		if aiResponse != nil {
			responseContent = aiResponse.Content
		}
		telemetry.AddSpanEvent(ctx, "llm.continuation_plan_generation.response",
			attribute.String("request_id", requestID),
			attribute.String("response", truncateString(responseContent, 2000)),
			attribute.Int("response_length", len(responseContent)),
			attribute.Int("prompt_tokens", aiResponse.Usage.PromptTokens),
			attribute.Int("completion_tokens", aiResponse.Usage.CompletionTokens),
			attribute.Int("total_tokens", aiResponse.Usage.TotalTokens),
			attribute.Int64("duration_ms", llmDuration.Milliseconds()),
			attribute.Int("attempt", attempt),
			attribute.Int("phase_number", phaseNumber),
		)

		// Unified Metrics: Record successful AI request
		telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "continuation_plan_generation",
			float64(llmDuration.Milliseconds()), "success")
		// Record token usage (input and output separately)
		telemetry.RecordAITokens(telemetry.ModuleOrchestration, "continuation_plan_generation",
			"input", int64(aiResponse.Usage.PromptTokens))
		telemetry.RecordAITokens(telemetry.ModuleOrchestration, "continuation_plan_generation",
			"output", int64(aiResponse.Usage.CompletionTokens))

		// Debug store: record successful LLM call
		model, provider := effectiveAIIdentity(invocationResult, aiResponse, nil)
		o.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:             "continuation_plan_generation",
			PhaseNumber:      phaseNumber,
			Timestamp:        llmStartTime,
			DurationMs:       llmDuration.Milliseconds(),
			Prompt:           effective.Prompt,
			SystemPrompt:     effective.SystemPrompt,
			Temperature:      effectiveAITemperature(effective, planOpts.Temperature),
			MaxTokens:        effectiveAIMaxTokens(effective, planOpts.MaxTokens),
			Response:         responseContent,
			PromptTokens:     aiResponse.Usage.PromptTokens,
			CompletionTokens: aiResponse.Usage.CompletionTokens,
			TotalTokens:      aiResponse.Usage.TotalTokens,
			Model:            model,
			Provider:         provider,
			Success:          true,
			Attempt:          attempt,
			CallDescription:  fmt.Sprintf("Phase %d continuation plan generation", phaseNumber),
		})

		// Parse the plan
		plan, parseErr := o.parsePlan(responseContent)
		if parseErr != nil {
			lastParseErr = parseErr

			// Span event: parse error
			willRetry := attempt < maxAttempts
			willRetryStr := "false"
			if willRetry {
				willRetryStr = "true"
			}

			telemetry.AddSpanEvent(ctx, "llm.continuation_plan_generation.parse_error",
				attribute.String("request_id", requestID),
				attribute.String("error", parseErr.Error()),
				attribute.Int("attempt", attempt),
				attribute.Bool("will_retry", willRetry),
				attribute.Int("phase_number", phaseNumber),
			)
			telemetry.Counter("continuation_plan_generation.parse_errors",
				"module", telemetry.ModuleOrchestration, "will_retry", willRetryStr)

			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Continuation plan parsing failed", map[string]interface{}{
					"operation":  "plan_parse_error",
					"request_id": requestID,
					"phase":      phaseNumber,
					"error":      parseErr.Error(),
					"will_retry": willRetry,
					"attempt":    attempt,
				})
			}

			if willRetry {
				telemetry.Counter("continuation_plan_generation.retries",
					"module", telemetry.ModuleOrchestration)
				// Rebuild prompt with parse error feedback for retry
				promptResult, err = o.buildPlanningPromptWithParseError(ctx, promptResult, parseErr)
				if err != nil {
					return nil, fmt.Errorf("failed to build continuation retry prompt: %w", err)
				}
				continue
			}
			break
		}

		// Fix A2 (Finding 1): collapse a terminal synthesis pseudo-step BEFORE the internal
		// hallucination/structural checks below — otherwise the first continuation generation
		// bounces on the pseudo-step's unregistered agent and burns a hallucination-triggered
		// regeneration before the phase-loop normalizer ever sees the plan. This lets the first
		// terminal synthesis plan be accepted as a zero-step terminal plan. The phase-loop fixpoint
		// normalizes again on regenerated plans (idempotent).
		o.normalizeTerminalSynthesisPlan(ctx, plan, knownStepIDSet(executedStepIDs, plan), requestID)

		// Hallucination validation
		if o.config != nil && o.config.HallucinationValidationEnabled && promptResult.AllowedAgents != nil {
			hallucinatedAgent, hallErr := o.validatePlanAgainstAllowedAgents(ctx, plan, promptResult.AllowedAgents)
			if hallErr != nil {
				if o.logger != nil {
					o.logger.WarnWithContext(ctx, "LLM hallucinated non-existent agent (continuation)", map[string]interface{}{
						"operation":          "hallucination_detection",
						"request_id":         requestID,
						"phase":              phaseNumber,
						"hallucinated_agent": hallucinatedAgent,
					})
				}

				telemetry.AddSpanEvent(ctx, "llm.hallucination_detected",
					attribute.String("request_id", requestID),
					attribute.String("hallucinated_agent", hallucinatedAgent),
					attribute.Int("phase_number", phaseNumber),
				)

				// Retry with hallucination feedback if retries are enabled
				if o.config.HallucinationRetryEnabled {
					retryPlan, retryErr := o.regenerateContinuationPlan(
						ctx, request, requestID, completedResults, executedStepIDs,
						continuationNote, phaseNumber, hallErr,
						plan.Terminal,
					)
					if retryErr == nil {
						retryPlan.PhaseNumber = phaseNumber
						return retryPlan, nil
					}
				}
				return nil, hallErr
			}
		}

		// Structural validation
		if err := o.validatePlan(plan); err != nil {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Continuation plan structural validation failed", map[string]interface{}{
					"operation":  "plan_validation",
					"request_id": requestID,
					"phase":      phaseNumber,
					"error":      err.Error(),
				})
			}
			return nil, fmt.Errorf("continuation plan structural validation failed: %w", err)
		}

		// Step ID conflict validation
		if err := validateNoStepIDConflicts(plan, executedStepIDs); err != nil {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Continuation plan has step ID conflicts", map[string]interface{}{
					"operation":  "step_id_conflict",
					"request_id": requestID,
					"phase":      phaseNumber,
					"error":      err.Error(),
				})
			}
			// Try to regenerate with conflict feedback
			retryPlan, retryErr := o.regenerateContinuationPlan(
				ctx, request, requestID, completedResults, executedStepIDs,
				continuationNote, phaseNumber, err,
				plan.Terminal,
			)
			if retryErr != nil {
				return nil, fmt.Errorf("step ID conflicts after retry: %w", retryErr)
			}
			plan = retryPlan
		}

		// Success
		plan.PhaseNumber = phaseNumber
		planDuration := time.Since(planGenStart)

		if o.logger != nil {
			o.logger.InfoWithContext(ctx, "Continuation plan generated successfully", map[string]interface{}{
				"operation":   "continuation_plan_generation_complete",
				"request_id":  requestID,
				"phase":       phaseNumber,
				"plan_id":     plan.PlanID,
				"step_count":  len(plan.Steps),
				"terminal":    plan.IsTerminal(),
				"duration_ms": planDuration.Milliseconds(),
			})
		}

		telemetry.Histogram("continuation_plan_generation.duration_ms", float64(planDuration.Milliseconds()),
			"module", telemetry.ModuleOrchestration, "status", "success")
		telemetry.Counter("continuation_plan_generation.total",
			"module", telemetry.ModuleOrchestration, "status", "success")

		return plan, nil
	}

	// All retries exhausted
	if o.logger != nil {
		o.logger.ErrorWithContext(ctx, "Continuation plan generation failed after all retries", map[string]interface{}{
			"operation":   "continuation_plan_generation_exhausted",
			"request_id":  requestID,
			"phase":       phaseNumber,
			"error":       lastParseErr.Error(),
			"attempts":    maxAttempts,
			"duration_ms": time.Since(planGenStart).Milliseconds(),
		})
	}

	telemetry.Histogram("continuation_plan_generation.duration_ms", float64(time.Since(planGenStart).Milliseconds()),
		"module", telemetry.ModuleOrchestration, "status", "error")
	telemetry.Counter("continuation_plan_generation.total",
		"module", telemetry.ModuleOrchestration, "status", "error")
	return nil, lastParseErr
}

// regenerateContinuationPlan retries plan generation for continuation phases,
// preserving accumulated phase context in the retry prompt (unlike regeneratePlan
// which rebuilds from the initial planning prompt and loses continuation state).
// originalTerminal preserves the terminal value from the plan that failed validation,
// preventing the LLM from conservatively flipping terminal to false during regeneration.
func (o *AIOrchestrator) regenerateContinuationPlan(
	ctx context.Context,
	request string,
	requestID string,
	completedResults map[string]*StepResult,
	executedStepIDs []string,
	continuationNote string,
	phaseNumber int,
	validationErr error,
	originalTerminal *bool, // preserve terminal from the original plan
) (*RoutingPlan, error) {
	var err error
	ctx, err = prepareOrchestrationBoundary(ctx, boundaryRegeneration)
	if err != nil {
		return nil, fmt.Errorf("prepare continuation regeneration boundary: %w", err)
	}
	if o.aiClient == nil {
		return nil, fmt.Errorf("AI client not configured for plan regeneration")
	}

	promptResult, err := o.buildContinuationPrompt(
		ctx, request, completedResults, executedStepIDs,
		continuationNote, phaseNumber,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build continuation retry prompt: %w", err)
	}
	validationFeedback, err := prepareValidationFeedback(ctx, promptRegeneration, validationErr)
	if err != nil {
		return nil, fmt.Errorf("prepare continuation regeneration feedback: %w", err)
	}

	// Build terminal preservation instruction (Issue 9A-3)
	terminalInstruction := ""
	if originalTerminal != nil {
		terminalInstruction = fmt.Sprintf(
			"\n\nThe original plan set \"terminal\": %t. "+
				"Preserve this value — the validation error is about plan structure, "+
				"not about whether more phases are needed.",
			*originalTerminal,
		)
	}

	prompt := fmt.Sprintf(
		"%s\n\nThe previous continuation plan failed validation with error: %s%s\n\n"+
			"Please generate a corrected plan that addresses this error.",
		promptResult.Prompt, validationFeedback, terminalInstruction,
	)

	// Span event: pre-LLM call
	contRegenEventAttrs := []attribute.KeyValue{
		attribute.String("request_id", requestID),
		attribute.Int("prompt_length", len(prompt)),
		attribute.Int("phase_number", phaseNumber),
		attribute.String("validation_error", truncateString(validationErr.Error(), 200)),
	}
	regenOpts := o.planAIOptions(0.2, promptResult.SystemPrompt)
	if regenOpts.Model != "" {
		contRegenEventAttrs = append(contRegenEventAttrs, attribute.String("model", regenOpts.Model))
	}
	telemetry.AddSpanEvent(ctx, "llm.continuation_plan_regeneration.request", contRegenEventAttrs...)

	// Defer wrapper-side recording — we emit the authoritative
	// `continuation_plan_regeneration` typed LLMInteraction below.
	llmStartTime := time.Now()

	invocation := aiInvocation{
		Purpose:        "continuation-planning",
		Prompt:         prompt,
		Options:        regenOpts,
		SystemSource:   o.planPromptSystemSource(),
		DeferRecording: o.debugStore != nil,
	}
	invocationResult, err := invokeAI(ctx, o.aiClient, invocation)
	var aiResponse *core.AIResponse
	if invocationResult != nil {
		aiResponse = invocationResult.Response
	}
	effective := effectiveAIRequestForDebug(invocationResult, invocation)

	llmDuration := time.Since(llmStartTime)
	if err == nil {
		core.RecordTokenUsage(ctx, "planning", aiResponse.Usage)
	}

	if err != nil {
		// Span event + metrics: failed regeneration
		telemetry.AddSpanEvent(ctx, "llm.continuation_plan_regeneration.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.Int64("duration_ms", llmDuration.Milliseconds()),
			attribute.Int("phase_number", phaseNumber),
		)
		telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "continuation_plan_regeneration",
			float64(llmDuration.Milliseconds()), "error")
		telemetry.Histogram("continuation_plan_regeneration.duration_ms", float64(llmDuration.Milliseconds()),
			"module", telemetry.ModuleOrchestration, "status", "error")
		telemetry.Counter("continuation_plan_regeneration.total",
			"module", telemetry.ModuleOrchestration, "status", "error")

		// Debug store: record failed LLM call
		errModel, errProvider := effectiveAIIdentity(invocationResult, aiResponse, err)
		o.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:            "continuation_plan_regeneration",
			PhaseNumber:     phaseNumber,
			Timestamp:       llmStartTime,
			DurationMs:      llmDuration.Milliseconds(),
			Prompt:          effective.Prompt,
			SystemPrompt:    effective.SystemPrompt,
			Temperature:     effectiveAITemperature(effective, regenOpts.Temperature),
			MaxTokens:       effectiveAIMaxTokens(effective, regenOpts.MaxTokens),
			Model:           errModel,
			Provider:        errProvider,
			Success:         false,
			Error:           err.Error(),
			Attempt:         1,
			CallDescription: fmt.Sprintf("Phase %d continuation plan REGENERATION (trigger: %s)", phaseNumber, truncateString(validationErr.Error(), 200)),
		})

		if o.logger != nil {
			o.logger.ErrorWithContext(ctx, "Continuation plan regeneration LLM call failed", map[string]interface{}{
				"operation":        "continuation_plan_regeneration_failed",
				"request_id":       requestID,
				"phase":            phaseNumber,
				"error":            err.Error(),
				"duration_ms":      llmDuration.Milliseconds(),
				"validation_error": truncateString(validationErr.Error(), 200),
			})
		}
		return nil, fmt.Errorf("continuation plan retry LLM call failed: %w", err)
	}

	if o.logger != nil {
		o.logger.DebugWithContext(ctx, "LLM continuation regeneration response received", map[string]interface{}{
			"operation":   "llm_regeneration_response",
			"request_id":  requestID,
			"phase":       phaseNumber,
			"duration_ms": llmDuration.Milliseconds(),
		})
	}

	// Span event + metrics: successful regeneration
	responseContent := ""
	if aiResponse != nil {
		responseContent = aiResponse.Content
	}
	telemetry.AddSpanEvent(ctx, "llm.continuation_plan_regeneration.response",
		attribute.String("request_id", requestID),
		attribute.String("response", truncateString(responseContent, 2000)),
		attribute.Int("response_length", len(responseContent)),
		attribute.Int("prompt_tokens", aiResponse.Usage.PromptTokens),
		attribute.Int("completion_tokens", aiResponse.Usage.CompletionTokens),
		attribute.Int("total_tokens", aiResponse.Usage.TotalTokens),
		attribute.Int64("duration_ms", llmDuration.Milliseconds()),
		attribute.Int("phase_number", phaseNumber),
	)
	telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "continuation_plan_regeneration",
		float64(llmDuration.Milliseconds()), "success")
	telemetry.RecordAITokens(telemetry.ModuleOrchestration, "continuation_plan_regeneration",
		"input", int64(aiResponse.Usage.PromptTokens))
	telemetry.RecordAITokens(telemetry.ModuleOrchestration, "continuation_plan_regeneration",
		"output", int64(aiResponse.Usage.CompletionTokens))
	telemetry.Histogram("continuation_plan_regeneration.duration_ms", float64(llmDuration.Milliseconds()),
		"module", telemetry.ModuleOrchestration, "status", "success")
	telemetry.Counter("continuation_plan_regeneration.total",
		"module", telemetry.ModuleOrchestration, "status", "success")

	// Debug store: record successful LLM call
	model, provider := effectiveAIIdentity(invocationResult, aiResponse, nil)
	o.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "continuation_plan_regeneration",
		PhaseNumber:      phaseNumber,
		Timestamp:        llmStartTime,
		DurationMs:       llmDuration.Milliseconds(),
		Prompt:           effective.Prompt,
		SystemPrompt:     effective.SystemPrompt,
		Temperature:      effectiveAITemperature(effective, regenOpts.Temperature),
		MaxTokens:        effectiveAIMaxTokens(effective, regenOpts.MaxTokens),
		Response:         aiResponse.Content,
		PromptTokens:     aiResponse.Usage.PromptTokens,
		CompletionTokens: aiResponse.Usage.CompletionTokens,
		TotalTokens:      aiResponse.Usage.TotalTokens,
		Model:            model,
		Provider:         provider,
		Success:          true,
		Attempt:          1,
		CallDescription:  fmt.Sprintf("Phase %d continuation plan REGENERATION (trigger: %s)", phaseNumber, truncateString(validationErr.Error(), 200)),
	})

	plan, err := o.parsePlan(aiResponse.Content)
	if err != nil {
		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "Regenerated continuation plan parsing failed", map[string]interface{}{
				"operation":        "continuation_plan_regeneration_parse_error",
				"request_id":       requestID,
				"phase":            phaseNumber,
				"error":            err.Error(),
				"response_preview": truncateString(aiResponse.Content, 500),
			})
		}
		return nil, fmt.Errorf("continuation plan retry parse failed: %w", err)
	}

	if validateErr := validateNoStepIDConflicts(plan, executedStepIDs); validateErr != nil {
		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "Regenerated continuation plan still has step ID conflicts", map[string]interface{}{
				"operation":  "continuation_plan_regeneration_step_id_conflict",
				"request_id": requestID,
				"phase":      phaseNumber,
				"error":      validateErr.Error(),
			})
		}
		return nil, fmt.Errorf("continuation plan retry still has conflicts: %w", validateErr)
	}

	if o.logger != nil {
		o.logger.InfoWithContext(ctx, "Continuation plan regeneration succeeded", map[string]interface{}{
			"operation":   "continuation_plan_regeneration_complete",
			"request_id":  requestID,
			"phase":       phaseNumber,
			"plan_id":     plan.PlanID,
			"step_count":  len(plan.Steps),
			"terminal":    plan.IsTerminal(),
			"duration_ms": llmDuration.Milliseconds(),
		})
	}

	plan.PhaseNumber = phaseNumber
	return plan, nil
}

// validateNoStepIDConflicts checks that a continuation plan doesn't reuse step IDs
// from previously executed phases.
func validateNoStepIDConflicts(plan *RoutingPlan, executedStepIDs []string) error {
	existing := make(map[string]bool, len(executedStepIDs))
	for _, id := range executedStepIDs {
		existing[id] = true
	}
	for _, step := range plan.Steps {
		if existing[step.StepID] {
			return fmt.Errorf("continuation plan contains duplicate step ID %q (already executed)", step.StepID)
		}
	}
	return nil
}

// buildPlanningPrompt constructs the prompt for the LLM using capability provider
// and optional PromptBuilder for customization.
// Returns PlanningPromptResult containing both the prompt and allowed agents for validation.
// Note: No child span is created here so that tiered_selection and plan_generation
// events are all recorded on the parent orchestrator span for unified visibility.
// planningContext holds the shared planning infrastructure used by both
// initial and continuation prompt builders. It separates the reusable parts
// (what tools exist, what the response format is) from the per-prompt framing
// (how the query is presented, what context is provided).
// Unexported: internal to the orchestrator, not a public API commitment.
type planningContext struct {
	// CapabilityCatalog is the "Available Agents and Capabilities:\n..." text block.
	// Built from tiered agent selection: includes agent names, namespaces,
	// capabilities, parameter schemas.
	CapabilityCatalog string

	// FormatRules contains ALL shared prompt rules concatenated:
	// - JSON response format rules (schema of the plan JSON)
	// - Parameter type rules (how to specify string/number/boolean params)
	// - Agent name rules (no hallucinated agents)
	// - ITERATIVE PLANNING instructions (terminal field, when to use it)
	// - "Important" constraint rules
	// - Critical format rules (JSON only, no markdown)
	FormatRules string

	// AllowedAgents maps discovered agent names → true for hallucination validation.
	// Passed to validatePlanAgainstAllowedAgents() after plan parsing.
	AllowedAgents map[string]bool
}

// buildDefaultFormatRules generates the shared format rules text used by both initial
// and continuation prompts. Unlike the previous const, this embeds budget-aware
// iterative planning instructions via BuildIterativePlanningInstructions.
// See BUG_PHASE3_SKIPPED_EXECUTION.md Change 2D.
func buildDefaultFormatRules(iterConfig *IterativePlanConfig) string {
	iterativeInstructions := BuildIterativePlanningInstructions(iterConfig)
	if iterativeInstructions != "" {
		iterativeInstructions = "\n" + iterativeInstructions + "\n"
	}

	return `Create an execution plan in JSON format with the following structure:
{
  "plan_id": "unique-id",
  "original_request": "the user request",
  "mode": "autonomous",
  "terminal": true,
  "steps": [
    {
      "step_id": "step-1",
      "agent_name": "agent-name-from-catalog",
      "namespace": "default",
      "instruction": "specific instruction for this agent",
      "depends_on": [],
      "metadata": {
        "capability": "capability-name",
        "parameters": {
          "string_param": "text value",
          "number_param": 42.5,
          "integer_param": 10,
          "boolean_param": true
        }
      }
    }
  ]
}

<type_rules>
- "number" or "float64" parameters use JSON numbers: 35.6897
- "integer" or "int" parameters use JSON integers: 10
- "boolean" or "bool" parameters use JSON booleans: true
- "string" parameters use JSON strings: "value"
</type_rules>

<constraints>
1. Use only agent_name values that appear in the available agents list — unlisted agents cause plan rejection
2. Match parameter names and types to each capability's schema
3. Order steps by dependencies
4. Template references are always quoted strings: "{{step-1.response.data.field}}"
5. If no available agent can fulfill the request, return a plan with zero steps
6. Write specific, actionable instructions for each step
7. Include steps for every part of the user's request
</constraints>
` + iterativeInstructions + `
Return a JSON execution plan. Output raw JSON — no markdown, no code blocks. Start with { and end with }.`
}

// buildplanningContext extracts the shared planning infrastructure:
// agent discovery (with tiered selection), JSON format rules, parameter type
// rules, template syntax rules, iterative planning instructions, and constraint
// rules. Both buildPlanningPrompt() and buildContinuationPrompt() call this,
// composing their own framing around the shared context.
func (o *AIOrchestrator) buildplanningContext(
	ctx context.Context,
	request string,
	phaseContext map[string]interface{}, // nil for Phase 1, populated for Phase 2+
) (*planningContext, error) {
	// Check if capability provider is available
	if o.capabilityProvider == nil {
		return nil, fmt.Errorf("capability provider not configured")
	}

	// Get capabilities from provider
	// Note: TieredCapabilityProvider adds span events to the current (parent) span
	// Returns CapabilityResult with both formatted info AND agent names (no regex needed)
	capabilityResult, err := o.capabilityProvider.GetCapabilities(ctx, request, phaseContext)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		return nil, fmt.Errorf("failed to get capabilities: %w", err)
	}

	// Defensive check: ensure provider returned a valid result
	if capabilityResult == nil {
		return nil, fmt.Errorf("capability provider returned nil result")
	}

	// Build allowedAgents map directly from structured agent names.
	// No regex parsing needed - agent names come directly from the capability provider.
	// Agent names are already normalized to lowercase by the provider, but we apply
	// ToLower() defensively for external providers that may not follow the convention.
	allowedAgents := make(map[string]bool, len(capabilityResult.AgentNames))
	for _, name := range capabilityResult.AgentNames {
		allowedAgents[strings.ToLower(name)] = true
	}

	if o.logger != nil {
		// Include a preview of capability info to help debug issues
		capabilityPreview := capabilityResult.FormattedInfo
		if len(capabilityPreview) > 500 {
			capabilityPreview = capabilityPreview[:500] + "...[truncated]"
		}
		o.logger.DebugWithContext(ctx, "Capability information retrieved", map[string]interface{}{
			"operation":          "capability_query",
			"capability_size":    len(capabilityResult.FormattedInfo),
			"capability_preview": capabilityPreview,
			"provider_type":      o.config.CapabilityProviderType,
			"allowed_agents":     len(allowedAgents),
			"agent_names":        capabilityResult.AgentNames, // Direct from provider, no regex
		})
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.Int("capability_info_size", len(capabilityResult.FormattedInfo)),
		attribute.Int("allowed_agents_count", len(allowedAgents)),
	)

	return &planningContext{
		CapabilityCatalog: capabilityResult.FormattedInfo,
		FormatRules:       buildDefaultFormatRules(&o.config.IterativePlanning),
		AllowedAgents:     allowedAgents,
	}, nil
}

func (o *AIOrchestrator) buildPlanningPrompt(ctx context.Context, request string) (*PlanningPromptResult, error) {
	planCtx, err := o.buildplanningContext(ctx, request, nil)
	if err != nil {
		return nil, err
	}
	input, err := preparePromptInput(ctx, promptInitialPlan, PromptInput{
		CapabilityInfo: planCtx.CapabilityCatalog,
		Request:        request,
		AgentName:      o.config.Name,
		Metadata:       core.GetPipelineEnrichments(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("prepare planning prompt input: %w", err)
	}

	// Use PromptBuilder if available (Layer 1-3 customization)
	if o.promptBuilder != nil {
		prompt, err := o.promptBuilder.BuildPlanningPrompt(ctx, input)
		if err != nil {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "PromptBuilder failed, falling back to default prompt", map[string]interface{}{
					"operation": "prompt_builder_fallback",
					"error":     err.Error(),
				})
			}
			// Fall through to default prompt
		} else {
			telemetry.SetSpanAttributes(ctx, attribute.Bool("prompt_builder_used", true))
			return &PlanningPromptResult{
				Prompt:        prompt,
				AllowedAgents: planCtx.AllowedAgents,
				SystemPrompt:  o.buildSystemPromptFromInput(ctx, input),
			}, nil
		}
	}

	// Default hardcoded prompt (backwards compatibility)
	// Build agent_memory section from pipeline enrichments (if any hooks injected context).
	// This enables memory hooks, RAG hooks, and conversation history hooks to reach the LLM
	// through the default prompt without requiring a custom PromptBuilder.
	agentCoordinationSection := ""
	userProfileSection := ""
	agentMemorySection := ""
	conversationHistorySection := ""
	contextPrecedenceSection := ""
	enrichments := input.Metadata
	if len(enrichments) > 0 {
		if coordCtx, ok := enrichments[core.EnrichmentActivityCoordination]; ok {
			if coordStr, isStr := coordCtx.(string); isStr && coordStr != "" {
				agentCoordinationSection = fmt.Sprintf("\n<agent_coordination>\n%s\n</agent_coordination>\n", coordStr)
			}
		}
		// User profile from UserMemoryEnrichmentHook (per-user private facts)
		if userProfile, ok := enrichments[core.EnrichmentUserProfile]; ok {
			if profileStr, isStr := userProfile.(string); isStr && profileStr != "" {
				userProfileSection = "\n" + profileStr + "\n"
			}
		}
		if ragCtx, ok := enrichments[core.EnrichmentRAGContext]; ok {
			if ragStr, isStr := ragCtx.(string); isStr && ragStr != "" {
				agentMemorySection = fmt.Sprintf("\n<agent_memory>\n%s\n</agent_memory>\n", ragStr)
			}
		}
		if convHistory, ok := enrichments[core.EnrichmentConversationHistory]; ok {
			if convStr, isStr := convHistory.(string); isStr && convStr != "" {
				conversationHistorySection = fmt.Sprintf("\n<conversation_history>\n%s\n</conversation_history>\n", convStr)
			}
		}
		// Precedence rule: emitted between the enrichments and <user_request>
		// so the planner reads it immediately before the live turn. Mirrors
		// the placement DefaultPromptBuilder uses on the primary path.
		var precSb strings.Builder
		writeContextPrecedence(ctx, &precSb, enrichments, PromptKindPlanningFallback)
		contextPrecedenceSection = precSb.String()
	}

	prompt := fmt.Sprintf(`<available_agents>
%s
</available_agents>
%s%s%s%s
%s<user_request>
%s
</user_request>

		%s`, input.CapabilityInfo, agentCoordinationSection, userProfileSection, agentMemorySection, conversationHistorySection, contextPrecedenceSection, input.Request, planCtx.FormatRules)

	return &PlanningPromptResult{
		Prompt:        prompt,
		AllowedAgents: planCtx.AllowedAgents,
		SystemPrompt:  o.buildSystemPromptFromInput(ctx, input),
	}, nil
}

// buildContinuationPrompt creates a planning prompt that includes results from
// previous phases, enabling the LLM to plan subsequent steps with actual data.
// buildSystemPrompt returns the system-level message for LLM providers that support
// separate system/user message roles. If a PromptBuilder implements SystemPromptBuilder,
// it delegates to that; otherwise it falls back through SystemInstructions and
// finally a built-in default persona.
//
// ORCH-020 RC7: every fallback path that produces a persona without going through
// a SystemPromptBuilder is wrapped with appendRuntimeContext so the planner
// receives today's date regardless of which constructor wired the orchestrator.
// See BUG_PHASE3_SKIPPED_EXECUTION.md Issue 5 P10.
func (o *AIOrchestrator) buildSystemPrompt(ctx context.Context, request string) string {
	return o.buildSystemPromptFromInput(ctx, PromptInput{
		Request: request, Metadata: clonePromptMetadata(core.GetPipelineEnrichments(ctx)),
	})
}

func (o *AIOrchestrator) buildSystemPromptFromInput(ctx context.Context, input PromptInput) string {
	if o.promptBuilder != nil {
		if spb, ok := o.promptBuilder.(SystemPromptBuilder); ok {
			return spb.BuildSystemPrompt(ctx, PromptInput{Request: input.Request, Metadata: clonePromptMetadata(input.Metadata)})
		}
	}
	if o.config.PromptConfig.SystemInstructions != "" {
		return appendRuntimeContext(o.config.PromptConfig.SystemInstructions +
			"\n\nAs an AI orchestrator, you manage a multi-agent system to fulfill user requests.")
	}
	return appendRuntimeContext(defaultOrchestratorPersona)
}

// renderContinuationStepResult formats one completed step's Result line for
// the continuation prompt's <completed_steps> section. Cause 2a: when the
// step failed, render an explicit "[FAILED: <error>]" marker instead of the
// empty result.Response — without this the prompt looks like the step
// succeeded with empty output and the planner re-issues downstream steps
// that depend on it. Error is reduced to a single line and capped at 200
// chars so a verbose HTTP body cannot unbalance the continuation prompt.
func renderContinuationStepResult(result *StepResult, response string) string {
	if result.Success {
		return response
	}
	errMsg := truncateRunes(firstLine(result.Error), 200)
	return fmt.Sprintf("[FAILED: %s]", errMsg)
}

// orderedStepResults returns completed steps in chronological (plan) order — by the numeric suffix of
// the step ID (step-1, step-2, … assigned in order across phases), falling back to lexical for
// non-standard IDs. Phase 14: replaces sort.Strings, which ordered step-10 before step-2 and so broke
// recency. nil entries are dropped.
func orderedStepResults(completed map[string]*StepResult) []StepResult {
	out := make([]StepResult, 0, len(completed))
	for _, r := range completed {
		if r != nil {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := stepSeq(out[i].StepID), stepSeq(out[j].StepID)
		if si != sj {
			return si < sj
		}
		return out[i].StepID < out[j].StepID
	})
	return out
}

// stepSeq extracts the trailing integer of a "step-N" ID for chronological ordering; IDs without a
// numeric suffix sort last but deterministically (by ID).
func stepSeq(id string) int {
	if i := strings.LastIndexByte(id, '-'); i >= 0 && i+1 < len(id) {
		if n, err := strconv.Atoi(id[i+1:]); err == nil {
			return n
		}
	}
	return 1 << 30
}

// continuationStepOverhead approximates the fixed per-step framing in <completed_steps>
// ("Step <id> (<agent>):\n  Task: …\n  Result: …\n\n") for the Phase-14 budget fill.
const continuationStepOverhead = 96

// intOrDefault returns v when positive, else d — used to fall back to a default when a config knob is
// unset (zero) on a literally-constructed OrchestratorConfig.
func intOrDefault(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

// stepRenderCost estimates the rendered size of one step in <completed_steps> for the greedy
// recency-fill: a failed step renders its [FAILED: <error>] marker (response ignored); a successful
// step renders its digest/floor body.
func stepRenderCost(s *StepResult, body string) int {
	cost := continuationStepOverhead + len(s.Instruction)
	if s.Success {
		return cost + len(body)
	}
	// renderContinuationStepResult caps the failed marker's error at 200 runes — mirror that here so the
	// estimate isn't biased high for verbose errors.
	return cost + min(len(firstLine(s.Error)), 200)
}

// emitNOfMNote writes the Phase-14 "showing N of M completed steps" marker — emitted ALWAYS (even when
// N == M), with the eviction/addressability note only when steps were dropped for budget.
func emitNOfMNote(sb *strings.Builder, shown, total int) {
	if total <= 0 {
		return
	}
	fmt.Fprintf(sb, "[showing %d of %d completed steps", shown, total)
	if shown < total {
		sb.WriteString(" — older steps omitted for budget; their results remain referenceable by step-ID at execution")
	}
	sb.WriteString("]\n")
}

func (o *AIOrchestrator) buildContinuationPrompt(
	ctx context.Context,
	request string,
	completedResults map[string]*StepResult,
	executedStepIDs []string,
	previousContinuationNote string,
	phaseNumber int,
) (*PlanningPromptResult, error) {
	startTime := time.Now()

	// Tracing parity with BuildPlanningPrompt — make continuation prompt
	// construction visible as its own span so per-phase prompt assembly cost
	// shows up in Jaeger flame graphs.
	if o.telemetry != nil {
		var contSpan core.Span
		ctx, contSpan = o.telemetry.StartSpan(ctx, SpanPromptBuilderBuild)
		defer func() {
			contSpan.SetAttribute("builder_type", "continuation")
			contSpan.SetAttribute("phase_number", phaseNumber)
			contSpan.SetAttribute("completed_step_count", len(completedResults))
			contSpan.SetAttribute("duration_ms", time.Since(startTime).Milliseconds())
			contSpan.End()
		}()
		telemetry.SetCommonAttrsOn(ctx, contSpan)
	}

	// 1. Build phase context for context-aware tool re-selection.
	// ORCH-018 Layer 2: PhaseContextKeyPriorToolIDs carries tool IDs in
	// "agent/capability" format — the same format the selector LLM must
	// return — so the selector can copy them verbatim as its prior-tools
	// fallback selection, and the Go-side defensive recovery in
	// selectRelevantTools can use them when validateAndFilterTools runs.
	// PhaseContextKeyPriorToolsUsed (agent names) is kept for backward
	// compatibility with any other consumers.
	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:      phaseNumber,
		PhaseContextKeyContinuationNote: previousContinuationNote,
		PhaseContextKeyPriorToolsUsed:   extractUniqueAgentNames(completedResults),
		PhaseContextKeyPriorToolIDs:     extractUniqueToolIDs(completedResults),
		PhaseContextKeyCompletedSummary: buildCompactResultSummary(completedResults, 500),
	}

	// 2. Get shared planning infrastructure (with phase context for tiered selection)
	planCtx, err := o.buildplanningContext(ctx, request, phaseContext)
	if err != nil {
		// Pattern 4 (DISTRIBUTED_TRACING_GUIDE §11 / Complete Error Handling): record the failure on
		// the continuation builder span so it surfaces as error=true in Jaeger, then log with operation
		// + request_id + error_type before returning the wrapped error.
		telemetry.RecordSpanError(ctx, err)
		if o.logger != nil {
			o.logger.ErrorWithContext(ctx, "Failed to build continuation planning context", map[string]interface{}{
				"operation":    "continuation_prompt_build",
				"request_id":   GetRequestID(ctx),
				"phase_number": phaseNumber,
				"error":        err.Error(),
				"error_type":   "preparation",
			})
		}
		return nil, fmt.Errorf("failed to build planning context: %w", err)
	}
	preparedRequest, err := preparePromptValue(
		ctx, promptContinuationPlan, promptValueRequest, promptFieldRequest, request,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare continuation request: %w", err)
	}
	preparedCatalog, err := preparePromptValue(
		ctx, promptContinuationPlan, promptValueCapabilityCatalog,
		promptFieldCapabilityCatalog, planCtx.CapabilityCatalog,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare continuation capability catalog: %w", err)
	}
	preparedContinuationNote, err := preparePromptValue(
		ctx, promptContinuationPlan, promptValueContinuationNote,
		promptFieldContinuationNote, previousContinuationNote,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare continuation note: %w", err)
	}
	preparedEnrichments, err := prepareKnownPromptEnrichments(
		ctx, promptContinuationPlan, core.GetPipelineEnrichments(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("prepare continuation enrichments: %w", err)
	}

	// 3. Build completed results section (Phase 14: chronological digests, greedy recency-fill, C escalate)
	var resultsSb strings.Builder
	requestID := GetRequestID(ctx)

	// Chronological order (oldest→newest); replaces the old lexical sort.Strings (step-10 < step-2).
	steps := orderedStepResults(completedResults)

	totalBudget := o.config.ContinuationResultMaxTotalChars
	if totalBudget <= 0 {
		totalBudget = 32768 // safety fallback (matches DefaultConfig)
	}

	// B: per-step decision digests (valid-JSON skeletons; non-JSON → structural-floor body + C mark).
	// Knobs resolve from config (env-tunable, Phase 14), falling back to the digest defaults.
	digestOpts := continuationDigestOpts{
		floorChars: intOrDefault(o.config.ContinuationResultMaxChars, 10000),
		sampleN:    intOrDefault(o.config.ContinuationDigestArraySample, defaultDigestSampleN),
		scalarMax:  intOrDefault(o.config.ContinuationDigestScalarMax, defaultDigestScalarMax),
		maxKeys:    intOrDefault(o.config.ContinuationDigestMaxKeys, defaultDigestMaxKeys),
	}
	bodies, digestMeta := renderContinuationDigests(steps, digestOpts)

	// A: greedy recency-fill. Failed steps are always kept (high-signal, small); successful steps fill
	// newest-first until the aggregate budget is spent (recency = eviction order). The newest successful
	// step is always kept so the section is never empty.
	keep := make([]bool, len(steps))
	spent := 0
	for i := range steps {
		if !steps[i].Success {
			keep[i] = true
			spent += stepRenderCost(&steps[i], bodies[i])
		}
	}
	keptSuccess := false
	for i := len(steps) - 1; i >= 0; i-- {
		if !steps[i].Success {
			continue
		}
		cost := stepRenderCost(&steps[i], bodies[i])
		if keptSuccess && spent+cost > totalBudget {
			break // budget spent; older successful steps are evicted (see N-of-M note)
		}
		keep[i] = true
		keptSuccess = true
		spent += cost
	}

	// Decide which kept non-JSON steps escalate to C — NEWEST-first, capped — so the budget is spent on
	// the steps most relevant to the next phase (consistent with the recency principle), not the oldest.
	// ContinuationMaxEscalations is used as-is (no default fallback): 0 legitimately disables C.
	escalate := make([]bool, len(steps))
	if o.continuationDistiller != nil {
		budget := o.config.ContinuationMaxEscalations
		for i := len(steps) - 1; i >= 0 && budget > 0; i-- {
			if keep[i] && steps[i].Success && isStructurallyDegenerate(digestMeta[i]) {
				escalate[i] = true
				budget--
			}
		}
	}

	// C: escalate the selected non-JSON steps to the continuation distiller, then render in chronological
	// order so the newest lands at the high-attention end of the section.
	cSummaryBudget := intOrDefault(o.config.ResultDistill.TargetSize, 4096) // distiller's own output cap
	escalated, shown := 0, 0
	for i := range steps {
		if !keep[i] {
			continue // evicted past budget — covered by the N-of-M note
		}
		result := &steps[i]
		body := bodies[i]

		// C — escalate the steps selected above (newest-first, capped). Fail-open: an empty distiller
		// result leaves the structural-floor body untouched.
		if escalate[i] {
			stepCtx := ResultProcessorContext{
				StepID:        result.StepID,
				AgentName:     result.AgentName,
				Instruction:   result.Instruction,
				OriginalQuery: request,
			}
			// C's summary is sized by the distiller's own TargetSize knob (TRUVAG3_RESULT_DISTILL_TARGET).
			// For a non-JSON step it REPLACES the floor preview (the gist supersedes a raw truncation;
			// JSON skeletons are never escalated, so nothing parseable is lost). It runs AFTER the fill,
			// which already counted this step's floor body (≤ ContinuationResultMaxChars). The distiller
			// bounds the summary to ≤ cSummaryBudget, so whenever the floor exceeds the summary budget
			// (the default: 10000 > 4096) escalation SHRINKS the step below its already-counted cost —
			// the only possible overshoot is the "[summary] " prefix (~10 chars/step).
			if summary := o.continuationDistiller.ProcessForPrompt(ctx, result.Response, cSummaryBudget, stepCtx); summary != "" {
				body = "[summary] " + summary
				escalated++
			}
		}

		// ORCH-015: surface orchestrator delegation sub-steps so the planner doesn't re-issue them.
		childSummary := extractOrchestratorChildSummary(result)
		preparedStepID, err := preparePromptValue(
			ctx, promptContinuationPlan, promptValuePriorResult,
			promptFieldPriorResultStepID, result.StepID,
		)
		if err != nil {
			return nil, fmt.Errorf("prepare continuation step identity: %w", err)
		}
		preparedAgentName, err := preparePromptValue(
			ctx, promptContinuationPlan, promptValuePriorResult,
			promptFieldPriorResultAgentName, result.AgentName,
		)
		if err != nil {
			return nil, fmt.Errorf("prepare continuation agent identity: %w", err)
		}
		preparedInstruction, err := preparePromptValue(
			ctx, promptContinuationPlan, promptValuePriorResult,
			promptFieldPriorResultInstruction, result.Instruction,
		)
		if err != nil {
			return nil, fmt.Errorf("prepare continuation instruction: %w", err)
		}
		preparedBody := body
		preparedResult := *result
		if result.Success {
			preparedBody, err = preparePromptValue(
				ctx, promptContinuationPlan, promptValuePriorResult,
				promptFieldPriorResultResponse, body,
			)
		} else {
			preparedResult.Error, err = preparePromptValue(
				ctx, promptContinuationPlan, promptValuePriorResult,
				promptFieldPriorResultError, result.Error,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("prepare continuation result: %w", err)
		}
		if childSummary != "" {
			childSummary, err = preparePromptValue(
				ctx, promptContinuationPlan, promptValuePriorResult,
				promptFieldPriorResultResponse, childSummary,
			)
			if err != nil {
				return nil, fmt.Errorf("prepare continuation child summary: %w", err)
			}
		}

		fmt.Fprintf(&resultsSb, "Step %s (%s):\n  Task: %s\n  Result: %s\n",
			preparedStepID, preparedAgentName, preparedInstruction,
			renderContinuationStepResult(&preparedResult, preparedBody))

		if childSummary != "" {
			fmt.Fprintf(&resultsSb,
				"  NOTE: This orchestrator step internally executed these sub-steps:\n%s"+
					"  Do NOT duplicate any of these actions in the next phase.\n",
				childSummary)

			childStepCount := strings.Count(childSummary, "\n")
			// Extract capability names from summary lines (format: "    - agent/capability [STATUS]: ...")
			var caps []string
			for _, line := range strings.Split(childSummary, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "- ") {
					parts := strings.SplitN(line[2:], " ", 2) // split "agent/cap [STATUS]: ..." at first space
					if len(parts) > 0 {
						caps = append(caps, parts[0]) // "agent/cap"
					}
				}
			}
			childCapabilities := strings.Join(caps, ",")

			if o.logger != nil {
				o.logger.InfoWithContext(ctx, "Orchestrator child steps extracted for continuation prompt", map[string]interface{}{
					"operation":          "continuation_child_extraction",
					"request_id":         requestID,
					"step_id":            result.StepID,
					"child_steps_count":  childStepCount,
					"child_capabilities": childCapabilities,
					"agent_name":         result.AgentName,
				})
			}

			telemetry.AddSpanEvent(ctx, "orchestrator.child_steps_extracted",
				attribute.String("request_id", requestID),
				attribute.Bool("child_steps_found", true),
				attribute.Int("child_steps_count", childStepCount),
				attribute.String("child_capabilities", childCapabilities),
				attribute.String("step_id", result.StepID),
				attribute.String("agent_name", result.AgentName),
			)
		}
		resultsSb.WriteString("\n")
		shown++
	}

	// Phase 14: "showing N of M completed steps" — always emitted (even N == M).
	emitNOfMNote(&resultsSb, shown, len(steps))

	// Observability (Phase 12): record the continuation budget outcome as a per-request span event,
	// and (Phase 14) as module-owned aggregate metrics for dashboards/alerts (telemetry §4).
	telemetry.AddSpanEvent(ctx, "orchestrator.continuation_budget",
		attribute.String("request_id", requestID),
		attribute.Int("steps_total", len(steps)),
		attribute.Int("steps_shown", shown),
		attribute.Int("c_escalations", escalated),
		attribute.Int("budget_chars", totalBudget),
	)
	emitContinuationBudgetMetrics(len(steps), shown, escalated, resultsSb.Len())

	// 4. Build executed step IDs list
	preparedExecutedStepIDs := make([]string, len(executedStepIDs))
	for index, stepID := range executedStepIDs {
		preparedExecutedStepIDs[index], err = preparePromptValue(
			ctx, promptContinuationPlan, promptValuePriorResult,
			promptFieldPriorResultStepID, stepID,
		)
		if err != nil {
			return nil, fmt.Errorf("prepare executed step identity: %w", err)
		}
	}
	executedIDsList := strings.Join(preparedExecutedStepIDs, ", ")

	// 5. Compute phase budget (Issue 3: LLM needs visibility into budget)
	maxPhases := o.config.IterativePlanning.MaxPhases
	maxTotalSteps := o.config.IterativePlanning.MaxTotalSteps
	stepsUsed := len(executedStepIDs)
	isFinalPhase := phaseNumber >= maxPhases

	// 6. Compose the full prompt with XML section tags (Issue 5: structured sections)
	var sb strings.Builder

	sb.WriteString("CONTINUATION PHASE — Plan the next steps based on completed results.\n\n")

	sb.WriteString("<user_request>\n")
	sb.WriteString(preparedRequest)
	sb.WriteString("\n</user_request>\n\n")

	sb.WriteString("<completed_steps>\n")
	sb.WriteString(resultsSb.String())
	sb.WriteString("</completed_steps>\n\n")

	sb.WriteString("<executed_ids>\n")
	fmt.Fprintf(&sb, "[%s]\n", executedIDsList)
	fmt.Fprintf(&sb, "Start new step IDs from step-%d.\n", stepsUsed+1)
	sb.WriteString("</executed_ids>\n\n")

	// Phase budget section (Issue 3: give LLM visibility into remaining budget)
	sb.WriteString("<phase_budget>\n")
	fmt.Fprintf(&sb, "Phase: %d of %d maximum.\n", phaseNumber, maxPhases)
	fmt.Fprintf(&sb, "Steps used: %d of %d. Remaining: %d.\n",
		stepsUsed, maxTotalSteps, maxTotalSteps-stepsUsed)
	if isFinalPhase {
		sb.WriteString("This is the FINAL phase. You MUST set terminal: true and plan ALL remaining work here.\n")
	} else {
		fmt.Fprintf(&sb, "Phases remaining after this: %d.\n", maxPhases-phaseNumber)
	}
	sb.WriteString("</phase_budget>\n\n")

	// Optimization reminder (Issue 4: reduce unnecessary phase splits)
	sb.WriteString("<optimization_reminder>\n")
	sb.WriteString("If remaining steps can reference completed step data via {{step-N.response.data.field}}\n")
	sb.WriteString("templates, include them in THIS phase with depends_on chains rather than requesting\n")
	sb.WriteString("another continuation. Each step using {{step-N...}} templates lists the referenced step-N in its depends_on.\n")
	sb.WriteString("Only use terminal: false if you need to discover new entities.\n")
	sb.WriteString("</optimization_reminder>\n\n")

	if preparedContinuationNote != "" {
		sb.WriteString("<previous_note>\n")
		sb.WriteString(preparedContinuationNote)
		sb.WriteString("\n</previous_note>\n\n")
	}

	sb.WriteString("<available_agents>\n")
	sb.WriteString(preparedCatalog)
	sb.WriteString("\n</available_agents>\n\n")

	// Include agent coordination, memory, and conversation history from pipeline enrichments.
	// Same mechanism as the initial plan prompt — enables hooks to provide context
	// for continuation phases. Enrichments were set once in BeforePlanningHooks and
	// flow through via context for all phases.
	enrichments := preparedEnrichments
	if len(enrichments) > 0 {
		if coordCtx, ok := enrichments[core.EnrichmentActivityCoordination]; ok {
			if coordStr, isStr := coordCtx.(string); isStr && coordStr != "" {
				sb.WriteString("<agent_coordination>\n")
				sb.WriteString(coordStr)
				sb.WriteString("\n</agent_coordination>\n\n")
			}
		}
		// User profile from UserMemoryEnrichmentHook (per-user private facts)
		if userProfile, ok := enrichments[core.EnrichmentUserProfile]; ok {
			if profileStr, isStr := userProfile.(string); isStr && profileStr != "" {
				sb.WriteString(profileStr)
				sb.WriteString("\n\n")
			}
		}
		if ragCtx, ok := enrichments[core.EnrichmentRAGContext]; ok {
			if ragStr, isStr := ragCtx.(string); isStr && ragStr != "" {
				sb.WriteString("<agent_memory>\n")
				sb.WriteString(ragStr)
				sb.WriteString("\n</agent_memory>\n\n")
			}
		}
		if convHistory, ok := enrichments[core.EnrichmentConversationHistory]; ok {
			if convStr, isStr := convHistory.(string); isStr && convStr != "" {
				sb.WriteString("<conversation_history>\n")
				sb.WriteString(convStr)
				sb.WriteString("\n</conversation_history>\n\n")
			}
		}
	}

	// Precedence rule: emitted right after the conflict-prone enrichments
	// so the planner reads it immediately after the stored context. Without
	// this, continuation phases re-anchor on stale <user_profile> "Context"
	// entries even when <conversation_history> has moved on.
	writeContextPrecedence(ctx, &sb, enrichments, PromptKindContinuation)

	// Include custom instructions in continuation prompts (same as initial plan).
	// Without this, domain-specific rules (e.g., "use project_key TRUV for JIRA")
	// are invisible to Phase 2+ plans, causing the LLM to hallucinate values.
	// Uses shared helper (ORCH-012 fix, extracted in ORCH-014).
	writeCustomInstructions(&sb, o.config.PromptConfig.CustomInstructions)

	sb.WriteString("<planning_instructions>\n")
	sb.WriteString("Plan the NEXT phase to fulfill remaining parts of the user's request.\n")
	sb.WriteString("- Reference completed step outputs using \"{{step-N.response.data.field}}\" syntax (always quoted)\n")
	sb.WriteString("- Each Result above is a structure-preserving digest (object keys, array sizes, sample values); sample values are illustrative — reference any field shown with the same {{step-N...}} syntax and the full value resolves at execution\n")
	sb.WriteString("- \"depends_on\" may ONLY reference step IDs within THIS phase's steps array\n")
	sb.WriteString("- For step IDs from PRIOR phases referenced via templates, list them in \"implicit_deps\"\n")
	sb.WriteString("- Set \"terminal\": true if this phase completes the user's request\n")
	sb.WriteString("- Set \"terminal\": false only if you need to discover new entities from results\n")
	sb.WriteString("</planning_instructions>\n\n")

	// Concrete example demonstrating implicit_deps for prior-phase references
	// (ORCH-020 RC7 Change #3). Schema-by-example is preferred over prose per
	// EFFECTIVE_PROMPTS_GUIDE §4.1. The example is only emitted when there are
	// at least two completed prior steps to reference — otherwise the example
	// would teach self-reference or point at non-existent step IDs, both of
	// which the new validators reject. Under the 2-step minimum, new-step IDs
	// are always strictly greater than the two prior IDs cited.
	if stepsUsed >= 2 {
		nextStep := stepsUsed + 1
		priorStep1 := stepsUsed - 1
		priorStep2 := stepsUsed
		sb.WriteString("<example>\n")
		fmt.Fprintf(&sb, `{
  "plan_id": "continuation-example",
  "terminal": true,
  "steps": [
    {
      "step_id": "step-%d",
      "agent_name": "example-tool",
      "depends_on": [],
      "implicit_deps": ["step-%d", "step-%d"],
      "metadata": {
        "capability": "do_work",
        "parameters": {
          "primary":   "{{step-%d.response.data.value}}",
          "secondary": "{{step-%d.response.data.value}}"
        }
      }
    }
  ]
}`, nextStep, priorStep1, priorStep2, priorStep1, priorStep2)
		sb.WriteString("\n</example>\n\n")
	}

	// Fix: Align the format rules example step ID with the actual next step ID.
	// Per P1 research: LLMs follow concrete examples over textual rules.
	// Without this, the hardcoded "step-1" example in buildDefaultFormatRules
	// causes the LLM to reuse step-1 in continuation plans, triggering
	// step ID conflict validation failures.
	formatRules := planCtx.FormatRules
	if stepsUsed > 0 {
		nextStepID := fmt.Sprintf("step-%d", stepsUsed+1)
		formatRules = strings.Replace(formatRules, `"step-1"`, fmt.Sprintf(`"%s"`, nextStepID), 1)
	}
	sb.WriteString(formatRules)

	prompt := sb.String()

	// Continuation prompt observability
	durationMs := float64(time.Since(startTime).Milliseconds())

	// requestID is declared earlier in this function (Phase 14 continuation builder); re-derive from
	// baggage here for the built-event span (baggage is the authoritative source at this point).
	requestID = ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	telemetry.AddSpanEvent(ctx, "orchestrator.continuation_prompt.built",
		attribute.String("request_id", requestID),
		attribute.Int("phase_number", phaseNumber),
		attribute.Int("prompt_length", len(prompt)),
		attribute.Int("completed_steps_count", len(completedResults)),
		attribute.Int("budget_steps_remaining", maxTotalSteps-stepsUsed),
		attribute.Bool("is_final_phase", isFinalPhase),
		attribute.Float64("duration_ms", durationMs),
	)

	if o.telemetry != nil {
		o.telemetry.RecordMetric("orchestrator.continuation_prompt.build_duration_ms", durationMs,
			map[string]string{
				"phase_number": fmt.Sprintf("%d", phaseNumber),
			})
		o.telemetry.RecordMetric("orchestrator.continuation_prompt.size_bytes", float64(len(prompt)),
			map[string]string{
				"phase_number": fmt.Sprintf("%d", phaseNumber),
			})
	}

	if o.logger != nil {
		logFields := map[string]interface{}{
			"operation":              "build_continuation_prompt",
			"phase_number":           phaseNumber,
			"prompt_length":          len(prompt),
			"budget_steps_remaining": maxTotalSteps - stepsUsed,
			"is_final_phase":         isFinalPhase,
			"completed_steps_count":  len(completedResults),
			"prior_tools_used":       extractUniqueAgentNames(completedResults),
			"continuation_note":      truncateString(previousContinuationNote, 200),
			"duration_ms":            durationMs,
		}
		// Pattern 3: request_id propagation (from context baggage)
		if baggage := telemetry.GetBaggage(ctx); baggage != nil {
			if reqID := baggage["request_id"]; reqID != "" {
				logFields["request_id"] = reqID
			}
		}
		o.logger.DebugWithContext(ctx, "Built continuation prompt", logFields)
	}

	return &PlanningPromptResult{
		Prompt:        prompt,
		AllowedAgents: planCtx.AllowedAgents,
		SystemPrompt:  o.buildSystemPrompt(ctx, request),
	}, nil
}

// extractOrchestratorChildSummary parses an orchestrator step's JSON response
// and returns a formatted summary of its child sub-steps. This enables the
// continuation planner to see what a delegated agent already did (e.g., JIRA
// ticket creation, Slack notification) and avoid duplicating those actions.
// Returns "" if the step is not an orchestrator or has no parseable steps[].
// See ORCH-015 §9 for design details.
func extractOrchestratorChildSummary(result *StepResult) string {
	if result == nil {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result.Response), &parsed); err != nil {
		return ""
	}
	steps, ok := parsed["steps"].([]interface{})
	if !ok || len(steps) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, s := range steps {
		sm, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		agent, _ := sm["agent_name"].(string)
		cap, _ := sm["capability"].(string)
		succ, _ := sm["success"].(bool)
		resp, _ := sm["response"].(string)
		if len(resp) > 200 {
			resp = resp[:200] + "..."
		}
		status := "FAILED"
		if succ {
			status = "SUCCESS"
		}
		fmt.Fprintf(&sb, "    - %s/%s [%s]: %s\n", agent, cap, status, resp)
	}
	return sb.String()
}

// extractUniqueAgentNames returns sorted, deduplicated agent names from completed results.
// Sorted for deterministic prompt generation across runs.
func extractUniqueAgentNames(results map[string]*StepResult) []string {
	seen := make(map[string]bool)
	for _, r := range results {
		seen[r.AgentName] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// extractUniqueToolIDs returns a deterministically sorted, deduplicated list
// of "agent/capability" tool IDs from successfully completed step results.
// Used by ORCH-018 Layer 2 to populate PhaseContextKeyPriorToolIDs in
// continuation phase context, so the selector LLM and the Go-side defensive
// fallback both see prior tool IDs in the format the selector must return.
// Steps with missing AgentName or Capability, or with Success=false, are skipped.
func extractUniqueToolIDs(results map[string]*StepResult) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, r := range results {
		if r == nil || !r.Success {
			continue
		}
		if r.AgentName == "" || r.Capability == "" {
			continue
		}
		id := r.AgentName + "/" + r.Capability
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// buildCompactResultSummary creates a brief summary of completed step results
// for the tiered selection LLM. Steps are sorted by ID for deterministic output.
// Kept short (maxLen) to minimize token overhead in the selection prompt.
func buildCompactResultSummary(results map[string]*StepResult, maxLen int) string {
	// Sort step IDs for deterministic prompt ordering
	stepIDs := make([]string, 0, len(results))
	for id := range results {
		stepIDs = append(stepIDs, id)
	}
	sort.Strings(stepIDs)

	var sb strings.Builder
	for _, stepID := range stepIDs {
		r := results[stepID]
		if r == nil {
			continue
		}
		line := fmt.Sprintf("%s(%s): %s\n", stepID, r.AgentName,
			truncateString(r.Response, 80))
		if sb.Len()+len(line) > maxLen {
			sb.WriteString("...[truncated]\n")
			break
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// buildPlanningPromptWithParseError constructs a retry prompt that reuses the previous
// prompt result (including tool selection) and prepends parse error feedback.
// This avoids redundant tiered capability selection LLM calls on retry.
func (o *AIOrchestrator) buildPlanningPromptWithParseError(ctx context.Context, previousPromptResult *PlanningPromptResult, parseErr error) (*PlanningPromptResult, error) {
	if previousPromptResult == nil {
		return nil, fmt.Errorf("previousPromptResult is nil: cannot build retry prompt without prior planning context")
	}

	// Construct error feedback section
	errorFeedback := fmt.Sprintf(`
<parse_error>
Your previous response could not be parsed as valid JSON.
Error: %s
</parse_error>

<json_rules>
1. All values are literals — use 46828.5, not 100 * 468.285
2. String values and keys use double quotes
3. All string values use plain text only
4. Omit trailing commas: {"a": 1} is valid, {"a": 1,} is not
5. Template references are quoted strings: "{{step-1.response.data.field}}"
</json_rules>

Regenerate the execution plan. Output raw JSON — no markdown, no code blocks. Start with { and end with }.`,
		parseErr.Error())

	// Reuse previous prompt, AllowedAgents, and SystemPrompt — no fresh tiered selection
	return &PlanningPromptResult{
		Prompt:        errorFeedback + "\n\n" + previousPromptResult.Prompt,
		AllowedAgents: previousPromptResult.AllowedAgents,
		SystemPrompt:  previousPromptResult.SystemPrompt,
	}, nil
}

// unquotedTemplateRegex matches unquoted {{...}} template references in JSON.
// Matches after : (key-value) or after [ or , (array elements).
// Already-quoted templates are not matched because " separates the delimiter from {{.
var unquotedTemplateRegex = regexp.MustCompile(`(?::\s*|[\[,]\s*)({{[^}]+}})`)

// quoteUnquotedTemplates converts unquoted template references to quoted strings
// so that json.Unmarshal can parse the plan. Templates are resolved later by the executor.
//
// IMPORTANT: Only call as a recovery step after json.Unmarshal fails (try-parse-first).
// The regex cannot distinguish structural , from , inside quoted strings, so running
// unconditionally risks false positives on strings like "{{city}}, {{country}}".
func quoteUnquotedTemplates(jsonStr string) string {
	return unquotedTemplateRegex.ReplaceAllStringFunc(jsonStr, func(match string) string {
		tmplStart := strings.Index(match, "{{")
		prefix := match[:tmplStart]
		tmpl := match[tmplStart:]
		return prefix + "\"" + tmpl + "\""
	})
}

// trailingCommaRegex matches trailing commas before } or ] in JSON.
var trailingCommaRegex = regexp.MustCompile(`,\s*([}\]])`)

// attemptJSONRepair tries to fix common JSON issues that LLMs produce.
// Returns (repairedJSON, true) if changes were made, (original, false) otherwise.
func attemptJSONRepair(jsonStr string) (string, bool) {
	repaired := jsonStr
	// Strip BOM and zero-width characters
	repaired = strings.TrimLeft(repaired, "\uFEFF\u200B\u200C\u200D")
	// Remove trailing commas before } or ]
	repaired = trailingCommaRegex.ReplaceAllString(repaired, "$1")
	return repaired, repaired != jsonStr
}

// parsePlan parses the LLM response into a RoutingPlan.
// Recovery chain: try raw → try quoted templates (C2) → try structural repair (B3) → return error.
func (o *AIOrchestrator) parsePlan(llmResponse string) (*RoutingPlan, error) {
	cleaned := cleanLLMResponse(llmResponse)

	var plan RoutingPlan
	if err := json.Unmarshal([]byte(cleaned), &plan); err != nil {
		// C2: Try quoting unquoted templates (try-parse-first recovery)
		quoted := quoteUnquotedTemplates(cleaned)
		if quoted != cleaned {
			if err2 := json.Unmarshal([]byte(quoted), &plan); err2 == nil {
				if o.logger != nil {
					o.logger.Info("Plan parsed after quoting unquoted templates", map[string]interface{}{
						"operation": "plan_parsing_recovery",
						"recovery":  "quote_templates",
						"error":     err.Error(),
					})
				}
				plan.CreatedAt = time.Now()
				return &plan, nil
			}
		}
		// B3: Attempt structural repair (trailing commas, BOM)
		if repaired, ok := attemptJSONRepair(cleaned); ok {
			if err2 := json.Unmarshal([]byte(repaired), &plan); err2 == nil {
				if o.logger != nil {
					o.logger.Info("Plan parsed after structural JSON repair", map[string]interface{}{
						"operation": "plan_parsing_recovery",
						"recovery":  "structural_repair",
						"error":     err.Error(),
					})
				}
				plan.CreatedAt = time.Now()
				return &plan, nil
			}
		}
		if o.logger != nil {
			o.logger.Warn("Failed to parse plan JSON", map[string]interface{}{
				"operation":   "plan_parsing",
				"error":       err.Error(),
				"json_length": len(cleaned),
				"json_prefix": truncateString(cleaned, 300),
			})
		}
		return nil, fmt.Errorf("failed to parse plan JSON: %w", err)
	}

	plan.CreatedAt = time.Now()
	return &plan, nil
}

// knownStepIDSet returns the step IDs that can satisfy a template reference at this point:
// completed prior-phase IDs ∪ the current plan's own step IDs. Rebuilt per round because
// plan.Steps changes on regeneration.
func knownStepIDSet(executedStepIDs []string, plan *RoutingPlan) map[string]struct{} {
	known := make(map[string]struct{}, len(executedStepIDs)+len(plan.Steps))
	for _, id := range executedStepIDs {
		known[id] = struct{}{}
	}
	for _, s := range plan.Steps {
		known[s.StepID] = struct{}{}
	}
	return known
}

// agentInCatalog reports whether an agent with this name is registered. Reuses the catalog's
// FindByName (scans under RLock, no full-map copy) and inherits its case-sensitive exact match —
// the same policy the executor's dispatch (findAgentByName) uses, so the detector agrees with the
// component that would actually run the step.
func (o *AIOrchestrator) agentInCatalog(name string) bool {
	return o.catalog != nil && o.catalog.FindByName(name) != nil
}

// capabilityRegistered reports whether any catalogued agent exposes this capability. Reuses the
// catalog's precomputed capability index (FindByCapability, O(1)) instead of scanning every agent.
func (o *AIOrchestrator) capabilityRegistered(capName string) bool {
	return o.catalog != nil && capName != "" && len(o.catalog.FindByCapability(capName)) > 0
}

// stepOutputTemplateExact matches a string that is EXACTLY one framework step-output template
// (anchored). Rejects mixed literal+template strings like "send {{step-1.response.data}} to Bob",
// which the unanchored stepOutputTemplatePattern would wrongly accept.
var stepOutputTemplateExact = regexp.MustCompile(`^\{\{[\w-]+\.[\w-]+(?:\.[\w-]+)*\}\}$`)

// onlyAggregatesPriorOutputs reports whether EVERY parameter leaf is a step-output template, so
// the step supplies NO literal external input of ANY type. Recurses maps/slices. A numeric/bool
// leaf, a mixed literal+template string, or an empty/nil container all fail (conservative).
// Domain-agnostic — inspects template shape only, never domain keywords.
func onlyAggregatesPriorOutputs(v interface{}) bool {
	switch val := v.(type) {
	case map[string]interface{}:
		if len(val) == 0 {
			return false
		}
		for _, item := range val {
			if !onlyAggregatesPriorOutputs(item) {
				return false
			}
		}
		return true
	case []interface{}:
		if len(val) == 0 {
			return false
		}
		for _, item := range val {
			if !onlyAggregatesPriorOutputs(item) {
				return false
			}
		}
		return true
	case string:
		return stepOutputTemplateExact.MatchString(strings.TrimSpace(val))
	default:
		return false // numbers, bools, nil → literal external input
	}
}

// detectTerminalSynthesisPseudoSteps is SIDE-EFFECT-FREE (unit-testable in isolation).
// It returns the steps in a TERMINAL plan that are non-dispatchable aggregations of prior
// outputs — the framework's own synthesis expressed (wrongly) as a dispatched step. The
// framework synthesizes the final answer itself (o.synthesizer over all completed results),
// so such a step is redundant by construction.
//
// knownStepIDs is the set of step IDs that can satisfy a template reference: completed
// prior-phase IDs plus the current plan's own step IDs.
//
// FAIL-OPEN: returns nil unless every signal proves the step is a SATISFIABLE aggregation of
// EXISTING outputs, and returns nil entirely when the catalog is unavailable (can't prove →
// don't touch).
func (o *AIOrchestrator) detectTerminalSynthesisPseudoSteps(plan *RoutingPlan, knownStepIDs map[string]struct{}) []RoutingStep {
	if plan == nil || !plan.IsTerminal() || len(plan.Steps) == 0 || o.catalog == nil {
		return nil
	}
	// Step IDs referenced by some step in this plan (depends_on, implicit_deps, or templates).
	// A referenced step is never dropped: removing it would leave a dangling reference that
	// validatePlan/validateTemplatePaths would then reject, forcing a needless regeneration round.
	// Synthesis pseudo-steps are terminal aggregators that nothing references, so they stay droppable.
	referenced := make(map[string]struct{})
	for _, s := range plan.Steps {
		for _, d := range s.DependsOn {
			referenced[d] = struct{}{}
		}
		for _, d := range s.ImplicitDeps {
			referenced[d] = struct{}{}
		}
		if p, ok := s.Metadata["parameters"].(map[string]interface{}); ok {
			for id := range collectReferencedStepIDs(p) {
				referenced[id] = struct{}{}
			}
		}
	}

	var dropped []RoutingStep
	for _, step := range plan.Steps {
		if _, isReferenced := referenced[step.StepID]; isReferenced {
			continue // another step depends on this one — dropping it would dangle the reference
		}
		capName, _ := step.Metadata["capability"].(string)
		params, _ := step.Metadata["parameters"].(map[string]interface{})

		refs := collectReferencedStepIDs(params)
		if len(refs) == 0 {
			continue // references no prior step ⇒ not an aggregation
		}
		// Every referenced step must be satisfiable. If a ref points at an unknown step
		// (e.g. {{step-999...}}), fail open so validateTemplatePaths rejects it and the loop
		// regenerates — never silently drop a malformed reference.
		allRefsKnown := true
		for id := range refs {
			if _, ok := knownStepIDs[id]; !ok {
				allRefsKnown = false
				break
			}
		}

		if allRefsKnown &&
			!o.agentInCatalog(step.AgentName) && // agent absent from full catalog
			!o.capabilityRegistered(capName) && // capability registered nowhere
			onlyAggregatesPriorOutputs(params) { // every leaf is a step-output template
			dropped = append(dropped, step)
		}
	}
	return dropped
}

// normalizeTerminalSynthesisPlan strips detected synthesis pseudo-steps and emits telemetry.
// Thin effectful wrapper around the pure detector. Returns true if it changed the plan.
//
// The framework's contract for a terminal/synthesis phase is a zero-step terminal plan: the
// final user-facing answer is produced by o.synthesizer from all completed step results, not by
// a dispatched step. When the planner instead emits a synthesis pseudo-step routed to a
// non-existent agent, this collapses it (→ zero-step terminal plan when it was the only step).
func (o *AIOrchestrator) normalizeTerminalSynthesisPlan(ctx context.Context, plan *RoutingPlan, knownStepIDs map[string]struct{}, requestID string) bool {
	dropped := o.detectTerminalSynthesisPseudoSteps(plan, knownStepIDs)
	if len(dropped) == 0 {
		return false
	}
	droppedIDs := make(map[string]struct{}, len(dropped))
	for _, s := range dropped {
		droppedIDs[s.StepID] = struct{}{}
	}
	kept := plan.Steps[:0:0]
	for _, s := range plan.Steps {
		if _, drop := droppedIDs[s.StepID]; !drop {
			kept = append(kept, s)
		}
	}
	plan.Steps = kept

	// Observability (NOT an error — a successful correction). WARN log + span event + counter,
	// NO RecordSpanError. Message is neutral because a mixed plan may retain real steps.
	for _, step := range dropped {
		capName, _ := step.Metadata["capability"].(string)
		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "Normalized terminal synthesis pseudo-step out of plan", map[string]interface{}{
				"operation":       "terminal_synthesis_normalization",
				"request_id":      requestID,
				"plan_id":         plan.PlanID,
				"step_id":         step.StepID,
				"agent_name":      step.AgentName,
				"capability":      capName,
				"remaining_steps": len(plan.Steps),
			})
		}
		telemetry.AddSpanEvent(ctx, "orchestrator.terminal_synthesis.normalized",
			attribute.String("request_id", requestID),
			attribute.String("plan_id", plan.PlanID),
			attribute.String("dropped_agent", step.AgentName),
			attribute.String("dropped_capability", capName),
			attribute.Int("remaining_steps", len(plan.Steps)),
		)
		telemetry.Counter("orchestration.plan.terminal_synthesis_normalized",
			"module", telemetry.ModuleOrchestration)
	}
	return true
}

// runPlanValidationGauntlet runs the plan validators in order and returns the FIRST failure,
// emitting that validator's WARN log + span event (and a rejection counter for the RC1/RC2/RC3
// validators; validatePlan and the step-ID-conflict check emit no counter). Telemetry strings
// (log messages, operation values, span-event names, counter names) are preserved VERBATIM from
// the former inline blocks in executePhaseLoop — Loki/Jaeger/Grafana key off them. It does NOT
// regenerate: the caller's fixpoint loop owns regeneration, so every validator re-runs after a
// regeneration.
func (o *AIOrchestrator) runPlanValidationGauntlet(
	ctx context.Context,
	plan *RoutingPlan,
	executedStepCaps map[string]stepCapability,
	executedStepIDs []string,
	phaseCount int,
	requestID string,
) error {
	// validatePlan — agent existence + capability + same-phase deps
	if valErr := o.validatePlan(plan); valErr != nil {
		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "Phase plan validation failed — triggering regeneration", map[string]interface{}{
				"operation":        "phase_validation_regeneration_trigger",
				"request_id":       requestID,
				"phase":            phaseCount,
				"validation_error": valErr.Error(),
				"plan_id":          plan.PlanID,
				"plan_terminal":    plan.IsTerminal(),
				"plan_step_count":  len(plan.Steps),
			})
		}
		telemetry.AddSpanEvent(ctx, "orchestrator.validation.regeneration_triggered",
			attribute.String("request_id", requestID),
			attribute.Int("phase_number", phaseCount),
			attribute.String("validation_error", valErr.Error()),
			attribute.String("plan_id", plan.PlanID),
			attribute.Bool("plan_terminal", plan.IsTerminal()),
		)
		return valErr
	}

	// RC2 — reject plans containing {{...}} tokens that don't match the supported
	// {{stepId.fieldPath}} shape (e.g. hallucinated {{today_plus_1}}).
	if macroErr := validateNoUnknownMacros(plan); macroErr != nil {
		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "Unknown macro in plan — triggering regeneration", map[string]interface{}{
				"operation":        "unknown_macro_validation",
				"request_id":       requestID,
				"phase":            phaseCount,
				"plan_id":          plan.PlanID,
				"validation_error": macroErr.Error(),
				"error_type":       "unknown_macro",
			})
		}
		telemetry.AddSpanEvent(ctx, "orchestrator.unknown_macro_validation.regeneration_triggered",
			attribute.String("request_id", requestID),
			attribute.Int("phase_number", phaseCount),
			attribute.String("plan_id", plan.PlanID),
			attribute.String("validation_error", macroErr.Error()),
		)
		telemetry.Counter("orchestration.plan.rejected_unknown_macro",
			"error_type", "unknown_macro",
			"module", telemetry.ModuleOrchestration,
		)
		return macroErr
	}

	// RC3 — enforce templates ⊆ depends_on (same-phase) ∪ implicit_deps (cross-phase).
	if depErr := o.validateDependencyConsistency(plan); depErr != nil {
		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "Missing depends_on/implicit_deps for templated step — triggering regeneration", map[string]interface{}{
				"operation":        "missing_dependency_validation",
				"request_id":       requestID,
				"phase":            phaseCount,
				"plan_id":          plan.PlanID,
				"validation_error": depErr.Error(),
				"error_type":       "missing_dependency",
			})
		}
		telemetry.AddSpanEvent(ctx, "orchestrator.missing_dependency_validation.regeneration_triggered",
			attribute.String("request_id", requestID),
			attribute.Int("phase_number", phaseCount),
			attribute.String("plan_id", plan.PlanID),
			attribute.String("validation_error", depErr.Error()),
		)
		telemetry.Counter("orchestration.plan.rejected_missing_dependency",
			"error_type", "missing_dependency",
			"module", telemetry.ModuleOrchestration,
		)
		return depErr
	}

	// RC1 — cross-phase-aware validateTemplatePaths: rejects references to steps absent from both
	// the current plan AND executedStepCaps, and verifies the requested field exists in the
	// referenced capability's declared output schema (when available).
	if templateErr := o.validateTemplatePaths(plan, executedStepCaps); templateErr != nil {
		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "Template path validation failed — triggering regeneration", map[string]interface{}{
				"operation":        "cross_phase_missing_step_validation",
				"request_id":       requestID,
				"phase":            phaseCount,
				"validation_error": templateErr.Error(),
				"plan_id":          plan.PlanID,
				"error_type":       "cross_phase_missing_step",
			})
		}
		telemetry.AddSpanEvent(ctx, "orchestrator.cross_phase_validation.regeneration_triggered",
			attribute.String("request_id", requestID),
			attribute.Int("phase_number", phaseCount),
			attribute.String("validation_error", templateErr.Error()),
			attribute.String("plan_id", plan.PlanID),
		)
		telemetry.Counter("orchestration.plan.rejected_cross_phase_missing_step",
			"error_type", "cross_phase_missing_step",
			"module", telemetry.ModuleOrchestration,
		)
		return templateErr
	}

	// Step ID conflict check for continuation phases (phase 1 has no prior step IDs to clash with).
	if phaseCount > 1 {
		if conflictErr := validateNoStepIDConflicts(plan, executedStepIDs); conflictErr != nil {
			if o.logger != nil {
				o.logger.WarnWithContext(ctx, "Step ID conflict detected — triggering regeneration", map[string]interface{}{
					"operation":      "step_id_conflict_regeneration_trigger",
					"request_id":     requestID,
					"phase":          phaseCount,
					"conflict_error": conflictErr.Error(),
					"plan_id":        plan.PlanID,
					"plan_terminal":  plan.IsTerminal(),
				})
			}
			telemetry.AddSpanEvent(ctx, "orchestrator.step_id_conflict.regeneration_triggered",
				attribute.String("request_id", requestID),
				attribute.Int("phase_number", phaseCount),
				attribute.String("conflict_error", conflictErr.Error()),
				attribute.String("plan_id", plan.PlanID),
				attribute.Bool("plan_terminal", plan.IsTerminal()),
			)
			return conflictErr
		}
	}

	return nil
}

// validatePlan checks if the plan is executable
func (o *AIOrchestrator) validatePlan(plan *RoutingPlan) error {
	// Check if discovery is available
	if o.discovery == nil {
		return fmt.Errorf("discovery service not configured")
	}

	if o.logger != nil {
		o.logger.Debug("Validating execution plan", map[string]interface{}{
			"operation":  "plan_validation",
			"plan_id":    plan.PlanID,
			"step_count": len(plan.Steps),
		})
	}

	// Check that plan has at least one step.
	// An empty plan cannot fulfill any user request UNLESS the plan is signaling
	// one of two valid "no execution needed" states:
	//
	//   1. ORCH-018 Layer 1: NeedsUserInput is set — the planner is asking the
	//      user for clarification. The phase loop short-circuits and routes the
	//      question through the synthesizer.
	//
	//   2. Terminal plan with zero steps — the planner has determined that all
	//      necessary work is already complete (e.g., a continuation phase that
	//      decides "I have all the data I need from prior phases, just synthesize
	//      from completed_steps"). This is a legitimate continuation pattern
	//      observed in real traces (see BUG_TIERED_SELECTION_EMPTY_ON_CONTINUATION.md
	//      Phase 2 trace evidence). The phase loop's existing termination check
	//      at line 2184 handles this naturally: plan.IsTerminal() → break.
	//
	// The pathological case (terminal: false, steps: [], NeedsUserInput == nil)
	// is still rejected because retrying or continuing serves no purpose — the
	// planner has indicated more phases are needed but hasn't proposed any work.
	if len(plan.Steps) == 0 && plan.NeedsUserInput == nil && !plan.IsTerminal() {
		if o.logger != nil {
			o.logger.Warn("Plan has no steps", map[string]interface{}{
				"operation": "plan_validation",
				"plan_id":   plan.PlanID,
				"status":    "empty_plan",
			})
		}
		return fmt.Errorf("plan has no steps - cannot execute empty plan")
	}

	for _, step := range plan.Steps {
		// Check if agent exists
		agents, err := o.discovery.FindService(context.Background(), step.AgentName)
		if err != nil || len(agents) == 0 {
			if o.logger != nil {
				o.logger.Debug("Agent not found during validation", map[string]interface{}{
					"operation":  "capability_validation",
					"step_id":    step.StepID,
					"agent_name": step.AgentName,
					"status":     "agent_not_found",
				})
			}
			return fmt.Errorf("agent %s not found", step.AgentName)
		}

		// Check if capability exists
		if capName, ok := step.Metadata["capability"].(string); ok {
			agentInfo := o.catalog.GetAgent(agents[0].ID)
			if agentInfo == nil {
				return fmt.Errorf("agent %s not in catalog", step.AgentName)
			}

			found := false
			availableCaps := make([]string, len(agentInfo.Capabilities))
			for i, cap := range agentInfo.Capabilities {
				availableCaps[i] = cap.Name
				if cap.Name == capName {
					found = true
				}
			}

			if o.logger != nil {
				o.logger.Debug("Capability validation", map[string]interface{}{
					"operation":              "capability_validation",
					"step_id":                step.StepID,
					"agent_name":             step.AgentName,
					"requested_capability":   capName,
					"available_capabilities": availableCaps,
					"found":                  found,
				})
			}

			if !found {
				return fmt.Errorf("capability %s not found for agent %s", capName, step.AgentName)
			}
		}

		// Check dependencies
		for _, dep := range step.DependsOn {
			found := false
			for _, s := range plan.Steps {
				if s.StepID == dep {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("dependency %s not found for step %s", dep, step.StepID)
			}
		}
	}

	if o.logger != nil {
		o.logger.Info("Plan validation successful", map[string]interface{}{
			"operation":  "plan_validation",
			"plan_id":    plan.PlanID,
			"step_count": len(plan.Steps),
			"status":     "valid",
		})
	}

	return nil
}

// templatePathRegex matches {{step-N.response.data.FIELD}} patterns, capturing the step ID and top-level field name.
var templatePathRegex = regexp.MustCompile(`\{\{(step-\d+)\.response\.data\.([^.}]+)`)

// validateNoUnknownMacros rejects plans containing MALFORMED framework-form
// template attempts — e.g. {{step-1}} (no field path), {{step-1.}} (empty
// field), {{step-99 foo}} (bad chars). Catches the LLM-hallucination pattern
// where the planner reaches for framework template syntax but gets the shape
// wrong.
//
// Scope: only tokens matching frameworkMacroPattern ({{step-...}}) are
// inspected. Tool-specific syntaxes like Prometheus's {{now}} / {{now-7d}},
// Helm values, and JIRA wiki macros deliberately pass through untouched —
// those are the tool's input contract, not the framework's to arbitrate
// (FRAMEWORK_DESIGN_PRINCIPLES.md §"Framework is domain-agnostic").
//
// ORCH-020 RC2 (Issue 11): walks parameters recursively via collectTemplateStrings
// so nested JSON objects and arrays are validated the same way the runtime
// interpolator traverses them.
func validateNoUnknownMacros(plan *RoutingPlan) error {
	if plan == nil {
		return nil
	}
	for _, step := range plan.Steps {
		params, _ := step.Metadata["parameters"].(map[string]interface{})
		for _, strVal := range collectTemplateStrings(params) {
			for _, token := range frameworkMacroPattern.FindAllString(strVal, -1) {
				if !stepOutputTemplatePattern.MatchString(token) {
					return fmt.Errorf(
						"step %s contains malformed framework template %q. "+
							"Framework templates must have the shape {{step-N.response.data.FIELD}} "+
							"where N is a step id and FIELD names a declared output — "+
							"fix the template or remove it",
						step.StepID, token)
				}
			}
		}
	}
	return nil
}

// validateDependencyConsistency enforces that every {{step-X...}} template
// reference in a step's parameters is accompanied by a matching declaration:
//   - for refs whose step ID is in the SAME plan, the ref must appear in
//     depends_on (so the executor's scheduler serializes correctly — this is
//     intentionally stricter than "depends_on OR implicit_deps" because
//     implicit_deps is not consulted by findReadySteps)
//   - for refs to PRIOR phases (not in plan.Steps), the ref must appear in
//     implicit_deps (plan self-documentation; existence is checked separately
//     by RC1 against executedStepCaps)
//   - self-references are always rejected: a step cannot consume its own output
//
// ORCH-020 RC3: rejection triggers regeneration so the LLM fixes its own
// plan without the orchestration wasting retries on a broken dispatch.
// ORCH-020 RC3 (Issue 11): walks parameters recursively.
//
// This is a declaration-consistency check (does the LLM acknowledge its own
// cross-step dependencies?), not an existence check. RC1 does existence
// validation against executedStepCaps separately.
func (o *AIOrchestrator) validateDependencyConsistency(plan *RoutingPlan) error {
	if plan == nil {
		return nil
	}

	// Steps declared in the current plan — used to distinguish in-plan refs
	// (must be in depends_on) from cross-phase refs (must be in implicit_deps
	// OR validated by RC1 against executedStepCaps).
	planStepSet := make(map[string]struct{}, len(plan.Steps))
	for _, step := range plan.Steps {
		planStepSet[step.StepID] = struct{}{}
	}

	for _, step := range plan.Steps {
		params, _ := step.Metadata["parameters"].(map[string]interface{})
		referenced := collectReferencedStepIDs(params)

		declaredDepends := make(map[string]struct{}, len(step.DependsOn))
		for _, d := range step.DependsOn {
			declaredDepends[d] = struct{}{}
		}
		declaredImplicit := make(map[string]struct{}, len(step.ImplicitDeps))
		for _, d := range step.ImplicitDeps {
			declaredImplicit[d] = struct{}{}
		}

		for refID := range referenced {
			if refID == step.StepID {
				// Self-references can never be satisfied (the executor would
				// block forever waiting on a step that depends on itself).
				// Surface one clean error here so the LLM fixes it in a
				// targeted regeneration rather than flailing on downstream
				// symptoms.
				return fmt.Errorf(
					"step %s references its own output {{%s.response.data.*}} — "+
						"remove the self-reference or split into two steps",
					step.StepID, refID)
			}

			if _, inPlan := planStepSet[refID]; inPlan {
				// In-plan reference: must be declared in depends_on so the executor
				// schedules the referenced step before this one.
				if _, ok := declaredDepends[refID]; !ok {
					return fmt.Errorf(
						"step %s references {{%s...}} but does not declare %s in depends_on — "+
							"add %q to %s's depends_on so the executor schedules %s first",
						step.StepID, refID, refID, refID, step.StepID, refID)
				}
				continue
			}

			// Cross-phase reference: must be declared in implicit_deps so the
			// plan is self-describing. RC1 separately verifies the referenced
			// step actually exists in completed results.
			if _, ok := declaredImplicit[refID]; !ok {
				return fmt.Errorf(
					"step %s references {{%s...}} from a prior phase but does not declare %s in implicit_deps — "+
						"add %q to %s's implicit_deps so the plan documents the cross-phase dependency",
					step.StepID, refID, refID, refID, step.StepID)
			}
		}
	}
	return nil
}

// stepCapability describes the agent+capability pair behind a given step ID.
// Used by validateTemplatePaths to resolve {{step-X.response.data.FIELD}}
// references against the capability's declared output schema.
type stepCapability struct {
	agent      string
	capability string
}

// validateTemplatePaths checks that template references in step parameters
// match the output schema of the referenced step's capability, and that the
// referenced step actually exists either in the current plan or in the set of
// prior-phase completed steps (executedStepCaps).
//
// ORCH-020 RC1: executedStepCaps is the authoritative source for existence
// validation of cross-phase references. Passing nil is valid for
// non-iterative plans or the first phase; references to steps outside the
// current plan are then reported as hard errors so the orchestrator can
// regenerate.
//
// ORCH-020 RC1 (Issue 11): parameter scanning uses collectTemplateStrings so
// nested JSON objects and arrays are walked the same way the runtime
// interpolator traverses them.
//
// Returns nil when every {{step-X.response.data.*}} reference points at a
// known step AND (when the referenced capability publishes an output schema)
// the requested field is declared. Returns a human-readable error describing
// the first violation otherwise.
func (o *AIOrchestrator) validateTemplatePaths(plan *RoutingPlan, executedStepCaps map[string]stepCapability) error {
	if plan == nil {
		return nil
	}

	// Union current-plan steps with prior-phase completed steps. The
	// continuation-phase step-ID-conflict check (see the block that follows
	// this validator call in executePhaseLoop) rejects duplicate IDs across
	// phases before RC1 runs, so the "current plan overwrites" behaviour is
	// defense-in-depth — it does not actually trigger in production flows.
	stepCaps := make(map[string]stepCapability)
	for id, cap := range executedStepCaps {
		stepCaps[id] = cap
	}
	for _, step := range plan.Steps {
		if capName, ok := step.Metadata["capability"].(string); ok {
			stepCaps[step.StepID] = stepCapability{agent: step.AgentName, capability: capName}
		}
	}

	// Build agent name → AgentInfo lookup from catalog.
	agentsByName := make(map[string]*AgentInfo)
	for _, info := range o.catalog.GetAgents() {
		agentsByName[info.Registration.Name] = info
	}

	for _, step := range plan.Steps {
		params, _ := step.Metadata["parameters"].(map[string]interface{})
		for _, strVal := range collectTemplateStrings(params) {
			for _, match := range templatePathRegex.FindAllStringSubmatch(strVal, -1) {
				refStepID := match[1]
				refField := match[2]

				refCap, exists := stepCaps[refStepID]
				if !exists {
					return fmt.Errorf(
						"step %s references {{%s.response.data.%s}} but step %s is not in "+
							"the current plan or any completed prior phase — add a step with id %s "+
							"that produces the required field, or remove the reference",
						step.StepID, refStepID, refField, refStepID, refStepID)
				}

				// Carve-out: if the referenced step comes from executedStepCaps
				// but its AgentName / Capability couldn't be populated (e.g.
				// the prior-phase step failed at agent discovery before
				// StepResult fields were set), we can't check its output
				// schema here. Existence was already verified above. Any
				// still-unresolved {{…}} that survives runtime interpolation
				// is caught by RC6's pre-dispatch guard.
				agentInfo := agentsByName[refCap.agent]
				if agentInfo == nil {
					continue
				}

				for _, cap := range agentInfo.Capabilities {
					if cap.Name != refCap.capability || len(cap.Returns.Fields) == 0 {
						continue
					}
					fieldFound := false
					availableFields := make([]string, len(cap.Returns.Fields))
					for i, f := range cap.Returns.Fields {
						availableFields[i] = f.Name
						if f.Name == refField {
							fieldFound = true
						}
					}
					if !fieldFound {
						return fmt.Errorf(
							"step %s references {{%s.response.data.%s}} but capability %s/%s returns fields: [%s]",
							step.StepID, refStepID, refField,
							refCap.agent, refCap.capability,
							strings.Join(availableFields, ", "),
						)
					}
				}
			}
		}
	}
	return nil
}

// regeneratePlan attempts to fix a plan based on validation errors
func (o *AIOrchestrator) regeneratePlan(ctx context.Context, request string, requestID string, validationErr error) (*RoutingPlan, error) {
	var err error
	ctx, err = prepareOrchestrationBoundary(ctx, boundaryRegeneration)
	if err != nil {
		return nil, fmt.Errorf("prepare regeneration boundary: %w", err)
	}
	// Check if AI client is available
	if o.aiClient == nil {
		return nil, fmt.Errorf("AI client not configured for plan regeneration")
	}

	// Inject requestID into context for child components
	ctx = WithRequestID(ctx, requestID)

	basePromptResult, err := o.buildPlanningPrompt(ctx, request)
	if err != nil {
		return nil, err
	}
	validationFeedback, err := prepareValidationFeedback(ctx, promptRegeneration, validationErr)
	if err != nil {
		return nil, fmt.Errorf("prepare regeneration feedback: %w", err)
	}

	prompt := fmt.Sprintf(`%s

The previous plan failed validation with error: %s

Please generate a corrected plan that addresses this error.`,
		basePromptResult.Prompt, validationFeedback)

	planOpts := o.planAIOptions(0.2, basePromptResult.SystemPrompt)

	llmStartTime := time.Now()
	invocation := aiInvocation{
		Purpose:        "planning",
		Prompt:         prompt,
		Options:        planOpts,
		SystemSource:   o.planPromptSystemSource(),
		DeferRecording: o.debugStore != nil,
	}
	invocationResult, err := invokeAI(ctx, o.aiClient, invocation)
	var aiResponse *core.AIResponse
	if invocationResult != nil {
		aiResponse = invocationResult.Response
	}
	effective := effectiveAIRequestForDebug(invocationResult, invocation)
	llmDuration := time.Since(llmStartTime)

	if err != nil {
		errModel, errProvider := effectiveAIIdentity(invocationResult, aiResponse, err)
		o.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:         "plan_regeneration_fallback",
			Timestamp:    llmStartTime,
			DurationMs:   llmDuration.Milliseconds(),
			Prompt:       effective.Prompt,
			SystemPrompt: effective.SystemPrompt,
			Temperature:  effectiveAITemperature(effective, planOpts.Temperature),
			MaxTokens:    effectiveAIMaxTokens(effective, planOpts.MaxTokens),
			Model:        errModel,
			Provider:     errProvider,
			Success:      false,
			Error:        err.Error(),
		})
		return nil, err
	}
	core.RecordTokenUsage(ctx, "correction", aiResponse.Usage)

	model, provider := effectiveAIIdentity(invocationResult, aiResponse, nil)
	o.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "plan_regeneration_fallback",
		Timestamp:        llmStartTime,
		DurationMs:       llmDuration.Milliseconds(),
		Prompt:           effective.Prompt,
		SystemPrompt:     effective.SystemPrompt,
		Temperature:      effectiveAITemperature(effective, planOpts.Temperature),
		MaxTokens:        effectiveAIMaxTokens(effective, planOpts.MaxTokens),
		Model:            model,
		Provider:         provider,
		Response:         aiResponse.Content,
		PromptTokens:     aiResponse.Usage.PromptTokens,
		CompletionTokens: aiResponse.Usage.CompletionTokens,
		TotalTokens:      aiResponse.Usage.TotalTokens,
		Success:          true,
	})

	return o.parsePlan(aiResponse.Content)
}

// extractAgentsFromPlan gets list of agents involved in a plan
func (o *AIOrchestrator) extractAgentsFromPlan(plan *RoutingPlan) []string {
	agentSet := make(map[string]bool)
	for _, step := range plan.Steps {
		agentSet[step.AgentName] = true
	}

	agents := make([]string, 0, len(agentSet))
	for agent := range agentSet {
		agents = append(agents, agent)
	}
	return agents
}

// ExecutePlan executes a pre-defined routing plan.
// This method sets up request_id in context baggage for observability,
// ensuring downstream components can correlate logs with traces.
func (o *AIOrchestrator) ExecutePlan(ctx context.Context, plan *RoutingPlan) (*ExecutionResult, error) {
	if err := o.rejectIfConstructionFailed(ctx, "execute_plan"); err != nil {
		return nil, err
	}
	if o.executor == nil {
		return nil, fmt.Errorf("executor not configured")
	}

	// Generate request_id for this plan execution
	requestID := o.newRequestID()
	defer o.releaseExecutionRecorder(requestID)

	// Add request_id to context baggage so downstream components (executor,
	// tools, etc.) can access it via telemetry.GetBaggage() and include it in their logs
	ctx = telemetry.WithBaggage(ctx, "request_id", requestID)

	// Set original_request_id for trace correlation across HITL resumes.
	// On initial requests: original_request_id = request_id (same value)
	// On resume requests: original_request_id is already set via header, don't overwrite
	if bag := telemetry.GetBaggage(ctx); bag == nil || bag["original_request_id"] == "" {
		ctx = telemetry.WithBaggage(ctx, "original_request_id", requestID)
	}

	// Add request_id to context for GetRequestID() - used by HITL controller
	ctx = WithRequestID(ctx, requestID)

	return o.executor.Execute(ctx, plan)
}

// ExecutePlanWithSynthesis executes a pre-defined routing plan and synthesizes the results.
// This method provides full observability by:
// - Recording synthesis LLM calls to LLM Debug Store
// - Storing execution to Execution Store for DAG visualization
// - Returning a complete OrchestratorResponse
//
// For custom synthesis logic, use ExecutePlan() instead.
//
// Follows patterns from:
// - ARCHITECTURE.md: Telemetry span with NoOp fallback, synthesizer nil check
// - telemetry/ARCHITECTURE.md: Context propagation, span attributes
// - docs/observability/DISTRIBUTED_TRACING_GUIDE.md: RecordSpanError, AddSpanEvent, Counter with module label
func (o *AIOrchestrator) ExecutePlanWithSynthesis(
	ctx context.Context,
	plan *RoutingPlan,
	originalRequest string,
) (*OrchestratorResponse, error) {
	if err := o.rejectIfConstructionFailed(ctx, "execute_plan_with_synthesis"); err != nil {
		return nil, err
	}
	startTime := time.Now()

	// Validate plan is not nil (fail fast before any telemetry setup)
	if plan == nil {
		return nil, fmt.Errorf("plan cannot be nil")
	}

	// Generate request_id for this workflow execution
	requestID := o.newRequestID()
	defer o.releaseExecutionRecorder(requestID)

	// Add request_id to context baggage so downstream components (AI client, synthesizer,
	// micro_resolver, etc.) can access it via telemetry.GetBaggage() and include it in their logs
	ctx = telemetry.WithBaggage(ctx, "request_id", requestID)

	// Set original_request_id for trace correlation across HITL resumes.
	// On initial requests: original_request_id = request_id (same value)
	// On resume requests: original_request_id is already set via header, don't overwrite
	if bag := telemetry.GetBaggage(ctx); bag == nil || bag["original_request_id"] == "" {
		ctx = telemetry.WithBaggage(ctx, "original_request_id", requestID)
	}

	// Add request_id to context for GetRequestID() - used by HITL controller
	ctx = WithRequestID(ctx, requestID)

	// Inject token usage accumulator for metering
	ctx, usageAcc := core.WithTokenUsageAccumulator(ctx)

	// Start telemetry span if telemetry is available (nil-safe per FRAMEWORK_DESIGN_PRINCIPLES.md)
	var span core.Span
	if o.telemetry != nil {
		ctx, span = o.telemetry.StartSpan(ctx, "orchestrator.execute_plan_with_synthesis")
		defer span.End()
	} else {
		// Create a no-op span to avoid nil pointer dereferences
		span = &core.NoOpSpan{}
	}

	// Set span attributes for distributed tracing searchability
	// (per telemetry/ARCHITECTURE.md Pattern 3: Context Propagation)
	span.SetAttribute("request_id", requestID)
	span.SetAttribute("mode", string(ModeWorkflow))
	span.SetAttribute("plan_id", plan.PlanID)
	span.SetAttribute("step_count", len(plan.Steps))

	if o.logger != nil {
		o.logger.InfoWithContext(ctx, "Starting workflow execution with synthesis", map[string]interface{}{
			"operation":  "execute_plan_with_synthesis",
			"request_id": requestID,
			"plan_id":    plan.PlanID,
			"step_count": len(plan.Steps),
		})
	}

	// Validate executor is configured
	if o.executor == nil {
		err := fmt.Errorf("executor not configured")
		telemetry.RecordSpanError(ctx, err)

		// Emit counter metric with module label (per DISTRIBUTED_TRACING_GUIDE.md Pattern 5)
		telemetry.Counter("workflow.execution.total",
			"module", telemetry.ModuleOrchestration,
			"status", "error",
			"phase", "validation",
		)

		if o.logger != nil {
			o.logger.ErrorWithContext(ctx, "Executor not configured", map[string]interface{}{
				"operation":  "execute_plan_with_synthesis",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		return nil, err
	}

	// Step 1: Execute the plan (uses SmartExecutor, which records micro_resolution, etc.)
	// The context now carries request_id baggage, so all downstream LLM calls will use it
	result, err := o.executor.Execute(ctx, plan)
	if err != nil {
		// Record error on span (per DISTRIBUTED_TRACING_GUIDE.md Pattern 4)
		telemetry.RecordSpanError(ctx, err)

		// Add span event with request_id first (per DISTRIBUTED_TRACING_GUIDE.md Pattern 6)
		telemetry.AddSpanEvent(ctx, "workflow.execution.error",
			attribute.String("request_id", requestID),
			attribute.String("plan_id", plan.PlanID),
			attribute.String("error", err.Error()),
			attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
		)

		// Emit counter metric with module label (per DISTRIBUTED_TRACING_GUIDE.md Pattern 5)
		telemetry.Counter("workflow.execution.total",
			"module", telemetry.ModuleOrchestration,
			"status", "error",
			"phase", "execution",
		)

		// Store failed execution for DAG visualization
		o.storeExecutionAsync(ctx, originalRequest, requestID, plan, result, nil)

		// Log with operation field (per DISTRIBUTED_TRACING_GUIDE.md Pattern 2)
		if o.logger != nil {
			o.logger.ErrorWithContext(ctx, "Workflow execution failed", map[string]interface{}{
				"operation":   "execute_plan_with_synthesis",
				"request_id":  requestID,
				"plan_id":     plan.PlanID,
				"error":       err.Error(),
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}

		// Update metrics for failed execution
		o.updateMetrics(time.Since(startTime), false)
		return nil, fmt.Errorf("execution failed: %w", err)
	}

	// Store successful execution for DAG visualization
	o.storeExecutionAsync(ctx, originalRequest, requestID, plan, result, nil)

	// Log execution completion (follows ProcessRequest pattern from orchestrator.go:1118-1126)
	if o.logger != nil {
		failedSteps := 0
		if result != nil && !result.Success {
			for _, step := range result.Steps {
				if !step.Success {
					failedSteps++
				}
			}
		}
		o.logger.InfoWithContext(ctx, "Workflow execution completed", map[string]interface{}{
			"operation":    "workflow_execution",
			"request_id":   requestID,
			"plan_id":      plan.PlanID,
			"success":      result != nil && result.Success,
			"failed_steps": failedSteps,
			"duration_ms":  time.Since(startTime).Milliseconds(),
		})
	}

	// Step 2: Synthesize using orchestrator's synthesizer (auto-records to LLM Debug Store)
	// Synthesizer nil check - fall back to raw results formatting if synthesizer unavailable
	var synthesizedResponse string
	if o.synthesizer != nil {
		synthesizedResponse, err = o.synthesizer.Synthesize(ctx, originalRequest, result)
		if err != nil {
			// Record synthesis error on span (per DISTRIBUTED_TRACING_GUIDE.md Pattern 4)
			telemetry.RecordSpanError(ctx, err)

			// Add span event with request_id first (per DISTRIBUTED_TRACING_GUIDE.md Pattern 6)
			telemetry.AddSpanEvent(ctx, "workflow.synthesis.error",
				attribute.String("request_id", requestID),
				attribute.String("plan_id", plan.PlanID),
				attribute.String("error", err.Error()),
				attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
			)

			// Emit counter metric with module label (per DISTRIBUTED_TRACING_GUIDE.md Pattern 5)
			telemetry.Counter("workflow.execution.total",
				"module", telemetry.ModuleOrchestration,
				"status", "error",
				"phase", "synthesis",
			)

			// Log with operation field (per DISTRIBUTED_TRACING_GUIDE.md Pattern 2)
			if o.logger != nil {
				o.logger.ErrorWithContext(ctx, "Workflow synthesis failed", map[string]interface{}{
					"operation":   "execute_plan_with_synthesis",
					"request_id":  requestID,
					"plan_id":     plan.PlanID,
					"error":       err.Error(),
					"duration_ms": time.Since(startTime).Milliseconds(),
				})
			}

			o.updateMetrics(time.Since(startTime), false)
			return nil, fmt.Errorf("synthesis failed: %w", err)
		}
	} else {
		// Fallback: format raw results (no LLM synthesis)
		synthesizedResponse = formatRawExecutionResults(result)
	}

	// Build response
	totalUsage, usageByPhase := usageAcc.Snapshot()
	response := &OrchestratorResponse{
		RequestID:       requestID,
		OriginalRequest: originalRequest,
		Response:        synthesizedResponse,
		RoutingMode:     ModeWorkflow,
		ExecutionTime:   time.Since(startTime),
		AgentsInvolved:  o.extractAgentsFromPlan(plan),
		Confidence:      0.95,
		Steps:           result.Steps, // Include step-level details for API consumers
		Usage:           &totalUsage,
		UsageByPhase:    usageByPhase,
	}

	// Update metrics and history (follows ProcessRequest pattern from orchestrator.go:1147-1149)
	o.updateMetrics(response.ExecutionTime, true)
	o.addToHistory(response)

	// Emit success counter (per DISTRIBUTED_TRACING_GUIDE.md Pattern 5)
	telemetry.Counter("workflow.execution.total",
		"module", telemetry.ModuleOrchestration,
		"status", "success",
	)

	if o.logger != nil {
		o.logger.InfoWithContext(ctx, "Workflow execution with synthesis completed", map[string]interface{}{
			"operation":         "execute_plan_with_synthesis_complete",
			"request_id":        requestID,
			"success":           true,
			"total_duration_ms": time.Since(startTime).Milliseconds(),
		})
	}

	// Record success metrics if telemetry is available (follows ProcessRequest pattern from orchestrator.go:1161-1168)
	if o.telemetry != nil {
		o.telemetry.RecordMetric("orchestrator.requests.success", 1, map[string]string{
			"module": telemetry.ModuleOrchestration,
			"mode":   string(ModeWorkflow),
		})
		o.telemetry.RecordMetric("orchestrator.latency_ms", float64(time.Since(startTime).Milliseconds()), map[string]string{
			"module":    telemetry.ModuleOrchestration,
			"operation": "execute_plan_with_synthesis",
		})
	}

	return response, nil
}

// formatRawExecutionResults formats execution results without AI synthesis.
// Used as fallback when synthesizer is unavailable.
func formatRawExecutionResults(result *ExecutionResult) string {
	if result == nil {
		return ""
	}
	var output string
	for _, step := range result.Steps {
		status := "Success"
		if !step.Success {
			status = fmt.Sprintf("Failed: %s", step.Error)
		}
		output += fmt.Sprintf("**%s** (%s): %s\n%s\n\n", step.AgentName, step.StepID, status, step.Response)
	}
	return output
}

// GetExecutionHistory returns recent execution history
func (o *AIOrchestrator) GetExecutionHistory() []ExecutionRecord {
	o.historyMutex.RLock()
	defer o.historyMutex.RUnlock()

	historyCopy := make([]ExecutionRecord, len(o.history))
	copy(historyCopy, o.history)
	return historyCopy
}

// GetMetrics returns orchestrator metrics
func (o *AIOrchestrator) GetMetrics() OrchestratorMetrics {
	o.metricsMutex.RLock()
	defer o.metricsMutex.RUnlock()

	return *o.metrics
}

// Helper functions

func (o *AIOrchestrator) updateMetrics(duration time.Duration, success bool) {
	o.metricsMutex.Lock()
	defer o.metricsMutex.Unlock()

	o.metrics.TotalRequests++
	if success {
		o.metrics.SuccessfulRequests++
	} else {
		o.metrics.FailedRequests++
	}

	// Update latency metrics (simplified for MVP)
	if o.metrics.AverageLatency == 0 {
		o.metrics.AverageLatency = duration
	} else {
		o.metrics.AverageLatency = (o.metrics.AverageLatency + duration) / 2
	}
	o.metrics.LastRequestTime = time.Now()
}

func (o *AIOrchestrator) addToHistory(response *OrchestratorResponse) {
	o.historyMutex.Lock()
	defer o.historyMutex.Unlock()

	record := ExecutionRecord{
		RequestID:      response.RequestID,
		Timestamp:      time.Now(),
		Request:        response.OriginalRequest,
		Response:       response.Response,
		RoutingMode:    response.RoutingMode,
		AgentsInvolved: response.AgentsInvolved,
		ExecutionTime:  response.ExecutionTime,
		Success:        len(response.Errors) == 0,
		Errors:         response.Errors,
	}

	o.history = append(o.history, record)

	// Trim history if needed
	if len(o.history) > o.config.HistorySize {
		o.history = o.history[1:]
	}
}

// findJSONStart finds the start of JSON in a string
//
//nolint:unused // kept for legacy parser tests and design-doc references
func findJSONStart(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			return i
		}
	}
	return -1
}

// findJSONEndStringSafe finds the end of JSON while properly handling strings.
// This correctly skips braces that appear inside quoted strings.
func findJSONEndStringSafe(s string, start int) int {
	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		c := s[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue // Skip characters inside strings
		}

		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// markdownCodeBlockRegex removes markdown code block fences from LLM responses.
// Handles both ```json and ``` formats (case-insensitive).
var markdownCodeBlockRegex = regexp.MustCompile("(?si)```(?:json)?\\s*([\\s\\S]*?)\\s*```")

// markdownBoldRegex matches **bold** patterns inside JSON string values
var markdownBoldRegex = regexp.MustCompile(`\*\*([^*]+)\*\*`)

// Note: Italic handling uses manual parsing in stripMarkdownFromJSON() rather than regex
// because single asterisks are harder to match reliably without false positives.

// cleanLLMResponse aggressively cleans LLM responses to extract valid JSON.
// It handles:
// - Markdown code blocks (```json ... ```)
// - Bold markers inside string values (**text** → text)
// - Italic markers inside string values (*text* → text)
// - Intro text like "Here's the plan:"
//
// This is a defensive measure since LLMs (especially Gemini) often add markdown
// formatting despite explicit instructions not to. See research:
// - https://community.openai.com/t/how-to-prevent-gpt-from-outputting-responses-in-markdown-format/961314
// - https://datachain.ai/blog/enforcing-json-outputs-in-commercial-llms
func cleanLLMResponse(s string) string {
	// Step 1: Try to extract from code blocks first (most reliable)
	if matches := markdownCodeBlockRegex.FindStringSubmatch(s); len(matches) > 1 {
		s = strings.TrimSpace(matches[1])
	} else {
		// Step 2: Find the JSON object directly by locating { and its matching }
		// This handles cases where LLM wraps JSON in other text
		jsonStart := strings.Index(s, "{")
		if jsonStart == -1 {
			return s
		}

		// Find the matching closing brace using string-safe detection
		jsonEnd := findJSONEndStringSafe(s, jsonStart)
		if jsonEnd == -1 {
			return s
		}

		// Extract just the JSON portion
		s = strings.TrimSpace(s[jsonStart:jsonEnd])
	}

	// Step 3: Strip markdown formatting from string values
	// This handles cases where LLM puts **bold** or *italic* inside JSON strings
	s = stripMarkdownFromJSON(s)

	return s
}

// stripMarkdownFromJSON removes markdown bold/italic formatting from JSON string values.
// Converts "**Paris**" → "Paris" and "*weather*" → "weather"
// This is safe for JSON because:
// - ** and * inside quoted strings are the only place markdown appears
// - We only strip when the pattern matches complete words
func stripMarkdownFromJSON(s string) string {
	// Strip bold: **text** → text
	s = markdownBoldRegex.ReplaceAllString(s, "$1")

	// Strip italic: *text* → text (but not **)
	// We need to be more careful here to avoid breaking valid content
	// Only strip if it looks like markdown (word boundaries)
	result := strings.Builder{}
	result.Grow(len(s))

	i := 0
	for i < len(s) {
		// Look for potential italic marker
		if s[i] == '*' && i+1 < len(s) && s[i+1] != '*' {
			// Check if this looks like italic markdown: *word*
			// Must have matching * and contain actual content
			endIdx := strings.Index(s[i+1:], "*")
			if endIdx > 0 && endIdx < 100 { // Reasonable word length
				// Check the end isn't a double asterisk
				fullEndIdx := i + 1 + endIdx
				if fullEndIdx+1 >= len(s) || s[fullEndIdx+1] != '*' {
					// Check content doesn't contain special chars that would make this not markdown
					content := s[i+1 : fullEndIdx]
					if !strings.ContainsAny(content, "\n\t{}[]\"") && len(strings.TrimSpace(content)) > 0 {
						// This looks like italic markdown, strip the asterisks
						result.WriteString(content)
						i = fullEndIdx + 1
						continue
					}
				}
			}
		}
		result.WriteByte(s[i])
		i++
	}

	return result.String()
}

// truncateString truncates a string to maxLen characters for logging
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// countStepOutcomes splits a step list into success/failure counts in one pass.
// Used by the synthesis span to record how many of the inputs to synthesis
// actually succeeded — a high failed_step_count on a "successful" trace is the
// signal that the orchestrator continued past partial failures.
func countStepOutcomes(steps []StepResult) (success, failed int) {
	for i := range steps {
		if steps[i].Success {
			success++
		} else {
			failed++
		}
	}
	return
}

// newRequestID is the single prefix-aware identifier generator for every
// public orchestration execution entry point.
func (o *AIOrchestrator) newRequestID() string {
	prefix := "orch"
	if o != nil && o.config != nil && o.config.RequestIDPrefix != "" {
		prefix = o.config.RequestIDPrefix
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// getMapKeys extracts keys from a map for logging
func getMapKeys(m map[string]interface{}) []string {
	if m == nil {
		return []string{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
