package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultCheckpointClaimLease  = 30 * time.Second
	defaultCheckpointScanTimeout = 30 * time.Second
	maxCheckpointClaimOwnerLen   = 128
)

// CheckpointExpiryProcessor owns expiry polling and callback delivery without
// requiring a storage provider to expose lifecycle methods.
type CheckpointExpiryProcessor struct {
	persistence CheckpointPersistence
	source      ExpiredCheckpointSource
	callback    ExpiryCallback
	config      ExpiryProcessorConfig
	owner       string
	lease       time.Duration
	scanTimeout time.Duration
	logger      core.Logger
	telemetry   core.Telemetry
	policy      CheckpointExpiryPolicy
}

// CheckpointExpiryPolicy resolves behavior that is independent of storage.
// Its defaults are deterministic and environment-free.
type CheckpointExpiryPolicy struct {
	DefaultRequestMode   RequestMode
	StreamingBehavior    StreamingExpiryBehavior
	NonStreamingBehavior NonStreamingExpiryBehavior
}

// CheckpointExpiryRuntimeConfig contains operational processor limits that are
// independent of expiry behavior and storage-provider choice.
type CheckpointExpiryRuntimeConfig struct {
	ClaimLease time.Duration
}

// DefaultCheckpointExpiryRuntimeConfig returns deterministic, environment-free
// runtime limits.
func DefaultCheckpointExpiryRuntimeConfig() CheckpointExpiryRuntimeConfig {
	return CheckpointExpiryRuntimeConfig{ClaimLease: defaultCheckpointClaimLease}
}

// LoadCheckpointExpiryRuntimeConfigFromEnvironment applies deployment-owned
// numeric tuning to a code-provided base. Explicit code options may be applied
// afterward and therefore retain highest precedence.
func LoadCheckpointExpiryRuntimeConfigFromEnvironment(
	base CheckpointExpiryRuntimeConfig,
	lookup func(string) (string, bool),
) (CheckpointExpiryRuntimeConfig, error) {
	if lookup == nil {
		return CheckpointExpiryRuntimeConfig{}, fmt.Errorf("orchestration: environment lookup is required")
	}
	if base.ClaimLease <= 0 {
		base = DefaultCheckpointExpiryRuntimeConfig()
	}
	const variable = "TRUVAG3_HITL_EXPIRY_CLAIM_LEASE"
	if raw, present := lookup(variable); present {
		lease, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil || lease <= 0 {
			return CheckpointExpiryRuntimeConfig{}, fmt.Errorf("orchestration: %s must be a positive duration", variable)
		}
		base.ClaimLease = lease
	}
	return base, nil
}

func DefaultCheckpointExpiryPolicy() CheckpointExpiryPolicy {
	return CheckpointExpiryPolicy{
		DefaultRequestMode:   RequestModeNonStreaming,
		StreamingBehavior:    StreamingExpiryImplicitDeny,
		NonStreamingBehavior: NonStreamingExpiryApplyDefault,
	}
}

type CheckpointExpiryProcessorOption func(*CheckpointExpiryProcessor) error

func WithCheckpointExpiryOwner(owner string) CheckpointExpiryProcessorOption {
	return func(processor *CheckpointExpiryProcessor) error {
		owner = strings.TrimSpace(owner)
		if owner == "" || len(owner) > maxCheckpointClaimOwnerLen {
			return fmt.Errorf("orchestration: checkpoint expiry owner must contain 1-%d characters", maxCheckpointClaimOwnerLen)
		}
		processor.owner = owner
		return nil
	}
}

func WithCheckpointExpiryLease(lease time.Duration) CheckpointExpiryProcessorOption {
	return WithCheckpointExpiryRuntimeConfig(CheckpointExpiryRuntimeConfig{ClaimLease: lease})
}

// WithCheckpointExpiryRuntimeConfig applies code-owned operational limits.
func WithCheckpointExpiryRuntimeConfig(config CheckpointExpiryRuntimeConfig) CheckpointExpiryProcessorOption {
	return func(processor *CheckpointExpiryProcessor) error {
		if config.ClaimLease <= 0 {
			return fmt.Errorf("orchestration: checkpoint expiry claim lease must be positive")
		}
		processor.lease = config.ClaimLease
		return nil
	}
}

