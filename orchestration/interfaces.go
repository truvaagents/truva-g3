package orchestration

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// RouterMode defines the routing strategy
// Note: This is currently only used for metrics and logging, not actual routing behavior
type RouterMode string

const (
	ModeAutonomous RouterMode = "autonomous" // AI-driven orchestration
	ModeWorkflow   RouterMode = "workflow"   // Workflow-based execution (separate system)
)

// RoutingStep represents a single step in a routing plan
type RoutingStep struct {
	StepID      string   `json:"step_id"`
	AgentName   string   `json:"agent_name"`
	Namespace   string   `json:"namespace"`
	Instruction string   `json:"instruction"`
	DependsOn   []string `json:"depends_on,omitempty"`
	// ImplicitDeps names step IDs from PRIOR phases that this step references
	// via {{step-X.response.data.field}} templates. depends_on is reserved for
	// same-phase dependencies (the executor's scheduler), so cross-phase refs
	// need a separate declaration channel. The field is advisory: the scheduler
	// does not use it (prior-phase steps are already complete), and existence
	// validation is performed against actual completed-step results, not this
	// list (see ORCH-020 RC1/RC7).
	ImplicitDeps []string               `json:"implicit_deps,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// RoutingPlan represents a complete execution plan
type RoutingPlan struct {
	PlanID          string        `json:"plan_id"`
	OriginalRequest string        `json:"original_request"`
	Mode            RouterMode    `json:"mode"`
	Steps           []RoutingStep `json:"steps"`
	CreatedAt       time.Time     `json:"created_at"`

	// Iterative planning fields
	Terminal         *bool  `json:"terminal,omitempty"`          // nil=terminal (backward compat), true=terminal, false=continuation needed
	ContinuationNote string `json:"continuation_note,omitempty"` // LLM explains why continuation is needed
	PhaseNumber      int    `json:"phase_number,omitempty"`      // Set by orchestrator, not LLM (1-indexed)

	// Clarification escape valve (ORCH-018). When set, the planner is signaling
	// that it cannot make further progress without information from the user.
	// The orchestrator terminates the phase loop and routes the question through
	// the synthesizer instead of starting another phase. When this field is set,
	// Steps SHOULD be empty for this plan.
	NeedsUserInput *ClarificationRequest `json:"needs_user_input,omitempty"`
}

// ClarificationRequest is the planner's structured request for user input.
// Populated when the next planning step depends on information that no
// available tool can produce — only the user can.
// (ORCH-018)
type ClarificationRequest struct {
	// Question is the natural-language question to surface to the user.
	// Required when ClarificationRequest is non-nil.
	Question string `json:"question"`

	// MissingFields is an optional list of structured field names the planner
	// is waiting on. Useful for UI consumers that want to render quick-reply
	// chips or form fields. Example: ["travel_dates", "destination_cities"].
	MissingFields []string `json:"missing_fields,omitempty"`

	// PartialProgress is an optional brief description of work already
	// completed in this turn. The synthesizer uses it to summarize discoveries
	// before asking the question. Example: "I gathered country information
	// and travel advisories for both countries."
	PartialProgress string `json:"partial_progress,omitempty"`
}

// IsTerminal returns whether this plan is complete (no continuation needed).
// Default is true for backward compatibility — plans without the field are terminal.
func (p *RoutingPlan) IsTerminal() bool {
	if p.Terminal == nil {
		return true
	}
	return *p.Terminal
}

// Orchestrator coordinates multi-agent interactions
type Orchestrator interface {
	// ProcessRequest handles a natural language request by orchestrating multiple agents
	ProcessRequest(ctx context.Context, request string, metadata map[string]interface{}) (*OrchestratorResponse, error)

	// ExecutePlan executes a pre-defined routing plan (raw results, no synthesis)
	ExecutePlan(ctx context.Context, plan *RoutingPlan) (*ExecutionResult, error)

	// ExecutePlanWithSynthesis executes a pre-defined routing plan with synthesis.
	// Unlike ExecutePlan(), this method:
	// 1. Uses the orchestrator's synthesizer (which auto-records to LLM Debug Store)
	// 2. Returns a complete OrchestratorResponse (not raw ExecutionResult)
	// 3. Stores execution to ExecutionStore for DAG visualization
	// 4. Sets up context baggage for request_id propagation
	//
	// Use this when you want workflow mode with full observability.
	// Use ExecutePlan() when you need raw results for custom synthesis logic.
	ExecutePlanWithSynthesis(ctx context.Context, plan *RoutingPlan, originalRequest string) (*OrchestratorResponse, error)

	// GetExecutionHistory returns recent execution history
	GetExecutionHistory() []ExecutionRecord

	// GetMetrics returns orchestrator metrics
	GetMetrics() OrchestratorMetrics
}

// OrchestratorResponse represents the final synthesized response
type OrchestratorResponse struct {
	RequestID       string                 `json:"request_id"`
	OriginalRequest string                 `json:"original_request"`
	Response        string                 `json:"response"`
	RoutingMode     RouterMode             `json:"routing_mode"`
	ExecutionTime   time.Duration          `json:"execution_time"`
	AgentsInvolved  []string               `json:"agents_involved"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	Errors          []string               `json:"errors,omitempty"`
	Confidence      float64                `json:"confidence"`
	// Steps contains individual step results (populated by ExecutePlanWithSynthesis)
	Steps []StepResult `json:"steps,omitempty"`
	// Usage contains aggregated token usage across all LLM calls in this request
	Usage *core.TokenUsage `json:"usage,omitempty"`
	// UsageByPhase breaks down token usage by orchestration phase
	UsageByPhase map[string]core.TokenUsage `json:"usage_by_phase,omitempty"`

	// Clarification is set when the planner needs user input to continue.
	// The natural-language question is also woven into the Response field by
	// the synthesizer; this structured field is provided for sophisticated
	// UI consumers that want to render quick-reply chips or form prompts.
	// (ORCH-018)
	Clarification *ClarificationRequest `json:"clarification,omitempty"`
}

// StreamingOrchestratorResponse extends OrchestratorResponse for streaming scenarios
// It includes additional fields to track streaming-specific state and progress
type StreamingOrchestratorResponse struct {
	OrchestratorResponse

	// Streaming-specific fields
	ChunksDelivered int  `json:"chunks_delivered"` // Number of chunks successfully delivered
	StreamCompleted bool `json:"stream_completed"` // Whether streaming finished successfully
	PartialContent  bool `json:"partial_content"`  // True if response was truncated due to error/cancellation

	// Enhanced tracking fields
	StepResults  []StepResult `json:"step_results,omitempty"`  // Detailed results from each execution step
	FinishReason string       `json:"finish_reason,omitempty"` // Why streaming stopped (e.g., "stop", "length", "cancelled")
}

// Executor handles the execution of routing plans
type Executor interface {
	// Execute runs a routing plan and collects agent responses
	Execute(ctx context.Context, plan *RoutingPlan) (*ExecutionResult, error)

	// ExecuteStep executes a single routing step
	ExecuteStep(ctx context.Context, step RoutingStep) (*StepResult, error)

	// SetMaxConcurrency sets the maximum number of parallel executions
	SetMaxConcurrency(max int)
}

// ExecutionResult contains the results from executing a routing plan
type ExecutionResult struct {
	PlanID        string                 `json:"plan_id"`
	Steps         []StepResult           `json:"steps"`
	Success       bool                   `json:"success"`
	TotalDuration time.Duration          `json:"total_duration"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`

	// Iterative planning metadata
	PhaseCount int `json:"phase_count,omitempty"` // Number of phases executed (1 = single-shot)

	// ClarificationNeeded is populated by executePhaseLoop when the planner
	// emits NeedsUserInput. Consumed by the synthesizer to produce a
	// clarification-aware response (ORCH-018). Nil for normal completions.
	ClarificationNeeded *ClarificationRequest `json:"clarification_needed,omitempty"`
}

