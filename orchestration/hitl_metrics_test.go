package orchestration

import (
	"testing"
)

// =============================================================================
// HITL Metrics Tests
// =============================================================================
//
// These tests verify that the metric helper functions are callable and don't panic.
// The actual metric emission is handled by the telemetry package, which is tested separately.
//
// =============================================================================

func TestMetricConstants(t *testing.T) {
	// Verify metric constants are defined and follow naming conventions
	constants := map[string]string{
		"MetricCheckpointCreated":  MetricCheckpointCreated,
		"MetricCheckpointStatus":   MetricCheckpointStatus,
		"MetricCommandProcessed":   MetricCommandProcessed,
		"MetricWebhookSent":        MetricWebhookSent,
		"MetricCommandPublished":   MetricCommandPublished,
		"MetricNotificationFailed": MetricNotificationFailed,
		"MetricApprovalLatency":    MetricApprovalLatency,
		"MetricWebhookDuration":    MetricWebhookDuration,
		// Expiry processor metrics
		"MetricCheckpointExpired":  MetricCheckpointExpired,
		"MetricExpiryScanSkipped":  MetricExpiryScanSkipped,
		"MetricCallbackPanic":      MetricCallbackPanic,
		"MetricExpiryScanDuration": MetricExpiryScanDuration,
		"MetricExpiryBatchSize":    MetricExpiryBatchSize,
		// Claim mechanism metrics (distributed concurrency)
		"MetricClaimSuccess": MetricClaimSuccess,
		"MetricClaimSkipped": MetricClaimSkipped,
	}

	for name, value := range constants {
		if value == "" {
			t.Errorf("Metric constant %s should not be empty", name)
		}
		// Verify naming convention: orchestration.hitl.*
		if len(value) < 20 {
			t.Errorf("Metric constant %s has unexpectedly short value: %s", name, value)
		}
	}
}

func TestRecordCheckpointCreated(t *testing.T) {
	// Should not panic
	testCases := []struct {
		name           string
		interruptPoint InterruptPoint
		reason         InterruptReason
	}{
		{
			name:           "plan generated interrupt",
			interruptPoint: InterruptPointPlanGenerated,
			reason:         ReasonPlanApproval,
		},
		{
			name:           "before step interrupt",
			interruptPoint: InterruptPointBeforeStep,
			reason:         ReasonSensitiveOperation,
		},
		{
			name:           "after step interrupt",
			interruptPoint: InterruptPointAfterStep,
			reason:         ReasonOutputValidation,
		},
		{
			name:           "on error interrupt",
			interruptPoint: InterruptPointOnError,
			reason:         ReasonEscalation,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			RecordCheckpointCreated(tc.interruptPoint, tc.reason)
		})
	}
}

func TestRecordCheckpointStatus(t *testing.T) {
	testCases := []struct {
		name       string
		fromStatus CheckpointStatus
		toStatus   CheckpointStatus
	}{
		{
			name:       "pending to approved",
			fromStatus: CheckpointStatusPending,
			toStatus:   CheckpointStatusApproved,
		},
		{
			name:       "pending to rejected",
			fromStatus: CheckpointStatusPending,
			toStatus:   CheckpointStatusRejected,
		},
		{
			name:       "pending to expired",
			fromStatus: CheckpointStatusPending,
			toStatus:   CheckpointStatusExpired,
		},
		{
			name:       "approved to completed",
			fromStatus: CheckpointStatusApproved,
			toStatus:   CheckpointStatusCompleted,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			RecordCheckpointStatus(tc.fromStatus, tc.toStatus)
		})
	}
}

func TestRecordCommandProcessed(t *testing.T) {
	testCases := []struct {
		name        string
		commandType CommandType
		success     bool
	}{
		{
			name:        "approve success",
			commandType: CommandApprove,
			success:     true,
		},
		{
			name:        "reject success",
			commandType: CommandReject,
			success:     true,
		},
		{
			name:        "edit success",
			commandType: CommandEdit,
			success:     true,
		},
		{
			name:        "abort success",
			commandType: CommandAbort,
			success:     true,
		},
		{
			name:        "skip success",
			commandType: CommandSkip,
			success:     true,
		},
		{
			name:        "retry success",
			commandType: CommandRetry,
			success:     true,
		},
		{
			name:        "command failure",
			commandType: CommandApprove,
			success:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			RecordCommandProcessed(tc.commandType, tc.success)
		})
	}
}

func TestRecordWebhookSent(t *testing.T) {
	testCases := []struct {
		name       string
		success    bool
		statusCode int
	}{
		{
			name:       "success 200",
			success:    true,
			statusCode: 200,
		},
		{
			name:       "success 201",
			success:    true,
			statusCode: 201,
		},
		{
			name:       "success 204",
			success:    true,
			statusCode: 204,
		},
		{
			name:       "failure 400",
			success:    false,
			statusCode: 400,
		},
		{
			name:       "failure 500",
			success:    false,
			statusCode: 500,
		},
		{
			name:       "connection error",
			success:    false,
			statusCode: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			RecordWebhookSent(tc.success, tc.statusCode)
		})
	}
}