func WithCheckpointExpiryLogger(logger core.Logger) CheckpointExpiryProcessorOption {
	return func(processor *CheckpointExpiryProcessor) error {
		if logger != nil {
			if componentAware, ok := logger.(core.ComponentAwareLogger); ok {
				processor.logger = componentAware.WithComponent("framework/orchestration")
			} else {
				processor.logger = logger
			}
		}
		return nil
	}
}

// WithCheckpointExpiryTelemetry supplies the optional provider used to create
// one root span per background expiry scan. A nil provider preserves the
// constructor's no-op default.
func WithCheckpointExpiryTelemetry(provider core.Telemetry) CheckpointExpiryProcessorOption {
	return func(processor *CheckpointExpiryProcessor) error {
		if provider != nil {
			processor.telemetry = provider
		}
		return nil
	}
}

func WithCheckpointExpiryPolicy(policy CheckpointExpiryPolicy) CheckpointExpiryProcessorOption {
	return func(processor *CheckpointExpiryProcessor) error {
		if err := validateCheckpointExpiryPolicy(policy); err != nil {
			return err
		}
		processor.policy = policy
		return nil
	}
}

func NewCheckpointExpiryProcessor(
	persistence CheckpointPersistence,
	source ExpiredCheckpointSource,
	callback ExpiryCallback,
	config ExpiryProcessorConfig,
	options ...CheckpointExpiryProcessorOption,
) (*CheckpointExpiryProcessor, error) {
	if isNilBackendValue(persistence) {
		return nil, fmt.Errorf("orchestration: checkpoint persistence is required")
	}
	if isNilBackendValue(source) {
		return nil, fmt.Errorf("orchestration: expired checkpoint source is required")
	}
	if err := validateExpiryConfig(config); err != nil {
		return nil, fmt.Errorf("invalid expiry processor configuration: %w", err)
	}
	if config.ScanInterval == 0 {
		config.ScanInterval = 10 * time.Second
	}
	if config.BatchSize == 0 {
		config.BatchSize = 100
	}
	if config.DeliverySemantics == "" {
		config.DeliverySemantics = DeliveryAtMostOnce
	}
	processor := &CheckpointExpiryProcessor{
		persistence: persistence,
		source:      source,
		callback:    callback,
		config:      config,
		owner:       generateInstanceID(),
		lease:       defaultCheckpointClaimLease,
		scanTimeout: defaultCheckpointScanTimeout,
		logger:      &core.NoOpLogger{},
		telemetry:   &core.NoOpTelemetry{},
		policy:      DefaultCheckpointExpiryPolicy(),
	}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("orchestration: checkpoint expiry option %d is nil", index)
		}
		if err := option(processor); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(processor.owner) == "" || len(processor.owner) > maxCheckpointClaimOwnerLen {
		return nil, fmt.Errorf("orchestration: checkpoint expiry owner must contain 1-%d characters", maxCheckpointClaimOwnerLen)
	}
	if processor.lease <= 0 {
		return nil, fmt.Errorf("orchestration: checkpoint expiry claim lease must be positive")
	}
	if err := validateCheckpointExpiryPolicy(processor.policy); err != nil {
		return nil, err
	}
	return processor, nil
}

var _ core.Runnable = (*CheckpointExpiryProcessor)(nil)

// Start blocks until ctx is cancelled. A disabled processor performs no work
// but still honors the core.Runnable lifecycle contract.
func (processor *CheckpointExpiryProcessor) Start(ctx context.Context) error {
	if !processor.config.Enabled {
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(processor.config.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			processor.processBatch(ctx)
		}
	}
}

