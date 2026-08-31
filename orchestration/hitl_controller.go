package orchestration

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// =============================================================================
// DefaultInterruptController - Reference Implementation
// =============================================================================
//
// DefaultInterruptController coordinates all HITL functionality:
// - Evaluates policy decisions via InterruptPolicy
// - Persists state via CheckpointPersistence
// - Sends notifications via InterruptHandler
//
// Per FRAMEWORK_DESIGN_PRINCIPLES.md, telemetry is nil-safe with NoOp default.
//
// Usage:
//
//	controller := NewInterruptController(
//	    policy,
//	    checkpointStore,
//	    webhookHandler,
//	    WithControllerLogger(logger),
//	    WithControllerTelemetry(telemetry),
//	)
//	orchestrator.SetInterruptController(controller)
//
// =============================================================================

// DefaultInterruptController is the framework's reference implementation.
type DefaultInterruptController struct {
	policy       InterruptPolicy
	handler      InterruptHandler
	store        CheckpointPersistence
	commandStore CommandStore // Optional: enables Pub/Sub notification on ProcessCommand

	// Optional dependencies (injected per framework patterns)
	logger    core.Logger    // Defaults to NoOp
	telemetry core.Telemetry // Defaults to NoOp - always nil-check before use
}

// NewInterruptController creates a controller with required dependencies.
// Returns concrete type per Go idiom "return structs, accept interfaces".
func NewInterruptController(
	policy InterruptPolicy,
	store CheckpointPersistence,
	handler InterruptHandler,
	opts ...InterruptControllerOption,
) *DefaultInterruptController {
	c := &DefaultInterruptController{
		policy:  policy,
		store:   store,
		handler: handler,
		logger:  &core.NoOpLogger{}, // Safe default per framework
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// -----------------------------------------------------------------------------
// InterruptController Interface Implementation
// -----------------------------------------------------------------------------

// SetPolicy configures the interrupt policy
func (c *DefaultInterruptController) SetPolicy(policy InterruptPolicy) {
	c.policy = policy
}

// SetHandler configures the notification handler
func (c *DefaultInterruptController) SetHandler(handler InterruptHandler) {
	c.handler = handler
}

// SetCheckpointPersistence configures the provider-neutral request-path store.
func (c *DefaultInterruptController) SetCheckpointPersistence(store CheckpointPersistence) {
	c.store = store
}

// SetCheckpointStore configures state persistence.
// Deprecated: use SetCheckpointPersistence.
func (c *DefaultInterruptController) SetCheckpointStore(store CheckpointStore) {
	c.SetCheckpointPersistence(store)
}

// CheckPlanApproval evaluates policy and triggers interrupt if needed.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md: telemetry is nil-safe.
// Per LOGGING_IMPLEMENTATION_GUIDE.md: use WithContext methods for trace correlation.
// Per DISTRIBUTED_TRACING_GUIDE.md: add span events for tracing visibility.
//
// Note: request_id is retrieved from context (set by orchestrator.ProcessRequest
// via WithRequestID), NOT from RoutingPlan (which doesn't have this field).
//
// Resume Mode: If the context is in resume mode (set via WithResumeMode), HITL
// checks are bypassed to prevent infinite loops during resume execution.
func (c *DefaultInterruptController) CheckPlanApproval(ctx context.Context, plan *RoutingPlan) (*ExecutionCheckpoint, error) {
	// Check if in resume mode - skip HITL to prevent infinite loops
	if checkpointID, ok := IsResumeMode(ctx); ok {
		if c.logger != nil {
			c.logger.DebugWithContext(ctx, "Skipping HITL check - resume mode", map[string]interface{}{
				"operation":          "hitl_plan_approval",
				"resumed_checkpoint": checkpointID,
			})
		}
		// Add span event for observability
		telemetry.AddSpanEvent(ctx, "hitl.plan_approval.skipped_resume_mode",
			attribute.String("resumed_checkpoint", checkpointID),
		)
		return nil, nil
	}

	if c.policy == nil {
		return nil, nil
	}

	// Get request_id from context (set by orchestrator via WithRequestID)
	requestID := GetRequestID(ctx)

	decision, err := c.policy.ShouldApprovePlan(ctx, plan)
	if err != nil {
		// Record error on span (per gold standard in executor.go)
		telemetry.RecordSpanError(ctx, err)
		// Log with operation field (per gold standard pattern)
		if c.logger != nil {
			c.logger.ErrorWithContext(ctx, "Policy check failed", map[string]interface{}{
				"operation":  "hitl_plan_approval",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		return nil, fmt.Errorf("policy check failed: %w", err)
	}

	if !decision.ShouldInterrupt {
		return nil, nil
	}

	// Add span event for tracing visibility (per DISTRIBUTED_TRACING_GUIDE.md)
	// Include detailed information about WHY the interrupt was triggered
	spanAttrs := []attribute.KeyValue{
		attribute.String("request_id", requestID),
		attribute.String("reason", string(decision.Reason)),
		attribute.String("priority", string(decision.Priority)),
		attribute.String("message", decision.Message),
		attribute.Int("step_count", len(plan.Steps)),
	}

	// Extract sensitive details from metadata if available
	if decision.Metadata != nil {
		if hasSensitive, ok := decision.Metadata["has_sensitive_ops"].(bool); ok {
			spanAttrs = append(spanAttrs, attribute.Bool("has_sensitive_ops", hasSensitive))
		}
		if sensitiveDetails, ok := decision.Metadata["sensitive_details"].([]string); ok && len(sensitiveDetails) > 0 {
			spanAttrs = append(spanAttrs, attribute.StringSlice("sensitive_details", sensitiveDetails))
		}
	}

	telemetry.AddSpanEvent(ctx, "hitl.plan_approval.interrupt_triggered", spanAttrs...)

	// Record counter metric (per gold standard pattern in executor.go)
	telemetry.Counter("orchestration.hitl.interrupt_triggered",
		"point", string(InterruptPointPlanGenerated),
		"reason", string(decision.Reason),
		"module", telemetry.ModuleOrchestration,
	)

	// Create and save checkpoint
	checkpoint := c.createCheckpoint(ctx, plan, nil, nil, decision, InterruptPointPlanGenerated)
	if err := c.store.SaveCheckpoint(ctx, checkpoint); err != nil {
		telemetry.RecordSpanError(ctx, err)
		if c.logger != nil {
			c.logger.ErrorWithContext(ctx, "Failed to save checkpoint", map[string]interface{}{
				"operation":     "hitl_checkpoint_save",
				"checkpoint_id": checkpoint.CheckpointID,
				"request_id":    requestID,
				"error":         err.Error(),
			})
		}
		return nil, fmt.Errorf("failed to save checkpoint: %w", err)
	}

	// Notify via handler (non-blocking - checkpoint is already saved)
	if c.handler != nil && !checkpointEnrichmentRequired(ctx) {
		if err := c.handler.NotifyInterrupt(ctx, checkpoint); err != nil {
			// Log warning but don't fail - checkpoint is saved, notification can be retried
			if c.logger != nil {
				c.logger.WarnWithContext(ctx, "Failed to notify interrupt", map[string]interface{}{
					"operation":     "hitl_notify_interrupt",
					"checkpoint_id": checkpoint.CheckpointID,
					"request_id":    requestID,
					"status":        "error",
					"error_type":    "notification",
					"error":         safeInterruptNotificationError(err),
				})
			}
			// Record notification failure metric (Phase 4 - Metrics Integration)
			RecordNotificationFailed(decision.Reason)
		}
	}

	// Add span event for checkpoint saved (per DISTRIBUTED_TRACING_GUIDE.md)
	telemetry.AddSpanEvent(ctx, "hitl.plan_approval.checkpoint_saved",
		attribute.String("request_id", requestID),
		attribute.String("checkpoint_id", checkpoint.CheckpointID),
		attribute.String("plan_id", plan.PlanID),
		attribute.String("reason", string(decision.Reason)),
		attribute.String("message", decision.Message),
		attribute.String("status", string(checkpoint.Status)),
		attribute.String("expires_at", checkpoint.ExpiresAt.Format(time.RFC3339)),
	)

	// Log successful interrupt with trace correlation
	if c.logger != nil {
		c.logger.InfoWithContext(ctx, "Plan approval interrupt triggered", map[string]interface{}{
			"operation":     "hitl_plan_approval_complete",
			"checkpoint_id": checkpoint.CheckpointID,
			"request_id":    requestID,
			"reason":        decision.Reason,
			"priority":      decision.Priority,
		})
	}

	return checkpoint, nil
}

// CheckBeforeStep evaluates policy for pre-step approval.
func (c *DefaultInterruptController) CheckBeforeStep(ctx context.Context, step RoutingStep, plan *RoutingPlan) (*ExecutionCheckpoint, error) {
	// Check if in resume mode - but only skip if this is the exact step that was approved
	if checkpointID, ok := IsResumeMode(ctx); ok {
		// Load the checkpoint to check if it's for THIS step
		// Plan-level checkpoints (InterruptPointPlanGenerated) should NOT skip step-level HITL
		// Only step-level checkpoints (InterruptPointBeforeStep) for THIS step should be skipped
		checkpoint, err := c.store.LoadCheckpoint(ctx, checkpointID)
		if err != nil {
			if c.logger != nil {
				c.logger.WarnWithContext(ctx, "Failed to load resumed checkpoint, proceeding with step HITL", map[string]interface{}{
					"operation":          "hitl_before_step",
					"resumed_checkpoint": checkpointID,
					"step_id":            step.StepID,
					"error":              err.Error(),
				})
			}
			// If we can't load the checkpoint, proceed with HITL check (fail-safe)
		} else if checkpoint != nil {
			// Only skip if:
			// 1. The checkpoint is a before_step checkpoint (not plan_generated)
			// 2. The checkpoint is for THIS specific step
			isStepCheckpoint := checkpoint.InterruptPoint == InterruptPointBeforeStep
			isThisStep := checkpoint.CurrentStep != nil && checkpoint.CurrentStep.StepID == step.StepID

			if isStepCheckpoint && isThisStep {
				if c.logger != nil {
					c.logger.DebugWithContext(ctx, "Skipping step HITL check - resuming from this step's approval", map[string]interface{}{
						"operation":          "hitl_before_step",
						"resumed_checkpoint": checkpointID,
						"step_id":            step.StepID,
					})
				}
				telemetry.AddSpanEvent(ctx, "hitl.before_step.skipped_resume_mode",
					attribute.String("resumed_checkpoint", checkpointID),
					attribute.String("step_id", step.StepID),
				)
				return nil, nil
			}

			// Plan-level resume - proceed with step-level HITL check
			if c.logger != nil {
				c.logger.DebugWithContext(ctx, "Resume mode from plan checkpoint, proceeding with step HITL check", map[string]interface{}{
					"operation":          "hitl_before_step",
					"resumed_checkpoint": checkpointID,
					"checkpoint_type":    string(checkpoint.InterruptPoint),
					"step_id":            step.StepID,
				})
			}
		}
	}

	if c.policy == nil {
		return nil, nil
	}

	requestID := GetRequestID(ctx)

	decision, err := c.policy.ShouldApproveBeforeStep(ctx, step, plan)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if c.logger != nil {
			c.logger.ErrorWithContext(ctx, "Pre-step policy check failed", map[string]interface{}{
				"operation":  "hitl_before_step",
				"request_id": requestID,
				"step_id":    step.StepID,
				"error":      err.Error(),
			})
		}
		return nil, fmt.Errorf("pre-step policy check failed: %w", err)
	}

	if !decision.ShouldInterrupt {
		return nil, nil
	}

	// Add span event
	telemetry.AddSpanEvent(ctx, "hitl.step_approval.interrupt_triggered",
		attribute.String("request_id", requestID),
		attribute.String("step_id", step.StepID),
		attribute.String("reason", string(decision.Reason)),
	)

	// Record metric
	telemetry.Counter("orchestration.hitl.interrupt_triggered",
		"point", string(InterruptPointBeforeStep),
		"reason", string(decision.Reason),
		"module", telemetry.ModuleOrchestration,
	)

	// Create and save checkpoint
	checkpoint := c.createCheckpoint(ctx, plan, &step, nil, decision, InterruptPointBeforeStep)
	if err := c.store.SaveCheckpoint(ctx, checkpoint); err != nil {
		telemetry.RecordSpanError(ctx, err)
		return nil, fmt.Errorf("failed to save checkpoint: %w", err)
	}

	// Notify
	if c.handler != nil && !checkpointEnrichmentRequired(ctx) {
		if err := c.handler.NotifyInterrupt(ctx, checkpoint); err != nil {
			if c.logger != nil {
				c.logger.WarnWithContext(ctx, "Failed to notify interrupt", map[string]interface{}{
					"operation":     "hitl_notify_interrupt",
					"checkpoint_id": checkpoint.CheckpointID,
					"request_id":    requestID,
					"step_id":       step.StepID,
					"status":        "error",
					"error_type":    "notification",
					"error":         safeInterruptNotificationError(err),
				})
			}
		}
	}

	if c.logger != nil {
		c.logger.InfoWithContext(ctx, "Pre-step approval interrupt triggered", map[string]interface{}{
			"operation":     "hitl_before_step_complete",
			"checkpoint_id": checkpoint.CheckpointID,
			"request_id":    requestID,
			"step_id":       step.StepID,
			"agent_name":    step.AgentName,
		})
	}

	return checkpoint, nil
}

// CheckAfterStep evaluates policy for post-step validation.
func (c *DefaultInterruptController) CheckAfterStep(ctx context.Context, step RoutingStep, result *StepResult) (*ExecutionCheckpoint, error) {
	if c.policy == nil {
		return nil, nil
	}

	requestID := GetRequestID(ctx)

	decision, err := c.policy.ShouldApproveAfterStep(ctx, step, result)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if c.logger != nil {
			c.logger.ErrorWithContext(ctx, "Post-step policy check failed", map[string]interface{}{
				"operation":  "hitl_after_step",
				"request_id": requestID,
				"step_id":    step.StepID,
				"error":      err.Error(),
			})
		}
		return nil, fmt.Errorf("post-step policy check failed: %w", err)
	}

	if !decision.ShouldInterrupt {
		return nil, nil
	}

	// Add span event
	telemetry.AddSpanEvent(ctx, "hitl.output_validation.interrupt_triggered",
		attribute.String("request_id", requestID),
		attribute.String("step_id", step.StepID),
		attribute.String("reason", string(decision.Reason)),
	)

	// Record metric
	telemetry.Counter("orchestration.hitl.interrupt_triggered",
		"point", string(InterruptPointAfterStep),
		"reason", string(decision.Reason),
		"module", telemetry.ModuleOrchestration,
	)

	// Create checkpoint with step result
	checkpoint := c.createCheckpoint(ctx, nil, &step, result, decision, InterruptPointAfterStep)
	if err := c.store.SaveCheckpoint(ctx, checkpoint); err != nil {
		telemetry.RecordSpanError(ctx, err)
		return nil, fmt.Errorf("failed to save checkpoint: %w", err)
	}

	// Notify
	if c.handler != nil && !checkpointEnrichmentRequired(ctx) {
		if err := c.handler.NotifyInterrupt(ctx, checkpoint); err != nil {
			if c.logger != nil {
				c.logger.WarnWithContext(ctx, "Failed to notify interrupt", map[string]interface{}{
					"operation":     "hitl_notify_interrupt",
					"checkpoint_id": checkpoint.CheckpointID,
					"request_id":    requestID,
					"step_id":       step.StepID,
					"status":        "error",
					"error_type":    "notification",
					"error":         safeInterruptNotificationError(err),
				})
			}
		}
	}

	if c.logger != nil {
		c.logger.InfoWithContext(ctx, "Post-step validation interrupt triggered", map[string]interface{}{
			"operation":     "hitl_after_step_complete",
			"checkpoint_id": checkpoint.CheckpointID,
			"request_id":    requestID,
			"step_id":       step.StepID,
		})
	}

	return checkpoint, nil
}

// CheckOnError evaluates policy for error escalation.
func (c *DefaultInterruptController) CheckOnError(ctx context.Context, step RoutingStep, err error, attempts int) (*ExecutionCheckpoint, error) {
	if c.policy == nil {
		return nil, nil
	}

	requestID := GetRequestID(ctx)

	decision, policyErr := c.policy.ShouldEscalateError(ctx, step, err, attempts)
	if policyErr != nil {
		telemetry.RecordSpanError(ctx, policyErr)
		if c.logger != nil {
			c.logger.ErrorWithContext(ctx, "Error escalation policy check failed", map[string]interface{}{
				"operation":  "hitl_on_error",
				"request_id": requestID,
				"step_id":    step.StepID,
				"error":      policyErr.Error(),
			})
		}
		return nil, fmt.Errorf("error escalation policy check failed: %w", policyErr)
	}

	if !decision.ShouldInterrupt {
		return nil, nil
	}

	// Add span event
	telemetry.AddSpanEvent(ctx, "hitl.escalation.interrupt_triggered",
		attribute.String("request_id", requestID),
		attribute.String("step_id", step.StepID),
		attribute.Int("attempts", attempts),
		attribute.String("original_error", err.Error()),
	)

	// Record metric
	telemetry.Counter("orchestration.hitl.interrupt_triggered",
		"point", string(InterruptPointOnError),
		"reason", string(decision.Reason),
		"module", telemetry.ModuleOrchestration,
	)

	// Create checkpoint
	checkpoint := c.createCheckpoint(ctx, nil, &step, nil, decision, InterruptPointOnError)
	// Store error info in metadata
	if checkpoint.Decision.Metadata == nil {
		checkpoint.Decision.Metadata = make(map[string]interface{})
	}
	checkpoint.Decision.Metadata["original_error"] = err.Error()
	checkpoint.Decision.Metadata["attempts"] = attempts

	if saveErr := c.store.SaveCheckpoint(ctx, checkpoint); saveErr != nil {
		telemetry.RecordSpanError(ctx, saveErr)
		return nil, fmt.Errorf("failed to save checkpoint: %w", saveErr)
	}

	// Notify
	if c.handler != nil && !checkpointEnrichmentRequired(ctx) {
		if notifyErr := c.handler.NotifyInterrupt(ctx, checkpoint); notifyErr != nil {
			if c.logger != nil {
				c.logger.WarnWithContext(ctx, "Failed to notify escalation", map[string]interface{}{
					"operation":     "hitl_notify_interrupt",
					"checkpoint_id": checkpoint.CheckpointID,
					"request_id":    requestID,
					"step_id":       step.StepID,
					"status":        "error",
					"error_type":    "notification",
					"error":         safeInterruptNotificationError(notifyErr),
				})
			}
		}
	}

	if c.logger != nil {
		c.logger.InfoWithContext(ctx, "Error escalation interrupt triggered", map[string]interface{}{
			"operation":      "hitl_on_error_complete",
			"checkpoint_id":  checkpoint.CheckpointID,
			"request_id":     requestID,
			"step_id":        step.StepID,
			"attempts":       attempts,
			"original_error": err.Error(),
		})
	}

	return checkpoint, nil
}

// ProcessCommand handles a human command and updates checkpoint status.
func (c *DefaultInterruptController) ProcessCommand(ctx context.Context, command *Command) (*ResumeResult, error) {
	if c.store == nil {
		return nil, fmt.Errorf("checkpoint store not configured")
	}

	// Load checkpoint
	checkpoint, err := c.store.LoadCheckpoint(ctx, command.CheckpointID)
	if err != nil {
		return nil, err
	}

	// Validate command
	if checkpoint.Status != CheckpointStatusPending {
		return nil, &ErrInvalidCommand{
			CommandType: command.Type,
			Reason:      fmt.Sprintf("checkpoint is not pending (status: %s)", checkpoint.Status),
		}
	}

	// Add span event with context about what's being approved/rejected
	receivedAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", command.CheckpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("command_type", string(command.Type)),
		attribute.String("interrupt_point", string(checkpoint.InterruptPoint)),
	}

	if command.UserID != "" {
		receivedAttrs = append(receivedAttrs, attribute.String("user_id", command.UserID))
	}

	// Include decision context
	if checkpoint.Decision != nil {
		receivedAttrs = append(receivedAttrs,
			attribute.String("interrupt_reason", string(checkpoint.Decision.Reason)),
		)
	}

	// Include plan info if available
	if checkpoint.Plan != nil {
		receivedAttrs = append(receivedAttrs,
			attribute.String("plan_id", checkpoint.Plan.PlanID),
			attribute.Int("step_count", len(checkpoint.Plan.Steps)),
		)
	}

	telemetry.AddSpanEvent(ctx, "hitl.command.received", receivedAttrs...)

	// Process based on command type
	result := &ResumeResult{
		CheckpointID: command.CheckpointID,
		Action:       command.Type,
	}

	switch command.Type {
	case CommandApprove:
		checkpoint.Status = CheckpointStatusApproved
		result.ShouldResume = true

	case CommandEdit:
		checkpoint.Status = CheckpointStatusEdited
		result.ShouldResume = true
		if command.EditedPlan != nil {
			result.ModifiedPlan = command.EditedPlan
		}

	case CommandReject:
		checkpoint.Status = CheckpointStatusRejected
		result.ShouldResume = false
		result.Feedback = command.Feedback

	case CommandSkip:
		checkpoint.Status = CheckpointStatusApproved
		result.ShouldResume = true
		result.SkipStep = true

	case CommandAbort:
		checkpoint.Status = CheckpointStatusAborted
		result.ShouldResume = false
		result.Abort = true
		result.Feedback = command.Feedback

	case CommandRetry:
		checkpoint.Status = CheckpointStatusApproved
		result.ShouldResume = true
		// Modified params will be in command.EditedParams

	case CommandRespond:
		checkpoint.Status = CheckpointStatusApproved
		result.ShouldResume = true
		// Response is in command.Response

	default:
		return nil, &ErrInvalidCommand{
			CommandType: command.Type,
			Reason:      "unknown command type",
		}
	}

	// Record status transition metric (Phase 4 - Metrics Integration)
	RecordCheckpointStatus(CheckpointStatusPending, checkpoint.Status)

	// Update checkpoint status and remove from pending index if applicable
	// Use UpdateCheckpointStatus instead of SaveCheckpoint to properly manage the pending index
	if err := c.store.UpdateCheckpointStatus(ctx, command.CheckpointID, checkpoint.Status); err != nil {
		// Record command failure
		RecordCommandProcessed(command.Type, false)
		return nil, fmt.Errorf("failed to update checkpoint status: %w", err)
	}

	// Calculate and record approval latency (Phase 4 - Metrics Integration)
	if !checkpoint.CreatedAt.IsZero() {
		latency := time.Since(checkpoint.CreatedAt).Seconds()
		RecordApprovalLatency(latency, command.Type)
	}

	// Record successful command processing (Phase 4 - Metrics Integration)
	RecordCommandProcessed(command.Type, true)

	// Publish command via Pub/Sub so SubscribeCommand listeners receive the decision.
	// This enables the ProcessQuery wait-and-resume pattern (CROSS_AGENT_HITL_PROPAGATION Change 1).
	if c.commandStore != nil {
		if pubErr := c.commandStore.PublishCommand(ctx, command); pubErr != nil {
			// Non-fatal: status is already updated, but subscriber won't be notified.
			// The subscriber will eventually time out and can poll the checkpoint status.
			if c.logger != nil {
				c.logger.WarnWithContext(ctx, "Failed to publish command to subscriber", map[string]interface{}{
					"operation":     "hitl_command_publish",
					"checkpoint_id": command.CheckpointID,
					"command_type":  command.Type,
					"error":         pubErr.Error(),
				})
			}
		}
	}

	// Add span event with detailed approval/rejection information
	commandAttrs := []attribute.KeyValue{
		attribute.String("checkpoint_id", command.CheckpointID),
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("command_type", string(command.Type)),
		attribute.String("decision", string(command.Type)), // approve/reject
		attribute.Bool("should_resume", result.ShouldResume),
		attribute.String("new_status", string(checkpoint.Status)),
	}

	// Include original interrupt reason for context
	if checkpoint.Decision != nil {
		commandAttrs = append(commandAttrs,
			attribute.String("original_reason", string(checkpoint.Decision.Reason)),
			attribute.String("original_message", checkpoint.Decision.Message),
		)
	}

	// Include user info if available
	if command.UserID != "" {
		commandAttrs = append(commandAttrs, attribute.String("user_id", command.UserID))
	}

	// Calculate and include approval latency
	if !checkpoint.CreatedAt.IsZero() {
		latencyMs := time.Since(checkpoint.CreatedAt).Milliseconds()
		commandAttrs = append(commandAttrs, attribute.Int64("approval_latency_ms", latencyMs))
	}

	telemetry.AddSpanEvent(ctx, "hitl.command.processed", commandAttrs...)

	if c.logger != nil {
		c.logger.InfoWithContext(ctx, "Command processed", map[string]interface{}{
			"operation":     "hitl_command_processed",
			"checkpoint_id": command.CheckpointID,
			"command_type":  command.Type,
			"user_id":       command.UserID,
			"should_resume": result.ShouldResume,
		})
	}

	return result, nil
}

// ResumeExecution continues workflow execution from a checkpoint.
// This is a stub - actual resume logic depends on the orchestrator implementation.
func (c *DefaultInterruptController) ResumeExecution(ctx context.Context, checkpointID string) (*ExecutionResult, error) {
	// Load checkpoint to get execution state
	checkpoint, err := c.store.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}

	// Verify checkpoint is in a resumable state
	if checkpoint.Status != CheckpointStatusApproved && checkpoint.Status != CheckpointStatusEdited {
		return nil, fmt.Errorf("checkpoint %s is not in a resumable state (status: %s)", checkpointID, checkpoint.Status)
	}

	// Add span event
	telemetry.AddSpanEvent(ctx, "hitl.resume.started",
		attribute.String("checkpoint_id", checkpointID),
		attribute.String("request_id", checkpoint.RequestID),
	)

	if c.logger != nil {
		c.logger.InfoWithContext(ctx, "Execution resume started", map[string]interface{}{
			"operation":       "hitl_resume_started",
			"checkpoint_id":   checkpointID,
			"request_id":      checkpoint.RequestID,
			"interrupt_point": checkpoint.InterruptPoint,
		})
	}

	// Note: The actual resume logic would be implemented by the orchestrator
	// This controller just provides the checkpoint data and validates state
	// The orchestrator calls this, gets the checkpoint, and continues execution

	// Mark checkpoint as completed
	checkpoint.Status = CheckpointStatusCompleted
	if err := c.store.SaveCheckpoint(ctx, checkpoint); err != nil {
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Failed to mark checkpoint completed", map[string]interface{}{
				"operation":     "hitl_resume_complete",
				"checkpoint_id": checkpointID,
				"error":         err.Error(),
			})
		}
	}

	// Return a placeholder result - the orchestrator will build the actual result
	return &ExecutionResult{
		PlanID:  checkpoint.Plan.PlanID,
		Success: true,
		Metadata: map[string]interface{}{
			"resumed_from_checkpoint": checkpointID,
			"interrupt_point":         checkpoint.InterruptPoint,
		},
	}, nil
}

