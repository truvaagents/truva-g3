package orchestration

import (
	"fmt"

	"github.com/truvaagents/truva-g3/telemetry"
)

// =============================================================================
// HITL Metrics - Following Framework Telemetry Patterns
// =============================================================================
//
// Per DISTRIBUTED_TRACING_GUIDE.md and existing executor.go patterns:
// - All counters use telemetry.Counter with "module" label
// - All histograms use telemetry.Histogram with standard buckets
// - Span events use hitl.{operation}.{status} naming pattern
//
// =============================================================================

// Metric names following framework conventions
const (
	// Counters
	MetricCheckpointCreated  = "orchestration.hitl.checkpoint_created_total"
	MetricCheckpointStatus   = "orchestration.hitl.checkpoint_status_total"
	MetricCommandProcessed   = "orchestration.hitl.command_processed_total"
	MetricWebhookSent        = "orchestration.hitl.webhook_sent_total"
	MetricCommandPublished   = "orchestration.hitl.command_published_total"
	MetricNotificationFailed = "orchestration.hitl.notification_failed_total"

	// Expiry processor counters
	MetricCheckpointExpired = "orchestration.hitl.checkpoint_expired_total"
	MetricExpiryScanSkipped = "orchestration.hitl.expiry_scan_skipped_total"
	MetricCallbackPanic     = "orchestration.hitl.callback_panic_total"
	MetricClaimSuccess      = "orchestration.hitl.claim_success_total"
	MetricClaimSkipped      = "orchestration.hitl.claim_skipped_total"

	// Histograms
	MetricApprovalLatency = "orchestration.hitl.approval_latency_seconds"
	MetricWebhookDuration = "orchestration.hitl.webhook_duration_seconds"

	// Expiry processor histograms/gauges
	MetricExpiryScanDuration = "orchestration.hitl.expiry_scan_duration_seconds"
	MetricExpiryBatchSize    = "orchestration.hitl.expiry_batch_size"
)

// =============================================================================
// Counter Helper Functions
// =============================================================================

// RecordCheckpointCreated records when a new checkpoint is created.
// Labels: interrupt_point, reason, module
func RecordCheckpointCreated(interruptPoint InterruptPoint, reason InterruptReason) {
	telemetry.Counter(MetricCheckpointCreated,
		"interrupt_point", string(interruptPoint),
		"reason", string(reason),
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordCheckpointStatus records checkpoint status transitions.
// Labels: from_status, to_status, module
func RecordCheckpointStatus(fromStatus, toStatus CheckpointStatus) {
	telemetry.Counter(MetricCheckpointStatus,
		"from_status", string(fromStatus),
		"to_status", string(toStatus),
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordCommandProcessed records when a command is processed.
// Labels: command_type, success, module
func RecordCommandProcessed(commandType CommandType, success bool) {
	telemetry.Counter(MetricCommandProcessed,
		"command_type", string(commandType),
		"success", fmt.Sprintf("%t", success),
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordWebhookSent records when a webhook notification is sent.
// Labels: success, status_code, module
func RecordWebhookSent(success bool, statusCode int) {
	telemetry.Counter(MetricWebhookSent,
		"success", fmt.Sprintf("%t", success),
		"status_code", fmt.Sprintf("%d", statusCode),
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordCommandPublished records when a command is published via pub/sub.
// Labels: command_type, module
func RecordCommandPublished(commandType CommandType) {
	telemetry.Counter(MetricCommandPublished,
		"command_type", string(commandType),
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordNotificationFailed records when a notification fails.
// Labels: reason, module
func RecordNotificationFailed(reason InterruptReason) {
	telemetry.Counter(MetricNotificationFailed,
		"reason", string(reason),
		"module", telemetry.ModuleOrchestration,
	)
}

// =============================================================================
// Histogram Helper Functions
// =============================================================================

// RecordApprovalLatency records the time between checkpoint creation and command.
// This measures how long humans take to respond to approval requests.
// Labels: command_type, module
func RecordApprovalLatency(latencySeconds float64, commandType CommandType) {
	telemetry.Histogram(MetricApprovalLatency,
		latencySeconds,
		"command_type", string(commandType),
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordWebhookDuration records webhook request duration.
// This measures the latency of webhook delivery.
// Labels: success, module
func RecordWebhookDuration(durationSeconds float64, success bool) {
	telemetry.Histogram(MetricWebhookDuration,
		durationSeconds,
		"success", fmt.Sprintf("%t", success),
		"module", telemetry.ModuleOrchestration,
	)
}

// =============================================================================
// Expiry Processor Helper Functions
// =============================================================================

// RecordCheckpointExpired records when a checkpoint is auto-processed on expiry.
// Labels: action (approve, reject, abort, or empty string for implicit_deny), request_mode, interrupt_point, module
func RecordCheckpointExpired(action string, requestMode string, interruptPoint InterruptPoint) {
	telemetry.Counter(MetricCheckpointExpired,
		"action", action,
		"request_mode", requestMode,
		"interrupt_point", string(interruptPoint),
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordExpiryScanDuration records time taken for each expiry scan.
// Labels: module
func RecordExpiryScanDuration(durationSeconds float64) {
	telemetry.Histogram(MetricExpiryScanDuration, durationSeconds,
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordExpiryBatchSize records number of checkpoints processed per scan.
// Labels: module
func RecordExpiryBatchSize(count int) {
	telemetry.Gauge(MetricExpiryBatchSize, float64(count),
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordExpiryScanSkipped records when an expiry scan is skipped due to errors.
// Labels: reason, module
func RecordExpiryScanSkipped(reason string) {
	telemetry.Counter(MetricExpiryScanSkipped,
		"reason", reason,
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordCallbackPanic records when a callback panics during expiry processing.
// Note: checkpoint_id is NOT included as a label to avoid high cardinality.
// Use structured logging to capture checkpoint_id for debugging.
// Labels: module
func RecordCallbackPanic() {
	telemetry.Counter(MetricCallbackPanic,
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordClaimSuccess records when this instance successfully claims an expired checkpoint.
// This means this pod will process the checkpoint (no other pod claimed it first).
// Labels: module
func RecordClaimSuccess() {
	telemetry.Counter(MetricClaimSuccess,
		"module", telemetry.ModuleOrchestration,
	)
}

// RecordClaimSkipped records when an expired checkpoint was already claimed by another instance.
// This is normal in multi-pod deployments and indicates proper distributed coordination.
// Labels: module
func RecordClaimSkipped() {
	telemetry.Counter(MetricClaimSkipped,
		"module", telemetry.ModuleOrchestration,
	)
}