func TestRecordCommandPublished(t *testing.T) {
	commandTypes := []CommandType{
		CommandApprove,
		CommandReject,
		CommandEdit,
		CommandSkip,
		CommandAbort,
		CommandRetry,
		CommandRespond,
	}

	for _, ct := range commandTypes {
		t.Run(string(ct), func(t *testing.T) {
			// Should not panic
			RecordCommandPublished(ct)
		})
	}
}

func TestRecordNotificationFailed(t *testing.T) {
	reasons := []InterruptReason{
		ReasonPlanApproval,
		ReasonSensitiveOperation,
		ReasonOutputValidation,
		ReasonEscalation,
		ReasonContextGathering,
		ReasonCustom,
	}

	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			// Should not panic
			RecordNotificationFailed(reason)
		})
	}
}

func TestRecordApprovalLatency(t *testing.T) {
	testCases := []struct {
		name        string
		latency     float64
		commandType CommandType
	}{
		{
			name:        "fast approval",
			latency:     0.5,
			commandType: CommandApprove,
		},
		{
			name:        "slow approval",
			latency:     120.0,
			commandType: CommandApprove,
		},
		{
			name:        "rejection",
			latency:     5.0,
			commandType: CommandReject,
		},
		{
			name:        "edit",
			latency:     30.0,
			commandType: CommandEdit,
		},
		{
			name:        "zero latency",
			latency:     0.0,
			commandType: CommandApprove,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			RecordApprovalLatency(tc.latency, tc.commandType)
		})
	}
}

func TestRecordWebhookDuration(t *testing.T) {
	testCases := []struct {
		name     string
		duration float64
		success  bool
	}{
		{
			name:     "fast success",
			duration: 0.05,
			success:  true,
		},
		{
			name:     "slow success",
			duration: 5.0,
			success:  true,
		},
		{
			name:     "fast failure",
			duration: 0.01,
			success:  false,
		},
		{
			name:     "timeout failure",
			duration: 30.0,
			success:  false,
		},
		{
			name:     "zero duration",
			duration: 0.0,
			success:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			RecordWebhookDuration(tc.duration, tc.success)
		})
	}
}

// =============================================================================
// Expiry Processor Metrics Tests
// =============================================================================

func TestRecordCheckpointExpired(t *testing.T) {
	testCases := []struct {
		name           string
		action         string
		requestMode    string
		interruptPoint InterruptPoint
	}{
		{
			name:           "implicit deny streaming",
			action:         "",
			requestMode:    "streaming",
			interruptPoint: InterruptPointPlanGenerated,
		},
		{
			name:           "approve non-streaming",
			action:         "approve",
			requestMode:    "non_streaming",
			interruptPoint: InterruptPointBeforeStep,
		},
		{
			name:           "reject streaming",
			action:         "reject",
			requestMode:    "streaming",
			interruptPoint: InterruptPointAfterStep,
		},
		{
			name:           "abort non-streaming",
			action:         "abort",
			requestMode:    "non_streaming",
			interruptPoint: InterruptPointOnError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			RecordCheckpointExpired(tc.action, tc.requestMode, tc.interruptPoint)
		})
	}
}

func TestRecordExpiryScanDuration(t *testing.T) {
	testCases := []struct {
		name     string
		duration float64
	}{
		{
			name:     "fast scan",
			duration: 0.001,
		},
		{
			name:     "normal scan",
			duration: 0.5,
		},
		{
			name:     "slow scan",
			duration: 5.0,
		},
		{
			name:     "zero duration",
			duration: 0.0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			RecordExpiryScanDuration(tc.duration)
		})
	}
}

func TestRecordExpiryBatchSize(t *testing.T) {
	testCases := []struct {
		name  string
		count int
	}{
		{
			name:  "no checkpoints",
			count: 0,
		},
		{
			name:  "single checkpoint",
			count: 1,
		},
		{
			name:  "small batch",
			count: 10,
		},
		{
			name:  "large batch",
			count: 100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			RecordExpiryBatchSize(tc.count)
		})
	}
}

func TestRecordExpiryScanSkipped(t *testing.T) {
	reasons := []string{
		"read_pending_index_failed",
		"context_canceled",
		"store_unavailable",
	}

	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			// Should not panic
			RecordExpiryScanSkipped(reason)
		})
	}
}

func TestRecordCallbackPanic(t *testing.T) {
	// Should not panic
	RecordCallbackPanic()
}

func TestRecordClaimSuccess(t *testing.T) {
	// Should not panic
	RecordClaimSuccess()
}

func TestRecordClaimSkipped(t *testing.T) {
	// Should not panic
	RecordClaimSkipped()
}