// UpdateCheckpointProgress updates a checkpoint with completed steps.
// Called by the executor before returning ErrInterrupted for step-level interrupts.
// This enables proper resume behavior that skips already-completed steps.
func (c *DefaultInterruptController) UpdateCheckpointProgress(ctx context.Context, checkpointID string, completedSteps []StepResult) error {
	// Load checkpoint
	checkpoint, err := c.store.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		return fmt.Errorf("failed to load checkpoint: %w", err)
	}

	// Update completed steps
	checkpoint.CompletedSteps = completedSteps

	// Build step results map for quick lookup on resume
	if checkpoint.StepResults == nil {
		checkpoint.StepResults = make(map[string]*StepResult)
	}
	for i := range completedSteps {
		step := &completedSteps[i]
		checkpoint.StepResults[step.StepID] = step
	}

	// Save updated checkpoint
	if err := c.store.SaveCheckpoint(ctx, checkpoint); err != nil {
		return fmt.Errorf("failed to save checkpoint progress: %w", err)
	}

	if c.logger != nil {
		c.logger.InfoWithContext(ctx, "Checkpoint progress updated", map[string]interface{}{
			"operation":       "hitl_checkpoint_progress_updated",
			"checkpoint_id":   checkpointID,
			"completed_steps": len(completedSteps),
		})
	}

	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.progress_updated",
		attribute.String("checkpoint_id", checkpointID),
		attribute.Int("completed_steps", len(completedSteps)),
	)

	return nil
}