// Metadata keys for multi-phase execution data.
// Used to transport phase context through ExecutionResult.Metadata
// into StoredExecution without changing storeExecutionAsync's signature.
const (
	MetadataKeyPhasePlans        = "phase_plans"              // []*RoutingPlan
	MetadataKeyPhaseCount        = "phase_count"              // int
	MetadataKeyForcedTerminal    = "forced_terminal"          // bool
	MetadataKeyPlanRegenerations = "plan_regeneration_events" // []map[string]interface{}
)

// Phase context keys for context-aware tiered re-selection (Issue 7).
// Used to transport phase execution context through CapabilityProvider.GetCapabilities
// metadata parameter into TieredCapabilityProvider.buildContinuationSelectionPrompt.
//
// Producer: buildContinuationPrompt (orchestrator.go) constructs the map.
// Consumer: buildContinuationSelectionPrompt (tiered_capability_provider.go) reads the map.
// Discriminator: selectRelevantTools branches on PhaseContextKeyPhaseNumber != nil.
const (
	PhaseContextKeyPhaseNumber      = "phase_number"      // int (1-indexed, always >= 2 for Phase 2+)
	PhaseContextKeyContinuationNote = "continuation_note" // string (LLM's reason for continuation)
	PhaseContextKeyPriorToolsUsed   = "prior_tools_used"  // []string (sorted, deduplicated agent names)
	PhaseContextKeyPriorToolIDs     = "prior_tool_ids"    // []string (sorted "agent/capability" IDs from completed steps) — ORCH-018 Layer 2
	PhaseContextKeyCompletedSummary = "completed_summary" // string (compact result summary, max 500 chars)
)

// StepResult contains the result from executing a single step
type StepResult struct {
	StepID      string                 `json:"step_id"`
	AgentName   string                 `json:"agent_name"`
	Capability  string                 `json:"capability,omitempty"` // Resolved capability name (e.g., "create_issue", "rollout_restart")
	Namespace   string                 `json:"namespace"`
	Instruction string                 `json:"instruction"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"` // Resolved request payload sent to tool/agent
	Response    string                 `json:"response"`
	Success     bool                   `json:"success"`
	Error       string                 `json:"error,omitempty"`
	Duration    time.Duration          `json:"duration"`
	Attempts    int                    `json:"attempts"`
	StartTime   time.Time              `json:"start_time"`
	EndTime     time.Time              `json:"end_time"`
	// Metadata holds optional step-level data (e.g., HITL checkpoint info, resolution metadata)
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	// Skipped indicates the step was skipped by post-orchestrator plan refinement (ORCH-015).
	// When true, the orchestrator step already performed the equivalent action internally.
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
	// RetryExhausted is true when the retry loop exited because the step's
	// maxAttempts budget was fully consumed on an unsuccessful attempt.
	// Distinct from Attempts — Attempts is a count; RetryExhausted is the
	// binary "we stopped retrying because budget ran out" signal, which
	// correctly accounts for per-step variable budgets (orchestrator
	// capabilities are forced to maxAttempts=1, so a single unsuccessful
	// attempt IS exhausted for them). Consumed by RC9's
	// summarizeUpstreamFailurePattern to decide whether an upstream failure
	// looks persistent vs transient.
	RetryExhausted bool `json:"retry_exhausted,omitempty"`
}

// ResolutionMetadata captures which layer resolved each parameter.
// Used for research analytics and debugging the 4-layer resolution system.
type ResolutionMetadata struct {
	// Per-parameter attribution (ordered list for UI display)
	Parameters []ParameterResolution `json:"parameters"`

	// Layer usage summary (for aggregation/research)
	AutoWiredCount     int `json:"auto_wired_count"`
	MicroResolvedCount int `json:"micro_resolved_count"`
	SemanticRetryCount int `json:"semantic_retry_count,omitempty"`
	UserProvidedCount  int `json:"user_provided_count,omitempty"`

	// Timing for cost analysis
	AutoWiringDurationUs      int64 `json:"auto_wiring_duration_us"`                // Microseconds (Layer 1 is fast)
	MicroResolutionDurationMs int64 `json:"micro_resolution_duration_ms,omitempty"` // Milliseconds (LLM call)

	// Source data stats
	SourceDataKeyCount int      `json:"source_data_key_count"`
	DependencyStepIDs  []string `json:"dependency_step_ids,omitempty"`
}

// ParameterResolution describes how a single parameter was resolved.
type ParameterResolution struct {
	Name      string      `json:"name"`
	Layer     string      `json:"layer"`                // "auto_wire", "micro_resolution", "semantic_retry", "user_provided", "default"
	MatchType string      `json:"match_type"`           // "exact", "case_insensitive", "type_coerced", "nested_extraction", "semantic", "computed"
	SourceKey string      `json:"source_key,omitempty"` // Original key in source data (for auto_wire)
	Value     interface{} `json:"value"`
}

// AutoWireResult is the result of auto-wiring parameter resolution.
type AutoWireResult struct {
	Parameters map[string]interface{}
	Resolved   []ParameterResolution // Per-parameter metadata
	Unmapped   []string
	DurationUs int64
}

// HybridResolutionResult is the result of hybrid parameter resolution.
type HybridResolutionResult struct {
	Parameters map[string]interface{}
	Metadata   *ResolutionMetadata
}

// Synthesizer combines multiple agent responses into a coherent result
type Synthesizer interface {
	// Synthesize combines agent responses into a final response
	Synthesize(ctx context.Context, request string, results *ExecutionResult) (string, error)

	// SetStrategy sets the synthesis strategy
	SetStrategy(strategy SynthesisStrategy)
}

// SynthesisStrategy defines how responses are combined
type SynthesisStrategy string

const (
	// StrategyLLM uses an LLM to synthesize responses
	StrategyLLM SynthesisStrategy = "llm"

	// StrategyTemplate uses predefined templates
	StrategyTemplate SynthesisStrategy = "template"

	// StrategySimple concatenates responses
	StrategySimple SynthesisStrategy = "simple"

	// StrategyCustom uses a custom synthesis function
	StrategyCustom SynthesisStrategy = "custom"
)

// ExecutionRecord represents a historical execution
type ExecutionRecord struct {
	RequestID      string        `json:"request_id"`
	Timestamp      time.Time     `json:"timestamp"`
	Request        string        `json:"request"`
	Response       string        `json:"response"`
	RoutingMode    RouterMode    `json:"routing_mode"`
	AgentsInvolved []string      `json:"agents_involved"`
	ExecutionTime  time.Duration `json:"execution_time"`
	Success        bool          `json:"success"`
	Errors         []string      `json:"errors,omitempty"`
}

// OrchestratorMetrics contains performance metrics
type OrchestratorMetrics struct {
	TotalRequests      int64         `json:"total_requests"`
	SuccessfulRequests int64         `json:"successful_requests"`
	FailedRequests     int64         `json:"failed_requests"`
	AverageLatency     time.Duration `json:"average_latency"`
	MedianLatency      time.Duration `json:"median_latency"`
	P99Latency         time.Duration `json:"p99_latency"`
	AgentCallsTotal    int64         `json:"agent_calls_total"`
	AgentCallsFailed   int64         `json:"agent_calls_failed"`
	SynthesisCount     int64         `json:"synthesis_count"`
	SynthesisErrors    int64         `json:"synthesis_errors"`
	LastRequestTime    time.Time     `json:"last_request_time"`
	UptimeSeconds      int64         `json:"uptime_seconds"`
}

