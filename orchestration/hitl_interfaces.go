package orchestration

import (
	"context"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// =============================================================================
// HITL (Human-in-the-Loop) Interfaces and Types
// =============================================================================
//
// This file defines the core interfaces and data types for the HITL system.
// The design follows Go best practices:
// - Small, focused interfaces (1-3 methods each)
// - Interface composition for larger behaviors
// - Constructors return concrete types, not interfaces
//
// See HUMAN_IN_THE_LOOP_PROPOSAL.md for full design documentation.
// =============================================================================

// -----------------------------------------------------------------------------
// Small, Focused Interfaces (Go Idiom: "Keep interfaces small!")
// -----------------------------------------------------------------------------

// PlanApprover determines if LLM-generated plans need human approval.
// Implement this interface for plan-level approval logic.
type PlanApprover interface {
	ShouldApprovePlan(ctx context.Context, plan *RoutingPlan) (*InterruptDecision, error)
}

// StepApprover determines if steps need approval before or after execution.
// Implement this interface for step-level approval logic.
type StepApprover interface {
	ShouldApproveBeforeStep(ctx context.Context, step RoutingStep, plan *RoutingPlan) (*InterruptDecision, error)
	ShouldApproveAfterStep(ctx context.Context, step RoutingStep, result *StepResult) (*InterruptDecision, error)
}

// ErrorEscalator determines if errors should escalate to humans.
// Implement this interface for error escalation logic.
type ErrorEscalator interface {
	ShouldEscalateError(ctx context.Context, step RoutingStep, err error, attempts int) (*InterruptDecision, error)
}

// -----------------------------------------------------------------------------
// Composed Interface
// -----------------------------------------------------------------------------

// InterruptPolicy composes all approval behaviors for convenience.
// Most implementations will embed the smaller interfaces or implement this directly.
// Implementations can be rule-based, ML-based, or external service calls.
type InterruptPolicy interface {
	PlanApprover
	StepApprover
	ErrorEscalator
}

// -----------------------------------------------------------------------------
// Checkpoint Storage Interface
// -----------------------------------------------------------------------------

// CheckpointPersistence is the provider-neutral persistence subset used by
// orchestration request processing. Background expiry lifecycle is composed
// separately through ExpiredCheckpointSource and core.Runnable.
//
// Existing CheckpointStore implementations satisfy this interface without
// modification.
type CheckpointPersistence interface {
	SaveCheckpoint(ctx context.Context, checkpoint *ExecutionCheckpoint) error
	LoadCheckpoint(ctx context.Context, checkpointID string) (*ExecutionCheckpoint, error)
	UpdateCheckpointStatus(ctx context.Context, checkpointID string, status CheckpointStatus) error
	ListPendingCheckpoints(ctx context.Context, filter CheckpointFilter) ([]*ExecutionCheckpoint, error)
	DeleteCheckpoint(ctx context.Context, checkpointID string) error
}

// ExpiredCheckpointClaimRequest bounds one atomic expiry claim operation.
type ExpiredCheckpointClaimRequest struct {
	Before time.Time
	Limit  int
	Owner  string
	Lease  time.Duration
}

// ExpiredCheckpointSource atomically discovers and leases expired pending
// checkpoints. A claim is exclusive until released by its owner or its lease
// expires.
type ExpiredCheckpointSource interface {
	ClaimExpiredCheckpoints(ctx context.Context, request ExpiredCheckpointClaimRequest) ([]*ExecutionCheckpoint, error)
	ReleaseExpiredCheckpointClaim(ctx context.Context, checkpointID, owner string) error
}

// CheckpointStore is the legacy combined persistence and lifecycle contract.
//
// Deprecated: request-path consumers should accept CheckpointPersistence.
// Background expiry should compose ExpiredCheckpointSource with
// CheckpointExpiryProcessor. This interface remains for source compatibility.
type CheckpointStore interface {
	CheckpointPersistence

	// Expiry processor methods (agent calls these during setup)

	// StartExpiryProcessor starts the background goroutine that processes expired checkpoints.
	// The agent calls this method during setup - the framework provides the implementation.
	// Deprecated: run CheckpointExpiryProcessor through application lifecycle ownership.
	StartExpiryProcessor(ctx context.Context, config ExpiryProcessorConfig) error

	// StopExpiryProcessor stops the expiry processor gracefully.
	// Deprecated: cancel the application-owned CheckpointExpiryProcessor context.
	StopExpiryProcessor(ctx context.Context) error

	// SetExpiryCallback sets the callback for expired checkpoints.
	// Must be called before StartExpiryProcessor.
	// Deprecated: pass the callback to NewCheckpointExpiryProcessor.
	SetExpiryCallback(callback ExpiryCallback) error
}

// DeliverySemantics controls callback invocation timing relative to status update.
// This determines retry behavior when callbacks fail.
type DeliverySemantics string

const (
	// DeliveryAtMostOnce updates status BEFORE invoking callback.
	// If callback fails, checkpoint is already marked processed - no retry.
	// Use for: Notifications, idempotent operations, fire-and-forget scenarios.
	// This is the DEFAULT and safest option for most use cases.
	DeliveryAtMostOnce DeliverySemantics = "at_most_once"

	// DeliveryAtLeastOnce invokes callback BEFORE updating status.
	// If callback fails, status remains "pending" and will be retried on next scan.
	// Use for: Critical operations that must complete.
	// WARNING: Callback MUST be idempotent - it may be called multiple times!
	DeliveryAtLeastOnce DeliverySemantics = "at_least_once"
)

// ExpiryProcessorConfig configures the background expiry processor.
// The agent passes this when calling StartExpiryProcessor().
type ExpiryProcessorConfig struct {
	Enabled      bool          // Whether to run the processor (default: true)
	ScanInterval time.Duration // How often to scan (default: 10s)
	BatchSize    int           // Max checkpoints per scan (default: 100)

	// DeliverySemantics controls callback timing relative to status update.
	// Default: DeliveryAtMostOnce (status updated before callback).
	// Use DeliveryAtLeastOnce only with idempotent callbacks.
	DeliverySemantics DeliverySemantics
}

// ExpiryCallback is called when a checkpoint expires.
// The agent provides this callback to define what happens on expiry.
// This is where the agent decides whether to auto-resume, notify users, etc.
//
// Parameters:
//   - ctx: Context for the callback (may have timeout)
//   - checkpoint: The expired checkpoint with updated status
//   - appliedAction: The action that was applied (empty string for implicit deny)
type ExpiryCallback func(ctx context.Context, checkpoint *ExecutionCheckpoint, appliedAction CommandType)

// CheckpointFilter for querying checkpoints
type CheckpointFilter struct {
	Status        CheckpointStatus `json:"status,omitempty"`
	RequestID     string           `json:"request_id,omitempty"`
	ExpiredBefore *time.Time       `json:"expired_before,omitempty"` // For expiry processor queries
	Limit         int              `json:"limit,omitempty"`
	Offset        int              `json:"offset,omitempty"`
}

// -----------------------------------------------------------------------------
// Interrupt Handler Interface
// -----------------------------------------------------------------------------

// InterruptHandler manages asynchronous human notification and response collection.
// This is the primary extension point for integrating with your notification infrastructure.
//
// The framework provides WebhookInterruptHandler as a reference implementation.
// Applications implement custom handlers for their specific infrastructure:
// - Slack/Discord bots
// - Email systems
// - Dashboard/UI integration
// - SMS gateways
// - Custom webhook processors
type InterruptHandler interface {
	// NotifyInterrupt sends notification about a pending interrupt.
	// The implementation decides HOW to notify (webhook, Slack, email, etc.)
	NotifyInterrupt(ctx context.Context, checkpoint *ExecutionCheckpoint) error

	// WaitForCommand blocks until human responds or timeout.
	// For async scenarios, use SubmitCommand instead.
	WaitForCommand(ctx context.Context, checkpointID string, timeout time.Duration) (*Command, error)

	// SubmitCommand processes a command submitted via external channel.
	// Called when human responds through your notification system.
	SubmitCommand(ctx context.Context, command *Command) error
}

// -----------------------------------------------------------------------------
// Command Store Interface
// -----------------------------------------------------------------------------

// CommandStore provides distributed command delivery for HITL.
// This enables cross-instance command submission in distributed deployments.
//
// The framework provides RedisCommandStore as the default implementation.
// Applications can implement custom stores (PostgreSQL, Kafka, etc.)
//
// Key format for Redis implementation:
//   - Command channel: {prefix}:command:{checkpoint_id}
//   - Pending commands: {prefix}:pending_commands (Redis List)
type CommandStore interface {
	// PublishCommand publishes a command for a waiting handler to receive.
	// Used by SubmitCommand to deliver commands across instances.
	PublishCommand(ctx context.Context, command *Command) error

	// SubscribeCommand subscribes to commands for a specific checkpoint.
	// Returns a channel that receives commands. Close the returned cancel func when done.
	SubscribeCommand(ctx context.Context, checkpointID string) (<-chan *Command, func(), error)

	// Close closes the command store and releases resources.
	Close() error
}

// -----------------------------------------------------------------------------
// Interrupt Controller Interface
// -----------------------------------------------------------------------------

// InterruptController coordinates all HITL functionality.
// This is the main entry point for HITL integration with the orchestrator.
//
// The controller delegates policy decisions to InterruptPolicy and notification
// to InterruptHandler. It owns the coordination logic between these components.
type InterruptController interface {
	// SetPolicy configures the interrupt policy (optional - can be set via constructor)
	SetPolicy(policy InterruptPolicy)

	// SetHandler configures the notification handler (optional - can be set via constructor)
	SetHandler(handler InterruptHandler)

	// SetCheckpointStore configures state persistence (optional - can be set via constructor).
	// Deprecated: concrete DefaultInterruptController users should call
	// SetCheckpointPersistence. This method remains for interface compatibility.
	SetCheckpointStore(store CheckpointStore)

	// Check methods evaluate policy and handle interrupt if needed.
	// Returns nil if no interrupt, otherwise saves checkpoint and returns it.
	// These are called by the orchestrator/executor at the appropriate points.
	CheckPlanApproval(ctx context.Context, plan *RoutingPlan) (*ExecutionCheckpoint, error)
	CheckBeforeStep(ctx context.Context, step RoutingStep, plan *RoutingPlan) (*ExecutionCheckpoint, error)
	CheckAfterStep(ctx context.Context, step RoutingStep, result *StepResult) (*ExecutionCheckpoint, error)
	CheckOnError(ctx context.Context, step RoutingStep, err error, attempts int) (*ExecutionCheckpoint, error)

	// ProcessCommand handles a human command and updates checkpoint status.
	// Called when human responds through the InterruptHandler.
	ProcessCommand(ctx context.Context, command *Command) (*ResumeResult, error)

	// ResumeExecution continues workflow execution from a checkpoint.
	// Called after ProcessCommand returns ShouldResume=true.
	ResumeExecution(ctx context.Context, checkpointID string) (*ExecutionResult, error)

	// UpdateCheckpointProgress updates a checkpoint with completed steps.
	// Called by executor before returning ErrInterrupted for step-level interrupts.
	// This allows resumption to skip already-completed steps.
	UpdateCheckpointProgress(ctx context.Context, checkpointID string, completedSteps []StepResult) error
}

// CheckpointEnricher is an optional interface for interrupt controllers that
// support saving enriched checkpoints (with accumulated multi-phase step results)
// back to the checkpoint store. The orchestrator uses a type assertion to check
// if the controller supports this — controllers that don't implement it simply
// skip the enrichment save, falling back to pre-existing behavior.
//
// This follows the Go idiom of small, focused interfaces (see io.WriterTo,
// encoding.TextMarshaler) rather than bloating the main InterruptController.
type CheckpointEnricher interface {
	// SaveEnrichedCheckpoint persists an enriched checkpoint back to the store.
	// Called by the orchestrator after phase enrichment to ensure BuildResumeContext
	// loads complete step results on HITL resume.
	SaveEnrichedCheckpoint(ctx context.Context, checkpoint *ExecutionCheckpoint) error
}

// =============================================================================
// Data Types
// =============================================================================

// -----------------------------------------------------------------------------
// Interrupt Decision
// -----------------------------------------------------------------------------

// InterruptDecision contains the decision and context for an interrupt.
// All expiry behavior settings are copied from the policy config so that
// the expiry processor knows how to handle this specific checkpoint.
type InterruptDecision struct {
	ShouldInterrupt bool                   `json:"should_interrupt"`
	Reason          InterruptReason        `json:"reason"`
	Message         string                 `json:"message"`
	Priority        InterruptPriority      `json:"priority"`
	Timeout         time.Duration          `json:"timeout,omitempty"`        // Auto-approve after timeout
	DefaultAction   CommandType            `json:"default_action,omitempty"` // Action to take on timeout
	Metadata        map[string]interface{} `json:"metadata,omitempty"`

	// StreamingExpiryBehavior controls what happens when a STREAMING request expires.
	//   - "implicit_deny" (default): No action applied, user must resume
	//   - "apply_default": Apply DefaultAction
	StreamingExpiryBehavior StreamingExpiryBehavior `json:"streaming_expiry_behavior,omitempty"`

	// NonStreamingExpiryBehavior controls what happens when a NON-STREAMING request expires.
	//   - "apply_default" (default): Apply DefaultAction
	//   - "implicit_deny": No action applied, user must resume
	NonStreamingExpiryBehavior NonStreamingExpiryBehavior `json:"non_streaming_expiry_behavior,omitempty"`

	// DefaultRequestMode is used when RequestMode is not set in the context.
	//   - "non_streaming" (default): Treat as async request
	//   - "streaming": Treat as live connection
	// NOTE: When this default is used, a WARN log is emitted and a trace event is added.
	DefaultRequestMode RequestMode `json:"default_request_mode,omitempty"`
}

// InterruptReason categorizes why an interrupt was triggered
type InterruptReason string

const (
	ReasonPlanApproval       InterruptReason = "plan_approval"
	ReasonSensitiveOperation InterruptReason = "sensitive_operation"
	ReasonOutputValidation   InterruptReason = "output_validation"
	ReasonEscalation         InterruptReason = "escalation"
	ReasonContextGathering   InterruptReason = "context_gathering"
	ReasonCustom             InterruptReason = "custom"
)

// InterruptPriority indicates urgency of human response
type InterruptPriority string

const (
	PriorityLow      InterruptPriority = "low"      // Can wait, auto-timeout ok
	PriorityNormal   InterruptPriority = "normal"   // Standard review
	PriorityHigh     InterruptPriority = "high"     // Needs prompt attention
	PriorityCritical InterruptPriority = "critical" // Blocking, no timeout
)

// -----------------------------------------------------------------------------
// Execution Checkpoint
// -----------------------------------------------------------------------------

// ExecutionCheckpoint contains all state needed to resume execution
type ExecutionCheckpoint struct {
	CheckpointID string `json:"checkpoint_id"`
	RequestID    string `json:"request_id"`

	// OriginalRequestID is the request_id from the first request in a HITL conversation.
	// For initial requests: OriginalRequestID == RequestID
	// For resume requests: OriginalRequestID is preserved from the original request
	// Use this field to correlate all checkpoints in a conversation and to search
	// distributed traces using the original_request_id tag.
	OriginalRequestID string `json:"original_request_id,omitempty"`

	// OriginalTraceID is the W3C trace ID from the span active when this checkpoint
	// was created. Populated by createCheckpoint for use in BuildResumeContext.
	// Empty when no active OTel span existed at interrupt time (e.g., untraced workers
	// before RC6 is deployed). BuildResumeContext degrades gracefully — it creates a
	// valid unlinked root span rather than failing.
	OriginalTraceID string `json:"original_trace_id,omitempty"`

	// OriginalSpanID is the W3C span ID from the span active when this checkpoint
	// was created. Populated alongside OriginalTraceID by createCheckpoint.
	OriginalSpanID string `json:"original_span_id,omitempty"`

	// AgentName is the name of the agent that created this checkpoint.
	// Populated from TRUVAG3_AGENT_NAME (with TRUVAG3_K8S_SERVICE_NAME fallback) at
	// checkpoint creation time. Used to identify the owning agent and look it up
	// in the service registry if needed.
	// Empty in single-agent deployments that don't set TRUVAG3_AGENT_NAME.
	AgentName string `json:"agent_name,omitempty"`

	// AgentAddress is the HTTP base address of the agent at checkpoint creation time.
	// Populated from the agent's registered service address. Enables direct command
	// routing without a service registry lookup — the registry viewer can POST
	// /hitl/command directly to this address using only the checkpoint data.
	// Example: "http://agent-with-human-approval.truvag3-examples.svc.cluster.local:8352"
	AgentAddress string `json:"agent_address,omitempty"`

	// Interrupt context
	InterruptPoint InterruptPoint     `json:"interrupt_point"`
	Decision       *InterruptDecision `json:"decision"`

	// Execution state
	Plan              *RoutingPlan           `json:"plan"`
	CompletedSteps    []StepResult           `json:"completed_steps"`
	CurrentStep       *RoutingStep           `json:"current_step,omitempty"`
	CurrentStepResult *StepResult            `json:"current_step_result,omitempty"`
	StepResults       map[string]*StepResult `json:"step_results"`

	// ResolvedParameters contains the actual parameter values after resolution.
	// For step-level HITL (Scenario 2), this shows users the real values
	// (e.g., amount: 15000) instead of templates (e.g., amount: "{{step-1.amount}}").
	ResolvedParameters map[string]interface{} `json:"resolved_parameters,omitempty"`

	// Metadata
	OriginalRequest string                 `json:"original_request"`
	UserContext     map[string]interface{} `json:"user_context,omitempty"`

	// RequestMode indicates how the original request was submitted.
	// This determines expiry behavior:
	// - "streaming": Implicit deny on expiry (user saw the dialog)
	// - "non_streaming": Apply DefaultAction on expiry
	RequestMode RequestMode `json:"request_mode,omitempty"`

	// Iterative planning context (for resume after HITL approval)
	PhaseNumber        int                    `json:"phase_number,omitempty"`
	AccumulatedResults map[string]*StepResult `json:"accumulated_results,omitempty"`
	ExecutedStepIDs    []string               `json:"executed_step_ids,omitempty"`
	ContinuationNote   string                 `json:"continuation_note_checkpoint,omitempty"`

	// SkillState preserves the body-free pinned candidates, active decisions,
	// resource-selection history, and diagnostics needed for causal resume.
	// Instruction and resource bodies are never checkpointed.
	SkillState        *SkillExecutionState `json:"skill_state,omitempty"`
	SkillCacheContext *SkillCacheContext   `json:"skill_cache_context,omitempty"`

	// Timing
	CreatedAt time.Time        `json:"created_at"`
	ExpiresAt time.Time        `json:"expires_at"`
	Status    CheckpointStatus `json:"status"`
}

// InterruptPoint identifies where in execution the interrupt occurred
type InterruptPoint string

const (
	InterruptPointPlanGenerated    InterruptPoint = "plan_generated"
	InterruptPointBeforeStep       InterruptPoint = "before_step"
	InterruptPointAfterStep        InterruptPoint = "after_step"
	InterruptPointOnError          InterruptPoint = "on_error"
	InterruptPointContextGathering InterruptPoint = "context_gathering"
)

// CheckpointStatus tracks the lifecycle of a checkpoint
type CheckpointStatus string

const (
	// Human-initiated statuses (explicit user action)
	CheckpointStatusPending   CheckpointStatus = "pending"   // Awaiting human response
	CheckpointStatusApproved  CheckpointStatus = "approved"  // Human approved, ready to resume
	CheckpointStatusRejected  CheckpointStatus = "rejected"  // Human rejected
	CheckpointStatusEdited    CheckpointStatus = "edited"    // Human edited, ready to resume
	CheckpointStatusCompleted CheckpointStatus = "completed" // Execution completed
	CheckpointStatusAborted   CheckpointStatus = "aborted"   // User aborted
	// CheckpointStatusPreparing is a transient framework-owned state used while
	// orchestration attaches durable run state. It is never resumable or exposed
	// as pending human work.
	CheckpointStatusPreparing CheckpointStatus = "preparing"

	// Expiry status for STREAMING requests (implicit deny - no action applied)
	// User must manually resume if desired
	CheckpointStatusExpired CheckpointStatus = "expired"

	// Expiry statuses for NON-STREAMING requests (policy-driven)
	// DefaultAction from policy was auto-applied on timeout
	CheckpointStatusExpiredApproved CheckpointStatus = "expired_approved" // Auto-approved on timeout
	CheckpointStatusExpiredRejected CheckpointStatus = "expired_rejected" // Auto-rejected on timeout
	CheckpointStatusExpiredAborted  CheckpointStatus = "expired_aborted"  // Auto-aborted on timeout
)

// RequestMode indicates how the original request was submitted.
// This determines expiry behavior for checkpoints.
type RequestMode string

const (
	// RequestModeStreaming indicates the user is actively connected (SSE/WebSocket).
	// On expiry: Implicit deny - checkpoint marked as "expired", no action applied.
	// Rationale: User saw the approval dialog but didn't act.
	RequestModeStreaming RequestMode = "streaming"

	// RequestModeNonStreaming indicates async submission (HTTP 202 + polling).
	// On expiry: Apply configured DefaultAction from policy.
	// Rationale: User may expect autonomous processing.
	RequestModeNonStreaming RequestMode = "non_streaming"
)

// StreamingExpiryBehavior controls what happens when a STREAMING request's
// checkpoint expires without a response.
type StreamingExpiryBehavior string

const (
	// StreamingExpiryImplicitDeny marks the checkpoint as "expired" with no action applied.
	// The user must manually resume if they want to proceed.
	// This is the DEFAULT for streaming requests.
	StreamingExpiryImplicitDeny StreamingExpiryBehavior = "implicit_deny"

	// StreamingExpiryApplyDefault applies the policy's DefaultAction.
	// Use this when you want consistent behavior regardless of request mode.
	StreamingExpiryApplyDefault StreamingExpiryBehavior = "apply_default"
)

// NonStreamingExpiryBehavior controls what happens when a NON-STREAMING request's
// checkpoint expires without a response.
type NonStreamingExpiryBehavior string

const (
	// NonStreamingExpiryApplyDefault applies the policy's DefaultAction.
	// This is the DEFAULT for non-streaming requests.
	NonStreamingExpiryApplyDefault NonStreamingExpiryBehavior = "apply_default"

	// NonStreamingExpiryImplicitDeny marks the checkpoint as "expired" with no action applied.
	// Use this when you want all expired checkpoints to require manual intervention.
	NonStreamingExpiryImplicitDeny NonStreamingExpiryBehavior = "implicit_deny"
)

// -----------------------------------------------------------------------------
// Command (Human Response)
// -----------------------------------------------------------------------------

// Command represents a human decision in response to an interrupt
type Command struct {
	CommandID    string      `json:"command_id"`
	CheckpointID string      `json:"checkpoint_id"`
	Type         CommandType `json:"type"`

	// Optional payload based on command type
	EditedPlan   *RoutingPlan           `json:"edited_plan,omitempty"`   // For plan edits
	EditedStep   *RoutingStep           `json:"edited_step,omitempty"`   // For step edits
	EditedParams map[string]interface{} `json:"edited_params,omitempty"` // For parameter edits
	Feedback     string                 `json:"feedback,omitempty"`      // Rejection reason
	Response     string                 `json:"response,omitempty"`      // Context gathering response

	// Audit
	UserID    string    `json:"user_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// CommandType defines the type of human decision
type CommandType string

const (
	CommandApprove CommandType = "approve" // Proceed as planned
	CommandEdit    CommandType = "edit"    // Proceed with modifications
	CommandReject  CommandType = "reject"  // Stop and provide feedback
	CommandSkip    CommandType = "skip"    // Skip current step, continue
	CommandAbort   CommandType = "abort"   // Stop entire workflow
	CommandRetry   CommandType = "retry"   // Retry with new parameters
	CommandRespond CommandType = "respond" // Provide requested information
)

// -----------------------------------------------------------------------------
// Resume Result
// -----------------------------------------------------------------------------

// ResumeResult contains the outcome of processing a command
type ResumeResult struct {
	CheckpointID string       `json:"checkpoint_id"`
	Action       CommandType  `json:"action"`
	ShouldResume bool         `json:"should_resume"`
	ModifiedPlan *RoutingPlan `json:"modified_plan,omitempty"`
	SkipStep     bool         `json:"skip_step,omitempty"`
	Abort        bool         `json:"abort,omitempty"`
	Feedback     string       `json:"feedback,omitempty"`
}

// -----------------------------------------------------------------------------
// HITL Configuration
// -----------------------------------------------------------------------------

// HITLConfig configures Human-in-the-Loop behavior
type HITLConfig struct {
	// Enable/disable HITL (default: false for backward compatibility)
	Enabled bool `json:"enabled"`

	// Policy configuration
	RequirePlanApproval   bool     `json:"require_plan_approval"`
	SensitiveCapabilities []string `json:"sensitive_capabilities"`
	SensitiveAgents       []string `json:"sensitive_agents"`
	EscalateAfterRetries  int      `json:"escalate_after_retries"`

	// Step-level HITL configuration (Scenario 2 only)
	// Capabilities listed here trigger step-level approval WITHOUT plan-level approval.
	// Use this when you want execution to start, but pause before specific sensitive steps.
	StepSensitiveCapabilities []string `json:"step_sensitive_capabilities"`
	StepSensitiveAgents       []string `json:"step_sensitive_agents"`

	// Timeout configuration
	DefaultTimeout time.Duration `json:"default_timeout"`
	DefaultAction  CommandType   `json:"default_action"`

	// Storage configuration
	CheckpointTTL time.Duration `json:"checkpoint_ttl"`
	// KeyPrefix remains for the legacy compatibility factory.
	// Deprecated: configure provider namespacing with redisprovider.WithNamespace.
	KeyPrefix string `json:"key_prefix"`

	// Expiry processor configuration
	// Controls how expired checkpoints are processed in the background.
	// See HITL_EXPIRY_PROCESSOR_DESIGN.md for details.
	ExpiryProcessor ExpiryProcessorConfig `json:"expiry_processor"`
}

// HITLOption configures HITLConfig using the functional options pattern.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md "Intelligent Configuration Over Convention".
type HITLOption func(*HITLConfig)

// DefaultHITLConfig returns sensible defaults for HITL configuration.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md: "Smart Defaults - Framework should work with minimal configuration"
func DefaultHITLConfig() HITLConfig {
	return HITLConfig{
		Enabled:              false, // Opt-in for backward compatibility
		RequirePlanApproval:  false,
		EscalateAfterRetries: 3,
		DefaultTimeout:       5 * time.Minute,
		DefaultAction:        CommandReject, // HITL enabled = require explicit approval
		CheckpointTTL:        24 * time.Hour,
		KeyPrefix:            "truvag3:hitl",
		ExpiryProcessor: ExpiryProcessorConfig{
			Enabled:           true,             // Expiry processing enabled by default
			ScanInterval:      10 * time.Second, // Scan every 10 seconds
			BatchSize:         100,              // Process up to 100 checkpoints per scan
			DeliverySemantics: DeliveryAtMostOnce,
		},
	}
}

// WithExpiryProcessor configures the expiry processor with smart defaults.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md "Intelligent Configuration Over Convention":
// - Auto-configure related settings when intent is clear
// - Always allow explicit configuration to override defaults
//
// Example:
//
//	config := DefaultHITLConfig()
//	WithExpiryProcessor(ExpiryProcessorConfig{
//	    Enabled:      true,
//	    ScanInterval: 5 * time.Second,
//	})(&config)
func WithExpiryProcessor(config ExpiryProcessorConfig) HITLOption {
	return func(h *HITLConfig) {
		// Apply smart defaults when intent is clear
		if config.Enabled && config.ScanInterval == 0 {
			config.ScanInterval = 10 * time.Second
		}
		if config.Enabled && config.BatchSize == 0 {
			config.BatchSize = 100
		}
		if config.DeliverySemantics == "" {
			config.DeliverySemantics = DeliveryAtMostOnce
		}

		h.ExpiryProcessor = config
	}
}

// NewHITLConfig creates a HITLConfig with sensible defaults and applies the given options.
// This is the recommended way to create a HITLConfig.
//
// Example:
//
//	config := NewHITLConfig(
//	    WithExpiryProcessor(ExpiryProcessorConfig{
//	        Enabled:      true,
//	        ScanInterval: 5 * time.Second,
//	    }),
//	)
func NewHITLConfig(opts ...HITLOption) HITLConfig {
	config := DefaultHITLConfig()
	for _, opt := range opts {
		opt(&config)
	}
	return config
}

// ApplyHITLOptions applies the given options to an existing HITLConfig.
// Useful when you need to modify an existing config.
func ApplyHITLOptions(config *HITLConfig, opts ...HITLOption) {
	for _, opt := range opts {
		opt(config)
	}
}

// -----------------------------------------------------------------------------
// Workflow HITL Configuration (for YAML-based workflows)
// -----------------------------------------------------------------------------

// WorkflowHITLConfig configures workflow-level HITL behavior
type WorkflowHITLConfig struct {
	Enabled        bool          `yaml:"enabled" json:"enabled"`
	DefaultTimeout time.Duration `yaml:"default_timeout" json:"default_timeout"`
	DefaultAction  CommandType   `yaml:"default_action" json:"default_action"`
}

// StepApprovalConfig configures pre-step approval
type StepApprovalConfig struct {
	Reason   string            `yaml:"reason" json:"reason"`
	Priority InterruptPriority `yaml:"priority" json:"priority"`
	Timeout  time.Duration     `yaml:"timeout" json:"timeout"`
}

// StepValidationConfig configures post-step validation
type StepValidationConfig struct {
	Reason   string            `yaml:"reason" json:"reason"`
	Priority InterruptPriority `yaml:"priority" json:"priority"`
	Timeout  time.Duration     `yaml:"timeout" json:"timeout"`
}

// -----------------------------------------------------------------------------
// Option Functions (for dependency injection)
// -----------------------------------------------------------------------------

// InterruptControllerOption configures optional dependencies for DefaultInterruptController
type InterruptControllerOption func(*DefaultInterruptController)

// WithControllerLogger sets the logger for the interrupt controller
func WithControllerLogger(logger core.Logger) InterruptControllerOption {
	return func(c *DefaultInterruptController) {
		if logger == nil {
			return
		}
		// Use ComponentAwareLogger for component-based log segregation (per LOGGING_IMPLEMENTATION_GUIDE.md)
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			c.logger = cal.WithComponent("framework/orchestration")
		} else {
			c.logger = logger
		}
	}
}

// WithControllerTelemetry sets the telemetry provider for the interrupt controller
func WithControllerTelemetry(telemetry core.Telemetry) InterruptControllerOption {
	return func(c *DefaultInterruptController) {
		c.telemetry = telemetry
	}
}

// WithControllerCommandStore sets the command store for Pub/Sub notification on ProcessCommand.
// When set, ProcessCommand publishes approved/rejected commands so that SubscribeCommand
// listeners (e.g., ProcessQuery wait-and-resume) receive the decision in real time.
func WithControllerCommandStore(cs CommandStore) InterruptControllerOption {
	return func(c *DefaultInterruptController) {
		c.commandStore = cs
	}
}

// WithControllerCheckpointPersistence supplies the narrow persistence
// capability without requiring the legacy expiry lifecycle methods carried by
// CheckpointStore.
func WithControllerCheckpointPersistence(store CheckpointPersistence) InterruptControllerOption {
	return func(c *DefaultInterruptController) {
		if store != nil {
			c.store = store
		}
	}
}

// WebhookHandlerOption configures optional dependencies for WebhookInterruptHandler
type WebhookHandlerOption func(*WebhookInterruptHandler)

// WithHandlerCircuitBreaker sets the circuit breaker for the webhook handler
func WithHandlerCircuitBreaker(cb core.CircuitBreaker) WebhookHandlerOption {
	return func(h *WebhookInterruptHandler) {
		h.circuitBreaker = cb
	}
}

// WithHandlerLogger sets the logger for the webhook handler
func WithHandlerLogger(logger core.Logger) WebhookHandlerOption {
	return func(h *WebhookInterruptHandler) {
		if logger == nil {
			return
		}
		// Use ComponentAwareLogger for component-based log segregation (per LOGGING_IMPLEMENTATION_GUIDE.md)
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			h.logger = cal.WithComponent("framework/orchestration")
		} else {
			h.logger = logger
		}
	}
}

// WithHandlerTelemetry sets the telemetry provider for the webhook handler
func WithHandlerTelemetry(telemetry core.Telemetry) WebhookHandlerOption {
	return func(h *WebhookInterruptHandler) {
		h.telemetry = telemetry
	}
}

// PolicyOption configures optional dependencies for RuleBasedPolicy
type PolicyOption func(*RuleBasedPolicy)

// WithPolicyLogger sets the logger for the policy
func WithPolicyLogger(logger core.Logger) PolicyOption {
	return func(p *RuleBasedPolicy) {
		if logger == nil {
			return
		}
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			p.logger = cal.WithComponent("framework/orchestration")
		} else {
			p.logger = logger
		}
	}
}