// SaveEnrichedCheckpoint persists an enriched checkpoint (with accumulated
// step results from all phases) back to the checkpoint store. Called by
// the orchestrator after phase enrichment to ensure BuildResumeContext
// loads complete step results on HITL resume.
func (c *DefaultInterruptController) SaveEnrichedCheckpoint(ctx context.Context, checkpoint *ExecutionCheckpoint) error {
	if c.store == nil {
		return nil
	}
	if err := c.store.SaveCheckpoint(ctx, checkpoint); err != nil {
		return err
	}
	// Framework-owned checkpoints suppress their initial notification while
	// lifecycle state is incomplete. Notify only after the authoritative save.
	if checkpointEnrichmentRequired(ctx) && c.handler != nil {
		if err := c.handler.NotifyInterrupt(ctx, checkpoint); err != nil {
			requestID := GetRequestID(ctx)
			if requestID == "" {
				requestID = checkpoint.RequestID
			}
			telemetry.AddSpanEvent(ctx, "hitl.notification.failed",
				attribute.String("request_id", requestID),
				attribute.String("checkpoint_id", checkpoint.CheckpointID),
				attribute.String("original_request_id", checkpoint.OriginalRequestID),
				attribute.String("error_type", "notification"),
			)
			if c.logger != nil {
				c.logger.WarnWithContext(ctx, "Failed to notify interrupt", map[string]interface{}{
					"operation":           "hitl_notify_interrupt",
					"checkpoint_id":       checkpoint.CheckpointID,
					"request_id":          requestID,
					"original_request_id": checkpoint.OriginalRequestID,
					"status":              "error",
					"error_type":          "notification",
					"error":               safeInterruptNotificationError(err),
				})
			}
			if checkpoint.Decision != nil {
				RecordNotificationFailed(checkpoint.Decision.Reason)
			}
		}
	}
	return nil
}