// StepCompleteCallback is called after each step in a routing plan completes.
// This enables real-time progress reporting for async workflows that use
// AI orchestration with multiple tool calls.
//
// The callback is invoked from within the executor goroutine after each step
// completes (success or failure). It should be lightweight or delegate to a
// channel for async processing to avoid blocking execution.
//
// Parameters:
//   - stepIndex: 0-based index of the completed step
//   - totalSteps: total number of steps in the plan
//   - step: the step that completed (contains AgentName, StepID, etc.)
//   - result: the step execution result (contains Success, Duration, Response, etc.)
//
// Usage with async tasks:
//
//	config.ExecutionOptions.OnStepComplete = func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
//	    reporter.Report(&core.TaskProgress{
//	        CurrentStep: stepIndex + 1,
//	        TotalSteps:  totalSteps,
//	        StepName:    step.AgentName,
//	        Percentage:  float64(stepIndex+1) / float64(totalSteps) * 100,
//	        Message:     fmt.Sprintf("Completed %s", step.AgentName),
//	    })
//	}
type StepCompleteCallback func(stepIndex, totalSteps int, step RoutingStep, result StepResult)

// stepCallbackKey is the context key for per-request step callbacks
type stepCallbackKey struct{}

// WithStepCallback returns a new context with the step callback attached.
// This allows per-request callbacks without modifying the orchestrator config.
func WithStepCallback(ctx context.Context, callback StepCompleteCallback) context.Context {
	return context.WithValue(ctx, stepCallbackKey{}, callback)
}

// GetStepCallback retrieves the step callback from context, if present.
func GetStepCallback(ctx context.Context) StepCompleteCallback {
	if cb, ok := ctx.Value(stepCallbackKey{}).(StepCompleteCallback); ok {
		return cb
	}
	return nil
}

// ExecutionOptions configures execution behavior
type ExecutionOptions struct {
	// Default: 25 | Env: TRUVAG3_EXECUTION_MAX_CONCURRENCY
	MaxConcurrency int `json:"max_concurrency"`
	// Default: 120s | Env: TRUVAG3_EXECUTION_STEP_TIMEOUT
	StepTimeout time.Duration `json:"step_timeout"`
	// Default: 600s | Env: TRUVAG3_ORCHESTRATION_TIMEOUT
	TotalTimeout     time.Duration `json:"total_timeout"`
	RetryAttempts    int           `json:"retry_attempts"`
	RetryDelay       time.Duration `json:"retry_delay"`
	CircuitBreaker   bool          `json:"circuit_breaker"`
	FailureThreshold int           `json:"failure_threshold"`
	RecoveryTimeout  time.Duration `json:"recovery_timeout"`

	// Layer 3: Validation Feedback configuration
	// When enabled, type errors trigger LLM-based parameter correction
	ValidationFeedbackEnabled bool `json:"validation_feedback_enabled"`
	MaxValidationRetries      int  `json:"max_validation_retries"` // Default: 2

	// Step completion callback for progress reporting (v1 addition)
	// Called after each step completes (success or failure).
	// Used by async task handlers to report per-tool progress.
	// See notes/ASYNC_TASK_DESIGN.md Phase 6 for details.
	OnStepComplete StepCompleteCallback `json:"-"` // Not serializable
}

// ServiceCapabilityConfig holds configuration for the service capability provider
type ServiceCapabilityConfig struct {
	// Required configuration
	Endpoint  string        `json:"endpoint"`
	TopK      int           `json:"top_k"`     // Default: 20
	Threshold float64       `json:"threshold"` // Default: 0.7
	Timeout   time.Duration `json:"timeout"`   // Default: 30s

	// Optional dependencies (not serializable, injected by application)
	CircuitBreaker   core.CircuitBreaker `json:"-"` // Optional: sophisticated resilience
	Logger           core.Logger         `json:"-"` // Optional: observability
	Telemetry        core.Telemetry      `json:"-"` // Optional: metrics
	FallbackProvider CapabilityProvider  `json:"-"` // Optional: graceful degradation
}