func (processor *CheckpointExpiryProcessor) processBatch(ctx context.Context) {
	start := time.Now()
	scanID := "hitl-expiry-" + uuid.NewString()[:12]
	ctx = telemetry.WithBaggage(ctx, "request_id", scanID)
	ctx = WithRequestID(ctx, scanID)
	spanCtx, span := processor.telemetry.StartSpan(ctx, "hitl.expiry_scan")
	if span == nil {
		span = &core.NoOpSpan{}
	}
	span.SetAttribute("request_id", scanID)
	span.SetAttribute("batch_limit", processor.config.BatchSize)
	status := "success"
	defer func() {
		durationMs := time.Since(start).Milliseconds()
		span.SetAttribute("status", status)
		span.SetAttribute("duration_ms", durationMs)
		span.End()
		RecordExpiryScanDuration(time.Since(start).Seconds())
	}()

	scanCtx, cancel := context.WithTimeout(spanCtx, processor.scanTimeout)
	defer cancel()
	checkpoints, err := processor.source.ClaimExpiredCheckpoints(scanCtx, ExpiredCheckpointClaimRequest{
		Before: time.Now(), Limit: processor.config.BatchSize,
		Owner: processor.owner, Lease: processor.lease,
	})
	if err != nil {
		status = "error"
		RecordExpiryScanSkipped("claim_failed")
		span.RecordError(errors.New("checkpoint expiry claim failed"))
		if processor.logger != nil {
			processor.logger.WarnWithContext(scanCtx, "Failed to claim expired checkpoints", map[string]interface{}{
				"operation": "hitl_expiry_scan", "request_id": scanID,
				"status": "error", "error_type": "claim", "error": safeBackendError(err),
				"duration_ms": time.Since(start).Milliseconds(),
			})
		}
		return
	}
	processed := 0
	failed := 0
	for _, checkpoint := range checkpoints {
		if checkpoint == nil {
			continue
		}
		if !processor.processCheckpoint(scanCtx, checkpoint) {
			status = "partial"
			failed++
			span.RecordError(errors.New("checkpoint expiry processing failed"))
		}
		if err := processor.source.ReleaseExpiredCheckpointClaim(scanCtx, checkpoint.CheckpointID, processor.owner); err != nil {
			status = "partial"
			span.RecordError(errors.New("checkpoint expiry claim release failed"))
			if processor.logger != nil {
				processor.logger.WarnWithContext(scanCtx, "Failed to release expired checkpoint claim", map[string]interface{}{
					"operation": "hitl_expiry_claim_release", "request_id": scanID,
					"checkpoint_id": checkpoint.CheckpointID, "checkpoint_request_id": checkpoint.RequestID,
					"original_request_id": checkpoint.OriginalRequestID, "status": "error",
					"error_type": "claim_release", "error": safeBackendError(err),
					"duration_ms": time.Since(start).Milliseconds(),
				})
			}
		}
		processed++
	}
	RecordExpiryBatchSize(processed)
	span.SetAttribute("claimed_count", len(checkpoints))
	span.SetAttribute("processed_count", processed)
	span.SetAttribute("failed_count", failed)
	if processor.logger != nil {
		fields := map[string]interface{}{
			"operation": "hitl_expiry_scan", "request_id": scanID,
			"status": status, "duration_ms": time.Since(start).Milliseconds(),
			"claimed_count": len(checkpoints), "processed_count": processed,
			"failed_count": failed,
		}
		if processed > 0 || status != "success" {
			processor.logger.InfoWithContext(scanCtx, "Checkpoint expiry scan completed", fields)
		} else {
			processor.logger.DebugWithContext(scanCtx, "Checkpoint expiry scan completed", fields)
		}
	}
}