func safeInterruptNotificationError(err error) string {
	if err == nil {
		return ""
	}
	return "interrupt notification failed"
}

// -----------------------------------------------------------------------------
// Helper Methods
// -----------------------------------------------------------------------------

// createCheckpoint creates an ExecutionCheckpoint with proper request_id from context.
// The request_id is retrieved from context (set by orchestrator.ProcessRequest via WithRequestID).
// UserContext is populated from context metadata (set via WithMetadata) for HITL resume support.
// The original trace_id is stored for cross-trace correlation in distributed tracing.
func (c *DefaultInterruptController) createCheckpoint(
	ctx context.Context,
	plan *RoutingPlan,
	step *RoutingStep,
	result *StepResult,
	decision *InterruptDecision,
	point InterruptPoint,
) *ExecutionCheckpoint {
	// Get request_id from context (set by orchestrator via WithRequestID)
	requestID := GetRequestID(ctx)

	// Get original_request_id from baggage for HITL conversation correlation.
	// For initial requests: original_request_id == request_id (set by orchestrator)
	// For resume requests: original_request_id is preserved from the first request
	originalRequestID := requestID // Default to current request_id
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		if origID := bag["original_request_id"]; origID != "" {
			originalRequestID = origID
		}
	}

	// Get metadata from context for UserContext (set via WithMetadata)
	// This preserves session_id, user_id, etc. for resume operations
	userContext := GetMetadata(ctx)
	if userContext == nil {
		userContext = make(map[string]interface{})
	}

	// Capture OTel trace context for checkpoint fields (RC7-B2).
	// Stored in both typed fields (primary, BuildResumeContext reads these first) and
	// UserContext (backward compat during transition — old consumers read UserContext).
	// After all consumers migrate to typed fields, the UserContext writes can be removed.
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		userContext["original_trace_id"] = tc.TraceID
		userContext["original_span_id"] = tc.SpanID
	}

	// Calculate expiry time
	expiresAt := time.Now().Add(24 * time.Hour) // Default 24h
	if decision.Timeout > 0 {
		expiresAt = time.Now().Add(decision.Timeout)
	}

	checkpoint := &ExecutionCheckpoint{
		CheckpointID:      fmt.Sprintf("cp-%s", uuid.New().String()[:16]),
		RequestID:         requestID,
		OriginalRequestID: originalRequestID,
		OriginalTraceID:   tc.TraceID,
		OriginalSpanID:    tc.SpanID,
		InterruptPoint:    point,
		Decision:          decision,
		Plan:              plan,
		CurrentStep:       step,
		CreatedAt:         time.Now(),
		ExpiresAt:         expiresAt,
		Status:            CheckpointStatusPending,
		StepResults:       make(map[string]*StepResult),
		UserContext:       userContext,
	}
	if checkpointEnrichmentRequired(ctx) {
		checkpoint.Status = CheckpointStatusPreparing
	}

	if result != nil {
		checkpoint.CurrentStepResult = result
	}

	if plan != nil {
		checkpoint.OriginalRequest = plan.OriginalRequest
	}

	// Get resolved parameters from context for HITL step-level visibility (Scenario 2)
	// This allows users to see actual values (e.g., amount: 15000) instead of templates
	if resolvedParams := GetResolvedParams(ctx); resolvedParams != nil {
		checkpoint.ResolvedParameters = resolvedParams
	}

	// Get request mode from context for expiry behavior determination
	// This is set by the application's HTTP handlers via WithRequestMode()
	// See HITL_EXPIRY_PROCESSOR_DESIGN.md for mode-aware expiry behavior
	if requestMode := GetRequestMode(ctx); requestMode != "" {
		checkpoint.RequestMode = requestMode
		// Record that RequestMode was found in context (helps debug expiry behavior issues)
		telemetry.AddSpanEvent(ctx, "hitl.checkpoint.request_mode_set",
			attribute.String("checkpoint_id", checkpoint.CheckpointID),
			attribute.String("request_id", requestID),
			attribute.String("request_mode", string(requestMode)),
		)
	} else {
		// Log warning when RequestMode is not set - this affects expiry behavior
		// Streaming requests should set RequestModeStreaming for implicit_deny behavior
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "RequestMode not set in context - expiry will use default behavior", map[string]interface{}{
				"operation":     "hitl_checkpoint_create",
				"checkpoint_id": checkpoint.CheckpointID,
				"request_id":    requestID,
				"hint":          "Call orchestration.WithRequestMode(ctx, RequestModeStreaming) for streaming requests",
			})
		}
		telemetry.AddSpanEvent(ctx, "hitl.checkpoint.request_mode_missing",
			attribute.String("checkpoint_id", checkpoint.CheckpointID),
			attribute.String("request_id", requestID),
		)
	}

	// Record checkpoint creation metric (Phase 4 - Metrics Integration)
	RecordCheckpointCreated(point, decision.Reason)

	return checkpoint
}

// Compile-time interface compliance check
var _ InterruptController = (*DefaultInterruptController)(nil)