// OrchestratorConfig configures the orchestrator
type OrchestratorConfig struct {
	// Name identifies this orchestrator agent for debugging and DAG visualization.
	// Examples: "travel-agent", "support-bot", "order-processor"
	// Default: Falls back to RequestIDPrefix if set, otherwise "orchestrator"
	// Env: TRUVAG3_AGENT_NAME
	Name string `json:"name,omitempty"`

	RoutingMode                      RouterMode        `json:"routing_mode"`
	ExecutionOptions                 ExecutionOptions  `json:"execution_options"`
	SynthesisStrategy                SynthesisStrategy `json:"synthesis_strategy"`
	HistorySize                      int               `json:"history_size"`
	MetricsEnabled                   bool              `json:"metrics_enabled"`
	ConversationTokenBudget          int               `json:"conversation_token_budget,omitempty"`
	ConversationRecentTurnsPreserved int               `json:"conversation_recent_turns_preserved,omitempty"`
	ConversationSummaryCacheSize     int               `json:"conversation_summary_cache_size,omitempty"`
	CacheEnabled                     bool              `json:"cache_enabled"`
	CacheTTL                         time.Duration     `json:"cache_ttl"`

	// CapabilityProvider configuration
	CapabilityProviderType string                  `json:"capability_provider_type"` // "default" or "service"
	CapabilityService      ServiceCapabilityConfig `json:"capability_service"`       // Service provider config
	EnableFallback         bool                    `json:"enable_fallback"`          // Graceful degradation

	// PromptBuilder configuration (extensible prompt customization)
	// Use omitempty to maintain backwards compatibility with existing JSON consumers
	PromptConfig PromptConfig `json:"prompt_config,omitempty"`

	// Telemetry configuration (uses framework telemetry)
	EnableTelemetry bool `json:"enable_telemetry"`

	// Hybrid Parameter Resolution (auto-wiring + micro-resolution)
	// When enabled, uses schema-based auto-wiring for parameter binding between steps,
	// with LLM-based micro-resolution as fallback for complex cases.
	// This provides more reliable parameter binding than template substitution alone.
	EnableHybridResolution bool `json:"enable_hybrid_resolution"`

	// Plan Parse Retry configuration
	// When enabled, retries LLM plan generation if JSON parsing fails.
	// This handles cases where the LLM produces invalid JSON (arithmetic expressions,
	// malformed syntax) that cannot be fixed by the cleanup functions.
	PlanParseRetryEnabled bool `json:"plan_parse_retry_enabled"`
	PlanParseMaxRetries   int  `json:"plan_parse_max_retries"` // Default: 2

	// PlanAIOptions overrides AIOptions for plan generation calls (initial plan,
	// continuation plan, hallucination retry, validation retry, and regeneration).
	// Env population: TRUVAG3_PLAN_MODEL, TRUVAG3_PLAN_MAX_TOKENS.
	PlanAIOptions *AIOptionsOverride `json:"plan_ai_options,omitempty"`

	// SynthesisAIOptions overrides AIOptions for response synthesis calls
	// (streaming and non-streaming).
	// Env population: TRUVAG3_SYNTHESIS_MODEL, TRUVAG3_SYNTHESIS_MAX_TOKENS,
	// TRUVAG3_SYNTHESIS_TEMPERATURE.
	SynthesisAIOptions *AIOptionsOverride `json:"synthesis_ai_options,omitempty"`

	// MicroResolutionAIOptions overrides AIOptions for micro-resolution,
	// semantic retry, and plan refinement calls.
	// Env population: TRUVAG3_MICRO_RESOLUTION_MODEL, TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS.
	MicroResolutionAIOptions *AIOptionsOverride `json:"micro_resolution_ai_options,omitempty"`

	// TieredSelectionAIOptions overrides AIOptions for tiered capability selection calls.
	TieredSelectionAIOptions *AIOptionsOverride `json:"tiered_selection_ai_options,omitempty"`

	// ErrorAnalysisAIOptions overrides AIOptions for error analysis calls.
	ErrorAnalysisAIOptions *AIOptionsOverride `json:"error_analysis_ai_options,omitempty"`

	// ResultDistillAIOptions overrides AIOptions for result distillation calls.
	ResultDistillAIOptions *AIOptionsOverride `json:"result_distill_ai_options,omitempty"`

	// Deprecated legacy scalar fields kept as a compatibility bridge while the
	// repo migrates to per-phase AIOptions overrides.
	PlanMaxTokens            int     `json:"-"`
	PlanModel                string  `json:"-"`
	SynthesisMaxTokens       int     `json:"-"`
	SynthesisTemperature     float64 `json:"-"`
	SynthesisModel           string  `json:"-"`
	MicroResolutionModel     string  `json:"-"`
	MicroResolutionMaxTokens int     `json:"-"`
	legacyAIOptionBridge     bool    `json:"-"`

	// Hallucination Detection configuration
	// When HallucinationValidationEnabled is true, validates that LLM-generated plans
	// only reference agents that were included in the prompt's capability info.
	// This catches cases where the LLM invents agent names not in the allowed list.
	// See orchestration/bugs/BUG_LLM_HALLUCINATED_TOOL.md for detailed analysis.
	//
	// Set HallucinationValidationEnabled to false to disable validation entirely.
	// Default: true | Env: TRUVAG3_HALLUCINATION_VALIDATION_ENABLED
	HallucinationValidationEnabled bool `json:"hallucination_validation_enabled"` // Default: true
	HallucinationRetryEnabled      bool `json:"hallucination_retry_enabled"`      // Default: true
	HallucinationMaxRetries        int  `json:"hallucination_max_retries"`        // Default: 1

	// Layer 4: Semantic Retry Configuration
	// When enabled, uses ContextualReResolver to fix errors that require computation
	SemanticRetry SemanticRetryConfig `json:"semantic_retry,omitempty"`

	// Iterative Planning Configuration (multi-phase DAG planning)
	// When enabled, the LLM planner can signal partial plans (terminal: false)
	// and the orchestrator will execute, feed results back, and continue planning.
	IterativePlanning IterativePlanConfig `json:"iterative_planning"`

	// Tiered Capability Resolution (token optimization)
	// When enabled, uses a two-phase approach to reduce LLM token usage:
	// Phase 1: Send lightweight tool summaries for selection
	// Phase 2: Send full schemas only for selected tools
	// Default: true | Env: TRUVAG3_TIERED_RESOLUTION_ENABLED
	EnableTieredResolution bool                   `json:"enable_tiered_resolution"`
	TieredResolution       TieredCapabilityConfig `json:"tiered_resolution,omitempty"`

	// LLM Debug Payload Storage
	// When enabled, stores complete LLM prompts/responses for debugging.
	// Disabled by default. Enable via TRUVAG3_LLM_DEBUG_ENABLED=true or WithLLMDebug(true).
	LLMDebug LLMDebugConfig `json:"llm_debug,omitempty"`

	// LLMDebugStore is the storage backend for LLM debug payloads.
	// If nil and LLMDebug.Enabled is true, auto-configures Redis from environment.
	// Use WithLLMDebugStore() to inject a custom backend (PostgreSQL, S3, etc.).
	LLMDebugStore LLMDebugStore `json:"-"` // Not serializable

	// HITL (Human-in-the-Loop) configuration
	// When enabled, allows human oversight at critical decision points.
	// Disabled by default for backward compatibility.
	// Enable via TRUVAG3_HITL_ENABLED=true or config.HITL.Enabled=true.
	HITL HITLConfig `json:"hitl,omitempty"`

	// ContinuationResultMaxChars controls the maximum characters per completed step
	// result included in continuation planning prompts. Orchestrator delegation responses
	// can be 20-30KB; the default ensures the child agent's steps[] array (which starts
	// ~4KB into the response) is visible to the continuation planner.
	// Env: TRUVAG3_CONTINUATION_RESULT_MAX_CHARS (default: 10000)
	ContinuationResultMaxChars int `json:"continuation_result_max_chars,omitempty"`

	// ORCH-020 RC9: Upstream-failure-pattern tunables. When RC8 triggers
	// a remediation replan, the pattern analyzer decides whether to embed
	// an "upstream appears persistently unavailable" summary in the
	// continuation note. Three fields govern the summary's emission and
	// rendering. All three affect LLM prompt construction, so per
	// FRAMEWORK_DESIGN_PRINCIPLES §5 (Externalize Hardcoded Limits) they
	// are config fields with env-var overrides.

	// RemediationFailurePatternMinFailures is the minimum distinct failed
	// upstream step count required before the pattern analyzer treats the
	// data as a "pattern". One failure is noise.
	// Default: 2 | Env: TRUVAG3_FAILURE_PATTERN_MIN_FAILURES
	RemediationFailurePatternMinFailures int `json:"remediation_failure_pattern_min_failures,omitempty"`

	// RemediationFailurePatternSignatureLen caps the error-signature length
	// used for CLASSIFICATION (grouping equal-shape errors into one bucket).
	// Intentionally wider than the display cap so classification is less
	// likely to collide two genuinely-different errors that share a common
	// prefix.
	// Default: 120 | Env: TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN
	RemediationFailurePatternSignatureLen int `json:"remediation_failure_pattern_signature_len,omitempty"`

	// RemediationFailurePatternDisplayLen caps how much of the signature
	// appears in the rendered prompt line. Kept shorter for
	// slim-continuation (EFFECTIVE_PROMPTS_GUIDE §4.5). Trailing "…" is
	// appended when truncation occurs.
	// Default: 80 | Env: TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN
	RemediationFailurePatternDisplayLen int `json:"remediation_failure_pattern_display_len,omitempty"`

	// Result Trim Configuration (large result data management)
	// Full results remain in StepResult.Response for template interpolation.
	// See orchestration/notes/LARGE_RESULT_DATA_MANAGEMENT.md
	ResultTrim ResultTrimConfig `json:"result_trim,omitempty"`

	// Result Distillation (opt-in two-stage pipeline: structural pre-filter → LLM distill)
	ResultDistill ResultDistillConfig `json:"result_distill,omitempty"`

	// ExecutionStore configuration for DAG visualization
	// When enabled, stores plan + execution results for debugging.
	// Disabled by default for backward compatibility.
	// Enable via TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true or config.ExecutionStore.Enabled=true.
	ExecutionStore ExecutionStoreConfig `json:"execution_store,omitempty"`

	// ExecutionStoreBackend is the storage backend for execution records.
	// If nil and ExecutionStore.Enabled is true, uses NoOp store (logs warning).
	// Use WithExecutionStore() to inject a StorageProvider-backed implementation.
	ExecutionStoreBackend ExecutionStore `json:"-"` // Not serializable

	// RequestIDPrefix is the prefix used for generated request IDs in distributed tracing.
	// Default: "orch" → generates IDs like "orch-1768510279883440759"
	// Custom: "awhl" → generates IDs like "awhl-1768510279883440759"
	RequestIDPrefix string `json:"request_id_prefix,omitempty"`

	// OAuth Bearer Token for service-to-service authentication.
	// Used for Scenario 2 (machine-to-machine) where the agent obtains a token
	// via client_credentials grant and configures it on the orchestrator.
	//
	// Per-request tokens set via WithOAuthToken(ctx) take priority over this value.
	// When neither is set, no Authorization header is sent (backward compatible).
	//
	// Env: TRUVAG3_OAUTH_TOKEN (default: empty — no auth header sent)
	OAuthToken string `json:"-"` // json:"-" to prevent token leakage in debug/serialization

	// PropagatedHeaders defines custom headers to inject into all outbound HTTP calls
	// from the executor to tool/agent endpoints. These act as instance-level defaults.
	// Per-request headers set via WithPropagatedHeaders(ctx) override these on key conflict.
	//
	// Common use cases:
	//   - X-Correlation-ID for distributed tracing across non-OTel services
	//   - X-Tenant-ID for multi-tenant routing
	//   - X-Request-Source for audit logging
	//
	// No env var — maps don't map to a single env var. Set programmatically:
	//   config.PropagatedHeaders = map[string]string{"X-Tenant-ID": tenantID}
	PropagatedHeaders map[string]string `json:"-"` // json:"-" to prevent header leakage

	// ExcludedCapabilities lists capability names that should be hidden from this
	// orchestrator's LLM planner. Filtered in the catalog's FormatForLLM(),
	// GetCapabilitySummaries(), GetPublicAgentNames(), and FormatToolsForLLM().
	//
	// Primary use case: preventing self-referential orchestration where an agent's
	// own capabilities appear in the catalog and the LLM plans recursive calls.
	//
	// Example: devops-chat-agent excludes "devops_operations" so its planner
	// never sees its own orchestrator capability, but other agents still can.
	//
	// Env: TRUVAG3_EXCLUDED_CAPABILITIES (comma-separated)
	ExcludedCapabilities []string `json:"excluded_capabilities,omitempty"`
}