func (processor *CheckpointExpiryProcessor) processCheckpoint(ctx context.Context, checkpoint *ExecutionCheckpoint) bool {
	originalTraceID := checkpoint.OriginalTraceID
	originalSpanID := checkpoint.OriginalSpanID
	if checkpoint.UserContext != nil {
		if originalTraceID == "" {
			originalTraceID, _ = checkpoint.UserContext["original_trace_id"].(string)
		}
		if originalSpanID == "" {
			originalSpanID, _ = checkpoint.UserContext["original_span_id"].(string)
		}
	}
	ctx, endLinkedSpan := telemetry.StartLinkedSpan(ctx, "hitl.expiry_process", originalTraceID, originalSpanID, map[string]string{
		"checkpoint_id": checkpoint.CheckpointID, "request_id": checkpoint.RequestID,
		"original_request_id": checkpoint.OriginalRequestID, "original_trace_id": originalTraceID,
		"link.type": "hitl_expiry", "trigger": "expiry_processor",
	})
	defer endLinkedSpan()

	mode := effectiveCheckpointRequestMode(checkpoint, processor.policy)
	if checkpoint.RequestMode == "" {
		if processor.logger != nil {
			processor.logger.WarnWithContext(ctx, "RequestMode not set, using default behavior", map[string]interface{}{
				"operation": "hitl_expiry_processor", "checkpoint_id": checkpoint.CheckpointID,
				"request_id": checkpoint.RequestID, "original_request_id": checkpoint.OriginalRequestID,
				"status": "fallback",
				"reason": "request_mode_missing", "default_request_mode": string(mode),
			})
		}
		telemetry.AddSpanEvent(ctx, "hitl.request_mode.default_used",
			attribute.String("request_id", checkpoint.RequestID),
			attribute.String("checkpoint_id", checkpoint.CheckpointID),
			attribute.String("original_request_id", checkpoint.OriginalRequestID),
			attribute.String("default_request_mode", string(mode)),
		)
	}
	action := CommandType("")
	status := CheckpointStatusExpired
	if shouldApplyCheckpointDefault(checkpoint, mode, processor.policy) {
		action = checkpointExpiryAction(checkpoint)
		status = checkpointStatusForExpiryAction(action)
	}
	telemetry.AddSpanEvent(ctx, "hitl.checkpoint.expired",
		attribute.String("request_id", checkpoint.RequestID),
		attribute.String("checkpoint_id", checkpoint.CheckpointID),
		attribute.String("original_request_id", checkpoint.OriginalRequestID),
		attribute.String("request_mode", string(mode)),
		attribute.String("action", string(action)),
		attribute.String("new_status", string(status)),
	)
	metricAction := string(action)
	if metricAction == "" {
		metricAction = "implicit_deny"
	}
	RecordCheckpointExpired(metricAction, string(mode), checkpoint.InterruptPoint)
	if processor.logger != nil {
		processor.logger.InfoWithContext(ctx, "Checkpoint expired", map[string]interface{}{
			"operation": "hitl_expiry_processor", "checkpoint_id": checkpoint.CheckpointID,
			"request_id": checkpoint.RequestID, "original_request_id": checkpoint.OriginalRequestID,
			"status": "success", "request_mode": string(mode),
			"action": metricAction, "new_status": string(status),
		})
	}

	callbackCheckpoint := *checkpoint
	callbackCheckpoint.Status = status
	if processor.config.DeliverySemantics == DeliveryAtLeastOnce {
		if processor.callback != nil && !processor.invokeCallback(ctx, &callbackCheckpoint, action) {
			return false
		}
		if !processor.updateStatus(ctx, checkpoint, status) {
			return false
		}
		telemetry.SetSpanAttributes(ctx, attribute.String("status", "success"))
		return true
	}
	if !processor.updateStatus(ctx, checkpoint, status) {
		return false
	}
	if processor.callback != nil && !processor.invokeCallback(ctx, &callbackCheckpoint, action) {
		return false
	}
	telemetry.SetSpanAttributes(ctx, attribute.String("status", "success"))
	return true
}

func (processor *CheckpointExpiryProcessor) updateStatus(ctx context.Context, checkpoint *ExecutionCheckpoint, status CheckpointStatus) bool {
	if err := processor.persistence.UpdateCheckpointStatus(ctx, checkpoint.CheckpointID, status); err != nil {
		telemetry.SetSpanAttributes(ctx,
			attribute.String("status", "error"),
			attribute.String("error_type", "store_write"),
		)
		telemetry.RecordSpanError(ctx, errors.New("checkpoint expiry status update failed"))
		if processor.logger != nil {
			processor.logger.WarnWithContext(ctx, "Failed to update expired checkpoint", map[string]interface{}{
				"operation": "hitl_expiry_processor", "checkpoint_id": checkpoint.CheckpointID,
				"request_id": checkpoint.RequestID, "original_request_id": checkpoint.OriginalRequestID,
				"status":     "error",
				"error_type": "store_write", "error": safeBackendError(err),
			})
		}
		return false
	}
	return true
}