// SemanticRetryConfig configures Layer 4 contextual re-resolution
type SemanticRetryConfig struct {
	// Enable contextual re-resolution on validation errors (default: true)
	Enabled bool `json:"enabled"`

	// Maximum semantic retry attempts per step (default: 2)
	MaxAttempts int `json:"max_attempts"`

	// HTTP status codes that trigger semantic retry in addition to ErrorAnalyzer
	// Default: [400, 422] - validation errors that might be fixable with different params
	TriggerStatusCodes []int `json:"trigger_status_codes,omitempty"`

	// EnableForIndependentSteps controls whether Layer 4 runs for steps without
	// dependencies (no DependsOn entries). When true, semantic retry activates
	// even when source data from previous steps is empty.
	// Default: true | Env: TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS
	EnableForIndependentSteps bool `json:"enable_for_independent_steps"`
}

// IterativePlanConfig controls multi-phase DAG planning behavior.
// When enabled, the LLM planner can signal that a plan is partial (terminal: false),
// causing the orchestrator to execute the known phase, feed results back to the planner,
// and generate continuation plans until the planner produces a terminal plan.
type IterativePlanConfig struct {
	// Enabled controls whether iterative planning is active.
	// When false, the terminal field is ignored and all plans are treated as terminal.
	// Default: true
	Enabled bool `json:"enabled"`

	// MaxPhases is the maximum number of planning phases allowed per request.
	// If reached without a terminal plan, the orchestrator forces termination
	// and synthesizes with available results.
	// Default: 5
	MaxPhases int `json:"max_phases"`

	// MaxTotalSteps is the maximum total steps across all phases.
	// Prevents runaway plan generation.
	// Default: 200
	MaxTotalSteps int `json:"max_total_steps"`

	// PhaseTimeout is the maximum duration for a single phase (plan generation + execution).
	// Prevents a single continuation phase from hanging indefinitely.
	// This is applied per-phase on top of the existing TotalTimeout which governs the
	// entire request. If a phase exceeds this timeout, it is treated as a failure
	// and the orchestrator synthesizes with whatever results have been collected.
	// Default: 180s
	// Env: TRUVAG3_ITERATIVE_PHASE_TIMEOUT
	PhaseTimeout time.Duration `json:"phase_timeout"`
}

// TieredCapabilityConfig configures tiered capability resolution for token optimization.
// When enabled, uses a two-phase approach: lightweight summaries for tool selection,
// then full schemas only for selected tools. This reduces token usage by 50-75% for
// deployments with 20+ tools.
type TieredCapabilityConfig struct {
	// MinToolsForTiering is the minimum tool count to trigger tiered resolution.
	// Below this threshold, sends all tools directly (simpler, one LLM call).
	// Default: 20 | Env: TRUVAG3_TIERED_MIN_TOOLS
	// Research: "Less is More" (Nov 2024) shows LLM accuracy degradation at ~20 tools
	MinToolsForTiering int `json:"min_tools_for_tiering,omitempty"`

	// SelectionMaxTokens is the maximum output tokens for tiered selection LLM calls.
	// Higher values allow complex multi-tool selections but cost more tokens.
	// Default: 2000 | Env: TRUVAG3_TIERED_SELECTION_MAX_TOKENS
	SelectionMaxTokens int `json:"selection_max_tokens,omitempty"`

	// RetryEnabled enables retry on empty LLM responses and parse failures
	// during tiered tool selection. Mirrors PlanParseRetryEnabled for consistency.
	// Default: true | Env: TRUVAG3_TIERED_SELECTION_RETRY_ENABLED
	RetryEnabled bool `json:"retry_enabled,omitempty"`

	// MaxRetries is the number of additional attempts after the initial failure.
	// 2 means up to 3 total attempts (1 initial + 2 retries).
	// Default: 2 | Env: TRUVAG3_TIERED_SELECTION_RETRY_MAX
	MaxRetries int `json:"max_retries,omitempty"`
}