func (processor *CheckpointExpiryProcessor) invokeCallback(ctx context.Context, checkpoint *ExecutionCheckpoint, action CommandType) (success bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			success = false
			RecordCallbackPanic()
			telemetry.SetSpanAttributes(ctx,
				attribute.String("status", "error"),
				attribute.String("error_type", "callback_panic"),
			)
			telemetry.RecordSpanError(ctx, errors.New("checkpoint expiry callback panicked"))
			if processor.logger != nil {
				processor.logger.ErrorWithContext(ctx, "Expiry callback panicked", map[string]interface{}{
					"operation": "hitl_expiry_callback", "checkpoint_id": checkpoint.CheckpointID,
					"request_id": checkpoint.RequestID, "original_request_id": checkpoint.OriginalRequestID,
					"status":     "error",
					"error_type": "callback_panic", "error": "expiry callback panicked",
				})
			}
		}
	}()
	processor.callback(ctx, checkpoint, action)
	return true
}

func effectiveCheckpointRequestMode(checkpoint *ExecutionCheckpoint, policy CheckpointExpiryPolicy) RequestMode {
	if checkpoint.RequestMode != "" {
		return checkpoint.RequestMode
	}
	if checkpoint.Decision != nil && checkpoint.Decision.DefaultRequestMode != "" {
		return checkpoint.Decision.DefaultRequestMode
	}
	return policy.DefaultRequestMode
}

func shouldApplyCheckpointDefault(checkpoint *ExecutionCheckpoint, mode RequestMode, policy CheckpointExpiryPolicy) bool {
	if mode == RequestModeStreaming {
		if checkpoint.Decision != nil && checkpoint.Decision.StreamingExpiryBehavior != "" {
			return checkpoint.Decision.StreamingExpiryBehavior == StreamingExpiryApplyDefault
		}
		return policy.StreamingBehavior == StreamingExpiryApplyDefault
	}
	if checkpoint.Decision != nil && checkpoint.Decision.NonStreamingExpiryBehavior != "" {
		return checkpoint.Decision.NonStreamingExpiryBehavior == NonStreamingExpiryApplyDefault
	}
	return policy.NonStreamingBehavior == NonStreamingExpiryApplyDefault
}

func validateCheckpointExpiryPolicy(policy CheckpointExpiryPolicy) error {
	if policy.DefaultRequestMode != RequestModeStreaming && policy.DefaultRequestMode != RequestModeNonStreaming {
		return fmt.Errorf("orchestration: invalid default checkpoint request mode %q", policy.DefaultRequestMode)
	}
	if policy.StreamingBehavior != StreamingExpiryApplyDefault && policy.StreamingBehavior != StreamingExpiryImplicitDeny {
		return fmt.Errorf("orchestration: invalid streaming checkpoint expiry behavior %q", policy.StreamingBehavior)
	}
	if policy.NonStreamingBehavior != NonStreamingExpiryApplyDefault && policy.NonStreamingBehavior != NonStreamingExpiryImplicitDeny {
		return fmt.Errorf("orchestration: invalid non-streaming checkpoint expiry behavior %q", policy.NonStreamingBehavior)
	}
	return nil
}

func checkpointExpiryAction(checkpoint *ExecutionCheckpoint) CommandType {
	if checkpoint.Decision != nil && checkpoint.Decision.DefaultAction != "" {
		return checkpoint.Decision.DefaultAction
	}
	if checkpoint.InterruptPoint == InterruptPointOnError {
		return CommandAbort
	}
	return CommandReject
}

func checkpointStatusForExpiryAction(action CommandType) CheckpointStatus {
	switch action {
	case CommandApprove:
		return CheckpointStatusExpiredApproved
	case CommandAbort:
		return CheckpointStatusExpiredAborted
	default:
		return CheckpointStatusExpiredRejected
	}
}

func safeBackendError(err error) string {
	if err == nil {
		return ""
	}
	return "backend operation failed"
}