// DefaultConfig returns default orchestrator configuration with intelligent defaults
func DefaultConfig() *OrchestratorConfig {
	planMaxTokens := 15000
	synthesisTemp := float32(0.5)
	synthesisMaxTokens := 5000
	microMaxTokens := 2000

	config := &OrchestratorConfig{
		RoutingMode:                      ModeAutonomous, // Default to AI-driven orchestration
		SynthesisStrategy:                StrategyLLM,
		HistorySize:                      100,
		MetricsEnabled:                   true,
		ConversationTokenBudget:          48000,
		ConversationRecentTurnsPreserved: 4,
		ConversationSummaryCacheSize:     256,
		CacheEnabled:                     true,
		CacheTTL:                         5 * time.Minute,
		ExecutionOptions: ExecutionOptions{
			MaxConcurrency:   25,
			StepTimeout:      120 * time.Second,
			TotalTimeout:     600 * time.Second,
			RetryAttempts:    3, // initial + 2 retries; aligns with executor's maxAttempts default (overridable via TRUVAG3_STEP_RETRY_MAX_ATTEMPTS)
			RetryDelay:       2 * time.Second,
			CircuitBreaker:   true,
			FailureThreshold: 5,
			RecoveryTimeout:  30 * time.Second,
			// Layer 3: Validation Feedback defaults
			ValidationFeedbackEnabled: true, // Enable by default for production reliability
			MaxValidationRetries:      2,    // Up to 2 correction attempts
		},
		// CapabilityProvider defaults
		CapabilityProviderType: "default", // Quick start default
		EnableTelemetry:        true,      // Production-first
		EnableFallback:         true,      // Graceful degradation

		// Hybrid Parameter Resolution defaults
		EnableHybridResolution: true, // Enable by default for reliable parameter binding

		// Plan Parse Retry defaults
		PlanParseRetryEnabled: true, // Enable by default for production reliability
		PlanParseMaxRetries:   2,    // Up to 2 retry attempts after initial failure

		PlanMaxTokens:            planMaxTokens,
		SynthesisMaxTokens:       synthesisMaxTokens,
		SynthesisTemperature:     roundLegacyFloat(float64(synthesisTemp)),
		MicroResolutionMaxTokens: microMaxTokens,
		legacyAIOptionBridge:     true,

		// Hallucination Detection defaults
		// Validates that LLM plans only use agents from the allowed list.
		// See orchestration/bugs/BUG_LLM_HALLUCINATED_TOOL.md for detailed analysis.
		HallucinationValidationEnabled: true, // Enable validation by default
		HallucinationRetryEnabled:      true, // Enable retry for production reliability
		HallucinationMaxRetries:        1,    // Up to 1 retry attempt (usually enough for self-correction)

		// Tiered Capability Resolution defaults (enabled by default for token optimization)
		// Research: "Less is More" (Nov 2024) shows LLM accuracy degradation at ~20 tools
		EnableTieredResolution: true,
		TieredResolution: TieredCapabilityConfig{
			MinToolsForTiering: 20,   // Research-backed default
			SelectionMaxTokens: 2000, // Allow room for complex multi-tool selections
			RetryEnabled:       true, // Retry on empty response / parse failure
			MaxRetries:         2,    // Up to 2 retry attempts after initial failure
		},
	}

	// Execution Options configuration from environment
	if maxConc := os.Getenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY"); maxConc != "" {
		if val, err := strconv.Atoi(maxConc); err == nil && val > 0 {
			config.ExecutionOptions.MaxConcurrency = val
		}
	}
	if stepTimeout := os.Getenv("TRUVAG3_EXECUTION_STEP_TIMEOUT"); stepTimeout != "" {
		if d, err := time.ParseDuration(stepTimeout); err == nil && d > 0 {
			config.ExecutionOptions.StepTimeout = d
		}
	}
	if totalTimeout := os.Getenv("TRUVAG3_ORCHESTRATION_TIMEOUT"); totalTimeout != "" {
		if d, err := time.ParseDuration(totalTimeout); err == nil && d > 0 {
			config.ExecutionOptions.TotalTimeout = d
		}
	}
	// TRUVAG3_STEP_RETRY_MAX_ATTEMPTS overrides the executor's step retry limit.
	// Read here (not just in NewSmartExecutor) because DefaultConfig drives the
	// orchestrator's SetMaxAttempts call which otherwise clobbers the env var.
	if maxAttempts := os.Getenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS"); maxAttempts != "" {
		if n, err := strconv.Atoi(maxAttempts); err == nil && n >= 1 {
			config.ExecutionOptions.RetryAttempts = n
		}
	}
	if budget := os.Getenv("TRUVAG3_CONVERSATION_TOKEN_BUDGET"); budget != "" {
		if val, err := strconv.Atoi(budget); err == nil && val > 0 {
			config.ConversationTokenBudget = val
		}
	}
	if preserved := os.Getenv("TRUVAG3_CONVERSATION_RECENT_TURNS_PRESERVED"); preserved != "" {
		if val, err := strconv.Atoi(preserved); err == nil && val > 0 {
			config.ConversationRecentTurnsPreserved = val
		}
	}
	if cacheSize := os.Getenv("TRUVAG3_CONVERSATION_SUMMARY_CACHE_SIZE"); cacheSize != "" {
		if val, err := strconv.Atoi(cacheSize); err == nil && val > 0 {
			config.ConversationSummaryCacheSize = val
		}
	}

	// Auto-configure based on environment (intelligent configuration)
	if serviceURL := os.Getenv("TRUVAG3_CAPABILITY_SERVICE_URL"); serviceURL != "" {
		// User intent is clear - auto-configure for service provider
		config.CapabilityProviderType = "service"
		config.CapabilityService.Endpoint = serviceURL
	}

	// Plan Parse Retry configuration from environment
	if retryEnabled := os.Getenv("TRUVAG3_PLAN_RETRY_ENABLED"); retryEnabled != "" {
		config.PlanParseRetryEnabled = strings.ToLower(retryEnabled) == "true"
	}
	if maxRetries := os.Getenv("TRUVAG3_PLAN_RETRY_MAX"); maxRetries != "" {
		if val, err := strconv.Atoi(maxRetries); err == nil && val >= 0 {
			config.PlanParseMaxRetries = val
		}
	}

	// LLM Max Tokens configuration from environment
	if maxTokens := os.Getenv("TRUVAG3_PLAN_MAX_TOKENS"); maxTokens != "" {
		if val, err := strconv.Atoi(maxTokens); err == nil && val > 0 {
			if config.PlanAIOptions == nil {
				config.PlanAIOptions = &AIOptionsOverride{}
			}
			config.PlanAIOptions.MaxTokens = IntPtr(val)
			config.PlanMaxTokens = val
		}
	}
	if maxTokens := os.Getenv("TRUVAG3_SYNTHESIS_MAX_TOKENS"); maxTokens != "" {
		if val, err := strconv.Atoi(maxTokens); err == nil && val > 0 {
			if config.SynthesisAIOptions == nil {
				config.SynthesisAIOptions = &AIOptionsOverride{}
			}
			config.SynthesisAIOptions.MaxTokens = IntPtr(val)
			config.SynthesisMaxTokens = val
		}
	}
	if temp := os.Getenv("TRUVAG3_SYNTHESIS_TEMPERATURE"); temp != "" {
		if val, err := strconv.ParseFloat(temp, 32); err == nil && val >= 0 && val <= 2.0 {
			if config.SynthesisAIOptions == nil {
				config.SynthesisAIOptions = &AIOptionsOverride{}
			}
			config.SynthesisAIOptions.Temperature = Float32Ptr(float32(val))
			config.SynthesisTemperature = roundLegacyFloat(val)
		}
	}

	// LLM Model Override configuration from environment
	// Use portable aliases ("fast", "default", "smart") with ChainClient.
	if model := os.Getenv("TRUVAG3_PLAN_MODEL"); model != "" {
		if config.PlanAIOptions == nil {
			config.PlanAIOptions = &AIOptionsOverride{}
		}
		config.PlanAIOptions.Model = StringPtr(model)
		config.PlanModel = model
	}
	if model := os.Getenv("TRUVAG3_SYNTHESIS_MODEL"); model != "" {
		if config.SynthesisAIOptions == nil {
			config.SynthesisAIOptions = &AIOptionsOverride{}
		}
		config.SynthesisAIOptions.Model = StringPtr(model)
		config.SynthesisModel = model
	}
	if model := os.Getenv("TRUVAG3_MICRO_RESOLUTION_MODEL"); model != "" {
		if config.MicroResolutionAIOptions == nil {
			config.MicroResolutionAIOptions = &AIOptionsOverride{}
		}
		config.MicroResolutionAIOptions.Model = StringPtr(model)
		config.MicroResolutionModel = model
	}
	if maxTokens := os.Getenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS"); maxTokens != "" {
		if val, err := strconv.Atoi(maxTokens); err == nil && val > 0 {
			if config.MicroResolutionAIOptions == nil {
				config.MicroResolutionAIOptions = &AIOptionsOverride{}
			}
			config.MicroResolutionAIOptions.MaxTokens = IntPtr(val)
			config.MicroResolutionMaxTokens = val
		}
	}

	// Hallucination Detection configuration from environment
	// TRUVAG3_HALLUCINATION_VALIDATION_ENABLED=false completely disables validation
	if hallValidation := os.Getenv("TRUVAG3_HALLUCINATION_VALIDATION_ENABLED"); hallValidation != "" {
		config.HallucinationValidationEnabled = strings.ToLower(hallValidation) == "true"
	}
	if hallRetryEnabled := os.Getenv("TRUVAG3_HALLUCINATION_RETRY_ENABLED"); hallRetryEnabled != "" {
		config.HallucinationRetryEnabled = strings.ToLower(hallRetryEnabled) == "true"
	}
	if hallMaxRetries := os.Getenv("TRUVAG3_HALLUCINATION_MAX_RETRIES"); hallMaxRetries != "" {
		if val, err := strconv.Atoi(hallMaxRetries); err == nil && val >= 0 {
			config.HallucinationMaxRetries = val
		}
	}

	// Iterative Planning defaults (enabled by default for multi-phase DAG support)
	config.IterativePlanning = IterativePlanConfig{
		Enabled:       true,
		MaxPhases:     5,
		MaxTotalSteps: 200,
		PhaseTimeout:  180 * time.Second,
	}

	// Layer 4: Semantic Retry defaults
	config.SemanticRetry = SemanticRetryConfig{
		Enabled:                   true,
		MaxAttempts:               2,
		TriggerStatusCodes:        []int{400, 422},
		EnableForIndependentSteps: true, // Default: enabled for steps without dependencies
	}

	// Semantic Retry configuration from environment
	if enabled := os.Getenv("TRUVAG3_SEMANTIC_RETRY_ENABLED"); enabled != "" {
		config.SemanticRetry.Enabled = strings.ToLower(enabled) == "true"
	}
	if maxAttempts := os.Getenv("TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS"); maxAttempts != "" {
		if val, err := strconv.Atoi(maxAttempts); err == nil && val >= 0 {
			config.SemanticRetry.MaxAttempts = val
		}
	}
	if independentSteps := os.Getenv("TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS"); independentSteps != "" {
		config.SemanticRetry.EnableForIndependentSteps = strings.ToLower(independentSteps) == "true"
	}

	// Tiered Capability Resolution configuration from environment
	if enabled := os.Getenv("TRUVAG3_TIERED_RESOLUTION_ENABLED"); enabled != "" {
		config.EnableTieredResolution = strings.ToLower(enabled) == "true"
	}
	if minTools := os.Getenv("TRUVAG3_TIERED_MIN_TOOLS"); minTools != "" {
		if val, err := strconv.Atoi(minTools); err == nil && val > 0 {
			config.TieredResolution.MinToolsForTiering = val
		}
	}
	if maxTokens := os.Getenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS"); maxTokens != "" {
		if val, err := strconv.Atoi(maxTokens); err == nil && val > 0 {
			config.TieredResolution.SelectionMaxTokens = val
		}
	}
	// Tiered selection retry configuration from environment
	if retryEnabled := os.Getenv("TRUVAG3_TIERED_SELECTION_RETRY_ENABLED"); retryEnabled != "" {
		config.TieredResolution.RetryEnabled = strings.ToLower(retryEnabled) == "true"
	}
	if maxRetries := os.Getenv("TRUVAG3_TIERED_SELECTION_RETRY_MAX"); maxRetries != "" {
		if val, err := strconv.Atoi(maxRetries); err == nil && val >= 0 {
			config.TieredResolution.MaxRetries = val
		}
	}

	// Iterative Planning configuration from environment
	if v := os.Getenv("TRUVAG3_ITERATIVE_PLANNING_ENABLED"); v != "" {
		config.IterativePlanning.Enabled = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("TRUVAG3_ITERATIVE_MAX_PHASES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			config.IterativePlanning.MaxPhases = n
		}
	}
	if v := os.Getenv("TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			config.IterativePlanning.MaxTotalSteps = n
		}
	}
	if v := os.Getenv("TRUVAG3_ITERATIVE_PHASE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			config.IterativePlanning.PhaseTimeout = d
		}
	}

	// LLM Debug Payload Storage defaults (disabled by default)
	config.LLMDebug = DefaultLLMDebugConfig()

	// LLM Debug configuration from environment
	if enabled := os.Getenv("TRUVAG3_LLM_DEBUG_ENABLED"); enabled != "" {
		config.LLMDebug.Enabled = strings.ToLower(enabled) == "true"
	}
	if ttl := os.Getenv("TRUVAG3_LLM_DEBUG_TTL"); ttl != "" {
		if duration, err := time.ParseDuration(ttl); err == nil {
			config.LLMDebug.TTL = duration
		}
	}
	if errorTTL := os.Getenv("TRUVAG3_LLM_DEBUG_ERROR_TTL"); errorTTL != "" {
		if duration, err := time.ParseDuration(errorTTL); err == nil {
			config.LLMDebug.ErrorTTL = duration
		}
	}
	if redisDB := os.Getenv("TRUVAG3_LLM_DEBUG_REDIS_DB"); redisDB != "" {
		if val, err := strconv.Atoi(redisDB); err == nil && val >= 0 {
			config.LLMDebug.RedisDB = val
		}
	}

	// HITL (Human-in-the-Loop) defaults (disabled by default for backward compatibility)
	config.HITL = DefaultHITLConfig()

	// HITL configuration from environment
	if enabled := os.Getenv("TRUVAG3_HITL_ENABLED"); enabled != "" {
		config.HITL.Enabled = strings.ToLower(enabled) == "true"
	}
	if planApproval := os.Getenv("TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL"); planApproval != "" {
		config.HITL.RequirePlanApproval = strings.ToLower(planApproval) == "true"
	}
	if capabilities := os.Getenv("TRUVAG3_HITL_SENSITIVE_CAPABILITIES"); capabilities != "" {
		config.HITL.SensitiveCapabilities = strings.Split(capabilities, ",")
	}
	if agents := os.Getenv("TRUVAG3_HITL_SENSITIVE_AGENTS"); agents != "" {
		config.HITL.SensitiveAgents = strings.Split(agents, ",")
	}
	// Step-sensitive capabilities/agents (Scenario 2 only - no plan approval)
	if stepCapabilities := os.Getenv("TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES"); stepCapabilities != "" {
		config.HITL.StepSensitiveCapabilities = strings.Split(stepCapabilities, ",")
	}
	if stepAgents := os.Getenv("TRUVAG3_HITL_STEP_SENSITIVE_AGENTS"); stepAgents != "" {
		config.HITL.StepSensitiveAgents = strings.Split(stepAgents, ",")
	}
	if timeout := os.Getenv("TRUVAG3_HITL_DEFAULT_TIMEOUT"); timeout != "" {
		if duration, err := time.ParseDuration(timeout); err == nil {
			config.HITL.DefaultTimeout = duration
		}
	}
	if escalateRetries := os.Getenv("TRUVAG3_HITL_ESCALATE_AFTER_RETRIES"); escalateRetries != "" {
		if val, err := strconv.Atoi(escalateRetries); err == nil && val >= 0 {
			config.HITL.EscalateAfterRetries = val
		}
	}
	// Override default action for all checkpoint types on expiry
	// Values: "approve", "reject", "abort"
	// Default is "reject" (HITL enabled = require explicit approval)
	if defaultAction := os.Getenv("TRUVAG3_HITL_DEFAULT_ACTION"); defaultAction != "" {
		switch strings.ToLower(defaultAction) {
		case "approve":
			config.HITL.DefaultAction = CommandApprove
		case "reject":
			config.HITL.DefaultAction = CommandReject
		case "abort":
			config.HITL.DefaultAction = CommandAbort
		default:
			// #nosec G706 -- env values are quoted and logged for operator diagnostics only.
			log.Printf("[WARN] Invalid TRUVAG3_HITL_DEFAULT_ACTION value: %q (valid: approve, reject, abort). Using default: reject", defaultAction)
		}
	}

	// Excluded Capabilities from environment (prevents self-referential orchestration)
	// Follows core.parseStringList() pattern: trim whitespace, filter empty strings.
	if excluded := os.Getenv("TRUVAG3_EXCLUDED_CAPABILITIES"); excluded != "" {
		parts := strings.Split(excluded, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				config.ExcludedCapabilities = append(config.ExcludedCapabilities, trimmed)
			}
		}
	}

	// Result Trim defaults (enabled by default for production safety)
	config.ResultTrim = ResultTrimConfig{
		Enabled:                      true,
		MaxResultBytes:               16384, // 16 KB per result (~4K tokens)
		MaxTotalPromptBytes:          32768, // 32 KB total (~8K tokens)
		MaxMicroResolutionBytes:      65536, // 64 KB (~26K tokens) for micro-resolution & semantic retry source data
		MaxAgentInputBytes:           65536, // 64 KB (~26K tokens) per parameter value for agent/tool HTTP calls
		SchemaGuidedMappingThreshold: 16384, // 16 KB — below this, value extraction is fine
	}
	if enabled := os.Getenv("TRUVAG3_RESULT_TRIM_ENABLED"); enabled != "" {
		config.ResultTrim.Enabled = strings.ToLower(enabled) == "true"
	}
	if maxBytes := os.Getenv("TRUVAG3_RESULT_TRIM_MAX_BYTES"); maxBytes != "" {
		if val, err := strconv.Atoi(maxBytes); err == nil && val > 0 {
			config.ResultTrim.MaxResultBytes = val
		}
	}
	if maxTotal := os.Getenv("TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES"); maxTotal != "" {
		if val, err := strconv.Atoi(maxTotal); err == nil && val > 0 {
			config.ResultTrim.MaxTotalPromptBytes = val
		}
	}
	if maxMicro := os.Getenv("TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES"); maxMicro != "" {
		if val, err := strconv.Atoi(maxMicro); err == nil && val > 0 {
			config.ResultTrim.MaxMicroResolutionBytes = val
		}
	}
	if maxAgentInput := os.Getenv("TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES"); maxAgentInput != "" {
		if val, err := strconv.Atoi(maxAgentInput); err == nil && val > 0 {
			config.ResultTrim.MaxAgentInputBytes = val
		}
	}
	// TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD — Override schema-guided mapping threshold.
	// Note: uses val >= 0 (not val > 0 like other TRUVAG3_RESULT_TRIM_* vars) because 0 is a
	// valid value meaning "disable schema-guided mapping entirely".
	if envVal := os.Getenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD"); envVal != "" {
		if val, err := strconv.Atoi(envVal); err == nil && val >= 0 {
			config.ResultTrim.SchemaGuidedMappingThreshold = val
		}
	}

	// Continuation prompt result truncation (ORCH-015: cross-phase dedup visibility)
	config.ContinuationResultMaxChars = 10000
	if maxChars := os.Getenv("TRUVAG3_CONTINUATION_RESULT_MAX_CHARS"); maxChars != "" {
		if val, err := strconv.Atoi(maxChars); err == nil && val > 0 {
			config.ContinuationResultMaxChars = val
		}
	}

	// ORCH-020 RC9: failure-pattern analyzer tunables (see field docs).
	config.RemediationFailurePatternMinFailures = 2
	config.RemediationFailurePatternSignatureLen = 120
	config.RemediationFailurePatternDisplayLen = 80
	if v := os.Getenv("TRUVAG3_FAILURE_PATTERN_MIN_FAILURES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			config.RemediationFailurePatternMinFailures = n
		}
	}
	if v := os.Getenv("TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			config.RemediationFailurePatternSignatureLen = n
		}
	}
	if v := os.Getenv("TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			config.RemediationFailurePatternDisplayLen = n
		}
	}

	// Result Distillation defaults (disabled by default — opt-in)
	config.ResultDistill = ResultDistillConfig{
		Enabled:          false,
		DistillThreshold: 32768,
		PreFilterBudget:  32768,
		TargetSize:       4096,
	}
	if enabled := os.Getenv("TRUVAG3_RESULT_DISTILL_ENABLED"); enabled != "" {
		config.ResultDistill.Enabled = strings.ToLower(enabled) == "true"
	}
	if v := os.Getenv("TRUVAG3_RESULT_DISTILL_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			config.ResultDistill.DistillThreshold = n
		}
	}
	if v := os.Getenv("TRUVAG3_RESULT_DISTILL_PREFILTER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			config.ResultDistill.PreFilterBudget = n
		}
	}
	if v := os.Getenv("TRUVAG3_RESULT_DISTILL_TARGET"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			config.ResultDistill.TargetSize = n
		}
	}
	if model := os.Getenv("TRUVAG3_RESULT_DISTILL_MODEL"); model != "" {
		config.ResultDistill.Model = model
	}

	// Execution Debug Store configuration from environment
	// Note: Storage-specific settings (Redis URL, DB, etc.) are NOT here.
	// The application configures those when creating the StorageProvider.
	config.ExecutionStore = DefaultExecutionStoreConfig()
	if enabled := os.Getenv("TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED"); enabled != "" {
		config.ExecutionStore.Enabled = strings.ToLower(enabled) == "true"
	}
	if ttl := os.Getenv("TRUVAG3_EXECUTION_DEBUG_TTL"); ttl != "" {
		if duration, err := time.ParseDuration(ttl); err == nil {
			config.ExecutionStore.TTL = duration
		}
	}
	if errorTTL := os.Getenv("TRUVAG3_EXECUTION_DEBUG_ERROR_TTL"); errorTTL != "" {
		if duration, err := time.ParseDuration(errorTTL); err == nil {
			config.ExecutionStore.ErrorTTL = duration
		}
	}
	if keyPrefix := os.Getenv("TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX"); keyPrefix != "" {
		config.ExecutionStore.KeyPrefix = keyPrefix
	}

	// Agent name from environment (for DAG visualization and HITL isolation)
	// Falls back to RequestIDPrefix if Name is not set, then "orchestrator"
	if name := os.Getenv("TRUVAG3_AGENT_NAME"); name != "" {
		config.Name = name
	}

	// OAuth Bearer token for service-to-service authentication
	// When set, all outbound executor HTTP calls include Authorization: Bearer header
	if token := os.Getenv("TRUVAG3_OAUTH_TOKEN"); token != "" {
		config.OAuthToken = token
	}

	return config
}

// ExecutionError represents an error during execution
type ExecutionError struct {
	StepID  string `json:"step_id"`
	Agent   string `json:"agent"`
	Message string `json:"message"`
	Code    string `json:"code"`
	Retries int    `json:"retries"`
}

func (e *ExecutionError) Error() string {
	return e.Code + " at " + e.Agent + ": " + e.Message
}

// Common error codes
const (
	ErrAgentTimeout      = "AGENT_TIMEOUT"
	ErrAgentUnavailable  = "AGENT_UNAVAILABLE"
	ErrAgentError        = "AGENT_ERROR"
	ErrSynthesisFailure  = "SYNTHESIS_FAILURE"
	ErrRoutingFailure    = "ROUTING_FAILURE"
	ErrCircuitOpen       = "CIRCUIT_BREAKER_OPEN"
	ErrMaxRetriesReached = "MAX_RETRIES_REACHED"
)
